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

// A repository's merge rules at the merge gate (grain/task-1659): read
// from the base as the declaration sync stored them, applied per pull
// request by the files it touches.

func declareMergeRules(t *testing.T, ctx context.Context, store *model.Store, repo model.RepoRef,
	rules *model.MergeRules) {

	t.Helper()
	if _, err := store.SyncRepoDeclarations(ctx, repo, "base1", model.RepoDeclarations{MergeRules: rules},
		baseTime, time.UTC); err != nil {
		t.Fatal(err)
	}
}

func TestAutoMergeNeverWaitsForAPersonsSubmit(t *testing.T) {
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	declareMergeRules(t, ctx, store, repo, &model.MergeRules{Path: model.MergeRulesPath,
		Rules: []model.MergeRule{{ID: "deploy", Paths: []string{"deploy/**"},
			MergePolicy: model.MergePolicy{AutoMerge: model.AutoMergeNever}}}})

	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	pushFile(t, sim.BareRepo, branch, "deploy/main.tf", "resource {}\n")
	setMergeable(sim, true)

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AutoMerge || sim.PullRequests[0].Merged {
		t.Fatalf("auto-merge = %v, merged = %v: auto_merge: never must wait for a person",
			got.AutoMerge, sim.PullRequests[0].Merged)
	}
	policy, ok := model.MergePolicyOf(*got)
	if !ok || !policy.AutoMergeNever || len(policy.Matched) != 1 || policy.Matched[0] != "deploy" {
		t.Fatalf("recorded policy = %+v (%v), want auto_merge never from deploy", policy, ok)
	}
	comments, err := store.Comments(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	said := false
	for _, c := range comments {
		if strings.Contains(c.Body, "`auto_merge: never` -- from rule `deploy`") {
			said = true
		}
	}
	if !said {
		t.Fatal("the task was not told which rule withdrew its auto-merge")
	}

	// A person's Submit is what turns it on again, and that merges it.
	if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		t.AutoMerge = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if !sim.PullRequests[0].Merged {
		t.Fatal("a person submitted the change and it did not merge")
	}
}

func TestAChangeNoRuleWithholdsStillMergesItself(t *testing.T) {
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	declareMergeRules(t, ctx, store, repo, &model.MergeRules{Path: model.MergeRulesPath,
		Rules: []model.MergeRule{{ID: "deploy", Paths: []string{"deploy/**"},
			MergePolicy: model.MergePolicy{AutoMerge: model.AutoMergeNever}}}})

	_, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	pushFile(t, sim.BareRepo, branch, "docs/readme.md", "hello\n")
	setMergeable(sim, true)
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatal(err)
	}
	if !sim.PullRequests[0].Merged {
		t.Fatal("a docs change no rule withholds did not merge")
	}
}

func TestAMissingRequiredGitHubCheckHoldsTheMergeAndIsNamed(t *testing.T) {
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	declareMergeRules(t, ctx, store, repo, &model.MergeRules{Path: model.MergeRulesPath,
		Rules: []model.MergeRule{{ID: "deploy", Paths: []string{"deploy/**"},
			MergePolicy: model.MergePolicy{CI: &model.CIRule{GitHub: []string{"cla/google"}}}}}})

	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	pushFile(t, sim.BareRepo, branch, "deploy/main.tf", "resource {}\n")
	setMergeable(sim, true)
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatal(err)
	}
	if sim.PullRequests[0].Merged {
		t.Fatal("merged without the cla/google check its merge rules require")
	}
	obs := mergeQueueStatus(t, ctx, store, task.ID)
	if !strings.Contains(obs.MergeQueueNote, "`cla/google` (required by `deploy`)") {
		t.Fatalf("queue note = %q, want it to name the missing check and its rule", obs.MergeQueueNote)
	}
}

// A grain suite narrowed to named jobs passes on those jobs alone -- and
// only when every one of them was in the run and passed.
func TestANarrowedGrainSuitePassesOnlyOnItsNamedJobs(t *testing.T) {
	run := ci.Run{Jobs: []ci.JobRun{
		{Workflow: "checks", WorkflowPath: ".grain/workflows/checks.yml", JobID: "lint", Name: "lint",
			Conclusion: ci.ConclusionSuccess},
		{Workflow: "checks", WorkflowPath: ".grain/workflows/checks.yml", JobID: "go", Name: "go",
			Conclusion: ci.ConclusionFailure},
	}}
	if !orchestrator.NarrowedJobsPassed(run, []string{"lint"}) {
		t.Fatal("lint passed and is all that was asked for")
	}
	if orchestrator.NarrowedJobsPassed(run, []string{"lint", "go"}) {
		t.Fatal("go failed and was asked for")
	}
	if orchestrator.NarrowedJobsPassed(run, []string{"ui e2e"}) {
		t.Fatal("a job the run does not have is not a pass")
	}
	if orchestrator.NarrowedJobsPassed(run, []string{"checks"}) {
		t.Fatal("a workflow is every job in it, and one of them failed")
	}
}

// runsByID is a ci.Runner that only remembers runs.
type runsByID struct {
	ci.NopRunner
	runs map[string]ci.Run
}

func (r runsByID) Start(context.Context, ci.Request) (ci.Run, error) { return ci.Run{}, nil }
func (r runsByID) Get(id string) (ci.Run, bool)                      { run, ok := r.runs[id]; return run, ok }

// A verdict a flake recheck reached is read job by job off the run it
// rechecked with the recheck laid over it. The recheck re-runs only the
// failing jobs, so read alone it has no `lint` at all, and a docs-only
// change narrowed to `lint` was sent to repair over a `go` failure its
// rules say nothing about (docs/test-runs/2026-10-05-merge-rules.md, F1).
func TestANarrowedSuiteIsReadThroughAFlakeRecheck(t *testing.T) {
	job := func(id string, c ci.Conclusion) ci.JobRun {
		return ci.JobRun{Workflow: "checks", WorkflowPath: ".grain/workflows/checks.yml", JobID: id, Name: id,
			Conclusion: c}
	}
	runner := runsByID{runs: map[string]ci.Run{
		"first":   {ID: "first", Jobs: []ci.JobRun{job("lint", ci.ConclusionSuccess), job("go", ci.ConclusionFailure)}},
		"recheck": {ID: "recheck", Jobs: []ci.JobRun{job("go", ci.ConclusionFailure)}},
	}}
	run, ok := orchestrator.VerdictRunOf(runner, &model.CIStatus{RunID: "recheck", RecheckOf: "first"})
	if !ok {
		t.Fatal("both runs are remembered")
	}
	if !orchestrator.NarrowedJobsPassed(run, []string{"lint"}) {
		t.Fatalf("lint passed in the run the recheck rechecked: %+v", run.Jobs)
	}
	if orchestrator.NarrowedJobsPassed(run, []string{"go"}) {
		t.Fatal("go failed both times")
	}
	if _, ok := orchestrator.VerdictRunOf(runner, &model.CIStatus{RunID: "recheck", RecheckOf: "gone"}); ok {
		t.Fatal("a recheck whose first run is forgotten cannot be read job by job")
	}
}
