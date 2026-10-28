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

// The reasons a due firing can be skipped that ScheduleSuppression does
// not already name, because none of them is a wait SuppressedFiring
// reports: a paused schedule says so on its own row, a firing into a
// repository off the target list moves the cadence on, and an archived
// team's schedules are not fired at all. Each is still a firing that came
// due and filed nothing, which is what a ScheduleSkip records.
const (
	// SkippedPaused is a schedule that came due while paused (Enabled
	// false). Its NextRunAt stays where it was, so resuming it fires the
	// held occurrence once (recordFiring's own loop).
	SkippedPaused ScheduleSuppressionReason = "paused"
	// SkippedOffTargetRepos is a firing into a repository this
	// deployment may no longer dispatch work into
	// (suppressOffTargetRepos). The occurrence is dropped.
	SkippedOffTargetRepos ScheduleSuppressionReason = "offTargetRepos"
	// SkippedTeamArchived is a schedule whose team is archived.
	SkippedTeamArchived ScheduleSuppressionReason = "teamArchived"
	// SkippedFiringFailed is a firing that failed (Store.FireSchedule
	// returned an error), Detail saying how. Its NextRunAt stays where it
	// was, so every cycle tries again, and the first that succeeds fires
	// the held occurrence once (Store.RecordScheduleFiringFailure).
	SkippedFiringFailed ScheduleSuppressionReason = "firingFailed"
	// SkippedQuiet is a firing the daemon decided was not worth a
	// sandbox because there was nothing for the job to look at
	// (Store.DropQuietFiring; today only the pipeline steward's,
	// orchestrator.pipelineStewardQuiet). The occurrence is dropped and
	// the cadence moves on; Detail says what was checked.
	SkippedQuiet ScheduleSuppressionReason = "quiet"
)

// ScheduleSkipOutcome is what became of a skipped occurrence.
type ScheduleSkipOutcome string

const (
	// ScheduleSkipHeld is an occurrence still owed: the schedule's
	// NextRunAt has not moved past it, so the poll fires it as soon as
	// whatever skipped it stops doing so. The zero value.
	ScheduleSkipHeld ScheduleSkipOutcome = ""
	// ScheduleSkipFired is a held occurrence that fired late, once the
	// blocker cleared (ScheduleSkip.FiredTaskID, ResolvedAt).
	ScheduleSkipFired ScheduleSkipOutcome = "fired"
	// ScheduleSkipDropped is an occurrence that will never fire: the
	// cadence moved on past it without filing anything
	// (suppressOffTargetRepos, suppressStandingWorkOff).
	ScheduleSkipDropped ScheduleSkipOutcome = "dropped"
)

// ScheduleSkipsKept is how many skips a schedule's history keeps. One row
// is one occurrence however many poll cycles re-skipped it, so this is
// that many skipped occurrences rather than that many cycles.
const ScheduleSkipsKept = 20

// ScheduleSkip is one occurrence of a schedule that came due and filed
// nothing (grain/task-3560).
//
// Before this, the only trace of a skipped firing was SuppressedFiring:
// derived at read time and gone the moment the blocker closed, so an
// operator reading the schedules page the morning after saw nothing at all
// -- a firing that was held for nine minutes and one that never happened
// look the same once both are over. This is the record that stays.
//
// One row per occurrence (ScheduleID, Occurrence), not per poll: an
// overlap skip leaves NextRunAt where it was, so every cycle re-skips the
// same occurrence, and they collapse into this one row. So do occurrences
// that come round while one is held, since NextRunAt does not move until
// the held one fires: Folded counts them when it does.
type ScheduleSkip struct {
	ID         int64
	ScheduleID string
	// Occurrence is the NextRunAt the skipped firing was for.
	Occurrence time.Time
	// At is when the skip was first seen.
	At time.Time
	// Reason is why nothing was filed.
	Reason ScheduleSuppressionReason
	// TaskID and TaskState are the open task holding the schedule, for
	// the reasons that have one (SuppressedByOpenTask,
	// SuppressedByOpenReview), as of the latest cycle that re-skipped it.
	TaskID    string
	TaskState State
	// Detail is the sentence for a reason with no task to name.
	Detail  string
	Outcome ScheduleSkipOutcome
	// ResolvedAt is when the occurrence fired late or was dropped.
	ResolvedAt *time.Time
	// FiredTaskID is the task the late firing filed, empty for a
	// schedule whose firing is not a task.
	FiredTaskID string
	// Folded is how many further occurrences came due while this one was
	// held and were absorbed into its one late firing.
	Folded int
	// NotedTaskID is the blocker this occurrence has already said so on
	// (Store.NoteHeldScheduleOnBlocker): the one comment a person waiting
	// on that task gets, rather than one a poll cycle.
	NotedTaskID string
}

// Summary is the skip as one line: "skipped at <time>: <why>", the
// wording the reconciler logs and the schedules page shows.
func (k ScheduleSkip) Summary() string {
	return "skipped at " + k.At.UTC().Format("2006-01-02 15:04 MST") + ": " + k.Why()
}

// Why is the reason half of Summary.
func (k ScheduleSkip) Why() string {
	switch k.Reason {
	case SuppressedByOpenTask:
		if k.TaskID != "" {
			return fmt.Sprintf("#%s from the previous firing is still open (%s)", k.TaskID, k.taskState())
		}
		return "the task its previous firing filed is still open"
	case SuppressedByOpenReview:
		if k.TaskID != "" {
			return fmt.Sprintf("review #%s of the previous firing is still open (%s)", k.TaskID, k.taskState())
		}
		return "the review of the previous firing is still open"
	case SuppressedBySuiteRun:
		return "the suite run from the previous firing has not finished"
	case SuppressedByWorkflowRun:
		return "the CI run from the previous firing has not finished"
	case SkippedPaused:
		return "the schedule is paused"
	case SkippedTeamArchived:
		return "the schedule's team is archived"
	case SkippedFiringFailed:
		if k.Detail != "" {
			return "firing failed: " + k.Detail
		}
		return "firing failed"
	}
	if k.Detail != "" {
		return k.Detail
	}
	return "the previous firing has not finished"
}

func (k ScheduleSkip) taskState() string {
	if k.TaskState == "" {
		return "state unknown"
	}
	return string(k.TaskState)
}

// WaitsOnPerson reports whether k is held by a task that is itself
// waiting on a person -- for a Submit, a reply or an acknowledgement --
// which is the one kind of skip somebody can clear by doing something on
// a task, and so the one Store.NoteHeldScheduleOnBlocker says so on.
func (k ScheduleSkip) WaitsOnPerson() bool {
	if k.Outcome != ScheduleSkipHeld || k.TaskID == "" {
		return false
	}
	if k.Reason != SuppressedByOpenTask && k.Reason != SuppressedByOpenReview {
		return false
	}
	switch k.TaskState {
	case StateAwaitingSubmit, StateAwaitingReply:
		return true
	}
	return false
}

// NoteHeldScheduleOnBlocker writes on the task holding k that sched's
// firing is held on it, and reports whether it did (grain/task-3748).
//
// The inbox already lists a task waiting on a person; what it did not
// say is that a schedule is waiting on that task too. On 10-13 the
// release cut sat held behind #3201 for a day while #3201 waited on a
// Submit, and nothing on #3201 said so. This is that sentence, on the
// task, where whoever has to act on it will read it.
//
// Once per occurrence and blocker: the claim on noted_task_id is what
// stops every poll cycle that re-skips the occurrence from writing it
// again, and a held occurrence that meets a second blocker notes that one
// too. It is claimed before the comment is written, and given back if the
// write fails, so a failure is tried again next cycle rather than lost.
// A comment written by the store never un-parks a task (noteAbandonedReview).
func (s *Store) NoteHeldScheduleOnBlocker(ctx context.Context, sched Schedule, k ScheduleSkip,
	now time.Time) (bool, error) {

	if !k.WaitsOnPerson() {
		return false, nil
	}
	var claimed bool
	err := s.write(ctx, "claim the note on "+k.TaskID+" for schedule "+k.ScheduleID, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			"UPDATE `scheduled_firing_skip` SET `noted_task_id` = ? "+
				"WHERE `schedule_id` = ? AND `occurrence` = ? AND `outcome` = ? AND `noted_task_id` <> ?",
			k.TaskID, k.ScheduleID, k.Occurrence.UTC(), string(ScheduleSkipHeld), k.TaskID)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		claimed = n > 0
		return err
	})
	if err != nil || !claimed {
		return false, err
	}
	if _, err := s.AddComment(ctx, Comment{
		TaskID:    k.TaskID,
		Author:    Attribution{Actor: SchedulePrincipal},
		Body:      HeldScheduleComment(sched, k),
		CreatedAt: now,
	}); err != nil {
		_ = s.write(ctx, "release the note on "+k.TaskID+" for schedule "+k.ScheduleID, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx,
				"UPDATE `scheduled_firing_skip` SET `noted_task_id` = '' WHERE `schedule_id` = ? AND `occurrence` = ?",
				k.ScheduleID, k.Occurrence.UTC())
			return err
		})
		return false, fmt.Errorf("noting on %s that schedule %s is held on it: %w", k.TaskID, k.ScheduleID, err)
	}
	return true, nil
}

// HeldScheduleComment is what NoteHeldScheduleOnBlocker writes on the
// task holding a schedule's firing.
func HeldScheduleComment(sched Schedule, k ScheduleSkip) string {
	name := sched.Title
	if name == "" {
		name = sched.ID
	}
	var waiting string
	switch k.TaskState {
	case StateAwaitingSubmit:
		waiting = "a Submit"
	case StateAwaitingReply:
		waiting = "a reply"
	default:
		waiting = "a person"
	}
	return fmt.Sprintf("Schedule %q (%s) is held on this task: its %s firing came due while this task "+
		"was still open, and it will not fire until this task closes. This task is waiting on %s.\n\n"+
		"Once it closes, the held firing goes ahead once, standing in for every occurrence that came "+
		"due in the meantime. The schedules page lists this skip with the rest of the schedule's "+
		"skipped firings.",
		name, sched.ID, k.Occurrence.UTC().Format("2006-01-02 15:04 MST"), waiting)
}

// RecordScheduleSkip records k, and reports whether it is new: false when
// this occurrence of the schedule was already recorded, which is every
// poll cycle after the first that re-skips a held occurrence. A repeat
// refreshes the blocker (a held occurrence can outlast one blocker and
// meet another) and can turn a held occurrence into a dropped one, but
// never reopens a resolved one.
func (s *Store) RecordScheduleSkip(ctx context.Context, k ScheduleSkip) (bool, error) {
	var created bool
	err := s.write(ctx, "record a skipped firing of schedule "+k.ScheduleID, func(tx *sql.Tx) error {
		created = false
		var id int64
		var outcome string
		err := tx.QueryRowContext(ctx,
			"SELECT `id`,`outcome` FROM `scheduled_firing_skip` WHERE `schedule_id` = ? AND `occurrence` = ?",
			k.ScheduleID, k.Occurrence.UTC()).Scan(&id, &outcome)
		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return err
		default:
			if ScheduleSkipOutcome(outcome) != ScheduleSkipHeld {
				return nil
			}
			_, err := tx.ExecContext(ctx,
				"UPDATE `scheduled_firing_skip` SET `reason` = ?, `task_id` = ?, `task_state` = ?, `detail` = ?, "+
					"`outcome` = ?, `resolved_at` = ? WHERE `id` = ?",
				string(k.Reason), k.TaskID, string(k.TaskState), k.Detail,
				string(k.Outcome), timeOf(k.ResolvedAt), id)
			return err
		}
		created = true
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO `scheduled_firing_skip` (`schedule_id`,`occurrence`,`at`,`reason`,`task_id`,`task_state`,"+
				"`detail`,`outcome`,`resolved_at`,`fired_task_id`,`folded`,`noted_task_id`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)",
			k.ScheduleID, k.Occurrence.UTC(), k.At.UTC(), string(k.Reason), k.TaskID, string(k.TaskState),
			k.Detail, string(k.Outcome), timeOf(k.ResolvedAt), k.FiredTaskID, k.Folded, k.NotedTaskID); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx,
			"DELETE FROM `scheduled_firing_skip` WHERE `schedule_id` = ? AND `id` NOT IN "+
				"(SELECT `id` FROM `scheduled_firing_skip` WHERE `schedule_id` = ? ORDER BY `id` DESC LIMIT ?)",
			k.ScheduleID, k.ScheduleID, ScheduleSkipsKept)
		return err
	})
	if err != nil {
		return false, fmt.Errorf("recording a skipped firing of schedule %s: %w", k.ScheduleID, err)
	}
	return created, nil
}

// scheduleFailureDetailMax bounds the error RecordScheduleFiringFailure
// keeps: enough for any store or template error, short enough for a row
// on the schedules page.
const scheduleFailureDetailMax = 500

// RecordScheduleFiringFailure records that sched's due firing failed with
// fireErr (SkippedFiringFailed), and reports whether that is news: true
// the first time this occurrence fails and whenever it fails differently,
// false for every cycle after that fails the same way. That is what the
// reconciler logs on (grain/task-4537): a schedule whose firing failed on
// every ten-second cycle for two days left nothing but one log line per
// cycle, and no word on the schedule itself, while the schedules page
// showed it as simply overdue.
//
// The row is the held skip every other skip is, so it is the schedule's
// latest skip on its row and in its pane, and the cycle that does fire
// it settles it as fired late, folding the occurrences that came due in
// the meantime (resolveHeldScheduleSkips): one firing, however long the
// failure lasted. A row this occurrence already had for another reason
// is taken over while it is held, and one already settled is left alone.
func (s *Store) RecordScheduleFiringFailure(ctx context.Context, sched Schedule, now time.Time,
	fireErr error) (bool, error) {

	detail := fireErr.Error()
	if len(detail) > scheduleFailureDetailMax {
		detail = detail[:scheduleFailureDetailMax] + "..."
	}
	var reason, prior, outcome string
	err := s.db.QueryRowContext(ctx,
		"SELECT `reason`,`detail`,`outcome` FROM `scheduled_firing_skip` WHERE `schedule_id` = ? AND `occurrence` = ?",
		sched.ID, sched.NextRunAt.UTC()).Scan(&reason, &prior, &outcome)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return false, fmt.Errorf("reading the skipped firing of schedule %s: %w", sched.ID, err)
	case ScheduleSkipOutcome(outcome) != ScheduleSkipHeld:
		return false, nil
	case ScheduleSuppressionReason(reason) == SkippedFiringFailed && prior == detail:
		return false, nil
	}
	if _, err := s.RecordScheduleSkip(ctx, ScheduleSkip{ScheduleID: sched.ID, Occurrence: sched.NextRunAt,
		At: now, Reason: SkippedFiringFailed, Detail: detail}); err != nil {
		return false, err
	}
	return true, nil
}

// resolveHeldScheduleSkips marks every occurrence of scheduleID still held
// as fired at now -- what a cadence firing does, since the one firing it
// makes is the late firing of whatever it was holding (recordFiring).
func (s *Store) resolveHeldScheduleSkips(ctx context.Context, scheduleID string, now time.Time,
	firedTaskID string, folded int) error {

	return s.write(ctx, "resolve held firings of schedule "+scheduleID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"UPDATE `scheduled_firing_skip` SET `outcome` = ?, `resolved_at` = ?, `fired_task_id` = ?, `folded` = ? "+
				"WHERE `schedule_id` = ? AND `outcome` = ?",
			string(ScheduleSkipFired), now.UTC(), firedTaskID, folded, scheduleID, string(ScheduleSkipHeld))
		return err
	})
}

// DropStrandedScheduleSkips marks every occurrence of scheduleID still
// held from before next as dropped at now. A held occurrence is owed
// because NextRunAt has not moved past it; an edit that retimes the
// cadence (ui.UpdateSchedule, a declaring repository's sync) moves
// NextRunAt past it anyway, and then it never fires. Left held, the pane
// went on saying it "fires once as soon as that clears", and the next
// on-time firing settled it as "fired late" (resolveHeldScheduleSkips).
func (s *Store) DropStrandedScheduleSkips(ctx context.Context, scheduleID string, next, now time.Time) error {
	return s.write(ctx, "drop stranded held firings of schedule "+scheduleID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"UPDATE `scheduled_firing_skip` SET `outcome` = ?, `resolved_at` = ? "+
				"WHERE `schedule_id` = ? AND `outcome` = ? AND `occurrence` < ?",
			string(ScheduleSkipDropped), now.UTC(), scheduleID, string(ScheduleSkipHeld), next.UTC())
		return err
	})
}

const scheduleSkipColumns = "`id`,`schedule_id`,`occurrence`,`at`,`reason`,`task_id`,`task_state`," +
	"`detail`,`outcome`,`resolved_at`,`fired_task_id`,`folded`,`noted_task_id`"

func scanScheduleSkip(rows *sql.Rows) (ScheduleSkip, error) {
	var k ScheduleSkip
	var reason, state, outcome string
	var resolved sql.NullTime
	if err := rows.Scan(&k.ID, &k.ScheduleID, &k.Occurrence, &k.At, &reason, &k.TaskID, &state,
		&k.Detail, &outcome, &resolved, &k.FiredTaskID, &k.Folded, &k.NotedTaskID); err != nil {
		return ScheduleSkip{}, err
	}
	k.Occurrence, k.At = k.Occurrence.UTC(), k.At.UTC()
	k.Reason, k.TaskState, k.Outcome = ScheduleSuppressionReason(reason), State(state), ScheduleSkipOutcome(outcome)
	if resolved.Valid {
		t := resolved.Time.UTC()
		k.ResolvedAt = &t
	}
	return k, nil
}

// ScheduleSkips is scheduleID's skip history, newest first -- at most
// ScheduleSkipsKept rows.
func (s *Store) ScheduleSkips(ctx context.Context, scheduleID string) ([]ScheduleSkip, error) {
	var out []ScheduleSkip
	err := each(ctx, s.db,
		"SELECT "+scheduleSkipColumns+" FROM `scheduled_firing_skip` WHERE `schedule_id` = ? ORDER BY `id` DESC",
		scheduleID, func(rows *sql.Rows) error {
			k, err := scanScheduleSkip(rows)
			if err != nil {
				return err
			}
			out = append(out, k)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("reading the skipped firings of schedule %s: %w", scheduleID, err)
	}
	return out, nil
}

// AllScheduleSkips is every schedule's kept skip history, newest first,
// keyed by schedule id -- at most ScheduleSkipsKept rows a schedule, so
// the schedules list reads it whole rather than once per row.
func (s *Store) AllScheduleSkips(ctx context.Context) (map[string][]ScheduleSkip, error) {
	out := map[string][]ScheduleSkip{}
	err := each(ctx, s.db,
		"SELECT "+scheduleSkipColumns+" FROM `scheduled_firing_skip` ORDER BY `id` DESC", nil,
		func(rows *sql.Rows) error {
			k, err := scanScheduleSkip(rows)
			if err != nil {
				return err
			}
			out[k.ScheduleID] = append(out[k.ScheduleID], k)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("reading skipped firings: %w", err)
	}
	return out, nil
}

// PausedDueSchedules is every paused schedule whose next run has gone by
// -- DueSchedules' question for the schedules it leaves out, so the
// reconciler can record their skipped occurrence (SkippedPaused) rather
// than say nothing.
func (s *Store) PausedDueSchedules(ctx context.Context, now time.Time) ([]Schedule, error) {
	var out []Schedule
	err := each(ctx, s.db,
		"SELECT "+scheduleColumns+" FROM `schedule` WHERE `enabled` = 0 AND `next_run_at` <= ? ORDER BY `id`",
		now.UTC(), func(rows *sql.Rows) error {
			t, err := scanSchedule(rows.Scan)
			if err != nil {
				return err
			}
			out = append(out, t)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("listing paused due schedules: %w", err)
	}
	return out, nil
}

// overlapSkip is the ScheduleSkip for a firing held by an open task --
// the task and its state, read now, since naming them is what makes the
// record actionable.
func (s *Store) overlapSkip(ctx context.Context, sched Schedule, now time.Time,
	reason ScheduleSuppressionReason, taskID string) *ScheduleSkip {

	k := &ScheduleSkip{ScheduleID: sched.ID, Occurrence: sched.NextRunAt, At: now, Reason: reason, TaskID: taskID}
	if taskID != "" {
		if st, err := s.State(ctx, taskID); err == nil {
			k.TaskState = st
		}
	}
	return k
}

// droppedSkip is the ScheduleSkip for an occurrence the cadence moved on
// past without firing.
func droppedSkip(sched Schedule, now time.Time, reason ScheduleSuppressionReason, detail string) *ScheduleSkip {
	return &ScheduleSkip{
		ScheduleID: sched.ID, Occurrence: sched.NextRunAt, At: now, Reason: reason, Detail: detail,
		Outcome: ScheduleSkipDropped, ResolvedAt: &now,
	}
}
