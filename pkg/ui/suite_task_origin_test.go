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

package ui_test

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ui"
)

// A task a suite run filed names that run on its own page -- which pass
// of which suite, and the run's id to link to -- the other direction
// from the run's pane, which already links to each of its tasks (found
// in the 2026-10-13 recurring/composed walkthrough, step 4). A task
// whose run is gone keeps only its plain SuiteRun chip.
func TestGetTaskNamesTheSuiteRunThatFiledIt(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{Name: "Sweep", Title: "Sweep for bugs"})
	if err != nil {
		t.Fatalf("creating a template: %v", err)
	}
	suite, err := c.CreateSuite(ctx, ui.CreateSuiteRequest{
		Name: "walk4-suite", TemplateIDs: []string{tmpl.ID}, Mode: "count", Count: 2,
	})
	if err != nil {
		t.Fatalf("creating a suite: %v", err)
	}
	run, err := c.CreateSuiteRun(ctx, ui.CreateSuiteRunRequest{
		SuiteID: suite.ID, Repo: "acme/widgets", Base: "main",
	})
	if err != nil {
		t.Fatalf("creating a suite run: %v", err)
	}
	stored, err := store.GetSuiteRun(ctx, run.ID)
	if err != nil || stored == nil {
		t.Fatalf("reading the run back: %v, %v", stored, err)
	}
	second, err := store.FireNextPass(ctx, *stored, time.Now())
	if err != nil {
		t.Fatalf("firing pass 2: %v", err)
	}
	passTwo := second.PassTasks(2)
	if len(passTwo) != 1 {
		t.Fatalf("pass 2 has %d tasks, want 1", len(passTwo))
	}

	detail, err := c.GetTask(ctx, passTwo[0].TaskID)
	if err != nil {
		t.Fatalf("reading pass 2's task: %v", err)
	}
	if !detail.SuiteRun {
		t.Error("want the task marked SuiteRun")
	}
	want := ui.TaskSuiteRunOrigin{RunID: run.ID, SuiteName: "walk4-suite", Pass: 2, TemplateName: "Sweep"}
	if detail.FiledBySuiteRun == nil || *detail.FiledBySuiteRun != want {
		t.Errorf("FiledBySuiteRun = %+v, want %+v", detail.FiledBySuiteRun, want)
	}

	// A task nobody's suite filed carries nothing.
	plain, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "by hand", Repo: "acme/widgets"})
	if err != nil {
		t.Fatalf("filing a task by hand: %v", err)
	}
	if d, err := c.GetTask(ctx, plain.ID); err != nil || d.FiledBySuiteRun != nil {
		t.Errorf("hand-filed task: FiledBySuiteRun = %+v, err %v; want nil", d.FiledBySuiteRun, err)
	}

	// The run's row gone, the task degrades to the plain chip rather
	// than linking to a run the suites page cannot show.
	if _, err := store.DB().ExecContext(ctx, "DELETE FROM `suite_run` WHERE `id` = ?", run.ID); err != nil {
		t.Fatalf("deleting the run: %v", err)
	}
	detail, err = c.GetTask(ctx, passTwo[0].TaskID)
	if err != nil {
		t.Fatalf("reading pass 2's task after its run is gone: %v", err)
	}
	if !detail.SuiteRun || detail.FiledBySuiteRun != nil {
		t.Errorf("after deleting the run: SuiteRun = %v, FiledBySuiteRun = %+v; want true, nil",
			detail.SuiteRun, detail.FiledBySuiteRun)
	}
}
