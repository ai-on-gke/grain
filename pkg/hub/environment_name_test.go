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
	"testing"

	"github.com/ai-on-gke/grain/pkg/hub/api"
	"github.com/ai-on-gke/grain/pkg/hub/store"
	"github.com/ai-on-gke/grain/pkg/webauth"
)

// A connected entry's heartbeat reply names the environment name the hub
// gives its grain by default -- the display name, else the entry name --
// and a rename of the entry is in the very next reply, which is how it
// reaches a running daemon (model.Config.AdoptHubEnvironmentName).
func TestHeartbeatResponseCarriesTheEntrysEnvironmentName(t *testing.T) {
	pgURL := startEphemeralPostgresForHubTest(t)
	ctx := context.Background()
	st, err := store.Open(ctx, pgURL, store.DefaultThresholds())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	cfg, err := ParseConfig([]byte(validTestYAML()))
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
	signer, err := NewEphemeralLocalSigner("env-k1")
	if err != nil {
		t.Fatalf("NewEphemeralLocalSigner: %v", err)
	}
	srv, err := NewServer(ServerOptions{
		Config:    *cfg,
		Store:     st,
		Admitter:  admitter,
		Groups:    webauthGroupResolverAdapter{inner: groups},
		Signer:    signer,
		ReplicaID: "rep-env",
		PeerAddr:  "127.0.0.1:18083",
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	alice, err := api.NewClient(api.ClientConfig{
		BaseURL:           ts.URL,
		AllowInsecureHTTP: true,
		ExtraHeaders:      http.Header{"X-Test-Email": []string{"alice@example.com"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := alice.CreateEntry(ctx, api.CreateEntryRequest{Name: "env-conn", Kind: api.KindConnected})
	if err != nil {
		t.Fatalf("CreateEntry: %v", err)
	}
	daemon, err := api.NewClient(api.ClientConfig{
		BaseURL:           ts.URL,
		AllowInsecureHTTP: true,
		RegistrationToken: created.RegistrationToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	heartbeat := func() string {
		t.Helper()
		resp, err := daemon.Heartbeat(ctx, "env-conn", api.HeartbeatRequest{GrainVersion: "v1.0.0", Health: api.HealthOK})
		if err != nil {
			t.Fatalf("heartbeat: %v", err)
		}
		return resp.EnvironmentName
	}

	if got := heartbeat(); got != "env-conn" {
		t.Errorf("environment_name with no display name = %q, want the entry name %q", got, "env-conn")
	}
	display := "Payments\tstaging"
	if _, err := alice.UpdateEntry(ctx, "env-conn", api.UpdateEntryRequest{DisplayName: &display}); err != nil {
		t.Fatalf("UpdateEntry: %v", err)
	}
	if got, want := heartbeat(), "Payments staging"; got != want {
		t.Errorf("environment_name after the display name changed = %q, want %q (sanitized)", got, want)
	}
}
