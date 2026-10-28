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
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/ai-on-gke/grain/pkg/granule"
	"github.com/ai-on-gke/grain/pkg/k8s"
	"github.com/bwsalmon/granule/pkg/sandbox"
)

// Defaults for `grain init -platform=gke-sandboxes`.
const (
	DefaultGKESandboxesProject         = "grain-sandboxes"
	DefaultGKESandboxesLocation        = "us-central1-a"
	DefaultGKESandboxesCluster         = "grain-sandboxes"
	DefaultGKESandboxesNodePool        = "kvm-pool"
	DefaultGKESandboxesMachineType     = "n2-standard-8"
	DefaultGKESandboxesDiskTypePD      = "pd-balanced"
	DefaultGKESandboxesDiskTypeHyper   = "hyperdisk-balanced"
	DefaultGKESandboxesDiskSizeGB      = 100
	DefaultGKESandboxesImageType       = "UBUNTU_CONTAINERD"
	DefaultGKESandboxesNumNodes        = 1
	DefaultGKESandboxesNamespace       = DefaultK8sNamespace
	DefaultGKESandboxesServiceAccount  = "grain"
	DefaultGKESandboxesKubeconfig      = "/var/lib/grain/kubeconfig"
	DefaultGKESandboxesGuestImage      = "ghcr.io/bwsalmon/grain/guest:main"
	DefaultGKESandboxesGitProxyURL     = "http://127.0.0.1:9728"
	DefaultGKESandboxesPackageCacheURL = "http://127.0.0.1:9729"
	DefaultGKESandboxesVerifyRepo      = "https://github.com/ai-on-gke/grain.git"
	DefaultGKESandboxesTokenDuration   = "8760h"
	DefaultGKESandboxesRelayDeployment = "grain-sandbox-relay"
	DefaultGKESandboxesRelayService    = "grain-sandbox-relay"
	DefaultGKESandboxesRelaySecret     = "grain-sandbox-relay-tls"
	DefaultGKESandboxesNetworkPolicy   = "granule-sandbox-access"
	DefaultGKESandboxesRelayPort       = 443

	gkeSandboxesVerifyPodName    = "grain-init-gke-sandboxes-verify"
	gkeSandboxesVerifySecretName = "grain-init-gke-sandboxes-verify-access"
)

// GKESandboxesOptions configures `grain init -platform=gke-sandboxes`:
// bringing up or reusing a GKE Standard cluster in the sandbox project
// with a nested-virtualization node pool, installing granule's KVM
// device plugin, creating the privileged `grain` namespace and
// least-privilege `grain` ServiceAccount + Role/RoleBinding (`pods`,
// `pods/exec`, `pods/log`), writing `/var/lib/grain/kubeconfig` for the
// off-cluster grain daemon, and verifying with
// `scripts/platform-verify/k8s-granule.sh` and one throwaway sandbox
// that clones through the git proxy and fetches through the package cache.
type GKESandboxesOptions struct {
	// Project is the GCP project ID where the GKE sandbox cluster lives.
	// Empty means DefaultGKESandboxesProject.
	Project string
	// Location is the GCP zone (e.g. "us-central1-a") or region (e.g.
	// "us-central1") for the cluster. Empty means DefaultGKESandboxesLocation.
	Location string
	// Cluster is the GKE Standard cluster name. Empty means
	// DefaultGKESandboxesCluster.
	Cluster string
	// NodePool is the nested-virtualization node pool name. Empty means
	// DefaultGKESandboxesNodePool.
	NodePool string
	// MachineType is the GCE machine type for the nested-virtualization
	// node pool (e.g. "c4-standard-96-lssd" or "n2-standard-8"). Empty
	// means DefaultGKESandboxesMachineType.
	MachineType string
	// DiskType is the boot disk type for the node pool. Empty defaults
	// to "hyperdisk-balanced" when MachineType begins with "c4-" and
	// "pd-balanced" otherwise.
	DiskType string
	// DiskSizeGB is the boot disk size in GiB. Zero means
	// DefaultGKESandboxesDiskSizeGB.
	DiskSizeGB int
	// ImageType is the node image type (nested virtualization on GKE
	// requires UBUNTU_CONTAINERD). Empty means DefaultGKESandboxesImageType.
	ImageType string
	// NumNodes is the initial node count per zone in the nested-virt pool.
	// Zero means DefaultGKESandboxesNumNodes.
	NumNodes int
	// MinNodes and MaxNodes enable cluster autoscaling on the node pool
	// when MaxNodes > 0.
	MinNodes int
	MaxNodes int
	// Namespace is the Kubernetes namespace created for sandbox pods.
	// Empty means DefaultGKESandboxesNamespace ("grain").
	Namespace string
	// ServiceAccount is the least-privilege Kubernetes ServiceAccount
	// created in Namespace and written into KubeconfigPath. Empty means
	// DefaultGKESandboxesServiceAccount ("grain").
	ServiceAccount string
	// KubeconfigPath is where the off-cluster controller's self-contained
	// kubeconfig is written. Empty means DefaultGKESandboxesKubeconfig
	// ("/var/lib/grain/kubeconfig").
	KubeconfigPath string
	// DevicePluginManifest is the path to granule's KVM device plugin
	// DaemonSet manifest. Empty means
	// <RepoRoot>/third_party/granule/deploy/k8s/kvm-device-plugin.yaml.
	DevicePluginManifest string
	// VerifyScript is the path to scripts/platform-verify/k8s-granule.sh.
	// Empty means <RepoRoot>/scripts/platform-verify/k8s-granule.sh.
	VerifyScript string
	// GuestImage is the packed granule guest image used by the
	// verification steps. Empty means DefaultGKESandboxesGuestImage.
	GuestImage string
	// GitProxyURL is the git proxy URL tested from inside the throwaway
	// verification sandbox. Empty means DefaultGKESandboxesGitProxyURL.
	GitProxyURL string
	// PackageCacheURL is the package cache URL tested from inside the
	// throwaway verification sandbox. Empty means
	// DefaultGKESandboxesPackageCacheURL.
	PackageCacheURL string
	// VerifyCloneRepo is the repository URL cloned through GitProxyURL
	// during the throwaway sandbox verification step. Empty means
	// DefaultGKESandboxesVerifyRepo.
	VerifyCloneRepo string
	// TokenDuration is the `--duration` passed to `kubectl create token`
	// when minting the ServiceAccount token for KubeconfigPath. Empty
	// means DefaultGKESandboxesTokenDuration ("8760h").
	TokenDuration string
	// RelayDir is the local directory where relay mTLS PKI, token, and
	// relay.json are written. Empty defaults to <DataDir>/gke-sandboxes.
	RelayDir string
	// RelayImage is the container image used by Deployment/grain-sandbox-relay.
	// Empty defaults to GuestImage.
	RelayImage string
	// RelayPort is the external LoadBalancer port on Service/grain-sandbox-relay.
	// Zero or negative defaults to DefaultGKESandboxesRelayPort (443).
	RelayPort int
	// VerifySandboxAccess optionally supplies pre-minted sandbox access
	// credentials for the throwaway verification pod (used by unit tests
	// running an in-process granule relay/sandbox server). When nil,
	// initForGKESandboxes mints a fresh SandboxCA and SandboxAccessSpec.
	VerifySandboxAccess *granule.SandboxAccessSpec
	// VerifyExecScript optionally overrides the shell snippet executed inside
	// the throwaway verification sandbox over the relay (used by unit tests).
	// When empty, the git-proxy + package-cache check script is executed.
	VerifyExecScript string
}

// DefaultGKESandboxesDiskTypeForMachine returns the required GKE boot
// disk type for machineType: c4-* machines require hyperdisk-balanced,
// while n2-* and other nested-virt families use pd-balanced.
func DefaultGKESandboxesDiskTypeForMachine(machineType string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(machineType)), "c4-") {
		return DefaultGKESandboxesDiskTypeHyper
	}
	return DefaultGKESandboxesDiskTypePD
}

func (g GKESandboxesOptions) withDefaults(repoRoot string) GKESandboxesOptions {
	if g.Project == "" {
		g.Project = DefaultGKESandboxesProject
	}
	if g.Location == "" {
		g.Location = DefaultGKESandboxesLocation
	}
	if g.Cluster == "" {
		g.Cluster = DefaultGKESandboxesCluster
	}
	if g.NodePool == "" {
		g.NodePool = DefaultGKESandboxesNodePool
	}
	if g.MachineType == "" {
		g.MachineType = DefaultGKESandboxesMachineType
	}
	if g.DiskType == "" {
		g.DiskType = DefaultGKESandboxesDiskTypeForMachine(g.MachineType)
	}
	if g.DiskSizeGB <= 0 {
		g.DiskSizeGB = DefaultGKESandboxesDiskSizeGB
	}
	if g.ImageType == "" {
		g.ImageType = DefaultGKESandboxesImageType
	}
	if g.NumNodes <= 0 {
		g.NumNodes = DefaultGKESandboxesNumNodes
	}
	if g.Namespace == "" {
		g.Namespace = DefaultGKESandboxesNamespace
	}
	if g.ServiceAccount == "" {
		g.ServiceAccount = DefaultGKESandboxesServiceAccount
	}
	if g.KubeconfigPath == "" {
		g.KubeconfigPath = DefaultGKESandboxesKubeconfig
	}
	if g.DevicePluginManifest == "" {
		g.DevicePluginManifest = filepath.Join(repoRoot, "third_party", "granule", "deploy", "k8s", "kvm-device-plugin.yaml")
	}
	if g.VerifyScript == "" {
		g.VerifyScript = filepath.Join(repoRoot, "scripts", "platform-verify", "k8s-granule.sh")
	}
	if g.GuestImage == "" {
		g.GuestImage = DefaultGKESandboxesGuestImage
	}
	if g.RelayImage == "" {
		g.RelayImage = g.GuestImage
	}
	if g.RelayPort <= 0 {
		g.RelayPort = DefaultGKESandboxesRelayPort
	}
	if g.GitProxyURL == "" {
		g.GitProxyURL = DefaultGKESandboxesGitProxyURL
	}
	if g.PackageCacheURL == "" {
		g.PackageCacheURL = DefaultGKESandboxesPackageCacheURL
	}
	if g.VerifyCloneRepo == "" {
		g.VerifyCloneRepo = DefaultGKESandboxesVerifyRepo
	}
	if g.TokenDuration == "" {
		g.TokenDuration = DefaultGKESandboxesTokenDuration
	}
	return g
}

func (g GKESandboxesOptions) locationFlag() string {
	// GCP zones have two hyphens (e.g. "us-central1-a"); regions have one ("us-central1").
	if strings.Count(g.Location, "-") >= 2 {
		return "--zone=" + g.Location
	}
	return "--region=" + g.Location
}

// gcloudKubeconfigUser is the name `gcloud container clusters
// get-credentials` gives the user (and context) it writes for this
// cluster.
func (g GKESandboxesOptions) gcloudKubeconfigUser() string {
	return fmt.Sprintf("gke_%s_%s_%s", g.Project, g.Location, g.Cluster)
}

// gkeSandboxesChecks returns preflight checks for PlatformGKESandboxes.
// In dry-run mode we do not fail on missing host binaries so that
// `grain init -platform=gke-sandboxes -dry-run` always prints the
// complete command plan on any machine.
func gkeSandboxesChecks(opts Options) []platformCheck {
	if opts.DryRun {
		return nil
	}
	return []platformCheck{
		checkCommand("gcloud",
			"-platform=gke-sandboxes creates or reuses a GKE Standard cluster and node pool via gcloud",
			"sudo apt-get install -y google-cloud-cli",
			"gcloud", "--version"),
		checkCommand("kubectl",
			"-platform=gke-sandboxes applies the KVM device plugin, namespace/RBAC, and verification pods via kubectl",
			"sudo apt-get install -y kubectl",
			"kubectl", "version", "--client"),
		checkCommand("gke-gcloud-auth-plugin",
			"kubectl needs gke-gcloud-auth-plugin to authenticate with GKE while minting the least-privilege ServiceAccount kubeconfig",
			"sudo apt-get install -y google-cloud-cli-gke-gcloud-auth-plugin",
			"gke-gcloud-auth-plugin", "--version"),
	}
}

func gkeClusterDescribeArgs(g GKESandboxesOptions) []string {
	return []string{
		"container", "clusters", "describe", g.Cluster,
		"--project=" + g.Project,
		g.locationFlag(),
		"--format=value(status)",
	}
}

func gkeClusterCreateArgs(g GKESandboxesOptions) []string {
	args := []string{
		"container", "clusters", "create", g.Cluster,
		"--project=" + g.Project,
		g.locationFlag(),
		"--machine-type=" + g.MachineType,
		"--image-type=" + g.ImageType,
		"--disk-type=" + g.DiskType,
		fmt.Sprintf("--disk-size=%d", g.DiskSizeGB),
		fmt.Sprintf("--num-nodes=%d", g.NumNodes),
		"--enable-nested-virtualization",
		"--node-labels=granule.io/kvm=true",
	}
	if g.MaxNodes > 0 {
		args = append(args,
			"--enable-autoscaling",
			fmt.Sprintf("--min-nodes=%d", g.MinNodes),
			fmt.Sprintf("--max-nodes=%d", g.MaxNodes),
		)
	}
	return args
}

func gkeNodePoolDescribeArgs(g GKESandboxesOptions) []string {
	return []string{
		"container", "node-pools", "describe", g.NodePool,
		"--cluster=" + g.Cluster,
		"--project=" + g.Project,
		g.locationFlag(),
		"--format=value(status)",
	}
}

func gkeNodePoolCreateArgs(g GKESandboxesOptions) []string {
	args := []string{
		"container", "node-pools", "create", g.NodePool,
		"--cluster=" + g.Cluster,
		"--project=" + g.Project,
		g.locationFlag(),
		"--machine-type=" + g.MachineType,
		"--image-type=" + g.ImageType,
		"--disk-type=" + g.DiskType,
		fmt.Sprintf("--disk-size=%d", g.DiskSizeGB),
		fmt.Sprintf("--num-nodes=%d", g.NumNodes),
		"--enable-nested-virtualization",
		"--node-labels=granule.io/kvm=true",
	}
	if g.MaxNodes > 0 {
		args = append(args,
			"--enable-autoscaling",
			fmt.Sprintf("--min-nodes=%d", g.MinNodes),
			fmt.Sprintf("--max-nodes=%d", g.MaxNodes),
		)
	}
	return args
}

func gkeGetCredentialsArgs(g GKESandboxesOptions) []string {
	return []string{
		"container", "clusters", "get-credentials", g.Cluster,
		"--project=" + g.Project,
		g.locationFlag(),
	}
}

// gkeSandboxesRBACManifest renders the grain Namespace (with
// pod-security.kubernetes.io/enforce=privileged), least-privilege
// ServiceAccount + Role + RoleBinding (pods, pods/exec, pods/log, and
// create/delete on secrets for per-sandbox access credentials), and the
// three cluster-scoped PriorityClasses sandbox pods name by default
// (grain-interactive, grain-agent, grain-ci).
func gkeSandboxesRBACManifest(namespace, serviceAccount string) string {
	// The daemon checks its sandbox PriorityClasses exist at start and
	// creates grain's own when they do not (k8s.EnsurePriorityClasses),
	// so its ServiceAccount may read those three by name and create
	// PriorityClasses. Kubernetes cannot narrow create by resourceNames
	// -- the name is not known when the request is authorized -- so
	// create is the one unrestricted verb. Named per namespace and
	// account, since a ClusterRole is cluster-wide and two deployments
	// may share a cluster.
	priorityRole := fmt.Sprintf("%s-%s-priorityclasses", namespace, serviceAccount)
	var names []string
	for _, spec := range k8s.SandboxPriorityClasses {
		names = append(names, strconv.Quote(spec.Name))
	}
	manifest := fmt.Sprintf(`apiVersion: v1
kind: Namespace
metadata:
  name: %s
  labels:
    pod-security.kubernetes.io/enforce: privileged
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: %s
  namespace: %s
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: %s
  namespace: %s
rules:
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["get", "list", "watch", "create", "delete"]
  - apiGroups: [""]
    resources: ["pods/exec"]
    verbs: ["create"]
  - apiGroups: [""]
    resources: ["pods/log"]
    verbs: ["get"]
  - apiGroups: [""]
    resources: ["secrets"]
    verbs: ["create", "delete"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: %s
  namespace: %s
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: %s
subjects:
  - kind: ServiceAccount
    name: %s
    namespace: %s
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: %s
rules:
  - apiGroups: ["scheduling.k8s.io"]
    resources: ["priorityclasses"]
    resourceNames: [%s]
    verbs: ["get"]
  - apiGroups: ["scheduling.k8s.io"]
    resources: ["priorityclasses"]
    verbs: ["create"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: %s
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: %s
subjects:
  - kind: ServiceAccount
    name: %s
    namespace: %s
`, namespace, serviceAccount, namespace, serviceAccount, namespace, serviceAccount, namespace, serviceAccount, serviceAccount, namespace,
		priorityRole, strings.Join(names, ", "), priorityRole, priorityRole, serviceAccount, namespace)
	for _, spec := range k8s.SandboxPriorityClasses {
		manifest += "---\n" + spec.Manifest()
	}
	return manifest
}

func gkeSandboxesSecretYAML(name, namespace string, data map[string][]byte) string {
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	fmt.Fprintf(&b, "apiVersion: v1\nkind: Secret\nmetadata:\n  name: %s\n  namespace: %s\ntype: Opaque\ndata:\n", name, namespace)
	for _, k := range keys {
		fmt.Fprintf(&b, "  %s: %s\n", k, base64.StdEncoding.EncodeToString(data[k]))
	}
	return b.String()
}

// gkeSandboxesRelayManifest renders:
//   - Secret/grain-sandbox-relay-tls holding the mTLS CA, server cert/key, and controller token
//   - ServiceAccount/grain-sandbox-relay + Role/RoleBinding limited to get/list/watch pods
//   - 1-replica Deployment/grain-sandbox-relay running `granule relay`
//   - Service/grain-sandbox-relay (type: LoadBalancer)
//   - NetworkPolicy/granule-sandbox-access denying all ingress to sandbox pods
//     except TCP port 7443 from the relay pods and in-cluster grain controller pods.
func gkeSandboxesRelayManifest(namespace, image string, relayPort int, pki *granule.RelayPKI) string {
	if relayPort <= 0 {
		relayPort = DefaultGKESandboxesRelayPort
	}
	secretDoc := gkeSandboxesSecretYAML(DefaultGKESandboxesRelaySecret, namespace, map[string][]byte{
		"ca.crt":  []byte(pki.CACertPEM),
		"tls.crt": []byte(pki.ServerCertPEM),
		"tls.key": []byte(pki.ServerKeyPEM),
		"token":   []byte(pki.Token),
	})
	return fmt.Sprintf(`%s---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: %s
  namespace: %s
  labels:
    app.kubernetes.io/name: %s
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: %s
  namespace: %s
  labels:
    app.kubernetes.io/name: %s
rules:
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: %s
  namespace: %s
  labels:
    app.kubernetes.io/name: %s
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: %s
subjects:
  - kind: ServiceAccount
    name: %s
    namespace: %s
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: %s
  namespace: %s
  labels:
    app.kubernetes.io/name: %s
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: %s
  template:
    metadata:
      labels:
        app.kubernetes.io/name: %s
    spec:
      serviceAccountName: %s
      containers:
        - name: relay
          image: %s
          command: ["granule", "relay"]
          args:
            - -controller-listen=:8443
            - -tls-cert=/etc/granule-relay/tls/tls.crt
            - -tls-key=/etc/granule-relay/tls/tls.key
            - -client-ca=/etc/granule-relay/tls/ca.crt
            - -token-file=/etc/granule-relay/tls/token
            - -namespace=%s
            - -selector=%s=true
            - -name-label=%s
            - -sandbox-port=%d
          ports:
            - name: relay
              containerPort: 8443
              protocol: TCP
          volumeMounts:
            - name: relay-tls
              mountPath: /etc/granule-relay/tls
              readOnly: true
      volumes:
        - name: relay-tls
          secret:
            secretName: %s
            defaultMode: 256
---
apiVersion: v1
kind: Service
metadata:
  name: %s
  namespace: %s
  labels:
    app.kubernetes.io/name: %s
spec:
  type: LoadBalancer
  selector:
    app.kubernetes.io/name: %s
  ports:
    - name: relay
      port: %d
      targetPort: 8443
      protocol: TCP
---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: %s
  namespace: %s
spec:
  podSelector:
    matchLabels:
      %s: "true"
  policyTypes:
    - Ingress
  ingress:
    - from:
        - podSelector:
            matchLabels:
              app.kubernetes.io/name: %s
        - podSelector:
            matchLabels:
              app.kubernetes.io/name: grain
      ports:
        - protocol: TCP
          port: %d
`,
		secretDoc,
		DefaultGKESandboxesRelayDeployment, namespace, DefaultGKESandboxesRelayDeployment,
		DefaultGKESandboxesRelayDeployment, namespace, DefaultGKESandboxesRelayDeployment,
		DefaultGKESandboxesRelayDeployment, namespace, DefaultGKESandboxesRelayDeployment, DefaultGKESandboxesRelayDeployment, DefaultGKESandboxesRelayDeployment, namespace,
		DefaultGKESandboxesRelayDeployment, namespace, DefaultGKESandboxesRelayDeployment,
		DefaultGKESandboxesRelayDeployment, DefaultGKESandboxesRelayDeployment, DefaultGKESandboxesRelayDeployment,
		image, namespace, granule.SandboxAccessLabel, granule.SandboxNameLabel, granule.DefaultAccessPort,
		DefaultGKESandboxesRelaySecret,
		DefaultGKESandboxesRelayService, namespace, DefaultGKESandboxesRelayDeployment, DefaultGKESandboxesRelayDeployment, relayPort,
		DefaultGKESandboxesNetworkPolicy, namespace, granule.SandboxAccessLabel, DefaultGKESandboxesRelayDeployment, granule.DefaultAccessPort,
	)
}

func gkeSandboxesVerifyPodManifest(namespace, image string, access *granule.SandboxAccessSpec) string {
	var prefix string
	if access != nil {
		prefix = gkeSandboxesSecretYAML(gkeSandboxesVerifySecretName, namespace, map[string][]byte{
			"token":   []byte(access.Token),
			"tls.crt": []byte(access.CertPEM),
			"tls.key": []byte(access.KeyPEM),
		}) + "---\n"
	}
	return prefix + fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: %s
  namespace: %s
  labels:
    app: grain-sandbox
    grain.io/init-verify: "true"
    %s: "true"
    %s: %s
spec:
  restartPolicy: Never
  containers:
    - name: granule
      image: %s
      args: ["run", "--rm", "--name", "verify", "/var/lib/granule/guest"]
      env:
        - name: GRANULE_ACCESS_LISTEN
          value: tcp://0.0.0.0:%d
        - name: GRANULE_ACCESS_SANDBOX_ID
          value: %s
        - name: GRANULE_ACCESS_TOKEN_FILE
          value: %s/token
        - name: GRANULE_ACCESS_TLS_CERT_FILE
          value: %s/tls.crt
        - name: GRANULE_ACCESS_TLS_KEY_FILE
          value: %s/tls.key
      ports:
        - name: granule-access
          containerPort: %d
          protocol: TCP
      volumeMounts:
        - name: granule-access
          mountPath: %s
          readOnly: true
      securityContext:
        privileged: true
      resources:
        limits:
          granule.io/kvm: "1"
      readinessProbe:
        exec:
          command: ["granule", "ready"]
        initialDelaySeconds: 2
        periodSeconds: 2
  volumes:
    - name: granule-access
      secret:
        secretName: %s
        defaultMode: 256
`,
		gkeSandboxesVerifyPodName, namespace,
		granule.SandboxAccessLabel, granule.SandboxNameLabel, gkeSandboxesVerifyPodName,
		image,
		granule.DefaultAccessPort, gkeSandboxesVerifyPodName,
		granule.DefaultAccessMountPath, granule.DefaultAccessMountPath, granule.DefaultAccessMountPath,
		granule.DefaultAccessPort, granule.DefaultAccessMountPath,
		gkeSandboxesVerifySecretName,
	)
}

func verifySandboxOverRelay(ctx context.Context, relayCfg granule.RelayConfig, access *granule.SandboxAccessSpec, script string) (string, error) {
	creds, err := relayCfg.RelayCredentials()
	if err != nil {
		return "", fmt.Errorf("loading relay credentials: %w", err)
	}
	dialOpts, err := access.ClientOptions()
	if err != nil {
		return "", fmt.Errorf("building sandbox client options: %w", err)
	}
	rc, err := sandbox.DialRelay(ctx, relayCfg.Addr, creds)
	if err != nil {
		return "", fmt.Errorf("dialing granule relay %s: %w", relayCfg.Addr, err)
	}
	defer rc.Close()

	cli, err := rc.Client(ctx, gkeSandboxesVerifyPodName, dialOpts)
	if err != nil {
		return "", fmt.Errorf("opening relay client for %s: %w", gkeSandboxesVerifyPodName, err)
	}
	if _, err := cli.Health(ctx); err != nil {
		return "", fmt.Errorf("granule health check over relay for %s: %w", gkeSandboxesVerifyPodName, err)
	}
	var stdout, stderr bytes.Buffer
	exitCode, err := cli.Exec(ctx, sandbox.ExecRequest{
		Argv: []string{"sh", "-c", script},
	}, nil, &stdout, &stderr)
	combined := strings.TrimSpace(stdout.String() + "\n" + stderr.String())
	if err != nil {
		return combined, fmt.Errorf("executing check over relay in %s: %w", gkeSandboxesVerifyPodName, err)
	}
	if exitCode != 0 {
		return combined, fmt.Errorf("check over relay in %s exited with status %d", gkeSandboxesVerifyPodName, exitCode)
	}
	return combined, nil
}

// initForGKESandboxes runs the idempotent, resumable setup flow for
// `grain init -platform=gke-sandboxes`. Every step records the exact
// command it runs (or would run under -dry-run) in Step.Command and
// Step.Detail.
func initForGKESandboxes(ctx context.Context, sys System, opts Options, r *Result) {
	g := opts.GKESandboxes.withDefaults(opts.RepoRoot)
	scratchDir := filepath.Join(opts.DataDir, "gke-sandboxes")
	relayDir := g.RelayDir
	if relayDir == "" {
		relayDir = scratchDir
	}
	rbacManifestPath := filepath.Join(scratchDir, "rbac.yaml")
	relayManifestPath := filepath.Join(scratchDir, "relay.yaml")
	relayConfigPath := granule.RelayConfigPath(relayDir)
	verifyPodManifestPath := filepath.Join(scratchDir, "verify-sandbox-pod.yaml")
	adminKubeconfigPath := filepath.Join(scratchDir, "admin.kubeconfig")

	clusterDescribeCmd := "gcloud " + strings.Join(gkeClusterDescribeArgs(g), " ")
	clusterCreateCmd := "gcloud " + strings.Join(gkeClusterCreateArgs(g), " ")
	poolDescribeCmd := "gcloud " + strings.Join(gkeNodePoolDescribeArgs(g), " ")
	poolCreateCmd := "gcloud " + strings.Join(gkeNodePoolCreateArgs(g), " ")
	getCredsCmd := fmt.Sprintf("KUBECONFIG=%s gcloud %s", adminKubeconfigPath, strings.Join(gkeGetCredentialsArgs(g), " "))
	devicePluginCmd := fmt.Sprintf("kubectl --kubeconfig=%s apply -f %s && kubectl --kubeconfig=%s -n kube-system rollout status daemonset/granule-device-plugin --timeout=2m",
		adminKubeconfigPath, g.DevicePluginManifest, adminKubeconfigPath)
	rbacCmd := fmt.Sprintf("kubectl --kubeconfig=%s apply -f %s", adminKubeconfigPath, rbacManifestPath)
	kubeconfigCmd := fmt.Sprintf("KUBECONFIG=%s gcloud %s && chmod 600 %s && token=$(kubectl --kubeconfig=%s -n %s create token %s --duration=%s) && kubectl --kubeconfig=%s config set-credentials %s --token=\"$token\" && kubectl --kubeconfig=%s config set-context --current --namespace=%s --user=%s && kubectl --kubeconfig=%s config delete-user %s",
		g.KubeconfigPath, strings.Join(gkeGetCredentialsArgs(g), " "), g.KubeconfigPath,
		adminKubeconfigPath, g.Namespace, g.ServiceAccount, g.TokenDuration,
		g.KubeconfigPath, g.ServiceAccount,
		g.KubeconfigPath, g.Namespace, g.ServiceAccount,
		g.KubeconfigPath, g.gcloudKubeconfigUser())
	relayCmd := fmt.Sprintf(
		"kubectl --kubeconfig=%s apply -f %s && kubectl --kubeconfig=%s -n %s rollout status deployment/%s --timeout=2m && kubectl --kubeconfig=%s -n %s get service %s -o jsonpath='{.status.loadBalancer.ingress[0].ip}{.status.loadBalancer.ingress[0].hostname}'",
		adminKubeconfigPath, relayManifestPath,
		adminKubeconfigPath, g.Namespace, DefaultGKESandboxesRelayDeployment,
		adminKubeconfigPath, g.Namespace, DefaultGKESandboxesRelayService,
	)
	verifyScriptCmd := fmt.Sprintf("KUBECONFIG=%s IMAGE=%s %s --keep",
		g.KubeconfigPath, g.GuestImage, g.VerifyScript)
	guestCheckScript := fmt.Sprintf("git -c http.proxy=%s ls-remote %s >/dev/null && curl -fsSL --proxy %s https://proxy.golang.org >/dev/null",
		g.GitProxyURL, g.VerifyCloneRepo, g.PackageCacheURL)
	verifySandboxCmd := fmt.Sprintf(
		"kubectl --kubeconfig=%s -n %s apply -f %s && kubectl --kubeconfig=%s -n %s wait --for=condition=Ready pod/%s --timeout=5m && granule relay exec (%s) %s -- sh -c %q && kubectl --kubeconfig=%s -n %s delete pod/%s --ignore-not-found --wait=true --grace-period=1",
		g.KubeconfigPath, g.Namespace, verifyPodManifestPath,
		g.KubeconfigPath, g.Namespace, gkeSandboxesVerifyPodName,
		relayConfigPath, gkeSandboxesVerifyPodName, guestCheckScript,
		g.KubeconfigPath, g.Namespace, gkeSandboxesVerifyPodName,
	)

	const (
		stepCluster       = "create or reuse GKE Standard cluster"
		stepNodePool      = "ensure nested-virtualization node pool"
		stepDevicePlugin  = "install granule KVM device plugin"
		stepRBAC          = "create grain namespace and least-privilege ServiceAccount RBAC"
		stepKubeconfig    = "write /var/lib/grain/kubeconfig"
		stepRelay         = "deploy granule sandbox relay and sandbox NetworkPolicy"
		stepVerifyScript  = "verify with scripts/platform-verify/k8s-granule.sh"
		stepVerifySandbox = "verify throwaway sandbox git proxy clone and package cache fetch"
	)

	allSteps := []struct {
		name string
		cmd  string
	}{
		{stepCluster, clusterCreateCmd},
		{stepNodePool, poolCreateCmd},
		{stepDevicePlugin, devicePluginCmd},
		{stepRBAC, rbacCmd},
		{stepKubeconfig, kubeconfigCmd},
		{stepRelay, relayCmd},
		{stepVerifyScript, verifyScriptCmd},
		{stepVerifySandbox, verifySandboxCmd},
	}

	if opts.DryRun {
		r.add(Step{
			Name:    stepCluster,
			Status:  StepWould,
			Detail:  fmt.Sprintf("check %s; if missing, run: %s", clusterDescribeCmd, clusterCreateCmd),
			Command: clusterCreateCmd,
		})
		r.add(Step{
			Name:    stepNodePool,
			Status:  StepWould,
			Detail:  fmt.Sprintf("check %s; if missing, run: %s", poolDescribeCmd, poolCreateCmd),
			Command: poolCreateCmd,
		})
		r.add(Step{
			Name:    stepDevicePlugin,
			Status:  StepWould,
			Detail:  fmt.Sprintf("fetch credentials (%s) and apply KVM device plugin: %s", getCredsCmd, devicePluginCmd),
			Command: devicePluginCmd,
		})
		r.add(Step{
			Name:    stepRBAC,
			Status:  StepWould,
			Detail:  fmt.Sprintf("write %s (namespace %s with pod-security.kubernetes.io/enforce=privileged, ServiceAccount %s, Role/RoleBinding for pods, pods/exec, pods/log, secrets (create/delete), a ClusterRole to read and create PriorityClasses, and PriorityClasses %s, %s, %s) and run: %s", rbacManifestPath, g.Namespace, g.ServiceAccount, k8s.PriorityClassInteractive, k8s.PriorityClassAgent, k8s.PriorityClassCI, rbacCmd),
			Command: rbacCmd,
		})
		r.add(Step{
			Name:    stepKubeconfig,
			Status:  StepWould,
			Detail:  fmt.Sprintf("mint least-privilege token for ServiceAccount %s/%s and write %s: %s", g.Namespace, g.ServiceAccount, g.KubeconfigPath, kubeconfigCmd),
			Command: kubeconfigCmd,
		})
		r.add(Step{
			Name:    stepRelay,
			Status:  StepWould,
			Detail:  fmt.Sprintf("mint mTLS relay PKI under %s, write %s (Secret/%s, Deployment/%s, LoadBalancer Service/%s, NetworkPolicy/%s), wait for LoadBalancer IP, and write %s: %s", relayDir, relayManifestPath, DefaultGKESandboxesRelaySecret, DefaultGKESandboxesRelayDeployment, DefaultGKESandboxesRelayService, DefaultGKESandboxesNetworkPolicy, relayConfigPath, relayCmd),
			Command: relayCmd,
		})
		r.add(Step{
			Name:    stepVerifyScript,
			Status:  StepWould,
			Detail:  fmt.Sprintf("run platform verification script: %s", verifyScriptCmd),
			Command: verifyScriptCmd,
		})
		r.add(Step{
			Name:    stepVerifySandbox,
			Status:  StepWould,
			Detail:  fmt.Sprintf("boot one throwaway sandbox pod and verify git proxy clone (%s) and package cache fetch (%s) over granule relay (%s): %s", g.GitProxyURL, g.PackageCacheURL, relayConfigPath, verifySandboxCmd),
			Command: verifySandboxCmd,
		})
		return
	}

	skipRemaining := func(fromIdx int, reason string) {
		for i := fromIdx; i < len(allSteps); i++ {
			r.add(Step{
				Name:    allSteps[i].name,
				Status:  StepSkipped,
				Detail:  "skipped: " + reason,
				Command: allSteps[i].cmd,
			})
		}
	}

	// Step 1: Create or reuse GKE Standard cluster.
	if out, err := sys.Run(ctx, "", nil, "gcloud", gkeClusterDescribeArgs(g)...); err == nil {
		status := strings.TrimSpace(out)
		if status == "" {
			status = "RUNNING"
		}
		r.add(Step{
			Name:    stepCluster,
			Status:  StepSkipped,
			Detail:  fmt.Sprintf("cluster %s already exists in %s (%s) -- checked with: %s", g.Cluster, g.Project, status, clusterDescribeCmd),
			Command: clusterDescribeCmd,
		})
	} else {
		if out, createErr := sys.Run(ctx, "", nil, "gcloud", gkeClusterCreateArgs(g)...); createErr != nil {
			r.add(Step{
				Name:    stepCluster,
				Status:  StepManual,
				Detail:  fmt.Sprintf("creating GKE Standard cluster %s failed (%v: %s) -- run: %s", g.Cluster, createErr, lastLine(out), clusterCreateCmd),
				Command: clusterCreateCmd,
			})
			skipRemaining(1, "GKE cluster is not ready yet")
			return
		}
		r.add(Step{
			Name:    stepCluster,
			Status:  StepDone,
			Detail:  fmt.Sprintf("created GKE Standard cluster %s in %s (%s): %s", g.Cluster, g.Project, g.Location, clusterCreateCmd),
			Command: clusterCreateCmd,
		})
	}

	// Step 2: Ensure nested-virtualization node pool.
	if out, err := sys.Run(ctx, "", nil, "gcloud", gkeNodePoolDescribeArgs(g)...); err == nil {
		status := strings.TrimSpace(out)
		if status == "" {
			status = "RUNNING"
		}
		r.add(Step{
			Name:    stepNodePool,
			Status:  StepSkipped,
			Detail:  fmt.Sprintf("node pool %s already exists on cluster %s (%s) -- checked with: %s", g.NodePool, g.Cluster, status, poolDescribeCmd),
			Command: poolDescribeCmd,
		})
	} else {
		if out, createErr := sys.Run(ctx, "", nil, "gcloud", gkeNodePoolCreateArgs(g)...); createErr != nil {
			r.add(Step{
				Name:    stepNodePool,
				Status:  StepManual,
				Detail:  fmt.Sprintf("creating nested-virtualization node pool %s failed (%v: %s) -- run: %s", g.NodePool, createErr, lastLine(out), poolCreateCmd),
				Command: poolCreateCmd,
			})
			skipRemaining(2, "nested-virtualization node pool is not ready yet")
			return
		}
		r.add(Step{
			Name:    stepNodePool,
			Status:  StepDone,
			Detail:  fmt.Sprintf("created nested-virtualization node pool %s (%s, disk %s): %s", g.NodePool, g.MachineType, g.DiskType, poolCreateCmd),
			Command: poolCreateCmd,
		})
	}

	// Prepare scratch directory and admin kubeconfig for cluster setup steps.
	if err := os.MkdirAll(scratchDir, 0o755); err != nil {
		r.add(Step{
			Name:    stepDevicePlugin,
			Status:  StepManual,
			Detail:  fmt.Sprintf("creating scratch directory %s failed: %v", scratchDir, err),
			Command: devicePluginCmd,
		})
		skipRemaining(3, "scratch directory could not be created")
		return
	}
	adminEnv := []string{"KUBECONFIG=" + adminKubeconfigPath}
	if out, err := sys.Run(ctx, "", adminEnv, "gcloud", gkeGetCredentialsArgs(g)...); err != nil {
		r.add(Step{
			Name:    stepDevicePlugin,
			Status:  StepManual,
			Detail:  fmt.Sprintf("fetching cluster credentials failed (%v: %s) -- run: %s", err, lastLine(out), getCredsCmd),
			Command: getCredsCmd,
		})
		skipRemaining(3, "cluster credentials are not available yet")
		return
	}

	// Step 3: Install granule's KVM device plugin.
	dsReadyOut, dsErr := sys.Run(ctx, "", nil, "kubectl", "--kubeconfig="+adminKubeconfigPath,
		"-n", "kube-system", "get", "daemonset", "granule-device-plugin",
		"-o", "jsonpath={.status.numberReady}/{.status.desiredNumberScheduled}")
	if dsErr == nil && isDaemonSetRolledOut(strings.TrimSpace(dsReadyOut)) {
		r.add(Step{
			Name:    stepDevicePlugin,
			Status:  StepSkipped,
			Detail:  fmt.Sprintf("daemonset/granule-device-plugin is already rolled out in kube-system (%s): %s", strings.TrimSpace(dsReadyOut), devicePluginCmd),
			Command: devicePluginCmd,
		})
	} else {
		if out, err := sys.Run(ctx, "", nil, "kubectl", "--kubeconfig="+adminKubeconfigPath, "apply", "-f", g.DevicePluginManifest); err != nil {
			r.add(Step{
				Name:    stepDevicePlugin,
				Status:  StepManual,
				Detail:  fmt.Sprintf("applying %s failed (%v: %s) -- run: %s", g.DevicePluginManifest, err, lastLine(out), devicePluginCmd),
				Command: devicePluginCmd,
			})
			skipRemaining(3, "granule KVM device plugin is not installed yet")
			return
		}
		if out, err := sys.Run(ctx, "", nil, "kubectl", "--kubeconfig="+adminKubeconfigPath, "-n", "kube-system", "rollout", "status", "daemonset/granule-device-plugin", "--timeout=2m"); err != nil {
			r.add(Step{
				Name:    stepDevicePlugin,
				Status:  StepManual,
				Detail:  fmt.Sprintf("waiting for daemonset/granule-device-plugin rollout failed (%v: %s) -- run: %s", err, lastLine(out), devicePluginCmd),
				Command: devicePluginCmd,
			})
			skipRemaining(3, "granule KVM device plugin rollout did not finish")
			return
		}
		r.add(Step{
			Name:    stepDevicePlugin,
			Status:  StepDone,
			Detail:  fmt.Sprintf("installed %s and waited for daemonset/granule-device-plugin rollout: %s", g.DevicePluginManifest, devicePluginCmd),
			Command: devicePluginCmd,
		})
	}

	// Step 4: Create grain namespace (pod-security.kubernetes.io/enforce=privileged)
	// and least-privilege grain ServiceAccount + Role/RoleBinding (pods, pods/exec, pods/log, secrets create/delete).
	rbacContent := gkeSandboxesRBACManifest(g.Namespace, g.ServiceAccount)
	if err := os.WriteFile(rbacManifestPath, []byte(rbacContent), 0o644); err != nil {
		r.add(Step{
			Name:    stepRBAC,
			Status:  StepManual,
			Detail:  fmt.Sprintf("writing %s failed: %v", rbacManifestPath, err),
			Command: rbacCmd,
		})
		skipRemaining(4, "namespace and ServiceAccount RBAC could not be written")
		return
	}
	// Always apply rather than probing for the namespace and RoleBinding:
	// a re-run must reset a grain Role someone widened since to the
	// least-privilege rules above, not take its existence as proof it is
	// still least-privilege. apply reports "unchanged" for every object
	// when nothing differed, which is what makes the step a skip.
	if out, err := sys.Run(ctx, "", nil, "kubectl", "--kubeconfig="+adminKubeconfigPath, "apply", "-f", rbacManifestPath); err != nil {
		r.add(Step{
			Name:    stepRBAC,
			Status:  StepManual,
			Detail:  fmt.Sprintf("applying %s failed (%v: %s) -- run: %s", rbacManifestPath, err, lastLine(out), rbacCmd),
			Command: rbacCmd,
		})
		skipRemaining(4, "namespace and ServiceAccount RBAC are not ready yet")
		return
	} else if kubectlApplyUnchanged(out) {
		r.add(Step{
			Name:    stepRBAC,
			Status:  StepSkipped,
			Detail:  fmt.Sprintf("namespace %s (pod-security.kubernetes.io/enforce=privileged), ServiceAccount/Role/RoleBinding %s, and PriorityClasses %s, %s, %s already match %s: %s", g.Namespace, g.ServiceAccount, k8s.PriorityClassInteractive, k8s.PriorityClassAgent, k8s.PriorityClassCI, rbacManifestPath, rbacCmd),
			Command: rbacCmd,
		})
	} else {
		r.add(Step{
			Name:    stepRBAC,
			Status:  StepDone,
			Detail:  fmt.Sprintf("applied %s (namespace %s with pod-security.kubernetes.io/enforce=privileged, ServiceAccount %s, Role/RoleBinding for pods, pods/exec, pods/log, secrets (create/delete), a ClusterRole to read and create PriorityClasses, and PriorityClasses %s, %s, %s): %s", rbacManifestPath, g.Namespace, g.ServiceAccount, k8s.PriorityClassInteractive, k8s.PriorityClassAgent, k8s.PriorityClassCI, rbacCmd),
			Command: rbacCmd,
		})
	}

	// Step 5: Write /var/lib/grain/kubeconfig.
	if _, statErr := os.Stat(g.KubeconfigPath); statErr == nil {
		canIOut, canIErr := sys.Run(ctx, "", nil, "kubectl", "--kubeconfig="+g.KubeconfigPath,
			"-n", g.Namespace, "auth", "can-i", "create", "pods")
		// An admin kubeconfig can create pods too: only one that is
		// also refused secrets is the least-privilege ServiceAccount's.
		// can-i exits non-zero when its answer is "no", so read the
		// answer rather than the exit status.
		secretsOut, _ := sys.Run(ctx, "", nil, "kubectl", "--kubeconfig="+g.KubeconfigPath,
			"-n", g.Namespace, "auth", "can-i", "get", "secrets")
		if canIErr == nil && lastLine(canIOut) == "yes" && lastLine(secretsOut) == "no" {
			r.add(Step{
				Name:    stepKubeconfig,
				Status:  StepSkipped,
				Detail:  fmt.Sprintf("%s already exists and authenticates as %s/%s (can-i create pods = yes): %s", g.KubeconfigPath, g.Namespace, g.ServiceAccount, kubeconfigCmd),
				Command: kubeconfigCmd,
			})
			goto relayStep
		}
	}
	{
		if err := os.MkdirAll(filepath.Dir(g.KubeconfigPath), 0o750); err != nil {
			r.add(Step{
				Name:    stepKubeconfig,
				Status:  StepManual,
				Detail:  fmt.Sprintf("creating directory %s failed (%v) -- run: %s", filepath.Dir(g.KubeconfigPath), err, kubeconfigCmd),
				Command: kubeconfigCmd,
			})
			skipRemaining(5, g.KubeconfigPath+" was not written")
			return
		}
		targetEnv := []string{"KUBECONFIG=" + g.KubeconfigPath}
		if out, err := sys.Run(ctx, "", targetEnv, "gcloud", gkeGetCredentialsArgs(g)...); err != nil {
			r.add(Step{
				Name:    stepKubeconfig,
				Status:  StepManual,
				Detail:  fmt.Sprintf("populating cluster entry in %s failed (%v: %s) -- run: %s", g.KubeconfigPath, err, lastLine(out), kubeconfigCmd),
				Command: kubeconfigCmd,
			})
			skipRemaining(5, g.KubeconfigPath+" was not written")
			return
		}
		// The file is about to hold a bearer token: make it owner-only
		// before the token is written into it, whatever mode gcloud or
		// an earlier run left it with.
		if err := os.Chmod(g.KubeconfigPath, 0o600); err != nil {
			r.add(Step{
				Name:    stepKubeconfig,
				Status:  StepManual,
				Detail:  fmt.Sprintf("restricting %s to mode 0600 failed (%v) -- run: chmod 600 %s", g.KubeconfigPath, err, g.KubeconfigPath),
				Command: kubeconfigCmd,
			})
			skipRemaining(5, g.KubeconfigPath+" was not written")
			return
		}
		tokenOut, err := sys.Run(ctx, "", nil, "kubectl", "--kubeconfig="+adminKubeconfigPath,
			"-n", g.Namespace, "create", "token", g.ServiceAccount, "--duration="+g.TokenDuration)
		if err != nil || strings.TrimSpace(tokenOut) == "" {
			r.add(Step{
				Name:    stepKubeconfig,
				Status:  StepManual,
				Detail:  fmt.Sprintf("minting token for ServiceAccount %s/%s failed (%v: %s) -- run: %s", g.Namespace, g.ServiceAccount, err, lastLine(tokenOut), kubeconfigCmd),
				Command: kubeconfigCmd,
			})
			skipRemaining(5, g.KubeconfigPath+" was not written")
			return
		}
		token := strings.TrimSpace(tokenOut)
		if out, err := sys.Run(ctx, "", nil, "kubectl", "--kubeconfig="+g.KubeconfigPath,
			"config", "set-credentials", g.ServiceAccount, "--token="+token); err != nil {
			r.add(Step{
				Name:    stepKubeconfig,
				Status:  StepManual,
				Detail:  fmt.Sprintf("setting ServiceAccount credentials in %s failed (%v: %s) -- run: %s", g.KubeconfigPath, err, lastLine(out), kubeconfigCmd),
				Command: kubeconfigCmd,
			})
			skipRemaining(5, g.KubeconfigPath+" was not written")
			return
		}
		if out, err := sys.Run(ctx, "", nil, "kubectl", "--kubeconfig="+g.KubeconfigPath,
			"config", "set-context", "--current", "--namespace="+g.Namespace, "--user="+g.ServiceAccount); err != nil {
			r.add(Step{
				Name:    stepKubeconfig,
				Status:  StepManual,
				Detail:  fmt.Sprintf("setting current context in %s failed (%v: %s) -- run: %s", g.KubeconfigPath, err, lastLine(out), kubeconfigCmd),
				Command: kubeconfigCmd,
			})
			skipRemaining(5, g.KubeconfigPath+" was not written")
			return
		}
		// get-credentials also wrote a user entry that authenticates as
		// the operator's own gcloud identity (cluster admin); nothing
		// selects it any more, but leaving it means anyone who can read
		// this file and run the gcloud auth plugin as that operator can
		// pick it. Drop it so the file names the ServiceAccount alone.
		if out, err := sys.Run(ctx, "", nil, "kubectl", "--kubeconfig="+g.KubeconfigPath,
			"config", "delete-user", g.gcloudKubeconfigUser()); err != nil {
			r.add(Step{
				Name:    stepKubeconfig,
				Status:  StepManual,
				Detail:  fmt.Sprintf("removing gcloud's admin user %s from %s failed (%v: %s) -- run: kubectl --kubeconfig=%s config delete-user %s", g.gcloudKubeconfigUser(), g.KubeconfigPath, err, lastLine(out), g.KubeconfigPath, g.gcloudKubeconfigUser()),
				Command: kubeconfigCmd,
			})
			skipRemaining(5, g.KubeconfigPath+" still names gcloud's admin user")
			return
		}
		r.add(Step{
			Name:    stepKubeconfig,
			Status:  StepDone,
			Detail:  fmt.Sprintf("wrote least-privilege kubeconfig to %s for ServiceAccount %s/%s: %s", g.KubeconfigPath, g.Namespace, g.ServiceAccount, kubeconfigCmd),
			Command: kubeconfigCmd,
		})
	}

relayStep:
	// Step 6: Mint relay PKI, apply Secret/grain-sandbox-relay-tls,
	// Deployment/grain-sandbox-relay, Service/grain-sandbox-relay (LoadBalancer),
	// and NetworkPolicy/granule-sandbox-access, wait for the LoadBalancer IP,
	// and write relay.json.
	{
		existingRelayCfg, _ := granule.LoadRelayConfig(relayConfigPath)
		pki, err := granule.MintRelayPKI(relayDir, g.Namespace)
		if err != nil {
			r.add(Step{
				Name:    stepRelay,
				Status:  StepManual,
				Detail:  fmt.Sprintf("minting relay PKI in %s failed: %v", relayDir, err),
				Command: relayCmd,
			})
			skipRemaining(6, "granule sandbox relay credentials could not be minted")
			return
		}
		relayContent := gkeSandboxesRelayManifest(g.Namespace, g.RelayImage, g.RelayPort, pki)
		if err := os.WriteFile(relayManifestPath, []byte(relayContent), 0o600); err != nil {
			r.add(Step{
				Name:    stepRelay,
				Status:  StepManual,
				Detail:  fmt.Sprintf("writing %s failed: %v", relayManifestPath, err),
				Command: relayCmd,
			})
			skipRemaining(6, "granule sandbox relay manifest could not be written")
			return
		}
		applyOut, err := sys.Run(ctx, "", nil, "kubectl", "--kubeconfig="+adminKubeconfigPath, "apply", "-f", relayManifestPath)
		if err != nil {
			r.add(Step{
				Name:    stepRelay,
				Status:  StepManual,
				Detail:  fmt.Sprintf("applying %s failed (%v: %s) -- run: %s", relayManifestPath, err, lastLine(applyOut), relayCmd),
				Command: relayCmd,
			})
			skipRemaining(6, "granule sandbox relay and NetworkPolicy are not deployed yet")
			return
		}
		if out, err := sys.Run(ctx, "", nil, "kubectl", "--kubeconfig="+adminKubeconfigPath,
			"-n", g.Namespace, "rollout", "status", "deployment/"+DefaultGKESandboxesRelayDeployment, "--timeout=2m"); err != nil {
			r.add(Step{
				Name:    stepRelay,
				Status:  StepManual,
				Detail:  fmt.Sprintf("waiting for deployment/%s rollout failed (%v: %s) -- run: %s", DefaultGKESandboxesRelayDeployment, err, lastLine(out), relayCmd),
				Command: relayCmd,
			})
			skipRemaining(6, "granule sandbox relay rollout did not finish")
			return
		}
		lbOut, err := sys.Run(ctx, "", nil, "kubectl", "--kubeconfig="+adminKubeconfigPath,
			"-n", g.Namespace, "get", "service", DefaultGKESandboxesRelayService,
			"-o", "jsonpath={.status.loadBalancer.ingress[0].ip}{.status.loadBalancer.ingress[0].hostname}")
		lbHost := strings.TrimSpace(lbOut)
		if err != nil || lbHost == "" {
			r.add(Step{
				Name:    stepRelay,
				Status:  StepManual,
				Detail:  fmt.Sprintf("service/%s has no LoadBalancer IP/hostname yet (%v: %s) -- run: %s", DefaultGKESandboxesRelayService, err, lastLine(lbOut), relayCmd),
				Command: relayCmd,
			})
			skipRemaining(6, "granule sandbox relay LoadBalancer IP is not ready yet")
			return
		}
		relayAddr := net.JoinHostPort(lbHost, strconv.Itoa(g.RelayPort))
		newRelayCfg := granule.RelayConfig{
			Addr:       relayAddr,
			CAFile:     pki.CAFile,
			CertFile:   pki.ClientCert,
			KeyFile:    pki.ClientKey,
			TokenFile:  pki.TokenFile,
			ServerName: DefaultGKESandboxesRelayService,
		}
		if err := granule.SaveRelayConfig(relayConfigPath, newRelayCfg); err != nil {
			r.add(Step{
				Name:    stepRelay,
				Status:  StepManual,
				Detail:  fmt.Sprintf("writing %s failed: %v", relayConfigPath, err),
				Command: relayCmd,
			})
			skipRemaining(6, relayConfigPath+" was not written")
			return
		}
		if kubectlApplyUnchanged(applyOut) && existingRelayCfg != nil && existingRelayCfg.Addr == relayAddr {
			r.add(Step{
				Name:    stepRelay,
				Status:  StepSkipped,
				Detail:  fmt.Sprintf("Deployment/%s, Service/%s (%s), NetworkPolicy/%s, and %s already match %s: %s", DefaultGKESandboxesRelayDeployment, DefaultGKESandboxesRelayService, relayAddr, DefaultGKESandboxesNetworkPolicy, relayConfigPath, relayManifestPath, relayCmd),
				Command: relayCmd,
			})
		} else {
			r.add(Step{
				Name:    stepRelay,
				Status:  StepDone,
				Detail:  fmt.Sprintf("deployed %s (Secret/%s, Deployment/%s, LoadBalancer Service/%s at %s, NetworkPolicy/%s) and wrote %s: %s", relayManifestPath, DefaultGKESandboxesRelaySecret, DefaultGKESandboxesRelayDeployment, DefaultGKESandboxesRelayService, relayAddr, DefaultGKESandboxesNetworkPolicy, relayConfigPath, relayCmd),
				Command: relayCmd,
			})
		}
	}

	// Step 7: Verify with scripts/platform-verify/k8s-granule.sh.
	verifyEnv := []string{
		"KUBECONFIG=" + g.KubeconfigPath,
		"IMAGE=" + g.GuestImage,
	}
	if out, err := sys.Run(ctx, opts.RepoRoot, verifyEnv, g.VerifyScript, "--keep"); err != nil {
		r.add(Step{
			Name:    stepVerifyScript,
			Status:  StepManual,
			Detail:  fmt.Sprintf("%s failed (%v: %s) -- run: %s", g.VerifyScript, err, lastLine(out), verifyScriptCmd),
			Command: verifyScriptCmd,
		})
		skipRemaining(7, "scripts/platform-verify/k8s-granule.sh did not pass")
		return
	}
	r.add(Step{
		Name:    stepVerifyScript,
		Status:  StepDone,
		Detail:  fmt.Sprintf("verified granule KVM pod boot with %s: %s", g.VerifyScript, verifyScriptCmd),
		Command: verifyScriptCmd,
	})

	// Step 8: Verify one throwaway sandbox that clones through the git proxy
	// and fetches through the package cache over the granule relay.
	access := g.VerifySandboxAccess
	if access == nil {
		ca, err := granule.NewSandboxCA()
		if err != nil {
			r.add(Step{
				Name:    stepVerifySandbox,
				Status:  StepManual,
				Detail:  fmt.Sprintf("generating throwaway sandbox CA failed: %v", err),
				Command: verifySandboxCmd,
			})
			return
		}
		access, err = ca.MintSandbox(gkeSandboxesVerifyPodName)
		if err != nil {
			r.add(Step{
				Name:    stepVerifySandbox,
				Status:  StepManual,
				Detail:  fmt.Sprintf("minting throwaway sandbox credentials failed: %v", err),
				Command: verifySandboxCmd,
			})
			return
		}
	}
	if err := os.WriteFile(verifyPodManifestPath, []byte(gkeSandboxesVerifyPodManifest(g.Namespace, g.GuestImage, access)), 0o600); err != nil {
		r.add(Step{
			Name:    stepVerifySandbox,
			Status:  StepManual,
			Detail:  fmt.Sprintf("writing %s failed: %v", verifyPodManifestPath, err),
			Command: verifySandboxCmd,
		})
		return
	}
	_, _ = sys.Run(ctx, "", nil, "kubectl", "--kubeconfig="+g.KubeconfigPath,
		"-n", g.Namespace, "delete", "pod/"+gkeSandboxesVerifyPodName, "--ignore-not-found", "--wait=true", "--grace-period=1")
	defer func() {
		_, _ = sys.Run(ctx, "", nil, "kubectl", "--kubeconfig="+g.KubeconfigPath,
			"-n", g.Namespace, "delete", "pod/"+gkeSandboxesVerifyPodName, "--ignore-not-found", "--wait=true", "--grace-period=1")
	}()

	if out, err := sys.Run(ctx, "", nil, "kubectl", "--kubeconfig="+g.KubeconfigPath,
		"-n", g.Namespace, "apply", "-f", verifyPodManifestPath); err != nil {
		r.add(Step{
			Name:    stepVerifySandbox,
			Status:  StepManual,
			Detail:  fmt.Sprintf("creating throwaway sandbox pod failed (%v: %s) -- run: %s", err, lastLine(out), verifySandboxCmd),
			Command: verifySandboxCmd,
		})
		return
	}
	if out, err := sys.Run(ctx, "", nil, "kubectl", "--kubeconfig="+g.KubeconfigPath,
		"-n", g.Namespace, "wait", "--for=condition=Ready", "pod/"+gkeSandboxesVerifyPodName, "--timeout=5m"); err != nil {
		r.add(Step{
			Name:    stepVerifySandbox,
			Status:  StepManual,
			Detail:  fmt.Sprintf("throwaway sandbox pod did not become Ready within 5m (%v: %s) -- run: %s", err, lastLine(out), verifySandboxCmd),
			Command: verifySandboxCmd,
		})
		return
	}
	relayCfg, err := granule.LoadRelayConfig(relayConfigPath)
	if err != nil {
		r.add(Step{
			Name:    stepVerifySandbox,
			Status:  StepManual,
			Detail:  fmt.Sprintf("loading %s failed: %v", relayConfigPath, err),
			Command: verifySandboxCmd,
		})
		return
	}
	execScript := guestCheckScript
	if g.VerifyExecScript != "" {
		execScript = g.VerifyExecScript
	}
	if out, err := verifySandboxOverRelay(ctx, *relayCfg, access, execScript); err != nil {
		r.add(Step{
			Name:    stepVerifySandbox,
			Status:  StepManual,
			Detail:  fmt.Sprintf("throwaway sandbox git proxy / package cache check over relay failed (%v: %s) -- run: %s", err, lastLine(out), verifySandboxCmd),
			Command: verifySandboxCmd,
		})
		return
	}
	r.add(Step{
		Name:    stepVerifySandbox,
		Status:  StepDone,
		Detail:  fmt.Sprintf("verified throwaway sandbox git clone through git proxy (%s) and fetch through package cache (%s) over granule relay (%s): %s", g.GitProxyURL, g.PackageCacheURL, relayCfg.Addr, verifySandboxCmd),
		Command: verifySandboxCmd,
	})
}

func isDaemonSetRolledOut(readyFraction string) bool {
	parts := strings.Split(strings.TrimSpace(readyFraction), "/")
	if len(parts) != 2 {
		return false
	}
	ready := strings.TrimSpace(parts[0])
	desired := strings.TrimSpace(parts[1])
	return ready != "" && ready != "0" && ready == desired
}

// kubectlApplyUnchanged reports whether `kubectl apply` output says
// every object it applied was already as the manifest describes.
func kubectlApplyUnchanged(out string) bool {
	lines := 0
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		if !strings.HasSuffix(line, " unchanged") {
			return false
		}
		lines++
	}
	return lines > 0
}
