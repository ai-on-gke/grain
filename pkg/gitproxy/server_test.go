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
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// alwaysAuthorize is core_test.go's own concern turned off here on
// purpose: server_test.go only checks what NewHandler does with whatever
// GitProxy.Handle decides, not how the decision gets made.
type alwaysAuthorize struct{}

func (alwaysAuthorize) Authorize(context.Context, string, string, string, string) (bool, error) {
	return true, nil
}

func mustEmptyCredentialSet(t *testing.T) *CredentialSet {
	t.Helper()
	dir := writeCredentialSet(t, map[string]string{"*": "anonymous"}, nil)
	set, err := LoadCredentialSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func mustAlwaysAuthTokens(t *testing.T) *SandboxTokens {
	t.Helper()
	path := writeTokensFile(t, t.TempDir(), map[string]string{"sandbox-0": "tok0"})
	tokens, err := LoadSandboxTokens(path)
	if err != nil {
		t.Fatal(err)
	}
	return tokens
}

// serveScriptedResponse boots a real HTTP server over NewHandler, with a
// FakeForwarder scripted to answer with response -- proving the
// transport (path/query split, body read, header handling) end to end,
// since GitProxy.Handle's own decision logic is already core_test.go's
// job.
func serveScriptedResponse(t *testing.T, response ProxyResponse) *httptest.Server {
	t.Helper()
	proxy := &GitProxy{
		Authorizer:      alwaysAuthorize{},
		Credentials:     mustEmptyCredentialSet(t),
		TeamCredentials: ladderConnections{creds: mustEmptyCredentialSet(t)},
		Tokens:          mustAlwaysAuthTokens(t),
		Forwarder:       &FakeForwarder{Response: UpstreamResponse{Status: response.Status, Headers: response.Headers, Body: response.Body}},
	}
	return httptest.NewServer(NewHandler(proxy))
}

func newGitRequest(t *testing.T, method, url string, body io.Reader) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("User-Agent", "git/2.39.2")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Authorization", basicAuthHeader("tok0"))
	return req
}

func TestHandlerSplitsPathAndQueryAndStreamsTheResponseBack(t *testing.T) {
	server := serveScriptedResponse(t, ProxyResponse{Status: 200, Headers: map[string]string{"X-Test": "yes"}, Body: []byte("hello")})
	defer server.Close()

	resp, err := http.DefaultClient.Do(newGitRequest(t, "GET", server.URL+"/acme/widgets.git/info/refs?service=git-upload-pack", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != "hello" || resp.Header.Get("X-Test") != "yes" {
		t.Fatalf("status=%d body=%q x-test=%q", resp.StatusCode, body, resp.Header.Get("X-Test"))
	}
}

func TestHandlerForwardsTheRequestBodyOnPost(t *testing.T) {
	server := serveScriptedResponse(t, ProxyResponse{Status: 200, Body: []byte("ok")})
	defer server.Close()

	req := newGitRequest(t, "POST", server.URL+"/acme/widgets.git/git-upload-pack", strings.NewReader("the request body"))
	req.Header.Set("Accept", "application/x-git-upload-pack-result")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Fatalf("body = %q", body)
	}
}

func TestHandlerRecomputesContentLengthAndDropsHopByHopHeaders(t *testing.T) {
	server := serveScriptedResponse(t, ProxyResponse{
		Status: 200,
		Headers: map[string]string{
			"Content-Length": "999", "Connection": "close",
			"Transfer-Encoding": "chunked", "X-Keep": "1",
		},
		Body: []byte("body"),
	})
	defer server.Close()

	resp, err := http.DefaultClient.Do(newGitRequest(t, "GET", server.URL+"/a/b.git/info/refs", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Length") != "4" {
		t.Errorf("Content-Length = %q, want 4", resp.Header.Get("Content-Length"))
	}
	if resp.Header.Get("X-Keep") != "1" {
		t.Error("expected X-Keep to survive")
	}
}

// 500 rather than the 403 this used to script: the transport is what is
// under test here, and 401/403/404 from upstream no longer reach it
// verbatim -- Handle rewrites those into an explanation naming the
// credential (refusals.go), which refusals_test.go asserts on directly.
func TestHandlerSurfacesANonOKStatusFromTheProxy(t *testing.T) {
	server := serveScriptedResponse(t, ProxyResponse{Status: 500, Body: []byte("denied")})
	defer server.Close()

	resp, err := http.DefaultClient.Do(newGitRequest(t, "GET", server.URL+"/a/b.git/info/refs", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 500 || string(body) != "denied" {
		t.Errorf("status=%d body=%q", resp.StatusCode, body)
	}
}
