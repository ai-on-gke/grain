// What a cycle records about itself, and the ring it records into.
//
// The point of all of it is one number a deployment could not otherwise
// get: how much of a task's queue wait was the tick rather than
// contention. pkg/metrics' queue_wait says a task waited; only
// CycleTiming.DispatchWait says whether there was anything else to wait
// for.
package orchestrator_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// A cycle handed a CycleTimes records itself into it: the whole tick,
// every reconciler in the order Reconcilers() runs them, and how far in
// the dispatch decision was reached.
func TestRunCycleRecordsItsOwnTiming(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)

	times := orchestrator.NewCycleTimes(8)
	deps := orchestrator.Deps{
		Store:      store,
		Client:     client,
		Sandboxes:  orchestrator.NewHostSandboxes(t.TempDir()),
		Framework:  completesWithAComment(),
		MaxWorkers: 1,
		CycleTimes: times,
	}
	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	// The cycle really did dispatch, so the timing below describes a
	// cycle that did the work rather than one that found nothing to do.
	// The scripted run answers in a comment and pushes nothing, so its
	// completion reads 'awaiting_ack' (model.AwaitsAck) -- the run being
	// over is the whole of what this asserts.
	if st := stateOf(t, ctx, store, task.ID); st != model.StateAwaitingReply {
		t.Fatalf("state = %q, want awaiting_reply", st)
	}

	cycles, observed := times.History()
	if len(cycles) != 1 || observed != 1 {
		t.Fatalf("History() = %d cycle(s), observed %d; want 1 and 1", len(cycles), observed)
	}
	got := cycles[0]
	if !got.Start.Equal(baseTime) {
		t.Errorf("Start = %v, want the cycle's own now (%v)", got.Start, baseTime)
	}
	if got.Duration <= 0 {
		t.Errorf("Duration = %v, want a positive tick duration", got.Duration)
	}
	if got.Commits == 0 {
		t.Error("Commits = 0, want the working cycle's store writes counted")
	}

	var names []string
	for _, r := range got.Reconcilers {
		names = append(names, r.Name)
	}
	var want []string
	for _, r := range orchestrator.Reconcilers() {
		want = append(want, r.Name)
	}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("reconcilers = %v, want every one, in the order the cycle runs them: %v", names, want)
	}

	// Wait is cumulative: each reconciler starts no earlier than the one
	// before it finished, and the last one finishes inside the tick.
	var previous time.Duration
	for _, r := range got.Reconcilers {
		if r.Wait < previous {
			t.Errorf("%s started at %v, before the reconciler ahead of it finished (%v) -- "+
				"Wait is meant to be the offset from the cycle's own start", r.Name, r.Wait, previous)
		}
		if r.Duration < 0 {
			t.Errorf("%s took %v", r.Name, r.Duration)
		}
		if r.Failed {
			t.Errorf("%s failed in a cycle that returned no error", r.Name)
		}
		previous = r.Wait + r.Duration
	}
	if previous > got.Duration {
		t.Errorf("reconcilers ran to %v, past the whole tick's %v", previous, got.Duration)
	}

	// The headline: DispatchWait is the dispatch reconciler's own start,
	// singled out because it is the part of a task's queue wait that is
	// the tick itself.
	var dispatch orchestrator.ReconcilerTiming
	for _, r := range got.Reconcilers {
		if r.Name == "dispatch" {
			dispatch = r
		}
	}
	if dispatch.Name == "" {
		t.Fatal("no dispatch reconciler recorded")
	}
	if got.DispatchWait != dispatch.Wait {
		t.Errorf("DispatchWait = %v, want the dispatch reconciler's own Wait (%v)", got.DispatchWait, dispatch.Wait)
	}
	if got.DispatchWait <= 0 {
		t.Errorf("DispatchWait = %v, want the stored-config read and the reconcilers ahead of dispatch to count", got.DispatchWait)
	}
}

// A reconciler that fails is recorded as having failed, not merely as
// having been quick: the two look identical in a duration alone, and the
// whole reason to break a tick down per reconciler is to tell them
// apart.
func TestRunCycleRecordsWhichReconcilerFailed(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	watched := mergedPullRequestTask(t, ctx, store, sim, client, "t1", repo, false)

	times := orchestrator.NewCycleTimes(8)
	deps := orchestrator.Deps{
		Store:      store,
		Client:     failingClient{Client: client, getPRFor: pullRequestNumber(t, watched)},
		Sandboxes:  orchestrator.NewHostSandboxes(t.TempDir()),
		Framework:  completesWithAComment(),
		MaxWorkers: 1,
		CycleTimes: times,
	}
	if err := orchestrator.RunCycle(ctx, deps, baseTime); err == nil {
		t.Fatal("RunCycle returned nil, want the injected sync failure")
	}

	cycles, _ := times.History()
	if len(cycles) != 1 {
		t.Fatalf("History() = %d cycle(s), want 1", len(cycles))
	}
	failed := map[string]bool{}
	for _, r := range cycles[0].Reconcilers {
		failed[r.Name] = r.Failed
	}
	if !failed["sync"] {
		t.Error("sync is not recorded as failed, but its GitHub read was the one that was made to fail")
	}
	if failed["dispatch"] {
		t.Error("dispatch is recorded as failed, but nothing failed it")
	}
}

// The shutdown tick is not a measurement. A cycle cut short by a
// cancelled context has a truncated duration that describes the daemon
// stopping rather than the daemon being slow, and there is at most one
// per process -- so it would be a single outlier permanently skewing a
// max.
func TestRunCycleRecordsNothingForACancelledCycle(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	filedTask(t, ctx, store, "t1", model.RepoRef{Owner: "acme", Name: "widgets"})

	cancelled, cancel := context.WithCancel(ctx)
	cancel()

	times := orchestrator.NewCycleTimes(8)
	deps := orchestrator.Deps{
		Store:      store,
		Client:     client,
		Sandboxes:  orchestrator.NewHostSandboxes(t.TempDir()),
		Framework:  completesWithAComment(),
		MaxWorkers: 1,
		CycleTimes: times,
	}
	_ = orchestrator.RunCycle(cancelled, deps, baseTime)

	if cycles, observed := times.History(); len(cycles) != 0 || observed != 0 {
		t.Fatalf("History() = %d cycle(s), observed %d; want a cancelled cycle to record nothing", len(cycles), observed)
	}
}

// A cycle with no CycleTimes measures nothing and still works -- the
// shape every caller in this repo that is not a daemon uses.
func TestRunCycleWithNoCycleTimesStillDispatches(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)

	deps := orchestrator.Deps{
		Store:      store,
		Client:     client,
		Sandboxes:  orchestrator.NewHostSandboxes(t.TempDir()),
		Framework:  completesWithAComment(),
		MaxWorkers: 1,
	}
	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if st := stateOf(t, ctx, store, task.ID); st != model.StateAwaitingReply {
		t.Fatalf("state = %q, want awaiting_reply", st)
	}
}

// The ring keeps the most recent cycles and forgets the rest, oldest
// first -- and Observed keeps counting past what it still holds, which
// is what tells a report it is looking at a tail rather than at the
// whole of a process's history.
func TestCycleTimesKeepsTheMostRecentCycles(t *testing.T) {
	t.Parallel()
	times := orchestrator.NewCycleTimes(3)
	for i := 0; i < 5; i++ {
		times.Observe(orchestrator.CycleTiming{
			Start:    baseTime.Add(time.Duration(i) * time.Minute),
			Duration: time.Duration(i+1) * time.Millisecond,
		})
	}
	cycles, observed := times.History()
	if observed != 5 {
		t.Errorf("observed = %d, want every cycle ever run (5)", observed)
	}
	if len(cycles) != 3 {
		t.Fatalf("History() = %d cycle(s), want the ring's own 3", len(cycles))
	}
	for i, c := range cycles {
		want := baseTime.Add(time.Duration(i+2) * time.Minute)
		if !c.Start.Equal(want) {
			t.Errorf("cycle %d starts %v, want %v -- the ring should hold the newest 3, oldest first", i, c.Start, want)
		}
	}
}

// The zero value works, so a caller needs no constructor, and a nil one
// drops what it is given rather than panicking -- which is what lets
// RunCycle's own measurement be optional.
func TestCycleTimesZeroValueAndNilAreUsable(t *testing.T) {
	t.Parallel()
	var zero orchestrator.CycleTimes
	zero.Observe(orchestrator.CycleTiming{Start: baseTime, Duration: time.Millisecond})
	if cycles, observed := zero.History(); len(cycles) != 1 || observed != 1 {
		t.Errorf("zero value: History() = %d cycle(s), observed %d; want 1 and 1", len(cycles), observed)
	}

	var nilTimes *orchestrator.CycleTimes
	nilTimes.Observe(orchestrator.CycleTiming{Start: baseTime})
	if cycles, observed := nilTimes.History(); cycles != nil || observed != 0 {
		t.Errorf("nil: History() = %v, %d; want nothing", cycles, observed)
	}
}
