package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/granule"
	"github.com/bwsalmon/grain/pkg/k8s"
	"github.com/bwsalmon/grain/pkg/mcp"
	"github.com/bwsalmon/grain/pkg/model"
)

func TestGranulePodNameForNamesTheRun(t *testing.T) {
	t.Parallel()
	k := NewK8sGranuleSandboxes(K8sGranuleConfig{Image: "example/granule"})
	name, err := k.PodNameFor("358-1")
	if err != nil {
		t.Fatalf("PodNameFor: %v", err)
	}
	if name != GranulePodNamePrefix+"358-1" {
		t.Errorf("PodNameFor = %q, want the run's name behind %q", name, GranulePodNamePrefix)
	}
	// A different prefix than K8sSandboxes' own, so an operator reading
	// `kubectl get pods` against a namespace that has run both platforms
	// at different times can still tell which kind of pod a leftover
	// was.
	if GranulePodNamePrefix == PodNamePrefix {
		t.Fatal("GranulePodNamePrefix must not collide with PodNamePrefix")
	}
}

func TestGranulePodNameForRefusesANameKubernetesWould(t *testing.T) {
	t.Parallel()
	k := NewK8sGranuleSandboxes(K8sGranuleConfig{Image: "example/granule"})
	for _, sandbox := range []string{
		strings.Repeat("9", maxPodNameLen),
		"Upper-1",
		"7_1",
	} {
		if name, err := k.PodNameFor(sandbox); err == nil {
			t.Errorf("PodNameFor(%q) = %q, want an error", sandbox, name)
		}
	}
}

// TestGranulePodOptionsAreAlwaysPrivilegedWithKVM is this platform's
// entire premise, restated as a test: every pod this backend creates
// carries /dev/kvm and privileged: true, with no flag to turn either
// off -- unlike K8sConfig.Privileged, which is an opt-in escape hatch,
// this is not optional.
func TestGranulePodOptionsAreAlwaysPrivilegedWithKVM(t *testing.T) {
	t.Parallel()
	k := NewK8sGranuleSandboxes(K8sGranuleConfig{Image: "example/granule"})
	opts := k.podOptions(Shape{})
	if !opts.Privileged {
		t.Error("Privileged = false, want true unconditionally")
	}
	if len(opts.HostDevices) != 1 || opts.HostDevices[0] != DevKVM {
		t.Errorf("HostDevices = %v, want [%s]", opts.HostDevices, DevKVM)
	}
	if !opts.NoCommand {
		t.Error("NoCommand = false, want true: the image's own ENTRYPOINT boots the VM")
	}
	if len(opts.ReadinessExec) == 0 {
		t.Error("ReadinessExec is empty, want a probe that waits on the guest answering")
	}
}

// TestGranulePodOptionsDeriveCHVSizeFromShape is what makes the VM boot
// at the size the pod was actually given, rather than at the image's own
// baked-in default.
func TestGranulePodOptionsDeriveCHVSizeFromShape(t *testing.T) {
	t.Parallel()
	k := NewK8sGranuleSandboxes(K8sGranuleConfig{Image: "example/granule"})
	opts := k.podOptions(Shape{CPUs: 4, MemoryMB: 8192, DiskGB: 40})
	if opts.Env["CHV_CPUS"] != "4" {
		t.Errorf("CHV_CPUS = %q, want 4", opts.Env["CHV_CPUS"])
	}
	if opts.Env["CHV_MEMORY_MB"] != "8192" {
		t.Errorf("CHV_MEMORY_MB = %q, want 8192", opts.Env["CHV_MEMORY_MB"])
	}
	if opts.Env["CHV_DISK_SIZE_MB"] != "40960" {
		t.Errorf("CHV_DISK_SIZE_MB = %q, want 40960", opts.Env["CHV_DISK_SIZE_MB"])
	}
	defOpts := k.podOptions(Shape{})
	wantDefaultDiskMB := strconv.Itoa(DefaultShape().DiskGB * mibPerGiB)
	if defOpts.Env["CHV_DISK_SIZE_MB"] != wantDefaultDiskMB {
		t.Errorf("default CHV_DISK_SIZE_MB = %q, want %s", defOpts.Env["CHV_DISK_SIZE_MB"], wantDefaultDiskMB)
	}
}

// TestGranulePodOptionsRunExecSessionsAsTheSandboxAccount pins the
// account every `granule exec` into the pod's guest runs as. Without
// GRANULE_EXEC_USER on the pod, guestexec defaults to root, and every
// task and CI step ran as uid 0 -- where initdb refuses to start and
// permission-based tests stop meaning anything. A deployment's own Env
// can still name another account.
func TestGranulePodOptionsRunExecSessionsAsTheSandboxAccount(t *testing.T) {
	t.Parallel()
	k := NewK8sGranuleSandboxes(K8sGranuleConfig{Image: "example/granule"})
	if got := k.podOptions(Shape{}).Env["GRANULE_EXEC_USER"]; got != DefaultK8sGranuleGuestUser {
		t.Errorf("GRANULE_EXEC_USER = %q, want %q", got, DefaultK8sGranuleGuestUser)
	}
	k = NewK8sGranuleSandboxes(K8sGranuleConfig{Image: "example/granule", Env: map[string]string{"GRANULE_EXEC_USER": "app"}})
	if got := k.podOptions(Shape{}).Env["GRANULE_EXEC_USER"]; got != "app" {
		t.Errorf("GRANULE_EXEC_USER = %q with Env naming app, want app", got)
	}
}

// TestGranulePodOptionsNetworkNoneMeansNoGuestNetworkDevice is the
// property that lets IsolatesNetwork answer true unconditionally: the
// guest itself has no network device, which is enforced by granule, not
// by a cluster policy object.
func TestGranulePodOptionsNetworkNoneMeansNoGuestNetworkDevice(t *testing.T) {
	t.Parallel()
	k := NewK8sGranuleSandboxes(K8sGranuleConfig{Image: "example/granule"})
	none := k.podOptions(Shape{NetworkAccess: model.NetworkNone})
	if none.Env["CHV_NET_MODE"] != "none" {
		t.Errorf("CHV_NET_MODE = %q, want none", none.Env["CHV_NET_MODE"])
	}
	if none.Labels[NetworkLabel] != NetworkNoneValue {
		t.Errorf("labels = %v, want %s=%s even though enforcement does not depend on it",
			none.Labels, NetworkLabel, NetworkNoneValue)
	}
	open := k.podOptions(Shape{NetworkAccess: model.NetworkOpen})
	if open.Env["CHV_NET_MODE"] != "inline" {
		t.Errorf("CHV_NET_MODE = %q, want inline", open.Env["CHV_NET_MODE"])
	}
}

// TestGranuleIsolatesNetworkUnconditionally is the whole reason this
// backend does not need K8sConfig.NetworkPolicyEnforced's own flag: the
// isolation is the guest's own missing network device, true on every
// cluster regardless of what its CNI enforces.
func TestGranuleIsolatesNetworkUnconditionally(t *testing.T) {
	t.Parallel()
	var iso NetworkIsolator = NewK8sGranuleSandboxes(K8sGranuleConfig{Image: "example/granule"})
	if !iso.IsolatesNetwork() {
		t.Error("IsolatesNetwork() = false, want true unconditionally")
	}
}

// TestGranuleAcquireNeverRefusesNetworkNone is K8sSandboxes.Acquire's
// refusal (TestAcquireRefusesNetworkNoneWithoutAnEnforcingCluster), the
// other way: this backend has nothing to assert before honouring
// network:none, so Acquire never has to refuse it for that reason. It
// still fails here, but only because there is no real cluster behind
// the fake "kubectl" on $PATH -- not because of the network request.
func TestGranuleAcquireNeverRefusesNetworkNone(t *testing.T) {
	t.Parallel()
	k := NewK8sGranuleSandboxes(K8sGranuleConfig{Image: "example/granule", Kubectl: "/nonexistent-kubectl-binary"})
	_, err := k.Acquire(context.Background(), "7-1", Shape{NetworkAccess: model.NetworkNone})
	if err == nil {
		t.Fatal("Acquire against a nonexistent kubectl succeeded")
	}
	if strings.Contains(err.Error(), "network-policy") || strings.Contains(err.Error(), "NetworkPolicyEnforced") {
		t.Errorf("error = %q, this backend should never refuse on network grounds", err)
	}
}

func TestGranulePodShapeFillsEveryDimensionNobodyNamed(t *testing.T) {
	t.Parallel()
	k := NewK8sGranuleSandboxes(K8sGranuleConfig{Image: "example/granule"})
	got := k.podShape(Shape{})
	want := DefaultShape()
	if got != (k8s.PodShape{CPUs: want.CPUs, MemoryMB: want.MemoryMB + GranuleVMMMemoryOverheadMB, DiskGB: want.DiskGB}) {
		t.Errorf("podShape of an unset shape = %+v, want grain's own DefaultShape %+v plus %d MiB VMM overhead", got, want, GranuleVMMMemoryOverheadMB)
	}
}

func TestGranuleSetDefaultShapeReachesTheNextSandbox(t *testing.T) {
	t.Parallel()
	k := NewK8sGranuleSandboxes(K8sGranuleConfig{Image: "example/granule", DefaultCPUs: 1})
	k.SetDefaultShape(Shape{CPUs: 6, MemoryMB: 2048, DiskGB: 10})
	if got := k.podShape(Shape{}.orDefault(k.defaultShape())); got.CPUs != 6 {
		t.Errorf("CPUs after SetDefaultShape = %d, want 6", got.CPUs)
	}
	var _ interface{ SetDefaultShape(Shape) } = k
}

func TestGranuleSetOvercommitScalesPodShapeAndKeepsCHVAtFullShape(t *testing.T) {
	t.Parallel()
	k := NewK8sGranuleSandboxes(K8sGranuleConfig{
		Image:            "example/granule",
		DefaultCPUs:      8,
		DefaultMemoryMB:  16384,
		DefaultDiskGB:    30,
		CPUOvercommit:    10,
		MemoryOvercommit: 10,
	})
	resolved := Shape{}.orDefault(k.defaultShape())
	gotShape := k.podShape(resolved)
	if gotShape.CPUs != 8 || gotShape.MemoryMB != 16384+GranuleVMMMemoryOverheadMB || gotShape.CPUOvercommit != 10 || gotShape.MemoryOvercommit != 10 {
		t.Fatalf("podShape = %+v, want 8 CPUs, 16384 MiB plus VMM overhead, 10x/10x overcommit", gotShape)
	}
	opts := k.podOptions(resolved)
	if opts.Env["CHV_CPUS"] != "8" || opts.Env["CHV_MEMORY_MB"] != "16384" || opts.Env["CHV_DISK_SIZE_MB"] != "30720" {
		t.Fatalf("CHV env = %v, want full unscaled CHV_CPUS=8, CHV_MEMORY_MB=16384, CHV_DISK_SIZE_MB=30720", opts.Env)
	}

	k.SetOvercommit(5, 2.5, 10)
	gotShape = k.podShape(resolved)
	if gotShape.CPUOvercommit != 5 || gotShape.MemoryOvercommit != 2.5 || gotShape.DiskOvercommit != 10 || gotShape.DiskGB != 30 {
		t.Fatalf("podShape after SetOvercommit = %+v, want 5x/2.5x/10x overcommit at the full 30 GiB disk", gotShape)
	}
	opts = k.podOptions(resolved)
	if opts.Env["CHV_CPUS"] != "8" || opts.Env["CHV_MEMORY_MB"] != "16384" || opts.Env["CHV_DISK_SIZE_MB"] != "30720" {
		t.Fatalf("CHV env after SetOvercommit = %v, want full unscaled CHV_CPUS=8, CHV_MEMORY_MB=16384, CHV_DISK_SIZE_MB=30720", opts.Env)
	}
	var _ interface {
		SetOvercommit(cpu, memory, disk float64)
	} = k
}

// Disk overcommit shrinks what the scheduler bin-packs (the pod's
// ephemeral-storage request) and nothing the guest sees: the VM's root
// disk is still created at the full size, so the rootfs grows onto all
// of it.
func TestGranuleDiskOvercommitScalesTheStorageRequestAndKeepsTheGuestDisk(t *testing.T) {
	t.Parallel()
	k := NewK8sGranuleSandboxes(K8sGranuleConfig{
		Image:           "example/granule",
		DefaultCPUs:     4,
		DefaultMemoryMB: 8192,
		DefaultDiskGB:   40,
		DiskOvercommit:  10,
	})
	resolved := Shape{}.orDefault(k.defaultShape())
	if _, _, disk := k.Overcommit(); disk != 10 {
		t.Fatalf("Overcommit() disk = %v, want the configured 10", disk)
	}
	raw, err := k.client.Manifest("grain-granule-1", k.podShape(resolved), k.podOptions(resolved))
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	var pod struct {
		Spec struct {
			Containers []struct {
				Resources struct {
					Requests map[string]string `json:"requests"`
					Limits   map[string]string `json:"limits"`
				} `json:"resources"`
			} `json:"containers"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(raw, &pod); err != nil {
		t.Fatalf("decoding manifest: %v", err)
	}
	res := pod.Spec.Containers[0].Resources
	if res.Requests["ephemeral-storage"] != "4096Mi" || res.Limits["ephemeral-storage"] != "40Gi" {
		t.Errorf("ephemeral-storage request/limit = %q/%q, want 4096Mi/40Gi",
			res.Requests["ephemeral-storage"], res.Limits["ephemeral-storage"])
	}
	if got := k.podOptions(resolved).Env["CHV_DISK_SIZE_MB"]; got != "40960" {
		t.Errorf("CHV_DISK_SIZE_MB = %q, want the full unscaled 40960", got)
	}
}

// TestK8sGranuleSandboxesAnswersTheSameInterfacesGranuleDoes is
// TestK8sSandboxesAnswersTheSameInterfacesGranuleDoes's counterpart for
// this backend.
func TestK8sGranuleSandboxesAnswersTheSameInterfacesGranuleDoes(t *testing.T) {
	t.Parallel()
	var sandboxes Sandboxes = NewK8sGranuleSandboxes(K8sGranuleConfig{Image: "example/granule"})
	if _, ok := sandboxes.(interface {
		ReapOrphans(context.Context) (int, error)
	}); !ok {
		t.Error("K8sGranuleSandboxes has no startup orphan sweep, so pods a killed process left behind are never reaped")
	}
	if _, ok := sandboxes.(interface {
		SweepOrphans(context.Context) (int, error)
	}); !ok {
		t.Error("K8sGranuleSandboxes has no live orphan sweep, so a pod whose delete failed holds its node until the next restart")
	}
	var sandbox Sandbox = &k8sGranuleSandbox{}
	if _, ok := sandbox.(SandboxPlacer); !ok {
		t.Error("k8sGranuleSandbox is not a SandboxPlacer: every capability placement would fail during preparation")
	}
	if _, ok := sandbox.(SandboxRebuilder); !ok {
		t.Error("k8sGranuleSandbox is not a SandboxRebuilder: recreate_sandbox would be refused on this backend alone")
	}
	if _, ok := sandbox.(SandboxShell); !ok {
		t.Error("k8sGranuleSandbox is not a SandboxShell: every grain CI step would go through run_command and its ten-minute ceiling")
	}
	if _, ok := sandbox.(SandboxPortDialer); !ok {
		t.Error("k8sGranuleSandbox is not a SandboxPortDialer: persistent sandbox port forwarding would fail on this backend")
	}
	if _, ok := sandbox.(rootedSandbox); ok {
		t.Error("k8sGranuleSandbox claims a local root it does not have")
	}
}

// streamRecordingRunner is a sandboxRunner that also streams, recording
// the argv it was handed and answering with fixed output and status.
type streamRecordingRunner struct {
	argv     []string
	streamed bool
	out      string
	code     int
}

func (r *streamRecordingRunner) Run(ctx context.Context, argv []string, stdin string) (string, string, int) {
	r.argv = argv
	return r.out, "", r.code
}

func (r *streamRecordingRunner) RunStream(ctx context.Context, argv []string, stdin string, out io.Writer) (string, string, int) {
	r.argv, r.streamed = argv, true
	_, _ = io.WriteString(out, r.out)
	return r.out, "", r.code
}

func TestK8sGranuleRunShellBoundsTheStepInTheGuestAtItsOwnTimeout(t *testing.T) {
	t.Parallel()
	runner := &streamRecordingRunner{out: "ok\n"}
	k := NewK8sGranuleSandboxes(K8sGranuleConfig{Image: "example/granule"})
	sb := &k8sGranuleSandbox{owner: k, name: "796-4", podName: "grain-granule-796-4", runner: runner}

	var live strings.Builder
	res, err := sb.RunShell(context.Background(), ShellRequest{
		Script: "go test ./...", Dir: "work", Timeout: 25 * time.Minute, Output: &live,
	})
	if err != nil {
		t.Fatalf("RunShell: %v", err)
	}
	if res.ExitCode != 0 || res.Output != "ok\n" || live.String() != "ok\n" || !runner.streamed {
		t.Fatalf("RunShell = %+v, live %q, streamed %v; want the streamed output and status 0", res, live.String(), runner.streamed)
	}
	script := strings.Join(runner.argv, " ")
	// A 25-minute step is bounded at 25 minutes inside the guest -- not
	// at run_command's 600s ceiling, which is what killed every long CI
	// step on this backend before it had a RunShell.
	if !strings.Contains(script, "timeout --kill-after=10s 1500 bash -c") {
		t.Fatalf("the guest script does not bound the step at its own 1500s: %s", script)
	}
	if !strings.Contains(script, "cd '\\''/home/debian/work'\\''") || !strings.Contains(script, "go test ./...") {
		t.Fatalf("the guest script does not run the step in the workspace: %s", script)
	}
}

func TestK8sGranuleRunShellReportsACutOffSessionAsTheTransport(t *testing.T) {
	t.Parallel()
	runner := &streamRecordingRunner{out: "half a step\n" + mcp.SessionCutOffNotice, code: -1}
	k := NewK8sGranuleSandboxes(K8sGranuleConfig{Image: "example/granule"})
	sb := &k8sGranuleSandbox{owner: k, name: "796-5", podName: "grain-granule-796-5", runner: runner}
	if _, err := sb.RunShell(context.Background(), ShellRequest{Script: "make test"}); err == nil || !strings.Contains(err.Error(), "grain-granule-796-5") {
		t.Fatalf("RunShell error = %v, want a transport error naming the pod", err)
	}
}

// A -1 that is not a cut-off session is still the transport's, and its
// error names the transport's last word rather than repeating the step's
// whole output.
func TestK8sGranuleRunShellReportsAnExecFailureByItsLastLine(t *testing.T) {
	t.Parallel()
	log := strings.Repeat("a line of the step's own log\n", 1000) + "error: unable to upgrade connection\n"
	runner := &streamRecordingRunner{out: log, code: -1}
	k := NewK8sGranuleSandboxes(K8sGranuleConfig{Image: "example/granule"})
	sb := &k8sGranuleSandbox{owner: k, name: "796-6", podName: "grain-granule-796-6", runner: runner}
	res, err := sb.RunShell(context.Background(), ShellRequest{Script: "make test"})
	if err == nil || !strings.Contains(err.Error(), "unable to upgrade connection") || strings.Contains(err.Error(), "step's own log") {
		t.Fatalf("RunShell error = %v, want only the transport's last line", err)
	}
	if res.ExitCode != -1 || res.Output != log {
		t.Fatalf("RunShell = exit %d, %d bytes of output; want -1 and the whole output", res.ExitCode, len(res.Output))
	}
}

// TestGranuleExecRunnerGoesOneLayerDeeper is the one-line difference in
// shape docs/k8s-granule-design.md §5 describes: a granule pod's main
// process is cloud-hypervisor, not a shell waiting to be exec'd into, so
// every argv has to be handed to the guest over granule's own exec
// protocol rather than run directly in the container.
func TestGranuleExecRunnerGoesOneLayerDeeper(t *testing.T) {
	t.Parallel()
	k := NewK8sGranuleSandboxes(K8sGranuleConfig{Image: "example/granule"})
	got := k.execRunner("p").Through
	want := []string{"granule", "exec", "--"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("execRunner.Through = %v, want %v", got, want)
	}
}

func TestGranuleExecRunnerStreamsAndDialsThroughGranuleExecPrefix(t *testing.T) {
	t.Parallel()
	kubectl := filepath.Join(t.TempDir(), "kubectl")
	script := `#!/bin/sh
while [ $# -gt 0 ]; do
	if [ "$1" = "--" ]; then
		shift
		break
	fi
	shift
done
if [ "$1" != "granule" ] || [ "$2" != "exec" ] || [ "$3" != "--" ]; then
	echo "missing granule exec prefix: $*" >&2
	exit 2
fi
shift 3
exec "$@"
`
	src := kubectl + ".src"
	if err := os.WriteFile(src, []byte(script), 0o644); err != nil {
		t.Fatalf("writing fake kubectl: %v", err)
	}
	if out, err := exec.Command("install", "-m", "0755", src, kubectl).CombinedOutput(); err != nil {
		t.Fatalf("installing fake kubectl: %v: %s", err, out)
	}
	k := NewK8sGranuleSandboxes(K8sGranuleConfig{
		Namespace: "gke-sandboxes",
		Kubectl:   kubectl,
		Image:     "example/granule:latest",
		Workspace: "/tmp",
	})
	sb := &k8sGranuleSandbox{
		owner:   k,
		name:    "p12345678",
		podName: "grain-granule-p12345678",
		runner:  k.execRunner("grain-granule-p12345678"),
	}

	var followed strings.Builder
	res, err := sb.RunShell(context.Background(), ShellRequest{
		Script: "echo streamed-line",
		Output: &followed,
	})
	if err != nil {
		t.Fatalf("RunShell: %v", err)
	}
	if res.ExitCode != 0 || strings.TrimSpace(res.Output) != "streamed-line" || followed.String() != res.Output {
		t.Fatalf("RunShell = (%q, %d), followed = %q; want streamed-line", res.Output, res.ExitCode, followed.String())
	}

	conn, err := sb.runner.(streamRunner).DialStream(context.Background(), []string{"cat"})
	if err != nil {
		t.Fatalf("DialStream: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("ping\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	buf := make([]byte, 16)
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	n, err := conn.Read(buf)
	if err != nil || string(buf[:n]) != "ping\n" {
		t.Fatalf("Read = (%q, %v), want (\"ping\\n\", nil)", string(buf[:n]), err)
	}
}

func writeFakeKubectlForGranule(t *testing.T, phaseFile string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "kubectl")
	script := `#!/bin/sh
phase="Running"
if [ -n "$1" ] && [ -f "` + phaseFile + `" ]; then
	phase=$(cat "` + phaseFile + `")
fi
for arg in "$@"; do
	case "$arg" in
		apply|delete)
			exit 0
			;;
	esac
done
printf '%s|True|docker-pullable://example/granule@sha256:abc|\n' "$phase"
`
	// The script is written as plain data and made executable by a child
	// process: a write fd held here could be inherited by any parallel
	// test's fork and make exec'ing the script fail with ETXTBSY.
	src := bin + ".src"
	if err := os.WriteFile(src, []byte(script), 0o644); err != nil {
		t.Fatalf("writing fake kubectl: %v", err)
	}
	if out, err := exec.Command("install", "-m", "0755", src, bin).CombinedOutput(); err != nil {
		t.Fatalf("installing fake kubectl: %v: %s", err, out)
	}
	return bin
}

func TestK8sGranuleSandboxesWiresKubeconfigAndReservedGuestPorts(t *testing.T) {
	t.Parallel()
	k := NewK8sGranuleSandboxes(K8sGranuleConfig{
		Namespace:          "gke-sandboxes",
		Kubectl:            "/opt/bin/kubectl",
		Kubeconfig:         "/var/lib/grain/kubeconfig",
		Image:              "example/granule:latest",
		Workspace:          "/home/debian",
		ForwardedPorts:     []int{18080, 22},
		ForwardedPortsFunc: func() []int { return []int{18081, 18080, 18096} },
	})
	if k.client.Kubeconfig != "/var/lib/grain/kubeconfig" {
		t.Fatalf("client.Kubeconfig = %q, want /var/lib/grain/kubeconfig", k.client.Kubeconfig)
	}
	runner := k.execRunner("grain-granule-42-1")
	if runner.Kubeconfig != "/var/lib/grain/kubeconfig" {
		t.Fatalf("execRunner.Kubeconfig = %q, want /var/lib/grain/kubeconfig", runner.Kubeconfig)
	}
	wantReserved := []int{22, 18080, 18081, 18096}
	if got := k.ReservedGuestPorts(); !reflect.DeepEqual(got, wantReserved) {
		t.Fatalf("ReservedGuestPorts() = %v, want %v", got, wantReserved)
	}
	sb := &k8sGranuleSandbox{owner: k, name: "42-1", podName: "grain-granule-42-1"}
	pod := sb.KubePod()
	if pod.Kubeconfig != "/var/lib/grain/kubeconfig" || !pod.Granule || pod.Namespace != "gke-sandboxes" || pod.Pod != "grain-granule-42-1" {
		t.Fatalf("KubePod() = %+v, want kubeconfig=/var/lib/grain/kubeconfig, Granule=true", pod)
	}
}

func TestK8sGranuleSandboxesSupervisesGranuleForwardReconnectsAndTearsDownOnRelease(t *testing.T) {
	t.Parallel()
	phaseFile := filepath.Join(t.TempDir(), "phase")
	if err := os.WriteFile(phaseFile, []byte("Running"), 0o644); err != nil {
		t.Fatalf("WriteFile phase: %v", err)
	}
	kubectl := writeFakeKubectlForGranule(t, phaseFile)

	var mu sync.Mutex
	var seenCfgs []granule.KubectlForwardConfig
	var calls atomic.Int32
	secondAttempt := make(chan struct{})
	releaseEntered := make(chan struct{})

	k := NewK8sGranuleSandboxes(K8sGranuleConfig{
		Namespace:          "gke-sandboxes",
		Kubectl:            kubectl,
		Kubeconfig:         "/var/lib/grain/kubeconfig",
		Image:              "example/granule:latest",
		ReadyPollInterval:  5 * time.Millisecond,
		ForwardBackoff:     10 * time.Millisecond,
		ForwardedPortsFunc: func() []int { return []int{18432, 18433, 18448} },
		ForwardRunner: func(ctx context.Context, cfg granule.KubectlForwardConfig) error {
			mu.Lock()
			seenCfgs = append(seenCfgs, cfg)
			mu.Unlock()
			if cfg.OnReady != nil {
				cfg.OnReady()
			}
			n := calls.Add(1)
			if n == 1 {
				// Simulate the first `kubectl exec -i` stream dropping while the pod is still Running.
				return errors.New("stream dropped")
			}
			if n == 2 {
				close(secondAttempt)
			}
			<-ctx.Done()
			close(releaseEntered)
			return ctx.Err()
		},
	})

	sb, err := k.Acquire(context.Background(), "721-1", Shape{NetworkAccess: model.NetworkNone})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	select {
	case <-secondAttempt:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for granule forward supervisor to reconnect (calls=%d)", calls.Load())
	}

	mu.Lock()
	if len(seenCfgs) < 2 {
		mu.Unlock()
		t.Fatalf("seenCfgs len = %d, want >= 2", len(seenCfgs))
	}
	first := seenCfgs[0]
	mu.Unlock()

	wantPorts := []int{18432, 18433, 18448}
	if !reflect.DeepEqual(first.RemotePorts, wantPorts) {
		t.Fatalf("RemotePorts = %v, want %v", first.RemotePorts, wantPorts)
	}
	if first.Kubeconfig != "/var/lib/grain/kubeconfig" || first.Namespace != "gke-sandboxes" || first.Pod != "grain-granule-721-1" || first.Container != k8s.SandboxContainer {
		t.Fatalf("KubectlForwardConfig = %+v", first)
	}

	if err := sb.Release(context.Background()); err != nil {
		t.Fatalf("Release: %v", err)
	}

	select {
	case <-releaseEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for granule forward session to be cancelled on Release")
	}
}

func TestK8sGranuleAcquireWaitsForForwardReadinessAcrossFailedAttempts(t *testing.T) {
	t.Parallel()
	phaseFile := filepath.Join(t.TempDir(), "phase")
	if err := os.WriteFile(phaseFile, []byte("Running"), 0o644); err != nil {
		t.Fatalf("WriteFile phase: %v", err)
	}
	var calls atomic.Int32
	var listening atomic.Bool
	k := NewK8sGranuleSandboxes(K8sGranuleConfig{
		Kubectl:           writeFakeKubectlForGranule(t, phaseFile),
		Image:             "example/granule:latest",
		ReadyPollInterval: 5 * time.Millisecond,
		ForwardBackoff:    time.Millisecond,
		ForwardedPorts:    []int{18432},
		ForwardRunner: func(ctx context.Context, cfg granule.KubectlForwardConfig) error {
			// The first two `kubectl exec` attempts fail the way a
			// congested API server makes them fail, before the guest
			// side ever binds; the third binds, late.
			if calls.Add(1) <= 2 {
				return errors.New("dial tcp 10.0.0.1:443: connect: connection timed out")
			}
			time.Sleep(50 * time.Millisecond)
			listening.Store(true)
			cfg.OnReady()
			<-ctx.Done()
			return ctx.Err()
		},
	})

	sb, err := k.Acquire(context.Background(), "796-1", Shape{})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer sb.Release(context.Background())
	if !listening.Load() {
		t.Fatalf("Acquire returned before granule forward reported its guest listeners ready (calls=%d)", calls.Load())
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("forward attempts = %d, want 3", got)
	}
}

func TestK8sGranuleAcquireFailsWhenForwardNeverBecomesReady(t *testing.T) {
	t.Parallel()
	phaseFile := filepath.Join(t.TempDir(), "phase")
	if err := os.WriteFile(phaseFile, []byte("Running"), 0o644); err != nil {
		t.Fatalf("WriteFile phase: %v", err)
	}
	var calls atomic.Int32
	k := NewK8sGranuleSandboxes(K8sGranuleConfig{
		Kubectl:             writeFakeKubectlForGranule(t, phaseFile),
		Image:               "example/granule:latest",
		ReadyPollInterval:   5 * time.Millisecond,
		ForwardBackoff:      time.Millisecond,
		ForwardReadyTimeout: 100 * time.Millisecond,
		ForwardedPorts:      []int{18432},
		ForwardRunner: func(ctx context.Context, cfg granule.KubectlForwardConfig) error {
			calls.Add(1)
			return errors.New("connection timed out")
		},
	})

	if _, err := k.Acquire(context.Background(), "796-2", Shape{}); err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("Acquire error = %v, want a forward-not-ready error", err)
	}
	k.mu.Lock()
	_, tracked := k.live["796-2"]
	k.mu.Unlock()
	if tracked {
		t.Fatal("a sandbox whose forward never came up is still tracked as live")
	}
	// The supervisor was stopped along with the failed Acquire.
	after := calls.Load()
	time.Sleep(50 * time.Millisecond)
	if got := calls.Load(); got != after {
		t.Fatalf("forward attempts went on after Acquire failed: %d -> %d", after, got)
	}
}

func TestK8sGranuleForwardBackoffGrowsOnImmediateDropsAndResetsAfterHealthySession(t *testing.T) {
	t.Parallel()
	phaseFile := filepath.Join(t.TempDir(), "phase")
	if err := os.WriteFile(phaseFile, []byte("Running"), 0o644); err != nil {
		t.Fatalf("WriteFile phase: %v", err)
	}
	const healthy = 100 * time.Millisecond
	var calls atomic.Int32
	k := NewK8sGranuleSandboxes(K8sGranuleConfig{
		Kubectl:               writeFakeKubectlForGranule(t, phaseFile),
		Image:                 "example/granule:latest",
		ReadyPollInterval:     5 * time.Millisecond,
		ForwardBackoff:        2 * time.Millisecond,
		ForwardMaxBackoff:     8 * time.Millisecond,
		ForwardHealthySession: healthy,
		ForwardedPorts:        []int{18432},
		ForwardRunner: func(ctx context.Context, cfg granule.KubectlForwardConfig) error {
			cfg.OnReady()
			if calls.Add(1) == 5 {
				// One session stays up past the healthy threshold
				// before it drops.
				time.Sleep(healthy + 20*time.Millisecond)
			}
			return errors.New("stream dropped")
		},
	})
	var mu sync.Mutex
	var delays []time.Duration
	enough := make(chan struct{})
	k.onForwardBackoff = func(d time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		delays = append(delays, d)
		if len(delays) == 7 {
			close(enough)
		}
	}

	sb, err := k.Acquire(context.Background(), "796-3", Shape{})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	select {
	case <-enough:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for reconnects (calls=%d)", calls.Load())
	}
	if err := sb.Release(context.Background()); err != nil {
		t.Fatalf("Release: %v", err)
	}

	mu.Lock()
	got := append([]time.Duration(nil), delays[:7]...)
	mu.Unlock()
	// Nominal delays: 2, 4, 8, 8 ms across four immediate drops, then
	// back to 2 after the healthy fifth session, growing again after.
	nominal := []time.Duration{2, 4, 8, 8, 2, 4, 8}
	for i, n := range nominal {
		want := n * time.Millisecond
		if got[i] < want/2 || got[i] > want {
			t.Fatalf("delay %d = %s, want within [%s, %s] (all: %v)", i, got[i], want/2, want, got)
		}
	}
}

func TestForwardBackoffSequence(t *testing.T) {
	t.Parallel()
	b := forwardBackoff{
		base:    500 * time.Millisecond,
		max:     10 * time.Second,
		healthy: 10 * time.Second,
		jitter:  func(d time.Duration) time.Duration { return d },
	}
	var got []time.Duration
	for range 7 {
		got = append(got, b.next(time.Millisecond))
	}
	want := []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 10 * time.Second, 10 * time.Second}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("immediate-drop delays = %v, want %v", got, want)
	}
	if d := b.next(11 * time.Second); d != 500*time.Millisecond {
		t.Fatalf("delay after a healthy session = %s, want 500ms", d)
	}
	if d := b.next(0); d != time.Second {
		t.Fatalf("delay after the next immediate drop = %s, want 1s", d)
	}
}

func TestEqualJitterStaysWithinHalfToFull(t *testing.T) {
	t.Parallel()
	d := 10 * time.Second
	seen := make(map[time.Duration]bool)
	for range 200 {
		j := equalJitter(d)
		if j < d/2 || j > d {
			t.Fatalf("equalJitter(%s) = %s, want within [%s, %s]", d, j, d/2, d)
		}
		seen[j] = true
	}
	if len(seen) < 2 {
		t.Fatal("equalJitter returned the same delay every time")
	}
}

func TestK8sGranuleSandboxesHostCapacityQueriesClusterNodesWithNodeSelector(t *testing.T) {
	t.Parallel()
	kubectl := filepath.Join(t.TempDir(), "kubectl")
	script := `#!/bin/sh
cat <<'EOF'
{
  "items": [
    {
      "metadata": {"name": "kvm-1", "labels": {"cloud.google.com/gke-nodepool": "kvm-pool"}},
      "spec": {},
      "status": {
        "allocatable": {"cpu": "15890m", "memory": "61440Mi"},
        "conditions": [{"type": "Ready", "status": "True"}]
      }
    },
    {
      "metadata": {"name": "kvm-2", "labels": {"cloud.google.com/gke-nodepool": "kvm-pool"}},
      "spec": {},
      "status": {
        "allocatable": {"cpu": "15890m", "memory": "61440Mi"},
        "conditions": [{"type": "Ready", "status": "True"}]
      }
    },
    {
      "metadata": {"name": "default-1", "labels": {"cloud.google.com/gke-nodepool": "default-pool"}},
      "spec": {},
      "status": {
        "allocatable": {"cpu": "8", "memory": "32Gi"},
        "conditions": [{"type": "Ready", "status": "True"}]
      }
    }
  ]
}
EOF
`
	if err := os.WriteFile(kubectl, []byte(script), 0o755); err != nil {
		t.Fatalf("writing fake kubectl: %v", err)
	}
	k := NewK8sGranuleSandboxes(K8sGranuleConfig{
		Image:        "example/granule",
		Kubectl:      kubectl,
		NodeSelector: map[string]string{"cloud.google.com/gke-nodepool": "kvm-pool"},
	})
	cpus, memoryMB, _, err := k.HostCapacity(context.Background())
	if err != nil {
		t.Fatalf("HostCapacity: %v", err)
	}
	// 15890m + 15890m = 31780m -> 31 whole cores; 61440Mi + 61440Mi = 122880 MiB.
	if cpus != 31 || memoryMB != 122880 {
		t.Fatalf("HostCapacity() = (%d, %d), want (31, 122880)", cpus, memoryMB)
	}
}

// sweepKubectl is a fake kubectl that keeps its cluster in a directory:
// `get pods -l` lists the names in pods, `get pod <name>` answers the
// phase in phase (Running when there is none), and `delete pod <name>`
// appends name to deleted -- or, while fail-deletes holds a count above
// zero, fails the way an API server that answered with a timeout does,
// and counts down.
type sweepKubectl struct {
	dir string
	bin string
}

func writeSweepKubectl(t *testing.T) *sweepKubectl {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "kubectl")
	script := `#!/bin/sh
d="` + dir + `"
echo "$*" >>"$d/log"
verb=""; prev=""; name=""; list=""
for arg in "$@"; do
	case "$arg" in
		get|delete|apply) [ -z "$verb" ] && verb=$arg ;;
		-l) list=1 ;;
	esac
	if [ "$verb" = delete ] && [ "$prev" = pod ] && [ -z "$name" ]; then
		name=$arg
	fi
	prev=$arg
done
case "$verb" in
	get)
		if [ -n "$list" ]; then
			cat "$d/pods" 2>/dev/null
			exit 0
		fi
		phase=Running
		[ -f "$d/phase" ] && phase=$(cat "$d/phase")
		printf '%s|True|docker-pullable://example/granule@sha256:abc|\n' "$phase"
		;;
	delete)
		n=$(cat "$d/fail-deletes" 2>/dev/null || echo 0)
		if [ "$n" -gt 0 ]; then
			echo $((n - 1)) >"$d/fail-deletes"
			echo "Error from server (Timeout): the server was unable to return a response in the time allotted" >&2
			exit 1
		fi
		echo "$name" >>"$d/deleted"
		;;
esac
exit 0
`
	src := bin + ".src"
	if err := os.WriteFile(src, []byte(script), 0o644); err != nil {
		t.Fatalf("writing fake kubectl: %v", err)
	}
	if out, err := exec.Command("install", "-m", "0755", src, bin).CombinedOutput(); err != nil {
		t.Fatalf("installing fake kubectl: %v: %s", err, out)
	}
	return &sweepKubectl{dir: dir, bin: bin}
}

func (f *sweepKubectl) write(t *testing.T, file, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, file), []byte(content), 0o644); err != nil {
		t.Fatalf("writing fake kubectl's %s: %v", file, err)
	}
}

func (f *sweepKubectl) setPods(t *testing.T, names ...string) {
	f.write(t, "pods", strings.Join(names, "\n")+"\n")
}

func (f *sweepKubectl) setPhase(t *testing.T, phase string) { f.write(t, "phase", phase) }

func (f *sweepKubectl) failDeletes(t *testing.T, n int) { f.write(t, "fail-deletes", strconv.Itoa(n)) }

// takeDeleted returns the pods deleted since it was last called.
func (f *sweepKubectl) takeDeleted(t *testing.T) []string {
	t.Helper()
	path := filepath.Join(f.dir, "deleted")
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("reading fake kubectl's deleted: %v", err)
	}
	_ = os.Remove(path)
	return strings.Fields(string(data))
}

// takeCalls returns every command line the fake has been run with since
// it was last called.
func (f *sweepKubectl) takeCalls(t *testing.T) []string {
	t.Helper()
	path := filepath.Join(f.dir, "log")
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("reading fake kubectl's log: %v", err)
	}
	_ = os.Remove(path)
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// podInstanceLabel is the k8s.InstanceLabel c's manifest gives the
// pod, and whether it has one at all.
func podInstanceLabel(t *testing.T, c k8s.Client, name string, shape k8s.PodShape, opts k8s.PodOptions) (string, bool) {
	t.Helper()
	manifest, err := c.Manifest(name, shape, opts)
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	var pod struct {
		Metadata struct {
			Labels map[string]string `json:"labels"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(manifest, &pod); err != nil {
		t.Fatalf("unmarshalling the manifest: %v", err)
	}
	v, ok := pod.Metadata.Labels[k8s.InstanceLabel]
	return v, ok
}

// assertSweepSelects fails t unless the sweep's calls list pods
// labelled instance -- and unlabelled ones, which predate the label.
func assertSweepSelects(t *testing.T, calls []string, instance string) {
	t.Helper()
	joined := strings.Join(calls, "\n")
	for _, want := range []string{k8s.InstanceLabel + "=" + instance, "!" + k8s.InstanceLabel} {
		if !strings.Contains(joined, want) {
			t.Errorf("sweep calls %q do not select on %q", calls, want)
		}
	}
}

func TestK8sGranuleSandboxesLabelAndSweepTheirInstance(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	kubectl := writeSweepKubectl(t)
	k := NewK8sGranuleSandboxes(K8sGranuleConfig{Kubectl: kubectl.bin, Image: "example/granule", Instance: "a1b2c3d4"})
	if got, _ := podInstanceLabel(t, k.pods(), "grain-granule-780-1", k.podShape(Shape{}), k.podOptions(Shape{})); got != "a1b2c3d4" {
		t.Errorf("pod %s = %q, want a1b2c3d4", k8s.InstanceLabel, got)
	}
	if _, err := k.SweepOrphans(ctx); err != nil {
		t.Fatalf("SweepOrphans: %v", err)
	}
	assertSweepSelects(t, kubectl.takeCalls(t), "a1b2c3d4")

	// A deployment id edited in Settings reaches the next pod and the
	// next sweep.
	var _ SandboxInstanceSetter = k
	k.SetInstance("renamed")
	if got, _ := podInstanceLabel(t, k.pods(), "grain-granule-780-1", k.podShape(Shape{}), k.podOptions(Shape{})); got != "renamed" {
		t.Errorf("pod %s after SetInstance = %q, want renamed", k8s.InstanceLabel, got)
	}
	if _, err := k.SweepOrphans(ctx); err != nil {
		t.Fatalf("SweepOrphans: %v", err)
	}
	assertSweepSelects(t, kubectl.takeCalls(t), "renamed")
}

// waitForCall waits until the fake has been run with a command line
// containing want.
func (f *sweepKubectl) waitForCall(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(filepath.Join(f.dir, "log"))
		if strings.Contains(string(data), want) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("fake kubectl was never run with %q", want)
}

// TestK8sGranuleSweepOrphansSparesBuildingAndLivePods is the periodic
// sweep's safety: a pod an Acquire is still waiting on, and a pod a run
// holds, are both in the namespace's list and neither is an orphan; a
// pod nothing holds is deleted.
func TestK8sGranuleSweepOrphansSparesBuildingAndLivePods(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	kubectl := writeSweepKubectl(t)
	kubectl.setPhase(t, "Pending")
	k := NewK8sGranuleSandboxes(K8sGranuleConfig{
		Kubectl:           kubectl.bin,
		Image:             "example/granule",
		ReadyTimeout:      time.Minute,
		ReadyPollInterval: 5 * time.Millisecond,
	})

	type acquired struct {
		sb  Sandbox
		err error
	}
	done := make(chan acquired, 1)
	go func() {
		sb, err := k.Acquire(ctx, "780-1", Shape{})
		done <- acquired{sb, err}
	}()
	// The pod has been created and Acquire is in waitReady.
	kubectl.waitForCall(t, "get pod grain-granule-780-1")
	kubectl.takeDeleted(t) // create's own delete of a stale pod

	kubectl.setPods(t, "grain-granule-780-1", "grain-granule-c0ffee-1")
	reaped, err := k.SweepOrphans(ctx)
	if err != nil {
		t.Fatalf("SweepOrphans while building: %v", err)
	}
	if got := kubectl.takeDeleted(t); reaped != 1 || !reflect.DeepEqual(got, []string{"grain-granule-c0ffee-1"}) {
		t.Fatalf("SweepOrphans while building = %d, deleted %v; want 1, only the orphan", reaped, got)
	}

	kubectl.setPhase(t, "Running")
	res := <-done
	if res.err != nil {
		t.Fatalf("Acquire: %v", res.err)
	}
	kubectl.setPods(t, "grain-granule-780-1")
	if reaped, err := k.SweepOrphans(ctx); err != nil || reaped != 0 {
		t.Fatalf("SweepOrphans with the pod live = (%d, %v), want (0, nil)", reaped, err)
	}
	if got := kubectl.takeDeleted(t); len(got) != 0 {
		t.Fatalf("SweepOrphans deleted the live pod: %v", got)
	}

	live := k.LiveSandboxes()
	if len(live) != 1 || live[0].ID != "780-1" || live[0].Name != "grain-granule-780-1" || live[0].Backend != "kubernetes-granule" {
		t.Fatalf("LiveSandboxes() = %+v, want the one kubernetes-granule pod grain-granule-780-1", live)
	}
	if err := res.sb.Release(ctx); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if got := k.LiveSandboxes(); len(got) != 0 {
		t.Fatalf("LiveSandboxes() after Release = %+v, want none", got)
	}
}

// TestK8sGranuleSandboxReleaseOutlivesACancelledContext is
// TestK8sSandboxReleaseOutlivesACancelledContext for granule pods.
func TestK8sGranuleSandboxReleaseOutlivesACancelledContext(t *testing.T) {
	t.Parallel()
	kubectl := writeSweepKubectl(t)
	k := NewK8sGranuleSandboxes(K8sGranuleConfig{Kubectl: kubectl.bin, Image: "example/granule"})
	sb := &k8sGranuleSandbox{owner: k, name: "784-1", podName: "grain-granule-784-1"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sb.Release(ctx); err != nil {
		t.Fatalf("Release on a cancelled context: %v", err)
	}
	if got := kubectl.takeDeleted(t); len(got) != 1 || got[0] != "grain-granule-784-1" {
		t.Fatalf("Release on a cancelled context deleted %v, want grain-granule-784-1", got)
	}
}
