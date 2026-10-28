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
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// flakyJob is a failing job of the shape the engine reports, with the
// test it named.
func flakyJob() ci.JobRun {
	return ci.JobRun{
		Workflow: "checks", WorkflowPath: ".grain/workflows/checks.yml", JobID: "test", Name: "test",
		Conclusion:     ci.ConclusionFailure,
		FailureSummary: &ci.FailureSummary{Tests: []ci.TestFailure{{Name: "TestSometimesTimesOut"}}},
	}
}

func repairObservationOf(t *testing.T, ctx context.Context, store *model.Store, taskID string) *model.Observation {
	t.Helper()
	obs, err := store.GetObservation(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil {
		t.Fatalf("no observation for %s", taskID)
	}
	return obs
}

// A job that fails once and passes an uncached recheck of the same commit
// is a flake: the verdict is green, the flake is said on the task and
// marked in the test history, and the pull request spends no repair.
func TestAFlakyJobWhoseRecheckPassesSpendsNoRepair(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	task = grantCI(t, ctx, store, task)
	runner := newFakeCI()
	runner.answerRechecks(recheckPasses)
	first := startRequiredCI(t, ctx, store, client, runner, task, repo, branch)
	runner.finish(first.RunID, ci.ConclusionFailure, flakyJob())

	if err := runCIReconciler(t, ctx, store, client, runner); err != nil {
		t.Fatal(err)
	}

	status := ciStatusOf(t, ctx, store, task.ID)
	if !status.Green() {
		t.Fatalf("status = %+v, want green: the failing job passed its recheck", status)
	}
	if status.RecheckOf != first.RunID {
		t.Fatalf("the verdict does not say which run it rechecked: %+v", status)
	}
	if n := runner.rechecks(); n != 1 {
		t.Fatalf("%d uncached rechecks were asked for, want 1", n)
	}
	req := runner.lastRequest(t)
	if !req.Recheck || len(req.Jobs) != 1 || req.Jobs[0] != ".grain/workflows/checks.yml:test" {
		t.Fatalf("the recheck asked for %+v, want only the failing job, uncached", req)
	}
	if got := runner.flakesOf(first.RunID); len(got) != 1 || got[0] != ".grain/workflows/checks.yml:test" {
		t.Fatalf("the first run's job was not marked a flake in the test history: %v", got)
	}
	bodies := commentBodies(t, ctx, store, task.ID)
	flake := strings.Join(bodies, "\n")
	for _, want := range []string{"flake", "checks/test", "TestSometimesTimesOut", "No automatic repair"} {
		if !strings.Contains(flake, want) {
			t.Errorf("the flake comment does not say %q:\n%s", want, flake)
		}
	}

	greenOnGitHub(sim, branch)
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if repairInFlightNow(t, ctx, store, task.ID) {
		t.Fatal("a flake whose recheck passed was sent for repair")
	}
	if n := repairObservationOf(t, ctx, store, task.ID).RepairsSpent(); n != 0 {
		t.Fatalf("%d repair(s) spent on a flake, want none", n)
	}
}

// A job that fails both times is the change's own failure, and spends
// the first of its two automatic repairs -- which says so.
func TestAJobThatFailsTwiceSpendsARepair(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	task = grantCI(t, ctx, store, task)
	runner := newFakeCI() // a recheck reproduces the failure
	first := startRequiredCI(t, ctx, store, client, runner, task, repo, branch)
	runner.finish(first.RunID, ci.ConclusionFailure, flakyJob())

	if err := runCIReconciler(t, ctx, store, client, runner); err != nil {
		t.Fatal(err)
	}
	status := ciStatusOf(t, ctx, store, task.ID)
	if !status.Done() || status.Green() || status.Detail != "failure: checks/test" {
		t.Fatalf("status = %+v, want the job that failed twice as the verdict", status)
	}
	if got := runner.flakesOf(first.RunID); len(got) != 0 {
		t.Fatalf("a job that failed twice was marked a flake: %v", got)
	}

	greenOnGitHub(sim, branch)
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if !repairInFlightNow(t, ctx, store, task.ID) {
		t.Fatal("a job that failed twice was not sent for repair")
	}
	obs := repairObservationOf(t, ctx, store, task.ID)
	if obs.RepairsSpent() != 1 || obs.MergeQueueRepairFailures != "checks/test" || obs.MergeQueueRepairBase == "" {
		t.Fatalf("the repair recorded %d spent, failures %q, base %q; want 1, the failing job, and main's tip",
			obs.RepairsSpent(), obs.MergeQueueRepairFailures, obs.MergeQueueRepairBase)
	}
	bodies := commentBodies(t, ctx, store, task.ID)
	if last := bodies[len(bodies)-1]; !strings.Contains(last, "automatic repair 1 of 2") {
		t.Errorf("the repair does not say which attempt it is:\n%s", last)
	}
}

// retrigger_ci's recheck of just the failed jobs, after the merge queue's
// own recheck reproduced the red, is recorded on the task as the
// verdict's next sample (ui's recordTaskRecheck writes exactly this
// status). When it passes, the combined verdict is green and the gate
// lifts, without the jobs that had passed running again
// (grain/task-2124).
func TestAManualRecheckOfOnlyTheFailedJobsLiftsTheGateWhenItPasses(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	task = grantCI(t, ctx, store, task)
	runner := newFakeCI() // the automatic recheck reproduces the failure
	passed := ci.JobRun{Workflow: "checks", WorkflowPath: ".grain/workflows/checks.yml", JobID: "go", Name: "go",
		Conclusion: ci.ConclusionSuccess}
	first := startRequiredCI(t, ctx, store, client, runner, task, repo, branch)
	runner.finish(first.RunID, ci.ConclusionFailure, passed, flakyJob())
	if err := runCIReconciler(t, ctx, store, client, runner); err != nil {
		t.Fatal(err)
	}
	red := ciStatusOf(t, ctx, store, task.ID)
	if !red.Done() || red.Green() || red.Detail != "failure: checks/test" {
		t.Fatalf("status = %+v, want the job that failed twice as the verdict", red)
	}
	before := runner.started()

	runner.answerRechecks(recheckPending)
	recheck, err := runner.Start(ctx, ci.Request{Repo: repo.String(), Ref: task.BranchName(), Commit: red.Commit,
		Jobs: []string{".grain/workflows/checks.yml:test"}, Recheck: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ObserveField(ctx, task.ID, baseTime, func(o *model.Observation) {
		o.CI = &model.CIStatus{RunID: recheck.ID, Commit: red.Commit, StartedAt: red.StartedAt,
			Attempts: red.Attempts, RecheckOf: red.RunID, RecheckDetail: red.Detail}
	}); err != nil {
		t.Fatal(err)
	}
	if err := runCIReconciler(t, ctx, store, client, runner); err != nil {
		t.Fatal(err)
	}
	if status := ciStatusOf(t, ctx, store, task.ID); status.Done() {
		t.Fatalf("status = %+v while the recheck runs, want it pending", status)
	}

	rerun := flakyJob()
	rerun.Conclusion, rerun.FailureSummary = ci.ConclusionSuccess, nil
	runner.finish(recheck.ID, ci.ConclusionSuccess, rerun)
	if err := runCIReconciler(t, ctx, store, client, runner); err != nil {
		t.Fatal(err)
	}
	status := ciStatusOf(t, ctx, store, task.ID)
	if !status.Green() || status.RecheckOf != red.RunID {
		t.Fatalf("status = %+v, want green: the only failing job passed its recheck", status)
	}
	if n := runner.started() - before; n != 1 {
		t.Fatalf("%d runs were started after the red verdict, want only the recheck: the passed job is not rerun", n)
	}
}

func TestAManualRecheckAfterATimedOutAndFailedRunLiftsTheGateWhenItPasses(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	task = grantCI(t, ctx, store, task)
	runner := newFakeCI()
	passed := ci.JobRun{Workflow: "checks", WorkflowPath: ".grain/workflows/checks.yml", JobID: "lint", Name: "lint",
		Conclusion: ci.ConclusionSuccess}
	failed := ci.JobRun{Workflow: "checks", WorkflowPath: ".grain/workflows/checks.yml", JobID: "go", Name: "go",
		Conclusion: ci.ConclusionFailure}
	timedOut := ci.JobRun{Workflow: "checks", WorkflowPath: ".grain/workflows/checks.yml", JobID: "ui-e2e", Name: "ui e2e",
		Conclusion: ci.ConclusionCancelled}

	first := startRequiredCI(t, ctx, store, client, runner, task, repo, branch)
	runner.finish(first.RunID, ci.ConclusionCancelled, passed, failed, timedOut)
	if err := runCIReconciler(t, ctx, store, client, runner); err != nil {
		t.Fatal(err)
	}
	cancelled := ciStatusOf(t, ctx, store, task.ID)
	if !cancelled.Done() || cancelled.Conclusion != string(ci.ConclusionCancelled) ||
		cancelled.Detail != "cancelled: checks/go, checks/ui e2e" {
		t.Fatalf("status = %+v, want cancelled: checks/go, checks/ui e2e", cancelled)
	}

	runner.answerRechecks(recheckPending)
	recheck, err := runner.Start(ctx, ci.Request{Repo: repo.String(), Ref: task.BranchName(), Commit: cancelled.Commit,
		Jobs: []string{".grain/workflows/checks.yml:go", ".grain/workflows/checks.yml:ui-e2e"}, Recheck: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ObserveField(ctx, task.ID, baseTime, func(o *model.Observation) {
		o.CI = &model.CIStatus{RunID: recheck.ID, Commit: cancelled.Commit, StartedAt: cancelled.StartedAt,
			Attempts: cancelled.Attempts, RecheckOf: cancelled.RunID, RecheckDetail: cancelled.Detail}
	}); err != nil {
		t.Fatal(err)
	}

	failedPass, timedOutPass := failed, timedOut
	failedPass.Conclusion = ci.ConclusionSuccess
	timedOutPass.Conclusion = ci.ConclusionSuccess
	runner.finish(recheck.ID, ci.ConclusionSuccess, failedPass, timedOutPass)
	if err := runCIReconciler(t, ctx, store, client, runner); err != nil {
		t.Fatal(err)
	}
	status := ciStatusOf(t, ctx, store, task.ID)
	if !status.Green() || status.RecheckOf != cancelled.RunID {
		t.Fatalf("status = %+v, want green once both failed and timed-out jobs passed their recheck", status)
	}
}
