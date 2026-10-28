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
	"crypto/sha512"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDockerHubFallbackOn429(t *testing.T) {
	var hubHits int
	hubServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hubHits++
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"errors":[{"code":"TOOMANYREQUESTS","message":"Too Many Requests"}]}`))
	}))
	defer hubServer.Close()

	var gcrHits int
	gcrServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gcrHits++
		w.Header().Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"schemaVersion":2,"config":{"digest":"sha256:config123"}}`))
	}))
	defer gcrServer.Close()

	upstreamClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, _, _ := net.SplitHostPort(addr)
				if host == "mirror.gcr.io" || host == "gcr.io" {
					return (&net.Dialer{}).DialContext(ctx, "tcp", gcrServer.Listener.Addr().String())
				}
				return (&net.Dialer{}).DialContext(ctx, "tcp", hubServer.Listener.Addr().String())
			},
		},
	}

	s, _ := newTestServer(t, Config{
		Client:         upstreamClient,
		DockerRegistry: hubServer.URL,
	})

	rec := get(t, s, "/v2/library/debian/manifests/bookworm-slim?ns=docker.io")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	if hubHits == 0 {
		t.Error("expected Docker Hub to be queried first")
	}
	if gcrHits == 0 {
		t.Error("expected fallback to mirror.gcr.io when Docker Hub returns 429")
	}
}

func TestDockerHubFallbackOn5xx(t *testing.T) {
	hubServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`service unavailable`))
	}))
	defer hubServer.Close()

	gcrServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`blob-content-from-gcr`))
	}))
	defer gcrServer.Close()

	upstreamClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, _, _ := net.SplitHostPort(addr)
				if host == "mirror.gcr.io" || host == "gcr.io" {
					return (&net.Dialer{}).DialContext(ctx, "tcp", gcrServer.Listener.Addr().String())
				}
				return (&net.Dialer{}).DialContext(ctx, "tcp", hubServer.Listener.Addr().String())
			},
		},
	}

	s, _ := newTestServer(t, Config{
		Client:         upstreamClient,
		DockerRegistry: hubServer.URL,
	})

	blob := "/v2/library/debian/blobs/" + digestOf("blob-content-from-gcr")
	rec := get(t, s, blob)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "blob-content-from-gcr" {
		t.Fatalf("expected body %q, got %q", "blob-content-from-gcr", rec.Body.String())
	}
	if got := get(t, s, blob).Header().Get(cacheStatusHeader); got != "hit" {
		t.Errorf("second fetch cache status = %q, want hit -- the mirror's bytes match their digest", got)
	}
}

func TestDockerManifestWithDigestIsCached(t *testing.T) {
	var upstreamHits int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		w.Header().Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"schemaVersion":2,"mediaType":"manifest"}`))
	}))
	defer upstream.Close()

	s, cache := newTestServer(t, Config{
		Client:         upstream.Client(),
		DockerRegistry: upstream.URL,
	})

	digestPath := "/v2/library/debian/manifests/" + digestOf(`{"schemaVersion":2,"mediaType":"manifest"}`)

	// 1. First fetch -> miss
	rec1 := get(t, s, digestPath)
	if rec1.Code != http.StatusOK {
		t.Fatalf("fetch 1 code = %d, want 200", rec1.Code)
	}
	if got := rec1.Header().Get(cacheStatusHeader); got != "miss" {
		t.Errorf("fetch 1 status = %q, want miss", got)
	}
	if upstreamHits != 1 {
		t.Errorf("upstream hits = %d, want 1", upstreamHits)
	}

	// 2. Second fetch -> hit
	rec2 := get(t, s, digestPath)
	if rec2.Code != http.StatusOK {
		t.Fatalf("fetch 2 code = %d, want 200", rec2.Code)
	}
	if got := rec2.Header().Get(cacheStatusHeader); got != "hit" {
		t.Errorf("fetch 2 status = %q, want hit", got)
	}
	if upstreamHits != 1 {
		t.Errorf("upstream hits = %d, want 1 (should be served from disk)", upstreamHits)
	}

	// 3. HEAD request -> served from cache without upstream hit
	headReq, err := http.NewRequest(http.MethodHead, digestPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	headRec := httptest.NewRecorder()
	s.ServeHTTP(headRec, headReq)
	if headRec.Code != http.StatusOK {
		t.Fatalf("HEAD code = %d, want 200", headRec.Code)
	}
	if got := headRec.Header().Get(cacheStatusHeader); got != "hit" {
		t.Errorf("HEAD cache status = %q, want hit", got)
	}
	if upstreamHits != 1 {
		t.Errorf("HEAD upstream hits = %d, want 1", upstreamHits)
	}
	if cache.Bytes() == 0 {
		t.Error("expected cache to hold bytes for digest manifest")
	}
}

func TestDockerDirectTransparentFallbackOn429(t *testing.T) {
	ca, err := NewCA()
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}

	var hubHits int
	hubServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hubHits++
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`rate limited`))
	}))
	defer hubServer.Close()

	var gcrHits int
	gcrServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gcrHits++
		w.Header().Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`manifest-from-gcr`))
	}))
	defer gcrServer.Close()

	upstreamClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, _, _ := net.SplitHostPort(addr)
				if host == "mirror.gcr.io" || host == "gcr.io" {
					return (&net.Dialer{}).DialContext(ctx, "tcp", gcrServer.Listener.Addr().String())
				}
				return (&net.Dialer{}).DialContext(ctx, "tcp", hubServer.Listener.Addr().String())
			},
		},
	}

	s, _ := newTestServer(t, Config{
		Client:         upstreamClient,
		DockerRegistry: hubServer.URL,
	})

	proxyServer := httptest.NewUnstartedServer(s)
	proxyServer.TLS = ca.TLSConfig()
	proxyServer.StartTLS()
	defer proxyServer.Close()

	rootPool := x509.NewCertPool()
	rootPool.AppendCertsFromPEM(ca.RootCertPEM())

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "tcp", proxyServer.Listener.Addr().String())
			},
			TLSClientConfig: &tls.Config{
				RootCAs: rootPool,
			},
		},
	}

	req, err := http.NewRequest(http.MethodGet, "https://registry-1.docker.io/v2/library/debian/manifests/bookworm-slim", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}
	if hubHits == 0 {
		t.Error("expected hub to be tried first")
	}
	if gcrHits == 0 {
		t.Error("expected gcr fallback on 429")
	}
}

// A fallback that cannot be reached must leave the client with the
// primary's own answer. It used to close the primary's body before trying
// the fallback, so the client got the 503 status over an empty body.
func TestDockerHubFallbackUnreachableKeepsPrimaryAnswer(t *testing.T) {
	hubServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`service unavailable`))
	}))
	defer hubServer.Close()

	upstreamClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, _, _ := net.SplitHostPort(addr)
				if host == "mirror.gcr.io" || host == "gcr.io" {
					return nil, errors.New("mirror unreachable")
				}
				return (&net.Dialer{}).DialContext(ctx, "tcp", hubServer.Listener.Addr().String())
			},
		},
	}

	s, _ := newTestServer(t, Config{
		Client:         upstreamClient,
		DockerRegistry: hubServer.URL,
	})

	rec := get(t, s, "/v2/library/debian/blobs/sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected the primary's 503, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "service unavailable" {
		t.Fatalf("expected the primary's body %q, got %q", "service unavailable", rec.Body.String())
	}
}

// A blob is filed under its digest alone, whichever registry it came from,
// so bytes that do not hash to that digest must never be stored: they
// would be served in answer to every later pull of that layer, from every
// registry, and every one of those pulls would fail its own digest check.
// The client still gets the bytes upstream sent, and checks them itself.
func TestDockerBlobWithTheWrongBytesIsNotStored(t *testing.T) {
	blob := dockerAPIPrefix + "library/demo/blobs/" + testDigest
	s, reg := dockerServer(t, map[string]string{blob: "not the layer the digest names"})

	for i := 0; i < 2; i++ {
		rec := get(t, s, blob)
		if rec.Code != http.StatusOK || rec.Body.String() != "not the layer the digest names" {
			t.Fatalf("pull %d: %d %q -- the client should still get what upstream sent", i, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get(cacheStatusHeader); got != "miss" {
			t.Errorf("pull %d cache status = %q, want miss -- bytes that do not match their digest were stored", i, got)
		}
	}
	if reg.hits[blob] != 2 {
		t.Errorf("the registry was asked for the layer %d times, want 2", reg.hits[blob])
	}
}

// The same holds for the mirror Docker Hub falls back to: a mirror
// answering with the wrong bytes must not poison the digest for Docker
// Hub, or for any other registry, once Docker Hub is back.
func TestDockerHubFallbackWithTheWrongBytesIsNotStored(t *testing.T) {
	hubUp := false
	hubServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hubUp {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(testLayer))
	}))
	defer hubServer.Close()
	gcrServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`a poisoned layer`))
	}))
	defer gcrServer.Close()

	upstreamClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, _, _ := net.SplitHostPort(addr)
				if host == "mirror.gcr.io" {
					return (&net.Dialer{}).DialContext(ctx, "tcp", gcrServer.Listener.Addr().String())
				}
				return (&net.Dialer{}).DialContext(ctx, "tcp", hubServer.Listener.Addr().String())
			},
		},
	}
	s, _ := newTestServer(t, Config{
		Client:         upstreamClient,
		DockerRegistry: hubServer.URL,
	})

	blob := "/v2/library/debian/blobs/" + testDigest
	rec := get(t, s, blob)
	if rec.Code != http.StatusOK || rec.Body.String() != "a poisoned layer" {
		t.Fatalf("fallback pull: %d %q", rec.Code, rec.Body.String())
	}

	hubUp = true
	rec = get(t, s, blob)
	if got := rec.Header().Get(cacheStatusHeader); got == "hit" {
		t.Fatalf("the mirror's wrong bytes were stored and served as a hit: %q", rec.Body.String())
	}
	if rec.Body.String() != testLayer {
		t.Errorf("pull once Docker Hub is back served %q, want the layer", rec.Body.String())
	}
	if got := get(t, s, blob).Header().Get(cacheStatusHeader); got != "hit" {
		t.Errorf("third pull cache status = %q, want hit -- the right bytes should be stored", got)
	}
}

// A sha512 digest is checked with sha512.
func TestDockerSHA512BlobIsVerifiedAndStored(t *testing.T) {
	sum := sha512.Sum512([]byte(testLayer))
	blob := dockerAPIPrefix + "library/demo/blobs/sha512:" + hex.EncodeToString(sum[:])
	s, reg := dockerServer(t, map[string]string{blob: testLayer})

	get(t, s, blob)
	if got := get(t, s, blob).Header().Get(cacheStatusHeader); got != "hit" {
		t.Errorf("second pull cache status = %q, want hit", got)
	}
	if reg.hits[blob] != 1 {
		t.Errorf("the registry was asked for the layer %d times, want 1", reg.hits[blob])
	}
}
