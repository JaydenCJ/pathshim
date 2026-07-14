// capBuffer: a bounded, concurrency-safe capture sink. Streams keep flowing
// to the real destinations at full length; only the *recorded copy* is
// capped, so shimming a tool that prints gigabytes never balloons the
// cassette or the shim's memory.
package shim

import "sync"

type capBuffer struct {
	mu    sync.Mutex
	max   int
	buf   []byte
	total int
}

func newCapBuffer(max int) *capBuffer {
	return &capBuffer{max: max}
}

// Write never fails (so it is safe inside io.MultiWriter chains); it stores
// at most max bytes but counts everything.
func (b *capBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	b.total += n
	if room := b.max - len(b.buf); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		b.buf = append(b.buf, p...)
	}
	return n, nil
}

// Snapshot returns a copy of the captured bytes and whether anything was
// dropped by the cap.
func (b *capBuffer) Snapshot() (data []byte, truncated bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]byte, len(b.buf))
	copy(out, b.buf)
	return out, b.total > len(b.buf)
}
