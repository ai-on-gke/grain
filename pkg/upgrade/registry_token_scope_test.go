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

package upgrade

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
)

// registryHosts routes registryClient's connections by the host a request
// names, each to its own server: separate listeners, so what one server
// saw is only what was sent to that host.
func registryHosts(t *testing.T, hosts map[string]*httptest.Server) {
	t.Helper()
	saved := registryClient
	registryClient = &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, _ := net.SplitHostPort(addr)
			srv, ok := hosts[host]
			if !ok {
				t.Errorf("dialed %s, which no server stands in for", addr)
				return nil, &net.OpError{Op: "dial", Net: network}
			}
			return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
		},
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}}
	t.Cleanup(func() { registryClient = saved })
}

// authSeen records the Authorization header of every request a server
// answered, by path.
type authSeen struct {
	mu   sync.Mutex
	seen map[string][]string
}

func (a *authSeen) record(r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.seen == nil {
		a.seen = map[string][]string{}
	}
	a.seen[r.URL.Path] = append(a.seen[r.URL.Path], r.Header.Get("Authorization"))
}

func (a *authSeen) all() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	for _, v := range a.seen {
		out = append(out, v...)
	}
	return out
}

// The token-scoping walkthrough of test plan 2026-10-29 §8 over real
// HTTP with separate servers: a Google-shaped registry host that answers
// its tag list with a redirect to another host, and a registry that is
// not Google's. The Google token reaches the first and only the first:
// not the host the redirect leads to, and never a non-Google registry,
// for which it is not even looked up.
func TestGoogleTokenReachesOnlyTheGoogleRegistryAcrossARedirect(t *testing.T) {
	asked := googleToken(t, "ya29.walkthrough")

	var google, blobs, other authSeen
	googleSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		google.record(r)
		if r.Header.Get("Authorization") != "Bearer ya29.walkthrough" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/v2/proj/grain/guest/manifests/sha-aaaa111":
			w.Header().Set("Docker-Content-Digest", testDigest)
		case "/v2/proj/grain/guest/tags/list":
			http.Redirect(w, r, "https://blobs.example/v2/proj/grain/guest/tags/list", http.StatusTemporaryRedirect)
		default:
			http.NotFound(w, r)
		}
	}))
	defer googleSrv.Close()
	blobsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		blobs.record(r)
		w.Write([]byte(`{"tags":["sha-aaaa111","sha-bbbb222"]}`))
	}))
	defer blobsSrv.Close()
	otherSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		other.record(r)
		switch r.URL.Path {
		case "/v2/team/guest/manifests/v1":
			w.Header().Set("Docker-Content-Digest", testDigest)
		case "/v2/team/guest/tags/list":
			w.Write([]byte(`{"tags":["v1"]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer otherSrv.Close()
	registryHosts(t, map[string]*httptest.Server{
		"us-docker.pkg.dev": googleSrv,
		"blobs.example":     blobsSrv,
		"registry.example":  otherSrv,
	})
	ctx := t.Context()

	if digest, err := RegistryManifestDigest(ctx, "us-docker.pkg.dev/proj/grain/guest:sha-aaaa111"); err != nil || digest != testDigest {
		t.Fatalf("RegistryManifestDigest(google) = %q, %v", digest, err)
	}
	tags, err := ListRegistryTags(ctx, "us-docker.pkg.dev/proj/grain/guest")
	if err != nil || !slices.Equal(tags, []string{"sha-aaaa111", "sha-bbbb222"}) {
		t.Fatalf("ListRegistryTags(google, redirected) = %v, %v", tags, err)
	}
	for _, got := range google.all() {
		if got != "Bearer ya29.walkthrough" {
			t.Errorf("the Google registry was asked with Authorization %q, want the Google token", got)
		}
	}
	if got := blobs.all(); len(got) != 1 || got[0] != "" {
		t.Errorf("the redirect target saw Authorization %q, want one request carrying none", got)
	}

	askedBefore := *asked
	if digest, err := RegistryManifestDigest(ctx, "registry.example/team/guest:v1"); err != nil || digest != testDigest {
		t.Fatalf("RegistryManifestDigest(other) = %q, %v", digest, err)
	}
	if tags, err := ListRegistryTags(ctx, "registry.example/team/guest"); err != nil || !slices.Equal(tags, []string{"v1"}) {
		t.Fatalf("ListRegistryTags(other) = %v, %v", tags, err)
	}
	if got := other.all(); len(got) != 2 || got[0] != "" || got[1] != "" {
		t.Errorf("the non-Google registry saw Authorization %q, want two requests carrying none", got)
	}
	if *asked != askedBefore {
		t.Errorf("a Google token was looked up for a non-Google registry")
	}
}
