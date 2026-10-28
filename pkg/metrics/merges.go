package metrics

// What this deployment actually lands, and what the merge queue spends
// getting it there: changes merged per hour over the window and over its
// own slices, the shape of the batches that carried them, and how long a
// task took to go from filed to landed.
//
// It is the last stretch of every other series here. Throughput counts
// tasks *completed* -- an agent stopping -- and Latency's LeadTime ends
// at the same moment, which is not the moment anybody waiting on a change
// is waiting for: after it comes a review, a queue, a batch, a suite, and
// on a bad night a repair and another turn round all four. Nothing
// measured that stretch, so "we are completing eight tasks a day" was
// quotable while "we are landing three" was not.
//
// # The two halves, and why they are one section
//
// The batch half is the dial. DefaultMergeBatchSize is three because
// three was a guess (model's own merge_batch_history.go), and the rule
// for moving it -- raise it while batches keep passing, lower it when
// they keep bisecting -- needs a pass rate over a window, which is
// exactly what nothing computed: model.SummarizeMergeBatches counts the
// whole kept history at once for a pane beside the setting, and a
// deployment that changed the size a fortnight ago cannot read last
// night off it.
//
// The lead-time half is what the dial is for. A bigger batch spends
// fewer suites per change and lands more per hour; it also fails as a
// unit, and every failure costs a bisect that the changes in it wait
// through. Those two effects show up in one number each -- LandedPerHour
// and OpenedToLanded -- and a tuning agent that moved the size and read
// only the first would be optimising CI cost by making people wait
// longer. Kept in one section so the trade is on one screen.
//
// # This package's rules, applied
//
// Nothing is stored: the batch rows already exist (model.MergeBatchRecord,
// one per finished batch run, bounded at MergeBatchHistoryKept) and the
// landing moment is a column the merge queue already writes
// (Observation.PrMergedAt, read here through model.TaskTiming). A window
// bounds measurements, not rows: a batch belongs to it when it *finished*
// inside it and a change when it *landed* inside it, so a task filed last
// month and merged this morning contributes its whole filed-to-landed
// time to this morning. And a missing moment is skipped: a task that
// never opened a pull request, one whose pull request is still open, and
// one whose pull request was closed unmerged are each in no distribution
// here rather than in one at zero.
//
// # What a scope does to it
//
// Landed, the buckets and the three lead times are about tasks, and are
// narrowed exactly as every other task measurement is -- by repo and by
// author both.
//
// The batch half is narrowed by repo alone, for the reason ci.go gives
// about CI jobs: a batch belongs to a repository and a base branch, its
// members are not recorded on it at all (MergeBatchRecord deliberately
// keeps no member list), and attributing one to whoever filed one of the
// changes in it would be an invention. So read an author-scoped report as
// "this author's changes, and this deployment's merge queue", the way
// Cycles and CIQueueing are already read.
//
// It is also why Batches.Changes and Landed are two different counts of
// what sounds like one thing, and neither is derivable from the other.
// Batches.Changes is what landed on a batch's verdict, whosever it was;
// Landed is how many of the tasks in scope had their pull request merge,
// including the ones an ordinary un-batched merge landed. A deployment
// that does not batch at all has a healthy Landed and no batches
// whatever.

import (
	"sort"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// Merges is how much this deployment landed during the window, how long
// each change took to get there, and what the merge queue's batching
// cost and saved.
type Merges struct {
	// Landed is how many of the window's tasks had their tracked pull
	// request merge inside it, and LandedPerHour and LandedPerDay are
	// that over the window's own length -- the rate two windows of
	// different lengths are compared by.
	//
	// Per hour as well as per day because the merge queue is the one part
	// of this deployment whose backlog is measured in hours: a queue that
	// lands two changes an hour and one that lands eight are different
	// afternoons, and the same pair of days rounds to a similar-looking
	// per-day figure.
	Landed        int
	LandedPerHour float64
	LandedPerDay  float64
	// Buckets is the same count sliced across the window, oldest first,
	// on the same slices Throughput and CIQueueing are sliced into, so a
	// landing rate that halves halfway through the window can be read
	// against the jobs, the host and the settings change beside it.
	Buckets []MergeBucket
	// FiledToLanded is the whole of what somebody who filed a task waited
	// for: filed -> the change merged. It is Latency.LeadTime's other
	// half and the number this section exists for.
	//
	// Over the tasks that landed in the window and were filed at all: a
	// task filed before created_at existed contributes to neither, which
	// is this package's own "a missing moment is skipped" and is why
	// every Distribution here carries its own N.
	FiledToLanded Distribution
	// CompletedToLanded is what happened after the agent stopped: the run
	// finishing -> the change merging. Review, the queue, the batch and
	// its suite, and any repair in between. Read beside Latency.LeadTime,
	// which ends where this begins: the two together are FiledToLanded,
	// and which of them is growing decides whether to look at the agents
	// or at the queue.
	CompletedToLanded Distribution
	// OpenedToLanded is the merge queue's own stretch: the pull request
	// opening -> it merging. Narrower than CompletedToLanded -- it leaves
	// out a run that opened its pull request early and kept working -- and
	// it is the one of the three that a batch-size change moves, so it is
	// the one to read against the batch counters below.
	OpenedToLanded Distribution
	// Batches is what batching cost and saved over the same window, or a
	// zeroed section for a deployment that formed none (Batches.Batches
	// is the denominator to read every share under it against).
	Batches MergeBatches
	// Repairs tracks duration and tool telemetry for automated branch repair runs.
	Repairs BranchRepairs
	// BaseHolds is how many changes each red stretch of a base branch held
	// in base_broken, and for how long (BaseHolds).
	BaseHolds BaseHolds
	// Ports is how the ports that landed in the window were made.
	Ports MergePorts
	// Contention is what the queue's contention cost over the window:
	// batch sizes, how long each batch's suite took, each change's time in
	// the queue, and what the queue had to do over again
	// (MergeQueueContention).
	Contention MergeQueueContention
	// Rechecks is what the overlap re-check said about the window's
	// merges (MergeRechecks): the evidence a repository is switched from
	// observe to enforce on.
	Rechecks MergeRechecks
}

// MergeRechecks counts the merge queue's overlap-rule records
// (model.MergeRecheckRecord) decided inside the window
// (docs/merge-queue-behind.md "The overlap re-check").
//
// Triggered over Merges is the rate of second suites enforce mode would
// spend; TriggeredAndCaught is what those suites bought, and
// SkippedBaseRed the upper bound on what skipping cost -- a skipped merge
// whose branch's own next run went red, which may be this change, a flake
// or another merge judged in the same run.
type MergeRechecks struct {
	Merges      int
	Triggered   int
	Skipped     int
	TriggerRate float64
	// BaseCurrent is the skipped merges that had nothing landed since
	// their fork at all.
	BaseCurrent int
	// Clauses counts each clause over the triggered merges (a merge on
	// which two fired counts under both), and Packages counts the Go
	// package clause 2 first fired on -- the hot-package number.
	Clauses  map[string]int
	Packages map[string]int
	// Rechecked is how many were in fact tested on top of their branch
	// (a batch, or an enforced re-check of one).
	Rechecked          int
	TriggeredAndCaught int
	SkippedAndCaught   int
	SkippedBaseRed     int
	// Modes counts the records by the repository's mode at the time.
	Modes map[string]int
	// Reaches is the oldest record handed over, inside the window or not.
	Reaches *time.Time
}

// mergeRechecksOf counts the records decided inside the window.
func mergeRechecksOf(w Window, records []model.MergeRecheckRecord) MergeRechecks {
	var kept []model.MergeRecheckRecord
	var reaches *time.Time
	for _, r := range records {
		at := r.DecidedAt
		if reaches == nil || at.Before(*reaches) {
			oldest := at
			reaches = &oldest
		}
		if w.holds(&at) {
			kept = append(kept, r)
		}
	}
	s := model.SummarizeMergeRechecks(kept)
	out := MergeRechecks{
		Merges: s.Merges, Triggered: s.Triggered, Skipped: s.Skipped, TriggerRate: s.TriggerRate(),
		BaseCurrent: s.BaseCurrent, Clauses: s.Clauses, Packages: map[string]int{},
		Rechecked: s.Rechecked, TriggeredAndCaught: s.TriggeredAndCaught,
		SkippedAndCaught: s.SkippedAndCaught, SkippedBaseRed: s.SkippedBaseRed,
		Modes: map[string]int{}, Reaches: reaches,
	}
	for _, r := range kept {
		out.Modes[string(r.Mode)]++
		if pkg := r.SamePackage(); r.Triggered && pkg != "" {
			out.Packages[pkg]++
		}
	}
	return out
}

// MergePorts splits the ports that landed in the window by who made
// them: grain itself, cherry-picking with no sandbox and no model
// (model.PortMechanical), or an agent run (model.PortByAgent) -- a
// conflicting pick, or a mechanical port whose CI went red and was sent
// to the merge queue's repair, counts as Repaired as well as Mechanical.
type MergePorts struct {
	Mechanical int
	Agent      int
	// Repaired is how many of Mechanical the merge queue had to send to an
	// agent after all.
	Repaired int
	// MechanicalRate is Mechanical over all the window's landed ports.
	MechanicalRate float64
}

// BranchRepairs tracks duration and tool telemetry for automated branch repair runs.
type BranchRepairs struct {
	Runs                   int
	Passes                 int
	Settled                int
	SettleRate             float64
	TimeToSettle           Distribution
	ConflictTimeToSettle   Distribution
	ConflictPasses         int
	TestRepairTimeToSettle Distribution
	TestRepairPasses       int
	ToolCalls              int
	ToolCallsPerRun        Counts
	ErroredToolCalls       int
	ErroredToolCallRate    float64
	TranscriptBytes        Counts
	TopTools               []ToolUse
}

// BaseHolds is the window's base-branch repair passes, measured by what
// they held: while a base branch is red, the changes queued for it wait
// in base_broken until its repair lands (model.BaseHoldPass).
type BaseHolds struct {
	// Passes is how many finished inside the window, and Open how many
	// are still being measured -- on a red branch right now -- which are
	// in none of the distributions, since none of them has an end yet.
	Passes int
	Open   int
	// Peak is the most changes held at once during each pass, Total how
	// many different changes each held at all, and PRHeld the PR-time
	// each held (two changes for an hour is two hours). Length is how
	// long each pass lasted, for reading PRHeld against.
	Peak   Counts
	Total  Counts
	PRHeld Distribution
	Length Distribution
	// Reaches is the oldest pass in the history handed over, for
	// MergeBatches.Reaches' reason: the history is bounded
	// (model.BaseHoldHistoryKept).
	Reaches *time.Time
}

// baseHoldsOf windows the hold history and summarizes it.
func baseHoldsOf(w Window, passes []model.BaseHoldPass) BaseHolds {
	var out BaseHolds
	var peaks, totals []int64
	var held, length []time.Duration
	for _, p := range passes {
		if out.Reaches == nil || p.StartedAt.Before(*out.Reaches) {
			at := p.StartedAt
			out.Reaches = &at
		}
		if p.FinishedAt == nil {
			out.Open++
			continue
		}
		if !w.holds(p.FinishedAt) {
			continue
		}
		out.Passes++
		peaks = append(peaks, int64(p.Peak))
		totals = append(totals, int64(p.Total()))
		held = append(held, p.PRHeld())
		length = append(length, p.FinishedAt.Sub(p.StartedAt))
	}
	out.Peak = summarizeCounts(peaks)
	out.Total = summarizeCounts(totals)
	out.PRHeld = summarize(held)
	out.Length = summarize(length)
	return out
}

// MergeBucket is one slice of the window: how much landed during it.
//
// PerHour is over this slice's own length rather than the window's, so a
// series is comparable point to point and against the window's own
// LandedPerHour. The last slice is the only one that can be partially
// elapsed, and it is reported at the rate it managed over the part of it
// that has happened.
type MergeBucket struct {
	Since   time.Time
	Until   time.Time
	Landed  int
	PerHour float64
}

// MergeBatches is the window's batch history: the shape of the bets the
// merge queue made and how they came out.
//
// It is model.MergeBatchStats over the batches that finished inside the
// window, plus the two things a rate needs and a lifetime counter cannot
// give: the size distribution, and the pass rates as rates. The counting
// itself is model's (SummarizeMergeBatches), deliberately, so the numbers
// here and the numbers in the Settings pane beside the batch size cannot
// come to disagree.
type MergeBatches struct {
	// Batches is how many finished inside the window and Changes how many
	// changes landed on one of their verdicts. Batches is the denominator
	// for every share below; read it first, since a pass rate over four
	// batches is not a pass rate.
	Batches int
	Changes int
	// Fresh is the batches formed from the queue rather than from the
	// wreck of a failed one, FirstTry how many of those passed, and
	// FirstTryRate the two divided -- the pass rate worth quoting, since a
	// halved batch passing is the bisect working rather than the bet
	// paying off.
	Fresh        int
	FirstTry     int
	FirstTryRate float64
	// Passed, Split, Culprits and Abandoned partition Batches by outcome
	// in the merge queue's own words (model.MergeBatchMerged and the three
	// beside it), and PassRate is Passed over Batches. SplitRate is Split
	// over Batches, which is the other side of the decision: batches that
	// had to be bisected are what says the size is too big.
	Passed    int
	PassRate  float64
	Split     int
	SplitRate float64
	Culprits  int
	Abandoned int
	// AbandonedBy partitions Abandoned by why each batch produced no
	// answer (model.MergeBatchRecord.Reason), most first; rows written
	// before the reason was recorded count as model.AbandonUnrecorded.
	AbandonedBy []MergeBatchAbandonReason
	// MeanSize is how many members a batch actually had on average and
	// Biggest the largest single bet. Sizes is the whole distribution,
	// smallest first -- a mean of 1.8 is two very different deployments
	// depending on whether it is made of twos or of ones and threes.
	MeanSize float64
	Biggest  int
	Sizes    []MergeBatchSize
	// Limit is what the batch size is configured to now
	// (Config.MergeBatchSizeOr, as the caller supplied it) and AtLimit how
	// many batches took every member it allowed while more were queued
	// behind them. AtLimit is the only evidence that raising the limit
	// would buy anything: a deployment whose batches are all smaller than
	// the limit is not one whose limit is too low, however well they do.
	Limit   int
	AtLimit int
	// Suites is what batching spent in whole CI suites, SerialSuites what
	// a one-at-a-time queue would have spent to land the same changes, and
	// Saved the difference -- negative for a window whose batches kept
	// failing.
	Suites       int
	SerialSuites int
	Saved        int
	// Reaches is the oldest batch in the history the caller handed over,
	// over all of it rather than over the window, and nil where it handed
	// over none. The history is bounded (model.MergeBatchHistoryKept), so
	// a window reaching back further than it covers has fewer batches in
	// it than its dates suggest, and this is the field that says so --
	// Changes.Reaches' job, for the same reason.
	Reaches *time.Time
}

// MergeBatchAbandonReason is one reason batches were abandoned and how
// many were.
type MergeBatchAbandonReason struct {
	Reason  string
	Batches int
}

// MergeBatchSize is one size of bet and how many were made at it.
type MergeBatchSize struct {
	// Size is how many members those batches tested, Batches how many of
	// them there were, and Passed how many of those passed -- the pass
	// rate per size, which is the comparison a size decision is actually
	// made from: threes passing as often as twos is the case for four.
	Size    int
	Batches int
	Passed  int
	// Share is Batches over the window's batches, so a size that is the
	// usual case is visible without arithmetic.
	Share float64
}

// MergeQueueContention is what the queue's contention cost over the window:
// how big its batches were and how long their suites took, how long
// changes waited in it, and what it had to do over again because
// something else moved.
type MergeQueueContention struct {
	// BatchSizes and GateLatency are over the batches that finished in the
	// window (model.MergeBatchRecord): each batch's member count, and its
	// suite's start to its verdict.
	BatchSizes  Counts       `json:"batchSizes"`
	GateLatency Distribution `json:"gateLatency"`
	// QueueWaitTime is each change that landed in the window, from the
	// moment the queue first took it on (TaskTiming.QueueEnteredAt) to its
	// merge -- the whole of its time in the queue, repairs and rebases
	// included, and not the pull request's lifetime, which starts while
	// the agent is still working. A change that merged on the cycle that
	// took it on is a zero, and counted as one: dropping those is what
	// would make a fast queue read slow. A change the queue never took on
	// (merged by hand, or landed before the stamp existed) has no sample.
	QueueWaitTime Distribution                 `json:"queueWaitTime"`
	RebaseRetest  RebaseRetestContention       `json:"rebaseRetest"`
	Buckets       []MergeQueueContentionBucket `json:"buckets,omitempty"`
}

// RebaseRetestContention counts the queue's own record of doing things
// over (model.MergeQueueContentionEvent) inside the window.
type RebaseRetestContention struct {
	// Rebases is how many times the queue merged a base branch into a
	// stale head (model.MergeQueueRebase).
	Rebases int `json:"rebases"`
	// Retests is how many suites the queue ran again about changes it had
	// already judged (model.MergeQueueRetest): the suite after each rebase,
	// every uncached flake recheck of a queued change or a batch, and every
	// half of a failed batch tested again.
	Retests int `json:"retests"`
	// TotalContention is the two summed, so a rebase is in it twice --
	// once as the new tree and once as the suite that tree cost.
	TotalContention int `json:"totalContention"`
	// ContentionRate is TotalContention per change that landed in the
	// window, and zero for a window in which nothing landed.
	ContentionRate float64 `json:"contentionRate"`
}

// MergeQueueContentionBucket is one slice of the contention series.
type MergeQueueContentionBucket struct {
	Since         time.Time    `json:"since"`
	Until         time.Time    `json:"until"`
	BatchSizes    Counts       `json:"batchSizes"`
	GateLatency   Distribution `json:"gateLatency"`
	QueueWaitTime Distribution `json:"queueWaitTime"`
	Rebases       int          `json:"rebases"`
	Retests       int          `json:"retests"`
}

// mergeQueueContentionOf builds Merges.Contention. Each sample is placed in
// the window, and in a bucket, by its own moment: a batch by its verdict,
// a wait by the merge that ended it, an event by when it happened.
func mergeQueueContentionOf(w Window, buckets []Bucket, tasks []model.TaskTiming, records []model.MergeBatchRecord, events []model.MergeQueueContentionEvent) MergeQueueContention {
	out := MergeQueueContention{}
	if len(buckets) > 0 {
		out.Buckets = make([]MergeQueueContentionBucket, len(buckets))
		for i, b := range buckets {
			out.Buckets[i].Since, out.Buckets[i].Until = b.Since, b.Until
		}
	}

	type bucketData struct {
		batchSizes     []int64
		gateLatencies  []time.Duration
		queueWaitTimes []time.Duration
	}
	bData := make([]bucketData, len(buckets))

	var allBatchSizes []int64
	var allGateLatencies []time.Duration
	var allQueueWaitTimes []time.Duration

	for _, r := range records {
		finished := r.FinishedAt
		if !w.holds(&finished) {
			continue
		}
		idx := bucketOf(buckets, w, &finished)
		if r.Size > 0 {
			allBatchSizes = append(allBatchSizes, int64(r.Size))
			if idx >= 0 {
				bData[idx].batchSizes = append(bData[idx].batchSizes, int64(r.Size))
			}
		}
		if gateDur := r.FinishedAt.Sub(r.StartedAt); gateDur > 0 {
			allGateLatencies = append(allGateLatencies, gateDur)
			if idx >= 0 {
				bData[idx].gateLatencies = append(bData[idx].gateLatencies, gateDur)
			}
		}
	}

	landed := 0
	for _, t := range tasks {
		if !w.holds(t.PrMergedAt) {
			continue
		}
		landed++
		if t.QueueEnteredAt == nil {
			continue
		}
		// Zero kept; only a merge stamped before the entry (a clock that
		// went backwards) is no sample at all.
		wait := t.PrMergedAt.Sub(*t.QueueEnteredAt)
		if wait < 0 {
			continue
		}
		allQueueWaitTimes = append(allQueueWaitTimes, wait)
		if idx := bucketOf(buckets, w, t.PrMergedAt); idx >= 0 {
			bData[idx].queueWaitTimes = append(bData[idx].queueWaitTimes, wait)
		}
	}

	for _, e := range events {
		at := e.At
		if !w.holds(&at) {
			continue
		}
		idx := bucketOf(buckets, w, &at)
		switch e.Kind {
		case model.MergeQueueRebase:
			out.RebaseRetest.Rebases++
			if idx >= 0 {
				out.Buckets[idx].Rebases++
			}
		case model.MergeQueueRetest:
			out.RebaseRetest.Retests++
			if idx >= 0 {
				out.Buckets[idx].Retests++
			}
		}
	}

	out.BatchSizes = summarizeCounts(allBatchSizes)
	out.GateLatency = summarize(allGateLatencies)
	out.QueueWaitTime = summarize(allQueueWaitTimes)
	out.RebaseRetest.TotalContention = out.RebaseRetest.Rebases + out.RebaseRetest.Retests
	if landed > 0 {
		out.RebaseRetest.ContentionRate = float64(out.RebaseRetest.TotalContention) / float64(landed)
	}
	for i := range out.Buckets {
		out.Buckets[i].BatchSizes = summarizeCounts(bData[i].batchSizes)
		out.Buckets[i].GateLatency = summarize(bData[i].gateLatencies)
		out.Buckets[i].QueueWaitTime = summarize(bData[i].queueWaitTimes)
	}
	return out
}

// mergesOf builds the section.
//
// buckets is the window's own slicing, shared with Throughput so the two
// series line up point for point; it may be empty for a window with no
// duration, which leaves Buckets empty and every total unaffected. limit
// is the configured batch size, which AtLimit is measured against and
// which the caller reads off the deployment's settings -- zero for a
// caller that has none, exactly as MaxConcurrent is.
func mergesOf(w Window, buckets []Bucket, tasks []model.TaskTiming, records []model.MergeBatchRecord, limit int, runs []model.RunTiming, uses []model.RunToolUse, transcripts []model.RunTranscript, events []model.MergeQueueContentionEvent) Merges {
	out := Merges{}
	if len(buckets) > 0 {
		out.Buckets = make([]MergeBucket, len(buckets))
		for i, b := range buckets {
			out.Buckets[i].Since, out.Buckets[i].Until = b.Since, b.Until
		}
	}

	var filed, completed, opened []time.Duration
	for _, t := range tasks {
		if !w.holds(t.PrMergedAt) {
			continue
		}
		out.Landed++
		switch t.Port {
		case model.PortMechanical:
			out.Ports.Mechanical++
			if t.FixTaskFiled {
				out.Ports.Repaired++
			}
		case model.PortByAgent:
			out.Ports.Agent++
		}
		if i := bucketOf(buckets, w, t.PrMergedAt); i >= 0 {
			out.Buckets[i].Landed++
		}
		landed := *t.PrMergedAt
		if t.CreatedAt != nil {
			filed = appendIfPositive(filed, landed.Sub(*t.CreatedAt))
		}
		if t.CompletedAt != nil {
			completed = appendIfPositive(completed, landed.Sub(*t.CompletedAt))
		}
		if t.PrOpenedAt != nil {
			opened = appendIfPositive(opened, landed.Sub(*t.PrOpenedAt))
		}
	}
	if n := out.Ports.Mechanical + out.Ports.Agent; n > 0 {
		out.Ports.MechanicalRate = float64(out.Ports.Mechanical) / float64(n)
	}
	out.FiledToLanded = summarize(filed)
	out.CompletedToLanded = summarize(completed)
	out.OpenedToLanded = summarize(opened)

	if hours := w.Duration().Hours(); hours > 0 {
		out.LandedPerHour = float64(out.Landed) / hours
		out.LandedPerDay = out.LandedPerHour * 24
	}
	for i := range out.Buckets {
		b := &out.Buckets[i]
		if hours := b.Until.Sub(b.Since).Hours(); hours > 0 {
			b.PerHour = float64(b.Landed) / hours
		}
	}

	out.Batches = mergeBatchesOf(w, records, limit)
	out.Repairs = branchRepairsOf(w, tasks, runs, uses, transcripts)
	out.Contention = mergeQueueContentionOf(w, buckets, tasks, records, events)
	return out
}

// repairRunsOf is the window's repair runs: every run of a task grain
// filed as a fix (TaskTiming.FixTaskFiled), and every run of a merge-queue
// repair that started once the repair began, over the runs that finished
// inside the window.
func repairRunsOf(w Window, tasks []model.TaskTiming, runs []model.RunTiming) map[string]bool {
	repairTaskAt := make(map[string]*time.Time)
	for _, t := range tasks {
		if t.MergeQueueRepairAt != nil {
			repairTaskAt[t.TaskID] = t.MergeQueueRepairAt
		} else if t.FixTaskFiled {
			repairTaskAt[t.TaskID] = nil
		}
	}
	out := make(map[string]bool)
	for _, r := range runs {
		if repairAt, ok := repairTaskAt[r.TaskID]; ok {
			if repairAt == nil || !r.StartedAt.Before(*repairAt) {
				if w.holds(r.FinishedAt) {
					out[r.RunID] = true
				}
			}
		}
	}
	return out
}

// branchRepairsOf derives time-to-settle, tool telemetry, and transcript size
// metrics for automated branch repair passes and runs within the window.
func branchRepairsOf(w Window, tasks []model.TaskTiming, runs []model.RunTiming, uses []model.RunToolUse, transcripts []model.RunTranscript) BranchRepairs {
	out := BranchRepairs{}

	var allDurations, conflictDurations, testRepairDurations []time.Duration

	for _, t := range tasks {
		if t.MergeQueueRepairAt != nil {
			repairAt := *t.MergeQueueRepairAt

			var settledAt *time.Time
			var merged bool
			if t.PrMergedAt != nil && !t.PrMergedAt.Before(repairAt) {
				settledAt = t.PrMergedAt
				merged = true
			} else if t.CompletedAt != nil && !t.CompletedAt.Before(repairAt) {
				settledAt = t.CompletedAt
			} else if t.ClosedAt != nil && !t.ClosedAt.Before(repairAt) {
				settledAt = t.ClosedAt
			}

			if settledAt != nil && w.holds(settledAt) {
				dur := settledAt.Sub(repairAt)
				if dur >= 0 {
					out.Passes++
					if merged {
						out.Settled++
					}
					allDurations = append(allDurations, dur)
					reason := strings.ToLower(t.MergeQueueRepairReason)
					if strings.Contains(reason, "conflict") {
						out.ConflictPasses++
						conflictDurations = append(conflictDurations, dur)
					} else {
						out.TestRepairPasses++
						testRepairDurations = append(testRepairDurations, dur)
					}
				}
			}
		}
	}

	if out.Passes > 0 {
		out.SettleRate = float64(out.Settled) / float64(out.Passes)
	}
	out.TimeToSettle = summarize(allDurations)
	out.ConflictTimeToSettle = summarize(conflictDurations)
	out.TestRepairTimeToSettle = summarize(testRepairDurations)

	repairRunIDs := repairRunsOf(w, tasks, runs)

	out.Runs = len(repairRunIDs)
	if out.Runs == 0 && len(repairRunIDs) == 0 {
		return out
	}

	callsPerRun := make(map[string]int64, len(repairRunIDs))
	for runID := range repairRunIDs {
		callsPerRun[runID] = 0
	}

	type series struct {
		use     ToolUse
		bytes   int64
		sizes   model.SizeHistogram
		dropped model.SizeHistogram
	}
	byName := make(map[string]*series)
	var names []string

	for _, use := range uses {
		if !repairRunIDs[use.RunID] {
			continue
		}
		entry, ok := byName[use.Tool]
		if !ok {
			entry = &series{
				use:     ToolUse{Name: use.Tool},
				sizes:   model.SizeHistogram{},
				dropped: model.SizeHistogram{},
			}
			byName[use.Tool] = entry
			names = append(names, use.Tool)
		}
		entry.use.Runs++
		entry.use.Calls += use.Calls
		entry.use.Errored += use.Errored
		entry.use.TimedOut += use.TimedOut
		entry.use.Cut.Calls += use.Elided
		entry.bytes += use.ResultBytes
		if use.MaxResultBytes > entry.use.ResultBytes.Max {
			entry.use.ResultBytes.Max = use.MaxResultBytes
		}
		entry.sizes.Merge(use.Sizes)
		entry.dropped.Merge(use.Dropped)

		out.ToolCalls += use.Calls
		out.ErroredToolCalls += use.Errored
		callsPerRun[use.RunID] += int64(use.Calls)
	}

	if out.ToolCalls > 0 {
		out.ErroredToolCallRate = float64(out.ErroredToolCalls) / float64(out.ToolCalls)
	}
	perRun := make([]int64, 0, len(callsPerRun))
	for _, calls := range callsPerRun {
		perRun = append(perRun, calls)
	}
	out.ToolCallsPerRun = summarizeCounts(perRun)

	for _, name := range names {
		entry := byName[name]
		use := entry.use
		if use.Calls > 0 {
			use.ErrorRate = float64(use.Errored) / float64(use.Calls)
			use.TimeoutRate = float64(use.TimedOut) / float64(use.Calls)
			use.Cut.Rate = float64(use.Cut.Calls) / float64(use.Calls)
			use.ResultBytes.Mean = entry.bytes / int64(use.Calls)
		}
		use.Cut.N = entry.dropped.Total()
		use.Cut.DroppedP50AtMost = sizeQuantile(entry.dropped, 0.50)
		use.Cut.DroppedP95AtMost = sizeQuantile(entry.dropped, 0.95)
		use.ResultBytes.N = entry.sizes.Total()
		use.ResultBytes.P50AtMost = sizeQuantile(entry.sizes, 0.50)
		use.ResultBytes.P95AtMost = sizeQuantile(entry.sizes, 0.95)
		use.ResultBytes.P99AtMost = sizeQuantile(entry.sizes, 0.99)
		out.TopTools = append(out.TopTools, use)
	}
	sort.SliceStable(out.TopTools, func(i, j int) bool {
		if out.TopTools[i].Calls == out.TopTools[j].Calls {
			return out.TopTools[i].Name < out.TopTools[j].Name
		}
		return out.TopTools[i].Calls > out.TopTools[j].Calls
	})

	var transcriptBytes []int64
	for _, tr := range transcripts {
		if repairRunIDs[tr.RunID] {
			transcriptBytes = append(transcriptBytes, int64(tr.Bytes))
		}
	}
	out.TranscriptBytes = summarizeCounts(transcriptBytes)

	return out
}

// mergeBatchesOf counts the batches that finished inside the window, and
// says how far back the history it was given reaches whether they did or
// not.
func mergeBatchesOf(w Window, records []model.MergeBatchRecord, limit int) MergeBatches {
	out := MergeBatches{Limit: limit}
	kept := make([]model.MergeBatchRecord, 0, len(records))
	bySize := map[int]*MergeBatchSize{}
	var sizes []int
	for _, r := range records {
		finished := r.FinishedAt
		if out.Reaches == nil || finished.Before(*out.Reaches) {
			// Over every record handed over rather than over the window,
			// for Changes.Reaches' reason: this is how far back the
			// *history* goes, which is what says whether a window with few
			// batches in it means anything.
			oldest := finished
			out.Reaches = &oldest
		}
		if !w.holds(&finished) {
			continue
		}
		kept = append(kept, r)
		size, ok := bySize[r.Size]
		if !ok {
			size = &MergeBatchSize{Size: r.Size}
			bySize[r.Size] = size
			sizes = append(sizes, r.Size)
		}
		size.Batches++
		if r.Outcome == model.MergeBatchMerged {
			size.Passed++
		}
	}
	if len(kept) == 0 {
		return out
	}

	s := model.SummarizeMergeBatches(kept, limit)
	out.Batches, out.Changes = s.Batches, s.Changes
	out.Fresh, out.FirstTry = s.Fresh, s.FirstTry
	out.Passed, out.Split, out.Culprits, out.Abandoned = s.Passed, s.Split, s.Culprits, s.Abandoned
	for reason, n := range s.AbandonedBy {
		out.AbandonedBy = append(out.AbandonedBy, MergeBatchAbandonReason{Reason: reason, Batches: n})
	}
	sort.Slice(out.AbandonedBy, func(i, j int) bool {
		a, b := out.AbandonedBy[i], out.AbandonedBy[j]
		if a.Batches != b.Batches {
			return a.Batches > b.Batches
		}
		return a.Reason < b.Reason
	})
	out.Biggest, out.AtLimit = s.Biggest, s.AtLimit
	out.Suites, out.SerialSuites, out.Saved = s.Suites, s.SerialSuites, s.Saved
	if s.Batches > 0 {
		out.MeanSize = float64(s.TotalSize) / float64(s.Batches)
		out.PassRate = float64(s.Passed) / float64(s.Batches)
		out.SplitRate = float64(s.Split) / float64(s.Batches)
	}
	if s.Fresh > 0 {
		out.FirstTryRate = float64(s.FirstTry) / float64(s.Fresh)
	}

	sort.Ints(sizes)
	out.Sizes = make([]MergeBatchSize, 0, len(sizes))
	for _, n := range sizes {
		entry := *bySize[n]
		entry.Share = float64(entry.Batches) / float64(out.Batches)
		out.Sizes = append(out.Sizes, entry)
	}
	return out
}
