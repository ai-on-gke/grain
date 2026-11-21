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
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

type syncClock struct {
	mu sync.Mutex
	at time.Time
}

func newSyncClock(at time.Time) *syncClock {
	return &syncClock{at: at}
}

func (c *syncClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *syncClock) Set(at time.Time) {
	c.mu.Lock()
	c.at = at
	c.mu.Unlock()
}

func TestRunCyclePassesPostRunCompletionTimeToAfterAgentRun(t *testing.T) {
	t.Parallel()
	const runDuration = 25 * time.Minute

	t.Run("completing task stamps CompletedAt and closing comment with post-run clock", func(t *testing.T) {
		t.Parallel()
		store, ctx := openStore(t)
		sim, client := newSim(t, "acme", "widgets", "main")
		repo := model.RepoRef{Owner: "acme", Name: "widgets"}
		task := filedTask(t, ctx, store, "t-complete", repo)

		t0 := baseTime
		clock := newSyncClock(t0)
		fw := agentFunc(func(_ context.Context, _ agent.RunConfig) (*agent.Result, error) {
			branch := model.BranchName(task.ID)
			pushBranch(t, sim.BareRepo, branch)
			pushFile(t, sim.BareRepo, branch, "pkg/ui/client.go", "package ui\n")
			clock.Set(t0.Add(runDuration))
			return &agent.Result{
				ToolCalls: []agent.ToolCall{
					{Name: "run_command", Text: "pushed"},
					{Name: "comment_on_issue", Arguments: map[string]any{"comment": "Updated UI client."}},
				},
			}, nil
		})

		deps := orchestrator.Deps{
			Store:      store,
			Client:     client,
			Sandboxes:  orchestrator.NewHostSandboxes(t.TempDir()),
			Framework:  orchestrator.StaticFramework(fw),
			MaxWorkers: 1,
			Config: orchestrator.Config{
				Now: clock.Now,
			},
		}
		if err := orchestrator.RunCycle(ctx, deps, t0); err != nil {
			t.Fatalf("RunCycle: %v", err)
		}

		wantFinish := t0.Add(runDuration)
		runs, err := store.Runs(ctx, task.ID)
		if err != nil || len(runs) != 1 {
			t.Fatalf("Runs(%s) = (%+v, %v), want 1 run", task.ID, runs, err)
		}
		if !runs[0].StartedAt.Equal(t0) {
			t.Fatalf("run.StartedAt = %v, want %v", runs[0].StartedAt, t0)
		}
		if runs[0].FinishedAt == nil || !runs[0].FinishedAt.Equal(wantFinish) {
			t.Fatalf("run.FinishedAt = %v, want %v", runs[0].FinishedAt, wantFinish)
		}

		obs, err := store.GetObservation(ctx, task.ID)
		if err != nil || obs == nil || obs.CompletedAt == nil {
			t.Fatalf("GetObservation(%s) = (%+v, %v), want non-nil CompletedAt", task.ID, obs, err)
		}
		if !obs.CompletedAt.Equal(wantFinish) {
			t.Fatalf("obs.CompletedAt = %v, want post-run clock %v (not dispatch clock %v)", *obs.CompletedAt, wantFinish, t0)
		}
		if got := obs.CompletedAt.Sub(runs[0].StartedAt); got != runDuration {
			t.Fatalf("CompletedAt.Sub(run.StartedAt) = %v, want %v", got, runDuration)
		}

		comments, err := store.Comments(ctx, task.ID)
		if err != nil || len(comments) == 0 {
			t.Fatalf("Comments(%s) = (%+v, %v), want closing comment", task.ID, comments, err)
		}
		closing := comments[len(comments)-1]
		if !closing.CreatedAt.Equal(wantFinish) {
			t.Fatalf("closing comment CreatedAt = %v, want %v", closing.CreatedAt, wantFinish)
		}
	})

	t.Run("trust-boundary review task CreatedAt and queued_at use post-run clock", func(t *testing.T) {
		t.Parallel()
		store, ctx := openStore(t)
		sim, client := newSim(t, "acme", "widgets", "main")
		repo := model.RepoRef{Owner: "acme", Name: "widgets"}
		task := filedTask(t, ctx, store, "t-sec", repo)
		task.AutoMerge = true
		if err := store.PutTask(ctx, task); err != nil {
			t.Fatal(err)
		}

		t0 := baseTime
		clock := newSyncClock(t0)
		fw := agentFunc(func(_ context.Context, cfg agent.RunConfig) (*agent.Result, error) {
			if strings.Contains(cfg.Prompt, "Security review:") {
				return &agent.Result{
					ToolCalls: []agent.ToolCall{
						{Name: "nothing_needed", Arguments: map[string]any{"reason": "clean"}},
					},
				}, nil
			}
			branch := model.BranchName(task.ID)
			pushBranch(t, sim.BareRepo, branch)
			pushFile(t, sim.BareRepo, branch, "pkg/gitproxy/proxy.go", "package gitproxy\n")
			clock.Set(t0.Add(runDuration))
			return &agent.Result{
				ToolCalls: []agent.ToolCall{{Name: "run_command", Text: "pushed"}},
			}, nil
		})

		deps := orchestrator.Deps{
			Store:      store,
			Client:     client,
			Sandboxes:  orchestrator.NewHostSandboxes(t.TempDir()),
			Framework:  orchestrator.StaticFramework(fw),
			MaxWorkers: 1,
			Config: orchestrator.Config{
				Now: clock.Now,
			},
		}
		if err := orchestrator.RunCycle(ctx, deps, t0); err != nil {
			t.Fatalf("RunCycle (parent): %v", err)
		}

		wantFinish := t0.Add(runDuration)
		parentRuns, err := store.Runs(ctx, task.ID)
		if err != nil || len(parentRuns) != 1 {
			t.Fatalf("Runs(%s) = (%+v, %v), want 1 run", task.ID, parentRuns, err)
		}
		obs, err := store.GetObservation(ctx, task.ID)
		if err != nil || obs == nil || obs.CompletedAt == nil {
			t.Fatalf("GetObservation(%s) = (%+v, %v), want non-nil CompletedAt", task.ID, obs, err)
		}
		if !obs.CompletedAt.Equal(wantFinish) {
			t.Fatalf("parent CompletedAt = %v, want %v (not dispatch clock %v)", *obs.CompletedAt, wantFinish, t0)
		}
		if got := obs.CompletedAt.Sub(parentRuns[0].StartedAt); got != runDuration {
			t.Fatalf("parent CompletedAt.Sub(run.StartedAt) = %v, want %v", got, runDuration)
		}

		reviewID := linkOf(reloaded(t, ctx, store, task.ID), model.LinkSecurityReviewTask)
		if reviewID == "" {
			t.Fatal("expected trust-boundary security review task to be filed")
		}
		review := reloaded(t, ctx, store, reviewID)
		if review.CreatedAt == nil || !review.CreatedAt.Equal(wantFinish) {
			t.Fatalf("security review CreatedAt = %v, want post-run clock %v (not parent dispatch %v)",
				review.CreatedAt, wantFinish, t0)
		}

		// Dispatch the security review on the next reconcile tick (10s after parent finish)
		// and verify its recorded queued_at is wantFinish (wait = 10s), not backdated to t0.
		const reconcileTick = 10 * time.Second
		tickAt := wantFinish.Add(reconcileTick)
		clock.Set(tickAt)
		if err := orchestrator.RunCycle(ctx, deps, tickAt); err != nil {
			t.Fatalf("RunCycle (security review): %v", err)
		}
		reviewRuns, err := store.Runs(ctx, reviewID)
		if err != nil || len(reviewRuns) != 1 {
			t.Fatalf("Runs(%s) = (%+v, %v), want 1 run", reviewID, reviewRuns, err)
		}
		if reviewRuns[0].QueuedAt == nil || !reviewRuns[0].QueuedAt.Equal(wantFinish) {
			t.Fatalf("security review run QueuedAt = %v, want %v (not backdated to %v)",
				reviewRuns[0].QueuedAt, wantFinish, t0)
		}
		if got := reviewRuns[0].StartedAt.Sub(*reviewRuns[0].QueuedAt); got != reconcileTick {
			t.Fatalf("security review queued -> dispatched = %v, want %v", got, reconcileTick)
		}
	})

	t.Run("async InFlight with nil Config.Now stamps post-run wall clock", func(t *testing.T) {
		t.Parallel()
		store, ctx := openStore(t)
		sim, client := newSim(t, "acme", "widgets", "main")
		repo := model.RepoRef{Owner: "acme", Name: "widgets"}
		task := filedTask(t, ctx, store, "t-async", repo)

		const agentSleep = 40 * time.Millisecond
		fw := agentFunc(func(_ context.Context, _ agent.RunConfig) (*agent.Result, error) {
			branch := model.BranchName(task.ID)
			pushBranch(t, sim.BareRepo, branch)
			pushFile(t, sim.BareRepo, branch, "pkg/ui/async.go", "package ui\n")
			time.Sleep(agentSleep)
			return &agent.Result{
				ToolCalls: []agent.ToolCall{
					{Name: "run_command", Text: "pushed"},
					{Name: "comment_on_issue", Arguments: map[string]any{"comment": "Done async."}},
				},
			}, nil
		})

		inFlight := &orchestrator.InFlight{}
		deps := orchestrator.Deps{
			Store:      store,
			Client:     client,
			Sandboxes:  orchestrator.NewHostSandboxes(t.TempDir()),
			Framework:  orchestrator.StaticFramework(fw),
			MaxWorkers: 1,
			Runs:       inFlight,
		}
		t0 := time.Now().UTC()
		if err := orchestrator.RunCycle(ctx, deps, t0); err != nil {
			t.Fatalf("RunCycle: %v", err)
		}
		if err := inFlight.Wait(ctx); err != nil {
			t.Fatalf("InFlight.Wait: %v", err)
		}

		runs, err := store.Runs(ctx, task.ID)
		if err != nil || len(runs) != 1 || runs[0].FinishedAt == nil {
			t.Fatalf("Runs(%s) = (%+v, %v), want 1 finished run", task.ID, runs, err)
		}
		obs, err := store.GetObservation(ctx, task.ID)
		if err != nil || obs == nil || obs.CompletedAt == nil {
			t.Fatalf("GetObservation(%s) = (%+v, %v), want non-nil CompletedAt", task.ID, obs, err)
		}
		if got := obs.CompletedAt.Sub(t0); got < agentSleep {
			t.Fatalf("obs.CompletedAt.Sub(t0) = %v, want >= %v", got, agentSleep)
		}
		if obs.CompletedAt.Before(*runs[0].FinishedAt) {
			t.Fatalf("obs.CompletedAt (%v) < run.FinishedAt (%v)", *obs.CompletedAt, *runs[0].FinishedAt)
		}
	})

	t.Run("synchronous RunCycle with nil Config.Now preserves synthetic now timeline", func(t *testing.T) {
		t.Parallel()
		store, ctx := openStore(t)
		sim, client := newSim(t, "acme", "widgets", "main")
		repo := model.RepoRef{Owner: "acme", Name: "widgets"}
		task := filedTask(t, ctx, store, "t-sync-timeline", repo)

		fw := agentFunc(func(_ context.Context, _ agent.RunConfig) (*agent.Result, error) {
			branch := model.BranchName(task.ID)
			pushBranch(t, sim.BareRepo, branch)
			pushFile(t, sim.BareRepo, branch, "pkg/ui/sync.go", "package ui\n")
			return &agent.Result{
				ToolCalls: []agent.ToolCall{
					{Name: "run_command", Text: "pushed"},
					{Name: "comment_on_issue", Arguments: map[string]any{"comment": "Done sync."}},
				},
			}, nil
		})

		deps := orchestrator.Deps{
			Store:      store,
			Client:     client,
			Sandboxes:  orchestrator.NewHostSandboxes(t.TempDir()),
			Framework:  orchestrator.StaticFramework(fw),
			MaxWorkers: 1,
		}
		if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
			t.Fatalf("RunCycle: %v", err)
		}

		obs, err := store.GetObservation(ctx, task.ID)
		if err != nil || obs == nil || obs.CompletedAt == nil {
			t.Fatalf("GetObservation(%s) = (%+v, %v), want non-nil CompletedAt", task.ID, obs, err)
		}
		if !obs.CompletedAt.Equal(baseTime) {
			t.Fatalf("obs.CompletedAt = %v, want synthetic cycle time %v", *obs.CompletedAt, baseTime)
		}
	})
}
