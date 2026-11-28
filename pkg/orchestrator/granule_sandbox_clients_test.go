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
	"errors"
	"net"
	"net/netip"
	"testing"
)

// recordingSandboxClients is a SandboxClientRegistry that keeps what it
// was told, address -> sandbox.
type recordingSandboxClients struct{ names map[string]string }

func (r *recordingSandboxClients) RegisterClient(ip net.IP, sandbox string) {
	r.names[ip.String()] = sandbox
}

func (r *recordingSandboxClients) ReleaseClient(ip net.IP, sandbox string) {
	if r.names[ip.String()] == sandbox {
		delete(r.names, ip.String())
	}
}

// TestGranuleGuestIsRegisteredWithThePackageCacheUnderItsSandboxName: every
// address a guest's traffic arrives from is registered under the name
// its sandbox was acquired as (the one GitScope knows it by, not the VM
// name), and released when its isolation is cleaned up -- so a request
// from an address that has been handed back names nobody.
func TestGranuleGuestIsRegisteredWithThePackageCacheUnderItsSandboxName(t *testing.T) {
	clients := &recordingSandboxClients{names: map[string]string{}}
	k := NewGranuleSandboxes(GranuleConfig{SandboxClients: clients})
	k.guestIPs = func(ctx context.Context, vmName string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("172.18.0.7"), net.ParseIP("fd00:18::7")}, nil
	}
	ctx := context.Background()
	vm := VMNamePrefix + "run-41"

	k.registerGuestClient(ctx, vm, netip.MustParseAddr("172.18.0.7"))
	for _, addr := range []string{"172.18.0.7", "fd00:18::7"} {
		if got := clients.names[addr]; got != "run-41" {
			t.Fatalf("address %s registered as %q, want run-41", addr, got)
		}
	}
	k.cleanupGranuleHostIsolation(ctx, vm)
	if len(clients.names) != 0 {
		t.Fatalf("after cleanup still registered: %v", clients.names)
	}

	// No address to be found and none allocated: nothing registered,
	// which the package cache reads as "spend no credential for it".
	k.guestIPs = func(ctx context.Context, vmName string) ([]net.IP, error) {
		return nil, errors.New("no such container")
	}
	k.registerGuestClient(ctx, vm, netip.Addr{})
	if len(clients.names) != 0 {
		t.Fatalf("registered with no address found: %v", clients.names)
	}
}
