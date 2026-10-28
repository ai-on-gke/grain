package ui_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// batchOf puts a running batch on acme/widgets' main branch holding the
// given tasks, in the order they are passed -- which is the queue's own
// order, and the order the API has to keep.
func batchOf(t *testing.T, store *model.Store, ctx context.Context, tasks ...ui.Task) model.MergeBatch {
	t.Helper()
	batch := model.MergeBatch{
		Repo:       model.RepoRef{Owner: "acme", Name: "widgets"},
		Branch:     "main",
		Ref:        model.MergeBatchRef("", "main"),
		BaseCommit: "base00000000",
		Commit:     "batch0000000",
		RunID:      "run-42",
		StartedAt:  baseTime,
	}
	for i, task := range tasks {
		batch.Members = append(batch.Members, model.MergeBatchMember{
			TaskID:  task.ID,
			Number:  100 + i,
			HeadRef: "grain/task-" + task.ID,
			HeadSHA: "head000000" + string(rune('a'+i)),
		})
	}
	if err := store.PutMergeBatch(ctx, batch); err != nil {
		t.Fatal(err)
	}
	return batch
}

// The whole point of the endpoint: three rows that each say "testing with
// the rest of the batch" are one batch, and this is what says so -- the
// ref, the commit, the run every member is riding on, and the members in
// the order a failure will be bisected in.
func TestMergeBatchesReportTheBatchAsOneThing(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	first, second, third := create(t, c, ctx), create(t, c, ctx), create(t, c, ctx)
	batchOf(t, store, ctx, first, second, third)

	got, err := c.MergeBatchesFor(ctx, "")
	if err != nil {
		t.Fatalf("MergeBatchesFor: %v", err)
	}
	if len(got.Batches) != 1 {
		t.Fatalf("batches = %d, want the one in flight", len(got.Batches))
	}
	b := got.Batches[0]
	if b.Repo != "acme/widgets" || b.Branch != "main" {
		t.Errorf("batch is on %s %s, want acme/widgets main", b.Repo, b.Branch)
	}
	if b.Ref != "grain-merge-queue/main" {
		t.Errorf("ref = %q, want the throwaway ref the batch was built on", b.Ref)
	}
	if b.Commit != "batch0000000" || b.BaseCommit != "base00000000" {
		t.Errorf("commits = %q on %q, want the tested tree and the base it came from", b.Commit, b.BaseCommit)
	}
	if b.RunID != "run-42" {
		t.Errorf("runId = %q, want the one run deciding every member", b.RunID)
	}
	if !b.Running || b.Conclusion != "" {
		t.Errorf("running = %v, conclusion = %q, want a batch still going", b.Running, b.Conclusion)
	}
	if len(b.Members) != 3 {
		t.Fatalf("members = %d, want all three", len(b.Members))
	}
	// Queue order, not any order: the first member is the half a failing
	// batch is bisected towards, so a card that reorders them is telling
	// somebody the wrong change is about to be blamed.
	if b.Members[0].TaskID != first.ID || b.Members[2].TaskID != third.ID {
		t.Errorf("members came back as %s/%s/%s, want the queue's own order",
			b.Members[0].TaskID, b.Members[1].TaskID, b.Members[2].TaskID)
	}
	if b.Members[0].Title != first.Title {
		t.Errorf("member title = %q, want the task's own title: a card that prints ids names nothing",
			b.Members[0].Title)
	}
	if b.Members[1].Number != 101 || b.Members[1].HeadSHA == "" {
		t.Errorf("member = #%d at %q, want the pull request and the commit the merge is pinned to",
			b.Members[1].Number, b.Members[1].HeadSHA)
	}
}

// The denominator for the elapsed time on the card. Hard-coding ninety
// minutes in the browser would be a second copy of a bound that lives in
// pkg/model, and would go on saying ninety after somebody changed it.
func TestMergeBatchesReportHowLongABatchMayRun(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	got, err := c.MergeBatchesFor(ctx, "")
	if err != nil {
		t.Fatalf("MergeBatchesFor: %v", err)
	}
	if want := int(model.MaxMergeBatchAge / time.Second); got.MaxAgeSeconds != want {
		t.Errorf("maxAgeSeconds = %d, want %d", got.MaxAgeSeconds, want)
	}
	// A deployment between batches reports none rather than null: the
	// pane iterates this without checking.
	if got.Batches == nil {
		t.Error("batches came back null, want an empty list")
	}
}

// A finished row is a cooldown marker rather than a batch holding
// anybody, and the card has to be able to tell them apart: Running is
// what says which, so that "this failed and the queue is bisecting it"
// does not read as "this is still going".
func TestMergeBatchesSayWhenABatchHasReported(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	batch := batchOf(t, store, ctx, create(t, c, ctx))
	finished := baseTime.Add(20 * time.Minute)
	batch.Conclusion = model.MergeBatchFailure
	batch.Detail = "the go job failed"
	batch.FinishedAt = &finished
	batch.Splits = 1
	if err := store.PutMergeBatch(ctx, batch); err != nil {
		t.Fatal(err)
	}

	got, err := c.MergeBatchesFor(ctx, "")
	if err != nil {
		t.Fatalf("MergeBatchesFor: %v", err)
	}
	b := got.Batches[0]
	if b.Running {
		t.Error("a batch with a conclusion is not running")
	}
	if b.Conclusion != model.MergeBatchFailure || b.Detail != "the go job failed" {
		t.Errorf("conclusion = %q (%q), want the verdict and its words", b.Conclusion, b.Detail)
	}
	if b.FinishedAt == nil || !b.FinishedAt.Equal(finished) {
		t.Errorf("finishedAt = %v, want %v", b.FinishedAt, finished)
	}
	if b.Splits != 1 {
		t.Errorf("splits = %d, want the halvings this lineage has cost", b.Splits)
	}
}

// ?repo= narrows to one repository, the way GET /api/ci/runs does -- the
// repo page asks about its own batches and should not be shown another
// repository's.
func TestMergeBatchesNarrowToOneRepo(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	batchOf(t, store, ctx, create(t, c, ctx))
	if err := store.PutMergeBatch(ctx, model.MergeBatch{
		Repo:      model.RepoRef{Owner: "other", Name: "thing"},
		Branch:    "main",
		Ref:       model.MergeBatchRef("", "main"),
		Commit:    "elsewhere00",
		RunID:     "run-99",
		StartedAt: baseTime,
	}); err != nil {
		t.Fatal(err)
	}

	all, err := c.MergeBatchesFor(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Batches) != 2 {
		t.Fatalf("unnarrowed = %d batches, want both", len(all.Batches))
	}
	// Spelled the way somebody might type it rather than the way it is
	// stored: parseRepo folds that, and a card must not go blank because
	// of a capital letter.
	mine, err := c.MergeBatchesFor(ctx, "ACME/Widgets")
	if err != nil {
		t.Fatal(err)
	}
	if len(mine.Batches) != 1 || mine.Batches[0].Repo != "acme/widgets" {
		t.Fatalf("narrowed = %+v, want only acme/widgets' own batch", mine.Batches)
	}
}

func TestMergeBatchesOverHTTP(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	srv := ui.NewServerWithClient(client)
	batchOf(t, store, ctx, create(t, client, ctx))

	rec := do(t, srv, http.MethodGet, "/api/merge-batches?repo=acme/widgets", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	got := decode[ui.MergeBatches](t, rec)
	if len(got.Batches) != 1 || got.Batches[0].RunID != "run-42" {
		t.Fatalf("body = %+v, want the batch in flight", got)
	}

	// A repo name that does not parse is the caller's mistake, not a 500.
	bad := do(t, srv, http.MethodGet, "/api/merge-batches?repo=not-a-repo", "")
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("status = %d for an unparseable repo, want 400: %s", bad.Code, bad.Body)
	}
}
