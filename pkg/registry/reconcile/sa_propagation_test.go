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

package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/registry"
	"github.com/ai-on-gke/grain/pkg/registry/api"
	"github.com/ai-on-gke/grain/pkg/registry/store"
)

// saNotReadyCompute fails the first `failures` CreateInstance calls (all of them when
// failures < 0) the way the GCE provider does while a just-created instance service account
// has not propagated to Compute yet, then delegates to fakeCompute.
type saNotReadyCompute struct {
	*fakeCompute
	mu       sync.Mutex
	failures int
	calls    int
	// accessDenied fails the way GCE does while the registry's just-made actAs grant on
	// the account has not applied yet, instead of the account not being visible.
	accessDenied bool
}

func (c *saNotReadyCompute) CreateInstance(ctx context.Context, spec registry.InstanceSpec) (*registry.InstanceHandle, error) {
	c.mu.Lock()
	c.calls++
	fail := c.failures != 0
	if c.failures > 0 {
		c.failures--
	}
	accessDenied := c.accessDenied
	c.mu.Unlock()
	if fail && accessDenied {
		return nil, fmt.Errorf("gcp compute: inserting instance grain-vm-%s in %s: GCE operation op-1 failed (SERVICE_ACCOUNT_ACCESS_DENIED): The user does not have access to service account '%s'. (%w)",
			spec.Name, spec.Zone, spec.ServiceAccount, registry.ErrServiceAccountNotReady)
	}
	if fail {
		return nil, fmt.Errorf("gcp compute: inserting instance grain-vm-%s in %s: GCE operation op-1 failed (EXTERNAL_RESOURCE_NOT_FOUND): The resource '%s' of type 'serviceAccount' was not found. (%w)",
			spec.Name, spec.Zone, spec.ServiceAccount, registry.ErrServiceAccountNotReady)
	}
	return c.fakeCompute.CreateInstance(ctx, spec)
}

func (c *saNotReadyCompute) createCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

type saPropagationFixture struct {
	st    *store.Store
	rec   *Reconciler
	entry *store.EntryRow
	now   *time.Time
}

func newSAPropagationFixture(t *testing.T, compute registry.Compute, provider string) *saPropagationFixture {
	t.Helper()
	st := newTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	nowFn := func() time.Time { return now }
	st.SetNow(nowFn)

	if err := st.SeedShapes(ctx, []store.ShapeRow{
		{Name: "n2-standard-2", MachineType: "n2-standard-2", BootDiskGB: 50, QuotaUnits: 1},
	}); err != nil {
		t.Fatalf("SeedShapes: %v", err)
	}
	if err := st.SeedSQLInstances(ctx, []store.SQLInstanceRow{
		{Name: "sql-pool-01", ConnectionName: "test-proj:us-central1:sql-pool-01", PrivateIP: "10.8.0.5", MaxDatabases: 50},
	}); err != nil {
		t.Fatalf("SeedSQLInstances: %v", err)
	}

	vmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			_, _ = w.Write([]byte("ok"))
		case "/api/registry/status":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(api.InstanceStatusResponse{GrainVersion: "v1.1.0", Health: api.HealthOK})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(vmSrv.Close)
	if sc, ok := compute.(*saNotReadyCompute); ok {
		sc.backendURL = vmSrv.URL
	}

	opts := Options{
		Config: registry.Config{
			Host:           "grain.example.com",
			InstanceDomain: "instances.grain.example.com",
			Compute: registry.ComputeConfig{
				Provider:      provider,
				Project:       "test-proj",
				Region:        "us-central1",
				Zones:         []string{"us-central1-a"},
				VMPrefix:      "grain-vm-",
				SAPrefix:      "gi-",
				HealthTimeout: 10 * time.Minute,
				RegistryID:    "grain.example.com",
			},
		},
		Store:         st,
		ConfigRepos:   newFakeConfigRepos(),
		HolderID:      "leader-1",
		HTTPClient:    vmSrv.Client(),
		HealthTimeout: 10 * time.Minute,
		Now:           nowFn,
	}
	if compute != nil {
		opts.Compute = compute
		opts.Resources = newFakeResources(st)
	}
	rec, err := New(opts)
	if err != nil {
		t.Fatalf("New Reconciler: %v", err)
	}

	listed := true
	entry, err := st.CreateEntry(ctx, store.CreateEntryInput{
		Name:       "hub-e2e-test1",
		Kind:       api.KindInstance,
		OwnerEmail: "alice@example.com",
		Shape:      "n2-standard-2",
		Zone:       "us-central1-a",
		Listed:     &listed,
	})
	if err != nil {
		t.Fatalf("CreateEntry: %v", err)
	}
	return &saPropagationFixture{st: st, rec: rec, entry: entry, now: &now}
}

func (f *saPropagationFixture) runOnce(t *testing.T) *store.EntryRow {
	t.Helper()
	ctx := context.Background()
	_ = f.rec.RunOnce(ctx)
	got, err := f.st.GetEntryByID(ctx, f.entry.ID)
	if err != nil {
		t.Fatalf("GetEntryByID: %v", err)
	}
	return got
}

func TestReconcilerRetriesVMCreationWhileServiceAccountPropagates(t *testing.T) {
	compute := &saNotReadyCompute{fakeCompute: newFakeCompute(""), failures: 1}
	f := newSAPropagationFixture(t, compute, "gce")

	// Pass 1: the insert is refused because Compute cannot see the new account yet. The
	// entry must stay `creating` and say why, not go to `failed`.
	got := f.runOnce(t)
	if got.LifecycleState != api.StateCreating {
		t.Fatalf("after refused insert: state = %q (reason %q), want creating", got.LifecycleState, got.StatusReason)
	}
	if !strings.Contains(got.StatusReason, "waiting for service account") || !strings.Contains(got.StatusReason, "EXTERNAL_RESOURCE_NOT_FOUND") {
		t.Fatalf("after refused insert: status_reason = %q, want it to explain the propagation wait", got.StatusReason)
	}
	if got.GCEInstanceID != "" {
		t.Fatalf("after refused insert: gce_instance_id = %q, want empty", got.GCEInstanceID)
	}

	// A pass before the backoff has elapsed does not hammer the API.
	*f.now = f.now.Add(2 * time.Second)
	f.runOnce(t)
	if n := compute.createCalls(); n != 1 {
		t.Fatalf("CreateInstance calls during backoff = %d, want 1", n)
	}

	// Once the backoff has elapsed the retry goes through and the healthy VM comes up.
	*f.now = f.now.Add(5 * time.Second)
	got = f.runOnce(t)
	if n := compute.createCalls(); n != 2 {
		t.Fatalf("CreateInstance calls after backoff = %d, want 2", n)
	}
	if got.LifecycleState != api.StateRunning {
		t.Fatalf("after retried insert: state = %q (reason %q), want running", got.LifecycleState, got.StatusReason)
	}
	if got.GCEInstanceID != "grain-vm-hub-e2e-test1" || got.StatusReason != "" {
		t.Fatalf("after retried insert: gce_instance_id = %q, status_reason = %q", got.GCEInstanceID, got.StatusReason)
	}
}

func TestReconcilerRetriesVMCreationWhileActAsGrantPropagates(t *testing.T) {
	compute := &saNotReadyCompute{fakeCompute: newFakeCompute(""), failures: 2, accessDenied: true}
	f := newSAPropagationFixture(t, compute, "gce")

	got := f.runOnce(t)
	if got.LifecycleState != api.StateCreating {
		t.Fatalf("after refused insert: state = %q (reason %q), want creating", got.LifecycleState, got.StatusReason)
	}
	if !strings.Contains(got.StatusReason, "permission to attach it") || !strings.Contains(got.StatusReason, "SERVICE_ACCOUNT_ACCESS_DENIED") {
		t.Fatalf("after refused insert: status_reason = %q, want it to explain the actAs propagation wait", got.StatusReason)
	}
	for i := 0; i < 60 && got.LifecycleState == api.StateCreating; i++ {
		*f.now = f.now.Add(time.Second)
		got = f.runOnce(t)
	}
	if got.LifecycleState != api.StateRunning || got.StatusReason != "" {
		t.Fatalf("after the grant applied: state = %q, status_reason = %q, want running", got.LifecycleState, got.StatusReason)
	}
	if n := compute.createCalls(); n != 3 {
		t.Fatalf("CreateInstance calls = %d, want 3", n)
	}
}

func TestReconcilerFailsEntryWhenServiceAccountNeverPropagates(t *testing.T) {
	compute := &saNotReadyCompute{fakeCompute: newFakeCompute(""), failures: -1}
	f := newSAPropagationFixture(t, compute, "gce")
	start := *f.now

	var got *store.EntryRow
	for i := 0; i < 300; i++ {
		got = f.runOnce(t)
		if got.LifecycleState != api.StateCreating {
			break
		}
		*f.now = f.now.Add(time.Second)
	}
	if got.LifecycleState != api.StateFailed {
		t.Fatalf("state = %q (reason %q), want failed once the propagation window is over", got.LifecycleState, got.StatusReason)
	}
	if elapsed := f.now.Sub(start); elapsed < DefaultServiceAccountPropagationTimeout {
		t.Fatalf("entry failed after %s, want it to keep retrying for %s", elapsed, DefaultServiceAccountPropagationTimeout)
	}
	if !strings.Contains(got.StatusReason, "still not visible") || !strings.Contains(got.StatusReason, "EXTERNAL_RESOURCE_NOT_FOUND") {
		t.Fatalf("status_reason = %q, want it to name the propagation failure", got.StatusReason)
	}
	// Backoff of 5s, 10s, 20s, then 30s steps over two minutes: a handful of attempts, not one per pass.
	if n := compute.createCalls(); n < 5 || n > 10 {
		t.Fatalf("CreateInstance calls = %d, want backoff between 5 and 10 attempts", n)
	}
}

func TestReconcilerFailsCreatingEntryWithoutComputeProvider(t *testing.T) {
	f := newSAPropagationFixture(t, nil, "none")

	got := f.runOnce(t)
	if got.LifecycleState != api.StateFailed {
		t.Fatalf("state = %q (reason %q), want failed when compute.provider is none", got.LifecycleState, got.StatusReason)
	}
	if !strings.Contains(got.StatusReason, `compute.provider is "none"`) {
		t.Fatalf("status_reason = %q, want it to name the missing compute provider", got.StatusReason)
	}
}

// countingResources counts Provision calls, each of which (on the GCP provider) generates a
// new database password, writes a new Secret Manager version and rotates the role.
type countingResources struct {
	registry.InstanceResources
	mu         sync.Mutex
	provisions int
}

func (c *countingResources) Provision(ctx context.Context, spec registry.InstanceResourceSpec) (*registry.InstanceResourceBundle, error) {
	c.mu.Lock()
	c.provisions++
	c.mu.Unlock()
	return c.InstanceResources.Provision(ctx, spec)
}

func (c *countingResources) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.provisions
}

// Retrying the VM insert while the service account propagates must not provision the
// instance's resources again: the 2026-11-09 walkthrough (task 3731) saw one new
// db-password Secret Manager version, and one password rotation, per attempt.
func TestReconcilerProvisionsResourcesOnceAcrossServiceAccountRetries(t *testing.T) {
	compute := &saNotReadyCompute{fakeCompute: newFakeCompute(""), failures: 3}
	f := newSAPropagationFixture(t, compute, "gce")
	res := &countingResources{InstanceResources: f.rec.resources}
	f.rec.resources = res

	var got *store.EntryRow
	for i := 0; i < 120; i++ {
		got = f.runOnce(t)
		if got.LifecycleState != api.StateCreating {
			break
		}
		*f.now = f.now.Add(time.Second)
	}
	if got.LifecycleState != api.StateRunning {
		t.Fatalf("state = %q (reason %q), want running once the account propagated", got.LifecycleState, got.StatusReason)
	}
	if n := compute.createCalls(); n != 4 {
		t.Fatalf("CreateInstance calls = %d, want 4 (three refused, one created)", n)
	}
	if n := res.count(); n != 1 {
		t.Fatalf("Provision calls across %d VM creation attempts = %d, want 1", compute.createCalls(), n)
	}
}
