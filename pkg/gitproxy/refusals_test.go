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

// What a sandbox is told when GitHub refuses the credential the proxy
// forwarded with, for the two statuses that were passed straight through
// until this file existed. The 404 half lives in notfound_test.go, next
// to the wording it has always had.
//
// Two layers, deliberately. The tests up to TestASuccessfulResponse...
// script an upstream status directly, which is the only way to state
// "this status produces this sentence" as a fact about the proxy. The
// ones after it run the same classification against
// githubsim.GitUpstream over real HTTP, so that what is being classified
// is what github.com actually answers rather than what this file
// remembers about it -- the gap that let 401 and 403 go unhandled in the
// first place, since every failure any double here modelled was a 404.

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/github/githubsim"
)

// upstreamHeaders is what GitHub sends with these refusals -- kept on
// the scripted responses so the tests also prove the substitution
// replaces them (git prints no body it is not told is text).
var upstreamHeaders = map[string]string{"Content-Type": "text/plain; charset=utf-8"}

func fetchWith(t *testing.T, p *GitProxy) ProxyResponse {
	t.Helper()
	headers := merge(gitHeaders, map[string]string{"Authorization": basicAuthHeader("tok0")})
	return p.Handle(context.Background(), "GET", "/owner/repo.git/info/refs",
		"service=git-upload-pack", headers, nil)
}

func pushWith(t *testing.T, p *GitProxy) ProxyResponse {
	t.Helper()
	headers := merge(gitHeaders, map[string]string{
		"Authorization": basicAuthHeader("tok0"),
		"Accept":        "application/x-git-receive-pack-result",
	})
	return p.Handle(context.Background(), "POST", "/owner/repo.git/git-receive-pack", "", headers, nil)
}

func mustContain(t *testing.T, body string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(body, want) {
			t.Errorf("body = %q, want it to mention %s", body, want)
		}
	}
}

// A 401 is about the credential and not about the repository, so the
// message has to say a different thing from the 404's -- and it must not
// reach the sandbox as a 401.
func TestAnUpstream401NamesTheCredentialGitHubRefused(t *testing.T) {
	p, forwarder, audit := newTestProxy(t)
	forwarder.Response = UpstreamResponse{
		Status: 401, Headers: upstreamHeaders,
		Body: []byte("Invalid username or token. Password authentication is not supported for Git operations.\n"),
	}
	resp := fetchWith(t, p)
	body := string(resp.Body)
	mustContain(t, body, "owner/repo", `"bot"`, "401", "Git connections",
		"Invalid username or token")
	if ct := resp.Headers["Content-Type"]; !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", ct)
	}
	// Audited as what upstream said, not as what the sandbox was told:
	// this substitutes the explanation, not the outcome.
	if audit.Entries[0].Outcome != "forwarded: 401" {
		t.Errorf("outcome = %q, want the forwarded status recorded as it was", audit.Entries[0].Outcome)
	}
	if len(forwarder.Calls) != 1 {
		t.Errorf("forwarded %d times, want 1", len(forwarder.Calls))
	}
}

// The sandbox's own credential is the proxy token, and it was accepted:
// answering 401 would make git reject and (with credential.helper=store)
// erase it, so every later git command in that run fails asking for a
// username instead of printing the sentence above.
func TestAnUpstream401IsNotPassedOnAsA401(t *testing.T) {
	p, _, _ := newTestProxy(t)
	p.Forwarder.(*FakeForwarder).Response = UpstreamResponse{Status: 401, Body: []byte("Invalid username or token.")}
	if resp := fetchWith(t, p); resp.Status != 403 {
		t.Errorf("status = %d, want 403 -- the sandbox's own credential is not the refused one", resp.Status)
	}
}

func TestAnUpstream401UnderNoCredentialSaysSo(t *testing.T) {
	dir := writeCredentialSet(t, map[string]string{"*": "anonymous"}, nil)
	credentials, err := LoadCredentialSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, forwarder, _ := newTestProxy(t)
	p.Credentials = credentials
	forwarder.Response = UpstreamResponse{Status: 401, Body: []byte("No anonymous write access.")}
	body := string(pushWith(t, p).Body)
	mustContain(t, body, "no credential at all", "No anonymous write access")
	// GitHub had no credential to refuse: an anonymous request is told
	// it reaches public repositories only, not sent after an expired
	// token (docs/test-runs/2026-10-14-teams-server-fixes.md, F2).
	mustContain(t, body, "GitHub asked for a credential (401)", "None (public)")
	if strings.Contains(body, "expired, revoked or mistyped") {
		t.Errorf("an anonymous request's 401 blames a token nobody sent: %s", body)
	}
	if !IsAccessRefusal(body) {
		t.Errorf("IsAccessRefusal(%q) = false; the dispatcher would retry it to the failure limit", body)
	}
}

// A 403 on a push is a permission the credential was never granted; a
// 403 on a fetch is a credential an organization holds back. They send
// an operator to two different settings pages, so they are two messages.
func TestAnUpstream403OnAPushSaysTheCredentialMayNotWrite(t *testing.T) {
	p, forwarder, _ := newTestProxy(t)
	forwarder.Response = UpstreamResponse{
		Status: 403, Headers: upstreamHeaders,
		Body: []byte("Write access to repository not granted."),
	}
	resp := pushWith(t, p)
	if resp.Status != 403 {
		t.Fatalf("status = %d, want 403", resp.Status)
	}
	mustContain(t, string(resp.Body), "owner/repo", `"bot"`, "write access", "Contents: write")
	if strings.Contains(string(resp.Body), "read access") {
		t.Errorf("body = %q, want it to say nothing about read access on a push", resp.Body)
	}
}

// The info/refs half of a push is a GET like a fetch's, and only its
// service parameter tells them apart.
func TestAnUpstream403OnAPushsInfoRefsIsAlsoAWriteRefusal(t *testing.T) {
	p, forwarder, _ := newTestProxy(t)
	forwarder.Response = UpstreamResponse{Status: 403, Body: []byte("Write access to repository not granted.")}
	headers := merge(gitHeaders, map[string]string{"Authorization": basicAuthHeader("tok0")})
	resp := p.Handle(context.Background(), "GET", "/owner/repo.git/info/refs",
		"service=git-receive-pack", headers, nil)
	mustContain(t, string(resp.Body), "write access")
}

func TestAnUpstream403OnAFetchSaysTheCredentialMayNotRead(t *testing.T) {
	p, forwarder, _ := newTestProxy(t)
	sso := "The 'owner' organization has enabled or enforced SAML SSO. To access this repository, " +
		"visit https://github.com/orgs/owner/sso and try again."
	forwarder.Response = UpstreamResponse{Status: 403, Headers: upstreamHeaders, Body: []byte(sso)}
	resp := fetchWith(t, p)
	if resp.Status != 403 {
		t.Fatalf("status = %d, want 403", resp.Status)
	}
	// GitHub's own sentence carries the URL a human has to visit, which
	// nothing on this side of the proxy could have known: quoted back
	// rather than dropped for the explanation.
	mustContain(t, string(resp.Body), "owner/repo", `"bot"`, "read access", "SSO",
		"https://github.com/orgs/owner/sso")
}

// The quote is of GitHub's sentence, not of whatever bytes came back:
// an HTML error page or a packfile fragment in a git "remote:" line is
// noise, and nothing has validated this response.
func TestOnlyAPlainSentenceFromGitHubIsQuotedBack(t *testing.T) {
	for name, body := range map[string]string{
		"html":    "<html><body>403 Forbidden</body></html>",
		"long":    strings.Repeat("x", 400),
		"binary":  "0000\x00\x01\x02PACK",
		"nothing": "   ",
	} {
		t.Run(name, func(t *testing.T) {
			p, forwarder, _ := newTestProxy(t)
			forwarder.Response = UpstreamResponse{Status: 403, Body: []byte(body)}
			if got := string(fetchWith(t, p).Body); strings.Contains(got, "GitHub said") {
				t.Errorf("body = %q, want no quote of %s", got, name)
			}
		})
	}
}

// Everything that is not one of the three refusals is still GitHub's own
// response, headers and all -- a fetch that works is a packfile, and a
// 500 is upstream's to explain.
func TestASuccessfulResponseAndAnUpstream500AreUntouched(t *testing.T) {
	for _, status := range []int{200, 304, 500, 502} {
		p, forwarder, _ := newTestProxy(t)
		forwarder.Response = UpstreamResponse{
			Status:  status,
			Headers: map[string]string{"Content-Type": "application/x-git-upload-pack-advertisement"},
			Body:    []byte("upstream's own body"),
		}
		resp := fetchWith(t, p)
		if resp.Status != status || string(resp.Body) != "upstream's own body" ||
			resp.Headers["Content-Type"] != "application/x-git-upload-pack-advertisement" {
			t.Errorf("%d: resp = %+v, want it passed through verbatim", status, resp)
		}
	}
}

// The same classification, against the answers github.com itself gives
// (githubsim.GitUpstream, whose doc comment records where each of them
// was measured), reached over real HTTP through the real forwarder.
func proxyOnto(t *testing.T, upstream *githubsim.GitUpstream) *GitProxy {
	t.Helper()
	server := httptest.NewServer(upstream)
	t.Cleanup(server.Close)
	p, _, _ := newTestProxy(t)
	// newTestProxy's ladder sends every repo under the "bot" credential,
	// whose token is bot-token -- which is the token GitUpstream sees as
	// the Basic auth username, since that is where RealForwarder puts it.
	p.Forwarder = &RealForwarder{Host: server.Listener.Addr().String(), UseTLS: false}
	return p
}

func TestARevokedCredentialAgainstTheSimulatedGitHubIsExplained(t *testing.T) {
	p := proxyOnto(t, &githubsim.GitUpstream{
		Tokens: map[string]githubsim.GitAccess{"some-other-token": {Read: []string{"owner/repo"}}},
	})
	resp := fetchWith(t, p)
	if resp.Status != 403 {
		t.Fatalf("status = %d, want 403", resp.Status)
	}
	mustContain(t, string(resp.Body), "refused the credential itself (401)", `"bot"`,
		"Invalid username or token")
}

func TestAReadOnlyCredentialPushingAgainstTheSimulatedGitHubIsExplained(t *testing.T) {
	p := proxyOnto(t, &githubsim.GitUpstream{
		Tokens: map[string]githubsim.GitAccess{"bot-token": {Read: []string{"owner/repo"}}},
	})
	resp := pushWith(t, p)
	if resp.Status != 403 {
		t.Fatalf("status = %d, want 403", resp.Status)
	}
	mustContain(t, string(resp.Body), "refused it write access (403)", `"bot"`,
		"Write access to repository not granted")
}

func TestACredentialAnOrganizationHoldsBackIsExplained(t *testing.T) {
	p := proxyOnto(t, &githubsim.GitUpstream{
		Tokens: map[string]githubsim.GitAccess{"bot-token": {
			Read:            []string{"owner/repo"},
			SSOUnauthorized: []string{"owner/repo"},
		}},
	})
	resp := fetchWith(t, p)
	if resp.Status != 403 {
		t.Fatalf("status = %d, want 403", resp.Status)
	}
	mustContain(t, string(resp.Body), "refused it read access (403)", "SAML SSO",
		"https://github.com/orgs/owner/sso")
}

// The narrow case the 404 wording was always about, still reached the
// same way: a credential GitHub accepts, against a repository it cannot
// see.
func TestACredentialThatCannotSeeTheRepoStillGetsTheNotFoundWording(t *testing.T) {
	p := proxyOnto(t, &githubsim.GitUpstream{
		Tokens: map[string]githubsim.GitAccess{"bot-token": {Read: []string{"owner/something-else"}}},
	})
	resp := fetchWith(t, p)
	if resp.Status != 404 {
		t.Fatalf("status = %d, want 404", resp.Status)
	}
	mustContain(t, string(resp.Body), "GitHub answered 404", `"bot"`, "Git connections")
}
