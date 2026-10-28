package modelexec

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/tunnel/mux"
)

// DefaultWait is how long a dispatch waits for a model machine to connect
// before giving up.
const DefaultWait = 30 * time.Second

// Config configures a Server.
type Config struct {
	Token string
	Store *model.Store
	Wait  time.Duration
	Logf  func(format string, args ...any)
	TLS   *tls.Config

	// ServeMCP is invoked when an MCP tunnel stream arrives over the multiplexer.
	ServeMCP func(rwc io.ReadWriteCloser, args []string) error

	// ServeGitProxy is invoked when a Git proxy stream arrives over the multiplexer.
	// If ServeGitProxy is nil and GitProxy is provided, GitProxy (http.Handler) is served.
	ServeGitProxy func(rwc io.ReadWriteCloser) error

	// GitProxy is the HTTP handler for Git proxy traffic tunneled over the multiplexer.
	GitProxy http.Handler

	PingInterval time.Duration
	IdleTimeout  time.Duration
}

// Status is one connected machine's summary for UI reporting.
type Status struct {
	ID          string            `json:"id,omitempty"`
	Host        string            `json:"host"`
	TokenID     string            `json:"token_id,omitempty"`
	Addr        string            `json:"addr"`
	Binaries    map[string]string `json:"binaries"`
	ServerURL   string            `json:"server_url,omitempty"`
	GitProxyURL string            `json:"git_proxy_url,omitempty"`
	Since       time.Time         `json:"since"`
	Running     int32             `json:"running"`
	Concurrency int               `json:"concurrency"`
}

// Server is the daemon's model-execution listener: it accepts connections from
// `grain executor` / `grain modelexec` daemons, maintains the registry of connected
// machines by offered binary, and bridges MCP tool calls back into Grain.
type Server struct {
	cfg Config

	mu      sync.Mutex
	hosts   map[string]*hostConn
	waiters map[string][]chan struct{}
}

// NewServer builds a Server.
func NewServer(cfg Config) (*Server, error) {
	if cfg.Token == "" && cfg.Store == nil {
		return nil, errors.New("modelexec: Server requires a non-empty Token or Store")
	}
	if cfg.Wait <= 0 {
		cfg.Wait = DefaultWait
	}
	return &Server{
		cfg:     cfg,
		hosts:   make(map[string]*hostConn),
		waiters: make(map[string][]chan struct{}),
	}, nil
}

// Serve accepts connections on ln until ctx is done or ln closes.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	defer ln.Close()
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if s.cfg.TLS != nil {
			conn = tls.Server(conn, s.cfg.TLS)
		}
		go s.handleConn(ctx, conn)
	}
}

func (s *Server) logf(format string, args ...any) {
	if s.cfg.Logf != nil {
		s.cfg.Logf(format, args...)
	}
}

// HandleConn serves a single connected remote executor on conn until ctx is canceled
// or the remote host disconnects.
func (s *Server) HandleConn(ctx context.Context, conn net.Conn) {
	s.handleConn(ctx, conn)
}

func (s *Server) pingInterval() time.Duration {
	if s.cfg.PingInterval > 0 {
		return s.cfg.PingInterval
	}
	return pingInterval
}

func (s *Server) idleTimeout() time.Duration {
	if s.cfg.IdleTimeout > 0 {
		return s.cfg.IdleTimeout
	}
	return idleTimeout
}
func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	connCtx, cancelConn := context.WithCancel(ctx)
	defer cancelConn()
	go func() {
		<-connCtx.Done()
		_ = conn.Close()
	}()

	addr := conn.RemoteAddr().String()

	// Initialize stream multiplexer over the transport connection.
	m := mux.NewMux(conn, mux.Config{
		IsServer:     true,
		PingInterval: s.pingInterval(),
		PingTimeout:  15 * time.Second,
	})
	defer m.Close()

	// Accept the control stream (the first stream opened by the client).
	ctrlStream, err := m.AcceptStream()
	if err != nil {
		s.logf("modelexec: accepting control stream from %s: %v", addr, err)
		return
	}

	codec := newCodec(ctrlStream)
	hello, err := codec.read(writeTimeout)
	if err != nil {
		s.logf("modelexec: handshake read from %s: %v", addr, err)
		_ = ctrlStream.Close()
		return
	}
	if hello.Type != frameHello {
		s.logf("modelexec: handshake from %s sent %q, want %q", addr, hello.Type, frameHello)
		_ = codec.write(frame{Type: frameWelcome, Error: fmt.Sprintf("expected hello frame, got %q", hello.Type)})
		time.Sleep(50 * time.Millisecond)
		return
	}

	var tokenID string
	authenticated := false

	if s.cfg.Store != nil {
		tokenHash := model.HashExecutorToken(hello.Token)
		tok, err := s.cfg.Store.GetExecutorTokenByHash(connCtx, tokenHash)
		if err == nil && tok != nil {
			if tok.RevokedAt != nil {
				s.logf("modelexec: authentication failure from %s (token revoked)", addr)
				_ = codec.write(frame{Type: frameWelcome, Error: "authentication failure: token revoked"})
				time.Sleep(50 * time.Millisecond)
				return
			}
			tokenID = tok.ID
			_ = s.cfg.Store.TouchExecutorTokenConnected(connCtx, tok.ID, time.Now().UTC())
			authenticated = true
		}
	}

	if !authenticated && s.cfg.Token != "" {
		if subtle.ConstantTimeCompare([]byte(hello.Token), []byte(s.cfg.Token)) == 1 {
			authenticated = true
		}
	}

	if !authenticated {
		s.logf("modelexec: authentication failure from %s (wrong token)", addr)
		_ = codec.write(frame{Type: frameWelcome, Error: "authentication failure: wrong token"})
		time.Sleep(50 * time.Millisecond)
		return
	}
	if len(hello.Binaries) == 0 {
		s.logf("modelexec: rejection from %s: no binaries offered", addr)
		_ = codec.write(frame{Type: frameWelcome, Error: "no binaries offered"})
		time.Sleep(50 * time.Millisecond)
		return
	}

	hostName := hello.Host
	if hostName == "" {
		hostName = addr
	}
	instanceID := strings.TrimSpace(hello.InstanceID)
	if instanceID == "" {
		var b [8]byte
		if _, err := crand.Read(b[:]); err == nil {
			instanceID = fmt.Sprintf("%s-%x", hostName, b)
		} else {
			instanceID = fmt.Sprintf("%s-%d", hostName, time.Now().UnixNano())
		}
	}
	// Refuse, in the welcome frame, a claim on an instance id another
	// principal holds a live connection under, so the executor hears why
	// and retries (its reconnect loop backs off) rather than being dropped.
	if err := s.checkClaim(instanceID, tokenID); err != nil {
		s.logf("modelexec: rejection from %s: %v", addr, err)
		_ = codec.write(frame{Type: frameWelcome, Error: err.Error()})
		time.Sleep(50 * time.Millisecond)
		return
	}

	if err := codec.write(frame{Type: frameWelcome, ProtocolVersion: ProtocolVersion}); err != nil {
		s.logf("modelexec: welcome write to %s: %v", addr, err)
		return
	}

	hc := newHostConn(s, m, codec, instanceID, hostName, tokenID, addr, hello.Binaries, hello.ServerURL, hello.CodexAuthFile, hello.GitProxyURL, hello.CapabilityMCPURL, hello.Concurrency)
	if err := s.register(hc); err != nil {
		// Another principal took the id between checkClaim and here. The
		// welcome has gone, so all that is left is to hang up; the
		// executor's next attempt is refused by checkClaim with the reason.
		s.logf("modelexec: rejection from %s: %v", addr, err)
		hc.close()
		return
	}
	defer s.unregister(hc)

	s.logf("modelexec: %s (%s, %s) connected, offering %s", hostName, instanceID, addr, hc.offering())

	// Loop accepting incoming streams on the multiplexer (e.g. MCP tunnel connections from remote CLIs).
	for {
		stream, err := m.AcceptStream()
		if err != nil {
			break
		}
		go s.handleStream(ctx, hc, stream)
	}

	hc.close()
}

type bufferedReadWriteCloser struct {
	io.Reader
	io.Writer
	io.Closer
}

type streamConn struct {
	net.Conn
	r         io.Reader
	closeFunc func()
}

func (c *streamConn) Read(p []byte) (int, error) {
	return c.r.Read(p)
}

func (c *streamConn) Close() error {
	if c.closeFunc != nil {
		c.closeFunc()
	}
	return c.Conn.Close()
}

type singleConnListener struct {
	conn   net.Conn
	once   sync.Once
	doneCh chan struct{}
}

func (l *singleConnListener) Accept() (net.Conn, error) {
	var c net.Conn
	l.once.Do(func() {
		c = l.conn
	})
	if c != nil {
		return c, nil
	}
	<-l.doneCh
	return nil, net.ErrClosed
}

func (l *singleConnListener) Close() error {
	select {
	case <-l.doneCh:
	default:
		close(l.doneCh)
	}
	return nil
}

func (l *singleConnListener) Addr() net.Addr {
	return l.conn.LocalAddr()
}

// serveHTTPStream serves HTTP with handler on one tunnel stream, whose
// first bytes may already have been read into r.
func (s *Server) serveHTTPStream(hc *hostConn, stream *mux.Stream, r io.Reader, handler http.Handler, what string) {
	doneCh := make(chan struct{})
	var closeOnce sync.Once
	closeDone := func() {
		closeOnce.Do(func() {
			close(doneCh)
		})
	}
	defer closeDone()

	conn := &streamConn{
		Conn:      stream,
		r:         r,
		closeFunc: closeDone,
	}
	ln := &singleConnListener{
		conn:   conn,
		doneCh: doneCh,
	}
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 30 * time.Second,
	}
	if err := srv.Serve(ln); err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, http.ErrServerClosed) {
		s.logf("modelexec: %s %s HTTP serve: %v", hc.name, what, err)
	}
}

func (s *Server) handleStream(ctx context.Context, hc *hostConn, stream *mux.Stream) {
	defer stream.Close()

	// Read stream header from stream.
	dec := json.NewDecoder(stream)
	var header StreamHeader
	if err := dec.Decode(&header); err != nil {
		s.logf("modelexec: %s stream decoding header: %v", hc.name, err)
		return
	}

	// What follows the header on an HTTP stream, less the newline the
	// header's encoder ended it with.
	httpBody := func() io.Reader {
		var r io.Reader = stream
		if buffered := dec.Buffered(); buffered != nil {
			b, _ := io.ReadAll(buffered)
			b = bytes.TrimLeft(b, "\r\n")
			if len(b) > 0 {
				r = io.MultiReader(bytes.NewReader(b), stream)
			}
		}
		return r
	}

	if header.Type == StreamTypeCapabilityMCP {
		s.serveHTTPStream(hc, stream, httpBody(), &hc.capabilityRoutes, "capability MCP")
		return
	}

	if header.Type == StreamTypeGitProxy {
		r := httpBody()
		if s.cfg.ServeGitProxy != nil {
			rwc := &bufferedReadWriteCloser{
				Reader: r,
				Writer: stream,
				Closer: stream,
			}
			if err := s.cfg.ServeGitProxy(rwc); err != nil && !errors.Is(err, io.EOF) {
				s.logf("modelexec: %s Git proxy tunnel error: %v", hc.name, err)
			}
			return
		}
		if s.cfg.GitProxy != nil {
			s.serveHTTPStream(hc, stream, r, s.cfg.GitProxy, "Git proxy")
			return
		}
		s.logf("modelexec: %s stream Git proxy tunnel received but no GitProxy handler configured", hc.name)
		return
	}

	if s.cfg.ServeMCP == nil {
		s.logf("modelexec: %s stream MCP tunnel received but no ServeMCP handler configured", hc.name)
		return
	}

	// The header's arguments are the executor's to write, and serving them
	// as given would let it choose what `grain mcpserver` does on this
	// machine -- -sandbox-root / is a bash shell on the controller. Only an
	// argv grain itself issued to this executor is served (mcpgrants.go).
	if !hc.mcpGrants.allowed(header.Args) {
		s.logf("modelexec: %s refused an MCP tunnel whose arguments grain did not issue to it", hc.name)
		return
	}

	// Serve MCP over this stream, preserving any buffered bytes read by dec.
	rwc := &bufferedReadWriteCloser{
		Reader: io.MultiReader(dec.Buffered(), stream),
		Writer: stream,
		Closer: stream,
	}
	if err := s.cfg.ServeMCP(rwc, header.Args); err != nil && !errors.Is(err, io.EOF) {
		s.logf("modelexec: %s MCP tunnel error: %v", hc.name, err)
	}
}

// errInstanceIDInUse refuses a hello claiming an instance id that a live
// connection authenticated as somebody else already holds.
var errInstanceIDInUse = errors.New("instance id in use by another executor token")

// sameExecutorPrincipal reports whether a connection authenticated with
// tokenID may take over the slot held by holder. The instance id is the
// executor's own word, and visible to anyone who can list executors, so
// only the token that holds it (or another connection under the shared
// cfg.Token, which has no token id) may replace it: a reconnect while the
// old connection is half-dead is the same principal, and anyone else
// would be evicting another token's executor and every run on it. A token rotated without revoking the old one is a different
// principal; while the old connection is alive it keeps the id, and once
// the ping timeout reaps it the new token's retry gets in.
func sameExecutorPrincipal(holder *hostConn, tokenID string) bool {
	return holder.tokenID == tokenID
}

// checkClaim reports whether a newcomer authenticated with tokenID may
// claim id, without claiming it; register decides for good.
func (s *Server) checkClaim(id, tokenID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, exists := s.hosts[id]; exists && !old.closed.Load() && !sameExecutorPrincipal(old, tokenID) {
		return fmt.Errorf("%w: %q", errInstanceIDInUse, id)
	}
	return nil
}

// register puts hc in its instance id's slot, replacing (and closing) a
// connection the same principal already holds there, and refuses a slot
// a live connection of another principal holds.
func (s *Server) register(hc *hostConn) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, exists := s.hosts[hc.id]; exists {
		if !old.closed.Load() && !sameExecutorPrincipal(old, hc.tokenID) {
			return fmt.Errorf("%w: %q", errInstanceIDInUse, hc.id)
		}
		s.logf("modelexec: %s (%s) reconnected from %s, replacing %s", hc.name, hc.id, hc.addr, old.addr)
		old.close()
	}
	s.hosts[hc.id] = hc
	s.notifyWaitersLocked(hc)
	return nil
}

func (s *Server) unregister(hc *hostConn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, exists := s.hosts[hc.id]; exists && cur == hc {
		delete(s.hosts, hc.id)
		s.logf("modelexec: %s (%s, %s) disconnected", hc.name, hc.id, hc.addr)
	}
}

func (s *Server) notifyWaitersLocked(hc *hostConn) {
	for binary := range hc.binaries {
		key := binary
		for _, ch := range s.waiters[key] {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
		delete(s.waiters, key)
	}
}

// RevokeToken immediately closes and unregisters any active host connections authenticated with tokenID.
func (s *Server) RevokeToken(tokenID string) {
	if tokenID == "" {
		return
	}
	s.mu.Lock()
	var toClose []*hostConn
	for _, h := range s.hosts {
		if h.tokenID == tokenID {
			toClose = append(toClose, h)
		}
	}
	s.mu.Unlock()
	for _, h := range toClose {
		h.close()
		s.unregister(h)
	}
}

// notifyCapacity wakes any waiters when a run completes or capacity is updated.
func (s *Server) notifyCapacity(hc *hostConn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notifyWaitersLocked(hc)
}

// SetLimits updates the configured concurrency limits for connected hosts matching
// backend IDs from stored configuration.
func (s *Server) SetLimits(backends []model.AgentBackend) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range backends {
		if b.Concurrency <= 0 {
			continue
		}
		for _, h := range s.hosts {
			if b.ID == fmt.Sprintf("executor-%s-%s", h.name, b.Framework) ||
				b.ID == "executor-"+h.name ||
				strings.HasPrefix(b.ID, "executor-"+h.name+"-") ||
				b.ID == fmt.Sprintf("executor-%s-%s", h.id, b.Framework) ||
				b.ID == "executor-"+h.id ||
				strings.HasPrefix(b.ID, "executor-"+h.id+"-") {
				h.setConfiguredConcurrency(b.Concurrency)
				s.notifyWaitersLocked(h)
			}
		}
	}
}

// Connected returns a snapshot of connected machines.
func (s *Server) Connected() []Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Status, 0, len(s.hosts))
	for _, h := range s.hosts {
		out = append(out, Status{
			ID:          h.id,
			Host:        h.name,
			TokenID:     h.tokenID,
			Addr:        h.addr,
			Binaries:    h.binariesCopy(),
			ServerURL:   h.serverURL,
			GitProxyURL: h.gitProxyURL,
			Since:       h.since,
			Running:     h.running.Load(),
			Concurrency: h.concurrency(),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Host != out[j].Host {
			return out[i].Host < out[j].Host
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// AgentBackends returns dynamically registered model.AgentBackend records for
// all connected remote executors.
func (s *Server) AgentBackends() []model.AgentBackend {
	s.mu.Lock()
	defer s.mu.Unlock()
	var list []model.AgentBackend
	frameworks := map[string]string{
		"agy":    model.AgentFrameworkAntigravity,
		"claude": model.AgentFrameworkClaude,
		"codex":  model.AgentFrameworkCodex,
	}
	nameCounts := make(map[string]int)
	for _, h := range s.hosts {
		nameCounts[h.name]++
	}
	for _, h := range s.hosts {
		hostKey := h.name
		if nameCounts[h.name] > 1 {
			hostKey = h.id
		}
		for binary := range h.binaries {
			fw, ok := frameworks[binary]
			if !ok {
				fw = binary
			}
			backendID := fmt.Sprintf("executor-%s-%s", hostKey, fw)
			list = append(list, model.AgentBackend{
				ID:          backendID,
				Name:        fmt.Sprintf("%s (%s remote)", h.name, fw),
				Framework:   fw,
				AuthMode:    model.AntigravityAuthAmbient,
				Disabled:    false,
				Concurrency: h.concurrency(),
			})
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return list
}

// ConnectedHost finds an already-connected host offering the named
// binary. Every executor serves the whole deployment: none belongs to a
// team since the single-tenant reset (stage 4c).
func (s *Server) ConnectedHost(binary string) *RemoteHost {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connectedHostLocked(binary)
}

func (s *Server) connectedHostLocked(binary string) *RemoteHost {
	if h := s.availableHostLocked(binary); h != nil {
		return h
	}
	var fallback *hostConn
	for _, h := range s.hosts {
		if !h.offers(binary) {
			continue
		}
		if fallback == nil {
			fallback = h
			continue
		}
		running := h.running.Load()
		fallbackRunning := fallback.running.Load()
		if running < fallbackRunning {
			fallback = h
		} else if running == fallbackRunning {
			if h.since.Before(fallback.since) || (h.since.Equal(fallback.since) && h.id < fallback.id) {
				fallback = h
			}
		}
	}
	if fallback != nil {
		return &RemoteHost{conn: fallback}
	}
	return nil
}

func (s *Server) availableHostLocked(binary string) *RemoteHost {
	var best *hostConn
	for _, h := range s.hosts {
		if !h.offers(binary) {
			continue
		}
		running := h.running.Load()
		concurrency := int32(h.concurrency())
		if running >= concurrency {
			continue
		}
		if best == nil {
			best = h
			continue
		}
		bestRunning := best.running.Load()
		if running < bestRunning {
			best = h
		} else if running == bestRunning {
			bestRemaining := int32(best.concurrency()) - bestRunning
			remaining := concurrency - running
			if remaining > bestRemaining {
				best = h
			} else if remaining == bestRemaining {
				if h.since.Before(best.since) || (h.since.Equal(best.since) && h.id < best.id) {
					best = h
				}
			}
		}
	}
	if best != nil {
		return &RemoteHost{conn: best}
	}
	return nil
}

// Host returns a RemoteHost offering binary, waiting up to Config.Wait.
func (s *Server) Host(ctx context.Context, binary string) (*RemoteHost, error) {
	s.mu.Lock()
	if h := s.availableHostLocked(binary); h != nil {
		s.mu.Unlock()
		return h, nil
	}
	waitCtx, cancel := context.WithTimeout(ctx, s.cfg.Wait)
	defer cancel()

	ch := make(chan struct{}, 1)
	key := binary
	s.waiters[key] = append(s.waiters[key], ch)
	s.mu.Unlock()

	for {
		select {
		case <-waitCtx.Done():
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, nil
		case <-ch:
			s.mu.Lock()
			if h := s.availableHostLocked(binary); h != nil {
				s.mu.Unlock()
				return h, nil
			}
			ch = make(chan struct{}, 1)
			s.waiters[key] = append(s.waiters[key], ch)
			s.mu.Unlock()
		}
	}
}

// hostConn manages one remote executor connection.
type hostConn struct {
	srv                   *Server
	mux                   *mux.Mux
	codec                 *codec
	id                    string
	name                  string
	tokenID               string
	addr                  string
	binaries              map[string]string
	serverURL             string
	codexAuthFile         string
	gitProxyURL           string
	capabilityMCPURL      string
	advertisedConcurrency int
	configuredConcurrency int
	since                 time.Time
	running               atomic.Int32

	reqMu   sync.Mutex
	nextID  uint64
	pending map[uint64]chan frame
	stdout  map[uint64]func([]byte)

	closed atomic.Bool
	doneCh chan struct{}

	// mcpGrants is every mcpserver argv grain has handed this connection
	// to run (RemoteHost.AuthorizeMCPArgs); an MCP tunnel stream arriving
	// on it is served only for one of them. See mcpgrants.go.
	mcpGrants mcpGrants

	// capabilityRoutes is the capability MCP servers carried for runs on
	// this connection; a StreamTypeCapabilityMCP stream is served with
	// them. See capabilitymcp.go.
	capabilityRoutes capabilityRoutes
}

func newHostConn(srv *Server, m *mux.Mux, codec *codec, id, name, tokenID, addr string, binaries map[string]string, serverURL, codexAuthFile, gitProxyURL, capabilityMCPURL string, concurrency int) *hostConn {
	if concurrency <= 0 {
		concurrency = 1
	}
	hc := &hostConn{
		srv:                   srv,
		mux:                   m,
		codec:                 codec,
		id:                    id,
		name:                  name,
		tokenID:               tokenID,
		addr:                  addr,
		binaries:              binaries,
		serverURL:             serverURL,
		codexAuthFile:         codexAuthFile,
		gitProxyURL:           gitProxyURL,
		capabilityMCPURL:      capabilityMCPURL,
		advertisedConcurrency: concurrency,
		since:                 time.Now(),
		pending:               make(map[uint64]chan frame),
		stdout:                make(map[uint64]func([]byte)),
		doneCh:                make(chan struct{}),
	}
	go hc.readLoop()
	return hc
}

func (h *hostConn) concurrency() int {
	h.reqMu.Lock()
	defer h.reqMu.Unlock()
	if h.configuredConcurrency > 0 {
		return h.configuredConcurrency
	}
	if h.advertisedConcurrency > 0 {
		return h.advertisedConcurrency
	}
	return 1
}

func (h *hostConn) setConfiguredConcurrency(c int) {
	h.reqMu.Lock()
	defer h.reqMu.Unlock()
	h.configuredConcurrency = c
}

func (h *hostConn) close() {
	if h.closed.CompareAndSwap(false, true) {
		close(h.doneCh)
		_ = h.codec.close()
		_ = h.mux.Close()
		h.reqMu.Lock()
		for _, ch := range h.pending {
			close(ch)
		}
		h.pending = nil
		h.stdout = nil
		h.reqMu.Unlock()
	}
}

func (h *hostConn) offers(binary string) bool {
	_, ok := h.binaries[binary]
	return ok
}

func (h *hostConn) offering() string {
	names := make([]string, 0, len(h.binaries))
	for name := range h.binaries {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func (h *hostConn) binariesCopy() map[string]string {
	out := make(map[string]string, len(h.binaries))
	for k, v := range h.binaries {
		out[k] = v
	}
	return out
}

func (h *hostConn) readLoop() {
	defer h.close()
	for {
		f, err := h.codec.read(h.srv.idleTimeout())
		if err != nil {
			return
		}
		switch f.Type {
		case framePing:
			_ = h.codec.write(frame{Type: framePong})
		case framePong:
			// keepalive ack
		case frameStdout:
			h.reqMu.Lock()
			cb := h.stdout[f.ID]
			h.reqMu.Unlock()
			if cb != nil && len(f.Data) > 0 {
				cb(f.Data)
			}
		case frameResult:
			h.reqMu.Lock()
			ch, ok := h.pending[f.ID]
			delete(h.pending, f.ID)
			delete(h.stdout, f.ID)
			h.reqMu.Unlock()
			if ok && ch != nil {
				ch <- f
			}
		}
	}
}

func (h *hostConn) request(ctx context.Context, f frame, onStdout func([]byte)) (frame, error) {
	if h.closed.Load() {
		return frame{}, errors.New("modelexec: remote executor disconnected")
	}
	h.reqMu.Lock()
	h.nextID++
	id := h.nextID
	f.ID = id
	resCh := make(chan frame, 1)
	h.pending[id] = resCh
	if onStdout != nil {
		h.stdout[id] = onStdout
	}
	h.reqMu.Unlock()

	defer func() {
		h.reqMu.Lock()
		delete(h.pending, id)
		delete(h.stdout, id)
		h.reqMu.Unlock()
	}()

	if err := h.codec.write(f); err != nil {
		return frame{}, err
	}

	select {
	case <-ctx.Done():
		_ = h.codec.write(frame{Type: frameCancel, ID: id})
		select {
		case res := <-resCh:
			return res, ctx.Err()
		case <-time.After(cancelGrace):
			return frame{}, ctx.Err()
		case <-h.doneCh:
			return frame{}, errors.New("modelexec: remote executor disconnected during cancellation")
		}
	case <-h.doneCh:
		return frame{}, errors.New("modelexec: remote executor disconnected")
	case res, ok := <-resCh:
		if !ok {
			return frame{}, errors.New("modelexec: remote executor disconnected")
		}
		return res, nil
	}
}
