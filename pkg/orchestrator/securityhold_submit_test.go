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
)

// humanSubmit is what ui.Client.Submit writes: auto-merge on, the holds
// the change waited under gone, and the decision marked, with shown, the
// head the human was shown, for the next cycle to pin if it reads that
// head.
func humanSubmit(t *testing.T, ctx context.Context, store *model.Store, id, shown string) {
	t.Helper()
	if err := store.UpdateTask(ctx, id, func(tk *model.Task) error {
		model.AllowAutoMerge(tk)
		model.DropHumanReviewHold(tk)
		tk.Links = slices.DeleteFunc(tk.Links, func(l model.Link) bool {
			return l.Kind == model.LinkHumanOnlyHold || l.Kind == model.LinkBrokenRulesHold ||
				l.Kind == model.LinkHumanSubmitted
		})
		tk.Links = append(tk.Links, model.Link{Kind: model.LinkHumanSubmitted, Target: model.HumanSubmittedTarget(baseTime, shown)})
		if !model.HasPendingReviewHold(*tk) {
			tk.AutoMerge = true
		}
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

	// The security review fixes an auxiliary file in place on the branch and completes cleanly.
	pushFile(t, sim.BareRepo, branch, "pkg/thing/thing.go", "fixed by the review\n")
	shipped := prHeadOf(t, client, reloaded(t, ctx, store, task.ID))
	reviewID := linkOf(reloaded(t, ctx, store, task.ID), model.LinkSecurityReviewTask)
	if err := store.UpdateTask(ctx, reviewID, func(tk *model.Task) error {
		tk.Links = append(tk.Links, model.Link{Kind: model.LinkReviewerHead, Target: shipped})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Human submits before SyncSecurityHolds runs: does NOT merge yet while security hold stands.
	humanSubmit(t, ctx, store, task.ID, shipped)
	completeReview(t, ctx, store, reviewID)
	if err := orchestrator.SyncSecurityHolds(ctx, store, client, baseTime.Add(35*time.Minute)); err != nil {
		t.Fatal(err)
	}

	// main moves on, and CI on the shipped head is red against the older
	// main: the queue merges main in and carries the clearance without a second review.
	pushFileToBranch(t, sim.BareRepo, "main", "MAIN.md", "unrelated work", "unrelated change on main")
	setMergeable(sim, true)
	sim.CheckRuns[branch] = []github.CheckRun{{Name: "go", Status: "completed", Conclusion: strPtr("failure")}}
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Hour)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	got := reloaded(t, ctx, store, task.ID)
	if !got.AutoMerge || linkOf(got, model.LinkHumanSubmitted) != "" {
		t.Fatalf("automerge=%v links=%+v, want auto-merge on and the Submit consumed", got.AutoMerge, got.Links)
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

// A change touching a human-only path requires BOTH the security review and a
// human's Submit; once both gates have passed, a base merge carries both
// without filing another review.
func TestHumanSubmitOverAHumanOnlyHoldPinsNoClearance(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client, task, branch := heldAt(t, ctx, store, ".grain/quarantine.yml")
	reviewID := linkOf(reloaded(t, ctx, store, task.ID), model.LinkSecurityReviewTask)
	humanSubmit(t, ctx, store, task.ID, prHeadOf(t, client, reloaded(t, ctx, store, task.ID)))
	completeReview(t, ctx, store, reviewID)
	if err := orchestrator.SyncSecurityHolds(ctx, store, client, baseTime.Add(35*time.Minute)); err != nil {
		t.Fatal(err)
	}

	pushFileToBranch(t, sim.BareRepo, "main", "MAIN.md", "unrelated work", "unrelated change on main")
	setMergeable(sim, true)
	sim.CheckRuns[branch] = []github.CheckRun{{Name: "go", Status: "completed", Conclusion: strPtr("failure")}}
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Hour)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	got := reloaded(t, ctx, store, task.ID)
	if !got.AutoMerge || linkOf(got, model.LinkHumanSubmitted) != "" {
		t.Fatalf("automerge=%v links=%+v, want auto-merge on and no pending submit marker", got.AutoMerge, got.Links)
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
	task := finishTouching(t, ctx, store, sim, "t1", "README.md", false)
	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	branch := model.BranchName(task.ID)
	if got := reloaded(t, ctx, store, task.ID); got.AutoMerge {
		t.Fatalf("a change not merging itself got auto-merge on: links=%+v", got.Links)
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
		t.Fatalf("filed %d security reviews of a non-trust-boundary change", len(reviews))
	}
}
