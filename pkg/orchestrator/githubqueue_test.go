package orchestrator_test

import (
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
)

// A base branch whose rules only take changes through GitHub's merge
// queue (google/xmc's "Changes must be made through the merge queue")
// is not a dead end: the pull request is added to GitHub's queue, the row
// says so, the merge is not asked for again while it waits, the change
// behind it keeps its place, and the task closes out once GitHub's queue
// merges it.
func TestAMergeQueueRuleHandsThePullRequestToGitHubsQueue(t *testing.T) {
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "queued", "main")
	repo := model.RepoRef{Owner: "acme", Name: "queued"}
	first, firstBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	second, secondBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t2", repo)

	setMergeable(sim, true)
	sim.CheckRuns[firstBranch] = greenChecks()
	sim.CheckRuns[secondBranch] = greenChecks()
	sim.RequireMergeQueue = true
	firstPR := prNumber(t, sim, firstBranch)

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if len(sim.MergeQueue) != 1 || sim.MergeQueue[0] != firstPR {
		t.Fatalf("GitHub's merge queue = %v, want just #%d", sim.MergeQueue, firstPR)
	}
	obs := mergeQueueStatus(t, ctx, store, first.ID)
	if obs.MergeQueueState != "github_merge_queue" || !strings.Contains(obs.MergeQueueNote, "GitHub's merge queue") {
		t.Fatalf("state = %q (%q), want github_merge_queue", obs.MergeQueueState, obs.MergeQueueNote)
	}
	if obs.MergeQueueBlockedAt != nil {
		t.Fatal("a change GitHub's queue is landing was handed to a person as a failure")
	}

	// While it waits nothing is merged directly, the entry keeps its
	// repository's slot, and GitHub is only polled.
	merges := mergeCalls(sim)
	for _, at := range []time.Duration{30 * time.Second, 3 * time.Minute, 6 * time.Minute} {
		if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(at)); err != nil {
			t.Fatalf("SyncPullRequests: %v", err)
		}
	}
	if got := mergeCalls(sim); got != merges {
		t.Fatalf("the queue asked GitHub for %d direct merges of a pull request in GitHub's merge queue", got-merges)
	}
	if got := sim.Enqueues; got != 1 {
		t.Fatalf("enqueued %d times, want once", got)
	}
	if len(sim.MergeQueue) != 1 {
		t.Fatalf("the change behind was queued past the one GitHub is landing: %v", sim.MergeQueue)
	}
	if state := mergeQueueStatus(t, ctx, store, first.ID).MergeQueueState; state != "github_merge_queue" {
		t.Fatalf("state while waiting = %q", state)
	}

	// GitHub's queue merges it: the task closes out, and the change behind
	// goes to GitHub's queue in its turn.
	if err := sim.LandMergeQueue(); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3 && len(sim.MergeQueue) == 0; i++ {
		if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(10*time.Minute+time.Duration(i)*10*time.Second)); err != nil {
			t.Fatalf("SyncPullRequests: %v", err)
		}
	}
	if obs := mergeQueueStatus(t, ctx, store, first.ID); obs.PrMergedAt == nil {
		t.Fatalf("the task was not closed out once GitHub's queue merged it: %q (%q)", obs.MergeQueueState, obs.MergeQueueNote)
	}
	if len(sim.MergeQueue) != 1 || sim.MergeQueue[0] != prNumber(t, sim, secondBranch) {
		t.Fatalf("GitHub's merge queue = %v, want the second change in it", sim.MergeQueue)
	}
	_ = second
}

// GitHub's queue can drop a pull request without merging it. The queue
// puts it back, a bounded number of times, and then reports the merge as
// refused rather than queuing it forever.
func TestAPullRequestGitHubsQueueKeepsDroppingIsRequeuedThenReported(t *testing.T) {
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "dropped", "main")
	repo := model.RepoRef{Owner: "acme", Name: "dropped"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)

	setMergeable(sim, true)
	sim.CheckRuns[branch] = greenChecks()
	sim.RequireMergeQueue = true
	pr := prNumber(t, sim, branch)

	at := baseTime
	for i := 0; i < 8; i++ {
		if err := orchestrator.SyncPullRequests(ctx, store, client, at); err != nil && i == 0 {
			t.Fatalf("SyncPullRequests: %v", err)
		}
		sim.DropFromMergeQueue(pr)
		at = at.Add(3 * time.Minute)
	}
	if got := sim.Enqueues; got != 3 {
		t.Fatalf("enqueued %d times, want 3", got)
	}
	obs := mergeQueueStatus(t, ctx, store, task.ID)
	if obs.MergeQueueState != "merge_failed" || !strings.Contains(obs.MergeQueueNote, "without merging it") {
		t.Fatalf("state = %q (%q), want merge_failed naming the drops", obs.MergeQueueState, obs.MergeQueueNote)
	}
}
