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

package ui_test

// The add/remove lifecycle of grain/task-4249 walked the whole way
// (grain/task-4334): a git connection and a repository added through the
// Settings routes, a task filed against them, dispatched by the
// orchestrator, cloned and pushed through a real gitproxy in front of a
// local bare repository, and its pull request opened on githubsim --
// then the connection and the repository removed, in each order, and
// what is left of that path checked: the proxy, the merge queue, and a
// re-add.

import (
	"context"
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
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// fullLifecycleRig is lifecycleRig with the rest of the path behind it:
// githubsim for the pull request, a bare repository it and the proxy
// share, the proxy itself, and the orchestrator's dispatcher.
type fullLifecycleRig struct {
	*lifecycleRig
	sim      *githubsim.Sim
	bare     string
	proxyURL string
	tokens   *gitproxy.SandboxTokenStore
	deps     orchestrator.Deps
	clock    time.Time
}

func newFullLifecycleRig(t *testing.T) *fullLifecycleRig {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ui.LogToTest(t)
	// No CI here at all (ci_mode none): an empty check list is the
	// verdict, not a list CI has yet to register into.
	t.Cleanup(orchestrator.SetCheckRegistrationWindow(0))
	// The proxy reads its credentials from <data>/secrets/github, the
	// directory the UI's credential set writes a connection's token into:
	// one deployment, two readers of the same files, as daemon.Run has it.
	dataDir := t.TempDir()
	credDir := filepath.Join(dataDir, "secrets", "github")
	if err := os.MkdirAll(credDir, 0o700); err != nil {
		t.Fatal(err)
	}
	r, _ := openLifecycleRigIn(t, t.TempDir(), credDir, t.TempDir())

	upstreamDir := t.TempDir()
	bare := seedBareRepo(t, filepath.Join(upstreamDir, "acme", "widgets.git"))
	sim := githubsim.New("acme", "widgets", bare, "main")
	sim.PullRequestBase = githubsim.UniquePullRequestBase()
	// acme/gadgets stays on the list throughout, so removing acme/widgets
	// leaves a restricted deployment rather than an unrestricted one
	// (TestRemovingTheLastRepositoryLeavesAnUnrestrictedDeployment).
	gadgets := githubsim.New("acme", "gadgets", "", "main")
	gadgets.PullRequestBase = githubsim.UniquePullRequestBase()
	gh := github.NewClient(simsByRepo{sim, gadgets}, nil)

	upstream := httptest.NewServer(gitBackend(upstreamDir))
	t.Cleanup(upstream.Close)
	u, _ := url.Parse(upstream.URL)
	proxy, err := gitproxy.BuildProxy(gitproxy.BuildConfig{
		DataDir: dataDir, Store: r.store, ForwardHost: model.DefaultGitHost, ForwardTLS: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Built for github.com, so a connection added for github.com is the
	// one its repositories resolve to, and only the last hop goes to the
	// local stand-in.
	proxy.Forwarder = &gitproxy.RealForwarder{Host: u.Host, UseTLS: false}
	// The connections the Settings pane writes name the credential each
	// repository resolves to (daemon.storeGitConnections).
	proxy.Credentials.SetConnections(func(owner, repo string) (string, bool) {
		ref := model.RepoFromStored(owner, repo)
		name, err := r.store.GitConnectionOn(r.ctx, ref.Host, ref.Owner, ref.Name)
		return name, err == nil && name != ""
	})
	proxyServer := httptest.NewServer(gitproxy.NewHandler(proxy))
	t.Cleanup(proxyServer.Close)

	tokens := gitproxy.NewSandboxTokenStore(filepath.Join(dataDir, "secrets", "sandbox-tokens.json"))
	f := &fullLifecycleRig{
		lifecycleRig: r, sim: sim, bare: bare, proxyURL: proxyServer.URL, tokens: tokens,
		clock: time.Now(),
	}
	f.deps = orchestrator.Deps{
		Store: r.store, Client: gh,
		Sandboxes:          orchestrator.NewHostSandboxes(t.TempDir()),
		MaxWorkers:         1,
		MintSandboxToken:   tokens.EnsureToken,
		RevokeSandboxToken: tokens.Revoke,
		Config:             orchestrator.Config{GitRemoteBase: proxyServer.URL},
	}
	f.agent(pushingAgent)
	return f
}

// agent makes every run the next cycle dispatches run a.
func (f *fullLifecycleRig) agent(a agent.Framework) {
	f.deps.Framework = func(context.Context, orchestrator.AgentChoice) (agent.Framework, error) { return a, nil }
}

// pushingAgent commits in the checkout the daemon cloned through the
// proxy and pushes its branch back through it -- what an agent finishing
// its task does.
var pushingAgent = agentRun(func(_ context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	return antigravity.NewForTest(antigravity.Steps(
		antigravity.ToolStep("run_command", map[string]any{"command": "cd work && " +
			"echo change >> NOTES.md && git add NOTES.md && " +
			"git commit -q -m 'agent commit' && " +
			"git push origin HEAD"}),
		antigravity.TextStep("pushed"),
	)).Run(context.Background(), cfg)
})

type agentRun func(context.Context, agent.RunConfig) (*agent.Result, error)

func (a agentRun) Run(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	return a(ctx, cfg)
}

// cycle runs one orchestrator cycle a minute after the last.
func (f *fullLifecycleRig) cycle() {
	f.t.Helper()
	f.clock = f.clock.Add(time.Minute)
	if err := orchestrator.RunCycle(f.ctx, f.deps, f.clock); err != nil {
		f.t.Fatalf("RunCycle: %v", err)
	}
}

// seedBareRepo makes a bare repository at path with one commit on main,
// pushable over git http-backend.
func seedBareRepo(t *testing.T, path string) string {
	t.Helper()
	gitRun(t, "", "init", "-q", "--bare", "-b", "main", path)
	gitRun(t, path, "config", "http.receivepack", "true")
	seed := t.TempDir()
	gitRun(t, seed, "clone", "-q", path, ".")
	gitRun(t, seed, "-c", "user.email=seed@example.com", "-c", "user.name=seed",
		"commit", "-q", "--allow-empty", "-m", "seed")
	gitRun(t, seed, "push", "-q", "origin", "HEAD:main")
	return path
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// gitBackend serves every bare repository under root over smart HTTP,
// pushes included: the stand-in for github.com's git endpoints.
func gitBackend(root string) http.Handler {
	git, _ := exec.LookPath("git")
	return &cgi.Handler{
		Path: git, Args: []string{"http-backend"},
		Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"},
	}
}

// proxyAsks asks the proxy what git would for a clone (upload-pack) or a
// push (receive-pack) of acme/widgets with token, and returns the status
// and body it answered.
func (f *fullLifecycleRig) proxyAsks(token, service string) (int, string) {
	f.t.Helper()
	req, err := http.NewRequest(http.MethodGet, f.proxyURL+"/acme/widgets.git/info/refs?service="+service, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	req.SetBasicAuth("x-access-token", token)
	req.Header.Set("User-Agent", "git/2.39.5")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// simsByRepo routes each REST request to the Sim for the repository in
// its path, falling back to the first.
type simsByRepo []*githubsim.Sim

func (s simsByRepo) Request(method, path string, headers map[string]string, body []byte) (github.ApiResponse, error) {
	if rest, ok := strings.CutPrefix(path, "/repos/"); ok {
		parts := strings.SplitN(strings.SplitN(rest, "?", 2)[0], "/", 3)
		for _, sim := range s {
			if len(parts) >= 2 && strings.EqualFold(sim.Owner, parts[0]) && strings.EqualFold(sim.Repo, parts[1]) {
				return sim.Request(method, path, headers, body)
			}
		}
	}
	return s[0].Request(method, path, headers, body)
}

// sandboxGit runs git as a run's sandbox does: with the sandbox root as
// HOME, where dispatch wrote its proxy credential.
func sandboxGit(root, dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "HOME="+root, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// sandboxToken is the proxy token dispatch gave the sandbox at root, read
// back from its git credential store.
func sandboxToken(root string) string {
	raw, _ := os.ReadFile(filepath.Join(root, ".git-credentials"))
	// scheme://sandbox:<token>@host -- the host percent-encoded, which
	// url.Parse refuses, so the token is cut out by hand.
	_, rest, _ := strings.Cut(strings.TrimSpace(string(raw)), "://sandbox:")
	token, _, _ := strings.Cut(rest, "@")
	return token
}

// removalSeen is what a run that was live across the removal saw of the
// proxy: before, and after.
type removalSeen struct {
	token                   string
	before                  [2]int // clone, push advertisement
	clone, push             [2]any // status, body
	gitClone, gitPush       string
	gitCloneErr, gitPushErr error
}

const removedFromList = "no longer on this deployment's target repo list"

func runFullLifecycle(t *testing.T, connectionFirst bool) {
	f := newFullLifecycleRig(t)
	f.must(http.MethodPost, "/api/repos", `{"repo":"acme/gadgets"}`)
	cred := f.addConnection("github.com", "acme", "ghp_full")
	f.must(http.MethodPost, "/api/repos", `{"repo":"acme/widgets"}`)
	f.must(http.MethodPut, "/api/deployment/repos/acme/widgets/git-connection", `{"credential":"`+cred+`"}`)
	f.must(http.MethodPut, "/api/repos/acme/widgets/ci-mode", `{"ciMode":"none"}`)
	f.must(http.MethodPut, "/api/repos/acme/widgets/setup-command", `{"setupCommand":"true"}`)

	// The task: dispatched, cloned through the proxy, pushed back
	// through it, and its pull request opened.
	queued, err := f.client.CreateTask(f.ctx, ui.CreateTaskRequest{Title: "Fix the widget",
		Repo: "acme/widgets", Approved: true, AutoMerge: true})
	if err != nil {
		t.Fatal(err)
	}
	f.cycle()
	if len(f.sim.PullRequests) != 1 || f.sim.PullRequests[0].Head != model.BranchName(queued.ID) {
		t.Fatalf("pull requests after the first run = %+v, want one for %s", f.sim.PullRequests, model.BranchName(queued.ID))
	}
	if out := gitRun(t, f.bare, "log", "--format=%s", "-1", model.BranchName(queued.ID)); !strings.Contains(out, "agent commit") {
		t.Fatalf("the run's push did not land upstream through the proxy: %q", out)
	}

	// A second task's run is live when the repository and its connection
	// are removed, in the order under test, and goes on asking the proxy
	// for the repository after.
	var seen removalSeen
	f.agent(agentRun(func(_ context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		root := cfg.SandboxRoot
		seen.token = sandboxToken(root)
		seen.before[0], _ = f.proxyAsks(seen.token, "git-upload-pack")
		seen.before[1], _ = f.proxyAsks(seen.token, "git-receive-pack")
		removals := []string{"/api/repos/acme/widgets", "/api/deployment/git-connections/" + cred}
		if connectionFirst {
			removals[0], removals[1] = removals[1], removals[0]
		}
		for _, path := range removals {
			if code, out := f.serve(http.MethodDelete, path, ""); code != http.StatusOK {
				return nil, fmt.Errorf("DELETE %s: %d %s", path, code, out)
			}
		}
		code, body := f.proxyAsks(seen.token, "git-upload-pack")
		seen.clone = [2]any{code, body}
		code, body = f.proxyAsks(seen.token, "git-receive-pack")
		seen.push = [2]any{code, body}
		seen.gitClone, seen.gitCloneErr = sandboxGit(root, root, "clone", "-q", f.proxyURL+"/acme/widgets.git", "again")
		work := filepath.Join(root, "work")
		if _, err := sandboxGit(root, work, "commit", "-q", "--allow-empty", "-m", "after removal"); err != nil {
			return nil, err
		}
		seen.gitPush, seen.gitPushErr = sandboxGit(root, work, "push", "origin", "HEAD")
		return &agent.Result{FinalText: "tried"}, nil
	}))
	live, err := f.client.CreateTask(f.ctx, ui.CreateTaskRequest{Title: "Polish the widget",
		Repo: "acme/widgets", Approved: true})
	if err != nil {
		t.Fatal(err)
	}
	f.cycle()
	if seen.token == "" {
		t.Fatalf("task %s's run never started", live.ID)
	}
	if seen.before != [2]int{http.StatusOK, http.StatusOK} {
		t.Fatalf("before the removal the proxy answered clone/push with %v, want 200s", seen.before)
	}
	for what, got := range map[string][2]any{"clone": seen.clone, "push": seen.push} {
		if got[0] != http.StatusForbidden || !strings.Contains(got[1].(string), removedFromList) ||
			!strings.Contains(got[1].(string), "acme/widgets") {
			t.Errorf("after the removal the proxy answered a %s with %v %q, want a 403 saying the repository was removed", what, got[0], got[1])
		}
	}
	if seen.gitCloneErr == nil || !strings.Contains(seen.gitClone, removedFromList) {
		t.Errorf("git clone after the removal = %v\n%s\nwant it refused, saying why", seen.gitCloneErr, seen.gitClone)
	}
	if seen.gitPushErr == nil || !strings.Contains(seen.gitPush, removedFromList) {
		t.Errorf("git push after the removal = %v\n%s\nwant it refused, saying why", seen.gitPushErr, seen.gitPush)
	}
	if out := gitRun(t, f.bare, "for-each-ref", "refs/heads/"+model.BranchName(live.ID)); out != "" {
		t.Fatalf("a push made after the removal landed upstream: %s", out)
	}

	// The first task's pull request is now clean and its CI (none) green:
	// everything the merge queue asks for, but in a repository the
	// deployment no longer works in.
	f.markMergeable()
	for i := 0; i < 3; i++ {
		f.cycle()
	}
	assertParkedUnmerged := func(when string) {
		t.Helper()
		if pr := f.pullRequest(); pr.Merged || pr.State != "open" {
			t.Fatalf("%s: the pull request into the removed repository is %s (merged %v), want it left open", when, pr.State, pr.Merged)
		}
		task, err := f.store.GetTask(f.ctx, queued.ID)
		if err != nil {
			t.Fatal(err)
		}
		if task.AutoMerge {
			t.Fatalf("%s: task %s still has auto-merge on", when, queued.ID)
		}
		detail, err := f.client.GetTask(f.ctx, queued.ID)
		if err != nil {
			t.Fatal(err)
		}
		notes := 0
		for _, c := range detail.Comments {
			if strings.Contains(c.Body, removedFromList) && strings.Contains(c.Body, "will not merge") {
				notes++
			}
		}
		if notes != 1 {
			t.Fatalf("%s: task %s has %d notes saying why it was not merged, want one: %+v", when, queued.ID, notes, detail.Comments)
		}
	}
	assertParkedUnmerged("after the removal")

	// Re-adding the repository brings back none of it: not the
	// connection or its credential, not the pin, not the repository's
	// settings, not the run's proxy token, and not the merge.
	f.must(http.MethodPost, "/api/repos", `{"repo":"acme/widgets"}`)
	for i := 0; i < 2; i++ {
		f.cycle()
	}
	assertParkedUnmerged("after re-adding the repository")
	for _, v := range f.connections() {
		if v.Credential == cred {
			t.Fatalf("the removed connection came back with its repository: %+v", v)
		}
	}
	if f.credentialExists(cred) {
		t.Fatalf("the removed connection's credential %s came back", cred)
	}
	if pins := f.pins(); len(pins) != 0 {
		t.Fatalf("pins after re-adding = %v, want none", pins)
	}
	if rc, err := f.store.GetRepoConfig(f.ctx, model.RepoRef{Owner: "acme", Name: "widgets"}); err != nil ||
		(rc != nil && (rc.SetupCommand != "" || rc.CIMode != "")) {
		t.Fatalf("re-added repository's config = %+v, %v; want its old settings gone", rc, err)
	}
	if code, body := f.proxyAsks(seen.token, "git-upload-pack"); code == http.StatusOK {
		t.Fatalf("the finished run's proxy token reaches the re-added repository: %d %s", code, body)
	}
}

func (f *fullLifecycleRig) markMergeable() {
	clean := true
	for i := range f.sim.PullRequests {
		f.sim.PullRequests[i].Mergeable = &clean
	}
}

func (f *fullLifecycleRig) pullRequest() githubsim.PullRequest {
	f.t.Helper()
	if len(f.sim.PullRequests) == 0 {
		f.t.Fatal("no pull request")
	}
	return f.sim.PullRequests[0]
}

func TestFullLifecycleRemovingTheRepoThenTheConnection(t *testing.T) {
	runFullLifecycle(t, false)
}

func TestFullLifecycleRemovingTheConnectionThenTheRepo(t *testing.T) {
	runFullLifecycle(t, true)
}
