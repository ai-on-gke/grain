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

// Rolling a durable task over to a successor
// (design/design/durable-tasks.design.md#DT-15, "Rollover to a successor
// task").
//
// Compaction bounds what a durable task's prompt carries; it does not
// bound its record. Every run, comment and transcript stays on the task
// for good, so a task open for years is a task whose page, export and
// reconcilers read more every month. Rollover is the storage backstop:
// it files a successor that carries the work on with a clean record, and
// closes the predecessor, which keeps everything it had. Nothing is
// copied or moved but references -- the successor takes over the
// predecessor's place in every other task's waits, its notifications,
// its schedule and its pull request.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The record limits past which a durable task is rolled over by itself
// (orchestrator.reconcileRollover). A durable task should compact many
// times for every rollover, so these are far above anything compaction
// is asked for at.
const (
	RolloverRunLimit     = 500
	RolloverCommentLimit = 2000
	RolloverAgeLimit     = 365 * 24 * time.Hour
)

// RolloverOutcome is the WaitTarget.Outcome of a task closed by
// Store.Rollover.
const RolloverOutcome = "rolled_over"

// RolledOverTo is the successor a task was rolled over to, read off its
// LinkRolledOverTo, or "" for a task that was not.
func RolledOverTo(t Task) string {
	for _, l := range t.Links {
		if l.Kind == LinkRolledOverTo {
			return l.Target
		}
	}
	return ""
}

// RolloverRecord is how big a task's record is, against the limits.
type RolloverRecord struct {
	Runs      int        `json:"runs"`
	Comments  int        `json:"comments"`
	CreatedAt *time.Time `json:"created_at,omitempty"`
}

// PastLimit names the first record limit r is past as of now, in a phrase
// that finishes "rolled over because it has ...", or "" when it is past
// none.
func (r RolloverRecord) PastLimit(now time.Time) string {
	switch {
	case r.Runs >= RolloverRunLimit:
		return fmt.Sprintf("%d runs (the limit is %d)", r.Runs, RolloverRunLimit)
	case r.Comments >= RolloverCommentLimit:
		return fmt.Sprintf("%d comments (the limit is %d)", r.Comments, RolloverCommentLimit)
	case r.CreatedAt != nil && now.Sub(*r.CreatedAt) >= RolloverAgeLimit:
		return fmt.Sprintf("been open since %s (the limit is a year)", r.CreatedAt.UTC().Format("2006-01-02"))
	}
	return ""
}

// TaskRolloverRecord counts taskID's runs and comments.
func (s *Store) TaskRolloverRecord(ctx context.Context, taskID string) (RolloverRecord, error) {
	var r RolloverRecord
	var created sql.NullTime
	err := s.db.QueryRowContext(ctx,
		"SELECT (SELECT COUNT(*) FROM `task_run` WHERE `task_id` = `t`.`id`), "+
			"(SELECT COUNT(*) FROM `task_comment` WHERE `task_id` = `t`.`id`), `t`.`created_at` "+
			"FROM `task` AS `t` WHERE `t`.`id` = ?", taskID).Scan(&r.Runs, &r.Comments, &created)
	if err != nil {
		return RolloverRecord{}, fmt.Errorf("model: measuring task %s's record: %w", taskID, err)
	}
	r.CreatedAt = timePtr(created)
	return r, nil
}

// OpenDurableTasksPastRolloverLimit lists every open durable task whose
// record is past a rollover limit as of now, by id, with the phrase
// RolloverRecord.PastLimit gives for it.
func (s *Store) OpenDurableTasksPastRolloverLimit(ctx context.Context, now time.Time) (map[string]string, error) {
	out := map[string]string{}
	err := each(ctx, s.db,
		"SELECT `t`.`id`, (SELECT COUNT(*) FROM `task_run` WHERE `task_id` = `t`.`id`), "+
			"(SELECT COUNT(*) FROM `task_comment` WHERE `task_id` = `t`.`id`), `t`.`created_at` "+
			"FROM `task` AS `t` LEFT JOIN `task_observation` AS `o` ON `o`.`task_id` = `t`.`id` "+
			"WHERE `o`.`closed_at` IS NULL AND "+durableTaskSQL+" ORDER BY `t`.`id`", nil,
		func(rows *sql.Rows) error {
			var id string
			var r RolloverRecord
			var created sql.NullTime
			if err := rows.Scan(&id, &r.Runs, &r.Comments, &created); err != nil {
				return err
			}
			r.CreatedAt = timePtr(created)
			if why := r.PastLimit(now); why != "" {
				out[id] = why
			}
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("model: listing durable tasks past a rollover limit: %w", err)
	}
	return out, nil
}

// RolloverResult is what Store.Rollover did.
type RolloverResult struct {
	// Successor is the task filed to carry the work on.
	Successor string `json:"successor"`
	// Moved and Kept are the predecessor's open dependents, as for a
	// supersede (SupersedeResult).
	Moved []string        `json:"moved,omitempty"`
	Kept  []KeptDependent `json:"kept,omitempty"`
	// Suspensions is the parks of other tasks (task_suspension.resume_on)
	// that now wait on the successor.
	Suspensions int `json:"suspensions,omitempty"`
	// Notifications is the open notifications now about the successor.
	Notifications int `json:"notifications,omitempty"`
}

// ErrRolloverRefused wraps every reason Store.Rollover will not roll a
// task over; the message says which.
var ErrRolloverRefused = errors.New("cannot roll over")

// RolloverRefusal says why t, in state, may not be rolled over, or ""
// if it may.
//
//   - A closed task has nothing to carry on.
//   - A running task is mid-run: the successor would be dispatched
//     beside it, and whatever the run says next would land on a closed
//     task. It rolls over when it rests.
//   - A console session already rolls over by its own mechanism: a
//     closed session is filed again the next cycle and carries its
//     handover (orchestrator.reconcileConsoleSession), and its binding is
//     its session tag, which two tasks cannot share.
func RolloverRefusal(t Task, state State) string {
	switch {
	case state == StateClosed:
		if to := RolledOverTo(t); to != "" {
			return "it was already rolled over, to task " + to
		}
		return "it is closed"
	case state == StateRunning:
		return "it has a run in progress; roll it over once the run has ended"
	}
	if _, ok := ConsoleSessionIDOf(t); ok {
		return "it is a console session, which rolls over by being closed: grain files it " +
			"again the next cycle, carrying its handover"
	}
	return ""
}

// rolloverKeepsOff is the predecessor's links the successor does not
// inherit: the predecessor's own lineage, which is about it and not about
// the work.
var rolloverKeepsOff = map[LinkKind]bool{
	LinkContinues:      true,
	LinkSupersedes:     true,
	LinkSupersededBy:   true,
	LinkRolledOverFrom: true,
	LinkRolledOverTo:   true,
	LinkRevertedBy:     true,
	LinkPortedBy:       true,
}

// RolloverBody is a successor's description: the predecessor's current
// brief (its description, when it never had one), then its last handover,
// then where it was rolled over from.
func RolloverBody(predecessorID, brief, handover string) string {
	var b strings.Builder
	if brief = strings.TrimSpace(brief); brief != "" {
		b.WriteString(brief)
		b.WriteString("\n\n")
	}
	if handover = strings.TrimSpace(handover); handover != "" {
		b.WriteString("## Handover\n\n")
		b.WriteString(handover)
		b.WriteString("\n\n")
	}
	fmt.Fprintf(&b, "Rolled over from [task %s](/tasks/%s).", predecessorID, predecessorID)
	return b.String()
}

// RolloverComment is what the predecessor's own conversation records.
func RolloverComment(successorID, why string) string {
	s := fmt.Sprintf("Rolled over to [task %s](/tasks/%s), which carries this work on with a fresh "+
		"record. Everything here stays: this task's comments, runs and deep links still resolve.",
		successorID, successorID)
	if why != "" {
		s += " " + why
	}
	return s
}

// RolloverMovedComment is what a dependent's conversation records when
// Store.Rollover moves it onto the successor of the task it waited on.
func RolloverMovedComment(oldID, successorID string) string {
	return fmt.Sprintf("Now waits on task %s, which task %s was rolled over to.", successorID, oldID)
}

// RolloverKeptComment is RolloverMovedComment for a dependent that could
// not be moved, and so was released by the close.
func RolloverKeptComment(oldID, successorID, reason string) string {
	return fmt.Sprintf("Task %s, which this waited on, was rolled over to task %s. This was not "+
		"moved to wait on task %s instead: %s. With task %s closed it no longer waits on it.",
		oldID, successorID, successorID, reason, oldID)
}

// Rollover files a successor to predecessorID and closes predecessorID
// with RolloverOutcome, in one transaction.
//
// The successor has the predecessor's repository, grants, hosting,
// admission, goal, branch and pull request, tags (which is what binds a
// schedule's standing task) and approval. Its description is
// RolloverBody, and it starts with no task_context of its own. The
// predecessor keeps everything but the references that move:
//
//   - its change links (IsChange), so the pull request is the
//     successor's and closing the predecessor does not orphan it;
//   - its goal (task_goal), notification subscriptions and the open
//     notifications about it;
//   - every open task's blocking link to it (depends-on, and with it
//     wait_for_task's parks on tasks), as Store.Supersede moves them;
//   - every standing park whose resume_on names it.
//
// by is who rolled it over, and why the phrase saying why, appended to
// the predecessor's closing comment ("" for none).
func (s *Store) Rollover(ctx context.Context, predecessorID string, by Attribution, why string,
	now time.Time) (RolloverResult, error) {

	predecessorID = NormalizeTaskID(predecessorID)
	now = now.UTC()
	var result RolloverResult
	err := s.write(ctx, "roll over task "+predecessorID, func(tx *sql.Tx) error {
		result = RolloverResult{}
		pred, err := getTask(ctx, tx, predecessorID)
		if err != nil {
			return err
		}
		if pred == nil {
			return fmt.Errorf("%w task %s: no such task", ErrRolloverRefused, predecessorID)
		}
		state, _, err := taskStateTx(ctx, tx, predecessorID)
		if err != nil {
			return err
		}
		if refusal := RolloverRefusal(*pred, state); refusal != "" {
			return fmt.Errorf("%w task %s: %s", ErrRolloverRefused, predecessorID, refusal)
		}
		brief, handover, err := rolloverCarry(ctx, tx, *pred)
		if err != nil {
			return err
		}
		id, err := newTaskID(ctx, tx)
		if err != nil {
			return err
		}
		result.Successor = id

		succ := *pred
		succ.ID = id
		succ.Body = RolloverBody(pred.ID, brief, handover)
		succ.CreatedAt = &now
		succ.Links = []Link{{Kind: LinkRolledOverFrom, Target: pred.ID}}
		var kept []Link
		for _, l := range pred.Links {
			if l.Kind.IsChange() {
				succ.Links = append(succ.Links, l)
				continue
			}
			kept = append(kept, l)
			if !rolloverKeepsOff[l.Kind] {
				succ.Links = append(succ.Links, l)
			}
		}
		if err := putTask(ctx, tx, succ); err != nil {
			return fmt.Errorf("filing task %s's successor: %w", pred.ID, err)
		}
		// A goal from the successor's template would have been declared
		// by putTask; the predecessor's own, with its progress, replaces it.
		if err := moveTaskGoalTx(ctx, tx, pred.ID, id); err != nil {
			return err
		}

		closed := *pred
		closed.Links = append(kept, Link{Kind: LinkRolledOverTo, Target: id})
		if err := putTask(ctx, tx, closed); err != nil {
			return err
		}
		if err := closeInTx(ctx, tx, pred.ID, now, by.Actor, "rolled over to task "+id); err != nil {
			return err
		}
		if _, err := addComment(ctx, tx, Comment{
			TaskID:    pred.ID,
			Author:    by,
			Body:      RolloverComment(id, why),
			CreatedAt: now,
		}); err != nil {
			return err
		}

		var moved SupersedeResult
		if err := retargetDependents(ctx, tx, pred.ID, id, by, now, &moved,
			RolloverMovedComment, RolloverKeptComment); err != nil {
			return err
		}
		result.Moved, result.Kept = moved.Moved, moved.Kept
		if result.Suspensions, err = retargetSuspensionsTx(ctx, tx, pred.ID, id); err != nil {
			return err
		}
		if result.Notifications, err = moveNotificationsTx(ctx, tx, pred.ID, id); err != nil {
			return err
		}
		return nil
	})
	return result, err
}

// rolloverCarry is what a successor's description carries of t: its
// current brief, or its description when it has none, and its last
// handover.
func rolloverCarry(ctx context.Context, tx *sql.Tx, t Task) (brief, handover string, err error) {
	brief = t.Body
	var current string
	err = tx.QueryRowContext(ctx,
		"SELECT `current_brief` FROM `task_context` WHERE `task_id` = ?", t.ID).Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", "", fmt.Errorf("reading task %s's brief: %w", t.ID, err)
	}
	if strings.TrimSpace(current) != "" {
		brief = current
	}
	err = tx.QueryRowContext(ctx,
		"SELECT `body` FROM `task_comment` WHERE `task_id` = ? AND `handover` = ? ORDER BY `id` DESC LIMIT 1",
		t.ID, true).Scan(&handover)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", "", fmt.Errorf("reading task %s's handover: %w", t.ID, err)
	}
	return brief, handover, nil
}

// moveTaskGoalTx moves from's goal, if it has one, onto to.
func moveTaskGoalTx(ctx context.Context, tx *sql.Tx, from, to string) error {
	var n int
	if err := tx.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM `task_goal` WHERE `task_id` = ?", from).Scan(&n); err != nil {
		return fmt.Errorf("reading task %s's goal: %w", from, err)
	}
	if n == 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM `task_goal` WHERE `task_id` = ?", to); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "UPDATE `task_goal` SET `task_id` = ? WHERE `task_id` = ?", to, from)
	return err
}

// retargetSuspensionsTx points every standing park whose resume_on waits
// on from at to instead, and reports how many it changed.
func retargetSuspensionsTx(ctx context.Context, tx *sql.Tx, from, to string) (int, error) {
	type park struct {
		runID string
		on    ResumeOn
	}
	var parks []park
	if err := each(ctx, tx,
		"SELECT `run_id`, `resume_on` FROM `task_suspension` WHERE `resumed_at` IS NULL AND `resume_on` IS NOT NULL",
		nil, func(rows *sql.Rows) error {
			var runID, raw string
			if err := rows.Scan(&runID, &raw); err != nil {
				return err
			}
			var on ResumeOn
			if json.Unmarshal([]byte(raw), &on) != nil {
				return nil
			}
			changed := false
			for i, id := range on.Tasks {
				if NormalizeTaskID(id) == from {
					on.Tasks[i] = to
					changed = true
				}
			}
			if changed {
				parks = append(parks, park{runID, on})
			}
			return nil
		}); err != nil {
		return 0, fmt.Errorf("reading parks on task %s: %w", from, err)
	}
	for _, p := range parks {
		raw, err := json.Marshal(p.on)
		if err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx,
			"UPDATE `task_suspension` SET `resume_on` = ? WHERE `run_id` = ?", string(raw), p.runID); err != nil {
			return 0, err
		}
	}
	return len(parks), nil
}

// moveNotificationsTx moves from's notification subscriptions, and the
// open notifications about it or investigated by it, onto to, and
// reports how many notifications it moved.
func moveNotificationsTx(ctx context.Context, tx *sql.Tx, from, to string) (int, error) {
	if _, err := tx.ExecContext(ctx,
		"UPDATE `task_notification_subscription` SET `task_id` = ? WHERE `task_id` = ?", to, from); err != nil {
		return 0, fmt.Errorf("moving task %s's subscriptions: %w", from, err)
	}
	res, err := tx.ExecContext(ctx,
		"UPDATE `notification` SET `task_id` = ? WHERE `task_id` = ? AND `cleared_at` IS NULL", to, from)
	if err != nil {
		return 0, fmt.Errorf("moving task %s's notifications: %w", from, err)
	}
	n, _ := res.RowsAffected()
	if _, err := tx.ExecContext(ctx,
		"UPDATE `notification` SET `investigation_task_id` = ? WHERE `investigation_task_id` = ? AND `cleared_at` IS NULL",
		to, from); err != nil {
		return 0, fmt.Errorf("moving task %s's investigations: %w", from, err)
	}
	return int(n), nil
}
