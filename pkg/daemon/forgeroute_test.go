package daemon

// Server-side traffic for a repository on a GitHub Enterprise host that
// only `forgecli` reaches (depot.code.forge.test): the forge calls a finishing
// run, the merge queue and grain CI make, and the git commands grain runs
// here itself, all routed to that host through forgecli, and none of them to
// the daemon's own -github-host.
//
// forgecli is this test binary itself, run under that name (TestMain): it does
// what the real one does for grain -- `auth token` prints a login, `api`
// sends one request and prints it --include style -- against a fake
// Enterprise host on loopback (fakeDepot) rather than the forge's front end.

import (
	"bytes"
	"context"
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
	"sync"
	"testing"

	"github.com/ai-on-gke/grain/pkg/gitproxy/gitproxytest"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// The made-up command-backed source these tests run against.
func init() { gitproxytest.Install() }

const (
	depotTestHost  = "depot.code.forge.test"
	depotTestOwner = "experimental"
	depotTestRepo  = "bsalmon-test"
	// fakeForgeCLIUpstreamEnv names the fake Enterprise host the fake forgecli
	// sends to.
	fakeForgeCLIUpstreamEnv = "GRAIN_TEST_FAKE_FORGECLI_UPSTREAM"
)

func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) == "forgecli" {
		os.Exit(fakeForgeCLI(os.Args[1:]))
	}
	os.Exit(m.Run())
}

// fakeForgeCLI is `forgecli` for these tests.
func fakeForgeCLI(args []string) int {
	if len(args) >= 2 && args[0] == "auth" && args[1] == "token" {
		fmt.Println("forgecli-token-" + os.Getenv("GH_HOST"))
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

// fakeDepot is the Enterprise host: its REST API under /api/v3, served
// by a githubsim over the bare repository, and git smart HTTP for the
// same repository through `git http-backend`. Every request is recorded.
type fakeDepot struct {
	sim  *githubsim.Sim
	bare string
	mu   sync.Mutex
	reqs []string
	auth []string
}

func (d *fakeDepot) requests() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.reqs...)
}

func (d *fakeDepot) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	d.reqs = append(d.reqs, r.Method+" "+r.URL.RequestURI()+" host="+r.Header.Get("X-Fake-ForgeCLI-Host"))
	if a := r.Header.Get("Authorization"); a != "" {
		d.auth = append(d.auth, a)
	}
	d.mu.Unlock()
	if rest, ok := strings.CutPrefix(r.URL.RequestURI(), "/api/v3"); ok {
		body, _ := io.ReadAll(r.Body)
		headers := map[string]string{}
		for k := range r.Header {
			headers[k] = r.Header.Get(k)
		}
		var resp github.ApiResponse
		var err error
		func() {
			defer func() {
				if p := recover(); p != nil {
					err = fmt.Errorf("%v", p)
				}
			}()
			resp, err = d.sim.Request(r.Method, rest, headers, body)
		}()
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotImplemented)
			return
		}
		for k, v := range resp.Headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(resp.Status)
		w.Write(resp.Body)
		return
	}
	gitExec, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	(&cgi.Handler{
		Path: filepath.Join(strings.TrimSpace(string(gitExec)), "git-http-backend"),
		Env:  []string{"GIT_PROJECT_ROOT=" + filepath.Dir(filepath.Dir(d.bare)), "GIT_HTTP_EXPORT_ALL=1"},
	}).ServeHTTP(w, r)
}

// depotFixture is the fake forgecli on PATH, the fake depot behind it, an
// ambient forgecli credential for depot in a credential set, and a store
// whose one repository is the depot one.
type depotFixture struct {
	depot  *fakeDepot
	creds  *gitproxy.CredentialSet
	store  *model.Store
	ctx    context.Context
	repo   model.RepoRef
	seeded string
}

func newDepotFixture(t *testing.T) *depotFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.Symlink(self, filepath.Join(bin, "forgecli")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	root := t.TempDir()
	bare := filepath.Join(root, depotTestOwner, depotTestRepo+".git")
	gitT(t, "", "init", "-q", "--bare", "-b", "main", bare)
	seed := filepath.Join(t.TempDir(), "seed")
	gitT(t, "", "clone", "-q", bare, seed)
	if err := os.MkdirAll(filepath.Join(seed, ".grain", "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	workflow := "name: ci\non: [push]\njobs:\n  go:\n    steps:\n      - run: go test ./...\n"
	if err := os.WriteFile(filepath.Join(seed, ".grain", "workflows", "ci.yml"), []byte(workflow), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, seed, "add", ".")
	gitT(t, seed, "-c", "user.email=s@example.com", "-c", "user.name=s", "commit", "-q", "-m", "seed")
	gitT(t, seed, "push", "-q", "origin", "main")

	sim := githubsim.New(depotTestOwner, depotTestRepo, bare, "main")
	sim.PullRequestBase = githubsim.UniquePullRequestBase()
	depot := &fakeDepot{sim: sim, bare: bare}
	srv := httptest.NewServer(depot)
	t.Cleanup(srv.Close)
	t.Setenv(fakeForgeCLIUpstreamEnv, srv.URL)

	creds, err := gitproxy.LoadCredentialSet(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := creds.SetAmbientOn("depot", gitproxy.CredentialHost{Host: depotTestHost, Kind: model.GitHostGitHub},
		gitproxy.AmbientSourceAuto); err != nil {
		t.Fatal(err)
	}
	store, ctx := openStuckTestStore(t)
	// The owner's install: single-tenant, its one team's connection on
	// depot the ambient forgecli login -- which is what the git proxy pushes
	// the task's branch with, and so what everything here must use too.
	store.SingleTenant = true
	if err := store.UpdateConfig(ctx, func(c *model.Config) error {
		c.GitConnections = []model.TeamGitConnection{{Credential: "depot", Host: depotTestHost}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	repo := model.RepoRef{Host: depotTestHost, Owner: depotTestOwner, Name: depotTestRepo}
	return &depotFixture{depot: depot, creds: creds, store: store, ctx: ctx, repo: repo, seeded: seed}
}

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// pushBranch pushes branch, one commit past main, straight into the
// bare repository -- standing in for the sandbox's push through the git
// proxy, which already works for this host.
func (f *depotFixture) pushBranch(t *testing.T, branch string) {
	t.Helper()
	gitT(t, f.seeded, "checkout", "-q", "-b", branch, "main")
	if err := os.WriteFile(filepath.Join(f.seeded, "work.txt"), []byte(branch), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, f.seeded, "add", ".")
	gitT(t, f.seeded, "-c", "user.email=s@example.com", "-c", "user.name=s", "commit", "-q", "-m", "work")
	gitT(t, f.seeded, "push", "-q", "origin", branch)
	gitT(t, f.seeded, "checkout", "-q", "main")
}

// fileTask files a task targeting the depot repository.
func (f *depotFixture) fileTask(t *testing.T, id string) model.Task {
	t.Helper()
	stuckTask(t, f.ctx, f.store, id)
	task, err := f.store.GetTask(f.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	repo := f.repo
	task.Target = &repo
	if err := f.store.PutTask(f.ctx, *task); err != nil {
		t.Fatal(err)
	}
	got, err := f.store.GetTask(f.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return *got
}

// defaultHostGuard is the -github-host transport, which nothing about the
// depot repository may reach: every call to it is recorded, and answered
// the way api.github.com answers a repository it has never heard of.
type defaultHostGuard struct {
	mu    sync.Mutex
	calls []string
}

func (g *defaultHostGuard) Request(method, path string, _ map[string]string, _ []byte) (github.ApiResponse, error) {
	g.mu.Lock()
	g.calls = append(g.calls, method+" "+path)
	g.mu.Unlock()
	return github.ApiResponse{Status: 404, Body: []byte(`{"message":"Not Found"}`),
		URL: method + " https://api.github.com" + path}, nil
}

func (g *defaultHostGuard) assertNoDepotCalls(t *testing.T) {
	t.Helper()
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, call := range g.calls {
		if strings.Contains(call, depotTestRepo) {
			t.Errorf("a request for the depot repository went to api.github.com: %s", call)
		}
	}
}

func (f *depotFixture) client(t *testing.T) (*github.RESTClient, *defaultHostGuard) {
	t.Helper()
	guard := &defaultHostGuard{}
	client, _ := newForgeClientOver(guard, model.DefaultGitHost, f.store, f.creds)
	return client, guard
}

// The run-finish path that 404'd: a pushed branch on depot is found, and
// the pull request for it opened, through forgecli on depot -- asked about by
// the bare owner the orchestrator passes, which the store resolves to
// depot.
func TestFinishOnAForgeCLIHostFindsTheBranchAndOpensThePullRequestThroughForgeCLI(t *testing.T) {
	f := newDepotFixture(t)
	task := f.fileTask(t, "t1")
	f.pushBranch(t, task.BranchName())
	client, guard := f.client(t)

	status, err := orchestrator.OpenPullRequestForTask(f.ctx, f.store, client, task)
	if err != nil {
		t.Fatalf("OpenPullRequestForTask: %v\ndepot saw: %q", err, f.depot.requests())
	}
	if status.PullRequest.Number == 0 {
		t.Fatalf("no pull request opened: %+v", status)
	}
	guard.assertNoDepotCalls(t)
	var sawBranch, sawCreate bool
	for _, r := range f.depot.requests() {
		if !strings.HasSuffix(r, "host="+depotTestHost) {
			t.Errorf("depot request %q was not sent by forgecli for %s", r, depotTestHost)
		}
		sawBranch = sawBranch || strings.Contains(r, "/repos/experimental/bsalmon-test/branches/")
		sawCreate = sawCreate || strings.HasPrefix(r, "POST /api/v3/repos/experimental/bsalmon-test/pulls ")
	}
	if !sawBranch || !sawCreate {
		t.Fatalf("depot saw no branch read or no pull request create: %q", f.depot.requests())
	}

	// The host-qualified owner a RepoRef's StoredOwner gives goes to the
	// same place, with the host taken back out of the path.
	if exists, err := client.BranchExists(f.repo.StoredOwner(), f.repo.Name, task.BranchName()); err != nil || !exists {
		t.Fatalf("BranchExists(host-qualified) = %v, %v", exists, err)
	}
	guard.assertNoDepotCalls(t)
}

// The merge queue's own calls for such a repository -- a batch branch
// cut from the base, the head merged into it, the base moved and the
// pull request merged -- all go to depot.
func TestMergeQueueBatchOnAForgeCLIHostGoesToThatHost(t *testing.T) {
	f := newDepotFixture(t)
	task := f.fileTask(t, "t2")
	f.pushBranch(t, task.BranchName())
	client, guard := f.client(t)
	owner, name := f.repo.Owner, f.repo.Name

	base, err := client.GetBranchHead(owner, name, "main")
	if err != nil || base == nil {
		t.Fatalf("GetBranchHead(main) = %v, %v", base, err)
	}
	batch := "grain/merge-batch/test"
	if err := client.CreateBranch(owner, name, batch, base.SHA); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if _, err := client.MergeBranch(owner, name, batch, task.BranchName(), "batch"); err != nil {
		t.Fatalf("MergeBranch: %v", err)
	}
	pr, err := client.CreatePullRequest(owner, name, task.BranchName(), "main", "t", "b")
	if err != nil {
		t.Fatalf("CreatePullRequest: %v", err)
	}
	head, err := client.GetBranchHead(owner, name, task.BranchName())
	if err != nil {
		t.Fatal(err)
	}
	if err := client.MergePullRequest(owner, name, pr.Number, head.SHA); err != nil {
		t.Fatalf("MergePullRequest: %v", err)
	}
	if err := client.DeleteBranch(owner, name, batch); err != nil {
		t.Fatalf("DeleteBranch: %v", err)
	}
	guard.assertNoDepotCalls(t)
	if len(f.depot.requests()) < 6 {
		t.Fatalf("depot saw %q", f.depot.requests())
	}
}

// grain CI plans a run for the depot repository: its workflows are read
// at a branch by a clone that goes through forgecli with the login as Basic
// auth, not by a plain clone of https://depot..., which the forge's front
// end redirects to its login page.
func TestGrainCIReadsAForgeCLIHostsWorkflowsThroughForgeCLI(t *testing.T) {
	f := newDepotFixture(t)
	f.pushBranch(t, "grain/e1010c47/task-2")
	byTeam, onHost := TeamGitCredentialLookups(f.store)
	src := CiWorkflowSource{Credentials: f.creds, TempDir: t.TempDir(),
		TeamGitCredential: byTeam, TeamGitCredentialOn: onHost}

	workflows, commit, err := src.Workflows(f.ctx, f.repo.String(), "grain/e1010c47/task-2")
	if err != nil {
		t.Fatalf("Workflows: %v\ndepot saw: %q", err, f.depot.requests())
	}
	if len(workflows) != 1 || commit == "" {
		t.Fatalf("workflows = %+v at %q", workflows, commit)
	}
	want := gitT(t, f.seeded, "rev-parse", "grain/e1010c47/task-2")
	if strings.TrimSpace(want) != commit {
		t.Fatalf("read at %s, want the branch's %s", commit, want)
	}
	var sawGit bool
	for _, r := range f.depot.requests() {
		if strings.HasPrefix(r, "GET /experimental/bsalmon-test.git/info/refs") && strings.HasSuffix(r, "host="+depotTestHost) {
			sawGit = true
		}
	}
	if !sawGit {
		t.Fatalf("the clone did not go through forgecli: %q", f.depot.requests())
	}
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:forgecli-token-"+depotTestHost))
	f.depot.mu.Lock()
	auth := append([]string(nil), f.depot.auth...)
	f.depot.mu.Unlock()
	if len(auth) == 0 || auth[0] != wantAuth {
		t.Fatalf("git requests carried %q, want %q", auth, wantAuth)
	}

	// And the history read a pinned run makes, through the same route.
	on, err := src.OnBranch(f.ctx, f.repo.String(), "grain/e1010c47/task-2", commit)
	if err != nil || !on {
		t.Fatalf("OnBranch = %v, %v", on, err)
	}
}

// A request the router cannot place on another host -- a repository the
// deployment knows on the default host, or on two hosts -- stays on the
// default host, and one that names its host always goes there.
func TestForgeRouterResolvesOnlyAnUnambiguousOtherHost(t *testing.T) {
	store, ctx := openStuckTestStore(t)
	for i, ref := range []model.RepoRef{
		{Host: depotTestHost, Owner: "experimental", Name: "only-depot"},
		{Host: depotTestHost, Owner: "acme", Name: "both"},
		{Owner: "acme", Name: "both"},
		{Owner: "acme", Name: "dotcom"},
	} {
		id := fmt.Sprintf("r%d", i)
		stuckTask(t, ctx, store, id)
		task, err := store.GetTask(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		r := ref
		task.Target = &r
		if err := store.PutTask(ctx, *task); err != nil {
			t.Fatal(err)
		}
	}
	hostOf := storeRepoHosts(store)
	for _, tc := range []struct{ owner, repo, want string }{
		{"experimental", "only-depot", depotTestHost},
		{"acme", "both", ""},
		{"acme", "dotcom", ""},
		{"nobody", "unknown", ""},
	} {
		if got := hostOf(tc.owner, tc.repo); got != tc.want {
			t.Errorf("hostOf(%s/%s) = %q, want %q", tc.owner, tc.repo, got, tc.want)
		}
	}

	r := &forgeRouter{defHost: model.DefaultGitHost, hostOf: hostOf}
	for _, tc := range []struct{ path, host, rest string }{
		{"/repos/depot.code.forge.test/acme/both/pulls?state=open", depotTestHost, "/repos/acme/both/pulls?state=open"},
		{"/repos/acme/both/pulls", "", "/repos/acme/both/pulls"},
		{"/repos/experimental/only-depot", depotTestHost, "/repos/experimental/only-depot"},
	} {
		host, _, _, rest, _, ok := r.route(tc.path, nil)
		if !ok || host != tc.host || rest != tc.rest {
			t.Errorf("route(%s) = %q %q %v, want %q %q", tc.path, host, rest, ok, tc.host, tc.rest)
		}
	}
	body := []byte(`{"query":"q","variables":{"owner":"depot.code.forge.test/acme","repo":"both","number":1}}`)
	host, owner, _, _, newBody, ok := r.route("/graphql", body)
	if !ok || host != depotTestHost || owner != "acme" || !bytes.Contains(newBody, []byte(`"owner":"acme"`)) {
		t.Errorf("route(graphql) = %q %q %s %v", host, owner, newBody, ok)
	}
}

// A 404 names the URL it came from, so a client aimed at the wrong host
// says which one in the same line.
func TestGitHubErrorNamesTheURLItAsked(t *testing.T) {
	err := (&github.Error{Status: 404, Body: []byte(`{"message":"Not Found"}`),
		URL: "GET https://api.github.com/repos/experimental/bsalmon-test"}).Error()
	if !strings.Contains(err, "from GET https://api.github.com/repos/experimental/bsalmon-test") {
		t.Fatalf("error = %q", err)
	}
}

// A deployment whose -github-host is the forgecli host itself: the plain
// transport for it would be redirected to the forge's login, so its
// repositories go through forgecli as well.
func TestForgeRouterSendsAForgeCLIDefaultHostThroughForgeCLI(t *testing.T) {
	f := newDepotFixture(t)
	task := f.fileTask(t, "t3")
	f.pushBranch(t, task.BranchName())
	guard := &defaultHostGuard{}
	client, _ := newForgeClientOver(guard, depotTestHost, f.store, f.creds)
	exists, err := client.BranchExists(f.repo.Owner, f.repo.Name, task.BranchName())
	if err != nil || !exists {
		t.Fatalf("BranchExists = %v, %v (depot saw %q)", exists, err, f.depot.requests())
	}
	if len(guard.calls) != 0 {
		t.Fatalf("the plain -github-host transport was asked: %q", guard.calls)
	}
}
