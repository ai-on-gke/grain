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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// setBranchUnknown is GitHub not having worked out yet whether one
// branch's pull request merges cleanly: `mergeable` is null.
func setBranchUnknown(sim *githubsim.Sim, branch string) {
	for i := range sim.PullRequests {
		if sim.PullRequests[i].Head == branch {
			sim.PullRequests[i].Mergeable = nil
		}
	}
}

// There is no order between pull requests: a member still running its
// checks, one GitHub has not worked out the mergeability of and one that
// conflicts each hold only themselves, and the two clean members queued
// after them both merge on the same cycle.
func TestEveryCleanMemberMergesWhateverIsQueuedBeforeIt(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	var branches []string
	for _, id := range []string{"t1", "t2", "t3", "t4", "t5"} {
		_, b := queuedTaskWithPullRequest(t, ctx, store, sim, client, id, repo)
		sim.CheckRuns[b] = greenCheck
		branches = append(branches, b)
	}
	setMergeable(sim, true)
	sim.CheckRuns[branches[0]] = []github.CheckRun{{Name: "tests", Status: "in_progress"}}
	setBranchUnknown(sim, branches[1])
	setBranchMergeable(sim, branches[2], false)

	if err := orchestrator.SyncPullRequestsGuarding(ctx, store, client, nil,
		orchestrator.StateRepoGate{}, 2, baseTime); err != nil {
		t.Fatalf("SyncPullRequestsGuarding: %v", err)
	}
	for i, b := range branches[:3] {
		if mergedBranch(sim, b) {
			t.Errorf("t%d merged, but it does not pass the predicate", i+1)
		}
	}
	for i, b := range branches[3:] {
		if !mergedBranch(sim, b) {
			_, note := mergeQueueStateOf(t, ctx, store, []string{"t4", "t5"}[i])
			t.Errorf("t%d did not merge: %q", i+4, note)
		}
	}
	for _, id := range []string{"t1", "t2", "t3"} {
		if _, note := mergeQueueStateOf(t, ctx, store, id); strings.Contains(note, "behind task") {
			t.Errorf("%s's note names another change: %q", id, note)
		}
	}
}

// The cap is the one thing a clean member waits on, and its row says so,
// naming the merge that spent it. It merges on the next cycle.
func TestACleanMemberPastTheCapMergesNextCycleAndSaysSo(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	_, first := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	_, second := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t2", repo)
	setMergeable(sim, true)
	sim.CheckRuns[first] = greenCheck
	sim.CheckRuns[second] = greenCheck

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if !mergedBranch(sim, first) || mergedBranch(sim, second) {
		t.Fatalf("merged first=%v second=%v, want only the first", mergedBranch(sim, first), mergedBranch(sim, second))
	}
	state, note := mergeQueueStateOf(t, ctx, store, "t2")
	want := "merges next cycle: this cycle's 1 merge into acme/widgets main is used (task t1 merged)"
	if state != "waiting_queue" || note != want {
		t.Fatalf("t2 says %q (%q), want waiting_queue (%q)", state, note, want)
	}
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if !mergedBranch(sim, second) {
		t.Fatal("t2 did not merge on the next cycle")
	}
}

// staleAfterMerge is a GitHub whose answer about one pull request's
// mergeability goes stale when another merges: the first read of it after
// that merge still says clean, and only reading it again says what the
// moved base did to it.
type staleAfterMerge struct {
	*github.RESTClient
	number int

	mu     sync.Mutex
	merged bool
	reads  int
}

func (c *staleAfterMerge) MergePullRequest(owner, repo string, number int, headSHA string) error {
	err := c.RESTClient.MergePullRequest(owner, repo, number, headSHA)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err == nil && number != c.number {
		c.merged = true
	}
	return err
}

func (c *staleAfterMerge) GetPullRequest(owner, repo string, number int) (github.PullRequestDetail, error) {
	d, err := c.RESTClient.GetPullRequest(owner, repo, number)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err == nil && number == c.number && c.merged {
		c.reads++
		if c.reads > 1 {
			conflicted := false
			d.Mergeable = &conflicted
		}
	}
	return d, err
}

// After a merge, the next merge into the same lane on the same cycle is
// judged against the base as it now stands: its pull request is read
// again just before it merges, and a change the first merge made conflict
// does not merge on the earlier answer.
func TestTheSecondMergeInACycleReadsMergeabilityAgain(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, rest := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	_, first := queuedTaskWithPullRequest(t, ctx, store, sim, rest, "t1", repo)
	second, secondBranch := queuedTaskWithPullRequest(t, ctx, store, sim, rest, "t2", repo)
	setMergeable(sim, true)
	sim.CheckRuns[first] = greenCheck
	sim.CheckRuns[secondBranch] = greenCheck
	number := 0
	for _, pr := range sim.PullRequests {
		if pr.Head == secondBranch {
			number = pr.Number
		}
	}
	client := &staleAfterMerge{RESTClient: rest, number: number}

	if err := orchestrator.SyncPullRequestsGuarding(ctx, store, client, nil,
		orchestrator.StateRepoGate{}, 2, baseTime); err != nil {
		t.Fatalf("SyncPullRequestsGuarding: %v", err)
	}
	if !mergedBranch(sim, first) {
		t.Fatal("t1 did not merge")
	}
	if mergedBranch(sim, secondBranch) {
		t.Fatalf("t2 merged on a mergeability read from before t1's merge moved its base (%d reads after)", client.reads)
	}
	if state, note := mergeQueueStateOf(t, ctx, store, second.ID); state != "repair_needed" {
		t.Fatalf("t2 says %q (%q), want repair_needed: the re-read found it conflicted", state, note)
	}
}

// Lanes are independent: a `main` member GitHub has not worked out holds
// itself, the clean `main` change merges, `candidate` merges on its own
// cap, and the candidate change the cap holds names its own lane's merge.
func TestLanesStayIndependentWithoutOrder(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	run(t, t.TempDir(), "git", "--git-dir", sim.BareRepo, "branch", "candidate", "main")
	_, mainUnknown := queuedTaskOnBase(t, ctx, store, sim, client, "t1", repo, "main")
	_, mainClean := queuedTaskOnBase(t, ctx, store, sim, client, "t2", repo, "main")
	_, cand1 := queuedTaskOnBase(t, ctx, store, sim, client, "t3", repo, "candidate")
	_, cand2 := queuedTaskOnBase(t, ctx, store, sim, client, "t4", repo, "candidate")
	for _, b := range []string{mainUnknown, mainClean, cand1, cand2} {
		sim.CheckRuns[b] = greenCheck
	}
	setMergeable(sim, true)
	setBranchUnknown(sim, mainUnknown)

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if mergedBranch(sim, mainUnknown) {
		t.Fatal("a pull request GitHub had not worked out the mergeability of was merged")
	}
	if !mergedBranch(sim, mainClean) || !mergedBranch(sim, cand1) {
		t.Fatalf("main clean merged=%v, candidate first merged=%v, want both", mergedBranch(sim, mainClean), mergedBranch(sim, cand1))
	}
	if mergedBranch(sim, cand2) {
		t.Fatal("candidate merged twice on a cap of one")
	}
	_, note := mergeQueueStateOf(t, ctx, store, "t4")
	if note != "merges next cycle: this cycle's 1 merge into acme/widgets candidate is used (task t3 merged)" {
		t.Fatalf("t4's note = %q, want its own lane's cap", note)
	}
}

// A member's row says what it waits on and how long: GitHub's
// mergeability, or the checks that have not moved. It is never merged
// while it waits.
func TestAWaitingMemberSaysWhatItWaitsOnAndForHowLong(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	_, unknown := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	_, pending := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t2", repo)
	setMergeable(sim, true)
	setBranchUnknown(sim, unknown)
	sim.CheckRuns[unknown] = greenCheck
	sim.CheckRuns[pending] = []github.CheckRun{{Name: "cla/google", Status: "queued"}}

	for _, at := range []time.Time{baseTime, baseTime.Add(16 * time.Minute)} {
		if err := orchestrator.SyncPullRequests(ctx, store, client, at); err != nil {
			t.Fatalf("SyncPullRequests: %v", err)
		}
	}
	if mergedBranch(sim, unknown) || mergedBranch(sim, pending) {
		t.Fatal("a member that does not pass the predicate merged")
	}
	if _, note := mergeQueueStateOf(t, ctx, store, "t1"); note != "waiting for GitHub to work out whether this branch merges cleanly for 16m" {
		t.Fatalf("t1's note = %q", note)
	}
	if _, note := mergeQueueStateOf(t, ctx, store, "t2"); !strings.Contains(note, "`cla/google`") || !strings.HasSuffix(note, " for 16m") {
		t.Fatalf("t2's note = %q, want the check named and how long", note)
	}
}
