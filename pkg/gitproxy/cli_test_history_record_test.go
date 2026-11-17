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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/namespace"
	"github.com/ai-on-gke/grain/pkg/redact"
)

// recordedFailuresCI is an Actions repository whose provider also keeps
// a record of failed jobs reaching further back than its listing.
type recordedFailuresCI struct {
	actionsCIRepo
	failures []ci.Run
}

func (r recordedFailuresCI) FindExternalTestFailures(_ context.Context, f ci.RunFilter, test string) ([]ci.Run, string) {
	return r.failures, ""
}

// `grain ci test-history` on an Actions repository counts a test's
// failures over grain's weeks-long record of failed jobs, not only over
// the newest runs the listing reaches, and says so (grain/task-5274).
func TestCLITestHistoryCountsFailuresOverTheRecordedWeeks(t *testing.T) {
	newest := time.Date(2026, 11, 12, 6, 0, 0, 0, time.UTC)
	listed := ci.Run{ID: "gha:acme:widgets:90", Provider: ci.ProviderGitHubActions, Repo: "acme/widgets", Ref: "main",
		Commit: "9999999999", Conclusion: ci.ConclusionFailure, StartedAt: &newest,
		Actions: &ci.ActionsRun{RunID: 90, Workflow: ".github/workflows/tests.yml"},
		Jobs: []ci.JobRun{{Name: "go", Conclusion: ci.ConclusionFailure, StartedAt: &newest,
			Steps: []ci.StepRun{{Name: "test", Conclusion: ci.ConclusionFailure, Output: "--- FAIL: TestThing (0.2s)\n"}}}}}
	recordedRun := func(id int64, at time.Time, test string) ci.Run {
		return ci.Run{ID: "gha:acme:widgets:" + strconv.FormatInt(id, 10), Provider: ci.ProviderGitHubActions,
			Repo: "acme/widgets", Ref: "grain/task-3", Conclusion: ci.ConclusionFailure, StartedAt: &at,
			Actions: &ci.ActionsRun{RunID: id},
			Jobs: []ci.JobRun{{Name: "go", Conclusion: ci.ConclusionFailure, StartedAt: &at,
				FailureSummary: &ci.FailureSummary{Tests: []ci.TestFailure{{Name: test}}}}}}
	}
	weeksAgo := time.Date(2026, 10, 20, 6, 0, 0, 0, time.UTC)
	failures := []ci.Run{
		listed, // also listed: counted once
		recordedRun(30, weeksAgo, "TestThing/sub"),
		recordedRun(31, weeksAgo.Add(time.Hour), "TestThing"),
		recordedRun(32, weeksAgo.Add(2*time.Hour), "TestThingElse"), // a containment hit the name match drops
	}

	tokenFile := filepath.Join(t.TempDir(), "sandbox-tokens.json")
	raw, _ := json.Marshal(map[string]string{"sb-ci": "tok-ci"})
	if err := os.WriteFile(tokenFile, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	tokens, err := gitproxy.LoadSandboxTokens(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	ext := recordedFailuresCI{actionsCIRepo: actionsCIRepo{fakeExternalCI{run: listed}}, failures: failures}
	proxy := &gitproxy.GitProxy{
		Tokens:    tokens,
		Namespace: &namespace.Server{CIStore: ci.NewRunStore(10), ExternalCI: ext, Redactor: redact.New(nil)},
		NamespaceScopes: fakeNamespaceScopes{scopes: map[string]model.NamespaceScope{
			"sb-ci": {TaskID: "7", RunID: "run-a", TaskRepo: "acme/widgets", TaskBranch: "grain/task-7", TaskBase: "main",
				MayReadCI: true, AllowedRepos: map[string]bool{"acme/widgets": true}},
		}},
		Activity: &fakeActivity{},
	}
	req := httptest.NewRequest(http.MethodGet, "/cli/ci/test-history?test=TestThing", nil)
	req.Header.Set("Authorization", basicAuthHeader("tok-ci"))
	rec := httptest.NewRecorder()
	gitproxy.NewHandler(proxy).ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("test-history = %d %s", rec.Code, body)
	}
	for _, want := range []string{
		"Runs examined: 3 | Executions found: 3",
		"GitHub Actions: 3 run(s) (2026-10-20 06:00 UTC .. 2026-11-12 06:00 UTC)",
		"Failed:  3",
		"2 further run(s) are grain's record of failed Actions jobs naming this test, kept for 8 weeks",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("test-history does not say %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "gha:acme:widgets:32") {
		t.Errorf("a run failing only another test was counted:\n%s", body)
	}
}
