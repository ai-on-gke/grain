// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
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
	"strconv"
	"strings"
)

// A receipt joins source identity to its own delivery acknowledgment.
// Chat replies can advance the conversation cursor without showing PR feedback.
// It is deliberately not a schedule: several comments owe one feedback run,
// and a comment arriving during a run is pending until actually shown.
const prFeedbackTable = "CREATE TABLE IF NOT EXISTS `task_pr_feedback` (" +
	"`task_id` TEXT NOT NULL, `event_key` TEXT NOT NULL, `comment_id` INTEGER NOT NULL, `received_run_id` TEXT NOT NULL DEFAULT '', `delivered` INTEGER NOT NULL DEFAULT 0," +
	"PRIMARY KEY (`task_id`, `event_key`))"

// prFeedbackWaitSQL is a narrow exception to task_ready's queued-state test.
// It retains the original wait, including CI's head evidence and questions.
// Permissions, secrets, operator deferrals, time/task-only parks and future
// scheduling are not feedback wakes. Requested suspensions use ResumeOn.JSON.
var prFeedbackWaitSQL = `
 EXISTS (SELECT 1 FROM task_pr_feedback f
         WHERE f.task_id = t.id AND f.delivered = 0)
 AND t.approval_actor_kind IS NOT NULL
 AND o.closed_at IS NULL
 AND o.pr_closed_at IS NULL
 AND o.pr_merged_at IS NULL
 AND NOT EXISTS (SELECT 1 FROM task_comment stopped WHERE stopped.id = o.pending_question_comment_id
                 AND stopped.body = '` + strings.ReplaceAll(TurnStoppedComment, "'", "''") + `')
 AND COALESCE(o.streak_count, 0) < ` + strconv.Itoa(MaxConsecutiveFailures) + `
 AND COALESCE(o.pending_secret, '') = ''
 AND COALESCE(o.pending_access_request, '') = ''
 AND NOT EXISTS (SELECT 1 FROM task_run live WHERE live.task_id = t.id AND live.finished_at IS NULL)
 AND NOT EXISTS (SELECT 1 FROM task_link gate WHERE gate.task_id = t.id AND gate.kind = 'review-questions')
 AND (
   (t.deferred_at IS NULL AND t.deferred_until IS NULL AND o.pending_question_comment_id IS NOT NULL AND o.completed_at IS NULL)
   OR (t.deferred_at IS NULL AND t.deferred_until IS NULL AND o.completed_at IS NOT NULL AND EXISTS (
     SELECT 1 FROM task_pr_feedback late JOIN task_run ended ON ended.id = late.received_run_id
     WHERE late.task_id = t.id AND late.delivered = 0 AND ended.finished_at IS NOT NULL
   ))
   OR EXISTS (
     SELECT 1 FROM task_suspension su JOIN task_run sr ON sr.id = su.run_id
     WHERE su.task_id = t.id AND su.kind = 'requested' AND su.resumed_at IS NULL
       AND sr.finished_at IS NOT NULL
       AND t.deferred_at >= sr.started_at AND t.deferred_at <= su.created_at
       AND (su.resume_on LIKE '%"ci":%' OR su.resume_on LIKE '%"message":true%')
   )
 )
`

// RecordPRFeedback atomically records the deduplication receipt and its context.
// Callers own forge authorization and self/relay filtering. Existing event links
// also suppress historical receipts recorded by older versions.
func (s *Store) RecordPRFeedback(ctx context.Context, c Comment, eventKey string, wakeCompleted bool) error {
	return s.RecordPRFeedbackBatch(ctx, c, []string{eventKey}, wakeCompleted)
}

// RecordPRFeedbackBatch shares one conversation entry across a polled batch.
// Every source identity is checked again in the write transaction.
func (s *Store) RecordPRFeedbackBatch(ctx context.Context, c Comment, eventKeys []string, wakeCompleted bool) error {
	recorded := false
	err := s.write(ctx, "record PR feedback on "+c.TaskID, func(tx *sql.Tx) error {
		t, err := getTask(ctx, tx, c.TaskID)
		if err != nil || t == nil {
			return err
		}
		var fresh []string
		unique := map[string]bool{}
		for _, eventKey := range eventKeys {
			// Receipts remain authoritative if a later task update replaces its
			// provenance links. A replay must still be an idempotent success.
			var one int
			err := tx.QueryRowContext(ctx, "SELECT 1 FROM task_pr_feedback WHERE task_id = ? AND event_key = ?", c.TaskID, eventKey).Scan(&one)
			if err == nil {
				continue
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if unique[eventKey] {
				continue
			}
			unique[eventKey] = true
			seen := false
			for _, l := range t.Links {
				if l.Kind == LinkGitHubEvent && l.Target == eventKey {
					seen = true
				}
				if l.Kind == LinkReviewThread && eventKey == "review_comment:"+l.Target {
					seen = true
				}
				if l.Kind == LinkRelayedReviewThread {
					if rt, ok := ParseRelayedReviewThread(l.Target); ok && eventKey == "review_comment:"+strconv.Itoa(rt.CommentID) {
						seen = true
					}
				}
			}
			if !seen {
				fresh = append(fresh, eventKey)
			}
		}
		if len(fresh) == 0 {
			return nil
		}
		var liveID string
		err = tx.QueryRowContext(ctx, "SELECT id FROM task_run WHERE task_id = ? AND finished_at IS NULL", c.TaskID).Scan(&liveID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		// FinishRun precedes ProcessResult. During that teardown window the
		// task is queued but the previous result may still complete it. Keep
		// the run identity in that window too, so completion cannot swallow
		// feedback which the just-finished agent was never shown.
		if liveID == "" {
			o, err := getObservation(ctx, tx, c.TaskID)
			if err != nil {
				return err
			}
			if o == nil || o.CompletedAt == nil {
				err = tx.QueryRowContext(ctx, "SELECT id FROM task_run WHERE task_id = ? ORDER BY started_at DESC, attempt DESC LIMIT 1", c.TaskID).Scan(&liveID)
				if err != nil && !errors.Is(err, sql.ErrNoRows) {
					return err
				}
			}
		}
		id, err := addComment(ctx, tx, c)
		if err != nil {
			return err
		}
		recorded = true
		for _, eventKey := range fresh {
			if _, err := tx.ExecContext(ctx, "INSERT INTO task_pr_feedback (task_id,event_key,comment_id,received_run_id) VALUES (?,?,?,?)", c.TaskID, eventKey, id, liveID); err != nil {
				return err
			}
			t.Links = append(t.Links, Link{Kind: LinkGitHubEvent, Target: eventKey})
			if root, ok := strings.CutPrefix(eventKey, "review_comment:"); ok {
				t.Links = append(t.Links, Link{Kind: LinkReviewThread, Target: root})
			}
		}
		if err := putTask(ctx, tx, *t); err != nil {
			return err
		}
		// Preserve the existing explicit-mention/changes-requested repair behavior
		// for finished changes, but never revive a closed or operator-deferred task.
		o, err := getObservation(ctx, tx, c.TaskID)
		if err != nil {
			return err
		}
		reviewQuestionsHeld := false
		for _, l := range t.Links {
			if l.Kind == LinkReviewQuestions {
				reviewQuestionsHeld = true
			}
		}
		if wakeCompleted && !reviewQuestionsHeld && o != nil && o.CompletedAt != nil && o.ClosedAt == nil && o.PrClosedAt == nil && o.PrMergedAt == nil && t.DeferredAt == nil && o.PendingQuestionCommentID == nil && o.PendingSecret == "" && o.PendingAccess == nil && liveID == "" {
			o.CompletedAt = nil
			o.RetryRequestedAt = &c.CreatedAt
			return observe(ctx, tx, *o)
		}
		return nil
	})
	if err == nil && recorded {
		s.signalCommentAdded(c.TaskID)
	}
	return err
}

// ErrTaskNotReady means admission raced a change to the task's eligibility.
var ErrTaskNotReady = errors.New("model: task is no longer ready")

func prFeedbackWait(ctx context.Context, q querier, taskID string) (bool, error) {
	var ready bool
	err := q.QueryRowContext(ctx, "SELECT ("+prFeedbackWaitSQL+") FROM task t LEFT JOIN task_observation o ON o.task_id = t.id WHERE t.id = ?", taskID).Scan(&ready)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return ready, err
}

// AcknowledgePRFeedback records only feedback actually shown in full, either in
// a dispatch prompt or an acknowledged addendum. Human-only chat delivery must
// never consume intervening PR feedback by advancing the conversation cursor.
func (s *Store) AcknowledgePRFeedback(ctx context.Context, taskID string, through int64) error {
	return s.write(ctx, "acknowledge PR feedback on "+taskID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE task_pr_feedback SET delivered = 1 WHERE task_id = ? AND comment_id <= ?", taskID, through)
		return err
	})
}

// PendingPRFeedback returns unread feedback within a conversation snapshot.
// Full bodies are carried outside the bounded history window.
func (s *Store) PendingPRFeedback(ctx context.Context, taskID string, through int64) ([]Comment, error) {
	return s.queryComments(ctx, taskID, "SELECT "+commentColumns+
		"FROM task_comment WHERE task_id = ? AND id IN ("+
		"SELECT f.comment_id FROM task_pr_feedback f "+
		"WHERE f.task_id = ? AND f.delivered = 0 AND f.comment_id <= ?) ORDER BY id",
		taskID, taskID, through)
}
