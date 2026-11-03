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
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
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

func TestSchedulesRefuseKeepRunningEditsOnTemplateSuiteAndSystemSchedules(t *testing.T) {
	t.Parallel()
	srv, c := testServer(t)
	ctx := t.Context()

	rec := do(t, srv, http.MethodPost, "/api/templates",
		`{"name":"Nightly audit","title":"Audit dependencies"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create template status = %d: %s", rec.Code, rec.Body)
	}
	tmpl := decode[ui.Template](t, rec)

	for _, field := range []string{`"keepRunning":true`, `"keep_running":true`} {
		rec = do(t, srv, http.MethodPost, "/api/schedules",
			`{"templateId":"`+tmpl.ID+`","repo":"acme/widgets",`+field+`,`+
				`"recurrence":{"kind":"everyNHours","everyNHours":24}}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("POST /api/schedules with %s alongside template status = %d, want 400: %s",
				field, rec.Code, rec.Body)
		}
		if !strings.Contains(rec.Body.String(), "keepRunning") {
			t.Errorf("400 body %s does not mention keepRunning", rec.Body)
		}
	}

	rec = do(t, srv, http.MethodPost, "/api/schedules",
		`{"templateId":"`+tmpl.ID+`","repo":"acme/widgets",`+
			`"recurrence":{"kind":"everyNHours","everyNHours":24}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create template schedule status = %d: %s", rec.Code, rec.Body)
	}
	sched := decode[ui.Schedule](t, rec)

	for _, body := range []string{`{"keepRunning":true}`, `{"keep_running":true}`} {
		rec = do(t, srv, http.MethodPatch, "/api/schedules/"+sched.ID, body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("PATCH /api/schedules/%s (%s) status = %d, want 400: %s",
				sched.ID, body, rec.Code, rec.Body)
		}
		if !strings.Contains(rec.Body.String(), "keepRunning") {
			t.Errorf("400 body %s does not mention keepRunning", rec.Body)
		}
	}

	// Resending the unchanged keepRunning value is allowed.
	rec = do(t, srv, http.MethodPatch, "/api/schedules/"+sched.ID, `{"keepRunning":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH /api/schedules/%s unchanged keepRunning status = %d, want 200: %s",
			sched.ID, rec.Code, rec.Body)
	}

	// Seed a system schedule and verify keepRunning edits are refused there too.
	sysTmplID := model.MemoryAgentTemplateID
	now := time.Date(2026, time.October, 31, 12, 0, 0, 0, time.UTC)
	sysSched := model.Schedule{
		ID:          model.MemoryAgentTemplateID,
		Title:       "Memory agent",
		TemplateID:  &sysTmplID,
		Recurrence:  model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 3 * 60},
		Overlap:     model.OverlapSkip,
		Enabled:     true,
		KeepRunning: false,
		NextRunAt:   now.Add(24 * time.Hour),
		CreatedAt:   now,
	}
	if err := c.Store.PutSchedule(ctx, sysSched); err != nil {
		t.Fatalf("PutSchedule(system): %v", err)
	}
	for _, body := range []string{`{"keepRunning":true}`, `{"keep_running":true}`} {
		rec = do(t, srv, http.MethodPatch, "/api/schedules/"+sysSched.ID, body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("PATCH system schedule (%s) status = %d, want 400: %s",
				body, rec.Code, rec.Body)
		}
		stored, err := c.Store.GetSchedule(ctx, sysSched.ID)
		if err != nil || stored == nil {
			t.Fatalf("GetSchedule(system): (%+v, %v)", stored, err)
		}
		if stored.KeepRunning {
			t.Fatalf("system schedule KeepRunning mutated to true via %s", body)
		}
	}
}
