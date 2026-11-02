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
// merge a cycle, as model.DefaultMaxMergesPerRepo was before schema 85:
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
