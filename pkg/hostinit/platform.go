package hostinit

// Platform names one of the deployment shapes `grain init` can bring
// up -- the host or cluster it is run against, and the orchestrator
// backend (pkg/orchestrator) that implies. See docs/grain-init-platforms.md
// for the full table of what each needs, what isolation it gives, and
// what it gives up; docs/k8s-init-design.md goes deeper on
// PlatformK8sContainer specifically.
//
// All four are implemented end to end. PlatformLinuxKVM and
// PlatformLinuxContainer share almost every step (preflight, below, and
// initFor's own order), differing only where the sandbox backend
// genuinely does -- no granule CLI or guest images to build, no
// /dev/kvm to demand, a different flag naming the same docker gateway
// address to the daemon. PlatformK8sContainer and PlatformK8sGranule
// each share none of initFor's host-build steps with those two: preflight
// reads a cluster instead of a host, and the rest of each path
// (pkg/hostinit/k8s.go, pkg/hostinit/k8s_granule.go) is
// apply-once-and-provision rather than build-and-start-a-daemon. The two
// Kubernetes platforms share almost nothing with *each other* either,
// by design -- see docs/k8s-granule-design.md for why one preflight
// refuses exactly what the other requires. Running agents directly in
// host directories (orchestrator's HostSandboxes) is deliberately not a
// fifth Platform: the README already says that shape is no longer
// supported.
type Platform string

const (
	// PlatformLinuxKVM is a single Linux host with /dev/kvm, running
	// orchestrator.GranuleSandboxes: every sandbox is a real VM with its
	// own kernel, booted by granule over cloud-hypervisor. This is
	// `grain init`'s original target -- everything this package's own
	// doc comment describes.
	PlatformLinuxKVM Platform = "linux-kvm"
	// PlatformLinuxContainer is a single Linux host with no /dev/kvm,
	// running orchestrator.ContainerSandboxes: every sandbox is a plain
	// docker container sharing the host's kernel. Cheaper to satisfy
	// (no nested virt, no three guest images to build, no granule CLI)
	// at the cost of kernel-level isolation between sandboxes and the
	// host -- and much cheaper to bring up: there is nothing here for
	// `grain init` to build but the grain binary itself, so this comes
	// up in well under a minute rather than PlatformLinuxKVM's
	// ten-plus.
	PlatformLinuxContainer Platform = "linux-container"
	// PlatformK8sContainer is a Kubernetes cluster running
	// orchestrator.K8sSandboxes: every sandbox is an ordinary pod,
	// sharing its node's kernel the same way PlatformLinuxContainer's
	// containers do. Needs a cluster, a pullable sandbox image,
	// ReadWriteOnce storage, and -- for any team that ever dispatches a
	// `network: none` task -- a NetworkPolicy-enforcing CNI. Its pods
	// must never be privileged: see docs/k8s-init-design.md, which
	// designs this platform specifically (and explains why it must not
	// be conflated with PlatformK8sGranule). Implemented: its
	// preflight and its entire post-preflight path
	// (apply deploy/kubernetes/grain.yaml once, wait for the rollout,
	// ProvisionOverAPI) live in pkg/hostinit/k8s.go, replacing every
	// one of the host-build steps the two host platforms share.
	PlatformK8sContainer Platform = "k8s-container"
	// PlatformK8sGranule is a Kubernetes cluster running
	// orchestrator.K8sGranuleSandboxes inside a privileged pod on a
	// nested-virt-capable node: every sandbox is a real VM with its own
	// kernel, the same isolation PlatformLinuxKVM gives, scheduled by
	// the cluster instead of run on a single box. Needs nested-virt
	// nodes, a real /dev/kvm inside the pod, and -- unlike
	// PlatformK8sContainer -- privileged pods, which is exactly why the
	// two are separate names rather than two modes of one `-target=k8s`:
	// their preflights contradict each other (privileged required vs.
	// forbidden). Implemented: its preflight and its entire
	// post-preflight path (apply deploy/kubernetes/grain-granule.yaml
	// once, wait for the rollout, ProvisionOverAPI) live in
	// pkg/hostinit/k8s_granule.go. See docs/k8s-granule-design.md.
	PlatformK8sGranule Platform = "k8s-granule"
	// PlatformGKESandboxes brings up an off-cluster GKE Standard sandbox
	// cluster for a controller running outside the cluster: it creates or
	// reuses a GKE Standard cluster in the sandbox project with a
	// nested-virtualization node pool (--enable-nested-virtualization, e.g.
	// c4-standard-96-lssd with --disk-type=hyperdisk-balanced or
	// n2-standard-* with --disk-type=pd-balanced), installs granule's KVM
	// device plugin (third_party/granule/deploy/k8s/kvm-device-plugin.yaml),
	// creates the grain namespace (pod-security.kubernetes.io/enforce=privileged)
	// with a least-privilege grain ServiceAccount + Role/RoleBinding
	// (pods, pods/exec, pods/log), writes /var/lib/grain/kubeconfig, and
	// verifies with scripts/platform-verify/k8s-granule.sh and one
	// throwaway sandbox that clones through the git proxy and fetches
	// through the package cache. Implemented in pkg/hostinit/gke_sandboxes.go.
	PlatformGKESandboxes Platform = "gke-sandboxes"
)

// DefaultPlatform is what `grain init` targets absent an explicit
// -platform: the shape this package has always served, so today's
// command line and today's behavior are unchanged.
const DefaultPlatform = PlatformLinuxKVM

// knownPlatforms is every Platform `grain init` has a name for, in the
// table's own order -- all five implemented. A -platform value that is
// not one of these is a typo or an invented name, rejected as a bad
// flag (Options.validate) rather than reaching preflight at all.
func knownPlatforms() []Platform {
	return []Platform{PlatformLinuxKVM, PlatformLinuxContainer, PlatformK8sContainer, PlatformK8sGranule, PlatformGKESandboxes}
}

func (p Platform) known() bool {
	for _, k := range knownPlatforms() {
		if p == k {
			return true
		}
	}
	return false
}
