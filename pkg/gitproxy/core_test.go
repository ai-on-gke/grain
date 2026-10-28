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
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

var gitHeaders = map[string]string{"User-Agent": "git/2.39.2", "Accept": "*/*"}

func basicAuthHeader(token string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte("x:"+token))
}

// stubAuthorizer authorizes exactly the (sandbox, owner, repo) tuples
// listed in allow, regardless of action -- core_test.go's own concern is
// what GitProxy.Handle does with an Authorizer's answer, not how a real
// one computes it (authorize_test.go covers that).
type stubAuthorizer struct {
	allow map[[3]string]bool
}

func (s stubAuthorizer) Authorize(_ context.Context, sandbox, owner, repo, _ string) (bool, error) {
	return s.allow[[3]string{sandbox, owner, repo}], nil
}

func newTestProxy(t *testing.T) (*GitProxy, *FakeForwarder, *RecordingAuditLog) {
	t.Helper()
	credsDir := writeCredentialSet(t, map[string]string{"*": "bot"}, map[string]string{"bot": "bot-token"})
	credentials, err := LoadCredentialSet(credsDir)
	if err != nil {
		t.Fatal(err)
	}
	tokensPath := writeTokensFile(t, t.TempDir(), map[string]string{"sandbox-0": "tok0"})
	tokens, err := LoadSandboxTokens(tokensPath)
	if err != nil {
		t.Fatal(err)
	}

	forwarder := &FakeForwarder{Response: UpstreamResponse{Status: 200, Headers: map[string]string{"Content-Type": "x"}, Body: []byte("ok")}}
	audit := &RecordingAuditLog{}
	proxy := &GitProxy{
		Authorizer:  stubAuthorizer{allow: map[[3]string]bool{{"sandbox-0", "owner", "repo"}: true}},
		Credentials: credentials,
		Tokens:      tokens,
		Forwarder:   forwarder,
		Audit:       audit,
	}
	proxy.TeamCredentials = ladderConnections{proxy: proxy}
	return proxy, forwarder, audit
}

func TestHandleUnknownPathIs404(t *testing.T) {
	p, forwarder, _ := newTestProxy(t)
	resp := p.Handle(context.Background(), "GET", "/owner/repo.git/HEAD", "", gitHeaders, nil)
	if resp.Status != 404 {
		t.Errorf("status = %d, want 404", resp.Status)
	}
	if len(forwarder.Calls) != 0 {
		t.Error("expected no forward call")
	}
}

func TestHandleNonGitUserAgentIs404(t *testing.T) {
	p, _, _ := newTestProxy(t)
	resp := p.Handle(context.Background(), "GET", "/owner/repo.git/info/refs", "",
		map[string]string{"User-Agent": "Mozilla/5.0"}, nil)
	if resp.Status != 404 {
		t.Errorf("status = %d, want 404", resp.Status)
	}
}

func TestHandleMissingAuthIs401BeforeAuthorizationIsConsulted(t *testing.T) {
	p, forwarder, audit := newTestProxy(t)
	resp := p.Handle(context.Background(), "GET", "/owner/repo.git/info/refs",
		"service=git-upload-pack", gitHeaders, nil)
	if resp.Status != 401 {
		t.Errorf("status = %d, want 401", resp.Status)
	}
	if _, ok := resp.Headers["WWW-Authenticate"]; !ok {
		t.Error("expected a WWW-Authenticate header")
	}
	if len(forwarder.Calls) != 0 {
		t.Error("expected no forward call")
	}
	if len(audit.Entries) != 0 {
		t.Error("unauthenticated callers should leave no audit trace either way")
	}
}

func TestHandleUnknownTokenIs401(t *testing.T) {
	p, _, _ := newTestProxy(t)
	headers := merge(gitHeaders, map[string]string{"Authorization": basicAuthHeader("not-a-real-token")})
	resp := p.Handle(context.Background(), "GET", "/owner/repo.git/info/refs",
		"service=git-upload-pack", headers, nil)
	if resp.Status != 401 {
		t.Errorf("status = %d, want 401", resp.Status)
	}
}

func TestHandleOutOfScopeRepoIs403WithLegibleError(t *testing.T) {
	p, forwarder, audit := newTestProxy(t)
	headers := merge(gitHeaders, map[string]string{"Authorization": basicAuthHeader("tok0")})
	resp := p.Handle(context.Background(), "GET", "/owner/unlisted-repo.git/info/refs",
		"service=git-upload-pack", headers, nil)
	if resp.Status != 403 {
		t.Errorf("status = %d, want 403", resp.Status)
	}
	if !strings.Contains(string(resp.Body), "not in scope") {
		t.Errorf("body = %q, want a legible reason", resp.Body)
	}
	if len(forwarder.Calls) != 0 {
		t.Error("expected no forward call")
	}
	if audit.Entries[0].Outcome != "denied: not in scope" {
		t.Errorf("outcome = %q", audit.Entries[0].Outcome)
	}
}

func TestHandleAllowedRepoIsForwardedWithTheSelectedCredential(t *testing.T) {
	p, forwarder, audit := newTestProxy(t)
	headers := merge(gitHeaders, map[string]string{"Authorization": basicAuthHeader("tok0")})
	resp := p.Handle(context.Background(), "GET", "/owner/repo.git/info/refs",
		"service=git-upload-pack", headers, nil)
	if resp.Status != 200 || string(resp.Body) != "ok" {
		t.Fatalf("resp = %+v", resp)
	}
	call := forwarder.Calls[0]
	if call.Token == nil || *call.Token != "bot-token" {
		t.Errorf("token = %v, want bot-token", call.Token)
	}
	if call.Path != "/owner/repo.git/info/refs" {
		t.Errorf("path = %q", call.Path)
	}
	if audit.Entries[0].Credential != "bot" || audit.Entries[0].Sandbox != "sandbox-0" {
		t.Errorf("entry = %+v", audit.Entries[0])
	}
}

func TestHandlePostBodyIsPassedThroughUntouched(t *testing.T) {
	p, forwarder, _ := newTestProxy(t)
	headers := merge(gitHeaders, map[string]string{
		"Authorization": basicAuthHeader("tok0"),
		"Content-Type":  "application/x-git-upload-pack-request",
		"Accept":        "application/x-git-upload-pack-result",
	})
	resp := p.Handle(context.Background(), "POST", "/owner/repo.git/git-upload-pack", "",
		headers, []byte("some pack request bytes"))
	if resp.Status != 200 {
		t.Fatalf("status = %d", resp.Status)
	}
	if string(forwarder.Calls[0].Body) != "some pack request bytes" {
		t.Errorf("body = %q", forwarder.Calls[0].Body)
	}
}

func TestHandleNoConfiguredCredentialIs500NotForwarded(t *testing.T) {
	credsDir := writeCredentialSet(t, map[string]string{}, nil) // nothing covers it
	credentials, err := LoadCredentialSet(credsDir)
	if err != nil {
		t.Fatal(err)
	}
	tokensPath := writeTokensFile(t, t.TempDir(), map[string]string{"sandbox-0": "tok0"})
	tokens, err := LoadSandboxTokens(tokensPath)
	if err != nil {
		t.Fatal(err)
	}
	forwarder := &FakeForwarder{}
	p := &GitProxy{
		Authorizer:      stubAuthorizer{allow: map[[3]string]bool{{"sandbox-0", "owner", "repo"}: true}},
		Credentials:     credentials,
		TeamCredentials: ladderConnections{creds: credentials},
		Tokens:          tokens,
		Forwarder:       forwarder,
	}
	headers := merge(gitHeaders, map[string]string{"Authorization": basicAuthHeader("tok0")})
	resp := p.Handle(context.Background(), "GET", "/owner/repo.git/info/refs",
		"service=git-upload-pack", headers, nil)
	if resp.Status != 500 {
		t.Errorf("status = %d, want 500", resp.Status)
	}
	if len(forwarder.Calls) != 0 {
		t.Error("expected no forward call")
	}
	// bwsalmon/agents#427: this is exactly the shape a widened-targetRepos/
	// stale-credentials.json drift takes at the proxy, so the message
	// needs to name the repo and point at the likely fix rather than
	// leaving an operator to guess what "no credential configured" means.
	if !strings.Contains(string(resp.Body), "owner/repo") {
		t.Errorf("body = %q, want it to name the repo", resp.Body)
	}
	if !strings.Contains(string(resp.Body), "Settings → Git") {
		t.Errorf("body = %q, want a pointer at the deployment's git connections", resp.Body)
	}
}

func TestHandleSurfacesAnAuthorizerErrorAs500(t *testing.T) {
	tokensPath := writeTokensFile(t, t.TempDir(), map[string]string{"sandbox-0": "tok0"})
	tokens, err := LoadSandboxTokens(tokensPath)
	if err != nil {
		t.Fatal(err)
	}
	credsDir := writeCredentialSet(t, map[string]string{"*": "bot"}, map[string]string{"bot": "tok"})
	credentials, err := LoadCredentialSet(credsDir)
	if err != nil {
		t.Fatal(err)
	}
	forwarder := &FakeForwarder{}
	audit := &RecordingAuditLog{}
	p := &GitProxy{
		Authorizer:      erroringAuthorizer{},
		Credentials:     credentials,
		TeamCredentials: ladderConnections{creds: credentials},
		Tokens:          tokens,
		Forwarder:       forwarder,
		Audit:           audit,
	}
	headers := merge(gitHeaders, map[string]string{"Authorization": basicAuthHeader("tok0")})
	resp := p.Handle(context.Background(), "GET", "/owner/repo.git/info/refs",
		"service=git-upload-pack", headers, nil)
	if resp.Status != 500 {
		t.Errorf("status = %d, want 500", resp.Status)
	}
	if len(forwarder.Calls) != 0 {
		t.Error("expected no forward call")
	}
	if len(audit.Entries) != 1 {
		t.Fatalf("Entries = %+v", audit.Entries)
	}
}

type erroringAuthorizer struct{}

func (erroringAuthorizer) Authorize(context.Context, string, string, string, string) (bool, error) {
	return false, errors.New("model store unreachable")
}

// stubCredentialOverrides answers a fixed (name, ok) for every sandbox --
// core_test.go's own concern is what GitProxy.Handle does with the
// answer, not how a real one resolves it (model.Store.GitCredentialOverride
// and its own sqlite-backed tests cover that).
type stubCredentialOverrides struct {
	name string
	ok   bool
}

func (s stubCredentialOverrides) GitCredentialOverride(context.Context, string, string, string) (string, bool, error) {
	return s.name, s.ok, nil
}

type erroringCredentialOverrides struct{}

func (erroringCredentialOverrides) GitCredentialOverride(context.Context, string, string, string) (string, bool, error) {
	return "", false, errors.New("model store unreachable")
}

func TestHandleUsesTheOverrideCredentialWhenTheTaskAsksForOne(t *testing.T) {
	p, forwarder, audit := newTestProxy(t)
	credsDir := writeCredentialSet(t, map[string]string{"*": "bot"},
		map[string]string{"bot": "bot-token", "workflow": "workflow-token"})
	credentials, err := LoadCredentialSet(credsDir)
	if err != nil {
		t.Fatal(err)
	}
	p.Credentials = credentials
	p.CredentialOverrides = stubCredentialOverrides{name: "workflow", ok: true}

	headers := merge(gitHeaders, map[string]string{"Authorization": basicAuthHeader("tok0")})
	resp := p.Handle(context.Background(), "GET", "/owner/repo.git/info/refs",
		"service=git-upload-pack", headers, nil)
	if resp.Status != 200 {
		t.Fatalf("status = %d, want 200", resp.Status)
	}
	call := forwarder.Calls[0]
	if call.Token == nil || *call.Token != "workflow-token" {
		t.Errorf("token = %v, want workflow-token -- the override should bypass the owner/repo ladder entirely", call.Token)
	}
	if audit.Entries[0].Credential != "workflow" {
		t.Errorf("audit credential = %q, want workflow", audit.Entries[0].Credential)
	}
}

func TestHandleFallsBackToTheLadderWithNoOverride(t *testing.T) {
	p, forwarder, _ := newTestProxy(t)
	p.CredentialOverrides = stubCredentialOverrides{ok: false}

	headers := merge(gitHeaders, map[string]string{"Authorization": basicAuthHeader("tok0")})
	resp := p.Handle(context.Background(), "GET", "/owner/repo.git/info/refs",
		"service=git-upload-pack", headers, nil)
	if resp.Status != 200 {
		t.Fatalf("status = %d, want 200", resp.Status)
	}
	call := forwarder.Calls[0]
	if call.Token == nil || *call.Token != "bot-token" {
		t.Errorf("token = %v, want bot-token from the ordinary ladder", call.Token)
	}
}

func TestHandleOverrideNamingAnUnconfiguredCredentialIs500NotForwarded(t *testing.T) {
	p, forwarder, audit := newTestProxy(t)
	p.CredentialOverrides = stubCredentialOverrides{name: "nonexistent", ok: true}

	headers := merge(gitHeaders, map[string]string{"Authorization": basicAuthHeader("tok0")})
	resp := p.Handle(context.Background(), "GET", "/owner/repo.git/info/refs",
		"service=git-upload-pack", headers, nil)
	if resp.Status != 500 {
		t.Errorf("status = %d, want 500", resp.Status)
	}
	if !strings.Contains(string(resp.Body), `"nonexistent"`) {
		t.Errorf("body = %q, want a legible reason naming the missing credential", resp.Body)
	}
	if len(forwarder.Calls) != 0 {
		t.Error("expected no forward call")
	}
	if audit.Entries[0].Outcome != `error: no credential named "nonexistent" configured` {
		t.Errorf("outcome = %q", audit.Entries[0].Outcome)
	}
}

func TestHandleSurfacesACredentialOverrideLookupErrorAs500(t *testing.T) {
	p, forwarder, audit := newTestProxy(t)
	p.CredentialOverrides = erroringCredentialOverrides{}

	headers := merge(gitHeaders, map[string]string{"Authorization": basicAuthHeader("tok0")})
	resp := p.Handle(context.Background(), "GET", "/owner/repo.git/info/refs",
		"service=git-upload-pack", headers, nil)
	if resp.Status != 500 {
		t.Errorf("status = %d, want 500", resp.Status)
	}
	if len(forwarder.Calls) != 0 {
		t.Error("expected no forward call")
	}
	if len(audit.Entries) != 1 {
		t.Fatalf("Entries = %+v", audit.Entries)
	}
}

func merge(a, b map[string]string) map[string]string {
	out := make(map[string]string, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// noTeamCredentialOverrides is a live task whose team has no credential
// for the repository (model.ErrNoTeamGitCredential).
type noTeamCredentialOverrides struct{}

func (noTeamCredentialOverrides) GitCredentialOverride(context.Context, string, string, string) (string, bool, error) {
	return "", false, &model.NoTeamGitCredentialError{Team: "infra"}
}

// A live task with no git connection for its repository is refused
// whatever the deployment's tenancy: the credentials.json ladder is no
// longer a fallback on a single-tenant deployment either
// (docs/single-tenant-reset.md, stage 1).
func TestHandleNoTeamCredentialIsRefusedOnEitherTenancy(t *testing.T) {
	for _, single := range []bool{false, true} {
		p, forwarder, _ := newTestProxy(t)
		p.CredentialOverrides = noTeamCredentialOverrides{}
		p.SingleTenant = single

		headers := merge(gitHeaders, map[string]string{"Authorization": basicAuthHeader("tok0")})
		resp := p.Handle(context.Background(), "GET", "/owner/repo.git/info/refs",
			"service=git-upload-pack", headers, nil)
		if resp.Status == 200 || len(forwarder.Calls) != 0 {
			t.Errorf("single-tenant=%v: status = %d with %d forwards; want the request refused, not forwarded",
				single, resp.Status, len(forwarder.Calls))
		}
	}
}
