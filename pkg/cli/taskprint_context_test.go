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

package cli

import (
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// TestTaskBlockRendersContextManifests is the regression for the §7
// walkthrough of docs/test-plans/2026-11-07.md: `grain show` named each
// pinned committed-context folder but dropped the context.yml manifest the
// sandbox discovered in it, though the Task detail page renders it.
func TestTaskBlockRendersContextManifests(t *testing.T) {
	sha := "2879df476108b048b551d73be67151bac7671af2"
	got := taskBlock(ui.Task{
		ID: "7", Title: "Implement architecture", State: model.StateQueued, Repo: "acme/app",
		Context: []ui.ContextRefView{
			{Repo: "acme/specs", Path: "architecture.md", Rev: sha, Placement: "read"},
			{Repo: "acme/specs", Path: "plc/042", Rev: sha, Placement: "read", Manifest: &model.ContextManifest{
				Title:       "PLC-042 billing spec",
				Description: "Approved by design review",
				Files: []model.ContextManifestFile{
					{Path: "spec.md", Kind: "design", Title: "Billing spec", SourceURL: "https://planner.example.com/PLC-042"},
					{Path: "notes.md"},
				},
			}},
		},
	})
	for _, want := range []string{
		"context:    acme/specs:architecture.md@2879df476108, acme/specs:plc/042@2879df476108\n",
		"  acme/specs:plc/042@2879df476108: PLC-042 billing spec\n",
		"    Approved by design review\n",
		"    [design] spec.md -- Billing spec <https://planner.example.com/PLC-042>\n",
		"    notes.md\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("taskBlock missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\n  acme/specs:architecture.md@") {
		t.Errorf("a reference without a manifest got a manifest block:\n%s", got)
	}
}
