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
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ui"
)

// Schema 25 dropped schedule.team and suite.team, and their requests
// dropped `team` with them: a body still carrying it is refused with a
// 400 that names the field, on create and on patch, rather than
// accepted with the field silently ignored (task 4924). Templates and
// suite runs keep no team either, and their requests refuse it the same
// way (task 4927).
func TestRequestsRefuseTheRetiredTeamField(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodPost, "/api/schedules",
		`{"title":"nightly","repo":"acme/widgets","recurrence":{"kind":"everyNHours","everyNHours":24}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create schedule without team: status = %d, want 201: %s", rec.Code, rec.Body)
	}
	sched := decode[ui.Schedule](t, rec)
	rec = do(t, srv, http.MethodPost, "/api/templates", `{"name":"t","title":"t"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create template: status = %d, want 201: %s", rec.Code, rec.Body)
	}
	tmpl := decode[ui.Template](t, rec)
	rec = do(t, srv, http.MethodPost, "/api/suites", `{"name":"s","mode":"count","count":1,"templateIds":["`+tmpl.ID+`"]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create suite without team: status = %d, want 201: %s", rec.Code, rec.Body)
	}
	suite := decode[ui.Suite](t, rec)

	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/schedules",
			`{"title":"nightly","repo":"acme/widgets","team":"default","recurrence":{"kind":"everyNHours","everyNHours":24}}`},
		{http.MethodPatch, "/api/schedules/" + sched.ID, `{"team":"eng"}`},
		{http.MethodPost, "/api/suites", `{"name":"s","mode":"count","count":1,"team":"eng"}`},
		{http.MethodPatch, "/api/suites/" + suite.ID, `{"team":""}`},
		{http.MethodPost, "/api/templates", `{"name":"u","title":"u","team":"default"}`},
		{http.MethodPatch, "/api/templates/" + tmpl.ID, `{"team":"eng"}`},
		{http.MethodPost, "/api/suite-runs", `{"suiteId":"` + suite.ID + `","repo":"acme/widgets","base":"main","team":"default"}`},
	} {
		rec := do(t, srv, tc.method, tc.path, tc.body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `\"team\" field`) {
			t.Errorf("%s %s %s: status = %d, body %s; want 400 naming the team field", tc.method, tc.path, tc.body, rec.Code, rec.Body)
		}
	}
}

// /api/config carries the deployment as one object, the shape GET
// /api/deployment answers, and no longer the one-element teams list or
// the tenancy that only ever said "single" (task 4924).
func TestConfigCarriesTheDeploymentAndNoTeams(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)

	rec := do(t, srv, http.MethodGet, "/api/config", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/config: status = %d: %s", rec.Code, rec.Body)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for _, retired := range []string{"teams", "tenancy"} {
		if _, ok := got[retired]; ok {
			t.Errorf("/api/config carries %q: %s", retired, got[retired])
		}
	}
	var deployment ui.Team
	if err := json.Unmarshal(got["deployment"], &deployment); err != nil {
		t.Fatalf("decoding deployment %s: %v", got["deployment"], err)
	}
	if deployment.ID != "default" || deployment.ConfigRepo == "" || deployment.Name == "admin" {
		t.Errorf("deployment = %+v, want the deployment with its config repo and no team name", deployment)
	}
}
