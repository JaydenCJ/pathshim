// In-process CLI tests: dispatch, flag validation, and the read-only
// subcommands (inspect, verify) against fixture cassettes. Record/replay
// end-to-end flows live in internal/e2e, which drives the compiled binary
// because shims are separate processes.
package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JaydenCJ/pathshim/internal/cassette"
	"github.com/JaydenCJ/pathshim/internal/version"
)

func runCLI(args ...string) (stdout, stderr string, code int) {
	var out, errBuf bytes.Buffer
	code = Run(args, &out, &errBuf)
	return out.String(), errBuf.String(), code
}

func writeFixtureCassette(t *testing.T) string {
	t.Helper()
	cas := &cassette.Cassette{
		Format:     cassette.FormatVersion,
		Version:    version.Version,
		RecordedAt: "2026-07-13T00:00:00Z",
		Command:    []string{"sh", "deploy.sh"},
		Interactions: []cassette.Interaction{
			{Command: "git", Args: []string{"rev-parse", "--short", "HEAD"},
				Stdout: cassette.NewBody([]byte("deadbee\n")), Stderr: cassette.NewBody(nil)},
			{Command: "docker", Args: []string{"build", "-t", "app:dev", "."},
				Stdout: cassette.NewBody(nil),
				Stderr: cassette.NewBody([]byte("no daemon\n")), ExitCode: 1, Truncated: true},
		},
	}
	path := filepath.Join(t.TempDir(), "c.json")
	if err := cas.Save(path); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBadInvocationsPrintUsageAndExit2(t *testing.T) {
	_, stderr, code := runCLI()
	if code != exitUsage || !strings.Contains(stderr, "Usage:") {
		t.Fatalf("no args: code=%d stderr=%q", code, stderr)
	}
	_, stderr, code = runCLI("frobnicate")
	if code != exitUsage || !strings.Contains(stderr, `unknown subcommand "frobnicate"`) {
		t.Fatalf("unknown: code=%d stderr=%q", code, stderr)
	}
}

func TestVersionAndHelpGoToStdout(t *testing.T) {
	for _, arg := range []string{"version", "--version", "-V"} {
		stdout, _, code := runCLI(arg)
		if code != exitOK || stdout != "pathshim "+version.Version+"\n" {
			t.Fatalf("%s: code=%d stdout=%q", arg, code, stdout)
		}
	}
	stdout, _, code := runCLI("help")
	if code != exitOK || !strings.Contains(stdout, "pathshim record") {
		t.Fatalf("help: code=%d stdout=%q", code, stdout)
	}
}

func TestRecordRequiresCassetteCmdAndWrappedCommand(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"record", "--cmd", "git", "--", "true"}, "--cassette is required"},
		{[]string{"record", "--cassette", "c.json", "--", "true"}, "--cmd is required"},
		{[]string{"record", "--cassette", "c.json", "--cmd", "git"}, "no command to run"},
	}
	for _, tc := range cases {
		_, stderr, code := runCLI(tc.args...)
		if code != exitUsage || !strings.Contains(stderr, tc.want) {
			t.Fatalf("%v: code=%d stderr=%q", tc.args, code, stderr)
		}
	}
}

func TestRecordRejectsUnsafeShimNames(t *testing.T) {
	_, stderr, code := runCLI("record", "--cassette", "c.json", "--cmd", "usr/bin/git", "--", "true")
	if code != exitUsage || !strings.Contains(stderr, "must be bare") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	_, stderr, code = runCLI("record", "--cassette", "c.json", "--cmd", "pathshim", "--", "true")
	if code != exitUsage || !strings.Contains(stderr, "tool itself") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestRecordRejectsBadRedactPatternAndCaptureCap(t *testing.T) {
	_, stderr, code := runCLI("record", "--cassette", "c.json", "--cmd", "git",
		"--redact", "(", "--", "true")
	if code != exitUsage || !strings.Contains(stderr, "--redact") {
		t.Fatalf("redact: code=%d stderr=%q", code, stderr)
	}
	_, stderr, code = runCLI("record", "--cassette", "c.json", "--cmd", "git",
		"--max-capture", "0", "--", "true")
	if code != exitUsage || !strings.Contains(stderr, "--max-capture") {
		t.Fatalf("max-capture: code=%d stderr=%q", code, stderr)
	}
}

func TestCmdFlagAcceptsCommaSeparatedAndRepeated(t *testing.T) {
	var l stringList
	l.Set("git,docker")
	l.Set("kubectl")
	l.Set(" spaced , ")
	if got := l.String(); got != "git,docker,kubectl,spaced" {
		t.Fatalf("stringList = %q", got)
	}
}

func TestReplayFlagValidation(t *testing.T) {
	_, stderr, code := runCLI("replay", "--", "true")
	if code != exitUsage || !strings.Contains(stderr, "--cassette is required") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	_, stderr, code = runCLI("replay", "--cassette", "c.json", "--on-miss", "explode", "--", "true")
	if code != exitUsage || !strings.Contains(stderr, "--on-miss") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	// A well-formed invocation pointing at a missing cassette is a runtime
	// error (3), not a usage error (2).
	_, stderr, code = runCLI("replay", "--cassette", filepath.Join(t.TempDir(), "nope.json"), "--", "true")
	if code != exitRuntime || !strings.Contains(stderr, "nope.json") {
		t.Fatalf("missing cassette: code=%d stderr=%q", code, stderr)
	}
}

func TestInspectSummaryListsInteractions(t *testing.T) {
	path := writeFixtureCassette(t)
	stdout, _, code := runCLI("inspect", path)
	if code != exitOK {
		t.Fatalf("code=%d", code)
	}
	for _, want := range []string{
		"format 1", "wrapped: sh deploy.sh", "interactions: 2 across 2 command(s)",
		"git rev-parse --short HEAD", "docker build -t app:dev .", "(truncated)",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("summary missing %q:\n%s", want, stdout)
		}
	}
}

func TestInspectJSONRoundTripsThroughLoad(t *testing.T) {
	path := writeFixtureCassette(t)
	stdout, _, code := runCLI("inspect", "--format", "json", path)
	if code != exitOK {
		t.Fatalf("code=%d", code)
	}
	out := filepath.Join(t.TempDir(), "copy.json")
	os.WriteFile(out, []byte(stdout), 0o644)
	cas, err := cassette.Load(out)
	if err != nil || len(cas.Interactions) != 2 {
		t.Fatalf("inspect --format json must emit a loadable cassette: %v", err)
	}
}

func TestInspectIndexShowsBodyDetail(t *testing.T) {
	path := writeFixtureCassette(t)
	stdout, _, code := runCLI("inspect", "--index", "1", path)
	if code != exitOK {
		t.Fatalf("code=%d", code)
	}
	for _, want := range []string{"interaction #1", "command:  git", "| deadbee", "stdin:  (not captured)"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("detail missing %q:\n%s", want, stdout)
		}
	}
}

func TestInspectRejectsBadFlags(t *testing.T) {
	path := writeFixtureCassette(t)
	_, stderr, code := runCLI("inspect", "--index", "9", path)
	if code != exitUsage || !strings.Contains(stderr, "out of range (1..2)") {
		t.Fatalf("index: code=%d stderr=%q", code, stderr)
	}
	_, stderr, code = runCLI("inspect", "--format", "yaml", path)
	if code != exitUsage || !strings.Contains(stderr, "--format") {
		t.Fatalf("format: code=%d stderr=%q", code, stderr)
	}
}

func TestVerifyReportsOKWithCountsAndTruncationWarning(t *testing.T) {
	path := writeFixtureCassette(t)
	stdout, stderr, code := runCLI("verify", path)
	if code != exitOK || !strings.Contains(stdout, "OK — format 1, 2 interaction(s), 2 command(s), 1 truncated") {
		t.Fatalf("code=%d stdout=%q", code, stdout)
	}
	if !strings.Contains(stderr, "capture cap") {
		t.Fatalf("truncation warning missing: %q", stderr)
	}
}

func TestVerifyFailsOnBrokenCassetteOrBadArgs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	os.WriteFile(path, []byte(`{"pathshim_cassette":1,"interactions":[{"command":""}]}`), 0o644)
	_, stderr, code := runCLI("verify", path)
	if code != exitFailure || !strings.Contains(stderr, "empty command") {
		t.Fatalf("broken: code=%d stderr=%q", code, stderr)
	}
	if _, _, code := runCLI("verify"); code != exitUsage {
		t.Fatalf("no args: code=%d", code)
	}
	if _, _, code := runCLI("verify", "a.json", "b.json"); code != exitUsage {
		t.Fatalf("two args: code=%d", code)
	}
}
