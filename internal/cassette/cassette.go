// Package cassette defines the on-disk cassette format: a JSON document that
// stores every intercepted command invocation (argv, stdin, stdout, stderr,
// exit code) so it can be replayed later without the real tool installed.
package cassette

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// FormatVersion is the cassette schema version this build reads and writes.
const FormatVersion = 1

// Body holds one captured byte stream (stdin, stdout, or stderr). Streams
// that are clean UTF-8 text are stored as a readable "text" field so
// cassettes diff well in code review; anything binary (or containing
// control sequences such as terminal colors) falls back to base64. Exactly
// one of Text/B64 may be non-empty.
type Body struct {
	Text string `json:"text,omitempty"`
	B64  string `json:"base64,omitempty"`
	Size int    `json:"bytes"`
}

// NewBody encodes raw captured bytes into a Body, choosing the text
// representation whenever it is lossless and human-readable.
func NewBody(b []byte) Body {
	if isCleanText(b) {
		return Body{Text: string(b), Size: len(b)}
	}
	return Body{B64: base64.StdEncoding.EncodeToString(b), Size: len(b)}
}

// isCleanText reports whether b can be stored verbatim in a JSON string
// without hiding information: valid UTF-8 and no control characters other
// than newline, carriage return, and tab. ANSI escape sequences are valid
// UTF-8 but contain ESC, so colored output is deliberately base64-encoded.
func isCleanText(b []byte) bool {
	if !utf8.Valid(b) {
		return false
	}
	for _, r := range string(b) {
		if r == 0x7f {
			return false
		}
		if r < 0x20 && r != '\n' && r != '\r' && r != '\t' {
			return false
		}
	}
	return true
}

// Data decodes the Body back into raw bytes.
func (b Body) Data() ([]byte, error) {
	if b.Text != "" && b.B64 != "" {
		return nil, errors.New("body sets both text and base64")
	}
	if b.B64 != "" {
		return base64.StdEncoding.DecodeString(b.B64)
	}
	return []byte(b.Text), nil
}

// Check validates internal consistency: decodable content whose length
// matches the declared byte count.
func (b Body) Check() error {
	d, err := b.Data()
	if err != nil {
		return err
	}
	if len(d) != b.Size {
		return fmt.Errorf("declared %d bytes but content decodes to %d", b.Size, len(d))
	}
	return nil
}

// Interaction is one recorded invocation of a shimmed command.
type Interaction struct {
	Command    string            `json:"command"`
	Args       []string          `json:"args"`
	Env        map[string]string `json:"env,omitempty"`
	Stdin      *Body             `json:"stdin,omitempty"`
	Stdout     Body              `json:"stdout"`
	Stderr     Body              `json:"stderr"`
	ExitCode   int               `json:"exit_code"`
	DurationMS int64             `json:"duration_ms"`
	Truncated  bool              `json:"truncated,omitempty"`
}

// Argv renders the interaction as a single shell-like line for diagnostics.
func (in Interaction) Argv() string {
	return strings.Join(append([]string{in.Command}, in.Args...), " ")
}

// Cassette is the top-level recorded document.
type Cassette struct {
	Format       int           `json:"pathshim_cassette"`
	Version      string        `json:"version"`
	RecordedAt   string        `json:"recorded_at"`
	Command      []string      `json:"command,omitempty"`
	Interactions []Interaction `json:"interactions"`
}

// Load reads and validates a cassette file.
func Load(path string) (*Cassette, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Cassette
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("%s: not a valid cassette: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

// Save writes the cassette atomically (temp file in the same directory,
// then rename) so a crashed run never leaves a half-written cassette.
func (c *Cassette) Save(path string) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".pathshim-cassette-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}

// Validate checks the format version and every interaction's consistency.
func (c *Cassette) Validate() error {
	if c.Format != FormatVersion {
		return fmt.Errorf("unsupported cassette format %d (this pathshim understands format %d)",
			c.Format, FormatVersion)
	}
	for i, in := range c.Interactions {
		if in.Command == "" {
			return fmt.Errorf("interaction #%d: empty command", i+1)
		}
		if strings.ContainsRune(in.Command, '/') {
			return fmt.Errorf("interaction #%d: command %q must be a bare name", i+1, in.Command)
		}
		if err := in.Stdout.Check(); err != nil {
			return fmt.Errorf("interaction #%d: stdout: %w", i+1, err)
		}
		if err := in.Stderr.Check(); err != nil {
			return fmt.Errorf("interaction #%d: stderr: %w", i+1, err)
		}
		if in.Stdin != nil {
			if err := in.Stdin.Check(); err != nil {
				return fmt.Errorf("interaction #%d: stdin: %w", i+1, err)
			}
		}
	}
	return nil
}

// Commands returns the sorted, de-duplicated set of command names that
// appear in the cassette; replay creates one shim per entry.
func (c *Cassette) Commands() []string {
	seen := map[string]bool{}
	var out []string
	for _, in := range c.Interactions {
		if !seen[in.Command] {
			seen[in.Command] = true
			out = append(out, in.Command)
		}
	}
	sort.Strings(out)
	return out
}
