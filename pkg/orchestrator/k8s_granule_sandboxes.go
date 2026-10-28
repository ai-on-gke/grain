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

package orchestrator

// K8sGranuleSandboxes is -platform=k8s-granule's own Sandboxes backend:
// one privileged pod per run, each booting a real cloud-hypervisor VM
// over /dev/kvm -- the same isolation GranuleSandboxes gives on a single
// docker host, scheduled by a cluster instead. See
// docs/k8s-granule-design.md for why this is a new backend rather than a
// mode of K8sSandboxes: that platform's pods must never be privileged,
// and its exec transport (`kubectl exec <pod> -- <argv>`, straight into
// a plain sandbox container) reaches the wrong thing here -- a granule
// pod's one process is cloud-hypervisor itself, so every tool call has
// to go one layer deeper, over granule's own vsock-based exec protocol
// (`kubectl exec <pod> -- granule exec -- <argv>`).
//
// What carries over from K8sSandboxes, almost verbatim: the pod
// lifecycle (pkg/k8s.Client's create/delete/wait-ready/list), the
// ReapOrphans sweep, PodNameFor's DNS-1123 check. What does not: Command
// is never set (k8s.PodOptions.NoCommand -- the image's own ENTRYPOINT
// is the VM boot, not a shell to exec into later), the pod is always
// Privileged with /dev/kvm as a HostDevice, it always carries a
// ReadinessExec so "Ready" means "the guest answered", and its exec
// transport wraps every argv in ["granule", "exec", "--"] before handing
// it to the same mcp.KubectlExecRunner K8sSandboxes uses.
import (
	"context"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/granule"
	"github.com/ai-on-gke/grain/pkg/k8s"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
)

// DefaultGranuleForwardBackoff is how long K8sGranuleSandboxes first
// waits before reconnecting a `granule forward` stream that dropped while
// the sandbox pod is still running. Each further drop without a healthy
// session in between doubles it, up to DefaultGranuleForwardMaxBackoff,
// and every wait is jittered so that a control-plane hiccup that drops
// every pod's stream at once does not have them all reconnect in step.
const DefaultGranuleForwardBackoff = 500 * time.Millisecond

// DefaultGranuleForwardMaxBackoff caps the reconnect backoff.
const DefaultGranuleForwardMaxBackoff = 10 * time.Second

// DefaultGranuleForwardHealthySession is how long a `granule forward`
// session has to stay up for its drop to count as a fresh failure, which
// resets the reconnect backoff to DefaultGranuleForwardBackoff.
const DefaultGranuleForwardHealthySession = 10 * time.Second

// DefaultGranuleForwardReadyTimeout bounds how long Acquire waits for the
// first `granule forward` session to have its guest-side listeners bound
// (reconnecting as often as the backoff allows meanwhile) before failing:
// a run whose guest cannot reach 127.0.0.1:9728 cannot clone anything, so
// handing it out early only moves the failure to its first git clone.
const DefaultGranuleForwardReadyTimeout = time.Minute

// GranulePodNamePrefix is PodNamePrefix's counterpart for this backend's
// pods -- a different prefix so an operator reading `kubectl get pods`
// against a namespace that has run both platforms at different times
// (never concurrently: only one Sandboxes backend is ever configured)
// can still tell which kind of pod a leftover was.
const GranulePodNamePrefix = "grain-granule-"

// DevKVM is the host device every K8sGranuleSandboxes pod mounts --
// there is no other way into a node's nested-virt capability than the
// device node itself (docs/k8s-granule-design.md §2).
const DevKVM = "/dev/kvm"

// GranuleReadinessExec is the readinessProbe every pod this backend
// creates carries: dial the guest's own agent over vsock and exit zero
// once it answers, the same probe
// third_party/granule/deploy/k8s/gke-pod-example.yaml ships. "-timeout
// 0" is a single attempt; WaitReady's own poll (k8s.DefaultPollInterval)
// does the retrying, the same division of labour the preflight's own
// nested-virt check uses.
var GranuleReadinessExec = []string{"granule", "ready", "-timeout", "0"}

// GranuleTransportMode selects how K8sGranuleSandboxes connects to sandbox pods.
type GranuleTransportMode string

const (
	// GranuleTransportAuto selects Direct when running inside a Kubernetes pod,
	// Relay when relay.json or -k8s-granule-relay-addr is configured, and
	// KubectlExec otherwise.
	GranuleTransportAuto GranuleTransportMode = ""
	// GranuleTransportDirect dials podIP:7443 directly via granule's sandbox.Dial.
	GranuleTransportDirect GranuleTransportMode = "direct"
	// GranuleTransportRelay connects through granule relay via sandbox.DialRelay.
	GranuleTransportRelay GranuleTransportMode = "relay"
	// GranuleTransportKubectlExec uses `kubectl exec <pod> -- granule exec --` as a fallback.
	GranuleTransportKubectlExec GranuleTransportMode = "kubectl-exec"
)

// K8sGranuleConfig is what K8sGranuleSandboxes needs to create and reach
// one granule pod per run.
type K8sGranuleConfig struct {
	// Instance is the k8s.InstanceLabel value every pod is created
	// with and the orphan sweep and Readopt select on: the deployment's
	// effective id (model.EffectiveDeploymentID), so deployments sharing
	// a namespace never reap or adopt each other's pods. Empty labels
	// nothing and lists every grain sandbox pod. SetInstance replaces it.
	Instance string
	// Namespace is where sandbox pods are created (k8s.DefaultNamespace
	// when empty).
	Namespace string
	// Kubectl is the binary pkg/k8s and the exec transport both run;
	// empty means "kubectl", found on PATH.
	Kubectl string
	// Kubeconfig is the kubeconfig file path passed as --kubeconfig to
	// pkg/k8s.Client, mcp.KubectlExecRunner, and `granule forward`; empty
	// leaves kubectl's own default discovery ($KUBECONFIG or in-cluster).
	Kubeconfig string
	// TransportMode overrides the startup transport choice (tests).
	// When empty, K8sGranuleSandboxes selects Direct in-cluster, Relay when
	// relay.json or RelayAddr is configured, and KubectlExec otherwise.
	TransportMode GranuleTransportMode
	// RelayAddr is the host:port of the `granule relay` LoadBalancer
	// (-k8s-granule-relay-addr). When set, it overrides the address in
	// RelayConfigPath.
	RelayAddr string
	// RelayConfigPath is the path to relay.json (granule.DefaultRelayConfigPath
	// when empty; "-" disables reading the default file).
	RelayConfigPath string
	// RelayConfig provides an explicit relay configuration (tests).
	RelayConfig *granule.RelayConfig
	// InPod overrides k8s.InKubernetesPod() when non-nil (tests).
	InPod *bool
	// PodIPResolver overrides k.client.PodIP when resolving pod IPs in
	// direct mode (tests).
	PodIPResolver func(ctx context.Context, podName string) (string, error)
	// AccessDir is the directory where per-sandbox access spec files are
	// written so forked `grain mcpserver` subprocesses can connect over
	// the same transport.
	AccessDir string
	// ForwardedPorts lists controller loopback ports to forward into each
	// guest VM at 127.0.0.1:<port> via `granule forward` over `kubectl exec -i`.
	ForwardedPorts []int
	// ForwardedPortsFunc, when non-nil, returns controller loopback ports
	// to forward into each guest VM (e.g. sandboxnet.SandboxServicePorts.EndpointPorts),
	// evaluated when starting a forward session and when reporting
	// ReservedGuestPorts.
	ForwardedPortsFunc func() []int
	// ForwardRunner overrides granule.ForwardKubectl in tests.
	ForwardRunner func(ctx context.Context, cfg granule.KubectlForwardConfig) error
	// ForwardBackoff is the delay between reconnect attempts when a
	// `granule forward` session drops while the sandbox pod is still running
	// (DefaultGranuleForwardBackoff when zero). It is the first delay;
	// consecutive drops double it up to ForwardMaxBackoff.
	ForwardBackoff time.Duration
	// ForwardMaxBackoff caps the reconnect delay
	// (DefaultGranuleForwardMaxBackoff when zero).
	ForwardMaxBackoff time.Duration
	// ForwardHealthySession is how long a session must stay up for the
	// backoff to reset to ForwardBackoff when it drops
	// (DefaultGranuleForwardHealthySession when zero).
	ForwardHealthySession time.Duration
	// ForwardReadyTimeout bounds Acquire's and Rebuild's wait for the
	// forward session to report its guest listeners bound
	// (DefaultGranuleForwardReadyTimeout when zero).
	ForwardReadyTimeout time.Duration
	// Image is the packed guest image every pod runs -- a
	// self-contained image with its own guest disk and kernel already
	// baked in (docs/k8s-granule-design.md §4), not the plain sandbox
	// image K8sConfig.Image names, and not
	// hostinit.DefaultGranuleRuntimeImage ("granule:vendored") either,
	// which carries no guest disk and cannot boot at all. Required:
	// there is no context-free default this package could pick.
	Image string
	// ServiceAccount is the sandbox pod's Kubernetes identity. Normally
	// empty, for the same reason K8sConfig.ServiceAccount is: nothing in
	// a sandbox should be talking to the API server.
	ServiceAccount string
	// NodeSelector pins sandbox pods to the nested-virt-capable node
	// pool -- required in practice on any cluster with more than one
	// pool, since nothing here can otherwise tell the scheduler not to
	// land a privileged pod asking for /dev/kvm on a node that has
	// neither (docs/k8s-granule-design.md §2's "one real gap").
	NodeSelector map[string]string
	// PriorityClasses names the PriorityClass each kind of sandbox pod
	// runs at, exactly as K8sConfig.PriorityClasses does.
	PriorityClasses K8sPriorityClasses
	// StrictPriorityClasses is K8sConfig.StrictPriorityClasses.
	StrictPriorityClasses bool
	// Env is set on every sandbox container beyond the CHV_* variables
	// this backend already derives from Shape -- a place for
	// CHV_SETUP_SCRIPT or any other cloud-hypervisor passthrough a
	// deployment wants on every boot.
	Env map[string]string
	// Workspace is the working directory run_command/read_file/
	// edit_file/write_file operate in inside each guest -- the granule
	// VM's own account home, not anything about the pod.
	Workspace string
	// ReadyTimeout bounds how long Acquire waits for a freshly created
	// pod's guest to answer "granule ready" before giving up
	// (DefaultK8sGranuleReadyTimeout if zero). Dominated by the image
	// pull on a cold node, same as K8sConfig.ReadyTimeout; the guest
	// boot itself is seconds (gke.md's own measured 2.3s on real KVM).
	ReadyTimeout time.Duration
	// ReadyPollInterval is how often Acquire re-asks while waiting out
	// ReadyTimeout (k8s.DefaultPollInterval if zero).
	ReadyPollInterval time.Duration
	// UnschedulableTimeout bounds how long, within ReadyTimeout, Acquire
	// lets the scheduler go on saying no node can take the pod before it
	// fails with the scheduler's own message (DefaultK8sUnschedulableTimeout
	// if zero) -- see that constant for why it is shorter.
	UnschedulableTimeout time.Duration
	// DefaultCPUs, DefaultMemoryMB and DefaultDiskGB seed the
	// deployment-wide default a run that asked for no shape of its own
	// falls back to, exactly as K8sConfig's fields of the same names do.
	DefaultCPUs     int
	DefaultMemoryMB int
	DefaultDiskGB   int
	// CPUOvercommit and MemoryOvercommit seed the deployment-wide
	// overcommit ratios (model.Config.SandboxCPUOvercommit /
	// SandboxMemoryOvercommit) used to scale down pod CPU and memory
	// requests while keeping limits and guest CHV_CPUS/CHV_MEMORY_MB at
	// the full sandbox shape.
	CPUOvercommit    float64
	MemoryOvercommit float64
	// DiskOvercommit seeds model.Config.SandboxDiskOvercommit, which
	// scales each pod's ephemeral-storage request down the same way while
	// its ephemeral-storage limit stays at the full sandbox disk.
	DiskOvercommit float64
	// DefaultNetworkAccess seeds the deployment-wide network a run that
	// asked for none of its own gets. Empty reads as model.NetworkOpen.
	DefaultNetworkAccess model.NetworkAccess
}

// DefaultK8sGranuleReadyTimeout bounds Acquire's wait for a sandbox pod's
// guest to answer ready. Ten minutes, matching DefaultK8sReadyTimeout --
// the wait here is dominated by the same autoscaler node scale-up and
// cold-node image pull, not by the guest boot, which is seconds once the
// image is already local.
const DefaultK8sGranuleReadyTimeout = 10 * time.Minute

// NetworkLabel/NetworkNoneValue are reused from k8s_sandboxes.go: a
// network:none run is labelled identically on either backend, even
// though what actually withholds the network differs (see
// IsolatesNetwork's own comment).

// K8sGranuleSandboxes builds one privileged, /dev/kvm-mounted pod per run
// and deletes it when the run is done.
type K8sGranuleSandboxes struct {
	cfg    K8sGranuleConfig
	client k8s.Client
	// fit admits each pod against the cluster's free capacity before it
	// is created (admitPod); nil in a backend not built by its
	// constructor, which then admits everything.
	fit *k8s.FitChecker
	// priority holds back the PriorityClasses the cluster does not
	// have (k8s_priority_classes.go).
	priority k8sPriorityGuard

	transportMode GranuleTransportMode
	relayConfig   *granule.RelayConfig
	relayPool     *granule.PersistentRelayPool
	ca            *granule.SandboxCA
	accessDir     string

	mu sync.Mutex
	// image is SetImage's replacement for cfg.Image (live_image.go).
	image liveImage
	// instance is SetInstance's replacement for cfg.Instance.
	instance liveInstance
	live     map[string]*k8sGranuleSandbox
	// building holds the pod names an Acquire or Rebuild is creating and
	// waiting on -- K8sSandboxes.building, for the same reason.
	building         map[string]bool
	accessSpecs      map[string]granule.SandboxAccessSpec
	defaults         Shape
	cpuOvercommit    float64
	memoryOvercommit float64
	diskOvercommit   float64

	// onForwardBackoff, when set (by tests), is told every reconnect
	// delay the forward supervisor is about to sleep.
	onForwardBackoff func(time.Duration)
}

// DefaultK8sGranuleWorkspace is the default working directory inside a
// Kubernetes granule VM pod when K8sGranuleConfig.Workspace is unset,
// matching Granulefile.
const DefaultK8sGranuleWorkspace = "/home/debian"

// DefaultK8sGranuleGuestUser is the guest account every `granule exec`
// into a pod's VM runs as -- tool calls, CI steps, git credential
// writes -- set on the pod as GRANULE_EXEC_USER, which granule-shim
// takes as the default account for every session into the container.
// Left unset, guestexec's own default applies, and that is root: every
// command a task or a CI step ran was uid 0, where initdb refuses to
// run and a 0555 directory stops nothing. "debian" is the account the
// repository's Granulefile creates (with passwordless sudo), the same
// one GranuleSandboxes runs as on a docker host and the one
// DefaultK8sGranuleWorkspace is the home of. What needs root reaches it
// through sudo, as it does on every other backend whose sandbox user is
// not root. K8sGranuleConfig.Env can still name another account.
const DefaultK8sGranuleGuestUser = "debian"

// NewK8sGranuleSandboxes returns a K8sGranuleSandboxes configured by cfg.
func NewK8sGranuleSandboxes(cfg K8sGranuleConfig) *K8sGranuleSandboxes {
	if cfg.Workspace == "" {
		cfg.Workspace = DefaultK8sGranuleWorkspace
	}
	client := k8s.Client{
		Kubectl:    cfg.Kubectl,
		Kubeconfig: cfg.Kubeconfig,
		Namespace:  cfg.Namespace,
	}
	ca, _ := granule.NewSandboxCA()
	accessDir := cfg.AccessDir
	if accessDir == "" {
		accessDir = filepath.Join(os.TempDir(), "grain-granule-access")
	}
	mode, relayCfg, relayPool := resolveGranuleTransport(cfg)
	return &K8sGranuleSandboxes{
		cfg:           cfg,
		client:        client,
		fit:           newFitChecker(client, cfg.NodeSelector),
		transportMode: mode,
		relayConfig:   relayCfg,
		relayPool:     relayPool,
		ca:            ca,
		accessDir:     accessDir,
		live:          map[string]*k8sGranuleSandbox{},
		building:      map[string]bool{},
		accessSpecs:   map[string]granule.SandboxAccessSpec{},
		defaults: Shape{
			CPUs: cfg.DefaultCPUs, MemoryMB: cfg.DefaultMemoryMB, DiskGB: cfg.DefaultDiskGB,
			NetworkAccess: cfg.DefaultNetworkAccess,
		},
		cpuOvercommit:    cfg.CPUOvercommit,
		memoryOvercommit: cfg.MemoryOvercommit,
		diskOvercommit:   cfg.DiskOvercommit,
	}
}

func resolveGranuleTransport(cfg K8sGranuleConfig) (GranuleTransportMode, *granule.RelayConfig, *granule.PersistentRelayPool) {
	resolveRelayCfg := func() *granule.RelayConfig {
		var rc *granule.RelayConfig
		if cfg.RelayConfig != nil {
			cp := *cfg.RelayConfig
			rc = &cp
		} else if cfg.RelayConfigPath != "-" {
			path := cfg.RelayConfigPath
			if path == "" {
				path = granule.DefaultRelayConfigPath
			}
			if loaded, err := granule.LoadRelayConfig(path); err == nil {
				rc = loaded
			}
		}
		if cfg.RelayAddr != "" {
			if rc == nil {
				rc = &granule.RelayConfig{}
			}
			rc.Addr = strings.TrimSpace(cfg.RelayAddr)
		}
		if rc != nil && strings.TrimSpace(rc.Addr) != "" {
			return rc
		}
		return nil
	}

	switch cfg.TransportMode {
	case GranuleTransportDirect:
		return GranuleTransportDirect, nil, nil
	case GranuleTransportRelay:
		rc := resolveRelayCfg()
		if rc != nil {
			return GranuleTransportRelay, rc, granule.NewPersistentRelayPool(*rc)
		}
		return GranuleTransportRelay, nil, nil
	case GranuleTransportKubectlExec:
		return GranuleTransportKubectlExec, nil, nil
	default:
		inPod := false
		if cfg.InPod != nil {
			inPod = *cfg.InPod
		} else {
			inPod = k8s.InKubernetesPod()
		}
		if inPod {
			return GranuleTransportDirect, nil, nil
		}
		if rc := resolveRelayCfg(); rc != nil {
			return GranuleTransportRelay, rc, granule.NewPersistentRelayPool(*rc)
		}
		return GranuleTransportKubectlExec, nil, nil
	}
}

// TransportMode returns the sandbox transport mode selected at startup.
func (k *K8sGranuleSandboxes) TransportMode() GranuleTransportMode {
	return k.transportMode
}

// Close closes any persistent relay session held by this backend.
func (k *K8sGranuleSandboxes) Close() error {
	if k.relayPool != nil {
		return k.relayPool.Close()
	}
	return nil
}

// SetDefaultShape replaces the deployment-wide default the next Acquire
// falls back to -- K8sSandboxes.SetDefaultShape's counterpart.
func (k *K8sGranuleSandboxes) SetDefaultShape(shape Shape) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.defaults = shape
}

// SetImage implements SandboxImageSetter: the image the next sandbox is
// created from.
func (k *K8sGranuleSandboxes) SetImage(image string) { k.image.set(image) }

// CurrentImage implements SandboxImageSetter.
func (k *K8sGranuleSandboxes) CurrentImage() string { return k.image.get(k.cfg.Image) }

// SetInstance implements SandboxInstanceSetter.
func (k *K8sGranuleSandboxes) SetInstance(id string) { k.instance.set(id) }

// CurrentInstance implements SandboxInstanceSetter.
func (k *K8sGranuleSandboxes) CurrentInstance() string { return k.instance.get(k.cfg.Instance) }

// pods is k.client labelling and listing under the instance currently
// in force -- what every call that creates or lists pods goes through.
// EnsurePriorityClasses implements K8sPriorityClassEnsurer.
func (k *K8sGranuleSandboxes) EnsurePriorityClasses(ctx context.Context, now time.Time) (K8sPriorityClassStatus, bool) {
	return ensureK8sPriorityClasses(ctx, &k.priority, k.client, k.cfg.PriorityClasses, "grain-granule.yaml", now)
}

func (k *K8sGranuleSandboxes) pods() k8s.Client {
	c := k.client
	c.Instance = k.CurrentInstance()
	return c
}

// DefaultShape reads back the deployment-wide default VM pod shape
// currently in effect on this backend.
func (k *K8sGranuleSandboxes) DefaultShape() Shape {
	return k.defaultShape()
}

func (k *K8sGranuleSandboxes) defaultShape() Shape {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.defaults
}

// SetOvercommit replaces the deployment-wide CPU, memory and disk
// overcommit ratios the next pod built scales its resource requests by, while
// leaving the pod's limits and the guest VM's CHV_CPUS/CHV_MEMORY_MB at
// the full sandbox shape.
func (k *K8sGranuleSandboxes) SetOvercommit(cpu, memory, disk float64) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.cpuOvercommit = cpu
	k.memoryOvercommit = memory
	k.diskOvercommit = disk
}

// Overcommit reads back the CPU, memory and disk overcommit ratios currently in
// effect on this backend.
func (k *K8sGranuleSandboxes) Overcommit() (cpu, memory, disk float64) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.cpuOvercommit, k.memoryOvercommit, k.diskOvercommit
}

// ClusterCapacity returns the sum of allocatable CPU cores, memory (in MiB)
// and ephemeral storage (in GiB)
// across ready, schedulable cluster nodes matching K8sGranuleConfig.NodeSelector.
func (k *K8sGranuleSandboxes) ClusterCapacity(ctx context.Context) (cpus, memoryMB, diskGB int, err error) {
	return k.client.ClusterCapacity(ctx, k.cfg.NodeSelector)
}

// FitsNow implements model.ClusterFit: whether a pod committing want,
// built at this backend's defaults otherwise, would be scheduled now.
func (k *K8sGranuleSandboxes) FitsNow(want model.Commitment) (bool, string) {
	return commitmentFits(k.fit, k.podShape(commitmentShape(want).orDefault(k.defaultShape())))
}

// ClusterFree describes the cluster's free capacity as this backend last
// read it, for the health pane beside grain's own budget.
func (k *K8sGranuleSandboxes) ClusterFree(ctx context.Context) k8s.FreeSummary {
	return k.fit.Summary(ctx)
}

// RefreshClusterFit re-reads the cluster's free capacity if the reading
// FitsNow answers from is stale -- once per reconcile tick.
func (k *K8sGranuleSandboxes) RefreshClusterFit(ctx context.Context) { k.fit.Refresh(ctx) }

// HostCapacity returns the cluster's allocatable CPU and memory capacity for
// sandbox pods on this backend.
func (k *K8sGranuleSandboxes) HostCapacity(ctx context.Context) (cpus, memoryMB, diskGB int, err error) {
	return k.ClusterCapacity(ctx)
}

// IsolatesNetwork implements NetworkIsolator: true, unconditionally.
//
// Unlike K8sSandboxes, which shares its node's network namespace and so
// can only withhold the internet if the cluster's own CNI enforces a
// NetworkPolicy object (K8sConfig.NetworkPolicyEnforced, an operator's
// assertion grain cannot check from inside a pod), a granule guest's
// network is the VM's own device, not the pod's: CHV_NET_MODE=none
// (podOptions below) boots a guest with no network interface at all,
// which is a property of the VM itself and true regardless of what the
// surrounding pod -- or the cluster's CNI -- can reach. There is nothing
// for an operator to assert here.
func (k *K8sGranuleSandboxes) IsolatesNetwork() bool { return true }

// forwardedPorts returns the deduplicated list of controller ports to
// forward into guest loopback (127.0.0.1:<port>) via `granule forward`.
func (k *K8sGranuleSandboxes) forwardedPorts() []int {
	var raw []int
	raw = append(raw, k.cfg.ForwardedPorts...)
	if k.cfg.ForwardedPortsFunc != nil {
		raw = append(raw, k.cfg.ForwardedPortsFunc()...)
	}
	if len(raw) == 0 {
		return nil
	}
	seen := make(map[int]bool, len(raw))
	out := make([]int, 0, len(raw))
	for _, p := range raw {
		if p <= 0 || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// ReservedGuestPorts are the guest loopback ports a capability grant may
// not forward into (docs/capabilities-spec.md §5.3.1): the guest's sshd
// (port 22) plus any controller service ports forwarded into guest
// loopback by `granule forward`.
func (k *K8sGranuleSandboxes) ReservedGuestPorts() []int {
	ports := []int{22}
	for _, p := range k.forwardedPorts() {
		if p != 22 {
			ports = append(ports, p)
		}
	}
	return ports
}

// PodNameFor returns the pod name for a sandbox.
func (k *K8sGranuleSandboxes) PodNameFor(sandbox string) (string, error) {
	return podNameWithPrefix(GranulePodNamePrefix, sandbox)
}

// Acquire implements Sandboxes: create this run's pod, wait for its
// guest to answer ready, start the supervised `granule forward` session,
// and hand back a handle that deletes it on Release.
func (k *K8sGranuleSandboxes) Acquire(ctx context.Context, sandbox string, shape Shape) (Sandbox, error) {
	name, err := k.PodNameFor(sandbox)
	if err != nil {
		return nil, err
	}
	resolved := shape.orDefault(k.defaultShape())
	defer k.markBuilding(name)()
	if err := k.create(ctx, name, resolved); err != nil {
		k.deleteQuietly(ctx, name)
		return nil, err
	}
	if err := k.waitReady(ctx, name); err != nil {
		k.deleteQuietly(ctx, name)
		return nil, unschedulableOutcome(ctx, k.fit, name, k.podShape(resolved), err)
	}
	sb := &k8sGranuleSandbox{owner: k, name: sandbox, podName: name, shape: shape, runner: k.sandboxRunnerFor(name), startedAt: time.Now()}
	k.mu.Lock()
	k.live[sandbox] = sb
	k.mu.Unlock()
	if err := sb.startForward(ctx); err != nil {
		k.mu.Lock()
		delete(k.live, sandbox)
		k.mu.Unlock()
		sb.stopForward()
		k.deleteQuietly(ctx, name)
		return nil, err
	}
	return sb, nil
}

// markBuilding is K8sSandboxes.markBuilding's counterpart.
func (k *K8sGranuleSandboxes) markBuilding(pod string) func() {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.building == nil {
		k.building = map[string]bool{}
	}
	k.building[pod] = true
	return func() {
		k.mu.Lock()
		delete(k.building, pod)
		k.mu.Unlock()
	}
}

// Readopt implements SandboxReadopter: K8sSandboxes.Readopt's
// counterpart for a pod running a granule VM.
func (k *K8sGranuleSandboxes) Readopt(ctx context.Context, sandbox string) (Sandbox, error) {
	name, err := k.PodNameFor(sandbox)
	if err != nil {
		return nil, err
	}
	if err := readoptPod(ctx, k.pods(), name); err != nil {
		return nil, err
	}
	sb := &k8sGranuleSandbox{owner: k, name: sandbox, podName: name, runner: k.sandboxRunnerFor(name), startedAt: time.Now()}
	k.mu.Lock()
	k.live[sandbox] = sb
	k.mu.Unlock()
	// A readopted run is already past its setup, and the supervisor goes
	// on reconnecting in the background, so a forward that is slow to
	// come back is reported rather than taking the run down with it.
	if err := sb.startForward(ctx); err != nil {
		log.Printf("%v; leaving the supervisor reconnecting", err)
	}
	return sb, nil
}

// create builds name's pod, deleting whatever is already under the name
// first and waiting for that delete to finish -- K8sSandboxes.create's
// own reasoning: a name is not free again until the old object has
// actually left the cluster.
func (k *K8sGranuleSandboxes) create(ctx context.Context, name string, shape Shape) error {
	if err := k.client.Delete(ctx, name, true); err != nil {
		return fmt.Errorf("orchestrator: deleting stale granule sandbox pod %q before rebuilding it: %w", name, err)
	}
	_ = k.client.DeleteSecret(ctx, granule.SandboxSecretName(name))
	podShape := k.podShape(shape)
	if err := admitPod(ctx, k.fit, name, podShape); err != nil {
		return err
	}
	opts, err := k.podOptionsFor(name, shape, true)
	if err != nil {
		return fmt.Errorf("orchestrator: preparing granule sandbox pod %q: %w", name, err)
	}
	if err := k.priority.createPod(ctx, k.pods(), name, podShape, opts, k.cfg.StrictPriorityClasses); err != nil {
		return fmt.Errorf("orchestrator: creating granule sandbox pod %q: %w", name, err)
	}
	k.fit.Reserve(name, podShape.Requests())
	return nil
}

// podShape resolves a shape that has already been through the
// deployment default against grain's own DefaultShape -- K8sSandboxes'
// own podShape, unchanged: the pod's own cpu/memory/ephemeral-storage
// request-and-limit, not the VM's boot size, which podOptions' CHV_*
// variables carry separately. The one difference is memory: the pod's
// limit is the guest's RAM plus GranuleVMMMemoryOverheadMB (the same
// headroom GranuleSandboxes' -memory-limit-mb carries), because the
// cloud-hypervisor process holding a guest that has touched all of its
// RAM is bigger than that RAM, and a limit of exactly CHV_MEMORY_MB gets
// the whole VM OOM-killed the first time the guest uses it all.
func (k *K8sGranuleSandboxes) podShape(shape Shape) k8s.PodShape {
	resolved := shape.orDefault(DefaultShape())
	cpuOvercommit, memOvercommit, diskOvercommit := k.Overcommit()
	return k8s.PodShape{
		CPUs:             resolved.CPUs,
		MemoryMB:         resolved.MemoryMB + GranuleVMMMemoryOverheadMB,
		DiskGB:           resolved.DiskGB,
		CPUOvercommit:    cpuOvercommit,
		MemoryOvercommit: memOvercommit,
		DiskOvercommit:   diskOvercommit,
	}
}

func (k *K8sGranuleSandboxes) ensureCA() (*granule.SandboxCA, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.ca == nil {
		ca, err := granule.NewSandboxCA()
		if err != nil {
			return nil, err
		}
		k.ca = ca
	}
	if k.accessSpecs == nil {
		k.accessSpecs = map[string]granule.SandboxAccessSpec{}
	}
	return k.ca, nil
}

// podOptions builds this backend's pod: always privileged, always
// carrying /dev/kvm, starting granule's sandbox server in TCP listen
// mode on port 7443 with a per-sandbox token and TLS certificate
// delivered through a projected Secret volume, never a Command override
// (the image's own ENTRYPOINT boots the VM), and
// CHV_CPUS/CHV_MEMORY_MB/CHV_DISK_SIZE_MB/CHV_NET_MODE derived from shape
// so the guest boots at the size the pod was actually given rather than
// at whatever the image's own defaults happen to be.
func (k *K8sGranuleSandboxes) podOptions(shape Shape) k8s.PodOptions {
	opts, err := k.podOptionsFor(GranulePodNamePrefix+"sandbox", shape, false)
	if err != nil {
		panic(err)
	}
	return opts
}

func (k *K8sGranuleSandboxes) podOptionsFor(name string, shape Shape, persist bool) (k8s.PodOptions, error) {
	ca, err := k.ensureCA()
	if err != nil {
		return k8s.PodOptions{}, err
	}
	token, err := granule.MintToken()
	if err != nil {
		return k8s.PodOptions{}, err
	}
	certPEM, keyPEM, err := ca.IssueSandboxCert(name, name)
	if err != nil {
		return k8s.PodOptions{}, err
	}

	resolved := shape.orDefault(DefaultShape())
	env := map[string]string{
		"CHV_CPUS":         strconv.Itoa(resolved.CPUs),
		"CHV_MEMORY_MB":    strconv.Itoa(resolved.MemoryMB),
		"CHV_DISK_SIZE_MB": strconv.Itoa(resolved.DiskGB * mibPerGiB),
		"CHV_NET_MODE":     chvNetMode(resolved.NetworkAccess),
		// granule-shim reads this as every exec session's default account.
		"GRANULE_EXEC_USER":            DefaultK8sGranuleGuestUser,
		"GRANULE_ACCESS_LISTEN":        granule.DefaultAccessListen,
		"GRANULE_ACCESS_SANDBOX_ID":    name,
		"GRANULE_ACCESS_TOKEN_FILE":    granule.DefaultAccessMountPath + "/token",
		"GRANULE_ACCESS_TLS_CERT_FILE": granule.DefaultAccessMountPath + "/tls.crt",
		"GRANULE_ACCESS_TLS_KEY_FILE":  granule.DefaultAccessMountPath + "/tls.key",
	}
	for key, value := range k.cfg.Env {
		env[key] = value
	}
	labels := map[string]string{
		granule.SandboxAccessLabel: granule.SandboxAccessLabelValue,
		granule.SandboxNameLabel:   name,
	}
	if resolved.NetworkAccess == model.NetworkNone {
		labels[NetworkLabel] = NetworkNoneValue
	} else {
		labels[granule.SandboxNetworkAccessLabel] = granule.SandboxNetworkOpenValue
	}
	opts := k8s.PodOptions{
		Image:          k.CurrentImage(),
		ServiceAccount: k.cfg.ServiceAccount,
		NoCommand:      true,
		Env:            env,
		Labels:         labels,
		NodeSelector:   k.cfg.NodeSelector,
		Privileged:     true,
		HostDevices:    []string{DevKVM},
		Ports: []k8s.ContainerPort{
			{Name: "granule-access", Port: granule.DefaultAccessPort},
		},
		SecretVolume: &k8s.SecretVolume{
			Name:      granule.SandboxSecretName(name),
			MountPath: granule.DefaultAccessMountPath,
			Data: map[string]string{
				"token":   token,
				"tls.crt": string(certPEM),
				"tls.key": string(keyPEM),
				"ca.crt":  string(ca.CAPEM()),
			},
		},
		PriorityClassName: k.priority.resolve(k.cfg.PriorityClasses.For(shape.Class), k.cfg.StrictPriorityClasses),
	}
	// The kubectl-exec fallback has no transport to poll granule health
	// over (waitGuestHealth skips it), so "Ready" still has to mean "the
	// guest answered" through the pod's own readiness probe.
	if k.transportMode == GranuleTransportKubectlExec {
		opts.ReadinessExec = GranuleReadinessExec
	}

	mode := string(k.transportMode)
	if mode == "" || mode == string(GranuleTransportKubectlExec) {
		mode = granule.AccessModeDirect
	}
	spec := granule.SandboxAccessSpec{
		Mode:       mode,
		PodName:    name,
		Namespace:  k.client.NamespaceOrDefault(),
		Kubectl:    k.cfg.Kubectl,
		Kubeconfig: k.cfg.Kubeconfig,
		Port:       granule.DefaultAccessPort,
		Token:      token,
		CACertPEM:  string(ca.CAPEM()),
		User:       env["GRANULE_EXEC_USER"],
		Relay:      k.relayConfig,
	}
	k.mu.Lock()
	k.accessSpecs[name] = spec
	k.mu.Unlock()
	if persist && k.transportMode != GranuleTransportKubectlExec {
		if path := k.accessFilePath(name); path != "" {
			if err := granule.WriteSandboxAccessSpec(path, spec); err != nil {
				return k8s.PodOptions{}, err
			}
		}
	}
	return opts, nil
}

func (k *K8sGranuleSandboxes) accessFilePath(podName string) string {
	dir := k.accessDir
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "grain-granule-access")
	}
	return filepath.Join(dir, podName+".json")
}

func (k *K8sGranuleSandboxes) accessFileForPod(podName string) string {
	if k.transportMode == GranuleTransportKubectlExec {
		return ""
	}
	return k.accessFilePath(podName)
}

func (k *K8sGranuleSandboxes) cleanupAccessSpec(podName string) {
	k.mu.Lock()
	delete(k.accessSpecs, podName)
	k.mu.Unlock()
	if path := k.accessFilePath(podName); path != "" {
		_ = os.Remove(path)
	}
}

// chvNetMode is the granule-shim CHV_NET_MODE this backend boots a guest
// with: "none" for model.NetworkNone, which gives the guest no network
// device at all (the strongest answer "no network access" can have --
// see IsolatesNetwork), and "inline" otherwise, which gives the guest the
// pod's own IP and MAC (gke-pod-exec-example.yaml) so an open-network run
// can reach what the pod could.
func chvNetMode(access model.NetworkAccess) string {
	if access == model.NetworkNone {
		return "none"
	}
	return "inline"
}

func (k *K8sGranuleSandboxes) readyTimeout() time.Duration {
	if k.cfg.ReadyTimeout > 0 {
		return k.cfg.ReadyTimeout
	}
	return DefaultK8sGranuleReadyTimeout
}

func (k *K8sGranuleSandboxes) waitReady(ctx context.Context, name string) error {
	deadline := time.Now().Add(k.readyTimeout())
	if err := k.client.WaitReady(ctx, name, deadline, unschedulableTimeout(k.cfg.UnschedulableTimeout), k.cfg.ReadyPollInterval); err != nil {
		return fmt.Errorf("orchestrator: waiting for granule sandbox pod %q: %w", name, err)
	}
	if err := k.waitGuestHealth(ctx, name, deadline); err != nil {
		return fmt.Errorf("orchestrator: waiting for granule sandbox guest %q health: %w", name, err)
	}
	return nil
}

func (k *K8sGranuleSandboxes) waitGuestHealth(ctx context.Context, name string, deadline time.Time) error {
	if k.transportMode == GranuleTransportKubectlExec {
		return nil
	}
	gr, ok := k.sandboxRunnerFor(name).(*mcp.GranuleSandboxRunner)
	if !ok || gr == nil {
		return nil
	}
	poll := k.cfg.ReadyPollInterval
	if poll <= 0 {
		poll = k8s.DefaultPollInterval
	}
	var lastErr error
	for {
		probeTimeout := min(10*time.Second, time.Until(deadline))
		if probeTimeout <= 0 {
			if lastErr != nil {
				return lastErr
			}
			return context.DeadlineExceeded
		}
		probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
		_, err := gr.Health(probeCtx)
		cancel()
		if err == nil {
			return nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().Add(poll).After(deadline) {
			return lastErr
		}
		timer := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (k *K8sGranuleSandboxes) resolvePodIP(ctx context.Context, podName string) (string, error) {
	if k.cfg.PodIPResolver != nil {
		return k.cfg.PodIPResolver(ctx, podName)
	}
	return k.client.PodIP(ctx, podName)
}

func (k *K8sGranuleSandboxes) lookupAccessSpec(name string) (granule.SandboxAccessSpec, bool) {
	k.mu.Lock()
	spec, ok := k.accessSpecs[name]
	k.mu.Unlock()
	if ok {
		return spec, true
	}
	if path := k.accessFilePath(name); path != "" {
		if loaded, err := granule.ReadSandboxAccessSpec(path); err == nil && loaded != nil {
			k.mu.Lock()
			if k.accessSpecs == nil {
				k.accessSpecs = map[string]granule.SandboxAccessSpec{}
			}
			k.accessSpecs[name] = *loaded
			k.mu.Unlock()
			return *loaded, true
		}
	}
	return granule.SandboxAccessSpec{}, false
}

func (k *K8sGranuleSandboxes) sandboxRunnerFor(name string) sandboxRunner {
	if k.transportMode == GranuleTransportKubectlExec {
		return k.execRunner(name)
	}
	spec, ok := k.lookupAccessSpec(name)
	if !ok {
		return k.execRunner(name)
	}
	r, err := mcp.NewGranuleSandboxRunnerFromSpec(spec, k.relayPool, k.resolvePodIP)
	if err != nil {
		return k.execRunner(name)
	}
	r.User = spec.User
	return r
}

// execRunner builds the fallback `kubectl exec <pod> -c sandbox -- granule exec -- <argv>`
// transport used when neither direct in-cluster dialing nor granule relay is configured.
func (k *K8sGranuleSandboxes) execRunner(name string) *mcp.KubectlExecRunner {
	return &mcp.KubectlExecRunner{
		Kubectl:    k.cfg.Kubectl,
		Kubeconfig: k.cfg.Kubeconfig,
		Namespace:  k.client.NamespaceOrDefault(),
		Pod:        name,
		Container:  k8s.SandboxContainer,
		Through:    granuleExecPrefix,
	}
}

// podAlive reports whether name is still tracked as live and not in a
// terminal Kubernetes pod phase.
func (k *K8sGranuleSandboxes) podAlive(ctx context.Context, sandbox, podName string) bool {
	k.mu.Lock()
	_, tracked := k.live[sandbox]
	k.mu.Unlock()
	if !tracked {
		return false
	}
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	st, err := k.client.Status(checkCtx, podName)
	if err != nil {
		// Transient kubectl/API error: keep reconnecting while the sandbox is tracked.
		return true
	}
	return !st.Phase.Terminal()
}

// k8sGranuleCleanupTimeout bounds a delete run on behalf of a caller
// that is already on its way out -- K8sSandboxes' own k8sCleanupTimeout,
// unchanged.
const k8sGranuleCleanupTimeout = 30 * time.Second

// deleteQuietly tears down a pod that never became usable -- see
// K8sSandboxes.deleteQuietly for why this runs on a context detached
// from the caller's own cancellation.
func (k *K8sGranuleSandboxes) deleteQuietly(ctx context.Context, name string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), k8sGranuleCleanupTimeout)
	defer cancel()
	k.fit.Release(name)
	_ = k.client.Delete(ctx, name, false)
	_ = k.client.DeleteSecret(ctx, granule.SandboxSecretName(name))
	k.cleanupAccessSpec(name)
}

// ReapOrphans is SweepOrphans at startup -- K8sSandboxes.ReapOrphans'
// counterpart.
func (k *K8sGranuleSandboxes) ReapOrphans(ctx context.Context) (int, error) {
	return k.SweepOrphans(ctx)
}

// SweepOrphans deletes every grain sandbox pod in this deployment's
// namespace that this process is not itself holding -- neither live nor
// being built by an Acquire or Rebuild -- K8sSandboxes.SweepOrphans,
// unchanged: both backends create pods under the same two labels
// (pkg/k8s.ManagedByLabel/SandboxLabel), so they share one sweep, each
// with its own idea of what it holds. reconcileSandboxes runs it on
// every tick, which is what reclaims a pod a Release could not delete.
func (k *K8sGranuleSandboxes) SweepOrphans(ctx context.Context) (int, error) {
	var reapedPods []string
	n, err := sweepOrphanPods(ctx, k.pods(), &k.mu, func(pod string) bool {
		if k.building[pod] {
			return true
		}
		for _, sb := range k.live {
			if sb.podName == pod {
				return true
			}
		}
		reapedPods = append(reapedPods, pod)
		return false
	})
	for _, pod := range reapedPods {
		_ = k.client.DeleteSecret(ctx, granule.SandboxSecretName(pod))
		k.cleanupAccessSpec(pod)
	}
	return n, err
}

// k8sGranuleSandbox is one run's pod.
type k8sGranuleSandbox struct {
	owner   *K8sGranuleSandboxes
	name    string
	podName string
	shape   Shape
	runner  sandboxRunner
	// startedAt is when this process acquired (or readopted) the pod --
	// SandboxHealth.StartedAt.
	startedAt time.Time

	forwardMu     sync.Mutex
	forwardCancel context.CancelFunc
	forwardDone   chan struct{}
}

func (s *k8sGranuleSandbox) Name() string { return s.name }

// PodName is the actual Kubernetes pod name -- K8sSandboxes.PodName's
// counterpart.
func (s *k8sGranuleSandbox) PodName() string { return s.podName }

// KubePod implements podNamedSandbox so subprocess agent frameworks
// (`grain mcpserver`) receive -k8s-pod, -k8s-kubeconfig, -k8s-granule,
// and -k8s-granule-access-file.
func (s *k8sGranuleSandbox) KubePod() agent.KubePod {
	return agent.KubePod{
		Namespace:         s.owner.client.NamespaceOrDefault(),
		Pod:               s.podName,
		Workspace:         s.owner.cfg.Workspace,
		Kubeconfig:        s.owner.cfg.Kubeconfig,
		Granule:           true,
		GranuleAccessFile: s.owner.accessFileForPod(s.podName),
	}
}

// startForward starts a supervised `granule forward` session into
// s.podName, forwarding each configured controller port from guest
// 127.0.0.1:<port> to controller 127.0.0.1:<port>, and waits until a
// session reports its guest listeners bound -- so nothing the caller
// runs in the guest next (the run's first git clone through
// 127.0.0.1:9728) can race the tunnel's startup. A session that fails
// before it gets there is retried with the supervisor's own backoff
// until ForwardReadyTimeout; the error is returned once that passes, ctx
// is done, or the supervisor gives up because the pod is gone. The
// supervisor itself is left running on error: stopForward stops it.
func (s *k8sGranuleSandbox) startForward(ctx context.Context) error {
	s.stopForward()
	ports := s.owner.forwardedPorts()
	if len(ports) == 0 {
		return nil
	}
	cfg := s.owner.cfg
	runner := cfg.ForwardRunner
	if runner == nil {
		if gr, ok := s.runner.(*mcp.GranuleSandboxRunner); ok {
			runner = func(fctx context.Context, fcfg granule.KubectlForwardConfig) error {
				return granule.ForwardStream(fctx, gr.OpenForwardStream, fcfg)
			}
		} else {
			runner = granule.ForwardKubectl
		}
	}
	bo := forwardBackoff{
		base:    positiveOr(cfg.ForwardBackoff, DefaultGranuleForwardBackoff),
		max:     positiveOr(cfg.ForwardMaxBackoff, DefaultGranuleForwardMaxBackoff),
		healthy: positiveOr(cfg.ForwardHealthySession, DefaultGranuleForwardHealthySession),
	}
	readyTimeout := positiveOr(cfg.ForwardReadyTimeout, DefaultGranuleForwardReadyTimeout)

	fwdCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	ready := make(chan struct{})
	var readyOnce sync.Once
	markReady := func() {
		readyOnce.Do(func() { close(ready) })
	}

	s.forwardMu.Lock()
	s.forwardCancel = cancel
	s.forwardDone = done
	s.forwardMu.Unlock()

	go func() {
		defer close(done)
		for {
			started := time.Now()
			err := runner(fwdCtx, granule.KubectlForwardConfig{
				Kubectl:     cfg.Kubectl,
				Kubeconfig:  cfg.Kubeconfig,
				Namespace:   s.owner.client.NamespaceOrDefault(),
				Pod:         s.podName,
				Container:   k8s.SandboxContainer,
				RemotePorts: append([]int(nil), ports...),
				OnReady:     markReady,
			})
			if fwdCtx.Err() != nil {
				return
			}
			if !s.owner.podAlive(fwdCtx, s.name, s.podName) {
				return
			}
			delay := bo.next(time.Since(started))
			if err != nil {
				log.Printf("orchestrator: granule forward for pod %q dropped (%v); reconnecting in %s", s.podName, err, delay.Round(time.Millisecond))
			}
			if hook := s.owner.onForwardBackoff; hook != nil {
				hook(delay)
			}
			timer := time.NewTimer(delay)
			select {
			case <-fwdCtx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()

	timer := time.NewTimer(readyTimeout)
	defer timer.Stop()
	select {
	case <-ready:
		return nil
	case <-done:
		return fmt.Errorf("orchestrator: granule forward for pod %q stopped before its guest listeners were ready", s.podName)
	case <-ctx.Done():
		return fmt.Errorf("orchestrator: waiting for granule forward into pod %q: %w", s.podName, ctx.Err())
	case <-timer.C:
		return fmt.Errorf("orchestrator: granule forward into pod %q was not ready after %s", s.podName, readyTimeout)
	}
}

// forwardBackoff is the `granule forward` supervisor's reconnect delay:
// exponential from base to max across consecutive drops, reset to base
// by a session that stayed up at least healthy, and jittered.
type forwardBackoff struct {
	base, max, healthy time.Duration
	cur                time.Duration
	// jitter maps a nominal delay to the one actually slept (tests pin
	// it); nil means equalJitter.
	jitter func(time.Duration) time.Duration
}

// next returns the delay before the reconnect that follows a session
// which ran for lasted, and advances the backoff.
func (b *forwardBackoff) next(lasted time.Duration) time.Duration {
	if b.cur <= 0 || lasted >= b.healthy {
		b.cur = b.base
	}
	d := b.cur
	b.cur = min(b.cur*2, b.max)
	if b.jitter != nil {
		return b.jitter(d)
	}
	return equalJitter(d)
}

// equalJitter returns a delay uniformly drawn from [d/2, d]: never less
// than half the backoff, so the exponential growth still bites, but
// spread enough that many pods dropped at once do not reconnect in step.
func equalJitter(d time.Duration) time.Duration {
	half := d / 2
	if half <= 0 {
		return d
	}
	return half + rand.N(d-half+1)
}

func positiveOr(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

func (s *k8sGranuleSandbox) stopForward() {
	s.forwardMu.Lock()
	cancel := s.forwardCancel
	done := s.forwardDone
	s.forwardCancel = nil
	s.forwardDone = nil
	s.forwardMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

// Tools implements Sandbox: the same four tools every remote backend
// exposes, run over the configured sandbox transport.
func (s *k8sGranuleSandbox) Tools(ctx context.Context) ([]mcp.Tool, error) {
	return mcp.NewSSHSandboxTools(s.runner, s.owner.cfg.Workspace), nil
}

// ConfigureGitCredentials points this guest's git at the proxy over the
// same transport every tool call uses.
func (s *k8sGranuleSandbox) ConfigureGitCredentials(ctx context.Context, remoteURL, token string, identity mcp.GitIdentity) error {
	if err := mcp.ConfigureGitCredentialsOverSSH(s.runner, remoteURL, token, identity); err != nil {
		return fmt.Errorf("orchestrator: configuring git credentials in granule sandbox pod %q: %w", s.podName, err)
	}
	return nil
}

// PlaceFile implements SandboxPlacer, the same way K8sSandboxes.PlaceFile
// does over its own transport.
func (s *k8sGranuleSandbox) PlaceFile(ctx context.Context, path, content, mode string) error {
	if err := mcp.PlaceFileOverSSH(ctx, s.runner, path, content, mode); err != nil {
		return fmt.Errorf("orchestrator: placing %s in granule sandbox pod %q: %w", path, s.podName, err)
	}
	return nil
}

// PlaceFiles places multiple files in a single batched operation when the
// transport supports PutFiles, or sequentially otherwise.
func (s *k8sGranuleSandbox) PlaceFiles(ctx context.Context, files []mcp.FilePlacement) error {
	if err := mcp.PlaceFilesOverSSH(ctx, s.runner, files); err != nil {
		return fmt.Errorf("orchestrator: placing files in granule sandbox pod %q: %w", s.podName, err)
	}
	return nil
}

// RunShell implements SandboxShell, granuleSandbox.RunShell's
// counterpart over this backend's transport. Without it a grain CI step
// here went through the run_command tool instead, which clamps every
// command to run_command's own ten-minute ceiling whatever the job's
// timeout-minutes said, and reports nothing until the step ends.
func (s *k8sGranuleSandbox) RunShell(ctx context.Context, req ShellRequest) (ShellResult, error) {
	var workspace string
	if s.owner != nil {
		workspace = s.owner.cfg.Workspace
	}
	return runGuestShell(ctx, s.runner, workspace, req, "granule sandbox pod "+strconv.Quote(s.podName))
}

// RunInSandbox implements SandboxCommander.
func (s *k8sGranuleSandbox) RunInSandbox(ctx context.Context, argv []string, stdin string) (string, string, int) {
	return s.runner.Run(ctx, argv, stdin)
}

// RunAsRootInSandbox implements SandboxRootCommander.
func (s *k8sGranuleSandbox) RunAsRootInSandbox(ctx context.Context, argv []string, stdin string) (string, string, int) {
	if gr, ok := s.runner.(*mcp.GranuleSandboxRunner); ok {
		return gr.RunAsRoot(ctx, argv, stdin)
	}
	return s.runner.Run(ctx, append([]string{"sudo", "-n", "--"}, argv...), stdin)
}

// Rebuild implements SandboxRebuilder: delete this run's pod and create
// it again, at the same name and the same size, waiting for the
// replacement's guest to answer ready before returning.
func (s *k8sGranuleSandbox) Rebuild(ctx context.Context) error {
	s.stopForward()
	defer s.owner.markBuilding(s.podName)()
	if err := s.owner.create(ctx, s.podName, s.shape.orDefault(s.owner.defaultShape())); err != nil {
		return err
	}
	s.runner = s.owner.sandboxRunnerFor(s.podName)
	if err := s.owner.waitReady(ctx, s.podName); err != nil {
		return err
	}
	return s.startForward(ctx)
}

// Release deletes the pod -- the isolation boundary itself, the same way
// K8sSandboxes.Release's doc comment explains.
func (s *k8sGranuleSandbox) Release(ctx context.Context) error {
	s.owner.mu.Lock()
	delete(s.owner.live, s.name)
	s.owner.mu.Unlock()
	s.stopForward()
	if closer, ok := s.runner.(io.Closer); ok {
		_ = closer.Close()
	}
	s.owner.fit.Release(s.podName)
	// Detached from ctx: a caller releasing because its run was cancelled
	// or timed out hands in a context that is already done, and the pod
	// has to go regardless -- the same reasoning deleteQuietly follows.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), k8sGranuleCleanupTimeout)
	defer cancel()
	podErr := s.owner.client.Delete(ctx, s.podName, false)
	_ = s.owner.client.DeleteSecret(ctx, granule.SandboxSecretName(s.podName))
	s.owner.cleanupAccessSpec(s.podName)
	if podErr != nil {
		return fmt.Errorf("orchestrator: deleting granule sandbox pod %q: %w", s.podName, podErr)
	}
	return nil
}

// granuleExecPrefix is what every command goes through in a granule pod
// -- the one-line difference in shape docs/k8s-granule-design.md §5
// describes: `kubectl exec <pod> -- granule exec -- <argv>` instead of
// `kubectl exec <pod> -- <argv>`. It is mcp.KubectlExecRunner.Through
// rather than a prefix put on argv here so that the runner's exit-status
// trailer runs inside the guest, after granule exec, where it can tell a
// guest session that was torn down from a command that finished.
var granuleExecPrefix = []string{"granule", "exec", "--"}
