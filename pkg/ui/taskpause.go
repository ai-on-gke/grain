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

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// PauseOptions is what a caller chose at the moment of pausing.
type PauseOptions struct {
	// Immediate skips a running task's grace period: its run is cancelled
	// on the next cycle rather than told to wrap up first.
	Immediate bool
}

// Pause is an operator's "stop working on this, keep its place", for a
// task in any open state (model.TaskPause has the design). What it does
// depends on what the task is doing:
//
//   - running: the run is told, in its own session, to commit, push and
//     write its handover within the grace period (Config.PauseGrace), and
//     orchestrator.reconcilePauses cancels it once that has passed -- or
//     at once with opts.Immediate. The run is recorded as interrupted, so
//     it costs the task no strike, and its CLI session is kept.
//   - awaiting_submit, or on the merge queue: the merge queue and any
//     batch leave it alone while it is paused; its pull request stays
//     open.
//   - every other open state: the flag alone. A pending question stays
//     pending, and is still answerable after a resume.
//
// In every state its grain CI runs in flight are cancelled and it is never
// dispatched while paused, so it holds no worker, connection or sandbox.
//
// A closed task is refused. Pausing a task that is already paused is a
// no-op -- except that --now on a graceful pause still waiting out its
// grace period ends the wait.
func (c *Client) Pause(ctx context.Context, id string, opts PauseOptions) error {
	task, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return err
	}
	if task == nil {
		return &NotFoundError{ID: id}
	}
	state, err := c.Store.State(ctx, id)
	if err != nil {
		return err
	}
	if state == model.StateClosed {
		return validationErrorf("task %s is closed: reopen it first if there is work to pause", id)
	}
	existing, err := c.Store.GetPause(ctx, id)
	if err != nil {
		return err
	}
	actor := c.requestActor(ctx).Actor
	if existing != nil {
		if actor.Kind == model.PrincipalAgent && !existing.PausedBy.Same(actor) {
			return forbiddenErrorf("task %s was paused by %s %s; only they may change or lift that pause",
				id, existing.PausedBy.Kind, principalName(existing.PausedBy))
		}
		if opts.Immediate && !existing.Immediate {
			if err := c.Store.SetPauseImmediate(ctx, id); err != nil {
				return err
			}
			c.wake()
		}
		return nil
	}

	now := c.now()
	p := model.TaskPause{TaskID: id, PausedAt: now, PausedBy: actor, FromState: state, Immediate: opts.Immediate}
	if state == model.StateRunning {
		runs, err := c.Store.Runs(ctx, id)
		if err != nil {
			return err
		}
		for i := range runs {
			if runs[i].FinishedAt == nil {
				p.RunID = runs[i].ID
			}
		}
		if !opts.Immediate {
			until := now.Add(c.pauseGrace())
			p.GraceUntil = &until
		}
	}
	paused, err := c.Store.Pause(ctx, p)
	if err != nil || !paused {
		return err
	}

	// Said in the task's conversation. A graceful pause of a running task
	// says it as the operator, which is what carries it into the live
	// session (pkg/mcp's addenda deliver a person's comments on the run's
	// next tool result); everything else is grain's own note, so that it
	// is never mistaken for a reply a parked task was waiting on.
	body := fmt.Sprintf("Paused by %s.", principalName(actor))
	author := model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}}
	switch {
	case state == model.StateRunning && !opts.Immediate:
		body = model.PauseMessage(c.pauseGrace())
		author = model.Attribution{Actor: actor}
	case state == model.StateRunning:
		body += " The run in progress is being stopped now; whatever it had not pushed is gone."
	}
	if _, err := c.Store.AddComment(ctx, model.Comment{TaskID: id, Author: author, Body: body, CreatedAt: now}); err != nil {
		return err
	}
	c.wake()
	return nil
}

// Resume lifts a pause: the task goes back to exactly the standing it had
// -- nothing about it was changed by pausing -- and the run-now request a
// paused run had spent is given back (model.Store.Resume). A task paused
// while running is queued again, and resumes its saved CLI session, with
// a note in its conversation (model.ResumedNote) that its next prompt
// carries.
//
// Resuming a task that is not paused is a no-op.
func (c *Client) Resume(ctx context.Context, id string) error {
	task, err := c.Store.GetTask(ctx, id)
	if err != nil {
		return err
	}
	if task == nil {
		return &NotFoundError{ID: id}
	}
	actor := c.requestActor(ctx).Actor
	if actor.Kind == model.PrincipalAgent {
		existing, err := c.Store.GetPause(ctx, id)
		if err != nil {
			return err
		}
		if existing != nil && !existing.PausedBy.Same(actor) {
			return forbiddenErrorf("task %s was paused by %s %s; only they may lift that pause",
				id, existing.PausedBy.Kind, principalName(existing.PausedBy))
		}
	}
	now := c.now()
	lifted, err := c.Store.Resume(ctx, id, now)
	if err != nil || lifted == nil {
		return err
	}
	if _, err := c.Store.AddComment(ctx, model.Comment{
		TaskID:    id,
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}},
		Body:      fmt.Sprintf("Resumed by %s. %s", principalName(actor), model.ResumedNote(*lifted, now)),
		CreatedAt: now,
	}); err != nil {
		return err
	}
	c.wake()
	return nil
}

func (c *Client) pauseGrace() time.Duration {
	if c.Config.PauseGrace > 0 {
		return c.Config.PauseGrace
	}
	return model.PauseGracePeriod
}

func (c *Client) wake() {
	if c.Config.Wake != nil {
		c.Config.Wake()
	}
}

func principalName(p model.Principal) string {
	if p.ID == "" {
		return string(p.Kind)
	}
	return p.ID
}

// pauseRequest is POST /api/tasks/{id}/pause's optional body.
type pauseRequest struct {
	Immediate bool `json:"immediate,omitempty"`
}

// handlePause and handleResume are the Pause and Resume buttons, `grain
// pause` and `grain resume` (Client.Pause, Client.Resume). An empty body is
// a graceful pause.
func (s *Server) handlePause(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req pauseRequest
	if !readOptionalJSON(w, r, &req) {
		return
	}
	if err := s.tasks.Pause(r.Context(), id, PauseOptions{Immediate: req.Immediate}); err != nil {
		writeClientError(w, err)
		return
	}
	s.respondWithTask(w, r, id)
}

func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.tasks.Resume(r.Context(), id); err != nil {
		writeClientError(w, err)
		return
	}
	s.respondWithTask(w, r, id)
}

// TaskPaused is Task.Paused: who paused the task, when, and what it was
// doing at the time.
type TaskPaused struct {
	At        time.Time   `json:"at"`
	By        string      `json:"by"`
	FromState model.State `json:"fromState"`
	Immediate bool        `json:"immediate,omitempty"`
	// GraceUntil is when a graceful pause of a running task stops waiting
	// for the run to wrap up.
	GraceUntil *time.Time `json:"graceUntil,omitempty"`
}

// withPause marks t paused, and takes its stall note away: a task a
// person paused is waiting on that person, not stuck.
func (t *Task) withPause(p model.TaskPause) {
	t.Paused = &TaskPaused{At: p.PausedAt, By: principalName(p.PausedBy), FromState: p.FromState,
		Immediate: p.Immediate, GraceUntil: p.GraceUntil}
	t.Stall = ""
	// And its sub-case is the pause itself: held by a person, ended by
	// Resume -- never whatever stalled reason its state would otherwise
	// carry, nor a merge-queue wait the queue is not pursuing.
	t.Reason = &Reason{Reason: string(t.State) + "/operator_paused", Class: "awaiting a person",
		Resource: "the operator to resume it", Control: "Resume"}
	t.MergeQueueReason = nil
}

func (c *Client) attachPause(ctx context.Context, t *Task) error {
	p, err := c.Store.GetPause(ctx, t.ID)
	if err != nil || p == nil {
		return err
	}
	t.withPause(*p)
	return nil
}
