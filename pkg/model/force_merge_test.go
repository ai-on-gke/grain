package model_test

// The bounded history of forced merges (grain/task-479): the record an
// override leaves behind once the stamp that asked for it has been spent,
// and the only thing that makes "forcing should be rare" a claim anybody
// can check.

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func forceMergeRecord(n int, at time.Time) model.ForceMergeRecord {
	return model.ForceMergeRecord{
		TaskID: "task-1", Repo: model.RepoRef{Owner: "acme", Name: "widgets"},
		Number: 40 + n, Branch: "main", Commit: "c3d4e5f",
		Reason: "the release is broken and this is the fix", By: "ada",
		Skipped:     "grain CI has not passed for this change's own head (go: failure)",
		RequestedAt: at, MergedAt: at.Add(time.Minute),
	}
}

func TestAForcedMergeIsKeptAndReadBackWhole(t *testing.T) {
	t.Parallel()
	store, ctx := open(t)
	want := forceMergeRecord(1, time.Date(2026, 9, 21, 4, 0, 0, 0, time.UTC))
	if err := store.RecordForceMerge(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := store.ForceMergeRecords(ctx)
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

// The bound is the insert's own job, for the batch history's reason: a
// history bounded by something that runs separately is unbounded whenever
// that thing is not running.
func TestTheForcedMergeHistoryIsBoundedByItsOwnWrites(t *testing.T) {
	t.Parallel()
	store, ctx := open(t)
	base := time.Date(2026, 9, 21, 4, 0, 0, 0, time.UTC)
	for i := 0; i < model.ForceMergeHistoryKept+5; i++ {
		at := base.Add(time.Duration(i) * time.Hour)
		if err := store.RecordForceMerge(ctx, forceMergeRecord(i, at)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.ForceMergeRecords(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != model.ForceMergeHistoryKept {
		t.Fatalf("the history holds %d record(s), want it bounded at %d",
			len(got), model.ForceMergeHistoryKept)
	}
	// Newest first, and the five oldest are the ones that went.
	if got[0].Number != 40+model.ForceMergeHistoryKept+4 {
		t.Fatalf("the newest record is #%d, want the last one written", got[0].Number)
	}
}

// The counter the history exists for: forcing is meant to be rare, and
// rare is a rate over a week rather than a total since the deployment was
// installed.
func TestForcedMergesAreCountableOverAWindow(t *testing.T) {
	t.Parallel()
	store, ctx := open(t)
	now := time.Date(2026, 9, 21, 4, 0, 0, 0, time.UTC)
	for i, at := range []time.Time{
		now.Add(-30 * 24 * time.Hour), now.Add(-8 * 24 * time.Hour),
		now.Add(-3 * 24 * time.Hour), now.Add(-time.Hour),
	} {
		if err := store.RecordForceMerge(ctx, forceMergeRecord(i, at)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.ForceMergeRecords(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n := model.CountForceMergesSince(got, now.Add(-7*24*time.Hour)); n != 2 {
		t.Fatalf("%d forced merges in the last week, want 2 of the 4 on file", n)
	}
}
