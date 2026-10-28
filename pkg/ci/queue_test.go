package ci

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// What this file is about (grain/task-326): a wait that is over has to
// leave something behind. JobRun.Activity is live state -- it is cleared
// the moment the job is admitted, and cleared again on a job that never
// got in at all -- so a job that queued for eighteen minutes and ran for
// four used to be indistinguishable afterwards from one that ran for
// twenty-two, and three jobs cancelled at exactly their timeout left
// nobody able to say whether they had been running or waiting.

// newTestClock is live_test.go's hand-moved clock, started somewhere
// recognisable: an engine's queueing is measured in minutes and no test
// is going to wait them out.
func newTestClock() *testClock {
	return &testClock{now: time.Date(2026, 9, 18, 14, 0, 0, 0, time.UTC)}
}

// scriptedGate is a Gate that holds a job for a written-down sequence of
// waits, moving a test's clock as it goes, and then admits it. It is the
// deployment's gate as a job experiences it: a sentence, then a while,
// then a different sentence.
type scriptedGate struct {
	clock *testClock
	held  []struct {
		wait  Wait
		lasts time.Duration
	}
	// refuse, when set, is what this gate does instead of admitting: the
	// job that queues and is then given up on, which is the one the
	// record matters most for.
	refuse func() error
}

func (g *scriptedGate) Acquire(_ context.Context, req GateRequest) (func(), error) {
	for _, step := range g.held {
		if req.Wait != nil {
			req.Wait(step.wait)
		}
		g.clock.advance(step.lasts)
	}
	if g.refuse != nil {
		return nil, g.refuse()
	}
	if req.Wait != nil {
		req.Wait(Wait{})
	}
	return func() {}, nil
}

func onePlan(t *testing.T) Plan {
	t.Helper()
	return planFor(t, Request{Repo: "o/r", Ref: "main", Commit: "abc", Event: EventPush}, map[string]string{
		".grain/workflows/a.yml": "on: push\njobs:\n  slow:\n    steps:\n      - run: true\n",
	})
}

// The whole wait is recorded, and it is attributed to the kind of thing
// that held it longest rather than to the last sentence said about it.
//
// The sentence is not the unit: a gate's reason says how the deployment
// stands right now, so a job held by the budget for a quarter of an hour
// is told about it in as many different words as the budget changes in,
// and bucketing those separately would report several short waits where
// there was one long one.
func TestJobRecordsItsWholeWaitAndWhatHeldItLongest(t *testing.T) {
	clock := newTestClock()
	budgetFirst := Wait{Cause: WaitForRoom, Reason: "for room in this deployment's sandbox budget -- 8 GiB free"}
	budgetLater := Wait{Cause: WaitForRoom, Reason: "for room in this deployment's sandbox budget -- 4 GiB free"}
	slot := Wait{Cause: WaitForSlot, Reason: "for one of the 3 sandboxes this deployment keeps for CI; 3 jobs hold one right now"}
	gate := &scriptedGate{clock: clock, held: []struct {
		wait  Wait
		lasts time.Duration
	}{
		{budgetFirst, 10 * time.Minute},
		{budgetLater, 5 * time.Minute},
		{slot, 12 * time.Minute},
	}}

	run := (&Engine{Sandboxes: &fakeSandboxes{}, Jobs: gate, Now: clock.Now}).Run(
		context.Background(), "r1", onePlan(t), nil)

	job := jobByID(t, run, "slow")
	if job.Queued != 27*time.Minute {
		t.Errorf("queued = %s, want the whole 27m between being startable and getting a machine", job.Queued)
	}
	// 15 minutes of budget against 12 of the count, in three sentences.
	if job.QueuedReason != budgetLater.Reason {
		t.Errorf("queued reason = %q, want the budget's last word on it (%q): it held this job longest",
			job.QueuedReason, budgetLater.Reason)
	}
	if job.Duration(clock.Now()) != 0 {
		// The steps run against a clock that is not moving, so the work
		// itself takes no time here. What matters is that the wait is
		// not in it: a job's duration begins when it gets a machine.
		t.Errorf("duration = %s, want none of the wait in it", job.Duration(clock.Now()))
	}
	if job.Activity != "" || job.ActivityAt != nil {
		t.Errorf("activity = %q at %v, want it cleared: this job is not waiting any more", job.Activity, job.ActivityAt)
	}
}

// The job the measurement exists for: it queued, it was cancelled before
// it ever got a machine, and the record has to be able to say which of
// those two things took the thirty-six minutes. It has no duration at
// all -- StartedAt is nil for a job that never started -- so without the
// wait written down there is nothing in the row at all.
func TestJobCancelledWhileQueueingKeepsItsWait(t *testing.T) {
	clock := newTestClock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	held := Wait{
		Cause:  WaitForHole,
		Reason: "for a hole big enough for it (4 vCPU, 16.0 GiB): this deployment is holding capacity back for this job",
	}
	gate := &scriptedGate{clock: clock, held: []struct {
		wait  Wait
		lasts time.Duration
	}{{held, 36 * time.Minute}}}
	gate.refuse = func() error {
		cancel()
		return ctx.Err()
	}

	run := (&Engine{Sandboxes: &fakeSandboxes{}, Jobs: gate, Now: clock.Now}).Run(ctx, "r1", onePlan(t), nil)

	job := jobByID(t, run, "slow")
	if job.StartedAt != nil {
		t.Fatalf("startedAt = %v, want none: this job never got a machine", *job.StartedAt)
	}
	if job.Conclusion != ConclusionCancelled {
		t.Errorf("conclusion = %q, want cancelled", job.Conclusion)
	}
	if job.Queued != 36*time.Minute {
		t.Errorf("queued = %s, want the 36m it spent waiting: it is the whole of what happened to this job", job.Queued)
	}
	if !strings.Contains(job.QueuedReason, "hole big enough") {
		t.Errorf("queued reason = %q, want what was holding it -- a job cancelled at its timeout is either starved "+
			"or slow, and only this says which", job.QueuedReason)
	}
}

// A cached job waited for nothing, whatever the run behind the entry
// waited for. Its timestamps are that earlier run's on purpose -- they
// say what the work costs -- but a wait is what this deployment did to
// this job, and carrying an old one forward would put a queue on a
// deployment that had none.
func TestCachedJobCarriesNoQueueingFromTheRunItWasRememberedFrom(t *testing.T) {
	cache := newCannedCache()
	plan := onePlan(t)
	cache.entries[plan.Jobs[0].Key] = JobRun{
		Conclusion:   ConclusionSuccess,
		Queued:       18 * time.Minute,
		QueuedReason: "for room in this deployment's sandbox budget -- 8 GiB free",
	}

	run := (&Engine{Sandboxes: &fakeSandboxes{}, Cache: cache}).Run(context.Background(), "r1", plan, nil)

	job := jobByID(t, run, "slow")
	if !job.Cached {
		t.Fatal("this job was meant to come from the cache")
	}
	if job.Queued != 0 || job.QueuedReason != "" {
		t.Errorf("a cache hit reports %s of queueing (%q), want none: it waited for nothing",
			job.Queued, job.QueuedReason)
	}
}

// A job held by its own run's concurrency is held by its siblings rather
// than by the deployment, and says so. It is where most of a quiet
// deployment's queueing happens -- a suite of eight jobs on a deployment
// that runs three at a time holds five of them there -- and a wait that
// nothing reports is a wait that goes down as time spent on nothing.
func TestJobHeldByItsOwnRunsWidthSaysSoRatherThanBlamingTheDeployment(t *testing.T) {
	clock := newTestClock()
	sandboxes := &fakeSandboxes{block: make(chan struct{})}
	plan := planFor(t, Request{Repo: "o/r", Ref: "main", Event: EventPush}, map[string]string{
		".grain/workflows/a.yml": "on: push\njobs:\n  first:\n    steps:\n      - run: true\n" +
			"  second:\n    steps:\n      - run: true\n",
	})
	// Wait for the wait this test is about, rather than asserting on the
	// first thing published. A job can be held by the gate's *ordering*
	// before it is held by its width -- both jobs reach the gate, the
	// second is ranked behind the first, and the slot the first is about
	// to take is not taken yet (SharedGate.take's priority branch) -- and
	// which of the two a reader sees first is a race the deployment's load
	// decides. Once the first job holds the slot every further attempt is
	// held by the count, so the message below is the one this job settles
	// on; whatever else it said goes into the failure, which is what says
	// so when it does not.
	//
	// The filtering happens in the observer, and drops what it is not
	// waiting for rather than queueing it: an engine whose observations
	// stop being read stops running, which is a deadlock wearing whatever
	// deadline the test had on it (grain/task-426, grain/task-431).
	width := make(chan JobRun, 4)
	var mu sync.Mutex
	var otherwise []string
	done := make(chan Run, 1)
	go func() {
		done <- (&Engine{Sandboxes: sandboxes, Concurrency: 1, Now: clock.Now}).Run(
			context.Background(), "r1", plan, func(job JobRun) {
				switch {
				case strings.Contains(job.Activity, "this run may hold"):
					signal(width, job)
				case job.Activity != "":
					mu.Lock()
					otherwise = append(otherwise, job.Activity)
					mu.Unlock()
				}
			})
	}()

	select {
	case <-width:
	case <-time.After(signalWaitLimit):
		mu.Lock()
		said := otherwise
		mu.Unlock()
		t.Fatalf("the second job never said it was waiting for its own run's capacity; "+
			"what it did say was %q", said)
	}
	close(sandboxes.block)
	<-done
}

// The suite's total, which no single job's wait says: a deployment short
// of sandboxes is one whose runs spend more job-time waiting than
// working, and that is an addition across the run rather than a fact
// about any job in it.
func TestRunAddsUpItsJobsQueueing(t *testing.T) {
	run := Run{Jobs: []JobRun{
		{JobID: "one", Queued: 18 * time.Minute},
		{JobID: "two", Queued: 4 * time.Minute},
		{JobID: "cached", Cached: true},
		{JobID: "straight in"},
	}}
	if got := run.Queued(); got != 22*time.Minute {
		t.Errorf("run queued = %s, want 22m: the jobs' own waits added together", got)
	}
}

// The clock underneath all of that, on its own: time nobody attributed
// to anything is still part of the wait, but it is never what the wait
// is blamed on -- a duration with no reason is an honest answer and an
// invented reason is not.
func TestQueueClockCountsUnattributedTimeWithoutBlamingItOnAnything(t *testing.T) {
	start := time.Date(2026, 9, 18, 14, 0, 0, 0, time.UTC)
	q := newQueueClock(start)
	// Ten minutes nothing reported, then two of the count.
	q.waiting(start.Add(10*time.Minute), Wait{Cause: WaitForSlot, Reason: "for one of the 3 sandboxes"})
	queued, cause, reason := q.stop(start.Add(12 * time.Minute))

	if queued != 12*time.Minute {
		t.Errorf("queued = %s, want the whole 12m including the time nobody accounted for", queued)
	}
	if cause != WaitForSlot {
		t.Errorf("cause = %q, want the only cause that ever held it", cause)
	}
	if reason != "for one of the 3 sandboxes" {
		t.Errorf("reason = %q, want the only thing that ever reported holding it", reason)
	}

	silent := newQueueClock(start)
	queued, cause, reason = silent.stop(start.Add(3 * time.Minute))
	if queued != 3*time.Minute || cause != "" || reason != "" {
		t.Errorf("a wait nothing reported = %s %q %q, want the duration and no cause or reason at all", queued, cause, reason)
	}
}
