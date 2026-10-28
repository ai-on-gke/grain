package demo

// demo_gitproxy.go is `grain demo -github-sim -git-proxy`: grain's own git
// proxy in front of the simulated GitHub's bare repo, so a walkthrough can
// `git push` as a team's run and read what the push wrote -- Settings ->
// Teams -> Audit / Security, above all, which has nothing else to show in
// a demo (docs/test-runs/2026-10-12-team-credential-audit.md, F2).
//
// The proxy is the real one, gitproxy.BuildProxy over the demo's own
// store and its secrets directory, with the team audit sink, the model
// authorizer and the ref policy a deployment has. What it forwards to is
// GitHub's git half, simulated: githubsim.GitUpstream's credential check
// in front of `git http-backend` over the bare repo -github-sim already
// keeps, on loopback, so ForwardTLS false is allowed. Nothing in a demo
// dispatches a run or mints its sandbox a token, so the one thing added
// is the step that does both (POST /api/e2e/git-proxy/runs): the run row
// StartRun records and the token SandboxTokenStore mints, as the
// orchestrator's runOne would.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/cgi"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/bwsalmon/grain/pkg/github/githubsim"
	"github.com/bwsalmon/grain/pkg/gitproxy"
	"github.com/bwsalmon/grain/pkg/model"
)

// demoGitProxy is the running proxy and what minting a run's access to it
// needs.
type demoGitProxy struct {
	store  *model.Store
	tokens *gitproxy.SandboxTokenStore
	repo   model.RepoRef
	// url is the proxy's own base URL, http://127.0.0.1:PORT.
	url string
}

// configureGitProxyDemo serves the bare repo at bare (configureGitHubSimDemo's)
// as GitHub's git endpoints would, and the real git proxy in front of
// them, each on a loopback port of its own. token is the credential the
// demo's teams push with (secrets/github/default.token), the one token
// the simulated forge accepts. public serves the repo to an anonymous
// fetch too (-github-sim-public); it is private otherwise, as the
// repositories grain works in usually are.
func configureGitProxyDemo(dir string, store *model.Store, repo model.RepoRef, bare, token string, public bool) (*demoGitProxy, error) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		return nil, fmt.Errorf("the git proxy forwards to `git http-backend`, and git is not installed: %w", err)
	}
	// http-backend refuses a push from a caller with no REMOTE_USER unless
	// the repo says otherwise; GitUpstream is what decides who may push.
	if _, err := demoGit(bare, "config", "http.receivepack", "true"); err != nil {
		return nil, err
	}
	full := repo.Owner + "/" + repo.Name
	var publicRepos []string
	if public {
		publicRepos = []string{full}
	}
	forge := &githubsim.GitUpstream{
		Tokens: map[string]githubsim.GitAccess{token: {Read: []string{full}, Write: []string{full}}},
		Public: publicRepos,
		Backend: &cgi.Handler{
			Path: gitBin,
			Args: []string{"http-backend"},
			Env: []string{
				"GIT_PROJECT_ROOT=" + filepath.Join(dir, "githubsim"),
				"GIT_HTTP_EXPORT_ALL=1",
			},
		},
	}
	forgeAddr, err := serveLoopback(forge)
	if err != nil {
		return nil, fmt.Errorf("serving the simulated forge: %w", err)
	}

	proxy, err := gitproxy.BuildProxy(gitproxy.BuildConfig{
		DataDir: dir, Store: store,
		ForwardHost: forgeAddr, ForwardTLS: false,
	})
	if err != nil {
		return nil, fmt.Errorf("building the git proxy: %w", err)
	}
	proxyAddr, err := serveLoopback(gitproxy.NewHandler(proxy))
	if err != nil {
		return nil, fmt.Errorf("serving the git proxy: %w", err)
	}
	p := &demoGitProxy{
		store:  store,
		tokens: gitproxy.NewSandboxTokenStore(filepath.Join(dir, "secrets", "sandbox-tokens.json")),
		repo:   repo,
		url:    "http://" + proxyAddr,
	}
	// Worded without "at http://", which ui/e2e/fixtures.js reads as the
	// UI's own address.
	log.Printf("grain demo: -git-proxy: grain's git proxy listens on %s, forwarding %s to a simulated GitHub on %s; "+
		"POST /api/e2e/git-proxy/runs {\"taskId\": ...} starts a run and mints its sandbox's token",
		p.url, full, forgeAddr)
	return p, nil
}

// serveLoopback serves h on a free loopback port for as long as the demo
// runs, and returns the host:port it took.
func serveLoopback(h http.Handler) (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	go func() {
		if err := http.Serve(ln, h); err != nil {
			log.Printf("grain demo: -git-proxy: %v", err)
		}
	}()
	return ln.Addr().String(), nil
}

// demoGitProxyRun is what POST /api/e2e/git-proxy/runs answers: who the
// run's sandbox is, the token it authenticates to the proxy with, and
// where and what it may push.
type demoGitProxyRun struct {
	TaskID  string `json:"taskId"`
	RunID   string `json:"runId"`
	Sandbox string `json:"sandbox"`
	Token   string `json:"token"`
	Remote  string `json:"remote"`
	Branch  string `json:"branch"`
}

// startRun records a live run of taskID on a sandbox of its own and mints
// that sandbox's token, as dispatch and the orchestrator do for a real
// one.
func (p *demoGitProxy) startRun(r *http.Request) (demoGitProxyRun, int, error) {
	var req struct {
		TaskID string `json:"taskId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.TaskID == "" {
		return demoGitProxyRun{}, http.StatusBadRequest, errors.New(`body must be {"taskId": "<id>"}`)
	}
	ctx := r.Context()
	task, err := p.store.GetTask(ctx, req.TaskID)
	if err != nil {
		return demoGitProxyRun{}, http.StatusInternalServerError, err
	}
	if task == nil {
		return demoGitProxyRun{}, http.StatusNotFound, fmt.Errorf("no task %q", req.TaskID)
	}
	runs, err := p.store.Runs(ctx, task.ID)
	if err != nil {
		return demoGitProxyRun{}, http.StatusInternalServerError, err
	}
	attempt := len(runs) + 1
	run := model.Run{
		ID:        fmt.Sprintf("%s-run-%d", task.ID, attempt),
		TaskID:    task.ID,
		Sandbox:   fmt.Sprintf("demo-sandbox-%s-%d", task.ID, attempt),
		Attempt:   attempt,
		StartedAt: time.Now().UTC(),
	}
	if err := p.store.StartRun(ctx, run, model.Limits{}); err != nil {
		return demoGitProxyRun{}, http.StatusConflict, err
	}
	token, err := p.tokens.EnsureToken(run.Sandbox)
	if err != nil {
		return demoGitProxyRun{}, http.StatusInternalServerError, err
	}
	target := p.repo
	if task.Target != nil {
		target = *task.Target
	}
	return demoGitProxyRun{
		TaskID:  task.ID,
		RunID:   run.ID,
		Sandbox: run.Sandbox,
		Token:   token,
		Remote:  p.url + "/" + target.Owner + "/" + target.Name + ".git",
		Branch:  task.BranchName(),
	}, http.StatusOK, nil
}

// gitProxyDemoHandler adds POST /api/e2e/git-proxy/runs in front of app.
func gitProxyDemoHandler(app http.Handler, p *demoGitProxy) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/e2e/git-proxy/runs" {
			app.ServeHTTP(w, r)
			return
		}
		run, status, err := p.startRun(r)
		if err != nil {
			http.Error(w, err.Error(), status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(run)
	})
}
