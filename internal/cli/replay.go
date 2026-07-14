// `pathshim replay`: build shims for every command in the cassette, run the
// wrapped command against them, and account for the outcome. The session
// fails when the wrapped command fails, when any call missed the cassette,
// or (with --require-all) when recorded interactions were never used —
// each with an explicit message, so a red test always says why.
package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/JaydenCJ/pathshim/internal/cassette"
	"github.com/JaydenCJ/pathshim/internal/session"
	"github.com/JaydenCJ/pathshim/internal/shim"
)

func runReplay(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("replay", stderr)
	var (
		cassPath   = fs.String("cassette", "", "cassette file to replay (required)")
		extraCmds  stringList
		ordered    = fs.Bool("ordered", false, "require calls in exactly the recorded order")
		matchStdin = fs.Bool("match-stdin", false, "also match on drained stdin")
		matchEnv   = fs.Bool("match-env", false, "also match recorded env vars")
		onMiss     = fs.String("on-miss", session.OnMissFail,
			"what a shim does when nothing matches: fail, passthrough, or empty")
		requireAll = fs.Bool("require-all", false,
			"fail unless every recorded interaction was replayed")
	)
	fs.Var(&extraCmds, "cmd", "extra command to shim beyond those in the cassette (repeatable)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	argv := fs.Args()
	switch {
	case *cassPath == "":
		fmt.Fprintln(stderr, "pathshim replay: --cassette is required")
		return exitUsage
	case len(argv) == 0:
		fmt.Fprintln(stderr, "pathshim replay: no command to run (put it after --)")
		return exitUsage
	}
	switch *onMiss {
	case session.OnMissFail, session.OnMissPassthrough, session.OnMissEmpty:
	default:
		fmt.Fprintf(stderr, "pathshim replay: --on-miss must be fail, passthrough, or empty (got %q)\n", *onMiss)
		return exitUsage
	}

	cas, err := cassette.Load(*cassPath)
	if err != nil {
		fmt.Fprintf(stderr, "pathshim replay: %v\n", err)
		return exitRuntime
	}
	names := mergeNames(cas.Commands(), extraCmds)
	for _, name := range names {
		if err := session.ValidateName(name); err != nil {
			fmt.Fprintf(stderr, "pathshim replay: %v\n", err)
			return exitUsage
		}
	}
	if len(names) == 0 {
		fmt.Fprintln(stderr, "pathshim replay: cassette has no interactions and no --cmd was given")
		return exitUsage
	}

	sess, cleanup, err := newSessionDir(names)
	if err != nil {
		fmt.Fprintf(stderr, "pathshim replay: %v\n", err)
		return exitRuntime
	}
	defer cleanup()

	statePath := filepath.Join(sess.dir, "state.json")
	vars := map[string]string{
		session.EnvMode:     session.ModeReplay,
		session.EnvShimDir:  sess.shimDir,
		session.EnvCassette: absolute(*cassPath),
		session.EnvState:    statePath,
		session.EnvErrFile:  sess.errFile,
		session.EnvOnMiss:   *onMiss,
	}
	if *ordered {
		vars[session.EnvOrdered] = "1"
	}
	if *matchStdin {
		vars[session.EnvMatchStdin] = "1"
	}
	if *matchEnv {
		vars[session.EnvMatchEnv] = "1"
	}
	env := session.BuildEnv(os.Environ(), sess.shimDir, vars)

	code, err := runWrapped(argv, env, stdout, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "pathshim replay: %v\n", err)
		return exitRuntime
	}

	st, err := shim.LoadState(statePath, len(cas.Interactions))
	if err != nil {
		fmt.Fprintf(stderr, "pathshim replay: %v\n", err)
		return exitRuntime
	}
	replayed := st.ConsumedCount()
	fmt.Fprintf(stderr, "pathshim: replayed %d/%d interaction(s), %d miss(es)\n",
		replayed, len(cas.Interactions), len(st.Misses))
	for _, m := range st.Misses {
		fmt.Fprintf(stderr, "pathshim: miss (%s): %s\n", m.Policy,
			strings.Join(append([]string{m.Command}, m.Args...), " "))
	}

	if failed, msgs := sessionErrors(sess.errFile); failed {
		for _, m := range msgs {
			fmt.Fprintf(stderr, "pathshim: shim error: %s\n", m)
		}
		return exitRuntime
	}
	if code != 0 {
		return code
	}
	if len(st.Misses) > 0 {
		fmt.Fprintln(stderr, "pathshim: replay failed: the cassette did not cover every call")
		return exitFailure
	}
	if *requireAll && replayed != len(cas.Interactions) {
		fmt.Fprintf(stderr, "pathshim: replay failed: %d recorded interaction(s) were never replayed (--require-all)\n",
			len(cas.Interactions)-replayed)
		return exitFailure
	}
	return exitOK
}

// mergeNames unions cassette commands with the extra --cmd names, keeping
// the sorted cassette order first and de-duplicating.
func mergeNames(base, extra []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range base {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	for _, n := range extra {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// absolute resolves the cassette path before the wrapped command runs,
// because the command may change its working directory.
func absolute(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}
