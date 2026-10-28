package hypervisor

import (
	"fmt"
	"os"
	"strings"

	"github.com/bwsalmon/granule/pkg/config"
	"github.com/bwsalmon/granule/pkg/qcow2"
)

// SnapshotExists reports whether path already holds a complete VM
// snapshot for BuildArgs to restore from (see its "--restore" branch
// below) rather than booting fresh. Runner.Suspend only ever makes a
// snapshot visible at its final path by renaming a finished staging
// directory into place, so simple existence is enough to know it's
// complete.
func SnapshotExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// BuildArgs turns a Config into the argv for the cloud-hypervisor binary.
//
// It always boots and configures the VM in one invocation (no separate
// vm.create/vm.boot API calls) so there is nothing to do at startup beyond
// exec'ing the process: the disk image is assumed to already be present on
// the local filesystem, so there is no image fetch on the hot path either.
func BuildArgs(cfg config.Config) ([]string, error) {
	var args []string

	args = append(args, "--api-socket", "path="+cfg.APISocket)

	if SnapshotExists(cfg.SnapshotPath) {
		// A restored VM's CPUs, memory, disks, kernel/firmware, net and
		// console are all replayed from the snapshot's own config.json
		// rather than given again here -- see Runner.Suspend and
		// cloud-hypervisor's snapshot/restore docs. "resume=true" starts
		// it running immediately, matching the running state it was in
		// right before Suspend paused it to take the snapshot.
		args = append(args, "--restore", "source_url=file://"+cfg.SnapshotPath+",resume=true")
		return args, nil
	}

	args = append(args, "--cpus", cpusArg(cfg))
	args = append(args, "--memory", memoryArg(cfg))

	for _, d := range cfg.Disks {
		// cloud-hypervisor is told each disk's format explicitly rather
		// than left to detect it, because its own auto-detection gets
		// both of the two kinds of disk this runtime hands it wrong.
		//
		// A raw image -- the bundled guest disk (config.defaultDiskImage)
		// and anything else CHV_DISK_IMAGE points at -- needs
		// image_type=raw, or cloud-hypervisor refuses the first write to
		// sector 0 ("Attempting to write to sector 0 on a disk without
		// specifying image_type"), which fails the guest's
		// root-filesystem mount before it ever reaches userspace.
		//
		// A qcow2 image -- the per-VM writable overlay this container
		// creates for -disk-mode=overlay (see config.PrepareOverlay),
		// backed by the shared read-only source image -- needs
		// image_type=qcow2 *and* backing_files=on.
		//
		// sparse=on is what lets a guest give space back: with it
		// cloud-hypervisor advertises VIRTIO_BLK_F_DISCARD, and a
		// guest's "fstrim" becomes a hole punched in the image (raw)
		// or unmapped clusters (qcow2).
		//
		// The content decides, not the name: config.PrepareOverlay leaves
		// a raw reflink clone at guestimage.Overlay (disk.qcow2) whenever
		// the overlay directory's filesystem supports reflinks (XFS,
		// btrfs), and calling that qcow2 has cloud-hypervisor refuse to
		// boot (DiskImageTypeMismatch). The suffix is only a guess for a
		// path that cannot be read yet.
		extra := ",image_type=raw,sparse=on"
		if isQcow2Disk(d.Path) {
			extra = ",image_type=qcow2,backing_files=on,sparse=on"
		}
		numQueues := d.NumQueues
		if numQueues == 0 {
			numQueues = cfg.DiskNumQueues
		}
		if numQueues > 0 {
			extra += fmt.Sprintf(",num_queues=%d", numQueues)
		}
		if d.Serial != "" {
			extra += ",serial=" + d.Serial
		}
		args = append(args, "--disk", fmt.Sprintf("path=%s,readonly=%s%s", d.Path, onOff(d.ReadOnly), extra))
	}

	if cfg.Kernel != "" {
		args = append(args, "--kernel", cfg.Kernel)
		if cfg.Initramfs != "" {
			args = append(args, "--initramfs", cfg.Initramfs)
		}
		if cfg.Cmdline != "" {
			args = append(args, "--cmdline", cfg.Cmdline)
		}
	} else {
		args = append(args, "--firmware", cfg.Firmware)
	}

	for _, n := range cfg.Nets {
		args = append(args, "--net", n)
	}

	for _, fs := range cfg.Filesystems {
		var parts []string
		parts = append(parts, "tag="+fs.Tag)
		if fs.Socket != "" {
			parts = append(parts, "socket="+fs.Socket)
		}
		if fs.SharedDir != "" {
			parts = append(parts, "shared_dir="+fs.SharedDir)
		}
		if fs.ReadOnly {
			parts = append(parts, "readonly=on")
		}
		args = append(args, "--fs", strings.Join(parts, ","))
	}

	// Route the guest's serial console to our own stdio (so it shows up
	// under `kubectl logs`) and disable the redundant virtio-console.
	args = append(args, "--serial", "tty")
	args = append(args, "--console", "off")

	// The vsock device "granule exec" reaches the guest's granule-agent
	// over. The host end is a unix socket cloud-hypervisor creates at
	// this path ("hybrid" vsock, the same shape firecracker's is), which
	// is why exec needs nothing addressable and nothing routed.
	if cfg.VsockSocket != "" {
		args = append(args, "--vsock", fmt.Sprintf("cid=%d,socket=%s", cfg.VsockCID, cfg.VsockSocket))
	}

	args = append(args, cfg.ExtraArgs...)

	return args, nil
}

// memoryArg builds --memory's value: a fixed starting size, plus (when
// MemoryHotplug is set and there's actually room to grow into) a
// virtio-mem hotplug device sized for growth up to MemoryMaxMB.
func memoryArg(cfg config.Config) string {
	arg := fmt.Sprintf("size=%dM,shared=%s", cfg.MemoryMB, onOff(cfg.MemoryShared))
	if cfg.MemoryHotplug && cfg.MemoryMaxMB > cfg.MemoryMB {
		arg += fmt.Sprintf(",hotplug_method=virtio-mem,hotplug_size=%dM", cfg.MemoryMaxMB-cfg.MemoryMB)
	}
	return arg
}

// cpusArg builds --cpus's value: a fixed boot count, plus (when CPUsMax
// is greater than CPUs) a "max=" ceiling that turns on cloud-
// hypervisor's ACPI-based CPU hotplug for growth up to CPUsMax.
func cpusArg(cfg config.Config) string {
	arg := fmt.Sprintf("boot=%d", cfg.CPUs)
	if cfg.CPUsMax > cfg.CPUs {
		arg += fmt.Sprintf(",max=%d", cfg.CPUsMax)
	}
	return arg
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// String renders argv the way it would be typed on a shell command line,
// for logging.
func String(binary string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, binary)
	for _, a := range args {
		if strings.ContainsAny(a, " \t\"'") {
			parts = append(parts, fmt.Sprintf("%q", a))
		} else {
			parts = append(parts, a)
		}
	}
	return strings.Join(parts, " ")
}

// isQcow2Disk reports whether path holds a qcow2 image, by its header when
// the file can be read and by its ".qcow2" suffix only when it cannot.
func isQcow2Disk(path string) bool {
	if _, err := os.Stat(path); err == nil {
		return qcow2.IsQcow2(path)
	}
	return strings.HasSuffix(path, ".qcow2")
}
