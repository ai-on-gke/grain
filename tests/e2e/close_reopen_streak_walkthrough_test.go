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

// docs/test-plans/2026-11-01.md §10 item 4 (PR #241, 166a83d7): a task
// closed while its agent is mid-turn, then reopened, is dispatched again on
// the very next cycle -- no "backing off after failed run" -- and the task
// as a person reads it (grain get, the task page's GET /api/tasks/{id})
// shows no failure streak. close_while_live_test.go closes before the run
// starts; this one closes while the scripted agent's run_command is
// actually running, which is the case 2026-10-30's five reopened tasks hit.
package e2e

import (
	"context"
	"encoding/json"
	"os/exec"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestClosingAndReopeningATaskMidTurnLeavesNoFailureStreakAndNoBackoff(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	bin := buildGrainCLI(t)
	storeDir := t.TempDir()

	created := runCLIStore(t, bin, storeDir,
		"-json", "create",
		"-title", "a task closed and reopened for a fresh sandbox",
		"-body", "sleeps until somebody closes it",
		"-repo", "acme/widgets",
		"-approve",
	)
	var task ui.Task
	if err := json.Unmarshal([]byte(created), &task); err != nil {
		t.Fatalf("parsing grain create -json output: %v\n%s", err, created)
	}

	var d dispatch.Dispatch
	var fullTask model.Task
	withStore(t, storeDir, func(store *model.Store, ctx context.Context) {
		dispatches, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, baseTime)
		if err != nil {
			t.Fatal(err)
		}
		if len(dispatches) != 1 || dispatches[0].TaskID != task.ID {
			t.Fatalf("Cycle dispatched %+v, want exactly %s", dispatches, task.ID)
		}
		d = dispatches[0]
		got, err := store.GetTask(ctx, task.ID)
		if err != nil || got == nil {
			t.Fatalf("GetTask(%s): %v", task.ID, err)
		}
		fullTask = *got
	})

	// The agent's turn is live in its sandbox when the close lands: a
	// run_command that would take a minute, cancelled by RunDispatch's
	// store poll once the task reads closed.
	root := t.TempDir()
	if err := mcp.ConfigureGitCredentials(root, "http://127.0.0.1:1/acme/widgets.git", "unused", mcp.GitIdentity{}); err != nil {
		t.Fatal(err)
	}
	script := []antigravity.Step{
		toolCall("run_command", map[string]any{"command": "sleep 60"}),
		finalText("never reached"),
	}
	finishedAt := baseTime.Add(time.Minute)
	done := make(chan error, 1)
	go func() {
		withStore(t, storeDir, func(store *model.Store, ctx context.Context) {
			fw := antigravity.NewForTest(antigravity.Steps(script...))
			_, err := orchestrator.RunDispatch(ctx, store, fw,
				orchestrator.Config{CancelPollInterval: 20 * time.Millisecond},
				fullTask, d, mcp.NewSandboxTools(root), root, "", nil, finishedAt)
			done <- err
		})
	}()
	time.Sleep(300 * time.Millisecond)
	runCLIStore(t, bin, storeDir, "-json", "close", task.ID)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("RunDispatch returned no error for a run whose task was closed mid-turn")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("RunDispatch did not cancel the live run within 20s of its task closing")
	}

	runCLIStore(t, bin, storeDir, "-json", "reopen", task.ID)

	withStore(t, storeDir, func(store *model.Store, ctx context.Context) {
		streak, err := store.FailureStreak(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if streak != nil && streak.Count != 0 {
			t.Fatalf("FailureStreak after close+reopen = %+v, want no streak", streak)
		}
		// FinishRun stamps the cancelled run with the wall clock, so this is
		// a second after it finished: a counted failure would hold the task
		// back by dispatch's retry backoff here.
		again, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, time.Now().Add(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if len(again) != 1 || again[0].TaskID != task.ID {
			t.Fatalf("Cycle after reopen dispatched %+v, want %s at once (no backoff)", again, task.ID)
		}
	})

	got := runCLIStore(t, bin, storeDir, "-json", "get", task.ID)
	var detail ui.TaskDetail
	if err := json.Unmarshal([]byte(got), &detail); err != nil {
		t.Fatalf("parsing grain get -json output: %v\n%s", err, got)
	}
	if detail.FailedAttempts != 0 || detail.LastFailureAt != nil || detail.LastFailureReason != "" {
		t.Fatalf("task detail shows a failure streak after close+reopen: attempts=%d at=%v reason=%q",
			detail.FailedAttempts, detail.LastFailureAt, detail.LastFailureReason)
	}
}
