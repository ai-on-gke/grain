// Copyright 2026 The grain Authors. All rights reserved.
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
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ai-on-gke/grain/pkg/webauth"
)

const (
	// MaxProxiedRequestBodyBytes caps non-upgraded proxied request bodies at 64 MiB.
	MaxProxiedRequestBodyBytes int64 = 64 << 20

	// DefaultResponseHeaderTimeout is the default 60s timeout waiting for backend response headers.
	DefaultResponseHeaderTimeout = 60 * time.Second

	// DefaultMaxResponseHeaderBytes caps backend response headers at 64 KiB.
	DefaultMaxResponseHeaderBytes = 64 << 10
)

// ErrTooManyStreams is returned when a connected entry already has 256 concurrent open streams.
var ErrTooManyStreams = errors.New("hub server: max concurrent tunnel streams exceeded")

// VerifyPreStripCSRF runs `webauth.VerifySameOriginForProxyAuth` BEFORE stripping IAP cookies
// on unsafe methods that arrived with `Cookie`, `Origin`, or `Sec-Fetch-Site` headers.
func VerifyPreStripCSRF(r *http.Request, entryHostname string) error {
	if r == nil {
		return nil
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return nil
	}
	if r.Header.Get("Cookie") == "" && r.Header.Get("Origin") == "" && r.Header.Get("Sec-Fetch-Site") == "" {
		return nil
	}
	clone := r.Clone(r.Context())
	if entryHostname != "" {
		clone.Host = entryHostname
	}
	// Clear X-Forwarded-Host on the clone so VerifySameOriginForProxyAuth compares
	// Origin strictly against the canonical entryHostname (`clone.Host`).
	clone.Header.Del("X-Forwarded-Host")
	return webauth.VerifySameOriginForProxyAuth(clone)
}

// IsUpgradeRequest reports whether `r` is an HTTP/1.1 connection upgrade (e.g. WebSocket or `/api/hostd/connect`).
func IsUpgradeRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	if strings.TrimSpace(r.Header.Get("Upgrade")) != "" {
		return true
	}
	for _, part := range strings.Split(r.Header.Get("Connection"), ",") {
		if strings.EqualFold(strings.TrimSpace(part), "upgrade") {
			return true
		}
	}
	return false
}

// SanitizeProxyRequest strips all edge credentials, IAP cookies, incoming registry/peer headers,
// and edge-audience OIDC bearer tokens, then sets canonical outbound Host and X-Forwarded-* headers.
func SanitizeProxyRequest(r *http.Request, entryHostname, hubHost, iapAudience, signedAssertion, requestID string) {
	if r == nil {
		return
	}

	// 1. Remove edge and registry-internal headers (including all X-Forwarded-* and X-Goog-* identity headers).
	for k := range r.Header {
		lower := strings.ToLower(k)
		switch {
		case lower == "x-goog-iap-jwt-assertion",
			strings.HasPrefix(lower, "x-goog-authenticated-user-"),
			strings.HasPrefix(lower, "x-goog-iap-"),
			lower == "x-grain-hub-assertion",
			lower == "x-grain-hub-token",
			lower == "x-grain-registry-assertion",
			lower == "x-grain-registry-token",
			lower == "x-grain-auth",
			lower == "x-registry-peer-auth",
			lower == "x-registry-hop",
			lower == "x-registry-target-uri",
			lower == "x-grain-on-behalf-of",
			lower == "x-grain-subject-token",
			lower == "proxy-authorization",
			lower == "forwarded",
			lower == "x-real-ip",
			strings.HasPrefix(lower, "x-forwarded-"):
			r.Header.Del(k)
		}
	}

	// 2. Strip Authorization: Bearer <tok> (and matching X-Grain-Tunnel-Token) when <tok>
	// is an edge/IAP credential (Google OAuth2 `ya29.` access tokens, `grc_…` registry
	// tokens, or OIDC JWTs minted for the registry's IAP backend service, registry host,
	// routed entry host, `/*` Cloud Run Direct IAP wildcard audiences, or a
	// `*.apps.googleusercontent.com` IAP OAuth client), while preserving grain-scoped
	// tokens such as `gma_…` and installation access codes.
	for _, auth := range r.Header.Values("Authorization") {
		if shouldStripAuthorizationHeader(strings.TrimSpace(auth), entryHostname, hubHost, iapAudience) {
			r.Header.Del("Authorization")
			break
		}
	}
	for _, tunTok := range r.Header.Values("X-Grain-Tunnel-Token") {
		rawTok := strings.TrimSpace(tunTok)
		if rest, ok := cutBearerPrefix(rawTok); ok {
			rawTok = rest
		}
		if shouldStripBearerToken(rawTok, entryHostname, hubHost, iapAudience) {
			r.Header.Del("X-Grain-Tunnel-Token")
			break
		}
	}

	// 3. Strip IAP cookies (`GCP_IAAP_AUTH_TOKEN_*`, `GCP_IAAP_*`, `__Host-GCP_IAP_*`, `GCP_IAP_*`).
	stripIAPCookies(r.Header)

	// 4. Preserve outbound Host: <name>.<instance-domain> and set canonical proxy headers.
	if entryHostname != "" {
		r.Host = entryHostname
		r.Header.Set("X-Forwarded-Host", entryHostname)
	}
	r.Header.Set("X-Forwarded-Proto", "https")

	if requestID == "" {
		requestID = uuid.NewString()
	}
	r.Header.Set("X-Request-Id", requestID)

	if signedAssertion != "" {
		// Under both names for a release: a daemon from before the
		// Grain Hub rename reads only the old one.
		r.Header.Set(webauth.HubAssertionHeader, signedAssertion)
		r.Header.Set(webauth.RegistryAssertionHeader, signedAssertion)
	}
}

func cutBearerPrefix(s string) (string, bool) {
	if len(s) < 7 || !strings.EqualFold(s[:6], "bearer") {
		return "", false
	}
	if s[6] != ' ' && s[6] != '\t' {
		return "", false
	}
	return strings.TrimSpace(s[7:]), true
}

func shouldStripAuthorizationHeader(authHeader, entryHostname, hubHost, iapAudience string) bool {
	tok, ok := cutBearerPrefix(authHeader)
	if !ok {
		return false
	}
	return shouldStripBearerToken(tok, entryHostname, hubHost, iapAudience)
}

func shouldStripBearerToken(tok, entryHostname, hubHost, iapAudience string) bool {
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return false
	}
	// Preserve grain machine/access tokens (`gma_…`).
	if strings.HasPrefix(tok, "gma_") {
		return false
	}
	// Strip registry credential tokens (`grc_…`) and Google OAuth2 access tokens
	// (`ya29.…`, such as Google-managed Cloud IAP programmatic bearer tokens) if
	// sent on a proxied request so they never reach a connected grain.
	if strings.HasPrefix(tok, "grc_") || strings.HasPrefix(tok, "ya29.") {
		return true
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return false
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		payloadBytes, err = base64.URLEncoding.DecodeString(parts[1])
		if err != nil {
			return false
		}
	}
	var claims struct {
		Aud any `json:"aud"`
	}
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return false
	}

	normReg := NormalizeHost(hubHost)
	normEntry := NormalizeHost(entryHostname)
	iapAud := strings.TrimSpace(iapAudience)
	const googleOAuthClientSuffix = ".apps.googleusercontent.com"

	matchesHostAud := func(a, normHost string) bool {
		if normHost == "" {
			return false
		}
		if strings.EqualFold(a, normHost) {
			return true
		}
		trimmed := strings.TrimSuffix(strings.TrimSuffix(a, "/*"), "/")
		return strings.EqualFold(trimmed, "https://"+normHost)
	}

	matchesEdgeAud := func(a string) bool {
		a = strings.TrimSpace(a)
		if a == "" {
			return false
		}
		if iapAud != "" && a == iapAud {
			return true
		}
		lowerA := strings.ToLower(a)
		if strings.HasSuffix(lowerA, googleOAuthClientSuffix) && len(lowerA) > len(googleOAuthClientSuffix) {
			return true
		}
		if matchesHostAud(a, normReg) || matchesHostAud(a, normEntry) {
			return true
		}
		return false
	}

	switch v := claims.Aud.(type) {
	case string:
		return matchesEdgeAud(v)
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && matchesEdgeAud(s) {
				return true
			}
		}
	}
	return false
}

func stripIAPCookies(h http.Header) {
	rawLines := h.Values("Cookie")
	if len(rawLines) == 0 {
		return
	}
	var kept []string
	for _, line := range rawLines {
		for _, part := range strings.Split(line, ";") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			name := part
			if eq := strings.IndexByte(part, '='); eq >= 0 {
				name = strings.TrimSpace(part[:eq])
			}
			if isIAPCookieName(name) {
				continue
			}
			kept = append(kept, part)
		}
	}
	h.Del("Cookie")
	if len(kept) > 0 {
		h.Set("Cookie", strings.Join(kept, "; "))
	}
}

func isIAPCookieName(name string) bool {
	upper := strings.ToUpper(strings.TrimSpace(name))
	return strings.HasPrefix(upper, "GCP_IAAP_AUTH_TOKEN_") ||
		strings.HasPrefix(upper, "GCP_IAAP_") ||
		strings.HasPrefix(upper, "__HOST-GCP_IAP_") ||
		strings.HasPrefix(upper, "GCP_IAP_")
}

// HardenResponseCookies rewrites every `Set-Cookie` header on `resp` to strip any
// `Domain` attribute (so cookies stay host-only on `<name>.<instance-domain>`) and
// enforce `Secure` and `HttpOnly` (while preserving `__Host-` prefix semantics).
func HardenResponseCookies(resp *http.Response) {
	if resp == nil {
		return
	}
	lines := resp.Header.Values("Set-Cookie")
	if len(lines) == 0 {
		return
	}
	resp.Header.Del("Set-Cookie")
	for _, line := range lines {
		hardened := HardenSetCookieValue(line)
		if hardened != "" {
			resp.Header.Add("Set-Cookie", hardened)
		}
	}
}

// HardenSetCookieValue strips `Domain=...` and ensures `Secure` and `HttpOnly` on a `Set-Cookie` header value.
func HardenSetCookieValue(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parts := strings.Split(raw, ";")
	nameVal := strings.TrimSpace(parts[0])
	if nameVal == "" {
		return ""
	}
	isHostPrefix := strings.HasPrefix(nameVal, "__Host-")

	var attrs []string
	hasSecure := false
	hasHTTPOnly := false
	hasPath := false

	for _, attr := range parts[1:] {
		attr = strings.TrimSpace(attr)
		if attr == "" {
			continue
		}
		attrName := attr
		if eq := strings.IndexByte(attr, '='); eq >= 0 {
			attrName = strings.TrimSpace(attr[:eq])
		}
		lower := strings.ToLower(attrName)
		if lower == "domain" {
			// Strip Domain attribute so the cookie is strictly host-only.
			continue
		}
		if lower == "secure" {
			hasSecure = true
		}
		if lower == "httponly" {
			hasHTTPOnly = true
		}
		if lower == "path" {
			if isHostPrefix {
				attrs = append(attrs, "Path=/")
				hasPath = true
				continue
			}
			hasPath = true
		}
		attrs = append(attrs, attr)
	}

	if isHostPrefix && !hasPath {
		attrs = append(attrs, "Path=/")
	}
	if !hasSecure {
		attrs = append(attrs, "Secure")
	}
	if !hasHTTPOnly {
		attrs = append(attrs, "HttpOnly")
	}
	return strings.Join(append([]string{nameVal}, attrs...), "; ")
}

// NewInstanceReverseProxy builds an `*httputil.ReverseProxy` that routes sanitized
// requests to `targetURL` (for `kind = instance`) or over `transport` (for `kind = connected`
// or `:8082` peer forwarding), preserving `Host: <entryHostname>` and hardening `Set-Cookie`.
func NewInstanceReverseProxy(targetURL *url.URL, entryHostname string, transport http.RoundTripper) *httputil.ReverseProxy {
	if transport == nil {
		transport = &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ResponseHeaderTimeout:  DefaultResponseHeaderTimeout,
			MaxResponseHeaderBytes: DefaultMaxResponseHeaderBytes,
			IdleConnTimeout:        90 * time.Second,
		}
	}

	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = targetURL.Scheme
			pr.Out.URL.Host = targetURL.Host
			if targetURL.Path != "" && targetURL.Path != "/" {
				pr.Out.URL.Path = singleJoiningSlash(targetURL.Path, pr.In.URL.Path)
			} else {
				pr.Out.URL.Path = pr.In.URL.Path
				pr.Out.URL.RawPath = pr.In.URL.RawPath
			}
			pr.Out.URL.RawQuery = pr.In.URL.RawQuery
			// Copy already-sanitized headers from pr.In without adding X-Forwarded-For
			// or overwriting Host with targetURL.Host.
			for k, vv := range pr.In.Header {
				pr.Out.Header[k] = append([]string(nil), vv...)
			}
			if entryHostname != "" {
				pr.Out.Host = entryHostname
				pr.Out.Header.Set("X-Forwarded-Host", entryHostname)
			} else {
				pr.Out.Host = pr.In.Host
			}
			pr.Out.Header.Set("X-Forwarded-Proto", "https")
		},
		Transport:     transport,
		FlushInterval: -1, // Immediate unbuffered streaming for SSE (text/event-stream) and chunked responses.
		ModifyResponse: func(resp *http.Response) error {
			HardenResponseCookies(resp)
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			handleProxyError(w, r, err)
		},
	}
}

func handleProxyError(w http.ResponseWriter, r *http.Request, err error) {
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) || (err != nil && strings.Contains(err.Error(), "http: request body too large")) {
		writeJSONError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "request body exceeds 64 MiB limit")
		return
	}
	if errors.Is(err, ErrTooManyStreams) {
		w.Header().Set("Retry-After", "2")
		writeJSONError(w, http.StatusServiceUnavailable, "stream_limit_exceeded", "entry has reached the maximum number of concurrent streams (256); retry shortly")
		return
	}
	if isBackendTimeoutError(r, err) {
		writeJSONError(w, http.StatusGatewayTimeout, "gateway_timeout", "backend did not respond within timeout")
		return
	}
	writeJSONError(w, http.StatusBadGateway, "bad_gateway", "upstream grain backend is unreachable or returned an invalid response")
}

func isBackendTimeoutError(r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	if r != nil && r.Context().Err() != nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	if strings.Contains(strings.ToLower(err.Error()), "timeout awaiting response headers") {
		return true
	}
	return false
}

func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	}
	return a + b
}
