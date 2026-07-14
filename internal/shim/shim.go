// Package shim implements what happens when the wrapped command runs an
// intercepted tool: the shim symlink resolves to the pathshim binary, which
// dispatches here on argv[0]. In record mode the shim executes the real
// tool and journals the interaction; in replay mode it answers entirely
// from the cassette.
package shim

import (
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/JaydenCJ/pathshim/internal/lockfile"
	"github.com/JaydenCJ/pathshim/internal/session"
)

// Run executes shim behavior for the command `name` and returns the process
// exit code. Streams are parameters (not os.Std*) so the full shim path is
// unit-testable in-process.
func Run(name string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	switch os.Getenv(session.EnvMode) {
	case session.ModeRecord:
		return runRecord(name, args, stdin, stdout, stderr)
	case session.ModeReplay:
		return runReplay(name, args, stdin, stdout, stderr)
	default:
		fmt.Fprintf(stderr,
			"pathshim: shim %q invoked outside a pathshim session (%s is unset)\n",
			name, session.EnvMode)
		return 125
	}
}

// reportError appends a line to the session error file so the parent
// process can fail the whole run even if the wrapped command ignored the
// shim's exit code. Best-effort: a missing error file is not itself fatal.
func reportError(msg string) {
	path := os.Getenv(session.EnvErrFile)
	if path == "" {
		return
	}
	_ = lockfile.With(path+".lock", func() error {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = f.WriteString(msg + "\n")
		return err
	})
}

// maxCapture reads the per-stream capture cap from the environment.
func maxCapture() int {
	if s := os.Getenv(session.EnvMaxCapture); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			return n
		}
	}
	return session.DefaultMaxCapture
}

// capturable reports whether stdin should be drained/teed: anything except
// a terminal (character device). Non-file readers, pipes, and regular files
// are all capturable.
func capturable(stdin io.Reader) bool {
	f, ok := stdin.(*os.File)
	if !ok {
		return true
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice == 0
}
