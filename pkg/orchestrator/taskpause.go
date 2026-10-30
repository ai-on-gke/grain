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

package orchestrator

import (
	"context"
	"log"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
)

// taskPaused reports whether an operator has paused taskID
// (model.TaskPause). A store that cannot answer is read as "not paused":
// every path that has to keep a paused task from running also asks the
// store itself (task_ready, RunNow, StartRun), so this is only ever the
// softer half of the answer.
func taskPaused(ctx context.Context, store *model.Store, taskID string) bool {
	if store == nil {
		return false
	}
	p, err := store.GetPause(ctx, taskID)
	if err != nil {
		log.Printf("orchestrator: reading whether task %s is paused: %v", taskID, err)
		return false
	}
	return p != nil
}

// pausedCIReason is the cancel reason grain CI records for a run stopped
// because its task was paused.
const pausedCIReason = "the task was paused by the operator"

// reconcileTaskPauses carries out what an operator's pause asks of work
// already in flight (ui.Client.Pause records the pause itself, and delivers
// the graceful pause's message into the run):
//
//   - a live run is cancelled with ErrOperatorPaused -- at once for an
//     immediate pause, or once its grace period (TaskPause.GraceUntil) has
//     passed. A run that stops by itself inside the grace period ends the
//     wait early simply by no longer being live. Cancelling the run is what
//     releases its sandbox, its worker slot and its connection; the run is
//     recorded as interrupted (classifyRunEnd), so the task's failure streak
//     is untouched and its CLI session is kept for the resume.
//   - the task's in-flight grain CI run is cancelled, and reconcileTaskCI
//     starts no other while the task is paused.
//
// Every cycle, for every pause: a pause on a task with nothing in flight
// costs a map lookup and an observation read, and a cycle that missed the
// moment a grace period ran out (a daemon restart) catches up on the next.
func reconcileTaskPauses(ctx context.Context, deps Deps, now time.Time) error {
	if deps.Store == nil {
		return nil
	}
	pauses, err := deps.Store.Pauses(ctx)
	if err != nil {
		return err
	}
	for _, p := range pauses {
		if pauseGraceOver(p, now) {
			releaseWarmSessionSandbox(ctx, deps, p.TaskID, true)
		}
		if deps.Config.TaskCancellers.Live(p.TaskID) && pauseGraceOver(p, now) {
			if deps.Config.TaskCancellers.Cancel(p.TaskID, ErrOperatorPaused) {
				log.Printf("orchestrator: task %s is paused; cancelled its live run", p.TaskID)
			}
		}
		if deps.CI == nil {
			continue
		}
		obs, err := deps.Store.GetObservation(ctx, p.TaskID)
		if err != nil || obs == nil || obs.CI == nil || obs.CI.RunID == "" || obs.CI.Conclusion != "" {
			continue
		}
		if run, ok := deps.CI.Get(obs.CI.RunID); ok && run.Conclusion == "" {
			if deps.CI.Cancel(obs.CI.RunID, pausedCIReason, ci.CancelBySystem) {
				log.Printf("orchestrator: task %s is paused; cancelled its grain CI run %s", p.TaskID, obs.CI.RunID)
			}
		}
	}
	return nil
}

// pauseGraceOver reports whether p's live run should be cancelled now.
func pauseGraceOver(p model.TaskPause, now time.Time) bool {
	return p.Immediate || p.GraceUntil == nil || !now.Before(*p.GraceUntil)
}

// noStrikeOutcome reports whether outcome is already one of the endings a
// task's failure streak passes over (model.Store's streak reads), which a
// pause leaves as it is rather than rewriting as interrupted.
func noStrikeOutcome(outcome string) bool {
	switch outcome {
	case model.InterruptedOutcome, model.PausedOutcome, model.CapacityOutcome,
		model.EnvironmentOutcome, model.OOMOutcome:
		return true
	}
	return false
}
