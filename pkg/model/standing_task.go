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
	"fmt"
	"slices"
	"strings"
	"time"
)

// IsStandingTemplateID reports whether a schedule firing from template
// id wakes one long-lived task instead of filing a new one each time
// (wakeStandingTask): the task the last firing filed, if it is still
// open and idle, is put back in the queue with a note saying it was
// woken, and keeps its conversation and its handover.
//
// A property of the id rather than a Template field, because only
// grain's own templates are standing -- today the pipeline steward
// (PipelineStewardTemplateID) -- and they are resolved from this build at
// every firing. Every Template field is a settings column a deployment
// can write (pkg/declschema), and a template that could make itself
// standing could keep a task, and every grant on it, alive across
// firings that never re-asked for them.
func IsStandingTemplateID(id string) bool { return id == PipelineStewardTemplateID }

// wakeStandingTask is what a firing of a standing template
// (IsStandingTemplateID) does with the task an earlier firing filed, which
// is still open: puts it back in the queue for another run, the way a
// person's "continue" on a finished report does (ui.Client's
// reopenAcknowledgeable) -- the completion goes, RetryRequestedAt is
// stamped so a task at its failure-streak limit still gets the attempt --
// and adds a note saying it was woken and why (StandingWakeComment).
//
// Only a task that is idle is woken: one whose last run finished into
// awaiting_reply with nothing pending. woke false is every other case,
// and the caller holds the firing as an overlap:
//
//   - queued or running: the wake is already happening.
//   - parked on a question or a secret: a person is being waited on, and
//     a wake that ran over the top of the question would answer it with
//     nothing.
//   - failed, proposed or deferred: somebody has to look before it runs
//     again, and a half-hourly cadence retrying it would spend a sandbox
//     every half hour on the same refusal.
//
// The task's content is refreshed from comp on the way, so a prompt this
// build changed reaches the next run rather than the one filed under the
// build before (the same reason firing resolves the template fresh,
// fireTaskSchedule's own doc comment). Grants are merged rather than
// replaced (mergeStandingGrants): a grant a person added to the standing
// task is that person's decision about this job and is kept, while one
// grain added that this build's template no longer names is dropped.
func (s *Store) wakeStandingTask(ctx context.Context, open Task, comp Composition,
	reason string, now time.Time) (woke bool, err error) {

	state, err := s.State(ctx, open.ID)
	if err != nil {
		return false, fmt.Errorf("reading standing task %s: %w", open.ID, err)
	}
	// A durable standing task with a goal does not finish into
	// awaiting_reply: it rests, deferred, on its goal or a message
	// (orchestrator.restOnGoal). A firing is such a message.
	resting, err := s.RestingTask(ctx, open.ID)
	if err != nil {
		return false, err
	}
	if state != StateAwaitingReply && !resting {
		return false, nil
	}
	// A standing task an operator has paused is held, not woken: the
	// firing is skipped the way an overlap is, naming the task, and the
	// pause decides when the job works again (TaskPause).
	if p, err := s.GetPause(ctx, open.ID); err != nil {
		return false, fmt.Errorf("reading standing task %s's pause: %w", open.ID, err)
	} else if p != nil {
		return false, nil
	}
	obs, err := s.GetObservation(ctx, open.ID)
	if err != nil {
		return false, fmt.Errorf("reading standing task %s: %w", open.ID, err)
	}
	if obs == nil && resting {
		obs = &Observation{}
	}
	if obs == nil || obs.PendingQuestionCommentID != nil || obs.PendingSecret != "" {
		return false, nil
	}
	if obs.CompletedAt == nil && !resting {
		return false, nil
	}
	if err := s.UpdateTask(ctx, open.ID, func(t *Task) error {
		t.Title, t.Body = comp.Title, comp.Body
		t.Reads, t.NetworkAccess, t.NothingNeededAction, t.KeepRunning = comp.Reads, comp.NetworkAccess, comp.NothingNeededAction, comp.KeepRunning
		t.Grants = mergeStandingGrants(t.Grants, comp.Grants)
		// The model is refreshed only onto a task nobody chose one for:
		// a model a person set on the standing task is their decision
		// about this job, like a grant they added.
		if strings.TrimSpace(t.Model) == "" && comp.Model != "" {
			t.Model, t.ModelSource = comp.Model, comp.Sources.Model
		}
		return nil
	}); err != nil {
		return false, fmt.Errorf("refreshing standing task %s: %w", open.ID, err)
	}
	if _, err := s.AddComment(ctx, Comment{
		TaskID:    open.ID,
		Author:    Attribution{Actor: SchedulePrincipal},
		Body:      StandingWakeComment(reason),
		CreatedAt: now,
	}); err != nil {
		return false, fmt.Errorf("noting the wake on standing task %s: %w", open.ID, err)
	}
	if resting {
		if err := s.SetDeferred(ctx, open.ID, nil, nil, now); err != nil {
			return false, fmt.Errorf("waking standing task %s: %w", open.ID, err)
		}
	}
	if err := s.ObserveField(ctx, open.ID, now, func(o *Observation) {
		o.CompletedAt = nil
		o.RetryRequestedAt = &now
	}); err != nil {
		return false, fmt.Errorf("re-queueing standing task %s: %w", open.ID, err)
	}
	return true, nil
}

// RestingTask reports whether task id is resting: deferred by its own
// run's requested suspension to resume on a message or its goal
// (orchestrator.restOnGoal, resume_later with message), or put to rest by
// its met goal (on_met rest) with no wake time -- the state a notification
// or a standing firing wakes a durable task out of. A deferral a person
// made, or one holding a goal at its progress limit, is not rest.
func (s *Store) RestingTask(ctx context.Context, id string) (bool, error) {
	held, err := s.heldResumes(ctx, id)
	if err != nil {
		return false, fmt.Errorf("reading task %s's suspensions: %w", id, err)
	}
	for _, h := range held {
		if h.resumeOn.Message || h.resumeOn.Goal {
			return true, nil
		}
	}
	t, err := s.GetTask(ctx, id)
	if err != nil || t == nil || t.DeferredAt == nil || t.DeferredUntil != nil {
		return false, err
	}
	g, err := s.TaskGoal(ctx, id)
	if err != nil || g == nil {
		return false, err
	}
	return g.OnMet == GoalRestOnMet && g.MetAt != nil && g.LimitNotifiedAt == nil, nil
}

// applyStandingDeclarations gives a standing template's task what the
// template declares beyond its content (docs/design/durable-tasks.md's
// Migration): its goal, when the task has none -- one a person changed
// is kept -- and its notification subscriptions
// (StandingTemplateSubscriptions), added to any the task already has.
// It runs at every firing, filing or waking, so a standing task filed
// before this build declared them is given them without losing its
// handover. System templates are not rows in the template table, so
// putTask's goalFromTemplateTx cannot do this for them.
func (s *Store) applyStandingDeclarations(ctx context.Context, taskID string, tmpl Template, now time.Time) error {
	if tmpl.Goal != nil {
		g, err := s.TaskGoal(ctx, taskID)
		if err != nil {
			return err
		}
		if g == nil {
			if err := s.SetTaskGoal(ctx, taskID, *tmpl.Goal, "template:"+tmpl.ID, now); err != nil {
				return fmt.Errorf("declaring standing task %s's goal: %w", taskID, err)
			}
		}
	}
	want := StandingTemplateSubscriptions(tmpl.ID)
	if len(want) == 0 {
		return nil
	}
	have, err := s.TaskNotificationSubscriptions(ctx, taskID)
	if err != nil {
		return err
	}
	merged := slices.Clone(have)
	for _, f := range want {
		if !slices.Contains(merged, f) {
			merged = append(merged, f)
		}
	}
	if len(merged) == len(have) {
		return nil
	}
	return s.SetTaskNotificationSubscriptions(ctx, taskID, merged)
}

// mergeStandingGrants is a standing task's grants after a wake: every
// grant of want, plus every grant it already holds that something other
// than grain gave it. A grant a person added (the documented way to widen
// the job, docs/pipeline-steward.md on releases:write) is that person's
// decision and is kept. A grant grain applied to itself (GrantByGrain,
// an earlier build's template) that want no longer names is dropped:
// grain's own grants are this build's to decide, and a build that
// narrowed the template -- taking a preset away from the job, say --
// must not leave the standing task holding it for as long as it stays
// open, which is indefinitely.
func mergeStandingGrants(have, want []Grant) []Grant {
	out := make([]Grant, 0, len(have)+len(want))
	for _, h := range have {
		if h.Via == GrantByGrain && !slices.ContainsFunc(want, func(g Grant) bool { return g.Capability == h.Capability }) {
			continue
		}
		out = append(out, h)
	}
	for _, g := range want {
		if !slices.ContainsFunc(out, func(h Grant) bool { return h.Capability == g.Capability }) {
			out = append(out, g)
		}
	}
	return out
}

// StandingWakeComment is the note grain adds to a standing task each
// time a firing wakes it, so the run can tell this wake from the last
// one and knows why it happened: its cadence, or something grain thought
// it should hear about now (reason, FireScheduleWake).
func StandingWakeComment(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return "_Scheduled wake._ Read the queue again and act on what has changed since your handover."
	}
	return "_Woken early:_ " + reason + ". Read the queue again and act on what has changed since your handover."
}

// DropQuietFiring is what the daemon does instead of firing sched when
// it has already found there is nothing for the job to do: the cadence
// moves on to its next occurrence, LastRunAt stays where it was because
// nothing ran, and the occurrence is written down as dropped
// (SkippedQuiet) with detail saying why -- the same bargain
// suppressStandingWorkOff makes, for the same reason: holding NextRunAt
// would re-ask every cycle and owe a catch-up firing the moment
// something went wrong, which is the wake the job already gets.
func (s *Store) DropQuietFiring(ctx context.Context, sched Schedule, now time.Time,
	loc *time.Location, detail string) (ScheduleSkip, bool, error) {

	next := nextOccurrence(sched, now, loc)
	if err := s.UpdateSchedule(ctx, sched.ID, func(sc *Schedule) error {
		sc.NextRunAt = next
		return nil
	}); err != nil {
		return ScheduleSkip{}, false, fmt.Errorf("advancing past a quiet firing: %w", err)
	}
	skip := *droppedSkip(sched, now, SkippedQuiet, detail)
	created, err := s.RecordScheduleSkip(ctx, skip)
	if err != nil {
		return skip, false, err
	}
	return skip, created, nil
}
