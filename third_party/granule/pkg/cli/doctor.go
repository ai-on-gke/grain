package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"
	"unsafe"

	"github.com/bwsalmon/granule/pkg/dockercli"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// doctorUsage is "granule doctor -h".
const doctorUsage = `Usage: granule doctor [-only <check>,...]

Checks that this host has what a granule VM needs -- /dev/kvm, the kernel
pieces netshim uses, a container daemon that answers -- and says what to
do about anything missing, one line per check, before "granule vm create"
fails somewhere deep inside the VM container instead.

Exits 1 when a required check fails, 0 otherwise; warnings and skipped
checks do not change the status.

Checks: ` + "arch, kvm, kvm-module, nested, tun, vsock, docker, netshim, lsm, io_uring" + `

Flags:
`

// verdict is how one doctor check came out. Only fail changes the exit
// status: a warning is something that may matter on this host and does
// not stop a VM booting on its own, and skip is a check that could not be
// made from here (most often: it needs root).
type verdict int

const (
	verdictPass verdict = iota
	verdictWarn
	verdictFail
	verdictSkip
)

func (v verdict) String() string {
	switch v {
	case verdictPass:
		return "PASS"
	case verdictWarn:
		return "WARN"
	case verdictFail:
		return "FAIL"
	default:
		return "SKIP"
	}
}

// checkResult is one line of the doctor's report: what was checked, how
// it came out, what was found, and -- for anything but a pass -- what to
// do about it.
type checkResult struct {
	name    string
	verdict verdict
	detail  string
	fix     string
}

// doctorHost is everything the doctor reads about the machine it runs on,
// so that tests can point every check at a fake one: root is prefixed to
// every /dev, /sys and /proc path, and the functions stand in for the
// calls that cannot be faked with files (opening a device, asking the
// container daemon, touching the kernel).
type doctorHost struct {
	root   string
	goarch string
	euid   int

	// open opens path (already under root) read-write, the way the VMM
	// opens /dev/kvm, and closes it again.
	open func(path string) error
	// docker runs the container CLI and returns its stdout.
	docker func(ctx context.Context, args ...string) (string, error)
	// netProbe tries netshim's kernel requirements in a throwaway
	// network namespace. Only called as root.
	netProbe func() error
	// ioUringProbe asks the kernel to set up an io_uring.
	ioUringProbe func() error
}

func realDoctorHost() *doctorHost {
	cli := &dockercli.CLI{}
	return &doctorHost{
		root:   "/",
		goarch: runtime.GOARCH,
		euid:   os.Geteuid(),
		open: func(path string) error {
			f, err := os.OpenFile(path, os.O_RDWR, 0)
			if err != nil {
				return err
			}
			return f.Close()
		},
		docker: func(ctx context.Context, args ...string) (string, error) {
			return cli.Output(ctx, args...)
		},
		netProbe:     probeNetshimKernel,
		ioUringProbe: probeIOUring,
	}
}

func (h *doctorHost) path(p string) string { return filepath.Join(h.root, p) }

func (h *doctorHost) exists(p string) bool {
	_, err := os.Stat(h.path(p))
	return err == nil
}

// read returns the trimmed contents of p, and whether it could be read.
func (h *doctorHost) read(p string) (string, bool) {
	b, err := os.ReadFile(h.path(p))
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(b)), true
}

// dockerFacts is what the container daemon says about itself, asked once
// and read by every check that depends on it.
type dockerFacts struct {
	// err is why the daemon could not be asked; the rest is empty then.
	err      error
	version  string
	rootless bool
	seccomp  string // the seccomp profile in use: "builtin", "unconfined", a path, or "" if none
	selinux  bool
	apparmor bool
}

func (h *doctorHost) dockerFacts(ctx context.Context) dockerFacts {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := h.docker(ctx, "version", "--format", "{{.Server.Version}}")
	if err != nil {
		return dockerFacts{err: err}
	}
	f := dockerFacts{version: strings.TrimSpace(out)}
	// Docker spells each option "name=<name>[,key=value...]". Another
	// CLI that does not understand the template leaves these unset,
	// which every check below reads as "do not know" rather than "no".
	opts, err := h.docker(ctx, "info", "--format", "{{range .SecurityOptions}}{{println .}}{{end}}")
	if err != nil {
		return f
	}
	for _, line := range strings.Split(opts, "\n") {
		fields := strings.Split(strings.TrimSpace(line), ",")
		switch fields[0] {
		case "name=rootless":
			f.rootless = true
		case "name=selinux":
			f.selinux = true
		case "name=apparmor":
			f.apparmor = true
		case "name=seccomp":
			f.seccomp = "builtin"
			for _, kv := range fields[1:] {
				if p, ok := strings.CutPrefix(kv, "profile="); ok {
					f.seccomp = p
				}
			}
		}
	}
	return f
}

// runDoctorChecks runs every check against h, in report order.
func runDoctorChecks(ctx context.Context, h *doctorHost) []checkResult {
	d := h.dockerFacts(ctx)
	return []checkResult{
		checkArch(h),
		checkKVM(h, d),
		checkKVMModule(h),
		checkNested(h),
		checkTun(h),
		checkVsock(h),
		checkDocker(d),
		checkNetshim(h),
		checkLSM(h, d),
		checkIOUring(h, d),
	}
}

// checkArch: cloud-hypervisor and the guest images are built for amd64
// and arm64, and config.guestConsoleFor only knows the console of those
// two.
func checkArch(h *doctorHost) checkResult {
	r := checkResult{name: "arch", detail: h.goarch}
	switch h.goarch {
	case "amd64", "arm64":
	default:
		r.verdict = verdictWarn
		r.detail += ": granule's VMM and guest images are built for amd64 and arm64 only"
		r.fix = "expect to build cloud-hypervisor and a guest image yourself, and pass -cmdline with your console"
	}
	return r
}

// checkKVM: the VMM opens /dev/kvm read-write inside the VM container,
// which docker hands it with "--device /dev/kvm". A rootful daemon can
// pass the device through whoever is asking; a rootless one runs as this
// user, so this user's own permissions are what the container gets.
func checkKVM(h *doctorHost, d dockerFacts) checkResult {
	r := checkResult{name: "kvm"}
	info, err := os.Stat(h.path("/dev/kvm"))
	if err != nil {
		r.verdict = verdictFail
		r.detail = "/dev/kvm does not exist"
		switch {
		case !h.cpuHasVirt():
			r.fix = "the CPU does not offer hardware virtualization (no vmx/svm in /proc/cpuinfo): enable VT-x/AMD-V in firmware, or nested virtualization on the VM this runs in"
		case !h.exists("/sys/module/kvm"):
			r.fix = "load KVM: modprobe kvm_intel (or kvm_amd)"
		default:
			r.fix = "KVM is loaded but has no device node: check udev, or mknod /dev/kvm c 10 232"
		}
		return r
	}
	err = h.open(h.path("/dev/kvm"))
	if err == nil {
		r.detail = "/dev/kvm opens read-write"
		return r
	}
	r.detail = fmt.Sprintf("/dev/kvm: %v", unwrapPathError(err))
	group := h.deviceGroup(info)
	r.fix = "add yourself to the group owning /dev/kvm" + group + " and log in again"
	switch {
	case d.err == nil && !d.rootless && errors.Is(err, fs.ErrPermission):
		// The daemon is root and opens the device itself; only running
		// a VMM by hand from this shell would need it.
		r.verdict = verdictWarn
		r.detail += " for this user (docker runs as root and can still pass it to the VM container)"
	case d.rootless:
		r.verdict = verdictFail
		r.detail += ", and docker is rootless, so --device /dev/kvm gets this user's permissions"
	default:
		r.verdict = verdictFail
	}
	return r
}

// deviceGroup names the group owning a device, for the fix line, as
// " (kvm)" -- or " (gid 103)" when /etc/group does not say.
func (h *doctorHost) deviceGroup(info fs.FileInfo) string {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	gid := strconv.FormatUint(uint64(st.Gid), 10)
	if groups, ok := h.read("/etc/group"); ok {
		for _, line := range strings.Split(groups, "\n") {
			f := strings.Split(line, ":")
			if len(f) >= 3 && f[2] == gid {
				return " (" + f[0] + ")"
			}
		}
	}
	return " (gid " + gid + ")"
}

// cpuHasVirt reports whether /proc/cpuinfo shows hardware virtualization.
// arm64's cpuinfo has no such flag, and neither does a /proc that cannot
// be read; both answer true, so the fix line falls through to the module.
func (h *doctorHost) cpuHasVirt() bool {
	info, ok := h.read("/proc/cpuinfo")
	if !ok || h.goarch != "amd64" {
		return true
	}
	for _, line := range strings.Split(info, "\n") {
		if name, flags, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(name) == "flags" {
			for _, f := range strings.Fields(flags) {
				if f == "vmx" || f == "svm" {
					return true
				}
			}
		}
	}
	return false
}

// checkKVMModule names the vendor module, which is also where the nested
// setting lives. A kernel with KVM built in still has /sys/module/kvm*,
// since the modules have parameters.
func checkKVMModule(h *doctorHost) checkResult {
	r := checkResult{name: "kvm-module"}
	for _, m := range []string{"kvm_intel", "kvm_amd"} {
		if h.exists("/sys/module/" + m) {
			r.detail = m + " loaded"
			return r
		}
	}
	if h.exists("/sys/module/kvm") {
		r.detail = "kvm loaded"
		if h.goarch == "amd64" {
			r.verdict = verdictWarn
			r.detail += ", but neither kvm_intel nor kvm_amd is"
			r.fix = "modprobe kvm_intel (or kvm_amd); if that fails, see dmesg -- virtualization may be off in firmware"
		}
		return r
	}
	r.verdict = verdictFail
	r.detail = "no KVM module loaded"
	r.fix = "modprobe kvm_intel (or kvm_amd); a kernel built without CONFIG_KVM cannot run granule VMs"
	return r
}

// checkNested: only a guest that runs VMs of its own needs it, so it is a
// warning at most.
func checkNested(h *doctorHost) checkResult {
	r := checkResult{name: "nested"}
	for _, m := range []string{"kvm_intel", "kvm_amd"} {
		v, ok := h.read("/sys/module/" + m + "/parameters/nested")
		if !ok {
			continue
		}
		if v == "Y" || v == "y" || v == "1" {
			r.detail = "enabled (" + m + ")"
			return r
		}
		r.verdict = verdictWarn
		r.detail = "disabled (" + m + "); granule VMs cannot run VMs of their own"
		r.fix = "only needed for VMs inside granule VMs: echo 'options " + m + " nested=1' > /etc/modprobe.d/kvm-nested.conf and reload " + m
		return r
	}
	r.verdict = verdictSkip
	r.detail = "not detectable here"
	return r
}

// checkTun: netshim opens /dev/net/tun inside the VM container to create
// the guest's tap.
func checkTun(h *doctorHost) checkResult {
	r := checkResult{name: "tun", detail: "/dev/net/tun present"}
	if !h.exists("/dev/net/tun") {
		r.verdict = verdictFail
		r.detail = "/dev/net/tun does not exist"
		r.fix = "modprobe tun (CONFIG_TUN); the VM container is given it with --device /dev/net/tun"
	}
	return r
}

// checkVsock: cloud-hypervisor's vsock is "hybrid" vsock, a virtio device
// it implements itself over a unix socket (see hypervisor.args), so unlike
// QEMU or Firecracker-with-vhost there is no host device to need. The line
// is here so that a host without /dev/vhost-vsock is not mistaken for the
// cause of "vsock port 1024 is not being listened on" -- that is the
// guest's agent, not the host.
func checkVsock(h *doctorHost) checkResult {
	r := checkResult{name: "vsock", detail: "cloud-hypervisor's vsock is userspace over a unix socket; /dev/vhost-vsock is not needed"}
	if h.exists("/dev/vhost-vsock") {
		r.detail = "/dev/vhost-vsock present, though cloud-hypervisor's userspace vsock does not need it"
	}
	return r
}

func checkDocker(d dockerFacts) checkResult {
	bin := dockercli.Binary()
	r := checkResult{name: "docker"}
	if d.err != nil {
		r.verdict = verdictFail
		r.detail = fmt.Sprintf("%s: %v", bin, firstErrLine(d.err.Error()))
		r.fix = "install docker and start its daemon, or point -docker / $" + dockercli.EnvBinary + " at the container CLI to use"
		return r
	}
	r.detail = fmt.Sprintf("%s: daemon %s answers", bin, d.version)
	if d.rootless {
		r.verdict = verdictWarn
		r.detail += " (rootless)"
		r.fix = "rootless docker passes --device /dev/kvm and /dev/net/tun with your own permissions: you need to be able to open both"
	}
	return r
}

// checkNetshim tries what netshim.Setup does to the VM container's network
// namespace -- a bridge, a tap enslaved to it, an ingress qdisc and a u32
// filter with a mirred action -- in a namespace of its own, so a kernel
// missing CONFIG_BRIDGE, CONFIG_NET_SCH_INGRESS, CONFIG_NET_CLS_U32 or
// CONFIG_NET_ACT_MIRRED says so here instead of in a VM container's log.
func checkNetshim(h *doctorHost) checkResult {
	r := checkResult{name: "netshim"}
	if h.euid != 0 {
		r.verdict = verdictSkip
		r.detail = "skipped (needs root): run as root to try bridge, tap, ingress qdisc and mirred in a scratch namespace"
		return r
	}
	if err := h.netProbe(); err != nil {
		r.verdict = verdictFail
		r.detail = err.Error()
		r.fix = "the kernel needs bridge, tun, sch_ingress, cls_u32 and act_mirred (modprobe them, or a kernel with CONFIG_BRIDGE, CONFIG_TUN, CONFIG_NET_SCH_INGRESS, CONFIG_NET_CLS_U32, CONFIG_NET_ACT_MIRRED)"
		return r
	}
	r.detail = "bridge, tap, ingress qdisc and mirred all work in a scratch namespace"
	return r
}

// checkLSM: the VM container is not --privileged; it gets the two devices
// and NET_ADMIN, under no-new-privileges, and whatever SELinux or AppArmor
// policy docker applies by default (dockervm.vmSecurityArgs). That works
// under docker's stock AppArmor profile; SELinux policy varies more by
// distribution, so enforcing mode is worth a warning.
func checkLSM(h *doctorHost, d dockerFacts) checkResult {
	r := checkResult{name: "lsm"}
	var found []string
	if v, ok := h.read("/sys/fs/selinux/enforce"); ok {
		if v == "1" {
			found = append(found, "SELinux enforcing")
			r.verdict = verdictWarn
			r.fix = "the VM container is given --device /dev/kvm, --device /dev/net/tun and --security-opt no-new-privileges under the default container label; if the VMM cannot open /dev/kvm or netshim cannot create its tap, look for AVC denials (ausearch -m avc)"
			if d.err == nil && !d.selinux {
				r.fix += "; docker's own SELinux support is off here"
			}
		} else {
			found = append(found, "SELinux permissive")
		}
	}
	if v, ok := h.read("/sys/module/apparmor/parameters/enabled"); ok && v == "Y" {
		found = append(found, "AppArmor enabled (docker-default profile applies; it allows what the VM container needs)")
	}
	if len(found) == 0 {
		r.detail = "no SELinux or AppArmor"
		return r
	}
	r.detail = strings.Join(found, "; ")
	return r
}

// dockerSeccompBlocksIOUring is the first docker whose default seccomp
// profile denies io_uring_setup (moby v25.0).
const dockerSeccompBlocksIOUring = 25

// checkIOUring: a VM can be given CHV_DISK_IO_ENGINE=io_uring
// (-disk-io-engine, VM SET disk-io-engine=). That needs the host kernel to
// allow it, and the VM container's seccomp profile too -- and docker's
// default one stopped allowing it in v25. Nothing uses io_uring unless
// asked, so this is a warning at most.
func checkIOUring(h *doctorHost, d dockerFacts) checkResult {
	r := checkResult{name: "io_uring"}
	if v, ok := h.read("/proc/sys/kernel/io_uring_disabled"); ok && v == "2" {
		r.verdict = verdictWarn
		r.detail = "disabled by kernel.io_uring_disabled=2"
		r.fix = "use -disk-io-engine epoll, or sysctl kernel.io_uring_disabled=0"
		return r
	}
	if err := h.ioUringProbe(); err != nil {
		r.verdict = verdictWarn
		r.detail = fmt.Sprintf("not available: %v", err)
		r.fix = "use -disk-io-engine epoll (or sync); io_uring needs Linux 5.1+ and no seccomp or sysctl blocking it"
		return r
	}
	r.detail = "available on the host"
	if d.err == nil && d.seccomp != "" && d.seccomp != "unconfined" && dockerMajor(d.version) >= dockerSeccompBlocksIOUring {
		r.verdict = verdictWarn
		r.detail += fmt.Sprintf(", but docker %s's default seccomp profile blocks it inside the VM container", d.version)
		r.fix = "for -disk-io-engine io_uring, add -docker-run-opt --security-opt -docker-run-opt seccomp=<a profile allowing io_uring_*>; otherwise use epoll"
	}
	return r
}

func dockerMajor(version string) int {
	major, _, _ := strings.Cut(strings.TrimPrefix(version, "v"), ".")
	n, err := strconv.Atoi(major)
	if err != nil {
		return 0
	}
	return n
}

func unwrapPathError(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

func firstErrLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

// probeIOUring calls io_uring_setup with a one-entry ring and closes it.
func probeIOUring() error {
	params := make([]byte, 120) // struct io_uring_params, zeroed
	fd, _, errno := unix.Syscall(unix.SYS_IO_URING_SETUP, 1, uintptr(unsafe.Pointer(&params[0])), 0)
	if errno != 0 {
		return errno
	}
	return unix.Close(int(fd))
}

// probeNetshimKernel runs probeScratchNetns on a thread of its own. A
// network namespace belongs to a thread, so the goroutine locks one,
// unshares it into a new namespace and never unlocks it: when a goroutine
// exits still locked, the runtime ends its thread rather than reusing it,
// which takes the namespace -- and everything the probe made in it --
// away with it, and no other goroutine ever runs there.
func probeNetshimKernel() error {
	errc := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		errc <- probeScratchNetns()
	}()
	return <-errc
}

func probeScratchNetns() error {
	if err := unix.Unshare(unix.CLONE_NEWNET); err != nil {
		return fmt.Errorf("creating a scratch network namespace: %w", err)
	}
	br := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "doctor-br"}}
	if err := netlink.LinkAdd(br); err != nil {
		return fmt.Errorf("adding a bridge: %w", err)
	}
	tap := &netlink.Tuntap{LinkAttrs: netlink.LinkAttrs{Name: "doctor-tap"}, Mode: netlink.TUNTAP_MODE_TAP}
	if err := netlink.LinkAdd(tap); err != nil {
		return fmt.Errorf("adding a tap: %w", err)
	}
	tapLink, err := netlink.LinkByName("doctor-tap")
	if err != nil {
		return fmt.Errorf("finding the tap: %w", err)
	}
	brLink, err := netlink.LinkByName("doctor-br")
	if err != nil {
		return fmt.Errorf("finding the bridge: %w", err)
	}
	if err := netlink.LinkSetMaster(tapLink, brLink); err != nil {
		return fmt.Errorf("enslaving a tap to a bridge: %w", err)
	}
	if err := netlink.QdiscAdd(&netlink.Ingress{QdiscAttrs: netlink.QdiscAttrs{
		LinkIndex: tapLink.Attrs().Index,
		Handle:    netlink.MakeHandle(0xffff, 0),
		Parent:    netlink.HANDLE_INGRESS,
	}}); err != nil {
		return fmt.Errorf("adding an ingress qdisc: %w", err)
	}
	if err := netlink.FilterAdd(&netlink.U32{
		FilterAttrs: netlink.FilterAttrs{
			LinkIndex: tapLink.Attrs().Index,
			Parent:    netlink.MakeHandle(0xffff, 0),
			Priority:  1,
			Protocol:  unix.ETH_P_ALL,
		},
		Actions: []netlink.Action{netlink.NewMirredAction(brLink.Attrs().Index)},
	}); err != nil {
		return fmt.Errorf("adding a u32 filter with a mirred action: %w", err)
	}
	return nil
}

// runDoctor is "granule doctor".
func runDoctor(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	return doctor(ctx, realDoctorHost(), args, stdout, stderr)
}

func doctor(ctx context.Context, h *doctorHost, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("granule doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprint(stderr, doctorUsage)
		flags.PrintDefaults()
	}
	only := flags.String("only", "", "comma-separated checks to run and judge by, e.g. \"kvm,tun\"; default all")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("doctor takes no arguments, got %q", flags.Args())
	}

	results := runDoctorChecks(ctx, h)
	if *only != "" {
		want := map[string]bool{}
		for _, n := range strings.Split(*only, ",") {
			want[strings.TrimSpace(n)] = true
		}
		var kept []checkResult
		for _, r := range results {
			if want[r.name] {
				kept = append(kept, r)
				delete(want, r.name)
			}
		}
		for n := range want {
			return fmt.Errorf("-only: no check named %q", n)
		}
		results = kept
	}

	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	var failed []string
	for _, r := range results {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", r.verdict, r.name, r.detail)
		if r.fix != "" && r.verdict != verdictPass {
			fmt.Fprintf(tw, "\t\t-> %s\n", r.fix)
		}
		if r.verdict == verdictFail {
			failed = append(failed, r.name)
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if len(failed) > 0 {
		return fmt.Errorf("doctor: required checks failed: %s", strings.Join(failed, ", "))
	}
	return nil
}
