package metrics_test

// CIQueueing: how much of a deployment's CI job-time went on waiting,
// what held it and who wore it. The cases here are the ones the store can
// really produce -- a job that never got a machine at all, a job answered
// from the cache, a job recorded before anything stored a cause -- since
// the point of the section is that it adds up what happened rather than
// what a tidy record would have looked like.

import (
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/metrics"
	"github.com/bwsalmon/grain/pkg/model"
)

// job is one finished CI job: d of waiting, then ran of working, ending
// at `end` into the window.
func job(repo, name string, end, queued, ran time.Duration, cause, reason string) model.CIJobTiming {
	j := model.CIJobTiming{
		RunID: repo + name, Repo: repo, Ref: "main", Name: name,
		FinishedAt: at(end), Queued: queued, QueuedCause: cause, QueuedReason: reason,
	}
	if ran > 0 {
		j.StartedAt = at(end - ran)
	}
	return j
}

func TestCIQueueingIsTheShareOfJobTimeSpentWaiting(t *testing.T) {
	rep := metrics.Compute(metrics.Input{
		Window: win,
		CIJobs: []model.CIJobTiming{
			job("acme/widgets", "go", 3*time.Hour, 18*time.Minute, 6*time.Minute, "slot", "for one of the 3 sandboxes"),
			job("acme/widgets", "lint", 4*time.Hour, 0, 6*time.Minute, "", ""),
		},
	})
	q := rep.CIQueueing
	if q.Jobs != 2 || q.Waited != 1 {
		t.Errorf("jobs = %d, waited = %d; want 2 jobs of which 1 waited", q.Jobs, q.Waited)
	}
	if q.Queued != 18*time.Minute || q.Busy != 12*time.Minute {
		t.Errorf("queued/busy = %s/%s, want 18m/12m", q.Queued, q.Busy)
	}
	if got, want := q.QueuedShare, 0.6; got != want {
		t.Errorf("QueuedShare = %v, want %v -- 18m of the 30m of job-time", got, want)
	}
	// The distribution is over the jobs that waited, so the job that went
	// straight in does not drag the median to nine minutes.
	if got := q.Wait; got.N != 1 || got.P50 != 18*time.Minute {
		t.Errorf("Wait = %+v, want one sample of 18m", got)
	}
}

// A job that never got a machine is the case the whole measurement was
// built for: its row has no duration of its own, and its wait is all
// there is to say about it.
func TestCIQueueingCountsAJobThatNeverGotAMachine(t *testing.T) {
	rep := metrics.Compute(metrics.Input{
		Window: win,
		CIJobs: []model.CIJobTiming{
			job("acme/widgets", "image", 2*time.Hour, 36*time.Minute, 0, "hole", "capacity is being held for a larger job"),
		},
	})
	q := rep.CIQueueing
	if q.Jobs != 1 || q.NeverRan != 1 {
		t.Errorf("jobs = %d, neverRan = %d; want one job that never ran", q.Jobs, q.NeverRan)
	}
	if q.Busy != 0 || q.Queued != 36*time.Minute {
		t.Errorf("queued/busy = %s/%s, want the whole 36m as waiting and no work at all", q.Queued, q.Busy)
	}
	if q.QueuedShare != 1 {
		t.Errorf("QueuedShare = %v, want 1 -- every minute of it was waiting", q.QueuedShare)
	}
}

// A cache hit took no machine and waited for nothing, and its timestamps
// are an earlier run's. Counting it as a job that queued for no time
// would let a deployment improve its queueing share by caching more.
func TestCIQueueingLeavesCacheHitsOutOfTheShare(t *testing.T) {
	cached := job("acme/widgets", "go", 3*time.Hour, 0, 20*time.Minute, "", "")
	cached.Cached = true
	rep := metrics.Compute(metrics.Input{
		Window: win,
		CIJobs: []model.CIJobTiming{
			cached,
			job("acme/widgets", "lint", 3*time.Hour, 10*time.Minute, 10*time.Minute, "slot", "for one of the 3 sandboxes"),
		},
	})
	q := rep.CIQueueing
	if q.Jobs != 1 || q.Cached != 1 {
		t.Errorf("jobs = %d, cached = %d; want one job measured and one counted apart", q.Jobs, q.Cached)
	}
	if q.QueuedShare != 0.5 {
		t.Errorf("QueuedShare = %v, want 0.5 -- the cache hit is in neither half", q.QueuedShare)
	}
	if len(q.ByJob) != 1 || q.ByJob[0].Job != "lint" {
		t.Errorf("ByJob = %+v, want the one job that asked for a machine", q.ByJob)
	}
}

// Each cause is a different setting to reach for, which is why they are
// kept apart -- and the sentence the gate said to the worst-hit job is
// kept with the bucket, because "budget" says where to look and the
// sentence says what to set it to.
func TestCIQueueingSplitsWaitingByCause(t *testing.T) {
	rep := metrics.Compute(metrics.Input{
		Window: win,
		CIJobs: []model.CIJobTiming{
			job("acme/widgets", "go", 2*time.Hour, 10*time.Minute, time.Minute, "slot", "for one of the 3 sandboxes"),
			job("acme/widgets", "ui", 3*time.Hour, 5*time.Minute, time.Minute, "slot", "for one of the 3 sandboxes"),
			job("acme/widgets", "image", 4*time.Hour, 30*time.Minute, time.Minute, "budget", "8 GiB free and this job asks for 16 GiB"),
		},
	})
	causes := rep.CIQueueing.ByCause
	if len(causes) != 2 {
		t.Fatalf("ByCause = %+v, want the two causes that held something", causes)
	}
	// Worst first: the budget held one job for longer than the count held
	// two, and an operator reading the top line should be sent to the
	// budget.
	if causes[0].Cause != "budget" || causes[0].Queued != 30*time.Minute || causes[0].Jobs != 1 {
		t.Errorf("worst cause = %+v, want 30m of budget over one job", causes[0])
	}
	if got, want := causes[0].Share, 30.0/45.0; got != want {
		t.Errorf("budget share = %v, want %v of all the waiting", got, want)
	}
	if causes[0].Reason != "8 GiB free and this job asks for 16 GiB" {
		t.Errorf("reason = %q, want the sentence said to the longest-waiting job of that cause", causes[0].Reason)
	}
	if causes[1].Cause != "slot" || causes[1].Queued != 15*time.Minute || causes[1].Jobs != 2 {
		t.Errorf("second cause = %+v, want 15m of the count over two jobs", causes[1])
	}
}

// Every job recorded before the cause was stored has a duration and no
// cause. It counts in the totals and under no cause, and the section says
// how much of itself is missing rather than guessing at the sentence.
func TestCIQueueingSaysHowMuchWaitingNothingAttributed(t *testing.T) {
	rep := metrics.Compute(metrics.Input{
		Window: win,
		CIJobs: []model.CIJobTiming{
			job("acme/widgets", "go", 2*time.Hour, 20*time.Minute, time.Minute, "", "for one of the 3 sandboxes"),
			job("acme/widgets", "lint", 3*time.Hour, 5*time.Minute, time.Minute, "slot", "for one of the 3 sandboxes"),
		},
	})
	q := rep.CIQueueing
	if q.Queued != 25*time.Minute {
		t.Errorf("queued = %s, want both waits counted whatever held them", q.Queued)
	}
	if q.Unattributed != 20*time.Minute {
		t.Errorf("Unattributed = %s, want the 20m nothing named a cause for", q.Unattributed)
	}
	if q.ByCause[0].Cause != metrics.UnattributedCause {
		t.Errorf("worst cause = %q, want the unattributed bucket to carry it under a name of its own", q.ByCause[0].Cause)
	}
}

// The suspicion this was all built to test: one repository's expensive
// jobs waiting while its cheap ones go straight in. ByJob keys on the
// repository as well as the name, so two repositories' "test" are two
// rows rather than one average of them.
func TestCIQueueingShowsWhichRepositoriesAndJobsWearIt(t *testing.T) {
	rep := metrics.Compute(metrics.Input{
		Window: win,
		CIJobs: []model.CIJobTiming{
			job("acme/widgets", "image", 2*time.Hour, 40*time.Minute, 20*time.Minute, "hole", "capacity is being held for a larger job"),
			job("acme/widgets", "lint", 2*time.Hour, 0, 20*time.Minute, "", ""),
			job("acme/docs", "lint", 3*time.Hour, time.Minute, 19*time.Minute, "slot", "for one of the 3 sandboxes"),
		},
	})
	q := rep.CIQueueing
	if len(q.ByRepo) != 2 || q.ByRepo[0].Repo != "acme/widgets" {
		t.Fatalf("ByRepo = %+v, want the worse-hit repository first", q.ByRepo)
	}
	if q.ByRepo[0].Queued != 40*time.Minute || q.ByRepo[0].Jobs != 2 {
		t.Errorf("worst repo = %+v, want 40m over its two jobs", q.ByRepo[0])
	}
	if q.ByRepo[0].TopCause != "hole" {
		t.Errorf("worst repo's cause = %q, want the reservation that held it", q.ByRepo[0].TopCause)
	}
	if len(q.ByJob) != 3 {
		t.Fatalf("ByJob = %+v, want a row per repository and job", q.ByJob)
	}
	worst := q.ByJob[0]
	if worst.Repo != "acme/widgets" || worst.Job != "image" || worst.QueuedShare < 0.66 {
		t.Errorf("worst job = %+v, want acme/widgets' image job waiting twice as long as it ran", worst)
	}
	// And the cheap job in the same repository, which waited for nothing:
	// the pair is the bias, and neither number alone is.
	var cheap metrics.CIQueueGroup
	for _, g := range q.ByJob {
		if g.Repo == "acme/widgets" && g.Job == "lint" {
			cheap = g
		}
	}
	if cheap.Jobs != 1 || cheap.Queued != 0 {
		t.Errorf("the cheap job in the same repo = %+v, want it measured and waiting for nothing", cheap)
	}
}

// Is the share rising? Buckets are the same slices Throughput's own trend
// line uses, so the two series are points over the same spans.
func TestCIQueueingBucketsTheShareAcrossTheWindow(t *testing.T) {
	rep := metrics.Compute(metrics.Input{
		Window:  win,
		Buckets: 7,
		CIJobs: []model.CIJobTiming{
			job("acme/widgets", "go", 12*time.Hour, 0, 10*time.Minute, "", ""),
			job("acme/widgets", "go", 6*24*time.Hour+12*time.Hour, 30*time.Minute, 10*time.Minute, "slot", "for one of the 3 sandboxes"),
		},
	})
	buckets := rep.CIQueueing.Buckets
	if len(buckets) != len(rep.Throughput.Buckets) {
		t.Fatalf("%d CI buckets against %d throughput buckets, want the same slicing", len(buckets), len(rep.Throughput.Buckets))
	}
	if buckets[0].Since != rep.Throughput.Buckets[0].Since {
		t.Errorf("first bucket starts at %s, want the same instant Throughput's does", buckets[0].Since)
	}
	if buckets[0].QueuedShare != 0 {
		t.Errorf("first day's share = %v, want nothing waiting", buckets[0].QueuedShare)
	}
	if got, want := buckets[len(buckets)-1].QueuedShare, 0.75; got != want {
		t.Errorf("last day's share = %v, want %v -- the same deployment a week later", got, want)
	}
}

// The window's own rule, applied to a job: it belongs to the window when
// it ended inside it, and a job still in flight belongs to nothing.
func TestCIQueueingWindowsAJobOnWhenItEnded(t *testing.T) {
	running := model.CIJobTiming{RunID: "r", Repo: "acme/widgets", Name: "go",
		StartedAt: at(6 * 24 * time.Hour), Queued: time.Hour}
	rep := metrics.Compute(metrics.Input{
		Window: win,
		CIJobs: []model.CIJobTiming{
			running,
			job("acme/widgets", "go", -time.Hour, time.Hour, time.Minute, "slot", "before the window"),
			job("acme/widgets", "go", time.Hour, 2*time.Minute, time.Minute, "slot", "inside it"),
		},
	})
	if got := rep.CIQueueing.Jobs; got != 1 {
		t.Errorf("jobs = %d, want the one that ended inside the window", got)
	}
	if got := rep.CIQueueing.Queued; got != 2*time.Minute {
		t.Errorf("queued = %s, want only the wait of the job that ended in the window", got)
	}
}

// A scope narrows this section by repository, because a CI job belongs to
// one -- and cannot narrow it by author, because a job belongs to a
// branch rather than to whoever filed a task (ci.go).
func TestAScopeNarrowsCIQueueingByRepositoryOnly(t *testing.T) {
	in := twoRepos()
	in.CIJobs = []model.CIJobTiming{
		job("acme/widgets", "go", 2*time.Hour, 10*time.Minute, time.Minute, "slot", "for one of the 3 sandboxes"),
		job("acme/docs", "go", 2*time.Hour, 20*time.Minute, time.Minute, "budget", "8 GiB free"),
	}
	in.Scope = metrics.Scope{Repos: []string{"acme/widgets"}}
	if got := metrics.Compute(in).CIQueueing.Queued; got != 10*time.Minute {
		t.Errorf("scoped queueing = %s, want acme/widgets' wait alone", got)
	}

	in.Scope = metrics.Scope{Authors: []string{"ada"}}
	if got := metrics.Compute(in).CIQueueing.Queued; got != 30*time.Minute {
		t.Errorf("author-scoped queueing = %s, want the deployment's whole CI: a job has no author", got)
	}
}

// The series a tuning decision is actually made from: the same job, run
// twice in different halves of the window, one of them ten minutes slower
// (grain/task-495). "This job got slower" and "this job waited longer"
// have opposite fixes, so a section that adds the two together cannot tell
// an operator which way to turn the dial.
func TestCIQueueingSeparatesAJobGettingSlowerFromAJobWaitingLonger(t *testing.T) {
	rep := metrics.Compute(metrics.Input{
		Window:  win,
		Buckets: 2,
		CIJobs: []model.CIJobTiming{
			// Early in the window: a short wait and a 15-minute run.
			job("acme/widgets", "go", 1*time.Hour, 2*time.Minute, 15*time.Minute, "slot", "for one of the 3 sandboxes"),
			// Late in it: the same job, waiting no longer and running
			// twice as long, which is contention and not work.
			job("acme/widgets", "go", 6*24*time.Hour, 2*time.Minute, 31*time.Minute, "slot", "for one of the 3 sandboxes"),
		},
	})
	q := rep.CIQueueing
	if q.Run.N != 2 || q.Run.Max != 31*time.Minute {
		t.Errorf("Run = %+v, want two runs with a 31m worst case", q.Run)
	}
	if len(q.ByJob) != 1 || len(q.ByJob[0].Buckets) != 2 {
		t.Fatalf("ByJob = %+v, want one job carrying a two-bucket series", q.ByJob)
	}
	early, late := q.ByJob[0].Buckets[0], q.ByJob[0].Buckets[1]
	if early.RunP50 != 15*time.Minute || late.RunP50 != 31*time.Minute {
		t.Errorf("run medians are %s then %s, want 15m then 31m -- the whole point of the series",
			early.RunP50, late.RunP50)
	}
	if early.WaitP50 != 2*time.Minute || late.WaitP50 != 2*time.Minute {
		t.Errorf("wait medians are %s then %s, want 2m both times -- the waiting did not change",
			early.WaitP50, late.WaitP50)
	}
	// And the same series at the top level, over every job at once.
	if len(q.Buckets) != 2 || q.Buckets[1].RunMax != 31*time.Minute {
		t.Errorf("Buckets = %+v, want the deployment's own series to show the same 31m", q.Buckets)
	}
}

// Only the busiest few rows carry a series, because a series per job for
// every job in every repository is a report nothing can read -- least of
// all an agent reading it inside a bounded answer.
func TestCIQueueingBoundsHowManyJobsCarryASeries(t *testing.T) {
	var jobs []model.CIJobTiming
	for i := 0; i < metrics.SeriesGroups+3; i++ {
		// Each job waits a little less than the one before, so the order
		// the series are handed out in is known.
		wait := time.Duration(metrics.SeriesGroups+3-i) * time.Minute
		jobs = append(jobs, job("acme/widgets", "job"+string(rune('a'+i)), 2*time.Hour, wait, time.Minute, "slot", "waiting"))
	}
	rep := metrics.Compute(metrics.Input{Window: win, CIJobs: jobs})
	q := rep.CIQueueing
	if len(q.ByJob) != len(jobs) {
		t.Fatalf("ByJob has %d rows, want every job", len(q.ByJob))
	}
	for i, g := range q.ByJob {
		hasSeries := len(g.Buckets) > 0
		if want := i < metrics.SeriesGroups; hasSeries != want {
			t.Errorf("row %d (%s) carries a series = %v, want %v", i, g.Job, hasSeries, want)
		}
	}
}

// The cache as a rate over the window, which is what a cumulative hit
// count cannot be: totals since boot only ever go up, so a cache that
// stopped working an hour ago still reads as mostly hits in them.
func TestCIQueueingReportsCacheHitsAsAShareOfTheWindow(t *testing.T) {
	cached := job("acme/widgets", "go", 3*time.Hour, 0, 20*time.Minute, "", "")
	cached.Cached = true
	rep := metrics.Compute(metrics.Input{
		Window: win,
		CIJobs: []model.CIJobTiming{
			cached,
			job("acme/widgets", "lint", 3*time.Hour, 0, 5*time.Minute, "", ""),
			job("acme/widgets", "go", 4*time.Hour, 0, 5*time.Minute, "", ""),
			job("acme/widgets", "lint", 4*time.Hour, 0, 5*time.Minute, "", ""),
		},
	})
	if got, want := rep.CIQueueing.CacheHitShare, 0.25; got != want {
		t.Errorf("CacheHitShare = %v, want %v -- one of the four jobs asked for was answered from the cache", got, want)
	}
}
