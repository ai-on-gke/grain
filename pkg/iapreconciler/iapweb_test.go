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

	"google.golang.org/api/googleapi"
	"google.golang.org/api/iap/v1"
)

const testCloudRunResource = "projects/123456/iap_web/cloud_run-europe-west4/services/grain-relay"

type mockIAPWebClient struct {
	mu            sync.Mutex
	policies      map[string]*iap.Policy
	getErr        error
	setErrors     []error
	getCalls      int
	setCalls      int
	lastSetPolicy *iap.Policy
}

func newMockIAPWebClient() *mockIAPWebClient {
	return &mockIAPWebClient{policies: make(map[string]*iap.Policy)}
}

func (m *mockIAPWebClient) GetIAPWebIAMPolicy(ctx context.Context, resource string) (*iap.Policy, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getCalls++
	if m.getErr != nil {
		return nil, m.getErr
	}
	if p, ok := m.policies[resource]; ok {
		return cloneWebPolicy(p), nil
	}
	return &iap.Policy{Etag: "initial-etag"}, nil
}

func (m *mockIAPWebClient) SetIAPWebIAMPolicy(ctx context.Context, resource string, policy *iap.Policy) (*iap.Policy, error) {
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
	m.lastSetPolicy = cloneWebPolicy(policy)
	cp := cloneWebPolicy(policy)
	cp.Etag = "updated-etag-" + policy.Etag
	m.policies[resource] = cp
	return cp, nil
}

func cloneWebPolicy(p *iap.Policy) *iap.Policy {
	if p == nil {
		return nil
	}
	cp := &iap.Policy{Etag: p.Etag, Version: p.Version}
	for _, b := range p.Bindings {
		if b == nil {
			continue
		}
		bc := &iap.Binding{Role: b.Role, Members: slices.Clone(b.Members)}
		if b.Condition != nil {
			c := *b.Condition
			bc.Condition = &c
		}
		cp.Bindings = append(cp.Bindings, bc)
	}
	return cp
}

func webAccessorMembers(p *iap.Policy) []string {
	for _, b := range p.Bindings {
		if b.Role == RoleIAPHTTPSResourceAccessor && b.Condition == nil {
			return b.Members
		}
	}
	return nil
}

func TestCloudRunWebResourceFromAudience(t *testing.T) {
	cases := []struct {
		aud    string
		want   string
		wantOK bool
	}{
		{"/projects/123456/locations/europe-west4/services/grain-relay", testCloudRunResource, true},
		{" /projects/1/global/backendServices/99 , /projects/123456/locations/europe-west4/services/grain-relay", testCloudRunResource, true},
		{"/projects/1/global/backendServices/99", "", false},
		{"", "", false},
		{"/projects/my-project/locations/europe-west4/services/grain-relay", "", false},
	}
	for _, c := range cases {
		got, ok := CloudRunWebResourceFromAudience(c.aud)
		if got != c.want || ok != c.wantOK {
			t.Errorf("CloudRunWebResourceFromAudience(%q) = %q, %v; want %q, %v", c.aud, got, ok, c.want, c.wantOK)
		}
	}
}

func TestValidateWebResource(t *testing.T) {
	for _, ok := range []string{
		testCloudRunResource,
		"projects/123/iap_web/compute/services/456",
	} {
		if err := ValidateWebResource(ok); err != nil {
			t.Errorf("ValidateWebResource(%q) = %v; want nil", ok, err)
		}
	}
	for _, bad := range []string{
		"",
		"grain-relay",
		"/projects/123/iap_web/cloud_run-europe-west4/services/grain-relay",
		"projects/123/locations/europe-west4/services/grain-relay",
		"projects/123/iap_web/cloud_run-europe-west4/services/",
		// The whole project's IAP: granting there opens every IAP app in it.
		"projects/123/iap_web",
		"projects/123/iap_web/compute",
	} {
		if err := ValidateWebResource(bad); err == nil {
			t.Errorf("ValidateWebResource(%q) = nil; want an error", bad)
		}
	}
}

func TestReconcileWebResource_FullCycle(t *testing.T) {
	ctx := context.Background()
	mock := newMockIAPWebClient()
	mock.policies[testCloudRunResource] = &iap.Policy{
		Etag:    "etag-1",
		Version: 3,
		Bindings: []*iap.Binding{
			{Role: RoleIAPHTTPSResourceAccessor, Members: []string{"user:old@example.com", "user:keep@example.com"}},
			{Role: "roles/iap.admin", Members: []string{"user:admin@example.com"}},
			{
				Role:      RoleIAPHTTPSResourceAccessor,
				Members:   []string{"user:temp@example.com"},
				Condition: &iap.Expr{Title: "expires", Expression: "request.time < timestamp('2030-01-01T00:00:00Z')"},
			},
		},
	}

	desired := []string{"keep@example.com", "new@example.com", "grain-eu-host@bsalmon-gke-dev.iam.gserviceaccount.com"}
	if err := ReconcileWebResourceMembersWithClient(ctx, mock, testCloudRunResource, desired); err != nil {
		t.Fatalf("ReconcileWebResource: %v", err)
	}
	if mock.setCalls != 1 {
		t.Fatalf("setCalls = %d; want 1", mock.setCalls)
	}
	got := mock.lastSetPolicy
	if got.Etag != "etag-1" || got.Version != 3 {
		t.Errorf("set policy etag/version = %q/%d; want the read's etag-1/3", got.Etag, got.Version)
	}
	want := []string{
		"serviceAccount:grain-eu-host@bsalmon-gke-dev.iam.gserviceaccount.com",
		"user:keep@example.com",
		"user:new@example.com",
	}
	if m := webAccessorMembers(got); !slices.Equal(m, want) {
		t.Errorf("accessor members = %v; want %v", m, want)
	}
	var sawAdmin, sawConditional bool
	for _, b := range got.Bindings {
		if b.Role == "roles/iap.admin" && slices.Equal(b.Members, []string{"user:admin@example.com"}) {
			sawAdmin = true
		}
		if b.Condition != nil && b.Condition.Title == "expires" && slices.Equal(b.Members, []string{"user:temp@example.com"}) {
			sawConditional = true
		}
	}
	if !sawAdmin || !sawConditional {
		t.Errorf("other bindings lost (admin %v, conditional %v): %+v", sawAdmin, sawConditional, got.Bindings)
	}

	// A second pass over the same desired set reads and changes nothing.
	if err := ReconcileWebResourceMembersWithClient(ctx, mock, testCloudRunResource, desired); err != nil {
		t.Fatalf("second ReconcileWebResource: %v", err)
	}
	if mock.setCalls != 1 {
		t.Errorf("setCalls after an idempotent pass = %d; want 1", mock.setCalls)
	}
}

func TestReconcileWebResource_ConcurrencyConflictRetry(t *testing.T) {
	mock := newMockIAPWebClient()
	mock.setErrors = []error{
		&googleapi.Error{Code: http.StatusConflict, Message: "etag mismatch"},
		&googleapi.Error{Code: http.StatusPreconditionFailed},
		nil,
	}
	var slept []time.Duration
	err := ReconcileWebResourceMembersWithClient(context.Background(), mock, testCloudRunResource, []string{"a@example.com"},
		WithSleep(func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }))
	if err != nil {
		t.Fatalf("ReconcileWebResource: %v", err)
	}
	if mock.getCalls != 3 || mock.setCalls != 3 || len(slept) != 2 {
		t.Errorf("get/set/sleeps = %d/%d/%d; want 3/3/2 (a fresh read before every retry)", mock.getCalls, mock.setCalls, len(slept))
	}
}

func TestReconcileWebResource_NonConflictErrorDoesNotRetry(t *testing.T) {
	mock := newMockIAPWebClient()
	mock.setErrors = []error{&googleapi.Error{Code: http.StatusForbidden, Message: "permission iap.webServiceVersions.setIamPolicy denied"}}
	err := ReconcileWebResourceMembersWithClient(context.Background(), mock, testCloudRunResource, []string{"a@example.com"},
		WithSleep(func(context.Context, time.Duration) error { return nil }))
	if err == nil {
		t.Fatal("ReconcileWebResource succeeded; want the permission error")
	}
	if mock.setCalls != 1 {
		t.Errorf("setCalls = %d; want 1", mock.setCalls)
	}
}

func TestReconcileWebResource_GetPolicyError(t *testing.T) {
	mock := newMockIAPWebClient()
	mock.getErr = errors.New("boom")
	if err := ReconcileWebResourceMembersWithClient(context.Background(), mock, testCloudRunResource, []string{"a@example.com"}); err == nil {
		t.Fatal("want an error")
	}
	if mock.setCalls != 0 {
		t.Errorf("setCalls = %d; want 0", mock.setCalls)
	}
}

func TestReconcileWebResource_ValidationErrors(t *testing.T) {
	ctx := context.Background()
	mock := newMockIAPWebClient()
	if err := NewWebResourceReconciler(mock).ReconcileWebResource(ctx, "", []string{"a@example.com"}); err == nil {
		t.Error("empty resource: want an error")
	}
	if err := NewWebResourceReconciler(mock).ReconcileWebResource(ctx, "grain-relay", []string{"a@example.com"}); err == nil {
		t.Error("malformed resource: want an error")
	}
	if err := NewWebResourceReconciler(nil).ReconcileWebResource(ctx, testCloudRunResource, []string{"a@example.com"}); err == nil {
		t.Error("nil client: want an error")
	}
	// A backend-service reconciler has no IAP web client, and the reverse.
	if err := NewReconciler(newMockIAMClient()).ReconcileWebResource(ctx, testCloudRunResource, []string{"a@example.com"}); err == nil {
		t.Error("backend-service reconciler reconciling a web resource: want an error")
	}
	if err := NewWebResourceReconciler(mock).Reconcile(ctx, "p", "svc", []string{"a@example.com"}); err == nil {
		t.Error("web-resource reconciler reconciling a backend service: want an error")
	}
	if mock.getCalls != 0 {
		t.Errorf("getCalls = %d; want 0", mock.getCalls)
	}
}

func TestReconcileWebResource_RefusesBroadPrincipals(t *testing.T) {
	mock := newMockIAPWebClient()
	mock.policies[testCloudRunResource] = &iap.Policy{
		Etag:     "e",
		Bindings: []*iap.Binding{{Role: RoleIAPHTTPSResourceAccessor, Members: []string{"allUsers", "user:a@example.com"}}},
	}
	desired := []string{"a@example.com", "allAuthenticatedUsers", "domain:example.com", "principalSet://iam.googleapis.com/pool"}
	if err := ReconcileWebResourceMembersWithClient(context.Background(), mock, testCloudRunResource, desired); err != nil {
		t.Fatalf("ReconcileWebResource: %v", err)
	}
	if m := webAccessorMembers(mock.lastSetPolicy); !slices.Equal(m, []string{"user:a@example.com"}) {
		t.Errorf("accessor members = %v; want only user:a@example.com (the existing allUsers grant removed)", m)
	}
}
