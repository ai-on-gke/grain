// Command granule-agent runs inside the guest and answers "granule exec".
//
// It listens on a virtio-vsock port and hands each connection to
// pkg/agent, which runs one command and streams it back
// (pkg/execwire). This is what replaced running an sshd in the
// guest purely so that granule had a way in: the guest no longer needs a
// network service, a host keypair, an authorized key or an account to
// log into, and "granule exec" no longer needs the guest to have finished
// bringing up a control network before it can work.
//
// vsock rather than a socket on the control link, because the point is
// to stop depending on guest networking at all. A vsock connection is
// carried by the virtio device itself: it works before the guest has an
// address, while its network is misconfigured, and with no NIC attached
// at all -- states in which the SSH transport left a running, healthy
// guest unreachable and unexplained. Under cloud-hypervisor the host
// side is a unix socket in the VM's own container rather than anything
// on a network, which is also why this needs no authentication of its
// own; pkg/execwire's package comment sets out that reasoning.
//
// It is deliberately not a systemd socket-activated service. Activation
// would put systemd's socket handling between the hypervisor and this,
// and the failure it protects against -- an agent that died -- is one
// Restart=always already covers, without the guest needing a systemd new
// enough to have vsock socket units.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/bwsalmon/granule/pkg/agent"
	"github.com/bwsalmon/granule/pkg/execwire"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("granule-agent: ")

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context) error {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		// The most likely cause by far, and the one worth naming: the
		// guest kernel has no virtio-vsock driver loaded, so the address
		// family does not exist. Debian ships it as a module
		// (vmw_vsock_virtio_transport) that something has to load; the
		// unit that starts this does.
		return fmt.Errorf("opening a vsock socket (is the virtio-vsock driver loaded?): %w", err)
	}
	defer unix.Close(fd)

	// VMADDR_CID_ANY: the guest does not need to know its own context
	// id, and being told one it disagrees with is a way to bind nothing.
	if err := unix.Bind(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: execwire.Port}); err != nil {
		return fmt.Errorf("binding vsock port %d: %w", execwire.Port, err)
	}
	if err := unix.Listen(fd, 16); err != nil {
		return fmt.Errorf("listening on vsock port %d: %w", execwire.Port, err)
	}

	// Stopping has to be immediate, and it cannot be done by unblocking
	// the loop below.
	//
	// accept(2) on a blocking descriptor is not interrupted by closing or
	// shutting down the socket -- the thread already inside the syscall
	// stays there -- so a handler that only cancelled a context would
	// leave this process alive with nothing watching that context.
	// systemd would then wait out TimeoutStopSec and SIGKILL it, which
	// on the way to poweroff is a guest that takes 90 seconds to shut
	// down. `granule guest build` gives up after 60 and reports a
	// guest whose filesystem may be inconsistent, which is how this was
	// found.
	//
	// Exiting outright is the whole of the correct behaviour here: this
	// process holds no state, has nothing buffered, and owns nothing a
	// later boot reads. A session still running is one whose client is
	// about to lose the VM anyway.
	go func() {
		<-ctx.Done()
		_ = unix.Close(fd)
		os.Exit(0)
	}()

	log.Printf("listening on vsock port %d", execwire.Port)
	for {
		conn, err := accept(fd)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, unix.EINTR) || errors.Is(err, unix.ECONNABORTED) {
				continue
			}
			return fmt.Errorf("accepting: %w", err)
		}
		go func() {
			defer conn.Close()

			// One context per connection, not the process's own. The
			// command a session starts is bounded by the connection that
			// asked for it: when that goes -- because the client
			// finished, timed out, or vanished -- this cancel runs and
			// pkg/agent ends the command rather than leaving it
			// running in a guest that may live for days. Handing every
			// session the process context, as this did, meant nothing
			// ever cancelled one.
			cctx, cancel := context.WithCancel(ctx)
			defer cancel()

			if err := agent.Serve(cctx, conn); err != nil {
				// One session failing says nothing about the next, so
				// this is reported and dropped rather than fatal. It
				// lands on the guest's console, which is where a VM's
				// own boot is read from.
				log.Printf("session: %v", err)
			}
		}()
	}
}

// accept takes the next connection as an *os.File.
//
// The descriptors are left blocking rather than handed to Go's network
// poller, because the poller cannot take them: net.FileConn identifies a
// socket by its address family and has no case for AF_VSOCK, so it
// refuses one.
//
// That costs an OS thread per session while a command runs, which for
// the handful of concurrent execs a guest ever sees is not worth
// engineering around -- and, less obviously, it costs the ability to
// interrupt this call at all, which is why run's shutdown path exits the
// process rather than trying to unblock it.
func accept(fd int) (*os.File, error) {
	for {
		nfd, sa, err := unix.Accept4(fd, unix.SOCK_CLOEXEC)
		if err != nil {
			return nil, err
		}
		if !fromHost(sa) {
			log.Printf("refused a connection from %s: only the host may run commands here", describePeer(sa))
			_ = unix.Close(nfd)
			continue
		}
		return os.NewFile(uintptr(nfd), "vsock"), nil
	}
}

// fromHost reports whether a connection's peer is the hypervisor's side
// of the vsock device, the only peer this agent serves.
//
// The trust argument in pkg/execwire -- reaching this port already means
// having the run of the VM's container -- holds only for connections the
// host opens. Binding VMADDR_CID_ANY also accepts connections made from
// inside the guest itself, over the loopback transport (CID 1, or the
// guest's own CID) whenever vsock_loopback is loaded, and every session
// defaults to root. Without this check any unprivileged process in the
// guest could connect to its own agent and run as root.
func fromHost(sa unix.Sockaddr) bool {
	vm, ok := sa.(*unix.SockaddrVM)
	return ok && vm.CID == unix.VMADDR_CID_HOST
}

func describePeer(sa unix.Sockaddr) string {
	if vm, ok := sa.(*unix.SockaddrVM); ok {
		return fmt.Sprintf("vsock CID %d port %d", vm.CID, vm.Port)
	}
	return fmt.Sprintf("%T", sa)
}
