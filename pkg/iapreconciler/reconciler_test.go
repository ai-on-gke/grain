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

package iapreconciler

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"google.golang.org/api/compute/v1"
	"google.golang.org/api/googleapi"
)

type mockIAMClient struct {
	mu            sync.Mutex
	policies      map[string]*compute.Policy
	getErr        error
	setErrors     []error
	getCalls      int
	setCalls      int
	lastSetPolicy *compute.Policy
}

func newMockIAMClient() *mockIAMClient {
	return &mockIAMClient{
		policies: make(map[string]*compute.Policy),
	}
}

func (m *mockIAMClient) GetBackendServiceIAMPolicy(ctx context.Context, projectID, backendServiceName string) (*compute.Policy, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getCalls++
	if m.getErr != nil {
		return nil, m.getErr
	}
	key := projectID + "/" + backendServiceName
	if p, ok := m.policies[key]; ok {
		// return a clone
		return clonePolicy(p), nil
	}
	return &compute.Policy{Etag: "initial-etag"}, nil
}

func (m *mockIAMClient) SetBackendServiceIAMPolicy(ctx context.Context, projectID, backendServiceName string, policy *compute.Policy) (*compute.Policy, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.setCalls++
	if len(m.setErrors) > 0 {
		err := m.setErrors[0]
		m.setErrors = m.setErrors[1:]
		if err != nil {
			return nil, err
		}
	}
	key := projectID + "/" + backendServiceName
	m.lastSetPolicy = clonePolicy(policy)
	policyCopy := clonePolicy(policy)
	policyCopy.Etag = "updated-etag-" + policy.Etag
	m.policies[key] = policyCopy
	return policyCopy, nil
}

func clonePolicy(p *compute.Policy) *compute.Policy {
	if p == nil {
		return nil
	}
	cp := &compute.Policy{
		Etag:         p.Etag,
		Version:      p.Version,
		AuditConfigs: p.AuditConfigs,
	}
	for _, b := range p.Bindings {
		if b == nil {
			continue
		}
		bCopy := &compute.Binding{
			Role:      b.Role,
			Members:   append([]string(nil), b.Members...),
			Condition: b.Condition,
		}
		cp.Bindings = append(cp.Bindings, bCopy)
	}
	return cp
}

func TestNormalizeMember(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", ""},
		{"   ", ""},
		{"alice@example.com", "user:alice@example.com"},
		{"user:bob@example.com", "user:bob@example.com"},
		{"agent@my-proj.iam.gserviceaccount.com", "serviceAccount:agent@my-proj.iam.gserviceaccount.com"},
		{"serviceAccount:agent@my-proj.iam.gserviceaccount.com", "serviceAccount:agent@my-proj.iam.gserviceaccount.com"},
		{"group:devs@example.com", "group:devs@example.com"},
		{"domain:example.com", "domain:example.com"},
		{"allUsers", "allUsers"},
		{"allAuthenticatedUsers", "allAuthenticatedUsers"},
		{"principal://iam.googleapis.com/projects/123/locations/global/workloadIdentityPools/p/subject/s", "principal://iam.googleapis.com/projects/123/locations/global/workloadIdentityPools/p/subject/s"},
		{"user:  ", ""},
		{"  user:alice@example.com  ", "user:alice@example.com"},
		{"alice", "user:alice"},
	}

	for _, tc := range tests {
		got := NormalizeMember(tc.input)
		if got != tc.want {
			t.Errorf("NormalizeMember(%q) = %q; want %q", tc.input, got, tc.want)
		}
	}
}

func TestNormalizeMembers(t *testing.T) {
	input := []string{
		"  bob@example.com ",
		"user:alice@example.com",
		"bob@example.com",
		"",
		"   ",
		"serviceAccount:agent@proj.iam.gserviceaccount.com",
		"agent@proj.iam.gserviceaccount.com",
	}
	want := []string{
		"serviceAccount:agent@proj.iam.gserviceaccount.com",
		"user:alice@example.com",
		"user:bob@example.com",
	}

	got := NormalizeMembers(input)
	if !slices.Equal(got, want) {
		t.Fatalf("NormalizeMembers(%v) = %v; want %v", input, got, want)
	}
}

func TestApplyDesiredMembers_Additions(t *testing.T) {
	current := &compute.Policy{
		Etag: "etag-1",
		Bindings: []*compute.Binding{
			{
				Role:    RoleIAPHTTPSResourceAccessor,
				Members: []string{"user:alice@example.com"},
			},
		},
	}
	desired := []string{"alice@example.com", "bob@example.com"}

	changed, newPolicy := ApplyDesiredMembers(current, desired)
	if !changed {
		t.Fatal("expected changed to be true")
	}
	if newPolicy.Etag != "etag-1" {
		t.Fatalf("expected Etag preserved, got %q", newPolicy.Etag)
	}
	if len(newPolicy.Bindings) != 1 {
		t.Fatalf("expected 1 binding, got %d", len(newPolicy.Bindings))
	}
	wantMembers := []string{"user:alice@example.com", "user:bob@example.com"}
	if !slices.Equal(newPolicy.Bindings[0].Members, wantMembers) {
		t.Fatalf("members = %v, want %v", newPolicy.Bindings[0].Members, wantMembers)
	}
}

func TestApplyDesiredMembers_Removals(t *testing.T) {
	current := &compute.Policy{
		Etag: "etag-1",
		Bindings: []*compute.Binding{
			{
				Role:    RoleIAPHTTPSResourceAccessor,
				Members: []string{"user:alice@example.com", "user:bob@example.com"},
			},
		},
	}
	desired := []string{"alice@example.com"}

	changed, newPolicy := ApplyDesiredMembers(current, desired)
	if !changed {
		t.Fatal("expected changed to be true")
	}
	wantMembers := []string{"user:alice@example.com"}
	if !slices.Equal(newPolicy.Bindings[0].Members, wantMembers) {
		t.Fatalf("members = %v, want %v", newPolicy.Bindings[0].Members, wantMembers)
	}
}

func TestApplyDesiredMembers_IdempotentNoOp(t *testing.T) {
	current := &compute.Policy{
		Etag: "etag-1",
		Bindings: []*compute.Binding{
			{
				Role:    RoleIAPHTTPSResourceAccessor,
				Members: []string{"user:alice@example.com", "user:bob@example.com"},
			},
		},
	}
	desired := []string{"bob@example.com", "user:alice@example.com"}

	changed, _ := ApplyDesiredMembers(current, desired)
	if changed {
		t.Fatal("expected changed to be false for idempotent policy")
	}
}

func TestApplyDesiredMembers_PreserveOtherRolesAndConditions(t *testing.T) {
	cond := &compute.Expr{Title: "expires_nightly", Expression: "request.time < timestamp('2026-10-01T00:00:00Z')"}
	current := &compute.Policy{
		Etag: "etag-1",
		Bindings: []*compute.Binding{
			{
				Role:    "roles/viewer",
				Members: []string{"user:viewer@example.com"},
			},
			{
				Role:      RoleIAPHTTPSResourceAccessor,
				Members:   []string{"user:temporary@example.com"},
				Condition: cond,
			},
			{
				Role:    RoleIAPHTTPSResourceAccessor,
				Members: []string{"user:old@example.com"},
			},
		},
	}
	desired := []string{"user:new@example.com"}

	changed, newPolicy := ApplyDesiredMembers(current, desired)
	if !changed {
		t.Fatal("expected changed to be true")
	}

	if len(newPolicy.Bindings) != 3 {
		t.Fatalf("expected 3 bindings, got %d", len(newPolicy.Bindings))
	}

	// First binding: roles/viewer
	if newPolicy.Bindings[0].Role != "roles/viewer" || !slices.Equal(newPolicy.Bindings[0].Members, []string{"user:viewer@example.com"}) {
		t.Errorf("unexpected binding 0: %+v", newPolicy.Bindings[0])
	}
	// Second binding: conditional roles/iap.httpsResourceAccessor
	if newPolicy.Bindings[1].Role != RoleIAPHTTPSResourceAccessor || newPolicy.Bindings[1].Condition != cond {
		t.Errorf("unexpected binding 1: %+v", newPolicy.Bindings[1])
	}
	// Third binding: unconditional roles/iap.httpsResourceAccessor
	if newPolicy.Bindings[2].Role != RoleIAPHTTPSResourceAccessor || !slices.Equal(newPolicy.Bindings[2].Members, []string{"user:new@example.com"}) {
		t.Errorf("unexpected binding 2: %+v", newPolicy.Bindings[2])
	}
}

func TestApplyDesiredMembers_EmptyDesiredMembersRemovesBinding(t *testing.T) {
	current := &compute.Policy{
		Etag: "etag-1",
		Bindings: []*compute.Binding{
			{
				Role:    RoleIAPHTTPSResourceAccessor,
				Members: []string{"user:alice@example.com"},
			},
			{
				Role:    "roles/viewer",
				Members: []string{"user:viewer@example.com"},
			},
		},
	}
	desired := []string{}

	changed, newPolicy := ApplyDesiredMembers(current, desired)
	if !changed {
		t.Fatal("expected changed to be true")
	}
	if len(newPolicy.Bindings) != 1 {
		t.Fatalf("expected 1 binding remaining, got %d", len(newPolicy.Bindings))
	}
	if newPolicy.Bindings[0].Role != "roles/viewer" {
		t.Fatalf("expected roles/viewer binding, got %q", newPolicy.Bindings[0].Role)
	}
}

func TestReconcileIAPMembers_FullCycle(t *testing.T) {
	mock := newMockIAMClient()
	ctx := context.Background()

	projectID := "test-project"
	backendService := "ui-backend"

	// 1. Initial reconciliation from empty policy
	desired := []string{"user:alice@example.com", "serviceAccount:agent@test-project.iam.gserviceaccount.com"}
	err := ReconcileIAPMembersWithClient(ctx, mock, projectID, backendService, desired)
	if err != nil {
		t.Fatalf("initial Reconcile failed: %v", err)
	}
	if mock.getCalls != 1 {
		t.Fatalf("getCalls = %d, want 1", mock.getCalls)
	}
	if mock.setCalls != 1 {
		t.Fatalf("setCalls = %d, want 1", mock.setCalls)
	}

	// 2. Idempotent call with same desired members
	err = ReconcileIAPMembersWithClient(ctx, mock, projectID, backendService, desired)
	if err != nil {
		t.Fatalf("idempotent Reconcile failed: %v", err)
	}
	if mock.getCalls != 2 {
		t.Fatalf("getCalls = %d, want 2", mock.getCalls)
	}
	if mock.setCalls != 1 {
		t.Fatalf("setCalls = %d, want 1 (should not have called Set for idempotent run)", mock.setCalls)
	}

	// 3. Update with new member
	desiredWithBob := append(desired, "bob@example.com")
	err = ReconcileIAPMembersWithClient(ctx, mock, projectID, backendService, desiredWithBob)
	if err != nil {
		t.Fatalf("update Reconcile failed: %v", err)
	}
	if mock.setCalls != 2 {
		t.Fatalf("setCalls = %d, want 2", mock.setCalls)
	}

	// Check final state
	p, err := mock.GetBackendServiceIAMPolicy(ctx, projectID, backendService)
	if err != nil {
		t.Fatalf("GetBackendServiceIAMPolicy failed: %v", err)
	}
	wantMembers := []string{
		"serviceAccount:agent@test-project.iam.gserviceaccount.com",
		"user:alice@example.com",
		"user:bob@example.com",
	}
	if len(p.Bindings) != 1 || !slices.Equal(p.Bindings[0].Members, wantMembers) {
		t.Fatalf("unexpected policy bindings: %+v", p.Bindings)
	}
}

func TestReconcileIAPMembers_ConcurrencyConflictRetry(t *testing.T) {
	mock := newMockIAMClient()
	ctx := context.Background()

	projectID := "test-project"
	backendService := "ui-backend"

	// Mock Set errors: first 409 conflict, then 412 precondition failed, then success
	conflictErr := &googleapi.Error{Code: http.StatusConflict, Message: "There were concurrent policy changes"}
	preconditionErr := &googleapi.Error{Code: http.StatusPreconditionFailed, Message: "ETag mismatch"}
	mock.setErrors = []error{conflictErr, preconditionErr, nil}

	var sleepCalls int
	sleepFunc := func(ctx context.Context, d time.Duration) error {
		sleepCalls++
		return nil
	}

	desired := []string{"user:alice@example.com"}
	err := ReconcileIAPMembersWithClient(ctx, mock, projectID, backendService, desired,
		WithSleep(sleepFunc),
		WithMaxRetries(3),
	)
	if err != nil {
		t.Fatalf("expected retry success, got err: %v", err)
	}
	if mock.setCalls != 3 {
		t.Fatalf("setCalls = %d, want 3", mock.setCalls)
	}
	if sleepCalls != 2 {
		t.Fatalf("sleepCalls = %d, want 2", sleepCalls)
	}
}

func TestReconcileIAPMembers_ConcurrencyConflictMaxRetriesExceeded(t *testing.T) {
	mock := newMockIAMClient()
	ctx := context.Background()

	projectID := "test-project"
	backendService := "ui-backend"

	conflictErr := &googleapi.Error{Code: http.StatusConflict, Message: "There were concurrent policy changes"}
	mock.setErrors = []error{conflictErr, conflictErr, conflictErr}

	sleepFunc := func(ctx context.Context, d time.Duration) error {
		return nil
	}

	desired := []string{"user:alice@example.com"}
	err := ReconcileIAPMembersWithClient(ctx, mock, projectID, backendService, desired,
		WithSleep(sleepFunc),
		WithMaxRetries(2),
	)
	if err == nil {
		t.Fatal("expected error after max retries exceeded, got nil")
	}
	if mock.setCalls != 3 { // initial + 2 retries
		t.Fatalf("setCalls = %d, want 3", mock.setCalls)
	}
}

func TestReconcileIAPMembers_NonConflictErrorDoesNotRetry(t *testing.T) {
	mock := newMockIAMClient()
	ctx := context.Background()

	forbiddenErr := &googleapi.Error{Code: http.StatusForbidden, Message: "Permission denied"}
	mock.setErrors = []error{forbiddenErr}

	var sleepCalls int
	sleepFunc := func(ctx context.Context, d time.Duration) error {
		sleepCalls++
		return nil
	}

	desired := []string{"user:alice@example.com"}
	err := ReconcileIAPMembersWithClient(ctx, mock, "proj", "backend", desired,
		WithSleep(sleepFunc),
		WithMaxRetries(5),
	)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if mock.setCalls != 1 {
		t.Fatalf("setCalls = %d, want 1 (should not retry forbidden error)", mock.setCalls)
	}
	if sleepCalls != 0 {
		t.Fatalf("sleepCalls = %d, want 0", sleepCalls)
	}
}

func TestReconcileIAPMembers_GetPolicyError(t *testing.T) {
	mock := newMockIAMClient()
	ctx := context.Background()

	mock.getErr = errors.New("network error")
	desired := []string{"user:alice@example.com"}
	err := ReconcileIAPMembersWithClient(ctx, mock, "proj", "backend", desired)
	if err == nil {
		t.Fatal("expected get error, got nil")
	}
}

func TestReconcileIAPMembers_ValidationErrors(t *testing.T) {
	mock := newMockIAMClient()
	ctx := context.Background()

	if err := ReconcileIAPMembersWithClient(ctx, mock, "", "backend", []string{"user:alice@example.com"}); err == nil {
		t.Error("expected error for empty projectID")
	}
	if err := ReconcileIAPMembersWithClient(ctx, mock, "proj", "", []string{"user:alice@example.com"}); err == nil {
		t.Error("expected error for empty backendServiceName")
	}
	if err := ReconcileIAPMembersWithClient(ctx, nil, "proj", "backend", []string{"user:alice@example.com"}); err == nil {
		t.Error("expected error for nil client")
	}
}

func TestReconcileIAPMembers_RefusesBroadPrincipals(t *testing.T) {
	mock := newMockIAMClient()
	ctx := context.Background()

	desired := []string{
		"alice@example.com",
		"allUsers",
		"allAuthenticatedUsers",
		"domain:gmail.com",
		"principalSet://iam.googleapis.com/locations/global/workforcePools/p/*",
	}
	if err := ReconcileIAPMembersWithClient(ctx, mock, "proj", "svc", desired); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	if mock.setCalls != 1 {
		t.Fatalf("setCalls = %d, want 1", mock.setCalls)
	}
	for _, b := range mock.lastSetPolicy.Bindings {
		for _, m := range b.Members {
			if IsBroadMember(m) {
				t.Fatalf("SetIamPolicy was sent broad member %q: %+v", m, mock.lastSetPolicy.Bindings)
			}
		}
	}
	want := []string{"user:alice@example.com"}
	if len(mock.lastSetPolicy.Bindings) != 1 || !slices.Equal(mock.lastSetPolicy.Bindings[0].Members, want) {
		t.Fatalf("bindings = %+v, want only %v", mock.lastSetPolicy.Bindings, want)
	}
}

func TestReconcileIAPMembers_OnlyBroadDesiredNeverReachesSet(t *testing.T) {
	mock := newMockIAMClient()
	ctx := context.Background()

	if err := ReconcileIAPMembersWithClient(ctx, mock, "proj", "svc", []string{"allUsers"}); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	if mock.setCalls != 0 {
		t.Fatalf("setCalls = %d, want 0: a desired allUsers must never reach SetIamPolicy (sent %+v)", mock.setCalls, mock.lastSetPolicy)
	}
}

func TestApplyDesiredMembers_RemovesExistingPublicBinding(t *testing.T) {
	current := &compute.Policy{
		Etag: "e1",
		Bindings: []*compute.Binding{{
			Role:    RoleIAPHTTPSResourceAccessor,
			Members: []string{"allUsers", "domain:example.com", "user:alice@example.com"},
		}},
	}
	changed, updated := ApplyDesiredMembers(current, []string{"alice@example.com", "allUsers"})
	if !changed {
		t.Fatal("expected the public members to be removed")
	}
	want := []string{"user:alice@example.com"}
	if len(updated.Bindings) != 1 || !slices.Equal(updated.Bindings[0].Members, want) {
		t.Fatalf("bindings = %+v, want only %v", updated.Bindings, want)
	}
}

func TestIsBroadMember(t *testing.T) {
	for _, m := range []string{"allUsers", "allAuthenticatedUsers", "domain:example.com", "principalSet://iam.googleapis.com/x/*"} {
		if !IsBroadMember(m) {
			t.Errorf("IsBroadMember(%q) = false, want true", m)
		}
	}
	for _, m := range []string{"user:allUsers", "user:alice@example.com", "serviceAccount:a@p.iam.gserviceaccount.com", "group:devs@example.com", "principal://iam.googleapis.com/x/subject/s"} {
		if IsBroadMember(m) {
			t.Errorf("IsBroadMember(%q) = true, want false", m)
		}
	}
}
