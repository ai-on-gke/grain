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

// Merges: what a deployment landed, how long each change took to land,
// and what its merge queue's batching cost. The cases here are the ones
// the store can really produce -- a task completed but not yet merged, a
// pull request closed without merging, a batch that failed and halved,
// a history that does not reach back as far as the window asks.

import (
	"context"
	"testing"
	"time"

	changepkg "github.com/ai-on-gke/grain/pkg/change"
	"github.com/ai-on-gke/grain/pkg/metrics"
	"github.com/ai-on-gke/grain/pkg/model"
)

// batch is one finished batch of `size` members that ended `end` into the
// window with `outcome`, `splits` halvings deep.
func batch(size, splits, merged int, outcome string, end time.Duration) model.MergeBatchRecord {
	return model.MergeBatchRecord{
		Repo: model.RepoRef{Owner: "acme", Name: "widgets"}, Branch: "main",
		Size: size, Splits: splits, Merged: merged, Outcome: outcome,
		Queued:     size,
		PostMerge:  true,
		StartedAt:  since.Add(end - 20*time.Minute),
		FinishedAt: since.Add(end),
	}
}

// landed is a task filed, completed and merged at the given offsets into
// the window -- the shape of every task whose work actually shipped.
func landed(id string, filed, completed, opened, merged time.Duration) model.TaskTiming {
	return model.TaskTiming{
		TaskID: id, Repo: "acme/widgets", Author: "ada",
		CreatedAt: at(filed), ApprovedAt: at(filed), CompletedAt: at(completed),
		PrOpenedAt: at(opened), PrMergedAt: at(merged),
	}
}

func TestMergesCountWhatLandedAndAtWhatRate(t *testing.T) {
	rep := metrics.Compute(metrics.Input{
		Window: win,
		Tasks: []model.TaskTiming{
			landed("1", time.Hour, 5*time.Hour, 5*time.Hour, 6*time.Hour),
			landed("2", 20*time.Hour, 26*time.Hour, 26*time.Hour, 30*time.Hour),
			// Completed inside the window and not merged: throughput has
			// it, this does not, because nothing has landed yet.
			{TaskID: "3", CreatedAt: at(time.Hour), CompletedAt: at(40 * time.Hour)},
			// The pull request closed without merging, which is the case a
			// count of "finished work" gets wrong: it is not a landing.
			{TaskID: "4", CreatedAt: at(time.Hour), CompletedAt: at(41 * time.Hour),
				PrOpenedAt: at(41 * time.Hour)},
			// Merged after the window ends: in the store, out of the report.
			landed("5", time.Hour, 2*time.Hour, 2*time.Hour, 8*24*time.Hour),
		},
	})

	m := rep.Merges
	if m.Landed != 2 {
		t.Errorf("Landed = %d, want the 2 whose pull request merged inside the window", m.Landed)
	}
	// Two over seven days.
	if got, want := m.LandedPerHour, 2.0/(7*24); !closeEnough(got, want) {
		t.Errorf("LandedPerHour = %v, want %v", got, want)
	}
	if got, want := m.LandedPerDay, 2.0/7; !closeEnough(got, want) {
		t.Errorf("LandedPerDay = %v, want %v", got, want)
	}
	// The whole wait, and the two stretches inside it. Task 1 waited five
	// hours from filing and one from its agent stopping; task 2 ten and
	// four.
	if got := m.FiledToLanded; got.N != 2 || got.Max != 10*time.Hour {
		t.Errorf("FiledToLanded = %+v, want two samples topping out at 10h", got)
	}
	if got := m.CompletedToLanded; got.N != 2 || got.Max != 4*time.Hour {
		t.Errorf("CompletedToLanded = %+v, want two samples topping out at 4h", got)
	}
	if got := m.OpenedToLanded; got.N != 2 || got.Max != 4*time.Hour {
		t.Errorf("OpenedToLanded = %+v, want two samples topping out at 4h", got)
	}
}

// A moment that was never recorded is skipped rather than guessed, so the
// three lead times legitimately have different sample counts -- a task
// whose pull request grain never saw open still landed.
func TestMergesSkipTheMomentsATaskNeverRecorded(t *testing.T) {
	rep := metrics.Compute(metrics.Input{
		Window: win,
		Tasks: []model.TaskTiming{
			landed("1", time.Hour, 5*time.Hour, 5*time.Hour, 6*time.Hour),
			// Merged, but filed before created_at existed and with no
			// opening ever observed: it counts as a landing and in neither
			// of those two distributions.
			{TaskID: "2", CompletedAt: at(20 * time.Hour), PrMergedAt: at(24 * time.Hour)},
		},
	})
	m := rep.Merges
	if m.Landed != 2 {
		t.Errorf("Landed = %d, want both of them", m.Landed)
	}
	if m.FiledToLanded.N != 1 || m.OpenedToLanded.N != 1 || m.CompletedToLanded.N != 2 {
		t.Errorf("filed/opened/completed samples = %d/%d/%d, want 1/1/2",
			m.FiledToLanded.N, m.OpenedToLanded.N, m.CompletedToLanded.N)
	}
}

// The landing rate is bucketed onto the window's own slices, which is
// what lets it be read beside every other series in the report.
func TestMergesBucketTheLandingRateAcrossTheWindow(t *testing.T) {
	rep := metrics.Compute(metrics.Input{
		Window: win,
		Tasks: []model.TaskTiming{
			landed("1", 0, time.Hour, time.Hour, 2*time.Hour),
			landed("2", 0, time.Hour, time.Hour, 3*time.Hour),
			landed("3", 0, 100*time.Hour, 100*time.Hour, 101*time.Hour),
		},
	})
	m := rep.Merges
	if len(m.Buckets) != metrics.DefaultBuckets {
		t.Fatalf("got %d buckets, want the default %d", len(m.Buckets), metrics.DefaultBuckets)
	}
	// The slices are the same slices, point for point: a tuning agent
	// reads this line against the CI and host lines beside it.
	for i, b := range m.Buckets {
		if !b.Since.Equal(rep.Throughput.Buckets[i].Since) || !b.Until.Equal(rep.Throughput.Buckets[i].Until) {
			t.Fatalf("bucket %d spans %s->%s, want throughput's own %s->%s",
				i, b.Since, b.Until, rep.Throughput.Buckets[i].Since, rep.Throughput.Buckets[i].Until)
		}
	}
	var total int
	for _, b := range m.Buckets {
		total += b.Landed
	}
	if total != m.Landed {
		t.Errorf("buckets summed to %d, want the window's own %d", total, m.Landed)
	}
	if got, want := m.Buckets[0].Landed, 2; got != want {
		t.Errorf("first bucket landed %d, want %d", got, want)
	}
	// Per hour over the slice's own length, so the points compare to each
	// other: a seven-day window in twelve slices is fourteen hours each.
	if got, want := m.Buckets[0].PerHour, 2.0/14; !closeEnough(got, want) {
		t.Errorf("first bucket PerHour = %v, want %v", got, want)
	}
}

// The counters the batch size is actually decided from: fresh batches
// that passed first try against the ones that had to be bisected.
func TestMergeBatchesCountThePassRateTheSizeIsDecidedFrom(t *testing.T) {
	rep := metrics.Compute(metrics.Input{
		Window:         win,
		MergeBatchSize: 3,
		MergeBatches: []model.MergeBatchRecord{
			batch(3, 0, 3, model.MergeBatchMerged, 2*time.Hour),
			batch(3, 0, 3, model.MergeBatchMerged, 3*time.Hour),
			// One that failed, halved, and whose half then passed: two
			// rows, one bet, and only the fresh one counts as a first try.
			batch(3, 0, 0, model.MergeBatchSplit, 4*time.Hour),
			batch(2, 1, 2, model.MergeBatchMerged, 5*time.Hour),
			batch(1, 0, 0, model.MergeBatchAbandoned, 6*time.Hour),
		},
	})

	b := rep.Merges.Batches
	if b.Batches != 5 || b.Changes != 8 {
		t.Errorf("batches/changes = %d/%d, want 5 and 8", b.Batches, b.Changes)
	}
	if b.Fresh != 4 || b.FirstTry != 2 {
		t.Errorf("fresh/firstTry = %d/%d, want 4 and 2", b.Fresh, b.FirstTry)
	}
	if got, want := b.FirstTryRate, 0.5; !closeEnough(got, want) {
		t.Errorf("FirstTryRate = %v, want %v -- the halved batch's pass is the bisect working", got, want)
	}
	if got, want := b.PassRate, 3.0/5; !closeEnough(got, want) {
		t.Errorf("PassRate = %v, want %v", got, want)
	}
	if b.Split != 1 || b.Abandoned != 1 || b.Culprits != 0 {
		t.Errorf("split/abandoned/culprits = %d/%d/%d, want 1/1/0", b.Split, b.Abandoned, b.Culprits)
	}
	if got, want := b.SplitRate, 0.2; !closeEnough(got, want) {
		t.Errorf("SplitRate = %v, want %v", got, want)
	}
	// Three threes, a two and a one.
	if got, want := b.MeanSize, 12.0/5; !closeEnough(got, want) {
		t.Errorf("MeanSize = %v, want %v", got, want)
	}
	if b.Biggest != 3 || b.Limit != 3 {
		t.Errorf("biggest/limit = %d/%d, want 3 and 3", b.Biggest, b.Limit)
	}
	// The distribution behind that mean: a mean of 2.4 made of threes,
	// a two and a one is a different deployment from one made of twos.
	want := []metrics.MergeBatchSize{
		{Size: 1, Batches: 1, Passed: 0, Share: 0.2},
		{Size: 2, Batches: 1, Passed: 1, Share: 0.2},
		{Size: 3, Batches: 3, Passed: 2, Share: 0.6},
	}
	if len(b.Sizes) != len(want) {
		t.Fatalf("Sizes = %+v, want %+v", b.Sizes, want)
	}
	for i, got := range b.Sizes {
		if got.Size != want[i].Size || got.Batches != want[i].Batches ||
			got.Passed != want[i].Passed || !closeEnough(got.Share, want[i].Share) {
			t.Errorf("Sizes[%d] = %+v, want %+v", i, got, want[i])
		}
	}
}

// A batch belongs to the window when it finished inside it, on the same
// rule everything else in this package follows -- and the history it was
// read out of is bounded, which the section says rather than letting an
// empty window read as a quiet one.
func TestMergeBatchesWindowOnWhenTheyEndedAndSayHowFarBackTheHistoryGoes(t *testing.T) {
	rep := metrics.Compute(metrics.Input{
		Window:         win,
		MergeBatchSize: 3,
		MergeBatches: []model.MergeBatchRecord{
			batch(3, 0, 3, model.MergeBatchMerged, -30*time.Hour),
			batch(2, 0, 2, model.MergeBatchMerged, 10*time.Hour),
			batch(2, 0, 2, model.MergeBatchMerged, 9*24*time.Hour),
		},
	})
	b := rep.Merges.Batches
	if b.Batches != 1 {
		t.Errorf("Batches = %d, want the one that finished inside the window", b.Batches)
	}
	if b.Reaches == nil || !b.Reaches.Equal(since.Add(-30*time.Hour)) {
		t.Errorf("Reaches = %v, want the oldest record read (%v), window or no window",
			b.Reaches, since.Add(-30*time.Hour))
	}
}

// A window with no batches in it still says how far back the history
// reaches: "nothing was batched this week" and "nothing is recorded" are
// different answers, and only one of them is about this week.
func TestMergeBatchesReachBackEvenWhenTheWindowHoldsNone(t *testing.T) {
	rep := metrics.Compute(metrics.Input{
		Window:       win,
		MergeBatches: []model.MergeBatchRecord{batch(3, 0, 3, model.MergeBatchMerged, -100*time.Hour)},
	})
	b := rep.Merges.Batches
	if b.Batches != 0 || b.Reaches == nil {
		t.Errorf("batches = %d, reaches = %v; want no batches and a history that still says where it starts",
			b.Batches, b.Reaches)
	}
	// And a deployment that has never batched says neither.
	empty := metrics.Compute(metrics.Input{Window: win})
	if empty.Merges.Batches.Reaches != nil {
		t.Errorf("Reaches = %v on a deployment with no history at all, want nil", empty.Merges.Batches.Reaches)
	}
}

// The limit is only holding batches back where one took every member it
// allowed *and* there was more queued behind it. Without that, raising it
// would have changed nothing however well the batches did.
func TestMergeBatchesCountOnlyTheBatchesTheLimitActuallyHeldBack(t *testing.T) {
	full := batch(3, 0, 3, model.MergeBatchMerged, 2*time.Hour)
	full.Queued = 7
	short := batch(2, 0, 2, model.MergeBatchMerged, 3*time.Hour)
	short.Queued = 2
	rep := metrics.Compute(metrics.Input{
		Window: win, MergeBatchSize: 3,
		MergeBatches: []model.MergeBatchRecord{full, short},
	})
	if got := rep.Merges.Batches.AtLimit; got != 1 {
		t.Errorf("AtLimit = %d, want 1 -- only the full batch had more waiting behind it", got)
	}
}

// A scope narrows the landings by repo and author, as it narrows every
// other task measurement -- and narrows the batches by repo alone, since
// a batch records no members to attribute to anybody.
func TestAScopeNarrowsMergeBatchesByRepositoryOnly(t *testing.T) {
	theirs := batch(3, 0, 3, model.MergeBatchMerged, 4*time.Hour)
	theirs.Repo = model.RepoRef{Owner: "acme", Name: "docs"}
	tasks := []model.TaskTiming{
		landed("1", time.Hour, 2*time.Hour, 2*time.Hour, 3*time.Hour),
		func() model.TaskTiming {
			t := landed("2", time.Hour, 2*time.Hour, 2*time.Hour, 3*time.Hour)
			t.Author = "grace"
			return t
		}(),
	}
	batches := []model.MergeBatchRecord{batch(3, 0, 3, model.MergeBatchMerged, 2*time.Hour), theirs}

	byAuthor := metrics.Compute(metrics.Input{
		Window: win, MergeBatchSize: 3, Tasks: tasks, MergeBatches: batches,
		Scope: metrics.Scope{Authors: []string{"ada"}},
	}).Merges
	if byAuthor.Landed != 1 {
		t.Errorf("Landed = %d under an author scope, want ada's one landing", byAuthor.Landed)
	}
	if byAuthor.Batches.Batches != 2 {
		t.Errorf("Batches = %d under an author scope, want both -- a batch is not attributed to an author",
			byAuthor.Batches.Batches)
	}

	byRepo := metrics.Compute(metrics.Input{
		Window: win, MergeBatchSize: 3, Tasks: tasks, MergeBatches: batches,
		Scope: metrics.Scope{Repos: []string{"acme/widgets"}},
	}).Merges
	if byRepo.Batches.Batches != 1 {
		t.Errorf("Batches = %d under a repo scope, want the one in that repository", byRepo.Batches.Batches)
	}
}

func TestBranchRepairsTelemetryAndDuration(t *testing.T) {
	repTask1 := landed("1", time.Hour, 5*time.Hour, 5*time.Hour, 12*time.Hour)
	repTask1.MergeQueueRepairAt = at(10 * time.Hour)
	repTask1.MergeQueueRepairReason = "merge conflict on lib/foo.go"

	repTask2 := landed("2", time.Hour, 5*time.Hour, 5*time.Hour, 21*time.Hour)
	repTask2.MergeQueueRepairAt = at(20 * time.Hour)
	repTask2.MergeQueueRepairReason = "tests failed: build error in test"

	repTask3 := model.TaskTiming{
		TaskID: "3", CreatedAt: at(time.Hour), CompletedAt: at(31 * time.Hour),
		MergeQueueRepairAt:     at(30 * time.Hour),
		MergeQueueRepairReason: "ci failed",
	}

	nonRepTask := landed("4", time.Hour, 2*time.Hour, 2*time.Hour, 3*time.Hour)

	runs := []model.RunTiming{
		{RunID: "r1", TaskID: "1", Attempt: 2, StartedAt: since.Add(10 * time.Hour), FinishedAt: at(11 * time.Hour)},
		{RunID: "r2", TaskID: "2", Attempt: 2, StartedAt: since.Add(20 * time.Hour), FinishedAt: at(20*time.Hour + 30*time.Minute)},
		{RunID: "r3", TaskID: "4", Attempt: 1, StartedAt: since.Add(time.Hour), FinishedAt: at(2 * time.Hour)},
	}

	uses := []model.RunToolUse{
		{RunID: "r1", Tool: "edit_file", Calls: 5, Errored: 1, ResultBytes: 2000},
		{RunID: "r1", Tool: "run_command", Calls: 3, Errored: 0, ResultBytes: 1500},
		{RunID: "r2", Tool: "run_command", Calls: 4, Errored: 1, ResultBytes: 2500},
		{RunID: "r3", Tool: "read_file", Calls: 10, Errored: 0, ResultBytes: 5000},
	}

	transcripts := []model.RunTranscript{
		{RunID: "r1", Bytes: 15000},
		{RunID: "r2", Bytes: 12000},
		{RunID: "r3", Bytes: 5000},
	}

	rep := metrics.Compute(metrics.Input{
		Window:      win,
		Tasks:       []model.TaskTiming{repTask1, repTask2, repTask3, nonRepTask},
		Runs:        runs,
		ToolUses:    uses,
		Transcripts: transcripts,
	})

	r := rep.Merges.Repairs
	if r.Passes != 3 {
		t.Errorf("Passes = %d, want 3", r.Passes)
	}
	if r.Settled != 2 {
		t.Errorf("Settled = %d, want 2", r.Settled)
	}
	if got, want := r.SettleRate, 2.0/3.0; !closeEnough(got, want) {
		t.Errorf("SettleRate = %v, want %v", got, want)
	}
	if r.TimeToSettle.N != 3 {
		t.Errorf("TimeToSettle.N = %d, want 3", r.TimeToSettle.N)
	}
	if r.ConflictPasses != 1 || r.ConflictTimeToSettle.N != 1 || r.ConflictTimeToSettle.P50 != 2*time.Hour {
		t.Errorf("ConflictPasses = %d, TimeToSettle = %+v; want 1 pass at 2h", r.ConflictPasses, r.ConflictTimeToSettle)
	}
	if r.TestRepairPasses != 2 || r.TestRepairTimeToSettle.N != 2 || r.TestRepairTimeToSettle.P50 != 1*time.Hour {
		t.Errorf("TestRepairPasses = %d, TimeToSettle = %+v; want 2 passes at 1h", r.TestRepairPasses, r.TestRepairTimeToSettle)
	}
	if r.Runs != 2 {
		t.Errorf("Runs = %d, want 2", r.Runs)
	}
	if r.ToolCalls != 12 {
		t.Errorf("ToolCalls = %d, want 12 (5+3+4)", r.ToolCalls)
	}
	if r.ErroredToolCalls != 2 {
		t.Errorf("ErroredToolCalls = %d, want 2 (1+1)", r.ErroredToolCalls)
	}
	if got, want := r.ErroredToolCallRate, 2.0/12.0; !closeEnough(got, want) {
		t.Errorf("ErroredToolCallRate = %v, want %v", got, want)
	}
	if r.TranscriptBytes.N != 2 || r.TranscriptBytes.Mean != 13500 {
		t.Errorf("TranscriptBytes = %+v, want N=2, Mean=13500", r.TranscriptBytes)
	}
	if len(r.TopTools) != 2 {
		t.Fatalf("TopTools count = %d, want 2", len(r.TopTools))
	}
	if r.TopTools[0].Name != "run_command" || r.TopTools[0].Calls != 7 {
		t.Errorf("TopTools[0] = %+v, want run_command with 7 calls", r.TopTools[0])
	}
	if r.TopTools[1].Name != "edit_file" || r.TopTools[1].Calls != 5 {
		t.Errorf("TopTools[1] = %+v, want edit_file with 5 calls", r.TopTools[1])
	}

	// Verify rep.Checks.Repairs matches
	if rep.Checks.Repairs.Runs != r.Runs || rep.Checks.Repairs.ToolCalls != r.ToolCalls {
		t.Errorf("rep.Checks.Repairs does not match rep.Merges.Repairs")
	}
}

// Ports are split by who made them, so the agent runs a mechanical
// cherry-pick saves show: grain's own, the ones of those the merge queue
// still had to hand to an agent, and the agent-made rest.
func TestMergesCountPortsByWhoMadeThem(t *testing.T) {
	clean := landed("1", time.Hour, 2*time.Hour, 2*time.Hour, 3*time.Hour)
	clean.Port = model.PortMechanical
	repaired := landed("2", time.Hour, 2*time.Hour, 2*time.Hour, 4*time.Hour)
	repaired.Port, repaired.FixTaskFiled = model.PortMechanical, true
	agent := landed("3", time.Hour, 2*time.Hour, 2*time.Hour, 5*time.Hour)
	agent.Port = model.PortByAgent
	notPort := landed("4", time.Hour, 2*time.Hour, 2*time.Hour, 6*time.Hour)
	// Merged after the window: not counted, port or not.
	late := landed("5", time.Hour, 2*time.Hour, 2*time.Hour, 8*24*time.Hour)
	late.Port = model.PortMechanical

	rep := metrics.Compute(metrics.Input{Window: win, Tasks: []model.TaskTiming{clean, repaired, agent, notPort, late}})

	got := rep.Merges.Ports
	if got.Mechanical != 2 || got.Agent != 1 || got.Repaired != 1 {
		t.Errorf("Ports = %+v, want 2 mechanical (1 repaired) and 1 by agent", got)
	}
	if !closeEnough(got.MechanicalRate, 2.0/3) {
		t.Errorf("MechanicalRate = %v, want 2/3", got.MechanicalRate)
	}
}

func TestMergesContentionTelemetry(t *testing.T) {
	// Two batches in the window, each taking 20m from suite to verdict.
	// The second is a half of a failed batch: its Splits is a depth, and
	// is no longer what retests are counted from.
	b1 := batch(2, 0, 2, model.MergeBatchMerged, 1*time.Hour)
	b2 := batch(4, 1, 3, model.MergeBatchMerged, 2*time.Hour)

	// Three changes landed. The first was opened at 30m but only became
	// the queue's at 50m, and merged at 1h: ten minutes in the queue, not
	// the thirty its pull request was open. The second needed a repair --
	// which is not a rebase, and counts as none. The third merged on the
	// cycle that took it on, and is a zero wait that is kept.
	t1 := landed("1", 10*time.Minute, 30*time.Minute, 30*time.Minute, 1*time.Hour)
	t1.QueueEnteredAt = at(50 * time.Minute)
	t2 := landed("2", 30*time.Minute, 1*time.Hour, 1*time.Hour, 2*time.Hour)
	t2.QueueEnteredAt = at(1 * time.Hour)
	t2.FixTaskFiled = true
	t3 := landed("3", 30*time.Minute, 90*time.Minute, 90*time.Minute, 100*time.Minute)
	t3.QueueEnteredAt = at(100 * time.Minute)
	// Merged by hand: never the queue's, so no wait sample.
	t4 := landed("4", 30*time.Minute, 90*time.Minute, 90*time.Minute, 110*time.Minute)

	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	ev := func(task, kind, reason string, off time.Duration) model.MergeQueueContentionEvent {
		return model.MergeQueueContentionEvent{Repo: repo, TaskID: task, Kind: kind, Reason: reason, At: *at(off)}
	}
	events := []model.MergeQueueContentionEvent{
		// t2's head rebased onto a moved base, and the suite that cost.
		ev("2", model.MergeQueueRebase, model.MergeQueueStaleHead, 70*time.Minute),
		ev("2", model.MergeQueueRetest, model.MergeQueueAfterRebase, 70*time.Minute),
		// A batch split, which names no member.
		ev("", model.MergeQueueRetest, model.MergeQueueBatchSplit, 100*time.Minute),
		// A flake recheck of t1.
		ev("1", model.MergeQueueRetest, model.MergeQueueFlakeRecheck, 55*time.Minute),
		// Before the window: not counted.
		ev("1", model.MergeQueueRebase, model.MergeQueueStaleHead, -time.Hour),
	}

	rep := metrics.Compute(metrics.Input{
		Window:           win,
		MergeBatches:     []model.MergeBatchRecord{b1, b2},
		Tasks:            []model.TaskTiming{t1, t2, t3, t4},
		MergeQueueEvents: events,
	})

	c := rep.Merges.Contention
	if c.BatchSizes.N != 2 || c.BatchSizes.Min != 2 || c.BatchSizes.Max != 4 {
		t.Errorf("BatchSizes = %+v, want n=2 min=2 max=4", c.BatchSizes)
	}
	if c.GateLatency.N != 2 || c.GateLatency.Min != 20*time.Minute || c.GateLatency.Max != 20*time.Minute {
		t.Errorf("GateLatency = %+v, want two of 20m", c.GateLatency)
	}
	if c.QueueWaitTime.N != 3 {
		t.Errorf("QueueWaitTime.N = %d, want 3 (the zero kept, the hand merge left out)", c.QueueWaitTime.N)
	}
	if c.QueueWaitTime.Min != 0 || c.QueueWaitTime.Max != time.Hour {
		t.Errorf("QueueWaitTime min/max = %v/%v, want 0/1h", c.QueueWaitTime.Min, c.QueueWaitTime.Max)
	}
	if c.QueueWaitTime.P50 != 10*time.Minute {
		t.Errorf("QueueWaitTime.P50 = %v, want 10m (time in the queue, not the pull request's lifetime)", c.QueueWaitTime.P50)
	}
	want := metrics.RebaseRetestContention{Rebases: 1, Retests: 3, TotalContention: 4, ContentionRate: 1.0}
	if c.RebaseRetest != want {
		t.Errorf("RebaseRetest = %+v, want %+v", c.RebaseRetest, want)
	}

	// A report narrowed to one author keeps that author's changes' events
	// and none of the batch's, which belongs to nobody.
	t2.Author = "grace"
	scoped := metrics.Compute(metrics.Input{
		Window:           win,
		Scope:            metrics.Scope{Authors: []string{"grace"}},
		Tasks:            []model.TaskTiming{t1, t2, t3, t4},
		MergeQueueEvents: events,
	}).Merges.Contention.RebaseRetest
	if scoped.Rebases != 1 || scoped.Retests != 1 {
		t.Errorf("author-scoped RebaseRetest = %+v, want t2's one rebase and its one retest", scoped)
	}
}

// The overlap re-check's counters (grain/task-985): what the rule said
// about each merge in the window, and what came of it.
func TestMergesCountTheOverlapRecheck(t *testing.T) {
	at := func(h int) time.Time { return win.Since.Add(time.Duration(h) * time.Hour) }
	rec := func(h int, triggered bool, clauses, detail string, rechecked, caught bool, base string) model.MergeRecheckRecord {
		return model.MergeRecheckRecord{
			Repo: model.RepoRef{Owner: "o", Name: "r"}, Branch: "main", Mode: model.MergeRecheckObserve,
			Fork: "f", Tip: "t", Triggered: triggered, Clauses: clauses, Detail: detail,
			Rechecked: rechecked, Caught: caught, BaseVerdict: base, DecidedAt: at(h),
		}
	}
	current := rec(4, false, "", "", false, false, model.MergeRecheckBaseGreen)
	current.Tip = "f"
	rep := metrics.Compute(metrics.Input{
		Window: win,
		MergeRechecks: []model.MergeRecheckRecord{
			rec(1, true, "same-file,same-package", "same-file pkg/ui/a.go; same-package pkg/ui",
				true, true, ""),
			rec(2, true, "same-package", "same-package pkg/ui", false, false, model.MergeRecheckBaseGreen),
			rec(3, false, "", "", false, false, model.MergeRecheckBaseRed),
			current,
			// Before the window: counted in Reaches only.
			{Repo: model.RepoRef{Owner: "o", Name: "r"}, Triggered: true, DecidedAt: win.Since.Add(-time.Hour)},
		},
	})
	got := rep.Merges.Rechecks
	if got.Merges != 4 || got.Triggered != 2 || got.Skipped != 2 || got.TriggerRate != 0.5 {
		t.Fatalf("merges/triggered/skipped/rate = %d/%d/%d/%v, want 4/2/2/0.5",
			got.Merges, got.Triggered, got.Skipped, got.TriggerRate)
	}
	if got.Clauses["same-package"] != 2 || got.Clauses["same-file"] != 1 || got.Packages["pkg/ui"] != 2 {
		t.Fatalf("clauses %v, packages %v", got.Clauses, got.Packages)
	}
	if got.TriggeredAndCaught != 1 || got.Rechecked != 1 || got.SkippedBaseRed != 1 || got.BaseCurrent != 1 {
		t.Fatalf("caught/rechecked/skippedBaseRed/baseCurrent = %d/%d/%d/%d, want 1/1/1/1",
			got.TriggeredAndCaught, got.Rechecked, got.SkippedBaseRed, got.BaseCurrent)
	}
	if got.Reaches == nil || !got.Reaches.Before(win.Since) {
		t.Fatalf("reaches = %v, want the record before the window", got.Reaches)
	}
}

// Abandoned batches are counted by why, most first.
func TestMergeBatchesCountAbandonsByReason(t *testing.T) {
	abandon := func(reason string, end time.Duration) model.MergeBatchRecord {
		b := batch(2, 0, 0, model.MergeBatchAbandoned, end)
		b.Reason = reason
		return b
	}
	rep := metrics.Compute(metrics.Input{
		Window: win, MergeBatchSize: 3,
		MergeBatches: []model.MergeBatchRecord{
			abandon(model.AbandonInterrupted, time.Hour),
			abandon(model.AbandonBaseMoved, 2*time.Hour),
			abandon(model.AbandonInterrupted, 3*time.Hour),
			abandon("", 4*time.Hour),
			batch(3, 0, 3, model.MergeBatchMerged, 5*time.Hour),
		},
	})
	want := []metrics.MergeBatchAbandonReason{
		{Reason: model.AbandonInterrupted, Batches: 2},
		{Reason: model.AbandonBaseMoved, Batches: 1},
		{Reason: model.AbandonUnrecorded, Batches: 1},
	}
	got := rep.Merges.Batches.AbandonedBy
	if rep.Merges.Batches.Abandoned != 4 || len(got) != len(want) {
		t.Fatalf("abandoned %d by %+v, want 4 by %+v", rep.Merges.Batches.Abandoned, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("AbandonedBy = %+v, want %+v", got, want)
		}
	}
}

// What each base-branch repair pass held: windowed on when each pass
// ended, with open passes counted apart and in no distribution.
func TestMergesSummarizeWhatEachBaseBranchRepairHeld(t *testing.T) {
	pass := func(start, end time.Duration, peak int, held []string, prSeconds int64) model.BaseHoldPass {
		p := model.BaseHoldPass{
			Repo: model.RepoRef{Owner: "acme", Name: "widgets"}, Branch: "main",
			StartedAt: since.Add(start), SampledAt: since.Add(end),
			Peak: peak, HeldTasks: held, PRSeconds: prSeconds,
		}
		if end != 0 {
			at := since.Add(end)
			p.FinishedAt = &at
		}
		return p
	}
	rep := metrics.Compute(metrics.Input{
		Window: win,
		BaseHolds: []model.BaseHoldPass{
			pass(time.Hour, 2*time.Hour, 1, []string{"a"}, 3600),
			pass(3*time.Hour, 5*time.Hour, 3, []string{"a", "b", "c", "d"}, 4*3600),
			pass(6*time.Hour, 7*time.Hour, 2, []string{"e", "f"}, 2*3600),
			// Ended before the window: in Reaches, not in the counts.
			pass(-3*time.Hour, -2*time.Hour, 9, []string{"z"}, 9*3600),
			// Still open.
			pass(8*time.Hour, 0, 4, []string{"g"}, 0),
		},
	})
	h := rep.Merges.BaseHolds
	if h.Passes != 3 || h.Open != 1 {
		t.Fatalf("passes/open = %d/%d, want 3 and 1", h.Passes, h.Open)
	}
	if h.Peak.P50 != 2 || h.Peak.Max != 3 {
		t.Errorf("peak = %+v, want p50 2 and max 3", h.Peak)
	}
	if h.Total.P50 != 2 || h.Total.Max != 4 || h.Total.Total != 7 {
		t.Errorf("total = %+v, want p50 2, max 4, 7 in all", h.Total)
	}
	if h.PRHeld.P50 != 2*time.Hour || h.PRHeld.Max != 4*time.Hour {
		t.Errorf("PR-time held = %+v, want p50 2h and max 4h", h.PRHeld)
	}
	if h.Length.Max != 2*time.Hour {
		t.Errorf("length = %+v, want max 2h", h.Length)
	}
	if h.Reaches == nil || !h.Reaches.Equal(since.Add(-3*time.Hour)) {
		t.Errorf("reaches = %v, want the oldest pass's start", h.Reaches)
	}
}

// forgeCLs names the forge its CLs land on, as the edition does.
type forgeCLs struct{}

func (forgeCLs) Kind() model.LinkKind { return model.LinkChangelist }
func (forgeCLs) Forge() string        { return "acme-forge" }
func (forgeCLs) Observe(context.Context, model.Task, string) (changepkg.Observation, error) {
	return changepkg.Observation{}, nil
}
func (forgeCLs) Threads(context.Context, model.Task, string) ([]changepkg.Thread, error) {
	return nil, nil
}
func (forgeCLs) Submit(context.Context, model.Task, string, changepkg.Person) (changepkg.Result, error) {
	return changepkg.Result{}, nil
}
func (forgeCLs) Sync(context.Context, model.Task, string) (changepkg.SyncResult, error) {
	return changepkg.SyncResult{}, nil
}

// A submitted CL lands like a merged pull request, and is counted under
// the forge its provider names.
func TestMergesCountSubmittedCLsByForge(t *testing.T) {
	changepkg.Register(forgeCLs{})
	t.Cleanup(func() { changepkg.Unregister(model.LinkChangelist) })
	cl := landed("2", time.Hour, 5*time.Hour, 5*time.Hour, 6*time.Hour)
	cl.ChangeKind = model.LinkChangelist
	rep := metrics.Compute(metrics.Input{
		Window: win,
		Tasks:  []model.TaskTiming{landed("1", time.Hour, 5*time.Hour, 5*time.Hour, 6*time.Hour), cl},
	})
	m := rep.Merges
	if m.Landed != 2 || m.LandedByForge[metrics.ForgeGitHub] != 1 || m.LandedByForge["acme-forge"] != 1 {
		t.Fatalf("Landed = %d by forge %v, want one github and one acme-forge", m.Landed, m.LandedByForge)
	}
}
