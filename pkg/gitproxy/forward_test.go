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

package gitproxy

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func strPtr(s string) *string { return &s }

func TestRealForwarderSendsMethodPathQueryAndBody(t *testing.T) {
	var gotMethod, gotPath, gotQuery string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotQuery = r.Method, r.URL.Path, r.URL.RawQuery
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("X-Test", "yes")
		w.WriteHeader(201)
		w.Write([]byte("upstream body"))
	}))
	defer server.Close()

	f := &RealForwarder{Host: server.Listener.Addr().String(), UseTLS: false}
	resp, err := f.Forward("POST", "/acme/widgets.git/git-upload-pack", "service=git-upload-pack",
		map[string]string{"Content-Type": "application/x-git-upload-pack-request"},
		[]byte("the body"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" || gotPath != "/acme/widgets.git/git-upload-pack" || gotQuery != "service=git-upload-pack" {
		t.Errorf("got method=%q path=%q query=%q", gotMethod, gotPath, gotQuery)
	}
	if string(gotBody) != "the body" {
		t.Errorf("got body = %q", gotBody)
	}
	body, err := io.ReadAll(resp.Stream)
	resp.Stream.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != 201 || string(body) != "upstream body" || resp.Headers["X-Test"] != "yes" {
		t.Errorf("resp = %+v, body = %q", resp, body)
	}
}

func TestRealForwarderSetsBasicAuthFromToken(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
	}))
	defer server.Close()

	f := &RealForwarder{Host: server.Listener.Addr().String(), UseTLS: false}
	resp, err := f.Forward("GET", "/x", "", nil, nil, strPtr("secret-token"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Stream.Close()
	username, password, ok := parseBasicAuthHeader(gotAuth)
	if !ok || username != "secret-token" || password != "" {
		t.Errorf("got auth = %q (user=%q pass=%q ok=%v)", gotAuth, username, password, ok)
	}
}

func TestRealForwarderSendsNoAuthorizationWithoutAToken(t *testing.T) {
	var sawAuth bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization") != ""
	}))
	defer server.Close()

	f := &RealForwarder{Host: server.Listener.Addr().String(), UseTLS: false}
	resp, err := f.Forward("GET", "/x", "", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Stream.Close()
	if sawAuth {
		t.Error("expected no Authorization header without a token")
	}
}

func TestRealForwarderStream(t *testing.T) {
	var gotBody []byte
	var gotContentLength int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotContentLength = r.ContentLength
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	f := &RealForwarder{Host: server.Listener.Addr().String(), UseTLS: false}
	bodyData := []byte("streamed data payload")
	resp, err := f.ForwardStream("POST", "/test/repo.git/git-receive-pack", "", nil, bytes.NewReader(bodyData), int64(len(bodyData)), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Stream.Close()

	if string(gotBody) != string(bodyData) {
		t.Errorf("got body %q, want %q", gotBody, bodyData)
	}
	if gotContentLength != int64(len(bodyData)) {
		t.Errorf("got Content-Length %d, want %d", gotContentLength, len(bodyData))
	}
}

func TestFakeForwarderRecordsCallsAndReplaysAScriptedResponse(t *testing.T) {
	fake := &FakeForwarder{Response: UpstreamResponse{Status: 200, Body: []byte("hi")}}
	resp, err := fake.Forward("GET", "/a/b.git/info/refs", "service=git-upload-pack",
		map[string]string{"User-Agent": "git/2.40"}, nil, strPtr("tok"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != 200 || string(resp.Body) != "hi" {
		t.Errorf("resp = %+v", resp)
	}
	if len(fake.Calls) != 1 || fake.Calls[0].Path != "/a/b.git/info/refs" || *fake.Calls[0].Token != "tok" {
		t.Errorf("Calls = %+v", fake.Calls)
	}
}

func parseBasicAuthHeader(header string) (username, password string, ok bool) {
	req := &http.Request{Header: http.Header{"Authorization": []string{header}}}
	return req.BasicAuth()
}

// A redirect -- even to another port on the same hostname, which net/http
// would otherwise follow with Authorization still attached -- is handed
// back to the caller unfollowed (grain/issue-2417).
func TestRealForwarderDoesNotFollowRedirects(t *testing.T) {
	var targetHit bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHit = true
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/elsewhere", http.StatusFound)
	}))
	defer origin.Close()

	for _, client := range []*http.Client{nil, {}} {
		targetHit = false
		f := &RealForwarder{Host: origin.Listener.Addr().String(), UseTLS: false, Client: client}
		resp, err := f.Forward("GET", "/acme/repo.git/info/refs", "service=git-upload-pack", nil, nil, strPtr("secret-token"))
		if err != nil {
			t.Fatal(err)
		}
		resp.Stream.Close()
		if targetHit {
			t.Errorf("client %v: redirect was followed to %s", client, target.URL)
		}
		if resp.Status != http.StatusFound || resp.Headers["Location"] != target.URL+"/elsewhere" {
			t.Errorf("client %v: resp = status %d Location %q, want the 302 itself", client, resp.Status, resp.Headers["Location"])
		}
	}
}

func TestDefaultForwardClientHasTimeouts(t *testing.T) {
	if defaultForwardClient.Timeout == 0 {
		t.Error("default forward client has no overall timeout")
	}
	transport, ok := defaultForwardClient.Transport.(*http.Transport)
	if !ok || transport.ResponseHeaderTimeout == 0 {
		t.Error("default forward client has no response header timeout")
	}
}

func TestValidateForwardTarget(t *testing.T) {
	for _, tc := range []struct {
		host   string
		useTLS bool
		ok     bool
	}{
		{"github.com", true, true},
		{"ghe.example.com", true, true},
		{"ghe.example.com:8443", true, true},
		{"127.0.0.1:8080", false, true},
		{"localhost:8080", false, true},
		{"[::1]:8080", false, true},
		// A custom host is fine; a custom host spoken to in the clear is
		// not, since the credential would cross the network readable.
		{"ghe.example.com", false, false},
		{"10.0.0.5:8080", false, false},
		{"localhost.evil.example", false, false},
		// Anything that is not a bare host[:port] could make Forward's URL
		// name a different host than the one configured.
		{"github.com@evil.example", true, false},
		{"evil.example/github.com", true, false},
		{"evil.example?github.com", true, false},
		{"evil.example#github.com", true, false},
		{"https://github.com", true, false},
		{"", true, false},
		{":443", true, false},
	} {
		err := ValidateForwardTarget(tc.host, tc.useTLS)
		if (err == nil) != tc.ok {
			t.Errorf("ValidateForwardTarget(%q, %v) = %v, want ok=%v", tc.host, tc.useTLS, err, tc.ok)
		}
	}
}

func TestRealForwarderRefusesInvalidTargetWithoutSending(t *testing.T) {
	for _, f := range []*RealForwarder{
		{Host: "ghe.example.com", UseTLS: false},
		{Host: "github.com@evil.example", UseTLS: true},
	} {
		if _, err := f.Forward("GET", "/acme/widgets.git/info/refs", "service=git-upload-pack",
			map[string]string{}, nil, strPtr("ghp_secret")); err == nil {
			t.Errorf("Forward to %q (tls=%v) succeeded, want it refused", f.Host, f.UseTLS)
		}
	}
}

func TestBuildProxyRefusesInsecureNonLoopbackHost(t *testing.T) {
	_, err := BuildProxy(BuildConfig{DataDir: t.TempDir(), ForwardHost: "ghe.example.com", ForwardTLS: false})
	if err == nil {
		t.Fatal("BuildProxy accepted plain HTTP to a non-loopback host")
	}
}
