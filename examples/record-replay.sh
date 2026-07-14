#!/usr/bin/env bash
# Runnable pathshim demo: fabricates fake `git` and `kubectl` binaries,
# records a deploy script that calls them, then deletes the fakes and
# replays the same script entirely from the cassette. Everything happens in
# a temp dir; nothing touches your real tools and nothing goes online.
#
# Usage:  go build -o pathshim ./cmd/pathshim
#         bash examples/record-replay.sh
set -euo pipefail

PATHSHIM="${PATHSHIM:-$(pwd)/pathshim}"
[ -x "$PATHSHIM" ] || { echo "build pathshim first: go build -o pathshim ./cmd/pathshim"; exit 1; }

DEMO="$(mktemp -d)"
trap 'rm -rf "$DEMO"' EXIT
mkdir -p "$DEMO/tools"

# --- fake external tools (stand-ins for the real git / kubectl) ----------
cat > "$DEMO/tools/git" <<'EOF'
#!/bin/sh
echo "deadbee"
EOF
cat > "$DEMO/tools/kubectl" <<'EOF'
#!/bin/sh
cat > /dev/null
echo "deployment.apps/app configured"
echo "Warning: using default context" >&2
EOF
chmod +x "$DEMO/tools/git" "$DEMO/tools/kubectl"

# --- the script under test ------------------------------------------------
cat > "$DEMO/deploy.sh" <<'EOF'
#!/bin/sh
sha="$(git rev-parse --short HEAD)"
echo "deploying $sha"
printf 'kind: Deployment\n' | kubectl apply -f -
EOF

echo "== record: real tools run, every call is captured =="
PATH="$DEMO/tools:$PATH" "$PATHSHIM" record \
  --cassette "$DEMO/deploy.json" --cmd git,kubectl -- sh "$DEMO/deploy.sh"

echo
echo "== the fake tools are now gone =="
rm "$DEMO/tools/git" "$DEMO/tools/kubectl"

echo
echo "== replay: same script, answers come from the cassette =="
"$PATHSHIM" replay --cassette "$DEMO/deploy.json" --require-all -- sh "$DEMO/deploy.sh"

echo
echo "== what got recorded =="
"$PATHSHIM" inspect "$DEMO/deploy.json"
