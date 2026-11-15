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

// `skip-when: publishing-held` (model.Schedule.SkipWhen, grain/task-4393):
// on 2026-11-10 the release train woke 45 times in a day while main's
// publishing was held by a release triage task, and could do nothing.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

var heldStart = time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)

// holdPublishing records three failed releases of repo's main and opens a
// release triage task on it -- the finding deployment health reports as
// gated -- and returns the triage task's id.
func holdPublishing(t *testing.T, store *model.Store, ctx context.Context, repo model.RepoRef) string {
	t.Helper()
	since := heldStart.Add(-9 * time.Hour)
	finished := heldStart.Add(-time.Hour)
	if err := store.PutBaseBranchRelease(ctx, model.BaseBranchRelease{
		Repo: repo, Branch: "main", Commit: "7746d62", RunID: "rel-12",
		StartedAt: heldStart.Add(-2 * time.Hour), FinishedAt: &finished, Conclusion: "failure",
		Failures: 12, FailingSince: &since,
	}); err != nil {
		t.Fatalf("recording the failed releases: %v", err)
	}
	triage := model.Task{
		ID: "4256", Title: "Triage main's failing releases", Target: &repo, Base: "main",
		Tags: []string{model.BaseReleaseTriageBranchTag(repo, "main")},
	}
	if err := store.PutTask(ctx, triage); err != nil {
		t.Fatalf("opening the triage task: %v", err)
	}
	return triage.ID
}

func trainSchedule(t *testing.T, store *model.Store, ctx context.Context, repo model.RepoRef,
	skipWhen model.ScheduleSkipWhen) model.Schedule {

	t.Helper()
	sched := model.Schedule{
		ID: "train", Title: "Release train", Body: "Run one tick of the train.",
		Target: repo, Base: "main",
		Recurrence: model.Recurrence{Kind: model.RecurrenceEveryNMinutes, EveryNMinutes: 20},
		Overlap:    model.OverlapSkip, SkipWhen: skipWhen,
		Enabled: true, NextRunAt: heldStart, CreatedAt: heldStart.Add(-48 * time.Hour),
	}
	if err := store.PutSchedule(ctx, sched); err != nil {
		t.Fatalf("writing the schedule: %v", err)
	}
	return sched
}

func fireTrain(t *testing.T, store *model.Store, ctx context.Context, now time.Time) model.ScheduleFiring {
	t.Helper()
	sched, err := store.GetSchedule(ctx, "train")
	if err != nil || sched == nil {
		t.Fatalf("reading the schedule: %v, %v", sched, err)
	}
	firing, err := store.FireSchedule(ctx, *sched, now, time.UTC, model.RepoRef{})
	if err != nil {
		t.Fatalf("firing: %v", err)
	}
	return firing
}

func TestASchedulesFiringIsHeldWhilePublishingIsHeldAndFiresOnceWhenItClears(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	triage := holdPublishing(t, store, ctx, repo)
	trainSchedule(t, store, ctx, repo, model.SkipWhenPublishingHeld)

	// Held, every cycle, for as many occurrences as come round.
	for i, at := range []time.Time{heldStart, heldStart.Add(time.Minute), heldStart.Add(41 * time.Minute)} {
		held := fireTrain(t, store, ctx, at)
		if !held.Skipped || held.TaskID != "" || held.Skip == nil {
			t.Fatalf("firing %d = %+v, want it held while publishing is", i, held)
		}
		if held.Skip.Reason != model.SuppressedByPublishingHeld || held.Skip.TaskID != triage {
			t.Errorf("skip %d = %+v, want it held by triage task %s", i, held.Skip, triage)
		}
	}
	sched, _ := store.GetSchedule(ctx, "train")
	if !sched.NextRunAt.Equal(heldStart) || sched.LastRunAt != nil {
		t.Errorf("NextRunAt %v, LastRunAt %v; want the occurrence held and nothing run", sched.NextRunAt, sched.LastRunAt)
	}

	// Recorded once, in the words the schedules page shows.
	skips, err := store.ScheduleSkips(ctx, "train")
	if err != nil || len(skips) != 1 {
		t.Fatalf("skips = %+v, %v; want the held occurrence recorded once", skips, err)
	}
	if got := skips[0].Summary(); !strings.Contains(got, "skipped at ") ||
		!strings.Contains(got, "publishing held by task "+triage) {
		t.Errorf("summary = %q, want \"skipped at ...: publishing held by task %s\"", got, triage)
	}
	if skips[0].Outcome != model.ScheduleSkipHeld {
		t.Errorf("outcome = %q, want held", skips[0].Outcome)
	}
	why, err := store.SuppressedFiring(ctx, *sched, heldStart.Add(42*time.Minute), time.UTC)
	if err != nil || why == nil || why.Reason != model.SuppressedByPublishingHeld || why.TaskID != triage ||
		!strings.Contains(why.Detail, "publishing held by task "+triage) {
		t.Errorf("suppression = %+v, %v; want it to name triage task %s", why, err, triage)
	}

	// The triage task closes: the next cycle fires the held occurrence
	// once, standing in for the ones that came round meanwhile.
	closeAt := heldStart.Add(50 * time.Minute)
	if err := store.ObserveField(ctx, triage, closeAt, func(o *model.Observation) { o.ClosedAt = &closeAt }); err != nil {
		t.Fatalf("closing the triage task: %v", err)
	}
	if why, err := store.SuppressedFiring(ctx, *sched, closeAt, time.UTC); err != nil || why != nil {
		t.Errorf("suppression after the close = %+v, %v; want none", why, err)
	}
	fired := fireTrain(t, store, ctx, closeAt.Add(time.Minute))
	if fired.Skipped || fired.TaskID == "" {
		t.Fatalf("firing after the close = %+v, want it to file a tick", fired)
	}
	skips, _ = store.ScheduleSkips(ctx, "train")
	if len(skips) != 1 || skips[0].Outcome != model.ScheduleSkipFired || skips[0].FiredTaskID != fired.TaskID {
		t.Errorf("skips after the close = %+v, want the held occurrence fired late as %s", skips, fired.TaskID)
	}
	sched, _ = store.GetSchedule(ctx, "train")
	if !sched.NextRunAt.After(closeAt) {
		t.Errorf("NextRunAt = %v, want the cadence moved on past %v", sched.NextRunAt, closeAt)
	}
}

func TestPublishingHeldOnlyHoldsTheSchedulesThatSayIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	holdPublishing(t, store, ctx, repo)
	trainSchedule(t, store, ctx, repo, model.SkipWhenNever)

	if firing := fireTrain(t, store, ctx, heldStart); firing.Skipped || firing.TaskID == "" {
		t.Errorf("firing = %+v, want a schedule without skip-when to file as before", firing)
	}
}

func TestPublishingHeldOnAnotherRepositoryHoldsNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	holdPublishing(t, store, ctx, model.RepoRef{Owner: "acme", Name: "gadgets"})
	trainSchedule(t, store, ctx, model.RepoRef{Owner: "acme", Name: "widgets"}, model.SkipWhenPublishingHeld)

	if firing := fireTrain(t, store, ctx, heldStart); firing.Skipped || firing.TaskID == "" {
		t.Errorf("firing = %+v, want a hold on another repository to hold nothing", firing)
	}
}

func TestRunNowIsNotHeldByPublishing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	holdPublishing(t, store, ctx, repo)
	sched := trainSchedule(t, store, ctx, repo, model.SkipWhenPublishingHeld)

	firing, err := store.FireScheduleNow(ctx, sched, heldStart, model.RepoRef{})
	if err != nil || firing.Skipped || firing.TaskID == "" {
		t.Errorf("Run now = %+v, %v; want a person's Run now to file whatever publishing says", firing, err)
	}
}

func TestParseScheduleSkipWhen(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]model.ScheduleSkipWhen{
		"": model.SkipWhenNever, "publishing-held": model.SkipWhenPublishingHeld, " publishing-held ": model.SkipWhenPublishingHeld,
	} {
		if got, err := model.ParseScheduleSkipWhen(in); err != nil || got != want {
			t.Errorf("ParseScheduleSkipWhen(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := model.ParseScheduleSkipWhen("ci-red"); err == nil {
		t.Errorf("ParseScheduleSkipWhen(ci-red) succeeded, want an unknown condition refused")
	}
}
