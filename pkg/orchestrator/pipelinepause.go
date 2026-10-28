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
	"fmt"
	"log"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// pipelinePauseDeadline bounds how long a pause that is waiting on one
// task (model.PipelinePause.UntilTaskID) holds the queue, however that
// task is getting on.
//
// A pause taken to check an upgrade is lifted by the check finishing,
// and the ordinary case is a run of minutes. The deadline is for the
// cases where "finishing" never happens: the check parks on a question
// nobody answers overnight, its runs fail until the task is out of
// attempts, an agent framework has no credential and every dispatch of
// it fails immediately. None of those is a reason for a deployment to do
// no work at all indefinitely -- the check is worth waiting for, and it
// is not worth waiting for forever.
//
// Six hours: long enough that a person who was going to look at the
// question when they woke up still finds the deployment where they left
// it, short enough that a deployment cannot sit out a working day on a
// check nobody is going to finish.
//
// An operator's own pause has no deadline at all and is not touched by
// any of this. It names no task, so nothing here can decide it is over;
// only a person lifts it.
const pipelinePauseDeadline = 6 * time.Hour

// pipelinePause reads the deployment's pause, or nil for a deployment
// that is running -- and nil, with no error, for a caller with no store
// at all (a test building Deps by hand), which has no pause to be under.
func pipelinePause(ctx context.Context, deps Deps) (*model.PipelinePause, error) {
	if deps.Store == nil {
		return nil, nil
	}
	pause, err := deps.Store.GetPipelinePause(ctx)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: %w", err)
	}
	return pause, nil
}

// reconcilePipelinePause lifts a pause that is waiting on a task once
// that task is no longer worth waiting for -- it finished, it was closed,
// it no longer exists, or it has held the queue for longer than
// pipelinePauseDeadline.
//
// It is the other half of the upgrade check's own pause
// (reconcileUpgradeCheck): the check holds the queue while it looks at
// the deployment, and this is what lets go. Level-triggered, like every
// reconciler here, which is what makes it survive the thing it is most
// likely to have to survive -- a daemon restarted while a pause is in
// force finds the row, finds the task, and reaches the same conclusion
// the process that took the pause would have.
//
// It runs before "dispatch" in the cycle so that a pause lifted this tick
// is a queue that moves this tick rather than on the next one.
func reconcilePipelinePause(ctx context.Context, deps Deps, now time.Time) error {
	if deps.Store == nil {
		return nil
	}
	pause, err := pipelinePause(ctx, deps)
	if err != nil {
		return err
	}
	// Not paused, or paused by a person: neither is this reconciler's to
	// decide anything about. An operator's pause names no task, and
	// lifting it is an operator's own act (`grain pause -resume`, the
	// banner's Resume button).
	if pause == nil || pause.UntilTaskID == "" {
		return nil
	}
	reason, done, err := pauseFinished(ctx, deps, *pause, now)
	if err != nil {
		return fmt.Errorf("orchestrator: %w", err)
	}
	if !done {
		return nil
	}
	// Conditional on the pause still being the one read above: an
	// operator who replaced it in the meantime keeps theirs.
	cleared, err := deps.Store.ClearPipelinePauseUntil(ctx, pause.UntilTaskID)
	if err != nil {
		return fmt.Errorf("orchestrator: resuming the pipeline: %w", err)
	}
	if cleared {
		log.Printf("orchestrator: %s -- the pipeline is picking work up again", reason)
	}
	return nil
}

// pauseFinished reports whether pause has done what it was taken for,
// and the sentence that says so for the log.
//
// The deadline is checked first, so that a pause held past it is lifted
// whatever the store says about the task -- including the case where
// reading the task is itself what is failing.
func pauseFinished(ctx context.Context, deps Deps, pause model.PipelinePause, now time.Time) (string, bool, error) {
	if !pause.PausedAt.IsZero() && now.Sub(pause.PausedAt.UTC()) >= pipelinePauseDeadline {
		return fmt.Sprintf("task %s has not finished within %s of the pipeline being paused for it",
			pause.UntilTaskID, pipelinePauseDeadline), true, nil
	}
	task, err := deps.Store.GetTask(ctx, pause.UntilTaskID)
	if err != nil {
		return "", false, fmt.Errorf("reading task %s, which the pipeline is paused for: %w", pause.UntilTaskID, err)
	}
	if task == nil {
		return fmt.Sprintf("task %s, which the pipeline was paused for, no longer exists", pause.UntilTaskID), true, nil
	}
	obs, err := deps.Store.GetObservation(ctx, pause.UntilTaskID)
	if err != nil {
		return "", false, fmt.Errorf("reading task %s's observation: %w", pause.UntilTaskID, err)
	}
	if obs == nil {
		return "", false, nil
	}
	switch {
	case obs.ClosedAt != nil:
		return fmt.Sprintf("task %s, which the pipeline was paused for, was closed", pause.UntilTaskID), true, nil
	case obs.CompletedAt != nil:
		return fmt.Sprintf("task %s has finished", pause.UntilTaskID), true, nil
	}
	return "", false, nil
}
