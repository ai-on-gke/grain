// Command granule-container runs the container a `granule wrap` image was
// built around, inside the VM that image boots.
//
// It is the guest-side half of wrap (pkg/guestwrap is the build-time
// half): wrap packs an ordinary container image's filesystem into the
// guest at containerspec.RootfsDir and writes its OCI configuration to
// containerspec.ConfigPath, and this is what the guest's init starts once
// the machine is up. It mounts the pseudo-filesystems a container expects,
// pivots into that filesystem in a mount namespace of its own (enter.go),
// drops to the image's user, and execs the
// image's entrypoint with the image's environment -- so the process sees
// what it would have seen under a container runtime, while the machine
// around it is a real kernel with granule's agent, networking and shutdown
// handling in it.
//
// What it deliberately does not do is isolate. There are no cgroup
// limits, no seccomp and no namespaces but the mount namespace, and
// there is nothing for them to protect: the container is the only thing
// in this VM, and the boundary that matters is the VM's. The mount
// namespace is not there to isolate either: it is what lets the image's
// root be a real root, which is what the container runtimes and user
// namespaces a workload may start of its own need of it.
//
// The VM powers off when the container's process exits (Spec.HaltOnExit,
// `granule wrap -halt-on-exit=false` to keep the machine), so a `docker
// run` of a wrapped image ends when the container it wraps would have
// ended -- and with the status it would have ended with: the exit status
// is reported to the host's granule-shim before the machine stops, and
// the shim exits with it (see pkg/workloadio's "Exit status").
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/bwsalmon/granule/pkg/containerspec"
	"github.com/bwsalmon/granule/pkg/workloadio"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("granule-container: ")

	// This program is also the step between its own fork and the
	// workload's exec (see enter.go), and is told so before anything
	// else can run.
	if _, ok := os.LookupEnv(enterEnv); ok {
		enterMain()
	}

	flags := flag.NewFlagSet("granule-container", flag.ContinueOnError)
	config := flags.String("config", containerspec.ConfigPath, "the container spec `granule wrap` wrote into this guest")
	effective := flags.String("effective", containerspec.EffectiveConfigPath, "where to write the spec actually run, with this VM's override merged in, for granule-agent's container exec")
	if err := flags.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		log.Fatalf("%v", err)
	}

	spec, err := effectiveSpec(*config, *effective, "/sys/block", "/dev")
	if err != nil {
		log.Fatalf("%v", err)
	}

	// The container's stdout and stderr are offered to the host on a
	// vsock port of their own as well as written to the console (see
	// pkg/workloadio), for a host that runs this VM as a container and
	// has to keep the two apart.
	broker := workloadio.NewBroker(0)
	// And it takes its stdin from there, and its terminal's size, if its
	// spec asks for them (`docker run -i` and -t): announced before
	// anything can attach, since a host decides what to send from the
	// first thing it reads.
	broker.OfferInput(spec.OpenStdin, spec.Tty)
	if err := serveStreams(broker); err != nil {
		log.Printf("%v; the container's output reaches the console only", err)
	}

	// The host is told the container has started, and later how it
	// exited, so that the VM container's own exit code can be the
	// workload's (see pkg/workloadio's "Exit status").
	reporter := newExitReporter(dialHostExit)
	reporter.start()

	code, asked, err := run(spec, broker)
	// Before anything else, and before the machine powers off: the host
	// that asked for the container's output gets the end of it. One that
	// did not ask is not waited for.
	wait := time.Duration(0)
	if streamsRequested() {
		wait = workloadio.ExitWait
	}
	if !broker.Close(wait) && wait > 0 {
		log.Printf("the host did not collect all of the container's output within %s", wait)
	}
	if err != nil {
		// A container that never started leaves the machine up on
		// purpose, whatever HaltOnExit says: this is a misconfigured
		// image rather than a finished workload, and a VM that stays
		// around is one `granule exec` can get into to find out why.
		log.Fatalf("%v", err)
	}
	// Before the machine powers off, and whether or not it is going to:
	// the status is the workload's, and a VM kept up after it is still
	// one whose host may want to know.
	reporter.exit(code)
	switch {
	case asked:
		// The machine is already stopping -- this service was signalled
		// as part of its shutdown -- so there is nothing to power off,
		// and asking for one in the middle of somebody else's shutdown
		// only gets in its way.
		log.Printf("the container exited with status %d on its way down", code)
	case spec.HaltOnExit:
		log.Printf("the container exited with status %d, powering the VM off", code)
		powerOff()
	}
	os.Exit(code)
}

// effectiveSpec is the spec this boot runs: the baked one at config with
// the VM's own override, if it was started with one, merged over it (see
// containerspec.Override). It is written to effective whether or not
// there was an override, so that granule-agent's container exec enters
// exactly the container this program started rather than re-deriving it;
// a spec left there by an earlier boot is never read here, since the
// override is the VM's and this boot's may differ.
func effectiveSpec(config, effective, sysBlock, dev string) (containerspec.Spec, error) {
	spec, err := containerspec.Load(config)
	if err != nil {
		return spec, err
	}
	o, found, err := containerspec.FindOverrideDevice(sysBlock, dev)
	if err != nil {
		return spec, err
	}
	if found && !o.IsZero() {
		spec = spec.Apply(o)
		if err := spec.Validate(); err != nil {
			return spec, fmt.Errorf("the container spec at %s with this VM's override is unusable: %w", config, err)
		}
		log.Printf("running %q with this VM's container override", spec.Argv())
	}
	if effective != "" {
		data, err := spec.Marshal()
		if err == nil {
			err = os.MkdirAll(filepath.Dir(effective), 0o755)
		}
		if err == nil {
			// Owner-only: an override's environment is where a
			// per-container secret would arrive.
			err = os.WriteFile(effective, data, 0o600)
		}
		if err != nil {
			// The container still runs; only an exec into it falls back
			// to finding the override itself (containerspec.LoadEffective).
			log.Printf("cannot write the effective container spec to %s: %v", effective, err)
		}
	}
	return spec, nil
}

// run starts the container's process and waits for it, returning its exit
// status and whether the container was asked to stop rather than having
// finished on its own. An error means the process never ran; a non-zero
// status means it ran and failed, which is the container's business
// rather than a failure of this program.
func run(spec containerspec.Spec, broker *workloadio.Broker) (int, bool, error) {
	if _, err := os.Stat(spec.Rootfs); err != nil {
		return 0, false, fmt.Errorf("the wrapped filesystem is not at %s: %w", spec.Rootfs, err)
	}

	prepare(spec.Rootfs)

	// Before anything is looked up inside the container: a share mounted
	// over a directory is what the container's process sees there, so it
	// is what its user, its program and its working directory have to be
	// found in too.
	shares, err := mountShares(spec.Rootfs, spec.Mounts, syscall.Mount)
	if err != nil {
		return 0, false, err
	}

	cred, err := containerspec.ResolveUser(spec.Rootfs, spec.User)
	if err != nil {
		return 0, false, err
	}

	argv := spec.Argv()
	program, err := lookPath(spec.Rootfs, argv[0], spec.Path())
	if err != nil {
		return 0, false, err
	}

	// A WORKDIR an image names but does not contain is created rather
	// than refused, which is what a container runtime does with the same
	// image -- and the failure otherwise arrives as a chdir error from
	// exec, naming a path that looks like it should be there.
	//
	// Both this and the volumes below are paths the image chose, created
	// and chowned by a process that is still root on the guest and not
	// yet chrooted, so they go through containerspec.MkdirIn: resolved
	// the way the chroot will resolve them, and never followed out of the
	// wrapped filesystem by a ".." or a symlink the image planted.
	if spec.WorkingDir != "" && spec.WorkingDir != "/" {
		dir, created, err := containerspec.MkdirIn(spec.Rootfs, spec.WorkingDir, 0o755)
		if err != nil {
			return 0, false, fmt.Errorf("creating the container's working directory %s: %w", spec.WorkingDir, err)
		}
		if created && cred.Set {
			// The image's user has to be able to write in a directory
			// created on its behalf; one the image shipped keeps
			// whatever ownership the image gave it.
			_ = os.Lchown(filepath.Join(spec.Rootfs, dir), int(cred.UID), int(cred.GID))
		}
	}

	for _, vol := range spec.Volumes {
		if vol == "" {
			continue
		}
		dir, _, err := containerspec.MkdirIn(spec.Rootfs, vol, 0o755)
		if err != nil {
			log.Printf("cannot create the container's volume %s: %v", vol, err)
			continue
		}
		// A volume that resolves to the container's root (VOLUME /, or
		// one that climbs there with "..") is not a directory to hand
		// over: chowning it would give the image's user the top of the
		// wrapped filesystem.
		//
		// Nor is a volume the host has mounted a share on: that
		// directory is the host's, and its ownership is too.
		if cred.Set && dir != "/" && !shares[filepath.Join(spec.Rootfs, dir)] {
			_ = os.Lchown(filepath.Join(spec.Rootfs, dir), int(cred.UID), int(cred.GID))
		}
	}

	stopSignal, err := containerspec.ParseSignal(spec.StopSignal)
	if err != nil {
		return 0, false, err
	}

	// The container's output goes through pipes rather than straight to
	// this process's own streams. That is not plumbing for its own sake:
	// a container's stdout and stderr have to be *openable*, because
	// images reopen them through /dev/stdout and /dev/stderr -- nginx's
	// error log is a symlink to /dev/stderr, and a container runtime
	// gives it a pipe, which /proc/self/fd/2 can open again. What systemd
	// hands a service is a journal socket, which cannot: the open fails
	// with ENXIO ("No such device or address") and nginx exits before it
	// has served anything. A pipe here makes the ordinary image work and
	// keeps the output where the unit sends it.
	//
	// Each pipe is copied to the console and to broker, which keeps the
	// two streams apart for a host that asks for them.
	//
	// A container on a terminal (spec.Tty) writes both to the terminal
	// instead, as one stream, which is copied the same way; its pipes are
	// made all the same and simply never written to.
	stdout, stdoutDone, err := outputPipe(io.MultiWriter(broker.Stdout(), os.Stdout))
	if err != nil {
		return 0, false, err
	}
	defer stdout.Close()
	stderr, stderrDone, err := outputPipe(io.MultiWriter(broker.Stderr(), os.Stderr))
	if err != nil {
		return 0, false, err
	}
	defer stderr.Close()

	// The command's Path is the path *inside* the container: everything
	// in this Cmd resolves after the pivot into its root that precedes the
	// exec, so nothing here may be resolved against the guest's own
	// filesystem.
	// That is also why the program was looked up by hand above rather
	// than left to exec.Command, which would have searched the guest's
	// PATH and refused a binary that only exists in the container.
	cmd := &exec.Cmd{
		Path:   program,
		Args:   argv,
		Env:    environ(spec, cred),
		Dir:    spec.WorkingDir,
		Stdout: stdout,
		Stderr: stderr,
		SysProcAttr: &syscall.SysProcAttr{
			// Its own process group, so a signal meant for the container
			// reaches the whole tree it started rather than just its
			// first process.
			Setpgid: true,
		},
	}
	if cred.Set {
		cmd.SysProcAttr.Credential = &syscall.Credential{Uid: cred.UID, Gid: cred.GID}
	}
	// Not a chroot: a mount namespace of the container's own with the
	// wrapped filesystem pivoted to as its root (see enter.go), which
	// everything above still describes from inside.
	entered := func() error { return nil }
	if spec.Rootfs != "" && spec.Rootfs != "/" {
		entered, err = enterCommand(cmd, spec.Rootfs, log.Printf)
		if err != nil {
			return 0, false, err
		}
	}
	// Its stdin: /dev/null, or the host's when the spec opens it, and a
	// terminal in place of all three when the spec asks for one.
	wired, err := wireStdio(cmd, spec, cred, broker, io.MultiWriter(broker.Stdout(), os.Stdout))
	if err != nil {
		return 0, false, err
	}
	defer wired.close()

	log.Printf("running %s from %s", strings.Join(argv, " "), spec.Image)
	if err := cmd.Start(); err != nil {
		wired.afterStart()
		return 0, false, fmt.Errorf("starting %s in %s: %w", program, spec.Rootfs, err)
	}
	if err := entered(); err != nil {
		// The helper never reached the workload's exec, and has exited
		// saying why.
		stdout.Close()
		stderr.Close()
		wired.afterStart()
		_ = cmd.Wait()
		return 0, false, fmt.Errorf("starting %s in %s: %w", program, spec.Rootfs, err)
	}
	// This process's own copy of each write end, so that the pipes reach
	// end-of-file when the container and everything it started have let
	// go of them -- and the same for the terminal and the stdin pipe.
	stdout.Close()
	stderr.Close()
	wired.afterStart()

	// Shutting the VM down asks the container to stop the way a container
	// runtime would: the image's own STOPSIGNAL to the process group,
	// and then whatever the init system's own timeout allows before it
	// kills everything anyway. Nothing is escalated here -- systemd and
	// OpenRC both SIGKILL a service that outstays its stop timeout, and
	// duplicating that logic would only make this program disagree with
	// them about how long a container gets.
	var asked atomic.Bool
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(signals)
	go func() {
		for sig := range signals {
			asked.Store(true)
			log.Printf("received %s, sending %s to the container", sig, spec.StopSignalName())
			_ = syscall.Kill(-cmd.Process.Pid, syscall.Signal(stopSignal))
		}
	}()

	err = cmd.Wait()
	// Whatever the container wrote just before exiting is still in the
	// pipes, and the VM may be about to power off. Waiting for the copies
	// is bounded rather than unconditional: a container that leaves a
	// daemonized child holding its stdout would otherwise keep this
	// process alive forever, having already exited.
	done := []<-chan struct{}{stdoutDone, stderrDone}
	if wired.done != nil {
		done = append(done, wired.done)
	}
	drain(2*time.Second, done...)
	// Nothing reads the container's stdin any more; what the host still
	// sends fails rather than waits.
	wired.close()

	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0, asked.Load(), nil
	case errors.As(err, &exit):
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			// The shell's convention, and the one a container runtime
			// reports: a process killed by signal N exited 128+N.
			return 128 + int(status.Signal()), asked.Load(), nil
		}
		return exit.ExitCode(), asked.Load(), nil
	default:
		return 0, asked.Load(), fmt.Errorf("waiting for the container: %w", err)
	}
}

// outputPipe returns the write end of a pipe for the container to write
// one of its streams to, and a channel closed once everything written to
// it has been copied to dst.
func outputPipe(dst io.Writer) (*os.File, <-chan struct{}, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, nil, fmt.Errorf("making a pipe for the container's output: %w", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer r.Close()
		io.Copy(dst, r)
	}()
	return w, done, nil
}

// drain waits for the output copies to finish, but not for longer than
// the container's last words are worth.
func drain(within time.Duration, done ...<-chan struct{}) {
	deadline := time.After(within)
	for _, ch := range done {
		select {
		case <-ch:
		case <-deadline:
			return
		}
	}
}

// environ is the environment the container's process gets -- see
// containerspec.Spec.Environ, which granule-agent's container exec uses
// too.
func environ(spec containerspec.Spec, cred containerspec.Credential) []string {
	return spec.Environ(cred)
}

// prepare gives the wrapped filesystem the pseudo-filesystems and the
// resolver configuration a container is started with.
//
// Every step is best effort and says so on the console when it fails: a
// container whose image has no /sys is unusual rather than broken, and a
// VM that refuses to start its workload because one mount did not take is
// worse than one that starts it and says what is missing. The container's
// own failure, if the missing mount matters, is the more informative
// message of the two.
func prepare(rootfs string) {
	if rootfs == "" || rootfs == "/" {
		return
	}
	for _, m := range []struct {
		source, target, fstype string
		flags                  uintptr
	}{
		{"proc", "/proc", "proc", 0},
		{"sysfs", "/sys", "sysfs", syscall.MS_RDONLY},
		// Writable, under the read-only sysfs: runc, BuildKit and
		// anything else that makes cgroups of its own looks for the
		// hierarchy here, and finds no cgroup mount at all without it.
		{"cgroup2", "/sys/fs/cgroup", "cgroup2", syscall.MS_NOSUID | syscall.MS_NODEV | syscall.MS_NOEXEC},
		// /dev comes from the guest rather than from a fresh devtmpfs, and
		// recursively, so that /dev/pts and /dev/shm come with it. The
		// container gets the machine's devices because it is the only
		// thing in the machine -- there is nothing here to hide them from.
		{"/dev", "/dev", "", syscall.MS_BIND | syscall.MS_REC},
	} {
		// Resolved inside the image, like everything else here: a /proc
		// the image ships as a symlink to somewhere on the guest would
		// otherwise put the mount there.
		dir, _, err := containerspec.MkdirIn(rootfs, m.target, 0o755)
		if err != nil {
			log.Printf("cannot create %s for the container: %v", m.target, err)
			continue
		}
		target := filepath.Join(rootfs, dir)
		if mounted(target) {
			continue
		}
		if err := syscall.Mount(m.source, target, m.fstype, m.flags, ""); err != nil {
			log.Printf("cannot mount %s on the container's %s: %v", m.source, m.target, err)
		}
	}

	// The guest's own resolver and hosts file, which granule's boot-time
	// network configuration has already written by the time this runs.
	// Copied in rather than bind-mounted so that a container writing to
	// either only changes its own copy, which is what a container runtime
	// arranges too.
	for _, name := range []string{"/etc/resolv.conf", "/etc/hosts", "/etc/hostname"} {
		if err := copyInto(rootfs, name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Printf("cannot give the container %s: %v", name, err)
		}
	}
}

// mountShares mounts the VM's virtio-fs shares inside the container
// (containerspec.Mount), parents first, and returns the guest paths they
// were mounted on. Each destination is resolved inside the wrapped
// filesystem by containerspec.MountTarget, so neither a ".." nor a
// symlink the image planted can put a share anywhere else on the guest.
//
// Unlike prepare's mounts these are not best effort: a share is somebody's
// data, and a container started without it writes what it meant to keep
// into the image's own directory instead, where nobody will look for it.
// An error leaves the VM up for `granule exec` to find out why, as any
// other container that cannot start does.
func mountShares(rootfs string, mounts []containerspec.Mount, mount func(source, target, fstype string, flags uintptr, data string) error) (map[string]bool, error) {
	targets := map[string]bool{}
	for _, m := range containerspec.MountOrder(mounts) {
		target, err := containerspec.MountTarget(rootfs, m, 0o755)
		if err != nil {
			return nil, err
		}
		targets[target] = true
		if mounted(target) {
			continue
		}
		var flags uintptr
		mode := "read-write"
		if m.ReadOnly {
			flags |= syscall.MS_RDONLY
			mode = "read-only"
		}
		if err := mount(m.Tag, target, containerspec.MountFSType, flags, ""); err != nil {
			return nil, fmt.Errorf("mounting the virtio-fs share %q on the container's %s: %w", m.Tag, m.Destination, err)
		}
		log.Printf("mounted the virtio-fs share %q on the container's %s, %s", m.Tag, m.Destination, mode)
	}
	return targets, nil
}

// mounted reports whether anything is already mounted at path, so that a
// restarted container does not stack a second proc on the first.
func mounted(path string) bool {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return false
	}
	return mountedIn(string(data), path)
}

func mountedIn(mountinfo, path string) bool {
	for _, line := range strings.Split(mountinfo, "\n") {
		// mountinfo's fields are: id parent major:minor root mountpoint ...
		fields := strings.Fields(line)
		if len(fields) >= 5 && fields[4] == path {
			return true
		}
	}
	return false
}

func copyInto(rootfs, name string) error {
	data, err := os.ReadFile(name)
	if err != nil {
		return err
	}
	return containerspec.WriteFileIn(rootfs, name, data, 0o644)
}

// lookPath resolves the program a container is asked to run inside the
// wrapped filesystem -- see containerspec.LookPath.
func lookPath(rootfs, name, pathEnv string) (string, error) {
	return containerspec.LookPath(rootfs, name, pathEnv)
}

// executableIn asks whether path in rootfs is something exec can run --
// see containerspec.ExecutableIn.
func executableIn(rootfs, path string) error {
	return containerspec.ExecutableIn(rootfs, path)
}

// powerOff stops the machine the way its own init would, so that
// filesystems are unmounted and the hypervisor sees an orderly shutdown
// -- the same path an ACPI power button press takes (see the guest
// overlay's powerbtn.sh).
//
// The syscall below is the fallback rather than the mechanism: it stops
// the kernel immediately, with whatever is still in the page cache still
// there. It is reached only by a guest with no poweroff command at all,
// where the alternative is a VM that never stops.
func powerOff() {
	for _, path := range []string{"/sbin/poweroff", "/usr/sbin/poweroff", "/bin/poweroff", "/usr/bin/poweroff"} {
		if err := executableIn("/", path); err != nil {
			continue
		}
		if err := exec.Command(path).Run(); err != nil {
			log.Printf("%s failed: %v", path, err)
			continue
		}
		// poweroff returns immediately and the shutdown happens behind
		// it; there is nothing left for this process to do but wait for
		// the machine to stop under it.
		time.Sleep(2 * time.Minute)
		return
	}
	log.Print("this guest has no poweroff command, stopping the machine directly")
	syscall.Sync()
	if err := syscall.Reboot(syscall.LINUX_REBOOT_CMD_POWER_OFF); err != nil {
		log.Printf("cannot power the machine off: %v", err)
	}
}
