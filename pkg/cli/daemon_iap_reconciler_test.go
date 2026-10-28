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

// Package main contains daemon tests including dynamic IAP IAM policy reconciliation.
package cli

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/iapreconciler"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"google.golang.org/api/compute/v1"
)

type mockIAPClient struct {
	mu            sync.Mutex
	policy        *compute.Policy
	getErr        error
	setErr        error
	setCalls      int
	lastProject   string
	lastService   string
	lastSetPolicy *compute.Policy
}

func (m *mockIAPClient) GetBackendServiceIAMPolicy(ctx context.Context, projectID, backendService string) (*compute.Policy, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getErr != nil {
		return nil, m.getErr
	}
	if m.policy == nil {
		return &compute.Policy{Etag: "initial-etag"}, nil
	}
	return m.policy, nil
}

func (m *mockIAPClient) SetBackendServiceIAMPolicy(ctx context.Context, projectID, backendService string, policy *compute.Policy) (*compute.Policy, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.setCalls++
	m.lastProject = projectID
	m.lastService = backendService
	m.lastSetPolicy = policy
	if m.setErr != nil {
		return nil, m.setErr
	}
	m.policy = policy
	return policy, nil
}

func TestReconcileIAPNoOpWhenDisabled(t *testing.T) {
	t.Parallel()
	mock := &mockIAPClient{}

	ctx := context.Background()

	// Empty gcpProject
	if err := daemon.ReconcileIAP(ctx, nil, daemon.Config{IapBackendService: "srv", IapClient: mock}); err != nil {
		t.Fatalf("reconcileIAP: %v", err)
	}
	if mock.setCalls != 0 {
		t.Errorf("expected 0 set calls, got %d", mock.setCalls)
	}

	// Empty iapBackendService
	if err := daemon.ReconcileIAP(ctx, nil, daemon.Config{GcpProject: "proj", IapClient: mock}); err != nil {
		t.Fatalf("reconcileIAP: %v", err)
	}
	if mock.setCalls != 0 {
		t.Errorf("expected 0 set calls, got %d", mock.setCalls)
	}
}

// accessorsOf is the unconditional IAP accessor binding's members in p.
func accessorsOf(p *compute.Policy) []string {
	if p == nil {
		return nil
	}
	for _, b := range p.Bindings {
		if b.Role == iapreconciler.RoleIAPHTTPSResourceAccessor && b.Condition == nil {
			return b.Members
		}
	}
	return nil
}

const agentSA = "agent@test-proj.iam.gserviceaccount.com"

// A reconcile adds what grain needs -- the agent's service account --
// and keeps everybody the binding already holds: a single-tenant grain
// keeps no list of the people it serves, so it revokes none of them.
func TestReconcileIAPExecutesPolicyUpdate(t *testing.T) {
	dataDir := t.TempDir()
	store, db, err := daemon.OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	defer db.Close()

	ctx := context.Background()

	mock := &mockIAPClient{
		policy: &compute.Policy{
			Etag: "etag-1",
			Bindings: []*compute.Binding{
				{
					Role:    iapreconciler.RoleIAPHTTPSResourceAccessor,
					Members: []string{"user:old-user@example.com"},
				},
			},
		},
	}

	cfg := model.DefaultConfig()
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}

	dCfg := daemon.Config{
		GcpProject:             "test-proj",
		IapBackendService:      "test-backend-service",
		IapClient:              mock,
		GcpServiceAccountEmail: agentSA,
	}

	if err := daemon.ReconcileIAP(ctx, store, dCfg); err != nil {
		t.Fatalf("reconcileIAP failed: %v", err)
	}

	if mock.setCalls != 1 {
		t.Fatalf("expected 1 SetIamPolicy call, got %d", mock.setCalls)
	}
	if mock.lastProject != "test-proj" || mock.lastService != "test-backend-service" {
		t.Errorf("reconciled target = %s/%s, want test-proj/test-backend-service", mock.lastProject, mock.lastService)
	}
	members := accessorsOf(mock.lastSetPolicy)
	if !slices.Contains(members, "serviceAccount:"+agentSA) {
		t.Errorf("expected the agent's service account in bindings, got %v", members)
	}
	if !slices.Contains(members, "user:old-user@example.com") {
		t.Errorf("old-user should have been kept, got %v", members)
	}

	// Run again: nothing to add, so nothing is written.
	if err := daemon.ReconcileIAP(ctx, store, dCfg); err != nil {
		t.Fatalf("second reconcileIAP: %v", err)
	}
	if mock.setCalls != 1 {
		t.Errorf("a reconcile with nothing to add wrote the policy again: %d calls", mock.setCalls)
	}
}

func TestReconcileWithIAPStartupAndTriggerSync(t *testing.T) {
	dataDir := t.TempDir()
	store, db, err := daemon.OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	defer db.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stored := model.DefaultConfig()
	if err := store.PutConfig(ctx, stored); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}

	mock := &mockIAPClient{}

	cfg := daemon.Config{
		PollInterval:           10 * time.Second,
		GcpProject:             "test-proj",
		IapBackendService:      "test-backend",
		IapClient:              mock,
		GcpServiceAccountEmail: agentSA,
	}

	live := daemon.NewLiveConfig(store, nil, cfg, nil)
	deps := orchestrator.Deps{
		Store: store,
	}

	wakes := make(chan struct{}, 1)

	done := make(chan struct{})
	go func() {
		defer close(done)
		daemon.ReconcileWith(ctx, deps, live, wakes, daemon.IapSyncTriggers)
	}()

	// 1. Startup initial sync should have been executed
	deadline := time.Now().Add(daemonWaitLimit)
	for {
		mock.mu.Lock()
		calls := mock.setCalls
		mock.mu.Unlock()
		if calls >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected startup sync setCalls >= 1, got %d", calls)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// 2. Somebody takes the agent's grant away by hand; triggerIAPSync()
	// puts it back.
	mock.mu.Lock()
	mock.policy = &compute.Policy{Etag: "by-hand"}
	mock.mu.Unlock()
	daemon.TriggerIAPSync()

	deadline = time.Now().Add(daemonWaitLimit)
	for {
		mock.mu.Lock()
		calls := mock.setCalls
		mock.mu.Unlock()
		if calls >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected trigger sync setCalls >= 2, got %d", calls)
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case <-done:
		// Clean exit
	case <-time.After(daemonWaitLimit):
		t.Fatal("reconcileWith did not exit cleanly upon ctx cancellation")
	}
}

// With nothing of grain's own to grant, a reconcile still never empties a
// binding of its people; it only takes away a broad principal.
func TestReconcileIAPNeverRevokesPeople(t *testing.T) {
	t.Parallel()
	mock := &mockIAPClient{policy: &compute.Policy{
		Bindings: []*compute.Binding{{Role: iapreconciler.RoleIAPHTTPSResourceAccessor, Members: []string{"user:admin@example.com"}}},
	}}
	cfg := daemon.Config{GcpProject: "p", IapBackendService: "s", IapClient: mock}
	if err := daemon.ReconcileIAP(context.Background(), nil, cfg); err != nil {
		t.Fatalf("reconcileIAP: %v", err)
	}
	if mock.setCalls != 0 {
		t.Fatalf("setCalls = %d, want 0: nothing to add or take away", mock.setCalls)
	}

	mock.policy = &compute.Policy{
		Bindings: []*compute.Binding{{Role: iapreconciler.RoleIAPHTTPSResourceAccessor, Members: []string{"user:admin@example.com", "allUsers"}}},
	}
	if err := daemon.ReconcileIAP(context.Background(), nil, cfg); err != nil {
		t.Fatalf("reconcileIAP: %v", err)
	}
	if got := accessorsOf(mock.lastSetPolicy); mock.setCalls != 1 || !slices.Equal(got, []string{"user:admin@example.com"}) {
		t.Fatalf("setCalls = %d, accessors = %v; want allUsers removed and admin kept", mock.setCalls, got)
	}
}
