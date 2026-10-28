package orchestrator_test

// SyncPullRequests' own reads on the adaptive poll schedule
// (syncpoll.go): what is moving is read every cycle, what is not backs
// off, and the queue's head is read every cycle whatever the budget.
//
// Each test has a repository of its own: a merge anywhere in a
// repository resets every pull request in it, in every scope
// (github.PollSchedule.ResetRepo), and the parallel tests around these
// merge into acme/widgets all the time.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// idleTaskWithPullRequest is a finished task with an open pull request
// the merge queue does not own: no /auto-merge, so it waits on a human.
func idleTaskWithPullRequest(t *testing.T, ctx context.Context, store *model.Store,
	sim *githubsim.Sim, client *github.RESTClient, id string, repo model.RepoRef) (model.Task, string, int) {

	t.Helper()
	queued, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, id, repo)
	task, err := store.GetTask(ctx, queued.ID)
	if err != nil || task == nil {
		t.Fatalf("reading task back: %v", err)
	}
	task.AutoMerge = false
	if err := store.PutTask(ctx, *task); err != nil {
		t.Fatal(err)
	}
	ref, err := model.ParsePullRequestRef(task.Links[len(task.Links)-1].Target)
	if err != nil {
		t.Fatal(err)
	}
	return *task, branch, ref.Number
}

// pullRequestReads is how many times the sim was asked for pull request n.
func pullRequestReads(sim *githubsim.Sim, repo model.RepoRef, n int) int {
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d", repo.Owner, repo.Name, n)
	reads := 0
	for _, c := range sim.Calls {
		if c.Method == "GET" && c.Path == path {
			reads++
		}
	}
	return reads
}

// syncCycles runs SyncPullRequests once every PollBase from start, n
// times, and answers the time after the last.
func syncCycles(t *testing.T, ctx context.Context, store *model.Store, client github.Client,
	start time.Time, n int) time.Time {

	t.Helper()
	now := start
	for i := 0; i < n; i++ {
		if err := orchestrator.SyncPullRequests(ctx, store, client, now); err != nil {
			t.Fatalf("SyncPullRequests at %s: %v", now.Format(time.Kitchen), err)
		}
		now = now.Add(github.PollBase)
	}
	return now
}

// Well past the head's first sighting, a pull request whose CI is still
// running is read on every cycle, as it always was.
func TestAPullRequestWithCIInFlightIsReadEveryCycle(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "syncpoll-inflight", "main")
	repo := model.RepoRef{Owner: "acme", Name: "syncpoll-inflight"}
	_, branch, n := idleTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	setMergeable(sim, true)
	sim.CheckRuns[branch] = []github.CheckRun{{Name: "tests", Status: "in_progress"}}

	start := baseTime.Add(time.Hour)
	later := syncCycles(t, ctx, store, client, start, 1).Add(15 * time.Minute)
	before := pullRequestReads(sim, repo, n)
	syncCycles(t, ctx, store, client, later, 8)
	if got := pullRequestReads(sim, repo, n) - before; got != 8 {
		t.Fatalf("a pull request with CI running was read %d times in 8 cycles, want every cycle", got)
	}
}

// A pull request nothing is happening to is read less and less often,
// and goes back to every cycle the moment its head moves.
//
// Not parallel: the schedule is process-wide and forgets an entry nobody
// has asked about for an hour of whatever clock its caller passes, so a
// parallel test on a later clock (time.Now, months past baseTime) sweeps
// this one's backoff away mid-test and it reads as due every cycle.
func TestAnIdlePullRequestBacksOffAndResetsOnAHeadChange(t *testing.T) {
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "syncpoll-idle", "main")
	repo := model.RepoRef{Owner: "acme", Name: "syncpoll-idle"}
	_, branch, n := idleTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	setMergeable(sim, true)
	sim.CheckRuns[branch] = greenCheck

	start := baseTime.Add(time.Hour)
	// Past the recent-push window the first sighting opens.
	now := syncCycles(t, ctx, store, client, start, 1).Add(15 * time.Minute)
	before := pullRequestReads(sim, repo, n)
	now = syncCycles(t, ctx, store, client, now, 16)
	idle := pullRequestReads(sim, repo, n) - before
	if idle == 0 || idle >= 8 {
		t.Fatalf("an idle pull request was read %d times in 16 cycles, want it backed off (1..7)", idle)
	}

	// A push. The next due read sees the new head; from then on it is
	// read every cycle again.
	pushBranch(t, sim.BareRepo, branch)
	now = now.Add(github.PollCap)
	before = pullRequestReads(sim, repo, n)
	now = syncCycles(t, ctx, store, client, now, 1)
	if got := pullRequestReads(sim, repo, n) - before; got != 1 {
		t.Fatalf("a pull request idle for PollCap was read %d times on the next cycle, want 1", got)
	}
	before = pullRequestReads(sim, repo, n)
	syncCycles(t, ctx, store, client, now, 6)
	if got := pullRequestReads(sim, repo, n) - before; got != 6 {
		t.Fatalf("a pull request whose head just moved was read %d times in 6 cycles, want every cycle", got)
	}
}

// Under a low budget everything but the queue's head is held to
// PollCap -- CI in flight or not -- and the head is read every cycle.
//
// Not parallel: the budget is the process-wide schedule's.
func TestTheQueueHeadIsReadEveryCycleUnderALowBudget(t *testing.T) {
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "syncpoll-lowbudget", "main")
	repo := model.RepoRef{Owner: "acme", Name: "syncpoll-lowbudget"}
	_, head := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	_, other, otherN := idleTaskWithPullRequest(t, ctx, store, sim, client, "t2", repo)
	setMergeable(sim, true)
	running := []github.CheckRun{{Name: "tests", Status: "in_progress"}}
	sim.CheckRuns[head], sim.CheckRuns[other] = running, running
	headN := 0
	for _, pr := range sim.PullRequests {
		if pr.Head == head {
			headN = pr.Number
		}
	}

	start := baseTime.Add(time.Hour)
	now := syncCycles(t, ctx, store, client, start, 1)
	defer github.SetPollBudgetLow(func(time.Time) bool { return true })()
	headBefore, otherBefore := pullRequestReads(sim, repo, headN), pullRequestReads(sim, repo, otherN)
	syncCycles(t, ctx, store, client, now, 8)
	if got := pullRequestReads(sim, repo, headN) - headBefore; got != 8 {
		t.Fatalf("the merge queue's head was read %d times in 8 cycles on a low budget, want every cycle", got)
	}
	if got := pullRequestReads(sim, repo, otherN) - otherBefore; got != 0 {
		t.Fatalf("a pull request outside the queue was read %d times in 8 cycles (4 minutes) "+
			"on a low budget, want none before PollCap", got)
	}
}
