package netshim

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"testing"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// ownNetnsEnv marks a test binary that is already running in a network
// namespace of its own, so the re-exec below happens exactly once. It is
// internal to this package's tests: setting it by hand only tells the
// tests a lie about where they are running.
const ownNetnsEnv = "GRANULE_NETSHIM_TEST_OWN_NETNS"

// netnsSetupErr is why this run is *not* in a namespace of its own, when
// it tried to get one and could not. It is read by requireRoot, which
// refuses to touch the kernel outside a private namespace.
var netnsSetupErr error

// TestMain gives the kernel-touching tests in this package a network
// namespace to themselves.
//
// They create real veths, taps, bridges and tc filters. Doing that in the
// namespace they inherit -- a CI runner's, or a developer's own -- means
// sharing it with everything else that reacts to an interface appearing:
// systemd-networkd, udev, NetworkManager and docker all watch for new
// links and will address, rename or enslave one out from under a test.
// It also means a test that dies without running its cleanup leaves
// interfaces behind on a machine somebody else is using, and that every
// name has to be made unique by hand in case two runs overlap.
//
// A namespace of our own removes all three problems at once: nothing else
// is in it, the names can be fixed and readable, and the whole namespace
// (and every interface in it) is destroyed by the kernel when the process
// holding it exits, cleanup or no cleanup.
//
// It is taken by re-executing this same test binary as a child with
// CLONE_NEWNET, rather than by calling unshare(2) here. A network
// namespace in Linux is a property of a *thread*, and the Go runtime
// moves goroutines between threads freely and keeps threads it created
// before now -- so unsharing on this one would leave the tests running on
// whichever thread the scheduler picked, some in the new namespace and
// some in the old, which is a worse kind of flakiness than the one this
// is fixing. A forked child starts with one thread in the new namespace
// and every thread it goes on to create inherits it, so there is no
// per-thread discipline to get wrong.
func TestMain(m *testing.M) {
	if os.Getenv(ownNetnsEnv) == "" && os.Geteuid() == 0 {
		code, err := runInOwnNetns()
		if err == nil {
			os.Exit(code)
		}
		// Fall through and run here instead. requireRoot turns this
		// into a skip -- or, under GRANULE_NETNS_TESTS=required, into a
		// failure naming this error -- rather than letting tests
		// create fixed-name interfaces in a namespace they share.
		netnsSetupErr = err
		fmt.Fprintf(os.Stderr, "netshim: %v\n", err)
	}

	if inOwnNetns() {
		// A fresh namespace has a loopback interface, but it is down.
		// Nothing here needs it; bringing it up only keeps a test that
		// one day talks to 127.0.0.1 from failing for a reason that has
		// nothing to do with it.
		if lo, err := netlink.LinkByName("lo"); err != nil {
			fmt.Fprintf(os.Stderr, "netshim: looking up loopback in the test namespace: %v\n", err)
		} else if err := netlink.LinkSetUp(lo); err != nil {
			fmt.Fprintf(os.Stderr, "netshim: bringing up loopback in the test namespace: %v\n", err)
		}
	}

	os.Exit(m.Run())
}

// inOwnNetns reports whether this process is the child TestMain re-execed
// into a private network namespace, and so may create interfaces at fixed
// names without regard for anything else on the machine.
func inOwnNetns() bool { return os.Getenv(ownNetnsEnv) != "" }

// runInOwnNetns re-runs this test binary, with the same arguments, as a
// child process in a new network namespace, and returns the status it
// exited with. Its output is this process's output: the child writes
// straight to our stdout and stderr, so `go test` sees exactly what it
// would have seen from a binary that did not re-exec at all.
func runInOwnNetns() (int, error) {
	// Pdeathsig below is delivered when the thread that forked exits,
	// not the process -- so the forking thread has to stay alive for as
	// long as we intend the child to. Locking it to this goroutine,
	// which does nothing else until the child is reaped, is what makes
	// that true.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// The binary's own path from the kernel rather than os.Args[0],
	// which is whatever `go test` chose to invoke it as and need not
	// resolve from here. /proc/self/exe would do as well, except that
	// the child would then show up in ps as "exe".
	exe, err := os.Executable()
	if err != nil {
		exe = "/proc/self/exe"
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Args[0] = os.Args[0]
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), ownNetnsEnv+"=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: unix.CLONE_NEWNET,
		// If this process is killed -- `go test` giving up on its
		// timeout, or a developer's ^C -- the child must not be left
		// running the tests on into an empty terminal.
		Pdeathsig: unix.SIGKILL,
	}
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("re-executing the tests in a new network namespace: %w", err)
	}

	// The signals that mean something to a test binary are all sent to
	// the process `go test` started, which is this one; the tests are in
	// the child. SIGQUIT is the load-bearing one: it is how `go test`
	// asks a binary that has run over its timeout for a goroutine dump,
	// and the useful stacks are the child's.
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, unix.SIGINT, unix.SIGTERM, unix.SIGQUIT)
	relayed := make(chan struct{})
	go func() {
		defer close(relayed)
		for sig := range sigs {
			cmd.Process.Signal(sig)
		}
	}()

	err = cmd.Wait()
	signal.Stop(sigs)
	close(sigs)
	<-relayed

	// Past this point the tests have already run, so every path has to
	// produce an exit status rather than an error: returning one would
	// have TestMain run them a second time, here, in the namespace this
	// whole function exists to keep them out of.
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return 0, nil
	case errors.As(err, &exitErr) && exitErr.ExitCode() >= 0:
		return exitErr.ExitCode(), nil
	default:
		// Killed by a signal, which has no exit code of its own, or a
		// wait that failed outright. The child has already said whatever
		// it had to say on the shared stderr; this only has to be
		// non-zero.
		fmt.Fprintf(os.Stderr, "netshim: the tests in the new network namespace ended abnormally: %v\n", err)
		return 1, nil
	}
}
