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

// The bounded history of what each red stretch of a base branch held
// (model.BaseHoldPass, grain/task-986).

import (
	"fmt"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// Sampling credits each stretch to the count that was held through it,
// and counts a change held over several samples once.
func TestABaseHoldPassCountsPeakTotalAndPRTime(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	p := model.BaseHoldPass{StartedAt: at, SampledAt: at}
	p.Sample([]string{"a"}, at)
	p.Sample([]string{"a", "b", "c"}, at.Add(time.Hour)) // a held for the hour
	p.Sample([]string{"b"}, at.Add(90*time.Minute))      // three held for half an hour
	p.Finish(at.Add(2 * time.Hour))                      // b held for the last half hour
	if p.Peak != 3 || p.Total() != 3 || p.Held != 0 {
		t.Fatalf("peak/total/held = %d/%d/%d, want 3/3/0", p.Peak, p.Total(), p.Held)
	}
	if want := time.Hour + 3*30*time.Minute + 30*time.Minute; p.PRHeld() != want {
		t.Fatalf("PR-time held = %s, want %s", p.PRHeld(), want)
	}
	if p.FinishedAt == nil || !p.FinishedAt.Equal(at.Add(2*time.Hour)) {
		t.Fatalf("finished at %v, want the closing sample", p.FinishedAt)
	}
}

func TestABaseHoldPassIsUpdatedInPlaceAndReadBackWhole(t *testing.T) {
	t.Parallel()
	store, ctx := open(t)
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	p := model.BaseHoldPass{Repo: model.RepoRef{Owner: "acme", Name: "widgets"}, Branch: "main",
		StartedAt: at, SampledAt: at}
	p.Sample([]string{"t1", "t2"}, at)
	id, err := store.PutBaseHoldPass(ctx, p)
	if err != nil || id == 0 {
		t.Fatalf("writing a new pass: id %d, %v", id, err)
	}
	p.ID = id
	p.RepairTaskID = "t9"
	p.Finish(at.Add(time.Hour))
	if _, err := store.PutBaseHoldPass(ctx, p); err != nil {
		t.Fatal(err)
	}
	if open, err := store.OpenBaseHoldPasses(ctx); err != nil || len(open) != 0 {
		t.Fatalf("open passes after finishing the only one: %+v (%v)", open, err)
	}
	got, err := store.BaseHoldPasses(ctx)
	if err != nil || len(got) != 1 {
		t.Fatalf("the history holds %+v (%v), want the one pass", got, err)
	}
	g := got[0]
	if g.ID != id || g.RepairTaskID != "t9" || g.Peak != 2 || g.Total() != 2 ||
		g.PRHeld() != 2*time.Hour || g.FinishedAt == nil || !g.StartedAt.Equal(at) {
		t.Fatalf("read back %+v, want what was written", g)
	}
}

// Bounded by its own writes, and only over finished passes: an open pass
// is one still being measured, whatever its age.
func TestTheBaseHoldHistoryIsBoundedAndKeepsOpenPasses(t *testing.T) {
	t.Parallel()
	store, ctx := open(t)
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	openPass := model.BaseHoldPass{Repo: model.RepoRef{Owner: "acme", Name: "widgets"},
		Branch: "release", StartedAt: at, SampledAt: at}
	if _, err := store.PutBaseHoldPass(ctx, openPass); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < model.BaseHoldHistoryKept+5; i++ {
		start := at.Add(time.Duration(i) * time.Minute)
		p := model.BaseHoldPass{Repo: model.RepoRef{Owner: "acme", Name: "widgets"},
			Branch: fmt.Sprintf("b%d", i), StartedAt: start, SampledAt: start}
		p.Finish(start.Add(time.Second))
		if _, err := store.PutBaseHoldPass(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	all, err := store.BaseHoldPasses(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != model.BaseHoldHistoryKept+1 {
		t.Fatalf("the history holds %d rows, want %d finished and the open one", len(all), model.BaseHoldHistoryKept)
	}
	if open, err := store.OpenBaseHoldPasses(ctx); err != nil || len(open) != 1 || open[0].Branch != "release" {
		t.Fatalf("the open pass was trimmed: %+v (%v)", open, err)
	}
	if all[0].Branch != fmt.Sprintf("b%d", model.BaseHoldHistoryKept+4) {
		t.Fatalf("newest row is %q, want the last one written", all[0].Branch)
	}
}

// Abandoned batches are partitioned by why, and a row from before the
// reason was recorded still counts, as unrecorded.
func TestSummarizingABatchHistoryCountsAbandonsByReason(t *testing.T) {
	t.Parallel()
	timedOut := batchRecord(1, model.MergeBatchAbandoned, 2, 0)
	timedOut.Reason = model.AbandonTimedOut
	timedOut2 := batchRecord(2, model.MergeBatchAbandoned, 2, 0)
	timedOut2.Reason = model.AbandonTimedOut
	old := batchRecord(3, model.MergeBatchAbandoned, 2, 0)
	s := model.SummarizeMergeBatches([]model.MergeBatchRecord{
		timedOut, timedOut2, old, batchRecord(4, model.MergeBatchMerged, 2, 2),
	}, 3)
	if s.Abandoned != 3 || s.AbandonedBy[model.AbandonTimedOut] != 2 ||
		s.AbandonedBy[model.AbandonUnrecorded] != 1 || len(s.AbandonedBy) != 2 {
		t.Fatalf("abandoned %d by %v, want 3: two timed out, one unrecorded", s.Abandoned, s.AbandonedBy)
	}
}
