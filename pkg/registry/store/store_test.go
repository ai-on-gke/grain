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
