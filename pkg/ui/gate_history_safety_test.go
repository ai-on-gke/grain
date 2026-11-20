// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
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
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestGateHistoryUnsafeRestoreCannotLeakThroughOrdinaryAPI(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := model.New(db)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(store.Init(ctx))
	now := baseTime.Add(time.Minute)
	c := ui.NewClient(ui.Config{Actor: ui.DefaultActor("alice"), GateObservationTaskIDs: []string{}}, store)
	c.Now = func() time.Time { return now }
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "Inspect restored diagnostics", NoRepo: true})
	must(err)
	c.Config.GateObservationTaskIDs = []string{task.ID}
	srv := ui.NewServerWithClient(c)
	read := func(path string) ui.TaskDetail {
		t.Helper()
		code, body := serve(t, srv, http.MethodGet, path, "")
		if code != http.StatusOK {
			t.Fatalf("ordinary read: %d %s", code, body)
		}
		if strings.Contains(body, "PRIVATE-RESTORED-EVIDENCE") {
			t.Fatal("unsafe historical payload exposed")
		}
		var detail ui.TaskDetail
		if strings.Contains(path, "summaries?") {
			var envelope struct {
				Tasks map[string]ui.TaskDetail `json:"tasks"`
			}
			must(json.Unmarshal([]byte(body), &envelope))
			detail = envelope.Tasks[task.ID]
		} else {
			must(json.Unmarshal([]byte(body), &detail))
		}
		if detail.GateObservations == nil || detail.GateObservations.Authority != "legacy" || detail.GateObservations.LifecycleEffects || detail.GateObservations.ActionEffects || detail.GateObservations.DispatchEffects || detail.GateObservations.AutomaticCutover {
			t.Fatal("ordinary read acquired shadow authority")
		}
		return detail
	}
	initial := read("/api/tasks/" + task.ID)
	damaged := *initial.GateObservations
	damaged.Authority = "builtins"
	damaged.AutomaticCutover = true
	private := "PRIVATE-RESTORED-EVIDENCE"
	damaged.Results[0].Result.HumanText = &private
	raw, err := json.Marshal(damaged)
	must(err)
	_, err = db.ExecContext(ctx, "UPDATE gate_observation_shadow_history SET view_json = ? WHERE task_id = ?", string(raw), task.ID)
	must(err)
	before, err := store.GetObservation(ctx, task.ID)
	must(err)
	wake, commits := store.Changed(), store.CommitCount()
	for _, path := range []string{"/api/tasks/" + task.ID, "/api/tasks/" + task.ID + "?comments_limit=1&attempts_limit=1", "/api/tasks/summaries?ids=" + task.ID} {
		detail := read(path)
		if detail.GateObservationDiagnostics == nil || detail.GateObservationDiagnostics.Counts["unknown_invalid_history"] != 1 || len(detail.GateObservationDiagnostics.History) != 0 {
			t.Fatal("unsafe historical gap missing")
		}
		if detail.GateObservations.SourceRevision != initial.GateObservations.SourceRevision {
			t.Fatal("bad history changed current accepted evidence")
		}
	}
	afterRead, err := store.GetObservation(ctx, task.ID)
	must(err)
	if !reflect.DeepEqual(before, afterRead) || commits != store.CommitCount() {
		t.Fatal("unsafe history read changed task state")
	}
	select {
	case <-wake:
		t.Fatal("unsafe history read woke dispatch")
	default:
	}
	// An ordinary source event still recovers a new safe sample; the unsafe
	// stored row remains inert and visibly withheld until normal retention.
	now = now.Add(time.Second)
	_, err = store.AddComment(ctx, model.Comment{TaskID: task.ID, Author: model.Attribution{Actor: model.GrainPrincipal}, Body: "legacy source event", CreatedAt: now})
	must(err)
	// This write legitimately notifies listeners. Snapshot after it.
	before, err = store.GetObservation(ctx, task.ID)
	must(err)
	wake, commits = store.Changed(), store.CommitCount()
	recovered := read("/api/tasks/" + task.ID)
	if len(recovered.GateObservationDiagnostics.History) == 0 {
		t.Fatal("new safe history failed to recover")
	}
	for _, sample := range recovered.GateObservationDiagnostics.History {
		if sample.View.Mode != "observation_only" || sample.View.Authority != "legacy" {
			t.Fatal("unsafe history survived projection")
		}
		for _, b := range sample.View.Bindings {
			if b.Required || b.Action != nil {
				t.Fatal("operational historical binding")
			}
		}
		for _, r := range sample.View.Results {
			if len(r.Result.Actions) > 0 || len(r.Result.Work) > 0 {
				t.Fatal("operational historical output")
			}
		}
	}
	after, err := store.GetObservation(ctx, task.ID)
	must(err)
	if !reflect.DeepEqual(before, after) || commits != store.CommitCount() {
		t.Fatal("history recovery changed task state")
	}
	select {
	case <-wake:
		t.Fatal("history recovery woke dispatch")
	default:
	}
}
