package metrics_test

// Compute, against timelines built by hand. Every case here is one the
// store can really produce -- a task approved in the instant it was
// filed, a run that failed before its agent started, an attempt still in
// flight when the report was taken -- since the point of the package is
// that it reports what happened rather than what a well-formed record
// would have looked like.

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/metrics"
	"github.com/ai-on-gke/grain/pkg/model"
)

var (
	since = time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	until = since.Add(7 * 24 * time.Hour)
	win   = metrics.Window{Since: since, Until: until}
)

// at is a moment d into the window, as the pointer every nilable timing
// field holds.
func at(d time.Duration) *time.Time {
	t := since.Add(d)
	return &t
}

func TestThroughputCountsWhatCrossedEachLineInTheWindow(t *testing.T) {
	rep := metrics.Compute(metrics.Input{
		Window: win,
		Tasks: []model.TaskTiming{
			{TaskID: "1", CreatedAt: at(time.Hour), ApprovedAt: at(2 * time.Hour), CompletedAt: at(30 * time.Hour)},
			{TaskID: "2", CreatedAt: at(26 * time.Hour), ApprovedAt: at(26 * time.Hour)},
			// Filed before the window and completed inside it: its
			// completion counts, its filing does not, and its lead time
			// still reaches back past Since.
			{TaskID: "3", CreatedAt: at(-48 * time.Hour), ApprovedAt: at(-48 * time.Hour),
				CompletedAt: at(50 * time.Hour), ClosedAt: at(51 * time.Hour)},
			// Completed after the window ends: in the store, out of the
			// report.
			{TaskID: "4", CreatedAt: at(time.Hour), CompletedAt: at(8 * 24 * time.Hour)},
		},
	})

	if got, want := rep.Throughput.TasksFiled, 3; got != want {
		t.Errorf("TasksFiled = %d, want %d", got, want)
	}
	if got, want := rep.Throughput.TasksCompleted, 2; got != want {
		t.Errorf("TasksCompleted = %d, want %d", got, want)
	}
	if got, want := rep.Throughput.TasksClosed, 1; got != want {
		t.Errorf("TasksClosed = %d, want %d", got, want)
	}
	// Two completions over seven days.
	if got, want := rep.Throughput.CompletedPerDay, 2.0/7.0; !closeEnough(got, want) {
		t.Errorf("CompletedPerDay = %v, want %v", got, want)
	}

	// The buckets are the same counts, sliced: nothing may be lost or
	// double-counted by the slicing.
	var filed, completed int
	for _, b := range rep.Throughput.Buckets {
		filed += b.Filed
		completed += b.Completed
	}
	if filed != rep.Throughput.TasksFiled || completed != rep.Throughput.TasksCompleted {
		t.Errorf("buckets summed to filed=%d completed=%d, want %d and %d",
			filed, completed, rep.Throughput.TasksFiled, rep.Throughput.TasksCompleted)
	}
	if len(rep.Throughput.Buckets) != metrics.DefaultBuckets {
		t.Errorf("got %d buckets, want the default %d", len(rep.Throughput.Buckets), metrics.DefaultBuckets)
	}
	if last := rep.Throughput.Buckets[len(rep.Throughput.Buckets)-1]; !last.Until.Equal(until) {
		t.Errorf("last bucket ends at %v, want the window's own end %v", last.Until, until)
	}
}

// The stage split is the measurement this package exists for: a run's own
// duration says nothing about whether the time went on booting a sandbox
// or on the agent.
func TestLatencySplitsSetupFromAgentWork(t *testing.T) {
	rep := metrics.Compute(metrics.Input{
		Window: win,
		Tasks:  []model.TaskTiming{{TaskID: "1", CreatedAt: at(time.Hour), ApprovedAt: at(time.Hour)}},
		Runs: []model.RunTiming{{
			TaskID: "1", Attempt: 1,
			StartedAt:      *at(2 * time.Hour),
			AgentStartedAt: at(2*time.Hour + 5*time.Minute),
			FinishedAt:     at(2*time.Hour + 35*time.Minute),
			Outcome:        "succeeded",
		}},
	})

	if got, want := rep.Latency.SandboxSetup.P50, 5*time.Minute; got != want {
		t.Errorf("SandboxSetup P50 = %v, want %v", got, want)
	}
	if got, want := rep.Latency.AgentWork.P50, 30*time.Minute; got != want {
		t.Errorf("AgentWork P50 = %v, want %v", got, want)
	}
	if got, want := rep.Latency.Attempt.P50, 35*time.Minute; got != want {
		t.Errorf("Attempt P50 = %v, want %v", got, want)
	}
	// Approved in the same instant it was filed: nobody waited for
	// approval, and a zero sample would say they did.
	if got := rep.Latency.ApprovalWait.N; got != 0 {
		t.Errorf("ApprovalWait N = %d, want 0 for a task approved as it was filed", got)
	}
	if got, want := rep.Latency.QueueWait.P50, time.Hour; got != want {
		t.Errorf("QueueWait P50 = %v, want %v", got, want)
	}
}

// A run that never reached its agent -- outcome "setup-failed", or a
// checkout that would not clone -- has no setup or agent latency to
// report. It must contribute to neither rather than to a wrong one, while
// still counting as the attempt it was.
func TestRunThatNeverReachedItsAgentIsSkippedNotGuessed(t *testing.T) {
	rep := metrics.Compute(metrics.Input{
		Window: win,
		Tasks:  []model.TaskTiming{{TaskID: "1", CreatedAt: at(0), ApprovedAt: at(0)}},
		Runs: []model.RunTiming{{
			TaskID: "1", Attempt: 1,
			StartedAt:  *at(time.Hour),
			FinishedAt: at(time.Hour + 4*time.Minute),
			Outcome:    "setup-failed",
		}},
	})

	if got := rep.Latency.SandboxSetup.N; got != 0 {
		t.Errorf("SandboxSetup N = %d, want 0", got)
	}
	if got := rep.Latency.AgentWork.N; got != 0 {
		t.Errorf("AgentWork N = %d, want 0", got)
	}
	if got, want := rep.Latency.Attempt.N, 1; got != want {
		t.Errorf("Attempt N = %d, want %d -- the attempt still took time", got, want)
	}
	if got, want := rep.Runs.Outcomes["setup-failed"], 1; got != want {
		t.Errorf("Outcomes[setup-failed] = %d, want %d", got, want)
	}
}

func TestRetryWaitAndAttemptsPerCompletion(t *testing.T) {
	rep := metrics.Compute(metrics.Input{
		Window: win,
		Tasks: []model.TaskTiming{
			{TaskID: "1", CreatedAt: at(0), ApprovedAt: at(0), CompletedAt: at(5 * time.Hour)},
		},
		Runs: []model.RunTiming{
			{TaskID: "1", Attempt: 1, StartedAt: *at(time.Hour), FinishedAt: at(2 * time.Hour), Outcome: "failed"},
			{TaskID: "1", Attempt: 2, StartedAt: *at(2*time.Hour + 20*time.Minute),
				FinishedAt: at(5 * time.Hour), Outcome: "succeeded"},
		},
	})

	if got, want := rep.Latency.RetryWait.N, 1; got != want {
		t.Fatalf("RetryWait N = %d, want %d", got, want)
	}
	if got, want := rep.Latency.RetryWait.P50, 20*time.Minute; got != want {
		t.Errorf("RetryWait P50 = %v, want %v", got, want)
	}
	// QueueWait is the first attempt only; the second one's wait is a
	// retry, which is a different question with a different answer.
	if got, want := rep.Latency.QueueWait.N, 1; got != want {
		t.Errorf("QueueWait N = %d, want %d", got, want)
	}
	if got, want := rep.Latency.TimeToFinish.P50, 4*time.Hour; got != want {
		t.Errorf("TimeToFinish P50 = %v, want %v", got, want)
	}
	if got, want := rep.Latency.LeadTime.P50, 5*time.Hour; got != want {
		t.Errorf("LeadTime P50 = %v, want %v", got, want)
	}
	if got, want := rep.Runs.AttemptsPerCompletion, 2.0; !closeEnough(got, want) {
		t.Errorf("AttemptsPerCompletion = %v, want %v", got, want)
	}
}

// Occupancy is what makes "the queue is deep and the machine is idle"
// visible, so it has to count a run that started before the window and one
// that has not finished yet -- both of which really were holding capacity.
func TestMeanConcurrentCountsOverlapIncludingLiveRuns(t *testing.T) {
	short := metrics.Window{Since: since, Until: since.Add(10 * time.Hour)}
	rep := metrics.Compute(metrics.Input{
		Window:        short,
		MaxConcurrent: 2,
		Runs: []model.RunTiming{
			// Started before the window, finished 2h into it: 2h counted.
			{TaskID: "1", Attempt: 1, StartedAt: *at(-3 * time.Hour), FinishedAt: at(2 * time.Hour)},
			// Wholly inside: 3h.
			{TaskID: "2", Attempt: 1, StartedAt: *at(4 * time.Hour), FinishedAt: at(7 * time.Hour)},
			// Still live at the window's end: 5h, up to Until.
			{TaskID: "3", Attempt: 1, StartedAt: *at(5 * time.Hour)},
		},
	})

	// 10 run-hours over a 10-hour window.
	if got, want := rep.Runs.MeanConcurrent, 1.0; !closeEnough(got, want) {
		t.Errorf("MeanConcurrent = %v, want %v", got, want)
	}
	if got, want := rep.Runs.Utilization, 0.5; !closeEnough(got, want) {
		t.Errorf("Utilization = %v, want %v", got, want)
	}
	if got, want := rep.Runs.Live, 1; got != want {
		t.Errorf("Live = %d, want %d", got, want)
	}
}

// Live is a gauge as of Until, which for a window ending in the past is
// not the same as "still unfinished now" -- a saved report board can carry
// an absolute span months back (ui.MetricsRequest), and every run of that
// week has long since finished.
func TestLiveCountsRunsThatOutlivedTheWindowRatherThanUnfinishedOnes(t *testing.T) {
	short := metrics.Window{Since: since, Until: since.Add(10 * time.Hour)}
	rep := metrics.Compute(metrics.Input{
		Window: short,
		Runs: []model.RunTiming{
			// Running when the window ended, finished long afterwards.
			{TaskID: "1", Attempt: 1, StartedAt: *at(5 * time.Hour), FinishedAt: at(30 * time.Hour)},
			// Over before the window ended.
			{TaskID: "2", Attempt: 1, StartedAt: *at(time.Hour), FinishedAt: at(2 * time.Hour)},
			// Not started until after it.
			{TaskID: "3", Attempt: 1, StartedAt: *at(20 * time.Hour)},
		},
	})
	if got, want := rep.Runs.Live, 1; got != want {
		t.Errorf("Live = %d, want %d -- the one run that was running at Until", got, want)
	}
}

func TestBacklogReportsQueueDepthAndTheOldestWait(t *testing.T) {
	tasks := []model.TaskTiming{
		{TaskID: "1", CreatedAt: at(0), ApprovedAt: at(0)},
		{TaskID: "2", CreatedAt: at(time.Hour), ApprovedAt: at(2 * time.Hour)},
		{TaskID: "3", CreatedAt: at(time.Hour)},
		// Put aside, and so not part of the backlog at all: it is asking
		// for no capacity, and counting it would make a deployment that
		// parks ideas look backed up (Backlog.ByState).
		{TaskID: "4", CreatedAt: at(time.Hour), ApprovedAt: at(time.Hour)},
	}
	states := map[string]model.State{
		"1": model.StateQueued,
		"2": model.StateQueued,
		"3": model.StateProposed,
		"4": model.StateDeferred,
	}
	rep := metrics.Compute(metrics.Input{Window: win, Tasks: tasks, States: states})

	if got, want := rep.Backlog.Queued, 2; got != want {
		t.Errorf("Queued = %d, want %d", got, want)
	}
	if got, want := rep.Backlog.ByState[model.StateProposed], 1; got != want {
		t.Errorf("ByState[proposed] = %d, want %d", got, want)
	}
	if got, want := rep.Backlog.ByState[model.StateDeferred], 0; got != want {
		t.Errorf("ByState[deferred] = %d, want %d -- a deferred task is not backlog", got, want)
	}
	if got, want := rep.Backlog.OldestQueuedTaskID, "1"; got != want {
		t.Errorf("OldestQueuedTaskID = %q, want %q", got, want)
	}
	if got, want := rep.Backlog.OldestQueuedWait, until.Sub(since); got != want {
		t.Errorf("OldestQueuedWait = %v, want %v", got, want)
	}

	// Without states there is no honest way to tell a queued task from
	// one awaiting a reply, so the section stays empty rather than
	// guessing.
	bare := metrics.Compute(metrics.Input{Window: win, Tasks: tasks})
	if bare.Backlog.ByState != nil || bare.Backlog.Queued != 0 {
		t.Errorf("backlog without states = %+v, want the zero value", bare.Backlog)
	}
}

// The oldest wait is measured from the moment a task came back, for one
// that was put aside and picked back up (model.Task.UndeferredAt).
//
// Measured from its approval instead, a task parked for months and
// released this morning is the oldest thing in the queue from the instant
// it returns, and stays so -- so the deployment's headline backlog number
// names the one task that has waited no time at all, and the row a stall
// flag lands on and the task named here disagree
// (model.StateTimes.Since measures the same moment).
func TestTheOldestWaitIsMeasuredFromAnUndeferralRatherThanTheApproval(t *testing.T) {
	tasks := []model.TaskTiming{
		// Approved at the window's start and waiting ever since.
		{TaskID: "waiting", CreatedAt: at(0), ApprovedAt: at(0)},
		// Approved before that, deferred, and back an hour before the
		// window ended.
		{TaskID: "returned", CreatedAt: at(-time.Hour), ApprovedAt: at(-time.Hour),
			UndeferredAt: at(until.Sub(since) - time.Hour)},
	}
	states := map[string]model.State{"waiting": model.StateQueued, "returned": model.StateQueued}
	rep := metrics.Compute(metrics.Input{Window: win, Tasks: tasks, States: states})

	if got, want := rep.Backlog.OldestQueuedTaskID, "waiting"; got != want {
		t.Errorf("OldestQueuedTaskID = %q, want %q -- a task picked back up an hour ago has not been "+
			"waiting since the approval it was deferred with", got, want)
	}
	if got, want := rep.Backlog.OldestQueuedWait, until.Sub(since); got != want {
		t.Errorf("OldestQueuedWait = %v, want %v", got, want)
	}

	// And the returned task is still counted as backlog -- it is queued,
	// it is asking for capacity, and only the clock on it moved.
	if got, want := rep.Backlog.Queued, 2; got != want {
		t.Errorf("Queued = %d, want %d", got, want)
	}
}

// Percentiles are nearest-rank over the samples themselves: every number
// a report shows is a duration something really took.
func TestPercentilesAreNearestRankOverRealSamples(t *testing.T) {
	var runs []model.RunTiming
	for i := 1; i <= 10; i++ {
		start := *at(time.Duration(i) * time.Hour)
		finish := start.Add(time.Duration(i) * time.Minute)
		runs = append(runs, model.RunTiming{
			TaskID: "1", Attempt: i, StartedAt: start, FinishedAt: &finish, Outcome: "succeeded",
		})
	}
	d := metrics.Compute(metrics.Input{Window: win, Runs: runs}).Latency.Attempt

	if d.N != 10 {
		t.Fatalf("N = %d, want 10", d.N)
	}
	for _, c := range []struct {
		name string
		got  time.Duration
		want time.Duration
	}{
		{"Min", d.Min, time.Minute},
		{"P50", d.P50, 5 * time.Minute},
		{"P90", d.P90, 9 * time.Minute},
		{"P99", d.P99, 10 * time.Minute},
		{"Max", d.Max, 10 * time.Minute},
		{"Mean", d.Mean, 5*time.Minute + 30*time.Second},
	} {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

// An empty store is a real state -- a deployment on its first day -- and
// has to produce a report rather than a division by zero.
func TestEmptyInputReportsZeroes(t *testing.T) {
	rep := metrics.Compute(metrics.Input{Window: win})
	if rep.Throughput.TasksCompleted != 0 || rep.Throughput.CompletedPerDay != 0 {
		t.Errorf("throughput over an empty store = %+v, want zeroes", rep.Throughput)
	}
	if rep.Latency.LeadTime.N != 0 || rep.Latency.LeadTime.P50 != 0 {
		t.Errorf("lead time over an empty store = %+v, want zeroes", rep.Latency.LeadTime)
	}
	if rep.Runs.MeanConcurrent != 0 || rep.Runs.Utilization != 0 {
		t.Errorf("capacity over an empty store = %+v, want zeroes", rep.Runs)
	}

	// A window that is empty or inverted divides by nothing.
	inverted := metrics.Compute(metrics.Input{Window: metrics.Window{Since: until, Until: since}})
	if inverted.Throughput.CompletedPerDay != 0 || len(inverted.Throughput.Buckets) != 0 {
		t.Errorf("inverted window = %+v, want no rate and no buckets", inverted.Throughput)
	}
}

func closeEnough(got, want float64) bool {
	d := got - want
	return d < 1e-9 && d > -1e-9
}
