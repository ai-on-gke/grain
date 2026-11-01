package sandbox

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bwsalmon/granule/pkg/execwire"
	"github.com/bwsalmon/granule/pkg/guestexec"
	"github.com/bwsalmon/granule/pkg/guestforward"
)

// ServerConfig configures a wrapper-side sandbox access Server.
type ServerConfig struct {
	// SandboxID uniquely identifies this sandbox.
	SandboxID string

	// Token is the per-sandbox bearer token used to authenticate incoming
	// connections in listen mode and outgoing connections to the
	// controller in dial-out mode. On a unix-socket listener it is checked
	// when non-empty; on a tcp:// listener it is mandatory (Serve refuses
	// to start without one, and every connection must present it).
	Token string

	// TLSConfig carries the server certificate for a tcp:// listener,
	// which always serves TLS 1.3 (MinVersion is forced). Required for
	// tcp://; ignored for unix sockets.
	TLSConfig *tls.Config

	// WrapperVersion is an optional version string reported in HealthInfo.
	WrapperVersion string

	// Guest configures how the wrapper reaches granule-agent inside the VM
	// over virtio-vsock.
	Guest guestexec.Config

	// DialGuest overrides how the server opens a raw connection to
	// granule-agent. When nil, guestexec.Dial(ctx, Guest) is used.
	DialGuest func(ctx context.Context) (net.Conn, error)

	// MuxConfig tunes the underlying guestforward.Session per connection.
	MuxConfig guestforward.Config

	// MaxExecBufferBytes bounds the per-exec output replay buffer retained
	// in the wrapper for Reattach across transport reconnects. Zero
	// defaults to maxExecBufferBytes (16 MiB).
	MaxExecBufferBytes int64

	// CompletedExecRetention is how long a completed exec session is kept
	// in the wrapper's session table for Reattach after the guest command
	// exits. Zero defaults to completedExecRetention (10 minutes).
	CompletedExecRetention time.Duration
}

const (
	maxExecBufferBytes     = 16 << 20
	completedExecRetention = 10 * time.Minute
)

// DialOutOptions configures TLS and reconnection backoff for Server.DialOut.
type DialOutOptions struct {
	// TLSConfig is an optional base *tls.Config. Minimum TLS version is
	// always enforced at TLS 1.3.
	TLSConfig *tls.Config

	// CAFile is an optional PEM bundle of trusted root CAs for verifying
	// the controller's certificate.
	CAFile string

	// CertFile and KeyFile are optional client certificate and key files
	// for mutual TLS (mTLS).
	CertFile string
	KeyFile  string

	// MinBackoff and MaxBackoff bound the reconnect delay when a dial-out
	// connection drops. Defaults to 50ms and 5s.
	MinBackoff time.Duration
	MaxBackoff time.Duration
}

// Server serves the granule sandbox access API in the pod-side wrapper
// (outside the guest VM).
type Server struct {
	cfg     ServerConfig
	execSeq atomic.Uint64

	mu    sync.Mutex
	execs map[string]*managedExec
}

// NewServer creates a new wrapper-side sandbox access server.
func NewServer(cfg ServerConfig) *Server {
	if cfg.Guest.Socket == "" {
		cfg.Guest.Socket = guestexec.DefaultSocket
	}
	if cfg.Guest.User == "" {
		cfg.Guest.User = "root"
	}
	if cfg.Guest.ConnectTimeout == 0 {
		cfg.Guest.ConnectTimeout = 30 * time.Second
	}
	if cfg.MaxExecBufferBytes <= 0 {
		cfg.MaxExecBufferBytes = maxExecBufferBytes
	}
	if cfg.CompletedExecRetention <= 0 {
		cfg.CompletedExecRetention = completedExecRetention
	}
	return &Server{
		cfg:   cfg,
		execs: make(map[string]*managedExec),
	}
}

func (s *Server) dialGuest(ctx context.Context) (net.Conn, error) {
	if s.cfg.DialGuest != nil {
		return s.cfg.DialGuest(ctx)
	}
	return guestexec.Dial(ctx, s.cfg.Guest)
}

// Listen creates a net.Listener for listenSpec ("unix:///path", "unix:/path",
// "/path", or "tcp://host:port"). For unix sockets, any stale socket file is
// removed before binding and the parent directory is created if needed.
func Listen(listenSpec string) (net.Listener, error) {
	spec := strings.TrimSpace(listenSpec)
	switch {
	case strings.HasPrefix(spec, "tcp://"):
		addr := strings.TrimPrefix(spec, "tcp://")
		return net.Listen("tcp", addr)
	case strings.HasPrefix(spec, "unix://"):
		return listenUnix(strings.TrimPrefix(spec, "unix://"))
	case strings.HasPrefix(spec, "unix:"):
		return listenUnix(strings.TrimPrefix(spec, "unix:"))
	case strings.HasPrefix(spec, "/"):
		return listenUnix(spec)
	default:
		return nil, fmt.Errorf("sandbox: unsupported listen address %q (expected unix:/path or tcp://host:port)", listenSpec)
	}
}

func listenUnix(sockPath string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(sockPath), 0o755); err != nil {
		return nil, fmt.Errorf("creating socket directory: %w", err)
	}
	_ = os.Remove(sockPath)
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(sockPath, 0o600)
	return ln, nil
}

// Serve accepts incoming listen-mode connections on ln until ctx is cancelled
// or ln is closed.
//
// A TCP listener is a network endpoint other pods can reach, so Serve
// hardens it: it refuses to start without a per-sandbox Token
// (ErrTokenRequired) or a server certificate in TLSConfig
// (ErrTLSRequired), and wraps ln so every connection is TLS 1.3. Pass the
// plain listener from Listen ("tcp://..."), not one already wrapped in TLS.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	if isNetworkAddr(ln.Addr()) {
		tlsCfg, err := s.listenTLS()
		if err != nil {
			_ = ln.Close()
			return err
		}
		ln = tls.NewListener(ln, tlsCfg)
	}
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go func(c net.Conn) {
			_ = s.ServeListenConn(ctx, c)
		}(conn)
	}
}

// listenTLS checks the configuration a TCP listener needs and returns the
// TLS 1.3 server config to wrap it with.
func (s *Server) listenTLS() (*tls.Config, error) {
	if s.cfg.Token == "" {
		return nil, ErrTokenRequired
	}
	if s.cfg.TLSConfig == nil || (len(s.cfg.TLSConfig.Certificates) == 0 && s.cfg.TLSConfig.GetCertificate == nil && s.cfg.TLSConfig.GetConfigForClient == nil) {
		return nil, ErrTLSRequired
	}
	cfg := s.cfg.TLSConfig.Clone()
	cfg.MinVersion = tls.VersionTLS13
	return cfg, nil
}

// isNetworkAddr reports whether a is a TCP address, as opposed to a unix
// socket or vsock port local to the host.
func isNetworkAddr(a net.Addr) bool {
	if a == nil {
		return false
	}
	switch a.Network() {
	case "tcp", "tcp4", "tcp6":
		return true
	}
	return false
}

// ListenAndServe listens on listenSpec and serves incoming connections until
// ctx is cancelled. A tcp:// listenSpec requires Token and TLSConfig (see
// Serve); both are checked before the port is bound.
func (s *Server) ListenAndServe(ctx context.Context, listenSpec string) error {
	if strings.HasPrefix(strings.TrimSpace(listenSpec), "tcp://") {
		if _, err := s.listenTLS(); err != nil {
			return err
		}
	}
	ln, err := Listen(listenSpec)
	if err != nil {
		return err
	}
	defer ln.Close()
	return s.Serve(ctx, ln)
}

// ServeListenConn performs the listen-mode handshake on conn and serves
// multiplexed streams until the session ends.
func (s *Server) ServeListenConn(ctx context.Context, conn net.Conn) error {
	defer conn.Close()
	br := bufio.NewReader(conn)

	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	var hs HandshakeRequest
	if err := readJSONLine(br, &hs); err != nil {
		return err
	}
	if hs.Version != ProtocolVersion {
		_ = writeJSONLine(conn, HandshakeResponse{
			OK:    false,
			Error: fmt.Sprintf("unsupported protocol version %q, want %q", hs.Version, ProtocolVersion),
		})
		return ErrAuthFailed
	}
	// Over TCP the token is never optional: an unset Token refuses every
	// connection rather than admitting them all.
	network := isNetworkAddr(conn.LocalAddr())
	if network && s.cfg.Token == "" {
		_ = writeJSONLine(conn, HandshakeResponse{
			OK:    false,
			Error: "access token required on a network listener",
		})
		return ErrAuthFailed
	}
	if s.cfg.Token != "" {
		if hs.Token == "" || subtle.ConstantTimeCompare([]byte(hs.Token), []byte(s.cfg.Token)) != 1 {
			_ = writeJSONLine(conn, HandshakeResponse{
				OK:    false,
				Error: "invalid access token",
			})
			return ErrAuthFailed
		}
	}
	if s.cfg.SandboxID != "" && hs.SandboxID != "" && hs.SandboxID != s.cfg.SandboxID {
		_ = writeJSONLine(conn, HandshakeResponse{
			OK:    false,
			Error: fmt.Sprintf("sandbox_id mismatch: got %q, want %q", hs.SandboxID, s.cfg.SandboxID),
		})
		return ErrAuthFailed
	}
	if err := writeJSONLine(conn, HandshakeResponse{
		OK:        true,
		Version:   ProtocolVersion,
		SandboxID: s.cfg.SandboxID,
	}); err != nil {
		return err
	}
	_ = conn.SetDeadline(time.Time{})

	return s.serveMux(ctx, &bufferedConn{Reader: br, Conn: conn}, false)
}

// DialOut connects out to controllerAddr over TLS 1.3, authenticates with the
// sandbox's ID and token, and serves multiplexed streams opened by the
// controller. Whenever the connection drops, DialOut reconnects with
// exponential backoff until ctx is cancelled, preserving in-flight exec
// sessions across reconnects.
func (s *Server) DialOut(ctx context.Context, controllerAddr string, opts DialOutOptions) error {
	tlsCfg, hostPort, err := buildDialOutTLS(controllerAddr, opts)
	if err != nil {
		return err
	}
	minBackoff := opts.MinBackoff
	if minBackoff <= 0 {
		minBackoff = 50 * time.Millisecond
	}
	maxBackoff := opts.MaxBackoff
	if maxBackoff <= 0 {
		maxBackoff = 5 * time.Second
	}

	backoff := minBackoff
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		connectedAt := time.Now()
		err := s.dialOutOnce(ctx, hostPort, tlsCfg)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, ErrAuthFailed) {
			return err
		}
		if time.Since(connectedAt) > 2*maxBackoff {
			backoff = minBackoff
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func (s *Server) dialOutOnce(ctx context.Context, hostPort string, tlsCfg *tls.Config) error {
	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 10 * time.Second},
		Config:    tlsCfg,
	}
	conn, err := dialer.DialContext(ctx, "tcp", hostPort)
	if err != nil {
		return err
	}
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if err := writeJSONLine(conn, HandshakeRequest{
		Version:   ProtocolVersion,
		SandboxID: s.cfg.SandboxID,
		Token:     s.cfg.Token,
	}); err != nil {
		return err
	}
	br := bufio.NewReader(conn)
	var resp HandshakeResponse
	if err := readJSONLine(br, &resp); err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("%w: %s", ErrAuthFailed, resp.Error)
	}
	_ = conn.SetDeadline(time.Time{})

	// Pass client=false so the controller (which passes client=true) uses odd
	// stream IDs and the wrapper uses even stream IDs without collision.
	return s.serveMux(ctx, &bufferedConn{Reader: br, Conn: conn}, false)
}

func buildDialOutTLS(controllerAddr string, opts DialOutOptions) (*tls.Config, string, error) {
	hostPort := strings.TrimSpace(controllerAddr)
	if strings.Contains(hostPort, "://") {
		u, err := url.Parse(hostPort)
		if err != nil {
			return nil, "", fmt.Errorf("invalid controller URL %q: %w", controllerAddr, err)
		}
		switch u.Scheme {
		case "https", "tls", "grpcs":
		default:
			return nil, "", fmt.Errorf("unsupported controller URL scheme %q (want https:// or tls://)", u.Scheme)
		}
		hostPort = u.Host
	}
	host, _, err := net.SplitHostPort(hostPort)
	if err != nil {
		return nil, "", fmt.Errorf("controller address %q must be host:port: %w", controllerAddr, err)
	}

	var base *tls.Config
	if opts.TLSConfig != nil {
		base = opts.TLSConfig.Clone()
	} else {
		base = &tls.Config{}
	}
	base.MinVersion = tls.VersionTLS13
	if base.ServerName == "" {
		base.ServerName = host
	}
	if opts.CAFile != "" {
		pemBytes, err := os.ReadFile(opts.CAFile)
		if err != nil {
			return nil, "", fmt.Errorf("reading CA file %s: %w", opts.CAFile, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pemBytes) {
			return nil, "", fmt.Errorf("CA file %s contains no valid certificates", opts.CAFile)
		}
		base.RootCAs = pool
	}
	if opts.CertFile != "" || opts.KeyFile != "" {
		cert, err := tls.LoadX509KeyPair(opts.CertFile, opts.KeyFile)
		if err != nil {
			return nil, "", fmt.Errorf("loading client cert/key: %w", err)
		}
		base.Certificates = []tls.Certificate{cert}
	}
	return base, hostPort, nil
}

func (s *Server) serveMux(ctx context.Context, conn io.ReadWriteCloser, isClient bool) error {
	sess := guestforward.NewSession(conn, isClient, s.cfg.MuxConfig)
	defer sess.Close()

	go func() {
		select {
		case <-ctx.Done():
			_ = sess.Close()
		case <-sess.Done():
		}
	}()

	for {
		st, err := sess.Accept(ctx)
		if err != nil {
			return err
		}
		go s.handleStream(ctx, st)
	}
}

func (s *Server) handleStream(ctx context.Context, st *guestforward.Stream) {
	var req StreamRequest
	if err := json.Unmarshal(st.Meta(), &req); err != nil {
		_ = st.Reject("invalid stream request JSON: " + err.Error())
		return
	}
	switch req.Op {
	case OpExec:
		s.handleExec(ctx, st, req.Exec)
	case OpReattach:
		s.handleReattach(ctx, st, req.Reattach)
	case OpSignal:
		s.handleSignal(st, req.Signal)
	case OpPutFiles:
		s.handlePutFiles(ctx, st)
	case OpGetFile:
		s.handleGetFile(ctx, st, req.GetFile)
	case OpHealth:
		s.handleHealth(ctx, st)
	case OpForward:
		s.handleForward(ctx, st, req.Forward)
	default:
		_ = st.Reject(fmt.Sprintf("unknown operation %q", req.Op))
	}
}

type outputChunk struct {
	typ         byte
	data        []byte
	startOffset int64
	endOffset   int64
}

type managedExec struct {
	id        string
	guestConn net.Conn
	br        *bufio.Reader
	features  []string
	maxBuf    int64
	onExit    func()

	writeMu sync.Mutex

	mu            sync.Mutex
	cond          *sync.Cond
	chunks        []outputChunk
	baseIdx       int
	bufferedBytes int64
	totalBytes    int64
	exited        bool
	exitCode      int
	transportCut  bool
	transportErr  string
}

func newManagedExec(id string, conn net.Conn, br *bufio.Reader, features []string, maxBuf int64, onExit func()) *managedExec {
	ex := &managedExec{
		id:        id,
		guestConn: conn,
		br:        br,
		features:  features,
		maxBuf:    maxBuf,
		onExit:    onExit,
	}
	ex.cond = sync.NewCond(&ex.mu)
	go ex.readLoop()
	return ex
}

func (ex *managedExec) trimLocked() {
	if ex.maxBuf <= 0 {
		return
	}
	trimmed := 0
	for len(ex.chunks)-trimmed > 1 && ex.bufferedBytes > ex.maxBuf {
		c := ex.chunks[trimmed]
		if c.typ == execwire.TypeStdout || c.typ == execwire.TypeStderr {
			ex.bufferedBytes -= int64(len(c.data))
		}
		trimmed++
	}
	if trimmed > 0 {
		remaining := make([]outputChunk, len(ex.chunks)-trimmed)
		copy(remaining, ex.chunks[trimmed:])
		ex.chunks = remaining
		ex.baseIdx += trimmed
	}
}

func (ex *managedExec) writeFrame(typ byte, payload []byte) error {
	ex.writeMu.Lock()
	defer ex.writeMu.Unlock()
	return execwire.WriteFrame(ex.guestConn, typ, payload)
}

func (ex *managedExec) readLoop() {
	defer func() {
		_ = ex.guestConn.Close()
		if ex.onExit != nil {
			ex.onExit()
		}
	}()
	for {
		typ, payload, err := execwire.ReadFrame(ex.br)
		if err != nil {
			ex.mu.Lock()
			if !ex.exited {
				ex.exited = true
				ex.transportCut = true
				ex.transportErr = err.Error()
				ex.cond.Broadcast()
			}
			ex.mu.Unlock()
			return
		}
		switch typ {
		case execwire.TypeStdout, execwire.TypeStderr:
			data := make([]byte, len(payload))
			copy(data, payload)
			ex.mu.Lock()
			start := ex.totalBytes
			end := start + int64(len(data))
			ex.totalBytes = end
			ex.bufferedBytes += int64(len(data))
			ex.chunks = append(ex.chunks, outputChunk{
				typ:         typ,
				data:        data,
				startOffset: start,
				endOffset:   end,
			})
			ex.trimLocked()
			ex.cond.Broadcast()
			ex.mu.Unlock()
		case execwire.TypeNote:
			data := make([]byte, len(payload))
			copy(data, payload)
			ex.mu.Lock()
			off := ex.totalBytes
			ex.chunks = append(ex.chunks, outputChunk{
				typ:         typ,
				data:        data,
				startOffset: off,
				endOffset:   off,
			})
			ex.cond.Broadcast()
			ex.mu.Unlock()
		case execwire.TypeExit:
			code, decErr := execwire.DecodeExit(payload)
			ex.mu.Lock()
			if decErr != nil {
				ex.transportCut = true
				ex.transportErr = decErr.Error()
			} else {
				ex.exitCode = code
			}
			ex.exited = true
			ex.cond.Broadcast()
			ex.mu.Unlock()
			return
		}
	}
}

// attach streams buffered and live output from fromOffset to st while
// forwarding stdin, resize, and signal frames from st to the guest. If st
// breaks or disconnects before the command exits, attach returns without
// closing guestConn or ending the guest command.
func (ex *managedExec) attach(ctx context.Context, st *guestforward.Stream, fromOffset int64) {
	detachCh := make(chan struct{})
	var detachOnce sync.Once
	markDetached := func() {
		detachOnce.Do(func() {
			close(detachCh)
			ex.mu.Lock()
			ex.cond.Broadcast()
			ex.mu.Unlock()
		})
	}
	defer markDetached()

	// Pump client input frames -> guestConn. A client stream read error
	// (such as a dropped connection or client detach reset) detaches cleanly
	// without closing the guest's stdin or terminating the guest command.
	//
	// A clean EOF on st (half-close of the client's write side) or a write
	// error on guestConn (because the guest command already exited or its
	// transport cut before stdin frames arrived) does NOT mean the client
	// detached: the client is still waiting on st for buffered output and
	// the terminal TypeExit frame (or guest-transport-cut reset).
	go func() {
		guestWritable := true
		for {
			typ, payload, err := execwire.ReadFrame(st)
			if err != nil {
				if !errors.Is(err, io.EOF) {
					markDetached()
				}
				return
			}
			if !guestWritable {
				continue
			}
			switch typ {
			case execwire.TypeStdin, execwire.TypeStdinClose, execwire.TypeWinsize, execwire.TypeSignal:
				if err := ex.writeFrame(typ, payload); err != nil {
					guestWritable = false
				}
			}
		}
	}()

	go func() {
		select {
		case <-ctx.Done():
			st.Reset("server context canceled")
			markDetached()
		case <-detachCh:
		}
	}()

	nextSeq := 0
	curOffset := fromOffset
	for {
		ex.mu.Lock()
		for nextSeq >= ex.baseIdx+len(ex.chunks) && !ex.exited {
			select {
			case <-detachCh:
				ex.mu.Unlock()
				return
			default:
			}
			ex.cond.Wait()
		}
		if nextSeq < ex.baseIdx {
			nextSeq = ex.baseIdx
		}
		startIdx := nextSeq - ex.baseIdx
		var pending []outputChunk
		if startIdx < len(ex.chunks) {
			pending = make([]outputChunk, len(ex.chunks)-startIdx)
			copy(pending, ex.chunks[startIdx:])
			nextSeq = ex.baseIdx + len(ex.chunks)
		}
		exited := ex.exited
		exitCode := ex.exitCode
		transportCut := ex.transportCut
		transportErr := ex.transportErr
		ex.mu.Unlock()

		for _, c := range pending {
			if c.typ == execwire.TypeNote {
				if c.startOffset >= fromOffset {
					if err := execwire.WriteFrame(st, c.typ, c.data); err != nil {
						return
					}
				}
				continue
			}
			if c.endOffset <= curOffset {
				continue
			}
			data := c.data
			if c.startOffset < curOffset {
				data = data[curOffset-c.startOffset:]
			}
			if err := execwire.WriteFrame(st, c.typ, data); err != nil {
				return
			}
			curOffset = c.endOffset
		}

		if exited {
			if transportCut {
				st.Reset("guest transport cut: " + transportErr)
				return
			}
			_ = execwire.WriteFrame(st, execwire.TypeExit, execwire.EncodeExit(exitCode))
			_ = st.CloseWrite()
			return
		}
	}
}

func (s *Server) nextExecID() string {
	seq := s.execSeq.Add(1)
	var b [4]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("exec-%d-%s", seq, hex.EncodeToString(b[:]))
}

func (s *Server) handleExec(ctx context.Context, st *guestforward.Stream, req *ExecRequest) {
	if req == nil {
		_ = st.Reject("missing exec request")
		return
	}
	id := strings.TrimSpace(req.ID)
	if id == "" {
		id = s.nextExecID()
	}

	s.mu.Lock()
	if _, exists := s.execs[id]; exists {
		s.mu.Unlock()
		_ = st.Reject(fmt.Sprintf("exec session %q already exists", id))
		return
	}
	s.mu.Unlock()

	user := req.User
	if user == "" {
		user = s.cfg.Guest.User
	}
	wireReq := execwire.Request{
		User:      user,
		Dir:       req.Workdir,
		Env:       req.Env,
		Container: req.Container,
		TTY:       req.TTY,
		Cols:      req.Cols,
		Rows:      req.Rows,
		Term:      req.Term,
	}
	if req.Container && len(req.Argv) > 0 {
		wireReq.Argv = req.Argv
	} else if req.Line != "" {
		wireReq.Line = req.Line
	} else {
		wireReq.Line = shellJoin(req.Argv)
	}

	guestConn, err := s.dialGuest(ctx)
	if err != nil {
		_ = st.Reject("connecting to guest agent: " + err.Error())
		return
	}
	if err := execwire.WriteRequest(guestConn, wireReq); err != nil {
		guestConn.Close()
		_ = st.Reject("sending request to guest agent: " + err.Error())
		return
	}
	br := bufio.NewReader(guestConn)
	resp, err := execwire.ReadResponse(br)
	if err != nil {
		guestConn.Close()
		_ = st.Reject("reading guest agent response: " + err.Error())
		return
	}
	if !resp.OK {
		guestConn.Close()
		_ = st.Reject(resp.Error)
		return
	}
	if missing := resp.MissingFeatures(wireReq); len(missing) > 0 {
		guestConn.Close()
		_ = st.Reject("guest agent missing required features: " + strings.Join(missing, ", "))
		return
	}

	s.mu.Lock()
	if _, exists := s.execs[id]; exists {
		s.mu.Unlock()
		guestConn.Close()
		_ = st.Reject(fmt.Sprintf("exec session %q already exists", id))
		return
	}
	var ex *managedExec
	ex = newManagedExec(id, guestConn, br, resp.Features, s.cfg.MaxExecBufferBytes, func() {
		if s.cfg.CompletedExecRetention <= 0 {
			return
		}
		time.AfterFunc(s.cfg.CompletedExecRetention, func() {
			s.mu.Lock()
			if s.execs[id] == ex {
				delete(s.execs, id)
			}
			s.mu.Unlock()
		})
	})
	s.execs[id] = ex
	s.mu.Unlock()

	replyBytes, _ := json.Marshal(ExecReply{ID: id})
	if err := st.Confirm(replyBytes); err != nil {
		return
	}
	ex.attach(ctx, st, 0)
}

func (s *Server) handleReattach(ctx context.Context, st *guestforward.Stream, req *ReattachRequest) {
	if req == nil || req.ID == "" {
		_ = st.Reject("missing reattach id")
		return
	}
	if req.Offset < 0 {
		_ = st.Reject("negative reattach offset")
		return
	}

	s.mu.Lock()
	ex := s.execs[req.ID]
	s.mu.Unlock()
	if ex == nil {
		_ = st.Reject(ErrExecNotFound.Error())
		return
	}

	ex.mu.Lock()
	reply := ReattachReply{
		ID:       ex.id,
		Exited:   ex.exited,
		ExitCode: ex.exitCode,
	}
	ex.mu.Unlock()

	replyBytes, _ := json.Marshal(reply)
	if err := st.Confirm(replyBytes); err != nil {
		return
	}
	ex.attach(ctx, st, req.Offset)
}

func (s *Server) handleSignal(st *guestforward.Stream, req *SignalRequest) {
	if req == nil || req.ID == "" {
		_ = st.Reject("missing signal exec id")
		return
	}
	if req.Signal <= 0 || req.Signal > execwire.MaxSignal {
		_ = st.Reject(fmt.Sprintf("invalid signal %d", req.Signal))
		return
	}

	s.mu.Lock()
	ex := s.execs[req.ID]
	s.mu.Unlock()
	if ex == nil {
		_ = st.Reject(ErrExecNotFound.Error())
		return
	}

	ex.mu.Lock()
	exited := ex.exited
	ex.mu.Unlock()
	if !exited {
		if err := ex.writeFrame(execwire.TypeSignal, execwire.EncodeSignal(req.Signal)); err != nil {
			_ = st.Reject("sending signal to guest agent: " + err.Error())
			return
		}
	}
	_ = st.Confirm(nil)
	_ = st.CloseWrite()
}

func (s *Server) handlePutFiles(ctx context.Context, st *guestforward.Stream) {
	if err := st.Confirm(nil); err != nil {
		return
	}
	defer st.CloseWrite()

	br := bufio.NewReader(st)
	var req PutFilesRequest
	if err := readJSONLine(br, &req); err != nil {
		_ = writeJSONLine(st, PutFilesReply{OK: false, Error: "reading put-files header: " + err.Error()})
		return
	}
	if len(req.Files) == 0 {
		_ = writeJSONLine(st, PutFilesReply{OK: true, Written: 0})
		return
	}

	var nonceBytes [6]byte
	_, _ = rand.Read(nonceBytes[:])
	nonce := hex.EncodeToString(nonceBytes[:])

	type stagedFile struct {
		dstPath string
		tmpPath string
		relTmp  string
		size    int64
		mode    uint32
		owner   string
	}

	staged := make([]stagedFile, len(req.Files))
	dirsSeen := make(map[string]bool)
	var dirs []string
	var tmpQuoted []string

	for i, f := range req.Files {
		if f.Path == "" || !strings.HasPrefix(f.Path, "/") {
			_ = writeJSONLine(st, PutFilesReply{OK: false, Error: fmt.Sprintf("file %q: path must be absolute", f.Path)})
			return
		}
		cleanDst := path.Clean(f.Path)
		if cleanDst == "/" || strings.HasSuffix(f.Path, "/") {
			_ = writeJSONLine(st, PutFilesReply{OK: false, Error: fmt.Sprintf("file %q: path names a directory", f.Path)})
			return
		}
		if f.Size < 0 {
			_ = writeJSONLine(st, PutFilesReply{OK: false, Error: fmt.Sprintf("file %q: negative size %d", f.Path, f.Size)})
			return
		}
		owner := strings.TrimSpace(f.Owner)
		if strings.HasPrefix(owner, "-") {
			_ = writeJSONLine(st, PutFilesReply{OK: false, Error: fmt.Sprintf("file %q: invalid owner %q", f.Path, f.Owner)})
			return
		}
		dir := path.Dir(cleanDst)
		if !dirsSeen[dir] {
			dirsSeen[dir] = true
			dirs = append(dirs, shellQuote(dir))
		}
		base := path.Base(cleanDst)
		tmpPath := path.Join(dir, fmt.Sprintf(".granule-put-%s-%d-%s", nonce, i, base))
		relTmp := strings.TrimPrefix(tmpPath, "/")
		mode := f.Mode & 0o7777
		if mode == 0 {
			mode = 0o644
		}
		staged[i] = stagedFile{
			dstPath: cleanDst,
			tmpPath: tmpPath,
			relTmp:  relTmp,
			size:    f.Size,
			mode:    mode,
			owner:   owner,
		}
		tmpQuoted = append(tmpQuoted, shellQuote(tmpPath))
	}

	// Build a single POSIX sh pipeline inside the guest that:
	//  1. Creates all parent directories;
	//  2. Extracts every file from the tar stream on stdin into its sibling
	//     temporary file (.granule-put-<nonce>-<i>-<base>);
	//  3. Applies explicit chmod and optional chown to each temporary file;
	//  4. Refuses to overwrite an existing directory and atomically renames
	//     each temporary file into place with mv -f.
	var script strings.Builder
	script.WriteString("set -e; ")
	script.WriteString("trap " + shellQuote("rm -f "+strings.Join(tmpQuoted, " ")) + " EXIT; ")
	script.WriteString("mkdir -p " + strings.Join(dirs, " ") + "; ")
	script.WriteString("tar -xf - -C /")
	for _, sf := range staged {
		qDst := shellQuote(sf.dstPath)
		qTmp := shellQuote(sf.tmpPath)
		qErr := shellQuote(fmt.Sprintf("sandbox put-files: %s is a directory", sf.dstPath))
		script.WriteString(fmt.Sprintf("; if [ -d %s ]; then printf '%%s\\n' %s >&2; exit 2; fi", qDst, qErr))
		script.WriteString(fmt.Sprintf("; chmod %04o %s", sf.mode, qTmp))
		if sf.owner != "" {
			script.WriteString(fmt.Sprintf("; chown %s %s", shellQuote(sf.owner), qTmp))
		}
		script.WriteString(fmt.Sprintf("; mv -f %s %s", qTmp, qDst))
	}

	guestConn, err := s.dialGuest(ctx)
	if err != nil {
		_ = writeJSONLine(st, PutFilesReply{OK: false, Error: "connecting to guest agent: " + err.Error()})
		return
	}
	defer guestConn.Close()

	wireReq := execwire.Request{
		User: s.cfg.Guest.User,
		Line: script.String(),
	}
	if err := execwire.WriteRequest(guestConn, wireReq); err != nil {
		_ = writeJSONLine(st, PutFilesReply{OK: false, Error: "sending guest request: " + err.Error()})
		return
	}
	gbr := bufio.NewReader(guestConn)
	resp, err := execwire.ReadResponse(gbr)
	if err != nil {
		_ = writeJSONLine(st, PutFilesReply{OK: false, Error: "reading guest response: " + err.Error()})
		return
	}
	if !resp.OK {
		_ = writeJSONLine(st, PutFilesReply{OK: false, Error: resp.Error})
		return
	}

	// Stream the tar archive into the guest session's TypeStdin frames.
	streamErrCh := make(chan error, 1)
	go func() {
		fw := &execwireStdinWriter{w: guestConn}
		tw := tar.NewWriter(fw)
		for _, sf := range staged {
			hdr := &tar.Header{
				Typeflag: tar.TypeReg,
				Name:     sf.relTmp,
				Size:     sf.size,
				Mode:     int64(sf.mode),
				ModTime:  time.Now(),
			}
			if err := tw.WriteHeader(hdr); err != nil {
				streamErrCh <- err
				return
			}
			if sf.size > 0 {
				if _, err := io.CopyN(tw, br, sf.size); err != nil {
					streamErrCh <- err
					return
				}
			}
		}
		if err := tw.Close(); err != nil {
			streamErrCh <- err
			return
		}
		streamErrCh <- execwire.WriteFrame(guestConn, execwire.TypeStdinClose, nil)
	}()

	var stderrBuf bytes.Buffer
	exitCode, waitErr := readGuestExec(gbr, io.Discard, &stderrBuf)
	streamErr := <-streamErrCh
	if streamErr != nil {
		_ = writeJSONLine(st, PutFilesReply{OK: false, Error: "streaming files to guest: " + streamErr.Error()})
		return
	}
	if waitErr != nil {
		_ = writeJSONLine(st, PutFilesReply{OK: false, Error: "waiting for guest file write: " + waitErr.Error()})
		return
	}
	if exitCode != 0 {
		msg := strings.TrimSpace(stderrBuf.String())
		if msg == "" {
			msg = fmt.Sprintf("guest shell exited with status %d", exitCode)
		}
		_ = writeJSONLine(st, PutFilesReply{OK: false, Error: msg})
		return
	}

	_ = writeJSONLine(st, PutFilesReply{OK: true, Written: len(staged)})
}

func (s *Server) handleGetFile(ctx context.Context, st *guestforward.Stream, req *GetFileRequest) {
	if req == nil || req.Path == "" || !strings.HasPrefix(req.Path, "/") {
		_ = st.Reject("get-file path must be an absolute path")
		return
	}
	if err := st.Confirm(nil); err != nil {
		return
	}
	defer st.CloseWrite()

	cleanPath := path.Clean(req.Path)
	qPath := shellQuote(cleanPath)
	line := fmt.Sprintf(
		"if [ -d %s ]; then echo 'is a directory' >&2; exit 2; fi; "+
			"if [ ! -e %s ]; then echo 'no such file' >&2; exit 3; fi; "+
			"(stat -c '%%a' %s 2>/dev/null || stat -f '%%Lp' %s 2>/dev/null || echo 644) && exec cat %s",
		qPath, qPath, qPath, qPath, qPath,
	)

	guestConn, err := s.dialGuest(ctx)
	if err != nil {
		_ = writeJSONLine(st, GetFileReply{OK: false, Error: "connecting to guest agent: " + err.Error()})
		return
	}
	defer guestConn.Close()

	wireReq := execwire.Request{
		User: s.cfg.Guest.User,
		Line: line,
	}
	if err := execwire.WriteRequest(guestConn, wireReq); err != nil {
		_ = writeJSONLine(st, GetFileReply{OK: false, Error: err.Error()})
		return
	}
	gbr := bufio.NewReader(guestConn)
	resp, err := execwire.ReadResponse(gbr)
	if err != nil {
		_ = writeJSONLine(st, GetFileReply{OK: false, Error: err.Error()})
		return
	}
	if !resp.OK {
		_ = writeJSONLine(st, GetFileReply{OK: false, Error: resp.Error})
		return
	}
	_ = execwire.WriteFrame(guestConn, execwire.TypeStdinClose, nil)

	var stdoutBuf, stderrBuf bytes.Buffer
	exitCode, waitErr := readGuestExec(gbr, &stdoutBuf, &stderrBuf)
	if waitErr != nil {
		_ = writeJSONLine(st, GetFileReply{OK: false, Error: waitErr.Error()})
		return
	}
	if exitCode != 0 {
		msg := strings.TrimSpace(stderrBuf.String())
		if msg == "" {
			msg = fmt.Sprintf("guest shell exited with status %d", exitCode)
		}
		_ = writeJSONLine(st, GetFileReply{OK: false, Error: msg})
		return
	}

	raw := stdoutBuf.Bytes()
	nl := bytes.IndexByte(raw, '\n')
	if nl < 0 {
		_ = writeJSONLine(st, GetFileReply{OK: false, Error: "unexpected output from guest stat"})
		return
	}
	modeStr := strings.TrimSpace(string(raw[:nl]))
	content := raw[nl+1:]
	modeBits, err := strconv.ParseUint(modeStr, 8, 32)
	if err != nil {
		modeBits = 0o644
	}

	if err := writeJSONLine(st, GetFileReply{
		OK:   true,
		Mode: uint32(modeBits),
		Size: int64(len(content)),
	}); err != nil {
		return
	}
	_, _ = st.Write(content)
}

func (s *Server) handleHealth(ctx context.Context, st *guestforward.Stream) {
	info := HealthInfo{
		SandboxID:  s.cfg.SandboxID,
		WrapperVer: s.cfg.WrapperVersion,
	}

	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	guestConn, err := s.dialGuest(probeCtx)
	if err != nil {
		info.Ready = false
		info.Error = err.Error()
		b, _ := json.Marshal(info)
		_ = st.Confirm(b)
		_ = st.CloseWrite()
		return
	}
	defer guestConn.Close()

	line := guestexec.ProbeCommand
	if s.cfg.Guest.ReadyFile != "" {
		line = "test -e " + shellQuote(s.cfg.Guest.ReadyFile)
	}
	wireReq := execwire.Request{
		User: s.cfg.Guest.User,
		Line: line,
	}
	if err := execwire.WriteRequest(guestConn, wireReq); err != nil {
		info.Error = err.Error()
		b, _ := json.Marshal(info)
		_ = st.Confirm(b)
		_ = st.CloseWrite()
		return
	}
	gbr := bufio.NewReader(guestConn)
	resp, err := execwire.ReadResponse(gbr)
	if err != nil {
		info.Error = err.Error()
		b, _ := json.Marshal(info)
		_ = st.Confirm(b)
		_ = st.CloseWrite()
		return
	}
	info.AgentFeatures = resp.Features
	if !resp.OK {
		info.Error = resp.Error
		b, _ := json.Marshal(info)
		_ = st.Confirm(b)
		_ = st.CloseWrite()
		return
	}
	_ = execwire.WriteFrame(guestConn, execwire.TypeStdinClose, nil)
	code, err := readGuestExec(gbr, io.Discard, io.Discard)
	if err != nil {
		info.Error = err.Error()
	} else if code != 0 {
		if s.cfg.Guest.ReadyFile != "" {
			info.Error = fmt.Sprintf("%v: %s", guestexec.ErrNotDeclared, s.cfg.Guest.ReadyFile)
		} else {
			info.Error = fmt.Sprintf("probe exited with status %d", code)
		}
	} else {
		info.Ready = true
	}

	b, _ := json.Marshal(info)
	_ = st.Confirm(b)
	_ = st.CloseWrite()
}

func (s *Server) handleForward(ctx context.Context, st *guestforward.Stream, req *ForwardRequest) {
	guestConn, err := s.dialGuest(ctx)
	if err != nil {
		_ = st.Reject("connecting to guest agent: " + err.Error())
		return
	}
	defer guestConn.Close()

	if err := execwire.WriteRequest(guestConn, execwire.Request{Forward: true}); err != nil {
		_ = st.Reject("sending forward request to guest agent: " + err.Error())
		return
	}
	gbr := bufio.NewReader(guestConn)
	resp, err := execwire.ReadResponse(gbr)
	if err != nil {
		_ = st.Reject("reading guest agent forward response: " + err.Error())
		return
	}
	if !resp.OK {
		_ = st.Reject(resp.Error)
		return
	}
	if err := st.Confirm(nil); err != nil {
		return
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(guestConn, st)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(st, gbr)
		_ = st.CloseWrite()
	}()
	wg.Wait()
}

type execwireStdinWriter struct {
	w io.Writer
}

func (w *execwireStdinWriter) Write(p []byte) (int, error) {
	total := 0
	for len(p) > 0 {
		n := len(p)
		if n > 32<<10 {
			n = 32 << 10
		}
		if err := execwire.WriteFrame(w.w, execwire.TypeStdin, p[:n]); err != nil {
			return total, err
		}
		total += n
		p = p[n:]
	}
	return total, nil
}

func readGuestExec(br *bufio.Reader, stdout, stderr io.Writer) (int, error) {
	for {
		typ, payload, err := execwire.ReadFrame(br)
		if err != nil {
			return 0, fmt.Errorf("%w: %v", ErrTransportCut, err)
		}
		switch typ {
		case execwire.TypeStdout:
			if stdout != nil {
				_, _ = stdout.Write(payload)
			}
		case execwire.TypeStderr:
			if stderr != nil {
				_, _ = stderr.Write(payload)
			}
		case execwire.TypeExit:
			return execwire.DecodeExit(payload)
		}
	}
}

type bufferedConn struct {
	*bufio.Reader
	net.Conn
}

func (c *bufferedConn) Read(p []byte) (int, error) {
	return c.Reader.Read(p)
}
