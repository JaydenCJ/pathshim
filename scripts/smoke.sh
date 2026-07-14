#!/usr/bin/env bash
# End-to-end smoke test for pathshim: builds the binary, fabricates fake
# external tools (git/kubectl stand-ins), records a deploy script through
# PATH shims, then replays it with the fake tools deleted — asserting on
# real CLI output at every step. No network, idempotent, finishes in
# seconds.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT

fail() {
  echo "SMOKE FAIL: $*" >&2
  exit 1
}

BIN="$WORKDIR/pathshim"
TOOLS="$WORKDIR/tools"
CASSETTE="$WORKDIR/deploy.json"
export PATHSHIM_FIXED_TIME="2026-07-13T00:00:00Z"

echo "1. build"
(cd "$ROOT" && go build -o "$BIN" ./cmd/pathshim) || fail "go build failed"

echo "2. version matches manifest"
"$BIN" --version | grep -qx "pathshim 0.1.0" || fail "--version mismatch"

echo "3. fabricate fake external tools and a deploy script"
mkdir -p "$TOOLS"
cat > "$TOOLS/git" <<'EOF'
#!/bin/sh
echo "deadbee"
EOF
cat > "$TOOLS/kubectl" <<'EOF'
#!/bin/sh
cat > /dev/null
echo "deployment.apps/app configured"
echo "Warning: using default context" >&2
echo "token=tok_s3cret"
EOF
chmod +x "$TOOLS/git" "$TOOLS/kubectl"
cat > "$WORKDIR/deploy.sh" <<'EOF'
#!/bin/sh
sha="$(git rev-parse --short HEAD)"
echo "deploying $sha"
printf 'kind: Deployment\n' | kubectl apply -f -
EOF

echo "4. record the script through PATH shims (with redaction)"
OUT="$(PATH="$TOOLS:$PATH" "$BIN" record --cassette "$CASSETTE" \
  --cmd git,kubectl --redact 'tok_[a-z0-9]+' -- sh "$WORKDIR/deploy.sh" 2>&1)"
echo "$OUT" | grep -q "deploying deadbee" || fail "live output missing"
echo "$OUT" | grep -q "recorded 2 interaction(s) for 2 command(s)" || fail "record summary missing"

echo "5. cassette verifies, is redacted, and inspects"
"$BIN" verify "$CASSETTE" | grep -q "OK — format 1, 2 interaction(s), 2 command(s)" \
  || fail "verify failed"
grep -q "tok_s3cret" "$CASSETTE" && fail "secret leaked into the cassette"
grep -q "\[REDACTED\]" "$CASSETTE" || fail "redaction placeholder missing"
"$BIN" inspect "$CASSETTE" | grep -q "git rev-parse --short HEAD" || fail "inspect missing argv"

echo "6. replay offline with the fake tools deleted"
rm -f "$TOOLS/git" "$TOOLS/kubectl"
OUT="$("$BIN" replay --cassette "$CASSETTE" --require-all -- sh "$WORKDIR/deploy.sh" 2>&1)" \
  || fail "replay exited non-zero"
echo "$OUT" | grep -q "deploying deadbee" || fail "replayed stdout wrong"
echo "$OUT" | grep -q "Warning: using default context" || fail "replayed stderr missing"
echo "$OUT" | grep -q "replayed 2/2 interaction(s), 0 miss(es)" || fail "replay summary missing"

echo "7. a call the cassette does not cover fails the session"
set +e
"$BIN" replay --cassette "$CASSETTE" -- git push --force >/dev/null 2>"$WORKDIR/miss.log"
CODE=$?
set -e
[ "$CODE" -eq 51 ] || fail "miss should exit 51, got $CODE"
grep -q "wanted: git push --force" "$WORKDIR/miss.log" || fail "miss diagnosis missing"

echo "8. exit codes replay faithfully"
mkdir -p "$TOOLS"
printf '#!/bin/sh\necho rejected >&2\nexit 3\n' > "$TOOLS/git"
chmod +x "$TOOLS/git"
set +e
PATH="$TOOLS:$PATH" "$BIN" record --cassette "$WORKDIR/fail.json" --cmd git -- git push >/dev/null 2>&1
[ $? -eq 3 ] || fail "record should propagate exit 3"
"$BIN" replay --cassette "$WORKDIR/fail.json" -- git push >/dev/null 2>&1
[ $? -eq 3 ] || fail "replay should reproduce exit 3"
set -e

echo "9. usage errors exit 2"
set +e
"$BIN" record --cmd git -- true >/dev/null 2>&1
[ $? -eq 2 ] || fail "missing --cassette should exit 2"
set -e

echo "SMOKE OK"
