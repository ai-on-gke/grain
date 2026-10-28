package hypervisor

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

// newFakeAPIServer starts an HTTP server listening on a unix socket at a
// fresh path under t.TempDir, standing in for cloud-hypervisor's API.
func newFakeAPIServer(t *testing.T, handler http.Handler) (socketPath string) {
	t.Helper()
	socketPath = filepath.Join(t.TempDir(), "api.sock")
	l, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listening on %s: %v", socketPath, err)
	}
	srv := httptest.NewUnstartedServer(handler)
	srv.Listener = l
	srv.Start()
	t.Cleanup(srv.Close)
	return socketPath
}

func TestAPIClient_PowerButton(t *testing.T) {
	var gotMethod, gotPath string
	socket := newFakeAPIServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))

	c := NewAPIClient(socket)
	if err := c.PowerButton(context.Background()); err != nil {
		t.Fatalf("PowerButton() error = %v", err)
	}
	if gotMethod != http.MethodPut || gotPath != "/api/v1/vm.power-button" {
		t.Errorf("got %s %s, want PUT /api/v1/vm.power-button", gotMethod, gotPath)
	}
}

func TestAPIClient_ShutdownVMM(t *testing.T) {
	var gotPath string
	socket := newFakeAPIServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))

	c := NewAPIClient(socket)
	if err := c.ShutdownVMM(context.Background()); err != nil {
		t.Fatalf("ShutdownVMM() error = %v", err)
	}
	if gotPath != "/api/v1/vmm.shutdown" {
		t.Errorf("got path %s, want /api/v1/vmm.shutdown", gotPath)
	}
}

func TestAPIClient_PauseAndResume(t *testing.T) {
	var gotMethods, gotPaths []string
	socket := newFakeAPIServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethods = append(gotMethods, r.Method)
		gotPaths = append(gotPaths, r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	}))

	c := NewAPIClient(socket)
	if err := c.Pause(context.Background()); err != nil {
		t.Fatalf("Pause() error = %v", err)
	}
	if err := c.Resume(context.Background()); err != nil {
		t.Fatalf("Resume() error = %v", err)
	}

	wantPaths := []string{"/api/v1/vm.pause", "/api/v1/vm.resume"}
	if len(gotPaths) != len(wantPaths) {
		t.Fatalf("got paths %v, want %v", gotPaths, wantPaths)
	}
	for i, want := range wantPaths {
		if gotPaths[i] != want || gotMethods[i] != http.MethodPut {
			t.Errorf("request %d = %s %s, want PUT %s", i, gotMethods[i], gotPaths[i], want)
		}
	}
}

func TestAPIClient_Snapshot(t *testing.T) {
	var gotMethod, gotPath, gotBody string
	socket := newFakeAPIServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	c := NewAPIClient(socket)
	if err := c.Snapshot(context.Background(), "/var/lib/granule/snapshot"); err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if gotMethod != http.MethodPut || gotPath != "/api/v1/vm.snapshot" {
		t.Errorf("got %s %s, want PUT /api/v1/vm.snapshot", gotMethod, gotPath)
	}
	if want := `{"destination_url":"file:///var/lib/granule/snapshot"}`; gotBody != want {
		t.Errorf("body = %s, want %s", gotBody, want)
	}
}

func TestAPIClient_Resize(t *testing.T) {
	var gotMethod, gotPath, gotContentType string
	var gotBody []byte
	socket := newFakeAPIServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))

	c := NewAPIClient(socket)
	if err := c.Resize(context.Background(), 2*1024*1024*1024); err != nil {
		t.Fatalf("Resize() error = %v", err)
	}
	if gotMethod != http.MethodPut || gotPath != "/api/v1/vm.resize" {
		t.Errorf("got %s %s, want PUT /api/v1/vm.resize", gotMethod, gotPath)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	wantBody := `{"desired_ram":2147483648}`
	if string(gotBody) != wantBody {
		t.Errorf("body = %s, want %s", gotBody, wantBody)
	}
}

func TestAPIClient_ResizeCPUs(t *testing.T) {
	var gotMethod, gotPath, gotContentType string
	var gotBody []byte
	socket := newFakeAPIServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))

	c := NewAPIClient(socket)
	if err := c.ResizeCPUs(context.Background(), 4); err != nil {
		t.Fatalf("ResizeCPUs() error = %v", err)
	}
	if gotMethod != http.MethodPut || gotPath != "/api/v1/vm.resize" {
		t.Errorf("got %s %s, want PUT /api/v1/vm.resize", gotMethod, gotPath)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	wantBody := `{"desired_vcpus":4}`
	if string(gotBody) != wantBody {
		t.Errorf("body = %s, want %s", gotBody, wantBody)
	}
}

func TestAPIClient_ResizeCPUsSurfacesPendingRemovalError(t *testing.T) {
	socket := newFakeAPIServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "a cpu removal is still pending", http.StatusTooManyRequests)
	}))

	c := NewAPIClient(socket)
	if err := c.ResizeCPUs(context.Background(), 4); err == nil {
		t.Fatal("expected error for 429 response, got nil")
	}
}

// vmInfoJSON is a vm.info body in the shape cloud-hypervisor v53.0
// actually returns, trimmed to the fields VMInfo reads. hotpluggedSize
// is rendered the way the real thing does: left out entirely while it is
// zero, rather than sent as 0.
func vmInfoJSON(bootSize, hotplugSize, hotpluggedSize uint64, bootVCPUs, maxVCPUs uint32) string {
	memory := fmt.Sprintf(`{"size":%d,"hotplug_method":"VirtioMem","hotplug_size":%d`, bootSize, hotplugSize)
	if hotpluggedSize > 0 {
		memory += fmt.Sprintf(`,"hotplugged_size":%d`, hotpluggedSize)
	}
	memory += "}"
	return fmt.Sprintf(`{"config":{"cpus":{"boot_vcpus":%d,"max_vcpus":%d},"memory":%s},"state":"Running","memory_actual_size":%d}`,
		bootVCPUs, maxVCPUs, memory, bootSize)
}

func TestAPIClient_Info(t *testing.T) {
	var gotMethod, gotPath string
	socket := newFakeAPIServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		io.WriteString(w, vmInfoJSON(256<<20, 768<<20, 256<<20, 2, 4))
	}))

	c := NewAPIClient(socket)
	info, err := c.Info(context.Background())
	if err != nil {
		t.Fatalf("Info() error = %v", err)
	}
	if gotMethod != http.MethodGet || gotPath != "/api/v1/vm.info" {
		t.Errorf("got %s %s, want GET /api/v1/vm.info", gotMethod, gotPath)
	}
	// A resize leaves the boot size alone and moves hotplugged_size, so
	// the size the VM currently has is the two added together.
	if got, want := info.DesiredRAMBytes(), uint64(512<<20); got != want {
		t.Errorf("DesiredRAMBytes() = %d, want %d", got, want)
	}
	if got, want := info.DesiredVCPUs(), uint32(2); got != want {
		t.Errorf("DesiredVCPUs() = %d, want %d", got, want)
	}
	if got, want := info.Config.CPUs.MaxVCPUs, uint32(4); got != want {
		t.Errorf("MaxVCPUs = %d, want %d", got, want)
	}
	if got, want := info.State, "Running"; got != want {
		t.Errorf("State = %q, want %q", got, want)
	}
	if got, want := info.MemoryActualSize, uint64(256<<20); got != want {
		t.Errorf("MemoryActualSize = %d, want %d", got, want)
	}
}

// A VM that has never been resized has no hotplugged_size field at all,
// which has to read as "still at the boot size" rather than as a parse
// failure.
func TestAPIClient_InfoWithoutHotpluggedSize(t *testing.T) {
	socket := newFakeAPIServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, vmInfoJSON(256<<20, 768<<20, 0, 1, 4))
	}))

	info, err := NewAPIClient(socket).Info(context.Background())
	if err != nil {
		t.Fatalf("Info() error = %v", err)
	}
	if got, want := info.DesiredRAMBytes(), uint64(256<<20); got != want {
		t.Errorf("DesiredRAMBytes() = %d, want %d", got, want)
	}
}

func TestAPIClient_InfoErrorStatus(t *testing.T) {
	socket := newFakeAPIServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not booted", http.StatusInternalServerError)
	}))

	if _, err := NewAPIClient(socket).Info(context.Background()); err == nil {
		t.Fatal("expected error for 500 response, got nil")
	}
}

func TestAPIClient_InfoRejectsGarbage(t *testing.T) {
	socket := newFakeAPIServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "not json")
	}))

	if _, err := NewAPIClient(socket).Info(context.Background()); err == nil {
		t.Fatal("expected error for a non-JSON body, got nil")
	}
}

func TestAPIClient_ResizeIfNeededSkipsCurrentSize(t *testing.T) {
	var paths []string
	socket := newFakeAPIServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/api/v1/vm.resize" {
			t.Error("vm.resize called for a size the VM already has")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		io.WriteString(w, vmInfoJSON(256<<20, 768<<20, 256<<20, 1, 4))
	}))

	c := NewAPIClient(socket)
	resized, err := c.ResizeIfNeeded(context.Background(), 512*1024*1024)
	if err != nil {
		t.Fatalf("ResizeIfNeeded() error = %v", err)
	}
	if resized {
		t.Error("ResizeIfNeeded() reported a resize, want none")
	}
	if want := []string{"/api/v1/vm.info"}; len(paths) != len(want) || paths[0] != want[0] {
		t.Errorf("requests = %v, want %v", paths, want)
	}
}

// The other end of the same case: a VM still at its boot size has no
// hotplugged_size at all, and asking for that size is just as much a
// no-op as asking for a size it was resized to.
func TestAPIClient_ResizeIfNeededSkipsBootSize(t *testing.T) {
	socket := newFakeAPIServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/vm.resize" {
			t.Error("vm.resize called for a size the VM already has")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		io.WriteString(w, vmInfoJSON(256<<20, 768<<20, 0, 1, 4))
	}))

	resized, err := NewAPIClient(socket).ResizeIfNeeded(context.Background(), 256*1024*1024)
	if err != nil {
		t.Fatalf("ResizeIfNeeded() error = %v", err)
	}
	if resized {
		t.Error("ResizeIfNeeded() reported a resize, want none")
	}
}

func TestAPIClient_ResizeIfNeededResizesWhenDifferent(t *testing.T) {
	var gotBody []byte
	socket := newFakeAPIServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/vm.resize" {
			gotBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		io.WriteString(w, vmInfoJSON(256<<20, 768<<20, 0, 1, 4))
	}))

	resized, err := NewAPIClient(socket).ResizeIfNeeded(context.Background(), 512*1024*1024)
	if err != nil {
		t.Fatalf("ResizeIfNeeded() error = %v", err)
	}
	if !resized {
		t.Error("ResizeIfNeeded() reported no resize, want one")
	}
	if want := `{"desired_ram":536870912}`; string(gotBody) != want {
		t.Errorf("body = %s, want %s", gotBody, want)
	}
}

// Not being able to read the current size is not a reason to guess: the
// resize goes through the same socket, so a vm.info that fails is a
// failure the caller has to hear about.
func TestAPIClient_ResizeIfNeededFailsWhenInfoFails(t *testing.T) {
	socket := newFakeAPIServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/vm.resize" {
			t.Error("vm.resize called without knowing the current size")
		}
		http.Error(w, "not booted", http.StatusInternalServerError)
	}))

	if _, err := NewAPIClient(socket).ResizeIfNeeded(context.Background(), 512*1024*1024); err == nil {
		t.Fatal("expected error when vm.info fails, got nil")
	}
}

func TestAPIClient_ResizeCPUsIfNeededSkipsCurrentCount(t *testing.T) {
	var paths []string
	socket := newFakeAPIServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/api/v1/vm.resize" {
			t.Error("vm.resize called for a vCPU count the VM already has")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		io.WriteString(w, vmInfoJSON(256<<20, 768<<20, 0, 2, 4))
	}))

	resized, err := NewAPIClient(socket).ResizeCPUsIfNeeded(context.Background(), 2)
	if err != nil {
		t.Fatalf("ResizeCPUsIfNeeded() error = %v", err)
	}
	if resized {
		t.Error("ResizeCPUsIfNeeded() reported a resize, want none")
	}
	if want := []string{"/api/v1/vm.info"}; len(paths) != len(want) || paths[0] != want[0] {
		t.Errorf("requests = %v, want %v", paths, want)
	}
}

func TestAPIClient_ResizeCPUsIfNeededResizesWhenDifferent(t *testing.T) {
	var gotBody []byte
	socket := newFakeAPIServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/vm.resize" {
			gotBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		io.WriteString(w, vmInfoJSON(256<<20, 768<<20, 0, 2, 4))
	}))

	resized, err := NewAPIClient(socket).ResizeCPUsIfNeeded(context.Background(), 4)
	if err != nil {
		t.Fatalf("ResizeCPUsIfNeeded() error = %v", err)
	}
	if !resized {
		t.Error("ResizeCPUsIfNeeded() reported no resize, want one")
	}
	if want := `{"desired_vcpus":4}`; string(gotBody) != want {
		t.Errorf("body = %s, want %s", gotBody, want)
	}
}

func TestAPIClient_ErrorStatusReturnsError(t *testing.T) {
	socket := newFakeAPIServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not booted", http.StatusMethodNotAllowed)
	}))

	c := NewAPIClient(socket)
	if err := c.PowerButton(context.Background()); err == nil {
		t.Fatal("expected error for 405 response, got nil")
	}
}

func TestAPIClient_WaitReady(t *testing.T) {
	socket := newFakeAPIServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	c := NewAPIClient(socket)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.WaitReady(ctx); err != nil {
		t.Fatalf("WaitReady() error = %v", err)
	}
}

func TestAPIClient_WaitReadyTimesOutWithoutServer(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "no-such.sock")
	c := NewAPIClient(socket)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := c.WaitReady(ctx); err == nil {
		t.Fatal("expected WaitReady to time out when nothing is listening, got nil")
	}
}
