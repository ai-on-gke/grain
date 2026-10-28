package orchestrator_test

import (
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/trustboundary"
)

// reconcileBranchCI is one cycle of grain CI as it reaches a held change
// and its in-place review: the "ci" reconciler, which walks the tasks with
// an open pull request (the held change), and then the review reconciled
// directly, as anything else that ever reconciled it would.
func reconcileBranchCI(t *testing.T, ctx context.Context, store *model.Store, client github.Client,
	runner *fakeCI, reviewID string, now time.Time) {

	t.Helper()
	if err := runCIReconcilerAt(t, ctx, store, client, runner, now); err != nil {
		t.Fatalf("ci reconciler: %v", err)
	}
	deps := orchestrator.Deps{Store: store, Client: client, CI: runner}
	if err := orchestrator.ReconcileTaskCI(ctx, deps, reloaded(t, ctx, store, reviewID), now); err != nil {
		t.Fatalf("reconciling the review's CI: %v", err)
	}
}

// requestsBy is every run fakeCI was asked for, by who asked.
func requestsBy(runner *fakeCI) []string {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	out := make([]string, 0, len(runner.requests))
	for _, req := range runner.requests {
		out = append(out, req.RequestedBy+"@"+req.Commit)
	}
	return out
}

// A security review works on the held change's own branch, so the two
// share one head -- and one CI run. Only the held task owns CI for the
// branch: a push to it, the reviewer's own included, starts exactly one
// run, asked for by the held task; the review starts, retries and records
// none (bwsalmon/grain#5011 ran every suite twice). The hold is released
// at the reviewer's head exactly as before.
func TestInPlaceReviewAndItsChangeShareOneCIRun(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client, task, reviewID := heldTaskWithSim(t, ctx, store)
	branch := model.BranchName(task.ID)
	grantCI(t, ctx, store, reloaded(t, ctx, store, task.ID))
	grantCI(t, ctx, store, reloaded(t, ctx, store, reviewID))
	runner := newFakeCI()

	first := branchSHA(t, sim.BareRepo, branch)
	reconcileBranchCI(t, ctx, store, client, runner, reviewID, baseTime.Add(time.Minute))
	reconcileBranchCI(t, ctx, store, client, runner, reviewID, baseTime.Add(2*time.Minute))
	if got := requestsBy(runner); len(got) != 1 || got[0] != task.ID+"@"+first {
		t.Fatalf("CI runs asked for = %v, want exactly one, by %s at %s", got, task.ID, first[:12])
	}
	held := ciStatusOf(t, ctx, store, task.ID)
	if held == nil || held.Commit != first {
		t.Fatalf("the held task's CI = %+v, want its run at %s", held, first[:12])
	}
	if own := ciStatusOf(t, ctx, store, reviewID); own != nil {
		t.Fatalf("the review recorded a CI status of its own: %+v", own)
	}

	// The reviewer pushes a fix: the held task's reconcile sees the moved
	// head and starts the one run for it, superseding exactly the stale one.
	finishInPlaceReview(t, ctx, store, sim, client, task, reviewID, "pkg/widgets/validate.go")
	head := branchSHA(t, sim.BareRepo, branch)
	reconcileBranchCI(t, ctx, store, client, runner, reviewID, baseTime.Add(3*time.Minute))
	reconcileBranchCI(t, ctx, store, client, runner, reviewID, baseTime.Add(4*time.Minute))
	got := requestsBy(runner)
	if len(got) != 2 || got[1] != task.ID+"@"+head {
		t.Fatalf("CI runs asked for after the reviewer's push = %v, want one more, by %s at %s", got, task.ID, head[:12])
	}
	now := ciStatusOf(t, ctx, store, task.ID)
	if now == nil || now.Commit != head || now.RunID == held.RunID {
		t.Fatalf("the held task's CI = %+v, want a new run at the reviewer's head %s", now, head[:12])
	}
	runner.mu.Lock()
	cancelled := append([]string(nil), runner.cancelled...)
	runner.mu.Unlock()
	if len(cancelled) != 1 || cancelled[0] != held.RunID {
		t.Fatalf("cancelled %v, want exactly the superseded run %s", cancelled, held.RunID)
	}
	if own := ciStatusOf(t, ctx, store, reviewID); own != nil {
		t.Fatalf("the review recorded a CI status of its own: %+v", own)
	}

	if err := orchestrator.SyncSecurityHolds(ctx, store, client, baseTime.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	cleared := reloaded(t, ctx, store, task.ID)
	if !cleared.AutoMerge || linkOf(cleared, model.LinkSecurityHold) != "" {
		t.Fatalf("the review did not release the hold: automerge=%v links=%+v", cleared.AutoMerge, cleared.Links)
	}
	if want := trustboundary.Version + "@" + head; linkOf(cleared, model.LinkTrustBoundaryCleared) != want {
		t.Fatalf("clearance = %q, want %q, the head the reviewer left", linkOf(cleared, model.LinkTrustBoundaryCleared), want)
	}
}

// ci.Service.Start hands an identical live plan to whoever asks for it,
// so the run judging the branch's head may carry an in-place review's
// name -- one it started before reviews stopped owning CI. That review
// closing (superseded here) cancels nothing: the run is the branch's, and
// the held task's verdict is waiting on it.
func TestClosingAnInPlaceReviewLeavesTheBranchsRunAlone(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client, task, first := heldTaskWithSim(t, ctx, store)
	branch := model.BranchName(task.ID)
	grantCI(t, ctx, store, reloaded(t, ctx, store, task.ID))
	if second := rerunHeld(t, ctx, store, sim, client, task); second == "" || second == first {
		t.Fatalf("the first review was not superseded: first=%q second=%q", first, second)
	}
	if st, err := store.State(ctx, first); err != nil || st != model.StateClosed {
		t.Fatalf("first review state = %q (%v), want closed", st, err)
	}
	head := branchSHA(t, sim.BareRepo, branch)
	runner := newFakeCI()
	runner.remember(ci.Run{ID: "run-shared", Repo: "acme/widgets", Ref: branch, Commit: head,
		Event: ci.EventPush, RequestedBy: first})
	if err := store.ObserveField(ctx, task.ID, baseTime.Add(time.Hour), func(o *model.Observation) {
		o.CI = &model.CIStatus{RunID: "run-shared", Commit: head}
	}); err != nil {
		t.Fatal(err)
	}

	if err := runCIReconcilerAt(t, ctx, store, client, runner, baseTime.Add(2*time.Hour)); err != nil {
		t.Fatalf("ci reconciler: %v", err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.cancelled) != 0 || len(runner.requests) != 0 {
		t.Fatalf("cancelled %v and started %d runs; the branch's live run at its head is the held task's verdict",
			runner.cancelled, len(runner.requests))
	}
}

// A review stacked on the reviewed branch with a branch of its own --
// filed before in-place reviewing, or a declared review (fileReviewTask)
// -- still gets its own CI on its own branch.
func TestStackedReviewStillGetsItsOwnCI(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client, task, _ := heldTaskWithSim(t, ctx, store)
	reviewed := model.BranchName(task.ID)
	own := model.BranchName("stacked-review")
	run(t, t.TempDir(), "git", "--git-dir", sim.BareRepo, "branch", own, reviewed)
	pushFile(t, sim.BareRepo, own, "pkg/widgets/validate.go", "a stacked fix\n")
	head := branchSHA(t, sim.BareRepo, own)
	review := grantCI(t, ctx, store, model.Task{ID: "stacked-review", Intent: model.IntentImplement,
		Title: "review it", Target: task.Target, Base: reviewed, Approval: task.Approval,
		Origin: model.Origin{Reason: model.ReasonReview}})

	runner := newFakeCI()
	deps := orchestrator.Deps{Store: store, Client: client, CI: runner}
	if err := orchestrator.ReconcileTaskCI(ctx, deps, review, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("reconciling the stacked review's CI: %v", err)
	}
	if got := requestsBy(runner); len(got) != 1 || got[0] != review.ID+"@"+head {
		t.Fatalf("CI runs asked for = %v, want the stacked review's own at %s", got, head[:12])
	}
	if st := ciStatusOf(t, ctx, store, review.ID); st == nil || st.Commit != head {
		t.Fatalf("the stacked review's CI = %+v, want its own run at %s", st, head[:12])
	}
}
