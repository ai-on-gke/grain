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

package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/hub/api"
)

// TestRegistryCLIDelegatingAppsAndGrants drives every
// `grain hub delegating-apps` and `grain hub grants` verb against a
// fake registry and checks each sends the request the registry's
// /api/admin/delegating-apps and /api/delegation-grants routes expect.
func TestRegistryCLIDelegatingAppsAndGrants(t *testing.T) {
	ctx := context.Background()
	type call struct {
		method, path, query string
		body                map[string]any
	}
	var (
		mu    sync.Mutex
		calls []call
	)
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	app := api.DelegatingApp{
		Name: "planner", Principal: "serviceAccount:planner@proj.iam.gserviceaccount.com",
		DisplayName: "Planner", Owner: "admin@example.com", MaxAccess: "read",
		MaxGrantTTLSeconds: 7 * 24 * 3600, CreatedBy: "admin@example.com", CreatedAt: now,
	}
	grant := api.DelegationGrant{
		ID: "dlg_0123456789abcdef", App: "planner", Person: "alice@example.com",
		Entries: []string{"alice-vm", "alice-lab"}, Access: "read",
		CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(24 * time.Hour),
	}
	regSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := call{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery}
		_ = json.NewDecoder(r.Body).Decode(&c.body)
		mu.Lock()
		calls = append(calls, c)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/admin/delegating-apps":
			_ = json.NewEncoder(w).Encode(api.ListDelegatingAppsResponse{Apps: []api.DelegatingApp{app}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/admin/delegating-apps":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(app)
		case r.Method == http.MethodPatch && r.URL.Path == "/api/admin/delegating-apps/planner":
			a := app
			if d, _ := c.body["disabled"].(bool); d {
				a.DisabledAt = &now
			}
			_ = json.NewEncoder(w).Encode(a)
		case r.Method == http.MethodGet && r.URL.Path == "/api/delegation-grants":
			_ = json.NewEncoder(w).Encode(api.ListDelegationGrantsResponse{Grants: []api.DelegationGrant{grant}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/delegation-grants":
			_ = json.NewEncoder(w).Encode(grant)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/delegation-grants/"):
			g := grant
			g.RevokedAt = &now
			_ = json.NewEncoder(w).Encode(g)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not found","code":"not_found"}`))
		}
	}))
	defer regSrv.Close()

	prevClient, prevOIDC := registryHTTPClient, registryOIDCToken
	registryHTTPClient = regSrv.Client()
	registryOIDCToken = func(ctx context.Context, registryURL, audience string) (string, error) {
		return "oidc-test-token", nil
	}
	t.Cleanup(func() { registryHTTPClient, registryOIDCToken = prevClient, prevOIDC })

	for _, asJSON := range []bool{true, false} {
		out := &printer{json: asJSON}
		run := func(group, verb string, args ...string) {
			t.Helper()
			full := append([]string{group, verb, "-hub-url", regSrv.URL}, args...)
			if err := cmdRegistry(ctx, nil, out, full); err != nil {
				t.Fatalf("cmdRegistry(%v) = %v", full, err)
			}
		}
		mu.Lock()
		calls = nil
		mu.Unlock()

		run("delegating-apps", "list")
		run("delegating-apps", "create", "-principal", "planner@proj.iam.gserviceaccount.com",
			"-display-name", "Planner", "-max-access", "read", "-max-grant-ttl", "168h",
			"-redirect-uri", "https://planner.example.com/cb", "planner")
		run("delegating-apps", "disable", "planner")
		run("delegating-apps", "enable", "planner")
		run("grants", "list", "-include-revoked")
		run("grants", "create", "-entry", "alice-vm,alice-lab", "-access", "read", "-ttl", "24h", "planner")
		run("grants", "revoke", "planner")

		mu.Lock()
		got := append([]call(nil), calls...)
		mu.Unlock()
		want := []struct{ method, path string }{
			{http.MethodGet, "/api/admin/delegating-apps"},
			{http.MethodPost, "/api/admin/delegating-apps"},
			{http.MethodPatch, "/api/admin/delegating-apps/planner"},
			{http.MethodPatch, "/api/admin/delegating-apps/planner"},
			{http.MethodGet, "/api/delegation-grants"},
			{http.MethodPost, "/api/delegation-grants"},
			{http.MethodDelete, "/api/delegation-grants/planner"},
		}
		if len(got) != len(want) {
			t.Fatalf("json=%v: %d requests, want %d: %+v", asJSON, len(got), len(want), got)
		}
		for i, w := range want {
			if got[i].method != w.method || got[i].path != w.path {
				t.Fatalf("json=%v: request %d = %s %s, want %s %s", asJSON, i, got[i].method, got[i].path, w.method, w.path)
			}
		}
		create := got[1].body
		if create["name"] != "planner" || create["principal"] != "planner@proj.iam.gserviceaccount.com" ||
			create["max_access"] != "read" || create["max_grant_ttl_seconds"] != float64(7*24*3600) {
			t.Errorf("delegating-apps create body = %+v", create)
		}
		if d, ok := got[2].body["disabled"].(bool); !ok || !d {
			t.Errorf("delegating-apps disable body = %+v, want disabled=true", got[2].body)
		}
		if d, ok := got[3].body["disabled"].(bool); !ok || d {
			t.Errorf("delegating-apps enable body = %+v, want disabled=false", got[3].body)
		}
		if got[4].query != "include_revoked=true" {
			t.Errorf("grants list query = %q, want include_revoked=true", got[4].query)
		}
		gc := got[5].body
		entries, _ := gc["entries"].([]any)
		if gc["app"] != "planner" || len(entries) != 2 || entries[0] != "alice-vm" || entries[1] != "alice-lab" ||
			gc["access"] != "read" || gc["ttl_seconds"] != float64(24*3600) {
			t.Errorf("grants create body = %+v", gc)
		}
		if _, ok := gc["person"]; ok {
			t.Errorf("grants create sent a person (%v): a grant is always the signed-in person's own", gc["person"])
		}
	}

	// Usage errors: create without -principal, grants create without -entry.
	out := &printer{}
	if err := cmdRegistry(ctx, nil, out, []string{"delegating-apps", "create", "-hub-url", regSrv.URL, "planner"}); err == nil {
		t.Error("delegating-apps create without -principal succeeded")
	}
	if err := cmdRegistry(ctx, nil, out, []string{"grants", "create", "-hub-url", regSrv.URL, "planner"}); err == nil {
		t.Error("grants create without -entry succeeded")
	}
	if err := cmdRegistry(ctx, nil, out, []string{"grants", "bogus"}); err == nil {
		t.Error("unknown grants verb succeeded")
	}
}
