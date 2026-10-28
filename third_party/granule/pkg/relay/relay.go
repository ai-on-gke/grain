// Package relay implements granule relay: the in-cluster hop an
// off-cluster controller uses to reach sandbox pods' access listeners.
//
// The controller dials out to the relay once (TLS 1.3, authenticated with a
// controller bearer token or an mTLS client certificate) and keeps one
// multiplexed pkg/guestforward session open. Each stream it opens names a
// target sandbox (sandbox.RelayStreamRequest); the relay resolves that name
// to a pod IP through a Resolver -- in a cluster, a watch on pods in its
// namespace filtered by a label selector, never exec -- dials
// podIP:<sandbox port>, and splices bytes both ways.
//
// The relay is stateless and holds no sandbox secrets. It does not parse
// what flows over a stream: TLS and the per-sandbox token run end to end
// between the controller's sandbox.Client and the sandbox's own server.
package relay

import (
	"bufio"
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bwsalmon/granule/pkg/guestforward"
	"github.com/bwsalmon/granule/pkg/sandbox"
)

// Resolver maps a target sandbox name to the IP address of its pod. It
// returns an error for any name it will not route to: unknown, not running,
// or outside the namespace and selector the relay is confined to.
type Resolver interface {
	Resolve(ctx context.Context, target string) (string, error)
}

// Config configures a relay Server.
type Config struct {
	// Token is the controller bearer token. When empty, TLSConfig must
	// require and verify client certificates (mTLS); New refuses a
	// configuration that would admit an unauthenticated controller.
	Token string

	// TLSConfig carries the relay's server certificate and, for mTLS,
	// ClientCAs with ClientAuth = tls.RequireAndVerifyClientCert. TLS 1.3
	// is enforced.
	TLSConfig *tls.Config

	// Resolver confines and resolves targets.
	Resolver Resolver

	// SandboxPort is the TCP port sandbox pods' access servers listen on.
	SandboxPort int

	// DialTimeout bounds each dial to a sandbox pod. Default 10s.
	DialTimeout time.Duration

	// Dial overrides how the relay connects to a sandbox pod (tests).
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)

	// MuxConfig tunes each controller session.
	MuxConfig guestforward.Config

	// Logf reports refusals and failures. Defaults to log.Printf.
	Logf func(format string, args ...any)
}

// Server is a running granule relay.
type Server struct {
	cfg    Config
	tlsCfg *tls.Config

	refusals atomic.Uint64

	mu       sync.Mutex
	sessions map[*guestforward.Session]struct{}
}

// New validates cfg and returns a Server.
func New(cfg Config) (*Server, error) {
	if cfg.TLSConfig == nil || (len(cfg.TLSConfig.Certificates) == 0 && cfg.TLSConfig.GetCertificate == nil && cfg.TLSConfig.GetConfigForClient == nil) {
		return nil, errors.New("relay: a server certificate is required")
	}
	if cfg.Token == "" && cfg.TLSConfig.ClientAuth != tls.RequireAndVerifyClientCert {
		return nil, errors.New("relay: refusing to start without controller authentication: set a controller token or require verified client certificates")
	}
	if cfg.Resolver == nil {
		return nil, errors.New("relay: a resolver is required")
	}
	if cfg.SandboxPort <= 0 || cfg.SandboxPort > 65535 {
		return nil, fmt.Errorf("relay: invalid sandbox port %d", cfg.SandboxPort)
	}
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = 10 * time.Second
	}
	if cfg.Dial == nil {
		d := &net.Dialer{}
		cfg.Dial = d.DialContext
	}
	if cfg.Logf == nil {
		cfg.Logf = log.Printf
	}
	tlsCfg := cfg.TLSConfig.Clone()
	tlsCfg.MinVersion = tls.VersionTLS13
	return &Server{
		cfg:      cfg,
		tlsCfg:   tlsCfg,
		sessions: make(map[*guestforward.Session]struct{}),
	}, nil
}

// Refusals counts controller sessions and streams the relay has refused.
func (s *Server) Refusals() uint64 { return s.refusals.Load() }

// Serve accepts controller connections on ln (a plain TCP listener; the
// relay adds TLS) until ctx is cancelled or ln is closed.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	ln = tls.NewListener(ln, s.tlsCfg)
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
		go s.handleConn(ctx, conn)
	}
}

// DropSessions closes every controller session, as a relay restart would.
// Controllers reconnect on their own.
func (s *Server) DropSessions() {
	s.mu.Lock()
	sessions := make([]*guestforward.Session, 0, len(s.sessions))
	for sess := range s.sessions {
		sessions = append(sessions, sess)
	}
	s.mu.Unlock()
	for _, sess := range sessions {
		_ = sess.Close()
	}
}

func (s *Server) refuse(conn net.Conn, reason string) {
	s.refusals.Add(1)
	s.cfg.Logf("relay: refused controller %s: %s", conn.RemoteAddr(), reason)
	_ = writeJSONLine(conn, sandbox.RelayHandshakeResponse{OK: false, Error: reason})
}

func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if tc, ok := conn.(*tls.Conn); ok {
		if err := tc.HandshakeContext(ctx); err != nil {
			s.refusals.Add(1)
			s.cfg.Logf("relay: TLS handshake with %s failed: %v", conn.RemoteAddr(), err)
			return
		}
	}
	br := bufio.NewReader(conn)
	var hs sandbox.RelayHandshakeRequest
	if err := readJSONLine(br, &hs); err != nil {
		s.refusals.Add(1)
		return
	}
	if hs.Version != sandbox.RelayProtocolVersion {
		s.refuse(conn, fmt.Sprintf("unsupported protocol version %q, want %q", hs.Version, sandbox.RelayProtocolVersion))
		return
	}
	if s.cfg.Token != "" {
		if hs.Token == "" || subtle.ConstantTimeCompare([]byte(hs.Token), []byte(s.cfg.Token)) != 1 {
			s.refuse(conn, "invalid controller token")
			return
		}
	}
	if err := writeJSONLine(conn, sandbox.RelayHandshakeResponse{OK: true, Version: sandbox.RelayProtocolVersion}); err != nil {
		return
	}
	_ = conn.SetDeadline(time.Time{})

	sess := guestforward.NewSession(&bufferedConn{Reader: br, Conn: conn}, false, s.cfg.MuxConfig)
	s.mu.Lock()
	s.sessions[sess] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.sessions, sess)
		s.mu.Unlock()
		_ = sess.Close()
	}()
	stop := context.AfterFunc(ctx, func() { _ = sess.Close() })
	defer stop()

	for {
		st, err := sess.Accept(ctx)
		if err != nil {
			return
		}
		go s.handleStream(ctx, st)
	}
}

func (s *Server) handleStream(ctx context.Context, st *guestforward.Stream) {
	var req sandbox.RelayStreamRequest
	if err := json.Unmarshal(st.Meta(), &req); err != nil || req.Target == "" {
		s.refusals.Add(1)
		_ = st.Reject("stream metadata must name a target")
		return
	}
	ip, err := s.cfg.Resolver.Resolve(ctx, req.Target)
	if err != nil {
		s.refusals.Add(1)
		s.cfg.Logf("relay: refused target %q: %v", req.Target, err)
		_ = st.Reject(err.Error())
		return
	}
	dialCtx, cancel := context.WithTimeout(ctx, s.cfg.DialTimeout)
	conn, err := s.cfg.Dial(dialCtx, "tcp", net.JoinHostPort(ip, strconv.Itoa(s.cfg.SandboxPort)))
	cancel()
	if err != nil {
		s.cfg.Logf("relay: dialing target %q at %s: %v", req.Target, ip, err)
		_ = st.Reject(fmt.Sprintf("dialing sandbox %q: %v", req.Target, err))
		return
	}
	if err := st.Confirm(nil); err != nil {
		_ = conn.Close()
		return
	}
	splice(st, conn)
}

// splice copies between a stream and a connection in both directions
// until both are done, passing a half-close through in each direction:
// EOF from c becomes a fin on st, and EOF on st a CloseWrite on c. An
// error in either direction tears down both.
func splice(st *guestforward.Stream, c net.Conn) {
	upDone := make(chan struct{})
	go func() {
		defer close(upDone)
		if _, err := io.Copy(st, c); err != nil {
			st.Reset(err.Error())
			_ = c.Close()
			return
		}
		_ = st.CloseWrite()
	}()

	if _, err := io.Copy(c, st); err != nil {
		_ = c.Close()
		st.Reset(err.Error())
	} else if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	} else {
		_ = c.Close()
	}
	<-upDone
	_ = c.Close()
	_ = st.Close()
}

const maxControlLine = 64 << 10

func writeJSONLine(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

func readJSONLine(r *bufio.Reader, v any) error {
	var line []byte
	for {
		chunk, err := r.ReadSlice('\n')
		if len(line)+len(chunk) > maxControlLine {
			return fmt.Errorf("relay: control line exceeds %d bytes", maxControlLine)
		}
		line = append(line, chunk...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			return err
		}
		break
	}
	return json.Unmarshal(line, v)
}

type bufferedConn struct {
	*bufio.Reader
	net.Conn
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.Reader.Read(p) }
