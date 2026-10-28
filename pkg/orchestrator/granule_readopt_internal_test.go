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

package orchestrator

import (
	"context"
	"fmt"
	"testing"

	"github.com/ai-on-gke/grain/pkg/sandboxaddr"
)

// A daemon readopting a granule VM on a deployment that allocates
// addresses reserves the address the VM's container holds in its own
// pool, which starts empty, so that address is not handed to the next
// sandbox while the adopted one still holds it -- and gives it back with
// the sandbox.
func TestReadoptAddressReservesTheAddressTheVMHolds(t *testing.T) {
	previous, err := sandboxaddr.NewPool("10.77.0.0/24")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := previous.AcquireRoot("t-0"); err != nil {
		t.Fatal(err)
	}
	held, err := previous.AcquireRoot("t-1")
	if err != nil {
		t.Fatal(err)
	}
	// The container is on the docker bridge as well as on the pool's own
	// network; only the pool's address is the one grain gave it.
	withHostFirewall(t, nil, fixedNetworks("172.17.0.5|16|172.17.0.1|||\n"+held.String()+"|24|10.77.0.1|||\n"))

	pool, err := sandboxaddr.NewPool("10.77.0.0/24")
	if err != nil {
		t.Fatal(err)
	}
	k := &GranuleSandboxes{cfg: GranuleConfig{Addresses: pool}}
	got, err := k.readoptAddress(context.Background(), "t-1", "g-t-1")
	if err != nil || got != held {
		t.Fatalf("readoptAddress = %v, %v, want %v", got, err, held)
	}
	for i := 0; i < pool.Families()-1; i++ {
		addr, err := pool.AcquireRoot(fmt.Sprint("next-", i))
		if err != nil {
			t.Fatal(err)
		}
		if addr == held {
			t.Fatalf("the pool handed out %s, which the readopted VM holds", addr)
		}
	}
	if released := pool.Release("t-1"); len(released) != 1 {
		t.Fatalf("released %v, want the readopted sandbox's reservation", released)
	}
}

// A VM whose container holds no address in the pool is refused rather
// than readopted with none, which would leave its real address free.
func TestReadoptAddressRefusesAVMOutsideThePool(t *testing.T) {
	withHostFirewall(t, nil, fixedNetworks("172.17.0.5|16|172.17.0.1|||\n"))
	pool, err := sandboxaddr.NewPool("10.77.0.0/24")
	if err != nil {
		t.Fatal(err)
	}
	k := &GranuleSandboxes{cfg: GranuleConfig{Addresses: pool}}
	if addr, err := k.readoptAddress(context.Background(), "t-1", "g-t-1"); err == nil {
		t.Fatalf("readoptAddress = %v, want a refusal", addr)
	}
	if _, ok := pool.Addr("t-1"); ok {
		t.Fatal("a refused VM was given an address")
	}
}
