package orchestrator

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
)

func fileTaggedTask(t *testing.T, ctx context.Context, store *model.Store, id string, repo model.RepoRef,
	tags ...string) {

	t.Helper()
	human := model.Principal{Kind: model.PrincipalHuman, ID: "alice"}
	if err := store.PutTask(ctx, model.Task{
		ID: id, Intent: model.IntentImplement, Title: id, Body: "please",
		Origin:   model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
		Approval: &model.Attribution{Actor: human},
		Target:   &repo, Base: "main", Binding: model.BindingDirective, Tags: tags,
	}); err != nil {
		t.Fatal(err)
	}
}

// The base going green, by any route, closes its open repair as
// superseded with a note naming the run and the commit.
func TestAGreenBaseClosesItsOpenRepair(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	fileTaggedTask(t, ctx, store, "repair-1", repo, BaseCIRepairBranchTag(repo, "main"))

	now := time.Date(2026, 10, 12, 4, 0, 0, 0, time.UTC)
	row := model.BaseBranchCI{Repo: repo, Branch: "main", Commit: "c0ffee1234567890", RunID: "run-9",
		Conclusion: "success"}
	if err := closeBaseRepairsOnGreen(ctx, Deps{Store: store}, row, now); err != nil {
		t.Fatal(err)
	}
	if st, _ := store.State(ctx, "repair-1"); st != model.StateClosed {
		t.Fatalf("repair-1 is %s, want closed once main is green", st)
	}
	comments, err := store.Comments(ctx, "repair-1")
	if err != nil || len(comments) == 0 {
		t.Fatalf("no closing note on repair-1 (%v)", err)
	}
	if note := comments[len(comments)-1].Body; !strings.Contains(note, model.GoalMetOutcome) ||
		!strings.Contains(note, "run-9") || !strings.Contains(note, "c0ffee123456") {
		t.Errorf("closing note does not name the green run and commit: %s", note)
	}

	// A red row closes nothing.
	fileTaggedTask(t, ctx, store, "repair-2", repo, BaseCIRepairBranchTag(repo, "main"))
	row.Conclusion = model.BaseBranchCIFailure
	if err := closeBaseRepairsOnGreen(ctx, Deps{Store: store}, row, now); err != nil {
		t.Fatal(err)
	}
	if st, _ := store.State(ctx, "repair-2"); st == model.StateClosed {
		t.Fatal("a red base closed its repair")
	}
	// A failure its confirmation cleared is the base not being broken too.
	row.Confirmation = model.BaseCICleared
	if err := closeBaseRepairsOnGreen(ctx, Deps{Store: store}, row, now); err != nil {
		t.Fatal(err)
	}
	if st, _ := store.State(ctx, "repair-2"); st != model.StateClosed {
		t.Fatalf("repair-2 is %s after a cleared confirmation, want closed", st)
	}
}

// redMain files grain's own repair of acme/widgets main, red and confirmed
// at a commit, and the row naming it.
func redMain(t *testing.T, ctx context.Context, store *model.Store) (model.RepoRef, *model.BaseBranchCI) {
	t.Helper()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	fileTaggedTask(t, ctx, store, "grain-repair", repo,
		baseCIRepairTag(repo, "main", "bad0000000000000"), BaseCIRepairBranchTag(repo, "main"))
	row := model.BaseBranchCI{Repo: repo, Branch: "main", Commit: "bad0000000000000", RunID: "run-1",
		Conclusion: model.BaseBranchCIFailure, Confirmation: model.BaseCIConfirmed, RepairTaskID: "grain-repair"}
	if err := store.PutBaseBranchCI(ctx, row); err != nil {
		t.Fatal(err)
	}
	return repo, &row
}

// fileMarkedTask is a person's task marked as fixing its base branch, the
// way ui.CreateTaskRequest.FixesBaseBranch files one.
func fileMarkedTask(t *testing.T, ctx context.Context, store *model.Store, id string, repo model.RepoRef) {
	t.Helper()
	fileTaggedTask(t, ctx, store, id, repo)
	if err := store.UpdateTask(ctx, id, func(task *model.Task) error {
		if !model.SetFixesBaseBranch(task, true) {
			t.Fatalf("%s could not be marked", id)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// A green branch meets every repair's base-green goal (DT-4): grain's own
// and the one a person marked fixes_base_branch both close with outcome
// goal-met and a note naming the green run, and what depends on them is
// released by the close.
func TestAGreenBaseClosesEveryRepairOnItsGoal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	fileTaggedTask(t, ctx, store, "grain-repair", repo,
		baseCIRepairTag(repo, "main", "bad0000000000000"), BaseCIRepairBranchTag(repo, "main"))
	fileMarkedTask(t, ctx, store, "deflake", repo)
	fileTaggedTask(t, ctx, store, "feature", repo)
	fileTaggedTask(t, ctx, store, "follow-up", repo)
	for _, id := range []string{"feature", "follow-up"} {
		if err := store.UpdateTask(ctx, id, func(task *model.Task) error {
			task.Links = append(task.Links, model.Link{Kind: model.LinkDependsOn, Target: "deflake"})
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}

	now := time.Date(2026, 10, 12, 23, 0, 0, 0, time.UTC)
	row := model.BaseBranchCI{Repo: repo, Branch: "main", Commit: "c0ffee1234567890", RunID: "run-9",
		Conclusion: "success"}
	for range 2 {
		if err := closeBaseRepairsOnGreen(ctx, Deps{Store: store}, row, now); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"grain-repair", "deflake"} {
		if st, _ := store.State(ctx, id); st != model.StateClosed {
			t.Fatalf("%s is %s, want closed once main is green", id, st)
		}
		g, err := store.TaskGoal(ctx, id)
		if err != nil || g == nil || g.Kind != model.GoalBaseGreen || g.OnMet != model.GoalCloseOnMet ||
			g.Outcome != model.GoalMetOutcome {
			t.Fatalf("%s's goal = %+v (%v), want base-green, close, met", id, g, err)
		}
		comments, _ := store.Comments(ctx, id)
		if len(comments) != 1 || !strings.Contains(comments[0].Body, model.GoalMetOutcome) ||
			!strings.Contains(comments[0].Body, "run-9") {
			t.Fatalf("want one goal-met note on %s naming run-9, got %+v", id, comments)
		}
	}
	for _, id := range []string{"feature", "follow-up"} {
		if n, _ := store.OpenBlockers(ctx, id); n != 0 {
			t.Fatalf("%s still has %d open blockers once main is green", id, n)
		}
	}
}
