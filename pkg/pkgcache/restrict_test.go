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
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

// httptest.NewRequest's RemoteAddr, which is where every request below
// that is not made over a real listener comes from.
var testClientIP = net.ParseIP("192.0.2.1")

// TestRestrictedClientCannotWriteThroughTheCache is the reason
// Config.Restricted exists: a sandbox with no network reaches this
// service on purpose, so every write it forwards -- a git push to any
// repository, an image pushed to any registry -- is a way out to the
// internet with a credential the sandbox brought, which is what a
// prompt-injected run is handed. Each one is refused before upstream sees
// it, and the same request from any other client still goes through.
func TestRestrictedClientCannotWriteThroughTheCache(t *testing.T) {
	var upstreamHits int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&upstreamHits, 1)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer up.Close()
	upURL, err := url.Parse(up.URL)
	if err != nil {
		t.Fatalf("parsing %s: %v", up.URL, err)
	}
	restricted := &ClientSet{}
	s, _ := newTestServer(t, Config{
		GitHubURL:      up.URL,
		DockerRegistry: up.URL,
		GHCRRegistry:   up.URL,
		Restricted:     restricted,
		// Every OCI host this test names, including us-docker.pkg.dev,
		// has no Config field of its own to point at up (unlike
		// DockerRegistry and GHCRRegistry above): the cache resolves an
		// arbitrary registry host straight from the request (ociClient's
		// default case), so the only way to keep this test off the real
		// network is to redirect every outbound request this client
		// makes, whatever host it was addressed to.
		Client: &http.Client{Transport: roundTripTo{base: upURL}},
	})

	writes := []struct {
		name, method, target, host string
	}{
		{"git push", http.MethodPost, "/acme/widgets.git/git-receive-pack", "github.com"},
		{"git push, legacy route", http.MethodPost, "/github/acme/widgets.git/git-receive-pack", ""},
		{"git push, absolute form", http.MethodPost, "http://github.com/acme/widgets.git/git-receive-pack", ""},
		{"git push's ref advertisement", http.MethodGet, "/acme/widgets.git/info/refs?service=git-receive-pack", "github.com"},
		{"OCI blob upload", http.MethodPost, "/v2/library/widgets/blobs/uploads/", ""},
		{"OCI blob upload, transparent", http.MethodPost, "/v2/acme/widgets/blobs/uploads/", "ghcr.io"},
		{"OCI blob upload chunk", http.MethodPatch, "/v2/acme/widgets/blobs/uploads/abc", "ghcr.io"},
		{"OCI manifest push", http.MethodPut, "/v2/acme/widgets/manifests/latest", "ghcr.io"},
		{"OCI delete", http.MethodDelete, "/v2/acme/widgets/manifests/sha256:" + strings.Repeat("a", 64), "ghcr.io"},
		{"Artifact Registry push", http.MethodPut, "/v2/p/r/widgets/manifests/latest", "us-docker.pkg.dev"},
	}
	send := func(method, target, host string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, strings.NewReader("0000"))
		if host != "" {
			req.Host = host
		}
		req.Header.Set("Authorization", "Bearer attacker-token")
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		return rec
	}

	restricted.RestrictClient(testClientIP)
	for _, c := range writes {
		rec := send(c.method, c.target, c.host)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s (%s %s, Host %q) from a sandbox with no network answered %d, want 403",
				c.name, c.method, c.target, c.host, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "no network") {
			t.Errorf("%s: the refusal does not say why:\n%s", c.name, rec.Body.String())
		}
	}
	if hits := atomic.LoadInt32(&upstreamHits); hits != 0 {
		t.Fatalf("upstream saw %d requests from a sandbox with no network, want none", hits)
	}

	// A fetch is a read, and still goes through: git's fetch POSTs.
	for _, c := range []struct{ method, target, host string }{
		{http.MethodGet, "/acme/widgets.git/info/refs?service=git-upload-pack", "github.com"},
		{http.MethodPost, "/acme/widgets.git/git-upload-pack", "github.com"},
	} {
		if rec := send(c.method, c.target, c.host); rec.Code == http.StatusForbidden {
			t.Errorf("%s %s from a sandbox with no network was refused, want it forwarded: it is a git fetch:\n%s",
				c.method, c.target, rec.Body.String())
		}
	}

	// Released, the same client writes as any other sandbox does.
	restricted.UnrestrictClient(testClientIP)
	atomic.StoreInt32(&upstreamHits, 0)
	for _, c := range writes {
		if rec := send(c.method, c.target, c.host); rec.Code == http.StatusForbidden {
			firstLine, _, _ := strings.Cut(rec.Body.String(), "\n")
			t.Errorf("%s from a client with network was refused: %d %s", c.name, rec.Code, firstLine)
		}
	}
	if hits := atomic.LoadInt32(&upstreamHits); hits == 0 {
		t.Error("no write from a client with network reached upstream")
	}
}

// TestRestrictedClientCONNECTIsHeldToDownloadHosts: a tunnel is opaque,
// so for a sandbox with no network the host is the whole gate, and every
// host that takes a write -- a registry, github.com, an entire domain --
// is refused before anything is dialled. An archive still tunnels.
func TestRestrictedClientCONNECTIsHeldToDownloadHosts(t *testing.T) {
	dialled := make(chan string, 16)
	restricted := &ClientSet{}
	restricted.RestrictClient(net.ParseIP("127.0.0.1"))
	s, _ := newTestServer(t, Config{
		Restricted: restricted,
		Client: &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				dialled <- addr
				return nil, errors.New("this test's cache has no upstream")
			},
		}},
	})
	cache := httptest.NewServer(s)
	defer cache.Close()

	for _, target := range []string{
		"github.com:443",
		"api.github.com:443",
		"gist.githubusercontent.com:443",
		"ghcr.io:443",
		"registry-1.docker.io:443",
		"registry.npmjs.org:443",
		"us-docker.pkg.dev:443",
		"salsa.debian.org:443",
	} {
		resp, body := connectThrough(t, cache.URL, target)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("CONNECT %s from a sandbox with no network answered %s, want 403", target, resp.Status)
		}
		if !strings.Contains(body, "no network") {
			t.Errorf("CONNECT %s: the refusal does not say why:\n%s", target, body)
		}
	}
	select {
	case addr := <-dialled:
		t.Fatalf("a refused CONNECT still dialled %s", addr)
	default:
	}

	// Past the gate: the dial is attempted, and fails only because this
	// test has nowhere for it to go.
	for _, target := range []string{"deb.debian.org:443", "files.pythonhosted.org:443", "codeload.github.com:443"} {
		resp, body := connectThrough(t, cache.URL, target)
		if resp.StatusCode != http.StatusBadGateway {
			t.Errorf("CONNECT %s from a sandbox with no network answered %s, want the tunnel attempted (502 here):\n%s",
				target, resp.Status, body)
		}
	}

	for _, host := range s.restrictedConnectHosts() {
		if strings.HasPrefix(host, ".") {
			t.Errorf("restrictedConnectHosts holds the wildcard %s: %v", host, s.restrictedConnectHosts())
		}
		if !hostAllowed(host, s.connectHosts()) {
			t.Errorf("restrictedConnectHosts holds %s, which connectHosts does not", host)
		}
	}
}

// TestGitHubHostsAreTheDownloadHosts: the GitHub hosts this cache answers
// for, and so tunnels to, are the ones it downloads from -- not
// api.github.com or gist.githubusercontent.com, which a ".github.com" or
// ".githubusercontent.com" wildcard used to take in.
func TestGitHubHostsAreTheDownloadHosts(t *testing.T) {
	s, _ := newTestServer(t, Config{})
	for _, host := range []string{"api.github.com", "uploads.github.com", "gist.github.com", "gist.githubusercontent.com"} {
		if hostAllowed(host, s.githubFileHosts()) || hostAllowed(host, s.connectHosts()) {
			t.Errorf("%s is served or tunnelled to as a GitHub download host", host)
		}
	}
	for _, host := range []string{"github.com", "codeload.github.com", "raw.githubusercontent.com",
		"objects.githubusercontent.com", "release-assets.githubusercontent.com"} {
		if !hostAllowed(host, s.githubFileHosts()) {
			t.Errorf("%s is not a GitHub host this cache serves", host)
		}
	}
}

// TestClientSetIsSharedAndNests: the one set is what both the resolver
// and the HTTP half read, restrictions nest, and an address is the same
// entry however net.IP happens to hold it.
func TestClientSetIsSharedAndNests(t *testing.T) {
	set := &ClientSet{}
	dns, err := NewDNSServer(DNSConfig{Restricted: set, UpstreamDNS: []string{"127.0.0.1:1"}})
	if err != nil {
		t.Fatal(err)
	}
	dns.RestrictClient(testClientIP)
	dns.RestrictClient(testClientIP.To16())
	if !set.Restricted(testClientIP.To4()) {
		t.Fatal("a client restricted through the resolver is not restricted in the set it shares")
	}
	s, _ := newTestServer(t, Config{Restricted: set})
	if !s.restrictedClient(httptest.NewRequest(http.MethodGet, "/", nil)) {
		t.Fatal("the HTTP half does not see a client the resolver restricted")
	}
	set.UnrestrictClient(testClientIP)
	if !dns.zeroEgressFor(testClientIP) {
		t.Error("one release of two lifted the restriction")
	}
	set.UnrestrictClient(testClientIP)
	if dns.zeroEgressFor(testClientIP) || set.Restricted(testClientIP) {
		t.Error("the second release left the client restricted")
	}

	var none *ClientSet
	none.RestrictClient(testClientIP)
	if none.Restricted(testClientIP) {
		t.Error("a nil set restricts somebody")
	}
}
