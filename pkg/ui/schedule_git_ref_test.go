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

	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/secretsrepo"
	"github.com/ai-on-gke/grain/pkg/staterepo"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestScheduleAPIRefusesForbiddenGitRefRepos(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	forbidden := gitproxy.NewForbiddenSet(model.RepoRef{Owner: "acme", Name: "grain-secrets"})
	c.Config.ForbiddenRepos = forbidden
	c.Config.StateRepo = &fakeStateRepo{
		status: ui.StateRepoStatus{
			Branch:      "main",
			SecretsRepo: "https://github.com/acme/secrets-backup.git",
		},
	}
	dataDir := t.TempDir()
	if err := secretsrepo.SaveSettings(dataDir, secretsrepo.Settings{
		Remote: "git@github.com:acme/host-secrets.git",
		Branch: "main",
	}); err != nil {
		t.Fatalf("writing secretsrepo settings: %v", err)
	}
	c.Config.DataDir = dataDir
	srv := ui.NewServerWithClient(c)

	valid, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "Watch allowed upstream",
		Repo:  "acme/widgets",
		Recurrence: ui.Recurrence{
			Kind:   "git_ref",
			GitRef: &model.GitRefTrigger{Repo: "acme/widgets", Ref: "refs/tags/latest"},
		},
	})
	if err != nil {
		t.Fatalf("creating valid git_ref schedule: %v", err)
	}

	cases := []struct {
		name    string
		repo    string
		wantSub string
	}{
		{
			name:    "forbidden set repo (case-insensitive)",
			repo:    "ACME/Grain-Secrets",
			wantSub: "encrypted secrets backup repository",
		},
		{
			name:    "state repo status secretsRepo",
			repo:    "acme/secrets-backup",
			wantSub: "encrypted secrets backup repository",
		},
		{
			name:    "secrets-repo.json remote",
			repo:    "acme/host-secrets",
			wantSub: "encrypted secrets backup repository",
		},
		{
			name:    "retired per-team config repo",
			repo:    "grain/config-alpha",
			wantSub: "grain/config-alpha not found: only grain/config exists",
		},
	}

	for _, tc := range cases {
		t.Run("create/"+tc.name, func(t *testing.T) {
			rec := do(t, srv, http.MethodPost, "/api/schedules",
				`{"title":"Bad watch","repo":"acme/widgets","recurrence":{"kind":"git_ref","gitRef":{"repo":"`+tc.repo+`","ref":"refs/heads/main"}}}`)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("POST /api/schedules watching %s: status = %d, want 400: %s", tc.repo, rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.wantSub) {
				t.Errorf("POST /api/schedules error = %q, want substring %q", rec.Body.String(), tc.wantSub)
			}
		})

		t.Run("update/"+tc.name, func(t *testing.T) {
			rec := do(t, srv, http.MethodPatch, "/api/schedules/"+valid.ID,
				`{"recurrence":{"kind":"git_ref","gitRef":{"repo":"`+tc.repo+`","ref":"refs/heads/main"}}}`)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("PATCH /api/schedules/%s watching %s: status = %d, want 400: %s", valid.ID, tc.repo, rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.wantSub) {
				t.Errorf("PATCH /api/schedules/%s error = %q, want substring %q", valid.ID, rec.Body.String(), tc.wantSub)
			}
		})
	}

	// Controller-local grain/config is permitted on deployment schedules when no
	// external config repository is configured...
	rec := do(t, srv, http.MethodPost, "/api/schedules",
		`{"title":"Watch local config","repo":"acme/widgets","recurrence":{"kind":"git_ref","gitRef":{"repo":"grain/config","ref":"refs/heads/main"}}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/schedules watching local grain/config: status = %d, want 201: %s", rec.Code, rec.Body.String())
	}

	// ...and refused with HTTP 400 when an external config repository disables local grain/config.
	c.Config.StateRepo = nil
	if err := staterepo.SaveSettings(dataDir, staterepo.Settings{
		Remote: "https://github.com/acme/grain-config.git",
		Branch: "main",
	}); err != nil {
		t.Fatalf("saving staterepo settings: %v", err)
	}
	rec = do(t, srv, http.MethodPost, "/api/schedules",
		`{"title":"Watch disabled local config","repo":"acme/widgets","recurrence":{"kind":"git_ref","gitRef":{"repo":"grain/config","ref":"refs/heads/main"}}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /api/schedules watching disabled grain/config: status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "local grain/config is disabled") {
		t.Errorf("POST /api/schedules error = %q, want explanation that local grain/config is disabled", rec.Body.String())
	}
}

func TestScheduleAPIPausingStrandedForbiddenGitRefScheduleSucceeds(t *testing.T) {
	t.Parallel()
	c, _, _ := testClient(t)
	forbidden := gitproxy.NewForbiddenSet()
	c.Config.ForbiddenRepos = forbidden
	srv := ui.NewServerWithClient(c)

	rec := do(t, srv, http.MethodPost, "/api/schedules",
		`{"title":"Legacy watch","repo":"acme/widgets","recurrence":{"kind":"git_ref","gitRef":{"repo":"acme/later-secrets","ref":"refs/heads/main"}}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("initial POST /api/schedules: %d %s", rec.Code, rec.Body.String())
	}
	created := decode[ui.Schedule](t, rec)

	// Later, the deployment adopts acme/later-secrets as its secrets backup repo.
	forbidden.Set([]model.RepoRef{{Owner: "acme", Name: "later-secrets"}})

	// Saving an edit while still enabled is refused with 400.
	rec = do(t, srv, http.MethodPatch, "/api/schedules/"+created.ID,
		`{"title":"Renamed","recurrence":{"kind":"git_ref","gitRef":{"repo":"acme/later-secrets","ref":"refs/heads/main"}}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PATCH while enabled: status = %d, want 400: %s", rec.Code, rec.Body.String())
	}

	// Pausing the schedule (even from a form that resends its recurrence) succeeds.
	rec = do(t, srv, http.MethodPatch, "/api/schedules/"+created.ID,
		`{"enabled":false,"recurrence":{"kind":"git_ref","gitRef":{"repo":"acme/later-secrets","ref":"refs/heads/main"}}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH pausing stranded schedule: status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	// Re-enabling it without changing the forbidden repo is refused with 400.
	rec = do(t, srv, http.MethodPatch, "/api/schedules/"+created.ID, `{"enabled":true}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PATCH re-enabling forbidden schedule: status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

func TestSchedulesEndpointSurfacesOffTargetGitRefSuppression(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	if err := store.SetTargetRepos(ctx, []string{"acme/widgets"}); err != nil {
		t.Fatalf("saving target repos: %v", err)
	}
	srv := ui.NewServerWithClient(c)

	rec := do(t, srv, http.MethodPost, "/api/schedules",
		`{"title":"Watch external release","repo":"acme/widgets","recurrence":{"kind":"git_ref","gitRef":{"repo":"offlist/upstream","ref":"refs/tags/latest"}}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/schedules: status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	created := decode[ui.Schedule](t, rec)
	if created.Suppressed == nil || created.Suppressed.Reason != "watchedRepoOffTarget" {
		t.Fatalf("POST response suppressed = %+v, want reason watchedRepoOffTarget", created.Suppressed)
	}

	list := decode[[]map[string]json.RawMessage](t, do(t, srv, http.MethodGet, "/api/schedules", ""))
	if len(list) != 1 {
		t.Fatalf("schedules = %d, want 1", len(list))
	}
	var got struct {
		Reason string `json:"reason"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(list[0]["suppressed"], &got); err != nil {
		t.Fatalf("decoding suppressed from GET /api/schedules: %v", err)
	}
	wantDetail := "watches offlist/upstream, which is not on this deployment's target repo list"
	if got.Reason != "watchedRepoOffTarget" || got.Detail != wantDetail {
		t.Errorf("GET /api/schedules suppressed = %+v, want watchedRepoOffTarget / %q", got, wantDetail)
	}

	// Widening TargetRepos clears the suppression on the next GET /api/schedules.
	if err := store.SetTargetRepos(ctx, []string{"acme/widgets", "offlist/upstream"}); err != nil {
		t.Fatalf("widening target repos: %v", err)
	}
	after := decode[[]ui.Schedule](t, do(t, srv, http.MethodGet, "/api/schedules", ""))
	if len(after) != 1 || after[0].Suppressed != nil {
		t.Errorf("GET /api/schedules after widening TargetRepos: suppressed = %+v, want nil", after[0].Suppressed)
	}
}
