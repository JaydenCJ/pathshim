// Environment protocol between the parent `pathshim record|replay` process
// and the shim processes it spawns indirectly. Every variable is documented
// in docs/cassette-format.md; they are internal plumbing, not public API.
package session

// Environment variable names.
const (
	// EnvMode selects shim behavior: ModeRecord or ModeReplay.
	EnvMode = "PATHSHIM_MODE"
	// EnvShimDir is the directory holding the shim symlinks; record-mode
	// shims skip it when resolving the real executable.
	EnvShimDir = "PATHSHIM_SHIM_DIR"
	// EnvLog is the record journal path (JSON lines, one per interaction).
	EnvLog = "PATHSHIM_LOG"
	// EnvErrFile collects shim-side failures so the parent can fail the
	// whole session even when the wrapped command swallowed exit codes.
	EnvErrFile = "PATHSHIM_ERRFILE"
	// EnvCassette is the cassette path replay shims read.
	EnvCassette = "PATHSHIM_CASSETTE"
	// EnvState is the replay consumption-state path.
	EnvState = "PATHSHIM_STATE"
	// EnvEnvKeys is a comma-separated list of environment variables to
	// capture per interaction at record time.
	EnvEnvKeys = "PATHSHIM_ENV_KEYS"
	// EnvRedact is the JSON-encoded list of redaction patterns.
	EnvRedact = "PATHSHIM_REDACT"
	// EnvMaxCapture caps captured bytes per stream (decimal).
	EnvMaxCapture = "PATHSHIM_MAX_CAPTURE"
	// EnvOnMiss selects replay miss behavior: fail, passthrough, or empty.
	EnvOnMiss = "PATHSHIM_ON_MISS"
	// EnvOrdered ("1") enforces recorded call order at replay.
	EnvOrdered = "PATHSHIM_ORDERED"
	// EnvMatchStdin ("1") makes replay matching compare drained stdin.
	EnvMatchStdin = "PATHSHIM_MATCH_STDIN"
	// EnvMatchEnv ("1") makes replay matching compare recorded env vars.
	EnvMatchEnv = "PATHSHIM_MATCH_ENV"
	// EnvFixedTime pins the cassette's recorded_at stamp (RFC 3339) so a
	// recording can be byte-reproducible; used by tests and the docs.
	EnvFixedTime = "PATHSHIM_FIXED_TIME"
)

// Mode values.
const (
	ModeRecord = "record"
	ModeReplay = "replay"
)

// Miss policies.
const (
	OnMissFail        = "fail"
	OnMissPassthrough = "passthrough"
	OnMissEmpty       = "empty"
)

// DefaultMaxCapture is the per-stream capture cap (1 MiB). Streams larger
// than this still flow through unmodified; only the recording is truncated,
// and the interaction is flagged.
const DefaultMaxCapture = 1 << 20

// MissExitCode is what a replay shim exits with when no recording matches
// and the miss policy is "fail". It is deliberately distinctive so wrapped
// scripts can tell a cassette miss from an ordinary tool failure.
const MissExitCode = 51
