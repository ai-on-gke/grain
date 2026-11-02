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
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/github"
)

// depotWorkflowRefusal is what depot.code.corp.goog says, word for word,
// to a push that adds a workflow with a token lacking the scope.
const depotWorkflowRefusal = "refusing to allow an OAuth App to create or update workflow " +
	"`.github/workflows/ci.yml` without `workflow` scope"

// pushResult is a receive-pack answer rejecting refs/heads/main with
// reason, as rejectPush builds one: the reason as "remote:" text on the
// progress band and on the ng line, with side-band, or the bare report
// without.
func pushResult(t *testing.T, reason string, sideband bool) []byte {
	t.Helper()
	caps := []string{"report-status"}
	if sideband {
		caps = append(caps, "side-band-64k")
	}
	resp, ok := rejectPush([]RefUpdate{{Ref: "refs/heads/main"}}, caps, reason)
	if !ok {
		t.Fatal("rejectPush built no answer")
	}
	return resp.Body
}

// resultForwarder answers every request 200 with body.
type resultForwarder struct{ body []byte }

func (f *resultForwarder) Forward(method, path, query string, headers map[string]string, body []byte, token *string) (UpstreamResponse, error) {
	return UpstreamResponse{Status: 200, Headers: map[string]string{"Content-Type": "application/x-git-receive-pack-result"},
		Body: f.body}, nil
}

// hintProxy is a proxy whose "op" credential is an ambient login from
// source, forwarding to forwarder.
func hintProxy(t *testing.T, source string, forwarder Forwarder) *GitProxy {
	t.Helper()
	LogToTest(t)
	state := fakeHostTools(t)
	writeState(t, state, "gh-token", "gho_hint")
	c, _ := ambientSet(t)
	if err := c.SetAmbient("op", "", source); err != nil {
		t.Fatal(err)
	}
	tokens, err := LoadSandboxTokens(writeTokensFile(t, t.TempDir(), map[string]string{"sandbox-0": "tok0"}))
	if err != nil {
		t.Fatal(err)
	}
	return &GitProxy{
		Audit:               &RecordingAuditLog{},
		Authorizer:          stubAuthorizer{allow: map[[3]string]bool{{"sandbox-0", "owner", "repo"}: true}},
		Credentials:         c,
		TeamCredentials:     ladderConnections{creds: c},
		CredentialOverrides: fixedOverride("op"),
		Tokens:              tokens,
		Forwarder:           forwarder,
	}
}

func readAll(t *testing.T, resp ProxyResponse) string {
	t.Helper()
	if resp.Stream == nil {
		return string(resp.Body)
	}
	defer resp.Stream.Close()
	data, err := io.ReadAll(resp.Stream)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// The builtin gh source knows a missing `workflow` scope when it sees
// one, and says what to run: on the progress band when the client asked
// for one, on the ng line otherwise. Every byte the host sent still
// arrives. A rejection that is about something else gets nothing.
func TestARejectedPushCarriesTheSourcesHint(t *testing.T) {
	for _, tc := range []struct {
		name     string
		reason   string
		sideband bool
		hint     bool
	}{
		{"workflow scope, side-band", depotWorkflowRefusal, true, true},
		{"workflow scope, no side-band", depotWorkflowRefusal, false, true},
		{"non-fast-forward", "non-fast-forward", true, false},
		{"protected branch", "protected branch hook declined", true, false},
		{"app permission", "refusing to allow a GitHub App to create or update workflow `.github/workflows/ci.yml` " +
			"without `workflows` permission", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := pushResult(t, tc.reason, tc.sideband)
			p := hintProxy(t, AmbientSourceGH, &resultForwarder{body: upstream})
			resp := pushWith(t, p)
			if resp.Status != 200 {
				t.Fatalf("push = %d %s", resp.Status, resp.Body)
			}
			got := readAll(t, resp)
			const fix = "gh auth refresh --hostname github.com --scopes repo,workflow"
			if !tc.hint {
				if got != string(upstream) {
					t.Fatalf("answer = %q, want the host's own %q untouched", got, upstream)
				}
				return
			}
			if !strings.Contains(got, fix) {
				t.Fatalf("answer = %q, want it to name %s", got, fix)
			}
			if tc.sideband {
				// The host's own bytes, then the hint, then the flush.
				if !strings.HasPrefix(got, string(upstream[:len(upstream)-4])) || !strings.HasSuffix(got, "0000") {
					t.Fatalf("answer = %q, want the host's %q with the hint before its final flush", got, upstream)
				}
				if !strings.Contains(got, "\x02grain: run `"+fix) {
					t.Fatalf("answer = %q, want the hint as a progress line", got)
				}
			} else if !strings.Contains(got, "ng refs/heads/main "+depotWorkflowRefusal+" -- grain: run `"+fix) {
				t.Fatalf("answer = %q, want the hint on the ng line", got)
			}
		})
	}
}

// A push the host took is passed through as it came.
func TestAnAcceptedPushIsPassedThrough(t *testing.T) {
	var report, body bytes.Buffer
	writePkt(&report, "unpack ok\n")
	writePkt(&report, "ok refs/heads/main\n")
	report.WriteString("0000")
	writePkt(&body, "\x02Resolving deltas: 100% (1/1)\n")
	writePkt(&body, "\x01"+report.String())
	body.WriteString("0000")
	p := hintProxy(t, AmbientSourceGH, &resultForwarder{body: body.Bytes()})
	if got := readAll(t, pushWith(t, p)); got != body.String() {
		t.Fatalf("answer = %q, want %q", got, body.String())
	}
}

// A registered source's own hint is what is said: the shape the Google
// layer's ggh.WorkflowRefusedHint has.
func TestARegisteredSourcesPushRefusedHint(t *testing.T) {
	var asked []string
	t.Cleanup(RegisterAmbientSource(AmbientCommandSource{
		Name:    "hinted",
		Command: "gh",
		Args:    []string{"auth", "token", "--hostname", "{host}"},
		PushRefusedHint: func(host, message string) string {
			asked = append(asked, host+": "+message)
			if !WorkflowScopeRefused(message) {
				return ""
			}
			return "run `GH_HOST=" + host + " ggh auth refresh --scopes repo,workflow` as the daemon's account"
		},
	}))
	p := hintProxy(t, "hinted", &resultForwarder{body: pushResult(t, depotWorkflowRefusal, true)})
	got := readAll(t, pushWith(t, p))
	if !strings.Contains(got, "grain: run `GH_HOST=github.com ggh auth refresh --scopes repo,workflow`") {
		t.Fatalf("answer = %q, want the source's hint", got)
	}
	if len(asked) != 1 || !strings.HasPrefix(asked[0], "github.com: ") || !strings.Contains(asked[0], depotWorkflowRefusal) {
		t.Fatalf("hint asked %q, want once, for github.com, with the host's text", asked)
	}

	// A source with no hint leaves the answer alone.
	t.Cleanup(RegisterAmbientSource(AmbientCommandSource{
		Name: "plain", Command: "gh", Args: []string{"auth", "token", "--hostname", "{host}"},
	}))
	upstream := pushResult(t, depotWorkflowRefusal, true)
	p = hintProxy(t, "plain", &resultForwarder{body: upstream})
	if got := readAll(t, pushWith(t, p)); got != string(upstream) {
		t.Fatalf("answer with no hook = %q, want %q", got, upstream)
	}
}

// A grain-made push (an auto-port's, say) through the proxy is a git
// client like a sandbox's: the hint reaches its error, which is what
// the task conversation is told.
func TestAGrainMadePushThroughTheProxyNamesTheHint(t *testing.T) {
	LogToTest(t)
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	// gh on PATH is a fake; git is the real one.
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\necho gho_hint\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+filepath.Dir(gitPath)+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GRAIN_IN_CONTAINER", "0")
	t.Setenv("HOME", t.TempDir())

	root := t.TempDir()
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}
	bare := filepath.Join(root, "owner", "repo.git")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	git(bare, "init", "--bare", "-q", "-b", "main")
	git(bare, "config", "http.receivepack", "true")
	backend := localGitHTTPBackend(root)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/git-receive-pack") {
			backend(w, r)
			return
		}
		// The host turns the push down the way depot does.
		commands, err := commandList(r.Body, r.Header.Get("Content-Encoding"))
		if err != nil {
			t.Errorf("reading the push: %v", err)
			return
		}
		updates, caps, err := parseReceivePack(commands)
		if err != nil {
			t.Errorf("parsing the push: %v", err)
			return
		}
		resp, _ := rejectPush(updates, caps, depotWorkflowRefusal)
		io.Copy(io.Discard, r.Body)
		WriteResponse(w, resp)
	}))
	defer upstream.Close()

	c, _ := ambientSet(t)
	if err := c.SetAmbient("op", "", AmbientSourceGH); err != nil {
		t.Fatal(err)
	}
	tokens, err := LoadSandboxTokens(writeTokensFile(t, t.TempDir(), map[string]string{"sandbox-0": "tok0"}))
	if err != nil {
		t.Fatal(err)
	}
	p := &GitProxy{
		Audit:               &RecordingAuditLog{},
		Authorizer:          stubAuthorizer{allow: map[[3]string]bool{{"sandbox-0", "owner", "repo"}: true}},
		Credentials:         c,
		TeamCredentials:     ladderConnections{creds: c},
		CredentialOverrides: fixedOverride("op"),
		Tokens:              tokens,
		Forwarder:           &RealForwarder{Host: strings.TrimPrefix(upstream.URL, "http://")},
	}
	proxy := httptest.NewServer(NewHandler(p))
	defer proxy.Close()

	work := filepath.Join(root, "work")
	git(root, "init", "-q", "-b", "main", work)
	if err := os.MkdirAll(filepath.Join(work, ".github", "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, ".github", "workflows", "ci.yml"), []byte("on: push\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(work, "add", ".")
	git(work, "-c", "user.name=grain", "-c", "user.email=grain@localhost", "commit", "-q", "-m", "ci")
	remote := strings.Replace(proxy.URL, "http://", "http://x-access-token:tok0@", 1) + "/owner/repo.git"
	cmd := exec.Command("git", "push", "--quiet", remote, "HEAD:refs/heads/main")
	cmd.Dir = work
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("push succeeded:\n%s", out)
	}
	for _, want := range []string{
		"[remote rejected]",
		depotWorkflowRefusal,
		"remote: grain: run `gh auth refresh --hostname github.com --scopes repo,workflow`",
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("git push said:\n%s\nwant it to say %q", out, want)
		}
	}
}

func TestAmbientIdentityWarnsOfAMissingWorkflowScope(t *testing.T) {
	state := fakeHostTools(t)
	writeState(t, state, "gh-token", "gho_who")
	for _, tc := range []struct {
		scopes             string
		noRepo, noWorkflow bool
	}{
		{"repo, read:org", false, true},
		{"repo, workflow, gist", false, false},
		{"gist", true, true},
		{"", false, false}, // no header: a fine-grained or App token
	} {
		headers := map[string]string{}
		if tc.scopes != "" {
			headers["X-Oauth-Scopes"] = tc.scopes
		}
		c, _ := ambientSet(t)
		c.AppTransport = github.NewFakeTransport(github.ApiResponse{Status: 200, Body: []byte(`{"login":"op"}`), Headers: headers})
		if err := c.SetAmbient("op", "github.com", "gh"); err != nil {
			t.Fatal(err)
		}
		id, err := c.AmbientIdentityOf("op")
		if err != nil {
			t.Fatalf("%q: %v", tc.scopes, err)
		}
		if id.NoRepoScope != tc.noRepo || id.NoWorkflowScope != tc.noWorkflow {
			t.Errorf("scopes %q: NoRepoScope=%v NoWorkflowScope=%v, want %v %v",
				tc.scopes, id.NoRepoScope, id.NoWorkflowScope, tc.noRepo, tc.noWorkflow)
		}
	}
	if got := AmbientScopeFix("github.com", "gh"); got != "gh auth refresh --hostname github.com --scopes repo,workflow" {
		t.Errorf("AmbientScopeFix = %q", got)
	}
}
