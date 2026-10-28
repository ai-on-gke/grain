// Package guestimage names the guest half of a granule OCI image: the
// fixed set of files that make up a bootable machine, as opposed to the
// machinery that boots it.
//
// A granule image carries both halves in one artifact -- `docker run` on
// it boots a VM -- but they have different provenance and different
// lifetimes, and the Dockerfile's own "final" stage already builds it
// that way:
//
//	the VMM half    /usr/local/bin/granule-shim, /usr/local/bin/cloud-hypervisor,
//	                the entrypoint, netshim. Everything an isolation
//	                property rests on, and everything an upgrade of granule
//	                itself moves.
//	the guest half  /var/lib/granule/guest/. The machine that boots: a
//	                packed root disk, and optionally the kernel and
//	                initramfs that disk needs.
//
// The two are separable on purpose. A consumer that customizes a guest
// and publishes it wants a later granule or cloud-hypervisor fix without
// rebuilding, which is a `COPY --from` of the VMM half over their image;
// a deployment that runs somebody else's guest wants the isolation
// boundary to stay its own, which is the same copy read the other way.
// Either way the copy is only expressible if the guest half is a
// documented list of paths rather than "whatever happens to be in that
// directory", and that list is this package.
//
// Two properties of the paths are load-bearing rather than cosmetic, and
// are why they are constants here instead of defaults spelled out at
// each use:
//
//   - They are absolute paths resolved *inside* the VM container, which
//     is where cloud-hypervisor runs too. An overlay records its
//     backing files as paths, so a disk that moves between images only
//     keeps resolving if the convention puts it at the same absolute
//     path in both.
//   - The ".qcow2" and ".img" suffixes are functional: hypervisor.BuildArgs
//     dispatches on them to decide image_type and configuration.
//     Renaming either is a security-relevant change, not a cosmetic one.
//
// Agreeing on the paths is only half of what the two halves have to agree
// on. The other half -- which version of the guest-side contract the
// guest was built for, and what happens to a boot where that does not
// match what the VMM half implements -- is contract.go, in this package
// for the same reason: there should be one statement of the seam rather
// than one per caller.
package guestimage

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// Root is the directory granule keeps its own state under inside a VM
	// container. Guest and Overlay both live below it.
	Root = "/var/lib/granule"

	// Dir holds the guest half. Everything a `docker run` of this image
	// boots, and nothing else: no configuration, no per-VM state, and
	// nothing written at runtime (the overlay a running VM writes into
	// goes in OverlayDir instead, so that Dir stays the read-only,
	// publishable part).
	Dir = Root + "/guest"

	// DiskImage is the guest's root disk: a raw ext4 filesystem packed by
	// `mke2fs -d` (the Dockerfile's guest-image stage), never written to
	// in the default overlay disk mode, and shared between every VM on a
	// host that boots this image. CHV_DISK_IMAGE overrides it for a guest
	// kept somewhere else.
	DiskImage = Dir + "/disk.img"

	// DiskExtents and DiskExtentsManifest are the optional alternative to
	// DiskImage: the guest's root disk split into fixed-size raw extent files
	// plus a manifest (see pkg/extents, pkg/extent, and
	// docs/extent-storage-design.md), so that an image derived from this one
	// ships only the extents its edit changed. Used when DiskImage is absent,
	// when GRANULE_STORAGE_BACKEND=extents / CHV_STORAGE_BACKEND=extents is
	// selected, or when named by CHV_DISK_IMAGE.
	DiskExtents         = Dir + "/disk.extents"
	DiskExtentsManifest = DiskExtents + "/manifest.json"

	// SnapshotDir holds the VM snapshot state (RAM, device/vCPU registers,
	// and disk overlay) saved when the image is built with VM SNAPSHOT or
	// VM SET snapshot=on. At boot, if present, granule restores and resumes
	// from this directory.
	SnapshotDir = Dir + "/snapshot"

	// SnapshottedLabel is the OCI image label applied when an image carries
	// a saved snapshot state for resume.
	SnapshottedLabel = "dev.granule.guest.snapshotted"

	// VolumesLabel is the OCI image label recording in-guest volume mount points.
	VolumesLabel = "dev.granule.guest.volumes"

	// GuestKernel and GuestInitramfs are the guest's *own* kernel and
	// initramfs, present only when the image was built with
	// GUEST_KERNEL_PACKAGE -- a distro kernel installed into the rootfs,
	// for a guest whose workload needs more than RuntimeKernel provides.
	//
	// They are a pair: a vmlinuz without the matching initrd.img boots
	// without the modules and udev rules that initramfs carries, which
	// shows up as a guest that never becomes reachable rather than as a
	// boot failure. Present() and Check() treat them as one unit for
	// exactly that reason.
	GuestKernel    = Dir + "/vmlinuz"
	GuestInitramfs = Dir + "/initrd.img"

	// RuntimeKernel is the kernel granule builds and bakes in for itself
	// (the Dockerfile's build-kernel stage): a cloud-hypervisor/linux
	// ch_defconfig build, plus IPv6, with a PVH entry point and virtio
	// built in, matching
	// what the bundled DiskImage needs to boot. Unlike the three above it
	// belongs to the VMM half -- it moves when granule moves, and a guest
	// somebody else built is expected to boot under whatever version of
	// it the deployment currently ships.
	RuntimeKernel = Dir + "/vmlinux"

	// PartsDir is the other direction of the same seam: the guest-side
	// pieces a granule image carries *outside* any disk, so that packing
	// somebody else's rootfs can lay granule's own half over it. It holds
	// the overlay trees (overlay-common, overlay-debian, overlay-alpine
	// -- deploy/guest-image's, verbatim), the two static guest programs
	// granule-agent and granule-mem-agent, and PartsScript
	// below -- and, inside overlay-common, the contract declaration the
	// rest of it implements (ContractFile), which is how a pack lays
	// today's version down over whatever the rootfs it is packing
	// declared.
	//
	// It ships in the VMM half deliberately, rather than being carried
	// by granule or fetched separately. The agents talk to granule over
	// vsock and the units start them; taking them from the same image
	// that supplies /usr/local/bin/granule means the two cannot skew,
	// however old the granule driving the pack is. "The current
	// agents" is then the same statement as "the granule image you are
	// packing onto", which is also what makes a later rebase meaningful.
	PartsDir = "/usr/local/share/granule/guest-parts"

	// PartsRunner is the guest-side program `granule wrap` installs into a
	// wrapped guest: it runs the container the wrapped image carries, from
	// inside the booted VM (cmd/granule-container). It sits in PartsDir for
	// the same reason the agents do -- a wrapped guest takes it from the
	// granule image it is packed onto, so the runner and the granule that
	// built the image around it are one artifact.
	//
	// Unlike the agents, pack-guest does not lay it into every guest: a
	// guest with no container in it has nothing for it to run. The wrap
	// build copies it in itself.
	PartsRunner = PartsDir + "/granule-container"

	// PartsScript packs a rootfs directory into a guest half, given
	// PartsDir and somewhere to write: `pack-guest <rootfs> <parts>
	// <out>`. It is deploy/guest-image/pack-guest, shipped in the image
	// rather than generated, so `granule guest pack` drives a build
	// whose packing logic comes from the granule image it is packing onto
	// -- see pkg/guestpack.
	//
	// It is run by the packer image's own /bin/sh, and never by anything
	// out of the rootfs being packed.
	PartsScript = PartsDir + "/pack-guest"

	// OverlayDir and Overlay are where a running VM's writes land in the
	// default disk mode: a qcow2 overlay backed by DiskImage (or reflink copy on supported fs),
	// created per container by config.PrepareOverlay. Not part of the guest half --
	// it is per-VM state, it is written while the VM runs, and copying it
	// between images would mean copying one VM's writes into another
	// machine.
	OverlayDir     = Root + "/overlay"
	Overlay        = OverlayDir + "/disk.qcow2"
	OverlayExtents = OverlayDir + "/disk.extents"

	// VhostBlkSocket is the default Unix socket path where granule-shim
	// serves the in-process vhost-user-blk extent backend to Cloud-Hypervisor.
	VhostBlkSocket = "/run/granule/vhost-blk.sock"
)

// File is one file of the guest half, as something a caller can enumerate
// rather than something each caller has to know.
type File struct {
	// Path is the absolute path inside the VM container. This is both
	// where the file is read from and where a copy of it has to be
	// written: see the package comment on why the path is fixed.
	Path string

	// Required says whether a guest half is incomplete without it. Only
	// the root disk is; the kernel pair is present in some builds and
	// absent in others, and its absence means "this guest boots under the
	// deployment's own kernel", which is the ordinary case.
	Required bool

	// Description is what the file is, for an error message that names
	// the missing link rather than the missing path.
	Description string
}

// Files lists the guest half in the order a caller should copy it: the
// disk first, then the declaration describing that disk, then the kernel
// pair that has to accompany it.
//
// The declaration is not Required, because an image assembled before it
// existed does not have one and boots as ContractUndeclaredVersion (see
// bootcontract.go). It does have to move with the disk when it is there,
// though: it describes the guest in that disk and nothing else, so a copy
// that takes one without the other is a guest whose declaration is about
// somebody else's filesystem.
//
// RuntimeKernel is deliberately not in this list. It is the VMM half's,
// and copying it along with a guest is how a consumer's image pins a
// kernel the deployment has since replaced.
func Files() []File {
	return []File{
		{Path: DiskImage, Required: true, Description: "the packed guest root disk"},
		{Path: ContractSidecar, Description: "the guest contract version that disk declares"},
		{Path: GuestKernel, Description: "the guest's own kernel, when it brings one"},
		{Path: GuestInitramfs, Description: "the initramfs matching that kernel"},
	}
}

// Under is the same path relative to root, for a caller assembling a
// guest half somewhere other than the container it will eventually be
// read from -- a staging directory that becomes an image layer, a
// `docker build` context, an extracted image being inspected. root == ""
// or "/" gives the path back unchanged.
//
// The path an overlay or a CHV_* variable records is still the absolute one:
// a staging root is where the bytes are written now, not where they are
// found at boot.
func Under(root, path string) string {
	if root == "" || root == "/" {
		return path
	}
	return filepath.Join(root, strings.TrimPrefix(path, "/"))
}

// Present reports which of the guest half's files exist under root, with
// the kernel pair collapsed into the one answer that matters: hasKernel
// is true only when both halves of it are there, because config's own
// kernel selection takes the pair or neither.
//
// A path that exists but is not a regular file reads as absent, matching
// what config does with the same question: the fallback that answers
// selects the deployment's own kernel, which is a working guest rather
// than a failed boot.
func Present(root string) (hasDisk, hasKernel bool) {
	return (regularFile(Under(root, DiskImage)) || regularFile(Under(root, DiskExtentsManifest))),
		regularFile(Under(root, GuestKernel)) && regularFile(Under(root, GuestInitramfs))
}

// Check validates a guest half assembled under root -- by a pack, a
// rebase, or a hand-written Dockerfile copying files in -- and returns an
// error naming what is wrong with it.
//
// It is deliberately about the *set* of files rather than their contents:
// the failures it is here to catch are an assembly that forgot the disk,
// and a kernel copied in without its initramfs (or the reverse), both of
// which otherwise surface as a VM that boots and never answers. Whether
// the disk is a filesystem the guest can mount is not something a caller
// can be told without booting it, which is what a verify step is for.
//
// The one content it does look at is the contract sidecar, and only to
// the extent of insisting that one which is present is readable at all:
// an assembly is where that is cheap to fix, and a boot is where it is
// expensive to discover. Whether the version it states is one this granule
// can boot is BootContract.Check's question, not this one -- an assembly
// may legitimately be producing a guest for a different granule than the
// one assembling it.
func Check(root string) error {
	if err := CheckPresent(func(path string) bool {
		return regularFile(Under(root, path))
	}, func(path string) string { return Under(root, path) }); err != nil {
		return err
	}
	if _, err := ReadBootContract(Under(root, DiskImage)); err != nil {
		return err
	}
	return nil
}

// CheckPresent is Check against something that is not a directory this
// process can stat: has(path) answers "is there a regular file at this
// absolute guest-half path", and name(path) renders that path the way
// the caller's error messages should name it (Under(root, path) for a
// staging directory; the path itself, or "<image>:<path>", for an image).
//
// It exists for `granule guest rebase`, which checks the image it has
// just built by asking docker what is in it rather than by unpacking a
// multi-gigabyte disk onto the host -- see pkg/guestrebase. The
// rule being checked is the same either way, so there is one statement
// of it here and Check above is this function with a stat in it.
//
// It is presence only, which is why Check does the declaration parse
// itself rather than passing it down: has(path) answers a yes/no about a
// path, and asking docker whether a file is in an image says nothing
// about what is in the file. A rebase that wants the declaration read
// has to read it, which means unpacking, which is the cost CheckPresent
// exists to avoid.
func CheckPresent(has func(path string) bool, name func(path string) string) error {
	if name == nil {
		name = func(path string) string { return path }
	}
	if !has(DiskImage) && !has(DiskExtentsManifest) {
		return fmt.Errorf("%s is missing: a guest image needs %s", name(DiskImage), describe(DiskImage))
	}
	kernel, initramfs := has(GuestKernel), has(GuestInitramfs)
	if kernel != initramfs {
		missing, present := GuestInitramfs, GuestKernel
		if !kernel {
			missing, present = GuestKernel, GuestInitramfs
		}
		return fmt.Errorf("%s is present but %s is missing: a guest that brings its own kernel needs both, or the guest boots without the modules and udev rules the initramfs carries and never becomes reachable",
			name(present), name(missing))
	}
	return nil
}

// describe returns a listed file's Description, or the path itself for
// one that is not in the list.
func describe(path string) string {
	for _, f := range Files() {
		if f.Path == path {
			return f.Description
		}
	}
	return path
}

// regularFile reports whether path is present and is a regular file. Any
// error -- including a directory sitting where a file belongs -- reads as
// absent, so the answer is "granule cannot use what is there" rather than
// a distinction no caller acts on differently.
func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
