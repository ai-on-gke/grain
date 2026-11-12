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

package e2e

// git_hosts_daemon_test.go is git_hosts_lifecycle_test.go's other half
// (docs/git-hosts.md section 8): the same acme/widgets on hosts other
// than the default one, but through a real daemon.Run rather than the
// harness, because what happens to a pushed branch on such a host goes
// through the daemon's own per-host forge router (pkg/daemon/forgeroute.go).
// A stub agent stands in for the claude CLI and pushes; the rest -- the
// finish path opening the pull request, its status and checks, the merge
// queue landing it -- is the daemon's.
//
// The hosts:
//
//   - a second GitHub-kind host, a githubsim behind a loopback base URL
//     serving git at its root and REST under /api/v3, as a GitHub
//     Enterprise Server does;
//   - a plain git server, with no forge API: its task ends with the
//     branch pushed and no pull request;
//   - the command-backed forge (gitproxytest's forgecli on
//     depot.code.forge.test), whose REST API is a second githubsim
//     reached only through `forgecli api`.
//
// The default host is a git server that records every request it is
// sent: nothing about another host's repository may ever reach it.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/gitproxy/gitproxytest"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// forgeUpstream is a hostUpstream with a forge API beside its git: a
// githubsim over the same bare repository, reached under /api/v3 (and
// GraphQL at /api/graphql), as a GitHub Enterprise Server lays them out.
type forgeUpstream struct {
	*hostUpstream

	// mu serialises the sim: the daemon's requests and the test's own
	// reads and pokes of its fields.
	mu  sync.Mutex
	sim *githubsim.Sim
	// api is every REST or GraphQL request's credential (or, for the
	// command-backed forge, the host its forgecli was run for).
	api []string
}

func newForgeUpstream(t *testing.T, kind, host, secret string) *forgeUpstream {
	return &forgeUpstream{hostUpstream: newHostUpstream(t, kind, host, secret)}
}

// start seeds the repository and puts the sim over it.
func (f *forgeUpstream) start() {
	f.newRepo()
	f.sim = githubsim.New(gitHostsOwner, f.repo, f.bare(), "main")
	f.sim.PullRequestBase = githubsim.UniquePullRequestBase()
}

func (f *forgeUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case path == "/api/graphql" || path == "/api/v3/graphql":
		r.URL.Path, r.URL.RawPath = "/graphql", ""
	case strings.HasPrefix(path, "/api/v3/"):
		r.URL.Path, r.URL.RawPath = strings.TrimPrefix(path, "/api/v3"), ""
	default:
		f.hostUpstream.ServeHTTP(w, r)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	who := r.Header.Get("X-Fake-ForgeCLI-Host")
	if who == "" {
		auth := r.Header.Get("Authorization")
		for _, p := range []string{"token ", "Bearer ", "bearer "} {
			auth = strings.TrimPrefix(auth, p)
		}
		who = auth
	}
	f.api = append(f.api, who)
	f.sim.Handler().ServeHTTP(w, r)
}

// with runs fn on the sim, serialised against the daemon's requests.
func (f *forgeUpstream) with(fn func(s *githubsim.Sim)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f.sim)
}

// apiSeen is every credential the forge API was sent since the last call.
func (f *forgeUpstream) apiSeen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	seen := f.api
	f.api = nil
	return seen
}

// openPullRequest is the pull request open on the sim for branch.
func (f *forgeUpstream) pullRequestFor(branch string) (githubsim.PullRequest, bool) {
	var pr githubsim.PullRequest
	found := false
	f.with(func(s *githubsim.Sim) {
		for _, p := range s.PullRequests {
			if p.Head == branch {
				pr, found = p, true
			}
		}
	})
	return pr, found
}

// headSHA is branch's tip on this host.
func (u *hostUpstream) headSHA(branch string) string {
	out, err := exec.Command("git", "--git-dir", u.bare(), "rev-parse", "refs/heads/"+branch).Output()
	if err != nil {
		u.t.Fatalf("reading %s on %s: %v", branch, u.label(), err)
	}
	return strings.TrimSpace(string(out))
}

// recordingUpstream is the default host: git, and a record of every
// request, so a test can say nothing was sent to it.
type recordingUpstream struct {
	backend http.Handler
	mu      sync.Mutex
	paths   []string
}

func (u *recordingUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u.mu.Lock()
	u.paths = append(u.paths, r.Method+" "+r.URL.RequestURI())
	u.mu.Unlock()
	if u.backend == nil {
		http.NotFound(w, r)
		return
	}
	u.backend.ServeHTTP(w, r)
}

func (u *recordingUpstream) seen() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	seen := u.paths
	u.paths = nil
	return seen
}

// pushingAgent is the claude CLI's stand-in for a run that commits once
// to the checkout the daemon cloned for it and pushes its branch, then
// ends: the newest sandbox's credential is the run's own.
func pushingAgent(t *testing.T, sandboxDir string) stubAgent {
	t.Helper()
	agent := newStubAgent(t, 0)
	script := `#!/bin/bash
echo start >> ` + filepath.Join(agent.dir, "starts") + `
creds=$(ls -t $(find ` + sandboxDir + ` -maxdepth 4 -name .git-credentials) 2>/dev/null | head -1)
[ -n "$creds" ] || { echo "no sandbox credential under ` + sandboxDir + `" >&2; exit 1; }
export HOME=$(dirname "$creds")
unset GRAIN_SANDBOX_PROXY GRAIN_SANDBOX_TOKEN GRAIN_SERVER GRAIN_LOCAL_API_TOKEN_FILE
cd "$HOME/work" || { echo "no checkout at $HOME/work" >&2; exit 1; }
echo '{"type":"assistant","message":{"content":[{"type":"tool_use","id":"call-1","name":"run_command","input":{"command":"git push"}}]}}'
branch=$(git rev-parse --abbrev-ref HEAD)
echo "$branch" > change.txt
git add change.txt
git -c user.email=a@b -c user.name=a commit -q -m "agent change on $branch" -m "What the stub agent changed." >> ` + filepath.Join(agent.dir, "log") + ` 2>&1
git push -q origin HEAD >> ` + filepath.Join(agent.dir, "log") + ` 2>&1
echo "$branch exit=$?" >> ` + filepath.Join(agent.dir, "pushes") + `
echo '{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"call-1","content":"pushed"}]}}'
echo '{"type":"result","result":"done"}'
`
	mustWriteFile(t, filepath.Join(agent.dir, "claude"), script)
	return agent
}

// daemonHostsRig is one daemon with acme/widgets on three hosts besides
// the default one.
type daemonHostsRig struct {
	def     *recordingUpstream
	ghe     *forgeUpstream
	plain   *hostUpstream
	depot   *forgeUpstream
	agent   stubAgent
	d       *liveDaemon
	c       *ui.HTTPClient
	cfg     daemon.Config
	dataDir string
	store   *model.Store
}

func newDaemonHostsRig(t *testing.T) *daemonHostsRig {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.Symlink(self, filepath.Join(bin, gitproxytest.Command)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	r := &daemonHostsRig{
		def:   &recordingUpstream{},
		ghe:   newForgeUpstream(t, "the second GitHub host", "", "ghe-secret"),
		plain: newHostUpstream(t, "the plain git server", "", "plain-secret"),
		depot: newForgeUpstream(t, "the command-backed forge", depotHost, fakeForgeCLITokenFor(depotHost)),
	}
	defDir := t.TempDir()
	r.def.backend = gitHTTPBackend(defDir)
	defURL := newTestServer(t, r.def)
	gheURL := newTestServer(t, r.ghe)
	plainURL := newTestServer(t, r.plain)
	r.ghe.host, r.plain.host = mustHost(t, gheURL), mustHost(t, plainURL)
	// Every host holds the same owner/name (gitHostsRepo), so a forge
	// call that named the repository without its host would be resolved
	// by storeRepoHosts to the default host -- which records it, and the
	// tests below fail on it.
	t.Setenv(fakeForgeCLIUpstreamEnv, newTestServer(t, r.depot))
	r.ghe.start()
	r.depot.start()
	r.plain.newRepo()

	r.dataDir = t.TempDir()
	sandboxDir := t.TempDir()
	r.agent = pushingAgent(t, sandboxDir)
	r.cfg = daemon.Config{
		DataDir: r.dataDir, SandboxDir: sandboxDir,
		UiAddr:     "127.0.0.1:" + strconv.Itoa(freeLoopbackPort(t)),
		ClaudePath: filepath.Join(r.agent.dir, "claude"),
		MaxWorkers: 1, PollInterval: time.Second,
		GithubHost: mustHost(t, defURL), GithubInsecureHTTP: true,
		HostSandboxes: true,
	}

	creds, err := gitproxy.LoadCredentialSet(filepath.Join(r.dataDir, "secrets", "github"))
	if err != nil {
		t.Fatal(err)
	}
	for name, token := range map[string]string{"team-default": "default-secret", "ghe": r.ghe.secret, "plain": r.plain.secret} {
		if err := creds.SetToken(name, token); err != nil {
			t.Fatalf("writing %s's token: %v", name, err)
		}
	}
	for name, h := range map[string]gitproxy.CredentialHost{
		"ghe":   {Host: r.ghe.host, Kind: model.GitHostGitHub, BaseURL: gheURL},
		"plain": {Host: r.plain.host, Kind: model.GitHostGit, BaseURL: plainURL},
	} {
		if err := creds.SetHost(name, h); err != nil {
			t.Fatalf("recording %s's host: %v", name, err)
		}
	}
	if err := creds.SetAmbientOn("depot", gitproxy.CredentialHost{Host: depotHost, Kind: model.GitHostGitHub},
		gitproxy.AmbientSourceAuto); err != nil {
		t.Fatalf("recording the forge's ambient credential: %v", err)
	}
	seedConnections(t, r.cfg, []model.TeamGitConnection{
		{Credential: "ghe", Host: r.ghe.host},
		{Credential: "plain", Host: r.plain.host},
		{Credential: "depot", Host: depotHost},
	}, []model.AgentBackend{{ID: "claude", Framework: model.AgentFrameworkClaude, Models: []string{"claude-sonnet-5"}}},
		map[string]string{"claude": "sk-ant-oat01-stub"})
	store, db, err := daemon.OpenStore("sqlite", "", r.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateConfig(context.Background(), func(c *model.Config) error {
		c.GitCredential = "team-default"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	db.Close()

	r.d = startDaemon(t, r.cfg)
	r.c = ui.NewHTTPClient(r.d.base)
	for _, ref := range []model.RepoRef{r.ghe.ref(), r.plain.ref(), r.depot.ref()} {
		accessDo(t, http.MethodPost, r.d.base+"/api/repos", `{"repo":"`+ref.String()+`"}`)
	}
	return r
}

// file files a task on ref through POST /api/tasks.
func (r *daemonHostsRig) file(t *testing.T, ref model.RepoRef, title string) string {
	t.Helper()
	created, err := r.c.CreateTask(context.Background(), ui.CreateTaskRequest{
		Title: title, Description: "work on " + ref.String(), Approved: true, Repo: ref.String(),
	})
	if err != nil {
		t.Fatalf("POST /api/tasks for %s: %v", ref, err)
	}
	return created.ID
}

// branch is task id's branch.
func (r *daemonHostsRig) branch(t *testing.T, id string) string {
	t.Helper()
	if r.store == nil {
		r.store = readStore(t, r.dataDir)
	}
	task, err := r.store.GetTask(context.Background(), id)
	if err != nil || task == nil {
		t.Fatalf("reading %s: %v", id, err)
	}
	return task.BranchName()
}

// taskJSON is GET /api/tasks/<id>, as the UI reads it.
func (r *daemonHostsRig) taskJSON(t *testing.T, id string) map[string]any {
	t.Helper()
	resp, err := accessGet(r.d.base + "/api/tasks/" + id)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("GET /api/tasks/%s: %v: %s", id, err, raw)
	}
	return v
}

// comments is the bodies of task id's conversation, as the UI shows it.
func (r *daemonHostsRig) comments(t *testing.T, id string) []string {
	t.Helper()
	list, _ := r.taskJSON(t, id)["comments"].([]any)
	var bodies []string
	for _, c := range list {
		if m, ok := c.(map[string]any); ok {
			body, _ := m["body"].(string)
			bodies = append(bodies, body)
		}
	}
	return bodies
}

// ciGate is the state of task id's CI merge gate, as the UI shows it.
func (r *daemonHostsRig) ciGate(t *testing.T, id string) string {
	t.Helper()
	gates, _ := r.taskJSON(t, id)["mergeGates"].([]any)
	for _, g := range gates {
		if m, ok := g.(map[string]any); ok && m["id"] == "ci" {
			state, _ := m["state"].(string)
			return state
		}
	}
	return ""
}

func anyContains(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// A run on each host that pushes its branch is finished by the daemon
// through that host's own forge: the pull request opened there, with that
// host's credential, its checks read back from there; on the plain git
// server, the branch left pushed with the documented note and no pull
// request. Nothing about any of them reaches the default host.
func TestADaemonFinishesARunOnEachHostThroughItsOwnForge(t *testing.T) {
	r := newDaemonHostsRig(t)
	r.def.seen()
	r.ghe.apiSeen()
	r.depot.apiSeen()

	prs := map[*forgeUpstream]string{}
	for _, h := range []*forgeUpstream{r.ghe, r.depot} {
		id := r.file(t, h.ref(), "change on "+h.label())
		branch := r.branch(t, id)
		var pr githubsim.PullRequest
		waitUntil(t, "a pull request on "+h.label(), 2*time.Minute, func() bool {
			var ok bool
			pr, ok = h.pullRequestFor(branch)
			return ok
		})
		if pr.Base != "main" {
			t.Errorf("the pull request on %s is into %q, want main", h.label(), pr.Base)
		}
		// The task's pull request keeps its host, so the UI links it there.
		want := fmt.Sprintf("%s#%d", h.ref(), pr.Number)
		waitUntil(t, "task "+id+" to record its pull request", 30*time.Second, func() bool {
			got, _ := r.taskJSON(t, id)["pullRequest"].(string)
			return got == want
		})
		prs[h] = id
	}
	// Each forge's API was only ever sent its own credential: the second
	// GitHub host its token, and the command-backed forge only ever asked
	// through forgecli for its own host.
	for h, want := range map[*forgeUpstream]string{r.ghe: r.ghe.secret, r.depot: depotHost} {
		seen := h.apiSeen()
		if len(seen) == 0 {
			t.Errorf("%s's forge API was never asked anything", h.label())
		}
		for _, got := range seen {
			if got != want {
				t.Errorf("%s's forge API was sent %q, want only %q", h.label(), got, want)
				break
			}
		}
	}

	// The pull request's checks are read from the second GitHub host, at
	// the head the agent pushed.
	gheTask := prs[r.ghe]
	head := r.ghe.headSHA(r.branch(t, gheTask))
	waitUntil(t, "the checks on "+r.ghe.label()+"'s pull request to be read there", time.Minute, func() bool {
		found := false
		r.ghe.with(func(s *githubsim.Sim) {
			for _, c := range s.Calls {
				if c.Method == http.MethodGet && strings.HasPrefix(c.Path, "/repos/acme/"+r.ghe.repo+"/commits/"+head+"/check-runs") {
					found = true
				}
			}
		})
		return found
	})

	// The plain git server: pushed, no pull request, and the note saying so.
	id := r.file(t, r.plain.ref(), "change on the plain git server")
	plainBranch := r.branch(t, id)
	note := "no pull request opened because " + r.plain.host + " is a plain git host"
	waitUntil(t, "the plain git server's task to say no pull request was opened", time.Minute, func() bool {
		return anyContains(r.comments(t, id), note)
	})
	if got := r.plain.branchTip(plainBranch); !strings.HasPrefix(got, "agent change on") {
		t.Errorf("%s on %s = %q, want the agent's commit", plainBranch, r.plain.label(), got)
	}
	if got, _ := r.taskJSON(t, id)["pullRequest"].(string); got != "" {
		t.Errorf("the task on the plain git server has pull request %q", got)
	}

	if seen := r.def.seen(); len(seen) != 0 {
		t.Errorf("the default host was sent %q, about repositories on other hosts", seen)
	}
}

// A host's connection taken away before the daemon clones a task on it:
// the task is parked rather than retried, and the reason a person reads
// in its conversation names the host. Nothing is sent to the default host.
func TestADaemonParksATaskWhoseHostLostItsConnectionNamingTheHost(t *testing.T) {
	r := newDaemonHostsRig(t)
	// Filed while the connection is there, and approved once it is gone:
	// filing itself is refused on a host with no connection.
	created, err := r.c.CreateTask(context.Background(), ui.CreateTaskRequest{
		Title: "lose the connection", Description: "work on " + r.ghe.ref().String(), Repo: r.ghe.ref().String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	id := created.ID
	store := readStore(t, r.dataDir)
	if err := store.UpdateConfig(context.Background(), func(c *model.Config) error {
		c.GitConnections = slices.DeleteFunc(c.GitConnections, func(conn model.TeamGitConnection) bool {
			return conn.Host == r.ghe.host
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r.def.seen()
	if err := r.c.Approve(context.Background(), id); err != nil {
		t.Fatalf("approving %s: %v", id, err)
	}
	// Parking is two writes: the comment, then the pending question on it
	// that makes the task awaiting_reply (stopOnAccessRefused). Waiting on
	// the comment alone reads the state between them on a loaded runner.
	waitUntil(t, "the task to be parked on the refusal", 2*time.Minute, func() bool {
		return anyContains(r.comments(t, id), r.ghe.host) && r.taskJSON(t, id)["state"] == "awaiting_reply"
	})
	if !anyContains(r.comments(t, id), "cloning it was refused") {
		t.Errorf("the task's conversation says %q, want the clone's refusal", r.comments(t, id))
	}
	if state := r.taskJSON(t, id)["state"]; state != "awaiting_reply" {
		t.Errorf("the task is %v, want it parked awaiting a reply rather than retried", state)
	}
	if starts := r.agent.starts(); starts != 0 {
		t.Errorf("the agent was started %d times for a task whose clone was refused", starts)
	}
	if seen := r.def.seen(); len(seen) != 0 {
		t.Errorf("the default host was sent %q", seen)
	}
}
