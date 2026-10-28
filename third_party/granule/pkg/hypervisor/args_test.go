package hypervisor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/granule/pkg/config"
	"github.com/bwsalmon/granule/pkg/qcow2"
)

func baseConfig() config.Config {
	return config.Config{
		Disks:           []config.Disk{{Path: "/data/disk.img"}},
		Firmware:        "/opt/firmware/CLOUDHV.fd",
		CPUs:            2,
		MemoryMB:        2048,
		MemoryShared:    true,
		APISocket:       "/run/cloud-hypervisor/api.sock",
		BinaryPath:      "/usr/local/bin/cloud-hypervisor",
		ShutdownTimeout: 20 * time.Second,
	}
}

// mustBuildArgs is BuildArgs for the tests that expect it to succeed --
// which is all of them bar the ones checking what it refuses.
func mustBuildArgs(t *testing.T, cfg config.Config) []string {
	t.Helper()
	args, err := BuildArgs(cfg)
	if err != nil {
		t.Fatalf("BuildArgs: %v", err)
	}
	return args
}

// argValue returns the value that follows a --flag in args, failing the
// test if the flag is absent.
func argValue(t *testing.T, args []string, flag string) string {
	t.Helper()
	for i, a := range args {
		if a == flag {
			if i+1 >= len(args) {
				t.Fatalf("flag %s has no value in %v", flag, args)
			}
			return args[i+1]
		}
	}
	t.Fatalf("flag %s not found in %v", flag, args)
	return ""
}

func TestBuildArgs_FirmwareBoot(t *testing.T) {
	cfg := baseConfig()
	args := mustBuildArgs(t, cfg)

	if got := argValue(t, args, "--firmware"); got != cfg.Firmware {
		t.Errorf("--firmware = %q, want %q", got, cfg.Firmware)
	}
	for _, flag := range []string{"--kernel", "--initramfs", "--cmdline"} {
		for _, a := range args {
			if a == flag {
				t.Errorf("did not expect %s when Firmware is set", flag)
			}
		}
	}
	if got := argValue(t, args, "--disk"); got != "path=/data/disk.img,readonly=off,image_type=raw,sparse=on" {
		t.Errorf("--disk = %q, want path=/data/disk.img,readonly=off,image_type=raw,sparse=on", got)
	}
	if got := argValue(t, args, "--cpus"); got != "boot=2" {
		t.Errorf("--cpus = %q, want boot=2", got)
	}
	if got := argValue(t, args, "--memory"); got != "size=2048M,shared=on" {
		t.Errorf("--memory = %q, want size=2048M,shared=on", got)
	}
	if got := argValue(t, args, "--api-socket"); got != "path=/run/cloud-hypervisor/api.sock" {
		t.Errorf("--api-socket = %q, want path=/run/cloud-hypervisor/api.sock", got)
	}
	if got := argValue(t, args, "--serial"); got != "tty" {
		t.Errorf("--serial = %q, want tty", got)
	}
	if got := argValue(t, args, "--console"); got != "off" {
		t.Errorf("--console = %q, want off", got)
	}
}

func TestBuildArgs_CPUHotplugAddsMaxVcpus(t *testing.T) {
	cfg := baseConfig()
	cfg.CPUs = 2
	cfg.CPUsMax = 8

	args := mustBuildArgs(t, cfg)

	if got := argValue(t, args, "--cpus"); got != "boot=2,max=8" {
		t.Errorf("--cpus = %q, want boot=2,max=8", got)
	}
}

func TestBuildArgs_CPUHotplugWithNoGrowthRoomOmitsMax(t *testing.T) {
	cfg := baseConfig()
	cfg.CPUs = 2
	cfg.CPUsMax = 2

	args := mustBuildArgs(t, cfg)

	if got := argValue(t, args, "--cpus"); got != "boot=2" {
		t.Errorf("--cpus = %q, want boot=2 (no max= when there's no room to grow into)", got)
	}
}

func TestBuildArgs_MemoryHotplugAddsVirtioMemDevice(t *testing.T) {
	cfg := baseConfig()
	cfg.MemoryMB = 256
	cfg.MemoryMaxMB = 2048
	cfg.MemoryHotplug = true

	args := mustBuildArgs(t, cfg)

	if got := argValue(t, args, "--memory"); got != "size=256M,shared=on,hotplug_method=virtio-mem,hotplug_size=1792M" {
		t.Errorf("--memory = %q, want size=256M,shared=on,hotplug_method=virtio-mem,hotplug_size=1792M", got)
	}
}

func TestBuildArgs_MemoryHotplugDisabledOmitsDevice(t *testing.T) {
	cfg := baseConfig()
	cfg.MemoryMB = 256
	cfg.MemoryMaxMB = 2048
	cfg.MemoryHotplug = false

	args := mustBuildArgs(t, cfg)

	if got := argValue(t, args, "--memory"); got != "size=256M,shared=on" {
		t.Errorf("--memory = %q, want size=256M,shared=on", got)
	}
}

func TestBuildArgs_MemoryHotplugWithNoGrowthRoomOmitsDevice(t *testing.T) {
	cfg := baseConfig()
	cfg.MemoryMB = 2048
	cfg.MemoryMaxMB = 2048
	cfg.MemoryHotplug = true

	args := mustBuildArgs(t, cfg)

	if got := argValue(t, args, "--memory"); got != "size=2048M,shared=on" {
		t.Errorf("--memory = %q, want size=2048M,shared=on (no hotplug device when there's no room to grow into)", got)
	}
}

func TestBuildArgs_KernelBoot(t *testing.T) {
	cfg := baseConfig()
	cfg.Firmware = ""
	cfg.Kernel = "/boot/vmlinux"
	cfg.Initramfs = "/boot/initrd"
	cfg.Cmdline = "console=ttyS0 root=/dev/vda rw"

	args := mustBuildArgs(t, cfg)

	if got := argValue(t, args, "--kernel"); got != cfg.Kernel {
		t.Errorf("--kernel = %q, want %q", got, cfg.Kernel)
	}
	if got := argValue(t, args, "--initramfs"); got != cfg.Initramfs {
		t.Errorf("--initramfs = %q, want %q", got, cfg.Initramfs)
	}
	if got := argValue(t, args, "--cmdline"); got != cfg.Cmdline {
		t.Errorf("--cmdline = %q, want %q", got, cfg.Cmdline)
	}
	for _, a := range args {
		if a == "--firmware" {
			t.Errorf("did not expect --firmware when Kernel is set")
		}
	}
}

func TestBuildArgs_Filesystems(t *testing.T) {
	cfg := baseConfig()
	cfg.Filesystems = []config.Filesystem{
		{Tag: "myfs", SharedDir: "/host/shared", ReadOnly: false},
		{Tag: "vfs", Socket: "/run/vfs.sock", ReadOnly: true},
	}

	args := mustBuildArgs(t, cfg)

	var fsArgs []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--fs" && i+1 < len(args) {
			fsArgs = append(fsArgs, args[i+1])
		}
	}
	if len(fsArgs) != 2 {
		t.Fatalf("len(fsArgs) = %d, want 2", len(fsArgs))
	}
	if fsArgs[0] != "tag=myfs,shared_dir=/host/shared" {
		t.Errorf("fsArgs[0] = %q, want tag=myfs,shared_dir=/host/shared", fsArgs[0])
	}
	if fsArgs[1] != "tag=vfs,socket=/run/vfs.sock,readonly=on" {
		t.Errorf("fsArgs[1] = %q, want tag=vfs,socket=/run/vfs.sock,readonly=on", fsArgs[1])
	}
}

// Note that the read-only disk here is expected to carry sparse=on
// exactly as the writable one does. That is deliberate rather than an
// oversight: the guest issues no discards to a device it cannot write
// to, so the option decides nothing there, and a --disk built the same
// way whatever the disk is for is one fewer thing to reason about. See
// BuildArgs for what sparse=on is doing at all.
func TestBuildArgs_MultipleDisksAndNet(t *testing.T) {
	cfg := baseConfig()
	cfg.Disks = append(cfg.Disks, config.Disk{Path: "/data/extra.img", ReadOnly: true})
	cfg.Nets = []string{"tap=eth0,mac=02:00:00:00:00:01"}

	args := mustBuildArgs(t, cfg)

	var disks []string
	for i, a := range args {
		if a == "--disk" {
			disks = append(disks, args[i+1])
		}
	}
	want := []string{"path=/data/disk.img,readonly=off,image_type=raw,sparse=on", "path=/data/extra.img,readonly=on,image_type=raw,sparse=on"}
	if len(disks) != len(want) {
		t.Fatalf("disks = %v, want %v", disks, want)
	}
	for i := range want {
		if disks[i] != want[i] {
			t.Errorf("disk[%d] = %q, want %q", i, disks[i], want[i])
		}
	}
	if got := argValue(t, args, "--net"); got != cfg.Nets[0] {
		t.Errorf("--net = %q, want %q", got, cfg.Nets[0])
	}
}

// A disk image gets image_type=raw and sparse=on.
//
// sparse=on is what lets a guest give space back: with it cloud-hypervisor
// advertises VIRTIO_BLK_F_DISCARD, so an extent that grew by 500MB the guest
// has since deleted stops carrying it.
// The file tested here is a raw image and holds raw bytes, which
// is exactly what config.PrepareOverlay leaves at guestimage.Overlay: the
// name says nothing about the format, so what decides image_type is the
// content.
func TestBuildArgs_DiskGetsRawImageTypeAndSparse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "disk.img")
	if err := os.WriteFile(path, []byte("raw root filesystem content"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := baseConfig()
	cfg.Disks = []config.Disk{{Path: path}}

	args := mustBuildArgs(t, cfg)

	want := "path=" + path + ",readonly=off,image_type=raw,sparse=on"
	if got := argValue(t, args, "--disk"); got != want {
		t.Errorf("--disk = %q, want %q", got, want)
	}
}

// A raw reflink clone is what config.PrepareOverlay leaves at
// guestimage.Overlay -- named disk.qcow2 -- when the overlay directory is on
// a filesystem with reflinks (XFS, btrfs). Its name must not make it qcow2:
// cloud-hypervisor refuses to boot a raw file declared as one.
func TestBuildArgs_RawDiskNamedQcow2GetsRawImageType(t *testing.T) {
	path := filepath.Join(t.TempDir(), "disk.qcow2")
	if err := os.WriteFile(path, []byte("raw root filesystem content"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := baseConfig()
	cfg.Disks = []config.Disk{{Path: path}}

	args := mustBuildArgs(t, cfg)

	want := "path=" + path + ",readonly=off,image_type=raw,sparse=on"
	if got := argValue(t, args, "--disk"); got != want {
		t.Errorf("--disk = %q, want %q", got, want)
	}
}

func TestBuildArgs_QcowDiskGetsQcow2ImageTypeAndBackingFiles(t *testing.T) {
	dir := t.TempDir()
	descPath := filepath.Join(dir, "disk.qcow2")
	if err := qcow2.WriteOverlay(descPath, "", 128*1024*1024); err != nil {
		t.Fatal(err)
	}
	if !qcow2.IsQcow2(descPath) {
		t.Fatalf("%s is not a QCOW2 image, so this test proves nothing", descPath)
	}

	cfg := baseConfig()
	cfg.Disks = []config.Disk{{Path: descPath}}

	args := mustBuildArgs(t, cfg)

	want := "path=" + descPath + ",readonly=off,image_type=qcow2,backing_files=on,sparse=on"
	if got := argValue(t, args, "--disk"); got != want {
		t.Errorf("--disk = %q, want %q", got, want)
	}
}

func TestBuildArgs_NoNetFlagWhenUnset(t *testing.T) {
	args := mustBuildArgs(t, baseConfig())
	for _, a := range args {
		if a == "--net" {
			t.Errorf("did not expect --net flag when Net is unset")
		}
	}
}

func TestBuildArgs_ExtraArgsAppended(t *testing.T) {
	cfg := baseConfig()
	cfg.ExtraArgs = []string{"--watchdog", "--rng", "src=/dev/urandom"}

	args := mustBuildArgs(t, cfg)
	tail := args[len(args)-len(cfg.ExtraArgs):]
	if strings.Join(tail, " ") != strings.Join(cfg.ExtraArgs, " ") {
		t.Errorf("extra args not appended verbatim at the end: got %v", args)
	}
}

func TestBuildArgs_RestoresFromExistingSnapshot(t *testing.T) {
	cfg := baseConfig()
	cfg.SnapshotPath = filepath.Join(t.TempDir(), "snapshot")
	if err := os.Mkdir(cfg.SnapshotPath, 0o755); err != nil {
		t.Fatal(err)
	}

	args := mustBuildArgs(t, cfg)

	if got := argValue(t, args, "--api-socket"); got != "path="+cfg.APISocket {
		t.Errorf("--api-socket = %q, want path=%s", got, cfg.APISocket)
	}
	want := "source_url=file://" + cfg.SnapshotPath + ",resume=true"
	if got := argValue(t, args, "--restore"); got != want {
		t.Errorf("--restore = %q, want %q", got, want)
	}
	for _, flag := range []string{"--cpus", "--memory", "--disk", "--firmware", "--kernel", "--serial", "--console"} {
		for _, a := range args {
			if a == flag {
				t.Errorf("did not expect %s when restoring from a snapshot", flag)
			}
		}
	}
}

func TestBuildArgs_BootsFreshWhenSnapshotPathUnsetOrMissing(t *testing.T) {
	cfg := baseConfig()
	args := mustBuildArgs(t, cfg)
	for _, a := range args {
		if a == "--restore" {
			t.Fatalf("did not expect --restore with no SnapshotPath: %v", args)
		}
	}

	cfg.SnapshotPath = filepath.Join(t.TempDir(), "does-not-exist")
	args = mustBuildArgs(t, cfg)
	for _, a := range args {
		if a == "--restore" {
			t.Fatalf("did not expect --restore when SnapshotPath does not exist yet: %v", args)
		}
	}
}

func TestSnapshotExists(t *testing.T) {
	if SnapshotExists("") {
		t.Error("SnapshotExists(\"\") = true, want false")
	}

	missing := filepath.Join(t.TempDir(), "missing")
	if SnapshotExists(missing) {
		t.Error("SnapshotExists(missing) = true, want false")
	}

	present := filepath.Join(t.TempDir(), "present")
	if err := os.Mkdir(present, 0o755); err != nil {
		t.Fatal(err)
	}
	if !SnapshotExists(present) {
		t.Error("SnapshotExists(present) = false, want true")
	}
}

func TestBuildArgs_DiskTuningParameters(t *testing.T) {
	cfg := baseConfig()
	cfg.Disks = []config.Disk{{
		Path:      "/disk/disk.raw",
		ReadOnly:  false,
		NumQueues: 4,
		IOThreads: true,
		IOEngine:  "io_uring",
	}}

	args := mustBuildArgs(t, cfg)
	want := "path=/disk/disk.raw,readonly=off,image_type=raw,sparse=on,num_queues=4"
	if got := argValue(t, args, "--disk"); got != want {
		t.Errorf("--disk = %q, want %q", got, want)
	}

	// Test config level defaults
	cfg2 := baseConfig()
	cfg2.DiskNumQueues = 8
	cfg2.DiskIOThreads = true
	cfg2.DiskIOEngine = "epoll"
	cfg2.Disks = []config.Disk{{Path: "/disk/disk.raw"}}

	args2 := mustBuildArgs(t, cfg2)
	want2 := "path=/disk/disk.raw,readonly=off,image_type=raw,sparse=on,num_queues=8"
	if got := argValue(t, args2, "--disk"); got != want2 {
		t.Errorf("--disk = %q, want %q", got, want2)
	}
}

func TestString_QuotesArgsWithSpaces(t *testing.T) {
	got := String("cloud-hypervisor", []string{"--cmdline", "console=ttyS0 root=/dev/vda"})
	want := `cloud-hypervisor --cmdline "console=ttyS0 root=/dev/vda"`
	if got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

// TestBuildArgs_MultipleNets covers a netshim-managed two-NIC guest: the
// spliced interface plus the control link, each of which needs its own
// --net (see pkg/netshim's GuestConfig).
func TestBuildArgs_MultipleNets(t *testing.T) {
	cfg := baseConfig()
	cfg.Nets = []string{"tap=tap-web,mac=02:42:ac:11:00:02,mtu=1450", "tap=ctl-web"}

	args := mustBuildArgs(t, cfg)

	var nets []string
	for i, a := range args {
		if a == "--net" {
			nets = append(nets, args[i+1])
		}
	}
	if len(nets) != len(cfg.Nets) {
		t.Fatalf("--net values = %v, want %v", nets, cfg.Nets)
	}
	for i := range cfg.Nets {
		if nets[i] != cfg.Nets[i] {
			t.Errorf("--net[%d] = %q, want %q", i, nets[i], cfg.Nets[i])
		}
	}
}

// The vsock device is how "granule exec" reaches the guest at all now, so
// a VM booted without one is a VM nothing can be run inside.
func TestBuildArgs_AttachesTheVsockDeviceExecReachesTheGuestOver(t *testing.T) {
	cfg := baseConfig()
	cfg.VsockSocket = "/run/granule/vsock.sock"
	cfg.VsockCID = 3

	args := mustBuildArgs(t, cfg)
	if got := argValue(t, args, "--vsock"); got != "cid=3,socket=/run/granule/vsock.sock" {
		t.Errorf("--vsock = %q", got)
	}

	// Empty means no device, which is the only way to ask for a VM with
	// no exec path in.
	cfg.VsockSocket = ""
	for _, a := range mustBuildArgs(t, cfg) {
		if a == "--vsock" {
			t.Error("a VM with no vsock socket configured still got a vsock device")
		}
	}
}

// A disk with a serial is attached with it, which is how the guest tells
// the container override's disk from the others.
func TestBuildArgs_DiskSerial(t *testing.T) {
	cfg := baseConfig()
	cfg.Disks = append(cfg.Disks, config.Disk{Path: "/data/override.img", ReadOnly: true, Serial: "granule-ctr-override"})
	args := mustBuildArgs(t, cfg)
	var disks []string
	for i, a := range args {
		if a == "--disk" {
			disks = append(disks, args[i+1])
		}
	}
	if len(disks) != 2 {
		t.Fatalf("--disk values = %q, want two", disks)
	}
	if strings.Contains(disks[0], "serial=") {
		t.Errorf("the boot disk was given a serial: %q", disks[0])
	}
	if !strings.Contains(disks[1], "path=/data/override.img,readonly=on") || !strings.HasSuffix(disks[1], ",serial=granule-ctr-override") {
		t.Errorf("override disk = %q, want it read-only with its serial", disks[1])
	}
}
