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

// Template.Interactive: a template stores it, and every firing point
// that turns a template into a task -- a schedule, a suite pass, a
// qualification run, a filing straight from the form -- carries it onto
// the task it files, so that task is a live chat with whoever watches it.

import (
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestATemplatesInteractiveSwitchRoundTrips(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	for _, on := range []bool{false, true} {
		id := "template-chat-off"
		if on {
			id = "template-chat-on"
		}
		if err := store.PutTemplate(ctx, model.Template{
			ID: id, Name: id, Title: "Set up a cluster", CreatedAt: firstNight, Interactive: on,
		}); err != nil {
			t.Fatalf("writing %s: %v", id, err)
		}
		got, err := store.GetTemplate(ctx, id)
		if err != nil || got == nil {
			t.Fatalf("reading %s: (%+v, %v)", id, got, err)
		}
		if got.Interactive != on {
			t.Errorf("%s: Interactive = %v, want %v", id, got.Interactive, on)
		}
	}
	list, err := store.ListTemplates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, tmpl := range list {
		if tmpl.Interactive != (tmpl.ID == "template-chat-on") {
			t.Errorf("listed %s: Interactive = %v", tmpl.ID, tmpl.Interactive)
		}
	}
}

func TestEveryFiringOfAnInteractiveTemplateFilesAnInteractiveTask(t *testing.T) {
	t.Parallel()
	tmpl := model.Template{ID: "template-1", Name: "cluster", Title: "Set up a cluster", Interactive: true}
	templateID := tmpl.ID
	sched := model.Schedule{ID: "sched-1", TemplateID: &templateID, Target: nightlyRepo}
	now := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	for name, comp := range map[string]model.Composition{
		"schedule":      model.ComposeScheduleTask(sched, &tmpl),
		"suite":         model.ComposeSuiteTask(tmpl, nightlyRepo, "", false, false, false),
		"qualification": model.ComposeQualificationTask(tmpl, nightlyRepo, "candidate", false),
		"filing":        model.ComposeTemplateTask(tmpl, nil, nightlyRepo, "", true),
	} {
		if !comp.Interactive || comp.Sources.Interactive != model.LayerTemplate {
			t.Errorf("%s: Interactive = %v from %q, want true from the template", name, comp.Interactive, comp.Sources.Interactive)
		}
		if task := comp.Task("task-1", model.SchedulePrincipal, model.ReasonSchedule, now); !task.Interactive {
			t.Errorf("%s: the filed task is not interactive", name)
		}
	}

	// An inline schedule has nothing to say it with.
	inline := model.ComposeScheduleTask(model.Schedule{ID: "sched-2", Title: "Sweep", Target: nightlyRepo}, nil)
	if inline.Interactive {
		t.Error("an inline schedule's firing is interactive")
	}
}
