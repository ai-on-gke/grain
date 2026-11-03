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

// A repository's default hostd (RepoConfig.DefaultHostd) pins what a
// schedule or a suite files against it when the template names none, and
// a template's own hostd wins over it.
func TestFiringsAgainstARepoTakeItsDefaultHostd(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	now := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	repo := model.RepoRef{Owner: "bwsalmon", Name: "grain"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, DefaultHostd: "pool=gpu"}); err != nil {
		t.Fatal(err)
	}
	if got, err := store.RepoDefaultHostd(ctx, repo); err != nil || got != "pool=gpu" {
		t.Fatalf("RepoDefaultHostd = %q, %v; want pool=gpu", got, err)
	}
	plain := model.Template{ID: "template-plain", Name: "Plain", Title: "Plain", Body: "anywhere", CreatedAt: now}
	pinned := model.Template{ID: "template-pinned", Name: "Pinned", Title: "Pinned", Body: "there", Hostd: "laptop", CreatedAt: now}
	for _, tmpl := range []model.Template{plain, pinned} {
		if err := store.PutTemplate(ctx, tmpl); err != nil {
			t.Fatal(err)
		}
	}

	for i, c := range []struct {
		tmpl string
		want string
	}{{plain.ID, "pool=gpu"}, {pinned.ID, "laptop"}} {
		tid := c.tmpl
		sched := model.Schedule{
			ID: "sched-" + c.tmpl, Title: "Fire", TemplateID: &tid, Target: repo,
			Recurrence: model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 60 + i},
			Overlap:    model.OverlapAllow, Enabled: true, NextRunAt: now, CreatedAt: now,
		}
		if err := store.PutSchedule(ctx, sched); err != nil {
			t.Fatal(err)
		}
		firing, err := store.FireSchedule(ctx, sched, now, time.UTC, model.RepoRef{})
		if err != nil || firing.TaskID == "" {
			t.Fatalf("firing %s: %+v, %v", c.tmpl, firing, err)
		}
		if got, err := store.TaskHostd(ctx, firing.TaskID); err != nil || got != c.want {
			t.Errorf("schedule firing %s is pinned to %q, %v; want %q", c.tmpl, got, err, c.want)
		}
	}

	suite := model.Suite{
		ID: "suite-hostd", Name: "Both", Items: []model.SuiteItem{{TemplateID: plain.ID}, {TemplateID: pinned.ID}},
		Mode: model.SuiteCount, Count: 1, CreatedAt: now,
	}
	if err := store.PutSuite(ctx, suite); err != nil {
		t.Fatal(err)
	}
	started, err := store.CreateSuiteRun(ctx, suite, repo, "main", now)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.GetSuiteRun(ctx, started.ID)
	if err != nil || run == nil {
		t.Fatal(err)
	}
	want := map[string]string{plain.ID: "pool=gpu", pinned.ID: "laptop"}
	if len(run.Tasks) != 2 {
		t.Fatalf("suite run filed %d tasks, want 2", len(run.Tasks))
	}
	for _, task := range run.Tasks {
		if got, err := store.TaskHostd(ctx, task.TaskID); err != nil || got != want[task.TemplateID] {
			t.Errorf("suite task from %s is pinned to %q, %v; want %q", task.TemplateID, got, err, want[task.TemplateID])
		}
	}

	startedOther, err := store.CreateSuiteRun(ctx, suite, model.RepoRef{Owner: "bwsalmon", Name: "other"}, "main", now)
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.GetSuiteRun(ctx, startedOther.ID)
	if err != nil || other == nil {
		t.Fatal(err)
	}
	for _, task := range other.Tasks {
		if got, _ := store.TaskHostd(ctx, task.TaskID); task.TemplateID == plain.ID && got != "" {
			t.Errorf("a suite task against a repo with no default hostd is pinned to %q", got)
		}
	}
}
