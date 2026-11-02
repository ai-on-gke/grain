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
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// What grain CI asks so that it can spend the deployment's sandboxes on
// the changes whose verdict turns into a merge (ci.PriorityMergeHead,
// grain/task-316). Members of a lane merge independently of each other
// (grain/task-2346), so every member that is otherwise ready ranks the
// same: the one filed second merges on its own green just as soon as the
// one filed first.
func TestMergeQueueHeadsNamesEveryMemberOfTheQueue(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	_, first := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	_, second := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t2", repo)

	heads, err := orchestrator.MergeQueueHeads(ctx, store)
	if err != nil {
		t.Fatalf("MergeQueueHeads: %v", err)
	}
	if first == second {
		t.Fatal("the two tasks are on the same branch, so this proves nothing")
	}
	if got := heads[repo.String()]; !got[first] || !got[second] || len(got) != 2 {
		t.Errorf("heads of the %s queue = %v, want both members (%q, %q)", repo, got, first, second)
	}
	if len(heads) != 1 {
		t.Errorf("one repository has a queue, so one entry: got %v", heads)
	}
}

// A member something besides CI is holding -- an approval nobody has
// given, a repair still being worked on its branch -- merges nothing when
// its CI goes green, so its jobs do not go ahead of a member's that does.
func TestMergeQueueHeadsLeavesOutAMemberHeldForSomethingElse(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	approval, approvalBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	repairing, repairingBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t2", repo)
	_, ready := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t3", repo)

	if err := store.UpdateTask(ctx, approval.ID, func(task *model.Task) error {
		task.Links = append(task.Links, model.Link{Kind: model.LinkApprovalRequired, Target: "pkg/secret/=alice"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	repairAsked := baseTime.Add(time.Hour)
	if err := store.Observe(ctx, model.Observation{
		TaskID: repairing.ID, CompletedAt: &baseTime, MergeQueueRepairAt: &repairAsked,
	}); err != nil {
		t.Fatal(err)
	}
	if obs, err := store.GetObservation(ctx, repairing.ID); err != nil || obs == nil || !obs.RepairInFlight() {
		t.Fatalf("the repairing member's observation = %+v (%v), want a repair in flight", obs, err)
	}

	heads, err := orchestrator.MergeQueueHeads(ctx, store)
	if err != nil {
		t.Fatalf("MergeQueueHeads: %v", err)
	}
	if got := heads[repo.String()]; !got[ready] || len(got) != 1 {
		t.Errorf("heads of the %s queue = %v, want only %q: %q awaits an approval and %q a repair",
			repo, got, ready, approvalBranch, repairingBranch)
	}
}

// A change the queue has given up on is nobody's head -- the same
// isQueueMember rule the reconciler is under -- so the entry behind it is
// the one whose CI is worth going first.
func TestMergeQueueHeadsPassesOverAChangeTheQueueHasGivenUpOn(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	blocked, blockedBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	_, behind := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t2", repo)

	if err := store.Observe(ctx, model.Observation{
		TaskID: blocked.ID, CompletedAt: &baseTime, MergeQueueBlockedAt: &baseTime,
	}); err != nil {
		t.Fatal(err)
	}

	heads, err := orchestrator.MergeQueueHeads(ctx, store)
	if err != nil {
		t.Fatalf("MergeQueueHeads: %v", err)
	}
	if got := heads[repo.String()]; !got[behind] || len(got) != 1 {
		t.Errorf("heads of the %s queue = %v, want %q -- %q is blocked and merges nothing",
			repo, got, behind, blockedBranch)
	}
}

// Nothing queued is no answer rather than an empty one: a deployment with
// an idle queue ranks every CI run the same.
func TestMergeQueueHeadsReportsNothingForARepoWithNothingQueued(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	heads, err := orchestrator.MergeQueueHeads(ctx, store)
	if err != nil {
		t.Fatalf("MergeQueueHeads: %v", err)
	}
	if len(heads) != 0 {
		t.Errorf("heads = %v, want none", heads)
	}
}

// Each base branch is a queue of its own (grain/task-3340), so a
// repository merging into `main` and `candidate` at once has members on
// each -- and every one of their verdicts unblocks a merge.
func TestMergeQueueHeadsNamesAHeadForEveryBaseBranch(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	run(t, t.TempDir(), "git", "--git-dir", sim.BareRepo, "branch", "candidate", "main")
	_, mainHead := queuedTaskOnBase(t, ctx, store, sim, client, "t1", repo, "main")
	_, mainBehind := queuedTaskOnBase(t, ctx, store, sim, client, "t2", repo, "main")
	_, candidateHead := queuedTaskOnBase(t, ctx, store, sim, client, "t3", repo, "candidate")

	heads, err := orchestrator.MergeQueueHeads(ctx, store)
	if err != nil {
		t.Fatalf("MergeQueueHeads: %v", err)
	}
	got := heads[repo.String()]
	if !got[mainHead] || !got[candidateHead] || !got[mainBehind] || len(got) != 3 {
		t.Errorf("heads of %s = %v, want %q and %q (main) and %q (candidate)",
			repo, got, mainHead, mainBehind, candidateHead)
	}
}

func TestMergeQueueHeadsIncludesBaseRepairsAndSharedFaultOwners(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	t1, first := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	t2, second := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t2", repo)

	// Mark an open approved FixesBaseBranch task even before it has opened a PR.
	repair := filedTask(t, ctx, store, "fix-main", repo)
	model.SetFixesBaseBranch(&repair, true)
	if err := store.PutTask(ctx, repair); err != nil {
		t.Fatal(err)
	}

	// Mark t1 as waiting on t2 (e.g. t2 is the fault owner).
	if err := store.UpdateTask(ctx, t1.ID, func(task *model.Task) error {
		task.Tags = append(task.Tags, model.BaseRepairWaitTag(t2.ID))
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	heads, err := orchestrator.MergeQueueHeads(ctx, store)
	if err != nil {
		t.Fatalf("MergeQueueHeads: %v", err)
	}
	got := heads[repo.String()]
	for _, wantBranch := range []string{first, second, repair.BranchName()} {
		if !got[wantBranch] {
			t.Errorf("heads of %s = %v, missing %q", repo, got, wantBranch)
		}
	}

	// Once t2 becomes merge-blocked, even before a sync cleans up t1's stale
	// base-ci-wait:t2 tag, t2 is no longer granted merge-head priority.
	if err := store.Observe(ctx, model.Observation{
		TaskID: t2.ID, CompletedAt: &baseTime, MergeQueueBlockedAt: &baseTime,
	}); err != nil {
		t.Fatal(err)
	}
	heads, err = orchestrator.MergeQueueHeads(ctx, store)
	if err != nil {
		t.Fatalf("MergeQueueHeads after block: %v", err)
	}
	if heads[repo.String()][second] {
		t.Errorf("blocked fault owner %q should not be in heads: %v", second, heads[repo.String()])
	}
}
