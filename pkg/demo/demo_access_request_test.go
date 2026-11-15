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

package demo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

// TestDemoPendingAccessHookParksTheTask: POST /api/e2e/tasks/{id}/pending-access
// leaves the task parked on a real model.PendingAccessRequest, pointed at
// a comment, as a run's request_access call does -- and refuses anything a
// run's call could not have parked on.
func TestDemoPendingAccessHookParksTheTask(t *testing.T) {
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	human := model.Principal{Kind: model.PrincipalHuman, ID: "alice"}
	if err := store.PutTask(ctx, model.Task{
		ID: "7", Intent: model.IntentImplement, Title: "parked", Body: "please",
		Origin:   model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
		Approval: &model.Attribution{Actor: human},
	}); err != nil {
		t.Fatal(err)
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
	h := accessRequestDemoHandler(next, store)
	post := func(path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:4242"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	for _, tc := range []struct {
		name, path, body string
		want             int
	}{
		{"not json", "/api/e2e/tasks/7/pending-access", `{`, http.StatusBadRequest},
		{"no items", "/api/e2e/tasks/7/pending-access", `{"items":[]}`, http.StatusBadRequest},
		{"unknown kind", "/api/e2e/tasks/7/pending-access", `{"items":[{"kind":"root"}]}`, http.StatusBadRequest},
		{"grant without capability", "/api/e2e/tasks/7/pending-access", `{"items":[{"kind":"grant"}]}`, http.StatusBadRequest},
		{"unknown task", "/api/e2e/tasks/404/pending-access", `{"items":[{"kind":"network","access":"open"}]}`, http.StatusNotFound},
		{"nested id", "/api/e2e/tasks/7/x/pending-access", `{"items":[{"kind":"network","access":"open"}]}`, http.StatusNotFound},
		{"another route", "/api/tasks/7", `{}`, http.StatusTeapot},
	} {
		if rec := post(tc.path, tc.body); rec.Code != tc.want {
			t.Errorf("%s: POST %s = %d %q, want %d", tc.name, tc.path, rec.Code, rec.Body.String(), tc.want)
		}
	}
	if obs, err := store.GetObservation(ctx, "7"); err != nil {
		t.Fatal(err)
	} else if obs != nil && obs.PendingAccess != nil {
		t.Fatalf("a refused body parked the task: %+v", obs.PendingAccess)
	}

	nonLoopback := httptest.NewRequest(http.MethodPost, "/api/e2e/tasks/7/pending-access",
		strings.NewReader(`{"items":[{"kind":"network","access":"open"}]}`))
	nonLoopback.RemoteAddr = "192.0.2.7:4242"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, nonLoopback)
	if rec.Code != http.StatusForbidden {
		t.Errorf("a non-loopback caller got %d, want 403", rec.Code)
	}

	rec = post("/api/e2e/tasks/7/pending-access",
		`{"summary":" Grant gcp ","reason":"matrix","items":[{"kind":"grant","capability":"https://grain.dev/capabilities/gcp#gcp-key","with":{"permission_set":"sandbox-agent"}}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("parking: %d %q", rec.Code, rec.Body.String())
	}
	obs, err := store.GetObservation(ctx, "7")
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || obs.PendingAccess == nil || obs.PendingQuestionCommentID == nil {
		t.Fatalf("observation = %+v, want a pending access request pointed at a comment", obs)
	}
	req := obs.PendingAccess
	if req.Summary != "Grant gcp" || req.Reason != "matrix" || len(req.Items) != 1 ||
		req.Items[0].Kind != model.AccessGrant || req.Items[0].With["permission_set"] != "sandbox-agent" {
		t.Errorf("pending access = %+v", req)
	}
	comments, err := store.Comments(ctx, "7")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range comments {
		if c.ID == *obs.PendingQuestionCommentID && strings.Contains(c.Body, "Grant gcp") {
			found = true
		}
	}
	if !found {
		t.Errorf("no comment %d naming the request among %+v", *obs.PendingQuestionCommentID, comments)
	}
}

// TestOpenDemoDBRefusesWhatItCannotOpen: an unknown driver, and a DSN
// without -db-driver=postgres, are refused rather than ignored.
func TestOpenDemoDBRefusesWhatItCannotOpen(t *testing.T) {
	ctx := context.Background()
	if _, _, _, err := openDemoDB(ctx, t.TempDir(), "mysql", ""); err == nil {
		t.Error("openDemoDB accepted driver mysql")
	}
	if _, _, _, err := openDemoDB(ctx, t.TempDir(), "sqlite", "postgres://x"); err == nil {
		t.Error("openDemoDB accepted a DSN with the sqlite driver")
	}
	db, driver, closeDB, err := openDemoDB(ctx, t.TempDir(), "", "")
	if err != nil {
		t.Fatalf("default driver: %v", err)
	}
	defer closeDB()
	if driver != "sqlite" || db.PingContext(ctx) != nil {
		t.Errorf("default driver = %q, want a live sqlite store", driver)
	}
}
