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

package orchestrator

import (
	"sync"
	"time"
)

// CycleTimes is how long this daemon's own RunCycle ticks have been
// taking: a fixed-size ring of the most recent cycles, written by
// RunCycle and read by whatever wants to report them (pkg/ui's
// GET /api/metrics, through cmd/grain's own adapter).
//
// **Why this is in memory rather than in the store.** Everything else
// grain measures is derived from rows that already exist -- pkg/metrics'
// own "nothing here is stored", and docs/data-model.md's "anything
// derivable is derived, never stored" behind it. A tick is the one thing
// that leaves no row at all: it reads the store, decides, and returns,
// so there is nothing to derive its duration from afterwards. That left
// two options, and this is the second:
//
//   - a row per cycle. Durable across restarts, and queryable over any
//     window a report asks for. It costs a new table, a write on every
//     single tick forever (2,880 a day at the default 30s
//     -poll-interval), and a growth curve nothing prunes -- to measure
//     something whose whole purpose is to say whether *this process,
//     right now* is dispatching promptly. It would also make the
//     measurement change what it measures: a tick that writes a row is a
//     tick with a store write in it.
//   - a ring in the process. Bounded memory, no schema, no write, and
//     nothing stored -- which keeps the doctrine above intact rather than
//     carving an exception into it. It is lost on restart, and that is
//     the honest cost: tick history belongs to the process that produced
//     it, and a daemon that has just restarted has a fresh tick anyway,
//     with none of the accumulated store the slow tick this exists to
//     catch would have been slow because of.
//
// The question this answers is "is the tick itself the reason tasks are
// waiting?", which is a question about now. A week of tick history would
// not answer it better; it would only cost a table.
//
// A CycleTimes is safe for concurrent use. Its zero value works -- the
// ring is allocated on first write -- so a test can use new(CycleTimes)
// without a constructor.
type CycleTimes struct {
	mu       sync.Mutex
	ring     []CycleTiming
	next     int
	full     bool
	observed int
}

// DefaultCycleHistory is how many cycles a CycleTimes remembers: six
// hours at the default 30s -poll-interval, which is long enough to show
// a tick that has been growing all morning and short enough that the
// whole ring is a few hundred kilobytes.
const DefaultCycleHistory = 720

// CycleTiming is one RunCycle tick, as that cycle measured itself.
//
// Start is the cycle's own logical `now` (RunCycle's parameter, which a
// deployment passes time.Now().UTC()), not a monotonic reading: it is
// what a report windows against, alongside the store timestamps every
// other measurement uses. Every duration here is measured monotonically.
type CycleTiming struct {
	Start time.Time
	// Commits is how many successful Store write transactions landed
	// while this RunCycle call was in flight. A zero is the decisive case
	// for the task-list cache: the cycle itself did not invalidate it.
	Commits uint64
	// Duration is the whole tick: the stored-configuration refresh at the
	// top of RunCycle plus every reconciler, and so the number the
	// -poll-interval between two ticks is really added to.
	Duration time.Duration
	// DispatchWait is how far into the cycle the dispatch reconciler
	// began -- everything RunCycle did before it was in a position to
	// decide what runs now. It is the tick's own share of a task's
	// queue_wait (pkg/metrics' latency stage of that name): a task
	// approved just after one cycle's dispatch decision waits a
	// -poll-interval plus this before the next one reaches it.
	//
	// Recorded here, rather than left to a reader to pick out of
	// Reconcilers below by name, because this package is the one that
	// knows which reconciler is the dispatch decision -- it names them.
	// Zero for a cycle that never reached dispatch at all.
	DispatchWait time.Duration
	// Reconcilers is where the rest of the tick went, in the order
	// Reconcilers() ran them. This is the part a single tick duration
	// cannot tell you: the reconcilers run in sequence, so a
	// pull-request sync that has grown to a minute is a minute the
	// reconcilers behind it did not get to spend, and only a per-
	// reconciler number says which one it was.
	Reconcilers []ReconcilerTiming
}

// ReconcilerTiming is one reconciler's share of one cycle.
type ReconcilerTiming struct {
	Name string
	// Wait is how far into the cycle this reconciler started -- the sum
	// of everything before it, which is what it actually costs whatever
	// runs after it.
	Wait time.Duration
	// Duration is how long it took.
	Duration time.Duration
	// Failed reports whether it returned an error (or panicked, which
	// recoverReconcile turns into one). A reconciler that fails fast is
	// not the same as one that is quick, and a duration alone cannot
	// tell them apart.
	Failed bool
}

// NewCycleTimes builds a CycleTimes remembering size cycles; size <= 0
// takes DefaultCycleHistory.
func NewCycleTimes(size int) *CycleTimes {
	if size <= 0 {
		size = DefaultCycleHistory
	}
	return &CycleTimes{ring: make([]CycleTiming, size)}
}

// Observe records one finished cycle, overwriting the oldest once the
// ring is full. A nil *CycleTimes drops it, so RunCycle needs no guard
// of its own for a caller that wants no measurement (every test that
// builds Deps by hand).
func (c *CycleTimes) Observe(t CycleTiming) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.ring) == 0 {
		c.ring = make([]CycleTiming, DefaultCycleHistory)
	}
	c.ring[c.next] = t
	c.next++
	if c.next == len(c.ring) {
		c.next, c.full = 0, true
	}
	c.observed++
}

// History returns the cycles still in the ring, oldest first, and how
// many this process has run in total -- every one, including those the
// ring has since forgotten. The two come back together, under one lock,
// because the second is only useful read against the first: observed
// above len(cycles) is what says a report is looking at a truncated tail
// of this process's history rather than the whole of it, and reading
// them separately would let a tick land in between and make that
// comparison say so when it isn't true.
//
// The returned slice, and the CycleTiming values in it, are the caller's
// own; each CycleTiming.Reconcilers slice is shared with the ring rather
// than copied. RunCycle builds a fresh one per cycle and never touches
// it again after Observe, and every reader of one only reads, so copying
// several hundred of them per report would buy nothing.
func (c *CycleTimes) History() (cycles []CycleTiming, observed int) {
	if c == nil {
		return nil, 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.next
	if c.full {
		n = len(c.ring)
	}
	out := make([]CycleTiming, 0, n)
	if c.full {
		out = append(out, c.ring[c.next:]...)
	}
	return append(out, c.ring[:c.next]...), c.observed
}
