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

package pkgcache

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPlaywrightBinaryCached(t *testing.T) {
	up := newUpstream(t, map[string]string{
		"/builds/chromium/1080/chromium-linux.zip": "chromium-1080-bytes",
		"/builds/firefox/1429/firefox.tar.gz":      "firefox-1429-bytes",
		"/builds/webkit/1944/webkit.tgz":           "webkit-1944-bytes",
	})
	s, cache := newTestServer(t, Config{
		Client: up.client(),
	})

	// Test cdn.playwright.dev
	u1 := "http://cdn.playwright.dev/builds/chromium/1080/chromium-linux.zip"
	get(t, s, u1)
	if up.hits["/builds/chromium/1080/chromium-linux.zip"] != 1 {
		t.Fatalf("first fetch went upstream %d times, want 1", up.hits["/builds/chromium/1080/chromium-linux.zip"])
	}
	get(t, s, u1)
	if up.hits["/builds/chromium/1080/chromium-linux.zip"] != 1 {
		t.Fatalf("second fetch went upstream %d times, want 1 (cache hit)", up.hits["/builds/chromium/1080/chromium-linux.zip"])
	}

	// Test playwright.azureedge.net
	u2 := "http://playwright.azureedge.net/builds/firefox/1429/firefox.tar.gz"
	get(t, s, u2)
	if up.hits["/builds/firefox/1429/firefox.tar.gz"] != 1 {
		t.Fatalf("first fetch went upstream %d times, want 1", up.hits["/builds/firefox/1429/firefox.tar.gz"])
	}
	get(t, s, u2)
	if up.hits["/builds/firefox/1429/firefox.tar.gz"] != 1 {
		t.Fatalf("second fetch went upstream %d times, want 1 (cache hit)", up.hits["/builds/firefox/1429/firefox.tar.gz"])
	}

	// Test playwright.download.prss.microsoft.com
	u3 := "http://playwright.download.prss.microsoft.com/builds/webkit/1944/webkit.tgz"
	get(t, s, u3)
	if up.hits["/builds/webkit/1944/webkit.tgz"] != 1 {
		t.Fatalf("first fetch went upstream %d times, want 1", up.hits["/builds/webkit/1944/webkit.tgz"])
	}
	get(t, s, u3)
	if up.hits["/builds/webkit/1944/webkit.tgz"] != 1 {
		t.Fatalf("second fetch went upstream %d times, want 1 (cache hit)", up.hits["/builds/webkit/1944/webkit.tgz"])
	}

	// Verify stats
	stats, err := cache.Stats()
	if err != nil {
		t.Fatalf("cache.Stats: %v", err)
	}
	var pwStats KindStats
	for _, st := range stats {
		if st.Kind == KindPlaywright {
			pwStats = st
		}
	}
	if pwStats.Entries != 3 || pwStats.Bytes == 0 {
		t.Errorf("playwright stats = %+v, want 3 entries with non-zero bytes", pwStats)
	}
}

func TestPlaywrightNonCacheablePassThrough(t *testing.T) {
	up := newUpstream(t, map[string]string{
		"/builds/manifest.json": `{"version":"1.0"}`,
		"/index.html":           `<html>playwright</html>`,
		"/other/file.zip":       `other-zip-data`,
	})
	s, cache := newTestServer(t, Config{
		Client: up.client(),
	})

	for _, path := range []string{"/builds/manifest.json", "/index.html", "/other/file.zip"} {
		u := "http://cdn.playwright.dev" + path
		get(t, s, u)
		get(t, s, u)
		if up.hits[path] != 2 {
			t.Errorf("path %s was fetched %d times, want 2 (uncached pass-through)", path, up.hits[path])
		}
	}

	if cache.Bytes() != 0 {
		t.Errorf("cache stored %d bytes for non-cacheable paths, want 0", cache.Bytes())
	}
}

func TestPlaywrightRangeRequest(t *testing.T) {
	content := "0123456789abcdefghijklmnopqrstuvwxyz"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "chromium-linux.zip", time.Time{}, strings.NewReader(content))
	}))
	defer srv.Close()

	s, cache := newTestServer(t, Config{
		PlaywrightURL: srv.URL,
		Client:        srv.Client(),
	})

	req, err := http.NewRequest(http.MethodGet, "http://cdn.playwright.dev/builds/chromium/1080/chromium-linux.zip", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Range", "bytes=0-9")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206 Partial Content", rec.Code)
	}
	if rec.Header().Get(cacheStatusHeader) != "pass" {
		t.Errorf("cache status = %q, want pass", rec.Header().Get(cacheStatusHeader))
	}
	if rec.Body.String() != "0123456789" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "0123456789")
	}
	if cache.Bytes() != 0 {
		t.Errorf("cache has %d bytes, want 0 after range request", cache.Bytes())
	}
}

func TestPlaywrightHEADRequest(t *testing.T) {
	up := newUpstream(t, map[string]string{
		"/builds/chromium/1080/chromium-linux.zip": "chromium-bytes",
	})
	s, cache := newTestServer(t, Config{
		Client: up.client(),
	})

	req, err := http.NewRequest(http.MethodHead, "http://cdn.playwright.dev/builds/chromium/1080/chromium-linux.zip", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("HEAD returned body length %d, want 0", rec.Body.Len())
	}
	if cache.Bytes() != 0 {
		t.Errorf("HEAD request populated cache with %d bytes, want 0", cache.Bytes())
	}
}

func TestPlaywrightPathTraversalRefusal(t *testing.T) {
	s, _ := newTestServer(t, Config{})

	// Transparent path traversal
	req, _ := http.NewRequest(http.MethodGet, "http://cdn.playwright.dev/builds/../secret", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("transparent traversal answered %d, want 400", rec.Code)
	}

	// Legacy path traversal
	req2, _ := http.NewRequest(http.MethodGet, "/playwright/../secret", nil)
	rec2 := httptest.NewRecorder()
	s.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusBadRequest {
		t.Errorf("legacy traversal answered %d, want 400", rec2.Code)
	}
}

func TestPlaywrightLegacyPrefix(t *testing.T) {
	up := newUpstream(t, map[string]string{
		"/builds/chromium/1080/chromium-linux.zip": "chromium-legacy-bytes",
	})
	s, cache := newTestServer(t, Config{
		Client: up.client(),
	})

	u := "/playwright/builds/chromium/1080/chromium-linux.zip"
	get(t, s, u)
	if up.hits["/builds/chromium/1080/chromium-linux.zip"] != 1 {
		t.Fatalf("first legacy fetch hits = %d, want 1", up.hits["/builds/chromium/1080/chromium-linux.zip"])
	}
	get(t, s, u)
	if up.hits["/builds/chromium/1080/chromium-linux.zip"] != 1 {
		t.Fatalf("second legacy fetch hits = %d, want 1 (cached)", up.hits["/builds/chromium/1080/chromium-linux.zip"])
	}

	if cache.Bytes() == 0 {
		t.Errorf("cache bytes = 0, want non-zero")
	}
}

func TestPlaywrightFlush(t *testing.T) {
	up := newUpstream(t, map[string]string{
		"/builds/chromium/1080/chromium-linux.zip": "chromium-flush-bytes",
	})
	s, cache := newTestServer(t, Config{
		Client: up.client(),
	})

	u := "http://cdn.playwright.dev/builds/chromium/1080/chromium-linux.zip"
	get(t, s, u)

	entries, freed, err := cache.Flush(KindPlaywright)
	if err != nil {
		t.Fatalf("Flush(KindPlaywright): %v", err)
	}
	if entries != 1 || freed == 0 {
		t.Errorf("Flush returned entries=%d, freed=%d, want 1 and >0", entries, freed)
	}
	if cache.Bytes() != 0 {
		t.Errorf("cache still has %d bytes after flush", cache.Bytes())
	}

	// Next fetch goes upstream again
	get(t, s, u)
	if up.hits["/builds/chromium/1080/chromium-linux.zip"] != 2 {
		t.Errorf("hits after flush = %d, want 2", up.hits["/builds/chromium/1080/chromium-linux.zip"])
	}
}

func TestPlaywrightTLSInterception(t *testing.T) {
	ca, err := NewCA()
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}

	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/builds/") && strings.HasSuffix(r.URL.Path, ".zip") {
			w.Header().Set("Content-Type", "application/zip")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("playwright-binary-tls"))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("other"))
	}))
	defer upstreamServer.Close()

	upstreamClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "tcp", upstreamServer.Listener.Addr().String())
			},
		},
	}

	srv, _ := newTestServer(t, Config{
		Client:         upstreamClient,
		PyPIFileScheme: "http",
	})

	proxyServer := httptest.NewUnstartedServer(srv)
	proxyServer.TLS = ca.TLSConfig()
	proxyServer.StartTLS()
	defer proxyServer.Close()

	rootPool := x509.NewCertPool()
	rootPool.AppendCertsFromPEM(ca.RootCertPEM())

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "tcp", proxyServer.Listener.Addr().String())
			},
			TLSClientConfig: &tls.Config{
				RootCAs: rootPool,
			},
		},
	}

	// First fetch (miss)
	resp, err := client.Get("https://cdn.playwright.dev/builds/chromium/1080/chromium-linux.zip")
	if err != nil {
		t.Fatalf("client.Get: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "playwright-binary-tls" {
		t.Fatalf("body = %q, want %q", body, "playwright-binary-tls")
	}
	if got := resp.Header.Get(cacheStatusHeader); got != "miss" {
		t.Errorf("cacheStatus (1) = %q, want miss", got)
	}

	// Second fetch (hit)
	resp2, err := client.Get("https://cdn.playwright.dev/builds/chromium/1080/chromium-linux.zip")
	if err != nil {
		t.Fatalf("client.Get (2): %v", err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if string(body2) != "playwright-binary-tls" {
		t.Fatalf("body (2) = %q, want %q", body2, "playwright-binary-tls")
	}
	if got := resp2.Header.Get(cacheStatusHeader); got != "hit" {
		t.Errorf("cacheStatus (2) = %q, want hit", got)
	}
}

func TestPlaywrightDNSInterception(t *testing.T) {
	interceptIP := net.ParseIP("172.17.0.1")
	srv, err := NewDNSServer(DNSConfig{
		ListenAddr:     "127.0.0.1:0",
		InterceptIP:    interceptIP,
		AllowedDomains: DefaultInterceptedDomains,
		ZeroEgress:     true,
	})
	if err != nil {
		t.Fatalf("NewDNSServer: %v", err)
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("srv.Start: %v", err)
	}
	defer srv.Close()

	r := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "udp", srv.Addr().String())
		},
	}

	for _, domain := range []string{
		"cdn.playwright.dev",
		"playwright.azureedge.net",
		"playwright.download.prss.microsoft.com",
	} {
		ips, err := r.LookupHost(context.Background(), domain)
		if err != nil {
			t.Errorf("LookupHost(%q): %v", domain, err)
			continue
		}
		if len(ips) == 0 || ips[0] != "172.17.0.1" {
			t.Errorf("LookupHost(%q) = %v, want [%s]", domain, ips, "172.17.0.1")
		}
	}

	// Non-intercepted domain under ZeroEgress should fail to resolve
	_, err = r.LookupHost(context.Background(), "unintercepted.example.com")
	if err == nil {
		t.Errorf("LookupHost(unintercepted.example.com) succeeded under ZeroEgress, want error")
	}
}
