package daemon

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/gitproxy"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/portpick"
)

// A deployment with no git proxy has no mechanical porter: every port is
// an agent task, as before.
func TestNewPorterIsNilWithoutAGitProxy(t *testing.T) {
	if p := NewPorter(t.TempDir(), "", nil, nil); p != nil {
		t.Fatalf("NewPorter with no proxy = %#v, want nil", p)
	}
}

// Each attempt reaches the repository through the git proxy as an
// identity of its own, allowed to push to that one repository, and that
// identity is gone -- token, scope and all -- once the attempt is done.
func TestNewPorterMintsAndRevokesAnIdentityPerAttempt(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "sandbox-tokens.json")
	tokens := gitproxy.NewSandboxTokenStore(tokenPath)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	porter, ok := NewPorter(dir, "http://127.0.0.1:9/", nil, tokens).(*portpick.Porter)
	if !ok {
		t.Fatal("NewPorter did not build a portpick.Porter")
	}
	remote, err := porter.Remote(context.Background(), repo, "")
	if err != nil {
		t.Fatal(err)
	}
	if remote.URL != "http://127.0.0.1:9/acme/widgets.git" {
		t.Errorf("URL = %q, want the proxy's own address for the repo", remote.URL)
	}
	known, err := gitproxy.LoadSandboxTokens(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	identity, ok := known.Authenticate(remote.Token)
	if !ok || !strings.HasPrefix(identity, "port-") {
		t.Fatalf("token authenticates as %q (%v), want a port- identity", identity, ok)
	}
	if got := CiWriteScopes.Repos(identity); len(got) != 1 || !got[0].Same(repo) {
		t.Errorf("write scope = %v, want only %s", got, repo)
	}

	remote.Release()
	if got := CiWriteScopes.Repos(identity); len(got) != 0 {
		t.Errorf("write scope after release = %v, want none", got)
	}
	known, err = gitproxy.LoadSandboxTokens(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := known.Authenticate(remote.Token); ok {
		t.Error("the attempt's token still authenticates after release")
	}
}
