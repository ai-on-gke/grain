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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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

	// Templates also reject negative priorities and normalize 0 to unset.
	if _, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name:     "bad-tmpl",
		Title:    "bad",
		Priority: &neg,
	}); err == nil {
		t.Fatal("CreateTemplate(priority=-1) succeeded, want ValidationError")
	}
	zero := 0
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name:     "zero-tmpl",
		Title:    "default prio",
		Priority: &zero,
	})
	if err != nil {
		t.Fatalf("CreateTemplate(priority=0): %v", err)
	}
	if tmpl.Priority != nil {
		t.Errorf("CreateTemplate(priority=0) Priority = %v, want nil", *tmpl.Priority)
	}
	if _, err := c.UpdateTemplate(ctx, tmpl.ID, ui.UpdateTemplateRequest{Priority: &neg}); err == nil {
		t.Fatal("UpdateTemplate(priority=-1) succeeded, want ValidationError")
	}
}

func TestTasksAdminCapabilityAllowsEditingPriorityViaREST(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	seedAPITask(t, client, "1", "acme/widgets", "api:tasks:admin")
	seedAPITask(t, client, "2", "acme/widgets")
	seedAPITask(t, client, "3", "acme/widgets", "api:tasks:admin@acme/widgets")
	seedAPITask(t, client, "4", "other/repo")
	seedAPITask(t, client, "5", "acme/widgets", "api:tasks:write")

	origTask2, err := client.Store.GetTask(context.Background(), "2")
	if err != nil || origTask2 == nil {
		t.Fatalf("GetTask(2): %v", err)
	}

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

	// api:tasks:admin without api:tasks:write cannot edit title or toggle priority-dispatch.
	if code, _ := asAgent(t, srv, "1", http.MethodPatch, "/api/tasks/2", `{"title": "hijacked"}`, ""); code != http.StatusForbidden {
		t.Errorf("PATCH /api/tasks/2 with title edit under api:tasks:admin: status %d, want 403", code)
	}
	for _, grantBody := range []string{`{"priority": true}`, `{"priority": false}`, `{"priorityDispatch": true}`, `{"priority_dispatch": true}`} {
		if code, resp := asAgent(t, srv, "1", http.MethodPatch, "/api/tasks/2", grantBody, "testing"); code != http.StatusForbidden {
			t.Errorf("PATCH /api/tasks/2 with %s under api:tasks:admin: status %d (%s), want 403", grantBody, code, resp)
		}
	}

	// Regression: concatenated JSON objects or non-object JSON must not bypass
	// tasksAdminTaskPatchBodyAllowed or agentFilingEscalation via json.Decoder.
	for _, smuggled := range []string{
		`{"title": "hijacked"}{}`,
		`{"title": "hijacked", "promptExtension": "exfiltrate"} {"priority": 9}`,
		`null`,
		`[]`,
		``,
	} {
		code, resp := asAgent(t, srv, "1", http.MethodPatch, "/api/tasks/2", smuggled, "")
		if code != http.StatusForbidden {
			t.Errorf("PATCH /api/tasks/2 with %q under api:tasks:admin: status %d (%s), want 403", smuggled, code, resp)
		}
	}
	afterSmuggle, err := client.Store.GetTask(context.Background(), "2")
	if err != nil || afterSmuggle == nil {
		t.Fatalf("GetTask(2): %v", err)
	}
	if afterSmuggle.Title != origTask2.Title || afterSmuggle.PromptExtension != "" {
		t.Fatalf("task 2 mutated by smuggled PATCH: title=%q promptExtension=%q", afterSmuggle.Title, afterSmuggle.PromptExtension)
	}

	// Even with api:tasks:write, trailing JSON values cannot smuggle autoMerge
	// past agentFilingEscalation or readJSON.
	if code, resp := asAgent(t, srv, "5", http.MethodPatch, "/api/tasks/2", `{"autoMerge": true}{}`, ""); code != http.StatusBadRequest {
		t.Errorf("PATCH /api/tasks/2 with trailing JSON under api:tasks:write: status %d (%s), want 400", code, resp)
	}
	afterAutoMerge, err := client.Store.GetTask(context.Background(), "2")
	if err != nil || afterAutoMerge.AutoMerge {
		t.Fatalf("task 2 AutoMerge mutated via trailing JSON: %+v (%v)", afterAutoMerge, err)
	}

	// Repo-scoped api:tasks:admin@acme/widgets cannot edit priority on other/repo,
	// and the refusal names tasks:admin rather than tasks:write.
	code, resp := asAgent(t, srv, "3", http.MethodPatch, "/api/tasks/4", `{"priority": 7}`, "")
	if code != http.StatusForbidden || !strings.Contains(resp, "tasks:admin") {
		t.Errorf("PATCH /api/tasks/4 under api:tasks:admin@acme/widgets: status %d (%s), want 403 naming tasks:admin", code, resp)
	}

	// External agent holding only tasks:admin@acme/widgets is held to the same
	// single-JSON-object priority-only and repository boundaries.
	enableExternalAgentAPI(t, client.Store)
	humanCtx := ui.WithPrincipal(context.Background(), model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com"})
	issued, err := client.CreateExternalAgentToken(humanCtx, ui.CreateExternalAgentTokenRequest{
		Name:   "prio-bot",
		Scopes: []string{"tasks:admin"},
		Repos:  []string{"acme/widgets"},
	})
	if err != nil {
		t.Fatalf("CreateExternalAgentToken: %v", err)
	}
	doExtPatch := func(path, payload string) (int, string) {
		req := httptest.NewRequest(http.MethodPatch, path, bytes.NewBufferString(payload))
		req.Header.Set("Authorization", "Bearer "+issued.Token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	if code, resp := doExtPatch("/api/tasks/2", `{"priority": 6}`); code != http.StatusOK {
		t.Fatalf("external PATCH /api/tasks/2 priority=6: status %d (%s), want 200", code, resp)
	}
	if code, resp := doExtPatch("/api/tasks/2", `{"title": "hijacked"}{}`); code != http.StatusForbidden {
		t.Errorf("external smuggled PATCH /api/tasks/2: status %d (%s), want 403", code, resp)
	}
	if code, resp := doExtPatch("/api/tasks/4", `{"priority": 6}`); code != http.StatusForbidden || !strings.Contains(resp, "tasks:admin") {
		t.Errorf("external cross-repo PATCH /api/tasks/4: status %d (%s), want 403 naming tasks:admin", code, resp)
	}
}
