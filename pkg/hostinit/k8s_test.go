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

package hostinit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// k8sTestOptions returns Options for -platform=k8s-container with every
// default resolved (K8s.Namespace, K8s.Manifest, K8s.PreflightTimeout,
// ...) against a fresh DataDir/RepoRoot, the same way cmdInit's own
// opts.withDefaults() call would before handing them to Init. Tests
// build expected kubectl command lines off these resolved fields
// (opts.K8s.PreflightTimeout.String(), opts.K8s.Manifest, ...) rather
// than duplicating the literal values, so a change to a default here
// cannot silently desync a test's expectation from production code.
func k8sTestOptions(t *testing.T) Options {
	t.Helper()
	opts := baseOptions(t)
	opts.Platform = PlatformK8sContainer
	opts.K8s = K8sOptions{SandboxImage: "example.com/k8s-sandbox:latest"}
	return opts.withDefaults()
}

func noStop() (func(), error) { return func() {}, nil }

// --- K8sOptions defaults / validation --------------------------------

func TestWithDefaultsFillsK8sOptions(t *testing.T) {
	t.Parallel()
	opts := k8sTestOptions(t)
	if opts.K8s.Namespace != DefaultK8sNamespace {
		t.Errorf("Namespace = %q, want %q", opts.K8s.Namespace, DefaultK8sNamespace)
	}
	if opts.K8s.Deployment != DefaultK8sDeployment {
		t.Errorf("Deployment = %q, want %q", opts.K8s.Deployment, DefaultK8sDeployment)
	}
	if opts.K8s.Service != DefaultK8sService {
		t.Errorf("Service = %q, want %q", opts.K8s.Service, DefaultK8sService)
	}
	if want := opts.RepoRoot + "/deploy/kubernetes/grain.yaml"; opts.K8s.Manifest != want {
		t.Errorf("Manifest = %q, want %q", opts.K8s.Manifest, want)
	}
	if want := opts.RepoRoot + "/deploy/kubernetes/networkpolicy.yaml"; opts.K8s.NetworkPolicyManifest != want {
		t.Errorf("NetworkPolicyManifest = %q, want %q", opts.K8s.NetworkPolicyManifest, want)
	}
	if opts.K8s.RolloutTimeout != DefaultK8sRolloutTimeout {
		t.Errorf("RolloutTimeout = %s, want %s", opts.K8s.RolloutTimeout, DefaultK8sRolloutTimeout)
	}
	if opts.K8s.PreflightTimeout != DefaultK8sPreflightTimeout {
		t.Errorf("PreflightTimeout = %s, want %s", opts.K8s.PreflightTimeout, DefaultK8sPreflightTimeout)
	}
	if opts.K8sPortForward == nil {
		t.Error("K8sPortForward = nil, want k8sPortForwardViaKubectl's default to be filled in")
	}
}

// TestWithDefaultsLeavesPortForwardNilWhenManagedIsFalse confirms the
// escape hatch for an operator who already has UIAddr reachable some
// other way: ManagePortForward=false must mean grain init never starts
// a kubectl port-forward of its own.
func TestWithDefaultsLeavesPortForwardNilWhenManagedIsFalse(t *testing.T) {
	t.Parallel()
	opts := baseOptions(t)
	opts.Platform = PlatformK8sContainer
	managed := false
	opts.K8s = K8sOptions{SandboxImage: "example.com/k8s-sandbox:latest", ManagePortForward: &managed}
	opts = opts.withDefaults()
	if opts.K8sPortForward != nil {
		t.Error("K8sPortForward is set despite ManagePortForward=false")
	}
}

// TestWithDefaultsNeverOverridesAnExplicitPortForward confirms a
// caller's own K8sPortForward (a test's fake, or an operator's own
// reachability strategy) is never clobbered by the default.
func TestWithDefaultsNeverOverridesAnExplicitPortForward(t *testing.T) {
	t.Parallel()
	opts := baseOptions(t)
	opts.Platform = PlatformK8sContainer
	opts.K8s = K8sOptions{SandboxImage: "example.com/k8s-sandbox:latest"}
	called := false
	opts.K8sPortForward = func(ctx context.Context, o Options) (func(), error) {
		called = true
		return noStop()
	}
	opts = opts.withDefaults()
	if opts.K8sPortForward == nil {
		t.Fatal("K8sPortForward was cleared by withDefaults")
	}
	if _, err := opts.K8sPortForward(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("withDefaults replaced the caller's own K8sPortForward")
	}
}

func TestValidateRequiresSandboxImageForK8sContainer(t *testing.T) {
	t.Parallel()
	opts := baseOptions(t)
	opts.Platform = PlatformK8sContainer
	if err := opts.withDefaults().validate(); err == nil {
		t.Fatal("expected an error with no K8s.SandboxImage set")
	}
	opts.K8s.SandboxImage = "example.com/k8s-sandbox:latest"
	if err := opts.withDefaults().validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

// --- checkK8sClusterReachable -----------------------------------------

func TestCheckK8sClusterReachableGatesOnFailure(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	// "kubectl cluster-info" unconfigured -> errNotFound, the same
	// shape a missing kubectl or an unreachable API server produces.
	var r Result
	ok, abort := checkK8sClusterReachable(K8sOptions{})(context.Background(), sys, &r)
	if ok || !abort {
		t.Fatalf("got (ok=%v, abort=%v), want (false, true)", ok, abort)
	}
	s, found := stepStatus(r, "kubectl cluster access")
	if !found || s.Status != StepRefused {
		t.Fatalf("step = %+v, want StepRefused", s)
	}
}

func TestCheckK8sClusterReachablePasses(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	sys.on("Kubernetes control plane is running", nil, "kubectl", "cluster-info")
	var r Result
	ok, abort := checkK8sClusterReachable(K8sOptions{})(context.Background(), sys, &r)
	if !ok || abort {
		t.Fatalf("got (ok=%v, abort=%v), want (true, false)", ok, abort)
	}
}

// With no kubeconfig at all, kubectl falls back to localhost:8080 and
// cluster-info's error carries five repeated memcache.go lines; the
// refusal should be the one line current-context gives instead.
func TestCheckK8sClusterReachableRefusesInOneLineWithNoKubeconfig(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	noise := strings.Repeat("E1004 memcache.go:265] couldn't get current server API group list: "+
		"Get \"http://localhost:8080/api\": dial tcp [::1]:8080: connect: connection refused\n", 5)
	sys.on(noise, fmt.Errorf("exit status 1: %s", noise), "kubectl", "cluster-info")
	sys.on("error: current-context is not set", fmt.Errorf("exit status 1"), "kubectl", "config", "current-context")
	var r Result
	ok, abort := checkK8sClusterReachable(K8sOptions{})(context.Background(), sys, &r)
	if ok || !abort {
		t.Fatalf("got (ok=%v, abort=%v), want (false, true)", ok, abort)
	}
	s, _ := stepStatus(r, "kubectl cluster access")
	if s.Status != StepRefused || !strings.Contains(s.Detail, "current-context is not set") {
		t.Fatalf("step = %+v, want StepRefused quoting current-context", s)
	}
	if strings.Contains(s.Detail, "memcache") {
		t.Errorf("Detail = %q, still carries cluster-info's discovery noise", s.Detail)
	}
}

// A named -k8s-context makes kubectl's own current context irrelevant,
// so cluster-info's own error is the one to report.
func TestCheckK8sClusterReachableWithANamedContextReportsClusterInfo(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	sys.on("", fmt.Errorf(`error: context "nope" does not exist`), "kubectl", "--context=nope", "cluster-info")
	var r Result
	ok, _ := checkK8sClusterReachable(K8sOptions{Context: "nope"})(context.Background(), sys, &r)
	if ok {
		t.Fatal("ok = true, want false")
	}
	s, _ := stepStatus(r, "kubectl cluster access")
	if !strings.Contains(s.Detail, `context "nope" does not exist`) {
		t.Fatalf("step = %+v, want cluster-info's own error", s)
	}
	if sys.calledAny("kubectl --context=nope config current-context") {
		t.Error("asked for the current context although -k8s-context named one")
	}
}

// --- kubectlCommand ------------------------------------------------------

// Every printed command has to target the cluster the check ran
// against, not kubectl's current context.
func TestKubectlCommandCarriesContextAndKubeconfig(t *testing.T) {
	t.Parallel()
	k := K8sOptions{Context: "kind-other", Kubeconfig: "/home/op/my configs/kube"}
	got := kubectlCommand(k, "-n", "default", "describe", "pod", k8sPreflightImagePod)
	want := `kubectl '--kubeconfig=/home/op/my configs/kube' --context=kind-other -n default describe pod ` + k8sPreflightImagePod
	if got != want {
		t.Fatalf("kubectlCommand = %q, want %q", got, want)
	}
	if got := kubectlCommand(K8sOptions{}, "get", "pods"); got != "kubectl get pods" {
		t.Errorf("kubectlCommand with no context = %q, want plain kubectl", got)
	}
}

// --- checkK8sSandboxImageMatchesManifest -------------------------------

func writeTestManifest(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "grain.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// The shipped manifest is what an operator applies by default, so the
// reader has to find its flag -- not just a fixture's.
func TestManifestK8sSandboxImageReadsTheShippedManifest(t *testing.T) {
	t.Parallel()
	image, err := ManifestK8sSandboxImage(filepath.Join("..", "..", "deploy", "kubernetes", "grain.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(image, "k8s-sandbox") {
		t.Fatalf("ManifestK8sSandboxImage(grain.yaml) = %q, want the k8s-sandbox image it runs", image)
	}
}

func TestManifestK8sSandboxImageSkipsComments(t *testing.T) {
	t.Parallel()
	path := writeTestManifest(t, "args:\n  # - -k8s-sandbox-image=commented/out:1\n  - \"--k8s-sandbox-image=example.com/sb:main\"\n")
	image, err := ManifestK8sSandboxImage(path)
	if err != nil || image != "example.com/sb:main" {
		t.Fatalf("got (%q, %v), want example.com/sb:main", image, err)
	}
}

func TestCheckK8sSandboxImageMatchesManifestRefusesADifferentTag(t *testing.T) {
	t.Parallel()
	k := K8sOptions{
		SandboxImage: "ghcr.io/bwsalmon/grain/k8s-sandbox:latest",
		Manifest:     writeTestManifest(t, "        - -k8s-sandbox-image=ghcr.io/bwsalmon/grain/k8s-sandbox:main\n"),
	}
	var r Result
	ok, _ := checkK8sSandboxImageMatchesManifest(k)(context.Background(), newFakeSystem(), &r)
	if ok {
		t.Fatal("ok = true, want false")
	}
	s, _ := stepStatus(r, "sandbox image matches the manifest")
	if s.Status != StepRefused || !strings.Contains(s.Detail, "k8s-sandbox:main") || !strings.Contains(s.Detail, "k8s-sandbox:latest") {
		t.Fatalf("step = %+v, want StepRefused naming both tags", s)
	}
}

func TestCheckK8sSandboxImageMatchesManifestPasses(t *testing.T) {
	t.Parallel()
	for _, content := range []string{
		"        - -k8s-sandbox-image=example.com/sb:main\n",
		"        - -k8s-workspace=/home/agent\n", // names none: the daemon runs its build's default
	} {
		k := K8sOptions{SandboxImage: "example.com/sb:main", Manifest: writeTestManifest(t, content)}
		var r Result
		if ok, _ := checkK8sSandboxImageMatchesManifest(k)(context.Background(), newFakeSystem(), &r); !ok {
			t.Errorf("manifest %q: ok = false, want true: %+v", content, r.Steps)
		}
	}
}

// --- checkK8sNamespacePodSecurity ---------------------------------------

func TestCheckK8sNamespacePodSecurityPassesWhenNamespaceIsAbsent(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	k := K8sOptions{Namespace: "grain"}
	// Nothing configured for "get namespace grain ..." -> errNotFound,
	// the shape a namespace that does not exist yet produces.
	var r Result
	ok, abort := checkK8sNamespacePodSecurity(k)(context.Background(), sys, &r)
	if !ok || abort {
		t.Fatalf("got (ok=%v, abort=%v), want (true, false): a namespace grain.yaml has not created yet is not a refusal", ok, abort)
	}
}

func TestCheckK8sNamespacePodSecurityPassesWhenBaseline(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	k := K8sOptions{Namespace: "grain"}
	sys.on("baseline", nil, "kubectl", "get", "namespace", "grain", "-o", `jsonpath={.metadata.labels.pod-security\.kubernetes\.io/enforce}`)
	var r Result
	ok, _ := checkK8sNamespacePodSecurity(k)(context.Background(), sys, &r)
	if !ok {
		t.Fatalf("ok = false, want true for an existing baseline namespace: %+v", r.Steps)
	}
}

// TestCheckK8sNamespacePodSecurityRefusesPrivileged is the property
// this platform exists to hold: a namespace already relaxed to
// "privileged" is a PlatformK8sGranule cluster, and this platform must
// refuse to apply there rather than hand away the isolation its whole
// trade is built on.
func TestCheckK8sNamespacePodSecurityRefusesPrivileged(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	k := K8sOptions{Namespace: "grain"}
	sys.on("privileged", nil, "kubectl", "get", "namespace", "grain", "-o", `jsonpath={.metadata.labels.pod-security\.kubernetes\.io/enforce}`)
	var r Result
	ok, abort := checkK8sNamespacePodSecurity(k)(context.Background(), sys, &r)
	if ok || abort {
		t.Fatalf("got (ok=%v, abort=%v), want (false, false): refused but not aborting, so other checks still run", ok, abort)
	}
	s, _ := stepStatus(r, "namespace pod-security level")
	if s.Status != StepRefused {
		t.Fatalf("status = %s, want %s", s.Status, StepRefused)
	}
	if !strings.Contains(s.Detail, "privileged") {
		t.Errorf("detail = %q, want it to name the level found", s.Detail)
	}
}

// --- checkK8sPermissions ------------------------------------------------

func allowAllK8sPerms(sys *fakeSystem, k K8sOptions) {
	for _, p := range k8sRequiredPerms {
		args := []string{"auth", "can-i", "create", p.resource}
		if p.namespaced {
			args = append(args, "-n", k.Namespace)
		}
		// "yes"/"no" is always kubectl's *last* line -- a cluster-scoped
		// resource like "namespaces" also gets a "Warning: ... is not
		// namespace scoped" line on stderr first, merged into the same
		// combined output System.Run returns (confirmed against a real
		// cluster; see lastLine's own doc comment). Reproduce that here
		// so a regression back to an exact-equality comparison fails
		// this test rather than only a real cluster.
		sys.on("Warning: resource 'x' is not namespace scoped\n\nyes", nil, "kubectl", args...)
	}
}

func TestCheckK8sPermissionsPassesWhenEverythingIsAllowed(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	k := K8sOptions{Namespace: "grain"}
	allowAllK8sPerms(sys, k)
	var r Result
	ok, _ := checkK8sPermissions(k)(context.Background(), sys, &r)
	if !ok {
		t.Fatalf("ok = false, want true: %+v", r.Steps)
	}
}

func TestCheckK8sPermissionsReportsEveryMissingOneTogether(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	k := K8sOptions{Namespace: "grain"}
	allowAllK8sPerms(sys, k)
	// Override two of them to "no".
	sys.on("no", nil, "kubectl", "auth", "can-i", "create", "deployments.apps", "-n", "grain")
	sys.on("no", nil, "kubectl", "auth", "can-i", "create", "services", "-n", "grain")
	var r Result
	ok, _ := checkK8sPermissions(k)(context.Background(), sys, &r)
	if ok {
		t.Fatal("ok = true, want false: two permissions are missing")
	}
	s, _ := stepStatus(r, "permission to create what grain.yaml creates")
	if s.Status != StepManual {
		t.Fatalf("status = %s, want %s", s.Status, StepManual)
	}
	for _, want := range []string{"deployments.apps", "services"} {
		if !strings.Contains(s.Detail, want) {
			t.Errorf("detail = %q, want it to mention %q", s.Detail, want)
		}
	}
	// make, in particular, should not be the only resource mentioned
	// per call -- every check must have run, not just the first no.
	if len(sys.calls) != len(k8sRequiredPerms) {
		t.Errorf("calls = %v, want exactly one per required permission", sys.calls)
	}
}

// --- checkK8sSandboxImagePullable ---------------------------------------

func TestCheckK8sSandboxImagePullableSucceeds(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	k := K8sOptions{SandboxImage: "example.com/sandbox:latest"}
	sys.on("pod/grain-init-preflight-image created", nil, "kubectl", "run", k8sPreflightImagePod, "-n", k8sPreflightNamespace,
		"--image="+k.SandboxImage, "--restart=Never", "--command", "--", "sleep", "3600")
	sys.on("pod/grain-init-preflight-image condition met", nil, "kubectl", "wait", "pod/"+k8sPreflightImagePod, "-n", k8sPreflightNamespace,
		"--for=condition=Ready", "--timeout=1s")
	sys.on("", nil, "kubectl", "delete", "pod", k8sPreflightImagePod, "-n", k8sPreflightNamespace, "--ignore-not-found", "--grace-period=1", "--timeout=1s")

	var r Result
	ok, _ := checkK8sSandboxImagePullable(k, t.TempDir(), time.Second)(context.Background(), sys, &r)
	if !ok {
		t.Fatalf("ok = false, want true: %+v", r.Steps)
	}
	if !sys.calledAny("kubectl delete pod " + k8sPreflightImagePod + " -n " + k8sPreflightNamespace + " --ignore-not-found --grace-period=1 --timeout=1s") {
		t.Error("the throwaway pod was never cleaned up")
	}
}

func TestCheckK8sSandboxImagePullableReportsImagePullBackOff(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	k := K8sOptions{SandboxImage: "example.com/does-not-exist:latest"}
	sys.on("pod/grain-init-preflight-image created", nil, "kubectl", "run", k8sPreflightImagePod, "-n", k8sPreflightNamespace,
		"--image="+k.SandboxImage, "--restart=Never", "--command", "--", "sleep", "3600")
	sys.on("", errNotFound, "kubectl", "wait", "pod/"+k8sPreflightImagePod, "-n", k8sPreflightNamespace,
		"--for=condition=Ready", "--timeout=1s")
	sys.on("ImagePullBackOff", nil, "kubectl", "get", "pod", k8sPreflightImagePod, "-n", k8sPreflightNamespace,
		"-o", "jsonpath={.status.containerStatuses[0].state.waiting.reason}")
	sys.on("", nil, "kubectl", "delete", "pod", k8sPreflightImagePod, "-n", k8sPreflightNamespace, "--ignore-not-found", "--grace-period=1", "--timeout=1s")

	var r Result
	ok, _ := checkK8sSandboxImagePullable(k, t.TempDir(), time.Second)(context.Background(), sys, &r)
	if ok {
		t.Fatal("ok = true, want false")
	}
	s, _ := stepStatus(r, "sandbox image pulls")
	if s.Status != StepManual || !strings.Contains(s.Detail, "ImagePullBackOff") {
		t.Fatalf("step = %+v, want StepManual naming ImagePullBackOff", s)
	}
	// The printed describe command needs the pod it names: only the
	// delete-before-create may have run, not a cleanup after the failure.
	if n := countCalls(sys, "kubectl delete pod "+k8sPreflightImagePod); n != 1 {
		t.Errorf("deleted the pod %d times, want once (before creating it), leaving it for %q", n, s.Command)
	}
}

// Printed against -k8s-context, the follow-up has to name that context.
func TestCheckK8sSandboxImagePullableCommandNamesTheCheckedContext(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	k := K8sOptions{SandboxImage: "registry.invalid/nope:1", Context: "kind-other"}
	sys.on("", errNotFound, "kubectl", "--context=kind-other", "wait", "pod/"+k8sPreflightImagePod, "-n", k8sPreflightNamespace,
		"--for=condition=Ready", "--timeout=1s")
	sys.on("created", nil, "kubectl", "--context=kind-other", "run", k8sPreflightImagePod, "-n", k8sPreflightNamespace,
		"--image="+k.SandboxImage, "--restart=Never", "--command", "--", "sleep", "3600")
	var r Result
	checkK8sSandboxImagePullable(k, t.TempDir(), time.Second)(context.Background(), sys, &r)
	s, _ := stepStatus(r, "sandbox image pulls")
	if want := "kubectl --context=kind-other -n default describe pod " + k8sPreflightImagePod; s.Command != want {
		t.Fatalf("Command = %q, want %q", s.Command, want)
	}
}

func countCalls(sys *fakeSystem, prefix string) int {
	sys.mu.Lock()
	defer sys.mu.Unlock()
	n := 0
	for _, c := range sys.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// --- checkK8sStorageClassRWO ---------------------------------------------

func TestCheckK8sStorageClassRWOBinds(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	dataDir := t.TempDir()
	k := K8sOptions{SandboxImage: "example.com/sandbox:latest"}
	path := filepath.Join(dataDir, "k8s-preflight", "storageclass-check.yaml")
	sys.on("persistentvolumeclaim/grain-init-preflight-pvc created", nil, "kubectl", "apply", "-f", path)
	sys.on("condition met", nil, "kubectl", "wait", "pod/"+k8sPreflightPVCPod, "-n", k8sPreflightNamespace,
		"--for=condition=Ready", "--timeout=1s")
	sys.on("", nil, "kubectl", "delete", "-f", path, "--ignore-not-found", "--grace-period=1", "--timeout=1s")

	var r Result
	ok, _ := checkK8sStorageClassRWO(k, dataDir, time.Second)(context.Background(), sys, &r)
	if !ok {
		t.Fatalf("ok = false, want true: %+v", r.Steps)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("scratch manifest was not written: %v", err)
	}
}

func TestCheckK8sStorageClassRWODoesNotBind(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	dataDir := t.TempDir()
	k := K8sOptions{StorageClass: "slow-nfs", SandboxImage: "example.com/sandbox:latest"}
	path := filepath.Join(dataDir, "k8s-preflight", "storageclass-check.yaml")
	sys.on("persistentvolumeclaim/grain-init-preflight-pvc created", nil, "kubectl", "apply", "-f", path)
	sys.on("", errNotFound, "kubectl", "wait", "pod/"+k8sPreflightPVCPod, "-n", k8sPreflightNamespace,
		"--for=condition=Ready", "--timeout=1s")
	sys.on("", nil, "kubectl", "delete", "-f", path, "--ignore-not-found", "--grace-period=1", "--timeout=1s")

	var r Result
	ok, _ := checkK8sStorageClassRWO(k, dataDir, time.Second)(context.Background(), sys, &r)
	if ok {
		t.Fatal("ok = true, want false")
	}
	s, _ := stepStatus(r, "storage class offers ReadWriteOnce")
	if s.Status != StepManual || !strings.Contains(s.Detail, "slow-nfs") {
		t.Fatalf("step = %+v, want StepManual naming the storage class", s)
	}
	if n := countCalls(sys, "kubectl delete -f "+path); n != 1 {
		t.Errorf("deleted the scratch PVC and pod %d times, want once (before creating them), leaving them for %q", n, s.Command)
	}
}

// A PVC that binds while its consuming pod cannot pull the sandbox
// image is an image problem, not a storage-class one -- a real kind
// cluster with an unpullable -k8s-sandbox-image used to be told its
// default class could not give ReadWriteOnce.
func TestCheckK8sStorageClassRWOBlamesThePodNotTheClassWhenThePVCBound(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	dataDir := t.TempDir()
	k := K8sOptions{SandboxImage: "registry.invalid/nope:1"}
	path := filepath.Join(dataDir, "k8s-preflight", "storageclass-check.yaml")
	sys.on("persistentvolumeclaim/grain-init-preflight-pvc created", nil, "kubectl", "apply", "-f", path)
	sys.on("", errNotFound, "kubectl", "wait", "pod/"+k8sPreflightPVCPod, "-n", k8sPreflightNamespace,
		"--for=condition=Ready", "--timeout=1s")
	sys.on("Bound", nil, "kubectl", "get", "pvc", k8sPreflightPVC, "-n", k8sPreflightNamespace,
		"-o", "jsonpath={.status.phase}")
	sys.on("ImagePullBackOff", nil, "kubectl", "get", "pod", k8sPreflightPVCPod, "-n", k8sPreflightNamespace,
		"-o", "jsonpath={.status.containerStatuses[0].state.waiting.reason}")
	sys.on("", nil, "kubectl", "delete", "-f", path, "--ignore-not-found", "--grace-period=1", "--timeout=1s")

	var r Result
	ok, _ := checkK8sStorageClassRWO(k, dataDir, time.Second)(context.Background(), sys, &r)
	if ok {
		t.Fatal("ok = true, want false")
	}
	s, _ := stepStatus(r, "storage class offers ReadWriteOnce")
	if s.Status != StepManual || !strings.Contains(s.Detail, "bound") || !strings.Contains(s.Detail, "ImagePullBackOff") {
		t.Fatalf("step = %+v, want StepManual saying the PVC bound and naming the pod's ImagePullBackOff", s)
	}
	if strings.Contains(s.Detail, "needs a class that actually gives ReadWriteOnce") {
		t.Fatalf("step = %+v, still blames the storage class", s)
	}
}

// --- checkK8sNetworkPolicyEnforced ----------------------------------------

func TestCheckK8sNetworkPolicyEnforcedSkippedWhenNotRequired(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	k := K8sOptions{RequireNetworkPolicy: false}
	var r Result
	ok, _ := checkK8sNetworkPolicyEnforced(k, t.TempDir(), time.Second)(context.Background(), sys, &r)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if len(sys.calls) != 0 {
		t.Errorf("kubectl was called despite RequireNetworkPolicy=false: %v", sys.calls)
	}
}

func TestCheckK8sNetworkPolicyEnforcedPassesWhenEgressIsBlocked(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	dataDir := t.TempDir()
	k := K8sOptions{RequireNetworkPolicy: true, SandboxImage: "example.com/sandbox:latest"}
	path := filepath.Join(dataDir, "k8s-preflight", "networkpolicy-check.yaml")
	sys.on("networkpolicy.networking.k8s.io/grain-init-preflight-netpol created", nil, "kubectl", "apply", "-f", path)
	sys.on("pod created", nil, "kubectl", "run", k8sPreflightNetpolPod, "-n", k8sPreflightNamespace,
		"--image="+k.SandboxImage, "--restart=Never", "--labels=grain.dev/init-preflight=network-none",
		"--command", "--", "sleep", "60")
	sys.on("condition met", nil, "kubectl", "wait", "pod/"+k8sPreflightNetpolPod, "-n", k8sPreflightNamespace,
		"--for=condition=Ready", "--timeout=1s")
	// curl fails -- egress was blocked, which is the success case.
	sys.on("", errNotFound, "kubectl", "exec", "-n", k8sPreflightNamespace, k8sPreflightNetpolPod, "--",
		"curl", "-m", "3", "-s", "-o", "/dev/null", "http://1.1.1.1")
	sys.on("", nil, "kubectl", "delete", "pod", k8sPreflightNetpolPod, "-n", k8sPreflightNamespace, "--ignore-not-found", "--grace-period=1", "--timeout=1s")
	sys.on("", nil, "kubectl", "delete", "-f", path, "--ignore-not-found", "--grace-period=1", "--timeout=1s")

	var r Result
	ok, _ := checkK8sNetworkPolicyEnforced(k, dataDir, time.Second)(context.Background(), sys, &r)
	if !ok {
		t.Fatalf("ok = false, want true: %+v", r.Steps)
	}
}

func TestCheckK8sNetworkPolicyEnforcedRefusesWhenEgressGetsThrough(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	dataDir := t.TempDir()
	k := K8sOptions{RequireNetworkPolicy: true, SandboxImage: "example.com/sandbox:latest"}
	path := filepath.Join(dataDir, "k8s-preflight", "networkpolicy-check.yaml")
	sys.on("networkpolicy.networking.k8s.io/grain-init-preflight-netpol created", nil, "kubectl", "apply", "-f", path)
	sys.on("pod created", nil, "kubectl", "run", k8sPreflightNetpolPod, "-n", k8sPreflightNamespace,
		"--image="+k.SandboxImage, "--restart=Never", "--labels=grain.dev/init-preflight=network-none",
		"--command", "--", "sleep", "60")
	sys.on("condition met", nil, "kubectl", "wait", "pod/"+k8sPreflightNetpolPod, "-n", k8sPreflightNamespace,
		"--for=condition=Ready", "--timeout=1s")
	// curl succeeds -- egress was NOT blocked, the failure case.
	sys.on("200", nil, "kubectl", "exec", "-n", k8sPreflightNamespace, k8sPreflightNetpolPod, "--",
		"curl", "-m", "3", "-s", "-o", "/dev/null", "http://1.1.1.1")
	sys.on("", nil, "kubectl", "delete", "pod", k8sPreflightNetpolPod, "-n", k8sPreflightNamespace, "--ignore-not-found", "--grace-period=1", "--timeout=1s")
	sys.on("", nil, "kubectl", "delete", "-f", path, "--ignore-not-found", "--grace-period=1", "--timeout=1s")

	var r Result
	ok, _ := checkK8sNetworkPolicyEnforced(k, dataDir, time.Second)(context.Background(), sys, &r)
	if ok {
		t.Fatal("ok = true, want false: the test pod reached the internet despite the deny-all policy")
	}
	s, _ := stepStatus(r, "CNI enforces NetworkPolicy")
	if s.Status != StepRefused {
		t.Fatalf("status = %s, want %s", s.Status, StepRefused)
	}
}

// --- checkK8sUid1000Migration ---------------------------------------------

func TestCheckK8sUid1000MigrationSkipsWhenDeploymentIsAbsent(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	k := K8sOptions{Namespace: "grain", Deployment: "grain"}
	var r Result
	ok, abort := checkK8sUid1000Migration(k)(context.Background(), sys, &r)
	if !ok || abort {
		t.Fatalf("got (ok=%v, abort=%v), want (true, false)", ok, abort)
	}
}

func TestCheckK8sUid1000MigrationSkipsWhenAlreadyMigrated(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	k := K8sOptions{Namespace: "grain", Deployment: "grain"}
	sys.on("1000", nil, "kubectl", "get", "deployment", "grain", "-n", "grain",
		"-o", "jsonpath={.spec.template.spec.securityContext.runAsUser}")
	var r Result
	ok, abort := checkK8sUid1000Migration(k)(context.Background(), sys, &r)
	if !ok || abort {
		t.Fatalf("got (ok=%v, abort=%v), want (true, false)", ok, abort)
	}
}

func TestCheckK8sUid1000MigrationReportsManualAndAborts(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	k := K8sOptions{Namespace: "grain", Deployment: "grain"}
	sys.on("", nil, "kubectl", "get", "deployment", "grain", "-n", "grain",
		"-o", "jsonpath={.spec.template.spec.securityContext.runAsUser}")
	var r Result
	ok, abort := checkK8sUid1000Migration(k)(context.Background(), sys, &r)
	if ok || !abort {
		t.Fatalf("got (ok=%v, abort=%v), want (false, true): applying over an unmigrated volume is not safe to attempt", ok, abort)
	}
	s, _ := stepStatus(r, "uid-1000 migration")
	if s.Status != StepManual || !strings.Contains(s.Command, "chown") {
		t.Fatalf("step = %+v, want StepManual with the chown Job as Command", s)
	}
}

// --- ensureK8sManifest / ensureK8sNetworkPolicyManifest / ensureK8sRollout --

func TestEnsureK8sManifestSkipsWhenDeploymentAlreadyExists(t *testing.T) {
	t.Parallel()
	opts := k8sTestOptions(t)
	sys := newFakeSystem()
	sys.on("deployment.apps/grain", nil, "kubectl", "get", "deployment", opts.K8s.Deployment, "-n", opts.K8s.Namespace)
	var r Result
	if !ensureK8sManifest(context.Background(), sys, opts, &r) {
		t.Fatalf("ensureK8sManifest = false, want true: %+v", r.Steps)
	}
	s, _ := stepStatus(r, "apply the grain manifest")
	if s.Status != StepSkipped {
		t.Fatalf("status = %s, want %s", s.Status, StepSkipped)
	}
	if sys.calledAny("kubectl apply -f " + opts.K8s.Manifest) {
		t.Error("ensureK8sManifest re-applied over an existing deployment")
	}
}

func TestEnsureK8sManifestAppliesWhenAbsent(t *testing.T) {
	t.Parallel()
	opts := k8sTestOptions(t)
	sys := newFakeSystem()
	// "get deployment" unconfigured -> errNotFound, as if absent.
	sys.on("namespace/grain created", nil, "kubectl", "apply", "-f", opts.K8s.Manifest)
	var r Result
	if !ensureK8sManifest(context.Background(), sys, opts, &r) {
		t.Fatalf("ensureK8sManifest = false, want true: %+v", r.Steps)
	}
	s, _ := stepStatus(r, "apply the grain manifest")
	if s.Status != StepDone {
		t.Fatalf("status = %s, want %s", s.Status, StepDone)
	}
}

func TestEnsureK8sManifestDryRunChangesNothing(t *testing.T) {
	t.Parallel()
	opts := k8sTestOptions(t)
	opts.DryRun = true
	sys := newFakeSystem()
	var r Result
	if ensureK8sManifest(context.Background(), sys, opts, &r) {
		t.Fatal("ensureK8sManifest = true on a dry run, want false")
	}
	if len(sys.calls) != 0 {
		t.Errorf("a dry run called kubectl: %v", sys.calls)
	}
	s, _ := stepStatus(r, "apply the grain manifest")
	if s.Status != StepWould {
		t.Fatalf("status = %s, want %s", s.Status, StepWould)
	}
}

func TestEnsureK8sRollout(t *testing.T) {
	t.Parallel()
	opts := k8sTestOptions(t)
	sys := newFakeSystem()
	sys.on("deployment \"grain\" successfully rolled out", nil, "kubectl", "rollout", "status",
		"deployment/"+opts.K8s.Deployment, "-n", opts.K8s.Namespace, "--timeout="+opts.K8s.RolloutTimeout.String())
	var r Result
	if !ensureK8sRollout(context.Background(), sys, opts, &r) {
		t.Fatalf("ensureK8sRollout = false, want true: %+v", r.Steps)
	}
}

// --- establishK8sReachability --------------------------------------------

func TestEstablishK8sReachabilityRunsThePortForward(t *testing.T) {
	t.Parallel()
	opts := k8sTestOptions(t)
	stopped := false
	opts.K8sPortForward = func(ctx context.Context, o Options) (func(), error) {
		return func() { stopped = true }, nil
	}
	var r Result
	stop, ok := establishK8sReachability(context.Background(), opts, &r)
	if !ok || stop == nil {
		t.Fatalf("got (stop==nil=%v, ok=%v), want a non-nil stop func and true", stop == nil, ok)
	}
	stop()
	if !stopped {
		t.Error("the stop func returned by establishK8sReachability did not call through to K8sPortForward's own stop")
	}
	s, _ := stepStatus(r, "reach the daemon's API")
	if s.Status != StepDone {
		t.Fatalf("status = %s, want %s", s.Status, StepDone)
	}
}

func TestEstablishK8sReachabilitySkipsWhenNotManaged(t *testing.T) {
	t.Parallel()
	opts := k8sTestOptions(t)
	opts.K8sPortForward = nil
	var r Result
	stop, ok := establishK8sReachability(context.Background(), opts, &r)
	if !ok || stop != nil {
		t.Fatalf("got (stop==nil=%v, ok=%v), want (true, true)", stop == nil, ok)
	}
	s, _ := stepStatus(r, "reach the daemon's API")
	if s.Status != StepSkipped {
		t.Fatalf("status = %s, want %s", s.Status, StepSkipped)
	}
}

func TestEstablishK8sReachabilityReportsFailure(t *testing.T) {
	t.Parallel()
	opts := k8sTestOptions(t)
	opts.K8sPortForward = func(ctx context.Context, o Options) (func(), error) {
		return nil, context.DeadlineExceeded
	}
	var r Result
	stop, ok := establishK8sReachability(context.Background(), opts, &r)
	if ok || stop != nil {
		t.Fatalf("got (stop==nil=%v, ok=%v), want (true, false)", stop == nil, ok)
	}
	s, _ := stepStatus(r, "reach the daemon's API")
	if s.Status != StepManual {
		t.Fatalf("status = %s, want %s", s.Status, StepManual)
	}
}

// --- initForK8sContainer / Init end to end -------------------------------

// stubK8sPreflight configures every kubectl call k8sContainerChecks
// makes when every prerequisite is already satisfied and
// RequireNetworkPolicy is false, the k8s-container equivalent of
// stubPreflight.
func stubK8sPreflight(t *testing.T, sys *fakeSystem, opts Options) {
	t.Helper()
	k := opts.K8s
	sys.on("ok", nil, "kubectl", "cluster-info")
	sys.on("baseline", nil, "kubectl", "get", "namespace", k.Namespace, "-o", `jsonpath={.metadata.labels.pod-security\.kubernetes\.io/enforce}`)
	allowAllK8sPerms(sys, k)
	sys.on("created", nil, "kubectl", "run", k8sPreflightImagePod, "-n", k8sPreflightNamespace,
		"--image="+k.SandboxImage, "--restart=Never", "--command", "--", "sleep", "3600")
	sys.on("ready", nil, "kubectl", "wait", "pod/"+k8sPreflightImagePod, "-n", k8sPreflightNamespace,
		"--for=condition=Ready", "--timeout="+k.PreflightTimeout.String())
	sys.on("", nil, "kubectl", "delete", "pod", k8sPreflightImagePod, "-n", k8sPreflightNamespace, "--ignore-not-found", "--grace-period=1", "--timeout="+k.PreflightTimeout.String())
	pvcPath := filepath.Join(opts.DataDir, "k8s-preflight", "storageclass-check.yaml")
	sys.on("created", nil, "kubectl", "apply", "-f", pvcPath)
	sys.on("bound", nil, "kubectl", "wait", "pod/"+k8sPreflightPVCPod, "-n", k8sPreflightNamespace,
		"--for=condition=Ready", "--timeout="+k.PreflightTimeout.String())
	sys.on("", nil, "kubectl", "delete", "-f", pvcPath, "--ignore-not-found", "--grace-period=1", "--timeout="+k.PreflightTimeout.String())
	// RequireNetworkPolicy is false, so checkK8sNetworkPolicyEnforced
	// makes no kubectl call at all. checkK8sUid1000Migration's own
	// "get deployment" is deliberately left unconfigured here (as if
	// the deployment does not exist yet, errNotFound) -- a caller
	// whose deployment already exists configures that key itself.
}

func TestInitK8sContainerEndToEnd(t *testing.T) {
	t.Parallel()
	opts := k8sTestOptions(t)
	opts.GitHubRepos = []string{"acme/widgets"}
	opts.GitHubCredentialName = "" // no token given -- the credential ladder step is irrelevant here
	stopped := false
	opts.K8sPortForward = func(ctx context.Context, o Options) (func(), error) {
		return func() { stopped = true }, nil
	}

	sys := newFakeSystem()
	stubK8sPreflight(t, sys, opts)
	// "get deployment" (ensureK8sManifest's own existence check) is
	// unconfigured -> errNotFound, so apply runs.
	sys.on("applied", nil, "kubectl", "apply", "-f", opts.K8s.Manifest)
	sys.on("rolled out", nil, "kubectl", "rollout", "status", "deployment/"+opts.K8s.Deployment,
		"-n", opts.K8s.Namespace, "--timeout="+opts.K8s.RolloutTimeout.String())

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
	for _, name := range []string{"apply the grain manifest", "wait for the rollout", "reach the daemon's API"} {
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
	// None of the host-build steps exist for this platform.
	for _, name := range []string{"git", "docker", "go toolchain", "build the granule CLI", "install the claude CLI"} {
		if _, found := stepStatus(result, name); found {
			t.Errorf("Steps included %q, which is a host-build step this platform has no use for", name)
		}
	}
}

// TestInitK8sContainerStoresTheGitHubTokenOnTheTeam: against a real
// kind cluster, -github-token-file was accepted and silently dropped on
// this platform (the host credential ladder writes under -data-dir,
// which here is not the daemon's), init exited 0, and every task into
// -github-repo was then refused for "team "default" has no GitHub
// credential". The token has to reach the daemon over its API, while
// the port-forward is still up.
func TestInitK8sContainerStoresTheGitHubTokenOnTheTeam(t *testing.T) {
	t.Parallel()
	opts := k8sTestOptions(t)
	opts.GitHubRepos = []string{"acme/widgets"}
	opts.GitHubCredentialName = "bot"
	opts.GitHubToken = "ghp_walkthrough"
	stopped := false
	opts.K8sPortForward = func(ctx context.Context, o Options) (func(), error) {
		return func() { stopped = true }, nil
	}

	sys := newFakeSystem()
	stubK8sPreflight(t, sys, opts)
	sys.on("applied", nil, "kubectl", "apply", "-f", opts.K8s.Manifest)
	sys.on("rolled out", nil, "kubectl", "rollout", "status", "deployment/"+opts.K8s.Deployment,
		"-n", opts.K8s.Namespace, "--timeout="+opts.K8s.RolloutTimeout.String())
	base := "http://" + opts.UIAddr
	sys.onHTTPRequest("GET", base+"/api/config", 200, []byte(`{}`), nil)
	sys.onHTTPRequest("GET", base+"/api/deployment", 200, []byte(`{"maxWorkers":0,"targetRepos":[]}`), nil)
	sys.onHTTPRequest("PUT", base+"/api/deployment", 200, nil, nil)
	sys.onHTTPRequest("POST", base+"/api/deployment/git-connections", 200, []byte(`{}`), nil)
	sys.onHTTPRequest("GET", base+"/api/settings", 200, []byte(roomySettingsJSON), nil)

	result, err := Init(context.Background(), sys, opts)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if result.HasManual() || result.HasRefused() {
		t.Fatalf("Steps = %+v, want every step done or skipped", result.Steps)
	}
	s, found := stepStatus(result, "give the deployment the GitHub token")
	if !found || s.Status != StepDone {
		t.Fatalf("step = %+v (found %v), want StepDone", s, found)
	}
	var put *fakeHTTPRequestCall
	for i, c := range sys.httpRequestCalls {
		if c.method == "POST" && c.url == base+"/api/deployment/git-connections" {
			put = &sys.httpRequestCalls[i]
		}
	}
	if put == nil {
		t.Fatalf("no POST to the team's git-connections in %+v", sys.httpRequestCalls)
	}
	if string(put.body) != `{"type":"pat","host":"github.com","value":"ghp_walkthrough"}` {
		t.Errorf("POST body = %s, want the token as a github.com PAT connection", put.body)
	}
	if strings.Contains(s.Detail, opts.GitHubToken) {
		t.Errorf("step detail %q echoes the token", s.Detail)
	}
	if !stopped {
		t.Error("Init returned without stopping the port-forward it started")
	}
	// 10-05 walkthrough: the host-only "point the default team at the
	// GitHub credential" step used to PUT gitCredential=bot first, naming
	// a ladder entry the cluster never had -- and on a real daemon, which
	// then answers GET with that name, the token step above skipped.
	for _, c := range sys.httpRequestCalls {
		if c.method == "PUT" && c.url == base+"/api/deployment" && strings.Contains(string(c.body), "gitCredential") {
			t.Errorf("PUT %s %s points the team at a credential the cluster does not have", c.url, c.body)
		}
	}
	if s, _ := stepStatus(result, "point the deployment at the GitHub credential"); s.Status != StepSkipped {
		t.Errorf("point the deployment at the GitHub credential = %+v, want StepSkipped on k8s", s)
	}
}

func TestEnsureK8sTeamGitHubCredentialLeavesAnExistingOneAlone(t *testing.T) {
	t.Parallel()
	opts := k8sTestOptions(t).withDefaults()
	opts.GitHubToken = "ghp_new"
	sys := newFakeSystem()
	sys.onHTTPRequest("GET", "http://"+opts.UIAddr+"/api/deployment", 200, []byte(`{"gitCredential":"team:default"}`), nil)

	var r Result
	ensureK8sTeamGitHubCredential(context.Background(), sys, opts, &r)
	s, _ := stepStatus(r, "give the deployment the GitHub token")
	if s.Status != StepSkipped {
		t.Fatalf("step = %+v, want StepSkipped", s)
	}
	for _, c := range sys.httpRequestCalls {
		if c.method == "PUT" {
			t.Fatalf("PUT %s on a team that already has a credential", c.url)
		}
	}
}

// A default team whose credentials are all git connections, its legacy
// gitCredential blanked, is not given a second, legacy one
// (grain/task-3079).
func TestEnsureK8sTeamGitHubCredentialCountsGitConnections(t *testing.T) {
	t.Parallel()
	opts := k8sTestOptions(t).withDefaults()
	opts.GitHubToken = "ghp_new"
	sys := newFakeSystem()
	sys.onHTTPRequest("GET", "http://"+opts.UIAddr+"/api/deployment", 200,
		[]byte(`{"gitCredential":"","hasGitCredential":true,"gitConnections":[{"credential":"a"},{"credential":"b","scope":"acme"}]}`), nil)

	var r Result
	ensureK8sTeamGitHubCredential(context.Background(), sys, opts, &r)
	s, _ := stepStatus(r, "give the deployment the GitHub token")
	if s.Status != StepSkipped {
		t.Fatalf("step = %+v, want StepSkipped", s)
	}
	for _, c := range sys.httpRequestCalls {
		if c.method == "PUT" {
			t.Fatalf("PUT %s on a team whose git connections already cover it", c.url)
		}
	}
}

func TestInitK8sContainerStopsAtPreflightWithoutApplying(t *testing.T) {
	t.Parallel()
	opts := k8sTestOptions(t)
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
