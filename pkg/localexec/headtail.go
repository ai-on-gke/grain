package localexec

// Adapted from github.com/bwsalmon/silo old/internal/headtail, itself
// adapted from this repository's pkg/mcp/exec_output.go.

import (
	"fmt"
	"sync"
)

// headTail is a bounded output buffer, an io.Writer: it keeps the first
// and last halves of its limit of whatever is written to it and counts
// what it dropped between them, so a script that prints without end costs
// a bounded amount of memory and still shows how it began and how it
// ended. It is safe for concurrent use.
type headTail struct {
	mu    sync.Mutex
	limit int
	head  []byte
	tail  []byte // a ring once full
	start int    // index of the oldest tail byte
	total int64
}

// newHeadTail returns a headTail keeping at most limit bytes.
func newHeadTail(limit int) *headTail { return &headTail{limit: limit} }

func (b *headTail) headCap() int { return b.limit / 2 }
func (b *headTail) tailCap() int { return b.limit - b.headCap() }

func (b *headTail) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	b.total += int64(n)
	if room := b.headCap() - len(b.head); room > 0 {
		take := min(room, len(p))
		b.head = append(b.head, p[:take]...)
		p = p[take:]
	}
	if len(p) == 0 {
		return n, nil
	}
	tailCap := b.tailCap()
	if len(p) >= tailCap {
		b.tail = append(b.tail[:0], p[len(p)-tailCap:]...)
		b.start = 0
		return n, nil
	}
	if room := tailCap - len(b.tail); room > 0 {
		take := min(room, len(p))
		b.tail = append(b.tail, p[:take]...)
		p = p[take:]
	}
	for len(p) > 0 {
		c := copy(b.tail[b.start:], p)
		p = p[c:]
		b.start = (b.start + c) % tailCap
	}
	return n, nil
}

// Total is how many bytes were written.
func (b *headTail) Total() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.total
}

// Dropped is how many bytes were written but not kept.
func (b *headTail) Dropped() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.total - int64(len(b.head)+len(b.tail))
}

// Bytes returns what was kept, with a line marking the cut when
// anything was dropped.
func (b *headTail) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	kept := len(b.head) + len(b.tail)
	dropped := b.total - int64(kept)
	out := make([]byte, 0, kept+128)
	out = append(out, b.head...)
	if dropped > 0 {
		out = fmt.Appendf(out, "\n... [%d bytes of output dropped: grain keeps the first and last %d bytes] ...\n", dropped, b.limit/2)
	}
	out = append(out, b.tail[b.start:]...)
	out = append(out, b.tail[:b.start]...)
	return out
}
