// End-to-end tests against the compiled binary: full record → replay flows
// across process boundaries, exactly as users run them. TestMain builds
// pathshim once; every test then fabricates fake tools (tiny /bin/sh
// scripts) in temp dirs, records a session, and replays it with the fake
// tools removed — proving replay needs nothing but the cassette. Offline
// and deterministic throughout.
package e2e

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var pathshim string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "pathshim-e2e-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	pathshim = filepath.Join(dir, "pathshim")
	build := exec.Command("go", "build", "-o", pathshim, "./cmd/pathshim")
	build.Dir = "../.."
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building pathshim: %v\n%s", err, out)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// fixture is a per-test sandbox: a fake-tool directory on PATH and a
// scratch directory for cassettes and scripts.
type fixture struct {
	t     *testing.T
	tools string
	work  string
}

func newFixture(t *testing.T) *fixture {
	return &fixture{t: t, tools: t.TempDir(), work: t.TempDir()}
}

// tool creates a fake external command (e.g. a stand-in git) in the PATH
// the recording session will see.
func (f *fixture) tool(name, script string) {
	f.t.Helper()
	path := filepath.Join(f.tools, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		f.t.Fatal(err)
	}
}

// script writes an executable wrapped script into the work dir.
func (f *fixture) script(name, body string) string {
	f.t.Helper()
	path := filepath.Join(f.work, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		f.t.Fatal(err)
	}
	return path
}

func (f *fixture) cassette() string { return filepath.Join(f.work, "cassette.json") }

// run invokes the compiled pathshim. withTools controls whether the fake
// tools stay on PATH: true while recording, false while replaying, which
// is how the tests prove replay never touches the real tools.
func (f *fixture) run(withTools bool, stdin string, args ...string) (stdout, stderr string, code int) {
	f.t.Helper()
	cmd := exec.Command(pathshim, args...)
	path := "/usr/bin:/bin"
	if withTools {
		path = f.tools + ":" + path
	}
	cmd.Env = []string{"PATH=" + path, "HOME=" + f.work, "PATHSHIM_FIXED_TIME=2026-07-13T00:00:00Z"}
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err := cmd.Run()
	code = 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		f.t.Fatalf("running pathshim: %v", err)
	}
	return out.String(), errBuf.String(), code
}

func TestVersionFlagMatchesManifest(t *testing.T) {
	f := newFixture(t)
	stdout, _, code := f.run(false, "", "--version")
	if code != 0 || stdout != "pathshim 0.1.0\n" {
		t.Fatalf("code=%d stdout=%q", code, stdout)
	}
}

func TestRecordThenReplaySingleCall(t *testing.T) {
	f := newFixture(t)
	f.tool("git", `echo "deadbee"`)
	stdout, stderr, code := f.run(true, "",
		"record", "--cassette", f.cassette(), "--cmd", "git", "--",
		"git", "rev-parse", "--short", "HEAD")
	if code != 0 || stdout != "deadbee\n" {
		t.Fatalf("record: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "recorded 1 interaction(s) for 1 command(s)") {
		t.Fatalf("record summary missing: %q", stderr)
	}

	// Replay with the fake git gone from PATH entirely.
	stdout, stderr, code = f.run(false, "",
		"replay", "--cassette", f.cassette(), "--",
		"git", "rev-parse", "--short", "HEAD")
	if code != 0 || stdout != "deadbee\n" {
		t.Fatalf("replay: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "replayed 1/1 interaction(s), 0 miss(es)") {
		t.Fatalf("replay summary missing: %q", stderr)
	}
}

func TestReplayNeverInvokesTheRealTool(t *testing.T) {
	f := newFixture(t)
	marker := filepath.Join(f.work, "side-effect")
	f.tool("docker", "touch "+marker+"\necho built\n")
	f.run(true, "", "record", "--cassette", f.cassette(), "--cmd", "docker", "--",
		"docker", "build", ".")
	os.Remove(marker)

	// Tools stay on PATH here — replay must still not execute them.
	stdout, _, code := f.run(true, "", "replay", "--cassette", f.cassette(), "--",
		"docker", "build", ".")
	if code != 0 || stdout != "built\n" {
		t.Fatalf("code=%d stdout=%q", code, stdout)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("replay executed the real tool (side-effect file exists)")
	}
}

func TestScriptWithMultipleCommandsAndTools(t *testing.T) {
	f := newFixture(t)
	f.tool("git", `echo "sha-$2"`) // $2 varies per call
	f.tool("kubectl", `echo "pod/app configured"; echo "warn: context" >&2`)
	script := f.script("deploy.sh", "git rev-parse one\ngit rev-parse two\nkubectl apply\n")

	_, stderr, code := f.run(true, "", "record", "--cassette", f.cassette(),
		"--cmd", "git,kubectl", "--", "sh", script)
	if code != 0 || !strings.Contains(stderr, "recorded 3 interaction(s) for 2 command(s)") {
		t.Fatalf("record: code=%d stderr=%q", code, stderr)
	}

	stdout, stderr, code := f.run(false, "", "replay", "--cassette", f.cassette(),
		"--require-all", "--", "sh", script)
	if code != 0 {
		t.Fatalf("replay: code=%d stderr=%q", code, stderr)
	}
	want := "sha-one\nsha-two\npod/app configured\n"
	if stdout != want {
		t.Fatalf("stdout=%q want %q", stdout, want)
	}
	if !strings.Contains(stderr, "warn: context") {
		t.Fatalf("recorded stderr must replay: %q", stderr)
	}
}

func TestExitCodesArePropagatedAndReplayed(t *testing.T) {
	f := newFixture(t)
	f.tool("git", "echo denied >&2\nexit 3\n")
	_, _, code := f.run(true, "", "record", "--cassette", f.cassette(), "--cmd", "git", "--",
		"git", "push")
	if code != 3 {
		t.Fatalf("record must propagate the wrapped exit code, got %d", code)
	}
	// The wrapped command failed identically at replay; pathshim passes
	// that code through rather than masking it.
	_, stderr, code := f.run(false, "", "replay", "--cassette", f.cassette(), "--",
		"git", "push")
	if code != 3 || !strings.Contains(stderr, "denied") {
		t.Fatalf("replay: code=%d stderr=%q", code, stderr)
	}
}

func TestReplayMissFailsSessionWithDiagnosis(t *testing.T) {
	f := newFixture(t)
	f.tool("git", "echo ok")
	f.run(true, "", "record", "--cassette", f.cassette(), "--cmd", "git", "--",
		"git", "status")
	script := f.script("swallow.sh", "git push --force || true\nexit 0\n")

	_, stderr, code := f.run(false, "", "replay", "--cassette", f.cassette(), "--",
		"sh", script)
	if code != 1 {
		t.Fatalf("a miss must fail the session even when the script swallowed it, got %d", code)
	}
	for _, want := range []string{"replay miss", "wanted: git push --force", "did not cover every call"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("miss diagnosis missing %q:\n%s", want, stderr)
		}
	}
}

func TestReplayOnMissEmptyKeepsScriptGreen(t *testing.T) {
	f := newFixture(t)
	f.tool("git", "echo ok")
	f.run(true, "", "record", "--cassette", f.cassette(), "--cmd", "git", "--",
		"git", "status")
	stdout, stderr, code := f.run(false, "", "replay", "--cassette", f.cassette(),
		"--on-miss", "empty", "--", "git", "fetch")
	// The shim exits 0 silently, but the parent still reports the gap.
	if code != 1 || stdout != "" || !strings.Contains(stderr, "miss (empty): git fetch") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestReplayOnMissPassthroughUsesLiveTool(t *testing.T) {
	f := newFixture(t)
	f.tool("git", "echo recorded-answer")
	f.run(true, "", "record", "--cassette", f.cassette(), "--cmd", "git", "--",
		"git", "status")
	f.tool("git", "echo live-answer") // change the tool after recording
	script := f.script("mixed.sh", "git status\ngit fetch\n")

	stdout, _, code := f.run(true, "", "replay", "--cassette", f.cassette(),
		"--on-miss", "passthrough", "--", "sh", script)
	if code != 1 { // passthrough answered, but the parent still flags the gap
		t.Fatalf("code=%d", code)
	}
	if stdout != "recorded-answer\nlive-answer\n" {
		t.Fatalf("hybrid replay wrong: %q", stdout)
	}
}

func TestOrderedModeRejectsReorderingThatUnorderedAccepts(t *testing.T) {
	f := newFixture(t)
	f.tool("git", `echo "$1"`)
	record := f.script("fwd.sh", "git first\ngit second\n")
	f.run(true, "", "record", "--cassette", f.cassette(), "--cmd", "git", "--", "sh", record)
	reversed := f.script("rev.sh", "git second\ngit first\n")

	// Default (unordered) matching replays the reordered script cleanly.
	stdout, _, code := f.run(false, "", "replay", "--cassette", f.cassette(),
		"--require-all", "--", "sh", reversed)
	if code != 0 || stdout != "second\nfirst\n" {
		t.Fatalf("unordered: code=%d stdout=%q", code, stdout)
	}

	// --ordered rejects the same reordering...
	_, stderr, code := f.run(false, "", "replay", "--cassette", f.cassette(),
		"--ordered", "--", "sh", reversed)
	if code == 0 || !strings.Contains(stderr, "replay miss") {
		t.Fatalf("ordered replay must reject reordering: code=%d stderr=%q", code, stderr)
	}

	// ...while the original order still passes under --ordered.
	_, _, code = f.run(false, "", "replay", "--cassette", f.cassette(),
		"--ordered", "--require-all", "--", "sh", record)
	if code != 0 {
		t.Fatalf("original order must replay cleanly, got %d", code)
	}
}

func TestRequireAllFailsWhenRecordingsGoUnused(t *testing.T) {
	f := newFixture(t)
	f.tool("git", "echo ok")
	f.run(true, "", "record", "--cassette", f.cassette(), "--cmd", "git", "--",
		"sh", f.script("two.sh", "git a\ngit b\n"))
	_, stderr, code := f.run(false, "", "replay", "--cassette", f.cassette(),
		"--require-all", "--", "git", "a")
	if code != 1 || !strings.Contains(stderr, "1 recorded interaction(s) were never replayed") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestStdinIsRecordedAndMatchedWhenAsked(t *testing.T) {
	f := newFixture(t)
	f.tool("kubectl", "cat >/dev/null\necho applied\n")
	script := f.script("apply.sh", `printf 'kind: Pod\n' | kubectl apply -f -`+"\n")
	f.run(true, "", "record", "--cassette", f.cassette(), "--cmd", "kubectl", "--",
		"sh", script)

	// Same payload replays; a different payload misses under --match-stdin.
	_, _, code := f.run(false, "", "replay", "--cassette", f.cassette(),
		"--match-stdin", "--", "sh", script)
	if code != 0 {
		t.Fatalf("same stdin should replay, got %d", code)
	}
	other := f.script("apply2.sh", `printf 'kind: Job\n' | kubectl apply -f -`+"\n")
	_, _, code = f.run(false, "", "replay", "--cassette", f.cassette(),
		"--match-stdin", "--", "sh", other)
	if code == 0 {
		t.Fatal("different stdin must miss under --match-stdin")
	}
}

func TestRedactionScrubsCassetteButNotLiveOutput(t *testing.T) {
	f := newFixture(t)
	f.tool("vault", "echo token=tok_abc123")
	stdout, _, code := f.run(true, "", "record", "--cassette", f.cassette(),
		"--cmd", "vault", "--redact", "tok_[a-z0-9]+", "--",
		"vault", "read", "secret/app")
	if code != 0 || stdout != "token=tok_abc123\n" {
		t.Fatalf("live output must be untouched: code=%d stdout=%q", code, stdout)
	}
	raw, err := os.ReadFile(f.cassette())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("tok_abc123")) {
		t.Fatal("secret leaked into the cassette")
	}
	if !bytes.Contains(raw, []byte("[REDACTED]")) {
		t.Fatal("placeholder missing from the cassette")
	}
}

func TestBinaryOutputReplaysByteIdentically(t *testing.T) {
	f := newFixture(t)
	f.tool("git", `printf '\037\213\000\001\377'`)
	rec, _, code := f.run(true, "", "record", "--cassette", f.cassette(), "--cmd", "git", "--",
		"git", "archive", "HEAD")
	if code != 0 {
		t.Fatalf("record code=%d", code)
	}
	rep, _, code := f.run(false, "", "replay", "--cassette", f.cassette(), "--",
		"git", "archive", "HEAD")
	if code != 0 || rep != rec {
		t.Fatalf("binary replay differs: % x vs % x", rep, rec)
	}
}

func TestRecordIsByteReproducibleWithFixedTime(t *testing.T) {
	f := newFixture(t)
	f.tool("git", "echo stable")
	f.run(true, "", "record", "--cassette", f.cassette(), "--cmd", "git", "--", "git", "log")
	first, err := os.ReadFile(f.cassette())
	if err != nil {
		t.Fatal(err)
	}
	// duration_ms can legitimately differ between runs; normalize it away
	// and require everything else to be byte-identical.
	f.run(true, "", "record", "--cassette", f.cassette(), "--cmd", "git", "--", "git", "log")
	second, _ := os.ReadFile(f.cassette())
	norm := func(b []byte) string {
		var out []string
		for _, line := range strings.Split(string(b), "\n") {
			if strings.Contains(line, "duration_ms") {
				continue
			}
			out = append(out, line)
		}
		return strings.Join(out, "\n")
	}
	if norm(first) != norm(second) {
		t.Fatalf("recordings differ:\n%s\n---\n%s", first, second)
	}
}

func TestRecordFailsWhenRealToolIsAbsent(t *testing.T) {
	f := newFixture(t)
	// No fake terraform anywhere: the shim resolves nothing and reports it.
	_, stderr, code := f.run(false, "", "record", "--cassette", f.cassette(),
		"--cmd", "terraform", "--", "terraform", "plan")
	if code != 3 || !strings.Contains(stderr, "shim error") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestInspectAndVerifyOnARealRecording(t *testing.T) {
	f := newFixture(t)
	f.tool("git", "echo ok")
	f.run(true, "", "record", "--cassette", f.cassette(), "--cmd", "git", "--",
		"git", "status", "--short")
	stdout, _, code := f.run(false, "", "inspect", f.cassette())
	if code != 0 || !strings.Contains(stdout, "git status --short") {
		t.Fatalf("inspect: code=%d stdout=%q", code, stdout)
	}
	stdout, _, code = f.run(false, "", "verify", f.cassette())
	if code != 0 || !strings.Contains(stdout, "OK — format 1, 1 interaction(s)") {
		t.Fatalf("verify: code=%d stdout=%q", code, stdout)
	}
}

func TestParallelInvocationsAllLandInTheCassette(t *testing.T) {
	f := newFixture(t)
	f.tool("git", `echo "$1"`)
	// Eight concurrent shim processes appending to one journal.
	script := f.script("par.sh", `for i in 1 2 3 4 5 6 7 8; do git "call-$i" & done; wait`+"\n")
	_, stderr, code := f.run(true, "", "record", "--cassette", f.cassette(),
		"--cmd", "git", "--", "sh", script)
	if code != 0 || !strings.Contains(stderr, "recorded 8 interaction(s)") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	stdout, _, code := f.run(false, "", "replay", "--cassette", f.cassette(),
		"--require-all", "--", "sh", script)
	if code != 0 || strings.Count(stdout, "call-") != 8 {
		t.Fatalf("parallel replay: code=%d stdout=%q", code, stdout)
	}
}

func TestNestedInvocationsAreRecordedToo(t *testing.T) {
	f := newFixture(t)
	// A "make"-like tool that itself shells out to git: the nested git call
	// resolves through the shim PATH and is captured as its own interaction.
	f.tool("git", "echo nested-sha")
	f.tool("maketool", "git rev-parse\necho make-done\n")
	_, stderr, code := f.run(true, "", "record", "--cassette", f.cassette(),
		"--cmd", "git,maketool", "--", "maketool")
	if code != 0 || !strings.Contains(stderr, "recorded 2 interaction(s) for 2 command(s)") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	// Replay answers at the outermost boundary: the maketool recording
	// already contains the nested git output, so the inner git recording
	// stays unconsumed (1/2 replayed) — by design, not a bug.
	stdout, stderr, code := f.run(false, "", "replay", "--cassette", f.cassette(),
		"--", "maketool")
	if code != 0 || stdout != "nested-sha\nmake-done\n" {
		t.Fatalf("nested replay: code=%d stdout=%q", code, stdout)
	}
	if !strings.Contains(stderr, "replayed 1/2") {
		t.Fatalf("outermost-boundary accounting missing: %q", stderr)
	}
}
