package gitproxy

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/model"
)

// routeTransport answers each request by its method and path, and
// records what it was asked.
type routeTransport struct {
	mu     sync.Mutex
	routes map[string]github.ApiResponse
	calls  []github.FakeCall
}

func (r *routeTransport) Request(method, path string, headers map[string]string, body []byte) (github.ApiResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, github.FakeCall{Method: method, Path: path, Headers: headers, Body: body})
	if resp, ok := r.routes[method+" "+path]; ok {
		return resp, nil
	}
	return github.ApiResponse{Status: 404, Body: []byte(`{"message":"Not Found"}`)}, nil
}

func (r *routeTransport) count(method, path string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, c := range r.calls {
		if c.Method == method && c.Path == path {
			n++
		}
	}
	return n
}

// A PAT commits as the person it belongs to: their display name (or
// login) and their login's noreply address, from GET /user with the
// token itself -- asked once per token.
func TestCredentialGitIdentityOfAPATIsItsOwnersLogin(t *testing.T) {
	t.Setenv("GRAIN_IN_CONTAINER", "1")
	c, _ := LoadCredentialSet(t.TempDir())
	rt := &routeTransport{routes: map[string]github.ApiResponse{
		"GET /user": {Status: 200, Body: []byte(`{"login":"alice","id":4242,"name":"Alice Example"}`)},
	}}
	c.AppTransport = rt
	if err := c.SetPAT("team-pat", "ghp_alice"); err != nil {
		t.Fatal(err)
	}
	id, err := c.CredentialGitIdentity("team-pat")
	if err != nil {
		t.Fatal(err)
	}
	if id.Name != "Alice Example" || id.Email != "4242+alice@users.noreply.github.com" {
		t.Fatalf("identity = %+v, want Alice's name and noreply address", id)
	}
	if got := rt.calls[0].Headers["Authorization"]; got != "token ghp_alice" {
		t.Fatalf("/user asked with Authorization %q, want the PAT itself", got)
	}
	if _, err := c.CredentialGitIdentity("team-pat"); err != nil {
		t.Fatal(err)
	}
	if n := rt.count("GET", "/user"); n != 1 {
		t.Fatalf("/user asked %d times for one token, want once", n)
	}

	// A new token is a new lookup: the cache is per token.
	rt.routes["GET /user"] = github.ApiResponse{Status: 200, Body: []byte(`{"login":"bob","id":7}`)}
	if err := c.SetPAT("team-pat", "ghp_bob"); err != nil {
		t.Fatal(err)
	}
	id, err = c.CredentialGitIdentity("team-pat")
	if err != nil {
		t.Fatal(err)
	}
	// A login with no display name is named by its login.
	if id.Name != "bob" || id.Email != "7+bob@users.noreply.github.com" {
		t.Fatalf("identity after the token changed = %+v", id)
	}

	// A token GitHub refuses names nobody.
	rt.routes["GET /user"] = github.ApiResponse{Status: 401, Body: []byte(`{"message":"Bad credentials"}`)}
	if err := c.SetPAT("team-pat", "ghp_revoked"); err != nil {
		t.Fatal(err)
	}
	if id, err := c.CredentialGitIdentity("team-pat"); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("identity for a refused token = %+v, %v; want an error", id, err)
	}
}

// A PAT prefers the host account's git config (HostGitIdentity) when
// available, then the email returned by GET /user, and only falls back to
// the synthesized noreply address when neither is set.
func TestCredentialGitIdentityOfAPATPrefersHostGitConfigAndUserEmail(t *testing.T) {
	state := fakeHostTools(t)
	c, now := ambientSet(t)
	rt := &routeTransport{routes: map[string]github.ApiResponse{
		"GET /user": {Status: 200, Body: []byte(`{"login":"bwsalmon","id":26034206,"name":"Ben Salmon","email":"public@example.com"}`)},
	}}
	c.AppTransport = rt
	if err := c.SetPAT("team-pat", "ghp_bwsalmon"); err != nil {
		t.Fatal(err)
	}

	// With no host git config, /user's email wins over the noreply address.
	id, err := c.CredentialGitIdentity("team-pat")
	if err != nil {
		t.Fatal(err)
	}
	if id.Name != "Ben Salmon" || id.Email != "public@example.com" {
		t.Fatalf("identity from /user email = %+v, want Ben Salmon <public@example.com>", id)
	}

	// When the host account's git config sets user.email (e.g. bsalmon@google.com),
	// that email overrides /user's email and the noreply address.
	writeState(t, state, "git-user-email", "bsalmon@google.com")
	*now = now.Add(hostGitIdentityTTL + time.Second)
	id, err = c.CredentialGitIdentity("team-pat")
	if err != nil {
		t.Fatal(err)
	}
	if id.Name != "Ben Salmon" || id.Email != "bsalmon@google.com" {
		t.Fatalf("identity with host git user.email = %+v, want Ben Salmon <bsalmon@google.com>", id)
	}

	// And when the host account's git config also sets user.name, both halves
	// come from the host git config while /user is still only asked once per token.
	writeState(t, state, "git-user-name", "B. Salmon")
	*now = now.Add(hostGitIdentityTTL + time.Second)
	id, err = c.CredentialGitIdentity("team-pat")
	if err != nil {
		t.Fatal(err)
	}
	if id.Name != "B. Salmon" || id.Email != "bsalmon@google.com" {
		t.Fatalf("identity with host git user.name and user.email = %+v, want B. Salmon <bsalmon@google.com>", id)
	}
	if n := rt.count("GET", "/user"); n != 1 {
		t.Fatalf("/user asked %d times, want 1", n)
	}
}

// A GitHub App installation commits as the App's bot account:
// `<slug>[bot]` with the bot user's noreply address, as GitHub's own
// Apps do.
func TestCredentialGitIdentityOfAnAppIsItsBotAccount(t *testing.T) {
	dir := t.TempDir()
	writeAppCredential(t, dir, "team-app")
	c, _ := LoadCredentialSet(dir)
	rt := &routeTransport{routes: map[string]github.ApiResponse{
		"POST /app/installations/67890/access_tokens": mintedTokenResponse("ghs_installation", time.Now().Add(time.Hour).UTC().Format(time.RFC3339)),
		"GET /app":                      {Status: 200, Body: []byte(`{"id":12345,"slug":"grain-bot","name":"Grain Bot"}`)},
		"GET /users/grain-bot%5Bbot%5D": {Status: 200, Body: []byte(`{"login":"grain-bot[bot]","id":998877,"type":"Bot"}`)},
	}}
	c.AppTransport = rt
	id, err := c.CredentialGitIdentity("team-app")
	if err != nil {
		t.Fatal(err)
	}
	if id.Name != "grain-bot[bot]" || id.Email != "998877+grain-bot[bot]@users.noreply.github.com" {
		t.Fatalf("identity = %+v, want the App's bot account", id)
	}
	for _, call := range rt.calls {
		switch call.Path {
		case "/app":
			if !strings.HasPrefix(call.Headers["Authorization"], "Bearer ") {
				t.Errorf("/app asked with %q, want the App's JWT", call.Headers["Authorization"])
			}
		case "/users/grain-bot%5Bbot%5D":
			if call.Headers["Authorization"] != "token ghs_installation" {
				t.Errorf("the bot user asked with %q, want the installation token", call.Headers["Authorization"])
			}
		}
	}
	if _, err := c.CredentialGitIdentity("team-app"); err != nil {
		t.Fatal(err)
	}
	if n := rt.count("GET", "/app"); n != 1 {
		t.Fatalf("/app asked %d times, want once (cached)", n)
	}

	// A bot user that cannot be looked up is still named, with no
	// address for the sandbox's default to fill -- and asked again next
	// time rather than cached without one.
	c2, _ := LoadCredentialSet(dir)
	rt2 := &routeTransport{routes: map[string]github.ApiResponse{
		"POST /app/installations/67890/access_tokens": rt.routes["POST /app/installations/67890/access_tokens"],
		"GET /app": rt.routes["GET /app"],
	}}
	c2.AppTransport = rt2
	id, err = c2.CredentialGitIdentity("team-app")
	if err != nil || id.Name != "grain-bot[bot]" || id.Email != "" {
		t.Fatalf("identity without the bot user = %+v, %v", id, err)
	}
	if _, err := c2.CredentialGitIdentity("team-app"); err != nil {
		t.Fatal(err)
	}
	if n := rt2.count("GET", "/app"); n != 2 {
		t.Fatalf("/app asked %d times, want it asked again while the address is missing", n)
	}
}

// An SSH key is whoever its paired API credential is; an unpaired one,
// a plain git host's credential and the anonymous one name nobody.
func TestCredentialGitIdentityOfSSHAndPlainGitHosts(t *testing.T) {
	t.Setenv("GRAIN_IN_CONTAINER", "1")
	c, _ := LoadCredentialSet(t.TempDir())
	rt := &routeTransport{routes: map[string]github.ApiResponse{
		"GET /user": {Status: 200, Body: []byte(`{"login":"carol","id":55,"name":"Carol"}`)},
	}}
	c.AppTransport = rt
	key, _ := testSSHKey(t, "")
	if err := c.SetPAT("api", "ghp_carol"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetSSHKey("gh", SSHKeyFile{Host: "github.com", PrivateKey: key, APICredential: "api"}, model.GitHostGitHub); err != nil {
		t.Fatal(err)
	}
	id, err := c.CredentialGitIdentity("gh")
	if err != nil || id.Name != "Carol" || id.Email != "55+carol@users.noreply.github.com" {
		t.Fatalf("paired SSH key identity = %+v, %v; want its API credential's login", id, err)
	}

	if err := c.SetSSHKey("gh-alone", SSHKeyFile{Host: "github.com", PrivateKey: key}, model.GitHostGitHub); err != nil {
		t.Fatal(err)
	}
	if id, err := c.CredentialGitIdentity("gh-alone"); err == nil {
		t.Fatalf("unpaired SSH key identity = %+v, want an error", id)
	}

	if err := c.SetPAT("plain", "secret"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetHost("plain", CredentialHost{Host: "git.example.com", Kind: model.GitHostGit}); err != nil {
		t.Fatal(err)
	}
	before := len(rt.calls)
	if id, err := c.CredentialGitIdentity("plain"); err == nil || !strings.Contains(err.Error(), "plain git host") {
		t.Fatalf("plain git host identity = %+v, %v; want an error", id, err)
	}
	if len(rt.calls) != before {
		t.Fatal("a plain git host's credential was looked up over a GitHub API")
	}
	if _, err := c.CredentialGitIdentity("anonymous"); err == nil {
		t.Fatal("the anonymous credential named someone")
	}
	if _, err := c.CredentialGitIdentity("missing"); err == nil {
		t.Fatal("a credential that does not exist named someone")
	}
}

// The settings pane's bounded wait: answered when the lookup is quick,
// pending (and then cached) when it is not.
func TestCredentialGitIdentityWithin(t *testing.T) {
	t.Setenv("GRAIN_IN_CONTAINER", "1")
	c, _ := LoadCredentialSet(t.TempDir())
	release := make(chan struct{})
	c.AppTransport = &blockingTransport{release: release, inner: &routeTransport{routes: map[string]github.ApiResponse{
		"GET /user": {Status: 200, Body: []byte(`{"login":"dave","id":9}`)},
	}}}
	if err := c.SetPAT("p", "ghp_dave"); err != nil {
		t.Fatal(err)
	}
	if _, pending, err := c.CredentialGitIdentityWithin("p", 10*time.Millisecond); !pending || err != nil {
		t.Fatalf("slow lookup: pending=%v err=%v, want pending", pending, err)
	}
	close(release)
	id, pending, err := c.CredentialGitIdentityWithin("p", 5*time.Second)
	if pending || err != nil || id.Name != "dave" {
		t.Fatalf("after the lookup finished: %+v pending=%v err=%v", id, pending, err)
	}
}

type blockingTransport struct {
	release chan struct{}
	inner   github.Transport
}

func (b *blockingTransport) Request(method, path string, headers map[string]string, body []byte) (github.ApiResponse, error) {
	<-b.release
	return b.inner.Request(method, path, headers, body)
}
