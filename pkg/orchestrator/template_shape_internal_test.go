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

package orchestrator

// A schedule firing a template that says `sandbox: {disk-gb: 60}` files a
// task carrying 60 GiB, and on a host-directory deployment every run of
// it failed setup ("asks for 0 vCPU/0 MiB/60 GiB but a host-directory
// sandbox has no shape of its own") and was retried for ever
// (docs/test-runs/2026-11-13-schedules-templates.md, F2). A size that
// came from the template is not asked of a backend with none to give; a
// size somebody stated on the task still is, and is still refused.

import (
	"context"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestATemplatesSandboxSizeIsNotAskedOfAHostDirectoryBackend(t *testing.T) {
	ctx := context.Background()
	store := openInternalStore(t)
	if err := store.PutTemplate(ctx, model.Template{
		ID: "big", Name: "Big", Title: "Build the big image", Body: "Needs a big disk.",
		Sandbox: model.SandboxShape{DiskGB: 60},
	}); err != nil {
		t.Fatal(err)
	}
	host := NewHostSandboxes(t.TempDir())
	shaped := struct{ Sandboxes }{} // any backend that is not shapeless

	cases := []struct {
		name     string
		task     model.Task
		backend  Sandboxes
		shape    Shape
		wantDrop bool
	}{
		{"fired from the template", model.Task{ID: "1", TemplateID: "big"}, host, Shape{DiskGB: 60}, true},
		{"proposed from the template", model.Task{ID: "2", SourceTemplateID: "big"}, host, Shape{DiskGB: 60}, true},
		{"a person's own disk", model.Task{ID: "3", TemplateID: "big"}, host, Shape{DiskGB: 80}, false},
		{"a person's own memory beside the template's disk", model.Task{ID: "4", TemplateID: "big"}, host,
			Shape{MemoryMB: 4096, DiskGB: 60}, false},
		{"no template", model.Task{ID: "5"}, host, Shape{DiskGB: 60}, false},
		{"a backend with sizes", model.Task{ID: "6", TemplateID: "big"}, shaped, Shape{DiskGB: 60}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.shape.NetworkAccess = model.NetworkOpen
			got, dropped := templateShapeDropped(ctx, store, c.backend, c.task, c.shape)
			if dropped != c.wantDrop {
				t.Fatalf("dropped = %v, want %v (shape %+v)", dropped, c.wantDrop, got)
			}
			if c.wantDrop {
				if !got.SizeIsZero() || got.NetworkAccess != model.NetworkOpen {
					t.Fatalf("shape = %+v, want no size and the network access kept", got)
				}
				return
			}
			if got != c.shape {
				t.Fatalf("shape = %+v, want it untouched: %+v", got, c.shape)
			}
		})
	}

	// And what the backend says to each: the template's size dropped is
	// acquired, a person's own is still refused by name.
	if _, err := host.Acquire(ctx, "kept", Shape{DiskGB: 80}); err == nil ||
		!strings.Contains(err.Error(), "has no shape of its own") {
		t.Fatalf("Acquire(80 GiB) = %v, want the host-directory refusal", err)
	}
}
