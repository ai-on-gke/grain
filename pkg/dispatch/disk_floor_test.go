package dispatch_test

// The agent half of grain/task-196: a cycle defers dispatch while the
// volume this deployment writes to is under its floor, and dispatches
// again once space is freed. The filesystem figures are stubbed --
// model.Budget carries the reading, and cmd/grain is what takes it.

import (
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/dispatch"
	"github.com/bwsalmon/grain/pkg/model"
)

// diskLimits is a deployment with room for three runs and a volume with
// freeMB left on it.
func diskLimits(freeMB int) model.Limits {
	return model.Limits{
		Workers:    3,
		AgentShape: model.Commitment{CPUs: 2, MemoryMB: 8 * 1024},
		Budget: model.Budget{
			Host:             model.Commitment{CPUs: 28, MemoryMB: 128 * 1024},
			ReserveMemoryMB:  16 * 1024,
			CPUOvercommit:    2,
			MemoryOvercommit: 1,
			DiskFloor:        model.DefaultDiskFloor(),
			Disk:             model.DiskSpace{Path: "/var/lib/grain/data", TotalMB: 443 * 1024, FreeMB: freeMB},
		},
	}
}

func TestCycleDefersDispatchWhileTheVolumeIsUnderItsDiskFloor(t *testing.T) {
	store, ctx := open(t)
	putTasks(t, store, ctx, task("t0", true), task("t1", true))

	// 20 GiB free of 443, against a floor of 44: nothing starts, though
	// the counts and the memory budget are both wide open.
	got, err := dispatch.Cycle(ctx, store, diskLimits(20*1024), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("dispatched %+v onto a volume 20 GiB from full, want nothing", got)
	}
	for _, id := range []string{"t0", "t1"} {
		if st, _ := store.State(ctx, id); st != model.StateQueued {
			t.Errorf("%s state = %q, want queued -- a deferred dispatch is not a failed one", id, st)
		}
	}

	// And they go the moment there is room again: the reading is taken
	// per cycle, so nothing has to be restarted to notice.
	got, err = dispatch.Cycle(ctx, store, diskLimits(300*1024), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("dispatched %d tasks once the volume was cleared, want 2: %+v", len(got), got)
	}
}

// The floor is where dispatch stops, not the last byte of the volume.
func TestCycleDispatchesOnAVolumeExactlyOnItsDiskFloor(t *testing.T) {
	store, ctx := open(t)
	putTasks(t, store, ctx, task("t0", true))

	floorMB := model.DefaultDiskFloor().FloorMB(443 * 1024)
	got, err := dispatch.Cycle(ctx, store, diskLimits(floorMB), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("dispatched %d tasks with the volume exactly on its floor, want 1", len(got))
	}
}

// What the deployment is told when it holds work back, so "nothing is
// starting" has a reason attached to it.
func TestSandboxVerdictSaysDiskWhenDiskIsWhatIsShort(t *testing.T) {
	verdict := diskLimits(20 * 1024).SandboxVerdict()
	if verdict.OK {
		t.Fatal("the agent budget admitted a run onto a volume under its floor")
	}
	if !strings.Contains(verdict.Reason, "disk:") || !strings.Contains(verdict.Reason, "/var/lib/grain/data") {
		t.Errorf("the refusal does not say which volume is short: %q", verdict.Reason)
	}
}
