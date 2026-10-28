package ui_test

// client_test.go's own doc comment gives the reasoning for testing
// against a real embedded store rather than a fake.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

var everyDay = ui.Recurrence{Kind: "daily", TimeOfDay: "09:00"}

func TestCreateScheduleFilesAnEnabledScheduleDueAtItsFirstOccurrence(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "Nightly dependency bump", Repo: "acme/widgets",
		Recurrence: ui.Recurrence{Kind: "everyNHours", EveryNHours: 24},
	})
	if err != nil {
		t.Fatalf("creating a schedule: %v", err)
	}
	if sched.ID == "" {
		t.Fatal("want a non-empty id")
	}
	if !sched.Enabled {
		t.Error("want a freshly created schedule enabled by default")
	}
	if sched.Repo != "acme/widgets" {
		t.Errorf("repo = %q, want acme/widgets", sched.Repo)
	}
	if sched.Recurrence.Kind != "everyNHours" || sched.Recurrence.EveryNHours != 24 {
		t.Errorf("recurrence = %+v, want every 24 hours", sched.Recurrence)
	}
	want := baseTime.Add(24 * time.Hour)
	if !sched.NextRunAt.Equal(want) {
		t.Errorf("nextRunAt = %v, want %v (its own cadence, not the moment it was created)",
			sched.NextRunAt, want)
	}
	if sched.LastRunAt != nil {
		t.Errorf("lastRunAt = %v, want nil: nothing has fired yet", sched.LastRunAt)
	}
}

// A calendar cadence is read against UTC by default at creation,
// exactly as orchestrator.reconcileSchedule reads it at every firing afterwards
// -- baseTime is 12:00 UTC, so "daily at 09:00" is due tomorrow at 09:00 UTC.
func TestCreateScheduleTimesTheFirstOccurrenceInTheDeploymentsZone(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "x", Repo: "acme/widgets", Recurrence: everyDay,
	})
	if err != nil {
		t.Fatalf("creating a schedule: %v", err)
	}
	loc := model.LoadLocation(model.DefaultTimeZone)
	want := time.Date(2026, 8, 29, 9, 0, 0, 0, loc)
	if !sched.NextRunAt.Equal(want) {
		t.Errorf("nextRunAt = %v, want %v (09:00 on the UTC clock)",
			sched.NextRunAt.UTC(), want.UTC())
	}
}

// The schedule must not have filed anything by the time CreateSchedule
// returns, nor be due for the cycle that runs next -- grain/task-61: a
// schedule created at four in the afternoon is not a request to run
// something at four in the afternoon.
func TestCreateScheduleDoesNotComeDueOnTheNextCycle(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	if _, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "x", Repo: "acme/widgets", Recurrence: everyDay,
	}); err != nil {
		t.Fatalf("creating a schedule: %v", err)
	}
	due, err := store.DueSchedules(ctx, baseTime)
	if err != nil {
		t.Fatalf("listing due schedules: %v", err)
	}
	if len(due) != 0 {
		t.Errorf("due schedules = %d, want none: a fresh schedule waits for its first occurrence", len(due))
	}
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatalf("listing tasks: %v", err)
	}
	if len(tasks) != 0 {
		t.Errorf("tasks = %d, want none filed by creating a schedule", len(tasks))
	}
}

func TestCreateScheduleAcceptsDailyWeeklyAndMonthlyRecurrences(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	cases := []ui.Recurrence{
		{Kind: "daily", TimeOfDay: "09:00"},
		{Kind: "weekly", TimeOfDay: "14:30", Weekday: "monday"},
		{Kind: "monthly", TimeOfDay: "00:00", DayOfMonth: 31},
	}
	for _, r := range cases {
		sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
			Title: "x", Repo: "acme/widgets", Recurrence: r,
		})
		if err != nil {
			t.Fatalf("creating a %s schedule: %v", r.Kind, err)
		}
		if sched.Recurrence != r {
			t.Errorf("recurrence = %+v, want %+v", sched.Recurrence, r)
		}
	}
}

func TestCreateScheduleRejectsAMissingTitle(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	_, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{Repo: "acme/widgets", Recurrence: everyDay})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
}

func TestCreateScheduleRejectsAMissingRepo(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	_, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{Title: "x", Recurrence: everyDay})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
}

func TestCreateScheduleRejectsAnUnrecognizedRecurrenceKind(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	_, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "x", Repo: "acme/widgets", Recurrence: ui.Recurrence{Kind: "nope"},
	})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
}

func TestCreateScheduleRejectsANonPositiveEveryNHours(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	_, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "x", Repo: "acme/widgets", Recurrence: ui.Recurrence{Kind: "everyNHours", EveryNHours: 0},
	})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
}

// A minutes cadence comes back as it was sent, and its first occurrence
// is that many minutes from creation -- counted from the moment, not
// aligned to the clock.
func TestCreateScheduleAcceptsAnEveryNMinutesRecurrence(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	now := baseTime.Add(7 * time.Minute)
	c.Now = func() time.Time { return now }
	r := ui.Recurrence{Kind: "everyNMinutes", EveryNMinutes: 30}
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "Release manager", Repo: "acme/widgets", Recurrence: r,
	})
	if err != nil {
		t.Fatalf("creating an everyNMinutes schedule: %v", err)
	}
	if sched.Recurrence != r {
		t.Errorf("recurrence = %+v, want %+v", sched.Recurrence, r)
	}
	if want := now.Add(30 * time.Minute); !sched.NextRunAt.Equal(want) {
		t.Errorf("nextRunAt = %v, want %v, thirty minutes after it was created", sched.NextRunAt, want)
	}
}

// The ten-minute floor: nobody gets a sandbox every minute, whether by
// creating such a schedule or by editing one down to it.
func TestScheduleRejectsAnEveryNMinutesBelowTheFloor(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	for _, n := range []int{0, -1, 1, model.MinEveryNMinutes - 1} {
		_, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
			Title: "x", Repo: "acme/widgets", Recurrence: ui.Recurrence{Kind: "everyNMinutes", EveryNMinutes: n},
		})
		var ve *ui.ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("everyNMinutes %d: error = %v, want a ValidationError", n, err)
		}
	}

	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "x", Repo: "acme/widgets", Recurrence: ui.Recurrence{Kind: "everyNMinutes", EveryNMinutes: model.MinEveryNMinutes},
	})
	if err != nil {
		t.Fatalf("creating a schedule at the floor: %v", err)
	}
	every := ui.Recurrence{Kind: "everyNMinutes", EveryNMinutes: 1}
	_, err = c.UpdateSchedule(ctx, sched.ID, ui.UpdateScheduleRequest{Recurrence: &every})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Errorf("editing down to every minute: error = %v, want a ValidationError", err)
	}
}

func TestCreateScheduleRejectsAnUnparseableTimeOfDay(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	_, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "x", Repo: "acme/widgets", Recurrence: ui.Recurrence{Kind: "daily", TimeOfDay: "nope"},
	})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
}

func TestCreateScheduleRejectsAnUnknownWeekday(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	_, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "x", Repo: "acme/widgets",
		Recurrence: ui.Recurrence{Kind: "weekly", TimeOfDay: "09:00", Weekday: "someday"},
	})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
}

func TestCreateScheduleRejectsADayOfMonthOutOfRange(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	_, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "x", Repo: "acme/widgets",
		Recurrence: ui.Recurrence{Kind: "monthly", TimeOfDay: "09:00", DayOfMonth: 32},
	})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
}

func TestCreateScheduleAcceptsCapabilitiesAndReads(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "x", Repo: "acme/widgets", Recurrence: everyDay,
		Capabilities: []string{"gemini-key"},
		Reads:        []string{"acme/shared-lib"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sched.Capabilities) != 1 || sched.Capabilities[0] != "gemini-key" {
		t.Errorf("capabilities = %v, want [gemini-key]", sched.Capabilities)
	}
	if len(sched.Reads) != 1 || sched.Reads[0] != "acme/shared-lib" {
		t.Errorf("reads = %v, want [acme/shared-lib]", sched.Reads)
	}
}

// grain/task-86: a schedule declares the extra writeable repos every
// task it files carries, with the branch for each -- so a recurring job
// that spans two repos is written down once rather than filed by hand
// every time.
func TestCreateScheduleAcceptsExtraWriteTargets(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "x", Repo: "acme/widgets", Recurrence: everyDay,
		Writes: []ui.WriteTarget{{Repo: "acme/widgets-state", Base: "deploy"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sched.Writes) != 1 || sched.Writes[0].Repo != "acme/widgets-state" ||
		sched.Writes[0].Base != "deploy" {
		t.Fatalf("writes = %+v, want acme/widgets-state on deploy", sched.Writes)
	}
	stored, err := store.GetSchedule(ctx, sched.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Writes) != 1 || stored.Writes[0].Base != "deploy" {
		t.Fatalf("stored writes = %+v, want the branch as well as the repo", stored.Writes)
	}
}

// Refused where the schedule is saved, for ui.parseWrites' own reason:
// the alternative is a firing nobody is watching failing on it.
func TestCreateScheduleRefusesAWriteTargetOffTheAllowlist(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	setDefaultTeamTargetRepos(t, store, ctx, []string{"acme/widgets", "acme/gadgets"})

	_, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "x", Repo: "acme/widgets", Recurrence: everyDay,
		Writes: []ui.WriteTarget{{Repo: "acme/elsewhere"}},
	})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
}

// A schedule's own repo is asked the same question its write targets
// are, and refused the same way (ui.refuseOffAllowlist): every firing
// files a task straight into the store, already approved and
// dispatchable, and nothing on that path asks again (grain/task-336).
func TestCreateScheduleRefusesATargetOffTheAllowlist(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	setDefaultTeamTargetRepos(t, store, ctx, []string{"acme/widgets"})

	_, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "nightly", Repo: "third/repo", Recurrence: everyDay,
	})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
	if _, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "nightly", Repo: "acme/widgets", Recurrence: everyDay,
	}); err != nil {
		t.Fatalf("saving a schedule whose repo is on the allowlist: %v", err)
	}
}

// Moving a schedule that already fires into an allowed repo onto one the
// list does not name is the schedule-shaped version of retargeting a
// task, and refused for the same reason.
func TestUpdateScheduleRefusesATargetOffTheAllowlist(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	setDefaultTeamTargetRepos(t, store, ctx, []string{"acme/widgets"})
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "nightly", Repo: "acme/widgets", Recurrence: everyDay,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = c.UpdateSchedule(ctx, sched.ID, ui.UpdateScheduleRequest{Repo: ptr("third/repo")})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
	list, err := c.ListSchedules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Repo != "acme/widgets" {
		t.Fatalf("schedules = %+v, want the one schedule left where it was", list)
	}

	// A schedule whose repo has since dropped off the list is not frozen
	// by it: disabling one is exactly what an operator who narrowed the
	// list wants to do, and a save that resends the repo it already has
	// is not a move onto it.
	setDefaultTeamTargetRepos(t, store, ctx, []string{"acme/gadgets"})
	disabled, err := c.UpdateSchedule(ctx, sched.ID, ui.UpdateScheduleRequest{
		Repo: ptr("acme/widgets"), Enabled: ptr(false),
	})
	if err != nil {
		t.Fatalf("disabling a schedule whose repo is no longer on the list: %v", err)
	}
	if disabled.Enabled {
		t.Fatal("want the schedule disabled")
	}
}

func TestUpdateScheduleReplacesWriteTargets(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "x", Repo: "acme/widgets", Recurrence: everyDay,
		Writes: []ui.WriteTarget{{Repo: "acme/docs"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	writes := []ui.WriteTarget{{Repo: "acme/widgets-state", Base: "deploy"}}
	updated, err := c.UpdateSchedule(ctx, sched.ID, ui.UpdateScheduleRequest{Writes: &writes})
	if err != nil {
		t.Fatalf("updating: %v", err)
	}
	if len(updated.Writes) != 1 || updated.Writes[0].Repo != "acme/widgets-state" {
		t.Fatalf("writes = %+v, want only the new one", updated.Writes)
	}
}

func TestCreateScheduleRejectsAnUnknownCapability(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	_, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "x", Repo: "acme/widgets", Recurrence: everyDay,
		Capabilities: []string{"not-a-real-capability"},
	})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
}

func TestCreateScheduleCanBeFiledDisabled(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	disabled := false
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "x", Repo: "acme/widgets", Recurrence: everyDay, Enabled: &disabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sched.Enabled {
		t.Error("want the schedule filed paused")
	}
}

func TestListSchedulesReturnsNewestFirst(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	first, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{Title: "first", Repo: "acme/widgets", Recurrence: everyDay})
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "second", Repo: "acme/widgets",
		Recurrence: ui.Recurrence{Kind: "everyNHours", EveryNHours: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	list, err := c.ListSchedules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != second.ID || list[1].ID != first.ID {
		t.Fatalf("list = %+v, want [%s, %s]", list, second.ID, first.ID)
	}
}

func TestUpdateScheduleAppliesOnlyGivenFields(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "old title", Repo: "acme/widgets", Recurrence: everyDay,
	})
	if err != nil {
		t.Fatal(err)
	}
	newTitle := "new title"
	updated, err := c.UpdateSchedule(ctx, sched.ID, ui.UpdateScheduleRequest{Title: &newTitle})
	if err != nil {
		t.Fatalf("updating: %v", err)
	}
	if updated.Title != "new title" {
		t.Errorf("title = %q, want %q", updated.Title, "new title")
	}
	if updated.Repo != "acme/widgets" {
		t.Errorf("repo = %q, want it left alone", updated.Repo)
	}
	if updated.Recurrence != everyDay {
		t.Errorf("recurrence = %+v, want it left alone at %+v", updated.Recurrence, everyDay)
	}
}

func TestUpdateScheduleReplacesRecurrenceCapabilitiesAndReads(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{Title: "x", Repo: "acme/widgets", Recurrence: everyDay})
	if err != nil {
		t.Fatal(err)
	}
	weekly := ui.Recurrence{Kind: "weekly", TimeOfDay: "08:00", Weekday: "friday"}
	capabilities := []string{"gemini-key"}
	reads := []string{"acme/shared-lib"}
	updated, err := c.UpdateSchedule(ctx, sched.ID, ui.UpdateScheduleRequest{
		Recurrence:   &weekly,
		Capabilities: &capabilities,
		Reads:        &reads,
	})
	if err != nil {
		t.Fatalf("updating: %v", err)
	}
	if updated.Recurrence != weekly {
		t.Errorf("recurrence = %+v, want %+v", updated.Recurrence, weekly)
	}
	if len(updated.Capabilities) != 1 || updated.Capabilities[0] != "gemini-key" {
		t.Errorf("capabilities = %v, want [gemini-key]", updated.Capabilities)
	}
	if len(updated.Reads) != 1 || updated.Reads[0] != "acme/shared-lib" {
		t.Errorf("reads = %v, want [acme/shared-lib]", updated.Reads)
	}
}

// Editing the cadence retimes the next firing to the new cadence's own
// first occurrence -- otherwise a monthly schedule moved to daily would
// keep its next firing a month out and only start running daily after
// that (ui.UpdateSchedule's own doc comment).
func TestUpdateScheduleRetimesTheNextRunWhenTheCadenceChanges(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "x", Repo: "acme/widgets",
		Recurrence: ui.Recurrence{Kind: "monthly", TimeOfDay: "09:00", DayOfMonth: 15},
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := c.UpdateSchedule(ctx, sched.ID, ui.UpdateScheduleRequest{Recurrence: &everyDay})
	if err != nil {
		t.Fatalf("updating: %v", err)
	}
	loc := model.LoadLocation(model.DefaultTimeZone)
	want := time.Date(2026, 8, 29, 9, 0, 0, 0, loc)
	if !updated.NextRunAt.Equal(want) {
		t.Errorf("nextRunAt = %v, want %v (the new cadence's first occurrence)",
			updated.NextRunAt.UTC(), want.UTC())
	}
}

// An edit that resends the cadence a schedule already has -- which is
// every save from a form that submits the whole schedule -- is not a
// cadence edit, and must not push the firing it is already waiting for
// any further out.
func TestUpdateScheduleKeepsTheNextRunWhenTheCadenceIsUnchanged(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "x", Repo: "acme/widgets", Recurrence: everyDay,
	})
	if err != nil {
		t.Fatal(err)
	}
	newTitle := "y"
	updated, err := c.UpdateSchedule(ctx, sched.ID, ui.UpdateScheduleRequest{
		Title: &newTitle, Recurrence: &everyDay,
	})
	if err != nil {
		t.Fatalf("updating: %v", err)
	}
	if !updated.NextRunAt.Equal(sched.NextRunAt) {
		t.Errorf("nextRunAt = %v, want it left at %v", updated.NextRunAt, sched.NextRunAt)
	}
}

// A schedule nobody has said anything to about overlapping skips a
// firing whose predecessor is still going -- the default, and what every
// schedule did before the field existed. The other answer is one word on
// the request, and it survives a round trip through the store.
func TestCreateScheduleDefaultsToSkippingAnOverlappingFiring(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "x", Repo: "acme/widgets", Recurrence: everyDay,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sched.Overlap != "skip" {
		t.Errorf("overlap = %q, want skip by default", sched.Overlap)
	}

	overlapping, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "y", Repo: "acme/widgets", Recurrence: everyDay, Overlap: "allow",
	})
	if err != nil {
		t.Fatal(err)
	}
	if overlapping.Overlap != "allow" {
		t.Errorf("overlap = %q, want allow", overlapping.Overlap)
	}
	list, err := c.ListSchedules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range list {
		want := "skip"
		if s.ID == overlapping.ID {
			want = "allow"
		}
		if s.Overlap != want {
			t.Errorf("listed %s with overlap %q, want %q", s.ID, s.Overlap, want)
		}
	}
}

func TestCreateScheduleRejectsAnUnknownOverlapPolicy(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	_, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "x", Repo: "acme/widgets", Recurrence: everyDay, Overlap: "queue",
	})
	if err == nil {
		t.Fatal("want an error for an unrecognized overlap policy")
	}
}

func TestUpdateScheduleChangesTheOverlapPolicy(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "x", Repo: "acme/widgets", Recurrence: everyDay,
	})
	if err != nil {
		t.Fatal(err)
	}
	allow := "allow"
	updated, err := c.UpdateSchedule(ctx, sched.ID, ui.UpdateScheduleRequest{Overlap: &allow})
	if err != nil {
		t.Fatalf("updating: %v", err)
	}
	if updated.Overlap != "allow" {
		t.Errorf("overlap = %q, want allow", updated.Overlap)
	}
	// And back again -- nothing about the switch is one-way.
	skip := "skip"
	back, err := c.UpdateSchedule(ctx, sched.ID, ui.UpdateScheduleRequest{Overlap: &skip})
	if err != nil {
		t.Fatalf("updating: %v", err)
	}
	if back.Overlap != "skip" {
		t.Errorf("overlap = %q, want skip", back.Overlap)
	}

	// Left out of a later request, it stays where it was put: nil is
	// "leave this one alone", the same as every other field here.
	title := "renamed"
	if again, err := c.UpdateSchedule(ctx, sched.ID, ui.UpdateScheduleRequest{Title: &title}); err != nil {
		t.Fatal(err)
	} else if again.Overlap != "skip" {
		t.Errorf("overlap = %q after an unrelated edit, want it left alone", again.Overlap)
	}
}

func TestUpdateScheduleRejectsABadOverlapPolicy(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "x", Repo: "acme/widgets", Recurrence: everyDay,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "queue"} {
		if _, err := c.UpdateSchedule(ctx, sched.ID, ui.UpdateScheduleRequest{Overlap: &bad}); err == nil {
			t.Errorf("overlap %q: want an error, got nil", bad)
		}
	}
}

func TestUpdateScheduleCanPauseAndResume(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{Title: "x", Repo: "acme/widgets", Recurrence: everyDay})
	if err != nil {
		t.Fatal(err)
	}
	disabled := false
	paused, err := c.UpdateSchedule(ctx, sched.ID, ui.UpdateScheduleRequest{Enabled: &disabled})
	if err != nil {
		t.Fatal(err)
	}
	if paused.Enabled {
		t.Fatal("want it paused")
	}
	enabled := true
	resumed, err := c.UpdateSchedule(ctx, sched.ID, ui.UpdateScheduleRequest{Enabled: &enabled})
	if err != nil {
		t.Fatal(err)
	}
	if !resumed.Enabled {
		t.Fatal("want it resumed")
	}
	if !resumed.NextRunAt.Equal(sched.NextRunAt) {
		t.Errorf("nextRunAt = %v, want it left at %v across a pause and resume",
			resumed.NextRunAt, sched.NextRunAt)
	}
}

func TestUpdateScheduleOnAnUnknownIDIsNotFound(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	_, err := c.UpdateSchedule(ctx, "nope", ui.UpdateScheduleRequest{})
	if err == nil {
		t.Fatal("want an error")
	}
}

func TestDeleteScheduleRemovesIt(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{Title: "x", Repo: "acme/widgets", Recurrence: everyDay})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteSchedule(ctx, sched.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	list, err := c.ListSchedules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("list = %+v, want empty after delete", list)
	}
}

func TestDeleteScheduleOnAnUnknownIDIsNotFound(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	err := c.DeleteSchedule(ctx, "nope")
	if err == nil {
		t.Fatal("want an error")
	}
}

// TestCreateScheduleFromATemplateCopiesItsContent is
// bwsalmon/agents#516's whole point: a schedule created with TemplateID
// needs no Title of its own, and reads it off the template instead.
// Repo and Base are not among the fields an ordinary, unbound template
// supplies, so they come from this same request -- the bound case is
// TestCreateScheduleFromABoundTemplateTakesItsRepoAndBranch below.
func TestCreateScheduleFromATemplateCopiesItsContent(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "Dependency bump", Title: "Bump dependencies", Description: "Bump every dependency.",
		AutoMerge:    true,
		Capabilities: []string{"gemini-key"}, Reads: []string{"acme/shared-lib"},
		Writes: []ui.WriteTarget{{Repo: "acme/widgets-state", Base: "deploy"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		TemplateID: tmpl.ID, Repo: "acme/widgets", Base: "main", Recurrence: everyDay,
	})
	if err != nil {
		t.Fatalf("creating a template-backed schedule: %v", err)
	}
	if sched.TemplateID != tmpl.ID {
		t.Errorf("templateId = %q, want %q", sched.TemplateID, tmpl.ID)
	}
	if sched.TemplateName != "Dependency bump" {
		t.Errorf("templateName = %q, want %q", sched.TemplateName, "Dependency bump")
	}
	if sched.Title != "Bump dependencies" || sched.Description != "Bump every dependency." {
		t.Errorf("title/description = %q/%q, want the template's own", sched.Title, sched.Description)
	}
	if !sched.AutoMerge {
		t.Errorf("autoMerge = %v, want the template's own", sched.AutoMerge)
	}
	if sched.Repo != "acme/widgets" || sched.Base != "main" {
		t.Errorf("repo/base = %q/%q, want this request's own", sched.Repo, sched.Base)
	}
	if len(sched.Capabilities) != 1 || sched.Capabilities[0] != "gemini-key" {
		t.Errorf("capabilities = %v, want [gemini-key]", sched.Capabilities)
	}
	if len(sched.Reads) != 1 || sched.Reads[0] != "acme/shared-lib" {
		t.Errorf("reads = %v, want [acme/shared-lib]", sched.Reads)
	}
	// The template's extra write targets are content like the rest, and
	// are copied onto the schedule with them (grain/task-86).
	if len(sched.Writes) != 1 || sched.Writes[0].Repo != "acme/widgets-state" ||
		sched.Writes[0].Base != "deploy" {
		t.Errorf("writes = %+v, want the template's own", sched.Writes)
	}
}

// TestCreateScheduleFromATemplateFillsInFieldDefaults pins
// grain/task-1425: a template-backed schedule caches its template's
// title and body for display, and that cache is what a firing would
// file -- every input at its Default -- rather than the raw
// `{{name}}` text, both on create and after the sync a firing does.
func TestCreateScheduleFromATemplateFillsInFieldDefaults(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "Audit", Title: "Audit {{service}}", Description: "Audit the {{service}} service.",
		Fields: []ui.TemplateField{{Name: "service", Options: []string{"api", "web"}, Default: "api"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		TemplateID: tmpl.ID, Repo: "acme/widgets", Base: "main", Recurrence: everyDay,
	})
	if err != nil {
		t.Fatal(err)
	}
	check := func(when string) {
		t.Helper()
		list, err := c.ListSchedules(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 1 {
			t.Fatalf("%s: listed %d schedules, want 1", when, len(list))
		}
		if list[0].Title != "Audit api" || list[0].Description != "Audit the api service." {
			t.Errorf("%s: title/description = %q/%q, want the defaults filled in",
				when, list[0].Title, list[0].Description)
		}
	}
	if sched.Title != "Audit api" {
		t.Errorf("created title = %q, want %q", sched.Title, "Audit api")
	}
	check("after create")

	run, err := c.RunScheduleNow(ctx, sched.ID)
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.GetTask(ctx, run.TaskID)
	if err != nil || task == nil {
		t.Fatalf("filed task %q: %v, %v", run.TaskID, task, err)
	}
	if task.Title != "Audit api" {
		t.Errorf("filed task title = %q, want %q", task.Title, "Audit api")
	}
	check("after Run now")
}

func TestCreateScheduleRejectsAnUnknownTemplate(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	_, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{TemplateID: "nope", Repo: "acme/widgets", Recurrence: everyDay})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
}

// TestUpdateScheduleAttachToATemplateOverridesInlineContent checks
// UpdateScheduleRequest.TemplateID's non-empty case: attaching an
// ordinary, inline schedule to a template overwrites its content from
// that template outright -- except Repo and Base, which an unbound
// template does not carry and so are left exactly as they already are
// unless this same request also gives a new one (a bound one does
// overwrite them:
// TestUpdateScheduleAttachToABoundTemplateTakesItsRepoAndBranch).
func TestUpdateScheduleAttachToATemplateOverridesInlineContent(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "old title", Repo: "acme/widgets", Recurrence: everyDay,
	})
	if err != nil {
		t.Fatal(err)
	}
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "Dependency bump", Title: "Bump dependencies",
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := c.UpdateSchedule(ctx, sched.ID, ui.UpdateScheduleRequest{TemplateID: &tmpl.ID})
	if err != nil {
		t.Fatalf("attaching: %v", err)
	}
	if updated.TemplateID != tmpl.ID {
		t.Errorf("templateId = %q, want %q", updated.TemplateID, tmpl.ID)
	}
	if updated.Title != "Bump dependencies" {
		t.Errorf("title = %q, want the template's own", updated.Title)
	}
	if updated.Repo != "acme/widgets" {
		t.Errorf("repo = %q, want it left alone (an unbound template carries no target)", updated.Repo)
	}
}

// TestCreateScheduleFromABoundTemplateTakesItsRepoAndBranch is the
// binding half of grain/task-285: a template bound to a repo already
// answers what a schedule fired from it targets, so the schedule needs
// no repo of its own and takes the template's instead.
func TestCreateScheduleFromABoundTemplateTakesItsRepoAndBranch(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "Dependency bump", Title: "Bump dependencies",
		Repo: "acme/widgets", Base: "release",
	})
	if err != nil {
		t.Fatal(err)
	}
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		TemplateID: tmpl.ID, Recurrence: everyDay,
	})
	if err != nil {
		t.Fatalf("creating a schedule from a bound template with no repo of its own: %v", err)
	}
	if sched.Repo != "acme/widgets" || sched.Base != "release" {
		t.Errorf("repo/base = %q/%q, want the template's own binding", sched.Repo, sched.Base)
	}
}

// A binding wins over a repo the same request also named: it is the
// template saying its content belongs to one repo, so a schedule cannot
// quietly aim it at another.
func TestCreateScheduleFromABoundTemplateOverridesTheRequestsOwnRepo(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "x", Title: "x", Repo: "acme/widgets", Base: "release",
	})
	if err != nil {
		t.Fatal(err)
	}
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		TemplateID: tmpl.ID, Repo: "acme/gadgets", Base: "main", Recurrence: everyDay,
	})
	if err != nil {
		t.Fatalf("creating: %v", err)
	}
	if sched.Repo != "acme/widgets" || sched.Base != "release" {
		t.Errorf("repo/base = %q/%q, want the template's binding to win", sched.Repo, sched.Base)
	}
}

// A schedule that fires no bound template still needs a repo -- nothing
// else can say where its firings go.
func TestCreateScheduleStillRequiresARepoWithoutABinding(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{Name: "x", Title: "x"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.CreateSchedule(ctx, ui.CreateScheduleRequest{TemplateID: tmpl.ID, Recurrence: everyDay})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
}

func TestUpdateScheduleAttachToABoundTemplateTakesItsRepoAndBranch(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "old title", Repo: "acme/gadgets", Base: "main", Recurrence: everyDay,
	})
	if err != nil {
		t.Fatal(err)
	}
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "x", Title: "x", Repo: "acme/widgets", Base: "release",
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := c.UpdateSchedule(ctx, sched.ID, ui.UpdateScheduleRequest{TemplateID: &tmpl.ID})
	if err != nil {
		t.Fatalf("attaching: %v", err)
	}
	if updated.Repo != "acme/widgets" || updated.Base != "release" {
		t.Errorf("repo/base = %q/%q, want the template's own binding", updated.Repo, updated.Base)
	}
}

// The schedule editor hides the repo and branch boxes once a bound
// template decides them, and sends both blank. Each save of such a
// schedule -- a cadence change on one already firing it, or attaching
// one to an inline schedule -- must take that as "the binding says",
// not refuse it as a schedule with no target.
func TestUpdateScheduleOnABoundTemplateAcceptsTheBlankRepoTheEditorSends(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "x", Title: "x", Repo: "acme/widgets", Base: "release",
	})
	if err != nil {
		t.Fatal(err)
	}
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{TemplateID: tmpl.ID, Recurrence: everyDay})
	if err != nil {
		t.Fatal(err)
	}
	blank := ""
	daily := ui.Recurrence{Kind: "daily", TimeOfDay: "01:15"}
	updated, err := c.UpdateSchedule(ctx, sched.ID, ui.UpdateScheduleRequest{
		TemplateID: &tmpl.ID, Repo: &blank, Base: &blank, Recurrence: &daily,
	})
	if err != nil {
		t.Fatalf("retiming a schedule on a bound template: %v", err)
	}
	if updated.Recurrence.Kind != "daily" || updated.Repo != "acme/widgets" || updated.Base != "release" {
		t.Errorf("schedule = %+v, want it retimed and still on the binding", updated)
	}

	inline, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "inline", Repo: "acme/gadgets", Base: "main", Recurrence: everyDay,
	})
	if err != nil {
		t.Fatal(err)
	}
	attached, err := c.UpdateSchedule(ctx, inline.ID, ui.UpdateScheduleRequest{
		TemplateID: &tmpl.ID, Repo: &blank, Base: &blank,
	})
	if err != nil {
		t.Fatalf("attaching a bound template the way the editor does: %v", err)
	}
	if attached.Repo != "acme/widgets" || attached.Base != "release" {
		t.Errorf("repo/base = %q/%q, want the template's binding", attached.Repo, attached.Base)
	}

	// Detaching takes the binding away, so a blank repo is again a
	// schedule with nowhere to fire.
	_, err = c.UpdateSchedule(ctx, attached.ID, ui.UpdateScheduleRequest{
		TemplateID: &blank, Repo: &blank, Base: &blank,
	})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("detaching with a blank repo: error = %v, want a ValidationError", err)
	}
}

// TestUpdateScheduleDetachFromATemplateKeepsCurrentContent checks
// UpdateScheduleRequest.TemplateID's empty-string case: it clears
// TemplateID but leaves whatever content the schedule currently has
// (most recently synced from the template) in place as an independent
// copy, rather than blanking it out.
func TestUpdateScheduleDetachFromATemplateKeepsCurrentContent(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "Dependency bump", Title: "Bump dependencies",
	})
	if err != nil {
		t.Fatal(err)
	}
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		TemplateID: tmpl.ID, Repo: "acme/widgets", Recurrence: everyDay,
	})
	if err != nil {
		t.Fatal(err)
	}
	empty := ""
	updated, err := c.UpdateSchedule(ctx, sched.ID, ui.UpdateScheduleRequest{TemplateID: &empty})
	if err != nil {
		t.Fatalf("detaching: %v", err)
	}
	if updated.TemplateID != "" {
		t.Errorf("templateId = %q, want empty after detaching", updated.TemplateID)
	}
	if updated.Title != "Bump dependencies" || updated.Repo != "acme/widgets" {
		t.Errorf("title/repo = %q/%q, want the content already on the row kept", updated.Title, updated.Repo)
	}

	// Now independent: editing it directly works the ordinary way.
	newTitle := "renamed"
	edited, err := c.UpdateSchedule(ctx, sched.ID, ui.UpdateScheduleRequest{Title: &newTitle})
	if err != nil {
		t.Fatal(err)
	}
	if edited.Title != "renamed" {
		t.Errorf("title = %q, want %q", edited.Title, "renamed")
	}
}

func TestUpdateScheduleRejectsAnUnknownTemplate(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{Title: "x", Repo: "acme/widgets", Recurrence: everyDay})
	if err != nil {
		t.Fatal(err)
	}
	nope := "nope"
	_, err = c.UpdateSchedule(ctx, sched.ID, ui.UpdateScheduleRequest{TemplateID: &nope})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
}

// --- schedules that run a suite -------------------------------------

// suiteToSchedule is the setup every suite-backed schedule test below
// shares: a template, and a suite that runs it.
func suiteToSchedule(t *testing.T, c *ui.Client, ctx context.Context, name string) ui.Suite {
	t.Helper()
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{Name: name + " template", Title: "Find and fix a bug"})
	if err != nil {
		t.Fatalf("creating a template: %v", err)
	}
	suite, err := c.CreateSuite(ctx, ui.CreateSuiteRequest{
		Name: name, TemplateIDs: []string{tmpl.ID}, Mode: "until_clean", MaxPasses: 5,
	})
	if err != nil {
		t.Fatalf("creating a suite: %v", err)
	}
	return suite
}

// A suite-backed schedule needs no title or content of its own: the
// suite decides what runs, and its name stands in as the schedule's own
// display title.
func TestCreateScheduleFromASuite(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	suite := suiteToSchedule(t, c, ctx, "Bug sweep")

	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		SuiteID: suite.ID, Repo: "acme/widgets", Base: "main", Recurrence: everyDay,
	})
	if err != nil {
		t.Fatalf("creating a suite-backed schedule: %v", err)
	}
	if sched.SuiteID != suite.ID || sched.SuiteName != suite.Name {
		t.Errorf("suite = %q/%q, want %q/%q", sched.SuiteID, sched.SuiteName, suite.ID, suite.Name)
	}
	if sched.TemplateID != "" {
		t.Errorf("templateId = %q, want empty: a schedule fires a suite or a task, never both", sched.TemplateID)
	}
	if sched.Title != suite.Name {
		t.Errorf("title = %q, want the suite's own name %q", sched.Title, suite.Name)
	}
	if sched.Repo != "acme/widgets" || sched.Base != "main" {
		t.Errorf("target = %q@%q, want acme/widgets@main", sched.Repo, sched.Base)
	}

	// It comes back off the list the same way, suite name and all.
	list, err := c.ListSchedules(ctx)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if len(list) != 1 || list[0].SuiteID != suite.ID || list[0].SuiteName != suite.Name {
		t.Fatalf("listed %+v, want one schedule naming %s (%s)", list, suite.ID, suite.Name)
	}
}

func TestCreateScheduleRejectsASuiteAndATemplateTogether(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	suite := suiteToSchedule(t, c, ctx, "Bug sweep")
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{Name: "Bump", Title: "Bump dependencies"})
	if err != nil {
		t.Fatalf("creating a template: %v", err)
	}

	_, err = c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		SuiteID: suite.ID, TemplateID: tmpl.ID, Repo: "acme/widgets", Base: "main", Recurrence: everyDay,
	})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
}

// CreateSuiteRunRequest's own rule, applied to the schedule that starts
// such a run on a cadence: a suite run stacks its tasks against one named
// branch, so there is no default to fall back to.
func TestCreateScheduleFromASuiteRejectsAMissingBase(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	suite := suiteToSchedule(t, c, ctx, "Bug sweep")

	_, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		SuiteID: suite.ID, Repo: "acme/widgets", Recurrence: everyDay,
	})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
}

func TestCreateScheduleRejectsAnUnknownSuite(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	_, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		SuiteID: "suite-nope", Repo: "acme/widgets", Base: "main", Recurrence: everyDay,
	})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
}

// Which suite a schedule runs is editable; everything else about a
// suite-backed schedule that is its own (repo, branch, cadence, paused)
// still edits exactly as any other schedule's does.
func TestUpdateScheduleRepointsASuiteBackedScheduleAtAnotherSuite(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	first := suiteToSchedule(t, c, ctx, "Bug sweep")
	second := suiteToSchedule(t, c, ctx, "Dependency sweep")
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		SuiteID: first.ID, Repo: "acme/widgets", Base: "main", Recurrence: everyDay,
	})
	if err != nil {
		t.Fatalf("creating: %v", err)
	}

	paused := false
	updated, err := c.UpdateSchedule(ctx, sched.ID, ui.UpdateScheduleRequest{
		SuiteID: &second.ID, Enabled: &paused,
	})
	if err != nil {
		t.Fatalf("repointing: %v", err)
	}
	if updated.SuiteID != second.ID || updated.SuiteName != second.Name {
		t.Errorf("suite = %q/%q, want %q/%q", updated.SuiteID, updated.SuiteName, second.ID, second.Name)
	}
	if updated.Title != second.Name {
		t.Errorf("title = %q, want the new suite's own name %q", updated.Title, second.Name)
	}
	if updated.Enabled {
		t.Error("want the schedule paused")
	}
}

// What a schedule fires is fixed when it is created -- neither direction
// of "convert it into the other kind" is supported, since there is
// nothing sensible to carry across (model.Schedule.SuiteID's own doc
// comment).
func TestUpdateScheduleRefusesToChangeWhatAScheduleFires(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	suite := suiteToSchedule(t, c, ctx, "Bug sweep")
	suiteBacked, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		SuiteID: suite.ID, Repo: "acme/widgets", Base: "main", Recurrence: everyDay,
	})
	if err != nil {
		t.Fatalf("creating a suite-backed schedule: %v", err)
	}
	taskBacked, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "Nightly dependency bump", Repo: "acme/widgets", Recurrence: everyDay,
	})
	if err != nil {
		t.Fatalf("creating a task schedule: %v", err)
	}

	empty := ""
	var ve *ui.ValidationError
	if _, err := c.UpdateSchedule(ctx, suiteBacked.ID, ui.UpdateScheduleRequest{SuiteID: &empty}); !errors.As(err, &ve) {
		t.Errorf("detaching a suite: error = %v, want a ValidationError", err)
	}
	if _, err := c.UpdateSchedule(ctx, taskBacked.ID, ui.UpdateScheduleRequest{SuiteID: &suite.ID}); !errors.As(err, &ve) {
		t.Errorf("attaching a suite to a task schedule: error = %v, want a ValidationError", err)
	}

	// Still exactly as they were.
	list, err := c.ListSchedules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range list {
		if s.ID == suiteBacked.ID && s.SuiteID != suite.ID {
			t.Errorf("suite-backed schedule lost its suite: %+v", s)
		}
		if s.ID == taskBacked.ID && s.SuiteID != "" {
			t.Errorf("task schedule gained a suite: %+v", s)
		}
	}
}

func TestUpdateScheduleRejectsAnUnknownSuite(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	suite := suiteToSchedule(t, c, ctx, "Bug sweep")
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		SuiteID: suite.ID, Repo: "acme/widgets", Base: "main", Recurrence: everyDay,
	})
	if err != nil {
		t.Fatalf("creating: %v", err)
	}

	nope := "suite-nope"
	_, err = c.UpdateSchedule(ctx, sched.ID, ui.UpdateScheduleRequest{SuiteID: &nope})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
}

// A suite-backed schedule's base branch cannot be emptied, for the same
// reason it was required at creation.
func TestUpdateScheduleRejectsEmptyingASuiteBackedScheduleBase(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	suite := suiteToSchedule(t, c, ctx, "Bug sweep")
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		SuiteID: suite.ID, Repo: "acme/widgets", Base: "main", Recurrence: everyDay,
	})
	if err != nil {
		t.Fatalf("creating: %v", err)
	}

	empty := ""
	_, err = c.UpdateSchedule(ctx, sched.ID, ui.UpdateScheduleRequest{Base: &empty})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
}

// --- running a schedule now (grain/task-227) --------------------------

// The whole of what "Run now" is: the firing happens this instant, and
// the cadence is untouched by it -- the occurrence this schedule was
// already going to have still happens when it was going to.
func TestRunScheduleNowFilesATaskWithoutMovingTheCadence(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "Nightly dependency bump", Description: "bump them", Repo: "acme/widgets",
		Recurrence: everyDay,
	})
	if err != nil {
		t.Fatalf("creating a schedule: %v", err)
	}

	run, err := c.RunScheduleNow(ctx, sched.ID)
	if err != nil {
		t.Fatalf("running now: %v", err)
	}
	if run.TaskID == "" {
		t.Fatal("want the filed task's id back")
	}
	if run.SuiteRunID != 0 {
		t.Errorf("suiteRunId = %d, want 0: this schedule files a task", run.SuiteRunID)
	}
	if !run.Schedule.NextRunAt.Equal(sched.NextRunAt) {
		t.Errorf("nextRunAt = %v, want it left at %v: a run by hand is an extra firing, not a rescheduling",
			run.Schedule.NextRunAt, sched.NextRunAt)
	}
	if run.Schedule.LastRunAt == nil || !run.Schedule.LastRunAt.Equal(baseTime) {
		t.Errorf("lastRunAt = %v, want %v: it really did fire", run.Schedule.LastRunAt, baseTime)
	}

	task, err := store.GetTask(ctx, run.TaskID)
	if err != nil {
		t.Fatalf("reading the filed task: %v", err)
	}
	if task == nil {
		t.Fatalf("no task %s: RunScheduleNow named one it did not file", run.TaskID)
	}
	if task.Title != "Nightly dependency bump" || task.Body != "bump them" {
		t.Errorf("task = %q/%q, want the schedule's own content", task.Title, task.Body)
	}
	if task.Origin.Reason != model.ReasonSchedule || task.Origin.Attribution.Actor != model.SchedulePrincipal {
		t.Errorf("origin = %+v, want it attributed to the schedule principal", task.Origin)
	}
	if task.Approval == nil {
		t.Error("want the firing already approved, exactly as the cadence's own firing is")
	}
	if len(task.Tags) != 1 || task.Tags[0] != model.ScheduleFiringTag(sched.ID) {
		t.Errorf("tags = %v, want just %q", task.Tags, model.ScheduleFiringTag(sched.ID))
	}
}

// Pausing stops the cadence; it does not stop a human. The schedule is
// still paused afterwards -- one run is all that was asked for.
func TestRunScheduleNowFiresAPausedSchedule(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	paused := false
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "x", Repo: "acme/widgets", Recurrence: everyDay, Enabled: &paused,
	})
	if err != nil {
		t.Fatalf("creating a schedule: %v", err)
	}

	run, err := c.RunScheduleNow(ctx, sched.ID)
	if err != nil {
		t.Fatalf("running a paused schedule now: %v", err)
	}
	if run.TaskID == "" {
		t.Fatal("want a task filed even though the schedule is paused")
	}
	if run.Schedule.Enabled {
		t.Error("want the schedule still paused: one run by hand is not a resume")
	}
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatalf("listing tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Errorf("tasks = %d, want exactly the one firing", len(tasks))
	}
}

// The overlap policy is not walked round by the button: a schedule set
// to skip refuses while its previous firing is still open, and says so
// rather than doing nothing.
func TestRunScheduleNowIsRefusedWhileThePreviousFiringIsOpen(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "x", Repo: "acme/widgets", Recurrence: everyDay,
	})
	if err != nil {
		t.Fatalf("creating a schedule: %v", err)
	}
	if _, err := c.RunScheduleNow(ctx, sched.ID); err != nil {
		t.Fatalf("first run: %v", err)
	}

	if _, err := c.RunScheduleNow(ctx, sched.ID); err == nil {
		t.Fatal("second run: want an error while the first firing is still open")
	}
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatalf("listing tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Errorf("tasks = %d, want just the first firing", len(tasks))
	}
}

// The cadence answers a schedule whose repo has come off
// Config.TargetRepos by quietly not firing (model.Store's own
// suppressOffTargetRepos, grain/task-430): nobody is waiting on it, and
// the operator was told what the narrowing stranded at the save that did
// it. "Run now" is the case where somebody is waiting, so it says so
// instead -- filing a task that parks before it reaches a sandbox is no
// answer to "run this now", and neither is a button that appears to do
// nothing.
func TestRunScheduleNowIsRefusedWhenTheRepoHasComeOffTheTargetRepoList(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	both := []string{"acme/widgets", "acme/gadgets"}
	setDefaultTeamTargetRepos(t, store, ctx, both)
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatalf("seeding settings: %v", err)
	}
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "nightly", Repo: "acme/gadgets", Recurrence: everyDay,
	})
	if err != nil {
		t.Fatalf("creating a schedule: %v", err)
	}
	narrowed := []string{"acme/widgets"}
	setDefaultTeamTargetRepos(t, store, ctx, narrowed)

	_, err = c.RunScheduleNow(ctx, sched.ID)
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError naming the repo and the list", err)
	}
	for _, want := range []string{"acme/gadgets", "acme/widgets", "target repo list"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want %q in it", err, want)
		}
	}
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 {
		t.Errorf("tasks = %d, want none: the firing was refused", len(tasks))
	}

	// A refused firing is not a firing: the schedule's own history and
	// cadence are exactly as they were, the same way a refused overlap
	// leaves them.
	after, err := store.GetSchedule(ctx, sched.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.LastRunAt != nil {
		t.Errorf("lastRunAt = %v, want nil: nothing ran", after.LastRunAt)
	}
	if !after.NextRunAt.Equal(sched.NextRunAt) {
		t.Errorf("nextRunAt = %v, want it left at %v", after.NextRunAt, sched.NextRunAt)
	}
	if !after.Enabled {
		t.Error("want the schedule still enabled: a refusal is not a pause")
	}
}

// ... and a schedule that says it may overlap fires again, exactly as it
// would on its own cadence.
func TestRunScheduleNowFiresAgainWhenTheScheduleAllowsOverlap(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "x", Repo: "acme/widgets", Recurrence: everyDay, Overlap: "allow",
	})
	if err != nil {
		t.Fatalf("creating a schedule: %v", err)
	}
	for i := range 2 {
		if _, err := c.RunScheduleNow(ctx, sched.ID); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatalf("listing tasks: %v", err)
	}
	if len(tasks) != 2 {
		t.Errorf("tasks = %d, want both firings", len(tasks))
	}
}

// A template-backed schedule run by hand files the template as it stands
// now, not the copy cached on the schedule -- the same guarantee the
// cadence's own firing gives (bwsalmon/agents#516).
func TestRunScheduleNowFilesTheTemplatesCurrentContent(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{Name: "Bump", Title: "Bump dependencies"})
	if err != nil {
		t.Fatalf("creating a template: %v", err)
	}
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		TemplateID: tmpl.ID, Repo: "acme/widgets", Recurrence: everyDay,
	})
	if err != nil {
		t.Fatalf("creating a schedule: %v", err)
	}
	retitled := "Bump dependencies, carefully"
	if _, err := c.UpdateTemplate(ctx, tmpl.ID, ui.UpdateTemplateRequest{Title: &retitled}); err != nil {
		t.Fatalf("editing the template: %v", err)
	}

	run, err := c.RunScheduleNow(ctx, sched.ID)
	if err != nil {
		t.Fatalf("running now: %v", err)
	}
	task, err := store.GetTask(ctx, run.TaskID)
	if err != nil {
		t.Fatalf("reading the filed task: %v", err)
	}
	if task.Title != retitled {
		t.Errorf("title = %q, want the template's current %q", task.Title, retitled)
	}
	if run.Schedule.Title != retitled {
		t.Errorf("schedule title = %q, want the display cache resynced to %q", run.Schedule.Title, retitled)
	}
	if run.Schedule.TemplateID != tmpl.ID || run.Schedule.TemplateName != "Bump" {
		t.Errorf("template = %q/%q, want it still naming %s (Bump)",
			run.Schedule.TemplateID, run.Schedule.TemplateName, tmpl.ID)
	}
}

// A suite-backed schedule run by hand starts a run of its suite, the same
// thing its cadence would have started.
func TestRunScheduleNowStartsARunOfASuiteBackedSchedule(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	suite := suiteToSchedule(t, c, ctx, "Bug sweep")
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		SuiteID: suite.ID, Repo: "acme/widgets", Base: "main", Recurrence: everyDay,
	})
	if err != nil {
		t.Fatalf("creating a suite-backed schedule: %v", err)
	}

	run, err := c.RunScheduleNow(ctx, sched.ID)
	if err != nil {
		t.Fatalf("running now: %v", err)
	}
	if run.SuiteRunID == 0 {
		t.Fatal("want the started run's id back")
	}
	if run.TaskID != "" {
		t.Errorf("taskId = %q, want empty: this schedule runs a suite", run.TaskID)
	}
	if run.Schedule.SuiteID != suite.ID || run.Schedule.SuiteName != suite.Name {
		t.Errorf("suite = %q/%q, want %q/%q",
			run.Schedule.SuiteID, run.Schedule.SuiteName, suite.ID, suite.Name)
	}
	active, err := store.HasActiveRunForSchedule(ctx, sched.ID)
	if err != nil {
		t.Fatalf("checking for an active run: %v", err)
	}
	if !active {
		t.Error("want the schedule to have a run in flight")
	}

	// And the same gate the cadence gets: a second run while the first
	// is still active is refused rather than stacked.
	if _, err := c.RunScheduleNow(ctx, sched.ID); err == nil {
		t.Error("second run: want an error while the first run is still active")
	}
}

func TestRunScheduleNowOnAnUnknownIDIsNotFound(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	if _, err := c.RunScheduleNow(ctx, "sched-nope"); err == nil {
		t.Fatal("want an error for a schedule that does not exist")
	}
}

func TestCreateScheduleWithExplicitTimeZone(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "x", Repo: "acme/widgets", Recurrence: everyDay, TimeZone: "America/New_York",
	})
	if err != nil {
		t.Fatalf("creating schedule: %v", err)
	}
	if sched.TimeZone != "America/New_York" {
		t.Fatalf("sched.TimeZone = %q, want America/New_York", sched.TimeZone)
	}
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 8, 28, 9, 0, 0, 0, loc)
	if !sched.NextRunAt.Equal(want) {
		t.Errorf("nextRunAt = %v, want %v", sched.NextRunAt.UTC(), want.UTC())
	}

	got, err := store.GetSchedule(ctx, sched.ID)
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if got.TimeZone != "America/New_York" {
		t.Errorf("got.TimeZone = %q, want America/New_York", got.TimeZone)
	}
}

func TestCreateScheduleRejectsInvalidTimeZone(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	_, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "x", Repo: "acme/widgets", Recurrence: everyDay, TimeZone: "Mars/Phobos",
	})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v, want ValidationError", err)
	}
}

func TestUpdateScheduleExplicitTimeZoneRetimes(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "x", Repo: "acme/widgets", Recurrence: everyDay, TimeZone: "America/New_York",
	})
	if err != nil {
		t.Fatal(err)
	}

	newZone := "America/Los_Angeles"
	updated, err := c.UpdateSchedule(ctx, sched.ID, ui.UpdateScheduleRequest{
		TimeZone: &newZone,
	})
	if err != nil {
		t.Fatalf("updating schedule timezone: %v", err)
	}
	if updated.TimeZone != "America/Los_Angeles" {
		t.Fatalf("updated.TimeZone = %q, want America/Los_Angeles", updated.TimeZone)
	}
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 8, 28, 9, 0, 0, 0, loc)
	if !updated.NextRunAt.Equal(want) {
		t.Errorf("nextRunAt = %v, want %v", updated.NextRunAt.UTC(), want.UTC())
	}
}

func TestUpdateScheduleRejectsInvalidTimeZone(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "x", Repo: "acme/widgets", Recurrence: everyDay,
	})
	if err != nil {
		t.Fatal(err)
	}
	badZone := "Invalid/Zone"
	_, err = c.UpdateSchedule(ctx, sched.ID, ui.UpdateScheduleRequest{
		TimeZone: &badZone,
	})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v, want ValidationError", err)
	}
}

// A branch pasted with a stray space names that branch, the way a
// template's base and a new task's branch already did: the composition
// preview beside the form showed " main " as main while Add refused it
// as "not a usable branch name" (2026-10-13 recurring/composed UI
// walkthrough).
func TestScheduleBaseIsTrimmedOnCreateUpdateAndPreview(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	req := ui.CreateScheduleRequest{
		Title: "x", Repo: "acme/widgets", Base: "  main ", Recurrence: everyDay,
	}
	preview, err := c.DraftScheduleComposition(ctx, req)
	if err != nil {
		t.Fatalf("previewing: %v", err)
	}
	wantField(t, preview.Tasks[0], "base", "schedule", "main")

	sched, err := c.CreateSchedule(ctx, req)
	if err != nil {
		t.Fatalf("creating a schedule on a padded base: %v", err)
	}
	if sched.Base != "main" {
		t.Errorf("created base = %q, want main", sched.Base)
	}

	updated, err := c.UpdateSchedule(ctx, sched.ID, ui.UpdateScheduleRequest{Base: ptr(" release-2.0\t")})
	if err != nil {
		t.Fatalf("updating to a padded base: %v", err)
	}
	if updated.Base != "release-2.0" {
		t.Errorf("updated base = %q, want release-2.0", updated.Base)
	}

	suite := suiteToSchedule(t, c, ctx, "Bug sweep")
	if _, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		SuiteID: suite.ID, Repo: "acme/widgets", Base: "   ", Recurrence: everyDay,
	}); err == nil {
		t.Error("a suite schedule on a blank base was accepted; want it refused as missing")
	}
}
