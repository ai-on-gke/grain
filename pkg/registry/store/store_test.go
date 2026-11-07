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

func TestEntryLifecycleQuotasAndTombstoneReservation(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 5, 21, 0, 0, 0, time.UTC)
	st.SetNow(func() time.Time { return now })

	if err := st.SeedShapes(ctx, []ShapeRow{
		{Name: "small", MachineType: "e2-standard-4", BootDiskGB: 50, QuotaUnits: 1},
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
		{Name: "small", MachineType: "e2-standard-4", BootDiskGB: 50, QuotaUnits: 1},
		{Name: "medium", MachineType: "e2-standard-8", BootDiskGB: 50, QuotaUnits: 2},
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
}

func TestStep17StopStartAutoWakeAndUpgradeAvailable(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 7, 2, 0, 0, 0, time.UTC)
	st.SetNow(func() time.Time { return now })

	if err := st.SeedShapes(ctx, []ShapeRow{
		{Name: "standard", MachineType: "e2-standard-4", BootDiskGB: 50, QuotaUnits: 1},
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
