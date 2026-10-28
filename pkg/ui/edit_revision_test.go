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
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// listed is the row with id from a GET of path, which is where every
// editor reads the revision it later sends back.
func listed[T any](t *testing.T, srv *ui.Server, path, id string, idOf func(T) string) T {
	t.Helper()
	rec := do(t, srv, http.MethodGet, path, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: status = %d: %s", path, rec.Code, rec.Body)
	}
	for _, v := range decode[[]T](t, rec) {
		if idOf(v) == id {
			return v
		}
	}
	t.Fatalf("GET %s: no row %s", path, id)
	var zero T
	return zero
}

// wantStale asserts rec is the 409 a save from a stale copy gets, coded
// so an editor can tell it from any other refusal.
func wantStale(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body)
	}
	var body struct{ Error, Code string }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding %s: %v", rec.Body, err)
	}
	if body.Code != "stale" || body.Error == "" {
		t.Fatalf("body = %s, want code \"stale\" and a message", rec.Body)
	}
}

func wantOK(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
}

// Two editors open on the same schedule: the second to save, holding the
// copy from before the first saved, is refused rather than putting the
// first one's change back (the 2026-10-13 recurring/composed
// walkthrough, step 2).
func TestScheduleSaveFromAStaleCopyIsRefused(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	rec := do(t, srv, http.MethodPost, "/api/schedules",
		`{"title":"weekly","repo":"acme/widgets","overlap":"skip",`+
			`"recurrence":{"kind":"weekly","weekday":"wednesday","timeOfDay":"12:00"}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status = %d: %s", rec.Code, rec.Body)
	}
	created := decode[ui.Schedule](t, rec)
	idOf := func(s ui.Schedule) string { return s.ID }
	opened := listed(t, srv, "/api/schedules", created.ID, idOf)
	if opened.Revision == "" {
		t.Fatal("a listed schedule carries no revision")
	}

	// Tab A.
	wantOK(t, do(t, srv, http.MethodPatch, "/api/schedules/"+created.ID, fmt.Sprintf(
		`{"revision":%q,"title":"weekly","repo":"acme/widgets","overlap":"skip",`+
			`"recurrence":{"kind":"weekly","weekday":"wednesday","timeOfDay":"13:15"}}`, opened.Revision)))
	// Tab B, still holding 12:00.
	wantStale(t, do(t, srv, http.MethodPatch, "/api/schedules/"+created.ID, fmt.Sprintf(
		`{"revision":%q,"title":"weekly","repo":"acme/widgets","overlap":"allow",`+
			`"recurrence":{"kind":"weekly","weekday":"wednesday","timeOfDay":"12:00"}}`, opened.Revision)))

	got := listed(t, srv, "/api/schedules", created.ID, idOf)
	if got.Recurrence.TimeOfDay != "13:15" || got.Overlap != "skip" {
		t.Fatalf("after the refused save: timeOfDay %q, overlap %q; want tab A's 13:15 and skip",
			got.Recurrence.TimeOfDay, got.Overlap)
	}
	// Reloaded, tab B's change goes through.
	wantOK(t, do(t, srv, http.MethodPatch, "/api/schedules/"+created.ID, fmt.Sprintf(
		`{"revision":%q,"overlap":"allow"}`, got.Revision)))
	// And a caller that sends no revision is not asked for one.
	wantOK(t, do(t, srv, http.MethodPatch, "/api/schedules/"+created.ID, `{"overlap":"skip"}`))
}

// What grain changes on a schedule by itself -- a firing moving
// nextRunAt/lastRunAt, Pause/Resume, the display copy of an edited
// template -- is not somebody else's edit, and does not turn an open
// editor stale.
func TestScheduleRevisionIgnoresRuntimeChanges(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	ctx := context.Background()
	rec := do(t, srv, http.MethodPost, "/api/templates", `{"name":"sweep","title":"Sweep","description":"first"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create template: status = %d: %s", rec.Code, rec.Body)
	}
	tmpl := decode[ui.Template](t, rec)
	rec = do(t, srv, http.MethodPost, "/api/schedules", fmt.Sprintf(
		`{"templateId":%q,"repo":"acme/widgets","recurrence":{"kind":"daily","timeOfDay":"09:00"}}`, tmpl.ID))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create schedule: status = %d: %s", rec.Code, rec.Body)
	}
	created := decode[ui.Schedule](t, rec)
	idOf := func(s ui.Schedule) string { return s.ID }
	opened := listed(t, srv, "/api/schedules", created.ID, idOf)

	// Fired.
	now := time.Now().UTC()
	if err := client.Store.UpdateSchedule(ctx, created.ID, func(s *model.Schedule) error {
		s.LastRunAt, s.NextRunAt = &now, now.Add(24*time.Hour)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Paused, the way the pane's own button does it.
	wantOK(t, do(t, srv, http.MethodPatch, "/api/schedules/"+created.ID, `{"enabled":false}`))
	// Its template edited, which resyncs the schedule's copy of it.
	wantOK(t, do(t, srv, http.MethodPatch, "/api/templates/"+tmpl.ID, `{"title":"Sweep renamed","description":"second"}`))

	after := listed(t, srv, "/api/schedules", created.ID, idOf)
	if after.Title != "Sweep renamed" {
		t.Fatalf("schedule title = %q; the template resync this test relies on did not happen", after.Title)
	}
	if after.Revision != opened.Revision {
		t.Fatalf("revision moved from %s to %s on runtime-only changes", opened.Revision, after.Revision)
	}
	wantOK(t, do(t, srv, http.MethodPatch, "/api/schedules/"+created.ID, fmt.Sprintf(
		`{"revision":%q,"templateId":%q,"repo":"acme/widgets","overlap":"allow",`+
			`"recurrence":{"kind":"daily","timeOfDay":"09:00"}}`, opened.Revision, tmpl.ID)))
}

func TestSuiteSaveFromAStaleCopyIsRefused(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	rec := do(t, srv, http.MethodPost, "/api/templates", `{"name":"smoke","title":"Smoke"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create template: status = %d: %s", rec.Code, rec.Body)
	}
	tmpl := decode[ui.Template](t, rec)
	rec = do(t, srv, http.MethodPost, "/api/suites", fmt.Sprintf(
		`{"name":"regression","templateIds":[%q],"mode":"until_clean","maxPasses":2}`, tmpl.ID))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create suite: status = %d: %s", rec.Code, rec.Body)
	}
	created := decode[ui.Suite](t, rec)
	idOf := func(s ui.Suite) string { return s.ID }
	opened := listed(t, srv, "/api/suites", created.ID, idOf)
	if opened.Revision == "" {
		t.Fatal("a listed suite carries no revision")
	}
	if opened.Revision != created.Revision {
		t.Fatalf("revision on create %s, on list %s: an editor opened from either must agree",
			created.Revision, opened.Revision)
	}

	whole := func(maxPasses int, requireApproval bool) string {
		return fmt.Sprintf(`{"revision":%q,"name":"regression","templateIds":[%q],"mode":"until_clean",`+
			`"maxPasses":%d,"requireApproval":%t}`, opened.Revision, tmpl.ID, maxPasses, requireApproval)
	}
	wantOK(t, do(t, srv, http.MethodPatch, "/api/suites/"+created.ID, whole(4, false)))
	wantStale(t, do(t, srv, http.MethodPatch, "/api/suites/"+created.ID, whole(2, true)))

	got := listed(t, srv, "/api/suites", created.ID, idOf)
	if got.MaxPasses != 4 || got.RequireApproval {
		t.Fatalf("after the refused save: maxPasses %d, requireApproval %t; want tab A's 4 and false",
			got.MaxPasses, got.RequireApproval)
	}
}

func TestTemplateSaveFromAStaleCopyIsRefused(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	rec := do(t, srv, http.MethodPost, "/api/templates", `{"name":"triage","title":"Triage","description":"look"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create template: status = %d: %s", rec.Code, rec.Body)
	}
	created := decode[ui.Template](t, rec)
	idOf := func(t ui.Template) string { return t.ID }
	opened := listed(t, srv, "/api/templates", created.ID, idOf)
	if opened.Revision == "" {
		t.Fatal("a listed template carries no revision")
	}

	whole := func(title, description string) string {
		return fmt.Sprintf(`{"revision":%q,"name":"triage","title":%q,"description":%q}`,
			opened.Revision, title, description)
	}
	wantOK(t, do(t, srv, http.MethodPatch, "/api/templates/"+created.ID, whole("Triage carefully", "look")))
	wantStale(t, do(t, srv, http.MethodPatch, "/api/templates/"+created.ID, whole("Triage", "look harder")))

	got := listed(t, srv, "/api/templates", created.ID, idOf)
	if got.Title != "Triage carefully" || got.Description != "look" {
		t.Fatalf("after the refused save: title %q, description %q; want tab A's", got.Title, got.Description)
	}
}
