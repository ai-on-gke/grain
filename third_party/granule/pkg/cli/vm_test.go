package cli

import (
	"bytes"
	"context"
	"flag"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bwsalmon/granule/pkg/config"
	"github.com/bwsalmon/granule/pkg/dockercli"
	"github.com/bwsalmon/granule/pkg/vmspec"
)

// fakedockerPath is built once per test run from
// ../dockervm/testdata/fakedocker, so tests can point $GRANULE_DOCKER at
// it and exercise granule's docker calls without a real docker daemon.
// It is deliberately not named "docker": a call that ignored
// GRANULE_DOCKER and went to PATH would not find it.
var fakedockerPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fakedocker-build")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	path := filepath.Join(dir, "fakedocker")
	cmd := exec.Command("go", "build", "-o", path, "../dockervm/testdata/fakedocker")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		panic("building fakedocker test helper: " + err.Error())
	}
	fakedockerPath = path

	os.Exit(m.Run())
}

func runVMArgs(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	return runVMStdin(t, strings.NewReader(""), args...)
}

// runVMStdin is runVMArgs for the subcommands that have a stdin: "vm
// exec" and friends proxy it to the guest command.
func runVMStdin(t *testing.T, stdin io.Reader, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var o, e bytes.Buffer
	err = runVM(context.Background(), args, stdin, &o, &e)
	return o.String(), e.String(), err
}

// withFakeDocker points $GRANULE_DOCKER at the fakedocker build for the
// duration of t, the way a user points granule at podman or nerdctl, so
// code that shells out to docker hits the fake instead.
func withFakeDocker(t *testing.T) {
	t.Helper()
	t.Setenv(dockercli.EnvBinary, fakedockerPath)
}

// vmContainerCall is the "docker run" that started a VM's own container,
// as one string to make substring checks readable. Create starts one VM
// container; after an update it is the re-created VM's launch.
//
// This is what the rendered static pod manifest used to be checked
// against: the settings a VM was created with are only really applied if
// they reach the container that boots the guest.
func vmContainerCall(t *testing.T, calls [][]string) string {
	t.Helper()
	return strings.Join(lastCall(t, calls, "run"), " ")
}

func TestVMLifecycle(t *testing.T) {
	withFakeDocker(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	calls := callLog(t)

	_, stderr, err := runVMArgs(t, "create", "web",
		"--disk", "/images/disk.img",
		"--kernel", "/images/vmlinux",
		"--state-dir", stateDir,
	)
	if err != nil {
		t.Fatalf("create error = %v, stderr = %s", err, stderr)
	}
	if call := vmContainerCall(t, calls()); !strings.Contains(call, "CHV_CPUS=2") {
		t.Errorf("VM container missing default CPUs=2:\n%s", call)
	}

	saved, err := vmspec.Load(stateDir, "web")
	if err != nil {
		t.Fatalf("saved state not found: %v", err)
	}
	if saved.DiskImage != "/images/disk.img" || saved.Kernel != "/images/vmlinux" {
		t.Errorf("saved state = %+v, want the disk and kernel it was created with", saved)
	}

	// create again should fail without touching anything
	if _, _, err := runVMArgs(t, "create", "web", "--disk", "x",
		"--state-dir", stateDir); err == nil {
		t.Fatalf("create of existing VM = nil error, want it to fail")
	}

	// list should show it
	out, _, err := runVMArgs(t, "list", "--state-dir", stateDir)
	if err != nil {
		t.Fatalf("list error = %v", err)
	}
	if !strings.Contains(out, "web") {
		t.Errorf("list output missing web VM:\n%s", out)
	}

	// update cpus only; disk and kernel should be preserved
	if _, stderr, err := runVMArgs(t, "update", "web", "--cpus", "4", "--state-dir", stateDir); err != nil {
		t.Fatalf("update error = %v, stderr = %s", err, stderr)
	}
	updated := vmContainerCall(t, calls())
	if !strings.Contains(updated, "CHV_CPUS=4") {
		t.Errorf("re-created VM container missing updated CPUs=4:\n%s", updated)
	}
	if !strings.Contains(updated, "/images/vmlinux") {
		t.Errorf("update lost preserved -kernel value:\n%s", updated)
	}

	// delete removes the containers and the saved state
	if _, stderr, err := runVMArgs(t, "delete", "web", "--state-dir", stateDir); err != nil {
		t.Fatalf("delete error = %v, stderr = %s", err, stderr)
	}
	if !strings.Contains(strings.Join(lastCall(t, calls(), "rm"), " "), "granule-vm-web") {
		t.Errorf("delete did not remove the VM's container: %v", calls())
	}
	if _, err := vmspec.Load(stateDir, "web"); err == nil {
		t.Errorf("saved state still present after delete")
	}

	// delete again is a no-op, not an error
	if _, _, err := runVMArgs(t, "delete", "web", "--state-dir", stateDir); err != nil {
		t.Errorf("second delete error = %v, want nil (idempotent)", err)
	}

	// update after delete should fail with a helpful message
	if _, _, err := runVMArgs(t, "update", "web", "--cpus", "2", "--state-dir", stateDir); err == nil {
		t.Fatalf("update of deleted VM = nil error, want it to fail")
	} else if !strings.Contains(err.Error(), "not found") {
		t.Errorf("update error = %v, want it to mention the VM wasn't found", err)
	}
}

func TestVMCreate_StorageBackend(t *testing.T) {
	withFakeDocker(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	calls := callLog(t)

	_, stderr, err := runVMArgs(t, "create", "extvm",
		"--disk", "/images/disk.img",
		"--storage-backend", "extents",
		"--extent-size-mb", "32",
		"--state-dir", stateDir,
	)
	if err != nil {
		t.Fatalf("create error = %v, stderr = %s", err, stderr)
	}
	call := vmContainerCall(t, calls())
	if !strings.Contains(call, "CHV_STORAGE_BACKEND=extents") {
		t.Errorf("VM container missing CHV_STORAGE_BACKEND=extents:\n%s", call)
	}
	if !strings.Contains(call, "CHV_EXTENT_SIZE_MB=32") {
		t.Errorf("VM container missing CHV_EXTENT_SIZE_MB=32:\n%s", call)
	}
	saved, err := vmspec.Load(stateDir, "extvm")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if saved.StorageBackend != config.StorageBackendExtents || saved.ExtentSizeMB != 32 {
		t.Errorf("saved = %+v, want StorageBackend=extents ExtentSizeMB=32", saved)
	}
}

// TestVMCreate_DeprecatedFlags covers the flags that outlived what they
// configured: the NAT mode's, and -static-pod-path from the static pod
// backend. A caller's existing "vm create" line keeps working (the values
// simply configure nothing now), except for -net nat, which asks for a
// mode that no longer exists and so has to be refused rather than
// silently reinterpreted.
func TestVMCreate_DeprecatedFlags(t *testing.T) {
	withFakeDocker(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	podDir := filepath.Join(t.TempDir(), "manifests")
	calls := callLog(t)

	if _, stderr, err := runVMArgs(t, "create", "web",
		"--disk", "/images/disk.img",
		"--ip", "169.254.100.2",
		"--port", "30080",
		"--guest-port", "8080",
		"--bridge-cidr", "169.254.100.1/24",
		"--net", "flat",
		"--static-pod-path", podDir,
		"--state-dir", stateDir,
	); err != nil {
		t.Fatalf("create with deprecated flags error = %v, stderr = %s", err, stderr)
	}

	call := vmContainerCall(t, calls())
	if strings.Contains(call, "169.254.100.2") || strings.Contains(call, "30080") {
		t.Errorf("deprecated flag values reached the VM container:\n%s", call)
	}
	// -static-pod-path is accepted and ignored: nothing is written to it.
	if _, err := os.Stat(podDir); !os.IsNotExist(err) {
		t.Errorf("granule created the static pod path (err = %v), want it untouched", err)
	}

	if _, _, err := runVMArgs(t, "create", "other", "--disk", "/images/disk.img", "--net", "nat",
		"--state-dir", stateDir); err == nil {
		t.Errorf("create with -net nat = nil error, want it refused rather than silently ignored")
	}
}

// TestVMCreate_RefusesTheStaticPodBackend: the backend that wrote a
// manifest for a standalone kubelet is gone, and a caller who asks for it
// is asking for a VM this command will not start -- so say so rather than
// quietly start a docker container somewhere they are not looking.
func TestVMCreate_RefusesTheStaticPodBackend(t *testing.T) {
	withFakeDocker(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	calls := callLog(t)

	_, _, err := runVMArgs(t, "create", "web",
		"--backend", "static-pod",
		"--disk", "/images/disk.img",
		"--state-dir", stateDir,
	)
	if err == nil {
		t.Fatal("create -backend static-pod = nil error, want it refused")
	}
	if !strings.Contains(err.Error(), "docker") || !strings.Contains(err.Error(), "deploy/k8s") {
		t.Errorf("error = %v, want it to name the backend that works and where a cluster deployment is documented", err)
	}
	if got := calls(); len(got) != 0 {
		t.Errorf("docker was called for a refused backend: %v", got)
	}
	if _, err := vmspec.Load(stateDir, "web"); err == nil {
		t.Error("a refused create saved state anyway")
	}
}

func TestVMUpdate_RecomputesAutoCmdlineOnDiskModeChange(t *testing.T) {
	withFakeDocker(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	calls := callLog(t)

	if _, stderr, err := runVMArgs(t, "create", "web",
		"--disk", "/images/disk.img",
		"--kernel", "/images/vmlinux",
		"--disk-mode", "readonly",
		"--state-dir", stateDir,
	); err != nil {
		t.Fatalf("create error = %v, stderr = %s", err, stderr)
	}

	if _, stderr, err := runVMArgs(t, "update", "web", "--disk-mode", "overlay", "--state-dir", stateDir); err != nil {
		t.Fatalf("update error = %v, stderr = %s", err, stderr)
	}

	call := vmContainerCall(t, calls())
	if !strings.Contains(call, "root=/dev/vda rw") {
		t.Errorf("cmdline was not recomputed for the new disk mode:\n%s", call)
	}
	if strings.Contains(call, "root=/dev/vda ro") {
		t.Errorf("stale read-only root still present after update:\n%s", call)
	}
}

// TestVMCreate_DefaultDiskModeIsBootable pins the default a VM gets when
// no disk flag is passed at all. It used to be read-only, which the
// reference guest never finishes booting from, so a create with defaults
// produced a VM that could not come up.
func TestVMCreate_DefaultDiskModeIsBootable(t *testing.T) {
	withFakeDocker(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	calls := callLog(t)

	if _, stderr, err := runVMArgs(t, "create", "web",
		"--disk", "/images/disk.img",
		"--state-dir", stateDir,
	); err != nil {
		t.Fatalf("create error = %v, stderr = %s", err, stderr)
	}

	call := vmContainerCall(t, calls())
	if !strings.Contains(call, "CHV_DISK_MODE=overlay") {
		t.Errorf("VM container does not ask for an overlay disk:\n%s", call)
	}
	if !strings.Contains(call, "root=/dev/vda rw") {
		t.Errorf("VM container boots a read-only root:\n%s", call)
	}
}

// TestVMDiskReadOnly_StillSelectsReadOnly covers the deprecated flag now
// that -disk-mode has a real default rather than an empty one: passing
// only -disk-readonly has to keep deciding the mode, on create and on
// update alike, or the new default would silently override every caller
// still using it.
func TestVMDiskReadOnly_StillSelectsReadOnly(t *testing.T) {
	withFakeDocker(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	calls := callLog(t)

	if _, stderr, err := runVMArgs(t, "create", "web",
		"--disk", "/images/disk.img",
		"--disk-readonly=true",
		"--state-dir", stateDir,
	); err != nil {
		t.Fatalf("create error = %v, stderr = %s", err, stderr)
	}
	if call := vmContainerCall(t, calls()); !strings.Contains(call, "CHV_DISK_MODE=readonly") {
		t.Errorf("-disk-readonly=true did not ask for a read-only disk:\n%s", call)
	}

	// And back again, from the same flag on an update: the saved spec
	// now records "readonly" as an explicit mode, so this only works if
	// the flag the caller passed still wins over the saved value.
	if _, stderr, err := runVMArgs(t, "update", "web",
		"--disk-readonly=false",
		"--state-dir", stateDir,
	); err != nil {
		t.Fatalf("update error = %v, stderr = %s", err, stderr)
	}
	if call := vmContainerCall(t, calls()); !strings.Contains(call, "CHV_DISK_MODE=overlay") {
		t.Errorf("-disk-readonly=false did not ask for an overlay disk:\n%s", call)
	}
}

func TestVMUpdate_ExplicitCmdlineSurvivesLaterUpdates(t *testing.T) {
	withFakeDocker(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	calls := callLog(t)

	if _, stderr, err := runVMArgs(t, "create", "web",
		"--disk", "/images/disk.img",
		"--kernel", "/images/vmlinux",
		"--cmdline", "console=ttyS0 root=/dev/vda ro custom=1",
		"--state-dir", stateDir,
	); err != nil {
		t.Fatalf("create error = %v, stderr = %s", err, stderr)
	}

	// An unrelated update must not clobber the explicit cmdline.
	if _, stderr, err := runVMArgs(t, "update", "web", "--cpus", "3", "--state-dir", stateDir); err != nil {
		t.Fatalf("update error = %v, stderr = %s", err, stderr)
	}

	if call := vmContainerCall(t, calls()); !strings.Contains(call, "custom=1") {
		t.Errorf("explicit cmdline was lost on unrelated update:\n%s", call)
	}
}

func TestVMLifecycle_WritableDiskNeedsNoHostState(t *testing.T) {
	withFakeDocker(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	imagesDir := t.TempDir()
	diskDir := filepath.Join(t.TempDir(), "vm-disks")
	calls := callLog(t)

	if err := os.WriteFile(filepath.Join(imagesDir, "disk.img"), []byte("base image"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, stderr, err := runVMArgs(t, "create", "web",
		"--disk", "/images/disk.img",
		"--disk-readonly=false",
		"--images-hostpath", imagesDir,
		"--disk-hostpath", diskDir,
		"--state-dir", stateDir,
	)
	if err != nil {
		t.Fatalf("create error = %v, stderr = %s", err, stderr)
	}

	// No host-side overlay any more: the VM's writable disk is an overlay
	// its own container creates against the shared image (see
	// config.PrepareOverlay), so granule writes nothing here and
	// -disk-hostpath has nothing left to configure.
	if _, err := os.Stat(diskDir); !os.IsNotExist(err) {
		t.Errorf("granule created the disk-hostpath directory (err = %v), want it untouched", err)
	}

	// The VM container names the source image and asks for an overlay,
	// rather than being pointed at a host path this side had to prepare.
	call := vmContainerCall(t, calls())
	if !strings.Contains(call, "CHV_DISK_IMAGE=/images/disk.img") {
		t.Errorf("VM container does not name the source disk image:\n%s", call)
	}
	if !strings.Contains(call, "CHV_DISK_MODE=overlay") {
		t.Errorf("VM container does not ask for an overlay:\n%s", call)
	}
	if strings.Contains(call, diskDir) {
		t.Errorf("VM container still mounts a host writable-disk directory:\n%s", call)
	}

	if _, stderr, err := runVMArgs(t, "update", "web", "--cpus", "4", "--state-dir", stateDir); err != nil {
		t.Fatalf("update error = %v, stderr = %s", err, stderr)
	}
	if _, stderr, err := runVMArgs(t, "delete", "web", "--state-dir", stateDir); err != nil {
		t.Fatalf("delete error = %v, stderr = %s", err, stderr)
	}
}

func TestRunVM_UnknownSubcommand(t *testing.T) {
	if _, _, err := runVMArgs(t, "frobnicate"); err == nil {
		t.Fatalf("runVM(frobnicate) = nil error, want one")
	}
}

// A per-VM setting only a hand-written pod spec could express is not one
// the CLI that creates VMs can be said to support, so -ready-file has to
// travel the whole way: saved with the spec, kept by an update that
// doesn't repeat it, and set on the VM container -- which is where every
// "granule ready" against this VM reads it from.
func TestVMCreate_ReadyFileReachesTheVMContainerAndIsRemembered(t *testing.T) {
	withFakeDocker(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	calls := callLog(t)

	if _, stderr, err := runVMArgs(t, "create", "web",
		"--ready-file", "/run/granule/ready",
		"--state-dir", stateDir,
	); err != nil {
		t.Fatalf("create error = %v, stderr = %s", err, stderr)
	}

	saved, err := vmspec.Load(stateDir, "web")
	if err != nil {
		t.Fatalf("saved state not found: %v", err)
	}
	if saved.ReadyFile != "/run/granule/ready" {
		t.Errorf("saved ReadyFile = %q, want /run/granule/ready", saved.ReadyFile)
	}

	var found bool
	for _, c := range calls() {
		for _, arg := range c {
			if arg == "GRANULE_READY_FILE=/run/granule/ready" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("no docker call set GRANULE_READY_FILE on the VM container: %v", calls())
	}

	// An update that says nothing about it must not drop it: every flag
	// defaults to the saved value, and a readiness condition silently
	// disappearing on an unrelated "vm update -cpus 4" would leave the
	// VM reporting ready earlier than it used to.
	if _, stderr, err := runVMArgs(t, "update", "web", "--cpus", "4", "--state-dir", stateDir); err != nil {
		t.Fatalf("update error = %v, stderr = %s", err, stderr)
	}
	saved, err = vmspec.Load(stateDir, "web")
	if err != nil {
		t.Fatalf("saved state not found after update: %v", err)
	}
	if saved.ReadyFile != "/run/granule/ready" {
		t.Errorf("saved ReadyFile after update = %q, want it kept", saved.ReadyFile)
	}
}

// A relative path would be looked for inside the guest from the
// account's home directory, so the VM would simply never report ready.
// Refused where the flag can be named.
func TestVMCreate_RejectsARelativeReadyFile(t *testing.T) {
	withFakeDocker(t)
	stateDir := filepath.Join(t.TempDir(), "state")

	_, stderr, err := runVMArgs(t, "create", "web",
		"--ready-file", "run/kontur/ready",
		"--state-dir", stateDir,
	)
	if err == nil {
		t.Fatal("create with a relative -ready-file = nil error, want it refused")
	}
	if !strings.Contains(err.Error()+stderr, "absolute") {
		t.Errorf("error = %v, want it to say the path must be absolute", err)
	}
}

// TestVMCreate_ImageDefault covers the one default a caller used to have
// to override: the image. A docker daemon already holds the image "docker
// build -t granule:latest ." made, and the registry reference the
// static-pod backend needed gave it nothing to pull.
func TestVMCreate_ImageDefault(t *testing.T) {
	withFakeDocker(t)

	stateDir := filepath.Join(t.TempDir(), "state")
	if _, stderr, err := runVMArgs(t, "create", "web",
		"--state-dir", stateDir,
	); err != nil {
		t.Fatalf("create error = %v, stderr = %s", err, stderr)
	}
	saved, err := vmspec.Load(stateDir, "web")
	if err != nil {
		t.Fatalf("saved state not found: %v", err)
	}
	if saved.KonturImage != vmspec.DockerImage {
		t.Errorf("KonturImage = %q, want the locally built %q", saved.KonturImage, vmspec.DockerImage)
	}

	// An explicit -kontur-image still wins -- which is how a VM runs an
	// image pulled from a registry, the way a cluster deployment does.
	namedState := filepath.Join(t.TempDir(), "state")
	if _, stderr, err := runVMArgs(t, "create", "web",
		"--kontur-image", "granule:custom",
		"--state-dir", namedState,
	); err != nil {
		t.Fatalf("create error = %v, stderr = %s", err, stderr)
	}
	saved, err = vmspec.Load(namedState, "web")
	if err != nil {
		t.Fatalf("saved state not found: %v", err)
	}
	if saved.KonturImage != "granule:custom" {
		t.Errorf("KonturImage = %q, want the explicitly named granule:custom", saved.KonturImage)
	}
}

// TestVMCreate_UnwritableStateDirFailsBeforeAnythingStarts is the order
// that used to be wrong: the state directory was only written after the
// containers had been started, so an unwritable one left a VM running
// with no saved state to find it by again. The default is a directory
// its caller can create now (see defaultStateDir), so what is left to
// refuse is an explicit -state-dir like this one.
func TestVMCreate_UnwritableStateDirFailsBeforeAnythingStarts(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write into any directory, so there is nothing to refuse here")
	}
	withFakeDocker(t)
	calls := callLog(t)

	readonly := filepath.Join(t.TempDir(), "readonly")
	if err := os.Mkdir(readonly, 0o555); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(readonly, "vms")

	_, _, err := runVMArgs(t, "create", "web",
		"--state-dir", stateDir,
	)
	if err == nil {
		t.Fatal("create into an unwritable state directory = nil error, want one")
	}
	if !strings.Contains(err.Error(), stateDir) || !strings.Contains(err.Error(), "-state-dir") {
		t.Errorf("error = %v, want it to name %q and the flag that fixes it", err, stateDir)
	}
	if got := changingCalls(calls()); len(got) != 0 {
		t.Errorf("something was started before the state directory was checked: %v", got)
	}
}

func TestVMLifecycle_DockerRunOpts(t *testing.T) {
	withFakeDocker(t)
	stateDir := filepath.Join(t.TempDir(), "state")

	_, stderr, err := runVMArgs(t, "create", "web",
		"--disk", "/images/disk.img",
		"--kernel", "/images/vmlinux",
		"--docker-run-opt", "--network",
		"--docker-run-opt", "mynet",
		"--docker-run-opt", "-p",
		"--docker-run-opt", "8080:80",
		"--state-dir", stateDir,
	)
	if err != nil {
		t.Fatalf("create error = %v, stderr = %s", err, stderr)
	}

	saved, err := vmspec.Load(stateDir, "web")
	if err != nil {
		t.Fatalf("saved state not found: %v", err)
	}
	want := []string{"--network", "mynet", "-p", "8080:80"}
	if !reflect.DeepEqual(saved.DockerRunOpts, want) {
		t.Errorf("saved DockerRunOpts = %v, want %v", saved.DockerRunOpts, want)
	}

	// Repeating -docker-run-opt replaces the saved list rather than
	// appending to it.
	if _, stderr, err := runVMArgs(t, "update", "web",
		"--docker-run-opt", "-p",
		"--docker-run-opt", "9090:80",
		"--state-dir", stateDir); err != nil {
		t.Fatalf("update error = %v, stderr = %s", err, stderr)
	}
	saved, err = vmspec.Load(stateDir, "web")
	if err != nil {
		t.Fatalf("saved state not found after update: %v", err)
	}
	if want := []string{"-p", "9090:80"}; !reflect.DeepEqual(saved.DockerRunOpts, want) {
		t.Errorf("saved DockerRunOpts after update = %v, want %v (replaced, not appended)", saved.DockerRunOpts, want)
	}

	// A further update that omits -docker-run-opt entirely preserves the
	// previously saved list rather than clearing it: registerVMFlags
	// defaults the flag to the saved spec's own list, so "update" behaves
	// the same way for -docker-run-opt as it does for every other flag --
	// give it and it replaces, omit it and the saved value is kept.
	if _, stderr, err := runVMArgs(t, "update", "web", "--cpus", "4", "--state-dir", stateDir); err != nil {
		t.Fatalf("update (no -docker-run-opt) error = %v, stderr = %s", err, stderr)
	}
	saved, err = vmspec.Load(stateDir, "web")
	if err != nil {
		t.Fatalf("saved state not found after second update: %v", err)
	}
	if want := []string{"-p", "9090:80"}; !reflect.DeepEqual(saved.DockerRunOpts, want) {
		t.Errorf("saved DockerRunOpts after update without -docker-run-opt = %v, want %v (preserved)", saved.DockerRunOpts, want)
	}
	if saved.CPUs != 4 {
		t.Errorf("saved CPUs after update without -docker-run-opt = %d, want 4", saved.CPUs)
	}

	if _, stderr, err := runVMArgs(t, "delete", "web", "--state-dir", stateDir); err != nil {
		t.Fatalf("delete error = %v, stderr = %s", err, stderr)
	}
}

// TestVMLifecycle_PublishDeclared covers the granting half of EXPOSE
// from the command line: the declaration is the image's and the grant is
// this flag, so the same image opens nothing without it and publishes
// what it declares with it. The grant is saved as a grant rather than as
// the ports it resolved to, so a repacked image declaring something new
// is published on the next create without the saved spec having gone
// stale.
func TestVMLifecycle_PublishDeclared(t *testing.T) {
	withFakeDocker(t)
	t.Setenv("FAKEDOCKER_EXPOSED_PORTS", "8080/tcp")
	stateDir := filepath.Join(t.TempDir(), "state")
	calls := callLog(t)

	// Without the flag, a declared port stays shut.
	if _, stderr, err := runVMArgs(t, "create", "shielded",
		"--disk", "/images/disk.img",
		"--kernel", "/images/vmlinux",
		"--state-dir", stateDir); err != nil {
		t.Fatalf("create error = %v, stderr = %s", err, stderr)
	}
	if call := vmContainerCall(t, calls()); strings.Contains(call, " -p ") {
		t.Errorf("VM container published a port nobody granted:\n%s", call)
	}

	stdout, stderr, err := runVMArgs(t, "create", "web",
		"--disk", "/images/disk.img",
		"--kernel", "/images/vmlinux",
		"--publish-declared",
		"--state-dir", stateDir,
	)
	if err != nil {
		t.Fatalf("create error = %v, stderr = %s", err, stderr)
	}
	if call := vmContainerCall(t, calls()); !strings.Contains(call, "-p 127.0.0.1::8080/tcp") {
		t.Errorf("VM container missing the declared port it was told to publish, on loopback:\n%s", call)
	}
	if !strings.Contains(stdout, "8080/tcp") {
		t.Errorf("create said %q, want it to name what it published", stdout)
	}

	saved, err := vmspec.Load(stateDir, "web")
	if err != nil {
		t.Fatalf("saved state not found: %v", err)
	}
	if !saved.PublishDeclaredPorts {
		t.Error("saved PublishDeclaredPorts = false, want the grant recorded")
	}

	// An update that doesn't mention it keeps it, like every other flag,
	// and the ports are read off the image again rather than replayed.
	t.Setenv("FAKEDOCKER_EXPOSED_PORTS", "8080/tcp 9090/tcp")
	if _, stderr, err := runVMArgs(t, "update", "web", "--cpus", "4", "--state-dir", stateDir); err != nil {
		t.Fatalf("update error = %v, stderr = %s", err, stderr)
	}
	if call := vmContainerCall(t, calls()); !strings.Contains(call, "-p 127.0.0.1::9090/tcp") {
		t.Errorf("VM container missing the port the image declares now:\n%s", call)
	}

	// Reaching the ports from the network is a second, explicit grant,
	// and it is kept by later updates the same way the first one is.
	if _, stderr, err := runVMArgs(t, "update", "web", "--publish-addr", "0.0.0.0", "--state-dir", stateDir); err != nil {
		t.Fatalf("update error = %v, stderr = %s", err, stderr)
	}
	if call := vmContainerCall(t, calls()); !strings.Contains(call, "-p 0.0.0.0::8080/tcp") {
		t.Errorf("VM container not publishing on the address asked for:\n%s", call)
	}
	if saved, err := vmspec.Load(stateDir, "web"); err != nil || saved.PublishAddr != "0.0.0.0" {
		t.Errorf("saved PublishAddr = %q (err %v), want 0.0.0.0", saved.PublishAddr, err)
	}
	if _, _, err := runVMArgs(t, "update", "web", "--publish-addr", "localhost", "--state-dir", stateDir); err == nil {
		t.Error("update with a hostname for -publish-addr = nil error, want it refused")
	}

	// And it can be taken back.
	if _, stderr, err := runVMArgs(t, "update", "web", "--publish-declared=false", "--state-dir", stateDir); err != nil {
		t.Fatalf("update error = %v, stderr = %s", err, stderr)
	}
	if call := vmContainerCall(t, calls()); strings.Contains(call, " -p ") {
		t.Errorf("VM container still publishing after the grant was withdrawn:\n%s", call)
	}
	if saved, err := vmspec.Load(stateDir, "web"); err != nil || saved.PublishDeclaredPorts {
		t.Errorf("saved PublishDeclaredPorts = %v (err %v), want false", saved.PublishDeclaredPorts, err)
	}
}

// TestVMLifecycle_DiskSize covers -disk-size-mb the way every other
// per-VM setting is covered: it reaches the VM container, it survives an
// update that doesn't mention it, a later update can raise it, and
// "list" reports it.
func TestVMLifecycle_DiskSize(t *testing.T) {
	withFakeDocker(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	calls := callLog(t)

	if _, stderr, err := runVMArgs(t, "create", "web",
		"--disk", "/images/disk.img",
		"--disk-mode", "overlay",
		"--disk-size-mb", "8192",
		"--state-dir", stateDir,
	); err != nil {
		t.Fatalf("create error = %v, stderr = %s", err, stderr)
	}
	if call := vmContainerCall(t, calls()); !strings.Contains(call, "CHV_DISK_SIZE_MB=8192") {
		t.Errorf("VM container missing CHV_DISK_SIZE_MB=8192:\n%s", call)
	}

	out, _, err := runVMArgs(t, "list", "--state-dir", stateDir)
	if err != nil {
		t.Fatalf("list error = %v", err)
	}
	if !strings.Contains(out, "DISK_MB") || !strings.Contains(out, "8192") {
		t.Errorf("list output missing the VM's disk size:\n%s", out)
	}

	// An update about something else keeps it, like every other flag.
	if _, stderr, err := runVMArgs(t, "update", "web", "--cpus", "4", "--state-dir", stateDir); err != nil {
		t.Fatalf("update error = %v, stderr = %s", err, stderr)
	}
	if call := vmContainerCall(t, calls()); !strings.Contains(call, "CHV_DISK_SIZE_MB=8192") {
		t.Errorf("update lost the preserved -disk-size-mb value:\n%s", call)
	}

	// Raising it is how a VM's disk grows: the VM container resizes its
	// overlay on the next boot (see config.PrepareOverlay).
	if _, stderr, err := runVMArgs(t, "update", "web", "--disk-size-mb", "16384", "--state-dir", stateDir); err != nil {
		t.Fatalf("update error = %v, stderr = %s", err, stderr)
	}
	if call := vmContainerCall(t, calls()); !strings.Contains(call, "CHV_DISK_SIZE_MB=16384") {
		t.Errorf("VM container missing the raised disk size:\n%s", call)
	}
}

func TestVMCreate_DiskSizeRejectedWithoutOverlayMode(t *testing.T) {
	withFakeDocker(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	calls := callLog(t)

	_, _, err := runVMArgs(t, "create", "web",
		"--disk", "/images/disk.img",
		"--disk-mode", "persistent",
		"--disk-size-mb", "8192",
		"--state-dir", stateDir,
	)
	if err == nil {
		t.Fatal("create = nil error, want -disk-size-mb rejected outside overlay mode")
	}
	if got := changingCalls(calls()); len(got) != 0 {
		t.Errorf("a VM was started despite the rejected flags: %v", got)
	}
}

// machineSizing is how a machine built with
//
//	VM SET cpus=2 cpus-max=4
//	VM SET memory-mb=1024 memory-max-mb=2048
//	VM SET disk-mode=persistent
//
// records those settings: as the runtime variables in its own OCI config
// (docs/granulefile.md §5.5), which is what granule reads them back out
// of. Compare test/granulefiles/08-vm-set-defaults, which builds one.
const machineSizing = "CHV_CPUS=2,CHV_CPUS_MAX=4,CHV_MEMORY_MB=1024,CHV_MEMORY_MAX_MB=2048,CHV_DISK_MODE=persistent"

// §5.5 calls VM SET's settings "the image's defaults", overridden by the
// runtime environment "the way a docker run -e overrides an ENV". granule's
// own flag defaults are not such an override: a caller who never mentioned
// memory is not asking for 2 GiB, and passing 2048 on as an explicit -e
// beat the machine's own 1024 -- so its recorded settings only ever took
// effect under a plain "docker run", never under the command that is meant
// to run machines.
func TestVMCreate_HonoursTheMachinesOwnSizing(t *testing.T) {
	withFakeDocker(t)
	t.Setenv("FAKEDOCKER_IMAGE_ENV", machineSizing)
	stateDir := filepath.Join(t.TempDir(), "state")
	calls := callLog(t)

	if _, stderr, err := runVMArgs(t, "create", "web", "--state-dir", stateDir); err != nil {
		t.Fatalf("create error = %v, stderr = %s", err, stderr)
	}
	call := vmContainerCall(t, calls())
	for _, want := range []string{"CHV_CPUS=2", "CHV_CPUS_MAX=4", "CHV_MEMORY_MB=1024", "CHV_MEMORY_MAX_MB=2048", "CHV_DISK_MODE=persistent"} {
		if !strings.Contains(call, want) {
			t.Errorf("VM container missing the machine's own %s:\n%s", want, call)
		}
	}
	// Resolved into the saved spec, not just into the container's
	// environment: "granule vm show" and a later "vm update" both read
	// that, and neither can ask the container what it worked out.
	saved, err := vmspec.Load(stateDir, "web")
	if err != nil {
		t.Fatalf("saved state not found: %v", err)
	}
	if saved.CPUs != 2 || saved.MemoryMB != 1024 || saved.CPUsMax != 4 || saved.MemoryMaxMB != 2048 || saved.DiskMode != "persistent" {
		t.Errorf("saved state = %+v, want the sizing the machine records for itself", saved)
	}
}

// The other half of §5.5: the settings are defaults, not limits, so a
// flag that was actually passed still wins -- and only the settings it
// names.
func TestVMCreate_FlagsOverrideTheMachinesOwnSizing(t *testing.T) {
	withFakeDocker(t)
	t.Setenv("FAKEDOCKER_IMAGE_ENV", machineSizing)
	stateDir := filepath.Join(t.TempDir(), "state")
	calls := callLog(t)

	if _, stderr, err := runVMArgs(t, "create", "web",
		"--cpus", "8", "--disk-mode", "overlay",
		"--state-dir", stateDir,
	); err != nil {
		t.Fatalf("create error = %v, stderr = %s", err, stderr)
	}
	call := vmContainerCall(t, calls())
	for _, want := range []string{"CHV_CPUS=8", "CHV_DISK_MODE=overlay"} {
		if !strings.Contains(call, want) {
			t.Errorf("VM container missing %s, which the caller asked for:\n%s", want, call)
		}
	}
	// Untouched by the flags above, so still the machine's.
	for _, want := range []string{"CHV_MEMORY_MB=1024", "CHV_MEMORY_MAX_MB=2048"} {
		if !strings.Contains(call, want) {
			t.Errorf("VM container lost the machine's own %s, which no flag overrode:\n%s", want, call)
		}
	}
	// The machine's vCPU ceiling was drawn around its own two vCPUs, and
	// the caller asked for eight: that ceiling cannot hold, and it is
	// dropped rather than failing a create over a number nobody typed.
	if strings.Contains(call, "CHV_CPUS_MAX=") {
		t.Errorf("VM container kept a ceiling the caller booted past:\n%s", call)
	}
}

// -vcpus is -cpus under another name, and passing it is the caller naming
// a vCPU count just as much.
func TestVMCreate_VCPUsAliasOverridesTheMachinesOwnSizing(t *testing.T) {
	withFakeDocker(t)
	t.Setenv("FAKEDOCKER_IMAGE_ENV", machineSizing)
	stateDir := filepath.Join(t.TempDir(), "state")
	calls := callLog(t)

	if _, stderr, err := runVMArgs(t, "create", "web", "--vcpus", "8", "--state-dir", stateDir); err != nil {
		t.Fatalf("create error = %v, stderr = %s", err, stderr)
	}
	if call := vmContainerCall(t, calls()); !strings.Contains(call, "CHV_CPUS=8") {
		t.Errorf("VM container missing the -vcpus the caller passed:\n%s", call)
	}
}

// A machine that records nothing of its own is every image granule built
// before VM SET existed, and granule:latest itself: it gets granule's own
// defaults, which is what a VM has always been given.
func TestVMCreate_MachineWithNoSettingsGetsGranulesDefaults(t *testing.T) {
	withFakeDocker(t)
	// The granule image's own ENV: an empty CHV_DISK_MODE (see the
	// Dockerfile's GUEST_DISK_MODE) is not a setting.
	t.Setenv("FAKEDOCKER_IMAGE_ENV", "PATH=/usr/bin:/bin,CHV_DISK_MODE=")
	stateDir := filepath.Join(t.TempDir(), "state")
	calls := callLog(t)

	if _, stderr, err := runVMArgs(t, "create", "web", "--state-dir", stateDir); err != nil {
		t.Fatalf("create error = %v, stderr = %s", err, stderr)
	}
	call := vmContainerCall(t, calls())
	for _, want := range []string{"CHV_CPUS=2", "CHV_MEMORY_MB=2048", "CHV_DISK_MODE=overlay", "CHV_SHUTDOWN_TIMEOUT=20s", "CHV_CMDLINE=console=" + config.GuestConsole() + " root=/dev/vda rw"} {
		if !strings.Contains(call, want) {
			t.Errorf("VM container missing granule's own default %s:\n%s", want, call)
		}
	}
	for _, unwanted := range []string{"CHV_CPUS_MAX=", "CHV_MEMORY_MAX_MB="} {
		if strings.Contains(call, unwanted) {
			t.Errorf("VM container sets %s for a machine that records no ceiling:\n%s", unwanted, call)
		}
	}
}

// Sizing is not the only thing a machine records for itself, and the rest
// of §5.5's table travels to the container the same way: as an explicit -e
// that beats the image's ENV. A machine that says how it wants to be shut
// down, or what command line it needs, is asking for that under "granule
// vm create" too -- and this is the same reader "granule guest verify"
// boots through (pkg/vmspec.ImageSettings), so the two cannot answer
// differently.
func TestVMCreate_HonoursTheMachinesOwnSettingsBeyondSizing(t *testing.T) {
	withFakeDocker(t)
	t.Setenv("FAKEDOCKER_IMAGE_ENV", "CHV_CMDLINE=quiet,CHV_SHUTDOWN_TIMEOUT=45s,CHV_DISK_NUM_QUEUES=4,CHV_DISK_IO_ENGINE=io_uring,CHV_DISK_IOTHREADS=on")
	stateDir := filepath.Join(t.TempDir(), "state")
	calls := callLog(t)

	if _, stderr, err := runVMArgs(t, "create", "web", "--state-dir", stateDir); err != nil {
		t.Fatalf("create error = %v, stderr = %s", err, stderr)
	}
	call := vmContainerCall(t, calls())
	for _, want := range []string{"CHV_CMDLINE=quiet", "CHV_SHUTDOWN_TIMEOUT=45s", "CHV_DISK_NUM_QUEUES=4", "CHV_DISK_IO_ENGINE=io_uring", "CHV_DISK_IOTHREADS=true"} {
		if !strings.Contains(call, want) {
			t.Errorf("VM container missing the machine's own %s:\n%s", want, call)
		}
	}
	saved, err := vmspec.Load(stateDir, "web")
	if err != nil {
		t.Fatalf("saved state not found: %v", err)
	}
	if saved.Cmdline != "quiet" || saved.ShutdownTimeout != "45s" {
		t.Errorf("saved state = %+v, want the command line and shutdown budget the machine records", saved)
	}
	if saved.CmdlineAuto {
		t.Error("the machine's own command line was recorded as one granule derived, which an update would then recompute away")
	}
}

// And a flag still wins over them, setting by setting.
func TestVMCreate_FlagsOverrideTheMachinesOwnSettingsBeyondSizing(t *testing.T) {
	withFakeDocker(t)
	t.Setenv("FAKEDOCKER_IMAGE_ENV", "CHV_CMDLINE=quiet,CHV_SHUTDOWN_TIMEOUT=45s")
	stateDir := filepath.Join(t.TempDir(), "state")
	calls := callLog(t)

	if _, stderr, err := runVMArgs(t, "create", "web",
		"--shutdown-timeout", "90s", "--state-dir", stateDir,
	); err != nil {
		t.Fatalf("create error = %v, stderr = %s", err, stderr)
	}
	call := vmContainerCall(t, calls())
	if !strings.Contains(call, "CHV_SHUTDOWN_TIMEOUT=90s") {
		t.Errorf("VM container missing the shutdown budget the caller asked for:\n%s", call)
	}
	if !strings.Contains(call, "CHV_CMDLINE=quiet") {
		t.Errorf("VM container lost the machine's own command line, which no flag overrode:\n%s", call)
	}
}

// The deprecated -disk-readonly names no mode, which is how it asks for
// the derivation -- but it is a caller with an opinion about the disk, and
// the machine's own disk-mode must not be read as overriding it.
func TestVMCreate_DiskReadOnlyBeatsTheMachinesOwnDiskMode(t *testing.T) {
	withFakeDocker(t)
	t.Setenv("FAKEDOCKER_IMAGE_ENV", machineSizing)
	stateDir := filepath.Join(t.TempDir(), "state")
	calls := callLog(t)

	if _, stderr, err := runVMArgs(t, "create", "web", "--disk-readonly", "--state-dir", stateDir); err != nil {
		t.Fatalf("create error = %v, stderr = %s", err, stderr)
	}
	if call := vmContainerCall(t, calls()); !strings.Contains(call, "CHV_DISK_MODE=readonly") {
		t.Errorf("VM container did not get the read-only disk the caller asked for:\n%s", call)
	}
}

// An update starts from the spec the VM is already running with, which had
// the machine's say resolved into it at create time. So it must not go
// back to the image: rebuilding the machine with different settings, or
// pointing the same name at another one, cannot be allowed to resize a VM
// behind an unrelated "vm update" -- and a spec saved with cpus: 2 in it
// cannot be told from a VM that asked for two.
func TestVMUpdate_KeepsTheSizingTheVMWasCreatedWith(t *testing.T) {
	withFakeDocker(t)
	t.Setenv("FAKEDOCKER_IMAGE_ENV", machineSizing)
	stateDir := filepath.Join(t.TempDir(), "state")
	calls := callLog(t)

	if _, stderr, err := runVMArgs(t, "create", "web", "--state-dir", stateDir); err != nil {
		t.Fatalf("create error = %v, stderr = %s", err, stderr)
	}
	// The machine, rebuilt with more of everything, under the tag this VM
	// already runs.
	t.Setenv("FAKEDOCKER_IMAGE_ENV", "CHV_CPUS=16,CHV_MEMORY_MB=8192,CHV_DISK_MODE=overlay")
	if _, stderr, err := runVMArgs(t, "update", "web", "--cpus", "4", "--state-dir", stateDir); err != nil {
		t.Fatalf("update error = %v, stderr = %s", err, stderr)
	}
	call := vmContainerCall(t, calls())
	for _, want := range []string{"CHV_CPUS=4", "CHV_MEMORY_MB=1024", "CHV_DISK_MODE=persistent"} {
		if !strings.Contains(call, want) {
			t.Errorf("update missing %s -- it kept neither the flag it was given nor the spec it started from:\n%s", want, call)
		}
	}
}

func TestVMCreate_ExtentSize(t *testing.T) {
	withFakeDocker(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	calls := callLog(t)

	if _, stderr, err := runVMArgs(t, "create", "web",
		"--disk", "/images/disk.img",
		"--disk-mode", "overlay",
		"--extent-size-mb", "128",
		"--state-dir", stateDir,
	); err != nil {
		t.Fatalf("create error = %v, stderr = %s", err, stderr)
	}
	if call := vmContainerCall(t, calls()); !strings.Contains(call, "CHV_EXTENT_SIZE_MB=128") {
		t.Errorf("VM container missing CHV_EXTENT_SIZE_MB=128:\n%s", call)
	}

	// An update about something else keeps it.
	if _, stderr, err := runVMArgs(t, "update", "web", "--cpus", "4", "--state-dir", stateDir); err != nil {
		t.Fatalf("update error = %v, stderr = %s", err, stderr)
	}
	if call := vmContainerCall(t, calls()); !strings.Contains(call, "CHV_EXTENT_SIZE_MB=128") {
		t.Errorf("update lost the preserved -extent-size-mb value:\n%s", call)
	}

	// Changing it updates the container args on next boot.
	if _, stderr, err := runVMArgs(t, "update", "web", "--extent-size-mb", "256", "--state-dir", stateDir); err != nil {
		t.Fatalf("update error = %v, stderr = %s", err, stderr)
	}
	if call := vmContainerCall(t, calls()); !strings.Contains(call, "CHV_EXTENT_SIZE_MB=256") {
		t.Errorf("VM container missing the updated extent size:\n%s", call)
	}
}

func TestVMCreate_ExtentSizeRejectedWithoutOverlayMode(t *testing.T) {
	withFakeDocker(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	calls := callLog(t)

	_, _, err := runVMArgs(t, "create", "web",
		"--disk", "/images/disk.img",
		"--disk-mode", "persistent",
		"--extent-size-mb", "128",
		"--state-dir", stateDir,
	)
	if err == nil {
		t.Fatal("create = nil error, want -extent-size-mb rejected outside overlay mode")
	}
	if got := changingCalls(calls()); len(got) != 0 {
		t.Errorf("a VM was started despite the rejected flags: %v", got)
	}
}

// TestVMSubcommands_RejectANameThatEscapesTheStateDir covers the
// subcommands that take only a name: they build no spec, so nothing used
// to check the name before it became "<state-dir>/<name>.json" and, for
// delete, an os.Remove in the manifest directory. "vm delete ../evil"
// resolved to a remove above the state directory -- no VM can be created
// under such a name, but the remove was guarded by nothing and its error
// would not have named the name either.
func TestVMSubcommands_RejectANameThatEscapesTheStateDir(t *testing.T) {
	for _, cmd := range []string{"delete", "status", "wait", "exec", "shell", "update"} {
		t.Run(cmd, func(t *testing.T) {
			outside := t.TempDir()
			stateDir := filepath.Join(outside, "state")
			if err := os.MkdirAll(stateDir, 0o755); err != nil {
				t.Fatal(err)
			}
			// Where "../evil" would land: specPath's own
			// "<state-dir>/../evil.json", one level above the state
			// directory.
			planted := filepath.Join(outside, "evil.json")
			if err := os.WriteFile(planted, []byte("{}"), 0o644); err != nil {
				t.Fatal(err)
			}

			_, _, err := runVMArgs(t, cmd, "../evil", "--state-dir", stateDir)
			if err == nil {
				t.Fatalf("vm %s ../evil = nil error, want the name refused", cmd)
			}
			if !strings.Contains(err.Error(), "../evil") {
				t.Errorf("error = %v, want it to name the name it refused", err)
			}
			if !strings.Contains(err.Error(), "DNS-1123 label") {
				t.Errorf("error = %v, want the same message a bad name gets at create time", err)
			}
			if _, statErr := os.Stat(planted); statErr != nil {
				t.Errorf("file outside the state directory no longer readable: %v", statErr)
			}
		})
	}
}

// TestVMDelete_LeavesTheManifestDirectoryAloneForABadName is the other
// half of the delete path: the manifest it removes is
// filepath.Join(podDir, "granule-vm-<name>.yaml"), and a "../" in the name
// eats the "granule-vm-" component so the remove lands on a file in that
// directory that was never granule's.
func TestVMDelete_LeavesTheManifestDirectoryAloneForABadName(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	podDir := filepath.Join(t.TempDir(), "manifests")
	if err := os.MkdirAll(podDir, 0o755); err != nil {
		t.Fatal(err)
	}
	planted := filepath.Join(podDir, "evil.yaml")
	if err := os.WriteFile(planted, []byte("kind: Pod\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, err := runVMArgs(t, "delete", "granule-vm-../evil",
		"--state-dir", stateDir, "--static-pod-path", podDir)
	if err == nil {
		t.Fatal("vm delete of a traversing name = nil error, want it refused")
	}
	if !strings.Contains(err.Error(), "DNS-1123 label") {
		t.Errorf("error = %v, want it to say what a VM name has to be", err)
	}
	if _, statErr := os.Stat(planted); statErr != nil {
		t.Errorf("manifest directory file removed by a name that names no VM: %v", statErr)
	}
}

// TestVMSubcommands_HelpStillWorks: the name check sits in front of every
// name-taking subcommand, and "-h" is not a name -- it has to keep
// reaching the flag package's own usage dump.
func TestVMSubcommands_HelpStillWorks(t *testing.T) {
	for _, cmd := range []string{"create", "update", "delete", "status", "wait", "exec"} {
		t.Run(cmd, func(t *testing.T) {
			_, stderr, err := runVMArgs(t, cmd, "-h")
			if err != nil && err != flag.ErrHelp {
				t.Fatalf("vm %s -h error = %v, want the usage dump", cmd, err)
			}
			if !strings.Contains(stderr, "-state-dir") {
				t.Errorf("vm %s -h stderr = %s, want it to list the flags", cmd, stderr)
			}
		})
	}
}

// The whole automatic-growth path as a caller reaches it: the flags reach
// the VM container as the CHV_MEM_AGENT* variables "granule run" reads,
// they survive an update about something else, and turning the agent off
// takes the variables with it.
func TestVMLifecycle_MemAgent(t *testing.T) {
	withFakeDocker(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	calls := callLog(t)

	if _, stderr, err := runVMArgs(t, "create", "web",
		"--memory-mb", "512",
		"--memory-max-mb", "1024",
		"--mem-agent",
		"--mem-agent-token", "secret123",
		"--mem-agent-step-mb", "128",
		"--mem-agent-cooldown", "45s",
		"--state-dir", stateDir,
	); err != nil {
		t.Fatalf("create error = %v, stderr = %s", err, stderr)
	}
	call := vmContainerCall(t, calls())
	for _, want := range []string{"CHV_MEM_AGENT=true", "CHV_MEM_AGENT_TOKEN=secret123", "CHV_MEM_AGENT_STEP_MB=128", "CHV_MEM_AGENT_COOLDOWN=45s"} {
		if !strings.Contains(call, want) {
			t.Errorf("VM container missing %s:\n%s", want, call)
		}
	}

	saved, err := vmspec.Load(stateDir, "web")
	if err != nil {
		t.Fatalf("saved state not found: %v", err)
	}
	if !saved.MemAgent || saved.MemAgentToken != "secret123" || saved.MemAgentStepMB != 128 || saved.MemAgentCooldown != "45s" {
		t.Errorf("saved mem agent settings = %v/%q/%d/%q, want them remembered", saved.MemAgent, saved.MemAgentToken, saved.MemAgentStepMB, saved.MemAgentCooldown)
	}

	if _, stderr, err := runVMArgs(t, "update", "web", "--cpus", "4", "--state-dir", stateDir); err != nil {
		t.Fatalf("update error = %v, stderr = %s", err, stderr)
	}
	if call := vmContainerCall(t, calls()); !strings.Contains(call, "CHV_MEM_AGENT=true") {
		t.Errorf("an unrelated update turned the mem agent off:\n%s", call)
	}

	if _, stderr, err := runVMArgs(t, "update", "web", "--mem-agent=false", "--state-dir", stateDir); err != nil {
		t.Fatalf("update error = %v, stderr = %s", err, stderr)
	}
	if call := vmContainerCall(t, calls()); strings.Contains(call, "CHV_MEM_AGENT") {
		t.Errorf("-mem-agent=false left the agent on:\n%s", call)
	}
}

// A VM whose ceiling is its boot size has a hotplug window of nothing, so
// its agent could only ever log refusals. Refused where the flags can be
// named -- and granule's own -memory-mb default is exactly the VM
// container's default ceiling, so this is what "create -mem-agent" on its
// own would otherwise produce.
func TestVMCreate_MemAgentNeedsAMemoryCeiling(t *testing.T) {
	withFakeDocker(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	calls := callLog(t)

	_, stderr, err := runVMArgs(t, "create", "web", "--mem-agent", "--state-dir", stateDir)
	if err == nil {
		t.Fatal("create with -mem-agent and no ceiling = nil error, want it refused")
	}
	if !strings.Contains(err.Error()+stderr, "memory-max-mb") {
		t.Errorf("error = %v, want it to name -memory-max-mb", err)
	}
	if got := changingCalls(calls()); len(got) != 0 {
		t.Errorf("a VM was started despite the rejected flags: %v", got)
	}
}

func TestVMLifecycle_MemoryLimitAndOOMScoreAdj(t *testing.T) {
	withFakeDocker(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	calls := callLog(t)

	if _, stderr, err := runVMArgs(t, "create", "web",
		"--memory-mb", "8192",
		"--memory-limit-mb", "8704",
		"--oom-score-adj", "500",
		"--state-dir", stateDir,
	); err != nil {
		t.Fatalf("create error = %v, stderr = %s", err, stderr)
	}
	call := vmContainerCall(t, calls())
	for _, want := range []string{
		"--oom-score-adj 500",
		"--memory 8704m",
		"--memory-swap 8704m",
	} {
		if !strings.Contains(call, want) {
			t.Errorf("VM container missing %s:\n%s", want, call)
		}
	}

	saved, err := vmspec.Load(stateDir, "web")
	if err != nil {
		t.Fatalf("saved state not found: %v", err)
	}
	if saved.MemoryLimitMB != 8704 || saved.OOMScoreAdj != 500 {
		t.Errorf("saved MemoryLimitMB/OOMScoreAdj = %d/%d, want 8704/500", saved.MemoryLimitMB, saved.OOMScoreAdj)
	}
}
