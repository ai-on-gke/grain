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

// Merge queue lanes (grain/task-3340): each base branch of a repository is
// a queue of its own, so a change for one branch still waiting on its CI
// holds nothing queued for another.
package orchestrator_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// queuedTaskOnBase is queuedTaskWithPullRequest for a task filed against
// base rather than the repository's default branch, which is what a port
// to `main` or a change for `candidate` is.
func queuedTaskOnBase(t *testing.T, ctx context.Context, store *model.Store,
	sim *githubsim.Sim, client *github.RESTClient, id string, repo model.RepoRef, base string) (model.Task, string) {

	t.Helper()
	task := filedTask(t, ctx, store, id, repo)
	task.AutoMerge = true
	task.CreatedAt = &baseTime
	task.Base = base
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	branch := model.BranchName(task.ID)
	pushBranch(t, sim.BareRepo, branch)
	pr, err := orchestrator.EnsurePullRequest(ctx, store, client, task, baseTime)
	if err != nil {
		t.Fatal(err)
	}
	task.Links = append(task.Links, model.Link{
		Kind: model.LinkFixes, Target: model.PullRequestRef{Repo: repo, Number: pr.Number}.String(),
	})
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := store.Observe(ctx, model.Observation{TaskID: task.ID, CompletedAt: &baseTime}); err != nil {
		t.Fatal(err)
	}
	return task, branch
}

// mainHeadInCIBeforeCandidates is Oct 12's queue in miniature: a `main`
// change at the very front whose grain CI is still running -- a freshly
// filed port -- and three green `candidate` changes behind it.
func mainHeadInCIBeforeCandidates(t *testing.T, ctx context.Context, store *model.Store,
	fix *simFixture, runner *fakeCI) (string, []string) {

	t.Helper()
	run(t, t.TempDir(), "git", "--git-dir", fix.sim.BareRepo, "branch", "candidate", "main")
	port, portBranch := queuedTaskOnBase(t, ctx, store, fix.sim, fix.client, "t1", fix.repo, "main")
	port = grantCI(t, ctx, store, port)
	startRequiredCI(t, ctx, store, fix.client, runner, port, fix.repo, portBranch)
	var candidates []string
	for _, id := range []string{"t2", "t3", "t4"} {
		task, branch := queuedTaskOnBase(t, ctx, store, fix.sim, fix.client, id, fix.repo, "candidate")
		task = grantCI(t, ctx, store, task)
		greenGrainCI(t, ctx, store, fix.client, runner, task, fix.repo, branch)
		candidates = append(candidates, branch)
	}
	for _, pr := range fix.sim.PullRequests {
		if pr.Head == portBranch && pr.Base != "main" {
			t.Fatalf("the port's pull request is against %q, want main", pr.Base)
		}
		for _, c := range candidates {
			if pr.Head == c && pr.Base != "candidate" {
				t.Fatalf("%s's pull request is against %q, want candidate", c, pr.Base)
			}
		}
	}
	setMergeable(fix.sim, true)
	return portBranch, candidates
}

// The merge slot a still-deciding `main` head closes is `main`'s, and the
// first `candidate` change merges on the same cycle -- and only the first
// when the per-cycle lane limit applies, since within a lane the slot and
// its FIFO are what they always were.
func TestACandidateMergesBehindAMainHeadStillInCIWithBatchingOff(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	fix := newBatchFixture(t)
	runner := newFakeCI()
	portBranch, candidates := mainHeadInCIBeforeCandidates(t, ctx, store, fix, runner)
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: fix.repo, MergeRecheck: model.MergeRecheckOff}); err != nil {
		t.Fatal(err)
	}

	syncWithCIOneMergePerCycle(t, ctx, store, fix.client, runner, baseTime.Add(time.Minute))

	if mergedBranch(fix.sim, portBranch) {
		t.Fatal("the main change merged while its own CI was still running")
	}
	if !mergedBranch(fix.sim, candidates[0]) {
		t.Fatal("the candidate head did not merge: the main head's slot was closed for candidate too")
	}
	for _, c := range candidates[1:] {
		if mergedBranch(fix.sim, c) {
			t.Fatalf("%s merged on the same cycle as the candidate head: one merge per lane per cycle", c)
		}
	}

	// The row behind it names its own lane's head, never the main change
	// it was never waiting for.
	state, note := mergeQueueStateOf(t, ctx, store, "t3")
	if state != "waiting_queue" {
		t.Fatalf("t3 says %q (%q), want waiting_queue", state, note)
	}
	if !strings.Contains(note, "behind task t2 in the acme/widgets candidate queue") {
		t.Fatalf("t3's note does not name the candidate head: %q", note)
	}
	if strings.Contains(note, "t1") {
		t.Fatalf("t3's note names the main change: %q", note)
	}
}

// unreadablePR is a GitHub that cannot answer for one pull request.
type unreadablePR struct {
	github.Client
	number int
}

func (c unreadablePR) GetPullRequest(owner, repo string, number int) (github.PullRequestDetail, error) {
	if number == c.number {
		return github.PullRequestDetail{}, errors.New("GitHub is having a moment")
	}
	return c.Client.GetPullRequest(owner, repo, number)
}

// The same with the `main` head unreadable rather than deciding: a pull
// request nobody could read is one nobody on its branch may be reordered
// around, and that is all it is -- the candidate lane merges on the same cycle.
func TestAnUnreadableMainHeadHoldsOnlyMain(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	fix := newBatchFixture(t)
	runner := newFakeCI()
	portBranch, candidates := mainHeadInCIBeforeCandidates(t, ctx, store, fix, runner)
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: fix.repo, MergeRecheck: model.MergeRecheckOff}); err != nil {
		t.Fatal(err)
	}
	number := 0
	for _, pr := range fix.sim.PullRequests {
		if pr.Head == portBranch {
			number = pr.Number
		}
	}
	client := unreadablePR{Client: fix.client, number: number}

	// The unreadable head is reported, and the cycle carries on.
	if err := orchestrator.SyncPullRequestsGuarding(ctx, store, client, nil,
		orchestrator.StateRepoGate{CI: runner}, 1,
		baseTime.Add(time.Minute)); err == nil {
		t.Fatal("a pull request nobody could read was not reported")
	}
	if !mergedBranch(fix.sim, candidates[0]) {
		t.Fatal("the candidate head did not merge: an unreadable main head held it")
	}
	if mergedBranch(fix.sim, candidates[1]) {
		t.Fatal("two candidate changes merged on one cycle")
	}
}

// Within one lane nothing changed: a head still deciding closes the slot
// for the changes behind it on its own branch.
func TestAHeadStillInCIStillHoldsItsOwnBranch(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	fix := newBatchFixture(t)
	runner := newFakeCI()
	run(t, t.TempDir(), "git", "--git-dir", fix.sim.BareRepo, "branch", "candidate", "main")
	head, headBranch := queuedTaskOnBase(t, ctx, store, fix.sim, fix.client, "t1", fix.repo, "candidate")
	head = grantCI(t, ctx, store, head)
	startRequiredCI(t, ctx, store, fix.client, runner, head, fix.repo, headBranch)
	behind, behindBranch := queuedTaskOnBase(t, ctx, store, fix.sim, fix.client, "t2", fix.repo, "candidate")
	behind = grantCI(t, ctx, store, behind)
	greenGrainCI(t, ctx, store, fix.client, runner, behind, fix.repo, behindBranch)
	setMergeable(fix.sim, true)
	cfg := model.DefaultConfig()
	cfg.MergeBatchSize = 1
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	syncWithCI(t, ctx, store, fix.client, runner, baseTime.Add(time.Minute))

	if mergedBranch(fix.sim, behindBranch) || mergedBranch(fix.sim, headBranch) {
		t.Fatal("a change merged past the head of its own branch's queue while that head's CI was still running")
	}
	_, note := mergeQueueStateOf(t, ctx, store, "t2")
	if !strings.Contains(note, "behind task t1 in the acme/widgets candidate queue") {
		t.Fatalf("t2's note does not name its own lane's head: %q", note)
	}
}
