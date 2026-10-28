package githubsim

// Every answer GitUpstream models is one that was measured against
// github.com by hand (task 123, and this type's own doc comment) -- so
// these tests are written as that table: one case per row, asserting the
// status and the sentence a git client would print, since both are what
// the code reading them downstream (gitproxy's refusals.go) keys on.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func upstreamServer(t *testing.T, u *GitUpstream) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(u)
	t.Cleanup(server.Close)
	return server
}

// ask makes one smart-HTTP request the way a git client does, with token
// in the Basic auth username GitHub's HTTPS convention puts it in --
// empty token meaning no credential at all.
func ask(t *testing.T, server *httptest.Server, token, path string) (int, string) {
	t.Helper()
	req, err := http.NewRequest("GET", server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("User-Agent", "git/2.39.2")
	if token != "" {
		req.SetBasicAuth(token, "")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body)
}

const (
	fetch = "/acme/widgets.git/info/refs?service=git-upload-pack"
	push  = "/acme/widgets.git/info/refs?service=git-receive-pack"
)

func TestARevokedTokenIsRefusedAsTheCredentialItself(t *testing.T) {
	server := upstreamServer(t, &GitUpstream{
		Tokens: map[string]GitAccess{"good": {Read: []string{"acme/widgets"}}},
	})
	for _, path := range []string{fetch, push} {
		status, body := ask(t, server, "revoked", path)
		if status != 401 {
			t.Errorf("%s: status = %d, want 401", path, status)
		}
		if !strings.Contains(body, "Invalid username or token") {
			t.Errorf("%s: body = %q, want GitHub's own sentence about the token", path, body)
		}
	}
}

func TestAnAnonymousPushIsRefusedBeforeAnythingElse(t *testing.T) {
	// Public and writable to somebody, so that what refuses this is the
	// absence of a credential rather than the absence of access.
	server := upstreamServer(t, &GitUpstream{
		Public: []string{"acme/widgets"},
		Tokens: map[string]GitAccess{"good": {Read: []string{"acme/widgets"}, Write: []string{"acme/widgets"}}},
	})
	status, body := ask(t, server, "", push)
	if status != 401 || !strings.Contains(body, "No anonymous write access.") {
		t.Errorf("status = %d, body = %q, want 401 and GitHub's anonymous-write refusal", status, body)
	}
}

func TestAnAnonymousFetchOfAPrivateRepoIsA401NotA404(t *testing.T) {
	server := upstreamServer(t, &GitUpstream{})
	status, body := ask(t, server, "", fetch)
	if status != 401 {
		t.Errorf("status = %d, want the 401 github.com answers an unidentified caller", status)
	}
	if body != "" {
		t.Errorf("body = %q, want the empty one measured here", body)
	}
	// The challenge is the half of this a git client acts on.
	req, _ := http.NewRequest("GET", server.URL+fetch, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if !strings.HasPrefix(resp.Header.Get("WWW-Authenticate"), "Basic ") {
		t.Errorf("WWW-Authenticate = %q, want a Basic challenge", resp.Header.Get("WWW-Authenticate"))
	}
}

func TestATokenGitHubAcceptsGetsA404ForARepoItCannotSee(t *testing.T) {
	server := upstreamServer(t, &GitUpstream{
		Tokens: map[string]GitAccess{"good": {Read: []string{"acme/other"}}},
	})
	status, body := ask(t, server, "good", fetch)
	if status != 404 || !strings.Contains(body, "Repository not found.") {
		t.Errorf("status = %d, body = %q, want the 404 that hides a private repo", status, body)
	}
}

func TestAReadOnlyTokenPushingIsA403(t *testing.T) {
	server := upstreamServer(t, &GitUpstream{
		Tokens: map[string]GitAccess{"reader": {Read: []string{"acme/widgets"}}},
	})
	status, body := ask(t, server, "reader", push)
	if status != 403 || !strings.Contains(body, "Write access to repository not granted.") {
		t.Errorf("status = %d, body = %q, want the 403 a read-only credential's push gets", status, body)
	}
	// The same token fetching is not refused: what it lacks is write.
	if status, _ := ask(t, server, "reader", fetch); status != 501 {
		t.Errorf("fetch status = %d, want the request allowed through to a (missing) Backend", status)
	}
}

// The one read failure GitHub explains rather than hiding -- and the
// reason the proxy quotes GitHub's own body back: the URL in it is the
// action an operator has to take, and nothing else knows it.
func TestATokenAnOrganizationHoldsBackIsA403WithSomewhereToGo(t *testing.T) {
	server := upstreamServer(t, &GitUpstream{
		Tokens: map[string]GitAccess{"good": {
			Read:            []string{"acme/widgets"},
			Write:           []string{"acme/widgets"},
			SSOUnauthorized: []string{"acme/widgets"},
		}},
	})
	status, body := ask(t, server, "good", fetch)
	if status != 403 {
		t.Fatalf("status = %d, want 403", status)
	}
	for _, want := range []string{"SAML SSO", "https://github.com/orgs/acme/sso"} {
		if !strings.Contains(body, want) {
			t.Errorf("body = %q, want it to mention %s", body, want)
		}
	}
}

func TestAnAllowedRequestReachesTheBackend(t *testing.T) {
	server := upstreamServer(t, &GitUpstream{
		Tokens: map[string]GitAccess{"good": {Read: []string{"ACME/Widgets.git"}}},
		Backend: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("packfile for " + r.URL.Path))
		}),
	})
	// Spelled differently from the GitAccess entry on purpose: GitHub
	// resolves a repository case-insensitively, .git or no .git.
	status, body := ask(t, server, "good", fetch)
	if status != 200 || !strings.Contains(body, "packfile for /acme/widgets.git/info/refs") {
		t.Errorf("status = %d, body = %q, want the backend's own answer", status, body)
	}
}

func TestAPathThatIsNotASmartHTTPEndpointIs404(t *testing.T) {
	server := upstreamServer(t, &GitUpstream{Public: []string{"acme/widgets"}})
	if status, _ := ask(t, server, "", "/acme/widgets.git/HEAD"); status != 404 {
		t.Errorf("status = %d, want 404", status)
	}
}

// GitHub also takes a token as the password half under the username
// x-access-token, which is how grain's workflow source (ciWorkflowSource's
// askpass) presents the deployment's credential. Before this was modelled
// every such clone through the double was refused as a revoked token
// (docs/test-runs/2026-10-05-declared-repo-access.md, finding F1).
func TestATokenSentAsTheXAccessTokenPasswordIsTheToken(t *testing.T) {
	server := upstreamServer(t, &GitUpstream{
		Tokens:  map[string]GitAccess{"good": {Read: []string{"acme/widgets"}}},
		Backend: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }),
	})
	for _, tc := range []struct {
		user, password string
		want           int
	}{
		{"x-access-token", "good", 200},
		{"x-access-token", "revoked", 401},
		// Any other username is still the token itself.
		{"someone", "good", 401},
	} {
		req, err := http.NewRequest("GET", server.URL+fetch, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.SetBasicAuth(tc.user, tc.password)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("%s:%s = %d, want %d", tc.user, tc.password, resp.StatusCode, tc.want)
		}
	}
}
