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
	"net/http"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ui"
)

// GET /api/tasks and GET /api/tasks/{id} say which template a task was
// filed from (templateId, grain/task-5273): a person filing from the New
// Task form, and an agent filing naming a templateId -- which records the
// template for reporting without making it the task's TemplateID -- and
// nothing for a task written from scratch.
func TestTheTaskAPISaysWhichTemplateATaskWasFiledFrom(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	ctx := context.Background()
	tmpl, err := client.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "Audit", Title: "Audit the thing", Description: "Audit it, then report.",
	})
	if err != nil {
		t.Fatal(err)
	}

	code, body := serve(t, srv, http.MethodPost, "/api/tasks",
		`{"templateId":"`+tmpl.ID+`","noRepo":true,"approved":true}`)
	if code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("POST /api/tasks from the template: %d %s", code, body)
	}
	var fromForm ui.Task
	if err := json.Unmarshal([]byte(body), &fromForm); err != nil {
		t.Fatal(err)
	}
	byAgent, err := client.CreateTask(agentCtx("t9"), ui.CreateTaskRequest{TemplateID: tmpl.ID, NoRepo: true})
	if err != nil {
		t.Fatalf("an agent filing from the template: %v", err)
	}
	scratch, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "by hand", Description: "no template", NoRepo: true})
	if err != nil {
		t.Fatal(err)
	}

	stored, err := client.Store.GetTask(ctx, byAgent.ID)
	if err != nil || stored == nil {
		t.Fatalf("reading the agent's filing: %v, %v", stored, err)
	}
	if stored.TemplateID != "" || stored.SourceTemplateID != tmpl.ID {
		t.Errorf("agent's filing: template %q, source template %q; want none and %q -- an agent hands on no template's push set",
			stored.TemplateID, stored.SourceTemplateID, tmpl.ID)
	}
	stored, err = client.Store.GetTask(ctx, fromForm.ID)
	if err != nil || stored == nil {
		t.Fatalf("reading the form's filing: %v, %v", stored, err)
	}
	if stored.TemplateID != tmpl.ID || stored.SourceTemplateID != tmpl.ID {
		t.Errorf("form's filing: template %q, source template %q, want both %q", stored.TemplateID, stored.SourceTemplateID, tmpl.ID)
	}

	want := map[string]string{fromForm.ID: tmpl.ID, byAgent.ID: tmpl.ID, scratch.ID: ""}
	for id, wantTemplate := range want {
		code, body := serve(t, srv, http.MethodGet, "/api/tasks/"+id, "")
		if code != http.StatusOK {
			t.Fatalf("GET /api/tasks/%s: %d %s", id, code, body)
		}
		var detail map[string]any
		if err := json.Unmarshal([]byte(body), &detail); err != nil {
			t.Fatal(err)
		}
		got, present := detail["templateId"]
		if wantTemplate == "" && present {
			t.Errorf("GET /api/tasks/%s: templateId = %v, want it omitted for a task written from scratch", id, got)
		}
		if wantTemplate != "" && got != wantTemplate {
			t.Errorf("GET /api/tasks/%s: templateId = %v, want %q", id, got, wantTemplate)
		}
	}

	code, body = serve(t, srv, http.MethodGet, "/api/tasks", "")
	if code != http.StatusOK {
		t.Fatalf("GET /api/tasks: %d %s", code, body)
	}
	var listed []ui.Task
	if err := json.Unmarshal([]byte(body), &listed); err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, tk := range listed {
		if wantTemplate, ok := want[tk.ID]; ok {
			seen++
			if tk.TemplateID != wantTemplate {
				t.Errorf("GET /api/tasks: task %s templateId = %q, want %q", tk.ID, tk.TemplateID, wantTemplate)
			}
		}
	}
	if seen != len(want) {
		t.Errorf("GET /api/tasks listed %d of the %d tasks filed", seen, len(want))
	}
}
