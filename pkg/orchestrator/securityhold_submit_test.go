package orchestrator_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/trustboundary"
)

// humanSubmit is what ui.Client.Submit writes: auto-merge on, the holds
// the change waited under gone, and the decision marked, with shown, the
// head the human was shown, for the next cycle to pin if it reads that
// head.
func humanSubmit(t *testing.T, ctx context.Context, store *model.Store, id, shown string) {
	t.Helper()
	if err := store.UpdateTask(ctx, id, func(t *model.Task) error {
		t.AutoMerge = true
		t.Links = slices.DeleteFunc(t.Links, func(l model.Link) bool {
			return l.Kind == model.LinkSecurityHold || l.Kind == model.LinkReviewQuestions ||
				l.Kind == model.LinkBrokenRulesHold
		})
		t.Links = append(t.Links, model.Link{Kind: model.LinkHumanSubmitted, Target: model.HumanSubmittedTarget(baseTime, shown)})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// heldAt finishes a run of an auto-merging change touching path, which
// puts it on a security hold, and returns the sim to move it with.
func heldAt(t *testing.T, ctx context.Context, store *model.Store, path string) (*githubsim.Sim, *github.RESTClient, model.Task, string) {
	t.Helper()
	sim, client := newSim(t, "acme", "widgets", "main")
	task := finishTouching(t, ctx, store, sim, "t1", path, true)
	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	if got := reloaded(t, ctx, store, task.ID); got.AutoMerge || linkOf(got, model.LinkSecurityReviewTask) == "" {
		t.Fatalf("the change was not held: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}
	return sim, client, task, model.BranchName(task.ID)
}

func prHeadOf(t *testing.T, client github.Client, task model.Task) string {
	t.Helper()
	ref := model.TaskPullRequest(task)
	if ref == nil {
		t.Fatalf("%s has no pull request", task.ID)
	}
	detail, err := client.GetPullRequest(ref.Repo.Owner, ref.Repo.Name, ref.Number)
	if err != nil {
		t.Fatal(err)
	}
	return detail.HeadSHA
}

// Task 441: a held change whose stacked fix merged back (so its recorded
// clearance is for a head it has moved past) is shipped by a human's
// Submit, and the queue then merges main into it. The Submit is pinned to
// the head the human shipped before that merge, the merge carries it, and
// the change lands without another security review.
func TestHumanSubmitIsCarriedAcrossABaseMergeWithoutAnotherReview(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client, task, branch := heldAt(t, ctx, store, "pkg/ui/server.go")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	// The review's stacked fix merges back, touching the same file.
	pushFile(t, sim.BareRepo, branch, "pkg/ui/server.go", "fixed by the review\n")
	shipped := prHeadOf(t, client, reloaded(t, ctx, store, task.ID))
	humanSubmit(t, ctx, store, task.ID, shipped)

	// main moves on, and CI on the shipped head is red against the older
	// main: the queue merges main in.
	pushFileToBranch(t, sim.BareRepo, "main", "MAIN.md", "unrelated work", "unrelated change on main")
	setMergeable(sim, true)
	sim.CheckRuns[branch] = []github.CheckRun{{Name: "go", Status: "completed", Conclusion: strPtr("failure")}}
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Hour)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	got := reloaded(t, ctx, store, task.ID)
	if want := trustboundary.Version + "@" + shipped; linkOf(got, model.LinkTrustBoundaryCleared) != want {
		t.Fatalf("clearance = %q, want the shipped head %q: links=%+v",
			linkOf(got, model.LinkTrustBoundaryCleared), want, got.Links)
	}
	if !got.AutoMerge || linkOf(got, model.LinkHumanSubmitted) != "" {
		t.Fatalf("automerge=%v links=%+v, want auto-merge on and the Submit pinned", got.AutoMerge, got.Links)
	}
	if n := mergeWrites(sim, repo); n != 1 {
		t.Fatalf("the queue made %d base merges, want 1", n)
	}

	sim.CheckRuns[branch] = greenCheck
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(2*time.Hour)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if reviews := securityReviewsOf(t, ctx, store, task.ID); len(reviews) != 1 {
		t.Fatalf("filed %d security reviews, want only the first", len(reviews))
	}
	if !mergedBranch(sim, branch) {
		t.Fatalf("the shipped change did not merge: links=%+v", reloaded(t, ctx, store, task.ID).Links)
	}
}

// A change only a human may release carries no clearance a base merge
// could be checked against, so a Submit over it pins nothing -- and the
// base merge after it is the human's call, not another review.
func TestHumanSubmitOverAHumanOnlyHoldPinsNoClearance(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client, task, branch := heldAt(t, ctx, store, ".grain/quarantine.yml")
	humanSubmit(t, ctx, store, task.ID, prHeadOf(t, client, reloaded(t, ctx, store, task.ID)))

	pushFileToBranch(t, sim.BareRepo, "main", "MAIN.md", "unrelated work", "unrelated change on main")
	setMergeable(sim, true)
	sim.CheckRuns[branch] = []github.CheckRun{{Name: "go", Status: "completed", Conclusion: strPtr("failure")}}
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Hour)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	got := reloaded(t, ctx, store, task.ID)
	if !got.AutoMerge || linkOf(got, model.LinkTrustBoundaryCleared) != "" || linkOf(got, model.LinkHumanSubmitted) != "" {
		t.Fatalf("automerge=%v links=%+v, want auto-merge on, no clearance and no marker", got.AutoMerge, got.Links)
	}

	sim.CheckRuns[branch] = greenCheck
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(2*time.Hour)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if reviews := securityReviewsOf(t, ctx, store, task.ID); len(reviews) != 1 {
		t.Fatalf("filed %d security reviews, want only the first", len(reviews))
	}
	if !mergedBranch(sim, branch) {
		t.Fatalf("the shipped change did not merge: links=%+v", reloaded(t, ctx, store, task.ID).Links)
	}
}

// Task 979: an agent pushes between a human's Submit and the cycle that
// pins it. The pushed commit is not the one the human looked at, so the
// Submit is not pinned to it: the moved head is classified again and held
// for a review of its own, rather than merged under the first one.
func TestPushBeforePinIsNotPinned(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client, task, branch := heldAt(t, ctx, store, "pkg/ui/server.go")
	humanSubmit(t, ctx, store, task.ID, prHeadOf(t, client, reloaded(t, ctx, store, task.ID)))
	pushFile(t, sim.BareRepo, branch, "pkg/ui/server.go", "agent pushed before the pin\n")
	setMergeable(sim, true)
	sim.CheckRuns[branch] = greenCheck
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Hour)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	got := reloaded(t, ctx, store, task.ID)
	if mergedBranch(sim, branch) {
		t.Fatalf("the pushed head merged on the human's Submit of its parent: links=%+v", got.Links)
	}
	if got.AutoMerge || linkOf(got, model.LinkSecurityHold) == "" || linkOf(got, model.LinkHumanSubmitted) != "" {
		t.Fatalf("automerge=%v links=%+v, want the moved head held and the marker gone", got.AutoMerge, got.Links)
	}
	if reviews := securityReviewsOf(t, ctx, store, task.ID); len(reviews) != 2 {
		t.Fatalf("filed %d security reviews, want a second one for the pushed head", len(reviews))
	}
}

// A marker naming no head -- written before the head was recorded, or by
// a Submit nothing could read the head for -- is about the head grain
// last classified: a push past that is classified again, not cleared.
func TestHumanSubmitNamingNoHeadIsNotPinnedToAPush(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client, task, branch := heldAt(t, ctx, store, "pkg/ui/server.go")
	humanSubmit(t, ctx, store, task.ID, "")
	pushFile(t, sim.BareRepo, branch, "pkg/ui/server.go", "agent pushed before the pin\n")
	setMergeable(sim, true)
	sim.CheckRuns[branch] = greenCheck
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Hour)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	got := reloaded(t, ctx, store, task.ID)
	if mergedBranch(sim, branch) || got.AutoMerge {
		t.Fatalf("a Submit naming no head cleared a push: merged=%v automerge=%v links=%+v",
			mergedBranch(sim, branch), got.AutoMerge, got.Links)
	}
	if reviews := securityReviewsOf(t, ctx, store, task.ID); len(reviews) != 2 {
		t.Fatalf("filed %d security reviews, want a second one for the pushed head", len(reviews))
	}
}

// A change the trust boundary does not judge -- a stacked fix, a review --
// is never classified, so a human's Submit of it names a head nothing
// will ever clear. Its marker is dropped and the cycle goes on, rather
// than waiting each cycle for a classification that never comes.
func TestHumanSubmitOfAnUnclassifiedFixIsNotStranded(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client, task, branch := heldAt(t, ctx, store, "pkg/ui/server.go")
	if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		t.Origin.Reason = model.ReasonFix
		t.Links = slices.DeleteFunc(t.Links, func(l model.Link) bool {
			return l.Kind == model.LinkTrustBoundaryCleared
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	humanSubmit(t, ctx, store, task.ID, prHeadOf(t, client, reloaded(t, ctx, store, task.ID)))
	pushFile(t, sim.BareRepo, branch, "pkg/ui/server.go", "pushed before the pin\n")
	setMergeable(sim, true)
	sim.CheckRuns[branch] = greenCheck
	for i := range 2 {
		if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Duration(i+1)*time.Hour)); err != nil {
			t.Fatalf("SyncPullRequests: %v", err)
		}
	}
	got := reloaded(t, ctx, store, task.ID)
	if linkOf(got, model.LinkHumanSubmitted) != "" || !got.AutoMerge {
		t.Fatalf("a submitted fix was left waiting on a classification: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}
	if reviews := securityReviewsOf(t, ctx, store, task.ID); len(reviews) != 1 {
		t.Fatalf("filed %d security reviews of a fix, want only the original one", len(reviews))
	}
}

// A Submit naming no head over a change grain never classified -- one
// that was not merging itself, on a repository the UI reads no head for
// -- has nothing to say which commit the human looked at, and is pinned to
// the head the cycle reads rather than held (TestConfigPullRequestStays-
// InsideTheController, which Submits a config repository change).
func TestHumanSubmitNamingNoHeadOverAnUnclassifiedChangeMerges(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	task := finishTouching(t, ctx, store, sim, "t1", "pkg/ui/server.go", false)
	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	branch := model.BranchName(task.ID)
	if got := reloaded(t, ctx, store, task.ID); got.AutoMerge || linkOf(got, model.LinkTrustBoundaryCleared) != "" {
		t.Fatalf("a change not merging itself was classified: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}
	humanSubmit(t, ctx, store, task.ID, "")
	setMergeable(sim, true)
	sim.CheckRuns[branch] = greenCheck
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Hour)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if !mergedBranch(sim, branch) {
		t.Fatalf("the submitted change did not merge: links=%+v", reloaded(t, ctx, store, task.ID).Links)
	}
	if reviews := securityReviewsOf(t, ctx, store, task.ID); len(reviews) != 0 {
		t.Fatalf("filed %d security reviews of a change a human submitted", len(reviews))
	}
}
