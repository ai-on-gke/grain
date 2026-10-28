package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/granule/pkg/containerspec"
	"github.com/bwsalmon/granule/pkg/extents"
	"github.com/bwsalmon/granule/pkg/guestimage"
	"github.com/bwsalmon/granule/pkg/qcow2"
)

// clearEnv resets every env var this package reads, so tests don't leak
// into each other or pick up the outer environment.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		envDiskImage, envDiskReadonly, envDiskMode, envDiskOverlayPath, envDiskSizeMB, envExtentSizeMB, envDiskExtentSizeMB, envStorageBackend, envExtraDisks, envKernel, envInitramfs,
		envFirmware, envCmdline, envCPUs, envCPUsMax, envMemoryMB, envMemoryMaxMB,
		envMemoryHotplug, envMemoryShared, envNet,
		envAPISocket, envBinaryPath, envExtraArgs, envShutdownTimeout,
		envSetupScript, envSnapshotPath,
		envMemAgent, envMemAgentAddr, envMemAgentToken, envMemAgentStepMB, envMemAgentCooldown,
		envGuestContractMin, envContainerOverride, envWorkloadStdout, envWorkloadStderr,
		envWorkloadExitFile, envWorkloadStdin, envWorkloadTTY, envWorkloadResize,
	} {
		os.Unsetenv(k)
	}
}

func writeTempFile(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFromEnv_MinimalKernelBoot(t *testing.T) {
	clearEnv(t)
	disk := writeTempFile(t, "disk.img")
	kernel := writeTempFile(t, "vmlinux")
	t.Setenv(envDiskImage, disk)
	t.Setenv(envKernel, kernel)

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}

	if len(cfg.Disks) != 1 || cfg.Disks[0].Path != disk || cfg.Disks[0].ReadOnly {
		t.Errorf("Disks = %+v, want single rw disk at %s", cfg.Disks, disk)
	}
	if cfg.Kernel != kernel {
		t.Errorf("Kernel = %q, want %q", cfg.Kernel, kernel)
	}
	if cfg.CPUs != defaultCPUs {
		t.Errorf("CPUs = %d, want default %d", cfg.CPUs, defaultCPUs)
	}
	if cfg.CPUsMax != defaultCPUs {
		t.Errorf("CPUsMax = %d, want default %d (no hotplug headroom unless CHV_CPUS_MAX is set)", cfg.CPUsMax, defaultCPUs)
	}
	if cfg.MemoryMB != defaultMemoryMB {
		t.Errorf("MemoryMB = %d, want default %d", cfg.MemoryMB, defaultMemoryMB)
	}
	if cfg.MemoryMaxMB != defaultMemoryMaxMB {
		t.Errorf("MemoryMaxMB = %d, want default %d", cfg.MemoryMaxMB, defaultMemoryMaxMB)
	}
	if !cfg.MemoryHotplug {
		t.Errorf("MemoryHotplug = false, want default true")
	}
	if cfg.ShutdownTimeout != defaultShutdownTimeout {
		t.Errorf("ShutdownTimeout = %s, want default %s", cfg.ShutdownTimeout, defaultShutdownTimeout)
	}
}

func TestFromEnv_DiskImageDefaultsToBundledGuestImage(t *testing.T) {
	clearEnv(t)
	t.Setenv(envFirmware, writeTempFile(t, "fw"))

	// CHV_DISK_IMAGE is left unset: FromEnv should fall back to
	// defaultDiskImage (which won't exist in a test environment) rather
	// than reporting it as missing outright.
	_, err := FromEnv()
	if err == nil {
		t.Fatal("expected error for nonexistent default disk image, got nil")
	}
	if !strings.Contains(err.Error(), defaultDiskImage) {
		t.Errorf("error = %v, want it to mention the default disk path %q", err, defaultDiskImage)
	}
}

func TestFromEnv_KernelDefaultsToBundledKernel(t *testing.T) {
	clearEnv(t)
	t.Setenv(envDiskImage, writeTempFile(t, "disk.img"))

	// Neither CHV_KERNEL nor CHV_FIRMWARE is set: FromEnv should fall
	// back to defaultKernel (which won't exist in a test environment)
	// rather than reporting one of the two as required.
	_, err := FromEnv()
	if err == nil {
		t.Fatal("expected error for nonexistent default kernel, got nil")
	}
	if !strings.Contains(err.Error(), defaultKernel) {
		t.Errorf("error = %v, want it to mention the default kernel path %q", err, defaultKernel)
	}
}

func TestFromEnv_KernelAndFirmwareMutuallyExclusive(t *testing.T) {
	clearEnv(t)
	t.Setenv(envDiskImage, writeTempFile(t, "disk.img"))
	t.Setenv(envKernel, writeTempFile(t, "vmlinux"))
	t.Setenv(envFirmware, writeTempFile(t, "fw"))

	if _, err := FromEnv(); err == nil {
		t.Fatal("expected error when both kernel and firmware are set, got nil")
	}
}

func TestFromEnv_MissingPathsRejected(t *testing.T) {
	clearEnv(t)
	t.Setenv(envDiskImage, filepath.Join(t.TempDir(), "does-not-exist.img"))
	t.Setenv(envFirmware, writeTempFile(t, "fw"))

	if _, err := FromEnv(); err == nil {
		t.Fatal("expected error for nonexistent disk image, got nil")
	}
}

func TestFromEnv_ExtraDisksAndOverrides(t *testing.T) {
	clearEnv(t)
	disk := writeTempFile(t, "disk.img")
	extra1 := writeTempFile(t, "extra1.img")
	extra2 := writeTempFile(t, "extra2.img")
	t.Setenv(envDiskImage, disk)
	t.Setenv(envDiskReadonly, "true")
	t.Setenv(envFirmware, writeTempFile(t, "fw"))
	t.Setenv(envExtraDisks, extra1+":ro,"+extra2+":rw")
	t.Setenv(envCPUs, "4")
	t.Setenv(envMemoryMB, "8192")
	t.Setenv(envMemoryShared, "false")
	t.Setenv(envNet, "tap=eth0")
	t.Setenv(envAPISocket, "/tmp/custom.sock")
	t.Setenv(envBinaryPath, "/opt/cloud-hypervisor")
	t.Setenv(envExtraArgs, "--rng src=/dev/urandom --watchdog")
	t.Setenv(envShutdownTimeout, "5s")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}

	wantDisks := []Disk{
		{Path: disk, ReadOnly: true},
		{Path: extra1, ReadOnly: true},
		{Path: extra2, ReadOnly: false},
	}
	if len(cfg.Disks) != len(wantDisks) {
		t.Fatalf("Disks = %+v, want %+v", cfg.Disks, wantDisks)
	}
	for i, d := range wantDisks {
		if cfg.Disks[i] != d {
			t.Errorf("Disks[%d] = %+v, want %+v", i, cfg.Disks[i], d)
		}
	}
	if cfg.CPUs != 4 {
		t.Errorf("CPUs = %d, want 4", cfg.CPUs)
	}
	if cfg.MemoryMB != 8192 {
		t.Errorf("MemoryMB = %d, want 8192", cfg.MemoryMB)
	}
	if cfg.MemoryShared {
		t.Errorf("MemoryShared = true, want false")
	}
	if len(cfg.Nets) != 1 || cfg.Nets[0] != "tap=eth0" {
		t.Errorf("Nets = %q, want [tap=eth0]", cfg.Nets)
	}
	if cfg.APISocket != "/tmp/custom.sock" {
		t.Errorf("APISocket = %q, want /tmp/custom.sock", cfg.APISocket)
	}
	if cfg.BinaryPath != "/opt/cloud-hypervisor" {
		t.Errorf("BinaryPath = %q, want /opt/cloud-hypervisor", cfg.BinaryPath)
	}
	wantArgs := []string{"--rng", "src=/dev/urandom", "--watchdog"}
	if len(cfg.ExtraArgs) != len(wantArgs) {
		t.Fatalf("ExtraArgs = %v, want %v", cfg.ExtraArgs, wantArgs)
	}
	for i, a := range wantArgs {
		if cfg.ExtraArgs[i] != a {
			t.Errorf("ExtraArgs[%d] = %q, want %q", i, cfg.ExtraArgs[i], a)
		}
	}
	if cfg.ShutdownTimeout != 5*time.Second {
		t.Errorf("ShutdownTimeout = %s, want 5s", cfg.ShutdownTimeout)
	}
}

func TestFromEnv_InvalidExtraDiskMode(t *testing.T) {
	clearEnv(t)
	t.Setenv(envDiskImage, writeTempFile(t, "disk.img"))
	t.Setenv(envFirmware, writeTempFile(t, "fw"))
	t.Setenv(envExtraDisks, writeTempFile(t, "extra.img")+":bogus")

	if _, err := FromEnv(); err == nil {
		t.Fatal("expected error for invalid extra disk mode, got nil")
	}
}

func TestFromEnv_SetupScriptWithoutSnapshotPath(t *testing.T) {
	clearEnv(t)
	t.Setenv(envDiskImage, writeTempFile(t, "disk.img"))
	t.Setenv(envFirmware, writeTempFile(t, "fw"))
	t.Setenv(envSetupScript, "echo hi")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v, want CHV_SETUP_SCRIPT to be usable without CHV_SNAPSHOT_PATH", err)
	}
	if cfg.SetupScript != "echo hi" {
		t.Errorf("SetupScript = %q, want %q", cfg.SetupScript, "echo hi")
	}
	if cfg.SnapshotPath != "" {
		t.Errorf("SnapshotPath = %q, want empty", cfg.SnapshotPath)
	}
}

func TestFromEnv_SnapshotPathMustBeAbsolute(t *testing.T) {
	clearEnv(t)
	t.Setenv(envDiskImage, writeTempFile(t, "disk.img"))
	t.Setenv(envFirmware, writeTempFile(t, "fw"))
	t.Setenv(envSnapshotPath, "relative/path")

	if _, err := FromEnv(); err == nil {
		t.Fatal("expected error for a relative CHV_SNAPSHOT_PATH, got nil")
	}
}

func TestFromEnv_SnapshotPathParentMustExist(t *testing.T) {
	clearEnv(t)
	t.Setenv(envDiskImage, writeTempFile(t, "disk.img"))
	t.Setenv(envFirmware, writeTempFile(t, "fw"))
	t.Setenv(envSnapshotPath, filepath.Join(t.TempDir(), "does-not-exist", "snapshot"))

	if _, err := FromEnv(); err == nil {
		t.Fatal("expected error when CHV_SNAPSHOT_PATH's parent directory doesn't exist, got nil")
	}
}

func TestFromEnv_SetupScriptAndSnapshotPath(t *testing.T) {
	clearEnv(t)
	t.Setenv(envDiskImage, writeTempFile(t, "disk.img"))
	t.Setenv(envFirmware, writeTempFile(t, "fw"))
	t.Setenv(envSetupScript, "apt-get install -y foo")
	t.Setenv(envSnapshotPath, filepath.Join(t.TempDir(), "snapshot"))

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if cfg.SetupScript != "apt-get install -y foo" {
		t.Errorf("SetupScript = %q, want %q", cfg.SetupScript, "apt-get install -y foo")
	}
	if cfg.SnapshotPath == "" {
		t.Error("SnapshotPath is empty, want it set")
	}
}

func TestFromEnv_AutoDetectsSnapshotDir(t *testing.T) {
	clearEnv(t)
	t.Setenv(envDiskImage, writeTempFile(t, "disk.img"))
	t.Setenv(envFirmware, writeTempFile(t, "fw"))

	// When guestimage.SnapshotDir does not exist, SnapshotPath remains empty.
	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if _, err := os.Stat(guestimage.SnapshotDir); os.IsNotExist(err) {
		if cfg.SnapshotPath != "" {
			t.Errorf("SnapshotPath = %q, want empty when snapshot dir does not exist", cfg.SnapshotPath)
		}
	}
}

func TestFromEnv_RejectsTooFewCPUs(t *testing.T) {
	clearEnv(t)
	t.Setenv(envDiskImage, writeTempFile(t, "disk.img"))
	t.Setenv(envFirmware, writeTempFile(t, "fw"))
	t.Setenv(envCPUs, "0")

	if _, err := FromEnv(); err == nil {
		t.Fatal("expected error for 0 cpus, got nil")
	}
}

func TestFromEnv_CPUsMaxDefaultsToCPUsWhenOverridden(t *testing.T) {
	clearEnv(t)
	t.Setenv(envDiskImage, writeTempFile(t, "disk.img"))
	t.Setenv(envFirmware, writeTempFile(t, "fw"))
	t.Setenv(envCPUs, "4")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if cfg.CPUsMax != 4 {
		t.Errorf("CPUsMax = %d, want 4 (tracking the overridden boot count, since CHV_CPUS_MAX was left unset)", cfg.CPUsMax)
	}
}

func TestFromEnv_CPUsMaxExplicit(t *testing.T) {
	clearEnv(t)
	t.Setenv(envDiskImage, writeTempFile(t, "disk.img"))
	t.Setenv(envFirmware, writeTempFile(t, "fw"))
	t.Setenv(envCPUs, "2")
	t.Setenv(envCPUsMax, "8")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if cfg.CPUs != 2 {
		t.Errorf("CPUs = %d, want 2", cfg.CPUs)
	}
	if cfg.CPUsMax != 8 {
		t.Errorf("CPUsMax = %d, want 8", cfg.CPUsMax)
	}
}

func TestFromEnv_RejectsCPUsMaxBelowCPUs(t *testing.T) {
	clearEnv(t)
	t.Setenv(envDiskImage, writeTempFile(t, "disk.img"))
	t.Setenv(envFirmware, writeTempFile(t, "fw"))
	t.Setenv(envCPUs, "4")
	t.Setenv(envCPUsMax, "2")

	if _, err := FromEnv(); err == nil {
		t.Fatal("expected error when CHV_CPUS_MAX is below CHV_CPUS, got nil")
	}
}

func TestFromEnv_MemoryMaxDefaultTracksOverriddenStartingSize(t *testing.T) {
	clearEnv(t)
	t.Setenv(envDiskImage, writeTempFile(t, "disk.img"))
	t.Setenv(envFirmware, writeTempFile(t, "fw"))
	t.Setenv(envMemoryMB, "4096")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if cfg.MemoryMaxMB != 4096 {
		t.Errorf("MemoryMaxMB = %d, want 4096 (tracking the overridden starting size, since it exceeds the default max)", cfg.MemoryMaxMB)
	}
}

func TestFromEnv_MemoryHotplugExplicit(t *testing.T) {
	clearEnv(t)
	t.Setenv(envDiskImage, writeTempFile(t, "disk.img"))
	t.Setenv(envFirmware, writeTempFile(t, "fw"))
	t.Setenv(envMemoryMB, "512")
	t.Setenv(envMemoryMaxMB, "1024")
	t.Setenv(envMemoryHotplug, "false")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if cfg.MemoryMB != 512 {
		t.Errorf("MemoryMB = %d, want 512", cfg.MemoryMB)
	}
	if cfg.MemoryMaxMB != 1024 {
		t.Errorf("MemoryMaxMB = %d, want 1024", cfg.MemoryMaxMB)
	}
	if cfg.MemoryHotplug {
		t.Errorf("MemoryHotplug = true, want false")
	}
}

// A Granule writes its switches as on and off: docs/granulefile.md
// §5.5 gives "VM SET memory-hotplug" those two values, and VM SET
// records the value it was given in the runtime variable verbatim. So a
// machine built from a Granule arrives here with "on" in
// CHV_MEMORY_HOTPLUG and has to boot.
func TestFromEnv_BooleansAcceptOnAndOff(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"on", true}, {"On", true}, {"yes", true}, {"true", true},
		{"off", false}, {"OFF", false}, {"no", false}, {"false", false},
	} {
		t.Run(tc.value, func(t *testing.T) {
			clearEnv(t)
			t.Setenv(envDiskImage, writeTempFile(t, "disk.img"))
			t.Setenv(envFirmware, writeTempFile(t, "fw"))
			t.Setenv(envMemoryHotplug, tc.value)

			cfg, err := FromEnv()
			if err != nil {
				t.Fatalf("FromEnv() with %s=%q: %v", envMemoryHotplug, tc.value, err)
			}
			if cfg.MemoryHotplug != tc.want {
				t.Errorf("%s=%q gave MemoryHotplug = %v, want %v", envMemoryHotplug, tc.value, cfg.MemoryHotplug, tc.want)
			}
		})
	}
}

func TestFromEnv_RejectsMemoryMaxBelowStart(t *testing.T) {
	clearEnv(t)
	t.Setenv(envDiskImage, writeTempFile(t, "disk.img"))
	t.Setenv(envFirmware, writeTempFile(t, "fw"))
	t.Setenv(envMemoryMB, "1024")
	t.Setenv(envMemoryMaxMB, "512")

	if _, err := FromEnv(); err == nil {
		t.Fatal("expected error when CHV_MEMORY_MAX_MB is below CHV_MEMORY_MB, got nil")
	}
}

func TestFromEnv_HotplugWithGrowthRoomRequiresSharedMemory(t *testing.T) {
	clearEnv(t)
	t.Setenv(envDiskImage, writeTempFile(t, "disk.img"))
	t.Setenv(envFirmware, writeTempFile(t, "fw"))
	t.Setenv(envMemoryMB, "512")
	t.Setenv(envMemoryMaxMB, "1024")
	t.Setenv(envMemoryShared, "false")

	if _, err := FromEnv(); err == nil {
		t.Fatal("expected error for hotplug enabled with growth room but shared memory disabled, got nil")
	}

	// Disabling hotplug altogether lifts the shared-memory requirement,
	// since no virtio-mem device gets attached at all.
	t.Setenv(envMemoryHotplug, "false")
	if _, err := FromEnv(); err != nil {
		t.Errorf("FromEnv() error = %v, want nil once hotplug is disabled", err)
	}
}

func TestFromEnv_MemAgentDefaultsOffAndDoesNotRequireHotplugUnlessEnabled(t *testing.T) {
	clearEnv(t)
	t.Setenv(envDiskImage, writeTempFile(t, "disk.img"))
	t.Setenv(envFirmware, writeTempFile(t, "fw"))
	t.Setenv(envMemoryHotplug, "false")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if cfg.MemAgent {
		t.Errorf("MemAgent = true, want false by default")
	}
	if cfg.MemAgentAddr != defaultMemAgentAddr {
		t.Errorf("MemAgentAddr = %q, want %q", cfg.MemAgentAddr, defaultMemAgentAddr)
	}
	if cfg.MemAgentToken != "" {
		t.Errorf("MemAgentToken = %q, want empty by default", cfg.MemAgentToken)
	}
	if cfg.MemAgentStepMB != defaultMemAgentStepMB {
		t.Errorf("MemAgentStepMB = %d, want %d", cfg.MemAgentStepMB, defaultMemAgentStepMB)
	}
	if cfg.MemAgentCooldown != defaultMemAgentCooldown {
		t.Errorf("MemAgentCooldown = %s, want %s", cfg.MemAgentCooldown, defaultMemAgentCooldown)
	}
	if cfg.MemAgentScaleDownCooldown != defaultMemAgentScaleDownCooldown {
		t.Errorf("MemAgentScaleDownCooldown = %s, want %s", cfg.MemAgentScaleDownCooldown, defaultMemAgentScaleDownCooldown)
	}
}

func TestFromEnv_MemAgentRequiresMemoryHotplug(t *testing.T) {
	clearEnv(t)
	t.Setenv(envDiskImage, writeTempFile(t, "disk.img"))
	t.Setenv(envFirmware, writeTempFile(t, "fw"))
	t.Setenv(envMemAgent, "true")
	t.Setenv(envMemoryHotplug, "false")

	if _, err := FromEnv(); err == nil {
		t.Fatal("expected error for CHV_MEM_AGENT=true with memory hotplug disabled, got nil")
	}
}

func TestFromEnv_MemAgentRequiresMemoryMaxAboveMemoryMB(t *testing.T) {
	for _, tc := range []struct {
		name    string
		bootMB  string
		maxMB   string
		wantErr bool
	}{
		{
			name:    "max equal to boot",
			bootMB:  "512",
			maxMB:   "512",
			wantErr: true,
		},
		{
			name:    "default max equal to overridden boot",
			bootMB:  "2048",
			maxMB:   "",
			wantErr: true,
		},
		{
			name:    "max above boot",
			bootMB:  "512",
			maxMB:   "1024",
			wantErr: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv(envDiskImage, writeTempFile(t, "disk.img"))
			t.Setenv(envFirmware, writeTempFile(t, "fw"))
			t.Setenv(envMemAgent, "true")
			t.Setenv(envMemoryMB, tc.bootMB)
			if tc.maxMB != "" {
				t.Setenv(envMemoryMaxMB, tc.maxMB)
			}

			_, err := FromEnv()
			if (err != nil) != tc.wantErr {
				t.Fatalf("FromEnv() error = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr && !strings.Contains(err.Error(), envMemoryMaxMB) {
				t.Errorf("error = %v, want it to name %s", err, envMemoryMaxMB)
			}
		})
	}
}

func TestFromEnv_MemAgentExplicitValues(t *testing.T) {
	clearEnv(t)
	t.Setenv(envDiskImage, writeTempFile(t, "disk.img"))
	t.Setenv(envFirmware, writeTempFile(t, "fw"))
	t.Setenv(envMemAgent, "true")
	t.Setenv(envMemAgentAddr, "169.254.100.1:12345")
	t.Setenv(envMemAgentToken, "test-secret-token")
	t.Setenv(envMemAgentStepMB, "128")
	t.Setenv(envMemAgentCooldown, "1m")
	t.Setenv(envMemAgentScaleDownCooldown, "3m")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if !cfg.MemAgent {
		t.Errorf("MemAgent = false, want true")
	}
	if cfg.MemAgentAddr != "169.254.100.1:12345" {
		t.Errorf("MemAgentAddr = %q, want 169.254.100.1:12345", cfg.MemAgentAddr)
	}
	if cfg.MemAgentToken != "test-secret-token" {
		t.Errorf("MemAgentToken = %q, want test-secret-token", cfg.MemAgentToken)
	}
	if cfg.MemAgentStepMB != 128 {
		t.Errorf("MemAgentStepMB = %d, want 128", cfg.MemAgentStepMB)
	}
	if cfg.MemAgentCooldown != time.Minute {
		t.Errorf("MemAgentCooldown = %s, want 1m", cfg.MemAgentCooldown)
	}
	if cfg.MemAgentScaleDownCooldown != 3*time.Minute {
		t.Errorf("MemAgentScaleDownCooldown = %s, want 3m", cfg.MemAgentScaleDownCooldown)
	}
}

func TestFromEnv_RejectsNonPositiveMemAgentStepMB(t *testing.T) {
	clearEnv(t)
	t.Setenv(envDiskImage, writeTempFile(t, "disk.img"))
	t.Setenv(envFirmware, writeTempFile(t, "fw"))
	t.Setenv(envMemAgentStepMB, "0")

	if _, err := FromEnv(); err == nil {
		t.Fatal("expected error for CHV_MEM_AGENT_STEP_MB=0, got nil")
	}
}

func TestBakedInKernelPrefersTheGuestsOwn(t *testing.T) {
	// A guest built with GUEST_KERNEL_PACKAGE carries its own kernel and
	// initramfs beside its disk; booting that rootfs under the
	// build-kernel build instead would give it a kernel whose modules
	// are not the ones in its /lib/modules.
	dir := t.TempDir()
	guestKernel := filepath.Join(dir, "vmlinuz")
	guestInitramfs := filepath.Join(dir, "initrd.img")
	fallback := filepath.Join(dir, "vmlinux")
	for _, p := range []string{guestKernel, guestInitramfs, fallback} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	kernel, initramfs := bakedInKernel(guestKernel, guestInitramfs, fallback)
	if kernel != guestKernel {
		t.Errorf("kernel = %q, want the guest's own %q", kernel, guestKernel)
	}
	if initramfs != guestInitramfs {
		t.Errorf("initramfs = %q, want %q", initramfs, guestInitramfs)
	}
}

func TestBakedInKernelFallsBackWithoutBothHalves(t *testing.T) {
	// Half a pair is not a guest kernel: a vmlinuz with no initrd boots
	// without the modules and udev rules the initramfs carries, which
	// fails as an unreachable guest rather than as a boot error. The
	// default build has neither half, which is the same case.
	dir := t.TempDir()
	fallback := filepath.Join(dir, "vmlinux")
	if err := os.WriteFile(fallback, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	guestKernel := filepath.Join(dir, "vmlinuz")

	for _, tc := range []struct {
		name    string
		present []string
	}{
		{"neither", nil},
		{"kernel only", []string{guestKernel}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, p := range tc.present {
				if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.Remove(p) })
			}
			kernel, initramfs := bakedInKernel(guestKernel, filepath.Join(dir, "initrd.img"), fallback)
			if kernel != fallback {
				t.Errorf("kernel = %q, want the fallback %q", kernel, fallback)
			}
			if initramfs != "" {
				t.Errorf("initramfs = %q, want none", initramfs)
			}
		})
	}
}

func TestDiskModeDefaultsToOverlay(t *testing.T) {
	// The default matters more than most: it is what every VM that says
	// nothing gets, and the alternative -- writing through to the disk
	// image -- costs a copy of that image on the guest's first write and
	// makes two VMs on one host contend for the same file.
	clearEnv(t)
	t.Setenv(envDiskImage, writeTempFile(t, "disk.img"))
	t.Setenv(envFirmware, writeTempFile(t, "fw"))

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if cfg.DiskMode != DiskModeOverlay {
		t.Errorf("DiskMode = %q, want %q", cfg.DiskMode, DiskModeOverlay)
	}
	if cfg.Disks[0].ReadOnly {
		t.Error("Disks[0].ReadOnly = true, want false: an overlay-backed disk is attached writable")
	}
}

func TestDiskModeFallsBackToTheBoolean(t *testing.T) {
	// CHV_DISK_READONLY=false has always meant "write through to the disk
	// image" in this container, so a deployment still setting it must keep
	// getting exactly that rather than quietly acquiring a throw-away
	// overlay.
	for _, tc := range []struct {
		readonly string
		want     string
	}{
		{"false", DiskModePersistent},
		{"true", DiskModeReadOnly},
	} {
		t.Run(tc.readonly, func(t *testing.T) {
			clearEnv(t)
			t.Setenv(envDiskImage, writeTempFile(t, "disk.img"))
			t.Setenv(envFirmware, writeTempFile(t, "fw"))
			t.Setenv(envDiskReadonly, tc.readonly)

			cfg, err := FromEnv()
			if err != nil {
				t.Fatalf("FromEnv() error = %v", err)
			}
			if cfg.DiskMode != tc.want {
				t.Errorf("DiskMode = %q, want %q", cfg.DiskMode, tc.want)
			}
		})
	}
}

func TestDiskModeRejectsContradictions(t *testing.T) {
	clearEnv(t)
	t.Setenv(envDiskImage, writeTempFile(t, "disk.img"))
	t.Setenv(envFirmware, writeTempFile(t, "fw"))
	t.Setenv(envDiskMode, DiskModeOverlay)
	t.Setenv(envDiskReadonly, "true")

	if _, err := FromEnv(); err == nil {
		t.Error("FromEnv() = nil, want an error: the two settings contradict each other and guessing is worse than saying so")
	}
}

func TestDiskModeRejectsUnknownModes(t *testing.T) {
	clearEnv(t)
	t.Setenv(envDiskImage, writeTempFile(t, "disk.img"))
	t.Setenv(envFirmware, writeTempFile(t, "fw"))
	t.Setenv(envDiskMode, "writable")

	if _, err := FromEnv(); err == nil {
		t.Error("FromEnv() = nil, want an error naming the three valid modes")
	}
}

func TestPrepareOverlayCreatesAndThenReusesTheOverlay(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "disk.img")
	if err := os.WriteFile(source, []byte("source disk contents"), 0o644); err != nil {
		t.Fatal(err)
	}
	overlay := filepath.Join(dir, "overlay", "disk.qcow2")

	cfg := Config{
		DiskMode:    DiskModeOverlay,
		OverlayPath: overlay,
		Disks:       []Disk{{Path: source}},
	}
	if err := cfg.PrepareOverlay(); err != nil {
		t.Fatalf("PrepareOverlay() error = %v", err)
	}
	if cfg.Disks[0].Path != overlay {
		t.Errorf("Disks[0].Path = %q, want the overlay %q", cfg.Disks[0].Path, overlay)
	}
	if filepath.Ext(cfg.Disks[0].Path) != ".qcow2" {
		t.Errorf("overlay path %q must end in .qcow2", cfg.Disks[0].Path)
	}

	// A restarted container must find the guest's writes still there, so
	// a second call reuses the overlay rather than starting it over.
	if err := os.WriteFile(overlay, []byte("written by the guest"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg2 := Config{DiskMode: DiskModeOverlay, OverlayPath: overlay, Disks: []Disk{{Path: source}}}
	if err := cfg2.PrepareOverlay(); err != nil {
		t.Fatalf("PrepareOverlay() second call error = %v", err)
	}
	data, err := os.ReadFile(overlay)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "written by the guest" {
		t.Error("PrepareOverlay() recreated an existing overlay, discarding whatever the guest had written")
	}
}

func TestPrepareOverlay_LatencyUnder20ms(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "base.img")
	f, err := os.Create(source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("BASE_ROOTFS_HEADER")); err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(1024 * 1024 * 1024); err != nil { // 1GB disk image
		t.Fatal(err)
	}
	f.Close()

	overlay := filepath.Join(dir, "overlay", "disk.qcow2")
	cfg := Config{
		DiskMode:    DiskModeOverlay,
		OverlayPath: overlay,
		Disks:       []Disk{{Path: source}},
	}

	start := time.Now()
	if err := cfg.PrepareOverlay(); err != nil {
		t.Fatalf("PrepareOverlay() error = %v", err)
	}
	elapsed := time.Since(start)

	if elapsed > 20*time.Millisecond && qcow2.HasReflinkSupport(dir) {
		t.Errorf("PrepareOverlay() took %v, want < 20ms", elapsed)
	}
	t.Logf("PrepareOverlay() completed in %v", elapsed)

	if cfg.Disks[0].Path != overlay {
		t.Errorf("Disks[0].Path = %q, want %q", cfg.Disks[0].Path, overlay)
	}
}

func BenchmarkPrepareOverlay(b *testing.B) {
	dir := b.TempDir()
	source := filepath.Join(dir, "base.img")
	f, _ := os.Create(source)
	_ = f.Truncate(512 * 1024 * 1024)
	_ = f.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		overlay := filepath.Join(dir, fmt.Sprintf("overlay-%d", i), "disk.qcow2")
		cfg := Config{
			DiskMode:    DiskModeOverlay,
			OverlayPath: overlay,
			Disks:       []Disk{{Path: source}},
		}
		if err := cfg.PrepareOverlay(); err != nil {
			b.Fatalf("PrepareOverlay failed: %v", err)
		}
	}
}

func TestPrepareOverlayIsANoOpInTheOtherModes(t *testing.T) {
	for _, mode := range []string{DiskModePersistent, DiskModeReadOnly} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			overlay := filepath.Join(dir, "overlay", "disk.qcow2")
			cfg := Config{DiskMode: mode, OverlayPath: overlay, Disks: []Disk{{Path: "/images/disk.img"}}}
			if err := cfg.PrepareOverlay(); err != nil {
				t.Fatalf("PrepareOverlay() error = %v", err)
			}
			if cfg.Disks[0].Path != "/images/disk.img" {
				t.Errorf("Disks[0].Path = %q, want the source untouched", cfg.Disks[0].Path)
			}
			if _, err := os.Stat(overlay); !os.IsNotExist(err) {
				t.Error("an overlay was created in a mode that has none")
			}
		})
	}
}

func TestFromEnv_DiskSizeMB(t *testing.T) {
	clearEnv(t)
	t.Setenv(envDiskImage, writeTempFile(t, "disk.img"))
	t.Setenv(envFirmware, writeTempFile(t, "fw"))

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if cfg.DiskSizeMB != 0 {
		t.Errorf("DiskSizeMB = %d, want 0 (the disk image's own size) when %s is unset", cfg.DiskSizeMB, envDiskSizeMB)
	}

	t.Setenv(envDiskSizeMB, "8192")
	cfg, err = FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if cfg.DiskSizeMB != 8192 {
		t.Errorf("DiskSizeMB = %d, want 8192", cfg.DiskSizeMB)
	}
}

func TestFromEnv_ExtentSizeMB(t *testing.T) {
	clearEnv(t)
	t.Setenv(envDiskImage, writeTempFile(t, "disk.img"))
	t.Setenv(envFirmware, writeTempFile(t, "fw"))

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if cfg.ExtentSizeMB != 0 {
		t.Errorf("ExtentSizeMB = %d, want 0 when %s is unset", cfg.ExtentSizeMB, envExtentSizeMB)
	}

	t.Setenv(envExtentSizeMB, "128")
	cfg, err = FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if cfg.ExtentSizeMB != 128 {
		t.Errorf("ExtentSizeMB = %d, want 128", cfg.ExtentSizeMB)
	}

	clearEnv(t)
	t.Setenv(envDiskImage, writeTempFile(t, "disk.img"))
	t.Setenv(envFirmware, writeTempFile(t, "fw"))
	t.Setenv(envDiskExtentSizeMB, "256")
	cfg, err = FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if cfg.ExtentSizeMB != 256 {
		t.Errorf("ExtentSizeMB = %d, want 256 from %s", cfg.ExtentSizeMB, envDiskExtentSizeMB)
	}
}

func TestFromEnv_ExtentSizeMBRejectsBadValues(t *testing.T) {
	for _, tc := range []struct {
		name  string
		size  string
		mode  string
		wants string
	}{
		{name: "not a number", size: "128M", mode: DiskModeOverlay, wants: envExtentSizeMB},
		{name: "negative", size: "-1", mode: DiskModeOverlay, wants: "negative"},
		{name: "persistent mode", size: "128", mode: DiskModePersistent, wants: DiskModeOverlay},
		{name: "readonly mode", size: "128", mode: DiskModeReadOnly, wants: DiskModeOverlay},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv(envDiskImage, writeTempFile(t, "disk.img"))
			t.Setenv(envFirmware, writeTempFile(t, "fw"))
			t.Setenv(envDiskMode, tc.mode)
			t.Setenv(envExtentSizeMB, tc.size)

			_, err := FromEnv()
			if err == nil {
				t.Fatalf("FromEnv() = nil, want an error for %s=%q with %s=%s", envExtentSizeMB, tc.size, envDiskMode, tc.mode)
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Errorf("FromEnv() error = %v, want it to mention %q", err, tc.wants)
			}
		})
	}
}

func TestFromEnv_DiskSizeMBRejectsBadValues(t *testing.T) {
	for _, tc := range []struct {
		name  string
		size  string
		mode  string
		wants string
	}{
		{name: "not a number", size: "8G", mode: DiskModeOverlay, wants: envDiskSizeMB},
		{name: "negative", size: "-1", mode: DiskModeOverlay, wants: "negative"},
		// The overlay is the only disk granule creates; in the other two
		// modes the guest is on the image itself, which is shared and
		// never resized -- so asking for a size there can't be honoured.
		{name: "persistent mode", size: "8192", mode: DiskModePersistent, wants: DiskModeOverlay},
		{name: "readonly mode", size: "8192", mode: DiskModeReadOnly, wants: DiskModeOverlay},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv(envDiskImage, writeTempFile(t, "disk.img"))
			t.Setenv(envFirmware, writeTempFile(t, "fw"))
			t.Setenv(envDiskMode, tc.mode)
			t.Setenv(envDiskSizeMB, tc.size)

			_, err := FromEnv()
			if err == nil {
				t.Fatalf("FromEnv() = nil, want an error for %s=%q with %s=%s", envDiskSizeMB, tc.size, envDiskMode, tc.mode)
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Errorf("FromEnv() error = %v, want it to mention %q", err, tc.wants)
			}
		})
	}
}

// TestPrepareOverlaySizesANewOverlay: a VM told how big a disk it wants
// gets an overlay presenting exactly that, rather than the source image's
// size, from its very first boot.
func TestPrepareOverlaySizesANewOverlay(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "disk.img")
	if err := os.WriteFile(source, make([]byte, 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	overlay := filepath.Join(dir, "overlay", "disk.qcow2")

	cfg := Config{
		DiskMode:    DiskModeOverlay,
		OverlayPath: overlay,
		DiskSizeMB:  4096,
		Disks:       []Disk{{Path: source}},
	}
	if err := cfg.PrepareOverlay(); err != nil {
		t.Fatalf("PrepareOverlay() error = %v", err)
	}
	size, err := qcow2.VirtualSize(overlay)
	if err != nil {
		t.Fatalf("VirtualSize() error = %v", err)
	}
	if want := int64(4096) * 1024 * 1024; size != want {
		t.Errorf("overlay virtual size = %d, want %d", size, want)
	}
}

func TestPrepareOverlayCustomExtentSize(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "disk.img")
	if err := os.WriteFile(source, make([]byte, 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	overlay := filepath.Join(dir, "overlay", "disk.qcow2")

	cfg := Config{
		DiskMode:     DiskModeOverlay,
		OverlayPath:  overlay,
		DiskSizeMB:   128,
		ExtentSizeMB: 32,
		Disks:        []Disk{{Path: source}},
	}
	if err := cfg.PrepareOverlay(); err != nil {
		t.Fatalf("PrepareOverlay() error = %v", err)
	}
	size, err := qcow2.VirtualSize(overlay)
	if err != nil {
		t.Fatalf("VirtualSize() error = %v", err)
	}
	if want := int64(128) * 1024 * 1024; size != want {
		t.Errorf("overlay size = %d, want %d", size, want)
	}
}

// TestPrepareOverlayGrowsAnExistingOverlay is the restart case: the
// overlay from an earlier boot is kept (whatever the guest wrote is in
// it), and raising the configured size grows it in place before the VM
// starts rather than being ignored until someone deletes the container.
func TestPrepareOverlayGrowsAnExistingOverlay(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "disk.img")
	if err := os.WriteFile(source, make([]byte, 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	overlay := filepath.Join(dir, "overlay", "disk.qcow2")

	first := Config{DiskMode: DiskModeOverlay, OverlayPath: overlay, DiskSizeMB: 1024, Disks: []Disk{{Path: source}}}
	if err := first.PrepareOverlay(); err != nil {
		t.Fatalf("PrepareOverlay() error = %v", err)
	}

	second := Config{DiskMode: DiskModeOverlay, OverlayPath: overlay, DiskSizeMB: 4096, Disks: []Disk{{Path: source}}}
	if err := second.PrepareOverlay(); err != nil {
		t.Fatalf("PrepareOverlay() on an existing overlay error = %v", err)
	}
	size, err := qcow2.VirtualSize(overlay)
	if err != nil {
		t.Fatalf("VirtualSize() error = %v", err)
	}
	if want := int64(4096) * 1024 * 1024; size != want {
		t.Errorf("overlay virtual size = %d, want it grown to %d", size, want)
	}
	if second.Disks[0].Path != overlay {
		t.Errorf("Disks[0].Path = %q, want the overlay %q", second.Disks[0].Path, overlay)
	}

	// Shrinking would leave the guest's filesystem spanning clusters
	// past the new end, so a lowered size fails rather than boots.
	third := Config{DiskMode: DiskModeOverlay, OverlayPath: overlay, DiskSizeMB: 2048, Disks: []Disk{{Path: source}}}
	if err := third.PrepareOverlay(); err == nil {
		t.Error("PrepareOverlay() = nil for a shrink, want an error")
	}
}

// TestPrepareOverlayRejectsASizeSmallerThanTheImage: an overlay reads
// through to the image for everything the guest hasn't written, so one
// smaller than the image would present a truncated root filesystem.
func TestPrepareOverlayRejectsASizeSmallerThanTheImage(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "disk.img")
	if err := os.WriteFile(source, make([]byte, 4<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	overlay := filepath.Join(dir, "overlay", "disk.qcow2")

	cfg := Config{DiskMode: DiskModeOverlay, OverlayPath: overlay, DiskSizeMB: 1, Disks: []Disk{{Path: source}}}
	err := cfg.PrepareOverlay()
	if err == nil {
		t.Fatal("PrepareOverlay() = nil, want an error for a size below the source image's")
	}
	if !strings.Contains(err.Error(), "truncated") {
		t.Errorf("PrepareOverlay() error = %v, want it to explain the guest's filesystem would be truncated", err)
	}
	if _, statErr := os.Stat(overlay); !os.IsNotExist(statErr) {
		t.Error("an overlay was created despite the rejected size")
	}
}

// guestWithContract writes a disk image and, if text is non-empty, the
// contract declaration beside it, and returns the disk's path. The two
// have to share a directory: a declaration is only ever read from beside
// the disk it describes.
func guestWithContract(t *testing.T, text string) string {
	t.Helper()
	dir := t.TempDir()
	disk := filepath.Join(dir, "disk.img")
	if err := os.WriteFile(disk, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if text != "" {
		if err := os.WriteFile(filepath.Join(dir, guestimage.ContractSidecarName), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return disk
}

// TestFromEnv_GuestContractAccepted: a guest declaring what this granule
// implements boots, and the declaration is carried on the Config so the
// boot can say which version it accepted.
func TestFromEnv_GuestContractAccepted(t *testing.T) {
	clearEnv(t)
	t.Setenv(envDiskImage, guestWithContract(t, fmt.Sprintf("%d\n", guestimage.Contract)))
	t.Setenv(envKernel, writeTempFile(t, "vmlinux"))

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if cfg.GuestContract.State != guestimage.ContractSupported || cfg.GuestContract.Version != guestimage.Contract {
		t.Errorf("GuestContract = %+v, want a supported version %d", cfg.GuestContract, guestimage.Contract)
	}
}

// TestFromEnv_GuestContractAbsentIsVersionZero: an image assembled before
// the declaration existed boots, and is reported as undeclared rather
// than as declaring version 0 -- the distinction an operator needs to
// find the images a later floor bump will stop. See
// pkg/guestimage's ContractUndeclaredVersion for why this is
// accepted at all.
func TestFromEnv_GuestContractAbsentIsVersionZero(t *testing.T) {
	clearEnv(t)
	t.Setenv(envDiskImage, guestWithContract(t, ""))
	t.Setenv(envKernel, writeTempFile(t, "vmlinux"))

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v, want an undeclared guest to boot", err)
	}
	if cfg.GuestContract.State != guestimage.ContractUndeclared {
		t.Errorf("GuestContract = %+v, want state %s", cfg.GuestContract, guestimage.ContractUndeclared)
	}
	if cfg.GuestContract.Version != guestimage.ContractUndeclaredVersion {
		t.Errorf("GuestContract.Version = %d, want %d", cfg.GuestContract.Version, guestimage.ContractUndeclaredVersion)
	}
}

// TestFromEnv_GuestContractFromTheFutureIsRefused: the boot fails, before
// anything has been started, with both versions in the message. A guest
// built against a newer granule expects guest-side machinery this one does
// not provide, which is a VM that looks healthy from outside and cannot
// be reached.
func TestFromEnv_GuestContractFromTheFutureIsRefused(t *testing.T) {
	clearEnv(t)
	future := guestimage.Contract + 1
	t.Setenv(envDiskImage, guestWithContract(t, fmt.Sprintf("%d\n", future)))
	t.Setenv(envKernel, writeTempFile(t, "vmlinux"))

	_, err := FromEnv()
	if err == nil {
		t.Fatalf("FromEnv() = nil error for a guest declaring contract version %d", future)
	}
	for _, want := range []string{strconv.Itoa(future), strconv.Itoa(guestimage.Contract)} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to name version %s", err, want)
		}
	}
}

// TestFromEnv_GuestContractUnparseableIsRefused: present and unreadable
// is not the same as absent. Absence means "older than the declaration";
// a file that is there and says nothing usable means something is wrong
// with the image, and guessing which is worse than refusing.
func TestFromEnv_GuestContractUnparseableIsRefused(t *testing.T) {
	clearEnv(t)
	t.Setenv(envDiskImage, guestWithContract(t, "this is not a declaration\n"))
	t.Setenv(envKernel, writeTempFile(t, "vmlinux"))

	if _, err := FromEnv(); err == nil {
		t.Fatal("FromEnv() = nil error for a declaration that does not parse")
	}
}

// TestFromEnv_GuestContractMinRaisesTheFloor: an operator who packs every
// guest they run can refuse an undeclared one today, and the refusal says
// it was their own policy that did it.
func TestFromEnv_GuestContractMinRaisesTheFloor(t *testing.T) {
	clearEnv(t)
	t.Setenv(envDiskImage, guestWithContract(t, ""))
	t.Setenv(envKernel, writeTempFile(t, "vmlinux"))
	t.Setenv(envGuestContractMin, strconv.Itoa(guestimage.Contract))

	_, err := FromEnv()
	if err == nil {
		t.Fatalf("FromEnv() = nil error for an undeclared guest under %s=%d", envGuestContractMin, guestimage.Contract)
	}
	if !strings.Contains(err.Error(), envGuestContractMin) {
		t.Errorf("error = %v, want it to name %s as what refused the boot", err, envGuestContractMin)
	}
}

// TestFromEnv_GuestContractMinOnlyTightens: the check cannot be turned
// into a warning by lowering the floor under it, and cannot be set to a
// value no guest could ever meet.
func TestFromEnv_GuestContractMinOnlyTightens(t *testing.T) {
	for _, tc := range []struct{ name, value string }{
		{"below the built-in floor", strconv.Itoa(guestimage.MinimumBootContract - 1)},
		{"above what this granule implements", strconv.Itoa(guestimage.Contract + 1)},
		{"not a number", "latest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv(envDiskImage, guestWithContract(t, fmt.Sprintf("%d\n", guestimage.Contract)))
			t.Setenv(envKernel, writeTempFile(t, "vmlinux"))
			t.Setenv(envGuestContractMin, tc.value)

			if _, err := FromEnv(); err == nil {
				t.Errorf("FromEnv() = nil error for %s=%q", envGuestContractMin, tc.value)
			}
		})
	}
}

// TestFromEnv_GuestContractIsReadBesideTheDiskBeingBooted: the bundled
// guest's declaration must not be what validates somebody else's disk.
// CHV_DISK_IMAGE moves the disk; the declaration moves with it.
func TestFromEnv_GuestContractIsReadBesideTheDiskBeingBooted(t *testing.T) {
	clearEnv(t)
	disk := guestWithContract(t, fmt.Sprintf("%d\n", guestimage.Contract+3))
	t.Setenv(envDiskImage, disk)
	t.Setenv(envKernel, writeTempFile(t, "vmlinux"))

	_, err := FromEnv()
	if err == nil {
		t.Fatal("FromEnv() = nil error, want the declaration beside CHV_DISK_IMAGE to be the one that is checked")
	}
	if !strings.Contains(err.Error(), guestimage.ContractSidecarPath(disk)) {
		t.Errorf("error = %v, want it to name %s rather than the bundled guest's own declaration", err, guestimage.ContractSidecarPath(disk))
	}
}

func TestFromEnv_Filesystems(t *testing.T) {
	clearEnv(t)
	t.Setenv(envDiskImage, writeTempFile(t, "disk.img"))
	t.Setenv(envKernel, writeTempFile(t, "vmlinux"))
	t.Setenv(envFS, "tag=shared,shared_dir=/host/share,readonly=off;mydata:/data:ro")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if len(cfg.Filesystems) != 2 {
		t.Fatalf("len(Filesystems) = %d, want 2", len(cfg.Filesystems))
	}
	if cfg.Filesystems[0].Tag != "shared" || cfg.Filesystems[0].SharedDir != "/host/share" || cfg.Filesystems[0].ReadOnly {
		t.Errorf("Filesystem[0] = %+v, unexpected", cfg.Filesystems[0])
	}
	if cfg.Filesystems[1].Tag != "mydata" || cfg.Filesystems[1].SharedDir != "/data" || !cfg.Filesystems[1].ReadOnly {
		t.Errorf("Filesystem[1] = %+v, unexpected", cfg.Filesystems[1])
	}
}

func TestFromEnv_StorageBackendExtents(t *testing.T) {
	clearEnv(t)
	tmp := t.TempDir()
	extDir := filepath.Join(tmp, "disk.extents")
	if _, err := extents.Init(extDir, 128<<20, 16<<20); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envDiskImage, extDir)
	t.Setenv(envKernel, writeTempFile(t, "vmlinux"))
	t.Setenv(envStorageBackend, StorageBackendExtents)
	t.Setenv(envDiskMode, DiskModePersistent)
	t.Setenv(envExtentSizeMB, "16")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if cfg.StorageBackend != StorageBackendExtents {
		t.Errorf("StorageBackend = %q, want %q", cfg.StorageBackend, StorageBackendExtents)
	}
	if cfg.Disks[0].VhostUserSocket != guestimage.VhostBlkSocket {
		t.Errorf("VhostUserSocket = %q, want %q", cfg.Disks[0].VhostUserSocket, guestimage.VhostBlkSocket)
	}
}

func TestPrepareOverlay_StorageBackendExtents(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "disk.img")
	if err := os.WriteFile(source, make([]byte, 4<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	overlay := filepath.Join(dir, "overlay", "disk.extents")

	cfg := Config{
		StorageBackend: StorageBackendExtents,
		DiskMode:       DiskModeOverlay,
		OverlayPath:    overlay,
		DiskSizeMB:     64,
		ExtentSizeMB:   16,
		Disks:          []Disk{{Path: source}},
	}
	if err := cfg.PrepareOverlay(); err != nil {
		t.Fatalf("PrepareOverlay() error = %v", err)
	}
	if cfg.Disks[0].Path != overlay {
		t.Errorf("Disks[0].Path = %q, want %q", cfg.Disks[0].Path, overlay)
	}
	if cfg.Disks[0].BasePath != source {
		t.Errorf("Disks[0].BasePath = %q, want %q", cfg.Disks[0].BasePath, source)
	}
	if cfg.Disks[0].VhostUserSocket != guestimage.VhostBlkSocket {
		t.Errorf("Disks[0].VhostUserSocket = %q, want %q", cfg.Disks[0].VhostUserSocket, guestimage.VhostBlkSocket)
	}
	vsize, err := extents.VirtualSize(overlay)
	if err != nil {
		t.Fatalf("extents.VirtualSize() error = %v", err)
	}
	if want := int64(64) << 20; vsize != want {
		t.Errorf("overlay virtual size = %d, want %d", vsize, want)
	}
}

func TestPrepareAuxiliaryDisks(t *testing.T) {
	dir := t.TempDir()
	auxPath := filepath.Join(dir, "sub", "aux.img")
	cfg := Config{
		Disks: []Disk{
			{Path: filepath.Join(dir, "root.img")},
			{Path: auxPath},
		},
	}
	if err := cfg.PrepareAuxiliaryDisks(); err != nil {
		t.Fatalf("PrepareAuxiliaryDisks() error = %v", err)
	}
	info, err := os.Stat(auxPath)
	if err != nil {
		t.Fatalf("auxiliary disk not created: %v", err)
	}
	if info.Size() != 1024*1024*1024 {
		t.Errorf("auxiliary disk size = %d, want 1GiB", info.Size())
	}
}

func TestFromEnv_DiskParameters(t *testing.T) {
	clearEnv(t)
	t.Setenv(envDiskImage, writeTempFile(t, "disk.img"))
	t.Setenv(envFirmware, writeTempFile(t, "fw"))
	t.Setenv(envDiskNumQueues, "4")
	t.Setenv(envDiskIOThreads, "true")
	t.Setenv(envDiskIOEngine, "io_uring")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if cfg.DiskNumQueues != 4 {
		t.Errorf("DiskNumQueues = %d, want 4", cfg.DiskNumQueues)
	}
	if !cfg.DiskIOThreads {
		t.Errorf("DiskIOThreads = %v, want true", cfg.DiskIOThreads)
	}
	if cfg.DiskIOEngine != "io_uring" {
		t.Errorf("DiskIOEngine = %q, want \"io_uring\"", cfg.DiskIOEngine)
	}
	if len(cfg.Disks) < 1 {
		t.Fatalf("len(Disks) = %d, want at least 1", len(cfg.Disks))
	}
	if cfg.Disks[0].NumQueues != 4 || !cfg.Disks[0].IOThreads || cfg.Disks[0].IOEngine != "io_uring" {
		t.Errorf("Disks[0] = %+v, want NumQueues=4, IOThreads=true, IOEngine=io_uring", cfg.Disks[0])
	}
}

func TestFromEnv_DiskParametersRejectsBadValues(t *testing.T) {
	for _, tc := range []struct {
		name   string
		queues string
		engine string
		wants  string
	}{
		{name: "negative queues", queues: "-1", engine: "io_uring", wants: "negative"},
		{name: "bad queues format", queues: "abc", engine: "io_uring", wants: envDiskNumQueues},
		{name: "unknown io engine", queues: "2", engine: "bad_engine", wants: envDiskIOEngine},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv(envDiskImage, writeTempFile(t, "disk.img"))
			t.Setenv(envFirmware, writeTempFile(t, "fw"))
			t.Setenv(envDiskNumQueues, tc.queues)
			t.Setenv(envDiskIOEngine, tc.engine)

			_, err := FromEnv()
			if err == nil {
				t.Fatalf("FromEnv() = nil, want error containing %q", tc.wants)
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Errorf("FromEnv() error = %v, want it to contain %q", err, tc.wants)
			}
		})
	}
}

func TestFromEnv_VCPUsFallback(t *testing.T) {
	clearEnv(t)
	t.Setenv(envDiskImage, writeTempFile(t, "disk.img"))
	t.Setenv(envFirmware, writeTempFile(t, "fw"))
	t.Setenv(envVCPUs, "4")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if cfg.CPUs != 4 {
		t.Errorf("CPUs = %d, want 4 from %s", cfg.CPUs, envVCPUs)
	}

	// When CHV_CPUS is explicitly given, it takes precedence.
	t.Setenv(envCPUs, "8")
	cfg, err = FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if cfg.CPUs != 8 {
		t.Errorf("CPUs = %d, want 8 from %s over %s", cfg.CPUs, envCPUs, envVCPUs)
	}
}

func TestPrepareOverlayPreservesDiskParameters(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "disk.img")
	if err := os.WriteFile(source, make([]byte, 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	overlay := filepath.Join(dir, "overlay", "disk.qcow2")

	cfg := Config{
		DiskMode:      DiskModeOverlay,
		OverlayPath:   overlay,
		DiskSizeMB:    1024,
		DiskNumQueues: 4,
		DiskIOThreads: true,
		DiskIOEngine:  "io_uring",
		Disks: []Disk{{
			Path:      source,
			NumQueues: 4,
			IOThreads: true,
			IOEngine:  "io_uring",
		}},
	}
	if err := cfg.PrepareOverlay(); err != nil {
		t.Fatalf("PrepareOverlay() error = %v", err)
	}
	if cfg.Disks[0].NumQueues != 4 || !cfg.Disks[0].IOThreads || cfg.Disks[0].IOEngine != "io_uring" {
		t.Errorf("Disks[0] after PrepareOverlay = %+v, want disk parameters preserved", cfg.Disks[0])
	}

	// PrepareOverlay again on existing overlay
	if err := cfg.PrepareOverlay(); err != nil {
		t.Fatalf("PrepareOverlay() second run error = %v", err)
	}
	if cfg.Disks[0].NumQueues != 4 || !cfg.Disks[0].IOThreads || cfg.Disks[0].IOEngine != "io_uring" {
		t.Errorf("Disks[0] after second PrepareOverlay = %+v, want disk parameters preserved", cfg.Disks[0])
	}
}

// A Granule's `VM SET kernel-cmdline=quiet` arrives here as exactly
// "quiet" (docs/granulefile.md §5.5 records the value verbatim), and it
// is extra parameters rather than the whole command line: read as a
// replacement it took root= and console= away and the guest never
// booted.
func TestFromEnv_CmdlineIsExtraParameters(t *testing.T) {
	clearEnv(t)
	disk := writeTempFile(t, "disk.img")
	kernel := writeTempFile(t, "vmlinux")
	t.Setenv(envDiskImage, disk)
	t.Setenv(envKernel, kernel)
	t.Setenv(envCmdline, "nomodeset")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if cfg.Cmdline != defaultCmdline()+" nomodeset" {
		t.Errorf("Cmdline = %q, want granule's own line with nomodeset added", cfg.Cmdline)
	}
}

// The console a guest is told to use follows the architecture it runs on,
// and both answers are checked from whichever one this test runs on.
//
// The arm64 case is the one with a history: cloud-hypervisor gives an
// aarch64 guest a PL011, so a guest told console=ttyS0 there has been
// handed a device it does not have, prints nothing, and never listens on
// the vsock port "granule exec" dials -- a dead VM with no output to say
// why. Asserting the mapping rather than the running binary's own answer
// is what makes that case testable on an amd64 machine at all.
func TestGuestConsoleFollowsTheArchitectureTheGuestRunsOn(t *testing.T) {
	for _, tc := range []struct{ goarch, want string }{
		{"amd64", "ttyS0"},
		{"arm64", "ttyAMA0"},
		{"386", "ttyS0"},
		// Anything granule has not been run on keeps today's answer
		// rather than being guessed at; see guestConsoleFor.
		{"riscv64", "ttyS0"},
	} {
		if got := guestConsoleFor(tc.goarch); got != tc.want {
			t.Errorf("guestConsoleFor(%q) = %q, want %q", tc.goarch, got, tc.want)
		}
	}

	// And the exported one answers for the binary actually running.
	if got, want := GuestConsole(), guestConsoleFor(runtime.GOARCH); got != want {
		t.Errorf("GuestConsole() = %q, want %q for GOARCH %q", got, want, runtime.GOARCH)
	}
}

// defaultCmdline carries that console, so the line the VM container falls
// back to names a device the guest has.
func TestTheDefaultCmdlineNamesThisArchitecturesConsole(t *testing.T) {
	if want := "console=" + GuestConsole() + " "; !strings.HasPrefix(defaultCmdline(), want) {
		t.Errorf("defaultCmdline() = %q, want it to start %q", defaultCmdline(), want)
	}
}

func TestCmdlineWith(t *testing.T) {
	const base = "console=ttyS0 root=/dev/vda rw"
	for _, tc := range []struct {
		name  string
		extra string
		want  string
	}{
		{"nothing given keeps granule's own", "", base},
		{"blanks are nothing given", "   ", base},
		{"a switch is added", "quiet", base + " quiet"},
		{"several are added in order", "quiet nomodeset loglevel=3", base + " quiet nomodeset loglevel=3"},
		{"a named parameter replaces granule's", "console=ttyS1", "root=/dev/vda rw console=ttyS1"},
		// A line naming every parameter granule sets: composing it
		// leaves it exactly as it is.
		{"a whole command line means itself", "console=ttyS0 root=/dev/vda ro", "console=ttyS0 root=/dev/vda ro"},
		{"ro replaces rw, being the same switch", "ro", "console=ttyS0 root=/dev/vda ro"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := CmdlineWith(base, tc.extra); got != tc.want {
				t.Errorf("CmdlineWith(%q, %q) = %q, want %q", base, tc.extra, got, tc.want)
			}
		})
	}
}

// A comma inside a path that BuildArgs puts in one of cloud-hypervisor's
// "key=value,..." option lists would start another option: here, a
// readonly=off that undoes the readonly=on a read-only disk is given.
func TestFromEnv_CommaInOptionValueRejected(t *testing.T) {
	for _, env := range []string{envDiskImage, envAPISocket, envVsockSocket, envSnapshotPath, envDiskOverlayPath} {
		t.Run(env, func(t *testing.T) {
			clearEnv(t)
			dir := t.TempDir()
			t.Setenv(envDiskImage, writeTempFile(t, "disk.img"))
			t.Setenv(envFirmware, writeTempFile(t, "fw"))
			t.Setenv(envDiskMode, DiskModeReadOnly)
			// A file that really exists under the comma'd name, so it
			// is the comma being refused rather than a missing path.
			bad := filepath.Join(dir, "disk.img,readonly=off")
			if err := os.WriteFile(bad, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			t.Setenv(env, bad)

			_, err := FromEnv()
			if err == nil || !strings.Contains(err.Error(), "comma") {
				t.Fatalf("FromEnv() with %s=%q: error = %v, want a refusal naming the comma", env, bad, err)
			}
		})
	}
}

func TestFromEnv_CommaInFilesystemRejected(t *testing.T) {
	clearEnv(t)
	t.Setenv(envDiskImage, writeTempFile(t, "disk.img"))
	t.Setenv(envFirmware, writeTempFile(t, "fw"))
	t.Setenv(envFS, "tag=data,shared_dir=/srv/a")
	if _, err := FromEnv(); err != nil {
		t.Fatalf("FromEnv() with a well-formed %s: %v", envFS, err)
	}
	t.Setenv(envFS, "data:/srv/a,b")
	if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), "comma") {
		t.Fatalf("FromEnv() with a comma in a %s shared_dir: error = %v, want a refusal naming the comma", envFS, err)
	}
}

// The token is the first whitespace-separated field of every signal line,
// so a token with whitespace in it could never match.
func TestFromEnv_MemAgentTokenWithWhitespaceRejected(t *testing.T) {
	clearEnv(t)
	t.Setenv(envDiskImage, writeTempFile(t, "disk.img"))
	t.Setenv(envFirmware, writeTempFile(t, "fw"))
	t.Setenv(envMemAgentToken, "abc init=/bin/sh")
	if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), envMemAgentToken) {
		t.Fatalf("FromEnv() error = %v, want a refusal naming %s", err, envMemAgentToken)
	}

	t.Setenv(envMemAgentToken, "0123abcd")
	if _, err := FromEnv(); err != nil {
		t.Fatalf("FromEnv() with a plain token: %v", err)
	}
}

// The memory agent's token reaches the guest over vsock (see
// pkg/memagent's "The token"), so a VM with the vsock device turned off
// cannot run the agent: it would listen for signals the guest has no
// token to send.
func TestFromEnv_MemAgentRequiresVsock(t *testing.T) {
	clearEnv(t)
	t.Setenv(envDiskImage, writeTempFile(t, "disk.img"))
	t.Setenv(envFirmware, writeTempFile(t, "fw"))
	t.Setenv(envMemAgent, "true")
	t.Setenv(envVsockSocket, "")

	_, err := FromEnv()
	if err == nil || !strings.Contains(err.Error(), envVsockSocket) {
		t.Fatalf("FromEnv() error = %v, want one naming %s", err, envVsockSocket)
	}
}

// GRANULE_WORKLOAD_STDOUT/STDERR are read as given, must be absolute, and
// need the vsock device the streams leave the guest over.
func TestFromEnv_WorkloadStreams(t *testing.T) {
	clearEnv(t)
	t.Setenv(envDiskImage, writeTempFile(t, "disk.img"))
	t.Setenv(envFirmware, writeTempFile(t, "fw"))

	cfg, err := FromEnv()
	if err != nil || cfg.WorkloadStdout != "" || cfg.WorkloadStderr != "" {
		t.Fatalf("unset: cfg=%q/%q err=%v", cfg.WorkloadStdout, cfg.WorkloadStderr, err)
	}

	t.Setenv(envWorkloadStdout, "/run/silo/stdout")
	t.Setenv(envWorkloadStderr, "/run/silo/stderr")
	cfg, err = FromEnv()
	if err != nil || cfg.WorkloadStdout != "/run/silo/stdout" || cfg.WorkloadStderr != "/run/silo/stderr" {
		t.Fatalf("set: cfg=%q/%q err=%v", cfg.WorkloadStdout, cfg.WorkloadStderr, err)
	}
	// The guest's cue to wait for this host before powering off.
	if !strings.Contains(cfg.Cmdline, "granule.workload_streams=1") {
		t.Errorf("Cmdline = %q, want it to ask the guest for the streams", cfg.Cmdline)
	}

	t.Setenv(envWorkloadStderr, "stderr")
	if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), envWorkloadStderr) {
		t.Errorf("relative path: err = %v, want one naming %s", err, envWorkloadStderr)
	}

	t.Setenv(envWorkloadStderr, "")
	t.Setenv(envVsockSocket, "")
	if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), envVsockSocket) {
		t.Errorf("no vsock: err = %v, want one naming %s", err, envVsockSocket)
	}
}

// GRANULE_WORKLOAD_EXIT_FILE is read as given, must be absolute, and needs
// the vsock device the status leaves the guest over.
func TestFromEnv_WorkloadExitFile(t *testing.T) {
	clearEnv(t)
	t.Setenv(envDiskImage, writeTempFile(t, "disk.img"))
	t.Setenv(envFirmware, writeTempFile(t, "fw"))

	cfg, err := FromEnv()
	if err != nil || cfg.WorkloadExitFile != "" {
		t.Fatalf("unset: cfg=%q err=%v", cfg.WorkloadExitFile, err)
	}
	t.Setenv(envWorkloadExitFile, "/run/silo/exit")
	if cfg, err = FromEnv(); err != nil || cfg.WorkloadExitFile != "/run/silo/exit" {
		t.Fatalf("set: cfg=%q err=%v", cfg.WorkloadExitFile, err)
	}
	t.Setenv(envWorkloadExitFile, "exit")
	if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), envWorkloadExitFile) {
		t.Errorf("relative path: err = %v, want one naming %s", err, envWorkloadExitFile)
	}
	t.Setenv(envWorkloadExitFile, "/run/silo/exit")
	t.Setenv(envVsockSocket, "")
	if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), envVsockSocket) {
		t.Errorf("no vsock: err = %v, want one naming %s", err, envVsockSocket)
	}
}

// CHV_CONTAINER_OVERRIDE is parsed (and refused when unusable) at start,
// and PrepareContainerOverride writes it to a read-only disk attached
// after every other one, carrying the serial the guest finds it by.
func TestFromEnv_ContainerOverride(t *testing.T) {
	clearEnv(t)
	disk := writeTempFile(t, "disk.img")
	extra := writeTempFile(t, "extra.img")
	t.Setenv(envDiskImage, disk)
	t.Setenv(envDiskMode, DiskModePersistent)
	t.Setenv(envFirmware, writeTempFile(t, "fw"))
	t.Setenv(envExtraDisks, extra+":rw")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if cfg.ContainerOverride != nil {
		t.Errorf("ContainerOverride = %+v with %s unset", cfg.ContainerOverride, envContainerOverride)
	}
	if err := cfg.PrepareContainerOverride(); err != nil || len(cfg.Disks) != 2 {
		t.Errorf("with no override: err=%v Disks=%+v", err, cfg.Disks)
	}

	t.Setenv(envContainerOverride, `{"entrypoint":["/bin/app"],"env":["A=1"],"user":"web","working_dir":"/srv"}`)
	cfg, err = FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if cfg.ContainerOverride == nil || cfg.ContainerOverride.User != "web" || cfg.ContainerOverrideDisk != defaultContainerOverrideDisk {
		t.Fatalf("ContainerOverride = %+v at %q", cfg.ContainerOverride, cfg.ContainerOverrideDisk)
	}

	cfg.ContainerOverrideDisk = filepath.Join(t.TempDir(), "overlay", "container-override.img")
	for i := 0; i < 2; i++ {
		// Twice, as a restart in the same container does: one disk, not two.
		if err := cfg.PrepareContainerOverride(); err != nil {
			t.Fatal(err)
		}
	}
	want := []Disk{
		{Path: disk},
		{Path: extra},
		{Path: cfg.ContainerOverrideDisk, ReadOnly: true, Serial: containerspec.OverrideSerial},
	}
	if len(cfg.Disks) != len(want) {
		t.Fatalf("Disks = %+v, want %+v", cfg.Disks, want)
	}
	for i := range want {
		if cfg.Disks[i] != want[i] {
			t.Errorf("Disks[%d] = %+v, want %+v", i, cfg.Disks[i], want[i])
		}
	}
	f, err := os.Open(cfg.ContainerOverrideDisk)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, ok, err := containerspec.DecodeOverrideDisk(f)
	if err != nil || !ok || got.User != "web" || got.Entrypoint[0] != "/bin/app" {
		t.Errorf("the disk carries %+v (ok=%v, err=%v)", got, ok, err)
	}

	for _, bad := range []string{`{"working_dir":"rel"}`, `{"entrypont":["x"]}`, `[1]`} {
		t.Setenv(envContainerOverride, bad)
		if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), envContainerOverride) {
			t.Errorf("%s=%s: err = %v, want it refused naming the variable", envContainerOverride, bad, err)
		}
	}
}

// GRANULE_WORKLOAD_STDIN and GRANULE_WORKLOAD_TTY open the container's
// stdin and terminal through the override, merged into whatever
// CHV_CONTAINER_OVERRIDE already says, and GRANULE_WORKLOAD_RESIZE needs
// the terminal it resizes.
func TestFromEnv_WorkloadStdin(t *testing.T) {
	clearEnv(t)
	t.Setenv(envDiskImage, writeTempFile(t, "disk.img"))
	t.Setenv(envFirmware, writeTempFile(t, "fw"))

	cfg, err := FromEnv()
	if err != nil || cfg.WorkloadStdin != "" || cfg.WorkloadTTY || cfg.ContainerOverride != nil {
		t.Fatalf("unset: cfg=%q/%v/%v err=%v", cfg.WorkloadStdin, cfg.WorkloadTTY, cfg.ContainerOverride, err)
	}

	t.Setenv(envWorkloadStdin, "/run/silo/stdin")
	cfg, err = FromEnv()
	if err != nil || cfg.WorkloadStdin != "/run/silo/stdin" {
		t.Fatalf("stdin: cfg=%q err=%v", cfg.WorkloadStdin, err)
	}
	o := cfg.ContainerOverride
	if o == nil || o.Stdin == nil || !*o.Stdin || o.Tty != nil || cfg.ContainerOverrideDisk == "" {
		t.Fatalf("stdin alone: override = %+v, disk %q; want stdin on and nothing else", o, cfg.ContainerOverrideDisk)
	}

	t.Setenv(envContainerOverride, `{"cmd":["/bin/sh"]}`)
	t.Setenv(envWorkloadTTY, "on")
	t.Setenv(envWorkloadResize, "/run/silo/resize")
	cfg, err = FromEnv()
	if err != nil || !cfg.WorkloadTTY || cfg.WorkloadResize != "/run/silo/resize" {
		t.Fatalf("tty: cfg=%v/%q err=%v", cfg.WorkloadTTY, cfg.WorkloadResize, err)
	}
	o = cfg.ContainerOverride
	if o == nil || len(o.Cmd) != 1 || o.Stdin == nil || !*o.Stdin || o.Tty == nil || !*o.Tty {
		t.Fatalf("with an override: %+v, want its cmd kept and stdin and tty on", o)
	}
	// Input alone does not ask the guest to wait for its output at exit.
	if strings.Contains(cfg.Cmdline, "granule.workload_streams") {
		t.Errorf("Cmdline = %q, asks for the streams nobody named", cfg.Cmdline)
	}

	t.Setenv(envWorkloadTTY, "")
	if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), envWorkloadTTY) {
		t.Errorf("resize without tty: err = %v, want one naming %s", err, envWorkloadTTY)
	}
	t.Setenv(envWorkloadResize, "")

	t.Setenv(envWorkloadStdin, "stdin")
	if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), envWorkloadStdin) {
		t.Errorf("relative path: err = %v, want one naming %s", err, envWorkloadStdin)
	}

	t.Setenv(envWorkloadStdin, "/run/silo/stdin")
	t.Setenv(envVsockSocket, "")
	if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), envVsockSocket) {
		t.Errorf("no vsock: err = %v, want one naming %s", err, envVsockSocket)
	}
}

func TestParseFilesystemDestination(t *testing.T) {
	for _, tc := range []struct {
		spec string
		want Filesystem
	}{
		{"data:/host/data:/data", Filesystem{Tag: "data", SharedDir: "/host/data", Destination: "/data"}},
		{"data:/host/data:/data:ro", Filesystem{Tag: "data", SharedDir: "/host/data", Destination: "/data", ReadOnly: true}},
		{"data:/run/vfs.sock:/var/lib/app:rw", Filesystem{Tag: "data", Socket: "/run/vfs.sock", Destination: "/var/lib/app"}},
		{"data:/host/data:ro", Filesystem{Tag: "data", SharedDir: "/host/data", ReadOnly: true}},
		{"tag=data,socket=/run/vfs.sock,destination=/data,readonly=on", Filesystem{Tag: "data", Socket: "/run/vfs.sock", Destination: "/data", ReadOnly: true}},
		{"tag=data,socket=/run/vfs.sock,dest=/data", Filesystem{Tag: "data", Socket: "/run/vfs.sock", Destination: "/data"}},
		{"tag=data,socket=/run/vfs.sock,target=/data", Filesystem{Tag: "data", Socket: "/run/vfs.sock", Destination: "/data"}},
	} {
		got, err := parseFilesystem(tc.spec)
		if err != nil {
			t.Errorf("parseFilesystem(%q) error = %v", tc.spec, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseFilesystem(%q) = %+v, want %+v", tc.spec, got, tc.want)
		}
	}
	for _, bad := range []string{
		"data:/host/data:data",
		"data:/host/data:/data:rw:extra",
		"data:/host/data:/data:readonly",
		"tag=data,socket=/run/vfs.sock,destination=data",
	} {
		if _, err := parseFilesystem(bad); err == nil {
			t.Errorf("parseFilesystem(%q) accepted it", bad)
		}
	}
}

func TestFromEnv_FilesystemDestinationsReachTheOverride(t *testing.T) {
	clearEnv(t)
	t.Setenv(envDiskImage, writeTempFile(t, "disk.img"))
	t.Setenv(envKernel, writeTempFile(t, "vmlinux"))
	t.Setenv(envContainerOverride, `{"cmd":["sh"]}`)
	t.Setenv(envFS, "tag=bare,socket=/run/bare.sock")
	t.Setenv(envVolumes, "data:/run/data.sock:/data;conf:/run/conf.sock:/etc/app:ro")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if len(cfg.Filesystems) != 3 {
		t.Fatalf("len(Filesystems) = %d, want 3", len(cfg.Filesystems))
	}
	if cfg.ContainerOverride == nil {
		t.Fatalf("ContainerOverride = nil, want the mounts in it")
	}
	want := []containerspec.Mount{
		{Tag: "data", Destination: "/data"},
		{Tag: "conf", Destination: "/etc/app", ReadOnly: true},
	}
	if !reflect.DeepEqual(cfg.ContainerOverride.Mounts, want) {
		t.Errorf("ContainerOverride.Mounts = %+v, want %+v", cfg.ContainerOverride.Mounts, want)
	}
	if len(cfg.ContainerOverride.Cmd) != 1 {
		t.Errorf("the override's own cmd was lost: %+v", cfg.ContainerOverride)
	}
}

func TestFromEnv_FilesystemDestinationsWithoutOverride(t *testing.T) {
	clearEnv(t)
	t.Setenv(envDiskImage, writeTempFile(t, "disk.img"))
	t.Setenv(envKernel, writeTempFile(t, "vmlinux"))
	t.Setenv(envFS, "tag=data,socket=/run/data.sock,destination=/data")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if cfg.ContainerOverride == nil || len(cfg.ContainerOverride.Mounts) != 1 || cfg.ContainerOverrideDisk == "" {
		t.Errorf("ContainerOverride = %+v (disk %q), want one mount on a disk", cfg.ContainerOverride, cfg.ContainerOverrideDisk)
	}
}

func TestFromEnv_FilesystemDestinationsClash(t *testing.T) {
	clearEnv(t)
	t.Setenv(envDiskImage, writeTempFile(t, "disk.img"))
	t.Setenv(envKernel, writeTempFile(t, "vmlinux"))
	t.Setenv(envFS, "a:/run/a.sock:/data;b:/run/b.sock:/data/")
	if _, err := FromEnv(); err == nil {
		t.Errorf("FromEnv() accepted two shares on /data")
	}
}

func TestFromEnv_FilesystemsWithoutDestinationLeaveNoOverride(t *testing.T) {
	clearEnv(t)
	t.Setenv(envDiskImage, writeTempFile(t, "disk.img"))
	t.Setenv(envKernel, writeTempFile(t, "vmlinux"))
	t.Setenv(envFS, "tag=data,socket=/run/data.sock")
	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if cfg.ContainerOverride != nil {
		t.Errorf("ContainerOverride = %+v, want none", cfg.ContainerOverride)
	}
}
