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
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/hub"
	"github.com/ai-on-gke/grain/pkg/hub/api"
	"github.com/ai-on-gke/grain/pkg/hub/store"
)

// slowCompute is a fakeCompute whose CreateInstance takes a while, like a GCE
// insert, and counts how often it is called.
type slowCompute struct {
	*fakeCompute
	delay time.Duration
	mu    sync.Mutex
	calls int
}

func (s *slowCompute) CreateInstance(ctx context.Context, spec hub.InstanceSpec) (*hub.InstanceHandle, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	select {
	case <-time.After(s.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return s.fakeCompute.CreateInstance(ctx, spec)
}

func (s *slowCompute) createCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// A reconcile pass that outlasts the lease TTL must keep the lease, so a second
// replica cannot take over mid-pass and create the same VM a second time.
func TestReconcilerKeepsLeaseThroughALongPass(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if err := st.SeedShapes(ctx, []store.ShapeRow{{Name: "medium", MachineType: "n2-standard-4", BootDiskGB: 50, QuotaUnits: 1}}); err != nil {
		t.Fatalf("SeedShapes: %v", err)
	}
	if err := st.SeedSQLInstances(ctx, []store.SQLInstanceRow{{Name: "sql-pool-01", ConnectionName: "test-proj:us-central1:sql-pool-01", PrivateIP: "10.8.0.5", MaxDatabases: 10}}); err != nil {
		t.Fatalf("SeedSQLInstances: %v", err)
	}
	cfg := hub.Config{
		Host:           "grain.example.com",
		InstanceDomain: "instances.grain.example.com",
		Compute: hub.ComputeConfig{
			Project:       "test-proj",
			Region:        "us-central1",
			Zones:         []string{"us-central1-a"},
			VMPrefix:      "grain-vm-",
			SAPrefix:      "grain-sa-",
			HealthTimeout: 10 * time.Minute,
			RegistryID:    "grain.example.com",
		},
	}
	newRec := func(holder string, comp hub.Compute) *Reconciler {
		t.Helper()
		rec, err := New(Options{
			Config:             cfg,
			Store:              st,
			Compute:            comp,
			Resources:          newFakeResources(st),
			ConfigRepos:        newFakeConfigRepos(),
			HolderID:           holder,
			LeaseTTL:           400 * time.Millisecond,
			LeaseRenewInterval: 50 * time.Millisecond,
			HealthTimeout:      10 * time.Minute,
		})
		if err != nil {
			t.Fatalf("New %s: %v", holder, err)
		}
		return rec
	}
	comp1 := &slowCompute{fakeCompute: newFakeCompute("http://127.0.0.1:1"), delay: 1500 * time.Millisecond}
	comp2 := &slowCompute{fakeCompute: newFakeCompute("http://127.0.0.1:1")}
	rec1 := newRec("replica-1", comp1)
	rec2 := newRec("replica-2", comp2)

	if _, err := st.CreateEntry(ctx, store.CreateEntryInput{
		Name: "slow-vm", Kind: api.KindInstance, OwnerEmail: "alice@example.com", Shape: "medium", Zone: "us-central1-a",
	}); err != nil {
		t.Fatalf("CreateEntry: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = rec1.RunOnce(ctx)
	}()
	// Well past the lease TTL, while rec1 is still inside its slow insert.
	time.Sleep(900 * time.Millisecond)
	if comp1.createCalls() != 1 {
		e, _ := st.GetEntryByName(ctx, "slow-vm")
		if e != nil {
			t.Logf("entry: state=%s reason=%q", e.LifecycleState, e.StatusReason)
		}
		t.Fatalf("rec1 CreateInstance calls = %d, want 1 (still in progress)", comp1.createCalls())
	}
	if err := rec2.RunOnce(ctx); err != nil {
		t.Fatalf("rec2.RunOnce: %v", err)
	}
	if rec2.IsLeader() {
		t.Fatal("replica-2 took the lease while replica-1's pass was still running")
	}
	if n := comp2.createCalls(); n != 0 {
		t.Fatalf("replica-2 created the VM again (%d calls) while replica-1 was creating it", n)
	}
	<-done
	if !rec1.IsLeader() {
		t.Fatal("replica-1 lost leadership although it kept renewing")
	}
}
