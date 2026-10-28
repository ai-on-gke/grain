package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"golang.org/x/sys/unix"

	"github.com/bwsalmon/granule/pkg/workloadio"
)

// exitReporter tells the host that the container has started and, once
// it has, how it exited (see pkg/workloadio's "Exit status"), so that the
// host's granule-shim can exit with the workload's status rather than
// cloud-hypervisor's.
//
// Every report is best effort: a host that is not listening -- an older
// granule-shim, or no host-side shim at all -- costs it the status and
// nothing else, and refuses the connection at once rather than holding
// the machine up.
type exitReporter struct {
	dial    func() (io.ReadWriteCloser, error)
	started chan struct{} // closed once the start report is done with
}

func newExitReporter(dial func() (io.ReadWriteCloser, error)) *exitReporter {
	return &exitReporter{dial: dial, started: make(chan struct{})}
}

// start reports the container's start, in the background: the workload
// is not held up for it.
func (r *exitReporter) start() {
	go func() {
		defer close(r.started)
		if err := r.send(workloadio.Report{Event: workloadio.EventStarted}); err != nil {
			log.Printf("the host will not be told the container's status: %v", err)
		}
	}()
}

// exit reports the container's exit status and returns once the host has
// recorded it, or has failed to. It goes after the start report, which
// would otherwise be able to arrive second and clear it.
func (r *exitReporter) exit(code int) {
	select {
	case <-r.started:
	case <-time.After(2 * workloadio.ReportTimeout):
	}
	if err := r.send(workloadio.Report{Event: workloadio.EventExited, ExitCode: code}); err != nil {
		log.Printf("cannot tell the host the container's exit status: %v", err)
	}
}

func (r *exitReporter) send(rep workloadio.Report) error {
	conn, err := r.dial()
	if err != nil {
		return err
	}
	defer conn.Close()
	return workloadio.SendReport(conn, rep)
}

// dialHostExit connects to the host's workloadio.ExitPort. The socket is
// blocking, like serveStreams' -- an os.File over a vsock descriptor has
// no deadlines -- so its timeouts are the kernel's: the connect gives up
// after workloadio.ReportTimeout, and so does any one read or write.
func dialHostExit() (io.ReadWriteCloser, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("opening a vsock socket to report the container's status (is the virtio-vsock driver loaded?): %w", err)
	}
	tv := unix.NsecToTimeval(workloadio.ReportTimeout.Nanoseconds())
	for _, opt := range []int{unix.SO_SNDTIMEO, unix.SO_RCVTIMEO} {
		if err := unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, opt, &tv); err != nil {
			unix.Close(fd)
			return nil, fmt.Errorf("setting a timeout on the vsock socket: %w", err)
		}
	}
	// The connect's own timeout, which SO_SNDTIMEO does not cover.
	if err := unix.SetsockoptTimeval(fd, unix.AF_VSOCK, unix.SO_VM_SOCKETS_CONNECT_TIMEOUT, &tv); err != nil {
		log.Printf("cannot bound the vsock connect, relying on the kernel's default: %v", err)
	}
	if err := unix.Connect(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_HOST, Port: workloadio.ExitPort}); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("connecting to vsock port %d on the host (is anything there listening?): %w", workloadio.ExitPort, err)
	}
	return os.NewFile(uintptr(fd), "vsock"), nil
}
