// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package metrics_test

// The inside of a run, over a window: the tool census, the endings that
// share an outcome word, and the CI loop.

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/metrics"
	"github.com/ai-on-gke/grain/pkg/model"
)

// finishedRun is one attempt that ended at d into the window, with the
// outcome and detail the store would have recorded for it.
func finishedRun(id string, d time.Duration, outcome, detail string) model.RunTiming {
	return model.RunTiming{
		RunID: id, TaskID: id, Attempt: 1,
		StartedAt: since.Add(d - time.Hour), FinishedAt: at(d),
		Outcome: outcome, Detail: detail,
	}
}

func sizes(ns ...int64) model.SizeHistogram {
	h := model.SizeHistogram{}
	for _, n := range ns {
		h.Add(n)
	}
	return h
}

func TestToolsCountsCallsErrorsAndRatesPerTool(t *testing.T) {
	rep := metrics.Compute(metrics.Input{
		Window: win,
		Runs: []model.RunTiming{
			finishedRun("r1", 10*time.Hour, "succeeded", ""),
			finishedRun("r2", 20*time.Hour, "succeeded", ""),
			// Finished after the window: its census is in the store and
			// out of the report, the same rule every other measurement
			// here follows.
			finishedRun("r3", 9*24*time.Hour, "succeeded", ""),
		},
		ToolUses: []model.RunToolUse{
			{RunID: "r1", Tool: "run_command", Calls: 40, Errored: 4, TimedOut: 2,
				ResultBytes: 4000, MaxResultBytes: 900, Sizes: sizes(100, 900)},
			{RunID: "r1", Tool: "edit_file", Calls: 10, Errored: 5,
				ResultBytes: 100, MaxResultBytes: 20, Sizes: sizes(20, 20)},
			{RunID: "r2", Tool: "run_command", Calls: 60, Errored: 6,
				ResultBytes: 6000, MaxResultBytes: 70000, Sizes: sizes(70000)},
			{RunID: "r3", Tool: "run_command", Calls: 500, Errored: 500,
				ResultBytes: 1, MaxResultBytes: 1, Sizes: sizes(1)},
		},
	})

	tools := rep.Tools
	if tools.Runs != 2 {
		t.Errorf("Tools.Runs = %d, want 2 -- only the runs that finished in the window", tools.Runs)
	}
	if tools.Calls != 110 || tools.Errored != 15 {
		t.Errorf("Tools = %d calls, %d errored; want 110 and 15", tools.Calls, tools.Errored)
	}
	if got, want := tools.ErroredShare, 15.0/110.0; !closeEnough(got, want) {
		t.Errorf("ErroredShare = %v, want %v", got, want)
	}
	if got := tools.CallsPerRun; got.N != 2 || got.Min != 50 || got.Max != 60 || got.Total != 110 {
		t.Errorf("CallsPerRun = %+v, want two runs of 50 and 60 calls", got)
	}

	if len(tools.ByTool) != 2 {
		t.Fatalf("ByTool = %+v, want two tools", tools.ByTool)
	}
	// Busiest first: 100 run_command calls against 10 edit_file ones.
	cmd, edits := tools.ByTool[0], tools.ByTool[1]
	if cmd.Name != "run_command" || edits.Name != "edit_file" {
		t.Fatalf("ByTool order = %q then %q, want the busiest tool first", cmd.Name, edits.Name)
	}
	if cmd.Runs != 2 || cmd.Calls != 100 || cmd.Errored != 10 {
		t.Errorf("run_command = %+v", cmd)
	}
	if got, want := cmd.ErrorRate, 0.10; !closeEnough(got, want) {
		t.Errorf("run_command ErrorRate = %v, want %v", got, want)
	}
	if got, want := cmd.TimeoutRate, 0.02; !closeEnough(got, want) {
		t.Errorf("run_command TimeoutRate = %v, want %v", got, want)
	}
	// edit_file's error rate is the "String not found" loop measured, and
	// the point of reporting per tool: half its calls, against a tenth of
	// run_command's.
	if got, want := edits.ErrorRate, 0.50; !closeEnough(got, want) {
		t.Errorf("edit_file ErrorRate = %v, want %v", got, want)
	}
}

// Result sizes: the max is exact, and the percentiles are the bound of
// the bucket the sample fell in -- named AtMost because that is what they
// are.
func TestToolResultSizesReportBoundsRatherThanGuesses(t *testing.T) {
	rep := metrics.Compute(metrics.Input{
		Window: win,
		Runs:   []model.RunTiming{finishedRun("r1", 10*time.Hour, "succeeded", "")},
		ToolUses: []model.RunToolUse{{
			RunID: "r1", Tool: "read_file", Calls: 4,
			ResultBytes: 100 + 200 + 300 + 70000, MaxResultBytes: 70000,
			Sizes: sizes(100, 200, 300, 70000),
		}},
	})
	got := rep.Tools.ByTool[0].ResultBytes
	if got.N != 4 {
		t.Fatalf("Sizes = %+v, want four samples", got)
	}
	if got.Max != 70000 {
		t.Errorf("Max = %d, want the exact 70000", got.Max)
	}
	if want := int64((100 + 200 + 300 + 70000) / 4); got.Mean != want {
		t.Errorf("Mean = %d, want %d", got.Mean, want)
	}
	// 200 lives in the [128, 256) bucket, whose bound is 255.
	if got.P50AtMost != 255 {
		t.Errorf("P50AtMost = %d, want 255 -- the bound of the bucket the median fell in", got.P50AtMost)
	}
	// The 95th of four samples is the largest: [65536, 131072), bound
	// 131071. This is the number that would size a truncation cap.
	if got.P95AtMost != 131071 {
		t.Errorf("P95AtMost = %d, want 131071", got.P95AtMost)
	}
	// The bound has to actually hold for the sample it describes, or "at
	// most" is a lie rather than a rounding.
	if got.P50AtMost < 200 {
		t.Errorf("P50AtMost = %d is below the median sample of 200", got.P50AtMost)
	}
}

// Cuts, which are the half of the census that survives the cap sizes are
// recorded under: every answer here is at or under 16 KB, and only these
// numbers say whether that is what the tools produce or what grain left
// of what they produced.
func TestToolCutsCountTheAnswersGrainShortened(t *testing.T) {
	rep := metrics.Compute(metrics.Input{
		Window: win,
		Runs: []model.RunTiming{
			finishedRun("r1", 10*time.Hour, "succeeded", ""),
			finishedRun("r2", 20*time.Hour, "succeeded", ""),
		},
		ToolUses: []model.RunToolUse{
			{RunID: "r1", Tool: "read_file", Calls: 6, Elided: 2,
				ResultBytes: 60000, MaxResultBytes: 16000, Sizes: sizes(16000),
				Dropped: sizes(3000, 40000)},
			{RunID: "r2", Tool: "read_file", Calls: 4, Elided: 1,
				ResultBytes: 40000, MaxResultBytes: 16000, Sizes: sizes(16000),
				Dropped: sizes(5000)},
			// A tool nothing was ever cut on: zero cuts and no
			// distribution, rather than a bound over no samples.
			{RunID: "r1", Tool: "edit_file", Calls: 20,
				ResultBytes: 400, MaxResultBytes: 40, Sizes: sizes(40)},
		},
	})

	if rep.Tools.Cut != 3 {
		t.Errorf("Tools.Cut = %d, want the 3 answers grain shortened", rep.Tools.Cut)
	}
	if got, want := rep.Tools.CutShare, 3.0/30.0; !closeEnough(got, want) {
		t.Errorf("CutShare = %v, want %v", got, want)
	}

	byTool := map[string]metrics.ToolUse{}
	for _, use := range rep.Tools.ByTool {
		byTool[use.Name] = use
	}
	reads := byTool["read_file"].Cut
	if reads.Calls != 3 || reads.N != 3 {
		t.Fatalf("read_file cuts = %+v, want 3 cuts with a size on each", reads)
	}
	if got, want := reads.Rate, 3.0/10.0; !closeEnough(got, want) {
		t.Errorf("read_file cut Rate = %v, want %v", got, want)
	}
	// 5000 is the median of 3000, 5000 and 40000, and lives in the
	// [4096, 8192) bucket -- a bound, like every percentile drawn from
	// these histograms.
	if reads.DroppedP50AtMost != 8191 {
		t.Errorf("DroppedP50AtMost = %d, want 8191 -- the bucket the median cut fell in",
			reads.DroppedP50AtMost)
	}
	if reads.DroppedP95AtMost != 65535 {
		t.Errorf("DroppedP95AtMost = %d, want 65535", reads.DroppedP95AtMost)
	}
	if edits := byTool["edit_file"].Cut; edits.Calls != 0 || edits.N != 0 ||
		edits.DroppedP50AtMost != 0 {
		t.Errorf("edit_file cuts = %+v, want nothing cut and no bound over no samples", edits)
	}
}

// A deployment with no census at all -- every run predating these rows --
// reports nothing rather than a report full of zeroed rates.
func TestToolsIsEmptyWithoutACensus(t *testing.T) {
	rep := metrics.Compute(metrics.Input{
		Window: win,
		Runs:   []model.RunTiming{finishedRun("r1", 10*time.Hour, "succeeded", "")},
	})
	if rep.Tools.Runs != 0 || len(rep.Tools.ByTool) != 0 || rep.Tools.Calls != 0 {
		t.Errorf("Tools = %+v, want nothing measured", rep.Tools)
	}
	if rep.Checks.Waits != 0 || rep.Checks.Blocked.N != 0 {
		t.Errorf("Checks = %+v, want nothing measured", rep.Checks)
	}
}

// The endings that share an outcome word, split. Runs.Outcomes still
// counts the words themselves, so both readings are available.
func TestEndingsSplitTheOutcomesThatShareAWord(t *testing.T) {
	rep := metrics.Compute(metrics.Input{
		Window: win,
		Runs: []model.RunTiming{
			finishedRun("r1", 1*time.Hour, "cancelled", model.RuntimeCapDetail(2*time.Hour)),
			finishedRun("r2", 2*time.Hour, "cancelled", model.TaskClosedDetail),
			finishedRun("r3", 3*time.Hour, "failed",
				"claude: exceeded max turns (100) without a final answer"),
			finishedRun("r4", 4*time.Hour, "failed", "the sandbox stopped answering"),
			finishedRun("r5", 5*time.Hour, "no_action",
				"the run made 3 tool call(s), and finished without pushing a branch"),
			finishedRun("r6", 6*time.Hour, model.PausedOutcome, "the agent's usage limit was reached"),
			finishedRun("r7", 7*time.Hour, "succeeded", "the run made 40 tool call(s)"),
			finishedRun("r8", 8*time.Hour, model.NothingNeededOutcome, "the agent found nothing needed"),
		},
	})

	for ending, want := range map[model.RunEnding]int{
		model.EndingRuntimeCap:     1,
		model.EndingTaskClosed:     1,
		model.EndingTurnsExhausted: 1,
		model.EndingFailed:         1,
		model.EndingNoAction:       1,
		model.EndingUsageLimit:     1,
		model.EndingSucceeded:      1,
		// Its own series, apart from both succeeded and no_action.
		model.EndingNothingNeeded: 1,
	} {
		if got := rep.Runs.Endings[ending]; got != want {
			t.Errorf("Endings[%q] = %d, want %d (all: %v)", ending, got, want, rep.Runs.Endings)
		}
	}
	// The outcome strings are still counted verbatim: "cancelled" covers
	// two of the runs above, which is exactly the conflation Endings
	// exists to see through.
	if got := rep.Runs.Outcomes["cancelled"]; got != 2 {
		t.Errorf("Outcomes[cancelled] = %d, want 2", got)
	}
}

func TestChecksMeasuresTheCILoop(t *testing.T) {
	rep := metrics.Compute(metrics.Input{
		Window: win,
		Runs: []model.RunTiming{
			finishedRun("r1", 10*time.Hour, "succeeded", ""),
			finishedRun("r2", 20*time.Hour, "succeeded", ""),
			finishedRun("r3", 9*24*time.Hour, "succeeded", ""),
		},
		CheckWaits: []model.RunCheckWait{
			// A run that failed CI once and went green on its second push.
			{RunID: "r1", Seq: 0, Verdict: "failed", Waited: 4 * time.Minute, PushesBefore: 1},
			{RunID: "r1", Seq: 1, Verdict: "passed", Waited: 9 * time.Minute, PushesBefore: 2},
			// A run whose waits kept timing out and never saw a verdict.
			{RunID: "r2", Seq: 0, Verdict: "timed_out", Waited: 15 * time.Minute, PushesBefore: 1},
			{RunID: "r2", Seq: 1, Verdict: "timed_out", Waited: 15 * time.Minute, PushesBefore: 1},
			// Outside the window with its run.
			{RunID: "r3", Seq: 0, Verdict: "passed", Waited: time.Minute, PushesBefore: 9},
		},
	})

	ci := rep.Checks
	if ci.Waits != 4 || ci.Runs != 2 {
		t.Errorf("Checks = %d waits over %d runs; want 4 over 2", ci.Waits, ci.Runs)
	}
	if ci.Verdicts["timed_out"] != 2 || ci.Verdicts["failed"] != 1 || ci.Verdicts["passed"] != 1 {
		t.Errorf("Verdicts = %v", ci.Verdicts)
	}
	if ci.Blocked.N != 4 || ci.Blocked.Max != 15*time.Minute {
		t.Errorf("Blocked = %+v, want four waits topping out at the timeout", ci.Blocked)
	}
	if ci.GreenRuns != 1 || ci.PushesToGreen.N != 1 || ci.PushesToGreen.Max != 2 {
		t.Errorf("PushesToGreen = %+v over %d green runs; want the one run that went green on its second push",
			ci.PushesToGreen, ci.GreenRuns)
	}
}

// Only the first pass in a run counts toward "how many pushes did this
// take": a run that went green, pushed again and went green again did not
// need the second round to get there.
func TestPushesToGreenCountsTheFirstPassOnly(t *testing.T) {
	rep := metrics.Compute(metrics.Input{
		Window: win,
		Runs:   []model.RunTiming{finishedRun("r1", 10*time.Hour, "succeeded", "")},
		CheckWaits: []model.RunCheckWait{
			{RunID: "r1", Seq: 0, Verdict: "passed", Waited: time.Minute, PushesBefore: 1},
			{RunID: "r1", Seq: 1, Verdict: "passed", Waited: time.Minute, PushesBefore: 4},
		},
	})
	if got := rep.Checks.PushesToGreen; got.N != 1 || got.Max != 1 {
		t.Errorf("PushesToGreen = %+v, want the one sample of 1", got)
	}
}

// Which pass is the first one is the run's own sequence number, not the
// order the rows arrived in. Store.RunCheckWaits orders by seq, so this is
// about every other caller -- a test, a CLI reading a report over REST --
// getting the same answer from the same facts.
func TestPushesToGreenReadsTheSequenceRatherThanTheInputOrder(t *testing.T) {
	rep := metrics.Compute(metrics.Input{
		Window: win,
		Runs:   []model.RunTiming{finishedRun("r1", 10*time.Hour, "succeeded", "")},
		CheckWaits: []model.RunCheckWait{
			{RunID: "r1", Seq: 1, Verdict: "passed", Waited: time.Minute, PushesBefore: 4},
			{RunID: "r1", Seq: 0, Verdict: "passed", Waited: time.Minute, PushesBefore: 1},
		},
	})
	if got := rep.Checks.PushesToGreen; got.N != 1 || got.Max != 1 {
		t.Errorf("PushesToGreen = %+v, want the one sample of 1 -- seq 0 passed first", got)
	}
}

// A wait whose own duration could not be read stored a zero, and a
// zero-second wait never happened: it counts as a wait and a verdict, and
// contributes no sample to the blocked distribution.
func TestChecksSkipsAWaitWithNoDurationRatherThanCountingZero(t *testing.T) {
	rep := metrics.Compute(metrics.Input{
		Window: win,
		Runs:   []model.RunTiming{finishedRun("r1", 10*time.Hour, "succeeded", "")},
		CheckWaits: []model.RunCheckWait{
			{RunID: "r1", Seq: 0, Verdict: "passed", Waited: 0, PushesBefore: 1},
			{RunID: "r1", Seq: 1, Verdict: "failed", Waited: 2 * time.Minute, PushesBefore: 1},
		},
	})
	if rep.Checks.Waits != 2 {
		t.Errorf("Waits = %d, want both of them", rep.Checks.Waits)
	}
	if got := rep.Checks.Blocked; got.N != 1 || got.P50 != 2*time.Minute {
		t.Errorf("Blocked = %+v, want the one wait that recorded a duration", got)
	}
}

// The red-CI procedure, measured over the runs that fixed red CI: a
// repair run counts whether or not it recorded anything, an ordinary run
// only once it pushed after a red wait, and a run outside the window not
// at all.
func TestRedCIMeasuresHowCIFixingRunsWorked(t *testing.T) {
	repair := model.TaskTiming{TaskID: "fix1", FixTaskFiled: true}
	idle := model.TaskTiming{TaskID: "fix2", FixTaskFiled: true}
	rep := metrics.Compute(metrics.Input{
		Window: win,
		Tasks:  []model.TaskTiming{repair, idle},
		Runs: []model.RunTiming{
			finishedRun("fix1", 10*time.Hour, "succeeded", ""),
			finishedRun("fix2", 11*time.Hour, "succeeded", ""),
			finishedRun("r1", 12*time.Hour, "succeeded", ""),
			finishedRun("r2", 13*time.Hour, "succeeded", ""),
			finishedRun("r3", 9*24*time.Hour, "succeeded", ""),
		},
		CIHabits: []model.RunCIHabit{
			// A repair that reproduced before its first push.
			{RunID: "fix1", NarrowTestBeforePush: true, Selective: true},
			// An ordinary run that pushed a fix after a red, narrowly.
			{RunID: "r1", NarrowTestBeforeFixPush: true, PushedAfterRed: true},
			// One that only re-ran selectively and never fixed a red.
			{RunID: "r2", Selective: true},
			// Outside the window with its run.
			{RunID: "r3", NarrowTestBeforeFixPush: true, PushedAfterRed: true},
		},
	})
	got := rep.Checks.RedCI
	want := metrics.RedCI{FixingRuns: 3, Narrowed: 2, NarrowedShare: 2.0 / 3, Selective: 1, SelectiveShare: 1.0 / 3}
	if got != want {
		t.Errorf("RedCI = %+v, want %+v", got, want)
	}
}
