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

// The §10 schedule-group walkthrough (docs/test-runs/2026-11-11-schedule-groups.md)
// found that a suite schedule withdrawn from .grain/schedules/ while its
// suite run was still going let the rest of its overlap group fire on top
// of the run: the run's tasks carried no group tag, so nothing outlived
// the schedule row the way a task schedule's tagged task does.

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func groupedSuiteSchedule(t *testing.T, store *model.Store, ctx context.Context, id string,
	repo model.RepoRef, group string) model.Schedule {

	t.Helper()
	tmplID := "tmpl-" + id
	if err := store.PutTemplate(ctx, model.Template{ID: tmplID, Name: "Sweep", Title: "Sweep", Body: "Sweep it."}); err != nil {
		t.Fatalf("writing the template: %v", err)
	}
	suiteID := "suite-" + id
	if err := store.PutSuite(ctx, model.Suite{ID: suiteID, Name: "Sweep " + id,
		Items: []model.SuiteItem{{TemplateID: tmplID}}, Mode: model.SuiteCount, Count: 2,
		CreatedAt: groupStart.Add(-48 * time.Hour)}); err != nil {
		t.Fatalf("writing the suite: %v", err)
	}
	sched := groupedSchedule(t, store, ctx, id, repo, group)
	sched.Body = ""
	sched.SuiteID = &suiteID
	sched.Base = "main"
	if err := store.PutSchedule(ctx, sched); err != nil {
		t.Fatalf("writing schedule %s: %v", id, err)
	}
	return sched
}

func TestAWithdrawnSuiteSchedulesRunStillHoldsItsGroup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	groupedSuiteSchedule(t, store, ctx, "sweep", repo, "train")
	groupedSchedule(t, store, ctx, "tick", repo, "train")

	fired := fireGrouped(t, store, ctx, "sweep", groupStart)
	if fired.SuiteRunID == 0 {
		t.Fatalf("suite firing = %+v, want a suite run", fired)
	}
	run, err := store.GetSuiteRun(ctx, fired.SuiteRunID)
	if err != nil || run == nil || len(run.Tasks) != 1 {
		t.Fatalf("suite run = %+v, %v; want one task in its first pass", run, err)
	}
	groupTag := model.ScheduleOverlapGroupTag(repo, "train")
	task, err := store.GetTask(ctx, run.Tasks[0].TaskID)
	if err != nil || task == nil {
		t.Fatalf("reading %s: %v", run.Tasks[0].TaskID, err)
	}
	if !slices.Contains(task.Tags, groupTag) {
		t.Errorf("suite task tags = %v, want %q", task.Tags, groupTag)
	}
	if slices.Contains(task.Tags, model.ScheduleFiringTag("sweep")) {
		t.Errorf("suite task tags = %v, want no schedule firing tag (that names a task schedule's own firing)", task.Tags)
	}

	if err := store.DeleteSchedule(ctx, "sweep"); err != nil {
		t.Fatalf("withdrawing the suite schedule: %v", err)
	}
	held := fireGrouped(t, store, ctx, "tick", groupStart.Add(time.Minute))
	if !held.Skipped || held.TaskID != "" || held.Skip == nil {
		t.Fatalf("firing after the suite schedule was withdrawn = %+v, want it held by the run", held)
	}
	if held.Skip.TaskID != task.ID || held.Skip.HeldByScheduleID != "sweep" ||
		!strings.Contains(held.Skip.Detail, `overlap group "train"`) {
		t.Errorf("skip = %+v, want it to name the withdrawn schedule sweep and its run's task %s", held.Skip, task.ID)
	}

	// A later pass of the run carries the tag too, with the schedule gone.
	closeGroupedTask(t, store, ctx, task.ID, groupStart.Add(2*time.Minute))
	run, _ = store.GetSuiteRun(ctx, fired.SuiteRunID)
	next, err := store.FireNextPass(ctx, *run, groupStart.Add(3*time.Minute))
	if err != nil {
		t.Fatalf("firing the next pass: %v", err)
	}
	var second string
	for _, rt := range next.Tasks {
		if rt.TaskID != task.ID {
			second = rt.TaskID
		}
	}
	nt, err := store.GetTask(ctx, second)
	if err != nil || nt == nil || !slices.Contains(nt.Tags, groupTag) {
		t.Fatalf("second pass task %q = %+v, %v; want it tagged %q", second, nt, err, groupTag)
	}
	if again := fireGrouped(t, store, ctx, "tick", groupStart.Add(4*time.Minute)); !again.Skipped {
		t.Errorf("firing during the second pass = %+v, want it held", again)
	}

	// An ungrouped suite schedule's tasks carry no group tag.
	groupedSuiteSchedule(t, store, ctx, "loose", repo, "")
	lf := fireGrouped(t, store, ctx, "loose", groupStart)
	lr, _ := store.GetSuiteRun(ctx, lf.SuiteRunID)
	lt, _ := store.GetTask(ctx, lr.Tasks[0].TaskID)
	for _, tag := range lt.Tags {
		if strings.HasPrefix(tag, "schedule-group:") {
			t.Errorf("ungrouped suite task tags = %v, want no group tag", lt.Tags)
		}
	}
}

// A schedule starved by another schedule's never-started task in its
// overlap group says so, rather than calling that task its own previous
// firing.
func TestAStarvedGroupFiringNamesTheHolder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	groupedSchedule(t, store, ctx, "sync", repo, "train")
	groupedSchedule(t, store, ctx, "safety-net", repo, "train")
	first := fireGrouped(t, store, ctx, "sync", groupStart)
	now := groupStart.Add(2 * time.Hour)
	held := fireGrouped(t, store, ctx, "safety-net", now)
	if held.Skip == nil {
		t.Fatalf("firing = %+v, want it held", held)
	}
	k := *held.Skip
	k.Occurrence = groupStart
	sched, _ := store.GetSchedule(ctx, "safety-net")
	starved, err := store.EscalateStarvedScheduleBlocker(ctx, *sched, k, now, time.UTC)
	if err != nil || starved.TaskID != first.TaskID {
		t.Fatalf("escalation = %+v, %v; want it to escalate %s", starved, err, first.TaskID)
	}
	if starved.HeldBy != "sync" || starved.Group != "train" {
		t.Errorf("escalation = %+v, want HeldBy sync in group train", starved)
	}
	title := model.StarvedScheduleTitle(*sched, starved)
	body := model.StarvedScheduleComment(*sched, starved)
	for _, s := range []string{title, body} {
		if strings.Contains(s, "its previous firing") || !strings.Contains(s, `schedule sync's firing in overlap group "train"`) {
			t.Errorf("starved wording %q, want it to name schedule sync and the group", s)
		}
	}
	// Its own previous firing keeps the old wording.
	own := model.StarvedScheduleTitle(*sched, model.StarvedScheduleFiring{TaskID: "7", HeldFor: time.Hour})
	if !strings.Contains(own, "from its previous firing") {
		t.Errorf("own-firing title %q, want \"from its previous firing\"", own)
	}
}
