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

package ui

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/ai-on-gke/grain/pkg/authfix"
	"github.com/ai-on-gke/grain/pkg/ci"
)

func TestHTTPClient_StaticTokenHeader(t *testing.T) {
	var gotAuth, gotTunnel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotTunnel = r.Header.Get("X-Grain-Tunnel-Token")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	c := NewHTTPClient(srv.URL)
	c.Token = "secret-proxy-token-123"

	tasks, err := c.ListTasks(context.Background())
	if err != nil {
		t.Fatalf("ListTasks failed: %v", err)
	}
	if tasks == nil {
		t.Fatalf("expected non-nil tasks")
	}
	if gotAuth != "Bearer secret-proxy-token-123" {
		t.Errorf("got Authorization %q, want %q", gotAuth, "Bearer secret-proxy-token-123")
	}
	if gotTunnel != "secret-proxy-token-123" {
		t.Errorf("got X-Grain-Tunnel-Token %q, want %q", gotTunnel, "secret-proxy-token-123")
	}
}

func TestHTTPClient_TokenWithBearerPrefix(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	c := NewHTTPClient(srv.URL)
	c.Token = "Bearer already-prefixed-token"

	_, err := c.ListTasks(context.Background())
	if err != nil {
		t.Fatalf("ListTasks failed: %v", err)
	}
	if gotAuth != "Bearer already-prefixed-token" {
		t.Errorf("got Authorization %q, want %q", gotAuth, "Bearer already-prefixed-token")
	}
}

func TestHTTPClient_TokenProvider(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer dynamic-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	c := NewHTTPClient(srv.URL)
	c.TokenProvider = func(ctx context.Context) (string, error) {
		calls.Add(1)
		return "dynamic-token", nil
	}

	_, err := c.ListTasks(context.Background())
	if err != nil {
		t.Fatalf("ListTasks failed: %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("got %d TokenProvider calls, want 1", calls.Load())
	}
}

func TestHTTPClient_EnvVarToken(t *testing.T) {
	for _, env := range []string{"GRAIN_TOKEN", "GRAIN_AUTH_TOKEN", "GRAIN_ID_TOKEN", "GRAIN_TUNNEL_TOKEN"} {
		t.Run(env, func(t *testing.T) {
			t.Setenv(env, "token-from-"+env)
			var gotAuth, gotTunnel string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotAuth = r.Header.Get("Authorization")
				gotTunnel = r.Header.Get("X-Grain-Tunnel-Token")
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`[]`))
			}))
			defer srv.Close()

			c := NewHTTPClient(srv.URL)
			_, err := c.ListTasks(context.Background())
			if err != nil {
				t.Fatalf("ListTasks failed: %v", err)
			}
			expected := "token-from-" + env
			if gotAuth != "Bearer "+expected {
				t.Errorf("got Authorization %q, want %q", gotAuth, "Bearer "+expected)
			}
			if gotTunnel != expected {
				t.Errorf("got X-Grain-Tunnel-Token %q, want %q", gotTunnel, "Bearer "+expected)
			}
		})
	}
}

func TestHTTPClient_UserIdentityHeader(t *testing.T) {
	var gotUser string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser = r.Header.Get("X-Forwarded-User")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	c := NewHTTPClient(srv.URL)
	c.User = "alice@example.com"

	_, err := c.ListTasks(context.Background())
	if err != nil {
		t.Fatalf("ListTasks failed: %v", err)
	}
	if gotUser != "alice@example.com" {
		t.Errorf("got X-Forwarded-User %q, want %q", gotUser, "alice@example.com")
	}
}

func TestHTTPClient_AttachmentAndDownloadCarryAuth(t *testing.T) {
	var attachmentAuth, downloadAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tasks/1/attachments/2":
			attachmentAuth = r.Header.Get("Authorization")
			_, _ = w.Write([]byte("attachment content"))
		case "/api/ci/runs/r1/artifacts/job1/art1":
			downloadAuth = r.Header.Get("Authorization")
			_, _ = w.Write([]byte("artifact content"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := NewHTTPClient(srv.URL)
	c.Token = "proxy-test-token"

	att, err := c.Attachment(context.Background(), "1", 2)
	if err != nil {
		t.Fatalf("Attachment failed: %v", err)
	}
	if string(att.Content) != "attachment content" {
		t.Errorf("got attachment content %q", string(att.Content))
	}
	if attachmentAuth != "Bearer proxy-test-token" {
		t.Errorf("Attachment got Authorization %q, want Bearer proxy-test-token", attachmentAuth)
	}

	var buf bytes.Buffer
	n, err := c.DownloadCIArtifact(context.Background(), ci.ArtifactRef{Run: "r1", Job: "job1", Name: "art1"}, &buf)
	if err != nil {
		t.Fatalf("DownloadCIArtifact failed: %v", err)
	}
	if n != int64(len("artifact content")) {
		t.Errorf("got downloaded bytes %d, want %d", n, len("artifact content"))
	}
	if downloadAuth != "Bearer proxy-test-token" {
		t.Errorf("DownloadCIArtifact got Authorization %q, want Bearer proxy-test-token", downloadAuth)
	}
}

func TestHTTPClient_ShouldAttemptCloudRunAuth(t *testing.T) {
	tests := []struct {
		url      string
		audience string
		want     bool
	}{
		{url: "http://127.0.0.1:8420", want: false},
		{url: "http://localhost:8420", want: false},
		{url: "https://grain-server.internal", want: true},
		{url: "http://grain-proxy-1234.a.run.app", want: true},
		{url: "https://grain-proxy-1234.a.run.app", want: true},
		{url: "http://grain.run.app.example.com", want: false},
		{url: "http://127.0.0.1:8420", audience: "https://my-cloudrun-service", want: true},
	}

	for _, tt := range tests {
		c := NewHTTPClient(tt.url)
		c.Audience = tt.audience
		got := c.shouldAttemptCloudRunAuth()
		if got != tt.want {
			t.Errorf("shouldAttemptCloudRunAuth(%q, aud=%q) = %v, want %v", tt.url, tt.audience, got, tt.want)
		}
	}
}

func TestHTTPClient_TokenCacheExpiry(t *testing.T) {
	c := NewHTTPClient("https://proxy.example.com")
	c.cachedToken = "cached-token-abc"
	c.cachedExpiry = time.Now().Add(10 * time.Minute)

	tok, err := c.getGoogleIDToken(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "cached-token-abc" {
		t.Errorf("got %q, want cached-token-abc", tok)
	}
}

// fakeJWT is a token with an exp claim and no valid signature, which is
// all the client reads of one.
func fakeJWT(exp time.Time) string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none"}`)) + "." +
		enc([]byte(fmt.Sprintf(`{"exp":%d}`, exp.Unix()))) + ".sig"
}

// gcloudStub answers print-identity-token: with audienceTok when asked
// for an audience (empty meaning it fails, as it does for a user login),
// and with plainTok when not. It records which forms were asked for.
func gcloudStub(audienceTok, plainTok string, calls *[]string) func(context.Context, ...string) (string, error) {
	return func(_ context.Context, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		*calls = append(*calls, joined)
		if strings.Contains(joined, "--audiences=") {
			if audienceTok == "" {
				return "", errors.New("audiences only for service accounts")
			}
			return audienceTok, nil
		}
		return plainTok, nil
	}
}

func noADC(context.Context, string) (*oauth2.Token, error) {
	return nil, errors.New("no application default credentials")
}

// TestHTTPClient_NoAudienceTokenOnlyForCloudRun: gcloud's audience-less
// identity token can be replayed by whoever receives it, so it goes to a
// Cloud Run service's own address and to no other https server.
func TestHTTPClient_NoAudienceTokenOnlyForCloudRun(t *testing.T) {
	plain := fakeJWT(time.Now().Add(time.Hour))

	var calls []string
	c := NewHTTPClient("https://grain.example.com")
	c.adcIDToken = noADC
	c.gcloud = gcloudStub("", plain, &calls)
	tok, err := c.getGoogleIDToken(context.Background())
	if err != nil || tok != "" {
		t.Fatalf("non-Cloud-Run host: token %q, err %v; want none", tok, err)
	}
	for _, call := range calls {
		if !strings.Contains(call, "--audiences=") {
			t.Errorf("non-Cloud-Run host ran gcloud %q, minting a token with no audience", call)
		}
	}

	calls = nil
	c = NewHTTPClient("https://grain-1234.a.run.app")
	c.adcIDToken = noADC
	c.gcloud = gcloudStub("", plain, &calls)
	if tok, err := c.getGoogleIDToken(context.Background()); err != nil || tok != plain {
		t.Fatalf("Cloud Run host: token %q, err %v; want gcloud's own", tok, err)
	}
}

// TestHTTPClient_IDTokenCachedUntilItsOwnExpiry: a minted token is kept
// until shortly before its exp claim says it expires, and one whose
// expiry cannot be read is not kept at all.
func TestHTTPClient_IDTokenCachedUntilItsOwnExpiry(t *testing.T) {
	exp := time.Now().Add(5 * time.Minute).Truncate(time.Second)
	var calls []string
	c := NewHTTPClient("https://grain.example.com")
	c.adcIDToken = noADC
	c.gcloud = gcloudStub(fakeJWT(exp), "", &calls)
	if _, err := c.getGoogleIDToken(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := exp.Add(-time.Minute); !c.cachedExpiry.Equal(want) {
		t.Errorf("cached until %v, want %v", c.cachedExpiry, want)
	}

	calls = nil
	c = NewHTTPClient("https://grain.example.com")
	c.adcIDToken = noADC
	c.gcloud = gcloudStub("opaque-token", "", &calls)
	for i := 0; i < 2; i++ {
		if tok, err := c.getGoogleIDToken(context.Background()); err != nil || tok != "opaque-token" {
			t.Fatalf("token %q, err %v", tok, err)
		}
	}
	if len(calls) != 2 {
		t.Errorf("a token with no readable expiry was cached: gcloud ran %d times, want 2", len(calls))
	}
}

// TestHTTPClient_DefaultClientHasResponseHeaderTimeout: with no HTTP
// client of its own, a call to a hung server gives up rather than
// blocking until the caller's context ends.
func TestHTTPClient_DefaultClientHasResponseHeaderTimeout(t *testing.T) {
	tr, ok := NewHTTPClient("http://127.0.0.1:1").httpClient().Transport.(*http.Transport)
	if !ok || tr.ResponseHeaderTimeout <= 0 {
		t.Fatalf("default client transport %#v has no ResponseHeaderTimeout", tr)
	}
}

// #XC-15: the CLI names the tool and session it runs in on every request,
// as one sanitized X-Grain-Client line built from $GRAIN_CLIENT and
// $GRAIN_CLIENT_SESSION, and sends nothing when neither is set.
func TestHTTPClient_XGrainClientFromEnvAndSession(t *testing.T) {
	var got []string
	var present []bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, ok := r.Header[http.CanonicalHeaderKey("X-Grain-Client")]
		present = append(present, ok)
		got = append(got, r.Header.Get("X-Grain-Client"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	long := strings.Repeat("x", 400)
	cases := []struct {
		name, client, session, want string
		sent                        bool
	}{
		{name: "neither", sent: false},
		{name: "client and session", client: "jetski/1.4", session: "abc123", want: "jetski/1.4 session=abc123", sent: true},
		{name: "client alone", client: "jetski/1.4", want: "jetski/1.4", sent: true},
		{name: "session alone", session: "abc123", want: "session=abc123", sent: true},
		{name: "client already names a session", client: "jetski/1.4 session=s1", session: "s2", want: "jetski/1.4 session=s1", sent: true},
		{name: "control characters and newlines stripped", client: "jet\x01ski/1.4\r\nX-Evil: 1", session: "a\tb\x7f",
			want: "jetski/1.4 X-Evil: 1 session=a b", sent: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, present = nil, nil
			t.Setenv("GRAIN_CLIENT", tc.client)
			t.Setenv("GRAIN_CLIENT_SESSION", tc.session)
			c := NewHTTPClient(srv.URL)
			c.Token = "tok"
			if _, err := c.ListTasks(context.Background()); err != nil {
				t.Fatalf("ListTasks: %v", err)
			}
			if len(got) != 1 || present[0] != tc.sent || got[0] != tc.want {
				t.Fatalf("X-Grain-Client = %q (sent %v), want %q (sent %v)", got, present, tc.want, tc.sent)
			}
		})
	}
	t.Run("capped at 256 bytes", func(t *testing.T) {
		got, present = nil, nil
		t.Setenv("GRAIN_CLIENT", "tool/"+long)
		t.Setenv("GRAIN_CLIENT_SESSION", "abc")
		c := NewHTTPClient(srv.URL)
		c.Token = "tok"
		if _, err := c.ListTasks(context.Background()); err != nil {
			t.Fatalf("ListTasks: %v", err)
		}
		if len(got) != 1 || len(got[0]) > 256 || !strings.HasPrefix(got[0], "tool/xxx") {
			t.Fatalf("X-Grain-Client = %q (%d bytes), want a tool/... prefix of at most 256 bytes", got, len(got[0]))
		}
	})
}

func TestHTTPClient_ServiceAccountIAPSelfSignedJWT(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating RSA key: %v", err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshaling PKCS8 key: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})

	saEmail := "iap-caller@example-project.iam.gserviceaccount.com"
	saJSON, err := json.Marshal(map[string]string{
		"type":           "service_account",
		"client_email":   saEmail,
		"private_key":    string(pemBytes),
		"private_key_id": "key-id-42",
	})
	if err != nil {
		t.Fatal(err)
	}
	credFile := filepath.Join(t.TempDir(), "sa.json")
	if err := os.WriteFile(credFile, saJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", credFile)

	// 1. With Cloud Run Direct IAP wildcard audience ("https://<host>/*"),
	// HTTPClient signs an RS256 service-account JWT without calling ADC/gcloud.
	c := NewHTTPClient("https://grain-iap-1234.us-central1.run.app")
	c.Audience = "https://grain-iap-1234.us-central1.run.app/*"
	c.adcIDToken = noADC
	var gcloudCalls []string
	c.gcloud = gcloudStub("", "", &gcloudCalls)

	tok, err := c.getGoogleIDToken(context.Background())
	if err != nil {
		t.Fatalf("getGoogleIDToken failed: %v", err)
	}
	if len(gcloudCalls) != 0 {
		t.Fatalf("expected 0 gcloud calls, got %v", gcloudCalls)
	}

	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("expected 3 JWT segments, got %d", len(parts))
	}
	hdrBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decoding header: %v", err)
	}
	var hdr map[string]string
	if err := json.Unmarshal(hdrBytes, &hdr); err != nil {
		t.Fatalf("unmarshaling header: %v", err)
	}
	if hdr["alg"] != "RS256" || hdr["kid"] != "key-id-42" {
		t.Errorf("unexpected JWT header: %#v", hdr)
	}

	claimBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decoding claims: %v", err)
	}
	var claims struct {
		Iss string `json:"iss"`
		Sub string `json:"sub"`
		Aud string `json:"aud"`
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
	}
	if err := json.Unmarshal(claimBytes, &claims); err != nil {
		t.Fatalf("unmarshaling claims: %v", err)
	}
	if claims.Iss != saEmail || claims.Sub != saEmail {
		t.Errorf("got iss=%q sub=%q, want %q", claims.Iss, claims.Sub, saEmail)
	}
	if claims.Aud != c.Audience {
		t.Errorf("got aud=%q, want %q", claims.Aud, c.Audience)
	}
	if claims.Exp <= claims.Iat {
		t.Errorf("invalid iat=%d exp=%d", claims.Iat, claims.Exp)
	}

	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decoding signature: %v", err)
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&priv.PublicKey, crypto.SHA256, sum[:], sig); err != nil {
		t.Fatalf("RS256 signature verification failed: %v", err)
	}

	// Cached on repeat call.
	tok2, err := c.getGoogleIDToken(context.Background())
	if err != nil || tok2 != tok {
		t.Fatalf("second call got (%q, %v), want cached token", tok2, err)
	}

	// 2. Without trailing "/*", standard Cloud Run IAM path (adcIDToken) still wins.
	var adcCalled bool
	c2 := NewHTTPClient("https://grain-iam-1234.us-central1.run.app")
	c2.adcIDToken = func(_ context.Context, aud string) (*oauth2.Token, error) {
		adcCalled = true
		return &oauth2.Token{AccessToken: "oidc-id-token", Expiry: time.Now().Add(time.Hour)}, nil
	}
	got2, err := c2.getGoogleIDToken(context.Background())
	if err != nil || got2 != "oidc-id-token" || !adcCalled {
		t.Fatalf("non-wildcard audience got (%q, %v), adcCalled=%v; want oidc-id-token", got2, err, adcCalled)
	}
}

// TestRegisterCredentialSourceAndHTTPClientRefusesLoginRedirectAndHTML is
// HTTPClient's half of external-cli.spec.md#XC-4 and #XC-10 (pkg/cli's
// credsource_test.go is the CLI's): a context's credential is sent after
// the environment's and before the local API token; a redirect to a
// login page a wrapper's classifier knows is not followed; and an HTML
// page where JSON was asked for is a LoginRequiredError, not a parse
// error.
func TestRegisterCredentialSourceAndHTTPClientRefusesLoginRedirectAndHTML(t *testing.T) {
	for _, env := range TokenEnvVars {
		t.Setenv(env, "")
	}
	t.Setenv(LocalAPITokenFileEnv, filepath.Join(t.TempDir(), "absent"))

	var gotAuth atomic.Value
	login := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<html>sign in</html>")
	}))
	defer login.Close()
	var followed atomic.Bool
	unregister := authfix.RegisterRedirect("test-sso", func(u *url.URL) (authfix.Fix, bool) {
		if u.Host != strings.TrimPrefix(login.URL, "http://") {
			return authfix.Fix{}, false
		}
		followed.Store(true)
		return authfix.Fix{Summary: "session expired", Action: "run `renew-sso`"}, true
	})
	defer unregister()

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		switch r.URL.Query().Get("as") {
		case "redirect":
			http.Redirect(w, r, login.URL+"/signin?continue=x", http.StatusFound)
		case "html":
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, "<html>please sign in</html>")
		default:
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, "[]")
		}
	}))
	defer api.Close()

	c := NewHTTPClient(api.URL)
	c.RetryPolicy = &RetryPolicy{}
	c.ContextToken = func(context.Context) (string, error) { return "gma_fromcontext", nil }
	if _, err := c.ListTasks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := gotAuth.Load(); got != "Bearer gma_fromcontext" {
		t.Errorf("Authorization = %v, want the context's credential", got)
	}
	t.Setenv("GRAIN_TOKEN", "from-env")
	if _, err := c.ListTasks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := gotAuth.Load(); got != "Bearer from-env" {
		t.Errorf("Authorization = %v, want $GRAIN_TOKEN ahead of the context", got)
	}
	t.Setenv("GRAIN_TOKEN", "")

	var out []Task
	err := c.do(context.Background(), http.MethodGet, "/api/tasks?as=redirect", nil, &out)
	var lr *LoginRequiredError
	if !errors.As(err, &lr) || lr.Fix == "" || !followed.Load() {
		t.Fatalf("login redirect: %v, want a LoginRequiredError with the classifier's fix", err)
	}
	if want := "not signed in to 127.0.0.1: run `renew-sso`"; !strings.Contains(err.Error(), want) {
		t.Errorf("login redirect said %q, want it to contain %q", err, want)
	}

	err = c.do(context.Background(), http.MethodGet, "/api/tasks?as=html", nil, &out)
	if !errors.As(err, &lr) || !strings.Contains(err.Error(), "unexpected HTML sign-in response from "+api.URL) {
		t.Fatalf("HTML 401: %v, want an unexpected HTML sign-in response", err)
	}
}

// TestHTTPClient_IAPProxyAuthSendsAnIDTokenBesideTheCredential: a
// context's gma_ token behind IAP goes in Authorization with a Google ID
// token in Proxy-Authorization, which IAP takes in its place -- from
// $GRAIN_ID_TOKEN, which is then not the bearer, or else minted for the
// audience (docs/test-runs/2026-11-11-caller-identity.md).
func TestHTTPClient_IAPProxyAuthSendsAnIDTokenBesideTheCredential(t *testing.T) {
	for _, env := range TokenEnvVars {
		t.Setenv(env, "")
	}
	var gotAuth, gotProxy string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotProxy = r.Header.Get("Authorization"), r.Header.Get("Proxy-Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	newClient := func() *HTTPClient {
		c := NewHTTPClient(srv.URL)
		c.Audience, c.IAPProxyAuth = "iap-client-id", true
		c.ContextToken = func(context.Context) (string, error) { return "gma_personal", nil }
		return c
	}

	t.Setenv(IDTokenEnv, "id-token-from-env")
	if _, err := newClient().ListTasks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer gma_personal" || gotProxy != "Bearer id-token-from-env" {
		t.Errorf("with $%s: Authorization %q, Proxy-Authorization %q; want the gma_ token and the ID token beside it", IDTokenEnv, gotAuth, gotProxy)
	}

	t.Setenv(IDTokenEnv, "")
	var calls []string
	c := newClient()
	c.adcIDToken, c.gcloud = noADC, gcloudStub("minted-for-audience", "", &calls)
	if _, err := c.ListTasks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer gma_personal" || gotProxy != "Bearer minted-for-audience" {
		t.Errorf("without $%s: Authorization %q, Proxy-Authorization %q; want the gma_ token and a minted ID token", IDTokenEnv, gotAuth, gotProxy)
	}

	c = newClient()
	c.IAPProxyAuth = false
	if _, err := c.ListTasks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotProxy != "" {
		t.Errorf("without IAPProxyAuth: Proxy-Authorization %q; want none", gotProxy)
	}
}
