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
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// The 2026-10-02 outage replayed with words no marker knows: three
// tasks each fail within seconds, before any tool call, saying the same
// thing. The third pauses the framework, and all three are recorded as
// paused -- none of the three tasks spends an attempt on it.
func TestAnUnrecognisedRefusalBurstPausesTheFrameworkAndChargesNoTask(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	refusal := errors.New("claude: run ended in error (subtype=success): You've reached a brand new kind of limit (claude also exited non-zero: exit status 1 (stderr: ))")
	fw := agentFunc(func(context.Context, agent.RunConfig) (*agent.Result, error) {
		return &agent.Result{}, refusal
	})
	pause := &orchestrator.Pause{}
	now := baseTime
	cfg := orchestrator.Config{Pause: pause, Now: func() time.Time { return now }}

	for i := 1; i <= 3; i++ {
		taskID := fmt.Sprintf("burst%d", i)
		dispatchTask(t, ctx, store, taskID)
		d := dispatch.Dispatch{TaskID: taskID, RunID: taskID + "-1", Attempt: 1}
		startRun(t, ctx, store, d, now)
		task, err := store.GetTask(ctx, taskID)
		if err != nil || task == nil {
			t.Fatalf("reading task: %v", err)
		}
		if _, runErr := orchestrator.RunDispatch(ctx, store, fw, cfg, *task, d, nil, t.TempDir(), "", nil, now); runErr == nil {
			t.Fatal("RunDispatch err = nil, want the failure reported")
		}
		if paused := pause.Pauses(now); i < 3 && len(paused) != 0 {
			t.Fatalf("after %d failures paused %v, want nothing yet", i, paused)
		}
		now = now.Add(30 * time.Second)
	}

	if paused := pause.Pauses(now); len(paused) != 1 {
		t.Fatalf("paused %v, want the one credential the burst came from", paused)
	}
	for i := 1; i <= 3; i++ {
		runs, err := store.Runs(ctx, fmt.Sprintf("burst%d", i))
		if err != nil || len(runs) != 1 {
			t.Fatalf("runs of burst%d = %+v, %v", i, runs, err)
		}
		if runs[0].Outcome != model.PausedOutcome {
			t.Errorf("burst%d outcome = %q, want %q", i, runs[0].Outcome, model.PausedOutcome)
		}
		if !strings.Contains(runs[0].Detail, "brand new kind of limit") || !strings.Contains(runs[0].Detail, "paused until") {
			t.Errorf("burst%d detail = %q, want the provider's words and the pause", i, runs[0].Detail)
		}
	}
}

// Three runs pinned to a hostd that failed in a second each, in the same
// words, because grain could not carry their MCP endpoint to it: grain's
// own wiring, not a provider's refusal (docs/test-runs/2026-11-03-hostd-
// routing.md, F4). Nothing is paused -- in memory or in usage_limit_pause
// -- and each run is setup-failed.
func TestHostdSetupFailuresAreNotARefusalBurst(t *testing.T) {
	t.Parallel()
	for name, cause := range map[string]error{
		"not connected": hostd.ErrRemoteNotConnected,
		"old hostd":     hostd.ErrCarryUnsupported,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store, ctx := openStore(t)
			host := agent.HostdHost{Remote: failingCarrier{cause}}
			fw := agentFunc(func(context.Context, agent.RunConfig) (*agent.Result, error) {
				_, _, err := agent.CarryMCPEndpoint(host, "http://127.0.0.1:1/mcp", "tok")
				return &agent.Result{}, fmt.Errorf("antigravity: %w", err)
			})
			pause := &orchestrator.Pause{}
			now := baseTime
			if _, err := pause.Restore(ctx, store, now); err != nil {
				t.Fatal(err)
			}
			cfg := orchestrator.Config{Pause: pause, Now: func() time.Time { return now }}
			for i := 1; i <= 3; i++ {
				taskID := fmt.Sprintf("carry%d", i)
				dispatchTask(t, ctx, store, taskID)
				d := dispatch.Dispatch{TaskID: taskID, RunID: taskID + "-1", Attempt: 1}
				startRun(t, ctx, store, d, now)
				task, err := store.GetTask(ctx, taskID)
				if err != nil || task == nil {
					t.Fatalf("reading task: %v", err)
				}
				if _, runErr := orchestrator.RunDispatch(ctx, store, fw, cfg, *task, d, nil, t.TempDir(), "", nil, now); runErr == nil {
					t.Fatal("RunDispatch err = nil, want the failure reported")
				}
				now = now.Add(time.Second)
			}
			if paused := pause.Pauses(now); len(paused) != 0 {
				t.Errorf("paused %v, want nothing: the provider was never involved", paused)
			}
			if rows, err := store.ListUsageLimitPauses(ctx); err != nil || len(rows) != 0 {
				t.Errorf("usage_limit_pause rows = %+v, %v; want none", rows, err)
			}
			for i := 1; i <= 3; i++ {
				runs, err := store.Runs(ctx, fmt.Sprintf("carry%d", i))
				if err != nil || len(runs) != 1 {
					t.Fatalf("runs of carry%d = %+v, %v", i, runs, err)
				}
				if runs[0].Outcome != model.SetupFailedOutcome || !strings.Contains(runs[0].Detail, cause.Error()) {
					t.Errorf("carry%d = %q %q, want setup-failed with the hostd's error", i, runs[0].Outcome, runs[0].Detail)
				}
			}
		})
	}
}

// failingCarrier is a hostd link that cannot carry anything.
type failingCarrier struct{ err error }

func (c failingCarrier) CarryHTTP(context.Context, http.Handler) (string, func(), error) {
	return "", nil, c.err
}

// Runs that did some work before failing are an agent's failures, not a
// provider's refusal, however alike their words.
func TestFailuresAfterWorkAreNotARefusalBurst(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	fw := agentFunc(func(context.Context, agent.RunConfig) (*agent.Result, error) {
		return pushed(), errors.New("claude: exit status 1")
	})
	pause := &orchestrator.Pause{}
	cfg := orchestrator.Config{Pause: pause, Now: func() time.Time { return baseTime }}
	for i := 1; i <= 4; i++ {
		taskID := fmt.Sprintf("worked%d", i)
		dispatchTask(t, ctx, store, taskID)
		d := dispatch.Dispatch{TaskID: taskID, RunID: taskID + "-1", Attempt: 1}
		startRun(t, ctx, store, d, baseTime)
		task, _ := store.GetTask(ctx, taskID)
		orchestrator.RunDispatch(ctx, store, fw, cfg, *task, d, nil, t.TempDir(), "", nil, baseTime)
	}
	if paused := pause.Pauses(baseTime); len(paused) != 0 {
		t.Errorf("paused %v, want nothing: every run made a tool call", paused)
	}
}
