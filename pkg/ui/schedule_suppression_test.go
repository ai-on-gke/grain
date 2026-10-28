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

package ui_test

// What the schedules page needs to say when a schedule has come due and
// is not firing: which unfinished firing is holding it, and what to
// close to let it go. Without it the page drew a next-run time that had
// already gone by with nothing saying why, which is what a suppressed
// firing always looks like (model.ScheduleSuppression, grain/task-493).

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// dueAt is baseTime's own next 09:00 plus half an hour -- past the
// NextRunAt everyDay gives a schedule created at baseTime, so the
// cadence has come round and whatever it did last time is what decides
// whether it fires.
var dueAt = time.Date(2026, 8, 29, 9, 30, 0, 0, time.UTC)

func scheduleNamed(t *testing.T, list []ui.Schedule, id string) ui.Schedule {
	t.Helper()
	for _, s := range list {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("schedule %s is not in the list", id)
	return ui.Schedule{}
}

// The plain kind, end to end: the task last night's firing filed is
// still open, so tonight's is suppressed, and the row says which task to
// close.
func TestListSchedulesSaysWhichOpenTaskIsHoldingADueSchedule(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "Tidy the backlog", Repo: "acme/widgets", Recurrence: everyDay,
	})
	if err != nil {
		t.Fatalf("creating a schedule: %v", err)
	}
	run, err := c.RunScheduleNow(ctx, sched.ID)
	if err != nil {
		t.Fatalf("firing it once: %v", err)
	}

	// Before the cadence comes round there is nothing to report: the
	// schedule is not firing because it is not due.
	list, err := c.ListSchedules(ctx)
	if err != nil {
		t.Fatalf("listing schedules: %v", err)
	}
	if got := scheduleNamed(t, list, sched.ID).Suppressed; got != nil {
		t.Errorf("suppressed = %+v before the next occurrence, want none", got)
	}

	c.Now = func() time.Time { return dueAt }
	list, err = c.ListSchedules(ctx)
	if err != nil {
		t.Fatalf("listing schedules: %v", err)
	}
	got := scheduleNamed(t, list, sched.ID).Suppressed
	if got == nil {
		t.Fatal("suppressed = nil on a schedule whose last task is still open")
	}
	if got.Reason != string(model.SuppressedByOpenTask) || got.TaskID != run.TaskID {
		t.Errorf("suppressed = %+v, want %q naming task %s",
			got, model.SuppressedByOpenTask, run.TaskID)
	}
	if got.Until != nil {
		t.Errorf("until = %s, want none: this one waits on a task, not on a clock", got.Until)
	}

	// Closed, and the schedule reads as firing again -- nothing was
	// stored, so nothing has to be cleared.
	closed := dueAt.Add(time.Hour)
	if err := store.ObserveField(ctx, run.TaskID, closed, func(o *model.Observation) {
		o.ClosedAt = &closed
	}); err != nil {
		t.Fatalf("closing the filed task: %v", err)
	}
	list, err = c.ListSchedules(ctx)
	if err != nil {
		t.Fatalf("listing schedules: %v", err)
	}
	if got := scheduleNamed(t, list, sched.ID).Suppressed; got != nil {
		t.Errorf("suppressed = %+v once the task was closed, want none", got)
	}
}

// A suite-backed schedule waits on its run rather than on a task, and
// says so: there is nothing to close, so no task is named.
func TestListSchedulesSaysASuiteRunIsHoldingADueSchedule(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	suite := suiteToSchedule(t, c, ctx, "Bug sweep")
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		SuiteID: suite.ID, Repo: "acme/widgets", Base: "main", Recurrence: everyDay,
	})
	if err != nil {
		t.Fatalf("creating a suite-backed schedule: %v", err)
	}
	if _, err := c.RunScheduleNow(ctx, sched.ID); err != nil {
		t.Fatalf("firing it once: %v", err)
	}

	c.Now = func() time.Time { return dueAt }
	list, err := c.ListSchedules(ctx)
	if err != nil {
		t.Fatalf("listing schedules: %v", err)
	}
	got := scheduleNamed(t, list, sched.ID).Suppressed
	if got == nil {
		t.Fatal("suppressed = nil on a schedule whose suite run is still going")
	}
	if got.Reason != string(model.SuppressedBySuiteRun) || got.TaskID != "" {
		t.Errorf("suppressed = %+v, want %q with no task named", got, model.SuppressedBySuiteRun)
	}
}

// A schedule that may overlap never suppresses anything, however long
// its last firing runs -- and the page must not claim otherwise.
func TestListSchedulesReportsNoSuppressionForAnOverlappingSchedule(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "Tidy the backlog", Repo: "acme/widgets", Recurrence: everyDay, Overlap: "allow",
	})
	if err != nil {
		t.Fatalf("creating a schedule: %v", err)
	}
	if _, err := c.RunScheduleNow(ctx, sched.ID); err != nil {
		t.Fatalf("firing it once: %v", err)
	}

	c.Now = func() time.Time { return dueAt }
	list, err := c.ListSchedules(ctx)
	if err != nil {
		t.Fatalf("listing schedules: %v", err)
	}
	if got := scheduleNamed(t, list, sched.ID).Suppressed; got != nil {
		t.Errorf("suppressed = %+v, want none: this schedule fires regardless", got)
	}
}

// And over the wire, which is the shape the page actually reads.
func TestSchedulesEndpointCarriesTheSuppression(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	srv := ui.NewServerWithClient(c)
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "Tidy the backlog", Repo: "acme/widgets", Recurrence: everyDay,
	})
	if err != nil {
		t.Fatalf("creating a schedule: %v", err)
	}
	run, err := c.RunScheduleNow(ctx, sched.ID)
	if err != nil {
		t.Fatalf("firing it once: %v", err)
	}
	c.Now = func() time.Time { return dueAt }

	body := decode[[]map[string]json.RawMessage](t, do(t, srv, http.MethodGet, "/api/schedules", ""))
	if len(body) != 1 {
		t.Fatalf("schedules = %d, want the one", len(body))
	}
	var got struct {
		Reason string `json:"reason"`
		TaskID string `json:"taskId"`
	}
	raw, ok := body[0]["suppressed"]
	if !ok {
		t.Fatal(`no "suppressed" on a schedule that is due and not firing`)
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decoding the suppression: %v", err)
	}
	if got.Reason != "task" || got.TaskID != run.TaskID {
		t.Errorf("suppressed = %+v, want the open task %s named", got, run.TaskID)
	}
}

// A skipped firing is recorded (model.ScheduleSkip, grain/task-3560), and
// the record is on the wire the page reads -- after the blocker closes
// as well as before, which is what Suppressed alone could never say.
func TestSchedulesEndpointCarriesTheSkipHistory(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	srv := ui.NewServerWithClient(c)
	created, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "Tidy the backlog", Repo: "acme/widgets", Recurrence: everyDay,
	})
	if err != nil {
		t.Fatalf("creating a schedule: %v", err)
	}
	run, err := c.RunScheduleNow(ctx, created.ID)
	if err != nil {
		t.Fatalf("firing it once: %v", err)
	}
	sched, err := store.GetSchedule(ctx, created.ID)
	if err != nil || sched == nil {
		t.Fatalf("reading the schedule back: %v", err)
	}
	// The cadence comes round with the first firing still open.
	firing, err := store.FireSchedule(ctx, *sched, dueAt, time.UTC, model.RepoRef{})
	if err != nil {
		t.Fatalf("firing on the cadence: %v", err)
	}
	if !firing.Skipped || !firing.SkipRecorded {
		t.Fatalf("firing = %+v, want a recorded skip", firing)
	}
	closed := dueAt.Add(time.Hour)
	if err := store.ObserveField(ctx, run.TaskID, closed, func(o *model.Observation) {
		o.ClosedAt = &closed
	}); err != nil {
		t.Fatalf("closing the filed task: %v", err)
	}
	c.Now = func() time.Time { return closed }

	body := decode[[]map[string]json.RawMessage](t, do(t, srv, http.MethodGet, "/api/schedules", ""))
	if len(body) != 1 {
		t.Fatalf("schedules = %d, want the one", len(body))
	}
	var skips []struct {
		Reason  string `json:"reason"`
		Summary string `json:"summary"`
		TaskID  string `json:"taskId"`
		Outcome string `json:"outcome"`
	}
	if err := json.Unmarshal(body[0]["skips"], &skips); err != nil {
		t.Fatalf("decoding the skips: %v", err)
	}
	want := "skipped at 2026-08-29 09:30 UTC: #" + run.TaskID + " from the previous firing is still open (queued)"
	if len(skips) != 1 || skips[0].Reason != "task" || skips[0].TaskID != run.TaskID ||
		skips[0].Summary != want || skips[0].Outcome != "" {
		t.Errorf("skips = %+v, want one held skip saying %q", skips, want)
	}
}

// A held occurrence is owed only while NextRunAt has not moved past it.
// Resuming keeps the firing time, so it stays held; a cadence edit retimes
// NextRunAt past it, so the edit drops it rather than leaving it saying it
// will fire (2026-10-15 §4 walkthrough).
func TestUpdateScheduleRetimeDropsAHeldSkip(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	created, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "Tidy the backlog", Repo: "acme/widgets", Recurrence: everyDay,
	})
	if err != nil {
		t.Fatalf("creating a schedule: %v", err)
	}
	paused, resumed := false, true
	if _, err := c.UpdateSchedule(ctx, created.ID, ui.UpdateScheduleRequest{Enabled: &paused}); err != nil {
		t.Fatalf("pausing: %v", err)
	}
	if _, err := store.RecordScheduleSkip(ctx, model.ScheduleSkip{ScheduleID: created.ID,
		Occurrence: created.NextRunAt, At: dueAt, Reason: model.SkippedPaused}); err != nil {
		t.Fatalf("recording the paused occurrence: %v", err)
	}
	c.Now = func() time.Time { return dueAt }
	outcome := func() model.ScheduleSkipOutcome {
		t.Helper()
		skips, err := store.ScheduleSkips(ctx, created.ID)
		if err != nil || len(skips) != 1 {
			t.Fatalf("skips = %+v (%v), want the one", skips, err)
		}
		return skips[0].Outcome
	}

	if _, err := c.UpdateSchedule(ctx, created.ID, ui.UpdateScheduleRequest{Enabled: &resumed}); err != nil {
		t.Fatalf("resuming: %v", err)
	}
	if got := outcome(); got != model.ScheduleSkipHeld {
		t.Fatalf("after resuming, outcome = %q, want still held", got)
	}
	weekly := ui.Recurrence{Kind: "weekly", Weekday: "monday", TimeOfDay: "09:00"}
	if _, err := c.UpdateSchedule(ctx, created.ID, ui.UpdateScheduleRequest{Recurrence: &weekly}); err != nil {
		t.Fatalf("retiming: %v", err)
	}
	if got := outcome(); got != model.ScheduleSkipDropped {
		t.Errorf("after a retime, outcome = %q, want dropped", got)
	}
}
