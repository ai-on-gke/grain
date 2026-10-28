package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/gitproxy"
	"github.com/bwsalmon/grain/pkg/model"
)

// The forked mcpserver reads GitHub as the credential the daemon named
// for the run (-pr-credential) -- the one its branch is pushed with --
// and not off the deployment's owner/repo ladder, which on a deployment
// whose credentials are all team connections (grain/task-1592) names
// nothing: every read then went out unauthenticated, GitHub answered 404
// for the private repository, and pull_request_status told a run that
// had just pushed that its branch did not exist.
func TestRunCredentialSourceReadsAsTheNamedCredential(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// A team connection and nothing on the ladder: this deployment.
	if err := os.WriteFile(filepath.Join(dir, "grain-team-default.token"), []byte("team-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	credentials, err := gitproxy.LoadCredentialSet(dir)
	if err != nil {
		t.Fatal(err)
	}

	named := runCredentialSource(credentials, "grain-team-default")
	if tok := named.TokenFor("bwsalmon", "grain"); tok == nil || *tok != "team-token" {
		t.Errorf("TokenFor with the run's credential named = %v, want the team's token", tok)
	}
	if !named.HasCredential("bwsalmon", "grain") {
		t.Error("HasCredential with the run's credential named = false, want true")
	}

	// No name is the ladder, exactly as before -- and here the ladder
	// is empty, so there is no token: the daemon naming the credential
	// is what the fix consists of.
	unnamed := runCredentialSource(credentials, "")
	if tok := unnamed.TokenFor("bwsalmon", "grain"); tok != nil {
		t.Errorf("TokenFor with no credential named and no ladder = %q, want none", *tok)
	}

	// A name the store does not hold falls through to the ladder rather
	// than failing: the daemon's name and this disk can disagree across
	// a credential removed mid-run, and the run is better off reading as
	// it always did than not at all.
	missing := runCredentialSource(credentials, "no-such-credential")
	if tok := missing.TokenFor("bwsalmon", "grain"); tok != nil {
		t.Errorf("TokenFor with an unknown credential named = %q, want the ladder's answer (none)", *tok)
	}
}

// The transport follows the named credential's host too: a credential
// recorded for a GitHub Enterprise host reaches that host's API, even
// when the ladder (empty here) would have said nothing about the repo.
func TestRepoTransportFollowsTheNamedCredentialsHost(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ghe.token"), []byte("ghe-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	hostJSON, err := json.Marshal(gitproxy.CredentialHost{Host: "ghe.example.com", Kind: model.GitHostGitHub})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ghe.host.json"), hostJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	credentials, err := gitproxy.LoadCredentialSet(dir)
	if err != nil {
		t.Fatal(err)
	}

	transport, credHost, err := repoTransport(credentials, false, "github.com", "ghe.example.com/acme/widgets", "ghe")
	if err != nil {
		t.Fatalf("repoTransport: %v", err)
	}
	if credHost.Host != "ghe.example.com" {
		t.Errorf("credential host = %q, want ghe.example.com", credHost.Host)
	}
	if want := github.APIHost("ghe.example.com"); transport.Host != want {
		t.Errorf("transport host = %q, want %q", transport.Host, want)
	}

	// And a github.com repository read with a credential that has no
	// host record -- every credential grain held before hosts existed --
	// still reaches api.github.com: -github-host names the git host, and
	// a transport pointed at github.com itself 404s on every API path
	// (github.APIHost).
	if err := os.WriteFile(filepath.Join(dir, "hub.token"), []byte("hub-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	transport, _, err = repoTransport(credentials, false, "github.com", "acme/widgets", "hub")
	if err != nil {
		t.Fatalf("repoTransport: %v", err)
	}
	if transport.Host != "api.github.com" {
		t.Errorf("transport host for github.com = %q, want api.github.com", transport.Host)
	}

	// A local mock, though, is the API itself, and is reached as named.
	transport, _, err = repoTransport(credentials, true, "127.0.0.1:9999", "acme/widgets", "hub")
	if err != nil {
		t.Fatalf("repoTransport: %v", err)
	}
	if transport.Host != "127.0.0.1:9999" {
		t.Errorf("transport host for a mock = %q, want 127.0.0.1:9999", transport.Host)
	}
}
