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

package staterepo_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// template.sandbox (model.Template.Sandbox, grain/task-4591) goes out to
// the state repository as the `sandbox: {cpus, memory-mb, disk-gb}` block
// a repository's own .grain templates write, and comes back through the
// settings import with the shape intact.
func TestATemplatesSandboxShapeRoundTripsThroughTheStateRepository(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	sized := model.Template{
		ID: "template-1", Name: "Publish", Title: "Publish images", Body: "push them",
		Sandbox: model.SandboxShape{CPUs: 4, DiskGB: 100}, CreatedAt: now,
	}
	plain := model.Template{
		ID: "template-2", Name: "Sweep", Title: "Sweep", Body: "sweep it", CreatedAt: now,
	}
	for _, tmpl := range []model.Template{sized, plain} {
		if err := store.PutTemplate(ctx, tmpl); err != nil {
			t.Fatalf("putting %s: %v", tmpl.ID, err)
		}
	}
	dir := t.TempDir()
	if err := staterepo.Export(ctx, db, dir); err != nil {
		t.Fatalf("exporting: %v", err)
	}
	path := staterepo.DumpPath("template")
	raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(path)))
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if !strings.Contains(string(raw), "sandbox: {cpus: 4, disk-gb: 100}") {
		t.Errorf("%s does not carry the sandbox block:\n%s", path, raw)
	}

	other, otherDB := openDB(t)
	if err := staterepo.ImportTables(ctx, otherDB, dir, staterepo.SettingsTables); err != nil {
		t.Fatalf("importing the settings tables: %v", err)
	}
	for _, want := range []model.Template{sized, plain} {
		got, err := other.GetTemplate(ctx, want.ID)
		if err != nil || got == nil {
			t.Fatalf("reading the imported %s: %v, %v", want.ID, got, err)
		}
		if got.Sandbox != want.Sandbox {
			t.Errorf("%s: Sandbox after the round trip = %+v, want %+v", want.ID, got.Sandbox, want.Sandbox)
		}
	}
}
