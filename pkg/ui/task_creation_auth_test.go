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

func TestTaskCreationDirectEmptyCreatorValidation(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	c.Config.Actor = model.Principal{} // clear default actor

	_, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "no creator task",
	})
	if err == nil || !strings.Contains(err.Error(), "creator is required") {
		t.Fatalf("expected creator is required error, got %v", err)
	}
}

func TestTaskCreationCapabilityPermissions(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	// 1. Requesting CI-only capability (e.g. package-publish) on task creation is rejected
	caps := []string{"package-publish"}
	_, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title:        "ci cap task",
		Creator:      "alice",
		Capabilities: &caps,
	})
	if err == nil || !strings.Contains(err.Error(), "package-publish is a CI capability, not a task grant") {
		t.Fatalf("expected CI capability rejection, got %v", err)
	}

	// 2. Allowed capability creates task successfully
	allowedCaps := []string{"self-debug"}
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title:        "valid cap task",
		Creator:      "alice",
		Capabilities: &allowedCaps,
	})
	if err != nil {
		t.Fatalf("CreateTask with self-debug failed: %v", err)
	}

	// 3. SetCapability with CI capability is rejected
	err = c.SetCapability(ctx, task.ID, "package-publish", true)
	if err == nil || !strings.Contains(err.Error(), "package-publish is a CI capability, not a task grant") {
		t.Fatalf("expected SetCapability CI rejection, got %v", err)
	}
}
