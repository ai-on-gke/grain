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

package orchestrator_test

// An overlap: skip schedule held by a task of its own that has never
// started (model.EscalateStarvedScheduleBlocker, grain/task-4086): the
// release train's tick #3946 sat queued for fourteen hours on 2026-11-10
// and every firing behind it was skipped in silence.

import (
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// trainSchedule is a 20-minute overlap: skip schedule, the release
// train's shape, first due at baseTime.
func trainSchedule(t *testing.T, store *model.Store) model.Schedule {
	t.Helper()
	sched := model.Schedule{
		ID:         "release-train",
		Title:      "Release train",
		Body:       "Run one tick of the release train.",
		Target:     model.RepoRef{Owner: "acme", Name: "widgets"},
		Recurrence: model.Recurrence{Kind: model.RecurrenceEveryNMinutes, EveryNMinutes: 20},
		Overlap:    model.OverlapSkip,
		Enabled:    true,
		NextRunAt:  baseTime,
		CreatedAt:  baseTime,
	}
	if err := store.PutSchedule(t.Context(), sched); err != nil {
		t.Fatalf("filing schedule: %v", err)
	}
	return sched
}

// busyQueue files a queued task at priority, the work the tick waits
// behind.
func busyQueue(t *testing.T, store *model.Store, id string, priority int) {
	t.Helper()
	task := filedTask(t, t.Context(), store, id, model.RepoRef{Owner: "acme", Name: "widgets"})
	task.Priority = priority
	if err := store.PutTask(t.Context(), task); err != nil {
		t.Fatal(err)
	}
}

func starvedNotification(t *testing.T, store *model.Store, schedID string) *model.Notification {
	t.Helper()
	n, err := store.OpenNotification(t.Context(),
		model.NotificationSubject(model.FamilyScheduleStarved, schedID))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestStarvedScheduleTickIsRaisedAndNotifiedOnce(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sched := trainSchedule(t, store)
	busyQueue(t, store, "busy-9", 9)
	busyQueue(t, store, "busy-7", 7)

	if err := runScheduleOnly(t, store, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	filed := scheduledTasksTagged(t, store, model.ScheduleFiringTag(sched.ID))
	if len(filed) != 1 {
		t.Fatalf("filed %d ticks, want 1", len(filed))
	}
	tick := filed[0].ID

	// Two later occurrences held behind a tick that has only waited 40
	// minutes: skipped, and nothing raised yet.
	if err := runScheduleOnly(t, store, baseTime.Add(40*time.Minute)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if got, _ := store.GetTask(ctx, tick); got.Priority != model.DefaultTaskPriority {
		t.Fatalf("priority after 40m = %d, want %d left alone", got.Priority, model.DefaultTaskPriority)
	}
	if n := starvedNotification(t, store, sched.ID); n != nil {
		t.Fatalf("notification raised after 40m: %+v", n)
	}

	// The third occurrence held: starved. Kept, not superseded: still one
	// tick, raised to the highest queued priority, told why, and in the Inbox.
	now := baseTime.Add(60 * time.Minute)
	if err := runScheduleOnly(t, store, now); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if filed := scheduledTasksTagged(t, store, model.ScheduleFiringTag(sched.ID)); len(filed) != 1 {
		t.Fatalf("filed %d ticks after starving, want the one kept", len(filed))
	}
	got, err := store.GetTask(ctx, tick)
	if err != nil {
		t.Fatal(err)
	}
	if got.Priority != 9 {
		t.Errorf("starved tick priority = %d, want 9, the highest queued", got.Priority)
	}
	bodies := commentBodies(t, ctx, store, tick)
	if len(bodies) != 1 || !strings.Contains(bodies[0], "from 5 to 9") {
		t.Errorf("comments on the starved tick = %q, want one saying it was raised from 5 to 9", bodies)
	}
	n := starvedNotification(t, store, sched.ID)
	if n == nil {
		t.Fatal("no pipeline.schedule-starved notification for a schedule held an hour by a never-started tick")
	}
	if n.TaskID != tick || n.RaiseCount != 1 || !strings.Contains(n.Title, "never started") {
		t.Errorf("notification = %+v, want one raise naming tick %s", n, tick)
	}

	// Later cycles neither comment again nor bump the notification.
	if err := runScheduleOnly(t, store, now.Add(10*time.Minute)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if bodies := commentBodies(t, ctx, store, tick); len(bodies) != 1 {
		t.Errorf("comments after a later cycle = %q, want still one", bodies)
	}
	if n := starvedNotification(t, store, sched.ID); n == nil || n.RaiseCount != 1 {
		t.Errorf("notification after a later cycle = %+v, want still one raise", n)
	}
}

func TestStarvedScheduleByTimeAloneOnASlowCadence(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sched := filedSchedule(t, store, "daily", baseTime)
	busyQueue(t, store, "busy-9", 9)
	if err := runScheduleOnly(t, store, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	tick := scheduledTasksTagged(t, store, model.ScheduleFiringTag(sched.ID))[0].ID

	// One held occurrence, but an hour past it.
	held := baseTime.Add(24 * time.Hour)
	if err := runScheduleOnly(t, store, held.Add(30*time.Minute)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if n := starvedNotification(t, store, sched.ID); n != nil {
		t.Fatalf("notification 30m into a held daily occurrence: %+v", n)
	}
	if err := runScheduleOnly(t, store, held.Add(time.Hour)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if got, _ := store.GetTask(ctx, tick); got.Priority != 9 {
		t.Errorf("priority = %d, want 9 an hour into the held occurrence", got.Priority)
	}
	if starvedNotification(t, store, sched.ID) == nil {
		t.Error("no notification an hour into the held occurrence")
	}
}

func TestStartedScheduleTickIsNotStarved(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sched := trainSchedule(t, store)
	busyQueue(t, store, "busy-9", 9)
	if err := runScheduleOnly(t, store, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	tick := scheduledTasksTagged(t, store, model.ScheduleFiringTag(sched.ID))[0].ID
	start := baseTime.Add(time.Minute)
	if err := store.StartRun(ctx, model.Run{ID: tick + "-1", TaskID: tick, Attempt: 1, StartedAt: start},
		model.Limits{}); err != nil {
		t.Fatal(err)
	}

	// A tick that is running holds the schedule as overlap: skip means
	// it to, however long it takes.
	if err := runScheduleOnly(t, store, baseTime.Add(2*time.Hour)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if got, _ := store.GetTask(ctx, tick); got.Priority != model.DefaultTaskPriority {
		t.Errorf("running tick priority = %d, want it left at %d", got.Priority, model.DefaultTaskPriority)
	}
	if n := starvedNotification(t, store, sched.ID); n != nil {
		t.Errorf("notification for a tick that is running: %+v", n)
	}
}

// A workflow schedule held by its previous firing's review, which has sat
// queued without ever starting (grain/task-5445): on 2026-11-13 a daily
// workflow's review sat queued at priority 5 for over 30 hours and held
// the next morning's firing in silence, because the held skip's reason is
// SuppressedByOpenReview rather than SuppressedByOpenTask. A review that
// never got a worker is not one a person is reading, so it is raised and
// notified on the same rule as a task schedule's never-started firing.
func TestStarvedWorkflowScheduleReviewIsRaisedAndNotified(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	firstMorning := time.Date(2026, 11, 12, 5, 53, 0, 0, time.UTC)
	templateID := model.WorkflowReviewTemplateID("live-secrets")
	sched := model.Schedule{
		ID:         "repo:schedule:acme:widgets:live-secrets",
		Title:      `Review the "live-secrets" workflow run`,
		Target:     repo,
		TemplateID: &templateID,
		Recurrence: model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 5*60 + 53},
		Overlap:    model.OverlapSkip,
		Enabled:    true,
		NextRunAt:  firstMorning.Add(24 * time.Hour),
		CreatedAt:  firstMorning.Add(-24 * time.Hour),
	}
	if err := store.PutSchedule(ctx, sched); err != nil {
		t.Fatalf("filing schedule: %v", err)
	}
	busyQueue(t, store, "busy-9", 9)

	// Yesterday's firing: its run finished and its review was filed, and
	// the review has been queued ever since without a run.
	filed := firstMorning.Add(5 * time.Minute)
	review := filedTask(t, ctx, store, "review-1", repo)
	review.Title = sched.Title
	review.Origin = model.Origin{Attribution: model.Attribution{Actor: model.SchedulePrincipal},
		Reason: model.ReasonSchedule}
	review.Approval = &model.Attribution{Actor: model.SchedulePrincipal}
	review.Tags = []string{model.ScheduleFiringTag(sched.ID)}
	review.CreatedAt = &filed
	review.Priority = model.DefaultTaskPriority
	if err := store.PutTask(ctx, review); err != nil {
		t.Fatal(err)
	}
	finished := filed
	if err := store.PutScheduledWorkflowRun(ctx, model.ScheduledWorkflowRun{
		ScheduleID:   sched.ID,
		Stem:         "live-secrets",
		Repo:         repo,
		FiredAt:      firstMorning,
		FinishedAt:   &finished,
		Conclusion:   "success",
		ReviewTaskID: review.ID,
	}); err != nil {
		t.Fatalf("recording yesterday's firing: %v", err)
	}

	// Half an hour into this morning's held occurrence: held, not raised.
	held := firstMorning.Add(24 * time.Hour)
	if err := runScheduleOnly(t, store, held.Add(30*time.Minute)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if got, _ := store.GetTask(ctx, review.ID); got.Priority != model.DefaultTaskPriority {
		t.Fatalf("review priority 30m into the hold = %d, want %d left alone", got.Priority, model.DefaultTaskPriority)
	}
	if n := starvedNotification(t, store, sched.ID); n != nil {
		t.Fatalf("notification 30m into the hold: %+v", n)
	}

	// An hour in: starved. The review is kept, raised and in the Inbox.
	if err := runScheduleOnly(t, store, held.Add(time.Hour)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	got, err := store.GetTask(ctx, review.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Priority != 9 {
		t.Errorf("starved review priority = %d, want 9, the highest queued", got.Priority)
	}
	if bodies := commentBodies(t, ctx, store, review.ID); len(bodies) != 1 || !strings.Contains(bodies[0], "from 5 to 9") {
		t.Errorf("comments on the starved review = %q, want one saying it was raised from 5 to 9", bodies)
	}
	n := starvedNotification(t, store, sched.ID)
	if n == nil {
		t.Fatal("no pipeline.schedule-starved notification for a workflow schedule held an hour by a never-started review")
	}
	if n.TaskID != review.ID || n.RaiseCount != 1 || !strings.Contains(n.Title, "never started") {
		t.Errorf("notification = %+v, want one raise naming review %s", n, review.ID)
	}

	// The review closes: the notification clears with it.
	closedAt := held.Add(90 * time.Minute)
	if err := store.ObserveClose(ctx, review.ID, closedAt, model.SchedulePrincipal, "done",
		func(o *model.Observation) { o.CompletedAt, o.ClosedAt = &closedAt, &closedAt }); err != nil {
		t.Fatal(err)
	}
	if err := runScheduleOnly(t, store, closedAt.Add(time.Minute)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if n := starvedNotification(t, store, sched.ID); n != nil {
		t.Errorf("notification after the review closed = %+v, want it cleared", n)
	}
}

// A review that has started holds the schedule as workflowReviewHold
// means it to: it is somebody's to read, and never raised as starved.
func TestStartedWorkflowScheduleReviewIsNotStarved(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	firstMorning := time.Date(2026, 11, 12, 5, 53, 0, 0, time.UTC)
	templateID := model.WorkflowReviewTemplateID("live-secrets")
	sched := model.Schedule{
		ID: "live-secrets", Title: "Review live-secrets", Target: repo, TemplateID: &templateID,
		Recurrence: model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 5*60 + 53},
		Overlap:    model.OverlapSkip, Enabled: true,
		NextRunAt: firstMorning.Add(24 * time.Hour), CreatedAt: firstMorning.Add(-24 * time.Hour),
	}
	if err := store.PutSchedule(ctx, sched); err != nil {
		t.Fatalf("filing schedule: %v", err)
	}
	busyQueue(t, store, "busy-9", 9)
	filed := firstMorning.Add(5 * time.Minute)
	review := filedTask(t, ctx, store, "review-1", repo)
	review.Origin = model.Origin{Attribution: model.Attribution{Actor: model.SchedulePrincipal},
		Reason: model.ReasonSchedule}
	review.Tags = []string{model.ScheduleFiringTag(sched.ID)}
	review.CreatedAt = &filed
	if err := store.PutTask(ctx, review); err != nil {
		t.Fatal(err)
	}
	finished := filed
	if err := store.PutScheduledWorkflowRun(ctx, model.ScheduledWorkflowRun{
		ScheduleID: sched.ID, Stem: "live-secrets", Repo: repo, FiredAt: firstMorning,
		FinishedAt: &finished, Conclusion: "success", ReviewTaskID: review.ID,
	}); err != nil {
		t.Fatal(err)
	}
	start := filed.Add(time.Minute)
	if err := store.StartRun(ctx, model.Run{ID: review.ID + "-1", TaskID: review.ID, Attempt: 1, StartedAt: start},
		model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := runScheduleOnly(t, store, firstMorning.Add(26*time.Hour)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if got, _ := store.GetTask(ctx, review.ID); got.Priority != model.DefaultTaskPriority {
		t.Errorf("started review priority = %d, want it left at %d", got.Priority, model.DefaultTaskPriority)
	}
	if n := starvedNotification(t, store, sched.ID); n != nil {
		t.Errorf("notification for a review that has started: %+v", n)
	}
}
