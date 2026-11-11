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

// git_hosts_lifecycle_test.go is docs/git-hosts.md section 8: one team,
// the default host and three others side by side, each with the same
// acme/widgets on it --
//
//   - a second GitHub-kind host (GitHub Enterprise style) on its own
//     token, reached at a base URL of its own;
//   - a plain git server, kind "git", with no forge API at all;
//   - a command-backed forge, gitproxytest's made-up forgecli source on
//     depot.code.forge.test, which behaves the way a forge reached only
//     through its own gh-style tool does: every request, git included, is
//     one run of `forgecli api`, and a lapsed login answers with the
//     login page rather than a refusal.
//
// Each repository is added through the UI client, filed against through
// POST /api/tasks, dispatched by a real dispatch.Cycle, cloned and pushed
// to by a scripted agent through the real git proxy, and carried through
// a state-repo export and import; at every step its host must survive,
// and every upstream must only ever be sent its own host's credential.
// Opening the pull request and the merge queue on a command-backed host
// are pkg/daemon/forgeroute_test.go's, one layer down: the forge router
// they go through is the daemon's own.

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
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/gitproxy/gitproxytest"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/staterepo"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// The made-up command-backed source the forge host is reached through.
func init() { gitproxytest.Install() }

const (
	// depotHost is the command-backed forge. The second GitHub-kind host
	// and the plain git server are each their own loopback host:port, as
	// a host record's base URL must be on its host.
	depotHost = gitproxytest.Host
	// fakeForgeCLIUpstreamEnv names the server the fake forgecli sends
	// every request to, standing in for the forge's front end.
	fakeForgeCLIUpstreamEnv = "GRAIN_E2E_FAKE_FORGECLI_UPSTREAM"
	// gitHostsOwner and gitHostsRepo name the repository every host has.
	gitHostsOwner = "acme"
	gitHostsRepo  = "widgets"
)

// fakeForgeCLITokenFor is the login the fake forgecli hands out for host.
func fakeForgeCLITokenFor(host string) string { return "forgecli-token-" + host }

// fakeForgeCLI is `forgecli` in this package's test binary (TestMain):
// `auth token` prints a login, and `api` sends the one request it was
// given to fakeForgeCLIUpstreamEnv and prints the answer --include style,
// the way gh does.
func fakeForgeCLI(args []string) int {
	if len(args) >= 2 && args[0] == "auth" && args[1] == "token" {
		fmt.Println(fakeForgeCLITokenFor(os.Getenv("GH_HOST")))
		return 0
	}
	if len(args) == 0 || args[0] != "api" {
		fmt.Fprintf(os.Stderr, "fake forgecli: unexpected %q\n", args)
		return 2
	}
	method, endpoint, hasBody := "GET", "", false
	headers := http.Header{}
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--include":
		case "--method":
			i++
			method = args[i]
		case "-H":
			i++
			k, v, _ := strings.Cut(args[i], ":")
			headers.Add(strings.TrimSpace(k), strings.TrimSpace(v))
		case "--input":
			i++
			hasBody = true
		default:
			endpoint = args[i]
		}
	}
	upstream := os.Getenv(fakeForgeCLIUpstreamEnv)
	target := upstream + "/api/v3/" + endpoint
	if strings.HasPrefix(endpoint, "https://") || strings.HasPrefix(endpoint, "http://") {
		u, err := url.Parse(endpoint)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		target = upstream + u.RequestURI()
	}
	var body io.Reader
	if hasBody {
		data, _ := io.ReadAll(os.Stdin)
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, target, body)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	req.Header = headers
	req.Header.Set("X-Fake-ForgeCLI-Host", os.Getenv("GH_HOST"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	out := &bytes.Buffer{}
	fmt.Fprintf(out, "HTTP/1.1 %s\r\n", resp.Status)
	resp.Header.Del("Content-Length")
	resp.Header.Write(out)
	out.WriteString("\r\n")
	os.Stdout.Write(out.Bytes())
	io.Copy(os.Stdout, resp.Body)
	return 0
}

// hostFailure is how a host's upstream answers instead of serving git.
type hostFailure string

const (
	hostServes hostFailure = ""
	// hostLoginPage is a lapsed forge login: the front end's login page,
	// 200 and all, which is what a tool that follows redirects prints.
	hostLoginPage hostFailure = "login"
	// hostUnauthorized is a credential the host no longer takes.
	hostUnauthorized hostFailure = "unauthorized"
)

// hostUpstream is one git host: a directory of bare repositories served
// over smart HTTP, recording the credential each request arrived with.
type hostUpstream struct {
	t *testing.T
	// host is the host as a RepoRef spells it: "" for the default host.
	host string
	// kind names the host in failures.
	kind string
	// secret is the one credential this host may ever be sent.
	secret  string
	dir     string
	backend http.Handler

	mu      sync.Mutex
	seen    []string
	failure hostFailure
}

func newHostUpstream(t *testing.T, kind, host, secret string) *hostUpstream {
	dir := t.TempDir()
	return &hostUpstream{t: t, kind: kind, host: host, secret: secret, dir: dir, backend: gitHTTPBackend(dir)}
}

func (u *hostUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u.mu.Lock()
	u.seen = append(u.seen, basicPassword(r.Header.Get("Authorization")))
	failure := u.failure
	u.mu.Unlock()
	switch failure {
	case hostLoginPage:
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<html><body><form action="https://%s/login">Sign in</form></body></html>`, gitproxytest.LoginHost)
		return
	case hostUnauthorized:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"message":"Bad credentials"}`)
		return
	}
	u.backend.ServeHTTP(w, r)
}

// basicPassword is the secret in a Basic Authorization header -- its
// password, or its username when the password is empty, which is how the
// proxy sends a token -- or what was sent when it is not one.
func basicPassword(header string) string {
	encoded, ok := strings.CutPrefix(header, "Basic ")
	if !ok {
		return header
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return header
	}
	username, password, _ := strings.Cut(string(raw), ":")
	if password == "" {
		return username
	}
	return password
}

func (u *hostUpstream) fail(f hostFailure) {
	u.mu.Lock()
	u.failure = f
	u.mu.Unlock()
}

// credentialsSeen is every credential this host has been sent since the
// last call, and forgets them.
func (u *hostUpstream) credentialsSeen() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	seen := u.seen
	u.seen = nil
	return seen
}

func (u *hostUpstream) label() string {
	if u.host == "" {
		return u.kind
	}
	return u.kind + " (" + u.host + ")"
}

// ref is acme/widgets on this host.
func (u *hostUpstream) ref() model.RepoRef {
	return model.RepoRef{Host: u.host, Owner: gitHostsOwner, Name: gitHostsRepo}
}

// newRepo seeds acme/widgets on this host with one commit on main, and a
// README naming the host, so that a clone shows which host it reached.
func (u *hostUpstream) newRepo() {
	t := u.t
	t.Helper()
	bare := initBareRepo(t, filepath.Join(u.dir, gitHostsOwner, gitHostsRepo+".git"), "main")
	run(t, u.dir, "git", "-C", bare, "config", "http.receivepack", "true")
	seed := filepath.Join(t.TempDir(), "seed")
	run(t, u.dir, "git", "clone", "-q", bare, seed)
	mustWriteFile(t, filepath.Join(seed, "README.md"), "acme/widgets on "+u.label()+"\n")
	run(t, seed, "git", "add", "README.md")
	run(t, seed, "git", "-c", "user.email=s@e", "-c", "user.name=s", "commit", "-q", "-m", "initial commit")
	run(t, seed, "git", "push", "-q", "origin", "HEAD:main")
}

// branchTip is the subject of branch's tip on this host, "" when the
// branch is not there.
func (u *hostUpstream) branchTip(branch string) string {
	bare := filepath.Join(u.dir, gitHostsOwner, gitHostsRepo+".git")
	out, err := exec.Command("git", "--git-dir", bare, "log", "-1", "--format=%s", "refs/heads/"+branch).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// hostsRig is one deployment with a repository on four hosts.
type hostsRig struct {
	w      *world
	hosts  []*hostUpstream // default, GitHub Enterprise, plain git, forge
	client *ui.Client
	api    *ui.HTTPClient
	clock  time.Time
}

func newHostsRig(t *testing.T) *hostsRig {
	t.Helper()
	// The forge is reached only through forgecli, which is this binary.
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.Symlink(self, filepath.Join(bin, gitproxytest.Command)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	def := newHostUpstream(t, "the default host", "", "default-secret")
	ghe := newHostUpstream(t, "the second GitHub host", "", "ghe-secret")
	plain := newHostUpstream(t, "the plain git server", "", "plain-secret")
	depot := newHostUpstream(t, "the command-backed forge", depotHost, fakeForgeCLITokenFor(depotHost))
	gheURL := newTestServer(t, ghe)
	plainURL := newTestServer(t, plain)
	ghe.host, plain.host = mustHost(t, gheURL), mustHost(t, plainURL)
	t.Setenv(fakeForgeCLIUpstreamEnv, newTestServer(t, depot))

	w := newWorld(t,
		withNamedSecrets(map[string]string{"team-default": def.secret, "ghe": ghe.secret, "plain": plain.secret}),
		func(c *worldConfig) {
			c.wrapUpstream = func(backend http.Handler) http.Handler {
				def.backend = backend
				return def
			}
		})
	def.dir = w.upstreamDir
	for name, h := range map[string]gitproxy.CredentialHost{
		"ghe":   {Host: ghe.host, Kind: model.GitHostGitHub, BaseURL: gheURL},
		"plain": {Host: plain.host, Kind: model.GitHostGit, BaseURL: plainURL},
	} {
		if err := w.credentials.SetHost(name, h); err != nil {
			t.Fatalf("recording %s's host: %v", name, err)
		}
	}
	if err := w.credentials.SetAmbientOn("depot", gitproxy.CredentialHost{Host: depotHost, Kind: model.GitHostGitHub},
		gitproxy.AmbientSourceAuto); err != nil {
		t.Fatalf("recording the forge's ambient credential: %v", err)
	}
	// The team's connections: the default host's on the team credential,
	// and one on each other host.
	if err := w.store.UpdateConfig(w.ctx, func(c *model.Config) error {
		c.PollInterval = model.DefaultPollInterval
		c.GitCredential = "team-default"
		c.GitConnections = []model.TeamGitConnection{
			{Credential: "ghe", Host: ghe.host},
			{Credential: "plain", Host: plain.host},
			{Credential: "depot", Host: depotHost},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	hosts := []*hostUpstream{def, ghe, plain, depot}
	for _, h := range hosts {
		h.newRepo()
	}
	client := ui.NewClient(ui.Config{Actor: ui.DefaultActor("alice"), Capabilities: ui.OfferedCapabilities()}, w.store)
	client.Config.Credentials = w.credentials
	// What a forge answers for a repository is its owner/name and never
	// its host -- the command-backed forge's full_name, which is what
	// grain/task-4257 stored in place of the host it was asked about.
	client.Config.RepoNames = hostlessRepoNames{}
	srv := httptest.NewServer(accessCodeServer(client))
	t.Cleanup(srv.Close)
	return &hostsRig{w: w, hosts: hosts, client: client, api: ui.NewHTTPClient(srv.URL), clock: baseTime}
}

// withNamedSecrets writes extra named tokens for the proxy to resolve.
func withNamedSecrets(secrets map[string]string) worldOption {
	return func(c *worldConfig) { c.namedSecrets = secrets }
}

// hostlessRepoNames answers every repository the way a forge's REST API
// does: its full_name, owner/name, with no host in it, whichever host it
// was asked about.
type hostlessRepoNames struct{}

func (hostlessRepoNames) RepoFullName(_, owner, name string) (string, error) {
	return owner + "/" + name, nil
}

// remote is the proxy URL a sandbox clones h's acme/widgets with.
func (r *hostsRig) remote(h *hostUpstream) string {
	if h.host == "" {
		return r.w.remote(gitHostsOwner, gitHostsRepo)
	}
	return r.w.proxyURL + "/" + h.host + "/" + gitHostsOwner + "/" + gitHostsRepo + ".git"
}

// targets is the deployment's repository list.
func (r *hostsRig) targets() []string {
	got, err := r.w.store.TargetRepos(r.w.ctx)
	if err != nil {
		r.w.t.Fatal(err)
	}
	return got
}

// assertTargets is the repository list holding acme/widgets once on
// every host.
func (r *hostsRig) assertTargets(t *testing.T, when string) {
	t.Helper()
	var want []string
	for _, h := range r.hosts {
		want = append(want, h.ref().String())
	}
	got := r.targets()
	if !slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(want))) {
		t.Fatalf("%s: repositories = %q, want acme/widgets once on each host %q", when, got, want)
	}
}

// fileTask files a task against h's repository through POST /api/tasks,
// reading reads and also writing writes, and returns it as stored.
func (r *hostsRig) fileTask(t *testing.T, h *hostUpstream, title string, reads, writes []model.RepoRef) model.Task {
	t.Helper()
	var readNames []string
	for _, ref := range reads {
		readNames = append(readNames, ref.String())
	}
	var writeTargets []ui.WriteTarget
	for _, ref := range writes {
		writeTargets = append(writeTargets, ui.WriteTarget{Repo: ref.String()})
	}
	created, err := r.api.CreateTask(context.Background(), ui.CreateTaskRequest{
		Title: title, Description: "work on " + h.ref().String(),
		Approved: true, Repo: h.ref().String(), Reads: readNames, Writes: writeTargets,
	})
	if err != nil {
		t.Fatalf("POST /api/tasks for %s: %v", h.ref(), err)
	}
	task, err := r.w.store.GetTask(r.w.ctx, created.ID)
	if err != nil || task == nil {
		t.Fatalf("reading back %s: %v", created.ID, err)
	}
	if task.Target == nil || !task.Target.Same(h.ref()) || task.Target.Host != h.ref().Host {
		t.Fatalf("task filed for %s stored target %v", h.ref(), task.Target)
	}
	for i, ref := range reads {
		if i >= len(task.Reads) || !task.Reads[i].Same(ref) || task.Reads[i].Host != ref.Host {
			t.Fatalf("task filed reading %q stored reads %v", readNames, task.Reads)
		}
	}
	for i, ref := range writes {
		if i >= len(task.Writes) || !task.Writes[i].Repo.Same(ref) || task.Writes[i].Repo.Host != ref.Host {
			t.Fatalf("task filed writing %v stored writes %v", writes, task.Writes)
		}
	}
	return *task
}

// dispatchOne runs a cycle and returns its dispatch of task, the only
// one it may start.
func (r *hostsRig) dispatchOne(t *testing.T, task model.Task) dispatch.Dispatch {
	t.Helper()
	r.clock = r.clock.Add(time.Minute)
	dispatches, err := dispatch.Cycle(r.w.ctx, r.w.store, model.Limits{Workers: 1}, r.clock)
	if err != nil {
		t.Fatal(err)
	}
	if len(dispatches) != 1 || dispatches[0].TaskID != task.ID {
		t.Fatalf("Cycle dispatched %+v, want exactly %s", dispatches, task.ID)
	}
	return dispatches[0]
}

// runPush runs d's agent cloning h's repository and pushing its branch,
// then closes the task, so that the next cycle starts the next one
// rather than retrying this one: what happens to a pushed branch after
// the push is pkg/daemon/forgeroute_test.go's, not this file's.
func (r *hostsRig) runPush(d dispatch.Dispatch, h *hostUpstream, task model.Task) *agent.Result {
	r.clock = r.clock.Add(time.Minute)
	result := r.w.runDispatch(d, pushScript(r.remote(h), task.BranchName(), task.ID), r.clock)
	closed := r.clock
	if err := r.w.store.Observe(r.w.ctx, model.Observation{TaskID: task.ID, CompletedAt: &closed, ClosedAt: &closed}); err != nil {
		r.w.t.Fatalf("closing %s: %v", task.ID, err)
	}
	return result
}

// forgetCredentials clears what every host has been sent.
func (r *hostsRig) forgetCredentials() {
	for _, h := range r.hosts {
		h.credentialsSeen()
	}
}

// assertOnlyOwnCredential checks that target was reached, and with its
// own credential only, and that no other host was sent anything.
func (r *hostsRig) assertOnlyOwnCredential(t *testing.T, target *hostUpstream) {
	t.Helper()
	for _, h := range r.hosts {
		seen := h.credentialsSeen()
		if h != target {
			if len(seen) != 0 {
				t.Errorf("%s was sent %d requests for a task on %s", h.label(), len(seen), target.label())
			}
			continue
		}
		if len(seen) == 0 {
			t.Errorf("%s was never reached", h.label())
		}
		for _, got := range seen {
			if got != h.secret {
				t.Errorf("%s was sent credential %q, want only its own", h.label(), got)
			}
		}
	}
}

// commandOutput is everything the run's commands printed.
func commandOutput(result *agent.Result) string {
	var b strings.Builder
	for _, c := range result.ToolCalls {
		b.WriteString(c.Text)
		b.WriteString("\n")
	}
	return b.String()
}

// The whole task path on every host, side by side: the repository added
// and re-added and its settings changed without losing its host, a task
// filed against it through POST /api/tasks that reads the same
// owner/name on the next host over and writes it on the one after, a real dispatch, and a clone and push
// through the git proxy that reaches that host alone on that host's
// credential alone. Then a state-repo export and import keeps every one.
func TestTaskLifecycleOnEveryKindOfGitHost(t *testing.T) {
	r := newHostsRig(t)
	ctx := r.w.ctx

	for _, h := range r.hosts {
		if _, err := r.client.AddTargetRepo(ctx, h.ref().String()); err != nil {
			t.Fatalf("adding %s: %v", h.ref(), err)
		}
	}
	r.assertTargets(t, "after adding each host's acme/widgets")
	// Re-adding is a no-op, the plain git server's repository with the
	// clone URL its sandboxes use included, and so is a settings change.
	for _, h := range r.hosts {
		cloneURL := ""
		if h == r.hosts[2] {
			cloneURL = "http://" + h.host + "/acme/widgets.git"
		}
		if _, err := r.client.AddTargetRepoWithCloneURL(ctx, h.ref().String(), cloneURL); err != nil {
			t.Fatalf("re-adding %s: %v", h.ref(), err)
		}
		if _, err := r.client.SetRepoMergeOnGreen(ctx, h.ref().String(), true); err != nil {
			t.Fatalf("changing %s's settings: %v", h.ref(), err)
		}
	}
	r.assertTargets(t, "after re-adding and changing settings")
	if rc, err := r.w.store.GetRepoConfig(ctx, r.hosts[2].ref()); err != nil || rc == nil || rc.CloneURL == "" {
		t.Fatalf("the plain git server's clone URL was not kept on its own repository: %+v, %v", rc, err)
	}
	if rc, err := r.w.store.GetRepoConfig(ctx, r.hosts[0].ref()); err != nil || (rc != nil && rc.CloneURL != "") {
		t.Fatalf("the plain git server's clone URL landed on the default host's acme/widgets: %+v, %v", rc, err)
	}

	tasks := map[string]model.Task{}
	for i, h := range r.hosts {
		next := r.hosts[(i+1)%len(r.hosts)]
		after := r.hosts[(i+2)%len(r.hosts)]
		t.Run(h.label(), func(t *testing.T) {
			task := r.fileTask(t, h, "work on "+h.label(), []model.RepoRef{next.ref()}, []model.RepoRef{after.ref()})
			tasks[h.label()] = task
			d := r.dispatchOne(t, task)
			r.forgetCredentials()
			result := r.runPush(d, h, task)
			if !pushedOK(result) {
				t.Fatalf("the run on %s did not push cleanly:\n%s", h.label(), commandOutput(result))
			}
			if got := h.branchTip(task.BranchName()); got != "agent commit for "+task.ID {
				t.Fatalf("%s's branch on %s = %q, want the agent's commit", task.BranchName(), h.label(), got)
			}
			for _, other := range r.hosts {
				if other != h && other.branchTip(task.BranchName()) != "" {
					t.Errorf("%s's branch landed on %s too", task.BranchName(), other.label())
				}
			}
			r.assertOnlyOwnCredential(t, h)
		})
	}

	// grain sync's round trip: the deployment exported to a state
	// repository and imported into an empty one keeps every host.
	dir := t.TempDir()
	if err := staterepo.Export(ctx, r.w.db, dir); err != nil {
		t.Fatalf("exporting: %v", err)
	}
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	imported := model.New(db)
	if err := imported.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := staterepo.Import(ctx, db, dir); err != nil {
		t.Fatalf("importing: %v", err)
	}
	got, err := imported.TargetRepos(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(r.targets()))) {
		t.Errorf("repositories after the round trip = %q, want %q", got, r.targets())
	}
	for label, task := range tasks {
		back, err := imported.GetTask(ctx, task.ID)
		if err != nil || back == nil {
			t.Fatalf("task on %s after the round trip: %v", label, err)
		}
		if back.Target == nil || *back.Target != *task.Target {
			t.Errorf("task on %s came back targeting %v, want %v", label, back.Target, task.Target)
		}
		if !slices.Equal(back.Reads, task.Reads) {
			t.Errorf("task on %s came back reading %v, want %v", label, back.Reads, task.Reads)
		}
		if !slices.Equal(back.Writes, task.Writes) {
			t.Errorf("task on %s came back writing %v, want %v", label, back.Writes, task.Writes)
		}
	}
}

// The command-backed forge's failures, through the same git proxy: a
// lapsed login answers with the login page, and a credential the forge no
// longer takes answers 401. Neither may read as a missing repository,
// and each names the source's own fix. (Neither is yet one
// gitproxy.IsAccessRefusal knows, so the orchestrator's own clone retries
// such a task rather than parking it on that fix: filed from
// grain/task-4345.)
// readsAsMissingRepo reports whether git's output says the repository was
// not there: "not found", or a 404 status. 404 is matched as a number of
// its own, since the output also quotes the git proxy's URL, and a random
// port such as 127.0.0.1:40439 contains it.
func readsAsMissingRepo(out string) bool {
	return strings.Contains(out, "not found") || status404.MatchString(out)
}

var status404 = regexp.MustCompile(`\b404\b`)

func TestACommandBackedForgesLapsedLoginSaysToRenewIt(t *testing.T) {
	r := newHostsRig(t)
	depot := r.hosts[3]
	if _, err := r.client.AddTargetRepo(r.w.ctx, depot.ref().String()); err != nil {
		t.Fatal(err)
	}

	depot.fail(hostLoginPage)
	task := r.fileTask(t, depot, "push with a lapsed login", nil, nil)
	result := r.runPush(r.dispatchOne(t, task), depot, task)
	out := commandOutput(result)
	if pushedOK(result) {
		t.Fatalf("a clone through a lapsed login succeeded:\n%s", out)
	}
	if !strings.Contains(out, gitproxytest.Summary) && !strings.Contains(out, gitproxytest.Action) {
		t.Errorf("a lapsed login on %s said %q, want the source's login fix (%q)", depotHost, out, gitproxytest.Summary)
	}
	if readsAsMissingRepo(out) {
		t.Errorf("a lapsed login on %s read as a missing repository: %q", depotHost, out)
	}

	depot.fail(hostUnauthorized)
	task = r.fileTask(t, depot, "push with a refused credential", nil, nil)
	result = r.runPush(r.dispatchOne(t, task), depot, task)
	out = commandOutput(result)
	if pushedOK(result) {
		t.Fatalf("a clone on a refused credential succeeded:\n%s", out)
	}
	// The source's LoginFix, with the host filled in.
	if fix := "GH_HOST=" + depotHost + " forgecli auth login"; !strings.Contains(out, fix) {
		t.Errorf("a refused credential on %s said %q, want the login renewed (%q)", depotHost, out, fix)
	}
	if readsAsMissingRepo(out) {
		t.Errorf("a refused credential on %s read as a missing repository: %q", depotHost, out)
	}
}

// A host's connection taken away while a task on it is running: its
// clone is refused, naming the host, and nothing is sent to that host or
// any other -- in particular not the default host's credential.
func TestAHostsConnectionRemovedMidTaskRefusesNamingTheHost(t *testing.T) {
	r := newHostsRig(t)
	ghe := r.hosts[1]
	if _, err := r.client.AddTargetRepo(r.w.ctx, ghe.ref().String()); err != nil {
		t.Fatal(err)
	}
	task := r.fileTask(t, ghe, "lose the connection", nil, nil)
	d := r.dispatchOne(t, task)
	if err := r.w.store.UpdateConfig(r.w.ctx, func(c *model.Config) error {
		c.GitConnections = slices.DeleteFunc(c.GitConnections, func(conn model.TeamGitConnection) bool {
			return conn.Host == ghe.host
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r.forgetCredentials()
	result := r.runPush(d, ghe, task)
	out := commandOutput(result)
	if pushedOK(result) {
		t.Fatalf("a clone on %s with no connection for it succeeded:\n%s", ghe.host, out)
	}
	if !strings.Contains(out, ghe.host) {
		t.Errorf("the refusal for %s's repository does not name the host: %q", ghe.host, out)
	}
	// What the orchestrator's own clone parks the task on, with the
	// refusal as its reason, rather than retrying it (stopOnAccessRefused).
	if !gitproxy.IsAccessRefusal(out) {
		t.Errorf("the refusal for %s's repository is not one the dispatcher parks on: %q", ghe.host, out)
	}
	for _, h := range r.hosts {
		if seen := h.credentialsSeen(); len(seen) != 0 {
			t.Errorf("%s was sent %d requests for a repository on %s with no connection", h.label(), len(seen), ghe.host)
		}
	}
}
