// Replay state: which interactions have been consumed and which live calls
// missed. The state lives in a sidecar JSON file next to the session (never
// next to the cassette, which stays read-only) and every mutation happens
// under an exclusive file lock, so concurrent shims — parallel make jobs,
// pipelines — consume interactions without double-serving any of them.
package shim

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/JaydenCJ/pathshim/internal/lockfile"
)

// State is the mutable replay bookkeeping shared by all shims in a session.
type State struct {
	Consumed []bool `json:"consumed"`
	Misses   []Miss `json:"misses"`
}

// Miss records one live call no recording matched, with the policy applied.
type Miss struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Policy  string   `json:"policy"`
}

// ConsumedCount returns how many interactions have been replayed.
func (s *State) ConsumedCount() int {
	n := 0
	for _, c := range s.Consumed {
		if c {
			n++
		}
	}
	return n
}

// LoadState reads the state file, returning a fresh state sized for n
// interactions when the file does not exist yet.
func LoadState(path string, n int) (*State, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &State{Consumed: make([]bool, n)}, nil
	}
	if err != nil {
		return nil, err
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("%s: corrupt replay state: %w", path, err)
	}
	if len(st.Consumed) != n {
		return nil, fmt.Errorf("%s: replay state tracks %d interactions but the cassette has %d",
			path, len(st.Consumed), n)
	}
	return &st, nil
}

// WithState runs fn with the state loaded, then persists it atomically —
// all under the session lock so read-modify-write is a single critical
// section across processes.
func WithState(path string, n int, fn func(*State) error) error {
	return lockfile.With(path+".lock", func() error {
		st, err := LoadState(path, n)
		if err != nil {
			return err
		}
		if err := fn(st); err != nil {
			return err
		}
		return saveState(path, st)
	})
}

func saveState(path string, st *State) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".pathshim-state-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, path)
}
