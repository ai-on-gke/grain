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
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// A rollover files a successor carrying the predecessor's brief, handover,
// settings and pull request, closes the predecessor as rolled_over, and
// moves every other task's wait on it onto the successor.
func TestRolloverFilesASuccessorAndMovesTheReferences(t *testing.T) {
	store, ctx := open(t)
	pred := queuedTask(t, store, ctx, "9001", &widgets)
	pred.Admission = model.AdmissionDurable
	pred.Body = "Stage 1: build the hub."
	pred.Tags = []string{"schedule:hub"}
	pred.Grants = []model.Grant{{Capability: "github#read-issues"}}
	pred.Links = []model.Link{
		{Kind: model.LinkFixes, Target: "acme/widgets#12"},
		{Kind: model.LinkProposedBy, Target: "9000"},
	}
	if err := store.PutTask(ctx, pred); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTaskBrief(ctx, pred.ID, "Stage 2: keep the hub green.", "alice", "stage 1 done", now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddComment(ctx, model.Comment{TaskID: pred.ID, Author: model.Attribution{Actor: bot},
		Body: "The hub is green; watch the nightly.", Handover: true, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTaskGoal(ctx, pred.ID, model.GoalDecl{Kind: model.GoalPersonCloses}, "alice", now); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTaskNotificationSubscriptions(ctx, pred.ID, []string{"durable.*"}); err != nil {
		t.Fatal(err)
	}

	dependent := queuedTask(t, store, ctx, "9002", &widgets)
	dependent.Links = []model.Link{{Kind: model.LinkDependsOn, Target: pred.ID}}
	if err := store.PutTask(ctx, dependent); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{ID: "9002-1", TaskID: dependent.ID, Attempt: 1, StartedAt: now},
		model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.Suspend(ctx, model.Suspension{RunID: "9002-1", TaskID: dependent.ID,
		Kind: model.SuspendRequested, ResumeOn: model.ResumeOn{Tasks: []string{pred.ID}}.JSON(),
		CreatedAt: now}); err != nil {
		t.Fatal(err)
	}

	later := now.Add(time.Hour)
	result, err := store.Rollover(ctx, pred.ID, model.Attribution{Actor: human}, "", later)
	if err != nil {
		t.Fatal(err)
	}
	succID := result.Successor
	if succID == "" || succID == pred.ID {
		t.Fatalf("successor = %q", succID)
	}

	succ, err := store.GetTask(ctx, succID)
	if err != nil || succ == nil {
		t.Fatalf("GetTask(successor) = %v, %v", succ, err)
	}
	for _, want := range []string{"Stage 2: keep the hub green.", "The hub is green; watch the nightly.",
		"Rolled over from [task 9001](/tasks/9001)"} {
		if !strings.Contains(succ.Body, want) {
			t.Errorf("successor body %q lacks %q", succ.Body, want)
		}
	}
	if strings.Contains(succ.Body, "Stage 1") {
		t.Errorf("successor body %q carries the original description, want the current brief", succ.Body)
	}
	if succ.Admission != model.AdmissionDurable || !slices.Equal(succ.Tags, pred.Tags) {
		t.Errorf("successor admission/tags = %s/%v, want the predecessor's", succ.Admission, succ.Tags)
	}
	if len(succ.Grants) != 1 || succ.Grants[0].Capability != "github#read-issues" {
		t.Errorf("successor grants = %+v", succ.Grants)
	}
	if linkTarget(succ.Links, model.LinkFixes) != "acme/widgets#12" ||
		linkTarget(succ.Links, model.LinkRolledOverFrom) != pred.ID {
		t.Errorf("successor links = %+v, want the pull request and rolled-over-from", succ.Links)
	}
	if tc, err := store.TaskContext(ctx, succID); err != nil || tc.CurrentBrief != "" || tc.HandoverVersion != 0 {
		t.Errorf("successor context = %+v, %v; want a fresh one", tc, err)
	}
	if g, err := store.TaskGoal(ctx, succID); err != nil || g == nil || g.Kind != model.GoalPersonCloses {
		t.Errorf("successor goal = %+v, %v; want the predecessor's", g, err)
	}
	if g, _ := store.TaskGoal(ctx, pred.ID); g != nil {
		t.Errorf("predecessor still has goal %+v", g)
	}
	if subs, _ := store.TaskNotificationSubscriptions(ctx, succID); !slices.Equal(subs, []string{"durable.*"}) {
		t.Errorf("successor subscriptions = %v", subs)
	}
	if state, err := store.State(ctx, succID); err != nil || state == model.StateClosed {
		t.Errorf("successor state = %s, %v; want it open", state, err)
	}

	closed, err := store.GetTask(ctx, pred.ID)
	if err != nil {
		t.Fatal(err)
	}
	if succ.Branch != closed.Branch {
		t.Errorf("successor branch = %s, want the predecessor's %s", succ.Branch, closed.Branch)
	}
	if model.RolledOverTo(*closed) != succID {
		t.Errorf("predecessor links = %+v, want rolled-over-to %s", closed.Links, succID)
	}
	if linkTarget(closed.Links, model.LinkFixes) != "" {
		t.Errorf("predecessor still holds the pull request: %+v", closed.Links)
	}
	if linkTarget(closed.Links, model.LinkProposedBy) != "9000" {
		t.Errorf("predecessor lost its own links: %+v", closed.Links)
	}
	target, err := store.WaitTargetAt(ctx, pred.ID, later)
	if err != nil {
		t.Fatal(err)
	}
	if !target.Closed || target.Outcome != model.WaitOutcomeRolledOver || target.RolledOverTo != succID {
		t.Errorf("predecessor wait target = %+v, want closed, rolled_over to %s", target, succID)
	}
	if tc, _ := store.TaskContext(ctx, pred.ID); tc.CurrentBrief == "" {
		t.Error("predecessor's brief was dropped; it keeps everything")
	}

	if !slices.Equal(result.Moved, []string{dependent.ID}) || result.Suspensions != 1 {
		t.Errorf("result = %+v, want the dependent and its park moved", result)
	}
	moved, _ := store.GetTask(ctx, dependent.ID)
	if got := linkTarget(moved.Links, model.LinkDependsOn); got != succID {
		t.Errorf("dependent now depends on %q, want %s", got, succID)
	}
	susps, err := store.Suspensions(ctx, dependent.ID)
	if err != nil || len(susps) != 1 {
		t.Fatalf("Suspensions = %+v, %v", susps, err)
	}
	if on, _ := model.ParseResumeOn(susps[0].ResumeOn); !slices.Equal(on.Tasks, []string{succID}) {
		t.Errorf("park resume_on = %s, want it on %s", susps[0].ResumeOn, succID)
	}

	if _, err := store.Rollover(ctx, pred.ID, model.Attribution{Actor: human}, "", later); !errors.Is(err, model.ErrRolloverRefused) ||
		!strings.Contains(err.Error(), "already rolled over, to task "+succID) {
		t.Errorf("second rollover = %v, want refused as already rolled over", err)
	}
}

func TestRolloverRefusesConsoleSessions(t *testing.T) {
	store, ctx := open(t)
	session := queuedTask(t, store, ctx, "9101", &widgets)
	session.Tags = []string{model.ConsoleSessionTag("ops")}
	if err := store.PutTask(ctx, session); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Rollover(ctx, session.ID, model.Attribution{Actor: human}, "", now); !errors.Is(err, model.ErrRolloverRefused) {
		t.Fatalf("rollover of a console session = %v, want refused", err)
	}
}

func TestRolloverRecordPastLimit(t *testing.T) {
	created := now.Add(-model.RolloverAgeLimit)
	for _, tc := range []struct {
		r    model.RolloverRecord
		want string
	}{
		{model.RolloverRecord{Runs: 499, Comments: 1999}, ""},
		{model.RolloverRecord{Runs: 500}, "500 runs"},
		{model.RolloverRecord{Comments: 2000}, "2000 comments"},
		{model.RolloverRecord{CreatedAt: &created}, "been open since"},
	} {
		got := tc.r.PastLimit(now)
		if (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
			t.Errorf("PastLimit(%+v) = %q, want %q", tc.r, got, tc.want)
		}
	}
}

func TestOpenDurableTasksPastRolloverLimit(t *testing.T) {
	store, ctx := open(t)
	old := now.Add(-2 * model.RolloverAgeLimit)
	for _, task := range []model.Task{durableTestTask("9201", true), durableTestTask("9202", true), durableTestTask("9203", false)} {
		task.CreatedAt = &old
		if task.ID == "9202" {
			task.CreatedAt = &now
		}
		if err := store.PutTask(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.OpenDurableTasksPastRolloverLimit(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["9201"] == "" {
		t.Fatalf("past the limit = %v, want only the old durable task", got)
	}
}
