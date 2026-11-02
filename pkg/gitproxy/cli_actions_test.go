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

// fakeExternalCI is one GitHub Actions run of acme/widgets.
type fakeExternalCI struct{ run ci.Run }

func (f fakeExternalCI) FindExternalRuns(_ context.Context, filter ci.RunFilter, _ int) ([]ci.Run, string) {
	if !strings.EqualFold(filter.Repo, f.run.Repo) || (filter.Provider != "" && filter.Provider != ci.ProviderGitHubActions) {
		return nil, ""
	}
	return []ci.Run{f.run}, ""
}

func (f fakeExternalCI) ExternalRun(_ context.Context, id string) (ci.Run, bool, error) {
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
