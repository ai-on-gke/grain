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

// An operator's pause (model.TaskPause, ui.Client.Pause) against real
// cycles: a paused task is not dispatched and holds no worker, a paused
// run is ended -- at once, after its grace period, or by itself -- as an
// interrupted run that costs no strike, and resuming gives back the
// run-now request the paused run had spent.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

func pauseTask(t *testing.T, store *model.Store, p model.TaskPause) {
	t.Helper()
	paused, err := store.Pause(t.Context(), p)
	if err != nil {
		t.Fatalf("pausing %s: %v", p.TaskID, err)
	}
	if !paused {
		t.Fatalf("pausing %s: it was already paused", p.TaskID)
	}
}

func resumeTask(t *testing.T, store *model.Store, id string, at time.Time) *model.TaskPause {
	t.Helper()
	lifted, err := store.Resume(t.Context(), id, at)
	if err != nil {
		t.Fatalf("resuming %s: %v", id, err)
	}
	if lifted == nil {
		t.Fatalf("resuming %s: it was not paused", id)
	}
	return lifted
}

func requestRunNow(t *testing.T, store *model.Store, id string, at time.Time) {
	t.Helper()
	if err := store.ObserveField(t.Context(), id, at, func(o *model.Observation) { o.RunNowAt = &at }); err != nil {
		t.Fatal(err)
	}
}

func runNowOf(t *testing.T, store *model.Store, id string) *time.Time {
	t.Helper()
	obs, err := store.GetObservation(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil {
		return nil
	}
	return obs.RunNowAt
}

func liveRunID(t *testing.T, store *model.Store, id string) string {
	t.Helper()
	runs, err := store.Runs(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range runs {
		if r.FinishedAt == nil {
			return r.ID
		}
	}
	t.Fatalf("%s has no live run", id)
	return ""
}

// waitFinished waits for taskID's only run to finish and returns it.
func waitFinished(t *testing.T, ctx context.Context, store *model.Store, runs *orchestrator.InFlight, taskID string) model.Run {
	t.Helper()
	drainRuns(t, ctx, runs)
	all, err := store.Runs(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].FinishedAt == nil {
		t.Fatalf("%s's runs = %+v, want one finished run", taskID, all)
	}
	return all[0]
}

func noStrike(t *testing.T, store *model.Store, taskID string) {
	t.Helper()
	streak, err := store.FailureStreak(t.Context(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	if streak.Count != 0 {
		t.Fatalf("%s's failure streak is %d after a paused run, want 0", taskID, streak.Count)
	}
}

func pausedDeps(t *testing.T, store *model.Store, fw agent.Framework, maxWorkers int) (orchestrator.Deps, *orchestrator.InFlight) {
	t.Helper()
	deps, runs := asyncDeps(t, store, fw, maxWorkers)
	deps.Config.TaskCancellers = orchestrator.NewTaskCancellers()
	return deps, runs
}

// A paused task is never dispatched, whatever asks for it -- a free worker
// or its own run-now request -- and holds no worker while it waits: the
// one worker goes to the task behind it. Resuming puts it back exactly as
// it was, run-now request included, and the next cycle starts it.
func TestAPausedTaskIsNotDispatchedAndResumesWithItsStanding(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	filedTask(t, ctx, store, "t1", repo)
	filedTask(t, ctx, store, "t2", repo)
	requestRunNow(t, store, "t1", baseTime)
	before, err := store.GetTask(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	pauseTask(t, store, model.TaskPause{TaskID: "t1", PausedAt: baseTime, FromState: model.StateQueued,
		PausedBy: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}})
	stateIs(t, store, "t1", model.StateQueued)

	fw := newBlockingFramework()
	deps, runs := pausedDeps(t, store, fw, 1)
	cycle(t, ctx, deps)
	notDispatched(t, store, "t1", "while it was paused, with a run-now request and a free worker")
	enteredRun(t, fw, "t2-1")
	fw.releaseAll()
	drainRuns(t, ctx, runs)

	resumeTask(t, store, "t1", baseTime.Add(time.Minute))
	after, err := store.GetTask(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if after.OrderKey != before.OrderKey || (after.Approval == nil) != (before.Approval == nil) {
		t.Fatalf("resuming changed t1's standing: order %v -> %v, approved %v -> %v",
			before.OrderKey, after.OrderKey, before.Approval != nil, after.Approval != nil)
	}
	if got := runNowOf(t, store, "t1"); got == nil || !got.Equal(baseTime) {
		t.Fatalf("t1's run-now request after resume = %v, want %v", got, baseTime)
	}
	cycleAt(t, ctx, deps, baseTime.Add(time.Minute))
	enteredRun(t, fw, "t1-1")
	drainRuns(t, ctx, runs)
}

// An immediate pause of a running task cancels its run on the next cycle.
// The run is recorded as interrupted with the pause's own detail, costs no
// strike, and the task reads queued again (not parked, as Stop leaves it).
// The run-now request the run spent when it started is given back when
// the run ends, and is only acted on once the task is resumed.
func TestAnImmediatePauseEndsTheRunWithoutAStrikeAndResumeGivesBackRunNow(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	filedTask(t, ctx, store, "t1", repo)
	requestRunNow(t, store, "t1", baseTime)

	fw := newBlockingFramework()
	deps, runs := pausedDeps(t, store, fw, 1)
	cycle(t, ctx, deps)
	enteredRun(t, fw, "t1-1")
	if got := runNowOf(t, store, "t1"); got != nil {
		t.Fatalf("the run did not spend t1's run-now request: %v", got)
	}

	pauseTask(t, store, model.TaskPause{TaskID: "t1", PausedAt: baseTime, FromState: model.StateRunning,
		Immediate: true, RunID: liveRunID(t, store, "t1")})
	cycle(t, ctx, deps)
	run := waitFinished(t, ctx, store, runs, "t1")
	if run.Outcome != model.InterruptedOutcome {
		t.Fatalf("the paused run's outcome = %q (%s), want %q", run.Outcome, run.Detail, model.InterruptedOutcome)
	}
	noStrike(t, store, "t1")
	stateIs(t, store, "t1", model.StateQueued)
	cycle(t, ctx, deps)
	if all, _ := store.Runs(ctx, "t1"); len(all) != 1 {
		t.Fatalf("t1 was dispatched again while paused: %d runs", len(all))
	}
	// The interrupted run gave its request back (task 837's rule,
	// model.RunNowSurvives), and it stands unused while t1 is paused: the
	// cycle above dispatched nothing on it.
	if got := runNowOf(t, store, "t1"); got == nil || !got.Equal(baseTime) {
		t.Fatalf("t1's run-now request while paused = %v, want %v given back by the interrupted run", got, baseTime)
	}
	comments, err := store.Comments(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	kept := false
	for _, c := range comments {
		kept = kept || strings.Contains(c.Body, "once the task is resumed")
	}
	if !kept {
		t.Fatalf("no Run now kept note saying the request waits for the resume: %+v", comments)
	}

	resumeTask(t, store, "t1", baseTime.Add(time.Minute))
	if got := runNowOf(t, store, "t1"); got == nil || !got.Equal(baseTime) {
		t.Fatalf("t1's run-now request after resume = %v, want %v still", got, baseTime)
	}
	cycleAt(t, ctx, deps, baseTime.Add(time.Minute))
	enteredRun(t, fw, "t1-2")
	fw.releaseAll()
	drainRuns(t, ctx, runs)
}

// A graceful pause leaves the run alone until its grace period has passed,
// then cancels it the way an immediate one does.
func TestAGracefulPauseWaitsOutItsGracePeriod(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	filedTask(t, ctx, store, "t1", repo)

	fw := newBlockingFramework()
	deps, runs := pausedDeps(t, store, fw, 1)
	cycle(t, ctx, deps)
	enteredRun(t, fw, "t1-1")

	grace := baseTime.Add(model.PauseGracePeriod)
	pauseTask(t, store, model.TaskPause{TaskID: "t1", PausedAt: baseTime, FromState: model.StateRunning,
		GraceUntil: &grace, RunID: liveRunID(t, store, "t1")})
	cycleAt(t, ctx, deps, grace.Add(-time.Second))
	stateIs(t, store, "t1", model.StateRunning)

	cycleAt(t, ctx, deps, grace)
	run := waitFinished(t, ctx, store, runs, "t1")
	if run.Outcome != model.InterruptedOutcome {
		t.Fatalf("the paused run's outcome = %q (%s), want %q", run.Outcome, run.Detail, model.InterruptedOutcome)
	}
	noStrike(t, store, "t1")
}

// stoppingFramework is an agent that, once released, stops without an
// answer -- what a run told to wrap up for a pause does: push, hand over,
// stop.
type stoppingFramework struct{ *blockingFramework }

func (f stoppingFramework) Run(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	f.entered <- filepath.Base(cfg.SandboxRoot)
	select {
	case <-f.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return toolResult(agent.ToolCall{Name: "write_handover", Arguments: map[string]any{"handover": "paused here"}}), nil
}

// A run that stops by itself inside the grace period ends the wait early:
// nothing is cancelled, and the run it ended is still not a failure, nor a
// completion -- the task is not finished, it was asked to stop.
func TestARunThatStopsDuringItsGracePeriodIsInterruptedNotFailed(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	filedTask(t, ctx, store, "t1", repo)

	fw := stoppingFramework{newBlockingFramework()}
	deps, runs := pausedDeps(t, store, fw, 1)
	cycle(t, ctx, deps)
	enteredRun(t, fw.blockingFramework, "t1-1")

	grace := baseTime.Add(model.PauseGracePeriod)
	pauseTask(t, store, model.TaskPause{TaskID: "t1", PausedAt: baseTime, FromState: model.StateRunning,
		GraceUntil: &grace, RunID: liveRunID(t, store, "t1")})
	fw.releaseAll()
	run := waitFinished(t, ctx, store, runs, "t1")
	if run.Outcome != model.InterruptedOutcome {
		t.Fatalf("the run that stopped for its pause ended %q (%s), want %q", run.Outcome, run.Detail, model.InterruptedOutcome)
	}
	noStrike(t, store, "t1")
	stateIs(t, store, "t1", model.StateQueued)
}
