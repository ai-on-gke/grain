package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/bwsalmon/granule/pkg/config"
	"github.com/bwsalmon/granule/pkg/workloadio"
)

// guestInput is a wrapped container's stdin and terminal as the guest's
// broker hands them over.
type guestInput struct {
	mu     sync.Mutex
	data   string
	closed bool
	sizes  [][2]uint16
}

func (g *guestInput) Write(p []byte) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.data += string(p)
	return len(p), nil
}

func (g *guestInput) CloseWrite() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closed = true
	return nil
}

func (g *guestInput) Resize(cols, rows uint16) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sizes = append(g.sizes, [2]uint16{cols, rows})
}

// fakeGuest answers cloud-hypervisor's hybrid vsock handshake on socket
// and serves broker on it, the way granule-container does in a VM.
func fakeGuest(t *testing.T, socket string, broker *workloadio.Broker) {
	t.Helper()
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				line, err := bufio.NewReader(io.LimitReader(conn, 64)).ReadString('\n')
				if err != nil || line != fmt.Sprintf("CONNECT %d\n", workloadio.Port) {
					conn.Close()
					return
				}
				fmt.Fprintf(conn, "OK %d\n", workloadio.Port)
				broker.Serve(conn)
			}()
		}
	}()
}

// The shim reads GRANULE_WORKLOAD_STDIN's FIFO to its writer's close and
// sends it on, the container reading end-of-file at that close, and
// reads GRANULE_WORKLOAD_RESIZE's FIFO each time a writer opens it.
func TestForwardWorkloadInput(t *testing.T) {
	dir := t.TempDir()
	stdinPath := filepath.Join(dir, "stdin")
	resizePath := filepath.Join(dir, "resize")
	for _, p := range []string{stdinPath, resizePath} {
		if err := syscall.Mkfifo(p, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	socket := filepath.Join(dir, "vsock.sock")
	broker := workloadio.NewBroker(0)
	broker.OfferInput(true, true)
	in := &guestInput{}
	broker.SetInput(in)
	fakeGuest(t, socket, broker)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	vmExited := make(chan struct{})
	done := forwardWorkloadOutput(ctx, vmExited, config.Config{
		VsockSocket:    socket,
		WorkloadStdin:  stdinPath,
		WorkloadTTY:    true,
		WorkloadResize: resizePath,
	})

	writeFIFO := func(path, s string) {
		t.Helper()
		f, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString(s); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	writeFIFO(resizePath, "80 24\n")
	writeFIFO(stdinPath, "echo hi\n")
	writeFIFO(resizePath, "bogus\n100x30\n")

	deadline := time.Now().Add(5 * time.Second)
	for {
		in.mu.Lock()
		data, closed, sizes := in.data, in.closed, append([][2]uint16(nil), in.sizes...)
		in.mu.Unlock()
		if data == "echo hi\n" && closed && len(sizes) > 0 && sizes[len(sizes)-1] == [2]uint16{100, 30} {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("guest has stdin %q (closed %v), sizes %v", data, closed, sizes)
		}
		time.Sleep(10 * time.Millisecond)
	}

	close(vmExited)
	broker.Close(0)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("forwarding kept going after the VM stopped")
	}
}
