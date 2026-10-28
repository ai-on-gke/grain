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
	"errors"
	"fmt"
	"time"
)

// GetScheduledWorkflowRun reads the firing one workflow-review schedule
// has in hand, or nil (with no error) for a schedule that has never
// fired -- or whose last firing predates this table, which reads the
// same way: there is nothing in flight.
func (s *Store) GetScheduledWorkflowRun(ctx context.Context, scheduleID string) (*ScheduledWorkflowRun, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT `stem`,`owner`,`name`,`branch`,`workflow`,`run_id`,`fired_at`,`attempted_at`,"+
			"`finished_at`,`attempts`,`conclusion`,`detail`,`unfinished`,`review_task_id`,`review_not_needed` "+
			"FROM `scheduled_workflow_run` WHERE `schedule_id` = ?", scheduleID)
	r := ScheduledWorkflowRun{ScheduleID: scheduleID}
	var firedAt time.Time
	var attemptedAt, finishedAt sql.NullTime
	if err := row.Scan(&r.Stem, &r.Repo.Owner, &r.Repo.Name, &r.Branch, &r.Workflow, &r.RunID,
		&firedAt, &attemptedAt, &finishedAt, &r.Attempts, &r.Conclusion, &r.Detail,
		&r.Unfinished, &r.ReviewTaskID, &r.ReviewNotNeeded); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading the scheduled workflow run for %s: %w", scheduleID, err)
	}
	r.FiredAt = firedAt.UTC()
	r.AttemptedAt = nullableTime(attemptedAt)
	r.FinishedAt = nullableTime(finishedAt)
	return &r, nil
}

// ListScheduledWorkflowRuns returns every firing grain is carrying,
// ordered so a listing is stable. It is what the reconciler walks: one
// row per workflow-review schedule that has ever fired, which is a
// handful on any deployment.
func (s *Store) ListScheduledWorkflowRuns(ctx context.Context) ([]ScheduledWorkflowRun, error) {
	var out []ScheduledWorkflowRun
	err := each(ctx, s.db,
		"SELECT `schedule_id`,`stem`,`owner`,`name`,`branch`,`workflow`,`run_id`,`fired_at`,"+
			"`attempted_at`,`finished_at`,`attempts`,`conclusion`,`detail`,`unfinished`,"+
			"`review_task_id`,`review_not_needed` FROM `scheduled_workflow_run` ORDER BY `schedule_id`", nil,
		func(rows *sql.Rows) error {
			var r ScheduledWorkflowRun
			var firedAt time.Time
			var attemptedAt, finishedAt sql.NullTime
			if err := rows.Scan(&r.ScheduleID, &r.Stem, &r.Repo.Owner, &r.Repo.Name, &r.Branch,
				&r.Workflow, &r.RunID, &firedAt, &attemptedAt, &finishedAt, &r.Attempts,
				&r.Conclusion, &r.Detail, &r.Unfinished, &r.ReviewTaskID, &r.ReviewNotNeeded); err != nil {
				return err
			}
			r.FiredAt = firedAt.UTC()
			r.AttemptedAt = nullableTime(attemptedAt)
			r.FinishedAt = nullableTime(finishedAt)
			out = append(out, r)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("listing scheduled workflow runs: %w", err)
	}
	return out, nil
}

// PutScheduledWorkflowRun replaces what is known about one schedule's
// firing wholesale -- PutBaseBranchCI's shape, and for its reason: a
// fresh firing supersedes the last one's answer, because what this row
// is ever asked is "what is this schedule doing *now*".
func (s *Store) PutScheduledWorkflowRun(ctx context.Context, r ScheduledWorkflowRun) error {
	return s.write(ctx, "record the scheduled workflow run for "+r.ScheduleID, func(tx *sql.Tx) error {
		var attemptedAt, finishedAt any
		if r.AttemptedAt != nil {
			attemptedAt = r.AttemptedAt.UTC()
		}
		if r.FinishedAt != nil {
			finishedAt = r.FinishedAt.UTC()
		}
		_, err := tx.ExecContext(ctx,
			"REPLACE INTO `scheduled_workflow_run` (`schedule_id`,`stem`,`owner`,`name`,`branch`,"+
				"`workflow`,`run_id`,`fired_at`,`attempted_at`,`finished_at`,`attempts`,"+
				"`conclusion`,`detail`,`unfinished`,`review_task_id`,`review_not_needed`) "+
				"VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
			r.ScheduleID, r.Stem, r.Repo.Owner, r.Repo.Name, r.Branch, r.Workflow, r.RunID,
			r.FiredAt.UTC(), attemptedAt, finishedAt, r.Attempts, r.Conclusion, r.Detail,
			r.Unfinished, r.ReviewTaskID, r.ReviewNotNeeded)
		return err
	})
}

// DeleteScheduledWorkflowRun forgets one schedule's firing. The
// reconciler uses it for a row whose schedule has gone entirely and
// whose review has already been filed: the row's whole purpose is to
// carry a firing from its run to its review, so once the schedule that
// would replace it no longer exists there is nothing left for it to say.
func (s *Store) DeleteScheduledWorkflowRun(ctx context.Context, scheduleID string) error {
	return s.write(ctx, "forget the scheduled workflow run for "+scheduleID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"DELETE FROM `scheduled_workflow_run` WHERE `schedule_id` = ?", scheduleID)
		return err
	})
}

// nullableTime is a NULL DATETIME as this package carries one: nil for
// absent, UTC for present.
func nullableTime(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	at := t.Time.UTC()
	return &at
}
