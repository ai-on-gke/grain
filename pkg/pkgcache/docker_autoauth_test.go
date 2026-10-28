package pkgcache

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/gitproxy"
)

func writeTestCredentialLadder(t *testing.T, patterns, tokens map[string]string) *gitproxy.CredentialSet {
	t.Helper()
	dir := t.TempDir()
	if patterns != nil {
		data, err := json.Marshal(patterns)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "credentials.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for name, token := range tokens {
		if err := os.WriteFile(filepath.Join(dir, name+".token"), []byte(token+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	creds, err := gitproxy.LoadCredentialSet(dir)
	if err != nil {
		t.Fatalf("LoadCredentialSet: %v", err)
	}
	// The deployment's git connections, spelled as the patterns map:
	// "owner/repo", then "owner/*", then "*".
	creds.SetConnections(func(owner, repo string) (string, bool) {
		for _, p := range []string{owner + "/" + repo, owner + "/*", "*"} {
			if name, ok := patterns[p]; ok && name != "" {
				return name, true
			}
		}
		return "", false
	})
	return creds
}

func basicAuthHeader(username, password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
}

func TestDockerAuthEndpointAutoAuth(t *testing.T) {
	ca, err := NewCA()
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}

	creds := writeTestCredentialLadder(t, map[string]string{
		"bwsalmon/grain": "narrow",
		"acme/*":         "wide",
		"*":              "global",
	}, map[string]string{
		"narrow": "narrow-token-123",
		"wide":   "wide-token-456",
		"global": "global-token-789",
	})

	var lastAuthHeader, lastScope string
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			lastAuthHeader = r.Header.Get("Authorization")
			lastScope = r.URL.Query().Get("scope")
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
		Credentials:  creds,
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

	// 1. Exact match pattern (bwsalmon/grain -> narrow-token-123)
	lastAuthHeader = ""
	req1, err := http.NewRequest(http.MethodGet, "https://ghcr.io/token?service=ghcr.io&scope=repository:bwsalmon/grain:pull,push", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp1, err := client.Do(req1)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	defer resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("resp1 status = %d, want 200", resp1.StatusCode)
	}
	wantAuth1 := basicAuthHeader("x-access-token", "narrow-token-123")
	if lastAuthHeader != wantAuth1 {
		t.Errorf("got auth header = %q, want %q", lastAuthHeader, wantAuth1)
	}

	// 1b. Subpackage scope (bwsalmon/grain/guest -> narrow-token-123 via bwsalmon/grain repo fallback)
	lastAuthHeader = ""
	req1b, err := http.NewRequest(http.MethodGet, "https://ghcr.io/token?service=ghcr.io&scope=repository:bwsalmon/grain/guest:pull,push", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp1b, err := client.Do(req1b)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	defer resp1b.Body.Close()
	if resp1b.StatusCode != http.StatusOK {
		t.Fatalf("resp1b status = %d, want 200", resp1b.StatusCode)
	}
	if lastAuthHeader != wantAuth1 {
		t.Errorf("got auth header for subpackage = %q, want %q", lastAuthHeader, wantAuth1)
	}
	if want := "repository:bwsalmon/grain/guest:pull"; lastScope != want {
		t.Errorf("scope forwarded with the daemon credential = %q, want %q", lastScope, want)
	}

	// 2. Org wildcard pattern (acme/widgets -> wide-token-456)
	lastAuthHeader = ""
	req2, err := http.NewRequest(http.MethodGet, "https://ghcr.io/token?service=ghcr.io&scope=repository:acme/widgets:push", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp2, err := client.Do(req2)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("resp2 status = %d, want 200", resp2.StatusCode)
	}
	wantAuth2 := basicAuthHeader("x-access-token", "wide-token-456")
	if lastAuthHeader != wantAuth2 {
		t.Errorf("got auth header = %q, want %q", lastAuthHeader, wantAuth2)
	}
	// The caller asked for push alone; the daemon's credential only
	// ever goes upstream asking for a pull (#2377).
	if want := "repository:acme/widgets:pull"; lastScope != want {
		t.Errorf("scope forwarded with the daemon credential = %q, want %q", lastScope, want)
	}

	// 3. Global wildcard pattern (other/repo -> global-token-789)
	lastAuthHeader = ""
	req3, err := http.NewRequest(http.MethodGet, "https://ghcr.io/token?service=ghcr.io&scope=repository:other/repo:pull,push", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp3, err := client.Do(req3)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("resp3 status = %d, want 200", resp3.StatusCode)
	}
	wantAuth3 := basicAuthHeader("x-access-token", "global-token-789")
	if lastAuthHeader != wantAuth3 {
		t.Errorf("got auth header = %q, want %q", lastAuthHeader, wantAuth3)
	}

	// 4. Client provided its own Authorization header -> must NOT be overwritten
	lastAuthHeader = ""
	req4, err := http.NewRequest(http.MethodGet, "https://ghcr.io/token?service=ghcr.io&scope=repository:bwsalmon/grain:pull,push", nil)
	if err != nil {
		t.Fatal(err)
	}
	clientCustomAuth := "Bearer custom-sandbox-token"
	req4.Header.Set("Authorization", clientCustomAuth)
	resp4, err := client.Do(req4)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	defer resp4.Body.Close()
	if resp4.StatusCode != http.StatusOK {
		t.Fatalf("resp4 status = %d, want 200", resp4.StatusCode)
	}
	if lastAuthHeader != clientCustomAuth {
		t.Errorf("got auth header = %q, want %q (should preserve client auth)", lastAuthHeader, clientCustomAuth)
	}
	// A caller's own credential asks for whatever it likes.
	if want := "repository:bwsalmon/grain:pull,push"; lastScope != want {
		t.Errorf("scope forwarded with the caller's credential = %q, want %q", lastScope, want)
	}
}

func TestDockerDirectWriteNoPrematureAuth(t *testing.T) {
	ca, err := NewCA()
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}

	creds := writeTestCredentialLadder(t, map[string]string{
		"bwsalmon/grain": "narrow",
	}, map[string]string{
		"narrow": "narrow-token-123",
	})

	var lastAuthHeader string
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastAuthHeader = r.Header.Get("Authorization")

		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v2/bwsalmon/grain/blobs/uploads/":
			// Upstream OCI registry challenges unauthenticated requests with 401 Www-Authenticate
			if lastAuthHeader == "" {
				w.Header().Set("Www-Authenticate", `Bearer realm="https://ghcr.io/token",service="ghcr.io",scope="repository:bwsalmon/grain:pull,push"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Location", "/v2/bwsalmon/grain/blobs/uploads/uuid-session-123")
			w.WriteHeader(http.StatusAccepted)
		case r.Method == http.MethodPut && r.URL.Path == "/v2/bwsalmon/grain/manifests/v1.0.0":
			if lastAuthHeader == "" {
				w.Header().Set("Www-Authenticate", `Bearer realm="https://ghcr.io/token",service="ghcr.io",scope="repository:bwsalmon/grain:pull,push"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Docker-Content-Digest", "sha256:manifestdigest")
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
		Client:       upstreamClient,
		GHCRRegistry: "http://ghcr.io",
		Credentials:  creds,
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

	// 1. Direct blob upload initiation without client auth -> must NOT inject raw GitHub token as Bearer
	req1, err := http.NewRequest(http.MethodPost, "https://ghcr.io/v2/bwsalmon/grain/blobs/uploads/", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	resp1, err := client.Do(req1)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	defer resp1.Body.Close()
	if resp1.StatusCode != http.StatusUnauthorized {
		t.Fatalf("POST status = %d, want 401 Unauthorized (challenge)", resp1.StatusCode)
	}
	if lastAuthHeader != "" {
		t.Errorf("got auth header = %q, want empty (unauthenticated direct request should not be stamped with invalid Bearer token)", lastAuthHeader)
	}

	// 2. Direct manifest push with valid Bearer token -> forwarded intact
	req2, err := http.NewRequest(http.MethodPut, "https://ghcr.io/v2/bwsalmon/grain/manifests/v1.0.0", strings.NewReader(`{"schemaVersion":2}`))
	if err != nil {
		t.Fatal(err)
	}
	req2.Header.Set("Authorization", "Bearer valid-docker-bearer-token")
	req2.Header.Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
	resp2, err := client.Do(req2)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusCreated {
		t.Fatalf("PUT status = %d, want 201 Created", resp2.StatusCode)
	}
	if lastAuthHeader != "Bearer valid-docker-bearer-token" {
		t.Errorf("got auth header = %q, want %q", lastAuthHeader, "Bearer valid-docker-bearer-token")
	}
}

func TestDockerAutoAuthUncoveredRepo(t *testing.T) {
	ca, err := NewCA()
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}

	// Credential set covers only acme/widgets
	creds := writeTestCredentialLadder(t, map[string]string{
		"acme/widgets": "acme-token",
	}, map[string]string{
		"acme-token": "secret-acme-val",
	})

	var lastAuthHeader string
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastAuthHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"token":"anon-token"}`))
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
		Credentials:  creds,
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

	req, err := http.NewRequest(http.MethodGet, "https://ghcr.io/token?service=ghcr.io&scope=repository:other/uncovered:pull,push", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if lastAuthHeader != "" {
		t.Errorf("got auth header = %q, want empty (uncovered repo)", lastAuthHeader)
	}
}

func TestDockerAutoAuthNilCredentials(t *testing.T) {
	ca, err := NewCA()
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}

	var lastAuthHeader string
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastAuthHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"token":"anon-token"}`))
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
		Credentials:  nil,
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
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if lastAuthHeader != "" {
		t.Errorf("got auth header = %q, want empty (nil credentials)", lastAuthHeader)
	}
}

func TestDockerPushAutoAuthFullE2E(t *testing.T) {
	ca, err := NewCA()
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}

	creds := writeTestCredentialLadder(t, map[string]string{
		"bwsalmon/grain": "narrow",
	}, map[string]string{
		"narrow": "secret-gh-token",
	})

	const issuedBearerToken = "issued-ghcr-bearer-token-abc"

	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			auth := r.Header.Get("Authorization")
			wantAuth := basicAuthHeader("x-access-token", "secret-gh-token")
			if auth != wantAuth {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"token":"` + issuedBearerToken + `"}`))
			return
		}

		auth := r.Header.Get("Authorization")
		if auth != "Bearer "+issuedBearerToken {
			w.Header().Set("Www-Authenticate", `Bearer realm="https://ghcr.io/token",service="ghcr.io",scope="repository:bwsalmon/grain:pull,push"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v2/bwsalmon/grain/blobs/uploads/":
			w.Header().Set("Location", "/v2/bwsalmon/grain/blobs/uploads/session-1234")
			w.WriteHeader(http.StatusAccepted)
		case r.Method == http.MethodPatch && r.URL.Path == "/v2/bwsalmon/grain/blobs/uploads/session-1234":
			w.Header().Set("Location", "/v2/bwsalmon/grain/blobs/uploads/session-1234")
			w.WriteHeader(http.StatusAccepted)
		case r.Method == http.MethodPut && r.URL.Path == "/v2/bwsalmon/grain/blobs/uploads/session-1234":
			w.Header().Set("Location", "/v2/bwsalmon/grain/blobs/sha256:blobdigest")
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPut && r.URL.Path == "/v2/bwsalmon/grain/manifests/latest":
			w.Header().Set("Docker-Content-Digest", "sha256:manifestdigest")
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
		Client:            upstreamClient,
		GHCRRegistry:      "http://ghcr.io",
		Credentials:       creds,
		AllowRegistryPush: true,
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

	// 1. Client unauthenticated request to /token -> proxy auto-authenticates against credential ladder
	tokenReq, err := http.NewRequest(http.MethodGet, "https://ghcr.io/token?service=ghcr.io&scope=repository:bwsalmon/grain:pull,push", nil)
	if err != nil {
		t.Fatal(err)
	}
	tokenResp, err := client.Do(tokenReq)
	if err != nil {
		t.Fatalf("token request failed: %v", err)
	}
	defer tokenResp.Body.Close()
	if tokenResp.StatusCode != http.StatusOK {
		t.Fatalf("token status = %d, want 200", tokenResp.StatusCode)
	}
	var tokenPayload struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(tokenResp.Body).Decode(&tokenPayload); err != nil {
		t.Fatalf("decode token body: %v", err)
	}
	if tokenPayload.Token != issuedBearerToken {
		t.Fatalf("got token %q, want %q", tokenPayload.Token, issuedBearerToken)
	}

	// 2. Client initiates blob upload using the acquired Bearer token
	postReq, err := http.NewRequest(http.MethodPost, "https://ghcr.io/v2/bwsalmon/grain/blobs/uploads/", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	postReq.Header.Set("Authorization", "Bearer "+tokenPayload.Token)
	postResp, err := client.Do(postReq)
	if err != nil {
		t.Fatalf("post uploads failed: %v", err)
	}
	defer postResp.Body.Close()
	if postResp.StatusCode != http.StatusAccepted {
		t.Fatalf("post uploads status = %d, want 202", postResp.StatusCode)
	}

	// 3. Client uploads chunk (PATCH)
	patchReq, err := http.NewRequest(http.MethodPatch, "https://ghcr.io/v2/bwsalmon/grain/blobs/uploads/session-1234", strings.NewReader("blobbytes"))
	if err != nil {
		t.Fatal(err)
	}
	patchReq.Header.Set("Authorization", "Bearer "+tokenPayload.Token)
	patchResp, err := client.Do(patchReq)
	if err != nil {
		t.Fatalf("patch upload failed: %v", err)
	}
	defer patchResp.Body.Close()
	if patchResp.StatusCode != http.StatusAccepted {
		t.Fatalf("patch upload status = %d, want 202", patchResp.StatusCode)
	}

	// 4. Client completes blob upload (PUT)
	putBlobReq, err := http.NewRequest(http.MethodPut, "https://ghcr.io/v2/bwsalmon/grain/blobs/uploads/session-1234?digest=sha256:blobdigest", nil)
	if err != nil {
		t.Fatal(err)
	}
	putBlobReq.Header.Set("Authorization", "Bearer "+tokenPayload.Token)
	putBlobResp, err := client.Do(putBlobReq)
	if err != nil {
		t.Fatalf("put blob failed: %v", err)
	}
	defer putBlobResp.Body.Close()
	if putBlobResp.StatusCode != http.StatusCreated {
		t.Fatalf("put blob status = %d, want 201", putBlobResp.StatusCode)
	}

	// 5. Client puts manifest (PUT)
	putManifestReq, err := http.NewRequest(http.MethodPut, "https://ghcr.io/v2/bwsalmon/grain/manifests/latest", strings.NewReader(`{"schemaVersion":2}`))
	if err != nil {
		t.Fatal(err)
	}
	putManifestReq.Header.Set("Authorization", "Bearer "+tokenPayload.Token)
	putManifestReq.Header.Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
	putManifestResp, err := client.Do(putManifestReq)
	if err != nil {
		t.Fatalf("put manifest failed: %v", err)
	}
	defer putManifestResp.Body.Close()
	if putManifestResp.StatusCode != http.StatusCreated {
		t.Fatalf("put manifest status = %d, want 201", putManifestResp.StatusCode)
	}

	// 6. Direct unauthenticated push operation -> proxy fetches bearer token and succeeds without client login
	directPostReq, err := http.NewRequest(http.MethodPost, "https://ghcr.io/v2/bwsalmon/grain/blobs/uploads/", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	directPostResp, err := client.Do(directPostReq)
	if err != nil {
		t.Fatalf("direct unauthenticated post failed: %v", err)
	}
	defer directPostResp.Body.Close()
	if directPostResp.StatusCode != http.StatusAccepted {
		t.Fatalf("direct post status = %d, want 202 Accepted", directPostResp.StatusCode)
	}
}

func TestDockerPushGHCRForbiddenChallenge(t *testing.T) {
	ca, err := NewCA()
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}

	creds := writeTestCredentialLadder(t, map[string]string{
		"bwsalmon/grain": "narrow",
	}, map[string]string{
		"narrow": "secret-gh-token",
	})

	const issuedBearerToken = "issued-ghcr-bearer-token-xyz"

	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			auth := r.Header.Get("Authorization")
			wantAuth := basicAuthHeader("x-access-token", "secret-gh-token")
			if auth != wantAuth {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"token":"` + issuedBearerToken + `"}`))
			return
		}

		auth := r.Header.Get("Authorization")
		if auth != "Bearer "+issuedBearerToken {
			// Real GHCR returns 403 Forbidden with {"errors":[{"code":"DENIED","message":"invalid token"}]}
			// and NO Www-Authenticate header when an unauthenticated POST is received.
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"errors":[{"code":"DENIED","message":"invalid token"}]}`))
			return
		}

		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v2/bwsalmon/grain/guest/blobs/uploads/":
			w.Header().Set("Location", "/v2/bwsalmon/grain/guest/blobs/uploads/session-5678")
			w.WriteHeader(http.StatusAccepted)
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
		Client:            upstreamClient,
		GHCRRegistry:      "http://ghcr.io",
		Credentials:       creds,
		AllowRegistryPush: true,
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

	// 1. Client with a stale/pull-only token makes request to POST /v2/bwsalmon/grain/guest/blobs/uploads/
	// GHCR returns 403, proxy should translate it to 401 with Www-Authenticate challenge
	postReq, err := http.NewRequest(http.MethodPost, "https://ghcr.io/v2/bwsalmon/grain/guest/blobs/uploads/", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	postReq.Header.Set("Authorization", "Bearer stale-pull-token")
	postResp, err := client.Do(postReq)
	if err != nil {
		t.Fatalf("post uploads failed: %v", err)
	}
	defer postResp.Body.Close()
	if postResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("stale token post uploads status = %d, want 401", postResp.StatusCode)
	}
	wwwAuth := postResp.Header.Get("Www-Authenticate")
	wantAuth := `Bearer realm="https://ghcr.io/token",service="ghcr.io",scope="repository:bwsalmon/grain/guest:pull,push"`
	if wwwAuth != wantAuth {
		t.Fatalf("got Www-Authenticate %q, want %q", wwwAuth, wantAuth)
	}

	// 2. Client requests token from /token based on Www-Authenticate realm/scope
	tokenReq, err := http.NewRequest(http.MethodGet, "https://ghcr.io/token?service=ghcr.io&scope=repository:bwsalmon/grain/guest:pull,push", nil)
	if err != nil {
		t.Fatal(err)
	}
	tokenResp, err := client.Do(tokenReq)
	if err != nil {
		t.Fatalf("token request failed: %v", err)
	}
	defer tokenResp.Body.Close()
	if tokenResp.StatusCode != http.StatusOK {
		t.Fatalf("token status = %d, want 200", tokenResp.StatusCode)
	}
	var tokenPayload struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(tokenResp.Body).Decode(&tokenPayload); err != nil {
		t.Fatalf("decode token body: %v", err)
	}
	if tokenPayload.Token != issuedBearerToken {
		t.Fatalf("got token %q, want %q", tokenPayload.Token, issuedBearerToken)
	}

	// 3. Client retries blob upload using the acquired Bearer token
	postReq2, err := http.NewRequest(http.MethodPost, "https://ghcr.io/v2/bwsalmon/grain/guest/blobs/uploads/", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	postReq2.Header.Set("Authorization", "Bearer "+tokenPayload.Token)
	postResp2, err := client.Do(postReq2)
	if err != nil {
		t.Fatalf("post uploads with token failed: %v", err)
	}
	defer postResp2.Body.Close()
	if postResp2.StatusCode != http.StatusAccepted {
		t.Fatalf("post uploads with token status = %d, want 202", postResp2.StatusCode)
	}

	// 4. Client with docker-login Basic auth sends POST -> GHCR returns 403, proxy translates to 401
	postReqBasic, err := http.NewRequest(http.MethodPost, "https://ghcr.io/v2/bwsalmon/grain/guest/blobs/uploads/", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	postReqBasic.Header.Set("Authorization", basicAuthHeader("x-access-token", "secret-gh-token"))
	postRespBasic, err := client.Do(postReqBasic)
	if err != nil {
		t.Fatalf("post uploads with basic auth failed: %v", err)
	}
	defer postRespBasic.Body.Close()
	if postRespBasic.StatusCode != http.StatusUnauthorized {
		t.Fatalf("basic auth post uploads status = %d, want 401", postRespBasic.StatusCode)
	}
	if wwwAuth := postRespBasic.Header.Get("Www-Authenticate"); wwwAuth != wantAuth {
		t.Fatalf("got Www-Authenticate %q, want %q", wwwAuth, wantAuth)
	}
}

// TestDockerAutoAuthNeverMintsPushWithoutOptIn is #2377: a server left at
// its default must never spend the daemon's credential on anything
// beyond a pull, however the caller shapes its request.
func TestDockerAutoAuthNeverMintsPushWithoutOptIn(t *testing.T) {
	creds := writeTestCredentialLadder(t, map[string]string{
		"acmecorp/*": "org",
	}, map[string]string{
		"org": "daemon-org-token",
	})
	daemonAuth := basicAuthHeader("x-access-token", "daemon-org-token")

	type seen struct{ method, path, scope, auth string }
	var requests []seen
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		requests = append(requests, seen{r.Method, r.URL.Path, r.Form.Get("scope"), r.Header.Get("Authorization")})
		switch {
		case r.URL.Path == "/token":
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Write([]byte(`{"token":"minted"}`))
		case r.Method == http.MethodGet && r.Header.Get("Authorization") == "":
			w.Header().Set("Www-Authenticate", `Bearer realm="`+"http://"+r.Host+`/token",service="ghcr.io",scope="repository:acmecorp/widgets:pull,push,delete"`)
			w.WriteHeader(http.StatusUnauthorized)
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusAccepted)
		default:
			w.Write([]byte("{}"))
		}
	}))
	defer upstreamServer.Close()

	srv, _ := newTestServer(t, Config{
		GHCRRegistry: upstreamServer.URL,
		Credentials:  creds,
	})
	proxy := httptest.NewServer(srv)
	defer proxy.Close()

	do := func(method, path string, body string) {
		t.Helper()
		requests = nil
		req, err := http.NewRequest(method, proxy.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		resp.Body.Close()
		for _, r := range requests {
			if r.auth != "" && (strings.Contains(r.scope, "push") || strings.Contains(r.scope, "delete")) {
				t.Errorf("%s %s: upstream was asked for scope %q with credential %q", method, path, r.scope, r.auth)
			}
			if r.method != http.MethodGet && r.auth != "" {
				t.Errorf("%s %s: upstream %s %s carried Authorization %q", method, path, r.method, r.path, r.auth)
			}
		}
	}

	// Scenario A: a push-only token request through the pass-through is
	// narrowed to pull before the daemon's credential is attached.
	do(http.MethodGet, "/v2/ghcr/token?service=ghcr.io&scope=repository%3Aacmecorp%2Fwidgets%3Apush", "")
	if len(requests) != 1 || requests[0].auth != daemonAuth || requests[0].scope != "repository:acmecorp/widgets:pull" {
		t.Errorf("token pass-through: upstream saw %+v, want one pull-scoped request with the daemon credential", requests)
	}

	// A second scope for some other repository rides along nowhere.
	do(http.MethodGet, "/v2/ghcr/token?service=ghcr.io&scope=repository%3Aacmecorp%2Fwidgets%3Apull&scope=repository%3Aacmecorp%2Fother%3Apush", "")

	// An OAuth2 POST names its scope in a body: no credential at all.
	do(http.MethodPost, "/v2/ghcr/token", "grant_type=refresh_token&service=ghcr.io&scope=repository%3Aacmecorp%2Fwidgets%3Apush")

	// Scenario B: an unauthenticated upload is forwarded without a token
	// being minted for it.
	do(http.MethodPost, "/v2/ghcr/acmecorp/widgets/blobs/uploads/", "")
	for _, r := range requests {
		if r.path == "/token" {
			t.Errorf("upload: pkgcache fetched a token %+v for a write", r)
		}
	}

	// A pull whose challenge asks for more than pull is answered with a
	// pull token.
	do(http.MethodGet, "/v2/ghcr/acmecorp/widgets/manifests/latest", "")
	var fetched bool
	for _, r := range requests {
		if r.path == "/token" {
			fetched = true
			if r.scope != "repository:acmecorp/widgets:pull" {
				t.Errorf("pull: token requested for scope %q, want pull only", r.scope)
			}
		}
	}
	if !fetched {
		t.Errorf("pull: no token fetched, upstream saw %+v", requests)
	}
}

func TestDockerPullOnlyScope(t *testing.T) {
	for in, want := range map[string]string{
		"repository:a/b:pull,push":                 "repository:a/b:pull",
		"repository:a/b/c:push repository:x/y:*":   "repository:a/b/c:pull repository:x/y:pull",
		"registry:catalog:* repository:a/b:delete": "repository:a/b:pull",
		"repository:ghcr.io:443/a/b:push":          "repository:ghcr.io:443/a/b:pull",
		"":                                         "",
		"repository:nonsense":                      "",
	} {
		if got := dockerPullOnlyScope(in); got != want {
			t.Errorf("dockerPullOnlyScope(%q) = %q, want %q", in, got, want)
		}
	}
}
