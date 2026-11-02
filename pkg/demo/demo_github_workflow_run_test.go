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

package demo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// TestDemoGitHubSimDrivesWorkflowRunScheduleAndReleaseTriage walks the
// `grain demo -github-sim -fake-ci` loop from docs/test-plans/2026-11-01.md
// §6 (docs/test-runs/2026-11-01-github-actions-ci-workflow-run.md, B4):
//
//   - acme/widgets is switched to ci_mode github with a declared
//     github_workflow_run schedule on release.yml;
//   - a failing release run posted to the loopback sim hook (with a
//     matching workflow_run webhook, then seen again by github-ci-tails)
//     fires the schedule once, carrying the run URL and failure tail, and
//     opens release triage;
//   - a fork pull_request run on head branch main changes nothing;
//   - a later green release run posted to the sim hook closes release
//     triage and leaves the failure-only schedule alone.
func TestDemoGitHubSimDrivesWorkflowRunScheduleAndReleaseTriage(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("the simulated GitHub needs git")
	}
	dir := t.TempDir()
	db, err := sqlite.Open(sqlite.DefaultConfig(dir))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}

	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	ghCalls := newDemoGitHubCalls()
	ghCalls.enableLive()
	cfg := ui.Config{
		Actor:              ui.DefaultActor("operator"),
		DefaultTarget:      &repo,
		Capabilities:       append(ui.OfferedCapabilities(), ui.APICapabilities()...),
		GitHubCalls:        ghCalls,
		GitHubPullRequests: demoPullRequests{repo: repo, store: store},
	}
	cfg, err = configureFakeCIDemo(ctx, store, cfg, repo)
	if err != nil {
		t.Fatalf("configureFakeCIDemo: %v", err)
	}
	cfg, bare, err := configureGitHubSimDemo(dir, cfg, repo)
	if err != nil {
		t.Fatalf("configureGitHubSimDemo: %v", err)
	}
	if err := store.PutConfig(ctx, model.Config{TargetRepos: []string{repo.String()}}); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}

	simClient, ok := cfg.GitHubBranches.(github.Client)
	if !ok {
		t.Fatalf("-github-sim wired %T, want github.Client", cfg.GitHubBranches)
	}
	var srv http.Handler = ui.NewServerWithClient(ui.NewClient(cfg, store))
	srv = githubSimDemoHandler(srv, simClient)

	pushDemoDeclarations(t, bare, map[string]string{
		".grain/schedules/release-failures.yaml": `trigger:
  github_workflow_run:
    workflow: release.yml
    conclusions: [failure]
    branches: [main]
`,
	})
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:         repo,
		CIMode:       model.CIModeGitHub,
		Declarations: true,
	}); err != nil {
		t.Fatalf("PutRepoConfig: %v", err)
	}

	// Non-loopback callers are refused by the sim hook.
	nonLoopback := httptest.NewRequest(http.MethodPost, "/api/e2e/github-sim/repos/acme/widgets/actions/runs", strings.NewReader(`{}`))
	nonLoopback.RemoteAddr = "198.51.100.7:4321"
	nonLoopbackRec := httptest.NewRecorder()
	srv.ServeHTTP(nonLoopbackRec, nonLoopback)
	if nonLoopbackRec.Code != http.StatusForbidden {
		t.Fatalf("non-loopback POST .../actions/runs = %d, want 403", nonLoopbackRec.Code)
	}

	postSimRun := func(query, body string) map[string]any {
		t.Helper()
		target := "/api/e2e/github-sim/repos/acme/widgets/actions/runs"
		if query != "" {
			target += "?" + query
		}
		req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:4321"
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("POST %s = %d: %s", target, rec.Code, rec.Body)
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decoding POST %s response: %v (%s)", target, err, rec.Body)
		}
		return out
	}

	now := time.Now().UTC().Truncate(time.Second)
	cycle := func(d time.Duration) {
		t.Helper()
		if err := demoReconcileCycle(ctx, store, simClient, now.Add(d)); err != nil {
			t.Fatalf("demoReconcileCycle(+%s): %v", d, err)
		}
	}

	// Post a fork pull_request run on head branch main (with a webhook) and a
	// failing release push run on main (also with a webhook). Both are in the
	// sim's completed-runs listing when github-ci-tails polls right after
	// github-events.
	postSimRun("webhook=1", `{
		"id": 1301,
		"name": "release",
		"path": ".github/workflows/release.yml",
		"head_branch": "main",
		"head_repo": "mallory/widgets",
		"event": "pull_request",
		"status": "completed",
		"conclusion": "failure",
		"jobs": [{
			"name": "publish-image",
			"conclusion": "failure",
			"failing_step": "ko publish",
			"log": "2026-11-01T08:59:00.0000000Z FORK-RUN-MARKER\n"
		}]
	}`)
	created := postSimRun("", `{
		"id": 1201,
		"name": "release",
		"path": ".github/workflows/release.yml",
		"head_branch": "main",
		"event": "push",
		"status": "completed",
		"conclusion": "failure",
		"webhook": true,
		"jobs": [{
			"name": "publish-image",
			"conclusion": "failure",
			"failing_step": "ko publish",
			"log": "2026-11-01T08:58:00.0000000Z error: pushing image to registry: 500 Internal Server Error\n"
		}]
	}`)
	if created["webhook_queued"] != true {
		t.Fatalf("POST response = %+v, want webhook_queued=true", created)
	}

	cycle(0)
	// A second cycle with unchanged runs revalidates (304) and files nothing more.
	beforeReval := ghCalls.Stats().Callers["github-ci-tails"]
	cycle(time.Minute)
	afterReval := ghCalls.Stats().Callers["github-ci-tails"]
	if afterReval.Revalidated <= beforeReval.Revalidated {
		t.Errorf("unchanged github-ci-tails poll did not record a 304 revalidation: %+v -> %+v", beforeReval, afterReval)
	}

	const schedID = "repo:schedule:acme:widgets:release-failures"
	sched, err := store.GetSchedule(ctx, schedID)
	if err != nil || sched == nil {
		t.Fatalf("GetSchedule(%s) = %+v, %v", schedID, sched, err)
	}

	reviewTasks := func() []model.Task {
		t.Helper()
		tasks, err := store.ListTasks(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var out []model.Task
		for _, task := range tasks {
			if task.Origin.Reason == model.ReasonSchedule {
				out = append(out, task)
			}
			if strings.Contains(task.Body, "FORK-RUN-MARKER") || strings.Contains(task.Body, "1301") {
				t.Fatalf("fork workflow run leaked into task %s: %s", task.ID, task.Body)
			}
		}
		return out
	}

	reviews := reviewTasks()
	if len(reviews) != 1 {
		t.Fatalf("schedule filed %d review tasks, want 1: %+v", len(reviews), reviews)
	}
	for _, want := range []string{
		"actions/runs/1201",
		"publish-image",
		"ko publish",
		"pushing image to registry: 500 Internal Server Error",
	} {
		if !strings.Contains(reviews[0].Body, want) {
			t.Errorf("review task body missing %q:\n%s", want, reviews[0].Body)
		}
	}
	if swr, err := store.GetScheduledWorkflowRun(ctx, schedID); err != nil || swr == nil ||
		swr.RunID != "1201" || swr.ReviewTaskID != reviews[0].ID {
		t.Fatalf("ScheduledWorkflowRun = %+v, %v; want RunID 1201 -> %s", swr, err, reviews[0].ID)
	}

	triageTag := orchestrator.BaseReleaseTriageBranchTag(repo, "main")
	triageIDs, err := store.OpenTasksWithTag(ctx, triageTag)
	if err != nil || len(triageIDs) != 1 {
		t.Fatalf("open release-triage tasks = %v, %v; want 1", triageIDs, err)
	}
	triageTask, err := store.GetTask(ctx, triageIDs[0])
	if err != nil || triageTask == nil || !strings.Contains(triageTask.Body, "pushing image to registry: 500 Internal Server Error") {
		t.Fatalf("release-triage task = %+v, %v; want failure tail from sim client", triageTask, err)
	}
	if rel, err := store.GetBaseBranchRelease(ctx, repo, "main"); err != nil || rel == nil ||
		rel.RunID != "1201" || rel.Conclusion != "failure" || rel.Failures != 1 {
		t.Fatalf("BaseBranchRelease after failing run = %+v, %v", rel, err)
	}

	// Post a green release run WITHOUT webhook: github-ci-tails' completed-runs
	// poll discovers it, closes release triage, and leaves the failure-only
	// schedule at one task.
	postSimRun("", `{
		"id": 1202,
		"name": "release",
		"path": ".github/workflows/release.yml",
		"head_branch": "main",
		"event": "push",
		"status": "completed",
		"conclusion": "success",
		"jobs": [{"name": "publish-image", "conclusion": "success"}]
	}`)
	cycle(3 * time.Minute)

	if rel, err := store.GetBaseBranchRelease(ctx, repo, "main"); err != nil || rel == nil ||
		rel.RunID != "1202" || rel.Conclusion != "success" || rel.Failures != 0 {
		t.Fatalf("BaseBranchRelease after green run = %+v, %v; want run 1202 with Failures=0", rel, err)
	}
	if open, err := store.OpenTasksWithTag(ctx, triageTag); err != nil || len(open) != 0 {
		t.Errorf("open release-triage tasks after green run = %v, %v; want none", open, err)
	}
	if got := reviewTasks(); len(got) != 1 {
		t.Errorf("green run changed failure-only schedule tasks to %d, want 1", len(got))
	}
}
