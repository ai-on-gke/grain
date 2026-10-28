package model

import (
	"strings"
	"testing"
)

func TestBudgetRepairReserveIsKeptForRepairs(t *testing.T) {
	b := Budget{
		Host: Commitment{CPUs: 8, MemoryMB: 16 * 1024}, CPUOvercommit: 1, MemoryOvercommit: 1,
		RepairReserve: Commitment{CPUs: 2, MemoryMB: 4096},
	}
	want := Commitment{CPUs: 2, MemoryMB: 4096}
	live := Commitment{CPUs: 4, MemoryMB: 8192}

	if v := b.Admits(live, want); !v.OK {
		t.Fatalf("ordinary admission with the reserve still clear refused: %s", v.Reason)
	}
	full := live.Plus(want)
	v := b.Admits(full, want)
	if v.OK {
		t.Fatal("an ordinary sandbox was admitted into the repair reserve")
	}
	if !strings.Contains(v.Reason, "repair reserve") {
		t.Errorf("refusal %q does not say part of the budget is the repair reserve", v.Reason)
	}
	if v := b.AdmitsRepair(full, want); !v.OK {
		t.Fatalf("a repair was refused the reserve kept for it: %s", v.Reason)
	}
	if v := b.AdmitsRepair(full.Plus(want), want); v.OK {
		t.Fatal("a repair was admitted past the whole budget")
	}
	if got := b.ReservedFor(); got != want {
		t.Errorf("ReservedFor = %v, want %v", got, want)
	}
	if !strings.Contains(b.Summary(), "kept for base-branch repairs") {
		t.Errorf("Summary %q does not mention the repair reserve", b.Summary())
	}
}

func TestBudgetRepairReserveNeverExceedsTheBudget(t *testing.T) {
	b := Budget{
		Host: Commitment{CPUs: 2, MemoryMB: 4096}, CPUOvercommit: 1, MemoryOvercommit: 1,
		RepairReserve: Commitment{CPUs: 8, MemoryMB: 64 * 1024},
	}
	if got, want := b.ReservedFor(), (Commitment{CPUs: 2, MemoryMB: 4096}); got != want {
		t.Errorf("ReservedFor = %v, want it capped at the budget %v", got, want)
	}
	// The Sole rule still holds: with nothing live, a sandbox runs alone.
	if v := b.Admits(Commitment{}, Commitment{CPUs: 1, MemoryMB: 1024}); !v.OK {
		t.Errorf("a sandbox on an empty host was refused: %s", v.Reason)
	}
}
