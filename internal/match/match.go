// Package match decides which recorded interaction answers a live shim
// call during replay. Matching is pure and deterministic: it sees only the
// cassette, the consumed-set, and the incoming call — never the clock, the
// filesystem, or the network — so the same test run always replays the same
// way.
package match

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/JaydenCJ/pathshim/internal/cassette"
)

// Call is one live invocation arriving at a replay shim.
type Call struct {
	Command string
	Args    []string
	// Stdin is the fully drained standard input, or nil when stdin was a
	// terminal or stdin matching is disabled.
	Stdin []byte
	// EnvLookup resolves an environment variable in the calling process;
	// nil disables env comparison even when Options.MatchEnv is set.
	EnvLookup func(string) (string, bool)
}

// Options select how strict matching is.
type Options struct {
	// Ordered requires calls to arrive in exactly the recorded sequence:
	// the next unconsumed interaction must match or the call is a miss.
	Ordered bool
	// MatchStdin additionally compares drained stdin against the recorded
	// stdin. Interactions recorded without captured stdin match any input.
	MatchStdin bool
	// MatchEnv additionally requires every environment variable recorded
	// for the interaction to have the same value in the calling process.
	MatchEnv bool
}

// Find returns the index of the interaction that answers call, or ok=false
// when nothing matches. Unordered mode consumes the earliest unconsumed
// match, which keeps repeated identical calls (e.g. two `git rev-parse
// HEAD` at different steps) replaying in recorded order.
func Find(inters []cassette.Interaction, consumed []bool, call Call, opts Options) (int, bool) {
	if opts.Ordered {
		for i := range inters {
			if consumed[i] {
				continue
			}
			if matches(inters[i], call, opts) {
				return i, true
			}
			return 0, false
		}
		return 0, false
	}
	for i := range inters {
		if consumed[i] {
			continue
		}
		if matches(inters[i], call, opts) {
			return i, true
		}
	}
	return 0, false
}

func matches(in cassette.Interaction, call Call, opts Options) bool {
	if in.Command != call.Command {
		return false
	}
	if !argsEqual(in.Args, call.Args) {
		return false
	}
	if opts.MatchStdin && in.Stdin != nil {
		want, err := in.Stdin.Data()
		if err != nil {
			return false
		}
		if !bytes.Equal(want, call.Stdin) {
			return false
		}
	}
	if opts.MatchEnv && call.EnvLookup != nil {
		for k, v := range in.Env {
			got, ok := call.EnvLookup(k)
			if !ok || got != v {
				return false
			}
		}
	}
	return true
}

func argsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Explain builds a human-readable diagnosis for a miss: the call that
// arrived, followed by the closest recordings for the same command so the
// user can see which argument diverged or which interaction was already
// consumed. At most three candidates are shown.
func Explain(inters []cassette.Interaction, consumed []bool, call Call) string {
	var b strings.Builder
	fmt.Fprintf(&b, "  wanted: %s\n", argvLine(call.Command, call.Args))

	type cand struct {
		idx   int
		score int
	}
	var cands []cand
	for i, in := range inters {
		if in.Command != call.Command {
			continue
		}
		score := 1 + 2*commonPrefix(in.Args, call.Args)
		if len(in.Args) == len(call.Args) {
			score++
		}
		cands = append(cands, cand{i, score})
	}
	if len(cands) == 0 {
		fmt.Fprintf(&b, "  the cassette has no %q recordings at all\n", call.Command)
		return b.String()
	}
	// Highest score first; ties keep cassette order, so output is stable
	// and deterministic.
	sort.SliceStable(cands, func(i, j int) bool {
		return cands[i].score > cands[j].score
	})
	if len(cands) > 3 {
		cands = cands[:3]
	}
	fmt.Fprintf(&b, "  closest %q recordings:\n", call.Command)
	for _, c := range cands {
		state := "not yet consumed"
		if consumed[c.idx] {
			state = "already consumed"
		}
		fmt.Fprintf(&b, "    #%d %s (exit %d) — %s\n",
			c.idx+1, inters[c.idx].Argv(), inters[c.idx].ExitCode, state)
	}
	return b.String()
}

func argvLine(cmd string, args []string) string {
	return strings.Join(append([]string{cmd}, args...), " ")
}

func commonPrefix(a, b []string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}
