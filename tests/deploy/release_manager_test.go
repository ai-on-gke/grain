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

package deploy

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

const releaseManagerTemplatePath = ".grain/templates/release-manager.yml"

// The grants the release manager needs (design/playbook/releases.playbook.md
// section 16), each limited to this repository but self-debug, whose
// tasks:read is how it reads the edition's release tasks.
var releaseManagerGrants = []string{
	`https://grain.dev/capabilities/api#release-manager={"repo":"ai-on-gke/grain"}`,
	`https://grain.dev/capabilities/api#task-admin={"repo":"ai-on-gke/grain"}`,
	`https://grain.dev/capabilities/api#dispatch-control={"repo":"ai-on-gke/grain"}`,
	`https://grain.dev/capabilities/api#ci-read={"repo":"ai-on-gke/grain"}`,
	"https://grain.dev/capabilities/api#self-debug",
}

func releaseManagerDeclarations(t *testing.T) (model.Template, map[string]model.Schedule) {
	t.Helper()
	decl := declarations(t)
	if len(decl.Refused) > 0 {
		t.Fatalf("declarations refused by this build: %+v", decl.Refused)
	}
	var tmpl *model.Template
	for _, f := range decl.Templates {
		if f.Path == releaseManagerTemplatePath {
			tmpl = &f.Template
		}
	}
	if tmpl == nil {
		t.Fatalf("%s is not declared", releaseManagerTemplatePath)
	}
	scheds := map[string]model.Schedule{}
	for _, f := range decl.Schedules {
		if f.Schedule.TemplateID != nil && *f.Schedule.TemplateID == tmpl.ID {
			scheds[f.Path] = f.Schedule
		}
	}
	return *tmpl, scheds
}

// A release manager starts for every release: when the train pushes an rc
// tag, and when a patch's release/vX.Y.Z branch is built. Each schedule
// holds further firings while its manager is open.
func TestAReleaseManagerStartsForEveryRelease(t *testing.T) {
	_, scheds := releaseManagerDeclarations(t)
	for path, want := range map[string][2]string{
		".grain/schedules/release-manager.yml":       {"release-train-assets.yml", "v*-rc.*"},
		".grain/schedules/release-manager-patch.yml": {"release.yml", "release/v*"},
	} {
		sched, ok := scheds[path]
		if !ok {
			t.Errorf("%s does not fire %s", path, releaseManagerTemplatePath)
			continue
		}
		trig := sched.Recurrence.GitHubWorkflowRun
		if sched.Recurrence.Kind != model.RecurrenceGitHubWorkflowRun || trig == nil ||
			trig.Workflow != want[0] || !slices.Equal(trig.Branches, []string{want[1]}) {
			t.Errorf("%s fires on %+v, want %s on %s", path, sched.Recurrence, want[0], want[1])
			continue
		}
		if !slices.Equal(trig.Conclusions, []string{"success", "failure"}) {
			t.Errorf("%s fires on conclusions %v, want [success failure]", path, trig.Conclusions)
		}
		if sched.Overlap != model.OverlapSkip {
			t.Errorf("%s overlap = %q, want skip: one version, one manager", path, sched.Overlap)
		}
	}
}

// The manager's grants are none that only a person may give, so a firing
// of its repo-declared schedule is filed approved, holding them, with no
// one's acceptance (model.Store.composeScheduleTask, DeclaredFiringHold).
// Adding a person-only grant here would hold every manager for a person
// until somebody accepted the set in Settings -> Capabilities.
func TestTheReleaseManagerLandsApprovedWithItsGrants(t *testing.T) {
	tmpl, scheds := releaseManagerDeclarations(t)
	var declared []string
	for _, g := range tmpl.Grants {
		declared = append(declared, g.Capability)
		if model.DeclaredGrantNeedsPerson(g.Capability) {
			t.Errorf("%s declares %s, which only a person may grant", releaseManagerTemplatePath, g.Capability)
		}
		if model.IsDangerousGrant(g.Capability) {
			t.Errorf("%s declares dangerous %s", releaseManagerTemplatePath, g.Capability)
		}
	}
	for _, want := range releaseManagerGrants {
		if !slices.Contains(declared, want) {
			t.Errorf("%s does not declare %s (declares %q)", releaseManagerTemplatePath, want, declared)
		}
	}
	if grants := model.APIGrantsOf(declared); grants.Allows("merge:force", "ai-on-gke/grain") {
		t.Errorf("%s may force-merge; a release manager never does", releaseManagerTemplatePath)
	}

	ctx := context.Background()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTemplate(ctx, tmpl); err != nil {
		t.Fatal(err)
	}
	sched := scheds[".grain/schedules/release-manager.yml"]
	sched.Enabled = true
	if err := store.PutSchedule(ctx, sched); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 12, 4, 0, 0, 0, time.UTC)
	id, filed, err := store.FileWorkflowReview(ctx, sched, model.ScheduledWorkflowRun{
		ScheduleID: sched.ID, Repo: model.GrainSourceRepo(), Branch: "v3.7.0-rc.1",
		Workflow: ".github/workflows/release-train-assets.yml", Stem: "release-train-assets",
		FiredAt: now, RunID: "1", Attempts: 1, Conclusion: "success",
	}, model.RepoRef{}, now)
	if err != nil || !filed {
		t.Fatalf("filing a manager: (%q, %v, %v)", id, filed, err)
	}
	task, err := store.GetTask(ctx, id)
	if err != nil || task == nil {
		t.Fatalf("reading the manager: (%+v, %v)", task, err)
	}
	if task.Approval == nil {
		t.Fatalf("the manager was filed for a person's approval:\n%s", task.Body)
	}
	if task.Title != "Release manager for the newest rc" {
		t.Errorf("title = %q, want the field's default until the run pins its version", task.Title)
	}
	var held []string
	for _, g := range task.Grants {
		held = append(held, g.Capability)
	}
	for _, want := range releaseManagerGrants {
		if !slices.Contains(held, want) {
			t.Errorf("the filed manager does not hold %s (holds %q)", want, held)
		}
	}
}
