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
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"

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
