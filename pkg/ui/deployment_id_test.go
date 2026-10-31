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
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// A system admin can replace the random deployment id with a readable
// one, and Settings shows the random default alongside whichever is set
// so an empty box says what it means.
func TestDeploymentIDIsEditableAndDefaultsToTheRandomOne(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}
	instanceID, err := store.InstanceID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settings.DeploymentID != "" || settings.DeploymentIDDefault != instanceID[:8] {
		t.Fatalf("fresh settings: deploymentId %q, default %q; want empty and %q",
			settings.DeploymentID, settings.DeploymentIDDefault, instanceID[:8])
	}

	settings, err = c.UpdateSettings(ctx, ui.UpdateSettingsRequest{DeploymentID: ptr(" prod-east ")})
	if err != nil {
		t.Fatal(err)
	}
	if settings.DeploymentID != "prod-east" {
		t.Fatalf("deploymentId = %q, want prod-east", settings.DeploymentID)
	}
	if got, err := store.DeploymentID(ctx); err != nil || got != "prod-east" {
		t.Fatalf("store.DeploymentID = %q, %v; want prod-east", got, err)
	}

	for _, bad := range []string{"Prod", "a/b", "-x"} {
		if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{DeploymentID: ptr(bad)}); err == nil {
			t.Errorf("deploymentId %q was accepted, want a validation error", bad)
		}
	}

	// Empty hands the id back to the random default.
	if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{DeploymentID: ptr("")}); err != nil {
		t.Fatal(err)
	}
	if got, err := store.DeploymentID(ctx); err != nil || got != instanceID[:8] {
		t.Fatalf("cleared store.DeploymentID = %q, %v; want %q", got, err, instanceID[:8])
	}
}

// A deployment's first Settings save stores the unique branch template
// (grain/task-3091), and Settings reports it before then and offers it
// afterwards as taskBranchTemplateDefault. Clearing it back to empty is
// still allowed: that is grain/task-{id}, what a deployment from before
// the default keeps.
func TestFirstSettingsSaveStoresTheUniqueBranchTemplate(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	before, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if before.TaskBranchTemplate != model.DefaultTaskBranchTemplate || before.TaskBranchTemplateDefault != model.DefaultTaskBranchTemplate {
		t.Fatalf("unsaved settings: template %q, default %q; want %q for both",
			before.TaskBranchTemplate, before.TaskBranchTemplateDefault, model.DefaultTaskBranchTemplate)
	}
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}
	cfg, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TaskBranchTemplate != model.DefaultTaskBranchTemplate {
		t.Fatalf("stored template = %q, want %q", cfg.TaskBranchTemplate, model.DefaultTaskBranchTemplate)
	}

	settings, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{TaskBranchTemplate: ptr("")})
	if err != nil {
		t.Fatal(err)
	}
	if settings.TaskBranchTemplate != "" || settings.TaskBranchTemplateDefault != model.DefaultTaskBranchTemplate {
		t.Fatalf("cleared: template %q, default %q; want empty and %q",
			settings.TaskBranchTemplate, settings.TaskBranchTemplateDefault, model.DefaultTaskBranchTemplate)
	}
}

// Settings refuses a branch template whose {slug} would let a task's
// title make another task's branch name -- the two #4031's review
// pointed at, grain/task-{id}{slug} (task 1 titled "3" on untitled task
// 13's grain/task-13) and grain/{slug}/task-{id} (task 5 titled
// "d4b120da" on deployment d4b120da's grain/d4b120da/task-5) -- and keeps
// the template it had. The default, {slug} last after {id} and a
// hyphen, is accepted.
func TestSettingsRefusesABranchTemplateWhoseSlugCouldNameAnotherTask(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"grain/task-{id}{slug}", "grain/{slug}/task-{id}", "{slug}-task-{id}",
		"grain/{deployment}-task-{id}-{slug}", "grain/task-{id}-{slug}-x",
		strings.Repeat("a", 210) + "/{deployment}/task-{id}-{slug}"} {
		if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{TaskBranchTemplate: ptr(bad)}); err == nil {
			t.Errorf("taskBranchTemplate %q was accepted, want a validation error", bad)
		}
	}
	cfg, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TaskBranchTemplate != model.DefaultTaskBranchTemplate {
		t.Fatalf("stored template = %q after refused saves, want %q", cfg.TaskBranchTemplate, model.DefaultTaskBranchTemplate)
	}
	if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{TaskBranchTemplate: ptr("grain/task-{id}.lock")}); err == nil || !strings.Contains(err.Error(), "grain/task-123.lock") {
		t.Errorf("taskBranchTemplate grain/task-{id}.lock error = %v, want it naming grain/task-123.lock", err)
	}
	if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{TaskBranchTemplate: ptr("alice/{deployment}/task-{id}-{slug}")}); err != nil {
		t.Fatalf("taskBranchTemplate alice/{deployment}/task-{id}-{slug} = %v, want it accepted", err)
	}
}

func TestCreateTaskWithVeryLongNameProducesValidBranch(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{DeploymentID: ptr("prod-1")}); err != nil {
		t.Fatal(err)
	}

	longTitle := strings.Repeat("Ensure very long task names never break branch naming or task creation ", 60)
	created, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title:       longTitle,
		Description: "body",
		Repo:        "acme/widgets",
	})
	if err != nil {
		t.Fatalf("CreateTask with %d-char title failed: %v", len(longTitle), err)
	}
	if created.Title != longTitle {
		t.Fatalf("created.Title length = %d, want %d", len(created.Title), len(longTitle))
	}
	stored, err := store.GetTask(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetTask(%s): %v", created.ID, err)
	}
	wantBranch := "grain/prod-1/task-" + created.ID + "-ensure-very-long-task-names"
	if stored.BranchName() != wantBranch || !model.ValidBranchName(stored.BranchName()) {
		t.Fatalf("stored.BranchName() = %q, want valid %q", stored.BranchName(), wantBranch)
	}

	// Custom template close to MaxBranchNameLen trims {slug} to fit rather than failing task creation.
	longPrefix := strings.Repeat("a", 100) + "/" + strings.Repeat("b", 85)
	if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{
		TaskBranchTemplate: ptr(longPrefix + "/{deployment}/task-{id}-{slug}"),
	}); err != nil {
		t.Fatalf("saving long custom template failed: %v", err)
	}
	unbrokenTitle := strings.Repeat("supercalifragilisticexpialidocious", 60)
	created2, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title:       unbrokenTitle,
		Description: "body",
		Repo:        "acme/widgets",
	})
	if err != nil {
		t.Fatalf("CreateTask with unbroken %d-char title under long template failed: %v", len(unbrokenTitle), err)
	}
	stored2, err := store.GetTask(ctx, created2.ID)
	if err != nil {
		t.Fatalf("GetTask(%s): %v", created2.ID, err)
	}
	if len(stored2.BranchName()) > model.MaxBranchNameLen || !model.ValidBranchName(stored2.BranchName()) {
		t.Fatalf("stored2.BranchName() = %q (len %d), want valid <= %d", stored2.BranchName(), len(stored2.BranchName()), model.MaxBranchNameLen)
	}
}
