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
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// A run stopped because its task's goal was met leaves the task queued
// for a moment, and "dispatch" runs before "task-goals" in the cycle: the
// next whole cycle must close the task rather than dispatch a new attempt
// that "task-goals" would stop a second later (task 5934's 5934-2).
func TestACycleAfterAGoalMetStopClosesTheTaskWithoutDispatchingIt(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	filedTask(t, ctx, store, "watcher", repo)
	filedTask(t, ctx, store, "dep", repo)
	if err := store.SetTaskGoal(ctx, "watcher", model.GoalDecl{Kind: model.GoalTasksClosed,
		Spec: model.GoalSpec{Tasks: []string{"dep"}}}, "human:alice", baseTime); err != nil {
		t.Fatal(err)
	}
	if err := store.ObserveField(ctx, "dep", baseTime, func(o *model.Observation) { o.ClosedAt = &baseTime }); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{ID: "watcher-1", TaskID: "watcher", Attempt: 1, StartedAt: baseTime},
		model.Limits{}); err != nil {
		t.Fatal(err)
	}

	dispatched := 0
	deps := orchestrator.Deps{
		Store:     store,
		Client:    client,
		Sandboxes: orchestrator.NewHostSandboxes(t.TempDir()),
		Framework: func(context.Context, orchestrator.AgentChoice) (agent.Framework, error) {
			dispatched++
			return stubFramework{result: toolResult(agent.ToolCall{
				Name: "comment_on_issue", Arguments: map[string]any{"comment": "done"},
			})}, nil
		},
		MaxWorkers: 1,
	}
	// The goal is met under the live run: it is warned, not dispatched.
	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if g, _ := store.TaskGoal(ctx, "watcher"); g == nil || g.MetAt == nil {
		t.Fatalf("watcher's goal = %+v after a cycle with dep closed, want met", g)
	}

	// The grace runs out and the run is stopped the way errGoalMet stops
	// one, returning the task to the queue.
	stoppedAt := baseTime.Add(time.Minute + model.PauseGracePeriod)
	if err := store.FinishRun(ctx, "watcher-1", stoppedAt, "cancelled",
		"orchestrator: run stopped because its task's goal was met"); err != nil {
		t.Fatal(err)
	}
	if err := orchestrator.RunCycle(ctx, deps, stoppedAt.Add(time.Minute)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if dispatched != 0 {
		t.Fatalf("dispatch started %d new attempt(s) of a task whose goal was met", dispatched)
	}
	if runs, _ := store.Runs(ctx, "watcher"); len(runs) != 1 {
		t.Fatalf("watcher has %d runs, want only the one its goal stopped", len(runs))
	}
	if st, _ := store.State(ctx, "watcher"); st != model.StateClosed {
		t.Fatalf("watcher is %s after the cycle following its goal-met stop, want closed", st)
	}
	if g, _ := store.TaskGoal(ctx, "watcher"); g == nil || g.Outcome != model.GoalMetOutcome {
		t.Fatalf("watcher's goal = %+v, want outcome %s", g, model.GoalMetOutcome)
	}
}
