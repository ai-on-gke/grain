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

// SyncQualifications' own tests (bwsalmon/agents#518). Unlike releases_
// test.go, nothing here talks to (simulated) GitHub: qualification
// scheduling and auto-promotion are both pure store operations, the same
// "declare, then let a later reconciler carry out the GitHub-side effect"
// split SyncReleases itself already draws, just one step further removed
// from GitHub than a promotion request is.

import (
	"context"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// cutFirstCandidate creates a fresh release named "myfeat" for repo and
// returns its own first candidate -- CreateRelease's own "also cuts its
// first candidate" (bwsalmon/agents#571), which is all every test below
// needs a candidate for.
func cutFirstCandidate(t *testing.T, ctx context.Context, store *model.Store, repo model.RepoRef) model.Candidate {
	t.Helper()
	release, err := store.CreateRelease(ctx, repo, "myfeat", baseTime)
	if err != nil {
		t.Fatalf("create release: %v", err)
	}
	// Store-level tests exercise SyncQualifications alone, never
	// SyncReleases (this package's own doc comment on why: qualification
	// scheduling and auto-promotion are pure store operations) -- so
	// nothing else here ever advances the release out of provisioning the
	// way the real releases reconciler would.
	if err := store.MarkReleaseProvisioned(ctx, release.ID); err != nil {
		t.Fatalf("mark provisioned: %v", err)
	}
	c, err := store.CurrentCandidateForRelease(ctx, release.ID)
	if err != nil || c == nil {
		t.Fatalf("current candidate: (%+v, %v)", c, err)
	}
	return *c
}

// currentCandidate returns repo's release named "myfeat"'s own current
// candidate -- a repo-scoped convenience over
// store.CurrentCandidateForRelease, which takes a release ID, applied
// here since every test in this file only ever has the one release in
// play.
func currentCandidate(t *testing.T, ctx context.Context, store *model.Store, repo model.RepoRef) model.Candidate {
	t.Helper()
	release, err := store.GetRelease(ctx, repo, "myfeat")
	if err != nil || release == nil {
		t.Fatalf("release: (%+v, %v)", release, err)
	}
	c, err := store.CurrentCandidateForRelease(ctx, release.ID)
	if err != nil || c == nil {
		t.Fatalf("current candidate: (%+v, %v)", c, err)
	}
	return *c
}

func smokeTestTemplate() model.Template {
	return model.Template{
		ID: "template-smoke", Name: "Smoke test", Title: "Smoke test", Body: "run the smoke suite",
		CreatedAt: baseTime,
	}
}

func onePassingItem() model.QualificationPlan {
	return model.QualificationPlan{
		Configured: true, AutoPromote: true,
		Items: []model.QualificationItem{{TemplateID: "template-smoke", Repeat: 1}},
	}
}

func TestSyncQualificationsCreatesARunForAFreshlyActiveCandidate(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	if err := store.PutTemplate(ctx, smokeTestTemplate()); err != nil {
		t.Fatalf("put template: %v", err)
	}
	candidate := cutFirstCandidate(t, ctx, store, repo)
	if err := store.MarkCandidateCut(ctx, candidate.ID); err != nil {
		t.Fatalf("mark cut: %v", err)
	}

	plan := onePassingItem()
	plan.Repo = repo
	if err := store.PutQualificationPlan(ctx, plan); err != nil {
		t.Fatalf("put plan: %v", err)
	}

	if err := orchestrator.SyncQualifications(ctx, store, baseTime); err != nil {
		t.Fatalf("SyncQualifications: %v", err)
	}

	run, err := store.CandidateQualificationRun(ctx, candidate.ID)
	if err != nil || run == nil {
		t.Fatalf("candidate qualification run: (%+v, %v)", run, err)
	}
	if len(run.Tasks) != 1 {
		t.Fatalf("got %d task instances, want 1", len(run.Tasks))
	}

	// A second cycle must not create a second run.
	if err := orchestrator.SyncQualifications(ctx, store, baseTime); err != nil {
		t.Fatalf("second SyncQualifications: %v", err)
	}
	again, err := store.CandidateQualificationRun(ctx, candidate.ID)
	if err != nil || again == nil || again.ID != run.ID {
		t.Fatalf("got a different run on the second cycle: (%+v, %v)", again, err)
	}
}

func TestSyncQualificationsAutoPromotesOnceEveryTaskCompletes(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	if err := store.PutTemplate(ctx, smokeTestTemplate()); err != nil {
		t.Fatalf("put template: %v", err)
	}
	candidate := cutFirstCandidate(t, ctx, store, repo)
	if err := store.MarkCandidateCut(ctx, candidate.ID); err != nil {
		t.Fatalf("mark cut: %v", err)
	}
	plan := onePassingItem()
	plan.Repo = repo
	if err := store.PutQualificationPlan(ctx, plan); err != nil {
		t.Fatalf("put plan: %v", err)
	}
	if err := orchestrator.SyncQualifications(ctx, store, baseTime); err != nil {
		t.Fatalf("create run: %v", err)
	}
	run, err := store.CandidateQualificationRun(ctx, candidate.ID)
	if err != nil || run == nil {
		t.Fatalf("run: (%+v, %v)", run, err)
	}

	// Not yet promoted -- the task has not completed.
	if err := orchestrator.SyncQualifications(ctx, store, baseTime); err != nil {
		t.Fatalf("SyncQualifications before completion: %v", err)
	}
	current := currentCandidate(t, ctx, store, repo)
	if current.Status != model.CandidateActive {
		t.Fatalf("got status %q before completion, want still active", current.Status)
	}

	completedAt := baseTime
	if err := store.Observe(ctx, model.Observation{TaskID: run.Tasks[0].TaskID, CompletedAt: &completedAt}); err != nil {
		t.Fatalf("observe completion: %v", err)
	}

	if err := orchestrator.SyncQualifications(ctx, store, baseTime); err != nil {
		t.Fatalf("SyncQualifications after completion: %v", err)
	}
	current = currentCandidate(t, ctx, store, repo)
	if current.Status != model.CandidatePromoting {
		t.Fatalf("got status %q after every task completed, want promoting", current.Status)
	}
}

// A run's own creation can fail -- here, because the plan's template does
// not exist yet -- and CreateQualificationRun's own doc comment promises
// that failure is "retried next cycle exactly like any other store error."
// Nothing should be left half-created in the meantime: no run row at all
// until a cycle that resolves every item's template cleanly.
func TestSyncQualificationsRetriesRunCreationAfterAMissingTemplateIsAdded(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	candidate := cutFirstCandidate(t, ctx, store, repo)
	if err := store.MarkCandidateCut(ctx, candidate.ID); err != nil {
		t.Fatalf("mark cut: %v", err)
	}
	plan := onePassingItem()
	plan.Repo = repo
	if err := store.PutQualificationPlan(ctx, plan); err != nil {
		t.Fatalf("put plan: %v", err)
	}

	// The plan's own template ("template-smoke") was never created --
	// this cycle must fail, and must not leave a run behind.
	if err := orchestrator.SyncQualifications(ctx, store, baseTime); err == nil {
		t.Fatal("want an error when the plan's template does not exist yet")
	}
	if run, err := store.CandidateQualificationRun(ctx, candidate.ID); err != nil || run != nil {
		t.Fatalf("got (%+v, %v) after a failed attempt, want (nil, nil)", run, err)
	}

	if err := store.PutTemplate(ctx, smokeTestTemplate()); err != nil {
		t.Fatalf("put template: %v", err)
	}
	if err := orchestrator.SyncQualifications(ctx, store, baseTime); err != nil {
		t.Fatalf("SyncQualifications once the template exists: %v", err)
	}
	run, err := store.CandidateQualificationRun(ctx, candidate.ID)
	if err != nil || run == nil {
		t.Fatalf("candidate qualification run: (%+v, %v)", run, err)
	}
	if len(run.Tasks) != 1 {
		t.Fatalf("got %d task instances, want 1", len(run.Tasks))
	}
}

// A task closed without ever completing is a failure
// (model.QualificationStatus's own reasoning), and AutoPromote must never
// act on a run in that state -- unlike a straggler still in flight, this
// is permanent: nothing about the candidate ever becomes eligible again.
func TestSyncQualificationsNeverAutoPromotesARunWithAClosedTask(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	if err := store.PutTemplate(ctx, smokeTestTemplate()); err != nil {
		t.Fatalf("put template: %v", err)
	}
	candidate := cutFirstCandidate(t, ctx, store, repo)
	if err := store.MarkCandidateCut(ctx, candidate.ID); err != nil {
		t.Fatalf("mark cut: %v", err)
	}
	plan := onePassingItem()
	plan.Repo = repo
	if err := store.PutQualificationPlan(ctx, plan); err != nil {
		t.Fatalf("put plan: %v", err)
	}
	if err := orchestrator.SyncQualifications(ctx, store, baseTime); err != nil {
		t.Fatalf("create run: %v", err)
	}
	run, err := store.CandidateQualificationRun(ctx, candidate.ID)
	if err != nil || run == nil {
		t.Fatalf("run: (%+v, %v)", run, err)
	}

	closedAt := baseTime
	if err := store.Observe(ctx, model.Observation{TaskID: run.Tasks[0].TaskID, ClosedAt: &closedAt}); err != nil {
		t.Fatalf("observe closed: %v", err)
	}

	if err := orchestrator.SyncQualifications(ctx, store, baseTime); err != nil {
		t.Fatalf("SyncQualifications after the task closed: %v", err)
	}
	current := currentCandidate(t, ctx, store, repo)
	if current.Status != model.CandidateActive {
		t.Fatalf("got status %q for a run with a closed task, want still active (never auto-promoted)", current.Status)
	}

	updated, err := store.CandidateQualificationRun(ctx, candidate.ID)
	if err != nil || updated == nil {
		t.Fatalf("run: (%+v, %v)", updated, err)
	}
	if got := model.QualificationStatus(updated.Tasks); got != model.QualificationFailed {
		t.Fatalf("got status %v, want failed", got)
	}
}

// The full lifecycle, run twice: a candidate cut, qualified, and
// auto-promoted must not stop a fresh candidate cut afterwards from
// getting its own independent qualification run -- CandidateQualificationRun
// keys on candidate_id, so a second candidate's run must never be confused
// with, or blocked by, the first one's.
func TestSyncQualificationsHandlesASecondCandidateAfterTheFirstIsPromoted(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	if err := store.PutTemplate(ctx, smokeTestTemplate()); err != nil {
		t.Fatalf("put template: %v", err)
	}
	plan := onePassingItem()
	plan.Repo = repo
	if err := store.PutQualificationPlan(ctx, plan); err != nil {
		t.Fatalf("put plan: %v", err)
	}

	first := cutFirstCandidate(t, ctx, store, repo)
	if err := store.MarkCandidateCut(ctx, first.ID); err != nil {
		t.Fatalf("mark first cut: %v", err)
	}
	if err := orchestrator.SyncQualifications(ctx, store, baseTime); err != nil {
		t.Fatalf("create first run: %v", err)
	}
	firstRun, err := store.CandidateQualificationRun(ctx, first.ID)
	if err != nil || firstRun == nil {
		t.Fatalf("first run: (%+v, %v)", firstRun, err)
	}
	completedAt := baseTime
	if err := store.Observe(ctx, model.Observation{TaskID: firstRun.Tasks[0].TaskID, CompletedAt: &completedAt}); err != nil {
		t.Fatalf("observe first completion: %v", err)
	}
	if err := orchestrator.SyncQualifications(ctx, store, baseTime); err != nil {
		t.Fatalf("auto-promote first: %v", err)
	}
	if err := store.MarkCandidatePromoted(ctx, first.ID, baseTime); err != nil {
		t.Fatalf("mark first promoted: %v", err)
	}

	second, err := store.CutCandidate(ctx, repo, "myfeat", baseTime)
	if err != nil {
		t.Fatalf("cut second candidate: %v", err)
	}
	if second.ID == first.ID {
		t.Fatalf("second candidate got the same id as the first")
	}
	if err := store.MarkCandidateCut(ctx, second.ID); err != nil {
		t.Fatalf("mark second cut: %v", err)
	}
	if err := orchestrator.SyncQualifications(ctx, store, baseTime); err != nil {
		t.Fatalf("create second run: %v", err)
	}

	secondRun, err := store.CandidateQualificationRun(ctx, second.ID)
	if err != nil || secondRun == nil {
		t.Fatalf("second run: (%+v, %v)", secondRun, err)
	}
	if secondRun.ID == firstRun.ID {
		t.Fatal("second candidate's run must not be the first candidate's run")
	}
	if len(secondRun.Tasks) != 1 || secondRun.Tasks[0].TaskID == firstRun.Tasks[0].TaskID {
		t.Fatalf("second run's task must be its own instance, not reused from the first: %+v", secondRun.Tasks)
	}
}

func TestSyncQualificationsDoesNotAutoPromoteWhenThePlanDoesNotAskForIt(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	if err := store.PutTemplate(ctx, smokeTestTemplate()); err != nil {
		t.Fatalf("put template: %v", err)
	}
	candidate := cutFirstCandidate(t, ctx, store, repo)
	if err := store.MarkCandidateCut(ctx, candidate.ID); err != nil {
		t.Fatalf("mark cut: %v", err)
	}
	plan := onePassingItem()
	plan.Repo = repo
	plan.AutoPromote = false
	if err := store.PutQualificationPlan(ctx, plan); err != nil {
		t.Fatalf("put plan: %v", err)
	}
	if err := orchestrator.SyncQualifications(ctx, store, baseTime); err != nil {
		t.Fatalf("create run: %v", err)
	}
	run, err := store.CandidateQualificationRun(ctx, candidate.ID)
	if err != nil || run == nil {
		t.Fatalf("run: (%+v, %v)", run, err)
	}
	completedAt := baseTime
	if err := store.Observe(ctx, model.Observation{TaskID: run.Tasks[0].TaskID, CompletedAt: &completedAt}); err != nil {
		t.Fatalf("observe completion: %v", err)
	}

	if err := orchestrator.SyncQualifications(ctx, store, baseTime); err != nil {
		t.Fatalf("SyncQualifications: %v", err)
	}
	current := currentCandidate(t, ctx, store, repo)
	if current.Status != model.CandidateActive {
		t.Fatalf("got status %q with AutoPromote disabled, want still active", current.Status)
	}
}

func TestSyncQualificationsGrantsGrainCIToGrainCIRepo(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo: repo, CIMode: model.CIModeGrain,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTemplate(ctx, smokeTestTemplate()); err != nil {
		t.Fatalf("put template: %v", err)
	}
	candidate := cutFirstCandidate(t, ctx, store, repo)
	if err := store.MarkCandidateCut(ctx, candidate.ID); err != nil {
		t.Fatalf("mark cut: %v", err)
	}
	plan := onePassingItem()
	plan.Repo = repo
	if err := store.PutQualificationPlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	if err := orchestrator.SyncQualifications(ctx, store, baseTime); err != nil {
		t.Fatalf("create run: %v", err)
	}
	run, err := store.CandidateQualificationRun(ctx, candidate.ID)
	if err != nil || run == nil {
		t.Fatalf("run: (%+v, %v)", run, err)
	}
	task, err := store.GetTask(ctx, run.Tasks[0].TaskID)
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

func TestSyncQualificationsDeepRCQualificationPlan(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "bwsalmon", Name: "grain"}

	tmplMergeQueue := model.RepoDeclaredID(model.DeclaredTemplate, repo, "qualification-mergequeue")
	tmplDiagnostics := model.RepoDeclaredID(model.DeclaredTemplate, repo, "qualification-ci-diagnostics")
	tmplBaseTriage := model.RepoDeclaredID(model.DeclaredTemplate, repo, "qualification-base-triage")
	tmplSystemSmoke := model.RepoDeclaredID(model.DeclaredTemplate, repo, "qualification-system-smoke")

	for _, tmplID := range []string{tmplMergeQueue, tmplDiagnostics, tmplBaseTriage, tmplSystemSmoke} {
		if err := store.PutTemplate(ctx, model.Template{
			ID: tmplID, Name: tmplID, Title: tmplID, Body: "test qualification body", CreatedAt: baseTime,
		}); err != nil {
			t.Fatalf("put template %s: %v", tmplID, err)
		}
	}

	plan := model.QualificationPlan{
		Repo:            repo,
		Configured:      true,
		RequireApproval: true,
		AutoPromote:     false,
		Items: []model.QualificationItem{
			{TemplateID: tmplMergeQueue, Repeat: 1},
			{TemplateID: tmplDiagnostics, Repeat: 1},
			{TemplateID: tmplBaseTriage, Repeat: 1, DependsOn: []string{tmplDiagnostics}},
			{TemplateID: tmplSystemSmoke, Repeat: 1, DependsOn: []string{tmplMergeQueue, tmplBaseTriage}},
		},
	}

	if err := plan.Validate(); err != nil {
		t.Fatalf("validate plan: %v", err)
	}
	if err := store.PutQualificationPlan(ctx, plan); err != nil {
		t.Fatalf("put qualification plan: %v", err)
	}

	candidate := cutFirstCandidate(t, ctx, store, repo)
	if err := store.MarkCandidateCut(ctx, candidate.ID); err != nil {
		t.Fatalf("mark cut: %v", err)
	}

	if err := orchestrator.SyncQualifications(ctx, store, baseTime); err != nil {
		t.Fatalf("SyncQualifications: %v", err)
	}

	run, err := store.CandidateQualificationRun(ctx, candidate.ID)
	if err != nil || run == nil {
		t.Fatalf("candidate qualification run: (%+v, %v)", run, err)
	}
	if len(run.Tasks) != 4 {
		t.Fatalf("got %d tasks, want 4", len(run.Tasks))
	}

	taskByTmpl := make(map[string]model.QualificationTaskStatus)
	for _, ts := range run.Tasks {
		if ts.Approved {
			t.Errorf("task %s (%s) is approved, want unapproved (RequireApproval: true)", ts.TaskID, ts.TemplateID)
		}
		taskByTmpl[ts.TemplateID] = ts
	}

	getTaskDeps := func(taskID string) []string {
		tk, err := store.GetTask(ctx, taskID)
		if err != nil || tk == nil {
			t.Fatalf("get task %s: (%+v, %v)", taskID, tk, err)
		}
		var deps []string
		for _, link := range tk.Links {
			if link.Kind == model.LinkDependsOn {
				deps = append(deps, link.Target)
			}
		}
		return deps
	}

	// 1. Merge queue and CI diagnostics run in parallel with no dependencies
	if deps := getTaskDeps(taskByTmpl[tmplMergeQueue].TaskID); len(deps) != 0 {
		t.Errorf("merge queue task has deps %v, want none", deps)
	}
	if deps := getTaskDeps(taskByTmpl[tmplDiagnostics].TaskID); len(deps) != 0 {
		t.Errorf("diagnostics task has deps %v, want none", deps)
	}

	// 2. Base triage depends on CI diagnostics
	baseTriageDeps := getTaskDeps(taskByTmpl[tmplBaseTriage].TaskID)
	if len(baseTriageDeps) != 1 || baseTriageDeps[0] != taskByTmpl[tmplDiagnostics].TaskID {
		t.Errorf("base triage task deps = %v, want [%s]", baseTriageDeps, taskByTmpl[tmplDiagnostics].TaskID)
	}

	// 3. System smoke depends on both merge queue and base triage
	smokeDeps := getTaskDeps(taskByTmpl[tmplSystemSmoke].TaskID)
	if len(smokeDeps) != 2 {
		t.Fatalf("system smoke task deps count = %d, want 2", len(smokeDeps))
	}
	hasMQ := smokeDeps[0] == taskByTmpl[tmplMergeQueue].TaskID || smokeDeps[1] == taskByTmpl[tmplMergeQueue].TaskID
	hasBT := smokeDeps[0] == taskByTmpl[tmplBaseTriage].TaskID || smokeDeps[1] == taskByTmpl[tmplBaseTriage].TaskID
	if !hasMQ || !hasBT {
		t.Errorf("system smoke deps = %v, want [%s, %s]", smokeDeps, taskByTmpl[tmplMergeQueue].TaskID, taskByTmpl[tmplBaseTriage].TaskID)
	}

	// 4. Verify completion does not auto-promote when AutoPromote is false
	for _, ts := range run.Tasks {
		completedAt := baseTime
		if err := store.Observe(ctx, model.Observation{TaskID: ts.TaskID, CompletedAt: &completedAt}); err != nil {
			t.Fatalf("observe task completion: %v", err)
		}
	}
	if err := orchestrator.SyncQualifications(ctx, store, baseTime); err != nil {
		t.Fatalf("SyncQualifications after completion: %v", err)
	}
	current := currentCandidate(t, ctx, store, repo)
	if current.Status != model.CandidateActive {
		t.Fatalf("candidate status = %q, want still %q (auto_promote: false)", current.Status, model.CandidateActive)
	}
}
