package gitproxy

import (
	"context"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
)

// A push begins with GET info/refs?service=git-receive-pack, and git
// prints the body of a refused advertisement but not of a refused
// git-receive-pack POST. So a sandbox that may only read a repository is
// refused at the advertisement, in a sentence that says why (finding F2
// of docs/test-runs/2026-10-05-declared-repo-access.md).

func advertise(t *testing.T, p *GitProxy, repo, service string) ProxyResponse {
	t.Helper()
	return p.Handle(context.Background(), "GET", "/owner/"+repo+".git/info/refs", "service="+service,
		merge(gitHeaders, map[string]string{"Authorization": basicAuthHeader("tok0")}), nil)
}

func TestAReadScopedSandboxIsRefusedAtThePushAdvertisementWithAReason(t *testing.T) {
	p, forwarder, audit := newTestProxy(t)
	scopes := NewReadScopes()
	scopes.Grant("sandbox-0", model.RepoRef{Owner: "owner", Name: "repo"})
	p.Authorizer = ciScoped(scopes, nil)

	if resp := advertise(t, p, "repo", "git-upload-pack"); resp.Status != 200 {
		t.Fatalf("fetch advertisement = %d %q, want 200", resp.Status, resp.Body)
	}
	resp := advertise(t, p, "repo", "git-receive-pack")
	if resp.Status != 403 {
		t.Fatalf("push advertisement = %d, want 403", resp.Status)
	}
	if want := ReadOnlyPushReason("owner", "repo"); !strings.Contains(string(resp.Body), want) {
		t.Errorf("body = %q, want it to contain %q", resp.Body, want)
	}
	if len(forwarder.Calls) != 1 {
		t.Errorf("forwarded %d requests, want only the fetch advertisement", len(forwarder.Calls))
	}
	last := audit.Entries[len(audit.Entries)-1]
	if last.Outcome != "denied: push to read-only repository" {
		t.Errorf("audit outcome = %q", last.Outcome)
	}

	// The same sandbox pushing somewhere it may not even fetch is still
	// "not in scope", not "read-only".
	other := advertise(t, p, "other", "git-receive-pack")
	if other.Status != 403 || !strings.Contains(string(other.Body), "owner/other is not in scope for this sandbox") {
		t.Errorf("push advertisement to an unscoped repo = %d %q", other.Status, other.Body)
	}
}

func TestAWriteScopedSandboxPassesThePushAdvertisement(t *testing.T) {
	p, forwarder, _ := newTestProxy(t)
	writes := NewWriteScopes()
	writes.Grant("sandbox-0", model.RepoRef{Owner: "owner", Name: "repo"})
	p.Authorizer = &ReadOnlyScopeAuthorizer{WriteScopes: writes, Next: &ModelAuthorizer{Store: stubScopeLookup{}}}

	if resp := advertise(t, p, "repo", "git-receive-pack"); resp.Status != 200 {
		t.Fatalf("push advertisement = %d %q, want 200", resp.Status, resp.Body)
	}
	if len(forwarder.Calls) != 1 {
		t.Errorf("forwarded %d requests, want 1", len(forwarder.Calls))
	}
}

func TestAnAgentRunsPushAdvertisementFollowsItsTasksScope(t *testing.T) {
	p, _, _ := newTestProxy(t)
	p.Authorizer = &ReadOnlyScopeAuthorizer{
		Scopes: NewReadScopes(),
		Next: &ModelAuthorizer{Store: stubScopeLookup{
			target: &model.RepoRef{Owner: "owner", Name: "repo"},
			reads:  []model.RepoRef{{Owner: "owner", Name: "lib"}},
			writes: []model.RepoRef{{Owner: "owner", Name: "out"}},
		}},
	}

	for _, repo := range []string{"repo", "out"} {
		if resp := advertise(t, p, repo, "git-receive-pack"); resp.Status != 200 {
			t.Errorf("push advertisement to owner/%s = %d %q, want 200", repo, resp.Status, resp.Body)
		}
	}
	if resp := advertise(t, p, "lib", "git-upload-pack"); resp.Status != 200 {
		t.Errorf("fetch advertisement to a Reads repo = %d %q, want 200", resp.Status, resp.Body)
	}
	resp := advertise(t, p, "lib", "git-receive-pack")
	if resp.Status != 403 || !strings.Contains(string(resp.Body), ReadOnlyPushReason("owner", "lib")) {
		t.Errorf("push advertisement to a Reads repo = %d %q, want the read-only refusal", resp.Status, resp.Body)
	}
}
