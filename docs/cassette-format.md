# Cassette format (v1)

A cassette is a single JSON document written by `pathshim record` and read
by `pathshim replay`, `inspect`, and `verify`. It is designed to be
committed to version control: pretty-printed, stable field order, text
bodies stored readably, and secrets removable at record time via
`--redact`.

## Top level

| Key | Type | Meaning |
|---|---|---|
| `pathshim_cassette` | int | Schema version; this build reads/writes `1` and rejects anything else. |
| `version` | string | The pathshim release that recorded the cassette. |
| `recorded_at` | string | RFC 3339 UTC timestamp. Pin it with `PATHSHIM_FIXED_TIME` for byte-reproducible recordings. |
| `command` | string[] | The wrapped command line, informational only. |
| `interactions` | object[] | One entry per intercepted call, in completion order. |

## Interaction

| Key | Type | Meaning |
|---|---|---|
| `command` | string | Bare shim name (`git`, never `/usr/bin/git`). |
| `args` | string[] | Exact argv the tool received. Replay matches on `command` + `args`. |
| `env` | object | Values of the variables named with `--env`, captured at call time. Unset variables are absent (not empty), so `--match-env` can tell the difference. |
| `stdin` | body? | What the tool actually consumed from a non-terminal stdin. Absent when stdin was a terminal. |
| `stdout` / `stderr` | body | Captured output streams. |
| `exit_code` | int | Tool exit status; signal deaths are `128 + signal`, as a shell reports them. |
| `duration_ms` | int | Wall time of the real call. Informational; never matched on. |
| `truncated` | bool | Present and `true` when any stream hit `--max-capture`. `verify` warns, because replaying a truncated body is lossy. |

## Body encoding

A body is `{"text": "...", "bytes": N}` when the content is valid UTF-8
with no control characters beyond `\n`, `\r`, `\t` — readable and
diff-friendly. Anything else (binary output, ANSI color sequences) becomes
`{"base64": "...", "bytes": N}` so no byte is ever hidden inside a "text"
field. `bytes` always holds the decoded length; `verify` cross-checks it.

## Session environment protocol

The parent process configures shims through `PATHSHIM_*` variables. They
are internal plumbing (subject to change between minor versions), listed
here for debugging:

| Variable | Set during | Effect |
|---|---|---|
| `PATHSHIM_MODE` | both | `record` or `replay`; selects shim behavior. |
| `PATHSHIM_SHIM_DIR` | both | Shim directory, skipped when resolving the real tool. |
| `PATHSHIM_LOG` | record | Journal file each shim appends to (JSON lines, flock-serialized). |
| `PATHSHIM_CASSETTE` / `PATHSHIM_STATE` | replay | Cassette to answer from and the shared consumption state. |
| `PATHSHIM_ERRFILE` | both | Where shims report internal failures so the parent exits 3. |
| `PATHSHIM_ENV_KEYS`, `PATHSHIM_REDACT`, `PATHSHIM_MAX_CAPTURE` | record | Carry `--env`, `--redact` (JSON-encoded), `--max-capture`. |
| `PATHSHIM_ORDERED`, `PATHSHIM_MATCH_STDIN`, `PATHSHIM_MATCH_ENV`, `PATHSHIM_ON_MISS` | replay | Carry the matching flags. |
| `PATHSHIM_FIXED_TIME` | record | Pins `recorded_at` for reproducible cassettes. |

## Concurrency model

Shims are independent processes, so both phases serialize shared mutations
with `flock(2)`: record appends journal lines under a lock, and replay
performs find-match/mark-consumed as one critical section on the state
file. Parallel invocations (`make -j`, backgrounded pipelines) therefore
record every call and never double-serve a recording.

## Compatibility promise

Within format 1, fields are only ever added, never renamed or re-typed.
A future format 2 will bump `pathshim_cassette`, and older builds will
refuse it with an explicit error instead of misreading it.
