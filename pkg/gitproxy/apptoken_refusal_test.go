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

// What a sandbox is told when the credential covering its repo is a
// GitHub App installation this deployment can no longer mint a token
// for (grain/task-158).
//
// The statuses scripted here are the ones task 123's hand run measured
// against github.com for an *unauthenticated* request, which is what
// these requests are: the ladder resolved the repo to an App credential,
// minting failed, and load fell back to anonymous. GitHub answers a
// private repo's fetch 401 with an empty body, a push 401 with "No
// anonymous write access.", and neither sentence is about the thing that
// broke.

import (
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/github"
)

// newAppProxy is newTestProxy with the "bot" credential replaced by an
// App-backed one whose token exchange GitHub refuses -- suspended, in
// this case, which is the failure with the clearest sentence behind it.
func newAppProxy(t *testing.T, mint github.ApiResponse) (*GitProxy, *FakeForwarder) {
	t.Helper()
	credsDir := writeCredentialSet(t, map[string]string{"*": "ci-app"}, nil)
	writeAppCredential(t, credsDir, "ci-app")
	credentials, err := LoadCredentialSet(credsDir)
	if err != nil {
		t.Fatal(err)
	}
	credentials.AppTransport = github.NewFakeTransport(mint, mint, mint)

	tokensPath := writeTokensFile(t, t.TempDir(), map[string]string{"sandbox-0": "tok0"})
	tokens, err := LoadSandboxTokens(tokensPath)
	if err != nil {
		t.Fatal(err)
	}
	forwarder := &FakeForwarder{Response: UpstreamResponse{Status: 200, Body: []byte("ok")}}
	proxy := &GitProxy{
		Authorizer:      stubAuthorizer{allow: map[[3]string]bool{{"sandbox-0", "owner", "repo"}: true}},
		Credentials:     credentials,
		TeamCredentials: ladderConnections{creds: credentials},
		Tokens:          tokens,
		Forwarder:       forwarder,
	}
	return proxy, forwarder
}

var suspendedMint = github.ApiResponse{
	Status: 403, Body: []byte(`{"message":"This installation has been suspended"}`),
}

// The push case: GitHub's own body is "No anonymous write access.", a
// sentence about a credential the ladder believes it sent. What the
// sandbox is told instead has to name the App.
func TestAPushOnAnUnmintableAppCredentialNamesTheApp(t *testing.T) {
	p, forwarder := newAppProxy(t, suspendedMint)
	forwarder.Response = UpstreamResponse{
		Status: 401, Headers: upstreamHeaders, Body: []byte("No anonymous write access.\n"),
	}
	resp := pushWith(t, p)
	body := string(resp.Body)
	mustContain(t, body, "owner/repo", `"ci-app"`, "GitHub App",
		"This installation has been suspended", "ci-app.app.json")
	// The same 403-for-a-401 substitution the ordinary refusal makes,
	// and for the same reason: a 401 costs the sandbox its proxy token.
	if resp.Status != 403 {
		t.Errorf("status = %d, want 403 rather than a 401 git would erase its own credential over", resp.Status)
	}
	if strings.Contains(body, "expired, revoked or mistyped token") {
		t.Errorf("body sends the operator after a token that was never sent: %q", body)
	}
}

// The private-repo fetch: 401 with nothing in it at all, which is the
// case with no vendor sentence to fall back on.
func TestAFetchOnAnUnmintableAppCredentialNamesTheApp(t *testing.T) {
	p, forwarder := newAppProxy(t, suspendedMint)
	forwarder.Response = UpstreamResponse{Status: 401, Headers: upstreamHeaders}
	resp := fetchWith(t, p)
	mustContain(t, string(resp.Body), "owner/repo", `"ci-app"`,
		"no installation token could be minted", "This installation has been suspended")
	if resp.Status != 403 {
		t.Errorf("status = %d, want 403", resp.Status)
	}
}

// A 404 keeps its own status downstream -- what git should do about it
// has not changed, only what it prints.
func TestA404OnAnUnmintableAppCredentialKeepsItsStatusAndNamesTheApp(t *testing.T) {
	p, forwarder := newAppProxy(t, suspendedMint)
	forwarder.Response = UpstreamResponse{
		Status: 404, Headers: upstreamHeaders, Body: []byte("Repository not found.\n"),
	}
	resp := fetchWith(t, p)
	if resp.Status != 404 {
		t.Errorf("status = %d, want the 404 left alone", resp.Status)
	}
	mustContain(t, string(resp.Body), "no installation token could be minted", "ci-app.app.json")
}

// A credential that minted fine is none of this file's business: the
// ordinary refusals still explain a credential GitHub actually saw.
func TestAWorkingAppCredentialStillGetsTheOrdinaryRefusal(t *testing.T) {
	p, forwarder := newAppProxy(t, mintedTokenResponse("ghs_minted", "2099-01-01T00:00:00Z"))
	forwarder.Response = UpstreamResponse{
		Status: 401, Headers: upstreamHeaders, Body: []byte("Invalid username or token.\n"),
	}
	body := string(fetchWith(t, p).Body)
	if strings.Contains(body, "no installation token could be minted") {
		t.Errorf("a credential that minted fine is reported as unmintable: %q", body)
	}
	mustContain(t, body, "refused the credential itself (401)")
}

// The failure with no other trace: a public repo reads perfectly well
// anonymously, so nothing is refused, nothing reaches the sandbox, and
// the deployment goes on working until the first push. The audit log is
// the only place that request is written down, so it has to say the
// credential beside it is not what went out.
func TestASuccessfulAnonymousReadIsAuditedAsUnauthenticated(t *testing.T) {
	p, forwarder := newAppProxy(t, suspendedMint)
	audit := &RecordingAuditLog{}
	p.Audit = audit
	forwarder.Response = UpstreamResponse{Status: 200, Body: []byte("ok")}

	if resp := fetchWith(t, p); resp.Status != 200 {
		t.Fatalf("status = %d, want the public read to still succeed", resp.Status)
	}
	if len(audit.Entries) != 1 {
		t.Fatalf("recorded %d entries, want one", len(audit.Entries))
	}
	outcome := audit.Entries[0].Outcome
	if !strings.Contains(outcome, "200") || !strings.Contains(outcome, "unauthenticated") {
		t.Errorf("outcome = %q, want a forwarded 200 that says the App credential minted nothing", outcome)
	}
}

func TestAnAnonymousLadderEntryIsNotReportedAsAMintFailure(t *testing.T) {
	p, forwarder, _ := newTestProxy(t)
	forwarder.Response = UpstreamResponse{Status: 401, Headers: upstreamHeaders}
	body := string(fetchWith(t, p).Body)
	if strings.Contains(body, "no installation token could be minted") {
		t.Errorf("an ordinary credential's refusal is reported as a mint failure: %q", body)
	}
}
