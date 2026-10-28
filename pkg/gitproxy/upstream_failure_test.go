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
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// The single-tenant walkthrough's B8: a forward that failed answered a
// bare "upstream request failed", so git's "returned error: 502" was all
// the run's log carried. The reason now rides in the body, which git
// prints as "remote: ...".
func TestAFailedForwardSaysWhyInTheDenialBody(t *testing.T) {
	why := `gitproxy: refusing to forward to "172.17.0.5:8088" over plain HTTP: ` +
		`that is only allowed to a loopback address (a local mock); use HTTPS for any other host`
	proxy := &GitProxy{
		Authorizer:      alwaysAuthorize{},
		Credentials:     mustEmptyCredentialSet(t),
		TeamCredentials: ladderConnections{creds: mustEmptyCredentialSet(t)},
		Tokens:          mustAlwaysAuthTokens(t),
		Forwarder:       &FakeForwarder{Err: errors.New(why)},
	}
	server := httptest.NewServer(NewHandler(proxy))
	defer server.Close()

	req := newGitRequest(t, "GET", server.URL+"/acme/widgets.git/info/refs?service=git-upload-pack", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 502 {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
	if !strings.Contains(string(body), why) {
		t.Errorf("body = %q, want it to carry %q", body, why)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain so git prints the body", ct)
	}
}

func TestUpstreamFailedDenialCutsTheTokenOut(t *testing.T) {
	token := "s3cret-token"
	denial := upstreamFailedDenial(errors.New("Get \"http://walker:s3cret-token@git.example/o/r.git\": refused"),
		Credential{Name: "plain", Token: &token})
	if strings.Contains(string(denial.Body), token) {
		t.Errorf("denial body %q carries the token", denial.Body)
	}
	if !strings.Contains(string(denial.Body), "refused") {
		t.Errorf("denial body %q lost the reason", denial.Body)
	}
}

// B8's first half: a plain-http base URL off loopback was saved, and the
// proxy then refused every request to it. It is refused when saved, with
// the proxy's own sentence; loopback (a local mock) still saves.
func TestAPlainHTTPBaseURLOffLoopbackIsRefusedWhenSaved(t *testing.T) {
	fakeCredentialHelperGit(t, "http", "172.17.0.5:8088")
	c, _ := ambientSet(t)
	remote := CredentialHost{Host: "172.17.0.5:8088", Kind: model.GitHostGit, BaseURL: "http://172.17.0.5:8088"}
	want := `refusing to forward to "172.17.0.5:8088" over plain HTTP: ` +
		`that is only allowed to a loopback address (a local mock); use HTTPS for any other host`
	if err := c.SetAmbientOn("plain", remote, "git"); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("SetAmbientOn = %v; want %q", err, want)
	}
	if _, ok := c.AmbientOf("plain"); ok {
		t.Error("the refused ambient credential was written anyway")
	}
	if err := c.SetToken("pat", "secret"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetHost("pat", remote); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("SetHost = %v; want %q", err, want)
	}
	if err := c.SetHost("pat", CredentialHost{Host: "127.0.0.1:8088", Kind: model.GitHostGit, BaseURL: "http://127.0.0.1:8088"}); err != nil {
		t.Errorf("SetHost on loopback = %v; want it saved", err)
	}
	if err := c.SetHost("pat", CredentialHost{Host: "172.17.0.5:8088", Kind: model.GitHostGit, BaseURL: "https://172.17.0.5:8088"}); err != nil {
		t.Errorf("SetHost over https = %v; want it saved", err)
	}
}
