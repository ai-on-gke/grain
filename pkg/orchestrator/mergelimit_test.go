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
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// syncOneMergePerCycle is SyncPullRequests with each lane held to one
// merge a cycle, as model.DefaultMaxMergesPerRepo was before schema 95:
// for a test about what the cycle after a merge does with the entries
// left behind it.
func syncOneMergePerCycle(ctx context.Context, store *model.Store, client github.Client, now time.Time) error {
	return orchestrator.SyncPullRequestsGuarding(ctx, store, client, nil, orchestrator.StateRepoGate{}, 1, now)
}

// With nothing configured a lane takes model.DefaultMaxMergesPerRepo
// merges a cycle, and three clean members are fewer than that.
func TestThreeCleanMembersOfOneLaneAllMergeInOneCycleByDefault(t *testing.T) {
	t.Parallel()
	if model.DefaultMaxMergesPerRepo < 3 {
		t.Fatalf("DefaultMaxMergesPerRepo = %d; this test is about a default of at least 3", model.DefaultMaxMergesPerRepo)
	}
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	var tasks []model.Task
	var branches []string
	for _, id := range []string{"t1", "t2", "t3"} {
		task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, id, repo)
		tasks, branches = append(tasks, task), append(branches, branch)
		sim.CheckRuns[branch] = greenCheck
	}
	setMergeable(sim, true)

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	for i, branch := range branches {
		if !mergedBranch(sim, branch) {
			t.Errorf("%s did not merge in the cycle", tasks[i].ID)
			continue
		}
		if st, err := store.State(ctx, tasks[i].ID); err != nil || st != model.StateClosed {
			t.Errorf("%s is %q (%v), want closed: its pull request merged", tasks[i].ID, st, err)
		}
	}
}

// Raising the limit spends more of a lane's merges; it does not open a
// lane the walk has closed. A merge that ends in an error leaves the base in
// a state nothing this cycle has read, so the lane closes behind it
// (mergeWalk.closeEntry), and the clean members after it wait for the next
// cycle even with merges left.
func TestAMemberBehindAClosedLaneStillWaits(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, rest := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	_, erroredBranch := queuedTaskWithPullRequest(t, ctx, store, sim, rest, "t1", repo)
	second, secondBranch := queuedTaskWithPullRequest(t, ctx, store, sim, rest, "t2", repo)
	third, thirdBranch := queuedTaskWithPullRequest(t, ctx, store, sim, rest, "t3", repo)
	setMergeable(sim, true)
	for _, b := range []string{erroredBranch, secondBranch, thirdBranch} {
		sim.CheckRuns[b] = greenCheck
	}
	client := &mergeMovesBase{RESTClient: rest, sim: sim, fail: map[string]bool{erroredBranch: true}}

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err == nil {
		t.Fatal("SyncPullRequests reported no error for a merge that failed")
	}
	for _, b := range []string{erroredBranch, secondBranch, thirdBranch} {
		if mergedBranch(sim, b) {
			t.Fatalf("%s merged into a lane closed by a failed merge", b)
		}
	}
	for _, task := range []model.Task{second, third} {
		if st, err := store.State(ctx, task.ID); err != nil || st != model.StateCompleted {
			t.Fatalf("%s is %q (%v), want still completed: it waits for the next cycle", task.ID, st, err)
		}
	}

	client.fail = nil
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("second SyncPullRequests: %v", err)
	}
	for _, b := range []string{erroredBranch, secondBranch, thirdBranch} {
		if !mergedBranch(sim, b) {
			t.Errorf("%s did not merge once the lane reopened", b)
		}
	}
}

// mergeMovesBase is a client whose merges change what GitHub says about
// the other pull requests afterwards, the way a real merge does: GitHub
// computes mergeability against the base as it stands, so a merge that
// moves the base changes the answer for everything still open on it.
type mergeMovesBase struct {
	*github.RESTClient
	sim          *githubsim.Sim
	afterMerging map[string]func()
	// refuse is the branches GitHub refuses to merge, as it does a pull
	// request whose clean read was about a base that has since moved.
	refuse map[string]bool
	// fail is the branches whose merge call errors with GitHub having a
	// bad minute, so nobody can say whether the merge happened.
	fail map[string]bool
}

func (c *mergeMovesBase) MergePullRequest(owner, repo string, number int, headSHA string) error {
	for _, pr := range c.sim.PullRequests {
		if pr.Number == number && c.refuse[pr.Head] {
			return &github.Error{Status: 405, Body: []byte(`{"message":"Pull Request is not mergeable"}`)}
		}
		if pr.Number == number && c.fail[pr.Head] {
			return &github.Error{Status: 502, Body: []byte(`{"message":"Server Error"}`)}
		}
	}
	if err := c.RESTClient.MergePullRequest(owner, repo, number, headSHA); err != nil {
		return err
	}
	for _, pr := range c.sim.PullRequests {
		if pr.Number == number {
			if after := c.afterMerging[pr.Head]; after != nil {
				after()
			}
		}
	}
	return nil
}

// Several merges in one cycle are each judged against the base the merge
// before them left, not against a read taken before the cycle's first
// merge: a member of a lane that has already had a merge this cycle is
// read again just before its own (syncEntry's merge arm). Here the first
// merge leaves the second conflicted and the third with GitHub still
// working out the answer, and neither merges, though both read clean
// before the first merge and the lane has slots to spare.
func TestEachMergeInACycleIsJudgedAgainstTheBaseThePreviousMergeLeft(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, rest := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	_, firstBranch := queuedTaskWithPullRequest(t, ctx, store, sim, rest, "t1", repo)
	second, secondBranch := queuedTaskWithPullRequest(t, ctx, store, sim, rest, "t2", repo)
	third, thirdBranch := queuedTaskWithPullRequest(t, ctx, store, sim, rest, "t3", repo)
	setMergeable(sim, true)
	for _, b := range []string{firstBranch, secondBranch, thirdBranch} {
		sim.CheckRuns[b] = greenCheck
	}
	client := &mergeMovesBase{RESTClient: rest, sim: sim, afterMerging: map[string]func(){
		firstBranch: func() {
			setBranchMergeable(sim, secondBranch, false)
			for i := range sim.PullRequests {
				if sim.PullRequests[i].Head == thirdBranch {
					sim.PullRequests[i].Mergeable = nil
				}
			}
		},
	}}

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if !mergedBranch(sim, firstBranch) {
		t.Fatal("the first clean member did not merge")
	}
	if mergedBranch(sim, secondBranch) {
		t.Fatal("merged a change that conflicts with the base the cycle's first merge left")
	}
	if mergedBranch(sim, thirdBranch) {
		t.Fatal("merged a change GitHub had not yet judged against the moved base")
	}
	// The conflicted one goes to its repair; the unknown one is judged
	// again next cycle.
	if !repairInFlightNow(t, ctx, store, second.ID) {
		t.Error("the change the first merge left conflicted was not sent back for repair")
	}
	if st, err := store.State(ctx, third.ID); err != nil || st != model.StateCompleted {
		t.Errorf("t3 is %q (%v), want still completed: it is judged again next cycle", st, err)
	}
}

// GitHub re-judges a pull request against a moved base in the background,
// so the read the walk takes straight after a merge can still be the
// answer about the base before it, and the merge it asks for is refused.
// That is the base moving, not the change failing: the cycle reports no
// error, the refused change holds only itself (the base did not move, so
// the rest of the lane still merges), and it merges next cycle with no
// refusal held against it.
func TestARefusalStraightAfterAMergeInTheLaneWaitsForTheNextCycle(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, rest := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	_, firstBranch := queuedTaskWithPullRequest(t, ctx, store, sim, rest, "t1", repo)
	second, secondBranch := queuedTaskWithPullRequest(t, ctx, store, sim, rest, "t2", repo)
	_, thirdBranch := queuedTaskWithPullRequest(t, ctx, store, sim, rest, "t3", repo)
	setMergeable(sim, true)
	for _, b := range []string{firstBranch, secondBranch, thirdBranch} {
		sim.CheckRuns[b] = greenCheck
	}
	client := &mergeMovesBase{RESTClient: rest, sim: sim, refuse: map[string]bool{secondBranch: true}}

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if !mergedBranch(sim, firstBranch) {
		t.Fatal("the first clean member did not merge")
	}
	if mergedBranch(sim, secondBranch) {
		t.Fatal("the refused change merged")
	}
	if !mergedBranch(sim, thirdBranch) {
		t.Fatal("a refusal that left the base as it was held up the clean change after it")
	}
	if st, err := store.State(ctx, second.ID); err != nil || st != model.StateCompleted {
		t.Fatalf("t2 is %q (%v), want still completed", st, err)
	}

	client.refuse = nil
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("second SyncPullRequests: %v", err)
	}
	if !mergedBranch(sim, secondBranch) {
		t.Fatal("the next cycle did not merge the change refused before")
	}
}

// A 5xx or transport failure on a lane's second merge is not a refusal that
// left the base as it was: GitHub may have applied the merge before failing,
// so the cycle must still report the error and close the lane behind it rather
// than letting later members merge into an unknown base.
func TestAMergeErrorAfterAnEarlierMergeStillClosesTheLane(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, rest := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	_, firstBranch := queuedTaskWithPullRequest(t, ctx, store, sim, rest, "t1", repo)
	second, secondBranch := queuedTaskWithPullRequest(t, ctx, store, sim, rest, "t2", repo)
	third, thirdBranch := queuedTaskWithPullRequest(t, ctx, store, sim, rest, "t3", repo)
	setMergeable(sim, true)
	for _, b := range []string{firstBranch, secondBranch, thirdBranch} {
		sim.CheckRuns[b] = greenCheck
	}
	client := &mergeMovesBase{RESTClient: rest, sim: sim, fail: map[string]bool{secondBranch: true}}

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err == nil {
		t.Fatal("SyncPullRequests swallowed a 502 on the lane's second merge")
	}
	if !mergedBranch(sim, firstBranch) {
		t.Fatal("the first clean member did not merge")
	}
	if mergedBranch(sim, secondBranch) {
		t.Fatal("the failed second merge landed")
	}
	if mergedBranch(sim, thirdBranch) {
		t.Fatal("the third member merged into a lane whose second merge ended in a 502")
	}
	for _, task := range []model.Task{second, third} {
		if st, err := store.State(ctx, task.ID); err != nil || st != model.StateCompleted {
			t.Fatalf("%s is %q (%v), want still completed", task.ID, st, err)
		}
	}

	client.fail = nil
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("second SyncPullRequests: %v", err)
	}
	for _, b := range []string{secondBranch, thirdBranch} {
		if !mergedBranch(sim, b) {
			t.Errorf("%s did not merge once the lane reopened", b)
		}
	}
}

// Classic branch protection answers a missing approving review with the same
// bare 405 ("Pull Request is not mergeable") as a moved base. Even straight
// after an earlier merge in the lane, a refusal that the branch's protection
// rules place as an approval wait enters waiting_github_approval at once.
func TestAGitHubApprovalRefusalAfterAnEarlierMergeEntersApprovalWait(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, rest := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	_, firstBranch := queuedTaskWithPullRequest(t, ctx, store, sim, rest, "t1", repo)
	second, secondBranch := queuedTaskWithPullRequest(t, ctx, store, sim, rest, "t2", repo)
	_, thirdBranch := queuedTaskWithPullRequest(t, ctx, store, sim, rest, "t3", repo)
	setMergeable(sim, true)
	for _, b := range []string{firstBranch, secondBranch, thirdBranch} {
		sim.CheckRuns[b] = greenCheck
	}
	firstPR, thirdPR := prNumber(t, sim, firstBranch), prNumber(t, sim, thirdBranch)
	sim.Approvals = map[int][]string{firstPR: {"alice"}, thirdPR: {"alice"}}
	sim.BranchRules = map[string][]map[string]any{"main": {{
		"type": "pull_request", "ruleset_id": 7,
		"parameters": map[string]any{"required_approving_review_count": 1},
	}}}
	sim.RulesetNames = map[int]string{7: "protect main"}
	client := &mergeMovesBase{RESTClient: rest, sim: sim, refuse: map[string]bool{secondBranch: true}}

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if !mergedBranch(sim, firstBranch) || !mergedBranch(sim, thirdBranch) {
		t.Fatal("the approved members did not merge")
	}
	obs := mergeQueueStatus(t, ctx, store, second.ID)
	if obs.MergeQueueState != "waiting_github_approval" {
		t.Fatalf("t2 state = %q (%q), want waiting_github_approval", obs.MergeQueueState, obs.MergeQueueNote)
	}
}

// A non-member merge acted on first in the cycle (for example, a pull request
// previously waiting on a GitHub approval that has now arrived) still moves
// the lane's base before the queue members run, so a queue member refused
// straight after it is treated as the base moving rather than charged a
// refusal.
func TestANonMemberMergeInTheLaneMarksTheBaseMovedForMembersBehindIt(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, rest := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	first, firstBranch := queuedTaskWithPullRequest(t, ctx, store, sim, rest, "t1", repo)
	second, secondBranch := queuedTaskWithPullRequest(t, ctx, store, sim, rest, "t2", repo)
	setMergeable(sim, true)
	for _, b := range []string{firstBranch, secondBranch} {
		sim.CheckRuns[b] = greenCheck
	}
	firstPR, secondPR := prNumber(t, sim, firstBranch), prNumber(t, sim, secondBranch)
	sim.Approvals = map[int][]string{secondPR: {"alice"}}
	sim.BranchRules = map[string][]map[string]any{"main": {{
		"type": "pull_request", "ruleset_id": 7,
		"parameters": map[string]any{"required_approving_review_count": 1},
	}}}
	sim.RulesetNames = map[int]string{7: "protect main"}
	client := &mergeMovesBase{RESTClient: rest, sim: sim, refuse: map[string]bool{firstBranch: true}}

	// Cycle 1: t1 enters waiting_github_approval (leaving queue membership);
	// hold t2 to Pending so it stays in the queue for cycle 2.
	sim.CheckRuns[secondBranch] = []github.CheckRun{{Name: "tests", Status: "in_progress"}}
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("first SyncPullRequests: %v", err)
	}
	if obs := mergeQueueStatus(t, ctx, store, first.ID); obs.MergeQueueState != "waiting_github_approval" {
		t.Fatalf("t1 state = %q, want waiting_github_approval", obs.MergeQueueState)
	}

	// Cycle 2: t1 gets its approval (acted on first as a non-member) and
	// merges, moving main; t2 (a queue member) is transiently refused right
	// after t1's merge and waits cleanly with no refusal recorded.
	sim.Approvals[firstPR] = []string{"alice"}
	sim.CheckRuns[secondBranch] = greenCheck
	client.refuse = map[string]bool{secondBranch: true}
	later := baseTime.Add(3 * time.Minute)
	if err := orchestrator.SyncPullRequests(ctx, store, client, later); err != nil {
		t.Fatalf("second SyncPullRequests: %v", err)
	}
	if !mergedBranch(sim, firstBranch) {
		t.Fatal("the newly approved non-member did not merge")
	}
	if mergedBranch(sim, secondBranch) {
		t.Fatal("the refused member merged")
	}
	if obs := mergeQueueStatus(t, ctx, store, second.ID); obs.MergeQueueState != "waiting_queue" {
		t.Fatalf("t2 state = %q (%q), want waiting_queue", obs.MergeQueueState, obs.MergeQueueNote)
	}

	client.refuse = nil
	if err := orchestrator.SyncPullRequests(ctx, store, client, later.Add(10*time.Second)); err != nil {
		t.Fatalf("third SyncPullRequests: %v", err)
	}
	if !mergedBranch(sim, secondBranch) {
		t.Fatal("t2 did not merge on the next cycle: a refusal was held against it despite the non-member merge moving main")
	}
}
