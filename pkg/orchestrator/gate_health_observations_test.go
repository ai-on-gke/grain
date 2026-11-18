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
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

type healthSourceCI struct {
	ci.NopRunner
	runs map[string]ci.Run
}

func (f healthSourceCI) Get(id string) (ci.Run, bool) { r, ok := f.runs[id]; return r, ok }

type healthSourceForge struct {
	branchClient
	latest, exact           []github.WorkflowRunJob
	exactErr                error
	latestCalls, exactCalls int
}
type healthSlowBaseForge struct{ branchClient }

func (*healthSlowBaseForge) DefaultBranch(string, string) (string, error) {
	time.Sleep(75 * time.Millisecond)
	return "main", nil
}

func (f *healthSourceForge) CompletedWorkflowRuns(string, string, int) ([]github.WorkflowRun, error) {
	return nil, nil
}
func (f *healthSourceForge) ListWorkflowRunJobs(string, string, int64) ([]github.WorkflowRunJob, error) {
	f.latestCalls++
	return f.latest, nil
}
func (f *healthSourceForge) ListWorkflowRunAttemptJobs(string, string, int64, int) ([]github.WorkflowRunJob, error) {
	f.exactCalls++
	return f.exact, f.exactErr
}

func TestHealthSourceCollectorsKeepLegacyInputsAndEffects(t *testing.T) {
	t.Setenv("GRAIN_GATE_OBSERVATION_TASKS", "source-health")
	ctx := context.Background()
	store := openInternalStore(t)
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	started := now.Add(-time.Hour)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(store.PutTask(ctx, model.Task{ID: "source-health", Intent: model.IntentImplement, Target: &repo, Base: "main", Title: "repair"}))
	must(store.SetTaskGoal(ctx, "source-health", model.GoalDecl{Kind: model.GoalNightlyGreen, Spec: model.GoalSpec{Repo: repo.String(), Workflow: "nightly"}}, "automation:test", started))
	yes := func(string) bool { return true }
	access := model.GateHealthAccess{Repository: yes, PublicationRepository: yes, PipelineRepository: yes, DeploymentWide: true}
	run := ConcludedGitHubWorkflowRun{Repo: repo, WorkflowName: "nightly", WorkflowPath: ".github/workflows/nightly.yml", RunID: 7, Attempt: 2, Conclusion: "success", HeadSHA: "head", HeadRepo: repo.String(), Ref: "main", Event: "schedule", Via: "webhook", StartedAt: &started, CompletedAt: &now}
	job := github.WorkflowRunJob{ID: 71, RunID: 7, Name: "test", Status: "completed", Conclusion: "success", StartedAt: &started, CompletedAt: &now}
	exact := job
	exact.Conclusion = "failure"
	forge := &healthSourceForge{latest: []github.WorkflowRunJob{job}, exact: []github.WorkflowRunJob{exact}}
	deps := Deps{Store: store, Client: forge}
	// The existing source collector receives the same legacy latest listing.
	// Shadow reads exact-attempt jobs separately and never overwrite legacy rows.
	jobs, err := ensureGitHubWorkflowRunJobs(ctx, deps, run, now)
	must(err)
	if len(jobs) != 1 || jobs[0].Conclusion != "success" || forge.latestCalls != 1 || forge.exactCalls != 1 {
		t.Fatal("shadow changed legacy collector inputs")
	}
	v, _, err := store.ObserveTaskGates(ctx, "source-health", now, access)
	must(err)
	if v.Results[0].Result.Status != "met" || v.Results[1].Result.Status != "unmet" {
		t.Fatalf("source predicates: %+v", v.Results)
	}
	// Replayed source reads preserve the diagnostic source identity; an actual
	// denial/missing exact read must replace proof with unknown, not old green.
	forge.exactErr = errors.New("denied")
	observeWorkflowHealthSource(ctx, deps, run, now.Add(time.Second))
	v, _, err = store.ObserveTaskGates(ctx, "source-health", now.Add(time.Second), access)
	must(err)
	if v.Results[1].Result.Status != "unknown" {
		t.Fatal("denied exact jobs reused prior proof")
	}
	goal, err := store.TaskGoal(ctx, "source-health")
	must(err)
	if goal.MetAt != nil || goal.ResumesSinceProgress != 0 {
		t.Fatal("shadow met or advanced legacy goal")
	}
	obs, err := store.GetObservation(ctx, "source-health")
	must(err)
	if obs != nil && (obs.ClosedAt != nil || obs.CompletedAt != nil) {
		t.Fatal("shadow closed/completed repair")
	}
	// Raw fork/PR rows can never establish a current base/nightly verdict.
	forge.exactErr = nil
	run.Event = "pull_request"
	run.HeadRepo = "fork/widgets"
	observeWorkflowHealthSource(ctx, deps, run, now.Add(2*time.Second))
	v, _, err = store.ObserveTaskGates(ctx, "source-health", now.Add(2*time.Second), access)
	must(err)
	if v.Results[1].Result.Status == "met" {
		t.Fatal("fork main became base health")
	}
}

func TestBaseHealthCollectorRequiresExactPlanAndAuthenticNonreproduction(t *testing.T) {
	t.Setenv("GRAIN_GATE_OBSERVATION_TASKS", "base-source-health")
	ctx := context.Background()
	store := openInternalStore(t)
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	cfg := model.RepoConfig{Repo: repo, CIMode: model.CIModeGrain, WatchDefaultBranch: true, BaseBranchCIJobs: []string{".grain/workflows/tests.yml:test"}}
	must(store.PutRepoConfig(ctx, cfg))
	must(store.PutTask(ctx, model.Task{ID: "base-source-health", Intent: model.IntentImplement, Target: &repo, Base: "main", Title: "repair"}))
	must(store.SetTaskGoal(ctx, "base-source-health", model.GoalDecl{Kind: model.GoalBaseGreen, Spec: model.GoalSpec{Repo: repo.String(), Branch: "main"}}, "automation:test", now))
	row := model.BaseBranchCI{Repo: repo, Branch: "main", RunID: "original", Commit: "head", Conclusion: "failure", Confirmation: model.BaseCICleared, ConfirmRunID: "recheck", ConfirmJobs: cfg.BaseBranchCIJobs, FinishedAt: &now}
	must(store.PutBaseBranchCI(ctx, row))
	original := ci.Run{ID: "original", Repo: repo.String(), Ref: "main", Commit: "head", Event: ci.EventPush, Conclusion: ci.ConclusionFailure, FinishedAt: &now, Jobs: []ci.JobRun{{WorkflowPath: ".grain/workflows/tests.yml", JobID: "test", Conclusion: ci.ConclusionFailure}}}
	recheck := original
	recheck.ID = "recheck"
	recheck.Conclusion = ci.ConclusionSuccess
	recheck.Jobs = []ci.JobRun{{WorkflowPath: ".grain/workflows/tests.yml", JobID: "test", Conclusion: ci.ConclusionSuccess}}
	runner := healthSourceCI{runs: map[string]ci.Run{"original": original, "recheck": recheck}}
	deps := Deps{Store: store, Client: &healthSlowBaseForge{}, CI: runner}
	yes := func(string) bool { return true }
	access := model.GateHealthAccess{Repository: yes}
	read := func(want string, at time.Time) {
		t.Helper()
		changed := store.Changed()
		observeBaseHealthSource(ctx, deps, row, "head", true, at)
		select {
		case <-changed:
			t.Fatal("source observation woke legacy dispatcher")
		default:
		}
		v, _, err := store.ObserveTaskGates(ctx, "base-source-health", at, access)
		must(err)
		if v.Results[0].Result.Status != "met" || v.Results[1].Result.Status != want {
			t.Fatalf("%+v", v.Results)
		}
	}
	read("met", now)
	recheck.Commit = "old"
	runner.runs["recheck"] = recheck
	read("unknown", now.Add(time.Second))
	recheck.Commit = "head"
	recheck.Jobs[0].Cached = true
	runner.runs["recheck"] = recheck
	read("unknown", now.Add(2*time.Second))
	recheck.Jobs[0].Cached = false
	runner.runs["recheck"] = recheck
	original.Jobs = nil
	runner.runs["original"] = original
	read("unknown", now.Add(3*time.Second))
	original.Jobs = []ci.JobRun{{WorkflowPath: ".grain/workflows/tests.yml", JobID: "test", Conclusion: ci.ConclusionFailure}}
	runner.runs["original"] = original
	// A newer tip invalidates a passing recorded-row legacy reading.
	observeBaseHealthSource(ctx, deps, row, "new", true, now.Add(4*time.Second))
	v, _, err := store.ObserveTaskGates(ctx, "base-source-health", now.Add(4*time.Second), access)
	must(err)
	if v.Results[1].Result.Status == "met" {
		t.Fatal("stale base tip passed")
	}
}

func TestHealthStallThresholdsMatchExistingLegacyClassifier(t *testing.T) {
	since := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	for _, state := range []model.State{model.StateQueued, model.StateRunning, model.StateCompleted, model.StateDeferred} {
		deadline := model.TaskHealthStallDeadline(state, since)
		if TaskStallNote(state, since, deadline.Add(-time.Nanosecond)) != "" || TaskStallNote(state, since, deadline) == "" {
			t.Fatalf("%s shadow deadline differs from legacy classifier", state)
		}
	}
}

func TestReportObservationSelectionDoesNotReadHealthSources(t *testing.T) {
	t.Setenv("GRAIN_GATE_OBSERVATION_TASKS", "report-only")
	store := openInternalStore(t)
	ctx := context.Background()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	now := time.Now().UTC()
	if err := store.PutTask(ctx, model.Task{ID: "report-only", Intent: model.IntentAnalyze, Title: "report"}); err != nil {
		t.Fatal(err)
	}
	forge := &healthSourceForge{}
	run := ConcludedGitHubWorkflowRun{Repo: repo, RunID: 42, Attempt: 1, WorkflowName: "nightly", Ref: "main", Event: "schedule", HeadRepo: repo.String(), CompletedAt: &now}
	observeWorkflowHealthSource(ctx, Deps{Store: store, Client: forge}, run, now)
	if forge.exactCalls != 0 {
		t.Fatal("unrelated report monitor caused a health source read")
	}
}
