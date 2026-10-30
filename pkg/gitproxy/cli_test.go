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

func TestCLIRoutesScopeNarrateAndAnswer(t *testing.T) {
	store := ci.NewRunStore(10)
	at := time.Now().Add(-time.Hour)
	store.Put(ci.Run{ID: "run-main", Repo: "bwsalmon/grain", Ref: "main", Conclusion: ci.ConclusionSuccess, StartedAt: &at,
		Jobs: []ci.JobRun{{Name: "go", Conclusion: ci.ConclusionSuccess}}})
	later := time.Now()
	store.Put(ci.Run{ID: "run-task", Repo: "bwsalmon/grain", Ref: "grain/task-7", Conclusion: ci.ConclusionFailure, StartedAt: &later,
		Jobs: []ci.JobRun{{Name: "go", Conclusion: ci.ConclusionFailure}, {Name: "lint", Conclusion: ci.ConclusionSuccess}}})

	tokenFile := filepath.Join(t.TempDir(), "sandbox-tokens.json")
	raw, _ := json.Marshal(map[string]string{"sb-ci": "tok-ci", "sb-plain": "tok-plain", "sb-norepo": "tok-norepo"})
	if err := os.WriteFile(tokenFile, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	tokens, err := gitproxy.LoadSandboxTokens(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	act := &fakeActivity{}
	proxy := &gitproxy.GitProxy{
		Tokens:    tokens,
		Namespace: &namespace.Server{CIStore: store, Redactor: redact.New(nil)},
		NamespaceScopes: fakeNamespaceScopes{scopes: map[string]model.NamespaceScope{
			"sb-ci": {TaskID: "7", RunID: "run-a", TaskRepo: "bwsalmon/grain", TaskBranch: "grain/task-7", TaskBase: "main",
				MayReadCI: true, AllowedRepos: map[string]bool{"bwsalmon/grain": true}},
			"sb-plain":  {TaskID: "8", RunID: "run-b", TaskRepo: "bwsalmon/grain"},
			"sb-norepo": {TaskID: "9", RunID: "run-c", MayReadCI: true, AllowedRepos: map[string]bool{"other/repo": true}},
		}},
		Activity: act,
	}
	h := gitproxy.NewHandler(proxy)
	get := func(token, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if token != "" {
			req.Header.Set("Authorization", basicAuthHeader(token))
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	if rec := get("", "/cli/ci/runs"); rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated /cli/: %d", rec.Code)
	}
	rec := get("tok-ci", "/cli/ci/runs?conclusion=failure")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "run-task grain/task-7") ||
		!strings.Contains(rec.Body.String(), "failing: go") || strings.Contains(rec.Body.String(), "run-main") {
		t.Errorf("ci runs: %d %s", rec.Code, rec.Body)
	}
	rec = get("tok-ci", "/cli/pr/status")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "run-task") || !strings.Contains(rec.Body.String(), "lint: success") {
		t.Errorf("pr status: %d %s", rec.Code, rec.Body)
	}
	rec = get("tok-ci", "/cli/pr/base-status")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "run-main") {
		t.Errorf("base status: %d %s", rec.Code, rec.Body)
	}
	rec = get("tok-plain", "/cli/ci/runs")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `does not hold the "grain-ci" capability`) {
		t.Errorf("ci runs without grain-ci: %d %s", rec.Code, rec.Body)
	}
	rec = get("tok-plain", "/cli/db/query")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"db-read"`) ||
		!strings.Contains(rec.Body.String(), "a human attaches it to the task") {
		t.Errorf("db query without db-read: %d %s", rec.Code, rec.Body)
	}
	// A task with no repository of its own, reading CI of one other
	// repository, is not handed every repository's runs by leaving the
	// repo out.
	rec = get("tok-norepo", "/cli/ci/runs")
	if rec.Code != http.StatusBadRequest || strings.Contains(rec.Body.String(), "run-") {
		t.Errorf("ci runs with no task repo and no --repo: %d %s", rec.Code, rec.Body)
	}
	// A route the proxy does not serve is a 404 and is not counted: its
	// name is the sandbox's own invention.
	if rec := get("tok-ci", "/cli/made-up/route"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown route: %d", rec.Code)
	}

	act.mu.Lock()
	defer act.mu.Unlock()
	var tools []string
	for _, row := range act.toolRows {
		tools = append(tools, row.tool)
	}
	want := "list_ci_runs pull_request_status base_branch_status list_ci_runs query_grain_db list_ci_runs"
	if got := strings.Join(tools, " "); got != want {
		t.Errorf("recorded tools = %q, want %q", got, want)
	}
}

// fakeTaskRefScopes is fakeNamespaceScopes that also names other tasks'
// repositories and branches (gitproxy.TaskRefLookup).
type fakeTaskRefScopes struct {
	fakeNamespaceScopes
	refs map[string][2]string
}

func (f fakeTaskRefScopes) TaskCIRef(_ context.Context, id string) (string, string, bool, error) {
	ref, ok := f.refs[id]
	return ref[0], ref[1], ok, nil
}

// A task reads the CI of a sibling task, by its id, or of any branch, in
// its own repository or one it reads -- and a run of either by its id
// alone -- and nothing outside them.
func TestCLIReadsOtherTasksAndBranchesCI(t *testing.T) {
	store := ci.NewRunStore(20)
	at := time.Now().Add(-time.Hour)
	later := time.Now()
	store.Put(ci.Run{ID: "run-sib-old", Repo: "acme/widgets", Ref: "grain/dep/task-19", Conclusion: ci.ConclusionFailure, StartedAt: &at,
		Jobs: []ci.JobRun{{Name: "go", Conclusion: ci.ConclusionFailure}}})
	store.Put(ci.Run{ID: "run-sib", Repo: "acme/widgets", Ref: "grain/dep/task-19", Conclusion: ci.ConclusionSuccess, StartedAt: &later,
		Jobs: []ci.JobRun{{Name: "go", Conclusion: ci.ConclusionSuccess}, {Name: "lint", Conclusion: ci.ConclusionSuccess}}})
	store.Put(ci.Run{ID: "run-190", Repo: "acme/widgets", Ref: "grain/dep/task-190", Conclusion: ci.ConclusionSuccess, StartedAt: &later})
	store.Put(ci.Run{ID: "run-lib", Repo: "acme/library", Ref: "feature/x", Conclusion: ci.ConclusionFailure, StartedAt: &later,
		Jobs: []ci.JobRun{{Name: "test", Conclusion: ci.ConclusionFailure}}})
	store.Put(ci.Run{ID: "run-lib-task", Repo: "acme/library", Ref: "grain/dep/task-30", Conclusion: ci.ConclusionSuccess, StartedAt: &later})
	store.Put(ci.Run{ID: "run-gadget", Repo: "acme/gadgets", Ref: "grain/dep/task-40", Conclusion: ci.ConclusionSuccess, StartedAt: &later})

	tokenFile := filepath.Join(t.TempDir(), "sandbox-tokens.json")
	raw, _ := json.Marshal(map[string]string{"sb": "tok"})
	if err := os.WriteFile(tokenFile, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	tokens, err := gitproxy.LoadSandboxTokens(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	proxy := &gitproxy.GitProxy{
		Tokens:    tokens,
		Namespace: &namespace.Server{CIStore: store, Redactor: redact.New(nil)},
		NamespaceScopes: fakeTaskRefScopes{
			fakeNamespaceScopes: fakeNamespaceScopes{scopes: map[string]model.NamespaceScope{
				"sb": {TaskID: "7", RunID: "run-a", TaskRepo: "acme/widgets", TaskBranch: "grain/dep/task-7", TaskBase: "main",
					MayReadCI: true, AllowedRepos: map[string]bool{"acme/widgets": true, "acme/library": true}},
			}},
			refs: map[string][2]string{
				"19": {"acme/widgets", "grain/dep/task-19"},
				"30": {"acme/library", "grain/dep/task-30"},
				"40": {"acme/gadgets", "grain/dep/task-40"},
			},
		},
		Activity: &fakeActivity{},
	}
	h := gitproxy.NewHandler(proxy)
	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", basicAuthHeader("tok"))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	rec := get("/cli/ci/runs?task=%2319")
	if body := rec.Body.String(); rec.Code != http.StatusOK || !strings.Contains(body, "run-sib ") ||
		!strings.Contains(body, "run-sib-old") || strings.Contains(body, "run-190") {
		t.Errorf("ci runs of a sibling task: %d %s", rec.Code, body)
	}
	rec = get("/cli/ci/runs?task=30")
	if body := rec.Body.String(); rec.Code != http.StatusOK || !strings.Contains(body, "run-lib-task") || strings.Contains(body, "run-lib ") {
		t.Errorf("ci runs of a task in a reads repository: %d %s", rec.Code, body)
	}
	for _, path := range []string{"/cli/ci/runs?task=40", "/cli/ci/runs?task=99", "/cli/pr/status?task=40"} {
		if rec := get(path); rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "run-gadget") {
			t.Errorf("%s outside the scope: %d %s", path, rec.Code, rec.Body)
		}
	}
	rec = get("/cli/pr/status?task=19")
	if body := rec.Body.String(); rec.Code != http.StatusOK || !strings.Contains(body, "task 19's branch") ||
		!strings.Contains(body, "run-sib ") || !strings.Contains(body, "lint: success") {
		t.Errorf("pr status of a sibling task: %d %s", rec.Code, body)
	}
	rec = get("/cli/pr/status?ref=feature/x&repo=acme/library")
	if body := rec.Body.String(); rec.Code != http.StatusOK || !strings.Contains(body, "run-lib") || !strings.Contains(body, "test: failure") {
		t.Errorf("pr status of a branch in a reads repository: %d %s", rec.Code, body)
	}
	if rec := get("/cli/pr/status?ref=grain/dep/task-40&repo=acme/gadgets"); rec.Code != http.StatusForbidden {
		t.Errorf("pr status of a branch outside the scope: %d %s", rec.Code, rec.Body)
	}

	rec = get("/cli/ci/run?id=run-lib")
	var named map[string]string
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &named) != nil || named["repo"] != "acme/library" {
		t.Errorf("ci run lookup in a reads repository: %d %s", rec.Code, rec.Body)
	}
	for _, id := range []string{"run-gadget", "no-such-run"} {
		if rec := get("/cli/ci/run?id=" + id); rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "acme/gadgets") {
			t.Errorf("ci run lookup of %s: %d %s", id, rec.Code, rec.Body)
		}
	}
}
