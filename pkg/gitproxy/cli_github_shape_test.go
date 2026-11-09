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
	"os/exec"
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

// fakeForgeCI is GitHub Actions runs of acme/widgets and the check runs
// on its commits, with every attempt of each run's jobs.
type fakeForgeCI struct {
	runs   []ci.Run
	jobs   map[string][]ci.GitHubJob
	checks map[string][]ci.GitHubCheckRun
	app    string
}

func (f fakeForgeCI) FindExternalRuns(_ context.Context, filter ci.RunFilter, limit int) ([]ci.Run, string) {
	var out []ci.Run
	for _, run := range f.runs {
		if strings.EqualFold(filter.Repo, run.Repo) && filter.Matches(run) && len(out) < limit {
			out = append(out, run)
		}
	}
	return out, ""
}

func (f fakeForgeCI) ExternalRun(_ context.Context, id string) (ci.Run, bool, error) {
	for _, run := range f.runs {
		if run.ID == id {
			return run, true, nil
		}
	}
	return ci.Run{}, false, nil
}

func (f fakeForgeCI) ExternalJobLog(context.Context, string, string) (string, error) { return "", nil }

func (f fakeForgeCI) ExternalJobAttempts(_ context.Context, id string) ([]ci.GitHubJob, error) {
	return f.jobs[id], nil
}

func (f fakeForgeCI) ExternalCheckRuns(_ context.Context, repo, sha, name string) ([]ci.GitHubCheckRun, string, error) {
	var out []ci.GitHubCheckRun
	for _, run := range f.checks[repo+"@"+sha] {
		if name == "" || run.Name == name {
			out = append(out, run)
		}
	}
	return out, f.app, nil
}

const (
	rcSHA    = "abcdef0123456789abcdef0123456789abcdef01"
	otherSHA = "1111111111111111111111111111111111111111"
)

func forgeProxy(t *testing.T, forge fakeForgeCI) func(path string) *httptest.ResponseRecorder {
	t.Helper()
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
		Namespace: &namespace.Server{CIStore: ci.NewRunStore(10), ExternalCI: forge, Redactor: redact.New(nil)},
		NamespaceScopes: fakeNamespaceScopes{scopes: map[string]model.NamespaceScope{
			"sb-ci": {TaskID: "7", RunID: "run-a", TaskRepo: "acme/widgets", TaskBranch: "grain/task-7", TaskBase: "main",
				MayReadCI: true, AllowedRepos: map[string]bool{"acme/widgets": true}},
			"sb-other": {TaskID: "8", RunID: "run-b", TaskRepo: "acme/other", MayReadCI: true,
				AllowedRepos: map[string]bool{"acme/other": true}},
		}},
		Activity: &fakeActivity{},
	}
	h := gitproxy.NewHandler(proxy)
	return func(path string) *httptest.ResponseRecorder {
		token := "tok-ci"
		if p, ok := strings.CutPrefix(path, "other:"); ok {
			token, path = "tok-other", p
		}
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", basicAuthHeader(token))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
}

func str(s string) *string { return &s }

// qualifiedRC is an rc whose nightly.yml go-long failed once and passed on
// its retry, and whose grain/release-qualification passed -- once from
// grain's App, once (failing) from an Actions workflow posting the same
// name.
func qualifiedRC() fakeForgeCI {
	at := time.Date(2026, 11, 3, 2, 0, 0, 0, time.UTC)
	retry, done := at.Add(time.Hour), at.Add(2*time.Hour)
	nightly := ci.Run{ID: "gha:acme:widgets:900", Provider: ci.ProviderGitHubActions, Repo: "acme/widgets",
		Ref: "v1.2.0-rc.1", Commit: rcSHA, Event: ci.Event("push"), StartedAt: &at, FinishedAt: &done,
		Conclusion: ci.ConclusionSuccess,
		Actions: &ci.ActionsRun{RunID: 900, RunAttempt: 2, Workflow: ".github/workflows/nightly.yml", WorkflowName: "nightly",
			Status: "completed", Conclusion: "success", HeadRepo: "acme/widgets"}}
	// What grain holds only from its record of a run's jobs: no event, no
	// GitHub conclusion. Never handed on as a GitHub verdict.
	recorded := ci.Run{ID: "gha:acme:widgets:901", Provider: ci.ProviderGitHubActions, Repo: "acme/widgets",
		Ref: "v1.2.0-rc.1", Commit: rcSHA, StartedAt: &at, Conclusion: ci.ConclusionFailure,
		Actions: &ci.ActionsRun{RunID: 901, Workflow: ".github/workflows/nightly.yml", Status: "completed"}}
	return fakeForgeCI{
		runs: []ci.Run{nightly, recorded},
		jobs: map[string][]ci.GitHubJob{nightly.ID: {
			{ID: 1, RunID: 900, RunAttempt: 1, WorkflowName: "nightly", HeadSHA: rcSHA, Name: "go, shuffled and repeated",
				Status: "completed", Conclusion: str("failure"), CreatedAt: &at, StartedAt: &at, CompletedAt: &retry},
			{ID: 2, RunID: 900, RunAttempt: 2, WorkflowName: "nightly", HeadSHA: rcSHA, Name: "go, shuffled and repeated",
				Status: "completed", Conclusion: str("success"), CreatedAt: &retry, StartedAt: &retry, CompletedAt: &done},
		}},
		checks: map[string][]ci.GitHubCheckRun{"acme/widgets@" + rcSHA: {
			{ID: 31, Name: "grain/release-qualification", HeadSHA: rcSHA, Status: "completed", Conclusion: str("success"),
				StartedAt: &done, CompletedAt: &done, App: &ci.GitHubApp{Slug: "grain-forge"}},
			{ID: 32, Name: "grain/release-qualification", HeadSHA: rcSHA, Status: "completed", Conclusion: str("failure"),
				StartedAt: &done, CompletedAt: &done, App: &ci.GitHubApp{Slug: "github-actions"}},
			{ID: 33, Name: "go", HeadSHA: rcSHA, Status: "completed", Conclusion: str("success"),
				StartedAt: &done, CompletedAt: &done, App: &ci.GitHubApp{Slug: "github-actions"}},
		}},
		app: "grain-forge",
	}
}

func TestCLICIChecksAnswersGitHubsShape(t *testing.T) {
	get := forgeProxy(t, qualifiedRC())

	rec := get("/cli/ci/checks?repo=acme/widgets&sha=" + rcSHA + "&name=grain/release-qualification&format=github")
	if rec.Code != http.StatusOK {
		t.Fatalf("ci/checks = %d %s", rec.Code, rec.Body)
	}
	var got struct {
		TotalCount int `json:"total_count"`
		CheckRuns  []struct {
			ID         int64   `json:"id"`
			Name       string  `json:"name"`
			HeadSHA    string  `json:"head_sha"`
			Status     string  `json:"status"`
			Conclusion *string `json:"conclusion"`
			App        struct {
				Slug string `json:"slug"`
			} `json:"app"`
		} `json:"check_runs"`
		GrainApp string `json:"grain_app"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("ci/checks is not JSON: %v\n%s", err, rec.Body)
	}
	if got.TotalCount != 2 || len(got.CheckRuns) != 2 || got.GrainApp != "grain-forge" {
		t.Fatalf("ci/checks = %+v, want both grain/release-qualification attempts and grain_app grain-forge", got)
	}
	if c := got.CheckRuns[0]; c.ID != 31 || c.HeadSHA != rcSHA || c.Status != "completed" || c.Conclusion == nil || *c.Conclusion != "success" || c.App.Slug != "grain-forge" {
		t.Errorf("first check run = %+v", c)
	}

	text := get("/cli/ci/checks?sha=" + rcSHA)
	if text.Code != http.StatusOK || !strings.Contains(text.Body.String(), "3 check run(s) on abcdef012345 in acme/widgets:") ||
		!strings.Contains(text.Body.String(), "grain-forge (grain)") {
		t.Errorf("ci/checks text = %d\n%s", text.Code, text.Body)
	}
	for path, want := range map[string]int{
		"/cli/ci/checks?sha=not-a-sha":                                http.StatusBadRequest,
		"/cli/ci/checks?sha=" + rcSHA + "&format=yaml":                http.StatusBadRequest,
		"other:/cli/ci/checks?repo=acme/widgets&sha=" + rcSHA:         http.StatusForbidden,
		"/cli/ci/runs?repo=acme/widgets&format=github&provider=grain": http.StatusBadRequest,
		"/cli/ci/runs?repo=acme/widgets&format=xml":                   http.StatusBadRequest,
	} {
		if rec := get(path); rec.Code != want {
			t.Errorf("%s = %d %s, want %d", path, rec.Code, rec.Body, want)
		}
	}
}

func TestCLICIListFormatGitHub(t *testing.T) {
	get := forgeProxy(t, qualifiedRC())

	rec := get("/cli/ci/runs?repo=acme/widgets&ref=v1.2.0-rc.1&workflow=nightly.yml&format=github")
	if rec.Code != http.StatusOK {
		t.Fatalf("ci/runs format=github = %d %s", rec.Code, rec.Body)
	}
	var runs struct {
		TotalCount   int              `json:"total_count"`
		WorkflowRuns []map[string]any `json:"workflow_runs"`
		Note         string           `json:"grain_note"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &runs); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, rec.Body)
	}
	if runs.TotalCount != 1 || len(runs.WorkflowRuns) != 1 || !strings.Contains(runs.Note, "1 run(s) left out") {
		t.Fatalf("workflow_runs = %+v: want the forge's run alone, and a note on the recorded one", runs)
	}
	wr := runs.WorkflowRuns[0]
	for key, want := range map[string]any{
		"id": float64(900), "path": ".github/workflows/nightly.yml", "head_sha": rcSHA, "head_branch": "v1.2.0-rc.1",
		"event": "push", "status": "completed", "conclusion": "success", "run_started_at": "2026-11-03T02:00:00Z",
	} {
		if wr[key] != want {
			t.Errorf("workflow_runs[0].%s = %v, want %v", key, wr[key], want)
		}
	}
	if repo, _ := wr["repository"].(map[string]any); repo["full_name"] != "acme/widgets" {
		t.Errorf("workflow_runs[0].repository = %v", wr["repository"])
	}

	jobs := get("/cli/ci/runs?repo=acme/widgets&ref=v1.2.0-rc.1&workflow=nightly.yml&format=github-jobs")
	var got struct {
		Jobs []ci.GitHubJob `json:"jobs"`
	}
	if jobs.Code != http.StatusOK || json.Unmarshal(jobs.Body.Bytes(), &got) != nil || len(got.Jobs) != 2 {
		t.Fatalf("ci/runs format=github-jobs = %d %s: want both attempts of go-long", jobs.Code, jobs.Body)
	}
}

// TestCLIGitHubShapesQualifyAnRCInCandidateGate is the point of both: what
// a task with no GitHub token writes from `grain ci list --format
// github-jobs` and `grain ci checks --format github`, unedited, is what
// scripts/candidate-gate.sh check-qualification judges -- the flake on its
// retry passes, only grain's App's check run counts -- and an rc that
// qualified passes.
func TestCLIGitHubShapesQualifyAnRCInCandidateGate(t *testing.T) {
	for _, tool := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	get := forgeProxy(t, qualifiedRC())
	dir := t.TempDir()
	write := func(name, path string) string {
		rec := get(path)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d %s", path, rec.Code, rec.Body)
		}
		file := filepath.Join(dir, name)
		if err := os.WriteFile(file, rec.Body.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		return file
	}
	runs := write("qual-runs.json", "/cli/ci/runs?repo=acme/widgets&ref=v1.2.0-rc.1&workflow=nightly.yml&format=github-jobs")
	checks := write("qual-checks.json", "/cli/ci/checks?repo=acme/widgets&sha="+rcSHA+"&name=grain/release-qualification&format=github")
	var answer struct {
		GrainApp string `json:"grain_app"`
	}
	raw, _ := os.ReadFile(checks)
	_ = json.Unmarshal(raw, &answer)

	gate := func(app string) (string, int) {
		cmd := exec.Command("bash", "../../scripts/candidate-gate.sh", "check-qualification",
			"--rc-tag", "v1.2.0-rc.1", "--rc-sha", rcSHA, "--runs-json", runs, "--checks-json", checks)
		cmd.Env = append(os.Environ(), "CHECK_QUALIFICATION_JOBS=go-long=go, shuffled and repeated",
			"CHECK_QUALIFICATION_IGNORE=", "RELEASE_NEVER_PROMOTE=", "CHECK_QUALIFICATION_APP="+app)
		out, err := cmd.CombinedOutput()
		code := 0
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return string(out), code
	}
	out, code := gate(answer.GrainApp)
	if code != 0 || !strings.Contains(out, "Qualification: PASS v1.2.0-rc.1.") ||
		!strings.Contains(out, "passed on its one retry (failure,success): a flake") ||
		!strings.Contains(out, "IGNORED grain/release-qualification: 1 from github-actions") {
		t.Errorf("check-qualification over grain's answers = exit %d\n%s", code, out)
	}
	// The other App's failing check run is the only one trusted when it is
	// named instead: the gate judges what it is handed, nothing else.
	// A completed failure of the trusted App's check run is confirmed red:
	// grain posts failure only after an item failed its own retry, and
	// never re-runs a completed candidate (docs/test-runs/2026-11-04-release-train.md,
	// finding 1).
	if out, code := gate("github-actions"); code != 1 || !strings.Contains(out, "FAIL grain/release-qualification") {
		t.Errorf("check-qualification trusting the other App = exit %d, want 1 (its failure is confirmed red)\n%s", code, out)
	}
}

// TestCLIGitHubRunsPickAnLKGInCandidateGate: `grain ci list --format
// github` of main, unedited, is what candidate-gate.sh select-lkg reads.
// A pull request's failing run at the same commit is the gate's to drop.
func TestCLIGitHubRunsPickAnLKGInCandidateGate(t *testing.T) {
	for _, tool := range []string{"bash", "jq", "git"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	repo := t.TempDir()
	gitIn := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	gitIn("init", "-q", "-b", "main")
	gitIn("commit", "-q", "--allow-empty", "-m", "one")
	sha := gitIn("rev-parse", "HEAD")

	at := time.Date(2026, 11, 3, 2, 0, 0, 0, time.UTC)
	done := at.Add(time.Hour)
	run := func(id int64, workflow, event, conclusion string) ci.Run {
		return ci.Run{ID: "gha:acme:widgets:" + strings.Repeat("9", int(id)), Provider: ci.ProviderGitHubActions,
			Repo: "acme/widgets", Ref: "main", Commit: sha, Event: ci.Event(event), StartedAt: &at, FinishedAt: &done,
			Actions: &ci.ActionsRun{RunID: id, RunAttempt: 1, Workflow: ".github/workflows/" + workflow, Status: "completed", Conclusion: conclusion}}
	}
	get := forgeProxy(t, fakeForgeCI{runs: []ci.Run{
		run(1, "tests.yml", "push", "success"),
		run(2, "release.yml", "push", "success"),
		run(3, "tests.yml", "pull_request", "failure"),
	}})
	rec := get("/cli/ci/runs?repo=acme/widgets&ref=main&provider=github-actions&format=github&limit=500")
	if rec.Code != http.StatusOK {
		t.Fatalf("ci/runs = %d %s", rec.Code, rec.Body)
	}
	runs := filepath.Join(t.TempDir(), "runs.json")
	if err := os.WriteFile(runs, rec.Body.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	gate, err := filepath.Abs("../../scripts/candidate-gate.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", gate, "select-lkg", "--runs-json", runs, "--ref", "main")
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "LKG "+sha) {
		t.Errorf("select-lkg over grain's answer = %v\n%s", err, out)
	}
}
