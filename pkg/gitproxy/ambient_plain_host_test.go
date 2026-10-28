package gitproxy

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
)

// fakeCredentialHelperGit is a `git` whose `credential fill` answers like
// git's own store helper: only for the protocol and host[:port] its one
// login was stored under. It records what it was asked in asked.
func fakeCredentialHelperGit(t *testing.T, protocol, host string) (asked string) {
	t.Helper()
	bin, state := t.TempDir(), t.TempDir()
	asked = filepath.Join(state, "asked")
	script := `#!/bin/sh
[ "$1 $2" = "credential fill" ] || exit 2
while read -r line; do
  [ -z "$line" ] && break
  echo "$line" >> "` + asked + `"
  case "$line" in protocol=*) p="${line#protocol=}" ;; host=*) h="${line#host=}" ;; esac
done
if [ "$p" = "` + protocol + `" ] && [ "$h" = "` + host + `" ]; then
  echo "username=walker"
  echo "password=plain-secret"
fi
`
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("GRAIN_IN_CONTAINER", "0")
	return asked
}

// The walkthrough in docs/test-runs/2026-10-15-ambient-git-credentials.md:
// a plain git host on http://127.0.0.1:9922 whose login the helper held
// under exactly that URL. grain asked for https://127.0.0.1 and got
// nothing; it now asks for the base URL's own scheme and host:port.
func TestAmbientCredentialFillAsksForTheBaseURLsSchemeAndPort(t *testing.T) {
	asked := fakeCredentialHelperGit(t, "http", "127.0.0.1:8080")
	c, _ := ambientSet(t)
	plain := CredentialHost{Host: "127.0.0.1:8080", Kind: model.GitHostGit, BaseURL: "http://127.0.0.1:8080"}
	if err := c.SetAmbientOn("plain", plain, "git"); err != nil {
		t.Fatal(err)
	}
	cred, _ := c.Get("plain")
	if cred.Token == nil || *cred.Token != "plain-secret" || cred.Ambient == nil || cred.Ambient.User != "walker" {
		data, _ := os.ReadFile(asked)
		t.Fatalf("Get = %+v; the helper was asked:\n%s", cred, data)
	}
}

// What the helper answered is sent the way git itself sends it -- its
// username with the password -- to a plain git host, so a server behind
// ordinary basic auth accepts the proxy's push.
func TestAPlainGitHostGetsTheHelpersUsernameAndPassword(t *testing.T) {
	var gotUser, gotPass string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPass, _ = r.BasicAuth()
		w.WriteHeader(200)
	}))
	defer server.Close()
	hostPort := strings.TrimPrefix(server.URL, "http://")

	fakeCredentialHelperGit(t, "http", hostPort)
	c, _ := ambientSet(t)
	plain := CredentialHost{Host: hostPort, Kind: model.GitHostGit, BaseURL: server.URL}
	if err := c.SetAmbientOn("plain", plain, "git"); err != nil {
		t.Fatal(err)
	}
	cred, _ := c.Get("plain")
	if cred.Token == nil {
		t.Fatalf("no token: %+v", cred)
	}
	p := &GitProxy{Credentials: c, TeamCredentials: ladderConnections{creds: c}}
	forwarder, err := p.forwarderFor(cred)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := forwarder.Forward("GET", "/team/notes.git/info/refs", "service=git-upload-pack", nil, nil, cred.Token); err != nil {
		t.Fatal(err)
	}
	if gotUser != "walker" || gotPass != "plain-secret" {
		t.Errorf("basic auth sent = %q:%q, want the helper's walker:plain-secret", gotUser, gotPass)
	}
}

// GitHub keeps its own convention: the token as the username.
func TestRealForwarderWithoutAUsernameSendsTheTokenAsTheUsername(t *testing.T) {
	var gotUser, gotPass string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPass, _ = r.BasicAuth()
	}))
	defer server.Close()
	token := "ghp_x"
	f := &RealForwarder{Host: strings.TrimPrefix(server.URL, "http://")}
	if _, err := f.Forward("GET", "/o/r.git/info/refs", "", nil, nil, &token); err != nil {
		t.Fatal(err)
	}
	if gotUser != "ghp_x" || gotPass != "" {
		t.Errorf("basic auth = %q:%q, want the token as username", gotUser, gotPass)
	}
}
