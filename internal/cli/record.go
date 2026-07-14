// `pathshim record`: build a shim directory for the named commands, run the
// wrapped command with that directory prepended to PATH, then fold the
// journal every shim wrote into a cassette. The wrapped command's exit code
// is propagated unless a shim reported an internal failure, which fails the
// session loudly.
package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/JaydenCJ/pathshim/internal/cassette"
	"github.com/JaydenCJ/pathshim/internal/session"
	"github.com/JaydenCJ/pathshim/internal/version"
)

func runRecord(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("record", stderr)
	var (
		cassPath = fs.String("cassette", "", "cassette file to write (required)")
		cmds     stringList
		envKeys  stringList
		redacts  stringList
		maxCap   = fs.Int("max-capture", session.DefaultMaxCapture,
			"max bytes recorded per stream (streams still flow through in full)")
	)
	fs.Var(&cmds, "cmd", "command name to shim (repeatable, or comma-separated; required)")
	fs.Var(&envKeys, "env", "environment variable to capture per interaction (repeatable)")
	fs.Var(&redacts, "redact", "regexp replaced with [REDACTED] in recorded streams (repeatable)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	argv := fs.Args()
	switch {
	case *cassPath == "":
		fmt.Fprintln(stderr, "pathshim record: --cassette is required")
		return exitUsage
	case len(cmds) == 0:
		fmt.Fprintln(stderr, "pathshim record: at least one --cmd is required")
		return exitUsage
	case len(argv) == 0:
		fmt.Fprintln(stderr, "pathshim record: no command to run (put it after --)")
		return exitUsage
	case *maxCap <= 0:
		fmt.Fprintln(stderr, "pathshim record: --max-capture must be positive")
		return exitUsage
	}
	for _, name := range cmds {
		if err := session.ValidateName(name); err != nil {
			fmt.Fprintf(stderr, "pathshim record: %v\n", err)
			return exitUsage
		}
	}
	redactEnc, err := cassette.EncodePatterns(redacts)
	if err == nil {
		_, err = cassette.NewRedactor(redacts) // validate patterns up front
	}
	if err != nil {
		fmt.Fprintf(stderr, "pathshim record: %v\n", err)
		return exitUsage
	}

	sess, cleanup, err := newSessionDir(cmds)
	if err != nil {
		fmt.Fprintf(stderr, "pathshim record: %v\n", err)
		return exitRuntime
	}
	defer cleanup()

	logPath := filepath.Join(sess.dir, "journal.jsonl")
	env := session.BuildEnv(os.Environ(), sess.shimDir, map[string]string{
		session.EnvMode:       session.ModeRecord,
		session.EnvShimDir:    sess.shimDir,
		session.EnvLog:        logPath,
		session.EnvErrFile:    sess.errFile,
		session.EnvEnvKeys:    envKeys.String(),
		session.EnvRedact:     redactEnc,
		session.EnvMaxCapture: strconv.Itoa(*maxCap),
	})

	code, err := runWrapped(argv, env, stdout, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "pathshim record: %v\n", err)
		return exitRuntime
	}

	interactions, err := cassette.ReadLog(logPath)
	if err != nil {
		fmt.Fprintf(stderr, "pathshim record: %v\n", err)
		return exitRuntime
	}
	cas := &cassette.Cassette{
		Format:       cassette.FormatVersion,
		Version:      version.Version,
		RecordedAt:   recordedAt(),
		Command:      argv,
		Interactions: interactions,
	}
	if err := cas.Save(*cassPath); err != nil {
		fmt.Fprintf(stderr, "pathshim record: %v\n", err)
		return exitRuntime
	}
	fmt.Fprintf(stderr, "pathshim: recorded %d interaction(s) for %d command(s) -> %s\n",
		len(interactions), len(cas.Commands()), *cassPath)

	if failed, msgs := sessionErrors(sess.errFile); failed {
		for _, m := range msgs {
			fmt.Fprintf(stderr, "pathshim: shim error: %s\n", m)
		}
		return exitRuntime
	}
	return code
}

// sessionDir bundles the per-session scratch layout.
type sessionDir struct {
	dir     string
	shimDir string
	errFile string
}

// newSessionDir creates the temp session directory, the shim subdirectory,
// and one symlink per command, all pointing at the running binary.
func newSessionDir(cmds []string) (*sessionDir, func(), error) {
	exe, err := session.Executable()
	if err != nil {
		return nil, nil, err
	}
	dir, err := os.MkdirTemp("", "pathshim-session-")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { os.RemoveAll(dir) }
	shimDir := filepath.Join(dir, "bin")
	if err := os.Mkdir(shimDir, 0o755); err != nil {
		cleanup()
		return nil, nil, err
	}
	if err := session.CreateShims(shimDir, exe, cmds); err != nil {
		cleanup()
		return nil, nil, err
	}
	return &sessionDir{
		dir:     dir,
		shimDir: shimDir,
		errFile: filepath.Join(dir, "errors.log"),
	}, cleanup, nil
}

// runWrapped resolves argv[0] against the session PATH (so directly wrapped
// shimmed commands hit the shims too) and runs it with inherited stdio.
func runWrapped(argv, env []string, stdout, stderr io.Writer) (int, error) {
	pathVar := ""
	for _, kv := range env {
		if len(kv) > 5 && kv[:5] == "PATH=" {
			pathVar = kv[5:]
		}
	}
	bin, err := session.LookPath(argv[0], pathVar)
	if err != nil {
		return 0, err
	}
	cmd := exec.Command(bin, argv[1:]...)
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err = cmd.Run()
	if err == nil {
		return 0, nil
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), nil
	}
	return 0, err
}

// sessionErrors reads shim-reported failures; any line fails the session.
func sessionErrors(path string) (bool, []string) {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return false, nil
	}
	var msgs []string
	for _, line := range splitLines(string(data)) {
		if line != "" {
			msgs = append(msgs, line)
		}
	}
	return len(msgs) > 0, msgs
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// recordedAt stamps the cassette. PATHSHIM_FIXED_TIME pins it for
// byte-reproducible recordings (used by tests and documentation).
func recordedAt() string {
	if fixed := os.Getenv(session.EnvFixedTime); fixed != "" {
		if _, err := time.Parse(time.RFC3339, fixed); err == nil {
			return fixed
		}
	}
	return time.Now().UTC().Format(time.RFC3339)
}
