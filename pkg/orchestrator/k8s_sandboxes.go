package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bwsalmon/grain/pkg/agent"
	"github.com/bwsalmon/grain/pkg/k8s"
	"github.com/bwsalmon/grain/pkg/mcp"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/pkgcache"
)

// K8sConfig is what K8sSandboxes needs to create and reach one pod per
// run in a Kubernetes cluster.
//
// It is the third Sandboxes backend, alongside HostSandboxes (a
// directory, no isolation) and GranuleSandboxes (a VM on a docker host).
// The trade it makes is worth stating plainly, because it is not the
// same one GranuleSandboxes makes: a pod is a container, so a run in one
// shares the node's kernel with every other pod on that node, where a
// granule VM does not share a kernel with anything. What it buys instead
// is that the isolation, the scheduling, the sizing and the teardown are
// all the cluster's own primitives -- so a deployment that already runs
// a cluster gets sandboxes that can be spread across nodes, and a
// deployment that has one machine keeps every reason to run granule.
type K8sConfig struct {
	// Instance is the k8s.InstanceLabel value every pod is created
	// with and the orphan sweep and Readopt select on: the deployment's
	// effective id (model.EffectiveDeploymentID), so deployments sharing
	// a namespace never reap or adopt each other's pods. Empty labels
	// nothing and lists every grain sandbox pod. SetInstance replaces it.
	Instance string
	// Namespace is where sandbox pods are created (k8s.DefaultNamespace
	// when empty) -- the same namespace deploy/kubernetes/ installs
	// grain into, and the one its RBAC grants pod and pod/exec
	// permissions in.
	Namespace string
	// Kubectl is the binary pkg/k8s and the exec transport both run;
	// empty means "kubectl", found on PATH.
	Kubectl string
	// Kubeconfig is the kubeconfig file path passed as --kubeconfig to
	// pkg/k8s.Client and mcp.KubectlExecRunner; empty leaves kubectl's
	// own default discovery ($KUBECONFIG or in-cluster).
	Kubeconfig string
	// Image is the sandbox image every pod runs. Required: it is the
	// whole of what a dispatched agent finds when it lands, so this
	// package has no default worth guessing (Dockerfile's own sandbox
	// stage is what a deployment normally points it at).
	Image string
	// ServiceAccount is the sandbox pod's Kubernetes identity. Normally
	// empty: nothing in a sandbox should be talking to the API server,
	// and pkg/k8s does not mount a token into one regardless.
	ServiceAccount string
	// Command replaces the sandbox image's own entrypoint; empty leaves
	// k8s.DefaultCommand, which is what makes a pod sit still and wait
	// to be exec'd into.
	Command []string
	// Env is set on every sandbox container -- a place for the handful
	// of variables a deployment's own image expects, not for
	// credentials: a capability's own material is placed into the
	// sandbox at dispatch (SandboxPlacer) precisely so it is not sitting
	// in a pod spec any `kubectl get pod -o yaml` can read.
	Env map[string]string
	// NodeSelector pins sandbox pods to a subset of nodes, e.g. a node
	// pool sized for build workloads.
	NodeSelector map[string]string
	// Privileged runs sandbox containers privileged. A deployment that
	// sets it has given every dispatched agent root on the node, and it
	// is here only because a sandbox that must run docker or kind needs
	// it -- see k8s.PodOptions.Privileged.
	Privileged bool
	// PriorityClasses names the PriorityClass each kind of sandbox pod
	// runs at (Shape.Class) -- see K8sPriorityClasses. The zero value
	// sets none on any pod.
	PriorityClasses K8sPriorityClasses
	// StrictPriorityClasses fails a pod naming a PriorityClass the
	// cluster does not have (k8s.ErrNoPriorityClass) rather than
	// creating it with none (k8sPriorityGuard). Off by default.
	StrictPriorityClasses bool
	// Workspace is the working directory run_command/read_file/
	// edit_file/write_file operate in inside each pod.
	Workspace string
	// ReadyTimeout bounds how long Acquire waits for a freshly created
	// pod to become Ready before giving up (DefaultK8sReadyTimeout if
	// zero). Unlike a VM boot this is mostly an image pull, which on a
	// cold node is the slowest thing between a dispatch and its first
	// tool call.
	ReadyTimeout time.Duration
	// ReadyPollInterval is how often Acquire re-asks the API server
	// while waiting out ReadyTimeout (k8s.DefaultPollInterval if zero).
	ReadyPollInterval time.Duration
	// UnschedulableTimeout bounds how long, within ReadyTimeout, Acquire
	// lets the scheduler go on saying no node can take the pod before it
	// fails with the scheduler's own message (DefaultK8sUnschedulableTimeout
	// if zero) -- see that constant for why it is shorter.
	UnschedulableTimeout time.Duration
	// DefaultCPUs, DefaultMemoryMB and DefaultDiskGB seed the
	// deployment-wide default pod shape a run that requested no shape of
	// its own falls back to (model.Config.SandboxCPUs/SandboxMemoryMB/
	// SandboxDiskGB), exactly as GranuleConfig's fields of the same names
	// do. Zero in a dimension means this deployment named none and the
	// backend falls through to grain's own DefaultShape -- every pod is
	// sized explicitly either way.
	DefaultCPUs     int
	DefaultMemoryMB int
	DefaultDiskGB   int
	// CPUOvercommit and MemoryOvercommit seed the deployment-wide
	// overcommit ratios (model.Config.SandboxCPUOvercommit /
	// SandboxMemoryOvercommit) used to scale down pod CPU and memory
	// requests while keeping limits at the full sandbox shape.
	CPUOvercommit    float64
	MemoryOvercommit float64
	// DiskOvercommit seeds model.Config.SandboxDiskOvercommit, which
	// scales each pod's ephemeral-storage request down the same way while
	// its ephemeral-storage limit stays at the full sandbox disk.
	DiskOvercommit float64
	// DefaultNetworkAccess seeds the deployment-wide network a run that
	// asked for none of its own gets (model.Config.NetworkAccess).
	// Empty reads as model.NetworkOpen.
	DefaultNetworkAccess model.NetworkAccess
	// NetworkPolicyEnforced is this deployment saying that its cluster's
	// CNI actually enforces NetworkPolicy objects.
	//
	// It exists because "no network access" is a promise, and the only
	// way to keep it for a pod is a NetworkPolicy -- which is an object
	// every cluster will happily accept and only some clusters enforce
	// (kubenet, and several managed offerings with their network plugin
	// left off, silently ignore them). Applying one to a cluster that
	// ignores it would hand a task that asked to reach nothing a sandbox
	// that reaches everything, with nothing anywhere saying so.
	//
	// So this backend refuses model.NetworkNone outright unless a
	// deployment has said this, and honours it by labelling the pod for
	// the policy deploy/kubernetes/networkpolicy.yaml selects on when it
	// has. An operator turning this on is asserting something about
	// their cluster that grain cannot check from inside a pod; the flag
	// that sets it says so.
	NetworkPolicyEnforced bool
}

// K8sPriorityClasses is the PriorityClass a sandbox pod of each
// Shape.Class is created with (k8s.PodOptions.PriorityClassName).
//
// The point of them is a full cluster. With every sandbox pod at the same
// priority the scheduler has nothing it may evict to make room, so an
// interactive session -- a person waiting -- fails to schedule behind CI
// jobs that could have been rerun. deploy/kubernetes/ ships the three
// DefaultK8sPriorityClasses names, ordered interactive > agent > ci, with
// only the interactive class allowed to preempt and the CI class never
// preempting anything; a preempted CI job is retried as infrastructure
// rather than failed (ci.PreemptionReporter).
//
// An empty name sets no priorityClassName on that kind's pods, which is
// what a deployment whose cluster does not carry the classes wants. A name
// the cluster has no PriorityClass for is created, if it is one of grain's
// own, or else left off that kind's pods with an operator alert until it
// exists (k8s_priority_classes.go); only a backend with
// StrictPriorityClasses fails pod creation on it, with
// k8s.ErrNoPriorityClass saying to apply the manifests.
type K8sPriorityClasses struct {
	Interactive string
	Agent       string
	CI          string
}

// DefaultK8sPriorityClasses is the three classes deploy/kubernetes/
// defines -- what a deployment configured nothing of its own gets.
func DefaultK8sPriorityClasses() K8sPriorityClasses {
	return K8sPriorityClasses{
		Interactive: k8s.PriorityClassInteractive,
		Agent:       k8s.PriorityClassAgent,
		CI:          k8s.PriorityClassCI,
	}
}

// For is the class a pod of class runs at; "" (an acquire that named no
// class) is SandboxClassAgent's.
func (p K8sPriorityClasses) For(class SandboxClass) string {
	switch class {
	case SandboxClassInteractive:
		return p.Interactive
	case SandboxClassCI:
		return p.CI
	default:
		return p.Agent
	}
}

// DefaultK8sReadyTimeout bounds Acquire's wait for a sandbox pod to
// become Ready. Ten minutes rather than the two a granule VM gets: the
// wait here can be a cluster autoscaler bringing up a node for the pod
// (DefaultK8sUnschedulableTimeout) followed by an image pull onto that
// node, which has never seen the sandbox image, and a multi-gigabyte
// pull over a cold cache is minutes rather than seconds.
const DefaultK8sReadyTimeout = 10 * time.Minute

// DefaultK8sUnschedulableTimeout bounds how long Acquire waits on a pod
// the scheduler keeps marking Unschedulable -- "0/1 nodes are available:
// 1 Insufficient memory" -- before failing the run with that message.
//
// Without it such a pod sat out the whole of DefaultK8sReadyTimeout, and
// then failed saying only that it was still Pending: a sandbox larger
// than every node (the 8Gi default on a kind node with 7.75Gi
// allocatable) looked like a slow image pull. It is not an immediate
// failure because the condition can clear on its own -- a node still
// releasing a just-deleted pod's requests, or a cluster autoscaler
// bringing up a node for a burst that outran the warm headroom
// (deploy/kubernetes/headroom.yaml). GKE takes two to three and a half
// minutes to boot a node VM and register its kubelet, so six minutes
// leaves that room with margin; a deployment whose autoscaler is slower
// still raises UnschedulableTimeout.
const DefaultK8sUnschedulableTimeout = 6 * time.Minute

func unschedulableTimeout(configured time.Duration) time.Duration {
	if configured > 0 {
		return configured
	}
	return DefaultK8sUnschedulableTimeout
}

// PodNamePrefix is prepended to a sandbox's own name to make its pod
// name, so every pod this deployment owns is identifiable as its own.
//
// It can be a readable word where GranuleSandboxes' own VMNamePrefix has
// to be two bytes: a pod name is a DNS-1123 label with 63 bytes to
// spend, and nothing derives a network interface name from it.
const PodNamePrefix = "grain-sandbox-"

// maxPodNameLen is Kubernetes' own limit on a DNS-1123 label, which is
// what a pod name has to be.
const maxPodNameLen = 63

// NetworkLabel and NetworkNoneValue mark a sandbox pod as one whose task
// asked to reach nothing -- the label
// deploy/kubernetes/networkpolicy.yaml's own podSelector matches, and
// the only thing standing between such a task and the internet. See
// K8sConfig.NetworkPolicyEnforced for why a deployment has to opt in
// before this is trusted to mean anything.
const (
	NetworkLabel     = "grain.dev/network"
	NetworkNoneValue = "none"
)

// K8sSandboxes builds one Kubernetes pod per run and deletes it when the
// run is done -- the same lifecycle GranuleSandboxes gives a VM, over the
// cluster's own primitives.
type K8sSandboxes struct {
	cfg    K8sConfig
	client k8s.Client
	// fit admits each pod against the cluster's free capacity before it
	// is created (admitPod); nil in a backend not built by its
	// constructor, which then admits everything.
	fit *k8s.FitChecker
	// priority holds back the PriorityClasses the cluster does not
	// have (k8s_priority_classes.go).
	priority k8sPriorityGuard

	mu sync.Mutex
	// image is SetImage's replacement for cfg.Image (live_image.go).
	image liveImage
	// instance is SetInstance's replacement for cfg.Instance.
	instance liveInstance
	live     map[string]*k8sSandbox
	// building holds the pod names an Acquire or Rebuild is creating and
	// waiting on, which are in no live map yet and must not be swept.
	building map[string]bool
	// defaults is the deployment-wide default pod shape the next Acquire
	// falls back to, replaceable while the daemon runs (SetDefaultShape)
	// -- see GranuleSandboxes' own field of the same name for why it
	// lives here rather than in cfg.
	defaults         Shape
	cpuOvercommit    float64
	memoryOvercommit float64
	diskOvercommit   float64
	// packageCache answers where this deployment's package cache is, and
	// the CA it intercepts TLS with, for the next pod built -- see
	// SetPackageCache. Nil until the daemon has a cache to say.
	packageCache func(context.Context) podPackageCache
}

// podPackageCache is the package-cache configuration one pod was built
// with: its base URL and its CA, either of which may be empty.
type podPackageCache struct {
	base  string
	caPEM []byte
}

func (c podPackageCache) empty() bool { return strings.TrimSpace(c.base) == "" && len(c.caPEM) == 0 }

// DefaultK8sWorkspace is the default working directory inside a Kubernetes
// sandbox pod when K8sConfig.Workspace is unset, matching Dockerfile.k8s-sandbox
// and ContainerSandboxes.
const DefaultK8sWorkspace = "/home/agent"

// NewK8sSandboxes returns a K8sSandboxes configured by cfg.
func NewK8sSandboxes(cfg K8sConfig) *K8sSandboxes {
	if cfg.Workspace == "" {
		cfg.Workspace = DefaultK8sWorkspace
	}
	client := k8s.Client{
		Kubectl:    cfg.Kubectl,
		Kubeconfig: cfg.Kubeconfig,
		Namespace:  cfg.Namespace,
	}
	return &K8sSandboxes{
		cfg:      cfg,
		client:   client,
		fit:      newFitChecker(client, cfg.NodeSelector),
		live:     map[string]*k8sSandbox{},
		building: map[string]bool{},
		defaults: Shape{
			CPUs: cfg.DefaultCPUs, MemoryMB: cfg.DefaultMemoryMB, DiskGB: cfg.DefaultDiskGB,
			NetworkAccess: cfg.DefaultNetworkAccess,
		},
		cpuOvercommit:    cfg.CPUOvercommit,
		memoryOvercommit: cfg.MemoryOvercommit,
		diskOvercommit:   cfg.DiskOvercommit,
	}
}

// SetDefaultShape replaces the deployment-wide default the next Acquire
// falls back to, so a change made in Settings reaches the next sandbox
// built rather than the next restart -- GranuleSandboxes.SetDefaultShape's
// counterpart, and the reason liveConfig can treat the two backends
// alike.
func (k *K8sSandboxes) SetDefaultShape(shape Shape) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.defaults = shape
}

// SetImage implements SandboxImageSetter: the image the next sandbox is
// created from.
func (k *K8sSandboxes) SetImage(image string) { k.image.set(image) }

// CurrentImage implements SandboxImageSetter.
func (k *K8sSandboxes) CurrentImage() string { return k.image.get(k.cfg.Image) }

// SetInstance implements SandboxInstanceSetter.
func (k *K8sSandboxes) SetInstance(id string) { k.instance.set(id) }

// CurrentInstance implements SandboxInstanceSetter.
func (k *K8sSandboxes) CurrentInstance() string { return k.instance.get(k.cfg.Instance) }

// pods is k.client labelling and listing under the instance currently
// in force -- what every call that creates or lists pods goes through.
// EnsurePriorityClasses implements K8sPriorityClassEnsurer.
func (k *K8sSandboxes) EnsurePriorityClasses(ctx context.Context, now time.Time) (K8sPriorityClassStatus, bool) {
	return ensureK8sPriorityClasses(ctx, &k.priority, k.client, k.cfg.PriorityClasses, "grain.yaml", now)
}

func (k *K8sSandboxes) pods() k8s.Client {
	c := k.client
	c.Instance = k.CurrentInstance()
	return c
}

// DefaultShape reads back the deployment-wide default pod shape currently
// in effect on this backend.
func (k *K8sSandboxes) DefaultShape() Shape {
	return k.defaultShape()
}

func (k *K8sSandboxes) defaultShape() Shape {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.defaults
}

// SetOvercommit replaces the deployment-wide CPU, memory and disk
// overcommit ratios the next pod built scales its resource requests by, so a change
// in Settings (or from the auto-overcommit controller) reaches the next
// sandbox pod without a restart.
func (k *K8sSandboxes) SetOvercommit(cpu, memory, disk float64) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.cpuOvercommit = cpu
	k.memoryOvercommit = memory
	k.diskOvercommit = disk
}

// Overcommit reads back the CPU, memory and disk overcommit ratios currently in
// effect on this backend.
func (k *K8sSandboxes) Overcommit() (cpu, memory, disk float64) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.cpuOvercommit, k.memoryOvercommit, k.diskOvercommit
}

// ClusterCapacity returns the sum of allocatable CPU cores, memory (in MiB)
// and ephemeral storage (in GiB)
// across ready, schedulable cluster nodes matching K8sConfig.NodeSelector.
func (k *K8sSandboxes) ClusterCapacity(ctx context.Context) (cpus, memoryMB, diskGB int, err error) {
	return k.client.ClusterCapacity(ctx, k.cfg.NodeSelector)
}

// FitsNow implements model.ClusterFit: whether a pod committing want,
// built at this backend's defaults otherwise, would be scheduled now.
func (k *K8sSandboxes) FitsNow(want model.Commitment) (bool, string) {
	return commitmentFits(k.fit, k.podShape(commitmentShape(want).orDefault(k.defaultShape())))
}

// ClusterFree describes the cluster's free capacity as this backend last
// read it, for the health pane beside grain's own budget.
func (k *K8sSandboxes) ClusterFree(ctx context.Context) k8s.FreeSummary { return k.fit.Summary(ctx) }

// RefreshClusterFit re-reads the cluster's free capacity if the reading
// FitsNow answers from is stale -- once per reconcile tick.
func (k *K8sSandboxes) RefreshClusterFit(ctx context.Context) { k.fit.Refresh(ctx) }

// HostCapacity returns the cluster's allocatable CPU and memory capacity for
// sandbox pods on this backend.
func (k *K8sSandboxes) HostCapacity(ctx context.Context) (cpus, memoryMB, diskGB int, err error) {
	return k.ClusterCapacity(ctx)
}

// SetPackageCache tells this backend where the deployment's package cache
// is, asked again for every pod it builds -- the same two answers
// orchestrator.Config.PackageCache and PackageCacheCA give a dispatch,
// so a pod is built with exactly what that dispatch will then ask
// ConfigurePackageCacheTransparent for.
//
// It is how a K8s sandbox gets its cache configuration at all. Writing
// it into a running pod needs root, which `kubectl exec` into an image
// that runs as its own user with no sudo cannot be; so the files under
// /etc are prepared before the sandbox container starts instead
// (packageCacheSetup), and the pod reports having them
// (SandboxPackageCacheProvisioner).
func (k *K8sSandboxes) SetPackageCache(base func(context.Context) string, caPEM func(context.Context) []byte) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.packageCache = func(ctx context.Context) podPackageCache {
		var cache podPackageCache
		if base != nil {
			cache.base = base(ctx)
		}
		if caPEM != nil {
			cache.caPEM = caPEM(ctx)
		}
		return cache
	}
}

func (k *K8sSandboxes) packageCacheFor(ctx context.Context) podPackageCache {
	k.mu.Lock()
	hook := k.packageCache
	k.mu.Unlock()
	if hook == nil {
		return podPackageCache{}
	}
	return hook(ctx)
}

// PodNameFor returns the pod name for a sandbox, so something outside
// this package can predict it without calling Acquire.
func (k *K8sSandboxes) PodNameFor(sandbox string) (string, error) {
	return podNameWithPrefix(PodNamePrefix, sandbox)
}

// podNameWithPrefix is PodNameFor's actual check, factored out so
// K8sGranuleSandboxes.PodNameFor can hold itself to the same DNS-1123
// validation without a second copy of it to keep in step -- the two
// backends differ in what runs inside the pod, not in what Kubernetes
// requires of its name.
func podNameWithPrefix(prefix, sandbox string) (string, error) {
	name := prefix + sandbox
	if len(name) > maxPodNameLen {
		return "", fmt.Errorf("orchestrator: sandbox pod name %q is %d bytes, over Kubernetes' %d-byte limit on a "+
			"DNS-1123 label -- a sandbox is named after its run (\"<task id>-<attempt>\", dispatch.RunID), and %q "+
			"leaves %d bytes for one", name, len(name), maxPodNameLen, prefix, maxPodNameLen-len(prefix))
	}
	// Kubernetes' own validation, applied here rather than left to the
	// API server: a name it refuses fails the create several layers
	// down, in a message about label syntax rather than about the run
	// the name came from.
	if !validPodName(name) {
		return "", fmt.Errorf("orchestrator: sandbox pod name %q is not a valid DNS-1123 label (lowercase "+
			"letters, digits and dashes, starting and ending with an alphanumeric) -- a sandbox is named after "+
			"its run (dispatch.RunID)", name)
	}
	return name, nil
}

// validPodName reports whether name is a DNS-1123 label, which is what
// Kubernetes requires of a pod name.
func validPodName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		alnum := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
		if alnum {
			continue
		}
		if c == '-' && i != 0 && i != len(name)-1 {
			continue
		}
		return false
	}
	return true
}

// markBuilding records pod as being built, so SweepOrphans spares it
// before it is live, and returns the func that forgets it again.
func (k *K8sSandboxes) markBuilding(pod string) func() {
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

// Acquire implements Sandboxes: create this run's pod, wait for it to be
// Ready, and hand back a handle that deletes it on Release.
//
// A pod already carrying this name is deleted first rather than adopted,
// for the reason GranuleSandboxes.Acquire deletes a VM: a name means a
// run, so anything already under it is a previous process's leftover or
// the wreckage of a failed Acquire, and neither is a filesystem this run
// should start from.
func (k *K8sSandboxes) Acquire(ctx context.Context, sandbox string, shape Shape) (Sandbox, error) {
	name, err := k.PodNameFor(sandbox)
	if err != nil {
		return nil, err
	}
	resolved := shape.orDefault(k.defaultShape())
	if err := k.checkNetwork(sandbox, resolved); err != nil {
		return nil, err
	}
	defer k.markBuilding(name)()
	cache, err := k.create(ctx, name, resolved)
	if err != nil {
		k.deleteQuietly(ctx, name)
		return nil, err
	}
	if err := k.waitReady(ctx, name); err != nil {
		k.deleteQuietly(ctx, name)
		return nil, unschedulableOutcome(ctx, k.fit, name, k.podShape(resolved), err)
	}

	sb := &k8sSandbox{owner: k, name: sandbox, podName: name, shape: shape, runner: k.execRunner(name), cache: cache, startedAt: time.Now()}
	k.mu.Lock()
	k.live[sandbox] = sb
	k.mu.Unlock()
	return sb, nil
}

// Readopt implements SandboxReadopter: the pod a previous process built
// for sandbox, claimed so ReapOrphans leaves it running. Like the other
// backends' Readopt, the handle is good for Release and nothing else --
// an adopted run's agent reaches its pod through its own mcpserver -- so
// what the pod was built with (its shape, its package cache) is not
// recovered.
func (k *K8sSandboxes) Readopt(ctx context.Context, sandbox string) (Sandbox, error) {
	name, err := k.PodNameFor(sandbox)
	if err != nil {
		return nil, err
	}
	if err := readoptPod(ctx, k.pods(), name); err != nil {
		return nil, err
	}
	sb := &k8sSandbox{owner: k, name: sandbox, podName: name, shape: k.defaultShape(), runner: k.execRunner(name), startedAt: time.Now()}
	k.mu.Lock()
	k.live[sandbox] = sb
	k.mu.Unlock()
	return sb, nil
}

// readoptPod confirms name is still one of this namespace's grain sandbox
// pods -- by the labels ReapOrphans lists them by, so a pod of anything
// else under the same name is not taken.
func readoptPod(ctx context.Context, client k8s.Client, name string) error {
	names, err := client.ListSandboxes(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: readopting sandbox pod %q: %w", name, err)
	}
	if !slices.Contains(names, name) {
		return fmt.Errorf("orchestrator: readopting sandbox pod %q: no such sandbox pod", name)
	}
	return nil
}

// checkNetwork refuses a task asking for a network this deployment has
// no way to actually withhold -- the same refusal HostSandboxes.Acquire
// gives, and for the same reason: the setting's whole point is that a
// job said to reach nothing reaches nothing, so answering it with
// silence is worse than failing the dispatch.
func (k *K8sSandboxes) checkNetwork(sandbox string, shape Shape) error {
	if shape.NetworkAccess != model.NetworkNone || k.cfg.NetworkPolicyEnforced {
		return nil
	}
	return fmt.Errorf("orchestrator: sandbox %q asks for no network access, and this deployment has not said "+
		"its cluster enforces NetworkPolicy (-k8s-network-policy) -- a pod's network can only be taken away by "+
		"a policy object, which a cluster whose CNI ignores them accepts and does nothing with. Turn the flag on "+
		"once deploy/kubernetes/networkpolicy.yaml is installed and enforced, or file this task without a "+
		"network-access override", sandbox)
}

// create builds name's pod, deleting whatever is already under the name
// first and waiting for that delete to finish -- a name is not free
// again until the old object has actually left the cluster.
//
// It reports the package-cache configuration the pod was built with,
// which is empty when there was none to build it with.
func (k *K8sSandboxes) create(ctx context.Context, name string, shape Shape) (podPackageCache, error) {
	if err := k.client.Delete(ctx, name, true); err != nil {
		return podPackageCache{}, fmt.Errorf("orchestrator: deleting stale sandbox pod %q before rebuilding it: %w", name, err)
	}
	podShape := k.podShape(shape)
	if err := admitPod(ctx, k.fit, name, podShape); err != nil {
		return podPackageCache{}, err
	}
	opts := k.podOptions(shape)
	cache := k.packageCacheFor(ctx)
	if !cache.empty() {
		setup, err := packageCacheSetup(cache)
		if err != nil {
			// The same trade applyPackageCache makes: a sandbox that
			// cannot be pointed at the cache downloads from the
			// internet, which is no reason to fail a dispatch.
			log.Printf("orchestrator: sandbox pod %q: preparing its package-cache configuration: %v "+
				"-- it will fetch its packages from the internet instead", name, err)
			cache = podPackageCache{}
		}
		opts.Setup = setup
	}
	if err := k.priority.createPod(ctx, k.pods(), name, podShape, opts, k.cfg.StrictPriorityClasses); err != nil {
		return podPackageCache{}, fmt.Errorf("orchestrator: creating sandbox pod %q: %w", name, err)
	}
	k.fit.Reserve(name, podShape.Requests())
	return cache, nil
}

// CABundlePath is the system CA bundle in a Debian-based image -- what
// update-ca-certificates writes, and what Dockerfile.k8s-sandbox points
// NODE_EXTRA_CA_CERTS at.
const CABundlePath = "/etc/ssl/certs/ca-certificates.crt"

// packageCacheSetupScript is a k8s.Setup's script: it leaves every file
// packageCacheSetup mounts in $1, using nothing but the shell's own
// printf and cat, as the image's own unprivileged user.
//
// Two of those files depend on the image as well as on the cache, which
// is why this is an init container from the sandbox's own image rather
// than files the controller could render alone:
//
//   - the CA bundle is the image's own ($2) with this deployment's CA
//     appended -- exactly what update-ca-certificates would have made of
//     the CA file ConfigurePackageCacheTransparent writes, without the
//     root it needs to run;
//   - dockerd's daemon.json ($3) is the image's own where it has one,
//     since there is no merging it from a shell (configureDockerCache
//     merges in Go, over a transport this pod does not have), and
//     grain's registry mirror where it has none -- the stock image's
//     case.
const packageCacheSetupScript = `set -eu
%s
if [ -n "${GRAIN_CA:-}" ]; then
  { cat "$2" 2>/dev/null || :; echo; cat "$1/ca"; } >"$1/ca-bundle"
fi
if [ -n "${GRAIN_DOCKER:-}" ] && ! cat "$3" >"$1/docker" 2>/dev/null; then
  cat "$1/docker-grain" >"$1/docker"
fi
`

// packageCacheSetup is the k8s.Setup that leaves a pod with every file
// ConfigurePackageCacheTransparent would have written into /etc as root:
// the per-ecosystem files for cache.base, dockerd's registry mirror, and
// cache.caPEM installed and in the system bundle.
func packageCacheSetup(cache podPackageCache) (*k8s.Setup, error) {
	var writes []string
	var mounts []k8s.SetupFile
	write := func(subPath, content string) {
		writes = append(writes, "printf '%s' "+shellQuote(content)+` >"$1/`+subPath+`"`)
	}
	if strings.TrimSpace(cache.base) != "" {
		files, err := pkgcache.SandboxFiles(cache.base, nil)
		if err != nil {
			return nil, err
		}
		for i, file := range files {
			subPath := fmt.Sprintf("file-%d", i)
			write(subPath, file.Content)
			mounts = append(mounts, k8s.SetupFile{SubPath: subPath, MountPath: file.Path})
		}
		docker, err := pkgcache.DockerDaemonJSON(nil, cache.base)
		if err != nil {
			return nil, err
		}
		write("docker-grain", docker)
		writes = append(writes, "GRAIN_DOCKER=1")
		mounts = append(mounts, k8s.SetupFile{SubPath: "docker", MountPath: pkgcache.DockerConfigPath})
	}
	for _, file := range pkgcache.SandboxCAFiles(cache.caPEM) {
		write("ca", file.Content)
		writes = append(writes, "GRAIN_CA=1")
		mounts = append(mounts,
			k8s.SetupFile{SubPath: "ca", MountPath: file.Path},
			k8s.SetupFile{SubPath: "ca-bundle", MountPath: CABundlePath})
	}
	return &k8s.Setup{
		Script: fmt.Sprintf(packageCacheSetupScript, strings.Join(writes, "\n")),
		Args:   []string{CABundlePath, pkgcache.DockerConfigPath},
		Files:  mounts,
	}, nil
}

// podShape resolves a shape that has already been through the
// deployment default against grain's own DefaultShape, so that a
// dimension nobody named still reaches the cluster as a number rather
// than as an absent request the scheduler fills with whatever the
// namespace's LimitRange happens to say.
func (k *K8sSandboxes) podShape(shape Shape) k8s.PodShape {
	resolved := shape.orDefault(DefaultShape())
	cpuOvercommit, memOvercommit, diskOvercommit := k.Overcommit()
	return k8s.PodShape{
		CPUs:             resolved.CPUs,
		MemoryMB:         resolved.MemoryMB,
		DiskGB:           resolved.DiskGB,
		CPUOvercommit:    cpuOvercommit,
		MemoryOvercommit: memOvercommit,
		DiskOvercommit:   diskOvercommit,
	}
}

func (k *K8sSandboxes) podOptions(shape Shape) k8s.PodOptions {
	opts := k8s.PodOptions{
		Image:          k.CurrentImage(),
		ServiceAccount: k.cfg.ServiceAccount,
		Command:        k.cfg.Command,
		Env:            k.cfg.Env,
		NodeSelector:   k.cfg.NodeSelector,
		Privileged:     k.cfg.Privileged,

		PriorityClassName: k.priority.resolve(k.cfg.PriorityClasses.For(shape.Class), k.cfg.StrictPriorityClasses),
	}
	if shape.NetworkAccess == model.NetworkNone {
		opts.Labels = map[string]string{NetworkLabel: NetworkNoneValue}
	}
	return opts
}

func (k *K8sSandboxes) readyTimeout() time.Duration {
	if k.cfg.ReadyTimeout > 0 {
		return k.cfg.ReadyTimeout
	}
	return DefaultK8sReadyTimeout
}

// waitReady blocks until the pod is Ready, or fails saying what the
// kubelet said about why it never will be.
func (k *K8sSandboxes) waitReady(ctx context.Context, name string) error {
	deadline := time.Now().Add(k.readyTimeout())
	if err := k.client.WaitReady(ctx, name, deadline, unschedulableTimeout(k.cfg.UnschedulableTimeout), k.cfg.ReadyPollInterval); err != nil {
		return fmt.Errorf("orchestrator: waiting for sandbox pod %q: %w", name, err)
	}
	return nil
}

// execRunner builds the `kubectl exec` transport for name's pod.
func (k *K8sSandboxes) execRunner(name string) *mcp.KubectlExecRunner {
	return &mcp.KubectlExecRunner{
		Kubectl:    k.cfg.Kubectl,
		Kubeconfig: k.cfg.Kubeconfig,
		Namespace:  k.client.NamespaceOrDefault(),
		Pod:        name,
		Container:  k8s.SandboxContainer,
	}
}

// k8sCleanupTimeout bounds a delete run on behalf of a caller that is
// already on its way out -- the same trade GranuleSandboxes'
// acquireCleanupTimeout makes: unbounded, a wedged API server would pin
// the dispatch goroutine, and a pod that outlives the timeout is left to
// the next SweepOrphans pass.
const k8sCleanupTimeout = 30 * time.Second

// deleteQuietly tears down a pod that never became usable, so a failed
// Acquire leaves nothing behind. The error is dropped for the reason
// GranuleSandboxes.deleteQuietly drops its own: the caller is already
// failing for a reason it can act on.
//
// It runs on a context detached from the caller's cancellation, because
// the ordinary way an Acquire is interrupted -- a stopping daemon, a
// task closed mid-run -- is also the way it would leak: exec.CommandContext
// on an already-cancelled context does not run kubectl at all.
func (k *K8sSandboxes) deleteQuietly(ctx context.Context, name string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), k8sCleanupTimeout)
	defer cancel()
	k.fit.Release(name)
	_ = k.client.Delete(ctx, name, false)
}

// ReapOrphans is SweepOrphans at startup, before anything can dispatch,
// when no pod can belong to this process and so any that exists is a
// leftover -- GranuleSandboxes.ReapOrphans' counterpart, and the same
// code as SweepOrphans for the reason that one gives.
func (k *K8sSandboxes) ReapOrphans(ctx context.Context) (int, error) {
	return k.SweepOrphans(ctx)
}

// SweepOrphans deletes every grain sandbox pod in this deployment's
// namespace that this process is not itself holding -- neither a live
// sandbox nor one an Acquire or Rebuild is building -- and reports how
// many it removed. reconcileSandboxes runs it on every tick, which is
// what reclaims a pod whose Release or failed Acquire could not delete
// it (the API server timing out under kubectl) while the daemon stays up,
// rather than leaving it holding its node's requests until the next
// restart.
//
// It matches on the labels pkg/k8s puts on the pods it creates rather
// than on the name prefix, so a namespace shared with anything else is
// not swept up -- k8s.InstanceLabel among them, under an Instance, so
// another grain deployment sharing the namespace keeps its pods. A pod
// with no instance label at all predates it, and is still swept.
func (k *K8sSandboxes) SweepOrphans(ctx context.Context) (int, error) {
	return sweepOrphanPods(ctx, k.pods(), &k.mu, func(pod string) bool {
		if k.building[pod] {
			return true
		}
		for _, sb := range k.live {
			if sb.podName == pod {
				return true
			}
		}
		return false
	})
}

// sweepOrphanPods is both Kubernetes backends' SweepOrphans: it lists
// the namespace's grain sandbox pods and deletes every one the backend
// is not holding. holds is asked under mu, once per listed pod and just
// before that pod's delete rather than once for the whole list, because
// a name means a run and an Acquire or Rebuild may start building a pod
// under a listed orphan's name while the sweep is still deleting others:
// with one snapshot taken up front, the sweep would go on to delete the
// pod that Acquire had just created. Asking again per pod narrows that to
// the one kubectl delete already in flight, whose target create itself
// deletes and waits out before it creates the replacement.
func sweepOrphanPods(ctx context.Context, client k8s.Client, mu *sync.Mutex, holds func(pod string) bool) (int, error) {
	names, err := client.ListSandboxes(ctx)
	if err != nil {
		return 0, fmt.Errorf("orchestrator: listing sandbox pods to reap: %w", err)
	}

	var reaped int
	var errs []error
	for _, name := range names {
		mu.Lock()
		held := holds(name)
		mu.Unlock()
		if held {
			continue
		}
		if err := client.Delete(ctx, name, false); err != nil {
			errs = append(errs, fmt.Errorf("deleting orphaned sandbox pod %q: %w", name, err))
			continue
		}
		reaped++
	}
	return reaped, errors.Join(errs...)
}

// k8sSandbox is one run's pod.
type k8sSandbox struct {
	owner   *K8sSandboxes
	name    string
	podName string
	// shape is the size this run asked for, kept so Rebuild can create
	// the replacement at the same one -- see granuleSandbox.shape.
	shape  Shape
	runner sandboxRunner
	// cache is the package-cache configuration this pod was built with
	// (K8sSandboxes.create), guarded by owner.mu since Rebuild replaces it.
	cache podPackageCache
	// startedAt is when this process acquired (or readopted) the pod --
	// SandboxHealth.StartedAt.
	startedAt time.Time
}

// ProvisionedPackageCache implements SandboxPackageCacheProvisioner: the
// cache base and CA this pod's /etc was prepared for when it was built.
func (s *k8sSandbox) ProvisionedPackageCache() (string, []byte) {
	s.owner.mu.Lock()
	defer s.owner.mu.Unlock()
	return s.cache.base, s.cache.caPEM
}

func (s *k8sSandbox) Name() string { return s.name }

// PodName is the actual Kubernetes pod name, not the run's own name Name
// returns -- what an operator needs for `kubectl logs`/`kubectl exec`,
// and what the health pane reports as this sandbox's identity within its
// backend.
func (s *k8sSandbox) PodName() string { return s.podName }

// KubePod implements podNamedSandbox.
func (s *k8sSandbox) KubePod() agent.KubePod {
	return agent.KubePod{
		Namespace:  s.owner.client.NamespaceOrDefault(),
		Pod:        s.podName,
		Workspace:  s.owner.cfg.Workspace,
		Kubeconfig: s.owner.cfg.Kubeconfig,
	}
}

// Tools implements Sandbox: the same four tools every remote backend
// exposes, run over `kubectl exec` instead of over SSH. Acquire does not
// return until the pod is Ready, so there is no start-up left to wait
// out here.
func (s *k8sSandbox) Tools(ctx context.Context) ([]mcp.Tool, error) {
	return mcp.NewSSHSandboxTools(s.runner, s.owner.cfg.Workspace), nil
}

// ConfigureGitCredentials points this pod's git at the proxy over the
// same transport every tool call uses.
func (s *k8sSandbox) ConfigureGitCredentials(ctx context.Context, remoteURL, token string, identity mcp.GitIdentity) error {
	if err := mcp.ConfigureGitCredentialsOverSSH(s.runner, remoteURL, token, identity); err != nil {
		return fmt.Errorf("orchestrator: configuring git credentials in sandbox pod %q: %w", s.podName, err)
	}
	return nil
}

// PlaceFile implements SandboxPlacer: a capability's placement written
// into this pod's own filesystem over the same transport every tool call
// uses. Without it every grant that materializes a sandbox-side file
// would fail during preparation, exactly as it did for granule VMs before
// they had a route of their own (SandboxPlacer's doc comment).
func (s *k8sSandbox) PlaceFile(ctx context.Context, path, content, mode string) error {
	if err := mcp.PlaceFileOverSSH(ctx, s.runner, path, content, mode); err != nil {
		return fmt.Errorf("orchestrator: placing %s in sandbox pod %q: %w", path, s.podName, err)
	}
	return nil
}

// RunInSandbox implements SandboxCommander -- see that interface's own
// doc comment: the controller's own route to a command inside this pod,
// which is what writing the package-cache configuration into /etc needs.
func (s *k8sSandbox) RunInSandbox(ctx context.Context, argv []string, stdin string) (string, string, int) {
	return s.runner.Run(ctx, argv, stdin)
}

// RootUnavailable implements SandboxRootless. Unlike a container
// sandbox's `docker exec -u 0`, `kubectl exec` has no way to ask for
// another user, so the pod's own sudo is the only route to root there is
// -- and the stock image carries none, nor is the pod ever privileged.
// It asks rather than assumes, so an image a deployment built with
// passwordless sudo still gets the whole package-cache configuration.
func (s *k8sSandbox) RootUnavailable(ctx context.Context) string {
	_, stderr, exitCode := s.runner.Run(ctx, []string{"sh", "-c", rootProbeScript}, "")
	if exitCode == 0 {
		return ""
	}
	return fmt.Sprintf("sandbox pod image %s: %s", s.owner.CurrentImage(), guestCommandDetail(stderr, exitCode))
}

// Rebuild implements SandboxRebuilder: delete this run's pod and create
// it again, at the same name and the same size, waiting for the
// replacement to be Ready before returning.
//
// The runner needs no replacing: it addresses a pod by name, and the
// name is unchanged -- the same reason granuleSandbox.Rebuild leaves its
// own runner alone.
func (s *k8sSandbox) Rebuild(ctx context.Context) error {
	defer s.owner.markBuilding(s.podName)()
	cache, err := s.owner.create(ctx, s.podName, s.shape.orDefault(s.owner.defaultShape()))
	if err != nil {
		return err
	}
	s.owner.mu.Lock()
	s.cache = cache
	s.owner.mu.Unlock()
	return s.owner.waitReady(ctx, s.podName)
}

// Release deletes the pod. This is the isolation boundary itself, not a
// tidy-up after one: what makes the next run unable to see this one's
// checkout, credentials or leftover processes is that none of it exists
// any more.
func (s *k8sSandbox) Release(ctx context.Context) error {
	s.owner.mu.Lock()
	delete(s.owner.live, s.name)
	s.owner.mu.Unlock()
	s.owner.fit.Release(s.podName)
	// Detached from ctx: a caller releasing because its run was cancelled
	// or timed out hands in a context that is already done, and the pod
	// has to go regardless -- the same reasoning deleteQuietly follows.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), k8sCleanupTimeout)
	defer cancel()
	if err := s.owner.client.Delete(ctx, s.podName, false); err != nil {
		return fmt.Errorf("orchestrator: deleting sandbox pod %q: %w", s.podName, err)
	}
	return nil
}

// k8sPodStatusDetail renders a pod status for an error a human reads.
func k8sPodStatusDetail(st k8s.Status) string {
	detail := string(st.Phase)
	if reason := strings.TrimSpace(st.Reason); reason != "" {
		detail += " (" + reason + ")"
	}
	return detail
}
