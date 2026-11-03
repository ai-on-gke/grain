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

import "testing"

func TestVMMHeadroomIsTheLargerOfAQuarterAndOneGiB(t *testing.T) {
	h := DefaultVMMHeadroom()
	for _, tc := range []struct{ guest, cap, headroom, limit int }{
		{guest: 0, headroom: 0, limit: 0},
		{guest: 2048, headroom: 1024, limit: 3072},
		{guest: 4096, headroom: 1024, limit: 5120},
		{guest: 8192, headroom: 2048, limit: 10240},
		{guest: 16384, headroom: 4096, limit: 20480},
		{guest: 32768, headroom: 8192, limit: 40960},
		// Capped at the host (or node), never below the guest itself.
		{guest: 16384, cap: 18000, headroom: 4096, limit: 18000},
		{guest: 16384, cap: 12000, headroom: 4096, limit: 16384},
		// A cap above the limit changes nothing.
		{guest: 8192, cap: 64 * 1024, headroom: 2048, limit: 10240},
	} {
		if got := h.MB(tc.guest); got != tc.headroom {
			t.Errorf("MB(%d) = %d, want %d", tc.guest, got, tc.headroom)
		}
		if got := h.LimitMB(tc.guest, tc.cap); got != tc.limit {
			t.Errorf("LimitMB(%d, cap %d) = %d, want %d", tc.guest, tc.cap, got, tc.limit)
		}
	}
	if got := (VMMHeadroom{}).LimitMB(8192, 0); got != 8192 {
		t.Errorf("zero headroom LimitMB(8192) = %d, want the guest alone", got)
	}
}

func TestBudgetChargesEachVMSandboxItsHeadroom(t *testing.T) {
	// 40 GiB of budget: two 16 GiB guests fit on guest memory alone, but
	// at 20 GiB of cgroup limit each, the second takes it to exactly 40
	// and a third 1 GiB guest (2 GiB charged) no longer fits.
	budget := NewBudget(Commitment{CPUs: 64, MemoryMB: 40 * 1024}, 0, 1, 1)
	budget.VMMHeadroom = DefaultVMMHeadroom()
	ledger := &Ledger{VMMHeadroom: DefaultVMMHeadroom()}
	guest := Commitment{CPUs: 4, MemoryMB: 16 * 1024}
	for i, id := range []string{"a", "b"} {
		if v, release := ledger.Admit(budget, PoolAgent, id, guest); !v.OK || release == nil {
			t.Fatalf("sandbox %d refused: %s", i, v.Reason)
		}
	}
	if got := ledger.Live().MemoryMB; got != 40*1024 {
		t.Errorf("live memory = %d MiB, want %d: each 16 GiB guest charged at its 20 GiB limit", got, 40*1024)
	}
	if v := ledger.Verdict(budget, Commitment{CPUs: 1, MemoryMB: 1024}); v.OK {
		t.Errorf("a third sandbox was admitted into a budget its cgroup limits already fill")
	}
	if got := ledger.Holders()[0].Shape.MemoryMB; got != 16*1024 {
		t.Errorf("holder shape = %d MiB, want the guest's own 16384: the headroom is added to sums, not filed", got)
	}

	// The same budget without headroom -- a backend with no VMM -- admits
	// the third, since nothing is charged beyond the shapes.
	plain := NewBudget(Commitment{CPUs: 64, MemoryMB: 40 * 1024}, 0, 1, 1)
	bare := &Ledger{}
	bare.Charge(PoolAgent, "a", guest)
	bare.Charge(PoolAgent, "b", guest)
	if v := bare.Verdict(plain, Commitment{CPUs: 1, MemoryMB: 1024}); !v.OK {
		t.Errorf("without headroom a third sandbox was refused: %s", v.Reason)
	}
}
