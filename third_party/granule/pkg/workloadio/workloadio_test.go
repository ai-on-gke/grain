package workloadio

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/bwsalmon/granule/pkg/execwire"
)

type received struct {
	typ     byte
	payload string
}

// attach serves broker on one end of a pipe and returns the other, past
// the opening line, with the opening line checked.
func attach(t *testing.T, b *Broker) (net.Conn, *bufio.Reader) {
	t.Helper()
	host, guest := net.Pipe()
	t.Cleanup(func() { host.Close() })
	go b.Serve(guest)
	br := bufio.NewReader(host)
	resp, err := ReadOpening(br)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Supports(FeatureStreams) {
		t.Fatalf("opening line %+v does not name %s", resp, FeatureStreams)
	}
	return host, br
}

func readFrames(t *testing.T, br *bufio.Reader, n int) []received {
	t.Helper()
	var got []received
	for len(got) < n {
		typ, payload, err := execwire.ReadFrame(br)
		if err != nil {
			t.Fatalf("after %v: %v", got, err)
		}
		got = append(got, received{typ, string(payload)})
	}
	return got
}

func TestBrokerSendsBacklogThenLiveOutput(t *testing.T) {
	b := NewBroker(0)
	io.WriteString(b.Stdout(), "early out\n")
	io.WriteString(b.Stderr(), "early err\n")

	_, br := attach(t, b)
	go io.WriteString(b.Stdout(), "live out\n")
	got := readFrames(t, br, 3)
	want := []received{
		{execwire.TypeStdout, "early out\n"},
		{execwire.TypeStderr, "early err\n"},
		{execwire.TypeStdout, "live out\n"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("frames = %v, want %v", got, want)
	}
}

// Past its limit the backlog drops its oldest output, and says how much.
func TestBrokerBacklogDropsOldest(t *testing.T) {
	b := NewBroker(10)
	io.WriteString(b.Stdout(), "aaaaaa")
	io.WriteString(b.Stdout(), "bbbbbb")
	io.WriteString(b.Stderr(), "cc")

	_, br := attach(t, b)
	got := readFrames(t, br, 3)
	if got[0].typ != execwire.TypeNote || !strings.Contains(got[0].payload, "6 bytes") {
		t.Errorf("first frame = %v, want a note that 6 bytes were dropped", got[0])
	}
	want := []received{{execwire.TypeStdout, "bbbbbb"}, {execwire.TypeStderr, "cc"}}
	if fmt.Sprint(got[1:]) != fmt.Sprint(want) {
		t.Errorf("frames = %v, want %v", got[1:], want)
	}
}

// Output larger than one frame can carry is split rather than refused.
func TestBrokerSplitsLargeWrites(t *testing.T) {
	b := NewBroker(0)
	big := bytes.Repeat([]byte("x"), execwire.MaxPayload+10)
	if n, err := b.Stdout().Write(big); n != len(big) || err != nil {
		t.Fatalf("Write = %d, %v", n, err)
	}
	_, br := attach(t, b)
	got := readFrames(t, br, 2)
	if len(got[0].payload) != execwire.MaxPayload || len(got[1].payload) != 10 {
		t.Errorf("frame sizes = %d, %d", len(got[0].payload), len(got[1].payload))
	}
}

// A second connection takes the first one's place, and output written
// while neither was attached is not lost in between.
func TestBrokerReattach(t *testing.T) {
	b := NewBroker(0)
	first, br1 := attach(t, b)
	go io.WriteString(b.Stdout(), "one")
	readFrames(t, br1, 1)

	first.Close()
	// The broker notices the hang-up on its own reader, or on the next
	// write; either way the write lands in the backlog.
	waitFor(t, func() bool { b.mu.Lock(); defer b.mu.Unlock(); return b.conn == nil })
	io.WriteString(b.Stdout(), "two")

	_, br2 := attach(t, b)
	if got := readFrames(t, br2, 1); got[0].payload != "two" {
		t.Errorf("after reattach got %v, want two", got)
	}

	_, br3 := attach(t, b)
	go io.WriteString(b.Stderr(), "three")
	if got := readFrames(t, br3, 1); got[0].payload != "three" {
		t.Errorf("replacement got %v, want three", got)
	}
	if _, _, err := execwire.ReadFrame(br2); err == nil {
		t.Error("the replaced connection was not hung up on")
	}
}

// Close waits for a host to collect the backlog, then hangs up on it.
func TestBrokerCloseWaitsForDelivery(t *testing.T) {
	b := NewBroker(0)
	io.WriteString(b.Stdout(), "last words")

	closed := make(chan bool, 1)
	go func() { closed <- b.Close(5 * time.Second) }()
	time.Sleep(20 * time.Millisecond)

	_, br := attach(t, b)
	readFrames(t, br, 1)
	if _, _, err := execwire.ReadFrame(br); !errors.Is(err, io.EOF) {
		t.Errorf("after the backlog: %v, want the broker to hang up", err)
	}
	if !<-closed {
		t.Error("Close reported the output undelivered")
	}
	if n, err := io.WriteString(b.Stdout(), "after"); n != 5 || err != nil {
		t.Errorf("a write after Close = %d, %v; want it accepted and dropped", n, err)
	}
}

func TestBrokerCloseWithNobodyAttached(t *testing.T) {
	b := NewBroker(0)
	io.WriteString(b.Stdout(), "unheard")
	start := time.Now()
	if b.Close(0) {
		t.Error("Close reported output delivered with nothing attached")
	}
	if time.Since(start) > time.Second {
		t.Error("Close(0) waited")
	}
	host, guest := net.Pipe()
	defer host.Close()
	if err := b.Serve(guest); err == nil {
		t.Error("a connection after Close was served")
	}
}

func TestRequested(t *testing.T) {
	for cmdline, want := range map[string]bool{
		"console=ttyS0 root=/dev/vda granule.workload_streams=1\n": true,
		"granule.workload_streams=0":                               false,
		"granule.workload_streams":                                 false,
		"console=ttyS0 quiet":                                      false,
	} {
		if got := Requested(cmdline); got != want {
			t.Errorf("Requested(%q) = %v, want %v", cmdline, got, want)
		}
	}
}

// fakeVsock is cloud-hypervisor's host end of a hybrid vsock device: a
// unix socket that answers "CONNECT <port>" by handing the connection to
// whatever is listening on that port in the "guest".
type fakeVsock struct {
	path string
	ln   net.Listener

	mu     sync.Mutex
	broker *Broker // nil: nothing listening on Port
}

func newFakeVsock(t *testing.T) *fakeVsock {
	t.Helper()
	path := filepath.Join(t.TempDir(), "vsock.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	f := &fakeVsock{path: path, ln: ln}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.handle(conn)
		}
	}()
	return f
}

func (f *fakeVsock) listen(b *Broker) {
	f.mu.Lock()
	f.broker = b
	f.mu.Unlock()
}

func (f *fakeVsock) handle(conn net.Conn) {
	line, err := bufio.NewReader(io.LimitReader(conn, 64)).ReadString('\n')
	f.mu.Lock()
	b := f.broker
	f.mu.Unlock()
	if err != nil || line != fmt.Sprintf("CONNECT %d\n", Port) || b == nil {
		conn.Close()
		return
	}
	fmt.Fprintf(conn, "OK %d\n", Port)
	b.Serve(conn)
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The host end waits for a guest that is not listening yet, gets what the
// workload wrote before it attached, follows the guest across a restart
// of granule-container, and stops once the VM has and the guest has hung
// up.
func TestForward(t *testing.T) {
	vsock := newFakeVsock(t)
	var stdout, stderr syncBuffer
	finish := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- Forward(context.Background(), finish, vsock.path, &stdout, &stderr, nil) }()

	// Booting: nothing listening yet.
	time.Sleep(50 * time.Millisecond)
	b1 := NewBroker(0)
	io.WriteString(b1.Stdout(), "boot out\n")
	io.WriteString(b1.Stderr(), "boot err\n")
	vsock.listen(b1)
	waitFor(t, func() bool { return stdout.String() == "boot out\n" && stderr.String() == "boot err\n" })

	// granule-container restarts: a new broker, a new connection.
	vsock.listen(nil)
	b1.Close(0)
	b2 := NewBroker(0)
	io.WriteString(b2.Stdout(), "again\n")
	vsock.listen(b2)
	waitFor(t, func() bool { return stdout.String() == "boot out\nagain\n" })

	io.WriteString(b2.Stderr(), "bye\n")
	close(finish)
	b2.Close(time.Second)
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Forward = %v, want nil once the VM is gone", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Forward kept going after the VM finished")
	}
	if stderr.String() != "boot err\nbye\n" {
		t.Errorf("stderr = %q", stderr.String())
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, syscall.EPIPE }

func TestForwardReportsWriteErrors(t *testing.T) {
	vsock := newFakeVsock(t)
	b := NewBroker(0)
	io.WriteString(b.Stderr(), "x")
	vsock.listen(b)

	err := Forward(context.Background(), nil, vsock.path, io.Discard, failingWriter{}, nil)
	var werr *WriteError
	if !errors.As(err, &werr) || werr.Stream != "stderr" || !IsBrokenReader(err) {
		t.Errorf("Forward = %v, want a broken-reader WriteError for stderr", err)
	}
}

func TestForwardStopsWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Forward(ctx, nil, filepath.Join(t.TempDir(), "absent.sock"), io.Discard, io.Discard, nil)
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Forward = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Forward ignored its context")
	}
}

func TestOpenOutputs(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "stdout.log")
	errPath := filepath.Join(dir, "stderr.fifo")
	if err := syscall.Mkfifo(errPath, 0o600); err != nil {
		t.Fatal(err)
	}

	// The FIFO's reader comes along after the open has started, and the
	// open waits for it.
	got := make(chan string, 1)
	go func() {
		time.Sleep(50 * time.Millisecond)
		f, err := os.Open(errPath)
		if err != nil {
			got <- err.Error()
			return
		}
		defer f.Close()
		data, _ := io.ReadAll(f)
		got <- string(data)
	}()

	stdout, stderr, closeAll, err := OpenOutputs(context.Background(), outPath, errPath)
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(stdout, "to the file\n")
	io.WriteString(stderr, "to the fifo\n")
	closeAll()

	if s := <-got; s != "to the fifo\n" {
		t.Errorf("FIFO reader got %q", s)
	}
	if data, _ := os.ReadFile(outPath); string(data) != "to the file\n" {
		t.Errorf("file holds %q", data)
	}

	// Appended to, not truncated; one path for both is opened once.
	stdout, stderr, closeAll, err = OpenOutputs(context.Background(), outPath, outPath)
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(stdout, "a")
	io.WriteString(stderr, "b")
	closeAll()
	if data, _ := os.ReadFile(outPath); string(data) != "to the file\nab" {
		t.Errorf("file holds %q", data)
	}

	// No path discards the stream.
	stdout, _, closeAll, err = OpenOutputs(context.Background(), "", outPath)
	if err != nil || stdout != io.Discard {
		t.Errorf("empty stdout path: %v, %v", stdout, err)
	}
	closeAll()
}

func TestOpenOutputsGivesUpWithContext(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "unread.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, _, _, err := OpenOutputs(ctx, fifo, ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("OpenOutputs = %v, want the context's error", err)
	}
	// Let the abandoned open finish so the test leaves nothing blocked.
	if f, err := os.OpenFile(fifo, os.O_RDONLY|syscall.O_NONBLOCK, 0); err == nil {
		f.Close()
	}
}
