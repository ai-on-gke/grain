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
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/granule"
	granuleagent "github.com/bwsalmon/granule/pkg/agent"
	"github.com/bwsalmon/granule/pkg/guestexec"
	"github.com/bwsalmon/granule/pkg/relay"
	"github.com/bwsalmon/granule/pkg/sandbox"
)

type funcPodResolver struct {
	resolve func(ctx context.Context, target string) (string, error)
}

func (f funcPodResolver) Resolve(ctx context.Context, target string) (string, error) {
	return f.resolve(ctx, target)
}

func stubGKESandboxesRelayServer(t *testing.T, sys *fakeSystem, opts *Options, unchanged bool) {
	t.Helper()
	scratchDir := filepath.Join(opts.DataDir, "gke-sandboxes")
	pki, err := granule.MintRelayPKI(scratchDir, "grain")
	if err != nil {
		t.Fatalf("MintRelayPKI: %v", err)
	}
	ca, err := granule.NewSandboxCA()
	if err != nil {
		t.Fatalf("NewSandboxCA: %v", err)
	}
	access, err := ca.MintSandbox(gkeSandboxesVerifyPodName)
	if err != nil {
		t.Fatalf("MintSandbox: %v", err)
	}
	sbTLS, err := access.ServerTLSConfig()
	if err != nil {
		t.Fatalf("access.ServerTLSConfig: %v", err)
	}
	u, err := user.Current()
	if err != nil {
		t.Fatalf("user.Current: %v", err)
	}
	srvCtx, srvCancel := context.WithCancel(context.Background())
	t.Cleanup(srvCancel)

	sbSrv := sandbox.NewServer(sandbox.ServerConfig{
		SandboxID: gkeSandboxesVerifyPodName,
		Token:     access.Token,
		TLSConfig: sbTLS,
		Guest: guestexec.Config{
			User: u.Username,
		},
		DialGuest: func(dialCtx context.Context) (net.Conn, error) {
			if err := dialCtx.Err(); err != nil {
				return nil, err
			}
			c1, c2 := net.Pipe()
			go func() {
				defer c2.Close()
				_ = granuleagent.Serve(srvCtx, c2)
			}()
			return c1, nil
		},
	})
	sbLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen sandbox: %v", err)
	}
	t.Cleanup(func() { _ = sbLn.Close() })
	go func() { _ = sbSrv.Serve(srvCtx, sbLn) }()

	sbHost, sbPortStr, err := net.SplitHostPort(sbLn.Addr().String())
	if err != nil {
		t.Fatalf("SplitHostPort sandbox: %v", err)
	}
	sbPort, err := strconv.Atoi(sbPortStr)
	if err != nil {
		t.Fatalf("Atoi sandbox port: %v", err)
	}

	relayTLS, err := pki.ServerTLSConfig()
	if err != nil {
		t.Fatalf("pki.ServerTLSConfig: %v", err)
	}
	relayLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen relay: %v", err)
	}
	t.Cleanup(func() { _ = relayLn.Close() })

	relaySrv, err := relay.New(relay.Config{
		Token:       pki.Token,
		TLSConfig:   relayTLS,
		SandboxPort: sbPort,
		Resolver: funcPodResolver{
			resolve: func(_ context.Context, name string) (string, error) {
				if name == gkeSandboxesVerifyPodName {
					return sbHost, nil
				}
				return "", fmt.Errorf("unexpected sandbox %q", name)
			},
		},
	})
	if err != nil {
		t.Fatalf("relay.New: %v", err)
	}
	go func() { _ = relaySrv.Serve(srvCtx, relayLn) }()

	_, portStr, err := net.SplitHostPort(relayLn.Addr().String())
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", relayLn.Addr().String(), err)
	}
	relayPort, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("Atoi(%q): %v", portStr, err)
	}

	opts.GKESandboxes.RelayPort = relayPort
	opts.GKESandboxes.VerifySandboxAccess = access
	opts.GKESandboxes.VerifyExecScript = "echo verified-over-relay"

	adminKube := filepath.Join(scratchDir, "admin.kubeconfig")
	relayManifest := filepath.Join(scratchDir, "relay.yaml")
	if unchanged {
		relayCfgPath := granule.RelayConfigPath(scratchDir)
		if err := granule.SaveRelayConfig(relayCfgPath, granule.RelayConfig{
			Addr:       net.JoinHostPort("127.0.0.1", strconv.Itoa(relayPort)),
			CAFile:     pki.CAFile,
			CertFile:   pki.ClientCert,
			KeyFile:    pki.ClientKey,
			TokenFile:  pki.TokenFile,
			ServerName: DefaultGKESandboxesRelayService,
		}); err != nil {
			t.Fatalf("SaveRelayConfig: %v", err)
		}
		sys.on("secret/grain-sandbox-relay-tls unchanged\nserviceaccount/grain-sandbox-relay unchanged\nrole.rbac.authorization.k8s.io/grain-sandbox-relay unchanged\nrolebinding.rbac.authorization.k8s.io/grain-sandbox-relay unchanged\ndeployment.apps/grain-sandbox-relay unchanged\nservice/grain-sandbox-relay unchanged\nnetworkpolicy.networking.k8s.io/granule-sandbox-access unchanged\n", nil,
			"kubectl", "--kubeconfig="+adminKube, "apply", "-f", relayManifest)
	} else {
		sys.on("secret/grain-sandbox-relay-tls created\nserviceaccount/grain-sandbox-relay created\nrole.rbac.authorization.k8s.io/grain-sandbox-relay created\nrolebinding.rbac.authorization.k8s.io/grain-sandbox-relay created\ndeployment.apps/grain-sandbox-relay created\nservice/grain-sandbox-relay created\nnetworkpolicy.networking.k8s.io/granule-sandbox-access created\n", nil,
			"kubectl", "--kubeconfig="+adminKube, "apply", "-f", relayManifest)
	}
	sys.on("deployment \"grain-sandbox-relay\" successfully rolled out", nil,
		"kubectl", "--kubeconfig="+adminKube, "-n", "grain", "rollout", "status", "deployment/grain-sandbox-relay", "--timeout=2m")
	sys.on("127.0.0.1\n", nil,
		"kubectl", "--kubeconfig="+adminKube, "-n", "grain", "get", "service", "grain-sandbox-relay",
		"-o", "jsonpath={.status.loadBalancer.ingress[0].ip}{.status.loadBalancer.ingress[0].hostname}")
}

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
		"deploy granule sandbox relay and sandbox NetworkPolicy",
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
		"pods, pods/exec, pods/log, secrets (create/delete)",
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

	relayStep, _ := stepStatus(result, "deploy granule sandbox relay and sandbox NetworkPolicy")
	for _, wantSub := range []string{
		"grain-sandbox-relay-tls",
		"grain-sandbox-relay",
		"granule-sandbox-access",
		"relay.json",
	} {
		if !strings.Contains(relayStep.Detail, wantSub) {
			t.Errorf("relay Detail %q missing %q", relayStep.Detail, wantSub)
		}
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
	relayManifest := filepath.Join(opts.DataDir, "gke-sandboxes", "relay.yaml")
	relayConfigPath := filepath.Join(opts.DataDir, "gke-sandboxes", "relay.json")
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

	// Relay + NetworkPolicy deployment and in-process granule relay/sandbox server.
	stubGKESandboxesRelayServer(t, sys, &opts, false)

	// Verify script + throwaway sandbox.
	sys.on("OK: k8s-granule verified", nil, verifyScript, "--keep")
	sys.on("", nil, "kubectl", "--kubeconfig="+kubeconfigOut,
		"-n", "grain", "delete", "pod/"+gkeSandboxesVerifyPodName, "--ignore-not-found", "--wait=true", "--grace-period=1")
	sys.on("secret/grain-init-gke-sandboxes-verify-access created\npod/grain-init-gke-sandboxes-verify created", nil, "kubectl", "--kubeconfig="+kubeconfigOut,
		"-n", "grain", "apply", "-f", verifyPodManifest)
	sys.on("pod/grain-init-gke-sandboxes-verify condition met", nil, "kubectl", "--kubeconfig="+kubeconfigOut,
		"-n", "grain", "wait", "--for=condition=Ready", "pod/"+gkeSandboxesVerifyPodName, "--timeout=5m")

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
		"deploy granule sandbox relay and sandbox NetworkPolicy",
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
		`resources: ["secrets"]`,
		`verbs: ["create", "delete"]`,
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

	// Verify the relay manifest has Secret/grain-sandbox-relay-tls, least-privilege Role,
	// Deployment/grain-sandbox-relay, LoadBalancer Service, and NetworkPolicy/granule-sandbox-access.
	relayBytes, err := os.ReadFile(relayManifest)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", relayManifest, err)
	}
	for _, want := range []string{
		"name: grain-sandbox-relay-tls",
		"kind: Deployment",
		"name: grain-sandbox-relay",
		`command: ["granule", "relay"]`,
		"- -selector=granule.gke.io/sandbox=true",
		"- -name-label=granule.gke.io/sandbox-name",
		"- -sandbox-port=7443",
		"kind: Service",
		"type: LoadBalancer",
		"kind: NetworkPolicy",
		"name: granule-sandbox-access",
		`granule.gke.io/sandbox: "true"`,
		"app.kubernetes.io/name: grain-sandbox-relay",
		"app.kubernetes.io/name: grain",
		"port: 7443",
	} {
		if !strings.Contains(string(relayBytes), want) {
			t.Errorf("relay.yaml missing %q:\n%s", want, string(relayBytes))
		}
	}

	// Verify relay.json was written with mode 0600 and valid credentials.
	if fi, err := os.Stat(relayConfigPath); err != nil {
		t.Fatalf("Stat(%s): %v", relayConfigPath, err)
	} else if mode := fi.Mode().Perm(); mode != 0o600 {
		t.Errorf("%s mode = %o, want 600", relayConfigPath, mode)
	}
	relayCfg, err := granule.LoadRelayConfig(relayConfigPath)
	if err != nil {
		t.Fatalf("LoadRelayConfig(%s): %v", relayConfigPath, err)
	}
	if !strings.HasPrefix(relayCfg.Addr, "127.0.0.1:") {
		t.Errorf("relayCfg.Addr = %q, want 127.0.0.1:<port>", relayCfg.Addr)
	}

	// Verify throwaway sandbox manifest uses Secret volume and TCP listen mode.
	verifyBytes, err := os.ReadFile(verifyPodManifest)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", verifyPodManifest, err)
	}
	for _, want := range []string{
		"name: grain-init-gke-sandboxes-verify-access",
		`granule.gke.io/sandbox: "true"`,
		"granule.gke.io/sandbox-name: grain-init-gke-sandboxes-verify",
		"name: GRANULE_ACCESS_LISTEN",
		"value: tcp://0.0.0.0:7443",
		"secretName: grain-init-gke-sandboxes-verify-access",
	} {
		if !strings.Contains(string(verifyBytes), want) {
			t.Errorf("verify-sandbox-pod.yaml missing %q:\n%s", want, string(verifyBytes))
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

	// Cluster, node pool, device plugin, RBAC, kubeconfig, and relay all already exist!
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

	stubGKESandboxesRelayServer(t, sys, &opts, true)

	sys.on("OK: k8s-granule verified", nil, verifyScript, "--keep")
	sys.on("", nil, "kubectl", "--kubeconfig="+kubeconfigOut,
		"-n", "grain", "delete", "pod/"+gkeSandboxesVerifyPodName, "--ignore-not-found", "--wait=true", "--grace-period=1")
	sys.on("pod/grain-init-gke-sandboxes-verify created", nil, "kubectl", "--kubeconfig="+kubeconfigOut,
		"-n", "grain", "apply", "-f", verifyPodManifest)
	sys.on("pod/grain-init-gke-sandboxes-verify condition met", nil, "kubectl", "--kubeconfig="+kubeconfigOut,
		"-n", "grain", "wait", "--for=condition=Ready", "pod/"+gkeSandboxesVerifyPodName, "--timeout=5m")

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
		"deploy granule sandbox relay and sandbox NetworkPolicy",
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
