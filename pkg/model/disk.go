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

import "fmt"

// DiskSpace is one filesystem as it stands at a moment: what the volume
// this deployment writes to has, and what is still free on it.
//
// A reading rather than a setting, which is the whole difference between
// this and the memory half of Budget. Host memory is a fact about the
// machine, read once at startup and true until somebody reboots it into
// a bigger one; free disk is what the runs have written so far, and the
// only useful figure is the current one. Whoever builds a Budget takes
// the reading (cmd/grain's diskWatch, one statfs per admission), and the
// zero value -- no reading at all -- bounds nothing, the same way an
// unread host does.
type DiskSpace struct {
	// Path is the path the reading was taken through, named in the
	// refusal so an operator knows which volume to go and look at. Not
	// the mount point: statfs resolves a path to whichever filesystem it
	// falls on (sysstat.DiskUsage), and the path grain was configured
	// with is the one an operator recognises.
	Path string
	// TotalMB and FreeMB are that filesystem's size and free space in
	// MiB -- `df`'s own two numbers, free counting the blocks only root
	// may allocate, since that is what sysstat.DiskUsage reports and
	// being a little pessimistic about the last few hundred MiB of a
	// volume this is trying to keep off is not a mistake worth fixing.
	TotalMB int
	FreeMB  int
}

// Known reports whether this is a reading at all. False for the zero
// value, and for a path that could not be read.
func (d DiskSpace) Known() bool { return d.TotalMB > 0 }

// UsedMB is the other side of FreeMB, for a caller that would rather say
// how full the volume is.
func (d DiskSpace) UsedMB() int { return max(d.TotalMB-d.FreeMB, 0) }

// DiskFloor is how much of that volume this deployment keeps free: the
// disk half of a sandbox admission, and deliberately not the arithmetic
// the memory half uses (grain/task-196).
//
// Memory is admitted by adding up what the live sandboxes were promised
// and holding the sum against the machine. Disk cannot be, because a
// sandbox VM's disk is a qcow2 overlay that grows as it is written
// rather than a slice taken out of the volume when the VM is made
// (ui.HostCapacity says the same of max_workers x sandbox_disk_gb):
// summing the declared disk-gb of every live sandbox and refusing work
// when the sum passed the volume would refuse jobs on a host with plenty
// of room. Six 100 GB CI overlays on a 443 GB volume is an ordinary
// deployment, not an overcommitment. What fills the volume is what the
// runs write, so what bounds it is a floor under the free space rather
// than a ceiling over the promises.
//
// The failure it exists for: on this deployment's own host, on
// 2026-09-17, its data directory went from 70 GB to 181 GB of 443 GB in one
// hour with about a dozen sandboxes live, and filling that volume takes
// the store down with it.
//
// Proportional with an absolute minimum, for the reason
// DefaultReserveMemoryMB is: a bare constant is wrong on both a 100 GB
// volume and a 4 TB one. Capped as a share of the volume too, which the
// memory reserve has no need of -- a floor larger than the volume it is
// on would hold every job for ever, and a deployment wedged by its own
// safety margin is worse than the full disk it was avoiding.
//
// The zero value bounds nothing, the same contract Budget's own zero
// value has.
type DiskFloor struct {
	// Share is the fraction of the volume kept free. 0 leaves MinMB as
	// the whole floor.
	Share float64
	// MinMB is the least that is ever kept free, whatever Share comes to
	// on a small volume. 0 leaves Share as the whole floor.
	MinMB int
}

// The default floor, and the cap that keeps it from swallowing a small
// volume.
const (
	// DefaultDiskFloorShare is a tenth of the volume. On the 443 GB one
	// grain/task-196 was filed about that is 44 GB -- less than one CI
	// sandbox's declared disk, which is the point: the floor is there to
	// leave room for the store and for the writes already in flight, not
	// to guarantee the next job can fill its overlay.
	DefaultDiskFloorShare = 0.10
	// MinDiskFloorMB is 5 GiB, and it is what a small volume is really
	// bounded by: a tenth of a 20 GB store disk is 2 GB, which is not
	// enough room to notice a runaway write in, let alone to recover in.
	MinDiskFloorMB = 5 * 1024
	// MaxDiskFloorShare is the cap: never hold work over more than a
	// quarter of a volume. It is what keeps MinDiskFloorMB from wedging
	// a deployment whose store lives on a 10 GB disk, where 5 GiB would
	// otherwise be half of everything it has.
	MaxDiskFloorShare = 0.25
)

// DefaultDiskFloor is the floor a deployment that has not chosen one
// runs with.
func DefaultDiskFloor() DiskFloor {
	return DiskFloor{Share: DefaultDiskFloorShare, MinMB: MinDiskFloorMB}
}

// SandboxDiskFloor is this deployment's stored floor as the rule an
// admission is held against -- Config's own two columns, which is where
// the floor lives now that it is a setting rather than a pair of daemon
// flags (Config.SandboxDiskFloorShare, grain/task-372).
//
// One method so that the daemon's budget, the Settings pane's resolved
// figure and anything else reading the row cannot assemble the same two
// numbers differently.
func (c Config) SandboxDiskFloor() DiskFloor {
	return DiskFloor{Share: c.SandboxDiskFloorShare, MinMB: c.SandboxDiskFloorMinMB}
}

// DiskReclaimHeadroomShare is how far past the floor a reclaim sweeps:
// half as much again as the floor itself (grain/task-213).
//
// A sweep that stopped at exactly the floor would be a sweep the next
// write undoes. The whole reason the deployment is here is that
// something is writing hard enough to have crossed the floor while it
// was being watched, so freeing the last byte it was short of buys one
// admission and then holds work again -- and each of those crossings
// costs a `konturctl vm delete` per orphan and an emptied job cache. A
// target above the floor is what makes one sweep worth the crossing that
// triggered it.
//
// A half rather than a multiple: on the volume this was filed about the
// floor is 44 GiB, so the target is 66 GiB of 443 -- enough for the
// writes already in flight to have somewhere to go, and still under a
// sixth of the volume. It is a share of the floor rather than of the
// volume so that the cap on the floor caps this too: at most 37.5% of a
// volume is ever swept for, whatever the floor was set to.
const DiskReclaimHeadroomShare = 0.5

// ReclaimTargetMB is how much free space a reclaim sweeps back to on a
// volume of this size, and 0 where there is nothing to sweep for.
//
// Nothing guarantees a sweep reaches it. It is a stopping rule, not a
// promise: a reclaim runs its steps in order and stops at the first
// reading that is over this, so on a deployment whose disk is full of
// live sandboxes it does everything it can and says it is still short.
func (f DiskFloor) ReclaimTargetMB(totalMB int) int {
	floorMB := f.FloorMB(totalMB)
	if floorMB <= 0 {
		return 0
	}
	return floorMB + int(float64(floorMB)*DiskReclaimHeadroomShare)
}

// NeedsReclaim reports whether this reading is one a reclaim has work to
// do about: under the target, rather than under the floor.
//
// The two differ on purpose in the window between them. A deployment
// that is over its floor but under its target is admitting work
// perfectly happily, and a sweep started there would be reclaiming
// against a problem nobody has; a deployment under the floor is holding
// work and is swept until it is over the target. So the caller that asks
// this is the one deciding whether to keep sweeping, and Admits is the
// one deciding whether to admit -- which is why a half-finished sweep
// leaves a deployment that is running again rather than one still held.
func (f DiskFloor) NeedsReclaim(d DiskSpace) bool {
	target := f.ReclaimTargetMB(d.TotalMB)
	return d.Known() && target > 0 && d.FreeMB < target
}

// Known reports whether this floor holds anything back at all.
func (f DiskFloor) Known() bool { return f.Share > 0 || f.MinMB > 0 }

// FloorMB is how much free space this floor keeps on a volume of this
// size: the larger of the share and the minimum, and never more than
// MaxDiskFloorShare of the volume.
func (f DiskFloor) FloorMB(totalMB int) int {
	if totalMB <= 0 || !f.Known() {
		return 0
	}
	floor := max(f.MinMB, 0)
	if f.Share > 0 {
		floor = max(floor, int(float64(totalMB)*f.Share))
	}
	return min(floor, int(float64(totalMB)*MaxDiskFloorShare))
}

// Admits reports whether there is room on this volume for more work to
// start, and says what is short when there is not.
//
// A reading this floor cannot judge -- no floor configured, or no
// reading taken -- admits everything, which is what a deployment that
// cannot see its own disk did before this existed.
//
// There is no counterpart to BudgetVerdict.Sole here, deliberately: a
// sandbox bigger than the whole memory budget is admitted when nothing
// else holds it, because waiting cannot make room that the budget was
// never going to have, but a volume with no room left has nothing to
// give whoever is alone on it either. Running anyway is how the store
// ends up on a full disk. What keeps that from being a permanent wedge
// is reclaiming space, not admitting the job.
func (f DiskFloor) Admits(d DiskSpace) BudgetVerdict {
	floorMB := f.FloorMB(d.TotalMB)
	if !d.Known() || floorMB <= 0 || d.FreeMB >= floorMB {
		return BudgetVerdict{OK: true}
	}
	return BudgetVerdict{Reason: fmt.Sprintf(
		"disk: %s GiB is free on %s, and this deployment holds work below %s GiB free of the volume's %s GiB "+
			"-- a sandbox's disk grows as it is written, so what is short is room for the runs already going",
		gib(d.FreeMB), d.Path, gib(floorMB), gib(d.TotalMB))}
}

// Describe is this floor as one line of prose for a startup log: what it
// comes to on the volume that was read.
func (f DiskFloor) Describe(d DiskSpace) string {
	if !f.Known() {
		return "no disk floor, so nothing holds work back as the volume fills"
	}
	if !d.Known() {
		return fmt.Sprintf("a disk floor of %.0f%% of the volume and never less than %s GiB, "+
			"on a volume that could not be read", f.Share*100, gib(f.MinMB))
	}
	return fmt.Sprintf("%s GiB free of %s GiB on %s, against a floor of %s GiB "+
		"(%.0f%% of the volume, never less than %s GiB and never more than %.0f%% of it)",
		gib(d.FreeMB), gib(d.TotalMB), d.Path, gib(f.FloorMB(d.TotalMB)),
		f.Share*100, gib(f.MinMB), MaxDiskFloorShare*100)
}
