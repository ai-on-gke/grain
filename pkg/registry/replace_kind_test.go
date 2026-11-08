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

package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/registry/api"
	"github.com/ai-on-gke/grain/pkg/registry/store"
	"github.com/ai-on-gke/grain/pkg/webauth"
)

// TestReplaceRefusesConnectedEntries is the regression for the 2026-11-08
// registry walkthrough: `grain registry resize -disk-gb 200 <connected>` was
// accepted, stored boot_disk_gb on the connected entry and moved it to
// lifecycle_state "replacing", after which the registry answered every request
// for it with the "currently replacing" state page instead of routing to its
// live tunnel.
func TestReplaceRefusesConnectedEntries(t *testing.T) {
	pgURL := startEphemeralPostgresForRegistryTest(t)
	ctx := context.Background()
	st, err := store.Open(ctx, pgURL, store.DefaultThresholds())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	if err := st.SeedShapes(ctx, []store.ShapeRow{
		{Name: "small", MachineType: "e2-standard-4", BootDiskGB: 50, QuotaUnits: 1},
	}); err != nil {
		t.Fatalf("SeedShapes: %v", err)
	}
	admitter, err := webauth.NewAdmitter(webauth.AdmissionConfig{
		Verifier:       fakeFrontIdentity{},
		AllowedDomains: []string{"example.com"},
		Admins:         []string{"user:admin@example.com"},
	})
	if err != nil {
		t.Fatalf("NewAdmitter: %v", err)
	}
	signer, err := NewEphemeralLocalSigner("replace-kind-k1")
	if err != nil {
		t.Fatalf("NewEphemeralLocalSigner: %v", err)
	}
	cfg, err := ParseConfig([]byte(validTestYAML()))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	srv, err := NewServer(ServerOptions{Config: *cfg, Store: st, Admitter: admitter, Signer: signer, ReplicaID: "rep-replace-kind"})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	cli, err := api.NewClient(api.ClientConfig{
		BaseURL:           ts.URL,
		AllowInsecureHTTP: true,
		ExtraHeaders:      http.Header{"X-Test-Email": []string{"alice@example.com"}},
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if _, err := cli.CreateEntry(ctx, api.CreateEntryRequest{Name: "alice-laptop", Kind: api.KindConnected}); err != nil {
		t.Fatalf("CreateEntry connected: %v", err)
	}
	for _, req := range []api.ReplaceEntryRequest{{BootDiskGB: 200}, {Shape: "small"}, {}} {
		if _, err := cli.ReplaceEntry(ctx, "alice-laptop", req); err == nil || !strings.Contains(err.Error(), "invalid_entry_kind") {
			t.Fatalf("ReplaceEntry(%+v) on a connected entry = %v, want invalid_entry_kind", req, err)
		}
	}
	got, err := st.GetEntryByName(ctx, "alice-laptop")
	if err != nil {
		t.Fatalf("GetEntryByName: %v", err)
	}
	if got.LifecycleState == api.StateReplacing || got.BootDiskGB != 0 {
		t.Fatalf("connected entry after refused replace: state %q boot_disk_gb %d, want unchanged", got.LifecycleState, got.BootDiskGB)
	}

	// Hosted instances still resize.
	if _, err := cli.CreateEntry(ctx, api.CreateEntryRequest{Name: "alice-vm", Kind: api.KindInstance, Shape: "small"}); err != nil {
		t.Fatalf("CreateEntry instance: %v", err)
	}
	vm, err := st.GetEntryByName(ctx, "alice-vm")
	if err != nil {
		t.Fatalf("GetEntryByName alice-vm: %v", err)
	}
	if _, err := st.DB().ExecContext(ctx, `UPDATE registry.entries SET lifecycle_state = 'running' WHERE id = $1`, vm.ID); err != nil {
		t.Fatalf("mark alice-vm running: %v", err)
	}
	if _, err := cli.ReplaceEntry(ctx, "alice-vm", api.ReplaceEntryRequest{BootDiskGB: 200}); err != nil {
		t.Fatalf("ReplaceEntry on a hosted instance: %v", err)
	}
}
