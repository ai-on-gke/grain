package pkgcache

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestDockerAuthEndpointDirectProxy(t *testing.T) {
	ca, err := NewCA()
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}

	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			if r.URL.Query().Get("service") != "ghcr.io" {
				t.Errorf("upstream service = %q, want ghcr.io", r.URL.Query().Get("service"))
			}
			if r.URL.Query().Get("scope") != "repository:bwsalmon/grain:pull,push" {
				t.Errorf("upstream scope = %q, want repository:bwsalmon/grain:pull,push", r.URL.Query().Get("scope"))
			}
			if auth := r.Header.Get("Authorization"); auth != "Basic dGVzdDp0b2tlbg==" {
				t.Errorf("upstream auth header = %q, want Basic dGVzdDp0b2tlbg==", auth)
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"token":"valid-bearer-token"}`))
			return
		}
		http.NotFound(w, r)
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
		Client:       upstreamClient,
		GHCRRegistry: "http://ghcr.io",
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

	req, err := http.NewRequest(http.MethodGet, "https://ghcr.io/token?service=ghcr.io&scope=repository:bwsalmon/grain:pull,push", nil)
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Basic dGVzdDp0b2tlbg==")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 OK", resp.StatusCode)
	}
	if got := resp.Header.Get(cacheStatusHeader); got != "pass" {
		t.Errorf("cacheStatus = %q, want pass", got)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("io.ReadAll: %v", err)
	}
	if string(body) != `{"token":"valid-bearer-token"}` {
		t.Errorf("body = %q, want %q", string(body), `{"token":"valid-bearer-token"}`)
	}
}

func TestDockerAuthEndpointsVariations(t *testing.T) {
	ca, err := NewCA()
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}

	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token", "/auth/token", "/oauth/token", "/v2/token", "/v2/auth":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"token":"tok"}`))
		default:
			http.NotFound(w, r)
		}
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
		Client:       upstreamClient,
		GHCRRegistry: "http://ghcr.io",
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

	paths := []string{
		"/token",
		"/auth/token",
		"/oauth/token",
		"/v2/token",
		"/v2/auth",
	}

	for _, p := range paths {
		t.Run(p, func(t *testing.T) {
			resp, err := client.Get("https://ghcr.io" + p)
			if err != nil {
				t.Fatalf("client.Get(%s): %v", p, err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("%s: status = %d, want 200", p, resp.StatusCode)
			}
			if got := resp.Header.Get(cacheStatusHeader); got != "pass" {
				t.Errorf("%s: cacheStatus = %q, want pass", p, got)
			}
		})
	}

	// Requests with dot-dot path traversal should be refused with 400 Bad Request
	badResp, err := client.Get("https://ghcr.io/token/../secret")
	if err != nil {
		t.Fatalf("client.Get dot-dot: %v", err)
	}
	defer badResp.Body.Close()
	if badResp.StatusCode != http.StatusBadRequest {
		t.Errorf("dot-dot status = %d, want 400", badResp.StatusCode)
	}
}

func TestDockerReferrersAndExtensions(t *testing.T) {
	ca, err := NewCA()
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}

	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/referrers/"):
			w.Header().Set("Content-Type", "application/vnd.oci.image.index.v1+json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"schemaVersion":2,"manifests":[]}`))
		case strings.HasSuffix(r.URL.Path, "/tags/list"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"name":"grain","tags":["v1.0.0"]}`))
		default:
			http.NotFound(w, r)
		}
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
		Client:       upstreamClient,
		GHCRRegistry: "http://ghcr.io",
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

	// 1. Referrers extension
	resp1, err := client.Get("https://ghcr.io/v2/bwsalmon/grain/referrers/sha256:1111111111111111111111111111111111111111111111111111111111111111")
	if err != nil {
		t.Fatalf("referrers Get: %v", err)
	}
	defer resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Errorf("referrers status = %d, want 200", resp1.StatusCode)
	}
	if got := resp1.Header.Get(cacheStatusHeader); got != "pass" {
		t.Errorf("referrers cacheStatus = %q, want pass", got)
	}

	// 2. Tags list endpoint
	resp2, err := client.Get("https://ghcr.io/v2/bwsalmon/grain/tags/list")
	if err != nil {
		t.Fatalf("tags/list Get: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("tags/list status = %d, want 200", resp2.StatusCode)
	}
	if got := resp2.Header.Get(cacheStatusHeader); got != "pass" {
		t.Errorf("tags/list cacheStatus = %q, want pass", got)
	}
}

func TestDockerMirrorReferrersAndTags(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/referrers/"):
			w.Header().Set("Content-Type", "application/vnd.oci.image.index.v1+json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"schemaVersion":2,"manifests":[]}`))
		case strings.HasSuffix(r.URL.Path, "/tags/list"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"name":"debian","tags":["bookworm-slim"]}`))
		default:
			http.NotFound(w, r)
		}
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
		DockerRegistry: upstreamServer.URL,
		GHCRRegistry:   upstreamServer.URL,
	})

	proxyServer := httptest.NewServer(srv)
	defer proxyServer.Close()

	mirrorClient := proxyServer.Client()

	tests := []struct {
		name       string
		path       string
		method     string
		wantStatus int
		wantCache  string
		wantBody   string
	}{
		{
			name:       "Docker Hub mirror referrers",
			path:       "/v2/library/debian/referrers/sha256:1111111111111111111111111111111111111111111111111111111111111111",
			method:     http.MethodGet,
			wantStatus: http.StatusOK,
			wantCache:  "pass",
			wantBody:   `{"schemaVersion":2,"manifests":[]}`,
		},
		{
			name:       "Docker Hub mirror referrers with query params",
			path:       "/v2/library/debian/referrers/sha256:1111111111111111111111111111111111111111111111111111111111111111?artifactType=application%2Fvnd.oci.empty.v1%2Bjson",
			method:     http.MethodGet,
			wantStatus: http.StatusOK,
			wantCache:  "pass",
			wantBody:   `{"schemaVersion":2,"manifests":[]}`,
		},
		{
			name:       "Docker Hub mirror referrers HEAD",
			path:       "/v2/library/debian/referrers/sha256:1111111111111111111111111111111111111111111111111111111111111111",
			method:     http.MethodHead,
			wantStatus: http.StatusOK,
			wantCache:  "pass",
			wantBody:   "",
		},
		{
			name:       "Docker Hub mirror tags list",
			path:       "/v2/library/debian/tags/list",
			method:     http.MethodGet,
			wantStatus: http.StatusOK,
			wantCache:  "pass",
			wantBody:   `{"name":"debian","tags":["bookworm-slim"]}`,
		},
		{
			name:       "Docker Hub prefixed mirror referrers",
			path:       "/v2/docker.io/library/debian/referrers/sha256:1111111111111111111111111111111111111111111111111111111111111111",
			method:     http.MethodGet,
			wantStatus: http.StatusOK,
			wantCache:  "pass",
			wantBody:   `{"schemaVersion":2,"manifests":[]}`,
		},
		{
			name:       "GHCR prefixed mirror referrers",
			path:       "/v2/ghcr/bwsalmon/grain/referrers/sha256:1111111111111111111111111111111111111111111111111111111111111111",
			method:     http.MethodGet,
			wantStatus: http.StatusOK,
			wantCache:  "pass",
			wantBody:   `{"schemaVersion":2,"manifests":[]}`,
		},
		{
			name:       "GHCR prefixed mirror tags list",
			path:       "/v2/ghcr/bwsalmon/grain/tags/list",
			method:     http.MethodGet,
			wantStatus: http.StatusOK,
			wantCache:  "pass",
			wantBody:   `{"name":"debian","tags":["bookworm-slim"]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(tt.method, proxyServer.URL+tt.path, nil)
			if err != nil {
				t.Fatalf("http.NewRequest: %v", err)
			}
			resp, err := mirrorClient.Do(req)
			if err != nil {
				t.Fatalf("client.Do: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
			if got := resp.Header.Get(cacheStatusHeader); got != tt.wantCache {
				t.Errorf("cacheStatus = %q, want %q", got, tt.wantCache)
			}
			if tt.method != http.MethodHead {
				body, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Fatalf("io.ReadAll: %v", err)
				}
				if string(body) != tt.wantBody {
					t.Errorf("body = %q, want %q", string(body), tt.wantBody)
				}
			}
		})
	}
}

func TestSplitDockerRef(t *testing.T) {
	tests := []struct {
		path        string
		wantName    string
		wantSection string
		wantRef     string
		wantOK      bool
	}{
		{"library/debian/manifests/bookworm-slim", "library/debian", "manifests", "bookworm-slim", true},
		{"library/debian/blobs/sha256:1111111111111111111111111111111111111111111111111111111111111111", "library/debian", "blobs", "sha256:1111111111111111111111111111111111111111111111111111111111111111", true},
		{"library/debian/referrers/sha256:1111111111111111111111111111111111111111111111111111111111111111", "library/debian", "referrers", "sha256:1111111111111111111111111111111111111111111111111111111111111111", true},
		{"library/debian/tags/list", "library/debian", "tags", "list", true},
		{"nested/org/repo/referrers/sha256:1234", "nested/org/repo", "referrers", "sha256:1234", true},
		{"nested/org/repo/tags/list", "nested/org/repo", "tags", "list", true},
		{"library/debian/referrers/", "", "", "", false},
		{"library/debian/tags/", "", "", "", false},
		{"library/debian/referrers/sha256:123/extra", "", "", "", false},
		{"library/debian/unknown/sha256:123", "", "", "", false},
		{"/manifests/latest", "", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			name, section, ref, ok := splitDockerRef(tt.path)
			if ok != tt.wantOK {
				t.Fatalf("splitDockerRef(%q) ok = %v, want %v", tt.path, ok, tt.wantOK)
			}
			if ok {
				if name != tt.wantName || section != tt.wantSection || ref != tt.wantRef {
					t.Errorf("splitDockerRef(%q) = (%q, %q, %q), want (%q, %q, %q)",
						tt.path, name, section, ref, tt.wantName, tt.wantSection, tt.wantRef)
				}
			}
		})
	}
}

func TestIsDockerAuthEndpoint(t *testing.T) {
	tests := []struct {
		path  string
		query url.Values
		want  bool
	}{
		{"/token", url.Values{}, true},
		{"/auth/token", url.Values{}, true},
		{"/oauth/token", url.Values{}, true},
		{"/v2/token", url.Values{}, true},
		{"/v2/auth", url.Values{}, true},
		{"/some/custom/token", url.Values{}, true},
		{"/some/custom/auth", url.Values{}, true},
		{"/anypath", url.Values{"service": []string{"ghcr.io"}}, true},
		{"/anypath", url.Values{"scope": []string{"repository:grain:pull"}}, true},
		{"/v2/bwsalmon/grain/manifests/v1.0.0", url.Values{}, false},
		{"/v2/bwsalmon/grain/blobs/sha256:123", url.Values{}, false},
		{"/v2/", url.Values{}, false},
	}

	for _, tt := range tests {
		got := isDockerAuthEndpoint(tt.path, tt.query)
		if got != tt.want {
			t.Errorf("isDockerAuthEndpoint(%q, %v) = %v, want %v", tt.path, tt.query, got, tt.want)
		}
	}
}
