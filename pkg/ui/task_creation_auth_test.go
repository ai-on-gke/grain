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
	caps := []string{model.PackagePublishRef + "={}"}
	_, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title:        "ci cap task",
		Creator:      "alice",
		Capabilities: &caps,
	})
	if err == nil || !strings.Contains(err.Error(), "#package-publish={} is a CI capability, not a task grant") {
		t.Fatalf("expected CI capability rejection, got %v", err)
	}

	// 2. Allowed capability creates task successfully
	allowedCaps := []string{"api:self-debug"}
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title:        "valid cap task",
		Creator:      "alice",
		Capabilities: &allowedCaps,
	})
	if err != nil {
		t.Fatalf("CreateTask with self-debug failed: %v", err)
	}

	// 3. SetCapability with CI capability is rejected
	err = c.SetCapability(ctx, task.ID, model.PackagePublishRef+"={}", true)
	if err == nil || !strings.Contains(err.Error(), "#package-publish={} is a CI capability, not a task grant") {
		t.Fatalf("expected SetCapability CI rejection, got %v", err)
	}
}

// A CI read of another repository (repo-read:<repo>) is refused when a
// task is filed or proposed with it, naming the reads entry a task reads
// that repository through instead, rather than accepted and refused by
// every dispatch after (task 4532).
func TestTaskCreationRefusesCIRepoReadNamingReads(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	parent, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "release sync", Creator: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	caps := []string{"repo-read:acme/lib"}
	for name, req := range map[string]ui.CreateTaskRequest{
		"filed":    {Title: "pin core", Creator: "alice", Capabilities: &caps},
		"proposed": {Title: "pin core", Creator: "alice", Capabilities: &caps, ProposedBy: parent.ID},
	} {
		_, err := c.CreateTask(ctx, req)
		if err == nil || !strings.Contains(err.Error(), "is a CI capability, not a task grant") ||
			!strings.Contains(err.Error(), `reads: ["acme/lib"]`) {
			t.Errorf("%s with repo-read:acme/lib: err = %v, want a CI-capability refusal naming reads", name, err)
		}
	}
	if err := c.SetCapability(ctx, parent.ID, "repo-read:acme/lib", true); err == nil ||
		!strings.Contains(err.Error(), `reads: ["acme/lib"]`) {
		t.Errorf("SetCapability repo-read:acme/lib: err = %v, want a CI-capability refusal naming reads", err)
	}
}

// A task already holding a CI capability -- filed before filing refused
// one -- is refused approval, naming the grant and its task equivalent,
// rather than queued for every dispatch to refuse (task 4532).
func TestApproveRefusesATaskHoldingACIOnlyCapability(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	held := model.Task{ID: "41", Title: "pin core", Body: "b",
		Grants: []model.Grant{{Capability: "repo-read:acme/lib", Via: model.GrantByLabel}}}
	if err := store.PutTask(ctx, held); err != nil {
		t.Fatal(err)
	}
	err := c.Approve(ctx, held.ID)
	if err == nil || !strings.Contains(err.Error(), "is a CI capability, not a task grant") ||
		!strings.Contains(err.Error(), `reads: ["acme/lib"]`) {
		t.Fatalf("Approve with repo-read:acme/lib: err = %v, want a CI-capability refusal naming reads", err)
	}
	if err := c.RunNow(ctx, held.ID); err == nil || !strings.Contains(err.Error(), "is a CI capability, not a task grant") {
		t.Fatalf("RunNow with repo-read:acme/lib: err = %v, want a CI-capability refusal", err)
	}
	got, err := store.GetTask(ctx, held.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Approval != nil {
		t.Errorf("task approved holding a CI capability: %+v", got.Approval)
	}
}
