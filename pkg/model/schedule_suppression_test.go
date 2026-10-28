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

package model_test

// A firing a schedule's overlap policy suppressed leaves NextRunAt
// exactly where it was, so the poll re-asks and re-skips every cycle and
// the schedule quietly stops running for as long as the previous firing
// stays unfinished. Store.SuppressedFiring is how somebody finds out
// they are in that state: the same question the firing loop asks every
// cycle, asked without firing anything, so a page can say which half of
// the previous firing is holding the cadence and -- for the one half
// that is bounded -- until when (grain/task-493 put the bound in; this
// is the part that says so out loud).
//
// The fixtures here (nightlySchedule, fireNightly, fileReview) are the
// ones the bound's own tests use, in workflow_schedule_overlap_test.go.

import (
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// dueSchedule re-reads the schedule after a firing has moved it on: the
// suppression question is asked of the row as it now stands, exactly as
// the next cycle's firing would ask it.
func dueSchedule(t *testing.T, store *model.Store, ctx context.Context, id string) model.Schedule {
	t.Helper()
	sched, err := store.GetSchedule(ctx, id)
	if err != nil || sched == nil {
		t.Fatalf("reading schedule %s: %v, %v", id, sched, err)
	}
	return *sched
}

func suppression(t *testing.T, store *model.Store, ctx context.Context, sched model.Schedule,
	now time.Time) *model.ScheduleSuppression {

	t.Helper()
	got, err := store.SuppressedFiring(ctx, sched, now, time.UTC)
	if err != nil {
		t.Fatalf("asking why %s is not firing: %v", sched.ID, err)
	}
	return got
}

// The run half: last night's firing recorded a CI run that nobody has
// been sent to read yet, so tonight is not firing and the reason is the
// run rather than anything anybody can close.
func TestADueWorkflowScheduleSaysItsLastRunHasNotFinished(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	sched := nightlySchedule(t, store, ctx)
	fireNightly(t, store, ctx, sched.ID, firstNight)

	secondNight := firstNight.Add(24 * time.Hour)
	got := suppression(t, store, ctx, dueSchedule(t, store, ctx, sched.ID), secondNight)
	if got == nil {
		t.Fatal("SuppressedFiring = nil, want the unfinished run reported")
	}
	if got.Reason != model.SuppressedByWorkflowRun {
		t.Errorf("reason = %q, want %q", got.Reason, model.SuppressedByWorkflowRun)
	}
	if got.TaskID != "" {
		t.Errorf("taskId = %q, want none: there is no review to close yet", got.TaskID)
	}
	if !got.Until.IsZero() {
		t.Errorf("until = %s, want none: this half waits on a run ending, not on a clock", got.Until)
	}
}

// The review half, which is the one an operator can do something about:
// the task holding the cadence is named, and so is the instant grain
// stops waiting for it.
func TestADueWorkflowScheduleNamesTheOpenReviewHoldingIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	sched := nightlySchedule(t, store, ctx)
	fireNightly(t, store, ctx, sched.ID, firstNight)
	review := fileReview(t, store, ctx, sched, firstNight.Add(3*time.Hour))

	due := dueSchedule(t, store, ctx, sched.ID)
	secondNight := firstNight.Add(24 * time.Hour)
	got := suppression(t, store, ctx, due, secondNight)
	if got == nil {
		t.Fatal("SuppressedFiring = nil, want the open review reported")
	}
	if got.Reason != model.SuppressedByOpenReview {
		t.Errorf("reason = %q, want %q", got.Reason, model.SuppressedByOpenReview)
	}
	if got.TaskID != review {
		t.Errorf("taskId = %q, want the review %s that is holding the cadence", got.TaskID, review)
	}
	// Three of this schedule's own occurrences after the review was
	// filed at 05:11 on the 21st: the nights of the 22nd, 23rd and 24th,
	// the last of which is past the 24-hour floor.
	wantUntil := time.Date(2026, 9, 24, 2, 11, 0, 0, time.UTC)
	if !got.Until.Equal(wantUntil) {
		t.Errorf("until = %s, want the hold expiring at %s", got.Until, wantUntil)
	}

	// Past the hold there is no suppression left to report: the next
	// firing goes ahead over the abandoned review rather than waiting
	// for it, so saying the schedule is being held would be a lie.
	if got := suppression(t, store, ctx, due, wantUntil.Add(time.Minute)); got != nil {
		t.Errorf("SuppressedFiring past the hold = %+v, want nil: the schedule fires again now", got)
	}
}

// A schedule whose cadence has not come round is not being suppressed --
// it is simply not due. Only a NextRunAt that has gone by can be a
// suppressed firing, which is what keeps this off every ordinary row.
func TestAScheduleThatIsNotDueYetReportsNoSuppression(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	sched := nightlySchedule(t, store, ctx)
	fireNightly(t, store, ctx, sched.ID, firstNight)
	fileReview(t, store, ctx, sched, firstNight.Add(3*time.Hour))

	due := dueSchedule(t, store, ctx, sched.ID)
	if got := suppression(t, store, ctx, due, firstNight.Add(12*time.Hour)); got != nil {
		t.Errorf("SuppressedFiring = %+v, want nil before the next occurrence", got)
	}
}

// The two settings that mean a schedule is not firing for a reason
// already on the page: paused, and set to overlap.
func TestSuppressionIsNotReportedForAPausedOrOverlappingSchedule(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	sched := nightlySchedule(t, store, ctx)
	fireNightly(t, store, ctx, sched.ID, firstNight)
	fileReview(t, store, ctx, sched, firstNight.Add(3*time.Hour))
	secondNight := firstNight.Add(24 * time.Hour)

	paused := dueSchedule(t, store, ctx, sched.ID)
	paused.Enabled = false
	if got := suppression(t, store, ctx, paused, secondNight); got != nil {
		t.Errorf("paused: SuppressedFiring = %+v, want nil -- it is not firing because it is paused", got)
	}

	overlapping := dueSchedule(t, store, ctx, sched.ID)
	overlapping.Overlap = model.OverlapAllow
	if got := suppression(t, store, ctx, overlapping, secondNight); got != nil {
		t.Errorf("overlapping: SuppressedFiring = %+v, want nil -- this schedule never skips", got)
	}
}

// And the plain kind: a schedule that files a task, whose last task
// nobody closed. It is the same silent halt, and the answer to it is the
// same -- close the task it names.
func TestADueTaskScheduleNamesTheOpenTaskHoldingIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	sched := model.Schedule{
		ID:         "sched-chore",
		Title:      "Weekly tidy",
		Target:     nightlyRepo,
		Recurrence: model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 9 * 60},
		Enabled:    true,
		NextRunAt:  firstNight,
		CreatedAt:  firstNight.Add(-24 * time.Hour),
	}
	if err := store.PutSchedule(ctx, sched); err != nil {
		t.Fatalf("writing the schedule: %v", err)
	}
	firing, err := store.FireSchedule(ctx, sched, firstNight, time.UTC, model.RepoRef{})
	if err != nil {
		t.Fatalf("firing the schedule: %v", err)
	}
	if firing.TaskID == "" {
		t.Fatalf("firing = %+v, want a filed task", firing)
	}

	due := dueSchedule(t, store, ctx, sched.ID)
	got := suppression(t, store, ctx, due, due.NextRunAt)
	if got == nil {
		t.Fatal("SuppressedFiring = nil, want the still-open task reported")
	}
	if got.Reason != model.SuppressedByOpenTask || got.TaskID != firing.TaskID {
		t.Errorf("suppression = %+v, want %q naming task %s",
			got, model.SuppressedByOpenTask, firing.TaskID)
	}

	// Closed, and the schedule is free again: nothing is stored about
	// the suppression, so there is nothing to clear.
	closed := due.NextRunAt.Add(time.Hour)
	if err := store.ObserveField(ctx, firing.TaskID, closed, func(o *model.Observation) {
		o.ClosedAt = &closed
	}); err != nil {
		t.Fatalf("closing the filed task: %v", err)
	}
	if got := suppression(t, store, ctx, due, closed); got != nil {
		t.Errorf("SuppressedFiring after the task was closed = %+v, want nil", got)
	}
}
