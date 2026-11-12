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

package hostsrig

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

func init() {
	if filepath.Base(os.Args[0]) == gitproxytest.Command && os.Getenv(FakeForgeCLIUpstreamEnv) != "" {
		os.Exit(FakeForgeCLI(os.Args[1:]))
	}
	gitproxytest.Install()
}

const (
	// FakeForgeCLIUpstreamEnv names the server FakeForgeCLI forwards requests
	// to when standing in for a command-backed forge's front end.
	FakeForgeCLIUpstreamEnv = "GRAIN_E2E_FAKE_FORGECLI_UPSTREAM"

	// DefaultOwner and DefaultRepo are the default repository coordinates used
	// across all four hosts when Config leaves Owner or Repo unset.
	DefaultOwner = "acme"
	DefaultRepo  = "widgets"

	testAccessCode = "TEST-ACCESS-CODE-0123"
)

// FakeForgeCLITokenFor returns the token FakeForgeCLI prints for host.
func FakeForgeCLITokenFor(host string) string { return "forgecli-token-" + host }

// FakeForgeCLI implements gitproxytest.Command (`forgecli`) when a test binary
// is invoked under that name: `auth token` prints a host-specific token and
// `api` sends the request to FakeForgeCLIUpstreamEnv, printing an `--include`
// HTTP response the way gh does.
func FakeForgeCLI(args []string) int {
	if len(args) >= 2 && args[0] == "auth" && args[1] == "token" {
		fmt.Println(FakeForgeCLITokenFor(os.Getenv("GH_HOST")))
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
	upstream := os.Getenv(FakeForgeCLIUpstreamEnv)
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
	_ = resp.Header.Write(out)
	out.WriteString("\r\n")
	_, _ = os.Stdout.Write(out.Bytes())
	_, _ = io.Copy(os.Stdout, resp.Body)
	return 0
}

// HostFailure controls how an in-process HostUpstream answers instead of
// serving git.
type HostFailure string

const (
	HostServes HostFailure = ""
	// HostLoginPage answers with an HTML login page pointing at
	// gitproxytest.LoginHost (HTTP 200), matching a tool that follows SSO
	// redirects.
	HostLoginPage HostFailure = "login"
	// HostUnauthorized answers 401 Bad credentials.
	HostUnauthorized HostFailure = "unauthorized"
)

// HostUpstream is an in-process smart-HTTP git host backed by bare
// repositories on disk, recording the Basic credential on each request.
type HostUpstream struct {
	t       testing.TB
	Host    string
	Kind    string
	Secret  string
	Owner   string
	Repo    string
	Dir     string
	Backend http.Handler

	mu      sync.Mutex
	seen    []string
	failure HostFailure
}

// NewHostUpstream builds an in-process smart-HTTP git host in a temp directory.
func NewHostUpstream(t testing.TB, kind, host, secret string) *HostUpstream {
	dir := t.TempDir()
	return &HostUpstream{
		t:       t,
		Kind:    kind,
		Host:    host,
		Secret:  secret,
		Owner:   DefaultOwner,
		Repo:    DefaultRepo,
		Dir:     dir,
		Backend: GitHTTPBackend(dir),
	}
}

func (u *HostUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u.mu.Lock()
	u.seen = append(u.seen, basicPassword(r.Header.Get("Authorization")))
	failure := u.failure
	u.mu.Unlock()
	switch failure {
	case HostLoginPage:
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<html><body><form action="https://%s/login">Sign in</form></body></html>`, gitproxytest.LoginHost)
		return
	case HostUnauthorized:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"message":"Bad credentials"}`)
		return
	}
	u.Backend.ServeHTTP(w, r)
}

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

// Fail sets how u responds to subsequent requests.
func (u *HostUpstream) Fail(f HostFailure) {
	u.mu.Lock()
	u.failure = f
	u.mu.Unlock()
}

// CredentialsSeen returns and clears the credentials received since the last call.
func (u *HostUpstream) CredentialsSeen() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	seen := u.seen
	u.seen = nil
	return seen
}

// Label names the host for subtests and failure messages.
func (u *HostUpstream) Label() string {
	if u.Host == "" {
		return u.Kind
	}
	return u.Kind + " (" + u.Host + ")"
}

// Ref returns u's repository reference.
func (u *HostUpstream) Ref() model.RepoRef {
	owner := u.Owner
	if owner == "" {
		owner = DefaultOwner
	}
	return model.RepoRef{Host: u.Host, Owner: owner, Name: u.Repo}
}

// Bare returns the filesystem path of u's bare repository.
func (u *HostUpstream) Bare() string {
	owner := u.Owner
	if owner == "" {
		owner = DefaultOwner
	}
	return filepath.Join(u.Dir, owner, u.Repo+".git")
}

// NewRepo seeds u's bare repository with an initial commit on main.
func (u *HostUpstream) NewRepo() {
	t := u.t
	t.Helper()
	owner := u.Owner
	if owner == "" {
		owner = DefaultOwner
	}
	bare := initBareRepo(t, u.Bare(), "main")
	runCmd(t, u.Dir, "git", "-C", bare, "config", "http.receivepack", "true")
	seed := filepath.Join(t.TempDir(), "seed")
	runCmd(t, u.Dir, "git", "clone", "-q", bare, seed)
	mustWriteFile(t, filepath.Join(seed, "README.md"), owner+"/"+u.Repo+" on "+u.Label()+"\n")
	runCmd(t, seed, "git", "add", "README.md")
	runCmd(t, seed, "git", "-c", "user.email=s@e", "-c", "user.name=s", "commit", "-q", "-m", "initial commit")
	runCmd(t, seed, "git", "push", "-q", "origin", "HEAD:main")
}

// BranchTip returns the commit subject at refs/heads/branch, or "" if absent.
func (u *HostUpstream) BranchTip(branch string) string {
	out, err := exec.Command("git", "--git-dir", u.Bare(), "log", "-1", "--format=%s", "refs/heads/"+branch).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// HeadSHA returns the commit SHA at refs/heads/branch on u.
func (u *HostUpstream) HeadSHA(branch string) string {
	out, err := exec.Command("git", "--git-dir", u.Bare(), "rev-parse", "refs/heads/"+branch).Output()
	if err != nil {
		u.t.Fatalf("reading %s on %s: %v", branch, u.Label(), err)
	}
	return strings.TrimSpace(string(out))
}

// Config configures the multi-host lifecycle rig. Leaving Config zero uses
// gitproxytest's in-process forgecli fake on gitproxytest.Host with
// acme/widgets; editions supply a real command-backed source, host, and
// existing scratch repository.
type Config struct {
	// Source is the command-backed ambient source to exercise. If Source.Name
	// is non-empty and not yet registered with gitproxy, the rig registers it
	// for the duration of the test. Leave zero to use SourceName (or auto-pick
	// the registered source claiming Host), or leave Host empty as well to use
	// gitproxytest's in-process forgecli fake.
	Source gitproxy.AmbientCommandSource

	// SourceName names an already-registered AmbientCommandSource when Source
	// is left zero. When empty and Host is set, gitproxy.AmbientAutoSource(Host)
	// is used.
	SourceName string

	// Host is the command-backed forge's hostname. When empty, the rig uses
	// gitproxytest's in-process fake on gitproxytest.Host; when set, the rig
	// does not create a bare repository for Host and verifies branch tips via
	// `git ls-remote` through the proxy.
	Host string

	// Owner and Repo name an existing scratch repository on Host when Host is
	// set (and also name the seeded repositories on the three in-process
	// hosts, so all four hosts share the same owner/name). Defaults to
	// DefaultOwner ("acme") and DefaultRepo ("widgets").
	Owner string
	Repo  string

	// LapsedHost, when non-empty, overrides Host in LapsedLogin with a host
	// claimed by the same source that the test account is not logged into.
	// When empty, LapsedLogin uses Host.
	LapsedHost string

	// AuthFixSummary is an optional authfix.Fix Summary or Action substring
	// that LapsedLogin accepts alongside the source's expanded LoginFix.
	AuthFixSummary string
}

func (c Config) ownerRepo() (string, string) {
	owner := c.Owner
	if owner == "" {
		owner = DefaultOwner
	}
	repo := c.Repo
	if repo == "" {
		repo = DefaultRepo
	}
	return owner, repo
}

// rigHost describes one of the four git hosts in a Rig.
type rigHost struct {
	kind       string
	host       string
	owner      string
	repo       string
	credential string
	upstream   *HostUpstream // nil when backed by a caller-supplied real forge
}

func (h *rigHost) label() string {
	if h.host == "" {
		return h.kind
	}
	return h.kind + " (" + h.host + ")"
}

func (h *rigHost) ref() model.RepoRef {
	return model.RepoRef{Host: h.host, Owner: h.owner, Name: h.repo}
}

// Rig is one single-tenant deployment wired to four git hosts: the default
// host, a second GitHub-kind host, a plain git server, and a command-backed
// forge.
type Rig struct {
	w          *world
	hosts      []*rigHost // default, GitHub Enterprise, plain git, command-backed forge
	client     *ui.Client
	api        *ui.HTTPClient
	clock      time.Time
	owner      string
	repo       string
	sourceName string
	realForge  bool
}

// New builds a Rig for cfg.
func New(t testing.TB, cfg Config) *Rig {
	t.Helper()
	owner, repo := cfg.ownerRepo()
	realForge := cfg.Host != ""

	defUp := NewHostUpstream(t, "the default host", "", "default-secret")
	gheUp := NewHostUpstream(t, "the second GitHub host", "", "ghe-secret")
	plainUp := NewHostUpstream(t, "the plain git server", "", "plain-secret")
	for _, u := range []*HostUpstream{defUp, gheUp, plainUp} {
		u.Owner, u.Repo = owner, repo
	}

	gheURL := NewTestServer(t, gheUp)
	plainURL := NewTestServer(t, plainUp)
	gheUp.Host, plainUp.Host = MustHost(t, gheURL), MustHost(t, plainURL)

	var (
		depotHost   string
		depotSource string
		depotUp     *HostUpstream
	)
	if !realForge {
		self, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		bin := t.TempDir()
		if err := os.Symlink(self, filepath.Join(bin, gitproxytest.Command)); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

		depotHost = gitproxytest.Host
		depotSource = gitproxy.AmbientSourceAuto
		depotUp = NewHostUpstream(t, "the command-backed forge", depotHost, FakeForgeCLITokenFor(depotHost))
		depotUp.Owner, depotUp.Repo = owner, repo
		t.Setenv(FakeForgeCLIUpstreamEnv, NewTestServer(t, depotUp))
	} else {
		depotHost = cfg.Host
		depotSource = ensureAmbientSource(t, cfg.Source, cfg.SourceName, depotHost)
	}

	w := newWorld(t,
		map[string]string{"team-default": defUp.Secret, "ghe": gheUp.Secret, "plain": plainUp.Secret},
		func(backend http.Handler) http.Handler {
			defUp.Backend = backend
			return defUp
		},
	)
	defUp.Dir = w.upstreamDir

	for name, h := range map[string]gitproxy.CredentialHost{
		"ghe":   {Host: gheUp.Host, Kind: model.GitHostGitHub, BaseURL: gheURL},
		"plain": {Host: plainUp.Host, Kind: model.GitHostGit, BaseURL: plainURL},
	} {
		if err := w.credentials.SetHost(name, h); err != nil {
			t.Fatalf("recording %s's host: %v", name, err)
		}
	}
	ambientArg := depotSource
	if ambientArg == "" {
		ambientArg = gitproxy.AmbientSourceAuto
	}
	if err := w.credentials.SetAmbientOn("depot", gitproxy.CredentialHost{Host: depotHost, Kind: model.GitHostGitHub}, ambientArg); err != nil {
		t.Fatalf("recording the forge's ambient credential: %v", err)
	}

	if err := w.store.UpdateConfig(w.ctx, func(c *model.Config) error {
		c.PollInterval = model.DefaultPollInterval
		c.TaskBranchTemplate = model.DefaultTaskBranchTemplate
		c.GitCredential = "team-default"
		c.GitConnections = []model.TeamGitConnection{
			{Credential: "ghe", Host: gheUp.Host},
			{Credential: "plain", Host: plainUp.Host},
			{Credential: "depot", Host: depotHost},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	hosts := []*rigHost{
		{kind: defUp.Kind, host: defUp.Host, owner: owner, repo: repo, credential: "team-default", upstream: defUp},
		{kind: gheUp.Kind, host: gheUp.Host, owner: owner, repo: repo, credential: "ghe", upstream: gheUp},
		{kind: plainUp.Kind, host: plainUp.Host, owner: owner, repo: repo, credential: "plain", upstream: plainUp},
		{kind: "the command-backed forge", host: depotHost, owner: owner, repo: repo, credential: "depot", upstream: depotUp},
	}
	for _, h := range hosts {
		if h.upstream != nil {
			h.upstream.NewRepo()
		}
	}

	client := ui.NewClient(ui.Config{
		Actor:         ui.DefaultActor("alice"),
		Capabilities:  ui.OfferedCapabilities(),
		LocalAPIToken: testAccessCode,
	}, w.store)
	client.Config.Credentials = w.credentials
	client.Config.RepoNames = hostlessRepoNames{}
	srv := httptest.NewServer(ui.NewServerWithClient(client))
	t.Cleanup(srv.Close)
	api := ui.NewHTTPClient(srv.URL)
	api.Token = testAccessCode

	resolvedSource := depotSource
	if resolvedSource == "" || resolvedSource == gitproxy.AmbientSourceAuto {
		resolvedSource = gitproxy.AmbientAutoSource(depotHost)
	}
	return &Rig{
		w:          w,
		hosts:      hosts,
		client:     client,
		api:        api,
		clock:      baseTime,
		owner:      owner,
		repo:       repo,
		sourceName: resolvedSource,
		realForge:  realForge,
	}
}

func ensureAmbientSource(t testing.TB, src gitproxy.AmbientCommandSource, name, host string) string {
	t.Helper()
	if src.Name != "" {
		already := false
		for _, have := range gitproxy.RegisteredAmbientSources() {
			if have.Name == src.Name {
				already = true
				break
			}
		}
		if !already {
			unregister := gitproxy.RegisterAmbientSource(src)
			t.Cleanup(unregister)
		}
		return src.Name
	}
	if name != "" {
		return name
	}
	return gitproxy.AmbientSourceAuto
}

type hostlessRepoNames struct{}

func (hostlessRepoNames) RepoFullName(_, owner, name string) (string, error) {
	return owner + "/" + name, nil
}

func (r *Rig) remote(h *rigHost) string {
	if h.host == "" {
		return r.w.remote(h.owner, h.repo)
	}
	return r.w.proxyURL + "/" + h.host + "/" + h.owner + "/" + h.repo + ".git"
}

func (r *Rig) targets(t testing.TB) []string {
	t.Helper()
	got, err := r.w.store.TargetRepos(r.w.ctx)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func (r *Rig) assertTargets(t testing.TB, when string) {
	t.Helper()
	var want []string
	for _, h := range r.hosts {
		want = append(want, h.ref().String())
	}
	got := r.targets(t)
	if !slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(want))) {
		t.Fatalf("%s: repositories = %q, want %s/%s once on each host %q", when, got, r.owner, r.repo, want)
	}
}

func (r *Rig) fileTask(t testing.TB, h *rigHost, title string, reads, writes []model.RepoRef) model.Task {
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

func (r *Rig) dispatchOne(t testing.TB, task model.Task) dispatch.Dispatch {
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

func (r *Rig) runPush(t testing.TB, d dispatch.Dispatch, h *rigHost, task model.Task) *agent.Result {
	t.Helper()
	r.clock = r.clock.Add(time.Minute)
	result := r.w.runDispatch(d, pushScript(r.remote(h), task.BranchName(), task.ID), r.clock)
	closed := r.clock
	if err := r.w.store.Observe(r.w.ctx, model.Observation{TaskID: task.ID, CompletedAt: &closed, ClosedAt: &closed}); err != nil {
		t.Fatalf("closing %s: %v", task.ID, err)
	}
	return result
}

func (r *Rig) forgetCredentials() {
	r.w.audit.drain()
	for _, h := range r.hosts {
		if h.upstream != nil {
			h.upstream.CredentialsSeen()
		}
	}
}

// assertOnlyOwnCredential checks both the proxy's audit log (available for
// every host, including a real command-backed forge) and any in-process
// HostUpstream to verify that target alone was reached, and only on target's
// own credential.
func (r *Rig) assertOnlyOwnCredential(t testing.TB, target *rigHost) {
	t.Helper()
	entries := r.w.audit.drain()
	forwarded := 0
	for _, e := range entries {
		if e.Host != target.host {
			t.Errorf("proxy recorded request for host %q (%s) during a task on %s", e.Host, e.Outcome, target.label())
			continue
		}
		if e.Credential != target.credential {
			t.Errorf("%s used proxy credential %q (%s), want only %q", target.label(), e.Credential, e.Outcome, target.credential)
		}
		if strings.HasPrefix(e.Outcome, "forwarded: ") {
			forwarded++
		}
	}
	if forwarded == 0 {
		t.Errorf("%s had no request forwarded by the proxy (audit entries: %+v)", target.label(), entries)
	}

	for _, h := range r.hosts {
		if h.upstream == nil {
			continue
		}
		seen := h.upstream.CredentialsSeen()
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
			if got != h.upstream.Secret {
				t.Errorf("%s was sent credential %q, want only its own", h.label(), got)
			}
		}
	}
}

func (r *Rig) branchExistsOrMatches(h *rigHost, branch string) string {
	if h.upstream != nil {
		return h.upstream.BranchTip(branch)
	}
	return r.w.lsRemoteSHA(r.remote(h), h.ref(), branch)
}

func runSubtest(t testing.TB, name string, fn func(testing.TB)) {
	if tt, ok := t.(*testing.T); ok {
		tt.Run(name, func(sub *testing.T) { fn(sub) })
		return
	}
	fn(t)
}

// Lifecycle exercises adding, re-adding, and updating settings on four hosts
// sharing one owner/repo; filing tasks via POST /api/tasks that read and write
// the other hosts; dispatching, cloning, and pushing through the git proxy on
// each host's own credential alone; and round-tripping through state-repo
// export and import.
func Lifecycle(t testing.TB, cfg Config) {
	t.Helper()
	r := New(t, cfg)
	ctx := r.w.ctx

	for _, h := range r.hosts {
		if _, err := r.client.AddTargetRepo(ctx, h.ref().String()); err != nil {
			t.Fatalf("adding %s: %v", h.ref(), err)
		}
	}
	r.assertTargets(t, "after adding each host's "+r.owner+"/"+r.repo)

	for _, h := range r.hosts {
		cloneURL := ""
		if h == r.hosts[2] {
			cloneURL = "http://" + h.host + "/" + r.owner + "/" + r.repo + ".git"
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
		t.Fatalf("the plain git server's clone URL landed on the default host's %s/%s: %+v, %v", r.owner, r.repo, rc, err)
	}

	tasks := map[string]model.Task{}
	for i, h := range r.hosts {
		next := r.hosts[(i+1)%len(r.hosts)]
		after := r.hosts[(i+2)%len(r.hosts)]
		runSubtest(t, h.label(), func(sub testing.TB) {
			sub.Helper()
			task := r.fileTask(sub, h, "work on "+h.label(), []model.RepoRef{next.ref()}, []model.RepoRef{after.ref()})
			tasks[h.label()] = task
			d := r.dispatchOne(sub, task)
			r.forgetCredentials()
			result := r.runPush(sub, d, h, task)
			if !pushedOK(result) {
				sub.Fatalf("the run on %s did not push cleanly:\n%s", h.label(), commandOutput(result))
			}
			r.assertOnlyOwnCredential(sub, h)

			if h.upstream != nil {
				if got := h.upstream.BranchTip(task.BranchName()); got != "agent commit for "+task.ID {
					sub.Fatalf("%s's branch on %s = %q, want the agent's commit", task.BranchName(), h.label(), got)
				}
			} else {
				wantSHA := r.w.workHeadSHA(d.RunID)
				if gotSHA := r.w.lsRemoteSHA(r.remote(h), h.ref(), task.BranchName()); gotSHA != wantSHA {
					sub.Fatalf("%s's branch SHA on %s via ls-remote = %q, want %q", task.BranchName(), h.label(), gotSHA, wantSHA)
				}
			}
			for _, other := range r.hosts {
				if other != h && r.branchExistsOrMatches(other, task.BranchName()) != "" {
					sub.Errorf("%s's branch landed on %s too", task.BranchName(), other.label())
				}
			}
			r.forgetCredentials()
		})
	}

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
	wantTargets := r.targets(t)
	if !slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(wantTargets))) {
		t.Errorf("repositories after the round trip = %q, want %q", got, wantTargets)
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

var status404 = regexp.MustCompile(`\b404\b`)

func readsAsMissingRepo(out string) bool {
	return strings.Contains(out, "not found") || status404.MatchString(out)
}

func assertParksOnRenewingTheLogin(t testing.TB, host, what, out, fix string) {
	t.Helper()
	said, ok := gitproxy.LoginLapsed(out)
	if !ok {
		t.Errorf("%s on %s is not one the dispatcher parks on renewing the login: %q", what, host, out)
	} else if fix != "" && !strings.Contains(said, fix) {
		t.Errorf("%s on %s parks on %q; want it to say %q", what, host, said, fix)
	}
	if gitproxy.IsAccessRefusal(out) {
		t.Errorf("%s on %s reads as the credential lacking access: %q", what, host, out)
	}
}

// LapsedLogin exercises cloning from a command-backed forge when the host's
// login is missing or lapsed: the clone fails naming the source's LoginFix or
// authfix Summary, and never reads as a missing repository (404 / "not found")
// or an access refusal.
func LapsedLogin(t testing.TB, cfg Config) {
	t.Helper()
	lapsedCfg := cfg
	if cfg.LapsedHost != "" {
		lapsedCfg.Host = cfg.LapsedHost
	}
	r := New(t, lapsedCfg)
	depot := r.hosts[3]
	if _, err := r.client.AddTargetRepo(r.w.ctx, depot.ref().String()); err != nil {
		t.Fatal(err)
	}

	if depot.upstream != nil {
		depot.upstream.Fail(HostLoginPage)
		task := r.fileTask(t, depot, "push with a lapsed login", nil, nil)
		result := r.runPush(t, r.dispatchOne(t, task), depot, task)
		out := commandOutput(result)
		if pushedOK(result) {
			t.Fatalf("a clone through a lapsed login succeeded:\n%s", out)
		}
		if !strings.Contains(out, gitproxytest.Summary) && !strings.Contains(out, gitproxytest.Action) {
			t.Errorf("a lapsed login on %s said %q, want the source's login fix (%q)", depot.host, out, gitproxytest.Summary)
		}
		if readsAsMissingRepo(out) {
			t.Errorf("a lapsed login on %s read as a missing repository: %q", depot.host, out)
		}
		assertParksOnRenewingTheLogin(t, depot.host, "a lapsed login", out, gitproxytest.Action)

		depot.upstream.Fail(HostUnauthorized)
		task = r.fileTask(t, depot, "push with a refused credential", nil, nil)
		result = r.runPush(t, r.dispatchOne(t, task), depot, task)
		out = commandOutput(result)
		if pushedOK(result) {
			t.Fatalf("a clone on a refused credential succeeded:\n%s", out)
		}
		fix := gitproxy.AmbientLoginFix(depot.host, r.sourceName)
		if !strings.Contains(out, fix) {
			t.Errorf("a refused credential on %s said %q, want the login renewed (%q)", depot.host, out, fix)
		}
		if readsAsMissingRepo(out) {
			t.Errorf("a refused credential on %s read as a missing repository: %q", depot.host, out)
		}
		assertParksOnRenewingTheLogin(t, depot.host, "a refused credential", out, fix)
		return
	}

	task := r.fileTask(t, depot, "push with a lapsed login", nil, nil)
	result := r.runPush(t, r.dispatchOne(t, task), depot, task)
	out := commandOutput(result)
	if pushedOK(result) {
		t.Fatalf("a clone through an unauthenticated host %s succeeded:\n%s", depot.host, out)
	}
	if readsAsMissingRepo(out) {
		t.Errorf("a lapsed login on %s read as a missing repository: %q", depot.host, out)
	}
	if gitproxy.IsAccessRefusal(out) {
		t.Errorf("a lapsed login on %s reads as the credential lacking access: %q", depot.host, out)
	}
	loginFix := gitproxy.AmbientLoginFix(depot.host, r.sourceName)
	authSummary := cfg.AuthFixSummary
	if authSummary == "" {
		if _, _, err := gitproxy.AmbientToken(depot.host, r.sourceName); err != nil {
			if _, rest, ok := strings.Cut(err.Error(), " token for "+depot.host+": "); ok {
				authSummary, _, _ = strings.Cut(rest, ": ")
			}
		}
	}
	_, lapsedMarker := gitproxy.LoginLapsed(out)
	matchedFix := (loginFix != "" && strings.Contains(out, loginFix)) ||
		(authSummary != "" && strings.Contains(out, authSummary)) ||
		(cfg.AuthFixSummary == "" && lapsedMarker)
	if !matchedFix {
		t.Errorf("a lapsed login on %s said %q, want LoginFix %q or authfix summary %q", depot.host, out, loginFix, authSummary)
	}
}

// ConnectionRemovedMidTask exercises removing a host's git connection after a
// task on it has been dispatched: the clone is refused naming the host, matches
// gitproxy.IsAccessRefusal so the dispatcher parks rather than retrying, and
// neither selects a credential on the proxy nor touches any upstream host.
func ConnectionRemovedMidTask(t testing.TB, cfg Config) {
	t.Helper()
	r := New(t, cfg)
	target := r.hosts[1]
	if r.realForge {
		target = r.hosts[3]
	}
	if _, err := r.client.AddTargetRepo(r.w.ctx, target.ref().String()); err != nil {
		t.Fatal(err)
	}
	task := r.fileTask(t, target, "lose the connection", nil, nil)
	d := r.dispatchOne(t, task)
	if err := r.w.store.UpdateConfig(r.w.ctx, func(c *model.Config) error {
		c.GitConnections = slices.DeleteFunc(c.GitConnections, func(conn model.TeamGitConnection) bool {
			return conn.Host == target.host
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r.forgetCredentials()
	result := r.runPush(t, d, target, task)
	out := commandOutput(result)
	if pushedOK(result) {
		t.Fatalf("a clone on %s with no connection for it succeeded:\n%s", target.host, out)
	}
	if !strings.Contains(out, target.host) {
		t.Errorf("the refusal for %s's repository does not name the host: %q", target.host, out)
	}
	if !gitproxy.IsAccessRefusal(out) {
		t.Errorf("the refusal for %s's repository is not one the dispatcher parks on: %q", target.host, out)
	}
	for _, e := range r.w.audit.drain() {
		if e.Credential != "" || strings.HasPrefix(e.Outcome, "forwarded: ") {
			t.Errorf("proxy selected credential %q (%s) for %s after its connection was removed", e.Credential, e.Outcome, target.host)
		}
		if e.Host != target.host {
			t.Errorf("proxy recorded request for host %q during a task on %s with no connection", e.Host, target.host)
		}
	}
	for _, h := range r.hosts {
		if h.upstream == nil {
			continue
		}
		if seen := h.upstream.CredentialsSeen(); len(seen) != 0 {
			t.Errorf("%s was sent %d requests for a repository on %s with no connection", h.label(), len(seen), target.host)
		}
	}
}
