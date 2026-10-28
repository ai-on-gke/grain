package guestforward

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// sessionPair returns two ends of one session over net.Pipe, whose
// writes block until read -- the least forgiving transport there is, so
// any frame that could deadlock behind another does here.
func sessionPair(t *testing.T, cfg Config) (client, server *Session) {
	t.Helper()
	a, b := net.Pipe()
	client = NewSession(a, true, cfg)
	server = NewSession(b, false, cfg)
	t.Cleanup(func() {
		client.Close()
		server.Close()
	})
	return client, server
}

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// echoServer confirms every stream and echoes it back, half-close
// included.
func echoServer(ctx context.Context, s *Session) {
	for {
		st, err := s.Accept(ctx)
		if err != nil {
			return
		}
		_ = st.Confirm(append([]byte("ok:"), st.Meta()...))
		go func() {
			_, _ = io.Copy(st, st)
			_ = st.CloseWrite()
		}()
	}
}

func TestOpenConfirmEchoHalfClose(t *testing.T) {
	ctx := ctxT(t)
	c, s := sessionPair(t, Config{})
	go echoServer(ctx, s)

	st, err := c.Open(ctx, []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(st.Reply()); got != "ok:hello" {
		t.Fatalf("reply = %q", got)
	}
	if _, err := st.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	if err := st.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(st)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "ping" {
		t.Fatalf("echo = %q", got)
	}
	if _, err := st.Write([]byte("x")); err == nil {
		t.Fatal("write after CloseWrite succeeded")
	}
	_ = st.Close()
	waitFor(t, func() bool { return c.NumStreams() == 0 && s.NumStreams() == 0 })
}

// A half-closed stream keeps working in the other direction: the far
// end sees EOF, and can still answer at length afterwards.
func TestHalfCloseThenReply(t *testing.T) {
	ctx := ctxT(t)
	c, s := sessionPair(t, Config{})
	reply := bytes.Repeat([]byte("r"), 1<<20)
	go func() {
		st, err := s.Accept(ctx)
		if err != nil {
			return
		}
		_ = st.Confirm(nil)
		req, _ := io.ReadAll(st) // until the client's fin
		if string(req) != "request" {
			st.Reset("bad request")
			return
		}
		_, _ = st.Write(reply)
		_ = st.CloseWrite()
	}()
	st, err := c.Open(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = st.Write([]byte("request"))
	_ = st.CloseWrite()
	got, err := io.ReadAll(st)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, reply) {
		t.Fatalf("got %d bytes, want %d", len(got), len(reply))
	}
}

func TestReject(t *testing.T) {
	ctx := ctxT(t)
	c, s := sessionPair(t, Config{})
	go func() {
		st, err := s.Accept(ctx)
		if err == nil {
			_ = st.Reject("no thanks")
		}
	}()
	_, err := c.Open(ctx, nil)
	var rej *RejectedError
	if !errors.As(err, &rej) || rej.Reason != "no thanks" {
		t.Fatalf("Open = %v, want a rejection saying why", err)
	}
	waitFor(t, func() bool { return c.NumStreams() == 0 && s.NumStreams() == 0 })
}

// A reader that stops reading stops its writer within one window: the
// far end never buffers more than Window for the stream.
func TestFlowControlBoundsBuffering(t *testing.T) {
	ctx := ctxT(t)
	const window = 64 << 10
	c, s := sessionPair(t, Config{Window: window})
	accepted := make(chan *Stream, 1)
	go func() {
		st, err := s.Accept(ctx)
		if err != nil {
			return
		}
		_ = st.Confirm(nil)
		accepted <- st
	}()
	st, err := c.Open(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := <-accepted

	payload := make([]byte, 4<<20)
	_, _ = rand.Read(payload)
	var mu sync.Mutex
	written := 0
	writeDone := make(chan error, 1)
	go func() {
		for off := 0; off < len(payload); off += 8 << 10 {
			n, err := st.Write(payload[off : off+8<<10])
			mu.Lock()
			written += n
			mu.Unlock()
			if err != nil {
				writeDone <- err
				return
			}
		}
		writeDone <- st.CloseWrite()
	}()

	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	w := written
	mu.Unlock()
	if w > window {
		t.Fatalf("writer got %d bytes out against a reader that has read nothing; window is %d", w, window)
	}
	srv.mu.Lock()
	buffered := len(srv.rbuf)
	srv.mu.Unlock()
	if buffered > window {
		t.Fatalf("reader buffered %d bytes; window is %d", buffered, window)
	}

	got, err := io.ReadAll(srv)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("payload corrupted")
	}
}

// One stalled stream does not hold up another on the same session.
func TestStalledStreamDoesNotBlockOthers(t *testing.T) {
	ctx := ctxT(t)
	c, s := sessionPair(t, Config{Window: 32 << 10})
	go func() {
		// The first stream is accepted and never read.
		st, _ := s.Accept(ctx)
		_ = st.Confirm(nil)
		echoServer(ctx, s)
	}()
	stalled, err := c.Open(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = stalled.Write(make([]byte, 1<<20)) }()
	time.Sleep(50 * time.Millisecond)

	st, err := c.Open(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Write([]byte("through")); err != nil {
		t.Fatal(err)
	}
	_ = st.CloseWrite()
	got, err := io.ReadAll(st)
	if err != nil || string(got) != "through" {
		t.Fatalf("echo = %q, %v", got, err)
	}
}

func TestManyConcurrentStreams(t *testing.T) {
	ctx := ctxT(t)
	c, s := sessionPair(t, Config{Window: 32 << 10})
	go echoServer(ctx, s)

	var wg sync.WaitGroup
	errs := make(chan error, 100)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st, err := c.Open(ctx, nil)
			if err != nil {
				errs <- err
				return
			}
			data := make([]byte, 200<<10)
			_, _ = rand.Read(data)
			go func() {
				_, _ = st.Write(data)
				_ = st.CloseWrite()
			}()
			got, err := io.ReadAll(st)
			if err != nil {
				errs <- err
				return
			}
			if !bytes.Equal(got, data) {
				errs <- errors.New("echo mismatch")
			}
			_ = st.Close()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestMaxStreams(t *testing.T) {
	ctx := ctxT(t)
	c, s := sessionPair(t, Config{MaxStreams: 2})
	go echoServer(ctx, s)
	for i := 0; i < 2; i++ {
		if _, err := c.Open(ctx, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.Open(ctx, nil); err == nil || !strings.Contains(err.Error(), "too many streams") {
		t.Fatalf("third Open = %v, want too many streams", err)
	}
}

// The acceptor enforces its own cap even when the opener's is higher.
func TestMaxStreamsEnforcedByAcceptor(t *testing.T) {
	ctx := ctxT(t)
	a, b := net.Pipe()
	c := NewSession(a, true, Config{MaxStreams: 10})
	s := NewSession(b, false, Config{MaxStreams: 1})
	defer c.Close()
	defer s.Close()
	go echoServer(ctx, s)
	if _, err := c.Open(ctx, nil); err != nil {
		t.Fatal(err)
	}
	var rej *RejectedError
	if _, err := c.Open(ctx, nil); !errors.As(err, &rej) {
		t.Fatalf("second Open = %v, want a rejection", err)
	}
}

func TestResetReachesPeer(t *testing.T) {
	ctx := ctxT(t)
	c, s := sessionPair(t, Config{})
	accepted := make(chan *Stream, 1)
	go func() {
		st, _ := s.Accept(ctx)
		_ = st.Confirm(nil)
		accepted <- st
	}()
	st, err := c.Open(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := <-accepted
	st.Reset("gone")
	if _, err := srv.Read(make([]byte, 1)); !errors.Is(err, ErrStreamReset) {
		t.Fatalf("Read after peer reset = %v", err)
	}
}

func TestSessionCloseEndsStreams(t *testing.T) {
	ctx := ctxT(t)
	c, s := sessionPair(t, Config{})
	go echoServer(ctx, s)
	st, err := c.Open(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	readErr := make(chan error, 1)
	go func() {
		_, err := st.Read(make([]byte, 1))
		readErr <- err
	}()
	s.Close()
	select {
	case err := <-readErr:
		if !errors.Is(err, ErrSessionClosed) {
			t.Fatalf("Read = %v, want session closed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Read still blocked after the far end closed")
	}
	<-c.Done()
	if !errors.Is(c.Err(), io.EOF) {
		t.Fatalf("session error = %v, want EOF", c.Err())
	}
}

func TestPing(t *testing.T) {
	c, _ := sessionPair(t, Config{})
	if _, err := c.Ping(ctxT(t)); err != nil {
		t.Fatal(err)
	}
}

// A far end that has gone silent -- reads, never answers -- is noticed
// by keepalive rather than waited on for ever.
func TestKeepAliveTimeout(t *testing.T) {
	a, b := net.Pipe()
	go func() { _, _ = io.Copy(io.Discard, b) }()
	c := NewSession(a, true, Config{KeepAlive: 20 * time.Millisecond, KeepAliveTimeout: 100 * time.Millisecond})
	defer c.Close()
	select {
	case <-c.Done():
		if !strings.Contains(c.Err().Error(), "keepalive") {
			t.Fatalf("session error = %v", c.Err())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session survived a silent peer")
	}
}

// Data beyond the window granted is a peer not honouring the bound, and
// ends the session rather than being buffered.
func TestWindowViolationEndsSession(t *testing.T) {
	a, b := net.Pipe()
	s := NewSession(b, false, Config{Window: maxFrame})
	defer s.Close()
	go func() { _, _ = io.Copy(io.Discard, a) }()

	write := func(typ byte, id uint32, payload []byte) {
		var hdr [headerSize]byte
		hdr[0] = typ
		binary.BigEndian.PutUint32(hdr[1:5], id)
		binary.BigEndian.PutUint32(hdr[5:9], uint32(len(payload)))
		_, _ = a.Write(append(hdr[:], payload...))
	}
	write(frameOpen, 1, nil)
	st, err := s.Accept(ctxT(t))
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Confirm(nil)
	write(frameData, 1, make([]byte, maxFrame))
	write(frameData, 1, make([]byte, 1))
	select {
	case <-s.Done():
		if !strings.Contains(s.Err().Error(), "beyond its window") {
			t.Fatalf("session error = %v", s.Err())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session accepted data beyond the window")
	}
}

func TestOversizedFrameEndsSession(t *testing.T) {
	a, b := net.Pipe()
	s := NewSession(b, false, Config{})
	defer s.Close()
	go func() { _, _ = io.Copy(io.Discard, a) }()
	var hdr [headerSize]byte
	binary.BigEndian.PutUint32(hdr[5:9], maxFrame+1)
	go func() { _, _ = a.Write(hdr[:]) }()
	select {
	case <-s.Done():
		if !strings.Contains(s.Err().Error(), "exceeds") {
			t.Fatalf("session error = %v", s.Err())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session accepted an oversized frame")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
