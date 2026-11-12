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
	"fmt"
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
// default branch: it neither files a repair nor closes one.
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
}
