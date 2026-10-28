package main

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/bwsalmon/granule/pkg/memagent"
)

func writeFixture(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadPressure(t *testing.T) {
	content := "some avg10=12.34 avg60=5.67 avg300=1.00 total=123456\n" +
		"full avg10=8.90 avg60=2.00 avg300=0.50 total=98765\n"
	path := writeFixture(t, content)

	got, err := readPressure(path)
	if err != nil {
		t.Fatalf("readPressure() error = %v", err)
	}
	if got != 12.34 {
		t.Errorf("readPressure() = %v, want 12.34", got)
	}
}

func TestReadPressure_Zero(t *testing.T) {
	content := "some avg10=0.00 avg60=0.00 avg300=0.00 total=0\n" +
		"full avg10=0.00 avg60=0.00 avg300=0.00 total=0\n"
	path := writeFixture(t, content)

	got, err := readPressure(path)
	if err != nil {
		t.Fatalf("readPressure() error = %v", err)
	}
	if got != 0 {
		t.Errorf("readPressure() = %v, want 0", got)
	}
}

func TestReadPressure_MissingFile(t *testing.T) {
	if _, err := readPressure(filepath.Join(t.TempDir(), "nonexistent")); err == nil {
		t.Fatal("expected error for a nonexistent path, got nil")
	}
}

func TestReadPressure_MalformedContent(t *testing.T) {
	path := writeFixture(t, "not what /proc/pressure/memory looks like\n")
	if _, err := readPressure(path); err == nil {
		t.Fatal("expected error for malformed content, got nil")
	}
}

func TestReadPSI(t *testing.T) {
	content := "some avg10=12.34 avg60=5.67 avg300=0.00 total=123456\n" +
		"full avg10=8.90 avg60=2.00 avg300=0.00 total=98765\n"
	path := writeFixture(t, content)

	stats, err := readPSI(path)
	if err != nil {
		t.Fatalf("readPSI() error = %v", err)
	}
	if stats.Avg10 != 12.34 || stats.Avg60 != 5.67 || stats.Avg300 != 0.00 {
		t.Errorf("readPSI() = %+v, want avg10=12.34 avg60=5.67 avg300=0.00", stats)
	}
}

func TestReadMemInfo(t *testing.T) {
	content := "MemTotal:        2048000 kB\n" +
		"MemFree:         1000000 kB\n" +
		"MemAvailable:    1536000 kB\n" +
		"Buffers:           50000 kB\n" +
		"Cached:           486000 kB\n"
	path := writeFixture(t, content)

	info, err := readMemInfo(path)
	if err != nil {
		t.Fatalf("readMemInfo() error = %v", err)
	}
	if info.TotalKB != 2048000 {
		t.Errorf("TotalKB = %d, want 2048000", info.TotalKB)
	}
	if info.AvailableKB != 1536000 {
		t.Errorf("AvailableKB = %d, want 1536000", info.AvailableKB)
	}
	if info.AvailableMB() != 1500 {
		t.Errorf("AvailableMB() = %d, want 1500", info.AvailableMB())
	}
	if percent := info.AvailablePercent(); percent != 75.0 {
		t.Errorf("AvailablePercent() = %f, want 75.0", percent)
	}
}

func TestReadMemInfo_FallbackWithoutMemAvailable(t *testing.T) {
	content := "MemTotal:        1000000 kB\n" +
		"MemFree:          200000 kB\n" +
		"Buffers:          100000 kB\n" +
		"Cached:           400000 kB\n"
	path := writeFixture(t, content)

	info, err := readMemInfo(path)
	if err != nil {
		t.Fatalf("readMemInfo() error = %v", err)
	}
	if info.AvailableKB != 700000 {
		t.Errorf("AvailableKB fallback = %d, want 700000 (200000+100000+400000)", info.AvailableKB)
	}
	if percent := info.AvailablePercent(); percent != 70.0 {
		t.Errorf("AvailablePercent() = %f, want 70.0", percent)
	}
}

func TestDefaultGateway(t *testing.T) {
	// A real /proc/net/route sample for a guest at 169.254.100.2/24 whose
	// default gateway is 169.254.100.1: fields are tab-separated, address
	// fields are 4-byte hex in the kernel's little-endian printed order
	// (see hexLEToIPv4's own doc comment). "0164FEA9" decodes, byte by
	// byte reversed, to A9.FE.64.01 = 169.254.100.1.
	content := "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n" +
		"eth0\t0064FEA9\t00000000\t0001\t0\t0\t0\t00FFFFFF\t0\t0\t0\n" +
		"eth0\t00000000\t0164FEA9\t0003\t0\t0\t0\t00000000\t0\t0\t0\n"
	path := writeFixture(t, content)

	gw, err := defaultGateway(path)
	if err != nil {
		t.Fatalf("defaultGateway() error = %v", err)
	}
	if want := net.ParseIP("169.254.100.1"); !gw.Equal(want) {
		t.Errorf("defaultGateway() = %v, want %v", gw, want)
	}
}

func TestDefaultGateway_MissingFile(t *testing.T) {
	if _, err := defaultGateway(filepath.Join(t.TempDir(), "nonexistent")); err == nil {
		t.Fatal("expected error for a nonexistent route table, got nil")
	}
}

func TestDefaultGateway_NoDefaultRoute(t *testing.T) {
	content := "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n" +
		"eth0\t0064FEA9\t00000000\t0001\t0\t0\t0\t00FFFFFF\t0\t0\t0\n"
	path := writeFixture(t, content)

	if _, err := defaultGateway(path); err == nil {
		t.Fatal("expected error when no default route is present, got nil")
	}
}

func TestHexLEToIPv4_InvalidInput(t *testing.T) {
	for _, bad := range []string{"not-hex", "1234", "1234567890"} {
		if _, err := hexLEToIPv4(bad); err == nil {
			t.Errorf("hexLEToIPv4(%q) = nil error, want an error", bad)
		}
	}
}

// The guest half has to look where the host half writes: the two ship
// in one image, and this is the only thing tying them together.
func TestTokenFileMatchesTheHostSide(t *testing.T) {
	if tokenFile != memagent.TokenFile {
		t.Errorf("tokenFile = %q, but pkg/memagent delivers the token to %q", tokenFile, memagent.TokenFile)
	}
}

func TestReadToken(t *testing.T) {
	for _, tc := range []struct {
		name, content, want string
	}{
		{"plain", "tok-file-123", "tok-file-123"},
		{"newline", "tok-file-123\n", "tok-file-123"},
		{"crlf", "tok-file-123\r\n", "tok-file-123"},
		{"first line only", "tok-file-123\nsomething else\n", "tok-file-123"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := readToken(writeFixture(t, tc.content))
			if err != nil || got != tc.want {
				t.Errorf("readToken() = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
	for _, tc := range []struct{ name, content string }{
		{"empty", ""},
		{"blank line", "\n"},
		{"two words", "tok 123\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := readToken(writeFixture(t, tc.content)); err == nil {
				t.Errorf("readToken() = %q, want an error", got)
			}
		})
	}
	t.Run("missing", func(t *testing.T) {
		if got, err := readToken(filepath.Join(t.TempDir(), "nope")); err == nil {
			t.Errorf("readToken() = %q, want an error", got)
		}
	})
}

func TestTokenSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	src := &tokenSource{path: path}

	if got := src.Get(); got != "" {
		t.Errorf("Get() before delivery = %q, want empty", got)
	}
	if err := os.WriteFile(path, []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := src.Get(); got != "first" {
		t.Errorf("Get() after delivery = %q, want %q", got, "first")
	}
	// A resume delivers a new token over the old one; the next signal
	// carries it without the agent restarting.
	if err := os.WriteFile(path, []byte("second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := src.Get(); got != "second" {
		t.Errorf("Get() after redelivery = %q, want %q", got, "second")
	}

	override := &tokenSource{override: "from-env", path: path}
	if got := override.Get(); got != "from-env" {
		t.Errorf("Get() with an override = %q, want %q", got, "from-env")
	}
}

// Without a token the host refuses the signal anyway, so the agent does
// not send one at all.
func TestSignal_WithoutATokenSendsNothing(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)

	accepted := make(chan struct{}, 2)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
			accepted <- struct{}{}
		}
	}()

	signalPressure(host, port, 42.5, "")
	signalLowPressure(host, port, 1536, "")

	select {
	case <-accepted:
		t.Error("the agent connected to the host with no token to send")
	case <-time.After(300 * time.Millisecond):
	}
}

func TestSignalPressure_WithToken(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	host, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("SplitHostPort: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("Atoi: %v", err)
	}

	got := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			got <- ""
			return
		}
		defer conn.Close()
		line, _ := bufio.NewReader(conn).ReadString('\n')
		got <- line
	}()

	signalPressure(host, port, 42.5, "my-secret-token")

	select {
	case line := <-got:
		if want := "PRESSURE my-secret-token 42.50\n"; line != want {
			t.Errorf("received %q, want %q", line, want)
		}
	case <-time.After(3 * time.Second):
		t.Errorf("nothing arrived on the listener")
	}
}

func TestSignalLowPressure_WithToken(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	host, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("SplitHostPort: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("Atoi: %v", err)
	}

	got := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			got <- ""
			return
		}
		defer conn.Close()
		line, _ := bufio.NewReader(conn).ReadString('\n')
		got <- line
	}()

	signalLowPressure(host, port, 1536, "my-secret-token")

	select {
	case line := <-got:
		if want := "PRESSURE_LOW my-secret-token 1536\n"; line != want {
			t.Errorf("received %q, want %q", line, want)
		}
	case <-time.After(3 * time.Second):
		t.Errorf("nothing arrived on the listener")
	}
}

func TestMonitorState_ScaleUpTrigger(t *testing.T) {
	cfg := MonitorConfig{
		Threshold:           10.0,
		LowPercent:          60.0,
		StabilizationWindow: 5 * time.Minute,
	}

	var state MonitorState
	var highCalled bool
	var highVal float64
	var lowCalled bool

	now := time.Now()
	psi := PSIStats{Avg10: 15.0, Avg60: 5.0, Avg300: 1.0}
	mem := MemInfo{TotalKB: 2048000, AvailableKB: 500000} // ~24% available

	state.Poll(now, psi, mem, cfg,
		func(val float64) {
			highCalled = true
			highVal = val
		},
		func(availMB int) {
			lowCalled = true
		},
	)

	if !highCalled {
		t.Error("expected high pressure signal to be called")
	}
	if highVal != 15.0 {
		t.Errorf("got highVal = %f, want 15.0", highVal)
	}
	if lowCalled {
		t.Error("low pressure signal should not be called")
	}
}

func TestMonitorState_ScaleDownStabilizationWindow(t *testing.T) {
	cfg := MonitorConfig{
		Threshold:           10.0,
		LowPercent:          60.0,
		StabilizationWindow: 5 * time.Minute,
	}

	var state MonitorState
	psi := PSIStats{Avg10: 0.0, Avg60: 0.0, Avg300: 0.0}
	mem := MemInfo{TotalKB: 2048000, AvailableKB: 1536000} // 75% available

	var lowCount int
	var lowVal int
	onLow := func(val int) {
		lowCount++
		lowVal = val
	}

	start := time.Now()

	// 1. Initial idle sample: starts stabilization window, no signal yet
	state.Poll(start, psi, mem, cfg, nil, onLow)
	if lowCount != 0 {
		t.Errorf("signal fired prematurely on 1st sample, count = %d", lowCount)
	}

	// 2. Sample at 3 minutes: still within window, no signal
	state.Poll(start.Add(3*time.Minute), psi, mem, cfg, nil, onLow)
	if lowCount != 0 {
		t.Errorf("signal fired prematurely at 3m, count = %d", lowCount)
	}

	// 3. Sample at 5 minutes: stabilization window reached, signal fires
	state.Poll(start.Add(5*time.Minute), psi, mem, cfg, nil, onLow)
	if lowCount != 1 {
		t.Errorf("signal did not fire at 5m, count = %d", lowCount)
	}
	if lowVal != 1500 {
		t.Errorf("lowVal = %d, want 1500", lowVal)
	}

	// 4. Sample at 6 minutes: window reset after firing, should not fire again immediately
	state.Poll(start.Add(6*time.Minute), psi, mem, cfg, nil, onLow)
	if lowCount != 1 {
		t.Errorf("signal fired repeatedly without waiting for next window, count = %d", lowCount)
	}

	// 5. Sample at 10 minutes (5m after previous signal): fires second low pressure signal
	state.Poll(start.Add(10*time.Minute), psi, mem, cfg, nil, onLow)
	if lowCount != 2 {
		t.Errorf("second signal did not fire at 10m, count = %d", lowCount)
	}
}

func TestMonitorState_StabilizationResetOnStrain(t *testing.T) {
	cfg := MonitorConfig{
		Threshold:           10.0,
		LowPercent:          60.0,
		StabilizationWindow: 5 * time.Minute,
	}

	var state MonitorState
	idlePSI := PSIStats{Avg10: 0.0, Avg60: 0.0, Avg300: 0.0}
	idleMem := MemInfo{TotalKB: 2048000, AvailableKB: 1536000} // 75% available

	strainedPSI := PSIStats{Avg10: 12.0, Avg60: 5.0, Avg300: 2.0}

	var lowCount int
	onLow := func(val int) { lowCount++ }

	start := time.Now()

	// 1. Idle at t=0
	state.Poll(start, idlePSI, idleMem, cfg, nil, onLow)

	// 2. Idle at t=4m
	state.Poll(start.Add(4*time.Minute), idlePSI, idleMem, cfg, nil, onLow)

	// 3. Transient strain at t=4m30s -> resets idle window
	state.Poll(start.Add(4*time.Minute+30*time.Second), strainedPSI, idleMem, cfg, nil, onLow)

	// 4. Back to idle at t=5m: only 30s since strain, must not fire!
	state.Poll(start.Add(5*time.Minute), idlePSI, idleMem, cfg, nil, onLow)
	if lowCount != 0 {
		t.Errorf("expected lowCount = 0 after strain reset, got %d", lowCount)
	}

	// 5. Idle maintained until t=10m (5m continuous after strain) -> fires
	state.Poll(start.Add(10*time.Minute), idlePSI, idleMem, cfg, nil, onLow)
	if lowCount != 1 {
		t.Errorf("expected lowCount = 1 after full window post-strain, got %d", lowCount)
	}
}

func TestEnvHelpers(t *testing.T) {
	t.Setenv("TEST_DUR_GOOD", "5s")
	t.Setenv("TEST_DUR_BAD", "bad-dur")
	if d := envDuration("TEST_DUR_GOOD", time.Minute); d != 5*time.Second {
		t.Errorf("envDuration good = %v, want 5s", d)
	}
	if d := envDuration("TEST_DUR_BAD", time.Minute); d != time.Minute {
		t.Errorf("envDuration bad = %v, want 1m fallback", d)
	}
	if d := envDuration("TEST_DUR_UNSET", time.Minute); d != time.Minute {
		t.Errorf("envDuration unset = %v, want 1m fallback", d)
	}

	t.Setenv("TEST_FLOAT_GOOD", "42.5")
	t.Setenv("TEST_FLOAT_BAD", "not-a-number")
	if f := envFloat("TEST_FLOAT_GOOD", 10.0); f != 42.5 {
		t.Errorf("envFloat good = %v, want 42.5", f)
	}
	if f := envFloat("TEST_FLOAT_BAD", 10.0); f != 10.0 {
		t.Errorf("envFloat bad = %v, want 10.0 default", f)
	}

	t.Setenv("TEST_INT_GOOD", "8080")
	t.Setenv("TEST_INT_BAD", "not-an-int")
	if i := envInt("TEST_INT_GOOD", 80); i != 8080 {
		t.Errorf("envInt good = %v, want 8080", i)
	}
	if i := envInt("TEST_INT_BAD", 80); i != 80 {
		t.Errorf("envInt bad = %v, want 80 default", i)
	}

	t.Setenv("TEST_STR_GOOD", "some 100000 1000000")
	t.Setenv("TEST_STR_EMPTY", "")
	if s := envString("TEST_STR_GOOD", "default"); s != "some 100000 1000000" {
		t.Errorf("envString good = %q, want %q", s, "some 100000 1000000")
	}
	if s := envString("TEST_STR_EMPTY", "default"); s != "default" {
		t.Errorf("envString empty = %q, want %q", s, "default")
	}
	if s := envString("TEST_STR_UNSET", "default"); s != "default" {
		t.Errorf("envString unset = %q, want %q", s, "default")
	}
}

func TestSignalPressure_UnreachableHost(t *testing.T) {
	// Should log error and not panic
	signalPressure("127.0.0.1", 59999, 50.0, "tok")
	signalLowPressure("127.0.0.1", 59999, 1024, "tok")
}

func TestRegisterTrigger_MissingFile(t *testing.T) {
	_, err := registerTrigger(filepath.Join(t.TempDir(), "nonexistent"), "some 150000 1000000")
	if err == nil {
		t.Fatal("expected error registering trigger on nonexistent file, got nil")
	}
}

func TestRegisterTrigger_RegularFile(t *testing.T) {
	// Regular files do not support epoll, so epoll_ctl should fail with EPERM.
	path := writeFixture(t, "not a psi file\n")
	_, err := registerTrigger(path, "some 150000 1000000")
	if err == nil {
		t.Fatal("expected error registering trigger on regular file, got nil")
	}
}

func TestPsiTrigger_Close(t *testing.T) {
	tr := &psiTrigger{file: nil, epfd: -1}
	if err := tr.Close(); err != nil {
		t.Fatalf("Close() on empty trigger error = %v", err)
	}
	// Calling Close again should be idempotent and return nil.
	if err := tr.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}

func TestPsiTrigger_LiveKernelTrigger(t *testing.T) {
	if _, err := os.Stat(pressurePath); err != nil {
		t.Skipf("skipping live PSI test: %s not available (%v)", pressurePath, err)
	}

	tr, err := registerTrigger(pressurePath, "some 150000 1000000")
	if err != nil {
		t.Skipf("skipping live PSI test: trigger registration failed (likely unprivileged): %v", err)
	}
	defer tr.Close()

	// Wait with a small timeout (5ms); should time out with false, nil.
	triggered, err := tr.Wait(5 * time.Millisecond)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if triggered {
		t.Logf("Wait() returned true unexpectedly on idle system, but is valid PSI event")
	}
}
