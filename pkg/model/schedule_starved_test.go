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

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// A task schedule held by a peer's suite run whose pass has never
// started (walkthrough finding F6) is raised as one held by a task is:
// the run's never-started tasks, together, under one notification that
// waits on the run rather than on one of its tasks.
func TestAStarvedGroupFiringHeldByASuiteRunRaisesTheRunsTasks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	groupedSuiteSchedule(t, store, ctx, "b-suite", repo, "train")
	groupedSchedule(t, store, ctx, "c-tick", repo, "train")
	if err := store.PutTask(ctx, model.Task{ID: "busy", Title: "busy", Body: "busy", Priority: 9,
		Target: &repo, Approval: &model.Attribution{Actor: model.SchedulePrincipal}}); err != nil {
		t.Fatal(err)
	}
	fired := fireGrouped(t, store, ctx, "b-suite", groupStart)
	run, err := store.GetSuiteRun(ctx, fired.SuiteRunID)
	if err != nil || run == nil || len(run.Tasks) != 1 {
		t.Fatalf("suite run = %+v, %v; want one task in its first pass", run, err)
	}
	suiteTask := run.Tasks[0].TaskID
	if st, _ := store.State(ctx, suiteTask); st != model.StateQueued {
		t.Fatalf("suite task state = %s, want queued", st)
	}

	now := groupStart.Add(2 * time.Hour)
	held := fireGrouped(t, store, ctx, "c-tick", now)
	if held.Skip == nil || held.Skip.Reason != model.SuppressedBySuiteRun || held.Skip.HeldByScheduleID != "b-suite" {
		t.Fatalf("firing = %+v (skip %+v), want it held by b-suite's suite run", held, held.Skip)
	}
	k := *held.Skip
	sched, _ := store.GetSchedule(ctx, "c-tick")

	// Not yet starved: nothing happens.
	k.Occurrence = now.Add(-10 * time.Minute)
	if early, err := store.EscalateStarvedScheduleBlocker(ctx, *sched, k, now, time.UTC); err != nil || early.TaskID != "" {
		t.Fatalf("escalation 10m in = %+v, %v; want nothing", early, err)
	}

	k.Occurrence = groupStart
	starved, err := store.EscalateStarvedScheduleBlocker(ctx, *sched, k, now, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if starved.SuiteRunID != run.ID || starved.TaskID != suiteTask || starved.Pass != 1 ||
		starved.HeldBy != "b-suite" || starved.Group != "train" || !starved.Raised() || !starved.Notified {
		t.Fatalf("escalation = %+v, want suite run %d's task %s raised and notified, held by b-suite in train",
			starved, run.ID, suiteTask)
	}
	if got, _ := store.GetTask(ctx, suiteTask); got.Priority != 9 {
		t.Errorf("suite task priority = %d, want 9, the highest queued", got.Priority)
	}
	title := model.StarvedScheduleTitle(*sched, starved)
	body := model.StarvedScheduleComment(*sched, starved)
	for _, s := range []string{title, body} {
		if !strings.Contains(s, fmt.Sprintf("suite run %d", run.ID)) ||
			!strings.Contains(s, `schedule b-suite's firing in overlap group "train"`) || !strings.Contains(s, "#"+suiteTask) {
			t.Errorf("starved wording %q, want it to name suite run %d, schedule b-suite and the group", s, run.ID)
		}
	}
	comments, err := store.Comments(ctx, suiteTask)
	if err != nil || len(comments) != 1 || !strings.Contains(comments[0].Body, "until the run finishes") {
		t.Errorf("comments on the suite task = %+v, %v; want one about the held schedule", comments, err)
	}
	n, err := store.OpenNotification(ctx, model.NotificationSubject(model.FamilyScheduleStarved, "c-tick"))
	if err != nil || n == nil {
		t.Fatalf("notification = %+v, %v; want one open", n, err)
	}
	if n.ClearsWhen == nil || n.ClearsWhen.Kind != model.ClearsOnSuiteRun || n.ClearsWhen.SuiteRun != run.ID {
		t.Errorf("notification clears_when = %+v, want suite run %d finishing", n.ClearsWhen, run.ID)
	}

	// Again: no second comment, no second raise.
	again, err := store.EscalateStarvedScheduleBlocker(ctx, *sched, k, now.Add(time.Minute), time.UTC)
	if err != nil || again.Raised() || again.Notified {
		t.Errorf("second escalation = %+v, %v; want nothing new", again, err)
	}

	// A pass with a task under way is not starved.
	if err := store.StartRun(ctx, model.Run{ID: suiteTask + "-1", TaskID: suiteTask, Attempt: 1,
		StartedAt: now.Add(2 * time.Minute)}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if busy, err := store.EscalateStarvedScheduleBlocker(ctx, *sched, k, now.Add(3*time.Minute), time.UTC); err != nil || busy.TaskID != "" {
		t.Errorf("escalation with the pass under way = %+v, %v; want nothing", busy, err)
	}
}
