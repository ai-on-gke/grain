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

// The census, recorded for real: RunDispatch writes what the run did with
// its tools beside the outcome it is judged on, and a run that could not
// have its census recorded still finishes.

import (
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

func TestRunDispatchRecordsWhatTheRunDidWithItsTools(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")
	d := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	fw := agentFunc(func(context.Context, agent.RunConfig) (*agent.Result, error) {
		return &agent.Result{FinalText: "done", ToolCalls: []agent.ToolCall{
			{Name: "run_command", Arguments: map[string]any{"command": "cd work && git push origin grain/t1"},
				Text: "exit=0\nstdout:\n\nstderr:\n"},
			{Name: "edit_file", Text: "String not found", IsError: true},
			{Name: "wait_for_checks", Text: "Waited 2m0s for CI on grain/t1 at abc1234.\n\n" +
				"Checks against abc1234:\n  ok tests\n\n0 failing, 0 not finished, 1 otherwise done.\n\n" +
				"Every check against abc1234 finished and none of them failed."},
		}}, nil
	})
	if _, err := orchestrator.RunDispatch(ctx, store, fw, orchestrator.Config{},
		*task, d, nil, t.TempDir(), "", nil, baseTime); err != nil {
		t.Fatalf("RunDispatch: %v", err)
	}

	uses, err := store.RunToolUses(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byTool := map[string]model.RunToolUse{}
	for _, use := range uses {
		if use.RunID != "r1" {
			t.Errorf("census row for run %q, want r1", use.RunID)
		}
		byTool[use.Tool] = use
	}
	if len(byTool) != 3 {
		t.Fatalf("recorded %d tools, want 3: %+v", len(byTool), uses)
	}
	if got := byTool["edit_file"]; got.Calls != 1 || got.Errored != 1 {
		t.Errorf("edit_file = %+v, want one call that errored", got)
	}
	if got := byTool["run_command"]; got.Calls != 1 || got.Errored != 0 || got.MaxResultBytes == 0 {
		t.Errorf("run_command = %+v, want one successful call with a sized result", got)
	}

	waits, err := store.RunCheckWaits(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(waits) != 1 {
		t.Fatalf("recorded %d CI waits, want 1: %+v", len(waits), waits)
	}
	if got := waits[0]; got.Verdict != mcp.WaitVerdictPassed || got.Waited != 2*time.Minute || got.PushesBefore != 1 {
		t.Errorf("wait = %+v, want a pass after 2m and one push", got)
	}
}

// A run that never reached its agent records no census -- there is
// nothing to record -- and still finishes normally.
func TestRunDispatchRecordsNoCensusForARunThatCalledNothing(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")
	d := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	fw := agentFunc(func(context.Context, agent.RunConfig) (*agent.Result, error) {
		return &agent.Result{FinalText: "I did nothing"}, nil
	})
	if _, err := orchestrator.RunDispatch(ctx, store, fw, orchestrator.Config{},
		*task, d, nil, t.TempDir(), "", nil, baseTime); err != nil {
		t.Fatalf("RunDispatch: %v", err)
	}
	uses, err := store.RunToolUses(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(uses) != 0 {
		t.Errorf("census = %+v, want nothing for a run that called nothing", uses)
	}
}
