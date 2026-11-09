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

package security

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ui"
)

// TestHubTargetSettingsAreAPersonsAndAudited holds changes to registry
// credential targets (url, authMode, oidcAudience) to a human principal -- an
// agent's request is refused so a run cannot redirect the daemon's registration
// secret or mint host OIDC tokens for an attacker-chosen audience -- and
// records an authorized change in the credential-access audit log. The entry
// name and ownerGroup are held the same way: in OIDC mode the daemon acts on
// whichever entry is named with the host's identity, and ownerGroup members may
// manage the entry like its owner.
func TestHubTargetSettingsAreAPersonsAndAudited(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)

	f := newRouteFixtureFor(t, true)

	for _, attempt := range []struct {
		name string
		body string
	}{
		{
			name: "set registry URL",
			body: `{"registry":{"url":"https://evil.example.com","entry":"grain-dev"}}`,
		},
		{
			name: "switch authMode to oidc",
			body: `{"registry":{"authMode":"oidc"}}`,
		},
		{
			name: "set oidcAudience",
			body: `{"registry":{"oidcAudience":"https://internal.example.com"}}`,
		},
	} {
		rec := serveAsAgent(t, f, http.MethodPut, "/api/settings", attempt.body)
		if rec.code != http.StatusForbidden {
			t.Errorf("agent %s got %d %s, want 403", attempt.name, rec.code, rec.excerpt())
		}
	}
	if cfg, err := f.store.GetConfig(context.Background()); err != nil {
		t.Fatal(err)
	} else if cfg != nil && (cfg.Hub.URL != "" || cfg.Hub.OIDCAudience != "") {
		t.Fatalf("refused agent saves persisted registry config: %+v", cfg.Hub)
	}

	saveBody := `{"registry":{"url":"https://registry.example.com","entry":"grain-dev","authMode":"oidc","oidcAudience":"https://registry.example.com"}}`
	if rec := f.serve(t, http.MethodPut, "/api/settings", saveBody, http.Header{}); rec.code != http.StatusOK {
		t.Fatalf("operator PUT /api/settings: %d %s, want 200", rec.code, rec.excerpt())
	}

	entries, err := f.store.ListCredentialAccess(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if e.Resource == "registry" && strings.Contains(e.Outcome, `"https://registry.example.com"`) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("saving registry target left no credential-access audit entry: %+v", entries)
	}

	// Once configured, an agent still cannot redirect URL or change OIDC audience,
	// while non-credential catalog fields (purpose, listed) remain updatable when
	// URL, authMode, and oidcAudience stay unchanged.
	redirectBody := `{"registry":{"url":"https://attacker.example.com","entry":"grain-dev","authMode":"oidc","oidcAudience":"https://registry.example.com"}}`
	if rec := serveAsAgent(t, f, http.MethodPut, "/api/settings", redirectBody); rec.code != http.StatusForbidden {
		t.Errorf("agent redirecting configured registry URL got %d %s, want 403", rec.code, rec.excerpt())
	}
	// Nor may it point the host's identity at another entry, or hand
	// management of this entry (credential rotation, the tunnel) to a group
	// of its choosing by changing ownerGroup.
	for _, attempt := range []struct {
		name string
		body string
	}{
		{
			name: "retarget the entry name",
			body: `{"registry":{"url":"https://registry.example.com","entry":"someone-elses","authMode":"oidc","oidcAudience":"https://registry.example.com"}}`,
		},
		{
			name: "set ownerGroup",
			body: `{"registry":{"url":"https://registry.example.com","entry":"grain-dev","authMode":"oidc","oidcAudience":"https://registry.example.com","ownerGroup":"group:attackers@example.com"}}`,
		},
	} {
		if rec := serveAsAgent(t, f, http.MethodPut, "/api/settings", attempt.body); rec.code != http.StatusForbidden {
			t.Errorf("agent %s got %d %s, want 403", attempt.name, rec.code, rec.excerpt())
		}
	}
	if cfg, err := f.store.GetConfig(context.Background()); err != nil {
		t.Fatal(err)
	} else if cfg.Hub.Entry != "grain-dev" || cfg.Hub.OwnerGroup != "" {
		t.Fatalf("refused agent saves persisted registry entry/ownerGroup: %+v", cfg.Hub)
	}
	metaBody := `{"registry":{"url":"https://registry.example.com","entry":"grain-dev","authMode":"oidc","oidcAudience":"https://registry.example.com","purpose":"Updated purpose","listed":true}}`
	if rec := serveAsAgent(t, f, http.MethodPut, "/api/settings", metaBody); rec.code != http.StatusOK {
		t.Errorf("agent updating non-credential registry metadata got %d %s, want 200", rec.code, rec.excerpt())
	}
}
