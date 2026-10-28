package main

import (
	"errors"
	"fmt"
	"log"
	"os"

	"golang.org/x/sys/unix"

	"github.com/bwsalmon/granule/pkg/workloadio"
)

// serveStreams offers the container's stdout and stderr to the host on
// workloadio.Port, apart from the console (see pkg/workloadio): every
// connection from the host is attached to broker in turn. It returns
// once the port is listening, and an error if it cannot be -- a guest
// with no vsock driver, say -- which costs the host the separate streams
// and nothing else, since the console still has the output.
func serveStreams(broker *workloadio.Broker) error {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("opening a vsock socket for the container's output (is the virtio-vsock driver loaded?): %w", err)
	}
	if err := unix.Bind(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: workloadio.Port}); err != nil {
		unix.Close(fd)
		return fmt.Errorf("binding vsock port %d for the container's output: %w", workloadio.Port, err)
	}
	if err := unix.Listen(fd, 4); err != nil {
		unix.Close(fd)
		return fmt.Errorf("listening on vsock port %d for the container's output: %w", workloadio.Port, err)
	}

	// The descriptor is never closed: it lives exactly as long as this
	// process, whose exit is what closes it. See granule-agent's accept
	// for why blocking descriptors, and why that is all right here.
	go func() {
		for {
			nfd, sa, err := unix.Accept4(fd, unix.SOCK_CLOEXEC)
			if err != nil {
				if errors.Is(err, unix.EINTR) || errors.Is(err, unix.ECONNABORTED) {
					continue
				}
				log.Printf("accepting a connection for the container's output: %v", err)
				return
			}
			// Only the host: a process inside the guest has the console
			// already, and no business taking the host's place on the
			// one connection the streams are sent to.
			if vm, ok := sa.(*unix.SockaddrVM); !ok || vm.CID != unix.VMADDR_CID_HOST {
				unix.Close(nfd)
				continue
			}
			go func() {
				if err := broker.Serve(os.NewFile(uintptr(nfd), "vsock")); err != nil {
					log.Printf("the host's connection for the container's output: %v", err)
				}
			}()
		}
	}()
	return nil
}

// streamsRequested reports whether the host booted this guest asking for
// the container's streams (workloadio.CmdlineParam), and so whether a
// finished container's output is worth waiting for it to collect.
func streamsRequested() bool {
	data, err := os.ReadFile("/proc/cmdline")
	return err == nil && workloadio.Requested(string(data))
}
