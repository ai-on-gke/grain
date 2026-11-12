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
	"slices"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// A repository's declared schedule firing a declared template carries
// grants of its own (`grants:` in .grain/schedules/<name>.yml) beside the
// template's. Recording a firing refreshes the schedule's copy of its
// template's content (syncTemplateCache), and that refresh used to
// replace the schedule's grants with the template's: the first firing
// carried the declared grant, and every later firing -- the one a
// person's acceptance of the declaration was for -- was filed without
// it (2026-11-11 capabilities walkthrough, §6 item 3).
func TestADeclaredSchedulesOwnGrantsSurviveItsFirings(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	tmplID := model.RepoDeclaredID(model.DeclaredTemplate, nightlyRepo, "walk")
	schedID := model.RepoDeclaredID(model.DeclaredSchedule, nightlyRepo, "walk")
	secrets := "https://grain.dev/capabilities/api#secrets:write"
	if err := store.PutTemplate(ctx, model.Template{ID: tmplID, Name: "walk", Title: "Walk", Body: "Walk it."}); err != nil {
		t.Fatalf("writing the template: %v", err)
	}
	if err := store.PutSchedule(ctx, model.Schedule{
		ID: schedID, TemplateID: &tmplID, Title: "Walk", Target: nightlyRepo,
		Recurrence: model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 9 * 60},
		Enabled:    true, NextRunAt: firstNight, CreatedAt: firstNight.Add(-time.Hour), Overlap: model.OverlapAllow,
		Grants: []model.Grant{{Capability: secrets, Via: ".grain/schedules/walk.yml"}},
	}); err != nil {
		t.Fatalf("writing the schedule: %v", err)
	}
	fire := func(at time.Time) *model.Task {
		t.Helper()
		sched, err := store.GetSchedule(ctx, schedID)
		if err != nil || sched == nil {
			t.Fatalf("reading the schedule back: (%+v, %v)", sched, err)
		}
		firing, err := store.FireScheduleNow(ctx, *sched, at, model.RepoRef{})
		if err != nil || firing.TaskID == "" {
			t.Fatalf("firing: (%+v, %v)", firing, err)
		}
		task, err := store.GetTask(ctx, firing.TaskID)
		if err != nil || task == nil {
			t.Fatalf("reading the filed task: (%+v, %v)", task, err)
		}
		return task
	}
	holds := func(task *model.Task) bool {
		return slices.ContainsFunc(task.Grants, func(g model.Grant) bool { return g.Capability == secrets })
	}
	if first := fire(firstNight); !holds(first) || first.Approval != nil {
		t.Fatalf("first firing: grants %+v, approval %+v; want %s held for a person", first.Grants, first.Approval, secrets)
	}
	if _, err := store.AcceptDeclaration(ctx, schedID, "alice@example.com", []string{"secrets:write"}, []string{secrets}, firstNight); err != nil {
		t.Fatalf("accepting: %v", err)
	}
	second := fire(firstNight.Add(time.Hour))
	if !holds(second) {
		t.Fatalf("the firing after acceptance dropped the schedule's own grant: %+v", second.Grants)
	}
	if second.Approval == nil {
		t.Fatalf("the accepted firing was held: %q", second.Body)
	}
	sched, err := store.GetSchedule(ctx, schedID)
	if err != nil || sched == nil || !slices.ContainsFunc(sched.Grants, func(g model.Grant) bool { return g.Capability == secrets }) {
		t.Fatalf("after two firings the schedule holds %+v (%v), want its own %s kept", sched, err, secrets)
	}
}
