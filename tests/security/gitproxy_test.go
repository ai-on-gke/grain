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

package security

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite/sqlitetest"
)

// The git proxy is the only way a sandbox reaches a repository, and the
// only place grain's GitHub credential is attached to anything a sandbox
// sends. What a sandbox's token may reach is its own live run's scope:
// the task's target for fetch and push, the task's Reads for fetch only,
// and nothing else -- whichever sandbox's token it is.
//
// Driven through gitproxy.BuildProxy and gitproxy.NewHandler, the same
// wiring the daemon serves, against a real model store and a stand-in
// "GitHub" that records what reached it. A refusal is only a refusal if
// the upstream never saw the request.

// gitUpstream is the stand-in GitHub: it answers every request 200 and
// remembers the path of each one.
type gitUpstream struct {
	mu   sync.Mutex
	seen []string
}

func (u *gitUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	u.seen = append(u.seen, r.Method+" "+r.URL.Path+" "+string(body))
	u.mu.Unlock()
	w.Header().Set("Content-Type", "application/x-git-result")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("upstream-ok"))
}

func (u *gitUpstream) reached(fragment string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, s := range u.seen {
		if strings.Contains(s, fragment) {
			return true
		}
	}
	return false
}

// gitProxyRig is one proxy over one store with three live runs:
// sandbox-a on acme/widgets (reading acme/shared-lib), sandbox-b on
// other/gadgets, and sandbox-c on acme/gizmos holding self-debug.
type gitProxyRig struct {
	url      string
	upstream *gitUpstream
	tokens   map[string]string
}

func newGitProxyRig(t *testing.T) *gitProxyRig {
	t.Helper()
	store, _ := sqlitetest.Open(t)
	ctx := context.Background()

	runs := []struct {
		task, sandbox string
		target        model.RepoRef
		reads         []model.RepoRef
		grants        []model.Grant
	}{
		{"ta", "sandbox-a", model.RepoRef{Owner: "acme", Name: "widgets"}, []model.RepoRef{{Owner: "acme", Name: "shared-lib"}}, nil},
		// tb is a release job: an operator granted it git-push-any-ref.
		{"tb", "sandbox-b", model.RepoRef{Owner: "other", Name: "gadgets"}, nil,
			[]model.Grant{{Capability: model.GitPushAnyRefCapability, Via: model.GrantByLabel}}},
		// tc is a self-debug run, which reads grain's own source. It holds
		// git-push-any-ref as well, so a refused push to grain is the
		// scope's refusal and not the one-branch rule's.
		{"tc", "sandbox-c", model.RepoRef{Owner: "acme", Name: "gizmos"}, nil,
			[]model.Grant{
				{Capability: model.SelfDebugCapability, Via: model.GrantByLabel},
				{Capability: model.GitPushAnyRefCapability, Via: model.GrantByLabel},
			}},
		// td holds self-debug in its team-scoped form: payments is not a
		// team Settings has widened self-debug to.
		{"td", "sandbox-d", model.RepoRef{Owner: "acme", Name: "ledger"}, nil,
			[]model.Grant{{Capability: model.SelfDebugCapability, Via: model.GrantByLabel}}},
		// te names a per-team configuration repository as its target and
		// another as a read: what a task filed against grain/config-default
		// before teams went would be scoped to. Neither exists.
		{"te", "sandbox-e", model.RepoRef{Owner: "grain", Name: "config-default"},
			[]model.RepoRef{{Owner: "grain", Name: "config-alpha"}}, nil},
	}
	for _, r := range runs {
		target := r.target
		task := model.Task{
			ID: r.task, Intent: model.IntentImplement, Title: "security boundary",
			Origin: model.Origin{
				Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "tester"}},
				Reason:      model.ReasonDirect,
			},
			Target:  &target,
			Reads:   r.reads,
			Grants:  r.grants,
			Binding: model.BindingDirective,
		}
		if err := store.PutTask(ctx, task); err != nil {
			t.Fatal(err)
		}
		if err := store.StartRun(ctx, model.Run{
			ID: r.task + "-1", TaskID: r.task, Sandbox: r.sandbox, Attempt: 1, StartedAt: time.Now(),
		}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
	}

	upstream := &gitUpstream{}
	backend := httptest.NewServer(upstream)
	t.Cleanup(backend.Close)
	backendURL, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}

	tokens := map[string]string{"sandbox-a": "token-for-a", "sandbox-b": "token-for-b", "sandbox-c": "token-for-c", "sandbox-d": "token-for-d", "sandbox-e": "token-for-e"}
	dataDir := t.TempDir()
	writeSecret(t, filepath.Join(dataDir, "secrets", "github", "credentials.json"), `{"*": "bot"}`)
	writeSecret(t, filepath.Join(dataDir, "secrets", "github", "bot.token"), "bot-token")
	writeSecret(t, filepath.Join(dataDir, "secrets", "sandbox-tokens.json"),
		fmt.Sprintf(`{"sandbox-a": %q, "sandbox-b": %q, "sandbox-c": %q, "sandbox-d": %q, "sandbox-e": %q}`,
			tokens["sandbox-a"], tokens["sandbox-b"], tokens["sandbox-c"], tokens["sandbox-d"], tokens["sandbox-e"]))
	// A team's work clones and pushes with the team's own credential and
	// never the deployment's ladder (grain/task-1592), so both runs' team
	// names "bot" the way the upgrade's credential migration would.
	if err := store.UpdateConfig(ctx, func(tm *model.Config) error {
		tm.GitCredential = "bot"
		return nil
	}); err != nil {
		t.Fatalf("giving the default team its credential: %v", err)
	}

	proxy, err := gitproxy.BuildProxy(gitproxy.BuildConfig{
		DataDir: dataDir, Store: store, ForwardHost: backendURL.Host, ForwardTLS: false,
		// Wired as cmd/grain's startGitProxy wires them, so the proxy
		// under test has the authorizer chain a deployment runs: a
		// ReadOnlyScopeAuthorizer in front of the model, which once
		// skipped the push ref check altogether (#2387).
		ReadScopes: gitproxy.NewReadScopes(), WriteScopes: gitproxy.NewWriteScopes(),
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(gitproxy.NewHandler(proxy))
	t.Cleanup(server.Close)
	return &gitProxyRig{url: server.URL, upstream: upstream, tokens: tokens}
}

func writeSecret(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// gitRequest is one smart-HTTP request as a git client sends it: fetch
// is the upload-pack half, push the receive-pack half, each starting
// with its info/refs advertisement.
func (r *gitProxyRig) gitRequest(t *testing.T, sandbox, repo, service string, advertisement bool, body []byte) (int, string) {
	t.Helper()
	var req *http.Request
	var err error
	if advertisement {
		req, err = http.NewRequest(http.MethodGet, r.url+"/"+repo+".git/info/refs?service="+service, nil)
		if err == nil {
			req.Header.Set("Accept", "*/*")
		}
	} else {
		req, err = http.NewRequest(http.MethodPost, r.url+"/"+repo+".git/"+service, bytes.NewReader(body))
		if err == nil {
			req.Header.Set("Accept", "application/x-"+service+"-result")
			req.Header.Set("Content-Type", "application/x-"+service+"-request")
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("User-Agent", "git/2.43.0")
	if token, ok := r.tokens[sandbox]; ok {
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("x:"+token)))
	} else if sandbox != "" {
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("x:"+sandbox)))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	text, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(text)
}

func (r *gitProxyRig) fetch(t *testing.T, sandbox, repo string) (int, string) {
	t.Helper()
	if status, text := r.gitRequest(t, sandbox, repo, "git-upload-pack", true, nil); status != http.StatusOK {
		return status, text
	}
	return r.gitRequest(t, sandbox, repo, "git-upload-pack", false, []byte("0000"))
}

func (r *gitProxyRig) push(t *testing.T, sandbox, repo string, commands []byte) (int, string) {
	t.Helper()
	if status, text := r.gitRequest(t, sandbox, repo, "git-receive-pack", true, nil); status != http.StatusOK {
		return status, text
	}
	return r.gitRequest(t, sandbox, repo, "git-receive-pack", false, commands)
}

// receivePackUpdate is the pkt-line command a push opens with: move ref
// from one object to another.
func receivePackUpdate(ref string) []byte {
	line := fmt.Sprintf("%s %s %s\x00report-status\n", strings.Repeat("1", 40), strings.Repeat("2", 40), ref)
	return []byte(fmt.Sprintf("%04x%s0000", len(line)+4, line))
}

func TestGitProxyScopesEachSandboxToItsOwnRepositories(t *testing.T) {
	rig := newGitProxyRig(t)

	// Its own target: fetch and push.
	if status, text := rig.fetch(t, "sandbox-a", "acme/widgets"); status != http.StatusOK {
		t.Fatalf("sandbox-a fetching its own target: %d %s", status, text)
	}
	if status, text := rig.push(t, "sandbox-a", "acme/widgets", receivePackUpdate("refs/heads/grain/task-ta")); status != http.StatusOK {
		t.Fatalf("sandbox-a pushing its own branch of its own target: %d %s", status, text)
	}
	if !rig.upstream.reached("POST /acme/widgets.git/git-receive-pack") {
		t.Fatal("sandbox-a's push of its own target never reached the upstream, so the refusals below prove nothing")
	}

	// A declared read: fetch, and never push.
	if status, text := rig.fetch(t, "sandbox-a", "acme/shared-lib"); status != http.StatusOK {
		t.Fatalf("sandbox-a fetching a repository in its reads: %d %s", status, text)
	}
	// (The receive-pack advertisement is let through: it lists refs, which
	// a fetch reads anyway, and the authorizer sees it as "info/refs"
	// whichever service asked. The push is the POST that follows it.)
	status, text := rig.gitRequest(t, "sandbox-a", "acme/shared-lib", "git-receive-pack", false,
		receivePackUpdate("refs/heads/grain/task-ta"))
	if status != http.StatusForbidden {
		t.Errorf("sandbox-a pushing a read-only repository answered %d %s, want 403", status, text)
	}
	if rig.upstream.reached("POST /acme/shared-lib.git/git-receive-pack") {
		t.Error("a push to a read-only repository reached the upstream")
	}

	// Anything else, including another task's repository, is out of
	// scope -- fetch and push alike -- and says so.
	outside := []struct{ sandbox, repo string }{
		{"sandbox-a", "acme/secrets"},
		{"sandbox-a", "other/gadgets"},
		{"sandbox-b", "acme/widgets"},
		{"sandbox-b", "acme/shared-lib"},
	}
	for _, c := range outside {
		for name, call := range map[string]func() (int, string){
			"fetch": func() (int, string) { return rig.fetch(t, c.sandbox, c.repo) },
			"push": func() (int, string) {
				return rig.push(t, c.sandbox, c.repo, receivePackUpdate("refs/heads/grain/task-ta"))
			},
		} {
			status, text := call()
			if status != http.StatusForbidden || !strings.Contains(text, "not in scope for this sandbox") {
				t.Errorf("%s %s of %s answered %d %q, want 403 \"not in scope for this sandbox\"",
					c.sandbox, name, c.repo, status, text)
			}
		}
	}
	if rig.upstream.reached("/acme/secrets.git") {
		t.Error("a request for a repository in nobody's scope reached the upstream")
	}

	// A token nobody issued, or none, gets nothing -- not even a 403
	// saying what exists.
	for _, sandbox := range []string{"", "forged-token"} {
		if status, _ := rig.fetch(t, sandbox, "acme/widgets"); status != http.StatusUnauthorized {
			t.Errorf("fetch with token %q answered %d, want 401", sandbox, status)
		}
	}
}

// grain/config is the only configuration repository. A per-team name
// (grain/config-default, grain/config-<team>) is answered not found, in
// any case, to a sandbox whose own scope names it as much as to one whose
// does not -- and never forwarded to a forge's grain/ owner with the
// deployment's credential, which is what a name nothing serves locally
// would otherwise become.
func TestGitProxyRefusesTeamConfigRepos(t *testing.T) {
	rig := newGitProxyRig(t)
	for _, c := range []struct{ sandbox, repo string }{
		{"sandbox-e", "grain/config-default"},
		{"sandbox-e", "grain/config-alpha"},
		{"sandbox-e", "Grain/Config-Default"},
		{"sandbox-a", "grain/config-default"},
	} {
		for name, call := range map[string]func() (int, string){
			"fetch": func() (int, string) { return rig.fetch(t, c.sandbox, c.repo) },
			"push": func() (int, string) {
				return rig.push(t, c.sandbox, c.repo, receivePackUpdate("refs/heads/grain/task-te"))
			},
		} {
			status, text := call()
			if status != http.StatusNotFound || !strings.Contains(text, "only grain/config exists") {
				t.Errorf("%s %s of %s answered %d %q, want 404 \"only grain/config exists\"",
					c.sandbox, name, c.repo, status, text)
			}
		}
	}
	if rig.upstream.reached("/grain/") || rig.upstream.reached("/Grain/") {
		t.Error("a request for a team configuration repository reached the upstream")
	}
	// The refusal is not a 404 for every grain/ name: without a token it
	// is still the proxy's 401.
	if status, _ := rig.fetch(t, "", "grain/config-default"); status != http.StatusUnauthorized {
		t.Errorf("an unauthenticated fetch of grain/config-default answered %d, want 401", status)
	}
}

// The self-debug grant widens a run's scope by nothing, in either form:
// its grain-src/ is the deployment's own baked-in copy, placed in the
// sandbox (orchestrator's placeGrainSource), so no self-debug run may
// fetch grain's own repository through the proxy, let alone push to it.
func TestGitProxySelfDebugGrantReadsNoRepository(t *testing.T) {
	rig := newGitProxyRig(t)
	grain := model.GrainSourceRepo().String()

	for _, sandbox := range []string{"sandbox-a", "sandbox-b", "sandbox-c", "sandbox-d"} {
		if status, text := rig.fetch(t, sandbox, grain); status != http.StatusForbidden || !strings.Contains(text, "not in scope for this sandbox") {
			t.Errorf("%s fetching %s answered %d %q, want 403 \"not in scope for this sandbox\"",
				sandbox, grain, status, text)
		}
	}
	for _, ref := range []string{"refs/heads/grain/task-tc", "refs/heads/main"} {
		if status, text := rig.gitRequest(t, "sandbox-c", grain, "git-receive-pack", false, receivePackUpdate(ref)); status != http.StatusForbidden {
			t.Errorf("a self-debug run pushing %s to %s answered %d %s, want 403", ref, grain, status, text)
		}
	}
	if rig.upstream.reached("POST /" + grain + ".git/git-upload-pack") {
		t.Error("a self-debug run's fetch reached grain's own repository upstream")
	}
	if rig.upstream.reached("POST /" + grain + ".git/git-receive-pack") {
		t.Error("a self-debug run's push reached grain's own repository upstream")
	}
}

// knownGapBranchScopedPush was true while the proxy authorized pushes
// per repository only, streaming a receive-pack body to GitHub without
// reading which refs it updated -- so a sandbox could push any branch of
// its target, the default branch included (filed from task 1509, fixed
// by task 1566: pkg/gitproxy/refupdates.go). It is false now, and the
// test below holds the boundary.
const knownGapBranchScopedPush = false

func TestGitProxyPushIsLimitedToTheTasksOwnBranch(t *testing.T) {
	rig := newGitProxyRig(t)
	forwarded := []string{}
	for _, ref := range []string{"refs/heads/main", "refs/heads/grain/task-tb", "refs/heads/release"} {
		// A refusal is a 200 whose report-status says ng, as
		// receive-pack's own would be; any other 200 was accepted.
		status, text := rig.push(t, "sandbox-a", "acme/widgets", receivePackUpdate(ref))
		if status == http.StatusOK && !strings.Contains(text, "ng "+ref+" ") || rig.upstream.reached(ref) {
			forwarded = append(forwarded, ref)
		}
	}
	switch {
	case knownGapBranchScopedPush && len(forwarded) == 0:
		t.Fatal("the proxy now refuses pushes to branches other than the task's own: " +
			"set knownGapBranchScopedPush to false so this boundary is held")
	case knownGapBranchScopedPush:
		t.Skipf("known gap: sandbox-a's push of %v reached GitHub; see knownGapBranchScopedPush", forwarded)
	case len(forwarded) > 0:
		t.Fatalf("sandbox-a's push of %v reached GitHub: a sandbox may push only its own task's branch, "+
			"and never the default branch", forwarded)
	}
}

// git-push-any-ref, an admin grant for release jobs, is the one thing
// that lifts the rule above -- for the run holding it, and in the
// repositories it may push at all.
func TestGitProxyPushAnyRefGrantIsTheOnlyWayPastTheTaskBranch(t *testing.T) {
	rig := newGitProxyRig(t)
	for _, ref := range []string{"refs/heads/main", "refs/tags/v1.0.0"} {
		status, text := rig.push(t, "sandbox-b", "other/gadgets", receivePackUpdate(ref))
		if status != http.StatusOK || strings.Contains(text, "ng "+ref+" ") || !rig.upstream.reached("POST /other/gadgets.git/git-receive-pack") {
			t.Fatalf("sandbox-b, holding git-push-any-ref, pushing %s: %d %q; want it forwarded", ref, status, text)
		}
	}
	if status, _ := rig.push(t, "sandbox-b", "acme/widgets", receivePackUpdate("refs/heads/main")); status == http.StatusOK ||
		rig.upstream.reached("POST /acme/widgets.git/git-receive-pack") {
		t.Fatal("git-push-any-ref let sandbox-b push a repository it may not write")
	}
}
