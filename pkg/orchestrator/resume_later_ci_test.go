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

package orchestrator_test

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

type fakeActionsClient struct {
	github.Client
	mu   sync.Mutex
	runs map[int64]github.WorkflowRun
	jobs map[int64][]github.ActionsRunJob
}

func newFakeActionsClient(base github.Client) *fakeActionsClient {
	return &fakeActionsClient{
		Client: base,
		runs:   map[int64]github.WorkflowRun{},
		jobs:   map[int64][]github.ActionsRunJob{},
	}
}

func (f *fakeActionsClient) putRun(r github.WorkflowRun) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs[r.ID] = r
}

func (f *fakeActionsClient) ListActionsRuns(owner, repo string, q github.ActionsRunsQuery) ([]github.WorkflowRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []github.WorkflowRun
	for _, r := range f.runs {
		if q.Branch != "" && r.HeadBranch != q.Branch {
			continue
		}
		if !q.Since.IsZero() && r.StartedAt != nil && r.StartedAt.Before(q.Since) {
			continue
		}
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b github.WorkflowRun) int {
		if a.ID > b.ID {
			return -1
		}
		if a.ID < b.ID {
			return 1
		}
		return 0
	})
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

func (f *fakeActionsClient) GetActionsRun(owner, repo string, runID int64) (*github.WorkflowRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.runs[runID]
	if !ok {
		return nil, nil
	}
	cp := r
	return &cp, nil
}

func (f *fakeActionsClient) ListActionsJobs(owner, repo string, runID int64) ([]github.ActionsRunJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.jobs[runID]), nil
}

func (f *fakeActionsClient) ActionsJobLog(owner, repo string, jobID int64) (string, error) {
	return "", nil
}

func (f *fakeActionsClient) ListActionsArtifacts(owner, repo string, runID int64) ([]github.ActionsArtifact, error) {
	return nil, nil
}

type uiTaskWaiter struct {
	client *ui.Client
	taskID string
}

func (w uiTaskWaiter) WaitOn(ctx context.Context, ids []string, park bool) ([]model.WaitTarget, error) {
	return w.client.WaitOn(ctx, w.taskID, ids, park)
}

func (w uiTaskWaiter) ParkUntil(ctx context.Context, until time.Time) (time.Time, error) {
	return w.client.ParkUntil(ctx, w.taskID, until)
}

func (w uiTaskWaiter) ResumeLater(ctx context.Context, when model.ResumeOn) (model.ResumeOn, error) {
	return w.client.ResumeLater(ctx, w.taskID, when)
}

func callResumeLaterTool(
	t *testing.T,
	ctx context.Context,
	uiClient *ui.Client,
	taskID string,
	args map[string]any,
) mcp.Result {
	t.Helper()
	tools := mcp.NewResumeLaterTools(uiTaskWaiter{client: uiClient, taskID: taskID}, nil)
	if len(tools) != 1 {
		t.Fatalf("NewResumeLaterTools returned %d tools, want 1", len(tools))
	}
	return tools[0].Handler(ctx, args)
}

func parkTaskWithResumeLater(
	t *testing.T,
	ctx context.Context,
	store *model.Store,
	ghClient github.Client,
	uiClient *ui.Client,
	task model.Task,
	runID string,
	when map[string]any,
	startedAt time.Time,
) {
	t.Helper()
	if err := store.StartRun(ctx, model.Run{
		ID: runID, TaskID: task.ID, Sandbox: "s-" + runID, Attempt: 1, StartedAt: startedAt,
	}, model.Limits{}); err != nil {
		t.Fatalf("StartRun(%s): %v", runID, err)
	}
	callArgs := map[string]any{"reason": "waiting on CI verdict", "when": when}
	res := callResumeLaterTool(t, ctx, uiClient, task.ID, callArgs)
	if res.IsError {
		t.Fatalf("resume_later refused unexpectedly: %s", res.Text)
	}
	endedAt := startedAt.Add(2 * time.Minute)
	if err := store.FinishRun(ctx, runID, endedAt, "succeeded", ""); err != nil {
		t.Fatalf("FinishRun(%s): %v", runID, err)
	}
	result := toolResult(agent.ToolCall{
		Name:      "resume_later",
		Arguments: callArgs,
		Text:      res.Text,
	})
	if err := orchestrator.ProcessResult(ctx, store, ghClient, task, result, runID, endedAt); err != nil {
		t.Fatalf("ProcessResult(%s): %v", task.ID, err)
	}
}

// On a ci_mode: github repository, a task that parks on ci: "my-branch" (or a
// gha:... run ID) stays deferred while Actions is running, ignores unrelated
// grain CI runs, and queues again once the GitHub Actions verdict lands.
func TestResumeLaterCIMyBranchWakesOnGitHubActionsVerdict(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, baseClient := newSim(t, "acme", "widgets", "main")
	ghClient := newFakeActionsClient(baseClient)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:   repo,
		CIMode: model.CIModeGitHub,
	}); err != nil {
		t.Fatal(err)
	}

	now := baseTime
	uiClient := &ui.Client{
		Store: store,
		Now:   func() time.Time { return now },
		Config: ui.Config{
			Actions:            ghClient,
			GitHubPullRequests: ghClient,
		},
	}

	// 1. Task parks on ci: "my-branch" while an Actions workflow run is in_progress.
	task := filedTask(t, ctx, store, "gh-my-branch", repo)
	pushBranch(t, sim.BareRepo, task.BranchName())
	head, err := ghClient.GetBranchHead(repo.Owner, repo.Name, task.BranchName())
	if err != nil || head == nil {
		t.Fatalf("GetBranchHead: %v, %v", head, err)
	}

	runStart := now.Add(30 * time.Second)
	ghClient.putRun(github.WorkflowRun{
		ID:         5001,
		Name:       "Tests",
		Path:       ".github/workflows/tests.yml",
		HeadBranch: task.BranchName(),
		HeadSHA:    head.SHA,
		Event:      "push",
		Status:     "in_progress",
		StartedAt:  &runStart,
		UpdatedAt:  &runStart,
	})
	sim.CheckRuns[head.SHA] = []github.CheckRun{
		{ID: 8001, Name: "Tests / go", Status: "in_progress", HeadSHA: head.SHA, StartedAt: &runStart},
	}

	now = baseTime.Add(time.Minute)
	parkTaskWithResumeLater(t, ctx, store, ghClient, uiClient, task, "gh-my-branch-1",
		map[string]any{"ci": "my-branch"}, baseTime)

	if st := taskState(t, ctx, store, task.ID); st != model.StateDeferred {
		t.Fatalf("state after parking = %q, want deferred", st)
	}

	// A grain CI run finishing on the same branch does NOT wake a ci_mode: github task.
	grainFinish := now.Add(3 * time.Minute)
	if err := store.PutCIRun(ctx, model.CIRun{
		ID: "ci-unrelated", Key: "k1", Repo: repo.String(), Ref: task.BranchName(),
		StartedAt: grainFinish.Add(-time.Minute), FinishedAt: grainFinish, Conclusion: "success",
	}); err != nil {
		t.Fatal(err)
	}
	deps := orchestrator.Deps{Store: store, Client: ghClient}
	if err := orchestrator.ReconcileWake(ctx, deps, now.Add(4*time.Minute)); err != nil {
		t.Fatalf("ReconcileWake while Actions in_progress: %v", err)
	}
	if st := taskState(t, ctx, store, task.ID); st != model.StateDeferred {
		t.Fatalf("state while Actions still running = %q, want deferred", st)
	}

	// Fake GitHub Actions verdict lands (completed -> success).
	completedAt := now.Add(5 * time.Minute)
	success := "success"
	ghClient.putRun(github.WorkflowRun{
		ID:         5001,
		Name:       "Tests",
		Path:       ".github/workflows/tests.yml",
		HeadBranch: task.BranchName(),
		HeadSHA:    head.SHA,
		Event:      "push",
		Status:     "completed",
		Conclusion: "success",
		StartedAt:  &runStart,
		UpdatedAt:  &completedAt,
	})
	sim.CheckRuns[head.SHA] = []github.CheckRun{
		{ID: 8001, Name: "Tests / go", Status: "completed", Conclusion: &success, HeadSHA: head.SHA, StartedAt: &runStart, CompletedAt: &completedAt},
	}

	if err := orchestrator.ReconcileWake(ctx, deps, now.Add(6*time.Minute)); err != nil {
		t.Fatalf("ReconcileWake after Actions verdict: %v", err)
	}
	if st := taskState(t, ctx, store, task.ID); st != model.StateQueued {
		t.Fatalf("state after Actions verdict = %q, want queued", st)
	}

	// 2. Also verify waking from recordPolledChecks alone (no live ActionsReader)
	// and waking on an explicit gha:<owner>:<name>:<run-id> target.
	taskRunID := filedTask(t, ctx, store, "gh-run-id", repo)
	pushBranch(t, sim.BareRepo, taskRunID.BranchName())
	run2Start := now.Add(7 * time.Minute)
	ghClient.putRun(github.WorkflowRun{
		ID:         5002,
		Name:       "Tests",
		Path:       ".github/workflows/tests.yml",
		HeadBranch: taskRunID.BranchName(),
		Event:      "push",
		Status:     "in_progress",
		StartedAt:  &run2Start,
		UpdatedAt:  &run2Start,
	})
	now = baseTime.Add(8 * time.Minute)
	parkTaskWithResumeLater(t, ctx, store, ghClient, uiClient, taskRunID, "gh-run-id-1",
		map[string]any{"ci": "gha:acme:widgets:5002"}, now.Add(-time.Minute))
	if st := taskState(t, ctx, store, taskRunID.ID); st != model.StateDeferred {
		t.Fatalf("state after parking on gha run id = %q, want deferred", st)
	}

	run2Done := now.Add(3 * time.Minute)
	ghClient.putRun(github.WorkflowRun{
		ID:         5002,
		Name:       "Tests",
		Path:       ".github/workflows/tests.yml",
		HeadBranch: taskRunID.BranchName(),
		Event:      "push",
		Status:     "completed",
		Conclusion: "failure",
		StartedAt:  &run2Start,
		UpdatedAt:  &run2Done,
	})
	if err := orchestrator.ReconcileWake(ctx, deps, now.Add(4*time.Minute)); err != nil {
		t.Fatalf("ReconcileWake for gha run id: %v", err)
	}
	if st := taskState(t, ctx, store, taskRunID.ID); st != model.StateQueued {
		t.Fatalf("state after gha run id concluded = %q, want queued", st)
	}
}

// On a ci_mode: grain repository, the flow is unchanged: GitHub Actions
// verdicts do not wake the task, and a concluded grain CI run on the branch
// after the deferral wakes it.
func TestResumeLaterCIMyBranchKeepsGrainCIBehaviourOnGrainRepo(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, baseClient := newSim(t, "acme", "grain-app", "main")
	ghClient := newFakeActionsClient(baseClient)
	repo := model.RepoRef{Owner: "acme", Name: "grain-app"}

	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:   repo,
		CIMode: model.CIModeGrain,
	}); err != nil {
		t.Fatal(err)
	}

	now := baseTime.Add(time.Minute)
	uiClient := &ui.Client{
		Store: store,
		Now:   func() time.Time { return now },
		Config: ui.Config{
			Actions:            ghClient,
			GitHubPullRequests: ghClient,
		},
	}

	task := filedTask(t, ctx, store, "grain-my-branch", repo)
	pushBranch(t, sim.BareRepo, task.BranchName())
	parkTaskWithResumeLater(t, ctx, store, ghClient, uiClient, task, "grain-my-branch-1",
		map[string]any{"ci": "my-branch"}, baseTime)

	if st := taskState(t, ctx, store, task.ID); st != model.StateDeferred {
		t.Fatalf("state after parking = %q, want deferred", st)
	}

	// A GitHub Actions job concluding on a ci_mode: grain repo must NOT wake it.
	ghDone := now.Add(3 * time.Minute)
	success := "success"
	orchestrator.RecordPolledChecks(ctx, store, repo, task.BranchName(), "deadbeef", []github.CheckRun{
		{ID: 9001, Name: "Tests", App: model.GitHubActionsApp, Status: "completed", Conclusion: &success, CompletedAt: &ghDone},
	}, ghDone)

	deps := orchestrator.Deps{Store: store, Client: ghClient}
	if err := orchestrator.ReconcileWake(ctx, deps, now.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if st := taskState(t, ctx, store, task.ID); st != model.StateDeferred {
		t.Fatalf("state after GitHub check on ci_mode: grain repo = %q, want still deferred", st)
	}

	// A grain CI verdict from BEFORE the deferral does not wake it either.
	beforeHold := baseTime.Add(30 * time.Second)
	if err := store.PutCIRun(ctx, model.CIRun{
		ID: "ci-old", Key: "k-old", Repo: repo.String(), Ref: task.BranchName(),
		StartedAt: baseTime, FinishedAt: beforeHold, Conclusion: "success",
	}); err != nil {
		t.Fatal(err)
	}
	if err := orchestrator.ReconcileWake(ctx, deps, now.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if st := taskState(t, ctx, store, task.ID); st != model.StateDeferred {
		t.Fatalf("state after pre-deferral grain CI run = %q, want deferred", st)
	}

	// A grain CI verdict arriving AFTER the deferral wakes it back to queued.
	afterHold := now.Add(6 * time.Minute)
	if err := store.PutCIRun(ctx, model.CIRun{
		ID: "ci-new", Key: "k-new", Repo: repo.String(), Ref: task.BranchName(),
		StartedAt: now.Add(5 * time.Minute), FinishedAt: afterHold, Conclusion: "success",
	}); err != nil {
		t.Fatal(err)
	}
	if err := orchestrator.ReconcileWake(ctx, deps, now.Add(7*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if st := taskState(t, ctx, store, task.ID); st != model.StateQueued {
		t.Fatalf("state after grain CI verdict = %q, want queued", st)
	}
}

// A CI wait that can never be satisfied (no repo/branch, ci_mode: none,
// unpushed branch, no CI configured for the branch, or a run ID that does not
// exist) is refused when requested instead of parking forever.
func TestResumeLaterRefusesUnsatisfiableCIWaits(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, baseClient := newSim(t, "acme", "widgets", "main")
	ghClient := newFakeActionsClient(baseClient)

	repoGH := model.RepoRef{Owner: "acme", Name: "widgets"}
	repoNone := model.RepoRef{Owner: "acme", Name: "nocirepo"}
	repoGrain := model.RepoRef{Owner: "acme", Name: "grainrepo"}

	for _, cfg := range []model.RepoConfig{
		{Repo: repoGH, CIMode: model.CIModeGitHub},
		{Repo: repoNone, CIMode: model.CIModeNone},
		{Repo: repoGrain, CIMode: model.CIModeGrain},
	} {
		if err := store.PutRepoConfig(ctx, cfg); err != nil {
			t.Fatal(err)
		}
	}

	now := baseTime
	uiClient := &ui.Client{
		Store: store,
		Now:   func() time.Time { return now },
		Config: ui.Config{
			Actions:            ghClient,
			GitHubPullRequests: ghClient,
		},
	}

	// 1. Task with no target repository (no_repo).
	human := model.Principal{Kind: model.PrincipalHuman, ID: "alice"}
	noRepoTask := model.Task{
		ID:       "no-repo",
		Intent:   model.IntentImplement,
		Title:    "no repo task",
		Origin:   model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
		Approval: &model.Attribution{Actor: human},
		Binding:  model.BindingDirective,
	}
	if err := store.PutTask(ctx, noRepoTask); err != nil {
		t.Fatal(err)
	}

	// 2. Task on ci_mode: none repo.
	noneTask := filedTask(t, ctx, store, "none-ci", repoNone)

	// 3. Task on ci_mode: github whose branch was never pushed.
	unpushedTask := filedTask(t, ctx, store, "unpushed-gh", repoGH)

	// 4. Task on ci_mode: github whose branch IS pushed, but the repository has
	// zero Actions workflows, zero check runs on branch/main, and zero recorded jobs.
	emptyCITask := filedTask(t, ctx, store, "empty-gh-ci", repoGH)
	pushBranch(t, sim.BareRepo, emptyCITask.BranchName())

	// 5. Task on ci_mode: grain for testing non-existent / wrong-provider run IDs.
	grainTask := filedTask(t, ctx, store, "grain-bad-run", repoGrain)

	cases := []struct {
		name    string
		taskID  string
		ciValue string
		wantSub string
	}{
		{
			name:    "no repository branch",
			taskID:  noRepoTask.ID,
			ciValue: "my-branch",
			wantSub: "no repository branch",
		},
		{
			name:    "ci_mode none",
			taskID:  noneTask.ID,
			ciValue: "my-branch",
			wantSub: "ci_mode: none",
		},
		{
			name:    "unpushed branch on ci_mode github",
			taskID:  unpushedTask.ID,
			ciValue: "my-branch",
			wantSub: "does not exist",
		},
		{
			name:    "no CI configured on ci_mode github branch",
			taskID:  emptyCITask.ID,
			ciValue: "my-branch",
			wantSub: "no GitHub Actions workflows or checks are configured",
		},
		{
			name:    "non-existent Actions run ID on ci_mode github",
			taskID:  emptyCITask.ID,
			ciValue: "gha:acme:widgets:999999",
			wantSub: "does not exist",
		},
		{
			name:    "grain CI run ID on ci_mode github",
			taskID:  emptyCITask.ID,
			ciValue: "ci-1234",
			wantSub: "not a GitHub Actions run id",
		},
		{
			name:    "foreign repo Actions run ID on ci_mode github",
			taskID:  emptyCITask.ID,
			ciValue: "gha:other:repo:42",
			wantSub: "not on acme/widgets",
		},
		{
			name:    "non-existent grain CI run ID on ci_mode grain",
			taskID:  grainTask.ID,
			ciValue: "ci-does-not-exist",
			wantSub: "does not exist",
		},
		{
			name:    "Actions run ID on ci_mode grain",
			taskID:  grainTask.ID,
			ciValue: "gha:acme:grainrepo:42",
			wantSub: "uses grain CI",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := callResumeLaterTool(t, ctx, uiClient, tc.taskID, map[string]any{
				"reason": "waiting on CI",
				"when":   map[string]any{"ci": tc.ciValue},
			})
			if !res.IsError {
				t.Fatalf("expected refusal for %s (%q), got success: %s", tc.name, tc.ciValue, res.Text)
			}
			if !strings.Contains(res.Text, tc.wantSub) {
				t.Fatalf("refusal text = %q, want substring %q", res.Text, tc.wantSub)
			}
			got, err := store.GetTask(ctx, tc.taskID)
			if err != nil {
				t.Fatal(err)
			}
			if got.DeferredAt != nil {
				t.Fatalf("refused resume_later still deferred task %s at %v", tc.taskID, got.DeferredAt)
			}
		})
	}
}

// A task deferred on a CI wake for longer than DefaultCIWakeStallBound (6h)
// is woken by the backstop with a note, a stall notification and a wake of
// the pipeline steward -- whether or not CI has shown activity on its
// branch meanwhile, since activity that never turns into a verdict grain
// can see is exactly how tasks sat deferred for days.
func TestResumeLaterCIWakeStallBoundFires(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, baseClient := newSim(t, "acme", "widgets", "main")
	ghClient := newFakeActionsClient(baseClient)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:   repo,
		CIMode: model.CIModeGitHub,
	}); err != nil {
		t.Fatal(err)
	}

	// Enable the pipeline-steward schedule so we can observe escalation waking it.
	stewardID := model.PipelineStewardTemplateID
	if err := store.PutSchedule(ctx, model.Schedule{
		ID:         stewardID,
		TemplateID: &stewardID,
		Recurrence: model.Recurrence{Kind: model.RecurrenceEveryNMinutes, EveryNMinutes: 30},
		Overlap:    model.OverlapSkip,
		Enabled:    true,
		NextRunAt:  baseTime.Add(24 * time.Hour),
		CreatedAt:  baseTime,
	}); err != nil {
		t.Fatal(err)
	}

	// Seed one historic workflow run on main so the repo has CI configured at
	// request time, then have no CI activity at all on the task's branch.
	historic := baseTime.Add(-time.Hour)
	ghClient.putRun(github.WorkflowRun{
		ID:         1000,
		Name:       "Tests",
		Path:       ".github/workflows/tests.yml",
		HeadBranch: "main",
		Event:      "push",
		Status:     "completed",
		Conclusion: "success",
		StartedAt:  &historic,
		UpdatedAt:  &historic,
	})

	now := baseTime.Add(time.Minute)
	uiClient := &ui.Client{
		Store: store,
		Now:   func() time.Time { return now },
		Config: ui.Config{
			Actions:            ghClient,
			GitHubPullRequests: ghClient,
		},
	}

	stalledTask := filedTask(t, ctx, store, "stall-me", repo)
	pushBranch(t, sim.BareRepo, stalledTask.BranchName())
	parkTaskWithResumeLater(t, ctx, store, ghClient, uiClient, stalledTask, "stall-me-1",
		map[string]any{"ci": "my-branch"}, baseTime)

	// Second task that DOES get CI activity at T+4h (an in-progress run that
	// never concludes): the backstop wakes it at T+6h05m all the same.
	activeTask := filedTask(t, ctx, store, "active-ci", repo)
	pushBranch(t, sim.BareRepo, activeTask.BranchName())
	parkTaskWithResumeLater(t, ctx, store, ghClient, uiClient, activeTask, "active-ci-1",
		map[string]any{"ci": "my-branch"}, baseTime)
	activityAt := now.Add(4 * time.Hour)
	ghClient.putRun(github.WorkflowRun{
		ID:         2000,
		Name:       "Tests",
		Path:       ".github/workflows/tests.yml",
		HeadBranch: activeTask.BranchName(),
		Event:      "push",
		Status:     "in_progress",
		StartedAt:  &activityAt,
		UpdatedAt:  &activityAt,
	})

	deps := orchestrator.Deps{Store: store, Client: ghClient}
	subject := model.NotificationSubject(model.FamilyCIWakeStalled, stalledTask.ID)

	// Just inside the 6h bound: still deferred, no stall notification.
	if err := orchestrator.ReconcileWake(ctx, deps, now.Add(orchestrator.DefaultCIWakeStallBound-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if st := taskState(t, ctx, store, stalledTask.ID); st != model.StateDeferred {
		t.Fatalf("state before stall bound = %q, want deferred", st)
	}
	if n, err := store.OpenNotification(ctx, subject); err != nil || n != nil {
		t.Fatalf("OpenNotification before stall bound = %+v, %v; want nil", n, err)
	}

	// Past the 6h bound: stalledTask is undeferred, stall notification is raised,
	// and the steward is woken.
	pastBound := now.Add(orchestrator.DefaultCIWakeStallBound + 5*time.Minute)
	if err := orchestrator.ReconcileWake(ctx, deps, pastBound); err != nil {
		t.Fatal(err)
	}
	if st := taskState(t, ctx, store, stalledTask.ID); st != model.StateQueued {
		t.Fatalf("stalledTask state past stall bound = %q, want queued", st)
	}
	notif, err := store.OpenNotification(ctx, subject)
	if err != nil || notif == nil {
		t.Fatalf("OpenNotification past stall bound = %+v, %v; want open warning", notif, err)
	}
	if notif.Severity != model.NotificationWarning || notif.TaskID != stalledTask.ID {
		t.Fatalf("stall notification = %+v, want warning on task %s", notif, stalledTask.ID)
	}
	comments, err := store.Comments(ctx, stalledTask.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(comments, func(c model.Comment) bool {
		return strings.Contains(c.Body, "backstop") && strings.Contains(c.Body, "undeferred")
	}) {
		t.Fatalf("expected stall undefer comment on %s, got %+v", stalledTask.ID, comments)
	}
	stewardTasks, err := store.OpenTasksWithTag(ctx, model.ScheduleFiringTag(model.PipelineStewardTemplateID))
	if err != nil || len(stewardTasks) == 0 {
		t.Fatalf("expected pipeline-steward schedule wake, got %v (%v)", stewardTasks, err)
	}

	if st := taskState(t, ctx, store, activeTask.ID); st != model.StateQueued {
		t.Fatalf("activeTask (CI busy but never concluding) state at T+6h05m = %q, want queued", st)
	}
}
