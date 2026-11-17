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
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
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

// A suite schedule held by its own suite run, whose pass has never
// started (walkthrough finding F6): the run's task is raised, the Inbox
// says which run, and the notification clears once the run finishes.
func TestStarvedSuiteScheduleRaisesItsRunsTasksUntilTheRunFinishes(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, sched := filedSuiteSchedule(t, store, "smoke", baseTime)
	sched.Overlap = model.OverlapSkip
	sched.Recurrence = model.Recurrence{Kind: model.RecurrenceEveryNMinutes, EveryNMinutes: 20}
	if err := store.PutSchedule(ctx, sched); err != nil {
		t.Fatal(err)
	}
	busyQueue(t, store, "busy-9", 9)

	if err := runScheduleOnly(t, store, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	runs := runsOfSchedule(t, store, sched.ID)
	if len(runs) != 1 || len(runs[0].Tasks) != 1 {
		t.Fatalf("suite runs = %+v, want one with one task", runs)
	}
	run, task := runs[0], runs[0].Tasks[0].TaskID

	if err := runScheduleOnly(t, store, baseTime.Add(40*time.Minute)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if got, _ := store.GetTask(ctx, task); got.Priority != model.DefaultTaskPriority {
		t.Fatalf("suite task priority after 40m = %d, want it left at %d", got.Priority, model.DefaultTaskPriority)
	}
	if n := starvedNotification(t, store, sched.ID); n != nil {
		t.Fatalf("notification after 40m: %+v", n)
	}

	now := baseTime.Add(60 * time.Minute)
	if err := runScheduleOnly(t, store, now); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if got := runsOfSchedule(t, store, sched.ID); len(got) != 1 {
		t.Fatalf("suite runs after starving = %d, want the one kept", len(got))
	}
	if got, _ := store.GetTask(ctx, task); got.Priority != 9 {
		t.Errorf("starved suite task priority = %d, want 9, the highest queued", got.Priority)
	}
	bodies := commentBodies(t, ctx, store, task)
	if len(bodies) != 1 || !strings.Contains(bodies[0], "from 5 to 9") ||
		!strings.Contains(bodies[0], fmt.Sprintf("suite run %d", run.ID)) {
		t.Errorf("comments on the suite task = %q, want one naming suite run %d raised from 5 to 9", bodies, run.ID)
	}
	n := starvedNotification(t, store, sched.ID)
	if n == nil {
		t.Fatal("no pipeline.schedule-starved notification for a schedule held an hour by a never-started suite run")
	}
	if n.TaskID != task || !strings.Contains(n.Title, fmt.Sprintf("suite run %d from its previous firing", run.ID)) ||
		n.ClearsWhen == nil || n.ClearsWhen.Kind != model.ClearsOnSuiteRun {
		t.Errorf("notification = %+v (clears %+v), want one naming suite run %d, clearing on the run", n, n.ClearsWhen, run.ID)
	}

	// Once the run is no longer active the notification clears, though
	// none of its tasks is the one it waits on.
	if err := store.CompleteSuiteRun(ctx, run.ID, model.SuiteRunFailed, "stopped", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := orchestrator.ClearMetNotifications(ctx, store, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if n := starvedNotification(t, store, sched.ID); n != nil {
		t.Errorf("notification after the run finished = %+v, want it cleared", n)
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
	review.Filing = model.Filing{Attribution: model.Attribution{Actor: model.SchedulePrincipal},
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
	// A review holds the schedule only for workflowReviewHold -- three
	// mornings after it was filed at 05:58, the 15th's 05:53 occurrence --
	// so neither the comment nor the Inbox may say the wait is unbounded.
	const until = "held until this review closes or until 2026-11-15T05:53:00Z, when the schedule fires over it"
	texts := map[string]string{"notification body": n.Body}
	if bodies := commentBodies(t, ctx, store, review.ID); len(bodies) > 0 {
		texts["comment"] = bodies[0]
	}
	for what, body := range texts {
		if !strings.Contains(body, until) || strings.Contains(body, "held until this task closes") {
			t.Errorf("%s = %q, want it to say occurrences are %s", what, body, until)
		}
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
	review.Filing = model.Filing{Attribution: model.Attribution{Actor: model.SchedulePrincipal},
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

// workflowReview files a queued review of sched's firing at filed, the
// way FileWorkflowReview leaves one, and records that firing as settled
// by it.
func workflowReview(t *testing.T, store *model.Store, sched model.Schedule, id string,
	firedAt, filed time.Time) model.Task {
	t.Helper()
	ctx := t.Context()
	review := filedTask(t, ctx, store, id, sched.Target)
	review.Title = sched.Title
	review.Filing = model.Filing{Attribution: model.Attribution{Actor: model.SchedulePrincipal},
		Reason: model.ReasonSchedule}
	review.Approval = &model.Attribution{Actor: model.SchedulePrincipal}
	review.Tags = []string{model.ScheduleFiringTag(sched.ID)}
	review.CreatedAt = &filed
	review.Priority = model.DefaultTaskPriority
	if err := store.PutTask(ctx, review); err != nil {
		t.Fatal(err)
	}
	if err := store.PutScheduledWorkflowRun(ctx, model.ScheduledWorkflowRun{
		ScheduleID: sched.ID, Stem: "live-secrets", Repo: sched.Target, FiredAt: firedAt,
		FinishedAt: &filed, Conclusion: "success", ReviewTaskID: review.ID,
	}); err != nil {
		t.Fatalf("recording the firing reviewed by %s: %v", id, err)
	}
	return review
}

// workflowReviewHold expires with the escalated review still open: the
// schedule fires over it and leaves it open, so the notification must
// clear on that firing rather than wait for a close that may never come,
// and the next review to starve the schedule must get a notification of
// its own rather than be masked by the abandoned one's.
func TestStarvedWorkflowNotificationClearsWhenTheScheduleFiresOverTheReview(t *testing.T) {
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
	first := workflowReview(t, store, sched, "review-1", firstMorning, firstMorning.Add(5*time.Minute))

	if err := runScheduleOnly(t, store, firstMorning.Add(25*time.Hour)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if n := starvedNotification(t, store, sched.ID); n == nil || n.TaskID != first.ID {
		t.Fatalf("notification an hour into the hold = %+v, want one naming %s", n, first.ID)
	}

	// Three mornings after review-1 was filed the hold is over: the
	// schedule fires over the review, which stays open.
	thirdMorning := firstMorning.Add(72 * time.Hour)
	if err := runScheduleOnly(t, store, thirdMorning.Add(10*time.Minute)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if st, err := store.State(ctx, first.ID); err != nil || st == model.StateClosed {
		t.Fatalf("review-1 state after the schedule fired over it = %q (%v), want it left open", st, err)
	}
	if row, err := store.GetScheduledWorkflowRun(ctx, sched.ID); err != nil || row == nil || !row.FiredAt.After(firstMorning) {
		t.Fatalf("workflow run after the hold expired = %+v (%v), want a new firing", row, err)
	}
	if n := starvedNotification(t, store, sched.ID); n != nil {
		t.Fatalf("notification after the schedule fired again = %+v, want it cleared", n)
	}

	// That firing's review never starts either, and starves the next
	// morning's occurrence while review-1 is still open.
	second := workflowReview(t, store, sched, "review-2", thirdMorning, thirdMorning.Add(30*time.Minute))
	if err := runScheduleOnly(t, store, thirdMorning.Add(25*time.Hour)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	n := starvedNotification(t, store, sched.ID)
	if n == nil {
		t.Fatal("no notification for a later review starving the schedule")
	}
	if n.TaskID != second.ID || n.ClearsWhen == nil || n.ClearsWhen.Task != second.ID {
		t.Errorf("notification = %+v (clears %+v), want one naming and clearing with %s", n, n.ClearsWhen, second.ID)
	}
}

// The blocker of a held occurrence can change while its notification is
// open, with no firing in between -- another never-started task in the
// overlap group once the first one's hold ends. The notification follows
// the task now holding the schedule, rather than keeping the first one's
// task and clearing when that one closes.
func TestStarvedScheduleNotificationFollowsANewBlocker(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	due := time.Date(2026, 11, 13, 5, 53, 0, 0, time.UTC)
	sched := model.Schedule{
		ID: "nightly", Title: "Nightly", Target: repo,
		Recurrence: model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 5*60 + 53},
		Overlap:    model.OverlapSkip, Enabled: true, NextRunAt: due, CreatedAt: due.Add(-48 * time.Hour),
	}
	if err := store.PutSchedule(ctx, sched); err != nil {
		t.Fatalf("filing schedule: %v", err)
	}
	busyQueue(t, store, "busy-9", 9)
	filedTask(t, ctx, store, "blocker-1", repo)
	filedTask(t, ctx, store, "blocker-2", repo)

	skip := func(task string) model.ScheduleSkip {
		return model.ScheduleSkip{ScheduleID: sched.ID, Occurrence: due, Outcome: model.ScheduleSkipHeld,
			Reason: model.SuppressedByOpenReview, TaskID: task}
	}
	now := due.Add(2 * time.Hour)
	if out, err := store.EscalateStarvedScheduleBlocker(ctx, sched, skip("blocker-1"), now, time.UTC); err != nil || !out.Notified {
		t.Fatalf("escalating blocker-1 = %+v, %v; want it notified", out, err)
	}
	out, err := store.EscalateStarvedScheduleBlocker(ctx, sched, skip("blocker-2"), now.Add(time.Minute), time.UTC)
	if err != nil || !out.Notified {
		t.Fatalf("escalating blocker-2 = %+v, %v; want it notified", out, err)
	}
	n := starvedNotification(t, store, sched.ID)
	if n == nil || n.TaskID != "blocker-2" || n.ClearsWhen == nil || n.ClearsWhen.Task != "blocker-2" {
		t.Fatalf("notification = %+v, want one naming and clearing with blocker-2", n)
	}
	// The same blocker again is the one notification, not a re-raise.
	if out, err := store.EscalateStarvedScheduleBlocker(ctx, sched, skip("blocker-2"), now.Add(2*time.Minute), time.UTC); err != nil || out.Notified {
		t.Fatalf("escalating blocker-2 again = %+v, %v; want no new notification", out, err)
	}
}
