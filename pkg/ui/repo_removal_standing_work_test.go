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

// Removing a repository that has standing work in it (grain/task-4288):
// what the Remove confirmation is told beforehand, and for each kind of
// work whether the removal cleans it up or leaves it for its own pane.

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func (r *lifecycleRig) planTargetRepos(repos ...string) ui.TargetReposPlan {
	r.t.Helper()
	body, err := json.Marshal(ui.PlanTargetReposRequest{TargetRepos: repos})
	if err != nil {
		r.t.Fatal(err)
	}
	var plan ui.TargetReposPlan
	if err := json.Unmarshal([]byte(r.must(http.MethodPost, "/api/repos/plan", string(body))), &plan); err != nil {
		r.t.Fatal(err)
	}
	return plan
}

func TestRemovingARepositoryWithStandingWork(t *testing.T) {
	r := newLifecycleRig(t)
	ctx, store := r.ctx, r.store
	r.must(http.MethodPost, "/api/repos", `{"repo":"acme/gadgets"}`)
	r.must(http.MethodPost, "/api/repos", `{"repo":"acme/widgets"}`)
	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}

	task, err := r.client.CreateTask(ctx, ui.CreateTaskRequest{Title: "Fix the widget", Repo: "acme/widgets"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutTemplate(ctx, model.Template{ID: "tpl-smoke", Name: "smoke", Title: "Smoke", Target: &widgets}); err != nil {
		t.Fatal(err)
	}
	tmpl := "tpl-smoke"
	if err := store.PutSchedule(ctx, model.Schedule{
		ID: "nightly-widgets", Title: "Nightly", Target: widgets, Enabled: true, TemplateID: &tmpl,
		Overlap:    model.OverlapSkip,
		Recurrence: model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 3 * 60},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutSuite(ctx, model.Suite{ID: "suite-smoke", Name: "smoke",
		Items: []model.SuiteItem{{TemplateID: tmpl}}, Mode: model.SuiteCount, Count: 1}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutQualificationPlan(ctx, model.QualificationPlan{Repo: widgets, Configured: true,
		RequireApproval: true, Items: []model.QualificationItem{{TemplateID: tmpl, Repeat: 1}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutScheduledWorkflowRun(ctx, model.ScheduledWorkflowRun{
		ScheduleID: model.RepoDeclaredID(model.DeclaredSchedule, widgets, "nightly"), Stem: "nightly",
		Repo: widgets, Branch: "main", Workflow: ".grain/workflows/nightly.yml", FiredAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	r.must(http.MethodPut, "/api/repos/acme/widgets/watch-default-branch", `{"enabled":true}`)
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{Repo: widgets, Branch: "main", RunID: "ci-1",
		Commit: "abc123", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	// What the Remove confirmation is told before the click: the task,
	// the schedule and the qualification plan, by name, in one sentence.
	plan := r.planTargetRepos("acme/gadgets")
	if len(plan.TargetReposStranded) != 1 {
		t.Fatalf("plan for removing acme/widgets = %+v, want one stranded repository", plan)
	}
	stranded := plan.TargetReposStranded[0]
	if stranded.Repo != "acme/widgets" || !slices.Equal(stranded.Tasks, []string{task.ID}) ||
		!slices.Equal(stranded.Schedules, []string{"nightly-widgets"}) || !stranded.QualificationPlan {
		t.Fatalf("stranded = %+v", stranded)
	}
	for _, want := range []string{"`acme/widgets`", "1 task", "nightly-widgets", "qualification"} {
		if !strings.Contains(stranded.Summary, want) {
			t.Errorf("summary %q does not mention %q", stranded.Summary, want)
		}
	}
	// A list that keeps acme/widgets strands nothing.
	if got := r.planTargetRepos("acme/gadgets", "acme/widgets"); len(got.TargetReposStranded) != 0 {
		t.Fatalf("a plan that removes nothing = %+v", got)
	}

	r.must(http.MethodDelete, "/api/repos/acme/widgets", "")

	// Cleaned up: the configuration and standing state that only exist to
	// run work in the repository.
	if p, err := store.GetQualificationPlan(ctx, widgets); err != nil || p != nil {
		t.Errorf("qualification plan after removal = %+v, %v; want forgotten", p, err)
	}
	if runs, err := store.ListScheduledWorkflowRuns(ctx); err != nil || len(runs) != 0 {
		t.Errorf("scheduled workflow firings after removal = %+v, %v; want none", runs, err)
	}
	if b, err := store.GetBaseBranchCI(ctx, widgets, "main"); err != nil || b != nil {
		t.Errorf("base-branch watch after removal = %+v, %v; want forgotten", b, err)
	}
	if rc, err := store.GetRepoConfig(ctx, widgets); err != nil || rc != nil {
		t.Errorf("repo_config after removal = %+v, %v; want forgotten", rc, err)
	}

	// Left for their own panes: the operator made them, and deleting a
	// schedule is not something putting the repository back would undo.
	if s, err := store.GetSchedule(ctx, "nightly-widgets"); err != nil || s == nil {
		t.Errorf("operator schedule after removal = %+v, %v; want kept", s, err)
	}
	if tp, err := store.GetTemplate(ctx, tmpl); err != nil || tp == nil {
		t.Errorf("operator template after removal = %+v, %v; want kept", tp, err)
	}
	if su, err := store.GetSuite(ctx, "suite-smoke"); err != nil || su == nil {
		t.Errorf("suite after removal = %+v, %v; want kept", su, err)
	}
	if _, err := r.client.GetTask(ctx, task.ID); err != nil {
		t.Errorf("task after removal: %v", err)
	}

	// Off the list, it still has the parked task and the schedule that
	// fires nothing, and no qualification plan any more.
	if got := r.planTargetRepos("acme/gadgets"); len(got.TargetReposStranded) != 1 ||
		got.TargetReposStranded[0].QualificationPlan || len(got.TargetReposStranded[0].Schedules) != 1 {
		t.Fatalf("plan after the removal = %+v", got)
	}
	// Re-adding starts with no qualification plan inherited.
	r.must(http.MethodPost, "/api/repos", `{"repo":"acme/widgets"}`)
	if p, err := store.GetQualificationPlan(ctx, widgets); err != nil || p != nil {
		t.Errorf("re-added repository inherited a qualification plan: %+v, %v", p, err)
	}
}
