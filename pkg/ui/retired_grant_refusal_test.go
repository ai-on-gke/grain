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

// docs/test-runs/2026-11-14-retired-capability-aliases.md: once the
// retired bare names stopped granting (PR #1455), every path that records
// a grant refused one as "unknown capability grain-ci" -- true, and no
// help to the person whose template or script still says grain-ci. Each
// refusal now names the canonical grant the bare name became.

import (
	"context"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestRetiredBareNamesAreRefusedNamingWhatTheyBecame(t *testing.T) {
	t.Parallel()
	_, c := testServer(t)
	ctx := context.Background()
	repo := "acme/widgets"
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "holder"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"self-debug", "grain-log", "read-issues", "github-sandbox", "bootstrap-playbooks",
		"github-credential:bot", "claude-proxy", "gemini-key", "gcp-key", "gcp-key:ops",
		"grain-ci", "image-signing", "registry-push",
	} {
		became := model.RetiredGrantsBecame(name)
		if len(became) == 0 || became[0] == name {
			t.Fatalf("%q is not retired any longer; drop it from this test", name)
		}
		names := func(err error) bool {
			return err != nil && strings.Contains(err.Error(), name+" is a retired capability name") &&
				strings.Contains(err.Error(), became[0]) && !strings.Contains(err.Error(), "unknown capability")
		}
		if _, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "bare", Capabilities: &[]string{name}}); !names(err) {
			t.Errorf("CreateTask with %q: %v", name, err)
		}
		if err := c.SetCapability(ctx, task.ID, name, true); !names(err) {
			t.Errorf("SetCapability %q: %v", name, err)
		}
		if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{DefaultCapabilities: &[]string{name}}); !names(err) {
			t.Errorf("UpdateSettings default %q: %v", name, err)
		}
		if _, err := c.SetRepoDefaultCapabilities(ctx, repo, []string{name}); !names(err) {
			t.Errorf("SetRepoDefaultCapabilities %q: %v", name, err)
		}
		if _, err := c.SetRepoCICapabilities(ctx, repo, []string{name}); !names(err) {
			t.Errorf("SetRepoCICapabilities %q: %v", name, err)
		}
	}
	// grain-ci and the CI-only release grants also say why the canonical
	// grant is not a task's to hold either.
	if _, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "bare", Capabilities: &[]string{"grain-ci"}}); err == nil ||
		!strings.Contains(err.Error(), "Use Grain CI") {
		t.Errorf("grain-ci refusal does not point at the CI Mode: %v", err)
	}
	if _, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "bare", Capabilities: &[]string{"image-signing"}}); err == nil ||
		!strings.Contains(err.Error(), "is a CI capability, not a task grant") {
		t.Errorf("image-signing refusal does not say it is CI-only: %v", err)
	}
}
