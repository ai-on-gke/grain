package guestforward

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// The mux is the part of this package that turns one byte stream -- the
// stdio of a `docker exec` or `kubectl exec`, carried on to the guest
// over vsock by the shim -- into many independent, flow-controlled,
// half-closable streams. It is deliberately small: nine frame types and
// one header, written by one goroutine per session.
//
// A frame is a 9-byte header and a payload:
//
//	type    uint8
//	stream  uint32, big-endian (0 for session-level frames)
//	length  uint32, big-endian, at most maxFrame
//
// Streams are opened by either end. The end that wrote NewSession with
// client=true numbers its streams odd, the other even, so the two never
// pick the same id.
//
// Flow control is per stream and credit-based: a stream may have at most
// Config.Window bytes the far end has not yet read sitting in the far
// end's buffer. The reader hands credit back with a window frame as it
// consumes, so a reader that stops reading stops its writer within one
// window, and neither end ever buffers more than Window per stream.
// Data beyond the credit granted is a protocol violation that ends the
// session, since it means the peer is not honouring the bound.

const (
	frameData    byte = 0 // payload: bytes for the stream
	frameOpen    byte = 1 // payload: what to open, opaque to the mux
	frameConfirm byte = 2 // payload: the acceptor's reply, opaque
	frameReject  byte = 3 // payload: why, as text
	frameWindow  byte = 4 // payload: uint32 credit to add
	frameFin     byte = 5 // no payload: the sender will write no more
	frameReset   byte = 6 // payload: why, as text; both directions end
	framePing    byte = 7 // payload: 8 opaque bytes, echoed in a pong
	framePong    byte = 8
)

const (
	headerSize = 9

	// maxFrame bounds one frame's payload. 32 KiB is io.Copy's own
	// buffer size, so a splice writes one frame per read.
	maxFrame = 32 << 10

	// maxQueued bounds the frames waiting for the session's writer. Data
	// frames are bounded by their writers -- each waits for its frame to
	// be written -- so this is a bound on control frames, which only a
	// peer that has stopped reading while it keeps writing pings can pile
	// up.
	maxQueued = 8192
)

// Config tunes a Session. The zero value is usable.
type Config struct {
	// MaxStreams caps the streams open at once, in both directions
	// together. An open beyond it is rejected. Default 256.
	MaxStreams int

	// Window is the per-stream receive buffer, and so the most data one
	// stream can have in flight. Default 256 KiB.
	Window uint32

	// KeepAlive is how often a ping is sent. Default 15s; negative
	// disables keepalive.
	KeepAlive time.Duration

	// KeepAliveTimeout is how long the session may go without receiving
	// any frame before it is declared dead. Default 3 × KeepAlive.
	KeepAliveTimeout time.Duration
}

func (c Config) withDefaults() Config {
	if c.MaxStreams <= 0 {
		c.MaxStreams = 256
	}
	if c.Window == 0 {
		c.Window = 256 << 10
	}
	if c.Window < maxFrame {
		c.Window = maxFrame
	}
	if c.KeepAlive == 0 {
		c.KeepAlive = 15 * time.Second
	}
	if c.KeepAliveTimeout <= 0 && c.KeepAlive > 0 {
		c.KeepAliveTimeout = 3 * c.KeepAlive
	}
	return c
}

// ErrSessionClosed is returned by operations on a session that has
// ended. Session.Err says why it ended.
var ErrSessionClosed = errors.New("guestforward: session closed")

// ErrStreamReset is wrapped by the error a stream's Read and Write
// return once either end has reset it.
var ErrStreamReset = errors.New("guestforward: stream reset")

type frame struct {
	typ     byte
	id      uint32
	payload []byte
	done    chan error // nil unless the sender waits for the write
}

// Session is one end of a multiplexed connection.
type Session struct {
	conn io.ReadWriter
	cfg  Config

	qmu    sync.Mutex
	qcond  *sync.Cond
	queue  []frame
	qclose bool

	mu       sync.Mutex
	streams  map[uint32]*Stream
	nextID   uint32
	pings    map[uint64]chan struct{}
	pingSeq  uint64
	acceptCh chan *Stream

	lastRecv atomic.Int64

	closeOnce sync.Once
	closed    chan struct{}
	err       error
}

// NewSession starts a session over conn. Exactly one end of a connection
// must pass client=true. If conn is also an io.Closer, closing the
// session closes it, which is what unblocks a read in progress.
func NewSession(conn io.ReadWriter, client bool, cfg Config) *Session {
	s := &Session{
		conn:    conn,
		cfg:     cfg.withDefaults(),
		streams: make(map[uint32]*Stream),
		pings:   make(map[uint64]chan struct{}),
		closed:  make(chan struct{}),
	}
	s.qcond = sync.NewCond(&s.qmu)
	// Room for every stream the session may hold, so that handing one
	// to Accept never blocks the read loop: a stream waiting to be
	// accepted counts against MaxStreams like any other.
	s.acceptCh = make(chan *Stream, s.cfg.MaxStreams)
	if client {
		s.nextID = 1
	} else {
		s.nextID = 2
	}
	s.lastRecv.Store(time.Now().UnixNano())
	go s.writeLoop()
	go s.readLoop()
	if s.cfg.KeepAlive > 0 {
		go s.keepAlive()
	}
	return s
}

// Done is closed once the session has ended.
func (s *Session) Done() <-chan struct{} { return s.closed }

// Err reports why the session ended, or nil while it is running.
func (s *Session) Err() error {
	select {
	case <-s.closed:
		return s.err
	default:
		return nil
	}
}

// Close ends the session and every stream on it.
func (s *Session) Close() error {
	s.fail(ErrSessionClosed)
	return nil
}

// NumStreams reports how many streams are open.
func (s *Session) NumStreams() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.streams)
}

func (s *Session) fail(err error) {
	s.closeOnce.Do(func() {
		s.err = err
		close(s.closed)
		if c, ok := s.conn.(io.Closer); ok {
			_ = c.Close()
		}
		s.qmu.Lock()
		s.qclose = true
		pending := s.queue
		s.queue = nil
		s.qcond.Broadcast()
		s.qmu.Unlock()
		for _, f := range pending {
			if f.done != nil {
				f.done <- err
			}
		}
		s.mu.Lock()
		streams := s.streams
		s.streams = map[uint32]*Stream{}
		s.mu.Unlock()
		for _, st := range streams {
			st.terminate(fmt.Errorf("%w: %v", ErrSessionClosed, err))
		}
	})
}

// enqueue hands f to the writer. A data frame's sender then waits on
// f.done; control frames are fire-and-forget.
func (s *Session) enqueue(f frame) error {
	s.qmu.Lock()
	if s.qclose {
		s.qmu.Unlock()
		return ErrSessionClosed
	}
	if len(s.queue) >= maxQueued {
		s.qmu.Unlock()
		s.fail(errors.New("guestforward: peer is not reading; too many frames queued"))
		return ErrSessionClosed
	}
	s.queue = append(s.queue, f)
	s.qcond.Signal()
	s.qmu.Unlock()
	return nil
}

func (s *Session) writeLoop() {
	bw := bufio.NewWriterSize(s.conn, 64<<10)
	var hdr [headerSize]byte
	for {
		s.qmu.Lock()
		for len(s.queue) == 0 && !s.qclose {
			s.qcond.Wait()
		}
		if s.qclose {
			s.qmu.Unlock()
			return
		}
		batch := s.queue
		s.queue = nil
		s.qmu.Unlock()

		var err error
		for i, f := range batch {
			if err == nil {
				hdr[0] = f.typ
				binary.BigEndian.PutUint32(hdr[1:5], f.id)
				binary.BigEndian.PutUint32(hdr[5:9], uint32(len(f.payload)))
				if _, err = bw.Write(hdr[:]); err == nil {
					_, err = bw.Write(f.payload)
				}
				if err == nil && i == len(batch)-1 {
					err = bw.Flush()
				}
			}
			if f.done != nil {
				f.done <- err
			}
		}
		if err != nil {
			s.fail(fmt.Errorf("guestforward: writing to the transport: %w", err))
			return
		}
	}
}

func (s *Session) readLoop() {
	br := bufio.NewReaderSize(s.conn, 64<<10)
	var hdr [headerSize]byte
	for {
		if _, err := io.ReadFull(br, hdr[:]); err != nil {
			if errors.Is(err, io.ErrUnexpectedEOF) {
				err = io.EOF
			}
			s.fail(fmt.Errorf("guestforward: reading from the transport: %w", err))
			return
		}
		typ := hdr[0]
		id := binary.BigEndian.Uint32(hdr[1:5])
		n := binary.BigEndian.Uint32(hdr[5:9])
		if n > maxFrame {
			s.fail(fmt.Errorf("guestforward: protocol error: frame of %d bytes exceeds %d", n, maxFrame))
			return
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(br, payload); err != nil {
			s.fail(fmt.Errorf("guestforward: reading from the transport: %w", err))
			return
		}
		s.lastRecv.Store(time.Now().UnixNano())
		if err := s.handle(typ, id, payload); err != nil {
			s.fail(err)
			return
		}
	}
}

func (s *Session) handle(typ byte, id uint32, payload []byte) error {
	switch typ {
	case framePing:
		return s.enqueue(frame{typ: framePong, payload: payload})
	case framePong:
		if len(payload) == 8 {
			seq := binary.BigEndian.Uint64(payload)
			s.mu.Lock()
			ch := s.pings[seq]
			delete(s.pings, seq)
			s.mu.Unlock()
			if ch != nil {
				close(ch)
			}
		}
		return nil
	case frameOpen:
		return s.handleOpen(id, payload)
	}

	s.mu.Lock()
	st := s.streams[id]
	s.mu.Unlock()
	if st == nil {
		// A frame for a stream this end has already finished with:
		// data or a fin the peer sent before it saw our reset, say.
		// Nothing to do, and nothing wrong.
		return nil
	}
	switch typ {
	case frameData:
		return st.receive(payload)
	case frameWindow:
		if len(payload) != 4 {
			return fmt.Errorf("guestforward: protocol error: window frame of %d bytes", len(payload))
		}
		st.credit(binary.BigEndian.Uint32(payload))
	case frameFin:
		st.receiveFin()
	case frameReset:
		st.terminate(fmt.Errorf("%w by peer: %s", ErrStreamReset, payload))
		s.forget(st)
	case frameConfirm:
		st.resolveOpen(payload, nil)
	case frameReject:
		st.resolveOpen(nil, &RejectedError{Reason: string(payload)})
		s.forget(st)
	default:
		return fmt.Errorf("guestforward: protocol error: unknown frame type %d", typ)
	}
	return nil
}

func (s *Session) handleOpen(id uint32, meta []byte) error {
	s.mu.Lock()
	if _, dup := s.streams[id]; dup {
		s.mu.Unlock()
		return fmt.Errorf("guestforward: protocol error: stream %d opened twice", id)
	}
	if len(s.streams) >= s.cfg.MaxStreams {
		s.mu.Unlock()
		return s.enqueue(frame{typ: frameReject, id: id, payload: []byte(fmt.Sprintf("too many streams (limit %d)", s.cfg.MaxStreams))})
	}
	st := newStream(s, id, meta)
	st.opened = true
	s.streams[id] = st
	s.mu.Unlock()

	select {
	case s.acceptCh <- st:
		return nil
	default:
		s.forget(st)
		return s.enqueue(frame{typ: frameReject, id: id, payload: []byte("accept backlog full")})
	}
}

func (s *Session) forget(st *Stream) {
	s.mu.Lock()
	if s.streams[st.id] == st {
		delete(s.streams, st.id)
	}
	s.mu.Unlock()
}

// RejectedError is what Open returns when the far end refused the
// stream, with the reason it gave.
type RejectedError struct{ Reason string }

func (e *RejectedError) Error() string { return "refused by the far end: " + e.Reason }

// Open asks the far end for a new stream, describing what it is for in
// meta, and waits for it to be confirmed or rejected. The returned
// stream's Reply is whatever the far end confirmed it with.
func (s *Session) Open(ctx context.Context, meta []byte) (*Stream, error) {
	if len(meta) > maxFrame {
		return nil, fmt.Errorf("guestforward: open metadata of %d bytes exceeds %d", len(meta), maxFrame)
	}
	s.mu.Lock()
	if s.Err() != nil {
		s.mu.Unlock()
		return nil, ErrSessionClosed
	}
	if len(s.streams) >= s.cfg.MaxStreams {
		s.mu.Unlock()
		return nil, fmt.Errorf("guestforward: too many streams (limit %d)", s.cfg.MaxStreams)
	}
	id := s.nextID
	s.nextID += 2
	st := newStream(s, id, meta)
	s.streams[id] = st
	s.mu.Unlock()

	if err := s.enqueue(frame{typ: frameOpen, id: id, payload: meta}); err != nil {
		s.forget(st)
		return nil, err
	}
	select {
	case <-st.openDone:
		if st.openErr != nil {
			return nil, st.openErr
		}
		return st, nil
	case <-ctx.Done():
		st.Reset("open abandoned")
		return nil, ctx.Err()
	case <-s.closed:
		return nil, fmt.Errorf("%w: %v", ErrSessionClosed, s.err)
	}
}

// Accept waits for the far end to open a stream. The caller must answer
// it with Confirm or Reject; until it does, the opener is waiting.
func (s *Session) Accept(ctx context.Context) (*Stream, error) {
	select {
	case st := <-s.acceptCh:
		return st, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.closed:
		return nil, fmt.Errorf("%w: %v", ErrSessionClosed, s.err)
	}
}

// Ping round-trips a ping through the far end and reports how long it
// took. It is how a caller learns the far end is actually there, rather
// than just that the transport started.
func (s *Session) Ping(ctx context.Context) (time.Duration, error) {
	s.mu.Lock()
	s.pingSeq++
	seq := s.pingSeq
	ch := make(chan struct{})
	s.pings[seq] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pings, seq)
		s.mu.Unlock()
	}()

	var payload [8]byte
	binary.BigEndian.PutUint64(payload[:], seq)
	start := time.Now()
	if err := s.enqueue(frame{typ: framePing, payload: payload[:]}); err != nil {
		return 0, fmt.Errorf("%w: %v", err, s.Err())
	}
	select {
	case <-ch:
		return time.Since(start), nil
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-s.closed:
		return 0, s.err
	}
}

func (s *Session) keepAlive() {
	t := time.NewTicker(s.cfg.KeepAlive)
	defer t.Stop()
	for {
		select {
		case <-s.closed:
			return
		case <-t.C:
		}
		idle := time.Since(time.Unix(0, s.lastRecv.Load()))
		if idle > s.cfg.KeepAliveTimeout {
			s.fail(fmt.Errorf("guestforward: nothing heard from the far end for %s (keepalive timeout)", idle.Round(time.Second)))
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), s.cfg.KeepAlive)
		go func() {
			defer cancel()
			_, _ = s.Ping(ctx)
		}()
	}
}

// Stream is one bidirectional byte stream on a session. Read and Write
// may be called concurrently with each other, but not each with itself.
type Stream struct {
	s    *Session
	id   uint32
	meta []byte

	// opened marks a stream the far end opened, which this end answers
	// with Confirm or Reject.
	opened   bool
	answered bool
	openDone chan struct{}
	openErr  error
	reply    []byte

	mu   sync.Mutex
	cond *sync.Cond

	rbuf     []byte // received, not yet read
	unacked  uint32 // read, not yet credited back
	rfin     bool   // the far end will send no more
	sendWin  uint32 // credit the far end has granted
	wfin     bool   // this end has sent its fin
	err      error  // set once the stream is reset or the session ends
	finished bool   // removed from the session
}

func newStream(s *Session, id uint32, meta []byte) *Stream {
	st := &Stream{s: s, id: id, meta: meta, openDone: make(chan struct{}), sendWin: s.cfg.Window}
	st.cond = sync.NewCond(&st.mu)
	return st
}

// ID is the stream's number on its session.
func (st *Stream) ID() uint32 { return st.id }

// Meta is what the opener said the stream is for.
func (st *Stream) Meta() []byte { return st.meta }

// Reply is what the far end confirmed an opened stream with.
func (st *Stream) Reply() []byte { return st.reply }

// Confirm accepts a stream the far end opened, sending reply back to it.
func (st *Stream) Confirm(reply []byte) error {
	if !st.opened || st.answered {
		return errors.New("guestforward: Confirm on a stream this end did not accept, or already answered")
	}
	st.answered = true
	return st.s.enqueue(frame{typ: frameConfirm, id: st.id, payload: reply})
}

// Reject refuses a stream the far end opened, telling it why.
func (st *Stream) Reject(reason string) error {
	if !st.opened || st.answered {
		return errors.New("guestforward: Reject on a stream this end did not accept, or already answered")
	}
	st.answered = true
	st.terminate(&RejectedError{Reason: reason})
	st.s.forget(st)
	return st.s.enqueue(frame{typ: frameReject, id: st.id, payload: []byte(reason)})
}

func (st *Stream) resolveOpen(reply []byte, err error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	select {
	case <-st.openDone:
		return
	default:
	}
	st.reply, st.openErr = reply, err
	if err != nil && st.err == nil {
		st.err = err
		st.cond.Broadcast()
	}
	close(st.openDone)
}

func (st *Stream) receive(p []byte) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.err != nil || st.rfin {
		// Already finished with on this end; the peer had sent this
		// before it heard. Credit it back so the accounting on its side
		// stays honest, though nothing will be written again.
		return nil
	}
	if uint64(len(st.rbuf))+uint64(len(p)) > uint64(st.s.cfg.Window) {
		return fmt.Errorf("guestforward: protocol error: stream %d sent beyond its window", st.id)
	}
	st.rbuf = append(st.rbuf, p...)
	st.cond.Broadcast()
	return nil
}

func (st *Stream) receiveFin() {
	st.mu.Lock()
	st.rfin = true
	st.cond.Broadcast()
	done := st.wfin
	st.mu.Unlock()
	if done {
		st.s.forget(st)
	}
}

func (st *Stream) credit(n uint32) {
	st.mu.Lock()
	st.sendWin += n
	st.cond.Broadcast()
	st.mu.Unlock()
}

func (st *Stream) terminate(err error) {
	st.mu.Lock()
	if st.err == nil {
		st.err = err
	}
	st.rbuf = nil
	st.cond.Broadcast()
	st.mu.Unlock()
	st.resolveOpen(nil, err)
}

// Read reads data the far end wrote. It returns io.EOF once the far end
// has closed its writing half and everything it sent has been read.
func (st *Stream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	st.mu.Lock()
	for len(st.rbuf) == 0 && !st.rfin && st.err == nil {
		st.cond.Wait()
	}
	if st.err != nil {
		err := st.err
		st.mu.Unlock()
		return 0, err
	}
	if len(st.rbuf) == 0 {
		st.mu.Unlock()
		return 0, io.EOF
	}
	n := copy(p, st.rbuf)
	st.rbuf = st.rbuf[n:]
	if len(st.rbuf) == 0 {
		st.rbuf = nil
	}
	st.unacked += uint32(n)
	var grant uint32
	if st.unacked >= st.s.cfg.Window/2 && !st.rfin {
		grant, st.unacked = st.unacked, 0
	}
	st.mu.Unlock()
	if grant > 0 {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], grant)
		_ = st.s.enqueue(frame{typ: frameWindow, id: st.id, payload: b[:]})
	}
	return n, nil
}

// Write writes p to the far end, blocking while the far end's buffer for
// this stream is full.
func (st *Stream) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		st.mu.Lock()
		for st.sendWin == 0 && st.err == nil && !st.wfin {
			st.cond.Wait()
		}
		if st.err != nil {
			err := st.err
			st.mu.Unlock()
			return written, err
		}
		if st.wfin {
			st.mu.Unlock()
			return written, errors.New("guestforward: write after CloseWrite")
		}
		n := len(p)
		if n > maxFrame {
			n = maxFrame
		}
		if uint32(n) > st.sendWin {
			n = int(st.sendWin)
		}
		st.sendWin -= uint32(n)
		st.mu.Unlock()

		chunk := make([]byte, n)
		copy(chunk, p[:n])
		done := make(chan error, 1)
		if err := st.s.enqueue(frame{typ: frameData, id: st.id, payload: chunk, done: done}); err != nil {
			return written, err
		}
		if err := <-done; err != nil {
			return written, err
		}
		written += n
		p = p[n:]
	}
	return written, nil
}

// CloseWrite tells the far end this end will write no more; its reads
// see io.EOF once they have drained what was sent. Reading continues.
func (st *Stream) CloseWrite() error {
	st.mu.Lock()
	if st.wfin || st.err != nil {
		st.mu.Unlock()
		return nil
	}
	st.wfin = true
	done := st.rfin
	st.cond.Broadcast()
	st.mu.Unlock()
	err := st.s.enqueue(frame{typ: frameFin, id: st.id})
	if done {
		st.s.forget(st)
	}
	return err
}

// Reset abandons the stream in both directions, telling the far end why.
func (st *Stream) Reset(reason string) {
	st.mu.Lock()
	already := st.err != nil
	st.mu.Unlock()
	st.terminate(fmt.Errorf("%w: %s", ErrStreamReset, reason))
	st.s.forget(st)
	if !already {
		_ = st.s.enqueue(frame{typ: frameReset, id: st.id, payload: []byte(reason)})
	}
}

// Close ends the stream: cleanly, if both halves have already been
// closed and drained, and with a reset otherwise, since data the far end
// sends after this would have nowhere to go.
func (st *Stream) Close() error {
	st.mu.Lock()
	clean := st.wfin && st.rfin && len(st.rbuf) == 0
	st.mu.Unlock()
	if clean {
		st.s.forget(st)
		return nil
	}
	st.Reset("closed")
	return nil
}
