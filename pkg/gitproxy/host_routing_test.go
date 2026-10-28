package gitproxy

// Per-host routing (docs/git-hosts.md, section 5): a sandbox URL names
// the repository's host (/<host>/<owner>/<repo>.git), the credential is
// resolved for that host, and the request goes to the credential's own
// host and nowhere else. The old /<owner>/<repo>.git form is the
// default host, exactly as before.

import (
	"context"
	"encoding/base64"
	"io"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
)

// hostCredentials is a CredentialOverrideLookup that also answers per
// host (HostCredentialOverrideLookup), the way model.Store does: one
// credential name per host, and none for a host it has no entry for.
type hostCredentials map[string]string

func (h hostCredentials) GitCredentialOverride(ctx context.Context, sandbox, owner, repo string) (string, bool, error) {
	return h.GitCredentialOverrideOn(ctx, sandbox, "", owner, repo)
}

func (h hostCredentials) GitCredentialOverrideOn(_ context.Context, _, host, _, _ string) (string, bool, error) {
	name, ok := h[host]
	if !ok {
		return "", false, &model.NoTeamGitCredentialError{Team: "t", Host: host}
	}
	return name, true, nil
}

// twoHostProxy is a real BuildProxy forwarding to fake host a by
// default, holding a-token (unrecorded, so on a) and b-token recorded on
// fake host b, with sandbox-0 running a task that targets b's
// acme/widgets and reads a's acme/widgets.
func twoHostProxy(t *testing.T) (proxy *GitProxy, a, b *fakeGHE, audit *RecordingAuditLog) {
	t.Helper()
	a, b = newFakeGHE(t), newFakeGHE(t)
	proxy = buildProxyOn(t, a.host(), nil, map[string]string{"a-token": "secret-a", "b-token": "secret-b"})
	if err := proxy.Credentials.SetHost("b-token", CredentialHost{Host: b.host(), BaseURL: b.URL}); err != nil {
		t.Fatal(err)
	}
	proxy.Authorizer = &ModelAuthorizer{
		Store: stubScopeLookup{
			target: &model.RepoRef{Host: b.host(), Owner: "acme", Name: "widgets"},
			reads:  []model.RepoRef{{Owner: "acme", Name: "widgets"}},
		},
		Branches: stubBranchLookup{branch: "grain/task-7", live: true},
	}
	proxy.CredentialOverrides = hostCredentials{"": "a-token", b.host(): "b-token"}
	audit = &RecordingAuditLog{}
	proxy.Audit = audit
	return proxy, a, b, audit
}

func gitAs(p *GitProxy, method, path, query, accept string, body []byte) ProxyResponse {
	headers := merge(gitHeaders, map[string]string{"Authorization": basicAuthHeader("tok0")})
	if accept != "" {
		headers["Accept"] = accept
	}
	resp := p.Handle(context.Background(), method, path, query, headers, body)
	if resp.Stream != nil {
		resp.Body, _ = io.ReadAll(resp.Stream)
		resp.Stream.Close()
		resp.Stream = nil
	}
	return resp
}

func basicToken(token string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(token+":"))
}

func TestARepoOnASecondHostClonesAndPushesThroughThatHostsCredentialOnly(t *testing.T) {
	proxy, a, b, audit := twoHostProxy(t)
	prefix := "/" + b.host() + "/acme/widgets.git/"

	if resp := gitAs(proxy, "GET", prefix+"info/refs", "service=git-upload-pack", "", nil); resp.Status != 200 {
		t.Fatalf("clone advertisement on %s: %d %q", b.host(), resp.Status, resp.Body)
	}
	if resp := gitAs(proxy, "POST", prefix+"git-upload-pack", "", "application/x-git-upload-pack-result", []byte("0000")); resp.Status != 200 {
		t.Fatalf("fetch on %s: %d %q", b.host(), resp.Status, resp.Body)
	}
	push := pushBody(oidA + " " + oidB + " refs/heads/grain/task-7")
	if resp := gitAs(proxy, "POST", prefix+"git-receive-pack", "", "application/x-git-receive-pack-result", push); resp.Status != 200 {
		t.Fatalf("push on %s: %d %q", b.host(), resp.Status, resp.Body)
	}

	b.mu.Lock()
	gotB := append([]string(nil), b.gitAuth...)
	b.mu.Unlock()
	if len(gotB) != 3 {
		t.Fatalf("host b saw %d git requests, want 3", len(gotB))
	}
	for _, auth := range gotB {
		if auth != basicToken("secret-b") {
			t.Errorf("host b was sent %q, want only b-token", auth)
		}
	}
	a.mu.Lock()
	gotA := len(a.gitAuth)
	a.mu.Unlock()
	if gotA != 0 {
		t.Errorf("the default host saw %d requests for a repository on %s", gotA, b.host())
	}
	for _, e := range audit.Entries {
		if e.Host != b.host() || e.Credential != "b-token" {
			t.Errorf("audit entry %+v, want host %s and credential b-token", e, b.host())
		}
	}

	// The read-only repository on the default host, by the old URL form:
	// a's credential, to a, as before.
	if resp := gitAs(proxy, "GET", "/acme/widgets.git/info/refs", "service=git-upload-pack", "", nil); resp.Status != 200 {
		t.Fatalf("default-host fetch: %d %q", resp.Status, resp.Body)
	}
	a.mu.Lock()
	if len(a.gitAuth) != 1 || a.gitAuth[0] != basicToken("secret-a") {
		t.Errorf("default host saw %q, want one request with a-token", a.gitAuth)
	}
	a.mu.Unlock()
	// And writing the default host out is the same repository as leaving
	// it off.
	if resp := gitAs(proxy, "GET", "/"+a.host()+"/acme/widgets.git/info/refs", "service=git-upload-pack", "", nil); resp.Status != 200 {
		t.Errorf("default host written out: %d %q", resp.Status, resp.Body)
	}
}

func TestOneHostsScopeIsNotAnothers(t *testing.T) {
	proxy, a, b, _ := twoHostProxy(t)
	// acme/widgets is read-only on the default host, so a push there by
	// the old URL form is refused even though b's acme/widgets is this
	// sandbox's target.
	push := pushBody(oidA + " " + oidB + " refs/heads/grain/task-7")
	if resp := gitAs(proxy, "POST", "/acme/widgets.git/git-receive-pack", "", "application/x-git-receive-pack-result", push); resp.Status != 403 {
		t.Errorf("push to the default host's acme/widgets: %d %q, want 403", resp.Status, resp.Body)
	}
	// A third host has nothing in scope at all.
	resp := gitAs(proxy, "GET", "/ghe-c.example.com/acme/widgets.git/info/refs", "service=git-upload-pack", "", nil)
	if resp.Status != 403 {
		t.Errorf("fetch from a host nothing is scoped to: %d %q, want 403", resp.Status, resp.Body)
	}
	mustContain(t, string(resp.Body), "ghe-c.example.com/acme/widgets")
	for _, f := range []*fakeGHE{a, b} {
		f.mu.Lock()
		if len(f.gitAuth) != 0 {
			t.Errorf("%s saw %q, want nothing", f.host(), f.gitAuth)
		}
		f.mu.Unlock()
	}
}

func TestAnotherHostsCredentialIsNeverSentToARepositorysHost(t *testing.T) {
	proxy, a, b, _ := twoHostProxy(t)
	// A ladder that hands b's repository the default host's credential
	// -- a team connection with the wrong host, say -- is refused with
	// nothing sent anywhere.
	proxy.CredentialOverrides = hostCredentials{"": "a-token", b.host(): "a-token"}
	resp := gitAs(proxy, "GET", "/"+b.host()+"/acme/widgets.git/info/refs", "service=git-upload-pack", "", nil)
	if resp.Status == 200 {
		t.Fatal("a request for host b went out with host a's credential")
	}
	mustContain(t, string(resp.Body), `"a-token"`, b.host())
	// And a host the team has no connection for says so, naming it.
	proxy.CredentialOverrides = hostCredentials{"": "a-token"}
	resp = gitAs(proxy, "GET", "/"+b.host()+"/acme/widgets.git/info/refs", "service=git-upload-pack", "", nil)
	if resp.Status == 200 || !strings.Contains(string(resp.Body), b.host()) {
		t.Errorf("no credential for host b: %d %q, want a refusal naming the host", resp.Status, resp.Body)
	}
	for _, f := range []*fakeGHE{a, b} {
		f.mu.Lock()
		if len(f.gitAuth) != 0 {
			t.Errorf("%s saw %q, want nothing", f.host(), f.gitAuth)
		}
		f.mu.Unlock()
	}
}

// A lookup that only knows the default host (every test stub written
// before hosts existed) never resolves a credential for another host as
// though it were the default's.
func TestAHostlessLookupRefusesAnotherHost(t *testing.T) {
	proxy, _, b, _ := twoHostProxy(t)
	proxy.CredentialOverrides = stubCredentialOverrides{name: "a-token", ok: true}
	resp := gitAs(proxy, "GET", "/"+b.host()+"/acme/widgets.git/info/refs", "service=git-upload-pack", "", nil)
	if resp.Status == 200 {
		t.Fatalf("a host-less credential lookup answered for %s", b.host())
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.gitAuth) != 0 {
		t.Errorf("host b saw %q, want nothing", b.gitAuth)
	}
}
