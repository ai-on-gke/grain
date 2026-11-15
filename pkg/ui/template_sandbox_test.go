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

// A template's sandbox shape (model.Template.Sandbox, grain/task-4591)
// through the API: shown on the template, carried onto a task filed from
// it, and overridden one dimension at a time by a filing that states its
// own.

import (
	"errors"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestATemplatesSandboxShapeIsShownAndFiledWithAndOverridable(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)

	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "publish-images", Title: "Publish the images",
		Sandbox: &model.SandboxShape{CPUs: 4, DiskGB: 100},
	})
	if err != nil {
		t.Fatalf("CreateTemplate: %v", err)
	}
	if tmpl.Sandbox == nil || *tmpl.Sandbox != (model.SandboxShape{CPUs: 4, DiskGB: 100}) {
		t.Fatalf("CreateTemplate Sandbox = %+v, want 4 vCPU / 100 GiB", tmpl.Sandbox)
	}
	listed, err := c.ListTemplates(ctx)
	if err != nil {
		t.Fatalf("ListTemplates: %v", err)
	}
	var shown *model.SandboxShape
	for _, l := range listed {
		if l.ID == tmpl.ID {
			shown = l.Sandbox
		}
	}
	if shown == nil || shown.DiskGB != 100 {
		t.Errorf("listed Sandbox = %+v, want the template's shape", shown)
	}

	// Filed from the template, saying nothing about size: the template's.
	inherited, err := c.CreateTask(ctx, ui.CreateTaskRequest{TemplateID: tmpl.ID})
	if err != nil {
		t.Fatalf("CreateTask(from template): %v", err)
	}
	stored, err := store.GetTask(ctx, inherited.ID)
	if err != nil || stored == nil {
		t.Fatalf("GetTask(%s) = %v, %v", inherited.ID, stored, err)
	}
	if stored.SandboxCPUs != 4 || stored.SandboxMemoryMB != 0 || stored.SandboxDiskGB != 100 {
		t.Errorf("inherited sandbox = %d/%d/%d, want 4/0/100",
			stored.SandboxCPUs, stored.SandboxMemoryMB, stored.SandboxDiskGB)
	}

	// A filing that states a dimension keeps it, and the template fills
	// the rest.
	overridden, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		TemplateID: tmpl.ID, SandboxDiskGB: 200, SandboxMemoryMB: 16384,
	})
	if err != nil {
		t.Fatalf("CreateTask(from template, explicit size): %v", err)
	}
	stored, err = store.GetTask(ctx, overridden.ID)
	if err != nil || stored == nil {
		t.Fatalf("GetTask(%s) = %v, %v", overridden.ID, stored, err)
	}
	if stored.SandboxCPUs != 4 || stored.SandboxMemoryMB != 16384 || stored.SandboxDiskGB != 200 {
		t.Errorf("overridden sandbox = %d/%d/%d, want 4/16384/200",
			stored.SandboxCPUs, stored.SandboxMemoryMB, stored.SandboxDiskGB)
	}

	// Editing: replaced wholesale, and {} clears it.
	updated, err := c.UpdateTemplate(ctx, tmpl.ID, ui.UpdateTemplateRequest{Sandbox: &model.SandboxShape{DiskGB: 50}})
	if err != nil {
		t.Fatalf("UpdateTemplate: %v", err)
	}
	if updated.Sandbox == nil || *updated.Sandbox != (model.SandboxShape{DiskGB: 50}) {
		t.Errorf("updated Sandbox = %+v, want 50 GiB alone", updated.Sandbox)
	}
	cleared, err := c.UpdateTemplate(ctx, tmpl.ID, ui.UpdateTemplateRequest{Sandbox: &model.SandboxShape{}})
	if err != nil {
		t.Fatalf("UpdateTemplate(clear): %v", err)
	}
	if cleared.Sandbox != nil {
		t.Errorf("cleared Sandbox = %+v, want none", cleared.Sandbox)
	}
}

func TestATemplatesSandboxShapeIsValidated(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	for _, bad := range []model.SandboxShape{
		{CPUs: -1},
		{DiskGB: model.DefaultMaxSandboxDiskGB + 1},
		{MemoryMB: 64},
	} {
		_, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{Name: "bad", Title: "Bad", Sandbox: &bad})
		var ve *ui.ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("CreateTemplate(sandbox %+v) = %v, want a ValidationError", bad, err)
		}
	}
}
