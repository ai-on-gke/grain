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

package sandboxaddr

import (
	"fmt"
	"net/netip"
	"sync"
	"testing"
)

func mustPool(t *testing.T, cidr string) *Pool {
	t.Helper()
	pool, err := NewPool(cidr)
	if err != nil {
		t.Fatalf("NewPool(%q): %v", cidr, err)
	}
	return pool
}

func mustRoot(t *testing.T, pool *Pool, name string) netip.Addr {
	t.Helper()
	addr, err := pool.AcquireRoot(name)
	if err != nil {
		t.Fatalf("AcquireRoot(%q): %v", name, err)
	}
	return addr
}

func mustChild(t *testing.T, pool *Pool, parent, name string) netip.Addr {
	t.Helper()
	addr, err := pool.AcquireChild(parent, name)
	if err != nil {
		t.Fatalf("AcquireChild(%q, %q): %v", parent, name, err)
	}
	return addr
}

// TestFamiliesAreAlignedBlocks is what makes a family one firewall rule:
// each root starts a block FamilySize wide, aligned, and the pool's own
// first block is never handed out.
func TestFamiliesAreAlignedBlocks(t *testing.T) {
	pool := mustPool(t, "10.128.0.0/16")

	first := mustRoot(t, pool, "a")
	second := mustRoot(t, pool, "b")

	// The reserved first block means the first family starts one block in,
	// not at the pool's own base.
	if got, want := first.String(), "10.128.0.32"; got != want {
		t.Errorf("first root at %s, want %s (the pool's own first block is reserved)", got, want)
	}
	if got, want := second.String(), "10.128.0.64"; got != want {
		t.Errorf("second root at %s, want %s (one block on)", got, want)
	}

	prefix, ok := pool.FamilyPrefix("a")
	if !ok {
		t.Fatal("no family prefix for a")
	}
	if got, want := prefix.String(), "10.128.0.32/27"; got != want {
		t.Errorf("family prefix %s, want %s", got, want)
	}
	if prefix.Masked() != prefix {
		t.Errorf("family prefix %s is not aligned, so it is not a single CIDR", prefix)
	}
	if got := 1 << (prefix.Addr().BitLen() - prefix.Bits()); got != FamilySize {
		t.Errorf("family prefix covers %d addresses, want FamilySize %d", got, FamilySize)
	}
}

// TestDescendantsShareTheRootsBlockHoweverDeep: depth is free, because the
// block is the family rather than one generation of it.
func TestDescendantsShareTheRootsBlockHoweverDeep(t *testing.T) {
	pool := mustPool(t, "10.128.0.0/16")
	root := mustRoot(t, pool, "root")
	child := mustChild(t, pool, "root", "child")
	grandchild := mustChild(t, pool, "child", "grandchild")
	greatgrand := mustChild(t, pool, "grandchild", "greatgrand")

	prefix, _ := pool.FamilyPrefix("root")
	for name, addr := range map[string]netip.Addr{
		"root": root, "child": child, "grandchild": grandchild, "greatgrand": greatgrand,
	} {
		if !prefix.Contains(addr) {
			t.Errorf("%s is at %s, outside its family block %s", name, addr, prefix)
		}
	}

	// And every member reports the same block, so a rule written for the
	// root already covers a child acquired later.
	for _, name := range []string{"child", "grandchild", "greatgrand"} {
		got, ok := pool.FamilyPrefix(name)
		if !ok || got != prefix {
			t.Errorf("FamilyPrefix(%q) = %v, want %v", name, got, prefix)
		}
	}

	members, _ := pool.Family("greatgrand")
	if len(members) != 4 || members[0] != "root" {
		t.Errorf("Family = %v, want all four with the root first", members)
	}
}

// TestReleasingARootFreesTheWholeFamilyAndItsBlock is the destroy-a-parent
// -and-everything-under-it case, and the block becoming reusable is what
// stops a long-lived deployment leaking address space.
func TestReleasingARootFreesTheWholeFamilyAndItsBlock(t *testing.T) {
	pool := mustPool(t, "10.128.0.0/16")
	rootAddr := mustRoot(t, pool, "root")
	mustChild(t, pool, "root", "child")
	mustChild(t, pool, "child", "grandchild")

	released := pool.Release("root")
	if len(released) != 3 {
		t.Fatalf("Release freed %v, want all three", released)
	}
	// Deepest first, so a caller can tear each down knowing nothing it
	// created is still live.
	if released[0] != "grandchild" || released[len(released)-1] != "root" {
		t.Errorf("Release order %v, want deepest first and the root last", released)
	}
	for _, name := range released {
		if _, ok := pool.Addr(name); ok {
			t.Errorf("%s still holds an address after release", name)
		}
	}

	// The block goes back to the pool, but not to the very next acquire:
	// the allocator's cursor moves on, so a freed address is not handed
	// straight to a new sandbox while the old one's rules and a
	// neighbour's ARP cache may still name it. It is reused once the
	// cursor comes round -- which TestAFullPoolIsAnError covers, since a
	// full pool has nowhere else to go.
	next := mustRoot(t, pool, "next")
	if next == rootAddr {
		t.Errorf("next root reused %s immediately; the cursor should have moved on", rootAddr)
	}
	prefix, _ := pool.FamilyPrefix("next")
	if prefix.Contains(rootAddr) {
		t.Errorf("the new family %s overlaps the freed block at %s", prefix, rootAddr)
	}
}

// TestReleasingAChildLeavesItsSiblings: destroying a child takes that
// child's own descendants and nothing else, which is the whole reason
// parent links are kept beside the block.
func TestReleasingAChildLeavesItsSiblings(t *testing.T) {
	pool := mustPool(t, "10.128.0.0/16")
	mustRoot(t, pool, "root")
	mustChild(t, pool, "root", "left")
	mustChild(t, pool, "root", "right")
	mustChild(t, pool, "left", "leftkid")
	mustChild(t, pool, "right", "rightkid")

	released := pool.Release("left")
	if len(released) != 2 || released[0] != "leftkid" || released[1] != "left" {
		t.Fatalf("Release(left) = %v, want [leftkid left]", released)
	}
	for _, survivor := range []string{"root", "right", "rightkid"} {
		if _, ok := pool.Addr(survivor); !ok {
			t.Errorf("%s lost its address when a sibling subtree was released", survivor)
		}
	}
	members, _ := pool.Family("root")
	if len(members) != 3 {
		t.Errorf("family is %v, want root, right and rightkid", members)
	}
}

// TestAFreedOffsetIsReused keeps a family usable across churn: an agent
// that creates and destroys sandboxes in a loop must not exhaust its block.
func TestAFreedOffsetIsReused(t *testing.T) {
	pool := mustPool(t, "10.128.0.0/16")
	mustRoot(t, pool, "root")
	first := mustChild(t, pool, "root", "one")
	pool.Release("one")
	second := mustChild(t, pool, "root", "two")
	if first != second {
		t.Errorf("second child at %s, want the freed %s", second, first)
	}
}

// TestAFamilyIsBoundedByItsBlock: the cap is the point, so it has to be an
// error rather than a spill into the next family's addresses.
func TestAFamilyIsBoundedByItsBlock(t *testing.T) {
	pool := mustPool(t, "10.128.0.0/16")
	mustRoot(t, pool, "root")
	for i := 1; i < FamilySize; i++ {
		mustChild(t, pool, "root", fmt.Sprintf("kid%d", i))
	}
	if _, err := pool.AcquireChild("root", "one-too-many"); err == nil {
		t.Fatal("a family took more than FamilySize sandboxes")
	}
	// Every address handed out stays inside the block.
	prefix, _ := pool.FamilyPrefix("root")
	members, _ := pool.Family("root")
	if len(members) != FamilySize {
		t.Fatalf("family holds %d, want %d", len(members), FamilySize)
	}
	for _, name := range members {
		addr, _ := pool.Addr(name)
		if !prefix.Contains(addr) {
			t.Errorf("%s at %s escaped the block %s", name, addr, prefix)
		}
	}
}

// TestAFullPoolIsAnError, and the reserved first block is why the count is
// one short of the arithmetic.
func TestAFullPoolIsAnError(t *testing.T) {
	// A /22 is 1024 addresses: 32 families of 32, less the reserved one.
	pool := mustPool(t, "10.128.0.0/22")
	if got, want := pool.Families(), 1024/FamilySize-1; got != want {
		t.Fatalf("Families = %d, want %d", got, want)
	}
	for i := 0; i < pool.Families(); i++ {
		mustRoot(t, pool, fmt.Sprintf("r%d", i))
	}
	if _, err := pool.AcquireRoot("one-too-many"); err == nil {
		t.Fatal("a full pool handed out another family")
	}
	// Freeing one makes room again.
	pool.Release("r0")
	if _, err := pool.AcquireRoot("replacement"); err != nil {
		t.Errorf("a freed family was not reused: %v", err)
	}
}

func TestRefusals(t *testing.T) {
	pool := mustPool(t, "10.128.0.0/16")
	mustRoot(t, pool, "root")

	if _, err := pool.AcquireRoot("root"); err == nil {
		t.Error("a second address for a known sandbox was allowed")
	}
	if _, err := pool.AcquireChild("root", "root"); err == nil {
		t.Error("a known sandbox was allowed a second address as a child")
	}
	if _, err := pool.AcquireChild("nobody", "kid"); err == nil {
		t.Error("a child of an unknown parent was allowed")
	}
	if _, err := pool.AcquireRoot(""); err == nil {
		t.Error("an unnamed sandbox was allowed")
	}
	// Releasing something unknown is quiet: a deferred teardown that runs
	// twice should not be an error.
	if got := pool.Release("nobody"); got != nil {
		t.Errorf("Release of an unknown sandbox returned %v", got)
	}
	if _, ok := pool.Addr("nobody"); ok {
		t.Error("an unknown sandbox reported an address")
	}
	if _, ok := pool.FamilyPrefix("nobody"); ok {
		t.Error("an unknown sandbox reported a family prefix")
	}
}

func TestPoolsTooSmallAreRefused(t *testing.T) {
	for _, cidr := range []string{
		"10.0.0.0/28", // one family, no room for the reserved block
		"10.0.0.0/32",
		"10.0.0.0/27", // exactly one block, which is the reserved one
	} {
		if _, err := NewPool(cidr); err == nil {
			t.Errorf("NewPool(%q) was accepted", cidr)
		}
	}
	if _, err := NewPool("not a cidr"); err == nil {
		t.Error("NewPool accepted a non-CIDR")
	}
	// A /26 is two blocks: the reserved one and one family.
	pool, err := NewPool("10.0.0.0/26")
	if err != nil {
		t.Fatalf("NewPool(/26): %v", err)
	}
	if got := pool.Families(); got != 1 {
		t.Errorf("a /26 holds %d families, want 1", got)
	}
}

// TestAnUnalignedPoolIsMaskedRatherThanRefused: a CIDR whose host bits are
// not already zero describes the same network, so it is normalised instead
// of rejected -- and the blocks still come out aligned.
func TestAnUnalignedPoolIsMasked(t *testing.T) {
	pool := mustPool(t, "10.128.0.77/16")
	if got, want := pool.Prefix().String(), "10.128.0.0/16"; got != want {
		t.Errorf("Prefix = %s, want %s", got, want)
	}
	addr := mustRoot(t, pool, "a")
	if got, want := addr.String(), "10.128.0.32"; got != want {
		t.Errorf("root at %s, want %s", got, want)
	}
}

// TestIPv6Pools: nothing here is IPv4-specific, and the byte arithmetic in
// addrPlus has a separate path for 16-byte addresses that needs covering.
func TestIPv6Pools(t *testing.T) {
	pool := mustPool(t, "fd00:1234:5678::/64")
	if pool == nil {
		t.Fatal("no pool")
	}
	root := mustRoot(t, pool, "root")
	child := mustChild(t, pool, "root", "child")
	prefix, _ := pool.FamilyPrefix("root")
	if !prefix.Contains(root) || !prefix.Contains(child) {
		t.Errorf("family %s does not contain %s and %s", prefix, root, child)
	}
	if got := prefix.Bits(); got != 128-familyHostBits {
		t.Errorf("family prefix is /%d, want /%d", got, 128-familyHostBits)
	}
}

// TestAddrPlusCarriesAcrossOctets is the arithmetic addrPlus replaced a
// loop of Next() calls with, so it is worth checking it rolls over.
func TestAddrPlusCarriesAcrossOctets(t *testing.T) {
	for _, tc := range []struct {
		from string
		n    uint64
		want string
	}{
		{"10.0.0.0", 0, "10.0.0.0"},
		{"10.0.0.250", 10, "10.0.1.4"},
		{"10.0.255.250", 10, "10.1.0.4"},
		{"10.0.0.0", 65536, "10.1.0.0"},
		{"::", 1, "::1"},
		{"::ff", 1, "::100"},
		{"::ffff:ffff", 1, "::1:0:0"},
	} {
		got := addrPlus(netip.MustParseAddr(tc.from), tc.n)
		if got.String() != netip.MustParseAddr(tc.want).String() {
			t.Errorf("addrPlus(%s, %d) = %s, want %s", tc.from, tc.n, got, tc.want)
		}
	}
}

// TestConcurrentAcquireHandsOutDistinctAddresses: a dispatch and a
// teardown run on different goroutines, so the one invariant that must not
// be racy is that two sandboxes never share an address.
func TestConcurrentAcquireHandsOutDistinctAddresses(t *testing.T) {
	pool := mustPool(t, "10.128.0.0/16")
	const workers = 32

	var wg sync.WaitGroup
	var mu sync.Mutex
	seen := map[netip.Addr]string{}

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			root := fmt.Sprintf("r%d", i)
			addr, err := pool.AcquireRoot(root)
			if err != nil {
				t.Errorf("AcquireRoot(%s): %v", root, err)
				return
			}
			kid := fmt.Sprintf("k%d", i)
			kidAddr, err := pool.AcquireChild(root, kid)
			if err != nil {
				t.Errorf("AcquireChild(%s): %v", kid, err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for name, a := range map[string]netip.Addr{root: addr, kid: kidAddr} {
				if other, clash := seen[a]; clash {
					t.Errorf("%s and %s both got %s", name, other, a)
				}
				seen[a] = name
			}
		}(i)
	}
	wg.Wait()
	if len(seen) != workers*2 {
		t.Errorf("handed out %d distinct addresses for %d sandboxes", len(seen), workers*2)
	}
}

// A new process's pool takes back the address a sandbox built by the
// previous one still holds, so the address is not handed out again beside
// it, and the reservation is released like any other.
func TestReserveRootTakesBackAnAddressInUse(t *testing.T) {
	for _, cidr := range []string{"10.0.0.0/24", "fd00::/112"} {
		t.Run(cidr, func(t *testing.T) {
			previous := mustPool(t, cidr)
			mustRoot(t, previous, "first")
			held := mustRoot(t, previous, "adopted")

			pool := mustPool(t, cidr)
			if err := pool.ReserveRoot("adopted", held); err != nil {
				t.Fatalf("ReserveRoot(%s): %v", held, err)
			}
			if err := pool.ReserveRoot("adopted", held); err != nil {
				t.Fatalf("reserving the same sandbox at the same address again: %v", err)
			}
			if got, ok := pool.Addr("adopted"); !ok || got != held {
				t.Fatalf("Addr = %v, %v, want %v", got, ok, held)
			}
			for i := 0; i < pool.Families()-1; i++ {
				if addr := mustRoot(t, pool, fmt.Sprintf("new-%d", i)); addr == held {
					t.Fatalf("handed out %s, which the adopted sandbox holds", addr)
				}
			}
			if _, err := pool.AcquireRoot("one-too-many"); err == nil {
				t.Fatal("a pool whose last family was reserved still handed one out")
			}
			if got := pool.Release("adopted"); fmt.Sprint(got) != "[adopted]" {
				t.Fatalf("Release = %v", got)
			}
			if addr := mustRoot(t, pool, "after"); addr != held {
				t.Fatalf("after release got %s, want the freed block %s back", addr, held)
			}
		})
	}
}

func TestReserveRootRefusals(t *testing.T) {
	pool := mustPool(t, "10.0.0.0/24")
	taken := mustRoot(t, pool, "taken")
	for _, tc := range []struct {
		name, sandbox, addr string
	}{
		{"outside the pool", "a", "10.0.1.32"},
		{"not a family's first address", "a", "10.0.0.33"},
		{"the reserved first block", "a", "10.0.0.0"},
		{"another sandbox's family", "a", taken.String()},
		{"a sandbox with another address", "taken", "10.0.0.64"},
		{"no name", "", "10.0.0.96"},
	} {
		if err := pool.ReserveRoot(tc.sandbox, netip.MustParseAddr(tc.addr)); err == nil {
			t.Errorf("%s: ReserveRoot(%q, %s) succeeded", tc.name, tc.sandbox, tc.addr)
		}
	}
}
