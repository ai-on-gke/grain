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
	"errors"
	"fmt"
	"log"
	"slices"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
)

// Base-branch repair tasks: what counts as one, and what a green branch
// does to them.
//
// Work used to wait on a red base branch as a task dependency -- every
// ready task targeting the branch was linked to depend on its open repair
// (linkBrokenBaseDependents, tagged base-ci-wait:<id>), and a pull request
// whose red was all the base's waited on that repair instead of being
// repaired. Those automatic waits are gone: system:pipeline-steward
// coordinates a red base branch -- filing or reverting, ordering the
// queue -- and the daemon neither holds dispatch nor parks queue entries
// behind a repair.
//
// What is left is the repair task's own lifecycle. A branch whose own
// grain CI is green again closes the repair grain filed of it as
// superseded, whoever made it green (closeBaseRepairsOnGreen). A person
// can mark a task of their own as fixing the branch (model.FixesBaseBranch,
// grain/task-3451): it carries the same branch repair tag grain's own
// repairs do, counts as the branch's open repair once it is approved and
// not deferred (countsAsBaseRepair), and is not closed by a green branch
// -- a de-flake task can have work left after one green run -- but told
// so instead, once per return to green.

// openBaseRepair is the open repair task work waiting on repo's branch
// should depend on, or nil if there is none: the one running if any
// (single flight, below), else the one the branch's row names, else the
// newest carrying the branch's repair tag -- of those that count as a
// repair at all (countsAsBaseRepair).
func openBaseRepair(ctx context.Context, store *model.Store, repo model.RepoRef, branch string,
	row *model.BaseBranchCI) (*model.Task, error) {

	tag := BaseCIRepairBranchTag(repo, branch)
	running, err := store.RunningTaskWithTag(ctx, tag)
	if err != nil || running != nil {
		return running, err
	}
	if row != nil && row.RepairTaskID != "" {
		st, err := store.State(ctx, row.RepairTaskID)
		if err != nil {
			return nil, err
		}
		if st != "" && st != model.StateClosed {
			task, err := store.GetTask(ctx, row.RepairTaskID)
			if err != nil || (task != nil && countsAsBaseRepair(*task)) {
				return task, err
			}
		}
	}
	ids, err := store.OpenTasksWithTag(ctx, tag)
	if err != nil {
		return nil, err
	}
	for i := len(ids) - 1; i >= 0; i-- {
		task, err := store.GetTask(ctx, ids[i])
		if err != nil {
			return nil, err
		}
		if task != nil && countsAsBaseRepair(*task) {
			return task, nil
		}
	}
	return nil, nil
}

// countsAsBaseRepair reports whether a task carrying a branch's repair
// tag is somebody working on the branch: always for grain's own, and for
// a task a person marked once it is approved and not deferred.
func countsAsBaseRepair(task model.Task) bool {
	return model.GrainBaseRepair(task) || (task.Approval != nil && task.DeferredAt == nil)
}

// isBaseRepair reports whether task is a repair of repo's branch itself.
func isBaseRepair(task model.Task, repo model.RepoRef, branch string, row *model.BaseBranchCI) bool {
	return hasTag(task, BaseCIRepairBranchTag(repo, branch)) || (row != nil && row.RepairTaskID == task.ID)
}

// closeBaseRepairsOnGreen is what a green verdict on a base branch does
// to the repairs of it: every repair -- grain's own and every one marked
// fixes_base_branch -- is a base-green goal with on_met close
// (docs/design/durable-tasks.md#DT-4), and this is that goal's check run
// against the verdict as it lands (checkBaseGreenGoals). A repair not
// running is closed with outcome goal-met and a note naming the green
// run and commit, which releases everything waiting on it; a running one
// is told, and stopped after the pause grace. A verdict a confirmation
// cleared counts as green too.
func closeBaseRepairsOnGreen(ctx context.Context, deps Deps, row model.BaseBranchCI, now time.Time) error {
	if baseCIInFlight(row) || row.Broken() {
		return nil
	}
	if row.Conclusion != string(ci.ConclusionSuccess) && row.Confirmation != model.BaseCICleared {
		return nil
	}
	// The green verdict is what clears pipeline.base-red (DT-5).
	if err := clearBaseRed(ctx, deps, row); err != nil {
		log.Printf("orchestrator: clearing %s %s's base-red notification: %v", row.Repo, row.Branch, err)
	}
	ids, err := deps.Store.OpenTasksWithTag(ctx, BaseCIRepairBranchTag(row.Repo, row.Branch))
	if err != nil {
		return err
	}
	var errs []error
	for _, id := range ids {
		task, err := deps.Store.GetTask(ctx, id)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if task == nil {
			continue
		}
		// Every repair is a base-green goal (DT-4); one filed before goals,
		// or by a path that did not declare it, is given it here.
		if g, err := deps.Store.TaskGoal(ctx, id); err != nil {
			errs = append(errs, err)
			continue
		} else if g == nil {
			if err := declareRepairGoal(ctx, deps.Store, *task, now); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		// Left open by a live run's grace, its repair-failing
		// notification's own condition (the task closing) would not clear
		// it: the branch it was failing to fix is fixed.
		errs = append(errs, clearPipelineNotification(ctx, deps.Store,
			model.NotificationSubject(model.FamilyRepairFailing, task.ID), baseCINotifier))
	}
	errs = append(errs, checkBaseGreenGoals(ctx, deps, row, now))
	return errors.Join(errs...)
}

// greenNotedTag is carried by a task a person marked as fixing its base
// once it has been told the branch is green, and dropped when the branch
// is red again (forgetGreenNoted): one comment per return to green, not
// one per green run.
const greenNotedTag = "base-ci-green-noted"

// forgetGreenNoted drops greenNotedTag from taskID, if it carries it.
func forgetGreenNoted(ctx context.Context, store *model.Store, taskID string) error {
	task, err := store.GetTask(ctx, taskID)
	if err != nil || task == nil || !hasTag(*task, greenNotedTag) {
		return err
	}
	return store.UpdateTask(ctx, taskID, func(t *model.Task) error {
		t.Tags = slices.DeleteFunc(t.Tags, func(x string) bool { return x == greenNotedTag })
		return nil
	})
}

// closeSupersededTasks closes each of ids that is not running, with note,
// recording the close as by's for reason.
func closeSupersededTasks(ctx context.Context, store *model.Store, ids []string, by model.Principal,
	reason, note string, now time.Time) error {

	var errs []error
	for _, id := range ids {
		st, err := store.State(ctx, id)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if st == "" || st == model.StateClosed || st == model.StateRunning {
			continue
		}
		if err := closeTaskBy(ctx, store, id, now, by, reason, func(o *model.Observation) {
			o.ClosedAt = &now
		}); err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: closing %s: %w", id, err))
			continue
		}
		log.Printf("orchestrator: closing %s as superseded: %s", id, note)
		if _, err := store.AddComment(ctx, model.Comment{
			TaskID: id, Author: model.Attribution{Actor: by}, Body: note, CreatedAt: now,
		}); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
