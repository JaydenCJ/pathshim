// Tests for replay matching: the exact rules deciding which recording
// answers a live call. Every branch here is a behavior users will observe
// as "my test replayed the wrong thing" if it regresses.
package match

import (
	"strings"
	"testing"

	"github.com/JaydenCJ/pathshim/internal/cassette"
)

func inter(cmd string, args ...string) cassette.Interaction {
	return cassette.Interaction{
		Command: cmd, Args: args,
		Stdout: cassette.NewBody([]byte("out")), Stderr: cassette.NewBody(nil),
	}
}

func withStdin(in cassette.Interaction, stdin string) cassette.Interaction {
	b := cassette.NewBody([]byte(stdin))
	in.Stdin = &b
	return in
}

func call(cmd string, args ...string) Call {
	return Call{Command: cmd, Args: args}
}

func TestFindMatchesCommandAndArgsExactly(t *testing.T) {
	inters := []cassette.Interaction{inter("git", "status"), inter("git", "push")}
	idx, ok := Find(inters, []bool{false, false}, call("git", "push"), Options{})
	if !ok || idx != 1 {
		t.Fatalf("want (1,true), got (%d,%v)", idx, ok)
	}
	if _, ok := Find(inters, []bool{false, false}, call("docker", "push"), Options{}); ok {
		t.Fatal("different command must not match")
	}
}

func TestFindRejectsArgCountAndValueDifferences(t *testing.T) {
	inters := []cassette.Interaction{inter("git", "log", "-n", "1")}
	consumed := []bool{false}
	if _, ok := Find(inters, consumed, call("git", "log", "-n"), Options{}); ok {
		t.Fatal("missing arg must not match")
	}
	if _, ok := Find(inters, consumed, call("git", "log", "-n", "2"), Options{}); ok {
		t.Fatal("different arg value must not match")
	}
}

func TestFindConsumesEarliestUnconsumedAndMissesWhenExhausted(t *testing.T) {
	// Two identical recordings: consuming them front-to-back keeps repeated
	// calls replaying in the order they originally happened.
	inters := []cassette.Interaction{
		inter("git", "rev-parse", "HEAD"),
		inter("git", "rev-parse", "HEAD"),
	}
	idx, ok := Find(inters, []bool{true, false}, call("git", "rev-parse", "HEAD"), Options{})
	if !ok || idx != 1 {
		t.Fatalf("want (1,true), got (%d,%v)", idx, ok)
	}
	if _, ok := Find(inters, []bool{true, true}, call("git", "rev-parse", "HEAD"), Options{}); ok {
		t.Fatal("fully consumed cassette must miss")
	}
}

func TestOrderedModeEnforcesRecordedSequence(t *testing.T) {
	inters := []cassette.Interaction{inter("git", "status"), inter("git", "push")}
	// The next unconsumed recording is #2: push matches it...
	idx, ok := Find(inters, []bool{true, false}, call("git", "push"), Options{Ordered: true})
	if !ok || idx != 1 {
		t.Fatalf("want (1,true), got (%d,%v)", idx, ok)
	}
	// ...but calling push while #1 is still pending must miss, even though
	// an unordered search would have found it.
	if _, ok := Find(inters, []bool{false, false}, call("git", "push"), Options{Ordered: true}); ok {
		t.Fatal("ordered mode must reject a call that skips the next recording")
	}
}

func TestStdinMatchingComparesDrainedBytes(t *testing.T) {
	inters := []cassette.Interaction{withStdin(inter("tee", "f"), "payload")}
	consumed := []bool{false}
	opts := Options{MatchStdin: true}
	c := call("tee", "f")
	c.Stdin = []byte("payload")
	if _, ok := Find(inters, consumed, c, opts); !ok {
		t.Fatal("identical stdin must match")
	}
	c.Stdin = []byte("different")
	if _, ok := Find(inters, consumed, c, opts); ok {
		t.Fatal("different stdin must miss when MatchStdin is on")
	}
	// By default stdin is ignored entirely.
	if _, ok := Find(inters, consumed, c, Options{}); !ok {
		t.Fatal("stdin must be ignored when MatchStdin is off")
	}
}

func TestRecordingWithoutStdinMatchesAnyInput(t *testing.T) {
	// A tool recorded from a terminal has no stdin capture; replaying it
	// from a pipe must still work.
	inters := []cassette.Interaction{inter("git", "status")}
	c := call("git", "status")
	c.Stdin = []byte("whatever")
	if _, ok := Find(inters, []bool{false}, c, Options{MatchStdin: true}); !ok {
		t.Fatal("nil recorded stdin must match any live stdin")
	}
}

func TestEnvMatchingComparesRecordedVariables(t *testing.T) {
	in := inter("kubectl", "get", "pods")
	in.Env = map[string]string{"KUBECONFIG": "/cfg/prod"}
	inters := []cassette.Interaction{in}
	consumed := []bool{false}
	opts := Options{MatchEnv: true}
	c := call("kubectl", "get", "pods")
	c.EnvLookup = func(k string) (string, bool) {
		if k == "KUBECONFIG" {
			return "/cfg/prod", true
		}
		return "", false
	}
	if _, ok := Find(inters, consumed, c, opts); !ok {
		t.Fatal("matching env must match")
	}
	c.EnvLookup = func(string) (string, bool) { return "/cfg/dev", true }
	if _, ok := Find(inters, consumed, c, opts); ok {
		t.Fatal("different env value must miss")
	}
	c.EnvLookup = func(string) (string, bool) { return "", false }
	if _, ok := Find(inters, consumed, c, opts); ok {
		t.Fatal("unset env var must miss when a value was recorded")
	}
}

func TestExplainListsRankedCandidatesWithConsumptionState(t *testing.T) {
	inters := []cassette.Interaction{
		inter("git", "status"),
		inter("git", "push", "origin", "dev"),
		inter("docker", "build", "."),
	}
	got := Explain(inters, []bool{false, true, false}, call("git", "push", "origin", "main"))
	if !strings.Contains(got, "wanted: git push origin main") {
		t.Fatalf("missing wanted line:\n%s", got)
	}
	if !strings.Contains(got, "#2 git push origin dev") || !strings.Contains(got, "already consumed") {
		t.Fatalf("closest candidate with state missing:\n%s", got)
	}
	if strings.Contains(got, "docker") {
		t.Fatalf("other commands must not appear as candidates:\n%s", got)
	}
	// Ranking: the prefix-similar push must appear before status.
	pushPos := strings.Index(got, "git push origin dev")
	statusPos := strings.Index(got, "git status")
	if pushPos == -1 || statusPos == -1 || pushPos > statusPos {
		t.Fatalf("prefix-similar candidate should rank first:\n%s", got)
	}
}

func TestExplainSaysWhenCommandIsAbsentEntirely(t *testing.T) {
	inters := []cassette.Interaction{inter("git", "status")}
	got := Explain(inters, []bool{false}, call("terraform", "plan"))
	if !strings.Contains(got, `no "terraform" recordings`) {
		t.Fatalf("absent command should be called out:\n%s", got)
	}
}
