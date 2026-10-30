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
// session's own grant). grain's own closes -- a goal met, a task made
// obsolete, a review finished -- still only stamp ClosedAt, and read as
// an unattributed close (MergeTaskEvents).

// TaskEventKind is what a TaskEvent records.
type TaskEventKind string

const (
	TaskEventClosed   TaskEventKind = "closed"
	TaskEventReopened TaskEventKind = "reopened"
)

// TransitionReopened is the timeline's name for a reopen. It is not one
// of StateOf's answers -- a reopened task is in whatever state its record
// implies -- but the moment it came back is worth a step of its own, the
// same way the close it undid is.
const TransitionReopened State = "reopened"

// TaskEvent is one close or reopen of a task.
type TaskEvent struct {
	ID     int64
	TaskID string
	Kind   TaskEventKind
	At     time.Time
	// By is who asked: a person, a session or API grant acting as its
	// task's agent, an external agent token, or the deployment's own
	// actor for a caller nothing identified.
	By Principal
	// Reason is what they said, "" for nothing.
	Reason string
	// Detail is what grain did because of it: for a close, the live run
	// and the CI runs it cancelled.
	Detail string
}

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
		res, err := tx.ExecContext(ctx,
			"INSERT INTO `task_event` (`task_id`,`kind`,`at`,`actor_kind`,`actor_id`,`reason`,`detail`) "+
				"VALUES (?,?,?,?,?,?,?)",
			e.TaskID, string(e.Kind), e.At.UTC(), string(e.By.Kind), e.By.ID, e.Reason, e.Detail)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	return id, err
}

// TaskEvents is one task's events, oldest first.
func (s *Store) TaskEvents(ctx context.Context, taskID string) ([]TaskEvent, error) {
	return s.taskEvents(ctx, "WHERE `task_id` = ?", taskID)
}

// TaskEventsSince is every task's events at or after since, oldest first.
func (s *Store) TaskEventsSince(ctx context.Context, since time.Time) ([]TaskEvent, error) {
	return s.taskEvents(ctx, "WHERE `at` >= ?", since.UTC())
}

func (s *Store) taskEvents(ctx context.Context, where string, arg any) ([]TaskEvent, error) {
	var out []TaskEvent
	err := each(ctx, s.db,
		"SELECT `id`,`task_id`,`kind`,`at`,`actor_kind`,`actor_id`,`reason`,`detail` "+
			"FROM `task_event` "+where+" ORDER BY `at`, `id`", arg,
		func(rows *sql.Rows) error {
			var e TaskEvent
			var kind, actorKind string
			if err := rows.Scan(&e.ID, &e.TaskID, &kind, &e.At, &actorKind, &e.By.ID,
				&e.Reason, &e.Detail); err != nil {
				return err
			}
			e.Kind, e.By.Kind = TaskEventKind(kind), PrincipalKind(actorKind)
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
// a ClosedAt nothing recorded (one of grain's own closes, or one from
// before task_event existed) keeps its unattributed step.
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
	out := make([]Transition, 0, len(ts)+len(events))
	for _, t := range ts {
		if t.State == StateClosed && recorded(t.At) {
			continue
		}
		out = append(out, t)
	}
	for _, e := range events {
		state := StateClosed
		if e.Kind == TaskEventReopened {
			state = TransitionReopened
		}
		by := e.By
		out = append(out, Transition{State: state, At: e.At, By: &by, Reason: e.Reason, Detail: e.Detail})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}
