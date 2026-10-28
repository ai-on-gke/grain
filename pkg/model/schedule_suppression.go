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
	"time"
)

// ScheduleSuppressionReason is which unfinished thing is holding a
// schedule's cadence -- one value per kind of firing, because "the
// previous firing has not finished" means a different object, and a
// different thing to do about it, for each of the three (OverlapPolicy's
// own doc comment on what counts as unfinished).
//
// The values are the vocabulary pkg/ui puts on the wire unchanged, the
// same way RecurrenceKind's are: one spelling for this fact, not a
// second one per layer.
type ScheduleSuppressionReason string

const (
	// SuppressedByOpenTask is a schedule that files a task whose last
	// firing's task is still open. Closing it lets the schedule fire.
	SuppressedByOpenTask ScheduleSuppressionReason = "task"
	// SuppressedBySuiteRun is a schedule that runs a suite whose last
	// run has not finished.
	SuppressedBySuiteRun ScheduleSuppressionReason = "suiteRun"
	// SuppressedByWorkflowRun is a schedule translated out of a
	// workflow's own cron whose last firing's CI run has not ended --
	// or has ended with nobody sent to read it yet, which is the same
	// half of the firing (ScheduledWorkflowRun.Settled).
	SuppressedByWorkflowRun ScheduleSuppressionReason = "workflowRun"
	// SuppressedByOpenReview is the other half of that one: the run
	// finished, the review was filed, and nobody has closed it. Unlike
	// every reason above it expires -- workflowReviewHold, reported as
	// ScheduleSuppression.Until.
	SuppressedByOpenReview ScheduleSuppressionReason = "review"
	// SuppressedByStandingWork is a schedule whose work lands in a
	// repository this deployment does not run the standing work of --
	// the repository default is off, or the schedule's own override is
	// (model.RunsWorkItem, grain/task-3332). Unlike every reason above it
	// is not waiting on anything: each occurrence is skipped and the
	// cadence moves on (suppressStandingWorkOff), so this is reported
	// whether or not the schedule is due, for as long as it is true.
	SuppressedByStandingWork ScheduleSuppressionReason = "standingWork"
)

// ScheduleSuppression is a firing that has come due and is not
// happening: the schedule's previous firing has not finished and its
// overlap policy is to skip (OverlapSkip).
//
// It exists to be *shown*. A suppressed firing leaves NextRunAt exactly
// where it was, so the poll re-asks and re-skips every cycle, and until
// this there was nothing anywhere saying so -- a schedules page drew a
// next-run time that had already gone by, with no hint why, for as long
// as the previous firing stayed unfinished (grain/task-493 bounded that
// wait for a workflow schedule's review; this is how somebody finds out
// they are inside one).
type ScheduleSuppression struct {
	// Reason is which unfinished thing is holding the cadence.
	Reason ScheduleSuppressionReason
	// TaskID is the open task doing the holding, for the two reasons
	// that have one -- SuppressedByOpenTask and SuppressedByOpenReview.
	// It is the whole point of reporting this: what to do about a
	// schedule that has stopped firing is to go and close that task.
	TaskID string
	// Until is when grain stops waiting and fires anyway, for the one
	// reason that is bounded from here (SuppressedByOpenReview, and
	// workflowReviewHold is the bound). Zero for every other reason, and
	// for a review whose filing time cannot be measured: those wait on
	// something ending rather than on a clock, so there is no date to
	// show and showing one would be a guess.
	Until time.Time
	// Detail is the sentence to show for a reason that has one of its
	// own -- SuppressedByStandingWork's "skipped: this deployment doesn't
	// run <repo>'s standing work" -- and empty for the reasons the task
	// or the date above already say everything about.
	Detail string
}

// SuppressedFiring reports why sched has come due and is not firing, or
// nil when nothing is holding it back.
//
// It is the read-only half of the overlap check each fireXSchedule makes
// -- the same store questions, in the same order, asked without writing
// anything -- so that a page can say what the firing loop is quietly
// doing every cycle. Kept beside them rather than derived from a column
// a suppressed firing writes: there is no such column, a suppression is
// a fact about right now (the task holding it may have been closed a
// second ago), and a stored one would go stale exactly when it mattered.
//
// Three conditions before the store is asked anything, and each of them
// is a different answer to "why is this schedule not firing", already
// visible without this:
//
//   - A disabled schedule is not firing because it is paused, which
//     every UI showing a schedule already says.
//   - A schedule set to OverlapAllow never suppresses anything.
//   - A schedule whose next run is still ahead is not firing because it
//     is not due yet. Only a NextRunAt that has gone by can be a
//     suppressed firing, because skipping is what leaves one there.
//
// The other suppression a firing has (suppressOffTargetRepos) is
// deliberately not reported here: that one advances the cadence, so it
// leaves a NextRunAt in the future and does not look like this. It is
// not a schedule silently waiting on work that has not finished; it is
// a schedule whose repo an operator took off the list, having been told
// so at the save that did it (StrandedWork).
func (s *Store) SuppressedFiring(ctx context.Context, sched Schedule, now time.Time,
	loc *time.Location) (*ScheduleSuppression, error) {

	if !sched.Enabled {
		return nil, nil
	}
	// Asked ahead of the conditions below, because it is the one
	// suppression that advances the cadence and so never leaves a
	// NextRunAt that has gone by: it is the schedule's standing status,
	// and the only place its skipped firings are said.
	if off, err := s.standingWorkOff(ctx, sched, sched.Target); err != nil {
		return nil, err
	} else if off {
		return &ScheduleSuppression{
			Reason: SuppressedByStandingWork,
			Detail: "skipped: " + StandingWorkOffReason(sched.Target),
		}, nil
	}
	if sched.Overlap.Allows() || sched.NextRunAt.After(now) {
		return nil, nil
	}
	_, workflow := workflowReviewSchedule(sched)
	switch {
	case sched.SuiteID != nil:
		// fireSuiteSchedule's own check.
		active, err := s.HasActiveRunForSchedule(ctx, sched.ID)
		if err != nil {
			return nil, fmt.Errorf("checking for a previous unfinished firing: %w", err)
		}
		if active {
			return &ScheduleSuppression{Reason: SuppressedBySuiteRun}, nil
		}
	case workflow:
		// fireWorkflowSchedule's own check, which is two questions --
		// and the only one of the three with a bound worth reporting.
		hold, err := s.workflowFiringInFlight(ctx, sched, now, loc)
		if err != nil {
			return nil, err
		}
		switch {
		case hold.Run:
			return &ScheduleSuppression{Reason: SuppressedByWorkflowRun}, nil
		case hold.Held:
			return &ScheduleSuppression{
				Reason: SuppressedByOpenReview,
				TaskID: hold.Review,
				Until:  hold.Until,
			}, nil
		}
	default:
		// fireTaskSchedule's own check, asked for the task rather than
		// for a bare yes/no (HasOpenTaskWithTag): which task is holding
		// the schedule is the actionable half of the answer, and it is
		// the same row either way.
		open, err := s.OpenTaskWithTag(ctx, ScheduleFiringTag(sched.ID))
		if err != nil {
			return nil, fmt.Errorf("checking for a previous unfinished firing: %w", err)
		}
		if open != nil {
			return &ScheduleSuppression{Reason: SuppressedByOpenTask, TaskID: open.ID}, nil
		}
	}
	return nil, nil
}
