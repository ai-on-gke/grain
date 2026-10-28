package sqlite_test

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// The overlap re-check's history (model.MergeRecheckRecord): written once
// per merge, read back whole, and told afterwards what the branch's own
// CI made of the merges it judged.
func TestMergeRecheckHistoryRecordsAndJudges(t *testing.T) {
	store, _, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	at := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	for i, r := range []model.MergeRecheckRecord{
		{Repo: repo, Branch: "main", Number: 1, TaskID: "t1", Mode: model.MergeRecheckObserve,
			Head: "h1", Fork: "f", Tip: "t", Triggered: true, Clauses: "same-package",
			Detail: "same-package pkg/ui", DecidedAt: at},
		{Repo: repo, Branch: "main", Number: 2, TaskID: "t2", Mode: model.MergeRecheckObserve,
			Head: "h2", Fork: "t", Tip: "t", DecidedAt: at.Add(time.Hour)},
		{Repo: repo, Branch: "other", Number: 3, TaskID: "t3", Mode: model.MergeRecheckEnforce,
			Head: "h3", Rechecked: true, Caught: true, Triggered: true, DecidedAt: at},
	} {
		if err := store.RecordMergeRecheck(ctx, r); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}
	// main's run started between the two merges into it: it judged the
	// first and not the second, and nothing about the other branch.
	if err := store.JudgeMergeRechecks(ctx, repo, "main", at.Add(30*time.Minute), model.MergeRecheckBaseRed); err != nil {
		t.Fatal(err)
	}
	// A later green does not overwrite a verdict already recorded.
	if err := store.JudgeMergeRechecks(ctx, repo, "main", at.Add(2*time.Hour), model.MergeRecheckBaseGreen); err != nil {
		t.Fatal(err)
	}
	records, err := store.MergeRecheckRecords(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byTask := map[string]model.MergeRecheckRecord{}
	for _, r := range records {
		byTask[r.TaskID] = r
	}
	if len(records) != 3 {
		t.Fatalf("read back %d records, want 3", len(records))
	}
	if got := byTask["t1"]; got.BaseVerdict != model.MergeRecheckBaseRed || !got.Triggered ||
		got.SamePackage() != "pkg/ui" || got.Mode != model.MergeRecheckObserve || !got.DecidedAt.Equal(at) {
		t.Fatalf("t1 = %+v", got)
	}
	if got := byTask["t2"]; got.BaseVerdict != model.MergeRecheckBaseGreen {
		t.Fatalf("t2's verdict = %q, want green", got.BaseVerdict)
	}
	if got := byTask["t3"]; got.BaseVerdict != "" || !got.Caught || !got.Rechecked {
		t.Fatalf("t3 = %+v", got)
	}
}
