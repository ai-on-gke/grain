package main

import (
	"bytes"
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwsalmon/granule/pkg/config"
)

// newFakeAPIServer starts an HTTP server on a unix socket and points
// CHV_API_SOCKET at it, so runResize talks to it the way it would talk
// to the cloud-hypervisor in this container.
func newFakeAPIServer(t *testing.T, handler http.Handler) {
	t.Helper()
	socketPath := filepath.Join(t.TempDir(), "api.sock")
	l, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listening on %s: %v", socketPath, err)
	}
	srv := httptest.NewUnstartedServer(handler)
	srv.Listener = l
	srv.Start()
	t.Cleanup(srv.Close)
	t.Setenv("CHV_API_SOCKET", socketPath)
}

// captureLog collects what runResize logs, which is the whole of what a
// successful resize tells the person who ran it.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &buf
}

// A resize to the size the guest already has is what a script that pins
// a VM to a fixed size does every run after the first. cloud-hypervisor
// answers the memory half of it with a 500, so the request is not sent
// at all.
func TestRunResize_SkipsWhatTheGuestAlreadyHas(t *testing.T) {
	var paths []string
	newFakeAPIServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		io.WriteString(w, `{"config":{"cpus":{"boot_vcpus":2,"max_vcpus":4},"memory":{"size":268435456,"hotplug_size":805306368,"hotplugged_size":268435456}},"state":"Running","memory_actual_size":536870912}`)
	}))
	logs := captureLog(t)

	if err := runResize([]string{"-memory-mb=512", "-cpus=2"}); err != nil {
		t.Fatalf("runResize() error = %v", err)
	}
	for _, p := range paths {
		if p == "/api/v1/vm.resize" {
			t.Error("vm.resize was called for sizes the guest already has")
		}
	}
	for _, want := range []string{"already at 512 MiB", "already at 2 vCPU(s)"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log = %q, want it to mention %q", logs.String(), want)
		}
	}
}

func TestRunResize_ResizesWhenTheGuestIsElsewhere(t *testing.T) {
	var bodies []string
	newFakeAPIServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/vm.resize" {
			body, _ := io.ReadAll(r.Body)
			bodies = append(bodies, string(body))
			w.WriteHeader(http.StatusNoContent)
			return
		}
		io.WriteString(w, `{"config":{"cpus":{"boot_vcpus":1,"max_vcpus":4},"memory":{"size":268435456,"hotplug_size":805306368}},"state":"Running","memory_actual_size":268435456}`)
	}))
	logs := captureLog(t)

	if err := runResize([]string{"-memory-mb=512", "-cpus=2"}); err != nil {
		t.Fatalf("runResize() error = %v", err)
	}
	want := []string{`{"desired_ram":536870912}`, `{"desired_vcpus":2}`}
	if len(bodies) != len(want) {
		t.Fatalf("resize requests = %v, want %v", bodies, want)
	}
	for i, w := range want {
		if bodies[i] != w {
			t.Errorf("resize request %d = %s, want %s", i, bodies[i], w)
		}
	}
	if !strings.Contains(logs.String(), "requested resize to 512 MiB") {
		t.Errorf("log = %q, want it to mention the memory resize", logs.String())
	}
}

// The current size has to be read before anything is decided, so an
// unreachable API is an error rather than a resize sent blind.
func TestRunResize_FailsWhenTheAPIIsUnreachable(t *testing.T) {
	t.Setenv("CHV_API_SOCKET", filepath.Join(t.TempDir(), "no-such.sock"))
	captureLog(t)

	if err := runResize([]string{"-memory-mb=512"}); err == nil {
		t.Fatal("expected an error with no cloud-hypervisor listening, got nil")
	}
}

// -file names a path inside the guest, checked in a session that starts
// in the account's home directory: a relative one would quietly mean a
// different file per account, and the only symptom would be a VM that
// never reports ready. Refused here, where the flag can be named.
func TestRunReady_RejectsBadFlags(t *testing.T) {
	// Nothing is dialled: both of these are refused before the socket
	// matters, which is the point of checking them out here at all.
	for _, args := range [][]string{
		{"-timeout=-1s"},
		{"-file=run/kontur/ready"},
	} {
		if err := runReady(args); err == nil {
			t.Errorf("runReady(%v) = nil, want an error", args)
		}
	}
}

func TestRunResize_RejectsBadFlags(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"-memory-mb=-1"},
		{"-cpus=-2"},
	} {
		if err := runResize(args); err == nil {
			t.Errorf("runResize(%v) = nil, want an error", args)
		}
	}
}

func TestRunSnapshot_CapturesSnapshotAndOverlay(t *testing.T) {
	var paths []string
	newFakeAPIServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	}))
	logs := captureLog(t)

	destDir := filepath.Join(t.TempDir(), "snapshot")
	overlayDir := t.TempDir()
	overlayPath := filepath.Join(overlayDir, "disk.qcow2")
	if err := os.WriteFile(overlayPath, []byte("fake-overlay-data"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := runSnapshot([]string{"-path=" + destDir, "-overlay=" + overlayPath, "-resume=true"}); err != nil {
		t.Fatalf("runSnapshot() error = %v", err)
	}

	wantPaths := []string{"/api/v1/vmm.ping", "/api/v1/vm.pause", "/api/v1/vm.snapshot", "/api/v1/vm.resume"}
	if len(paths) != len(wantPaths) {
		t.Fatalf("API calls = %v, want %v", paths, wantPaths)
	}
	for i, want := range wantPaths {
		if paths[i] != want {
			t.Errorf("API call %d = %s, want %s", i, paths[i], want)
		}
	}

	// Verify overlay was copied into destDir
	savedOverlay := filepath.Join(destDir, "granule-disk-overlay.qcow2")
	data, err := os.ReadFile(savedOverlay)
	if err != nil {
		t.Fatalf("reading saved overlay: %v", err)
	}
	if string(data) != "fake-overlay-data" {
		t.Errorf("saved overlay data = %q, want fake-overlay-data", string(data))
	}

	if !strings.Contains(logs.String(), "captured VM snapshot to "+destDir) {
		t.Errorf("log = %q, want mention of captured snapshot", logs.String())
	}
}

func TestRunSnapshot_FailsWhenTheAPIIsUnreachable(t *testing.T) {
	t.Setenv("CHV_API_SOCKET", filepath.Join(t.TempDir(), "no-such.sock"))
	captureLog(t)

	if err := runSnapshot([]string{"-path=" + filepath.Join(t.TempDir(), "snapshot"), "-timeout=100ms"}); err == nil {
		t.Fatal("expected an error with no cloud-hypervisor listening, got nil")
	}
}

// A VM that names no destination for its workload's streams forwards
// nothing and holds up nothing at exit.
func TestForwardWorkloadOutputUnset(t *testing.T) {
	done := forwardWorkloadOutput(context.Background(), make(chan struct{}), config.Config{})
	select {
	case <-done:
	default:
		t.Error("forwarding started with neither GRANULE_WORKLOAD_STDOUT nor GRANULE_WORKLOAD_STDERR set")
	}
}
