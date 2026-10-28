package hypervisor

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/granule/pkg/config"
	"github.com/bwsalmon/granule/pkg/qcow2"
)

// fakechvPath is built once per test run from testdata/fakechv, which
// stands in for the real cloud-hypervisor binary: it serves the same
// vm.power-button / vmm.shutdown API endpoints and lets each test control
// how it reacts to them via environment variables.
var fakechvPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fakechv-build")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	fakechvPath = filepath.Join(dir, "fakechv")
	cmd := exec.Command("go", "build", "-o", fakechvPath, "./testdata/fakechv")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		panic("building fakechv test helper: " + err.Error())
	}

	os.Exit(m.Run())
}

func testConfig(t *testing.T) config.Config {
	t.Helper()
	return config.Config{
		Disks:           []config.Disk{{Path: "/nonexistent/disk.img"}},
		Firmware:        "/nonexistent/firmware",
		CPUs:            1,
		MemoryMB:        256,
		MemoryShared:    true,
		APISocket:       filepath.Join(t.TempDir(), "api.sock"),
		BinaryPath:      fakechvPath,
		ShutdownTimeout: 200 * time.Millisecond,
	}
}

func startRunner(t *testing.T, cfg config.Config, env []string) *Runner {
	t.Helper()
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}

	r := New(cfg)
	if err := r.Start(io.Discard, io.Discard); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() {
		if r.cmd.Process != nil {
			_ = r.cmd.Process.Kill()
		}
	})
	return r
}

func TestRunner_GracefulShutdownOnPowerButton(t *testing.T) {
	cfg := testConfig(t)
	r := startRunner(t, cfg, nil)

	done := make(chan struct{})
	go func() {
		r.Shutdown(context.Background())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown did not return in time")
	}

	err := r.Wait()
	if err != nil {
		t.Errorf("Wait() error = %v, want nil (guest exited 0 on power button)", err)
	}
}

func TestRunner_EscalatesToVMMShutdown(t *testing.T) {
	oldKillGrace := killGrace
	killGrace = 300 * time.Millisecond
	t.Cleanup(func() { killGrace = oldKillGrace })

	cfg := testConfig(t)
	cfg.ShutdownTimeout = 50 * time.Millisecond
	r := startRunner(t, cfg, []string{"FAKECHV_EXIT_ON_POWER_BUTTON=false"})

	done := make(chan struct{})
	go func() {
		r.Shutdown(context.Background())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown did not return in time")
	}

	if err := r.Wait(); err != nil {
		t.Errorf("Wait() error = %v, want nil (vmm.shutdown exited 0)", err)
	}
}

func TestRunner_EscalatesToSIGKILL(t *testing.T) {
	oldKillGrace := killGrace
	killGrace = 100 * time.Millisecond
	t.Cleanup(func() { killGrace = oldKillGrace })

	cfg := testConfig(t)
	cfg.ShutdownTimeout = 50 * time.Millisecond
	r := startRunner(t, cfg, []string{
		"FAKECHV_SERVE_API=false",
		"FAKECHV_IGNORE_SIGTERM=true",
	})

	done := make(chan struct{})
	go func() {
		r.Shutdown(context.Background())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Shutdown did not return in time (SIGKILL fallback never fired)")
	}

	err := r.Wait()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("Wait() error = %v, want *exec.ExitError from SIGKILL", err)
	}
}

func TestRunner_Start_InvalidBinary(t *testing.T) {
	cfg := testConfig(t)
	cfg.BinaryPath = "/no/such/binary"
	r := New(cfg)
	if err := r.Start(io.Discard, io.Discard); err == nil {
		t.Fatal("expected Start() to fail for a nonexistent binary")
	}
}

func TestRunner_Suspend_PublishesSnapshotAndResumes(t *testing.T) {
	cfg := testConfig(t)
	cfg.SnapshotPath = filepath.Join(t.TempDir(), "snapshot")
	r := startRunner(t, cfg, nil)

	if r.Restored() {
		t.Error("Restored() = true for a fresh boot with no existing snapshot")
	}

	readyCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := r.api.WaitReady(readyCtx); err != nil {
		t.Fatalf("waiting for fakechv's api socket: %v", err)
	}

	if err := r.Suspend(context.Background()); err != nil {
		t.Fatalf("Suspend() error = %v", err)
	}

	if _, err := os.Stat(cfg.SnapshotPath); err != nil {
		t.Errorf("snapshot not published at %s: %v", cfg.SnapshotPath, err)
	}
	if _, err := os.Stat(cfg.SnapshotPath + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("staging directory %s.tmp should not remain after a successful Suspend", cfg.SnapshotPath)
	}

	// fakechv's vm.pause/vm.resume just ack unconditionally, so the only
	// way to tell Suspend actually resumed (rather than leaving the vm
	// paused) is that a subsequent graceful shutdown still works.
	done := make(chan struct{})
	go func() {
		r.Shutdown(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown did not return in time after Suspend")
	}
}

// A snapshot is the guest's memory and the API socket is control of the
// VM, so neither may be left readable or connectable by anyone but their
// owner -- even though the VMM writes the snapshot's files 0644 here.
func TestRunner_SocketAndSnapshotArePrivate(t *testing.T) {
	cfg := testConfig(t)
	cfg.APISocket = filepath.Join(t.TempDir(), "run", "api.sock")
	cfg.SnapshotPath = filepath.Join(t.TempDir(), "snapshots", "snapshot")
	r := startRunner(t, cfg, nil)

	readyCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := r.api.WaitReady(readyCtx); err != nil {
		t.Fatalf("waiting for fakechv's api socket: %v", err)
	}
	if err := r.Suspend(context.Background()); err != nil {
		t.Fatalf("Suspend() error = %v", err)
	}
	t.Cleanup(func() { r.Shutdown(context.Background()) })

	for _, c := range []struct {
		path string
		want os.FileMode
	}{
		{filepath.Dir(cfg.APISocket), 0o700},
		{cfg.APISocket, 0o700},
		{filepath.Dir(cfg.SnapshotPath), 0o700},
		{cfg.SnapshotPath, 0o700},
		{filepath.Join(cfg.SnapshotPath, "memory-ranges"), 0o600},
	} {
		info, err := os.Stat(c.path)
		if err != nil {
			t.Errorf("stat %s: %v", c.path, err)
			continue
		}
		if got := info.Mode().Perm(); got&^c.want != 0 {
			t.Errorf("%s has mode %o, want no more than %o", c.path, got, c.want)
		}
	}
}

// The snapshot has to carry the guest's writable disk with it: a
// restored VM's memory believes in whatever the guest had written by the
// time it was paused, and in the default disk mode that lives in a qcow2
// overlay inside this container, which the next run (a new container, the whole
// reason for snapshotting to a volume) does not have.
func TestRunner_Suspend_SavesTheDiskOverlayIntoTheSnapshot(t *testing.T) {
	cfg := testConfig(t)
	dir := t.TempDir()
	cfg.SnapshotPath = filepath.Join(dir, "snapshot")
	cfg.DiskMode = config.DiskModeOverlay
	cfg.OverlayPath = filepath.Join(dir, "overlay", "disk.qcow2")
	if err := os.MkdirAll(filepath.Dir(cfg.OverlayPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.OverlayPath, []byte("guest wrote this"), 0o600); err != nil {
		t.Fatal(err)
	}

	r := startRunner(t, cfg, nil)
	readyCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := r.api.WaitReady(readyCtx); err != nil {
		t.Fatalf("waiting for fakechv's api socket: %v", err)
	}
	if err := r.Suspend(context.Background()); err != nil {
		t.Fatalf("Suspend() error = %v", err)
	}

	saved, err := os.ReadFile(filepath.Join(cfg.SnapshotPath, SnapshotDiskOverlayName))
	if err != nil {
		t.Fatalf("the snapshot carries no disk overlay: %v", err)
	}
	if string(saved) != "guest wrote this" {
		t.Errorf("saved overlay = %q, want the overlay's own contents", saved)
	}

	// The next run's half of it: the saved overlay goes back where the
	// snapshot's own config.json expects to find the disk.
	dst := filepath.Join(t.TempDir(), "overlay", "disk.qcow2")
	restored, err := RestoreDiskOverlay(cfg.SnapshotPath, dst)
	if err != nil {
		t.Fatalf("RestoreDiskOverlay() error = %v", err)
	}
	if !restored {
		t.Fatal("RestoreDiskOverlay() reported no overlay in a snapshot that has one")
	}
	back, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(back) != "guest wrote this" {
		t.Errorf("restored overlay = %q, want the overlay's own contents", back)
	}
	if _, err := os.Stat(dst + ".restoring"); !os.IsNotExist(err) {
		t.Errorf("the staging copy at %s.restoring was left behind", dst)
	}
}

func TestRunner_Suspend_SavesQcow2OverlayIntoTheSnapshot(t *testing.T) {
	cfg := testConfig(t)
	dir := t.TempDir()
	cfg.SnapshotPath = filepath.Join(dir, "snapshot")
	cfg.DiskMode = config.DiskModeOverlay
	cfg.OverlayPath = filepath.Join(dir, "overlay", "disk.qcow2")

	baseRaw := filepath.Join(dir, "base.img")
	if err := os.WriteFile(baseRaw, []byte("BASE DATA"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(baseRaw, 64*1024*1024); err != nil {
		t.Fatal(err)
	}

	if err := qcow2.WriteOverlay(cfg.OverlayPath, baseRaw, 64*1024*1024); err != nil {
		t.Fatal(err)
	}

	r := startRunner(t, cfg, nil)
	readyCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := r.api.WaitReady(readyCtx); err != nil {
		t.Fatalf("waiting for fakechv's api socket: %v", err)
	}
	if err := r.Suspend(context.Background()); err != nil {
		t.Fatalf("Suspend() error = %v", err)
	}

	// Verify qcow2 overlay was saved into snapshot
	savedOverlayPath := filepath.Join(cfg.SnapshotPath, SnapshotDiskOverlayName)
	if !qcow2.IsQcow2(savedOverlayPath) {
		t.Fatalf("saved overlay %s is not a valid qcow2 file", savedOverlayPath)
	}

	// Test RestoreDiskOverlay with qcow2
	dst := filepath.Join(t.TempDir(), "overlay", "disk.qcow2")
	restored, err := RestoreDiskOverlay(cfg.SnapshotPath, dst)
	if err != nil {
		t.Fatalf("RestoreDiskOverlay() error = %v", err)
	}
	if !restored {
		t.Fatal("RestoreDiskOverlay() reported no overlay in a snapshot that has one")
	}

	if !qcow2.IsQcow2(dst) {
		t.Fatalf("restored overlay %s is not a valid qcow2 file", dst)
	}
	hdr, err := qcow2.ReadHeader(dst)
	if err != nil {
		t.Fatal(err)
	}
	if hdr.BackingFile != baseRaw {
		t.Errorf("restored backing file = %q, want %q", hdr.BackingFile, baseRaw)
	}
}

// A snapshot from before granule saved the overlay, or one of a VM whose
// disk was never an overlay to begin with, has nothing to put back --
// and says so rather than failing the boot that asked.
func TestRestoreDiskOverlay_SnapshotWithoutOne(t *testing.T) {
	snap := filepath.Join(t.TempDir(), "snapshot")
	if err := os.Mkdir(snap, 0o755); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "disk.qcow2")

	restored, err := RestoreDiskOverlay(snap, dst)
	if err != nil {
		t.Fatalf("RestoreDiskOverlay() error = %v", err)
	}
	if restored {
		t.Error("RestoreDiskOverlay() claimed to restore an overlay that isn't in the snapshot")
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Errorf("something was written to %s anyway", dst)
	}
}

// Only the overlay is granule's to carry: the other two disk modes write
// to (or refuse to write to) an image that lives outside the container
// and is still there on the next run.
func TestRunner_Suspend_LeavesAPersistentDiskAlone(t *testing.T) {
	cfg := testConfig(t)
	dir := t.TempDir()
	cfg.SnapshotPath = filepath.Join(dir, "snapshot")
	cfg.DiskMode = config.DiskModePersistent
	cfg.OverlayPath = filepath.Join(dir, "overlay", "disk.qcow2")

	r := startRunner(t, cfg, nil)
	readyCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := r.api.WaitReady(readyCtx); err != nil {
		t.Fatalf("waiting for fakechv's api socket: %v", err)
	}
	if err := r.Suspend(context.Background()); err != nil {
		t.Fatalf("Suspend() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(cfg.SnapshotPath, SnapshotDiskOverlayName)); !os.IsNotExist(err) {
		t.Errorf("a persistent-disk VM's snapshot has an overlay in it (stat err = %v)", err)
	}
}

func TestRunner_Restored_TrueWhenSnapshotAlreadyExists(t *testing.T) {
	cfg := testConfig(t)
	cfg.SnapshotPath = filepath.Join(t.TempDir(), "snapshot")
	if err := os.Mkdir(cfg.SnapshotPath, 0o755); err != nil {
		t.Fatal(err)
	}

	r := startRunner(t, cfg, nil)
	if !r.Restored() {
		t.Error("Restored() = false with a pre-existing snapshot at cfg.SnapshotPath")
	}
}

// The resize that "granule resize" makes, end to end against fakechv,
// which reproduces cloud-hypervisor's own answers: a resize to a new
// size goes through and is visible in vm.info, a repeat of it is
// skipped, and the raw Resize the skip protects against is a hard error
// -- the failure a script that pins a VM to a fixed size used to hit the
// second time it ran.
func TestRunner_ResizeIfNeededIsIdempotent(t *testing.T) {
	cfg := testConfig(t)
	cfg.MemoryHotplug = true
	cfg.MemoryMaxMB = 1024
	cfg.CPUsMax = 4
	r := startRunner(t, cfg, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.api.WaitReady(ctx); err != nil {
		t.Fatalf("waiting for fakechv's api socket: %v", err)
	}

	info, err := r.api.Info(ctx)
	if err != nil {
		t.Fatalf("Info() error = %v", err)
	}
	if got, want := info.DesiredRAMBytes(), uint64(cfg.MemoryMB)*1024*1024; got != want {
		t.Errorf("DesiredRAMBytes() before any resize = %d, want the boot size %d", got, want)
	}
	if got, want := info.DesiredVCPUs(), uint32(cfg.CPUs); got != want {
		t.Errorf("DesiredVCPUs() before any resize = %d, want the boot count %d", got, want)
	}

	const wantRAM = 512 * 1024 * 1024
	resized, err := r.api.ResizeIfNeeded(ctx, wantRAM)
	if err != nil {
		t.Fatalf("ResizeIfNeeded() error = %v", err)
	}
	if !resized {
		t.Error("first ResizeIfNeeded() reported no resize, want one")
	}
	if info, err := r.api.Info(ctx); err != nil {
		t.Fatalf("Info() error = %v", err)
	} else if info.DesiredRAMBytes() != wantRAM {
		t.Errorf("DesiredRAMBytes() after resize = %d, want %d", info.DesiredRAMBytes(), uint64(wantRAM))
	}

	resized, err = r.api.ResizeIfNeeded(ctx, wantRAM)
	if err != nil {
		t.Fatalf("second ResizeIfNeeded() error = %v", err)
	}
	if resized {
		t.Error("second ResizeIfNeeded() reported a resize, want it skipped")
	}

	if err := r.api.Resize(ctx, wantRAM); err == nil {
		t.Error("Resize() to the size the VM already has succeeded; fakechv should refuse it as cloud-hypervisor does")
	}

	// vCPUs the same way round, except that the repeat would have been
	// accepted silently: what changes is that the caller can now tell
	// the two apart.
	resized, err = r.api.ResizeCPUsIfNeeded(ctx, 4)
	if err != nil {
		t.Fatalf("ResizeCPUsIfNeeded() error = %v", err)
	}
	if !resized {
		t.Error("first ResizeCPUsIfNeeded() reported no resize, want one")
	}
	resized, err = r.api.ResizeCPUsIfNeeded(ctx, 4)
	if err != nil {
		t.Fatalf("second ResizeCPUsIfNeeded() error = %v", err)
	}
	if resized {
		t.Error("second ResizeCPUsIfNeeded() reported a resize, want it skipped")
	}
}

func TestRunner_DirectQcow2DiskAttachment(t *testing.T) {
	dir := t.TempDir()
	qcowPath := filepath.Join(dir, "disk.qcow2")
	if err := qcow2.WriteOverlay(qcowPath, "", 128*1024*1024); err != nil {
		t.Fatal(err)
	}

	cfg := testConfig(t)
	cfg.Disks = []config.Disk{{Path: qcowPath}}

	r := startRunner(t, cfg, nil)
	t.Cleanup(func() {
		r.Shutdown(context.Background())
	})

	if r.cfg.Disks[0].Path != qcowPath {
		t.Errorf("r.cfg.Disks[0].Path = %q, want %q", r.cfg.Disks[0].Path, qcowPath)
	}

	args, err := BuildArgs(r.cfg)
	if err != nil {
		t.Fatalf("BuildArgs: %v", err)
	}
	want := "path=" + qcowPath + ",readonly=off,image_type=qcow2,backing_files=on,sparse=on"
	if got := argValue(t, args, "--disk"); got != want {
		t.Errorf("--disk = %q, want %q", got, want)
	}
}

func TestRunner_DirectRawDiskAttachment(t *testing.T) {
	dir := t.TempDir()
	rawPath := filepath.Join(dir, "disk.img")
	if err := os.WriteFile(rawPath, []byte("raw root filesystem content"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := testConfig(t)
	cfg.Disks = []config.Disk{{Path: rawPath}}

	r := startRunner(t, cfg, nil)
	t.Cleanup(func() {
		r.Shutdown(context.Background())
	})

	if r.cfg.Disks[0].Path != rawPath {
		t.Errorf("r.cfg.Disks[0].Path = %q, want %q", r.cfg.Disks[0].Path, rawPath)
	}

	args, err := BuildArgs(r.cfg)
	if err != nil {
		t.Fatalf("BuildArgs: %v", err)
	}
	want := "path=" + rawPath + ",readonly=off,image_type=raw,sparse=on"
	if got := argValue(t, args, "--disk"); got != want {
		t.Errorf("--disk = %q, want %q", got, want)
	}
}
