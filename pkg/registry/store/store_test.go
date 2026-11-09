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

package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/registry/api"
)

func TestMigrationsAndFillfactor80(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// Re-running Migrate must be idempotent.
	if err := Migrate(ctx, st.DB()); err != nil {
		t.Fatalf("second Migrate call failed: %v", err)
	}

	var reloptions string
	err := st.DB().QueryRowContext(ctx, `
SELECT COALESCE(array_to_string(c.reloptions, ','), '')
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'registry' AND c.relname = 'entries'`).Scan(&reloptions)
	if err != nil {
		t.Fatalf("querying pg_class reloptions: %v", err)
	}
	if !strings.Contains(strings.ToLower(reloptions), "fillfactor=80") {
		t.Fatalf("expected registry.entries reloptions to contain fillfactor=80, got %q", reloptions)
	}
}

// TestMigrationReplacesE2ShapeMachineTypes holds migration 8: shape rows an
// older registry seeded with E2 machine types (which cannot run the nested
// virtualization every hosted VM needs) move to their N2 equivalents, and
// rows already on a supported machine type are left alone.
func TestMigrationReplacesE2ShapeMachineTypes(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	legacy := map[string]string{
		"legacy-small":  "e2-small",
		"legacy-micro":  "e2-micro",
		"legacy-std4":   "e2-standard-4",
		"legacy-std8":   "e2-standard-8",
		"legacy-mem16":  "e2-highmem-16",
		"legacy-zonal":  "zones/us-central1-a/machineTypes/e2-standard-2",
		"current-n2":    "n2-standard-16",
		"current-c4":    "c4-standard-8",
		"current-n2d-x": "n2d-standard-4",
	}
	want := map[string]string{
		"legacy-small":  "n2-standard-2",
		"legacy-micro":  "n2-standard-2",
		"legacy-std4":   "n2-standard-4",
		"legacy-std8":   "n2-standard-8",
		"legacy-mem16":  "n2-highmem-16",
		"legacy-zonal":  "zones/us-central1-a/machineTypes/n2-standard-2",
		"current-n2":    "n2-standard-16",
		"current-c4":    "c4-standard-8",
		"current-n2d-x": "n2d-standard-4",
	}
	for name, mt := range legacy {
		if _, err := st.DB().ExecContext(ctx, `INSERT INTO registry.shapes (name, machine_type, boot_disk_gb, quota_units) VALUES ($1, $2, 50, 1)`, name, mt); err != nil {
			t.Fatalf("inserting legacy shape %s: %v", name, err)
		}
	}
	if _, err := st.DB().ExecContext(ctx, `DELETE FROM registry.schema_migrations WHERE version = 8`); err != nil {
		t.Fatalf("forgetting migration 8: %v", err)
	}
	if err := Migrate(ctx, st.DB()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	for name, mt := range want {
		row, err := st.GetShape(ctx, name)
		if err != nil {
			t.Fatalf("GetShape(%s): %v", name, err)
		}
		if row.MachineType != mt {
			t.Errorf("shape %s machine_type = %q, want %q", name, row.MachineType, mt)
		}
	}
}

func TestEntryLifecycleQuotasAndTombstoneReservation(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 5, 21, 0, 0, 0, time.UTC)
	st.SetNow(func() time.Time { return now })

	if err := st.SeedShapes(ctx, []ShapeRow{
		{Name: "small", MachineType: "n2-standard-4", BootDiskGB: 50, QuotaUnits: 1},
		{Name: "large", MachineType: "n2-standard-8", BootDiskGB: 100, QuotaUnits: 2},
	}); err != nil {
		t.Fatalf("SeedShapes: %v", err)
	}
	if err := st.SeedSQLInstances(ctx, []SQLInstanceRow{
		{Name: "sql-pool-01", ConnectionName: "proj:us-central1:sql-pool-01", PrivateIP: "10.8.0.5", MaxDatabases: 50},
	}); err != nil {
		t.Fatalf("SeedSQLInstances: %v", err)
	}

	// Default instance quota is 2 units: one "large" (2 units) succeeds, a second "small" (1 unit) fails.
	e1, err := st.CreateEntry(ctx, CreateEntryInput{
		Name:       "alice-vm",
		Kind:       api.KindInstance,
		OwnerEmail: "alice@example.com",
		Shape:      "large",
		Zone:       "us-central1-a",
	})
	if err != nil {
		t.Fatalf("CreateEntry alice-vm: %v", err)
	}
	if e1.LifecycleState != api.StateCreating || st.DeriveStatus(e1) != api.StatusOffline {
		t.Fatalf("expected creating/offline, got %s/%s", e1.LifecycleState, st.DeriveStatus(e1))
	}

	_, err = st.CreateEntry(ctx, CreateEntryInput{
		Name:       "alice-vm-2",
		Kind:       api.KindInstance,
		OwnerEmail: "alice@example.com",
		Shape:      "small",
		Zone:       "us-central1-a",
	})
	if !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("expected ErrQuotaExceeded on 3rd unit, got %v", err)
	}

	// Update placement with plain HTTP backend URL and transition to running.
	e1, err = st.UpdateInstancePlacement(ctx, e1.ID, e1.Version, "gce-123", "10.8.0.14", "http://10.8.0.14:8080", "grain-sa-alice@proj.iam.gserviceaccount.com", "sql-pool-01", "grain_alice", "grain-inst-alice-")
	if err != nil {
		t.Fatalf("UpdateInstancePlacement: %v", err)
	}
	e1, err = st.TransitionEntryState(ctx, e1.ID, e1.Version, api.StateRunning, "")
	if err != nil {
		t.Fatalf("TransitionEntryState running: %v", err)
	}

	// Stale version must fail with ErrConflict.
	if _, err := st.TransitionEntryState(ctx, e1.ID, e1.Version-1, api.StateDeleting, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict on stale version, got %v", err)
	}

	// Heartbeat brings status to online, then degraded after 50s, then offline after 100s.
	e1, err = st.RecordHeartbeat(ctx, e1.ID, api.HeartbeatRequest{
		GrainVersion: "v0.9.0",
		ActiveTasks:  3,
		QueueDepth:   1,
	})
	if err != nil {
		t.Fatalf("RecordHeartbeat: %v", err)
	}
	if got := st.DeriveStatus(e1); got != api.StatusOnline {
		t.Fatalf("expected online immediately after heartbeat, got %s", got)
	}
	now = now.Add(50 * time.Second)
	if got := st.DeriveStatus(e1); got != api.StatusDegraded {
		t.Fatalf("expected degraded at +50s, got %s", got)
	}
	now = now.Add(50 * time.Second)
	if got := st.DeriveStatus(e1); got != api.StatusOffline {
		t.Fatalf("expected offline at +100s, got %s", got)
	}

	// Soft-delete reserves the name for 30 days against other owners, but allows same owner.
	if _, err := st.SoftDeleteEntry(ctx, e1.ID, e1.Version); err != nil {
		t.Fatalf("SoftDeleteEntry: %v", err)
	}

	availBob, err := st.CheckNameAvailability(ctx, "alice-vm", "bob@example.com")
	if err != nil {
		t.Fatalf("CheckNameAvailability bob: %v", err)
	}
	if availBob.State != "reserved" || availBob.Status != api.NameReserved {
		t.Fatalf("expected reserved for bob, got %+v", availBob)
	}
	if _, err := st.CreateEntry(ctx, CreateEntryInput{
		Name:       "alice-vm",
		Kind:       api.KindConnected,
		OwnerEmail: "bob@example.com",
	}); !errors.Is(err, ErrNameReserved) {
		t.Fatalf("expected ErrNameReserved for bob within 30 days, got %v", err)
	}

	availAlice, err := st.CheckNameAvailability(ctx, "alice-vm", "alice@example.com")
	if err != nil {
		t.Fatalf("CheckNameAvailability alice: %v", err)
	}
	if availAlice.State != "available" || availAlice.Status != api.NameFree {
		t.Fatalf("expected available for original owner alice, got %+v", availAlice)
	}

	// Advance past 30-day reservation window: bob can now claim "alice-vm".
	now = now.Add(31 * 24 * time.Hour)
	availBobAfter, err := st.CheckNameAvailability(ctx, "alice-vm", "bob@example.com")
	if err != nil {
		t.Fatalf("CheckNameAvailability bob after 31d: %v", err)
	}
	if availBobAfter.State != "available" || availBobAfter.Status != api.NameFree {
		t.Fatalf("expected available after 31d, got %+v", availBobAfter)
	}
}

func TestScopesPrimaryOverlapAndRouting(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	eA, err := st.CreateEntry(ctx, CreateEntryInput{
		Name:       "team-payments",
		Kind:       api.KindConnected,
		OwnerEmail: "alice@example.com",
		Visibility: VisibilityPublic,
		DeclaredScopes: []ScopeClaim{
			{Repo: "acme/monorepo", PathPrefix: "services/payments", Primary: true},
		},
	})
	if err != nil {
		t.Fatalf("CreateEntry team-payments: %v", err)
	}

	// Non-overlapping primary claim in the same repo succeeds.
	eB, err := st.CreateEntry(ctx, CreateEntryInput{
		Name:       "team-billing",
		Kind:       api.KindConnected,
		OwnerEmail: "bob@example.com",
		Visibility: VisibilityPublic,
		DeclaredScopes: []ScopeClaim{
			{Repo: "acme/monorepo", PathPrefix: "services/billing", Primary: true},
		},
	})
	if err != nil {
		t.Fatalf("CreateEntry team-billing: %v", err)
	}

	// Overlapping primary claim (subpath "services/payments/api" or repo-wide "") is rejected.
	_, err = st.CreateEntry(ctx, CreateEntryInput{
		Name:       "team-overlap",
		Kind:       api.KindConnected,
		OwnerEmail: "carol@example.com",
		Visibility: VisibilityPublic,
		DeclaredScopes: []ScopeClaim{
			{Repo: "acme/monorepo", PathPrefix: "services/payments/api", Primary: true},
		},
	})
	if !errors.Is(err, ErrPrimaryScopeConflict) {
		t.Fatalf("expected ErrPrimaryScopeConflict on overlapping subpath, got %v", err)
	}

	// Non-primary overlapping scope is allowed.
	if err := st.ReplaceEntryScopes(ctx, eB.ID, "reported", []ScopeClaim{
		{Repo: "acme/monorepo", PathPrefix: "", Primary: false},
	}); err != nil {
		t.Fatalf("ReplaceEntryScopes non-primary repo-wide: %v", err)
	}

	// Longest-prefix route resolution picks team-payments as primary for services/payments/checkout.go.
	route, err := st.RouteRepoPath(ctx, "acme/monorepo", "services/payments/checkout.go", "instances.example.com", nil)
	if err != nil {
		t.Fatalf("RouteRepoPath: %v", err)
	}
	if route.Primary == nil || route.Primary.Name != eA.Name {
		t.Fatalf("expected primary route match %q, got %+v", eA.Name, route.Primary)
	}
	if len(route.Candidates) != 2 {
		t.Fatalf("expected 2 candidates (longest prefix + repo-wide secondary), got %d", len(route.Candidates))
	}
}

func TestCredentialsFencedTunnelSessionsAndLeases(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 5, 21, 0, 0, 0, time.UTC)
	st.SetNow(func() time.Time { return now })

	entry, err := st.CreateEntry(ctx, CreateEntryInput{
		Name:       "laptop-connected",
		Kind:       api.KindConnected,
		OwnerEmail: "alice@example.com",
	})
	if err != nil {
		t.Fatalf("CreateEntry: %v", err)
	}

	tok, _, err := st.IssueCredential(ctx, entry.ID)
	if err != nil {
		t.Fatalf("IssueCredential: %v", err)
	}
	if !strings.HasPrefix(tok, "grc_") {
		t.Fatalf("expected grc_ prefix, got %q", tok)
	}
	authed, err := st.AuthenticateCredential(ctx, tok)
	if err != nil || authed.ID != entry.ID {
		t.Fatalf("AuthenticateCredential failed: entry=%+v err=%v", authed, err)
	}
	if _, err := st.AuthenticateCredential(ctx, "grc_invalid"); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("expected ErrInvalidCredential, got %v", err)
	}

	// Register two replicas and test session_epoch fencing.
	if err := st.UpsertReplica(ctx, "rep-1", "10.0.0.1:8082"); err != nil {
		t.Fatalf("UpsertReplica rep-1: %v", err)
	}
	if err := st.UpsertReplica(ctx, "rep-2", "10.0.0.2:8082"); err != nil {
		t.Fatalf("UpsertReplica rep-2: %v", err)
	}

	s1, err := st.AcquireTunnelSession(ctx, entry.ID, "rep-1", "203.0.113.10:51111")
	if err != nil {
		t.Fatalf("AcquireTunnelSession 1: %v", err)
	}
	if s1.SessionEpoch != 1 || s1.ReplicaID != "rep-1" {
		t.Fatalf("unexpected session 1: %+v", s1)
	}

	// Reconnect on rep-2 bumps session_epoch to 2.
	s2, err := st.AcquireTunnelSession(ctx, entry.ID, "rep-2", "203.0.113.10:52222")
	if err != nil {
		t.Fatalf("AcquireTunnelSession 2: %v", err)
	}
	if s2.SessionEpoch != 2 || s2.ReplicaID != "rep-2" {
		t.Fatalf("unexpected session 2: %+v", s2)
	}

	// Stale disconnect from epoch 1 must NOT delete epoch 2's tunnel session.
	released, err := st.ReleaseTunnelSession(ctx, entry.ID, s1.SessionEpoch)
	if err != nil {
		t.Fatalf("ReleaseTunnelSession stale: %v", err)
	}
	if released {
		t.Fatalf("stale epoch 1 release should not have deleted active session")
	}

	reloaded, err := st.GetEntryByID(ctx, entry.ID)
	if err != nil {
		t.Fatalf("GetEntryByID: %v", err)
	}
	if st.DeriveStatus(reloaded) != api.StatusOnline {
		t.Fatalf("expected connected entry with live tunnel to be online, got %s", st.DeriveStatus(reloaded))
	}

	// Leases: rep-1 acquires lease, rep-2 cannot steal until TTL expires.
	ok, err := st.AcquireOrRenewLease(ctx, "reconciler", "rep-1", 30*time.Second)
	if err != nil || !ok {
		t.Fatalf("rep-1 AcquireOrRenewLease: ok=%v err=%v", ok, err)
	}
	ok, err = st.AcquireOrRenewLease(ctx, "reconciler", "rep-2", 30*time.Second)
	if err != nil || ok {
		t.Fatalf("rep-2 should not acquire active lease held by rep-1: ok=%v err=%v", ok, err)
	}
	now = now.Add(31 * time.Second)
	ok, err = st.AcquireOrRenewLease(ctx, "reconciler", "rep-2", 30*time.Second)
	if err != nil || !ok {
		t.Fatalf("rep-2 should acquire expired lease: ok=%v err=%v", ok, err)
	}

	// Audit log round-trip.
	if err := st.RecordAudit(ctx, "alice@example.com", "entry.create", entry.ID, entry.Name, map[string]any{"kind": "connected"}); err != nil {
		t.Fatalf("RecordAudit: %v", err)
	}
	audits, err := st.ListAuditLog(ctx, 10)
	if err != nil || len(audits) != 1 || audits[0].Action != "entry.create" {
		t.Fatalf("unexpected audit log: %+v (err=%v)", audits, err)
	}
}

func TestStep12RandomNameScopesCombinationSyncAndAsyncConflictsAndPromotion(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	st.SetNow(func() time.Time { return now })

	if err := st.SeedShapes(ctx, []ShapeRow{
		{Name: "small", MachineType: "n2-standard-4", BootDiskGB: 50, QuotaUnits: 1},
		{Name: "medium", MachineType: "n2-standard-8", BootDiskGB: 50, QuotaUnits: 2},
	}); err != nil {
		t.Fatalf("SeedShapes: %v", err)
	}

	// 1. Random name generation when Name is omitted.
	listedFalse := false
	randEntry, err := st.CreateEntry(ctx, CreateEntryInput{
		Kind:       api.KindInstance,
		OwnerEmail: "alice@example.com",
		Shape:      "small",
		Listed:     &listedFalse,
	})
	if err != nil {
		t.Fatalf("CreateEntry with random name: %v", err)
	}
	if err := api.ValidateEntryName(randEntry.Name); err != nil {
		t.Fatalf("random name %q failed ValidateEntryName: %v", randEntry.Name, err)
	}

	// 2. Declared + reported scope combination rules on a connected entry.
	listedTrue := true
	combEntry, err := st.CreateEntry(ctx, CreateEntryInput{
		Name:       "combo-grain",
		Kind:       api.KindConnected,
		OwnerEmail: "alice@example.com",
		Listed:     &listedTrue,
		DeclaredScopes: []ScopeClaim{
			{Repo: "acme/monorepo", PathPrefix: "pkg/autoscaler", Primary: true},
			{Repo: "acme/unconfigured", PathPrefix: "", Primary: true},
		},
	})
	if err != nil {
		t.Fatalf("CreateEntry combo-grain: %v", err)
	}

	// Before first heartbeat (`has_reported_scopes = false`), declared scopes are effective.
	initialScopes := CombinedAPIScopes(combEntry.Scopes)
	if len(initialScopes) != 2 || !initialScopes[0].Active || !initialScopes[1].Active {
		t.Fatalf("expected both declared scopes active before first heartbeat, got %+v", initialScopes)
	}

	// First heartbeat reports:
	// - acme/monorepo "" as secondary (declared narrows to "pkg/autoscaler" and wants primary, but effective_role is capped at secondary!)
	// - acme/core "" as primary (no declared override -> reported is effective verbatim)
	// - acme/unconfigured is NOT reported -> declared row becomes inactive with not_reported_by_instance
	combAfterHB, err := st.RecordHeartbeat(ctx, combEntry.ID, api.HeartbeatRequest{
		GrainVersion: "v1.4.0",
		GrainCommit:  "abc1234",
		HostKind:     api.HostKindWorkstation,
		Health:       api.HealthOK,
		ActiveTasks:  2,
		Scopes: []api.HeartbeatScope{
			{Repo: "acme/monorepo", PathPrefix: "", Role: api.RoleSecondary},
			{Repo: "acme/core", PathPrefix: "", Role: api.RolePrimary},
		},
	})
	if err != nil {
		t.Fatalf("RecordHeartbeat combo-grain: %v", err)
	}
	if !combAfterHB.HasReportedScopes {
		t.Fatalf("expected HasReportedScopes=true after first heartbeat")
	}

	byKey := make(map[string]api.Scope)
	for _, sc := range CombinedAPIScopes(combAfterHB.Scopes) {
		byKey[string(sc.Source)+":"+sc.Repo+":"+sc.PathPrefix] = sc
	}
	// Reported acme/monorepo "" is overridden by narrower declared "pkg/autoscaler".
	repMono := byKey["reported:acme/monorepo:"]
	if repMono.Active || repMono.InactiveReason != api.InactiveOverriddenByNarrower {
		t.Fatalf("expected reported acme/monorepo to be overridden_by_narrower, got %+v", repMono)
	}
	// Declared acme/monorepo "pkg/autoscaler" is active, but effective_role is capped at secondary (reported role).
	decMono := byKey["declared:acme/monorepo:pkg/autoscaler"]
	if !decMono.Active || decMono.EffectiveRole != api.RoleSecondary {
		t.Fatalf("expected declared acme/monorepo:pkg/autoscaler active with EffectiveRole=secondary, got %+v", decMono)
	}
	// Reported acme/core "" is active with EffectiveRole=primary.
	repCore := byKey["reported:acme/core:"]
	if !repCore.Active || repCore.EffectiveRole != api.RolePrimary {
		t.Fatalf("expected reported acme/core active with EffectiveRole=primary, got %+v", repCore)
	}
	// Declared acme/unconfigured "" is inactive with not_reported_by_instance.
	decUnconf := byKey["declared:acme/unconfigured:"]
	if decUnconf.Active || decUnconf.InactiveReason != api.InactiveNotReportedByInstance {
		t.Fatalf("expected declared acme/unconfigured inactive with not_reported_by_instance, got %+v", decUnconf)
	}

	// 3. HOT-friendly heartbeat check: identical heartbeat does not bump CatalogGeneration.
	genBefore := st.CatalogGeneration()
	now = now.Add(15 * time.Second)
	combAfterHB2, err := st.RecordHeartbeat(ctx, combEntry.ID, api.HeartbeatRequest{
		GrainVersion: "v1.4.0",
		GrainCommit:  "abc1234",
		HostKind:     api.HostKindWorkstation,
		Health:       api.HealthOK,
		ActiveTasks:  5, // task count changed, but scopes/version/health/status-bucket did not
		Scopes: []api.HeartbeatScope{
			{Repo: "acme/monorepo", PathPrefix: "", Role: api.RoleSecondary},
			{Repo: "acme/core", PathPrefix: "", Role: api.RolePrimary},
		},
	})
	if err != nil {
		t.Fatalf("RecordHeartbeat 2: %v", err)
	}
	if combAfterHB2.ActiveTasks != 5 {
		t.Fatalf("expected ActiveTasks=5, got %d", combAfterHB2.ActiveTasks)
	}
	if st.CatalogGeneration() != genBefore {
		t.Fatalf("expected identical-scope/version/health heartbeat not to bump CatalogGeneration (%d -> %d)", genBefore, st.CatalogGeneration())
	}

	// 4. Synchronous 409 PrimaryScopeConflictError with holder details on listed entry creation,
	// flipping listed: false -> true, and SetDeclaredScopes.
	_, err = st.CreateEntry(ctx, CreateEntryInput{
		Name:       "sync-collider",
		Kind:       api.KindConnected,
		OwnerEmail: "bob@example.com",
		Listed:     &listedTrue,
		DeclaredScopes: []ScopeClaim{
			{Repo: "acme/core", PathPrefix: "sub/dir", Primary: true},
		},
	})
	var pce *PrimaryScopeConflictError
	if !errors.As(err, &pce) {
		t.Fatalf("expected PrimaryScopeConflictError on sync create, got %v", err)
	}
	if pce.Conflict.ConflictingEntry != "combo-grain" || pce.Conflict.ConflictingOwner != "alice@example.com" || pce.Conflict.ConflictingPrefix != "" {
		t.Fatalf("unexpected conflict details: %+v", pce.Conflict)
	}

	// Unlisted entry with overlapping primary scope succeeds.
	unlistedOverlap, err := st.CreateEntry(ctx, CreateEntryInput{
		Name:       "unlisted-collider",
		Kind:       api.KindConnected,
		OwnerEmail: "bob@example.com",
		Listed:     &listedFalse,
		DeclaredScopes: []ScopeClaim{
			{Repo: "acme/core", PathPrefix: "sub/dir", Primary: true},
		},
	})
	if err != nil {
		t.Fatalf("unlisted entry with overlapping primary scope should succeed: %v", err)
	}
	// Flipping listed: false -> true on unlisted-collider fails with PrimaryScopeConflictError.
	_, err = st.UpdateEntryCatalog(ctx, unlistedOverlap.ID, unlistedOverlap.Version, UpdateEntryCatalogInput{
		Listed: &listedTrue,
	})
	if !errors.As(err, &pce) || pce.Conflict.ConflictingEntry != "combo-grain" {
		t.Fatalf("expected PrimaryScopeConflictError when flipping listed false->true, got %v", err)
	}

	// 5. Async heartbeat primary-prefix conflict (`claimed_at` first-registered wins) & automatic contender promotion.
	now = now.Add(10 * time.Second)
	contender1, err := st.CreateEntry(ctx, CreateEntryInput{
		Name:       "contender-auth",
		Kind:       api.KindConnected,
		OwnerEmail: "bob@example.com",
		Listed:     &listedTrue,
	})
	if err != nil {
		t.Fatalf("CreateEntry contender-auth: %v", err)
	}
	now = now.Add(5 * time.Second)
	c1Updated, err := st.RecordHeartbeat(ctx, contender1.ID, api.HeartbeatRequest{
		GrainVersion: "v1.4.0",
		Health:       api.HealthOK,
		Scopes: []api.HeartbeatScope{
			{Repo: "acme/core", PathPrefix: "services/auth", Role: api.RolePrimary},
		},
	})
	if err != nil {
		t.Fatalf("RecordHeartbeat contender-auth must succeed even on async primary conflict: %v", err)
	}
	c1Conflicts := st.EntryScopeConflicts(ctx, c1Updated)
	if len(c1Conflicts) != 1 || c1Conflicts[0].ConflictingEntry != "combo-grain" {
		t.Fatalf("expected contender-auth to record conflict with combo-grain, got %+v", c1Conflicts)
	}

	now = now.Add(5 * time.Second)
	contender2, err := st.CreateEntry(ctx, CreateEntryInput{
		Name:       "contender-billing",
		Kind:       api.KindConnected,
		OwnerEmail: "carol@example.com",
		Listed:     &listedTrue,
	})
	if err != nil {
		t.Fatalf("CreateEntry contender-billing: %v", err)
	}
	c2Updated, err := st.RecordHeartbeat(ctx, contender2.ID, api.HeartbeatRequest{
		GrainVersion: "v1.4.0",
		Health:       api.HealthOK,
		Scopes: []api.HeartbeatScope{
			{Repo: "acme/core", PathPrefix: "services/billing", Role: api.RolePrimary},
		},
	})
	if err != nil {
		t.Fatalf("RecordHeartbeat contender-billing: %v", err)
	}
	if len(st.EntryScopeConflicts(ctx, c2Updated)) != 1 {
		t.Fatalf("expected contender-billing to conflict with combo-grain")
	}

	// Now unlist incumbent `combo-grain`: both disjoint contenders (`services/auth` and `services/billing`)
	// must be promoted to `active = true` in `claimed_at` order!
	curComb, err := st.GetEntryByID(ctx, combEntry.ID)
	if err != nil {
		t.Fatalf("GetEntryByID combo-grain: %v", err)
	}
	if _, err := st.UpdateEntryCatalog(ctx, combEntry.ID, curComb.Version, UpdateEntryCatalogInput{
		Listed: &listedFalse,
	}); err != nil {
		t.Fatalf("unlist combo-grain: %v", err)
	}

	c1Promoted, err := st.GetEntryByID(ctx, contender1.ID)
	if err != nil {
		t.Fatalf("GetEntryByID contender1: %v", err)
	}
	if len(st.EntryScopeConflicts(ctx, c1Promoted)) != 0 || len(CombinedAPIScopes(c1Promoted.Scopes)) != 1 || !CombinedAPIScopes(c1Promoted.Scopes)[0].Active {
		t.Fatalf("expected contender-auth to be promoted to active after incumbent unlisted, got %+v", c1Promoted.Scopes)
	}
	c2Promoted, err := st.GetEntryByID(ctx, contender2.ID)
	if err != nil {
		t.Fatalf("GetEntryByID contender2: %v", err)
	}
	if len(st.EntryScopeConflicts(ctx, c2Promoted)) != 0 || len(CombinedAPIScopes(c2Promoted.Scopes)) != 1 || !CombinedAPIScopes(c2Promoted.Scopes)[0].Active {
		t.Fatalf("expected disjoint contender-billing to be promoted to active after incumbent unlisted, got %+v", c2Promoted.Scopes)
	}

	// 6. Credential rotation with overlap_seconds and immediate revocation.
	tok1, meta1, err := st.IssueCredentialWithMeta(ctx, contender1.ID, "initial", "bob@example.com")
	if err != nil {
		t.Fatalf("IssueCredentialWithMeta: %v", err)
	}
	_ = st.UpsertReplica(ctx, "rep-1", "10.0.0.1:8082")
	if _, err := st.AcquireTunnelSessionWithCred(ctx, contender1.ID, "rep-1", "198.51.100.10:4444", meta1.CredentialID); err != nil {
		t.Fatalf("AcquireTunnelSessionWithCred: %v", err)
	}

	// Rotate with 60s overlap: tok1 remains valid during overlap window, then expires.
	tok2, meta2, err := st.RotateCredential(ctx, contender1.ID, api.RotateCredentialRequest{
		Label:          "rotated-overlap",
		OverlapSeconds: 60,
	}, "bob@example.com")
	if err != nil {
		t.Fatalf("RotateCredential overlap: %v", err)
	}
	if _, _, err := st.AuthenticateCredentialWithID(ctx, tok1); err != nil {
		t.Fatalf("expected tok1 to remain valid during 60s overlap window: %v", err)
	}
	if _, _, err := st.AuthenticateCredentialWithID(ctx, tok2); err != nil {
		t.Fatalf("expected tok2 to be valid: %v", err)
	}
	now = now.Add(61 * time.Second)
	if _, _, err := st.AuthenticateCredentialWithID(ctx, tok1); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("expected tok1 to expire after 60s overlap window, got %v", err)
	}

	// Rotate with immediate RevokePrevious on tok2's tunnel session: active tunnel session is removed immediately.
	if _, err := st.AcquireTunnelSessionWithCred(ctx, contender1.ID, "rep-1", "198.51.100.10:4445", meta2.CredentialID); err != nil {
		t.Fatalf("AcquireTunnelSessionWithCred meta2: %v", err)
	}
	_, _, err = st.RotateCredential(ctx, contender1.ID, api.RotateCredentialRequest{
		Label:          "rotated-immediate",
		RevokePrevious: true,
	}, "bob@example.com")
	if err != nil {
		t.Fatalf("RotateCredential RevokePrevious: %v", err)
	}
	if _, err := st.GetActiveTunnelSession(ctx, contender1.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected active tunnel session for revoked cred to be deleted immediately, got %v", err)
	}

	// 12. Delegating apps, consent grants, usage, and audit_log delegation fields.
	app, err := st.CreateDelegatingApp(ctx, DelegatingAppRow{
		Name:        "planning-app",
		Principal:   "serviceAccount:planning-app@proj.iam.gserviceaccount.com",
		DisplayName: "Planning App",
		Owner:       "alice@example.com",
		MaxAccess:   "write",
		MaxGrantTTL: 30 * 24 * time.Hour,
		CreatedBy:   "admin@example.com",
	})
	if err != nil {
		t.Fatalf("CreateDelegatingApp: %v", err)
	}
	if app.Name != "planning-app" || app.Disabled() {
		t.Fatalf("unexpected delegating app: %+v", app)
	}
	byPrincipal, err := st.GetDelegatingAppByPrincipal(ctx, "planning-app@proj.iam.gserviceaccount.com")
	if err != nil || byPrincipal.Name != "planning-app" {
		t.Fatalf("GetDelegatingAppByPrincipal: %v %+v", err, byPrincipal)
	}
	grant, err := st.UpsertDelegationGrant(ctx, "planning-app", "alice@example.com", []string{"*"}, "write", 24*time.Hour)
	if err != nil {
		t.Fatalf("UpsertDelegationGrant: %v", err)
	}
	if !strings.HasPrefix(grant.ID, "dlg_") || !grant.CoversEntry("any-entry") {
		t.Fatalf("unexpected grant: %+v", grant)
	}
	// Replacing keeps the same grant ID.
	grantReplaced, err := st.UpsertDelegationGrant(ctx, "planning-app", "alice@example.com", []string{"contender-one"}, "read", 24*time.Hour)
	if err != nil {
		t.Fatalf("UpsertDelegationGrant replace: %v", err)
	}
	if grantReplaced.ID != grant.ID || grantReplaced.Access != "read" || !grantReplaced.CoversEntry("contender-one") || grantReplaced.CoversEntry("other") {
		t.Fatalf("unexpected replaced grant: %+v", grantReplaced)
	}
	if err := st.RecordDelegationUsage(ctx, grant.ID, "contender-one", true); err != nil {
		t.Fatalf("RecordDelegationUsage: %v", err)
	}
	if err := st.RecordAuditWithDelegation(ctx, "alice@example.com", "planning-app@proj.iam.gserviceaccount.com", grant.ID, "delegated_write", contender1.ID, contender1.Name, map[string]any{"method": "POST"}); err != nil {
		t.Fatalf("RecordAuditWithDelegation: %v", err)
	}
	if err := st.RevokeDelegationGrant(ctx, grant.ID, "alice@example.com"); err != nil {
		t.Fatalf("RevokeDelegationGrant: %v", err)
	}
	if _, err := st.GetLiveDelegationGrant(ctx, "planning-app", "alice@example.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after RevokeDelegationGrant, got %v", err)
	}

	// A bare-email principal is stored in serviceAccount: form, and a second
	// app with the same principal (in either form) or name is ErrConflict.
	bare, err := st.CreateDelegatingApp(ctx, DelegatingAppRow{
		Name: "triage-app", Principal: " Triage-App@proj.iam.gserviceaccount.com ",
		DisplayName: "Triage", Owner: "alice@example.com", CreatedBy: "admin@example.com",
	})
	if err != nil || bare.Principal != "serviceAccount:triage-app@proj.iam.gserviceaccount.com" {
		t.Fatalf("CreateDelegatingApp bare principal: %+v (err=%v)", bare, err)
	}
	for _, dup := range []DelegatingAppRow{
		{Name: "triage-two", Principal: "serviceAccount:triage-app@proj.iam.gserviceaccount.com"},
		{Name: "triage-app", Principal: "other@proj.iam.gserviceaccount.com"},
	} {
		dup.Owner, dup.DisplayName, dup.CreatedBy = "alice@example.com", "dup", "admin@example.com"
		if _, err := st.CreateDelegatingApp(ctx, dup); !errors.Is(err, ErrConflict) {
			t.Fatalf("CreateDelegatingApp duplicate %+v: err=%v, want ErrConflict", dup, err)
		}
	}

	// ListDelegationGrantsForPerson returns only that person's grants, and
	// revoked ones only when asked.
	if _, err := st.UpsertDelegationGrant(ctx, "triage-app", "Alice@Example.com", []string{"*"}, "read", time.Hour); err != nil {
		t.Fatalf("UpsertDelegationGrant triage-app: %v", err)
	}
	if _, err := st.UpsertDelegationGrant(ctx, "triage-app", "bob@example.com", []string{"*"}, "read", time.Hour); err != nil {
		t.Fatalf("UpsertDelegationGrant bob: %v", err)
	}
	liveGrants, err := st.ListDelegationGrantsForPerson(ctx, "alice@example.com", false)
	if err != nil || len(liveGrants) != 1 || liveGrants[0].App != "triage-app" || liveGrants[0].Person != "alice@example.com" {
		t.Fatalf("ListDelegationGrantsForPerson live: %+v (err=%v)", liveGrants, err)
	}
	allGrants, err := st.ListDelegationGrantsForPerson(ctx, "ALICE@example.com", true)
	if err != nil || len(allGrants) != 2 {
		t.Fatalf("ListDelegationGrantsForPerson include revoked: %+v (err=%v)", allGrants, err)
	}
	for _, g := range allGrants {
		if g.Person != "alice@example.com" {
			t.Fatalf("alice's grant list includes %s's grant %+v", g.Person, g)
		}
	}
}

func TestNormalizeAppPrincipal(t *testing.T) {
	for in, want := range map[string]string{
		"":                                       "",
		"   ":                                    "",
		"serviceAccount:":                        "",
		"planner@proj.iam.gserviceaccount.com":   "serviceAccount:planner@proj.iam.gserviceaccount.com",
		" Planner@Proj.iam.gserviceaccount.com ": "serviceAccount:planner@proj.iam.gserviceaccount.com",
		"serviceAccount:planner@proj.iam.gserviceaccount.com": "serviceAccount:planner@proj.iam.gserviceaccount.com",
		"SERVICEACCOUNT:Planner@proj.iam.gserviceaccount.com": "serviceAccount:planner@proj.iam.gserviceaccount.com",
	} {
		if got := NormalizeAppPrincipal(in); got != want {
			t.Errorf("NormalizeAppPrincipal(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStep17StopStartAutoWakeAndUpgradeAvailable(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 7, 2, 0, 0, 0, time.UTC)
	st.SetNow(func() time.Time { return now })

	if err := st.SeedShapes(ctx, []ShapeRow{
		{Name: "standard", MachineType: "n2-standard-4", BootDiskGB: 50, QuotaUnits: 1},
	}); err != nil {
		t.Fatalf("SeedShapes: %v", err)
	}
	if err := st.SeedSQLInstances(ctx, []SQLInstanceRow{
		{Name: "sql-pool-01", ConnectionName: "proj:us-central1:sql-pool-01", PrivateIP: "10.8.0.5", MaxDatabases: 50},
	}); err != nil {
		t.Fatalf("SeedSQLInstances: %v", err)
	}

	e, err := st.CreateEntry(ctx, CreateEntryInput{
		Name:        "wake-vm",
		Kind:        api.KindInstance,
		OwnerEmail:  "alice@example.com",
		Shape:       "standard",
		Zone:        "us-central1-a",
		AutoUpgrade: string(api.AutoUpgradeMinor),
	})
	if err != nil {
		t.Fatalf("CreateEntry wake-vm: %v", err)
	}
	if e.AutoUpgrade != "minor" || !e.AutoWake {
		t.Fatalf("expected auto_upgrade=minor and auto_wake=true, got %q/%v", e.AutoUpgrade, e.AutoWake)
	}

	e, err = st.UpdateInstancePlacement(ctx, e.ID, e.Version, "gce-wake-vm", "10.8.0.21", "http://10.8.0.21:8080", "grain-sa-wake-vm@proj.iam.gserviceaccount.com", "sql-pool-01", "grain_wake_vm", "grain-inst-wake-vm-")
	if err != nil {
		t.Fatalf("UpdateInstancePlacement: %v", err)
	}
	e, err = st.TransitionEntryState(ctx, e.ID, e.Version, api.StateRunning, "")
	if err != nil {
		t.Fatalf("TransitionEntryState running: %v", err)
	}

	// Set upgrade_available notification.
	e, err = st.SetEntryUpgradeAvailable(ctx, e.ID, "v1.4.0")
	if err != nil || e.UpgradeAvailable != "v1.4.0" {
		t.Fatalf("SetEntryUpgradeAvailable = (%+v, %v)", e, err)
	}

	// Stop lifecycle: running -> stopping -> stopped (clears VM placement while keeping externalized state).
	e, err = st.BeginStopEntry(ctx, e.ID, e.Version, "stopping instance")
	if err != nil || e.LifecycleState != api.StateStopping || st.DeriveStatus(e) != api.StatusOffline {
		t.Fatalf("BeginStopEntry = (%+v, %v)", e, err)
	}
	now = now.Add(5 * time.Second)
	e, err = st.CompleteStopEntry(ctx, e.ID, e.Version)
	if err != nil {
		t.Fatalf("CompleteStopEntry: %v", err)
	}
	if e.LifecycleState != api.StateStopped || e.GCEInstanceID != "" || e.BackendURL != "" || e.StoppedAt == nil {
		t.Fatalf("expected stopped with cleared VM placement and StoppedAt set, got %+v", e)
	}
	if e.ServiceAccount == "" || e.SQLInstance == "" || e.DBName == "" || e.SecretPrefix == "" {
		t.Fatalf("expected externalized state to be preserved while stopped, got %+v", e)
	}

	// Auto-wake on visit transitions stopped -> creating once and is idempotent on concurrent second visit.
	woken, didWake, err := st.WakeStoppedEntry(ctx, e.ID, "bob@example.com")
	if err != nil || !didWake || woken.LifecycleState != api.StateCreating || woken.StoppedAt != nil {
		t.Fatalf("WakeStoppedEntry = (%+v, %v, %v), want creating/didWake=true/StoppedAt=nil", woken, didWake, err)
	}
	_, didWakeAgain, err := st.WakeStoppedEntry(ctx, e.ID, "carol@example.com")
	if err != nil || didWakeAgain {
		t.Fatalf("second WakeStoppedEntry = (didWake=%v, err=%v), want didWake=false", didWakeAgain, err)
	}

	// Disable auto_wake via UpdateEntryCatalog and verify WakeStoppedEntry does not wake when auto_wake=false.
	woken, err = st.TransitionEntryState(ctx, woken.ID, woken.Version, api.StateRunning, "")
	if err != nil {
		t.Fatalf("TransitionEntryState running: %v", err)
	}
	autoWakeFalse := false
	autoUpgradeNotify := string(api.AutoUpgradeNotify)
	woken, err = st.UpdateEntryCatalog(ctx, woken.ID, woken.Version, UpdateEntryCatalogInput{
		AutoWake:    &autoWakeFalse,
		AutoUpgrade: &autoUpgradeNotify,
	})
	if err != nil || woken.AutoWake || woken.AutoUpgrade != "notify" {
		t.Fatalf("UpdateEntryCatalog auto_wake=false: (%+v, %v)", woken, err)
	}
	woken, err = st.BeginStopEntry(ctx, woken.ID, woken.Version, "stopping")
	if err != nil {
		t.Fatalf("BeginStopEntry: %v", err)
	}
	woken, err = st.CompleteStopEntry(ctx, woken.ID, woken.Version)
	if err != nil {
		t.Fatalf("CompleteStopEntry: %v", err)
	}
	stillStopped, didWakeDisabled, err := st.WakeStoppedEntry(ctx, woken.ID, "alice@example.com")
	if err != nil || didWakeDisabled || stillStopped.LifecycleState != api.StateStopped {
		t.Fatalf("expected WakeStoppedEntry with auto_wake=false to leave entry stopped, got (%+v, %v, %v)", stillStopped, didWakeDisabled, err)
	}

	// Explicit StartEntry works even when auto_wake=false.
	started, err := st.BeginStartEntry(ctx, stillStopped.ID, stillStopped.Version, "starting instance")
	if err != nil || started.LifecycleState != api.StateCreating {
		t.Fatalf("BeginStartEntry = (%+v, %v), want creating", started, err)
	}
}

func TestStep16TwoStepTransfer7DayExpiryRestoreHardPurgeAndAdminRecovery(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	st.SetNow(func() time.Time { return now })

	if err := st.SeedShapes(ctx, []ShapeRow{
		{Name: "small", MachineType: "n2-standard-4", BootDiskGB: 50, QuotaUnits: 1},
		{Name: "large", MachineType: "n2-standard-8", BootDiskGB: 100, QuotaUnits: 2},
	}); err != nil {
		t.Fatalf("SeedShapes: %v", err)
	}
	if err := st.SeedSQLInstances(ctx, []SQLInstanceRow{
		{Name: "sql-pool-01", ConnectionName: "proj:us-central1:sql-pool-01", PrivateIP: "10.8.0.5", MaxDatabases: 50},
	}); err != nil {
		t.Fatalf("SeedSQLInstances: %v", err)
	}
	if err := st.UpsertReplica(ctx, "replica-a", "10.0.0.1:8082"); err != nil {
		t.Fatalf("UpsertReplica: %v", err)
	}

	// 1. Create a connected entry with owner_group, active credential, and active tunnel.
	connEntry, err := st.CreateEntry(ctx, CreateEntryInput{
		Name:       "platform-bridge",
		Kind:       api.KindConnected,
		OwnerEmail: "alice@example.com",
		OwnerGroup: "platform-team@example.com",
	})
	if err != nil {
		t.Fatalf("CreateEntry platform-bridge: %v", err)
	}
	tok, credMeta, err := st.IssueCredentialWithMeta(ctx, connEntry.ID, "initial", "alice@example.com")
	if err != nil || tok == "" {
		t.Fatalf("IssueCredentialWithMeta: %v", err)
	}
	if _, err := st.AcquireTunnelSessionWithCred(ctx, connEntry.ID, "replica-a", "192.0.2.10:55111", credMeta.CredentialID); err != nil {
		t.Fatalf("AcquireTunnelSessionWithCred: %v", err)
	}

	// 2. Offer transfer to bob@example.com without specifying ownerGroup -> preserves owner_group and sets 7-day expiry.
	offered, err := st.OfferEntryTransfer(ctx, connEntry.ID, connEntry.Version, "bob@example.com", nil)
	if err != nil {
		t.Fatalf("OfferEntryTransfer: %v", err)
	}
	if offered.PendingOwner != "bob@example.com" || offered.OwnerGroup != "platform-team@example.com" {
		t.Fatalf("expected pending_owner=bob and preserved owner_group=platform-team, got %+v", offered)
	}
	wantExp := now.Add(DefaultTransferWindow)
	if offered.TransferExpiresAt == nil || !offered.TransferExpiresAt.Equal(wantExp) {
		t.Fatalf("TransferExpiresAt = %v, want %v", offered.TransferExpiresAt, wantExp)
	}

	// Non-recipient (even an owner_group peer) cannot accept on bob's behalf.
	if _, err := st.AcceptEntryTransfer(ctx, offered.ID, offered.Version, "peer@example.com"); err == nil {
		t.Fatalf("expected non-recipient AcceptEntryTransfer to fail")
	}

	// Advance clock past 7 days -> transfer offer expired.
	now = now.Add(7*24*time.Hour + time.Minute)
	if _, err := st.AcceptEntryTransfer(ctx, offered.ID, offered.Version, "bob@example.com"); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected expired AcceptEntryTransfer to fail with ErrConflict, got %v", err)
	}

	// Cancel transfer offer.
	cancelled, err := st.OfferEntryTransfer(ctx, offered.ID, offered.Version, "", nil)
	if err != nil {
		t.Fatalf("cancel OfferEntryTransfer: %v", err)
	}
	if cancelled.PendingOwner != "" || cancelled.TransferExpiresAt != nil {
		t.Fatalf("expected cancelled transfer to clear pending_owner and transfer_expires_at, got %+v", cancelled)
	}

	// Re-offer transfer to bob@example.com and verify quota enforcement on accept.
	if err := st.SetQuota(ctx, "bob@example.com", 2, 0, "admin@example.com"); err != nil {
		t.Fatalf("SetQuota bob: %v", err)
	}
	reoffered, err := st.OfferEntryTransfer(ctx, cancelled.ID, cancelled.Version, "bob@example.com", nil)
	if err != nil {
		t.Fatalf("re-offer transfer: %v", err)
	}
	if _, err := st.AcceptEntryTransfer(ctx, reoffered.ID, reoffered.Version, "bob@example.com"); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("expected ErrQuotaExceeded when bob has max_connected=0, got %v", err)
	}

	// Reset bob's quota override so bob reverts to defaults and accepts the transfer.
	if err := st.DeleteQuota(ctx, "bob@example.com"); err != nil {
		t.Fatalf("DeleteQuota bob: %v", err)
	}
	accepted, err := st.AcceptEntryTransfer(ctx, reoffered.ID, reoffered.Version, "bob@example.com")
	if err != nil {
		t.Fatalf("AcceptEntryTransfer: %v", err)
	}
	if accepted.OwnerEmail != "bob@example.com" || accepted.PendingOwner != "" || accepted.TransferExpiresAt != nil {
		t.Fatalf("unexpected accepted entry state: %+v", accepted)
	}
	if accepted.Tunnel != nil {
		t.Fatalf("expected active tunnel session to be terminated on ownership transfer")
	}
	if _, err := st.AuthenticateCredential(ctx, tok); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("expected previous grc_ credential to be revoked on ownership transfer, got %v", err)
	}

	// 3. Admin force-transfer (`TransferEntryOwner`) immediately transfers ownership, clears pending_owner, and revokes credentials.
	tok2, _, err := st.IssueCredentialWithMeta(ctx, accepted.ID, "bob-token", "bob@example.com")
	if err != nil {
		t.Fatalf("IssueCredentialWithMeta bob: %v", err)
	}
	forced, err := st.TransferEntryOwner(ctx, accepted.ID, accepted.Version, "carol@example.com", "sre-team@example.com")
	if err != nil {
		t.Fatalf("TransferEntryOwner: %v", err)
	}
	if forced.OwnerEmail != "carol@example.com" || forced.OwnerGroup != "sre-team@example.com" {
		t.Fatalf("unexpected forced entry state: %+v", forced)
	}
	if _, err := st.AuthenticateCredential(ctx, tok2); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("expected previous credential to be revoked on admin force-transfer, got %v", err)
	}

	// 4. Disabling an owner releases tombstone reservations and blocks mutations.
	deletedConn, err := st.SoftDeleteEntry(ctx, forced.ID, forced.Version)
	if err != nil {
		t.Fatalf("SoftDeleteEntry platform-bridge: %v", err)
	}
	availBefore, err := st.CheckNameAvailability(ctx, deletedConn.Name, "dave@example.com")
	if err != nil || availBefore.Status != api.NameReserved {
		t.Fatalf("expected name to be reserved for carol before disable, got %+v (err=%v)", availBefore, err)
	}
	if err := st.SetOwnerDisabled(ctx, "carol@example.com", true); err != nil {
		t.Fatalf("SetOwnerDisabled carol: %v", err)
	}
	availAfter, err := st.CheckNameAvailability(ctx, deletedConn.Name, "dave@example.com")
	if err != nil || availAfter.Status != api.NameFree {
		t.Fatalf("expected disabled owner to hold no reservation, got %+v (err=%v)", availAfter, err)
	}
	if _, err := st.CreateEntry(ctx, CreateEntryInput{
		Name:       "carol-blocked",
		Kind:       api.KindConnected,
		OwnerEmail: "carol@example.com",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected disabled owner CreateEntry to fail with ErrConflict, got %v", err)
	}

	// 5. Hosted instance retention restore and immediate hard purge.
	inst, err := st.CreateEntry(ctx, CreateEntryInput{
		Name:       "restore-vm",
		Kind:       api.KindInstance,
		OwnerEmail: "alice@example.com",
		Shape:      "small",
		Zone:       "us-central1-a",
	})
	if err != nil {
		t.Fatalf("CreateEntry restore-vm: %v", err)
	}
	inst, err = st.UpdateInstancePlacement(ctx, inst.ID, inst.Version, "gce-restore-1", "10.8.0.25", "http://10.8.0.25:8080", "grain-sa-restore@proj.iam.gserviceaccount.com", "sql-pool-01", "grain_restore_vm", "grain-inst-restore-vm-")
	if err != nil {
		t.Fatalf("UpdateInstancePlacement: %v", err)
	}
	inst, err = st.TransitionEntryState(ctx, inst.ID, inst.Version, api.StateRunning, "")
	if err != nil {
		t.Fatalf("TransitionEntryState: %v", err)
	}

	// Soft-delete (retaining DB for restore window).
	inst, err = st.BeginDeleteEntry(ctx, inst.ID, inst.Version, false)
	if err != nil {
		t.Fatalf("BeginDeleteEntry: %v", err)
	}
	inst, err = st.CompleteDeleteEntry(ctx, inst.ID, inst.Version, false)
	if err != nil {
		t.Fatalf("CompleteDeleteEntry: %v", err)
	}
	if inst.DBName != "grain_restore_vm" || inst.SQLInstance != "sql-pool-01" || inst.PurgedAt != nil {
		t.Fatalf("expected retained DB state on soft-deleted instance, got %+v", inst)
	}

	// SQL pool status shows 1 retained database.
	sqlStatuses, err := st.ListSQLInstanceStatuses(ctx)
	if err != nil || len(sqlStatuses) != 1 || sqlStatuses[0].RetainedDatabases != 1 || sqlStatuses[0].UsedDatabases != 1 {
		t.Fatalf("ListSQLInstanceStatuses = (%+v, %v), want 1 retained database", sqlStatuses, err)
	}

	// Restore within db_purge_after transitions back to creating with preserved externalized state.
	restored, err := st.RestoreEntry(ctx, inst.ID, inst.Version)
	if err != nil {
		t.Fatalf("RestoreEntry: %v", err)
	}
	if restored.LifecycleState != api.StateCreating || restored.DeletedAt != nil || restored.DBName != "grain_restore_vm" || restored.SQLInstance != "sql-pool-01" {
		t.Fatalf("unexpected restored instance state: %+v", restored)
	}

	// Delete again and perform immediate hard purge -> subsequent RestoreEntry fails with ErrConflict.
	delAgain, err := st.SoftDeleteEntry(ctx, restored.ID, restored.Version)
	if err != nil {
		t.Fatalf("SoftDeleteEntry restore-vm: %v", err)
	}
	purged, err := st.HardPurgeEntry(ctx, delAgain.ID, true, false)
	if err != nil {
		t.Fatalf("HardPurgeEntry: %v", err)
	}
	if purged.PurgedAt == nil || purged.DBName != "" || purged.SQLInstance != "" {
		t.Fatalf("expected HardPurgeEntry to set PurgedAt and clear DB columns, got %+v", purged)
	}
	if _, err := st.RestoreEntry(ctx, purged.ID, purged.Version); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected RestoreEntry after HardPurgeEntry to fail with ErrConflict, got %v", err)
	}

	// 6. Admin scope conflict listing and resolution (demoting incumbent promotes waiting contender).
	inc, err := st.CreateEntry(ctx, CreateEntryInput{
		Name:       "incumbent-entry",
		Kind:       api.KindConnected,
		OwnerEmail: "alice@example.com",
		DeclaredScopes: []ScopeClaim{
			{Repo: "acme/monorepo", PathPrefix: "services/payments", Primary: true},
		},
	})
	if err != nil {
		t.Fatalf("CreateEntry incumbent-entry: %v", err)
	}
	contender, err := st.CreateEntry(ctx, CreateEntryInput{
		Name:       "contender-entry",
		Kind:       api.KindConnected,
		OwnerEmail: "bob@example.com",
	})
	if err != nil {
		t.Fatalf("CreateEntry contender-entry: %v", err)
	}
	if _, err := st.RecordHeartbeat(ctx, contender.ID, api.HeartbeatRequest{
		GrainVersion: "v1.2.0",
		Health:       api.HealthOK,
		Scopes: []api.HeartbeatScope{
			{Repo: "acme/monorepo", PathPrefix: "services/payments/api", Role: api.RolePrimary},
		},
	}); err != nil {
		t.Fatalf("RecordHeartbeat contender: %v", err)
	}

	conflicts, err := st.ListAllScopeConflicts(ctx)
	if err != nil || len(conflicts) != 1 || conflicts[0].EntryName != "contender-entry" || conflicts[0].ConflictWith != "incumbent-entry" {
		t.Fatalf("ListAllScopeConflicts = (%+v, %v), want 1 conflict with incumbent-entry", conflicts, err)
	}

	// Admin resolves conflict by demoting incumbent-entry to secondary -> contender-entry promotes to active primary!
	if err := st.ResolveScopeConflict(ctx, inc.Name, "acme/monorepo", "services/payments", "demote"); err != nil {
		t.Fatalf("ResolveScopeConflict: %v", err)
	}
	conflictsAfter, err := st.ListAllScopeConflicts(ctx)
	if err != nil || len(conflictsAfter) != 0 {
		t.Fatalf("expected 0 scope conflicts after resolution, got (%+v, %v)", conflictsAfter, err)
	}
	contenderAfter, err := st.GetEntryByID(ctx, contender.ID)
	if err != nil {
		t.Fatalf("GetEntryByID contender: %v", err)
	}
	comb := CombinedAPIScopes(contenderAfter.Scopes)
	if len(comb) != 1 || !comb[0].Active || comb[0].Role != api.RolePrimary {
		t.Fatalf("expected contender scope to promote to active primary after incumbent demotion, got %+v", comb)
	}

	// 7. Reconciler lease status and owner quota summaries.
	if ok, err := st.AcquireOrRenewLease(ctx, "reconciler", "holder-1", 15*time.Second); err != nil || !ok {
		t.Fatalf("AcquireOrRenewLease = (%v, %v)", ok, err)
	}
	leaseSt, err := st.GetLeaseStatus(ctx, "reconciler")
	if err != nil || !leaseSt.Active || leaseSt.HolderID != "holder-1" {
		t.Fatalf("GetLeaseStatus = (%+v, %v)", leaseSt, err)
	}
	owners, err := st.ListOwnerQuotaSummaries(ctx)
	if err != nil || len(owners) < 3 {
		t.Fatalf("ListOwnerQuotaSummaries = (%+v, %v)", owners, err)
	}
}

func TestCustomBootDiskGBAndResize(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if err := st.SeedShapes(ctx, []ShapeRow{
		{Name: "n2-standard-4", MachineType: "n2-standard-4", BootDiskGB: 100, QuotaUnits: 2},
		{Name: "n2-standard-8", MachineType: "n2-standard-8", BootDiskGB: 200, QuotaUnits: 4},
	}); err != nil {
		t.Fatalf("SeedShapes: %v", err)
	}
	if err := st.SetQuota(ctx, "alice@example.com", 16, 10, "admin"); err != nil {
		t.Fatalf("SetQuota: %v", err)
	}

	// Out-of-bounds disk sizes are rejected.
	if _, err := st.CreateEntry(ctx, CreateEntryInput{
		Name:       "bad-disk-vm",
		Kind:       api.KindInstance,
		OwnerEmail: "alice@example.com",
		Shape:      "n2-standard-4",
		BootDiskGB: 10,
	}); err == nil {
		t.Fatal("expected error for boot_disk_gb < 20")
	}

	// Default disk size falls back to shape's BootDiskGB in ToAPIEntry.
	eDefault, err := st.CreateEntry(ctx, CreateEntryInput{
		Name:       "default-disk-vm",
		Kind:       api.KindInstance,
		OwnerEmail: "alice@example.com",
		Shape:      "n2-standard-4",
	})
	if err != nil {
		t.Fatalf("CreateEntry default-disk-vm: %v", err)
	}
	apiDefault := st.ToAPIEntry(eDefault, "grain.example.com")
	if apiDefault.BootDiskGB != 100 || apiDefault.MachineType != "n2-standard-4" {
		t.Fatalf("apiDefault = (disk=%d, mt=%q), want (100, n2-standard-4)", apiDefault.BootDiskGB, apiDefault.MachineType)
	}

	// Custom disk size overrides shape default and survives same-shape version upgrade,
	// then resets to new shape's default when resizing shape without explicit disk override.
	eCustom, err := st.CreateEntry(ctx, CreateEntryInput{
		Name:       "custom-disk-vm",
		Kind:       api.KindInstance,
		OwnerEmail: "alice@example.com",
		Shape:      "n2-standard-4",
		BootDiskGB: 150,
	})
	if err != nil {
		t.Fatalf("CreateEntry custom-disk-vm: %v", err)
	}
	if eCustom.BootDiskGB != 150 {
		t.Fatalf("eCustom.BootDiskGB = %d, want 150", eCustom.BootDiskGB)
	}
	apiCustom := st.ToAPIEntry(eCustom, "grain.example.com")
	if apiCustom.BootDiskGB != 150 {
		t.Fatalf("apiCustom.BootDiskGB = %d, want 150", apiCustom.BootDiskGB)
	}

	eUpgraded, err := st.RequestEntryReplace(ctx, eCustom.ID, eCustom.Version, "n2-standard-4", "v1.3.0")
	if err != nil {
		t.Fatalf("RequestEntryReplace same shape: %v", err)
	}
	if eUpgraded.BootDiskGB != 150 {
		t.Fatalf("expected custom BootDiskGB=150 preserved on same-shape upgrade, got %d", eUpgraded.BootDiskGB)
	}

	eResizedShape, err := st.RequestEntryReplaceWithDisk(ctx, eUpgraded.ID, eUpgraded.Version, "n2-standard-8", "", 0)
	if err != nil {
		t.Fatalf("RequestEntryReplaceWithDisk shape change: %v", err)
	}
	if got := st.ToAPIEntry(eResizedShape, "grain.example.com").BootDiskGB; got != 200 {
		t.Fatalf("expected new shape default BootDiskGB=200 after shape change, got %d", got)
	}

	eResizedDisk, err := st.RequestEntryReplaceWithDisk(ctx, eResizedShape.ID, eResizedShape.Version, "n2-standard-8", "", 350)
	if err != nil {
		t.Fatalf("RequestEntryReplaceWithDisk custom disk: %v", err)
	}
	if eResizedDisk.BootDiskGB != 350 || st.ToAPIEntry(eResizedDisk, "grain.example.com").BootDiskGB != 350 {
		t.Fatalf("expected custom BootDiskGB=350 after disk resize, got %d", eResizedDisk.BootDiskGB)
	}
}
