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

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// fakeActions is a forge with one repository's workflow runs, jobs,
// steps and a log, counting what it was asked.
type fakeActions struct {
	mu    sync.Mutex
	calls map[string]int
	// owners is every owner argument a call carried: host/owner for a
	// repository off the default host, which is how the daemon's
	// forgeRouter is told which forge to ask.
	owners []string
	runs   []github.WorkflowRun
	jobs   map[int64][]github.ActionsRunJob
	logs   map[int64]string
}

func (f *fakeActions) note(call, owner string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[call]++
	f.owners = append(f.owners, owner)
}

func (f *fakeActions) count(call string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[call]
}

func (f *fakeActions) ListActionsRuns(owner, repo string, q github.ActionsRunsQuery) ([]github.WorkflowRun, error) {
	f.note("runs", owner)
	var out []github.WorkflowRun
	for _, r := range f.runs {
		if q.Branch == "" || r.HeadBranch == q.Branch {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeActions) GetActionsRun(owner, repo string, runID int64) (*github.WorkflowRun, error) {
	f.note("run", owner)
	for _, r := range f.runs {
		if r.ID == runID {
			return &r, nil
		}
	}
	return nil, nil
}

func (f *fakeActions) ListActionsJobs(owner, repo string, runID int64) ([]github.ActionsRunJob, error) {
	f.note("jobs", owner)
	return f.jobs[runID], nil
}

func (f *fakeActions) ActionsJobLog(owner, repo string, jobID int64) (string, error) {
	f.note("log", owner)
	text, ok := f.logs[jobID]
	if !ok {
		return "", &github.Error{Status: http.StatusGone}
	}
	return text, nil
}

func (f *fakeActions) ListActionsArtifacts(owner, repo string, runID int64) ([]github.ActionsArtifact, error) {
	f.note("artifacts", owner)
	return []github.ActionsArtifact{{ID: 1, Name: "dist", Bytes: 99}}, nil
}

func newFakeActions(now time.Time) *fakeActions {
	started := now.Add(-10 * time.Minute)
	finished := now.Add(-5 * time.Minute)
	live := now.Add(-time.Minute)
	return &fakeActions{
		runs: []github.WorkflowRun{
			{ID: 42, Name: "Release", Path: ".github/workflows/release.yml", HeadBranch: "main", HeadSHA: "abc",
				Event: "push", Status: "completed", Conclusion: "failure", RunAttempt: 1,
				HTMLURL: "https://github.com/acme/widgets/actions/runs/42", StartedAt: &started, UpdatedAt: &finished},
			{ID: 43, Name: "Tests", Path: ".github/workflows/tests.yml", HeadBranch: "grain/task-9", HeadSHA: "def",
				Event: "pull_request", Status: "in_progress", RunAttempt: 1, StartedAt: &live},
		},
		jobs: map[int64][]github.ActionsRunJob{
			42: {
				{ID: 700, RunID: 42, Name: "build", Status: "completed", Conclusion: "success",
					Steps: []github.ActionsRunStep{{Number: 1, Name: "checkout", Status: "completed", Conclusion: "success"}}},
				{ID: 701, RunID: 42, Name: "publish", Status: "completed", Conclusion: "failure",
					Steps: []github.ActionsRunStep{
						{Number: 1, Name: "login", Status: "completed", Conclusion: "success"},
						{Number: 2, Name: "push", Status: "completed", Conclusion: "failure"},
					}},
			},
		},
		logs: map[int64]string{
			700: "built\n",
			701: "a\nb\nc\nerror: GHCR_TOKEN is not set\n",
		},
	}
}

// actionsServer is a deployment whose acme/widgets is gated by GitHub
// Actions, with one older grain CI run of it beside, and two tasks: one
// that may read acme/widgets' CI and one that may read only another
// repository's.
func actionsServer(t *testing.T, forge *fakeActions, now time.Time) (*ui.Server, *ui.Client, *model.Store) {
	t.Helper()
	grainStarted := now.Add(-time.Hour)
	runner := &fakeCIRunner{runs: []ci.Run{{ID: "run-g1", Repo: "acme/widgets", Ref: "main",
		Conclusion: ci.ConclusionSuccess, StartedAt: &grainStarted, FinishedAt: &grainStarted}}}
	client, store, _ := testClient(t)
	client.Config.CI = runner
	if forge != nil {
		client.Config.Actions = forge
	}
	ctx := context.Background()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CIMode: model.CIModeGitHub}); err != nil {
		t.Fatal(err)
	}
	seedAPITask(t, client, "reader", "acme/widgets", "api:ci:read@acme/widgets")
	seedAPITask(t, client, "outsider", "acme/other", "api:ci:read@acme/other")
	return ui.NewServerWithClient(client), client, store
}

func TestCIRunsListMergesGrainAndActionsRows(t *testing.T) {
	t.Parallel()
	now := time.Now()
	forge := newFakeActions(now)
	srv, _, _ := actionsServer(t, forge, now)

	code, body := asAgent(t, srv, "reader", http.MethodGet, "/api/ci/runs?repo=acme/widgets", "", "")
	if code != http.StatusOK {
		t.Fatalf("list: %d %s", code, body)
	}
	got := decodeString[ui.CIRunsResponse](t, body)
	var ids, providers []string
	for _, run := range got.Runs {
		ids = append(ids, run.ID)
		providers = append(providers, run.Provider)
	}
	wantIDs := []string{"gha:acme:widgets:43", "gha:acme:widgets:42", "run-g1"}
	if strings.Join(ids, ",") != strings.Join(wantIDs, ",") {
		t.Fatalf("ids = %v, want %v (newest first, both providers)", ids, wantIDs)
	}
	if strings.Join(providers, ",") != "github-actions,github-actions,grain" {
		t.Errorf("providers = %v", providers)
	}
	release := got.Runs[1]
	if release.Actions == nil || release.Actions.Workflow != ".github/workflows/release.yml" || release.Actions.WorkflowName != "Release" ||
		release.Actions.RunID != 42 || release.Conclusion != ci.ConclusionFailure || release.Commit != "abc" || release.Event != "push" ||
		release.Actions.URL == "" || release.FinishedAt == nil {
		t.Errorf("release row = %+v / %+v", release, release.Actions)
	}
	if ci.RunStateOf(got.Runs[0]) != ci.StateRunning {
		t.Errorf("an in-progress Actions run reads %q, want running", ci.RunStateOf(got.Runs[0]))
	}

	for provider, want := range map[string]string{
		"grain":          "run-g1",
		"github-actions": "gha:acme:widgets:43,gha:acme:widgets:42",
	} {
		code, body := asAgent(t, srv, "reader", http.MethodGet, "/api/ci/runs?repo=acme/widgets&provider="+provider, "", "")
		if code != http.StatusOK {
			t.Fatalf("provider=%s: %d %s", provider, code, body)
		}
		var ids []string
		for _, run := range decodeString[ui.CIRunsResponse](t, body).Runs {
			ids = append(ids, run.ID)
		}
		if strings.Join(ids, ",") != want {
			t.Errorf("provider=%s ids = %v, want %s", provider, ids, want)
		}
	}

	code, body = asAgent(t, srv, "reader", http.MethodGet, "/api/ci/runs?repo=acme/widgets&ref=main&conclusion=failure", "", "")
	if code != http.StatusOK {
		t.Fatalf("filtered: %d %s", code, body)
	}
	if runs := decodeString[ui.CIRunsResponse](t, body).Runs; len(runs) != 1 || runs[0].ID != "gha:acme:widgets:42" {
		t.Errorf("ref=main&conclusion=failure = %+v", runs)
	}

	if code, body := asAgent(t, srv, "reader", http.MethodGet, "/api/ci/runs?repo=acme/widgets&provider=jenkins", "", ""); code != http.StatusBadRequest {
		t.Errorf("an unknown provider answered %d %s, want 400", code, body)
	}

	// The CI pane -- a person, naming no provider -- sees grain's alone.
	rec := do(t, srv, http.MethodGet, "/api/ci/runs?repo=acme/widgets", "")
	if runs := decode[ui.CIRunsResponse](t, rec).Runs; len(runs) != 1 || runs[0].ID != "run-g1" {
		t.Errorf("a person's listing = %+v, want grain's run alone", runs)
	}
}

func TestCIRunsListCachesTheForgeListing(t *testing.T) {
	t.Parallel()
	now := time.Now()
	forge := newFakeActions(now)
	srv, _, _ := actionsServer(t, forge, now)
	for i := 0; i < 3; i++ {
		if code, body := asAgent(t, srv, "reader", http.MethodGet, "/api/ci/runs?repo=acme/widgets&provider=github-actions", "", ""); code != http.StatusOK {
			t.Fatalf("list: %d %s", code, body)
		}
	}
	if n := forge.count("runs"); n != 1 {
		t.Errorf("three listings asked the forge %d times, want once", n)
	}
}

func TestCIRunShowAndLogForAnActionsRun(t *testing.T) {
	t.Parallel()
	now := time.Now()
	forge := newFakeActions(now)
	srv, _, _ := actionsServer(t, forge, now)

	code, body := asAgent(t, srv, "reader", http.MethodGet, "/api/ci/runs/gha:acme:widgets:42", "", "")
	if code != http.StatusOK {
		t.Fatalf("show: %d %s", code, body)
	}
	run := decodeString[ci.Run](t, body)
	if run.Provider != ci.ProviderGitHubActions || len(run.Jobs) != 2 {
		t.Fatalf("show = %+v", run)
	}
	publish := run.Jobs[1]
	if publish.Name != "publish" || publish.JobID != "701" || publish.Conclusion != ci.ConclusionFailure ||
		len(publish.Steps) != 2 || publish.Steps[1].Name != "push" || publish.Steps[1].Conclusion != ci.ConclusionFailure {
		t.Errorf("publish = %+v", publish)
	}
	if strings.Join(run.Actions.FailedJobs, ",") != "publish" {
		t.Errorf("failed jobs = %v", run.Actions.FailedJobs)
	}

	// No job named: the one that failed.
	code, body = asAgent(t, srv, "reader", http.MethodGet, "/api/ci/runs/gha:acme:widgets:42/log", "", "")
	if code != http.StatusOK {
		t.Fatalf("log: %d %s", code, body)
	}
	if got := decodeString[ui.CIRunLogResponse](t, body); got.Job != "publish" || got.JobID != 701 || !strings.Contains(got.Log, "GHCR_TOKEN") {
		t.Errorf("default log = %+v", got)
	}
	code, body = asAgent(t, srv, "reader", http.MethodGet, "/api/ci/runs/gha:acme:widgets:42/log?job=build", "", "")
	if got := decodeString[ui.CIRunLogResponse](t, body); code != http.StatusOK || got.Job != "build" || got.Log != "built\n" {
		t.Errorf("log of build = %d %+v", code, got)
	}
	code, body = asAgent(t, srv, "reader", http.MethodGet, "/api/ci/runs/gha:acme:widgets:42/log?job=701&tail=2", "", "")
	if got := decodeString[ui.CIRunLogResponse](t, body); code != http.StatusOK || got.Log != "c\nerror: GHCR_TOKEN is not set\n" || !got.Truncated {
		t.Errorf("tail=2 = %d %+v", code, got)
	}
	if code, body := asAgent(t, srv, "reader", http.MethodGet, "/api/ci/runs/gha:acme:widgets:42/log?job=deploy", "", ""); code != http.StatusNotFound ||
		!strings.Contains(body, "publish") {
		t.Errorf("an unknown job answered %d %s, want 404 naming the jobs", code, body)
	}

	code, body = asAgent(t, srv, "reader", http.MethodGet, "/api/ci/runs/gha:acme:widgets:42/artifacts", "", "")
	if arts := decodeString[ui.CIRunArtifactsResponse](t, body).Artifacts; code != http.StatusOK || len(arts) != 1 || arts[0].Name != "dist" ||
		!strings.Contains(arts[0].Note, "listed only") {
		t.Errorf("artifacts = %d %s", code, body)
	}
	if code, _ := asAgent(t, srv, "reader", http.MethodGet, "/api/ci/runs/gha:acme:widgets:42/artifacts/build/dist", "", ""); code != http.StatusNotImplemented {
		t.Errorf("an Actions artifact download answered %d, want 501", code)
	}

	// A finished run's jobs and a finished job's log are read once.
	if forge.count("jobs") != 1 || forge.count("run") != 1 || forge.count("log") != 2 {
		t.Errorf("forge calls = %v, want one run, one jobs and a log per job", forge.calls)
	}
}

func TestCIReadOfActionsRunsKeepsToTheTasksReadScope(t *testing.T) {
	t.Parallel()
	now := time.Now()
	forge := newFakeActions(now)
	srv, _, _ := actionsServer(t, forge, now)
	for _, path := range []string{
		"/api/ci/runs?repo=acme/widgets",
		"/api/ci/runs/gha:acme:widgets:42",
		"/api/ci/runs/gha:acme:widgets:42/log",
		"/api/ci/runs/gha:acme:widgets:42/artifacts",
	} {
		if code, body := asAgent(t, srv, "outsider", http.MethodGet, path, "", ""); code != http.StatusForbidden {
			t.Errorf("a task scoped to acme/other read %s: %d %s", path, code, body)
		}
	}
	if n := forge.count("runs") + forge.count("run") + forge.count("log"); n != 0 {
		t.Errorf("a refused read still asked the forge %d times", n)
	}
}

// TestCIReadOfActionsRunsKeepsToTheDeploymentsRepos is the bound on a
// grant for every repository, and on a person: the repository of a gha:
// id is text the caller wrote, and the forge is read with the daemon's
// own credential, so a repository this deployment does not work in is
// refused before the forge is asked anything about it.
func TestCIReadOfActionsRunsKeepsToTheDeploymentsRepos(t *testing.T) {
	t.Parallel()
	now := time.Now()
	forge := newFakeActions(now)
	srv, client, store := actionsServer(t, forge, now)
	if err := store.SetTargetRepos(context.Background(), []string{"acme/widgets"}); err != nil {
		t.Fatal(err)
	}
	seedAPITask(t, client, "everywhere", "acme/widgets", "api:ci:read")
	paths := []string{
		"/api/ci/runs/gha:elsewhere:secrets:42",
		"/api/ci/runs/gha:elsewhere:secrets:42/log",
		"/api/ci/runs/gha:elsewhere:secrets:42/artifacts",
	}
	for _, path := range paths {
		if code, body := asAgent(t, srv, "everywhere", http.MethodGet, path, "", ""); code == http.StatusOK {
			t.Errorf("a task granted ci:read everywhere read %s of a repository off the deployment's list: %s", path, body)
		}
		if rec := do(t, srv, http.MethodGet, path, ""); rec.Code == http.StatusOK {
			t.Errorf("a person read %s of a repository off the deployment's list: %s", path, rec.Body.String())
		}
	}
	code, body := asAgent(t, srv, "everywhere", http.MethodGet, "/api/ci/runs?repo=elsewhere/secrets&provider=github-actions", "", "")
	if runs := decodeString[ui.CIRunsResponse](t, body).Runs; code != http.StatusOK || len(runs) != 0 {
		t.Errorf("listing a repository off the deployment's list = %d %s, want no runs", code, body)
	}
	if n := forge.count("runs") + forge.count("run") + forge.count("jobs") + forge.count("log") + forge.count("artifacts"); n != 0 {
		t.Errorf("the forge was asked %d times about a repository this deployment does not work in", n)
	}
	// The deployment's own repository still reads.
	if code, body := asAgent(t, srv, "everywhere", http.MethodGet, "/api/ci/runs/gha:acme:widgets:42", "", ""); code != http.StatusOK {
		t.Errorf("show of acme/widgets: %d %s", code, body)
	}
}

func TestCIRunsListFallsBackToGrainsRecordOfActions(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Second)
	srv, _, store := actionsServer(t, nil, now)
	done := now.Add(-2 * time.Minute)
	for _, j := range []model.GitHubCIJob{
		{Repo: "acme/widgets", CheckRunID: 1, App: model.GitHubActionsApp, Ref: "main", Commit: "abc", Workflow: "Release",
			RunID: 42, RunAttempt: 1, JobName: "publish", Conclusion: "failure", CompletedAt: &done, RunURL: "https://github.com/acme/widgets/actions/runs/42"},
		{Repo: "acme/widgets", CheckRunID: 2, App: model.GitHubActionsApp, Ref: "main", Commit: "abc", Workflow: "Release",
			RunID: 42, RunAttempt: 1, JobName: "build", Conclusion: "success", CompletedAt: &done},
	} {
		if err := store.PutGitHubCIJob(context.Background(), j); err != nil {
			t.Fatal(err)
		}
	}
	code, body := asAgent(t, srv, "reader", http.MethodGet, "/api/ci/runs?repo=acme/widgets&provider=github-actions", "", "")
	if code != http.StatusOK {
		t.Fatalf("list: %d %s", code, body)
	}
	got := decodeString[ui.CIRunsResponse](t, body)
	if len(got.Runs) != 1 || got.Runs[0].ID != "gha:acme:widgets:42" || got.Runs[0].Actions == nil ||
		strings.Join(got.Runs[0].Actions.FailedJobs, ",") != "publish" || got.Runs[0].Conclusion != ci.ConclusionFailure {
		t.Fatalf("recorded rows = %+v", got.Runs)
	}
	if got.ActionsNote == "" {
		t.Errorf("a listing from grain's record alone should say why")
	}
	code, body = asAgent(t, srv, "reader", http.MethodGet, "/api/ci/runs/gha:acme:widgets:42", "", "")
	if run := decodeString[ci.Run](t, body); code != http.StatusOK || len(run.Jobs) != 2 {
		t.Errorf("show from the record = %d %s", code, body)
	}
}

func TestActionsOffTheDefaultHostAreAskedWithTheHostInTheOwner(t *testing.T) {
	t.Parallel()
	now := time.Now()
	forge := newFakeActions(now)
	srv, client, store := actionsServer(t, forge, now)
	repo := model.RepoRef{Host: "depot.code.example", Owner: "experimental", Name: "grain"}
	if err := store.PutRepoConfig(context.Background(), model.RepoConfig{Repo: repo, CIMode: model.CIModeGitHub}); err != nil {
		t.Fatal(err)
	}
	seedAPITask(t, client, "ghe-reader", "depot.code.example/experimental/grain", "api:ci:read")
	code, body := asAgent(t, srv, "ghe-reader", http.MethodGet, "/api/ci/runs/gha:depot.code.example:experimental:grain:42", "", "")
	if code != http.StatusOK {
		t.Fatalf("show: %d %s", code, body)
	}
	if run := decodeString[ci.Run](t, body); run.Repo != "depot.code.example/experimental/grain" || run.ID != "gha:depot.code.example:experimental:grain:42" {
		t.Errorf("run = %s in %s", run.ID, run.Repo)
	}
	for _, owner := range forge.owners {
		if owner != "depot.code.example/experimental" {
			t.Errorf("the forge was asked with owner %q, want host/owner", owner)
		}
	}
}

func TestParseActionsRunID(t *testing.T) {
	t.Parallel()
	for id, want := range map[string]string{
		"gha:acme:widgets:42":                     "acme/widgets#42",
		"gha:acme/widgets:42":                     "acme/widgets#42",
		"gha:depot.code.example:experimental:g:7": "depot.code.example/experimental/g#7",
	} {
		repo, n, ok := ui.ParseActionsRunID(id)
		if !ok || repo+"#"+itoa(n) != want {
			t.Errorf("ParseActionsRunID(%q) = %q %d %v, want %s", id, repo, n, ok, want)
		}
	}
	for _, id := range []string{"run-1", "gha:", "gha:widgets:42", "gha:acme:widgets:x", "gha:acme:widgets:0"} {
		if _, _, ok := ui.ParseActionsRunID(id); ok {
			t.Errorf("ParseActionsRunID(%q) parsed", id)
		}
	}
	if got := ui.ActionsRunID("acme/widgets", 42); got != "gha:acme:widgets:42" {
		t.Errorf("ActionsRunID = %q", got)
	}
}

func decodeString[T any](t *testing.T, body string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("decoding %q: %v", body, err)
	}
	return v
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
