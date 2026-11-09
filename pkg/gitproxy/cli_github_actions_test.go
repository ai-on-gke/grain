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

package gitproxy_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/namespace"
	"github.com/ai-on-gke/grain/pkg/redact"
)

// fakeExternalCI is one GitHub Actions run of acme/widgets. asked, when
// set, records the id of every run read from it.
type fakeExternalCI struct {
	run   ci.Run
	asked *[]string
	// err, when set, is what ExternalRun fails with.
	err error
}

func (f fakeExternalCI) FindExternalRuns(_ context.Context, filter ci.RunFilter, _ int) ([]ci.Run, string) {
	if !strings.EqualFold(filter.Repo, f.run.Repo) || (filter.Provider != "" && filter.Provider != ci.ProviderGitHubActions) {
		return nil, ""
	}
	return []ci.Run{f.run}, ""
}

func (f fakeExternalCI) ExternalRun(_ context.Context, id string) (ci.Run, bool, error) {
	if f.asked != nil {
		*f.asked = append(*f.asked, id)
	}
	if f.err != nil {
		return ci.Run{}, false, f.err
	}
	return f.run, id == f.run.ID, nil
}

func (f fakeExternalCI) ExternalJobLog(_ context.Context, id, job string) (string, error) {
	return "log of " + job + "\n", nil
}

func TestCLIListsAndReadsGitHubActionsRuns(t *testing.T) {
	store := ci.NewRunStore(10)
	grainAt := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	store.Put(ci.Run{ID: "run-1", Repo: "acme/widgets", Ref: "main", Commit: "1111111111111111", Conclusion: ci.ConclusionSuccess,
		StartedAt: &grainAt, Jobs: []ci.JobRun{{Name: "go", Conclusion: ci.ConclusionSuccess}}})
	ghaAt := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	gha := ci.Run{ID: "gha:acme:widgets:42", Provider: ci.ProviderGitHubActions, Repo: "acme/widgets", Ref: "main",
		Commit: "abcdefabcdefabcdef", Conclusion: ci.ConclusionFailure, StartedAt: &ghaAt,
		Actions: &ci.ActionsRun{RunID: 42, Workflow: ".github/workflows/release.yml", WorkflowName: "Release", FailedJobs: []string{"publish"}},
		Jobs:    []ci.JobRun{{JobID: "701", Name: "publish", Conclusion: ci.ConclusionFailure}}}

	tokenFile := filepath.Join(t.TempDir(), "sandbox-tokens.json")
	raw, _ := json.Marshal(map[string]string{"sb-ci": "tok-ci", "sb-other": "tok-other"})
	if err := os.WriteFile(tokenFile, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	tokens, err := gitproxy.LoadSandboxTokens(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	proxy := &gitproxy.GitProxy{
		Tokens:    tokens,
		Namespace: &namespace.Server{CIStore: store, ExternalCI: fakeExternalCI{run: gha}, Redactor: redact.New(nil)},
		NamespaceScopes: fakeNamespaceScopes{scopes: map[string]model.NamespaceScope{
			"sb-ci": {TaskID: "7", RunID: "run-a", TaskRepo: "acme/widgets", TaskBranch: "grain/task-7", TaskBase: "main",
				MayReadCI: true, AllowedRepos: map[string]bool{"acme/widgets": true}},
			"sb-other": {TaskID: "8", RunID: "run-b", TaskRepo: "acme/other", MayReadCI: true,
				AllowedRepos: map[string]bool{"acme/other": true}},
		}},
		Activity: &fakeActivity{},
	}
	h := gitproxy.NewHandler(proxy)
	get := func(token, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", basicAuthHeader(token))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	golden := map[string]string{
		"/cli/ci/runs?repo=acme/widgets": "2 of 2 matching run(s) in acme/widgets, newest first:\n" +
			"gha:acme:widgets:42 github-actions/release.yml main abcdefabcdef failure 2026-11-01T10:00:00Z failing: publish\n" +
			"run-1 grain main 111111111111 success 2026-11-01T09:00:00Z\n",
		"/cli/ci/runs?repo=acme/widgets&provider=grain": "1 of 1 matching run(s) in acme/widgets, newest first:\n" +
			"run-1 grain main 111111111111 success 2026-11-01T09:00:00Z\n",
		"/cli/ci/runs?repo=acme/widgets&provider=github-actions": "1 of 1 matching run(s) in acme/widgets, newest first:\n" +
			"gha:acme:widgets:42 github-actions/release.yml main abcdefabcdef failure 2026-11-01T10:00:00Z failing: publish\n",
	}
	for path, want := range golden {
		if rec := get("tok-ci", path); rec.Code != http.StatusOK || rec.Body.String() != want {
			t.Errorf("%s = %d\n%s\nwant\n%s", path, rec.Code, rec.Body, want)
		}
	}
	if rec := get("tok-ci", "/cli/ci/runs?repo=acme/widgets&provider=jenkins"); rec.Code != http.StatusBadRequest {
		t.Errorf("an unknown provider answered %d %s", rec.Code, rec.Body)
	}
	if rec := get("tok-ci", "/cli/ci/run?id=gha:acme:widgets:42"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"repo":"acme/widgets"`) {
		t.Errorf("ci/run = %d %s", rec.Code, rec.Body)
	}
	if rec := get("tok-other", "/cli/ci/run?id=gha:acme:widgets:42"); rec.Code != http.StatusNotFound {
		t.Errorf("a sandbox outside acme/widgets found its Actions run: %d %s", rec.Code, rec.Body)
	}
	if rec := get("tok-other", "/fs/ci/acme/widgets/gha:acme:widgets:42/publish/job.log"); rec.Code != http.StatusForbidden {
		t.Errorf("a sandbox outside acme/widgets read its Actions log: %d %s", rec.Code, rec.Body)
	}
	if rec := get("tok-ci", "/fs/ci/acme/widgets/gha:acme:widgets:42/publish/job.log"); rec.Code != http.StatusOK || rec.Body.String() != "log of 701\n" {
		t.Errorf("job.log = %d %q", rec.Code, rec.Body)
	}
	if rec := get("tok-ci", "/fs/ci/acme/widgets?list"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "gha:acme:widgets:42") {
		t.Errorf("ci://acme/widgets listing = %d %s", rec.Code, rec.Body)
	}
}

// TestCLIRefusesAnOutOfScopeActionsRunBeforeReadingIt: the daemon reads
// an Actions run from the forge with its own credential, so a run id
// naming a repository outside the sandbox's scope -- or, under ci://,
// one other than the path's -- is refused without that read.
func TestCLIRefusesAnOutOfScopeActionsRunBeforeReadingIt(t *testing.T) {
	store := ci.NewRunStore(10)
	gha := ci.Run{ID: "gha:acme:widgets:42", Provider: ci.ProviderGitHubActions, Repo: "acme/widgets",
		Jobs: []ci.JobRun{{JobID: "701", Name: "publish", Conclusion: ci.ConclusionFailure}}}
	var asked []string
	tokenFile := filepath.Join(t.TempDir(), "sandbox-tokens.json")
	raw, _ := json.Marshal(map[string]string{"sb-ci": "tok-ci", "sb-other": "tok-other"})
	if err := os.WriteFile(tokenFile, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	tokens, err := gitproxy.LoadSandboxTokens(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	proxy := &gitproxy.GitProxy{
		Tokens:    tokens,
		Namespace: &namespace.Server{CIStore: store, ExternalCI: fakeExternalCI{run: gha, asked: &asked}, Redactor: redact.New(nil)},
		NamespaceScopes: fakeNamespaceScopes{scopes: map[string]model.NamespaceScope{
			"sb-ci": {TaskID: "7", RunID: "run-a", TaskRepo: "acme/widgets", MayReadCI: true,
				AllowedRepos: map[string]bool{"acme/widgets": true}},
			"sb-other": {TaskID: "8", RunID: "run-b", TaskRepo: "acme/other", MayReadCI: true,
				AllowedRepos: map[string]bool{"acme/other": true}},
		}},
		Activity: &fakeActivity{},
	}
	h := gitproxy.NewHandler(proxy)
	get := func(token, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", basicAuthHeader(token))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	for _, path := range []string{"/cli/ci/run?id=gha:acme:widgets:42", "/cli/ci/show?id=gha:acme:widgets:42"} {
		if rec := get("tok-other", path); rec.Code != http.StatusNotFound {
			t.Errorf("a sandbox outside acme/widgets: %s = %d %s", path, rec.Code, rec.Body)
		}
	}
	// acme/other's path is in scope; the id in it names acme/widgets.
	if rec := get("tok-other", "/fs/ci/acme/other/gha:acme:widgets:42/publish/job.log"); rec.Code == http.StatusOK {
		t.Errorf("a sandbox read acme/widgets' Actions log under acme/other's path: %s", rec.Body)
	}
	if len(asked) != 0 {
		t.Errorf("out-of-scope runs were read from the forge: %v", asked)
	}
	if rec := get("tok-ci", "/cli/ci/show?id=gha:acme:widgets:42"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "job publish") {
		t.Errorf("an in-scope show = %d %s", rec.Code, rec.Body)
	}
}

// TestCLISaysWhyAnActionsRunOffTheDefaultHostIsNotFound: a run of a
// repository on another git host that the daemon could not read is still
// not found, but the answer carries why -- it used to be a bare "no CI
// run", which said nothing about which of the forge, grain's record or
// the scope had come up empty.
func TestCLISaysWhyAnActionsRunOffTheDefaultHostIsNotFound(t *testing.T) {
	const repo = "ghe.example.com/acme/widgets"
	const id = "gha:ghe.example.com:acme:widgets:77"
	tokenFile := filepath.Join(t.TempDir(), "sandbox-tokens.json")
	raw, _ := json.Marshal(map[string]string{"sb-ci": "tok-ci"})
	if err := os.WriteFile(tokenFile, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	tokens, err := gitproxy.LoadSandboxTokens(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	external := fakeExternalCI{run: ci.Run{ID: id, Repo: repo}, err: errors.New("no GitHub Actions run 77 in " + repo)}
	proxy := &gitproxy.GitProxy{
		Tokens:    tokens,
		Namespace: &namespace.Server{CIStore: ci.NewRunStore(10), ExternalCI: external, Redactor: redact.New(nil)},
		NamespaceScopes: fakeNamespaceScopes{scopes: map[string]model.NamespaceScope{
			"sb-ci": {TaskID: "7", RunID: "run-a", TaskRepo: repo, MayReadCI: true, AllowedRepos: map[string]bool{repo: true}},
		}},
		Activity: &fakeActivity{},
	}
	h := gitproxy.NewHandler(proxy)
	for _, path := range []string{"/cli/ci/run?id=" + id, "/cli/ci/show?id=" + id} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", basicAuthHeader("tok-ci"))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "no GitHub Actions run 77 in "+repo) {
			t.Errorf("%s = %d %s, want a 404 saying why", path, rec.Code, rec.Body)
		}
	}
}

// actionsCIRepo is fakeExternalCI for a repository whose CI is GitHub
// Actions (ci.ExternalRunsRepos).
type actionsCIRepo struct{ fakeExternalCI }

func (a actionsCIRepo) ServesRepo(_ context.Context, repo string) bool {
	return strings.EqualFold(repo, a.run.Repo)
}

// TestCLITestHistoryReadsGitHubActionsOnAnActionsRepo: on a repository
// whose CI is GitHub Actions, `grain ci test-history` counts the test's
// failures out of the Actions runs grain recorded the failed jobs' logs
// of, rather than reporting grain CI's month-old passes of a retired
// mode as its history -- and says which runs it read, and which it left
// out.
func TestCLITestHistoryReadsGitHubActionsOnAnActionsRepo(t *testing.T) {
	store := ci.NewRunStore(10)
	old := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	for _, id := range []string{"run-1", "run-2"} {
		store.Put(ci.Run{ID: id, Repo: "acme/widgets", Ref: "main", Conclusion: ci.ConclusionSuccess, StartedAt: &old,
			Jobs: []ci.JobRun{{Name: "go test", Conclusion: ci.ConclusionSuccess}}})
	}
	at := time.Date(2026, 11, 8, 6, 0, 0, 0, time.UTC)
	gha := ci.Run{ID: "gha:acme:widgets:42", Provider: ci.ProviderGitHubActions, Repo: "acme/widgets", Ref: "main",
		Commit: "abcdefabcdefabcdef", Conclusion: ci.ConclusionFailure, StartedAt: &at,
		Actions: &ci.ActionsRun{RunID: 42, Workflow: ".github/workflows/nightly.yml", FailedJobs: []string{"go-long"}},
		Jobs: []ci.JobRun{
			{Name: "go-long", Conclusion: ci.ConclusionFailure, StartedAt: &at, Steps: []ci.StepRun{{Name: "test",
				Conclusion: ci.ConclusionFailure, Output: "--- FAIL: TestThing (0.20s)\nFAIL\n"}}},
			{Name: "go unit", Conclusion: ci.ConclusionSuccess, StartedAt: &at},
		}}
	tokenFile := filepath.Join(t.TempDir(), "sandbox-tokens.json")
	raw, _ := json.Marshal(map[string]string{"sb-ci": "tok-ci"})
	if err := os.WriteFile(tokenFile, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	tokens, err := gitproxy.LoadSandboxTokens(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	proxy := &gitproxy.GitProxy{
		Tokens:    tokens,
		Namespace: &namespace.Server{CIStore: store, ExternalCI: actionsCIRepo{fakeExternalCI{run: gha}}, Redactor: redact.New(nil)},
		NamespaceScopes: fakeNamespaceScopes{scopes: map[string]model.NamespaceScope{
			"sb-ci": {TaskID: "7", RunID: "run-a", TaskRepo: "acme/widgets", TaskBranch: "grain/task-7", TaskBase: "main",
				MayReadCI: true, AllowedRepos: map[string]bool{"acme/widgets": true}},
		}},
		Activity: &fakeActivity{},
	}
	h := gitproxy.NewHandler(proxy)
	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", basicAuthHeader("tok-ci"))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	rec := get("/cli/ci/test-history?test=TestThing")
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("test-history = %d %s", rec.Code, body)
	}
	for _, want := range []string{
		"Runs examined: 1 | Executions found: 1",
		"GitHub Actions: 1 run(s) (2026-11-08 06:00 UTC)",
		"GitHub Actions runs count only this test's failures",
		"its 2 Grain CI run(s), from before, are left out (--provider grain reads them)",
		"Failed:  1",
		`run gha:acme:widgets:42 (nightly.yml, main @ abcdefa) | job "go-long": FAIL`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("test-history does not say %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Passed:  1") || strings.Contains(body, "Grain CI: ") {
		t.Errorf("an Actions repository's history counted a pass or grain CI's runs:\n%s", body)
	}

	rec = get("/cli/ci/test-history?test=TestThing&provider=grain")
	if body := rec.Body.String(); rec.Code != http.StatusOK || !strings.Contains(body, "Grain CI: 2 run(s) (2026-10-31 09:00 UTC)") ||
		!strings.Contains(body, "Passed:  2") {
		t.Errorf("--provider grain = %d\n%s", rec.Code, body)
	}
	if rec := get("/cli/ci/test-history?test=TestThing&provider=jenkins"); rec.Code != http.StatusBadRequest {
		t.Errorf("an unknown provider answered %d %s", rec.Code, rec.Body)
	}
}
