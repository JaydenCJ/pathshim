// Tests for record-time redaction and the env-var pattern transport.
package cassette

import (
	"strings"
	"testing"
)

func TestRedactorReplacesEveryMatch(t *testing.T) {
	r, err := NewRedactor([]string{`tok_[a-z0-9]+`})
	if err != nil {
		t.Fatal(err)
	}
	got := string(r.Apply([]byte("token=tok_abc123 other=tok_zzz")))
	want := "token=" + Placeholder + " other=" + Placeholder
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	empty, err := NewRedactor(nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(empty.Apply([]byte("unchanged"))) != "unchanged" {
		t.Fatal("empty redactor must not modify input")
	}
}

func TestRedactorAppliesPatternsInOrder(t *testing.T) {
	r, err := NewRedactor([]string{`secret`, `\[REDACTED\]-suffix`})
	if err != nil {
		t.Fatal(err)
	}
	// The second pattern sees the output of the first — order matters and
	// is part of the contract.
	got := string(r.Apply([]byte("secret-suffix")))
	if got != Placeholder {
		t.Fatalf("got %q", got)
	}
}

func TestNewRedactorRejectsInvalidPattern(t *testing.T) {
	if _, err := NewRedactor([]string{"("}); err == nil || !strings.Contains(err.Error(), "--redact") {
		t.Fatal("invalid regexp must be rejected, naming the flag that carried it")
	}
}

func TestEncodeDecodePatternsRoundTrip(t *testing.T) {
	// Patterns can contain commas, quotes, and spaces — the transport must
	// survive all of them.
	exprs := []string{`a,b{1,2}`, `"quoted"`, `\s+`}
	enc, err := EncodePatterns(exprs)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodePatterns(enc)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] != exprs[0] || got[1] != exprs[1] || got[2] != exprs[2] {
		t.Fatalf("roundtrip lost patterns: %v", got)
	}
}

func TestDecodePatternsEdgeCases(t *testing.T) {
	got, err := DecodePatterns("")
	if err != nil || got != nil {
		t.Fatalf("empty transport should decode to nil, got %v %v", got, err)
	}
	if _, err := DecodePatterns("{broken"); err == nil {
		t.Fatal("garbage transport must be rejected")
	}
}
