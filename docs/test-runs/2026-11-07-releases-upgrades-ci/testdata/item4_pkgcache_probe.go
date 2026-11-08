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

//go:build ignore

// item4_pkgcache_probe drives a real pkg/pkgcache server over TLS (its own CA
// minting leaf certificates for the intercepted registry host, as a sandbox
// sees it) through a multi-chunk Artifact Registry blob push (#811):
//
//	POST /v2/<repo>/blobs/uploads/            -> 202 Location: https://<host>.pkg.dev/artifacts-uploads/...
//	PATCH /artifacts-uploads/... (x4 chunks)  -> 202 Location: (same session)
//	PUT   /artifacts-uploads/...?digest=...   -> 201 Docker-Content-Digest
//
// and checks the auth boundaries around it: the client's own registry
// Authorization reaches the *.pkg.dev upstream, grain's internal headers
// (Grit-Job-Token, Grit-Cache-Trust, Connection-named hop headers) and the
// daemon's GitHub package credential never do; an upload session Location
// naming another host (or a pkg.dev look-alike) is refused rather than
// proxied; a sandbox with no network (restricted client) cannot start or
// continue an upload; and dot-dot traversal never reaches the upstream.
//
//	go run docs/test-runs/2026-11-07-releases-upgrades-ci/testdata/item4_pkgcache_probe.go
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"

	"github.com/ai-on-gke/grain/pkg/pkgcache"
)

const (
	registryHost = "us-central1-docker.pkg.dev"
	repoPath     = "/v2/acme-proj/containers/widgets/blobs/uploads/"
	sessionPath  = "/artifacts-uploads/namespaces/acme-proj/repositories/containers/uploads/sess-811"
	chunkSize    = 256 << 10
	chunks       = 4
)

type daemonCreds struct{}

// PackageToken is the daemon's GitHub package credential: it must never be
// sent to a non-GHCR host.
func (daemonCreds) PackageToken(owner, repo string) (string, bool) { return "ghp_DAEMON_SECRET", true }

type upstream struct {
	mu       sync.Mutex
	received []byte
	final    string
	leaks    []string
	hosts    map[string]int
	requests int
}

func (u *upstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.requests++
	if u.hosts == nil {
		u.hosts = map[string]int{}
	}
	u.hosts[r.Host]++
	for _, h := range []string{"Grit-Job-Token", "Grit-Cache-Trust", "X-Hop-Secret"} {
		if v := r.Header.Get(h); v != "" {
			u.leaks = append(u.leaks, h+"="+v)
		}
	}
	if a := r.Header.Get("Authorization"); strings.Contains(a, "ghp_DAEMON_SECRET") || strings.HasPrefix(a, "Basic ") {
		u.leaks = append(u.leaks, "Authorization="+a)
	}
	if strings.Contains(r.URL.Path, "..") || r.URL.Path == "/v1/secret" {
		u.leaks = append(u.leaks, "traversal reached "+r.URL.Path)
	}
	if r.Header.Get("Authorization") != "Bearer ar-push-token" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	loc := "https://" + registryHost + sessionPath
	switch {
	case r.Method == http.MethodPost && r.URL.Path == repoPath:
		w.Header().Set("Location", loc)
		w.WriteHeader(http.StatusAccepted)
	case r.Method == http.MethodPatch && r.URL.Path == sessionPath:
		b, _ := io.ReadAll(r.Body)
		u.received = append(u.received, b...)
		w.Header().Set("Location", loc)
		w.Header().Set("Range", fmt.Sprintf("0-%d", len(u.received)-1))
		w.WriteHeader(http.StatusAccepted)
	case r.Method == http.MethodPut && r.URL.Path == sessionPath:
		b, _ := io.ReadAll(r.Body)
		u.received = append(u.received, b...)
		sum := sha256.Sum256(u.received)
		got := "sha256:" + hex.EncodeToString(sum[:])
		if r.URL.Query().Get("digest") != got {
			http.Error(w, "digest mismatch", http.StatusBadRequest)
			return
		}
		u.final = got
		w.Header().Set("Docker-Content-Digest", got)
		w.WriteHeader(http.StatusCreated)
	default:
		http.NotFound(w, r)
	}
}

var failures int

func check(ok bool, format string, args ...any) {
	if ok {
		fmt.Printf("PASS "+format+"\n", args...)
		return
	}
	failures++
	fmt.Printf("FAIL "+format+"\n", args...)
}

func main() {
	up := &upstream{}
	upSrv := httptest.NewServer(up)
	defer upSrv.Close()
	upClient := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", upSrv.Listener.Addr().String())
		},
	}}

	cacheDir, err := os.MkdirTemp("", "pkgcache-811-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(cacheDir)
	cache, err := pkgcache.NewCache(cacheDir, 0)
	if err != nil {
		panic(err)
	}
	restricted := &pkgcache.ClientSet{}
	srv := pkgcache.NewServer(pkgcache.Config{
		Cache:          cache,
		Credentials:    daemonCreds{},
		Restricted:     restricted,
		Client:         upClient,
		DockerRegistry: "http://registry-1.docker.io",
		GHCRRegistry:   "http://ghcr.io",
		Log:            func(string, ...any) {},
	})
	ca, err := pkgcache.NewCA()
	if err != nil {
		panic(err)
	}
	proxy := httptest.NewUnstartedServer(srv)
	proxy.TLS = ca.TLSConfig()
	proxy.StartTLS()
	defer proxy.Close()

	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(ca.RootCertPEM())
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "tcp", proxy.Listener.Addr().String())
			},
			TLSClientConfig: &tls.Config{RootCAs: roots},
		},
	}
	do := func(method, rawURL string, body []byte) *http.Response {
		req, err := http.NewRequest(method, rawURL, bytes.NewReader(body))
		if err != nil {
			panic(err)
		}
		req.Header.Set("Authorization", "Bearer ar-push-token")
		req.Header.Set("Grit-Job-Token", "internal-job-secret")
		req.Header.Set("Grit-Cache-Trust", "trusted")
		req.Header.Set("Connection", "X-Hop-Secret")
		req.Header.Set("X-Hop-Secret", "hop-secret")
		resp, err := client.Do(req)
		if err != nil {
			panic(fmt.Sprintf("%s %s: %v", method, rawURL, err))
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp
	}

	blob := make([]byte, chunkSize*chunks+123)
	for i := range blob {
		blob[i] = byte(i*31 + i/7)
	}
	sum := sha256.Sum256(blob)
	digest := "sha256:" + hex.EncodeToString(sum[:])

	fmt.Println("=== 4B.1 multi-chunk Artifact Registry push through pkgcache (#811) ===")
	resp := do(http.MethodPost, "https://"+registryHost+repoPath, nil)
	loc := resp.Header.Get("Location")
	check(resp.StatusCode == http.StatusAccepted && strings.Contains(loc, "/artifacts-uploads/"),
		"POST blobs/uploads/ -> %d Location %s", resp.StatusCode, loc)
	for i := 0; i < chunks; i++ {
		resp = do(http.MethodPatch, loc, blob[i*chunkSize:(i+1)*chunkSize])
		check(resp.StatusCode == http.StatusAccepted && resp.Header.Get("X-Grain-Cache") != "hit",
			"PATCH chunk %d/%d (%d bytes) -> %d Range %s", i+1, chunks, chunkSize, resp.StatusCode, resp.Header.Get("Range"))
		loc = resp.Header.Get("Location")
	}
	resp = do(http.MethodPut, loc+"?digest="+url.QueryEscape(digest), blob[chunks*chunkSize:])
	check(resp.StatusCode == http.StatusCreated && resp.Header.Get("Docker-Content-Digest") == digest,
		"PUT final chunk ?digest -> %d %s", resp.StatusCode, resp.Header.Get("Docker-Content-Digest"))
	check(up.final == digest && len(up.received) == len(blob),
		"upstream reassembled %d/%d bytes with digest %s", len(up.received), len(blob), up.final)

	fmt.Println("=== 4B.2 auth boundary: what reached the *.pkg.dev upstream ===")
	check(len(up.leaks) == 0, "no internal header, daemon GitHub credential, or traversal reached upstream (leaks=%v)", up.leaks)
	check(len(up.hosts) == 1 && up.hosts[registryHost] > 0, "every upstream request was for %s (hosts=%v)", registryHost, up.hosts)

	fmt.Println("=== 4B.3 upload session Locations on other hosts are refused, not proxied ===")
	before := up.requests
	for _, host := range []string{"ghcr.io", "registry-1.docker.io", "pkg.dev", "evilpkg.dev", "us-central1-docker.pkg.dev.evil.example", "attacker.example"} {
		r := do(http.MethodPatch, "https://"+host+sessionPath, []byte("chunk"))
		// Registry hosts pkgcache intercepts answer 404 (not a *.pkg.dev
		// upload session); hosts it does not serve at all answer 405 (no
		// write route). Either way nothing is proxied.
		check(r.StatusCode == http.StatusNotFound || r.StatusCode == http.StatusMethodNotAllowed,
			"PATCH https://%s%s -> %d (refused)", host, sessionPath, r.StatusCode)
	}
	check(up.requests == before, "no refused session request reached any upstream (%d new)", up.requests-before)

	fmt.Println("=== 4B.4 dot-dot traversal out of /artifacts-uploads/ ===")
	for _, p := range []string{"/artifacts-uploads/../v1/secret", "/artifacts-uploads/%2e%2e/v1/secret", "/artifacts-uploads/%252e%252e/v1/secret"} {
		r := do(http.MethodPut, "https://"+registryHost+p, []byte("x"))
		check(r.StatusCode == http.StatusBadRequest || r.StatusCode == http.StatusNotFound, "PUT %s -> %d", p, r.StatusCode)
	}
	check(len(up.leaks) == 0, "traversal never reached the upstream (leaks=%v)", up.leaks)

	fmt.Println("=== 4B.5 a sandbox with no network (restricted client) cannot push ===")
	restricted.RestrictClient(net.ParseIP("127.0.0.1"))
	before = up.requests
	for _, c := range []struct{ method, target string }{
		{http.MethodPost, "https://" + registryHost + repoPath},
		{http.MethodPatch, "https://" + registryHost + sessionPath},
		{http.MethodPut, "https://" + registryHost + sessionPath + "?digest=" + url.QueryEscape(digest)},
		{http.MethodGet, "https://" + registryHost + sessionPath},
	} {
		r := do(c.method, c.target, []byte("x"))
		check(r.StatusCode == http.StatusForbidden || r.StatusCode == http.StatusNotFound,
			"restricted %s %s -> %d", c.method, strings.TrimPrefix(c.target, "https://"+registryHost), r.StatusCode)
	}
	check(up.requests == before, "no restricted request reached the upstream (%d new)", up.requests-before)
	restricted.UnrestrictClient(net.ParseIP("127.0.0.1"))

	if failures > 0 {
		fmt.Printf("%d CHECK(S) FAILED\n", failures)
		os.Exit(1)
	}
	fmt.Println("ALL ITEM 4B PKGCACHE CHECKS PASSED")
}
