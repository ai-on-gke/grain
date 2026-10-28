package metrics

// How much of this deployment's CI job-time went on waiting for a
// machine rather than on running the tree, what held it, and who wore it.
//
// grain/task-326 put a duration and a sentence on every stored CI job:
// "18m queued, 4m running" instead of "22m", per job and added up per
// run. What it deliberately did not do is add them up across the
// deployment, and every question an operator actually has is an addition:
// is a quarter of CI's time going on waiting, is that share rising, which
// of the six things that can hold a job is holding them -- each sends an
// operator to a different setting -- and whether one repository's
// expensive jobs wait forty minutes while its cheap ones wait none, which
// was the suspicion that motivated measuring any of it and which nothing
// has ever measured either way.
//
// # Why the cause is a column and not a sentence parsed back
//
// Only one of the two things stored per job can be added up. A gate's
// reason says how the deployment stands at the instant it is asked ("8
// GiB free and this job asks for 16 GiB") and is a different string a
// minute later, so a job held by the budget for half an hour reads as a
// dozen unrelated short waits if the string is all there is -- which is
// the argument ci.Wait already makes for separating the two. The cause is
// computed at the source, has always been, and was simply thrown away
// once the winning sentence had been chosen; so this change keeps it
// (ci_job.queued_cause) rather than matching prose that every gate is
// free to rewrite. The cost is that jobs recorded before the column reach
// this with no cause at all: they count in every total here and under no
// cause, which Unattributed says out loud rather than papering over with
// a guess -- this package's own "a missing moment is skipped, never
// guessed", applied to a missing word.
//
// # What a window and a scope mean here
//
// The same as everywhere else in this package, with one stated exception.
// A job belongs to the window when the moment it *ended* falls inside it,
// so a job that queued across midnight contributes its whole wait to the
// day it finished; a job still in flight contributes to nothing.
//
// One bound is worth knowing before reading a long window: CI history is
// swept (ci.StoredRunRetention.History, five thousand concluded runs by
// default), so a report asking for ninety days of a busy deployment
// covers however much of that is still stored rather than all of it. The
// buckets show it -- the oldest ones go empty -- and Jobs is the
// denominator to read every share against, which is why it is reported
// first.
//
// The exception is the author half of a scope. A CI job belongs to a
// repository and a branch, not to a task and not to whoever filed one:
// nothing joins a run's ref back to the task that pushed it, and inventing
// that join would attribute a base-branch run or an operator's own
// `grain ci run` to somebody. So Scope.Repos narrows this section exactly
// as it narrows the rest, and Scope.Authors does not narrow it at all --
// read a report scoped to an author as "this author's tasks, and this
// deployment's CI", the way Cycles is already read.

import (
	"sort"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// UnattributedCause is what a wait nothing attributed is counted under:
// the empty cause given a word of its own, the way outcomeOf gives one to
// the empty outcome. It covers two different jobs -- one recorded before
// the cause was stored at all, and one whose whole wait was at a gate
// with nothing to say about itself -- and CIQueueing.Unattributed is the
// number to read before reading any share below it.
const UnattributedCause = "unattributed"

// CIQueueing is this deployment's CI waiting over the window: the share
// of job-time spent queueing, what it was spent on, and which
// repositories and jobs wore it.
//
// Everything here is over jobs that ended inside the window and were not
// served from the job cache. Cache hits are counted apart (Cached) rather
// than included as jobs that waited no time: such a job never asked for a
// machine, its timestamps belong to the run that did the work
// (ci.JobRun.Cached), and folding them in would let a deployment improve
// its queueing share by caching more -- which is a good thing to do and
// not an answer to "am I short of sandboxes".
type CIQueueing struct {
	// Jobs is the denominator: jobs that ended in the window, cache hits
	// excluded. Waited is how many of them waited at all, and NeverRan is
	// how many were given up on while still queueing -- a job that waited
	// and then was cancelled is the case this measurement was built for,
	// since its row has no duration of its own to show.
	Jobs     int
	Waited   int
	NeverRan int
	Cached   int
	// Queued is all of that waiting added together and Busy is all of the
	// working: job-time, not wall-clock, so both can exceed the window and
	// a deployment running four jobs at once spends four minutes of it a
	// minute.
	Queued time.Duration
	Busy   time.Duration
	// QueuedShare is Queued / (Queued + Busy) -- the one number this
	// section exists to produce. A deployment short of sandboxes shows up
	// here long before anybody notices a slow check, because a job that
	// waits twenty minutes and runs four looks exactly like a slow job
	// from everywhere else.
	QueuedShare float64
	// Wait is how long a job that waited waited, over the jobs that waited
	// at all. Read it beside QueuedShare: a high share made of a long tail
	// is a few jobs starving, and a high share made of a fat P50 is a
	// deployment that is simply full.
	Wait Distribution
	// Run is how long a job that got a machine spent on it -- the other
	// half of the same job, and the half that says whether the machines
	// this deployment did hand out were any good.
	//
	// It is here because a wait and a run move in opposite directions
	// under the same pressure, and the fix for one is the opposite of the
	// fix for the other. Cut concurrency and each job gets more of the
	// host: Run falls and Wait rises. Raise it and the reverse. So an
	// operator holding only Wait can tell that CI is slow and cannot tell
	// which way to turn the dial -- which is exactly the evening that
	// motivated this (grain/task-495): the same job ran 15-26 minutes
	// early on and 31-34 minutes later, on a host at 99% CPU, and that
	// ten minutes was contention rather than work. Nothing kept it, so
	// "is this worse than last week" was unanswerable and the only
	// evidence was a person reading run rows by eye.
	//
	// Over the jobs that ran at all, so its N is Jobs minus NeverRan, and
	// a cache hit is in neither (see the type's own note on Cached).
	Run Distribution
	// CacheHitShare is Cached over Cached+Jobs: how much of the CI this
	// deployment was asked for it did not have to run. A rate rather than
	// the cumulative hit and miss counters a health reading carries, which
	// is the difference between noticing a regression and not: totals
	// since boot can only ever go up, and a cache that stopped working an
	// hour ago still reads as mostly hits in them.
	CacheHitShare float64
	// Unattributed is how much of Queued no cause was recorded for, and so
	// how much of the by-cause breakdown below is missing. Before this
	// build stored a cause it is all of it.
	Unattributed time.Duration
	// ByCause is where the waiting went, worst first. Each cause is a
	// different setting to reach for -- the sandbox count, CI's share of
	// the host, a reservation held open for a big job, a capability grant,
	// a run's own width, the priority order -- which is the whole reason
	// they are kept apart.
	ByCause []CIQueueCause
	// ByRepo and ByJob are who wears it, worst first by time waited. ByJob
	// keys on the repository *and* the job name rather than the name
	// alone: two repositories with a job called "test" are two different
	// jobs on two different shapes of machine, and the question this
	// breakdown was built for is about one repository's jobs against each
	// other.
	ByRepo []CIQueueGroup
	ByJob  []CIQueueGroup
	// Buckets is the same share sliced across the window, oldest first --
	// what answers "and is it rising?". The last bucket is the only one
	// that can be partially elapsed.
	Buckets []CIQueueBucket
}

// CIQueueCause is one kind of thing that held jobs, and how much of the
// window's waiting it accounts for.
type CIQueueCause struct {
	// Cause is ci.WaitCause's own vocabulary as a string ("slot",
	// "budget", "hole", "grant", "run", "priority"), or
	// UnattributedCause. A cause this build has never heard of still
	// counts under its own name rather than being dropped, the way
	// Runs.Outcomes treats an outcome it does not know.
	Cause string
	// Jobs is how many jobs this cause held longest, Queued is their
	// waiting added up, and Share is that over CIQueueing.Queued.
	Jobs   int
	Queued time.Duration
	Share  float64
	// Longest is the longest single wait this cause held, and Reason is
	// what the gate said to that job as its wait ended -- the concrete
	// sentence behind the bucket, kept because "budget" sends an operator
	// to a setting and "8 GiB free and this job asks for 16 GiB" tells
	// them what to set it to.
	Longest time.Duration
	Reason  string
}

// CIQueueGroup is one repository's, or one job's, share of the waiting.
type CIQueueGroup struct {
	// Repo is "owner/name", and Job is the job's display name -- empty in
	// ByRepo, where the repository is the whole key.
	Repo string
	Job  string
	// Jobs, Queued, Busy and QueuedShare are CIQueueing's own fields over
	// this group alone, and mean what they mean there.
	Jobs        int
	Queued      time.Duration
	Busy        time.Duration
	QueuedShare float64
	Wait        Distribution
	Run         Distribution
	// TopCause is the cause that held this group longest, so a row can be
	// read without the breakdown above it: an "expensive job waits, cheap
	// job does not" bias shows up as the expensive rows saying "hole" or
	// "budget" while the cheap ones say "slot".
	TopCause string
	// Buckets is this group's own run-and-wait series across the window,
	// oldest first and on the same slices as everything else here -- the
	// highest-value thing this section reports, and the one that separates
	// "this job got slower" from "this job waited longer".
	//
	// Only the first SeriesGroups rows of a breakdown carry one -- the
	// busiest few, since a breakdown is already worst first by time waited
	// -- because a series per job for every job in every repository is a
	// report nothing can read: the whole point is to be readable by an
	// agent inside a bounded answer, and the jobs that matter to a tuning
	// decision are the ones wearing the time. A row with no series carries
	// no buckets rather than empty ones, so a reader can tell "not in the
	// top few" from "ran nothing".
	Buckets []CIQueueBucket
}

// SeriesGroups is how many of ByJob's rows carry their own Buckets, worst
// first. Eight is a full suite's worth of jobs for one repository (grain's
// own is seven as of this writing), which is the comparison a tuning
// decision is actually made from -- this job against that one, tonight
// against last week -- and it keeps the whole section inside the bounded
// answer an agent reads it through.
const SeriesGroups = 8

// CIQueueBucket is one slice of the window: how the waiting share stood
// during it, and how long a job took to wait and to run.
//
// The four durations are medians and maxima rather than whole
// Distributions for the reason SeriesGroups is bounded at all: a
// distribution per bucket per job is seven numbers where two will do, and
// the question a series answers is "which way is it moving". Each is over
// the jobs in this bucket alone, so a bucket that held one job reports
// that job twice and a bucket that held none reports zeroes -- Jobs is
// the denominator to read them against, as everywhere else here.
type CIQueueBucket struct {
	Since       time.Time
	Until       time.Time
	Jobs        int
	Queued      time.Duration
	Busy        time.Duration
	QueuedShare float64
	// RunP50 and RunMax are how long this bucket's jobs spent on a
	// machine; WaitP50 and WaitMax how long the ones that waited waited.
	// Read together they are the whole of "contention or admission": both
	// rising is a host that is simply oversubscribed, run rising alone is
	// jobs fighting each other on machines they already have, and wait
	// rising alone is jobs that cannot get one.
	RunP50  time.Duration
	RunMax  time.Duration
	WaitP50 time.Duration
	WaitMax time.Duration
}

// ciQueueingOf builds the section from the store's CI job rows.
//
// buckets is the window's own slicing, shared with Throughput so the two
// time series line up point for point; it may be empty for a window with
// no duration, which leaves Buckets empty and every total unaffected.
func ciQueueingOf(w Window, buckets []Bucket, jobs []model.CIJobTiming) CIQueueing {
	out := CIQueueing{}
	series := newJobSeries(buckets)

	type causeTotals struct {
		entry CIQueueCause
	}
	byCause := map[string]*causeTotals{}
	var causeOrder []string

	byRepo, byJob := map[string]*groupTotals{}, map[string]*groupTotals{}
	var repoOrder, jobOrder []string
	group := func(into map[string]*groupTotals, order *[]string, key string, entry CIQueueGroup) *groupTotals {
		g, ok := into[key]
		if !ok {
			g = &groupTotals{entry: entry, causes: map[string]time.Duration{}, series: newJobSeries(buckets)}
			into[key] = g
			*order = append(*order, key)
		}
		return g
	}

	var waits, runs []time.Duration
	for _, j := range jobs {
		if !w.holds(j.FinishedAt) {
			continue
		}
		if j.Cached {
			out.Cached++
			continue
		}
		out.Jobs++
		// A job that never got a machine has no StartedAt and did no
		// work; one that did has the span between the two. Neither is
		// ever guessed from the other end of the row.
		busy := time.Duration(0)
		if j.StartedAt == nil {
			out.NeverRan++
		} else if d := j.FinishedAt.Sub(*j.StartedAt); d > 0 {
			busy = d
		}
		queued := j.Queued
		if queued < 0 {
			queued = 0
		}
		out.Queued += queued
		out.Busy += busy
		if queued > 0 {
			out.Waited++
			waits = append(waits, queued)
		}
		if busy > 0 {
			runs = append(runs, busy)
		}

		cause := j.QueuedCause
		if cause == "" {
			cause = UnattributedCause
			out.Unattributed += queued
		}
		if queued > 0 {
			c, ok := byCause[cause]
			if !ok {
				c = &causeTotals{entry: CIQueueCause{Cause: cause}}
				byCause[cause] = c
				causeOrder = append(causeOrder, cause)
			}
			c.entry.Jobs++
			c.entry.Queued += queued
			if queued > c.entry.Longest {
				c.entry.Longest, c.entry.Reason = queued, j.QueuedReason
			}
		}

		for _, g := range []*groupTotals{
			group(byRepo, &repoOrder, j.Repo, CIQueueGroup{Repo: j.Repo}),
			group(byJob, &jobOrder, j.Repo+"\x00"+j.Name, CIQueueGroup{Repo: j.Repo, Job: j.Name}),
		} {
			g.entry.Jobs++
			g.entry.Queued += queued
			g.entry.Busy += busy
			if queued > 0 {
				g.waits = append(g.waits, queued)
				g.causes[cause] += queued
			}
			if busy > 0 {
				g.runs = append(g.runs, busy)
			}
			g.series.add(bucketOf(buckets, w, j.FinishedAt), queued, busy)
		}

		series.add(bucketOf(buckets, w, j.FinishedAt), queued, busy)
	}

	out.Wait = summarize(waits)
	out.Run = summarize(runs)
	out.QueuedShare = share(out.Queued, out.Busy)
	out.CacheHitShare = hitShare(out.Cached, out.Jobs)
	out.Buckets = series.finish()

	for _, name := range causeOrder {
		entry := byCause[name].entry
		if out.Queued > 0 {
			entry.Share = float64(entry.Queued) / float64(out.Queued)
		}
		out.ByCause = append(out.ByCause, entry)
	}
	sort.SliceStable(out.ByCause, func(i, j int) bool {
		if out.ByCause[i].Queued == out.ByCause[j].Queued {
			return out.ByCause[i].Cause < out.ByCause[j].Cause
		}
		return out.ByCause[i].Queued > out.ByCause[j].Queued
	})

	out.ByRepo = groupsOf(byRepo, repoOrder)
	out.ByJob = groupsOf(byJob, jobOrder)
	return out
}

// groupsOf finishes the per-group rows: the derived numbers that need the
// whole group, and the worst-first order every breakdown here is read in.
func groupsOf(groups map[string]*groupTotals, order []string) []CIQueueGroup {
	rows := make([]*groupTotals, 0, len(order))
	for _, key := range order {
		g := groups[key]
		g.entry.Wait = summarize(g.waits)
		g.entry.Run = summarize(g.runs)
		g.entry.QueuedShare = share(g.entry.Queued, g.entry.Busy)
		g.entry.TopCause = worstCause(g.causes)
		rows = append(rows, g)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i].entry, rows[j].entry
		if a.Queued == b.Queued {
			if a.Repo == b.Repo {
				return a.Job < b.Job
			}
			return a.Repo < b.Repo
		}
		return a.Queued > b.Queued
	})
	out := make([]CIQueueGroup, 0, len(rows))
	for i, g := range rows {
		entry := g.entry
		// The series is attached after the sort and only to the rows at
		// the top of it, which is why this loop is separate from the one
		// above: which rows are the busiest few is not known until they
		// are in order.
		if i < SeriesGroups {
			entry.Buckets = g.series.finish()
		}
		out = append(out, entry)
	}
	return out
}

// groupTotals is what a breakdown accumulates per key before groupsOf
// turns it into a row. It is declared here rather than inside
// ciQueueingOf because groupsOf takes it.
type groupTotals struct {
	entry  CIQueueGroup
	waits  []time.Duration
	runs   []time.Duration
	causes map[string]time.Duration
	series jobSeries
}

// jobSeries accumulates one run-and-wait series: the per-bucket totals,
// and the samples the medians in each bucket are taken from. The samples
// are kept rather than summarized as they arrive because a median cannot
// be accumulated -- which is the whole reason a series costs more than a
// total, and the reason only a bounded few of them are built (SeriesGroups).
type jobSeries struct {
	totals []CIQueueBucket
	waits  [][]time.Duration
	runs   [][]time.Duration
}

// newJobSeries lays out a series over the window's own slices. A window
// with no duration has none, which leaves every series empty rather than
// inventing a single bucket for it.
func newJobSeries(buckets []Bucket) jobSeries {
	if len(buckets) == 0 {
		return jobSeries{}
	}
	s := jobSeries{
		totals: make([]CIQueueBucket, len(buckets)),
		waits:  make([][]time.Duration, len(buckets)),
		runs:   make([][]time.Duration, len(buckets)),
	}
	for i, b := range buckets {
		s.totals[i].Since, s.totals[i].Until = b.Since, b.Until
	}
	return s
}

// add counts one job into bucket i, which is bucketOf's answer and so may
// be -1 for a job the window's slices do not cover -- ignored here rather
// than checked at every call site.
func (s *jobSeries) add(i int, queued, busy time.Duration) {
	if i < 0 || i >= len(s.totals) {
		return
	}
	s.totals[i].Jobs++
	s.totals[i].Queued += queued
	s.totals[i].Busy += busy
	if queued > 0 {
		s.waits[i] = append(s.waits[i], queued)
	}
	if busy > 0 {
		s.runs[i] = append(s.runs[i], busy)
	}
}

// finish turns the accumulated buckets into the series a report carries.
func (s jobSeries) finish() []CIQueueBucket {
	for i := range s.totals {
		b := &s.totals[i]
		b.QueuedShare = share(b.Queued, b.Busy)
		wait, run := summarize(s.waits[i]), summarize(s.runs[i])
		b.WaitP50, b.WaitMax = wait.P50, wait.Max
		b.RunP50, b.RunMax = run.P50, run.Max
	}
	return s.totals
}

// worstCause is whichever cause holds the most of a group's waiting, ties
// broken by name for the reason ci's own queueClock breaks them that way:
// a map's order is not an order, and the same report computed twice must
// not name a different cause each time.
func worstCause(causes map[string]time.Duration) string {
	var worst string
	var most time.Duration
	for cause, spent := range causes {
		if spent > most || (spent == most && (worst == "" || cause < worst)) {
			worst, most = cause, spent
		}
	}
	return worst
}

// share is queued / (queued + busy), and zero when there was neither --
// which is a deployment that ran no CI in the window rather than one that
// waited for nothing, and is why Jobs is reported beside every share here.
func share(queued, busy time.Duration) float64 {
	total := queued + busy
	if total <= 0 {
		return 0
	}
	return float64(queued) / float64(total)
}

// hitShare is cache hits over every job that was asked for, and zero for a
// window that asked for none -- a deployment that ran no CI rather than
// one whose cache never hit, which is why Cached and Jobs are both
// reported beside it.
func hitShare(cached, ran int) float64 {
	asked := cached + ran
	if asked <= 0 {
		return 0
	}
	return float64(cached) / float64(asked)
}
