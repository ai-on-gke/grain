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
	"fmt"
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
	total  int
	jobs   map[int64][]github.ActionsRunJob
	logs   map[int64]string
	// notFound makes it a forge that answers every Actions read with a
	// 404, as one with Actions off or unreadable to the credential does.
	notFound bool
	// logErr, when set, is every job log's answer.
	logErr error
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
	page, err := f.ListActionsRunsPage(owner, repo, q)
	return page.Runs, err
}

func (f *fakeActions) ListActionsRunsPage(owner, repo string, q github.ActionsRunsQuery) (github.ActionsRunsPage, error) {
	f.note("runs", owner)
	if f.notFound {
		return github.ActionsRunsPage{NotFound: true}, nil
	}
	var out []github.WorkflowRun
	wfFiltered := false
	w := strings.TrimSpace(q.Workflow)
	if w != "" && !strings.Contains(w, " ") {
		wfFiltered = true
	}
	for _, r := range f.runs {
		if q.Branch != "" && r.HeadBranch != q.Branch {
			continue
		}
		if wfFiltered && !(ci.RunFilter{Workflow: w}).Matches(ci.Run{Actions: &ci.ActionsRun{Workflow: r.Path, WorkflowName: r.Name}}) {
			continue
		}
		out = append(out, r)
	}
	total := len(out)
	if !wfFiltered && q.Branch == "" && f.total > total {
		total = f.total
	}
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return github.ActionsRunsPage{Runs: out, TotalCount: total, WorkflowFiltered: wfFiltered || w == ""}, nil
}

func (f *fakeActions) GetActionsRun(owner, repo string, runID int64) (*github.WorkflowRun, error) {
	f.note("run", owner)
	if f.notFound {
		return nil, nil
	}
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
	if f.logErr != nil {
		return "", f.logErr
	}
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
	if forge == nil {
		return actionsServerOver(t, nil, now)
	}
	return actionsServerOver(t, forge, now)
}

// actionsServerOver is actionsServer over any Actions reader, such as a
// github.RESTClient in front of githubsim; nil leaves the client's own.
func actionsServerOver(t *testing.T, forge github.ActionsReader, now time.Time) (*ui.Server, *ui.Client, *model.Store) {
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

// TestActionsOffTheDefaultHostListFromPolledChecks is a repository on
// another git host whose forge answers its Actions listing with a 404 and
// whose jobs grain heard of only by polling its pull requests' checks --
// which carry no run id, only each job's page. The runs are still listed
// and shown, from that record, and the listing says the forge 404ed.
func TestActionsOffTheDefaultHostListFromPolledChecks(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Second)
	forge := newFakeActions(now)
	forge.notFound = true
	_, client, store := actionsServer(t, forge, now)
	ctx := context.Background()
	repo := model.RepoRef{Host: "ghe.example.com", Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CIMode: model.CIModeGitHub}); err != nil {
		t.Fatal(err)
	}
	done := now.Add(-2 * time.Minute)
	for _, j := range []model.GitHubCIJob{
		{Repo: repo.String(), CheckRunID: 9001, App: model.GitHubActionsApp, Ref: "grain/task-9", Commit: "abc",
			JobName: "go build, vet and test", Conclusion: "failure", CompletedAt: &done, Via: "poll",
			JobURL: "https://ghe.example.com/acme/widgets/actions/runs/77/job/9001"},
		{Repo: repo.String(), CheckRunID: 9002, App: model.GitHubActionsApp, Ref: "grain/task-9", Commit: "abc",
			JobName: "changes", Conclusion: "success", CompletedAt: &done, Via: "poll",
			JobURL: "https://ghe.example.com/acme/widgets/actions/runs/77/job/9002"},
		{Repo: repo.String(), CheckRunID: 9003, App: "semgrep", Ref: "grain/task-9", Commit: "abc",
			JobName: "scan-pr", Conclusion: "success", CompletedAt: &done, Via: "poll", JobURL: "https://semgrep.example/scan/1"},
	} {
		if err := store.PutGitHubCIJob(ctx, j); err != nil {
			t.Fatal(err)
		}
	}
	if j, err := store.GetGitHubCIJob(ctx, repo.String(), 9001); err != nil || j == nil || j.RunID != 77 ||
		j.RunURL != "https://ghe.example.com/acme/widgets/actions/runs/77" {
		t.Fatalf("a polled job was recorded as %+v (%v), want run 77 read from its page", j, err)
	}

	ext := client.ExternalCIRuns()
	for _, f := range []ci.RunFilter{
		{Repo: repo.String(), Provider: ci.ProviderGitHubActions},
		{Repo: repo.String(), Ref: "grain/task-9"},
	} {
		runs, note := ext.FindExternalRuns(ctx, f, 20)
		if len(runs) != 1 || runs[0].ID != "gha:ghe.example.com:acme:widgets:77" || runs[0].Repo != repo.String() ||
			strings.Join(runs[0].Actions.FailedJobs, ",") != "go build, vet and test" {
			t.Fatalf("%+v listed %+v (note %q)", f, runs, note)
		}
		if !strings.Contains(note, "404") || !strings.Contains(note, "ghe.example.com") {
			t.Errorf("%+v: note = %q, want the forge's 404 named", f, note)
		}
	}
	run, ok, err := ext.ExternalRun(ctx, "gha:ghe.example.com:acme:widgets:77")
	if err != nil || !ok || run.Repo != repo.String() || len(run.Jobs) != 2 {
		t.Errorf("show = %+v %v %v", run, ok, err)
	}
	for _, owner := range forge.owners {
		if owner != "ghe.example.com/acme" {
			t.Errorf("the forge was asked with owner %q, want host/owner", owner)
		}
	}

	// Nothing recorded and nothing on the forge: still said, not silent.
	empty := model.RepoRef{Host: "ghe.example.com", Owner: "acme", Name: "empty"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: empty, CIMode: model.CIModeGitHub}); err != nil {
		t.Fatal(err)
	}
	if runs, note := ext.FindExternalRuns(ctx, ci.RunFilter{Repo: empty.String(), Provider: ci.ProviderGitHubActions}, 20); len(runs) != 0 ||
		!strings.Contains(note, "404") || !strings.Contains(note, "recorded none") {
		t.Errorf("an empty listing = %v, note %q", runs, note)
	}
	if _, _, err := ext.ExternalRun(ctx, "gha:ghe.example.com:acme:empty:5"); err == nil || !strings.Contains(err.Error(), "no GitHub Actions run 5") {
		t.Errorf("show of a run nobody has = %v, want why", err)
	}
}

// TestActionsListingSaysWhyARepositoryIsNotListed: a listing that asked
// for GitHub Actions by name and got none because grain will not list
// that repository's runs says so, rather than answering an empty list.
func TestActionsListingSaysWhyARepositoryIsNotListed(t *testing.T) {
	t.Parallel()
	now := time.Now()
	_, client, store := actionsServer(t, newFakeActions(now), now)
	ctx := context.Background()
	grainRepo := model.RepoRef{Host: "ghe.example.com", Owner: "acme", Name: "gears"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: grainRepo, CIMode: model.CIModeGrain}); err != nil {
		t.Fatal(err)
	}
	ext := client.ExternalCIRuns()
	if _, note := ext.FindExternalRuns(ctx, ci.RunFilter{Repo: grainRepo.String(), Provider: ci.ProviderGitHubActions}, 20); !strings.Contains(note, "CI mode") {
		t.Errorf("asked for Actions of a grain-CI repository: note %q, want its CI mode named", note)
	}
	if _, note := ext.FindExternalRuns(ctx, ci.RunFilter{Repo: grainRepo.String()}, 20); note != "" {
		t.Errorf("a listing of any provider noted a grain-CI repository's missing Actions: %q", note)
	}
	if err := store.SetTargetRepos(ctx, []string{"acme/widgets"}); err != nil {
		t.Fatal(err)
	}
	if _, note := ext.FindExternalRuns(ctx, ci.RunFilter{Repo: "ghe.example.com/elsewhere/secrets"}, 20); !strings.Contains(note, "not a repository this deployment works in") {
		t.Errorf("a repository off the deployment's list: note %q", note)
	}
	if _, note := ext.FindExternalRuns(ctx, ci.RunFilter{Repo: "not a repo", Provider: ci.ProviderGitHubActions}, 20); note == "" {
		t.Errorf("an unparseable repository answered with no note")
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

func TestCIRunsListFindsScheduledNightlyPastFirstHundredRunsAndReportsMatched(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 2, 10, 44, 0, 0, time.UTC)
	var runs []github.WorkflowRun
	for i := 0; i < 160; i++ {
		at := now.Add(-time.Duration(i+1) * 3 * time.Minute)
		runs = append(runs, github.WorkflowRun{
			ID: int64(2000 - i), Name: "Tests", Path: ".github/workflows/tests.yml",
			HeadBranch: "main", HeadSHA: "aaa", Event: "push", Status: "completed", Conclusion: "success",
			StartedAt: &at, UpdatedAt: &at,
		})
	}
	nightlyAt := time.Date(2026, 11, 2, 2, 21, 46, 0, time.UTC)
	nightlyEnd := time.Date(2026, 11, 2, 2, 59, 54, 0, time.UTC)
	runs = append(runs, github.WorkflowRun{
		ID: 41377312988, Name: "nightly", Path: ".github/workflows/nightly.yml",
		HeadBranch: "main", HeadSHA: "744a1dbecd03", Event: "schedule", Status: "completed", Conclusion: "failure",
		StartedAt: &nightlyAt, UpdatedAt: &nightlyEnd,
	})
	forge := &fakeActions{runs: runs, total: 3410}
	srv, client, _ := actionsServer(t, forge, now)

	// Unfiltered with limit=20 reports GitHub's true total_count (3410) rather than 20.
	code, body := asAgent(t, srv, "reader", http.MethodGet, "/api/ci/runs?repo=acme/widgets&provider=github-actions&limit=20", "", "")
	if code != http.StatusOK {
		t.Fatalf("unfiltered list: %d %s", code, body)
	}
	unfiltered := decodeString[ui.CIRunsResponse](t, body)
	if len(unfiltered.Runs) != 20 || unfiltered.Matched != 3410 {
		t.Fatalf("unfiltered = %d runs, Matched=%d; want 20 runs of 3410", len(unfiltered.Runs), unfiltered.Matched)
	}

	// Filtering by ?workflow=nightly.yml (or ?workflow=nightly) finds the scheduled nightly run directly.
	for _, wf := range []string{"nightly.yml", "nightly", ".github/workflows/nightly.yml"} {
		code, body = asAgent(t, srv, "reader", http.MethodGet, "/api/ci/runs?repo=acme/widgets&provider=github-actions&workflow="+wf, "", "")
		if code != http.StatusOK {
			t.Fatalf("workflow=%s: %d %s", wf, code, body)
		}
		got := decodeString[ui.CIRunsResponse](t, body)
		if len(got.Runs) != 1 || got.Runs[0].ID != "gha:acme:widgets:41377312988" || got.Matched != 1 {
			t.Fatalf("workflow=%s = %+v (Matched=%d), want 1 nightly run", wf, got.Runs, got.Matched)
		}
	}

	// And ref=main&limit=200 pages past 100 runs to include nightly.yml without needing ?workflow=.
	code, body = asAgent(t, srv, "reader", http.MethodGet, "/api/ci/runs?repo=acme/widgets&provider=github-actions&ref=main&limit=200", "", "")
	if code != http.StatusOK {
		t.Fatalf("ref=main&limit=200: %d %s", code, body)
	}
	mainPage := decodeString[ui.CIRunsResponse](t, body)
	if len(mainPage.Runs) != 161 || mainPage.Runs[160].ID != "gha:acme:widgets:41377312988" {
		t.Fatalf("ref=main&limit=200 returned %d runs, last=%+v; want 161 ending in nightly",
			len(mainPage.Runs), mainPage.Runs[len(mainPage.Runs)-1])
	}

	// ExternalCIRuns.FindExternalRunsCounted agrees on both --workflow and matched counts.
	ext := client.ExternalCIRuns()
	extRuns, extMatched, note := ci.FindExternalRunsCounted(context.Background(), ext, ci.RunFilter{
		Repo: "acme/widgets", Provider: ci.ProviderGitHubActions, Workflow: "nightly.yml", Descending: true,
	}, 20)
	if note != "" || extMatched != 1 || len(extRuns) != 1 || extRuns[0].ID != "gha:acme:widgets:41377312988" {
		t.Fatalf("FindExternalRunsCounted(nightly.yml) = %+v, matched=%d, note=%q", extRuns, extMatched, note)
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

// A job log the forge will not serve -- refused at the blob store an
// Enterprise Server redirected to, or served empty -- is an error naming
// the status and the hosts it came from, never an empty log.
func TestCIRunLogSaysWhyTheForgeServedNoLog(t *testing.T) {
	t.Parallel()
	now := time.Now()
	forge := newFakeActions(now)
	forge.logErr = &github.Error{Status: http.StatusForbidden,
		URL: "GET https://ghe.example.test/api/v3/repos/acme/widgets/actions/jobs/701/logs -> https://blobs.example.test"}
	srv, _, _ := actionsServer(t, forge, now)
	code, body := asAgent(t, srv, "reader", http.MethodGet, "/api/ci/runs/gha:acme:widgets:42/log?job=publish", "", "")
	if code != http.StatusForbidden || !strings.Contains(body, "HTTP 403 from ghe.example.test, redirected to blobs.example.test") {
		t.Errorf("refused log = %d %s; want 403 naming the status and both hosts", code, body)
	}

	forge2 := newFakeActions(now)
	for id := range forge2.logs {
		forge2.logs[id] = ""
	}
	srv2, _, _ := actionsServer(t, forge2, now)
	code, body = asAgent(t, srv2, "reader", http.MethodGet, "/api/ci/runs/gha:acme:widgets:42/log?job=publish", "", "")
	if code != http.StatusBadGateway || !strings.Contains(body, "no bytes") {
		t.Errorf("empty log = %d %s; want 502 saying it came back empty", code, body)
	}
	// Nothing back is not cached as the log: the next read asks again.
	before := forge2.count("log")
	asAgent(t, srv2, "reader", http.MethodGet, "/api/ci/runs/gha:acme:widgets:42/log?job=publish", "", "")
	if after := forge2.count("log"); after != before+1 {
		t.Errorf("a second read of an empty log asked the forge %d more times; want 1 (not served from cache)", after-before)
	}

	// The REST client's own empty answer names the status, the hosts and
	// who the headers say sent it.
	forge3 := newFakeActions(now)
	forge3.logErr = fmt.Errorf("%w: %w", github.ErrEmptyJobLog, &github.Error{Status: http.StatusOK,
		URL:  "GET https://ghe.example.test/api/v3/repos/acme/widgets/actions/jobs/701/logs -> https://blobs.example.test",
		Note: "content-type text/plain, server blobstore"})
	srv3, _, _ := actionsServer(t, forge3, now)
	code, body = asAgent(t, srv3, "reader", http.MethodGet, "/api/ci/runs/gha:acme:widgets:42/log?job=publish", "", "")
	if code != http.StatusBadGateway || !strings.Contains(body, "no bytes at all, HTTP 200 from ghe.example.test, redirected to blobs.example.test (content-type text/plain, server blobstore)") {
		t.Errorf("empty log from the REST client = %d %s; want 502 naming the status, both hosts and the server", code, body)
	}
}
