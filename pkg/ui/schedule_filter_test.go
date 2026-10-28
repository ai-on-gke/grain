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

// GET /api/schedules narrowed by the Processes page's own filter
// grammar (pkg/schedulefilter, grain/task-429): the question the page's
// address bar asks, pasted onto the API, answers with the same rows the
// page shows for it (ui/src/processFilters.test.js asks the page the
// same questions of the same kinds of schedule).

import (
	"net/http"
	"net/url"
	"reflect"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestTheSchedulesAPIAnswersTheProcessesPagesFilter(t *testing.T) {
	t.Parallel()
	srv, _, declaredID := declaringServer(t)
	rec := do(t, srv, http.MethodPost, "/api/schedules",
		`{"title":"candidate cut","repo":"acme/gadgets","overlap":"skip","enabled":false,`+
			`"recurrence":{"kind":"weekly","weekday":"wednesday","timeOfDay":"12:00"}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status = %d: %s", rec.Code, rec.Body)
	}
	inline := decode[ui.Schedule](t, rec)
	// Created disabled or not, pause it: the create request's own
	// default is not what this test is about.
	if rec := do(t, srv, http.MethodPatch, "/api/schedules/"+inline.ID, `{"enabled":false}`); rec.Code != http.StatusOK {
		t.Fatalf("pause: status = %d: %s", rec.Code, rec.Body)
	}

	all := decode[[]ui.Schedule](t, do(t, srv, http.MethodGet, "/api/schedules", ""))
	byID := map[string]ui.Schedule{}
	for _, s := range all {
		byID[s.ID] = s
	}
	if got := byID[declaredID]; got.Source != "acme/widgets" || got.Type != "template" {
		t.Errorf("declared schedule source/type = %q/%q, want acme/widgets/template", got.Source, got.Type)
	}
	if got := byID[inline.ID]; got.Source != "deployment" || got.Type != "task" {
		t.Errorf("inline schedule source/type = %q/%q, want deployment/task", got.Source, got.Type)
	}

	ids := func(query string) []string {
		t.Helper()
		rec := do(t, srv, http.MethodGet, "/api/schedules?"+query, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET ?%s: status = %d: %s", query, rec.Code, rec.Body)
		}
		var out []string
		for _, s := range decode[[]ui.Schedule](t, rec) {
			out = append(out, s.ID)
		}
		return out
	}
	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"f=source:any:acme/widgets", []string{declaredID}},
		{"f=source:none:acme/widgets", []string{inline.ID}},
		{"f=source:any:deployment", []string{inline.ID}},
		{"f=type:any:template", []string{declaredID}},
		{"f=type:any:task|suite", []string{inline.ID}},
		{"f=name:has:CANDIDATE", []string{inline.ID}},
		{"f=enabled:any:no", []string{inline.ID}},
		{"f=enabled:any:yes&f=source:any:acme/widgets", []string{declaredID}},
		{"f=cadence:any:weekly", []string{inline.ID}},
		{"f=requireApproval:any:yes", nil},
		{"f=requireApproval:empty:", []string{declaredID, inline.ID}},
		{"f=last:empty:", []string{declaredID, inline.ID}},
		{"q=gadgets", []string{inline.ID}},
		{"sort=name:asc", []string{inline.ID, declaredID}},
		{"sort=name:desc", []string{declaredID, inline.ID}},
		{"sort=cadence:asc", []string{declaredID, inline.ID}},
		{"sort=cadence:desc", []string{inline.ID, declaredID}},
		// The kind chips, as the page writes them (?kind=, not an f):
		// neither schedule here runs a workflow.
		{"kind=schedule", []string{declaredID, inline.ID}},
		{"kind=schedule&f=enabled:any:no", []string{inline.ID}},
		{"kind=workflow", nil},
		{"kind=template,suite", nil},
		{"f=kind:none:workflow&sort=kind:asc,name:asc", []string{inline.ID, declaredID}},
		// The page's own words for a URL, escaped as paths.js writes them.
		{"f=" + url.QueryEscape("repo:any:acme/gadgets"), []string{inline.ID}},
	} {
		if got := ids(tc.query); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("GET /api/schedules?%s = %v, want %v", tc.query, got, tc.want)
		}
	}

	for _, bad := range []string{"f=nosuch:any:x", "f=enabled:has:x", "f=enabled", "sort=nosuch:asc", "kind=schedules"} {
		if rec := do(t, srv, http.MethodGet, "/api/schedules?"+bad, ""); rec.Code != http.StatusBadRequest {
			t.Errorf("GET ?%s: status = %d, want 400: %s", bad, rec.Code, rec.Body)
		}
	}
}
