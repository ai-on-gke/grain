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

package model

import "testing"

// adjustGrainCIGrant is handed a template's or schedule's own Grants
// slice, which every composition made from that declaration shares. A
// slice with spare capacity must come back as a fresh array, not with the
// grain-ci grant written into the declaration's own backing store.
func TestAdjustGrainCIGrantDoesNotWriteIntoTheDeclaration(t *testing.T) {
	backing := make([]Grant, 1, 4)
	backing[0] = Grant{Capability: "self-debug", Via: GrantByLabel}
	declared := backing[:1]

	got := adjustGrainCIGrant(declared, true)
	if len(got) != 2 || got[1].Capability != GrainCIGrant {
		t.Fatalf("adjustGrainCIGrant(..., true) = %+v, want self-debug then grain#ci", got)
	}
	if spare := backing[:2][1]; !spare.Equal(Grant{}) {
		t.Fatalf("adjustGrainCIGrant wrote %+v into the declaration's spare capacity", spare)
	}

	// A second composition from the same declaration, for a repo that
	// does not use grain CI, must not see the first one's grant.
	if again := adjustGrainCIGrant(declared, false); len(again) != 1 || again[0].Capability != "self-debug" {
		t.Fatalf("adjustGrainCIGrant(..., false) = %+v, want only self-debug", again)
	}
}

func TestApplyGrainCIGrantCreditsTheRepoNotTheDeclaration(t *testing.T) {
	sched := Schedule{
		ID:     "sched-2",
		Title:  "Weekly Dependency & Vulnerability Audit",
		Target: RepoRef{Owner: "acme", Name: "widgets"},
		Grants: []Grant{{Capability: "self-debug", Via: GrantByLabel}},
	}
	comp := ComposeScheduleTask(sched, nil)
	applyGrainCIGrant(&comp, true)

	if len(comp.Grants) != 2 || comp.Grants[0].Capability != "self-debug" || comp.Grants[1].Capability != GrainCIGrant {
		t.Fatalf("Grants = %+v, want [self-debug, grain#ci]", comp.Grants)
	}
	if len(comp.GrantSources) != 2 || comp.GrantSources[0] != LayerSchedule || comp.GrantSources[1] != LayerRepo {
		t.Errorf("GrantSources = %v, want [%q, %q]", comp.GrantSources, LayerSchedule, LayerRepo)
	}
	if comp.Sources.Grants != LayerSchedule {
		t.Errorf("Sources.Grants = %q, want %q", comp.Sources.Grants, LayerSchedule)
	}

	// When the declaration itself asked for no capabilities, the repo is
	// the only layer that granted any.
	empty := ComposeScheduleTask(Schedule{
		ID:     "sched-1",
		Title:  "Sweep",
		Target: RepoRef{Owner: "acme", Name: "widgets"},
	}, nil)
	applyGrainCIGrant(&empty, true)
	if len(empty.Grants) != 1 || empty.Grants[0].Capability != GrainCIGrant {
		t.Fatalf("empty.Grants = %+v, want [grain-ci]", empty.Grants)
	}
	if empty.Sources.Grants != LayerRepo {
		t.Errorf("empty.Sources.Grants = %q, want %q", empty.Sources.Grants, LayerRepo)
	}
	if len(empty.GrantSources) != 1 || empty.GrantSources[0] != LayerRepo {
		t.Errorf("empty.GrantSources = %v, want [%q]", empty.GrantSources, LayerRepo)
	}
}

func TestWorkflowReviewCompositionGrantsRepoScopedCIReadWithoutGrainCI(t *testing.T) {
	repo := RepoRef{Owner: "acme", Name: "widgets"}
	tmpl := workflowReviewTemplate("release")
	templateID := tmpl.ID

	sched := Schedule{
		ID:         "sched-gh-review",
		Title:      "Review release workflow",
		Target:     repo,
		TemplateID: &templateID,
		Recurrence: Recurrence{
			Kind:              RecurrenceGitHubWorkflowRun,
			GitHubWorkflowRun: &GitHubWorkflowRunTrigger{Workflow: "release.yml"},
		},
	}

	comp := ComposeScheduleTask(sched, tmpl)
	applyGrainCIGrant(&comp, false)
	from, ok := scheduleReviewsWorkflowRun(sched, tmpl)
	if !ok || from != LayerTemplate {
		t.Fatalf("scheduleReviewsWorkflowRun = (%q, %v), want (%q, true)", from, ok, LayerTemplate)
	}
	applyWorkflowReviewCIReadGrant(&comp, from)

	wantGrant := APIGrantString("ci-read", "acme/widgets")
	if len(comp.Grants) != 1 || comp.Grants[0].Capability != wantGrant || comp.Grants[0].Via != GrantByGrain {
		t.Fatalf("comp.Grants = %+v, want [{Capability:%s Via:grain}]", comp.Grants, wantGrant)
	}
	if len(comp.GrantSources) != 1 || comp.GrantSources[0] != LayerTemplate {
		t.Errorf("comp.GrantSources = %v, want [%q]", comp.GrantSources, LayerTemplate)
	}
	if comp.Sources.Grants != LayerTemplate {
		t.Errorf("comp.Sources.Grants = %q, want %q", comp.Sources.Grants, LayerTemplate)
	}
	// Calling applyWorkflowReviewCIReadGrant again when ci:read is already
	// granted for the repository does not add a duplicate.
	applyWorkflowReviewCIReadGrant(&comp, from)
	if len(comp.Grants) != 1 {
		t.Errorf("duplicate ci-read grant added: %+v", comp.Grants)
	}

	// A github_workflow_run schedule firing an inline prompt or custom
	// template (not workflowReviewTemplate) credits LayerSchedule.
	customSched := Schedule{
		ID:     "sched-gh-custom",
		Title:  "Custom workflow review",
		Target: repo,
		Recurrence: Recurrence{
			Kind:              RecurrenceGitHubWorkflowRun,
			GitHubWorkflowRun: &GitHubWorkflowRunTrigger{Workflow: "release.yml"},
		},
	}
	customComp := ComposeScheduleTask(customSched, nil)
	applyGrainCIGrant(&customComp, false)
	from, ok = scheduleReviewsWorkflowRun(customSched, nil)
	if !ok || from != LayerSchedule {
		t.Fatalf("scheduleReviewsWorkflowRun(custom) = (%q, %v), want (%q, true)", from, ok, LayerSchedule)
	}
	applyWorkflowReviewCIReadGrant(&customComp, from)
	if len(customComp.Grants) != 1 || customComp.Grants[0].Capability != wantGrant {
		t.Fatalf("customComp.Grants = %+v, want [%s]", customComp.Grants, wantGrant)
	}
	if customComp.Sources.Grants != LayerSchedule || len(customComp.GrantSources) != 1 || customComp.GrantSources[0] != LayerSchedule {
		t.Errorf("customComp sources = %q / %v, want %q", customComp.Sources.Grants, customComp.GrantSources, LayerSchedule)
	}

	// Ordinary cron schedules on non-grain-CI repos do not get api:ci-read.
	ordinary := Schedule{
		ID:         "sched-ordinary",
		Title:      "Weekly audit",
		Target:     repo,
		Recurrence: Recurrence{Kind: RecurrenceDaily},
	}
	if _, ok := scheduleReviewsWorkflowRun(ordinary, nil); ok {
		t.Fatal("ordinary daily schedule reported as workflow-run review")
	}
}

// A template or schedule that asks for grain-ci, filed against a repository
// grain CI does not gate (ci_mode: github), loses grain#ci -- it would gate
// merges there -- but keeps read access to that repository's CI through
// api:ci-read@<repo>, so `grain ci list` / `grain ci checks` still answer.
// This is what starved the scheduled release train on ai-on-gke/grain.
func TestApplyGrainCIGrantKeepsCIReadForARequestedGrainCI(t *testing.T) {
	repo := RepoRef{Owner: "acme", Name: "widgets"}
	tmpl := Template{
		ID:     "tmpl-train",
		Name:   "Release train",
		Grants: []Grant{{Capability: "grain-ci", Via: GrantByLabel}, {Capability: "self-debug", Via: GrantByLabel}},
	}
	id := tmpl.ID
	sched := Schedule{ID: "sched-train", Title: "Release train", Target: repo, TemplateID: &id,
		Recurrence: Recurrence{Kind: RecurrenceDaily}}
	comp := ComposeScheduleTask(sched, &tmpl)
	applyGrainCIGrant(&comp, false)

	want := []Grant{{Capability: "self-debug", Via: GrantByLabel}, {Capability: APIGrantString("ci-read", "acme/widgets"), Via: GrantByGrain}}
	if len(comp.Grants) != len(want) || !comp.Grants[0].Equal(want[0]) || !comp.Grants[1].Equal(want[1]) {
		t.Fatalf("Grants = %+v, want %+v", comp.Grants, want)
	}
	if len(comp.GrantSources) != 2 || comp.GrantSources[1] != comp.GrantSources[0] || comp.GrantSources[1] == "" {
		t.Errorf("GrantSources = %v, want the ci-read grant credited to the declaring layer", comp.GrantSources)
	}
	if tmpl.Grants[0].Capability != "grain-ci" || len(tmpl.Grants) != 2 {
		t.Errorf("template's own grants were rewritten: %+v", tmpl.Grants)
	}

	// Applying it again (or to a declaration already holding ci:read on
	// the repository) adds no duplicate.
	applyGrainCIGrant(&comp, false)
	if len(comp.Grants) != 2 {
		t.Errorf("second apply: Grants = %+v, want no duplicate", comp.Grants)
	}
	held := ComposeScheduleTask(Schedule{ID: "s", Title: "t", Target: repo, Grants: []Grant{
		{Capability: "grain-ci", Via: GrantByLabel}, {Capability: "api:ci-read@acme/widgets", Via: GrantByLabel},
	}}, nil)
	applyGrainCIGrant(&held, false)
	if len(held.Grants) != 1 || held.Grants[0].Capability != "api:ci-read@acme/widgets" || held.Grants[0].Via != GrantByLabel {
		t.Errorf("held.Grants = %+v, want only the declared api:ci-read@acme/widgets", held.Grants)
	}

	// A grain#ci attached only as a repo or deployment default asked for
	// nothing: stripping it leaves no CI read behind.
	defaulted := ComposeScheduleTask(Schedule{ID: "d", Title: "t", Target: repo, Grants: []Grant{
		{Capability: GrainCIGrant, Via: GrantByDefault},
	}}, nil)
	applyGrainCIGrant(&defaulted, false)
	if len(defaulted.Grants) != 0 {
		t.Errorf("defaulted.Grants = %+v, want none", defaulted.Grants)
	}

	// With no target there is no repository to scope the read to.
	untargeted := Composition{Grants: []Grant{{Capability: "grain-ci", Via: GrantByLabel}}}
	applyGrainCIGrant(&untargeted, false)
	if len(untargeted.Grants) != 0 {
		t.Errorf("untargeted.Grants = %+v, want none", untargeted.Grants)
	}
}
