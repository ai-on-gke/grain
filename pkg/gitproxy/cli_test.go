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
