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

package postgres_test

// The schedule store, against a real embedded SQLite database --
// store_test.go's own doc comment on why gives the reasoning again here.

import (
	"slices"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func schedule(id string, nextRunAt time.Time) model.Schedule {
	return model.Schedule{
		ID:         id,
		Title:      "Nightly dependency bump",
		Body:       "Bump every dependency to its latest patch release.",
		Target:     model.RepoRef{Owner: "owner", Name: "payments-api"},
		Recurrence: model.Recurrence{Kind: model.RecurrenceEveryNHours, EveryNHours: 24},
		Enabled:    true,
		NextRunAt:  nextRunAt,
		CreatedAt:  now,
	}
}

func TestScheduleRoundTrips(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	want := schedule("sched-1", now)
	want.Base = "main"
	want.AutoMerge = true
	want.Reads = []model.RepoRef{{Owner: "owner", Name: "shared-lib"}}
	want.Writes = []model.WriteTarget{
		{Repo: model.RepoRef{Owner: "owner", Name: "deploy-config"}, Base: "deploy"},
	}
	want.Grants = []model.Grant{{Capability: "web-search", Via: model.GrantByLabel}}
	want.CustomFields = map[string][]string{"team": {"infra"}, "components": {"ui", "api"}}
	last := now.Add(-24 * time.Hour)
	want.LastRunAt = &last

	if err := store.PutSchedule(ctx, want); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := store.GetSchedule(ctx, "sched-1")
	if err != nil || got == nil {
		t.Fatalf("get: %v (nil=%v)", err, got == nil)
	}
	if got.Title != want.Title || got.Body != want.Body {
		t.Errorf("text did not survive: %+v", got)
	}
	if got.Target != want.Target {
		t.Errorf("target = %+v, want %+v", got.Target, want.Target)
	}
	if got.Base != "main" || !got.AutoMerge {
		t.Errorf("declared fields did not survive: %+v", got)
	}
	if got.Recurrence.Kind != model.RecurrenceEveryNHours || got.Recurrence.EveryNHours != 24 {
		t.Errorf("recurrence = %+v, want every 24 hours", got.Recurrence)
	}
	if len(got.Reads) != 1 || got.Reads[0] != want.Reads[0] {
		t.Errorf("reads = %+v, want %+v", got.Reads, want.Reads)
	}
	// Repo and branch both (grain/task-86) -- schedule_write carries the
	// base for the same reason task_write does.
	if len(got.Writes) != 1 || got.Writes[0] != want.Writes[0] {
		t.Errorf("writes = %+v, want %+v", got.Writes, want.Writes)
	}
	if len(got.Grants) != 1 || got.Grants[0].Capability != "web-search" {
		t.Errorf("grants = %+v, want web-search", got.Grants)
	}
	// The values every firing carries for this deployment's own declared
	// fields (grain/task-105), on their own table beside the reads and
	// the grants.
	if !slices.Equal(got.CustomFields["team"], []string{"infra"}) {
		t.Errorf("customFields = %v, want the schedule's own", got.CustomFields)
	}
	// A field declared to hold a set carries all of them, a row per
	// value, and reads back in one order (CustomField.Multiple): the
	// value is in schedule_custom_field's primary key for exactly the
	// reason it is in task_custom_field's.
	if !slices.Equal(got.CustomFields["components"], []string{"api", "ui"}) {
		t.Errorf("customFields = %v, want the whole set, sorted", got.CustomFields)
	}
	// And rewriting the schedule replaces them rather than adding to
	// them, the same as every other child table here: a cleared value
	// leaves no row behind.
	want.CustomFields = map[string][]string{"team": {"platform"}}
	if err := store.PutSchedule(ctx, want); err != nil {
		t.Fatalf("re-put: %v", err)
	}
	if got, err = store.GetSchedule(ctx, "sched-1"); err != nil || got == nil {
		t.Fatalf("re-get: %v (nil=%v)", err, got == nil)
	}
	if len(got.CustomFields) != 1 || !slices.Equal(got.CustomFields["team"], []string{"platform"}) {
		t.Errorf("customFields = %v after a rewrite, want only the new value", got.CustomFields)
	}
	if !got.Enabled {
		t.Errorf("enabled did not survive")
	}
	if !got.NextRunAt.Equal(now) {
		t.Errorf("nextRunAt = %v, want %v", got.NextRunAt, now)
	}
	if got.LastRunAt == nil || !got.LastRunAt.Equal(last) {
		t.Errorf("lastRunAt = %v, want %v", got.LastRunAt, last)
	}
	if !got.CreatedAt.Equal(now) {
		t.Errorf("createdAt = %v, want %v", got.CreatedAt, now)
	}
}

// Overlap is what a schedule does when it comes due with its own
// previous firing still unfinished. It round-trips like any other
// declared field, and a schedule that never sets it comes back as
// model.OverlapSkip -- the zero value written down as the policy it
// means, so a row never says "" and leaves a reader to guess.
func TestScheduleOverlapRoundTripsAndDefaultsToSkip(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutSchedule(ctx, schedule("sched-1", now)); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := store.GetSchedule(ctx, "sched-1")
	if err != nil || got == nil {
		t.Fatalf("get: %v (nil=%v)", err, got == nil)
	}
	if got.Overlap != model.OverlapSkip {
		t.Errorf("overlap = %q for a schedule that set none, want %q", got.Overlap, model.OverlapSkip)
	}
	if got.Overlap.Allows() {
		t.Error("want the default policy to suppress a firing whose predecessor is still going")
	}

	overlapping := schedule("sched-2", now)
	overlapping.Overlap = model.OverlapAllow
	if err := store.PutSchedule(ctx, overlapping); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err = store.GetSchedule(ctx, "sched-2")
	if err != nil || got == nil {
		t.Fatalf("get: %v (nil=%v)", err, got == nil)
	}
	if got.Overlap != model.OverlapAllow || !got.Overlap.Allows() {
		t.Errorf("overlap = %q, want %q", got.Overlap, model.OverlapAllow)
	}
}

// A schedule that runs a suite instead of filing a task: SuiteID is
// what says so, and SchedulesUsingSuite is what reads it back for
// ui.Client.DeleteSuite's own "not out from under a schedule still
// running it" guard.
func TestSuiteBackedScheduleRoundTrips(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	suiteID := "suite-1"
	want := schedule("sched-1", now)
	want.Title = "nightly"
	want.Base = "main"
	want.SuiteID = &suiteID

	if err := store.PutSchedule(ctx, want); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := store.GetSchedule(ctx, "sched-1")
	if err != nil || got == nil {
		t.Fatalf("get: %v (nil=%v)", err, got == nil)
	}
	if got.SuiteID == nil || *got.SuiteID != suiteID {
		t.Fatalf("suiteId = %v, want %q", got.SuiteID, suiteID)
	}
	if got.TemplateID != nil {
		t.Errorf("templateId = %v, want nil: a schedule fires a suite or a task, never both", got.TemplateID)
	}

	using, err := store.SchedulesUsingSuite(ctx, suiteID)
	if err != nil {
		t.Fatalf("SchedulesUsingSuite: %v", err)
	}
	if len(using) != 1 || using[0].ID != "sched-1" {
		t.Fatalf("schedules using %s = %+v, want just sched-1", suiteID, using)
	}
	other, err := store.SchedulesUsingSuite(ctx, "suite-nobody-uses")
	if err != nil {
		t.Fatalf("SchedulesUsingSuite: %v", err)
	}
	if len(other) != 0 {
		t.Errorf("schedules using an unreferenced suite = %+v, want none", other)
	}
}

func TestScheduleRoundTripsEveryRecurrenceKind(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	cases := []model.Recurrence{
		{Kind: model.RecurrenceDaily, TimeOfDay: 9 * 60},
		{Kind: model.RecurrenceWeekly, TimeOfDay: 14*60 + 30, Weekday: time.Monday},
		{Kind: model.RecurrenceMonthly, TimeOfDay: 0, DayOfMonth: 31},
	}
	for i, want := range cases {
		sched := schedule("sched-"+string(rune('a'+i)), now)
		sched.Recurrence = want
		if err := store.PutSchedule(ctx, sched); err != nil {
			t.Fatalf("put %+v: %v", want, err)
		}
		got, err := store.GetSchedule(ctx, sched.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Recurrence != want {
			t.Errorf("recurrence = %+v, want %+v", got.Recurrence, want)
		}
	}
}

func TestGetScheduleReturnsNilWhenAbsent(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	got, err := store.GetSchedule(ctx, "nope")
	if err != nil || got != nil {
		t.Fatalf("want (nil, nil), got (%v, %v)", got, err)
	}
}

func TestNewScheduleIDsAreDistinctFromTaskIDs(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	taskID, err := store.NewTaskID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	schedID, err := store.NewScheduleID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if taskID == schedID {
		t.Fatalf("task id %q and schedule id %q collided", taskID, schedID)
	}
	if schedID[:6] != "sched-" {
		t.Errorf("schedule id = %q, want a sched- prefix", schedID)
	}
}

func TestListSchedulesReturnsNewestFirst(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	earlier := schedule("sched-1", now)
	earlier.CreatedAt = now.Add(-time.Hour)
	later := schedule("sched-2", now)
	later.CreatedAt = now
	for _, s := range []model.Schedule{earlier, later} {
		if err := store.PutSchedule(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.ListSchedules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "sched-2" || got[1].ID != "sched-1" {
		t.Fatalf("list = %+v, want [sched-2, sched-1]", got)
	}
}

func TestDueSchedulesFiltersOnEnabledAndNextRunAt(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	due := schedule("sched-due", now.Add(-time.Minute))
	notYet := schedule("sched-not-yet", now.Add(time.Hour))
	paused := schedule("sched-paused", now.Add(-time.Minute))
	paused.Enabled = false
	for _, s := range []model.Schedule{due, notYet, paused} {
		if err := store.PutSchedule(ctx, s); err != nil {
			t.Fatal(err)
		}
	}

	got, err := store.DueSchedules(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "sched-due" {
		t.Fatalf("due = %+v, want only sched-due", got)
	}
}

func TestUpdateScheduleAppliesAndPersists(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutSchedule(ctx, schedule("sched-1", now)); err != nil {
		t.Fatal(err)
	}
	next := now.Add(24 * time.Hour)
	err := store.UpdateSchedule(ctx, "sched-1", func(s *model.Schedule) error {
		s.Enabled = false
		s.NextRunAt = next
		s.LastRunAt = &now
		return nil
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err := store.GetSchedule(ctx, "sched-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled {
		t.Errorf("enabled = true, want false after update")
	}
	if !got.NextRunAt.Equal(next) {
		t.Errorf("nextRunAt = %v, want %v", got.NextRunAt, next)
	}
	if got.LastRunAt == nil || !got.LastRunAt.Equal(now) {
		t.Errorf("lastRunAt = %v, want %v", got.LastRunAt, now)
	}
}

func TestUpdateScheduleOnAnUnknownIDErrors(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	err := store.UpdateSchedule(ctx, "nope", func(s *model.Schedule) error { return nil })
	if err == nil {
		t.Fatal("want an error updating an unknown schedule, got nil")
	}
}

func TestDeleteScheduleRemovesIt(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutSchedule(ctx, schedule("sched-1", now)); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteSchedule(ctx, "sched-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got, err := store.GetSchedule(ctx, "sched-1")
	if err != nil || got != nil {
		t.Fatalf("want (nil, nil) after delete, got (%v, %v)", got, err)
	}
}

func TestHasOpenTaskWithTag(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	tagged := task("t1", true)
	tagged.Tags = []string{"schedule:sched-1"}
	if err := store.PutSchedule(ctx, schedule("sched-1", now)); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTask(ctx, tagged); err != nil {
		t.Fatal(err)
	}

	open, err := store.HasOpenTaskWithTag(ctx, "schedule:sched-1")
	if err != nil {
		t.Fatal(err)
	}
	if !open {
		t.Fatal("want an open task tagged schedule:sched-1, got none")
	}

	if err := store.Observe(ctx, model.Observation{TaskID: "t1", ClosedAt: &now}); err != nil {
		t.Fatal(err)
	}
	open, err = store.HasOpenTaskWithTag(ctx, "schedule:sched-1")
	if err != nil {
		t.Fatal(err)
	}
	if open {
		t.Fatal("want no open task tagged schedule:sched-1 once it is closed")
	}

	open, err = store.HasOpenTaskWithTag(ctx, "schedule:unknown")
	if err != nil {
		t.Fatal(err)
	}
	if open {
		t.Fatal("want false for a tag nothing carries")
	}
}
