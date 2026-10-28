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
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"sync"
	"testing"
)

// transparentLayer is a layer the stand-in registries below serve, one per
// registry so that each registry's first pull is a miss rather than a hit
// on the copy another registry's pull stored under the same digest.
func transparentLayer(registry string) string {
	return "docker-layer-blob-bytes from " + registry
}

// transparentLayers maps each transparentLayer's digest to its bytes, which
// is what the stand-in registries answer a blob request with: a blob is
// only stored once its bytes hash to the digest it was asked for by.
var transparentLayers = func() map[string]string {
	layers := map[string]string{}
	for _, registry := range []string{"docker.io", "ghcr.io", "gcr.io", "quay.io", "registry.k8s.io", "pkg.dev"} {
		layers[digestOf(transparentLayer(registry))] = transparentLayer(registry)
	}
	return layers
}()

func TestTransparentE2E(t *testing.T) {
	ca, err := NewCA()
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}

	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/builds/"):
			w.Header().Set("Content-Type", "application/zip")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("playwright-build-bytes"))
		case strings.HasSuffix(r.URL.Path, ".deb"):
			w.Header().Set("Content-Type", "application/vnd.debian.binary-package")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("deb-binary-bytes"))
		case strings.HasSuffix(r.URL.Path, ".apk"):
			w.Header().Set("Content-Type", "application/vnd.android.package-archive")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("apk-binary-bytes"))
		case strings.HasSuffix(r.URL.Path, ".tgz"):
			w.Header().Set("Content-Type", "application/gzip")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("npm-tarball-bytes"))
		case strings.HasSuffix(r.URL.Path, ".whl"):
			w.Header().Set("Content-Type", "application/zip")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("pypi-wheel-bytes"))
		case strings.HasSuffix(r.URL.Path, ".zip"):
			w.Header().Set("Content-Type", "application/zip")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("go-module-zip-bytes"))
		case strings.HasPrefix(r.URL.Path, "/tile/"):
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("sumdb-tile-bytes"))
		case strings.Contains(r.URL.Path, "/blobs/sha256:"):
			w.Header().Set("Content-Type", "application/octet-stream")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(transparentLayers[path.Base(r.URL.Path)]))
		case strings.Contains(r.URL.Path, "/referrers/"):
			w.Header().Set("Content-Type", "application/vnd.oci.image.index.v1+json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"schemaVersion":2,"manifests":[]}`))
		case strings.HasSuffix(r.URL.Path, "/tags/list"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"name":"test","tags":["v1.0.0"]}`))
		case strings.HasPrefix(r.URL.Path, "/simple"):
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("<a href=\"https://files.pythonhosted.org/packages/pkg-1.0.whl\">pkg</a>"))
		case r.URL.Path == "/express":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"name":"express"}`))
		case r.URL.Path == "/playwright":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"name":"playwright"}`))
		case r.URL.Path == "/auth/token":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"token":"valid-token"}`))
		default:
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("ok"))
		}
	}))
	defer upstreamServer.Close()

	// Direct all upstream client calls to upstreamServer
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
		AptScheme:      "http",
		PyPIFileScheme: "http",
		PyPIIndex:      "http://pypi.org/simple",
		NPMRegistry:    "http://registry.npmjs.org",
		GoProxy:        "http://proxy.golang.org",
		DockerRegistry: "http://registry-1.docker.io",
		GHCRRegistry:   "http://ghcr.io",
		GCRRegistry:    "http://gcr.io",
	})

	proxyServer := httptest.NewUnstartedServer(srv)
	proxyServer.TLS = ca.TLSConfig()
	proxyServer.StartTLS()
	defer proxyServer.Close()

	proxyAddr := proxyServer.Listener.Addr().String()

	rootPool := x509.NewCertPool()
	rootPool.AppendCertsFromPEM(ca.RootCertPEM())

	// Simulate DNS interception by resolving every intercepted host to the proxy server address
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "tcp", proxyAddr)
			},
			TLSClientConfig: &tls.Config{
				RootCAs: rootPool,
			},
		},
	}

	testCases := []struct {
		name         string
		url          string
		wantBody     string
		isCacheable  bool
		firstStatus  string
		secondStatus string
	}{
		{
			name:         "Transparent APT .deb",
			url:          "https://deb.debian.org/debian/pool/main/h/hello/hello_2.10-2_amd64.deb",
			wantBody:     "deb-binary-bytes",
			isCacheable:  true,
			firstStatus:  "miss",
			secondStatus: "hit",
		},
		{
			name:         "Transparent NPM Tarball",
			url:          "https://registry.npmjs.org/express/-/express-4.18.2.tgz",
			wantBody:     "npm-tarball-bytes",
			isCacheable:  true,
			firstStatus:  "miss",
			secondStatus: "hit",
		},
		{
			name:         "Transparent NPM Packument",
			url:          "https://registry.npmjs.org/express",
			wantBody:     `{"name":"express"}`,
			isCacheable:  false,
			firstStatus:  "pass",
			secondStatus: "pass",
		},
		{
			name:         "Transparent NPM Packument Playwright",
			url:          "https://registry.npmjs.org/playwright",
			wantBody:     `{"name":"playwright"}`,
			isCacheable:  false,
			firstStatus:  "pass",
			secondStatus: "pass",
		},
		{
			name:         "Transparent PyPI Wheel",
			url:          "https://files.pythonhosted.org/packages/pkg-1.0.whl",
			wantBody:     "pypi-wheel-bytes",
			isCacheable:  true,
			firstStatus:  "miss",
			secondStatus: "hit",
		},
		{
			name:         "Transparent Go Module Zip",
			url:          "https://proxy.golang.org/github.com/gin-gonic/gin/@v/v1.9.1.zip",
			wantBody:     "go-module-zip-bytes",
			isCacheable:  true,
			firstStatus:  "miss",
			secondStatus: "hit",
		},
		{
			name:         "Transparent Go SumDB Tile",
			url:          "https://sum.golang.org/tile/8/0/000.p/1",
			wantBody:     "sumdb-tile-bytes",
			isCacheable:  true,
			firstStatus:  "miss",
			secondStatus: "hit",
		},
		{
			name:         "Transparent Go SumDB Supported",
			url:          "https://sum.golang.org/supported",
			wantBody:     "",
			isCacheable:  false,
			firstStatus:  "pass",
			secondStatus: "pass",
		},
		{
			name:         "Transparent Docker Hub Blob",
			url:          "https://registry-1.docker.io/v2/library/ubuntu/blobs/" + digestOf(transparentLayer("docker.io")),
			wantBody:     transparentLayer("docker.io"),
			isCacheable:  true,
			firstStatus:  "miss",
			secondStatus: "hit",
		},
		{
			name:         "Transparent GHCR Blob",
			url:          "https://ghcr.io/v2/bwsalmon/grain/blobs/" + digestOf(transparentLayer("ghcr.io")),
			wantBody:     transparentLayer("ghcr.io"),
			isCacheable:  true,
			firstStatus:  "miss",
			secondStatus: "hit",
		},
		{
			name:         "Transparent GCR Blob",
			url:          "https://gcr.io/v2/distroless/static/blobs/" + digestOf(transparentLayer("gcr.io")),
			wantBody:     transparentLayer("gcr.io"),
			isCacheable:  true,
			firstStatus:  "miss",
			secondStatus: "hit",
		},
		{
			name:         "Transparent Alpine APK",
			url:          "https://dl-cdn.alpinelinux.org/alpine/v3.19/main/x86_64/curl-8.5.0-r0.apk",
			wantBody:     "apk-binary-bytes",
			isCacheable:  true,
			firstStatus:  "miss",
			secondStatus: "hit",
		},
		{
			name:         "Transparent Tailscale APT",
			url:          "https://pkgs.tailscale.com/stable/debian/pool/tailscale_1.56.1_amd64.deb",
			wantBody:     "deb-binary-bytes",
			isCacheable:  true,
			firstStatus:  "miss",
			secondStatus: "hit",
		},
		{
			name:         "Transparent Yarn NPM Tarball",
			url:          "https://registry.yarnpkg.com/lodash/-/lodash-4.17.21.tgz",
			wantBody:     "npm-tarball-bytes",
			isCacheable:  true,
			firstStatus:  "miss",
			secondStatus: "hit",
		},
		{
			name:         "Transparent Quay Blob",
			url:          "https://quay.io/v2/coreos/etcd/blobs/" + digestOf(transparentLayer("quay.io")),
			wantBody:     transparentLayer("quay.io"),
			isCacheable:  true,
			firstStatus:  "miss",
			secondStatus: "hit",
		},
		{
			name:         "Transparent Kubernetes Registry Blob",
			url:          "https://registry.k8s.io/v2/kube-apiserver/blobs/" + digestOf(transparentLayer("registry.k8s.io")),
			wantBody:     transparentLayer("registry.k8s.io"),
			isCacheable:  true,
			firstStatus:  "miss",
			secondStatus: "hit",
		},
		{
			name:         "Transparent Google Artifact Registry Blob",
			url:          "https://us-docker.pkg.dev/v2/proj/repo/img/blobs/" + digestOf(transparentLayer("pkg.dev")),
			wantBody:     transparentLayer("pkg.dev"),
			isCacheable:  true,
			firstStatus:  "miss",
			secondStatus: "hit",
		},
		{
			name:         "Transparent Docker Hub Referrers",
			url:          "https://registry-1.docker.io/v2/library/debian/referrers/sha256:1111111111111111111111111111111111111111111111111111111111111111",
			wantBody:     `{"schemaVersion":2,"manifests":[]}`,
			isCacheable:  false,
			firstStatus:  "pass",
			secondStatus: "pass",
		},
		{
			name:         "Transparent Docker Hub Tags List",
			url:          "https://registry-1.docker.io/v2/library/debian/tags/list",
			wantBody:     `{"name":"test","tags":["v1.0.0"]}`,
			isCacheable:  false,
			firstStatus:  "pass",
			secondStatus: "pass",
		},
		{
			name:         "Transparent GHCR Referrers",
			url:          "https://ghcr.io/v2/bwsalmon/grain/referrers/sha256:2222222222222222222222222222222222222222222222222222222222222222",
			wantBody:     `{"schemaVersion":2,"manifests":[]}`,
			isCacheable:  false,
			firstStatus:  "pass",
			secondStatus: "pass",
		},
		{
			name:         "Transparent GHCR Tags List",
			url:          "https://ghcr.io/v2/bwsalmon/grain/tags/list",
			wantBody:     `{"name":"test","tags":["v1.0.0"]}`,
			isCacheable:  false,
			firstStatus:  "pass",
			secondStatus: "pass",
		},
		{
			name:         "Transparent GitHub Release Asset",
			url:          "https://github.com/owner/repo/releases/download/v1.0.0/asset.tar.gz",
			wantBody:     "ok",
			isCacheable:  true,
			firstStatus:  "miss",
			secondStatus: "hit",
		},
		{
			name:         "Transparent GitHub Tag Archive",
			url:          "https://github.com/owner/repo/archive/refs/tags/v1.0.0.tar.gz",
			wantBody:     "ok",
			isCacheable:  true,
			firstStatus:  "miss",
			secondStatus: "hit",
		},
		{
			name:         "Transparent GitHub Branch Archive (Pass-through)",
			url:          "https://github.com/owner/repo/archive/refs/heads/main.tar.gz",
			wantBody:     "ok",
			isCacheable:  false,
			firstStatus:  "pass",
			secondStatus: "pass",
		},
		{
			name:         "Transparent GitHub Raw Commit File",
			url:          "https://raw.githubusercontent.com/owner/repo/1111222233334444555566667777888899990000/README.md",
			wantBody:     "ok",
			isCacheable:  true,
			firstStatus:  "miss",
			secondStatus: "hit",
		},
		{
			name:         "Transparent Playwright Browser Zip",
			url:          "https://cdn.playwright.dev/builds/chromium/1080/chromium-linux.zip",
			wantBody:     "playwright-build-bytes",
			isCacheable:  true,
			firstStatus:  "miss",
			secondStatus: "hit",
		},
		{
			name:         "Transparent Playwright AzureEdge Zip",
			url:          "https://playwright.azureedge.net/builds/cft/120.0.6099.109/chromium-headless-shell-linux.zip",
			wantBody:     "playwright-build-bytes",
			isCacheable:  true,
			firstStatus:  "miss",
			secondStatus: "hit",
		},
		{
			name:         "Transparent Playwright Non-Build (Pass-through)",
			url:          "https://cdn.playwright.dev/other/index.html",
			wantBody:     "ok",
			isCacheable:  false,
			firstStatus:  "pass",
			secondStatus: "pass",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// First request -> should be miss or pass
			resp, err := client.Get(tc.url)
			if err != nil {
				t.Fatalf("client.Get (1): %v", err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()

			if string(body) != tc.wantBody {
				t.Errorf("body (1) = %q, want %q", string(body), tc.wantBody)
			}
			if status := resp.Header.Get(cacheStatusHeader); status != tc.firstStatus {
				t.Errorf("cacheStatus (1) = %q, want %q", status, tc.firstStatus)
			}

			// Second request -> should be hit (if cacheable) or pass
			resp2, err := client.Get(tc.url)
			if err != nil {
				t.Fatalf("client.Get (2): %v", err)
			}
			body2, _ := io.ReadAll(resp2.Body)
			resp2.Body.Close()

			if string(body2) != tc.wantBody {
				t.Errorf("body (2) = %q, want %q", string(body2), tc.wantBody)
			}
			if status := resp2.Header.Get(cacheStatusHeader); status != tc.secondStatus {
				t.Errorf("cacheStatus (2) = %q, want %q", status, tc.secondStatus)
			}
		})
	}
}

func TestTransparentDisallowedHost(t *testing.T) {
	ca, err := NewCA()
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}

	srv, _ := newTestServer(t, Config{})
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

	resp, err := client.Get("https://unapproved.attacker.com/malicious.deb")
	if err != nil {
		t.Fatalf("client.Get: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403 or 404", resp.StatusCode)
	}
}

func TestSandboxCAAndResolvConf(t *testing.T) {
	ca, err := NewCA()
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}

	files := SandboxCAFiles(ca.RootCertPEM())
	if len(files) != 1 {
		t.Fatalf("got %d CA files, want 1", len(files))
	}
	if files[0].Path != CACertPath {
		t.Errorf("path = %q, want %q", files[0].Path, CACertPath)
	}
	if !strings.Contains(files[0].Content, "BEGIN CERTIFICATE") {
		t.Errorf("expected PEM certificate content, got %s", files[0].Content)
	}

	resolv := SandboxResolvConf("172.17.0.1")
	if resolv.Path != ResolvConfPath {
		t.Errorf("path = %q, want %q", resolv.Path, ResolvConfPath)
	}
	if !strings.Contains(resolv.Content, "nameserver 172.17.0.1") {
		t.Errorf("resolv content = %q", resolv.Content)
	}
}

func TestTransparentDockerPushE2E(t *testing.T) {
	ca, err := NewCA()
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}

	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			if r.URL.Query().Get("service") != "ghcr.io" || r.URL.Query().Get("scope") != "repository:bwsalmon/grain:pull,push" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"token":"push-token-12345"}`))
			return
		}

		// Verify authorization header when supplied
		auth := r.Header.Get("Authorization")
		if auth == "" {
			w.Header().Set("Www-Authenticate", `Bearer realm="https://ghcr.io/token",service="ghcr.io",scope="repository:bwsalmon/grain:pull,push"`)
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"errors":[{"code":"UNAUTHORIZED","message":"authentication required"}]}`))
			return
		}
		if auth != "Bearer push-token-12345" {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"errors":[{"code":"DENIED","message":"access denied"}]}`))
			return
		}

		switch {
		case r.Method == http.MethodHead && r.URL.Path == "/v2/bwsalmon/grain/blobs/sha256:exists":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodHead && r.URL.Path == "/v2/bwsalmon/grain/blobs/sha256:missing":
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/v2/bwsalmon/grain/blobs/uploads/":
			if mount := r.URL.Query().Get("mount"); mount == "sha256:mounted" {
				w.Header().Set("Location", "/v2/bwsalmon/grain/blobs/sha256:mounted")
				w.WriteHeader(http.StatusCreated)
				return
			}
			w.Header().Set("Location", "/v2/bwsalmon/grain/blobs/uploads/uuid-1234")
			w.Header().Set("Docker-Upload-UUID", "uuid-1234")
			w.Header().Set("Range", "0-0")
			w.WriteHeader(http.StatusAccepted)
		case r.Method == http.MethodPatch && r.URL.Path == "/v2/bwsalmon/grain/blobs/uploads/uuid-1234":
			body, _ := io.ReadAll(r.Body)
			if string(body) != "chunk-data" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.Header().Set("Location", "/v2/bwsalmon/grain/blobs/uploads/uuid-1234")
			w.Header().Set("Docker-Upload-UUID", "uuid-1234")
			w.Header().Set("Range", "0-10")
			w.WriteHeader(http.StatusAccepted)
		case r.Method == http.MethodPut && r.URL.Path == "/v2/bwsalmon/grain/blobs/uploads/uuid-1234":
			if r.URL.Query().Get("digest") != "sha256:chunkdigest" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.Header().Set("Location", "/v2/bwsalmon/grain/blobs/sha256:chunkdigest")
			w.Header().Set("Docker-Content-Digest", "sha256:chunkdigest")
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPut && r.URL.Path == "/v2/bwsalmon/grain/manifests/v1.0.0":
			if ct := r.Header.Get("Content-Type"); !strings.Contains(ct, "manifest") {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"schemaVersion": 2`) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.Header().Set("Location", "/v2/bwsalmon/grain/manifests/v1.0.0")
			w.Header().Set("Docker-Content-Digest", "sha256:manifestdigest")
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodDelete && r.URL.Path == "/v2/bwsalmon/grain/blobs/uploads/uuid-cancel":
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
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

	proxyAddr := proxyServer.Listener.Addr().String()

	rootPool := x509.NewCertPool()
	rootPool.AppendCertsFromPEM(ca.RootCertPEM())

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "tcp", proxyAddr)
			},
			TLSClientConfig: &tls.Config{
				RootCAs: rootPool,
			},
		},
	}

	// 1. Unauthenticated push request -> 401 Unauthorized with Www-Authenticate challenge
	reqNoAuth, _ := http.NewRequest(http.MethodPost, "https://ghcr.io/v2/bwsalmon/grain/blobs/uploads/", nil)
	respNoAuth, err := client.Do(reqNoAuth)
	if err != nil {
		t.Fatalf("unauthenticated push request: %v", err)
	}
	respNoAuth.Body.Close()
	if respNoAuth.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated status = %d, want 401", respNoAuth.StatusCode)
	}
	challenge := respNoAuth.Header.Get("Www-Authenticate")
	if !strings.Contains(challenge, "realm=") {
		t.Errorf("Www-Authenticate = %q, want realm", challenge)
	}
	if got := respNoAuth.Header.Get(cacheStatusHeader); got != "pass" {
		t.Errorf("cache status = %q, want pass", got)
	}

	// 1b. Fetch bearer token from auth challenge endpoint -> 200 OK
	reqToken, _ := http.NewRequest(http.MethodGet, "https://ghcr.io/token?service=ghcr.io&scope=repository:bwsalmon/grain:pull,push", nil)
	reqToken.Header.Set("Authorization", "Basic dGVzdDp0b2tlbg==")
	respToken, err := client.Do(reqToken)
	if err != nil {
		t.Fatalf("fetch auth token: %v", err)
	}
	tokenBody, _ := io.ReadAll(respToken.Body)
	respToken.Body.Close()
	if respToken.StatusCode != http.StatusOK {
		t.Fatalf("token status = %d, want 200 OK", respToken.StatusCode)
	}
	if string(tokenBody) != `{"token":"push-token-12345"}` {
		t.Errorf("token body = %q, want %q", string(tokenBody), `{"token":"push-token-12345"}`)
	}
	if got := respToken.Header.Get(cacheStatusHeader); got != "pass" {
		t.Errorf("token cache status = %q, want pass", got)
	}

	// 2. Check blob existence: HEAD existing blob -> 200 OK
	reqHeadExists, _ := http.NewRequest(http.MethodHead, "https://ghcr.io/v2/bwsalmon/grain/blobs/sha256:exists", nil)
	reqHeadExists.Header.Set("Authorization", "Bearer push-token-12345")
	respHeadExists, err := client.Do(reqHeadExists)
	if err != nil {
		t.Fatalf("head exists: %v", err)
	}
	respHeadExists.Body.Close()
	if respHeadExists.StatusCode != http.StatusOK {
		t.Errorf("head exists status = %d, want 200", respHeadExists.StatusCode)
	}

	// 3. Check blob existence: HEAD missing blob -> 404 Not Found
	reqHeadMissing, _ := http.NewRequest(http.MethodHead, "https://ghcr.io/v2/bwsalmon/grain/blobs/sha256:missing", nil)
	reqHeadMissing.Header.Set("Authorization", "Bearer push-token-12345")
	respHeadMissing, err := client.Do(reqHeadMissing)
	if err != nil {
		t.Fatalf("head missing: %v", err)
	}
	respHeadMissing.Body.Close()
	if respHeadMissing.StatusCode != http.StatusNotFound {
		t.Errorf("head missing status = %d, want 404", respHeadMissing.StatusCode)
	}

	// 4. Initiate blob upload: POST /v2/bwsalmon/grain/blobs/uploads/ -> 202 Accepted
	reqInit, _ := http.NewRequest(http.MethodPost, "https://ghcr.io/v2/bwsalmon/grain/blobs/uploads/", nil)
	reqInit.Header.Set("Authorization", "Bearer push-token-12345")
	respInit, err := client.Do(reqInit)
	if err != nil {
		t.Fatalf("initiate blob upload: %v", err)
	}
	respInit.Body.Close()
	if respInit.StatusCode != http.StatusAccepted {
		t.Errorf("initiate status = %d, want 202", respInit.StatusCode)
	}
	if loc := respInit.Header.Get("Location"); loc != "/v2/bwsalmon/grain/blobs/uploads/uuid-1234" {
		t.Errorf("Location = %q, want /v2/bwsalmon/grain/blobs/uploads/uuid-1234", loc)
	}
	if uuid := respInit.Header.Get("Docker-Upload-UUID"); uuid != "uuid-1234" {
		t.Errorf("Docker-Upload-UUID = %q, want uuid-1234", uuid)
	}

	// 5. Upload chunk: PATCH /v2/bwsalmon/grain/blobs/uploads/uuid-1234 -> 202 Accepted
	reqChunk, _ := http.NewRequest(http.MethodPatch, "https://ghcr.io/v2/bwsalmon/grain/blobs/uploads/uuid-1234", strings.NewReader("chunk-data"))
	reqChunk.Header.Set("Authorization", "Bearer push-token-12345")
	reqChunk.Header.Set("Content-Type", "application/octet-stream")
	respChunk, err := client.Do(reqChunk)
	if err != nil {
		t.Fatalf("upload chunk: %v", err)
	}
	respChunk.Body.Close()
	if respChunk.StatusCode != http.StatusAccepted {
		t.Errorf("chunk status = %d, want 202", respChunk.StatusCode)
	}

	// 6. Complete blob upload: PUT /v2/bwsalmon/grain/blobs/uploads/uuid-1234?digest=sha256:chunkdigest -> 201 Created
	reqFinish, _ := http.NewRequest(http.MethodPut, "https://ghcr.io/v2/bwsalmon/grain/blobs/uploads/uuid-1234?digest=sha256:chunkdigest", nil)
	reqFinish.Header.Set("Authorization", "Bearer push-token-12345")
	respFinish, err := client.Do(reqFinish)
	if err != nil {
		t.Fatalf("finish upload: %v", err)
	}
	respFinish.Body.Close()
	if respFinish.StatusCode != http.StatusCreated {
		t.Errorf("finish status = %d, want 201", respFinish.StatusCode)
	}
	if dig := respFinish.Header.Get("Docker-Content-Digest"); dig != "sha256:chunkdigest" {
		t.Errorf("Docker-Content-Digest = %q, want sha256:chunkdigest", dig)
	}

	// 7. Upload manifest: PUT /v2/bwsalmon/grain/manifests/v1.0.0 -> 201 Created
	manifestJSON := `{"schemaVersion": 2, "mediaType": "application/vnd.docker.distribution.manifest.v2+json", "config": {}, "layers": []}`
	reqManifest, _ := http.NewRequest(http.MethodPut, "https://ghcr.io/v2/bwsalmon/grain/manifests/v1.0.0", strings.NewReader(manifestJSON))
	reqManifest.Header.Set("Authorization", "Bearer push-token-12345")
	reqManifest.Header.Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
	respManifest, err := client.Do(reqManifest)
	if err != nil {
		t.Fatalf("upload manifest: %v", err)
	}
	respManifest.Body.Close()
	if respManifest.StatusCode != http.StatusCreated {
		t.Errorf("manifest status = %d, want 201", respManifest.StatusCode)
	}
	if dig := respManifest.Header.Get("Docker-Content-Digest"); dig != "sha256:manifestdigest" {
		t.Errorf("manifest digest = %q, want sha256:manifestdigest", dig)
	}

	// 8. Cross-repo blob mount: POST /v2/bwsalmon/grain/blobs/uploads/?mount=sha256:mounted&from=other/repo -> 201 Created
	reqMount, _ := http.NewRequest(http.MethodPost, "https://ghcr.io/v2/bwsalmon/grain/blobs/uploads/?mount=sha256:mounted&from=other/repo", nil)
	reqMount.Header.Set("Authorization", "Bearer push-token-12345")
	respMount, err := client.Do(reqMount)
	if err != nil {
		t.Fatalf("cross-repo mount: %v", err)
	}
	respMount.Body.Close()
	if respMount.StatusCode != http.StatusCreated {
		t.Errorf("mount status = %d, want 201", respMount.StatusCode)
	}

	// 9. Upload cancellation: DELETE /v2/bwsalmon/grain/blobs/uploads/uuid-cancel -> 204 No Content
	reqDelete, _ := http.NewRequest(http.MethodDelete, "https://ghcr.io/v2/bwsalmon/grain/blobs/uploads/uuid-cancel", nil)
	reqDelete.Header.Set("Authorization", "Bearer push-token-12345")
	respDelete, err := client.Do(reqDelete)
	if err != nil {
		t.Fatalf("delete upload: %v", err)
	}
	respDelete.Body.Close()
	if respDelete.StatusCode != http.StatusNoContent {
		t.Errorf("delete status = %d, want 204", respDelete.StatusCode)
	}

	// 10. Non-OCI write attempt: POST https://registry.npmjs.org/mypkg -> 405 Method Not Allowed
	reqNPM, _ := http.NewRequest(http.MethodPost, "https://registry.npmjs.org/mypkg", strings.NewReader("bad"))
	respNPM, err := client.Do(reqNPM)
	if err != nil {
		t.Fatalf("post npm: %v", err)
	}
	respNPM.Body.Close()
	if respNPM.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("npm post status = %d, want 405 Method Not Allowed", respNPM.StatusCode)
	}
}

func TestTransparentConcurrentLoad(t *testing.T) {
	ca, err := NewCA()
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}

	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ".deb"):
			w.Header().Set("Content-Type", "application/vnd.debian.binary-package")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("deb-binary-bytes"))
		case strings.HasSuffix(r.URL.Path, ".zip"):
			w.Header().Set("Content-Type", "application/zip")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("go-module-zip-bytes"))
		case strings.Contains(r.URL.Path, "/blobs/sha256:"):
			w.Header().Set("Content-Type", "application/octet-stream")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(transparentLayers[path.Base(r.URL.Path)]))
		case strings.Contains(r.URL.Path, "/referrers/"):
			w.Header().Set("Content-Type", "application/vnd.oci.image.index.v1+json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"schemaVersion":2,"manifests":[]}`))
		case strings.HasSuffix(r.URL.Path, "/tags/list"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"name":"grain","tags":["v1.0.0"]}`))
		default:
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("ok"))
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
		AptScheme:      "http",
		GoProxy:        "http://proxy.golang.org",
		DockerRegistry: "http://registry-1.docker.io",
		GHCRRegistry:   "http://ghcr.io",
	})

	proxyServer := httptest.NewUnstartedServer(srv)
	proxyServer.TLS = ca.TLSConfig()
	proxyServer.StartTLS()
	defer proxyServer.Close()

	proxyAddr := proxyServer.Listener.Addr().String()

	rootPool := x509.NewCertPool()
	rootPool.AppendCertsFromPEM(ca.RootCertPEM())

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "tcp", proxyAddr)
			},
			TLSClientConfig: &tls.Config{
				RootCAs: rootPool,
			},
		},
	}

	endpoints := []string{
		"https://deb.debian.org/debian/pool/main/h/hello/hello_2.10-2_amd64.deb",
		"https://proxy.golang.org/github.com/gin-gonic/gin/@v/v1.9.1.zip",
		"https://registry-1.docker.io/v2/library/ubuntu/blobs/" + digestOf(transparentLayer("docker.io")),
		"https://ghcr.io/v2/bwsalmon/grain/referrers/sha256:2222222222222222222222222222222222222222222222222222222222222222",
		"https://ghcr.io/v2/bwsalmon/grain/tags/list",
	}

	const workers = 15
	const requestsPerWorker = 10
	var wg sync.WaitGroup
	errCh := make(chan error, workers*requestsPerWorker)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for r := 0; r < requestsPerWorker; r++ {
				target := endpoints[(workerID+r)%len(endpoints)]
				resp, err := client.Get(target)
				if err != nil {
					errCh <- err
					continue
				}
				_, _ = io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					errCh <- fmt.Errorf("worker %d req %d: unexpected status %d", workerID, r, resp.StatusCode)
					continue
				}
				errCh <- nil
			}
		}(w)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Errorf("concurrent request error: %v", err)
		}
	}
}
