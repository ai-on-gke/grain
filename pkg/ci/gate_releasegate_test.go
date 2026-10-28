package ci

import (
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// PriorityReleaseGate (docs/design/release-from-trunk.md, RFT-2): the
// runs a trunk release is selected on rank above a merge-queue head and
// below a repair, may spend the repair reserve, and have a hole held for
// them from their first refusal. Driven through take directly, one
// attempt at a time, because each claim is about what one attempt
// decides.

func TestPriorityReleaseGateSitsBetweenAMergeHeadAndARepair(t *testing.T) {
	if !(PriorityMergeHead < PriorityReleaseGate && PriorityReleaseGate < PriorityRepair) {
		t.Fatalf("PriorityReleaseGate = %d, want it between PriorityMergeHead (%d) and PriorityRepair (%d)",
			PriorityReleaseGate, PriorityMergeHead, PriorityRepair)
	}
	if PriorityReleaseGate+PriorityUnderway >= PriorityRepair {
		t.Fatal("a release gate with jobs finished outranks a repair")
	}
	for _, p := range []Priority{PriorityNormal, PriorityUnderway, PriorityMergeHead, PriorityMergeHead + PriorityUnderway} {
		if p.MaySpendReserve() || p.ReservesAtOnce() {
			t.Fatalf("priority %d may spend the reserve or reserve at once, want only release gates and repairs", p)
		}
	}
	for _, p := range []Priority{PriorityReleaseGate, PriorityReleaseGate + PriorityUnderway, PriorityRepair} {
		if !p.MaySpendReserve() || !p.ReservesAtOnce() {
			t.Fatalf("priority %d may not spend the reserve or reserve at once", p)
		}
	}
}

func releaseGateTestJob() model.Commitment { return model.Commitment{CPUs: 4, MemoryMB: 16 * 1024} }

// A host with room for two jobs, one of which is the repair reserve: an
// ordinary job and a merge-queue head are refused the second, and a
// release gate is admitted into it.
func TestAReleaseGateMaySpendTheRepairReserve(t *testing.T) {
	gate := &SharedGate{Budget: func() model.Budget {
		return model.Budget{
			Host: model.Commitment{CPUs: 8, MemoryMB: 32 * 1024}, CPUOvercommit: 1, MemoryOvercommit: 1,
			RepairReserve: releaseGateTestJob(),
		}
	}}
	first := gate.join(releaseGateTestJob())
	if _, why, ok := gate.take(first, PriorityNormal); !ok {
		t.Fatalf("the first job was refused room outside the reserve: %s", why.Reason)
	}
	gate.leave(first)
	for _, p := range []Priority{PriorityNormal, PriorityMergeHead} {
		w := gate.join(releaseGateTestJob())
		if _, _, ok := gate.take(w, p); ok {
			t.Fatalf("a job at priority %d was admitted into the repair reserve", p)
		}
		gate.leave(w)
	}
	w := gate.join(releaseGateTestJob())
	if _, why, ok := gate.take(w, PriorityReleaseGate); !ok {
		t.Fatalf("a release gate was refused the repair reserve: %s", why.Reason)
	}
}

// A release gate the budget refuses holds the hole at once: a smaller job
// ranked below it that would fit waits, rather than spending the room the
// release gate is waiting for. A merge-queue head refused the same way
// holds nothing until ReserveAfter has passed.
func TestAReleaseGateHasItsHoleHeldFromTheFirstRefusal(t *testing.T) {
	for _, tc := range []struct {
		name    string
		rank    Priority
		reserve time.Duration
		held    bool
	}{
		{"release gate", PriorityReleaseGate, 0, true},
		{"merge head", PriorityMergeHead, 0, false},
		{"release gate with reservation off", PriorityReleaseGate, -1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Unix(1_800_000_000, 0)
			gate := &SharedGate{
				Budget: func() model.Budget {
					return model.Budget{
						Host: model.Commitment{CPUs: 8, MemoryMB: 32 * 1024}, CPUOvercommit: 1, MemoryOvercommit: 1,
					}
				},
				ReserveAfter: window(tc.reserve),
				Now:          func() time.Time { return now },
			}
			running := gate.join(model.Commitment{CPUs: 2, MemoryMB: 8 * 1024})
			if _, why, ok := gate.take(running, PriorityNormal); !ok {
				t.Fatalf("the running job was refused: %s", why.Reason)
			}
			gate.leave(running)
			big := gate.join(model.Commitment{CPUs: 8, MemoryMB: 32 * 1024})
			if _, _, ok := gate.take(big, tc.rank); ok {
				t.Fatal("a job larger than what is left was admitted")
			}
			small := gate.join(model.Commitment{CPUs: 1, MemoryMB: 4 * 1024})
			_, why, ok := gate.take(small, PriorityNormal)
			if tc.held && ok {
				t.Fatal("a small job was admitted into the hole a release gate is waiting for")
			}
			if !tc.held && !ok {
				t.Fatalf("a small job was held although nothing is reserving: %s", why.Reason)
			}
		})
	}
}

// Request.Priority is a floor under whatever the deployment ranks a run:
// a trunk release gate asks for PriorityReleaseGate, a merge-queue ranking
// of PriorityNormal does not lower it, and a repair's ranking still raises
// it.
func TestEngineHonoursTheRequestsOwnPriorityFloor(t *testing.T) {
	for _, tc := range []struct {
		name     string
		deployed func(Request) Priority
		want     Priority
	}{
		{"no deployment ranking", nil, PriorityReleaseGate},
		{"ranked lower by the deployment", func(Request) Priority { return PriorityNormal }, PriorityReleaseGate},
		{"ranked higher by the deployment", func(Request) Priority { return PriorityRepair }, PriorityRepair},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gate := &recordingGate{}
			plan := planFor(t, Request{Repo: "o/r", Ref: "main", Event: EventPush, Priority: PriorityReleaseGate},
				map[string]string{".grain/workflows/a.yml": "on: push\njobs:\n  only:\n    steps:\n      - run: a\n"})
			engine := &Engine{Sandboxes: &fakeSandboxes{}, Jobs: gate, Priority: tc.deployed}
			if run := engine.Run(context.Background(), "r1", plan, nil); run.Conclusion != ConclusionSuccess {
				t.Fatalf("conclusion: got %q (%s)", run.Conclusion, run.Error)
			}
			gate.mu.Lock()
			defer gate.mu.Unlock()
			if len(gate.ranks) != 1 || gate.ranks[0] != tc.want {
				t.Fatalf("the job asked at %v, want %d", gate.ranks, tc.want)
			}
		})
	}
}
