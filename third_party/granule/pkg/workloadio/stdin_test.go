package workloadio

import (
	"bufio"
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/bwsalmon/granule/pkg/execwire"
)

// recordingInput is a workload's stdin and terminal, as the broker sees
// them.
type recordingInput struct {
	mu     sync.Mutex
	data   []byte
	closes int
	sizes  [][2]uint16
}

func (r *recordingInput) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data = append(r.data, p...)
	return len(p), nil
}

func (r *recordingInput) CloseWrite() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closes++
	return nil
}

func (r *recordingInput) Resize(cols, rows uint16) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sizes = append(r.sizes, [2]uint16{cols, rows})
}

func (r *recordingInput) state() (string, int, [][2]uint16) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.data), r.closes, append([][2]uint16(nil), r.sizes...)
}

// serveOver attaches b to one end of a pipe and returns the host's end,
// past the opening line.
func serveOver(t *testing.T, b *Broker) (net.Conn, *bufio.Reader, execwire.Response) {
	t.Helper()
	host, guest := net.Pipe()
	t.Cleanup(func() { host.Close() })
	go b.Serve(guest)
	br := bufio.NewReader(host)
	resp, err := ReadOpening(br)
	if err != nil {
		t.Fatal(err)
	}
	// The guest's output side is not what is under test; keep it moving.
	go io.Copy(io.Discard, br)
	return host, br, resp
}

// A workload offered stdin is announced as taking it, gets what the host
// sends -- including what arrived before the workload had a stdin to
// give it -- and reads end-of-file once, when the host closes.
func TestBrokerDeliversStdin(t *testing.T) {
	b := NewBroker(0)
	b.OfferInput(true, true)
	host, _, resp := serveOver(t, b)
	if !resp.Supports(FeatureStdin) || !resp.Supports(FeatureWinsize) || !resp.Supports(FeatureStreams) {
		t.Fatalf("opening line names %q, want streams, stdin and winsize", resp.Features)
	}

	// Sent before SetInput: held, not dropped.
	if err := execwire.WriteFrame(host, execwire.TypeStdin, []byte("early ")); err != nil {
		t.Fatal(err)
	}
	in := &recordingInput{}
	b.SetInput(in)
	for _, f := range []struct {
		typ     byte
		payload []byte
	}{
		{execwire.TypeStdin, []byte("late")},
		{execwire.TypeWinsize, execwire.EncodeWinsize(120, 40)},
		{execwire.TypeStdinClose, nil},
		{execwire.TypeStdin, []byte(" after")},
		{execwire.TypeStdinClose, nil},
	} {
		if err := execwire.WriteFrame(host, f.typ, f.payload); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, func() bool { _, closes, _ := in.state(); return closes > 0 })
	b.Close(0)
	data, closes, sizes := in.state()
	if data != "early late" {
		t.Errorf("stdin = %q, want %q", data, "early late")
	}
	if closes != 1 {
		t.Errorf("stdin closed %d times, want once", closes)
	}
	if len(sizes) != 1 || sizes[0] != [2]uint16{120, 40} {
		t.Errorf("sizes = %v, want [[120 40]]", sizes)
	}
}

// A workload with no stdin open is not announced as having one, and what
// a host sends anyway is stepped over rather than held.
func TestBrokerWithoutStdinStepsOverIt(t *testing.T) {
	b := NewBroker(0)
	host, _, resp := serveOver(t, b)
	if resp.Supports(FeatureStdin) || resp.Supports(FeatureWinsize) {
		t.Fatalf("opening line names %q without any input offered", resp.Features)
	}
	for i := 0; i < 3; i++ {
		if err := execwire.WriteFrame(host, execwire.TypeStdin, []byte("ignored")); err != nil {
			t.Fatal(err)
		}
	}
	if err := execwire.WriteFrame(host, execwire.TypeStdinClose, nil); err != nil {
		t.Fatalf("the broker stopped reading: %v", err)
	}
}

// A workload that never got a stdin -- it failed to start -- does not
// leave the host's frames waiting for one once the broker closes.
func TestBrokerClosedWithoutInputDropsIt(t *testing.T) {
	b := NewBroker(0)
	b.OfferInput(true, false)
	host, _, _ := serveOver(t, b)
	if err := execwire.WriteFrame(host, execwire.TypeStdin, []byte("x")); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { b.waitInput(); close(done) }()
	b.Close(0)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("still waiting for an input after the broker closed")
	}
}

// End to end: what the caller writes to the host's stdin reaches the
// workload, its writer closing is the workload's end-of-file, the last
// terminal size follows the guest to a restarted granule-container, and
// that restarted container reads end-of-file at once.
func TestForwardCarriesStdin(t *testing.T) {
	vsock := newFakeVsock(t)
	in := NewHostInput()
	pr, pw := io.Pipe()
	copied := make(chan error, 1)
	go func() { copied <- in.CopyStdin(nil, pr) }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finish := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- Forward(ctx, finish, vsock.path, io.Discard, io.Discard, in) }()

	b1 := NewBroker(0)
	b1.OfferInput(true, true)
	in1 := &recordingInput{}
	b1.SetInput(in1)
	vsock.listen(b1)

	in.Resize(80, 24)
	if _, err := io.WriteString(pw, "hello\n"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { data, _, sizes := in1.state(); return data == "hello\n" && len(sizes) > 0 })
	in.Resize(100, 30)
	waitFor(t, func() bool {
		_, _, sizes := in1.state()
		return len(sizes) > 0 && sizes[len(sizes)-1] == [2]uint16{100, 30}
	})

	pw.Close()
	waitFor(t, func() bool { _, closes, _ := in1.state(); return closes == 1 })
	if err := <-copied; err != nil {
		t.Errorf("CopyStdin = %v", err)
	}

	// granule-container restarts.
	vsock.listen(nil)
	b1.Close(0)
	b2 := NewBroker(0)
	b2.OfferInput(true, true)
	in2 := &recordingInput{}
	b2.SetInput(in2)
	vsock.listen(b2)
	waitFor(t, func() bool {
		data, closes, sizes := in2.state()
		return data == "" && closes == 1 && len(sizes) == 1 && sizes[0] == [2]uint16{100, 30}
	})

	close(finish)
	b2.Close(0)
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Forward = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Forward kept going after the VM finished")
	}
}

// A guest whose workload has no stdin open costs the caller its input,
// and says so, but does not leave the caller's writes blocked.
func TestForwardDiscardsStdinTheGuestDoesNotTake(t *testing.T) {
	vsock := newFakeVsock(t)
	b := NewBroker(0)
	vsock.listen(b)
	in := NewHostInput()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go Forward(ctx, nil, vsock.path, io.Discard, io.Discard, in)

	pr, pw := io.Pipe()
	copied := make(chan error, 1)
	go func() { copied <- in.CopyStdin(nil, pr) }()
	wrote := make(chan error, 1)
	go func() {
		_, err := io.WriteString(pw, "nobody reads this")
		pw.Close()
		wrote <- err
	}()
	select {
	case err := <-wrote:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the caller's write is blocked on a guest that does not take stdin")
	}
	if err := <-copied; err != nil {
		t.Errorf("CopyStdin = %v", err)
	}
}

func TestParseWinsize(t *testing.T) {
	for _, tc := range []struct {
		in         string
		cols, rows uint16
		ok         bool
	}{
		{"80 24", 80, 24, true},
		{"132x43\n", 132, 43, true},
		{"  200\t50 ", 200, 50, true},
		{"80", 0, 0, false},
		{"80 24 1", 0, 0, false},
		{"80 -1", 0, 0, false},
		{"70000 24", 0, 0, false},
	} {
		cols, rows, err := ParseWinsize(tc.in)
		if (err == nil) != tc.ok || cols != tc.cols || rows != tc.rows {
			t.Errorf("ParseWinsize(%q) = %d, %d, %v", tc.in, cols, rows, err)
		}
	}
}

// A terminal without an open stdin takes sizes and nothing else.
func TestBrokerOffersWinsizeAlone(t *testing.T) {
	b := NewBroker(0)
	b.OfferInput(false, true)
	in := &recordingInput{}
	b.SetInput(in)
	host, _, resp := serveOver(t, b)
	if resp.Supports(FeatureStdin) || !resp.Supports(FeatureWinsize) {
		t.Fatalf("opening line names %q, want winsize without stdin", resp.Features)
	}
	execwire.WriteFrame(host, execwire.TypeStdin, []byte("typed"))
	execwire.WriteFrame(host, execwire.TypeStdinClose, nil)
	execwire.WriteFrame(host, execwire.TypeWinsize, execwire.EncodeWinsize(10, 5))
	waitFor(t, func() bool { _, _, sizes := in.state(); return len(sizes) == 1 })
	if data, closes, _ := in.state(); data != "" || closes != 0 {
		t.Errorf("a workload with no stdin open got %q and %d closes", data, closes)
	}
}
