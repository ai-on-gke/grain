package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/bwsalmon/granule/pkg/dockercli"
	"github.com/bwsalmon/granule/pkg/dockervm"
	"github.com/bwsalmon/granule/pkg/vmspec"
)

// stringList collects a flag that may be repeated, in order. Used for
// -docker-run-opt, where each option and each of its values is passed
// separately so no quoting or splitting convention has to be invented for
// values that may themselves contain spaces or commas.
//
// The first Set clears whatever the list was seeded with, so that passing
// the flag at all replaces the saved value wholesale rather than
// appending to it. That keeps "granule vm update" behaving the same way
// for this flag as for every other one -- give it and it replaces, omit
// it and the saved value is kept -- instead of silently accumulating a
// longer list on each update.
type stringList struct {
	values   []string
	replaced bool
}

func (l *stringList) String() string {
	if l == nil {
		return ""
	}
	return strings.Join(l.values, " ")
}

func (l *stringList) Set(v string) error {
	if !l.replaced {
		l.values = nil
		l.replaced = true
	}
	l.values = append(l.values, v)
	return nil
}

func runVM(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("vm: expected a subcommand (create, run, exec, shell, wait, status, update, delete, list)")
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "create":
		return runVMCreate(ctx, rest, stdout, stderr)
	case "run":
		return runVMRun(ctx, rest, stdin, stdout, stderr)
	case "exec":
		return runVMExec(ctx, rest, stdin, stdout, stderr)
	case "shell":
		return runVMShell(ctx, rest, stdin, stdout, stderr)
	case "wait":
		return runVMWait(ctx, rest, stdout, stderr)
	case "status":
		return runVMStatus(ctx, rest, stdout, stderr)
	case "update":
		return runVMUpdate(ctx, rest, stdout, stderr)
	case "delete":
		return runVMDelete(ctx, rest, stdout, stderr)
	case "list":
		return runVMList(ctx, rest, stdout, stderr)
	case "forward":
		return runLegacyForward(ctx, rest, stderr)
	default:
		return fmt.Errorf("vm: unknown subcommand %q", cmd)
	}
}

// vmFlags binds every VMSpec field (other than Name) to a flag, and the
// resulting values can be read back out via toSpec once Parse has run.
type vmFlags struct {
	// fs is the set these flags were registered on, kept so toSpec can
	// ask which of them the caller actually passed (see
	// resolvedDiskMode).
	fs *flag.FlagSet
	// newVM is set by registerCreateVMFlags: these flags belong to a
	// command that creates a VM, so a sizing flag the caller didn't pass
	// means "whatever the machine image asks for" rather than the default
	// value it was registered with. See imageOwned.
	newVM            bool
	diskImage        *string
	diskReadOnly     *bool
	diskMode         *string
	diskSizeMB       *int
	extentSizeMB     *int
	diskNumQueues    *int
	diskIOThreads    *bool
	diskIOEngine     *string
	storageBackend   *string
	kernel           *string
	initramfs        *string
	firmware         *string
	cmdline          *string
	cpus             *int
	memoryMB         *int
	cpusMax          *int
	memoryMaxMB      *int
	memoryLimitMB    *int
	oomScoreAdj      *int
	memAgent         *bool
	memAgentAddr     *string
	memAgentToken    *string
	memAgentStepMB   *int
	memAgentCooldown *string
	shutdownTimeout  *string
	guestUser        *string
	readyFile        *string
	// ip, port, guestPort, bridgeCIDR, netMode and staticPodPath are
	// settings for things that no longer exist -- the NAT mode and the
	// static pod backend -- kept only to be accepted and ignored (see
	// registerVMFlags and checkDeprecated).
	ip                            *string
	port                          *int
	guestPort                     *int
	bridgeCIDR                    *string
	netMode                       *string
	bridge                        *string
	externalIface                 *string
	ipv6                          *string
	dns                           *string
	controlCIDR                   *string
	dockerRunOpts                 stringList
	publishDeclaredPorts          *bool
	publishAddr                   *string
	env                           stringList
	imagesHostPath                *string
	diskHostPath                  *string
	konturImage                   *string
	terminationGracePeriodSeconds *int
	staticPodPath                 *string
}

// registerVMFlags registers one flag per VMSpec field on fs, defaulting
// each to the matching field of d. Passing vmspec.Defaults() gives the
// usual create-time defaults; passing a previously saved spec makes every
// flag default to "keep the current value", which is what makes "granule
// vm update" a partial update instead of requiring every flag to be
// repeated.
func registerVMFlags(fs *flag.FlagSet, d vmspec.VMSpec) *vmFlags {
	v := &vmFlags{fs: fs}
	v.diskImage = fs.String("disk", d.DiskImage, "path to the VM's disk image under -images-hostpath, as seen inside the granule container (e.g. /images/disk.img)")
	v.diskMode = fs.String("disk-mode", d.DiskMode, `how the VM attaches its disk: "overlay" (the guest writes into a qcow2 overlay of its own, created in its container, leaving the image untouched and shared), "persistent" (the guest writes through to the image itself) or "readonly". Left out, the machine image's own ("VM SET disk-mode=") is used, and this default applies only to an image that records none`)
	v.diskReadOnly = fs.Bool("disk-readonly", d.DiskReadOnly, "deprecated, use -disk-mode: true means -disk-mode=readonly, false means -disk-mode=overlay (which is what it always did: a private writable disk per VM)")
	v.diskSizeMB = fs.Int("disk-size-mb", d.DiskSizeMB, "size of the VM's own writable overlay, in MiB; 0 means the disk image's own size, and a larger value grows an overlay an earlier boot already created (-disk-mode=overlay only; the bundled guest grows its filesystem onto the space at boot, a guest of your own has to do that itself)")
	v.extentSizeMB = fs.Int("extent-size-mb", d.ExtentSizeMB, "extent size of the VM's writable overlay in MiB; 0 means the default 64 MiB (-disk-mode=overlay only)")
	v.diskNumQueues = fs.Int("disk-num-queues", d.DiskNumQueues, "number of virtio-blk queues (multi-queue); 0 means hypervisor default")
	v.diskIOThreads = fs.Bool("disk-iothreads", d.DiskIOThreads, "dedicated host iothreads for block device eventfd servicing")
	v.diskIOEngine = fs.String("disk-io-engine", d.DiskIOEngine, "host asynchronous I/O backend: \"io_uring\", \"epoll\" or \"sync\"")
	v.storageBackend = fs.String("storage-backend", d.StorageBackend, "disk storage backend: \"standard\" (qcow2 overlay / flat disk.img) or \"extents\" (N-MiB extent directory over vhost-user-blk)")
	v.kernel = fs.String("kernel", d.Kernel, "path to a kernel for direct boot, as seen inside the container (mutually exclusive with -firmware)")
	v.initramfs = fs.String("initramfs", d.Initramfs, "path to an initramfs, used with -kernel")
	v.firmware = fs.String("firmware", d.Firmware, "path to firmware for firmware boot, as seen inside the container (mutually exclusive with -kernel)")
	v.cmdline = fs.String("cmdline", d.Cmdline, "kernel command line parameters, used with -kernel. These are added to the line granule derives from -disk-mode, replacing the parameters they name and keeping the ones they do not, so \"-cmdline quiet\" still gets a root= to boot from; the guest's own ip= is appended at boot from the address the container runtime assigned")
	v.cpus = fs.Int("cpus", d.CPUs, "boot vCPU count; left out, the machine image's own (\"VM SET cpus=\") is used, and this default applies only to an image that records none")
	fs.IntVar(v.cpus, "vcpus", d.CPUs, "boot vCPU count (alias for -cpus)")
	v.memoryMB = fs.Int("memory-mb", d.MemoryMB, "guest memory, in MiB; left out, the machine image's own (\"VM SET memory-mb=\") is used, and this default applies only to an image that records none")
	v.cpusMax = fs.Int("cpus-max", d.CPUsMax, "vCPU ceiling \"granule resize\" may grow this VM to; left out, the machine image's own (\"VM SET cpus-max=\"), and 0 if it records none -- no headroom, and the count is fixed at -cpus for the VM's whole life")
	v.memoryMaxMB = fs.Int("memory-max-mb", d.MemoryMaxMB, "memory ceiling \"granule resize\" may grow this VM to, in MiB; left out, the machine image's own (\"VM SET memory-max-mb=\"), and 0 if it records none -- no headroom beyond the VM container's own default")
	v.memoryLimitMB = fs.Int("memory-limit-mb", d.MemoryLimitMB, "cgroup memory limit for the VM container or process, in MiB, with swap bounded to 0; 0 uses guest memory plus 512 MiB VMM overhead")
	v.oomScoreAdj = fs.Int("oom-score-adj", d.OOMScoreAdj, "host oom_score_adj for the VM container or process; 0 uses granule's default (500)")
	v.memAgent = fs.Bool("mem-agent", d.MemAgent, "let the guest grow its own memory: the VM container listens for the pressure signals the guest's granule-mem-agent sends and hotplugs -mem-agent-step-mb on each, up to -memory-max-mb (which is required with this, since the hotplug window is sized once at boot). Needs a guest whose kernel reports pressure through /proc/pressure/memory -- of the images this tree builds, the debian12 variant -- and the control link the signals travel over (-control-cidr)")
	v.memAgentAddr = fs.String("mem-agent-addr", d.MemAgentAddr, "address in the VM container the pressure signals are received on; empty means the container's own default (169.254.100.1:30090), which is the control link address the guest image is built to signal (-mem-agent only)")
	v.memAgentToken = fs.String("mem-agent-token", d.MemAgentToken, "secret token required by the memory agent listener to authenticate pressure signals (-mem-agent only)")
	v.memAgentStepMB = fs.Int("mem-agent-step-mb", d.MemAgentStepMB, "how much a single pressure signal grows the guest by, in MiB, capped at -memory-max-mb; 0 means the VM container's own default of 256 (-mem-agent only)")
	v.memAgentCooldown = fs.String("mem-agent-cooldown", d.MemAgentCooldown, "minimum time between two guest-triggered resizes (Go duration syntax); empty means the VM container's own default of 30s (-mem-agent only)")
	v.shutdownTimeout = fs.String("shutdown-timeout", d.ShutdownTimeout, "how long to wait for a graceful guest shutdown before forcing it (Go duration syntax); left out, the machine image's own (\"VM SET shutdown-timeout=\") is used, and this default applies only to an image that records none")
	v.guestUser = fs.String("guest-user", d.GuestUser, "guest account, besides root, that \"granule exec\" logs in as: the guest authorizes this boot's generated key for it too (empty means root)")
	v.readyFile = fs.String("ready-file", d.ReadyFile, "absolute path inside the guest that must exist before this VM counts as ready, on top of its agent answering: the guest's own declaration that its workload is up (e.g. /run/granule/ready, created by a setup script or a systemd ExecStartPost=). Empty leaves readiness meaning \"the guest answers\"")
	v.bridge = fs.String("bridge", d.Bridge, "name of the in-pod bridge holding the control link's host end")
	v.externalIface = fs.String("external-iface", d.ExternalIface, "the pod's primary interface, whose address, MAC and MTU the guest takes over; \"auto\" (or empty) finds it inside the VM container as the interface carrying the default route (IPv4 or IPv6), falling back to the only non-loopback interface with an IPv4 or global IPv6 address")
	v.ipv6 = fs.String("ipv6", d.IPv6, "whether the guest takes over the pod's IPv6 identity as well as its IPv4 one: \"auto\" reads it off the pod's interface, \"off\" discards it and keeps the guest IPv4-only")
	v.dns = fs.String("dns", d.DNS, "nameserver(s) the guest resolves through, comma separated, IPv4 or IPv6, at most three and at most two IPv4: they travel on the guest's boot parameters and become its /etc/resolv.conf; \"auto\" uses the VM container's own resolver (and its search domains) when the guest can reach it and 8.8.8.8 (2001:4860:4860::8888 for a v6-only guest) when it cannot; empty leaves whatever the guest image ships with")
	v.controlCIDR = fs.String("control-cidr", d.ControlCIDR, "address netshim holds on the control link, the private second NIC that keeps \"granule exec\" and the memory agent able to reach the guest; empty disables it")
	// The NAT mode these configured is gone: a VM now takes over the
	// address the container runtime assigned its sandbox, so there is no
	// private subnet to place it on and no external port to forward (see
	// the top-level README's "Container networking"). Accepted and
	// ignored rather than removed, the same way -disk-hostpath is: a
	// deployment's own scripts pass them, and failing their "vm create"
	// outright over flags that now have nothing to configure would break
	// them for no gain.
	v.ip = fs.String("ip", "", "deprecated and ignored: the container runtime assigns the address the guest takes over")
	v.port = fs.Int("port", 0, "deprecated and ignored: ports are published on the sandbox itself (-docker-run-opt -p ..., or -publish-declared for what the image asks for), not forwarded per VM")
	v.guestPort = fs.Int("guest-port", 0, "deprecated and ignored: nothing forwards to a fixed in-guest port any more")
	v.bridgeCIDR = fs.String("bridge-cidr", "", "deprecated and ignored: use -control-cidr, the only subnet netshim still creates")
	// -net is the one that cannot simply be ignored: "flat" is what
	// every VM is now, but silently accepting "nat" would hand back a
	// guest on a different network than the caller asked for.
	v.netMode = fs.String("net", "", "deprecated: only the former \"flat\" behaviour exists now, and it is the default")
	v.dockerRunOpts.values = append([]string(nil), d.DockerRunOpts...)
	v.env.values = append([]string(nil), d.Env...)
	if fs.Lookup("e") == nil {
		fs.Var(&v.env, "e", "set general container environment variables (e.g. -e CHV_FOO=bar), restricted to CHV_, NETSHIM_, GRANULE_ prefixes; repeatable")
	}
	fs.Var(&v.dockerRunOpts, "docker-run-opt", "extra option passed verbatim to the \"docker run\" creating the VM container, repeatable (e.g. -docker-run-opt -p -docker-run-opt 8080:80); -backend docker only")
	// The granting half of EXPOSE. The declaration is the image's and
	// this is the deployment's: without it a guest stays shielded no
	// matter what its image declares, which is what keeps a casually
	// written EXPOSE from being a hole. See the README's "Publishing a
	// guest's ports".
	v.publishDeclaredPorts = fs.Bool("publish-declared", d.PublishDeclaredPorts, "publish the ports this VM's image declares with EXPOSE on its container, the way \"docker run -P\" does: each declared port is reachable in the guest on a host port docker picks, and this create -- and \"granule vm status <name>\" afterwards -- says which. Unlike -P they are bound to -publish-addr, which is loopback unless you say otherwise, since it is the image's author who chose them. Without it a declaration opens nothing. -docker-run-opt -p HOST:GUEST publishes a port of your choosing instead, declared or not, bound wherever docker binds it (every interface, unless you write -p ADDR:HOST:GUEST)")
	// Loopback by default because the ports are the image author's
	// choice, not the caller's: -publish-declared says "yes to what the
	// image asks for", and this is the separate yes to the network
	// seeing it. See dockervm's publishArgs.
	v.publishAddr = fs.String("publish-addr", d.PublishAddr, "host address -publish-declared binds the declared ports to; empty means 127.0.0.1, reachable from this host only. 0.0.0.0 reaches them from every network this host is on, the way \"docker run -P\" binds them")
	v.imagesHostPath = fs.String("images-hostpath", d.ImagesHostPath, "host directory mounted read-only at /images in the granule container")
	// Accepted and ignored rather than removed: a deployment's own
	// scripts pass it, and failing their "vm create" outright over a flag
	// that now has nothing to configure would break them for no gain.
	v.diskHostPath = fs.String("disk-hostpath", d.DiskHostPath, "deprecated and ignored: each VM's writable overlay is created inside its own container now, so there is no host directory to place it in")
	v.konturImage = fs.String("kontur-image", d.KonturImage, "granule image reference, used for the single VM container with inline networking; the default is the locally built \"granule:latest\", which the daemon already has and never tries to pull")
	v.terminationGracePeriodSeconds = fs.Int("termination-grace-period", d.TerminationGracePeriodSeconds, "how long the VM container is given to stop before it is killed; must comfortably exceed -shutdown-timeout")
	// Accepted and ignored rather than removed, the same way
	// -disk-hostpath is: granule no longer writes manifests for a
	// standalone kubelet at all (see checkBackend), and failing a "vm
	// create" outright over a flag that now has nothing to configure
	// would break a deployment's own scripts for no gain.
	v.staticPodPath = fs.String("static-pod-path", "", "deprecated and ignored: granule runs VMs on docker, and a cluster runs them from a pod manifest of its own (deploy/k8s/gke.md), so there is no manifest directory to write to")
	return v
}

// registerCreateVMFlags registers the same flags for a command that
// creates a VM from scratch -- "vm create" and "vm run" -- where there is
// no previous spec to keep and the machine image gets a say.
//
// The defaults it shows are granule's own (vmspec.Defaults), which is
// where a VM ends up when nothing else asks for anything; a sizing flag
// the caller doesn't pass is recorded as unset instead, so the settings
// the image records for itself are what actually apply (see imageOwned and
// vmspec.VMSpec.ResolveSettings).
func registerCreateVMFlags(fs *flag.FlagSet) *vmFlags {
	v := registerVMFlags(fs, vmspec.Defaults())
	v.newVM = true
	return v
}

// wasSet reports whether the caller actually passed one of these flags, as
// opposed to leaving it at whatever default registerVMFlags gave it.
//
// More than one name is for a setting with an alias: -cpus and -vcpus are
// the same flag registered twice, and either of them being passed is the
// caller naming a vCPU count.
func (v *vmFlags) wasSet(names ...string) bool {
	set := false
	v.fs.Visit(func(f *flag.Flag) {
		for _, name := range names {
			if f.Name == name {
				set = true
			}
		}
	})
	return set
}

// imageOwned returns what to record for one of the settings a machine
// image can carry of its own: value as the caller gave it, or zero --
// "the image's own", which vmspec.VMSpec.ResolveSettings fills in from the
// image and falls back to granule's default for -- when this is a create
// and the caller passed none of names.
//
// The point is that granule's flag defaults must not arrive at the VM
// container as an override. They travel as an explicit -e, which beats the
// image's ENV the way "docker run -e" does (docs/granulefile.md §5.5), so
// passing 2048 on for a caller who never mentioned memory boots a machine
// built with "VM SET memory-mb=1024" at twice the size it asked for, and
// leaves its recorded settings taking effect only under a plain "docker
// run".
//
// An update has nothing to resolve: its flags were seeded from the spec
// the VM is already running with, which had the image's say resolved into
// it when it was created, so an omitted flag means "keep that" and is
// passed straight through. A spec saved before any of this, with cpus: 2
// in it, cannot be told from a VM that asked for two -- so it is taken at
// its word rather than quietly resized by an unrelated "vm update".
func (v *vmFlags) imageOwned(value int, names ...string) int {
	if v.newVM && !v.wasSet(names...) {
		return 0
	}
	return value
}

// imageOwnedString is imageOwned for a setting whose unset value is the
// empty string rather than zero -- the shutdown budget, the only one of
// those whose flag defaults to something granule chose.
func (v *vmFlags) imageOwnedString(value string, names ...string) string {
	if v.newVM && !v.wasSet(names...) {
		return ""
	}
	return value
}

// diskMode returns the disk mode to save, reconciling -disk-mode with the
// deprecated -disk-readonly.
//
// -disk-mode now defaults to a real mode ("overlay") rather than to
// "derive it from -disk-readonly", and an explicit mode always wins over
// a derived one (vmspec.VMSpec.DiskModeOrDerived), so a caller passing
// only -disk-readonly would otherwise have it silently overridden by that
// default. Returning "" when -disk-readonly is the flag that was passed
// hands the decision back to the derivation, which is what the deprecated
// flag has always meant.
//
// A create with neither flag passed returns "" for the other reason an
// empty mode exists now: the machine image may record one of its own ("VM
// SET disk-mode="), and granule's default must not override it any more
// than its default sizing does (see imageOwned). Nothing is lost if the
// image records none either -- an empty mode still ends at the same
// "overlay", via DiskModeOrDerived.
func (v *vmFlags) resolvedDiskMode() string {
	if v.wasSet("disk-mode") {
		return *v.diskMode
	}
	if v.wasSet("disk-readonly") || v.newVM {
		return ""
	}
	return *v.diskMode
}

func (v *vmFlags) toSpec(name string) vmspec.VMSpec {
	return vmspec.VMSpec{
		Name:                          name,
		DiskImage:                     *v.diskImage,
		DiskReadOnly:                  *v.diskReadOnly,
		DiskMode:                      v.resolvedDiskMode(),
		DiskSizeMB:                    *v.diskSizeMB,
		ExtentSizeMB:                  *v.extentSizeMB,
		DiskNumQueues:                 *v.diskNumQueues,
		DiskIOThreads:                 *v.diskIOThreads,
		DiskIOEngine:                  *v.diskIOEngine,
		StorageBackend:                *v.storageBackend,
		Kernel:                        *v.kernel,
		Initramfs:                     *v.initramfs,
		Firmware:                      *v.firmware,
		Cmdline:                       *v.cmdline,
		CPUs:                          v.imageOwned(*v.cpus, "cpus", "vcpus"),
		MemoryMB:                      v.imageOwned(*v.memoryMB, "memory-mb"),
		CPUsMax:                       v.imageOwned(*v.cpusMax, "cpus-max"),
		MemoryMaxMB:                   v.imageOwned(*v.memoryMaxMB, "memory-max-mb"),
		MemoryLimitMB:                 *v.memoryLimitMB,
		OOMScoreAdj:                   *v.oomScoreAdj,
		MemAgent:                      *v.memAgent,
		MemAgentAddr:                  *v.memAgentAddr,
		MemAgentToken:                 *v.memAgentToken,
		MemAgentStepMB:                *v.memAgentStepMB,
		MemAgentCooldown:              *v.memAgentCooldown,
		ShutdownTimeout:               v.imageOwnedString(*v.shutdownTimeout, "shutdown-timeout"),
		GuestUser:                     *v.guestUser,
		ReadyFile:                     *v.readyFile,
		Bridge:                        *v.bridge,
		ExternalIface:                 *v.externalIface,
		IPv6:                          *v.ipv6,
		DNS:                           *v.dns,
		ControlCIDR:                   *v.controlCIDR,
		DockerRunOpts:                 v.dockerRunOpts.values,
		PublishDeclaredPorts:          *v.publishDeclaredPorts,
		PublishAddr:                   *v.publishAddr,
		Env:                           v.env.values,
		ImagesHostPath:                *v.imagesHostPath,
		DiskHostPath:                  *v.diskHostPath,
		KonturImage:                   *v.konturImage,
		TerminationGracePeriodSeconds: *v.terminationGracePeriodSeconds,
	}
}

// checkDeprecated rejects the one deprecated flag that cannot just be
// ignored. -ip/-port/-guest-port/-bridge-cidr described a network that no
// longer exists, and -static-pod-path a manifest directory nothing writes
// to any more; ignoring them leaves the caller with a working VM. -net
// nat asks for a mode that is gone, and ignoring that would hand back a
// guest reachable somewhere else entirely, so say so instead.
func (v *vmFlags) checkDeprecated() error {
	switch *v.netMode {
	case "", "flat":
		return nil
	default:
		return fmt.Errorf("-net %q is no longer supported: a VM always takes over the address the container runtime assigned its sandbox (what -net flat used to select); drop the flag", *v.netMode)
	}
}

// backendDocker is the only value -backend still takes: granule drives
// a local docker daemon and nothing else.
const backendDocker = "docker"

// checkBackend rejects a -backend other than docker -- in practice
// "static-pod", the backend granule used to default to, which wrote a
// manifest for a standalone kubelet to run.
//
// Unlike the other retired flags this one cannot be accepted and ignored:
// a caller asking for static-pod is asking for a VM this command will not
// start, and quietly starting a docker container instead would leave them
// with a VM somewhere they are not looking for it. The flag itself stays,
// so a script that spells out the backend it already gets keeps working.
func checkBackend(backend string) error {
	if backend == backendDocker {
		return nil
	}
	return fmt.Errorf(`-backend %q is no longer supported: granule runs VMs on the local docker daemon (-backend %s, the default).
To run this VM on Kubernetes instead, apply a pod manifest of your own -- see deploy/k8s/gke.md and the examples beside it`, backend, backendDocker)
}

// helpName stands in for the VM name when the caller asked for the
// command's flags instead of naming a VM ("granule vm create -h").
// The help request stays in the arguments, so the command goes on to
// build its own flag set and let the flag package print it -- which is
// the answer that was asked for, and one that names the flags this
// particular subcommand takes rather than a generic synopsis.
const helpName = "<name>"

// splitNameAndFlags takes the VM name off the front of a subcommand's
// arguments, where every "granule vm <verb> <name> [flags]" puts it.
//
// A first argument that looks like a flag is not a name. It used to be
// taken as one anyway: "granule vm create -h" tried to create a VM
// literally called "-h" and failed on whatever the spec was missing,
// while "granule vm create -state-dir /tmp/vms" created a VM called
// "-state-dir" in the default state directory and reported success. The
// help forms get the flags they were after (see helpName); anything else
// is refused here, where the argument that is actually wrong can still
// be named.
//
// The name itself is checked here too, for the same reason and in the
// same place: "vm create" and "vm update" build a spec and so reach
// vmspec.Validate eventually, but "vm delete", "vm status", "vm exec",
// "vm shell" and "vm wait" build no spec at all -- they take the name
// straight to the state directory, and for delete to a container name
// as well. Checking it on the way past covers all of them at
// once, in one message, before any path is built out of it (see
// vmspec.ValidateName).
func splitNameAndFlags(args []string, cmd string) (name string, rest []string, err error) {
	if len(args) == 0 {
		return "", nil, fmt.Errorf("%s: a VM name is required (granule %s <name> [flags])", cmd, cmd)
	}
	if first := args[0]; strings.HasPrefix(first, "-") {
		if isHelpFlag(first) {
			return helpName, args, nil
		}
		return "", nil, fmt.Errorf("%s: a VM name comes before the flags, and the first argument is %q (granule %s <name> [flags]; \"granule %s -h\" lists them)", cmd, first, cmd, cmd)
	}
	if err := vmspec.ValidateName(args[0]); err != nil {
		return "", nil, fmt.Errorf("%s: %w", cmd, err)
	}
	return args[0], args[1:], nil
}

// isHelpFlag reports whether arg is one of the spellings the flag
// package itself answers with a usage dump.
func isHelpFlag(arg string) bool {
	switch arg {
	case "-h", "--h", "-help", "--help":
		return true
	}
	return false
}

// peekFlag scans args by hand for a named flag, in any of flag's accepted
// forms ("-name value" or "-name=value"), without registering or
// consuming any other flag. Returns fallback if the flag isn't there.
//
// One flag has to be read before the real parse can happen: -state-dir,
// because which saved spec seeds "vm update"'s defaults depends on it,
// and it is itself one of the flags being parsed. flag.FlagSet has no way
// to parse a subset of its flags and ignore the rest, hence doing it by
// hand.
func peekFlag(args []string, name, fallback string) string {
	for i, a := range args {
		switch {
		case a == "-"+name || a == "--"+name:
			if i+1 < len(args) {
				return args[i+1]
			}
		case strings.HasPrefix(a, "-"+name+"="):
			return strings.TrimPrefix(a, "-"+name+"=")
		case strings.HasPrefix(a, "--"+name+"="):
			return strings.TrimPrefix(a, "--"+name+"=")
		}
	}
	return fallback
}

// peekStateDir is peekFlag for -state-dir, defaulting to the same
// directory the flag itself does.
func peekStateDir(args []string) string {
	return peekFlag(args, "state-dir", defaultStateDir())
}

func runVMCreate(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	name, rest, err := splitNameAndFlags(args, "vm create")
	if err != nil {
		return err
	}

	fs := flag.NewFlagSet("granule vm create "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	stateDir := fs.String("state-dir", defaultStateDir(), "directory granule stores VM state in")
	backend := fs.String("backend", backendDocker, `deprecated: "docker" (run against the local docker daemon) is the only way granule runs a VM; a cluster runs one from a pod manifest of its own instead (deploy/k8s/gke.md)`)
	wait := fs.Bool("wait", false, "don't return until the VM's guest answers a command, the way \"granule vm wait\" does; without it the VM is still booting when this returns")
	readyTimeout := fs.Duration("ready-timeout", defaultReadyTimeout, "how long -wait gives the guest to become reachable")
	v := registerCreateVMFlags(fs)
	if err := fs.Parse(rest); err != nil {
		return err
	}

	if _, err := vmspec.Load(*stateDir, name); err == nil {
		return fmt.Errorf("VM %q already exists (use \"granule vm update\" to change it)", name)
	}

	if err := v.checkDeprecated(); err != nil {
		return err
	}
	// Refused here rather than after the VM is up: a backend granule
	// cannot run is a flag the caller has to take back out, and saying so
	// before anything is created leaves nothing to clean up.
	if err := checkBackend(*backend); err != nil {
		return err
	}

	spec := v.toSpec(name)
	resolveImageSettings(ctx, &spec, stdout)
	if err := submitVM(ctx, spec, *stateDir, stdout); err != nil {
		return err
	}
	if !*wait {
		return nil
	}
	// The VM is left in place if the guest never answers: it was asked
	// for as a VM that outlives this command, and "docker logs
	// granule-vm-<name>" is how to find out why it didn't come up.
	// ("granule vm run", whose VM exists only for one command, deletes
	// its own instead -- see -keep-on-failure there.)
	return waitForGuest(ctx, name, *readyTimeout, stdout)
}

func runVMUpdate(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	name, rest, err := splitNameAndFlags(args, "vm update")
	if err != nil {
		return err
	}

	// registerVMFlags needs the existing spec to seed its defaults (so
	// omitted flags keep their current value), but which spec to load
	// depends on -state-dir, which is itself one of those flags. Find it
	// by hand before doing the real parse below, since flag.FlagSet has
	// no way to parse a subset of its flags and ignore the rest.
	// A help request names no VM, so there is no saved spec to seed the
	// flag defaults from and nothing to look up: the plain defaults are
	// what the listing below shows.
	existing := vmspec.Defaults()
	if name != helpName {
		dir := peekStateDir(rest)
		if existing, err = vmspec.Load(dir, name); err != nil {
			return fmt.Errorf("VM %q not found in %s (use \"granule vm create\" first)%s: %w", name, dir, stateDirNoteSuffix(dir), err)
		}
	}
	if existing.CmdlineAuto {
		existing.Cmdline = ""
	}

	fs := flag.NewFlagSet("granule vm update "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	stateDir := fs.String("state-dir", defaultStateDir(), "directory granule stores VM state in")
	v := registerVMFlags(fs, existing)
	if err := fs.Parse(rest); err != nil {
		return err
	}

	if err := v.checkDeprecated(); err != nil {
		return err
	}
	// Checked here as well as in submitVM: an update tears the existing
	// VM's containers down first, and doing that only to fail on saving
	// the new spec would leave the caller with neither the old VM nor the
	// new one.
	if err := ensureStateDir(*stateDir); err != nil {
		return err
	}

	spec := v.toSpec(name)

	// The VM's containers are torn down and recreated: there is no
	// updating a running cloud-hypervisor's disk, memory or command line
	// in place, so an update is a delete and a create of the same name.
	if err := dockervm.Delete(ctx, &dockervm.Docker{}, name, existing.TerminationGracePeriodSeconds, stdout); err != nil {
		return fmt.Errorf("removing old containers before update: %w", err)
	}
	return submitVM(ctx, spec, *stateDir, stdout)
}

// resolveImageSettings gives the machine image its say over every setting
// a "vm create" or "vm run" was not given: the vCPU count, the memory size,
// the hotplug ceilings, the disk settings, the kernel command line and the
// shutdown budget a Granule's "VM SET" recorded in the image's own OCI
// config (docs/granulefile.md §5.5). Anything the caller did name is left
// exactly as they gave it, and anything neither of them named ends at
// granule's own default -- see vmspec.VMSpec.ResolveSettings, which decides
// all of that and is the same reader "granule guest verify" boots through
// (pkg/guestboot).
//
// It runs before the spec is validated and saved, so what "granule vm
// show" reports and what a later "vm update" starts from is what this VM
// is really running with, rather than a set of numbers the VM container
// worked out for itself and nothing out here can see.
//
// Best effort: an image whose config cannot be read is still started, with
// a line saying the settings are granule's rather than its own. The usual
// reason is an image the daemon has not pulled yet -- "docker run" pulls
// it a moment later -- and an image that cannot be inspected at all will
// have plenty more to say once it is run.
func resolveImageSettings(ctx context.Context, spec *vmspec.VMSpec, report io.Writer) {
	env, err := (&dockercli.CLI{}).ConfigEnv(ctx, spec.KonturImage)
	if err != nil {
		fmt.Fprintf(report, "could not read %s's own settings (%v); starting it with granule's own defaults\n", spec.KonturImage, err)
	}
	spec.ResolveSettings(env)
	if spec.ShutdownTimeout == "" {
		// The one setting ResolveSettings deliberately leaves open, because
		// the two callers fall back differently: a VM that outlives the
		// command gets vmspec's budget, a throwaway boot gets guestboot's
		// longer one.
		spec.ShutdownTimeout = vmspec.DefaultShutdownTimeout
	}
}

// submitVM validates spec, starts its containers via dockervm, and saves
// spec so a later update/delete can find it again.
func submitVM(ctx context.Context, spec vmspec.VMSpec, stateDir string, stdout io.Writer) error {
	if err := spec.Validate(); err != nil {
		return err
	}
	// Before anything is started: a VM whose state cannot be saved is a
	// VM nothing can find again to update or delete (see
	// ensureStateDir).
	if err := ensureStateDir(stateDir); err != nil {
		return err
	}
	if err := dockervm.Create(ctx, &dockervm.Docker{}, spec, stdout); err != nil {
		return err
	}

	if err := vmspec.Save(stateDir, spec); err != nil {
		return fmt.Errorf("saving VM state: %w", err)
	}
	return nil
}

func runVMDelete(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	name, rest, err := splitNameAndFlags(args, "vm delete")
	if err != nil {
		return err
	}

	fs := flag.NewFlagSet("granule vm delete "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	stateDir := fs.String("state-dir", defaultStateDir(), "directory granule stores VM state in")
	// Accepted and ignored, like "vm create"'s own -static-pod-path:
	// there is no manifest directory to remove anything from any more.
	fs.String("static-pod-path", "", "deprecated and ignored: granule no longer writes static pod manifests, so a delete only removes this VM's containers and its saved state")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	return deleteVM(ctx, name, *stateDir, stdout)
}

// containerPhrase is a container's state as a clause about it, for the
// middle of a sentence: a container that is there and stopped is still a
// VM that exists, and "is exited" is not how to say so.
func containerPhrase(state dockervm.ContainerState) string {
	if state == dockervm.ContainerRunning {
		return "is running"
	}
	return "is there, stopped"
}

// deleteVM removes a VM's containers and its saved state. Split out of
// runVMDelete so "vm run" can tear down the VM it created without going
// back through flag parsing.
//
// out is where the progress lines go. It is granule's stdout for
// "granule vm delete", and its *stderr* for the teardown at the end of
// "granule vm run", whose stdout belongs to the guest command -- so
// everything printed here goes to the one writer the caller handed in
// rather than to os.Stdout or a stream of its own.
func deleteVM(ctx context.Context, name, stateDir string, out io.Writer) error {
	gracePeriod := vmspec.Defaults().TerminationGracePeriodSeconds
	// elsewhere records "this VM's spec is not here, but its container
	// is": the state that makes a plain "deleted VM" a lie (see below).
	elsewhere := false
	existing, err := vmspec.Load(stateDir, name)
	switch {
	case err == nil:
		gracePeriod = existing.TerminationGracePeriodSeconds
	case os.IsNotExist(err):
		// A VM whose saved state is already gone -- a second "vm delete"
		// of the same one, or one whose state directory was wiped -- is
		// still worth the container teardown below, under the default
		// grace period. dockervm.Delete treats containers that aren't
		// there as success, so this stays a no-op when there is truly
		// nothing left.
		//
		// The other way to get here is a VM that exists and was created
		// by a differently privileged caller, whose spec is in *that*
		// caller's default state directory (see defaultStateDir): "sudo
		// granule vm delete web" on a VM created without sudo finds
		// nothing to load and, left to itself, says "deleted VM" about a
		// VM whose state is still sitting in the other directory. Every
		// other subcommand answers that with stateDirNote; a delete has
		// to say it too, and can go one better by asking docker whether
		// the containers are there -- which turns "your VM may be
		// somewhere else" into "it is". A docker that cannot be asked is
		// no reason to fail here: the teardown below asks it again and
		// reports it properly.
		if note := stateDirNote(stateDir); note != "" {
			if state, err := dockervm.State(ctx, &dockervm.Docker{}, name); err == nil && state != dockervm.ContainerMissing {
				elsewhere = true
				fmt.Fprintf(out, "no saved state for %q in %s, but its container %s %s: the VM exists, and its spec was saved somewhere else\n", name, stateDir, vmspec.ContainerName(name), containerPhrase(state))
			} else {
				fmt.Fprintf(out, "no saved state for %q in %s\n", name, stateDir)
			}
			fmt.Fprintf(out, "(%s)\n", note)
		}
	default:
		return err
	}

	// Nothing to clean up out here any more: a VM's writable overlay
	// lives inside its own container (see config.PrepareOverlay), so
	// removing the container removes it, and there is no host directory
	// left over to delete.

	if err := dockervm.Delete(ctx, &dockervm.Docker{}, name, gracePeriod, out); err != nil {
		return err
	}

	if err := vmspec.Delete(stateDir, name); err != nil {
		return fmt.Errorf("removing saved state for %q: %w", name, err)
	}
	if elsewhere {
		// The containers are gone (docker is shared between the two
		// callers even when their state directories aren't), but the
		// spec that survived is what the other caller's "vm list" reads
		// -- so it would still show a VM with nothing behind it. Saying
		// "deleted VM" here would be the sharp edge over again, one
		// directory along.
		fmt.Fprintf(out, "removed the containers for %q, but not its saved state: re-run this delete with the -state-dir above to clear that too\n", name)
		return nil
	}
	fmt.Fprintf(out, "deleted VM %q\n", name)
	return nil
}

// diskSizeLabel is a VM's disk size for a column: a VM that asked for no
// particular size takes the image's own, which isn't a number granule
// knows out here (it's a file inside the VM's container), so say so rather
// than print "0".
func diskSizeLabel(s vmspec.VMSpec) string {
	if s.DiskSizeMB > 0 {
		return strconv.Itoa(s.DiskSizeMB)
	}
	return "image"
}

func runVMList(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("granule vm list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	stateDir := fs.String("state-dir", defaultStateDir(), "directory granule stores VM state in")
	// Opt-in, because it changes what this command costs: without it a
	// listing is a read of -state-dir and nothing else, and with it every
	// VM listed is a "docker inspect" plus, for one that is running, a
	// probe of its guest (see "granule vm status", which is this same
	// check for one VM in full).
	withStatus := fs.Bool("status", false, "check each VM's container and guest too, and print STATUS and PORTS columns (costs a docker call per VM)")
	timeout := fs.Duration("timeout", defaultStatusTimeout, "how long each -status check may take before that VM's guest is reported unreachable")
	if err := fs.Parse(args); err != nil {
		return err
	}

	specs, err := vmspec.List(*stateDir)
	if err != nil {
		return err
	}
	if len(specs) == 0 {
		// Naming the directory matters now that it depends on who is
		// asking (see defaultStateDir): "no VMs" on its own reads as
		// "there are none", when what it may mean is "none of yours".
		fmt.Fprintf(stdout, "no VMs in %s\n", *stateDir)
		if note := stateDirNote(*stateDir); note != "" {
			fmt.Fprintf(stdout, "(%s)\n", note)
		}
		return nil
	}

	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	// No address column: the guest takes over whichever address the
	// container runtime assigned its sandbox, which is not something
	// granule chose or has saved out here.
	header := "NAME\tCPUS\tMEMORY_MB\tDISK_MB\tIMAGE"
	if *withStatus {
		// PORTS rides with STATUS rather than with the cheap listing for
		// the same reason STATUS does: it is another docker call per VM.
		header += "\tSTATUS\tPORTS"
	}
	fmt.Fprintln(w, header)
	if !*withStatus {
		for _, s := range specs {
			row := fmt.Sprintf("%s\t%d\t%d\t%s\t%s", s.Name, s.CPUs, s.MemoryMB, diskSizeLabel(s), s.KonturImage)
			fmt.Fprintln(w, row)
		}
		return w.Flush()
	}

	type statusResult struct {
		index  int
		status string
		ports  string
		err    error
	}
	ch := make(chan statusResult, len(specs))
	for i, s := range specs {
		go func(idx int, spec vmspec.VMSpec) {
			st, ports, err := listStatus(ctx, spec, *timeout)
			ch <- statusResult{index: idx, status: st, ports: ports, err: err}
		}(i, s)
	}
	statuses := make([]string, len(specs))
	portsList := make([]string, len(specs))
	var firstErr error
	for range specs {
		res := <-ch
		if res.err != nil && firstErr == nil {
			firstErr = res.err
		}
		statuses[res.index] = res.status
		portsList[res.index] = res.ports
	}
	if firstErr != nil {
		return firstErr
	}
	for i, s := range specs {
		row := fmt.Sprintf("%s\t%d\t%d\t%s\t%s\t%s\t%s", s.Name, s.CPUs, s.MemoryMB, diskSizeLabel(s), s.KonturImage, statuses[i], portsList[i])
		fmt.Fprintln(w, row)
	}
	return w.Flush()
}

// listStatus is the STATUS and PORTS columns for one VM: the same check
// "granule vm status" makes, collapsed to a word (see statusWord) and to
// the host side of what the VM publishes (see portsWord).
func listStatus(ctx context.Context, s vmspec.VMSpec, timeout time.Duration) (status, ports string, err error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	d := &dockervm.Docker{}
	st, err := dockervm.Inspect(ctx, d, s.Name)
	if err != nil {
		// Not this VM being down -- docker itself being unusable, which
		// every other row would hit too.
		return "", "", err
	}
	published, err := dockervm.Published(ctx, d, s.Name)
	if err != nil {
		return "", "", err
	}
	return statusWord(st), portsWord(published), nil
}
