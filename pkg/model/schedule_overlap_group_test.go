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

// An overlap group shared across schedules (model.Schedule.OverlapGroup,
// grain/task-3895): two schedules that fire the same work -- a release
// sync and its safety net -- must not run at once, and `overlap: skip`
// on its own only ever looked at a schedule's own previous firing.

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

var groupStart = time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)

func groupedSchedule(t *testing.T, store *model.Store, ctx context.Context, id string,
	repo model.RepoRef, group string) model.Schedule {

	t.Helper()
	sched := model.Schedule{
		ID:           id,
		Title:        "Sync " + id,
		Body:         "Sync the release.",
		Target:       repo,
		Recurrence:   model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 9 * 60},
		Overlap:      model.OverlapSkip,
		OverlapGroup: group,
		Enabled:      true,
		NextRunAt:    groupStart,
		CreatedAt:    groupStart.Add(-48 * time.Hour),
	}
	if err := store.PutSchedule(ctx, sched); err != nil {
		t.Fatalf("writing schedule %s: %v", id, err)
	}
	return sched
}

func fireGrouped(t *testing.T, store *model.Store, ctx context.Context, id string, now time.Time) model.ScheduleFiring {
	t.Helper()
	sched, err := store.GetSchedule(ctx, id)
	if err != nil || sched == nil {
		t.Fatalf("reading schedule %s: %v, %v", id, sched, err)
	}
	firing, err := store.FireSchedule(ctx, *sched, now, time.UTC, model.RepoRef{})
	if err != nil {
		t.Fatalf("firing %s: %v", id, err)
	}
	return firing
}

func closeGroupedTask(t *testing.T, store *model.Store, ctx context.Context, id string, at time.Time) {
	t.Helper()
	if err := store.ObserveField(ctx, id, at, func(o *model.Observation) { o.ClosedAt = &at }); err != nil {
		t.Fatalf("closing %s: %v", id, err)
	}
}

func TestAnOverlapGroupHoldsAnotherSchedulesFiringUntilTheFirstCloses(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	groupedSchedule(t, store, ctx, "sync", repo, "release-sync")
	groupedSchedule(t, store, ctx, "safety-net", repo, "release-sync")

	first := fireGrouped(t, store, ctx, "sync", groupStart)
	if first.TaskID == "" {
		t.Fatalf("first firing = %+v, want a filed task", first)
	}
	task, err := store.GetTask(ctx, first.TaskID)
	if err != nil || task == nil {
		t.Fatalf("reading %s: %v", first.TaskID, err)
	}
	groupTag := model.ScheduleOverlapGroupTag(repo, "release-sync")
	if !slices.Contains(task.Tags, groupTag) || !slices.Contains(task.Tags, model.ScheduleFiringTag("sync")) {
		t.Errorf("tags = %v, want the schedule's own tag and %q", task.Tags, groupTag)
	}

	held := fireGrouped(t, store, ctx, "safety-net", groupStart)
	if !held.Skipped || held.TaskID != "" || held.Skip == nil {
		t.Fatalf("second firing = %+v, want it held by the group", held)
	}
	if held.Skip.HeldByScheduleID != "sync" || held.Skip.TaskID != first.TaskID ||
		held.Skip.Reason != model.SuppressedByOpenTask {
		t.Errorf("skip = %+v, want it held by schedule sync's task %s", held.Skip, first.TaskID)
	}
	skips, err := store.ScheduleSkips(ctx, "safety-net")
	if err != nil || len(skips) != 1 {
		t.Fatalf("skips = %+v, %v; want the held occurrence recorded", skips, err)
	}
	if skips[0].HeldByScheduleID != "sync" || !strings.Contains(skips[0].Summary(), "sync") ||
		!strings.Contains(skips[0].Summary(), `overlap group "release-sync"`) {
		t.Errorf("recorded skip = %+v (%q), want it to name schedule sync and the group",
			skips[0], skips[0].Summary())
	}

	due, _ := store.GetSchedule(ctx, "safety-net")
	why, err := store.SuppressedFiring(ctx, *due, groupStart.Add(time.Minute), time.UTC)
	if err != nil {
		t.Fatalf("SuppressedFiring: %v", err)
	}
	if why == nil || why.HeldByScheduleID != "sync" || why.TaskID != first.TaskID ||
		!strings.Contains(why.Detail, "release-sync") {
		t.Errorf("suppression = %+v, want it to name schedule sync and its task", why)
	}

	closeGroupedTask(t, store, ctx, first.TaskID, groupStart.Add(30*time.Minute))
	late := fireGrouped(t, store, ctx, "safety-net", groupStart.Add(31*time.Minute))
	if late.TaskID == "" {
		t.Fatalf("firing after the close = %+v, want it to file", late)
	}
	// And now sync is the one held: the group goes both ways.
	sync, _ := store.GetSchedule(ctx, "sync")
	if why, err := store.SuppressedFiring(ctx, *sync, sync.NextRunAt, time.UTC); err != nil ||
		why == nil || why.HeldByScheduleID != "safety-net" {
		t.Errorf("sync's suppression = %+v, %v; want it held by safety-net", why, err)
	}
}

func TestAnOverlapGroupIsScopedToItsRepository(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	groupedSchedule(t, store, ctx, "here", model.RepoRef{Owner: "acme", Name: "widgets"}, "release-sync")
	groupedSchedule(t, store, ctx, "elsewhere", model.RepoRef{Owner: "other", Name: "gadgets"}, "release-sync")
	groupedSchedule(t, store, ctx, "ungrouped", model.RepoRef{Owner: "acme", Name: "widgets"}, "")

	if f := fireGrouped(t, store, ctx, "here", groupStart); f.TaskID == "" {
		t.Fatalf("first firing = %+v", f)
	}
	if f := fireGrouped(t, store, ctx, "elsewhere", groupStart); f.TaskID == "" || f.Skipped {
		t.Errorf("another repository's schedule in a group of the same name = %+v, want it to fire", f)
	}
	if f := fireGrouped(t, store, ctx, "ungrouped", groupStart); f.TaskID == "" || f.Skipped {
		t.Errorf("a schedule in no group = %+v, want it to fire", f)
	}
}

// A task outlives the schedule that filed it: a schedule file renamed or
// withdrawn while its task runs still holds the group, through the tag.
func TestAWithdrawnSchedulesOpenTaskStillHoldsItsGroup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	groupedSchedule(t, store, ctx, "old-name", repo, "release-sync")
	groupedSchedule(t, store, ctx, "safety-net", repo, "release-sync")
	first := fireGrouped(t, store, ctx, "old-name", groupStart)
	if err := store.DeleteSchedule(ctx, "old-name"); err != nil {
		t.Fatalf("deleting the schedule: %v", err)
	}
	held := fireGrouped(t, store, ctx, "safety-net", groupStart)
	if !held.Skipped || held.Skip == nil || held.Skip.TaskID != first.TaskID ||
		held.Skip.HeldByScheduleID != "old-name" {
		t.Errorf("firing = %+v (skip %+v), want it held by the withdrawn schedule's task %s",
			held, held.Skip, first.TaskID)
	}
}

// overlap: allow never asks the group, and tasks it files carry no group
// tag, so it holds nobody either.
func TestAnAllowSchedulesTasksCarryNoGroupTag(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	sched := groupedSchedule(t, store, ctx, "loose", repo, "release-sync")
	sched.Overlap = model.OverlapAllow
	if err := store.PutSchedule(ctx, sched); err != nil {
		t.Fatal(err)
	}
	f := fireGrouped(t, store, ctx, "loose", groupStart)
	task, err := store.GetTask(ctx, f.TaskID)
	if err != nil || task == nil {
		t.Fatalf("reading %s: %v", f.TaskID, err)
	}
	if slices.Contains(task.Tags, model.ScheduleOverlapGroupTag(repo, "release-sync")) {
		t.Errorf("tags = %v, want no group tag on an overlap: allow firing", task.Tags)
	}
}

func TestOverlapGroupNames(t *testing.T) {
	t.Parallel()
	for _, ok := range []string{"", "sync", "sync-oss-release", "a.b_c", "9lives"} {
		if err := model.ValidateOverlapGroup(ok); err != nil {
			t.Errorf("ValidateOverlapGroup(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"-x", "a b", "a:b", "a/b", strings.Repeat("x", 129)} {
		if err := model.ValidateOverlapGroup(bad); err == nil {
			t.Errorf("ValidateOverlapGroup(%q) = nil, want it refused", bad)
		}
	}
	if got := model.ScheduleOverlapGroupTag(model.RepoRef{Owner: "acme", Name: "widgets"}, "g"); got !=
		"schedule-group:acme/widgets:g" {
		t.Errorf("tag = %q", got)
	}
	if got := model.ScheduleOverlapGroupTag(model.RepoRef{}, "g"); got != "" {
		t.Errorf("tag with no repo = %q, want none", got)
	}
}
