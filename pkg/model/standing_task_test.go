package model_test

// A standing template (IsStandingTemplateID, the pipeline steward) is one
// task rather than one per firing: the first firing files it, and every
// later one wakes that same task -- but only when it is idle, keeping a
// grant a person added, and filing afresh only once somebody closes it.

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
)

func pipelineStewardSchedule(t *testing.T, ctx context.Context, store *model.Store) model.Schedule {
	t.Helper()
	id := model.PipelineStewardTemplateID
	sched := model.Schedule{
		ID: id, TemplateID: &id,
		Recurrence: model.Recurrence{Kind: model.RecurrenceEveryNMinutes, EveryNMinutes: 30},
		Overlap:    model.OverlapSkip, Enabled: true,
		NextRunAt: firstNight, CreatedAt: firstNight,
	}
	if err := store.PutSchedule(ctx, sched); err != nil {
		t.Fatalf("PutSchedule: %v", err)
	}
	return sched
}

func refire(t *testing.T, ctx context.Context, store *model.Store, now time.Time) model.ScheduleFiring {
	t.Helper()
	sched, err := store.GetSchedule(ctx, model.PipelineStewardTemplateID)
	if err != nil || sched == nil {
		t.Fatalf("GetSchedule: %v %v", sched, err)
	}
	firing, err := store.FireSchedule(ctx, *sched, now, time.UTC, model.RepoRef{})
	if err != nil {
		t.Fatalf("FireSchedule: %v", err)
	}
	return firing
}

func TestAStandingScheduleWakesItsOneTaskRatherThanFilingAnother(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	pipelineStewardSchedule(t, ctx, store)

	first := refire(t, ctx, store, firstNight)
	if first.TaskID == "" || first.Woke {
		t.Fatalf("the first firing = %+v, want a newly filed task", first)
	}
	id := first.TaskID

	// Still queued: the wake is already happening, and a second firing
	// neither files nor wakes anything.
	held := refire(t, ctx, store, firstNight.Add(30*time.Minute))
	if !held.Skipped || held.TaskID != "" {
		t.Fatalf("a firing while the standing task is queued = %+v, want it held", held)
	}

	// The run finishes into awaiting_reply, and a person adds a grant.
	done := firstNight.Add(40 * time.Minute)
	if err := store.ObserveField(ctx, id, done, func(o *model.Observation) { o.CompletedAt = &done }); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateTask(ctx, id, func(task *model.Task) error {
		task.Grants = append(task.Grants, model.Grant{Capability: "api:releases:write", Via: model.GrantByLabel})
		// And one an earlier build's template gave it, which this
		// build's no longer names: a wake must take it away.
		task.Grants = append(task.Grants, model.Grant{Capability: "api:deployment-ops", Via: model.GrantByGrain})
		// And a standing task filed before the template carried
		// priority-dispatch: a wake must add it.
		task.Grants = slices.DeleteFunc(task.Grants, func(g model.Grant) bool {
			return g.Capability == model.PriorityDispatchCapability
		})
		// And the title an earlier build filed it under: a wake must
		// rename it to this build's.
		task.Title = "Keep the merge pipeline moving"
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	wake := firstNight.Add(time.Hour)
	woke := refire(t, ctx, store, wake)
	if !woke.Woke || woke.TaskID != id {
		t.Fatalf("a firing with the standing task idle = %+v, want task %s woken", woke, id)
	}
	if st, err := store.State(ctx, id); err != nil || st != model.StateQueued {
		t.Errorf("the woken task reads %q (%v), want queued", st, err)
	}
	task, err := store.GetTask(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if task.Title != "Pipeline steward" {
		t.Errorf("title after the wake = %q, want this build's %q", task.Title, "Pipeline steward")
	}
	if !slices.ContainsFunc(task.Grants, func(g model.Grant) bool { return g.Capability == "api:releases:write" }) {
		t.Errorf("grants after the wake = %v, want the person's api:releases:write kept", task.Grants)
	}
	if slices.ContainsFunc(task.Grants, func(g model.Grant) bool { return g.Capability == "api:deployment-ops" }) {
		t.Errorf("grants after the wake = %v, want grain's api:deployment-ops, which the template no longer names, dropped", task.Grants)
	}
	if !task.HasPriorityDispatch() {
		t.Errorf("grants after the wake = %v, want the template's priority-dispatch added", task.Grants)
	}
	for _, preset := range model.PipelineStewardAPIPresets {
		if !slices.ContainsFunc(task.Grants, func(g model.Grant) bool { return g.Capability == model.APIGrantPrefix+preset }) {
			t.Errorf("grants after the wake = %v, want the template's api:%s kept", task.Grants, preset)
		}
	}
	comments, err := store.Comments(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) == 0 || !strings.Contains(comments[len(comments)-1].Body, "Scheduled wake") {
		t.Errorf("comments = %+v, want the wake noted on the task", comments)
	}
	if sched, _ := store.GetSchedule(ctx, model.PipelineStewardTemplateID); !sched.NextRunAt.After(wake) {
		t.Errorf("next run %s, want the cadence advanced past the wake at %s", sched.NextRunAt, wake)
	}

	// Somebody closes it: the next firing starts a new standing task.
	closed := wake.Add(time.Minute)
	if err := store.ObserveField(ctx, id, closed, func(o *model.Observation) { o.ClosedAt = &closed }); err != nil {
		t.Fatal(err)
	}
	fresh := refire(t, ctx, store, wake.Add(30*time.Minute))
	if fresh.TaskID == "" || fresh.TaskID == id || fresh.Woke {
		t.Fatalf("a firing after the standing task was closed = %+v, want a new task", fresh)
	}
}

// A standing task parked on a question is waiting on a person, and a wake
// would run over the question: the firing is held instead.
func TestAStandingTaskParkedOnAQuestionIsNotWoken(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	pipelineStewardSchedule(t, ctx, store)
	id := refire(t, ctx, store, firstNight).TaskID

	at := firstNight.Add(10 * time.Minute)
	q, err := store.AddComment(ctx, model.Comment{TaskID: id, Body: "Which branch?", CreatedAt: at,
		Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAgent, ID: "run"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ObserveField(ctx, id, at, func(o *model.Observation) {
		o.CompletedAt, o.PendingQuestionCommentID = &at, &q
	}); err != nil {
		t.Fatal(err)
	}
	if got := refire(t, ctx, store, firstNight.Add(time.Hour)); !got.Skipped || got.Woke {
		t.Fatalf("a firing with the standing task parked on a question = %+v, want it held", got)
	}
}

// FireScheduleWake is the out-of-band wake: the reason reaches the task,
// and the cadence stays where it was.
func TestAWakeCarriesItsReasonAndLeavesTheCadence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	sched := pipelineStewardSchedule(t, ctx, store)
	id := refire(t, ctx, store, firstNight).TaskID
	done := firstNight.Add(5 * time.Minute)
	if err := store.ObserveField(ctx, id, done, func(o *model.Observation) { o.CompletedAt = &done }); err != nil {
		t.Fatal(err)
	}
	before, _ := store.GetSchedule(ctx, sched.ID)

	firing, err := store.FireScheduleWake(ctx, *before, done.Add(time.Minute), model.RepoRef{},
		"acme/widgets main is confirmed red at abc1234")
	if err != nil {
		t.Fatal(err)
	}
	if !firing.Woke || firing.TaskID != id {
		t.Fatalf("FireScheduleWake = %+v, want task %s woken", firing, id)
	}
	comments, _ := store.Comments(ctx, id)
	if last := comments[len(comments)-1].Body; !strings.Contains(last, "confirmed red at abc1234") {
		t.Errorf("the wake note %q does not say why it woke", last)
	}
	if after, _ := store.GetSchedule(ctx, sched.ID); !after.NextRunAt.Equal(before.NextRunAt) {
		t.Errorf("next run moved from %s to %s; a wake is an extra firing", before.NextRunAt, after.NextRunAt)
	}
}
