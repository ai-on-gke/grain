// Package vmspec defines a VM's configuration (VMSpec) and its
// persistence as JSON under granule's state directory. Running a spec
// is pkg/dockervm's job: granule drives a local docker daemon and
// nothing else now that the static-pod backend is gone (a cluster runs
// the same container pair from the pod manifests under deploy/k8s/, which
// this side neither renders nor submits -- see deploy/k8s/gke.md).
//
// Each VM gets one netshim init container plus one VM container, as
// documented in the top-level README -- which is what lets the guest take
// over the sandbox's own address, and gives a simple, independent
// create/update/delete-by-name lifecycle.
package vmspec

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/bwsalmon/granule/pkg/config"

	"github.com/bwsalmon/granule/pkg/netshim"
)

const (
	// ImagesMountPath is the container path -images-hostpath is always
	// mounted at, read-only: it's a shared node-local image cache several
	// VMs may read from concurrently, so it's never made writable
	// regardless of -disk-readonly (see pkg/dockervm). A writable
	// -disk must live under this path, since it's the only place
	// granule ever mounts images from.
	//
	// Must match the literal ":/images:ro" mount in
	// pkg/dockervm/docker.go, and the /images volume in the pod
	// manifests under deploy/k8s/.
	ImagesMountPath = "/images"
)

// VMSpec holds every parameter needed to run one VM, under either
// backend. It is also what gets persisted to the state directory (as
// JSON) so a later "update" can start from the previous values instead of
// requiring every flag to be repeated.
type VMSpec struct {
	Name string `json:"name"`

	DiskImage    string `json:"diskImage"`
	DiskReadOnly bool   `json:"diskReadOnly"`

	// DiskMode is how the VM attaches its disk: config.DiskModeOverlay,
	// DiskModePersistent or DiskModeReadOnly, passed straight through as
	// CHV_DISK_MODE. Empty means "derive it from DiskReadOnly", which is
	// what a spec saved before this existed does.
	//
	// The overlay it names is made inside the VM's own container now, not
	// here: that is why a writable disk no longer needs a host directory
	// of its own, and why nothing on this side has to know that a qcow2
	// is involved at all.
	DiskMode string `json:"diskMode,omitempty"`

	// NetMode is how networking is configured: "inline" or "none", passed
	// straight through as CHV_NET_MODE.
	NetMode string `json:"netMode,omitempty"`

	// DiskSizeMB is how large a disk the guest is given, in MiB, passed
	// straight through as CHV_DISK_SIZE_MB: the VM's container sizes its
	// overlay to it before boot, creating it that large or growing one an
	// earlier boot left behind. Zero means the disk image's own size,
	// which is what an overlay has always been given.
	//
	// Only meaningful in config.DiskModeOverlay -- the overlay is the
	// only disk granule creates, and the image underneath it is shared
	// with every other VM using it, so nothing here ever resizes that.
	DiskSizeMB int `json:"diskSizeMB,omitempty"`

	// ExtentSizeMB is the chunk size in MiB for multi-extent VMDK overlays,
	// passed straight through as CHV_EXTENT_SIZE_MB. Zero means the default
	// 64 MiB extent size.
	//
	// Only meaningful in config.DiskModeOverlay.
	ExtentSizeMB int `json:"extentSizeMB,omitempty"`

	// DiskNumQueues configures virtio-blk multi-queue count (CHV_DISK_NUM_QUEUES).
	// 0 means single queue / hypervisor default.
	DiskNumQueues int `json:"diskNumQueues,omitempty"`

	// DiskIOThreads enables dedicated host iothreads for block devices (CHV_DISK_IOTHREADS).
	DiskIOThreads bool `json:"diskIOThreads,omitempty"`

	// DiskIOEngine configures the host asynchronous I/O backend (CHV_DISK_IO_ENGINE: "io_uring", "epoll", or "sync").
	DiskIOEngine string `json:"diskIOEngine,omitempty"`

	Kernel    string `json:"kernel,omitempty"`
	Initramfs string `json:"initramfs,omitempty"`
	Firmware  string `json:"firmware,omitempty"`
	Cmdline   string `json:"cmdline,omitempty"`

	// CmdlineAuto records whether Cmdline was derived automatically (from
	// DiskMode) rather than given explicitly, so a later "granule vm
	// update" that changes one of those inputs without also passing
	// --cmdline knows to recompute it instead of keeping the now stale
	// value.
	CmdlineAuto bool `json:"cmdlineAuto,omitempty"`

	// CPUs and MemoryMB are what the guest boots with, in vCPUs and MiB
	// (CHV_CPUS and CHV_MEMORY_MB).
	//
	// Zero means "whatever the machine image asks for": the settings a
	// Granule's "VM SET" records in the image's own config are the
	// image's defaults, and a caller who named no size of their own is
	// not overriding them (docs/granulefile.md §5.5). ResolveSettings is
	// what turns a zero here into a number -- the image's, or granule's
	// own DefaultCPUs/DefaultMemoryMB when the image says nothing -- and
	// Validate insists it has been run, since only a resolved spec can be
	// saved as a truthful record of what the VM is running with.
	CPUs     int `json:"cpus"`
	MemoryMB int `json:"memoryMB"`

	// CPUsMax and MemoryMaxMB are the ceilings CPUs and MemoryMB can be
	// grown to later with "granule resize" (CHV_CPUS_MAX and
	// CHV_MEMORY_MAX_MB -- see the top-level README's "CPU hotplug" and
	// "Memory hotplug"). cloud-hypervisor sizes both hotplug windows once,
	// at boot, so a VM created without them can never be resized upward
	// at all: the vCPU half is refused outright ("Requested vCPUs exceed
	// maximum") and the memory half silently does nothing.
	//
	// Zero means "no headroom": omitted from the VM container's
	// environment entirely, leaving granule run's own defaults (CHV_CPUS
	// for the vCPU ceiling, and CHV_MEMORY_MB or 2048 -- whichever is
	// larger -- for memory), which is what every spec saved before these
	// existed gets. A machine that records ceilings of its own has them
	// taken over by ResolveSettings first, the same way CPUs and MemoryMB
	// are.
	CPUsMax     int `json:"cpusMax,omitempty"`
	MemoryMaxMB int `json:"memoryMaxMB,omitempty"`

	// MemoryLimitMB bounds the VM container or process's cgroup memory
	// (memory.max, with memory.swap.max bounded to 0 via --memory-swap
	// equal to --memory). Zero derives ContainerMemoryLimitMB() as the
	// guest memory ceiling (max(MemoryMB, MemoryMaxMB)) plus
	// VMMMemoryOverheadMB (512 MiB) whenever guest memory is set.
	MemoryLimitMB int `json:"memoryLimitMB,omitempty"`

	// OOMScoreAdj is the host oom_score_adj applied to the VM container or
	// process so the kernel OOM killer picks a sandbox VM before host
	// daemons running at negative scores. Zero applies DefaultOOMScoreAdj
	// (500).
	OOMScoreAdj int `json:"oomScoreAdj,omitempty"`

	// MemAgent turns on the guest-driven half of memory hotplug
	// (CHV_MEM_AGENT -- see the top-level README's "Memory hotplug"): the
	// VM container listens for the pressure signals the guest's own
	// granule-mem-agent sends, and grows the guest by MemAgentStepMB each
	// time one arrives, up to MemoryMaxMB. Without it a VM only ever
	// grows when an operator runs "granule resize".
	//
	// Only useful on a guest whose kernel reports pressure through
	// /proc/pressure/memory. Of the images this tree builds that is the
	// debian12 variant, whose Debian kernel is built CONFIG_PSI=y; the
	// bundled cloud-hypervisor CI kernel is not, and there the guest
	// agent finds no PSI, says so once and exits, leaving the listener
	// here with nothing to answer.
	//
	// It is the guest that has to reach the listener, over netshim's
	// control link (ControlCIDR), so a VM with the control link disabled
	// has no path for a signal to arrive on -- see Validate.
	MemAgent bool `json:"memAgent,omitempty"`

	// MemAgentAddr, MemAgentStepMB and MemAgentCooldown tune that
	// listener: the address it binds (CHV_MEM_AGENT_ADDR), how much a
	// single signal grows the guest by (CHV_MEM_AGENT_STEP_MB) and the
	// minimum time between two guest-triggered resizes
	// (CHV_MEM_AGENT_COOLDOWN). Each is passed through only when set;
	// empty or zero leaves the VM container's own defaults
	// (169.254.100.1:30090, 256 MiB, 30s), which is what the guest image
	// is built to signal and what a spec saved before these existed gets.
	//
	// Changing MemAgentAddr's port away from 30090 needs a matching
	// guest-side GRANULE_MEM_AGENT_PORT, since the guest is built to
	// signal that port; the address itself is fixed in the guest image
	// too (granule-control-net's gateway). Ignored entirely unless
	// MemAgent is set, so turning the agent off with "vm update" doesn't
	// require unsetting the tuning beside it.
	MemAgentAddr     string `json:"memAgentAddr,omitempty"`
	MemAgentToken    string `json:"memAgentToken,omitempty"`
	MemAgentStepMB   int    `json:"memAgentStepMB,omitempty"`
	MemAgentCooldown string `json:"memAgentCooldown,omitempty"`

	ShutdownTimeout string `json:"shutdownTimeout"`

	// GuestUser is an account inside the guest, besides root, that
	// "granule exec" may log in as -- GRANULE_EXEC_USER on the VM
	// container.
	//
	// It is one setting rather than two because the VM container reads it
	// twice, for the two halves of the same fact: "granule run" puts it on
	// the guest's kernel command line so the guest authorizes this boot's
	// generated key for that account (see pkg/guestkey), and "granule
	// exec" reads it to decide who to log in as. Naming the account in
	// only one of those places gives either a key authorized for someone
	// nobody logs in as, or a login as someone with no key.
	//
	// Empty means root, which is always authorized.
	GuestUser string `json:"guestUser,omitempty"`

	// ReadyFile is a path inside the guest that has to exist before this
	// VM counts as ready, on top of granule-agent answering --
	// GRANULE_READY_FILE on the VM container, read by "granule ready" (see
	// guestexec.Config.ReadyFile) and so by everything built on it: "vm
	// create -wait", "vm wait", "vm status" and the readinessProbe a pod
	// manifest sets (see deploy/k8s/).
	//
	// It is here, per VM, because the guest that creates the file and
	// the pod condition that reports on it are two ends of one setting:
	// a VM whose guest touches /run/granule/ready is only better off for
	// it if whatever waits on that VM knows to look. Nothing else about
	// it is granule's business -- how the guest decides its workload is
	// up is the guest image's own affair.
	//
	// Empty, the default, keeps readiness meaning what it has always
	// meant here, so a guest image that declares nothing is not one that
	// is never ready.
	ReadyFile string `json:"readyFile,omitempty"`

	// Bridge holds the host end of the control link (see ControlCIDR);
	// nothing on the guest's own data path goes through it. ExternalIface
	// is the sandbox interface whose address, MAC and MTU the guest takes
	// over. "auto" (the default) or empty leaves the VM container to find
	// it from the default route inside the sandbox's own namespace, which
	// the host cannot see; any other value is used as an interface name,
	// so a spec saved when the default was "eth0" behaves as it did.
	Bridge        string `json:"bridge"`
	ExternalIface string `json:"externalIface"`

	// ControlCIDR is the address netshim holds on the control link, the
	// private second NIC that keeps "granule exec" and the memory agent
	// able to reach a guest that now answers to the namespace's own
	// address. Set to the empty string to omit the control link
	// entirely.
	ControlCIDR string `json:"controlCIDR,omitempty"`

	// DNS is the nameserver(s) the guest resolves through, comma
	// separated, in either family: at most three, and at most two of them
	// IPv4 (netshim.ParseDNS). They reach the guest on its granule.dns=
	// boot parameter, and the IPv4 ones on ip='s own dns0/dns1 fields too,
	// where granule-configure-dns writes them into /etc/resolv.conf. This side
	// only carries the value: the parameter itself is assembled inside
	// the VM container, from the identity the container runtime assigned
	// (see pkg/netshim.GuestConfig), so this reaches it as
	// NETSHIM_DNS.
	//
	// netshim.DefaultDNS, "auto" (netshim.AutoDNS), is what "vm create"
	// fills in when the flag is left alone: the VM container reads its
	// own /etc/resolv.conf and hands the guest those nameservers, search
	// domains and ndots when the guest can reach them -- a Kubernetes
	// pod's cluster DNS, say -- and 8.8.8.8 when it cannot (or
	// 2001:4860:4860::8888, for a v6-only guest), as with
	// docker's embedded 127.0.0.11 or a link-local cloud resolver. It is
	// resolved there and not here because the host's resolver is not the
	// sandbox's. A deployment that wants a particular resolver names it
	// here; the empty string leaves the guest with whatever
	// /etc/resolv.conf its image ships. A spec saved before "auto"
	// existed holds the 8.8.8.8 it was created with, and keeps it.
	DNS string `json:"dns,omitempty"`

	// IPv6 is whether the guest takes over the sandbox's IPv6 identity as
	// well as its IPv4 one: netshim.IPv6Auto, the default, or
	// netshim.IPv6Off, which discards the IPv6 half and gives back the
	// IPv4-only behaviour netshim had before. Empty means the default, so
	// a spec saved before this existed behaves as auto. It reaches the
	// VM container as NETSHIM_IPV6.
	IPv6 string `json:"ipv6,omitempty"`

	// DockerRunOpts are extra options passed verbatim to the "docker
	// run" that creates the VM container. Port publishing and network
	// membership apply directly to the VM's own namespace, e.g.
	// []string{"-p", "8080:80", "--network", "mynet"}.
	DockerRunOpts []string `json:"dockerRunOpts,omitempty"`

	// PublishDeclaredPorts grants this VM's image what it declares with
	// EXPOSE: every declared port is published on the VM's container,
	// which is the namespace the guest takes over, so the declaration
	// becomes a path in rather than a number in a config. The analogue
	// of `docker run -P`, and off by default for the same reason docker
	// leaves it off -- an image declares, a deployment grants.
	//
	// A boolean rather than the ports themselves: the declaration
	// belongs to the image and is read afresh from it at each create
	// (see dockervm.Create), so a repacked image that declares a new
	// port gets it published on the next create without the saved spec
	// having gone stale, and a spec saved here never claims to know what
	// somebody else's image says. Explicit publication is the other flag
	// -- DockerRunOpts' own "-p 8080:80" -- and works whether or not
	// anything was declared, exactly as it does for docker.
	PublishDeclaredPorts bool `json:"publishDeclaredPorts,omitempty"`

	// PublishAddr is the host address PublishDeclaredPorts binds each
	// declared port to. Empty means loopback (127.0.0.1): the ports are
	// chosen by whoever wrote the image, not by the caller granting
	// them, so by default they are reachable from this host only, and
	// reaching them from the network takes a second, deliberate "yes"
	// -- "0.0.0.0" for every IPv4 interface, as `docker run -P` would
	// bind. It says nothing about DockerRunOpts' own "-p", which is a
	// raw docker flag and binds wherever docker binds it.
	PublishAddr string `json:"publishAddr,omitempty"`

	Env []string `json:"env,omitempty"`

	ImagesHostPath string `json:"imagesHostPath"`

	// DiskHostPath is the host directory a VM's own private writable
	// overlay is stored under (see WritableDiskDir), one
	// subdirectory per VM name. Only used when DiskReadOnly is false:
	// ImagesHostPath itself is always mounted read-only (see
	// ImagesMountPath), so a genuinely writable disk needs to live
	// somewhere else entirely.
	DiskHostPath string `json:"diskHostPath,omitempty"`

	// GranuleImage is the single OCI image used for both the netshim init
	// container and the VM container: the same granule binary, invoked
	// with different args ("netshim" vs "run") for each role.
	KonturImage string `json:"konturImage"`

	// TerminationGracePeriodSeconds is how long the VM container is given
	// to stop of its own accord before it is killed -- the "docker stop"
	// timeout "vm delete" and "vm update" use, and the value the pod
	// manifests under deploy/k8s/ set as terminationGracePeriodSeconds.
	// It must comfortably exceed ShutdownTimeout, since the guest's own
	// graceful shutdown happens inside it.
	TerminationGracePeriodSeconds int `json:"terminationGracePeriodSeconds"`
}

// Defaults returns a VMSpec with every field the CLI defaults if left
// unset, matching the defaults documented for granule in the top-level
// README wherever granule's own architecture doesn't force a stricter
// choice.
//
// DiskMode is config.DiskModeOverlay, which is what "granule run" itself
// defaults to and the only one of the three modes a stock guest actually
// finishes booting from: a read-only root leaves systemd unable to write
// the state it needs and the boot never completes, so defaulting to it
// gave a VM nobody could use. The overlay is a qcow2 overlay created inside
// the VM's own container (see config.PrepareOverlay), so this costs
// neither a host directory nor a write to the shared image -- the reason
// this used to default to read-only (that -images-hostpath is mounted
// read-only, so a writable disk was impossible) stopped applying when the
// overlay moved into the container.
//
// The sizing here is granule's own fallback, not an override of the
// machine's: it is what a VM gets when neither the caller nor the image
// asks for anything in particular. A command that means to let the image
// have its say leaves CPUs, MemoryMB, DiskMode and ShutdownTimeout unset
// and calls ResolveSettings instead, which ends at these same values.
func Defaults() VMSpec {
	return VMSpec{
		DiskMode:                      config.DiskModeOverlay,
		CPUs:                          DefaultCPUs,
		MemoryMB:                      DefaultMemoryMB,
		OOMScoreAdj:                   DefaultOOMScoreAdj,
		ShutdownTimeout:               DefaultShutdownTimeout,
		ControlCIDR:                   "169.254.100.1/24",
		DNS:                           netshim.DefaultDNS,
		IPv6:                          netshim.IPv6Auto,
		Bridge:                        "granule0",
		ExternalIface:                 netshim.AutoExternalIface,
		ImagesHostPath:                "/var/lib/vm-images",
		DiskHostPath:                  "/var/lib/granule/vm-disks",
		KonturImage:                   DockerImage,
		TerminationGracePeriodSeconds: 40,
	}
}

// DefaultCPUs and DefaultMemoryMB are the sizing granule falls back to
// when neither the caller nor the machine image asked for anything: the
// defaults Defaults() carries, and the ones "granule vm create -h" shows
// against -cpus and -memory-mb.
//
// Named rather than left as literals because ResolveSettings has to be able
// to hand them out on its own, after it has given the image its say.
const (
	DefaultCPUs     = 2
	DefaultMemoryMB = 2048

	// VMMMemoryOverheadMB is the host/container cgroup memory headroom (in MiB)
	// added on top of the guest's memory ceiling (max(MemoryMB, MemoryMaxMB))
	// for cloud-hypervisor / VMM overhead when MemoryLimitMB is not explicitly
	// set (matching the 512 MiB overhead in deploy/k8s/gke-pod-example.yaml).
	VMMMemoryOverheadMB = 512

	// DefaultOOMScoreAdj is the default host/container oom_score_adj for a VM
	// so the kernel OOM killer picks a sandbox VM before host daemons (-900).
	DefaultOOMScoreAdj = 500
)

// EffectiveOOMScoreAdj returns the oom_score_adj to apply to the VM container
// or process, defaulting to DefaultOOMScoreAdj (500) when unset (0).
func (s VMSpec) EffectiveOOMScoreAdj() int {
	if s.OOMScoreAdj != 0 {
		return s.OOMScoreAdj
	}
	return DefaultOOMScoreAdj
}

// ContainerMemoryLimitMB returns the cgroup memory limit in MiB for the VM
// container or process. When MemoryLimitMB is explicitly set (> 0), it is
// returned directly; otherwise, when guest memory is positive, it returns the
// guest memory ceiling (max(MemoryMB, MemoryMaxMB)) plus VMMMemoryOverheadMB.
func (s VMSpec) ContainerMemoryLimitMB() int {
	if s.MemoryLimitMB > 0 {
		return s.MemoryLimitMB
	}
	guestMB := s.MemoryMB
	if s.MemoryMaxMB > guestMB {
		guestMB = s.MemoryMaxMB
	}
	if guestMB > 0 {
		return guestMB + VMMMemoryOverheadMB
	}
	return 0
}

// DefaultShutdownTimeout is how long a VM that outlives the command which
// created it is given to shut its guest down gracefully, when neither the
// caller nor the machine image names a budget.
//
// Unlike the sizing above, ResolveSettings does not hand this out itself:
// pkg/guestboot's throwaway boots fall back to a longer one of their own,
// and a resolution that filled this in would leave them unable to tell a
// machine recording 20s from a machine recording nothing.
const DefaultShutdownTimeout = "20s"

// ImageSettings reads the settings a machine image records for itself out
// of the ENV of its OCI config -- the §5.5 keys of docs/granulefile.md, in
// the spelling a Granule's "VM SET" writes them, "so that nothing has to
// read a Granule to boot the result".
//
// The spec it returns names only what the image names; every other field
// is left zero. That is what lets a caller decide the order between the
// two of them, which §5.5 settles in the image's disfavour: the settings
// are "the image's defaults, not fixed limits", overridden by the runtime
// environment "the way a docker run -e overrides an ENV". ResolveSettings
// is that order applied.
//
// Every value is read the way the VM container reads the same variable
// (pkg/config), and one granule cannot make sense of is left out rather
// than guessed at: this may be somebody else's image, and the container
// reads the variable itself and says so properly. The two keys in §5.5's
// table with no VMSpec field of their own -- memory-hotplug
// (CHV_MEMORY_HOTPLUG) and snapshot (CHV_SNAPSHOT_PATH) -- need nothing
// here, since granule never passes either to the container and the
// image's own ENV therefore stands (see dockervm.Create).
func ImageSettings(env []string) VMSpec {
	var image VMSpec
	// The vcpus spelling of cpus (§5.5 gives both), applied at the end so
	// that an image recording both is read the way the VM container reads
	// it: CHV_CPUS wins, whichever order they appear in.
	var cpus, vcpus int
	for _, entry := range env {
		key, rawValue, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		value := strings.TrimSpace(rawValue)
		if value == "" {
			// The granule image itself carries an empty CHV_DISK_MODE (the
			// Dockerfile's GUEST_DISK_MODE build argument, unset unless a
			// build bakes a mode in), and an ENV with no value is not a
			// setting.
			continue
		}
		switch key {
		case "CHV_CMDLINE":
			// What the image asked for, not what it asked for plus
			// granule's own: the VM container composes the two itself
			// (config.CmdlineWith), and doing it here as well would hand
			// it a line it would then compose onto a second time.
			image.Cmdline = rawValue
			continue
		case "CHV_DISK_MODE":
			switch value {
			case config.DiskModeOverlay, config.DiskModePersistent, config.DiskModeReadOnly:
				image.DiskMode = value
			}
			continue
		case "CHV_DISK_IO_ENGINE":
			switch value {
			case "io_uring", "epoll", "sync":
				image.DiskIOEngine = value
			}
			continue
		case "CHV_DISK_IOTHREADS":
			if on, err := onOffBool(value); err == nil {
				image.DiskIOThreads = on
			}
			continue
		case "CHV_SHUTDOWN_TIMEOUT":
			// Parsed rather than passed through, because an unparseable
			// one would fail Validate and take the whole boot with it -- a
			// machine that records nonsense here is one to report on, not
			// one to refuse to start.
			if d, err := time.ParseDuration(value); err == nil && d > 0 {
				image.ShutdownTimeout = value
			}
			continue
		}
		n, err := strconv.Atoi(value)
		if err != nil || n <= 0 {
			continue
		}
		switch key {
		case "CHV_CPUS":
			cpus = n
		case "CHV_VCPUS":
			vcpus = n
		case "CHV_CPUS_MAX":
			image.CPUsMax = n
		case "CHV_MEMORY_MB":
			image.MemoryMB = n
		case "CHV_MEMORY_MAX_MB":
			image.MemoryMaxMB = n
		case "CHV_DISK_SIZE_MB":
			image.DiskSizeMB = n
		case "CHV_EXTENT_SIZE_MB":
			image.ExtentSizeMB = n
		case "CHV_DISK_NUM_QUEUES":
			image.DiskNumQueues = n
		}
	}
	switch {
	case cpus > 0:
		image.CPUs = cpus
	case vcpus > 0:
		image.CPUs = vcpus
	}
	return image
}

// onOffBool reads a switch the way the VM container does
// (config.getEnvBool): a Granule writes its switches "on" and "off"
// (docs/granulefile.md §5.5) and VM SET records the value verbatim, so
// both those and Go's own spellings have to be understood here for the
// spec to agree with the machine.
func onOffBool(value string) (bool, error) {
	switch strings.ToLower(value) {
	case "on", "yes":
		return true, nil
	case "off", "no":
		return false, nil
	}
	return strconv.ParseBool(value)
}

// ResolveSettings settles what the VM actually boots with, from the two
// sources that get a say: the caller, whose choices are whatever is
// already set on s, and the machine image, whose own are read out of
// imageEnv by ImageSettings above.
//
// Anything the caller set stands. Anything they left unset -- zero, or
// empty for the strings -- takes the image's. The boot sizing neither of
// them named ends at granule's own DefaultCPUs and DefaultMemoryMB, since
// that is the one setting that cannot be left open: the VM container's own
// default is not granule's (256 MiB, see pkg/config's defaultMemoryMB), so
// a spec that reached "docker run" with no memory size would quietly boot
// a guest an eighth of the size the operator was promised, and Validate
// refuses one instead. Everything else is free to stay unset, which means
// the VM container's own default -- no hotplug headroom (see CPUsMax), a
// DiskMode left for DiskModeOrDerived to settle, and a ShutdownTimeout for
// the caller to fall back on (they differ: vmspec's DefaultShutdownTimeout
// against guestboot's own, longer one).
//
// This has to run before Validate, for the reason above and so that the
// checks comparing a ceiling to a boot size have both numbers in front of
// them.
func (s *VMSpec) ResolveSettings(imageEnv []string) {
	image := ImageSettings(imageEnv)
	// Which ceilings are the image's own rather than the caller's, so that
	// one it recorded around its own boot size can be dropped if the caller
	// asks to boot past it (see the end of this function).
	cpusMaxFromImage := s.CPUsMax == 0 && image.CPUsMax > 0
	memoryMaxFromImage := s.MemoryMaxMB == 0 && image.MemoryMaxMB > 0

	if s.CPUs == 0 {
		s.CPUs = image.CPUs
	}
	if s.CPUsMax == 0 {
		s.CPUsMax = image.CPUsMax
	}
	if s.MemoryMB == 0 {
		s.MemoryMB = image.MemoryMB
	}
	if s.MemoryMaxMB == 0 {
		s.MemoryMaxMB = image.MemoryMaxMB
	}
	if s.Cmdline == "" {
		s.Cmdline = image.Cmdline
	}
	if s.ShutdownTimeout == "" {
		s.ShutdownTimeout = image.ShutdownTimeout
	}
	if s.DiskSizeMB == 0 {
		s.DiskSizeMB = image.DiskSizeMB
	}
	if s.ExtentSizeMB == 0 {
		s.ExtentSizeMB = image.ExtentSizeMB
	}
	if s.DiskNumQueues == 0 {
		s.DiskNumQueues = image.DiskNumQueues
	}
	if s.DiskIOEngine == "" {
		s.DiskIOEngine = image.DiskIOEngine
	}
	if !s.DiskIOThreads {
		s.DiskIOThreads = image.DiskIOThreads
	}
	// The disk mode is left alone when anything else has already spoken for
	// the disk, the deprecated -disk-readonly included: that one names no
	// mode of its own, so it arrives as an empty DiskMode with DiskReadOnly
	// set, and honouring the image instead would hand a writable guest back
	// to a caller who asked for a read-only one (see DiskModeOrDerived).
	if s.DiskMode == "" && !s.DiskReadOnly {
		s.DiskMode = image.DiskMode
	}

	// Granule's own defaults, for a boot size neither side named -- capped
	// at a ceiling the machine did name. A machine recording only
	// "memory-max-mb=1024" is not asking to boot above its own ceiling,
	// and granule's 2048 would be both more than it wanted and a spec
	// Validate refuses.
	if s.CPUs == 0 {
		s.CPUs = DefaultCPUs
		if cpusMaxFromImage && s.CPUsMax < s.CPUs {
			s.CPUs = s.CPUsMax
		}
	}
	if s.MemoryMB == 0 {
		s.MemoryMB = DefaultMemoryMB
		if memoryMaxFromImage && s.MemoryMaxMB < s.MemoryMB {
			s.MemoryMB = s.MemoryMaxMB
		}
	}
	// The other way round, a caller who asked to boot bigger than a
	// ceiling the machine recorded has contradicted it: "VM SET cpus=2
	// cpus-max=4" and "-cpus 8" cannot both hold. The caller wins -- the
	// settings are the image's defaults, not its limits -- and the ceiling
	// they walked past is dropped rather than turned into a Validate error
	// about a number nobody typed. What is left is the hotplug window the
	// VM container picks for itself, which is exactly what this VM got
	// before the image had any say at all. A ceiling the caller passed
	// themselves is never dropped: "-cpus 8 -cpus-max 4" is their own
	// contradiction, and one they should hear about.
	if cpusMaxFromImage && s.CPUsMax < s.CPUs {
		s.CPUsMax = 0
	}
	if memoryMaxFromImage && s.MemoryMaxMB < s.MemoryMB {
		s.MemoryMaxMB = 0
	}
}

// DockerImage is the default GranuleImage: the tag the top-level README's
// own "docker build -t granule:latest ." makes, which is an image the
// daemon already has and never tries to pull. A cluster deployment names
// its own registry reference instead, in the pod manifest it applies (see
// deploy/k8s/gke.md) -- nothing granule defaults.
const DockerImage = "granule:latest"

// ContainerName is the name a VM's own container is given, and the pod
// name the manifests under deploy/k8s/ use for the same VM, so a VM is
// called the same thing whichever way it was started.
func ContainerName(name string) string { return "granule-vm-" + name }

// nameRE is what a VM's name has to match, checked by ValidateName. A name
// becomes three different things: a tap device name ("tap-<name>" and
// "ctl-<name>", see netshim), a docker container name
// ("granule-vm-<name>", see ContainerName), and a file name --
// "<name>.json" under the state directory (specPath).
//
// A DNS-1123 label is what a pod name has to be anyway, and it is a
// subset of what docker container names and Linux interface names
// accept, so requiring one here satisfies all three at once -- including
// the pod manifests under deploy/k8s/, which name a VM the same way. It
// is also what keeps a name out of the file name it lands in: docker
// refuses a container named "granule-vm-../evil", so nothing is created
// today either -- but that is docker's check and an accident of the name
// prefix, and what it says ("Unable to find image") never mentions the
// name. This does.
var nameRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// ValidateName checks a VM name on its own, without a spec around it.
//
// Validate calls it, so "vm create" and "vm update" go through it as part
// of checking the spec they built -- but the subcommands that only take a
// name ("vm delete", "vm status", "vm exec", "vm shell", "vm wait") never
// build a spec at all: they take the name straight from argv to
// specPath's "<stateDir>/<name>.json". Nothing there cleans the name or
// knows what it should look like, so "granule vm delete ../evil"
// resolved to a path above the state directory. No VM can be created
// under such a
// name (that is what Validate rejects), and the name comes from the
// operator's own argv, so this is not a way in past anything -- but a
// name that cannot name a VM should be refused as one, in the same words
// wherever it is given, rather than surface as a stray filesystem error
// that never mentions the name at all.
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("name is required")
	}
	if !nameRE.MatchString(name) {
		return fmt.Errorf("invalid name %q: a VM name must be a DNS-1123 label -- lower-case letters, digits and dashes, starting and ending with a letter or digit (%s)", name, nameRE)
	}
	return nil
}

// sortByName sorts specs in place by Name.
func sortByName(specs []VMSpec) {
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
}

// Validate checks that spec is complete and internally consistent, filling
// in a default Cmdline unless Firmware is set -- Cmdline only applies to
// direct kernel boot, which is still what happens even with Kernel left
// empty (granule run's own CHV_KERNEL default then applies, see
// pkg/config's defaultKernel).
//
// A Cmdline that is set is passed on as it stands, whether it is a whole
// command line or the one parameter a caller cares about: the VM
// container composes it onto the line it derives for itself
// (config.CmdlineWith), so this does not have to be complete to be
// bootable.
func (s *VMSpec) Validate() error {
	if err := ValidateName(s.Name); err != nil {
		return err
	}
	// netshim names the VM's tap device "tap-<name>" and Linux interface
	// names are capped at 15 characters (IFNAMSIZ-1); catch an
	// over-length name here, at "vm create"/"vm update" time, rather than
	// as a netshim init container crash loop once the pod's already been
	// submitted.
	// The image is the one value docker run takes positionally, after
	// every flag granule passes, so one beginning with "-" would be read
	// as another flag -- "--privileged", say -- and the next argument as
	// the image. No image reference starts with one.
	if strings.HasPrefix(s.KonturImage, "-") {
		return fmt.Errorf("invalid kontur image %q: an image reference cannot start with '-'", s.KonturImage)
	}
	// An address rather than a hostname: it ends up spliced into a
	// "-p ADDR::PORT" docker parses, and a value docker refused would
	// fail the create only after the old container had been removed.
	if s.PublishAddr != "" && net.ParseIP(s.PublishAddr) == nil {
		return fmt.Errorf("invalid publish address %q: want an IP address, such as 127.0.0.1 or 0.0.0.0", s.PublishAddr)
	}
	if tapName := "tap-" + s.Name; len(tapName) > 15 {
		return fmt.Errorf("name %q too long: tap device name %q would exceed 15 characters", s.Name, tapName)
	}
	// Parsed here even though nothing on this side renders it: the value
	// is assembled into an ip= parameter inside the VM container, and an
	// address rejected there would fail a VM that "vm create" had
	// already reported as started.
	if err := netshim.ValidateDNS(s.DNS); err != nil {
		return fmt.Errorf("invalid dns %q: %w", s.DNS, err)
	}
	if err := netshim.ValidateIPv6(s.IPv6); err != nil {
		return fmt.Errorf("invalid ipv6 %q: %w", s.IPv6, err)
	}
	// An empty DiskImage means the guest disk baked into the granule
	// image itself (pkg/config's defaultDiskImage), rather than one
	// supplied from outside under ImagesMountPath. That is what a VM
	// booted from a *customized* granule image is: its guest travels
	// inside the image, so there is no host path to name, nothing to
	// mount, and no shared backing file to overlay -- the container's own
	// writable layer is the per-VM copy. Every check below is about a
	// supplied disk and so only applies when there is one.
	//
	// A writable disk used to have to live under ImagesMountPath, because
	// the overlay backing it was created out here and needed a host
	// path to point at. The overlay is made inside the VM's container now
	// (see config.PrepareOverlay), so any path the container can read
	// works -- including the guest baked into the granule image itself,
	// which has no host path at all.
	s.DiskMode = s.DiskModeOrDerived()
	switch s.DiskMode {
	case config.DiskModeOverlay, config.DiskModePersistent, config.DiskModeReadOnly:
	default:
		return fmt.Errorf("disk mode must be %q, %q or %q, got %q",
			config.DiskModeOverlay, config.DiskModePersistent, config.DiskModeReadOnly, s.DiskMode)
	}
	// Caught here rather than left to the VM container, which rejects the
	// same combination on its own (see config.FromEnv): "vm create" can
	// say so before a pod is submitted or a container started, the same
	// way every other check in here does.
	if s.DiskSizeMB < 0 {
		return fmt.Errorf("disk-size-mb must not be negative, got %d", s.DiskSizeMB)
	}
	if s.DiskSizeMB > 0 && s.DiskMode != config.DiskModeOverlay {
		return fmt.Errorf("disk-size-mb needs -disk-mode=%s: only the VM's own overlay is resized, never the shared disk image (got %q)",
			config.DiskModeOverlay, s.DiskMode)
	}
	if s.ExtentSizeMB < 0 {
		return fmt.Errorf("extent-size-mb must not be negative, got %d", s.ExtentSizeMB)
	}
	if s.ExtentSizeMB > 0 && s.DiskMode != config.DiskModeOverlay {
		return fmt.Errorf("extent-size-mb needs -disk-mode=%s: extents are only used with overlay disks (got %q)",
			config.DiskModeOverlay, s.DiskMode)
	}
	// Checked here rather than left to the probe: the path is looked for
	// inside the guest, from a session that starts in the account's home
	// directory, so a relative one would mean a different file per
	// account and the VM would simply never report ready.
	if s.ReadyFile != "" && !strings.HasPrefix(s.ReadyFile, "/") {
		return fmt.Errorf("ready-file %q must be an absolute path inside the guest", s.ReadyFile)
	}
	// The control link's tap is named from the VM's name too, and a VM
	// whose control tap cannot be named is a VM with no exec path in.
	if ctlTap := "ctl-" + s.Name; len(ctlTap) > 15 {
		return fmt.Errorf("name %q too long: control tap device name %q would exceed 15 characters", s.Name, ctlTap)
	}
	if s.ControlCIDR != "" {
		if _, _, err := net.ParseCIDR(s.ControlCIDR); err != nil {
			return fmt.Errorf("invalid control CIDR %q: %w", s.ControlCIDR, err)
		}
	}
	// Zero is not treated as "leave it to the image" here, even though
	// that is what it means on the way in: by this point ResolveSettings has
	// had the image's say and granule's own defaults to fall back on, so a
	// spec still carrying no sizing is one that never asked either. Saying
	// so is better than passing the gap to the VM container, whose own
	// defaults are smaller than granule's (see ResolveSettings).
	if s.CPUs < 1 {
		return fmt.Errorf("cpus must be at least 1, got %d (0 means the machine image's own vCPU count, which is resolved before a VM is started)", s.CPUs)
	}
	if s.MemoryMB < 128 {
		return fmt.Errorf("memory-mb must be at least 128, got %d (0 means the machine image's own memory size, which is resolved before a VM is started)", s.MemoryMB)
	}
	// Caught here rather than left to the VM container, where it would
	// surface as a container that crash-loops on granule run's own
	// validation after the VM has already been submitted.
	if s.DiskNumQueues < 0 {
		return fmt.Errorf("disk-num-queues must not be negative, got %d", s.DiskNumQueues)
	}
	if s.DiskIOEngine != "" {
		switch strings.ToLower(s.DiskIOEngine) {
		case "io_uring", "epoll", "sync":
		default:
			return fmt.Errorf("disk-io-engine must be \"io_uring\", \"epoll\" or \"sync\", got %q", s.DiskIOEngine)
		}
	}
	if s.CPUsMax != 0 && s.CPUsMax < s.CPUs {
		return fmt.Errorf("cpus-max (%d) must be at least cpus (%d)", s.CPUsMax, s.CPUs)
	}
	if s.MemoryMaxMB != 0 && s.MemoryMaxMB < s.MemoryMB {
		return fmt.Errorf("memory-max-mb (%d) must be at least memory-mb (%d)", s.MemoryMaxMB, s.MemoryMB)
	}
	if s.MemoryLimitMB < 0 {
		return fmt.Errorf("memory-limit-mb must not be negative, got %d", s.MemoryLimitMB)
	}
	if s.MemoryLimitMB > 0 && s.MemoryMB > 0 && s.MemoryLimitMB < s.MemoryMB {
		return fmt.Errorf("memory-limit-mb (%d) must be at least memory-mb (%d)", s.MemoryLimitMB, s.MemoryMB)
	}
	if s.OOMScoreAdj < -1000 || s.OOMScoreAdj > 1000 {
		return fmt.Errorf("oom-score-adj must be between -1000 and 1000, got %d", s.OOMScoreAdj)
	}
	for _, e := range s.Env {
		eq := strings.IndexByte(e, '=')
		if eq == -1 {
			return fmt.Errorf("invalid env %q: must be in KEY=value format", e)
		}
		key := e[:eq]
		if !strings.HasPrefix(key, "CHV_") && !strings.HasPrefix(key, "NETSHIM_") && !strings.HasPrefix(key, "GRANULE_") {
			return fmt.Errorf("invalid env %q: prefix must be CHV_, NETSHIM_ or GRANULE_", e)
		}
		switch key {
		case "CHV_DISK_MODE", "CHV_DISK_SIZE_MB", "CHV_EXTENT_SIZE_MB", "CHV_DISK_EXTENT_SIZE_MB", "CHV_DISK_NUM_QUEUES", "CHV_DISK_IOTHREADS", "CHV_DISK_IO_ENGINE", "CHV_DISK_IMAGE", "CHV_KERNEL", "CHV_INITRAMFS", "CHV_FIRMWARE", "CHV_CMDLINE", "CHV_CPUS", "CHV_VCPUS", "CHV_MEMORY_MB", "CHV_SHUTDOWN_TIMEOUT", "CHV_CPUS_MAX", "CHV_MEMORY_MAX_MB", "CHV_MEM_AGENT", "CHV_MEM_AGENT_ADDR", "CHV_MEM_AGENT_STEP_MB", "CHV_MEM_AGENT_COOLDOWN", "GRANULE_EXEC_USER", "GRANULE_READY_FILE", "NETSHIM_VM", "NETSHIM_BRIDGE", "NETSHIM_CONTROL_CIDR", "NETSHIM_EXTERNAL_IFACE", "NETSHIM_DNS", "NETSHIM_IPV6":
			return fmt.Errorf("env %q is controlled by its own flag; use the flag instead of -e", key)
		}
	}

	if err := s.validateMemAgent(); err != nil {
		return err
	}
	if s.Kernel != "" && s.Firmware != "" {
		return fmt.Errorf("kernel and firmware are mutually exclusive")
	}
	if _, err := time.ParseDuration(s.ShutdownTimeout); err != nil {
		return fmt.Errorf("invalid shutdown timeout %q: %w", s.ShutdownTimeout, err)
	}
	s.CmdlineAuto = false
	if s.Firmware == "" && s.Cmdline == "" {
		root := "rw"
		if s.DiskMode == config.DiskModeReadOnly {
			root = "ro"
		}
		// No ip= parameter: the address is only knowable once the
		// sandbox exists, so the VM container appends it at boot from
		// what it reads off the namespace itself (see cmd/granule-shim's
		// applyNetshimNet).
		// console= comes from the architecture rather than being written
		// here: an aarch64 guest has a PL011, not an 8250, and is
		// unreachable if it is told otherwise (config.GuestConsole).
		s.Cmdline = fmt.Sprintf("console=%s root=/dev/vda %s noresume quiet loglevel=3", config.GuestConsole(), root)
		s.CmdlineAuto = true
	}
	return nil
}

// validateMemAgent checks the guest-driven growth settings (see
// VMSpec.MemAgent), which is worth doing here rather than leaving to the
// VM container for the two reasons every other check in Validate exists
// for: a malformed value would surface as a container that crash-loops on
// config.FromEnv after "vm create" has already reported the VM started,
// and a well-formed but useless combination would surface as nothing at
// all -- a listener that comes up, is signalled, and can never grow the
// guest it was turned on for.
//
// The syntax checks run whether or not the agent is on, since a value
// that cannot be parsed is a mistake either way. The two checks about
// whether the agent could actually work only apply when it is: turning it
// off with "granule vm update -mem-agent=false" leaves the tuning from
// the previous create in the saved spec, and that must not fail.
func (s VMSpec) validateMemAgent() error {
	if s.MemAgentStepMB < 0 {
		return fmt.Errorf("mem-agent-step-mb must not be negative, got %d", s.MemAgentStepMB)
	}
	if s.MemAgentAddr != "" {
		// Only the port is required: an empty host is legitimate, since
		// ":30090" binds every address in the VM container's namespace.
		// A host that is given has to be one the guest can dial, which
		// is netshim's own control link end in every configuration this
		// tree builds.
		_, port, err := net.SplitHostPort(s.MemAgentAddr)
		if err != nil {
			return fmt.Errorf("invalid mem-agent-addr %q: %w (want host:port, e.g. %s)", s.MemAgentAddr, err, defaultMemAgentAddr)
		}
		if port == "" {
			return fmt.Errorf("invalid mem-agent-addr %q: no port (want host:port, e.g. %s)", s.MemAgentAddr, defaultMemAgentAddr)
		}
	}
	// The VM container refuses this too (config.FromEnv): the token is
	// one whitespace-separated field of every signal line, so a token
	// with whitespace in it could never be matched.
	if strings.ContainsFunc(s.MemAgentToken, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return fmt.Errorf("mem-agent-token must not contain whitespace: it is one whitespace-separated field of the memory agent's signal line")
	}
	if s.MemAgentCooldown != "" {
		d, err := time.ParseDuration(s.MemAgentCooldown)
		if err != nil {
			return fmt.Errorf("invalid mem-agent-cooldown %q: %w", s.MemAgentCooldown, err)
		}
		if d < 0 {
			return fmt.Errorf("mem-agent-cooldown must not be negative, got %q", s.MemAgentCooldown)
		}
	}
	if !s.MemAgent {
		return nil
	}
	// cloud-hypervisor sizes the virtio-mem window once, at boot, from
	// the gap between the boot size and the ceiling (see CPUsMax and
	// MemoryMaxMB). With no gap there is nothing for a pressure signal to
	// grow into, and the VM container's own default ceiling -- the larger
	// of 2048 and the boot size -- is no gap at all at granule's
	// default -memory-mb of 2048. So the ceiling is required here rather
	// than defaulted into a VM whose agent can only ever log refusals.
	if s.MemoryMaxMB <= s.MemoryMB {
		return fmt.Errorf("mem-agent needs memory-max-mb above memory-mb (%d): the guest can only be grown inside the hotplug window, which is sized once at boot", s.MemoryMB)
	}
	// The signals travel over netshim's control link: the guest holds the
	// far end of it and dials this listener at the near end. With the
	// control link off there is no route from the guest into the VM
	// container's namespace at all -- the address the guest took over is
	// the container's own, so dialing it from in there comes back to the
	// guest. A caller who names an address anyway is taken at their word;
	// what cannot be allowed is the default address silently pointing at
	// a link this VM doesn't have.
	if s.ControlCIDR == "" && s.MemAgentAddr == "" {
		return fmt.Errorf("mem-agent needs the control link: -control-cidr is empty, so the guest has no route to the listener at %s (leave -control-cidr at its default, or name a reachable -mem-agent-addr)", defaultMemAgentAddr)
	}
	return nil
}

// defaultMemAgentAddr is the address the VM container's listener binds
// when CHV_MEM_AGENT_ADDR is unset (config.defaultMemAgentAddr), and the
// one the guest image is built to signal -- netshim's control link host
// end. Only named here to say so in an error message; nothing in this
// package sets it, since an unset MemAgentAddr is passed through as an
// unset variable so the container keeps deciding its own default.
const defaultMemAgentAddr = "169.254.100.1:30090"

// DiskModeOrDerived returns s.DiskMode, deriving it from DiskReadOnly
// when unset -- a spec saved before DiskMode existed, or a caller still
// passing only -disk-readonly.
//
// -disk-readonly=false has always meant "give this VM a private writable
// disk", implemented as a per-VM overlay; it maps to DiskModeOverlay, so
// that intent survives the move of the overlay into the container. (Note
// this is not how the same-named CHV_DISK_READONLY maps inside the
// container, where false meant writing through to the image -- see
// config.diskModeFromEnv.)
func (s VMSpec) DiskModeOrDerived() string {
	if s.DiskMode != "" {
		return s.DiskMode
	}
	if s.DiskReadOnly {
		return config.DiskModeReadOnly
	}
	return config.DiskModeOverlay
}

// DefaultPublishAddr is where -publish-declared binds a declared port
// when the spec names no PublishAddr: this host only.
const DefaultPublishAddr = "127.0.0.1"

// PublishAddrOrDefault is the host address declared ports are bound to:
// PublishAddr, or DefaultPublishAddr when it is unset -- which includes
// every spec saved before PublishAddr existed, so re-creating one of
// those narrows what it publishes to loopback until it is asked for
// with -publish-addr.
func (s VMSpec) PublishAddrOrDefault() string {
	if s.PublishAddr != "" {
		return s.PublishAddr
	}
	return DefaultPublishAddr
}

// specPath returns where a VM's spec JSON lives within stateDir.
//
// filepath.Join rather than concatenation, so a name is at least cleaned
// on its way into a path instead of being pasted into one verbatim.
// That is tidiness, not the guard: Join would resolve "../evil" to a path
// outside stateDir just as happily. ValidateName is what keeps a name
// from being able to say that in the first place, and every caller that
// takes a name from argv runs it.
func specPath(stateDir, name string) string {
	return filepath.Join(stateDir, name+".json")
}

// Load reads a previously saved VMSpec back from the state directory.
func Load(stateDir, name string) (VMSpec, error) {
	data, err := os.ReadFile(specPath(stateDir, name))
	if err != nil {
		return VMSpec{}, err
	}
	var s VMSpec
	if err := json.Unmarshal(data, &s); err != nil {
		return VMSpec{}, fmt.Errorf("parsing saved spec for %q: %w", name, err)
	}
	return s, nil
}

// Save persists spec to the state directory, creating it if needed.
//
// The file is readable by its owner only. A spec carries the VM's Env --
// which is where a deployment's credentials go, the same as with
// "docker run -e" -- and its MemAgentToken, and the root state directory
// is a node's shared /var/lib/granule/vms, so a world-readable spec
// handed both to every local account. It is written to a private
// temporary file and renamed into place rather than written in place,
// because os.WriteFile's mode only applies to a file it creates: a spec
// saved 0644 by an older granule would otherwise keep that mode across
// every later "vm update".
func Save(stateDir string, s VMSpec) error {
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	// CreateTemp makes the file 0600.
	tmp, err := os.CreateTemp(stateDir, "."+s.Name+".json.tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), specPath(stateDir, s.Name)); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

// Delete removes a VM's saved spec from the state directory. It is not an
// error if no spec is saved for name.
func Delete(stateDir, name string) error {
	err := os.Remove(specPath(stateDir, name))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// List returns the names of every VM with a saved spec in stateDir, sorted
// alphabetically. It returns an empty slice (not an error) if stateDir
// doesn't exist yet.
func List(stateDir string) ([]VMSpec, error) {
	entries, err := os.ReadDir(stateDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var specs []VMSpec
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if len(name) < 6 || name[len(name)-5:] != ".json" {
			continue
		}
		s, err := Load(stateDir, name[:len(name)-5])
		if err != nil {
			return nil, fmt.Errorf("loading %s: %w", name, err)
		}
		specs = append(specs, s)
	}
	sortByName(specs)
	return specs, nil
}
