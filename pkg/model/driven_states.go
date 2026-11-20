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

package model

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"
)

// DrivenTaskState is one open task in a state grain itself drives -- the four
// states for which StateTimes.Since can return a non-nil timestamp: queued,
// running, completed, or deferred with a wake time.
type DrivenTaskState struct {
	TaskID string
	State  State
	Times  StateTimes
}

// DrivenTaskStates returns every open task currently in one of the four states
// grain drives (StateRunning, StateCompleted, StateDeferred with a wake time,
// and StateQueued) together with its StateTimes.
//
// Stall and liveness reconcilers need only those tasks: closed, failed,
// proposed, awaiting_reply, awaiting_submit, and undated deferred tasks always
// answer nil from StateTimes.Since. Querying the four arms directly off the
// state/observation/run indexes avoids scanning full task bodies, joining
// task_state over all history, or aggregating every historical run across
// closed and failed tasks.
func (s *Store) DrivenTaskStates(ctx context.Context) (map[string]DrivenTaskState, error) {
	out := map[string]DrivenTaskState{}

	// 1. Live runs: read once up front from task_run_open_task (bounded by
	// active concurrency). Keeps the deferred and queued arms from running a
	// correlated NOT EXISTS against task_run for every historical row.
	liveRuns := map[string]time.Time{}
	if err := each(ctx, s.db,
		"SELECT `r`.`task_id`, `r`.`started_at`, `o`.`closed_at`, `o`.`completed_at`, `o`.`pending_question_comment_id` "+
			"FROM `task_run` AS `r` "+
			"JOIN `task` AS `t` ON `t`.`id` = `r`.`task_id` "+
			"LEFT JOIN `task_observation` AS `o` ON `o`.`task_id` = `r`.`task_id` "+
			"WHERE `r`.`finished_at` IS NULL",
		nil,
		func(rows *sql.Rows) error {
			var id string
			var started time.Time
			var closed, completed sql.NullTime
			var pendingQ sql.NullInt64
			if err := rows.Scan(&id, &started, &closed, &completed, &pendingQ); err != nil {
				return err
			}
			liveRuns[id] = started
			if !closed.Valid && !completed.Valid && !pendingQ.Valid {
				st := started
				out[id] = DrivenTaskState{
					TaskID: id,
					State:  StateRunning,
					Times:  StateTimes{LiveRunStartedAt: &st},
				}
			}
			return nil
		}); err != nil {
		return nil, fmt.Errorf("reading running driven task states: %w", err)
	}

	// 2. Completed: open completed task that is neither awaiting_submit nor
	// awaiting_reply. Driven off task_observation_open_completed.
	if err := each(ctx, s.db,
		"SELECT `o`.`task_id`, `o`.`completed_at` FROM `task_observation` AS `o` "+
			"JOIN `task` AS `t` ON `t`.`id` = `o`.`task_id` "+
			"WHERE `o`.`closed_at` IS NULL "+
			"AND `o`.`completed_at` IS NOT NULL "+
			"AND NOT (`t`.`auto_merge` = FALSE AND EXISTS ("+
			"SELECT 1 FROM `task_link` AS `fl` WHERE `fl`.`task_id` = `o`.`task_id` AND `fl`.`kind` IN ("+changeLinkKindsSQL+"))) "+
			"AND NOT (`t`.`origin_reason` != ? AND NOT EXISTS ("+
			"SELECT 1 FROM `task_link` AS `al` WHERE `al`.`task_id` = `o`.`task_id` AND `al`.`kind` IN ("+changeLinkKindsSQL+")))",
		[]any{string(ReasonReview)},
		func(rows *sql.Rows) error {
			var id string
			var completed time.Time
			if err := rows.Scan(&id, &completed); err != nil {
				return err
			}
			at := completed
			out[id] = DrivenTaskState{
				TaskID: id,
				State:  StateCompleted,
				Times:  StateTimes{CompletedAt: &at},
			}
			return nil
		}); err != nil {
		return nil, fmt.Errorf("reading completed driven task states: %w", err)
	}

	// 3. Deferred with a wake time: driven off task_deferred_wake.
	if err := each(ctx, s.db,
		"SELECT `t`.`id`, `t`.`deferred_until` FROM `task` AS `t` "+
			"LEFT JOIN `task_observation` AS `o` ON `o`.`task_id` = `t`.`id` "+
			"WHERE `t`.`deferred_at` IS NOT NULL "+
			"AND `t`.`deferred_until` IS NOT NULL "+
			"AND `o`.`closed_at` IS NULL "+
			"AND `o`.`completed_at` IS NULL "+
			"AND `o`.`pending_question_comment_id` IS NULL "+
			"AND COALESCE(`o`.`streak_count`, 0) < "+strconv.Itoa(MaxConsecutiveFailures)+" "+
			"ORDER BY `t`.`id`",
		nil,
		func(rows *sql.Rows) error {
			var id string
			var until time.Time
			if err := rows.Scan(&id, &until); err != nil {
				return err
			}
			if _, running := liveRuns[id]; running {
				return nil
			}
			at := until
			out[id] = DrivenTaskState{
				TaskID: id,
				State:  StateDeferred,
				Times:  StateTimes{DeferredUntil: &at},
			}
			return nil
		}); err != nil {
		return nil, fmt.Errorf("reading deferred-wake driven task states: %w", err)
	}

	// 4. Queued: approved, not deferred, not closed/completed/awaiting_reply/
	// failed/running. ORDER BY t.id lets SQLite scan the narrow covering
	// partial index task_approved_active instead of the wide task table heap;
	// task_observation is read via covering index task_observation_state_cols,
	// and the latest finished run is sought only for the surviving queued rows.
	if err := each(ctx, s.db,
		"SELECT `t`.`id`, `t`.`approved_at`, `t`.`undeferred_at`, "+
			"`o`.`retry_requested_at`, `o`.`run_now_at`, "+
			"(SELECT `r`.`finished_at` FROM `task_run` AS `r` "+
			"WHERE `r`.`task_id` = `t`.`id` AND `r`.`finished_at` IS NOT NULL "+
			"ORDER BY `r`.`finished_at` DESC LIMIT 1) "+
			"FROM `task` AS `t` "+
			"LEFT JOIN `task_observation` AS `o` ON `o`.`task_id` = `t`.`id` "+
			"WHERE `t`.`approval_actor_kind` IS NOT NULL "+
			"AND `t`.`deferred_at` IS NULL "+
			"AND `o`.`closed_at` IS NULL "+
			"AND `o`.`completed_at` IS NULL "+
			"AND `o`.`pending_question_comment_id` IS NULL "+
			"AND COALESCE(`o`.`streak_count`, 0) < "+strconv.Itoa(MaxConsecutiveFailures)+" "+
			"ORDER BY `t`.`id`",
		nil,
		func(rows *sql.Rows) error {
			var id string
			var approved, undeferred, retried, runNow, finished sql.NullTime
			if err := rows.Scan(&id, &approved, &undeferred, &retried, &runNow, &finished); err != nil {
				return err
			}
			if _, running := liveRuns[id]; running {
				return nil
			}
			out[id] = DrivenTaskState{
				TaskID: id,
				State:  StateQueued,
				Times: StateTimes{
					ApprovedAt:        timePtr(approved),
					RequeuedAt:        latest(timePtr(retried), timePtr(runNow), timePtr(undeferred)),
					LastRunFinishedAt: timePtr(finished),
				},
			}
			return nil
		}); err != nil {
		return nil, fmt.Errorf("reading queued driven task states: %w", err)
	}

	return out, nil
}
