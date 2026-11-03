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

// A template's hostd selector (Template.Hostd) is carried by every
// composition of it, whichever firing point composes it.
func TestComposeCarriesTheTemplatesHostd(t *testing.T) {
	tmpl := composeTemplate()
	tmpl.Hostd = "gpu=yes"
	repo := composeRepo("bwsalmon", "grain")
	for name, comp := range map[string]model.Composition{
		"schedule fires it":        model.ComposeScheduleTask(composeSchedule(), &tmpl),
		"suite runs it":            model.ComposeSuiteTask(tmpl, repo, "main", false, false, false),
		"a plan qualifies with it": model.ComposeQualificationTask(tmpl, repo, "cand", false),
		"somebody files it":        model.ComposeTemplateTask(tmpl, nil, repo, "main", true),
	} {
		if comp.Hostd != "gpu=yes" || comp.Sources.Hostd != model.LayerTemplate {
			t.Errorf("%s: Hostd = %q from %q, want the template's gpu=yes", name, comp.Hostd, comp.Sources.Hostd)
		}
	}
	if comp := model.ComposeScheduleTask(composeSchedule(), nil); comp.Hostd != "" {
		t.Errorf("an inline schedule's firing has hostd %q, want the default hostd", comp.Hostd)
	}
}

// A template's hostd is stored with it, and a schedule firing it files a
// task pinned to that hostd.
func TestFiringAScheduleFilesTheTemplatesHostd(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	now := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	tmpl := model.Template{
		ID: "template-h", Name: "Train", Title: "Train", Body: "train it",
		Hostd: "gpu=yes", CreatedAt: now,
	}
	if err := store.PutTemplate(ctx, tmpl); err != nil {
		t.Fatalf("putting the template: %v", err)
	}
	if got, err := store.GetTemplate(ctx, tmpl.ID); err != nil || got == nil || got.Hostd != "gpu=yes" {
		t.Fatalf("template read back = %+v, %v; want hostd gpu=yes", got, err)
	}
	tid := tmpl.ID
	sched := model.Schedule{
		ID: "sched-hostd", Title: "Train", TemplateID: &tid,
		Target:     model.RepoRef{Owner: "bwsalmon", Name: "grain"},
		Recurrence: model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 60},
		Overlap:    model.OverlapAllow, Enabled: true, NextRunAt: now, CreatedAt: now,
	}
	if err := store.PutSchedule(ctx, sched); err != nil {
		t.Fatal(err)
	}
	firing, err := store.FireSchedule(ctx, sched, now, time.UTC, model.RepoRef{})
	if err != nil || firing.TaskID == "" {
		t.Fatalf("firing: %+v, %v", firing, err)
	}
	if got, err := store.TaskHostd(ctx, firing.TaskID); err != nil || got != "gpu=yes" {
		t.Errorf("task the firing filed is pinned to %q, %v; want gpu=yes", got, err)
	}
}
