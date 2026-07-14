# Contributing to pathshim

Issues, discussions and pull requests are all welcome.

## Getting started

You need Go ≥1.22 on Linux or macOS (shims are symlinks and locking uses
flock, so Windows is out of scope for now); nothing else.

```bash
git clone https://github.com/JaydenCJ/pathshim && cd pathshim
go build ./...
go test ./...
bash scripts/smoke.sh
```

`scripts/smoke.sh` builds the binary, fabricates fake git/kubectl tools in
a temp dir, records a deploy script through PATH shims, deletes the fakes,
and replays the script offline, asserting on real CLI output at every
step; it must finish by printing `SMOKE OK`.

## Before you open a pull request

1. `gofmt -l .` reports nothing (formatting is enforced).
2. `go vet ./...` passes with no findings.
3. `go test ./...` passes (90 deterministic tests, no network).
4. `bash scripts/smoke.sh` prints `SMOKE OK`.
5. Add tests for behavior changes; keep logic in pure, unit-testable
   modules (matching and cassette encoding never touch the filesystem —
   only the session, shim, and CLI layers do).

## Ground rules

- Keep dependencies at zero; adding one needs strong justification in the PR.
- No network calls, ever — pathshim's only external interface is executing
  local binaries the user pointed it at. No telemetry.
- The cassette format is a public contract: within format 1, fields may be
  added but never renamed or re-typed, and every schema change needs a row
  in `docs/cassette-format.md` plus a load/validate test.
- Determinism first: replay decisions may depend only on the cassette, the
  consumed-state, and the incoming call — never on the clock or the host.
- Code comments and doc comments are written in English.

## Reporting bugs

Include the output of `pathshim version`, the full record and replay
commands you ran, the session summary lines (`pathshim: recorded …` /
`pathshim: replayed …`), and — for matching bugs — the cassette (or the
relevant `pathshim inspect --index N` output, redacted if needed), since
argv + recorded interactions are exactly what the matcher sees.

## Security

Please do not open public issues for security problems; use GitHub's
private vulnerability reporting on this repository instead.
