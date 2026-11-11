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

// A run on a GitHub-CI repository holding only the default
// api:ci-read@<target> (model.DefaultCIReadGrant) -- a scope that may read
// its own repository's CI and no other -- gets the commands its prompt
// lists: `grain ci list`, `grain ci log`, `grain pr status` and `grain pr
// base-status` all answer for its GitHub Actions runs, and another
// repository's CI is refused. A run without it is refused naming
// api#ci-read, never grain#ci (task 4364).
func TestDefaultCIReadScopeReadsItsOwnGitHubActionsCI(t *testing.T) {
	store := ci.NewRunStore(20)
	at := func(m int) *time.Time { t := time.Date(2026, 11, 11, 9, m, 0, 0, time.UTC); return &t }
	gha := func(id int64, ref, commit, workflow string, concl ci.Conclusion, m int, jobs ...ci.JobRun) ci.Run {
		return ci.Run{ID: "gha:acme:widgets:" + time.Duration(id).String(), Provider: ci.ProviderGitHubActions,
			Repo: "acme/widgets", Ref: ref, Commit: commit, Conclusion: concl, StartedAt: at(m),
			Actions: &ci.ActionsRun{RunID: id, Workflow: ".github/workflows/" + workflow}, Jobs: jobs}
	}
	// An older push, then two workflows at the newest commit.
	store.Put(gha(1, "grain/task-7", "aaaaaaaaaaaaaaaa", "tests.yml", ci.ConclusionFailure, 1,
		ci.JobRun{Name: "go", Conclusion: ci.ConclusionFailure}))
	store.Put(gha(2, "grain/task-7", "bbbbbbbbbbbbbbbb", "tests.yml", ci.ConclusionSuccess, 5,
		ci.JobRun{Name: "go", Conclusion: ci.ConclusionSuccess}))
	store.Put(gha(3, "grain/task-7", "bbbbbbbbbbbbbbbb", "plc-tests.yml", ci.ConclusionFailure, 6,
		ci.JobRun{Name: "plc", Conclusion: ci.ConclusionFailure}))
	store.Put(gha(4, "main", "cccccccccccccccc", "tests.yml", ci.ConclusionSuccess, 0,
		ci.JobRun{Name: "go", Conclusion: ci.ConclusionSuccess}))
	store.Put(ci.Run{ID: "gha:acme:other:9", Provider: ci.ProviderGitHubActions, Repo: "acme/other", Ref: "main",
		Commit: "dddddddddddddddd", Conclusion: ci.ConclusionSuccess, StartedAt: at(2)})

	tokenFile := filepath.Join(t.TempDir(), "sandbox-tokens.json")
	raw, _ := json.Marshal(map[string]string{"sb-read": "tok-read", "sb-none": "tok-none"})
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
		NamespaceScopes: fakeNamespaceScopes{scopes: map[string]model.NamespaceScope{
			// What SandboxNamespaceScope makes of api:ci-read@acme/widgets.
			"sb-read": {TaskID: "7", RunID: "run-a", TaskRepo: "acme/widgets", TaskBranch: "grain/task-7", TaskBase: "main",
				MayReadCI: true, AllowedRepos: map[string]bool{"acme/widgets": true},
				Grants: []string{model.DefaultCIReadGrant(model.RepoRef{Owner: "acme", Name: "widgets"}).Capability}},
			"sb-none": {TaskID: "8", RunID: "run-b", TaskRepo: "acme/widgets", TaskBranch: "grain/task-8", TaskBase: "main",
				AllowedRepos: map[string]bool{"acme/widgets": true}},
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

	rec := get("tok-read", "/cli/pr/status")
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "latest GitHub Actions runs at bbbbbbbbbbbb") ||
		!strings.Contains(body, "tests.yml") || !strings.Contains(body, "plc-tests.yml") || !strings.Contains(body, "plc: failure") ||
		strings.Contains(body, "grain CI") || strings.Contains(body, "aaaa") {
		t.Errorf("pr status: %d %s, want both workflows at the newest commit and nothing older", rec.Code, body)
	}
	rec = get("tok-read", "/cli/pr/base-status")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "latest GitHub Actions runs at cccccccccccc") {
		t.Errorf("base status: %d %s", rec.Code, rec.Body)
	}
	rec = get("tok-read", "/cli/ci/runs")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "gha:acme:widgets") || strings.Contains(rec.Body.String(), "acme:other") {
		t.Errorf("ci runs: %d %s", rec.Code, rec.Body)
	}
	for _, path := range []string{"/cli/ci/runs?repo=acme/other", "/cli/pr/status?ref=main&repo=acme/other"} {
		if rec := get("tok-read", path); rec.Code != http.StatusForbidden {
			t.Errorf("%s with ci-read on acme/widgets only: %d %s, want 403", path, rec.Code, rec.Body)
		}
	}

	for _, path := range []string{"/cli/ci/runs", "/cli/pr/status", "/cli/pr/base-status"} {
		rec := get("tok-none", path)
		body := rec.Body.String()
		if rec.Code != http.StatusForbidden || !strings.Contains(body, `"https://grain.dev/capabilities/api#ci-read" on acme/widgets`) ||
			strings.Contains(body, `"https://grain.dev/capabilities/grain#ci"`) {
			t.Errorf("%s without a CI read: %d %s, want a refusal naming api#ci-read and not grain#ci", path, rec.Code, body)
		}
	}
}
