// Tests for the cassette format: body encoding choices, validation, and
// atomic save/load. These pin the on-disk schema — a change that breaks any
// of them breaks every committed cassette in the wild.
package cassette

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewBodyStoresCleanTextReadably(t *testing.T) {
	b := NewBody([]byte("hello world\n"))
	if b.Text != "hello world\n" || b.B64 != "" || b.Size != 12 {
		t.Fatalf("unexpected body: %+v", b)
	}
	// Tabs and CRLF are ordinary tool output and must stay readable text.
	if b := NewBody([]byte("a\tb\r\nc")); b.Text == "" || b.B64 != "" {
		t.Fatalf("tab/CRLF text should stay text: %+v", b)
	}
}

func TestNewBodyBase64EncodesBinaryAndANSI(t *testing.T) {
	raw := []byte{0x00, 0x01, 0xff, 0xfe}
	b := NewBody(raw)
	if b.Text != "" || b.B64 == "" {
		t.Fatalf("binary should be base64: %+v", b)
	}
	got, err := base64.StdEncoding.DecodeString(b.B64)
	if err != nil || string(got) != string(raw) {
		t.Fatalf("base64 roundtrip failed: %v %q", err, got)
	}
	// Terminal colors are valid UTF-8 but contain ESC; storing them as
	// "text" would hide invisible bytes from cassette reviewers.
	if b := NewBody([]byte("\x1b[31mred\x1b[0m")); b.B64 == "" {
		t.Fatalf("ANSI escapes must be base64-encoded: %+v", b)
	}
}

func TestNewBodyEmptyIsTextWithZeroBytes(t *testing.T) {
	b := NewBody(nil)
	if b.Text != "" || b.B64 != "" || b.Size != 0 {
		t.Fatalf("empty body should be all-zero: %+v", b)
	}
	data, err := b.Data()
	if err != nil || len(data) != 0 {
		t.Fatalf("empty body should decode to empty: %v %q", err, data)
	}
}

func TestBodyRejectsInconsistentEncodings(t *testing.T) {
	if _, err := (Body{Text: "a", B64: "YQ==", Size: 1}).Data(); err == nil {
		t.Fatal("body with both text and base64 must be rejected")
	}
	if err := (Body{Text: "abc", Size: 5}).Check(); err == nil {
		t.Fatal("size mismatch must fail Check")
	}
}

func TestBodyRoundTripsAllByteValues(t *testing.T) {
	raw := make([]byte, 256)
	for i := range raw {
		raw[i] = byte(i)
	}
	got, err := NewBody(raw).Data()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(raw) {
		t.Fatal("binary body did not roundtrip byte-identically")
	}
}

func TestInteractionArgvJoinsCommandAndArgs(t *testing.T) {
	in := Interaction{Command: "git", Args: []string{"status", "--porcelain"}}
	if got := in.Argv(); got != "git status --porcelain" {
		t.Fatalf("Argv = %q", got)
	}
}

func testCassette() *Cassette {
	return &Cassette{
		Format:     FormatVersion,
		Version:    "0.1.0",
		RecordedAt: "2026-07-13T00:00:00Z",
		Command:    []string{"sh", "deploy.sh"},
		Interactions: []Interaction{
			{Command: "git", Args: []string{"rev-parse", "HEAD"},
				Stdout: NewBody([]byte("deadbeef\n")), Stderr: NewBody(nil)},
			{Command: "docker", Args: []string{"build", "."},
				Stdout: NewBody(nil), Stderr: NewBody([]byte("warn\n")), ExitCode: 1},
		},
	}
}

func TestSaveThenLoadRoundTripsAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.json")
	if err := testCassette().Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Interactions) != 2 || got.Interactions[0].Command != "git" {
		t.Fatalf("roundtrip lost data: %+v", got)
	}
	out, err := got.Interactions[0].Stdout.Data()
	if err != nil || string(out) != "deadbeef\n" {
		t.Fatalf("stdout body lost: %v %q", err, out)
	}
	data, _ := os.ReadFile(path)
	if !strings.HasSuffix(string(data), "\n") {
		t.Fatal("cassette file must end with a newline")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("atomic save leaked temp files: %v", entries)
	}
}

func TestLoadRejectsWrongFormatAndGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	os.WriteFile(path, []byte(`{"pathshim_cassette": 99, "interactions": []}`), 0o644)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "format 99") {
		t.Fatalf("format 99 must be rejected with a clear message, got %v", err)
	}
	os.WriteFile(path, []byte("not json"), 0o644)
	if _, err := Load(path); err == nil {
		t.Fatal("garbage must not load")
	}
}

func TestValidateRejectsBrokenInteractions(t *testing.T) {
	c := testCassette()
	c.Interactions[0].Command = ""
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "#1") {
		t.Fatalf("empty command must name the interaction, got %v", err)
	}
	c = testCassette()
	c.Interactions[1].Command = "usr/bin/docker"
	if err := c.Validate(); err == nil {
		t.Fatal("command with path separator must be rejected")
	}
	c = testCassette()
	c.Interactions[0].Stdout = Body{B64: "!!!not-base64!!!", Size: 3}
	if err := c.Validate(); err == nil {
		t.Fatal("corrupt base64 body must be rejected")
	}
}

func TestCommandsSortedAndUnique(t *testing.T) {
	c := testCassette()
	c.Interactions = append(c.Interactions, Interaction{
		Command: "git", Stdout: NewBody(nil), Stderr: NewBody(nil)})
	got := c.Commands()
	if len(got) != 2 || got[0] != "docker" || got[1] != "git" {
		t.Fatalf("Commands = %v", got)
	}
}
