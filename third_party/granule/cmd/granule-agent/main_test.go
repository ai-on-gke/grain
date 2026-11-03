package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestGranuleAgentRun(t *testing.T) {
	// Where vsock works (a guest, or a Hyper-V host such as a GitHub-hosted
	// runner) run listens, and its stop is a call to exit(0) rather than a
	// return: the accept it is blocked in never comes back. Elsewhere it
	// fails at the socket and says why.
	exited := make(chan int, 1)
	exit = func(code int) { exited <- code }

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	errc := make(chan error, 1)
	go func() { errc <- run(ctx) }()
	select {
	case err := <-errc:
		if err != nil {
			if !strings.Contains(err.Error(), "virtio-vsock") && !strings.Contains(err.Error(), "vsock") {
				t.Errorf("run(ctx) error = %v, want vsock error description", err)
			}
			return
		}
		// It listened and returned anyway; the stop still has to exit.
		select {
		case code := <-exited:
			if code != 0 {
				t.Errorf("exit(%d) on stop, want exit(0)", code)
			}
		case <-time.After(10 * time.Second):
			t.Error("run returned nil without exiting")
		}
	case code := <-exited:
		if code != 0 {
			t.Errorf("exit(%d) on stop, want exit(0)", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run neither failed nor stopped within 10s of a 100ms context")
	}
}

func TestAcceptInvalidFD(t *testing.T) {
	_, err := accept(-1)
	if err == nil {
		t.Error("accept(-1) returned nil error, want error")
	}
}

func TestFromHost(t *testing.T) {
	for _, tc := range []struct {
		name string
		sa   unix.Sockaddr
		want bool
	}{
		{"the hypervisor", &unix.SockaddrVM{CID: unix.VMADDR_CID_HOST, Port: 1234}, true},
		{"guest loopback", &unix.SockaddrVM{CID: unix.VMADDR_CID_LOCAL, Port: 1234}, false},
		{"the guest's own CID", &unix.SockaddrVM{CID: 3, Port: 1234}, false},
		{"the hypervisor reserved CID", &unix.SockaddrVM{CID: unix.VMADDR_CID_HYPERVISOR, Port: 1234}, false},
		{"no address", nil, false},
		{"not vsock", &unix.SockaddrUnix{Name: "/tmp/x"}, false},
	} {
		if got := fromHost(tc.sa); got != tc.want {
			t.Errorf("fromHost(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}
}
