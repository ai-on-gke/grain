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
	"strings"
	"time"
)

// A schedule set to OverlapSkip holds its next occurrence while the task
// its previous firing filed is open (fireTaskSchedule). That is right for
// a task that is running, or waiting on a person: a second copy would
// race the first. It is wrong for a task that has never been dispatched
// at all. On 2026-11-10 release-train tick #3946 sat queued at the
// default priority behind about sixty other tasks for fourteen hours,
// and every 20-minute firing after it was skipped because #3946 was
// still open: one slow dispatch stalled the release train for the day,
// and nothing said so.
//
// What grain does about it (grain/task-4086): the starved firing is
// kept and raised rather than superseded. Filing a fresh task in its
// place would put the new one at the back of the same queue, behind
// everything the old one had already waited out, and would close a task
// a person may already be looking at; keeping it keeps its place. Once
// the held occurrence is starved -- ScheduleStarvedAfter since it came
// due, or ScheduleStarvedOccurrences occurrences held behind it -- and
// the blocker is still queued with no run ever started, the blocker's
// priority is raised to the highest priority any other queued task has
// (it is already the older of any task it ties with, so the queue's FIFO
// order puts it first at that priority), the task is told why, and a
// FamilyScheduleStarved notification is raised in the Inbox until it
// closes.
const (
	// ScheduleStarvedAfter is how long a held occurrence may wait on a
	// never-started task before the schedule counts as starved.
	ScheduleStarvedAfter = time.Hour
	// ScheduleStarvedOccurrences is how many occurrences (the held one
	// and those folded behind it) may come due on a never-started task
	// before the schedule counts as starved, whichever of this and
	// ScheduleStarvedAfter comes first: three ticks of a 20-minute train
	// is the hour, and a daily schedule is starved by the hour.
	ScheduleStarvedOccurrences = 3
)

// StarvedScheduleFiring is what EscalateStarvedScheduleBlocker did.
type StarvedScheduleFiring struct {
	// TaskID is the never-started task holding the schedule, empty when
	// the schedule is not starved.
	TaskID string
	// HeldFor and Occurrences are how long the held occurrence has
	// waited and how many occurrences have come due behind it, itself
	// included.
	HeldFor     time.Duration
	Occurrences int
	// From and To are the task's priority before and after; equal when
	// nothing queued outranked it.
	From, To int
	// Notified is a first raise of the schedule's FamilyScheduleStarved
	// notification.
	Notified bool
	// HeldBy is the other schedule in the overlap group whose firing
	// filed the task (ScheduleSkip.HeldByScheduleID), empty when the task
	// is the schedule's own previous firing; Group is that group.
	HeldBy, Group string
}

// blocker says whose firing the starved task came from, after "#<id> from".
func (f StarvedScheduleFiring) blocker() string {
	if f.HeldBy == "" {
		return "its previous firing"
	}
	if f.Group == "" {
		return fmt.Sprintf("schedule %s's firing", f.HeldBy)
	}
	return fmt.Sprintf("schedule %s's firing in overlap group %q", f.HeldBy, f.Group)
}

// Raised reports whether the task's priority was raised.
func (f StarvedScheduleFiring) Raised() bool { return f.To > f.From }

// ScheduleStarved reports whether a held occurrence that came due at
// occurrence, with occurrences due behind it (itself included), has
// waited long enough to count as starved by now.
func ScheduleStarved(occurrence, now time.Time, occurrences int) bool {
	return now.Sub(occurrence) >= ScheduleStarvedAfter || occurrences >= ScheduleStarvedOccurrences
}

// EscalateStarvedScheduleBlocker is the starved-firing rule above, for
// the skip k this cycle's firing of sched recorded. It does nothing --
// and returns the zero StarvedScheduleFiring -- unless k is a held
// occurrence blocked by an open task that is still queued, has never had
// a run, and has been holding the schedule past ScheduleStarvedAfter or
// ScheduleStarvedOccurrences.
//
// The open task may be a task schedule's previous firing
// (SuppressedByOpenTask) or a workflow schedule's review
// (SuppressedByOpenReview). A review that has never started is not one a
// person is reading -- it never got a worker -- so workflowReviewHold
// would otherwise hold the schedule for days in silence (grain/task-5445:
// a daily workflow's review sat queued for 30h at priority 5). A review
// that has run, or finished into awaiting_reply for somebody to read, is
// left alone by the queued and never-run checks below.
//
// Every cycle may call it: the priority only ever rises, the comment is
// written only when it does, and the notification is raised only while
// none is open for the schedule.
func (s *Store) EscalateStarvedScheduleBlocker(ctx context.Context, sched Schedule, k ScheduleSkip,
	now time.Time, loc *time.Location) (StarvedScheduleFiring, error) {

	if k.Outcome != ScheduleSkipHeld || k.TaskID == "" {
		return StarvedScheduleFiring{}, nil
	}
	if k.Reason != SuppressedByOpenTask && k.Reason != SuppressedByOpenReview {
		return StarvedScheduleFiring{}, nil
	}
	occurrences := 1 + foldedOccurrences(sched, now, loc)
	if !ScheduleStarved(k.Occurrence, now, occurrences) {
		return StarvedScheduleFiring{}, nil
	}
	st, err := s.State(ctx, k.TaskID)
	if err != nil {
		return StarvedScheduleFiring{}, fmt.Errorf("reading the state of %s: %w", k.TaskID, err)
	}
	if st != StateQueued {
		return StarvedScheduleFiring{}, nil
	}
	runs, err := s.Runs(ctx, k.TaskID)
	if err != nil {
		return StarvedScheduleFiring{}, fmt.Errorf("reading the runs of %s: %w", k.TaskID, err)
	}
	if len(runs) > 0 {
		// Started and back in the queue (a retry, a resume): its own
		// backoff is a different wait, and not one a priority answers.
		return StarvedScheduleFiring{}, nil
	}
	top, err := s.topQueuedPriority(ctx, k.TaskID)
	if err != nil {
		return StarvedScheduleFiring{}, err
	}
	out := StarvedScheduleFiring{TaskID: k.TaskID, HeldFor: now.Sub(k.Occurrence), Occurrences: occurrences}
	if k.HeldByScheduleID != "" && k.HeldByScheduleID != sched.ID {
		out.HeldBy, out.Group = k.HeldByScheduleID, strings.TrimSpace(sched.OverlapGroup)
	}
	if err := s.UpdateTask(ctx, k.TaskID, func(t *Task) error {
		out.From, out.To = t.Priority, t.Priority
		if top > t.Priority {
			t.Priority, out.To = top, top
		}
		return nil
	}); err != nil {
		return StarvedScheduleFiring{}, fmt.Errorf("raising the priority of %s: %w", k.TaskID, err)
	}
	if out.Raised() {
		if _, err := s.AddComment(ctx, Comment{TaskID: k.TaskID, Author: Attribution{Actor: SchedulePrincipal},
			Body: StarvedScheduleComment(sched, out), CreatedAt: now}); err != nil {
			return out, fmt.Errorf("noting the raised priority on %s: %w", k.TaskID, err)
		}
	}
	subject := NotificationSubject(FamilyScheduleStarved, sched.ID)
	open, err := s.OpenNotification(ctx, subject)
	if err != nil {
		return out, err
	}
	if open == nil {
		if _, err := s.RaiseNotification(ctx, Notification{
			Subject:    subject,
			Severity:   NotificationWarning,
			Title:      StarvedScheduleTitle(sched, out),
			Body:       StarvedScheduleComment(sched, out),
			TaskID:     k.TaskID,
			RaisedBy:   PrincipalString(SchedulePrincipal),
			RaisedAt:   now,
			ClearsWhen: &NotificationClear{Kind: ClearsOnTask, Task: k.TaskID, Until: "closed"},
		}); err != nil {
			return out, err
		}
		out.Notified = true
	}
	return out, nil
}

// topQueuedPriority is the highest priority of any queued task but
// except, and 0 when there is none.
func (s *Store) topQueuedPriority(ctx context.Context, except string) (int, error) {
	var top sql.NullInt64
	if err := s.db.QueryRowContext(ctx,
		"SELECT MAX(`t`.`priority`) FROM `task` AS `t` "+
			"JOIN `task_state` AS `st` ON `st`.`task_id` = `t`.`id` "+
			"WHERE `st`.`state` = ? AND `t`.`id` <> ?",
		string(StateQueued), except).Scan(&top); err != nil {
		return 0, fmt.Errorf("reading the highest queued priority: %w", err)
	}
	return int(top.Int64), nil
}

func scheduleName(sched Schedule) string {
	if sched.Title != "" {
		return sched.Title
	}
	return sched.ID
}

// StarvedScheduleTitle is the FamilyScheduleStarved notification's title.
func StarvedScheduleTitle(sched Schedule, f StarvedScheduleFiring) string {
	return fmt.Sprintf("Schedule %q has not fired for %s: #%s from %s has never started",
		scheduleName(sched), f.HeldFor.Round(time.Minute), f.TaskID, f.blocker())
}

// StarvedScheduleComment is what EscalateStarvedScheduleBlocker writes on
// the starved task, and the notification's body.
func StarvedScheduleComment(sched Schedule, f StarvedScheduleFiring) string {
	raised := fmt.Sprintf("Its priority is already the highest queued (%d), so it was left as it is.", f.To)
	if f.Raised() {
		raised = fmt.Sprintf("Grain raised its priority from %d to %d, the highest any other queued task has, "+
			"so it is dispatched next at that priority.", f.From, f.To)
	}
	return fmt.Sprintf("Schedule %q (%s) is set to overlap: skip, and this task, from %s, "+
		"has been queued without ever starting while %d of its occurrences came due (%s since the first). "+
		"Every one of them is held until this task closes. %s\n\n"+
		"A schedule whose task cannot get a worker stops for as long as it waits, so this task is kept "+
		"and raised rather than replaced by a new firing at the back of the queue. If it should not run "+
		"at all, close it and the schedule fires again on its next cycle.",
		scheduleName(sched), sched.ID, f.blocker(), f.Occurrences, f.HeldFor.Round(time.Minute), raised)
}
