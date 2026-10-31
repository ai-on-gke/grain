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

package ui_test

// grain/task-185: grain's own CI as an API -- POST /api/ci/runs against
// a repo and a branch, the three routes that read one back, and the
// grain-ci capability that decides which repositories any of it answers
// for.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// fakeCIRunner is a ci.Runner with its answers written down: these tests
// are about the routes and the capability gate, not about the engine
// (pkg/ci's own tests are).
type fakeCIRunner struct {
	ci.NopRunner
	started   []ci.Request
	run       ci.Run
	err       error
	runs      []ci.Run
	cancelled []string
	// reasons is the note each of those Cancel calls carried, in the same
	// order -- "" for a cancellation that said nothing -- and sources is
	// who each of them said was asking (ci.CancelSource).
	reasons  []string
	sources  []ci.CancelSource
	cancelOK bool
	// The job cache half: stats is what this runner claims to hold,
	// cache is whether it has one at all (a deployment can run CI with
	// none), and cleared counts the times it was emptied.
	stats            ci.CacheStats
	cache            bool
	cacheErr         error
	cleared          int
	removes          int
	clearErr         error
	workflowOptions  []ci.WorkflowOption
	workflowErr      error
	workflowRequests []ci.Request
	listCalls        int
	findCalls        int
	getCalls         int
	getsByID         map[string]int
}

func (f *fakeCIRunner) Start(_ context.Context, req ci.Request) (ci.Run, error) {
	f.started = append(f.started, req)
	if f.err != nil {
		return ci.Run{}, f.err
	}
	return f.run, nil
}

func (f *fakeCIRunner) WorkflowOptions(_ context.Context, req ci.Request) ([]ci.WorkflowOption, error) {
	f.workflowRequests = append(f.workflowRequests, req)
	return f.workflowOptions, f.workflowErr
}

func (f *fakeCIRunner) Get(id string) (ci.Run, bool) {
	f.getCalls++
	if f.getsByID == nil {
		f.getsByID = make(map[string]int)
	}
	f.getsByID[id]++
	for _, run := range f.runs {
		if run.ID == id {
			return run, true
		}
	}
	return ci.Run{}, false
}

func (f *fakeCIRunner) List() []ci.Run {
	f.listCalls++
	return f.runs
}

// Find is the narrowed listing, answered through a real in-memory run
// store rather than by a second filter written here: the route's job is
// to hand the store a filter and a limit and serve the page it gets back
// (ci.RunStore.Find), so what a fake owes it is a page with a store's
// own rules on it.
func (f *fakeCIRunner) Find(filter ci.RunFilter, limit int) ci.RunPage {
	f.findCalls++
	store := ci.NewRunStore(len(f.runs) + 1)
	for _, run := range f.runs {
		store.Put(run)
	}
	return store.Find(filter, limit)
}

func (f *fakeCIRunner) CacheStats(context.Context) (ci.CacheStats, bool, error) {
	if !f.cache {
		return ci.CacheStats{}, false, nil
	}
	return f.stats, true, f.cacheErr
}

func (f *fakeCIRunner) ClearCache(context.Context) (int, bool, error) {
	if !f.cache {
		return 0, false, nil
	}
	f.cleared++
	f.stats.Entries, f.stats.Bytes = 0, 0
	return f.removes, true, f.clearErr
}

func (f *fakeCIRunner) Cancel(id, reason string, by ci.CancelSource) bool {
	f.cancelled = append(f.cancelled, id)
	f.reasons = append(f.reasons, reason)
	f.sources = append(f.sources, by)
	return f.cancelOK
}

func ciServer(t *testing.T, runner ci.Runner) (*ui.Server, *model.Store) {
	t.Helper()
	client, store, _ := testClient(t)
	client.Config.CI = runner
	return ui.NewServerWithClient(client), store
}

// enableCI turns grain CI on for one repo the way an operator would:
// that repo's CI mode set to grain (RepoPage's "Use Grain CI").
func enableCI(t *testing.T, store *model.Store, repo model.RepoRef) {
	t.Helper()
	if err := store.PutRepoConfig(context.Background(), model.RepoConfig{
		Repo: repo, CIMode: model.CIModeGrain,
	}); err != nil {
		t.Fatalf("enabling grain CI for %s: %v", repo, err)
	}
}

func TestStartCIRunTakesARepoAndABranch(t *testing.T) {
	t.Parallel()
	runner := &fakeCIRunner{run: ci.Run{ID: "run-1", Repo: "acme/widgets", Ref: "grain/task-1"}}
	srv, store := ciServer(t, runner)
	enableCI(t, store, model.RepoRef{Owner: "acme", Name: "widgets"})

	rec := do(t, srv, http.MethodPost, "/api/ci/runs", `{"repo":"acme/widgets","branch":"grain/task-1"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body)
	}
	got := decode[ci.Run](t, rec)
	if got.ID != "run-1" {
		t.Errorf("the caller is handed the run: %+v", got)
	}
	if len(runner.started) != 1 {
		t.Fatalf("started %d runs", len(runner.started))
	}
	if req := runner.started[0]; req.Repo != "acme/widgets" || req.Ref != "grain/task-1" {
		t.Errorf("request: got %+v", req)
	}
	if runner.started[0].RequestedBy != "alice" {
		t.Errorf("a run records who asked: got %q", runner.started[0].RequestedBy)
	}
	if runner.started[0].Recheck {
		t.Errorf("recheck defaults to false, got true")
	}
}

func TestStartCIRunWithRecheck(t *testing.T) {
	t.Parallel()
	runner := &fakeCIRunner{run: ci.Run{ID: "run-recheck"}}
	srv, store := ciServer(t, runner)
	enableCI(t, store, model.RepoRef{Owner: "acme", Name: "widgets"})

	rec := do(t, srv, http.MethodPost, "/api/ci/runs", `{"repo":"acme/widgets","branch":"main","recheck":true}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body)
	}
	if len(runner.started) != 1 {
		t.Fatalf("started %d runs", len(runner.started))
	}
	if !runner.started[0].Recheck {
		t.Errorf("expected Recheck: true, got false")
	}
}

func TestListCIWorkflowsAndStartSelectedWorkflow(t *testing.T) {
	t.Parallel()
	runner := &fakeCIRunner{
		run: ci.Run{ID: "run-1"},
		workflowOptions: []ci.WorkflowOption{
			{Path: ".github/workflows/ci.yml", Name: "CI"},
			{Path: ".github/workflows/images.yml", Name: "Runner images"},
		},
	}
	srv, store := ciServer(t, runner)
	enableCI(t, store, model.RepoRef{Owner: "acme", Name: "widgets"})

	rec := do(t, srv, http.MethodGet, "/api/ci/workflows?repo=acme/widgets&ref=main", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("workflow status = %d, want 200: %s", rec.Code, rec.Body)
	}
	got := decode[ui.CIWorkflowsResponse](t, rec)
	if len(got.Workflows) != 2 || got.Workflows[1].Path != ".github/workflows/images.yml" {
		t.Fatalf("workflows = %+v", got.Workflows)
	}
	if len(runner.workflowRequests) != 1 || runner.workflowRequests[0].Event != ci.EventManual {
		t.Fatalf("discovery request = %+v", runner.workflowRequests)
	}

	rec = do(t, srv, http.MethodPost, "/api/ci/runs", `{"repo":"acme/widgets","branch":"main","workflow":".github/workflows/ci.yml"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start status = %d, want 202: %s", rec.Code, rec.Body)
	}
	if len(runner.started) != 1 || runner.started[0].Workflow != ".github/workflows/ci.yml" {
		t.Fatalf("start request = %+v", runner.started)
	}
}

func TestTaskScopedCIRunDerivesScopeAndRecordsTheRun(t *testing.T) {
	t.Parallel()
	started := time.Unix(10, 0).UTC()
	runner := &fakeCIRunner{run: ci.Run{ID: "run-task", Repo: "acme/widgets", Ref: "grain/task-7", Commit: "abcdef", StartedAt: &started}}
	srv, store := ciServer(t, runner)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutTask(context.Background(), model.Task{
		ID: "7", Intent: model.IntentImplement, Title: "test it", Target: &repo,
		Grants: []model.Grant{{Capability: "grain-ci"}},
	}); err != nil {
		t.Fatal(err)
	}

	rec := do(t, srv, http.MethodPost, "/api/ci/runs", `{"taskId":"7","repo":"other/wrong","branch":"wrong"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body)
	}
	if len(runner.started) != 1 {
		t.Fatalf("started %d runs", len(runner.started))
	}
	req := runner.started[0]
	if req.Repo != "acme/widgets" || req.Ref != "grain/task-7" || req.RequestedBy != "7" {
		t.Fatalf("task scope was not enforced: %+v", req)
	}
	obs, err := store.GetObservation(context.Background(), "7")
	if err != nil || obs == nil || obs.CI == nil || obs.CI.RunID != "run-task" || obs.CI.Commit != "abcdef" {
		t.Fatalf("recorded CI = %+v, err=%v", obs, err)
	}
}

func TestTaskScopedSelectiveCIRunDoesNotReplaceTheRequiredVerdict(t *testing.T) {
	t.Parallel()
	started := time.Unix(10, 0).UTC()
	runner := &fakeCIRunner{run: ci.Run{ID: "run-selected", Repo: "acme/widgets", Ref: "grain/task-7", Commit: "abcdef", StartedAt: &started}}
	srv, store := ciServer(t, runner)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutTask(context.Background(), model.Task{
		ID: "7", Intent: model.IntentImplement, Title: "test it", Target: &repo,
		Grants: []model.Grant{{Capability: "grain-ci"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.ObserveField(context.Background(), "7", started, func(o *model.Observation) {
		o.CI = &model.CIStatus{RunID: "run-required", Commit: "old-full", Conclusion: string(ci.ConclusionSuccess)}
	}); err != nil {
		t.Fatal(err)
	}

	rec := do(t, srv, http.MethodPost, "/api/ci/runs", `{"taskId":"7","jobs":["go","frontend"]}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body)
	}
	if got := strings.Join(runner.started[0].Jobs, ","); got != "go,frontend" {
		t.Fatalf("jobs = %q, want go,frontend", got)
	}
	obs, err := store.GetObservation(context.Background(), "7")
	if err != nil || obs == nil || obs.CI == nil || obs.CI.RunID != "run-required" {
		t.Fatalf("selective run replaced required CI = %+v, err=%v", obs, err)
	}
	if len(runner.cancelled) != 0 {
		t.Fatalf("selective run cancelled required run(s): %v", runner.cancelled)
	}
}

// retrigger_ci's default (recheckFailed) reruns only the jobs whose
// newest result at the commit did not pass -- not the go job that
// passed, even though the newest run on the branch was a selective go
// run that said nothing about the failing one -- and makes the recheck
// the second sample of the task's red verdict, so that the
// orchestrator combines the two and the gate lifts when it passes
// (grain/task-2124).
func TestTaskScopedRecheckRerunsOnlyTheFailedJobsAndBecomesTheVerdictsSecondSample(t *testing.T) {
	t.Parallel()
	first, later := time.Unix(10, 0).UTC(), time.Unix(20, 0).UTC()
	job := func(id, name string, c ci.Conclusion) ci.JobRun {
		return ci.JobRun{Workflow: "tests", WorkflowPath: ".grain/workflows/tests.yml", JobID: id, Name: name, Conclusion: c}
	}
	runner := &fakeCIRunner{
		run: ci.Run{ID: "run-recheck", Repo: "acme/widgets", Ref: "grain/task-7", Commit: "abcdef0123", Recheck: true,
			Jobs: []ci.JobRun{job("ui-e2e", "ui e2e", "")}},
		runs: []ci.Run{
			{ID: "run-full", Repo: "acme/widgets", Ref: "grain/task-7", Commit: "abcdef0123", StartedAt: &first,
				Conclusion: ci.ConclusionFailure, Jobs: []ci.JobRun{
					job("go", "go", ci.ConclusionSuccess), job("ui-e2e", "ui e2e", ci.ConclusionFailure),
					job("lint", "lint", ci.ConclusionSuccess)}},
			{ID: "run-go", Repo: "acme/widgets", Ref: "grain/task-7", Commit: "abcdef0123", StartedAt: &later,
				Conclusion: ci.ConclusionSuccess, Jobs: []ci.JobRun{job("go", "go", ci.ConclusionSuccess)}},
		},
	}
	srv, store := ciServer(t, runner)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutTask(context.Background(), model.Task{
		ID: "7", Intent: model.IntentImplement, Title: "test it", Target: &repo,
		Grants: []model.Grant{{Capability: "grain-ci"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.ObserveField(context.Background(), "7", first, func(o *model.Observation) {
		o.CI = &model.CIStatus{RunID: "run-full", Commit: "abcdef0123", Conclusion: string(ci.ConclusionFailure),
			Detail: "failure: tests/ui e2e", Attempts: 1}
	}); err != nil {
		t.Fatal(err)
	}

	rec := do(t, srv, http.MethodPost, "/api/ci/runs", `{"taskId":"7","recheck":true,"recheckFailed":true}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body)
	}
	req := runner.started[0]
	if got := strings.Join(req.Jobs, ","); got != ".grain/workflows/tests.yml:ui-e2e" || !req.Recheck || req.Commit != "abcdef0123" {
		t.Fatalf("recheck asked for jobs %q (recheck %v) at %q, want only the failed ui e2e at abcdef0123", got, req.Recheck, req.Commit)
	}
	obs, err := store.GetObservation(context.Background(), "7")
	if err != nil || obs == nil || obs.CI == nil {
		t.Fatalf("observation = %+v, err=%v", obs, err)
	}
	if got := *obs.CI; got.RunID != "run-recheck" || got.RecheckOf != "run-full" ||
		got.RecheckDetail != "failure: tests/ui e2e" || got.Done() || got.Attempts != 1 {
		t.Fatalf("recorded CI = %+v, want the recheck as the red verdict's second sample", got)
	}

	// While it runs, a full-suite wait is handed the recheck rather than
	// starting a run that would read the old red, and cancels nothing.
	runner.runs = append(runner.runs, runner.run)
	rec = do(t, srv, http.MethodPost, "/api/ci/runs", `{"taskId":"7"}`)
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"run-recheck"`) {
		t.Fatalf("full-suite wait during a recheck = %d %s, want the recheck", rec.Code, rec.Body)
	}
	if len(runner.started) != 1 || len(runner.cancelled) != 0 {
		t.Fatalf("full-suite wait during a recheck started %d runs and cancelled %v", len(runner.started)-1, runner.cancelled)
	}
}

func TestTaskScopedRecheckBeforePullRequestJoinsWaitForChecks(t *testing.T) {
	t.Parallel()
	first := time.Unix(10, 0).UTC()
	job := func(id, name string, c ci.Conclusion) ci.JobRun {
		return ci.JobRun{Workflow: "tests", WorkflowPath: ".grain/workflows/tests.yml", JobID: id, Name: name, Conclusion: c}
	}
	runner := &fakeCIRunner{
		run: ci.Run{ID: "run-recheck", Repo: "acme/widgets", Ref: "grain/task-7", Commit: "abcdef0123", Recheck: true,
			Jobs: []ci.JobRun{job("ui-e2e", "ui e2e", "")}},
		runs: []ci.Run{
			{ID: "run-full", Repo: "acme/widgets", Ref: "grain/task-7", Commit: "abcdef0123", StartedAt: &first,
				Conclusion: ci.ConclusionFailure, Jobs: []ci.JobRun{
					job("go", "go", ci.ConclusionSuccess),
					job("ui-e2e", "ui e2e", ci.ConclusionFailure),
				}},
		},
	}
	srv, store := ciServer(t, runner)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutTask(context.Background(), model.Task{
		ID: "7", Intent: model.IntentImplement, Title: "test it", Target: &repo,
		Grants: []model.Grant{{Capability: "grain-ci"}},
	}); err != nil {
		t.Fatal(err)
	}
	// Before open_pull_request (or before reconcileTaskCI collects the
	// finished run), Observation.CI has Conclusion == "" and Detail == "".
	if err := store.ObserveField(context.Background(), "7", first, func(o *model.Observation) {
		o.CI = &model.CIStatus{RunID: "run-full", Commit: "abcdef0123", StartedAt: &first}
	}); err != nil {
		t.Fatal(err)
	}

	rec := do(t, srv, http.MethodPost, "/api/ci/runs", `{"taskId":"7","recheck":true,"recheckFailed":true}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body)
	}
	obs, err := store.GetObservation(context.Background(), "7")
	if err != nil || obs == nil || obs.CI == nil {
		t.Fatalf("observation = %+v, err=%v", obs, err)
	}
	if got := *obs.CI; got.RunID != "run-recheck" || got.RecheckOf != "run-full" ||
		got.RecheckDetail != "failure: tests/ui e2e" || got.Done() {
		t.Fatalf("recorded CI = %+v, want pre-PR recheck recorded with RecheckOf=run-full", got)
	}

	runner.runs = append(runner.runs, runner.run)
	rec = do(t, srv, http.MethodPost, "/api/ci/runs", `{"taskId":"7"}`)
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"run-recheck"`) {
		t.Fatalf("wait_for_checks after pre-PR retrigger_ci = %d %s, want run-recheck joined", rec.Code, rec.Body)
	}
	if len(runner.started) != 1 || len(runner.cancelled) != 0 {
		t.Fatalf("wait_for_checks started %d extra runs and cancelled %v, want 0 and none", len(runner.started)-1, runner.cancelled)
	}
}

func TestTaskScopedRecheckAfterTimedOutAndFailedRunIsRecordedAndJoined(t *testing.T) {
	t.Parallel()
	first := time.Unix(10, 0).UTC()
	job := func(id, name string, c ci.Conclusion) ci.JobRun {
		return ci.JobRun{Workflow: "tests", WorkflowPath: ".grain/workflows/tests.yml", JobID: id, Name: name, Conclusion: c}
	}
	runner := &fakeCIRunner{
		run: ci.Run{ID: "run-recheck", Repo: "acme/widgets", Ref: "grain/task-7", Commit: "abcdef0123", Recheck: true,
			Jobs: []ci.JobRun{job("go", "go", ""), job("ui-e2e", "ui e2e", "")}},
		runs: []ci.Run{
			{ID: "run-full", Repo: "acme/widgets", Ref: "grain/task-7", Commit: "abcdef0123", StartedAt: &first,
				Conclusion: ci.ConclusionCancelled, Jobs: []ci.JobRun{
					job("go", "go", ci.ConclusionFailure),
					job("ui-e2e", "ui e2e", ci.ConclusionCancelled),
					job("lint", "lint", ci.ConclusionSuccess),
				}},
		},
	}
	srv, store := ciServer(t, runner)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutTask(context.Background(), model.Task{
		ID: "7", Intent: model.IntentImplement, Title: "test it", Target: &repo,
		Grants: []model.Grant{{Capability: "grain-ci"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.ObserveField(context.Background(), "7", first, func(o *model.Observation) {
		o.CI = &model.CIStatus{RunID: "run-full", Commit: "abcdef0123", Conclusion: string(ci.ConclusionCancelled),
			Detail: "cancelled: tests/go, tests/ui e2e", Attempts: 1}
	}); err != nil {
		t.Fatal(err)
	}

	rec := do(t, srv, http.MethodPost, "/api/ci/runs", `{"taskId":"7","recheck":true,"recheckFailed":true}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body)
	}
	if got := strings.Join(runner.started[0].Jobs, ","); got != ".grain/workflows/tests.yml:go,.grain/workflows/tests.yml:ui-e2e" {
		t.Fatalf("recheck jobs = %q, want both failed go and timed-out ui-e2e", got)
	}
	obs, err := store.GetObservation(context.Background(), "7")
	if err != nil || obs == nil || obs.CI == nil {
		t.Fatalf("observation = %+v, err=%v", obs, err)
	}
	if got := *obs.CI; got.RunID != "run-recheck" || got.RecheckOf != "run-full" ||
		got.RecheckDetail != "cancelled: tests/go, tests/ui e2e" || got.Done() {
		t.Fatalf("recorded CI = %+v, want recheck of cancelled/timed-out run recorded", got)
	}

	runner.runs = append(runner.runs, runner.run)
	rec = do(t, srv, http.MethodPost, "/api/ci/runs", `{"taskId":"7"}`)
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"run-recheck"`) {
		t.Fatalf("wait_for_checks after timed-out run recheck = %d %s, want run-recheck joined", rec.Code, rec.Body)
	}
	if len(runner.started) != 1 || len(runner.cancelled) != 0 {
		t.Fatalf("wait_for_checks started %d extra runs and cancelled %v", len(runner.started)-1, runner.cancelled)
	}
}

func TestTaskScopedSelectiveRunsAreSuperseded(t *testing.T) {
	t.Parallel()
	t1, t2 := time.Unix(10, 0).UTC(), time.Unix(20, 0).UTC()
	job := func(id string, c ci.Conclusion) ci.JobRun {
		return ci.JobRun{Workflow: "tests", WorkflowPath: ".grain/workflows/tests.yml", JobID: id, Name: id, Conclusion: c}
	}

	t.Run("full run cancels live selective runs on branch", func(t *testing.T) {
		t.Parallel()
		runner := &fakeCIRunner{
			run: ci.Run{ID: "run-full", Repo: "acme/widgets", Ref: "grain/task-7", Commit: "abcdef0123", StartedAt: &t2},
			runs: []ci.Run{
				{ID: "run-sel-1", Repo: "acme/widgets", Ref: "grain/task-7", Commit: "abcdef0123", StartedAt: &t1,
					Jobs: []ci.JobRun{job("go", "")}},
				{ID: "run-sel-2", Repo: "acme/widgets", Ref: "grain/task-7", Commit: "abcdef0123", StartedAt: &t2,
					Jobs: []ci.JobRun{job("lint", "")}},
				{ID: "run-other-branch", Repo: "acme/widgets", Ref: "grain/task-70", Commit: "abcdef0123", StartedAt: &t1,
					Jobs: []ci.JobRun{job("go", "")}},
			},
		}
		srv, store := ciServer(t, runner)
		repo := model.RepoRef{Owner: "acme", Name: "widgets"}
		if err := store.PutTask(context.Background(), model.Task{
			ID: "7", Intent: model.IntentImplement, Title: "test it", Target: &repo,
			Grants: []model.Grant{{Capability: "grain-ci"}},
		}); err != nil {
			t.Fatal(err)
		}
		rec := do(t, srv, http.MethodPost, "/api/ci/runs", `{"taskId":"7"}`)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body)
		}
		if got := strings.Join(runner.cancelled, ","); got != "run-sel-2,run-sel-1" {
			t.Fatalf("cancelled = %v, want [run-sel-2 run-sel-1] (and not run-other-branch)", runner.cancelled)
		}
	})

	t.Run("recheck cancels live selective and earlier runs on branch", func(t *testing.T) {
		t.Parallel()
		runner := &fakeCIRunner{
			run: ci.Run{ID: "run-recheck", Repo: "acme/widgets", Ref: "grain/task-7", Commit: "abcdef0123", Recheck: true,
				Jobs: []ci.JobRun{job("go", "")}},
			runs: []ci.Run{
				{ID: "run-full", Repo: "acme/widgets", Ref: "grain/task-7", Commit: "abcdef0123", StartedAt: &t1,
					Conclusion: ci.ConclusionFailure, Jobs: []ci.JobRun{job("go", ci.ConclusionFailure)}},
				{ID: "run-sel-live", Repo: "acme/widgets", Ref: "grain/task-7", Commit: "abcdef0123", StartedAt: &t2,
					Jobs: []ci.JobRun{job("ui-e2e", "")}},
			},
		}
		srv, store := ciServer(t, runner)
		repo := model.RepoRef{Owner: "acme", Name: "widgets"}
		if err := store.PutTask(context.Background(), model.Task{
			ID: "7", Intent: model.IntentImplement, Title: "test it", Target: &repo,
			Grants: []model.Grant{{Capability: "grain-ci"}},
		}); err != nil {
			t.Fatal(err)
		}
		rec := do(t, srv, http.MethodPost, "/api/ci/runs", `{"taskId":"7","recheck":true,"recheckFailed":true}`)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body)
		}
		if got := strings.Join(runner.cancelled, ","); got != "run-sel-live" {
			t.Fatalf("cancelled = %v, want [run-sel-live]", runner.cancelled)
		}
	})

	t.Run("selective run cancels earlier selective run and older-commit required run", func(t *testing.T) {
		t.Parallel()
		runner := &fakeCIRunner{
			run: ci.Run{ID: "run-sel-new", Repo: "acme/widgets", Ref: "grain/task-7", Commit: "2222222222", StartedAt: &t2},
			runs: []ci.Run{
				{ID: "run-full-old", Repo: "acme/widgets", Ref: "grain/task-7", Commit: "1111111111", StartedAt: &t1},
				{ID: "run-sel-old", Repo: "acme/widgets", Ref: "grain/task-7", Commit: "2222222222", StartedAt: &t1},
			},
		}
		srv, store := ciServer(t, runner)
		repo := model.RepoRef{Owner: "acme", Name: "widgets"}
		if err := store.PutTask(context.Background(), model.Task{
			ID: "7", Intent: model.IntentImplement, Title: "test it", Target: &repo,
			Grants: []model.Grant{{Capability: "grain-ci"}},
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.ObserveField(context.Background(), "7", t1, func(o *model.Observation) {
			o.CI = &model.CIStatus{RunID: "run-full-old", Commit: "1111111111", StartedAt: &t1}
		}); err != nil {
			t.Fatal(err)
		}
		rec := do(t, srv, http.MethodPost, "/api/ci/runs", `{"taskId":"7","jobs":["go"]}`)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body)
		}
		if got := strings.Join(runner.cancelled, ","); got != "run-full-old,run-sel-old" {
			t.Fatalf("cancelled = %v, want [run-full-old run-sel-old]", runner.cancelled)
		}
	})

	t.Run("recheck of an unrecorded selective run does not become the required verdict", func(t *testing.T) {
		t.Parallel()
		runner := &fakeCIRunner{
			run: ci.Run{ID: "run-recheck", Repo: "acme/widgets", Ref: "grain/task-7", Commit: "2222222222", Recheck: true,
				Jobs: []ci.JobRun{job("lint", "")}},
			runs: []ci.Run{
				{ID: "run-sel-failed", Repo: "acme/widgets", Ref: "grain/task-7", Commit: "2222222222", StartedAt: &t1,
					Conclusion: ci.ConclusionFailure, Jobs: []ci.JobRun{job("lint", ci.ConclusionFailure)}},
			},
		}
		srv, store := ciServer(t, runner)
		repo := model.RepoRef{Owner: "acme", Name: "widgets"}
		if err := store.PutTask(context.Background(), model.Task{
			ID: "7", Intent: model.IntentImplement, Title: "test it", Target: &repo,
			Grants: []model.Grant{{Capability: "grain-ci"}},
		}); err != nil {
			t.Fatal(err)
		}
		rec := do(t, srv, http.MethodPost, "/api/ci/runs", `{"taskId":"7","recheck":true,"recheckFailed":true}`)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body)
		}
		obs, err := store.GetObservation(context.Background(), "7")
		if err != nil {
			t.Fatal(err)
		}
		if obs != nil && obs.CI != nil {
			t.Fatalf("observation.CI = %+v, want nil (selective run must not become required verdict)", obs.CI)
		}
		// A subsequent full-suite wait_for_checks must start the full suite and
		// cancel the unrecorded recheck rather than joining it.
		runner.runs = append(runner.runs, runner.run)
		runner.run = ci.Run{ID: "run-full-new", Repo: "acme/widgets", Ref: "grain/task-7", Commit: "2222222222", StartedAt: &t2}
		rec = do(t, srv, http.MethodPost, "/api/ci/runs", `{"taskId":"7"}`)
		if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"run-full-new"`) {
			t.Fatalf("full-suite wait = %d %s, want run-full-new started", rec.Code, rec.Body)
		}
		if got := strings.Join(runner.cancelled, ","); got != "run-recheck" {
			t.Fatalf("cancelled = %v, want [run-recheck]", runner.cancelled)
		}
	})

	t.Run("diagnostic recheck does not cancel an in-flight required run at the same commit", func(t *testing.T) {
		t.Parallel()
		runner := &fakeCIRunner{
			run: ci.Run{ID: "run-diag-recheck", Repo: "acme/widgets", Ref: "grain/task-7", Commit: "abcdef0123", Recheck: true,
				Jobs: []ci.JobRun{job("lint", "")}},
			runs: []ci.Run{
				{ID: "run-full-live", Repo: "acme/widgets", Ref: "grain/task-7", Commit: "abcdef0123", StartedAt: &t1,
					Jobs: []ci.JobRun{job("lint", ci.ConclusionSuccess), job("go", "")}},
			},
		}
		srv, store := ciServer(t, runner)
		repo := model.RepoRef{Owner: "acme", Name: "widgets"}
		if err := store.PutTask(context.Background(), model.Task{
			ID: "7", Intent: model.IntentImplement, Title: "test it", Target: &repo,
			Grants: []model.Grant{{Capability: "grain-ci"}},
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.ObserveField(context.Background(), "7", t1, func(o *model.Observation) {
			o.CI = &model.CIStatus{RunID: "run-full-live", Commit: "abcdef0123", StartedAt: &t1}
		}); err != nil {
			t.Fatal(err)
		}
		rec := do(t, srv, http.MethodPost, "/api/ci/runs", `{"taskId":"7","recheck":true,"jobs":["lint"]}`)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body)
		}
		if len(runner.cancelled) != 0 {
			t.Fatalf("cancelled = %v, want in-flight required run preserved", runner.cancelled)
		}
		obs, err := store.GetObservation(context.Background(), "7")
		if err != nil || obs == nil || obs.CI == nil || obs.CI.RunID != "run-full-live" {
			t.Fatalf("observation.CI = %+v, want run-full-live preserved", obs)
		}
	})

	t.Run("chained recheck and skipped downstream jobs preserve full failure set in RecheckDetail", func(t *testing.T) {
		t.Parallel()
		runner := &fakeCIRunner{
			run: ci.Run{ID: "run-recheck-2", Repo: "acme/widgets", Ref: "grain/task-7", Commit: "abcdef0123", Recheck: true,
				Jobs: []ci.JobRun{job("go", "")}},
			runs: []ci.Run{
				{ID: "run-recheck-1", Repo: "acme/widgets", Ref: "grain/task-7", Commit: "abcdef0123", StartedAt: &t2, Recheck: true,
					Conclusion: ci.ConclusionFailure, Jobs: []ci.JobRun{job("go", ci.ConclusionFailure)}},
				{ID: "run-full", Repo: "acme/widgets", Ref: "grain/task-7", Commit: "abcdef0123", StartedAt: &t1,
					Conclusion: ci.ConclusionFailure, Jobs: []ci.JobRun{
						job("go", ci.ConclusionFailure),
						job("ui-e2e", ci.ConclusionFailure),
						job("smoke", ci.ConclusionSkipped),
					}},
			},
		}
		srv, store := ciServer(t, runner)
		repo := model.RepoRef{Owner: "acme", Name: "widgets"}
		if err := store.PutTask(context.Background(), model.Task{
			ID: "7", Intent: model.IntentImplement, Title: "test it", Target: &repo,
			Grants: []model.Grant{{Capability: "grain-ci"}},
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.ObserveField(context.Background(), "7", t1, func(o *model.Observation) {
			o.CI = &model.CIStatus{
				RunID: "run-recheck-1", Commit: "abcdef0123", StartedAt: &t1,
				RecheckOf: "run-full", RecheckDetail: "failure: tests/go, tests/ui-e2e",
			}
		}); err != nil {
			t.Fatal(err)
		}
		rec := do(t, srv, http.MethodPost, "/api/ci/runs", `{"taskId":"7","recheck":true,"jobs":["go"]}`)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body)
		}
		obs, err := store.GetObservation(context.Background(), "7")
		if err != nil || obs == nil || obs.CI == nil {
			t.Fatalf("observation = %+v, err=%v", obs, err)
		}
		if got := *obs.CI; got.RunID != "run-recheck-2" || got.RecheckOf != "run-full" ||
			got.RecheckDetail != "failure: tests/go, tests/ui-e2e, tests/smoke" {
			t.Fatalf("recorded CI = %+v, want RecheckDetail preserving un-rechecked ui-e2e and skipped smoke", got)
		}
	})
}

// retrigger_ci's default reads the commit the branch's newest run was
// against, not its oldest: after a second push the jobs re-run are the
// ones that failed at the new head. The zero ci.RunFilter is oldest
// first, which is what this used to ask for (grain/task-2388).
func TestTaskScopedRecheckOfTheFailedJobsIsAtTheNewestCommit(t *testing.T) {
	t.Parallel()
	older, newer := time.Unix(10, 0).UTC(), time.Unix(20, 0).UTC()
	job := func(id string, c ci.Conclusion) ci.JobRun {
		return ci.JobRun{Workflow: "tests", WorkflowPath: ".grain/workflows/tests.yml", JobID: id, Name: id, Conclusion: c}
	}
	runner := &fakeCIRunner{
		run: ci.Run{ID: "run-recheck", Commit: "2222222222"},
		runs: []ci.Run{
			{ID: "run-first-push", Repo: "acme/widgets", Ref: "grain/task-7", Commit: "1111111111", StartedAt: &older,
				Conclusion: ci.ConclusionFailure, Jobs: []ci.JobRun{job("go", ci.ConclusionFailure), job("lint", ci.ConclusionSuccess)}},
			{ID: "run-second-push", Repo: "acme/widgets", Ref: "grain/task-7", Commit: "2222222222", StartedAt: &newer,
				Conclusion: ci.ConclusionFailure, Jobs: []ci.JobRun{job("go", ci.ConclusionSuccess), job("lint", ci.ConclusionFailure)}},
		},
	}
	srv, store := ciServer(t, runner)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutTask(context.Background(), model.Task{
		ID: "7", Intent: model.IntentImplement, Title: "test it", Target: &repo,
		Grants: []model.Grant{{Capability: "grain-ci"}},
	}); err != nil {
		t.Fatal(err)
	}
	rec := do(t, srv, http.MethodPost, "/api/ci/runs", `{"taskId":"7","recheck":true,"recheckFailed":true}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body)
	}
	req := runner.started[0]
	if got := strings.Join(req.Jobs, ","); req.Commit != "2222222222" || got != ".grain/workflows/tests.yml:lint" {
		t.Fatalf("recheck asked for %q at %q, want lint at the second push's 2222222222", got, req.Commit)
	}
}

func TestTaskScopedRecheckJobSelection(t *testing.T) {
	t.Parallel()
	started := time.Unix(10, 0).UTC()
	passing := ci.Run{ID: "run-full", Repo: "acme/widgets", Ref: "grain/task-7", Commit: "abcdef0123", StartedAt: &started,
		Conclusion: ci.ConclusionSuccess, Jobs: []ci.JobRun{{JobID: "go", Conclusion: ci.ConclusionSuccess}}}
	for _, tc := range []struct {
		name, body, wantJobs, wantError string
		runs                            []ci.Run
	}{
		{name: "explicit list", body: `{"taskId":"7","recheck":true,"jobs":["ui e2e"]}`, wantJobs: "ui e2e", runs: []ci.Run{passing}},
		{name: "all of them", body: `{"taskId":"7","recheck":true}`, wantJobs: "", runs: []ci.Run{passing}},
		{name: "nothing failed", body: `{"taskId":"7","recheck":true,"recheckFailed":true}`, runs: []ci.Run{passing},
			wantError: "no job failed"},
		{name: "nothing ran", body: `{"taskId":"7","recheck":true,"recheckFailed":true}`, wantError: "no grain CI run"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runner := &fakeCIRunner{run: ci.Run{ID: "run-recheck", Commit: "abcdef0123"}, runs: tc.runs}
			srv, store := ciServer(t, runner)
			repo := model.RepoRef{Owner: "acme", Name: "widgets"}
			if err := store.PutTask(context.Background(), model.Task{
				ID: "7", Intent: model.IntentImplement, Title: "test it", Target: &repo,
				Grants: []model.Grant{{Capability: "grain-ci"}},
			}); err != nil {
				t.Fatal(err)
			}
			rec := do(t, srv, http.MethodPost, "/api/ci/runs", tc.body)
			if tc.wantError != "" {
				if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.wantError) || len(runner.started) != 0 {
					t.Fatalf("got %d %s with %d runs started, want a 400 saying %q", rec.Code, rec.Body, len(runner.started), tc.wantError)
				}
				return
			}
			if rec.Code != http.StatusAccepted {
				t.Fatalf("status = %d: %s", rec.Code, rec.Body)
			}
			if got := strings.Join(runner.started[0].Jobs, ","); got != tc.wantJobs || !runner.started[0].Recheck {
				t.Fatalf("jobs = %q (recheck %v), want %q", got, runner.started[0].Recheck, tc.wantJobs)
			}
		})
	}
}

func TestStartCIRunRefusesARepoGrainCIIsNotEnabledFor(t *testing.T) {
	t.Parallel()
	runner := &fakeCIRunner{run: ci.Run{ID: "run-1"}}
	srv, _ := ciServer(t, runner)

	rec := do(t, srv, http.MethodPost, "/api/ci/runs", `{"repo":"acme/widgets","branch":"main"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", rec.Code, rec.Body)
	}
	if len(runner.started) != 0 {
		t.Error("a refused request must not have started anything")
	}
	if body := rec.Body.String(); !strings.Contains(body, "Use Grain CI") {
		t.Errorf("the refusal should name the CI mode that turns this on: %s", body)
	}
}

// The grain-ci capability is grantable (6172e1205), and this refusal used
// to tell an operator to attach it -- but since 57939261f a repo's CI mode
// is what turns grain CI on, so one who did as told, to the repo and to
// the deployment both, was refused again by the same sentence
// (docs/test-runs/2026-10-05-grain-ci-grantable-and-ports.md, finding A).
// The refusal has to name the switch that works, and say the capability
// is not it.
func TestStartCIRunRefusalDoesNotSendAnOperatorBackToTheCapability(t *testing.T) {
	t.Parallel()
	runner := &fakeCIRunner{run: ci.Run{ID: "run-1"}}
	srv, store := ciServer(t, runner)
	ctx := context.Background()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo: repo, DefaultCapabilities: []string{"grain-ci"},
	}); err != nil {
		t.Fatal(err)
	}
	cfg, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil {
		cfg = &model.Config{}
	}
	cfg.DefaultCapabilities = append(cfg.DefaultCapabilities, "grain-ci")
	if err := store.PutConfig(ctx, *cfg); err != nil {
		t.Fatal(err)
	}

	rec := do(t, srv, http.MethodPost, "/api/ci/runs", `{"repo":"acme/widgets","branch":"main"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 -- the capability alone does not enable grain CI: %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if strings.Contains(body, "attach the") {
		t.Errorf("the refusal tells the operator to attach a capability that is already attached: %s", body)
	}
	if !strings.Contains(body, "CI Mode") || !strings.Contains(body, "does not turn it on") {
		t.Errorf("the refusal should name the CI mode and say the capability is not the switch: %s", body)
	}

	enableCI(t, store, repo)
	if rec := do(t, srv, http.MethodPost, "/api/ci/runs", `{"repo":"acme/widgets","branch":"main"}`); rec.Code != http.StatusAccepted {
		t.Fatalf("with the CI mode set: status = %d, want 202: %s", rec.Code, rec.Body)
	}
}

func TestStartCIRunAcceptsRepoEnable(t *testing.T) {
	t.Parallel()
	runner := &fakeCIRunner{run: ci.Run{ID: "run-1"}}
	srv, store := ciServer(t, runner)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(context.Background(), model.RepoConfig{
		Repo:           repo,
		IgnoreGitHubCI: true,
	}); err != nil {
		t.Fatalf("writing repo config: %v", err)
	}

	rec := do(t, srv, http.MethodPost, "/api/ci/runs", `{"repo":"acme/widgets","branch":"main"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body)
	}
}

func TestCIRoutesSayNothingIsWiredWhenNothingIs(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	if rec := do(t, srv, http.MethodPost, "/api/ci/runs", `{"repo":"acme/widgets","branch":"main"}`); rec.Code != http.StatusNotFound {
		t.Errorf("POST status = %d, want 404 on a deployment with no CI wired", rec.Code)
	}
	if rec := do(t, srv, http.MethodGet, "/api/ci/runs/anything", ""); rec.Code != http.StatusNotFound {
		t.Errorf("GET status = %d, want 404", rec.Code)
	}
	rec := do(t, srv, http.MethodGet, "/api/ci/runs", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("listing status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := decode[ui.CIRunsResponse](t, rec); got.Enabled {
		t.Errorf("a deployment with no CI wired reports itself unavailable rather than empty: %+v", got)
	}
}

func TestListAndGetCIRuns(t *testing.T) {
	t.Parallel()
	runner := &fakeCIRunner{runs: []ci.Run{
		{ID: "a", Repo: "acme/widgets", Conclusion: ci.ConclusionSuccess, StartedAt: ptr(time.Unix(2, 0))},
		{ID: "b", Repo: "other/repo", Conclusion: ci.ConclusionFailure, StartedAt: ptr(time.Unix(1, 0))},
	}}
	srv, _ := ciServer(t, runner)

	rec := do(t, srv, http.MethodGet, "/api/ci/runs", "")
	listed := decode[ui.CIRunsResponse](t, rec)
	if !listed.Enabled || len(listed.Runs) != 2 {
		t.Fatalf("listing: %+v", listed)
	}

	rec = do(t, srv, http.MethodGet, "/api/ci/runs?repo=acme/widgets", "")
	listed = decode[ui.CIRunsResponse](t, rec)
	if len(listed.Runs) != 1 || listed.Runs[0].ID != "a" {
		t.Fatalf("filtered listing: %+v", listed.Runs)
	}

	rec = do(t, srv, http.MethodGet, "/api/ci/runs/b", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if got := decode[ci.Run](t, rec); got.Conclusion != ci.ConclusionFailure {
		t.Errorf("run: %+v", got)
	}

	if rec := do(t, srv, http.MethodGet, "/api/ci/runs/nope", ""); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown run is a 404, got %d", rec.Code)
	}
}

// grain/task-473: the CI pane on grain/config's repo page lists that
// repository's grain CI runs -- the config repository is checked by grain
// CI on every deployment, and its runs are filed under its own name.
func TestListCIRunsForTheConfigRepo(t *testing.T) {
	t.Parallel()
	runner := &fakeCIRunner{runs: []ci.Run{
		{ID: "cfg", Repo: "grain/config", Ref: "grain/049f1e09/task-465", Conclusion: ci.ConclusionSuccess, StartedAt: ptr(time.Unix(2, 0))},
		{ID: "w", Repo: "acme/widgets", Conclusion: ci.ConclusionSuccess, StartedAt: ptr(time.Unix(1, 0))},
	}}
	srv, _ := ciServer(t, runner)
	rec := do(t, srv, http.MethodGet, "/api/ci/runs?repo=grain%2Fconfig", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	listed := decode[ui.CIRunsResponse](t, rec)
	if !listed.Enabled || len(listed.Runs) != 1 || listed.Runs[0].ID != "cfg" {
		t.Fatalf("grain/config runs = %+v", listed.Runs)
	}
}

// A step's output can be a quarter of a megabyte, a run has as many
// steps as its workflows declare, and a deployment remembers a hundred
// runs -- so the listing the pane polls carries every job and every step
// but not what any of them printed. The route that shows a step is where
// output belongs, and it still has all of it.
func TestListCIRunsLeavesStepOutputToTheRunItself(t *testing.T) {
	t.Parallel()
	run := ci.Run{
		ID: "a", Repo: "acme/widgets", Conclusion: ci.ConclusionFailure,
		Jobs: []ci.JobRun{{
			Name: "test", Conclusion: ci.ConclusionFailure,
			Steps: []ci.StepRun{
				{Name: "build", Conclusion: ci.ConclusionSuccess, Output: "compiling"},
				{Name: "go test", Conclusion: ci.ConclusionFailure, ExitCode: 1, Output: "FAIL: TestThing"},
			},
		}},
	}
	runner := &fakeCIRunner{runs: []ci.Run{run}}
	srv, _ := ciServer(t, runner)

	listed := decode[ui.CIRunsResponse](t, do(t, srv, http.MethodGet, "/api/ci/runs", ""))
	if len(listed.Runs) != 1 || len(listed.Runs[0].Jobs) != 1 {
		t.Fatalf("listing: %+v", listed.Runs)
	}
	job := listed.Runs[0].Jobs[0]
	if len(job.Steps) != 2 {
		t.Fatalf("the listing keeps the steps themselves -- that is what says how far a run has got: %+v", job)
	}
	for _, step := range job.Steps {
		if step.Output != "" {
			t.Errorf("step %q carries its output in the listing: %q", step.Name, step.Output)
		}
	}
	if job.Steps[1].ExitCode != 1 || job.Steps[1].Conclusion != ci.ConclusionFailure {
		t.Errorf("the listing keeps everything but the output: %+v", job.Steps[1])
	}
	// And the runner's own copy is untouched: trimming a response must
	// not empty out the run the deployment is still holding.
	if runner.runs[0].Jobs[0].Steps[1].Output == "" {
		t.Error("the listing emptied the store's own copy of the run")
	}

	got := decode[ci.Run](t, do(t, srv, http.MethodGet, "/api/ci/runs/a", ""))
	if len(got.Jobs) != 1 || got.Jobs[0].Steps[1].Output != "FAIL: TestThing" {
		t.Errorf("the run itself must carry what its steps printed: %+v", got.Jobs)
	}
}

// The four calls behind `grain ci`, over a real HTTP round trip against
// the routes above -- the CLI reaching a daemon the way an operator's
// terminal does.
func TestHTTPClientCIRoundTrip(t *testing.T) {
	t.Parallel()
	runner := &fakeCIRunner{
		run:      ci.Run{ID: "run-1", Repo: "acme/widgets", Ref: "main"},
		cancelOK: true,
	}
	srv, store := ciServer(t, runner)
	enableCI(t, store, model.RepoRef{Owner: "acme", Name: "widgets"})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	c := serverAccessClient(srv, ts.URL)
	ctx := context.Background()

	started, err := c.StartCIRun(ctx, ui.CIRunRequest{Repo: "acme/widgets", Ref: "main", Event: "push"})
	if err != nil {
		t.Fatalf("StartCIRun: %v", err)
	}
	if started.ID != "run-1" {
		t.Fatalf("started = %+v, want the run the daemon began", started)
	}
	if len(runner.started) != 1 || runner.started[0].Event != ci.Event("push") {
		t.Errorf("the request reached the engine as sent: %+v", runner.started)
	}

	runner.runs = []ci.Run{
		{ID: "run-1", Repo: "acme/widgets", Conclusion: ci.ConclusionSuccess},
		{ID: "run-2", Repo: "other/repo", Conclusion: ci.ConclusionFailure},
	}
	runs, enabled, err := c.ListCIRuns(ctx, "")
	if err != nil {
		t.Fatalf("ListCIRuns: %v", err)
	}
	if !enabled || len(runs) != 2 {
		t.Fatalf("ListCIRuns = %+v (enabled %v), want both runs", runs, enabled)
	}
	runs, _, err = c.ListCIRuns(ctx, "acme/widgets")
	if err != nil {
		t.Fatalf("ListCIRuns(repo): %v", err)
	}
	if len(runs) != 1 || runs[0].ID != "run-1" {
		t.Fatalf("ListCIRuns(repo) = %+v, want that repository's run alone", runs)
	}

	got, err := c.CIRun(ctx, "run-2")
	if err != nil {
		t.Fatalf("CIRun: %v", err)
	}
	if got.Conclusion != ci.ConclusionFailure {
		t.Errorf("CIRun = %+v", got)
	}
	if _, err := c.CIRun(ctx, "nope"); err == nil {
		t.Error("a run this deployment does not hold should be an error")
	} else if _, ok := err.(*ui.NotFoundError); !ok {
		t.Errorf("a forgotten run should arrive as a NotFoundError, got %T: %v", err, err)
	}

	if _, err := c.CancelCIRun(ctx, "run-1", "the base branch is about to be reverted"); err != nil {
		t.Fatalf("CancelCIRun: %v", err)
	}
	if len(runner.cancelled) != 1 || runner.cancelled[0] != "run-1" {
		t.Errorf("cancelled %v", runner.cancelled)
	}
	if len(runner.reasons) != 1 || runner.reasons[0] != "the base branch is about to be reverted" {
		t.Errorf("reasons %q, want the note the caller gave", runner.reasons)
	}
	runner.cancelOK = false
	if _, err := c.CancelCIRun(ctx, "run-1", ""); err == nil {
		t.Error("cancelling a run that is not going should be an error")
	}
}

// A deployment with no CI wired answers the listing rather than
// refusing it, and the flag is how the CLI and the pane tell "nothing
// runs here" from "nothing has run yet".
func TestHTTPClientCIListSaysWhenNothingIsWired(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	runs, enabled, err := serverAccessClient(srv, ts.URL).ListCIRuns(context.Background(), "")
	if err != nil {
		t.Fatalf("ListCIRuns: %v", err)
	}
	if enabled || len(runs) != 0 {
		t.Fatalf("ListCIRuns = %+v (enabled %v), want an unwired deployment to say so", runs, enabled)
	}
}

func TestCancelCIRun(t *testing.T) {
	t.Parallel()
	runner := &fakeCIRunner{cancelOK: true, runs: []ci.Run{{ID: "a"}}}
	srv, _ := ciServer(t, runner)

	if rec := do(t, srv, http.MethodDelete, "/api/ci/runs/a", ""); rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body)
	}
	if len(runner.cancelled) != 1 || runner.cancelled[0] != "a" {
		t.Errorf("cancelled %v", runner.cancelled)
	}
	// A cancellation with no body at all is the request this route has
	// always answered, and it says nothing about why.
	if len(runner.reasons) != 1 || runner.reasons[0] != "" {
		t.Errorf("reasons %q, want a cancellation with nothing to say", runner.reasons)
	}
	// It still says who: this route is the only door a person cancels
	// through, and a cancellation with nobody's name on it is one the
	// merge queue's reconciler would start again behind them
	// (orchestrator's ciReplacementFor). The note cannot carry that --
	// there is no note here at all.
	if len(runner.sources) != 1 || runner.sources[0] != ci.CancelByOperator {
		t.Errorf("sources %q, want a person's own cancellation", runner.sources)
	}

	// And one with a note carries it through to the runner, trimmed.
	if rec := do(t, srv, http.MethodDelete, "/api/ci/runs/a",
		`{"reason":"  wrong branch, I meant task-9  "}`); rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body)
	}
	if len(runner.reasons) != 2 || runner.reasons[1] != "wrong branch, I meant task-9" {
		t.Errorf("reasons %q, want the note the request carried", runner.reasons)
	}

	runner.cancelOK = false
	if rec := do(t, srv, http.MethodDelete, "/api/ci/runs/a", ""); rec.Code != http.StatusNotFound {
		t.Errorf("cancelling a run that is not going is a 404, got %d", rec.Code)
	}
}

// grain/task-304: the job cache from outside the daemon -- GET
// /api/ci/cache and the DELETE that empties it.

func TestCICacheSaysWhatItHolds(t *testing.T) {
	t.Parallel()
	swept := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	runner := &fakeCIRunner{cache: true, stats: ci.CacheStats{
		Dir:        "/var/lib/grain/data/state/ci-cache",
		Entries:    12,
		Bytes:      3 << 20,
		Oldest:     swept.Add(-48 * time.Hour),
		Newest:     swept,
		LastSweep:  swept,
		Hits:       7,
		Misses:     5,
		MaxAge:     30 * 24 * time.Hour,
		MaxBytes:   256 << 20,
		MaxEntries: 4096,
	}}
	srv, _ := ciServer(t, runner)

	got := decode[ui.CICacheResponse](t, do(t, srv, http.MethodGet, "/api/ci/cache", ""))
	if !got.Enabled || !got.Available || got.Cache == nil {
		t.Fatalf("a deployment with a cache should say so: %+v", got)
	}
	if got.Cache.Entries != 12 || got.Cache.Bytes != 3<<20 {
		t.Errorf("what it holds: %+v", got.Cache)
	}
	if got.Cache.Hits != 7 || got.Cache.Misses != 5 {
		t.Errorf("hits and misses: %+v", got.Cache)
	}
	// Seconds, not the nanoseconds a time.Duration would have arrived
	// as: the pane divides this into days.
	if got.Cache.MaxAgeSeconds != 30*24*60*60 {
		t.Errorf("maxAgeSeconds = %v", got.Cache.MaxAgeSeconds)
	}
	if got.Cache.Dir == "" || got.Cache.LastSweep.IsZero() {
		t.Errorf("an operator is told where to look and when it last swept: %+v", got.Cache)
	}
	if got.Removed != nil {
		t.Errorf("a read removed nothing and should say nothing about it: %v", *got.Removed)
	}
}

// Three states, and the two that are not the ordinary one are what a
// reader needs told apart: no CI at all, and CI with no cache behind it.
func TestCICacheSaysWhenThereIsNoneToSee(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	if got := decode[ui.CICacheResponse](t, do(t, srv, http.MethodGet, "/api/ci/cache", "")); got.Enabled || got.Available {
		t.Errorf("a deployment with no CI wired: %+v", got)
	}

	srv, _ = ciServer(t, &fakeCIRunner{})
	got := decode[ui.CICacheResponse](t, do(t, srv, http.MethodGet, "/api/ci/cache", ""))
	if !got.Enabled || got.Available || got.Cache != nil {
		t.Errorf("CI wired with no cache behind it: %+v", got)
	}
}

func TestClearCICacheIsAvailableForTheBuiltInConfigCI(t *testing.T) {
	t.Parallel()
	runner := &fakeCIRunner{cache: true, removes: 3}
	srv, store := ciServer(t, runner)
	_ = store.PutConfig(context.Background(), model.Config{PollInterval: model.DefaultPollInterval})

	// grain/config has mandatory CI on every deployment, so its jobs can
	// fill the shared cache without an operator enabling grain-ci for an
	// external repository first.
	got := decode[ui.CICacheResponse](t, do(t, srv, http.MethodDelete, "/api/ci/cache", ""))
	if runner.cleared != 1 {
		t.Fatalf("cleared %d times", runner.cleared)
	}
	if got.Removed == nil || *got.Removed != 3 {
		t.Errorf("a clear says how many entries it removed: %+v", got.Removed)
	}
	if got.Cache == nil || got.Cache.Entries != 0 {
		t.Errorf("the cache is read back after clearing it: %+v", got.Cache)
	}
}

func TestClearCICacheWithNoCacheBehindIt(t *testing.T) {
	t.Parallel()
	runner := &fakeCIRunner{}
	srv, store := ciServer(t, runner)
	_ = store.PutConfig(context.Background(), model.Config{PollInterval: model.DefaultPollInterval})
	enableCI(t, store, model.RepoRef{Owner: "acme", Name: "widgets"})

	// Not a 200 saying it cleared nothing: there was nothing here to
	// act on, and an action that did nothing should say so.
	if rec := do(t, srv, http.MethodDelete, "/api/ci/cache", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("clearing a cache that does not exist = %d, want 404: %s", rec.Code, rec.Body)
	}

	srv, client := testServer(t)
	storeSettingsRow(t, client)
	if rec := do(t, srv, http.MethodDelete, "/api/ci/cache", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("clearing on a deployment with no CI = %d, want 404: %s", rec.Code, rec.Body)
	}
}

// The route understands the whole filter vocabulary the tool does, and
// spells it the same way (ci.RunFilter, mcp.ciRunFilter): an operator
// following a link and an agent calling list_ci_runs have to be able to
// ask one question and compare two answers (grain/task-231).
func TestListCIRunsNarrowsByTheSharedVocabulary(t *testing.T) {
	t.Parallel()
	started := time.Now().Add(-30 * time.Minute)
	finished := started.Add(20 * time.Minute)
	old := time.Now().Add(-90 * time.Hour)
	queued := time.Now().Add(-10 * time.Minute)
	runner := &fakeCIRunner{runs: []ci.Run{
		{ID: "red", Repo: "acme/widgets", Ref: "main", Conclusion: ci.ConclusionFailure,
			StartedAt: &started, FinishedAt: &finished,
			Jobs: []ci.JobRun{{Name: "ui e2e", Conclusion: ci.ConclusionFailure, StartedAt: &started}}},
		{ID: "green", Repo: "acme/widgets", Ref: "grain/task-231", Conclusion: ci.ConclusionSuccess,
			StartedAt: &old, FinishedAt: &old},
		{ID: "live", Repo: "acme/gadgets", Ref: "main", StartedAt: &queued,
			Jobs: []ci.JobRun{{Name: "go tests", StartedAt: &queued}}},
	}}
	srv, _ := ciServer(t, runner)

	ids := func(query string) string {
		t.Helper()
		listed := decode[ui.CIRunsResponse](t, do(t, srv, http.MethodGet, "/api/ci/runs"+query, ""))
		out := make([]string, 0, len(listed.Runs))
		for _, run := range listed.Runs {
			out = append(out, run.ID)
		}
		return strings.Join(out, ",")
	}
	for _, tc := range []struct{ query, want string }{
		{"", "live,red,green"},
		{"?conclusion=failure", "red"},
		{"?conclusion=failure&conclusion=running", "live,red"},
		{"?conclusion=failure,running", "live,red"},
		{"?ref=task-231", "green"},
		{"?job=ui%20e2e", "red"},
		{"?since=24h", "live,red"},
		{"?repo=acme/widgets&conclusion=failure&job=ui%20e2e", "red"},
		{"?sort=started&order=asc", "green,red,live"},
	} {
		if got := ids(tc.query); got != tc.want {
			t.Errorf("GET /api/ci/runs%s listed %q, want %q", tc.query, got, tc.want)
		}
	}
	// A word the vocabulary does not have is a 400 rather than an empty
	// table: a filter that is quietly ignored answers with everything,
	// and reads as a fact about CI rather than as a typo.
	if rec := do(t, srv, http.MethodGet, "/api/ci/runs?conclusion=failed", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("an unknown conclusion is a 400, got %d: %s", rec.Code, rec.Body)
	}
}

// The listing is a page of an answer rather than the answer: ?limit=
// says how much of it to send, and every response says how many runs
// matched and how many the deployment holds, so a reader can tell a run
// that was cut from one that never matched and from one that is no
// longer kept (ci.RunPage, grain/task-289).
func TestListCIRunsIsAPageThatSaysWhatItLeftOut(t *testing.T) {
	t.Parallel()
	started := time.Now().Add(-30 * time.Minute)
	runner := &fakeCIRunner{runs: []ci.Run{
		{ID: "red", Repo: "acme/widgets", Ref: "main", Conclusion: ci.ConclusionFailure, StartedAt: &started},
		{ID: "green", Repo: "acme/widgets", Ref: "main", Conclusion: ci.ConclusionSuccess,
			StartedAt: timeAt(started.Add(-time.Hour))},
		{ID: "other", Repo: "acme/gadgets", Ref: "main", Conclusion: ci.ConclusionSuccess,
			StartedAt: timeAt(started.Add(-2 * time.Hour))},
	}}
	srv, _ := ciServer(t, runner)

	page := decode[ui.CIRunsResponse](t, do(t, srv, http.MethodGet, "/api/ci/runs?repo=acme/widgets&limit=1", ""))
	if len(page.Runs) != 1 || page.Runs[0].ID != "red" {
		t.Errorf("a page of one held %+v", page.Runs)
	}
	if page.Matched != 2 || page.Stored != 3 {
		t.Errorf("the page said %d of %d runs matched, want 2 of 3", page.Matched, page.Stored)
	}
	whole := decode[ui.CIRunsResponse](t, do(t, srv, http.MethodGet, "/api/ci/runs", ""))
	if len(whole.Runs) != 3 || whole.Matched != 3 || whole.Stored != 3 {
		t.Errorf("an unnarrowed listing said %d runs, %d matched, %d stored", len(whole.Runs), whole.Matched, whole.Stored)
	}
	// A limit that is not a limit is refused rather than ignored, for
	// the reason an unknown filter word is.
	for _, query := range []string{"?limit=lots", "?limit=-3"} {
		if rec := do(t, srv, http.MethodGet, "/api/ci/runs"+query, ""); rec.Code != http.StatusBadRequest {
			t.Errorf("GET /api/ci/runs%s = %d, want 400: %s", query, rec.Code, rec.Body)
		}
	}
}

func timeAt(t time.Time) *time.Time { return &t }

// The filter travels to the daemon rather than being applied to what it
// answered with, which is the whole of what list_ci_runs gains from this
// (cmd/grain's daemonGrainCI, grain/task-289): a narrowed question over
// a real round trip, and the page's own counts coming back with it.
func TestHTTPClientFindsCIRunsByAskingTheDaemon(t *testing.T) {
	t.Parallel()
	started := time.Now().Add(-20 * time.Minute)
	runner := &fakeCIRunner{runs: []ci.Run{
		{ID: "red", Repo: "acme/widgets", Ref: "main", Conclusion: ci.ConclusionFailure,
			StartedAt: &started, FinishedAt: timeAt(started.Add(time.Minute)),
			Jobs: []ci.JobRun{{Name: "ui e2e", Conclusion: ci.ConclusionFailure}}},
		{ID: "older-red", Repo: "acme/widgets", Ref: "grain/task-289", Conclusion: ci.ConclusionFailure,
			StartedAt: timeAt(started.Add(-time.Hour)), FinishedAt: timeAt(started.Add(-time.Hour)),
			Jobs: []ci.JobRun{{Name: "ui e2e", Conclusion: ci.ConclusionFailure}}},
		{ID: "green", Repo: "acme/widgets", Ref: "main", Conclusion: ci.ConclusionSuccess,
			StartedAt: timeAt(started.Add(-2 * time.Hour))},
	}}
	srv, _ := ciServer(t, runner)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	page, err := serverAccessClient(srv, ts.URL).FindCIRuns(context.Background(), ci.RunFilter{
		Repo:       "acme/widgets",
		Job:        "ui e2e",
		States:     []string{string(ci.ConclusionFailure)},
		Since:      started.Add(-2 * time.Hour),
		Descending: true,
	}, 1)
	if err != nil {
		t.Fatalf("FindCIRuns: %v", err)
	}
	if len(page.Runs) != 1 || page.Runs[0].ID != "red" {
		t.Fatalf("the narrowed page is %+v", page.Runs)
	}
	if page.Matched != 2 || page.Stored != 3 {
		t.Errorf("the page said %d of %d runs matched, want 2 of 3", page.Matched, page.Stored)
	}
}

// A task-scoped start that cannot go ahead says why, with the status that
// fits: 403 for a task without grain-ci, 400 for a task with no target or a
// request Start refuses. Each used to be a 500 whose reason apierror.Body
// keeps to the daemon's log, which left the agent asking with nothing to go
// on.
func TestTaskScopedCIRunRefusalsSayWhy(t *testing.T) {
	t.Parallel()
	runner := &fakeCIRunner{err: errors.New(`branch "grain/task-9" does not resolve`)}
	srv, store := ciServer(t, runner)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	for _, task := range []model.Task{
		{ID: "8", Intent: model.IntentImplement, Title: "no grant", Target: &repo},
		{ID: "9", Intent: model.IntentImplement, Title: "unpushed", Target: &repo,
			Grants: []model.Grant{{Capability: "grain-ci"}}},
		{ID: "10", Intent: model.IntentImplement, Title: "no repo",
			Grants: []model.Grant{{Capability: "grain-ci"}}},
	} {
		if err := store.PutTask(context.Background(), task); err != nil {
			t.Fatal(err)
		}
	}

	for _, tc := range []struct {
		task string
		code int
		says string
	}{
		{"8", http.StatusForbidden, "does not hold"},
		{"9", http.StatusBadRequest, "does not resolve"},
		{"10", http.StatusBadRequest, "no target repository"},
	} {
		rec := do(t, srv, http.MethodPost, "/api/ci/runs", `{"taskId":"`+tc.task+`"}`)
		if rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.says) {
			t.Errorf("task %s: %d %s, want %d saying %q", tc.task, rec.Code, rec.Body, tc.code, tc.says)
		}
	}
}

func TestCloseCancelsInFlightTaskCIRuns(t *testing.T) {
	t.Parallel()
	now := time.Unix(100, 0).UTC()
	runner := &fakeCIRunner{
		cancelOK: true,
		runs: []ci.Run{
			{ID: "run-recorded", Repo: "acme/widgets", Ref: "grain/task-11", RequestedBy: "11"},
			{ID: "run-selective", Repo: "acme/widgets", Ref: "grain/task-11", RequestedBy: "11"},
			{ID: "run-done", Repo: "acme/widgets", Ref: "grain/task-11", RequestedBy: "11", Conclusion: ci.ConclusionSuccess},
			{ID: "run-other", Repo: "acme/widgets", Ref: "grain/task-12", RequestedBy: "12"},
			{ID: "run-merged", Repo: "acme/widgets", Ref: "grain/task-13", RequestedBy: "13"},
		},
	}
	client, store, _ := testClient(t)
	client.Config.CI = runner
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	ctx := context.Background()

	for _, id := range []string{"11", "12", "13"} {
		if err := store.PutTask(ctx, model.Task{
			ID: id, Intent: model.IntentImplement, Title: "task " + id, Target: &repo,
			Grants: []model.Grant{{Capability: "grain-ci"}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ObserveField(ctx, "11", now, func(o *model.Observation) {
		o.CI = &model.CIStatus{RunID: "run-recorded", Commit: "abcdef", StartedAt: &now}
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.ObserveField(ctx, "13", now, func(o *model.Observation) {
		o.PrMergedAt = &now
		o.CI = &model.CIStatus{RunID: "run-merged", Commit: "abcdef", StartedAt: &now}
	}); err != nil {
		t.Fatal(err)
	}

	if err := client.Close(ctx, "11", ui.CloseOptions{}); err != nil {
		t.Fatalf("Close(11): %v", err)
	}
	if got := strings.Join(runner.cancelled, ","); got != "run-recorded,run-selective" {
		t.Fatalf("cancelled = %v, want [run-recorded run-selective]", runner.cancelled)
	}
	for i, reason := range runner.reasons {
		if reason != "task 11 was closed" {
			t.Errorf("reason[%d] = %q, want %q", i, reason, "task 11 was closed")
		}
	}
	for i, source := range runner.sources {
		if source != ci.CancelByOperator {
			t.Errorf("source[%d] = %q, want %q", i, source, ci.CancelByOperator)
		}
	}

	// Closing a merged task does not cancel its CI runs.
	if err := client.Close(ctx, "13", ui.CloseOptions{}); err != nil {
		t.Fatalf("Close(13): %v", err)
	}
	if got := strings.Join(runner.cancelled, ","); got != "run-recorded,run-selective" {
		t.Fatalf("cancelled after closing merged task = %v, want no new cancellations", runner.cancelled)
	}
}

func TestTaskScopedCIRunRetargetsStackedTaskBaseWhenUpstreamTaskMerged(t *testing.T) {
	t.Parallel()
	now := time.Unix(100, 0).UTC()
	runner := &fakeCIRunner{
		run: ci.Run{ID: "run-stacked", Repo: "bwsalmon/grain", Ref: "grain/049f1e09/task-755", Commit: "abcdef", StartedAt: &now},
	}
	srv, store := ciServer(t, runner)
	repo := model.RepoRef{Owner: "bwsalmon", Name: "grain"}
	ctx := context.Background()

	if err := store.PutTask(ctx, model.Task{
		ID:     "732",
		Branch: "grain/049f1e09/task-732",
		Intent: model.IntentImplement,
		Title:  "upstream feature",
		Target: &repo,
		Base:   "candidate",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.ObserveField(ctx, "732", now, func(o *model.Observation) {
		o.PrMergedAt = &now
		o.ClosedAt = &now
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTask(ctx, model.Task{
		ID:     "755",
		Branch: "grain/049f1e09/task-755",
		Intent: model.IntentImplement,
		Title:  "stacked review",
		Target: &repo,
		Base:   "grain/049f1e09/task-732",
		Grants: []model.Grant{{Capability: "grain-ci"}},
	}); err != nil {
		t.Fatal(err)
	}

	rec := do(t, srv, http.MethodPost, "/api/ci/runs", `{"taskId":"755"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body)
	}
	if len(runner.started) != 1 {
		t.Fatalf("started %d runs, want 1", len(runner.started))
	}
	if req := runner.started[0]; !req.GateOnBase || req.BaseRef != "candidate" {
		t.Fatalf("CI request = %+v, want GateOnBase=true and BaseRef=candidate", req)
	}
	updated, err := store.GetTask(ctx, "755")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Base != "candidate" {
		t.Fatalf("stacked task Base = %q, want candidate", updated.Base)
	}
}

// A review retargeted off the branch it reviews loses AutoMerge, as the
// orchestrator's retarget does: it is never classified on its own account.
func TestTaskScopedCIRunRetargetedReviewLosesAutoMerge(t *testing.T) {
	t.Parallel()
	now := time.Unix(100, 0).UTC()
	runner := &fakeCIRunner{
		run: ci.Run{ID: "run-review", Repo: "bwsalmon/grain", Ref: "grain/049f1e09/task-755", Commit: "abcdef", StartedAt: &now},
	}
	srv, store := ciServer(t, runner)
	repo := model.RepoRef{Owner: "bwsalmon", Name: "grain"}
	ctx := context.Background()

	if err := store.PutTask(ctx, model.Task{
		ID:     "732",
		Branch: "grain/049f1e09/task-732",
		Intent: model.IntentImplement,
		Title:  "upstream feature",
		Target: &repo,
		Base:   "candidate",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.ObserveField(ctx, "732", now, func(o *model.Observation) {
		o.PrMergedAt = &now
		o.ClosedAt = &now
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTask(ctx, model.Task{
		ID:        "755",
		Branch:    "grain/049f1e09/task-755",
		Intent:    model.IntentImplement,
		Title:     "security review",
		Target:    &repo,
		Base:      "grain/049f1e09/task-732",
		AutoMerge: true,
		Origin:    model.Origin{Reason: model.ReasonReview},
		Grants:    []model.Grant{{Capability: "grain-ci"}},
	}); err != nil {
		t.Fatal(err)
	}

	rec := do(t, srv, http.MethodPost, "/api/ci/runs", `{"taskId":"755"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body)
	}
	updated, err := store.GetTask(ctx, "755")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Base != "candidate" {
		t.Fatalf("review Base = %q, want candidate", updated.Base)
	}
	if updated.AutoMerge {
		t.Fatal("review retargeted onto candidate kept AutoMerge; want it cleared")
	}
}

func TestStartTaskCIRunOptRefusesFullSuiteWhenRepoUsesGitHubCIMode(t *testing.T) {
	t.Parallel()
	runner := &fakeCIRunner{
		run: ci.Run{ID: "run-1", Repo: "acme/widgets", Ref: "grain/task-21"},
	}
	srv, store := ciServer(t, runner)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	ctx := context.Background()

	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:   repo,
		CIMode: model.CIModeGitHub,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTask(ctx, model.Task{
		ID:     "21",
		Branch: "grain/task-21",
		Intent: model.IntentImplement,
		Title:  "pre-existing task with default grant",
		Target: &repo,
		Base:   "main",
		Grants: []model.Grant{{Capability: "grain-ci", Via: model.GrantByDefault}},
	}); err != nil {
		t.Fatal(err)
	}

	rec := do(t, srv, http.MethodPost, "/api/ci/runs", `{"taskId":"21"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST /api/ci/runs on CIModeGitHub repo: status = %d, want 403: %s", rec.Code, rec.Body)
	}
	if len(runner.started) != 0 {
		t.Fatalf("started %d Grain CI runs on CIModeGitHub repo, want 0", len(runner.started))
	}
}
