package orchestrator_test

import (
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
)

// What grain CI asks so that it can spend the deployment's sandboxes on
// the change whose verdict unblocks everything behind it
// (ci.PriorityMergeHead, grain/task-316). The answer has to be the queue's
// own order rather than a second opinion about it, which is why it is
// computed from the same two functions SyncPullRequests uses.
func TestMergeQueueHeadsNamesTheBranchAtTheFrontOfTheQueue(t *testing.T) {
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
	if got := heads[repo.String()]; !got[first] || len(got) != 1 {
		t.Errorf("heads of the %s queue = %v, want only the branch filed first (%q)", repo, got, first)
	}
	if first == second {
		t.Fatal("the two tasks are on the same branch, so this proves nothing")
	}
	if len(heads) != 1 {
		t.Errorf("one repository has a queue, so one head: got %v", heads)
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
// repository merging into `main` and `candidate` at once has a head on
// each -- and both of their verdicts unblock somebody.
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
	if !got[mainHead] || !got[candidateHead] || got[mainBehind] || len(got) != 2 {
		t.Errorf("heads of %s = %v, want %q (main) and %q (candidate) and nothing else",
			repo, got, mainHead, candidateHead)
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
