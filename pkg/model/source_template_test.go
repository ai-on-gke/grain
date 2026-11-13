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
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// A schedule firing a template records it as the template its task was
// filed from (Task.SourceTemplateID, grain/task-5273); one firing its own
// inline content records none.
func TestAScheduleFiringRecordsTheTemplateItFiled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	now := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	tmpl := model.Template{ID: "template-sweep", Name: "Sweep", Title: "Sweep", Body: "sweep it", CreatedAt: now}
	if err := store.PutTemplate(ctx, tmpl); err != nil {
		t.Fatalf("putting the template: %v", err)
	}
	fire := func(sched model.Schedule) model.Task {
		t.Helper()
		sched.Target = model.RepoRef{Owner: "acme", Name: "widgets"}
		sched.Recurrence = model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 60}
		sched.Overlap, sched.Enabled, sched.NextRunAt, sched.CreatedAt = model.OverlapAllow, true, now, now
		if err := store.PutSchedule(ctx, sched); err != nil {
			t.Fatalf("putting %s: %v", sched.ID, err)
		}
		firing, err := store.FireSchedule(ctx, sched, now, time.UTC, model.RepoRef{})
		if err != nil || firing.TaskID == "" {
			t.Fatalf("firing %s: %+v, %v", sched.ID, firing, err)
		}
		task, err := store.GetTask(ctx, firing.TaskID)
		if err != nil || task == nil {
			t.Fatalf("reading the filed task: %v, %v", task, err)
		}
		return *task
	}

	tid := tmpl.ID
	task := fire(model.Schedule{ID: "sched-from-template", Title: "Sweep", TemplateID: &tid})
	if task.SourceTemplateID != tmpl.ID || task.TemplateID != tmpl.ID {
		t.Errorf("a template's firing: template %q, source template %q, want both %q", task.TemplateID, task.SourceTemplateID, tmpl.ID)
	}
	task = fire(model.Schedule{ID: "sched-inline", Title: "Inline", Body: "do the inline thing"})
	if task.SourceTemplateID != "" || task.TemplateID != "" {
		t.Errorf("an inline firing: template %q, source template %q, want neither", task.TemplateID, task.SourceTemplateID)
	}
}

// The record survives every rewrite of the task -- a redispatch puts the
// same task back on the queue through PutTask -- and a source template
// recorded without a TemplateID stays a record: it is never read back as
// the TemplateID a push set or a template-restricted grant is judged by.
func TestTheSourceTemplateSurvivesARewriteAndGrantsNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	now := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	for _, tc := range []struct {
		name, templateID, source, want string
	}{
		{name: "filed from a template", templateID: "template-a", want: "template-a"},
		{name: "proposed naming a template", source: "template-b", want: "template-b"},
		{name: "written from scratch"},
	} {
		task := model.Task{
			ID: "t-" + tc.want + "-x", Title: tc.name, Body: "body", Target: &repo, Base: "main",
			Binding: model.BindingDirective, TemplateID: tc.templateID, SourceTemplateID: tc.source,
			Approval:   &model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "jane"}},
			ApprovedAt: &now, CreatedAt: &now,
		}
		if err := store.PutTask(ctx, task); err != nil {
			t.Fatalf("%s: putting the task: %v", tc.name, err)
		}
		got, err := store.GetTask(ctx, task.ID)
		if err != nil || got == nil {
			t.Fatalf("%s: reading the task: %v, %v", tc.name, got, err)
		}
		// Redispatched: the task comes back off the queue and is written
		// again with everything it was read with.
		got.Title += " (again)"
		if err := store.PutTask(ctx, *got); err != nil {
			t.Fatalf("%s: rewriting the task: %v", tc.name, err)
		}
		got, err = store.GetTask(ctx, task.ID)
		if err != nil || got == nil {
			t.Fatalf("%s: re-reading the task: %v, %v", tc.name, got, err)
		}
		if got.SourceTemplateID != tc.want {
			t.Errorf("%s: source template = %q, want %q", tc.name, got.SourceTemplateID, tc.want)
		}
		if got.TemplateID != tc.templateID {
			t.Errorf("%s: template = %q, want %q: a recorded source template is not a TemplateID", tc.name, got.TemplateID, tc.templateID)
		}
	}
}
