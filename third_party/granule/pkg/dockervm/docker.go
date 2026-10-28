// Package dockervm runs one self-contained Granule container per VM using
// the Docker CLI. The runtime sets up its own network namespace before
// booting cloud-hypervisor; no namespace holder or init container is needed.
package dockervm

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strconv"
	"strings"

	"github.com/bwsalmon/granule/pkg/dockercli"
	"github.com/bwsalmon/granule/pkg/guestimage"
	"github.com/bwsalmon/granule/pkg/vmspec"
)

// netshimEnvArgs describes the network the VM container sets up itself.
func netshimEnvArgs(spec vmspec.VMSpec) []string {
	if spec.NetMode == "none" {
		return []string{
			"-e", "CHV_NET_MODE=none",
		}
	}
	args := []string{
		"-e", "CHV_NET_MODE=inline",
		"-e", "NETSHIM_VM=" + spec.Name,
		"-e", "NETSHIM_BRIDGE=" + spec.Bridge,
		"-e", "NETSHIM_CONTROL_CIDR=" + spec.ControlCIDR,
		"-e", "NETSHIM_EXTERNAL_IFACE=" + spec.ExternalIface,
		// Always passed, empty value included: the VM container is what
		// assembles the guest's ip= parameter, so an unset variable
		// there would mean netshim's own default rather than the
		// nameservers this VM was created with ("auto" included, which
		// that container resolves against its own resolv.conf) -- and
		// "-dns ''" is how a
		// caller asks for the guest image's own resolv.conf to be left
		// alone.
		"-e", "NETSHIM_DNS=" + spec.DNS,
	}
	// Only when set, so a spec saved before -ipv6 existed creates
	// exactly the container it always did; netshim defaults to auto.
	if spec.IPv6 != "" {
		args = append(args, "-e", "NETSHIM_IPV6="+spec.IPv6)
	}
	return args
}

// Docker runs the docker CLI to implement Create/Delete below. "vm update"
// has no separate implementation here: pkg/cli drives it as a
// Delete followed by a Create, since docker containers have no equivalent
// of re-submitting a pod manifest for the kubelet to reconcile.
type Docker struct {
	// BinaryPath is the docker binary to exec, resolved via PATH if it
	// contains no slash. Defaults to dockercli.Binary() ($GRANULE_DOCKER,
	// else "docker"); tests point it at a fake
	// so this package can be exercised without a real docker daemon.
	BinaryPath string
}

func (d *Docker) binary() string {
	if d.BinaryPath == "" {
		return dockercli.Binary()
	}
	return d.BinaryPath
}

// run execs "docker <args...>", streaming stdout to the given writer and
// folding stderr into the returned error.
func (d *Docker) run(ctx context.Context, stdout io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, d.binary(), args...)
	var stderr bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// isNotFound reports whether err came from a docker CLI invocation that
// failed because the named container doesn't exist -- the docker CLI's
// only way of expressing that is a stderr substring, and it words it
// differently per subcommand ("No such container" from stop/rm/exec, "No
// such object" from inspect).
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "No such container") || strings.Contains(msg, "No such object")
}

// remove force-removes a container, treating "already gone" as success.
func (d *Docker) remove(ctx context.Context, name string) error {
	if err := d.run(ctx, io.Discard, "rm", "-f", name); err != nil && !isNotFound(err) {
		return err
	}
	return nil
}

// stopAndRemove stops a container (SIGTERM, then SIGKILL after
// graceSeconds -- the same shape as kubelet's terminationGracePeriodSeconds
// handling, which is what lets "granule run"'s own
// SIGTERM->power-button->forced-shutdown sequence, see the top-level
// README's "Shutdown" section, play out the same way here as under a real
// kubelet) and removes it, treating "already gone" as success throughout.
func (d *Docker) stopAndRemove(ctx context.Context, name string, graceSeconds int) error {
	if err := d.run(ctx, io.Discard, "stop", "-t", strconv.Itoa(graceSeconds), name); err != nil && !isNotFound(err) {
		return err
	}
	return d.remove(ctx, name)
}

// Keep the legacy holder name for cleanup during upgrades and deletes.
// New VMs only create vmContainerName.
func vmContainerName(name string) string { return vmspec.ContainerName(name) }
func netnsContainerName(name string) string {
	return vmspec.ContainerName(name) + "-netns"
}

// vmSecurityArgs is everything the VM container is granted beyond an
// ordinary container, and it is deliberately not --privileged: that hands
// the container every capability, every host device and no seccomp or
// AppArmor confinement, and the VMM inside it opens files whose paths come
// from guest images (a qcow2 layer's backing_file, for one) that a
// repository may have chosen. What "granule run" actually needs is
// /dev/kvm for the VMM, /dev/net/tun plus CAP_NET_ADMIN for netshim's tap,
// bridge and tc splice (see netshim.Setup, and the top-level README's
// "Running on Unmodified Docker"), and nothing else -- so every other
// capability, Docker's defaults included, is dropped, and no-new-privileges
// keeps a setuid binary in the image from getting any of them back. A
// caller that really does need more can still say so in DockerRunOpts.
var vmSecurityArgs = []string{
	"--device", "/dev/kvm",
	"--device", "/dev/net/tun",
	"--cap-drop", "ALL",
	"--cap-add", "NET_ADMIN",
	"--security-opt", "no-new-privileges",
}

// Create starts one VM container. Networking is initialized by "granule run"
// inside that container before the VMM boots. Old namespace holders are
// still removed so updates from the split-container layout do not leak.
func Create(ctx context.Context, d *Docker, spec vmspec.VMSpec, stdout io.Writer) error {
	netnsName := netnsContainerName(spec.Name)
	vmName := vmContainerName(spec.Name)

	// Before anything is removed or started: a VM whose image cannot be
	// asked what it declares is a VM this caller asked to have published
	// and granule cannot publish, and finding that out after the
	// container is up means a guest that is running and unreachable.
	publish, err := publishArgs(ctx, d, spec, stdout)
	if err != nil {
		return err
	}

	// A previous Create for this VM name may have died partway through
	// -- e.g. the daemon restarting between "docker run"ning these
	// containers and granule persisting that success to local state --
	// leaving one or both containers running under the names this
	// Create is about to reuse. Force-remove any such leftovers first so
	// Create is idempotent the same way Delete already is, rather than
	// failing outright on docker's "name already in use" error.
	if err := d.remove(ctx, vmName); err != nil {
		return fmt.Errorf("removing stale VM container %s: %w", vmName, err)
	}
	if err := d.remove(ctx, netnsName); err != nil {
		return fmt.Errorf("removing stale network namespace holder %s: %w", netnsName, err)
	}

	vmArgs := []string{
		"run", "-d",
		"--name", vmName,
		"--label", "granule.dev/vm=" + spec.Name,
		"--oom-score-adj", strconv.Itoa(spec.EffectiveOOMScoreAdj()),
	}
	if limitMB := spec.ContainerMemoryLimitMB(); limitMB > 0 {
		mem := fmt.Sprintf("%dm", limitMB)
		vmArgs = append(vmArgs, "--memory", mem, "--memory-swap", mem)
	}
	vmArgs = append(vmArgs, vmSecurityArgs...)
	// Left unset when nothing on this side has an opinion about the disk,
	// so the mode the machine image recorded for itself stands (a Granule's
	// "VM SET disk-mode=", docs/granulefile.md §5.5). A spec that came
	// through "vm create" always has one by then -- either the caller's or
	// the image's own, resolved into it before it was validated and saved
	// (vmspec.VMSpec.ResolveSettings) -- so this is the boundary being honest
	// rather than a second implementation of that choice.
	if spec.DiskMode != "" || spec.DiskReadOnly {
		vmArgs = append(vmArgs, "-e", "CHV_DISK_MODE="+spec.DiskModeOrDerived())
	}
	// Port publishing, network membership and aliases now belong directly
	// to the VM container, which owns the namespace.
	vmArgs = append(vmArgs, spec.DockerRunOpts...)
	vmArgs = append(vmArgs, publish...)
	// Left unset when the VM asked for no particular size, so its overlay
	// keeps taking the disk image's own. See VMSpec.DiskSizeMB.
	if spec.DiskSizeMB > 0 {
		vmArgs = append(vmArgs, "-e", "CHV_DISK_SIZE_MB="+strconv.Itoa(spec.DiskSizeMB))
	}
	if spec.ExtentSizeMB > 0 {
		vmArgs = append(vmArgs, "-e", "CHV_EXTENT_SIZE_MB="+strconv.Itoa(spec.ExtentSizeMB))
	}
	if spec.DiskNumQueues > 0 {
		vmArgs = append(vmArgs, "-e", "CHV_DISK_NUM_QUEUES="+strconv.Itoa(spec.DiskNumQueues))
	}
	if spec.DiskIOThreads {
		vmArgs = append(vmArgs, "-e", "CHV_DISK_IOTHREADS=true")
	}
	if spec.DiskIOEngine != "" {
		vmArgs = append(vmArgs, "-e", "CHV_DISK_IO_ENGINE="+spec.DiskIOEngine)
	}
	if spec.StorageBackend != "" {
		vmArgs = append(vmArgs, "-e", "CHV_STORAGE_BACKEND="+spec.StorageBackend)
	}
	// A spec with no disk of its own boots the one baked into the granule
	// image: nothing to mount, and CHV_DISK_IMAGE left unset so "granule
	// run"'s own default applies. See VMSpec.Validate.
	if spec.DiskImage != "" {
		// Read-only, always: a writable disk is an overlay the VM's
		// own container creates against this image now, so nothing here
		// ever writes to the shared mount.
		vmArgs = append(vmArgs,
			"-v", spec.ImagesHostPath+":"+vmspec.ImagesMountPath+":ro",
			"-e", "CHV_DISK_IMAGE="+spec.DiskImage)
	}
	if spec.Kernel != "" {
		vmArgs = append(vmArgs, "-e", "CHV_KERNEL="+spec.Kernel)
	}
	if spec.Initramfs != "" {
		vmArgs = append(vmArgs, "-e", "CHV_INITRAMFS="+spec.Initramfs)
	}
	if spec.Firmware != "" {
		vmArgs = append(vmArgs, "-e", "CHV_FIRMWARE="+spec.Firmware)
	}
	if spec.Cmdline != "" {
		vmArgs = append(vmArgs, "-e", "CHV_CMDLINE="+spec.Cmdline)
	}
	// The VM container derives its own --net values (and the guest's ip=
	// parameter) at boot from the identity on the namespace's external
	// interface, so it gets the same netshim settings rather than a
	// precomputed CHV_NET. See cmd/granule-shim's applyNetshimNet.
	vmArgs = append(vmArgs, netshimEnvArgs(spec)...)
	// The boot sizing, each left unset when the spec names none: granule's
	// own numbers are passed as an explicit -e, which overrides the image's
	// ENV the way "docker run -e" does, so a VM that asked for no
	// particular size must not be given one here. Nothing that goes
	// through "vm create" arrives unset -- the image's own sizing is
	// resolved into the spec first (vmspec.VMSpec.ResolveSettings) so the
	// saved state says what the VM is running with -- but a caller of this
	// package that leaves it out is asking for the machine's own, and gets
	// it.
	if spec.CPUs > 0 {
		vmArgs = append(vmArgs, "-e", "CHV_CPUS="+strconv.Itoa(spec.CPUs))
	}
	if spec.MemoryMB > 0 {
		vmArgs = append(vmArgs, "-e", "CHV_MEMORY_MB="+strconv.Itoa(spec.MemoryMB))
	}
	vmArgs = append(vmArgs, "-e", "CHV_SHUTDOWN_TIMEOUT="+spec.ShutdownTimeout)
	// Only when asked for: zero means "no headroom", and passing the
	// boot values through as their own ceilings is what granule run
	// already does with them unset. See VMSpec.CPUsMax.
	if spec.CPUsMax != 0 {
		vmArgs = append(vmArgs, "-e", "CHV_CPUS_MAX="+strconv.Itoa(spec.CPUsMax))
	}
	if spec.MemoryMaxMB != 0 {
		vmArgs = append(vmArgs, "-e", "CHV_MEMORY_MAX_MB="+strconv.Itoa(spec.MemoryMaxMB))
	}
	// Guest-driven growth, off unless this VM asked for it (see
	// VMSpec.MemAgent). The three tuning variables go on only when the
	// agent itself does and only when they were set, so a VM that asked
	// for nothing in particular gets the VM container's own defaults --
	// which are what the guest image is built to signal.
	if spec.MemAgent {
		vmArgs = append(vmArgs, "-e", "CHV_MEM_AGENT=true")
		if spec.MemAgentAddr != "" {
			vmArgs = append(vmArgs, "-e", "CHV_MEM_AGENT_ADDR="+spec.MemAgentAddr)
		}
		if spec.MemAgentToken != "" {
			vmArgs = append(vmArgs, "-e", "CHV_MEM_AGENT_TOKEN="+spec.MemAgentToken)
		}
		if spec.MemAgentStepMB != 0 {
			vmArgs = append(vmArgs, "-e", "CHV_MEM_AGENT_STEP_MB="+strconv.Itoa(spec.MemAgentStepMB))
		}
		if spec.MemAgentCooldown != "" {
			vmArgs = append(vmArgs, "-e", "CHV_MEM_AGENT_COOLDOWN="+spec.MemAgentCooldown)
		}
	}
	// The account "granule exec" runs commands as inside the guest (see
	// VMSpec.GuestUser). It used to be read twice, because the old SSH
	// transport also needed the guest told which account to authorize
	// this boot's key for; over vsock the agent runs as root and drops
	// to whatever account a request names, so this is now read only by
	// "granule exec" itself -- which docker exec runs with the
	// container's own environment.
	if spec.GuestUser != "" {
		vmArgs = append(vmArgs, "-e", "GRANULE_EXEC_USER="+spec.GuestUser)
	}
	// The guest's own readiness declaration, if this VM has one (see
	// VMSpec.ReadyFile). On the container rather than on the probe's
	// command line, so that every "granule ready" run against this VM --
	// the one behind "vm wait" and "vm status", and one typed by hand --
	// asks the same question. Absent, readiness stays "granule-agent
	// answered", which is what it means for every VM created before this
	// existed.
	if spec.ReadyFile != "" {
		vmArgs = append(vmArgs, "-e", envReadyFile+"="+spec.ReadyFile)
	}
	for _, env := range spec.Env {
		vmArgs = append(vmArgs, "-e", env)
	}
	vmArgs = append(vmArgs, spec.KonturImage, "run")
	if err := d.run(ctx, io.Discard, vmArgs...); err != nil {
		_ = d.remove(context.WithoutCancel(ctx), vmName)
		return fmt.Errorf("starting VM container %s: %w", vmName, err)
	}

	fmt.Fprintf(stdout, "started %s (VM with inline networking)\n", vmName)
	if len(publish) > 0 {
		reportPublished(ctx, d, spec.Name, stdout)
	}
	return nil
}

// publishArgs is the granting half of EXPOSE: the "docker run" options
// that make the ports this VM's image declares reachable, or nothing at
// all when the VM did not ask for that.
//
// # Why a port needs granting at all, and why this is the place
//
// A granule guest is not a process in the VM container -- it is a
// machine behind it, spliced onto the container's own interface by
// netshim, answering to the address the container runtime assigned (see
// the top-level README's "Container networking"). That is what makes the
// path a published port takes here an ordinary one: docker's own
// publication DNATs to the container's address, the splice redirects
// that interface's ingress into the tap, and the guest -- which holds
// that address -- receives it. So the whole of honouring a declaration
// is publishing it on the container, and there is no per-VM forwarding,
// no rule on the data path and nothing inside the guest to configure.
//
// It also has to be *here*: publication is a property of a container,
// fixed when the container is created, and nothing that joins a
// namespace afterwards can add one. The same reason DockerRunOpts land
// on this call.
//
// # Read from the image, at each create
//
// The declaration belongs to the image, so it is read off the image
// rather than out of the saved spec (see VMSpec.PublishDeclaredPorts) --
// `docker image inspect` of the very image this container is about to
// run, which is a manifest already on the machine and starts nothing.
// An image repacked with a new EXPOSE therefore publishes the new port
// on the next create, and a spec saved months ago never speaks for
// somebody else's image.
//
// Each declared port becomes `-p <addr>::<port>/<proto>`, so docker
// picks the host port, which is what `docker run -P` does with the same
// field. Pinning a host port is a different request with a different
// answer -- `-docker-run-opt -p 8080:80`, which works whether or not
// anything was declared -- and it stays the caller's to make: a
// deployment that let an image choose host ports would be letting the
// image choose what it collides with.
//
// # Bound to loopback unless asked otherwise
//
// Where `docker run -P` differs is the address. Docker binds a bare
// `-p` to every interface, so -P opens the image's ports to every
// network the host is on -- reasonable for an image the user wrote, and
// a lot to grant one somebody else did, which is the image a
// -publish-declared VM usually runs. So the address is
// VMSpec.PublishAddrOrDefault, loopback unless -publish-addr says
// otherwise, and reaching a declared port from off this host is a
// decision the caller makes in so many words. A `-p` in DockerRunOpts
// is left exactly as written: it is a raw docker flag, and rewriting it
// would make it mean something other than what docker documents.
func publishArgs(ctx context.Context, d *Docker, spec vmspec.VMSpec, stdout io.Writer) ([]string, error) {
	if !spec.PublishDeclaredPorts {
		return nil, nil
	}
	ports, err := guestimage.DeclaredPorts(ctx, d.binary(), spec.KonturImage, stdout)
	if err != nil {
		// Refused rather than degraded to publishing nothing: -publish-declared
		// is a caller saying yes to whatever the image asks for, and a
		// yes that quietly grants nothing produces a VM that looks
		// healthy and answers on no port at all.
		return nil, fmt.Errorf("%w; pull it first, or publish the ports explicitly with -docker-run-opt -p", err)
	}
	if len(ports) == 0 {
		fmt.Fprintf(stdout, "%s declares no ports with EXPOSE, so -publish-declared published nothing; -docker-run-opt -p HOST:GUEST publishes one anyway\n", spec.KonturImage)
		return nil, nil
	}
	addr := spec.PublishAddrOrDefault()
	fmt.Fprintf(stdout, "publishing what %s declares with EXPOSE on %s (%s): %s\n", spec.KonturImage, addr, bindScope(addr), strings.Join(ports, ", "))
	// JoinHostPort brackets an IPv6 address, which is how docker's -p
	// tells it apart from the colons that follow.
	host := net.JoinHostPort(addr, "")
	args := make([]string, 0, 2*len(ports))
	for _, port := range ports {
		args = append(args, "-p", host+":"+port)
	}
	return args, nil
}

// reportPublished prints the host side of what was just published, which
// is the half the caller does not already know: docker assigns those
// ports, the way it does for `docker run -P`.
//
// Best effort, and deliberately not an error: the VM is up and its ports
// are published by the time this is asked, so a docker that cannot
// answer this one question is worth a line saying where to ask again
// rather than a failed create of a VM that exists. It is also not the
// only chance to ask -- "granule vm status <name>" reports the same
// mapping for as long as the VM runs (see Published).
func reportPublished(ctx context.Context, d *Docker, name string, stdout io.Writer) {
	ports, err := Published(ctx, d, name)
	if err != nil || len(ports) == 0 {
		fmt.Fprintf(stdout, "(\"granule vm status %s\" shows which host ports they landed on)\n", name)
		return
	}
	for _, p := range ports {
		fmt.Fprintf(stdout, "  %s\n", p.Describe())
	}
}

// Delete stops and removes a VM's containers. Safe to call when they
// don't exist at all (e.g. a retried delete, or a VM that never finished
// Create), and safe to call concurrently with nothing else touching the
// same VM name.
func Delete(ctx context.Context, d *Docker, name string, terminationGracePeriodSeconds int, stdout io.Writer) error {
	vmName := vmContainerName(name)
	netnsName := netnsContainerName(name)

	if err := d.stopAndRemove(ctx, vmName, terminationGracePeriodSeconds); err != nil {
		return fmt.Errorf("removing VM container %s: %w", vmName, err)
	}
	if err := d.remove(ctx, netnsName); err != nil {
		return fmt.Errorf("removing network namespace holder %s: %w", netnsName, err)
	}
	fmt.Fprintf(stdout, "removed %s (and any legacy namespace holder)\n", vmName)
	return nil
}
