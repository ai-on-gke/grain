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

// A template's sandbox shape (model.Template.Sandbox, grain/task-4591):
// a publish job that needs 100 GB of disk got the 30 GB default at every
// scheduled firing, because a template had no way to say so.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestATemplatesSandboxShapeRoundTripsThroughTheStore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	created := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	for _, tmpl := range []model.Template{
		{ID: "template-1", Name: "publish", Title: "Publish", Sandbox: model.SandboxShape{DiskGB: 100}, CreatedAt: created},
		{ID: "template-2", Name: "plain", Title: "Plain", CreatedAt: created},
	} {
		if err := store.PutTemplate(ctx, tmpl); err != nil {
			t.Fatalf("PutTemplate(%s): %v", tmpl.ID, err)
		}
	}
	got, err := store.GetTemplate(ctx, "template-1")
	if err != nil || got == nil {
		t.Fatalf("GetTemplate = %v, %v", got, err)
	}
	if want := (model.SandboxShape{DiskGB: 100}); got.Sandbox != want {
		t.Errorf("Sandbox = %+v, want %+v", got.Sandbox, want)
	}
	plain, err := store.GetTemplate(ctx, "template-2")
	if err != nil || plain == nil || !plain.Sandbox.IsZero() {
		t.Errorf("plain template = %+v, %v; want no sandbox shape", plain, err)
	}
}

func TestAScheduleFiringATemplateFilesItsSandboxShape(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	start := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	if err := store.PutTemplate(ctx, model.Template{
		ID: "template-9", Name: "publish-images", Title: "Publish the images",
		Body:      "Build and push every image.",
		Sandbox:   model.SandboxShape{CPUs: 4, DiskGB: 100},
		CreatedAt: start.Add(-time.Hour),
	}); err != nil {
		t.Fatalf("PutTemplate: %v", err)
	}
	templateID := "template-9"
	sched := model.Schedule{
		ID: "publish", TemplateID: &templateID, Target: repo, Base: "main",
		Recurrence: model.Recurrence{Kind: model.RecurrenceEveryNMinutes, EveryNMinutes: 60},
		Enabled:    true, NextRunAt: start, CreatedAt: start.Add(-time.Hour),
	}
	if err := store.PutSchedule(ctx, sched); err != nil {
		t.Fatalf("PutSchedule: %v", err)
	}
	firing, err := store.FireSchedule(ctx, sched, start, time.UTC, model.RepoRef{})
	if err != nil || firing.TaskID == "" {
		t.Fatalf("FireSchedule = %+v, %v; want a task filed", firing, err)
	}
	task, err := store.GetTask(ctx, firing.TaskID)
	if err != nil || task == nil {
		t.Fatalf("GetTask(%s) = %v, %v", firing.TaskID, task, err)
	}
	if task.SandboxCPUs != 4 || task.SandboxMemoryMB != 0 || task.SandboxDiskGB != 100 {
		t.Errorf("filed task's sandbox = %d vCPU / %d MiB / %d GiB, want 4 / 0 (the default) / 100",
			task.SandboxCPUs, task.SandboxMemoryMB, task.SandboxDiskGB)
	}
}

func TestValidateTemplateSandbox(t *testing.T) {
	t.Parallel()
	for _, ok := range []model.SandboxShape{
		{},
		{DiskGB: 100},
		{CPUs: model.DefaultMaxSandboxCPUs, MemoryMB: model.DefaultMaxSandboxMemoryMB, DiskGB: model.DefaultMaxSandboxDiskGB},
		{MemoryMB: 128},
	} {
		if err := model.ValidateTemplateSandbox(ok); err != nil {
			t.Errorf("ValidateTemplateSandbox(%+v) = %v, want nil", ok, err)
		}
	}
	for _, tc := range []struct {
		shape model.SandboxShape
		want  string
	}{
		{model.SandboxShape{CPUs: -1}, "cpus is -1"},
		{model.SandboxShape{CPUs: model.DefaultMaxSandboxCPUs + 1}, "cpus is 33, and may be at most 32"},
		{model.SandboxShape{MemoryMB: 127}, "memory-mb is 127, and must be at least 128"},
		{model.SandboxShape{DiskGB: model.DefaultMaxSandboxDiskGB + 1}, "disk-gb is 1025"},
	} {
		err := model.ValidateTemplateSandbox(tc.shape)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("ValidateTemplateSandbox(%+v) = %v, want %q", tc.shape, err, tc.want)
		}
	}
}

func TestSandboxShapeEncodesAndDescribes(t *testing.T) {
	t.Parallel()
	s := model.SandboxShape{CPUs: 4, DiskGB: 100}
	if got := model.DecodeSandboxShape(model.EncodeSandboxShape(s)); got != s {
		t.Errorf("round trip = %+v, want %+v", got, s)
	}
	if got := model.EncodeSandboxShape(model.SandboxShape{}); got != "" {
		t.Errorf("EncodeSandboxShape(zero) = %q, want \"\"", got)
	}
	if got := model.DecodeSandboxShape("not json"); !got.IsZero() {
		t.Errorf("DecodeSandboxShape(garbage) = %+v, want zero", got)
	}
	if got, want := s.Describe(), "4 vCPU, 100 GiB disk"; got != want {
		t.Errorf("Describe = %q, want %q", got, want)
	}
}
