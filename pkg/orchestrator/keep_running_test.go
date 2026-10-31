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
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

func fileKeepRunningTask(t *testing.T, ctx context.Context, store *model.Store, id string) model.Task {
	t.Helper()
	human := model.Principal{Kind: model.PrincipalHuman, ID: "alice"}
	created := baseTime
	task := model.Task{
		ID:          id,
		Intent:      model.IntentAnalyze,
		Title:       "Keep running worker " + id,
		Body:        "Continuously monitor and report.",
		Origin:      model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
		Approval:    &model.Attribution{Actor: human},
		Binding:     model.BindingDirective,
		KeepRunning: true,
		CreatedAt:   &created,
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("PutTask(%s): %v", id, err)
	}
	return task
}

func TestKeepRunningTaskImmediatelyRequeuesAndRestartsOnRunExit(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	task := fileKeepRunningTask(t, ctx, store, "kr-loop")

	var attempts atomic.Int32
	fw := orchestrator.StaticFramework(agentFunc(func(_ context.Context, _ agent.RunConfig) (*agent.Result, error) {
		n := attempts.Add(1)
		switch n {
		case 1:
			return &agent.Result{
				FinalText: "turn 1 done",
				ToolCalls: []agent.ToolCall{{
					Name:      "comment_on_issue",
					Arguments: map[string]any{"comment": "report from turn 1"},
				}},
			}, nil
		case 2:
			return &agent.Result{
				FinalText: "turn 2 nothing needed",
				ToolCalls: []agent.ToolCall{{
					Name:      "nothing_needed",
					Arguments: map[string]any{"reason": "quiet on turn 2"},
				}},
			}, nil
		default:
			// Normal exit with plain text and zero tool calls.
			return &agent.Result{
				FinalText: fmt.Sprintf("turn %d plain exit", n),
			}, nil
		}
	}))

	deps := orchestrator.Deps{
		Store:      store,
		Sandboxes:  orchestrator.NewHostSandboxes(t.TempDir()),
		Framework:  fw,
		MaxWorkers: 1,
	}

	for cycle := 1; cycle <= 3; cycle++ {
		now := baseTime.Add(time.Duration(cycle) * time.Second)
		if err := orchestrator.RunCycle(ctx, deps, now); err != nil {
			t.Fatalf("cycle %d: %v", cycle, err)
		}
		if got := int(attempts.Load()); got != cycle {
			t.Fatalf("after cycle %d: attempts = %d, want %d", cycle, got, cycle)
		}
		st, err := store.State(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if st != model.StateQueued {
			t.Fatalf("after cycle %d: state = %q, want %q", cycle, st, model.StateQueued)
		}
		streak, err := store.FailureStreak(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if streak.Count != 0 || streak.BackoffCount != 0 {
			t.Fatalf("after cycle %d: streak = %+v; want Count=0 and BackoffCount=0 so restart is immediate", cycle, streak)
		}
	}

	runs, err := store.Runs(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 3 {
		t.Fatalf("len(runs) = %d, want 3", len(runs))
	}
	for i, r := range runs {
		if r.Outcome != "succeeded" && r.Outcome != model.NothingNeededOutcome {
			t.Errorf("run %d outcome = %q, want succeeded or nothing_needed", i+1, r.Outcome)
		}
	}
}

func TestKeepRunningTaskWithPersonClosesGoalRestartsInsteadOfResting(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	task := fileKeepRunningTask(t, ctx, store, "kr-goal")
	if err := store.SetTaskGoal(ctx, task.ID, model.GoalDecl{Kind: model.GoalPersonCloses}, "human:alice", baseTime); err != nil {
		t.Fatal(err)
	}

	var attempts atomic.Int32
	fw := orchestrator.StaticFramework(agentFunc(func(_ context.Context, _ agent.RunConfig) (*agent.Result, error) {
		n := attempts.Add(1)
		return &agent.Result{
			FinalText: fmt.Sprintf("standing turn %d", n),
		}, nil
	}))
	deps := orchestrator.Deps{
		Store:      store,
		Sandboxes:  orchestrator.NewHostSandboxes(t.TempDir()),
		Framework:  fw,
		MaxWorkers: 1,
	}

	// Cycle 1 & 2: exits without calling ask_question, but because KeepRunning is true,
	// it must not rest on GoalPersonCloses and must immediately run again on cycle 2.
	for cycle := 1; cycle <= 2; cycle++ {
		now := baseTime.Add(time.Duration(cycle) * time.Second)
		if err := orchestrator.RunCycle(ctx, deps, now); err != nil {
			t.Fatalf("cycle %d: %v", cycle, err)
		}
		if got := int(attempts.Load()); got != cycle {
			t.Fatalf("after cycle %d: attempts = %d, want %d", cycle, got, cycle)
		}
		st, err := store.State(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if st != model.StateQueued {
			t.Fatalf("after cycle %d: state = %q, want %q", cycle, st, model.StateQueued)
		}
	}

	// Toggle keep_running off: the next cycle must settle the task onto its goal without running a 3rd attempt.
	if err := store.UpdateTask(ctx, task.ID, func(tk *model.Task) error {
		tk.KeepRunning = false
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(3*time.Second)); err != nil {
		t.Fatalf("cycle 3: %v", err)
	}
	if got := int(attempts.Load()); got != 2 {
		t.Fatalf("attempts after clearing keep_running = %d, want 2", got)
	}
	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateDeferred {
		t.Fatalf("state after clearing keep_running on goal task = %q, want %q", st, model.StateDeferred)
	}
}

func TestKeepRunningTaskStopsWhenClosedDeferredOrParked(t *testing.T) {
	t.Parallel()

	t.Run("operator close stops restarts", func(t *testing.T) {
		t.Parallel()
		store, ctx := openStore(t)
		task := fileKeepRunningTask(t, ctx, store, "kr-close")
		var attempts atomic.Int32
		deps := orchestrator.Deps{
			Store:     store,
			Sandboxes: orchestrator.NewHostSandboxes(t.TempDir()),
			Framework: orchestrator.StaticFramework(agentFunc(func(_ context.Context, _ agent.RunConfig) (*agent.Result, error) {
				attempts.Add(1)
				return &agent.Result{FinalText: "turn done"}, nil
			})),
			MaxWorkers: 1,
		}
		if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		closedAt := baseTime.Add(2 * time.Second)
		if err := store.ObserveField(ctx, task.ID, closedAt, func(o *model.Observation) {
			o.ClosedAt = &closedAt
		}); err != nil {
			t.Fatal(err)
		}
		if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(3*time.Second)); err != nil {
			t.Fatal(err)
		}
		if got := int(attempts.Load()); got != 1 {
			t.Fatalf("attempts = %d, want 1 after operator close", got)
		}
		if st, _ := store.State(ctx, task.ID); st != model.StateClosed {
			t.Fatalf("state = %q, want %q", st, model.StateClosed)
		}
	})

	t.Run("operator defer stops restarts", func(t *testing.T) {
		t.Parallel()
		store, ctx := openStore(t)
		task := fileKeepRunningTask(t, ctx, store, "kr-defer")
		var attempts atomic.Int32
		deps := orchestrator.Deps{
			Store:     store,
			Sandboxes: orchestrator.NewHostSandboxes(t.TempDir()),
			Framework: orchestrator.StaticFramework(agentFunc(func(_ context.Context, _ agent.RunConfig) (*agent.Result, error) {
				attempts.Add(1)
				return &agent.Result{FinalText: "turn done"}, nil
			})),
			MaxWorkers: 1,
		}
		if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		deferredAt := baseTime.Add(2 * time.Second)
		if err := store.SetDeferred(ctx, task.ID, &deferredAt, nil, deferredAt); err != nil {
			t.Fatal(err)
		}
		if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(3*time.Second)); err != nil {
			t.Fatal(err)
		}
		if got := int(attempts.Load()); got != 1 {
			t.Fatalf("attempts = %d, want 1 after operator defer", got)
		}
		if st, _ := store.State(ctx, task.ID); st != model.StateDeferred {
			t.Fatalf("state = %q, want %q", st, model.StateDeferred)
		}
	})

	t.Run("clearing keep_running completes task without another run", func(t *testing.T) {
		t.Parallel()
		store, ctx := openStore(t)
		task := fileKeepRunningTask(t, ctx, store, "kr-clear")
		var attempts atomic.Int32
		deps := orchestrator.Deps{
			Store:     store,
			Sandboxes: orchestrator.NewHostSandboxes(t.TempDir()),
			Framework: orchestrator.StaticFramework(agentFunc(func(_ context.Context, _ agent.RunConfig) (*agent.Result, error) {
				attempts.Add(1)
				return &agent.Result{
					FinalText: "turn done",
					ToolCalls: []agent.ToolCall{{
						Name:      "comment_on_issue",
						Arguments: map[string]any{"comment": "finished step"},
					}},
				}, nil
			})),
			MaxWorkers: 1,
		}
		if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if err := store.UpdateTask(ctx, task.ID, func(tk *model.Task) error {
			tk.KeepRunning = false
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(2*time.Second)); err != nil {
			t.Fatal(err)
		}
		if got := int(attempts.Load()); got != 1 {
			t.Fatalf("attempts = %d, want 1 after clearing keep_running", got)
		}
		obs, err := store.GetObservation(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if obs == nil || obs.CompletedAt == nil {
			t.Fatalf("obs = %+v, want CompletedAt set once keep_running is cleared", obs)
		}
		if st, _ := store.State(ctx, task.ID); st != model.StateAwaitingReply {
			t.Fatalf("state = %q, want %q", st, model.StateAwaitingReply)
		}
	})

	t.Run("explicit ask_question still parks task", func(t *testing.T) {
		t.Parallel()
		store, ctx := openStore(t)
		task := fileKeepRunningTask(t, ctx, store, "kr-question")
		var attempts atomic.Int32
		deps := orchestrator.Deps{
			Store:     store,
			Sandboxes: orchestrator.NewHostSandboxes(t.TempDir()),
			Framework: orchestrator.StaticFramework(agentFunc(func(_ context.Context, _ agent.RunConfig) (*agent.Result, error) {
				attempts.Add(1)
				return &agent.Result{
					ToolCalls: []agent.ToolCall{{
						Name: "ask_question",
						Arguments: map[string]any{
							"question": "Which region should I target?",
							"summary":  "Pick target region",
						},
					}},
				}, nil
			})),
			MaxWorkers: 1,
		}
		if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if st, _ := store.State(ctx, task.ID); st != model.StateAwaitingReply {
			t.Fatalf("state = %q, want %q when agent explicitly asks a question", st, model.StateAwaitingReply)
		}
		if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(2*time.Second)); err != nil {
			t.Fatal(err)
		}
		if got := int(attempts.Load()); got != 1 {
			t.Fatalf("attempts = %d, want 1 while awaiting reply", got)
		}
	})
}

func TestKeepRunningTaskKeepsSessionSandboxWarmAcrossRestarts(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	task := fileKeepRunningTask(t, ctx, store, "kr-warm-sb")

	sbPool := &countingSandboxes{inner: orchestrator.NewHostSandboxes(t.TempDir())}
	var reusedFlags []bool
	var attempts atomic.Int32
	deps := orchestrator.Deps{
		Store:     store,
		Sandboxes: sbPool,
		Framework: orchestrator.StaticFramework(agentFunc(func(_ context.Context, cfg agent.RunConfig) (*agent.Result, error) {
			n := attempts.Add(1)
			reusedFlags = append(reusedFlags, cfg.SandboxReused)
			return &agent.Result{
				FinalText:        fmt.Sprintf("turn %d complete", n),
				ResumableSession: "sess-warm-1",
			}, nil
		})),
		MaxWorkers: 1,
		Config: orchestrator.Config{
			SessionSandboxIdleTTL: 30 * time.Minute,
		},
	}

	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(time.Second)); err != nil {
		t.Fatalf("cycle 1: %v", err)
	}
	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(2*time.Second)); err != nil {
		t.Fatalf("cycle 2: %v", err)
	}

	if got := int(attempts.Load()); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
	if sbPool.acquires != 1 {
		t.Fatalf("sandbox acquires = %d, want 1 (warm sandbox reused across restart)", sbPool.acquires)
	}
	if sbPool.releases != 0 {
		t.Fatalf("sandbox releases = %d, want 0 while warm sandbox is parked", sbPool.releases)
	}
	if len(reusedFlags) != 2 || reusedFlags[0] || !reusedFlags[1] {
		t.Fatalf("SandboxReused flags across attempts = %v, want [false, true]", reusedFlags)
	}
	_ = task
}

type failingAcquireSandboxes struct {
	err error
}

func (f *failingAcquireSandboxes) Acquire(context.Context, string, orchestrator.Shape) (orchestrator.Sandbox, error) {
	return nil, f.err
}

func TestKeepRunningSetupFailureStillCountsTowardStreak(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	task := fileKeepRunningTask(t, ctx, store, "kr-setup-fail")

	deps := orchestrator.Deps{
		Store:     store,
		Sandboxes: &failingAcquireSandboxes{err: errors.New("sandbox quota exceeded")},
		Framework: orchestrator.StaticFramework(agentFunc(func(_ context.Context, _ agent.RunConfig) (*agent.Result, error) {
			t.Fatal("agent should not run when sandbox acquire fails")
			return nil, nil
		})),
		MaxWorkers: 1,
	}

	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(time.Second)); err == nil {
		t.Fatal("cycle 1 expected setup error from failingAcquireSandboxes, got nil")
	}
	streak, err := store.FailureStreak(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if streak.Count != 1 || streak.BackoffCount != 1 {
		t.Fatalf("streak = %+v; want Count=1 and BackoffCount=1 on setup failure", streak)
	}
}

func TestKeepRunningConsoleSessionRestartsWhenTurnExits(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	settings(t, store, func(cfg *model.Config) {
		cfg.ConsoleSessions = []model.ConsoleSession{{
			ID:            model.DirectorSessionID,
			Title:         "Director",
			Capabilities:  []string{},
			NetworkAccess: model.NetworkOpen,
			KeepRunning:   true,
		}}
	})
	runSessions(t, store, baseTime)
	director := sessionTasks(t, store)[0]
	if !director.KeepRunning {
		t.Fatal("director task KeepRunning = false, want true")
	}

	var attempts atomic.Int32
	deps := orchestrator.Deps{
		Store:     store,
		Sandboxes: orchestrator.NewHostSandboxes(t.TempDir()),
		Framework: orchestrator.StaticFramework(agentFunc(func(_ context.Context, _ agent.RunConfig) (*agent.Result, error) {
			n := attempts.Add(1)
			return &agent.Result{
				FinalText: fmt.Sprintf("director turn %d", n),
			}, nil
		})),
		MaxWorkers: 1,
		Config: orchestrator.Config{
			StateRepo: func() model.RepoRef { return model.RepoRef{Owner: "acme", Name: "grain-home"} },
		},
	}

	// Wake the director session with an initial operator message.
	replyToSession(t, store, director.ID, baseTime.Add(time.Minute))

	// Cycle 1 runs turn 1 and leaves the session queued because KeepRunning is true.
	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(2*time.Minute)); err != nil {
		t.Fatalf("cycle 1: %v", err)
	}
	if got := int(attempts.Load()); got != 1 {
		t.Fatalf("after cycle 1: attempts = %d, want 1", got)
	}
	if st, _ := store.State(ctx, director.ID); st != model.StateQueued {
		t.Fatalf("after cycle 1: state = %q, want %q", st, model.StateQueued)
	}

	// Cycle 2 immediately runs turn 2 without needing another human reply.
	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(2*time.Minute+time.Second)); err != nil {
		t.Fatalf("cycle 2: %v", err)
	}
	if got := int(attempts.Load()); got != 2 {
		t.Fatalf("after cycle 2: attempts = %d, want 2", got)
	}

	// Turn KeepRunning off on the console session: next cycle parks it on _Waiting for you._
	settings(t, store, func(cfg *model.Config) {
		cfg.ConsoleSessions = []model.ConsoleSession{{
			ID:            model.DirectorSessionID,
			Title:         "Director",
			Capabilities:  []string{},
			NetworkAccess: model.NetworkOpen,
			KeepRunning:   false,
		}}
	})
	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(3*time.Minute)); err != nil {
		t.Fatalf("cycle 3: %v", err)
	}
	if got := int(attempts.Load()); got != 2 {
		t.Fatalf("after cycle 3: attempts = %d, want 2", got)
	}
	if st, _ := store.State(ctx, director.ID); st != model.StateAwaitingReply {
		t.Fatalf("after cycle 3: state = %q, want %q", st, model.StateAwaitingReply)
	}
}

func TestKeepRunningWarmSandboxReclaimedOnToggleOffOrWithdrawnRepoAccess(t *testing.T) {
	t.Parallel()

	t.Run("clearing keep_running mid-run does not park warm sandbox", func(t *testing.T) {
		t.Parallel()
		store, ctx := openStore(t)
		task := fileKeepRunningTask(t, ctx, store, "kr-midrun-off")

		sbPool := &countingSandboxes{inner: orchestrator.NewHostSandboxes(t.TempDir())}
		var revoked atomic.Int32
		deps := orchestrator.Deps{
			Store:     store,
			Sandboxes: sbPool,
			Framework: orchestrator.StaticFramework(agentFunc(func(_ context.Context, _ agent.RunConfig) (*agent.Result, error) {
				if err := store.UpdateTask(ctx, task.ID, func(tk *model.Task) error {
					tk.KeepRunning = false
					return nil
				}); err != nil {
					return nil, err
				}
				return &agent.Result{
					FinalText:        "done",
					ResumableSession: "sess-midrun",
					ToolCalls: []agent.ToolCall{{
						Name:      "comment_on_issue",
						Arguments: map[string]any{"comment": "finished before toggle took effect"},
					}},
				}, nil
			})),
			MintSandboxToken:   func(string) (string, error) { return "tok", nil },
			RevokeSandboxToken: func(string) error { revoked.Add(1); return nil },
			MaxWorkers:         1,
			Config: orchestrator.Config{
				GitRemoteBase:         "http://proxy.example",
				SessionSandboxIdleTTL: 30 * time.Minute,
			},
		}

		if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(time.Second)); err != nil {
			t.Fatalf("cycle 1: %v", err)
		}
		if got := orchestrator.WarmSessionSandboxID(store, task.ID); got != "" {
			t.Fatalf("WarmSessionSandboxID = %q, want empty when KeepRunning was cleared mid-run", got)
		}
		if sbPool.releases != 1 {
			t.Fatalf("sandbox releases = %d, want 1", sbPool.releases)
		}
		if got := int(revoked.Load()); got != 1 {
			t.Fatalf("revoked tokens = %d, want 1", got)
		}
		if st, _ := store.State(ctx, task.ID); st != model.StateAwaitingReply {
			t.Fatalf("state = %q, want %q", st, model.StateAwaitingReply)
		}
	})

	t.Run("clearing keep_running between cycles releases warm sandbox and revokes token", func(t *testing.T) {
		t.Parallel()
		store, ctx := openStore(t)
		task := fileKeepRunningTask(t, ctx, store, "kr-between-off")

		sbPool := &countingSandboxes{inner: orchestrator.NewHostSandboxes(t.TempDir())}
		var revoked atomic.Int32
		var configs []agent.RunConfig
		deps := orchestrator.Deps{
			Store:     store,
			Sandboxes: sbPool,
			Framework: orchestrator.StaticFramework(agentFunc(func(_ context.Context, cfg agent.RunConfig) (*agent.Result, error) {
				configs = append(configs, cfg)
				return &agent.Result{
					FinalText:        "step done",
					ResumableSession: "sess-between",
					ToolCalls: []agent.ToolCall{{
						Name:      "comment_on_issue",
						Arguments: map[string]any{"comment": "reported"},
					}},
				}, nil
			})),
			MintSandboxToken:   func(string) (string, error) { return "tok", nil },
			RevokeSandboxToken: func(string) error { revoked.Add(1); return nil },
			MaxWorkers:         1,
			Config: orchestrator.Config{
				GitRemoteBase:         "http://proxy.example",
				SessionSandboxIdleTTL: 30 * time.Minute,
			},
		}

		if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(time.Second)); err != nil {
			t.Fatalf("cycle 1: %v", err)
		}
		if got := orchestrator.WarmSessionSandboxID(store, task.ID); got == "" {
			t.Fatal("expected warm sandbox parked after cycle 1")
		}
		if sbPool.releases != 0 || revoked.Load() != 0 {
			t.Fatalf("after cycle 1: releases=%d revoked=%d, want 0/0", sbPool.releases, revoked.Load())
		}

		if err := store.UpdateTask(ctx, task.ID, func(tk *model.Task) error {
			tk.KeepRunning = false
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(2*time.Second)); err != nil {
			t.Fatalf("cycle 2: %v", err)
		}
		if got := orchestrator.WarmSessionSandboxID(store, task.ID); got != "" {
			t.Fatalf("WarmSessionSandboxID after clearing KeepRunning = %q, want empty", got)
		}
		if sbPool.releases != 1 || revoked.Load() != 1 {
			t.Fatalf("after clearing KeepRunning: releases=%d revoked=%d, want 1/1", sbPool.releases, revoked.Load())
		}

		// A human reply wakes the now non-keep-running task: it must acquire a fresh
		// sandbox, report that the previous session sandbox was reclaimed, and release
		// the new sandbox at the end of the turn.
		replyToSession(t, store, task.ID, baseTime.Add(3*time.Second))
		if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(4*time.Second)); err != nil {
			t.Fatalf("cycle 3: %v", err)
		}
		if len(configs) != 2 {
			t.Fatalf("runs = %d, want 2", len(configs))
		}
		if configs[1].SandboxReused {
			t.Fatal("turn 2 SandboxReused = true, want false")
		}
		if !strings.Contains(configs[1].Prompt, "The sandbox is not the same one") {
			t.Errorf("turn 2 Prompt missing reclaimed-sandbox orientation:\n%s", configs[1].Prompt)
		}
		if sbPool.acquires != 2 || sbPool.releases != 2 {
			t.Fatalf("after cycle 3: acquires=%d releases=%d, want 2/2", sbPool.acquires, sbPool.releases)
		}
	})

	t.Run("withdrawing read repo between cycles releases warm sandbox", func(t *testing.T) {
		t.Parallel()
		store, ctx := openStore(t)
		task := fileKeepRunningTask(t, ctx, store, "kr-read-withdrawn")
		if err := store.UpdateTask(ctx, task.ID, func(tk *model.Task) error {
			tk.Reads = []model.RepoRef{{Owner: "acme", Name: "private-lib"}}
			return nil
		}); err != nil {
			t.Fatal(err)
		}

		sbPool := &countingSandboxes{inner: orchestrator.NewHostSandboxes(t.TempDir())}
		var reused []bool
		deps := orchestrator.Deps{
			Store:     store,
			Sandboxes: sbPool,
			Framework: orchestrator.StaticFramework(agentFunc(func(_ context.Context, cfg agent.RunConfig) (*agent.Result, error) {
				reused = append(reused, cfg.SandboxReused)
				return &agent.Result{
					FinalText:        "step",
					ResumableSession: "sess-reads",
				}, nil
			})),
			MaxWorkers: 1,
			Config: orchestrator.Config{
				SessionSandboxIdleTTL: 30 * time.Minute,
			},
		}

		if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(time.Second)); err != nil {
			t.Fatalf("cycle 1: %v", err)
		}
		firstWarm := orchestrator.WarmSessionSandboxID(store, task.ID)
		if firstWarm == "" {
			t.Fatal("expected warm sandbox after cycle 1")
		}

		if err := store.UpdateTask(ctx, task.ID, func(tk *model.Task) error {
			tk.Reads = nil
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(2*time.Second)); err != nil {
			t.Fatalf("cycle 2: %v", err)
		}
		if sbPool.acquires != 2 || sbPool.releases != 1 {
			t.Fatalf("after withdrawing Reads: acquires=%d releases=%d, want 2/1", sbPool.acquires, sbPool.releases)
		}
		if len(reused) != 2 || reused[0] || reused[1] {
			t.Fatalf("SandboxReused = %v, want [false, false]", reused)
		}
		if secondWarm := orchestrator.WarmSessionSandboxID(store, task.ID); secondWarm == "" || secondWarm == firstWarm {
			t.Fatalf("secondWarm = %q, want a new warm sandbox distinct from %q", secondWarm, firstWarm)
		}
	})
}
