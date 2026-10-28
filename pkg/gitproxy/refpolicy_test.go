package gitproxy

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
)

// The wrapper every deployment runs (BuildProxy puts it in front of
// ModelAuthorizer whenever a scope registry is wired) must itself be a
// RefAuthorizer: Handle checks a push's refs only when its Authorizer
// is one, so a wrapper that is not switches the check off (#2387).
func TestReadOnlyScopeAuthorizerHoldsAPushToTheRunsOwnBranch(t *testing.T) {
	writes := NewWriteScopes()
	writes.Grant("ci-job", model.RepoRef{Owner: "acme", Name: "widgets"})
	var a Authorizer = &ReadOnlyScopeAuthorizer{
		Scopes:      NewReadScopes(),
		WriteScopes: writes,
		Next: &ModelAuthorizer{
			Store:    stubScopeLookup{target: &model.RepoRef{Owner: "acme", Name: "widgets"}},
			Branches: stubBranchLookup{branch: "grain/task-7", live: true},
		},
	}
	refs, ok := a.(RefAuthorizer)
	if !ok {
		t.Fatal("ReadOnlyScopeAuthorizer is not a RefAuthorizer, so Handle never checks a push's refs behind it")
	}
	ctx := context.Background()
	main := []RefUpdate{{Old: oidA, New: oidB, Ref: "refs/heads/main"}}

	if reason, err := refs.AuthorizeRefUpdates(ctx, "sandbox-0", "acme", "widgets", main); err != nil || reason == "" {
		t.Errorf("an agent run's push of main: reason %q, err %v; want it refused", reason, err)
	}
	own := []RefUpdate{{Old: oidA, New: oidB, Ref: "refs/heads/grain/task-7"}}
	if reason, err := refs.AuthorizeRefUpdates(ctx, "sandbox-0", "acme", "widgets", own); err != nil || reason != "" {
		t.Errorf("an agent run's push of its own branch: reason %q, err %v; want it allowed", reason, err)
	}
	// An operator's repo-write grant is to a workflow whose job is to
	// push; it has no task branch to be held to -- but it may only
	// fast-forward main (ciref.go), which this push does not show.
	tag := []RefUpdate{{Old: oidA, New: oidB, Ref: "refs/tags/latest"}}
	if reason, err := refs.AuthorizeRefUpdates(ctx, "ci-job", "acme", "widgets", tag); err != nil || reason != "" {
		t.Errorf("a CI job with repo-write moving a tag: reason %q, err %v; want it allowed", reason, err)
	}
	if reason, err := refs.AuthorizeRefUpdates(ctx, "ci-job", "acme", "widgets", main); err != nil || reason == "" {
		t.Errorf("a CI job with repo-write force-pushing main: reason %q, err %v; want it refused", reason, err)
	}
	if reason, _ := refs.AuthorizeRefUpdates(ctx, "ci-job", "acme", "gadgets", tag); reason == "" {
		t.Error("a CI job's write grant for acme/widgets let it push acme/gadgets")
	}

	// A Next that decides no refs refuses rather than allows.
	bare := &ReadOnlyScopeAuthorizer{Next: stubAuthorizer{}}
	if reason, _ := bare.AuthorizeRefUpdates(ctx, "sandbox-0", "acme", "widgets", own); reason == "" {
		t.Error("a wrapper around an authorizer that decides no refs allowed a push")
	}
}

// The issue's own proof, end to end: a real git client pushing through
// GitProxy to a repository served locally by git http-backend, which
// has no GitHub branch protection behind it. Only the run's own branch
// may move; everything else is rejected with the reason printed.
func TestLocalRepositoryPushesAreHeldToTheRunsOwnBranch(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	runLocalGit(t, "", "init", "-q", "-b", "main", state)
	runLocalGit(t, state, "-c", "user.name=grain", "-c", "user.email=grain@example.invalid",
		"commit", "-q", "--allow-empty", "-m", "Initial state")
	// Neither the checked-out branch nor the one beside it: receive-pack's
	// own denyCurrentBranch would never have caught this one.
	runLocalGit(t, state, "branch", "release")

	tokens, err := LoadSandboxTokens(writeTokensFile(t, t.TempDir(), map[string]string{"sandbox-0": "tok0"}))
	if err != nil {
		t.Fatal(err)
	}
	audit := &RecordingAuditLog{}
	proxy := &GitProxy{
		Authorizer: &ReadOnlyScopeAuthorizer{
			Scopes: NewReadScopes(), WriteScopes: NewWriteScopes(),
			Next: &ModelAuthorizer{
				Store:    stubScopeLookup{target: &model.RepoRef{Owner: "grain", Name: "grain-config"}},
				Branches: stubBranchLookup{branch: "grain/task-7", live: true},
			},
		},
		Tokens:    tokens,
		Forwarder: &FakeForwarder{},
		Audit:     audit,
		Local:     &LocalRepository{Owner: "grain", Name: "grain-config", Dir: state},
	}
	srv := httptest.NewServer(NewHandler(proxy))
	t.Cleanup(srv.Close)
	url := strings.Replace(srv.URL, "http://", "http://x:tok0@", 1) + "/grain/grain-config.git"

	client := filepath.Join(root, "client")
	runLocalGit(t, "", "clone", "-q", url, client)
	runLocalGit(t, client, "config", "user.name", "sandbox")
	runLocalGit(t, client, "config", "user.email", "sandbox@example.invalid")
	if err := os.WriteFile(filepath.Join(client, "settings.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runLocalGit(t, client, "add", "settings.json")
	runLocalGit(t, client, "commit", "-q", "-m", "Propose settings")

	tip := func(ref string) string {
		out, _ := localGitOutput(t, state, "", "rev-parse", "--verify", "-q", ref)
		return out
	}
	release := tip("refs/heads/release")

	for name, args := range map[string][]string{
		"another branch":       {"push", "origin", "HEAD:refs/heads/release"},
		"force another branch": {"push", "--force", "origin", "HEAD:refs/heads/release"},
		"delete another":       {"push", "origin", ":refs/heads/release"},
		"a new branch":         {"push", "origin", "HEAD:refs/heads/grain/task-8"},
		"a tag":                {"push", "origin", "HEAD:refs/tags/v1"},
		"own branch and other": {"push", "origin", "HEAD:refs/heads/grain/task-7", "HEAD:refs/heads/release"},
	} {
		out, err := localGitOutput(t, client, "", args...)
		if err == nil {
			t.Errorf("%s: accepted:\n%s", name, out)
			continue
		}
		if !strings.Contains(out, "[remote rejected]") || !strings.Contains(out, "push with `git push origin grain/task-7`") {
			t.Errorf("%s: refused without git printing the proxy's reason:\n%s", name, out)
		}
	}
	if got := tip("refs/heads/release"); got != release {
		t.Errorf("release moved from %s to %s", release, got)
	}
	for _, ref := range []string{"refs/heads/grain/task-7", "refs/heads/grain/task-8", "refs/tags/v1"} {
		if got := tip(ref); got != "" {
			t.Errorf("%s was created by a refused push: %s", ref, got)
		}
	}

	// The run's own branch: created, then force-moved, as an agent
	// amending its work does.
	if out, err := localGitOutput(t, client, "", "push", "origin", "HEAD:refs/heads/grain/task-7"); err != nil {
		t.Fatalf("pushing the run's own branch: %v\n%s", err, out)
	}
	runLocalGit(t, client, "commit", "-q", "--amend", "-m", "Propose settings, amended")
	if out, err := localGitOutput(t, client, "", "push", "--force", "origin", "HEAD:refs/heads/grain/task-7"); err != nil {
		t.Fatalf("force-pushing the run's own branch: %v\n%s", err, out)
	}
	head, _ := localGitOutput(t, client, "", "rev-parse", "HEAD")
	if got := tip("refs/heads/grain/task-7"); got != head {
		t.Errorf("grain/task-7 = %q, want the amended commit %s", got, head)
	}
}

type stubAnyRefLookup struct {
	stubBranchLookup
	held bool
	err  error
}

func (s stubAnyRefLookup) GitPushAnyRef(context.Context, string) (bool, error) { return s.held, s.err }

func TestGitPushAnyRefLiftsTheOneBranchRule(t *testing.T) {
	ctx := context.Background()
	main := []RefUpdate{{Old: oidA, New: zero, Ref: "refs/heads/main"}, {Old: zero, New: oidB, Ref: "refs/tags/v1"}}
	authorizer := func(lookup BranchLookup) *ModelAuthorizer {
		return &ModelAuthorizer{Store: stubScopeLookup{}, Branches: lookup}
	}
	own := stubBranchLookup{branch: "grain/task-7", live: true}
	if reason, err := authorizer(stubAnyRefLookup{stubBranchLookup: own, held: true}).AuthorizeRefUpdates(ctx, "s", "acme", "widgets", main); err != nil || reason != "" {
		t.Errorf("a run holding git-push-any-ref: reason %q, err %v; want every ref allowed", reason, err)
	}
	if reason, _ := authorizer(stubAnyRefLookup{stubBranchLookup: own}).AuthorizeRefUpdates(ctx, "s", "acme", "widgets", main); reason == "" {
		t.Error("a run without git-push-any-ref pushed main")
	}
	if _, err := authorizer(stubAnyRefLookup{stubBranchLookup: own, err: errors.New("db down")}).AuthorizeRefUpdates(ctx, "s", "acme", "widgets", main); err == nil {
		t.Error("a failed grant lookup was not reported (Handle answers it with a 500, never a push)")
	}
	// No live run: no branch, and no grant either.
	if reason, _ := authorizer(stubAnyRefLookup{held: true}).AuthorizeRefUpdates(ctx, "s", "acme", "widgets", main); reason == "" {
		t.Error("a sandbox with no live run pushed main")
	}
}
