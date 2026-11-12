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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

const (
	scopedSandboxAddr   = "10.20.0.5"
	unscopedSandboxAddr = "10.20.0.9"
)

// scopeByAddr is a RepoScope that grants each sandbox address the
// repositories named for it, the shape the daemon's own resolver has
// (client address -> sandbox -> repository grants).
func scopeByAddr(grants map[string][]string) RepoScope {
	return RepoScopeFunc(func(r *http.Request, owner, repo string) bool {
		for _, g := range grants[RequestClientIP(r).String()] {
			if strings.EqualFold(g, owner+"/"+repo) {
				return true
			}
		}
		return false
	})
}

// fakePrivateGHCR stands in for ghcr.io serving one private image: a
// token is minted only for the deployment's basic credential, and the
// manifest is served only to that token.
type fakePrivateGHCR struct {
	srv       *httptest.Server
	manifest  []byte
	digest    string
	mu        sync.Mutex
	tokenAuth []string
}

func newFakePrivateGHCR(t *testing.T, wantBasic string) *fakePrivateGHCR {
	t.Helper()
	f := &fakePrivateGHCR{manifest: []byte(`{"schemaVersion":2,"private":true}`)}
	sum := sha256.Sum256(f.manifest)
	f.digest = "sha256:" + hex.EncodeToString(sum[:])
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			f.mu.Lock()
			f.tokenAuth = append(f.tokenAuth, r.Header.Get("Authorization"))
			f.mu.Unlock()
			if r.Header.Get("Authorization") != wantBasic {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"token":"anonymous-token"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"private-pull-jwt"}`))
		case strings.HasPrefix(r.URL.Path, "/v2/acme/private-service/"):
			if r.Header.Get("Authorization") != "Bearer private-pull-jwt" {
				w.Header().Set("Www-Authenticate", `Bearer realm="http://ghcr.io/token",service="ghcr.io",scope="repository:acme/private-service:pull"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			_, _ = w.Write(f.manifest)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakePrivateGHCR) client() *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", f.srv.Listener.Addr().String())
		},
	}}
}

func (f *fakePrivateGHCR) lastTokenAuth() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.tokenAuth) == 0 {
		return ""
	}
	return f.tokenAuth[len(f.tokenAuth)-1]
}

func newScopedGHCRServer(t *testing.T, f *fakePrivateGHCR, scope RepoScope) *Server {
	t.Helper()
	s, _ := newTestServer(t, Config{
		Client:       f.client(),
		GHCRRegistry: "http://ghcr.io",
		Credentials:  staticCreds{"acme/private-service": "ghp_deployment_pat"},
		RepoScope:    scope,
	})
	return s
}

type staticCreds map[string]string

func (m staticCreds) PackageToken(owner, repo string) (string, bool) {
	tok, ok := m[owner+"/"+repo]
	return tok, ok
}

func ghcrRequest(method, target, from string) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	req.Host = "ghcr.io"
	req.RemoteAddr = from + ":40000"
	return req
}

var deploymentBasic = "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:ghp_deployment_pat"))

func TestGHCRTokenIsMintedWithDeploymentCredentialOnlyForSandboxInScope(t *testing.T) {
	f := newFakePrivateGHCR(t, deploymentBasic)
	s := newScopedGHCRServer(t, f, scopeByAddr(map[string][]string{
		scopedSandboxAddr:   {"acme/private-service"},
		unscopedSandboxAddr: {"acme/public"},
	}))

	const target = "/token?service=ghcr.io&scope=repository:acme/private-service:pull"

	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, ghcrRequest(http.MethodGet, target, scopedSandboxAddr))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "private-pull-jwt") {
		t.Fatalf("sandbox granted acme/private-service: got %d %q, want the private pull token", rec.Code, rec.Body.String())
	}
	if got := f.lastTokenAuth(); got != deploymentBasic {
		t.Fatalf("sandbox granted acme/private-service: token request carried %q, want the deployment credential", got)
	}

	for _, from := range []string{unscopedSandboxAddr, "10.20.0.77"} {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, ghcrRequest(http.MethodGet, target, from))
		if strings.Contains(rec.Body.String(), "private-pull-jwt") {
			t.Errorf("sandbox at %s without a grant for acme/private-service was minted a private pull token: %q", from, rec.Body.String())
		}
		if got := f.lastTokenAuth(); got != "" {
			t.Errorf("sandbox at %s without a grant: token request carried %q, want no credential at all", from, got)
		}
	}
}

func TestGHCRTokenNeverUsesDeploymentCredentialWithoutRepoScope(t *testing.T) {
	f := newFakePrivateGHCR(t, deploymentBasic)
	s := newScopedGHCRServer(t, f, nil)

	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, ghcrRequest(http.MethodGet, "/token?service=ghcr.io&scope=repository:acme/private-service:pull", scopedSandboxAddr))
	if strings.Contains(rec.Body.String(), "private-pull-jwt") || f.lastTokenAuth() != "" {
		t.Fatalf("with no RepoScope the deployment credential was spent: body %q, token auth %q", rec.Body.String(), f.lastTokenAuth())
	}

	// The legacy mirror route is held to the same rule.
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v2/ghcr/token?service=ghcr.io&scope=repository:acme/private-service:pull", nil)
	req.RemoteAddr = scopedSandboxAddr + ":40000"
	s.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), "private-pull-jwt") || f.lastTokenAuth() != "" {
		t.Fatalf("legacy /v2/ghcr/token with no RepoScope spent the deployment credential: body %q, token auth %q", rec.Body.String(), f.lastTokenAuth())
	}
}

func TestGHCRPrivateManifestServedOnlyToSandboxInScope(t *testing.T) {
	f := newFakePrivateGHCR(t, deploymentBasic)
	s := newScopedGHCRServer(t, f, scopeByAddr(map[string][]string{
		scopedSandboxAddr: {"acme/private-service"},
	}))
	byDigest := "/v2/acme/private-service/manifests/" + f.digest

	// Out of scope first, so a refusal here cannot be explained by
	// anything the in-scope fetch below left behind.
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, ghcrRequest(http.MethodGet, byDigest, unscopedSandboxAddr))
	if rec.Code == http.StatusOK || bytes.Contains(rec.Body.Bytes(), f.manifest) {
		t.Fatalf("sandbox without a grant pulled the private manifest: %d %q", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, ghcrRequest(http.MethodGet, byDigest, scopedSandboxAddr))
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), f.manifest) {
		t.Fatalf("sandbox granted acme/private-service: got %d %q, want the manifest", rec.Code, rec.Body.String())
	}

	// The in-scope pull is now cached; the copy must stay with that
	// repository rather than in the shared digest namespace.
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, ghcrRequest(http.MethodGet, byDigest, unscopedSandboxAddr))
	if rec.Code == http.StatusOK || bytes.Contains(rec.Body.Bytes(), f.manifest) {
		t.Fatalf("sandbox without a grant was served the cached private manifest: %d %q", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, ghcrRequest(http.MethodGet, byDigest, scopedSandboxAddr))
	if rec.Code != http.StatusOK || rec.Header().Get(cacheStatusHeader) != "hit" {
		t.Fatalf("second in-scope pull: got %d cache %q, want a hit", rec.Code, rec.Header().Get(cacheStatusHeader))
	}
}

// A private manifest this cache stored before RepoScope existed sits under
// the old shared "blob:<digest>" key, fetched with the deployment's
// credential. It must never be served again -- not through GHCR's route,
// nor through any other registry's, since that namespace named none.
func TestGHCRPrivateManifestCachedBeforeRepoScopeIsNotServed(t *testing.T) {
	f := newFakePrivateGHCR(t, deploymentBasic)
	s, cache := newTestServer(t, Config{
		Client:         f.client(),
		GHCRRegistry:   "http://ghcr.io",
		DockerRegistry: "http://ghcr.io",
		Credentials:    staticCreds{"acme/private-service": "ghp_deployment_pat"},
		RepoScope:      scopeByAddr(nil),
	})
	w, err := cache.NewWriter(KindDocker, "blob:"+f.digest)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if _, err := w.Write(f.manifest); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !w.Commit(Meta{URL: "http://ghcr.io/v2/acme/private-service/manifests/" + f.digest}) {
		t.Fatal("Commit stored nothing")
	}

	dockerHub := ghcrRequest(http.MethodGet, "/v2/library/anything/manifests/"+f.digest, unscopedSandboxAddr)
	dockerHub.Host = "registry-1.docker.io"
	for _, req := range []*http.Request{
		ghcrRequest(http.MethodGet, "/v2/acme/private-service/manifests/"+f.digest, unscopedSandboxAddr),
		ghcrRequest(http.MethodGet, "/v2/acme/private-service/blobs/"+f.digest, unscopedSandboxAddr),
		ghcrRequest(http.MethodGet, "/v2/ghcr/acme/private-service/manifests/"+f.digest, unscopedSandboxAddr),
		dockerHub,
	} {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if bytes.Contains(rec.Body.Bytes(), f.manifest) || rec.Header().Get(cacheStatusHeader) == "hit" {
			t.Errorf("%s (host %s): served the private manifest cached under the pre-RepoScope key: %d %q",
				req.URL.Path, req.Host, rec.Code, rec.Body.String())
		}
	}
}

func TestClientNamesReleaseLeavesAReusedAddressAlone(t *testing.T) {
	var c ClientNames
	ip := net.ParseIP("10.20.0.5")
	c.RegisterClient(ip, "run-1")
	c.RegisterClient(net.ParseIP("::ffff:10.20.0.5"), "run-2")
	c.ReleaseClient(ip, "run-1")
	if name, ok := c.Name(ip); !ok || name != "run-2" {
		t.Fatalf("after a stale release: Name = %q, %v; want run-2", name, ok)
	}
	c.ReleaseClient(ip, "run-2")
	if name, ok := c.Name(ip); ok {
		t.Fatalf("after release: Name = %q, want none", name)
	}
	var nilNames *ClientNames
	if _, ok := nilNames.NameFor(httptest.NewRequest(http.MethodGet, "/", nil)); ok {
		t.Fatal("a nil ClientNames named somebody")
	}
}

func TestGitHubUploadPackWithCredentialIsNeverCachedForOthers(t *testing.T) {
	var hits int32
	packData := []byte("0008NAK\n0000PACKprivate-repository-objects")
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/acme/private-service.git/git-upload-pack" {
			http.NotFound(w, r)
			return
		}
		atomic.AddInt32(&hits, 1)
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
		_, _ = w.Write(packData)
	}))
	defer up.Close()
	s, _ := newTestServer(t, Config{GitHubURL: up.URL})
	body := []byte("0032want 95036130a102ff96004b4904d0f50d03247d0335\n00000009done\n")

	post := func(auth string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/github/acme/private-service.git/git-upload-pack", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/x-git-upload-pack-request")
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		return rec
	}

	rec := post("Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:sandbox-a-token")))
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), packData) {
		t.Fatalf("credentialed fetch: got %d %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(cacheStatusHeader); got != "pass" {
		t.Errorf("credentialed fetch cache status = %q, want pass (never stored)", got)
	}

	rec = post("")
	if rec.Code == http.StatusOK || bytes.Contains(rec.Body.Bytes(), []byte("private-repository-objects")) {
		t.Fatalf("anonymous fetch with the same negotiation was served the private pack: %d %q", rec.Code, rec.Body.String())
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("upstream hits = %d, want 2 (the anonymous request must go upstream)", got)
	}
}

func TestGitHubCredentialedArchiveIsNeverCachedForOthers(t *testing.T) {
	var hits int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte("private release asset"))
	}))
	defer up.Close()
	s, _ := newTestServer(t, Config{GitHubURL: up.URL})
	get := func(auth string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/github/acme/private-service/releases/download/v1.0.0/tool.tar.gz", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		return rec
	}
	if rec := get("Bearer sandbox-a-token"); rec.Code != http.StatusOK {
		t.Fatalf("credentialed download: %d", rec.Code)
	}
	if rec := get(""); rec.Code == http.StatusOK || strings.Contains(rec.Body.String(), "private release asset") {
		t.Fatalf("anonymous download was served the credentialed copy: %d %q", rec.Code, rec.Body.String())
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("upstream hits = %d, want 2", got)
	}
}

func TestGitHubAnonymousUploadPackIsStillCached(t *testing.T) {
	var hits int32
	packData := []byte("0008NAK\n0000PACKpublic-objects")
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
		_, _ = w.Write(packData)
	}))
	defer up.Close()
	s, _ := newTestServer(t, Config{GitHubURL: up.URL})
	body := []byte("0032want 95036130a102ff96004b4904d0f50d03247d0335\n00000009done\n")
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/github/acme/public.git/git-upload-pack", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), packData) {
			t.Fatalf("fetch %d: %d %q", i, rec.Code, rec.Body.String())
		}
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("upstream hits = %d, want 1 (a public pack is still shared)", got)
	}
}

func TestGHCRTrailingDotAndMirrorHostsEnforceRepoScope(t *testing.T) {
	f := newFakePrivateGHCR(t, deploymentBasic)
	s, _ := newTestServer(t, Config{
		Client:         f.client(),
		GHCRRegistry:   "http://ghcr.io",
		DockerRegistry: "http://ghcr.io",
		Credentials:    staticCreds{"acme/private-service": "ghp_deployment_pat"},
		RepoScope: scopeByAddr(map[string][]string{
			scopedSandboxAddr: {"acme/private-service"},
		}),
	})
	byDigest := "/v2/acme/private-service/manifests/" + f.digest

	// A trailing dot in Host ("ghcr.io.") or another registry mirror pointed
	// at ghcr.io must never bypass scopedOCIClient or spend Credentials for
	// an unscoped sandbox.
	trailingDotOut := ghcrRequest(http.MethodGet, byDigest, unscopedSandboxAddr)
	trailingDotOut.Host = "ghcr.io."
	dockerMirrorOut := ghcrRequest(http.MethodGet, byDigest, unscopedSandboxAddr)
	dockerMirrorOut.Host = "registry-1.docker.io"
	for _, req := range []*http.Request{trailingDotOut, dockerMirrorOut} {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code == http.StatusOK || bytes.Contains(rec.Body.Bytes(), f.manifest) {
			t.Fatalf("unscoped request (%s %s) pulled private manifest: %d %q", req.Host, req.URL.Path, rec.Code, rec.Body.String())
		}
	}

	// An in-scope sandbox reaching "ghcr.io." still resolves through the
	// scoped GHCR client and caches per repository rather than globally.
	trailingDotIn := ghcrRequest(http.MethodGet, byDigest, scopedSandboxAddr)
	trailingDotIn.Host = "ghcr.io."
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, trailingDotIn)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), f.manifest) {
		t.Fatalf("in-scope trailing-dot request: got %d %q, want private manifest", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, trailingDotOut)
	if rec.Code == http.StatusOK || bytes.Contains(rec.Body.Bytes(), f.manifest) {
		t.Fatalf("unscoped trailing-dot request hit cached private manifest: %d %q", rec.Code, rec.Body.String())
	}
}

func TestGHCRTokenScopeRejectsActionSmugglingPathConfusionAndMultiScopeBypass(t *testing.T) {
	var mu sync.Mutex
	type upstreamCall struct {
		method, path, scope, auth string
	}
	var calls []upstreamCall
	resetCalls := func() {
		mu.Lock()
		calls = nil
		mu.Unlock()
	}
	takeCalls := func() []upstreamCall {
		mu.Lock()
		defer mu.Unlock()
		return append([]upstreamCall(nil), calls...)
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, upstreamCall{
			method: r.Method,
			path:   r.URL.Path,
			scope:  strings.Join(r.URL.Query()["scope"], " | "),
			auth:   r.Header.Get("Authorization"),
		})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"tok"}`))
	}))
	t.Cleanup(up.Close)

	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", up.Listener.Addr().String())
		},
	}}
	scope := scopeByAddr(map[string][]string{
		scopedSandboxAddr: {"acme/in-scope"},
	})
	creds := staticCreds{
		"acme/in-scope":     "ghp_deployment_pat",
		"acme/out-of-scope": "ghp_deployment_pat",
	}

	// 1. Default pull-only mode: extra colons in scope must not smuggle push/delete,
	// and ?scope= on a /v2/<other-repo>/... path must never receive Basic auth.
	pullSrv, _ := newTestServer(t, Config{
		Client:       client,
		GHCRRegistry: "http://ghcr.io",
		Credentials:  creds,
		RepoScope:    scope,
	})

	resetCalls()
	rec := httptest.NewRecorder()
	pullSrv.ServeHTTP(rec, ghcrRequest(http.MethodGet, "/token?service=ghcr.io&scope=repository:acme/in-scope/sub:push,delete:pull", scopedSandboxAddr))
	if got := takeCalls(); len(got) != 1 || got[0].auth != deploymentBasic || got[0].scope != "repository:acme/in-scope/sub:pull" {
		t.Fatalf("colon-smuggling token request sent %+v, want pull-only scope with deployment Basic auth", got)
	}

	resetCalls()
	rec = httptest.NewRecorder()
	pullSrv.ServeHTTP(rec, ghcrRequest(http.MethodGet, "/v2/acme/out-of-scope/manifests/latest?scope=repository:acme/in-scope:pull", scopedSandboxAddr))
	for _, c := range takeCalls() {
		if c.auth != "" {
			t.Fatalf("/v2/ out-of-scope path with ?scope= received credential: %+v", c)
		}
	}

	// 2. AllowRegistryPush mode: multi-scope GET, POST /token, and ?scope= on
	// an out-of-scope PUT must never spend the credential on acme/out-of-scope.
	pushSrv, _ := newTestServer(t, Config{
		Client:            client,
		GHCRRegistry:      "http://ghcr.io",
		Credentials:       creds,
		RepoScope:         scope,
		AllowRegistryPush: true,
	})

	resetCalls()
	rec = httptest.NewRecorder()
	pushSrv.ServeHTTP(rec, ghcrRequest(http.MethodGet, "/token?service=ghcr.io&scope=repository:acme/in-scope:pull&scope=repository:acme/out-of-scope:pull,push", scopedSandboxAddr))
	if got := takeCalls(); len(got) != 1 || got[0].scope != "repository:acme/in-scope:pull" {
		t.Fatalf("multi-scope GET forwarded %+v, want narrowed single in-scope repository", got)
	}

	resetCalls()
	postTok := httptest.NewRequest(http.MethodPost, "/token?service=ghcr.io&scope=repository:acme/in-scope:pull", strings.NewReader("scope=repository:acme/out-of-scope:pull,push"))
	postTok.Host = "ghcr.io"
	postTok.RemoteAddr = scopedSandboxAddr + ":40000"
	rec = httptest.NewRecorder()
	pushSrv.ServeHTTP(rec, postTok)
	for _, c := range takeCalls() {
		if c.auth != "" {
			t.Fatalf("POST /token received deployment credential: %+v", c)
		}
	}

	resetCalls()
	putOut := httptest.NewRequest(http.MethodPut, "/v2/acme/out-of-scope/manifests/latest?scope=repository:acme/in-scope:pull", strings.NewReader(`{}`))
	putOut.Host = "ghcr.io"
	putOut.RemoteAddr = scopedSandboxAddr + ":40000"
	rec = httptest.NewRecorder()
	pushSrv.ServeHTTP(rec, putOut)
	for _, c := range takeCalls() {
		if c.auth != "" {
			t.Fatalf("out-of-scope PUT with in-scope ?scope= received deployment credential: %+v", c)
		}
	}
}

func TestOCIPullWithCallerAuthorizationIsNeverCachedForOthers(t *testing.T) {
	manifest := []byte(`{"schemaVersion":2,"callerPrivate":true}`)
	sum := sha256.Sum256(manifest)
	digest := "sha256:" + hex.EncodeToString(sum[:])

	var hits int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if r.Header.Get("Authorization") != "Bearer sandbox-a-private-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		_, _ = w.Write(manifest)
	}))
	t.Cleanup(up.Close)

	s, _ := newTestServer(t, Config{
		Client: &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "tcp", up.Listener.Addr().String())
			},
		}},
		GCRRegistry: "http://gcr.io",
	})

	reqA := httptest.NewRequest(http.MethodGet, "/v2/secret-proj/app/manifests/"+digest, nil)
	reqA.Host = "gcr.io"
	reqA.Header.Set("Authorization", "Bearer sandbox-a-private-token")
	recA := httptest.NewRecorder()
	s.ServeHTTP(recA, reqA)
	if recA.Code != http.StatusOK || !bytes.Equal(recA.Body.Bytes(), manifest) {
		t.Fatalf("credentialed pull: got %d %q", recA.Code, recA.Body.String())
	}
	if got := recA.Header().Get(cacheStatusHeader); got != "pass" {
		t.Errorf("credentialed OCI pull cache status = %q, want pass", got)
	}

	reqB := httptest.NewRequest(http.MethodGet, "/v2/secret-proj/app/manifests/"+digest, nil)
	reqB.Host = "gcr.io"
	recB := httptest.NewRecorder()
	s.ServeHTTP(recB, reqB)
	if recB.Code == http.StatusOK || bytes.Contains(recB.Body.Bytes(), manifest) {
		t.Fatalf("anonymous caller was served private OCI manifest cached from another sandbox's token: %d %q", recB.Code, recB.Body.String())
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("upstream hits = %d, want 2", got)
	}
}

func TestGitHubReleaseAssetCachedBeforeAnonKeyIsNotServed(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(up.Close)

	s, cache := newTestServer(t, Config{GitHubURL: up.URL})
	assetPath := "/acme/private-service/releases/download/v1.0.0/tool.tar.gz"
	preFixKey := up.URL + assetPath
	w, err := cache.NewWriter(KindGitHub, preFixKey)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if _, err := w.Write([]byte("pre-fix-private-asset")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !w.Commit(Meta{URL: preFixKey}) {
		t.Fatal("Commit stored nothing")
	}

	req := httptest.NewRequest(http.MethodGet, "/github"+assetPath, nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK || strings.Contains(rec.Body.String(), "pre-fix-private-asset") || rec.Header().Get(cacheStatusHeader) == "hit" {
		t.Fatalf("served pre-fix URL-keyed GitHub asset to anonymous caller: %d %q", rec.Code, rec.Body.String())
	}
}
