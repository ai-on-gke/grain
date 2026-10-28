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

// Package sandboxaddr allocates the address every sandbox this deployment
// builds is reachable at, in blocks that keep a sandbox and everything it
// goes on to create together.
//
// # Why grain assigns these at all
//
// It used not to. A granule VM's guest takes over the address the
// container runtime assigned its container (granule's own
// dockervm: "the VM container derives its own --net values, and the
// guest's ip= parameter, at boot from the identity on the namespace's
// external interface"), and grain found out what that was afterwards by
// asking docker -- orchestrator.inspectContainerNetwork, whose answer is
// what the host nftables chain enforcing model.NetworkNone matches on.
//
// Three things want the address to be grain's choice instead:
//
//   - The isolation rules stop depending on discovery. Today the chain for
//     a VM cannot be written until `docker inspect` has answered, because
//     the address is whatever docker's DHCP handed out.
//   - A sandbox becomes identifiable by where it is, not only by the token
//     it carries. docs/design.md called this "source pinning as an extra
//     layer where available"; the git proxy authenticates a bearer token
//     today and cannot tell two sandboxes apart if one holds the other's.
//   - Addresses stop colliding between hosts. Every docker bridge is
//     172.17.0.0/16 by default, so two sandbox hosts hand out the same
//     addresses -- which is fine while sandboxes only ever talk to the
//     daemon on their own host, and is the thing that blocks them being
//     routed anywhere else.
//
// # Why blocks rather than a flat pool
//
// A sandbox may create more sandboxes -- siblings of itself rather than
// guests inside it -- and the operation that matters for those is
// destroying a parent together with everything beneath it. Two facts make
// a contiguous block the right shape for that rather than a convenience:
//
//   - A sandbox name has no room to say who its parent is. A granule VM
//     name is capped at 11 bytes (orchestrator.maxVMNameLen: netshim
//     derives "ctl-<name>" and Linux caps an interface name at 15), of
//     which "g-" takes two, and a run id -- "<task id>-<attempt>" -- fills
//     the nine that are left. "999999-99" is exactly at the limit. There is
//     no suffix to add.
//   - One block is one nftables rule. The chain enforcing NetworkNone
//     currently accepts the VM's whole surrounding subnet, which is every
//     other sandbox on that host; an aligned block lets that narrow to
//     "this family and nothing else" without the rule growing per child.
//
// So the block is the family: a root sandbox and every descendant it
// acquires, however deep, share one aligned CIDR. Releasing a root
// releases the block. Parent links are kept as well, because destroying a
// *child* has to take that child's own descendants and not its siblings --
// the block bounds the family, the links order it.
package sandboxaddr

import (
	"fmt"
	"math/bits"
	"net/netip"
	"sort"
	"sync"
)

// FamilySize is how many addresses one family gets: a root sandbox and
// every sandbox created beneath it, at any depth.
//
// It is the one number here that is chosen rather than derived --
// familyHostBits, the block's prefix length and how many families a pool
// holds all follow from it -- and it must be a power of two, because a
// family that is not an aligned block is not a single CIDR, and a family
// that is not a single CIDR is not one firewall rule.
//
// 32 leaves a root and 31 descendants. The number that matters is how
// wide an agent's fan-out gets, not how deep: depth costs nothing here
// because a family is flat in the address space however nested it is in
// parentage.
//
// Raising it is a change to the address layout, not a setting: every
// family moves, so a deployment cannot be renumbered under running
// sandboxes. It belongs in a release, not in Settings.
const FamilySize = 32

// familyHostBits is how many of an address's low bits one family spans,
// derived from FamilySize so the two cannot disagree.
const familyHostBits = 5

func init() {
	// FamilySize and familyHostBits are both written out, because a
	// constant expression is clearer at the point of use than a call --
	// but only one of them is allowed to be wrong, and this is where that
	// is caught, at the first import rather than in whichever rule came
	// out malformed.
	if FamilySize != 1<<familyHostBits || bits.OnesCount(FamilySize) != 1 {
		panic(fmt.Sprintf("sandboxaddr: FamilySize %d is not 1<<%d, or is not a power of two",
			FamilySize, familyHostBits))
	}
}

// slot is where one sandbox sits: which family, where in it, and who
// created it.
type slot struct {
	family int
	offset int
	// parent is the sandbox that created this one, empty for a root.
	// Kept beside the family rather than inferred from the address,
	// because the address says which family a sandbox is in and says
	// nothing about the shape of it.
	parent string
}

// family is one block's occupancy.
type family struct {
	root string
	// byOffset is the live members, so a free offset is a gap in it.
	byOffset map[int]string
}

// Pool hands out addresses from one CIDR, in FamilySize-sized blocks.
//
// One Pool is one address space, which in practice is one sandbox host:
// a deployment with sandboxes on several hosts gives each its own Pool
// over its own CIDR, and that -- rather than anything here -- is what
// stops two hosts handing out the same address. Nothing in this package
// knows a host exists.
//
// Safe for concurrent use: Acquire and Release are what the reconcile
// loop and a dispatch call from different goroutines.
type Pool struct {
	prefix netip.Prefix
	// families is how many blocks this pool holds, counting from 1 --
	// see firstFamily.
	families int

	mu sync.Mutex
	// nextFamily is where the next acquire starts looking, relative to
	// firstFamily, so a pool with thousands of live families does not
	// rescan them all on every acquire.
	//
	// It also means a freed block is not handed straight back out, which
	// is worth having rather than worth fixing: an address reused the
	// instant its sandbox was destroyed can take delivery of traffic meant
	// for the dead one, while a neighbour's ARP cache and this host's
	// nftables rules still describe the old occupant. The block comes back
	// when the cursor wraps, or immediately if the pool is otherwise full.
	nextFamily int
	byName     map[string]slot
	occupied   map[int]*family
}

// firstFamily is the lowest family index handed out, and it is 1 rather
// than 0 so that the pool's own first block is never allocated.
//
// Whatever the pool's CIDR is configured on has infrastructure of its own
// in the low addresses: the network address itself, and the gateway a
// docker user-defined network puts at the first usable address. Reserving
// a whole block for them keeps every family uniformly FamilySize --
// a first family of 30 usable addresses instead of 32 would be an
// off-by-one waiting in every caller -- and costs 32 addresses out of a
// pool that holds thousands.
const firstFamily = 1

// maxFamilies bounds how many families a Pool will index, however large
// its prefix is. Sixteen million is past any fleet this orchestrates, and
// having a bound is what lets a family index be multiplied by FamilySize
// without reasoning about overflow on an IPv6 prefix.
const maxFamilies = 1 << 24

// NewPool returns a Pool over cidr.
//
// cidr must be large enough for at least one family beyond the reserved
// first block, and its prefix must leave the family blocks aligned --
// which any prefix at least familyHostBits short of the address width
// does.
func NewPool(cidr string) (*Pool, error) {
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		return nil, fmt.Errorf("sandboxaddr: parsing pool %q: %w", cidr, err)
	}
	prefix = prefix.Masked()
	hostBits := prefix.Addr().BitLen() - prefix.Bits()
	if hostBits < familyHostBits+1 {
		return nil, fmt.Errorf("sandboxaddr: pool %s has %d host bits, too few for a reserved first "+
			"block plus one family of %d addresses", cidr, hostBits, FamilySize)
	}
	// How many families the prefix could hold, capped rather than
	// computed exactly: an IPv6 /64 has 59 index bits, which is a number
	// no deployment will reach and which the index arithmetic below should
	// not have to carry. maxFamilies is far past any real fleet, so the cap
	// costs nothing and keeps every index small enough to multiply safely.
	families := maxFamilies
	if indexBits := hostBits - familyHostBits; indexBits < 31 && 1<<indexBits < families {
		families = 1 << indexBits
	}
	return &Pool{
		prefix:   prefix,
		families: families,
		byName:   map[string]slot{},
		occupied: map[int]*family{},
	}, nil
}

// Prefix is the whole pool, for whoever has to create the network these
// addresses live on.
func (p *Pool) Prefix() netip.Prefix { return p.prefix }

// Families is how many families this pool can hold at once, the reserved
// first block excluded.
func (p *Pool) Families() int { return p.families - firstFamily }

// AcquireRoot reserves a new family for sandbox and returns its address:
// the first address of that family's block.
//
// An already-known sandbox is an error rather than a second address,
// because the caller that asked twice has a bug and the second answer
// would quietly orphan the first.
func (p *Pool) AcquireRoot(sandbox string) (netip.Addr, error) {
	if sandbox == "" {
		return netip.Addr{}, fmt.Errorf("sandboxaddr: a sandbox needs a name")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, taken := p.byName[sandbox]; taken {
		return netip.Addr{}, fmt.Errorf("sandboxaddr: sandbox %q already has an address", sandbox)
	}
	// From a cursor rather than from the bottom each time: a pool holds
	// thousands of families and a scan from firstFamily would walk every
	// live one on every acquire. The wrap is what reuses a block a release
	// freed below the cursor.
	index := -1
	for tried := 0; tried < p.families-firstFamily; tried++ {
		candidate := firstFamily + (p.nextFamily+tried)%(p.families-firstFamily)
		if _, busy := p.occupied[candidate]; !busy {
			index = candidate
			p.nextFamily = (p.nextFamily + tried + 1) % (p.families - firstFamily)
			break
		}
	}
	if index < 0 {
		return netip.Addr{}, fmt.Errorf("sandboxaddr: pool %s is full -- all %d families of %d "+
			"addresses are in use", p.prefix, p.Families(), FamilySize)
	}
	p.occupied[index] = &family{root: sandbox, byOffset: map[int]string{0: sandbox}}
	p.byName[sandbox] = slot{family: index, offset: 0}
	return p.addrAt(index, 0), nil
}

// ReserveRoot reserves the family whose block starts at addr for
// sandbox, which already holds that address: a sandbox a previous process
// built and this one takes back (orchestrator.GranuleSandboxes.Readopt).
// A new process's pool starts empty, so without this it would hand the
// address out again beside the sandbox already using it.
//
// addr must be a family's first address, in a block this pool hands out,
// and that block must be free. Descendants the sandbox created are not
// reserved: nothing records which of the block's addresses they hold.
func (p *Pool) ReserveRoot(sandbox string, addr netip.Addr) error {
	if sandbox == "" {
		return fmt.Errorf("sandboxaddr: a sandbox needs a name")
	}
	if !p.prefix.Contains(addr) {
		return fmt.Errorf("sandboxaddr: %s is not in pool %s", addr, p.prefix)
	}
	index, offset, ok := p.indexOf(addr)
	if !ok || offset != 0 || index < firstFamily || index >= p.families {
		return fmt.Errorf("sandboxaddr: %s is not the first address of a family pool %s hands out", addr, p.prefix)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if s, taken := p.byName[sandbox]; taken {
		if s.family == index && s.offset == 0 {
			return nil
		}
		return fmt.Errorf("sandboxaddr: sandbox %q already has an address", sandbox)
	}
	if f, busy := p.occupied[index]; busy {
		return fmt.Errorf("sandboxaddr: %s is already the family of %q", addr, f.root)
	}
	p.occupied[index] = &family{root: sandbox, byOffset: map[int]string{0: sandbox}}
	p.byName[sandbox] = slot{family: index, offset: 0}
	return nil
}

// indexOf is addrAt's inverse for an address inside the pool's prefix:
// which family addr is in, and where in it. False for one beyond the
// families the pool indexes.
func (p *Pool) indexOf(addr netip.Addr) (index, offset int, ok bool) {
	base, a := p.prefix.Addr().As16(), addr.As16()
	// Inside the prefix, the two differ only in host bits; a difference
	// above the low 64 bits is past maxFamilies however it is read.
	if [8]byte(base[:8]) != [8]byte(a[:8]) {
		return 0, 0, false
	}
	diff := beUint64(a[8:]) - beUint64(base[8:])
	n := diff / FamilySize
	if n >= uint64(p.families) {
		return 0, 0, false
	}
	return int(n), int(diff % FamilySize), true
}

// beUint64 reads eight big-endian bytes.
func beUint64(b []byte) uint64 {
	var v uint64
	for _, c := range b[:8] {
		v = v<<8 | uint64(c)
	}
	return v
}

// AcquireChild reserves the next free address in parent's family for
// sandbox, which is a sandbox that parent created.
//
// The child lands in the parent's block however deep the parentage runs:
// a grandchild is in the same block as the root, because the block is the
// family rather than one generation of it.
func (p *Pool) AcquireChild(parent, sandbox string) (netip.Addr, error) {
	if sandbox == "" {
		return netip.Addr{}, fmt.Errorf("sandboxaddr: a sandbox needs a name")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	parentSlot, known := p.byName[parent]
	if !known {
		return netip.Addr{}, fmt.Errorf("sandboxaddr: %q has no address, so nothing can be created "+
			"beneath it", parent)
	}
	if _, taken := p.byName[sandbox]; taken {
		return netip.Addr{}, fmt.Errorf("sandboxaddr: sandbox %q already has an address", sandbox)
	}
	block := p.occupied[parentSlot.family]
	offset := -1
	for i := 1; i < FamilySize; i++ {
		if _, busy := block.byOffset[i]; !busy {
			offset = i
			break
		}
	}
	if offset < 0 {
		return netip.Addr{}, fmt.Errorf("sandboxaddr: the family rooted at %q holds %d sandboxes, "+
			"which is all of its block -- raising sandboxaddr.FamilySize is an address-layout change, "+
			"not a setting", block.root, FamilySize)
	}
	block.byOffset[offset] = sandbox
	p.byName[sandbox] = slot{family: parentSlot.family, offset: offset, parent: parent}
	return p.addrAt(parentSlot.family, offset), nil
}

// Addr is the address sandbox holds, and false for one this pool has
// never given an address to.
func (p *Pool) Addr(sandbox string) (netip.Addr, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.byName[sandbox]
	if !ok {
		return netip.Addr{}, false
	}
	return p.addrAt(s.family, s.offset), true
}

// FamilyPrefix is the one CIDR covering sandbox's whole family -- the
// block a firewall rule names to let a sandbox reach its own descendants
// and nothing else.
//
// It is the same answer for every member of a family, which is the point:
// a rule written for a root covers a child acquired afterwards without
// being rewritten.
func (p *Pool) FamilyPrefix(sandbox string) (netip.Prefix, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.byName[sandbox]
	if !ok {
		return netip.Prefix{}, false
	}
	return netip.PrefixFrom(p.addrAt(s.family, 0),
		p.prefix.Addr().BitLen()-familyHostBits), true
}

// Family is every live member of sandbox's family, root first and the
// rest in address order.
func (p *Pool) Family(sandbox string) ([]string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.byName[sandbox]
	if !ok {
		return nil, false
	}
	return p.membersLocked(s.family), true
}

// Descendants is sandbox and everything created beneath it, deepest
// first -- the order a caller tearing them down wants, so that nothing is
// destroyed before whatever it created.
//
// Siblings are not included: destroying a child takes that child's own
// descendants and leaves the rest of the family alone, which is what
// makes this different from Family above.
func (p *Pool) Descendants(sandbox string) ([]string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.byName[sandbox]; !ok {
		return nil, false
	}
	return p.descendantsLocked(sandbox), true
}

// Release frees sandbox and everything created beneath it, and reports
// what it freed in the order Descendants gives -- deepest first, so a
// caller can tear each one down as it goes.
//
// Releasing a root frees the whole family and its block. Releasing a
// child frees that child's subtree and leaves the block to its siblings.
// An unknown sandbox frees nothing and is not an error: Release is what a
// deferred teardown calls, and one that runs twice should be quiet rather
// than loud.
func (p *Pool) Release(sandbox string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, known := p.byName[sandbox]
	if !known {
		return nil
	}
	released := p.descendantsLocked(sandbox)
	block := p.occupied[s.family]
	for _, name := range released {
		delete(block.byOffset, p.byName[name].offset)
		delete(p.byName, name)
	}
	if len(block.byOffset) == 0 {
		delete(p.occupied, s.family)
	}
	return released
}

// descendantsLocked walks the parent links, deepest first.
func (p *Pool) descendantsLocked(sandbox string) []string {
	children := map[string][]string{}
	for name, s := range p.byName {
		if s.parent != "" {
			children[s.parent] = append(children[s.parent], name)
		}
	}
	var out []string
	var walk func(string)
	walk = func(name string) {
		kids := children[name]
		sort.Slice(kids, func(i, j int) bool {
			return p.byName[kids[i]].offset < p.byName[kids[j]].offset
		})
		for _, kid := range kids {
			walk(kid)
		}
		out = append(out, name)
	}
	walk(sandbox)
	return out
}

// membersLocked is a family's live members, root first.
func (p *Pool) membersLocked(index int) []string {
	block, ok := p.occupied[index]
	if !ok {
		return nil
	}
	offsets := make([]int, 0, len(block.byOffset))
	for offset := range block.byOffset {
		offsets = append(offsets, offset)
	}
	sort.Ints(offsets)
	out := make([]string, 0, len(offsets))
	for _, offset := range offsets {
		out = append(out, block.byOffset[offset])
	}
	return out
}

// addrAt is the address at one offset of one family block.
func (p *Pool) addrAt(index, offset int) netip.Addr {
	addr := p.prefix.Addr()
	return addrPlus(addr, uint64(index)*FamilySize+uint64(offset))
}

// addrPlus is addr advanced by n, which netip has no operator for.
//
// Done on the bytes rather than by calling Next() n times: n here is a
// family index times FamilySize, which on a /12 reaches two million, and
// an allocation should not walk two million addresses to answer.
//
// Overflow is not checked because it cannot happen: every caller derives n
// from an index below Pool.families, which NewPool computed from this same
// prefix's host bits.
func addrPlus(addr netip.Addr, n uint64) netip.Addr {
	if addr.Is4() {
		b := addr.As4()
		v := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
		v += uint32(n)
		return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
	}
	b := addr.As16()
	for i := 15; i >= 0 && n > 0; i-- {
		sum := uint64(b[i]) + n&0xff
		b[i] = byte(sum)
		n >>= 8
		n += sum >> 8
	}
	return netip.AddrFrom16(b)
}
