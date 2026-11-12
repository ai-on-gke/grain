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

package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// onGitHubActions makes repo a ci_mode: github repository, whose default
// branch (branchClient's) is main.
func onGitHubActions(t *testing.T, ctx context.Context, store *model.Store, repo model.RepoRef) Deps {
	t.Helper()
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CIMode: model.CIModeGitHub}); err != nil {
		t.Fatal(err)
	}
	return Deps{Store: store, Client: &branchClient{}}
}

// testsRun is a concluded tests.yml run on ref, its jobs recorded the way
// ensureGitHubWorkflowRunJobs records them.
func testsRun(t *testing.T, ctx context.Context, store *model.Store, repo model.RepoRef, id int64, ref,
	conclusion string, at time.Time, jobs map[string]string) (ConcludedGitHubWorkflowRun, []model.GitHubCIJob) {

	t.Helper()
	run := ConcludedGitHubWorkflowRun{
		Repo: repo, WorkflowName: "Tests", WorkflowPath: ".github/workflows/tests.yml", RunID: id, Attempt: 1,
		Conclusion: conclusion, HeadSHA: fmt.Sprintf("%040x", id), Ref: ref, Event: "push",
		HTMLURL:     fmt.Sprintf("https://github.example/acme/widgets/actions/runs/%d", id),
		CompletedAt: &at,
	}
	var out []model.GitHubCIJob
	n := int64(0)
	for name, c := range jobs {
		n++
		j := model.GitHubCIJob{
			Repo: repo.String(), CheckRunID: id*100 + n, Source: model.GitHubCISource, App: model.GitHubActionsApp,
			Ref: ref, Commit: run.HeadSHA, Workflow: "Tests", RunID: id, RunAttempt: 1, JobName: name,
			Conclusion: c, CompletedAt: &at, RunURL: run.HTMLURL, UpdatedAt: at,
		}
		if j.Failed() {
			j.FailingStep, j.FailureTail = "go test", "--- FAIL: TestOnlyBuildersNameTheCIService ("+name+")"
		}
		if err := store.PutGitHubCIJob(ctx, j); err != nil {
			t.Fatal(err)
		}
		out = append(out, j)
	}
	return run, out
}

func baseRepairs(t *testing.T, ctx context.Context, store *model.Store, repo model.RepoRef) []string {
	t.Helper()
	ids, err := store.OpenTasksWithTag(ctx, model.GitHubBaseRepairTag(repo))
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

// A red tests.yml on main files exactly one repair at the train's priority
// with the failing jobs, their tails, the run and the commit; a cancelled
// run neither files one nor closes it; further red runs are comments on it
// with its priority kept; the next green run on main closes it.
func TestARedDefaultBranchOnGitHubActionsHasExactlyOneRepairUntilGreen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	deps := onGitHubActions(t, ctx, store, repo)
	now := time.Date(2026, 11, 11, 18, 0, 0, 0, time.UTC)

	// Cancelled by the next push: not red.
	cancelled, cancelledJobs := testsRun(t, ctx, store, repo, 10, "main", "cancelled", now,
		map[string]string{"go unit": "cancelled", "lint": "success"})
	if err := handleGitHubBaseVerdict(ctx, deps, cancelled, cancelledJobs, now); err != nil {
		t.Fatal(err)
	}
	if ids := baseRepairs(t, ctx, store, repo); len(ids) != 0 {
		t.Fatalf("a cancelled run filed repairs %v", ids)
	}

	run, jobs := testsRun(t, ctx, store, repo, 11, "main", "failure", now,
		map[string]string{"go unit": "failure", "ui e2e": "failure", "lint": "success"})
	if err := handleGitHubBaseVerdict(ctx, deps, run, jobs, now); err != nil {
		t.Fatal(err)
	}
	ids := baseRepairs(t, ctx, store, repo)
	if len(ids) != 1 {
		t.Fatalf("a red tests.yml on main filed %d repairs, want 1", len(ids))
	}
	repair, err := store.GetTask(ctx, ids[0])
	if err != nil || repair == nil {
		t.Fatal(err)
	}
	if repair.Priority != GitHubBaseRepairPriority || GitHubBaseRepairPriority != Stage1RepairPriority {
		t.Errorf("repair priority %d, want the train's %d", repair.Priority, Stage1RepairPriority)
	}
	for _, want := range []string{"go unit", "ui e2e", "TestOnlyBuildersNameTheCIService", run.HTMLURL, run.HeadSHA, "tests.yml", "`main`"} {
		if !strings.Contains(repair.Body, want) {
			t.Errorf("repair body does not name %q:\n%s", want, repair.Body)
		}
	}
	if strings.Contains(repair.Body, "**lint**") {
		t.Errorf("repair body names a green job:\n%s", repair.Body)
	}
	if !strings.Contains(repair.Title, "tests.yml") || !strings.Contains(repair.Title, "main") {
		t.Errorf("repair title %q does not name the workflow and branch", repair.Title)
	}
	g, err := store.TaskGoal(ctx, repair.ID)
	if err != nil || g == nil || g.Kind != model.GoalNightlyGreen || g.OnMet != model.GoalCloseOnMet || g.Spec.Workflow != "Tests" {
		t.Fatalf("repair goal = %+v (%v), want nightly-green on Tests, on_met close", g, err)
	}

	// Somebody lowers its priority; the same run delivered twice (webhook
	// and poll) and a second red run update it, never a second repair.
	if err := store.UpdateTask(ctx, repair.ID, func(t *model.Task) error { t.Priority = 3; return nil }); err != nil {
		t.Fatal(err)
	}
	later := now.Add(time.Hour)
	run2, jobs2 := testsRun(t, ctx, store, repo, 12, "main", "failure", later,
		map[string]string{"go e2e": "failure"})
	for range 2 {
		if err := handleGitHubBaseVerdict(ctx, deps, run2, jobs2, later); err != nil {
			t.Fatal(err)
		}
	}
	if ids := baseRepairs(t, ctx, store, repo); len(ids) != 1 || ids[0] != repair.ID {
		t.Fatalf("a further red run left repairs %v, want only %s", ids, repair.ID)
	}
	repair, _ = store.GetTask(ctx, repair.ID)
	if repair.Priority != GitHubBaseRepairPriority {
		t.Errorf("an updated repair is at priority %d, want %d kept", repair.Priority, GitHubBaseRepairPriority)
	}
	comments, _ := store.Comments(ctx, repair.ID)
	found := false
	for _, c := range comments {
		found = found || (strings.Contains(c.Body, "go e2e") && strings.Contains(c.Body, run2.HTMLURL))
	}
	if !found {
		t.Errorf("the second red run's failures were not appended to %s", repair.ID)
	}

	// A red run on a pull request's branch, and a red nightly on main, are
	// not this repair's.
	other, otherJobs := testsRun(t, ctx, store, repo, 13, "feature-x", "failure", later,
		map[string]string{"go unit": "failure"})
	if err := handleGitHubBaseVerdict(ctx, deps, other, otherJobs, later); err != nil {
		t.Fatal(err)
	}
	nightly, nightlyJobs := stage1Run(t, ctx, store, repo, 14, "main", "schedule", "failure", later,
		map[string]string{"go-long": "failure"})
	if err := handleGitHubBaseVerdict(ctx, deps, nightly, nightlyJobs, later); err != nil {
		t.Fatal(err)
	}
	if n, _ := store.Comments(ctx, repair.ID); len(n) != len(comments) {
		t.Errorf("a run off main or of another workflow was appended to %s", repair.ID)
	}

	// A cancelled run after the red ones does not close it, either by the
	// verdict or by the goal's own reading.
	c2At := later.Add(10 * time.Minute)
	c2, c2Jobs := testsRun(t, ctx, store, repo, 15, "main", "cancelled", c2At,
		map[string]string{"go unit": "cancelled", "ui e2e": "success"})
	if err := handleGitHubBaseVerdict(ctx, deps, c2, c2Jobs, c2At); err != nil {
		t.Fatal(err)
	}
	if err := reconcileTaskGoal(ctx, deps, *g, c2At); err != nil {
		t.Fatal(err)
	}
	if st, _ := store.State(ctx, repair.ID); st == model.StateClosed {
		t.Fatalf("a cancelled run closed repair %s", repair.ID)
	}

	// The next fully green run on main closes it.
	green := later.Add(time.Hour)
	run3, jobs3 := testsRun(t, ctx, store, repo, 16, "main", "success", green,
		map[string]string{"go unit": "success", "ui e2e": "success", "go e2e": "success", "real vm": "skipped"})
	if err := handleGitHubBaseVerdict(ctx, deps, run3, jobs3, green); err != nil {
		t.Fatal(err)
	}
	if st, _ := store.State(ctx, repair.ID); st != model.StateClosed {
		t.Fatalf("a green run on main left repair %s %s, want closed", repair.ID, st)
	}
}

// A repository whose CI is grain's has basebranchci.go's base-green
// repair; this one never files there.
func TestADefaultBranchRepairNeedsGitHubActionsCI(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "gadgets"}
	deps := Deps{Store: store, Client: &branchClient{}}
	now := time.Date(2026, 11, 11, 18, 0, 0, 0, time.UTC)
	run, jobs := testsRun(t, ctx, store, repo, 21, "main", "failure", now, map[string]string{"go unit": "failure"})
	if err := handleGitHubBaseVerdict(ctx, deps, run, jobs, now); err != nil {
		t.Fatal(err)
	}
	if ids := baseRepairs(t, ctx, store, repo); len(ids) != 0 {
		t.Fatalf("a repository on grain CI got default-branch repairs %v", ids)
	}
}

// A repair queued and never started for an hour raises
// pipeline.base-red-unattended naming it; it clears once the repair
// closes. A Stage 1 green does not clear it.
func TestAnUnattendedDefaultBranchRepairRaisesANotification(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	deps := onGitHubActions(t, ctx, store, repo)
	now := time.Date(2026, 11, 11, 18, 0, 0, 0, time.UTC)
	run, jobs := testsRun(t, ctx, store, repo, 31, "main", "failure", now, map[string]string{"go unit": "failure"})
	if err := handleGitHubBaseVerdict(ctx, deps, run, jobs, now); err != nil {
		t.Fatal(err)
	}
	ids := baseRepairs(t, ctx, store, repo)
	if len(ids) != 1 {
		t.Fatalf("want one repair, have %v", ids)
	}
	subject := gitHubBaseRepairSubject(repo)

	if err := reconcileGitHubBaseRepairs(ctx, deps, now.Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if n, _ := store.OpenNotification(ctx, subject); n != nil {
		t.Fatalf("raised %s after 30 minutes queued: %s", subject, n.Body)
	}
	if err := reconcileGitHubBaseRepairs(ctx, deps, now.Add(61*time.Minute)); err != nil {
		t.Fatal(err)
	}
	n, err := store.OpenNotification(ctx, subject)
	if err != nil || n == nil {
		t.Fatalf("no %s after an hour queued (%v)", subject, err)
	}
	if n.TaskID != ids[0] || !strings.Contains(n.Body, ids[0]) || !strings.Contains(n.Body, "never started") ||
		n.Family != model.FamilyBaseRedUnattended {
		t.Errorf("notification does not name the repair and what it waits on: %+v", n)
	}

	// Stage 1 going green is not this branch going green.
	optInStage1(t, ctx, store, repo)
	nightlyAt := now.Add(70 * time.Minute)
	nightly, nightlyJobs := stage1Run(t, ctx, store, repo, 32, "v3.6.0-rc.7", "push", "success", nightlyAt,
		map[string]string{"go-long": "success"})
	if err := handleStage1Verdict(ctx, deps, nightly, nightlyJobs, nightlyAt); err != nil {
		t.Fatal(err)
	}
	if st, _ := store.State(ctx, ids[0]); st == model.StateClosed {
		t.Fatalf("a green Stage 1 closed default-branch repair %s", ids[0])
	}
	if n, _ := store.OpenNotification(ctx, subject); n == nil {
		t.Fatalf("a green Stage 1 cleared %s", subject)
	}

	closeAt := now.Add(2 * time.Hour)
	if err := closeTaskBy(ctx, store, ids[0], closeAt, githubBaseRepairer, "test", func(o *model.Observation) { o.ClosedAt = &closeAt }); err != nil {
		t.Fatal(err)
	}
	if err := reconcileGitHubBaseRepairs(ctx, deps, closeAt); err != nil {
		t.Fatal(err)
	}
	if n, _ := store.OpenNotification(ctx, subject); n != nil {
		t.Fatalf("%s still open once the repair closed", subject)
	}
}

// Runs that keep being cancelled by the next push for six hours, with no
// finished verdict between them, raise pipeline.base-no-verdict; the next
// finished run clears it.
func TestADefaultBranchWithNoFinishedVerdictRaisesANotice(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	deps := onGitHubActions(t, ctx, store, repo)
	start := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	subject := gitHubBaseNoVerdictSubject(repo)

	testsRun(t, ctx, store, repo, 40, "main", "success", start, map[string]string{"go unit": "success"})
	for i := range 8 {
		at := start.Add(time.Duration(i+1) * 50 * time.Minute)
		testsRun(t, ctx, store, repo, int64(41+i), "main", "cancelled", at,
			map[string]string{"go unit": "cancelled", "lint": "success"})
	}
	// A pull request's finished runs are not the default branch's verdict.
	testsRun(t, ctx, store, repo, 60, "feature-x", "success", start.Add(5*time.Hour), map[string]string{"go unit": "success"})

	if err := reconcileGitHubBaseRepairs(ctx, deps, start.Add(5*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n, _ := store.OpenNotification(ctx, subject); n != nil {
		t.Fatalf("raised %s five hours after the last verdict: %s", subject, n.Body)
	}
	if err := reconcileGitHubBaseRepairs(ctx, deps, start.Add(7*time.Hour)); err != nil {
		t.Fatal(err)
	}
	n, err := store.OpenNotification(ctx, subject)
	if err != nil || n == nil {
		t.Fatalf("no %s seven hours after the last verdict (%v)", subject, err)
	}
	if n.Family != model.FamilyBaseNoVerdict || !strings.Contains(n.Body, "cancelled") {
		t.Errorf("notice does not say why: %+v", n)
	}

	finished := start.Add(7*time.Hour + 30*time.Minute)
	testsRun(t, ctx, store, repo, 70, "main", "failure", finished, map[string]string{"go unit": "failure"})
	if err := reconcileGitHubBaseRepairs(ctx, deps, finished.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if n, _ := store.OpenNotification(ctx, subject); n != nil {
		t.Fatalf("%s still open after a finished run", subject)
	}
}

// A pull request's run whose head branch is main, or a fork's, is not the
// default branch: it neither files a repair nor closes one, by its verdict
// or through the goal's reading of its job rows.
func TestADefaultBranchRepairIgnoresPullRequestAndForkRuns(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	deps := onGitHubActions(t, ctx, store, repo)
	now := time.Date(2026, 11, 11, 18, 0, 0, 0, time.UTC)

	pr, prJobs := testsRun(t, ctx, store, repo, 20, "main", "failure", now, map[string]string{"go unit": "failure"})
	pr.Event = "pull_request"
	fork, forkJobs := testsRun(t, ctx, store, repo, 21, "main", "failure", now, map[string]string{"go unit": "failure"})
	fork.HeadRepo = "mallory/widgets"
	for _, r := range []struct {
		run  ConcludedGitHubWorkflowRun
		jobs []model.GitHubCIJob
	}{{pr, prJobs}, {fork, forkJobs}} {
		if err := handleGitHubBaseVerdict(ctx, deps, r.run, r.jobs, now); err != nil {
			t.Fatal(err)
		}
	}
	if ids := baseRepairs(t, ctx, store, repo); len(ids) != 0 {
		t.Fatalf("a pull request's or a fork's red run filed repairs %v", ids)
	}

	// With main really red, a fork's pull request opened from its own main
	// goes green: its job rows say "main" too, but neither its verdict nor
	// the goal's reading of those rows closes the repair.
	red, redJobs := testsRun(t, ctx, store, repo, 22, "main", "failure", now, map[string]string{"go unit": "failure"})
	if err := handleGitHubBaseVerdict(ctx, deps, red, redJobs, now); err != nil {
		t.Fatal(err)
	}
	ids := baseRepairs(t, ctx, store, repo)
	if len(ids) != 1 {
		t.Fatalf("a red push to main filed %v, want one repair", ids)
	}
	g, err := store.TaskGoal(ctx, ids[0])
	if err != nil || g == nil {
		t.Fatalf("repair %s has no goal (%v)", ids[0], err)
	}
	later := now.Add(time.Hour)
	forkGreen, forkGreenJobs := testsRun(t, ctx, store, repo, 23, "main", "success", later, map[string]string{"go unit": "success"})
	forkGreen.Event, forkGreen.HeadRepo = "pull_request", "mallory/widgets"
	if err := handleGitHubBaseVerdict(ctx, deps, forkGreen, forkGreenJobs, later); err != nil {
		t.Fatal(err)
	}
	if err := reconcileTaskGoal(ctx, deps, *g, later); err != nil {
		t.Fatal(err)
	}
	if st, _ := store.State(ctx, ids[0]); st == model.StateClosed {
		t.Fatalf("a fork's green pull request run closed repair %s", ids[0])
	}

	// A green push that concluded before the repair was filed, delivered
	// late, does not close it either; the next green push does.
	stale, staleJobs := testsRun(t, ctx, store, repo, 24, "main", "success", now.Add(-time.Minute), map[string]string{"go unit": "success"})
	if err := handleGitHubBaseVerdict(ctx, deps, stale, staleJobs, later); err != nil {
		t.Fatal(err)
	}
	if st, _ := store.State(ctx, ids[0]); st == model.StateClosed {
		t.Fatalf("a green run older than repair %s closed it", ids[0])
	}
	pushed, pushedJobs := testsRun(t, ctx, store, repo, 25, "main", "success", later, map[string]string{"go unit": "success"})
	if err := handleGitHubBaseVerdict(ctx, deps, pushed, pushedJobs, later); err != nil {
		t.Fatal(err)
	}
	if st, _ := store.State(ctx, ids[0]); st != model.StateClosed {
		t.Fatalf("a green push to main left repair %s %s, want closed", ids[0], st)
	}
}

// tests.yml gates by its file whatever its name: a repair of one named
// "Go" is not closed by a fork's green pull request rows on "main" either,
// though the goal names only "Go".
func TestADefaultBranchRepairOfARenamedWorkflowIgnoresForkRuns(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	deps := onGitHubActions(t, ctx, store, repo)
	now := time.Date(2026, 11, 11, 18, 0, 0, 0, time.UTC)

	named := func(id int64, conclusion string, at time.Time, jobs map[string]string) (ConcludedGitHubWorkflowRun, []model.GitHubCIJob) {
		run, rows := testsRun(t, ctx, store, repo, id, "main", conclusion, at, jobs)
		run.WorkflowName = "Go"
		for i := range rows {
			rows[i].Workflow = "Go"
			if err := store.PutGitHubCIJob(ctx, rows[i]); err != nil {
				t.Fatal(err)
			}
		}
		return run, rows
	}
	red, redJobs := named(30, "failure", now, map[string]string{"go unit": "failure"})
	if err := handleGitHubBaseVerdict(ctx, deps, red, redJobs, now); err != nil {
		t.Fatal(err)
	}
	ids := baseRepairs(t, ctx, store, repo)
	if len(ids) != 1 {
		t.Fatalf("a red tests.yml named Go on main filed %v, want one repair", ids)
	}
	g, err := store.TaskGoal(ctx, ids[0])
	if err != nil || g == nil || g.Spec.Workflow != "Go" {
		t.Fatalf("repair %s goal = %+v (%v), want nightly-green on Go", ids[0], g, err)
	}

	later := now.Add(time.Hour)
	forkGreen, forkGreenJobs := named(31, "success", later, map[string]string{"go unit": "success"})
	forkGreen.Event, forkGreen.HeadRepo = "pull_request", "mallory/widgets"
	if err := handleGitHubBaseVerdict(ctx, deps, forkGreen, forkGreenJobs, later); err != nil {
		t.Fatal(err)
	}
	if err := reconcileTaskGoal(ctx, deps, *g, later); err != nil {
		t.Fatal(err)
	}
	if st, _ := store.State(ctx, ids[0]); st == model.StateClosed {
		t.Fatalf("a fork's green pull request run closed repair %s of a workflow named Go", ids[0])
	}
}

// A running default-branch repair whose goal is met by a green push stays met
// across reconcileTaskGoals cycles -- even when a fork's pull request on main
// fails during the pause grace -- cancels its run at the end of the grace, and
// closes once the run exits; fork runs also never reset ResumesSinceProgress.
func TestARunningDefaultBranchRepairClosesAfterPauseGraceEvenWithForkRuns(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	deps := onGitHubActions(t, ctx, store, repo)
	deps.Config.TaskCancellers = NewTaskCancellers()
	now := time.Date(2026, 11, 11, 18, 0, 0, 0, time.UTC)

	red, redJobs := testsRun(t, ctx, store, repo, 50, "main", "failure", now, map[string]string{"go unit": "failure"})
	if err := handleGitHubBaseVerdict(ctx, deps, red, redJobs, now); err != nil {
		t.Fatal(err)
	}
	repairID := baseRepairs(t, ctx, store, repo)[0]

	// Attempt 1 finishes without making progress; fork runs on "main" must not
	// reset ResumesSinceProgress, while a real push failing a different job does.
	run1At := now.Add(5 * time.Minute)
	if err := store.StartRun(ctx, model.Run{ID: repairID + "-1", TaskID: repairID, Attempt: 1, StartedAt: run1At}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, repairID+"-1", run1At.Add(time.Minute), "failed", "no progress"); err != nil {
		t.Fatal(err)
	}
	if err := reconcileTaskGoals(ctx, deps, run1At.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	g, err := store.TaskGoal(ctx, repairID)
	if err != nil || g == nil || g.ResumesSinceProgress != 1 {
		t.Fatalf("ResumesSinceProgress after fruitless run = %+v (%v), want 1", g, err)
	}
	forkRedAt := run1At.Add(3 * time.Minute)
	forkRed, _ := testsRun(t, ctx, store, repo, 51, "main", "failure", forkRedAt, map[string]string{"lint": "failure"})
	forkRed.Event, forkRed.HeadRepo = "pull_request", "mallory/widgets"
	if err := reconcileTaskGoals(ctx, deps, forkRedAt); err != nil {
		t.Fatal(err)
	}
	if g, _ = store.TaskGoal(ctx, repairID); g.ResumesSinceProgress != 1 {
		t.Fatalf("fork PR failure reset ResumesSinceProgress to %d, want 1", g.ResumesSinceProgress)
	}
	pushRedAt := run1At.Add(4 * time.Minute)
	pushRed, pushRedJobs := testsRun(t, ctx, store, repo, 52, "main", "failure", pushRedAt, map[string]string{"lint": "failure"})
	if err := handleGitHubBaseVerdict(ctx, deps, pushRed, pushRedJobs, pushRedAt); err != nil {
		t.Fatal(err)
	}
	if g, _ = store.TaskGoal(ctx, repairID); g.ResumesSinceProgress != 0 || g.Measure != "lint" {
		t.Fatalf("real red push left goal %+v, want Measure=lint and ResumesSinceProgress=0", g)
	}

	// Attempt 2 is running when main goes green.
	run2At := now.Add(15 * time.Minute)
	if err := store.StartRun(ctx, model.Run{ID: repairID + "-2", TaskID: repairID, Attempt: 2, StartedAt: run2At}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancelCause(ctx)
	unregister := deps.Config.TaskCancellers.register(repairID, cancel)
	defer unregister()

	greenAt := now.Add(20 * time.Minute)
	green, greenJobs := testsRun(t, ctx, store, repo, 53, "main", "success", greenAt, map[string]string{"go unit": "success", "lint": "success"})
	if err := handleGitHubBaseVerdict(ctx, deps, green, greenJobs, greenAt); err != nil {
		t.Fatal(err)
	}
	if g, _ = store.TaskGoal(ctx, repairID); g.MetAt == nil {
		t.Fatalf("green push while repair was running did not mark goal met: %+v", g)
	}

	// During PauseGracePeriod a fork's red PR on "main" lands and reconcileTaskGoals ticks:
	// MetAt must stay set and the live run must not be cancelled yet.
	midGrace := greenAt.Add(model.PauseGracePeriod / 2)
	forkDuringGrace, _ := testsRun(t, ctx, store, repo, 54, "main", "failure", midGrace, map[string]string{"go unit": "failure"})
	forkDuringGrace.Event, forkDuringGrace.HeadRepo = "pull_request", "mallory/widgets"
	if err := reconcileTaskGoals(ctx, deps, midGrace); err != nil {
		t.Fatal(err)
	}
	if g, _ = store.TaskGoal(ctx, repairID); g.MetAt == nil {
		t.Fatalf("reconcileTaskGoals during pause grace wiped MetAt: %+v", g)
	}
	if context.Cause(runCtx) != nil {
		t.Fatalf("live run cancelled before PauseGracePeriod elapsed: %v", context.Cause(runCtx))
	}

	// Once PauseGracePeriod elapses, reconcileTaskGoals stops the live run, and
	// once the run exits the repair closes.
	afterGrace := greenAt.Add(model.PauseGracePeriod)
	if err := reconcileTaskGoals(ctx, deps, afterGrace); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(context.Cause(runCtx), errGoalMet) {
		t.Fatalf("live run cancel cause = %v, want %v", context.Cause(runCtx), errGoalMet)
	}
	unregister()
	if err := store.FinishRun(ctx, repairID+"-2", afterGrace, model.InterruptedOutcome, "goal met"); err != nil {
		t.Fatal(err)
	}
	if err := reconcileTaskGoals(ctx, deps, afterGrace.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if st, _ := store.State(ctx, repairID); st != model.StateClosed {
		t.Fatalf("repair %s state after live run exited = %s, want closed", repairID, st)
	}
}

// A ci_mode: github repository is polled without a .grain/ declaration while
// TargetRepos allows it; once excluded from TargetRepos, it is neither polled
// nor given repairs or default-branch notifications.
func TestADefaultBranchRepairRespectsTargetReposAndPollsGitHubRepos(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	other := model.RepoRef{Owner: "acme", Name: "gadgets"}
	deps := onGitHubActions(t, ctx, store, repo)
	now := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)

	targets, err := gitHubWorkflowRunPollTargets(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(targets, func(p gitHubWorkflowRunPollTarget) bool { return p.repo.Same(repo) }) {
		t.Fatalf("ci_mode: github repo %s missing from poll targets %+v", repo, targets)
	}

	// Raise both notifications while allowed, then exclude repo from TargetRepos.
	red, redJobs := testsRun(t, ctx, store, repo, 80, "main", "failure", now, map[string]string{"go unit": "failure"})
	if err := handleGitHubBaseVerdict(ctx, deps, red, redJobs, now); err != nil {
		t.Fatal(err)
	}
	for i := range 8 {
		at := now.Add(time.Duration(i+1) * 50 * time.Minute)
		testsRun(t, ctx, store, repo, int64(81+i), "main", "cancelled", at, map[string]string{"go unit": "cancelled"})
	}
	checkAt := now.Add(7 * time.Hour)
	if err := reconcileGitHubBaseRepairs(ctx, deps, checkAt); err != nil {
		t.Fatal(err)
	}
	if n, _ := store.OpenNotification(ctx, gitHubBaseRepairSubject(repo)); n == nil {
		t.Fatal("expected base-red-unattended while repo is allowed")
	}
	if n, _ := store.OpenNotification(ctx, gitHubBaseNoVerdictSubject(repo)); n == nil {
		t.Fatal("expected base-no-verdict while repo is allowed")
	}

	if err := store.SetTargetRepos(ctx, []string{other.String()}); err != nil {
		t.Fatal(err)
	}
	targets, err = gitHubWorkflowRunPollTargets(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(targets, func(p gitHubWorkflowRunPollTarget) bool { return p.repo.Same(repo) }) {
		t.Fatalf("off-TargetRepos repo %s still in poll targets %+v", repo, targets)
	}
	if err := reconcileGitHubBaseRepairs(ctx, deps, checkAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if n, _ := store.OpenNotification(ctx, gitHubBaseRepairSubject(repo)); n != nil {
		t.Errorf("base-red-unattended still open after leaving TargetRepos: %+v", n)
	}
	if n, _ := store.OpenNotification(ctx, gitHubBaseNoVerdictSubject(repo)); n != nil {
		t.Errorf("base-no-verdict still open after leaving TargetRepos: %+v", n)
	}

	// A new off-TargetRepos repo on GitHub Actions never gets a repair filed.
	excluded := model.RepoRef{Owner: "acme", Name: "offscope"}
	onGitHubActions(t, ctx, store, excluded)
	exRed, exJobs := testsRun(t, ctx, store, excluded, 99, "main", "failure", checkAt, map[string]string{"go unit": "failure"})
	if err := handleGitHubBaseVerdict(ctx, deps, exRed, exJobs, checkAt); err != nil {
		t.Fatal(err)
	}
	if ids := baseRepairs(t, ctx, store, excluded); len(ids) != 0 {
		t.Fatalf("off-TargetRepos repo %s got repairs %v", excluded, ids)
	}
}

// Polling a ci_mode: github repository's default branch reads that branch
// from the forge once an hour, not on every poll: GitHub never answers it
// 304, so an unchanged poll would otherwise cost a request.
func TestPollingReadsTheDefaultBranchOnceAnHour(t *testing.T) {
	t.Parallel()
	store := openInternalStore(t)
	client := &branchClient{}
	deps := Deps{Store: store, Client: client}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	now := time.Date(2026, 11, 12, 16, 0, 0, 0, time.UTC)
	for _, at := range []time.Time{now, now.Add(time.Minute), now.Add(59 * time.Minute)} {
		if got, err := gitHubBasePollDefaultBranch(deps, repo, at); err != nil || got != "main" {
			t.Fatalf("default branch %q (%v), want main", got, err)
		}
	}
	if client.repoCalls != 1 {
		t.Fatalf("three polls within the hour read the default branch %d times, want 1", client.repoCalls)
	}
	if _, err := gitHubBasePollDefaultBranch(deps, repo, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if client.repoCalls != 2 {
		t.Fatalf("a poll an hour later read the default branch %d times in all, want 2", client.repoCalls)
	}
}
