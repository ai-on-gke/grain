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

package ui

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestReadJSONBoundsBody pins #2423: readJSON stops reading at its limit
// and answers 413 instead of buffering whatever a client sends, while a
// body under the limit decodes as before.
func TestReadJSONBoundsBody(t *testing.T) {
	var v struct {
		Body string `json:"body"`
	}

	big := `{"body":"` + strings.Repeat("x", maxJSONBody) + `"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(big))
	if readJSON(w, r, &v) {
		t.Fatal("readJSON accepted a body over maxJSONBody")
	}
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusRequestEntityTooLarge)
	}

	// The same body fits the attachment routes' larger bound.
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(big))
	if !readJSONLimit(w, r, &v, maxAttachmentJSONBody) {
		t.Fatalf("readJSONLimit rejected a body under maxAttachmentJSONBody: %d %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(`{"body":`)))
	if readJSON(w, r, &v) || w.Code != http.StatusBadRequest {
		t.Fatalf("malformed body: status = %d, want %d", w.Code, http.StatusBadRequest)
	}

	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(big))
	if readOptionalJSON(w, r, &v) || w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("readOptionalJSON oversized body: status = %d, want %d", w.Code, http.StatusRequestEntityTooLarge)
	}
}

// TestAttachmentBodyLimitFitsMaxAttachments checks the attachment routes'
// bound still admits the largest request decodeAttachments would accept.
func TestAttachmentBodyLimitFitsMaxAttachments(t *testing.T) {
	encoded := (MaxAttachmentSize + 2) / 3 * 4
	if need := MaxAttachmentsPerRequest * encoded; maxAttachmentJSONBody <= need {
		t.Fatalf("maxAttachmentJSONBody = %d, need more than %d", maxAttachmentJSONBody, need)
	}
}

// TestReadJSONNamesAWrongTypedField is the 2026-11-17 connections &
// infrastructure settings walkthrough's F1: Settings → Caches with a
// Proxy cache max size of 1.5 showed Go's "json: cannot unmarshal number
// 1.5 into Go struct field plain.packageCacheSizeGb of type int". The
// field is named in the request's own words instead.
func TestReadJSONNamesAWrongTypedField(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`{"packageCacheSizeGb":1.5}`, "packageCacheSizeGb must be a whole number, not 1.5"},
		{`{"maxWorkers":"4"}`, "maxWorkers must be a whole number, not a string"},
		{`{"pollInterval":10}`, "pollInterval must be a string, not a number"},
		{`{"settingsLocked":"yes"}`, "settingsLocked must be true or false, not a string"},
		{`{"hub":{"listed":"no"}}`, "hub.listed must be true or false, not a string"},
	} {
		var req UpdateSettingsRequest
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/api/settings", strings.NewReader(tc.body))
		if readJSON(w, r, &req) {
			t.Errorf("%s: accepted", tc.body)
			continue
		}
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", tc.body, w.Code)
		}
		if got := w.Body.String(); !strings.Contains(got, tc.want) || strings.Contains(got, "Go struct") {
			t.Errorf("%s: body = %s, want %q", tc.body, got, tc.want)
		}
	}
}

// TestReadJSONTaskRequestsDropAliasPrefix pins the 2026-11-19 §9 walkthrough's
// F1: CreateTaskRequest and UpdateTaskRequest decode through an anonymous
// struct embedding `Alias`, so encoding/json previously prefixed Field with
// "Alias." ("Alias.title must be a string, not a number").
func TestReadJSONTaskRequestsDropAliasPrefix(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`{"title":123,"repo":"acme/widgets"}`, "title must be a string, not a number"},
		{`{"title":"t","repo":"acme/widgets","approved":"yes"}`, "approved must be true or false, not a string"},
		{`{"title":"t","repo":"acme/widgets","reads":123}`, "reads must be a list, not a number"},
		{`{"title":"t","repo":"acme/widgets","goalMode":{"maxPasses":1.5}}`, "goalMode.maxPasses must be a whole number, not 1.5"},
	} {
		var req CreateTaskRequest
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(tc.body))
		if readAttachmentJSON(w, r, &req, func(r *CreateTaskRequest) []AttachmentUpload { return r.Attachments }) {
			t.Errorf("POST /api/tasks %s: accepted", tc.body)
			continue
		}
		if w.Code != http.StatusBadRequest {
			t.Errorf("POST /api/tasks %s: status = %d, want 400", tc.body, w.Code)
		}
		if got := w.Body.String(); !strings.Contains(got, tc.want) || strings.Contains(got, "Alias.") || strings.Contains(got, "Go struct") {
			t.Errorf("POST /api/tasks %s: body = %s, want %q", tc.body, got, tc.want)
		}
	}

	for _, tc := range []struct{ body, want string }{
		{`{"title":true}`, "title must be a string, not true or false"},
		{`{"autoMerge":"abc"}`, "autoMerge must be true or false, not a string"},
		{`{"sandboxCpus":2.5}`, "sandboxCpus must be a whole number, not 2.5"},
		{`{"reads":123}`, "reads must be a list, not a number"},
		{`{"issue":"abc"}`, "issue must be a whole number, not a string"},
	} {
		var req UpdateTaskRequest
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPatch, "/api/tasks/1", strings.NewReader(tc.body))
		if readJSON(w, r, &req) {
			t.Errorf("PATCH /api/tasks/1 %s: accepted", tc.body)
			continue
		}
		if w.Code != http.StatusBadRequest {
			t.Errorf("PATCH /api/tasks/1 %s: status = %d, want 400", tc.body, w.Code)
		}
		if got := w.Body.String(); !strings.Contains(got, tc.want) || strings.Contains(got, "Alias.") || strings.Contains(got, "Go struct") {
			t.Errorf("PATCH /api/tasks/1 %s: body = %s, want %q", tc.body, got, tc.want)
		}
	}
}
