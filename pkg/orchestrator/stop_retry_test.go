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
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

type isolatingTestSandboxes struct {
	orchestrator.Sandboxes
}

func (isolatingTestSandboxes) IsolatesNetwork() bool { return true }

type blockingFirstAcquireSandboxes struct {
	inner     orchestrator.Sandboxes
	calls     atomic.Int32
	acquiring chan struct{}
}

func (*blockingFirstAcquireSandboxes) IsolatesNetwork() bool { return true }

func (b *blockingFirstAcquireSandboxes) Acquire(ctx context.Context, name string, shape orchestrator.Shape) (orchestrator.Sandbox, error) {
	if b.calls.Add(1) == 1 {
		close(b.acquiring)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return b.inner.Acquire(ctx, name, shape)
}

func simulateStopAndRetry(t *testing.T, ctx context.Context, store *model.Store, cancellers *orchestrator.TaskCancellers, inFlight *orchestrator.InFlight, taskID string, stopAt, retryAt time.Time) {
	t.Helper()
	commentID, err := store.AddComment(ctx, model.Comment{
		TaskID:    taskID,
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}},
		Body:      model.TurnStoppedComment,
		CreatedAt: stopAt,
	})
	if err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	if err := store.ObserveField(ctx, taskID, stopAt, func(obs *model.Observation) {
		obs.PendingQuestionCommentID = &commentID
	}); err != nil {
		t.Fatalf("ObserveField stop: %v", err)
	}
	if err := store.AdvanceDelivered(ctx, taskID, commentID, stopAt); err != nil {
		t.Fatalf("AdvanceDelivered: %v", err)
	}
	if !cancellers.Cancel(taskID, orchestrator.ErrTurnStopped) {
		t.Fatal("cancellers.Cancel returned false while run was in setup")
	}

	deadline := time.Now().Add(runWaitLimit)
	for inFlight.Busy(taskID) {
		if time.Now().After(deadline) {
			t.Fatalf("task %s remained Busy in InFlight after Stop", taskID)
		}
		time.Sleep(10 * time.Millisecond)
	}

	runs, err := store.Runs(ctx, taskID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("Runs(%s) = %v, len %d; want 1", taskID, err, len(runs))
	}
	if runs[0].Outcome != "cancelled" {
		t.Fatalf("attempt 1 outcome = %q, want \"cancelled\" (not setup-failed)", runs[0].Outcome)
	}
	if runs[0].Detail != model.TurnStoppedDetail {
		t.Fatalf("attempt 1 detail = %q, want %q", runs[0].Detail, model.TurnStoppedDetail)
	}
	if runs[0].FinishedAt == nil {
		t.Fatal("attempt 1 finished_at = nil, want finished")
	}

	if err := store.ObserveField(ctx, taskID, retryAt, func(obs *model.Observation) {
		obs.PendingQuestionCommentID = nil
		obs.RetryRequestedAt = &retryAt
	}); err != nil {
		t.Fatalf("ObserveField retry: %v", err)
	}
}

func TestStopDuringSandboxSetupCancelsImmediatelyAndRetrySkipsBackoff(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")

	sbx := &blockingFirstAcquireSandboxes{
		inner:     orchestrator.NewHostSandboxes(t.TempDir()),
		acquiring: make(chan struct{}),
	}
	cancellers := orchestrator.NewTaskCancellers()
	inFlight := &orchestrator.InFlight{}
	defer func() { _ = inFlight.Wait(ctx) }()

	attempt2Ran := make(chan struct{})
	fw := orchestrator.StaticFramework(agentFunc(func(runCtx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		if runCtx.Err() != nil {
			return nil, runCtx.Err()
		}
		close(attempt2Ran)
		return &agent.Result{
			FinalText:  "done",
			Transcript: "done",
			ToolCalls: []agent.ToolCall{{
				Name:      "nothing_needed",
				Arguments: map[string]any{"reason": "done"},
			}},
		}, nil
	}))

	deps := orchestrator.Deps{
		Store:      store,
		Sandboxes:  sbx,
		Framework:  fw,
		Runs:       inFlight,
		MaxWorkers: 2,
		Config: orchestrator.Config{
			TaskCancellers: cancellers,
		},
	}

	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(time.Second)); err != nil {
		t.Fatalf("first RunCycle: %v", err)
	}
	select {
	case <-sbx.acquiring:
	case <-time.After(runWaitLimit):
		t.Fatal("attempt 1 never reached Sandboxes.Acquire")
	}

	stopAt := baseTime.Add(2 * time.Second)
	retryAt := baseTime.Add(3 * time.Second)
	simulateStopAndRetry(t, ctx, store, cancellers, inFlight, "t1", stopAt, retryAt)

	if err := orchestrator.RunCycle(ctx, deps, retryAt); err != nil {
		t.Fatalf("retry RunCycle: %v", err)
	}
	select {
	case <-attempt2Ran:
	case <-time.After(runWaitLimit):
		t.Fatal("Retry did not dispatch attempt 2 immediately (stuck in failure backoff or InFlight)")
	}
	if err := inFlight.Wait(ctx); err != nil {
		t.Fatalf("inFlight.Wait: %v", err)
	}

	runs, err := store.Runs(ctx, "t1")
	if err != nil || len(runs) != 2 {
		t.Fatalf("Runs(t1) = %v, len %d; want 2", err, len(runs))
	}
}

func TestStopDuringCloneCancelsImmediatelyWithoutSetupFailedStrike(t *testing.T) {
	t.Parallel()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}

	store, ctx := openStore(t)
	root := t.TempDir()
	bare := filepath.Join(root, "acme", "widgets.git")
	if err := os.MkdirAll(filepath.Dir(bare), 0o755); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "seed")
	for _, args := range [][]string{
		{"init", "--bare", "--initial-branch=main", bare},
		{"init", "--initial-branch=main", work},
		{"-C", work, "config", "user.name", "Test"},
		{"-C", work, "config", "user.email", "test@example.test"},
	} {
		if out, err := exec.Command(gitPath, args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("# widgets\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"-C", work, "add", "README.md"},
		{"-C", work, "commit", "-m", "init"},
		{"-C", work, "push", bare, "main:main"},
	} {
		if out, err := exec.Command(gitPath, args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}

	backend := &cgi.Handler{
		Path: gitPath,
		Args: []string{"http-backend"},
		Env: []string{
			"GIT_PROJECT_ROOT=" + root,
			"GIT_HTTP_EXPORT_ALL=1",
		},
	}
	var gitCalls atomic.Int32
	cloning := make(chan struct{})
	gitSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if gitCalls.Add(1) == 1 {
			close(cloning)
			<-r.Context().Done()
			return
		}
		backend.ServeHTTP(w, r)
	}))
	defer gitSrv.Close()

	dispatchTask(t, ctx, store, "t2")

	cancellers := orchestrator.NewTaskCancellers()
	inFlight := &orchestrator.InFlight{}
	defer func() { _ = inFlight.Wait(ctx) }()

	attempt2Ran := make(chan struct{})
	fw := orchestrator.StaticFramework(agentFunc(func(runCtx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		if runCtx.Err() != nil {
			return nil, runCtx.Err()
		}
		close(attempt2Ran)
		return &agent.Result{
			FinalText:  "done",
			Transcript: "done",
			ToolCalls: []agent.ToolCall{{
				Name:      "nothing_needed",
				Arguments: map[string]any{"reason": "done"},
			}},
		}, nil
	}))

	deps := orchestrator.Deps{
		Store:      store,
		Sandboxes:  isolatingTestSandboxes{orchestrator.NewHostSandboxes(t.TempDir())},
		Framework:  fw,
		Runs:       inFlight,
		MaxWorkers: 2,
		Config: orchestrator.Config{
			GitRemoteBase:  gitSrv.URL,
			TaskCancellers: cancellers,
		},
	}

	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(time.Second)); err != nil {
		t.Fatalf("first RunCycle: %v", err)
	}
	select {
	case <-cloning:
	case <-time.After(runWaitLimit):
		t.Fatal("attempt 1 never started git clone")
	}

	stopAt := baseTime.Add(2 * time.Second)
	retryAt := baseTime.Add(3 * time.Second)
	simulateStopAndRetry(t, ctx, store, cancellers, inFlight, "t2", stopAt, retryAt)

	if err := orchestrator.RunCycle(ctx, deps, retryAt); err != nil {
		t.Fatalf("retry RunCycle: %v", err)
	}
	select {
	case <-attempt2Ran:
	case <-time.After(runWaitLimit):
		t.Fatal("Retry did not dispatch attempt 2 immediately after Stop during clone")
	}
	if err := inFlight.Wait(ctx); err != nil {
		t.Fatalf("inFlight.Wait: %v", err)
	}
}

func TestStopBeforeSetupStartsPreservesCancelledRowWithoutAcquiringSandbox(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	created := baseTime
	if err := store.PutTask(ctx, model.Task{
		ID:        "t3",
		Title:     "Stop before setup starts",
		Body:      "verify pre-finished cancelled row skips sandbox setup and preserves timestamp",
		CreatedAt: &created,
	}); err != nil {
		t.Fatal(err)
	}
	d := dispatch.Dispatch{TaskID: "t3", RunID: "t3-1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime.Add(time.Second))
	stopAt := baseTime.Add(2 * time.Second)
	if err := store.FinishRun(ctx, "t3-1", stopAt, "cancelled", model.TurnStoppedDetail); err != nil {
		t.Fatal(err)
	}
	task, err := store.GetTask(ctx, "t3")
	if err != nil || task == nil {
		t.Fatalf("GetTask: %v", err)
	}

	fw := agentFunc(func(runCtx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		return nil, runCtx.Err()
	})
	_, runErr := orchestrator.RunDispatch(ctx, store, fw, orchestrator.Config{CancelPollInterval: time.Hour}, *task, d, nil, t.TempDir(), "", nil, baseTime.Add(time.Second))
	if runErr == nil {
		t.Fatal("RunDispatch err = nil, want ErrTurnStopped")
	}

	runs, err := store.Runs(ctx, "t3")
	if err != nil || len(runs) != 1 {
		t.Fatalf("Runs(t3) = %v, len %d; want 1", err, len(runs))
	}
	if runs[0].Outcome != "cancelled" || runs[0].Detail != model.TurnStoppedDetail {
		t.Fatalf("run = (%q, %q), want (cancelled, %q)", runs[0].Outcome, runs[0].Detail, model.TurnStoppedDetail)
	}
	if runs[0].FinishedAt == nil || !runs[0].FinishedAt.Equal(stopAt) {
		t.Fatalf("finished_at = %v, want %v preserved", runs[0].FinishedAt, stopAt)
	}
}
