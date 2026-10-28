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
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestClientReadStatesAndPagination(t *testing.T) {
	client, store, ctx := testClient(t)
	now := time.Now().UTC()
	actor := model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}}

	// Create 5 approved tasks
	for i := 1; i <= 5; i++ {
		id := fmt.Sprintf("task-%02d", i)
		if err := store.PutTask(ctx, model.Task{
			ID:         id,
			Title:      "Task " + id,
			CreatedAt:  &now,
			Origin:     model.Origin{Attribution: actor},
			Approval:   &actor,
			ApprovedAt: &now,
			Binding:    model.BindingDirective,
		}); err != nil {
			t.Fatalf("PutTask(%s): %v", id, err)
		}
	}

	// 1. ReadStates gets all 5 tasks
	allStates, err := client.ReadStates(ctx)
	if err != nil {
		t.Fatalf("ReadStates: %v", err)
	}
	if len(allStates) != 5 {
		t.Fatalf("ReadStates returned %d tasks, want 5", len(allStates))
	}
	for i := 1; i <= 5; i++ {
		id := fmt.Sprintf("task-%02d", i)
		if st, ok := allStates[id]; !ok || st != model.StateQueued {
			t.Errorf("task %s state = %q (exists=%v), want %q", id, st, ok, model.StateQueued)
		}
	}

	// 2. StatesPage with limit 2
	p1, c1, err := client.StatesPage(ctx, 2, "")
	if err != nil {
		t.Fatalf("StatesPage p1: %v", err)
	}
	if len(p1) != 2 || c1 != "task-02" {
		t.Fatalf("p1 = %v, c1 = %q; want 2 items and cursor task-02", p1, c1)
	}

	p2, c2, err := client.StatesPage(ctx, 2, c1)
	if err != nil {
		t.Fatalf("StatesPage p2: %v", err)
	}
	if len(p2) != 2 || c2 != "task-04" {
		t.Fatalf("p2 = %v, c2 = %q; want 2 items and cursor task-04", p2, c2)
	}

	p3, c3, err := client.StatesPage(ctx, 2, c2)
	if err != nil {
		t.Fatalf("StatesPage p3: %v", err)
	}
	if len(p3) != 1 || c3 != "" {
		t.Fatalf("p3 = %v, c3 = %q; want 1 item and empty cursor", p3, c3)
	}

	// 3. EachState callback
	visited := make(map[string]model.State)
	err = client.EachState(ctx, 2, func(id string, state model.State) error {
		visited[id] = state
		return nil
	})
	if err != nil {
		t.Fatalf("EachState: %v", err)
	}
	if len(visited) != 5 {
		t.Fatalf("EachState visited %d tasks, want 5", len(visited))
	}
}

func TestServerTaskStatesEndpointAndHTTPClient(t *testing.T) {
	srv, client := testServer(t)
	ctx := context.Background()
	now := time.Now().UTC()
	actor := model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}}

	for i := 1; i <= 5; i++ {
		id := fmt.Sprintf("t-%02d", i)
		if err := client.Store.PutTask(ctx, model.Task{
			ID:         id,
			Title:      "Task " + id,
			CreatedAt:  &now,
			Origin:     model.Origin{Attribution: actor},
			Approval:   &actor,
			ApprovedAt: &now,
			Binding:    model.BindingDirective,
		}); err != nil {
			t.Fatalf("PutTask(%s): %v", id, err)
		}
	}

	// Test GET /api/tasks/states with limit=2
	rec := do(t, srv, "GET", "/api/tasks/states?limit=2", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/tasks/states: status %d", rec.Code)
	}
	resp := decode[ui.TaskStatesResponse](t, rec)
	if len(resp.States) != 2 || resp.Cursor != "t-02" || !resp.HasMore {
		t.Fatalf("resp = %+v; want 2 items, cursor t-02, hasMore true", resp)
	}

	// Test GET /api/states alias with cursor
	rec2 := do(t, srv, "GET", "/api/states?limit=2&cursor="+resp.Cursor, "")
	if rec2.Code != http.StatusOK {
		t.Fatalf("GET /api/states: status %d", rec2.Code)
	}
	resp2 := decode[ui.TaskStatesResponse](t, rec2)
	if len(resp2.States) != 2 || resp2.Cursor != "t-04" || !resp2.HasMore {
		t.Fatalf("resp2 = %+v; want 2 items, cursor t-04, hasMore true", resp2)
	}

	// Test HTTPClient against an httptest.Server
	ts := httptest.NewServer(srv)
	defer ts.Close()

	httpCli := serverAccessClient(srv, ts.URL)
	allStates, err := httpCli.ReadStates(ctx)
	if err != nil {
		t.Fatalf("HTTPClient.ReadStates: %v", err)
	}
	if len(allStates) != 5 {
		t.Fatalf("HTTPClient.ReadStates returned %d tasks, want 5", len(allStates))
	}

	page, err := httpCli.TaskStatesPage(ctx, 2, "")
	if err != nil {
		t.Fatalf("HTTPClient.TaskStatesPage: %v", err)
	}
	if len(page.States) != 2 || page.Cursor != "t-02" || !page.HasMore {
		t.Fatalf("HTTPClient.TaskStatesPage page = %+v", page)
	}
}
