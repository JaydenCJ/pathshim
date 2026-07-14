// Tests for the advisory lock. The critical property is mutual exclusion
// for read-modify-write cycles, exercised here with a classic lost-update
// counter: without the lock the final count would (nondeterministically)
// fall short.
package lockfile

import (
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
)

func TestAcquireReleaseSucceeds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	l, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestWithRunsFunctionAndPropagatesError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	ran := false
	if err := With(path, func() error { ran = true; return nil }); err != nil || !ran {
		t.Fatalf("With should run fn: ran=%v err=%v", ran, err)
	}
	sentinel := os.ErrPermission
	if err := With(path, func() error { return sentinel }); err != sentinel {
		t.Fatalf("With must propagate fn's error, got %v", err)
	}
}

func TestWithSerializesReadModifyWrite(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "counter.lock")
	dataPath := filepath.Join(dir, "counter")
	os.WriteFile(dataPath, []byte("0"), 0o644)

	const workers, rounds = 8, 25
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				err := With(lockPath, func() error {
					raw, err := os.ReadFile(dataPath)
					if err != nil {
						return err
					}
					n, err := strconv.Atoi(string(raw))
					if err != nil {
						return err
					}
					return os.WriteFile(dataPath, []byte(strconv.Itoa(n+1)), 0o644)
				})
				if err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	raw, _ := os.ReadFile(dataPath)
	if string(raw) != strconv.Itoa(workers*rounds) {
		t.Fatalf("lost updates: counter = %s, want %d", raw, workers*rounds)
	}
}
