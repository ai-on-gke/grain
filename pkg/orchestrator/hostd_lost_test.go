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
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// A run whose CLI's hostd dropped and was not back within the reattach
// timeout (agent.ErrHostdLost) ends as an infrastructure failure:
// interrupted, with no strike against its task.
func TestRunDispatchRecordsALostHostdAsInterruptedWithoutAStrike(t *testing.T) {
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
		return nil, fmt.Errorf("running claude: %w", fmt.Errorf("%w: grain hostd ws-a dropped run r1's CLI (p-1) at cursor 12 and did not reconnect within 15m0s",
			agent.ErrHostdLost))
	})
	cfg := orchestrator.Config{Pause: &orchestrator.Pause{}, Now: func() time.Time { return baseTime }}
	if _, runErr := orchestrator.RunDispatch(ctx, store, fw, cfg, *task, d, nil, t.TempDir(), "", nil, baseTime); !errors.Is(runErr, agent.ErrHostdLost) {
		t.Fatalf("RunDispatch err = %v, want it to carry ErrHostdLost", runErr)
	}
	runs, err := store.Runs(ctx, "t1")
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %+v, %v; want exactly one", runs, err)
	}
	if runs[0].Outcome != model.InterruptedOutcome {
		t.Errorf("run outcome = %q, want %q", runs[0].Outcome, model.InterruptedOutcome)
	}
	if !strings.Contains(runs[0].Detail, "did not reconnect within 15m0s") {
		t.Errorf("run detail = %q, want it to say the hostd did not come back", runs[0].Detail)
	}
	streak, err := store.FailureStreak(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if streak.Count != 0 {
		t.Errorf("failure streak after a lost hostd = %+v, want no strike", streak)
	}
}
