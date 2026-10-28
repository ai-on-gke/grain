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

// SyncSuites' own tests (bwsalmon/agents#642). Like qualifications_
// test.go, nothing here talks to (simulated) GitHub: a pull request
// having been opened is simulated directly as the LinkFixes link
// finishWithPullRequest itself would have written, and a follow-up task
// having been proposed is simulated as relayProposedTasks itself would
// have written, so what is under test stays SyncSuites' own
// pass-to-pass decision, not the machinery that would normally produce
// those links.

import (
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

func suiteSmokeTemplate() model.Template {
	return model.Template{
		ID: "template-suite-smoke", Name: "Smoke test", Title: "Smoke test", Body: "run the smoke suite",
		CreatedAt: baseTime,
	}
}

func openPullRequest(t *testing.T, ctx context.Context, store *model.Store, taskID string) {
	t.Helper()
	if err := store.UpdateTask(ctx, taskID, func(task *model.Task) error {
		task.Links = append(task.Links, model.Link{Kind: model.LinkFixes, Target: "acme/widgets#1"})
		return nil
	}); err != nil {
		t.Fatalf("recording a pull request for %s: %v", taskID, err)
	}
}

func completeTask(t *testing.T, ctx context.Context, store *model.Store, taskID string, at time.Time) {
	t.Helper()
	if err := store.Observe(ctx, model.Observation{TaskID: taskID, CompletedAt: &at}); err != nil {
		t.Fatalf("completing %s: %v", taskID, err)
	}
}

// onlyTask returns run's own single pass-1 task id -- every test below
// uses a one-item suite, so a pass is always exactly one task.
func onlyTask(t *testing.T, run model.SuiteRun, pass int) string {
	t.Helper()
	tasks := run.PassTasks(pass)
	if len(tasks) != 1 {
		t.Fatalf("pass %d has %d tasks, want 1", pass, len(tasks))
	}
	return tasks[0].TaskID
}

func TestSyncSuitesDoesNothingWhileThePassIsStillPending(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutTemplate(ctx, suiteSmokeTemplate()); err != nil {
		t.Fatalf("put template: %v", err)
	}
	suite := model.Suite{
		Name: "smoke", Items: []model.SuiteItem{{TemplateID: "template-suite-smoke"}},
		Mode: model.SuiteCount, Count: 2, AutoMerge: true,
	}
	run, err := store.CreateSuiteRun(ctx, suite, repo, "main", baseTime)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	if err := orchestrator.SyncSuites(ctx, store, baseTime); err != nil {
		t.Fatalf("SyncSuites: %v", err)
	}
	updated, err := store.GetSuiteRun(ctx, run.ID)
	if err != nil || updated == nil {
		t.Fatalf("run: (%+v, %v)", updated, err)
	}
	if updated.Status != model.SuiteRunActive {
		t.Fatalf("got status %q while pass 1 is still queued, want active", updated.Status)
	}
	if len(updated.Tasks) != 1 {
		t.Fatalf("got %d tasks while pass 1 is still pending, want exactly the 1 already filed", len(updated.Tasks))
	}
}

func TestSyncSuitesInCountModeFiresEveryPassThenSucceeds(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutTemplate(ctx, suiteSmokeTemplate()); err != nil {
		t.Fatalf("put template: %v", err)
	}
	suite := model.Suite{
		Name: "smoke", Items: []model.SuiteItem{{TemplateID: "template-suite-smoke"}},
		Mode: model.SuiteCount, Count: 2, AutoMerge: true,
	}
	run, err := store.CreateSuiteRun(ctx, suite, repo, "main", baseTime)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	// Pass 1 completes with no change at all -- SuiteCount fires the
	// second pass anyway, unlike SuiteUntilClean.
	completeTask(t, ctx, store, onlyTask(t, run, 1), baseTime)
	if err := orchestrator.SyncSuites(ctx, store, baseTime); err != nil {
		t.Fatalf("SyncSuites after pass 1: %v", err)
	}
	updated, err := store.GetSuiteRun(ctx, run.ID)
	if err != nil || updated == nil {
		t.Fatalf("run: (%+v, %v)", updated, err)
	}
	run = *updated
	if run.Status != model.SuiteRunActive {
		t.Fatalf("got status %q after pass 1 of 2, want still active", run.Status)
	}
	if run.CurrentPass() != 2 {
		t.Fatalf("got current pass %d after pass 1 completed, want 2", run.CurrentPass())
	}

	completeTask(t, ctx, store, onlyTask(t, run, 2), baseTime)
	if err := orchestrator.SyncSuites(ctx, store, baseTime); err != nil {
		t.Fatalf("SyncSuites after pass 2: %v", err)
	}
	updated, err = store.GetSuiteRun(ctx, run.ID)
	if err != nil || updated == nil {
		t.Fatalf("run: (%+v, %v)", updated, err)
	}
	run = *updated
	if run.Status != model.SuiteRunSucceeded {
		t.Fatalf("got status %q after both passes completed, want succeeded", run.Status)
	}
	if run.CompletedAt == nil {
		t.Fatal("want CompletedAt set once succeeded")
	}
}

func TestSyncSuitesInUntilCleanModeStopsAtTheFirstCleanPass(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutTemplate(ctx, suiteSmokeTemplate()); err != nil {
		t.Fatalf("put template: %v", err)
	}
	suite := model.Suite{
		Name: "bugfinder", Items: []model.SuiteItem{{TemplateID: "template-suite-smoke"}},
		Mode: model.SuiteUntilClean, MaxPasses: 5, AutoMerge: true,
	}
	run, err := store.CreateSuiteRun(ctx, suite, repo, "main", baseTime)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	// Pass 1 opens a pull request -- a real change, so the run must not
	// stop yet.
	first := onlyTask(t, run, 1)
	openPullRequest(t, ctx, store, first)
	completeTask(t, ctx, store, first, baseTime)
	if err := orchestrator.SyncSuites(ctx, store, baseTime); err != nil {
		t.Fatalf("SyncSuites after pass 1: %v", err)
	}
	updated, err := store.GetSuiteRun(ctx, run.ID)
	if err != nil || updated == nil {
		t.Fatalf("run: (%+v, %v)", updated, err)
	}
	run = *updated
	if run.Status != model.SuiteRunActive {
		t.Fatalf("got status %q after a pass that opened a pull request, want still active", run.Status)
	}
	if run.CurrentPass() != 2 {
		t.Fatalf("got current pass %d, want 2", run.CurrentPass())
	}

	// Pass 2 completes clean -- no pull request, no proposed follow-up --
	// so the run stops here, succeeded, without ever reaching MaxPasses.
	completeTask(t, ctx, store, onlyTask(t, run, 2), baseTime)
	if err := orchestrator.SyncSuites(ctx, store, baseTime); err != nil {
		t.Fatalf("SyncSuites after pass 2: %v", err)
	}
	updated, err = store.GetSuiteRun(ctx, run.ID)
	if err != nil || updated == nil {
		t.Fatalf("run: (%+v, %v)", updated, err)
	}
	run = *updated
	if run.Status != model.SuiteRunSucceeded {
		t.Fatalf("got status %q after a clean pass, want succeeded", run.Status)
	}
	if run.CurrentPass() != 2 {
		t.Fatalf("got current pass %d, want exactly 2 (no third pass fired)", run.CurrentPass())
	}
}

func TestSyncSuitesInUntilCleanModeFailsAfterMaxPassesWithNoCleanPass(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutTemplate(ctx, suiteSmokeTemplate()); err != nil {
		t.Fatalf("put template: %v", err)
	}
	suite := model.Suite{
		Name: "bugfinder", Items: []model.SuiteItem{{TemplateID: "template-suite-smoke"}},
		Mode: model.SuiteUntilClean, MaxPasses: 2, AutoMerge: true,
	}
	run, err := store.CreateSuiteRun(ctx, suite, repo, "main", baseTime)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	for pass := 1; pass <= 2; pass++ {
		task := onlyTask(t, run, pass)
		openPullRequest(t, ctx, store, task)
		completeTask(t, ctx, store, task, baseTime)
		if err := orchestrator.SyncSuites(ctx, store, baseTime); err != nil {
			t.Fatalf("SyncSuites after pass %d: %v", pass, err)
		}
		run2, err := store.GetSuiteRun(ctx, run.ID)
		if err != nil || run2 == nil {
			t.Fatalf("run: (%+v, %v)", run2, err)
		}
		run = *run2
	}

	if run.Status != model.SuiteRunFailed {
		t.Fatalf("got status %q after MaxPasses with no clean pass, want failed", run.Status)
	}
	if run.LastError == "" {
		t.Fatal("want a LastError explaining why the run failed")
	}
	if run.CurrentPass() != 2 {
		t.Fatalf("got current pass %d, want exactly MaxPasses (2), no further pass fired", run.CurrentPass())
	}
}

func TestSyncSuitesFailsTheRunWhenATaskClosesWithoutCompleting(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutTemplate(ctx, suiteSmokeTemplate()); err != nil {
		t.Fatalf("put template: %v", err)
	}
	suite := model.Suite{
		Name: "smoke", Items: []model.SuiteItem{{TemplateID: "template-suite-smoke"}},
		Mode: model.SuiteCount, Count: 3, AutoMerge: true,
	}
	run, err := store.CreateSuiteRun(ctx, suite, repo, "main", baseTime)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	closedAt := baseTime
	if err := store.Observe(ctx, model.Observation{TaskID: onlyTask(t, run, 1), ClosedAt: &closedAt}); err != nil {
		t.Fatalf("observe closed: %v", err)
	}
	if err := orchestrator.SyncSuites(ctx, store, baseTime); err != nil {
		t.Fatalf("SyncSuites: %v", err)
	}
	updated, err := store.GetSuiteRun(ctx, run.ID)
	if err != nil || updated == nil {
		t.Fatalf("run: (%+v, %v)", updated, err)
	}
	if updated.Status != model.SuiteRunFailed {
		t.Fatalf("got status %q after a task closed without completing, want failed", updated.Status)
	}
	if updated.CurrentPass() != 1 {
		t.Fatalf("got current pass %d, want 1 (no further pass fired after a failure)", updated.CurrentPass())
	}
}

// 2026-10-11 walkthrough: a template deleted while a run of a suite using
// it was between passes left the run "active" for good -- every cycle
// failed to fire the next pass with "template ... no longer exists",
// and nothing on the run said so. The run now fails, saying why.
func TestSyncSuitesFailsTheRunWhenItsTemplateIsDeletedBetweenPasses(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutTemplate(ctx, suiteSmokeTemplate()); err != nil {
		t.Fatalf("put template: %v", err)
	}
	suite := model.Suite{
		Name: "smoke", Items: []model.SuiteItem{{TemplateID: "template-suite-smoke"}},
		Mode: model.SuiteCount, Count: 2, AutoMerge: true,
	}
	run, err := store.CreateSuiteRun(ctx, suite, repo, "main", baseTime)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := store.DeleteTemplate(ctx, "template-suite-smoke"); err != nil {
		t.Fatalf("delete template: %v", err)
	}

	completeTask(t, ctx, store, onlyTask(t, run, 1), baseTime)
	if err := orchestrator.SyncSuites(ctx, store, baseTime); err != nil {
		t.Fatalf("SyncSuites: %v", err)
	}
	updated, err := store.GetSuiteRun(ctx, run.ID)
	if err != nil || updated == nil {
		t.Fatalf("run: (%+v, %v)", updated, err)
	}
	if updated.Status != model.SuiteRunFailed {
		t.Fatalf("got status %q after its template was deleted, want failed", updated.Status)
	}
	want := "pass 2 could not be filed: template template-suite-smoke no longer exists"
	if updated.LastError != want {
		t.Fatalf("got last error %q, want %q", updated.LastError, want)
	}
	if updated.CurrentPass() != 1 {
		t.Fatalf("got current pass %d, want 1", updated.CurrentPass())
	}
	// Ended, so the next cycle has nothing left to retry.
	if err := orchestrator.SyncSuites(ctx, store, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("SyncSuites on the next cycle: %v", err)
	}
}

func TestSyncSuitesLeavesARequireApprovalRunActiveUntilApproved(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutTemplate(ctx, suiteSmokeTemplate()); err != nil {
		t.Fatalf("put template: %v", err)
	}
	suite := model.Suite{
		Name: "smoke", Items: []model.SuiteItem{{TemplateID: "template-suite-smoke"}},
		Mode: model.SuiteCount, Count: 1, RequireApproval: true,
	}
	run, err := store.CreateSuiteRun(ctx, suite, repo, "main", baseTime)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if run.Tasks[0].Approved {
		t.Fatal("a RequireApproval suite's tasks must land unapproved")
	}

	if err := orchestrator.SyncSuites(ctx, store, baseTime); err != nil {
		t.Fatalf("SyncSuites before approval: %v", err)
	}
	updated, err := store.GetSuiteRun(ctx, run.ID)
	if err != nil || updated == nil {
		t.Fatalf("run: (%+v, %v)", updated, err)
	}
	if updated.Status != model.SuiteRunActive {
		t.Fatalf("got status %q before approval, want still active", updated.Status)
	}

	if err := store.Approve(ctx, run.Tasks[0].TaskID, model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "grace"}}, baseTime); err != nil {
		t.Fatalf("approve: %v", err)
	}
	completeTask(t, ctx, store, run.Tasks[0].TaskID, baseTime)
	if err := orchestrator.SyncSuites(ctx, store, baseTime); err != nil {
		t.Fatalf("SyncSuites after approval and completion: %v", err)
	}
	updated, err = store.GetSuiteRun(ctx, run.ID)
	if err != nil || updated == nil {
		t.Fatalf("run: (%+v, %v)", updated, err)
	}
	if updated.Status != model.SuiteRunSucceeded {
		t.Fatalf("got status %q after approval and completion, want succeeded", updated.Status)
	}
}

// --- multi-item passes ---------------------------------------------------
//
// Every test above uses a one-item suite, where a pass is always a single
// task. A suite is a *combination* of templates, though, so its ordinary
// shape is a pass of several tasks finishing at different times and with
// different outcomes -- which is where the interesting decisions are, and
// where nothing above reaches.

func suiteLintTemplate() model.Template {
	return model.Template{
		ID: "template-suite-lint", Name: "Lint", Title: "Lint", Body: "go vet ./...",
		CreatedAt: baseTime,
	}
}

// twoItemRun is a fresh two-item run of mode, with pass 1 already filed.
func twoItemRun(t *testing.T, ctx context.Context, store *model.Store, suite model.Suite) model.SuiteRun {
	t.Helper()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutTemplate(ctx, suiteSmokeTemplate()); err != nil {
		t.Fatalf("put smoke template: %v", err)
	}
	if err := store.PutTemplate(ctx, suiteLintTemplate()); err != nil {
		t.Fatalf("put lint template: %v", err)
	}
	suite.Items = []model.SuiteItem{
		{TemplateID: "template-suite-smoke"}, {TemplateID: "template-suite-lint"},
	}
	run, err := store.CreateSuiteRun(ctx, suite, repo, "main", baseTime)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if len(run.PassTasks(1)) != 2 {
		t.Fatalf("pass 1 filed %d tasks, want one per item", len(run.PassTasks(1)))
	}
	return run
}

func syncedRun(t *testing.T, ctx context.Context, store *model.Store, id int64) model.SuiteRun {
	t.Helper()
	if err := orchestrator.SyncSuites(ctx, store, baseTime); err != nil {
		t.Fatalf("SyncSuites: %v", err)
	}
	run, err := store.GetSuiteRun(ctx, id)
	if err != nil || run == nil {
		t.Fatalf("run: (%+v, %v)", run, err)
	}
	return *run
}

func TestSyncSuitesWaitsForEveryTaskInAPassBeforeFiringTheNext(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	run := twoItemRun(t, ctx, store, model.Suite{
		Name: "nightly", Mode: model.SuiteCount, Count: 2, AutoMerge: true,
	})
	tasks := run.PassTasks(1)

	// Only the first item is done: the pass is not finished, so no
	// second pass, and no verdict either.
	completeTask(t, ctx, store, tasks[0].TaskID, baseTime)
	updated := syncedRun(t, ctx, store, run.ID)
	if updated.Status != model.SuiteRunActive || updated.CurrentPass() != 1 {
		t.Fatalf("got status %q at pass %d with one item still running, want active at pass 1",
			updated.Status, updated.CurrentPass())
	}

	completeTask(t, ctx, store, tasks[1].TaskID, baseTime)
	updated = syncedRun(t, ctx, store, run.ID)
	if updated.CurrentPass() != 2 {
		t.Fatalf("got pass %d once every item finished, want 2", updated.CurrentPass())
	}
	if len(updated.PassTasks(2)) != 2 {
		t.Fatalf("pass 2 filed %d tasks, want one per item again", len(updated.PassTasks(2)))
	}
}

// A failure anywhere in a pass stops the run then and there, without
// waiting for the pass's other tasks to settle -- otherwise a suite in
// SuiteCount mode would go on firing whole passes behind a failure
// already known (model.OutcomeOfPass' own precedence).
func TestSyncSuitesFailsTheRunWhileAnotherTaskInThePassIsStillRunning(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	run := twoItemRun(t, ctx, store, model.Suite{
		Name: "nightly", Mode: model.SuiteCount, Count: 3, AutoMerge: true,
	})
	tasks := run.PassTasks(1)

	closedAt := baseTime
	if err := store.Observe(ctx, model.Observation{TaskID: tasks[0].TaskID, ClosedAt: &closedAt}); err != nil {
		t.Fatalf("observe closed: %v", err)
	}
	updated := syncedRun(t, ctx, store, run.ID)
	if updated.Status != model.SuiteRunFailed {
		t.Fatalf("got status %q with one item of the pass failed, want failed", updated.Status)
	}
	if updated.CurrentPass() != 1 {
		t.Fatalf("got pass %d, want 1 -- no further pass fires behind a failure", updated.CurrentPass())
	}
	if updated.LastError == "" {
		t.Error("a failed run must say why")
	}
}

// One item of a pass opening a pull request is enough to make the whole
// pass "changed", so an until_clean run keeps going even though its
// other item came back with nothing.
func TestSyncSuitesTreatsAPassAsChangedWhenAnyOneTaskChangedSomething(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	run := twoItemRun(t, ctx, store, model.Suite{
		Name: "sweep", Mode: model.SuiteUntilClean, MaxPasses: 3, AutoMerge: true,
	})
	tasks := run.PassTasks(1)

	openPullRequest(t, ctx, store, tasks[0].TaskID)
	completeTask(t, ctx, store, tasks[0].TaskID, baseTime)
	completeTask(t, ctx, store, tasks[1].TaskID, baseTime)

	updated := syncedRun(t, ctx, store, run.ID)
	if updated.Status != model.SuiteRunActive {
		t.Fatalf("got status %q after a pass that opened a pull request, want still active", updated.Status)
	}
	if updated.CurrentPass() != 2 {
		t.Fatalf("got pass %d, want a second pass fired", updated.CurrentPass())
	}

	// Pass 2 comes back with nothing from either item: that is clean, and
	// the run stops.
	for _, ts := range updated.PassTasks(2) {
		completeTask(t, ctx, store, ts.TaskID, baseTime)
	}
	updated = syncedRun(t, ctx, store, run.ID)
	if updated.Status != model.SuiteRunSucceeded {
		t.Fatalf("got status %q after a pass where no item changed anything, want succeeded", updated.Status)
	}
	if updated.CurrentPass() != 2 {
		t.Fatalf("got pass %d, want no third pass after a clean one", updated.CurrentPass())
	}
}

// SyncSuites is level-triggered across runs: one run's own store error
// must not stop it advancing the others. Exercised here with two
// healthy runs in one cycle, the ordinary case that would break first
// if the loop ever returned early.
func TestSyncSuitesAdvancesEveryActiveRunInOneCycle(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutTemplate(ctx, suiteSmokeTemplate()); err != nil {
		t.Fatalf("put template: %v", err)
	}
	suite := model.Suite{
		Name: "smoke", Items: []model.SuiteItem{{TemplateID: "template-suite-smoke"}},
		Mode: model.SuiteCount, Count: 1, AutoMerge: true,
	}
	first, err := store.CreateSuiteRun(ctx, suite, repo, "main", baseTime)
	if err != nil {
		t.Fatalf("create first run: %v", err)
	}
	second, err := store.CreateSuiteRun(ctx, suite, repo, "release-1", baseTime)
	if err != nil {
		t.Fatalf("create second run: %v", err)
	}
	completeTask(t, ctx, store, onlyTask(t, first, 1), baseTime)
	completeTask(t, ctx, store, onlyTask(t, second, 1), baseTime)

	if err := orchestrator.SyncSuites(ctx, store, baseTime); err != nil {
		t.Fatalf("SyncSuites: %v", err)
	}
	for _, id := range []int64{first.ID, second.ID} {
		run, err := store.GetSuiteRun(ctx, id)
		if err != nil || run == nil {
			t.Fatalf("run %d: (%+v, %v)", id, run, err)
		}
		if run.Status != model.SuiteRunSucceeded {
			t.Errorf("run %d: got status %q, want succeeded in the same cycle as the other", id, run.Status)
		}
	}
	active, err := store.ActiveSuiteRuns(ctx)
	if err != nil {
		t.Fatalf("active: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("got %d runs still active, want none -- a finished run is never read again", len(active))
	}
}

func TestSyncSuitesGrantsGrainCIToGrainCIRepo(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo: repo, CIMode: model.CIModeGrain,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTemplate(ctx, suiteSmokeTemplate()); err != nil {
		t.Fatalf("put template: %v", err)
	}
	suite := model.Suite{
		Name: "smoke", Items: []model.SuiteItem{{TemplateID: "template-suite-smoke"}},
		Mode: model.SuiteCount, Count: 1, AutoMerge: true,
	}
	run, err := store.CreateSuiteRun(ctx, suite, repo, "main", baseTime)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	taskID := onlyTask(t, run, 1)
	task, err := store.GetTask(ctx, taskID)
	if err != nil || task == nil {
		t.Fatalf("get task: (%+v, %v)", task, err)
	}
	found := false
	for _, g := range task.Grants {
		if g.Capability == "grain-ci" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("task grants = %v, want grain-ci grant included", task.Grants)
	}
}
