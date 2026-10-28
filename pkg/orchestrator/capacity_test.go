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
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// A run whose framework reports the provider having no capacity for it
// records model.CapacityOutcome, not "failed": nothing about the task was
// wrong, the provider was overloaded. That word is what keeps the attempt
// out of the task's failure streak, and what lets `grain get` and the UI
// show the attempt as itself rather than as a generic failure.
//
// And nothing is paused. That is the whole difference from a usage
// limit: an overload is usually seconds long, and stopping the
// deployment -- cancelling every other framework's live runs with it --
// would cost far more than the wait it saves.
func TestRunDispatchRecordsACapacityRefusalWithoutPausing(t *testing.T) {
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
		return pushed(), &agent.CapacityError{
			Framework: "antigravity",
			Message:   "[503 Service Unavailable] The model is overloaded. Please try again later.",
		}
	})
	pause := &orchestrator.Pause{}
	cfg := orchestrator.Config{Pause: pause, Now: func() time.Time { return baseTime }}

	result, runErr := orchestrator.RunDispatch(ctx, store, fw, cfg, *task, d, nil, t.TempDir(), "", nil, baseTime)
	if runErr == nil {
		t.Fatal("RunDispatch err = nil, want the refusal reported")
	}
	if _, ok := agent.Capacity(runErr); !ok {
		t.Errorf("RunDispatch err = %v, want it to still carry the agent.CapacityError", runErr)
	}
	// The branch this run pushed before the provider refused it still
	// comes back, so runOne can salvage it (agent.Framework's contract).
	if result == nil {
		t.Error("RunDispatch returned no result, want the work the run did before the refusal")
	}

	runs, err := store.Runs(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %+v, want exactly one", runs)
	}
	if runs[0].Outcome != model.CapacityOutcome {
		t.Errorf("run outcome = %q, want %q", runs[0].Outcome, model.CapacityOutcome)
	}
	if !strings.Contains(runs[0].Detail, "overloaded") {
		t.Errorf("run detail = %q, want it to say what the provider said", runs[0].Detail)
	}
	// Half the point of the new word is that an operator can read the
	// attempt back as weather rather than as a fault.
	if got := model.EndingOf(runs[0].Outcome, runs[0].Detail); got != model.EndingCapacity {
		t.Errorf("EndingOf(%q, %q) = %q, want %q", runs[0].Outcome, runs[0].Detail, got, model.EndingCapacity)
	}

	if pause.Blocked(baseTime).Any() {
		t.Error("dispatch was paused by a capacity refusal; only a usage limit should stop the deployment")
	}
}

// The work the run got done before the refusal is still named on the
// attempt, exactly as it is for every other way a run ends badly -- an
// operator reading "the provider had no capacity" wants to know whether
// the branch is already pushed.
func TestACapacityRefusalStillNamesTheWorkTheRunDid(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t2")
	d := dispatch.Dispatch{TaskID: "t2", RunID: "r2", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)
	task, err := store.GetTask(ctx, "t2")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	fw := agentFunc(func(context.Context, agent.RunConfig) (*agent.Result, error) {
		return pushed(), &agent.CapacityError{Framework: "codex", Message: "The server is overloaded."}
	})
	cfg := orchestrator.Config{Now: func() time.Time { return baseTime }}
	if _, runErr := orchestrator.RunDispatch(ctx, store, fw, cfg, *task, d, nil, t.TempDir(), "", nil, baseTime); runErr == nil {
		t.Fatal("RunDispatch err = nil, want the refusal reported")
	}

	runs, err := store.Runs(ctx, "t2")
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %+v (%v), want exactly one", runs, err)
	}
	if !strings.Contains(runs[0].Detail, "tool call") {
		t.Errorf("run detail = %q, want it to name what the run got done first", runs[0].Detail)
	}
}
