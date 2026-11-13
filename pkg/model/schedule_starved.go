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

// StarvedTask is one never-started task EscalateStarvedScheduleBlocker
// raised (or found already at the top), with its priority before and
// after.
type StarvedTask struct {
	ID       string
	From, To int
}

// StarvedScheduleFiring is what EscalateStarvedScheduleBlocker did.
type StarvedScheduleFiring struct {
	// TaskID is the never-started task holding the schedule -- for a
	// suite run, the first of its never-started tasks -- empty when the
	// schedule is not starved.
	TaskID string
	// Tasks is every task escalated, TaskID first: the one task, or each
	// never-started task of the holding suite run's current pass.
	Tasks []StarvedTask
	// SuiteRunID is the suite run holding the schedule
	// (SuppressedBySuiteRun), and Pass its current pass; zero when the
	// schedule is held by a task.
	SuiteRunID int64
	Pass       int
	// HeldFor and Occurrences are how long the held occurrence has
	// waited and how many occurrences have come due behind it, itself
	// included.
	HeldFor     time.Duration
	Occurrences int
	// From and To are TaskID's priority before and after; equal when
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

// blocker says whose firing the starved task (or suite run) came from,
// after "#<id> from" or "suite run <id> from".
func (f StarvedScheduleFiring) blocker() string {
	if f.HeldBy == "" {
		return "its previous firing"
	}
	if f.Group == "" {
		return fmt.Sprintf("schedule %s's firing", f.HeldBy)
	}
	return fmt.Sprintf("schedule %s's firing in overlap group %q", f.HeldBy, f.Group)
}

// Raised reports whether any task's priority was raised.
func (f StarvedScheduleFiring) Raised() bool {
	for _, t := range f.Tasks {
		if t.To > t.From {
			return true
		}
	}
	return f.To > f.From
}

// taskList is the escalated tasks as "#a, #b".
func (f StarvedScheduleFiring) taskList() string {
	ids := make([]string, 0, len(f.Tasks))
	for _, t := range f.Tasks {
		ids = append(ids, "#"+t.ID)
	}
	if len(ids) == 0 && f.TaskID != "" {
		ids = append(ids, "#"+f.TaskID)
	}
	return strings.Join(ids, ", ")
}

// ScheduleStarved reports whether a held occurrence that came due at
// occurrence, with occurrences due behind it (itself included), has
// waited long enough to count as starved by now.
func ScheduleStarved(occurrence, now time.Time, occurrences int) bool {
	return now.Sub(occurrence) >= ScheduleStarvedAfter || occurrences >= ScheduleStarvedOccurrences
}

// EscalateStarvedScheduleBlocker is the starved-firing rule above, for
// the skip k this cycle's firing of sched recorded. It does nothing --
// and returns the zero StarvedScheduleFiring -- unless k is a held
// occurrence that has been holding the schedule past
// ScheduleStarvedAfter or ScheduleStarvedOccurrences, and is blocked
// either by an open task that is still queued and has never had a run,
// or by a suite run (the schedule's own, or a peer's in its overlap
// group: SuppressedBySuiteRun) whose current pass has not started any of
// its tasks. A suite run's never-started tasks are raised together, and
// its notification clears when the run finishes rather than when one of
// them closes.
//
// Every cycle may call it: the priority only ever rises, the comment is
// written only when it does, and the notification is raised only while
// none is open for the schedule.
func (s *Store) EscalateStarvedScheduleBlocker(ctx context.Context, sched Schedule, k ScheduleSkip,
	now time.Time, loc *time.Location) (StarvedScheduleFiring, error) {

	if k.Outcome != ScheduleSkipHeld {
		return StarvedScheduleFiring{}, nil
	}
	switch {
	case k.Reason == SuppressedByOpenTask && k.TaskID != "":
	case k.Reason == SuppressedBySuiteRun:
	default:
		return StarvedScheduleFiring{}, nil
	}
	occurrences := 1 + foldedOccurrences(sched, now, loc)
	if !ScheduleStarved(k.Occurrence, now, occurrences) {
		return StarvedScheduleFiring{}, nil
	}
	out := StarvedScheduleFiring{HeldFor: now.Sub(k.Occurrence), Occurrences: occurrences}
	if k.HeldByScheduleID != "" && k.HeldByScheduleID != sched.ID {
		out.HeldBy, out.Group = k.HeldByScheduleID, strings.TrimSpace(sched.OverlapGroup)
	}
	var taskIDs []string
	clear := &NotificationClear{Kind: ClearsOnTask, Task: k.TaskID, Until: "closed"}
	if k.Reason == SuppressedBySuiteRun {
		run, err := s.suiteRunHolding(ctx, sched, k)
		if err != nil || run == nil {
			return StarvedScheduleFiring{}, err
		}
		ids, err := s.neverStartedPassTasks(ctx, *run)
		if err != nil || len(ids) == 0 {
			return StarvedScheduleFiring{}, err
		}
		out.SuiteRunID, out.Pass = run.ID, run.CurrentPass()
		taskIDs = ids
		clear = &NotificationClear{Kind: ClearsOnSuiteRun, SuiteRun: run.ID}
	} else {
		started, err := s.startedOrNotQueued(ctx, k.TaskID)
		if err != nil || started {
			return StarvedScheduleFiring{}, err
		}
		taskIDs = []string{k.TaskID}
	}
	top, err := s.topQueuedPriority(ctx, taskIDs...)
	if err != nil {
		return StarvedScheduleFiring{}, err
	}
	for _, id := range taskIDs {
		raise := StarvedTask{ID: id}
		if err := s.UpdateTask(ctx, id, func(t *Task) error {
			raise.From, raise.To = t.Priority, t.Priority
			if top > t.Priority {
				t.Priority, raise.To = top, top
			}
			return nil
		}); err != nil {
			return StarvedScheduleFiring{}, fmt.Errorf("raising the priority of %s: %w", id, err)
		}
		out.Tasks = append(out.Tasks, raise)
	}
	out.TaskID, out.From, out.To = out.Tasks[0].ID, out.Tasks[0].From, out.Tasks[0].To
	for _, raise := range out.Tasks {
		if raise.To <= raise.From {
			continue
		}
		one := out
		one.TaskID, one.From, one.To = raise.ID, raise.From, raise.To
		if _, err := s.AddComment(ctx, Comment{TaskID: raise.ID, Author: Attribution{Actor: SchedulePrincipal},
			Body: StarvedScheduleComment(sched, one), CreatedAt: now}); err != nil {
			return out, fmt.Errorf("noting the raised priority on %s: %w", raise.ID, err)
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
			TaskID:     out.TaskID,
			RaisedBy:   PrincipalString(SchedulePrincipal),
			RaisedAt:   now,
			ClearsWhen: clear,
		}); err != nil {
			return out, err
		}
		out.Notified = true
	}
	return out, nil
}

// startedOrNotQueued reports whether task id is anything but queued with
// no run ever started: a task that is running, or started and back in
// the queue (a retry, a resume), is waiting on something a priority does
// not answer.
func (s *Store) startedOrNotQueued(ctx context.Context, id string) (bool, error) {
	st, err := s.State(ctx, id)
	if err != nil {
		return false, fmt.Errorf("reading the state of %s: %w", id, err)
	}
	if st != StateQueued {
		return true, nil
	}
	runs, err := s.Runs(ctx, id)
	if err != nil {
		return false, fmt.Errorf("reading the runs of %s: %w", id, err)
	}
	return len(runs) > 0, nil
}

// suiteRunHolding is the active suite run a SuppressedBySuiteRun skip of
// sched is held by: the run k's holder (the schedule itself, or the peer
// in its overlap group) started, or -- for a group held by a run that
// names no schedule (ActiveSuiteRunSchedulesWithGroupTag) -- the oldest
// active run whose tasks carry the group's tag. Nil when none is active
// any more.
func (s *Store) suiteRunHolding(ctx context.Context, sched Schedule, k ScheduleSkip) (*SuiteRun, error) {
	holder := sched.ID
	if k.HeldByScheduleID != "" {
		holder = k.HeldByScheduleID
	}
	ids, err := listSuiteRunIDs(ctx, s.db,
		"SELECT `id` FROM `suite_run` WHERE `schedule_id` = ? AND `status` = '"+string(SuiteRunActive)+"' ORDER BY `id`",
		holder)
	if err != nil {
		return nil, fmt.Errorf("reading the active suite run of schedule %s: %w", holder, err)
	}
	if len(ids) == 0 && k.HeldByScheduleID == "" {
		if gt := ScheduleOverlapGroupTag(sched.Target, sched.OverlapGroup); gt != "" {
			ids, err = listSuiteRunIDs(ctx, s.db,
				"SELECT `r`.`id` FROM `suite_run` AS `r` "+
					"WHERE `r`.`status` = '"+string(SuiteRunActive)+"' "+
					"AND COALESCE(`r`.`schedule_id`, '') = '' AND EXISTS ("+
					"SELECT 1 FROM `suite_run_task` AS `rt` "+
					"JOIN `task_tag` AS `g` ON `g`.`task_id` = `rt`.`task_id` "+
					"WHERE `rt`.`run_id` = `r`.`id` AND `g`.`tag` = ?) ORDER BY `r`.`id`", gt)
			if err != nil {
				return nil, fmt.Errorf("reading the active suite run holding %q: %w", gt, err)
			}
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	return s.GetSuiteRun(ctx, ids[0])
}

// neverStartedPassTasks is the tasks of run's current pass that are
// queued with no run ever started, oldest first -- or none when any open
// task of the pass has started or is waiting on something else (a
// person's approval, a review): a pass that is under way is not starved
// of a worker, however long it takes.
func (s *Store) neverStartedPassTasks(ctx context.Context, run SuiteRun) ([]string, error) {
	var out []string
	for _, rt := range run.PassTasks(run.CurrentPass()) {
		st, err := s.State(ctx, rt.TaskID)
		if err != nil {
			return nil, fmt.Errorf("reading the state of %s: %w", rt.TaskID, err)
		}
		if st.Settled() {
			continue
		}
		started, err := s.startedOrNotQueued(ctx, rt.TaskID)
		if err != nil {
			return nil, err
		}
		if started {
			return nil, nil
		}
		out = append(out, rt.TaskID)
	}
	return out, nil
}

// topQueuedPriority is the highest priority of any queued task but
// those in except, and 0 when there is none.
func (s *Store) topQueuedPriority(ctx context.Context, except ...string) (int, error) {
	var top sql.NullInt64
	query := "SELECT MAX(`t`.`priority`) FROM `task` AS `t` " +
		"JOIN `task_state` AS `st` ON `st`.`task_id` = `t`.`id` WHERE `st`.`state` = ?"
	args := []any{string(StateQueued)}
	if len(except) > 0 {
		query += " AND `t`.`id` NOT IN (?" + strings.Repeat(", ?", len(except)-1) + ")"
		for _, id := range except {
			args = append(args, id)
		}
	}
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&top); err != nil {
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
	if f.SuiteRunID != 0 {
		return fmt.Sprintf("Schedule %q has not fired for %s: suite run %d from %s has not started any task of its pass %d (%s)",
			scheduleName(sched), f.HeldFor.Round(time.Minute), f.SuiteRunID, f.blocker(), f.Pass, f.taskList())
	}
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
	if f.SuiteRunID != 0 {
		return fmt.Sprintf("Schedule %q (%s) is set to overlap: skip, and suite run %d, from %s, "+
			"has not started any task of its pass %d (%s) while %d of the schedule's occurrences came due "+
			"(%s since the first). Every one of them is held until the run finishes. %s\n\n"+
			"A schedule held by a suite run whose tasks cannot get a worker stops for as long as they wait, "+
			"so the run's never-started tasks are kept and raised rather than left at the back of the queue. "+
			"If the run should not go on, close its tasks: the run ends, and the schedule fires again on its next cycle.",
			scheduleName(sched), sched.ID, f.SuiteRunID, f.blocker(), f.Pass, f.taskList(),
			f.Occurrences, f.HeldFor.Round(time.Minute), raised)
	}
	return fmt.Sprintf("Schedule %q (%s) is set to overlap: skip, and this task, from %s, "+
		"has been queued without ever starting while %d of its occurrences came due (%s since the first). "+
		"Every one of them is held until this task closes. %s\n\n"+
		"A schedule whose task cannot get a worker stops for as long as it waits, so this task is kept "+
		"and raised rather than replaced by a new firing at the back of the queue. If it should not run "+
		"at all, close it and the schedule fires again on its next cycle.",
		scheduleName(sched), sched.ID, f.blocker(), f.Occurrences, f.HeldFor.Round(time.Minute), raised)
}
