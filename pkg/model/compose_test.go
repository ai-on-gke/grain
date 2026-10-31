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

// compose.go's own tests: what each firing point composes, and -- the
// part that is new and the part a UI renders -- which layer it says
// decided each field. Nothing here touches a store: the composition
// itself is a pure function over the declarations, which is exactly why
// it is worth pinning down here rather than only through each firing
// point's end-to-end test.

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func composeRepo(owner, name string) model.RepoRef {
	return model.RepoRef{Owner: owner, Name: name}
}

func composeTemplate() model.Template {
	return model.Template{
		ID:            "template-1",
		Name:          "Dependency bump",
		Title:         "Bump the dependencies",
		Body:          "Bump everything and run the tests.",
		Reads:         []model.RepoRef{composeRepo("bwsalmon", "notes")},
		Writes:        []model.WriteTarget{{Repo: composeRepo("bwsalmon", "other"), Base: "main"}},
		Grants:        []model.Grant{{Capability: "github"}},
		CustomFields:  map[string][]string{"team": {"infra"}},
		NetworkAccess: model.NetworkNone,
		AutoMerge:     true,
	}
}

func composeSchedule() model.Schedule {
	return model.Schedule{
		ID:     "sched-1",
		Title:  "Weekly sweep",
		Body:   "Sweep the repo.",
		Target: composeRepo("bwsalmon", "grain"),
		Base:   "rel.latest",
	}
}

func TestComposeScheduleTaskInlineContentIsAllTheSchedules(t *testing.T) {
	sched := composeSchedule()
	sched.AutoMerge = true
	sched.Reads = []model.RepoRef{composeRepo("bwsalmon", "notes")}
	sched.Grants = []model.Grant{{Capability: "github"}}
	sched.CustomFields = map[string][]string{"team": {"infra"}}

	comp := model.ComposeScheduleTask(sched, nil)

	if comp.Title != "Weekly sweep" || comp.Body != "Sweep the repo." {
		t.Errorf("content = %q/%q, want the schedule's own", comp.Title, comp.Body)
	}
	if comp.Target == nil || *comp.Target != sched.Target || comp.Base != "rel.latest" {
		t.Errorf("placement = %v/%q, want the schedule's own", comp.Target, comp.Base)
	}
	if !comp.AutoMerge {
		t.Error("want the schedule's own AutoMerge")
	}
	if !comp.Approved {
		t.Error("a schedule's firing lands approved: creating the schedule was the approval")
	}
	if comp.NetworkAccess != "" {
		t.Errorf("NetworkAccess = %q, want empty: this schedule fixes none", comp.NetworkAccess)
	}
	if comp.TemplateID != "" {
		t.Errorf("TemplateID = %q, want empty for inline content", comp.TemplateID)
	}
	want := model.CompositionSources{
		Title: model.LayerSchedule, Body: model.LayerSchedule,
		Target: model.LayerSchedule, Base: model.LayerSchedule,
		Reads: model.LayerSchedule, Writes: model.LayerSchedule, Grants: model.LayerSchedule,
		CustomFields: model.LayerSchedule, NetworkAccess: model.LayerDeployment, Model: model.LayerTeam,
		NothingNeededAction: model.LayerSchedule,
		AutoMerge:           model.LayerSchedule, AutoApproveProposals: model.LayerSchedule, Approved: model.LayerSchedule,
		Interactive: model.LayerSchedule, KeepRunning: model.LayerSchedule,
	}
	if comp.Sources != want {
		t.Errorf("sources = %+v, want %+v", comp.Sources, want)
	}
}

// An inline schedule's own network access is content like its reads and
// its capabilities (grain/task-427): the schedule said it, so the
// composition carries it and says the schedule decided it -- the cell
// docs/composition.md's table used to have to write "deployment" in.
func TestComposeScheduleTaskInlineNetworkAccessIsTheSchedules(t *testing.T) {
	sched := composeSchedule()
	sched.NetworkAccess = model.NetworkNone

	comp := model.ComposeScheduleTask(sched, nil)

	if comp.NetworkAccess != model.NetworkNone {
		t.Errorf("NetworkAccess = %q, want the schedule's own", comp.NetworkAccess)
	}
	if comp.Sources.NetworkAccess != model.LayerSchedule {
		t.Errorf("network access decided by %q, want the schedule", comp.Sources.NetworkAccess)
	}
	task := comp.Task("task-1", model.SchedulePrincipal, model.ReasonSchedule, time.Now())
	if task.NetworkAccess != model.NetworkNone {
		t.Errorf("filed task's NetworkAccess = %q, want the schedule's own", task.NetworkAccess)
	}
}

// A template that fixes no network decides nothing about it either: the
// deployment answers when the run is dispatched, and the preview says so
// rather than crediting the template with a value it does not carry
// (model.networkSourceOf).
func TestComposeNetworkAccessNobodyFixedIsTheDeployments(t *testing.T) {
	tmpl := composeTemplate()
	tmpl.NetworkAccess = ""

	for _, tc := range []struct {
		name string
		comp model.Composition
	}{
		{"schedule fires it", model.ComposeScheduleTask(composeSchedule(), &tmpl)},
		{"suite runs it", model.ComposeSuiteTask(tmpl, composeRepo("bwsalmon", "grain"), "main", false, false, false)},
		{"a plan qualifies with it", model.ComposeQualificationTask(tmpl, composeRepo("bwsalmon", "grain"), "cand", false)},
	} {
		if tc.comp.NetworkAccess != "" {
			t.Errorf("%s: NetworkAccess = %q, want empty", tc.name, tc.comp.NetworkAccess)
		}
		if tc.comp.Sources.NetworkAccess != model.LayerDeployment {
			t.Errorf("%s: network access decided by %q, want the deployment",
				tc.name, tc.comp.Sources.NetworkAccess)
		}
	}
}

func TestComposeScheduleTaskTemplateContentBeatsTheSchedules(t *testing.T) {
	sched := composeSchedule()
	// Everything a schedule keeps as its display cache while it fires a
	// template -- stale on purpose here, to prove none of it is read.
	sched.Title, sched.Body, sched.AutoMerge = "stale", "stale", false
	sched.CustomFields = map[string][]string{"team": {"stale"}}
	sched.NetworkAccess = model.NetworkOpen
	tmpl := composeTemplate()

	comp := model.ComposeScheduleTask(sched, &tmpl)

	if comp.Title != tmpl.Title || comp.Body != tmpl.Body {
		t.Errorf("content = %q/%q, want the template's", comp.Title, comp.Body)
	}
	if !comp.AutoMerge {
		t.Error("want the template's AutoMerge, not the schedule's cached copy")
	}
	if comp.NetworkAccess != model.NetworkNone {
		t.Errorf("NetworkAccess = %q, want the template's", comp.NetworkAccess)
	}
	if got := comp.CustomFields["team"]; len(got) != 1 || got[0] != "infra" {
		t.Errorf("custom fields = %v, want the template's", comp.CustomFields)
	}
	// An unbound template leaves the placement to the schedule.
	if comp.Target == nil || *comp.Target != sched.Target || comp.Base != "rel.latest" {
		t.Errorf("placement = %v/%q, want the schedule's own", comp.Target, comp.Base)
	}
	if comp.Sources.Target != model.LayerSchedule || comp.Sources.Title != model.LayerTemplate {
		t.Errorf("sources = %+v, want the placement from the schedule and the content from the template", comp.Sources)
	}
	if comp.Sources.Approved != model.LayerSchedule || !comp.Approved {
		t.Errorf("approval = %v from %q, want approved by the schedule", comp.Approved, comp.Sources.Approved)
	}
	if comp.TemplateID != tmpl.ID || comp.TemplateName != tmpl.Name {
		t.Errorf("template = %s/%s, want %s/%s", comp.TemplateID, comp.TemplateName, tmpl.ID, tmpl.Name)
	}
}

func TestComposeScheduleTaskBoundTemplateBeatsTheSchedulesPlacement(t *testing.T) {
	sched := composeSchedule()
	tmpl := composeTemplate()
	bound := composeRepo("bwsalmon", "kontur")
	tmpl.Target, tmpl.Base = &bound, "release"

	comp := model.ComposeScheduleTask(sched, &tmpl)

	if comp.Target == nil || *comp.Target != bound || comp.Base != "release" {
		t.Errorf("placement = %v/%q, want the binding's %v/%q", comp.Target, comp.Base, bound, "release")
	}
	if comp.Sources.Target != model.LayerTemplate || comp.Sources.Base != model.LayerTemplate {
		t.Errorf("sources = %+v, want the placement from the template", comp.Sources)
	}
}

func TestComposeScheduleTaskWithNoRepoHasNoTarget(t *testing.T) {
	// The state only one of grain's own system templates is ever in: its
	// work is a comment or a store write, so there is no repository to
	// name and "" / "" must not be filed as one.
	sched := composeSchedule()
	sched.Target, sched.Base = model.RepoRef{}, ""

	if comp := model.ComposeScheduleTask(sched, nil); comp.Target != nil {
		t.Errorf("Target = %v, want nil for a firing with no repo", comp.Target)
	}
}

func TestComposeScheduleTaskCombinesScheduleAndTemplateGrants(t *testing.T) {
	tmpl := composeTemplate()
	tmpl.Grants = []model.Grant{
		{Capability: "self-debug", Via: ".grain/templates/nightly.yml"},
		{Capability: "gcp-key:deploy", Via: ".grain/templates/nightly.yml"},
	}

	sched := composeSchedule()
	sched.Grants = []model.Grant{
		{Capability: "gcp-key:deploy", Via: ".grain/schedules/nightly.yml"},
		{Capability: "package-publish", Via: ".grain/schedules/nightly.yml"},
	}

	comp := model.ComposeScheduleTask(sched, &tmpl)
	if len(comp.Grants) != 3 {
		t.Fatalf("Grants = %+v, want 3 combined grants", comp.Grants)
	}
	if comp.Grants[0].Capability != "self-debug" || comp.Grants[0].Via != ".grain/templates/nightly.yml" {
		t.Errorf("Grants[0] = %+v, want self-debug via template", comp.Grants[0])
	}
	if comp.Grants[1].Capability != "gcp-key:deploy" || comp.Grants[1].Via != ".grain/templates/nightly.yml" {
		t.Errorf("Grants[1] = %+v, want gcp-key:deploy via template (retained)", comp.Grants[1])
	}
	if comp.Grants[2].Capability != "package-publish" || comp.Grants[2].Via != ".grain/schedules/nightly.yml" {
		t.Errorf("Grants[2] = %+v, want package-publish via schedule", comp.Grants[2])
	}
	if comp.Sources.Grants != model.LayerSchedule {
		t.Errorf("Sources.Grants = %q, want schedule", comp.Sources.Grants)
	}
	wantGrantSources := []model.CompositionLayer{model.LayerTemplate, model.LayerTemplate, model.LayerSchedule}
	if len(comp.GrantSources) != len(wantGrantSources) {
		t.Fatalf("GrantSources = %v, want %v", comp.GrantSources, wantGrantSources)
	}
	for i, want := range wantGrantSources {
		if comp.GrantSources[i] != want {
			t.Errorf("GrantSources[%d] = %q, want %q", i, comp.GrantSources[i], want)
		}
	}
}

// A template-backed schedule carries a copy of its template's grants
// (ui.scheduleContentFromTemplate). That copy adds nothing, so the
// capabilities are still the template's to credit, not the schedule's
// (2026-10-15 recurring/composed walkthrough: the schedule editor's
// preview said a template's capabilities came "from the schedule").
func TestComposeScheduleTaskCreditsCopiedGrantsToTheTemplate(t *testing.T) {
	tmpl := composeTemplate()
	tmpl.Grants = []model.Grant{{Capability: "gcp-key"}, {Capability: "memory"}}

	sched := composeSchedule()
	sched.Grants = tmpl.Grants

	comp := model.ComposeScheduleTask(sched, &tmpl)
	if len(comp.Grants) != 2 {
		t.Fatalf("Grants = %+v, want the template's 2", comp.Grants)
	}
	if comp.Sources.Grants != model.LayerTemplate {
		t.Errorf("Sources.Grants = %q, want template", comp.Sources.Grants)
	}
}

func TestComposeSuiteTaskTakesDispositionFromTheRun(t *testing.T) {
	tmpl := composeTemplate() // AutoMerge true
	target := composeRepo("bwsalmon", "grain")

	comp := model.ComposeSuiteTask(tmpl, target, "main", true, false, true)

	if comp.AutoMerge {
		t.Error("want the run's AutoMerge (off), not the template's (on)")
	}
	if comp.Sources.AutoMerge != model.LayerSuite {
		t.Errorf("AutoMerge source = %q, want the suite", comp.Sources.AutoMerge)
	}
	if !comp.AutoApproveProposals {
		t.Error("want the run's AutoApproveProposals (on)")
	}
	if comp.Sources.AutoApproveProposals != model.LayerSuite {
		t.Errorf("AutoApproveProposals source = %q, want the suite", comp.Sources.AutoApproveProposals)
	}
	if comp.Approved {
		t.Error("a run that requires approval files its tasks unapproved")
	}
	if comp.Sources.Approved != model.LayerSuite {
		t.Errorf("Approved source = %q, want the suite", comp.Sources.Approved)
	}
	if comp.Title != tmpl.Title || comp.Sources.Title != model.LayerTemplate {
		t.Errorf("content = %q from %q, want the template's", comp.Title, comp.Sources.Title)
	}
	if comp.Target == nil || *comp.Target != target || comp.Base != "main" {
		t.Errorf("placement = %v/%q, want the run's own", comp.Target, comp.Base)
	}
	if comp.Sources.Target != model.LayerSuite {
		t.Errorf("Target source = %q, want the suite", comp.Sources.Target)
	}
}

func TestComposeSuiteTaskBoundTemplateStillPlacesItself(t *testing.T) {
	tmpl := composeTemplate()
	bound := composeRepo("bwsalmon", "kontur")
	tmpl.Target = &bound

	comp := model.ComposeSuiteTask(tmpl, composeRepo("bwsalmon", "grain"), "main", false, true, false)

	if comp.Target == nil || *comp.Target != bound {
		t.Errorf("Target = %v, want the binding's %v", comp.Target, bound)
	}
	// A bound template with no branch of its own means the bound repo's
	// default branch, never the run's branch of another repo.
	if comp.Base != "" {
		t.Errorf("Base = %q, want empty: the run's branch belongs to another repo", comp.Base)
	}
	if comp.Sources.Base != model.LayerTemplate {
		t.Errorf("Base source = %q, want the template", comp.Sources.Base)
	}
}

func TestComposeQualificationTaskPinsTheCandidateAndKeepsTheTemplatesAutoMerge(t *testing.T) {
	tmpl := composeTemplate() // AutoMerge true
	// A binding does not move a qualification task: the plan refuses a
	// template bound elsewhere outright, and one bound to the candidate's
	// own repo must still run against the candidate's branch.
	bound := composeRepo("bwsalmon", "grain")
	tmpl.Target, tmpl.Base = &bound, "main"
	candidate := composeRepo("bwsalmon", "grain")

	comp := model.ComposeQualificationTask(tmpl, candidate, "grain/task-1", false)

	if comp.Target == nil || *comp.Target != candidate || comp.Base != "grain/task-1" {
		t.Errorf("placement = %v/%q, want the candidate's own branch", comp.Target, comp.Base)
	}
	if comp.Sources.Target != model.LayerCandidate || comp.Sources.Base != model.LayerCandidate {
		t.Errorf("sources = %+v, want the placement from the candidate", comp.Sources)
	}
	if !comp.AutoMerge || comp.Sources.AutoMerge != model.LayerTemplate {
		t.Errorf("AutoMerge = %v from %q, want the template's: a plan has no switch of its own",
			comp.AutoMerge, comp.Sources.AutoMerge)
	}
	if !comp.Approved || comp.Sources.Approved != model.LayerPlan {
		t.Errorf("approval = %v from %q, want approved by the plan", comp.Approved, comp.Sources.Approved)
	}
}

func TestComposeDropsAWriteNamingItsOwnTarget(t *testing.T) {
	tmpl := composeTemplate()
	target := composeRepo("bwsalmon", "grain")
	tmpl.Writes = append(tmpl.Writes, model.WriteTarget{Repo: target, Base: "main"})

	comp := model.ComposeSuiteTask(tmpl, target, "main", false, true, false)

	for _, w := range comp.Writes {
		if w.Repo.Same(target) {
			t.Errorf("writes = %+v, want the entry naming the task's own target dropped", comp.Writes)
		}
	}
	if len(comp.Writes) != 1 {
		t.Errorf("writes = %+v, want the one entry that names another repo", comp.Writes)
	}
}

func TestCompositionTaskCarriesWhatWasComposed(t *testing.T) {
	tmpl := composeTemplate()
	comp := model.ComposeSuiteTask(tmpl, composeRepo("bwsalmon", "grain"), "main", false, false, true)

	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	task := comp.Task("task-9", model.SuitePrincipal, model.ReasonSuite, now)

	if task.ID != "task-9" || task.Title != tmpl.Title || task.Body != tmpl.Body {
		t.Errorf("task = %+v, want the composition's content under the given id", task)
	}
	if task.Intent != model.IntentImplement || task.Binding != model.BindingDirective {
		t.Errorf("task = %+v, want an implement/directive task", task)
	}
	if task.Origin.Reason != model.ReasonSuite || task.Origin.Attribution.Actor != model.SuitePrincipal {
		t.Errorf("origin = %+v, want the suite's", task.Origin)
	}
	if task.Approval == nil || task.Approval.Actor != model.SuitePrincipal || task.ApprovedAt == nil {
		t.Errorf("approval = %+v/%v, want it stamped for the filing actor", task.Approval, task.ApprovedAt)
	}
	if !task.AutoApproveProposals {
		t.Errorf("task.AutoApproveProposals = %v, want true", task.AutoApproveProposals)
	}
	if task.AutoMerge {
		t.Error("want the composition's AutoMerge (off)")
	}
	if task.NetworkAccess != tmpl.NetworkAccess {
		t.Errorf("NetworkAccess = %q, want the composition's %q", task.NetworkAccess, tmpl.NetworkAccess)
	}

	unapproved := comp
	unapproved.Approved = false
	if task := unapproved.Task("task-10", model.SuitePrincipal, model.ReasonSuite, now); task.Approval != nil {
		t.Errorf("approval = %+v, want none for an unapproved composition", task.Approval)
	}
}
