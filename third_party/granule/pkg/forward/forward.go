// Package forward multiplexes TCP port forwards (-L local-to-guest and -R
// guest-to-local) over a single bidirectional byte stream such as a
// `docker exec -i` or `kubectl exec -i` session connected to the guest agent
// over virtio-vsock.
//
// The two ends of the stream sit on opposite sides of the sandbox boundary:
// the controller (RunClient) is trusted, the guest (GuestListeners.Serve) is
// not, and root inside the guest can write any frame it likes onto the
// stream. The payload of an Open frame -- the address the receiving side is
// asked to dial -- is therefore untrusted input. Each side dials only the
// targets of the specs it was itself configured with and answers any other
// Open with an OpenErr, so a guest cannot turn the controller into a TCP
// proxy to whatever the controller can reach (a metadata server, its own
// loopback listeners, cluster services).
package forward

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// Direction identifies which side of the stream binds the listening socket.
type Direction int

const (
	// LocalToRemote (-L) binds on the host/controller and dials inside the guest.
	LocalToRemote Direction = iota + 1
	// RemoteToLocal (-R) binds inside the guest and dials on the host/controller.
	RemoteToLocal
)

// Spec describes a single TCP port forward.
type Spec struct {
	Direction  Direction
	BindHost   string
	BindPort   int
	TargetHost string
	TargetPort int
}

// BindAddr returns the host:port to listen on.
func (s Spec) BindAddr() string {
	host := s.BindHost
	if host == "" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, strconv.Itoa(s.BindPort))
}

// TargetAddr returns the host:port to dial when a connection is accepted.
func (s Spec) TargetAddr() string {
	host := s.TargetHost
	if host == "" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, strconv.Itoa(s.TargetPort))
}

// String formats the spec as "bindHost:bindPort:targetHost:targetPort".
func (s Spec) String() string {
	bindHost := s.BindHost
	if bindHost == "" {
		bindHost = "127.0.0.1"
	}
	targetHost := s.TargetHost
	if targetHost == "" {
		targetHost = "127.0.0.1"
	}
	return fmt.Sprintf("%s:%d:%s:%d", bindHost, s.BindPort, targetHost, s.TargetPort)
}

// ParseSpec parses a port-forward specification in any of the forms:
//   - "bindHost:bindPort:targetHost:targetPort"
//   - "bindPort:targetHost:targetPort" (bindHost defaults to 127.0.0.1)
//   - "bindPort:targetPort" (both hosts default to 127.0.0.1)
func ParseSpec(dir Direction, raw string) (Spec, error) {
	parts := strings.Split(strings.TrimSpace(raw), ":")
	var bindHost, bindPortStr, targetHost, targetPortStr string
	switch len(parts) {
	case 2:
		bindHost = "127.0.0.1"
		bindPortStr = parts[0]
		targetHost = "127.0.0.1"
		targetPortStr = parts[1]
	case 3:
		bindHost = "127.0.0.1"
		bindPortStr = parts[0]
		targetHost = parts[1]
		targetPortStr = parts[2]
	case 4:
		bindHost = parts[0]
		if bindHost == "" {
			bindHost = "127.0.0.1"
		}
		bindPortStr = parts[1]
		targetHost = parts[2]
		if targetHost == "" {
			targetHost = "127.0.0.1"
		}
		targetPortStr = parts[3]
	default:
		return Spec{}, fmt.Errorf("invalid forward spec %q: want [bind_host:]port:target_host:target_port", raw)
	}
	bindPort, err := parsePort(bindPortStr)
	if err != nil {
		return Spec{}, fmt.Errorf("invalid bind port in %q: %w", raw, err)
	}
	targetPort, err := parsePort(targetPortStr)
	if err != nil {
		return Spec{}, fmt.Errorf("invalid target port in %q: %w", raw, err)
	}
	return Spec{
		Direction:  dir,
		BindHost:   bindHost,
		BindPort:   bindPort,
		TargetHost: targetHost,
		TargetPort: targetPort,
	}, nil
}

func parsePort(s string) (int, error) {
	p, err := strconv.Atoi(s)
	if err != nil || p < 1 || p > 65535 {
		return 0, fmt.Errorf("port %q out of range 1..65535", s)
	}
	return p, nil
}

// Wire frame types.
const (
	frameReady      byte = 1
	frameOpen       byte = 2
	frameOpenOK     byte = 3
	frameOpenErr    byte = 4
	frameData       byte = 5
	frameCloseWrite byte = 6
	frameClose      byte = 7

	headerLen      = 9
	maxPayloadSize = 1 << 20 // 1 MiB
	copyChunkSize  = 32 * 1024

	// maxStreams caps the streams a session holds open at once, counting
	// those either side opened, so a peer cannot make this side hold an
	// unbounded number of connections, goroutines and queues.
	maxStreams = 256
	// maxQueuedBytes caps the Data payload queued for one stream's
	// connection while its target reads slowly. A frame is always admitted
	// to an empty queue, so at most maxQueuedBytes+maxPayloadSize is held.
	maxQueuedBytes = 1 << 20 // 1 MiB
)

type mux struct {
	r       io.Reader
	w       io.Writer
	writeMu sync.Mutex

	nextID uint32
	// ownParity is nextID's parity: the ids this side allocates. A peer's
	// Open must name an id of the other parity.
	ownParity uint32

	// allowed maps the normalised form of every target an incoming Open
	// may name to the address this side dials for it, which is taken from
	// its own spec rather than from the frame. nil allows any target; a
	// non-nil empty map allows none.
	allowed map[string]string

	mu      sync.Mutex
	streams map[uint32]*muxStream
	closed  bool
	done    chan struct{}
}

type muxStream struct {
	id      uint32
	conn    net.Conn
	openCh  chan error
	writeCh chan frameMsg
	closeCh chan struct{}
	once    sync.Once

	// localEOF and remoteEOF record whether each direction has closed for
	// writing (local conn read EOF and sent frameCloseWrite; peer sent
	// frameCloseWrite). Protected by m.mu.
	localEOF  bool
	remoteEOF bool

	// queued counts the Data bytes in writeCh; drained is signalled each
	// time the writer takes a frame off it, to wake readLoop when it is
	// waiting for room.
	queued  atomic.Int64
	drained chan struct{}
}

type frameMsg struct {
	typ     byte
	payload []byte
}

func newMux(r io.Reader, w io.Writer, clientSide bool) *mux {
	start := uint32(1)
	if !clientSide {
		start = 2
	}
	return &mux{
		r:         r,
		w:         w,
		nextID:    start,
		ownParity: start % 2,
		streams:   make(map[uint32]*muxStream),
		done:      make(chan struct{}),
	}
}

// normaliseAddr returns addr in a canonical host:port form, so that two
// spellings of the same target compare equal: the host lower-cased and, if it
// is an IP literal, in net.IP's own formatting, and the port as a plain
// decimal number. It fails on anything net.SplitHostPort does not accept.
func normaliseAddr(addr string) (string, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return "", err
	}
	port, err := parsePort(portStr)
	if err != nil {
		return "", err
	}
	host = strings.ToLower(host)
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	}
	if host == "" {
		return "", fmt.Errorf("address %q has no host", addr)
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}

// allowTargets returns the allow-list for the targets of the specs in dir.
// It is never nil, so a side configured with no such specs refuses every
// incoming Open.
func allowTargets(specs []Spec, dir Direction) map[string]string {
	allowed := make(map[string]string)
	for _, spec := range specs {
		if spec.Direction != dir {
			continue
		}
		target := spec.TargetAddr()
		key, err := normaliseAddr(target)
		if err != nil {
			continue
		}
		allowed[key] = target
	}
	return allowed
}

// resolveTarget looks requested up in the allow-list and returns the
// address to dial for it.
func (m *mux) resolveTarget(requested string) (string, error) {
	if m.allowed == nil {
		return requested, nil
	}
	key, err := normaliseAddr(requested)
	if err != nil {
		return "", fmt.Errorf("forward: target %q is not a host:port: %v", requested, err)
	}
	target, ok := m.allowed[key]
	if !ok {
		return "", fmt.Errorf("forward: target %s is not one this session forwards", requested)
	}
	return target, nil
}

func (m *mux) allocID() uint32 {
	return atomic.AddUint32(&m.nextID, 2) - 2
}

func (m *mux) writeFrame(typ byte, id uint32, payload []byte) error {
	m.writeMu.Lock()
	defer m.writeMu.Unlock()

	var hdr [headerLen]byte
	hdr[0] = typ
	binary.BigEndian.PutUint32(hdr[1:5], id)
	binary.BigEndian.PutUint32(hdr[5:9], uint32(len(payload)))
	if _, err := m.w.Write(hdr[:]); err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := m.w.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

var errMuxClosed = errors.New("forward: session closed")

// registerStream adds a stream under id. It refuses an id already in use
// and, once maxStreams are open, any id at all. conn may be nil for an
// incoming stream reserved before it is dialed; see attachConn.
func (m *mux) registerStream(id uint32, conn net.Conn, waitingOpen bool) (*muxStream, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, errMuxClosed
	}
	if _, ok := m.streams[id]; ok {
		return nil, fmt.Errorf("forward: stream %d refused: its id is already in use", id)
	}
	if len(m.streams) >= maxStreams {
		return nil, fmt.Errorf("forward: stream %d refused: %d streams are already open, the most a session allows", id, maxStreams)
	}
	st := &muxStream{
		id:      id,
		conn:    conn,
		writeCh: make(chan frameMsg, 64),
		closeCh: make(chan struct{}),
		drained: make(chan struct{}, 1),
	}
	if waitingOpen {
		st.openCh = make(chan error, 1)
	}
	m.streams[id] = st
	return st, nil
}

// reserveIncoming registers the stream a peer's Open names before anything
// is dialed for it, so that an id of this side's own parity, one already in
// use (including by an Open still being dialed) and an Open past maxStreams
// are all refused without a dial or a goroutine.
func (m *mux) reserveIncoming(id uint32) (*muxStream, error) {
	if id == 0 || id%2 == m.ownParity {
		return nil, fmt.Errorf("forward: stream %d refused: its id is not one the peer allocates", id)
	}
	return m.registerStream(id, nil, false)
}

// attachConn gives a reserved stream the connection dialed for it. It
// reports false, having closed conn, if the stream was removed meanwhile.
func (m *mux) attachConn(st *muxStream, conn net.Conn) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.streams[st.id] != st {
		_ = conn.Close()
		return false
	}
	st.conn = conn
	return true
}

func (m *mux) removeStream(id uint32) *muxStream {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.streams[id]
	if ok {
		delete(m.streams, id)
	}
	return st
}

func (st *muxStream) closeLocal() {
	st.once.Do(func() {
		close(st.closeCh)
		if st.conn != nil {
			_ = st.conn.Close()
		}
	})
}

// closeHalf records that one direction of st has finished writing (local true
// when the local conn read EOF and sent frameCloseWrite; local false when
// frameCloseWrite was received from the peer). Once both halves have closed,
// the stream is removed and its local connection is closed.
func (m *mux) closeHalf(st *muxStream, local bool) bool {
	m.mu.Lock()
	if local {
		st.localEOF = true
	} else {
		st.remoteEOF = true
	}
	both := st.localEOF && st.remoteEOF
	m.mu.Unlock()
	if both {
		m.removeStream(st.id)
		st.closeLocal()
	}
	return both
}

func (m *mux) closeAll() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	streams := make([]*muxStream, 0, len(m.streams))
	for _, st := range m.streams {
		streams = append(streams, st)
	}
	m.streams = make(map[uint32]*muxStream)
	close(m.done)
	m.mu.Unlock()

	for _, st := range streams {
		if st.openCh != nil {
			select {
			case st.openCh <- io.ErrClosedPipe:
			default:
			}
		}
		st.closeLocal()
	}
}

func (m *mux) acceptAndForward(ctx context.Context, ln net.Listener, targetAddr string) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go m.handleAccepted(ctx, conn, targetAddr)
	}
}

func (m *mux) handleAccepted(ctx context.Context, conn net.Conn, targetAddr string) {
	id := m.allocID()
	st, err := m.registerStream(id, conn, true)
	if err != nil {
		if !errors.Is(err, errMuxClosed) {
			slog.Warn("forward: refused a local connection", "err", err)
		}
		_ = conn.Close()
		return
	}
	if err := m.writeFrame(frameOpen, id, []byte(targetAddr)); err != nil {
		m.removeStream(id)
		st.closeLocal()
		return
	}
	select {
	case err := <-st.openCh:
		if err != nil {
			m.removeStream(id)
			st.closeLocal()
			return
		}
	case <-m.done:
		st.closeLocal()
		return
	case <-ctx.Done():
		st.closeLocal()
		return
	}
	m.pumpStream(ctx, st)
}

// handleIncomingOpen dials targetAddr for st, a stream readLoop reserved
// for a peer's Open.
func (m *mux) handleIncomingOpen(ctx context.Context, st *muxStream, targetAddr string) {
	id := st.id
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp", targetAddr)
	if err != nil {
		m.removeStream(id)
		st.closeLocal()
		_ = m.writeFrame(frameOpenErr, id, []byte(err.Error()))
		return
	}
	if !m.attachConn(st, conn) {
		return
	}
	if err := m.writeFrame(frameOpenOK, id, nil); err != nil {
		m.removeStream(id)
		st.closeLocal()
		return
	}
	m.pumpStream(ctx, st)
}

func (m *mux) pumpStream(ctx context.Context, st *muxStream) {
	var wg sync.WaitGroup
	wg.Add(2)

	// Writer to TCP connection from incoming mux frames.
	go func() {
		defer wg.Done()
		for {
			select {
			case msg := <-st.writeCh:
				if msg.typ == frameData {
					st.queued.Add(-int64(len(msg.payload)))
					select {
					case st.drained <- struct{}{}:
					default:
					}
				}
				switch msg.typ {
				case frameData:
					if _, err := st.conn.Write(msg.payload); err != nil {
						_ = m.writeFrame(frameClose, st.id, nil)
						m.removeStream(st.id)
						st.closeLocal()
						return
					}
				case frameCloseWrite:
					if cw, ok := st.conn.(interface{ CloseWrite() error }); ok {
						_ = cw.CloseWrite()
					}
					if m.closeHalf(st, false) {
						return
					}
				case frameClose:
					m.removeStream(st.id)
					st.closeLocal()
					return
				}
			case <-st.closeCh:
				return
			case <-m.done:
				st.closeLocal()
				return
			case <-ctx.Done():
				st.closeLocal()
				return
			}
		}
	}()

	// Reader from TCP connection to outgoing mux frames.
	go func() {
		defer wg.Done()
		buf := make([]byte, copyChunkSize)
		for {
			n, err := st.conn.Read(buf)
			if n > 0 {
				if werr := m.writeFrame(frameData, st.id, buf[:n]); werr != nil {
					m.removeStream(st.id)
					st.closeLocal()
					return
				}
			}
			if err != nil {
				if errors.Is(err, io.EOF) {
					_ = m.writeFrame(frameCloseWrite, st.id, nil)
					m.closeHalf(st, true)
				} else {
					_ = m.writeFrame(frameClose, st.id, nil)
					m.removeStream(st.id)
					st.closeLocal()
				}
				return
			}
		}
	}()

	wg.Wait()
	m.removeStream(st.id)
	st.closeLocal()
}

func (m *mux) readLoop(ctx context.Context, onReady func()) error {
	defer m.closeAll()

	var readyOnce sync.Once
	fireReady := func() {
		if onReady != nil {
			readyOnce.Do(onReady)
		}
	}

	var hdr [headerLen]byte
	for {
		if _, err := io.ReadFull(m.r, hdr[:]); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil
			}
			return err
		}
		typ := hdr[0]
		streamID := binary.BigEndian.Uint32(hdr[1:5])
		length := binary.BigEndian.Uint32(hdr[5:9])
		if length > maxPayloadSize {
			return fmt.Errorf("forward frame payload too large: %d bytes", length)
		}
		var payload []byte
		if length > 0 {
			payload = make([]byte, length)
			if _, err := io.ReadFull(m.r, payload); err != nil {
				return err
			}
		}

		switch typ {
		case frameReady:
			fireReady()
		case frameOpen:
			// Refusals are answered here, before any dial or goroutine,
			// so a peer flooding Opens costs this side only the reply.
			requested := string(payload)
			targetAddr, err := m.resolveTarget(requested)
			if err != nil {
				slog.Warn("forward: refused an open for a target outside this session's specs", "stream", streamID, "target", requested)
				_ = m.writeFrame(frameOpenErr, streamID, []byte(err.Error()))
				continue
			}
			st, err := m.reserveIncoming(streamID)
			if err != nil {
				if errors.Is(err, errMuxClosed) {
					continue
				}
				slog.Warn("forward: refused an open", "stream", streamID, "err", err)
				_ = m.writeFrame(frameOpenErr, streamID, []byte(err.Error()))
				continue
			}
			go m.handleIncomingOpen(ctx, st, targetAddr)
		case frameOpenOK:
			m.mu.Lock()
			st := m.streams[streamID]
			m.mu.Unlock()
			if st != nil && st.openCh != nil {
				select {
				case st.openCh <- nil:
				default:
				}
			}
		case frameOpenErr:
			m.mu.Lock()
			st := m.streams[streamID]
			m.mu.Unlock()
			if st != nil && st.openCh != nil {
				select {
				case st.openCh <- errors.New(string(payload)):
				default:
				}
			}
		case frameData, frameCloseWrite, frameClose:
			m.mu.Lock()
			st := m.streams[streamID]
			m.mu.Unlock()
			if st != nil {
				m.enqueue(st, frameMsg{typ: typ, payload: payload})
			}
		default:
			return fmt.Errorf("unknown forward frame type %d", typ)
		}
	}
}

// enqueue hands msg to st's writer, first waiting while st already has
// maxQueuedBytes of Data queued. Waiting stalls readLoop, which is the
// session's back-pressure on a peer sending faster than a target reads.
func (m *mux) enqueue(st *muxStream, msg frameMsg) {
	if msg.typ == frameData {
		n := int64(len(msg.payload))
		for {
			q := st.queued.Load()
			if q == 0 || q+n <= maxQueuedBytes {
				break
			}
			select {
			case <-st.drained:
			case <-st.closeCh:
				return
			case <-m.done:
				return
			}
		}
		st.queued.Add(n)
	}
	select {
	case st.writeCh <- msg:
	case <-st.closeCh:
	case <-m.done:
	}
}

// RunClient runs the host/controller side of a port-forward session over r and w.
// Any LocalToRemote (-L) specs in specs are bound locally on the caller; RemoteToLocal
// (-R) connections opened by the guest are dialed locally by the caller, and
// only to the target of one of the RemoteToLocal specs: an Open naming any
// other address is refused without being dialed.
func RunClient(ctx context.Context, r io.Reader, w io.Writer, specs []Spec, onReady func()) error {
	var listeners []net.Listener
	defer func() {
		for _, ln := range listeners {
			_ = ln.Close()
		}
	}()

	var localSpecs []Spec
	for _, spec := range specs {
		if spec.Direction == LocalToRemote {
			ln, err := net.Listen("tcp", spec.BindAddr())
			if err != nil {
				return fmt.Errorf("binding local forward %s: %w", spec.BindAddr(), err)
			}
			listeners = append(listeners, ln)
			localSpecs = append(localSpecs, spec)
		}
	}

	m := newMux(r, w, true)
	m.allowed = allowTargets(specs, RemoteToLocal)
	for i, ln := range listeners {
		go m.acceptAndForward(ctx, ln, localSpecs[i].TargetAddr())
	}

	go func() {
		select {
		case <-ctx.Done():
			for _, ln := range listeners {
				_ = ln.Close()
			}
			m.closeAll()
		case <-m.done:
		}
	}()

	return m.readLoop(ctx, onReady)
}

// GuestListeners holds the TCP listeners bound inside the guest for a forward session.
type GuestListeners struct {
	listeners []net.Listener
	specs     []Spec
	// allowed restricts the targets the controller may open in the guest;
	// nil when the guest was told no LocalToRemote specs.
	allowed map[string]string
}

// BindGuestListeners binds all RemoteToLocal (-R) specs inside the guest.
//
// If specs also lists LocalToRemote (-L) specs, Serve dials only their targets
// when the controller opens a stream. If it lists none, the controller may
// open any target: the controller is the trusted side of the session, and the
// guest agent is not told the -L specs today.
func BindGuestListeners(remoteSpecs []Spec) (*GuestListeners, error) {
	var listeners []net.Listener
	var boundSpecs []Spec
	var allowed map[string]string
	for _, spec := range remoteSpecs {
		if spec.Direction == LocalToRemote && allowed == nil {
			allowed = allowTargets(remoteSpecs, LocalToRemote)
		}
		if spec.Direction != RemoteToLocal {
			continue
		}
		ln, err := net.Listen("tcp", spec.BindAddr())
		if err != nil {
			for _, prev := range listeners {
				_ = prev.Close()
			}
			return nil, fmt.Errorf("binding guest forward %s: %w", spec.BindAddr(), err)
		}
		listeners = append(listeners, ln)
		boundSpecs = append(boundSpecs, spec)
	}
	return &GuestListeners{listeners: listeners, specs: boundSpecs, allowed: allowed}, nil
}

// Close closes all bound guest listeners.
func (g *GuestListeners) Close() {
	if g == nil {
		return
	}
	for _, ln := range g.listeners {
		_ = ln.Close()
	}
}

// Serve runs the guest side of the forward session over r and w.
func (g *GuestListeners) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	defer g.Close()

	m := newMux(r, w, false)
	m.allowed = g.allowed
	if err := m.writeFrame(frameReady, 0, nil); err != nil {
		return err
	}
	for i, ln := range g.listeners {
		go m.acceptAndForward(ctx, ln, g.specs[i].TargetAddr())
	}

	go func() {
		select {
		case <-ctx.Done():
			g.Close()
			m.closeAll()
		case <-m.done:
		}
	}()

	return m.readLoop(ctx, nil)
}

// ServeGuest binds remoteSpecs inside the guest and serves the forward session over r and w.
func ServeGuest(ctx context.Context, r io.Reader, w io.Writer, remoteSpecs []Spec) error {
	gl, err := BindGuestListeners(remoteSpecs)
	if err != nil {
		return err
	}
	return gl.Serve(ctx, r, w)
}
