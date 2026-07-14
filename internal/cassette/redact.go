// Redaction: recorded output often contains tokens, temporary URLs, or
// hostnames that must not land in a cassette committed to version control.
// A Redactor applies user-supplied regular expressions to captured stdin,
// stdout, and stderr at record time, replacing every match with [REDACTED].
// Argv is intentionally not redacted — replay matches on argv, and rewriting
// it would silently break every future match.
package cassette

import (
	"encoding/json"
	"fmt"
	"regexp"
)

// Placeholder is what every redacted span becomes.
const Placeholder = "[REDACTED]"

// Redactor applies an ordered list of compiled patterns.
type Redactor struct {
	patterns []*regexp.Regexp
}

// NewRedactor compiles exprs; an empty list yields a no-op redactor.
func NewRedactor(exprs []string) (*Redactor, error) {
	r := &Redactor{}
	for _, e := range exprs {
		re, err := regexp.Compile(e)
		if err != nil {
			return nil, fmt.Errorf("invalid --redact pattern %q: %w", e, err)
		}
		r.patterns = append(r.patterns, re)
	}
	return r, nil
}

// Apply replaces every pattern match in b with the placeholder. The input
// slice is never mutated.
func (r *Redactor) Apply(b []byte) []byte {
	if r == nil || len(r.patterns) == 0 {
		return b
	}
	out := b
	for _, re := range r.patterns {
		out = re.ReplaceAll(out, []byte(Placeholder))
	}
	return out
}

// EncodePatterns packs patterns into a single environment-variable-safe
// string (JSON), because regular expressions may contain any delimiter we
// could otherwise pick.
func EncodePatterns(exprs []string) (string, error) {
	if len(exprs) == 0 {
		return "", nil
	}
	b, err := json.Marshal(exprs)
	return string(b), err
}

// DecodePatterns is the inverse of EncodePatterns; "" means no patterns.
func DecodePatterns(s string) ([]string, error) {
	if s == "" {
		return nil, nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, fmt.Errorf("invalid redact pattern list: %w", err)
	}
	return out, nil
}
