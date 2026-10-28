package steps

// What a step has printed *so far*, while it is still printing it.
//
// Everything else in this package reports a step once it is over: runSteps
// records a StepRun with its output when Exec returns, and the progress
// snapshot a running step produces carries a name, a start time and
// nothing to read. That is fine for a two-second step and useless for the
// forty-minute `go test` a CI job usually is -- the whole of what somebody
// watching a run wants is the line it is on now, and until this existed
// there was no line at all until the step ended.
//
// So a step that is running publishes what it has printed, on a clock. The
// two halves of that sentence are both deliberate:
//
//   - published through the same progress callback a step start already
//     goes through (Engine.runJob's progress, Service's observer, the run
//     store), so a live step reaches every reader that already exists --
//     the CI pane's follow-the-output view, `grain ci show`, and the
//     agent's own get_ci_log -- rather than needing a second transport;
//   - on a clock, because that callback ends in a database write
//     (StoredRunStore.Update) and a step printing a line a millisecond
//     would otherwise turn one run into thousands of writes. LiveOutputInterval
//     is the cadence, and nothing is published between ticks.
//
// A sandbox that cannot stream (ci.Sandbox without ExecStream) is not
// made to: the step simply reports itself when it finishes, exactly as it
// always did.

import (
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/workflows"
)

// LiveOutputInterval is how often a step still running publishes what it
// has printed so far.
//
// Five seconds, which is the cadence everything that reads a live run
// already ticks at: the CI pane polls every five (CIPage's REFRESH_MS)
// and so does `grain ci run -watch` (ciWatchInterval), so publishing
// faster would buy a reader nothing they could see.
//
// And publishing faster is not free, which is the other half of the
// choice. Every publication is a progress callback, and on a deployment
// with a database behind its runs that callback ends in a read of the run
// and a write of it (StoredRunStore.Update). The write is a narrow one --
// a publication changes one step's `output` and is written as one UPDATE
// of that cell rather than as the whole-run replacement every other kind
// of progress gets -- but a transaction is still a transaction, and a
// per-line publication would turn one chatty forty-minute job into tens
// of thousands of them. Five seconds bounds it at a few hundred per job,
// which is the same order as the writes a task run's own activity already
// costs -- and it is why nothing here publishes on a timer of its own: a
// step that prints nothing writes nothing.
const LiveOutputInterval = 5 * time.Second

// LiveOutput is one running step's output as it accumulates, and the
// decision of when that is worth telling anybody about.
//
// Bounded the same way a finished step's output is (MaxStepOutput, head
// and tail kept, middle dropped): a step that prints a gigabyte must cost
// the daemon watching it no more than a step that prints a page, and the
// two ends are the ones that carry meaning -- the head says what the step
// set out to do and the tail is where it has got to.
//
// Safe to write to from the goroutine a transport happens to read a pipe
// on, which is where the chunks come from, and closed by the engine
// before the step's real result is recorded so that nothing arrives after
// the last word on the step.
type LiveOutput struct {
	now      func() time.Time
	interval time.Duration
	publish  func(string)

	mu      sync.Mutex
	closed  bool
	last    time.Time
	head    []byte
	tail    []byte
	dropped int
}

// liveHeadBytes and liveTailBytes split MaxStepOutput the way
// TruncateOutputTo splits it, so what a reader follows while the step runs
// has the same shape as what they are left with when it ends.
const (
	liveHeadBytes = workflows.MaxStepOutput / 4
	liveTailBytes = workflows.MaxStepOutput - liveHeadBytes
)

// liveTailSlack is how far past the tail bound the buffer is allowed to
// grow before it is compacted. Trimming on every write would memmove the
// whole tail for each chunk a pipe delivers; trimming at twice the bound
// makes that cost amortized while still holding the memory to a fixed
// multiple of what the step is allowed to keep.
const liveTailSlack = 2 * liveTailBytes

// NewLiveOutput returns a sink that calls publish with the step's output
// so far, no more often than interval, and never after close.
func NewLiveOutput(now func() time.Time, interval time.Duration, publish func(string)) *LiveOutput {
	if now == nil {
		now = time.Now
	}
	if interval <= 0 {
		interval = LiveOutputInterval
	}
	// last starts at the first write rather than at zero, so a step is not
	// published the instant it prints its first byte and then again a
	// whole interval later: what a reader wants is the newest line, and
	// the first tick is where the first useful one is.
	return &LiveOutput{now: now, interval: interval, publish: publish}
}

// Write records a chunk of output and publishes the step if the interval
// has passed since the last time it did.
func (l *LiveOutput) Write(chunk string) {
	if l == nil || chunk == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	l.append(chunk)
	now := l.now()
	if l.last.IsZero() {
		l.last = now
		return
	}
	if now.Sub(l.last) < l.interval {
		return
	}
	l.last = now
	if l.publish != nil {
		l.publish(l.text())
	}
}

// Close stops the sink. The step's real output is recorded by the engine
// the moment the command returns, so there is deliberately no final
// publication here: it would be the same text arriving twice, the second
// time without the exit status that makes it worth reading.
func (l *LiveOutput) Close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
}

// append adds chunk to the buffer, filling the head first and keeping the
// tail bounded. Called with the lock held.
func (l *LiveOutput) append(chunk string) {
	b := []byte(chunk)
	if room := liveHeadBytes - len(l.head); room > 0 {
		n := len(b)
		if n > room {
			n = room
		}
		l.head = append(l.head, b[:n]...)
		b = b[n:]
	}
	if len(b) == 0 {
		return
	}
	l.tail = append(l.tail, b...)
	if len(l.tail) > liveTailSlack {
		over := len(l.tail) - liveTailBytes
		copy(l.tail, l.tail[over:])
		l.tail = l.tail[:liveTailBytes]
		l.dropped += over
	}
}

// text is the step's output as a reader should see it now: the head, what
// has been dropped from the middle said in the same words a finished step
// says it in, and the newest lines. Called with the lock held.
func (l *LiveOutput) text() string {
	if l.dropped == 0 {
		return string(l.head) + string(l.tail)
	}
	return string(l.head) + "\n\n[grain ci] " + workflows.ElisionNote(l.dropped) + "\n\n" + string(l.tail)
}
