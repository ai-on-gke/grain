package gitproxy

// The repo no sandbox may reach, whoever asks and whatever their task
// says: grain's own state repository, when it holds the encrypted
// secrets file (ModelAuthorizer.Forbidden).

import (
	"context"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
)

// A task dispatched at the state repository has it as its Target, which
// is exactly what would otherwise allow both a fetch and a push. That is
// the case that has to be refused: the fetch is what carries the
// ciphertext out.
func TestForbiddenRepoIsDeniedEvenAsATasksOwnTarget(t *testing.T) {
	state := model.RepoRef{Owner: "acme", Name: "grain-state"}
	a := &ModelAuthorizer{
		Store:     stubScopeLookup{target: &state},
		Forbidden: NewForbiddenSet(state),
	}
	for _, action := range []string{"info/refs", "git-upload-pack", "git-receive-pack"} {
		ok, err := a.Authorize(context.Background(), "sandbox-0", "acme", "grain-state", action)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			t.Errorf("action %q against a forbidden repo was allowed", action)
		}
	}
}

// GitHub treats owner/repo case-insensitively and a client may or may
// not send .git, so a refusal that only matched one spelling would be no
// refusal at all.
func TestForbiddenRepoIsDeniedHoweverItIsSpelled(t *testing.T) {
	a := &ModelAuthorizer{
		Store:     stubScopeLookup{target: &model.RepoRef{Owner: "acme", Name: "grain-state"}},
		Forbidden: NewForbiddenSet(model.RepoRef{Owner: "ACME", Name: "grain-state.git"}),
	}
	ok, err := a.Authorize(context.Background(), "sandbox-0", "acme", "Grain-State", "git-upload-pack")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("a forbidden repo spelled differently was allowed")
	}
}

// Nothing else moves: a deployment whose state repository has never held
// the secrets file has no forbidden repos at all, and one that does must
// still dispatch normally against everything else.
func TestForbiddingOneRepoLeavesTheRestAlone(t *testing.T) {
	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	a := &ModelAuthorizer{
		Store:     stubScopeLookup{target: &widgets},
		Forbidden: NewForbiddenSet(model.RepoRef{Owner: "acme", Name: "grain-state"}),
	}
	ok, err := a.Authorize(context.Background(), "sandbox-0", "acme", "widgets", "git-receive-pack")
	if err != nil || !ok {
		t.Fatalf("push to the task's own target = (%v, %v), want allowed", ok, err)
	}
}

// The caller has to be told why, or an agent whose task really does
// target that repository reads a bare "not in scope" as grain being
// broken -- and nothing is forwarded upstream on the way.
func TestHandleRefusesAForbiddenRepoWithAReason(t *testing.T) {
	p, forwarder, audit := newTestProxy(t)
	state := model.RepoRef{Owner: "owner", Name: "repo"}
	p.Authorizer = &ModelAuthorizer{
		Store:     stubScopeLookup{target: &state},
		Forbidden: NewForbiddenSet(state),
	}

	resp := p.Handle(context.Background(), "GET", "/owner/repo.git/info/refs", "service=git-upload-pack",
		map[string]string{"User-Agent": "git/2.39.2", "Accept": "*/*", "Authorization": basicAuthHeader("tok0")}, nil)

	if resp.Status != 403 {
		t.Fatalf("status = %d, want 403", resp.Status)
	}
	if !strings.Contains(string(resp.Body), "secrets") {
		t.Errorf("the refusal does not say why: %q", resp.Body)
	}
	if len(forwarder.Calls) != 0 {
		t.Errorf("a forbidden request reached the upstream: %+v", forwarder.Calls)
	}
	if len(audit.Entries) != 1 || !strings.Contains(audit.Entries[0].Outcome, "forbidden") {
		t.Errorf("audit entries = %+v, want one naming the refusal", audit.Entries)
	}
}

// The set is re-read per request, not captured when the proxy was built:
// adopting a state repository points an installation at a different one
// under a daemon that is already serving, so a proxy that could only be
// told once would go on serving the ciphertext until somebody restarted
// it (ForbiddenSet's own doc comment).
func TestReplacingTheForbiddenSetTakesEffectOnALiveProxy(t *testing.T) {
	p, forwarder, _ := newTestProxy(t)
	state := model.RepoRef{Owner: "owner", Name: "repo"}
	forbidden := NewForbiddenSet()
	p.Authorizer = &ModelAuthorizer{Store: stubScopeLookup{target: &state}, Forbidden: forbidden}
	request := func() ProxyResponse {
		return p.Handle(context.Background(), "GET", "/owner/repo.git/info/refs", "service=git-upload-pack",
			map[string]string{"User-Agent": "git/2.39.2", "Accept": "*/*", "Authorization": basicAuthHeader("tok0")}, nil)
	}

	if resp := request(); resp.Status != 200 {
		t.Fatalf("status = %d with nothing forbidden, want the task's own target served: %q", resp.Status, resp.Body)
	}

	forbidden.Set([]model.RepoRef{state})

	resp := request()
	if resp.Status != 403 || !strings.Contains(string(resp.Body), "secrets") {
		t.Fatalf("status = %d body = %q after the repo was forbidden, want a 403 saying why", resp.Status, resp.Body)
	}
	if len(forwarder.Calls) != 1 {
		t.Errorf("upstream calls = %d, want only the one made before the repo was forbidden", len(forwarder.Calls))
	}

	// And back again: a repository that has never held the secrets file
	// is an ordinary target, so adopting one has to lift the refusal as
	// readily as adopting the other imposed it.
	forbidden.Set(nil)
	if resp := request(); resp.Status != 200 {
		t.Fatalf("status = %d once the repo was unforbidden, want it served again: %q", resp.Status, resp.Body)
	}
}
