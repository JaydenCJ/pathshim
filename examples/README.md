# pathshim examples

One runnable script, offline and self-contained.

## record-replay.sh

The full loop in ~50 lines: fabricate fake `git` and `kubectl` stand-ins,
record a deploy script that calls both (including a pipe into kubectl's
stdin), delete the fakes, and replay the script byte-for-byte from the
cassette — proving the replay needs nothing but the JSON file.

```bash
go build -o pathshim ./cmd/pathshim
bash examples/record-replay.sh
```

Everything runs in a `mktemp -d` sandbox that is removed on exit; your real
git and kubectl are never touched. The script pins no timestamps, so the
cassette it writes will carry the current time — set
`PATHSHIM_FIXED_TIME=2026-07-13T00:00:00Z` first if you want byte-identical
reruns.

For a cassette you can commit to a test suite, add `--redact` patterns for
anything secret and check the result with `pathshim verify` — see the
[cassette format notes](../docs/cassette-format.md).
