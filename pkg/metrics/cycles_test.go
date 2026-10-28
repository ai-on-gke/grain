package metrics_test

// The cycles section: the daemon's own tick, and the one part of a
// report that is handed in rather than derived, because a tick leaves no
// row to derive it from.

import (
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/metrics"
)

// tick is one cycle as a daemon would report it: started d into the
// window, taking took, with the dispatch decision reached dispatch in.
func tick(d, took, dispatch time.Duration) metrics.CycleSample {
	return metrics.CycleSample{
		Start:        since.Add(d),
		Duration:     took,
		DispatchWait: dispatch,
		Reconcilers: []metrics.ReconcilerSample{
			{Name: "schedule", Wait: time.Millisecond, Duration: dispatch - time.Millisecond},
			{Name: "dispatch", Wait: dispatch, Duration: 5 * time.Millisecond},
			{Name: "sync", Wait: dispatch + 5*time.Millisecond, Duration: took - dispatch - 5*time.Millisecond},
		},
	}
}

// The headline the section exists for: how long a tick takes, how often
// one happens, and how far in the dispatch decision is reached -- which
// together are the queue wait a task pays for grain's own scheduling
// with no contention involved at all.
func TestCyclesReportsTheTickAndWhenDispatchIsReached(t *testing.T) {
	history := metrics.CycleHistory{
		Observed: 3,
		Samples: []metrics.CycleSample{
			tick(0, 100*time.Millisecond, 20*time.Millisecond),
			tick(30*time.Second, 200*time.Millisecond, 40*time.Millisecond),
			tick(60*time.Second, 300*time.Millisecond, 60*time.Millisecond),
		},
	}
	// One working tick happened not to write, while the other two did.
	history.Samples[1].Commits = 2
	history.Samples[2].Commits = 5
	rep := metrics.Compute(metrics.Input{Window: win, Cycles: history})
	c := rep.Cycles

	if c.N != 3 || c.Observed != 3 {
		t.Fatalf("N = %d, Observed = %d; want 3 and 3", c.N, c.Observed)
	}
	if c.Truncated {
		t.Error("Truncated = true, but the daemon supplied every tick it ran")
	}
	if !c.First.Equal(since) || !c.Last.Equal(since.Add(60*time.Second)) {
		t.Errorf("span = %v -> %v, want the first and last tick actually covered", c.First, c.Last)
	}
	if got, want := c.Tick.P50, 200*time.Millisecond; got != want {
		t.Errorf("Tick.P50 = %v, want %v", got, want)
	}
	if got, want := c.DispatchWait.P50, 40*time.Millisecond; got != want {
		t.Errorf("DispatchWait.P50 = %v, want %v", got, want)
	}
	// Two gaps between three ticks -- the first tick has nothing before
	// it to be measured against.
	if got, want := c.Interval.N, 2; got != want {
		t.Errorf("Interval.N = %d, want %d", got, want)
	}
	if got, want := c.Interval.P50, 30*time.Second; got != want {
		t.Errorf("Interval.P50 = %v, want the loop's real period (%v)", got, want)
	}
	if got, want := c.CommitFree, 1; got != want {
		t.Errorf("CommitFree = %d, want %d", got, want)
	}
	if got, want := c.Commits.P50, int64(2); got != want {
		t.Errorf("Commits.P50 = %d, want %d", got, want)
	}
}

// A tick's time is broken down per reconciler, in the order the cycle
// ran them, because they run in sequence: a sync that has grown to a
// minute is a minute everything behind it did not get to spend, and one
// tick duration cannot say which one grew.
func TestCyclesBreaksTheTickDownPerReconciler(t *testing.T) {
	slow := tick(0, time.Minute, 20*time.Millisecond)
	slow.Reconcilers[2].Duration = 59 * time.Second
	slow.Reconcilers[2].Failed = true

	rep := metrics.Compute(metrics.Input{
		Window: win,
		Cycles: metrics.CycleHistory{Observed: 1, Samples: []metrics.CycleSample{slow}},
	})

	var names []string
	byName := map[string]metrics.ReconcilerCycles{}
	for _, r := range rep.Cycles.Reconcilers {
		names = append(names, r.Name)
		byName[r.Name] = r
	}
	if len(names) != 3 || names[0] != "schedule" || names[1] != "dispatch" || names[2] != "sync" {
		t.Fatalf("reconcilers = %v, want them in the order the cycle ran them", names)
	}
	if got, want := byName["sync"].Duration.Max, 59*time.Second; got != want {
		t.Errorf("sync duration max = %v, want %v", got, want)
	}
	if byName["sync"].Failures != 1 {
		t.Errorf("sync failures = %d, want 1 -- a reconciler that is fast because it failed is not a fast reconciler",
			byName["sync"].Failures)
	}
	if byName["dispatch"].Failures != 0 {
		t.Errorf("dispatch failures = %d, want 0", byName["dispatch"].Failures)
	}
	// The dispatch reconciler's own Wait is the same series the headline
	// DispatchWait carries; the headline exists because only the daemon
	// knows which name means "the decision a queued task waits for".
	if got, want := byName["dispatch"].Wait.P50, rep.Cycles.DispatchWait.P50; got != want {
		t.Errorf("dispatch Wait.P50 = %v, want it to agree with DispatchWait.P50 (%v)", got, want)
	}
}

// A tick belongs to the window on the same rule every other measurement
// here follows: the moment it *ended* falls inside. Observed keeps
// counting past what the ring still holds, which is what Truncated
// reports -- a fact about the history supplied, not about the window.
func TestCyclesWindowsOnWhenATickEndedAndReportsATruncatedRing(t *testing.T) {
	rep := metrics.Compute(metrics.Input{
		Window: win,
		Cycles: metrics.CycleHistory{
			// The daemon has run 5,000 ticks and remembers three.
			Observed: 5000,
			Samples: []metrics.CycleSample{
				// Began before the window and was still running at
				// Since: it ended inside, so it counts in full.
				tick(-time.Second, 2*time.Second, 20*time.Millisecond),
				tick(time.Hour, 100*time.Millisecond, 30*time.Millisecond),
				// Ends after the window: in the ring, out of the report.
				tick(8*24*time.Hour, 100*time.Millisecond, 40*time.Millisecond),
			},
		},
	})
	c := rep.Cycles

	if c.N != 2 {
		t.Errorf("N = %d, want the two ticks that ended inside the window", c.N)
	}
	if c.Observed != 5000 || !c.Truncated {
		t.Errorf("Observed = %d, Truncated = %v; want 5000 and true", c.Observed, c.Truncated)
	}
	if got, want := c.Tick.Max, 2*time.Second; got != want {
		t.Errorf("Tick.Max = %v, want the straddling tick counted in full (%v)", got, want)
	}
	if !c.First.Equal(since.Add(-time.Second)) {
		t.Errorf("First = %v, want the straddling tick's own start", c.First)
	}
}

// A report with no daemon behind it -- the CLI computing one from rows
// alone, a test, a UI not colocated with a reconcile loop -- reports no
// ticks rather than failing. Compute cannot tell that from a daemon that
// has simply not ticked yet; pkg/ui's Enabled is what does.
func TestCyclesOverNoHistoryIsEmpty(t *testing.T) {
	c := metrics.Compute(metrics.Input{Window: win}).Cycles
	if c.N != 0 || c.Observed != 0 || c.Truncated {
		t.Errorf("cycles over no history = %+v, want zeroes", c)
	}
	if c.Tick.N != 0 || c.DispatchWait.N != 0 || c.Interval.N != 0 || len(c.Reconcilers) != 0 {
		t.Errorf("distributions over no history = %+v, want zeroes", c)
	}
}
