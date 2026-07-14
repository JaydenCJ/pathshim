// Replay-mode shim: answer a live call purely from the cassette. The shim
// locks the shared state, finds an unconsumed matching interaction, marks
// it consumed, and reproduces its stdout, stderr, and exit code. Misses
// follow the configured policy: fail loudly (default), pass through to the
// real tool, or return empty success.
package shim

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/JaydenCJ/pathshim/internal/cassette"
	"github.com/JaydenCJ/pathshim/internal/match"
	"github.com/JaydenCJ/pathshim/internal/session"
)

func runReplay(name string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	cassPath := os.Getenv(session.EnvCassette)
	statePath := os.Getenv(session.EnvState)
	if cassPath == "" || statePath == "" {
		fmt.Fprintf(stderr, "pathshim: replay shim %q has no cassette or state configured\n", name)
		return 125
	}
	cas, err := cassette.Load(cassPath)
	if err != nil {
		fmt.Fprintf(stderr, "pathshim: %v\n", err)
		reportError(err.Error())
		return 125
	}

	opts := match.Options{
		Ordered:    os.Getenv(session.EnvOrdered) == "1",
		MatchStdin: os.Getenv(session.EnvMatchStdin) == "1",
		MatchEnv:   os.Getenv(session.EnvMatchEnv) == "1",
	}

	// Stdin is drained only when it participates in matching; otherwise it
	// is left untouched so pipelines behave as they would with any tool
	// that ignores its input.
	var stdinBytes []byte
	if opts.MatchStdin && capturable(stdin) {
		buf := newCapBuffer(maxCapture())
		_, _ = io.Copy(buf, stdin)
		stdinBytes, _ = buf.Snapshot()
	}

	call := match.Call{
		Command:   name,
		Args:      args,
		Stdin:     stdinBytes,
		EnvLookup: os.LookupEnv,
	}

	matched := -1
	explanation := ""
	policy := onMissPolicy()
	err = WithState(statePath, len(cas.Interactions), func(st *State) error {
		if idx, ok := match.Find(cas.Interactions, st.Consumed, call, opts); ok {
			st.Consumed[idx] = true
			matched = idx
			return nil
		}
		explanation = match.Explain(cas.Interactions, st.Consumed, call)
		st.Misses = append(st.Misses, Miss{
			Command: name,
			Args:    append([]string{}, args...),
			Policy:  policy,
		})
		return nil
	})
	if err != nil {
		fmt.Fprintf(stderr, "pathshim: %v\n", err)
		reportError(err.Error())
		return 125
	}

	if matched >= 0 {
		return emit(cas.Interactions[matched], stdout, stderr)
	}

	switch policy {
	case session.OnMissPassthrough:
		return passthrough(name, args, stdin, stdout, stderr, stdinBytes)
	case session.OnMissEmpty:
		return 0
	default:
		fmt.Fprintf(stderr, "pathshim: replay miss for %q — no unconsumed recording matches\n%s", name, explanation)
		return session.MissExitCode
	}
}

func onMissPolicy() string {
	switch p := os.Getenv(session.EnvOnMiss); p {
	case session.OnMissPassthrough, session.OnMissEmpty:
		return p
	default:
		return session.OnMissFail
	}
}

// emit reproduces the recorded streams and exit code byte-for-byte.
func emit(in cassette.Interaction, stdout, stderr io.Writer) int {
	if out, err := in.Stdout.Data(); err == nil {
		_, _ = stdout.Write(out)
	}
	if errs, err := in.Stderr.Data(); err == nil {
		_, _ = stderr.Write(errs)
	}
	return in.ExitCode
}

// passthrough hands the call to the real executable for hybrid replays
// (replay what you recorded, run everything else live). Stdin that was
// already drained for matching is re-fed to the real tool.
func passthrough(name string, args []string, stdin io.Reader, stdout, stderr io.Writer, drained []byte) int {
	real, err := session.LookPathExcluding(name, os.Getenv("PATH"), os.Getenv(session.EnvShimDir))
	if err != nil {
		fmt.Fprintf(stderr, "pathshim: passthrough: %s: no real executable found outside the shim directory\n", name)
		return 127
	}
	cmd := exec.Command(real, args...)
	if drained != nil {
		cmd.Stdin = bytes.NewReader(drained)
	} else if f, ok := stdin.(*os.File); ok {
		cmd.Stdin = f
	} else if stdin != nil {
		cmd.Stdin = stdin
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	code, execFailed := exitStatus(cmd.Run())
	if execFailed {
		fmt.Fprintf(stderr, "pathshim: passthrough: %s failed to start\n", name)
		return 126
	}
	return code
}
