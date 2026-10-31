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
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestCreateAndUpdateTaskPriority(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)

	def, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "default priority"})
	if err != nil {
		t.Fatalf("CreateTask (default): %v", err)
	}
	if def.Priority != model.DefaultTaskPriority {
		t.Errorf("default task Priority = %d, want %d", def.Priority, model.DefaultTaskPriority)
	}
	storedDef, err := store.GetTask(ctx, def.ID)
	if err != nil || storedDef == nil {
		t.Fatalf("GetTask(%s): %v", def.ID, err)
	}
	if storedDef.Priority != model.DefaultTaskPriority {
		t.Errorf("stored default task Priority = %d, want %d", storedDef.Priority, model.DefaultTaskPriority)
	}

	p8 := 8
	high, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "high priority", Priority: &p8})
	if err != nil {
		t.Fatalf("CreateTask (priority=8): %v", err)
	}
	if high.Priority != 8 {
		t.Errorf("high task Priority = %d, want 8", high.Priority)
	}

	p2 := 2
	updated, err := c.UpdateTask(ctx, high.ID, ui.UpdateTaskRequest{Priority: &p2})
	if err != nil {
		t.Fatalf("UpdateTask (priority=2): %v", err)
	}
	if updated.Priority != 2 {
		t.Errorf("updated task Priority = %d, want 2", updated.Priority)
	}
	storedUpdated, err := store.GetTask(ctx, high.ID)
	if err != nil || storedUpdated == nil {
		t.Fatalf("GetTask(%s): %v", high.ID, err)
	}
	if storedUpdated.Priority != 2 {
		t.Errorf("stored updated task Priority = %d, want 2", storedUpdated.Priority)
	}

	neg := -1
	if _, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "bad", Priority: &neg}); err == nil {
		t.Fatal("CreateTask(priority=-1) succeeded, want ValidationError")
	} else {
		var ve *ui.ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("CreateTask(priority=-1) error = %v, want ValidationError", err)
		}
	}
	if _, err := c.UpdateTask(ctx, high.ID, ui.UpdateTaskRequest{Priority: &neg}); err == nil {
		t.Fatal("UpdateTask(priority=-1) succeeded, want ValidationError")
	}
}

func TestTasksAdminCapabilityAllowsEditingPriorityViaREST(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	seedAPITask(t, client, "1", "acme/widgets", "api:tasks:admin")
	seedAPITask(t, client, "2", "acme/widgets")

	// api:tasks:admin can edit priority on an existing task.
	code, body := asAgent(t, srv, "1", http.MethodPatch, "/api/tasks/2", `{"priority": 9}`, "")
	if code != http.StatusOK {
		t.Fatalf("PATCH /api/tasks/2 with priority=9: status %d (%s), want 200", code, body)
	}
	var got ui.Task
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Priority != 9 {
		t.Errorf("response Priority = %d, want 9", got.Priority)
	}
	stored, err := client.Store.GetTask(context.Background(), "2")
	if err != nil || stored.Priority != 9 {
		t.Errorf("stored Priority = %d (err=%v), want 9", stored.Priority, err)
	}

	// api:tasks:admin without api:tasks:write cannot edit title.
	if code, _ := asAgent(t, srv, "1", http.MethodPatch, "/api/tasks/2", `{"title": "hijacked"}`, ""); code != http.StatusForbidden {
		t.Errorf("PATCH /api/tasks/2 with title edit under api:tasks:admin: status %d, want 403", code)
	}
}
