// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package api

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ai-on-gke/grain/pkg/tunnel/mux"
	"github.com/ai-on-gke/grain/pkg/tunnel/wsupgrade"
)

const (
	// ConnectProtocolVersion is the current X-Grain-Connect-Protocol wire version.
	ConnectProtocolVersion = "1"
	// DefaultConnectTransport is the built-in RFC 6455 WebSocket + mux transport name.
	DefaultConnectTransport = "ws-mux"
	// AuthModeToken identifies grc_... registration-token authentication on grain connect.
	AuthModeToken = "token"

	// HeaderConnectProtocol carries the tunnel protocol version ("1").
	HeaderConnectProtocol = "X-Grain-Connect-Protocol"
	// HeaderEntry carries the target entry name on upgrade requests.
	HeaderEntry = "X-Grain-Entry"
	// HeaderVersion carries the connecting daemon's semantic version.
	HeaderVersion = "X-Grain-Version"
	// HeaderCommit carries the connecting daemon's git commit SHA.
	HeaderCommit = "X-Grain-Commit"
	// HeaderHostKind carries the connecting daemon's HostKind.
	HeaderHostKind = "X-Grain-Host-Kind"
	// HeaderAuth selects the connect auth mode ("token").
	HeaderAuth = "X-Grain-Auth"
	// HeaderRegistryToken carries the grc_... registration credential across
	// proxies that strip or consume Authorization.
	HeaderRegistryToken = "X-Grain-Registry-Token"
	// HeaderTunnelSession is returned on 101 Switching Protocols with the assigned session ID.
	HeaderTunnelSession = "X-Grain-Tunnel-Session"
	// HeaderRegistryAssertion is the ES256-signed identity assertion grain-registry
	// attaches to requests forwarded to hosted instances and connected tunnels.
	HeaderRegistryAssertion = "X-Grain-Registry-Assertion"

	defaultConnectDialTimeout      = 10 * time.Second
	defaultConnectHandshakeTimeout = 30 * time.Second
	defaultTunnelPingInterval      = 15 * time.Second
	defaultTunnelPingTimeout       = 15 * time.Second
	defaultTunnelMaxBufferedBytes  = 16 << 20 // 16 MiB per-stream flow control
)

// ConnectPath returns the WebSocket tunnel upgrade path for entryName.
func ConnectPath(entryName string) string {
	return "/api/entries/" + url.PathEscape(entryName) + "/connect"
}

// ConnectConfig configures an outbound grain connect tunnel dial.
type ConnectConfig struct {
	// BaseURL is the registry root URL ("https://grain.example.com", or
	// "http://127.0.0.1:..." in local tests).
	BaseURL string
	// Entry is the registered entry name (must match the grc_... token's entry).
	Entry string
	// RegistrationToken is the grc_... secret authenticating this entry.
	RegistrationToken string
	// BearerToken is an optional static OIDC/IAP bearer token for proxy traversal.
	BearerToken string
	// IDToken optionally mints a fresh OIDC/IAP bearer token per dial.
	IDToken func(ctx context.Context) (string, error)
	// GrainVersion is the connecting daemon's version string.
	GrainVersion string
	// GrainCommit is the connecting daemon's commit hash.
	GrainCommit string
	// HostKind classifies the connecting host ("workstation", "kubernetes", "other").
	HostKind HostKind
	// Transport selects a registered ConnectDialer; empty defaults to "ws-mux".
	Transport string
	// ExtraHeaders are optional additional headers sent on the upgrade request.
	ExtraHeaders http.Header
	// TLSConfig customizes TLS verification for wss/https dials (TLS 1.2+ is always enforced).
	TLSConfig *tls.Config
	// Proxy resolves an HTTP/HTTPS forward proxy for the dial; nil defaults to
	// http.ProxyFromEnvironment.
	Proxy func(*http.Request) (*url.URL, error)
	// DialContext overrides raw TCP dialing when non-nil.
	DialContext func(ctx context.Context, network, addr string) (net.Conn, error)
	// DialTimeout bounds TCP connection establishment (defaults to 10s).
	DialTimeout time.Duration
	// HandshakeTimeout bounds proxy CONNECT, TLS, and HTTP 101 upgrade (defaults to 30s).
	HandshakeTimeout time.Duration
	// AllowInsecureHTTP permits ws:///http:// to non-loopback hosts (for tests).
	AllowInsecureHTTP bool
}

// ConnectResult holds an upgraded tunnel byte stream and server handshake metadata.
type ConnectResult struct {
	Conn      net.Conn
	SessionID string
	Protocol  string
}

// ConnectDialer dials the grain-registry tunnel endpoint and returns an
// upgraded bidirectional byte-stream net.Conn ready for pkg/tunnel/mux.
type ConnectDialer interface {
	DialConnect(ctx context.Context, cfg ConnectConfig) (ConnectResult, error)
}

type dialerEntry struct {
	id     uint64
	name   string
	dialer ConnectDialer
}

var (
	dialersMu     sync.RWMutex
	dialersNextID atomic.Uint64
	dialerStack   []dialerEntry
)

// RegisterConnectDialer registers dialer under name (for example a corporate
// tunnel transport or test dialer) and returns an idempotent function that
// unregisters it. If multiple dialers are registered for the same name, the
// most recently registered active dialer wins.
func RegisterConnectDialer(name string, dialer ConnectDialer) (unregister func()) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		panic("registry/api: RegisterConnectDialer called with empty transport name")
	}
	if dialer == nil {
		panic("registry/api: RegisterConnectDialer called with nil ConnectDialer")
	}
	id := dialersNextID.Add(1)
	dialersMu.Lock()
	dialerStack = append(dialerStack, dialerEntry{id: id, name: name, dialer: dialer})
	dialersMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			dialersMu.Lock()
			defer dialersMu.Unlock()
			for i, e := range dialerStack {
				if e.id == id {
					dialerStack = append(dialerStack[:i], dialerStack[i+1:]...)
					break
				}
			}
		})
	}
}

// LookupConnectDialer returns the active ConnectDialer for name (falling back
// to DefaultConnectDialer for "ws-mux" or "").
func LookupConnectDialer(name string) (ConnectDialer, bool) {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		key = DefaultConnectTransport
	}
	dialersMu.RLock()
	for i := len(dialerStack) - 1; i >= 0; i-- {
		if dialerStack[i].name == key {
			d := dialerStack[i].dialer
			dialersMu.RUnlock()
			return d, true
		}
	}
	dialersMu.RUnlock()
	if key == DefaultConnectTransport {
		return DefaultConnectDialer{}, true
	}
	return nil, false
}

// DialConnect dials the tunnel endpoint using the ConnectDialer selected by cfg.Transport.
func DialConnect(ctx context.Context, cfg ConnectConfig) (ConnectResult, error) {
	d, ok := LookupConnectDialer(cfg.Transport)
	if !ok {
		return ConnectResult{}, fmt.Errorf("unknown grain connect transport %q", cfg.Transport)
	}
	return d.DialConnect(ctx, cfg)
}

// DefaultConnectDialer implements the built-in "ws-mux" transport over TLS 1.2+
// (with HTTP CONNECT proxy support via HTTPS_PROXY/HTTP_PROXY/NO_PROXY) and
// RFC 6455 binary-framed WebSocket upgrade.
type DefaultConnectDialer struct{}

func normalizeConnectURL(raw string, allowInsecure bool) (*url.URL, bool, error) {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) > 2048 {
		return nil, false, fmt.Errorf("connect URL exceeds 2048 bytes")
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return nil, false, fmt.Errorf("invalid registry URL %q: %w", raw, err)
	}
	if u.User != nil {
		return nil, false, fmt.Errorf("connect URL must not include userinfo credentials")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, false, fmt.Errorf("connect URL must not include query parameters or fragments")
	}
	scheme := strings.ToLower(u.Scheme)
	useTLS := false
	switch scheme {
	case "https", "wss":
		useTLS = true
		u.Scheme = "https"
	case "http", "ws":
		if !allowInsecure && !isLoopbackHost(u.Host) {
			return nil, false, fmt.Errorf("refusing insecure %s:// connect URL %q for non-loopback host", scheme, u.Redacted())
		}
		u.Scheme = "http"
	default:
		return nil, false, fmt.Errorf("connect URL %q must use https:// or wss:// (or http:// on loopback)", raw)
	}
	host := u.Hostname()
	if host == "" {
		return nil, false, fmt.Errorf("connect URL %q is missing host", raw)
	}
	if isForbiddenMetadataHost(host) {
		return nil, false, fmt.Errorf("connect URL host %q is not permitted", host)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u, useTLS, nil
}

func hostPortWithDefault(u *url.URL, defaultPort string) (hostName, hostPort string) {
	h := u.Hostname()
	p := u.Port()
	if p == "" {
		p = defaultPort
	}
	return h, net.JoinHostPort(h, p)
}

func wrapTLSClient(ctx context.Context, conn net.Conn, serverName string, base *tls.Config) (*tls.Conn, error) {
	var cfg *tls.Config
	if base != nil {
		cfg = base.Clone()
	} else {
		cfg = &tls.Config{}
	}
	if cfg.MinVersion < tls.VersionTLS12 {
		cfg.MinVersion = tls.VersionTLS12
	}
	if cfg.ServerName == "" {
		cfg.ServerName = serverName
	}
	tlsConn := tls.Client(conn, cfg)
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("TLS handshake with %s: %w", serverName, err)
	}
	return tlsConn, nil
}

// DialConnect performs the outbound TCP / HTTP-CONNECT-proxy / TLS / RFC 6455
// WebSocket upgrade handshake against GET /api/entries/<name>/connect.
func (DefaultConnectDialer) DialConnect(ctx context.Context, cfg ConnectConfig) (ConnectResult, error) {
	entryName := strings.TrimSpace(cfg.Entry)
	if err := ValidateEntryName(entryName); err != nil {
		return ConnectResult{}, err
	}
	regToken := strings.TrimSpace(cfg.RegistrationToken)
	if regToken == "" && strings.TrimSpace(cfg.BearerToken) == "" && cfg.IDToken == nil {
		return ConnectResult{}, errors.New("registration token is required for grain connect")
	}

	baseURL, useTLS, err := normalizeConnectURL(cfg.BaseURL, cfg.AllowInsecureHTTP)
	if err != nil {
		return ConnectResult{}, err
	}

	dialTimeout := cfg.DialTimeout
	if dialTimeout <= 0 {
		dialTimeout = defaultConnectDialTimeout
	}
	handshakeTimeout := cfg.HandshakeTimeout
	if handshakeTimeout <= 0 {
		handshakeTimeout = defaultConnectHandshakeTimeout
	}

	defaultPort := "80"
	if useTLS {
		defaultPort = "443"
	}
	targetServerName, targetAddr := hostPortWithDefault(baseURL, defaultPort)

	targetURL := *baseURL
	targetURL.Path = baseURL.Path + ConnectPath(entryName)

	proxyFn := cfg.Proxy
	if proxyFn == nil {
		proxyFn = http.ProxyFromEnvironment
	}
	proxyLookupReq := &http.Request{Method: http.MethodGet, URL: &targetURL, Host: targetURL.Host}
	proxyURL, err := proxyFn(proxyLookupReq)
	if err != nil {
		return ConnectResult{}, fmt.Errorf("resolving HTTP proxy: %w", err)
	}

	dialFn := cfg.DialContext
	if dialFn == nil {
		d := &net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}
		dialFn = d.DialContext
	}

	dialAddr := targetAddr
	if proxyURL != nil {
		proxyDefaultPort := "80"
		if strings.EqualFold(proxyURL.Scheme, "https") {
			proxyDefaultPort = "443"
		}
		_, dialAddr = hostPortWithDefault(proxyURL, proxyDefaultPort)
	}

	dialCtx, dialCancel := context.WithTimeout(ctx, dialTimeout)
	rawConn, err := dialFn(dialCtx, "tcp", dialAddr)
	dialCancel()
	if err != nil {
		return ConnectResult{}, fmt.Errorf("dialing %s: %w", dialAddr, err)
	}

	hsCtx, hsCancel := context.WithTimeout(ctx, handshakeTimeout)
	defer hsCancel()
	deadline, _ := hsCtx.Deadline()
	_ = rawConn.SetDeadline(deadline)

	conn := rawConn
	if proxyURL != nil {
		if strings.EqualFold(proxyURL.Scheme, "https") {
			proxyHost, _ := hostPortWithDefault(proxyURL, "443")
			tlsProxyConn, err := wrapTLSClient(hsCtx, conn, proxyHost, nil)
			if err != nil {
				return ConnectResult{}, fmt.Errorf("proxy TLS handshake: %w", err)
			}
			conn = tlsProxyConn
		}
		conn, err = tunnelViaHTTPConnectProxy(conn, proxyURL, targetAddr)
		if err != nil {
			_ = conn.Close()
			return ConnectResult{}, err
		}
	}

	if useTLS {
		tlsConn, err := wrapTLSClient(hsCtx, conn, targetServerName, cfg.TLSConfig)
		if err != nil {
			return ConnectResult{}, err
		}
		conn = tlsConn
	}

	req, err := http.NewRequestWithContext(hsCtx, http.MethodGet, targetURL.String(), nil)
	if err != nil {
		_ = conn.Close()
		return ConnectResult{}, err
	}
	for k, vals := range cfg.ExtraHeaders {
		for _, val := range vals {
			req.Header.Add(k, val)
		}
	}
	wsupgrade.SetRequestHeaders(req.Header)
	secKey := req.Header.Get("Sec-WebSocket-Key")

	req.Header.Set(HeaderConnectProtocol, ConnectProtocolVersion)
	req.Header.Set(HeaderEntry, entryName)
	if v := strings.TrimSpace(cfg.GrainVersion); v != "" {
		req.Header.Set(HeaderVersion, v)
	}
	if c := strings.TrimSpace(cfg.GrainCommit); c != "" {
		req.Header.Set(HeaderCommit, c)
	}
	if cfg.HostKind != "" {
		req.Header.Set(HeaderHostKind, string(cfg.HostKind))
	}
	if err := applyAuthHeaders(hsCtx, req.Header, regToken, strings.TrimSpace(cfg.BearerToken), cfg.IDToken); err != nil {
		_ = conn.Close()
		return ConnectResult{}, err
	}

	if err := req.Write(conn); err != nil {
		_ = conn.Close()
		return ConnectResult{}, fmt.Errorf("writing connect upgrade request: %w", err)
	}

	br := bufio.NewReaderSize(conn, 4096)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		_ = conn.Close()
		return ConnectResult{}, fmt.Errorf("reading connect upgrade response: %w", err)
	}

	if resp.StatusCode != http.StatusSwitchingProtocols {
		apiErr := parseAPIError(resp)
		_ = resp.Body.Close()
		_ = conn.Close()
		return ConnectResult{}, apiErr
	}
	if !strings.EqualFold(strings.TrimSpace(resp.Header.Get("Upgrade")), "websocket") {
		_ = conn.Close()
		return ConnectResult{}, fmt.Errorf("connect upgrade missing Upgrade: websocket response header (got %q)", resp.Header.Get("Upgrade"))
	}
	wantAccept := wsupgrade.Accept(secKey)
	if gotAccept := strings.TrimSpace(resp.Header.Get("Sec-WebSocket-Accept")); gotAccept != wantAccept {
		_ = conn.Close()
		return ConnectResult{}, fmt.Errorf("connect upgrade Sec-WebSocket-Accept mismatch: got %q, want %q", gotAccept, wantAccept)
	}

	// Clear handshake deadline so the long-lived mux manages liveness via its own pings.
	_ = conn.SetDeadline(time.Time{})

	wsStream := wsupgrade.NewClientConn(wsupgrade.BufferedConn(conn, br))
	return ConnectResult{
		Conn:      wsStream,
		SessionID: strings.TrimSpace(resp.Header.Get(HeaderTunnelSession)),
		Protocol:  strings.TrimSpace(resp.Header.Get(HeaderConnectProtocol)),
	}, nil
}

func tunnelViaHTTPConnectProxy(conn net.Conn, proxyURL *url.URL, targetAddr string) (net.Conn, error) {
	connectReq := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Opaque: targetAddr},
		Host:   targetAddr,
		Header: make(http.Header),
	}
	if proxyURL.User != nil {
		pw, _ := proxyURL.User.Password()
		creds := base64.StdEncoding.EncodeToString([]byte(proxyURL.User.Username() + ":" + pw))
		connectReq.Header.Set("Proxy-Authorization", "Basic "+creds)
	}
	if err := connectReq.Write(conn); err != nil {
		return conn, fmt.Errorf("writing proxy CONNECT %s: %w", targetAddr, err)
	}
	br := bufio.NewReaderSize(conn, 4096)
	resp, err := http.ReadResponse(br, connectReq)
	if err != nil {
		return conn, fmt.Errorf("reading proxy CONNECT %s response: %w", targetAddr, err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return conn, fmt.Errorf("proxy CONNECT %s failed with HTTP %d (%s)", targetAddr, resp.StatusCode, resp.Status)
	}
	return wsupgrade.BufferedConn(conn, br), nil
}

// TunnelSession wraps an active client-side *mux.Mux established over grain connect.
type TunnelSession struct {
	Mux       *mux.Mux
	SessionID string
	Protocol  string
}

// Close closes the underlying mux and WebSocket connection.
func (s *TunnelSession) Close() error {
	if s == nil || s.Mux == nil {
		return nil
	}
	return s.Mux.Close()
}

// Done returns a channel closed when the tunnel mux terminates.
func (s *TunnelSession) Done() <-chan struct{} {
	return s.Mux.Done()
}

// Listener returns a net.Listener that yields incoming streams opened by
// grain-registry over this TunnelSession.
func (s *TunnelSession) Listener() *MuxListener {
	return NewMuxListener(s.Mux)
}

// ConnectMux dials grain-registry via DialConnect and wraps the upgraded
// connection in a client-side *mux.Mux (15s ping interval/timeout, 16 MiB
// per-stream receive window by default).
func ConnectMux(ctx context.Context, cfg ConnectConfig, muxOverride *mux.Config) (*TunnelSession, error) {
	res, err := DialConnect(ctx, cfg)
	if err != nil {
		return nil, err
	}
	mcfg := mux.Config{
		IsServer:         false,
		PingInterval:     defaultTunnelPingInterval,
		PingTimeout:      defaultTunnelPingTimeout,
		MaxBufferedBytes: defaultTunnelMaxBufferedBytes,
	}
	if muxOverride != nil {
		mcfg = *muxOverride
		mcfg.IsServer = false
	}
	m := mux.NewMux(res.Conn, mcfg)
	return &TunnelSession{
		Mux:       m,
		SessionID: res.SessionID,
		Protocol:  res.Protocol,
	}, nil
}

type muxListenerAddr string

func (a muxListenerAddr) Network() string { return "mux" }
func (a muxListenerAddr) String() string  { return string(a) }

// MuxListener adapts a *mux.Mux's incoming streams to the standard net.Listener
// interface so an http.Server can serve tunneled registry requests directly.
//
// Accepted connections report RemoteAddr().Network() == "mux" and
// "stream-<id>-remote", ensuring loopback trust checks never match tunneled
// requests.
type MuxListener struct {
	m         *mux.Mux
	ctx       context.Context
	cancel    context.CancelFunc
	closed    atomic.Bool
	closeOnce sync.Once
}

// NewMuxListener wraps m in a net.Listener.
func NewMuxListener(m *mux.Mux) *MuxListener {
	if m == nil {
		panic("registry/api: NewMuxListener called with nil *mux.Mux")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &MuxListener{
		m:      m,
		ctx:    ctx,
		cancel: cancel,
	}
}

// Accept waits for and returns the next incoming mux stream as a net.Conn.
func (l *MuxListener) Accept() (net.Conn, error) {
	if l.closed.Load() {
		return nil, net.ErrClosed
	}
	stream, err := l.m.AcceptStreamContext(l.ctx)
	if err != nil {
		if l.closed.Load() || errors.Is(err, context.Canceled) || errors.Is(err, mux.ErrSessionClosed) || errors.Is(err, io.EOF) {
			return nil, net.ErrClosed
		}
		return nil, err
	}
	if l.closed.Load() {
		_ = stream.Reset()
		return nil, net.ErrClosed
	}
	return stream, nil
}

// Close stops accepting new streams so blocked Accept calls return
// net.ErrClosed while allowing in-flight streams on the underlying *mux.Mux to
// drain (for http.Server.Shutdown during graceful session rotation) until
// TunnelSession.Close closes the mux. Any new stream opened by the peer after
// Close is immediately reset.
func (l *MuxListener) Close() error {
	l.closed.Store(true)
	l.closeOnce.Do(func() {
		l.cancel()
		go func() {
			for {
				s, err := l.m.AcceptStream()
				if err != nil {
					return
				}
				_ = s.Reset()
			}
		}()
	})
	return nil
}

// Addr returns a non-IP net.Addr with Network() == "mux".
func (l *MuxListener) Addr() net.Addr {
	return muxListenerAddr("grain-registry-tunnel")
}
