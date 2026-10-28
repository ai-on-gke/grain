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
	"net/http"
	"testing"

	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// grain/task-322: the zone of the browser a task is filed or messaged
// from -- X-Grain-Time-Zone, which ui/src/api.js sends on every write --
// is the zone recorded on the task, and so the one its runs are told the
// time in, rather than UTC.
func TestTheBrowsersTimeZoneIsRecordedOnTheTask(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	ctx := context.Background()
	zoneOf := func(id string) string {
		t.Helper()
		task, err := client.Store.GetTask(ctx, id)
		if err != nil || task == nil {
			t.Fatalf("GetTask(%s) = %v, %v", id, task, err)
		}
		return task.TimeZone
	}
	la := map[string]string{ui.TimeZoneHeader: "America/Los_Angeles"}
	london := map[string]string{ui.TimeZoneHeader: "Europe/London"}

	t.Run("filed", func(t *testing.T) {
		rec := doWithHeaders(t, srv, http.MethodPost, "/api/tasks", `{"title":"t","description":"d"}`, la)
		wantStatus(t, rec, http.StatusCreated)
		if got := zoneOf(decode[ui.Task](t, rec).ID); got != "America/Los_Angeles" {
			t.Fatalf("TimeZone = %q, want America/Los_Angeles", got)
		}
	})

	t.Run("a zone the body names wins over the header", func(t *testing.T) {
		rec := doWithHeaders(t, srv, http.MethodPost, "/api/tasks", `{"title":"t","description":"d","timeZone":"Asia/Tokyo"}`, la)
		wantStatus(t, rec, http.StatusCreated)
		if got := zoneOf(decode[ui.Task](t, rec).ID); got != "Asia/Tokyo" {
			t.Fatalf("TimeZone = %q, want Asia/Tokyo", got)
		}
	})

	t.Run("a zone that is not one is ignored", func(t *testing.T) {
		rec := doWithHeaders(t, srv, http.MethodPost, "/api/tasks", `{"title":"t","description":"d"}`,
			map[string]string{ui.TimeZoneHeader: "Not/AZone"})
		wantStatus(t, rec, http.StatusCreated)
		if got := zoneOf(decode[ui.Task](t, rec).ID); got != "" {
			t.Fatalf("TimeZone = %q, want none", got)
		}
	})

	t.Run("messaged", func(t *testing.T) {
		rec := do(t, srv, http.MethodPost, "/api/tasks", `{"title":"t","description":"d"}`)
		wantStatus(t, rec, http.StatusCreated)
		id := decode[ui.Task](t, rec).ID
		wantStatus(t, doWithHeaders(t, srv, http.MethodPost, "/api/tasks/"+id+"/comments", `{"body":"hi"}`, la), http.StatusOK)
		if got := zoneOf(id); got != "America/Los_Angeles" {
			t.Fatalf("TimeZone = %q after a comment, want America/Los_Angeles", got)
		}
		// Somebody else commenting on a task that already has its
		// filer's zone leaves it there.
		wantStatus(t, doWithHeaders(t, srv, http.MethodPost, "/api/tasks/"+id+"/comments", `{"body":"hi"}`, london), http.StatusOK)
		if got := zoneOf(id); got != "America/Los_Angeles" {
			t.Fatalf("TimeZone = %q after a second commenter, want it kept", got)
		}
	})

	t.Run("a chat follows whoever spoke last", func(t *testing.T) {
		rec := doWithHeaders(t, srv, http.MethodPost, "/api/tasks", `{"title":"chat","description":"d","interactive":true}`, la)
		wantStatus(t, rec, http.StatusCreated)
		id := decode[ui.Task](t, rec).ID
		wantStatus(t, doWithHeaders(t, srv, http.MethodPost, "/api/tasks/"+id+"/comments", `{"body":"hi"}`, london), http.StatusOK)
		if got := zoneOf(id); got != "Europe/London" {
			t.Fatalf("TimeZone = %q, want Europe/London", got)
		}
	})
}

// A standing session opened from the browser records its opener's zone.
func TestAConsoleSessionRecordsItsOpenersTimeZone(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	storeSettingsRow(t, client)
	srv := ui.NewServer(ui.Config{
		Actor:        ui.DefaultActor("alice"),
		Capabilities: ui.OfferedCapabilities(),
		Secrets:      secrets.New(t.TempDir()),
	}, store)

	rec := doWithHeaders(t, srv, http.MethodPost, "/api/console-sessions", `{"message": "what is queued?"}`,
		map[string]string{ui.TimeZoneHeader: "America/Los_Angeles"})
	wantStatus(t, rec, http.StatusCreated)
	resp := decode[ui.ConsoleSessionSettingsResponse](t, rec)
	var taskID string
	for _, s := range resp.Sessions {
		if s.ID == resp.Created {
			taskID = s.TaskID
		}
	}
	task, err := store.GetTask(ctx, taskID)
	if err != nil || task == nil {
		t.Fatalf("GetTask(%q) = %v, %v", taskID, task, err)
	}
	if task.TimeZone != "America/Los_Angeles" {
		t.Fatalf("TimeZone = %q, want America/Los_Angeles", task.TimeZone)
	}
}
