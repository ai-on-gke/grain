// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package mcp

// Bounded collection of a command's output.
//
// Every runner here used to collect a command's two streams into a
// bytes.Buffer each and hand them back whole, and the cap on what a tool
// answer may carry (maxResultTextBytes) was only applied afterwards, in
// Registry.handleCall. Nothing bounded the collecting itself: a read_file of
// /dev/zero on a remote sandbox (a `cat` with no timeout of its own) or a
// run_command of `cat /dev/zero` streamed into this process's heap until it
// died -- and under a model executor this process is the daemon, so one tool
// call from any run took every other run down with it (task 2801's audit,
// reproduced at 4 GB of heap in three seconds).
//
// So the runners collect into a headTailBuffer instead: the first and last
// halves of execOutputLimit bytes, and a count of what fell between them.

import (
	"fmt"
	"strings"
	"sync"
)

// execOutputLimit is the most of one stream a runner keeps in memory.
//
// Generous on purpose. These runners are not only the MCP tools' transport:
// the orchestrator reads files, transcripts and CI job output through them,
// and those callers take what comes back as the whole of it. Anything up to
// this size still arrives byte for byte; past it the middle is dropped with
// a marker saying so, which no tool answer (capped far lower, at
// maxResultTextBytes) could have shown anyway. The point is a fixed ceiling,
// not a tight one.
//
// A var rather than a constant so a test can shrink it.
var execOutputLimit = 64 << 20

// headTailBuffer is an io.Writer that keeps the first half of limit bytes
// written to it and the most recent other half, and counts what it dropped
// between them -- boundedBuffer's head-only bound, made to keep a tail as
// well, because the end of a command's output (the error, the summary line,
// the exit report) is usually the half worth reading, and elideMiddle cuts
// a head and a tail out of whatever this returns.
//
// Concurrency-safe for the same reason as boundedBuffer: os/exec drains
// stdout and stderr on goroutines of its own.
type headTailBuffer struct {
	mu    sync.Mutex
	limit int
	head  []byte
	// tail grows by append until it holds tailCap bytes, then becomes a
	// ring whose oldest byte is at start.
	tail  []byte
	start int
	total int64
}

func newHeadTailBuffer() *headTailBuffer {
	return &headTailBuffer{limit: execOutputLimit}
}

func (b *headTailBuffer) headCap() int { return b.limit / 2 }
func (b *headTailBuffer) tailCap() int { return b.limit - b.headCap() }

func (b *headTailBuffer) Write(p []byte) (int, error) {
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
		// The whole tail is replaced by this one write's last bytes.
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

// headTailMarker is the line String puts where the middle was.
const headTailMarker = "\n... [%d bytes of output dropped: grain keeps the first and last %d bytes of a command's output] ...\n"

// runnerDropped reads back what a headTailBuffer dropped from out, which
// String marked at exactly the end of its head, and the length of that
// marker: 0, 0 when out is not output a runner cut.
func runnerDropped(out string) (dropped int64, markerLen int) {
	head := execOutputLimit / 2
	if len(out) <= head {
		return 0, 0
	}
	rest := out[head:]
	var keeps int
	if n, err := fmt.Sscanf(rest, strings.TrimSuffix(headTailMarker, "] ...\n"), &dropped, &keeps); n != 2 || err != nil || keeps != head {
		return 0, 0
	}
	marker := fmt.Sprintf(headTailMarker, dropped, keeps)
	if !strings.HasPrefix(rest, marker) {
		return 0, 0
	}
	return dropped, len(marker)
}

// String returns everything written when it fit in limit bytes, and
// otherwise the head, a one-line marker naming how much was dropped, and
// the tail.
func (b *headTailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	kept := len(b.head) + len(b.tail)
	dropped := b.total - int64(kept)
	out := make([]byte, 0, kept+128)
	out = append(out, b.head...)
	if dropped > 0 {
		out = fmt.Appendf(out, headTailMarker, dropped, b.limit/2)
	}
	out = append(out, b.tail[b.start:]...)
	out = append(out, b.tail[:b.start]...)
	return string(out)
}
