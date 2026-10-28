package model_test

// The bounded history of what the merge queue had to do over again
// (grain/task-3227): the rebases and retests the merges report's
// contention counters are read from, and which nothing else remembers.

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func contentionEvent(n int, kind, reason string) model.MergeQueueContentionEvent {
	return model.MergeQueueContentionEvent{
		Repo: model.RepoRef{Owner: "acme", Name: "widgets"}, TaskID: "c3d4",
		Kind: kind, Reason: reason,
		At: time.Date(2026, 10, 12, 4, 0, 0, 0, time.UTC).Add(time.Duration(n) * time.Minute),
	}
}

func TestAMergeQueueContentionEventIsKeptAndReadBackWhole(t *testing.T) {
	t.Parallel()
	store, ctx := open(t)
	rebase := contentionEvent(1, model.MergeQueueRebase, model.MergeQueueStaleHead)
	split := contentionEvent(2, model.MergeQueueRetest, model.MergeQueueBatchSplit)
	split.TaskID = ""
	for _, e := range []model.MergeQueueContentionEvent{rebase, split} {
		if err := store.RecordMergeQueueContention(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.MergeQueueContentionEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("the history holds %d event(s), want 2", len(got))
	}
	// Newest first.
	split.ID, rebase.ID = got[0].ID, got[1].ID
	if got[0] != split || got[1] != rebase {
		t.Fatalf("the history came back as %+v, want %+v then %+v", got, split, rebase)
	}
}

func TestTheMergeQueueContentionHistoryIsBoundedByItsOwnWrites(t *testing.T) {
	t.Parallel()
	store, ctx := open(t)
	for i := 0; i < model.MergeQueueContentionKept+5; i++ {
		if err := store.RecordMergeQueueContention(ctx, contentionEvent(i, model.MergeQueueRetest, model.MergeQueueFlakeRecheck)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.MergeQueueContentionEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != model.MergeQueueContentionKept {
		t.Fatalf("the history holds %d event(s), want %d", len(got), model.MergeQueueContentionKept)
	}
	if want := contentionEvent(model.MergeQueueContentionKept+4, "", "").At; !got[0].At.Equal(want) {
		t.Fatalf("the newest event kept is at %v, want %v", got[0].At, want)
	}
}
