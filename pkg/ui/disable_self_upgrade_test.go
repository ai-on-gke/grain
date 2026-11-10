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
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
	"github.com/ai-on-gke/grain/pkg/upgrade"
)

func TestDisableSelfUpgradeBlocksUpgradeAndDatabaseCutoverRoutes(t *testing.T) {
	_, store, ctx := testClient(t)
	if err := store.PutConfig(ctx, model.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	refFile := filepath.Join(dir, "image.env")
	up := upgrade.New(upgrade.Config{
		Image: &upgrade.ImageConfig{
			Repository: "docker.io/grainai/grain",
			RefFile:    refFile,
		},
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
		AllowedBranches: []string{"main"},
	})
	t.Cleanup(func() { waitForUpgradeToFinish(t, up) })

	srv := ui.NewServer(ui.Config{
		Actor:              ui.DefaultActor("alice"),
		Capabilities:       ui.OfferedCapabilities(),
		Upgrader:           up,
		DisableSelfUpgrade: true,
		ManagementURL:      "https://grain.example.com/entries/alice",
	}, store)

	rec := do(t, srv, http.MethodGet, "/api/upgrade", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/upgrade = %d: %s", rec.Code, rec.Body)
	}
	var status struct {
		Enabled             bool   `json:"enabled"`
		SelfUpgradeDisabled bool   `json:"selfUpgradeDisabled"`
		ManagementURL       string `json:"managementUrl"`
	}
	status = decode[struct {
		Enabled             bool   `json:"enabled"`
		SelfUpgradeDisabled bool   `json:"selfUpgradeDisabled"`
		ManagementURL       string `json:"managementUrl"`
	}](t, rec)
	if status.Enabled {
		t.Fatalf("GET /api/upgrade enabled = true, want false")
	}
	if !status.SelfUpgradeDisabled {
		t.Fatalf("GET /api/upgrade selfUpgradeDisabled = false, want true")
	}
	if status.ManagementURL != "https://grain.example.com/entries/alice" {
		t.Fatalf("GET /api/upgrade managementUrl = %q, want https://grain.example.com/entries/alice", status.ManagementURL)
	}

	cfgRec := do(t, srv, http.MethodGet, "/api/config", "")
	if cfgRec.Code != http.StatusOK {
		t.Fatalf("GET /api/config = %d: %s", cfgRec.Code, cfgRec.Body)
	}
	cfgBody := decode[struct {
		SelfUpgradeDisabled bool   `json:"selfUpgradeDisabled"`
		ManagementURL       string `json:"managementUrl"`
	}](t, cfgRec)
	if !cfgBody.SelfUpgradeDisabled || cfgBody.ManagementURL != "https://grain.example.com/entries/alice" {
		t.Fatalf("GET /api/config = %+v, want selfUpgradeDisabled=true and managementUrl", cfgBody)
	}

	blocked := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/api/upgrade", `{"branch":"main"}`},
		{http.MethodPost, "/api/upgrade", `{"branch":"main","afterTasksFinish":true}`},
		{http.MethodGet, "/api/upgrade/targets", ""},
		{http.MethodGet, "/api/upgrade/release-notes?branch=main", ""},
		{http.MethodDelete, "/api/upgrade/scheduled", ""},
	}
	for _, tc := range blocked {
		r := do(t, srv, tc.method, tc.path, tc.body)
		if r.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d (%s), want 404", tc.method, tc.path, r.Code, r.Body.String())
		}
		if !strings.Contains(r.Body.String(), "-disable-self-upgrade") {
			t.Errorf("%s %s body = %q, want -disable-self-upgrade explanation", tc.method, tc.path, r.Body.String())
		}
	}

	for _, path := range []string{
		"/api/database/test",
		"/api/database/upgrade",
		"/api/upgrade/postgres",
		"/api/settings/db/migrate-to-postgres",
	} {
		r := do(t, srv, http.MethodPost, path, `{"dsn":"postgres://localhost:5432/grain"}`)
		if r.Code != http.StatusNotFound && r.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s = %d (%s), want 404 or 405", path, r.Code, r.Body.String())
		}
	}
}

func TestDisableSelfUpgradeSanitizesUnsafeManagementURL(t *testing.T) {
	_, store, ctx := testClient(t)
	if err := store.PutConfig(ctx, model.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	for _, unsafeURL := range []string{
		"javascript:alert(1)",
		"https://:8080/entries/alice",
		"https://user:secret@grain.example.com/entries/alice",
	} {
		srv := ui.NewServer(ui.Config{
			Actor:              ui.DefaultActor("alice"),
			DisableSelfUpgrade: true,
			ManagementURL:      unsafeURL,
		}, store)

		cfgRec := do(t, srv, http.MethodGet, "/api/config", "")
		if cfgRec.Code != http.StatusOK {
			t.Fatalf("GET /api/config (%q) = %d: %s", unsafeURL, cfgRec.Code, cfgRec.Body)
		}
		cfgBody := decode[struct {
			SelfUpgradeDisabled bool   `json:"selfUpgradeDisabled"`
			ManagementURL       string `json:"managementUrl"`
		}](t, cfgRec)
		if !cfgBody.SelfUpgradeDisabled || cfgBody.ManagementURL != "" {
			t.Fatalf("GET /api/config (%q) = %+v, want selfUpgradeDisabled=true and sanitized empty managementUrl", unsafeURL, cfgBody)
		}

		upRec := do(t, srv, http.MethodGet, "/api/upgrade", "")
		if upRec.Code != http.StatusOK {
			t.Fatalf("GET /api/upgrade (%q) = %d: %s", unsafeURL, upRec.Code, upRec.Body)
		}
		upBody := decode[struct {
			SelfUpgradeDisabled bool   `json:"selfUpgradeDisabled"`
			ManagementURL       string `json:"managementUrl"`
		}](t, upRec)
		if !upBody.SelfUpgradeDisabled || upBody.ManagementURL != "" {
			t.Fatalf("GET /api/upgrade (%q) = %+v, want selfUpgradeDisabled=true and sanitized empty managementUrl", unsafeURL, upBody)
		}
	}

	srv := ui.NewServer(ui.Config{
		Actor:              ui.DefaultActor("alice"),
		DisableSelfUpgrade: true,
		ManagementURL:      "https://grain.example.com/entries/alice",
	}, store)
	unauthReq := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	unauthReq.RemoteAddr = "203.0.113.10:54321"
	unauthRec := httptest.NewRecorder()
	srv.ServeHTTP(unauthRec, unauthReq)
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GET /api/config = %d, want 401", unauthRec.Code)
	}
}
