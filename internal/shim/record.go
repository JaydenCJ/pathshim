// Record-mode shim: resolve the real executable (skipping the shim
// directory), run it with streams teed into bounded capture buffers, then
// journal the interaction. The wrapped command observes the exact bytes and
// exit code the real tool produced — recording is a pure tap.
package shim

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/JaydenCJ/pathshim/internal/cassette"
	"github.com/JaydenCJ/pathshim/internal/session"
)

func runRecord(name string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	shimDir := os.Getenv(session.EnvShimDir)
	logPath := os.Getenv(session.EnvLog)
	if logPath == "" {
		fmt.Fprintf(stderr, "pathshim: record shim %q has no journal (%s unset)\n",
			name, session.EnvLog)
		return 125
	}

	redactor, err := loadRedactor()
	if err != nil {
		fmt.Fprintf(stderr, "pathshim: %v\n", err)
		reportError(err.Error())
		return 125
	}

	real, err := session.LookPathExcluding(name, os.Getenv("PATH"), shimDir)
	if err != nil {
		msg := fmt.Sprintf("%s: no real executable found outside the shim directory", name)
		fmt.Fprintf(stderr, "pathshim: %s\n", msg)
		reportError(msg)
		return 127
	}

	max := maxCapture()
	cmd := exec.Command(real, args...)
	outBuf := newCapBuffer(max)
	errBuf := newCapBuffer(max)
	cmd.Stdout = io.MultiWriter(stdout, outBuf)
	cmd.Stderr = io.MultiWriter(stderr, errBuf)

	// Stdin handling. A terminal passes through untouched (interactive
	// tools keep working; nothing is recorded). Everything else is teed
	// through a pipe we manage ourselves, so a child that exits without
	// reading stdin never wedges the shim: we simply stop waiting.
	var stdinBuf *capBuffer
	var stdinDone chan struct{}
	if capturable(stdin) {
		w, err := cmd.StdinPipe()
		if err != nil {
			fmt.Fprintf(stderr, "pathshim: %v\n", err)
			return 126
		}
		stdinBuf = newCapBuffer(max)
		stdinDone = make(chan struct{})
		go func() {
			defer close(stdinDone)
			defer w.Close()
			// TeeReader fills the capture buffer as the child consumes
			// input, so the recording holds exactly what the tool read.
			_, _ = io.Copy(w, io.TeeReader(stdin, stdinBuf))
		}()
	} else {
		cmd.Stdin = stdin.(*os.File)
	}

	start := time.Now()
	runErr := cmd.Run()
	code, execFailed := exitStatus(runErr)
	if execFailed {
		fmt.Fprintf(stderr, "pathshim: %s: %v\n", name, runErr)
		reportError(fmt.Sprintf("%s: %v", name, runErr))
		return 126
	}
	if stdinDone != nil {
		// If the copier already hit EOF, let it finish flushing; if it is
		// still blocked on an idle upstream pipe, do not wait for it.
		select {
		case <-stdinDone:
		default:
		}
	}

	in := cassette.Interaction{
		Command:    name,
		Args:       append([]string{}, args...),
		Env:        capturedEnv(),
		ExitCode:   code,
		DurationMS: time.Since(start).Milliseconds(),
	}
	outBytes, outTrunc := outBuf.Snapshot()
	errBytes, errTrunc := errBuf.Snapshot()
	in.Stdout = cassette.NewBody(redactor.Apply(outBytes))
	in.Stderr = cassette.NewBody(redactor.Apply(errBytes))
	in.Truncated = outTrunc || errTrunc
	if stdinBuf != nil {
		inBytes, inTrunc := stdinBuf.Snapshot()
		body := cassette.NewBody(redactor.Apply(inBytes))
		in.Stdin = &body
		in.Truncated = in.Truncated || inTrunc
	}

	if err := cassette.AppendLog(logPath, in); err != nil {
		msg := fmt.Sprintf("failed to journal %s invocation: %v", name, err)
		fmt.Fprintf(stderr, "pathshim: %s\n", msg)
		reportError(msg)
		return 125
	}
	return code
}

// loadRedactor builds the redactor from the JSON-encoded pattern list the
// parent placed in the environment.
func loadRedactor() (*cassette.Redactor, error) {
	exprs, err := cassette.DecodePatterns(os.Getenv(session.EnvRedact))
	if err != nil {
		return nil, err
	}
	return cassette.NewRedactor(exprs)
}

// capturedEnv snapshots the environment variables the user asked to record
// (PATHSHIM_ENV_KEYS). Unset variables are simply absent, which lets replay
// distinguish "unset" from "empty".
func capturedEnv() map[string]string {
	keys := os.Getenv(session.EnvEnvKeys)
	if keys == "" {
		return nil
	}
	env := map[string]string{}
	for _, k := range strings.Split(keys, ",") {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if v, ok := os.LookupEnv(k); ok {
			env[k] = v
		}
	}
	if len(env) == 0 {
		return nil
	}
	return env
}

// exitStatus converts an exec error into a shell-style exit code. Signal
// deaths map to 128+signal, matching what a shell would report, so replay
// reproduces them faithfully.
func exitStatus(err error) (code int, execFailed bool) {
	if err == nil {
		return 0, false
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal()), false
		}
		return ee.ExitCode(), false
	}
	return 0, true
}
