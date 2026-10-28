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

package model_test

// The bounded history of finished merge batches (grain/task-300): what a
// deployment reads to find out whether its batch size is a good bet, and
// the only record a batch leaves behind once its row is deleted.

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func batchRecord(n int, outcome string, size, merged int) model.MergeBatchRecord {
	at := time.Date(2026, 9, 19, 4, 0, 0, 0, time.UTC).Add(time.Duration(n) * time.Minute)
	return model.MergeBatchRecord{
		Repo:   model.RepoRef{Owner: "acme", Name: "widgets"},
		Branch: "main", Commit: "9f8e7d6c", RunID: "run-1",
		Size: size, Queued: size, Outcome: outcome, Merged: merged, PostMerge: true,
		StartedAt: at, FinishedAt: at.Add(20 * time.Minute),
	}
}

func TestAFinishedBatchIsKeptAndReadBackWhole(t *testing.T) {
	t.Parallel()
	store, ctx := open(t)
	want := batchRecord(1, model.MergeBatchMerged, 3, 3)
	want.Queued = 7
	want.Splits = 2
	want.Detail = "the go job failed"
	want.Reason = model.AbandonBaseMoved
	if err := store.RecordMergeBatch(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := store.MergeBatchRecords(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("the history holds %d record(s), want 1", len(got))
	}
	want.ID = got[0].ID
	if got[0] != want {
		t.Fatalf("the record came back as %+v, want %+v", got[0], want)
	}
}

// The bound is the insert's own job, not a sweep's: a history bounded by
// something that runs separately is unbounded whenever that thing is not
// running.
func TestTheBatchHistoryIsBoundedByItsOwnWrites(t *testing.T) {
	t.Parallel()
	store, ctx := open(t)
	for i := 0; i < model.MergeBatchHistoryKept+5; i++ {
		if err := store.RecordMergeBatch(ctx, batchRecord(i, model.MergeBatchMerged, 2, 2)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.MergeBatchRecords(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != model.MergeBatchHistoryKept {
		t.Fatalf("the history holds %d record(s), want %d", len(got), model.MergeBatchHistoryKept)
	}
	// Newest first, and the oldest five are the ones that went.
	if !got[0].FinishedAt.After(got[len(got)-1].FinishedAt) {
		t.Fatalf("the history is not newest-first: %v then %v", got[0].FinishedAt, got[len(got)-1].FinishedAt)
	}
}

// The counters, which are the whole point: they have to be able to say
// both "your batches are too small" and "your batches are too big".
func TestSummarizingABatchHistoryCountsBothSidesOfTheTrade(t *testing.T) {
	t.Parallel()
	records := []model.MergeBatchRecord{
		// Three landed on one suite: the bet paying off.
		batchRecord(1, model.MergeBatchMerged, 3, 3),
		// A batch that failed, halved, and the half passed: two suites
		// spent, two changes landed, and one of them was not a first try.
		batchRecord(2, model.MergeBatchSplit, 3, 0),
		func() model.MergeBatchRecord {
			r := batchRecord(3, model.MergeBatchMerged, 2, 2)
			r.Splits = 1
			return r
		}(),
		// And one that answered nothing at all.
		batchRecord(4, model.MergeBatchAbandoned, 2, 0),
	}
	// One of them had more queued behind it than it could take, which is
	// the only evidence that a bigger limit would have bought anything.
	records[0].Queued = 6

	s := model.SummarizeMergeBatches(records, 3)

	if s.Batches != 4 || s.Changes != 5 {
		t.Fatalf("counted %d batch(es) landing %d change(s), want 4 and 5", s.Batches, s.Changes)
	}
	if s.Passed != 2 || s.Split != 1 || s.Abandoned != 1 || s.Culprits != 0 {
		t.Fatalf("outcomes counted as %+v, want two passes, one split and one abandoned", s)
	}
	if s.FirstTry != 1 || s.Fresh != 3 {
		t.Fatalf("%d of %d fresh batches passed first time, want 1 of 3", s.FirstTry, s.Fresh)
	}
	if s.AtLimit != 1 {
		t.Fatalf("%d batch(es) were held down by the size limit, want 1", s.AtLimit)
	}
	// Four batch suites, two of them followed by a post-merge run on a tip
	// that moved once instead of once per change: 2+1+2+1. The
	// one-at-a-time queue would have spent one post-merge suite for each
	// of the five changes that landed.
	if s.Suites != 6 || s.SerialSuites != 5 || s.Saved != -1 {
		t.Fatalf("spent %d suite(s) against %d serial (saved %d), want 6, 5 and -1",
			s.Suites, s.SerialSuites, s.Saved)
	}
	if s.Biggest != 3 || s.TotalSize != 10 {
		t.Fatalf("bets sized %d at most, %d in total, want 3 and 10", s.Biggest, s.TotalSize)
	}
	if s.FirstAt == nil || s.LastAt == nil || !s.LastAt.After(*s.FirstAt) {
		t.Fatalf("the history is not bounded in time: %v to %v", s.FirstAt, s.LastAt)
	}
}

// An empty history says nothing rather than saying zero is a rate: a
// deployment that has never batched must not read as one whose batches
// all fail.
func TestSummarizingAnEmptyBatchHistorySaysNothing(t *testing.T) {
	t.Parallel()
	s := model.SummarizeMergeBatches(nil, 3)
	if s.Batches != 0 || s.Suites != 0 || s.Saved != 0 || s.FirstAt != nil || s.LastAt != nil {
		t.Fatalf("an empty history summarized to %+v", s)
	}
}
