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

package ui

// A task's goal (model.TaskGoal, docs/design/durable-tasks.md#DT-4), as
// the task page's goal editor sets and clears it: PUT and DELETE
// /api/tasks/{id}/goal.

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// setGoalRequest is PUT /api/tasks/{id}/goal's body: the goal as
// propose_task's goal argument declares it.
type setGoalRequest model.GoalDecl

// SetGoal declares task id's goal, replacing any it had, and says so on
// the task. Its progress counters start again from now.
func (c *Client) SetGoal(ctx context.Context, id string, goal model.GoalDecl) error {
	task, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return err
	}
	if task == nil {
		return &NotFoundError{ID: id}
	}
	if err := goal.Normalize(); err != nil {
		return validationErrorf("%v", err)
	}
	state, err := c.Store.State(ctx, id)
	if err != nil {
		return err
	}
	if state == model.StateClosed {
		return validationErrorf("%s is closed: reopen it before giving it a goal", id)
	}
	prev, err := c.Store.TaskGoal(ctx, id)
	if err != nil {
		return err
	}
	actor := c.actor(ctx)
	now := c.now()
	if err := c.Store.SetTaskGoal(ctx, id, goal, model.PrincipalString(actor), now); err != nil {
		return err
	}
	_, _ = c.Store.ClearNotification(ctx, model.NotificationSubject(model.FamilyNoProgress, id), model.PrincipalString(actor))
	if prev != nil && task.DeferredAt != nil && (prev.LimitNotifiedAt != nil || (prev.MetAt != nil && prev.OnMet == model.GoalRestOnMet)) {
		if err := c.Store.SetDeferred(ctx, id, nil, nil, now); err != nil {
			return err
		}
	}
	_, err = c.Store.AddComment(ctx, model.Comment{
		TaskID: id, Author: model.Attribution{Actor: actor}, CreatedAt: now,
		Body: fmt.Sprintf("Set this task's goal: %s (on met: %s, at its progress limit: %s).",
			goalWords(goal), goal.OnMet, goal.OnLimit),
	})
	return err
}

// ClearGoal removes task id's goal: it is an ordinary task again, which
// its agent's closing comment completes.
func (c *Client) ClearGoal(ctx context.Context, id string) error {
	task, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return err
	}
	if task == nil {
		return &NotFoundError{ID: id}
	}
	goal, err := c.Store.TaskGoal(ctx, id)
	if err != nil || goal == nil {
		return err
	}
	if err := c.Store.ClearTaskGoal(ctx, id); err != nil {
		return err
	}
	actor := c.actor(ctx)
	now := c.now()
	_, _ = c.Store.ClearNotification(ctx, model.NotificationSubject(model.FamilyNoProgress, id), model.PrincipalString(actor))
	if task.DeferredAt != nil && deferredByGoal(ctx, c.Store, id, *goal) {
		if err := c.Store.SetDeferred(ctx, id, nil, nil, now); err != nil {
			return err
		}
	}
	_, err = c.Store.AddComment(ctx, model.Comment{
		TaskID: id, Author: model.Attribution{Actor: actor}, CreatedAt: now,
		Body: fmt.Sprintf("Removed this task's goal (%s).", goalWords(goal.Decl())),
	})
	return err
}

// deferredByGoal reports whether task id's current deferral was set by its
// goal (at its progress limit, resting after meeting an on_met: rest goal,
// or resting between runs via restOnGoal).
func deferredByGoal(ctx context.Context, store *model.Store, id string, goal model.TaskGoal) bool {
	if goal.LimitNotifiedAt != nil || (goal.MetAt != nil && goal.OnMet == model.GoalRestOnMet) {
		return true
	}
	sus, err := store.Suspensions(ctx, id)
	if err != nil || len(sus) == 0 {
		return false
	}
	last := sus[len(sus)-1]
	if last.ResumedAt != nil || last.Kind != model.SuspendRequested {
		return false
	}
	if on, err := model.ParseResumeOn(last.ResumeOn); err == nil && on.Goal {
		return true
	}
	return strings.HasPrefix(last.Detail, "the agent ended its run; the task rests on its ")
}

// goalWords is a goal in a sentence, for the comments above.
func goalWords(g model.GoalDecl) string {
	switch g.Kind {
	case model.GoalBaseGreen:
		return fmt.Sprintf("`%s` on %s green", g.Spec.Branch, g.Spec.Repo)
	case model.GoalPRMerged:
		if g.Spec.Task != "" {
			return fmt.Sprintf("task %s's pull request merged", g.Spec.Task)
		}
		return fmt.Sprintf("%s#%d merged", g.Spec.Repo, g.Spec.Number)
	case model.GoalTasksClosed:
		return fmt.Sprintf("tasks %v %s", g.Spec.Tasks, g.Spec.Until)
	case model.GoalPipelineHealthy:
		return "the pipeline healthy"
	}
	return "a person closes it"
}

func (s *Server) handleSetGoal(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req setGoalRequest
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.tasks.SetGoal(r.Context(), id, model.GoalDecl(req)); err != nil {
		writeClientError(w, err)
		return
	}
	s.respondWithTask(w, r, id)
}

func (s *Server) handleClearGoal(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.tasks.ClearGoal(r.Context(), id); err != nil {
		writeClientError(w, err)
		return
	}
	s.respondWithTask(w, r, id)
}
