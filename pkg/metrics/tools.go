package metrics

// What happened inside a run, over a window.
//
// Everything else this package measures is about the outside of a run --
// when it started, how long it took, how it was recorded. This is the
// inside: how many tool calls a run makes, how many of them fail, which
// tool the failures are in, how big the answers are, and how the CI loop
// the prompt sends every run around actually goes. Every finding in
// docs/agent-ergonomics.md was argued from code-reading and single
// transcripts because none of this had an aggregate to argue from.
//
// The package's three rules still hold, with one stated exception. The
// census rows this reads are the one thing in the store that had to be
// written rather than derived (model/telemetry.go says why), and given
// them, everything here is derived: no counter, nothing to reset, and a
// report that changes when the rows do. A row belongs to a window when
// its *run* finished inside it -- census rows carry no moment of their
// own, and the run's ending is the moment the measurement was taken. And
// what was never recorded is skipped: a run from before these tables
// existed contributes to nothing rather than to a zero, which is why
// Tools.Runs is reported next to every rate drawn from it.

import (
	"math"
	"sort"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
)

// Counts summarises a series of plain numbers -- calls in a run, pushes
// before a green build. Nearest-rank percentiles over the samples
// themselves, exactly like Distribution, so every number reported is one
// that really happened.
type Counts struct {
	N     int
	Min   int64
	P50   int64
	P90   int64
	P99   int64
	Max   int64
	Mean  float64
	Total int64
}

// Sizes is how big one tool's answers were.
//
// Max and Mean are exact. The percentiles are not, and say so in their
// names: they come from the base-2 histogram each census row carries
// (model.SizeHistogram), so each is the bound of the bucket the p'th
// sample fell in -- "95% of read_file answers were at most 65535 bytes"
// is true, and the real number is somewhere in the octave below it.
//
// That is deliberate rather than a shortcut. An exact percentile needs
// every sample, which means a row per tool call rather than per tool per
// run -- hundreds a run, read whole on every report. A bound within a
// factor of two is enough for the question these exist to answer: what
// cap keeps most answers intact. It was, in the event --
// mcp.maxToolResultBytes is 16 KB rather than the 64 KB guess it was
// left at because of what these said about run_command's and read_file's
// answers over one real deployment's 90 days (grain/task-183).
//
// What it cannot do a second time is size the cap that now bounds it.
// These are the sizes of answers as the run received them, so a cap in
// force truncates the distribution at itself: nothing here separates a
// tool that fits under the cap from one being cut down to it. ToolUse.Cut
// is the measurement that survives that, and is meant to be read
// alongside these.
type Sizes struct {
	N         int
	Mean      int64
	Max       int64
	P50AtMost int64
	P95AtMost int64
	P99AtMost int64
}

// ToolUse is one tool's numbers across the window.
type ToolUse struct {
	Name string
	// Runs is how many of the window's runs called this tool at all --
	// the difference between a tool every run uses twice and one that a
	// single run used two hundred times.
	Runs    int
	Calls   int
	Errored int
	// ErrorRate is Errored/Calls. An errored call is an ordinary turn of
	// an agent's loop rather than a broken run (orchestrator.outcomeOf
	// says why at length), so this is never zero for a healthy
	// deployment: what it is for is the trend and the comparison between
	// tools. edit_file's is the "String not found" loop measured;
	// run_command's is the sandbox's own health.
	ErrorRate float64
	// TimedOut is how many calls the tool's own bound cut off rather than
	// the work finishing, and TimeoutRate is that over Calls. Only
	// run_command reports this today (mcp.RunCommandTimedOut); it is zero
	// for every other tool because none of them can say, not because none
	// of them ever ran long.
	TimedOut    int
	TimeoutRate float64
	// ResultBytes is how much text this tool's answers carried back --
	// the run's own context, spent.
	ResultBytes Sizes
	// Cut is how often grain shortened this tool's answers, and by how
	// much. Read it *with* ResultBytes rather than after it: every result
	// cap censors the distribution beside it (Cuts says how).
	Cut Cuts
}

// Cuts is how often grain elided a tool's answer, and how much it took
// out when it did.
//
// It exists because ResultBytes stops being able to answer the question
// it was added for. mcp.maxToolResultBytes and the backstop over it cap
// every answer, and the size recorded for a call is the size after the
// cap, so once a cap is in force the distribution is censored at it:
// "p95 <= 16383" is then equally true of a tool whose answers comfortably
// fit and a tool whose answers are all being cut down to fit, and the
// argument that last sized the cap from those percentiles
// (grain/task-183) could not be made a second time from the same numbers.
//
// Calls and Rate are what say whether a cap fires at all -- a cap nothing
// reaches is one that could come down, and a tool cut on most of its calls
// is one whose bound is costing the run something on every turn.
// DroppedP50AtMost and DroppedP95AtMost say how much: a median cut of a
// few hundred bytes is a cap sitting just under a tool's ordinary answer,
// and a median cut of megabytes is the cap doing exactly its job. Both are
// bucket bounds for Sizes' reason, over one sample per cut call, and they
// are drawn from what each notice *said* it dropped -- rounded to two
// figures on the way out (mcp.humanBytes), which is far inside the octave
// a bucket is wide.
//
// Everything here is zero for a window whose runs finished before any of
// it was recorded, exactly like TimedOut: a run that counted no cuts and
// a run that could not count them are both nothing, which is why Tools.Runs
// is reported beside every rate.
type Cuts struct {
	Calls int
	Rate  float64
	// N is how many of those cuts said a size this build could read, and
	// is what the two percentiles are over. It is Calls for every cut a
	// current build records; the gap is old rows and unreadable notices.
	N                int
	DroppedP50AtMost int64
	DroppedP95AtMost int64
}

// Tools is the whole census over the window.
type Tools struct {
	// Runs is how many finished runs in the window recorded a census at
	// all, and is the first thing to read: every rate below is over these
	// runs, not over Throughput.RunsFinished. A run that never reached
	// its agent, one that made no tool calls, and one that finished
	// before these rows were recorded at all each contribute nothing.
	Runs    int
	Calls   int
	Errored int
	// ErroredShare is Errored/Calls across every tool. A handful of
	// errors is the ordinary shape of agentic work; a deployment drifting
	// upward is a deployment whose tools got worse.
	ErroredShare float64
	// Cut is how many calls came back with grain's own elision in them,
	// and CutShare is that over Calls -- the whole deployment's answer to
	// "is the result cap firing, and how often?", which no per-tool byte
	// distribution can give once the cap is what bounds it.
	Cut      int
	CutShare float64
	// CallsPerRun is the distribution of how many calls a run makes.
	CallsPerRun Counts
	// ByTool is every tool the window's runs called, busiest first --
	// which is the order that matters, since a rate over four calls is
	// not a rate.
	ByTool []ToolUse
}

// Checks is the CI loop, measured end to end: BuildPrompt tells every run
// to push, call wait_for_checks, fix and push again, and nothing showed
// how that loop actually goes.
type Checks struct {
	// Waits is how many wait_for_checks calls the window's runs made, and
	// Runs is how many runs made at least one.
	Waits int
	Runs  int
	// Verdicts counts each of the four endings a wait can have
	// (mcp.WaitVerdict*). A deployment where most waits end "timed_out"
	// has mcp.DefaultWaitForChecksTimeout set wrong for its CI, and one
	// where most end "no_checks" is telling every run to wait for CI that
	// does not exist -- neither is visible in any other number here.
	Verdicts map[string]int
	// Blocked is how long a wait blocked for. Read against the verdicts:
	// waits that blocked a long time and then passed are CI being slow,
	// and waits that blocked the full timeout are the timeout being
	// wrong.
	Blocked Distribution
	// PushesToGreen is how many pushes a run had made by the time its
	// first passing wait returned, over the runs that got a pass at all
	// (GreenRuns). 1.0 is a deployment whose runs get CI right first
	// time; the distance above it is the rework the CI loop costs.
	PushesToGreen Counts
	GreenRuns     int
	// Repairs tracks duration and telemetry for automated branch repair runs.
	Repairs BranchRepairs
	// RedCI is how the window's CI-fixing runs worked a red build.
	RedCI RedCI
}

// RedCI measures the red-CI procedure every run with grain CI is given
// (orchestrator's redCIProcedureSection) by what runs did, from the rows
// model.RunCIHabit records.
//
// FixingRuns is the runs that fixed red CI: every repair run (Repairs),
// red from its first turn, and every other run that pushed after one of
// its own waits came back failed. Narrowed is how many of those ran a
// narrowed local test before the push that fixed it -- for a repair
// run, before its first push at all -- and Selective how many asked CI
// for less than the whole suite (run_ci_job, retrigger_ci, or
// wait_for_checks with `jobs`). The runs per repair pass the procedure
// should bring down are Repairs' own Runs over Passes.
type RedCI struct {
	FixingRuns     int
	Narrowed       int
	NarrowedShare  float64
	Selective      int
	SelectiveShare float64
}

// redCIOf is RedCI over the runs that finished inside the window.
func redCIOf(w Window, tasks []model.TaskTiming, runs []model.RunTiming, habits []model.RunCIHabit) RedCI {
	finished := finishedInWindow(w, runs)
	repairs := repairRunsOf(w, tasks, runs)
	byRun := make(map[string]model.RunCIHabit, len(habits))
	for _, h := range habits {
		byRun[h.RunID] = h
	}
	var out RedCI
	count := func(h model.RunCIHabit, narrowed bool) {
		out.FixingRuns++
		if narrowed {
			out.Narrowed++
		}
		if h.Selective {
			out.Selective++
		}
	}
	// Every repair run counts whether or not it recorded a row: one that
	// did none of the procedure is the case being measured.
	for runID := range repairs {
		h := byRun[runID]
		count(h, h.NarrowTestBeforePush || h.NarrowTestBeforeFixPush)
	}
	for runID, h := range byRun {
		if repairs[runID] || !finished[runID] || !h.PushedAfterRed {
			continue
		}
		count(h, h.NarrowTestBeforeFixPush)
	}
	if out.FixingRuns > 0 {
		out.NarrowedShare = float64(out.Narrowed) / float64(out.FixingRuns)
		out.SelectiveShare = float64(out.Selective) / float64(out.FixingRuns)
	}
	return out
}

// toolsOf and checksOf turn the stored census into the two sections
// above, over the runs that finished inside the window.
func toolsOf(w Window, runs []model.RunTiming, uses []model.RunToolUse) Tools {
	finished := finishedInWindow(w, runs)
	out := Tools{}
	callsPerRun := map[string]int64{}

	type series struct {
		use     ToolUse
		bytes   int64
		sizes   model.SizeHistogram
		dropped model.SizeHistogram
	}
	var names []string
	byName := map[string]*series{}

	for _, use := range uses {
		if !finished[use.RunID] {
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

		out.Calls += use.Calls
		out.Errored += use.Errored
		out.Cut += use.Elided
		callsPerRun[use.RunID] += int64(use.Calls)
	}

	out.Runs = len(callsPerRun)
	if out.Calls > 0 {
		out.ErroredShare = float64(out.Errored) / float64(out.Calls)
		out.CutShare = float64(out.Cut) / float64(out.Calls)
	}
	perRun := make([]int64, 0, len(callsPerRun))
	for _, calls := range callsPerRun {
		perRun = append(perRun, calls)
	}
	out.CallsPerRun = summarizeCounts(perRun)

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
		out.ByTool = append(out.ByTool, use)
	}
	// Busiest first, and by name where two tools tie, so the order is
	// stable across reports of the same window.
	sort.SliceStable(out.ByTool, func(i, j int) bool {
		if out.ByTool[i].Calls == out.ByTool[j].Calls {
			return out.ByTool[i].Name < out.ByTool[j].Name
		}
		return out.ByTool[i].Calls > out.ByTool[j].Calls
	})
	return out
}

func checksOf(w Window, runs []model.RunTiming, waits []model.RunCheckWait) Checks {
	finished := finishedInWindow(w, runs)
	out := Checks{Verdicts: map[string]int{}}
	var blocked []time.Duration
	seenRun := map[string]bool{}
	// The *first* pass in a run is the one that answers "how many pushes
	// did this take": a run that went green, pushed again and went green
	// again did not need the second round to get there. Which wait that is
	// is decided by the run's own sequence number rather than by the order
	// these rows arrived in, the same reason runsByTask sorts what the
	// store already returns sorted -- a caller building its input by hand
	// gets the same answer as one reading Store.RunCheckWaits.
	type green struct{ seq, pushes int }
	greenRun := map[string]green{}

	for _, wait := range waits {
		if !finished[wait.RunID] {
			continue
		}
		out.Waits++
		out.Verdicts[wait.Verdict]++
		if !seenRun[wait.RunID] {
			seenRun[wait.RunID] = true
			out.Runs++
		}
		// A wait whose own header could not be read stored a zero, and a
		// zero-second wait never happened: appendIfPositive drops it
		// rather than reporting a wait that took no time.
		blocked = appendIfPositive(blocked, wait.Waited)
		if wait.Verdict != waitVerdictPassed {
			continue
		}
		if first, ok := greenRun[wait.RunID]; !ok || wait.Seq < first.seq {
			greenRun[wait.RunID] = green{seq: wait.Seq, pushes: wait.PushesBefore}
		}
	}

	pushes := make([]int64, 0, len(greenRun))
	for _, first := range greenRun {
		pushes = append(pushes, int64(first.pushes))
	}

	out.Blocked = summarize(blocked)
	out.PushesToGreen = summarizeCounts(pushes)
	out.GreenRuns = len(greenRun)
	return out
}

// waitVerdictPassed is mcp.WaitVerdictPassed, spelled out rather than
// imported: this package reads rows, and a metrics report has no business
// depending on the package that serves an agent's tools. The store's own
// column is the contract between them, and a verdict this build does not
// know still counts under its own name in Verdicts.
const waitVerdictPassed = "passed"

// finishedInWindow is the set of runs whose ending falls inside the
// window -- the same rule every other measurement here follows, applied
// to rows that have no moment of their own to be judged by.
func finishedInWindow(w Window, runs []model.RunTiming) map[string]bool {
	out := make(map[string]bool)
	for _, r := range runs {
		if r.RunID != "" && w.holds(r.FinishedAt) {
			out[r.RunID] = true
		}
	}
	return out
}

// sizeQuantile is the p'th sample's own bucket bound: the smallest size
// bucket whose cumulative count reaches the p'th sample, reported as the
// largest result that bucket can hold. Nearest-rank, like percentile
// above, and an upper bound rather than a value -- see Sizes.
func sizeQuantile(h model.SizeHistogram, p float64) int64 {
	total := h.Total()
	if total == 0 {
		return 0
	}
	rank := int(math.Ceil(p * float64(total)))
	if rank < 1 {
		rank = 1
	}
	seen := 0
	buckets := h.Buckets()
	for _, bucket := range buckets {
		seen += h[bucket]
		if seen >= rank {
			return model.SizeBucketMax(bucket)
		}
	}
	return model.SizeBucketMax(buckets[len(buckets)-1])
}

// summarizeCounts is summarize for whole numbers. It sorts a copy, like
// its duration twin, and reports N first: a p99 over four samples is the
// maximum wearing a percentile's name here too.
func summarizeCounts(samples []int64) Counts {
	c := Counts{N: len(samples)}
	if c.N == 0 {
		return c
	}
	sorted := make([]int64, len(samples))
	copy(sorted, samples)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	for _, s := range sorted {
		c.Total += s
	}
	c.Min, c.Max = sorted[0], sorted[len(sorted)-1]
	c.P50, c.P90, c.P99 = countPercentile(sorted, 0.50), countPercentile(sorted, 0.90), countPercentile(sorted, 0.99)
	c.Mean = float64(c.Total) / float64(c.N)
	return c
}

func countPercentile(sorted []int64, p float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(math.Ceil(p*float64(len(sorted)))) - 1
	if i < 0 {
		i = 0
	}
	if i >= len(sorted) {
		i = len(sorted) - 1
	}
	return sorted[i]
}
