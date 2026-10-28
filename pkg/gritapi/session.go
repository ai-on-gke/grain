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

package gritapi

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/tunnel/mux"
	"github.com/ai-on-gke/grain/pkg/tunnel/wsupgrade"
)

// OfferHeader names reverse-stream offers ("git-proxy", "capability-ports",
// "ci-api") sent during the `GET /v1/session` upgrade handshake.
const OfferHeader = "X-Grit-Offer"

type bufferedConn struct {
	net.Conn
	r io.Reader
}

func (c *bufferedConn) Read(b []byte) (int, error) {
	return c.r.Read(b)
}

func wrapBufferedConn(conn net.Conn, br *bufio.Reader) net.Conn {
	if br != nil && br.Buffered() > 0 {
		return &bufferedConn{
			Conn: conn,
			r:    io.MultiReader(br, conn),
		}
	}
	return conn
}

// ParseOffers extracts deduplicated, non-empty offer tokens from an HTTP request's
// `X-Grit-Offer` headers and `offer` query parameters.
func ParseOffers(r *http.Request) []string {
	var out []string
	seen := make(map[string]bool)
	add := func(raw string) {
		for _, part := range strings.Split(raw, ",") {
			tok := strings.TrimSpace(part)
			if tok != "" && !seen[tok] {
				seen[tok] = true
				out = append(out, tok)
			}
		}
	}
	for _, v := range r.Header.Values(OfferHeader) {
		add(v)
	}
	for _, v := range r.URL.Query()["offer"] {
		add(v)
	}
	return out
}

// DialUpgrade dials rawBaseURL's `/v1/session` endpoint, performs an RFC 6455
// WebSocket upgrade handshake with optional client token, bearer token, and
// offer headers, and returns the upgraded connection.
func DialUpgrade(ctx context.Context, rawBaseURL, clientToken, bearerToken string, offers []string) (net.Conn, error) {
	base := strings.TrimSpace(rawBaseURL)
	if !strings.Contains(base, "://") {
		base = "http://" + base
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("gritapi: parse url %q: %w", rawBaseURL, err)
	}

	host := u.Host
	if !strings.Contains(host, ":") {
		if u.Scheme == "https" || u.Scheme == "wss" {
			host += ":443"
		} else {
			host += ":80"
		}
	}

	dialer := &net.Dialer{Timeout: 10 * time.Second}
	var rawConn net.Conn
	if u.Scheme == "https" || u.Scheme == "wss" {
		rawConn, err = tls.DialWithDialer(dialer, "tcp", host, &tls.Config{
			ServerName: u.Hostname(),
			MinVersion: tls.VersionTLS12,
		})
	} else {
		rawConn, err = dialer.DialContext(ctx, "tcp", host)
	}
	if err != nil {
		return nil, fmt.Errorf("gritapi: dial %s: %w", host, err)
	}

	path := strings.TrimRight(u.Path, "/") + "/v1/session"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		_ = rawConn.Close()
		return nil, err
	}
	req.Host = u.Host
	wsupgrade.SetRequestHeaders(req.Header)
	wsKey := req.Header.Get("Sec-WebSocket-Key")
	if clientToken != "" {
		req.Header.Set(ClientHeader, clientToken)
	}
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}
	if len(offers) > 0 {
		req.Header.Set(OfferHeader, strings.Join(offers, ","))
	}

	if err := req.Write(rawConn); err != nil {
		_ = rawConn.Close()
		return nil, fmt.Errorf("gritapi: write session upgrade: %w", err)
	}

	br := bufio.NewReader(rawConn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		_ = rawConn.Close()
		return nil, fmt.Errorf("gritapi: read session upgrade response: %w", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		_ = rawConn.Close()
		return nil, DecodeAPIError(resp.StatusCode, body)
	}
	if got := resp.Header.Get("Sec-WebSocket-Accept"); got != "" && wsKey != "" {
		if want := wsupgrade.Accept(wsKey); got != want {
			_ = rawConn.Close()
			return nil, fmt.Errorf("gritapi: invalid Sec-WebSocket-Accept %q", got)
		}
	}

	return wrapBufferedConn(rawConn, br), nil
}

// Session represents an upgraded `/v1/session` multiplexed tunnel connection.
type Session struct {
	mux    *mux.Mux
	offers []string
}

// NewSession wraps an upgraded connection in a `mux.Mux`.
func NewSession(conn net.Conn, isServer bool, offers []string) *Session {
	return &Session{
		mux: mux.NewMux(conn, mux.Config{
			IsServer: isServer,
		}),
		offers: append([]string(nil), offers...),
	}
}

// Mux returns the underlying stream multiplexer.
func (s *Session) Mux() *mux.Mux {
	return s.mux
}

// Offers returns the reverse-stream offers negotiated on this session.
func (s *Session) Offers() []string {
	return append([]string(nil), s.offers...)
}

// Close closes the underlying multiplexed session.
func (s *Session) Close() error {
	if s == nil || s.mux == nil {
		return nil
	}
	return s.mux.Close()
}

// HTTPClient returns an `*http.Client` that opens a new mux stream for each HTTP
// request over this session.
func (s *Session) HTTPClient() *http.Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			if s == nil || s.mux == nil {
				return nil, errors.New("gritapi: session is nil")
			}
			return s.mux.OpenStream(ctx)
		},
		DisableKeepAlives: true,
	}
	return &http.Client{
		Transport: tr,
		Timeout:   60 * time.Second,
	}
}

// ConnectSession upgrades `GET /v1/session` on `c.baseURL` into a multiplexed
// `pkg/tunnel/mux` session, declaring any reverse-stream offers, and switches `c`
// to route subsequent `/v1` and `/pkg/v1` calls over mux streams on that session.
func (c *Client) ConnectSession(ctx context.Context, offers ...string) (*Session, error) {
	conn, err := DialUpgrade(ctx, c.baseURL, c.clientToken, c.bearerToken, offers)
	if err != nil {
		return nil, err
	}
	sess := NewSession(conn, false, offers)
	c.httpClient = sess.HTTPClient()
	return sess, nil
}

// MuxListener adapts `*mux.Mux.AcceptStream` to `net.Listener` so standard
// `http.Server` instances can serve HTTP requests over incoming mux streams.
type MuxListener struct {
	mux *mux.Mux
}

// NewMuxListener wraps m as a `net.Listener`.
func NewMuxListener(m *mux.Mux) *MuxListener {
	return &MuxListener{mux: m}
}

// Accept waits for the next stream opened by the peer.
func (l *MuxListener) Accept() (net.Conn, error) {
	return l.mux.AcceptStream()
}

// Close closes the underlying multiplexer.
func (l *MuxListener) Close() error {
	return l.mux.Close()
}

// Addr returns a synthetic address for the mux listener.
func (l *MuxListener) Addr() net.Addr {
	return muxAddr("grit-session-mux")
}

type muxAddr string

func (a muxAddr) Network() string { return "mux" }
func (a muxAddr) String() string  { return string(a) }

// SessionHub accepts incoming reverse-tunnel `GET /v1/session` connections from
// `grit serve -grain-url ...` and provides a `Client` that opens streams back
// to the connected `grit` server over the reverse tunnel.
type SessionHub struct {
	expectedToken string

	mu      sync.RWMutex
	session *Session
	readyCh chan struct{}
}

// NewSessionHub constructs a reverse-tunnel `/v1/session` acceptor.
// If expectedToken is non-empty, incoming `GET /v1/session` requests must carry
// it in `Grit-Client` or `Authorization: Bearer`.
func NewSessionHub(expectedToken string) *SessionHub {
	return &SessionHub{
		expectedToken: strings.TrimSpace(expectedToken),
		readyCh:       make(chan struct{}),
	}
}

// ServeHTTP handles `GET /v1/session` WebSocket upgrade requests from a remote
// `grit serve -grain-url ...` process.
func (h *SessionHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if h.expectedToken != "" {
		tok := strings.TrimSpace(r.Header.Get(ClientHeader))
		if tok == "" {
			if auth := strings.TrimSpace(r.Header.Get("Authorization")); strings.HasPrefix(strings.ToLower(auth), "bearer ") {
				tok = strings.TrimSpace(auth[len("bearer "):])
			}
		}
		if tok != h.expectedToken {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized","message":"invalid session client token"}`))
			return
		}
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), wsupgrade.Protocol) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad_request","message":"expected websocket upgrade"}`))
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	conn, brw, err := hijacker.Hijack()
	if err != nil {
		return
	}

	_, _ = fmt.Fprintf(brw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: %s\r\nConnection: Upgrade\r\n%s\r\n", wsupgrade.Protocol, wsupgrade.AcceptHeader(r))
	if err := brw.Flush(); err != nil {
		_ = conn.Close()
		return
	}

	wrapped := wrapBufferedConn(conn, brw.Reader)
	offers := ParseOffers(r)
	sess := NewSession(wrapped, true, offers)

	h.mu.Lock()
	old := h.session
	h.session = sess
	select {
	case <-h.readyCh:
	default:
		close(h.readyCh)
	}
	h.mu.Unlock()

	if old != nil {
		_ = old.Close()
	}

	<-sess.Mux().Done()
}

// WaitConnected blocks until at least one reverse-tunnel `/v1/session` has connected
// or ctx is cancelled.
func (h *SessionHub) WaitConnected(ctx context.Context) error {
	h.mu.RLock()
	ch := h.readyCh
	h.mu.RUnlock()
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Offers returns the offers announced by the currently connected session.
func (h *SessionHub) Offers() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.session == nil {
		return nil
	}
	return h.session.Offers()
}

// HTTPClient returns an `*http.Client` that dials streams over the active
// reverse-tunnel session.
func (h *SessionHub) HTTPClient() *http.Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			h.mu.RLock()
			sess := h.session
			h.mu.RUnlock()
			if sess == nil {
				return nil, errors.New("gritapi: no active /v1/session connected to hub")
			}
			return sess.Mux().OpenStream(ctx)
		},
		DisableKeepAlives: true,
	}
	return &http.Client{
		Transport: tr,
		Timeout:   60 * time.Second,
	}
}

// NewClient constructs a `*Client` that routes requests over the reverse-tunnel
// `/v1/session` accepted by this hub.
func (h *SessionHub) NewClient(cfg ClientConfig) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "http://grit.session"
	}
	cfg.HTTPClient = h.HTTPClient()
	return NewClient(cfg)
}

// Close closes any active session held by the hub.
func (h *SessionHub) Close() error {
	h.mu.Lock()
	sess := h.session
	h.session = nil
	h.mu.Unlock()
	if sess != nil {
		return sess.Close()
	}
	return nil
}
