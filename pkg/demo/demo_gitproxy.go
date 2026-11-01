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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/cgi"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/grainlog"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/namespace"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/redact"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/systemlog"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// demoGitProxy is the running proxy and what minting a run's access to it
// needs.
type demoGitProxy struct {
	store  *model.Store
	tokens *gitproxy.SandboxTokenStore
	repo   model.RepoRef
	proxy  *gitproxy.GitProxy
	ns     *namespace.Server
	cli    *daemon.SandboxCLIBackend
	api    *daemon.InProcessAPI
	// url is the proxy's own base URL, http://127.0.0.1:PORT.
	url string
}

// CreditCLIWait implements gitproxy.CLIWaitCreditor for runs started on the
// demo git proxy.
func (p *demoGitProxy) CreditCLIWait(_ context.Context, _ string, blocked time.Duration) (gitproxy.CLIWaitCredit, error) {
	credited := blocked
	if credited > orchestrator.RunWaitCreditCap {
		credited = orchestrator.RunWaitCreditCap
	}
	return gitproxy.CLIWaitCredit{
		Blocked:  blocked,
		Credited: credited,
		Cap:      orchestrator.RunWaitCreditCap,
		Live:     true,
	}, nil
}

func demoSrcDir(dir string) string {
	if info, err := os.Stat("/usr/local/share/grain/src"); err == nil && info.IsDir() {
		return "/usr/local/share/grain/src"
	}
	if info, err := os.Stat("playbooks"); err == nil && info.IsDir() {
		if abs, err := filepath.Abs("."); err == nil {
			return abs
		}
	}
	src := filepath.Join(dir, "src")
	_ = os.MkdirAll(filepath.Join(src, "playbooks"), 0o755)
	_ = os.WriteFile(filepath.Join(src, "playbooks", "incident-triage.md"), []byte("# Incident Triage\n\nCheck CI runs and logs.\n"), 0o644)
	return src
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

	// Ensure state-repo/settings/settings.yaml and secrets/secrets.key exist so
	// config://, deploy://health, and deploy://grain.log reads work in demo.
	settingsDir := filepath.Join(daemon.StateRepoDir(dir), "settings")
	_ = os.MkdirAll(settingsDir, 0o755)
	_ = os.WriteFile(filepath.Join(settingsDir, "settings.yaml"), []byte("default_model: gemini-2.5-pro\n"), 0o644)
	_ = secrets.New(daemon.SecretsDir(dir))

	logPath := filepath.Join(dir, "demo-daemon.log")
	_ = os.WriteFile(logPath, []byte("2026-10-29T00:00:00Z grain demo: started daemon log\n"), 0o644)
	api := &daemon.InProcessAPI{}
	srcDir := demoSrcDir(dir)
	coreSrcDir, coreModule := namespace.CoreSource(srcDir, "/usr/local/share/grain/core-src")
	nsServer := &namespace.Server{
		ArtifactDir: filepath.Join(dir, "state", "ci-artifacts"),
		SrcDir:      srcDir,
		CoreSrcDir:  coreSrcDir,
		CoreModule:  coreModule,
		NewRedactor: func() *redact.Redactor { return daemon.BuildNamespaceRedactor(dir) },
	}
	cliBackend := daemon.WireSandboxNamespace(nsServer, daemon.SandboxNamespaceConfig{
		DataDir:      dir,
		Store:        store,
		InsecureHTTP: true,
		API:          api,
		GrainLogSources: []grainlog.Source{
			{
				Name: "daemon",
				What: "the grain demo daemon's log",
				Read: func(_ context.Context, _ systemlog.Query) ([]string, error) {
					b, err := os.ReadFile(logPath)
					if err != nil {
						return nil, err
					}
					trimmed := strings.TrimRight(string(b), "\n")
					if trimmed == "" {
						return nil, nil
					}
					return strings.Split(trimmed, "\n"), nil
				},
			},
		},
	})
	p := &demoGitProxy{
		store:  store,
		tokens: gitproxy.NewSandboxTokenStore(filepath.Join(dir, "secrets", "sandbox-tokens.json")),
		repo:   repo,
		ns:     nsServer,
		cli:    cliBackend,
		api:    api,
	}
	proxy, err := gitproxy.BuildProxy(gitproxy.BuildConfig{
		DataDir:     dir,
		Store:       store,
		ForwardHost: forgeAddr,
		ForwardTLS:  false,
		Namespace:   nsServer,
		CLI:         cliBackend,
		WaitCredit:  p,
		API:         api,
	})
	if err != nil {
		return nil, fmt.Errorf("building the git proxy: %w", err)
	}
	p.proxy = proxy
	proxyAddr, err := serveLoopback(gitproxy.NewHandler(proxy))
	if err != nil {
		return nil, fmt.Errorf("serving the git proxy: %w", err)
	}
	p.url = "http://" + proxyAddr
	// Worded without "at http://", which ui/e2e/fixtures.js reads as the
	// UI's own address.
	log.Printf("grain demo: -git-proxy: grain's git proxy listens on %s, forwarding %s to a simulated GitHub on %s; "+
		"POST /api/e2e/git-proxy/runs {\"taskId\": ...} starts a run and mints its sandbox's token",
		p.url, full, forgeAddr)
	return p, nil
}

// wireServer connects the proxy's namespace and /cli/ routes to the demo's
// UI/API server, CI run store, and simulated GitHub client once they are built.
func (p *demoGitProxy) wireServer(srv http.Handler, client *ui.Client, cfg ui.Config) {
	p.api.Set(srv)
	p.proxy.CLIActions = ui.SandboxCLIActions{Client: client}
	if ciStore, ok := cfg.CI.(ci.RunStore); ok {
		p.ns.CIStore = ciStore
	} else if p.store != nil {
		p.ns.CIStore = ci.NewStoredRunStore(p.store, ci.StoredRunStoreOptions{})
	}
	if ghClient, ok := cfg.GitHubBranches.(github.Client); ok {
		p.cli.SetIssues(ghClient)
	}
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
