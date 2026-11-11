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
	"sort"
	"strings"
	"time"
)

// Who closed or reopened a task, and when.
//
// Observation.ClosedAt is the task's closure as it stands: a close stamps
// it and a reopen clears it, so a task closed and reopened within seconds
// leaves no trace at all, and a close that stands says nothing about who
// asked for it. That is how five in-flight tasks lost their runs and CI
// on 2026-10-30 with nothing in grain able to say who had done it. A
// TaskEvent is the record that survives: one row per close or reopen
// somebody asked for, written by ui.Client in the same call that moves
// ClosedAt, and never rewritten.
//
// It covers the closes a caller asks for (the UI, the CLI, the API and a
// session's own grant) and grain's own -- a goal met, a task made
// obsolete, a review finished -- which go through Store.ObserveClose
// instead of ObserveField so the event is written with the stamp. Only a
// ClosedAt from before task_event existed reads as an unattributed close
// (MergeTaskEvents).

// TaskEventKind is what a TaskEvent records.
type TaskEventKind string

const (
	TaskEventClosed   TaskEventKind = "closed"
	TaskEventReopened TaskEventKind = "reopened"
)

// The notes operator-attribution spec §3.1 asks of the task actions that
// recorded nobody: who withdrew an approval, submitted, retried,
// deferred, undeferred or parked a task, set what it waits on or
// depends on, moved it in the queue, or set the secret it asked for.
// They are events rather than comments because a person's comment is a
// reply -- it answers a pending question and reaches a live run -- and
// none of these is one. Not timeline steps: MergeTaskEvents leaves them
// out, and the pipeline's change feed (ui.taskChanges) lists them.
const (
	TaskEventApprovalWithdrawn TaskEventKind = "approval-withdrawn"
	TaskEventSubmitted         TaskEventKind = "submitted"
	TaskEventRetried           TaskEventKind = "retried"
	TaskEventDeferred          TaskEventKind = "deferred"
	TaskEventUndeferred        TaskEventKind = "undeferred"
	TaskEventParked            TaskEventKind = "parked"
	TaskEventDependencies      TaskEventKind = "dependencies"
	TaskEventReordered         TaskEventKind = "reordered"
	// TaskEventSecretSet's Detail names the secret, never its value.
	TaskEventSecretSet TaskEventKind = "secret-set"
	// TaskEventAccessGranted's Detail lists what a person granted of an
	// access request (ui.Client.GrantAccessRequest); TaskEventAccessDenied's
	// is their reason for denying one.
	TaskEventAccessGranted TaskEventKind = "access-granted"
	TaskEventAccessDenied  TaskEventKind = "access-denied"
	// TaskEventGateOverridden records an operator's merge:force override of
	// named merge gates, with Reason = the operator's typed reason and
	// Detail = the overridden gates and commit SHA.
	TaskEventGateOverridden TaskEventKind = "gate-overridden"
	// TaskEventGrantAttached and TaskEventGrantDetached record one grant
	// given to or taken from a task after it was filed: a capability, or
	// a repository read or write (unified-grants Q5). Detail is the
	// grant's canonical string, Grant.Key -- its reference, then "=" and
	// its normalized with: -- so a repo#write names its repo and base.
	TaskEventGrantAttached TaskEventKind = "grant-attached"
	TaskEventGrantDetached TaskEventKind = "grant-detached"
)

// TransitionReopened is the timeline's name for a reopen. It is not one
// of StateOf's answers -- a reopened task is in whatever state its record
// implies -- but the moment it came back is worth a step of its own, the
// same way the close it undid is.
const (
	TransitionReopened       State = "reopened"
	TransitionGateOverridden State = "gate_overridden"
)

// TaskEvent is one close or reopen of a task, a change to its
// auto-merge, or a note of who took one of the actions above.
type TaskEvent struct {
	ID     int64
	TaskID string
	Kind   TaskEventKind
	At     time.Time
	// By is who asked: a person, a session or API grant acting as its
	// task's agent, an external agent token, or the deployment's own
	// actor for a caller nothing identified.
	By Principal
	// OnBehalfOf is whose request By relayed, nil for its own
	// (operator-attribution spec §2.4). Nothing sets it yet.
	OnBehalfOf *Principal
	// Reason is what they said, "" for nothing.
	Reason string
	// Detail is what grain did because of it: for a close, the live run
	// and the CI runs it cancelled.
	Detail string
}

// GrainPrincipal is grain itself, as the author of a close it makes on
// its own account.
var GrainPrincipal = Principal{Kind: PrincipalAutomation, ID: "grain"}

// RecordTaskEvent appends one event and returns its id.
func (s *Store) RecordTaskEvent(ctx context.Context, e TaskEvent) (int64, error) {
	if strings.TrimSpace(e.TaskID) == "" {
		return 0, fmt.Errorf("recording a task event: no task id")
	}
	if e.At.IsZero() {
		e.At = time.Now()
	}
	var id int64
	err := s.write(ctx, "record "+string(e.Kind)+" of task "+e.TaskID, func(tx *sql.Tx) error {
		var err error
		id, err = insertTaskEvent(ctx, tx, e)
		return err
	})
	return id, err
}

func insertTaskEvent(ctx context.Context, tx *sql.Tx, e TaskEvent) (int64, error) {
	res, err := tx.ExecContext(ctx,
		"INSERT INTO `task_event` (`task_id`,`kind`,`at`,`actor_kind`,`actor_id`,`reason`,`detail`,"+
			"`actor_source`,`behalf_kind`,`behalf_id`,`behalf_source`) VALUES (?,?,?,?,?,?,?,?,?,?,?)",
		e.TaskID, string(e.Kind), e.At.UTC(), string(e.By.Kind), e.By.ID, e.Reason, e.Detail,
		string(e.By.Source), behalfColumn(e.OnBehalfOf, func(p Principal) string { return string(p.Kind) }),
		behalfColumn(e.OnBehalfOf, func(p Principal) string { return p.ID }), sourceOf(e.OnBehalfOf))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ObserveClose is ObserveField for a close made without a caller to ask
// for it -- one of grain's own, or a person's that does not go through
// ui.Client.setClosed. set runs on the task's observation as
// ObserveField's does; when it leaves ClosedAt stamped at a moment it was
// not stamped at before, a TaskEvent{closed, by, reason} is appended in
// the same transaction, at that stamp, so the closed step the timeline
// places there says who closed the task and why. A set that leaves
// ClosedAt where it was -- the task already closed, a guard that declined
// -- records nothing, and a task already closed keeps the stamp it has
// whatever set writes (set may still reopen it by clearing ClosedAt).
func (s *Store) ObserveClose(ctx context.Context, taskID string, now time.Time, by Principal, reason string,
	set func(*Observation)) error {

	return s.write(ctx, "close task "+taskID, func(tx *sql.Tx) error {
		obs, err := getObservation(ctx, tx, taskID)
		if err != nil {
			return fmt.Errorf("reading observation for %s: %w", taskID, err)
		}
		if obs == nil {
			obs = &Observation{TaskID: taskID}
		}
		var before time.Time
		if obs.ClosedAt != nil {
			before = *obs.ClosedAt
		}
		set(obs)
		// Most close sites stamp ClosedAt without asking whether the task
		// is already closed. Re-stamping a standing closure would move the
		// close the timeline shows and record a second close nothing
		// reopened in between, so an existing stamp is kept.
		if !before.IsZero() && obs.ClosedAt != nil {
			obs.ClosedAt = &before
		}
		obs.ObservedAt = &now
		observeCtx := WithEventActor(ctx, eventActorOr(ctx, Attribution{Actor: by}))
		if err := observe(observeCtx, tx, *obs); err != nil {
			return err
		}
		return recordCloseInTx(ctx, tx, taskID, before, obs.ClosedAt, by, reason)
	})
}

// recordCloseInTx appends the closed event for a ClosedAt that moved
// from before (zero for none) to after, and nothing when it did not.
func recordCloseInTx(ctx context.Context, tx *sql.Tx, taskID string, before time.Time, after *time.Time,
	by Principal, reason string) error {

	if after == nil || (!before.IsZero() && before.Equal(*after)) {
		return nil
	}
	if _, err := insertTaskEvent(ctx, tx, TaskEvent{TaskID: taskID, Kind: TaskEventClosed, At: *after,
		By: by, Reason: reason}); err != nil {
		return fmt.Errorf("recording the close of %s: %w", taskID, err)
	}
	return nil
}

// TaskEvents is one task's events, oldest first.
func (s *Store) TaskEvents(ctx context.Context, taskID string) ([]TaskEvent, error) {
	return s.taskEvents(ctx, "WHERE `task_id` = ?", taskID)
}

// TaskEventsLatest is at most the newest limit events on taskID, oldest first.
func (s *Store) TaskEventsLatest(ctx context.Context, taskID string, limit int) ([]TaskEvent, error) {
	if limit <= 0 {
		return s.TaskEvents(ctx, taskID)
	}
	var out []TaskEvent
	err := each(ctx, s.db,
		"SELECT `id`,`task_id`,`kind`,`at`,`actor_kind`,`actor_id`,`reason`,`detail`,"+
			"`actor_source`,`behalf_kind`,`behalf_id`,`behalf_source` "+
			"FROM `task_event` WHERE `task_id` = ? ORDER BY `at` DESC, `id` DESC LIMIT ?", []any{taskID, limit},
		func(rows *sql.Rows) error {
			var e TaskEvent
			var kind, actorKind, actorSource, behalfSource string
			var behalfKind, behalfID sql.NullString
			if err := rows.Scan(&e.ID, &e.TaskID, &kind, &e.At, &actorKind, &e.By.ID,
				&e.Reason, &e.Detail, &actorSource, &behalfKind, &behalfID, &behalfSource); err != nil {
				return err
			}
			e.Kind, e.By.Kind, e.By.Source = TaskEventKind(kind), PrincipalKind(actorKind), AuthSource(actorSource)
			e.OnBehalfOf = principalFrom(behalfKind, behalfID, behalfSource)
			out = append(out, e)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("reading task events: %w", err)
	}
	slices.Reverse(out)
	return out, nil
}

// TaskEventsSince is every task's events at or after since, oldest first.
func (s *Store) TaskEventsSince(ctx context.Context, since time.Time) ([]TaskEvent, error) {
	return s.taskEvents(ctx, "WHERE `at` >= ?", since.UTC())
}

func (s *Store) taskEvents(ctx context.Context, where string, arg any) ([]TaskEvent, error) {
	var out []TaskEvent
	err := each(ctx, s.db,
		"SELECT `id`,`task_id`,`kind`,`at`,`actor_kind`,`actor_id`,`reason`,`detail`,"+
			"`actor_source`,`behalf_kind`,`behalf_id`,`behalf_source` "+
			"FROM `task_event` "+where+" ORDER BY `at`, `id`", arg,
		func(rows *sql.Rows) error {
			var e TaskEvent
			var kind, actorKind, actorSource, behalfSource string
			var behalfKind, behalfID sql.NullString
			if err := rows.Scan(&e.ID, &e.TaskID, &kind, &e.At, &actorKind, &e.By.ID,
				&e.Reason, &e.Detail, &actorSource, &behalfKind, &behalfID, &behalfSource); err != nil {
				return err
			}
			e.Kind, e.By.Kind, e.By.Source = TaskEventKind(kind), PrincipalKind(actorKind), AuthSource(actorSource)
			e.OnBehalfOf = principalFrom(behalfKind, behalfID, behalfSource)
			out = append(out, e)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("reading task events: %w", err)
	}
	return out, nil
}

// RunSpan is one attempt's start and end, for a reader that wants every
// attempt of every task without the rest of a Run.
type RunSpan struct {
	ID         string
	TaskID     string
	Attempt    int
	StartedAt  time.Time
	FinishedAt *time.Time
	Outcome    string
}

// RunSpansSince is every attempt that started, finished or is still live
// at or after since, oldest start first.
func (s *Store) RunSpansSince(ctx context.Context, since time.Time) ([]RunSpan, error) {
	var out []RunSpan
	at := since.UTC()
	err := each(ctx, s.db,
		"SELECT `id`,`task_id`,`attempt`,`started_at`,`finished_at`,`outcome` FROM `task_run` "+
			"WHERE `started_at` >= ? OR `finished_at` IS NULL OR `finished_at` >= ? "+
			"ORDER BY `started_at`, `id`", []any{at, at},
		func(rows *sql.Rows) error {
			var r RunSpan
			var finished sql.NullTime
			var outcome sql.NullString
			if err := rows.Scan(&r.ID, &r.TaskID, &r.Attempt, &r.StartedAt, &finished, &outcome); err != nil {
				return err
			}
			r.FinishedAt, r.Outcome = timePtr(finished), outcome.String
			out = append(out, r)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("reading run spans: %w", err)
	}
	return out, nil
}

// MergeTaskEvents puts a task's recorded closes and reopens into the
// timeline Transitions built, each as a step of its own carrying who
// asked and why.
//
// Transitions places one closed step at Observation.ClosedAt, which is
// the current closure only. A recorded close at that same instant is the
// same close (to within the second either store keeps), so it replaces
// that step rather than standing beside it;
// a ClosedAt nothing recorded (one from before task_event existed, or
// from before grain recorded its own closes there) keeps its
// unattributed step. An inferred
// requeue (Transitions places one at the finish of a run when a later run
// or the current open state proves the task returned to the queue) that
// falls while the task was closed is the cancelled run finishing inside
// that closure: the reopen that ended the closure is the moment the task
// actually came back, so the inferred step is dropped in its favour.
func MergeTaskEvents(ts []Transition, events []TaskEvent) []Transition {
	if len(events) == 0 {
		return ts
	}
	recorded := func(at time.Time) bool {
		for _, e := range events {
			if d := e.At.Sub(at); e.Kind == TaskEventClosed && d < time.Second && d > -time.Second {
				return true
			}
		}
		return false
	}
	whileClosed := func(at time.Time) bool {
		var closedAt *time.Time
		for i := range events {
			switch events[i].Kind {
			case TaskEventClosed:
				if closedAt == nil {
					closedAt = &events[i].At
				}
			case TaskEventReopened:
				if closedAt != nil && !at.Before(closedAt.Add(-time.Second)) && !at.After(events[i].At) {
					return true
				}
				closedAt = nil
			}
		}
		return closedAt != nil && !at.Before(closedAt.Add(-time.Second))
	}
	out := make([]Transition, 0, len(ts)+len(events))
	for _, t := range ts {
		if t.State == StateClosed && recorded(t.At) {
			continue
		}
		if t.State == StateQueued && t.Inferred && whileClosed(t.At) {
			continue
		}
		out = append(out, t)
	}
	for _, e := range events {
		var state State
		switch e.Kind {
		case TaskEventClosed:
			state = StateClosed
		case TaskEventReopened:
			state = TransitionReopened
		case TaskEventAutoMergeOn:
			state = TransitionAutoMergeOn
		case TaskEventAutoMergeOff:
			state = TransitionAutoMergeOff
		case TaskEventAutoMergeKeptOff:
			state = TransitionAutoMergeKeptOff
		case TaskEventOwnerChanged:
			state = TransitionOwnerChanged
		case TaskEventAdmissionChanged:
			state = TransitionAdmissionChanged
		case TaskEventHostingChanged:
			state = TransitionHostingChanged
		case TaskEventHumanReviewRequested:
			state = TransitionHumanReviewRequested
		case TaskEventHumanReviewReleased:
			state = TransitionHumanReviewReleased
		case TaskEventGateOverridden:
			state = TransitionGateOverridden
		default:
			// A note about an action, not a step (TaskEventSubmitted, ...).
			continue
		}
		by := e.By
		out = append(out, Transition{State: state, At: e.At, By: &by, OnBehalfOf: e.OnBehalfOf, Reason: e.Reason, Detail: e.Detail})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// behalfColumn is one of task_event's behalf_* columns, "" for none: the
// columns are NOT NULL DEFAULT ” there, unlike task's nullable pairs.
func behalfColumn(p *Principal, col func(Principal) string) string {
	if p == nil {
		return ""
	}
	return col(*p)
}
