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

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// rerunSim is acme/widgets on GitHub Actions with two tasks' branches:
// grain/mine has an older red commit and, at its head bbb222, a red
// Tests run and a green Lint run; grain/theirs has a red run of its own.
// newer are seeded ahead of them all, as the newest runs.
func rerunSim(t *testing.T, headAttempt int, newer ...githubsim.WorkflowRun) (*githubsim.Sim, *ui.Server, *ui.Client) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	sim := githubsim.New("acme", "widgets", filepath.Join(t.TempDir(), "none.git"), "main")
	for _, run := range newer {
		sim.AddWorkflowRun(run)
	}
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

// With no pull request to pin the head, the branch's newest run is its
// own repository's: a fork's newer run on a branch of the same name is
// left alone.
func TestRetriggerOnGitHubActionsIgnoresAForksBranchOfTheSameName(t *testing.T) {
	sim, _, client := rerunSim(t, 1, githubsim.WorkflowRun{ID: 730, Name: "Tests", Path: ".github/workflows/tests.yml",
		HeadBranch: "grain/mine", HeadSHA: "ddd444", HeadRepo: "stranger/widgets", Event: "pull_request", Conclusion: "failure",
		CreatedAt: time.Now().UTC(), Jobs: []githubsim.WorkflowJob{{Name: "go", Conclusion: "failure"}}})

	if _, err := (ui.SandboxCLIActions{Client: client}).RerunGitHubChecks(context.Background(), "mine",
		gitproxy.CIStartRequest{FailedOnly: true}); err != nil {
		t.Fatalf("retrigger: %v", err)
	}
	if !slices.Equal(sim.RerunFailedJobs, []int64{710}) {
		t.Fatalf("re-ran %v, want only 710 (the branch's own head), not the fork's 730", sim.RerunFailedJobs)
	}
}

// run-job of a job that has finished in a run that has not is refused as
// "wait for the run", with nothing posted: GitHub answers that re-run
// with a 403, which would otherwise read as a credential short of
// actions: write.
func TestRunJobOnGitHubActionsWaitsForTheWholeRun(t *testing.T) {
	sim, _, client := rerunSim(t, 1, githubsim.WorkflowRun{ID: 750, Name: "E2E",
		Path: ".github/workflows/e2e.yml", HeadBranch: "grain/mine", HeadSHA: "bbb222", Event: "pull_request",
		Status: "in_progress", CreatedAt: time.Now().UTC(), Jobs: []githubsim.WorkflowJob{
			{Name: "e2e", Conclusion: "failure"}, {Name: "slow", Status: "in_progress"}}})

	_, err := (ui.SandboxCLIActions{Client: client}).RerunGitHubChecks(context.Background(), "mine",
		gitproxy.CIStartRequest{Jobs: []string{"e2e"}})
	if err == nil || !strings.Contains(err.Error(), "its run is still in_progress") || strings.Contains(err.Error(), "actions: write") {
		t.Fatalf("run-job e2e of a run still going: %v, want a refusal to wait for the run", err)
	}
	if len(sim.RerunJobs) != 0 {
		t.Fatalf("re-ran jobs %v of a run still going", sim.RerunJobs)
	}
}

// The bound is each run's own: a run at the head that has spent its
// re-runs is skipped, and the other red runs at that head are still
// re-run.
func TestRetriggerOnGitHubActionsBoundsEachRunOnItsOwn(t *testing.T) {
	sim, _, client := rerunSim(t, ui.MaxActionsRerunsPerHead+1, githubsim.WorkflowRun{ID: 740, Name: "E2E",
		Path: ".github/workflows/e2e.yml", HeadBranch: "grain/mine", HeadSHA: "bbb222", Event: "pull_request",
		Conclusion: "failure", CreatedAt: time.Now().UTC(), Jobs: []githubsim.WorkflowJob{{Name: "e2e", Conclusion: "failure"}}})

	res, err := ui.SandboxCLIActions{Client: client}.RerunGitHubChecks(context.Background(), "mine",
		gitproxy.CIStartRequest{FailedOnly: true})
	if err != nil {
		t.Fatalf("retrigger with one run past the bound: %v", err)
	}
	if !slices.Equal(sim.RerunFailedJobs, []int64{740}) {
		t.Fatalf("re-ran %v, want only 740 (710 has spent its re-runs)", sim.RerunFailedJobs)
	}
	if !strings.Contains(res.Text, "skipped gha:acme:widgets:710") {
		t.Fatalf("retrigger said %q, want it to say 710 was skipped", res.Text)
	}

	if _, err := (ui.SandboxCLIActions{Client: client}).RerunGitHubChecks(context.Background(), "mine",
		gitproxy.CIStartRequest{Jobs: []string{"go"}}); err == nil || !strings.Contains(err.Error(), "re-run 3 times already") {
		t.Fatalf("run-job go of the spent run: %v, want the bound's refusal", err)
	}
	if len(sim.RerunJobs) != 0 {
		t.Fatalf("re-ran jobs %v of a spent run", sim.RerunJobs)
	}
}

// Re-running a workflow_dispatch, release, pull_request_target or
// deployment run on GitHub Actions re-runs it with its release secrets,
// base-branch tokens and dispatch inputs intact: the sandbox door ignores
// such runs, and POST /api/ci/runs/{id}/rerun holds them to a person
// (matching TestCIRunReleaseSecretsAreAPersonsToFire).
func TestRetriggerOnGitHubActionsHoldsDispatchAndReleaseRunsToAPerson(t *testing.T) {
	now := time.Now().UTC()
	sim, srv, client := rerunSim(t, 1,
		githubsim.WorkflowRun{ID: 760, Name: "Promote", Path: ".github/workflows/promote.yml",
			HeadBranch: "grain/mine", HeadSHA: "bbb222", Event: "workflow_dispatch", Conclusion: "failure",
			CreatedAt: now, Jobs: []githubsim.WorkflowJob{{Name: "promote", Conclusion: "failure"}}},
		githubsim.WorkflowRun{ID: 761, Name: "Release", Path: ".github/workflows/release.yml",
			HeadBranch: "grain/mine", HeadSHA: "bbb222", Event: "release", Conclusion: "failure",
			CreatedAt: now, Jobs: []githubsim.WorkflowJob{{Name: "publish", Conclusion: "failure"}}},
		githubsim.WorkflowRun{ID: 762, Name: "PR Target", Path: ".github/workflows/target.yml",
			HeadBranch: "grain/mine", HeadSHA: "bbb222", Event: "pull_request_target", Conclusion: "failure",
			CreatedAt: now, Jobs: []githubsim.WorkflowJob{{Name: "label", Conclusion: "failure"}}},
	)

	if _, err := (ui.SandboxCLIActions{Client: client}).RerunGitHubChecks(context.Background(), "mine",
		gitproxy.CIStartRequest{FailedOnly: true}); err != nil {
		t.Fatalf("sandbox retrigger: %v", err)
	}
	if !slices.Equal(sim.RerunFailedJobs, []int64{710}) {
		t.Fatalf("sandbox re-ran %v, want only 710 (pull_request), never 760/761/762", sim.RerunFailedJobs)
	}

	sim.RerunFailedJobs = nil
	for _, id := range []string{"gha:acme:widgets:760", "gha:acme:widgets:761", "gha:acme:widgets:762"} {
		code, body := asAgent(t, srv, "writer", http.MethodPost, "/api/ci/runs/"+id+"/rerun", `{}`, "re-running")
		if code != http.StatusForbidden {
			t.Fatalf("ci:write agent re-running %s: %d %s, want 403", id, code, body)
		}
	}
	if len(sim.RerunFailedJobs)+len(sim.RerunRuns)+len(sim.RerunJobs) != 0 {
		t.Fatalf("agent re-ran privileged runs: %v %v %v", sim.RerunFailedJobs, sim.RerunRuns, sim.RerunJobs)
	}

	rec := doAs(t, srv, "carol", http.MethodPost, "/api/ci/runs/gha:acme:widgets:760/rerun", `{}`)
	if rec.Code != http.StatusAccepted || !slices.Equal(sim.RerunFailedJobs, []int64{760}) {
		t.Fatalf("person re-running 760: %d %s, re-runs %v; want 202 and [760]", rec.Code, rec.Body, sim.RerunFailedJobs)
	}
}

// An in-place review may re-run the reviewed task's checks only while its
// target and branch match the reviewed task's and while the review itself
// is still open; and even with an open pull request pinning the head SHA,
// a fork's run at the same SHA on a same-named branch is ignored.
func TestRetriggerOnGitHubActionsChecksInPlaceReviewClosedCallerAndPinnedPRFork(t *testing.T) {
	now := time.Now().UTC()
	sim, srv, client := rerunSim(t, 1, githubsim.WorkflowRun{ID: 770, Name: "Tests", Path: ".github/workflows/tests.yml",
		HeadBranch: "grain/mine", HeadSHA: "bbb222", HeadRepo: "stranger/widgets", Event: "pull_request", Conclusion: "failure",
		CreatedAt: now, Jobs: []githubsim.WorkflowJob{{Name: "go", Conclusion: "failure"}}})
	ctx := context.Background()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	other := model.RepoRef{Owner: "acme", Name: "other"}

	client.Config.GitHubPullRequests = prHead("bbb222")
	if err := client.Store.UpdateTask(ctx, "mine", func(tk *model.Task) error {
		tk.Links = []model.Link{{Kind: model.LinkFixes, Target: "acme/widgets#12"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Mismatched branch or repo on LinkPushesReviewedBranch is refused.
	for _, bad := range []model.Task{
		{ID: "rev-branch", Intent: model.IntentImplement, Title: "rev", Target: &repo, Branch: "grain/rev-branch",
			Origin: model.Origin{Reason: model.ReasonReview}, Links: []model.Link{{Kind: model.LinkPushesReviewedBranch, Target: "theirs"}}},
		{ID: "rev-repo", Intent: model.IntentImplement, Title: "rev", Target: &other, Branch: "grain/theirs",
			Origin: model.Origin{Reason: model.ReasonReview}, Links: []model.Link{{Kind: model.LinkPushesReviewedBranch, Target: "theirs"}}},
	} {
		if err := client.Store.PutTask(ctx, bad); err != nil {
			t.Fatal(err)
		}
		if _, err := (ui.SandboxCLIActions{Client: client}).RerunGitHubChecks(ctx, bad.ID,
			gitproxy.CIStartRequest{FailedOnly: true}); err == nil {
			t.Fatalf("%s with mismatched LinkPushesReviewedBranch was allowed to re-run", bad.ID)
		}
	}
	if len(sim.RerunFailedJobs) != 0 {
		t.Fatalf("mismatched reviews re-ran %v", sim.RerunFailedJobs)
	}

	// A matching open in-place review re-runs mine's own 710, ignoring the
	// stranger fork's 770 even though PR #12 pins HeadSHA bbb222.
	rev := model.Task{ID: "rev-ok", Intent: model.IntentImplement, Title: "rev", Target: &repo, Branch: "grain/mine",
		Origin: model.Origin{Reason: model.ReasonReview}, Links: []model.Link{{Kind: model.LinkPushesReviewedBranch, Target: "mine"}}}
	if err := client.Store.PutTask(ctx, rev); err != nil {
		t.Fatal(err)
	}
	if _, err := (ui.SandboxCLIActions{Client: client}).RerunGitHubChecks(ctx, "rev-ok",
		gitproxy.CIStartRequest{FailedOnly: true}); err != nil {
		t.Fatalf("open in-place review retrigger: %v", err)
	}
	if !slices.Equal(sim.RerunFailedJobs, []int64{710}) {
		t.Fatalf("open in-place review re-ran %v, want [710] (not stranger fork's 770)", sim.RerunFailedJobs)
	}

	// Once closed (superseded), the review's sandbox cannot spend another attempt.
	closedAt := now
	if err := client.Store.ObserveField(ctx, "rev-ok", now, func(o *model.Observation) {
		o.ClosedAt = &closedAt
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := (ui.SandboxCLIActions{Client: client}).RerunGitHubChecks(ctx, "rev-ok",
		gitproxy.CIStartRequest{FailedOnly: true}); err == nil {
		t.Fatal("closed in-place review was allowed to re-run")
	}

	// Short/non-hex commit prefix is rejected on the API route too.
	if code, body := asAgent(t, srv, "writer", http.MethodPost, "/api/ci/runs/gha:acme:widgets:710/rerun",
		`{"commit":"b"}`, "short prefix"); code != http.StatusBadRequest {
		t.Fatalf("1-char commit prefix: %d %s, want 400", code, body)
	}
}

// Re-running a completed run invalidates its cached finished detail so
// GET /api/ci/runs/{id} immediately serves the new attempt, and multi-job
// --jobs requests either map to a supported GitHub endpoint or fail cleanly
// before calling the forge.
func TestRetriggerOnGitHubActionsEvictsCacheAndValidatesMultiJobSelection(t *testing.T) {
	now := time.Now().UTC()
	sim, srv, client := rerunSim(t, 1, githubsim.WorkflowRun{ID: 780, Name: "Matrix", Path: ".github/workflows/matrix.yml",
		HeadBranch: "grain/mine", HeadSHA: "bbb222", Event: "pull_request", Conclusion: "failure",
		CreatedAt: now, Jobs: []githubsim.WorkflowJob{
			{Name: "unit", Conclusion: "failure"},
			{Name: "e2e", Conclusion: "failure"},
			{Name: "docs", Conclusion: "success"},
		}})
	actions := ui.SandboxCLIActions{Client: client}
	ctx := context.Background()

	// Warm the 10-minute finished-run cache entry for 710.
	before := decode[ci.Run](t, doAs(t, srv, "carol", http.MethodGet, "/api/ci/runs/gha:acme:widgets:710", ""))
	if before.Actions == nil || before.Actions.RunAttempt != 1 || before.Actions.Status != "completed" {
		t.Fatalf("before = %+v", before.Actions)
	}

	// Duplicate job names deduplicate to one RerunWorkflowJob call.
	if _, err := actions.RerunGitHubChecks(ctx, "mine", gitproxy.CIStartRequest{Jobs: []string{"go", "go"}}); err != nil {
		t.Fatalf("run-job go,go: %v", err)
	}
	if len(sim.RerunJobs) != 1 {
		t.Fatalf("duplicate job names posted %v, want 1 job rerun", sim.RerunJobs)
	}

	// Simulate GitHub starting attempt 2 of 710; GET /api/ci/runs/{id} must
	// see attempt 2 immediately rather than serving attempt 1 from cache.
	sim.AddWorkflowRun(githubsim.WorkflowRun{ID: 710, Name: "Tests", Path: ".github/workflows/tests.yml",
		HeadBranch: "grain/mine", HeadSHA: "bbb222", Event: "pull_request", Status: "in_progress", RunAttempt: 2,
		CreatedAt: now, Jobs: []githubsim.WorkflowJob{{Name: "go", Status: "in_progress"}, {Name: "lint", Conclusion: "success"}}})
	after := decode[ci.Run](t, doAs(t, srv, "carol", http.MethodGet, "/api/ci/runs/gha:acme:widgets:710", ""))
	if after.Actions == nil || after.Actions.RunAttempt != 2 || after.Actions.Status != "in_progress" {
		t.Fatalf("after re-run, GET /api/ci/runs served stale cache: %+v, want attempt 2 in_progress", after.Actions)
	}

	// Partial subset of >=2 jobs (1 failed + 1 passed out of 3 jobs) in one
	// workflow run is refused before posting anything to GitHub.
	sim.RerunJobs, sim.RerunFailedJobs, sim.RerunRuns = nil, nil, nil
	if _, err := actions.RerunGitHubChecks(ctx, "mine", gitproxy.CIStartRequest{Jobs: []string{"unit", "docs"}}); err == nil {
		t.Fatal("partial multi-job subset in 3-job workflow was accepted")
	}
	if len(sim.RerunJobs)+len(sim.RerunFailedJobs)+len(sim.RerunRuns) != 0 {
		t.Fatalf("refused multi-job subset still posted %v %v %v", sim.RerunJobs, sim.RerunFailedJobs, sim.RerunRuns)
	}

	// Naming the exact failed jobs of 780 uses rerun-failed-jobs cleanly.
	if _, err := actions.RerunGitHubChecks(ctx, "mine", gitproxy.CIStartRequest{Jobs: []string{"unit", "e2e"}}); err != nil {
		t.Fatalf("re-running exact failed set unit,e2e: %v", err)
	}
	if !slices.Equal(sim.RerunFailedJobs, []int64{780}) {
		t.Fatalf("exact failed set posted RerunFailedJobs=%v, want [780]", sim.RerunFailedJobs)
	}
}
