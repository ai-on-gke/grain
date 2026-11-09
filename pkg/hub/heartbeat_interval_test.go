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

package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/hub/api"
	"github.com/ai-on-gke/grain/pkg/hub/store"
	"github.com/ai-on-gke/grain/pkg/webauth"
)

// TestHeartbeatResponseCarriesConfiguredCatalogInterval pins the 2026-11-07
// walkthrough finding: both heartbeat handlers told daemons to come back in a
// hard-coded 15s whatever catalog.heartbeat_interval said, so a registry
// configured with heartbeat_interval 2s / degraded_after 6s / offline_after
// 12s (a config Validate accepts) showed every connected entry offline
// between heartbeats.
func TestHeartbeatResponseCarriesConfiguredCatalogInterval(t *testing.T) {
	pgURL := startEphemeralPostgresForRegistryTest(t)
	ctx := context.Background()
	st, err := store.Open(ctx, pgURL, store.DefaultThresholds())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	yaml := strings.Replace(validTestYAML(), "signing:\n", "catalog:\n  heartbeat_interval: 4s\n  degraded_after: 10s\n  offline_after: 20s\nsigning:\n", 1)
	cfg, err := ParseConfig([]byte(yaml))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	groups := webauth.NewStaticGroups(map[string][]string{})
	admitter, err := webauth.NewAdmitter(webauth.AdmissionConfig{
		Verifier:       fakeFrontIdentity{},
		Groups:         groups,
		AllowedDomains: []string{"example.com"},
	})
	if err != nil {
		t.Fatalf("NewAdmitter: %v", err)
	}
	signer, err := NewEphemeralLocalSigner("hb-k1")
	if err != nil {
		t.Fatalf("NewEphemeralLocalSigner: %v", err)
	}
	srv, err := NewServer(ServerOptions{
		Config:    *cfg,
		Store:     st,
		Admitter:  admitter,
		Groups:    webauthGroupResolverAdapter{inner: groups},
		Signer:    signer,
		ReplicaID: "rep-hb",
		PeerAddr:  "127.0.0.1:18082",
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	mainTS := httptest.NewServer(srv.Handler())
	defer mainTS.Close()
	pubTS := httptest.NewServer(srv.PublicHandler())
	defer pubTS.Close()

	alice, err := api.NewClient(api.ClientConfig{
		BaseURL:           mainTS.URL,
		AllowInsecureHTTP: true,
		ExtraHeaders:      http.Header{"X-Test-Email": []string{"alice@example.com"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := alice.CreateEntry(ctx, api.CreateEntryRequest{Name: "hb-conn", Kind: api.KindConnected})
	if err != nil {
		t.Fatalf("CreateEntry: %v", err)
	}
	// Ten heartbeats two seconds apart on the router's clock (faster than
	// the default 12/min limit refills, slower than three per 4s
	// interval) also pin that the per-entry heartbeat limiter scales
	// with the interval the registry asks for.
	now := time.Now()
	srv.Router().SetNow(func() time.Time { return now })
	for i := 0; i < 10; i++ {
		name, base := "main", mainTS.URL
		if i%2 == 1 {
			name, base = "public", pubTS.URL
		}
		c, err := api.NewClient(api.ClientConfig{
			BaseURL:           base,
			AllowInsecureHTTP: true,
			RegistrationToken: created.RegistrationToken,
		})
		if err != nil {
			t.Fatal(err)
		}
		resp, err := c.Heartbeat(ctx, "hb-conn", api.HeartbeatRequest{GrainVersion: "v1.0.0", Health: api.HealthOK})
		if err != nil {
			t.Fatalf("heartbeat %d (%s listener): %v", i, name, err)
		}
		if resp.NextIntervalSeconds != 4 || resp.NextHeartbeatSeconds != 4 {
			t.Errorf("%s listener heartbeat next interval = %d/%d, want 4/4 (catalog.heartbeat_interval)", name, resp.NextIntervalSeconds, resp.NextHeartbeatSeconds)
		}
		now = now.Add(2 * time.Second)
	}
}
