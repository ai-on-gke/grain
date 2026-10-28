package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// The wrapped workload runs in a mount namespace of its own, with the
// wrapped filesystem as that namespace's root -- pivoted into, not
// chrooted into. A chroot would give the image's paths the right
// meaning, but it leaves the process with a root that is not the root of
// its mount namespace, and the kernel holds that against it: unshare(2)
// refuses CLONE_NEWUSER to such a process (so rootless BuildKit, podman
// and `unshare -r` cannot start), and runc cannot make a "/" that is not
// a mount point a slave mount. Under pivot_root the container's "/" is a
// mount, and the root of its namespace, as it would be under a container
// runtime.
//
// None of it is isolation, and none of it is meant to be (see the
// package comment): the process keeps every capability root has, no
// seccomp filter and no no_new_privs, and what it mounts in its own
// namespace simply stays out of the guest's.
//
// Go cannot change the mount namespace or the root of a process that is
// already running threads, and os/exec offers chroot but not pivot_root,
// so the step between the fork and the workload's exec is this program
// itself: run starts /proc/self/exe with enterEnv set, in a new mount
// namespace, and enterMain -- which main reaches before anything else --
// pivots, drops to the image's user and execs the workload. The process
// that started is the workload's, so its pid, process group, terminal and
// streams are the ones run set up.

// enterEnv carries an entry to the helper. It is the helper's only
// environment variable: the workload's own is in the entry.
const enterEnv = "GRANULE_CONTAINER_ENTER"

// enterReportFD is the descriptor the helper reports on: a pipe whose
// write end is close-on-exec, so run reads end-of-file the moment the
// workload's exec succeeds and an error line if anything before it
// failed -- what os/exec does for its own fork-and-exec.
const enterReportFD = 3

// entry is what the helper is to start, every path in it a path inside
// the container.
type entry struct {
	Rootfs string   `json:"rootfs"`
	Path   string   `json:"path"`
	Args   []string `json:"args"`
	Env    []string `json:"env"`
	Dir    string   `json:"dir,omitempty"`
	// Credential is the image's user, nil to stay root.
	Credential *syscall.Credential `json:"credential,omitempty"`
}

// enterCommand makes cmd -- already carrying the workload's program,
// arguments, environment, working directory and credential -- start the
// workload through the helper instead, in a new mount namespace rooted at
// rootfs. The returned function, called once cmd has started, waits for
// the helper's report and returns the error that stopped it before the
// workload's exec, if one did, after logging any note it made on the way.
func enterCommand(cmd *exec.Cmd, rootfs string, logf func(string, ...any)) (func() error, error) {
	e := entry{Rootfs: rootfs, Path: cmd.Path, Args: cmd.Args, Env: cmd.Env, Dir: cmd.Dir}
	if cmd.SysProcAttr.Credential != nil {
		c := *cmd.SysProcAttr.Credential
		e.Credential = &c
	}
	data, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("finding this program to start the container through: %w", err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("making a pipe to start the container through: %w", err)
	}
	if len(cmd.ExtraFiles) != 0 {
		r.Close()
		w.Close()
		return nil, errors.New("the container's command already has extra descriptors")
	}

	cmd.Path = self
	cmd.Args = []string{"granule-container", "enter", rootfs}
	cmd.Env = []string{enterEnv + "=" + string(data)}
	// The helper starts in the guest's root and changes directory itself,
	// inside the container, once it is there.
	cmd.Dir = ""
	// And it starts as root, which pivot_root needs; it drops to the
	// image's user after, as the kernel's chroot-then-setuid did.
	cmd.SysProcAttr.Credential = nil
	cmd.SysProcAttr.Chroot = ""
	cmd.SysProcAttr.Unshareflags |= syscall.CLONE_NEWNS
	cmd.ExtraFiles = []*os.File{w}

	return func() error {
		w.Close()
		defer r.Close()
		var failure string
		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			line := scanner.Text()
			if msg, ok := strings.CutPrefix(line, "error: "); ok {
				failure = msg
				continue
			}
			logf("%s", line)
		}
		if failure != "" {
			return errors.New(failure)
		}
		return nil
	}, nil
}

// enterMain is the helper: it never returns. Everything it says goes to
// the report pipe rather than to stderr, which is already the container's.
func enterMain() {
	report := os.NewFile(enterReportFD, "report")
	syscall.CloseOnExec(enterReportFD)
	fail := func(err error) {
		fmt.Fprintf(report, "error: %v\n", err)
		os.Exit(127)
	}
	var e entry
	if err := json.Unmarshal([]byte(os.Getenv(enterEnv)), &e); err != nil {
		fail(fmt.Errorf("reading what to start: %w", err))
	}
	if err := pivotInto(e.Rootfs, report); err != nil {
		fail(err)
	}
	if err := becomeAndExec(e); err != nil {
		fail(err)
	}
}

// pivotInto makes rootfs the root of this process's mount namespace,
// which must already be its own: rootfs is bound onto itself, together
// with everything prepare and mountShares mounted under it, so that it is
// a mount point, then pivoted to, and the guest's root it replaces is
// detached, so nothing under the container's "/" leads back out of it.
func pivotInto(rootfs string, notes io.Writer) error {
	// Already done for a namespace os/exec unshared, but said here
	// rather than relied on: a mount made in this namespace must not
	// propagate back to the guest's, nor the bind and detach below.
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("making the container's mounts private: %w", err)
	}
	if err := unix.Mount(rootfs, rootfs, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		return fmt.Errorf("binding %s onto itself: %w", rootfs, err)
	}
	if err := unix.Chdir(rootfs); err != nil {
		return fmt.Errorf("entering %s: %w", rootfs, err)
	}
	// pivot_root(".", ".") stacks the old root on top of the new one at
	// "/", where unmounting it uncovers the container's own; no directory
	// inside the image has to be borrowed to hold it in between.
	if err := unix.PivotRoot(".", "."); err != nil {
		if !errors.Is(err, unix.EINVAL) {
			return fmt.Errorf("pivoting into %s: %w", rootfs, err)
		}
		// A guest whose own root is the initramfs's rootfs, which
		// cannot be pivoted away from. Moving the container's root over
		// "/" still makes it a mount point (which is what runc needs),
		// but the chroot that follows is what user namespaces are
		// refused under.
		fmt.Fprintf(notes, "cannot pivot_root into %s (%v), so the container is chrooted there instead: it cannot create user namespaces\n", rootfs, err)
		if err := unix.Mount(rootfs, "/", "", unix.MS_MOVE, ""); err != nil {
			return fmt.Errorf("moving %s over the guest's root: %w", rootfs, err)
		}
		if err := unix.Chroot("."); err != nil {
			return fmt.Errorf("chrooting into %s: %w", rootfs, err)
		}
		return unix.Chdir("/")
	}
	if err := unix.Unmount(".", unix.MNT_DETACH); err != nil {
		return fmt.Errorf("detaching the guest's root from the container: %w", err)
	}
	return unix.Chdir("/")
}

// becomeAndExec drops to the entry's user, enters its working directory
// and execs its program, in the order os/exec does them for a Cmd with a
// Credential and a Dir: the working directory is entered as the user, so
// one the user cannot enter fails here as it did there.
func becomeAndExec(e entry) error {
	if c := e.Credential; c != nil {
		// No supplementary groups, as os/exec gives a Credential that
		// names none: root's are dropped rather than inherited.
		if err := syscall.Setgroups(nil); err != nil {
			return fmt.Errorf("dropping supplementary groups: %w", err)
		}
		if err := syscall.Setgid(int(c.Gid)); err != nil {
			return fmt.Errorf("setgid %d: %w", c.Gid, err)
		}
		if err := syscall.Setuid(int(c.Uid)); err != nil {
			return fmt.Errorf("setuid %d: %w", c.Uid, err)
		}
	}
	if e.Dir != "" {
		if err := syscall.Chdir(e.Dir); err != nil {
			return fmt.Errorf("chdir %s: %w", e.Dir, err)
		}
	}
	if err := syscall.Exec(e.Path, e.Args, e.Env); err != nil {
		return fmt.Errorf("exec %s: %w", e.Path, err)
	}
	return nil
}
