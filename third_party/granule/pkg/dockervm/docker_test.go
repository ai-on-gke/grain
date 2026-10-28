package dockervm

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwsalmon/granule/pkg/dockercli"
	"github.com/bwsalmon/granule/pkg/vmspec"
)

// fakedockerPath is built once per test run from testdata/fakedocker,
// which stands in for the real docker CLI: it records every invocation to
// a log file and can be told to fail specific calls, so dockervm's command
// construction and error handling can be tested without a real docker
// daemon.
var fakedockerPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fakedocker-build")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	fakedockerPath = filepath.Join(dir, "fakedocker")
	cmd := exec.Command("go", "build", "-o", fakedockerPath, "./testdata/fakedocker")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		panic("building fakedocker test helper: " + err.Error())
	}

	os.Exit(m.Run())
}

// testDocker returns a Docker that reaches fakedocker through
// $GRANULE_DOCKER, as granule's own commands do, plus a function that
// reads back the calls it recorded so far as one []string per invocation
// (its args, in order).
func testDocker(t *testing.T) (*Docker, func() [][]string) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "calls.log")
	t.Setenv("FAKEDOCKER_LOG", logPath)
	t.Setenv(dockercli.EnvBinary, fakedockerPath)

	return &Docker{}, func() [][]string {
		data, err := os.ReadFile(logPath)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			t.Fatalf("reading fakedocker log: %v", err)
		}
		var calls [][]string
		for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
			if line == "" {
				continue
			}
			calls = append(calls, strings.Split(line, "\x1f"))
		}
		return calls
	}
}

func testSpec() vmspec.VMSpec {
	s := vmspec.Defaults()
	s.Name = "web"
	s.DiskImage = "/images/disk.img"
	s.Kernel = "/images/vmlinux"
	return s
}

// containsArg reports whether call (a docker invocation's argv) contains
// arg anywhere.
func containsArg(call []string, arg string) bool {
	for _, a := range call {
		if a == arg {
			return true
		}
	}
	return false
}

func TestCreate_RunsSingleContainerWithInlineNetworking(t *testing.T) {
	d, calls := testDocker(t)
	var stdout strings.Builder

	if err := Create(context.Background(), d, testSpec(), &stdout); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	got := calls()
	if len(got) != 3 {
		t.Fatalf("Create() issued %d docker calls, want 3 (legacy cleanup then one run):\n%v", len(got), got)
	}

	staleVMRm, staleNetnsRm, vmCall := got[0], got[1], got[2]

	if staleVMRm[0] != "rm" || !containsArg(staleVMRm, "granule-vm-web") {
		t.Errorf("first call = %v, want `docker rm -f granule-vm-web` clearing any stale container", staleVMRm)
	}
	if staleNetnsRm[0] != "rm" || !containsArg(staleNetnsRm, "granule-vm-web-netns") {
		t.Errorf("second call = %v, want `docker rm -f granule-vm-web-netns` clearing any stale container", staleNetnsRm)
	}

	if vmCall[0] != "run" || !containsArg(vmCall, "granule-vm-web") {
		t.Fatalf("third call = %v, want a single VM container", vmCall)
	}
	for _, forbidden := range []string{"sleep", "netshim", "container:granule-vm-web-netns"} {
		if containsArg(vmCall, forbidden) {
			t.Errorf("VM call = %v, must not use helper %q", vmCall, forbidden)
		}
	}
	for _, want := range []string{"CHV_NET_MODE=inline", "NETSHIM_CONTROL_CIDR=169.254.100.1/24"} {
		if !containsArg(vmCall, want) {
			t.Errorf("VM call = %v, missing %q", vmCall, want)
		}
	}

	// The VM container derives its own --net from the namespace, so it
	// gets netshim's settings rather than a precomputed CHV_NET.
	if !containsArg(vmCall, "NETSHIM_VM=web") {
		t.Errorf("VM call = %v, want the netshim settings it derives its --net from", vmCall)
	}
	for _, a := range vmCall {
		if strings.HasPrefix(a, "CHV_NET=") {
			t.Errorf("VM call = %v, should not set CHV_NET", vmCall)
		}
	}
	if !containsArg(vmCall, "CHV_KERNEL=/images/vmlinux") {
		t.Errorf("VM call = %v, missing expected CHV_KERNEL", vmCall)
	}
	// Nothing tells the container how to reach its guest any more: exec
	// goes over the VM's own vsock device, whose host end both halves of
	// the granule binary default to the same path for.
	for _, a := range vmCall {
		if strings.HasPrefix(a, "GRANULE_EXEC_ADDR=") {
			t.Errorf("VM call = %v, still sets GRANULE_EXEC_ADDR: exec no longer dials the guest", vmCall)
		}
	}
	// Not privileged: the two devices and the one capability netshim
	// needs, with everything else dropped.
	if containsArg(vmCall, "--privileged") {
		t.Errorf("VM call = %v, should not run the VM container --privileged", vmCall)
	}
	for _, pair := range [][2]string{
		{"--device", "/dev/kvm"},
		{"--device", "/dev/net/tun"},
		{"--cap-drop", "ALL"},
		{"--cap-add", "NET_ADMIN"},
		{"--security-opt", "no-new-privileges"},
	} {
		if !containsArgPair(vmCall, pair[0], pair[1]) {
			t.Errorf("VM call = %v, want %s %s", vmCall, pair[0], pair[1])
		}
	}
}

func TestCreate_NetModeNone(t *testing.T) {
	d, calls := testDocker(t)
	spec := testSpec()
	spec.NetMode = "none"

	if err := Create(context.Background(), d, spec, &strings.Builder{}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	vmCall := calls()[2]
	if !containsArg(vmCall, "CHV_NET_MODE=none") {
		t.Errorf("VM call = %v, missing expected CHV_NET_MODE=none", vmCall)
	}
	for _, unwanted := range []string{"CHV_NET_MODE=inline", "NETSHIM_VM", "NETSHIM_BRIDGE", "NETSHIM_CONTROL_CIDR", "NETSHIM_EXTERNAL_IFACE", "NETSHIM_DNS"} {
		for _, a := range vmCall {
			if strings.HasPrefix(a, unwanted) {
				t.Errorf("VM call = %v with NetMode=none, unexpectedly contains %s", vmCall, a)
			}
		}
	}
}

func TestCreate_OmitsUnsetOptionalEnv(t *testing.T) {
	d, calls := testDocker(t)
	spec := testSpec()
	spec.Kernel = ""
	spec.Firmware = "/images/CLOUDHV.fd"

	if err := Create(context.Background(), d, spec, &strings.Builder{}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	vmCall := calls()[2]
	for _, unwanted := range []string{"CHV_KERNEL", "CHV_INITRAMFS", "CHV_CMDLINE"} {
		for _, a := range vmCall {
			if strings.HasPrefix(a, unwanted+"=") {
				t.Errorf("VM call = %v, unexpectedly contains %s", vmCall, unwanted)
			}
		}
	}
	if !containsArg(vmCall, "CHV_FIRMWARE=/images/CLOUDHV.fd") {
		t.Errorf("VM call = %v, missing expected CHV_FIRMWARE", vmCall)
	}
}

func TestCreate_WritableDiskMountsNothingExtra(t *testing.T) {
	// A writable disk used to mean a second, read-write bind mount of the
	// host directory holding this VM's overlay. That overlay is made inside
	// the VM's own container now, so the only mount left is the shared
	// read-only images directory, and CHV_DISK_IMAGE names the source
	// image rather than an overlay path.
	d, calls := testDocker(t)
	spec := testSpec()
	spec.DiskReadOnly = false

	if err := Create(context.Background(), d, spec, &strings.Builder{}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	vmCall := calls()[2]
	for _, a := range vmCall {
		if strings.HasSuffix(a, ":/disk") {
			t.Errorf("VM call = %v, still bind-mounts a host directory for the overlay", vmCall)
		}
	}
	if !containsArg(vmCall, "CHV_DISK_IMAGE=/images/disk.img") {
		t.Errorf("VM call = %v, want CHV_DISK_IMAGE naming the source image", vmCall)
	}
	if !containsArg(vmCall, "CHV_DISK_MODE=overlay") {
		t.Errorf("VM call = %v, want -disk-readonly=false to ask for an overlay", vmCall)
	}
	if !containsArg(vmCall, "/var/lib/vm-images:/images:ro") {
		t.Errorf("VM call = %v, want the images mount still read-only", vmCall)
	}
}

func TestCreate_ReadOnlyDiskHasNoDiskMount(t *testing.T) {
	d, calls := testDocker(t)

	if err := Create(context.Background(), d, testSpec(), &strings.Builder{}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	vmCall := calls()[2]
	for _, a := range vmCall {
		if strings.HasSuffix(a, ":/disk") {
			t.Errorf("VM call = %v, unexpectedly contains a /disk mount for a read-only disk", vmCall)
		}
	}
	if !containsArg(vmCall, "CHV_DISK_IMAGE=/images/disk.img") {
		t.Errorf("VM call = %v, want CHV_DISK_IMAGE unchanged for a read-only disk", vmCall)
	}
}

func TestCreate_NoDiskBootsTheImagesOwnGuest(t *testing.T) {
	// A customized granule image carries its guest inside it, so there is
	// nothing on the host to mount and no CHV_DISK_IMAGE to set -- "granule
	// run"'s own default (pkg/config's defaultDiskImage) is the
	// whole point. Mounting -images-hostpath anyway would shadow nothing
	// but would make a host directory a requirement for a VM that needs
	// none.
	d, calls := testDocker(t)
	spec := testSpec()
	spec.DiskImage = ""
	spec.DiskReadOnly = false

	if err := Create(context.Background(), d, spec, &strings.Builder{}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	vmCall := calls()[2]
	for _, a := range vmCall {
		if strings.HasPrefix(a, "CHV_DISK_IMAGE=") {
			t.Errorf("VM call = %v, want no CHV_DISK_IMAGE so the image's own default applies", vmCall)
		}
		if strings.HasSuffix(a, ":/images:ro") || strings.HasSuffix(a, ":/disk") {
			t.Errorf("VM call = %v, want no disk mounts at all", vmCall)
		}
	}
	if !containsArg(vmCall, "CHV_DISK_MODE=overlay") {
		t.Errorf("VM call = %v, want the disk mode still passed through", vmCall)
	}
}

func TestCreate_RunFailureRemovesVMContainer(t *testing.T) {
	d, calls := testDocker(t)
	t.Setenv("FAKEDOCKER_FAIL_CONTAINS", "run -d")

	err := Create(context.Background(), d, testSpec(), &strings.Builder{})
	if err == nil {
		t.Fatalf("Create() error = nil, want an error from the failing docker run")
	}

	got := calls()
	if len(got) != 4 {
		t.Fatalf("Create() issued %d docker calls, want 4 (2 stale-cleanup rm, failing run, cleanup rm):\n%v", len(got), got)
	}
	cleanup := got[3]
	if cleanup[0] != "rm" || !containsArg(cleanup, "granule-vm-web") {
		t.Errorf("cleanup call = %v, want `docker rm -f` of the VM container", cleanup)
	}
}

func TestCreate_RemovesStaleContainersBeforeCreating(t *testing.T) {
	d, calls := testDocker(t)

	if err := Create(context.Background(), d, testSpec(), &strings.Builder{}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	got := calls()
	if len(got) < 2 {
		t.Fatalf("Create() issued %d docker calls, want at least 2 leading stale-cleanup calls:\n%v", len(got), got)
	}
	if got[0][0] != "rm" || !containsArg(got[0], "granule-vm-web") {
		t.Errorf("first call = %v, want `docker rm -f granule-vm-web` (clearing a stale VM container left by an interrupted prior Create)", got[0])
	}
	if got[1][0] != "rm" || !containsArg(got[1], "granule-vm-web-netns") {
		t.Errorf("second call = %v, want `docker rm -f granule-vm-web-netns` (clearing a stale netns holder left by an interrupted prior Create)", got[1])
	}
}

func TestDelete_StopsVMAndCleansLegacyHolder(t *testing.T) {
	d, calls := testDocker(t)

	if err := Delete(context.Background(), d, "web", 40, &strings.Builder{}); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	got := calls()
	if len(got) != 3 {
		t.Fatalf("Delete() issued %d docker calls, want 3 (stop, rm VM, rm netns):\n%v", len(got), got)
	}
	if got[0][0] != "stop" || !containsArg(got[0], "-t") || !containsArg(got[0], "40") || !containsArg(got[0], "granule-vm-web") {
		t.Errorf("first call = %v, want `docker stop -t 40 granule-vm-web`", got[0])
	}
	if got[1][0] != "rm" || !containsArg(got[1], "granule-vm-web") {
		t.Errorf("second call = %v, want `docker rm -f granule-vm-web`", got[1])
	}
	if got[2][0] != "rm" || !containsArg(got[2], "granule-vm-web-netns") {
		t.Errorf("third call = %v, want `docker rm -f granule-vm-web-netns`", got[2])
	}
}

func TestDelete_IdempotentWhenContainersAlreadyGone(t *testing.T) {
	d, _ := testDocker(t)
	t.Setenv("FAKEDOCKER_MISSING", "granule-vm-web,granule-vm-web-netns")

	if err := Delete(context.Background(), d, "web", 40, &strings.Builder{}); err != nil {
		t.Errorf("Delete() of already-gone containers error = %v, want nil (idempotent)", err)
	}
}

// runOptsSpec is testSpec plus a couple of caller-supplied docker options
// that the VM container now carries itself.
func runOptsSpec() vmspec.VMSpec {
	s := testSpec()
	s.DockerRunOpts = []string{"--network", "mynet", "-p", "8080:80"}
	return s
}

// TestCreate_DockerRunOpts covers where a caller's own docker options
// land: on the container that creates the namespace, since nothing
// joining an existing one can add them afterwards.
func TestCreate_DockerRunOpts(t *testing.T) {
	d, calls := testDocker(t)
	var stdout strings.Builder

	if err := Create(context.Background(), d, runOptsSpec(), &stdout); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	got := calls()
	if len(got) != 3 {
		t.Fatalf("Create() issued %d docker calls, want 3:\n%v", len(got), got)
	}
	vmCall := got[2]

	// Port publishing and network membership belong to the container
	// that owns the namespace; nothing joining it later can add them.
	for _, want := range []string{"--network", "mynet", "-p", "8080:80"} {
		if !containsArg(vmCall, want) {
			t.Errorf("VM call = %v, missing pass-through option %q", vmCall, want)
		}
	}
	if containsArg(vmCall, "container:granule-vm-web-netns") {
		t.Errorf("VM should own its namespace, got %v", vmCall)
	}
}

// TestCreate_NoControlLink confirms that disabling the control link drops
// the exec address entirely rather than emitting an empty or bogus one
// that would fail at dial time.
func TestCreate_NoControlLink(t *testing.T) {
	d, calls := testDocker(t)
	var stdout strings.Builder

	spec := testSpec()
	spec.ControlCIDR = ""
	if err := Create(context.Background(), d, spec, &stdout); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	vmCall := calls()[2]
	for _, a := range vmCall {
		if strings.HasPrefix(a, "GRANULE_EXEC_ADDR=") {
			t.Errorf("VM call = %v, want no GRANULE_EXEC_ADDR", vmCall)
		}
	}
}

func TestCreate_StorageBackend(t *testing.T) {
	d, calls := testDocker(t)
	spec := testSpec()
	spec.StorageBackend = "extents"
	spec.ExtentSizeMB = 64
	if err := spec.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if err := Create(context.Background(), d, spec, &strings.Builder{}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	got := calls()
	vmCall := got[len(got)-1]
	if !containsArg(vmCall, "CHV_STORAGE_BACKEND=extents") {
		t.Errorf("VM call = %v, missing CHV_STORAGE_BACKEND=extents", vmCall)
	}
	if !containsArg(vmCall, "CHV_EXTENT_SIZE_MB=64") {
		t.Errorf("VM call = %v, missing CHV_EXTENT_SIZE_MB=64", vmCall)
	}
}

// GuestUser has to reach the VM container, because both halves of it are
// read there: "granule run" puts it on the guest's kernel command line so
// the generated key is authorized for that account, and "granule exec" --
// which docker exec runs with the container's own environment -- logs in
// as it. See vmspec.VMSpec.GuestUser.
func TestCreate_GuestUser(t *testing.T) {
	d, calls := testDocker(t)
	spec := testSpec()
	spec.GuestUser = "debian"
	if err := spec.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	if err := Create(context.Background(), d, spec, &strings.Builder{}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	got := calls()
	vmCall := got[len(got)-1]
	if !containsArg(vmCall, "GRANULE_EXEC_USER=debian") {
		t.Errorf("VM call = %v, missing GRANULE_EXEC_USER", vmCall)
	}
}

// Unset means root, so the variable is left out rather than sent empty --
// guestexec would read "" as an account of that name rather than as
// "unset".
func TestCreate_NoGuestUser(t *testing.T) {
	d, calls := testDocker(t)
	if err := Create(context.Background(), d, testSpec(), &strings.Builder{}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	got := calls()
	for _, arg := range got[len(got)-1] {
		if strings.HasPrefix(arg, "GRANULE_EXEC_USER=") {
			t.Errorf("VM call sets %q with no guest user configured", arg)
		}
	}
}

// ReadyFile has to reach the VM container too, and for the same reason:
// every "granule ready" run against this VM -- the probe behind "vm wait"
// and "vm status", and one typed by hand -- reads it from the
// container's own environment, so the VM's readiness means one thing
// rather than one per caller. See vmspec.VMSpec.ReadyFile.
func TestCreate_ReadyFile(t *testing.T) {
	d, calls := testDocker(t)
	spec := testSpec()
	spec.ReadyFile = "/run/granule/ready"
	if err := spec.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	if err := Create(context.Background(), d, spec, &strings.Builder{}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	got := calls()
	vmCall := got[len(got)-1]
	if !containsArg(vmCall, "GRANULE_READY_FILE=/run/granule/ready") {
		t.Errorf("VM call = %v, missing GRANULE_READY_FILE", vmCall)
	}
}

// The opt-in half: a VM created without one must not carry the variable
// at all, or every guest image that declares nothing would become a VM
// that is never ready.
func TestCreate_NoReadyFile(t *testing.T) {
	d, calls := testDocker(t)
	if err := Create(context.Background(), d, testSpec(), &strings.Builder{}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	got := calls()
	for _, arg := range got[len(got)-1] {
		if strings.HasPrefix(arg, "GRANULE_READY_FILE=") {
			t.Errorf("VM call sets %q with no ready file configured", arg)
		}
	}
}

func TestCreate_DiskSizeMB(t *testing.T) {
	d, calls := testDocker(t)
	spec := testSpec()
	spec.DiskReadOnly = false
	spec.DiskSizeMB = 8192

	if err := Create(context.Background(), d, spec, &strings.Builder{}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	vmCall := calls()[2]
	if !containsArg(vmCall, "CHV_DISK_SIZE_MB=8192") {
		t.Errorf("VM call = %v, missing expected CHV_DISK_SIZE_MB", vmCall)
	}
}

func TestCreate_OmitsDiskSizeWhenUnset(t *testing.T) {
	d, calls := testDocker(t)

	if err := Create(context.Background(), d, testSpec(), &strings.Builder{}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	vmCall := calls()[2]
	for _, a := range vmCall {
		if strings.HasPrefix(a, "CHV_DISK_SIZE_MB=") {
			t.Errorf("VM call = %v, unexpectedly sets CHV_DISK_SIZE_MB for a VM that asked for no size", vmCall)
		}
	}
}

func TestCreate_ExtentSizeMB(t *testing.T) {
	d, calls := testDocker(t)
	spec := testSpec()
	spec.DiskReadOnly = false
	spec.ExtentSizeMB = 128

	if err := Create(context.Background(), d, spec, &strings.Builder{}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	vmCall := calls()[2]
	if !containsArg(vmCall, "CHV_EXTENT_SIZE_MB=128") {
		t.Errorf("VM call = %v, missing expected CHV_EXTENT_SIZE_MB", vmCall)
	}
}

func TestCreate_OmitsExtentSizeWhenUnset(t *testing.T) {
	d, calls := testDocker(t)

	if err := Create(context.Background(), d, testSpec(), &strings.Builder{}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	vmCall := calls()[2]
	for _, a := range vmCall {
		if strings.HasPrefix(a, "CHV_EXTENT_SIZE_MB=") {
			t.Errorf("VM call = %v, unexpectedly sets CHV_EXTENT_SIZE_MB for a VM that asked for no extent size", vmCall)
		}
	}
}

// A spec that names no sizing and no disk mode is a caller asking for the
// machine image's own, recorded in its ENV by a Granule's "VM SET" (see
// docs/granulefile.md §5.5). Passing granule's numbers as an explicit -e
// would override exactly that, since -e beats ENV the way it does for any
// image, so the variables have to be left off the call entirely.
func TestCreate_OmitsSizingWhenUnset(t *testing.T) {
	d, calls := testDocker(t)
	spec := testSpec()
	spec.CPUs, spec.MemoryMB = 0, 0
	spec.DiskMode, spec.DiskReadOnly = "", false

	if err := Create(context.Background(), d, spec, &strings.Builder{}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	vmCall := calls()[2]
	for _, a := range vmCall {
		for _, unwanted := range []string{"CHV_CPUS=", "CHV_MEMORY_MB=", "CHV_DISK_MODE="} {
			if strings.HasPrefix(a, unwanted) {
				t.Errorf("VM call = %v, unexpectedly sets %s for a VM that asked for the image's own", vmCall, unwanted)
			}
		}
	}
}

// The deprecated -disk-readonly names no mode, so it reaches Create as an
// empty DiskMode -- but it is still a caller with an opinion about the
// disk, and the mode it derives has to reach the container or the guest
// comes up writable.
func TestCreate_DiskReadOnlyStillSetsTheMode(t *testing.T) {
	d, calls := testDocker(t)
	spec := testSpec()
	spec.DiskMode, spec.DiskReadOnly = "", true

	if err := Create(context.Background(), d, spec, &strings.Builder{}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	vmCall := calls()[2]
	if !containsArg(vmCall, "CHV_DISK_MODE=readonly") {
		t.Errorf("VM call = %v, missing the disk mode -disk-readonly derives", vmCall)
	}
}

func TestCreate_HotplugCeilings(t *testing.T) {
	d, calls := testDocker(t)
	spec := testSpec()
	spec.CPUsMax = 8
	spec.MemoryMaxMB = 4096

	if err := Create(context.Background(), d, spec, &strings.Builder{}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	vmCall := calls()[2]
	for _, want := range []string{"CHV_CPUS_MAX=8", "CHV_MEMORY_MAX_MB=4096"} {
		if !containsArg(vmCall, want) {
			t.Errorf("VM call = %v, missing expected %s", vmCall, want)
		}
	}
}

func TestCreate_OmitsHotplugCeilingsWhenUnset(t *testing.T) {
	d, calls := testDocker(t)

	if err := Create(context.Background(), d, testSpec(), &strings.Builder{}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	vmCall := calls()[2]
	for _, a := range vmCall {
		if strings.HasPrefix(a, "CHV_CPUS_MAX=") || strings.HasPrefix(a, "CHV_MEMORY_MAX_MB=") {
			t.Errorf("VM call = %v, unexpectedly sets a hotplug ceiling for a VM that asked for none", vmCall)
		}
	}
}

// The guest-driven half of memory hotplug: turned on per VM, with the
// tuning only carried when this VM asked for something other than the VM
// container's own defaults.
func TestCreate_MemAgent(t *testing.T) {
	d, calls := testDocker(t)
	spec := testSpec()
	spec.MemoryMB = 512
	spec.MemoryMaxMB = 1024
	spec.MemAgent = true
	spec.MemAgentAddr = "169.254.100.1:30091"
	spec.MemAgentToken = "tok-secret-456"
	spec.MemAgentStepMB = 128
	spec.MemAgentCooldown = "45s"

	if err := Create(context.Background(), d, spec, &strings.Builder{}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	vmCall := calls()[2]
	for _, want := range []string{
		"CHV_MEM_AGENT=true",
		"CHV_MEM_AGENT_ADDR=169.254.100.1:30091",
		"CHV_MEM_AGENT_TOKEN=tok-secret-456",
		"CHV_MEM_AGENT_STEP_MB=128",
		"CHV_MEM_AGENT_COOLDOWN=45s",
	} {
		if !containsArg(vmCall, want) {
			t.Errorf("VM call = %v, missing expected %s", vmCall, want)
		}
	}

	// Left alone, the tuning is omitted rather than passed as the VM
	// container's defaults spelled out here, so those defaults stay one
	// package's business (see config.FromEnv).
	d, calls = testDocker(t)
	spec.MemAgentAddr = ""
	spec.MemAgentToken = ""
	spec.MemAgentStepMB = 0
	spec.MemAgentCooldown = ""
	if err := Create(context.Background(), d, spec, &strings.Builder{}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	vmCall = calls()[2]
	if !containsArg(vmCall, "CHV_MEM_AGENT=true") {
		t.Errorf("VM call = %v, missing CHV_MEM_AGENT", vmCall)
	}
	for _, a := range vmCall {
		if strings.HasPrefix(a, "CHV_MEM_AGENT_") {
			t.Errorf("VM call = %v, unexpectedly sets %q for a VM that asked for no tuning", vmCall, a)
		}
	}
}

// The opt-in half: growth a guest drives on its own is off unless asked
// for, so a VM created without it must carry none of these at all.
func TestCreate_OmitsMemAgentWhenUnset(t *testing.T) {
	d, calls := testDocker(t)

	if err := Create(context.Background(), d, testSpec(), &strings.Builder{}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	vmCall := calls()[2]
	for _, a := range vmCall {
		if strings.HasPrefix(a, "CHV_MEM_AGENT") {
			t.Errorf("VM call = %v, unexpectedly sets %q for a VM that asked for no mem agent", vmCall, a)
		}
	}
}

// NETSHIM_DNS is passed on every create, empty value included: the VM
// container assembles the guest's ip= parameter, so an unset variable
// there would silently mean netshim's own default rather than what this
// VM was created with -- and an empty one is how a caller asks for the
// guest image's own resolv.conf to be left alone.
func TestCreate_AlwaysPassesTheNameserversTheVMWasCreatedWith(t *testing.T) {
	d, calls := testDocker(t)
	var stdout strings.Builder

	spec := testSpec()
	spec.DNS = "10.0.0.53"
	if err := Create(context.Background(), d, spec, &stdout); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if vmCall := calls()[2]; !containsArg(vmCall, "NETSHIM_DNS=10.0.0.53") {
		t.Errorf("VM call = %v, missing NETSHIM_DNS", vmCall)
	}

	d, calls = testDocker(t)
	stdout.Reset()
	spec.DNS = ""
	if err := Create(context.Background(), d, spec, &stdout); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if vmCall := calls()[2]; !containsArg(vmCall, "NETSHIM_DNS=") {
		t.Errorf("VM call = %v, want NETSHIM_DNS passed even when empty", vmCall)
	}
}

func TestCreate_DiskTuningParameters(t *testing.T) {
	d, calls := testDocker(t)
	spec := testSpec()
	spec.DiskNumQueues = 4
	spec.DiskIOThreads = true
	spec.DiskIOEngine = "io_uring"

	if err := Create(context.Background(), d, spec, &strings.Builder{}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	vmCall := calls()[2]
	for _, want := range []string{
		"CHV_DISK_NUM_QUEUES=4",
		"CHV_DISK_IOTHREADS=true",
		"CHV_DISK_IO_ENGINE=io_uring",
	} {
		if !containsArg(vmCall, want) {
			t.Errorf("VM call = %v, missing expected %s", vmCall, want)
		}
	}

	// When unset, they are omitted
	d, calls = testDocker(t)
	spec = testSpec()
	if err := Create(context.Background(), d, spec, &strings.Builder{}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	vmCall = calls()[2]
	for _, a := range vmCall {
		if strings.HasPrefix(a, "CHV_DISK_NUM_QUEUES=") || strings.HasPrefix(a, "CHV_DISK_IOTHREADS=") || strings.HasPrefix(a, "CHV_DISK_IO_ENGINE=") {
			t.Errorf("VM call = %v, unexpectedly set %s", vmCall, a)
		}
	}
}

// NETSHIM_IPV6 carries -ipv6 to the VM container, and is left off for a
// spec that never set it, so that VM's container is created exactly as
// it was before the setting existed.
func TestCreate_PassesIPv6Setting(t *testing.T) {
	d, calls := testDocker(t)
	var stdout strings.Builder

	spec := testSpec()
	spec.IPv6 = "off"
	if err := Create(context.Background(), d, spec, &stdout); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if vmCall := calls()[2]; !containsArg(vmCall, "NETSHIM_IPV6=off") {
		t.Errorf("VM call = %v, missing NETSHIM_IPV6=off", vmCall)
	}

	d, calls = testDocker(t)
	stdout.Reset()
	spec.IPv6 = ""
	if err := Create(context.Background(), d, spec, &stdout); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	for _, a := range calls()[2] {
		if strings.HasPrefix(a, "NETSHIM_IPV6") {
			t.Errorf("VM call passes %q for a spec with no IPv6 setting", a)
		}
	}
}

func TestCreate_SetsCgroupMemoryLimitAndPositiveOOMScoreAdj(t *testing.T) {
	d, calls := testDocker(t)
	spec := testSpec() // MemoryMB = 2048

	if err := Create(context.Background(), d, spec, &strings.Builder{}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	vmCall := calls()[2]
	joined := strings.Join(vmCall, " ")
	for _, want := range []string{
		"--oom-score-adj 500",
		"--memory 2560m",
		"--memory-swap 2560m",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("VM call = %q, missing %q", joined, want)
		}
	}

	// Explicit MemoryLimitMB and OOMScoreAdj override the derived defaults.
	d, calls = testDocker(t)
	spec.MemoryMB = 8192
	spec.MemoryLimitMB = 8704
	spec.OOMScoreAdj = 600
	if err := Create(context.Background(), d, spec, &strings.Builder{}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	joined = strings.Join(calls()[2], " ")
	for _, want := range []string{
		"--oom-score-adj 600",
		"--memory 8704m",
		"--memory-swap 8704m",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("VM call = %q, missing %q", joined, want)
		}
	}
}
