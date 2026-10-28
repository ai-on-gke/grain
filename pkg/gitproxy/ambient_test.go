// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package gitproxy

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/whererun"
)

// fakeHostTools puts a fake `gh` and a fake `git` on PATH -- and nothing
// else, so the real ones are never asked -- and returns the directory
// they read their answers from:
//
//   - gh-token: what `gh auth token` prints; absent, gh exits 1 the way
//     an account that is not logged in does.
//   - git-password: what `git credential fill` answers as password=;
//     absent, it answers nothing.
//   - git-user-name, git-user-email: what `git config --global --get
//     user.name` / `user.email` print; absent, git exits 1 the way it
//     does for a key that is not set.
//   - gh-calls, git-calls: one line appended per run.
//
// The scripts use shell builtins only, since PATH holds nothing else.
func fakeHostTools(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	state := t.TempDir()
	gh := `#!/bin/sh
echo "$*" >> "` + state + `/gh-calls"
if [ "$1 $2 $3 $4" != "auth token --hostname github.com" ] && [ "$1 $2 $3" != "auth token --hostname" ]; then
  echo "unexpected gh $*" >&2
  exit 2
fi
if [ ! -f "` + state + `/gh-token" ]; then
  echo "You are not logged into any GitHub hosts. To log in, run: gh auth login" >&2
  exit 1
fi
read -r token < "` + state + `/gh-token"
echo "$token"
`
	git := `#!/bin/sh
echo "$*" >> "` + state + `/git-calls"
if [ "$1 $2 $3" = "config --global --get" ]; then
  case "$4" in
    user.name) f="` + state + `/git-user-name" ;;
    user.email) f="` + state + `/git-user-email" ;;
    *) exit 1 ;;
  esac
  [ -f "$f" ] || exit 1
  read -r v < "$f"
  echo "$v"
  exit 0
fi
[ "$1 $2" = "credential fill" ] || exit 2
proto=""; host=""
while read -r line; do
  [ -z "$line" ] && break
  case "$line" in
    protocol=*) proto="${line#protocol=}" ;;
    host=*) host="${line#host=}" ;;
  esac
done
echo "protocol=$proto"
echo "host=$host"
if [ "$proto" = https ] && [ -f "` + state + `/git-password" ]; then
  read -r pw < "` + state + `/git-password"
  echo "username=x-access-token"
  echo "password=$pw"
fi
`
	for name, body := range map[string]string{"gh": gh, "git": git} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	t.Setenv("GRAIN_IN_CONTAINER", "0")
	return state
}

func writeState(t *testing.T, state, file, value string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(state, file), []byte(value+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func calls(t *testing.T, state, file string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(state, file))
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "\n")
}

func ambientSet(t *testing.T) (*CredentialSet, *time.Time) {
	t.Helper()
	c, err := LoadCredentialSet(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	c.Now = func() time.Time { return now }
	return c, &now
}

func TestAmbientTokenIsFetchedOnUseCachedAndDroppedOn401(t *testing.T) {
	state := fakeHostTools(t)
	c, now := ambientSet(t)
	if err := c.SetAmbient("op", "", "gh"); err != nil {
		t.Fatal(err)
	}
	if got := calls(t, state, "gh-calls"); got != 0 {
		t.Fatalf("saving the credential ran gh %d times; it should run only when a token is needed", got)
	}
	if got := c.AuthType("op"); got != AuthTypeAmbient {
		t.Fatalf("AuthType = %q, want %q", got, AuthTypeAmbient)
	}

	writeState(t, state, "gh-token", "gho_first")
	cred, ok := c.Get("op")
	if !ok || cred.Token == nil || *cred.Token != "gho_first" || cred.Ambient == nil || cred.Ambient.From != "gh" {
		t.Fatalf("Get = %+v, %v; want gh's token", cred, ok)
	}
	if cred, _ := c.GetForRepo("op", "owner", "repo"); cred.Token == nil || *cred.Token != "gho_first" {
		t.Fatalf("GetForRepo = %+v", cred)
	}
	c.Get("op")
	if got := calls(t, state, "gh-calls"); got != 2 {
		// One for the full-scope key, one for the repo-scoped key.
		t.Fatalf("gh ran %d times for repeated lookups inside the cache window, want 2", got)
	}

	// The login rotates: the cache serves the old token until it
	// expires, then asks again.
	writeState(t, state, "gh-token", "gho_second")
	*now = now.Add(ambientTokenTTL - time.Second)
	if cred, _ := c.Get("op"); *cred.Token != "gho_first" {
		t.Fatalf("token inside the cache window = %q", *cred.Token)
	}
	*now = now.Add(2 * time.Second)
	if cred, _ := c.Get("op"); *cred.Token != "gho_second" {
		t.Fatalf("token after the cache window = %q, want the rotated one", *cred.Token)
	}

	// A 401 drops it at once.
	writeState(t, state, "gh-token", "gho_third")
	cred, _ = c.Get("op")
	c.Refused(cred)
	if cred, _ := c.Get("op"); *cred.Token != "gho_third" {
		t.Fatalf("token after a 401 = %q, want a fresh one", *cred.Token)
	}
	// So does a 401 the REST client reports by token alone.
	writeState(t, state, "gh-token", "gho_fourth")
	c.RefusedToken("gho_third")
	if cred, _ := c.Get("op"); *cred.Token != "gho_fourth" {
		t.Fatalf("token after RefusedToken = %q, want a fresh one", *cred.Token)
	}
	// A stored token is never dropped by a 401: it would only be re-read.
	if err := c.SetToken("bot", "ghp_stored"); err != nil {
		t.Fatal(err)
	}
	bot, _ := c.Get("bot")
	c.Refused(bot)
	c.RefusedToken("ghp_stored")
}

func TestAmbientGitCredentialFillAndAutoFallback(t *testing.T) {
	state := fakeHostTools(t)
	c, _ := ambientSet(t)
	if err := c.SetAmbient("viagit", "github.com", "git"); err != nil {
		t.Fatal(err)
	}
	writeState(t, state, "git-password", "gho_fromhelper")
	cred, _ := c.Get("viagit")
	if cred.Token == nil || *cred.Token != "gho_fromhelper" || cred.Ambient.From != "git" {
		t.Fatalf("git source: %+v", cred)
	}
	if calls(t, state, "gh-calls") != 0 {
		t.Fatal("source git ran gh")
	}

	// auto: gh first; with gh not logged in, git's helper answers.
	if err := c.SetAmbient("auto", "github.com", ""); err != nil {
		t.Fatal(err)
	}
	if f, _ := c.AmbientOf("auto"); f.Source != AmbientSourceAuto {
		t.Fatalf("empty source stored as %q, want auto", f.Source)
	}
	cred, _ = c.Get("auto")
	if cred.Token == nil || *cred.Token != "gho_fromhelper" || cred.Ambient.From != "git" {
		t.Fatalf("auto with gh logged out: %+v", cred)
	}
	if calls(t, state, "gh-calls") != 1 {
		t.Fatal("auto did not try gh first")
	}
}

func TestAmbientNotLoggedInSaysWhatToRun(t *testing.T) {
	state := fakeHostTools(t)
	c, now := ambientSet(t)
	if err := c.SetAmbient("op", "github.com", "auto"); err != nil {
		t.Fatal(err)
	}
	cred, ok := c.Get("op")
	if !ok || cred.Token != nil {
		t.Fatalf("Get with nobody logged in = %+v, %v; want no token", cred, ok)
	}
	for _, want := range []string{"gh auth login --hostname github.com", "git credential fill returned no password"} {
		if !strings.Contains(cred.Unusable, want) {
			t.Errorf("refusal %q does not say %q", cred.Unusable, want)
		}
	}
	// The git proxy refuses rather than sending the request anonymously.
	p := &GitProxy{Credentials: c, TeamCredentials: ladderConnections{creds: c}, CredentialOverrides: fixedOverride("op")}
	if _, refusal, err := p.selectCredential(t.Context(), "sb", "", "o", "r"); err != nil || !strings.Contains(refusal, "gh auth login") {
		t.Fatalf("proxy refusal = %q, %v", refusal, err)
	}

	// Logging in is picked up once the short failure backoff passes.
	writeState(t, state, "gh-token", "gho_now")
	*now = now.Add(ambientFailureRetryDelay + time.Second)
	if cred, _ := c.Get("op"); cred.Token == nil || *cred.Token != "gho_now" {
		t.Fatalf("after gh auth login: %+v", cred)
	}

	// gh not installed at all.
	t.Setenv("PATH", t.TempDir())
	if _, _, err := AmbientToken("github.com", "gh"); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("AmbientToken without gh = %v", err)
	}
}

type fixedOverride string

func (f fixedOverride) GitCredentialOverride(ctx context.Context, sandbox, owner, repo string) (string, bool, error) {
	return string(f), true, nil
}

func TestAmbientRefusedInAContainer(t *testing.T) {
	state := fakeHostTools(t)
	writeState(t, state, "gh-token", "gho_secret")
	t.Setenv("GRAIN_IN_CONTAINER", "1")
	c, _ := ambientSet(t)
	err := c.SetAmbient("op", "github.com", "gh")
	if err == nil || !strings.Contains(err.Error(), "only available when the daemon runs on the bare machine") {
		t.Fatalf("SetAmbient in a container = %v, want the container refusal", err)
	}
	if c.IsAmbient("op") {
		t.Fatal("a refused SetAmbient left a file behind")
	}
	// A file written by hand is never used either.
	if err := os.WriteFile(filepath.Join(c.Dir(), "op.ambient.json"), []byte(`{"host":"github.com","source":"gh"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cred, _ := c.Get("op")
	if cred.Token != nil || !strings.Contains(cred.Unusable, "personal access token") {
		t.Fatalf("hand-written ambient credential in container mode = %+v", cred)
	}
	if calls(t, state, "gh-calls") != 0 {
		t.Fatal("gh was run in container mode")
	}
}

func TestAmbientIsNeverAPatternOrACapability(t *testing.T) {
	fakeHostTools(t)
	c, _ := ambientSet(t)
	if err := c.SetAmbient("op", "github.com", "gh"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetToken("bot", "ghp_x"); err != nil {
		t.Fatal(err)
	}
	if got := c.ExtraNames(); len(got) != 1 || got[0] != "bot" {
		t.Fatalf("ExtraNames = %v, want only the stored token", got)
	}
	if got := c.Names(); len(got) != 2 {
		t.Fatalf("Names = %v, want both", got)
	}
	// Replacing it with a token removes the ambient file, and Remove
	// deletes it.
	if err := c.SetToken("op", "ghp_y"); err == nil {
		t.Fatal("SetToken over an ambient credential was allowed")
	}
	if err := c.SetPAT("op", "ghp_y"); err != nil || c.IsAmbient("op") {
		t.Fatalf("SetPAT over ambient: %v, still ambient %v", err, c.IsAmbient("op"))
	}
	if err := c.SetAmbient("op", "github.com", "gh"); err != nil || c.AuthType("op") != AuthTypeAmbient {
		t.Fatalf("SetAmbient over a PAT: %v, %q", err, c.AuthType("op"))
	}
	if _, err := os.Stat(filepath.Join(c.Dir(), "op.token")); !os.IsNotExist(err) {
		t.Fatal("SetAmbient left the PAT it replaced")
	}
	if err := c.Remove("op"); err != nil || c.AuthType("op") != "" {
		t.Fatalf("Remove: %v, %q", err, c.AuthType("op"))
	}
}

func TestAmbientTokenNeverStoredOrLogged(t *testing.T) {
	state := fakeHostTools(t)
	const token = "gho_neverwrittenanywhere0123456789"
	writeState(t, state, "gh-token", token)
	var logs bytes.Buffer
	prev, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(prev); log.SetFlags(prevFlags) })

	c, _ := ambientSet(t)
	if err := c.SetAmbient("op", "github.com", "gh"); err != nil {
		t.Fatal(err)
	}
	cred, _ := c.Get("op")
	if cred.Token == nil || *cred.Token != token {
		t.Fatalf("Get = %+v", cred)
	}
	c.Refused(cred)
	c.Get("op")
	err := filepath.Walk(c.Dir(), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), token) {
			t.Errorf("%s holds the ambient token", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), token) {
		t.Errorf("the log holds the ambient token:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), `ambient credential "op": token for github.com fetched from gh`) {
		t.Errorf("the fetch was not logged:\n%s", logs.String())
	}
}

func TestAmbientIdentityNamesTheLoginAndItsScopes(t *testing.T) {
	state := fakeHostTools(t)
	writeState(t, state, "gh-token", "gho_who")
	c, _ := ambientSet(t)
	fake := github.NewFakeTransport(
		github.ApiResponse{Status: 200, Body: []byte(`{"login":"bsalmon"}`), Headers: map[string]string{"X-Oauth-Scopes": "gist, read:org"}},
	)
	c.AppTransport = fake
	if err := c.SetAmbient("op", "github.com", "gh"); err != nil {
		t.Fatal(err)
	}
	id, err := c.AmbientIdentityOf("op")
	if err != nil {
		t.Fatal(err)
	}
	if id.Login != "bsalmon" || !id.NoRepoScope {
		t.Fatalf("identity = %+v, want bsalmon without repo scope", id)
	}
	if got := fake.Calls[0].Headers["Authorization"]; got != "token gho_who" {
		t.Fatalf("/user asked with %q", got)
	}
	// Asked once per token, not on every read of the pane.
	if _, err := c.AmbientIdentityOf("op"); err != nil || len(fake.Calls) != 1 {
		t.Fatalf("second identity read: %v, %d calls", err, len(fake.Calls))
	}

	// A login GitHub refuses is dropped and named.
	fake.Responses = []github.ApiResponse{{Status: 401, Body: []byte(`{"message":"Bad credentials"}`)}}
	c.identities = nil
	if _, err := c.AmbientIdentityOf("op"); err == nil || !strings.Contains(err.Error(), "gh auth login") {
		t.Fatalf("identity with a refused token = %v", err)
	}
	if n := calls(t, state, "gh-calls"); n != 1 {
		t.Fatalf("gh ran %d times before the 401; the identity check should reuse load's token", n)
	}
	c.Get("op")
	if n := calls(t, state, "gh-calls"); n != 2 {
		t.Fatalf("the 401 did not drop the cached token (gh ran %d times)", n)
	}
}

func TestAmbientOnAPlainGitHostAsksGitsCredentialHelper(t *testing.T) {
	state := fakeHostTools(t)
	c, _ := ambientSet(t)
	depot := CredentialHost{Host: "git.forge.example", Kind: model.GitHostGit, BaseURL: "https://git.forge.example"}
	if err := c.SetAmbientOn("depot", depot, "gh"); err == nil || !strings.Contains(err.Error(), "gh holds GitHub logins only") {
		t.Fatalf("source gh on a plain git host: err = %v, want a refusal", err)
	}
	if c.AuthType("depot") != "" {
		t.Fatal("a refused ambient credential was written anyway")
	}
	if err := c.SetAmbientOn("depot", depot, "auto"); err != nil {
		t.Fatal(err)
	}
	if f, _ := c.AmbientOf("depot"); f.Source != AmbientSourceGit || f.Host != "git.forge.example" {
		t.Fatalf("ambient file = %+v, want source git on git.forge.example", f)
	}
	if h := c.HostOf("depot"); h.Kind != model.GitHostGit || h.Host != "git.forge.example" {
		t.Fatalf("host record = %+v, want kind git on git.forge.example", h)
	}

	writeState(t, state, "git-password", "depot-secret")
	id, err := c.AmbientIdentityOf("depot")
	if err != nil {
		t.Fatal(err)
	}
	// The fake helper answers username=x-access-token: on a plain git
	// host that username is the whole identity, with no API to ask.
	if id.Login != "x-access-token" || id.NoRepoScope {
		t.Fatalf("identity = %+v, want the helper's own username", id)
	}
	if calls(t, state, "gh-calls") != 0 {
		t.Fatal("a plain git host's ambient credential ran gh")
	}
	if got := calls(t, state, "git-calls"); got != 1 {
		t.Fatalf("git credential fill ran %d times, want 1", got)
	}
}

// An ambient credential's host record is the host its file asks a token
// for, and nothing re-points it: SetHost refuses another host, a record
// edited to say one makes the credential unusable rather than routing
// the login's token there, and the identity lookup asks the file's host
// whatever the record says (task 3764's review).
func TestAmbientHostRecordCannotBeRepointed(t *testing.T) {
	state := fakeHostTools(t)
	writeState(t, state, "gh-token", "gho_operator")
	c, _ := ambientSet(t)
	if err := c.SetAmbient("op", "github.com", "gh"); err != nil {
		t.Fatal(err)
	}
	err := c.SetHost("op", CredentialHost{Host: "evil.example.com", Kind: model.GitHostGitHub})
	if err == nil || !strings.Contains(err.Error(), "ambient credential for github.com") {
		t.Fatalf("SetHost re-pointing an ambient credential = %v", err)
	}
	if h := c.HostOf("op"); h.Host != "github.com" {
		t.Fatalf("host after a refused SetHost = %q", h.Host)
	}
	// The same host is still accepted: it is how SetAmbientOn records it.
	if err := c.SetHost("op", CredentialHost{Host: "github.com", Kind: model.GitHostGitHub}); err != nil {
		t.Fatalf("SetHost on the ambient credential's own host: %v", err)
	}

	// A record edited behind SetHost's back: refused, not followed.
	if err := writeFileAtomic(c.dir, "op.host.json", []byte(`{"host":"evil.example.com","kind":"github"}`)); err != nil {
		t.Fatal(err)
	}
	c.forget("op")
	before := calls(t, state, "gh-calls")
	cred, _ := c.Get("op")
	if cred.Token != nil || !strings.Contains(cred.Unusable, "host record says evil.example.com") {
		t.Fatalf("ambient credential with a mismatched host record = token %v, unusable %q", cred.Token != nil, cred.Unusable)
	}
	if n := calls(t, state, "gh-calls"); n != before {
		t.Fatalf("gh was asked for a token for a mismatched credential (%d calls, was %d)", n, before)
	}
	if _, err := c.AmbientIdentityOf("op"); err == nil {
		t.Fatal("identity lookup on a mismatched ambient credential succeeded")
	}
	// And the identity lookup's transport is the file's host regardless.
	transport, err := c.ambientTransportFor("op", "github.com")
	if err != nil {
		t.Fatal(err)
	}
	if rt := transport.(*github.RealTransport); rt.Host != "api.github.com" || rt.PathPrefix != "" {
		t.Fatalf("identity transport = %s%s, want api.github.com", rt.Host, rt.PathPrefix)
	}
}

func TestAmbientRefusalNamesTheContainerSignalItSaw(t *testing.T) {
	err := ambientAvailable(whererun.Detection{Host: false, Signal: "/.dockerenv exists"}, nil)
	if err == nil {
		t.Fatal("a container detection allowed an ambient credential")
	}
	for _, want := range []string{"/.dockerenv exists", "personal access token", "GitHub App"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not mention %q", err, want)
		}
	}
	// Host mode is gone (task 5041): the refusal points nowhere near it.
	for _, gone := range []string{"GRAIN_INSTALL_MODE", "host mode"} {
		if strings.Contains(err.Error(), gone) {
			t.Errorf("refusal %q still points at %q", err, gone)
		}
	}
}

// A daemon on the bare machine -- a development one -- has no container
// marker, and is a host.
func TestAmbientAllowedOnTheBareMachine(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "proc", "1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "proc", "1", "cgroup"), []byte("0::/init.scope\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := whererun.Probe{Getenv: func(string) string { return "" }, ReadFile: os.ReadFile, Root: root}
	if err := ambientAvailable(p.Detect(), nil); err != nil {
		t.Fatalf("ambient on a host with a pre-d497083 unit = %v", err)
	}
}

// On workstations where NSS/LDAP reports u.HomeDir as /home/<username> (an
// autofs mount that hangs on non-interactive access), a registered local-disk
// home parent (<dir>/<username>) is preferred when owned by the daemon's uid.
func TestAmbientHomePrefersRegisteredLocalDirectory(t *testing.T) {
	prevUser := userCurrent
	t.Cleanup(func() {
		userCurrent = prevUser
	})

	localRoot := t.TempDir()
	t.Cleanup(RegisterAmbientHomeDir(localRoot))

	userCurrent = func() (*user.User, error) {
		return &user.User{
			Username: "localuser",
			HomeDir:  "/home/localuser",
		}, nil
	}

	localUserHome := filepath.Join(localRoot, "localuser")
	if err := os.Mkdir(localUserHome, 0o755); err != nil {
		t.Fatal(err)
	}

	if got := ambientHome(); got != localUserHome {
		t.Fatalf("ambientHome() = %q, want %q", got, localUserHome)
	}

	env := ambientEnv()
	var homeVal string
	for _, kv := range env {
		if strings.HasPrefix(kv, "HOME=") {
			homeVal = strings.TrimPrefix(kv, "HOME=")
		}
	}
	if homeVal != localUserHome {
		t.Fatalf("ambientEnv() HOME = %q, want %q", homeVal, localUserHome)
	}
}

func TestAmbientHomeFallsBackToPasswdWhenRegisteredLocalMissing(t *testing.T) {
	prevUser := userCurrent
	t.Cleanup(func() {
		userCurrent = prevUser
	})

	localRoot := t.TempDir()
	t.Cleanup(RegisterAmbientHomeDir(localRoot))

	userCurrent = func() (*user.User, error) {
		return &user.User{
			Username: "normaluser",
			HomeDir:  "/home/normaluser",
		}, nil
	}

	if got := ambientHome(); got != "/home/normaluser" {
		t.Fatalf("ambientHome() = %q, want %q", got, "/home/normaluser")
	}
}

func TestAmbientHomeIgnoresRegularFileInRegisteredLocalDir(t *testing.T) {
	prevUser := userCurrent
	t.Cleanup(func() {
		userCurrent = prevUser
	})

	localRoot := t.TempDir()
	t.Cleanup(RegisterAmbientHomeDir(localRoot))

	userCurrent = func() (*user.User, error) {
		return &user.User{
			Username: "fileuser",
			HomeDir:  "/home/fileuser",
		}, nil
	}

	fileUser := filepath.Join(localRoot, "fileuser")
	if err := os.WriteFile(fileUser, []byte("regular file"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := ambientHome(); got != "/home/fileuser" {
		t.Fatalf("ambientHome() = %q, want fallback %q", got, "/home/fileuser")
	}
}

// A registered local-home directory somebody else owns is not this account's home.
func TestAmbientHomeIgnoresRegisteredDirectoryOwnedByAnotherUser(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can chown; test needs a directory root does not own")
	}
	prevUser := userCurrent
	t.Cleanup(func() {
		userCurrent = prevUser
	})

	// /, owned by root, stands in for a directory the daemon's uid does not own.
	t.Cleanup(RegisterAmbientHomeDir("/"))
	userCurrent = func() (*user.User, error) {
		return &user.User{Username: "tmp", HomeDir: "/home/tmp"}, nil
	}
	if got := ambientHome(); got != "/home/tmp" {
		t.Fatalf("ambientHome() = %q, want passwd home %q", got, "/home/tmp")
	}
}

// Neither $USER nor $HOME names the local home: only the passwd entry.
func TestAmbientHomeIgnoresEnvironment(t *testing.T) {
	prevUser := userCurrent
	t.Cleanup(func() {
		userCurrent = prevUser
	})

	localRoot := t.TempDir()
	t.Cleanup(RegisterAmbientHomeDir(localRoot))
	if err := os.Mkdir(filepath.Join(localRoot, "envuser"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("USER", "envuser")
	t.Setenv("HOME", filepath.Join(localRoot, "envuser"))

	userCurrent = func() (*user.User, error) {
		return &user.User{Username: "realuser", HomeDir: "/home/realuser"}, nil
	}
	if got := ambientHome(); got != "/home/realuser" {
		t.Fatalf("ambientHome() = %q, want %q", got, "/home/realuser")
	}
	userCurrent = func() (*user.User, error) {
		return nil, errors.New("user current unavailable")
	}
	if got := ambientHome(); got != "" {
		t.Fatalf("ambientHome() with no passwd entry = %q, want empty", got)
	}
}
