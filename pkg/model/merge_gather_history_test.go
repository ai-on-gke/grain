package model_test

// The bounded history of ended gather holds (grain/task-440): what a
// deployment reads to find out whether the wait its merge queue takes is
// the right length, and the only record a hold leaves at all -- the hold
// itself is process state, and the row on the change it held is
// overwritten the moment the wait ends.

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func gatherRecord(n int, outcome string, waited time.Duration) model.MergeGatherRecord {
	at := time.Date(2026, 9, 21, 4, 0, 0, 0, time.UTC).Add(time.Duration(n) * time.Minute)
	return model.MergeGatherRecord{
		Repo: model.RepoRef{Owner: "acme", Name: "widgets"}, Number: 40 + n,
		Waiting: "c3d4", Window: 5 * time.Minute, Outcome: outcome,
		ArmedAt: at, EndedAt: at.Add(waited),
	}
}

func TestAnEndedGatherHoldIsKeptAndReadBackWhole(t *testing.T) {
	t.Parallel()
	store, ctx := open(t)
	want := gatherRecord(1, model.MergeGatherExpired, 5*time.Minute+2*time.Second)
	if err := store.RecordMergeGather(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := store.MergeGatherRecords(ctx)
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
	// The wait as it was actually paid, which for an expired hold is the
	// window plus however long the reconcile tick that noticed took.
	if got[0].Waited() != 5*time.Minute+2*time.Second {
		t.Fatalf("the hold waited %s, want 5m2s", got[0].Waited())
	}
}

// The bound is the insert's own job, for the batch history's reason: a
// history bounded by something that runs separately is unbounded
// whenever that thing is not running.
func TestTheGatherHistoryIsBoundedByItsOwnWrites(t *testing.T) {
	t.Parallel()
	store, ctx := open(t)
	for i := 0; i < model.MergeGatherHistoryKept+5; i++ {
		if err := store.RecordMergeGather(ctx, gatherRecord(i, model.MergeGatherExpired, time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.MergeGatherRecords(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != model.MergeGatherHistoryKept {
		t.Fatalf("the history holds %d record(s), want %d", len(got), model.MergeGatherHistoryKept)
	}
	if !got[0].EndedAt.After(got[len(got)-1].EndedAt) {
		t.Fatalf("the history is not newest-first: %v then %v", got[0].EndedAt, got[len(got)-1].EndedAt)
	}
}

// The reading the whole thing exists for: holds that paid off pay
// quickly, holds that expire pay the whole window, and the counters say
// which of the two this deployment is mostly doing.
func TestSummarizingAGatherHistorySaysWhetherTheWaitPaid(t *testing.T) {
	t.Parallel()
	records := []model.MergeGatherRecord{
		gatherRecord(1, model.MergeGatherBatched, 40*time.Second),
		gatherRecord(2, model.MergeGatherBatched, 80*time.Second),
		gatherRecord(3, model.MergeGatherReleased, 30*time.Second),
		gatherRecord(4, model.MergeGatherExpired, 5*time.Minute),
		gatherRecord(5, model.MergeGatherExpired, 5*time.Minute),
		gatherRecord(6, model.MergeGatherExpired, 5*time.Minute),
	}
	s := model.SummarizeMergeGathers(records)
	if s.Holds != 6 || s.Batched != 2 || s.Released != 1 || s.Expired != 3 {
		t.Fatalf("counted %+v, want six holds: two batched, one released, three expired", s)
	}
	// Two minutes of waiting bought two batches; fifteen bought nothing.
	// That gap is the "shorten it" branch of the rule in
	// docs/merge-queue-batching.md, and it is unreadable without these.
	if s.BatchedWaited != 2*time.Minute || s.ExpiredWaited != 15*time.Minute {
		t.Fatalf("batched waits came to %s and expired to %s, want 2m and 15m",
			s.BatchedWaited, s.ExpiredWaited)
	}
	if s.Waited != 17*time.Minute+30*time.Second {
		t.Fatalf("the holds cost %s in all, want 17m30s", s.Waited)
	}
	if s.LongestWindow != 5*time.Minute {
		t.Fatalf("the history was taken under a window of %s, want 5m", s.LongestWindow)
	}
	if s.FirstAt == nil || s.LastAt == nil || !s.LastAt.After(*s.FirstAt) {
		t.Fatalf("the counters are not bounded in time: %+v", s)
	}
}

// A window that was lengthened or shortened mid-history is visible in
// it: the longest window any row was armed under is what a reader
// compares the waits against, so a history taken partly under an older
// setting cannot be mistaken for one taken under the current one.
func TestSummarizingAGatherHistoryKeepsTheWindowEachHoldWasArmedUnder(t *testing.T) {
	t.Parallel()
	old := gatherRecord(1, model.MergeGatherExpired, 10*time.Minute)
	old.Window = 10 * time.Minute
	s := model.SummarizeMergeGathers([]model.MergeGatherRecord{
		old, gatherRecord(2, model.MergeGatherExpired, 5*time.Minute),
	})
	if s.LongestWindow != 10*time.Minute {
		t.Fatalf("the longest window in the history is %s, want 10m", s.LongestWindow)
	}
}

func TestSummarizingAnEmptyGatherHistorySaysNothing(t *testing.T) {
	t.Parallel()
	s := model.SummarizeMergeGathers(nil)
	if s.Holds != 0 || s.Waited != 0 || s.FirstAt != nil || s.LastAt != nil {
		t.Fatalf("an empty history counted as %+v", s)
	}
}
