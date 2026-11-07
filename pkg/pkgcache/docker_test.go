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

func TestDockerArtifactRegistryBlobUploadDirectProxy(t *testing.T) {
	ca, err := NewCA()
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}

	const (
		registryHost      = "us-central1-docker.pkg.dev"
		uploadPath        = "/artifacts-uploads/namespaces/acme-proj/repositories/containers/uploads/upload-123"
		escapedUploadPath = "/artifacts-uploads/namespaces/acme-proj/repositories/containers/uploads/upload-123%2Fpart"
		uploadURL         = "https://" + registryHost + uploadPath
		blobBytes         = "artifact-registry-layer-bytes"
	)
	wantDigest := digestOf(blobBytes)

	var chunkedBytes []byte
	var finalBytes []byte
	var sawEscapedUploadPath bool
	var sawSecretV1 bool
	var leakedInternalHeader string
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/secret" || strings.Contains(r.URL.Path, "..") {
			sawSecretV1 = true
		}
		for _, hdr := range []string{"Grit-Job-Token", "Grit-Cache-Trust", "X-Hop-Secret"} {
			if v := r.Header.Get(hdr); v != "" {
				leakedInternalHeader = hdr + "=" + v
			}
		}
		if r.Host != registryHost {
			http.Error(w, "unexpected host: "+r.Host, http.StatusBadRequest)
			return
		}
		if r.Header.Get("Authorization") != "Bearer ar-push-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v2/acme-proj/containers/widgets/blobs/uploads/":
			w.Header().Set("Location", uploadURL)
			w.WriteHeader(http.StatusAccepted)
		case r.Method == http.MethodPatch && r.URL.EscapedPath() == escapedUploadPath:
			sawEscapedUploadPath = true
			w.Header().Set("Location", "https://"+registryHost+escapedUploadPath)
			w.WriteHeader(http.StatusAccepted)
		case r.Method == http.MethodPatch && r.URL.Path == uploadPath:
			b, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			chunkedBytes = append(chunkedBytes, b...)
			w.Header().Set("Location", uploadURL)
			w.WriteHeader(http.StatusAccepted)
		case r.Method == http.MethodPut && r.URL.Path == uploadPath:
			if got := r.URL.Query().Get("digest"); got != wantDigest {
				http.Error(w, "unexpected digest: "+got, http.StatusBadRequest)
				return
			}
			b, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			finalBytes = append(chunkedBytes, b...)
			w.Header().Set("Docker-Content-Digest", wantDigest)
			w.WriteHeader(http.StatusCreated)
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
		DockerRegistry: "http://registry-1.docker.io",
		GHCRRegistry:   "http://ghcr.io",
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

	doReq := func(method, rawURL, body string) *http.Response {
		t.Helper()
		var bodyReader io.Reader
		if body != "" {
			bodyReader = strings.NewReader(body)
		}
		req, err := http.NewRequest(method, rawURL, bodyReader)
		if err != nil {
			t.Fatalf("http.NewRequest(%s, %s): %v", method, rawURL, err)
		}
		req.Header.Set("Authorization", "Bearer ar-push-token")
		req.Header.Set("Grit-Job-Token", "internal-job-secret")
		req.Header.Set("Grit-Cache-Trust", "trusted")
		req.Header.Set("Connection", "X-Hop-Secret")
		req.Header.Set("X-Hop-Secret", "hop-secret")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("client.Do(%s %s): %v", method, rawURL, err)
		}
		return resp
	}

	// Step 1: Start blob upload under /v2/.../blobs/uploads/.
	postResp := doReq(http.MethodPost, "https://"+registryHost+"/v2/acme-proj/containers/widgets/blobs/uploads/", "")
	postResp.Body.Close()
	if postResp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST blobs/uploads status = %d, want 202", postResp.StatusCode)
	}
	loc := postResp.Header.Get("Location")
	if loc != uploadURL {
		t.Fatalf("POST blobs/uploads Location = %q, want %q", loc, uploadURL)
	}

	// Step 2: Follow Location to /artifacts-uploads/... with PATCH chunk.
	patchResp := doReq(http.MethodPatch, loc, blobBytes[:10])
	patchResp.Body.Close()
	if patchResp.StatusCode != http.StatusAccepted {
		t.Fatalf("PATCH %s status = %d, want 202", loc, patchResp.StatusCode)
	}
	if got := patchResp.Header.Get(cacheStatusHeader); got != "pass" {
		t.Errorf("PATCH cacheStatus = %q, want pass", got)
	}
	loc = patchResp.Header.Get("Location")
	if loc != uploadURL {
		t.Fatalf("PATCH Location = %q, want %q", loc, uploadURL)
	}

	// Step 3: Complete upload with PUT /artifacts-uploads/...?digest=...
	putResp := doReq(http.MethodPut, loc+"?digest="+url.QueryEscape(wantDigest), blobBytes[10:])
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT %s status = %d, want 201", loc, putResp.StatusCode)
	}
	if got := putResp.Header.Get(cacheStatusHeader); got != "pass" {
		t.Errorf("PUT cacheStatus = %q, want pass", got)
	}
	if got := putResp.Header.Get("Docker-Content-Digest"); got != wantDigest {
		t.Errorf("Docker-Content-Digest = %q, want %q", got, wantDigest)
	}
	if string(finalBytes) != blobBytes {
		t.Errorf("uploaded bytes = %q, want %q", string(finalBytes), blobBytes)
	}
	if leakedInternalHeader != "" {
		t.Errorf("internal/hop-by-hop header leaked to upstream: %s", leakedInternalHeader)
	}

	// Percent-encoded characters in upload session IDs are preserved untouched.
	escResp := doReq(http.MethodPatch, "https://"+registryHost+escapedUploadPath, "chunk")
	escResp.Body.Close()
	if escResp.StatusCode != http.StatusAccepted || !sawEscapedUploadPath {
		t.Errorf("PATCH escaped upload path status = %d (sawEscaped=%v), want 202 and preserved %%2F", escResp.StatusCode, sawEscapedUploadPath)
	}

	// Non-*.pkg.dev registries and bare pkg.dev still reject /artifacts-uploads/,
	// even when auth-looking query strings or /token suffixes are added.
	for _, host := range []string{"ghcr.io", "registry-1.docker.io", "pkg.dev"} {
		for _, suffix := range []string{
			uploadPath + "?digest=" + url.QueryEscape(wantDigest),
			uploadPath + "?scope=repository:acme/widgets:pull",
			uploadPath + "/token",
		} {
			resp := doReq(http.MethodPut, "https://"+host+suffix, blobBytes)
			resp.Body.Close()
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("PUT https://%s%s status = %d, want 404", host, suffix, resp.StatusCode)
			}
		}
	}

	// Other non-/v2/ paths (including /v2beta1/...) on *.pkg.dev remain 404,
	// and both single- and double-encoded dot-dot traversals cannot reach /v1/secret.
	for _, nonV2 := range []string{"/v1/projects/acme-proj", "/v2beta1/projects/acme-proj"} {
		otherResp := doReq(http.MethodPut, "https://"+registryHost+nonV2, blobBytes)
		otherResp.Body.Close()
		if otherResp.StatusCode != http.StatusNotFound {
			t.Errorf("PUT %s on %s status = %d, want 404", nonV2, registryHost, otherResp.StatusCode)
		}
	}

	badResp := doReq(http.MethodPut, "https://"+registryHost+"/artifacts-uploads/../v1/secret", blobBytes)
	badResp.Body.Close()
	if badResp.StatusCode != http.StatusBadRequest {
		t.Errorf("PUT dot-dot on %s status = %d, want 400", registryHost, badResp.StatusCode)
	}

	doubleBadResp := doReq(http.MethodPut, "https://"+registryHost+"/artifacts-uploads/%252e%252e/v1/secret", blobBytes)
	doubleBadResp.Body.Close()
	if sawSecretV1 {
		t.Errorf("double-encoded dot-dot reached /v1/secret on upstream")
	}
}

func TestDockerNeverForwardsCredentialsToNonGHCRHosts(t *testing.T) {
	creds := writeTestCredentialLadder(t, map[string]string{
		"*": "global",
	}, map[string]string{
		"global": "daemon-secret-gh-pat",
	})

	var leakedAuth string
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "" && auth != "Bearer anon" {
			leakedAuth = r.Host + " " + r.URL.RequestURI() + " -> " + auth
		}
		if r.URL.Path == "/v2/acmecorp/widgets/manifests/latest" && r.Header.Get("Authorization") == "" {
			w.Header().Set("Www-Authenticate", `Bearer realm="https://ghcr-mirror-docker.pkg.dev/token",service="ghcr.io",scope="repository:acmecorp/widgets:pull"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"token":"anon"}`))
	}))
	defer upstreamServer.Close()

	upURL, err := url.Parse(upstreamServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	srv, _ := newTestServer(t, Config{
		Client:            &http.Client{Transport: roundTripTo{base: upURL}},
		Credentials:       creds,
		AllowRegistryPush: true,
	})

	for _, c := range []struct {
		method, host, target string
	}{
		{http.MethodGet, "us-central1-docker.pkg.dev", "/artifacts-uploads/namespaces/p/repositories/r/uploads/u1?service=ghcr.io&scope=repository:acmecorp/widgets:pull"},
		{http.MethodPut, "us-central1-docker.pkg.dev", "/artifacts-uploads/namespaces/p/repositories/r/uploads/u1?ns=ghcr.io&scope=repository:acmecorp/widgets:pull"},
		{http.MethodGet, "us-central1-docker.pkg.dev", "/token?service=ghcr.io&scope=repository:acmecorp/widgets:pull"},
		{http.MethodPost, "us-central1-docker.pkg.dev", "/v2/ghcr/acmecorp/widgets/blobs/uploads/"},
		{http.MethodGet, "ghcr-mirror-docker.pkg.dev", "/v2/acmecorp/widgets/manifests/latest"},
		{http.MethodGet, "auth.docker.io", "/token?service=ghcr.io&scope=repository:acmecorp/widgets:pull"},
	} {
		req := httptest.NewRequest(c.method, c.target, nil)
		req.Host = c.host
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if leakedAuth != "" {
			t.Fatalf("daemon GitHub credential forwarded to non-GHCR host: %s", leakedAuth)
		}
	}
}
