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
	"slices"
	"strings"
	"time"
)

// misdirectedMessagesRoute is the route whose misuse
// ClearMisdirectedQuestions undoes, as pkg/ui's recordAgentCall names it
// in the note it leaves on the task an agent-API call changed.
const misdirectedMessagesRoute = "POST /api/tasks/{id}/messages"

// ClearMisdirectedQuestions lifts every open task's pending question that
// was never that task's question: one another task's run posted through
// POST /api/tasks/{id}/messages, before the agent API refused that route
// on any task but the caller's own. That route speaks as the run of task
// {id} and parks {id} on its reply, so a run that called it to advise
// another task's agent (task 768's notes to tasks 796, 657 and 736) left
// that task awaiting_reply on words it never said, in front of an
// operator who had nothing to answer.
//
// Such a question is recognized from the conversation alone: the pending
// comment is grain speaking on the parked task's own behalf, and the
// note recordAgentCall writes straight after any agent-API change to
// another task -- signed on the calling task's behalf, naming the
// /messages route -- follows it. A question a task's own run asked has
// no such note, since a run's call on its own task leaves none.
//
// Each lifted question gets a note of grain's own saying why, and the ids
// of the tasks lifted come back for the caller to log. It is safe to run
// on every start: once lifted, a task no longer matches.
func (s *Store) ClearMisdirectedQuestions(ctx context.Context, now time.Time) ([]string, error) {
	pending := map[string]int64{}
	err := each(ctx, s.db,
		"SELECT `task_id`,`pending_question_comment_id` FROM `task_observation` "+
			"WHERE `pending_question_comment_id` IS NOT NULL AND `closed_at` IS NULL", nil,
		func(rows *sql.Rows) error {
			var id string
			var comment int64
			if err := rows.Scan(&id, &comment); err != nil {
				return err
			}
			pending[id] = comment
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("reading pending questions: %w", err)
	}
	var cleared []string
	for taskID, commentID := range pending {
		comments, err := s.Comments(ctx, taskID)
		if err != nil {
			return cleared, fmt.Errorf("reading %s's conversation: %w", taskID, err)
		}
		sender := misdirectedQuestionSender(comments, taskID, commentID)
		if sender == "" {
			continue
		}
		lifted := false
		if err := s.ObserveField(ctx, taskID, now, func(o *Observation) {
			if o.PendingQuestionCommentID != nil && *o.PendingQuestionCommentID == commentID {
				o.PendingQuestionCommentID = nil
				lifted = true
			}
		}); err != nil {
			return cleared, fmt.Errorf("lifting %s's misdirected question: %w", taskID, err)
		}
		if !lifted {
			continue
		}
		if _, err := s.AddComment(ctx, Comment{
			TaskID: taskID,
			Author: Attribution{Actor: Principal{Kind: PrincipalAutomation, ID: "grain"}},
			Body: fmt.Sprintf("grain lifted the question above: task %s's run posted it as this task's "+
				"through `%s`, so it was never this task's question and nobody needs to answer it. "+
				"A run sends another task's agent a message with `POST /api/tasks/{id}/agent-messages` now.",
				sender, misdirectedMessagesRoute),
			CreatedAt: now,
		}); err != nil {
			return cleared, fmt.Errorf("noting %s's lifted question: %w", taskID, err)
		}
		cleared = append(cleared, taskID)
	}
	slices.Sort(cleared)
	return cleared, nil
}

// misdirectedQuestionSender is the task whose run posted taskID's pending
// question commentID through another task's /messages route, or "" when
// the question is taskID's own. It looks a few comments past the question
// for recordAgentCall's note rather than only at the next one, so a
// comment written in between does not hide it.
func misdirectedQuestionSender(comments []Comment, taskID string, commentID int64) string {
	at := slices.IndexFunc(comments, func(c Comment) bool { return c.ID == commentID })
	if at < 0 {
		return ""
	}
	question := comments[at].Author
	if question.Actor.Kind != PrincipalAutomation || question.OnBehalfOf == nil ||
		question.OnBehalfOf.Kind != PrincipalAgent || question.OnBehalfOf.ID != taskID {
		return ""
	}
	for _, c := range comments[at+1 : min(len(comments), at+4)] {
		by := c.Author.OnBehalfOf
		if c.Author.Actor.Kind != PrincipalAutomation || by == nil || by.Kind != PrincipalAgent ||
			by.ID == "" || by.ID == taskID {
			continue
		}
		if strings.HasPrefix(c.Body, fmt.Sprintf("Task %s called `%s`", by.ID, misdirectedMessagesRoute)) {
			return by.ID
		}
	}
	return ""
}
