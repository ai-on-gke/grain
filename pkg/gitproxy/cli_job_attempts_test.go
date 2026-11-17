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
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
)

// stage1Week is nightly.yml on two release candidates -- rc.1 failed its
// first run and passed when re-dispatched, rc.2 passed first time -- and
// on main, where it failed, beside a pull request's tests.yml run that
// failed the job of the same name.
func stage1Week() fakeForgeCI {
	base := time.Date(2026, 11, 16, 2, 0, 0, 0, time.UTC)
	f := fakeForgeCI{jobs: map[string][]ci.GitHubJob{}}
	add := func(runID int64, workflow, ref, sha string, hour int, conclusion string, jobs ...string) {
		at := base.Add(time.Duration(hour) * time.Hour)
		id := fmt.Sprintf("gha:acme:widgets:%d", runID)
		c := ci.ConclusionSuccess
		if conclusion != "success" {
			c = ci.ConclusionFailure
		}
		f.runs = append(f.runs, ci.Run{ID: id, Provider: ci.ProviderGitHubActions, Repo: "acme/widgets", Ref: ref, Commit: sha,
			Event: ci.Event("push"), StartedAt: &at, FinishedAt: &at, Conclusion: c,
			Actions: &ci.ActionsRun{RunID: runID, RunAttempt: 1, Workflow: ".github/workflows/" + workflow, WorkflowName: strings.TrimSuffix(workflow, ".yml"),
				Status: "completed", Conclusion: conclusion, HeadRepo: "acme/widgets"}})
		for i := 0; i+1 < len(jobs); i += 2 {
			f.jobs[id] = append(f.jobs[id], ci.GitHubJob{ID: runID*10 + int64(i), RunID: runID, RunAttempt: 1, HeadSHA: sha,
				Name: jobs[i], Status: "completed", Conclusion: str(jobs[i+1]), StartedAt: &at})
		}
	}
	// Newest first, as the forge lists them.
	add(950, "tests.yml", "grain/task-9", otherSHA, 9, "failure", "go", "failure")
	add(940, "nightly.yml", "main", "2222222222222222222222222222222222222222", 8, "failure", "go", "failure")
	add(930, "nightly.yml", "v1.2.0-rc.2", "3333333333333333333333333333333333333333", 6, "success",
		"go", "success", "ui e2e (1)", "success", "Stage 1 qualification verdict", "success")
	add(920, "nightly.yml", "v1.2.0-rc.1", rcSHA, 4, "success",
		"go", "success", "ui e2e (1)", "success", "Stage 1 qualification verdict", "success")
	add(910, "nightly.yml", "v1.2.0-rc.1", rcSHA, 2, "failure",
		"go", "failure", "ui e2e (1)", "success", "Stage 1 qualification verdict", "failure")
	return f
}

func TestTestHistoryJudgesAStage1JobFromEveryAttempt(t *testing.T) {
	get := forgeProxy(t, stage1Week())
	q := url.Values{"test": {"go"}, "repo": {"acme/widgets"}, "provider": {"github-actions"},
		"workflow": {"nightly.yml"}, "ref": {"v*-rc.*"}, "since": {"30d"}}
	rec := get("/cli/ci/test-history?" + q.Encode())
	if rec.Code != http.StatusOK {
		t.Fatalf("ci/test-history = %d %s", rec.Code, rec.Body)
	}
	out := rec.Body.String()
	for _, want := range []string{
		"Runs examined: 3 | Executions found: 2",
		`Job "go" read from every attempt of its runs, one execution per commit: 2 commit(s).`,
		"Passed:  1 (50.0%)",
		"Passed on retry: 1 (50.0%",
		"Failed:  0",
		"attempts: failure,success",
		"v1.2.0-rc.1 @ abcdef0",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("ci/test-history lacks %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"grain/task-9", "main @"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("ci/test-history counted a run --workflow and --ref leave out (%q):\n%s", unwanted, out)
		}
	}

	// Without --workflow it is the failing-log-tail history it always was.
	plain := get("/cli/ci/test-history?test=go&repo=acme/widgets&provider=github-actions")
	if plain.Code != http.StatusOK || strings.Contains(plain.Body.String(), "every attempt") {
		t.Errorf("ci/test-history without --workflow = %d\n%s", plain.Code, plain.Body)
	}
	if bad := get("/cli/ci/test-history?test=go&repo=acme/widgets&ref=" + url.QueryEscape("v[")); bad.Code != http.StatusBadRequest {
		t.Errorf("a malformed --ref glob = %d %s, want 400", bad.Code, bad.Body)
	}
}

func TestFlakeReportCountsPassOnRetryPerRcTag(t *testing.T) {
	get := forgeProxy(t, stage1Week())
	q := url.Values{"repo": {"acme/widgets"}, "workflow": {"nightly.yml"}, "ref": {"v*-rc.*"}, "since": {"30d"}, "format": {"json"}}
	rec := get("/cli/ci/flake-report?" + q.Encode())
	if rec.Code != http.StatusOK {
		t.Fatalf("ci/flake-report = %d %s", rec.Code, rec.Body)
	}
	var report ci.FlakeReport
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatalf("ci/flake-report is not JSON: %v\n%s", err, rec.Body)
	}
	if report.Runs != 3 || report.Commits != 2 || len(report.Jobs) != 2 {
		t.Fatalf("flake report = %+v, want 3 rc runs on 2 commits and the jobs go and ui e2e (no roll-up)", report)
	}
	goJob, ui := report.Jobs[0], report.Jobs[1]
	if goJob.Job != "go" || goJob.Commits != 2 || goJob.Passed != 1 || goJob.PassedOnRetry != 1 || goJob.Failed != 0 {
		t.Errorf("go = %+v, want rc.1's fail-then-pass as one pass-on-retry and rc.2's pass", goJob)
	}
	if ui.Job != "ui e2e" || ui.Passed != 2 || ui.PassedOnRetry != 0 {
		t.Errorf("ui e2e = %+v", ui)
	}

	text := get("/cli/ci/flake-report?repo=acme/widgets&workflow=nightly.yml&ref=" + url.QueryEscape("v*-rc.*"))
	if text.Code != http.StatusOK || !strings.Contains(text.Body.String(), "over budget") {
		t.Errorf("ci/flake-report text = %d\n%s", text.Code, text.Body)
	}
	if rec := get("/cli/ci/flake-report?repo=acme/widgets"); rec.Code != http.StatusBadRequest {
		t.Errorf("ci/flake-report without --workflow = %d %s, want 400", rec.Code, rec.Body)
	}
	if rec := get("other:/cli/ci/flake-report?repo=acme/widgets&workflow=nightly.yml"); rec.Code != http.StatusForbidden {
		t.Errorf("ci/flake-report of a repository the task may not read = %d %s, want 403", rec.Code, rec.Body)
	}
}
