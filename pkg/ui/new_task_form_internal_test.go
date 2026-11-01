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

package ui

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// A field the resolved form hides files with its default whatever the
// request says; the repository's own layer is picked after a hidden
// repository is applied, and a shown field is left alone.
func TestApplyHiddenFormDefaults(t *testing.T) {
	// Through JSON, as grain_config.new_task_form stores it.
	var form model.NewTaskForm
	if err := json.Unmarshal([]byte(`{
		"default": {"fields": {
			"repo": {"visibility": "hidden", "default": "ai-on-gke/grain"},
			"model": {"visibility": "shown", "default": "m1"},
			"sandboxShape": {"visibility": "hidden", "default_preset": "large",
				"presets": [{"id": "large", "cpus": 8, "memory_mb": 16384, "disk_gb": 60}]}
		}},
		"repos": {"ai-on-gke/grain": {"fields": {
			"networkAccess": {"visibility": "hidden", "default": "none"},
			"grants": {"visibility": "hidden", "default": ["corp-research"]}
		}}},
		"boards": {"triage": {"fields": {"approved": {"visibility": "hidden", "default": false},
			"atFront": {"visibility": "hidden", "default": true}}}}
	}`), &form); err != nil {
		t.Fatal(err)
	}
	req := CreateTaskRequest{Repo: "other/repo", Model: "mine", NetworkAccess: "open", Approved: true, BoardID: "triage"}
	if err := applyHiddenFormDefaults(&req, &form); err != nil {
		t.Fatal(err)
	}
	if req.Repo != "ai-on-gke/grain" || req.NetworkAccess != "none" || req.Model != "mine" || req.Approved {
		t.Errorf("req = %+v", req)
	}
	if req.Capabilities == nil || !slices.Equal(*req.Capabilities, []string{"corp-research"}) {
		t.Errorf("capabilities = %v", req.Capabilities)
	}
	if req.AtFront == nil || !*req.AtFront {
		t.Errorf("atFront = %v", req.AtFront)
	}
	if req.SandboxCPUs != 8 || req.SandboxMemoryMB != 16384 || req.SandboxDiskGB != 60 {
		t.Errorf("sandbox shape = %d/%d/%d", req.SandboxCPUs, req.SandboxMemoryMB, req.SandboxDiskGB)
	}
	// No configuration changes nothing.
	plain := CreateTaskRequest{Repo: "other/repo", NetworkAccess: "open"}
	if err := applyHiddenFormDefaults(&plain, nil); err != nil || plain.Repo != "other/repo" || plain.NetworkAccess != "open" {
		t.Errorf("nil form: %+v, %v", plain, err)
	}
}
