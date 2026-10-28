// What InFlight itself promises, independent of a cycle: that a run is
// live from before its goroutine starts until its work returns, that
// Wait blocks for exactly that long, and that a Wait which runs out of
// context says so rather than returning as if the drain succeeded.
package orchestrator_test

import (
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// runWaitLimit is the ceiling this package's tests wait under for
// something a run does -- a dispatch returning, a drain finishing, a
// cancellation arriving. It is a hang-guard and not a measurement: what
// is waited for is a signal the code raises as it goes, so it either
// arrives as fast as the machine can carry it or never arrives at all,
// and a five-second bound on a goroutine handoff is a statement about how
// busy the host is rather than about the code (grain/task-426,
// grain/task-431). The 30-second bounds already in this package
// (concurrent_dispatch_test.go, live_settings_test.go) are the same
// number for the same reason.
const runWaitLimit = 30 * time.Second

func TestInFlightWaitReturnsImmediatelyWithNothingLive(t *testing.T) {
	t.Parallel()
	var runs orchestrator.InFlight
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runs.Wait(ctx); err != nil {
		t.Fatalf("Wait on an empty set: %v", err)
	}
}

func TestInFlightTracksARunUntilItReturns(t *testing.T) {
	t.Parallel()
	var runs orchestrator.InFlight
	started := make(chan struct{})
	release := make(chan struct{})

	runs.Go("t1-1", "t1", func() {
		close(started)
		<-release
	})
	<-started

	if got := runs.Len(); got != 1 {
		t.Fatalf("Len while the run is live = %d, want 1", got)
	}
	if got := runs.Runs(); len(got) != 1 || got[0] != "t1-1" {
		t.Fatalf("Runs while the run is live = %v, want [t1-1]", got)
	}

	waited := make(chan error, 1)
	go func() { waited <- runs.Wait(context.Background()) }()
	// A moment for a Wait that was going to return wrongly to do it. This
	// one stays a wall-clock wait on purpose: what it is waiting for is
	// the test's own goroutine reaching Wait at all, and there is no
	// signal for "this goroutine is now blocked inside a function", so
	// asking immediately would pass whether Wait were right or wrong
	// (grain/task-431). It cannot fail under load -- a slow machine only
	// makes the wrong wake less likely, never the right behaviour red.
	select {
	case err := <-waited:
		t.Fatalf("Wait returned (%v) while a run was still live", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	select {
	case err := <-waited:
		if err != nil {
			t.Fatalf("Wait: %v", err)
		}
	case <-time.After(runWaitLimit):
		t.Fatal("Wait never returned after the run finished")
	}
	if got := runs.Len(); got != 0 {
		t.Errorf("Len after the run finished = %d, want 0", got)
	}
}

// Busy is what keeps a cycle off a task whose run this process has not
// finished with -- true for as long as the work is, and only for that
// task.
func TestInFlightBusyAnswersForTheTaskARunBelongsTo(t *testing.T) {
	t.Parallel()
	var runs orchestrator.InFlight
	started := make(chan struct{})
	release := make(chan struct{})
	runs.Go("t1-1", "t1", func() {
		close(started)
		<-release
	})
	<-started

	if !runs.Busy("t1") {
		t.Error("Busy(t1) = false while t1's own run is live")
	}
	if runs.Busy("t2") {
		t.Error("Busy(t2) = true with nothing of t2's running")
	}

	close(release)
	if err := runs.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if runs.Busy("t1") {
		t.Error("Busy(t1) = true after its run returned")
	}
}

func TestInFlightWaitReportsARunThatOutlastsItsContext(t *testing.T) {
	t.Parallel()
	var runs orchestrator.InFlight
	release := make(chan struct{})
	defer close(release)
	runs.Go("t1-1", "t1", func() { <-release })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := runs.Wait(ctx); err == nil {
		t.Fatal("Wait returned nil for a run that never finished, want the context's error")
	}
}

// A Wait covers runs started while it is waiting too -- "nothing is
// live" is the condition, not "the runs I could see when I asked".
func TestInFlightWaitCoversRunsStartedWhileWaiting(t *testing.T) {
	t.Parallel()
	var runs orchestrator.InFlight
	first := make(chan struct{})
	second := make(chan struct{})
	runs.Go("t1-1", "t1", func() { <-first })

	waited := make(chan error, 1)
	go func() { waited <- runs.Wait(context.Background()) }()

	runs.Go("t2-1", "t2", func() { <-second })
	close(first)
	select {
	case err := <-waited:
		t.Fatalf("Wait returned (%v) with the second run still live", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(second)
	select {
	case err := <-waited:
		if err != nil {
			t.Fatalf("Wait: %v", err)
		}
	case <-time.After(runWaitLimit):
		t.Fatal("Wait never returned after both runs finished")
	}
}
