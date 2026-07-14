// Record journal: while a recording session runs, every shim process appends
// one JSON line to a shared journal file. The parent `pathshim record`
// process folds the journal into the pretty cassette when the wrapped
// command exits. Appends are serialized with a lock file so concurrent
// shims (e.g. a Makefile with -j) never interleave partial lines.
package cassette

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"github.com/JaydenCJ/pathshim/internal/lockfile"
)

// AppendLog appends one interaction to the journal at path.
func AppendLog(path string, in Interaction) error {
	data, err := json.Marshal(in)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return lockfile.With(path+".lock", func() error {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = f.Write(data)
		return err
	})
}

// ReadLog reads every interaction from the journal at path, in append
// order. A missing journal means the wrapped command made no shimmed calls
// and yields an empty slice, not an error.
func ReadLog(path string) ([]Interaction, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Interaction
	for i, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var in Interaction
		if err := json.Unmarshal(line, &in); err != nil {
			return nil, fmt.Errorf("journal line %d: %w", i+1, err)
		}
		out = append(out, in)
	}
	return out, nil
}
