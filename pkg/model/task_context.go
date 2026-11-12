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

// A durable task's context: what a prompt carries of a task that has been
// open for months, and what it no longer needs to
// (design/design/durable-tasks.design.md#DT-15).
//
// Three things live here rather than on the task row, because all three
// change while the task is running and none of them is a setting:
//
//   - the compaction cursor: every comment up to and including
//     CompactedThrough is summarised by the handover written at
//     compaction. Prompts carry that handover and only the comments after
//     the cursor; the summarised ones stay in the record and the UI.
//   - the handover's version and when a run last confirmed it, which is
//     what a stale-handover warning is measured from.
//   - the current brief: a durable task's description as it stands now,
//     edited by its agent or by people, which replaces Task.Body in
//     prompts. Task.Body stays as the original and every edit is kept in
//     task_brief_edit with who made it and why.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// taskContextTable is one row per task that has ever been compacted,
// had its handover confirmed, had its brief edited or had compaction
// requested. A task with no row has none of those, which is every task
// before schema 136 and most tasks after it.
const taskContextTable = `CREATE TABLE IF NOT EXISTS ` + "`task_context`" + ` (
  ` + "`task_id`" + `               TEXT     NOT NULL PRIMARY KEY,
  ` + "`compacted_through`" + `     INTEGER  NOT NULL DEFAULT 0,
  ` + "`compacted_at`" + `          DATETIME NULL,
  ` + "`compacted_by`" + `          TEXT     NOT NULL DEFAULT '',
  ` + "`handover_version`" + `      INTEGER  NOT NULL DEFAULT 0,
  ` + "`handover_confirmed_at`" + ` DATETIME NULL,
  ` + "`compact_requested_at`" + `  DATETIME NULL,
  ` + "`compact_requested_by`" + `  TEXT     NOT NULL DEFAULT '',
  ` + "`current_brief`" + `         TEXT     NOT NULL DEFAULT '',
  ` + "`brief_updated_at`" + `      DATETIME NULL,
  ` + "`brief_updated_by`" + `      TEXT     NOT NULL DEFAULT ''
)`

// taskBriefEditTable is every edit of a task's current brief, oldest
// first: the brief it set, who set it and why.
const taskBriefEditTable = `CREATE TABLE IF NOT EXISTS ` + "`task_brief_edit`" + ` (
  ` + "`id`" + `      INTEGER PRIMARY KEY AUTOINCREMENT,
  ` + "`task_id`" + ` TEXT     NOT NULL,
  ` + "`at`" + `      DATETIME NOT NULL,
  ` + "`by`" + `      TEXT     NOT NULL DEFAULT '',
  ` + "`reason`" + `  TEXT     NOT NULL DEFAULT '',
  ` + "`brief`" + `   TEXT     NOT NULL DEFAULT ''
)`

const taskBriefEditTaskIndex = `CREATE INDEX IF NOT EXISTS ` + "`task_brief_edit_task`" + ` ON ` + "`task_brief_edit`" + ` (` + "`task_id`" + `, ` + "`id`" + `)`

// TaskContext is a task's task_context row, or the zero value (with
// TaskID set) for a task that has none.
type TaskContext struct {
	TaskID string `json:"task_id"`
	// CompactedThrough is the ID of the last comment the compaction
	// handover summarises; 0 is a task never compacted. Comments with
	// IDs at or below it are left out of prompts.
	CompactedThrough int64      `json:"compacted_through,omitempty"`
	CompactedAt      *time.Time `json:"compacted_at,omitempty"`
	CompactedBy      string     `json:"compacted_by,omitempty"`
	// HandoverVersion counts the handovers written on the task since
	// schema 136: each new handover body, by write_handover or by
	// compaction, is one more.
	HandoverVersion     int        `json:"handover_version,omitempty"`
	HandoverConfirmedAt *time.Time `json:"handover_confirmed_at,omitempty"`
	// CompactRequestedAt is set by a person (or by auto-compaction) asking
	// for the next run to compact before it does anything else, and
	// cleared by the compaction that answers it.
	CompactRequestedAt *time.Time `json:"compact_requested_at,omitempty"`
	CompactRequestedBy string     `json:"compact_requested_by,omitempty"`
	// CurrentBrief replaces Task.Body in prompts when it is not empty.
	CurrentBrief   string     `json:"current_brief,omitempty"`
	BriefUpdatedAt *time.Time `json:"brief_updated_at,omitempty"`
	BriefUpdatedBy string     `json:"brief_updated_by,omitempty"`
}

// TaskBriefEdit is one row of task_brief_edit.
type TaskBriefEdit struct {
	ID     int64     `json:"id"`
	TaskID string    `json:"task_id"`
	At     time.Time `json:"at"`
	By     string    `json:"by"`
	Reason string    `json:"reason,omitempty"`
	Brief  string    `json:"brief"`
}

// HandoverAge is how stale a task's handover is: the runs started and the
// comments posted since it was written or last confirmed.
type HandoverAge struct {
	// Exists is false for a task that has never had a handover.
	Exists   bool      `json:"exists"`
	At       time.Time `json:"at,omitempty"`
	Runs     int       `json:"runs"`
	Comments int       `json:"comments"`
}

// Handover staleness thresholds: past either, the task view and the
// prompt say the handover is stale (HandoverAge.Stale).
const (
	HandoverStaleRuns     = 10
	HandoverStaleComments = 30
)

// Stale reports whether the handover is past HandoverStaleRuns runs or
// HandoverStaleComments comments old. A task without one is not stale:
// it has nothing to go stale, and the required-handover turn asks for
// one where it matters.
func (a HandoverAge) Stale() bool {
	return a.Exists && (a.Runs >= HandoverStaleRuns || a.Comments >= HandoverStaleComments)
}

// ErrEmptyHandover is returned by CompactTask without a handover to
// compact to.
var ErrEmptyHandover = errors.New("a compaction needs a handover: the summary of what the comments it folds away settled")

const taskContextColumns = "`task_id`,`compacted_through`,`compacted_at`,`compacted_by`,`handover_version`," +
	"`handover_confirmed_at`,`compact_requested_at`,`compact_requested_by`,`current_brief`,`brief_updated_at`,`brief_updated_by`"

// TaskContext reads taskID's context row; a task without one reads as the
// zero TaskContext.
func (s *Store) TaskContext(ctx context.Context, taskID string) (TaskContext, error) {
	tc := TaskContext{TaskID: taskID}
	var compactedAt, confirmedAt, requestedAt, briefAt sql.NullTime
	err := s.db.QueryRowContext(ctx, "SELECT "+taskContextColumns+" FROM `task_context` WHERE `task_id` = ?", taskID).Scan(
		&tc.TaskID, &tc.CompactedThrough, &compactedAt, &tc.CompactedBy, &tc.HandoverVersion,
		&confirmedAt, &requestedAt, &tc.CompactRequestedBy, &tc.CurrentBrief, &briefAt, &tc.BriefUpdatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return TaskContext{TaskID: taskID}, nil
	}
	if err != nil {
		return TaskContext{TaskID: taskID}, fmt.Errorf("model: reading task_context for %s: %w", taskID, err)
	}
	tc.CompactedAt = nullTimePtr(compactedAt)
	tc.HandoverConfirmedAt = nullTimePtr(confirmedAt)
	tc.CompactRequestedAt = nullTimePtr(requestedAt)
	tc.BriefUpdatedAt = nullTimePtr(briefAt)
	return tc, nil
}

func nullTimePtr(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time.UTC()
	return &v
}

// ensureTaskContext inserts taskID's empty row if it has none.
func ensureTaskContext(ctx context.Context, tx *sql.Tx, taskID string) error {
	var n int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM `task_context` WHERE `task_id` = ?", taskID).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, "INSERT INTO `task_context` (`task_id`) VALUES (?)", taskID)
	return err
}

// BumpHandoverVersion counts one more handover written on taskID. Called
// wherever a handover comment with a new body is written.
func (s *Store) BumpHandoverVersion(ctx context.Context, taskID string) error {
	return s.write(ctx, "bump handover version on task "+taskID, func(tx *sql.Tx) error {
		if err := ensureTaskContext(ctx, tx, taskID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			"UPDATE `task_context` SET `handover_version` = `handover_version` + 1 WHERE `task_id` = ?", taskID)
		return err
	})
}

// ConfirmHandover records that a run read taskID's handover and found it
// still true, which resets its age (TaskHandoverAge) without a new
// version.
func (s *Store) ConfirmHandover(ctx context.Context, taskID string, at time.Time) error {
	return s.write(ctx, "confirm handover on task "+taskID, func(tx *sql.Tx) error {
		if err := ensureTaskContext(ctx, tx, taskID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			"UPDATE `task_context` SET `handover_confirmed_at` = ? WHERE `task_id` = ?", at.UTC(), taskID)
		return err
	})
}

// RequestCompaction asks for taskID's next run to compact its context
// before anything else. Asking again while a request stands keeps the
// first.
func (s *Store) RequestCompaction(ctx context.Context, taskID, by string, at time.Time) error {
	return s.write(ctx, "request compaction on task "+taskID, func(tx *sql.Tx) error {
		if err := ensureTaskContext(ctx, tx, taskID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			"UPDATE `task_context` SET `compact_requested_at` = ?, `compact_requested_by` = ? "+
				"WHERE `task_id` = ? AND `compact_requested_at` IS NULL", at.UTC(), by, taskID)
		return err
	})
}

// CompactTask folds taskID's conversation up to throughID into handover:
// it posts handover as the task's newest handover comment, moves the
// cursor to throughID (never backwards), bumps the handover version and
// clears any standing compaction request -- or, when handover is the
// task's newest handover already, confirms it instead of bumping the
// version. throughID <= 0 means every
// comment posted before this call.
//
// author is the comment's attribution: an agent's compaction is posted
// on its behalf the way write_handover's is, a person's under their own
// name. by is the short name recorded as who compacted.
func (s *Store) CompactTask(ctx context.Context, taskID, handover string, throughID int64,
	author Attribution, by string, at time.Time) (TaskContext, error) {
	handover = strings.TrimSpace(handover)
	if handover == "" {
		return TaskContext{}, ErrEmptyHandover
	}
	if throughID <= 0 {
		last, err := s.LatestComment(ctx, taskID)
		if err != nil {
			return TaskContext{}, err
		}
		if last != nil {
			throughID = last.ID
		}
	}
	// The same handover compacted again is a confirmation, as it is
	// written again with write_handover: the cursor still moves, but
	// there is no new version and no second copy of it on the task.
	latest, err := s.LatestHandover(ctx, taskID)
	if err != nil {
		return TaskContext{}, err
	}
	bump := 1
	if latest != nil && strings.TrimSpace(latest.Body) == handover {
		bump = 0
	} else if _, err := s.AddComment(ctx, Comment{
		TaskID:    taskID,
		Author:    author,
		Body:      handover,
		CreatedAt: at.UTC(),
		Handover:  true,
	}); err != nil {
		return TaskContext{}, fmt.Errorf("model: posting compaction handover for %s: %w", taskID, err)
	}
	if err := s.write(ctx, "compact task "+taskID, func(tx *sql.Tx) error {
		if err := ensureTaskContext(ctx, tx, taskID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			"UPDATE `task_context` SET "+
				"`compacted_through` = CASE WHEN `compacted_through` > ? THEN `compacted_through` ELSE ? END, "+
				"`compacted_at` = ?, `compacted_by` = ?, `handover_version` = `handover_version` + ?, "+
				"`handover_confirmed_at` = ?, `compact_requested_at` = NULL, `compact_requested_by` = '' "+
				"WHERE `task_id` = ?",
			throughID, throughID, at.UTC(), by, bump, at.UTC(), taskID)
		return err
	}); err != nil {
		return TaskContext{}, err
	}
	return s.TaskContext(ctx, taskID)
}

// SetTaskBrief replaces taskID's current brief with brief, recording the
// edit. An empty brief reverts prompts to the original description.
func (s *Store) SetTaskBrief(ctx context.Context, taskID, brief, by, reason string, at time.Time) error {
	brief = strings.TrimSpace(brief)
	return s.write(ctx, "edit brief on task "+taskID, func(tx *sql.Tx) error {
		if err := ensureTaskContext(ctx, tx, taskID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			"UPDATE `task_context` SET `current_brief` = ?, `brief_updated_at` = ?, `brief_updated_by` = ? WHERE `task_id` = ?",
			brief, at.UTC(), by, taskID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			"INSERT INTO `task_brief_edit` (`task_id`,`at`,`by`,`reason`,`brief`) VALUES (?,?,?,?,?)",
			taskID, at.UTC(), by, strings.TrimSpace(reason), brief)
		return err
	})
}

// TaskBriefEdits lists taskID's brief edits, oldest first.
func (s *Store) TaskBriefEdits(ctx context.Context, taskID string) ([]TaskBriefEdit, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT `id`,`task_id`,`at`,`by`,`reason`,`brief` FROM `task_brief_edit` WHERE `task_id` = ? ORDER BY `id`", taskID)
	if err != nil {
		return nil, fmt.Errorf("model: reading brief edits for %s: %w", taskID, err)
	}
	defer rows.Close()
	var out []TaskBriefEdit
	for rows.Next() {
		var e TaskBriefEdit
		if err := rows.Scan(&e.ID, &e.TaskID, &e.At, &e.By, &e.Reason, &e.Brief); err != nil {
			return nil, err
		}
		e.At = e.At.UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}

// TaskHandoverAge measures taskID's handover against what has happened
// since: the runs started after it was written or last confirmed,
// whichever is later, and the comments posted after it that are not
// handovers themselves.
//
// Only comments from people and agents count -- grain's own notices
// (automation on nobody's behalf, such as "this task's run parked it
// until ...") are left out, as DueMessageResumes leaves them out, since
// a durable task that parks collects two or three of them every run and
// would otherwise go stale by comments with nobody having said anything
// (docs/test-runs/2026-11-11-durable-tasks.md, follow-up B). Grain
// relaying somebody's words (automation on their behalf) still counts.
func (s *Store) TaskHandoverAge(ctx context.Context, taskID string) (HandoverAge, error) {
	latest, err := s.LatestHandover(ctx, taskID)
	if err != nil {
		return HandoverAge{}, err
	}
	if latest == nil {
		return HandoverAge{}, nil
	}
	tc, err := s.TaskContext(ctx, taskID)
	if err != nil {
		return HandoverAge{}, err
	}
	age := HandoverAge{Exists: true, At: latest.CreatedAt}
	if tc.HandoverConfirmedAt != nil && tc.HandoverConfirmedAt.After(age.At) {
		age.At = *tc.HandoverConfirmedAt
	}
	if err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM `task_comment` WHERE `task_id` = ? AND `id` > ? AND `handover` = 0 AND `created_at` > ? "+
			"AND NOT (`author_kind` = ? AND COALESCE(`author_behalf_kind`, '') = '')",
		taskID, latest.ID, age.At.UTC(), string(PrincipalAutomation)).Scan(&age.Comments); err != nil {
		return HandoverAge{}, fmt.Errorf("model: counting comments since the handover on %s: %w", taskID, err)
	}
	if err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM `task_run` WHERE `task_id` = ? AND `started_at` > ?",
		taskID, age.At.UTC()).Scan(&age.Runs); err != nil {
		return HandoverAge{}, fmt.Errorf("model: counting runs since the handover on %s: %w", taskID, err)
	}
	return age, nil
}

// RunStartedAt is when runID started; false for a run grain has no row
// for.
func (s *Store) RunStartedAt(ctx context.Context, runID string) (time.Time, bool) {
	var at time.Time
	if runID == "" || s.db.QueryRowContext(ctx,
		"SELECT `started_at` FROM `task_run` WHERE `id` = ?", runID).Scan(&at) != nil {
		return time.Time{}, false
	}
	return at.UTC(), true
}

// LastCommentBefore is the ID of the last of comments posted before at, or
// 0 if none was: the cursor a compaction written during a run moves to,
// so that nothing posted while the run was working is folded away unread.
func LastCommentBefore(comments []Comment, at time.Time) int64 {
	var id int64
	for _, c := range comments {
		if c.CreatedAt.Before(at) && c.ID > id {
			id = c.ID
		}
	}
	return id
}
