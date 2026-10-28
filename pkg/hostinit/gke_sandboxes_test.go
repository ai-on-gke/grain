package hostinit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGKESandboxesDryRunPlanDefaultN2(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	opts := baseOptions(t)
	opts.Platform = PlatformGKESandboxes
	opts.DryRun = true

	result, err := Init(context.Background(), sys, opts)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if len(sys.calls) != 0 {
		t.Fatalf("DryRun made system calls: %v", sys.calls)
	}

	wantSteps := []string{
		"create or reuse GKE Standard cluster",
		"ensure nested-virtualization node pool",
		"install granule KVM device plugin",
		"create grain namespace and least-privilege ServiceAccount RBAC",
		"write /var/lib/grain/kubeconfig",
		"verify with scripts/platform-verify/k8s-granule.sh",
		"verify throwaway sandbox git proxy clone and package cache fetch",
	}
	if len(result.Steps) != len(wantSteps) {
		t.Fatalf("Steps = %+v, want %d steps (%v)", result.Steps, len(wantSteps), wantSteps)
	}
	for i, wantName := range wantSteps {
		s := result.Steps[i]
		if s.Name != wantName {
			t.Errorf("step[%d].Name = %q, want %q", i, s.Name, wantName)
		}
		if s.Status != StepWould {
			t.Errorf("step[%d] (%s).Status = %q, want %q", i, s.Name, s.Status, StepWould)
		}
		if strings.TrimSpace(s.Command) == "" {
			t.Errorf("step[%d] (%s) has empty Command", i, s.Name)
		}
		if !strings.Contains(s.Detail, s.Command) {
			t.Errorf("step[%d] (%s) Detail %q does not include Command %q", i, s.Name, s.Detail, s.Command)
		}
	}

	clusterStep, _ := stepStatus(result, "create or reuse GKE Standard cluster")
	for _, wantSub := range []string{
		"--enable-nested-virtualization",
		"--machine-type=n2-standard-8",
		"--disk-type=pd-balanced",
		"--image-type=UBUNTU_CONTAINERD",
		"--zone=us-central1-a",
	} {
		if !strings.Contains(clusterStep.Command, wantSub) {
			t.Errorf("cluster Command %q missing %q", clusterStep.Command, wantSub)
		}
	}

	pluginStep, _ := stepStatus(result, "install granule KVM device plugin")
	if !strings.Contains(pluginStep.Command, filepath.Join("third_party", "granule", "deploy", "k8s", "kvm-device-plugin.yaml")) {
		t.Errorf("device plugin Command %q missing kvm-device-plugin.yaml", pluginStep.Command)
	}

	rbacStep, _ := stepStatus(result, "create grain namespace and least-privilege ServiceAccount RBAC")
	for _, wantSub := range []string{
		"pod-security.kubernetes.io/enforce=privileged",
		"pods, pods/exec, pods/log",
		"PriorityClasses grain-interactive, grain-agent, grain-ci",
	} {
		if !strings.Contains(rbacStep.Detail, wantSub) {
			t.Errorf("RBAC Detail %q missing %q", rbacStep.Detail, wantSub)
		}
	}

	kubeStep, _ := stepStatus(result, "write /var/lib/grain/kubeconfig")
	if !strings.Contains(kubeStep.Command, DefaultGKESandboxesKubeconfig) {
		t.Errorf("kubeconfig Command %q missing %s", kubeStep.Command, DefaultGKESandboxesKubeconfig)
	}

	verifyStep, _ := stepStatus(result, "verify with scripts/platform-verify/k8s-granule.sh")
	if !strings.Contains(verifyStep.Command, filepath.Join("scripts", "platform-verify", "k8s-granule.sh")) {
		t.Errorf("verify script Command %q missing scripts/platform-verify/k8s-granule.sh", verifyStep.Command)
	}

	sandboxStep, _ := stepStatus(result, "verify throwaway sandbox git proxy clone and package cache fetch")
	if !strings.Contains(sandboxStep.Command, DefaultGKESandboxesGitProxyURL) ||
		!strings.Contains(sandboxStep.Command, DefaultGKESandboxesPackageCacheURL) {
		t.Errorf("throwaway sandbox Command %q missing git proxy or package cache URL", sandboxStep.Command)
	}
}

func TestGKESandboxesDryRunPlanC4SelectsHyperdiskBalanced(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	opts := baseOptions(t)
	opts.Platform = PlatformGKESandboxes
	opts.DryRun = true
	opts.GKESandboxes = GKESandboxesOptions{
		Project:     "my-sandbox-proj",
		Location:    "us-central1",
		Cluster:     "c4-cluster",
		NodePool:    "c4-pool",
		MachineType: "c4-standard-96-lssd",
		NumNodes:    2,
		MinNodes:    1,
		MaxNodes:    4,
	}

	result, err := Init(context.Background(), sys, opts)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	clusterStep, ok := stepStatus(result, "create or reuse GKE Standard cluster")
	if !ok {
		t.Fatalf("missing cluster step: %+v", result.Steps)
	}
	for _, wantSub := range []string{
		"--project=my-sandbox-proj",
		"--region=us-central1",
		"--machine-type=c4-standard-96-lssd",
		"--disk-type=hyperdisk-balanced",
		"--enable-nested-virtualization",
		"--enable-autoscaling",
		"--min-nodes=1",
		"--max-nodes=4",
	} {
		if !strings.Contains(clusterStep.Command, wantSub) {
			t.Errorf("c4 cluster Command %q missing %q", clusterStep.Command, wantSub)
		}
	}
	poolStep, ok := stepStatus(result, "ensure nested-virtualization node pool")
	if !ok {
		t.Fatalf("missing node pool step: %+v", result.Steps)
	}
	for _, wantSub := range []string{
		"node-pools create c4-pool",
		"--machine-type=c4-standard-96-lssd",
		"--disk-type=hyperdisk-balanced",
		"--enable-nested-virtualization",
	} {
		if !strings.Contains(poolStep.Command, wantSub) {
			t.Errorf("c4 node pool Command %q missing %q", poolStep.Command, wantSub)
		}
	}
}

func TestGKESandboxesFreshClusterCreationAndVerification(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	opts := baseOptions(t)
	opts.Platform = PlatformGKESandboxes
	kubeconfigOut := filepath.Join(opts.DataDir, "out", "kubeconfig")
	opts.GKESandboxes = GKESandboxesOptions{
		Project:        "sandbox-proj",
		Location:       "us-central1-a",
		Cluster:        "grain-sandboxes",
		NodePool:       "kvm-pool",
		MachineType:    "n2-standard-8",
		KubeconfigPath: kubeconfigOut,
	}

	// Preflight binaries present.
	sys.on("Google Cloud SDK 500.0.0", nil, "gcloud", "--version")
	sys.on("Client Version: v1.31.0", nil, "kubectl", "version", "--client")
	sys.on("gke-gcloud-auth-plugin v0.5.9", nil, "gke-gcloud-auth-plugin", "--version")

	// Cluster & node pool do not exist yet, so describe fails and create succeeds.
	sys.on("NOT_FOUND", fmt.Errorf("exit 1"), "gcloud",
		"container", "clusters", "describe", "grain-sandboxes",
		"--project=sandbox-proj", "--zone=us-central1-a", "--format=value(status)")
	sys.on("CREATED", nil, "gcloud",
		"container", "clusters", "create", "grain-sandboxes",
		"--project=sandbox-proj", "--zone=us-central1-a",
		"--machine-type=n2-standard-8", "--image-type=UBUNTU_CONTAINERD",
		"--disk-type=pd-balanced", "--disk-size=100", "--num-nodes=1",
		"--enable-nested-virtualization", "--node-labels=granule.io/kvm=true")

	sys.on("NOT_FOUND", fmt.Errorf("exit 1"), "gcloud",
		"container", "node-pools", "describe", "kvm-pool",
		"--cluster=grain-sandboxes", "--project=sandbox-proj", "--zone=us-central1-a", "--format=value(status)")
	sys.on("CREATED", nil, "gcloud",
		"container", "node-pools", "create", "kvm-pool",
		"--cluster=grain-sandboxes", "--project=sandbox-proj", "--zone=us-central1-a",
		"--machine-type=n2-standard-8", "--image-type=UBUNTU_CONTAINERD",
		"--disk-type=pd-balanced", "--disk-size=100", "--num-nodes=1",
		"--enable-nested-virtualization", "--node-labels=granule.io/kvm=true")

	// get-credentials succeeds.
	sys.on("kubeconfig entry generated", nil, "gcloud",
		"container", "clusters", "get-credentials", "grain-sandboxes",
		"--project=sandbox-proj", "--zone=us-central1-a")

	adminKube := filepath.Join(opts.DataDir, "gke-sandboxes", "admin.kubeconfig")
	rbacManifest := filepath.Join(opts.DataDir, "gke-sandboxes", "rbac.yaml")
	verifyPodManifest := filepath.Join(opts.DataDir, "gke-sandboxes", "verify-sandbox-pod.yaml")
	devicePluginManifest := filepath.Join(opts.RepoRoot, "third_party", "granule", "deploy", "k8s", "kvm-device-plugin.yaml")
	verifyScript := filepath.Join(opts.RepoRoot, "scripts", "platform-verify", "k8s-granule.sh")

	// Device plugin not yet present -> apply + rollout status.
	sys.on("NotFound", fmt.Errorf("exit 1"), "kubectl", "--kubeconfig="+adminKube,
		"-n", "kube-system", "get", "daemonset", "granule-device-plugin",
		"-o", "jsonpath={.status.numberReady}/{.status.desiredNumberScheduled}")
	sys.on("daemonset.apps/granule-device-plugin created", nil, "kubectl", "--kubeconfig="+adminKube,
		"apply", "-f", devicePluginManifest)
	sys.on("daemon set \"granule-device-plugin\" successfully rolled out", nil, "kubectl", "--kubeconfig="+adminKube,
		"-n", "kube-system", "rollout", "status", "daemonset/granule-device-plugin", "--timeout=2m")

	// Namespace not yet present -> apply RBAC manifest.
	sys.on("NotFound", fmt.Errorf("exit 1"), "kubectl", "--kubeconfig="+adminKube,
		"get", "namespace", "grain", "-o", `jsonpath={.metadata.labels.pod-security\.kubernetes\.io/enforce}`)
	sys.on("NotFound", fmt.Errorf("exit 1"), "kubectl", "--kubeconfig="+adminKube,
		"-n", "grain", "get", "rolebinding", "grain", "-o", "name")
	sys.on("namespace/grain created\nserviceaccount/grain created\nrole.rbac/grain created\nrolebinding.rbac/grain created", nil,
		"kubectl", "--kubeconfig="+adminKube, "apply", "-f", rbacManifest)

	// Mint token & write kubeconfig.
	sys.on("eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.test-token\n", nil,
		"kubectl", "--kubeconfig="+adminKube, "-n", "grain", "create", "token", "grain", "--duration=8760h")
	sys.on("User \"grain\" set.", nil,
		"kubectl", "--kubeconfig="+kubeconfigOut, "config", "set-credentials", "grain", "--token=eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.test-token")
	sys.on("Context modified.", nil,
		"kubectl", "--kubeconfig="+kubeconfigOut, "config", "set-context", "--current", "--namespace=grain", "--user=grain")
	sys.on("deleted user gke_sandbox-proj_us-central1-a_grain-sandboxes from "+kubeconfigOut, nil,
		"kubectl", "--kubeconfig="+kubeconfigOut, "config", "delete-user", "gke_sandbox-proj_us-central1-a_grain-sandboxes")
	// The fake gcloud writes nothing, so stand in for the file
	// get-credentials would leave behind -- world-readable, as a umask
	// of 022 would make it -- to show the token never lands in it so.
	if err := os.MkdirAll(filepath.Dir(kubeconfigOut), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kubeconfigOut, []byte("apiVersion: v1\nkind: Config\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Verify script + throwaway sandbox.
	sys.on("OK: k8s-granule verified", nil, verifyScript, "--keep")
	sys.on("", nil, "kubectl", "--kubeconfig="+kubeconfigOut,
		"-n", "grain", "delete", "pod/"+gkeSandboxesVerifyPodName, "--ignore-not-found", "--wait=true", "--grace-period=1")
	sys.on("pod/grain-init-gke-sandboxes-verify created", nil, "kubectl", "--kubeconfig="+kubeconfigOut,
		"-n", "grain", "apply", "-f", verifyPodManifest)
	sys.on("pod/grain-init-gke-sandboxes-verify condition met", nil, "kubectl", "--kubeconfig="+kubeconfigOut,
		"-n", "grain", "wait", "--for=condition=Ready", "pod/"+gkeSandboxesVerifyPodName, "--timeout=5m")
	guestCheckScript := fmt.Sprintf("git -c http.proxy=%s ls-remote %s >/dev/null && curl -fsSL --proxy %s https://proxy.golang.org >/dev/null",
		DefaultGKESandboxesGitProxyURL, DefaultGKESandboxesVerifyRepo, DefaultGKESandboxesPackageCacheURL)
	sys.on("", nil, "kubectl", "--kubeconfig="+kubeconfigOut,
		"-n", "grain", "exec", gkeSandboxesVerifyPodName, "--", "granule", "exec", "--", "sh", "-c", guestCheckScript)

	result, err := Init(context.Background(), sys, opts)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	for _, stepName := range []string{
		"create or reuse GKE Standard cluster",
		"ensure nested-virtualization node pool",
		"install granule KVM device plugin",
		"create grain namespace and least-privilege ServiceAccount RBAC",
		"write /var/lib/grain/kubeconfig",
		"verify with scripts/platform-verify/k8s-granule.sh",
		"verify throwaway sandbox git proxy clone and package cache fetch",
	} {
		s, ok := stepStatus(result, stepName)
		if !ok || s.Status != StepDone {
			t.Errorf("%s: got %+v, want StepDone", stepName, s)
		}
	}

	if fi, err := os.Stat(kubeconfigOut); err != nil {
		t.Fatalf("Stat(%s): %v", kubeconfigOut, err)
	} else if mode := fi.Mode().Perm(); mode != 0o600 {
		t.Errorf("%s mode = %o, want 600: it holds the ServiceAccount's bearer token", kubeconfigOut, mode)
	}

	// Verify the RBAC manifest written to disk has least-privilege rules, privileged namespace, and sandbox PriorityClasses.
	rbacBytes, err := os.ReadFile(rbacManifest)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", rbacManifest, err)
	}
	for _, want := range []string{
		"pod-security.kubernetes.io/enforce: privileged",
		`resources: ["pods"]`,
		`verbs: ["get", "list", "watch", "create", "delete"]`,
		`resources: ["pods/exec"]`,
		`verbs: ["create"]`,
		`resources: ["pods/log"]`,
		`verbs: ["get"]`,
		"kind: PriorityClass",
		"name: grain-interactive",
		"value: 1000000",
		"preemptionPolicy: PreemptLowerPriority",
		"name: grain-agent",
		"value: 100000",
		"name: grain-ci",
		"value: 1000",
	} {
		if !strings.Contains(string(rbacBytes), want) {
			t.Errorf("rbac.yaml missing %q:\n%s", want, string(rbacBytes))
		}
	}
}

func TestGKESandboxesIdempotentAndResumableWhenAlreadyProvisioned(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	opts := baseOptions(t)
	opts.Platform = PlatformGKESandboxes
	kubeconfigOut := filepath.Join(opts.DataDir, "out", "kubeconfig")
	opts.GKESandboxes = GKESandboxesOptions{
		Project:        "sandbox-proj",
		Location:       "us-central1-a",
		Cluster:        "grain-sandboxes",
		NodePool:       "kvm-pool",
		MachineType:    "n2-standard-8",
		KubeconfigPath: kubeconfigOut,
	}

	sys.on("Google Cloud SDK 500.0.0", nil, "gcloud", "--version")
	sys.on("Client Version: v1.31.0", nil, "kubectl", "version", "--client")
	sys.on("gke-gcloud-auth-plugin v0.5.9", nil, "gke-gcloud-auth-plugin", "--version")

	// Cluster, node pool, device plugin, RBAC, and kubeconfig all already exist!
	sys.on("RUNNING\n", nil, "gcloud",
		"container", "clusters", "describe", "grain-sandboxes",
		"--project=sandbox-proj", "--zone=us-central1-a", "--format=value(status)")
	sys.on("RUNNING\n", nil, "gcloud",
		"container", "node-pools", "describe", "kvm-pool",
		"--cluster=grain-sandboxes", "--project=sandbox-proj", "--zone=us-central1-a", "--format=value(status)")
	sys.on("kubeconfig entry generated", nil, "gcloud",
		"container", "clusters", "get-credentials", "grain-sandboxes",
		"--project=sandbox-proj", "--zone=us-central1-a")

	adminKube := filepath.Join(opts.DataDir, "gke-sandboxes", "admin.kubeconfig")
	verifyPodManifest := filepath.Join(opts.DataDir, "gke-sandboxes", "verify-sandbox-pod.yaml")
	verifyScript := filepath.Join(opts.RepoRoot, "scripts", "platform-verify", "k8s-granule.sh")

	sys.on("2/2", nil, "kubectl", "--kubeconfig="+adminKube,
		"-n", "kube-system", "get", "daemonset", "granule-device-plugin",
		"-o", "jsonpath={.status.numberReady}/{.status.desiredNumberScheduled}")
	sys.on("namespace/grain unchanged\nserviceaccount/grain unchanged\nrole.rbac.authorization.k8s.io/grain unchanged\nrolebinding.rbac.authorization.k8s.io/grain unchanged\n", nil,
		"kubectl", "--kubeconfig="+adminKube, "apply", "-f", filepath.Join(opts.DataDir, "gke-sandboxes", "rbac.yaml"))

	if err := os.MkdirAll(filepath.Dir(kubeconfigOut), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kubeconfigOut, []byte("apiVersion: v1\nkind: Config\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sys.on("yes\n", nil, "kubectl", "--kubeconfig="+kubeconfigOut,
		"-n", "grain", "auth", "can-i", "create", "pods")
	sys.on("no\n", fmt.Errorf("exit 1"), "kubectl", "--kubeconfig="+kubeconfigOut,
		"-n", "grain", "auth", "can-i", "get", "secrets")

	sys.on("OK: k8s-granule verified", nil, verifyScript, "--keep")
	sys.on("", nil, "kubectl", "--kubeconfig="+kubeconfigOut,
		"-n", "grain", "delete", "pod/"+gkeSandboxesVerifyPodName, "--ignore-not-found", "--wait=true", "--grace-period=1")
	sys.on("pod/grain-init-gke-sandboxes-verify created", nil, "kubectl", "--kubeconfig="+kubeconfigOut,
		"-n", "grain", "apply", "-f", verifyPodManifest)
	sys.on("pod/grain-init-gke-sandboxes-verify condition met", nil, "kubectl", "--kubeconfig="+kubeconfigOut,
		"-n", "grain", "wait", "--for=condition=Ready", "pod/"+gkeSandboxesVerifyPodName, "--timeout=5m")
	guestCheckScript := fmt.Sprintf("git -c http.proxy=%s ls-remote %s >/dev/null && curl -fsSL --proxy %s https://proxy.golang.org >/dev/null",
		DefaultGKESandboxesGitProxyURL, DefaultGKESandboxesVerifyRepo, DefaultGKESandboxesPackageCacheURL)
	sys.on("", nil, "kubectl", "--kubeconfig="+kubeconfigOut,
		"-n", "grain", "exec", gkeSandboxesVerifyPodName, "--", "granule", "exec", "--", "sh", "-c", guestCheckScript)

	result, err := Init(context.Background(), sys, opts)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	for _, skippedStep := range []string{
		"create or reuse GKE Standard cluster",
		"ensure nested-virtualization node pool",
		"install granule KVM device plugin",
		"create grain namespace and least-privilege ServiceAccount RBAC",
		"write /var/lib/grain/kubeconfig",
	} {
		s, ok := stepStatus(result, skippedStep)
		if !ok || s.Status != StepSkipped {
			t.Errorf("%s: got %+v, want StepSkipped on idempotent re-run", skippedStep, s)
		}
	}
	for _, doneStep := range []string{
		"verify with scripts/platform-verify/k8s-granule.sh",
		"verify throwaway sandbox git proxy clone and package cache fetch",
	} {
		s, ok := stepStatus(result, doneStep)
		if !ok || s.Status != StepDone {
			t.Errorf("%s: got %+v, want StepDone", doneStep, s)
		}
	}
}

// TestGKESandboxesExistingAdminKubeconfigIsNotTakenAsLeastPrivilege pins
// the re-run's skip check on /var/lib/grain/kubeconfig: a file that can
// create pods but can also read secrets is an admin's, not the grain
// ServiceAccount's, and must be rewritten rather than reported done --
// and a grain Role widened since the last run is re-applied, not skipped.
func TestGKESandboxesExistingAdminKubeconfigIsNotTakenAsLeastPrivilege(t *testing.T) {
	t.Parallel()
	sys := newFakeSystem()
	opts := baseOptions(t)
	opts.Platform = PlatformGKESandboxes
	kubeconfigOut := filepath.Join(opts.DataDir, "out", "kubeconfig")
	opts.GKESandboxes = GKESandboxesOptions{
		Project:        "sandbox-proj",
		Location:       "us-central1-a",
		Cluster:        "grain-sandboxes",
		NodePool:       "kvm-pool",
		MachineType:    "n2-standard-8",
		KubeconfigPath: kubeconfigOut,
	}

	sys.on("Google Cloud SDK 500.0.0", nil, "gcloud", "--version")
	sys.on("Client Version: v1.31.0", nil, "kubectl", "version", "--client")
	sys.on("gke-gcloud-auth-plugin v0.5.9", nil, "gke-gcloud-auth-plugin", "--version")
	sys.on("RUNNING\n", nil, "gcloud",
		"container", "clusters", "describe", "grain-sandboxes",
		"--project=sandbox-proj", "--zone=us-central1-a", "--format=value(status)")
	sys.on("RUNNING\n", nil, "gcloud",
		"container", "node-pools", "describe", "kvm-pool",
		"--cluster=grain-sandboxes", "--project=sandbox-proj", "--zone=us-central1-a", "--format=value(status)")
	sys.on("kubeconfig entry generated", nil, "gcloud",
		"container", "clusters", "get-credentials", "grain-sandboxes",
		"--project=sandbox-proj", "--zone=us-central1-a")

	adminKube := filepath.Join(opts.DataDir, "gke-sandboxes", "admin.kubeconfig")
	sys.on("2/2", nil, "kubectl", "--kubeconfig="+adminKube,
		"-n", "kube-system", "get", "daemonset", "granule-device-plugin",
		"-o", "jsonpath={.status.numberReady}/{.status.desiredNumberScheduled}")
	sys.on("namespace/grain unchanged\nserviceaccount/grain unchanged\nrole.rbac.authorization.k8s.io/grain configured\nrolebinding.rbac.authorization.k8s.io/grain unchanged\n", nil,
		"kubectl", "--kubeconfig="+adminKube, "apply", "-f", filepath.Join(opts.DataDir, "gke-sandboxes", "rbac.yaml"))

	if err := os.MkdirAll(filepath.Dir(kubeconfigOut), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kubeconfigOut, []byte("apiVersion: v1\nkind: Config\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sys.on("yes\n", nil, "kubectl", "--kubeconfig="+kubeconfigOut,
		"-n", "grain", "auth", "can-i", "create", "pods")
	sys.on("yes\n", nil, "kubectl", "--kubeconfig="+kubeconfigOut,
		"-n", "grain", "auth", "can-i", "get", "secrets")
	// Minting is left unconfigured: the run must reach it, and stop there.

	result, err := Init(context.Background(), sys, opts)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if s, ok := stepStatus(result, "create grain namespace and least-privilege ServiceAccount RBAC"); !ok || s.Status != StepDone {
		t.Errorf("RBAC step with a Role re-configured: got %+v, want StepDone", s)
	}
	if s, ok := stepStatus(result, "write /var/lib/grain/kubeconfig"); !ok || s.Status != StepManual {
		t.Errorf("kubeconfig step over an admin kubeconfig: got %+v, want StepManual (re-minting, not skipped)", s)
	}
	minted := false
	for _, call := range sys.calls {
		if strings.Contains(call, "create token grain") {
			minted = true
		}
	}
	if !minted {
		t.Errorf("an admin kubeconfig was accepted without minting a ServiceAccount token; calls: %v", sys.calls)
	}
}
