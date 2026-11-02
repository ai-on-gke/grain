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

// This file walks docs/test-plans/2026-11-01.md §6's "not proved" items
// 1, 2, 4 and 6 end to end (docs/test-runs/2026-11-01-github-actions-ci-workflow-run.md):
// a repository in ci_mode github, a .grain/schedules entry with
// trigger.github_workflow_run read out of the repository's own tree, a
// concluded failing release run delivered as a webhook (twice) and seen
// again by the completed-runs poll, the schedule firing once, release
// triage filing and then closing on a later success, a fork pull_request
// run whose head branch is main changing nothing, and the filed review
// task reading the GitHub run's log through its own `grain mcpserver`.
//
// What is real: a SQLite store, the CI service the daemon builds
// (newCIWorld), the orchestrator's own "github-events" and
// "github-ci-tails" reconcilers, github.RealTransport -- its ETag cache
// and the process-wide caller ledger Health's GitHub budget panel reads --
// talking HTTP to a githubsim (githubHostServer), webhook payloads parsed
// by ghwebhook.ParsePayload, and a real `grain mcpserver`. What is stood
// in for is the webhook queue (a slice, the way pkg/orchestrator's own
// tests feed one) and the declarations reader (repodecl.Load over the
// seed checkout rather than the "declarations" reconciler's clone).
package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/ghwebhook"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/repodecl"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// sliceEventSource is the webhook queue: every event not yet acked.
type sliceEventSource struct {
	events []ghwebhook.Event
	acked  uint64
}

func (s *sliceEventSource) Pending() ([]ghwebhook.Event, error) {
	var out []ghwebhook.Event
	for _, ev := range s.events {
		if ev.Seq > s.acked {
			out = append(out, ev)
		}
	}
	return out, nil
}

func (s *sliceEventSource) Ack(seq uint64) error {
	if seq > s.acked {
		s.acked = seq
	}
	return nil
}

// answerRecorder is githubsim with every answer's status kept, so the
// walkthrough can see a 304 really crossed the wire.
type answerRecorder struct {
	sim *githubsim.Sim
	mu  sync.Mutex
	got []string
}

func (r *answerRecorder) Request(method, path string, headers map[string]string, body []byte) (github.ApiResponse, error) {
	resp, err := r.sim.Request(method, path, headers, body)
	r.mu.Lock()
	r.got = append(r.got, fmt.Sprintf("%d %s %s", resp.Status, method, path))
	r.mu.Unlock()
	return resp, err
}

func (r *answerRecorder) answers() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.got...)
}

func workflowRunWebhook(t *testing.T, seq uint64, r githubsim.WorkflowRun, started, done time.Time) ghwebhook.Event {
	t.Helper()
	headRepo := r.HeadRepo
	if headRepo == "" {
		headRepo = "acme/widgets"
	}
	payload := fmt.Sprintf(`{
		"action": "completed",
		"repository": {"full_name": "acme/widgets"},
		"sender": {"login": "github-actions[bot]", "type": "Bot"},
		"workflow_run": {
			"id": %d, "name": %q, "path": %q, "head_branch": %q, "head_sha": %q,
			"head_repository": {"full_name": %q},
			"event": %q, "status": %q, "conclusion": %q, "run_attempt": %d,
			"html_url": "https://github.com/acme/widgets/actions/runs/%d",
			"run_started_at": %q, "updated_at": %q
		}
	}`, r.ID, r.Name, r.Path, r.HeadBranch, r.HeadSHA, headRepo, r.Event, r.Status, r.Conclusion,
		r.RunAttempt, r.ID, started.Format(time.RFC3339), done.Format(time.RFC3339))
	ev, err := ghwebhook.ParsePayload("workflow_run", fmt.Sprintf("delivery-%d", seq), []byte(payload))
	if err != nil {
		t.Fatalf("ParsePayload: %v", err)
	}
	ev.Seq = seq
	return ev
}

func runNamedReconciler(t *testing.T, w *ciWorld, deps orchestrator.Deps, name string, now time.Time) {
	t.Helper()
	for _, r := range orchestrator.Reconcilers() {
		if r.Name == name {
			// What RunCycle does: every reconciler's GitHub spend is
			// charged to its own name on the caller ledger.
			deps.Client = github.WithCaller(deps.Client, r.Name)
			if err := r.Reconcile(w.ctx, deps, now); err != nil {
				t.Fatalf("%s reconciler: %v", name, err)
			}
			return
		}
	}
	t.Fatalf("no %q reconciler in the cycle", name)
}

func TestGitHubWorkflowRunScheduleLoopOverSim(t *testing.T) {
	w := newCIWorld(t, recheckWorkflow, model.Limits{})
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	ctx := w.ctx

	// GitHub Actions is this repository's CI, and main is a watched base
	// branch, which is what release triage keys on.
	if err := w.store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CIMode: model.CIModeGitHub}); err != nil {
		t.Fatal(err)
	}
	if err := w.store.PutBaseBranchCI(ctx, model.BaseBranchCI{Repo: repo, Branch: "main"}); err != nil {
		t.Fatal(err)
	}

	// The schedule lives in the repository's own tree, as docs/schedules.md
	// documents it.
	mustWriteFile(t, filepath.Join(w.seed, ".grain", "schedules", "release-failures.yaml"), `
trigger:
  github_workflow_run:
    workflow: release.yml
    conclusions: [failure]
    branches: [main]
`)
	run(t, w.seed, "git", "checkout", "-q", "main")
	run(t, w.seed, "git", "add", "-A")
	run(t, w.seed, "git", "commit", "-q", "-m", "release failure schedule")
	run(t, w.seed, "git", "push", "-q", "origin", "main")
	decls, err := repodecl.Load(os.DirFS(w.seed), repo)
	if err != nil {
		t.Fatalf("repodecl.Load: %v", err)
	}
	if len(decls.Schedules) != 1 {
		t.Fatalf("loaded %d schedules, want 1", len(decls.Schedules))
	}
	sched := decls.Schedules[0].Schedule
	if sched.Recurrence.Kind != model.RecurrenceGitHubWorkflowRun {
		t.Fatalf("recurrence = %+v, want github_workflow_run", sched.Recurrence)
	}
	if err := w.store.PutSchedule(ctx, sched); err != nil {
		t.Fatal(err)
	}

	// GitHub over HTTP, so the ETag cache and the caller ledger are the
	// real ones.
	recorder := &answerRecorder{sim: w.sim}
	transport := github.NewRealTransport(githubHostServer(t, recorder, filepath.Dir(w.bare)))
	transport.UseTLS = false
	client := github.NewClient(transport, nil)

	now := time.Now().UTC().Truncate(time.Second)
	started, done := now.Add(-10*time.Minute), now.Add(-2*time.Minute)
	release := githubsim.WorkflowRun{
		ID: 1201, Name: "release", Path: ".github/workflows/release.yml",
		HeadBranch: "main", HeadSHA: "deadbeef12345678", Event: "push",
		Status: "completed", Conclusion: "failure", RunAttempt: 1,
		Jobs: []githubsim.WorkflowJob{{
			Name: "publish-image", Conclusion: "failure", FailingStep: "ko publish",
			Log: "2026-11-01T08:58:00.0000000Z error: pushing image to registry: 500 Internal Server Error\n",
		}},
	}
	// A fork's pull request whose head branch happens to be called main,
	// failing the same workflow: none of it is the base branch.
	fork := githubsim.WorkflowRun{
		ID: 1301, Name: "release", Path: ".github/workflows/release.yml",
		HeadBranch: "main", HeadSHA: "f0f0f0f0f0f0f0f0", HeadRepo: "mallory/widgets", Event: "pull_request",
		Status: "completed", Conclusion: "failure", RunAttempt: 1,
		Jobs: []githubsim.WorkflowJob{{
			Name: "publish-image", Conclusion: "failure", FailingStep: "ko publish",
			Log: "2026-11-01T08:59:00.0000000Z FORK-RUN-MARKER\n",
		}},
	}
	// And a pull_request run from the base repository itself, head branch
	// main: a pull request event is never the base branch either.
	prRun := fork
	prRun.ID, prRun.HeadRepo, prRun.HeadSHA = 1302, "", "e0e0e0e0e0e0e0e0"
	w.sim.WorkflowRuns = []githubsim.WorkflowRun{release, fork, prRun}

	source := &sliceEventSource{events: []ghwebhook.Event{
		workflowRunWebhook(t, 1, fork, started, done),
		workflowRunWebhook(t, 2, release, started, done),
		workflowRunWebhook(t, 3, release, started, done), // redelivery
		workflowRunWebhook(t, 4, prRun, started, done),
	}}
	deps := orchestrator.Deps{Store: w.store, Client: client, GitHubEvents: source, CI: w.service}

	scheduleTasks := func() []model.Task {
		t.Helper()
		tasks, err := w.store.ListTasks(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var out []model.Task
		for _, task := range tasks {
			if task.Origin.Reason == model.ReasonSchedule {
				out = append(out, task)
			}
		}
		return out
	}
	assertNoForkEffect := func() {
		t.Helper()
		tasks, _ := w.store.ListTasks(ctx)
		for _, task := range tasks {
			if strings.Contains(task.Body, "1301") || strings.Contains(task.Body, "1302") || strings.Contains(task.Body, "FORK-RUN-MARKER") {
				t.Errorf("task %s (%q) carries the fork's run:\n%s", task.ID, task.Title, task.Body)
			}
		}
		if rel, _ := w.store.GetBaseBranchRelease(ctx, repo, "main"); rel != nil && (rel.RunID == "1301" || rel.RunID == "1302") {
			t.Errorf("the fork's run became main's release: %+v", rel)
		}
	}

	// 1. Webhooks: the fork's run, the release run twice, the pull request's.
	runNamedReconciler(t, w, deps, "github-events", now)
	if source.acked != 4 {
		t.Fatalf("acked through %d, want 4", source.acked)
	}
	// 2. The poll sees the same runs again.
	runNamedReconciler(t, w, deps, "github-ci-tails", now.Add(time.Minute))

	filed := scheduleTasks()
	if len(filed) != 1 {
		t.Fatalf("schedule filed %d tasks across two webhooks and a poll, want 1: %+v", len(filed), filed)
	}
	review := filed[0]
	for _, want := range []string{"actions/runs/1201", "publish-image", "ko publish", "pushing image to registry: 500 Internal Server Error"} {
		if !strings.Contains(review.Body, want) {
			t.Errorf("review task body missing %q:\n%s", want, review.Body)
		}
	}
	swr, err := w.store.GetScheduledWorkflowRun(ctx, sched.ID)
	if err != nil || swr == nil || swr.RunID != "1201" || swr.ReviewTaskID != review.ID {
		t.Fatalf("scheduled_workflow_run = %+v, %v; want run 1201 -> %s", swr, err, review.ID)
	}
	t.Logf("schedule %s filed %s (%q) once for run 1201", sched.ID, review.ID, review.Title)

	triage, err := w.store.OpenTasksWithTag(ctx, orchestrator.BaseReleaseTriageBranchTag(repo, "main"))
	if err != nil || len(triage) != 1 {
		t.Fatalf("open release-triage tasks = %v, %v; want 1", triage, err)
	}
	if rel, _ := w.store.GetBaseBranchRelease(ctx, repo, "main"); rel == nil || rel.RunID != "1201" || rel.Conclusion != "failure" {
		t.Fatalf("main's release = %+v, want run 1201 failing", rel)
	}
	assertNoForkEffect()

	// Composing a task for a repository grain CI does not judge strips
	// workflowReviewTemplate's grain-ci grant (model.applyGrainCIGrant,
	// so merge gating is untouched) and attaches a repo-scoped
	// api:ci-read@acme/widgets grant instead, so the reviewer of a GitHub
	// workflow run can read the synthesized ci.Run log it was asked to
	// inspect (`grain ci log 1201` / get_ci_log).
	var hasCIRead bool
	for _, g := range review.Grants {
		if g.Capability == "grain-ci" {
			t.Fatalf("review task holds grain-ci in a ci_mode github repo: %+v", review.Grants)
		}
		if g.Capability == "api:ci-read@acme/widgets" && g.Via == model.GrantByGrain {
			hasCIRead = true
		}
	}
	if !hasCIRead {
		t.Fatalf("review task grants = %+v, want api:ci-read@acme/widgets (GrantByGrain)", review.Grants)
	}
	// 4. With its composed grant, the review task reads the GitHub run's
	// log from its sandbox: the synthesized ci.Run behind `grain ci log 1201`.
	agent := startMCPServer(t, w.bin, "-sandbox-root", t.TempDir(), "-server", w.daemonURL,
		"-task", review.ID, "-pr-repo", "acme/widgets", "-pr-branch", review.BranchName())
	text, isErr := callTool(t, agent, "get_ci_log", map[string]any{"run_id": "1201"})
	if isErr || !strings.Contains(text, "pushing image to registry: 500 Internal Server Error") {
		t.Errorf("get_ci_log {run_id: 1201} = %v:\n%s", isErr, text)
	}
	if runsText, runsErr := callTool(t, agent, "list_ci_runs", map[string]any{}); runsErr {
		t.Errorf("list_ci_runs {} = %v:\n%s", runsErr, runsText)
	}
	if otherText, otherErr := callTool(t, agent, "list_ci_runs", map[string]any{"repo": "acme/other"}); !otherErr {
		t.Errorf("list_ci_runs {repo: acme/other} succeeded with repo-scoped grant:\n%s", otherText)
	}

	// 6. An unchanged poll is a 304: revalidated, so free.
	answersBefore := len(recorder.answers())
	before := github.Callers()["github-ci-tails"]
	runNamedReconciler(t, w, deps, "github-ci-tails", now.Add(2*time.Minute))
	after := github.Callers()["github-ci-tails"]
	if after.Sent == before.Sent {
		t.Errorf("the unchanged poll sent nothing: %+v -> %+v", before, after)
	}
	var saw304 bool
	for _, a := range recorder.answers()[answersBefore:] {
		if strings.Contains(a, "/actions/runs?") && strings.HasPrefix(a, "304 ") {
			saw304 = true
		}
	}
	if !saw304 {
		t.Errorf("the unchanged completed-runs poll was not answered 304: %q", recorder.answers()[answersBefore:])
	}
	if paid, was := after.Sent-after.Revalidated, before.Sent-before.Revalidated; paid != was {
		t.Errorf("the unchanged poll cost %d requests, want 0 (304s are free): %+v -> %+v", paid-was, before, after)
	}
	if len(scheduleTasks()) != 1 {
		t.Fatalf("an unchanged poll filed again")
	}

	// A later green release closes triage, and a success is not one of
	// the schedule's conclusions.
	w.sim.WorkflowRuns = append(w.sim.WorkflowRuns, githubsim.WorkflowRun{
		ID: 1202, Name: "release", Path: ".github/workflows/release.yml",
		HeadBranch: "main", HeadSHA: "feedface87654321", Event: "push",
		Status: "completed", Conclusion: "success", RunAttempt: 1,
		Jobs: []githubsim.WorkflowJob{{Name: "publish-image", Conclusion: "success"}},
	})
	runNamedReconciler(t, w, deps, "github-ci-tails", now.Add(5*time.Minute))
	if rel, _ := w.store.GetBaseBranchRelease(ctx, repo, "main"); rel == nil || rel.RunID != "1202" || rel.Failures != 0 {
		t.Fatalf("main's release after green = %+v, want run 1202 with no failures", rel)
	}
	if open, _ := w.store.OpenTasksWithTag(ctx, orchestrator.BaseReleaseTriageBranchTag(repo, "main")); len(open) != 0 {
		t.Errorf("release triage still open after a green release: %v", open)
	}
	if len(scheduleTasks()) != 1 {
		t.Errorf("a successful run fired a failure-only schedule")
	}
	assertNoForkEffect()
}

// heldWorkflow is one job that runs until the walkthrough removes its
// hold file: a Grain CI run that is still in flight when the repository's
// CI mode changes under it.
const heldWorkflow = `name: held
on: push
jobs:
  held:
    steps:
      - name: wait for the hold
        run: |
          while [ -f "$E2E_HOLD" ]; do sleep 0.05; done
          echo released
`

// §6 "not proved" item 3: switching a repository from Grain CI to GitHub
// mid-run cancels the in-flight run with a system reason, frees its
// sandbox and moves `grain ci wait` to GitHub's checks; switching back
// starts Grain CI again.
func TestCIModeSwitchMidRunStandsGrainCIDownAndBack(t *testing.T) {
	w := newCIWorld(t, heldWorkflow, model.Limits{})
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	ctx := w.ctx
	hold := filepath.Join(w.sandboxes.dir, "hold")
	mustWriteFile(t, hold, "")
	t.Cleanup(func() { os.Remove(hold) })

	task := w.task("switch-1")
	actions := ui.SandboxCLIActions{Client: ui.NewClient(ui.Config{CI: w.service}, w.store)}
	usesGitHub := func() bool {
		t.Helper()
		ok, err := actions.UsesGitHubChecks(ctx, task.ID)
		if err != nil {
			t.Fatalf("UsesGitHubChecks: %v", err)
		}
		return ok
	}
	jobDirs := func() []string {
		dirs, _ := filepath.Glob(filepath.Join(w.sandboxes.dir, "job-*"))
		return dirs
	}
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	w.reconcile()
	first := w.verdict(task.ID)
	if first.RunID == "" || first.Done() {
		t.Fatalf("Grain CI verdict after the first reconcile = %+v, want a live run", first)
	}
	waitFor("the held job to take a sandbox", func() bool { return len(jobDirs()) == 1 })
	if usesGitHub() {
		t.Fatal("grain ci wait reads GitHub checks while the repository is on Grain CI")
	}

	// Grain -> GitHub, mid-run.
	if err := w.store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CIMode: model.CIModeGitHub}); err != nil {
		t.Fatal(err)
	}
	w.reconcile()
	cancelled := w.settle(first.RunID)
	if cancelled.Conclusion != ci.ConclusionCancelled || cancelled.CancelledBy != ci.CancelBySystem ||
		!strings.Contains(cancelled.CancelReason, "switched to GitHub CI") {
		t.Fatalf("run %s after the switch = %s by %q (%q), want cancelled by the system for the switch",
			first.RunID, cancelled.Conclusion, cancelled.CancelledBy, cancelled.CancelReason)
	}
	if v := w.verdict(task.ID); v.Conclusion != string(ci.ConclusionCancelled) || !strings.Contains(v.Detail, "switched to GitHub CI") {
		t.Errorf("task verdict after the switch = %+v", v)
	}
	waitFor("the cancelled job's sandbox to be released", func() bool { return len(jobDirs()) == 0 })
	if !usesGitHub() {
		t.Error("grain ci wait still waits on Grain CI after the switch to GitHub")
	}
	w.reconcile()
	if runs := w.runsOn(task.BranchName()); len(runs) != 1 {
		t.Errorf("Grain CI started again on a GitHub repository: %d runs on the branch", len(runs))
	}
	t.Logf("run %s: %s by system: %s", first.RunID, cancelled.Conclusion, cancelled.CancelReason)

	// GitHub -> Grain: the system's cancellation is replaced, once
	// orchestrator's ciRetryDelay (5m) has passed since it.
	if err := w.store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CIMode: model.CIModeGrain}); err != nil {
		t.Fatal(err)
	}
	if usesGitHub() {
		t.Error("grain ci wait still reads GitHub checks after switching back to Grain CI")
	}
	os.Remove(hold)
	runNamedReconciler(t, w, orchestrator.Deps{Store: w.store, Client: w.client, CI: w.service}, "ci", time.Now().Add(time.Minute))
	if v := w.verdict(task.ID); v.RunID != first.RunID {
		t.Errorf("Grain CI replaced the cancelled run inside its retry delay: %+v", v)
	}
	runNamedReconciler(t, w, orchestrator.Deps{Store: w.store, Client: w.client, CI: w.service}, "ci", time.Now().Add(6*time.Minute))
	again := w.verdict(task.ID)
	if again.RunID == "" || again.RunID == first.RunID {
		t.Fatalf("verdict after switching back = %+v, want a new run replacing %s", again, first.RunID)
	}
	if run := w.settle(again.RunID); run.Conclusion != ci.ConclusionSuccess {
		t.Fatalf("the resumed Grain CI run concluded %s", run.Conclusion)
	}
	t.Logf("switching back started run %s", again.RunID)
}
