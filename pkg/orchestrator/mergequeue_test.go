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
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// A conflicted queue head is not handed to a new task on a new branch
// any more: the task itself goes back to working, on the branch its pull
// request is already open from, so the resolution and the change share
// one pull request and one round of CI.
func TestSyncPullRequestsSendsAConflictedQueueHeadBackForRepair(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)

	setMergeable(sim, false)

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}

	// The task is queued again -- dispatchable by the ordinary path, with
	// nobody's approval asked for -- rather than parked as completed
	// while something else repairs it.
	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateQueued {
		t.Fatalf("state = %q, want queued: the task itself is what repairs its branch now", st)
	}

	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || obs.MergeQueueRepairAt == nil {
		t.Fatalf("observation = %+v, want MergeQueueRepairAt set", obs)
	}
	if obs.CompletedAt != nil {
		t.Fatalf("CompletedAt = %v, want cleared: that is what requeues the task", obs.CompletedAt)
	}
	if !obs.RepairInFlight() {
		t.Fatal("the repair does not read as in flight, so nothing will wait for it")
	}
	// Asking for the repair is asking for the attempt: a task sitting on
	// a failure streak (a salvaged run keeps its "failed" outcome
	// forever) would otherwise read 'failed' rather than 'queued' here.
	if obs.RetryRequestedAt == nil {
		t.Fatal("RetryRequestedAt was not set, so a task with a failure streak could not be repaired")
	}

	// No second task anywhere, and no branch but the one the pull request
	// is already open from.
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected the one task and no separate fix task, got %d", len(tasks))
	}

	// The comment is the whole of what the dispatched run is told, so it
	// has to name the pull request, what is wrong with it, and the branch
	// to push to.
	bodies := commentBodies(t, ctx, store, task.ID)
	if len(bodies) != 1 {
		t.Fatalf("expected one comment asking for the repair, got %q", bodies)
	}
	for _, want := range []string{"acme/widgets#", "conflict", branch} {
		if !strings.Contains(bodies[0], want) {
			t.Errorf("the repair comment does not mention %q:\n%s", want, bodies[0])
		}
	}

	// Nothing filed a GitHub issue for any of it: this is a store row and
	// a comment, and dispatch.Cycle picks the task up with no label
	// anywhere.
	if len(sim.Issues) != 0 {
		t.Fatalf("expected no GitHub issues at all, got %+v", sim.Issues)
	}
}

// A pull request behind the queue head whose standalone CI failed is sent for
// repair immediately, without waiting for the head to finish: CI failure is on
// the branch itself, so available workers can fix PRs in parallel.
func TestSyncPullRequestsRepairsFailingCIBehindQueueHeadInParallel(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	headTask, headBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	behindTask, behindBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t2", repo)

	// headTask has CI pending; behindTask has CI failed.
	setMergeable(sim, true)
	sim.CheckRuns[headBranch] = []github.CheckRun{
		{Name: "test", Status: "in_progress"},
	}
	sim.CheckRuns[behindBranch] = []github.CheckRun{
		{Name: "test", Status: "completed", Conclusion: strPtr("failure")},
	}

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}

	// headTask remains completed (waiting for CI)
	stHead, err := store.State(ctx, headTask.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stHead != model.StateCompleted {
		t.Fatalf("head state = %q, want completed", stHead)
	}

	// behindTask is immediately requeued for repair in parallel
	stBehind, err := store.State(ctx, behindTask.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stBehind != model.StateQueued {
		t.Fatalf("behind state = %q, want queued: standalone CI repair dispatches in parallel", stBehind)
	}

	obsBehind, err := store.GetObservation(ctx, behindTask.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obsBehind == nil || obsBehind.MergeQueueRepairAt == nil {
		t.Fatalf("behind observation = %+v, want MergeQueueRepairAt set", obsBehind)
	}
	if obsBehind.CompletedAt != nil {
		t.Fatalf("behind CompletedAt = %v, want cleared", obsBehind.CompletedAt)
	}
	if !obsBehind.RepairInFlight() {
		t.Fatal("behind repair does not read as in flight")
	}
}

// One repair per pull request, and one only: a cycle finding the repair
// already in flight must not ask again, or a head whose agent is mid-run
// would be re-requeued every tick.
func TestSyncPullRequestsDoesNotAskForASecondRepairWhileOneIsInFlight(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, _ := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	setMergeable(sim, false)

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("first SyncPullRequests: %v", err)
	}
	asked := repairAskedAt(t, ctx, store, task.ID)
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("second SyncPullRequests: %v", err)
	}

	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected no task to have been filed at all, got %d", len(tasks))
	}
	if got := commentBodies(t, ctx, store, task.ID); len(got) != 1 {
		t.Fatalf("expected exactly one repair comment, got %q", got)
	}
	if again := repairAskedAt(t, ctx, store, task.ID); !again.Equal(asked) {
		t.Fatalf("MergeQueueRepairAt moved from %v to %v: the repair was asked for twice", asked, again)
	}
}

// The repair ran, the task completed again, and the pull request is still
// conflicted: the automatic attempt did not stick, and the queue asks a
// person rather than dispatching the same repair a second time.
func TestSyncPullRequestsEscalatesWhenTheRepairFinishesButThePrIsStillBroken(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, _ := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	setMergeable(sim, false)

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("asking for the repair: %v", err)
	}
	if !repairInFlightNow(t, ctx, store, task.ID) {
		t.Fatal("expected a repair to have been asked for")
	}

	// The repair run finishes -- pushing something, or nothing -- and the
	// task completes again, standing in for a run that could not actually
	// resolve the conflict. The pull request is conflicted throughout.
	finished := baseTime.Add(time.Hour)
	if err := store.ObserveField(ctx, task.ID, finished, func(o *model.Observation) {
		o.CompletedAt = &finished
	}); err != nil {
		t.Fatal(err)
	}

	// Not on the cycle it finished on: dispatch runs before sync inside
	// one tick, so this cycle's verdict describes the branch as it was
	// before the repair's own push. Giving up here would give up on a
	// resolution that has just landed.
	if err := orchestrator.SyncPullRequests(ctx, store, client, finished); err != nil {
		t.Fatalf("SyncPullRequests on the cycle the repair finished on: %v", err)
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.MergeQueueBlockedAt != nil {
		t.Fatal("gave up on the cycle the repair finished on, before GitHub had judged what it pushed")
	}

	// The next cycle is judging the commit the repair actually pushed,
	// and it is still conflicted.
	later := finished.Add(time.Minute)
	if err := orchestrator.SyncPullRequests(ctx, store, client, later); err != nil {
		t.Fatalf("SyncPullRequests a cycle later (should escalate): %v", err)
	}

	obs, err = store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || obs.MergeQueueBlockedAt == nil {
		t.Fatal("expected the task to be marked as needing user input")
	}

	if got := commentBodies(t, ctx, store, task.ID); len(got) != 2 {
		t.Fatalf("expected the repair comment plus one escalation comment, got %q", got)
	}

	// No further cycle asks for another repair -- the task keeps the one
	// MergeQueueRepairAt it already has, and the queue has moved on.
	if err := orchestrator.SyncPullRequests(ctx, store, client, later.Add(time.Minute)); err != nil {
		t.Fatalf("third SyncPullRequests: %v", err)
	}
	if got := commentBodies(t, ctx, store, task.ID); len(got) != 2 {
		t.Fatalf("the queue said something a third time: %q", got)
	}
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected no fix task to have been filed, got %d tasks", len(tasks))
	}
}

// The repair that never finishes. Once one is asked for, its head does
// nothing at all until the task completes again -- and nothing else times
// the wait: the head reads CONFLICTED (not PENDING) for as long as it is
// waiting, so the check-stall deadline never looks at it. A repair that
// never finishes -- a dispatch that never happens, an agent run that
// never comes back -- would hold the head of its repo's queue for the
// life of the deployment, with everything behind it waiting: the same
// stall the check-stall deadline closes, reached from the other side.
//
// orchestrator.RepairDeadline is the bound, measured from the moment the
// repair was asked for. Past it the queue says so and gets on with the
// next task -- while the head it gave up on still merges the moment it
// reads clean.
func TestSyncPullRequestsGivesUpOnAQueueHeadWhoseRepairNeverFinishes(t *testing.T) {
	t.Parallel()
	deadline := orchestrator.RepairDeadline

	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	stuck, stuckBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	behind, _ := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t2", repo)

	// The head is conflicted, so the queue sends it back for repair. The
	// task behind it is perfectly mergeable and is held up only by being
	// second.
	setMergeable(sim, true)
	setBranchMergeable(sim, stuckBranch, false)
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests asking for the repair: %v", err)
	}
	if !repairInFlightNow(t, ctx, store, stuck.ID) {
		t.Fatal("expected a repair to have been asked of the conflicted head")
	}
	// Nothing ever dispatches it, and it never completes. That is the
	// whole of the failure: the task simply sits there, while the head
	// goes on reading conflicted every cycle.
	//
	// Well inside the deadline: the queue is still waiting, and has said
	// nothing beyond the comment asking for the repair. This is the same
	// cycle a repair that is simply taking a while gets.
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(deadline/2)); err != nil {
		t.Fatalf("SyncPullRequests inside the deadline: %v", err)
	}
	if bodies := commentBodies(t, ctx, store, stuck.ID); len(bodies) != 1 {
		t.Fatalf("expected only the repair comment inside the deadline, got %q", bodies)
	}
	obs, err := store.GetObservation(ctx, stuck.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs != nil && obs.MergeQueueBlockedAt != nil {
		t.Fatal("gave up on the queue head while its repair was still inside the deadline")
	}

	// Past it. The queue gives up, says how long it waited, and asks for
	// no second repair.
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(deadline)); err != nil {
		t.Fatalf("SyncPullRequests past the deadline: %v", err)
	}
	obs, err = store.GetObservation(ctx, stuck.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || obs.MergeQueueBlockedAt == nil {
		t.Fatal("the queue never gave up on a head whose repair never finished")
	}
	bodies := commentBodies(t, ctx, store, stuck.ID)
	if len(bodies) != 2 {
		t.Fatalf("expected the repair comment plus one escalation, got %q", bodies)
	}
	if !strings.Contains(bodies[1], "never finished") {
		t.Errorf("the escalation does not say the repair never finished:\n%s", bodies[1])
	}
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Fatalf("expected the two queued tasks and no fix task at all, got %d", len(tasks))
	}

	// And the queue has moved on: the task behind the stuck one is head
	// now, and merges.
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(deadline+time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests after the queue moved on: %v", err)
	}
	st, err := store.State(ctx, behind.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateClosed {
		t.Fatalf("state of the task behind the stuck one = %q, want closed: the queue never moved on", st)
	}

	// Giving up is not abandoning. A person resolves the conflict by
	// hand, and the pull request the queue stopped driving merges on its
	// own -- no second escalation, no further repair, no human step
	// beyond the push. The unfinished repair does not hold that merge
	// back: the queue has already given up on it.
	setBranchMergeable(sim, stuckBranch, true)
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(deadline+2*time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests once the conflict was resolved: %v", err)
	}
	st, err = store.State(ctx, stuck.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateClosed {
		t.Fatalf("state = %q, want closed: a blocked task still merges the moment it reads clean", st)
	}
	if bodies := commentBodies(t, ctx, store, stuck.ID); len(bodies) != 2 {
		t.Fatalf("expected the queue to say this once, got %q", bodies)
	}
}

// TestSyncPullRequestsOnlyActsOnTheQueueHeadNotLaterEntries pins the
// property that merges occur in queue order: only the task in front of its
// repo's queue is merged on a cycle, even when multiple entries are ready.
// Which one that is comes off the backlog (queueOrder) -- t1 sits ahead of
// t2 there, and was also filed first.
func TestSyncPullRequestsOnlyActsOnTheQueueHeadNotLaterEntries(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	earlier := baseTime
	later := baseTime.Add(time.Hour)

	head := filedTask(t, ctx, store, "t1", repo)
	head.AutoMerge = true
	head.CreatedAt = &earlier
	head.OrderKey = 100
	if err := store.PutTask(ctx, head); err != nil {
		t.Fatal(err)
	}
	headBranch := model.BranchName(head.ID)
	pushBranch(t, sim.BareRepo, headBranch)
	headPR, err := orchestrator.EnsurePullRequest(ctx, store, client, head, baseTime)
	if err != nil {
		t.Fatal(err)
	}
	head.Links = append(head.Links, model.Link{
		Kind: model.LinkFixes, Target: model.PullRequestRef{Repo: repo, Number: headPR.Number}.String(),
	})
	if err := store.PutTask(ctx, head); err != nil {
		t.Fatal(err)
	}
	if err := store.Observe(ctx, model.Observation{TaskID: head.ID, CompletedAt: &baseTime}); err != nil {
		t.Fatal(err)
	}

	second := filedTask(t, ctx, store, "t2", repo)
	second.AutoMerge = true
	second.CreatedAt = &later
	second.OrderKey = 200
	if err := store.PutTask(ctx, second); err != nil {
		t.Fatal(err)
	}
	secondBranch := model.BranchName(second.ID)
	pushBranch(t, sim.BareRepo, secondBranch)
	secondPR, err := orchestrator.EnsurePullRequest(ctx, store, client, second, baseTime)
	if err != nil {
		t.Fatal(err)
	}
	second.Links = append(second.Links, model.Link{
		Kind: model.LinkFixes, Target: model.PullRequestRef{Repo: repo, Number: secondPR.Number}.String(),
	})
	if err := store.PutTask(ctx, second); err != nil {
		t.Fatal(err)
	}
	if err := store.Observe(ctx, model.Observation{TaskID: second.ID, CompletedAt: &baseTime}); err != nil {
		t.Fatal(err)
	}

	setMergeable(sim, true)
	sim.CheckRuns[headBranch] = greenCheck
	sim.CheckRuns[secondBranch] = greenCheck

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}

	if !mergedBranch(sim, headBranch) {
		t.Fatal("expected the queue head to have merged")
	}
	if mergedBranch(sim, secondBranch) {
		t.Fatal("did not expect the second queue entry to merge on the same cycle")
	}
}

// queuedTaskWithPullRequest is the four-step setup every merge queue test
// here repeats: file an auto-merge task, push its branch, open its pull
// request, and record it as completed so SyncPullRequests picks it up. It
// returns the task as stored and the pull request's own head branch, which
// is what a test seeds check runs under (Sim.CheckRuns' own doc comment).
func queuedTaskWithPullRequest(t *testing.T, ctx context.Context, store *model.Store,
	sim *githubsim.Sim, client *github.RESTClient, id string, repo model.RepoRef) (model.Task, string) {

	t.Helper()
	task := filedTask(t, ctx, store, id, repo)
	task.AutoMerge = true
	task.CreatedAt = &baseTime
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

// setMergeable is GitHub having finished computing mergeability for every
// pull request the sim knows about.
func setMergeable(sim *githubsim.Sim, mergeable bool) {
	for i := range sim.PullRequests {
		sim.PullRequests[i].Mergeable = &mergeable
	}
}

// setBranchMergeable is the same for one branch's pull request alone, so
// a test can hold a queue head conflicted while everything behind it
// reads clean.
func setBranchMergeable(sim *githubsim.Sim, branch string, mergeable bool) {
	for i := range sim.PullRequests {
		if sim.PullRequests[i].Head == branch {
			sim.PullRequests[i].Mergeable = &mergeable
		}
	}
}

// A pull request whose tests are still running must not merge, however
// mergeable GitHub says it is: a queue that merges on "no failures
// reported yet" lands changes before CI has said anything about them.
// The task holds its place at the head of the queue and merges on a
// later cycle, once the checks it is waiting for come back green.
func TestSyncPullRequestsWaitsForRunningChecksBeforeMerging(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)

	setMergeable(sim, true)
	sim.CheckRuns[branch] = []github.CheckRun{
		{Name: "lint", Status: "completed", Conclusion: strPtr("success")},
		{Name: "tests", Status: "in_progress"},
	}

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests with CI still running: %v", err)
	}

	for _, pr := range sim.PullRequests {
		if pr.Merged {
			t.Fatal("the pull request was merged while its tests were still running")
		}
	}
	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateCompleted {
		t.Fatalf("state = %q, want still completed: nothing has happened yet", st)
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil {
		t.Fatal("merge queue state was not recorded")
	}
	if obs.MergeQueueState != "waiting_ci" {
		t.Fatalf("merge queue state = %q, want waiting_ci", obs.MergeQueueState)
	}
	// Waiting is not the same as giving up on: an unfinished check is not
	// a broken one, so nothing is asked of anyone and nobody is told
	// anything.
	if repairInFlightNow(t, ctx, store, task.ID) {
		t.Fatal("a repair was asked for a pull request whose checks had not finished")
	}
	if bodies := commentBodies(t, ctx, store, task.ID); len(bodies) != 0 {
		t.Fatalf("expected no comments while simply waiting for CI, got %q", bodies)
	}

	// CI finishes, green -- and the same task, still the head of its
	// queue, merges on the very next cycle.
	sim.CheckRuns[branch] = []github.CheckRun{
		{Name: "lint", Status: "completed", Conclusion: strPtr("success")},
		{Name: "tests", Status: "completed", Conclusion: strPtr("success")},
	}
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests once CI passed: %v", err)
	}
	st, err = store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateClosed {
		t.Fatalf("state = %q, want closed once the checks passed and auto-merge landed", st)
	}
}

// The other half of the same rule: as soon as any test finishes red, a
// failing head is a broken head, and it gets exactly the automatic fix a
// conflicted one gets -- requeued pre-approved on the branch it repairs,
// naming the checks already known to have failed.
func TestSyncPullRequestsStartsRepairAtFirstCIFailure(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)

	setMergeable(sim, true)
	// One job has already gone red while another is still running. That is
	// enough to start the repair: CI can no longer produce a clean verdict,
	// and the agent can work in parallel with the remaining job.
	sim.CheckRuns[branch] = []github.CheckRun{
		{Name: "unit tests", Status: "completed", Conclusion: strPtr("failure")},
		{Name: "e2e", Status: "in_progress"},
	}
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests with CI still running: %v", err)
	}
	if !repairInFlightNow(t, ctx, store, task.ID) {
		t.Fatal("the repair did not start when the first CI failure appeared")
	}

	for _, pr := range sim.PullRequests {
		if pr.Merged {
			t.Fatal("a pull request with failing checks was merged")
		}
	}
	if !repairInFlightNow(t, ctx, store, task.ID) {
		t.Fatal("expected the failing queue head to have been sent back for repair")
	}
	bodies := commentBodies(t, ctx, store, task.ID)
	if len(bodies) != 1 {
		t.Fatalf("expected one comment asking for the repair, got %q", bodies)
	}
	for _, want := range []string{"unit tests", branch} {
		if !strings.Contains(bodies[0], want) {
			t.Errorf("the repair comment does not name %q:\n%s", want, bodies[0])
		}
	}
}

// A check GitHub concluded `cancelled` delivered no verdict, so a head
// carrying one does not merge on the strength of the checks that did
// report: it holds its place, and when the verdict is still missing at
// the stall deadline the queue hands the pull request to a person
// instead. Decided 2026-09-08 -- this used to merge, silently, and
// healthFrom's own doc comment carries the whole argument.
//
// The shape is not invented. It was read off bwsalmon/grain itself on
// 2026-09-07 (docs/hand-runs/2026-09-07-merge-queue.md): five `tests`
// runs cancelled that day, each leaving `go` -- the whole Go suite --
// `completed`/`cancelled` beside checks that concluded `success`. The
// list below is four of the eleven on one of those commits. What kept
// every one of them away from this path is
// `.github/workflows/tests.yml`'s own concurrency group, which cancels
// the *superseded* commit's run: the queue reads checks by head sha, so
// a cancellation belonging to a commit that has been pushed past is
// never read. A cancellation on the head sha itself -- a person pressing
// the button, a run cancelled from the Actions page -- is read, and
// lands here.
func TestSyncPullRequestsHoldsAHeadWhoseCheckWasCancelled(t *testing.T) {
	deadline := 2 * time.Hour
	defer orchestrator.SetCheckStallDeadline(deadline)()

	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)

	setMergeable(sim, true)
	sim.CheckRuns[branch] = []github.CheckRun{
		{Name: "workflows", Status: "completed", Conclusion: strPtr("success")},
		{Name: "go", Status: "completed", Conclusion: strPtr("cancelled")},
		{Name: "real KVM sandboxes", Status: "completed", Conclusion: strPtr("cancelled")},
		{Name: "frontend", Status: "completed", Conclusion: strPtr("success")},
	}

	// Inside the deadline the queue waits, and waiting is all it does: a
	// re-run registering against this same head sha would resolve it with
	// nobody told anything, which is the "cancelled, will retry" case.
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if mergedBranch(sim, branch) {
		t.Fatal("a head merged on a suite whose verdict was cancelled")
	}
	if repairInFlightNow(t, ctx, store, task.ID) {
		t.Fatal("a cancelled check was read as something to repair")
	}
	if bodies := commentBodies(t, ctx, store, task.ID); len(bodies) != 0 {
		t.Fatalf("the queue said something inside the deadline: %q", bodies)
	}

	// Past it: no fix task -- nothing failed and no agent has anything to
	// go on -- but a person, told which check went unanswered and that a
	// re-run is what answers it.
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(deadline)); err != nil {
		t.Fatalf("SyncPullRequests past the deadline: %v", err)
	}
	if mergedBranch(sim, branch) {
		t.Fatal("a head merged on a suite whose verdict was cancelled")
	}
	if repairInFlightNow(t, ctx, store, task.ID) {
		t.Fatal("the queue spent this pull request's repair on a cancellation")
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || obs.MergeQueueBlockedAt == nil {
		t.Fatal("the queue waited past its deadline on a verdict that is never coming")
	}
	bodies := commentBodies(t, ctx, store, task.ID)
	if len(bodies) != 1 {
		t.Fatalf("expected one comment handing this to a person, got %q", bodies)
	}
	for _, want := range []string{"go", "cancelled", "re-run"} {
		if !strings.Contains(bodies[0], want) {
			t.Errorf("the escalation does not mention %q:\n%s", want, bodies[0])
		}
	}
}

// Naming the failing job is where the queue's own message used to stop,
// and for the agent it dispatches that is barely a starting point: a
// sandbox is not a CI runner, and this deployment's sandboxes reach
// nothing but the git proxy, so an agent told "the `go` job is red"
// cannot go and read what the `go` job said. It has to arrive with the
// task. That is the difference between a repair and a guess, and a repair
// that is a guess costs the queue another cycle.
func TestSyncPullRequestsPutsTheFailingJobsOwnLogIntoTheRepairComment(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)

	setMergeable(sim, true)
	sim.CheckRuns[branch] = []github.CheckRun{
		{Name: "go", Status: "completed", Conclusion: strPtr("failure")},
		{Name: "terraform", Status: "completed", Conclusion: strPtr("success")},
	}
	// Seeded under the branch name, the same key the check runs above
	// use: the Actions endpoints are called with the head sha, which
	// githubsim resolves against its own bare repo.
	sim.WorkflowJobs[branch] = []githubsim.WorkflowJob{
		{Name: "go", Conclusion: "failure", Log: "" +
			"2026-01-02T03:04:05.1234567Z --- FAIL: TestQueueHead (0.01s)\n" +
			"2026-01-02T03:04:05.1234567Z     sync_test.go:42: got 3, want 4\n" +
			"2026-01-02T03:04:05.1234567Z FAIL\tgithub.com/ai-on-gke/grain/pkg/orchestrator\n"},
		{Name: "terraform", Conclusion: "success", Log: "terraform: no changes\n"},
	}

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests once CI failed: %v", err)
	}

	if !repairInFlightNow(t, ctx, store, task.ID) {
		t.Fatal("expected the failing queue head to have been sent back for repair")
	}
	bodies := commentBodies(t, ctx, store, task.ID)
	if len(bodies) != 1 {
		t.Fatalf("expected one comment asking for the repair, got %q", bodies)
	}
	for _, want := range []string{
		"--- FAIL: TestQueueHead",
		"sync_test.go:42: got 3, want 4",
	} {
		if !strings.Contains(bodies[0], want) {
			t.Errorf("the repair comment does not carry %q from the failing job's log:\n%s", want, bodies[0])
		}
	}
	// Actions stamps every line with the same timestamp. It says nothing
	// about the failure and costs about a quarter of every line.
	if strings.Contains(bodies[0], "2026-01-02T03:04:05") {
		t.Errorf("the repair comment kept Actions' per-line timestamps:\n%s", bodies[0])
	}
	// The job that passed is not evidence of anything, and its log is
	// never fetched: FailedJobLogs filters at every step.
	if strings.Contains(bodies[0], "terraform: no changes") {
		t.Errorf("the repair comment carries a passing job's log:\n%s", bodies[0])
	}
}

// The other shape of a red build with no log: a workflow run GitHub
// concluded before it started a job of it. No job ran, so nothing in the
// tree is what failed and no commit pushed to the branch clears it --
// which makes the automatic repair the one move that cannot work, and
// this pull request only ever gets one. So the queue skips it and asks a
// person, in a comment that names the four things that fail a run this
// way and which of them a branch can fix.
//
// bwsalmon/grain-home#1 is why: the queue asked for a repair there, an
// agent spent a whole run establishing that the check could not go green
// from that repository, and the queue escalated a run later anyway.
func TestSyncPullRequestsEscalatesARunThatNeverStartedInsteadOfRepairingIt(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)

	setMergeable(sim, true)
	sim.CheckRuns[branch] = []github.CheckRun{
		{Name: "grain can load this", Status: "completed", Conclusion: strPtr("failure")},
	}
	// Keyed by the branch name like the check runs above, and seeded
	// instead of WorkflowJobs rather than alongside it: this run has no
	// jobs at all, which is the whole point of it.
	sim.StartupFailures[branch] = true

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests once CI failed at startup: %v", err)
	}

	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || obs.MergeQueueBlockedAt == nil {
		t.Fatal("the queue head was not given up on")
	}
	// Not sent back to an agent, and the repair still unspent: the whole
	// point is that this head never cost a run that could not have
	// helped, and that the person now holding it still has one to give if
	// they push a commit that changes the answer.
	if obs.MergeQueueRepairAt != nil {
		t.Error("the queue spent this pull request's one repair on a run that never started")
	}
	if obs.CompletedAt == nil {
		t.Error("the task was requeued for an agent rather than handed to a person")
	}

	bodies := commentBodies(t, ctx, store, task.ID)
	if len(bodies) != 1 {
		t.Fatalf("expected one comment escalating to a person, got %q", bodies)
	}
	for _, want := range []string{
		// Which check is red, the verdict GitHub gave the run, and the
		// page carrying its actual reason.
		"its checks are failing (`grain can load this`)",
		"startup_failure",
		"/actions/runs/",
		// And what that page will show when they open it: jobs with no
		// steps under them. Unnamed, it reads as an accusation against
		// the workflow file -- an operator has already filed one --
		// which sends the reader looking in the one place with nothing
		// to find.
		"no steps",
		// The one cause a branch can fix, named first, and the fact that
		// no agent was sent.
		"workflow file GitHub cannot parse at this commit",
		"Actions disabled or restricted for the repository",
		"one automatic repair",
		"moved on to the next task in acme/widgets",
	} {
		if !strings.Contains(bodies[0], want) {
			t.Errorf("the escalation comment does not say %q:\n%s", want, bodies[0])
		}
	}
	// It reads as an escalation, not as a task brief: nothing here asks
	// anybody to push to the branch on the queue's behalf.
	if strings.Contains(bodies[0], "sent this task back to an agent") {
		t.Errorf("the escalation comment reads as a repair request:\n%s", bodies[0])
	}
	// And it says why the one cause a branch could own is not this one,
	// rather than leaving the reader to check: the queue looked, and the
	// pull request edits nothing GitHub would have tried to parse.
	if !strings.Contains(bodies[0], "touches no file under `.github/workflows/`") {
		t.Errorf("the escalation comment does not rule out the workflow edit:\n%s", bodies[0])
	}
}

// The same escalation, arrived at from the shape GitHub actually
// produces for a repository that has run out of Actions minutes: the run
// is `failure`, not `startup_failure`, because GitHub created the job
// before it decided not to start it. Nothing but the job's empty `steps`
// array separates that from a job that ran and failed with an expired
// log, and read as the latter it costs the pull request its one automatic
// repair and an agent a whole run pushing at a check that has never
// passed on any tree. It reads as the former now -- and GitHub's own
// sentence about it, which the check run carries and the API says nowhere
// else, is in the comment the person gets.
func TestSyncPullRequestsEscalatesAJobGitHubNeverStarted(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)

	setMergeable(sim, true)
	sim.CheckRuns[branch] = []github.CheckRun{
		{Name: "grain can load this", Status: "completed", Conclusion: strPtr("failure")},
	}
	reason := "The job was not started because recent account payments have failed or " +
		"your spending limit needs to be increased"
	sim.WorkflowJobs[branch] = []githubsim.WorkflowJob{
		{Name: "load", Conclusion: "failure", NeverStarted: true, FailureReason: reason},
	}

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests once CI failed without starting a job: %v", err)
	}

	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || obs.MergeQueueBlockedAt == nil {
		t.Fatal("the queue head was not given up on")
	}
	if obs.MergeQueueRepairAt != nil {
		t.Error("the queue spent this pull request's one repair on a job that never started")
	}

	bodies := commentBodies(t, ctx, store, task.ID)
	if len(bodies) != 1 {
		t.Fatalf("expected one comment escalating to a person, got %q", bodies)
	}
	for _, want := range []string{
		// GitHub's own words, first -- the whole point of the read, and
		// the thing no amount of reasoning from a conclusion arrives at.
		reason,
		// The conclusion as GitHub actually gave it, rather than the
		// `startup_failure` this comment used to assert of every run it
		// escalated.
		"`failure`",
		"before a single job of it ran a step",
		"one automatic repair",
	} {
		if !strings.Contains(bodies[0], want) {
			t.Errorf("the escalation comment does not say %q:\n%s", want, bodies[0])
		}
	}
	if strings.Contains(bodies[0], github.StartupFailure) {
		t.Errorf("the comment claims a conclusion GitHub did not give this run:\n%s", bodies[0])
	}
}

// The other side of that read, and the one startup failure whose cause
// really is in the tree: a workflow file this very pull request edited,
// which GitHub could not parse at this commit. The run dies exactly as
// it does for a repository that may not run Actions -- same conclusion,
// same absent jobs -- and the only thing separating them is which files
// the branch touched. So the queue asks, and spends the automatic repair
// on the case an agent can actually fix.
func TestSyncPullRequestsRepairsAStartupFailureThisBranchsWorkflowEditCaused(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	// A real edit to a real workflow file, pushed onto the head branch:
	// githubsim reads the pull request's files off the repository, so
	// this is the whole of the seeding.
	pushFile(t, sim.BareRepo, branch, ".github/workflows/ci.yml", "on: push\njobs:\n  - not-a-mapping\n")

	setMergeable(sim, true)
	sim.CheckRuns[branch] = []github.CheckRun{
		{Name: "ci", Status: "completed", Conclusion: strPtr("failure")},
	}
	sim.StartupFailures[branch] = true

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests once CI failed at startup: %v", err)
	}

	if !repairInFlightNow(t, ctx, store, task.ID) {
		t.Fatal("expected the workflow edit to have been sent back for repair")
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.MergeQueueBlockedAt != nil {
		t.Error("the queue gave up on a startup failure its own branch caused")
	}

	bodies := commentBodies(t, ctx, store, task.ID)
	if len(bodies) != 1 {
		t.Fatalf("expected one comment asking for the repair, got %q", bodies)
	}
	for _, want := range []string{
		"sent this task back to an agent",
		// The file it edited, named, and what GitHub did about it.
		".github/workflows/ci.yml",
		"startup_failure",
		"cannot parse at this commit",
		// And the way out of a wrong guess: a workflow edit is evidence,
		// not proof, so the run is told how to recognise the settings
		// cause and to say so rather than push at it.
		"Say that on this task and stop",
	} {
		if !strings.Contains(bodies[0], want) {
			t.Errorf("the repair comment does not say %q:\n%s", want, bodies[0])
		}
	}
}

// A workflow file somewhere else in the tree is not a workflow file:
// GitHub reads them from .github/workflows/ and nowhere else, so an edit
// to a file that merely looks like one cannot be why the run never
// started, and the queue must not spend the repair on it.
func TestSyncPullRequestsEscalatesAStartupFailureWithAWorkflowFileOutsideDotGithub(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	pushFile(t, sim.BareRepo, branch, "deploy/workflows/ci.yml", "on: push\n")

	setMergeable(sim, true)
	sim.CheckRuns[branch] = []github.CheckRun{
		{Name: "ci", Status: "completed", Conclusion: strPtr("failure")},
	}
	sim.StartupFailures[branch] = true

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests once CI failed at startup: %v", err)
	}

	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || obs.MergeQueueBlockedAt == nil {
		t.Fatal("the queue head was not given up on")
	}
	if obs.MergeQueueRepairAt != nil {
		t.Error("the queue spent this pull request's one repair on a file GitHub never reads")
	}
}

// The read itself can fail -- a credential that may not list a pull
// request's files, GitHub having a bad minute -- and then grain knows no
// more than it did before task 154. So it escalates, exactly as it did
// then: a repair asked for on a guess is the run this whole path exists
// not to spend. What changes is the sentence, which must not claim to
// have ruled anything out.
func TestSyncPullRequestsEscalatesAStartupFailureWhenTheFilesCannotBeRead(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, _ := newSim(t, "acme", "widgets", "main")
	client := github.NewClient(brokenFilesTransport{sim}, nil)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	pushFile(t, sim.BareRepo, branch, ".github/workflows/ci.yml", "on: push\n")

	setMergeable(sim, true)
	sim.CheckRuns[branch] = []github.CheckRun{
		{Name: "ci", Status: "completed", Conclusion: strPtr("failure")},
	}
	sim.StartupFailures[branch] = true

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests once CI failed at startup: %v", err)
	}

	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || obs.MergeQueueBlockedAt == nil {
		t.Fatal("the queue head was not given up on")
	}
	if obs.MergeQueueRepairAt != nil {
		t.Error("the queue spent the repair on a startup failure it could not attribute")
	}
	bodies := commentBodies(t, ctx, store, task.ID)
	if len(bodies) != 1 {
		t.Fatalf("expected one comment escalating to a person, got %q", bodies)
	}
	if strings.Contains(bodies[0], "touches no file under") {
		t.Errorf("the escalation comment rules out a cause nobody managed to check:\n%s", bodies[0])
	}
	if !strings.Contains(bodies[0], "could not read which files this pull request touches") {
		t.Errorf("the escalation comment does not say the read failed:\n%s", bodies[0])
	}
}

// brokenFilesTransport is a githubsim that has lost exactly one
// endpoint: the pull request's files. Everything else answers normally,
// which is the shape of a credential scoped short of that read rather
// than of GitHub being down.
type brokenFilesTransport struct{ inner github.Transport }

func (b brokenFilesTransport) Request(method, path string, headers map[string]string,
	body []byte) (github.ApiResponse, error) {

	if strings.Contains(path, "/files") {
		return github.ApiResponse{Status: 403, Body: []byte(`{"message":"Resource not accessible"}`)}, nil
	}
	return b.inner.Request(method, path, headers, body)
}

// The startup failure's near neighbour, which must still get the repair:
// a run whose jobs did run and fail and whose logs simply could not be
// read (past Actions' retention window, or a credential that may not
// fetch one). It arrives here as the same kind of entry -- a run standing
// in for jobs with no log -- and the only thing telling the two apart is
// that this one has failing jobs under it. Something in the tree failed,
// so an agent has something to fix.
func TestSyncPullRequestsStillRepairsAFailedRunWhoseJobsHaveNoLog(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)

	setMergeable(sim, true)
	sim.CheckRuns[branch] = []github.CheckRun{
		{Name: "tests", Status: "completed", Conclusion: strPtr("failure")},
	}
	// A job that ran and failed, with no log to serve for it -- the sim
	// answers 410 for an empty Log, exactly as a real expired one does.
	sim.WorkflowJobs[branch] = []githubsim.WorkflowJob{
		{Name: "go test", Conclusion: "failure"},
	}

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests once CI failed with no readable log: %v", err)
	}

	if !repairInFlightNow(t, ctx, store, task.ID) {
		t.Fatal("expected the failing queue head to have been sent back for repair")
	}
	bodies := commentBodies(t, ctx, store, task.ID)
	if len(bodies) != 1 {
		t.Fatalf("expected one comment asking for the repair, got %q", bodies)
	}
	if !strings.Contains(bodies[0], "sent this task back to an agent") {
		t.Errorf("a build whose jobs really failed did not ask for a repair:\n%s", bodies[0])
	}
	if !strings.Contains(bodies[0], "served no log") {
		t.Errorf("the repair comment does not say why there is no log:\n%s", bodies[0])
	}
	// And what a repair's scope is: a red check the branch did not cause
	// still stops it merging, so the queue asks for that one too rather
	// than leaving the agent to apply the "not yours to fix" rule every
	// other run works under (orchestrator.repairCISection).
	if !strings.Contains(bodies[0], "already red on the base") {
		t.Errorf("the repair comment does not put inherited breakage in scope:\n%s", bodies[0])
	}
	// The run's verdict is not the whole of what GitHub will say without
	// a log: the jobs endpoint names the job that failed and the step of
	// it that did, and those cost no log fetch at all
	// (github.ActionsState, github.FailedJobsNote). Without them this
	// section names a workflow and nothing under it.
	if !strings.Contains(bodies[0], "### go test") {
		t.Errorf("the repair comment does not name the job that failed:\n%s", bodies[0])
	}
	if !strings.Contains(bodies[0], "The step that failed: `Run the job`") {
		t.Errorf("the repair comment does not name the step that failed:\n%s", bodies[0])
	}
}

// The race the merge queue is on the wrong side of, and the reason a
// repair could open with a check name and nothing else: a check run
// concludes `failure` before the workflow run behind it does, so
// FailedJobLogs has no *completed failed run* to walk and finds nothing
// at all -- while the jobs endpoint already names the job that broke and
// the step of it that did.
//
// The queue does not wait for the log (readBuildEvidence: a tick is
// thirty seconds and the repair comment is written once), so what it
// must do instead is say which of the three "no log" stories this is,
// name the job and step it can have for free, and tell the run that
// reads this minutes later how to fetch the log itself.
//
// Not seedable in githubsim, which serves every run as `completed`: this
// is one commit's Actions state answered directly.
func TestSyncPullRequestsRepairNamesTheJobAndStepWhenNoLogIsServedYet(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)

	setMergeable(sim, true)
	sim.CheckRuns[branch] = []github.CheckRun{
		{Name: "ci", Status: "completed", Conclusion: strPtr("failure")},
	}
	unpublished := unfinishedRun{Client: client, state: github.ActionsState{
		Runs: 1, Unfinished: 1,
		Jobs: []github.ActionsJob{{
			Name:        "ui e2e",
			RunName:     "ci",
			URL:         "https://github.example/acme/widgets/actions/runs/7/job/8",
			FailedSteps: []string{"Test e2e"},
		}},
	}}

	if err := orchestrator.SyncPullRequests(ctx, store, unpublished, baseTime); err != nil {
		t.Fatalf("SyncPullRequests on a build whose log is not published yet: %v", err)
	}

	// The routing is exactly what it was: a build with no readable log is
	// still a repair, not an escalation.
	if !repairInFlightNow(t, ctx, store, task.ID) {
		t.Fatal("expected the failing queue head to have been sent back for repair")
	}
	bodies := commentBodies(t, ctx, store, task.ID)
	if len(bodies) != 1 {
		t.Fatalf("expected one comment asking for the repair, got %q", bodies)
	}
	for _, want := range []string{
		"### ui e2e (in the ci workflow, still running)",
		"The step that failed: `Test e2e`",
		"https://github.example/acme/widgets/actions/runs/7/job/8",
		// Which of the three stories it is, said as a fact rather than
		// offered as a list of possibilities...
		"Actions only serves a job's log once its run has finished",
		// ...and the move that gets the log itself, from a sandbox that
		// starts minutes after this read.
		"`pull_request_status`",
	} {
		if !strings.Contains(bodies[0], want) {
			t.Errorf("the repair comment does not carry %q:\n%s", want, bodies[0])
		}
	}
}

// unfinishedRun is a client reading a commit whose check run has gone
// red while the workflow run behind it is still going: no completed
// failed run for FailedJobLogs to walk, and an Actions state that
// already names the failed job.
type unfinishedRun struct {
	github.Client
	state github.ActionsState
}

func (c unfinishedRun) FailedJobLogs(owner, repo, headSHA string) ([]github.JobLog, error) {
	return nil, nil
}

func (c unfinishedRun) ActionsState(owner, repo, headSHA string) (github.ActionsState, error) {
	return c.state, nil
}

// The other end of the same question, and the one the old wording
// guessed at loudest: a failing check with no Actions run behind it at
// all. That really is a check posted by some other provider, and the
// repair has to say so -- there is no log coming, and no tool call that
// will fetch one.
func TestSyncPullRequestsRepairSaysWhenTheFailingCheckIsNotActions(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)

	setMergeable(sim, true)
	// A check run, and no workflow jobs seeded under the branch at all:
	// the sim answers the runs endpoint with none, which is what a repo
	// whose CI is Buildkite looks like from here.
	sim.CheckRuns[branch] = []github.CheckRun{
		{Name: "buildkite/tests", Status: "completed", Conclusion: strPtr("failure")},
	}

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests on a failing check that is not Actions: %v", err)
	}

	if !repairInFlightNow(t, ctx, store, task.ID) {
		t.Fatal("expected the failing queue head to have been sent back for repair")
	}
	bodies := commentBodies(t, ctx, store, task.ID)
	if len(bodies) != 1 {
		t.Fatalf("expected one comment asking for the repair, got %q", bodies)
	}
	if !strings.Contains(bodies[0], "no workflow run at all against this commit") {
		t.Errorf("the repair comment does not say the failure is not Actions:\n%s", bodies[0])
	}
	// And says outright that the tool which fetches a log elsewhere in
	// these answers cannot fetch this one, rather than sending the run
	// after a log that is never coming: it reads the same Actions
	// endpoints this read.
	if !strings.Contains(bodies[0], "neither can `pull_request_status`") {
		t.Errorf("the repair comment leaves the run to try for a log nothing can fetch:\n%s", bodies[0])
	}
}

// The second read is only ever the price of an answer that has nothing
// in it. A build with a real job log says everything the Actions state
// could, and a build whose runs all died before starting is not going to
// be repaired at all (github.OnlyStartupFailures) -- so neither pays for
// the read, and neither has its decision touched by it.
func TestSyncPullRequestsAsksActionsWhyOnlyWhenThereIsNoLogToShow(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		seed     func(sim *githubsim.Sim, branch string)
		repaired bool
	}{
		{
			name: "a job log the queue could read",
			seed: func(sim *githubsim.Sim, branch string) {
				sim.WorkflowJobs[branch] = []githubsim.WorkflowJob{
					{Name: "go", Conclusion: "failure", Log: "--- FAIL: TestThing\n"},
				}
			},
			repaired: true,
		},
		{
			name: "a run that died before a job of it started",
			seed: func(sim *githubsim.Sim, branch string) {
				sim.StartupFailures[branch] = true
			},
			repaired: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, ctx := openStore(t)
			sim, client := newSim(t, "acme", "widgets", "main")
			repo := model.RepoRef{Owner: "acme", Name: "widgets"}
			task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)

			setMergeable(sim, true)
			sim.CheckRuns[branch] = []github.CheckRun{
				{Name: "ci", Status: "completed", Conclusion: strPtr("failure")},
			}
			tc.seed(sim, branch)

			reads := 0
			counted := countedActionsState{Client: client, reads: &reads}
			if err := orchestrator.SyncPullRequests(ctx, store, counted, baseTime); err != nil {
				t.Fatalf("SyncPullRequests: %v", err)
			}

			if reads != 0 {
				t.Errorf("the queue spent %d Actions reads on a build that had already said "+
					"what broke", reads)
			}
			// And the decision each of these already made is the
			// decision it still makes.
			if got := repairInFlightNow(t, ctx, store, task.ID); got != tc.repaired {
				t.Errorf("repair asked for = %v, want %v", got, tc.repaired)
			}
		})
	}
}

// countedActionsState answers ActionsState exactly as the sim would and
// counts the asking, for the tests above about a read that should not
// happen at all.
type countedActionsState struct {
	github.Client
	reads *int
}

func (c countedActionsState) ActionsState(owner, repo, headSHA string) (github.ActionsState, error) {
	*c.reads++
	return c.Client.ActionsState(owner, repo, headSHA)
}

// logsUnreadable is a client whose FailedJobLogs is refused -- the shape
// of a deployment whose credential can read checks but not Actions, and
// of a repo whose CI is not Actions at all.
type logsUnreadable struct {
	github.Client
}

func (c logsUnreadable) FailedJobLogs(owner, repo, headSHA string) ([]github.JobLog, error) {
	return nil, errors.New("403 Forbidden")
}

// The log is an annotation on the repair, not the repair. Failing the
// cycle over an unreadable log would cost the queue head the one
// automatic repair it gets, over the part of the comment that is a bonus
// -- so the read is best effort, and its failure leaves exactly the
// message this queue sent before logs were ever fetched.
func TestSyncPullRequestsStillAsksForARepairWhenTheJobLogsCannotBeRead(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)

	setMergeable(sim, true)
	sim.CheckRuns[branch] = []github.CheckRun{
		{Name: "go", Status: "completed", Conclusion: strPtr("failure")},
	}

	if err := orchestrator.SyncPullRequests(ctx, store, logsUnreadable{client}, baseTime); err != nil {
		t.Fatalf("SyncPullRequests with unreadable job logs: %v", err)
	}

	if !repairInFlightNow(t, ctx, store, task.ID) {
		t.Fatal("no repair was asked for when the job logs could not be read")
	}
	bodies := commentBodies(t, ctx, store, task.ID)
	if len(bodies) != 1 {
		t.Fatalf("expected one comment asking for the repair, got %q", bodies)
	}
	if !strings.Contains(bodies[0], "its checks are failing (`go`)") {
		t.Errorf("the repair comment no longer names the failing check:\n%s", bodies[0])
	}
	if strings.Contains(bodies[0], "What CI printed") {
		t.Errorf("the repair comment has an empty log section:\n%s", bodies[0])
	}
}

// The narrower half of the same race, and the one an empty check list
// hides. GitHub creates a workflow run's check runs asynchronously, after
// it has processed the push, while the pull request exists the instant
// the branch lands -- so a sync landing in that gap sees no checks at
// all, which is also precisely what a repo with no CI looks like. Merging
// on it lands the change before CI has said a word.
//
// The window (orchestrator.SetCheckRegistrationWindow) is what tells the
// two apart, and the only thing that can: this test is the whole shape of
// it, a pull request that does not merge on the cycle it was opened on
// and does merge once its head commit has sat there long enough that CI
// would have shown up by now.
func TestSyncPullRequestsWaitsForCiToRegisterBeforeMergingAFreshPullRequest(t *testing.T) {
	window := 2 * time.Minute
	defer orchestrator.SetCheckRegistrationWindow(window)()

	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, _ := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)

	// Everything GitHub has answered so far says merge me: mergeability
	// computed and clean, and not one check run reported against the
	// commit. Only time distinguishes that from a green repo with no CI.
	setMergeable(sim, true)

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests on a just-opened pull request: %v", err)
	}
	for _, pr := range sim.PullRequests {
		if pr.Merged {
			t.Fatal("the pull request merged on the cycle it was opened on, before CI could register a check")
		}
	}
	// Waiting, not giving up: an unregistered check is not a broken one.
	if repairInFlightNow(t, ctx, store, task.ID) {
		t.Fatal("a repair was asked for a pull request whose CI had not reported yet")
	}
	if bodies := commentBodies(t, ctx, store, task.ID); len(bodies) != 0 {
		t.Fatalf("expected no comments while simply waiting for CI to register, got %q", bodies)
	}

	// Half a window later it is still waiting, and the entry is still the
	// head of its queue rather than having been passed over.
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(window/2)); err != nil {
		t.Fatalf("SyncPullRequests inside the window: %v", err)
	}
	for _, pr := range sim.PullRequests {
		if pr.Merged {
			t.Fatal("the pull request merged inside the registration window")
		}
	}

	// Nothing ever reported, so there was nothing to report: this repo
	// has no CI, and its pull requests still have to merge.
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(window)); err != nil {
		t.Fatalf("SyncPullRequests once the window had elapsed: %v", err)
	}
	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateClosed {
		t.Fatalf("state = %q, want closed: a repo with no CI at all must still merge", st)
	}
}

// The window costs a repo that does have CI nothing. The moment a check
// exists there is a real signal to read, and the answer comes from that
// check rather than from a clock -- so a green one merges without sitting
// out the rest of a window it only ever existed to fill.
func TestSyncPullRequestsMergesOnARegisteredCheckWithoutWaitingOutTheWindow(t *testing.T) {
	window := time.Hour
	defer orchestrator.SetCheckRegistrationWindow(window)()

	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	setMergeable(sim, true)

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests before CI registered: %v", err)
	}
	for _, pr := range sim.PullRequests {
		if pr.Merged {
			t.Fatal("merged before CI registered anything")
		}
	}

	// CI registers, runs, and passes -- all well inside the window.
	sim.CheckRuns[branch] = []github.CheckRun{{Name: "tests", Status: "queued"}}
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests with CI queued: %v", err)
	}
	for _, pr := range sim.PullRequests {
		if pr.Merged {
			t.Fatal("merged with a queued check")
		}
	}

	sim.CheckRuns[branch] = []github.CheckRun{
		{Name: "tests", Status: "completed", Conclusion: strPtr("success")},
	}
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(2*time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests once CI passed: %v", err)
	}
	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateClosed {
		t.Fatalf("state = %q, want closed: a passing check answers on its own, window or no window", st)
	}
}

// Waiting for CI is right up to the point where CI is never going to
// answer. A workflow waiting on an approval nobody gives, a self-hosted
// runner that never picks the job up, a provider that reported "queued"
// and went away: each reads PENDING every cycle forever, and PENDING is
// acted on by neither of the arms that merge or file a fix -- so without
// a deadline the task holds the head of its repo's queue for the life of
// the deployment, with nothing merged, nothing filed, nothing said to
// anyone, and everything queued behind it waiting too.
//
// orchestrator.SetCheckStallDeadline is the bound. Past it the queue says
// so on the task, stops driving it, and gets on with the next one --
// while the stuck pull request still merges the moment its checks do
// finish green, since giving up costs the queue position and the
// automatic fix, not the merge.
func TestSyncPullRequestsGivesUpOnAQueueHeadWhoseChecksNeverFinish(t *testing.T) {
	deadline := 2 * time.Hour
	defer orchestrator.SetCheckStallDeadline(deadline)()

	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	stuck, stuckBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	behind, _ := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t2", repo)

	setMergeable(sim, true)
	// The head's own check never leaves "queued". The task behind it has
	// nothing to wait for at all and is only kept from merging by being
	// second in the queue.
	sim.CheckRuns[stuckBranch] = []github.CheckRun{{Name: "deploy approval", Status: "queued"}}

	// Well inside the deadline: still waiting, still nothing said. This is
	// the same cycle a slow-but-honest CI run gets. The head is never
	// merged while PENDING -- but a check that has not so much as started
	// in all that time no longer holds the change behind it, which merges
	// past it (pendingStalled).
	for _, at := range []time.Time{baseTime, baseTime.Add(deadline / 2)} {
		if err := orchestrator.SyncPullRequests(ctx, store, client, at); err != nil {
			t.Fatalf("SyncPullRequests inside the deadline: %v", err)
		}
	}
	if mergedBranch(sim, stuckBranch) {
		t.Fatal("the queue head merged while its checks were still unfinished")
	}
	if st, err := store.State(ctx, behind.ID); err != nil || st != model.StateClosed {
		t.Fatalf("state of the task behind the stalled head = %q (%v), want closed: "+
			"a check that never started held it for an hour", st, err)
	}
	if bodies := commentBodies(t, ctx, store, stuck.ID); len(bodies) != 0 {
		t.Fatalf("expected no comments while inside the deadline, got %q", bodies)
	}
	obs, err := store.GetObservation(ctx, stuck.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs != nil && obs.MergeQueueBlockedAt != nil {
		t.Fatal("gave up on the queue head inside the deadline")
	}

	// Past it. The queue says what it was waiting on and marks the task
	// as needing a person -- but files no fix task: nothing has failed,
	// and there may be nothing in the pull request to fix at all.
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(deadline)); err != nil {
		t.Fatalf("SyncPullRequests past the deadline: %v", err)
	}
	obs, err = store.GetObservation(ctx, stuck.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || obs.MergeQueueBlockedAt == nil {
		t.Fatal("the queue never gave up on a head whose checks never finished")
	}
	if repairInFlightNow(t, ctx, store, stuck.ID) {
		t.Error("asked for an automatic repair of a pull request nothing had said was broken")
	}
	bodies := commentBodies(t, ctx, store, stuck.ID)
	if len(bodies) != 1 {
		t.Fatalf("expected exactly one comment explaining the wait, got %q", bodies)
	}
	if !strings.Contains(bodies[0], "deploy approval") {
		t.Errorf("the comment does not name the check that never finished:\n%s", bodies[0])
	}

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(deadline+time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests after the queue gave up: %v", err)
	}

	// Giving up is not abandoning. The checks finally report, green, and
	// the pull request the queue stopped driving still merges -- no
	// second comment, no fix task, no human step in between.
	sim.CheckRuns[stuckBranch] = []github.CheckRun{
		{Name: "deploy approval", Status: "completed", Conclusion: strPtr("success")},
	}
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(deadline+2*time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests once the stuck checks finished: %v", err)
	}
	st, err := store.State(ctx, stuck.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateClosed {
		t.Fatalf("state = %q, want closed: a blocked task still merges the moment it reads clean", st)
	}
	// One comment for the whole episode, not one per cycle: the task
	// stopped being its repo's queue head the moment it was blocked, so
	// nothing brings it back through the arm that said this.
	if bodies := commentBodies(t, ctx, store, stuck.ID); len(bodies) != 1 {
		t.Fatalf("expected the queue to say this once, got %q", bodies)
	}
}

// The clock runs per pull request, not per queue position. Every entry's
// checks are timed on every cycle, head or not, so a repo whose CI is
// wedged for everything in it clears in one deadline rather than in one
// deadline per queued task -- which is what timing only the head would
// have cost, on exactly the outage that produces the most queued tasks.
func TestSyncPullRequestsTimesStalledChecksBehindTheQueueHeadToo(t *testing.T) {
	deadline := 2 * time.Hour
	defer orchestrator.SetCheckStallDeadline(deadline)()

	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	first, firstBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	second, secondBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t2", repo)

	setMergeable(sim, true)
	stuck := []github.CheckRun{{Name: "tests", Status: "in_progress"}}
	sim.CheckRuns[firstBranch] = stuck
	sim.CheckRuns[secondBranch] = stuck

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests on the cycle both went pending: %v", err)
	}
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(deadline)); err != nil {
		t.Fatalf("SyncPullRequests once the deadline elapsed: %v", err)
	}

	obs, err := store.GetObservation(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || obs.MergeQueueBlockedAt == nil {
		t.Fatal("the queue head was not given up on")
	}
	// The second task was never the head, so nothing has been said about
	// it yet -- but its own clock has been running since the same cycle
	// the head's was, so the very next cycle, with no further waiting,
	// gives up on it too.
	obs, err = store.GetObservation(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs != nil && obs.MergeQueueBlockedAt != nil {
		t.Fatal("gave up on a task that was not the queue head")
	}

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(deadline)); err != nil {
		t.Fatalf("SyncPullRequests on the cycle the second task became head: %v", err)
	}
	obs, err = store.GetObservation(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || obs.MergeQueueBlockedAt == nil {
		t.Fatal("the second task waited out a second deadline of its own after being promoted")
	}
}

// Reading clean is a verdict about one commit, and the merge that acts on
// it is a second request: a push landing in between -- a human's own "push
// a fix by hand", a fix task merging into this branch, a redispatched task
// pushing again -- moves the branch after the verdict and before the
// merge. Merging then lands a commit whose CI this cycle never read, which
// is the one thing waiting for CI exists to prevent, so the merge names
// the commit it was passed and GitHub refuses it if the branch has moved.
//
// Refusing is cheap: the task keeps its place at the head of the queue and
// the next cycle judges whatever is there now on its own checks.
func TestSyncPullRequestsRefusesToMergeACommitThatLandedAfterTheVerdict(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, rest := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, rest, "t1", repo)

	setMergeable(sim, true)
	sim.CheckRuns[branch] = []github.CheckRun{
		{Name: "tests", Status: "completed", Conclusion: strPtr("success")},
	}

	client := &pushBeforeMerge{Client: rest, t: t, bare: sim.BareRepo, branch: branch}
	err := orchestrator.SyncPullRequests(ctx, store, client, baseTime)
	if err == nil {
		t.Fatal("the merge of a moved head was not refused")
	}
	var ghErr *github.Error
	if !errors.As(err, &ghErr) || ghErr.Status != 409 {
		t.Fatalf("SyncPullRequests: %v, want a 409 from the pinned merge", err)
	}
	if !client.pushed {
		t.Fatal("the test never landed its racing commit")
	}
	for _, pr := range sim.PullRequests {
		if pr.Merged {
			t.Fatal("merged a commit that landed after the health read")
		}
	}
	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateCompleted {
		t.Fatalf("state = %q, want still completed and still queued", st)
	}

	// Next cycle, with nothing else landing: the commit that arrived is
	// read, judged on the checks that are there for it, and merged.
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests on the cycle after the race: %v", err)
	}
	st, err = store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateClosed {
		t.Fatalf("state = %q, want closed once the new commit was judged and merged", st)
	}
}

// pushBeforeMerge is a real client with one instant wedged into it: a
// commit lands on the head branch just as the queue asks for the merge,
// which is the window between syncEntry's health read and its merge
// request. Once, so the cycle after it is an ordinary one.
type pushBeforeMerge struct {
	github.Client
	t      *testing.T
	bare   string
	branch string
	pushed bool
}

func (c *pushBeforeMerge) MergePullRequest(owner, repo string, number int, headSHA string) error {
	if !c.pushed {
		c.pushed = true
		pushAnotherCommit(c.t, c.bare, c.branch)
	}
	return c.Client.MergePullRequest(owner, repo, number, headSHA)
}

// backlogIDs is the backlog as a person reading a task list sees it:
// every task in Store.ListTasks' own order, which is also the order
// Store.Ready dispatches in.
func backlogIDs(t *testing.T, ctx context.Context, store *model.Store) []string {
	t.Helper()
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	ids := make([]string, len(tasks))
	for i, tk := range tasks {
		ids[i] = tk.ID
	}
	return ids
}

// placeInBacklog moves an already-filed task to an explicit position, so
// a test can state the backlog it starts from rather than rely on
// whatever OrderKey its helpers left behind.
func placeInBacklog(t *testing.T, ctx context.Context, store *model.Store, task model.Task, key float64) model.Task {
	t.Helper()
	task.OrderKey = key
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	return task
}

// TestSyncPullRequestsMovesTheQueueToTheFrontOfTheBacklog is the merge
// queue's own ordering made visible. The tasks whose pull requests are
// waiting to land go to the front of the backlog in the order the queue
// will act on them -- so a task list answers "what is grain about to
// finish, and in what order" without anyone opening a task, and answers
// it with the same order Store.Ready dispatches from. That is also what
// dispatches a repair promptly, now that the repair is the head task
// itself going back to work rather than a separate row filed in front of
// it.
func TestSyncPullRequestsMovesTheQueueToTheFrontOfTheBacklog(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	// An ordinary task nobody has run yet, sitting ahead of both queue
	// members to begin with, so the move is visible as a move.
	ordinary := placeInBacklog(t, ctx, store, filedTask(t, ctx, store, "t0", repo), 100)
	head, _ := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	head = placeInBacklog(t, ctx, store, head, 200)
	second, _ := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t2", repo)
	second = placeInBacklog(t, ctx, store, second, 300)

	// Both pull requests are conflicted: the head is sent back for repair
	// and neither of them merges, so the whole queue is still there to be
	// looked at afterwards.
	setMergeable(sim, false)

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if !repairInFlightNow(t, ctx, store, head.ID) {
		t.Fatal("expected the queue head to have been sent back for repair")
	}

	want := []string{head.ID, second.ID, ordinary.ID}
	if backlog := backlogIDs(t, ctx, store); !reflect.DeepEqual(backlog, want) {
		t.Fatalf("backlog = %v, want %v -- the queue in order, then ordinary work", backlog, want)
	}
}

// TestSyncPullRequestsTakesItsQueueHeadFromTheBacklogOrder is the other
// direction of the same fact: because the queue's order is the backlog's,
// a human who drags one waiting pull request above another really has
// changed which one merges first. Nothing about the tasks themselves
// differs here -- t1 was filed first and is still the older of the two --
// so a queue ordered by anything but position would go on repairing t1.
func TestSyncPullRequestsTakesItsQueueHeadFromTheBacklogOrder(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	first, firstBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	first = placeInBacklog(t, ctx, store, first, 100)
	second, secondBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t2", repo)
	second = placeInBacklog(t, ctx, store, second, 200)

	// The drag: t2 dropped at the head of the list, above t1.
	if err := store.Reorder(ctx, []string{second.ID}, nil, strPtr(first.ID)); err != nil {
		t.Fatal(err)
	}
	setMergeable(sim, true)
	sim.CheckRuns[firstBranch] = greenCheck
	sim.CheckRuns[secondBranch] = greenCheck

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}

	if !mergedBranch(sim, secondBranch) {
		t.Fatal("expected the task dragged to the front of the backlog to merge first")
	}
	if mergedBranch(sim, firstBranch) {
		t.Fatal("did not expect the task now behind it in the backlog to merge first")
	}
}

func strPtr(s string) *string { return &s }

// repairAskedAt is when the merge queue asked this task to repair its own
// branch (model.Observation.MergeQueueRepairAt), and fails the test if it
// never did.
func repairAskedAt(t *testing.T, ctx context.Context, store *model.Store, taskID string) time.Time {
	t.Helper()
	obs, err := store.GetObservation(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || obs.MergeQueueRepairAt == nil {
		t.Fatalf("no repair was asked of %s", taskID)
	}
	return *obs.MergeQueueRepairAt
}

// repairInFlightNow is model.Observation.RepairInFlight read fresh from
// the store -- "has the queue sent this task back to repair its own
// branch, and has that repair not finished yet", which is what most of
// these tests assert instead of the fix-task link the queue used to
// write.
func repairInFlightNow(t *testing.T, ctx context.Context, store *model.Store, taskID string) bool {
	t.Helper()
	obs, err := store.GetObservation(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	return obs.RepairInFlight()
}

// mergedBranch reports whether the sim has merged the pull request open
// from branch -- what a test asks when the question is which of several
// queued changes actually landed.
func mergedBranch(sim *githubsim.Sim, branch string) bool {
	for _, pr := range sim.PullRequests {
		if pr.Head == branch {
			return pr.Merged
		}
	}
	return false
}

// greenCheck is CI having finished and passed.
var greenCheck = []github.CheckRun{{Name: "tests", Status: "completed", Conclusion: strPtr("success")}}

// One conflict used to hold up every merge behind it. The head is
// repaired by an agent on its own branch -- a sandbox, a clone, a
// resolution, a push and another round of CI -- and until that landed,
// every green pull request queued behind it sat there waiting for a
// conflict that had nothing to do with it.
//
// So the queue passes a conflicted head over: it still gets its repair,
// on the same cycle, but it stops being what everything behind it is
// waiting for. The green change merges now, and the conflict merges when
// its resolution lands.
func TestSyncPullRequestsMergesAGreenEntryPastAConflictedHead(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	head, headBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	behind, behindBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t2", repo)

	setMergeable(sim, true)
	setBranchMergeable(sim, headBranch, false)
	sim.CheckRuns[headBranch] = greenCheck
	sim.CheckRuns[behindBranch] = greenCheck

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}

	// The head is being dealt with, exactly as it was before: it holds
	// its place in the queue and an agent is on its way to resolve it.
	if !repairInFlightNow(t, ctx, store, head.ID) {
		t.Fatal("the conflicted head was not sent back for repair")
	}
	if mergedBranch(sim, headBranch) {
		t.Fatal("a conflicted pull request was merged")
	}

	// And the change behind it did not wait for any of that.
	if !mergedBranch(sim, behindBranch) {
		t.Fatal("a green pull request queued behind a conflicted one did not merge")
	}
	st, err := store.State(ctx, behind.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateClosed {
		t.Fatalf("state = %q, want closed: its pull request merged", st)
	}
}

func TestSyncPullRequestsAllowsConfiguredMergesPerRepo(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	_, firstBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	_, secondBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t2", repo)
	setMergeable(sim, true)
	sim.CheckRuns[firstBranch] = greenCheck
	sim.CheckRuns[secondBranch] = greenCheck

	if err := orchestrator.SyncPullRequestsGuarding(ctx, store, client, nil,
		orchestrator.StateRepoGate{}, 2, baseTime); err != nil {
		t.Fatalf("SyncPullRequestsGuarding: %v", err)
	}
	if !mergedBranch(sim, firstBranch) || !mergedBranch(sim, secondBranch) {
		t.Fatalf("configured limit did not merge both ready entries: first=%v second=%v",
			mergedBranch(sim, firstBranch), mergedBranch(sim, secondBranch))
	}
}

// Being passed over is not a one-cycle affair: the repair the queue asked
// for is a whole agent run, and for as long as it is in flight the
// changes behind it keep merging. One per cycle, in queue order, the same
// as any other merge -- everything behind the one that just merged had
// its checks read against a base that merge has moved.
func TestSyncPullRequestsKeepsMergingWhileTheHeadIsRepaired(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	head, headBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	_, secondBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t2", repo)
	_, thirdBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t3", repo)

	setMergeable(sim, true)
	setBranchMergeable(sim, headBranch, false)
	for _, branch := range []string{headBranch, secondBranch, thirdBranch} {
		sim.CheckRuns[branch] = greenCheck
	}

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("first SyncPullRequests: %v", err)
	}
	if !mergedBranch(sim, secondBranch) {
		t.Fatal("the second entry did not merge past the conflicted head")
	}
	if mergedBranch(sim, thirdBranch) {
		t.Fatal("two entries merged on one cycle: the third was judged against a base the second had just moved")
	}

	// Second cycle: the head's repair is still in flight -- nobody has
	// pushed anything to that branch -- and the third entry is now what
	// the queue merges.
	if !repairInFlightNow(t, ctx, store, head.ID) {
		t.Fatal("the head's repair finished on its own")
	}
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("second SyncPullRequests: %v", err)
	}
	if !mergedBranch(sim, thirdBranch) {
		t.Fatal("the third entry did not merge while the head was still being repaired")
	}
	if mergedBranch(sim, headBranch) {
		t.Fatal("the head merged while it was still conflicted")
	}
}
