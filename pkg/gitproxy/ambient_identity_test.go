package gitproxy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
)

// The commit identity that goes with an ambient credential: the host
// account's own git identity first, then the login GitHub answers /user
// with, and never `grain agent <grain-agent@localhost>` while the host
// has either to offer.

func TestAmbientGitIdentityIsTheHostAccountsOwn(t *testing.T) {
	state := fakeHostTools(t)
	writeState(t, state, "gh-token", "gho_who")
	writeState(t, state, "git-user-name", "Olive Operator")
	writeState(t, state, "git-user-email", "olive@example.com")
	c, now := ambientSet(t)
	fake := github.NewFakeTransport()
	c.AppTransport = fake
	if err := c.SetAmbient("op", "github.com", "gh"); err != nil {
		t.Fatal(err)
	}

	id, err := c.AmbientGitIdentity("op")
	if err != nil {
		t.Fatal(err)
	}
	if id.Name != "Olive Operator" || id.Email != "olive@example.com" {
		t.Fatalf("identity = %+v, want the host's own git config", id)
	}
	// A host whose git config says who the person is needs no GitHub
	// lookup at all: neither gh nor /user was asked.
	if n := calls(t, state, "gh-calls"); n != 0 || len(fake.Calls) != 0 {
		t.Fatalf("gh ran %d times and /user was asked %d times for an identity git config answered", n, len(fake.Calls))
	}
	if got := id.String(); got != "Olive Operator <olive@example.com>" {
		t.Fatalf("String() = %q", got)
	}

	// Cached: a second ask within the TTL does not run git again, and
	// one past it does.
	before := calls(t, state, "git-calls")
	if _, err := c.AmbientGitIdentity("op"); err != nil {
		t.Fatal(err)
	}
	if n := calls(t, state, "git-calls"); n != before {
		t.Fatalf("git ran again within the TTL (%d -> %d calls)", before, n)
	}
	writeState(t, state, "git-user-name", "Olive O. Operator")
	*now = now.Add(hostGitIdentityTTL + time.Second)
	if id, err := c.AmbientGitIdentity("op"); err != nil || id.Name != "Olive O. Operator" {
		t.Fatalf("after the TTL: %+v, %v; want the host's new name", id, err)
	}
}

func TestAmbientGitIdentityFallsBackToTheLoginsNoreplyAddress(t *testing.T) {
	state := fakeHostTools(t)
	writeState(t, state, "gh-token", "gho_who")
	// The host names the person but gives no address: the address is
	// the login's noreply one, which GitHub attributes to the account.
	writeState(t, state, "git-user-name", "Olive Operator")
	c, _ := ambientSet(t)
	fake := github.NewFakeTransport(
		github.ApiResponse{Status: 200, Body: []byte(`{"login":"operator","id":12345,"name":"Olive from GitHub"}`), Headers: map[string]string{"X-Oauth-Scopes": "repo"}},
	)
	c.AppTransport = fake
	if err := c.SetAmbient("op", "github.com", "gh"); err != nil {
		t.Fatal(err)
	}
	id, err := c.AmbientGitIdentity("op")
	if err != nil {
		t.Fatal(err)
	}
	if id.Name != "Olive Operator" || id.Email != "12345+operator@users.noreply.github.com" {
		t.Fatalf("identity = %+v, want the host's name and the login's noreply address", id)
	}

	// A host with no git identity at all: both halves come from the
	// login -- its display name, and its noreply address.
	if err := os.Remove(filepath.Join(state, "git-user-name")); err != nil {
		t.Fatal(err)
	}
	c.hostIdentity = nil
	id, err = c.AmbientGitIdentity("op")
	if err != nil {
		t.Fatal(err)
	}
	if id.Name != "Olive from GitHub" || id.Email != "12345+operator@users.noreply.github.com" {
		t.Fatalf("identity = %+v, want the login's own name and noreply address", id)
	}
	// Asked /user once for both: the login is cached per token.
	if len(fake.Calls) != 1 {
		t.Fatalf("/user asked %d times", len(fake.Calls))
	}

	// A login with no display name is named by its login.
	fake.Responses = []github.ApiResponse{{Status: 200, Body: []byte(`{"login":"operator","id":12345}`)}}
	c.identities = nil
	if id, err := c.AmbientGitIdentity("op"); err != nil || id.Name != "operator" {
		t.Fatalf("identity for a login without a name = %+v, %v", id, err)
	}
}

func TestAmbientGitIdentityOnGHESUsesThatHostsNoreplyDomain(t *testing.T) {
	id := AmbientIdentity{Login: "operator", ID: 7}
	if got := id.NoreplyEmail("ghe.example.com:8443"); got != "7+operator@users.noreply.ghe.example.com" {
		t.Fatalf("NoreplyEmail = %q", got)
	}
	if got := id.NoreplyEmail(""); got != "7+operator@users.noreply.github.com" {
		t.Fatalf("NoreplyEmail for the default host = %q", got)
	}
	if got := (AmbientIdentity{Login: "x-access-token"}).NoreplyEmail("git.example.com"); got != "" {
		t.Fatalf("NoreplyEmail for a plain git host's helper user = %q, want none", got)
	}
}

func TestAmbientGitIdentityWithNothingOnTheHostSaysSo(t *testing.T) {
	state := fakeHostTools(t)
	c, _ := ambientSet(t)
	c.AppTransport = github.NewFakeTransport()
	if err := c.SetAmbient("op", "github.com", "gh"); err != nil {
		t.Fatal(err)
	}
	// No git identity and gh not logged in: an error naming what to
	// run, for the caller to log and fall back on grain's own default.
	if _, err := c.AmbientGitIdentity("op"); err == nil || !strings.Contains(err.Error(), "gh auth login") {
		t.Fatalf("identity with nothing on the host = %v", err)
	}
	// With a name but no login: the name alone, no error -- the other
	// half is the sandbox's to default.
	writeState(t, state, "git-user-name", "Olive Operator")
	c.hostIdentity = nil
	if id, err := c.AmbientGitIdentity("op"); err != nil || id.Name != "Olive Operator" || id.Email != "" {
		t.Fatalf("identity with a name only = %+v, %v", id, err)
	}
	// Anything that is not an ambient credential has no host identity.
	if _, err := c.AmbientGitIdentity("nobody"); err == nil {
		t.Fatal("a non-ambient name answered an identity")
	}
}

func TestHostGitIdentityIsOnlyReadOnAHost(t *testing.T) {
	state := fakeHostTools(t)
	writeState(t, state, "git-user-name", "Olive Operator")
	t.Setenv("GRAIN_IN_CONTAINER", "1")
	c, _ := ambientSet(t)
	if id := c.HostGitIdentity(); !id.Empty() {
		t.Fatalf("HostGitIdentity inside the container = %+v, want nothing", id)
	}
	if n := calls(t, state, "git-calls"); n != 0 {
		t.Fatalf("git ran %d times inside the container", n)
	}
}
