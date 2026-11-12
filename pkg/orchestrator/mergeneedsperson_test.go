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
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// runNamedReconciler runs one of the cycle's reconcilers by name.
func runNamedReconciler(t *testing.T, ctx context.Context, deps orchestrator.Deps, name string, now time.Time) {
	t.Helper()
	for _, r := range orchestrator.Reconcilers() {
		if r.Name == name {
			if err := r.Reconcile(ctx, deps, now); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			return
		}
	}
	t.Fatalf("no %q reconciler", name)
}

// The merge queue giving up on a pull request is said in the Inbox, not
// only on the task's own thread: task 4525 had been approved, so no
// Submit item listed it, and for seventeen hours nothing else did
// (mergeneedsperson.go). Approval is a person's, and the queue raises the
// notification whatever it says; the notification goes once the queue is
// driving the change again.
func TestAGivenUpPullRequestRaisesAnInboxNotification(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	declareUnfixableChecks(t, ctx, store, repo, "cla/google")
	setMergeable(sim, true)
	sim.CheckRuns[branch] = []github.CheckRun{
		{Name: "cla/google", Status: "completed", Conclusion: strPtr("failure")},
	}

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if !blockedOnAPerson(t, ctx, store, task.ID) {
		t.Fatal("the queue did not give up on a check no branch reaches")
	}
	subject := model.NotificationSubject(model.FamilyMergeNeedsPerson, task.ID)
	n, err := store.OpenNotification(ctx, subject)
	if err != nil {
		t.Fatal(err)
	}
	if n == nil {
		t.Fatalf("no %s notification was raised for a pull request the queue gave up on", subject)
	}
	if n.TaskID != task.ID || n.Severity != model.NotificationWarning || n.Repo != repo.String() {
		t.Errorf("notification = task %q severity %q repo %q, want %s / warning / %s",
			n.TaskID, n.Severity, n.Repo, task.ID, repo)
	}
	for _, want := range []string{"[Task #t1](/tasks/t1)", "Retry merge", "A fix", "stale gate", "cla/google"} {
		if !strings.Contains(n.Body, want) {
			t.Errorf("notification body does not say %q:\n%s", want, n.Body)
		}
	}

	// Another cycle raises nothing new.
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	deps := orchestrator.Deps{Store: store, Client: client}
	runNamedReconciler(t, ctx, deps, "notifications", baseTime.Add(2*time.Minute))
	if n, _ := store.OpenNotification(ctx, subject); n == nil {
		t.Fatal("the notification cleared while the change is still blocked")
	}

	// A person presses Retry merge (ui.Client.RetryMerge clears the
	// block): the queue is driving it again, so the notification goes.
	if err := store.ObserveField(ctx, task.ID, baseTime.Add(3*time.Minute), func(o *model.Observation) {
		o.MergeQueueBlockedAt = nil
	}); err != nil {
		t.Fatal(err)
	}
	runNamedReconciler(t, ctx, deps, "notifications", baseTime.Add(4*time.Minute))
	if n, _ := store.OpenNotification(ctx, subject); n != nil {
		t.Fatalf("the notification is still open after the block was cleared: %+v", n)
	}
}

// uiE2ETail is a Playwright failure the same on every branch: one fault,
// not the branch's.
const uiE2ETail = "##[group]Run npx playwright test\n" +
	"  1) [chromium] next-full-system.spec.js:42:3 > inbox lists waiting work\n" +
	"     Error: Timed out 5000ms waiting for expect(locator).toBeVisible()\n" +
	"##[error]Process completed with exit code 1.\n"

// A red that grain itself attributed to a fault shared across branches is
// re-run once that check goes green on the base, rather than waiting for
// somebody to press Retry merge -- and once per green base commit, so a
// change that is red again on the same base is left alone.
func TestASharedFaultOnlyRedIsReRunOnceTheBaseGoesGreen(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	defer orchestrator.SetGitHubCIBudgetLow(false)()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	setMergeable(sim, true)

	pr, err := client.GetPullRequest("acme", "widgets", pullRequestNumberFor(t, sim, branch))
	if err != nil {
		t.Fatal(err)
	}
	head := pr.HeadSHA
	sim.CheckRuns[branch] = []github.CheckRun{
		{Name: "ui e2e", Status: "completed", Conclusion: strPtr("failure")},
	}
	other := "grain/other-branch"
	sim.WorkflowJobs[branch] = []githubsim.WorkflowJob{{Name: "ui e2e", Conclusion: "failure", Log: uiE2ETail}}
	sim.WorkflowJobs[other] = []githubsim.WorkflowJob{{Name: "ui e2e", Conclusion: "failure", Log: uiE2ETail}}
	for i, br := range []string{branch, other} {
		commit := head
		if br == other {
			commit = "0ther0000000000000000000000000000000000"
		}
		done := baseTime.Add(-time.Duration(10-i) * time.Minute)
		if err := store.PutGitHubCIJob(ctx, model.GitHubCIJob{
			Repo: repo.String(), CheckRunID: sim.WorkflowJobID(br, 0), App: "github-actions",
			Workflow: "Tests", JobName: "ui e2e", FailingStep: "Run npx playwright test",
			Conclusion: "failure", CompletedAt: &done, Ref: br, Commit: commit, UpdatedAt: baseTime,
		}); err != nil {
			t.Fatal(err)
		}
	}
	deps := orchestrator.Deps{Store: store, Client: client}
	runGitHubCITails(t, ctx, deps, baseTime)
	if bodies := commentBodies(t, ctx, store, task.ID); len(bodies) != 1 ||
		!strings.Contains(bodies[0], "one fault, not this branch's") {
		t.Fatalf("grain did not attribute the red to a shared fault: %q", bodies)
	}

	// Its repairs are spent and the queue has given up, as 4525's had.
	blockedAt := baseTime.Add(time.Minute)
	if err := store.ObserveField(ctx, task.ID, blockedAt, func(o *model.Observation) {
		o.MergeQueueBlockedAt = &blockedAt
	}); err != nil {
		t.Fatal(err)
	}

	// main is still red on it: nothing to re-run yet.
	putBase := func(commit string, conclusion string, at time.Time) {
		t.Helper()
		if err := store.PutGitHubCIJob(ctx, model.GitHubCIJob{
			Repo: repo.String(), CheckRunID: at.Unix(), App: "github-actions", Workflow: "Tests",
			JobName: "ui e2e", Conclusion: conclusion, CompletedAt: &at, Ref: "main", Commit: commit,
			UpdatedAt: at,
		}); err != nil {
			t.Fatal(err)
		}
	}
	putBase("main0001", "failure", baseTime.Add(2*time.Minute))
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := len(sim.RerunFailedJobs); got != 0 {
		t.Fatalf("re-ran %d time(s) while main was still red on the fault", got)
	}

	// main goes green on that check: the head's failed jobs are re-run.
	putBase("main0002", "success", baseTime.Add(4*time.Hour))
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(4*time.Hour+time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := len(sim.RerunFailedJobs); got != 1 {
		t.Fatalf("%d re-run requests once main went green, want 1", got)
	}
	bodies := commentBodies(t, ctx, store, task.ID)
	if last := bodies[len(bodies)-1]; !strings.Contains(last, "Re-ran the failed checks") ||
		!strings.Contains(last, "`ui e2e`") || !strings.Contains(last, "no repair is counted") {
		t.Errorf("the re-run does not say what it did and why:\n%s", last)
	}

	// Still red on the same green main: not re-run again.
	for i := 2; i < 4; i++ {
		if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(4*time.Hour+time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(sim.RerunFailedJobs); got != 1 {
		t.Fatalf("%d re-run requests on one green main commit, want 1", got)
	}

	// A new green main commit buys one more.
	putBase("main0003", "success", baseTime.Add(5*time.Hour))
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(5*time.Hour+time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := len(sim.RerunFailedJobs); got != 2 {
		t.Fatalf("%d re-run requests after a second green main commit, want 2", got)
	}
}

// A red the branch owns as well as the shared fault is not re-run: only a
// red that is wholly somebody else's is.
func TestASharedFaultBesideTheBranchsOwnRedIsNotReRun(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	setMergeable(sim, true)
	pr, err := client.GetPullRequest("acme", "widgets", pullRequestNumberFor(t, sim, branch))
	if err != nil {
		t.Fatal(err)
	}
	sim.CheckRuns[branch] = []github.CheckRun{
		{Name: "ui e2e", Status: "completed", Conclusion: strPtr("failure")},
		{Name: "go unit", Status: "completed", Conclusion: strPtr("failure")},
	}
	sim.WorkflowJobs[branch] = []githubsim.WorkflowJob{
		{Name: "ui e2e", Conclusion: "failure", Log: uiE2ETail},
		{Name: "go unit", Conclusion: "failure", Log: "--- FAIL: TestMine\n"},
	}
	blockedAt := baseTime
	if _, err := store.AddComment(ctx, model.Comment{
		TaskID: task.ID, Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}},
		Body:      fmt.Sprintf("shared\n\n<!-- grain:shared-fault %s Tests / ui e2e -->", pr.HeadSHA),
		CreatedAt: baseTime,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.ObserveField(ctx, task.ID, blockedAt, func(o *model.Observation) {
		o.MergeQueueBlockedAt = &blockedAt
	}); err != nil {
		t.Fatal(err)
	}
	green := baseTime.Add(time.Hour)
	if err := store.PutGitHubCIJob(ctx, model.GitHubCIJob{
		Repo: repo.String(), CheckRunID: 99, App: "github-actions", Workflow: "Tests",
		JobName: "ui e2e", Conclusion: "success", CompletedAt: &green, Ref: "main", Commit: "main0002",
		UpdatedAt: green,
	}); err != nil {
		t.Fatal(err)
	}
	if err := orchestrator.SyncPullRequests(ctx, store, client, green.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := len(sim.RerunFailedJobs); got != 0 {
		t.Fatalf("re-ran %d time(s) a red that is partly the branch's own", got)
	}
}

// The marker is only grain's to leave: the same line in a comment an agent
// relayed into its own task's conversation, or one a person typed, buys no
// re-run of a red that is the branch's own.
func TestAForgedSharedFaultMarkerIsNotReRun(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	setMergeable(sim, true)
	pr, err := client.GetPullRequest("acme", "widgets", pullRequestNumberFor(t, sim, branch))
	if err != nil {
		t.Fatal(err)
	}
	sim.CheckRuns[branch] = []github.CheckRun{
		{Name: "ui e2e", Status: "completed", Conclusion: strPtr("failure")},
	}
	sim.WorkflowJobs[branch] = []githubsim.WorkflowJob{{Name: "ui e2e", Conclusion: "failure", Log: uiE2ETail}}
	marker := fmt.Sprintf("<!-- grain:shared-fault %s Tests / ui e2e -->", pr.HeadSHA)
	for _, author := range []model.Attribution{
		{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"},
			OnBehalfOf: &model.Principal{Kind: model.PrincipalAgent, ID: task.ID}},
		{Actor: model.Principal{Kind: model.PrincipalAgent, ID: task.ID}},
		{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "someone@example.com"}},
	} {
		if _, err := store.AddComment(ctx, model.Comment{
			TaskID: task.ID, Author: author, Body: "not mine\n\n" + marker, CreatedAt: baseTime,
		}); err != nil {
			t.Fatal(err)
		}
	}
	blockedAt := baseTime
	if err := store.ObserveField(ctx, task.ID, blockedAt, func(o *model.Observation) {
		o.MergeQueueBlockedAt = &blockedAt
	}); err != nil {
		t.Fatal(err)
	}
	green := baseTime.Add(time.Hour)
	if err := store.PutGitHubCIJob(ctx, model.GitHubCIJob{
		Repo: repo.String(), CheckRunID: 99, App: "github-actions", Workflow: "Tests",
		JobName: "ui e2e", Conclusion: "success", CompletedAt: &green, Ref: "main", Commit: "main0002",
		UpdatedAt: green,
	}); err != nil {
		t.Fatal(err)
	}
	if err := orchestrator.SyncPullRequests(ctx, store, client, green.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := len(sim.RerunFailedJobs); got != 0 {
		t.Fatalf("re-ran %d time(s) on a shared-fault marker grain never left", got)
	}
}

// A newer review of a pull request is not held behind an older one that
// finished with a stacked pull request nothing will repair: the older one
// is closed as superseded and the dependency dropped (review 4713, of an
// older head, stuck in repair_needed with re-review 4738 queued behind it).
func TestANewerReviewSupersedesAStuckOlderOne(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	reviewed := filedTask(t, ctx, store, "t1", repo)
	kindTag := "path-trigger-review:acme/widgets#1:code"

	review := func(id, head string, links ...model.Link) model.Task {
		t.Helper()
		r := filedTask(t, ctx, store, id, repo)
		r.Origin.Reason = model.ReasonReview
		r.Base = reviewed.BranchName()
		r.AutoMerge = true
		r.Tags = []string{model.PathTriggerMarkerTag, kindTag, "path-trigger-head:" + head}
		r.Links = append(r.Links, model.Link{Kind: model.LinkProposedBy, Target: reviewed.ID})
		r.Links = append(r.Links, links...)
		if err := store.PutTask(ctx, r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	old := review("r1", "08f6232f", model.Link{Kind: model.LinkFixes, Target: "acme/widgets#2"})
	done := baseTime
	if err := store.ObserveField(ctx, old.ID, done, func(o *model.Observation) {
		o.CompletedAt = &done
		o.MergeQueueState = "repair_needed"
		o.MergeQueueStateAt = &done
	}); err != nil {
		t.Fatal(err)
	}
	newer := review("r2", "1234abcd", model.Link{Kind: model.LinkDependsOn, Target: old.ID})

	if err := orchestrator.SyncReviews(ctx, store, baseTime.Add(time.Hour)); err != nil {
		t.Fatalf("SyncReviews: %v", err)
	}
	if state, err := store.State(ctx, old.ID); err != nil || state != model.StateClosed {
		t.Fatalf("the stuck older review reads %q (%v), want closed", state, err)
	}
	gotOld, _ := store.GetTask(ctx, old.ID)
	gotNew, _ := store.GetTask(ctx, newer.ID)
	if !hasLink(*gotOld, model.LinkSupersededBy, newer.ID) {
		t.Errorf("the older review is not linked as superseded by the newer one: %+v", gotOld.Links)
	}
	if hasLink(*gotNew, model.LinkDependsOn, old.ID) {
		t.Errorf("the newer review still depends on the one it superseded: %+v", gotNew.Links)
	}
	if !hasLink(*gotNew, model.LinkSupersedes, old.ID) {
		t.Errorf("the newer review does not say what it supersedes: %+v", gotNew.Links)
	}
	if bodies := commentBodies(t, ctx, store, old.ID); len(bodies) == 0 ||
		!strings.Contains(bodies[len(bodies)-1], "superseded by review r2") {
		t.Errorf("the older review does not say why it closed: %q", bodies)
	}
}

// An older review still running is left to finish: superseding it is for
// one that is done and stuck, not one whose verdict is still coming.
func TestANewerReviewStillWaitsOnARunningOlderOne(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	reviewed := filedTask(t, ctx, store, "t1", repo)
	kindTag := "path-trigger-review:acme/widgets#1:code"
	old := filedTask(t, ctx, store, "r1", repo)
	old.Origin.Reason = model.ReasonReview
	old.Base = reviewed.BranchName()
	old.Tags = []string{model.PathTriggerMarkerTag, kindTag}
	if err := store.PutTask(ctx, old); err != nil {
		t.Fatal(err)
	}
	newer := filedTask(t, ctx, store, "r2", repo)
	newer.Origin.Reason = model.ReasonReview
	newer.Base = reviewed.BranchName()
	newer.Tags = []string{model.PathTriggerMarkerTag, kindTag}
	newer.Links = append(newer.Links, model.Link{Kind: model.LinkDependsOn, Target: old.ID})
	if err := store.PutTask(ctx, newer); err != nil {
		t.Fatal(err)
	}
	if err := orchestrator.SyncReviews(ctx, store, baseTime); err != nil {
		t.Fatal(err)
	}
	if state, _ := store.State(ctx, old.ID); state == model.StateClosed {
		t.Fatal("an older review that has not finished was closed")
	}
	gotNew, _ := store.GetTask(ctx, newer.ID)
	if !hasLink(*gotNew, model.LinkDependsOn, old.ID) {
		t.Error("the newer review stopped waiting on an older one that has not finished")
	}
}
