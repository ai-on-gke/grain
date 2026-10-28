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

package model_test

import (
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// The floor is a share of the volume with an absolute minimum, for the
// reason the memory reserve is: a constant is wrong at both ends of the
// range of disks a deployment is run on.
func TestDiskFloorScalesWithTheVolumeAndNeverGoesUnderTheMinimum(t *testing.T) {
	floor := model.DefaultDiskFloor()
	for _, c := range []struct {
		name    string
		totalMB int
		wantMB  int
	}{
		// The 443 GB volume grain/task-196 was filed about: a tenth.
		{"the host this was filed about", 443 * 1024, 45363},
		// A 4 TB volume is still a tenth rather than a token amount.
		{"a big volume", 4096 * 1024, 419430},
		// A tenth of a 20 GB store disk is 2 GB, which is not room to
		// recover in: the minimum is what binds.
		{"a small store volume", 20 * 1024, model.MinDiskFloorMB},
		// And on a volume small enough that the minimum would be most of
		// it, the cap is what binds -- a floor is not supposed to be a
		// wedge.
		{"a volume smaller than the minimum is worth", 10 * 1024, 2560},
		{"no reading", 0, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := floor.FloorMB(c.totalMB); got != c.wantMB {
				t.Errorf("FloorMB(%d) = %d, want %d", c.totalMB, got, c.wantMB)
			}
		})
	}
}

// The floor is a floor: work stops at it rather than at the last byte of
// the volume, which is the whole difference between holding a job and
// discovering a full disk.
func TestDiskFloorIsWhereAdmissionStopsRatherThanTheLastByte(t *testing.T) {
	floor := model.DefaultDiskFloor()
	const totalMB = 443 * 1024
	floorMB := floor.FloorMB(totalMB)

	for _, c := range []struct {
		name   string
		freeMB int
		admits bool
	}{
		{"well clear of it", floorMB * 3, true},
		{"exactly on it", floorMB, true},
		{"one MiB under it", floorMB - 1, false},
		{"most of the way through it", floorMB / 2, false},
		{"nothing left at all", 0, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			space := model.DiskSpace{Path: "/var/lib/grain/data", TotalMB: totalMB, FreeMB: c.freeMB}
			verdict := floor.Admits(space)
			if verdict.OK != c.admits {
				t.Fatalf("Admits(%d free of %d, floor %d).OK = %v, want %v: %s",
					c.freeMB, totalMB, floorMB, verdict.OK, c.admits, verdict.Reason)
			}
			if verdict.OK {
				return
			}
			if !strings.HasPrefix(verdict.Reason, "disk:") {
				t.Errorf("a refusal for disk says so first: %q", verdict.Reason)
			}
			if !strings.Contains(verdict.Reason, "/var/lib/grain/data") {
				t.Errorf("a refusal names the volume to go and look at: %q", verdict.Reason)
			}
		})
	}
}

// A deployment that cannot see its own disk is bounded by what it always
// was, rather than admitting nothing: this limits how much work runs, it
// is not a precondition for running any.
func TestDiskFloorAdmitsEverythingWithNoFloorOrNoReading(t *testing.T) {
	full := model.DiskSpace{Path: "/var/lib/grain/data", TotalMB: 100 * 1024, FreeMB: 1}
	if !(model.DiskFloor{}).Admits(full).OK {
		t.Error("a floor nobody configured refused a job")
	}
	if !model.DefaultDiskFloor().Admits(model.DiskSpace{}).OK {
		t.Error("a volume that could not be read refused a job")
	}
	if !model.DefaultDiskFloor().Admits(model.DiskSpace{Path: "/x", TotalMB: 0, FreeMB: 0}).OK {
		t.Error("a reading of nothing refused a job")
	}
}

// The budget's other two dimensions go on applying exactly as they did:
// the disk floor is a third bound, not a replacement for either.
func TestBudgetHoldsAJobForDiskBeforeItLooksAtAnythingElse(t *testing.T) {
	budget := model.Budget{
		Host:             model.Commitment{CPUs: 28, MemoryMB: 128 * 1024},
		ReserveMemoryMB:  16 * 1024,
		CPUOvercommit:    2,
		MemoryOvercommit: 1,
		DiskFloor:        model.DefaultDiskFloor(),
	}
	small := model.Commitment{CPUs: 2, MemoryMB: 4 * 1024}

	roomy := budget
	roomy.Disk = model.DiskSpace{Path: "/var/lib/grain/data", TotalMB: 443 * 1024, FreeMB: 300 * 1024}
	if verdict := roomy.Admits(model.Commitment{}, small); !verdict.OK {
		t.Fatalf("a job on an empty host and an empty disk was held: %s", verdict.Reason)
	}

	filling := budget
	filling.Disk = model.DiskSpace{Path: "/var/lib/grain/data", TotalMB: 443 * 1024, FreeMB: 20 * 1024}
	verdict := filling.Admits(model.Commitment{}, small)
	if verdict.OK {
		t.Fatal("a job was admitted onto a volume under its floor")
	}
	if !strings.Contains(verdict.Reason, "disk:") {
		t.Errorf("the refusal reads as memory rather than disk: %q", verdict.Reason)
	}
	// And no Sole escape hatch: a sandbox bigger than the memory budget
	// runs alone because waiting cannot make room that was never coming,
	// but a full volume has nothing to give whoever is alone on it
	// either.
	if verdict.Sole {
		t.Error("a full disk admitted a job because nothing else held it")
	}

	// Memory still binds where the disk is fine.
	huge := model.Commitment{CPUs: 4, MemoryMB: 100 * 1024}
	if verdict := roomy.Admits(model.Commitment{CPUs: 4, MemoryMB: 100 * 1024}, huge); verdict.OK {
		t.Error("the memory budget stopped applying once a disk floor was configured")
	}
}

// A backend that applies no shape at all commits no memory this can add
// up -- and its runs still write to the volume.
func TestBudgetHoldsAShapelessSandboxForDiskAlone(t *testing.T) {
	budget := model.Budget{
		DiskFloor: model.DefaultDiskFloor(),
		Disk:      model.DiskSpace{Path: "/var/lib/grain/data", TotalMB: 100 * 1024, FreeMB: 1024},
	}
	if !budget.Known() {
		t.Fatal("a budget with a floor and a reading claims to bound nothing")
	}
	if verdict := budget.Admits(model.Commitment{}, model.Commitment{}); verdict.OK {
		t.Error("a sandbox with no shape was admitted onto a full volume")
	}

	limits := model.Limits{Workers: 3, Budget: budget}
	if limits.SandboxAdmits() {
		t.Error("an agent run was admitted onto a full volume")
	}
	if verdict := limits.SandboxVerdict(); verdict.OK || !strings.Contains(verdict.Reason, "disk") {
		t.Errorf("the deployment's own verdict does not say disk: %+v", verdict)
	}
}

// Both pools are held by the same floor on the same volume: free space
// is not something to give the agent pool a share of.
func TestCIAndAgentsShareTheSameDiskFloor(t *testing.T) {
	limits := model.Limits{
		Workers: 2, CIJobs: 4,
		AgentShape: model.Commitment{CPUs: 2, MemoryMB: 8 * 1024},
		Budget: model.Budget{
			Host:             model.Commitment{CPUs: 28, MemoryMB: 128 * 1024},
			CPUOvercommit:    2,
			MemoryOvercommit: 1,
			DiskFloor:        model.DefaultDiskFloor(),
			Disk:             model.DiskSpace{Path: "/var/lib/grain/data", TotalMB: 443 * 1024, FreeMB: 20 * 1024},
		},
	}
	// One budget, so necessarily one volume and one floor: CI is admitted
	// against the same Budget value dispatch is (grain/task-28).
	if verdict := limits.Budget.Admits(model.Commitment{}, model.Commitment{CPUs: 2, MemoryMB: 4 * 1024}); verdict.OK {
		t.Error("a CI job was admitted onto a volume the agent pool is held off")
	}
	if limits.SandboxAdmits() {
		t.Error("an agent run was admitted onto the same full volume")
	}
}

// The startup log has a figure to print, on a deployment with a floor
// and on one without.
func TestDiskFloorDescribesItself(t *testing.T) {
	space := model.DiskSpace{Path: "/var/lib/grain/data", TotalMB: 443 * 1024, FreeMB: 300 * 1024}
	got := model.DefaultDiskFloor().Describe(space)
	for _, want := range []string{"/var/lib/grain/data", "443.0", "300.0", "44.3"} {
		if !strings.Contains(got, want) {
			t.Errorf("Describe = %q, want it to mention %q", got, want)
		}
	}
	if got := (model.DiskFloor{}).Describe(space); !strings.Contains(got, "no disk floor") {
		t.Errorf("a deployment with no floor describes itself as %q", got)
	}
	if got := model.DefaultDiskFloor().Describe(model.DiskSpace{}); !strings.Contains(got, "could not be read") {
		t.Errorf("an unread volume describes itself as %q", got)
	}
}

// A reclaim sweeps back to a target above the floor rather than to the
// floor itself: stopping on the floor is stopping at the reading the
// next write undoes (grain/task-213).
func TestDiskReclaimSweepsPastTheFloorRatherThanToIt(t *testing.T) {
	floor := model.DefaultDiskFloor()
	const totalMB = 443 * 1024
	floorMB, targetMB := floor.FloorMB(totalMB), floor.ReclaimTargetMB(totalMB)

	if targetMB <= floorMB {
		t.Fatalf("the target %d is not above the floor %d, so a sweep buys one admission", targetMB, floorMB)
	}
	if want := floorMB + floorMB/2; targetMB != want {
		t.Errorf("ReclaimTargetMB(%d) = %d, want the floor and half as much again, %d", totalMB, targetMB, want)
	}
	// Whatever the floor is set to, the cap on the floor caps the target
	// with it: no deployment sweeps for more than 37.5% of its volume.
	huge := model.DiskFloor{Share: 0.9, MinMB: 10 << 20}
	if got, limit := huge.ReclaimTargetMB(totalMB), int(float64(totalMB)*model.MaxDiskFloorShare*1.5); got > limit {
		t.Errorf("a floor set to 90%% sweeps for %d MiB of a %d MiB volume, past the %d the cap allows", got, totalMB, limit)
	}
	// Nothing to sweep for where nothing holds work back.
	if got := (model.DiskFloor{}).ReclaimTargetMB(totalMB); got != 0 {
		t.Errorf("a deployment with no floor sweeps for %d MiB", got)
	}
	if got := floor.ReclaimTargetMB(0); got != 0 {
		t.Errorf("an unread volume sweeps for %d MiB", got)
	}
}

// Sweeping and admitting are two questions with two answers, and the gap
// between them is deliberate: a deployment over its floor and under its
// target is running, and is still worth another step of reclaim.
func TestNeedsReclaimGoesOnPastTheReadingThatAdmitsAgain(t *testing.T) {
	floor := model.DefaultDiskFloor()
	const totalMB = 443 * 1024
	floorMB, targetMB := floor.FloorMB(totalMB), floor.ReclaimTargetMB(totalMB)

	for _, c := range []struct {
		name          string
		freeMB        int
		admits, sweep bool
	}{
		{"under the floor", floorMB - 1, false, true},
		{"back over the floor but short of the target", floorMB + 1, true, true},
		{"exactly on the target", targetMB, true, false},
		{"well clear", targetMB * 2, true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			space := model.DiskSpace{Path: "/var/lib/grain/data", TotalMB: totalMB, FreeMB: c.freeMB}
			if got := floor.Admits(space).OK; got != c.admits {
				t.Errorf("Admits = %v, want %v", got, c.admits)
			}
			if got := floor.NeedsReclaim(space); got != c.sweep {
				t.Errorf("NeedsReclaim = %v, want %v", got, c.sweep)
			}
		})
	}

	// Nothing to reclaim for on a deployment that holds nothing back, or
	// on a volume nobody could read: the sweep deletes files, and a
	// reading it cannot judge is not a reason to start deleting.
	if (model.DiskFloor{}).NeedsReclaim(model.DiskSpace{Path: "/x", TotalMB: totalMB, FreeMB: 1}) {
		t.Error("a deployment with no floor swept anyway")
	}
	if floor.NeedsReclaim(model.DiskSpace{}) {
		t.Error("a volume that could not be read swept anyway")
	}
}
