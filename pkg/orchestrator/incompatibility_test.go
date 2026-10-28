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

func TestRunDispatchPausesAnUnsupportedModelWithoutChargingTheTask(t *testing.T) {
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
		return nil, &agent.ModelIncompatibilityError{
			Framework: "codex",
			Model:     "gpt-5.1-codex",
			Message:   "not available with your ChatGPT account",
		}
	})
	pause := &orchestrator.Pause{}
	cfg := orchestrator.Config{Pause: pause, Now: func() time.Time { return baseTime }}
	_, runErr := orchestrator.RunDispatch(ctx, store, fw, cfg, *task, d, nil, t.TempDir(), "", nil, baseTime)
	if _, ok := agent.ModelIncompatibility(runErr); !ok {
		t.Fatalf("RunDispatch err = %v, want model incompatibility preserved", runErr)
	}

	runs, err := store.Runs(ctx, "t1")
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %+v (%v)", runs, err)
	}
	if runs[0].Outcome != model.PausedOutcome {
		t.Fatalf("outcome = %q, want %q", runs[0].Outcome, model.PausedOutcome)
	}
	if !strings.Contains(runs[0].Detail, "gpt-5.1-codex") ||
		!strings.Contains(runs[0].Detail, "ChatGPT account") ||
		!strings.Contains(runs[0].Detail, "dispatch paused until") {
		t.Errorf("operator detail = %q", runs[0].Detail)
	}
	until, _ := pause.Until()
	if want := baseTime.Add(15 * time.Minute); !until.Equal(want) {
		t.Errorf("pause until = %s, want bounded retry at %s", until, want)
	}
	// The run named no connection, so what is paused is its team's
	// (the default) runs on its framework (the deployment's default).
	if !pause.Blocked(baseTime).Covers(orchestrator.PauseScope{Framework: model.AgentFrameworkAntigravity}) {
		t.Error("dispatch onto the refused credential remains open")
	}
	if pause.Blocked(until).Any() {
		t.Error("codex dispatch did not recover after the bounded pause")
	}
}
