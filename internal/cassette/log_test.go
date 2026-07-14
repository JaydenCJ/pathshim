// Tests for the record journal: append-order preservation, tolerance of a
// missing journal (a run with zero shimmed calls), and safety under
// concurrent appends from many goroutines standing in for parallel shims.
package cassette

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func journalEntry(cmd string, n int) Interaction {
	return Interaction{
		Command: cmd,
		Args:    []string{"call", string(rune('a' + n))},
		Stdout:  NewBody([]byte("out\n")),
		Stderr:  NewBody(nil),
	}
}

func TestAppendThenReadPreservesOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	for i := 0; i < 5; i++ {
		if err := AppendLog(path, journalEntry("git", i)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := ReadLog(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("want 5 entries, got %d", len(got))
	}
	for i, in := range got {
		if in.Args[1] != string(rune('a'+i)) {
			t.Fatalf("entry %d out of order: %v", i, in.Args)
		}
	}
}

func TestReadLogMissingFileMeansNoInteractions(t *testing.T) {
	got, err := ReadLog(filepath.Join(t.TempDir(), "nope.jsonl"))
	if err != nil || got != nil {
		t.Fatalf("missing journal should be empty, got %v %v", got, err)
	}
}

func TestReadLogRejectsCorruptLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	os.WriteFile(path, []byte("{\"command\":\"git\",\"args\":[]}\nnot-json\n"), 0o644)
	if _, err := ReadLog(path); err == nil {
		t.Fatal("corrupt journal line must surface an error")
	}
}

func TestConcurrentAppendsNeverInterleave(t *testing.T) {
	// 20 goroutines × 5 appends: every line must parse and none may be
	// lost, which is exactly what parallel shim processes rely on.
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	var wg sync.WaitGroup
	for g := 0; g < 20; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 5; i++ {
				if err := AppendLog(path, journalEntry("kubectl", i)); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	got, err := ReadLog(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 100 {
		t.Fatalf("want 100 entries, got %d", len(got))
	}
}
