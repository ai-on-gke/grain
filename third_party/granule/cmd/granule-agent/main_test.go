package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestGranuleAgentRun(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := run(ctx)
	// In an environment without vsock, run() returns an error describing the vsock socket failure.
	if err != nil {
		if !strings.Contains(err.Error(), "virtio-vsock") && !strings.Contains(err.Error(), "vsock") {
			t.Errorf("run(ctx) error = %v, want vsock error description", err)
		}
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
