package orchestrator

import (
	"context"
	"sort"
	"sync"
)

// InFlight is the set of runs a cycle has started and that have not
// ended yet: the thing that lets RunCycle hand a dispatch off and return
// while the agent it started is still working.
//
// A cycle used to wait for every run it dispatched before it returned
// (reconcileDispatch's own sync.WaitGroup), and cmd/grain's reconcile
// loop waits for a cycle before it ticks again -- so one long run held
// the whole controller: the next tick, and with it every other
// reconciler and every dispatch of the capacity still free, could not
// happen until that agent finished. A deployment configured for several
// concurrent runs only ever reached that number when a single tick
// happened to find several tasks ready at once; a task filed a second
// after a run started waited out the whole run, no matter how much of
// -max-workers was idle. That is what this fixes: the runs of a cycle
// outlive the cycle, and the next tick dispatches into whatever
// headroom is free at that moment.
//
// It is not itself the concurrency limit, and deliberately keeps no
// count anything enforces. model.Limits is the limit, and
// dispatch.Cycle enforces it from the store -- LiveRunCount, re-checked
// inside StartRun's own transaction -- which stays accurate across ticks
// exactly because a run's row stays live until the goroutine tracked
// here finishes it. Nothing would be gained by having a second, in-
// memory notion of "how many are running" that a restart forgets and the
// store does not.
//
// What it is for is waiting: a daemon shutting down (cmd/grain's own
// drainInFlight) and a test that dispatched asynchronously both need to
// know when the runs are done, and neither can ask the store, whose row
// is finished a moment before the goroutine that finishes it returns
// from releasing the sandbox.
//
// The zero value is ready to use. Every method is safe for concurrent
// callers.
type InFlight struct {
	mu      sync.Mutex
	live    map[string]string // run ID -> task ID
	waiters []chan struct{}
}

// Go runs fn on its own goroutine, counting run runID (of task taskID)
// as live from before that goroutine starts until fn returns. Registering
// before the goroutine is what makes a Wait immediately after a cycle
// see the runs that cycle just started, rather than racing the scheduler.
func (f *InFlight) Go(runID, taskID string, fn func()) {
	f.mu.Lock()
	if f.live == nil {
		f.live = make(map[string]string)
	}
	f.live[runID] = taskID
	f.mu.Unlock()

	go func() {
		defer f.done(runID)
		fn()
	}()
}

// done drops runID and wakes every outstanding Wait once nothing is left.
func (f *InFlight) done(runID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.live, runID)
	if len(f.live) > 0 {
		return
	}
	for _, ch := range f.waiters {
		close(ch)
	}
	f.waiters = nil
}

// Busy reports whether taskID has a run this set is still working on --
// which lasts until the run's own result has been turned into what it
// implies (runOne returns only after ProcessResult and after the sandbox
// is released), rather than until its row is finished.
//
// That difference is the whole point of the method: dispatch.Cycle
// (which takes this as dispatch.Busy) would otherwise dispatch a task a
// second time in the window between the two -- see dispatch.Busy's own
// doc comment.
func (f *InFlight) Busy(taskID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range f.live {
		if id == taskID {
			return true
		}
	}
	return false
}

// Len is how many runs are live right now.
func (f *InFlight) Len() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.live)
}

// Runs is the ID of every live run, sorted -- for a log line naming what
// a shutdown is still waiting on.
func (f *InFlight) Runs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.live))
	for runID := range f.live {
		out = append(out, runID)
	}
	sort.Strings(out)
	return out
}

// Wait blocks until no run is live, or ctx is done -- returning ctx's own
// error in the second case, so a caller can tell a drain that finished
// from one that ran out of time.
//
// It waits for the runs live when it is called *and* for any started
// while it waits: "nothing is live" is the condition, not "these
// particular runs ended". A caller that wants the first meaning stops
// dispatching before it waits, which is what a daemon shutting down does
// by cancelling the context its reconcile loop reads.
//
// A Wait that gives up on ctx leaves its channel in waiters until the
// next time the set empties, which closes it with nobody listening. That
// is a bounded, harmless leak -- one channel per abandoned Wait, and a
// Wait is a shutdown or a test, not something a cycle does.
func (f *InFlight) Wait(ctx context.Context) error {
	f.mu.Lock()
	if len(f.live) == 0 {
		f.mu.Unlock()
		return nil
	}
	ch := make(chan struct{})
	f.waiters = append(f.waiters, ch)
	f.mu.Unlock()

	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
