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
