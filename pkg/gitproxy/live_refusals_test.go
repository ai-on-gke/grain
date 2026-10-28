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

package gitproxy_test

// What a real `git` process does with the refusals refusals.go writes.
//
// Everything in refusals_test.go is about the ProxyResponse: which status
// carries which sentence. None of it says the sentence is ever read by
// anybody, and a message an operator never sees is worth nothing -- git
// prints a response body only when it is told the body is text, and only
// for some statuses, so "the proxy returned this string" and "the
// terminal showed this string" are two different claims. These tests make
// the second one, by driving a real git client through a real proxy in
// front of githubsim.GitUpstream (the measured stand-in for github.com's
// git auth layer) and reading what came out of git.
//
// The stack is live_test.go's, with one layer inserted: GitUpstream sits
// between the proxy and the `git http-backend` that serves the actual
// repository, so a request that gets past the door still clones and
// pushes for real, and one that does not gets github.com's own status and
// body. That is what makes a *failure on a credential* arrangeable at
// all -- until GitUpstream existed the only refusal any double here could
// produce was the proxy's own.
//
// TestA401FromTheProxyWouldEraseTheSandboxsCredential is the odd one out
// and the reason this file is worth its length: it is not about a message
// but about the status carrying it, and it asks a real git for the 401
// refusals.go deliberately does not send.

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
)

// sandboxToken is what the sandbox's git holds, and botToken is what the
// proxy sends upstream on its behalf. They are deliberately different
// strings: the whole point of the 401 handling is that the credential
// GitHub refuses is never the one the sandbox has.
const (
	sandboxToken = "live-refusal-sandbox-token"
	botToken     = "live-refusal-bot-token"
)

// refusalRig is one sandbox pointed at one repository through the whole
// stack: a `git` process with the sandbox's own credentials, a proxy
// scoped by the model, GitUpstream deciding whether the bot credential
// gets in, and a bare repo behind it for the requests that do.
type refusalRig struct {
	// home is the sandbox's HOME -- ConfigureGitCredentials' root, so
	// git finds .gitconfig (credential.helper = store) and
	// .git-credentials exactly where a dispatched sandbox's git would.
	home string
	// remote is the proxy URL of owner/widgets, the repo the model
	// scopes this sandbox to.
	remote string
	// bare is the upstream repository on disk, for a test that wants to
	// check what a push actually did or did not land.
	bare string
}

// newRefusalRig builds that stack with access as the bot credential's
// standing at GitHub -- the one knob these tests turn. An empty access
// map is a bot token GitHub has never heard of, which is what a revoked
// or mistyped one looks like.
func newRefusalRig(t *testing.T, tokens map[string]githubsim.GitAccess) refusalRig {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	upstreamDir := t.TempDir()
	bare := newBareRepoWithOneCommit(t, upstreamDir, "owner", "widgets")
	upstream := httptest.NewServer(&githubsim.GitUpstream{
		Tokens:  tokens,
		Backend: gitHTTPBackend(upstreamDir),
	})
	t.Cleanup(upstream.Close)

	store, ctx := openStore(t)
	task := model.Task{
		ID: "t1", Intent: model.IntentImplement, Title: "test task",
		Origin: model.Origin{
			Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "tester"}},
			Reason:      model.ReasonDirect,
		},
		Target:  &model.RepoRef{Owner: "owner", Name: "widgets"},
		Binding: model.BindingDirective,
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: "t1-1", TaskID: "t1", Sandbox: "sandbox-0",
		Attempt: 1, StartedAt: time.Now(),
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}

	dataDir := t.TempDir()
	mustWriteFile(t, filepath.Join(dataDir, "secrets", "github", "credentials.json"), `{"*": "bot"}`)
	giveTeamCredential(t, store, ctx, model.DefaultTeamID, "bot")
	mustWriteFile(t, filepath.Join(dataDir, "secrets", "github", "bot.token"), botToken)
	mustWriteFile(t, filepath.Join(dataDir, "secrets", "sandbox-tokens.json"),
		fmt.Sprintf(`{"sandbox-0": %q}`, sandboxToken))

	proxy, err := gitproxy.BuildProxy(gitproxy.BuildConfig{
		DataDir: dataDir, Store: store,
		ForwardHost: mustHost(t, upstream.URL), ForwardTLS: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	proxyServer := httptest.NewServer(gitproxy.NewHandler(proxy))
	t.Cleanup(proxyServer.Close)

	remote := proxyServer.URL + "/owner/widgets.git"
	return refusalRig{home: sandboxHome(t, remote), remote: remote, bare: bare}
}

// sandboxHome is a HOME with the git credentials a dispatch writes into
// a sandbox -- the proxy token behind credential.helper = store, never
// on a command line, which is what makes the erasure in
// TestA401FromTheProxyWouldEraseTheSandboxsCredential possible at all.
func sandboxHome(t *testing.T, remote string) string {
	t.Helper()
	home := t.TempDir()
	if err := mcp.ConfigureGitCredentials(home, remote, sandboxToken, mcp.GitIdentity{}); err != nil {
		t.Fatal(err)
	}
	return home
}

// git runs one git command as the sandbox would: with the sandbox's HOME
// so its credential helper is found, and with no terminal to prompt at,
// so a git that wants a username fails instead of hanging this test.
func git(t *testing.T, home, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=",
		"SSH_ASKPASS=",
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// credentialsFile is what the sandbox's ~/.git-credentials holds now --
// git-credential-store rewrites it in place, so this is read after the
// git command rather than before.
func credentialsFile(t *testing.T, home string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(home, ".git-credentials"))
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

// mustPrintRemote asserts that every line of body reached the terminal as
// a git "remote:" line -- the whole sentence, not a recognizable piece of
// it. Truncation is the failure worth catching: these messages are long,
// and the fact an operator needs (which credentials.json entry) is at the
// end of them.
func mustPrintRemote(t *testing.T, output, body string) {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		if !strings.Contains(output, "remote: "+line) {
			t.Errorf("git printed:\n%s\nwant a \"remote: \" line carrying the whole of:\n%s", output, line)
		}
	}
}

// refusalBody asks the proxy the same question git is about to ask it,
// and returns the sentence it answers with. Taking the expected text from
// the proxy itself, rather than copying refusals.go's wording into an
// assertion, keeps this file about what git does with a message and lets
// the message be reworded without touching it.
func refusalBody(t *testing.T, remote, action, query string) string {
	t.Helper()
	req, err := http.NewRequest("GET", remote+"/"+action+"?"+query, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth("sandbox", sandboxToken)
	req.Header.Set("User-Agent", "git/2.39.5")
	req.Header.Set("Accept", "*/*")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode < 400 {
		t.Fatalf("proxy answered %d, want a refusal to compare git's output against", resp.StatusCode)
	}
	return string(body)
}

// A clone of a repository the deployment's credential cannot see: GitHub
// answers 404 rather than admitting the repository exists, and the whole
// of the proxy's substitution for "Repository not found" has to reach the
// terminal, because naming the credential is the entire point of it.
func TestLiveCloneOfARepoTheCredentialCannotSeePrintsTheWholeExplanation(t *testing.T) {
	rig := newRefusalRig(t, map[string]githubsim.GitAccess{
		botToken: {Read: []string{"owner/something-else"}},
	})

	out, err := git(t, rig.home, t.TempDir(), "clone", rig.remote, "clone")
	if err == nil {
		t.Fatalf("expected the clone to fail, output:\n%s", out)
	}
	mustPrintRemote(t, out, refusalBody(t, rig.remote, "info/refs", "service=git-upload-pack"))
	// The sandbox's own credential was never at fault and is still there
	// for the next command in the run.
	if !strings.Contains(credentialsFile(t, rig.home), sandboxToken) {
		t.Error("the sandbox's ~/.git-credentials lost its proxy token on a 404")
	}
}

// A push with a credential GitHub accepts for reading and refuses for
// writing -- the read-only fine-grained token, the App installation
// granted Contents: read. The clone in front of it is half the test: this
// is a working remote, so the sentence is not a failure of the rig.
func TestLivePushWithACredentialThatMayNotWritePrintsTheWholeExplanation(t *testing.T) {
	rig := newRefusalRig(t, map[string]githubsim.GitAccess{
		botToken: {Read: []string{"owner/widgets"}},
	})

	dir := t.TempDir()
	if out, err := git(t, rig.home, dir, "clone", rig.remote, "clone"); err != nil {
		t.Fatalf("clone should have worked with a readable credential: %v\n%s", err, out)
	}
	work := filepath.Join(dir, "clone")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("sandbox was here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := git(t, rig.home, work, "commit", "-am", "sandbox commit", "-q"); err != nil {
		t.Fatalf("commit: %v\n%s", err, out)
	}

	out, err := git(t, rig.home, work, "push", "origin", "main")
	if err == nil {
		t.Fatalf("expected the push to fail, output:\n%s", out)
	}
	mustPrintRemote(t, out, refusalBody(t, rig.remote, "info/refs", "service=git-receive-pack"))
	if !strings.Contains(credentialsFile(t, rig.home), sandboxToken) {
		t.Error("the sandbox's ~/.git-credentials lost its proxy token on a refused push")
	}
}

// GitHub refusing the deployment's credential outright (401): the
// sandbox hears a 403 carrying the whole explanation, and keeps the
// credential of its own that was never in question. The next test is
// what the alternative would have cost.
func TestLiveCloneWithARevokedCredentialPrintsTheWholeExplanation(t *testing.T) {
	rig := newRefusalRig(t, nil) // no token GitHub knows

	out, err := git(t, rig.home, t.TempDir(), "clone", rig.remote, "clone")
	if err == nil {
		t.Fatalf("expected the clone to fail, output:\n%s", out)
	}
	body := refusalBody(t, rig.remote, "info/refs", "service=git-upload-pack")
	mustPrintRemote(t, out, body)
	// GitHub's own sentence, quoted back inside the proxy's -- proving
	// the quote survives the trip to the terminal too, since it is the
	// half of the message that carries an SSO authorization URL in the
	// case that needs one.
	if !strings.Contains(out, "Invalid username or token") {
		t.Errorf("git printed:\n%s\nwant GitHub's own sentence quoted in it", out)
	}
	if !strings.Contains(credentialsFile(t, rig.home), sandboxToken) {
		t.Errorf("the sandbox's ~/.git-credentials lost its proxy token: %q",
			credentialsFile(t, rig.home))
	}
}

// The hazard refusals.go answers 403 to avoid, made a fact rather than a
// reading of git's source: if the proxy passed GitHub's 401 through, git
// would take it as a verdict on the credential it used -- the proxy token
// it got from credential.helper = store, which authenticated fine -- and
// tell the helper to erase it. Every later git command in that run then
// fails asking for a username, with the explanation buried somewhere
// above.
//
// So this test does not go through the proxy at all: it asks a server
// that answers 401 the way GitHub does, and looks at what is left of the
// sandbox's ~/.git-credentials afterwards. Anyone tempted to "fix" the
// status mismatch in upstreamRefusal by forwarding the 401 will find out
// here what it costs.
func TestA401FromTheProxyWouldEraseTheSandboxsCredential(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	for _, tc := range []struct {
		name   string
		status int
		// erased is whether the sandbox's stored proxy token is gone
		// afterwards.
		erased bool
	}{
		{"401, the status GitHub sent", 401, true},
		{"403, the status the proxy substitutes", 403, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The proxy's own two steps, and only they differ in the
			// status of the second: an unauthenticated request is
			// challenged (core.go's "authentication required"), and the
			// request that comes back carrying the sandbox's token --
			// the one it would lose -- is answered with the refusal.
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				resp := gitproxy.Denial(tc.status,
					"owner/widgets: the proxy's explanation of an upstream refusal")
				if r.Header.Get("Authorization") == "" {
					resp = gitproxy.Denial(401, "authentication required")
					resp.Headers["WWW-Authenticate"] = `Basic realm="grain-git-proxy"`
				}
				for k, v := range resp.Headers {
					w.Header().Set(k, v)
				}
				w.WriteHeader(resp.Status)
				w.Write(resp.Body)
			}))
			defer server.Close()

			remote := server.URL + "/owner/widgets.git"
			home := sandboxHome(t, remote)
			out, err := git(t, home, t.TempDir(), "clone", remote, "clone")
			if err == nil {
				t.Fatalf("expected the clone to fail, output:\n%s", out)
			}

			// Both statuses put the explanation on the terminal, so what
			// separates them really is only what the sandbox is left
			// holding afterwards.
			mustPrintRemote(t, out, "owner/widgets: the proxy's explanation of an upstream refusal")

			stored := credentialsFile(t, home)
			if erased := !strings.Contains(stored, sandboxToken); erased != tc.erased {
				t.Errorf("after a %d, ~/.git-credentials = %q (erased=%v), want erased=%v\ngit said:\n%s",
					tc.status, stored, erased, tc.erased, out)
			}
		})
	}
}

// Why Denial sets a Content-Type at all, asked of a real git rather than
// asserted about the header. git prints a refusal's body as "remote:"
// lines only when the response says it is text; the same bytes under any
// other content type are read as a payload it should not be showing a
// human, and every message in refusals.go silently stops arriving. The
// content type is the load-bearing half of these responses, and nothing
// about a wrong one is visible in the status a test would otherwise
// check.
func TestGitPrintsARefusalOnlyWhenItIsToldTheBodyIsText(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	const message = "owner/widgets: the proxy's explanation of an upstream refusal"
	for _, tc := range []struct {
		contentType string
		printed     bool
	}{
		{"text/plain; charset=utf-8", true}, // what Denial sends
		{"application/octet-stream", false},
		// What upstream's own refusal can arrive as, and what passing one
		// through unchanged would therefore hand the sandbox.
		{"application/x-git-upload-pack-advertisement", false},
	} {
		t.Run(tc.contentType, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(http.StatusForbidden)
				fmt.Fprintln(w, message)
			}))
			defer server.Close()

			remote := server.URL + "/owner/widgets.git"
			out, err := git(t, sandboxHome(t, remote), t.TempDir(), "clone", remote, "clone")
			if err == nil {
				t.Fatalf("expected the clone to fail, output:\n%s", out)
			}
			if printed := strings.Contains(out, "remote: "+message); printed != tc.printed {
				t.Errorf("Content-Type %q: git printed the explanation = %v, want %v\ngit said:\n%s",
					tc.contentType, printed, tc.printed, out)
			}
		})
	}
}
