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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestCreateAndUpdateTaskCarriesKeepRunning(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)

	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "standing worker", KeepRunning: true})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if !task.KeepRunning {
		t.Errorf("task.KeepRunning = false, want true")
	}
	stored, err := store.GetTask(ctx, task.ID)
	if err != nil || stored == nil {
		t.Fatalf("GetTask: (%+v, %v)", stored, err)
	}
	if !stored.KeepRunning {
		t.Errorf("stored.KeepRunning = false, want true")
	}

	off := false
	updated, err := c.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{KeepRunning: &off})
	if err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}
	if updated.KeepRunning {
		t.Errorf("updated.KeepRunning = true, want false")
	}
	stored, err = store.GetTask(ctx, task.ID)
	if err != nil || stored == nil {
		t.Fatalf("GetTask: (%+v, %v)", stored, err)
	}
	if stored.KeepRunning {
		t.Errorf("stored.KeepRunning after clearing = true, want false")
	}
}

func TestTasksHTTPHandlersAcceptSnakeAndCamelCaseKeepRunning(t *testing.T) {
	t.Parallel()
	srv, c := testServer(t)
	ctx := t.Context()

	postBody := bytes.NewBufferString(`{"title":"snake worker","description":"watch","noRepo":true,"keep_running":true}`)
	req := httptest.NewRequest(http.MethodPost, "/api/tasks", postBody)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/tasks status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var created ui.Task
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal created task: %v", err)
	}
	if !created.KeepRunning {
		t.Fatalf("created.KeepRunning = false, want true")
	}

	patchBody := bytes.NewBufferString(`{"keep_running":false}`)
	patchReq := httptest.NewRequest(http.MethodPatch, "/api/tasks/"+created.ID, patchBody)
	patchRec := httptest.NewRecorder()
	srv.ServeHTTP(patchRec, patchReq)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("PATCH /api/tasks/%s status = %d, body = %s", created.ID, patchRec.Code, patchRec.Body.String())
	}
	stored, err := c.Store.GetTask(ctx, created.ID)
	if err != nil || stored == nil {
		t.Fatalf("GetTask: (%+v, %v)", stored, err)
	}
	if stored.KeepRunning {
		t.Fatalf("stored.KeepRunning = true, want false after PATCH")
	}
}

func TestConsoleSessionUpdateCarriesKeepRunning(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	on := true
	sessions, err := c.UpdateConsoleSession(ctx, "director", ui.UpdateConsoleSessionRequest{KeepRunning: &on})
	if err != nil {
		t.Fatalf("UpdateConsoleSession: %v", err)
	}
	if len(sessions) == 0 || !sessions[0].KeepRunning {
		t.Fatalf("sessions = %+v, want director KeepRunning = true", sessions)
	}

	off := false
	sessions, err = c.UpdateConsoleSession(ctx, "director", ui.UpdateConsoleSessionRequest{KeepRunning: &off})
	if err != nil {
		t.Fatalf("UpdateConsoleSession(off): %v", err)
	}
	if len(sessions) == 0 || sessions[0].KeepRunning {
		t.Fatalf("sessions = %+v, want director KeepRunning = false", sessions)
	}
}
