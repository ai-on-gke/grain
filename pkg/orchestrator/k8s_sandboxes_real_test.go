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

package orchestrator_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/pkgcache"
)

// realK8sImage stands in for a deployment's own sandbox image, so it has
// to be one a grain sandbox can actually be hosted in. That rules out the
// alpine/git this used to name: every tool call goes out as `bash -c` with
// a GNU `timeout --kill-after` in front of it (mcp.SSHTools), and busybox
// has neither -- the suite got as far as its first run_command and came
// back with `exec: "bash": executable file not found in $PATH`.
// buildpack-deps' curl variant is the smallest official image with the
// same Debian userland Dockerfile.k8s-sandbox is built on, plus the wget
// the NetworkPolicy assertions at the end of this test reach for.
const (
	realK8sImage      = "buildpack-deps:bookworm-curl"
	realK8sProxyImage = "nginx:1.27-alpine"
)

// trimCalicoManifest writes a copy of the pinned Calico manifest with
// calico-node's 250m CPU request lowered to 50m so system pods leave budget
// for sandbox pods on 2- to 4-vCPU CI runners.
func trimCalicoManifest(t *testing.T, manifestPath string) (string, []string) {
	t.Helper()
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("reading GRAIN_CALICO_MANIFEST %q: %v", manifestPath, err)
	}
	trimmed := strings.ReplaceAll(string(raw), "cpu: 250m", "cpu: 50m")
	outPath := filepath.Join(t.TempDir(), "calico-trimmed.yaml")
	if err := os.WriteFile(outPath, []byte(trimmed), 0o600); err != nil {
		t.Fatalf("writing trimmed Calico manifest: %v", err)
	}
	seen := map[string]bool{}
	var images []string
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "image:" {
			img := strings.Trim(fields[1], `"'`)
			if img != "" && !seen[img] {
				seen[img] = true
				images = append(images, img)
			}
		}
	}
	return outPath, images
}

// loadRealK8sImages loads pre-pulled or cached workload images into the kind
// cluster so kubelet uses containerd's local store (imagePullPolicy:
// IfNotPresent) instead of pulling over the network.
//
// Exporting with `docker save --platform linux/<arch>` + `kind load
// image-archive` avoids `kind load docker-image` failing under Docker's
// containerd snapshotter when only the host architecture's layers were pulled
// from a multi-arch manifest index.
func loadRealK8sImages(t *testing.T, cluster string, calicoImages []string) {
	t.Helper()
	if archive := strings.TrimSpace(os.Getenv("GRAIN_K8S_IMAGE_ARCHIVE")); archive != "" {
		out, err := exec.Command("kind", "load", "image-archive", archive, "--name", cluster).CombinedOutput()
		if err != nil {
			t.Fatalf("kind load image-archive %s: %v\n%s", archive, err, out)
		}
		return
	}
	candidates := append([]string{}, calicoImages...)
	candidates = append(candidates, realK8sImage, realK8sProxyImage)
	var present []string
	seen := map[string]bool{}
	for _, img := range candidates {
		if seen[img] {
			continue
		}
		seen[img] = true
		if exec.Command("docker", "image", "inspect", img).Run() == nil {
			present = append(present, img)
		}
	}
	if len(present) == 0 {
		return
	}
	archive := filepath.Join(t.TempDir(), "k8s-workload-images.tar")
	saveArgs := append([]string{"save", "--platform", "linux/" + runtime.GOARCH, "-o", archive}, present...)
	if out, err := exec.Command("docker", saveArgs...).CombinedOutput(); err != nil {
		fallbackArgs := append([]string{"save", "-o", archive}, present...)
		if out2, err2 := exec.Command("docker", fallbackArgs...).CombinedOutput(); err2 != nil {
			t.Fatalf("docker save %v: %v\n%s\nfallback: %v\n%s", present, err, out, err2, out2)
		}
	}
	if out, err := exec.Command("kind", "load", "image-archive", archive, "--name", cluster).CombinedOutput(); err != nil {
		t.Fatalf("kind load image-archive: %v\n%s", err, out)
	}
	_ = os.Remove(archive)
}

func realK8sPrereqs(t *testing.T) {
	t.Helper()
	if os.Getenv("GRAIN_REAL_K8S_TESTS") == "" {
		t.Skip("set GRAIN_REAL_K8S_TESTS=1 to run the real kind suite")
	}
	for _, binary := range []string{"docker", "kind", "kubectl"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Skipf("%s is unavailable: %v", binary, err)
		}
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("docker daemon is unavailable: %v", err)
	}
}

func realKubectl(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("kubectl", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("kubectl %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// stopShippedDaemon scales deploy/kubernetes/grain.yaml's own Deployment
// to nothing and does not return until no pod of it is left on the node.
//
// It exists because a *running* grain daemon in this cluster is a second
// reaper racing this test: cmd/grain's daemon calls
// K8sSandboxes.ReapOrphans at startup, which deletes every sandbox pod
// in its namespace that that process is not itself holding -- every pod
// this test creates. When the reap landed decided which assertion failed
// (a first Acquire whose pod came back "Missing", a tool call into a pod
// that had gone, an isolated sandbox that never came up), and whether it
// landed at all came down to whether several hundred megabytes of
// docker.io/grainai/grain:main finished pulling before the suite
// finished running -- which is why this job was intermittent rather than
// simply red.
//
// The pod is force-deleted rather than waited out. An ordinary delete
// leaves it Terminating for as long as the image pull it is in the
// middle of takes to unwind -- over two minutes when this was measured,
// still holding the whole CPU it requested -- and nothing is running
// inside it to be forced off a node, which is the case immediate
// deletion is normally the wrong answer for. The loop retries because
// the delete is racing the ReplicaSet controller's own, and because a
// busy kind cluster answers the odd call with an etcd timeout; it is the
// emptiness of the list that ends it, not any one call succeeding.
func stopShippedDaemon(t *testing.T) {
	t.Helper()
	const selector = "app.kubernetes.io/name=grain"
	realKubectl(t, "scale", "deployment/grain", "-n", "grain", "--replicas=0")
	deadline := time.Now().Add(2 * time.Minute)
	for {
		out, err := exec.Command("kubectl", "delete", "pod", "-l", selector, "-n", "grain",
			"--force", "--grace-period=0", "--ignore-not-found").CombinedOutput()
		if err != nil {
			// Not "kubectl ...": the job that runs this suite names the
			// phase a failure stopped at by grepping the log for that
			// prefix (.grain/workflows/tests.yml), and a call this loop
			// is about to make again is not a phase that stopped.
			t.Logf("retrying the delete of the shipped daemon's pod: %v\n%s", err, out)
		}
		left := realKubectl(t, "get", "pods", "-l", selector, "-n", "grain", "-o", "name")
		if left == "" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the shipped grain Deployment still has pods after being scaled to zero: %s", left)
		}
		time.Sleep(time.Second)
	}
}

func realTool(t *testing.T, sb orchestrator.Sandbox, name string) *mcp.Tool {
	t.Helper()
	tools, err := sb.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i := range tools {
		if tools[i].Name == name {
			return &tools[i]
		}
	}
	t.Fatalf("Tools returned no %s", name)
	return nil
}

func writeKindControlPlanePatches(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, cpu := range map[string]string{
		"kube-apiserver":          "50m",
		"kube-controller-manager": "50m",
		"kube-scheduler":          "25m",
		"etcd":                    "50m",
	} {
		patch := fmt.Sprintf("apiVersion: v1\nkind: Pod\nmetadata:\n  name: %s\n  namespace: kube-system\nspec:\n  containers:\n    - name: %s\n      resources:\n        requests:\n          cpu: %s\n", name, name, cpu)
		if err := os.WriteFile(filepath.Join(dir, name+"+strategic.yaml"), []byte(patch), 0o600); err != nil {
			t.Fatalf("writing kubeadm patch for %s: %v", name, err)
		}
	}
	return dir
}

// TestK8sSandboxesAgainstARealKindCluster is the Kubernetes counterpart
// to TestGranuleSandboxesAgainstARealDockerBackedVM: manifests, selectors,
// exec stdin and kubelet status all meet a real API server and runtime.
func TestK8sSandboxesAgainstARealKindCluster(t *testing.T) {
	t.Parallel()
	realK8sPrereqs(t)
	calico := os.Getenv("GRAIN_CALICO_MANIFEST")
	if calico == "" {
		t.Fatal("GRAIN_CALICO_MANIFEST must name the pinned Calico manifest")
	}
	trimmedCalico, calicoImages := trimCalicoManifest(t, calico)

	cluster := fmt.Sprintf("grain-k8s-%d", os.Getpid())
	patchDir := writeKindControlPlanePatches(t)
	kindConfig := fmt.Sprintf("kind: Cluster\napiVersion: kind.x-k8s.io/v1alpha4\nnetworking:\n  disableDefaultCNI: true\nkubeadmConfigPatches:\n  - |\n    kind: InitConfiguration\n    patches:\n      directory: /etc/kubeadm/patches\nnodes:\n  - role: control-plane\n    extraMounts:\n      - hostPath: %s\n        containerPath: /etc/kubeadm/patches\n        readOnly: true\n", patchDir)
	createArgs := []string{"create", "cluster", "--name", cluster, "--config", "-"}
	if nodeImg := strings.TrimSpace(os.Getenv("GRAIN_KIND_NODE_IMAGE")); nodeImg != "" {
		createArgs = append(createArgs, "--image", nodeImg)
	}
	create := exec.Command("kind", createArgs...)
	create.Stdin = strings.NewReader(kindConfig)
	if out, err := create.CombinedOutput(); err != nil {
		t.Fatalf("creating kind cluster: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("kind", "delete", "cluster", "--name", cluster).Run() })

	loadRealK8sImages(t, cluster, calicoImages)
	realKubectl(t, "scale", "deployment/coredns", "-n", "kube-system", "--replicas=1")
	realKubectl(t, "apply", "-f", trimmedCalico)
	// `kubectl rollout status` rather than `kubectl wait --for=condition=Ready
	// pod -l k8s-app=calico-node`, which was here and is a race: the apply
	// above creates the DaemonSet, not its pod, and `kubectl wait` against a
	// selector that matches nothing *yet* fails immediately with "no matching
	// resources found" rather than waiting for one to show up. It passed on a
	// GitHub runner and failed about half the time in a grain CI sandbox,
	// which only means the gap between apply and wait landed differently
	// there. rollout status waits on the DaemonSet, which does exist the
	// moment the apply returns, and is satisfied by the same pod readiness.
	// Five minutes rather than three: what this waits out is three Calico
	// images arriving on a node that has never seen them, so it is a
	// bound on a download rather than on anything this test measures, and
	// the job's own timeout is what catches a cluster that is really
	// stuck.
	realKubectl(t, "rollout", "status", "daemonset/calico-node", "-n", "kube-system", "--timeout=5m")

	_, here, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(here), "../.."))
	realKubectl(t, "apply", "-f", filepath.Join(root, "deploy/kubernetes/grain.yaml"))
	// Applied as shipped -- that the file applies at all, and that the
	// Role in it grants what the assertion below asks for, is the point
	// of applying it -- and then stripped of the one part of itself this
	// cluster must not actually run. See stopShippedDaemon: the daemon
	// that Deployment starts reaps the sandbox pods this test is
	// asserting on. Scaling it away also gives those pods back the whole
	// CPU it requests, and stops its image pull competing for the link
	// with realK8sImage's.
	stopShippedDaemon(t)
	realKubectl(t, "apply", "-f", filepath.Join(root, "deploy/kubernetes/networkpolicy.yaml"))
	if got := realKubectl(t, "auth", "can-i", "create", "pods/exec", "-n", "grain", "--as=system:serviceaccount:grain:grain"); got != "yes" {
		t.Fatalf("grain RBAC cannot create pods/exec: %q", got)
	}

	shape := orchestrator.Shape{CPUs: 1, MemoryMB: 128, DiskGB: 1}
	// Three minutes rather than one: this is the first pod in a cluster
	// that was created seconds ago, so its readiness includes pulling
	// realK8sImage into the node from scratch. A minute was enough while
	// that image was a 35MB busybox and is a coin flip for a 250MB Debian
	// userland -- half the runs of this suite in a grain CI sandbox ended
	// here, in a pod that was still Pending because the pull was still
	// going. Nothing is being measured by this bound; the one timing
	// assertion in this test is the bad-image one below, which wants a
	// failure to be quick and gets its own short deadline.
	k := orchestrator.NewK8sSandboxes(orchestrator.K8sConfig{
		Namespace: "grain", Image: realK8sImage, Workspace: "/tmp",
		CPUOvercommit: 4,
		ReadyTimeout:  3 * time.Minute, ReadyPollInterval: time.Second,
	})
	sb, err := k.Acquire(context.Background(), "real-1", shape)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	run := realTool(t, sb, "run_command")
	result := run.Handler(context.Background(), map[string]any{"command": "printf real-kubernetes-tool-call"})
	if result.IsError || !strings.Contains(result.Text, "real-kubernetes-tool-call") {
		t.Fatalf("real tool call: %s", result.Text)
	}
	if err := sb.ConfigureGitCredentials(context.Background(), "http://proxy.invalid/owner/repo.git", "secret",
		mcp.GitIdentity{Name: "grain test", Email: "grain@example.test"}); err != nil {
		t.Fatalf("ConfigureGitCredentials: %v", err)
	}
	placer, ok := sb.(orchestrator.SandboxPlacer)
	if !ok {
		t.Fatal("Kubernetes sandbox does not implement SandboxPlacer")
	}
	if err := placer.PlaceFile(context.Background(), "/tmp/placed", "placed through kubectl stdin", "600"); err != nil {
		t.Fatalf("PlaceFile: %v", err)
	}
	result = run.Handler(context.Background(), map[string]any{"command": "test \"$(cat /tmp/placed)\" = 'placed through kubectl stdin' && test \"$(stat -c %a /tmp/placed)\" = 600"})
	if result.IsError {
		t.Fatalf("checking placed file: %s", result.Text)
	}
	// With deploy/kubernetes/networkpolicy.yaml applied on Calico, even an open-network
	// sandbox pod cannot reach the cloud metadata server (169.254.169.254).
	result = run.Handler(context.Background(), map[string]any{"command": "! wget -q -T 3 -O/dev/null http://169.254.169.254/computeMetadata/v1/"})
	if result.IsError {
		t.Fatalf("open-network Kubernetes sandbox reached cloud metadata 169.254.169.254: %s", result.Text)
	}
	if err := sb.Release(context.Background()); err != nil {
		t.Fatalf("Release: %v", err)
	}
	realKubectl(t, "wait", "--for=delete", "pod/grain-sandbox-real-1", "-n", "grain", "--timeout=30s")

	realK8sPackageCache(t, shape)

	if _, err := k.Acquire(context.Background(), "orphan-1", shape); err != nil {
		t.Fatalf("Acquire orphan: %v", err)
	}
	reaper := orchestrator.NewK8sSandboxes(orchestrator.K8sConfig{Namespace: "grain", Image: realK8sImage})
	if n, err := reaper.ReapOrphans(context.Background()); err != nil || n != 1 {
		t.Fatalf("ReapOrphans = %d, %v; want 1, nil", n, err)
	}
	// The sweep deletes without waiting (K8sSandboxes.ReapOrphans ->
	// Client.Delete(wait=false)), so the count above says the API server
	// accepted the deletion and not that the pod has left the node. This
	// asserts the rest of it -- and keeps the reaped pod's whole CPU from
	// being still spoken for when the next one asks for one of its own,
	// which on a node with room for two of these is the difference
	// between the pod below being scheduled and it sitting Unschedulable
	// with no kubelet verdict on it at all.
	realKubectl(t, "wait", "--for=delete", "pod/grain-sandbox-orphan-1", "-n", "grain", "--timeout=60s")

	bad := orchestrator.NewK8sSandboxes(orchestrator.K8sConfig{
		Namespace: "grain", Image: "invalid.invalid/grain/image-does-not-exist:never",
		ReadyPollInterval: time.Second,
	})
	started := time.Now()
	_, err = bad.Acquire(context.Background(), "bad-image", shape)
	if err == nil || (!strings.Contains(err.Error(), "ErrImagePull") && !strings.Contains(err.Error(), "ImagePullBackOff")) {
		t.Fatalf("bad-image Acquire error = %v, want kubelet image-pull reason", err)
	}
	if elapsed := time.Since(started); elapsed >= time.Minute {
		t.Fatalf("bad-image Acquire took %s, want a quick failure", elapsed)
	}
	// A failed Acquire deletes what it created (K8sSandboxes.deleteQuietly),
	// again without waiting for the pod to go. Asserted here for the
	// reason the orphan's deletion is asserted above: it is a promise
	// this backend makes -- a failed dispatch leaves nothing behind --
	// and the pod below wants the CPU this one is holding.
	// Two minutes rather than the minute the orphan above gets: this is
	// the one pod here that may be in the middle of a pull when it is
	// deleted, and a kubelet finishes the pull it is in before it lets go
	// of the pod.
	realKubectl(t, "wait", "--for=delete", "pod/grain-sandbox-bad-image", "-n", "grain", "--timeout=2m")

	realKubectl(t, "run", "grain-proxy", "-n", "grain", "--image="+realK8sProxyImage, "--image-pull-policy=IfNotPresent", "--labels=app.kubernetes.io/name=grain")
	realKubectl(t, "wait", "pod/grain-proxy", "-n", "grain", "--for=condition=Ready", "--timeout=2m")
	proxyIP := realKubectl(t, "get", "pod/grain-proxy", "-n", "grain", "-o", "jsonpath={.status.podIP}")
	isolated := orchestrator.NewK8sSandboxes(orchestrator.K8sConfig{
		Namespace: "grain", Image: realK8sImage, Workspace: "/tmp",
		CPUOvercommit: 4,
		// Three minutes for the reason the first Acquire above has three:
		// the image is in the node by now, but this pod is asking for a
		// whole CPU on a node that is still finishing with the pods
		// before it.
		NetworkPolicyEnforced: true, ReadyTimeout: 3 * time.Minute, ReadyPollInterval: time.Second,
	})
	netSB, err := isolated.Acquire(context.Background(), "network-none",
		orchestrator.Shape{CPUs: 1, MemoryMB: 128, DiskGB: 1, NetworkAccess: model.NetworkNone})
	if err != nil {
		t.Fatalf("Acquire isolated sandbox: %v", err)
	}
	defer netSB.Release(context.Background())
	run = realTool(t, netSB, "run_command")
	result = run.Handler(context.Background(), map[string]any{"command": "wget -q -T 5 -O- http://" + proxyIP})
	if result.IsError {
		t.Fatalf("isolated sandbox cannot reach grain proxy exception: %s", result.Text)
	}
	result = run.Handler(context.Background(), map[string]any{"command": "! wget -q -T 5 -O/dev/null http://1.1.1.1"})
	if result.IsError {
		t.Fatalf("isolated sandbox can reach the internet: %s", result.Text)
	}
}

// realK8sPackageCache builds one pod pointed at a package cache and checks,
// from inside it, what the kubelet made of packageCacheSetup: every file
// under /etc a running pod has no root to write, present, read-only, and
// in the case of the CA bundle, the image's own with the cache's appended.
func realK8sPackageCache(t *testing.T, shape orchestrator.Shape) {
	t.Helper()
	base := "http://10.241.0.1:9729"
	caPEM := realTestCA(t)
	k := orchestrator.NewK8sSandboxes(orchestrator.K8sConfig{
		Namespace: "grain", Image: realK8sImage, Workspace: "/tmp",
		CPUOvercommit: 4,
		ReadyTimeout:  3 * time.Minute, ReadyPollInterval: time.Second,
	})
	k.SetPackageCache(func(context.Context) string { return base }, func(context.Context) []byte { return caPEM })
	sb, err := k.Acquire(context.Background(), "cached-1", shape)
	if err != nil {
		t.Fatalf("Acquire with a package cache: %v", err)
	}
	configured, err := orchestrator.ConfigurePackageCacheTransparent(context.Background(), sb, base, caPEM, "")
	if err != nil || !configured {
		t.Fatalf("ConfigurePackageCacheTransparent = %v, %v; want configured", configured, err)
	}
	run := realTool(t, sb, "run_command")
	check := strings.Join([]string{
		"grep -q 10.241.0.1:9729 " + pkgcache.PipConfigPath,
		"grep -q 10.241.0.1:9729 " + pkgcache.AptConfigPath,
		"grep -q 10.241.0.1:9729 " + pkgcache.GHCRHostsConfigPath,
		"grep -q 10.241.0.1:9729 " + pkgcache.DockerConfigPath,
		"grep -q 'BEGIN CERTIFICATE' " + pkgcache.CACertPath,
		// The image's own roots are still there, and the cache's is last.
		"test $(grep -c 'BEGIN CERTIFICATE' " + orchestrator.CABundlePath + ") -gt 1",
		"tail -n " + fmt.Sprint(strings.Count(string(caPEM), "\n")) + " " + orchestrator.CABundlePath + " | cmp -s " + pkgcache.CACertPath + " -",
		"! sh -c 'echo x >>" + pkgcache.PipConfigPath + "' 2>/dev/null",
	}, " && ")
	if result := run.Handler(context.Background(), map[string]any{"command": check}); result.IsError {
		t.Fatalf("checking the package-cache files in the pod: %s", result.Text)
	}
	if err := sb.Release(context.Background()); err != nil {
		t.Fatalf("Release: %v", err)
	}
	realKubectl(t, "wait", "--for=delete", "pod/grain-sandbox-cached-1", "-n", "grain", "--timeout=30s")
}

// realTestCA is a throwaway self-signed certificate: the bundle the pod's
// own curl reads has to stay parseable.
func realTestCA(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "grain test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
