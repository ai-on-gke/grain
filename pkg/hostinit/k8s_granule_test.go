package hostinit

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// k8sGranuleTestOptions returns Options for -platform=k8s-granule with
// every default resolved, the same way k8sTestOptions does for
// -platform=k8s-container -- see that function's own doc comment for
// why tests build expected kubectl command lines off these resolved
// fields rather than off literals.
func k8sGranuleTestOptions(t *testing.T) Options {
	t.Helper()
	opts := baseOptions(t)
	opts.Platform = PlatformK8sGranule
	opts.K8sGranule = K8sGranuleOptions{Image: "example.com/granule:latest"}
	return opts.withDefaults()
}

// --- K8sGranuleOptions defaults / validation -------------------------

func TestWithDefaultsFillsK8sGranuleOptions(t *testing.T) {
	t.Parallel()
	opts := k8sGranuleTestOptions(t)
	if opts.K8sGranule.Namespace != DefaultK8sNamespace {
		t.Errorf("Namespace = %q, want %q", opts.K8sGranule.Namespace, DefaultK8sNamespace)
	}
	if opts.K8sGranule.Deployment != DefaultK8sDeployment {
		t.Errorf("Deployment = %q, want %q", opts.K8sGranule.Deployment, DefaultK8sDeployment)
	}
	if opts.K8sGranule.Service != DefaultK8sService {
		t.Errorf("Service = %q, want %q", opts.K8sGranule.Service, DefaultK8sService)
	}
	if want := opts.RepoRoot + "/deploy/kubernetes/grain-granule.yaml"; opts.K8sGranule.Manifest != want {
		t.Errorf("Manifest = %q, want %q", opts.K8sGranule.Manifest, want)
	}
	if opts.K8sGranule.RolloutTimeout != DefaultK8sRolloutTimeout {
		t.Errorf("RolloutTimeout = %s, want %s", opts.K8sGranule.RolloutTimeout, DefaultK8sRolloutTimeout)
	}
	if opts.K8sGranule.PreflightTimeout != DefaultK8sGranulePreflightTimeout {
		t.Errorf("PreflightTimeout = %s, want %s", opts.K8sGranule.PreflightTimeout, DefaultK8sGranulePreflightTimeout)
	}
	if opts.K8sGranulePortForward == nil {
		t.Error("K8sGranulePortForward = nil, want k8sGranulePortForwardViaKubectl's default to be filled in")
	}
}

func TestWithDefaultsLeavesGranulePortForwardNilWhenManagedIsFalse(t *testing.T) {
	t.Parallel()
	opts := baseOptions(t)
	opts.Platform = PlatformK8sGranule
	managed := false
	opts.K8sGranule = K8sGranuleOptions{Image: "example.com/granule:latest", ManagePortForward: &managed}
	opts = opts.withDefaults()
	if opts.K8sGranulePortForward != nil {
		t.Error("K8sGranulePortForward is set despite ManagePortForward=false")
	}
}

// TestValidateAllowsEmptyImageForK8sGranule is validate()'s own side
// of defaulting -k8s-granule-image to defaultSandboxImage: that
// default lives in cmd/grain (the stamped value), not in this
// package, so this package must not refuse an empty K8sGranule.Image
// -- a caller that goes through cmd/grain's flag never reaches
// validate() with one empty in the first place, and one that bypasses
// it should fail pulling the image at the preflight, not here.
func TestValidateAllowsEmptyImageForK8sGranule(t *testing.T) {
	t.Parallel()
	opts := baseOptions(t)
	opts.Platform = PlatformK8sGranule
	if err := opts.validate(); err != nil {
		t.Fatalf("validate() = %v, want nil: K8sGranule.Image is no longer required here", err)
	}
}

// --- checkK8sGranuleClusterReachable ----------------------------------

func TestCheckK8sGranuleClusterReachableGatesOnFailure(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	var r Result
	ok, abort := checkK8sGranuleClusterReachable(K8sGranuleOptions{})(context.Background(), sys, &r)
	if ok || !abort {
		t.Fatalf("got (ok=%v, abort=%v), want (false, true)", ok, abort)
	}
}

func TestCheckK8sGranuleClusterReachablePasses(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	sys.on("Kubernetes control plane is running", nil, "kubectl", "cluster-info")
	var r Result
	ok, abort := checkK8sGranuleClusterReachable(K8sGranuleOptions{})(context.Background(), sys, &r)
	if !ok || abort {
		t.Fatalf("got (ok=%v, abort=%v), want (true, false)", ok, abort)
	}
}

// --- checkK8sGranuleNamespacePodSecurity ------------------------------

// TestCheckK8sGranuleNamespacePodSecurityRefusesMissingNamespace is the
// one place this platform's own check diverges from
// checkK8sNamespacePodSecurity's shape: a namespace that does not exist
// yet is *not* fine here, because this platform's manifest never
// creates one (docs/k8s-granule-design.md §1) -- unlike
// PlatformK8sContainer's grain.yaml, which creates its Namespace with
// the right label as part of apply.
func TestCheckK8sGranuleNamespacePodSecurityRefusesMissingNamespace(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	g := K8sGranuleOptions{Namespace: "grain"}
	// Nothing configured for "get namespace grain ..." -> errNotFound.
	var r Result
	ok, abort := checkK8sGranuleNamespacePodSecurity(g)(context.Background(), sys, &r)
	if ok || !abort {
		t.Fatalf("got (ok=%v, abort=%v), want (false, true): a missing namespace is this platform's own prerequisite, not yet met", ok, abort)
	}
	s, _ := stepStatus(r, "namespace pod-security level")
	if s.Status != StepManual || !strings.Contains(s.Command, "kubectl create namespace") {
		t.Fatalf("step = %+v, want StepManual with a create-namespace command", s)
	}
}

func TestCheckK8sGranuleNamespacePodSecurityRefusesBaseline(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	g := K8sGranuleOptions{Namespace: "grain"}
	sys.on("baseline", nil, "kubectl", "get", "namespace", "grain", "-o", `jsonpath={.metadata.labels.pod-security\.kubernetes\.io/enforce}`)
	var r Result
	ok, abort := checkK8sGranuleNamespacePodSecurity(g)(context.Background(), sys, &r)
	if ok || !abort {
		t.Fatalf("got (ok=%v, abort=%v), want (false, true): a baseline namespace cannot run this platform's privileged pods", ok, abort)
	}
	s, _ := stepStatus(r, "namespace pod-security level")
	if !strings.Contains(s.Command, "privileged") {
		t.Errorf("command = %q, want the exact relabel command", s.Command)
	}
}

// TestCheckK8sGranuleNamespacePodSecurityPassesWhenPrivileged is the
// property this platform exists to require: the exact label
// PlatformK8sContainer's own check refuses is this one's prerequisite.
func TestCheckK8sGranuleNamespacePodSecurityPassesWhenPrivileged(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	g := K8sGranuleOptions{Namespace: "grain"}
	sys.on("privileged", nil, "kubectl", "get", "namespace", "grain", "-o", `jsonpath={.metadata.labels.pod-security\.kubernetes\.io/enforce}`)
	var r Result
	ok, abort := checkK8sGranuleNamespacePodSecurity(g)(context.Background(), sys, &r)
	if !ok || abort {
		t.Fatalf("got (ok=%v, abort=%v), want (true, false) for a privileged namespace: %+v", ok, abort, r.Steps)
	}
}

// --- checkK8sGranulePermissions ---------------------------------------

func allowAllK8sGranulePerms(sys *fakeSystem, g K8sGranuleOptions) {
	for _, p := range k8sGranuleRequiredPerms {
		args := []string{"auth", "can-i", "create", p.resource}
		if p.namespaced {
			args = append(args, "-n", g.Namespace)
		}
		sys.on("yes", nil, "kubectl", args...)
	}
}

func TestCheckK8sGranulePermissionsNeverChecksNamespaceCreate(t *testing.T) {
	t.Parallel()
	for _, p := range k8sGranuleRequiredPerms {
		if p.resource == "namespaces" {
			t.Fatal("k8sGranuleRequiredPerms still names \"namespaces\" -- this platform's manifest never creates one")
		}
	}
}

func TestCheckK8sGranulePermissionsPassesWhenEverythingIsAllowed(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	g := K8sGranuleOptions{Namespace: "grain"}
	allowAllK8sGranulePerms(sys, g)
	var r Result
	ok, _ := checkK8sGranulePermissions(g)(context.Background(), sys, &r)
	if !ok {
		t.Fatalf("ok = false, want true: %+v", r.Steps)
	}
}

func TestCheckK8sGranulePermissionsReportsMissingOnes(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	g := K8sGranuleOptions{Namespace: "grain"}
	allowAllK8sGranulePerms(sys, g)
	sys.on("no", nil, "kubectl", "auth", "can-i", "create", "deployments.apps", "-n", "grain")
	var r Result
	ok, _ := checkK8sGranulePermissions(g)(context.Background(), sys, &r)
	if ok {
		t.Fatal("ok = true, want false")
	}
	s, _ := stepStatus(r, "permission to create what grain-granule.yaml creates")
	if s.Status != StepManual || !strings.Contains(s.Detail, "deployments.apps") {
		t.Fatalf("step = %+v, want StepManual naming deployments.apps", s)
	}
}

// --- checkK8sGranuleNestedVirt -----------------------------------------

func TestCheckK8sGranuleNestedVirtPassesWhenThePodBoots(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	sys := newFakeSystem()
	g := K8sGranuleOptions{Namespace: "grain", Image: "example.com/granule:latest"}
	path := filepath.Join(dataDir, "k8s-preflight", "granule-nested-virt-check.yaml")
	sys.on("", nil, "kubectl", "delete", "-f", path, "--ignore-not-found", "--grace-period=1", "--timeout=1s")
	sys.on("pod/grain-init-preflight-granule created", nil, "kubectl", "apply", "-f", path)
	sys.on("condition met", nil, "kubectl", "wait", "pod/"+k8sGranulePreflightPod, "-n", "grain",
		"--for=condition=Ready", "--timeout=1s")

	var r Result
	ok, _ := checkK8sGranuleNestedVirt(g, dataDir, time.Second)(context.Background(), sys, &r)
	if !ok {
		t.Fatalf("ok = false, want true: %+v", r.Steps)
	}
	if !sys.calledAny("kubectl delete -f " + path + " --ignore-not-found --grace-period=1 --timeout=1s") {
		t.Error("the throwaway pod's manifest was never cleaned up")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the scratch manifest: %v", err)
	}
	content := string(raw)
	if !strings.Contains(content, "privileged: true") || !strings.Contains(content, "/dev/kvm") {
		t.Errorf("manifest = %q, want privileged: true and /dev/kvm", content)
	}
	if !strings.Contains(content, `value: "none"`) {
		t.Errorf("manifest = %q, want CHV_NET_MODE: none", content)
	}
}

func TestCheckK8sGranuleNestedVirtReportsTimeoutAsManual(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	sys := newFakeSystem()
	g := K8sGranuleOptions{Namespace: "grain", Image: "example.com/granule:latest"}
	path := filepath.Join(dataDir, "k8s-preflight", "granule-nested-virt-check.yaml")
	sys.on("", nil, "kubectl", "delete", "-f", path, "--ignore-not-found", "--grace-period=1", "--timeout=1s")
	sys.on("pod/grain-init-preflight-granule created", nil, "kubectl", "apply", "-f", path)
	sys.on("", errNotFound, "kubectl", "wait", "pod/"+k8sGranulePreflightPod, "-n", "grain",
		"--for=condition=Ready", "--timeout=1s")
	sys.on("Terminated", nil, "kubectl", "get", "pod", k8sGranulePreflightPod, "-n", "grain",
		"-o", "jsonpath={.status.phase}")
	sys.on(`{"terminated":{"exitCode":1,"reason":"Error"}}`, nil, "kubectl", "get", "pod", k8sGranulePreflightPod, "-n", "grain",
		"-o", "jsonpath={.status.containerStatuses[0].state}")
	sys.on("0s  Warning  BackOff  pod/grain-init-preflight-granule  Back-off restarting failed container", nil,
		"kubectl", "get", "event", "-n", "grain", "--field-selector", "involvedObject.name="+k8sGranulePreflightPod,
		"--sort-by=.lastTimestamp")
	sys.on("granule: disk[0] /var/lib/granule/guest/disk.img: no such file or directory", nil,
		"kubectl", "logs", k8sGranulePreflightPod, "-n", "grain", "--tail=20")

	var r Result
	ok, _ := checkK8sGranuleNestedVirt(g, dataDir, time.Second)(context.Background(), sys, &r)
	if ok {
		t.Fatal("ok = true, want false")
	}
	s, _ := stepStatus(r, "nested virtualization (scheduled boot)")
	if s.Status != StepManual || !strings.Contains(s.Detail, "nested virtualization") {
		t.Fatalf("step = %+v, want StepManual naming nested virtualization", s)
	}
	if s.Command != "" {
		t.Errorf("Command = %q, want empty: the throwaway pod is already deleted by the time this is read", s.Command)
	}
	for _, want := range []string{
		"pod phase: Terminated", `"exitCode":1`, "BackOff", "no such file or directory",
		"-k8s-granule-image", "not a packed guest",
	} {
		if !strings.Contains(s.Detail, want) {
			t.Errorf("Detail = %q, want it to contain %q (captured before the pod was deleted)", s.Detail, want)
		}
	}
	if strings.Contains(s.Detail, "nested virtualization is not enabled") {
		t.Errorf("Detail = %q, want the wrong-image explanation, not the nested-virt one, for a terminated container", s.Detail)
	}
	if strings.Contains(s.Detail, "within 1s ()") {
		t.Errorf("Detail = %q, want no empty parenthetical", s.Detail)
	}
}

// TestCheckK8sGranuleNestedVirtRefusesANodeWithNoKVMDevice is the 10-05
// kind run's G1: with no /dev/kvm on the node, the untyped hostPath had
// the kubelet create a directory there, cloud-hypervisor exited with "Is
// a directory", and the step blamed -k8s-granule-image. Typed CharDevice,
// the kubelet refuses the mount, and the step refuses naming /dev/kvm.
func TestCheckK8sGranuleNestedVirtRefusesANodeWithNoKVMDevice(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	sys := newFakeSystem()
	g := K8sGranuleOptions{Namespace: "grain", Image: "example.com/guest:latest"}
	path := filepath.Join(dataDir, "k8s-preflight", "granule-nested-virt-check.yaml")
	sys.on("", nil, "kubectl", "delete", "-f", path, "--ignore-not-found", "--grace-period=1", "--timeout=1s")
	sys.on("pod/grain-init-preflight-granule created", nil, "kubectl", "apply", "-f", path)
	sys.on("", errNotFound, "kubectl", "wait", "pod/"+k8sGranulePreflightPod, "-n", "grain",
		"--for=condition=Ready", "--timeout=1s")
	sys.on("Pending", nil, "kubectl", "get", "pod", k8sGranulePreflightPod, "-n", "grain",
		"-o", "jsonpath={.status.phase}")
	sys.on(`{"waiting":{"reason":"ContainerCreating"}}`, nil, "kubectl", "get", "pod", k8sGranulePreflightPod, "-n", "grain",
		"-o", "jsonpath={.status.containerStatuses[0].state}")
	sys.on(`1s  Warning  FailedMount  pod/grain-init-preflight-granule  MountVolume.SetUp failed for volume "dev-kvm" : hostPath type check failed: /dev/kvm is not a character device`, nil,
		"kubectl", "get", "event", "-n", "grain", "--field-selector", "involvedObject.name="+k8sGranulePreflightPod,
		"--sort-by=.lastTimestamp")
	sys.on("", nil, "kubectl", "logs", k8sGranulePreflightPod, "-n", "grain", "--tail=20")

	var r Result
	ok, _ := checkK8sGranuleNestedVirt(g, dataDir, time.Second)(context.Background(), sys, &r)
	if ok {
		t.Fatal("ok = true, want false")
	}
	s, _ := stepStatus(r, "nested virtualization (scheduled boot)")
	if s.Status != StepRefused || !strings.Contains(s.Detail, "no /dev/kvm character device") {
		t.Fatalf("step = %+v, want StepRefused naming the missing /dev/kvm", s)
	}
	if strings.Contains(s.Detail, "not a packed guest") {
		t.Errorf("Detail = %q blames the image for a node with no /dev/kvm", s.Detail)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the scratch manifest: %v", err)
	}
	if !strings.Contains(string(raw), "path: /dev/kvm\n        type: CharDevice\n") {
		t.Errorf("manifest's /dev/kvm hostPath is not type CharDevice:\n%s", raw)
	}
}

// TestCheckK8sGranuleNestedVirtReportsNestedVirtWhenStillRunning is
// this test's counterpart for the genuine nested-virt failure mode: a
// correct, packed guest image whose container is still "running" (it
// started; cloud-hypervisor just never answered the readinessProbe),
// which must get the nested-virt explanation, not the wrong-image one
// TestCheckK8sGranuleNestedVirtReportsTimeoutAsManual exercises.
func TestCheckK8sGranuleNestedVirtReportsNestedVirtWhenStillRunning(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	sys := newFakeSystem()
	g := K8sGranuleOptions{Namespace: "grain", Image: "example.com/granule:latest"}
	path := filepath.Join(dataDir, "k8s-preflight", "granule-nested-virt-check.yaml")
	sys.on("", nil, "kubectl", "delete", "-f", path, "--ignore-not-found", "--grace-period=1", "--timeout=1s")
	sys.on("pod/grain-init-preflight-granule created", nil, "kubectl", "apply", "-f", path)
	sys.on("", errNotFound, "kubectl", "wait", "pod/"+k8sGranulePreflightPod, "-n", "grain",
		"--for=condition=Ready", "--timeout=1s")
	sys.on("Running", nil, "kubectl", "get", "pod", k8sGranulePreflightPod, "-n", "grain",
		"-o", "jsonpath={.status.phase}")
	sys.on(`{"running":{"startedAt":"2026-10-04T00:00:00Z"}}`, nil, "kubectl", "get", "pod", k8sGranulePreflightPod, "-n", "grain",
		"-o", "jsonpath={.status.containerStatuses[0].state}")
	sys.on("", nil, "kubectl", "get", "event", "-n", "grain", "--field-selector", "involvedObject.name="+k8sGranulePreflightPod,
		"--sort-by=.lastTimestamp")
	sys.on("", nil, "kubectl", "logs", k8sGranulePreflightPod, "-n", "grain", "--tail=20")

	var r Result
	ok, _ := checkK8sGranuleNestedVirt(g, dataDir, time.Second)(context.Background(), sys, &r)
	if ok {
		t.Fatal("ok = true, want false")
	}
	s, _ := stepStatus(r, "nested virtualization (scheduled boot)")
	if !strings.Contains(s.Detail, "nested virtualization is not enabled") {
		t.Errorf("Detail = %q, want the nested-virt explanation for a still-running container", s.Detail)
	}
	if strings.Contains(s.Detail, "-k8s-granule-image") {
		t.Errorf("Detail = %q, want no wrong-image hint for a still-running container", s.Detail)
	}
}

// TestCheckK8sGranuleNestedVirtDiagnosisToleratesMissingPieces confirms
// the diagnosis is still useful -- and the check still reports, rather
// than panicking or erroring out -- against a cluster that cannot
// answer one or more of the diagnostic kubectl calls (an old
// server-side kubectl, RBAC that allows "wait" but not "get events",
// or a container that was never created to have logs at all).
func TestCheckK8sGranuleNestedVirtDiagnosisToleratesMissingPieces(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	sys := newFakeSystem()
	g := K8sGranuleOptions{Namespace: "grain", Image: "example.com/granule:latest"}
	path := filepath.Join(dataDir, "k8s-preflight", "granule-nested-virt-check.yaml")
	sys.on("", nil, "kubectl", "delete", "-f", path, "--ignore-not-found", "--grace-period=1", "--timeout=1s")
	sys.on("pod/grain-init-preflight-granule created", nil, "kubectl", "apply", "-f", path)
	sys.on("", errNotFound, "kubectl", "wait", "pod/"+k8sGranulePreflightPod, "-n", "grain",
		"--for=condition=Ready", "--timeout=1s")
	// Every diagnostic call left unconfigured -- fakeSystem fails all of
	// them with errNotFound.

	var r Result
	ok, _ := checkK8sGranuleNestedVirt(g, dataDir, time.Second)(context.Background(), sys, &r)
	if ok {
		t.Fatal("ok = true, want false")
	}
	s, _ := stepStatus(r, "nested virtualization (scheduled boot)")
	for _, want := range []string{"pod phase: (none)", "container state: (none)", "recent events:\n(none)", "recent log output:\n(none)"} {
		if !strings.Contains(s.Detail, want) {
			t.Errorf("Detail = %q, want it to contain %q", s.Detail, want)
		}
	}
}

func TestK8sGranuleNestedVirtManifestCarriesTheNodeSelector(t *testing.T) {
	t.Parallel()
	manifest := k8sGranuleNestedVirtManifest("grain", "example.com/granule", map[string]string{"pool": "nested-virt"})
	if !strings.Contains(manifest, "nodeSelector:") || !strings.Contains(manifest, `pool: "nested-virt"`) {
		t.Errorf("manifest = %q, want a nodeSelector naming pool=nested-virt", manifest)
	}
}

func TestK8sGranuleNestedVirtManifestOmitsNodeSelectorWhenEmpty(t *testing.T) {
	t.Parallel()
	manifest := k8sGranuleNestedVirtManifest("grain", "example.com/granule", nil)
	if strings.Contains(manifest, "nodeSelector:") {
		t.Errorf("manifest = %q, want no nodeSelector block with none given", manifest)
	}
}

// --- checkK8sGranuleUid1000Migration -----------------------------------

func TestCheckK8sGranuleUid1000MigrationSkipsWhenDeploymentIsAbsent(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	g := K8sGranuleOptions{Namespace: "grain", Deployment: "grain"}
	var r Result
	ok, abort := checkK8sGranuleUid1000Migration(g)(context.Background(), sys, &r)
	if !ok || abort {
		t.Fatalf("got (ok=%v, abort=%v), want (true, false)", ok, abort)
	}
}

func TestCheckK8sGranuleUid1000MigrationReportsManualAndAborts(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	g := K8sGranuleOptions{Namespace: "grain", Deployment: "grain"}
	sys.on("", nil, "kubectl", "get", "deployment", "grain", "-n", "grain",
		"-o", "jsonpath={.spec.template.spec.securityContext.runAsUser}")
	var r Result
	ok, abort := checkK8sGranuleUid1000Migration(g)(context.Background(), sys, &r)
	if ok || !abort {
		t.Fatalf("got (ok=%v, abort=%v), want (false, true)", ok, abort)
	}
	s, _ := stepStatus(r, "uid-1000 migration")
	if s.Status != StepManual || !strings.Contains(s.Command, "chown") {
		t.Fatalf("step = %+v, want StepManual with the chown Job as Command", s)
	}
}

// --- ensureK8sGranuleManifest / ensureK8sGranuleRollout ----------------

func TestEnsureK8sGranuleManifestSkipsWhenDeploymentAlreadyExists(t *testing.T) {
	t.Parallel()
	opts := k8sGranuleTestOptions(t)
	sys := newFakeSystem()
	sys.on("deployment.apps/grain", nil, "kubectl", "get", "deployment", opts.K8sGranule.Deployment, "-n", opts.K8sGranule.Namespace)
	var r Result
	if !ensureK8sGranuleManifest(context.Background(), sys, opts, &r) {
		t.Fatalf("ensureK8sGranuleManifest = false, want true: %+v", r.Steps)
	}
	s, _ := stepStatus(r, "apply the grain-granule manifest")
	if s.Status != StepSkipped {
		t.Fatalf("status = %s, want %s", s.Status, StepSkipped)
	}
	if sys.calledAny("kubectl apply -f " + opts.K8sGranule.Manifest) {
		t.Error("ensureK8sGranuleManifest re-applied over an existing deployment")
	}
}

func TestEnsureK8sGranuleManifestAppliesWhenAbsent(t *testing.T) {
	t.Parallel()
	opts := k8sGranuleTestOptions(t)
	sys := newFakeSystem()
	sys.on("deployment.apps/grain created", nil, "kubectl", "apply", "-f", opts.K8sGranule.Manifest)
	var r Result
	if !ensureK8sGranuleManifest(context.Background(), sys, opts, &r) {
		t.Fatalf("ensureK8sGranuleManifest = false, want true: %+v", r.Steps)
	}
	s, _ := stepStatus(r, "apply the grain-granule manifest")
	if s.Status != StepDone {
		t.Fatalf("status = %s, want %s", s.Status, StepDone)
	}
}

func TestEnsureK8sGranuleManifestDryRunChangesNothing(t *testing.T) {
	t.Parallel()
	opts := k8sGranuleTestOptions(t)
	opts.DryRun = true
	sys := newFakeSystem()
	var r Result
	if ensureK8sGranuleManifest(context.Background(), sys, opts, &r) {
		t.Fatal("ensureK8sGranuleManifest = true on a dry run")
	}
	s, _ := stepStatus(r, "apply the grain-granule manifest")
	if s.Status != StepWould {
		t.Fatalf("status = %s, want %s", s.Status, StepWould)
	}
	if len(sys.calls) != 0 {
		t.Errorf("dry run made kubectl calls: %v", sys.calls)
	}
}

func TestEnsureK8sGranuleRollout(t *testing.T) {
	t.Parallel()
	opts := k8sGranuleTestOptions(t)
	sys := newFakeSystem()
	sys.on("deployment \"grain\" successfully rolled out", nil, "kubectl", "rollout", "status",
		"deployment/"+opts.K8sGranule.Deployment, "-n", opts.K8sGranule.Namespace,
		"--timeout="+opts.K8sGranule.RolloutTimeout.String())
	var r Result
	if !ensureK8sGranuleRollout(context.Background(), sys, opts, &r) {
		t.Fatalf("ensureK8sGranuleRollout = false, want true: %+v", r.Steps)
	}
}

// --- establishK8sGranuleReachability ------------------------------------

func TestEstablishK8sGranuleReachabilityRunsThePortForward(t *testing.T) {
	t.Parallel()
	opts := k8sGranuleTestOptions(t)
	called := false
	opts.K8sGranulePortForward = func(ctx context.Context, o Options) (func(), error) {
		called = true
		return noStop()
	}
	var r Result
	stop, ok := establishK8sGranuleReachability(context.Background(), opts, &r)
	if !ok || stop == nil {
		t.Fatalf("got (stop==nil=%v, ok=%v), want a non-nil stop func and true", stop == nil, ok)
	}
	if !called {
		t.Error("the configured K8sGranulePortForward was never called")
	}
}

func TestEstablishK8sGranuleReachabilitySkipsWhenNotManaged(t *testing.T) {
	t.Parallel()
	opts := k8sGranuleTestOptions(t)
	opts.K8sGranulePortForward = nil
	var r Result
	stop, ok := establishK8sGranuleReachability(context.Background(), opts, &r)
	if stop != nil || !ok {
		t.Fatalf("got (stop==nil=%v, ok=%v), want (true, true)", stop == nil, ok)
	}
}

// --- end to end ---------------------------------------------------------

func stubK8sGranulePreflight(t *testing.T, sys *fakeSystem, opts Options) {
	t.Helper()
	g := opts.K8sGranule
	sys.on("ok", nil, "kubectl", "cluster-info")
	sys.on("privileged", nil, "kubectl", "get", "namespace", g.Namespace, "-o", `jsonpath={.metadata.labels.pod-security\.kubernetes\.io/enforce}`)
	allowAllK8sGranulePerms(sys, g)
	path := filepath.Join(opts.DataDir, "k8s-preflight", "granule-nested-virt-check.yaml")
	sys.on("", nil, "kubectl", "delete", "-f", path, "--ignore-not-found", "--grace-period=1", "--timeout="+g.PreflightTimeout.String())
	sys.on("created", nil, "kubectl", "apply", "-f", path)
	sys.on("ready", nil, "kubectl", "wait", "pod/"+k8sGranulePreflightPod, "-n", g.Namespace,
		"--for=condition=Ready", "--timeout="+g.PreflightTimeout.String())
	// checkK8sGranuleUid1000Migration's own "get deployment" is
	// deliberately left unconfigured here (as if the deployment does
	// not exist yet, errNotFound).
}

func TestInitK8sGranuleEndToEnd(t *testing.T) {
	t.Parallel()
	opts := k8sGranuleTestOptions(t)
	opts.GitHubRepos = []string{"acme/widgets"}
	opts.GitHubCredentialName = ""
	stopped := false
	opts.K8sGranulePortForward = func(ctx context.Context, o Options) (func(), error) {
		return func() { stopped = true }, nil
	}

	sys := newFakeSystem()
	stubK8sGranulePreflight(t, sys, opts)
	sys.on("applied", nil, "kubectl", "apply", "-f", opts.K8sGranule.Manifest)
	sys.on("rolled out", nil, "kubectl", "rollout", "status", "deployment/"+opts.K8sGranule.Deployment,
		"-n", opts.K8sGranule.Namespace, "--timeout="+opts.K8sGranule.RolloutTimeout.String())

	sys.onHTTPRequest("GET", "http://"+opts.UIAddr+"/api/config", 200, []byte(`{}`), nil)
	sys.onHTTPRequest("GET", "http://"+opts.UIAddr+"/api/deployment", 200, []byte(`{"maxWorkers":0,"targetRepos":[]}`), nil)
	sys.onHTTPRequest("PUT", "http://"+opts.UIAddr+"/api/deployment", 200, nil, nil)
	sys.onHTTPRequest("GET", "http://"+opts.UIAddr+"/api/settings", 200, []byte(roomySettingsJSON), nil)

	result, err := Init(context.Background(), sys, opts)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if result.HasManual() || result.HasRefused() {
		t.Fatalf("Steps = %+v, want every step done or skipped", result.Steps)
	}
	for _, name := range []string{"apply the grain-granule manifest", "wait for the rollout", "reach the daemon's API"} {
		s, found := stepStatus(result, name)
		if !found {
			t.Fatalf("no %q step in %+v", name, result.Steps)
		}
		if s.Status != StepDone {
			t.Errorf("%s: status = %s, want %s", name, s.Status, StepDone)
		}
	}
	if !stopped {
		t.Error("Init returned without stopping the port-forward it started")
	}
	for _, name := range []string{"git", "docker", "go toolchain", "build the granule CLI", "install the claude CLI"} {
		if _, found := stepStatus(result, name); found {
			t.Errorf("Steps included %q, which is a host-build step this platform has no use for", name)
		}
	}
}

func TestInitK8sGranuleStopsAtPreflightWithoutApplying(t *testing.T) {
	t.Parallel()
	opts := k8sGranuleTestOptions(t)
	sys := newFakeSystem()
	// Cluster unreachable -- nothing after the gate may run.
	result, err := Init(context.Background(), sys, opts)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if len(result.Steps) != 1 || result.Steps[0].Status != StepRefused {
		t.Fatalf("Steps = %+v, want exactly one StepRefused (the cluster-reachable gate)", result.Steps)
	}
	// cluster-info, then the current-context probe that turns "no
	// kubeconfig at all" into a one-line refusal.
	if len(sys.calls) != 2 {
		t.Errorf("Init ran calls beyond the gate check: %v", sys.calls)
	}
}

// TestInitK8sGranuleRefusesABaselineNamespaceWithoutProbingFurther
// confirms the abort propagates through the whole list: a cluster
// still configured for -platform=k8s-container (or simply never
// labelled) must not have its RBAC or nested-virt checked at all, the
// same way a cluster PlatformK8sContainer refuses never reaches that
// platform's own storage or NetworkPolicy checks.
func TestInitK8sGranuleRefusesABaselineNamespaceWithoutProbingFurther(t *testing.T) {
	t.Parallel()
	opts := k8sGranuleTestOptions(t)
	sys := newFakeSystem()
	sys.on("ok", nil, "kubectl", "cluster-info")
	sys.on("baseline", nil, "kubectl", "get", "namespace", opts.K8sGranule.Namespace, "-o",
		`jsonpath={.metadata.labels.pod-security\.kubernetes\.io/enforce}`)

	result, err := Init(context.Background(), sys, opts)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if len(result.Steps) != 2 {
		t.Fatalf("Steps = %+v, want exactly two (the gate, then the refusal)", result.Steps)
	}
	s, _ := stepStatus(result, "namespace pod-security level")
	if s.Status != StepManual {
		t.Fatalf("status = %s, want %s", s.Status, StepManual)
	}
	for _, call := range sys.calls {
		if strings.Contains(call, "auth can-i") || strings.Contains(call, "granule-nested-virt-check") {
			t.Errorf("a later check ran despite the namespace gate failing: %v", sys.calls)
		}
	}
}
