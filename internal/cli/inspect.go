// `pathshim inspect` and `pathshim verify`: read-only views over a
// cassette. Inspect summarizes interactions as a table (or re-emits the
// normalized JSON); verify validates the schema and reports a one-line
// verdict with a non-zero exit on any problem.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/JaydenCJ/pathshim/internal/cassette"
)

func runInspect(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("inspect", stderr)
	format := fs.String("format", "text", "output format: text or json")
	index := fs.Int("index", 0, "show full detail for interaction N (1-based)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "pathshim inspect: exactly one cassette file is required")
		return exitUsage
	}
	if *format != "text" && *format != "json" {
		fmt.Fprintf(stderr, "pathshim inspect: --format must be text or json (got %q)\n", *format)
		return exitUsage
	}
	cas, err := cassette.Load(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(stderr, "pathshim inspect: %v\n", err)
		return exitFailure
	}
	if *index != 0 {
		if *index < 1 || *index > len(cas.Interactions) {
			fmt.Fprintf(stderr, "pathshim inspect: --index %d out of range (1..%d)\n",
				*index, len(cas.Interactions))
			return exitUsage
		}
		return inspectDetail(cas.Interactions[*index-1], *index, *format, stdout)
	}
	if *format == "json" {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(cas)
		return exitOK
	}
	inspectSummary(cas, fs.Arg(0), stdout)
	return exitOK
}

func inspectSummary(cas *cassette.Cassette, path string, w io.Writer) {
	fmt.Fprintf(w, "cassette %s — format %d, pathshim %s, recorded %s\n",
		path, cas.Format, cas.Version, cas.RecordedAt)
	if len(cas.Command) > 0 {
		fmt.Fprintf(w, "wrapped: %s\n", strings.Join(cas.Command, " "))
	}
	fmt.Fprintf(w, "interactions: %d across %d command(s)\n\n",
		len(cas.Interactions), len(cas.Commands()))
	fmt.Fprintf(w, "%4s  %-12s %5s  %9s  %9s  %s\n",
		"#", "command", "exit", "stdout", "stderr", "argv")
	for i, in := range cas.Interactions {
		note := ""
		if in.Truncated {
			note = "  (truncated)"
		}
		fmt.Fprintf(w, "%4d  %-12s %5d  %8dB  %8dB  %s%s\n",
			i+1, in.Command, in.ExitCode, in.Stdout.Size, in.Stderr.Size,
			truncateArgv(in.Argv(), 60), note)
	}
}

func inspectDetail(in cassette.Interaction, n int, format string, w io.Writer) int {
	if format == "json" {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(in)
		return exitOK
	}
	fmt.Fprintf(w, "interaction #%d\n", n)
	fmt.Fprintf(w, "  command:  %s\n", in.Command)
	fmt.Fprintf(w, "  args:     %s\n", strings.Join(in.Args, " "))
	fmt.Fprintf(w, "  exit:     %d\n", in.ExitCode)
	fmt.Fprintf(w, "  duration: %d ms\n", in.DurationMS)
	if len(in.Env) > 0 {
		fmt.Fprintf(w, "  env:      %d captured variable(s)\n", len(in.Env))
	}
	printBody(w, "stdin", in.Stdin)
	body := in.Stdout
	printBody(w, "stdout", &body)
	body = in.Stderr
	printBody(w, "stderr", &body)
	return exitOK
}

func printBody(w io.Writer, label string, b *cassette.Body) {
	if b == nil {
		fmt.Fprintf(w, "  %-7s (not captured)\n", label+":")
		return
	}
	kind := "text"
	if b.B64 != "" {
		kind = "base64"
	}
	fmt.Fprintf(w, "  %-7s %d B (%s)\n", label+":", b.Size, kind)
	if b.Size > 0 && kind == "text" {
		for _, line := range strings.Split(strings.TrimRight(b.Text, "\n"), "\n") {
			fmt.Fprintf(w, "  | %s\n", line)
		}
	}
}

// truncateArgv shortens s to at most max bytes plus an ellipsis, cutting on
// a rune boundary so multi-byte arguments never render as mojibake.
func truncateArgv(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max - 1
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func runVerify(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("verify", stderr)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "pathshim verify: exactly one cassette file is required")
		return exitUsage
	}
	path := fs.Arg(0)
	cas, err := cassette.Load(path)
	if err != nil {
		fmt.Fprintf(stderr, "pathshim verify: %v\n", err)
		return exitFailure
	}
	truncated := 0
	for _, in := range cas.Interactions {
		if in.Truncated {
			truncated++
		}
	}
	fmt.Fprintf(stdout, "%s: OK — format %d, %d interaction(s), %d command(s), %d truncated\n",
		path, cas.Format, len(cas.Interactions), len(cas.Commands()), truncated)
	if truncated > 0 {
		fmt.Fprintf(stderr, "pathshim verify: warning: %d interaction(s) hit the capture cap; replaying them is lossy\n", truncated)
	}
	return exitOK
}
