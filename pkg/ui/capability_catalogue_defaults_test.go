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
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
	"github.com/ai-on-gke/grain/pkg/ui"
)

type fieldErrorResponse struct {
	Error  string            `json:"error"`
	Fields map[string]string `json:"fields"`
}

func assertBadRequestOnField(t *testing.T, rec interface {
	Code() int
	BodyString() string
}, wantField string, wantReasonSubstrings ...string) {
	t.Helper()
	if rec.Code() != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rec.Code(), rec.BodyString())
	}
	var got fieldErrorResponse
	if err := json.Unmarshal([]byte(rec.BodyString()), &got); err != nil {
		t.Fatalf("decoding error body %q: %v", rec.BodyString(), err)
	}
	fieldMsg, ok := got.Fields[wantField]
	if !ok {
		t.Fatalf("fields = %+v (error = %q), want offending field %q", got.Fields, got.Error, wantField)
	}
	if !strings.HasPrefix(got.Error, wantField+": ") {
		t.Fatalf("error = %q, want prefix %q", got.Error, wantField+": ")
	}
	for _, sub := range wantReasonSubstrings {
		if !strings.Contains(got.Error, sub) || !strings.Contains(fieldMsg, sub) {
			t.Fatalf("error = %q, fields[%q] = %q; want substring %q", got.Error, wantField, fieldMsg, sub)
		}
	}
}

type recordedResp struct {
	code int
	body string
}

func (r recordedResp) Code() int          { return r.code }
func (r recordedResp) BodyString() string { return r.body }

func doRecorded(t *testing.T, srv *ui.Server, method, path, body string) recordedResp {
	t.Helper()
	rec := do(t, srv, method, path, body)
	return recordedResp{code: rec.Code, body: rec.Body.String()}
}

func exportStateAndCheck(t *testing.T, store *model.Store) error {
	t.Helper()
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "state")
	if err := staterepo.Export(ctx, store.DB(), dir); err != nil {
		t.Fatalf("staterepo.Export: %v", err)
	}
	if err := staterepo.WriteSchemaVersion(dir, model.SchemaVersion); err != nil {
		t.Fatalf("staterepo.WriteSchemaVersion: %v", err)
	}
	_, checkStore, _ := testClient(t)
	_, err := staterepo.Check(ctx, checkStore.DB(), dir, model.SchemaVersion)
	return err
}

func assertStateExportChecksClean(t *testing.T, store *model.Store) {
	t.Helper()
	if err := exportStateAndCheck(t, store); err != nil {
		t.Fatalf("staterepo.Check failed on exported state: %v", err)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func setupAllowlistedRepoServer(t *testing.T) (*ui.Server, *ui.Client) {
	t.Helper()
	srv, c := testServer(t)
	if rec := do(t, srv, http.MethodPut, "/api/deployment", `{"targetRepos":["acme/widgets"]}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT /api/deployment: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, srv, http.MethodPut, "/api/settings", `{"pollInterval":"1s","githubHost":"github.com"}`); rec.Code != http.StatusOK {
		t.Fatalf("initial PUT /api/settings: %d %s", rec.Code, rec.Body.String())
	}
	return srv, c
}

// TestRefusesContradictoryCICapabilitiesAndPolicy reproduces finding F2 across
// every repo and catalogue CI default write path in both save orders, and
// checks that every accepted state passes `grain state check`.
func TestRefusesContradictoryCICapabilitiesAndPolicy(t *testing.T) {
	t.Parallel()
	f := false
	disableImageSigningPolicy := mustJSON(t, ui.CapabilityPolicyBody{
		Policy: model.CapabilityPolicy{
			Catalogue: &model.CapabilityCatalogue{
				InProcess: map[string]model.CatalogueInProcess{
					"image-signing": {Enabled: &f},
				},
			},
		},
	})

	repoWritePaths := []struct {
		name      string
		method    string
		path      string
		grantBody string
	}{
		{
			name:      "PUT /api/repos/{owner}/{name}/ci-capabilities",
			method:    http.MethodPut,
			path:      "/api/repos/acme/widgets/ci-capabilities",
			grantBody: `{"ciCapabilities":["image-signing"]}`,
		},
		{
			name:      "PUT /api/repos/{owner}/{name}/settings",
			method:    http.MethodPut,
			path:      "/api/repos/acme/widgets/settings",
			grantBody: `{"ciCapabilities":["image-signing"]}`,
		},
		{
			name:      "PUT /api/deployment/repos/{owner}/{name}/config",
			method:    http.MethodPut,
			path:      "/api/deployment/repos/acme/widgets/config",
			grantBody: `{"ciCapabilities":["image-signing"]}`,
		},
	}

	for _, wp := range repoWritePaths {
		t.Run(wp.name+"/repo_grant_then_policy", func(t *testing.T) {
			t.Parallel()
			srv, c := setupAllowlistedRepoServer(t)

			if resp := doRecorded(t, srv, wp.method, wp.path, wp.grantBody); resp.Code() != http.StatusOK {
				t.Fatalf("saving repo CI grant first: %d %s", resp.Code(), resp.BodyString())
			}
			resp := doRecorded(t, srv, http.MethodPut, "/api/capability-policy", disableImageSigningPolicy)
			assertBadRequestOnField(t, resp, "catalogue",
				"defaults.repos[acme/widgets].ci",
				"https://grain.dev/capabilities/release#image-signing={}",
				"is disabled in settings/capabilities.yml",
				"in_process.image-signing, the capability it replaced",
			)
			assertStateExportChecksClean(t, c.Store)
		})

		t.Run(wp.name+"/policy_then_repo_grant", func(t *testing.T) {
			t.Parallel()
			srv, c := setupAllowlistedRepoServer(t)

			if resp := doRecorded(t, srv, http.MethodPut, "/api/capability-policy", disableImageSigningPolicy); resp.Code() != http.StatusOK {
				t.Fatalf("saving policy first: %d %s", resp.Code(), resp.BodyString())
			}
			resp := doRecorded(t, srv, wp.method, wp.path, wp.grantBody)
			assertBadRequestOnField(t, resp, "ciCapabilities",
				"defaults.repos[acme/widgets].ci",
				"https://grain.dev/capabilities/release#image-signing={}",
				"is disabled in settings/capabilities.yml",
				"in_process.image-signing, the capability it replaced",
			)
			assertStateExportChecksClean(t, c.Store)
		})
	}

	t.Run("PUT /api/capability-policy defaults.ci contradiction", func(t *testing.T) {
		t.Parallel()
		srv, c := setupAllowlistedRepoServer(t)

		contradictory := mustJSON(t, ui.CapabilityPolicyBody{
			Policy: model.CapabilityPolicy{
				Catalogue: &model.CapabilityCatalogue{
					Defaults: model.CatalogueDefaults{
						CI: []string{model.CanonicalLegacyGrant("image-signing")},
					},
					InProcess: map[string]model.CatalogueInProcess{
						"image-signing": {Enabled: &f},
					},
				},
			},
		})
		resp := doRecorded(t, srv, http.MethodPut, "/api/capability-policy", contradictory)
		assertBadRequestOnField(t, resp, "catalogue",
			"defaults.ci",
			"https://grain.dev/capabilities/release#image-signing={}",
			"is disabled in settings/capabilities.yml",
			"in_process.image-signing, the capability it replaced",
		)
		assertStateExportChecksClean(t, c.Store)
	})
}

// TestRefusesContradictoryTaskDefaultsAndPolicy verifies every repo and
// deployment defaultCapabilities write path (including settings/plan) against
// the catalogue in both save orders.
func TestRefusesContradictoryTaskDefaultsAndPolicy(t *testing.T) {
	t.Parallel()
	f := false
	disableMemoryPolicy := mustJSON(t, ui.CapabilityPolicyBody{
		Policy: model.CapabilityPolicy{
			Catalogue: &model.CapabilityCatalogue{
				InProcess: map[string]model.CatalogueInProcess{
					"memory": {Enabled: &f},
				},
			},
		},
	})

	taskWritePaths := []struct {
		name          string
		method        string
		path          string
		grantBody     string
		wantRefusalAt string
	}{
		{
			name:          "PUT /api/repos/{owner}/{name}/capabilities",
			method:        http.MethodPut,
			path:          "/api/repos/acme/widgets/capabilities",
			grantBody:     `{"defaultCapabilities":["memory"]}`,
			wantRefusalAt: "defaults.repos[acme/widgets].task",
		},
		{
			name:          "PUT /api/repos/{owner}/{name}/settings",
			method:        http.MethodPut,
			path:          "/api/repos/acme/widgets/settings",
			grantBody:     `{"defaultCapabilities":["memory"]}`,
			wantRefusalAt: "defaults.repos[acme/widgets].task",
		},
		{
			name:          "PUT /api/deployment/repos/{owner}/{name}/config",
			method:        http.MethodPut,
			path:          "/api/deployment/repos/acme/widgets/config",
			grantBody:     `{"defaultCapabilities":["memory"]}`,
			wantRefusalAt: "defaults.repos[acme/widgets].task",
		},
		{
			name:          "PUT /api/settings",
			method:        http.MethodPut,
			path:          "/api/settings",
			grantBody:     `{"defaultCapabilities":["memory"]}`,
			wantRefusalAt: "defaults.task",
		},
	}

	for _, wp := range taskWritePaths {
		t.Run(wp.name+"/default_grant_then_policy", func(t *testing.T) {
			t.Parallel()
			srv, c := setupAllowlistedRepoServer(t)

			if resp := doRecorded(t, srv, wp.method, wp.path, wp.grantBody); resp.Code() != http.StatusOK {
				t.Fatalf("saving task default first: %d %s", resp.Code(), resp.BodyString())
			}
			resp := doRecorded(t, srv, http.MethodPut, "/api/capability-policy", disableMemoryPolicy)
			assertBadRequestOnField(t, resp, "catalogue",
				wp.wantRefusalAt,
				"api:memory",
				"is disabled in settings/capabilities.yml",
				"in_process.memory, the capability it replaced",
			)
			assertStateExportChecksClean(t, c.Store)
		})

		t.Run(wp.name+"/policy_then_default_grant", func(t *testing.T) {
			t.Parallel()
			srv, c := setupAllowlistedRepoServer(t)

			if resp := doRecorded(t, srv, http.MethodPut, "/api/capability-policy", disableMemoryPolicy); resp.Code() != http.StatusOK {
				t.Fatalf("saving policy first: %d %s", resp.Code(), resp.BodyString())
			}
			resp := doRecorded(t, srv, wp.method, wp.path, wp.grantBody)
			assertBadRequestOnField(t, resp, "defaultCapabilities",
				wp.wantRefusalAt,
				"api:memory",
				"is disabled in settings/capabilities.yml",
				"in_process.memory, the capability it replaced",
			)
			assertStateExportChecksClean(t, c.Store)
		})
	}

	t.Run("POST /api/settings/plan refuses disabled defaultCapabilities", func(t *testing.T) {
		t.Parallel()
		srv, _ := setupAllowlistedRepoServer(t)
		if resp := doRecorded(t, srv, http.MethodPut, "/api/capability-policy", disableMemoryPolicy); resp.Code() != http.StatusOK {
			t.Fatalf("saving policy first: %d %s", resp.Code(), resp.BodyString())
		}
		resp := doRecorded(t, srv, http.MethodPost, "/api/settings/plan", `{"defaultCapabilities":["memory"]}`)
		assertBadRequestOnField(t, resp, "defaultCapabilities",
			"defaults.task",
			"api:memory",
			"is disabled in settings/capabilities.yml",
		)
	})
}

// TestRefusesDisablingMCPServerHeldByDefaults ensures disabling a capability
// server when a catalogue is active and defaults hold one of its grants is
// refused before export writes enabled: false into settings/capabilities.yml.
func TestRefusesDisablingMCPServerHeldByDefaults(t *testing.T) {
	t.Parallel()
	srv, c := setupAllowlistedRepoServer(t)
	ctx := context.Background()
	const serverID = "https://capabilities.example.com/custom"
	const grant = serverID + "#deploy={}"

	if err := c.Store.UpdateConfig(ctx, func(cfg *model.Config) error {
		cfg.MCPServers = map[string]protocol.ServerConfig{
			serverID: {Command: "echo"},
		}
		cfg.CapabilityPolicy.Catalogue = &model.CapabilityCatalogue{}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.Store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:           model.RepoRef{Owner: "acme", Name: "widgets"},
		CICapabilities: []string{grant},
	}); err != nil {
		t.Fatal(err)
	}

	resp := doRecorded(t, srv, http.MethodPost, "/api/capability-servers/disable", mustJSON(t, map[string]string{"id": serverID}))
	assertBadRequestOnField(t, resp, "disabled",
		"defaults.repos[acme/widgets].ci",
		"server "+serverID+" is disabled in settings/capabilities.yml",
	)
	assertStateExportChecksClean(t, c.Store)
}

// TestPreExistingContradictionCanBeClearedByHand verifies an upgraded store
// that already holds a contradiction does not deadlock manual cleanup from
// either side and does not block unrelated edits.
func TestPreExistingContradictionCanBeClearedByHand(t *testing.T) {
	t.Parallel()

	seedBroken := func(t *testing.T) (*ui.Server, *ui.Client) {
		t.Helper()
		srv, c := setupAllowlistedRepoServer(t)
		ctx := context.Background()
		f := false
		if err := c.Store.PutRepoConfig(ctx, model.RepoConfig{
			Repo:           model.RepoRef{Owner: "acme", Name: "widgets"},
			CICapabilities: []string{model.CanonicalLegacyGrant("image-signing")},
		}); err != nil {
			t.Fatal(err)
		}
		if err := c.Store.UpdateConfig(ctx, func(cfg *model.Config) error {
			cfg.CapabilityPolicy.Catalogue = &model.CapabilityCatalogue{
				InProcess: map[string]model.CatalogueInProcess{
					"image-signing": {Enabled: &f},
				},
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := exportStateAndCheck(t, c.Store); err == nil {
			t.Fatal("expected seeded contradiction to fail staterepo.Check before cleanup")
		}
		return srv, c
	}

	t.Run("clear via repo ciCapabilities while unrelated edits still work", func(t *testing.T) {
		t.Parallel()
		srv, c := seedBroken(t)

		// Unrelated repo setting and rules-only policy edits are not deadlocked.
		if resp := doRecorded(t, srv, http.MethodPut, "/api/repos/acme/widgets/settings", `{"promptExtension":"keep going"}`); resp.Code() != http.StatusOK {
			t.Fatalf("unrelated repo settings save: %d %s", resp.Code(), resp.BodyString())
		}
		if resp := doRecorded(t, srv, http.MethodPut, "/api/capability-policy", `{"policy":{"rules":[]}}`); resp.Code() != http.StatusOK {
			t.Fatalf("rules-only policy save: %d %s", resp.Code(), resp.BodyString())
		}

		// Clearing the repo's CI capabilities resolves the contradiction.
		if resp := doRecorded(t, srv, http.MethodPut, "/api/repos/acme/widgets/ci-capabilities", `{"ciCapabilities":[]}`); resp.Code() != http.StatusOK {
			t.Fatalf("clearing ciCapabilities: %d %s", resp.Code(), resp.BodyString())
		}
		assertStateExportChecksClean(t, c.Store)
	})

	t.Run("clear via capability policy re-enable", func(t *testing.T) {
		t.Parallel()
		srv, c := seedBroken(t)

		tr := true
		reenable := mustJSON(t, ui.CapabilityPolicyBody{
			Policy: model.CapabilityPolicy{
				Catalogue: &model.CapabilityCatalogue{
					InProcess: map[string]model.CatalogueInProcess{
						"image-signing": {Enabled: &tr},
					},
				},
			},
		})
		if resp := doRecorded(t, srv, http.MethodPut, "/api/capability-policy", reenable); resp.Code() != http.StatusOK {
			t.Fatalf("re-enabling image-signing in policy: %d %s", resp.Code(), resp.BodyString())
		}
		assertStateExportChecksClean(t, c.Store)
	})
}
