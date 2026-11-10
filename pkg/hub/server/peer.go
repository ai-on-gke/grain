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

package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/hub/store"
)

const (
	// PeerAuthAudience is the fixed JWT audience for replica-to-replica `:8082` forwarding.
	PeerAuthAudience = "urn:grain:registry:peer"

	// HeaderPeerAuth carries the ES256 peer JWT on `:8082` requests.
	HeaderPeerAuth = "X-Registry-Peer-Auth"

	// HeaderHubHop enforces single-hop peer forwarding on `:8082`.
	HeaderHubHop = "X-Registry-Hop"

	// HeaderTunnelGone signals to an ingress replica that the target tunnel session
	// is no longer present on the peer replica.
	HeaderTunnelGone = "X-Registry-Tunnel-Gone"

	// HeaderTargetURI preserves the original request path and query across the
	// `/internal/tunnel/<name>` peer forwarding hop.
	HeaderTargetURI = "X-Registry-Target-URI"

	// PeerTokenTTL is the lifetime of a signed `X-Registry-Peer-Auth` token (5m).
	PeerTokenTTL = 5 * time.Minute

	// PeerTokenRefreshAfter is when a cached `X-Registry-Peer-Auth` token is refreshed (4m).
	PeerTokenRefreshAfter = 4 * time.Minute

	// PeerDialTimeout bounds TCP dial latency when connecting to a sibling replica on `:8082`.
	PeerDialTimeout = 2 * time.Second
)

// PeerTokenCache caches a replica's outbound `X-Registry-Peer-Auth` JWT (5m TTL,
// refreshed at 4m) and verifies inbound peer tokens against the registry's JWKS.
type PeerTokenCache struct {
	signer    AssertionSigner
	replicaID string
	nowFn     func() time.Time

	mu        sync.Mutex
	token     string
	refreshAt time.Time
}

// NewPeerTokenCache creates a PeerTokenCache for replicaID.
func NewPeerTokenCache(signer AssertionSigner, replicaID string) *PeerTokenCache {
	return &PeerTokenCache{
		signer:    signer,
		replicaID: replicaID,
		nowFn:     time.Now,
	}
}

// SetNow overrides the clock used by PeerTokenCache.
func (c *PeerTokenCache) SetNow(fn func() time.Time) {
	if c != nil && fn != nil {
		c.mu.Lock()
		c.nowFn = fn
		c.mu.Unlock()
	}
}

// GetToken returns a cached `X-Registry-Peer-Auth` JWT or signs a fresh one.
func (c *PeerTokenCache) GetToken(ctx context.Context) (string, error) {
	if c == nil || c.signer == nil {
		return "", errors.New("peer token signer is not configured")
	}
	now := c.nowFn()
	c.mu.Lock()
	if c.token != "" && now.Before(c.refreshAt) {
		tok := c.token
		c.mu.Unlock()
		return tok, nil
	}
	c.mu.Unlock()

	tok, err := c.signer.SignPeerToken(ctx, c.replicaID, PeerTokenTTL)
	if err != nil {
		return "", err
	}

	c.mu.Lock()
	c.token = tok
	c.refreshAt = now.Add(PeerTokenRefreshAfter)
	c.mu.Unlock()
	return tok, nil
}

// VerifyRequest validates the `X-Registry-Peer-Auth` header on an inbound `:8082` request.
func (c *PeerTokenCache) VerifyRequest(r *http.Request) error {
	if c == nil || c.signer == nil {
		return errors.New("peer token verifier is not configured")
	}
	raw := strings.TrimSpace(r.Header.Get(HeaderPeerAuth))
	if strings.HasPrefix(raw, "Bearer ") {
		raw = strings.TrimSpace(strings.TrimPrefix(raw, "Bearer "))
	}
	if raw == "" {
		return errors.New("missing X-Registry-Peer-Auth header")
	}
	_, err := c.signer.VerifyPeerToken(r.Context(), raw)
	return err
}

type tunnelGoneError struct {
	peerAddr string
	cause    error
}

func (e *tunnelGoneError) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("peer %s tunnel gone: %v", e.peerAddr, e.cause)
	}
	return fmt.Sprintf("peer %s tunnel gone", e.peerAddr)
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

// PeerHandler returns the HTTP handler for the `:8082` replica-to-replica peer listener.
// It serves `GET /healthz` and `/internal/tunnel/<name>`.
func (rt *Router) PeerHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/internal/tunnel/") {
			writeJSONError(w, http.StatusNotFound, "not_found", "peer endpoint not found")
			return
		}
		rt.handlePeerTunnelRequest(w, r)
	})
}

func (rt *Router) handlePeerTunnelRequest(w http.ResponseWriter, r *http.Request) {
	// 1. Verify X-Registry-Peer-Auth (`aud = "urn:grain:registry:peer"`).
	if err := rt.peerTokens.VerifyRequest(r); err != nil {
		writeJSONError(w, http.StatusUnauthorized, "peer_unauthenticated", "valid X-Registry-Peer-Auth token required")
		return
	}

	// 2. Enforce single-hop X-Registry-Hop guard.
	hops := r.Header.Values(HeaderHubHop)
	if len(hops) == 0 || strings.TrimSpace(hops[0]) == "" {
		writeJSONError(w, http.StatusBadRequest, "missing_hop_header", "X-Registry-Hop header is required")
		return
	}
	if len(hops) > 1 || strings.TrimSpace(hops[0]) != "1" {
		writeJSONError(w, http.StatusLoopDetected, "loop_detected", "single-hop X-Registry-Hop limit exceeded")
		return
	}

	// 3. Extract <name> from /internal/tunnel/<name>[/<rest>].
	rest := strings.TrimPrefix(r.URL.Path, "/internal/tunnel/")
	entryName := rest
	suffixPath := "/"
	if slash := strings.IndexByte(rest, '/'); slash >= 0 {
		entryName = rest[:slash]
		suffixPath = rest[slash:]
	}
	entryName = strings.ToLower(strings.TrimSpace(entryName))
	if entryName == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_entry_name", "missing entry name in peer path")
		return
	}

	// 4. Look up local active tunnel session, waiting out a handshake that has
	// acquired the session here but not yet registered the tunnel.
	local := rt.GetLocalTunnel(entryName)
	if local == nil && rt.awaitHandshake(r.Context(), entryName) {
		local = rt.GetLocalTunnel(entryName)
	}
	if local == nil {
		w.Header().Set(HeaderTunnelGone, "1")
		writeJSONError(w, http.StatusServiceUnavailable, "tunnel_gone", "tunnel session is not active on this replica")
		return
	}

	// 5. Restore original target URI and strip internal peer headers before forwarding over mux.
	targetURI := strings.TrimSpace(r.Header.Get(HeaderTargetURI))
	r.Header.Del(HeaderPeerAuth)
	r.Header.Del(HeaderHubHop)
	r.Header.Del(HeaderTargetURI)

	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = pr.In.Host
			if pr.Out.URL.Host == "" {
				pr.Out.URL.Host = entryName + "." + rt.opts.InstanceDomain
			}
			pr.Out.Host = pr.In.Host
			for k, vv := range pr.In.Header {
				pr.Out.Header[k] = append([]string(nil), vv...)
			}
			if targetURI != "" {
				if parsed, err := url.ParseRequestURI(targetURI); err == nil {
					pr.Out.URL.Path = parsed.Path
					pr.Out.URL.RawPath = parsed.RawPath
					pr.Out.URL.RawQuery = parsed.RawQuery
					return
				}
			}
			pr.Out.URL.Path = suffixPath
			pr.Out.URL.RawPath = ""
		},
		Transport:     local.Transport(),
		FlushInterval: -1,
		ModifyResponse: func(resp *http.Response) error {
			HardenResponseCookies(resp)
			return nil
		},
		ErrorHandler: func(rw http.ResponseWriter, req *http.Request, err error) {
			if local.IsClosed() || local.IsSuperseded() {
				rw.Header().Set(HeaderTunnelGone, "1")
				writeJSONError(rw, http.StatusServiceUnavailable, "tunnel_gone", "tunnel session closed during request")
				return
			}
			handleProxyError(rw, req, err)
		},
	}
	proxy.ServeHTTP(w, r)
}

// forwardToPeer proxies a request for entry to the peer replica at peerAddr
// that holds its tunnel. If that peer reports the tunnel gone, a replayable
// request goes back to handleOfflineOrWait to wait for the reconnect, within
// graceDeadline when it is already inside a reconnect_grace (see
// routeConnectedEntry).
func (rt *Router) forwardToPeer(w http.ResponseWriter, r *http.Request, entry *store.EntryRow, isManager bool, peerAddr string, graceDeadline time.Time) {
	// Refuse if request already carries X-Registry-Hop (single-hop guard).
	if strings.TrimSpace(r.Header.Get(HeaderHubHop)) != "" {
		writeJSONError(w, http.StatusLoopDetected, "loop_detected", "single-hop X-Registry-Hop limit exceeded")
		return
	}

	peerToken, err := rt.peerTokens.GetToken(r.Context())
	if err != nil {
		writeInternalError(w, err)
		return
	}

	targetHost := NormalizePeerAddr(peerAddr)
	if targetHost == "" {
		rt.tunnelCache.InvalidatePeerGone(entry.Name)
		rt.handleOfflineOrWait(w, r, entry, isManager, graceDeadline)
		return
	}

	if rt.opts.OnPeerForward != nil {
		rt.opts.OnPeerForward()
	}

	origURI := r.URL.RequestURI()
	origHost := r.Host

	baseTransport := rt.peerHTTPTransport
	if baseTransport == nil {
		baseTransport = &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   PeerDialTimeout,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ResponseHeaderTimeout:  DefaultResponseHeaderTimeout,
			MaxResponseHeaderBytes: DefaultMaxResponseHeaderBytes,
			MaxIdleConnsPerHost:    32,
			IdleConnTimeout:        60 * time.Second,
		}
	}

	interceptTransport := roundTripperFunc(func(outReq *http.Request) (*http.Response, error) {
		resp, rtErr := baseTransport.RoundTrip(outReq)
		if rtErr != nil {
			return nil, &tunnelGoneError{peerAddr: targetHost, cause: rtErr}
		}
		if resp.StatusCode == http.StatusServiceUnavailable && resp.Header.Get(HeaderTunnelGone) == "1" {
			_ = resp.Body.Close()
			return nil, &tunnelGoneError{peerAddr: targetHost}
		}
		return resp, nil
	})

	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = targetHost
			pr.Out.URL.Path = "/internal/tunnel/" + entry.Name
			pr.Out.URL.RawPath = ""
			pr.Out.URL.RawQuery = ""
			pr.Out.Host = origHost
			for k, vv := range pr.In.Header {
				pr.Out.Header[k] = append([]string(nil), vv...)
			}
			pr.Out.Header.Set(HeaderPeerAuth, peerToken)
			pr.Out.Header.Set(HeaderHubHop, "1")
			pr.Out.Header.Set(HeaderTargetURI, origURI)
		},
		Transport:     interceptTransport,
		FlushInterval: -1,
		ModifyResponse: func(resp *http.Response) error {
			HardenResponseCookies(resp)
			return nil
		},
		ErrorHandler: func(rw http.ResponseWriter, req *http.Request, proxyErr error) {
			var goneErr *tunnelGoneError
			if errors.As(proxyErr, &goneErr) {
				rt.tunnelCache.InvalidatePeerGone(entry.Name)
				if isReplayableRequest(r) {
					rt.handleOfflineOrWait(rw, r, entry, isManager, graceDeadline)
					return
				}
				tc, _ := rt.tunnelCache.Get(entry.Name)
				WriteOfflineConnectedState(rw, r, rt.enrichOfflineEntry(r.Context(), entry), tc, rt.now(), isManager)
				return
			}
			handleProxyError(rw, req, proxyErr)
		},
	}
	proxy.ServeHTTP(w, r)
}

// NormalizePeerAddr strips any http:// scheme prefix or trailing slash from a replica peer_addr.
func NormalizePeerAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	addr = strings.TrimPrefix(addr, "http://")
	addr = strings.TrimPrefix(addr, "https://")
	addr = strings.TrimRight(addr, "/")
	return addr
}
