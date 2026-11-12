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
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// rerunSim is acme/widgets on GitHub Actions with two tasks' branches:
// grain/mine has an older red commit and, at its head bbb222, a red
// Tests run and a green Lint run; grain/theirs has a red run of its own.
func rerunSim(t *testing.T, headAttempt int) (*githubsim.Sim, *ui.Server, *ui.Client) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	sim := githubsim.New("acme", "widgets", filepath.Join(t.TempDir(), "none.git"), "main")
	sim.AddWorkflowRun(githubsim.WorkflowRun{ID: 720, Name: "Tests", Path: ".github/workflows/tests.yml",
		HeadBranch: "grain/theirs", HeadSHA: "ccc111", Event: "pull_request", Conclusion: "failure",
		CreatedAt: now.Add(-time.Minute), Jobs: []githubsim.WorkflowJob{{Name: "go", Conclusion: "failure"}}})
	sim.AddWorkflowRun(githubsim.WorkflowRun{ID: 710, Name: "Tests", Path: ".github/workflows/tests.yml",
		HeadBranch: "grain/mine", HeadSHA: "bbb222", Event: "pull_request", Conclusion: "failure", RunAttempt: headAttempt,
		CreatedAt: now.Add(-2 * time.Minute), Jobs: []githubsim.WorkflowJob{
			{Name: "go", Conclusion: "failure"}, {Name: "lint", Conclusion: "success"}}})
	sim.AddWorkflowRun(githubsim.WorkflowRun{ID: 711, Name: "Lint", Path: ".github/workflows/lint.yml",
		HeadBranch: "grain/mine", HeadSHA: "bbb222", Event: "pull_request", Conclusion: "success",
		CreatedAt: now.Add(-2 * time.Minute), Jobs: []githubsim.WorkflowJob{{Name: "vet", Conclusion: "success"}}})
	sim.AddWorkflowRun(githubsim.WorkflowRun{ID: 700, Name: "Tests", Path: ".github/workflows/tests.yml",
		HeadBranch: "grain/mine", HeadSHA: "aaa333", Event: "pull_request", Conclusion: "failure",
		CreatedAt: now.Add(-time.Hour), Jobs: []githubsim.WorkflowJob{{Name: "go", Conclusion: "failure"}}})
	srv, client, store := actionsServerOver(t, github.NewClient(sim, nil), now)
	ctx := context.Background()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	for id, branch := range map[string]string{"mine": "grain/mine", "theirs": "grain/theirs"} {
		if err := store.PutTask(ctx, model.Task{ID: id, Intent: model.IntentImplement, Title: "task " + id,
			Target: &repo, Branch: branch}); err != nil {
			t.Fatal(err)
		}
	}
	seedAPITask(t, client, "writer", "acme/widgets", "api:ci:write@acme/widgets")
	seedAPITask(t, client, "elsewhere", "acme/other", "api:ci:write@acme/other")
	return sim, srv, client
}

func posted(sim *githubsim.Sim, suffix string) []string {
	var out []string
	for _, c := range sim.Calls {
		if c.Method == "POST" && strings.Contains(c.Path, "/actions/") && strings.HasSuffix(c.Path, suffix) {
			out = append(out, c.Path)
		}
	}
	return out
}

// grain ci retrigger on a ci_mode: github repository re-runs the failed
// jobs of the runs at the task's own head -- POST .../rerun-failed-jobs
// on the red run, nothing on the green one or the older commit's -- and
// --all and run-job reach .../rerun and .../jobs/{id}/rerun.
func TestRetriggerOnGitHubActionsReRunsTheRunsAtTheTasksOwnHead(t *testing.T) {
	sim, _, client := rerunSim(t, 1)
	actions := ui.SandboxCLIActions{Client: client}
	ctx := context.Background()

	res, err := actions.RerunGitHubChecks(ctx, "mine", gitproxy.CIStartRequest{Recheck: true, FailedOnly: true})
	if err != nil {
		t.Fatalf("retrigger: %v", err)
	}
	if got, want := posted(sim, "/rerun-failed-jobs"), []string{"/repos/acme/widgets/actions/runs/710/rerun-failed-jobs"}; !slices.Equal(got, want) {
		t.Fatalf("POSTs = %v, want %v", got, want)
	}
	for _, want := range []string{"gha:acme:widgets:710", "attempt 2 of at most 4", "No commit was pushed", "at most 3 times", "grain ci wait"} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("retrigger said %q, want it to mention %q", res.Text, want)
		}
	}

	if _, err := actions.RerunGitHubChecks(ctx, "mine", gitproxy.CIStartRequest{Recheck: true}); err != nil {
		t.Fatalf("retrigger --all: %v", err)
	}
	if got, want := posted(sim, "/rerun"), []string{"/repos/acme/widgets/actions/runs/710/rerun",
		"/repos/acme/widgets/actions/runs/711/rerun"}; !slices.Equal(got, want) {
		t.Fatalf("--all POSTs = %v, want %v", got, want)
	}

	res, err = actions.RerunGitHubChecks(ctx, "mine", gitproxy.CIStartRequest{Jobs: []string{"go"}, Recheck: true})
	if err != nil {
		t.Fatalf("run-job go: %v", err)
	}
	if !slices.Equal(sim.RerunJobs, []int64{710000}) || !strings.Contains(res.Text, "go") {
		t.Fatalf("run-job go re-ran jobs %v (%q), want job 710000 (go of run 710)", sim.RerunJobs, res.Text)
	}
	if _, err := actions.RerunGitHubChecks(ctx, "mine", gitproxy.CIStartRequest{Jobs: []string{"nope"}}); err == nil ||
		!strings.Contains(err.Error(), "no job \"nope\"") {
		t.Fatalf("run-job nope: %v, want a refusal naming the jobs there are", err)
	}
	if slices.Contains(sim.RerunFailedJobs, 700) || slices.Contains(sim.RerunFailedJobs, 720) {
		t.Fatalf("re-ran %v: an older commit's run, or another task's", sim.RerunFailedJobs)
	}
}

// The task-scoped door re-runs its own branch's head and nothing else:
// another task's commit is refused, with nothing posted. The API route
// that names any run is ci:write on its repository.
func TestRetriggerOnGitHubActionsRefusesAnotherTasksChecks(t *testing.T) {
	sim, srv, client := rerunSim(t, 1)
	actions := ui.SandboxCLIActions{Client: client}

	if _, err := actions.RerunGitHubChecks(context.Background(), "mine",
		gitproxy.CIStartRequest{Commit: "ccc111", FailedOnly: true}); err == nil {
		t.Fatal("retrigger --commit of another task's head was accepted")
	}
	if len(sim.RerunFailedJobs)+len(sim.RerunRuns)+len(sim.RerunJobs) != 0 {
		t.Fatalf("re-runs posted %v %v %v, want none", sim.RerunFailedJobs, sim.RerunRuns, sim.RerunJobs)
	}

	path := "/api/ci/runs/gha:acme:widgets:720/rerun"
	for _, caller := range []string{"reader", "elsewhere", "mine"} {
		if code, body := asAgent(t, srv, caller, http.MethodPost, path, `{}`, "re-running a flake"); code != http.StatusForbidden {
			t.Fatalf("%s re-running another task's run: %d %s, want 403", caller, code, body)
		}
	}
	if len(sim.RerunFailedJobs) != 0 {
		t.Fatalf("re-runs posted %v without ci:write, want none", sim.RerunFailedJobs)
	}
	code, body := asAgent(t, srv, "writer", http.MethodPost, path, `{}`, "re-running a flake")
	if code != http.StatusAccepted || !slices.Equal(sim.RerunFailedJobs, []int64{720}) {
		t.Fatalf("ci:write re-running run 720: %d %s, re-runs %v; want 202 and 720", code, body, sim.RerunFailedJobs)
	}
}

// No run at one head is re-run past MaxActionsRerunsPerHead, and the
// refusal says why -- grain's automatic re-runs count too.
func TestRetriggerOnGitHubActionsIsBoundedPerHead(t *testing.T) {
	sim, srv, client := rerunSim(t, ui.MaxActionsRerunsPerHead+1)
	actions := ui.SandboxCLIActions{Client: client}

	_, err := actions.RerunGitHubChecks(context.Background(), "mine", gitproxy.CIStartRequest{FailedOnly: true})
	if err == nil || !strings.Contains(err.Error(), "re-run 3 times already") || !strings.Contains(err.Error(), "automatic") {
		t.Fatalf("retrigger past the bound: %v, want a refusal saying it has been re-run 3 times", err)
	}
	if code, body := asAgent(t, srv, "writer", http.MethodPost, "/api/ci/runs/gha:acme:widgets:710/rerun", `{"all":true}`,
		"once more"); code != http.StatusBadRequest {
		t.Fatalf("API re-run past the bound: %d %s, want 400", code, body)
	}
	if len(sim.RerunFailedJobs)+len(sim.RerunRuns)+len(sim.RerunJobs) != 0 {
		t.Fatalf("re-runs posted past the bound: %v %v %v", sim.RerunFailedJobs, sim.RerunRuns, sim.RerunJobs)
	}

	// One under the bound is still re-run.
	sim, _, client = rerunSim(t, ui.MaxActionsRerunsPerHead)
	res, err := ui.SandboxCLIActions{Client: client}.RerunGitHubChecks(context.Background(), "mine",
		gitproxy.CIStartRequest{FailedOnly: true})
	if err != nil || !strings.Contains(res.Text, "attempt 4 of at most 4") || !slices.Equal(sim.RerunFailedJobs, []int64{710}) {
		t.Fatalf("the last re-run allowed: %v %q %v", err, res.Text, sim.RerunFailedJobs)
	}
}
