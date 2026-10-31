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

// A fake forgecli whose forge answers the way a real command-backed one
// was measured to on 2026-10-17 (docs/git-hosts.md section 12): `forgecli
// api` gets through the forge's front end, but a repository's smart-HTTP
// endpoints answer 401 to the "Authorization: token <token>" header
// forgecli sends by default and serve the request only for Basic
// x-access-token:<token>. Behind it is a
// real `git http-backend`, so clones and pushes through the git proxy
// are real round trips. The fake is this test binary itself, run through
// a symlink named forgecli (TestMain hands over to fakeDepotGGHMain).

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// fakeDepotGGHEnv names the fake's state directory; set, together with the
// binary being run as "forgecli", it makes TestMain run the fake.
const fakeDepotGGHEnv = "GRAIN_TEST_FAKE_DEPOT_GGH"

func fakeDepotGGHAsked() bool {
	return os.Getenv(fakeDepotGGHEnv) != "" && filepath.Base(os.Args[0]) == "forgecli"
}

// fakeDepotGGHMain is the fake. Its state directory holds:
//   - backend: the base URL git requests are served from;
//   - logged-out: present when the forgecli login has lapsed (auth token
//     fails, the REST API answers 401);
//   - git-401: present to have the git endpoints refuse even the right
//     header while the REST API still accepts the login;
//   - corp-login: present when the forge login has lapsed (every api run gets the
//     302 to login.forge.test);
//   - api-args: each api run's argv, one argument a line and "--" after;
//   - git-headers: each request's headers as the backend would get them,
//     Authorization left out, "--" after each.
func fakeDepotGGHMain() int {
	state := os.Getenv(fakeDepotGGHEnv)
	host := os.Getenv("GH_HOST")
	token := "forgecli-token-" + host
	exists := func(name string) bool {
		_, err := os.Stat(filepath.Join(state, name))
		return err == nil
	}
	appendTo := func(name, text string) {
		f, err := os.OpenFile(filepath.Join(state, name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			f.WriteString(text)
			f.Close()
		}
	}
	args := os.Args[1:]
	if len(args) >= 2 && args[0] == "auth" && args[1] == "token" {
		if exists("logged-out") {
			fmt.Fprintf(os.Stderr, "You are not logged into %s. Run: GH_HOST=%s forgecli auth login\n", host, host)
			return 1
		}
		fmt.Println(token)
		return 0
	}
	if len(args) == 0 || args[0] != "api" {
		fmt.Fprintf(os.Stderr, "unexpected forgecli %q\n", args)
		return 2
	}
	appendTo("api-args", strings.Join(args, "\n")+"\n--\n")

	method, endpoint, input := "GET", "", false
	header := http.Header{}
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--include":
		case "--method":
			i++
			method = args[i]
		case "-H":
			i++
			k, v, _ := strings.Cut(args[i], ":")
			header.Add(k, strings.TrimSpace(v))
		case "--input":
			i++
			input = args[i] == "-"
		default:
			endpoint = args[i]
		}
	}
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	respond := func(status string, headers map[string]string, body string) int {
		fmt.Fprintf(out, "HTTP/1.1 %s\r\n", status)
		for k, v := range headers {
			fmt.Fprintf(out, "%s: %s\r\n", k, v)
		}
		fmt.Fprintf(out, "\r\n%s", body)
		if strings.HasPrefix(status, "2") {
			return 0
		}
		return 1
	}
	if exists("corp-login") {
		return respond("302 Found", map[string]string{"Location": "https://" + testLoginHost + "/?return=x"}, "")
	}
	if header.Get("Authorization") == "" {
		// gh's own: set only when the caller set none.
		if exists("logged-out") {
			return respond("401 Unauthorized", map[string]string{"Content-Type": "application/json"},
				`{"message":"Bad credentials"}`)
		}
		header.Set("Authorization", "token "+token)
	}
	if !strings.HasPrefix(endpoint, "https://") {
		if endpoint == "user" {
			if header.Get("Authorization") != "token "+token {
				return respond("401 Unauthorized", map[string]string{"Content-Type": "application/json"},
					`{"message":"Bad credentials"}`)
			}
			return respond("200 OK", map[string]string{"Content-Type": "application/json"},
				`{"login":"bsalmon","id":1,"name":"B Salmon"}`)
		}
		return respond("404 Not Found", map[string]string{"Content-Type": "application/json"}, `{"message":"Not Found"}`)
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host != host {
		fmt.Fprintf(os.Stderr, "unexpected endpoint %q\n", endpoint)
		return 2
	}
	// depot's git endpoints: Basic x-access-token or nothing.
	if header.Get("Authorization") != testGitBasicAuth(token) || exists("logged-out") || exists("git-401") {
		return respond("401 Unauthorized", map[string]string{"Content-Type": "text/plain"}, "invalid credentials\n")
	}
	var seen strings.Builder
	for k, vs := range header {
		if k != "Authorization" {
			for _, v := range vs {
				seen.WriteString(k + ": " + v + "\n")
			}
		}
	}
	appendTo("git-headers", seen.String()+"--\n")
	backend, err := os.ReadFile(filepath.Join(state, "backend"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	var body io.Reader
	if input {
		// Read whole: git http-backend under net/http/cgi wants a
		// CONTENT_LENGTH, which a chunked request does not give it.
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, strings.TrimSpace(string(backend))+u.RequestURI(), body)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	for k, vs := range header {
		if k != "Authorization" {
			req.Header[k] = vs
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error connecting to %s\n", host)
		return 1
	}
	defer resp.Body.Close()
	fmt.Fprintf(out, "HTTP/1.1 %s\r\n", resp.Status)
	resp.Header.Write(out)
	out.WriteString("\r\n")
	io.Copy(out, resp.Body)
	if resp.StatusCode >= 300 {
		return 1
	}
	return 0
}

// depotFixture is the fake forgecli on PATH, a git http-backend behind it, and
// a git proxy whose one sandbox (sandbox-0, token tok0) may fetch and push
// depot.code.forge.test/experimental/<repo> under the ambient forgecli
// credential "depot".
type depotFixture struct {
	state    string
	root     string
	proxyURL string
	creds    *CredentialSet
}

func newDepotFixture(t *testing.T, repos ...string) *depotFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	logToTest(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.Symlink(self, filepath.Join(bin, "forgecli")); err != nil {
		t.Fatal(err)
	}
	// The real git (the sandbox's, and the backend's), and this forgecli.
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GRAIN_IN_CONTAINER", "0")
	t.Setenv("HOME", t.TempDir())
	globalConfig := filepath.Join(t.TempDir(), "gitconfig")
	writeRaw(t, filepath.Dir(globalConfig), "gitconfig", "")
	t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	state := t.TempDir()
	t.Setenv(fakeDepotGGHEnv, state)

	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	backend := httptest.NewServer(&cgi.Handler{
		Path: gitPath,
		Args: []string{"http-backend"},
		Env:  []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"},
	})
	t.Cleanup(backend.Close)
	writeRaw(t, state, "backend", backend.URL)

	allow := map[[3]string]bool{}
	for _, repo := range repos {
		dir := filepath.Join(root, "experimental", repo+".git")
		runCmd(t, "", "git", "init", "--bare", "--initial-branch=main", dir)
		runCmd(t, "", "git", "-C", dir, "config", "http.receivepack", "true")
		allow[[3]string{"sandbox-0", depotHost + "/experimental", repo}] = true
	}
	tokens, err := LoadSandboxTokens(writeTokensFile(t, t.TempDir(), map[string]string{"sandbox-0": "tok0"}))
	if err != nil {
		t.Fatal(err)
	}
	c := gghDepotSet(t, "")
	p := &GitProxy{
		Authorizer:          stubAuthorizer{allow: allow},
		Credentials:         c,
		TeamCredentials:     ladderConnections{creds: c},
		CredentialOverrides: depotOverride{"depot"},
		Tokens:              tokens,
		Forwarder:           &RealForwarder{Host: "github.com", UseTLS: true},
	}
	srv := httptest.NewServer(NewHandler(p))
	t.Cleanup(srv.Close)
	return &depotFixture{state: state, root: root, proxyURL: srv.URL, creds: c}
}

// depotOverride resolves every repository, on any host, to the depot
// credential.
type depotOverride struct{ fixedOverride }

func (d depotOverride) GitCredentialOverrideOn(ctx context.Context, sandbox, host, owner, repo string) (string, bool, error) {
	return string(d.fixedOverride), true, nil
}

// remote is the sandbox's view of repo: through the proxy, with its token.
func (f *depotFixture) remote(repo string) string {
	return fmt.Sprintf("http://x-access-token:tok0@%s/%s/experimental/%s.git",
		strings.TrimPrefix(f.proxyURL, "http://"), depotHost, repo)
}

// seed puts one commit on main of repo, straight into the backend.
func (f *depotFixture) seed(t *testing.T, repo string) {
	t.Helper()
	work := filepath.Join(t.TempDir(), "seed")
	runCmd(t, "", "git", "clone", "-q", filepath.Join(f.root, "experimental", repo+".git"), work)
	commitFile(t, work, "README.md", []byte("hello\n"))
	runCmd(t, work, "git", "push", "-q", "origin", "HEAD:main")
}

func commitFile(t *testing.T, dir, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
	runCmd(t, dir, "git", "add", name)
	runCmd(t, dir, "git", "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "add "+name)
}

// git runs git in dir and returns what it printed, failing the test on a
// non-zero exit.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// backendRev is rev in repo's bare repository behind the fake.
func (f *depotFixture) backendRev(t *testing.T, repo, rev string) string {
	t.Helper()
	return strings.TrimSpace(gitIn(t, filepath.Join(f.root, "experimental", repo+".git"), "rev-parse", rev))
}

// assertNoTokenLogged checks that neither the token nor the header built
// from it reached grain's own log -- forgecli's argv carries the header, and
// nothing grain writes may.
func assertNoTokenLogged(t *testing.T) {
	t.Helper()
	token := "forgecli-token-" + depotHost
	logged := logToTest(t).String()
	for _, secret := range []string{token, testGitBasicAuth(token), strings.TrimPrefix(testGitBasicAuth(token), "Basic ")} {
		if strings.Contains(logged, secret) {
			t.Fatalf("grain's log carries the forgecli token or its header:\n%s", logged)
		}
	}
}

func TestGGHDepotFetchAndPushRoundTripThroughTheProxy(t *testing.T) {
	f := newDepotFixture(t, "bsalmon-test")
	f.seed(t, "bsalmon-test")
	work := filepath.Join(t.TempDir(), "clone")
	gitIn(t, "", "clone", "-q", f.remote("bsalmon-test"), work)
	if data, err := os.ReadFile(filepath.Join(work, "README.md")); err != nil || string(data) != "hello\n" {
		t.Fatalf("cloned README = %q, %v", data, err)
	}

	// A push big enough that git streams it chunked (past http.postBuffer).
	big := make([]byte, 3<<20)
	if _, err := rand.Read(big); err != nil {
		t.Fatal(err)
	}
	commitFile(t, work, "big.bin", big)
	gitIn(t, work, "-c", "http.postBuffer=65536", "push", "-q", "origin", "HEAD:refs/heads/grain/feature")
	if got, want := f.backendRev(t, "bsalmon-test", "refs/heads/grain/feature"), strings.TrimSpace(gitIn(t, work, "rev-parse", "HEAD")); got != want {
		t.Fatalf("depot has grain/feature at %s, want %s", got, want)
	}

	// And fetched back into a second clone, byte for byte.
	other := filepath.Join(t.TempDir(), "other")
	gitIn(t, "", "clone", "-q", "--branch", "grain/feature", f.remote("bsalmon-test"), other)
	if data, err := os.ReadFile(filepath.Join(other, "big.bin")); err != nil || !bytes.Equal(data, big) {
		t.Fatalf("fetched big.bin differs (%d bytes, %v)", len(data), err)
	}

	// Every git request went through forgecli with Basic x-access-token, and
	// git's own Content-Type with it.
	var sawUpload, sawReceive bool
	for _, run := range apiRuns(t, f.state) {
		args := strings.Join(run, " ")
		if !strings.Contains(args, "-H Authorization: "+testGitBasicAuth("forgecli-token-"+depotHost)) {
			t.Errorf("git request without depot's Basic auth: %q", args)
		}
		if strings.Contains(args, "git-upload-pack") && strings.Contains(args, "--method POST") {
			sawUpload = strings.Contains(args, "-H Content-Type: application/x-git-upload-pack-request") && strings.Contains(args, "--input -")
		}
		if strings.HasSuffix(args, "/git-receive-pack") {
			sawReceive = strings.Contains(args, "-H Content-Type: application/x-git-receive-pack-request") && strings.Contains(args, "--input -")
		}
	}
	if !sawUpload || !sawReceive {
		t.Errorf("upload-pack POST seen with its body: %v, receive-pack: %v", sawUpload, sawReceive)
	}
	assertNoTokenLogged(t)
}

// An empty depot repository -- no commits, so no default branch -- clones
// (git says it is empty), takes a first commit, and is pushed to.
func TestGGHDepotEmptyRepositoryClonesAndTakesTheFirstPush(t *testing.T) {
	f := newDepotFixture(t, "empty")
	work := filepath.Join(t.TempDir(), "clone")
	out := gitIn(t, "", "clone", f.remote("empty"), work)
	if !strings.Contains(out, "empty repository") {
		t.Fatalf("clone of an empty repository said %q", out)
	}
	commitFile(t, work, "README.md", []byte("first\n"))
	gitIn(t, work, "push", "-q", "origin", "HEAD:refs/heads/main")
	if got, want := f.backendRev(t, "empty", "refs/heads/main"), strings.TrimSpace(gitIn(t, work, "rev-parse", "HEAD")); got != want {
		t.Fatalf("depot has main at %s, want %s", got, want)
	}
	assertNoTokenLogged(t)
}

// Git-Protocol (protocol v2) and a gzip-encoded body reach depot as git
// sent them.
func TestGGHTransportPassesGitHeadersThrough(t *testing.T) {
	f := newDepotFixture(t, "bsalmon-test")
	f.seed(t, "bsalmon-test")
	src, h, ok := f.creds.forgeCommand("depot")
	if !ok {
		t.Fatal("depot is not a forgecli credential")
	}
	req, _ := http.NewRequest("GET", "https://"+depotHost+"/experimental/bsalmon-test.git/info/refs?service=git-upload-pack", nil)
	req.Header.Set("Git-Protocol", "version=2")
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("forgecli-token-"+depotHost+":")))
	resp, err := commandForgeClient(src, h, 0).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "version 2") {
		t.Fatalf("v2 advertisement = %d %q", resp.StatusCode, body)
	}
	headers, _ := os.ReadFile(filepath.Join(f.state, "git-headers"))
	if !strings.Contains(string(headers), "Git-Protocol: version=2") {
		t.Fatalf("depot saw headers %q", headers)
	}
}

// gitFails runs git in dir expecting it to fail, and returns what it
// printed.
func gitFails(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("git %s succeeded:\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

// What a refused depot clone tells the sandbox, by what is actually wrong.
func TestGGHDepotRefusalsNameWhatToFix(t *testing.T) {
	f := newDepotFixture(t, "bsalmon-test")
	f.seed(t, "bsalmon-test")
	clone := func() string {
		return gitFails(t, "", "clone", "-q", f.remote("bsalmon-test"), filepath.Join(t.TempDir(), "c"))
	}

	// The git endpoint refuses while `forgecli api user` answers: not the login.
	writeRaw(t, f.state, "git-401", "")
	out := clone()
	for _, want := range []string{"git endpoint refused the \"depot\" credential's token (401)",
		"the forgecli login it came from is valid", "answers as bsalmon", "not a login to renew", "x-access-token"} {
		if !strings.Contains(out, want) {
			t.Errorf("git endpoint 401 said %q; want %q", out, want)
		}
	}
	for _, wrong := range []string{"no longer valid", "auth login", "gh auth refresh"} {
		if strings.Contains(out, wrong) {
			t.Errorf("git endpoint 401 blamed the login (%q): %q", wrong, out)
		}
	}
	os.Remove(filepath.Join(f.state, "git-401"))

	// The forgecli login itself lapsed: `forgecli api user` fails too, and forgecli's
	// own login command is named -- not gh's.
	gitIn(t, "", "clone", "-q", f.remote("bsalmon-test"), filepath.Join(t.TempDir(), "ok"))
	writeRaw(t, f.state, "logged-out", "")
	out = clone()
	if !strings.Contains(out, "api user` failed too") || !strings.Contains(out, "the forgecli login on the grain host is no longer valid") ||
		!strings.Contains(out, "GH_HOST=depot.code.forge.test forgecli auth login") || strings.Contains(out, "gh auth login --hostname") {
		t.Errorf("lapsed forgecli login said %q", out)
	}
	os.Remove(filepath.Join(f.state, "logged-out"))

	// The login lapsed: the front end's redirect to the forge login still says to renew it.
	writeRaw(t, f.state, "corp-login", "")
	out = clone()
	if !strings.Contains(out, "forge login needed: run `forgecli-login`") || strings.Contains(out, "auth login") {
		t.Errorf("corp login redirect said %q", out)
	}
	assertNoTokenLogged(t)
}

func TestCISandboxSingleTenantAmbientGGHOnNonDefaultHost(t *testing.T) {
	f := newDepotFixture(t, "bsalmon-test")
	f.seed(t, "bsalmon-test")

	// In the bare backend repo, push a feature branch representing a task's pushed branch.
	seedWork := filepath.Join(t.TempDir(), "seed-branch")
	runCmd(t, "", "git", "clone", "-q", filepath.Join(f.root, "experimental", "bsalmon-test.git"), seedWork)
	runCmd(t, seedWork, "git", "checkout", "-b", "grain/eu/task-4622")
	commitFile(t, seedWork, "pushed.txt", []byte("ci-ambient-depot-content\n"))
	runCmd(t, seedWork, "git", "push", "-q", "origin", "grain/eu/task-4622")

	// Set up single-tenant store with default team having GitConnection to depot.
	store, ctx := openStoreForTest(t)
	store.SingleTenant = true
	if err := store.UpdateConfig(ctx, func(tm *model.Config) error {
		tm.GitConnections = []model.TeamGitConnection{{Credential: "depot", Host: depotHost}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	reads := NewReadScopes()
	reads.Grant("ci-sandbox", model.RepoRef{Host: depotHost, Owner: "experimental", Name: "bsalmon-test"})
	teams := NewSandboxTeams()
	teams.Grant("ci-sandbox", model.DefaultTeamID)

	tokens, err := LoadSandboxTokens(writeTokensFile(t, t.TempDir(), map[string]string{"ci-sandbox": "tok-ci"}))
	if err != nil {
		t.Fatal(err)
	}

	ciProxy := &GitProxy{
		Authorizer:      &ReadOnlyScopeAuthorizer{Scopes: reads, Next: stubAuthorizer{}},
		Credentials:     f.creds,
		Tokens:          tokens,
		SandboxTeams:    teams,
		TeamCredentials: store,
		SingleTenant:    true,
	}
	ciSrv := httptest.NewServer(NewHandler(ciProxy))
	t.Cleanup(ciSrv.Close)

	// Checkout of the task's pushed branch through the proxy.
	cloneWork := filepath.Join(t.TempDir(), "ci-clone")
	remoteURL := fmt.Sprintf("http://x-access-token:tok-ci@%s/%s/experimental/bsalmon-test.git",
		strings.TrimPrefix(ciSrv.URL, "http://"), depotHost)

	gitIn(t, "", "clone", "-q", "--branch", "grain/eu/task-4622", remoteURL, cloneWork)
	if data, err := os.ReadFile(filepath.Join(cloneWork, "pushed.txt")); err != nil || string(data) != "ci-ambient-depot-content\n" {
		t.Fatalf("cloned pushed.txt = %q, %v", data, err)
	}
}
