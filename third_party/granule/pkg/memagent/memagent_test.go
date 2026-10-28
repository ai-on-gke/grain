package memagent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwsalmon/granule/pkg/hypervisor"
	"github.com/bwsalmon/granule/pkg/telemetry"
)

const mib = 1024 * 1024

// testToken is the token startServer gives a Server whose Config names
// none: Serve refuses to run without one.
const testToken = "test-token-0123456789abcdef"

// fakeVM stands in for cloud-hypervisor: it holds a size the way the
// real API does -- a boot size plus whatever the last resize hotplugged
// on top -- and answers Info/ResizeIfNeeded from it, so a test can move
// the VM underneath the Server (as an outside "granule resize" would)
// and see what the Server asks for next.
type fakeVM struct {
	mu sync.Mutex
	// bootBytes and hotpluggedBytes are the two halves vm.info reports
	// separately and DesiredRAMBytes adds back together.
	bootBytes       uint64
	hotpluggedBytes uint64
	// actualBytes is what the guest has plugged in so far, which lags
	// the two above; zero means "same as desired", i.e. caught up.
	actualBytes uint64
	// resizes records the desiredRAMBytes of every ResizeIfNeeded call
	// that actually sent a resize; skipped ones are counted separately.
	resizes   []uint64
	skips     int
	infos     int
	infoErr   error
	resizeErr error
	// beforeResize, if set, runs once at the start of the next
	// ResizeIfNeeded call -- the hook a test uses to move the VM in the
	// window between the Server reading its size and asking for a new
	// one. Called without the lock held.
	beforeResize func()
}

func newFakeVM(bootMB int) *fakeVM {
	return &fakeVM{bootBytes: uint64(bootMB) * mib}
}

func (f *fakeVM) Info(context.Context) (hypervisor.VMInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.infos++
	if f.infoErr != nil {
		return hypervisor.VMInfo{}, f.infoErr
	}
	actual := f.actualBytes
	if actual == 0 {
		actual = f.bootBytes + f.hotpluggedBytes
	}
	return hypervisor.VMInfo{
		State:            "Running",
		MemoryActualSize: actual,
		Config: hypervisor.VMConfigInfo{
			Memory: hypervisor.MemoryInfo{
				Size:           f.bootBytes,
				HotpluggedSize: f.hotpluggedBytes,
			},
		},
	}, nil
}

func (f *fakeVM) ResizeIfNeeded(_ context.Context, desiredRAMBytes uint64) (bool, error) {
	f.mu.Lock()
	hook := f.beforeResize
	f.beforeResize = nil
	f.mu.Unlock()
	if hook != nil {
		hook()
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.resizeErr != nil {
		return false, f.resizeErr
	}
	if f.bootBytes+f.hotpluggedBytes == desiredRAMBytes {
		f.skips++
		return false, nil
	}
	f.resizes = append(f.resizes, desiredRAMBytes)
	f.hotpluggedBytes = desiredRAMBytes - f.bootBytes
	return true, nil
}

// resizeTo moves the VM the way a "granule resize" run from outside the
// Server would, without the Server hearing about it.
func (f *fakeVM) resizeTo(mb int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hotpluggedBytes = uint64(mb)*mib - f.bootBytes
}

func (f *fakeVM) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.resizes)
}

func (f *fakeVM) skipCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.skips
}

func (f *fakeVM) lastCall() uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.resizes) == 0 {
		return 0
	}
	return f.resizes[len(f.resizes)-1]
}

func (f *fakeVM) infoCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.infos
}

func (f *fakeVM) setResizeErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resizeErr = err
}

// startServer starts a Server on an ephemeral loopback port and returns
// its address plus a cancel func that stops it.
func startServer(t *testing.T, cfg Config, api Resizer) (addr string, cancel context.CancelFunc) {
	t.Helper()
	if cfg.Addr == "" {
		cfg.Addr = "127.0.0.1:0"
	}
	if cfg.Token == "" {
		cfg.Token = testToken
	}

	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", cfg.Addr)
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	addr = ln.Addr().String()
	ln.Close()
	cfg.Addr = addr

	s := New(cfg, api)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()

	// Wait for the listener to actually be accepting before returning.
	waitForListener(t, addr)

	t.Cleanup(func() {
		cancel()
		<-done
	})
	return addr, cancel
}

func waitForListener(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server at %s never started accepting connections", addr)
}

func signal(t *testing.T, addr, line string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dialing %s: %v", addr, err)
	}
	defer conn.Close()
	if _, err := fmt.Fprintf(conn, "%s\n", line); err != nil {
		t.Fatalf("writing signal: %v", err)
	}
}

func waitForCallCount(t *testing.T, api *fakeVM, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if api.callCount() >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("resize requested %d time(s), want at least %d", api.callCount(), want)
}

func TestServer_GrowsOnPressureSignal(t *testing.T) {
	api := newFakeVM(256)
	cfg := Config{MaxMB: 2048, StepMB: 256, Cooldown: time.Hour}
	addr, _ := startServer(t, cfg, api)

	signal(t, addr, "PRESSURE "+testToken+" 20.00")
	waitForCallCount(t, api, 1)

	wantBytes := uint64(512) * mib
	if got := api.lastCall(); got != wantBytes {
		t.Errorf("resize requested %d bytes, want %d (256+256 MiB)", got, wantBytes)
	}
}

// The point of the change this test guards: the step is measured from
// the size the VM actually has, not from a size the Server counted up
// itself, so a resize made from outside it (or before it started) is
// picked up rather than stepped over.
func TestServer_StepsFromTheVMsCurrentSize(t *testing.T) {
	api := newFakeVM(256)
	api.resizeTo(1024) // as an outside "granule resize" would
	cfg := Config{MaxMB: 2048, StepMB: 256, Cooldown: time.Hour}
	addr, _ := startServer(t, cfg, api)

	signal(t, addr, "PRESSURE "+testToken+" 20.00")
	waitForCallCount(t, api, 1)

	wantBytes := uint64(1280) * mib
	if got := api.lastCall(); got != wantBytes {
		t.Errorf("resize requested %d bytes, want %d (1024+256 MiB): the step should start from the VM's own size", got, wantBytes)
	}
}

// The size to step from is what cloud-hypervisor was last asked for, not
// what the guest has finished plugging in: a guest still catching up on
// an earlier grow would otherwise have that grow handed to it twice.
func TestServer_StepsFromDesiredSizeNotActualSize(t *testing.T) {
	api := newFakeVM(256)
	api.resizeTo(768)
	api.mu.Lock()
	api.actualBytes = 512 * mib // guest is midway through plugging it in
	api.mu.Unlock()
	cfg := Config{MaxMB: 2048, StepMB: 256, Cooldown: time.Hour}
	addr, _ := startServer(t, cfg, api)

	signal(t, addr, "PRESSURE "+testToken+" 20.00")
	waitForCallCount(t, api, 1)

	wantBytes := uint64(1024) * mib
	if got := api.lastCall(); got != wantBytes {
		t.Errorf("resize requested %d bytes, want %d (768+256 MiB, stepping from the desired size)", got, wantBytes)
	}
}

func TestServer_AdaptiveStepping(t *testing.T) {
	tests := []struct {
		name         string
		bootMB       int
		maxMB        int
		stepMB       int
		signal       string
		wantTargetMB int
	}{
		{
			name:         "moderate pressure lower bound (10%)",
			bootMB:       256,
			maxMB:        2048,
			stepMB:       256,
			signal:       "PRESSURE " + testToken + " 10.00",
			wantTargetMB: 512, // 256 + 1*256
		},
		{
			name:         "moderate pressure mid range (20%)",
			bootMB:       512,
			maxMB:        2048,
			stepMB:       256,
			signal:       "PRESSURE " + testToken + " 20.00",
			wantTargetMB: 768, // 512 + 1*256
		},
		{
			name:         "moderate pressure upper bound (29.99%)",
			bootMB:       256,
			maxMB:        2048,
			stepMB:       256,
			signal:       "PRESSURE " + testToken + " 29.99",
			wantTargetMB: 512, // 256 + 1*256
		},
		{
			name:         "high pressure lower bound (30%)",
			bootMB:       256,
			maxMB:        2048,
			stepMB:       256,
			signal:       "PRESSURE " + testToken + " 30.00",
			wantTargetMB: 768, // 256 + 2*256
		},
		{
			name:         "high pressure mid range (45%)",
			bootMB:       512,
			maxMB:        2048,
			stepMB:       256,
			signal:       "PRESSURE " + testToken + " 45.00",
			wantTargetMB: 1024, // 512 + 2*256
		},
		{
			name:         "high pressure upper bound (59.99%)",
			bootMB:       256,
			maxMB:        2048,
			stepMB:       256,
			signal:       "PRESSURE " + testToken + " 59.99",
			wantTargetMB: 768, // 256 + 2*256
		},
		{
			name:         "critical pressure lower bound (60%)",
			bootMB:       256,
			maxMB:        2048,
			stepMB:       256,
			signal:       "PRESSURE " + testToken + " 60.00",
			wantTargetMB: 1280, // 256 + 4*256
		},
		{
			name:         "critical pressure high (85%)",
			bootMB:       512,
			maxMB:        4096,
			stepMB:       256,
			signal:       "PRESSURE " + testToken + " 85.00",
			wantTargetMB: 1536, // 512 + 4*256
		},
		{
			name:         "critical pressure capped at MaxMB",
			bootMB:       1500,
			maxMB:        2048,
			stepMB:       256,
			signal:       "PRESSURE " + testToken + " 75.00",
			wantTargetMB: 2048, // min(1500 + 4*256 = 2524, 2048)
		},
		{
			name:         "fallback for non-float reading",
			bootMB:       256,
			maxMB:        2048,
			stepMB:       256,
			signal:       "PRESSURE " + testToken + " invalid",
			wantTargetMB: 512, // 256 + 1*256
		},
		{
			name:         "fallback for low pressure (< 10%)",
			bootMB:       256,
			maxMB:        2048,
			stepMB:       256,
			signal:       "PRESSURE " + testToken + " 5.00",
			wantTargetMB: 512, // 256 + 1*256
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeVM(tt.bootMB)
			cfg := Config{MaxMB: tt.maxMB, StepMB: tt.stepMB, Cooldown: time.Hour}
			addr, _ := startServer(t, cfg, api)

			signal(t, addr, tt.signal)
			waitForCallCount(t, api, 1)

			wantBytes := uint64(tt.wantTargetMB) * mib
			if got := api.lastCall(); got != wantBytes {
				t.Errorf("resize requested %d bytes (%d MiB), want %d bytes (%d MiB)", got, got/mib, wantBytes, tt.wantTargetMB)
			}
		})
	}
}

func TestServer_CapsGrowthAtMaxMB(t *testing.T) {
	api := newFakeVM(1900)
	cfg := Config{MaxMB: 2048, StepMB: 256, Cooldown: time.Hour}
	addr, _ := startServer(t, cfg, api)

	signal(t, addr, "PRESSURE "+testToken+" 99.99")
	waitForCallCount(t, api, 1)

	wantBytes := uint64(2048) * mib
	if got := api.lastCall(); got != wantBytes {
		t.Errorf("resize requested %d bytes, want %d (capped at 2048 MiB)", got, wantBytes)
	}
}

func TestServer_IgnoresSignalAlreadyAtCeiling(t *testing.T) {
	api := newFakeVM(2048)
	cfg := Config{MaxMB: 2048, StepMB: 256, Cooldown: time.Hour}
	addr, _ := startServer(t, cfg, api)

	signal(t, addr, "PRESSURE "+testToken+" 1.00")
	// Give the (non-)call a moment to (not) happen, then confirm it never
	// did -- there's no success signal to wait on for a call that's
	// expected not to occur.
	time.Sleep(200 * time.Millisecond)
	if got := api.callCount(); got != 0 {
		t.Errorf("resize requested %d time(s), want 0 when already at ceiling", got)
	}
}

// A Server that had grown the guest itself used to keep believing it
// had, even after the guest was resized back down from outside; it now
// re-reads the size and grows again.
func TestServer_GrowsAgainAfterAnOutsideShrink(t *testing.T) {
	api := newFakeVM(256)
	api.resizeTo(2048)
	cfg := Config{MaxMB: 2048, StepMB: 256, Cooldown: 50 * time.Millisecond}
	addr, _ := startServer(t, cfg, api)

	signal(t, addr, "PRESSURE "+testToken+" 10.00")
	time.Sleep(200 * time.Millisecond)
	if got := api.callCount(); got != 0 {
		t.Fatalf("resize requested %d time(s) at the ceiling, want 0", got)
	}

	api.resizeTo(512) // "granule resize -memory-mb=512" from outside
	signal(t, addr, "PRESSURE "+testToken+" 10.00")
	waitForCallCount(t, api, 1)

	wantBytes := uint64(768) * mib
	if got := api.lastCall(); got != wantBytes {
		t.Errorf("resize requested %d bytes, want %d (512+256 MiB): the ceiling should be judged against the VM's own size", got, wantBytes)
	}
}

// The guest landing on exactly the size the Server was about to ask for
// -- an outside resize racing this one -- is a skip, not the 500 the
// virtio-mem device answers an identical request with.
func TestServer_SkipsResizeWhenTheVMIsAlreadyAtTheTarget(t *testing.T) {
	api := newFakeVM(256)
	// An outside "granule resize -memory-mb=512" lands in the window
	// between the Server reading the size and asking for 512 itself.
	api.beforeResize = func() { api.resizeTo(512) }
	cfg := Config{MaxMB: 2048, StepMB: 256, Cooldown: time.Hour}
	addr, _ := startServer(t, cfg, api)

	signal(t, addr, "PRESSURE "+testToken+" 10.00")
	time.Sleep(200 * time.Millisecond)

	if got := api.callCount(); got != 0 {
		t.Errorf("resize requested %d time(s), want 0 when the VM is already at the target", got)
	}
	if got := api.skipCount(); got != 1 {
		t.Errorf("skipped resizes = %d, want 1", got)
	}
}

func TestServer_RespectsCooldown(t *testing.T) {
	api := newFakeVM(256)
	cfg := Config{MaxMB: 2048, StepMB: 256, Cooldown: time.Hour}
	addr, _ := startServer(t, cfg, api)

	signal(t, addr, "PRESSURE "+testToken+" 10.00")
	waitForCallCount(t, api, 1)

	signal(t, addr, "PRESSURE "+testToken+" 10.00")
	time.Sleep(200 * time.Millisecond)
	if got := api.callCount(); got != 1 {
		t.Errorf("resize requested %d time(s), want exactly 1 within the cooldown window", got)
	}
}

func TestServer_ConcurrentSignalsIgnoredWhenInFlight(t *testing.T) {
	api := newFakeVM(256)
	inFlightStarted := make(chan struct{})
	releaseInFlight := make(chan struct{})

	api.mu.Lock()
	api.beforeResize = func() {
		close(inFlightStarted)
		<-releaseInFlight
	}
	api.mu.Unlock()

	cfg := Config{MaxMB: 2048, StepMB: 256, Cooldown: 10 * time.Millisecond}
	addr, _ := startServer(t, cfg, api)

	// Send first signal in a goroutine
	go func() {
		signal(t, addr, "PRESSURE "+testToken+" 10.00")
	}()

	// Wait until the first signal is in-flight (inside beforeResize)
	select {
	case <-inFlightStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first signal to reach resize")
	}

	// Send second signal while first is still in-flight
	signal(t, addr, "PRESSURE "+testToken+" 20.00")
	time.Sleep(50 * time.Millisecond)

	// Let the first resize complete
	close(releaseInFlight)
	waitForCallCount(t, api, 1)

	// Wait out the cooldown to verify that the second signal was dropped, not queued
	time.Sleep(100 * time.Millisecond)
	if got := api.callCount(); got != 1 {
		t.Errorf("resize requested %d time(s), want exactly 1 (concurrent signal during in-flight should be ignored)", got)
	}
}

func TestServer_GrowsAgainAfterCooldownElapses(t *testing.T) {
	api := newFakeVM(256)
	cfg := Config{MaxMB: 2048, StepMB: 256, Cooldown: 50 * time.Millisecond}
	addr, _ := startServer(t, cfg, api)

	signal(t, addr, "PRESSURE "+testToken+" 10.00")
	waitForCallCount(t, api, 1)

	time.Sleep(100 * time.Millisecond)
	signal(t, addr, "PRESSURE "+testToken+" 10.00")
	waitForCallCount(t, api, 2)

	wantBytes := uint64(768) * mib
	if got := api.lastCall(); got != wantBytes {
		t.Errorf("resize requested %d bytes, want %d (256+256+256 MiB)", got, wantBytes)
	}
}

func TestServer_ResizeErrorDoesNotAdvanceState(t *testing.T) {
	api := newFakeVM(256)
	api.setResizeErr(errors.New("cloud-hypervisor rejected the resize"))
	cfg := Config{MaxMB: 2048, StepMB: 256, Cooldown: 10 * time.Millisecond}
	addr, _ := startServer(t, cfg, api)

	signal(t, addr, "PRESSURE "+testToken+" 10.00")
	time.Sleep(200 * time.Millisecond)

	api.setResizeErr(nil)

	signal(t, addr, "PRESSURE "+testToken+" 10.00")
	waitForCallCount(t, api, 1)

	wantBytes := uint64(512) * mib
	if got := api.lastCall(); got != wantBytes {
		t.Errorf("resize requested %d bytes, want %d: a failed resize should not have moved the guest", got, wantBytes)
	}
}

// A vm.info that fails leaves the Server with no size to step from, so
// it asks for nothing rather than guessing -- and stays out of its own
// cooldown, so the next signal tries again.
func TestServer_InfoErrorSkipsTheResize(t *testing.T) {
	api := newFakeVM(256)
	api.mu.Lock()
	api.infoErr = errors.New("cloud-hypervisor api GET /api/v1/vm.info: 500")
	api.mu.Unlock()
	cfg := Config{MaxMB: 2048, StepMB: 256, Cooldown: time.Hour}
	addr, _ := startServer(t, cfg, api)

	signal(t, addr, "PRESSURE "+testToken+" 10.00")
	time.Sleep(200 * time.Millisecond)
	if got := api.callCount(); got != 0 {
		t.Fatalf("resize requested %d time(s), want 0 when the current size could not be read", got)
	}

	api.mu.Lock()
	api.infoErr = nil
	api.mu.Unlock()

	signal(t, addr, "PRESSURE "+testToken+" 10.00")
	waitForCallCount(t, api, 1)

	wantBytes := uint64(512) * mib
	if got := api.lastCall(); got != wantBytes {
		t.Errorf("resize requested %d bytes, want %d", got, wantBytes)
	}
}

func TestServer_IgnoresBlankConnections(t *testing.T) {
	api := newFakeVM(256)
	cfg := Config{MaxMB: 2048, StepMB: 256, Cooldown: time.Hour}
	addr, _ := startServer(t, cfg, api)

	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dialing %s: %v", addr, err)
	}
	conn.Close()

	time.Sleep(200 * time.Millisecond)
	if got := api.callCount(); got != 0 {
		t.Errorf("resize requested %d time(s), want 0 for a connection that sent nothing", got)
	}
	if got := api.infoCount(); got != 0 {
		t.Errorf("vm.info read %d time(s), want 0 for a connection that sent nothing", got)
	}
}

func TestServer_AuthenticatedSignal(t *testing.T) {
	api := newFakeVM(256)
	cfg := Config{
		Addr:     "127.0.0.1:0",
		Token:    "secret-token-12345",
		MaxMB:    2048,
		StepMB:   256,
		Cooldown: time.Hour,
	}
	addr, _ := startServer(t, cfg, api)

	signal(t, addr, "PRESSURE secret-token-12345 20.00")
	waitForCallCount(t, api, 1)

	wantBytes := uint64(512) * mib
	if got := api.lastCall(); got != wantBytes {
		t.Errorf("resize requested %d bytes, want %d", got, wantBytes)
	}
}

func TestServer_RejectsInvalidToken(t *testing.T) {
	api := newFakeVM(256)
	cfg := Config{
		Addr:     "127.0.0.1:0",
		Token:    "secret-token-12345",
		MaxMB:    2048,
		StepMB:   256,
		Cooldown: time.Hour,
	}
	addr, _ := startServer(t, cfg, api)

	signal(t, addr, "PRESSURE wrong-token 20.00")
	time.Sleep(200 * time.Millisecond)

	if got := api.callCount(); got != 0 {
		t.Errorf("resize requested %d time(s), want 0 for invalid token", got)
	}
}

func TestServer_RejectsMissingTokenWhenConfigured(t *testing.T) {
	api := newFakeVM(256)
	cfg := Config{
		Addr:     "127.0.0.1:0",
		Token:    "secret-token-12345",
		MaxMB:    2048,
		StepMB:   256,
		Cooldown: time.Hour,
	}
	addr, _ := startServer(t, cfg, api)

	// The format the listener used to accept with no token configured.
	signal(t, addr, "PRESSURE 20.00")
	time.Sleep(200 * time.Millisecond)

	if got := api.callCount(); got != 0 {
		t.Errorf("resize requested %d time(s), want 0 for missing token", got)
	}
}

func TestServer_AuthenticatedLowPressureSignal(t *testing.T) {
	api := newFakeVM(256)
	api.resizeTo(1024)
	cfg := Config{
		Addr:     "127.0.0.1:0",
		Token:    "secret-token-12345",
		MinMB:    256,
		MaxMB:    2048,
		StepMB:   256,
		Cooldown: time.Hour,
	}
	addr, _ := startServer(t, cfg, api)

	signal(t, addr, "PRESSURE_LOW secret-token-12345 800")
	waitForCallCount(t, api, 1)

	wantBytes := uint64(768) * mib
	if got := api.lastCall(); got != wantBytes {
		t.Errorf("resize requested %d bytes, want %d", got, wantBytes)
	}
}

func TestServer_RejectsInvalidTokenLowPressure(t *testing.T) {
	api := newFakeVM(256)
	api.resizeTo(1024)
	cfg := Config{
		Addr:     "127.0.0.1:0",
		Token:    "secret-token-12345",
		MinMB:    256,
		MaxMB:    2048,
		StepMB:   256,
		Cooldown: time.Hour,
	}
	addr, _ := startServer(t, cfg, api)

	signal(t, addr, "PRESSURE_LOW wrong-token 800")
	time.Sleep(200 * time.Millisecond)

	if got := api.callCount(); got != 0 {
		t.Errorf("resize requested %d time(s), want 0 for invalid token on PRESSURE_LOW", got)
	}
}

func TestServer_StopsOnContextCancellation(t *testing.T) {
	api := newFakeVM(256)
	cfg := Config{Addr: "127.0.0.1:0", Token: testToken, MaxMB: 2048, StepMB: 256, Cooldown: time.Hour}

	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", cfg.Addr)
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()
	cfg.Addr = addr

	s := New(cfg, api)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	waitForListener(t, addr)

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve() error = %v, want nil after context cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after context cancellation")
	}
}

func TestServer_ShrinksOnLowPressureSignal(t *testing.T) {
	api := newFakeVM(256)
	api.resizeTo(1024)
	cfg := Config{MaxMB: 2048, StepMB: 256, Cooldown: time.Hour, ScaleDownCooldown: time.Hour}
	addr, _ := startServer(t, cfg, api)

	signal(t, addr, "PRESSURE_LOW "+testToken+" 512")
	waitForCallCount(t, api, 1)

	wantBytes := uint64(768) * mib
	if got := api.lastCall(); got != wantBytes {
		t.Errorf("resize requested %d bytes, want %d (1024-256 MiB)", got, wantBytes)
	}
}

func TestServer_StepsDownToFloor(t *testing.T) {
	api := newFakeVM(256)
	api.resizeTo(384)
	cfg := Config{MaxMB: 2048, StepMB: 256, Cooldown: time.Hour, ScaleDownCooldown: time.Hour}
	addr, _ := startServer(t, cfg, api)

	signal(t, addr, "PRESSURE_LOW "+testToken+" 100")
	waitForCallCount(t, api, 1)

	wantBytes := uint64(256) * mib
	if got := api.lastCall(); got != wantBytes {
		t.Errorf("resize requested %d bytes, want %d (clamped to floor of 256 MiB)", got, wantBytes)
	}
}

func TestServer_IgnoresLowPressureAlreadyAtFloor(t *testing.T) {
	api := newFakeVM(256)
	cfg := Config{MaxMB: 2048, StepMB: 256, Cooldown: time.Hour, ScaleDownCooldown: time.Hour}
	addr, _ := startServer(t, cfg, api)

	signal(t, addr, "PRESSURE_LOW "+testToken+" 100")
	time.Sleep(200 * time.Millisecond)
	if got := api.callCount(); got != 0 {
		t.Errorf("resize requested %d time(s), want 0 when already at floor", got)
	}
}

func TestServer_RespectsScaleDownCooldown(t *testing.T) {
	api := newFakeVM(256)
	api.resizeTo(1024)
	cfg := Config{MaxMB: 2048, StepMB: 256, Cooldown: time.Hour, ScaleDownCooldown: time.Hour}
	addr, _ := startServer(t, cfg, api)

	signal(t, addr, "PRESSURE_LOW "+testToken+" 500")
	waitForCallCount(t, api, 1)

	signal(t, addr, "PRESSURE_LOW "+testToken+" 500")
	time.Sleep(200 * time.Millisecond)
	if got := api.callCount(); got != 1 {
		t.Errorf("resize requested %d time(s), want exactly 1 within the scale-down cooldown window", got)
	}
}

func TestServer_ShrinksAgainAfterScaleDownCooldownElapses(t *testing.T) {
	api := newFakeVM(256)
	api.resizeTo(1024)
	cfg := Config{MaxMB: 2048, StepMB: 256, Cooldown: 50 * time.Millisecond, ScaleDownCooldown: 50 * time.Millisecond}
	addr, _ := startServer(t, cfg, api)

	signal(t, addr, "PRESSURE_LOW "+testToken+" 500")
	waitForCallCount(t, api, 1)

	time.Sleep(100 * time.Millisecond)
	signal(t, addr, "PRESSURE_LOW "+testToken+" 500")
	waitForCallCount(t, api, 2)

	wantBytes := uint64(512) * mib
	if got := api.lastCall(); got != wantBytes {
		t.Errorf("resize requested %d bytes, want %d (1024-256-256 MiB)", got, wantBytes)
	}
}

func TestServer_BidirectionalElasticity(t *testing.T) {
	api := newFakeVM(256)
	cfg := Config{MaxMB: 2048, StepMB: 256, Cooldown: 50 * time.Millisecond, ScaleDownCooldown: 50 * time.Millisecond}
	addr, _ := startServer(t, cfg, api)

	// 1. Grow under pressure: 256 -> 512
	signal(t, addr, "PRESSURE "+testToken+" 25.00")
	waitForCallCount(t, api, 1)
	if got, want := api.lastCall(), uint64(512)*mib; got != want {
		t.Fatalf("grow 1: got %d, want %d", got, want)
	}

	// 2. Grow again: 512 -> 768
	time.Sleep(100 * time.Millisecond)
	signal(t, addr, "PRESSURE "+testToken+" 20.00")
	waitForCallCount(t, api, 2)
	if got, want := api.lastCall(), uint64(768)*mib; got != want {
		t.Fatalf("grow 2: got %d, want %d", got, want)
	}

	// 3. Shrink on low pressure / idle: 768 -> 512
	time.Sleep(100 * time.Millisecond)
	signal(t, addr, "PRESSURE_LOW "+testToken+" 600")
	waitForCallCount(t, api, 3)
	if got, want := api.lastCall(), uint64(512)*mib; got != want {
		t.Fatalf("shrink 1: got %d, want %d", got, want)
	}

	// 4. Shrink again down to boot size: 512 -> 256
	time.Sleep(100 * time.Millisecond)
	signal(t, addr, "PRESSURE_LOW "+testToken+" 600")
	waitForCallCount(t, api, 4)
	if got, want := api.lastCall(), uint64(256)*mib; got != want {
		t.Fatalf("shrink 2: got %d, want %d", got, want)
	}

	// 5. Shrink below boot size ignored: stays at 256
	time.Sleep(100 * time.Millisecond)
	signal(t, addr, "PRESSURE_LOW "+testToken+" 600")
	time.Sleep(150 * time.Millisecond)
	if got := api.callCount(); got != 4 {
		t.Fatalf("shrink 3: got %d calls, want 4 (ignored at floor)", got)
	}

	// 6. Grow again under new pressure: 256 -> 512
	time.Sleep(100 * time.Millisecond)
	signal(t, addr, "PRESSURE "+testToken+" 20.00")
	waitForCallCount(t, api, 5)
	if got, want := api.lastCall(), uint64(512)*mib; got != want {
		t.Fatalf("grow 3: got %d, want %d", got, want)
	}
}

func TestServer_SkipsShrinkWhenTheVMIsAlreadyAtTheTarget(t *testing.T) {
	api := newFakeVM(256)
	api.resizeTo(1024)
	// An outside "granule resize -memory-mb=768" lands in the window
	// between the Server reading the size (1024) and asking for 768 itself.
	api.beforeResize = func() { api.resizeTo(768) }
	cfg := Config{MaxMB: 2048, StepMB: 256, Cooldown: time.Hour, ScaleDownCooldown: time.Hour}
	addr, _ := startServer(t, cfg, api)

	signal(t, addr, "PRESSURE_LOW "+testToken+" 500")
	time.Sleep(200 * time.Millisecond)

	if got := api.callCount(); got != 0 {
		t.Errorf("resize requested %d time(s), want 0 when the VM is already at the target", got)
	}
	if got := api.skipCount(); got != 1 {
		t.Errorf("skipped resizes = %d, want 1", got)
	}
}

func TestServer_CustomMinMBAndStepDownMB(t *testing.T) {
	api := newFakeVM(256)
	api.resizeTo(1024)
	cfg := Config{MinMB: 512, MaxMB: 2048, StepMB: 256, StepDownMB: 128, Cooldown: 50 * time.Millisecond, ScaleDownCooldown: 50 * time.Millisecond}
	addr, _ := startServer(t, cfg, api)

	// Step down by 128MB: 1024 -> 896
	signal(t, addr, "PRESSURE_LOW "+testToken+" 600")
	waitForCallCount(t, api, 1)
	if got, want := api.lastCall(), uint64(896)*mib; got != want {
		t.Errorf("shrink: got %d, want %d", got, want)
	}

	// Move VM to 600MB
	api.resizeTo(600)
	time.Sleep(100 * time.Millisecond)
	// Step down by 128MB from 600: 600 - 128 = 472 < MinMB(512), should clamp to 512MB
	signal(t, addr, "PRESSURE_LOW "+testToken+" 600")
	waitForCallCount(t, api, 2)
	if got, want := api.lastCall(), uint64(512)*mib; got != want {
		t.Errorf("shrink with MinMB clamp: got %d, want %d", got, want)
	}
}

func TestServer_ConcurrentLowPressureIgnoredWhenInFlight(t *testing.T) {
	api := newFakeVM(256)
	api.resizeTo(1024)
	inFlightStarted := make(chan struct{})
	releaseInFlight := make(chan struct{})

	api.mu.Lock()
	api.beforeResize = func() {
		close(inFlightStarted)
		<-releaseInFlight
	}
	api.mu.Unlock()

	cfg := Config{MaxMB: 2048, StepMB: 256, Cooldown: 10 * time.Millisecond, ScaleDownCooldown: 10 * time.Millisecond}
	addr, _ := startServer(t, cfg, api)

	// Send first low pressure signal in a goroutine
	go func() {
		signal(t, addr, "PRESSURE_LOW "+testToken+" 500")
	}()

	// Wait until the first signal is in-flight (inside beforeResize)
	select {
	case <-inFlightStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first low pressure signal to reach resize")
	}

	// Send second low pressure signal while first is still in-flight
	signal(t, addr, "PRESSURE_LOW "+testToken+" 600")
	time.Sleep(50 * time.Millisecond)

	// Let the first resize complete
	close(releaseInFlight)
	waitForCallCount(t, api, 1)

	// Wait out the cooldown to verify that the second signal was dropped, not queued
	time.Sleep(100 * time.Millisecond)
	if got := api.callCount(); got != 1 {
		t.Errorf("resize requested %d time(s), want exactly 1 (concurrent signal during in-flight should be ignored)", got)
	}
}

func TestServer_TelemetryIntegration(t *testing.T) {
	api := newFakeVM(256)
	metrics := telemetry.NewMemoryHotplugMetrics()
	cfg := Config{
		Addr:     "127.0.0.1:0",
		Token:    testToken,
		MaxMB:    1024,
		StepMB:   256,
		Cooldown: 50 * time.Millisecond,
		Metrics:  metrics,
	}

	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", cfg.Addr)
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()
	cfg.Addr = addr

	s := New(cfg, api)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	waitForListener(t, addr)
	t.Cleanup(func() {
		cancel()
		<-done
	})

	// 1. First signal -> success (256MB -> 512MB)
	signal(t, addr, "PRESSURE "+testToken+" 20.00")
	waitForCallCount(t, api, 1)

	if got := s.Metrics().EventsTotal(telemetry.StatusSuccess); got != 1 {
		t.Errorf("success events = %d, want 1", got)
	}
	if got := s.Metrics().AllocatedBytes(); got != 512*mib {
		t.Errorf("allocated bytes = %d, want %d", got, 512*mib)
	}
	if got := s.Metrics().CeilingSaturationRatio(); got != 0.5 {
		t.Errorf("ceiling saturation ratio = %f, want 0.5", got)
	}

	// 2. Immediate second signal -> skipped due to cooldown
	signal(t, addr, "PRESSURE "+testToken+" 20.00")
	time.Sleep(20 * time.Millisecond)

	if got := s.Metrics().EventsTotal(telemetry.StatusSkipped); got != 1 {
		t.Errorf("skipped events = %d, want 1 (cooldown)", got)
	}

	// 3. Wait for cooldown, grow to 768MB, then 1024MB (ceiling)
	time.Sleep(60 * time.Millisecond)
	signal(t, addr, "PRESSURE "+testToken+" 25.00")
	waitForCallCount(t, api, 2)

	time.Sleep(60 * time.Millisecond)
	signal(t, addr, "PRESSURE "+testToken+" 30.00")
	waitForCallCount(t, api, 3)

	if got := s.Metrics().EventsTotal(telemetry.StatusSuccess); got != 3 {
		t.Errorf("success events = %d, want 3", got)
	}
	if got := s.Metrics().AllocatedBytes(); got != 1024*mib {
		t.Errorf("allocated bytes = %d, want %d (1024 MiB)", got, 1024*mib)
	}
	if got := s.Metrics().CeilingSaturationRatio(); got != 1.0 {
		t.Errorf("ceiling saturation ratio = %f, want 1.0", got)
	}

	// 4. Signal when already at ceiling -> skipped
	time.Sleep(60 * time.Millisecond)
	signal(t, addr, "PRESSURE "+testToken+" 40.00")
	time.Sleep(50 * time.Millisecond)

	if got := s.Metrics().EventsTotal(telemetry.StatusSkipped); got != 2 {
		t.Errorf("skipped events = %d, want 2 (1 cooldown + 1 ceiling)", got)
	}

	// 5. Test Prometheus exposition output
	prom := s.Metrics().RenderPrometheus()
	for _, expected := range []string{
		"granule_memory_hotplug_events_total{status=\"success\"} 3",
		"granule_memory_hotplug_events_total{status=\"skipped\"} 2",
		"granule_memory_hotplug_events_total{status=\"failed\"} 0",
		"granule_memory_allocated_bytes 1073741824",
		"granule_memory_ceiling_saturation_ratio 1.0000",
		"granule_memory_hotplug_latency_seconds",
	} {
		if !strings.Contains(prom, expected) {
			t.Errorf("Prometheus output missing %q\nFull output:\n%s", expected, prom)
		}
	}
}

// Server must satisfy itself with what hypervisor.APIClient offers: the
// interface exists to keep these tests off a real API socket, not to
// describe methods the real client doesn't have.
var _ Resizer = (*hypervisor.APIClient)(nil)

func TestServe_RefusesToListenWithoutAToken(t *testing.T) {
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()

	s := New(Config{Addr: addr, MaxMB: 2048, StepMB: 256}, newFakeVM(256))
	done := make(chan error, 1)
	go func() { done <- s.Serve(context.Background()) }()
	select {
	case err := <-done:
		if !errors.Is(err, errNoToken) {
			t.Errorf("Serve() error = %v, want %v", err, errNoToken)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve with no token is still running, want it to refuse at once")
	}
	if conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
		conn.Close()
		t.Errorf("something is listening on %s after Serve refused to start", addr)
	}
}

// A token that is only a prefix of the real one, or the real one with
// more after it, is as wrong as any other: the comparison is of the
// whole token, not of however much of it the guess happened to cover.
func TestServer_RejectsPartialAndExtendedTokens(t *testing.T) {
	for _, guess := range []string{testToken[:len(testToken)-1], testToken + "0", testToken[:1], strings.ToUpper(testToken)} {
		t.Run(guess, func(t *testing.T) {
			api := newFakeVM(256)
			addr, _ := startServer(t, Config{MaxMB: 2048, StepMB: 256, Cooldown: time.Hour}, api)

			signal(t, addr, "PRESSURE "+guess+" 20.00")
			time.Sleep(200 * time.Millisecond)
			if got := api.callCount(); got != 0 {
				t.Errorf("resize requested %d time(s) for token %q, want 0", got, guess)
			}
		})
	}
}

func TestServer_AcceptsALineOfExactlyMaxLineBytes(t *testing.T) {
	api := newFakeVM(256)
	addr, _ := startServer(t, Config{MaxMB: 2048, StepMB: 256, Cooldown: time.Hour}, api)

	line := "PRESSURE " + testToken + " 20.00"
	// signal adds the newline, which counts towards the cap.
	line += strings.Repeat(" ", MaxLineBytes-len(line)-1)
	signal(t, addr, line)
	waitForCallCount(t, api, 1)
}

func TestServer_RefusesALineLongerThanMaxLineBytes(t *testing.T) {
	api := newFakeVM(256)
	addr, _ := startServer(t, Config{MaxMB: 2048, StepMB: 256, Cooldown: time.Hour}, api)

	// A valid signal, but one byte over the cap: refused whole rather than
	// cut down to something that happens to parse.
	line := "PRESSURE " + testToken + " 20.00"
	line += strings.Repeat(" ", MaxLineBytes-len(line))
	signal(t, addr, line)
	time.Sleep(200 * time.Millisecond)
	if got := api.callCount(); got != 0 {
		t.Errorf("resize requested %d time(s) for a %d-byte line, want 0", got, len(line)+1)
	}
	if got := api.infoCount(); got != 0 {
		t.Errorf("vm.info read %d time(s) for an over-long line, want 0", got)
	}
}

// The point of the cap: a peer that never sends a newline is cut off
// once it has sent MaxLineBytes, rather than buffered until the read
// deadline five seconds later.
func TestServer_StopsReadingAtMaxLineBytes(t *testing.T) {
	api := newFakeVM(256)
	addr, _ := startServer(t, Config{MaxMB: 2048, StepMB: 256, Cooldown: time.Hour}, api)

	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dialing %s: %v", addr, err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte(strings.Repeat("A", 2*MaxLineBytes))); err != nil {
		t.Fatalf("writing: %v", err)
	}

	start := time.Now()
	conn.SetReadDeadline(time.Now().Add(4 * time.Second))
	var buf [1]byte
	_, err = conn.Read(buf[:])
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		t.Fatalf("server still holding the connection open after %s, want it closed as soon as the cap was reached", time.Since(start))
	}
	if err == nil {
		t.Fatal("server wrote back to an over-long signal, want the connection closed")
	}
}

// syncBuffer is a bytes.Buffer safe to hand to log.SetOutput while
// handler goroutines write to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func captureLog(t *testing.T) *syncBuffer {
	t.Helper()
	var b syncBuffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&b)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})
	return &b
}

func waitForLog(t *testing.T, b *syncBuffer, substr string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s := b.String(); strings.Contains(s, substr) {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("log never mentioned %q; it said:\n%s", substr, b.String())
	return ""
}

// What a peer sends is untrusted until its token checks out, so it is
// logged quoted: a carriage return or terminal escape in it cannot
// rewrite the line it lands on, or pass for an entry of the shim's own.
func TestServer_QuotesUnrecognizedSignalsInTheLog(t *testing.T) {
	logs := captureLog(t)
	api := newFakeVM(256)
	addr, _ := startServer(t, Config{MaxMB: 2048, StepMB: 256, Cooldown: time.Hour}, api)

	signal(t, addr, "BOGUS\x1b[2K\rmemagent: grew guest memory to 9999 MiB")
	got := waitForLog(t, logs, "unrecognized signal")

	if strings.ContainsAny(got, "\x1b\r") {
		t.Errorf("log carries the peer's raw control characters: %q", got)
	}
	if want := `unrecognized signal "BOGUS\x1b[2K\rmemagent: grew guest memory to 9999 MiB"`; !strings.Contains(got, want) {
		t.Errorf("log = %q, want it to contain %s", got, want)
	}
}

func TestServer_QuotesTheReadingInTheLog(t *testing.T) {
	logs := captureLog(t)
	api := newFakeVM(256)
	addr, _ := startServer(t, Config{MaxMB: 2048, StepMB: 256, Cooldown: time.Hour}, api)

	signal(t, addr, "PRESSURE "+testToken+" 20.00\x1b[2K")
	got := waitForLog(t, logs, "grew guest memory")
	if strings.Contains(got, "\x1b") {
		t.Errorf("log carries the reading's raw escape: %q", got)
	}
	if want := `pressure signal ("20.00\x1b[2K")`; !strings.Contains(got, want) {
		t.Errorf("log = %q, want it to contain %s", got, want)
	}
}

func TestInstallTokenCommand(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh to run the command with")
	}
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "granule-mem-agent", "token")
	line := strings.ReplaceAll(InstallTokenCommand(), TokenFile, tokenFile)
	line = strings.ReplaceAll(line, filepath.Dir(TokenFile), filepath.Dir(tokenFile))
	if line == InstallTokenCommand() {
		t.Fatalf("could not point %q at a test directory", line)
	}

	run := func(stdin string) error {
		cmd := exec.Command(sh, "-c", line)
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%v: %s", err, out)
		}
		return nil
	}

	if strings.Contains(InstallTokenCommand(), testToken) {
		t.Fatal("the command line carries a token, want it read from stdin")
	}
	if err := run(testToken + "\n"); err != nil {
		t.Fatalf("installing the token: %v", err)
	}
	got, err := os.ReadFile(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != testToken+"\n" {
		t.Errorf("token file = %q, want %q", got, testToken+"\n")
	}
	for _, p := range []string{tokenFile, filepath.Dir(tokenFile)} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s mode = %v, want nothing for group or other", p, fi.Mode().Perm())
		}
	}

	// A second install (a resume, with a new token) replaces the first.
	if err := run("second-token\n"); err != nil {
		t.Fatalf("replacing the token: %v", err)
	}
	if got, _ := os.ReadFile(tokenFile); string(got) != "second-token\n" {
		t.Errorf("token file after replacement = %q, want %q", got, "second-token\n")
	}

	// Nothing on stdin installs nothing, rather than an empty token.
	if err := run(""); err == nil {
		t.Error("installing an empty token succeeded, want it refused")
	}
	if got, _ := os.ReadFile(tokenFile); string(got) != "second-token\n" {
		t.Errorf("token file after an empty install = %q, want it left alone", got)
	}
}

func TestDeliverToken(t *testing.T) {
	var (
		mu    sync.Mutex
		calls int
		lines []string
		got   []string
	)
	run := func(_ context.Context, line string, stdin io.Reader, stderr io.Writer) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		b, _ := io.ReadAll(stdin)
		lines = append(lines, line)
		got = append(got, string(b))
		switch calls {
		case 1:
			return 0, errors.New("connecting to the guest: timed out")
		case 2:
			fmt.Fprint(stderr, "mkdir: read-only file system")
			return 1, nil
		}
		return 0, nil
	}

	if err := DeliverToken(context.Background(), run, testToken, time.Millisecond); err != nil {
		t.Fatalf("DeliverToken() = %v, want nil once an attempt succeeds", err)
	}
	if calls != 3 {
		t.Errorf("DeliverToken ran %d attempts, want 3 (two failures, then success)", calls)
	}
	for i := range lines {
		if lines[i] != InstallTokenCommand() {
			t.Errorf("attempt %d ran %q, want InstallTokenCommand()", i+1, lines[i])
		}
		if strings.Contains(lines[i], testToken) {
			t.Errorf("attempt %d put the token on the guest command line: %q", i+1, lines[i])
		}
		if got[i] != testToken+"\n" {
			t.Errorf("attempt %d sent stdin %q, want %q", i+1, got[i], testToken+"\n")
		}
	}
}

func TestDeliverToken_StopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	run := func(context.Context, string, io.Reader, io.Writer) (int, error) {
		cancel()
		return 0, errors.New("guest not up")
	}
	if err := DeliverToken(ctx, run, testToken, time.Hour); !errors.Is(err, context.Canceled) {
		t.Errorf("DeliverToken() = %v, want context.Canceled", err)
	}
}

func TestDeliverToken_RefusesAnEmptyToken(t *testing.T) {
	run := func(context.Context, string, io.Reader, io.Writer) (int, error) {
		t.Error("DeliverToken ran a guest command with no token to deliver")
		return 0, nil
	}
	if err := DeliverToken(context.Background(), run, "", time.Millisecond); err == nil {
		t.Error("DeliverToken with an empty token = nil, want an error")
	}
}

// awaitClosedByServer reports whether the server closed conn within d.
func awaitClosedByServer(conn net.Conn, d time.Duration) bool {
	conn.SetReadDeadline(time.Now().Add(d))
	_, err := conn.Read(make([]byte, 1))
	var ne net.Error
	return err != nil && !(errors.As(err, &ne) && ne.Timeout())
}

// Anything that reaches the listener can open connections and keep them,
// so it holds at most maxConns at a time and closes the rest as they
// arrive -- and recovers as soon as they are let go.
func TestServer_BoundsConcurrentConnections(t *testing.T) {
	api := newFakeVM(256)
	cfg := Config{MaxMB: 2048, StepMB: 256, Cooldown: time.Hour}
	addr, _ := startServer(t, cfg, api)
	// startServer's readiness probe took a slot for a moment; let it go.
	time.Sleep(100 * time.Millisecond)

	var held []net.Conn
	for i := 0; i < maxConns; i++ {
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			t.Fatalf("dialing %s: %v", addr, err)
		}
		held = append(held, conn)
	}
	time.Sleep(100 * time.Millisecond)

	extra, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dialing %s: %v", addr, err)
	}
	defer extra.Close()
	if !awaitClosedByServer(extra, time.Second) {
		t.Errorf("connection %d was held open, want it closed while %d others are", maxConns+1, maxConns)
	}

	for _, c := range held {
		c.Close()
	}
	time.Sleep(100 * time.Millisecond)
	signal(t, addr, "PRESSURE "+testToken+" 50")
	waitForCallCount(t, api, 1)
}
