package cli

// The live daemon tests' own pacing helpers, checked without a live key.
//
// They need their own coverage precisely because of what they are for:
// liveDaemonContext, awaitFinishedRun and pollUntil exist to keep
// daemon_live_test.go and daemon_true_e2e_test.go from failing on the
// clock, and both of those skip everywhere GEMINI_API_KEY is unset --
// including CI. A helper whose only exercise is a test that never runs
// is a helper nobody finds out is broken until the next person spends a
// live credential on it, which is the same "an assertion nothing ever
// runs" problem the fixed windows they replaced had.
//
// What is checked here is the arithmetic and the wording, which is all
// of these helpers that does not need an agent: that the daemon's own
// context really does outlive the wait for its run by the window grain's
// finishing needs, that a finished row ends the wait and an unfinished
// one does not, and that the message a spent budget produces names the
// clock rather than leaving a reader to suspect the daemon.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

// pacingStore is an on-disk store to write run rows into, opened the same
// way the daemon's own is.
func pacingStore(t *testing.T) (*model.Store, context.Context) {
	t.Helper()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	return store, ctx
}

// The daemon has to be alive for longer than the wait for its run, or the
// pull request ProcessResult opens after framework.Run returns would be
// cut off by the very context that was supposed to allow for it -- which
// is the failure the whole two-context arrangement exists to prevent.
func TestLiveDaemonContextKeepsTheFinishingWindowForTheDaemon(t *testing.T) {
	t.Parallel()
	const inside = 90 * time.Second
	if !enoughDeadlineFor(t, inside) {
		t.Skip("`go test -timeout` leaves too little for this test's own arithmetic")
	}

	ctx, runCtx, cancel := liveDaemonContext(t, inside, liveShutdownReserve)
	defer cancel()

	daemonDeadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("the daemon's context has no deadline; `go test -timeout` is what bounds a live run")
	}
	runDeadline, ok := runCtx.Deadline()
	if !ok {
		t.Fatal("the wait for the run has no deadline")
	}
	if gap := daemonDeadline.Sub(runDeadline); gap < inside-time.Second || gap > inside+time.Second {
		t.Errorf("the daemon outlives the wait for its run by %s, want %s -- that gap is what "+
			"grain's own finishing (ProcessResult, and the merge the granule test makes) runs in",
			gap.Round(time.Millisecond), inside)
	}
	if budget := time.Until(daemonDeadline); budget > liveRunBudget+time.Second {
		t.Errorf("the daemon is given %s, which is more than liveRunBudget (%s)",
			budget.Round(time.Second), liveRunBudget)
	}

	// Cancelling the pair cancels both: the wait's context is derived
	// from the daemon's, so a test that stops the daemon never leaves a
	// goroutine polling for a run that can no longer finish.
	cancel()
	if ctx.Err() == nil || runCtx.Err() == nil {
		t.Errorf("after cancel: daemon ctx err = %v, run ctx err = %v; want both cancelled",
			ctx.Err(), runCtx.Err())
	}
}

// The wait ends on the row RunDispatch stamps, and not before: a live run
// is exactly the state the fixed windows this replaced kept mistaking for
// a finished one.
func TestAwaitFinishedRunWaitsForTheFinishedRow(t *testing.T) {
	t.Parallel()
	store, ctx := pacingStore(t)
	if err := store.PutTask(ctx, model.Task{ID: "t1", Title: "live pacing"}); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: "r1", TaskID: "t1", Sandbox: "r1", Attempt: 1, StartedAt: time.Now().UTC(),
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}

	// Finished from another goroutine while the wait is already running,
	// which is the shape the daemon writes it in.
	finishedAt := time.Now().UTC()
	go func() {
		time.Sleep(50 * time.Millisecond)
		if err := store.FinishRun(context.Background(), "r1", finishedAt, "succeeded", "pushed a branch"); err != nil {
			t.Errorf("finishing the run: %v", err)
		}
	}()

	waitCtx, cancel := context.WithTimeout(ctx, daemonWaitLimit)
	defer cancel()
	run, err := awaitFinishedRun(waitCtx, store, "t1", neverStops())
	if err != nil {
		t.Fatalf("awaitFinishedRun: %v", err)
	}
	if run.FinishedAt == nil {
		t.Fatal("awaitFinishedRun returned a run with no FinishedAt")
	}
	if run.Outcome != "succeeded" || run.Detail != "pushed a branch" {
		t.Errorf("run = %q (%s), want the outcome and detail the finished row carries -- they are "+
			"what every failure message after the wait quotes", run.Outcome, run.Detail)
	}
}

// What a spent budget says. The message is the point of the wait as much
// as the wait is: a reader who cannot tell "the model was slow" from
// "grain never finished the run" is back where the fixed windows left
// them.
func TestAwaitFinishedRunBlamesTheClockAndQuotesTheRun(t *testing.T) {
	t.Parallel()
	store, ctx := pacingStore(t)
	if err := store.PutTask(ctx, model.Task{ID: "t1", Title: "live pacing"}); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: "r1", TaskID: "t1", Sandbox: "r1", Attempt: 1, StartedAt: time.Now().UTC(),
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetTaskActivity(ctx, "t1", "cloning acme/widgets", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	if _, err := awaitFinishedRun(waitCtx, store, "t1", neverStops()); err == nil {
		t.Fatal("awaitFinishedRun returned no error for a run that never finished")
	} else {
		for _, want := range []string{"t1", "go test -timeout", "attempt 1", "cloning acme/widgets"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the wait's own error does not mention %q: %v", want, err)
			}
		}
	}
}

// A daemon that returned early is not a slow agent, and must not be
// reported as one: run() can fail on its own -- a store it cannot open, a
// UI address it cannot bind -- and then nothing is writing the row this
// wait is watching for.
func TestAwaitFinishedRunEndsWhenTheDaemonStops(t *testing.T) {
	t.Parallel()
	store, ctx := pacingStore(t)
	if err := store.PutTask(ctx, model.Task{ID: "t1", Title: "live pacing"}); err != nil {
		t.Fatal(err)
	}

	stopped := make(chan struct{})
	go func() {
		time.Sleep(50 * time.Millisecond)
		close(stopped)
	}()

	// A budget far longer than this test is prepared to wait: the point
	// is that the daemon stopping, and not the clock, is what ends it.
	waitCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	started := time.Now()
	_, err := awaitFinishedRun(waitCtx, store, "t1", stopped)
	if err == nil || !strings.Contains(err.Error(), "run() returned") {
		t.Errorf("awaitFinishedRun after run() returned = %v, want an error naming the daemon", err)
	}
	if elapsed := time.Since(started); elapsed > 30*time.Second {
		t.Errorf("the wait took %s to notice the daemon had stopped", elapsed.Round(time.Second))
	}
}

// A task nobody dispatched reads differently from a run that is merely
// slow, since the two call for entirely different things of a reader.
func TestAwaitFinishedRunSaysWhenNothingWasDispatched(t *testing.T) {
	t.Parallel()
	store, ctx := pacingStore(t)
	if err := store.PutTask(ctx, model.Task{ID: "t1", Title: "live pacing"}); err != nil {
		t.Fatal(err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	_, err := awaitFinishedRun(waitCtx, store, "t1", neverStops())
	if err == nil || !strings.Contains(err.Error(), "not been dispatched") {
		t.Errorf("awaitFinishedRun for an undispatched task = %v, want an error saying so", err)
	}
}

func TestPollUntil(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// True on the first look costs no time at all: every window these
	// tests poll is grain's own, and the common case is that grain is
	// already done.
	started := time.Now()
	if !pollUntil(ctx, time.Minute, func() bool { return true }) {
		t.Error("pollUntil = false for a condition that was already true")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("pollUntil took %s to notice a condition that was already true", elapsed)
	}

	// A condition that never comes true costs the window and no more.
	started = time.Now()
	if pollUntil(ctx, 100*time.Millisecond, func() bool { return false }) {
		t.Error("pollUntil = true for a condition that never came true")
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Errorf("pollUntil overran its 100ms window by a long way: %s", elapsed)
	}

	// A cancelled context ends the wait, but not before one last look:
	// the daemon being cancelled and the thing being waited for having
	// just happened are not mutually exclusive.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if !pollUntil(cancelled, time.Minute, func() bool { return true }) {
		t.Error("pollUntil = false on a cancelled context for a condition that was true")
	}
}

// neverStops is the "the daemon is still up" signal, for the waits in
// here that are about something else.
func neverStops() <-chan struct{} { return make(chan struct{}) }

// enoughDeadlineFor reports whether this test binary's own `go test
// -timeout` leaves room for liveDaemonContext to hand out a context at
// all -- it fails the test rather than returning an error when it does
// not, which is right for a live test being asked for eight minutes and
// wrong for this one, which needs no time whatsoever.
func enoughDeadlineFor(t *testing.T, inside time.Duration) bool {
	t.Helper()
	deadline, ok := t.Deadline()
	if !ok {
		return true
	}
	return time.Until(deadline)-liveShutdownReserve >= inside+minLiveRunWait
}
