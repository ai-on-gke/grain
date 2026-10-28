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

//go:build linux || darwin

package sysstat

import (
	"fmt"
	"syscall"
)

// This file is both Linux and darwin because statfs(2) and stat(2) are
// the same two calls on both, spelled the same way in package syscall --
// the only thing that differs is the width of the fields they answer in
// (f_bsize is int64 on linux/amd64 and uint32 on darwin; st_dev is
// uint64 and int32), and the conversions below had to be explicit about
// that anyway. The /proc readings in sysstat_procfs.go are the part of
// this package that genuinely had to be written a second time for
// darwin; this part never did, and the //go:build linux it used to carry
// was the tag standing in for "the platform grain deploys onto" rather
// than for anything this code needs.

// DiskUsage reports the size and usage, in MiB, of the filesystem that
// holds path -- the same two numbers `df` prints for it, read with one
// statfs(2) rather than by shelling out.
//
// Used is total minus *free*, not total minus available: free counts the
// reserved blocks only root may allocate, and total-minus-free is what
// `df`'s own "Used" column reports. The daemon's own host disk filling
// up is a deployment-wide failure (every run's sandbox overlay is
// allocated out of it), so the number worth showing is the one an
// operator will see if they run `df` themselves.
//
// path is a path, not a device: statfs resolves it to whichever
// filesystem it falls on, so passing the daemon's -data-dir reports the
// disk that actually fills, wherever it happens to be mounted.
//
// On darwin the pair comes back as the APFS *container's* figures rather
// than the mounted volume's, which is worth knowing before anyone
// compares a reading from a mac against one from a deployment. Free space
// in APFS belongs to the container and every volume in it draws on the
// same pool, so f_blocks and f_bfree describe the container: a 250GB
// laptop reports 233752MB total and 144367MB used, counting the data
// volume, the VM volume, Preboot, Update and every snapshot. `df` prints a
// much smaller Used for the same path (12059MB on that machine) because it
// asks getattrlist for ATTR_VOL_SPACEUSED instead, which is the one
// volume's own share -- so the "same numbers `df` prints" above is exact
// on Linux and true only of the total and the free space on darwin.
//
// Keeping the container's figure is the right trade rather than a
// concession to what statfs happens to answer. What the host-stats pane
// and cmd/grain's disk floor guard against is the daemon running out of
// room to write, and the room it has is the container's: a volume
// reporting 12GB used inside a container with nothing left cannot
// allocate a byte, and a floor checked against the volume's own share
// would clear a sandbox onto a disk that is already full.
func DiskUsage(path string) (totalMB, usedMB int, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, fmt.Errorf("sysstat: statfs %s: %w", path, err)
	}
	// Bsize is the block size every count below is in; f_blocks/f_bfree
	// are counts of those blocks, so both have to be scaled by it before
	// they mean anything in bytes. The conversion is also what lets one
	// body serve both platforms: f_bsize is a signed 64-bit field on
	// linux/amd64 and an unsigned 32-bit one on darwin, so neither
	// platform's own type is the one the arithmetic wants.
	const mib = 1024 * 1024
	blockSize := uint64(st.Bsize)
	totalBytes := st.Blocks * blockSize
	usedBytes := (st.Blocks - st.Bfree) * blockSize
	return int(totalBytes / mib), int(usedBytes / mib), nil
}

// FilesystemID identifies the filesystem that holds path: st_dev from
// stat(2), the same number `du -x` and `find -xdev` compare to decide
// they have crossed onto another disk.
//
// It exists so a caller asking DiskUsage about several paths can tell
// which of them are one filesystem answering twice. grain's own
// -data-dir, -sandbox-dir and docker's data root are three separate
// volumes on one deployment and three names for one filesystem on the
// next, and only the kernel knows which (cmd/grain's own hostDisks). A
// path comparison cannot stand in for this, because the case that
// matters is a bind mount: terraform/gcp's /var/lib/grain/scratch and
// /mnt/grain-sandbox/sandboxes share every block and no prefix.
//
// On darwin this folds more paths together than a reader coming from
// Linux would expect, and it is right to: the sealed system volume and
// the data volume of one APFS volume group answer the same st_dev --
// firmlinks are what make / and /System/Volumes/Data one namespace -- so
// a caller asking about a path on each is told it has one filesystem.
// Which it has, in the only sense this is asked for: both draw their free
// space from the same container, so DiskUsage would answer twice with the
// same pair, and folding them is exactly the duplicate this exists to
// catch. A separate container (/System/Volumes/VM) or a network mount
// answers with a different st_dev, as it should.
//
// The value is opaque and worth nothing on its own -- it is only
// meaningful compared against another reading taken on this same machine
// at this same moment, and never worth storing. That is also what makes
// the conversion below safe on darwin, where dev_t is *signed*: a
// sign-extended st_dev would be a strange number to print, but it is the
// same strange number for both paths on one filesystem, and being equal
// to itself is the only property anything here reads.
func FilesystemID(path string) (uint64, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return 0, fmt.Errorf("sysstat: stat %s: %w", path, err)
	}
	return uint64(st.Dev), nil
}
