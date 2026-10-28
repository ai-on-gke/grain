package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/granule"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/sandboxaddr"
)

// GranuleConfig is what GranuleSandboxes needs to create and reach one
// bwsalmon/granule-managed VM per run. StateDir defaults to
// granule.DefaultStateDir when left zero.
type GranuleConfig struct {
	// StateDir is granule's VM state directory (granule.DefaultStateDir if
	// empty), where granule records one "<name>.json" per VM.
	StateDir string
	// Command is how the granule binary is invoked: its argv prefix,
	// granule.Local when left zero. A deployment whose granule is not on
	// this machine names the wrapper that reaches it -- a grain on macOS
	// driving a Linux VM on the same mac sets
	// {"limactl", "shell", "<vm>", "--", "granule"}, because granule
	// builds only for Linux and so cannot be exec'd there at all.
	//
	// It changes where granule's own state directory lives as well as how
	// granule is reached, which is why granule.Command answers Exists and
	// List rather than this package reading that directory: see
	// granule.Command.Exists.
	Command granule.Command
	// CreateArgs is appended to "granule vm create <name> -state-dir
	// <dir>" verbatim -- guest image,
	// guest SSH port, resource sizing, and anything else a deployment's
	// own "granule vm create" needs beyond a name, since this package has
	// no way to know those on its own (see package granule's doc comment).
	CreateArgs []string
	// SandboxImage, when set, is asked at each create which guest image
	// that VM is to be built from, and its answer replaces whatever
	// granuleImageFlag ("-kontur-image") CreateArgs carries.
	//
	// It is the daemon's sandboximage.Source: the stock guest this build
	// is stamped for, or the custom OCI image this deployment names in
	// Settings (model.Config.SandboxImage) in its place. Asking per create
	// rather than fixing the image at construction is what lets a changed
	// image reach the next sandbox instead of the next restart -- the
	// same live-settings shape SetDefaultShape gives the VM's size.
	//
	// An error fails the create: going ahead with some other image would
	// put a run in a sandbox that is not the one this deployment asked
	// for.
	//
	// Left nil, CreateArgs names the image, exactly as it always has.
	SandboxImage func(ctx context.Context) (string, error)
	// DNS, when set, is asked at each create which nameserver this VM's
	// guest should be told about on its kernel command line ("granule vm
	// create -dns"), and a non-empty answer is appended to CreateArgs.
	//
	// It exists to close the window between a guest's own boot and this
	// deployment's post-boot rewrite of its /etc/resolv.conf
	// (ConfigurePackageCacheTransparent, reached over exec once the
	// guest is up): without it, every lookup a guest's own early boot
	// makes resolves through whatever /etc/resolv.conf its image
	// shipped with -- a baseline this deployment did not choose and,
	// on a deployment running the package cache, one the cache never
	// sees a single lookup through (docs/granule-local-patches.md's "A
	// networked guest's resolver default"). Naming the cache's own
	// resolver here instead reaches the guest on its kernel command
	// line, which granule's own granule-configure-dns applies in early
	// boot, before this package ever gets a shell inside it.
	//
	// cmd/grain wires this to the same function
	// (packageCache.sandboxDNS) that gates the post-boot rewrite, so
	// the two can never disagree: left nil, or answering "" -- the
	// cache is off, or its resolver is not actually serving -- every
	// create leaves this flag off and granule's own default applies,
	// exactly as it did before this field existed. Never pass a
	// resolver nobody is listening on: a guest told one has no name
	// resolution at all until the post-boot rewrite can run, which is
	// worse than the window this closes.
	DNS func(ctx context.Context) string
	// SSHUser is the guest account GranuleSandboxes runs every tool call
	// as -- passed to `granule exec` as GRANULE_EXEC_USER. Still an SSH
	// login into the guest, just one originating inside the VM's own
	// container.
	SSHUser string
	// ExecKeyPath is the private key `granule exec` authenticates to the
	// guest with, as a path *inside the VM's own container* rather than
	// on the host.
	//
	// Optional, and normally empty. `granule run` generates a keypair for
	// each guest it boots and hands the guest the public half on its
	// kernel command line, so `granule exec`'s own default path already
	// holds a key that guest authorizes (bwsalmon/granule's
	// internal/guestkey). Set this only for a custom guest image that
	// authorizes a key of its own instead; the images directory
	// internal/dockervm mounts read-only at /images is the natural place
	// to put one.
	//
	// This used to be required, and grain used to generate and distribute
	// that keypair itself, because granule's baked-in key only worked when
	// the guest disk and the runtime image came out of the same
	// `docker build` -- which a deployment pointing -disk at its own
	// guest build was not doing. The keypair moving into the boot removed
	// the mismatch and the deployment-side key management with it.
	ExecKeyPath string
	// Workspace is the working directory run_command/read_file/edit_file/
	// write_file operate in on each VM.
	Workspace string
	// ReadyTimeout bounds how long Acquire waits for a freshly created
	// VM's guest to become reachable before giving up (2 minutes if
	// zero). Every VM is freshly created now, so unlike before this
	// applies to every Acquire rather than only the first for a slot.
	ReadyTimeout time.Duration
	// ReadyPollInterval is how often Acquire re-probes the guest while
	// waiting out ReadyTimeout (2 seconds if zero).
	ReadyPollInterval time.Duration
	// NetMode, IP and Port are read nowhere and are kept only so that a
	// caller still setting them compiles. They go in a later release.
	//
	// All three described a granule that no longer exists. The vendored
	// third_party/granule deprecates every one of them: "-net" is
	// "deprecated: only the former flat behaviour exists now, and it is
	// the default", and "-ip" and "-port" are "deprecated and ignored" --
	// the container runtime assigns the address the guest takes over
	// (pkg/dockervm: the VM container derives its own --net values, and the
	// guest's ip= parameter, at boot from the identity on the namespace's
	// external interface), and ports are published on the sandbox itself
	// rather than forwarded per VM.
	//
	// So NAT mode, a private subnet per VM and an assigned address are all
	// gone from the layer underneath, and createArgs no longer passes any
	// of the three. cmd/grain warns when a deployment's own flags still set
	// them, rather than failing a start over configuration that has been
	// quietly inert for some time.
	//
	// Assigning a sandbox's address is coming back, but not this way:
	// pkg/sandboxaddr allocates one per sandbox in per-family blocks, and
	// it reaches the VM as a docker run option (granule's own
	// -docker-run-opt) on a user-defined docker network, which is the only
	// route the current granule leaves open.
	//
	// One thing their removed documentation said is worth keeping, because
	// the address work replacing them depends on it. VM containers share a
	// segment unless CreateArgs names a network of its own, and flat mode
	// keeps a guest from abusing that: netshim only lets a guest's frames
	// onto the segment if they carry the MAC and IPv4 address its container
	// was assigned, so a guest cannot ARP-spoof the gateway or a
	// neighbouring sandbox (third_party/granule/pkg/netshim.spliceGuest).
	//
	// That is what makes an address grain assigns usable as identity rather
	// than merely as a label: the guest cannot present a different one. It
	// is the property the host firewall's jump-on-source-address already
	// leans on, and the one a narrower rule -- a family's own block instead
	// of the whole subnet -- would lean on harder.
	NetMode string
	IP      string
	Port    int
	// DefaultCPUs, DefaultMemoryMB and DefaultDiskGB
	// (bwsalmon/agents#534, grain/task-41) seed the deployment-wide
	// default VM shape (model.Config.SandboxCPUs/SandboxMemoryMB/
	// SandboxDiskGB) a run that requested no shape of its own falls
	// back to -- appended to createArgs' own result as
	// "-cpus"/"-memory-mb"/"-disk-size-mb", last, so they win out over
	// anything CreateArgs also happens to set; a run that did request a
	// shape overrides them per dimension (Shape.orDefault). Zero, the
	// default for all three, means this deployment named no size of its
	// own and falls through to grain's own DefaultShape -- every create
	// passes all three flags either way (createArgs).
	//
	// They are only the *starting* value: the shape actually applied to
	// each create lives on GranuleSandboxes itself, where
	// SetDefaultShape replaces it while the daemon runs, so changing it
	// in Settings reaches the next sandbox built rather than only the
	// next restart.
	DefaultCPUs     int
	DefaultMemoryMB int
	DefaultDiskGB   int
	// DefaultNetworkAccess seeds the deployment-wide network a run that
	// asked for none of its own gets (model.Config.NetworkAccess,
	// grain/task-269), exactly as the three defaults above seed the
	// deployment-wide size. Empty means this deployment named nothing,
	// which reads as model.NetworkOpen.
	//
	// Like them it is only the *starting* value: SetDefaultShape
	// replaces it while the daemon runs, so a change in Settings reaches
	// the next sandbox built rather than the next restart.
	DefaultNetworkAccess model.NetworkAccess
	// Addresses, when set, is where every VM's address comes from
	// (pkg/sandboxaddr): grain allocates one and hands it to docker,
	// instead of docker assigning one and grain discovering it afterwards
	// with inspectContainerNetwork.
	//
	// Nil is how every deployment ran before this existed, and still the
	// default: no address is named, docker assigns from whatever network
	// the VM container lands on, and nothing below changes. A deployment
	// opts in with -granule-sandbox-cidr.
	//
	// The route is granule's own -docker-run-opt, which is the only one
	// the current granule leaves open: its "-ip" is "deprecated and
	// ignored" because "the container runtime assigns the address the
	// guest takes over" -- so the address has to be given to the runtime
	// rather than to granule. Its dockervm puts DockerRunOpts straight
	// onto the "docker run" that creates the VM container, saying exactly
	// that: "port publishing, network membership and aliases now belong
	// directly to the VM container, which owns the namespace". It adds no
	// --network of its own, so there is nothing to collide with.
	//
	// Flat mode then does the rest with no further involvement: the guest
	// takes over the address of the container it is booted inside, so an
	// address given to docker arrives in the guest.
	Addresses *sandboxaddr.Pool
	// DockerNetwork is the user-defined docker network those addresses are
	// on, created by ensureNetwork if it is not there.
	//
	// It has to be user-defined and it has to be grain's: docker honours
	// "--ip" only on a network it was given a subnet for, never on the
	// default bridge, so there is no arrangement in which grain assigns an
	// address on 172.17.0.0/16. Required with Addresses and unused
	// without it.
	DockerNetwork string
	// GitProxyHost is the address a guest reaches this deployment's own
	// services at -- cmd/grain's -granule-git-proxy-host, the host-side
	// docker gateway every checkout clones through and the model proxies
	// are advertised on. Empty for a deployment that named none, which is
	// every deployment not running granule sandboxes.
	//
	// It is here for one purpose: enforceGuestNetwork checks the guest
	// can still reach it after the guest's default route has been taken
	// away, which is the difference between "no network" and "no
	// network and no checkout either" (its own doc comment). Nothing
	// else in this package uses it -- the URL a sandbox is actually
	// handed is minted by the daemon, not here.
	GitProxyHost string
	// DNSEgress, if set, is the resolver every sandbox is pointed at --
	// the package cache's interception DNS -- told which guests have
	// model.NetworkNone, so it refuses their queries for anything outside
	// its allowlist instead of forwarding them upstream (see
	// restrictGuestDNS). Nil for a deployment with no such resolver.
	DNSEgress DNSEgressPolicy
	// SandboxServicePorts, if set, is every port this deployment serves a
	// sandbox on at the docker gateway (and at GitProxyHost): the git
	// proxy, the model proxies, the package and Actions caches, and the
	// package cache's resolver and interception listeners. A NetworkNone
	// guest's host firewall chain accepts those ports there and drops the
	// rest, so a host service bound to the wildcard address -- sshd, a
	// local HTTP proxy -- is not a way out (granuleIsolationRuleset).
	//
	// Asked once per NetworkNone Acquire, when that VM's chain is
	// written, so a listener that comes up later is in the next VM's
	// chain rather than any earlier one's. Nil leaves the gateway
	// reachable on every port, which is what this package did before it
	// knew the ports at all.
	SandboxServicePorts func() []SandboxServicePort
}

// SandboxServicePort is one port a sandbox-facing service listens on.
type SandboxServicePort struct {
	// Proto is "tcp" or "udp".
	Proto string
	Port  int
}

func (c GranuleConfig) stateDir() string {
	if c.StateDir != "" {
		return c.StateDir
	}
	return granule.DefaultStateDir
}

// StateDirLabel is the docker label grain puts on every VM container it
// asks granule for, valued with the granule state directory the VM was
// created under (GranuleConfig.StateDir).
//
// granule's own label (granule.VMLabel) names the VM and nothing else, and
// a VM name under VMNamePrefix says nothing about which deployment built
// it. That is enough for the state-file half of SweepOrphans, which only
// ever sees this deployment's own state directory, and not for the
// container half, which reads the whole docker host: without this, two
// daemons sharing a docker host would each reap the other's live VMs as
// containers with no state file of their own.
const StateDirLabel = "grain.granule-state-dir"

func (c GranuleConfig) stateDirLabel() string {
	return StateDirLabel + "=" + c.stateDir()
}

// granuleCommand is how this deployment invokes granule: its own prefix,
// or granule.Local.
func (c GranuleConfig) granuleCommand() granule.Command {
	if len(c.Command) > 0 {
		return c.Command
	}
	return granule.Local
}

func (c GranuleConfig) readyTimeout() time.Duration {
	if c.ReadyTimeout > 0 {
		return c.ReadyTimeout
	}
	return 2 * time.Minute
}

func (c GranuleConfig) readyPollInterval() time.Duration {
	if c.ReadyPollInterval > 0 {
		return c.ReadyPollInterval
	}
	return 2 * time.Second
}

// mibPerGiB converts Shape.DiskGB into the MiB `granule vm create
// -disk-size-mb` takes. grain's own knob is in GiB because a sandbox's
// disk is chosen in whole gigabytes; granule's is in MiB because that is
// the granularity CHV_DISK_SIZE_MB sizes a qcow2 at. Neither is wrong,
// so the conversion lives at the one place they meet (createArgs).
const mibPerGiB = 1024

// GranuleVMMMemoryOverheadMB is the host cgroup memory headroom (in MiB)
// added to a sandbox's guest MemoryMB for cloud-hypervisor / qemu overhead
// when sizing the VM container's cgroup memory limit (-memory-limit-mb).
const GranuleVMMMemoryOverheadMB = 512

// GranuleOOMScoreAdj is the positive oom_score_adj passed on every
// `granule vm create` so the kernel OOM killer picks a sandbox VM
// before the daemon or hostd (-900).
const GranuleOOMScoreAdj = 500

// DefaultShape is the size a sandbox VM is built at when nothing else
// named one -- grain's own granule.DefaultCPUs/DefaultMemoryMB/
// DefaultDiskGB, filled in per dimension by createArgs after a run's own
// request and the deployment-wide default have both had their say.
//
// It exists so that `granule vm create` is never asked to pick a size:
// an unset dimension used to mean "leave the flag off", which handed the
// answer to whichever granule is vendored (2 vCPU/2048 MiB) and, for
// disk, to however large the guest image a deployment built happens to
// be. Both are real numbers a run lives inside, and neither was chosen
// for the job an agent's sandbox does, so grain names all three itself
// and always passes them.
//
// On large hosts (>= LargeHostMinCPUs vCPUs and >= LargeHostMinMemoryMB
// MiB RAM, such as 96-vCPU / 242 GiB Cloudtops), workstation installers
// seed DefaultShapeForHost (8 vCPUs / 16384 MiB RAM) as the
// deployment-wide default so compilation and test suites inside agent
// sandboxes are not bottlenecked on a few vCPUs while keeping this
// baseline constant for small hosts and unconfigured unit tests.
func DefaultShape() Shape {
	return Shape{CPUs: granule.DefaultCPUs, MemoryMB: granule.DefaultMemoryMB, DiskGB: granule.DefaultDiskGB}
}

const (
	// LargeHostMinCPUs and LargeHostMinMemoryMB are the host capacity
	// thresholds at or above which workstation/cloudtop installers seed
	// LargeHostDefaultCPUs and LargeHostDefaultMemoryMB as the
	// deployment-wide default sandbox shape.
	LargeHostMinCPUs         = 32
	LargeHostMinMemoryMB     = 64 * mibPerGiB
	LargeHostDefaultCPUs     = 8
	LargeHostDefaultMemoryMB = 16 * mibPerGiB
)

// DefaultShapeForHost returns the recommended default sandbox VM shape for a
// host with the given vCPU count and total memory in MiB. On large hosts
// (>= 32 vCPUs and >= 64 GiB RAM) it scales the default up to 8 vCPUs and
// 16 GiB RAM; smaller hosts (or unknown capacity) use DefaultShape().
func DefaultShapeForHost(hostCPUs, hostMemoryMB int) Shape {
	s := DefaultShape()
	if hostCPUs >= LargeHostMinCPUs && hostMemoryMB >= LargeHostMinMemoryMB {
		s.CPUs = LargeHostDefaultCPUs
		s.MemoryMB = LargeHostDefaultMemoryMB
	}
	return s
}

// createArgs returns the full argument list Acquire passes to
// granule.Create for a sandbox's VM beyond a name and -state-dir:
// -backend docker first, the only backend this package supports (its
// transport reaches a guest by exec'ing into that VM's docker container,
// which no other backend gives it), so a caller's own CreateArgs never
// needs to repeat it, then cfg.CreateArgs verbatim, then the VM's size --
// the size winning out over anything CreateArgs also happens to set, on
// the theory that a deployment configuring it at all wants it applied
// consistently rather than overridden by a CreateArgs list that is
// otherwise identical across every create call.
//
// -net, -ip and -port are no longer passed. The granule this repository
// vendors deprecates all three: "only the former flat behaviour exists
// now, and it is the default" for -net, and -ip and -port are "deprecated
// and ignored" -- the container runtime assigns the address the guest
// takes over, and ports are published on the sandbox rather than
// forwarded per VM. Passing them described a granule that no longer
// exists, so NetMode, IP and Port are read nowhere now; cmd/grain warns
// when a deployment still sets the flags behind them, and they go in a
// later release.
//
// shape is the size this VM is actually to be created at -- the run's
// own requested size (model.Task's SandboxCPUs/SandboxMemoryMB/
// SandboxDiskGB) already resolved per dimension against the deployment
// default (Shape.orDefault, in create, against whatever default the
// sandboxes currently carry), and resolved once more here against
// grain's own DefaultShape, so that a dimension nobody named still
// reaches granule as a number rather than as a missing flag. This is
// where a per-task override takes effect now:
// a sandbox is built for one run, so the moment it is created is the one
// moment its size is decided. It used to be applied afterwards, by a
// `granule vm update` against a slot's already-created VM, and undone
// by the recreate that followed the run -- both of which existed only
// because the VM outlived the task.
func (c GranuleConfig) createArgs(shape Shape) []string {
	shape = shape.orDefault(DefaultShape())
	args := append([]string{"-backend", granule.BackendDocker}, c.CreateArgs...)
	args = append(args, "-cpus", strconv.Itoa(shape.CPUs), "-memory-mb", strconv.Itoa(shape.MemoryMB))
	// -disk-size-mb sizes the writable qcow2 overlay the VM's own
	// container creates for it (bwsalmon/granule's config.PrepareOverlay),
	// which is otherwise made exactly as large as the guest image it is
	// backed by. granule takes MiB and grain's own knob is in GiB
	// (model.Config.SandboxDiskGB, Shape.DiskGB), so the conversion
	// happens here, at the single point the two vocabularies meet.
	//
	// Only the overlay is ever resized, never the guest image every other
	// VM booting it shares, so this needs the VM to be in granule's
	// overlay disk mode -- which scripts/setup.sh asks for by name
	// (-disk-mode=overlay, also granule's own default), and which a create
	// rejects outright rather than ignoring if it is not.
	//
	// It is passed on every create, the same way -cpus and -memory-mb
	// above are: a deployment that has never chosen a disk size gets
	// grain's own DefaultShape rather than the guest image's own size,
	// which is a few hundred megabytes of slack a build-heavy run spends
	// (the Granulefile's `VM PACK` sizes disk.img to the rootfs plus
	// 20%). granule grows an overlay to the size asked for and refuses to
	// shrink one, so a VM never comes up smaller than this.
	args = append(args,
		"-disk-size-mb", strconv.Itoa(shape.DiskGB*mibPerGiB),
		"-memory-limit-mb", strconv.Itoa(shape.MemoryMB+GranuleVMMMemoryOverheadMB),
		"-oom-score-adj", strconv.Itoa(GranuleOOMScoreAdj),
	)
	return args
}

// granuleImageFlag is granule's own flag naming the image a VM's
// container runs -- which, since bwsalmon/granule#37, is also the guest
// that container boots. cmd/grain names it too, where it fills the flag
// in from this build's own stamp; here it is what SandboxImage's answer
// replaces.
const granuleImageFlag = "-kontur-image"

// createArgsWithImage is createArgs with this deployment's own guest
// image resolved into it -- the whole of what SandboxImage changes about
// a create.
//
// Resolved per create, for the reasons SandboxImage's own doc comment
// gives. With no resolver configured this is createArgs and
// nothing else, so a deployment that customizes nothing pays neither the
// call nor the copy.
//
// override, when non-empty, wins over both -- the image this one VM is to
// be built from rather than the one every VM is. It exists for a serving
// sandbox (serving.go), whose image is assembled per (stock guest,
// repository digest) and is therefore not a property of the deployment at
// all. SandboxImage is not even asked in that case: its answer is this
// deployment's own guest, which is not what was wanted.
func (c GranuleConfig) createArgsWithImage(ctx context.Context, shape Shape, override string) ([]string, error) {
	args := c.createArgs(shape)
	switch {
	case override != "":
		args = withGranuleImage(args, override)
	case c.SandboxImage != nil:
		image, err := c.SandboxImage(ctx)
		if err != nil {
			return nil, fmt.Errorf("orchestrator: resolving the sandbox image to create this VM from: %w", err)
		}
		if image != "" {
			args = withGranuleImage(args, image)
		}
	}
	// Asked last and appended rather than replacing anything already in
	// CreateArgs: no deployment has a reason to set "-dns" there itself
	// today (DNS's own doc comment), so there is nothing to collide
	// with, and the flag package's own "last wins" rule would still do
	// the right thing if one ever did.
	if c.DNS != nil {
		if dns := c.DNS(ctx); dns != "" {
			args = append(args, "-dns", dns)
		}
	}
	return args, nil
}

// withGranuleImage replaces the granuleImageFlag ("-kontur-image")
// argument in args with image, appending the flag when args names none.
//
// Replaces rather than appends-and-lets-the-last-win because granule
// parses these with the standard flag package, where the *last*
// occurrence wins for a repeated flag but a "-kontur-image=x" and a
// "-kontur-image x" mixed together are easy to get wrong -- and because
// the argument list is what an operator reads in a log line when a VM
// came up on the wrong guest. One flag, one value.
func withGranuleImage(args []string, image string) []string {
	out := make([]string, 0, len(args)+2)
	replaced := false
	for i := 0; i < len(args); i++ {
		switch {
		case strings.HasPrefix(args[i], granuleImageFlag+"="):
			out, replaced = append(out, granuleImageFlag+"="+image), true
		case args[i] == granuleImageFlag:
			out, replaced = append(out, granuleImageFlag, image), true
			// Skip the value this flag already had, if it has one.
			if i+1 < len(args) {
				i++
			}
		default:
			out = append(out, args[i])
		}
	}
	if !replaced {
		out = append(out, granuleImageFlag, image)
	}
	return out
}

// GranuleSandboxes builds one bwsalmon/granule-managed VM per run, reached
// by exec'ing into that VM's own docker container, and deletes it when
// the run is done.
//
// It used to hold one VM per slot, created on first use and reused across
// every task dispatched onto that slot -- which meant isolating one task
// from the next had to be added on top, as a delete-and-recreate after
// each run plus a startup pass to rebuild whatever a crashed process left
// behind. Neither exists now: a VM is created for a run and destroyed
// with it, so a task cannot inherit anything from the one before it
// because there is nothing left to inherit. What remains of the startup
// pass is ReapOrphans, which deletes VMs rather than rebuilding them.
type GranuleSandboxes struct {
	// netMu and netReady guard ensureNetwork: the docker network only has
	// to be checked once per process once it is known to be there.
	netMu    sync.Mutex
	netReady bool
	cfg      GranuleConfig

	mu   sync.Mutex
	live map[string]*granuleSandbox
	// building is the VM names an Acquire is in the middle of, keyed by
	// VM name rather than by sandbox name because that is what a sweep
	// has to match against granule's own state directory.
	//
	// It exists for SweepOrphans alone. A VM is real from the moment
	// granule.Create returns and does not reach live until the guest has
	// booted, answered and had its network applied -- minutes later on a
	// deployment building a guest inline -- so a sweep that went by live
	// would delete the VM of a run that was still starting, which is a
	// reclaim that costs a dispatch. Held under the same mutex as live
	// so "is anything using this name" is one answer rather than two.
	building map[string]bool
	// defaults is the deployment-wide default VM shape the next Acquire
	// falls back to, seeded from cfg's own DefaultCPUs/DefaultMemoryMB
	// and replaceable while the daemon runs (SetDefaultShape). It lives
	// here rather than in cfg because cfg is read unsynchronised by
	// every live sandbox's own goroutine; this one field is the only
	// piece of a GranuleSandboxes' configuration that changes after
	// construction, so it is the only one that needs mu.
	defaults Shape
	// breakers is what the last health probe of each live sandbox found,
	// keyed by sandbox name, so that a guest which has stopped answering
	// is not re-probed at full price by every sweep for as long as it
	// stays sick (healthBreaker, health.go). Held under the same mutex
	// as live, which is also where Health prunes it.
	breakers map[string]*healthBreaker

	// clock is what health.go's breaker measures its cooldowns with,
	// nil everywhere but in tests -- see now().
	clock func() time.Time

	// dnsRestricted is the addresses each VM with model.NetworkNone was
	// restricted under at cfg.DNSEgress, keyed by VM name, so that
	// cleanupGranuleHostIsolation releases exactly what was restricted.
	// Held under mu.
	dnsRestricted map[string][]net.IP
	// guestIPs finds the addresses a VM's traffic reaches the host from,
	// nil everywhere but in tests -- see restrictGuestDNS.
	guestIPs func(ctx context.Context, vmName string) ([]net.IP, error)
	// hostNFTUnusable is set when cleanupGranuleHostIsolation finds that
	// nft cannot be run on this host (no nft binary or no privilege/sudoers
	// rule), so later NetworkOpen teardowns do not keep invoking sudo -n nft.
	// Reset by enforceGranuleHostIsolation; guarded by nftLock.
	hostNFTUnusable bool
	// vmExit and consoleLog read a VM container's state and its guest's
	// console for SandboxOOM, nil everywhere but in tests --
	// granule.DockerContainerExit and granule.DockerContainerLogTail.
	vmExit     func(ctx context.Context, vmName string) (granule.ContainerExit, error)
	consoleLog func(ctx context.Context, vmName string) (string, error)
}

// now is the clock the health breaker's own intervals are measured
// against: time.Now, unless a test has replaced it. Nil-safe rather than
// filled in by the constructor, since this package builds bare
// GranuleSandboxes values in a couple of places to reach one method.
func (k *GranuleSandboxes) now() time.Time {
	if k.clock != nil {
		return k.clock()
	}
	return time.Now()
}

// NewGranuleSandboxes returns a GranuleSandboxes configured by cfg.
func NewGranuleSandboxes(cfg GranuleConfig) *GranuleSandboxes {
	return &GranuleSandboxes{
		cfg:      cfg,
		live:     map[string]*granuleSandbox{},
		building: map[string]bool{},
		breakers: map[string]*healthBreaker{},
		defaults: Shape{
			CPUs: cfg.DefaultCPUs, MemoryMB: cfg.DefaultMemoryMB, DiskGB: cfg.DefaultDiskGB,
			NetworkAccess: cfg.DefaultNetworkAccess,
		},
	}
}

// SetDefaultShape replaces the deployment-wide default VM shape every
// later Acquire resolves a run's own request against -- what
// model.Config.SandboxCPUs/SandboxMemoryMB/SandboxDiskGB mean once they
// have been changed in Settings, applied to the next sandbox built
// rather than only to the next process (pkg/cli/daemon.go's
// liveConfig, which calls this once per reconcile tick when any of them
// has changed).
//
// A zero dimension means "no deployment default", exactly as it does at
// construction: the create falls through to grain's own DefaultShape for
// that dimension rather than leaving the flag off. Sandboxes already
// built are untouched -- a VM's size is decided when it is created.
func (k *GranuleSandboxes) SetDefaultShape(shape Shape) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.defaults = shape
}

// DefaultShape reads back the deployment-wide default VM shape currently in
// effect on this backend (what GranuleConfig seeded or SetDefaultShape last
// set).
func (k *GranuleSandboxes) DefaultShape() Shape {
	return k.defaultShape()
}

// defaultShape reads back whatever default SetDefaultShape last set.
func (k *GranuleSandboxes) defaultShape() Shape {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.defaults
}

// maxVMNameLen is how long a granule VM name may be under the docker
// backend: netshim derives "tap-<name>" and "ctl-<name>" from it, and
// Linux caps an interface name at 15 bytes (IFNAMSIZ-1). Checked in
// VMNameFor so an over-long name fails with something that says what the
// budget is and what is spending it, rather than as `granule vm create`
// refusing a tap device name several layers down.
const maxVMNameLen = 11

// VMNamePrefix is prepended to a sandbox's own name to make its VM name,
// so every VM this deployment owns is identifiable as its own -- which is
// what the startup sweep (ReapOrphans) matches on to find VMs a previous
// process left behind.
//
// A constant rather than configuration, because there is nothing left to
// configure. maxVMNameLen leaves 11 bytes for the whole name and a run id
// ("<task id>-<attempt>", dispatch.RunID) needs most of them, so the only
// prefixes that fit at all are one or two bytes -- a choice too narrow to
// be worth an operator's attention, and one where a wrong answer is not a
// preference but a daemon that cannot build a single VM. It was
// -granule-vm-name-prefix until now, defaulting (in scripts/setup.sh
// and terraform/gcp alike) to "granule-", which fit while a VM was
// named after a *slot* ("granule-1") and stopped fitting the moment one
// was named after its run.
//
// Two bytes rather than one: this is the whole of what tells a grain VM
// apart from anything else on the host, and ReapOrphans deletes what it
// matches. Deployments that must not reap each other's VMs get that from
// separate -granule-state-dir values, which is what ReapOrphans actually
// lists from.
const VMNamePrefix = "g-"

// maxRunNameLen is how much of maxVMNameLen is left for the run's own
// name once VMNamePrefix has taken its share.
//
// Nine, one of which "<task id>-<attempt>" (dispatch.RunID) spends on its
// own separator -- so eight digits of task id and attempt combined, and
// "999999-99" sits exactly at the limit. Task ids are a monotonically
// increasing counter (Store.NewTaskID), so that is a ceiling a long-lived
// deployment climbs toward rather than a fixed fact; see
// TestVMNameBudgetCoversRealisticRunIDs for where it actually bites.
const maxRunNameLen = maxVMNameLen - len(VMNamePrefix)

// VMNameFor returns the granule VM name for a sandbox, so something
// outside this package can predict it without calling Acquire.
func (k *GranuleSandboxes) VMNameFor(sandbox string) (string, error) {
	name := VMNamePrefix + sandbox
	if len(name) > maxVMNameLen {
		return "", fmt.Errorf("orchestrator: granule VM name %q is %d bytes, over the %d-byte limit netshim's tap "+
			"device naming imposes -- a sandbox is named after its run (\"<task id>-<attempt>\", dispatch.RunID), "+
			"and %q leaves %d bytes for one",
			name, len(name), maxVMNameLen, VMNamePrefix, maxRunNameLen)
	}
	return name, nil
}

// IsolatesNetwork implements NetworkIsolator: a granule VM has a network
// of its own, so model.NetworkNone is something this backend really
// takes away (enforceGuestNetwork) rather than a promise it cannot keep.
// It is the only backend here that can say yes.
func (k *GranuleSandboxes) IsolatesNetwork() bool { return true }

// ReservedGuestPorts are the guest loopback ports a capability grant may
// not forward into (docs/capabilities-spec.md §5.3.1): the guest's sshd,
// which a granule guest image runs for grain's own transport.
func (k *GranuleSandboxes) ReservedGuestPorts() []int { return []int{22} }

// Acquire implements Sandboxes: create this run's VM, wait for its guest
// to answer, and hand back a handle that deletes it on Release.
//
// A VM that already exists under this name is deleted first rather than
// adopted. Adopting one was the old behaviour and the right one while a
// name meant a slot -- "reuse what's there," the same choice a directory
// that already existed got -- but a name means a run now, so anything
// already carrying it is either a previous process's leftover or a
// half-built VM from a failed Acquire. Either way its filesystem is not
// something this run should start from.
func (k *GranuleSandboxes) Acquire(ctx context.Context, sandbox string, shape Shape) (Sandbox, error) {
	return k.acquire(ctx, sandbox, shape, "", "")
}

// AcquireUnder is Acquire for a sandbox another sandbox created: it takes
// its address from parent's family, so the two share one aligned block and
// releasing parent releases this one with it (sandboxaddr.Pool).
//
// This is the lifecycle half of sub-sandboxes and not the permission half.
// Nothing here decides whether parent may create anything -- there is no
// agent-facing route to this yet, and when there is it will need a
// capability, a per-parent bound on the deployment's sandbox budget, and a
// git scope no wider than parent's. What this gives those is somewhere to
// put the sandbox once they have agreed to it.
//
// Nor does a child survive a restart: Readopt takes back a run's root VM
// and reserves only its family's first address (sandboxaddr.Pool.ReserveRoot),
// so the startup reap destroys whatever was created beneath it. Whatever
// adds that route also has to make children readoptable: label each
// child's container with its parent, and reserve each child at the
// address it holds (docs/non-disruptive-upgrade.md, stage 2d).
//
// A deployment that allocates no addresses (nil Addresses) has no families
// to put one in, so the parent is recorded nowhere and this is Acquire with
// a longer name. That is honest rather than an error: the address is the
// only thing parentage currently changes.
func (k *GranuleSandboxes) AcquireUnder(ctx context.Context, parent, sandbox string, shape Shape) (Sandbox, error) {
	return k.acquire(ctx, sandbox, shape, "", parent)
}

// acquire is Acquire with the image named rather than resolved, which is
// the one thing a serving sandbox needs of it that a run's does not: its
// guest is assembled per repository digest, so the deployment-wide
// resolver has no answer for it (createArgsWithImage's own comment). An
// empty image is Acquire exactly, and is every caller but serving.go.
func (k *GranuleSandboxes) acquire(ctx context.Context, sandbox string, shape Shape, image, parent string) (*granuleSandbox, error) {
	name, err := k.VMNameFor(sandbox)
	if err != nil {
		return nil, err
	}
	// kept is set once this Acquire is certain to return a sandbox, so the
	// address released below is released on exactly the paths that do not.
	kept := false

	// Claimed for the whole of this Acquire, so a reclaim sweeping
	// orphaned VMs off a filling disk does not delete the one this run
	// is still booting (building's own comment). Released on every exit
	// from here: by then the VM is either in live or has been deleted.
	k.claim(name)
	defer k.unclaim(name)

	// Allocated here rather than in create, because create runs again on
	// a Rebuild and the address has to survive one: it is what the host
	// firewall matches this sandbox on, so a replacement VM arriving at a
	// different address would be a sandbox that silently lost its
	// isolation rules. Released on every failure below, so an Acquire that
	// never returned a sandbox does not leak a block.
	var addr netip.Addr
	if k.cfg.Addresses != nil {
		var err error
		if parent == "" {
			addr, err = k.cfg.Addresses.AcquireRoot(sandbox)
		} else {
			addr, err = k.cfg.Addresses.AcquireChild(parent, sandbox)
		}
		if err != nil {
			return nil, fmt.Errorf("orchestrator: no address for sandbox %q: %w", sandbox, err)
		}
		defer func() {
			// Freed unless this Acquire is returning the sandbox that
			// holds it. Every early return below has already deleted the
			// VM, so the address is the only thing left to give back.
			if !kept {
				k.cfg.Addresses.Release(sandbox)
			}
		}()
	}

	if err := k.create(ctx, name, shape, image, addr); err != nil {
		// Even a failed create can leave a VM behind -- `granule vm
		// create` brings up a netns holder and the VM's own container in
		// separate steps, so a failure between them is a half-built VM
		// with nothing holding a handle to it.
		k.deleteQuietly(ctx, name)
		return nil, err
	}

	// Two failures are worth one rebuild before the run pays for them --
	// a VM container found already dead, and a guest that answers but
	// never acquires a default route. See rebuildableGuestFailure for why
	// those two and nothing else. Rebuilding costs a boot; not rebuilding
	// costs the run.
	//
	// The first failure is logged rather than discarded, whatever the
	// second attempt does with it. It carries the guest's own account of
	// why it never routed itself (guestNetworkDump), collected from a
	// guest that no longer exists by the time anybody reads this -- and a
	// rebuild that works is exactly the case where that evidence would
	// otherwise be thrown away, leaving a deployment quietly rebuilding
	// every other VM with nobody the wiser.
	runner, err := k.runnerFor(ctx, name)
	if err != nil {
		reason, worthIt := rebuildableGuestFailure(err)
		if !worthIt {
			k.deleteQuietly(ctx, name)
			return nil, err
		}
		log.Printf("orchestrator: granule VM %q: %s -- rebuilding it once rather than failing this run: %v",
			name, reason, err)
		if err := k.create(ctx, name, shape, image, addr); err != nil {
			k.deleteQuietly(ctx, name)
			return nil, fmt.Errorf("orchestrator: granule VM %q was rebuilt because %s, and creating it again failed: %w",
				name, reason, err)
		}
		if runner, err = k.runnerFor(ctx, name); err != nil {
			k.deleteQuietly(ctx, name)
			// Named as a second failure, so an operator reading this is
			// not left deciding whether it was a one-off boot: this VM
			// has already been built twice on this host, minutes apart.
			return nil, fmt.Errorf("%w -- and this VM had already been rebuilt once because %s", err, reason)
		}
	}

	// The network this run's sandbox is allowed, applied to the guest
	// that is now up. Unlike ensureGuestKVMAccess inside runnerFor, this
	// fails the Acquire when it does not take: a shape is a promise
	// about what this sandbox can reach, and a run dispatched into a
	// sandbox that quietly kept its route is the one outcome worse than
	// a failed create.
	if err := k.enforceGuestNetwork(ctx, name, runner, shape.orDefault(k.defaultShape()), addr); err != nil {
		k.deleteQuietly(ctx, name)
		return nil, err
	}

	sb := &granuleSandbox{owner: k, name: sandbox, vmName: name, shape: shape, image: image, addr: addr, runner: runner, startedAt: k.now()}
	k.mu.Lock()
	k.live[sandbox] = sb
	k.mu.Unlock()
	kept = true
	return sb, nil
}

// create deletes whatever is under name and builds it again. The delete
// is skipped when granule has no state for the name at all: with no saved
// state to read a backend off, "granule vm delete" falls back to the
// static-pod backend and tries to unlink a manifest path (bwsalmon/
// granule's own internal/cli runVMDelete), never reaching the docker
// backend this package builds every VM under -- so it accomplishes
// nothing at best, and at worst fails against a root-owned
// /etc/kubernetes/manifests the daemon does not run as.
func (k *GranuleSandboxes) create(ctx context.Context, name string, shape Shape, image string, addr netip.Addr) error {
	// Before the stale VM is torn down, not after: this is the step that
	// can wait minutes for a guest build, and a delete followed by a
	// long wait would leave the slot with nothing in it for no reason.
	args, err := k.cfg.createArgsWithImage(ctx, shape.orDefault(k.defaultShape()), image)
	if err != nil {
		return err
	}
	stale, err := k.cfg.granuleCommand().Exists(ctx, k.cfg.stateDir(), name)
	if err != nil {
		return fmt.Errorf("orchestrator: asking granule whether VM %q already exists: %w", name, err)
	}
	if stale {
		k.cleanupGranuleHostIsolation(ctx, name)
		if err := k.cfg.granuleCommand().Delete(ctx, k.cfg.stateDir(), name); err != nil {
			return fmt.Errorf("orchestrator: deleting stale granule VM %q before rebuilding it: %w", name, err)
		}
	}
	// The address, when this deployment assigns them, reaches the VM as
	// options on the "docker run" granule builds -- see
	// GranuleConfig.Addresses for why that is the only route left. Each
	// option and each value is its own -docker-run-opt, which is granule's
	// own convention ("so no quoting or splitting convention has to be
	// invented for values that may themselves contain spaces or commas").
	//
	// Appended after cfg.CreateArgs so that a deployment passing
	// -docker-run-opt itself keeps whatever it asked for: granule's list
	// flag clears its saved default on the first one it is given and
	// appends from then on, so both sets arrive.
	// Every VM container is also labelled with the state directory it was
	// created under, so SweepOrphans can find one whose create died before
	// granule saved its state -- and find only this deployment's: another
	// daemon on the same docker host, under its own state directory, is
	// building VMs under the same VMNamePrefix. See stateDirLabel.
	args = append(args, "-docker-run-opt", "--label", "-docker-run-opt", k.cfg.stateDirLabel())
	if addr.IsValid() {
		if err := k.ensureNetwork(ctx); err != nil {
			return err
		}
		args = append(args,
			"-docker-run-opt", "--network", "-docker-run-opt", k.cfg.DockerNetwork,
			"-docker-run-opt", "--ip", "-docker-run-opt", addr.String())
	}
	err = k.cfg.granuleCommand().Create(ctx, k.cfg.stateDir(), name, args...)
	if err != nil && addr.IsValid() && k.networkVanished(ctx) {
		// The network is the one piece of this that can vanish under a
		// running daemon, and a create is where that shows up: docker
		// refuses the VM container with "network ... not found". Bring
		// the network back and build this VM again, once, rather than
		// failing a dispatch that the very next acquire would have
		// served -- measured against real VMs, the first acquire after a
		// `docker network rm` used to fail and only the second recreated
		// it (docs/test-runs/2026-10-02-sandbox-backends-addressing.md).
		k.forgetNetwork()
		if nerr := k.ensureNetwork(ctx); nerr == nil {
			if leftover, xerr := k.cfg.granuleCommand().Exists(ctx, k.cfg.stateDir(), name); xerr == nil && leftover {
				_ = k.cfg.granuleCommand().Delete(ctx, k.cfg.stateDir(), name)
			}
			err = k.cfg.granuleCommand().Create(ctx, k.cfg.stateDir(), name, args...)
		}
	}
	if err != nil {
		// Forgetting the network costs one inspect on the next acquire,
		// and is what lets one removed by some other route still come
		// back without a restart.
		k.forgetNetwork()
		return fmt.Errorf("orchestrator: creating granule VM %q: %w", name, err)
	}
	return nil
}

// networkVanished reports whether this deployment's sandbox network is
// no longer there -- the one cause of a failed create that recreating
// the network repairs. An inspect that cannot say (docker unreachable)
// reads as vanished too, which only costs a create that fails again.
func (k *GranuleSandboxes) networkVanished(ctx context.Context) bool {
	return dockerCommand(ctx, "network", "inspect", k.cfg.DockerNetwork).Run() != nil
}

// dockerClientEnv is the environment this package's own docker commands run
// with: the Docker client's settings, so a deployment whose docker is not
// the local socket still reaches the right daemon; the HOME it finds
// ~/.docker under when DOCKER_CONFIG does not say otherwise; and PATH, for
// the credential helpers the client may exec.
//
// Nothing else, and that is the point. A `docker network create` has no use
// for this daemon's model-provider keys, GitHub tokens or cloud
// credentials, and a child started with a nil Env inherits every one of
// them -- which is what tests/security's exec-env-implicit ratchet exists
// to stop. The same set pkg/agent/custom's mcpServerEnv names, for the same
// reason and with the same property: none of them is a key.
// dockerCommand builds a docker invocation with that environment, so a new
// docker call in this package cannot forget it by being written the obvious
// way.
func dockerCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Env = dockerClientEnv()
	return cmd
}

func dockerClientEnv() []string {
	var env []string
	for _, name := range []string{
		"PATH", "HOME", "DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG",
		"DOCKER_CERT_PATH", "DOCKER_TLS_VERIFY",
	} {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	return env
}

// ensureNetwork makes sure the user-defined docker network this
// deployment's sandbox addresses live on exists, creating it with the
// pool's own CIDR as its subnet.
//
// Done here, lazily, rather than once at startup, because the network is
// state on the docker host rather than in this process: an operator who
// removed it, or a host rebuilt under a daemon that kept running, would
// otherwise leave every create failing until a restart. Once it is there
// this is one "docker network inspect" per create, and the result is
// remembered so it is one per process in the ordinary case.
//
// The subnet is the pool's whole prefix, so docker's own allocator and
// grain's are describing the same space. Nothing is asked of docker's
// allocator -- every container on this network is created with an
// explicit --ip -- but a subnet is what makes --ip legal at all.
func (k *GranuleSandboxes) ensureNetwork(ctx context.Context) error {
	if k.cfg.DockerNetwork == "" {
		return fmt.Errorf("orchestrator: a sandbox address pool needs a docker network to put the " +
			"addresses on (-granule-sandbox-network); docker honours --ip only on a user-defined network")
	}
	k.netMu.Lock()
	defer k.netMu.Unlock()
	if k.netReady {
		return nil
	}
	// An existing network is only the right network if its subnet is the
	// one grain is handing out addresses from. A network left behind by an
	// earlier deployment with a different -granule-sandbox-cidr accepts
	// the inspect and then refuses every container, because docker checks
	// --ip against the subnet it actually has:
	//
	//	invalid endpoint settings: no configured subnet contains IP
	//	address 10.199.0.96
	//
	// That arrives once per acquire, names neither the network's subnet
	// nor grain's, and is followed by a dispatch backoff that makes it
	// look like a scheduling problem. So it is checked here, where both
	// numbers are in hand, and refused rather than worked around: which
	// one is wrong -- the flag or the network -- is the operator's call,
	// and grain deleting a network it did not make is not a repair it
	// should perform on its own.
	if subnet, err := k.networkSubnet(ctx); err == nil {
		want := k.cfg.Addresses.Prefix().String()
		if subnet != "" && subnet != want {
			return fmt.Errorf("orchestrator: docker network %q has subnet %s, but this deployment hands out "+
				"addresses from %s (-granule-sandbox-cidr): every sandbox would be refused its address. "+
				"Either delete that network (docker network rm %s, once nothing is on it) or set the flag "+
				"to %s", k.cfg.DockerNetwork, subnet, want, k.cfg.DockerNetwork, subnet)
		}
		k.netReady = true
		return nil
	}
	// A create that loses a race with another process creating the same
	// network reports that it already exists, which is success here: what
	// this needs is for the network to be there, not to have been the one
	// that made it.
	out, err := dockerCommand(ctx, "network", "create",
		"--subnet", k.cfg.Addresses.Prefix().String(), k.cfg.DockerNetwork).CombinedOutput()
	if err != nil {
		if dockerCommand(ctx, "network", "inspect", k.cfg.DockerNetwork).Run() == nil {
			k.netReady = true
			return nil
		}
		return fmt.Errorf("orchestrator: creating docker network %q for sandbox addresses on %s: %w: %s",
			k.cfg.DockerNetwork, k.cfg.Addresses.Prefix(), err, strings.TrimSpace(string(out)))
	}
	log.Printf("orchestrator: created docker network %q with subnet %s -- every sandbox VM is given an "+
		"address on it rather than taking one docker chose", k.cfg.DockerNetwork, k.cfg.Addresses.Prefix())
	k.netReady = true
	return nil
}

// networkSubnet is the subnet docker says this deployment's sandbox
// network has, or an error when the network is not there at all. An empty
// string with no error is a network that reports no IPAM subnet, which is
// not something a user-defined network grain made would do -- it is left
// to the caller rather than guessed at.
func (k *GranuleSandboxes) networkSubnet(ctx context.Context) (string, error) {
	out, err := dockerCommand(ctx, "network", "inspect", "-f",
		"{{range .IPAM.Config}}{{.Subnet}}{{end}}", k.cfg.DockerNetwork).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// forgetNetwork drops what ensureNetwork remembered, so the next acquire
// looks again.
//
// This is what makes ensureNetwork's own promise true. It is lazy so that
// "an operator who removed it, or a host rebuilt under a daemon that kept
// running" recovers without a restart -- but a result remembered forever
// recovers from neither, and that is exactly how a removed network turned
// into "network grain-sandboxes not found" on every acquire until the
// daemon was restarted. Called wherever a create fails, since a create is
// the only thing that notices.
func (k *GranuleSandboxes) forgetNetwork() {
	k.netMu.Lock()
	defer k.netMu.Unlock()
	k.netReady = false
}

// acquireCleanupTimeout bounds deleteQuietly's own detached delete, the
// same trade granuleSandbox.Release makes with runCleanupTimeout and for
// the same two reasons: unbounded, a wedged `granule vm delete` would
// pin the dispatch goroutine that is already on its way out, and a VM
// that outlives the timeout is ReapOrphans' problem at the next startup.
// Shorter than runCleanupTimeout because nothing is waiting on this VM --
// it never became usable, so there is no guest to shut down gracefully.
const acquireCleanupTimeout = 30 * time.Second

// deleteQuietly tears down a VM that never became usable, so a failed
// Acquire does not leave one running with no handle to release it. The
// error is dropped deliberately: the caller is already failing for a
// reason it can act on, and "the cleanup after that also failed" would
// replace it with a less useful one. ReapOrphans catches whatever this
// misses at the next startup.
//
// It runs on a context detached from the caller's cancellation, exactly
// as granuleSandbox.Release does. granule.Delete execs granule through
// exec.CommandContext, so on an already-cancelled ctx the delete does not
// merely fail -- it never runs at all, and the VM is left behind. That is
// not a rare shutdown-only case: ctx is cancelled whenever the daemon is
// stopping *or* a task was closed mid-run, so the ordinary way an Acquire
// is interrupted is also the way it would leak. Detaching here is what
// makes "a failed Acquire leaves nothing behind" true on the path that
// most needs it.
//
// The host firewall cleanup runs on the detached context too, for the
// same reason: it execs nft, and a NetworkNone chain left behind keeps
// dropping forwarded traffic from that VM's address -- which docker hands
// to the next container, NetworkOpen or not.
func (k *GranuleSandboxes) deleteQuietly(ctx context.Context, name string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), acquireCleanupTimeout)
	defer cancel()
	k.cleanupGranuleHostIsolation(ctx, name)
	// A question that fails is not an answer of "no": this runs on a
	// detached context after an acquire gave up, and the delete it guards
	// is the only thing that reclaims the VM. So an error falls through to
	// the delete, which is idempotent, rather than skipping it.
	//
	// No state is not the same as no VM, though: granule starts the VM's
	// container before it saves the state file, and an Acquire cancelled
	// in between -- the ordinary interruption this function detaches for
	// -- leaves exactly that. So with no state the containers are removed
	// by name instead (granule.RemoveContainers), which is all `granule vm
	// delete` would have done for a docker-backed VM, and which it cannot
	// do without state (see create).
	if exists, err := k.cfg.granuleCommand().Exists(ctx, k.cfg.stateDir(), name); err == nil && !exists {
		_ = granule.RemoveContainers(ctx, name)
		return
	}
	_ = k.cfg.granuleCommand().Delete(ctx, k.cfg.stateDir(), name)
}

// claim and unclaim mark a VM name as one an Acquire is in the middle
// of, so SweepOrphans can tell a VM that is booting from one nothing
// owns. See the building field for why live is not enough on its own.
func (k *GranuleSandboxes) claim(vmName string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.building[vmName] = true
}

func (k *GranuleSandboxes) unclaim(vmName string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.building, vmName)
}

// inUse reports whether this process is using vmName: a live sandbox
// holding it, or an Acquire building it.
func (k *GranuleSandboxes) inUse(vmName string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.building[vmName] {
		return true
	}
	for _, sb := range k.live {
		if sb.vmName == vmName {
			return true
		}
	}
	return false
}

// ReapOrphans deletes every VM under VMNamePrefix in this deployment's
// own state directory,
// and reports how many it removed. A daemon calls it once, at startup,
// before anything can dispatch -- the same moment, and for the same
// reason, orchestrator.RecoverOrphanedRuns reconciles the run rows a dead
// process left behind: at that instant no VM can belong to this process,
// so any that exists is a leftover.
//
// This is what is left of the startup pass that used to rebuild every
// slot's VM. That pass existed because a slot's VM was meant to outlive
// the process and had to be made clean again; these are meant to have
// been deleted already, and the only question is whether something died
// before that could happen.
//
// It is SweepOrphans with nothing yet in flight, and is deliberately the
// same code: at startup no VM is live and none is being built, so "every
// VM under the prefix" and "every VM this process is not using" are the
// same set, and keeping two passes would be keeping two ideas of what a
// leftover is.
func (k *GranuleSandboxes) ReapOrphans(ctx context.Context) (int, error) {
	return k.SweepOrphans(ctx)
}

// SweepOrphans deletes the VMs under VMNamePrefix that this process is
// not using -- neither a live sandbox nor one an Acquire is building --
// and reports how many it removed.
//
// This is what stands behind the disk floor (grain/task-213). A sandbox
// VM's writable root is a qcow2 overlay inside its own container, so
// `granule vm delete`'s `docker rm` is what returns 60-80 GB of CI
// overlay to the volume; a delete that never ran is that much disk held
// by nothing at all until the next restart. ReapOrphans catching it at
// startup was enough while a deployment restarted often and had no floor
// to be wedged under. It is not enough now: a daemon that has been up
// for days and is holding every job for disk should not need a restart
// to reclaim space nothing owns.
//
// Safe to run at any moment, which is the whole point of going by what
// this process is using rather than by what exists. What it deletes is
// a VM no run can reach: nothing holds a handle to it, nothing is
// waiting for its guest, and the only thing it is doing is occupying the
// disk. A VM belonging to *another* daemon on the same host would be
// deleted too -- as ReapOrphans has always done -- because a deployment's
// state directory is its own (GranuleConfig.StateDir), and two daemons
// sharing one were already reaping each other's VMs at startup.
//
// A VM granule has no state for -- its create died after starting the
// container and before saving it -- is found by its container instead,
// and only when that container carries this deployment's StateDirLabel.
// Going by granule's own label alone would read the whole docker host,
// and two daemons sharing a host under separate state directories would
// then reap each other's live VMs, not just their leftovers. A container
// created before that label existed is invisible to this pass.
func (k *GranuleSandboxes) SweepOrphans(ctx context.Context) (int, error) {
	names, err := k.cfg.granuleCommand().List(ctx, k.cfg.stateDir())
	if err != nil {
		return 0, fmt.Errorf("orchestrator: listing granule VMs to reap: %w", err)
	}
	var reaped int
	var errs []error
	stated := make(map[string]bool, len(names))
	for _, name := range names {
		stated[name] = true
		if !strings.HasPrefix(name, VMNamePrefix) || k.inUse(name) {
			continue
		}
		k.cleanupGranuleHostIsolation(ctx, name)
		if err := k.cfg.granuleCommand().Delete(ctx, k.cfg.stateDir(), name); err != nil {
			errs = append(errs, fmt.Errorf("deleting orphaned granule VM %q: %w", name, err))
			continue
		}
		reaped++
	}

	// Then the VMs granule has no state for at all: containers started by
	// a create that died before granule saved the VM (deleteQuietly's own
	// comment), which no state-file listing can see. Found by the label
	// create puts on them (StateDirLabel), so only this deployment's own,
	// and removed by name, since `granule vm delete` cannot reach a VM
	// without state. A failure to ask docker is reported but does not undo
	// what the state-file pass above already reclaimed.
	labelled, err := granule.ListLabeledVMs(ctx, k.cfg.stateDirLabel())
	if err != nil {
		errs = append(errs, fmt.Errorf("listing granule VM containers to reap: %w", err))
	}
	for _, name := range labelled {
		if stated[name] || !strings.HasPrefix(name, VMNamePrefix) || k.inUse(name) {
			continue
		}
		k.cleanupGranuleHostIsolation(ctx, name)
		if err := granule.RemoveContainers(ctx, name); err != nil {
			errs = append(errs, fmt.Errorf("removing containers of stateless granule VM %q: %w", name, err))
			continue
		}
		reaped++
	}
	return reaped, errors.Join(errs...)
}

// granuleSandbox is one run's VM.
type granuleSandbox struct {
	owner  *GranuleSandboxes
	name   string
	vmName string
	// shape is the size this run asked its VM to be, kept so Rebuild can
	// create the replacement at the same one. Acquire's own resolution
	// against the deployment default (Shape.orDefault) happens inside
	// create, so this stays the run's own request rather than a snapshot
	// of the default that was in force when the VM was first built.
	shape Shape
	// image is the guest this VM was built from when it was named rather
	// than resolved, kept for the same reason shape is: a Rebuild has to
	// land on the same one. Empty means the deployment's own, resolved
	// afresh -- which is what a run's sandbox wants, since an upgrade
	// between a create and a rebuild should move it.
	image string
	// addr is the address this VM was created with, zero when this
	// deployment names none. Kept for the reason shape and image are: a
	// Rebuild has to land on the same one, because the address is the
	// sandbox's identity to the host firewall and to anything that has
	// already been told where to reach it.
	addr   netip.Addr
	runner sandboxRunner
	// startedAt is when this process acquired (or readopted) the
	// sandbox -- SandboxHealth.StartedAt. A Rebuild keeps it: the
	// sandbox is the run's, and the run started when it did.
	startedAt time.Time
}

func (s *granuleSandbox) Name() string { return s.name }

// VMName implements vmNamedSandbox: the actual granule VM name
// (VMNameFor's own result), not the run's own name Name returns above --
// what agent.RunConfig.GranuleVM needs to be, so agent/claude's forked
// "mcpserver -granule-vm" names the VM granule itself knows, not this
// sandbox's internal key.
func (s *granuleSandbox) VMName() string { return s.vmName }

// Tools implements Sandbox. The runner was resolved by Acquire, which
// does not return until the guest has answered a command, so there is no
// boot left to wait out here.
func (s *granuleSandbox) Tools(ctx context.Context) ([]mcp.Tool, error) {
	return mcp.NewSSHSandboxTools(s.runner, s.owner.cfg.Workspace), nil
}

// ConfigureGitCredentials points this VM's guest at the proxy over the
// same transport every tool call uses -- what mcp.ConfigureGitCredentials
// does with a plain file write for a host directory.
func (s *granuleSandbox) ConfigureGitCredentials(ctx context.Context, remoteURL, token string, identity mcp.GitIdentity) error {
	if err := mcp.ConfigureGitCredentialsOverSSH(s.runner, remoteURL, token, identity); err != nil {
		return fmt.Errorf("orchestrator: configuring git credentials on granule VM %q: %w", s.vmName, err)
	}
	return nil
}

// PlaceFile implements SandboxPlacer: a capability's placement written
// into this VM's own filesystem over the same transport every tool call
// uses -- what applyPlacements does with os.MkdirAll/os.WriteFile for a
// host directory, and what a granule VM had no route for at all until now
// (SandboxPlacer's own doc comment).
func (s *granuleSandbox) PlaceFile(ctx context.Context, path, content, mode string) error {
	if err := mcp.PlaceFileOverSSH(ctx, s.runner, path, content, mode); err != nil {
		return fmt.Errorf("orchestrator: placing %s on granule VM %q: %w", path, s.vmName, err)
	}
	return nil
}

// RunInSandbox implements SandboxCommander: one command run inside the
// guest over the same transport every tool call uses, for the controller
// itself rather than for an agent. What needs it is the package-cache
// configuration, which lands in /etc and so has to go through sudo --
// see SandboxCommander's own doc comment.
func (s *granuleSandbox) RunInSandbox(ctx context.Context, argv []string, stdin string) (string, string, int) {
	return s.runner.Run(ctx, argv, stdin)
}

// Rebuild implements SandboxRebuilder: delete this run's VM and create
// it again, at the same name and the same size, waiting for the new
// guest to answer a command before returning.
//
// It is Acquire's own create-and-wait pair, reused rather than
// reimplemented -- create deletes whatever is under the name first,
// which is exactly the destroy half of this operation.
//
// Nothing here replaces s.runner, and nothing needs to. The transport
// reaches a guest by exec'ing into the VM's own container, whose name is
// derived from the VM name alone (granule.PodName), so the runner this
// sandbox already holds -- and the separate one a run's forked mcpserver
// holds, which this process could not reach to replace anyway -- address
// the new VM the moment it exists. runnerFor's result is discarded for
// that reason: what is wanted from it is the wait.
//
// A boot that fails the way Acquire rebuilds for -- a container found
// dead, a guest that answers but never routes itself
// (rebuildableGuestFailure) -- is not retried here. Acquire is rebuilding
// on behalf of a run that has no other recourse; here the caller is an
// agent that just asked for a rebuild and can read the failure and ask
// again.
func (s *granuleSandbox) Rebuild(ctx context.Context) error {
	if err := s.owner.create(ctx, s.vmName, s.shape, s.image, s.addr); err != nil {
		return err
	}
	runner, err := s.owner.runnerFor(ctx, s.vmName)
	if err != nil {
		return err
	}
	// The replacement guest boots with a default route like any other,
	// so a run that was promised no network has to be given none again
	// -- this is a fresh VM, and nothing of the old one's routing table
	// came with it. A rebuild that cannot deliver that fails the
	// rebuild, for the reason Acquire's own call fails the acquire.
	return s.owner.enforceGuestNetwork(ctx, s.vmName, runner, s.shape.orDefault(s.owner.defaultShape()), s.addr)
}

// Release deletes the VM. This is the isolation boundary itself, not a
// tidy-up after one: what makes the next run unable to see this one's
// checkout, credentials or leftover processes is that none of it exists
// any more.
func (s *granuleSandbox) Release(ctx context.Context) error {
	s.owner.mu.Lock()
	delete(s.owner.live, s.name)
	s.owner.mu.Unlock()
	// The address goes back with the VM, together with any sandbox created
	// beneath this one (sandboxaddr.Pool.Release walks the family): an
	// address outliving its sandbox is a block this deployment can never
	// hand out again.
	if s.owner.cfg.Addresses != nil {
		s.owner.cfg.Addresses.Release(s.name)
	}
	s.owner.cleanupGranuleHostIsolation(ctx, s.vmName)
	exists, err := s.owner.cfg.granuleCommand().Exists(ctx, s.owner.cfg.stateDir(), s.vmName)
	if err != nil {
		return fmt.Errorf("orchestrator: asking granule whether VM %q still exists: %w", s.vmName, err)
	}
	if !exists {
		// A VM with no state can still have a container: a Rebuild whose
		// create was interrupted after granule started it and before it
		// saved it. See deleteQuietly.
		if err := granule.RemoveContainers(ctx, s.vmName); err != nil {
			return fmt.Errorf("orchestrator: removing the containers of granule VM %q, which has no state: %w", s.vmName, err)
		}
		return nil
	}
	if err := s.owner.cfg.granuleCommand().Delete(ctx, s.owner.cfg.stateDir(), s.vmName); err != nil {
		return fmt.Errorf("orchestrator: deleting granule VM %q: %w", s.vmName, err)
	}
	return nil
}

// sandboxRunner is the transport a sandbox's four tools run over:
// the method set mcp.NewSSHSandboxTools and mcp.ConfigureGitCredentialsOverSSH
// both take, satisfied by mcp.DockerExecRunner.
// Declared here rather than imported because mcp's own equivalent
// (remoteRunner) is unexported -- deliberately, so that package's tests
// can double it -- and runnerFor needs a name for what it returns.
type sandboxRunner interface {
	Run(ctx context.Context, argv []string, stdin string) (stdout, stderr string, exitCode int)
}

// runnerFor returns the transport reaching name's guest, once that guest
// is actually reachable over it -- so a caller holding a runner has
// already waited out the VM's boot.
//
// The wait is waitForGuestReady rather than anything watching a port,
// because there is no port out here to watch: reaching this guest means
// exec'ing into its own container. What that wait asks for is a command
// running inside the guest *and* a default route under it -- see
// guestReadyProbe for why answering alone stopped being enough.
func (k *GranuleSandboxes) runnerFor(ctx context.Context, name string) (sandboxRunner, error) {
	if err := k.waitForGuestReady(ctx, name, time.Now().Add(k.cfg.readyTimeout())); err != nil {
		return nil, fmt.Errorf("orchestrator: waiting for granule VM %q's guest to become reachable: %w", name, err)
	}
	runner := k.execRunner(name)
	k.ensureGuestTmpfs(ctx, name, runner)
	k.ensureGuestKVMAccess(ctx, name, runner)
	k.ensureGuestDockerNetwork(ctx, name, runner)
	return runner, nil
}

// guestTmpfsMount mounts tmpfs on /tmp inside a Granule guest when the
// guest image did not already mount /tmp as tmpfs on boot (Granulefile's
// /etc/fstab entry). Pre-built guest images that predate that entry leave
// /tmp on /dev/vda (ext4 over nested virtio-blk), turning go test binary
// links (/tmp/go-build*) and t.TempDir() SQLite databases into thousands
// of disk writes and fsyncs.
const guestTmpfsMount = `set -e
if [ "$(findmnt -n -o FSTYPE /tmp 2>/dev/null || true)" != "tmpfs" ]; then
  sudo -n mount -t tmpfs -o mode=1777,nosuid,nodev,size=50% tmpfs /tmp
fi
`

// ensureGuestTmpfs runs guestTmpfsMount in name's guest before package-cache
// setup, task execution, or CI steps touch /tmp. Best effort like
// ensureGuestKVMAccess and ensureGuestDockerNetwork: its result cannot fail an
// Acquire.
func (k *GranuleSandboxes) ensureGuestTmpfs(ctx context.Context, name string, runner sandboxRunner) {
	runner.Run(ctx, []string{"sh", "-c", guestTmpfsMount}, "")
}

// guestKVMGrant is what ensureGuestKVMAccess runs in a guest whose
// /dev/kvm the exec account cannot open: the same grant the
// repository's Granulefile bakes into a freshly built image, applied
// to a guest booted from one built before it did.
//
// `id -un` rather than cfg.SSHUser: the account to grant is the one this
// command is actually running as, which is the only account whose access
// the probe measured -- and reading it in the guest keeps a configured
// user name out of a shell command entirely.
//
// The group is required to exist rather than created. Every guest with
// systemd has it (its own sysusers definitions make it), and it is the
// group /dev/kvm is owned by -- so a guest without one is a guest where
// creating a fresh "kvm" group would grant membership of something the
// device is not owned by, which reads as a successful repair and fixes
// nothing.
const guestKVMGrant = `getent group kvm >/dev/null || { echo "this guest has no kvm group" >&2; exit 1; }; ` +
	`sudo -n usermod -aG kvm "$(id -un)"`

// ensureGuestKVMAccess puts the account this sandbox's tools run as into
// the guest's "kvm" group, when that guest has a /dev/kvm the account
// cannot open.
//
// The grant belongs to the guest image -- the Granulefile's `usermod -aG
// kvm debian` makes it, as the guest build has since bwsalmon/agents'
// nested-virtualization work -- and
// baking it there alone means every VM booted from an image built before
// that line has a working /dev/kvm that every tool call is refused,
// until somebody rebuilds and republishes the image. That is the state
// this deployment was actually found in: the host's own KVM had nesting
// on, cloud-hypervisor passed vmx through, the guest had loaded
// kvm_intel and created the device root:kvm 0660 -- and the "debian"
// account was in no group but its own and docker's, so the sandbox
// health pane reported "denied" on every sandbox. Doing it here as well
// makes the grant a property of acquiring a sandbox rather than of the
// vintage of the image it was booted from.
//
// Deliberately silent and cheap in the ordinary case: one probe, the
// same one the health pane reads (nestedVirtProbe), and nothing else
// unless it comes back "denied". Nothing about this can fail an Acquire
// -- a sandbox that cannot run VMs of its own is still a sandbox nearly
// every task can be dispatched into, and the state it is left in is
// exactly what the health pane already has a vocabulary for. What a
// failure does get is a log line, since the alternative is an operator
// reading "denied" on the pane with no account of a repair having been
// tried at all.
//
// The verification is a second probe rather than the grant's own exit
// status, and it needs a *new* session to mean anything: supplementary
// groups are resolved when a session is created, so the session that ran
// usermod keeps the credentials it started with however well the grant
// worked. Every `granule exec` is its own session, so the next Run has
// them -- which is also why one grant at acquire time reaches every tool
// call in the run that follows.
func (k *GranuleSandboxes) ensureGuestKVMAccess(ctx context.Context, name string, runner sandboxRunner) {
	stdout, _, _ := runner.Run(ctx, []string{"sh", "-c", nestedVirtProbe}, "")
	if parseNestedVirt(stdout) != NestedVirtDenied {
		return
	}

	if _, stderr, exitCode := runner.Run(ctx, []string{"sh", "-c", guestKVMGrant}, ""); exitCode != 0 {
		log.Printf("orchestrator: granule VM %q has a /dev/kvm the account its tools run as cannot open, "+
			"and granting that account the kvm group failed (%s) -- this sandbox cannot run VMs of its own",
			name, strings.TrimSpace(stderr))
		return
	}

	stdout, _, _ = runner.Run(ctx, []string{"sh", "-c", nestedVirtProbe}, "")
	if state := parseNestedVirt(stdout); state != NestedVirtReady {
		log.Printf("orchestrator: granule VM %q: granted the kvm group to the account its tools run as, "+
			"but /dev/kvm still reads %q -- this sandbox cannot run VMs of its own",
			name, state)
		return
	}
	log.Printf("orchestrator: granule VM %q: granted the kvm group to the account its tools run as, "+
		"so /dev/kvm is usable in this sandbox -- its guest image predates the guest build's own grant",
		name)
}

// guestDockerNetworkRepair is run inside a guest to ensure that any inner dockerd
// configuration does not create a docker0 interface that conflicts with the host gateway
// (for example, if the guest image was built with a docker0 bip matching 172.30.255.1/24
// or 10.240.0.1/24, or if docker0 has hijacked the gateway address).
const guestDockerNetworkRepair = `set -e
target="$1"
if [ -z "$target" ]; then
  target=$(ip -4 route show default 2>/dev/null | awk '/default/ {print $3}' | head -n1 || true)
fi
[ -n "$target" ] || target="172.30.255.1"
repaired=0
case "$target" in
  10.*)
    alt_bip="172.30.255.1/24"
    alt_pool="172.31.0.0/16"
    orig_bip="10.240.0.1/24"
    orig_pool="10.241.0.0/16"
    ;;
  *)
    alt_bip="10.240.0.1/24"
    alt_pool="10.241.0.0/16"
    orig_bip="172.30.255.1/24"
    orig_pool="172.31.0.0/16"
    ;;
esac

target_prefix=$(echo "$target" | cut -d. -f1-3)
# -F, and two -e patterns rather than one -E alternation: $target and
# $target_prefix are dotted quads, and to -E every one of those dots is
# "any character" -- the prefix 10.0.0 matches the literal text 100000,
# so an unrelated six-digit number anywhere in this ip output or in
# daemon.json reads as a conflict and costs this guest a docker0 and a
# dockerd restart it never needed. -F compares them as the strings they
# are. ($target is strictly redundant beside its own prefix, since
# anything containing it contains that too; both are named because the
# pair is what the check means.)
if ip -4 addr show docker0 2>/dev/null | grep -F -q -e "$target" -e "$target_prefix"; then
  sudo -n ip link del docker0 2>/dev/null || true
  repaired=1
fi
if [ -f /etc/docker/daemon.json ] && grep -F -q -e "$target" -e "$target_prefix" /etc/docker/daemon.json 2>/dev/null; then
  # Both substitutions in one sed, not two: sed -i writes a temp file and
  # renames it over the original, so one invocation leaves daemon.json
  # holding either both edits or neither. As two invocations under set -e,
  # a second that failed after a first that did not left dockerd configured
  # with its bridge address moved to the far family and its address pool
  # still in the colliding one -- a state worse than either end of the
  # repair, and one the guest never got a dockerd restart to even act on.
  # The two expressions are applied in order to each line rather than in
  # two passes over the file, which is the same result here because
  # neither pattern can match what the other one writes.
  sudo -n sed -i -e "s#${orig_bip}#${alt_bip}#g" -e "s#${orig_pool}#${alt_pool}#g" /etc/docker/daemon.json
  repaired=1
fi
if [ "$repaired" -eq 1 ]; then
  sudo -n systemctl restart docker 2>/dev/null || true
fi
`

// ensureGuestDockerNetwork runs guestDockerNetworkRepair in name's guest,
// aimed at this deployment's git proxy address (or, when none is
// configured, the guest's own default gateway). Best effort like
// ensureGuestKVMAccess: its result is not checked, and nothing about it
// can fail an Acquire.
func (k *GranuleSandboxes) ensureGuestDockerNetwork(ctx context.Context, name string, runner sandboxRunner) {
	target := k.cfg.GitProxyHost
	runner.Run(ctx, []string{"sh", "-c", guestDockerNetworkRepair, "sh", target}, "")
}

// guestNetworkNoneCommand is what enforceGuestNetwork runs in a guest
// that is to have no network: it deletes the guest's default routes, and
// then reads the kernel's own tables back to say whether they are gone.
//
// Dropping the default route rather than installing a packet filter is
// the whole trick, and it works because of what a sandbox still has to
// reach. Both of those things are on segments the guest is directly
// connected to -- this deployment's git proxy and model proxies live on
// the host-side docker gateway, which is the guest's own /16 under
// netshim's flat mode, and the control link `granule exec` arrives on is
// a link of its own -- so an on-link route carries them and the default
// route carries nothing but the internet. Taking it away needs no
// nftables, no package the guest image may not have, and no cooperation
// from granule.
//
// It is not, on its own, a boundary: the guest account has passwordless
// root and can add the route straight back. The boundary is the host's
// firewall (enforceGranuleHostIsolation); this is what makes a guest
// that is not trying to get out fail fast with "Network is unreachable"
// rather than hang on packets the host is silently dropping. That root is
// granted on purpose and stays (docs/granule-guest-root.md).
//
// The loop is because a guest may have more than one default route (one
// per interface, or several with different metrics), and `ip route del
// default` removes one at a time. Bounded rather than `while`: a guest
// where something keeps reinstalling the route is a guest this cannot
// make a promise about, and it should say so rather than spin.
//
// The verification is a read of /proc/net/route for IPv4 -- a file read,
// so it is checking the kernel's table rather than trusting the tool
// that just edited it -- and `ip -6 route show default` for IPv6, whose
// /proc form would also list the unreachable ::/0 that a guest with no
// IPv6 at all carries on lo. Destination 00000000 is the default route,
// the same match guestReadyProbe makes for the opposite reason.
//
// PATH is set because these commands run as an unprivileged account
// whose PATH need not carry /usr/sbin, which is where iproute2 puts `ip`
// on a Debian guest; sudo -n because the deletes need root and this must
// never sit waiting for a password, exactly as guestKVMGrant does.
const guestNetworkNoneCommand = `set -e
PATH=/usr/sbin:/sbin:$PATH
for _ in 1 2 3 4 5 6 7 8; do
  ip -4 route show default | grep -q . || break
  sudo -n ip -4 route del default
done
sudo -n ip -6 route flush default >/dev/null 2>&1 || true
if ! awk '$2 == "00000000" { found = 1 } END { exit found }' /proc/net/route; then
  echo "an IPv4 default route is still installed: $(ip -4 route show default)" >&2
  exit 1
fi
if [ -n "$(ip -6 route show default)" ]; then
  echo "an IPv6 default route is still installed: $(ip -6 route show default)" >&2
  exit 1
fi`

// guestGitProxyReachable is the second half of the promise: with the
// default route gone, this deployment's own git proxy has to still be
// reachable, or the sandbox is one where no task can so much as clone.
//
// `ip route get` answers exactly the question worth asking -- which
// route the kernel would actually use for that address, now -- and fails
// with "Network is unreachable" when the answer is none. An address that
// resolves through a *gateway* is one this run would have reached over
// the default route that has just been deleted, so it is refused too:
// the check is that the proxy is on a segment the guest is directly
// connected to, which is what makes taking the default route away safe
// for it.
const guestGitProxyReachable = `set -e
PATH=/usr/sbin:/sbin:$PATH
route="$(ip -4 route get "$1" 2>&1)" || { echo "no route to the git proxy at $1: $route" >&2; exit 1; }
case "$route" in
  *" via "*) echo "the git proxy at $1 is reachable only through a gateway ($route), not on a segment this guest is directly connected to" >&2; exit 1;;
esac`

// enforceGuestNetwork applies shape's NetworkAccess to name's guest, and
// fails the Acquire that called it if it cannot.
//
// model.NetworkOpen -- and an unset access, which reads as open -- is
// nothing to do: a guest comes up with the network its image and
// granule's own networking gave it, which is what "open" means here.
// model.NetworkNone is three things: the sandbox resolver told to stop
// forwarding this guest's queries (restrictGuestDNS), the host's
// firewall told to drop everything from this guest -- forwarded, bridged
// to a neighbour on its segment, or addressed to the host -- that is not
// for its gateway or the git proxy (enforceGranuleHostIsolation), and
// guestNetworkNoneCommand above, followed by a check that the git proxy
// survived it. The first two are the ones that hold: they live outside
// the VM, and the guest's own route table does not, so the route is
// taken away for a task that never tries to reach out at all, and the
// host's rules are what stop one that puts the route back.
//
// Unlike ensureGuestKVMAccess, which repairs a guest image's oversight
// and logs whatever it could not fix, every failure here is fatal to the
// acquire. The difference is what the two are for: a sandbox that cannot
// run nested VMs is still a sandbox nearly every task can use, while a
// sandbox that was promised no network and has one is a sandbox the task
// asking for it must not be dispatched into. There is no state in
// between to hand back, and no vocabulary for one.
//
// It runs after the readiness wait rather than before it, and has to:
// guestReadyProbe waits for a default route to *appear*, since a guest
// whose network units have not finished is one whose clone would fail
// for a reason that reads like a blocked network. So the ordering is
// wait for the route, then take it away -- which also means this cannot
// race the units that install it.
func (k *GranuleSandboxes) enforceGuestNetwork(ctx context.Context, name string, runner sandboxRunner, shape Shape, addr netip.Addr) error {
	if model.NormalizeNetworkAccess(shape.NetworkAccess) != model.NetworkNone {
		return nil
	}
	if err := k.restrictGuestDNS(ctx, name); err != nil {
		return fmt.Errorf("orchestrator: granule VM %q was to have no network access, and this deployment's "+
			"sandbox resolver could not be told to stop forwarding its queries upstream (%v) -- refusing to "+
			"hand over a sandbox that could still reach any nameserver on the internet through it", name, err)
	}
	if err := k.enforceGranuleHostIsolation(ctx, name, addr); err != nil {
		return fmt.Errorf("orchestrator: granule VM %q was to have no network access, and this host's firewall "+
			"could not be set to drop its traffic (%v) -- refusing to hand over a sandbox whose only isolation "+
			"would be a default route its own guest has root enough to put back. NetworkNone on granule needs "+
			"nft on this host, and this daemon able to run it as root (directly, with CAP_NET_ADMIN, or through "+
			"`sudo -n nft`)", name, err)
	}
	if _, stderr, exitCode := runner.Run(ctx, []string{"sh", "-c", guestNetworkNoneCommand}, ""); exitCode != 0 {
		return fmt.Errorf("orchestrator: granule VM %q was to have no network access and its guest's default "+
			"route could not be removed (%s) -- refusing to hand over a sandbox with the network the task "+
			"asking for it must not have", name, guestCommandDetail(stderr, exitCode))
	}
	if k.cfg.GitProxyHost == "" {
		// Nothing to check against, which is a deployment that named no
		// -granule-git-proxy-host -- it has no git proxy for a guest to
		// reach either, so there is no reachability left to lose.
		return nil
	}
	if _, stderr, exitCode := runner.Run(ctx,
		[]string{"sh", "-c", guestGitProxyReachable, "sh", k.cfg.GitProxyHost}, ""); exitCode != 0 {
		return fmt.Errorf("orchestrator: granule VM %q has no network access, as its task asked, but its "+
			"guest can no longer reach this deployment's git proxy at %s (%s) -- every checkout in that "+
			"sandbox would fail, so it is not handed over. The proxy has to be on a segment the guest is "+
			"directly connected to for a sandbox with no default route to reach it",
			name, k.cfg.GitProxyHost, guestCommandDetail(stderr, exitCode))
	}
	log.Printf("orchestrator: granule VM %q: dropped the guest's default route, so this sandbox reaches "+
		"this deployment's own services and nothing else", name)
	return nil
}

// guestCommandDetail renders what a failed guest command said, for an
// error a human reads -- its stderr, or the exit status when it produced
// none (a command killed by the transport, or one that failed a `set -e`
// step silently).
func guestCommandDetail(stderr string, exitCode int) string {
	if detail := strings.TrimSpace(stderr); detail != "" {
		return detail
	}
	return fmt.Sprintf("exited %d with no output", exitCode)
}

// execRunner builds the docker-exec transport for name's VM. Its
// ConnectTimeout is left at guestexec's own default rather than tied to
// cfg.readyTimeout: by the time a caller has a runner back, runnerFor has
// already waited out the boot this config's timeouts describe, and what
// is left for this to ride out is only the ordinary case of a guest that
// blinks mid-task.
func (k *GranuleSandboxes) execRunner(name string) *mcp.DockerExecRunner {
	return &mcp.DockerExecRunner{
		Container: granule.PodName(name),
		User:      k.cfg.SSHUser,
		KeyPath:   k.cfg.ExecKeyPath,
	}
}

// guestReadyProbe is the command waitForGuestReady runs in the guest,
// and it asks for a default route rather than for nothing.
//
// Answering at all stopped being enough when `granule exec` moved from
// SSH to vsock (bwsalmon/granule#46). The agent is deliberately the
// earliest service a guest starts -- ordered Before=sysinit.target, so
// that a guest whose networking failed is still one that can be got into
// to find out why -- and it needs no network to answer. So `true`
// succeeds a second into the boot, while the units that configure the
// NIC from the ip= parameter and install the container's routes
// (granule-net-cmdline, granule-net-routes) are still running. Under SSH
// that gap could not exist: reaching the guest at all *was* the network
// coming up.
//
// A dispatched task is the thing that pays for the difference. It clones
// through the git proxy and installs packages within seconds of the
// sandbox being handed over, and a guest that is reachable but has no
// route off its own segment fails all of it in a way that reads as a
// blocked network rather than as a sandbox handed over too early.
//
// /proc/net/route rather than `ip route`: it is a file read, so this
// does not depend on iproute2 being installed in a guest image or on
// /usr/sbin being on the PATH of the unprivileged account these commands
// run as. Destination 00000000 is the default route.
const guestReadyProbe = `awk '$2 == "00000000" { found = 1 } END { exit !found }' /proc/net/route`

// waitForGuestReady polls the guest through the docker-exec transport
// until it answers *and* has a default route, or deadline passes: a VM
// whose container is up is not yet a VM whose guest has finished booting,
// and a guest that answers is not yet one a task can be dispatched into.
//
// It probes with a runner of its own whose ConnectTimeout is one poll
// interval, so a probe against a guest that is not up yet gives up and
// lets this loop decide whether to keep waiting, rather than each probe
// sitting on guestexec's own 30s default and overshooting the deadline
// this is measuring against.
//
// It fails immediately on a VM container that has already exited rather
// than waiting out the rest of deadline exec'ing into something that will
// never answer.
//
// Two of the ways it gives up have types of their own --
// guestContainerDeadError and guestNoRouteError -- because Acquire
// rebuilds the VM once for either of them.
func (k *GranuleSandboxes) waitForGuestReady(ctx context.Context, name string, deadline time.Time) error {
	probe := k.execRunner(name)
	probe.ConnectTimeout = k.cfg.readyPollInterval()
	started := time.Now()
	var lastErr error
	for {
		_, stderr, exitCode := probe.Run(ctx, []string{"sh", "-c", guestReadyProbe}, "")
		if exitCode == 0 {
			return nil
		}
		lastErr = guestExecProbeError(probe.Container, stderr, exitCode)
		if status, dead := dockerExitedEarly(ctx, name); dead {
			return &guestContainerDeadError{fmt.Errorf("VM container %q exited (status %q) before its guest ever ran a command: %w%s",
				probe.Container, status, lastErr, k.guestFailureDiagnosis(ctx, name))}
		}
		if time.Now().After(deadline) {
			// Both ways out carry the diagnosis, not just the routeless
			// one. A guest that answers can be asked why it has no route
			// and a guest that never answered cannot, but the console
			// half of guestFailureDiagnosis is read from the host and
			// does not care which of the two this is -- and "the guest
			// never came up" with nothing attached is the harder of the
			// two to act on, not the easier.
			//
			// How long the wait really was is stated here because the
			// probe error cannot: what it reports is the last probe's
			// own connect timeout, one poll interval, which on its own
			// reads as an orchestrator that waited two seconds for a
			// booting guest rather than the whole of ReadyTimeout.
			err := fmt.Errorf("gave up after waiting %s for it: %w%s",
				time.Since(started).Round(time.Second), lastErr, k.guestFailureDiagnosis(ctx, name))
			if exitCode > 0 {
				return &guestNoRouteError{err}
			}
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(k.cfg.readyPollInterval()):
		}
	}
}

// guestNetworkDump is what waitForGuestReady asks a guest for on its way
// out, when the guest is answering and has no default route.
//
// The point is that this failure is otherwise unreadable from outside.
// The one guest-side thing that addresses a flat-mode guest whose kernel
// does not act on "ip=" itself -- every guest this deployment runs, since
// scripts/granule builds the Debian-kernel variant -- is
// granule-net-cmdline.service, it runs once in the guest's first seconds,
// and when it does not run the guest still boots and still answers here,
// because granule-agent is ordered ahead of the network on purpose. The
// trace it leaves is a failed unit and a journal line *inside* a guest
// whose sandbox is about to be destroyed, and the same symptom was read
// as a concurrency bug in whichever test caught it three times over
// before anyone got that far (grain tasks 109 and 116). So the answer
// travels back in the error, and an operator reading a red job has the
// evidence rather than an invitation to reproduce it.
//
// Everything in it is best effort and nothing in it can fail the dump: a
// guest image with no iproute2, no systemd or no journal still answers
// with the parts it does have, and a guest that has gone away entirely
// contributes an empty dump rather than an error of its own.
//
// It opens by saying which account it ran as, because the two halves
// that matter most -- the failed units and the journal -- are the two an
// unprivileged account silently cannot read, answering "Failed to
// connect to bus" and "No journal files were opened due to insufficient
// permissions" instead. A dump that says who it is is a dump whose
// empty-looking half can be told apart from a guest that really had
// nothing to say (see guestDumpUser).
const guestNetworkDump = `: granule-guest-dump
echo "## dumped as"; { id -un 2>&1 || true; }
echo "## /proc/cmdline"; cat /proc/cmdline 2>&1
echo "## addresses"; { ip -o addr show 2>&1 || true; }
echo "## routes"; { ip route show 2>&1 || true; }
echo "## /proc/net/route"; cat /proc/net/route 2>&1
echo "## failed units"; { systemctl --no-pager --no-legend --failed 2>&1 || true; }
echo "## granule units"; { systemctl --no-pager --no-legend list-units 'granule-*' 2>&1 || true; }
echo "## granule journal"; { journalctl -b --no-pager -o short-monotonic -u 'granule-*' 2>&1 | tail -40 || true; }
exit 0`

// guestFailureDiagnosis is what every way of giving up on a booting
// guest carries back with it: the guest's own account of its networking
// (guestNetworkDump), asked over the exec transport, and the tail of the
// VM container's console, read from the host. Each is rendered as an
// indented block under a line saying what it is, and each is dropped
// when it comes back empty, so an error this decorates reads exactly as
// it did before when neither source had anything.
//
// Both, rather than either, because they fail in opposite directions. A
// guest that answers but never routed itself can be asked directly, and
// what it says about its own units is the better evidence -- but a guest
// that never got as far as listening on vsock cannot be asked anything
// at all, and that is precisely the boot whose console says why. Neither
// survives the moment this returns: Acquire deletes or rebuilds the VM
// within seconds either way, so whatever is not collected here is gone
// before anybody reads the error (which is why the console tail is
// carried rather than the "run `docker logs` yourself" it replaces).
//
// It runs on a context detached from the caller's cancellation, for the
// same reason deleteQuietly does: a daemon shutting down or a task
// closed mid-run cancels ctx, and a diagnosis that is skipped exactly
// when a run was interrupted is one an operator never gets. Its own
// timeout still bounds it.
func (k *GranuleSandboxes) guestFailureDiagnosis(ctx context.Context, name string) string {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), guestDumpTimeout)
	defer cancel()
	var b strings.Builder
	appendDiagnosisBlock(&b, "What the guest itself says, asked before this sandbox was given up on",
		k.guestSelfReport(ctx, name))
	appendDiagnosisBlock(&b, fmt.Sprintf("The last %d lines of %s's console (`docker logs %s`), where the guest's "+
		"own boot units write whether or not anything inside it can be reached",
		guestConsoleTailLines, granule.PodName(name), granule.PodName(name)),
		k.guestConsoleTail(ctx, name))
	return strings.TrimRight(b.String(), "\n")
}

// guestSelfReport runs guestNetworkDump in name's guest, or returns the
// empty string if the guest is past answering.
//
// It runs as root rather than as cfg.SSHUser, because the two halves of
// the dump that say *why* -- the failed units and the journal -- are the
// two an unprivileged account cannot read.
func (k *GranuleSandboxes) guestSelfReport(ctx context.Context, name string) string {
	dumper := k.execRunner(name)
	dumper.User = guestDumpUser
	dumper.ConnectTimeout = k.cfg.readyPollInterval()
	stdout, _, _ := dumper.Run(ctx, []string{"sh", "-c", guestNetworkDump}, "")
	return strings.TrimSpace(stdout)
}

// guestDumpUser is the guest account guestSelfReport runs the dump as.
//
// It is named rather than left empty, even though guestexec's own
// default *is* root. Empty means "pass no GRANULE_EXEC_USER", and
// `docker exec` runs with the container's own environment -- which under
// this backend already carries GRANULE_EXEC_USER, set on the VM
// container by `granule vm create -guest-user` (bwsalmon/granule's
// dockervm) to the deployment's ordinary sandbox account. So omitting it
// does not fall back to guestexec's default at all; it inherits the
// unprivileged account every other command here runs as, and every dump
// collected that way answered "Failed to connect to bus: No such file or
// directory" for its failed units and "No journal files were opened due
// to insufficient permissions" for its journal, which is to say it
// explained nothing about the failure it existed to explain.
//
// The guest is one this method's caller has already decided not to hand
// to anybody, so there is nothing here that grants a task more than it
// had.
const guestDumpUser = "root"

// guestConsoleTail is the host's half of the diagnosis: the end of the
// VM container's log, which is the guest's serial console (see
// granule.DockerContainerLogTail). Errors are folded into the returned
// text rather than dropped -- "No such container" is itself an answer
// about a VM that was supposed to be booting -- except for the empty
// one, which keeps a container that logged nothing out of the error.
func (k *GranuleSandboxes) guestConsoleTail(ctx context.Context, name string) string {
	tail, err := granule.DockerContainerLogTail(ctx, name, guestConsoleTailLines)
	if err != nil {
		return strings.TrimSpace(err.Error())
	}
	return strings.TrimSpace(tail)
}

// guestConsoleTailLines is how much of the console the diagnosis carries.
// A guest gives up its whole ReadyTimeout before anything here runs, so
// the end of the log is the part where its network units ran and the
// kernel's own boot chatter is already behind it; this is enough to
// cover the units and a little of what preceded them, without pasting a
// whole boot into a daemon log line.
const guestConsoleTailLines = 120

// guestDumpTimeout bounds guestFailureDiagnosis, both halves together.
// It is a diagnostic on the way out of an acquire that has already spent
// its whole ReadyTimeout, so it is short enough not to add meaningfully
// to that and long enough for a journal read in a guest under load.
const guestDumpTimeout = 20 * time.Second

// appendDiagnosisBlock writes one titled, indented section of a
// diagnosis to b, or nothing at all when that source said nothing.
func appendDiagnosisBlock(b *strings.Builder, title, body string) {
	if body == "" {
		return
	}
	b.WriteString(". ")
	b.WriteString(title)
	b.WriteString(":\n")
	for _, line := range strings.Split(body, "\n") {
		b.WriteString("\t")
		b.WriteString(strings.TrimRight(line, "\r"))
		b.WriteString("\n")
	}
}

// guestContainerDeadError is what waitForGuestReady returns when it gives
// up specifically because the VM's own container has already exited or
// died, never for any other probe failure (a timeout, a guest still
// booting, ...) -- see dockerExitedEarly. Acquire matches on this type
// alone (errors.As) to decide whether a failure is worth one rebuild, so
// it stays its own type rather than a sentinel value or a string match
// against the formatted message below.
type guestContainerDeadError struct{ err error }

func (e *guestContainerDeadError) Error() string { return e.err.Error() }
func (e *guestContainerDeadError) Unwrap() error { return e.err }

// guestNoRouteError is what waitForGuestReady returns when it gives up on
// a guest that was answering the whole time and never acquired a default
// route -- never for a guest that could not be reached at all, which is a
// different half of the boot (guestExecProbeError). Like
// guestContainerDeadError it exists so Acquire can match on it with
// errors.As rather than on the wording of a message.
type guestNoRouteError struct{ err error }

func (e *guestNoRouteError) Error() string { return e.err.Error() }
func (e *guestNoRouteError) Unwrap() error { return e.err }

// rebuildableGuestFailure says whether a failure to get a runner for a
// freshly created VM is one of the two worth rebuilding that VM once for,
// and names it for the log line and the error that follow. Everything
// else -- a guest that never answered at all, a cancelled context, a
// granule that failed -- is returned to the caller as it is.
//
// Both are failures of *this boot* rather than of the deployment: a
// container found already dead is one docker started and lost (Acquire's
// own comment), and a guest that answers but never routes itself is the
// one-shot early-boot unit that addresses it having failed, which on a
// loaded host is a unit whose `udevadm settle` timed out (grain task 137,
// and guestNetworkDump's own comment). Neither says anything about the
// next boot, and both cost the same thing if they stand: the whole
// dispatched run, or a whole CI job, on one unlucky boot -- while a
// rebuild costs a boot.
//
// The case this deliberately does not try to tell apart is a deployment
// whose sandboxes genuinely have no gateway to route to, where a routeless
// guest is correct (netshim's Identity.Gateway may be nil). That
// deployment cannot acquire a sandbox either way, because the readiness
// probe requires a default route, so what the rebuild changes for it is
// not success or failure but how long each Acquire takes to report the
// same thing -- and the log line at the point of the rebuild, carrying
// the guest's own account of its networking, says which of the two it is
// looking at without waiting for the second attempt.
func rebuildableGuestFailure(err error) (reason string, worthIt bool) {
	var dead *guestContainerDeadError
	if errors.As(err, &dead) {
		return "its container was found dead", true
	}
	var routeless *guestNoRouteError
	if errors.As(err, &routeless) {
		return "its guest answered but never acquired a default route", true
	}
	return "", false
}

// guestExecProbeError renders one failed waitForGuestReady probe as the
// error a caller sees if the deadline runs out on it, naming the command
// an operator can run by hand to see the same thing.
//
// The two ways it fails are worth telling apart, because they point at
// different halves of the boot. A probe that never ran (exitCode < 0)
// means the guest could not be reached; one that ran and exited non-zero
// means the guest is answering and simply has no default route yet,
// which is the network units still coming up -- or, past the deadline,
// not coming up at all.
func guestExecProbeError(container, stderr string, exitCode int) error {
	detail := strings.TrimSpace(stderr)
	if detail == "" {
		detail = fmt.Sprintf("exited %d with no output", exitCode)
	}
	if exitCode > 0 {
		return fmt.Errorf("the guest in %s answers but has no default route yet (`docker exec %s granule exec -- sh -c %q`: %s)",
			container, container, guestReadyProbe, detail)
	}
	return fmt.Errorf("`docker exec %s granule exec -- sh -c %q`: %s", container, guestReadyProbe, detail)
}

// dockerContainerDead is the set of docker State.Status values
// dockerExitedEarly treats as "this container will never answer" rather
// than "not ready yet" -- see its own doc comment. "created" is
// deliberately absent: a container docker has accepted but not yet
// started is still on its way up, same as "running" itself taking a
// moment to start accepting connections.
var dockerContainerDead = map[string]bool{"exited": true, "dead": true}

// dockerExitedEarly reports whether name's own VM container (not the
// "-netns" holder) has already exited -- see granule.DockerContainerStatus's
// own doc comment for why "vm create" returning success does not mean
// this can't happen. Errors from the status lookup itself (e.g. a
// transient docker daemon hiccup) are treated as "not dead" rather than
// propagated: this is only ever a fast-fail optimization layered on top
// of waitForGuestReady's own deadline, which still applies regardless.
func dockerExitedEarly(ctx context.Context, name string) (status string, dead bool) {
	status, err := granule.DockerContainerStatus(ctx, name)
	if err != nil {
		return "", false
	}
	return status, dockerContainerDead[status]
}

// Readopt implements SandboxReadopter: the VM a previous process built
// for sandbox, claimed so ReapOrphans and SweepOrphans leave it running.
// The handle it returns is good for Release and nothing else -- an
// adopted run's agent reaches its VM through its own mcpserver, not
// through this process -- so no runner is resolved.
//
// On a deployment allocating addresses (GranuleConfig.Addresses) the
// address the VM holds is read off its container and reserved in this
// process's pool (readoptAddress), which starts empty and would otherwise
// hand it out again beside the VM. A VM whose address cannot be read, or
// cannot be reserved, is refused, and its run finished as interrupted.
func (k *GranuleSandboxes) Readopt(ctx context.Context, sandbox string) (Sandbox, error) {
	name, err := k.VMNameFor(sandbox)
	if err != nil {
		return nil, err
	}
	exists, err := k.cfg.granuleCommand().Exists(ctx, k.cfg.stateDir(), name)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: asking granule whether VM %q still exists: %w", name, err)
	}
	if !exists {
		return nil, fmt.Errorf("orchestrator: readopting granule VM %q: granule has no such VM", name)
	}
	var addr netip.Addr
	if k.cfg.Addresses != nil {
		if addr, err = k.readoptAddress(ctx, sandbox, name); err != nil {
			return nil, fmt.Errorf("orchestrator: readopting granule VM %q: %w", name, err)
		}
	}
	sb := &granuleSandbox{owner: k, name: sandbox, vmName: name, runner: k.execRunner(name), shape: k.defaultShape(), addr: addr, startedAt: k.now()}
	k.mu.Lock()
	k.live[sandbox] = sb
	k.mu.Unlock()
	return sb, nil
}

// readoptAddress reserves, in this process's address pool, the address
// the VM name was given by the process that built it: the one its
// container holds inside the pool's prefix, which is what create asked
// docker for (--ip) and enforceGranuleHostIsolation checked it got.
func (k *GranuleSandboxes) readoptAddress(ctx context.Context, sandbox, name string) (netip.Addr, error) {
	pool := k.cfg.Addresses
	nets, err := hostGuestNetworks(ctx, granule.PodName(name))
	if err != nil {
		return netip.Addr{}, fmt.Errorf("reading the address it holds: %w", err)
	}
	for _, n := range nets {
		addr, ok := netip.AddrFromSlice(n.IP)
		if !ok || !pool.Prefix().Contains(addr.Unmap()) {
			continue
		}
		addr = addr.Unmap()
		if err := pool.ReserveRoot(sandbox, addr); err != nil {
			return netip.Addr{}, fmt.Errorf("reserving the address it holds: %w", err)
		}
		return addr, nil
	}
	return netip.Addr{}, fmt.Errorf("its container holds no address in this deployment's pool %s", pool.Prefix())
}
