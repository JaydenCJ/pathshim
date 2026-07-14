// In-process tests for both shim modes. Record tests execute tiny /bin/sh
// fixtures standing in for real tools; replay tests answer purely from
// fixture cassettes. Everything runs offline in temp dirs with pinned
// content, so results are identical on every machine.
package shim

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JaydenCJ/pathshim/internal/cassette"
	"github.com/JaydenCJ/pathshim/internal/session"
)

// writeTool creates an executable shell fixture named name in dir.
func writeTool(t *testing.T, dir, name, script string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// recordSession points the environment at a fresh record session and
// returns the journal path.
func recordSession(t *testing.T, toolDir string) string {
	t.Helper()
	dir := t.TempDir()
	journal := filepath.Join(dir, "journal.jsonl")
	shimDir := filepath.Join(dir, "bin")
	os.Mkdir(shimDir, 0o755)
	t.Setenv(session.EnvMode, session.ModeRecord)
	t.Setenv(session.EnvShimDir, shimDir)
	t.Setenv(session.EnvLog, journal)
	t.Setenv(session.EnvErrFile, filepath.Join(dir, "errors.log"))
	// Keep the system dirs so fixture scripts can use coreutils like cat.
	t.Setenv("PATH", toolDir+":/usr/bin:/bin")
	return journal
}

func readJournal(t *testing.T, path string) []cassette.Interaction {
	t.Helper()
	got, err := cassette.ReadLog(path)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestShimWithoutModeFailsLoudly(t *testing.T) {
	t.Setenv(session.EnvMode, "")
	var out, errBuf bytes.Buffer
	code := Run("git", []string{"status"}, bytes.NewReader(nil), &out, &errBuf)
	if code != 125 || !strings.Contains(errBuf.String(), "outside a pathshim session") {
		t.Fatalf("code=%d stderr=%q", code, errBuf.String())
	}
}

func TestRecordCapturesStdoutStderrAndExitCode(t *testing.T) {
	tools := t.TempDir()
	writeTool(t, tools, "git", "echo out-line\necho err-line >&2\nexit 4\n")
	journal := recordSession(t, tools)

	var out, errBuf bytes.Buffer
	code := Run("git", []string{"status", "--short"}, bytes.NewReader(nil), &out, &errBuf)
	if code != 4 {
		t.Fatalf("shim must propagate exit code, got %d (stderr %q)", code, errBuf.String())
	}
	if out.String() != "out-line\n" || errBuf.String() != "err-line\n" {
		t.Fatalf("streams must pass through: %q %q", out.String(), errBuf.String())
	}
	ins := readJournal(t, journal)
	if len(ins) != 1 {
		t.Fatalf("want 1 journal entry, got %d", len(ins))
	}
	in := ins[0]
	if in.Command != "git" || in.Args[0] != "status" || in.ExitCode != 4 {
		t.Fatalf("journal entry wrong: %+v", in)
	}
	stdout, _ := in.Stdout.Data()
	stderr, _ := in.Stderr.Data()
	if string(stdout) != "out-line\n" || string(stderr) != "err-line\n" {
		t.Fatalf("captured streams wrong: %q %q", stdout, stderr)
	}
}

func TestRecordTeesStdinToToolAndJournal(t *testing.T) {
	tools := t.TempDir()
	writeTool(t, tools, "kubectl", "cat\n") // echoes stdin to stdout
	journal := recordSession(t, tools)

	var out, errBuf bytes.Buffer
	code := Run("kubectl", []string{"apply", "-f", "-"},
		strings.NewReader("kind: Pod\n"), &out, &errBuf)
	if code != 0 || out.String() != "kind: Pod\n" {
		t.Fatalf("tool must still receive stdin: code=%d out=%q", code, out.String())
	}
	in := readJournal(t, journal)[0]
	if in.Stdin == nil {
		t.Fatal("stdin must be captured")
	}
	data, _ := in.Stdin.Data()
	if string(data) != "kind: Pod\n" {
		t.Fatalf("captured stdin = %q", data)
	}
}

func TestRecordSurvivesToolThatIgnoresStdin(t *testing.T) {
	// A tool that exits without reading its input must not wedge the shim.
	tools := t.TempDir()
	writeTool(t, tools, "git", "echo done\n")
	journal := recordSession(t, tools)

	var out, errBuf bytes.Buffer
	code := Run("git", []string{"status"}, strings.NewReader("ignored input"), &out, &errBuf)
	if code != 0 || out.String() != "done\n" {
		t.Fatalf("code=%d out=%q", code, out.String())
	}
	if len(readJournal(t, journal)) != 1 {
		t.Fatal("interaction must still be journaled")
	}
}

func TestRecordAppliesRedactionToStreams(t *testing.T) {
	tools := t.TempDir()
	writeTool(t, tools, "vault", "echo token=tok_abc123\n")
	journal := recordSession(t, tools)
	enc, _ := cassette.EncodePatterns([]string{`tok_[a-z0-9]+`})
	t.Setenv(session.EnvRedact, enc)

	var out, errBuf bytes.Buffer
	Run("vault", []string{"read"}, bytes.NewReader(nil), &out, &errBuf)
	if out.String() != "token=tok_abc123\n" {
		t.Fatalf("live output must NOT be redacted: %q", out.String())
	}
	data, _ := readJournal(t, journal)[0].Stdout.Data()
	if string(data) != "token="+cassette.Placeholder+"\n" {
		t.Fatalf("journal must be redacted: %q", data)
	}
}

func TestRecordCapturesRequestedEnvVars(t *testing.T) {
	tools := t.TempDir()
	writeTool(t, tools, "kubectl", "exit 0\n")
	journal := recordSession(t, tools)
	t.Setenv(session.EnvEnvKeys, "KUBECONFIG,ABSENT_VAR")
	t.Setenv("KUBECONFIG", "/cfg/test")

	var out, errBuf bytes.Buffer
	Run("kubectl", []string{"get", "pods"}, bytes.NewReader(nil), &out, &errBuf)
	env := readJournal(t, journal)[0].Env
	if env["KUBECONFIG"] != "/cfg/test" {
		t.Fatalf("KUBECONFIG not captured: %v", env)
	}
	if _, present := env["ABSENT_VAR"]; present {
		t.Fatal("unset variables must be absent, not empty")
	}
}

func TestRecordTruncatesCaptureButNotTheStream(t *testing.T) {
	tools := t.TempDir()
	writeTool(t, tools, "git", "printf 'abcdefghij'\n")
	journal := recordSession(t, tools)
	t.Setenv(session.EnvMaxCapture, "4")

	var out, errBuf bytes.Buffer
	Run("git", []string{"log"}, bytes.NewReader(nil), &out, &errBuf)
	if out.String() != "abcdefghij" {
		t.Fatalf("live stream must be complete: %q", out.String())
	}
	in := readJournal(t, journal)[0]
	data, _ := in.Stdout.Data()
	if string(data) != "abcd" || !in.Truncated {
		t.Fatalf("capture should be capped and flagged: %q truncated=%v", data, in.Truncated)
	}
}

func TestRecordMissingRealToolExits127AndReportsToParent(t *testing.T) {
	journal := recordSession(t, t.TempDir()) // PATH has no tools at all
	var out, errBuf bytes.Buffer
	code := Run("terraform", []string{"plan"}, bytes.NewReader(nil), &out, &errBuf)
	if code != 127 || !strings.Contains(errBuf.String(), "no real executable") {
		t.Fatalf("code=%d stderr=%q", code, errBuf.String())
	}
	if len(readJournal(t, journal)) != 0 {
		t.Fatal("failed resolution must not journal an interaction")
	}
	errs, _ := os.ReadFile(os.Getenv(session.EnvErrFile))
	if !strings.Contains(string(errs), "terraform") {
		t.Fatalf("parent error file should mention the tool: %q", errs)
	}
}

func TestRecordBinaryOutputRoundTrips(t *testing.T) {
	tools := t.TempDir()
	writeTool(t, tools, "git", `printf '\000\001\377'`+"\n")
	journal := recordSession(t, tools)
	var out, errBuf bytes.Buffer
	Run("git", []string{"cat-file"}, bytes.NewReader(nil), &out, &errBuf)
	in := readJournal(t, journal)[0]
	if in.Stdout.B64 == "" {
		t.Fatalf("binary output should be base64: %+v", in.Stdout)
	}
	data, _ := in.Stdout.Data()
	if !bytes.Equal(data, []byte{0x00, 0x01, 0xff}) {
		t.Fatalf("binary capture wrong: %v", data)
	}
}

// replaySession writes a cassette and points the environment at it,
// returning the state path.
func replaySession(t *testing.T, cas *cassette.Cassette) string {
	t.Helper()
	dir := t.TempDir()
	cassPath := filepath.Join(dir, "c.json")
	if err := cas.Save(cassPath); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(dir, "state.json")
	t.Setenv(session.EnvMode, session.ModeReplay)
	t.Setenv(session.EnvCassette, cassPath)
	t.Setenv(session.EnvState, statePath)
	t.Setenv(session.EnvShimDir, filepath.Join(dir, "bin"))
	t.Setenv(session.EnvErrFile, filepath.Join(dir, "errors.log"))
	return statePath
}

func fixtureCassette() *cassette.Cassette {
	return &cassette.Cassette{
		Format:  cassette.FormatVersion,
		Version: "0.1.0",
		Interactions: []cassette.Interaction{
			{Command: "git", Args: []string{"rev-parse", "HEAD"},
				Stdout: cassette.NewBody([]byte("deadbeef\n")),
				Stderr: cassette.NewBody([]byte("hint: shallow\n")), ExitCode: 0},
			{Command: "git", Args: []string{"push"},
				Stdout: cassette.NewBody(nil),
				Stderr: cassette.NewBody([]byte("rejected\n")), ExitCode: 1},
		},
	}
}

func TestReplayEmitsRecordedStreamsAndExitCode(t *testing.T) {
	statePath := replaySession(t, fixtureCassette())
	var out, errBuf bytes.Buffer
	code := Run("git", []string{"push"}, bytes.NewReader(nil), &out, &errBuf)
	if code != 1 || out.String() != "" || errBuf.String() != "rejected\n" {
		t.Fatalf("code=%d out=%q err=%q", code, out.String(), errBuf.String())
	}
	st, err := LoadState(statePath, 2)
	if err != nil || st.Consumed[1] != true || st.Consumed[0] != false {
		t.Fatalf("state wrong: %+v %v", st, err)
	}
}

func TestReplayConsumesEachRecordingOnce(t *testing.T) {
	replaySession(t, fixtureCassette())
	var out, errBuf bytes.Buffer
	if code := Run("git", []string{"push"}, bytes.NewReader(nil), &out, &errBuf); code != 1 {
		t.Fatalf("first call should replay, got %d", code)
	}
	out.Reset()
	errBuf.Reset()
	code := Run("git", []string{"push"}, bytes.NewReader(nil), &out, &errBuf)
	if code != session.MissExitCode {
		t.Fatalf("second identical call must miss with %d, got %d", session.MissExitCode, code)
	}
	if !strings.Contains(errBuf.String(), "already consumed") {
		t.Fatalf("miss diagnosis should mention consumption:\n%s", errBuf.String())
	}
}

func TestReplayMissFailIsDefaultAndExplains(t *testing.T) {
	replaySession(t, fixtureCassette())
	var out, errBuf bytes.Buffer
	code := Run("git", []string{"push", "--force"}, bytes.NewReader(nil), &out, &errBuf)
	if code != session.MissExitCode {
		t.Fatalf("want %d, got %d", session.MissExitCode, code)
	}
	if !strings.Contains(errBuf.String(), "wanted: git push --force") ||
		!strings.Contains(errBuf.String(), "#2 git push") {
		t.Fatalf("miss must explain with candidates:\n%s", errBuf.String())
	}
}

func TestReplayMissEmptyPolicySucceedsSilently(t *testing.T) {
	statePath := replaySession(t, fixtureCassette())
	t.Setenv(session.EnvOnMiss, session.OnMissEmpty)
	var out, errBuf bytes.Buffer
	code := Run("git", []string{"fetch"}, bytes.NewReader(nil), &out, &errBuf)
	if code != 0 || out.Len() != 0 {
		t.Fatalf("empty policy: code=%d out=%q", code, out.String())
	}
	st, _ := LoadState(statePath, 2)
	if len(st.Misses) != 1 || st.Misses[0].Policy != session.OnMissEmpty {
		t.Fatalf("miss must still be accounted: %+v", st.Misses)
	}
}

func TestReplayMissPassthroughRunsTheRealTool(t *testing.T) {
	tools := t.TempDir()
	writeTool(t, tools, "git", "echo live-answer\nexit 7\n")
	replaySession(t, fixtureCassette())
	t.Setenv(session.EnvOnMiss, session.OnMissPassthrough)
	t.Setenv("PATH", tools)
	var out, errBuf bytes.Buffer
	code := Run("git", []string{"fetch"}, bytes.NewReader(nil), &out, &errBuf)
	if code != 7 || out.String() != "live-answer\n" {
		t.Fatalf("passthrough failed: code=%d out=%q err=%q", code, out.String(), errBuf.String())
	}
}

func TestReplayOrderedRejectsOutOfOrderCalls(t *testing.T) {
	replaySession(t, fixtureCassette())
	t.Setenv(session.EnvOrdered, "1")
	var out, errBuf bytes.Buffer
	// Recording order is rev-parse then push; calling push first must miss.
	code := Run("git", []string{"push"}, bytes.NewReader(nil), &out, &errBuf)
	if code != session.MissExitCode {
		t.Fatalf("ordered replay must reject out-of-order call, got %d", code)
	}
}

func TestReplayMatchStdinDistinguishesPayloads(t *testing.T) {
	cas := fixtureCassette()
	body := cassette.NewBody([]byte("apiVersion: v1\n"))
	cas.Interactions[0] = cassette.Interaction{
		Command: "kubectl", Args: []string{"apply", "-f", "-"}, Stdin: &body,
		Stdout: cassette.NewBody([]byte("created\n")), Stderr: cassette.NewBody(nil),
	}
	replaySession(t, cas)
	t.Setenv(session.EnvMatchStdin, "1")
	var out, errBuf bytes.Buffer
	code := Run("kubectl", []string{"apply", "-f", "-"},
		strings.NewReader("apiVersion: v1\n"), &out, &errBuf)
	if code != 0 || out.String() != "created\n" {
		t.Fatalf("matching stdin should replay: code=%d out=%q", code, out.String())
	}
	out.Reset()
	code = Run("kubectl", []string{"apply", "-f", "-"},
		strings.NewReader("apiVersion: v2\n"), &out, &errBuf)
	if code != session.MissExitCode {
		t.Fatalf("different stdin must miss, got %d", code)
	}
}

func TestReplayCorruptCassetteFailsWithoutAnswering(t *testing.T) {
	dir := t.TempDir()
	cassPath := filepath.Join(dir, "c.json")
	os.WriteFile(cassPath, []byte("{"), 0o644)
	t.Setenv(session.EnvMode, session.ModeReplay)
	t.Setenv(session.EnvCassette, cassPath)
	t.Setenv(session.EnvState, filepath.Join(dir, "state.json"))
	t.Setenv(session.EnvErrFile, filepath.Join(dir, "errors.log"))
	var out, errBuf bytes.Buffer
	if code := Run("git", []string{"status"}, bytes.NewReader(nil), &out, &errBuf); code != 125 {
		t.Fatalf("corrupt cassette should exit 125, got %d", code)
	}
}

func TestCapBufferCapsCapturesWithoutShortWrites(t *testing.T) {
	b := newCapBuffer(3)
	// Writers in a MultiWriter chain must report full-length writes even
	// past the cap, or the passthrough stream would abort mid-flight.
	if n, err := b.Write([]byte("ab")); n != 2 || err != nil {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if n, err := b.Write([]byte("cdef")); n != 4 || err != nil {
		t.Fatalf("write past cap must still report full length: n=%d err=%v", n, err)
	}
	data, truncated := b.Snapshot()
	if string(data) != "abc" || !truncated {
		t.Fatalf("data=%q truncated=%v", data, truncated)
	}
	// An exact fit is not truncation.
	b = newCapBuffer(4)
	b.Write([]byte("abcd"))
	if data, truncated := b.Snapshot(); string(data) != "abcd" || truncated {
		t.Fatalf("data=%q truncated=%v", data, truncated)
	}
}

func TestStateMismatchWithCassetteIsAnError(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	if err := WithState(statePath, 3, func(*State) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadState(statePath, 5); err == nil {
		t.Fatal("state sized for a different cassette must be rejected")
	}
}
