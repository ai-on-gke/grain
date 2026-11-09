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
// newWalkthroughRegistry starts a PostgreSQL-backed registry Server and
// returns a client factory acting as the given person.
func newWalkthroughRegistry(t *testing.T) (context.Context, *store.Store, func(email string) *api.Client) {
	t.Helper()
	pgURL := startEphemeralPostgresForRegistryTest(t)
	ctx := context.Background()
	st, err := store.Open(ctx, pgURL, store.DefaultThresholds())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
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
	t.Cleanup(ts.Close)
	clientFor := func(email string) *api.Client {
		c, err := api.NewClient(api.ClientConfig{
			BaseURL:           ts.URL,
			AllowInsecureHTTP: true,
			ExtraHeaders:      http.Header{"X-Test-Email": []string{email}},
		})
		if err != nil {
			t.Fatalf("NewClient(%s): %v", email, err)
		}
		return c
	}
	return ctx, st, clientFor

}

func TestReplaceRefusesConnectedEntries(t *testing.T) {
	ctx, st, clientFor := newWalkthroughRegistry(t)
	cli := clientFor("alice@example.com")

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

// TestOthersDeletedEntryReadsAsNotFound is the regression for the second
// 2026-11-08 walkthrough finding: after an admin purge released a deleted
// entry's name reservation, GET /api/names reported the name available but
// `grain registry register` by anybody else stopped at "caller does not have
// permission to view this entry" (403 from GET /api/entries/{name} on the
// tombstone), so the name could not be claimed through the CLI.
func TestOthersDeletedEntryReadsAsNotFound(t *testing.T) {
	ctx, _, clientFor := newWalkthroughRegistry(t)
	alice, bob, admin := clientFor("alice@example.com"), clientFor("bob@example.com"), clientFor("admin@example.com")

	if _, err := alice.CreateEntry(ctx, api.CreateEntryRequest{Name: "old-box", Kind: api.KindConnected}); err != nil {
		t.Fatalf("CreateEntry: %v", err)
	}
	if err := alice.DeleteEntry(ctx, "old-box", api.DeleteEntryRequest{Confirm: "old-box", ConfirmName: "old-box"}); err != nil {
		t.Fatalf("DeleteEntry: %v", err)
	}
	if _, err := bob.GetEntry(ctx, "old-box"); !api.IsNotFound(err) {
		t.Fatalf("bob GET of alice's deleted entry = %v, want not found", err)
	}
	if _, err := alice.GetEntry(ctx, "old-box"); err != nil {
		t.Fatalf("alice GET of her own deleted entry: %v (owners still see tombstones to restore them)", err)
	}
	if _, err := admin.PurgeEntry(ctx, "old-box", api.HardPurgeRequest{ReleaseReservation: true}); err != nil {
		t.Fatalf("PurgeEntry: %v", err)
	}
	if _, err := bob.CreateEntry(ctx, api.CreateEntryRequest{Name: "old-box", Kind: api.KindConnected}); err != nil {
		t.Fatalf("bob claiming the released name: %v", err)
	}
	got, err := bob.GetEntry(ctx, "old-box")
	if err != nil || got.Owner != "bob@example.com" {
		t.Fatalf("bob GET of his new old-box = %+v, %v", got, err)
	}
}

// TestUpgradeRefusesConnectedEntries covers the 2026-11-09 registry walkthrough
// finding: POST /api/entries/{name}/upgrade (`grain registry upgrade`) accepted
// `connected` entries and stored `target_version` on them even though only
// hosted `instance` VMs are upgraded by the reconciler.
func TestUpgradeRefusesConnectedEntries(t *testing.T) {
	ctx, st, clientFor := newWalkthroughRegistry(t)
	cli := clientFor("alice@example.com")

	if _, err := cli.CreateEntry(ctx, api.CreateEntryRequest{Name: "alice-laptop", Kind: api.KindConnected}); err != nil {
		t.Fatalf("CreateEntry connected: %v", err)
	}
	if _, err := cli.UpgradeEntry(ctx, "alice-laptop", api.UpgradeEntryRequest{TargetVersion: "v3.2.0"}); err == nil || !strings.Contains(err.Error(), "invalid_entry_kind") {
		t.Fatalf("UpgradeEntry on a connected entry = %v, want invalid_entry_kind", err)
	}
	got, err := st.GetEntryByName(ctx, "alice-laptop")
	if err != nil {
		t.Fatalf("GetEntryByName: %v", err)
	}
	if got.TargetVersion != "" {
		t.Fatalf("connected entry target_version = %q after refused upgrade, want empty", got.TargetVersion)
	}

	if _, err := cli.CreateEntry(ctx, api.CreateEntryRequest{Name: "alice-vm", Kind: api.KindInstance, Shape: "small"}); err != nil {
		t.Fatalf("CreateEntry instance: %v", err)
	}
	if _, err := cli.UpgradeEntry(ctx, "alice-vm", api.UpgradeEntryRequest{TargetVersion: "v3.2.0"}); err != nil {
		t.Fatalf("UpgradeEntry on a hosted instance: %v", err)
	}
}

// TestListEntriesRedactsStatusDetailForPendingTransferRecipient verifies that
// GET /api/entries redacts status_detail and credentials for a pending transfer
// recipient who does not yet manage the entry, matching GET /api/entries/{name}.
func TestListEntriesRedactsStatusDetailForPendingTransferRecipient(t *testing.T) {
	ctx, st, clientFor := newWalkthroughRegistry(t)
	alice, bob, admin := clientFor("alice@example.com"), clientFor("bob@example.com"), clientFor("admin@example.com")

	if _, err := alice.CreateEntry(ctx, api.CreateEntryRequest{Name: "xfer-vm", Kind: api.KindInstance, Shape: "small"}); err != nil {
		t.Fatalf("CreateEntry: %v", err)
	}
	row, err := st.GetEntryByName(ctx, "xfer-vm")
	if err != nil {
		t.Fatalf("GetEntryByName: %v", err)
	}
	const secretReason = "waiting for service account gi-xfer-vm@acme-prod.iam.gserviceaccount.com to propagate"
	if _, err := st.DB().ExecContext(ctx, `UPDATE registry.entries SET status_reason = $1 WHERE id = $2`, secretReason, row.ID); err != nil {
		t.Fatalf("set status_reason: %v", err)
	}
	if _, err := alice.OfferTransfer(ctx, "xfer-vm", api.TransferRequest{To: "bob@example.com"}); err != nil {
		t.Fatalf("OfferTransfer: %v", err)
	}

	bobShow, err := bob.GetEntry(ctx, "xfer-vm")
	if err != nil {
		t.Fatalf("bob GetEntry: %v", err)
	}
	if bobShow.StatusDetail != "" {
		t.Fatalf("bob GetEntry StatusDetail = %q, want redacted empty", bobShow.StatusDetail)
	}
	bobList, err := bob.ListEntries(ctx, api.ListEntriesQuery{Mine: true})
	if err != nil {
		t.Fatalf("bob ListEntries: %v", err)
	}
	if len(bobList) != 1 || bobList[0].Name != "xfer-vm" {
		t.Fatalf("bob ListEntries = %+v, want [xfer-vm]", bobList)
	}
	if bobList[0].StatusDetail != "" {
		t.Fatalf("bob ListEntries StatusDetail = %q, want redacted empty", bobList[0].StatusDetail)
	}

	aliceList, err := alice.ListEntries(ctx, api.ListEntriesQuery{Mine: true})
	if err != nil || len(aliceList) != 1 || aliceList[0].StatusDetail != secretReason {
		t.Fatalf("alice ListEntries = %+v, %v, want StatusDetail=%q", aliceList, err, secretReason)
	}
	adminList, err := admin.ListEntries(ctx, api.ListEntriesQuery{All: true})
	if err != nil || len(adminList) != 1 || adminList[0].StatusDetail != secretReason {
		t.Fatalf("admin ListEntries = %+v, %v, want StatusDetail=%q", adminList, err, secretReason)
	}
}
