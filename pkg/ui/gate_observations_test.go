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
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestGateObservationSelectionIsBoundedAndOffByDefault(t *testing.T) {
	for _, raw := range []string{"a,b,a", "../secret", "1,,2", strings.Repeat("1,", 33)} {
		if _, err := ui.ObservationSelection(raw); err == nil {
			t.Errorf("admitted %q", raw)
		}
	}
	ids, err := ui.ObservationSelection(" 1, 2 ")
	if err != nil || len(ids) != 2 {
		t.Fatalf("selection %v %v", ids, err)
	}
	ids, err = ui.ObservationSelection("")
	if err != nil || len(ids) != 0 {
		t.Fatal("not off by default")
	}
}

func TestGateObservationsOrdinaryAPIReportReceiptAndLegacyReply(t *testing.T) {
	srv, c := testServer(t)
	ctx := context.Background()
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "Find the cause", NoRepo: true})
	if err != nil {
		t.Fatal(err)
	}
	read := func() ui.TaskDetail {
		t.Helper()
		code, body := serve(t, srv, http.MethodGet, "/api/tasks/"+task.ID, "")
		if code != 200 {
			t.Fatalf("GET %d %s", code, body)
		}
		var d ui.TaskDetail
		if err := json.Unmarshal([]byte(body), &d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	if read().GateObservations != nil {
		t.Fatal("default enabled")
	}
	if err = c.Store.UpdateTask(ctx, task.ID, func(t *model.Task) error { t.Intent = model.IntentAnalyze; return nil }); err != nil {
		t.Fatal(err)
	}
	c.Config.GateObservationTaskIDs = []string{task.ID}
	now := baseTime.Add(time.Minute)
	c.Now = func() time.Time { return now }
	if err := c.Store.StartRun(ctx, model.Run{ID: "shadow-api-run", TaskID: task.ID, Attempt: 1, StartedAt: baseTime, Sandbox: "test"}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	_, err = c.Store.AddComment(ctx, model.Comment{TaskID: task.ID, Author: model.Attribution{Actor: model.GrainPrincipal, OnBehalfOf: &model.Principal{Kind: model.PrincipalAgent, ID: task.ID}}, Body: "Sensitive report body: do not copy into diagnostics", CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Store.FinishRun(ctx, "shadow-api-run", now, "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	if err = c.Store.ObserveField(ctx, task.ID, now, func(o *model.Observation) { o.CompletedAt = &now }); err != nil {
		t.Fatal(err)
	}
	d := read()
	if d.GateObservations == nil || d.GateObservations.Results[0].Result.Status != "met" || d.GateObservations.Results[1].Result.Status != "pending" {
		t.Fatalf("report observations %+v", d.GateObservations)
	}
	// Export actual persisted observations for canonical schema and browser checks.
	if path := os.Getenv("GRAIN_TEST_GATE_FIXTURE"); path != "" {
		b, _ := json.MarshalIndent(map[string]any{"gate_observations": d.GateObservations, "gate_observation_diagnostics": d.GateObservationDiagnostics}, "", "  ")
		if err = os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := json.Marshal(d.GateObservationDiagnostics)
	if strings.Contains(string(b), "Sensitive report") {
		t.Fatal("diagnostics leaked body")
	}
	person := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "alice@example.com", Source: model.SourceIAP})
	now = now.Add(time.Second)
	if err = c.Close(person, task.ID, ui.CloseOptions{}); err != nil {
		t.Fatal(err)
	}
	d = read()
	if d.GateObservations.Results[1].Result.Status != "met" {
		t.Fatalf("receipt %+v", d.GateObservations.Results[1])
	}
	if err = c.Reopen(person, task.ID); err != nil {
		t.Fatal(err)
	}
	// Legacy person reply remains owned by AddComment and requeues work.
	err = c.AddComment(person, task.ID, "Please investigate another case", nil)
	if err != nil {
		t.Fatal(err)
	}
	o, err := c.Store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if o.CompletedAt != nil {
		t.Fatal("legacy reply did not clear completion")
	}
	d = read()
	if d.GateObservations.Results[1].Result.Status == "met" {
		t.Fatal("reply became receipt")
	}
	c.Config.GateObservationTaskIDs = []string{}
	if read().GateObservations != nil {
		t.Fatal("disable left sidecar visible")
	}
}

func TestGateObservationsRespectTaskAuthorization(t *testing.T) {
	srv, c := testServer(t)
	seedAPITask(t, c, "caller-observe", "acme/widgets", "api:tasks:read@acme/widgets")
	seedAPITask(t, c, "visible-observe", "acme/widgets", "")
	seedAPITask(t, c, "hidden-observe", "acme/private", "")
	c.Config.GateObservationTaskIDs = []string{"visible-observe", "hidden-observe"}
	code, body := asAgent(t, srv, "caller-observe", http.MethodGet, "/api/tasks/visible-observe", "", "")
	if code != 200 || !strings.Contains(body, "gate_observations") {
		t.Fatalf("authorized %d %s", code, body)
	}
	code, body = asAgent(t, srv, "caller-observe", http.MethodGet, "/api/tasks/hidden-observe", "", "")
	if code == 200 || strings.Contains(body, "gate_observations") {
		t.Fatalf("hidden observation leaked %d %s", code, body)
	}
}

// Main's batch cache can have a stable domain token while a diagnostic lease
// expires. Never return that cached body for a selected observation task.
func TestGateObservationSummariesBypassStableDomainCache(t *testing.T) {
	srv, c := testServer(t)
	ctx := context.Background()
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "Observe a task", NoRepo: true})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/tasks/summaries?ids=" + task.ID + "&comments_limit=1&attempts_limit=1"
	code, body := serve(t, srv, http.MethodGet, path, "")
	if code != 200 || strings.Contains(body, "gate_observations") {
		t.Fatalf("unselected %d %s", code, body)
	}
	c.Config.GateObservationTaskIDs = []string{task.ID}
	now := baseTime
	c.Now = func() time.Time { return now }
	code, body = serve(t, srv, http.MethodGet, path, "")
	if code != 200 || !strings.Contains(body, "gate_observations") {
		t.Fatalf("selected cached %d %s", code, body)
	}
	var envelope struct {
		Tasks map[string]ui.TaskDetail `json:"tasks"`
	}
	if err = json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatal(err)
	}
	initial := envelope.Tasks[task.ID].GateObservations.ComparedAt
	// A coalesced read then leases the same stable domain state.
	code, _ = serve(t, srv, http.MethodGet, path, "")
	if code != 200 {
		t.Fatal(code)
	}
	now = now.Add(2 * time.Minute)
	code, body = serve(t, srv, http.MethodGet, path, "")
	if code != 200 {
		t.Fatalf("expired %d %s", code, body)
	}
	if err = json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.Tasks[task.ID].GateObservations.ComparedAt.After(initial) {
		t.Fatal("batch cache hid expired observation lease")
	}
}
