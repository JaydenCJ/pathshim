// Package cli implements the pathshim command line: record, replay,
// inspect, verify, version. Run is pure with respect to its streams and
// returns the process exit code, so the whole surface is testable
// in-process.
package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/JaydenCJ/pathshim/internal/version"
)

// Exit codes shared by every subcommand. Record and replay additionally
// propagate the wrapped command's own exit code.
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
	exitRuntime = 3
)

const usageText = `pathshim — record external command invocations via PATH shims,
replay them offline in tests.

Usage:
  pathshim record  --cassette FILE --cmd NAME [flags] -- COMMAND [ARG...]
  pathshim replay  --cassette FILE [flags] -- COMMAND [ARG...]
  pathshim inspect [--format text|json] [--index N] FILE
  pathshim verify  FILE
  pathshim version

Run 'pathshim <subcommand> -h' for that subcommand's flags.
`

// Run dispatches the CLI and returns the process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText)
		return exitUsage
	}
	switch args[0] {
	case "record":
		return runRecord(args[1:], stdout, stderr)
	case "replay":
		return runReplay(args[1:], stdout, stderr)
	case "inspect":
		return runInspect(args[1:], stdout, stderr)
	case "verify":
		return runVerify(args[1:], stdout, stderr)
	case "version", "--version", "-V":
		fmt.Fprintln(stdout, "pathshim "+version.Version)
		return exitOK
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usageText)
		return exitOK
	default:
		fmt.Fprintf(stderr, "pathshim: unknown subcommand %q\n\n", args[0])
		fmt.Fprint(stderr, usageText)
		return exitUsage
	}
}

// stringList is a repeatable flag that also accepts comma-separated values,
// so both `--cmd git --cmd docker` and `--cmd git,docker` work.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(v string) error {
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			*s = append(*s, part)
		}
	}
	return nil
}

// newFlagSet builds a FlagSet that reports errors on our stderr and never
// calls os.Exit.
func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}
