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

package model

// VMMMemoryHeadroomPct and VMMMemoryHeadroomMinMB size the memory a
// granule VM's container cgroup is allowed beyond its guest's RAM: the
// larger of a quarter of the guest and 1 GiB.
//
// The headroom is not only cloud-hypervisor's own few hundred MiB. Its
// disk writes go through host page cache charged to the same cgroup, and
// there is no balloon, so a guest that has filled its RAM with page cache
// and then writes a multi-GB file (pack-guest's mke2fs) pushes the
// cgroup past guest + 512 MiB -- granule's own default -- and the kernel
// OOM-kills the VMM. The guest just sees its connection close. Dirty
// pages the cgroup has charged are written back and reclaimed within its
// limit, but only if the limit leaves room for the writeback in flight;
// a quarter of the guest does on every shape grain builds, and the 1 GiB
// floor covers the small ones where a quarter would not.
//
// Constants rather than deployment settings: every sandbox setting is a
// grain_config column with a migration, a state-repo binding and a
// Settings field, and this is a correctness margin rather than a policy
// anyone should need to tune. Whatever it comes to is charged to the
// budget (Budget.VMMHeadroom, Ledger.VMMHeadroom), so it never
// oversubscribes a host in exchange.
const (
	VMMMemoryHeadroomPct   = 25
	VMMMemoryHeadroomMinMB = 1024
)

// VMMHeadroom is the cgroup memory a VM sandbox is given, and charged,
// beyond its guest's RAM. The zero value adds nothing, which is what a
// backend with no VMM (a plain pod, a host directory) has.
type VMMHeadroom struct {
	Pct   int
	MinMB int
}

// DefaultVMMHeadroom is the headroom every granule backend applies.
func DefaultVMMHeadroom() VMMHeadroom {
	return VMMHeadroom{Pct: VMMMemoryHeadroomPct, MinMB: VMMMemoryHeadroomMinMB}
}

// IsZero reports whether h adds nothing.
func (h VMMHeadroom) IsZero() bool { return h.Pct <= 0 && h.MinMB <= 0 }

// MB is the headroom for a guest of guestMB: max(MinMB, Pct% of the
// guest). A guest of no memory -- a shape nothing has resolved -- has
// none, so a zero commitment stays zero.
func (h VMMHeadroom) MB(guestMB int) int {
	if guestMB <= 0 || h.IsZero() {
		return 0
	}
	return max(h.MinMB, guestMB*max(h.Pct, 0)/100)
}

// LimitMB is the cgroup memory limit for a guest of guestMB: guest plus
// MB, capped at capMB (the host's, or the largest node's, memory) when
// that is known. The cap never takes the limit below the guest itself,
// since a VM limited below its own RAM is OOM-killed by design.
func (h VMMHeadroom) LimitMB(guestMB, capMB int) int {
	limit := guestMB + h.MB(guestMB)
	if capMB > 0 && limit > capMB {
		limit = max(capMB, guestMB)
	}
	return limit
}

// Charge is c with its memory raised by the headroom of one sandbox of
// that shape -- what one VM sandbox of shape c really takes out of a
// budget. It is applied per sandbox, never to a sum of them: the floor
// makes the headroom of two sandboxes more than that of one twice the
// size.
func (h VMMHeadroom) Charge(c Commitment) Commitment {
	c.MemoryMB += h.MB(c.MemoryMB)
	return c
}
