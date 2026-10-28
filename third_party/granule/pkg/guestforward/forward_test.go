package guestforward

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeGuest stands in for the shim and the guest's agent: every
// transport it hands out is one end of a pipe whose other end
// ServeAgent is serving. kill drops every transport, the way a kubelet
// restart drops an exec stream.
type fakeGuest struct {
	mu    sync.Mutex
	conns []net.Conn
	dials int
	fail  error
}

func (g *fakeGuest) transport(ctx context.Context) (io.ReadWriteCloser, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.dials++
	if g.fail != nil {
		return nil, g.fail
	}
	a, b := net.Pipe()
	g.conns = append(g.conns, b)
	go func() {
		_ = ServeAgent(context.Background(), b, AgentOptions{})
		b.Close()
	}()
	return a, nil
}

func (g *fakeGuest) kill() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, c := range g.conns {
		c.Close()
	}
	g.conns = nil
}

func (g *fakeGuest) dialCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.dials
}

// echoListener is a TCP echo service on loopback: the "service" on
// whichever side a test needs one.
func echoListener(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	return ln.Addr().String()
}

// roundTrip sends data to addr, half-closes, and returns everything read
// back.
func roundTrip(addr string, data []byte) ([]byte, error) {
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(30 * time.Second))
	errc := make(chan error, 1)
	go func() {
		_, err := c.Write(data)
		if err == nil {
			err = c.(*net.TCPConn).CloseWrite()
		}
		errc <- err
	}()
	got, err := io.ReadAll(c)
	if werr := <-errc; werr != nil {
		return got, werr
	}
	return got, err
}

func startForwarder(t *testing.T, g *fakeGuest, opts Options) *Forwarder {
	t.Helper()
	opts.Transport = g.transport
	if opts.MinBackoff == 0 {
		opts.MinBackoff = 10 * time.Millisecond
	}
	if opts.ReadyTimeout == 0 {
		opts.ReadyTimeout = 5 * time.Second
	}
	opts.Logf = t.Logf
	f, err := Start(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func binding(t *testing.T, f *Forwarder, direction string) string {
	t.Helper()
	for _, b := range f.Bindings() {
		if b.Direction == direction {
			return b.Listen
		}
	}
	t.Fatalf("no %s binding in %+v", direction, f.Bindings())
	return ""
}

// Both directions at once: a caller-side service reached through a
// listener in the "guest", and a "guest" service reached through a
// caller-side port. The fake guest is this process, so both ends are
// loopback here; what is under test is the plumbing between them.
func TestForwardBothDirections(t *testing.T) {
	callerSvc := echoListener(t)
	guestSvc := echoListener(t)
	g := &fakeGuest{}
	f := startForwarder(t, g, Options{
		Remote: []RemoteForward{{Guest: "127.0.0.1:0", Target: callerSvc}},
		Local:  []LocalForward{{Listen: "127.0.0.1:0", Guest: guestSvc}},
	})

	for _, dir := range []string{"remote", "local"} {
		addr := binding(t, f, dir)
		if strings.HasSuffix(addr, ":0") {
			t.Fatalf("%s binding %s reports the port asked for, not the one bound", dir, addr)
		}
		got, err := roundTrip(addr, []byte("hello "+dir))
		if err != nil || string(got) != "hello "+dir {
			t.Fatalf("%s: got %q, %v", dir, got, err)
		}
	}
}

func TestForwardManyConcurrentConnections(t *testing.T) {
	svc := echoListener(t)
	g := &fakeGuest{}
	f := startForwarder(t, g, Options{
		Remote: []RemoteForward{{Guest: "127.0.0.1:0", Target: svc}},
		Local:  []LocalForward{{Listen: "127.0.0.1:0", Guest: svc}},
	})
	var wg sync.WaitGroup
	errs := make(chan error, 200)
	for _, dir := range []string{"remote", "local"} {
		addr := binding(t, f, dir)
		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				data := []byte(fmt.Sprintf("%s-%d-%s", dir, i, strings.Repeat("x", i*100)))
				got, err := roundTrip(addr, data)
				if err != nil {
					errs <- err
				} else if !bytes.Equal(got, data) {
					errs <- fmt.Errorf("%s %d: echo mismatch", dir, i)
				}
			}(i)
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestForwardLargeTransfer(t *testing.T) {
	svc := echoListener(t)
	g := &fakeGuest{}
	f := startForwarder(t, g, Options{Local: []LocalForward{{Listen: "127.0.0.1:0", Guest: svc}}})
	data := make([]byte, 32<<20)
	_, _ = rand.Read(data)
	got, err := roundTrip(binding(t, f, "local"), data)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(got) != sha256.Sum256(data) {
		t.Fatalf("got %d bytes back, want %d identical", len(got), len(data))
	}
}

// Killing the transport drops what was in flight, and with Reconnect the
// forwarder comes back on its own -- at the same guest-side address it
// reported, even though that one was asked for as port 0.
func TestForwardReconnect(t *testing.T) {
	svc := echoListener(t)
	g := &fakeGuest{}
	f := startForwarder(t, g, Options{
		Reconnect: true,
		Remote:    []RemoteForward{{Guest: "127.0.0.1:0", Target: svc}},
		Local:     []LocalForward{{Listen: "127.0.0.1:0", Guest: svc}},
	})
	remote := binding(t, f, "remote")
	local := binding(t, f, "local")

	// A connection open across the kill is torn down rather than hung.
	held, err := net.Dial("tcp", local)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if _, err := held.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1)
	if _, err := io.ReadFull(held, buf); err != nil {
		t.Fatal(err)
	}

	g.kill()
	_ = held.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := held.Read(buf); err == nil {
		t.Fatal("a connection across the transport dropping is still open")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("a connection across the transport dropping hung instead of closing")
	}

	waitFor(t, func() bool { return g.dialCount() >= 2 })
	if got := binding(t, f, "remote"); got != remote {
		t.Fatalf("remote binding moved from %s to %s across a reconnect", remote, got)
	}
	// The local listener never went away; the remote one is back.
	for _, addr := range []string{local, remote} {
		var got []byte
		var err error
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if got, err = roundTrip(addr, []byte("again")); err == nil && string(got) == "again" {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if err != nil || string(got) != "again" {
			t.Fatalf("%s after reconnect: %q, %v", addr, got, err)
		}
	}
}

func TestForwardEndsWithoutReconnect(t *testing.T) {
	svc := echoListener(t)
	g := &fakeGuest{}
	f := startForwarder(t, g, Options{Local: []LocalForward{{Listen: "127.0.0.1:0", Guest: svc}}})
	local := binding(t, f, "local")
	g.kill()
	select {
	case <-f.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("forwarder outlived its transport without Reconnect")
	}
	if err := f.Wait(); err == nil {
		t.Fatal("Wait = nil after the transport dropped")
	}
	if c, err := net.Dial("tcp", local); err == nil {
		c.Close()
		t.Fatal("local listener still bound after the forwarder ended")
	}
}

func TestStartFailsClearly(t *testing.T) {
	g := &fakeGuest{fail: errors.New("VM \"web\" is not running")}
	_, err := Start(context.Background(), Options{
		Transport: g.transport,
		Local:     []LocalForward{{Listen: "127.0.0.1:0", Guest: "80"}},
	})
	if err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("Start = %v, want the transport's own reason", err)
	}
}

// A transport that starts and then never answers -- a shim still waiting
// for a guest that will not boot -- times out with a message saying so.
func TestStartTimesOutOnSilentAgent(t *testing.T) {
	_, err := Start(context.Background(), Options{
		Transport: func(context.Context) (io.ReadWriteCloser, error) {
			a, b := net.Pipe()
			go func() { _, _ = io.Copy(io.Discard, b) }()
			return a, nil
		},
		ReadyTimeout: 200 * time.Millisecond,
		Local:        []LocalForward{{Listen: "127.0.0.1:0", Guest: "80"}},
	})
	if err == nil || !strings.Contains(err.Error(), "did not answer") {
		t.Fatalf("Start = %v", err)
	}
}

func TestGuestListenersAreLoopbackOnly(t *testing.T) {
	g := &fakeGuest{}
	_, err := Start(context.Background(), Options{
		Transport:    g.transport,
		ReadyTimeout: 5 * time.Second,
		Remote:       []RemoteForward{{Guest: "0.0.0.0:0", Target: "80"}},
	})
	if err == nil || !strings.Contains(err.Error(), "loopback-only") {
		t.Fatalf("Start = %v, want a loopback-only refusal", err)
	}

	f := startForwarder(t, g, Options{Remote: []RemoteForward{{Guest: "0.0.0.0:0", Target: "80", AnyAddr: true}}})
	if b := binding(t, f, "remote"); !strings.HasPrefix(b, "0.0.0.0:") && !strings.HasPrefix(b, "[::]:") {
		t.Fatalf("AnyAddr binding = %s", b)
	}
}

func TestAgentListenerCap(t *testing.T) {
	a, b := net.Pipe()
	go func() { _ = ServeAgent(context.Background(), b, AgentOptions{MaxListeners: 1}) }()
	sess := NewSession(a, true, Config{})
	defer sess.Close()
	ctx := ctxT(t)
	if _, err := sess.Open(ctx, encode(openMsg{Op: opListen, Addr: "127.0.0.1:0"})); err != nil {
		t.Fatal(err)
	}
	_, err := sess.Open(ctx, encode(openMsg{Op: opListen, Addr: "127.0.0.1:0"}))
	if err == nil || !strings.Contains(err.Error(), "too many listeners") {
		t.Fatalf("second listen = %v", err)
	}
}

// A guest-side listener goes with the session that asked for it.
func TestAgentListenerDiesWithSession(t *testing.T) {
	a, b := net.Pipe()
	served := make(chan struct{})
	go func() {
		_ = ServeAgent(context.Background(), b, AgentOptions{})
		close(served)
	}()
	sess := NewSession(a, true, Config{})
	st, err := sess.Open(ctxT(t), encode(openMsg{Op: opListen, Addr: "127.0.0.1:0"}))
	if err != nil {
		t.Fatal(err)
	}
	addr := strings.Trim(strings.TrimPrefix(string(st.Reply()), `{"addr":`), `"}`)
	if c, err := net.Dial("tcp", addr); err != nil {
		t.Fatalf("guest listener %s not up: %v", addr, err)
	} else {
		c.Close()
	}
	sess.Close()
	<-served
	waitFor(t, func() bool {
		c, err := net.Dial("tcp", addr)
		if err == nil {
			c.Close()
		}
		return err != nil
	})
}

func TestAgentConnectRefusedIsRejected(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := ln.Addr().String()
	ln.Close()
	a, b := net.Pipe()
	go func() { _ = ServeAgent(context.Background(), b, AgentOptions{}) }()
	sess := NewSession(a, true, Config{})
	defer sess.Close()
	_, err := sess.Open(ctxT(t), encode(openMsg{Op: opConnect, Addr: dead}))
	var rej *RejectedError
	if !errors.As(err, &rej) || !strings.Contains(rej.Reason, "dialing") {
		t.Fatalf("connect to a closed port = %v", err)
	}
}

func TestParseAddr(t *testing.T) {
	for _, tc := range []struct{ in, network, addr string }{
		{"9728", "tcp", "127.0.0.1:9728"},
		{"0", "tcp", "127.0.0.1:0"},
		{"localhost:80", "tcp", "localhost:80"},
		{"[::1]:80", "tcp", "[::1]:80"},
		{"unix:/run/x.sock", "unix", "/run/x.sock"},
	} {
		n, a, err := ParseAddr(tc.in)
		if err != nil || n != tc.network || a != tc.addr {
			t.Errorf("ParseAddr(%q) = %q, %q, %v", tc.in, n, a, err)
		}
	}
	for _, bad := range []string{"", "unix:", "host", "host:x", "70000", "host:70000"} {
		if _, _, err := ParseAddr(bad); err == nil {
			t.Errorf("ParseAddr(%q) succeeded", bad)
		}
	}
}

func TestIsLoopback(t *testing.T) {
	for in, want := range map[string]bool{
		"127.0.0.1:1": true, "127.0.0.2:1": true, "[::1]:1": true, "localhost:1": true,
		"0.0.0.0:1": false, "10.0.0.1:1": false, "[::]:1": false, "example.com:1": false,
	} {
		if got := isLoopback("tcp", in); got != want {
			t.Errorf("isLoopback(%q) = %v", in, got)
		}
	}
	if !isLoopback("unix", "/x") {
		t.Error("a unix socket is not reachable over a network")
	}
}

func TestKubectlArgs(t *testing.T) {
	got := strings.Join(Kubectl{Context: "gke", Namespace: "sb", Pod: "vm-1", Container: "vm"}.Args(), " ")
	want := "--context gke exec -i -n sb vm-1 -c vm -- granule forward-stdio"
	if got != want {
		t.Fatalf("args = %q, want %q", got, want)
	}
}

// CommandTransport's error names the command and carries its stderr, so
// "no such container" reaches whoever is reading the forwarder's error.
func TestCommandTransportReportsStderr(t *testing.T) {
	conn, err := CommandTransport("sh", "-c", "echo 'Error: No such container: vm-web' >&2; exit 1")(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, err = io.ReadAll(conn)
	if err == nil || !strings.Contains(err.Error(), "No such container") || !errors.Is(err, io.EOF) {
		t.Fatalf("read error = %v", err)
	}
}

// The command transport end to end: a "container" that is just this
// test binary's agent relayed over a subprocess's stdio would need a
// binary; cat stands in for the relay, echoing frames straight back,
// which is enough to show bytes move both ways through the pipes.
func TestCommandTransportCarriesBytes(t *testing.T) {
	conn, err := CommandTransport("cat")(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("round")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "round" {
		t.Fatalf("read %q, %v", buf, err)
	}
	done := make(chan struct{})
	go func() { conn.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not end the command")
	}
}
