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

//go:build ignore

// Command walkthrough executes §7 of docs/test-plans/2026-11-07.md ("Task
// context, attribution, external references, review holds, PR adoption &
// grain-plc") against real SQLite stores, real bare git repositories served
// by git-http-backend behind the real scoped git proxy (pkg/gitproxy), a
// real orchestrator.RunCycle dispatching into host sandboxes, githubsim for
// the GitHub REST API, and ui.NewServerWithClient serving the real REST API
// over HTTP (so token scoping, owner:me and route gates are the production
// code paths).
//
//	go run ./docs/test-runs/2026-11-07-task-context-attribution-pr-plc/walkthrough.go -mode=all
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

var outDir string

const operatorToken = "walkthrough-3491-operator-code"

// checks accumulates every assertion the walkthrough makes, written to
// <item>.json beside the transcript-style log.
type checks struct {
	Item    string         `json:"item"`
	Results []checkResult  `json:"results"`
	Data    map[string]any `json:"data"`
}

type checkResult struct {
	Name   string `json:"name"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail,omitempty"`
}

func newChecks(item string) *checks { return &checks{Item: item, Data: map[string]any{}} }

func (c *checks) check(name string, pass bool, format string, args ...any) {
	detail := fmt.Sprintf(format, args...)
	c.Results = append(c.Results, checkResult{Name: name, Pass: pass, Detail: detail})
	mark := "PASS"
	if !pass {
		mark = "FAIL"
	}
	log.Printf("[%s] %s: %s -- %s", c.Item, mark, name, detail)
}

func (c *checks) write() int {
	data, _ := json.MarshalIndent(c, "", "  ")
	_ = os.WriteFile(filepath.Join(outDir, c.Item+".json"), append(data, '\n'), 0o644)
	failed := 0
	for _, r := range c.Results {
		if !r.Pass {
			failed++
		}
	}
	return failed
}

func main() {
	log.SetFlags(log.Ltime | log.Lmicroseconds)
	restore := orchestrator.SetCheckRegistrationWindow(0)
	defer restore()
	mode := flag.String("mode", "all", "item1 | item2 | item3 | item4 | all")
	flag.StringVar(&outDir, "out", "docs/test-runs/2026-11-07-task-context-attribution-pr-plc", "artifact directory")
	flag.Parse()
	ctx := context.Background()
	items := map[string]func(context.Context) *checks{
		"item1": runItem1, "item2": runItem2, "item3": runItem3, "item4": runItem4,
	}
	order := []string{"item1", "item2", "item3", "item4"}
	if *mode != "all" {
		order = []string{*mode}
	}
	failed := 0
	for _, name := range order {
		fn, ok := items[name]
		if !ok {
			log.Fatalf("unknown mode %q", name)
		}
		failed += fn(ctx).write()
	}
	if failed > 0 {
		log.Printf("%d check(s) failed", failed)
		os.Exit(1)
	}
	log.Printf("all checks passed")
}

// ---------------------------------------------------------------------------
// shared helpers

func gitEnv() []string {
	return append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=Walkthrough Tester", "GIT_AUTHOR_EMAIL=tester@example.com",
		"GIT_COMMITTER_NAME=Walkthrough Tester", "GIT_COMMITTER_EMAIL=tester@example.com")
}

func gitCmd(dir string, args ...string) string {
	out, err := gitTry(dir, args...)
	if err != nil {
		panic(fmt.Sprintf("git %v in %s: %v\n%s", args, dir, err, out))
	}
	return out
}

func gitTry(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func writeFiles(dir string, files map[string]string) {
	for k, v := range files {
		full := filepath.Join(dir, k)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(v), 0o644); err != nil {
			panic(err)
		}
	}
}

// initBare makes <root>/<owner>/<name>.git with main seeded from files, and
// returns the bare dir, a working clone and the seed commit.
func initBare(root, owner, name string, files map[string]string) (string, string, string) {
	bare := filepath.Join(root, owner, name+".git")
	_ = os.MkdirAll(filepath.Dir(bare), 0o755)
	gitCmd(root, "init", "-q", "--bare", bare)
	gitCmd(bare, "symbolic-ref", "HEAD", "refs/heads/main")
	gitCmd(bare, "config", "http.receivepack", "true")
	seed := filepath.Join(root, "seed-"+owner+"-"+name)
	gitCmd(root, "clone", "-q", bare, seed)
	gitCmd(seed, "checkout", "-q", "-b", "main")
	writeFiles(seed, files)
	gitCmd(seed, "add", ".")
	gitCmd(seed, "commit", "-q", "-m", "initial commit")
	gitCmd(seed, "push", "-q", "origin", "main")
	return bare, seed, gitCmd(seed, "rev-parse", "HEAD")
}

func commitAndPush(seed string, files map[string]string, msg string) string {
	writeFiles(seed, files)
	gitCmd(seed, "add", ".")
	gitCmd(seed, "commit", "-q", "-m", msg)
	gitCmd(seed, "push", "-q", "origin", "main")
	return gitCmd(seed, "rev-parse", "HEAD")
}

func openStore(ctx context.Context, dir string) (*model.Store, *sql.DB) {
	_ = os.MkdirAll(dir, 0o755)
	db, err := sqlite.Open(sqlite.DefaultConfig(dir))
	if err != nil {
		panic(err)
	}
	store := model.New(db)
	store.SingleTenant = true
	if err := store.Init(ctx); err != nil {
		panic(err)
	}
	return store, db
}

// gitRevResolver is ui.Config.RepoRevs over the bare repositories the
// scoped proxy forwards to: the in-process stand-in for the daemon's
// repoRevGate, which asks GitHub's refs API the same question.
type gitRevResolver struct{ root string }

func (g gitRevResolver) ResolveRepoRev(_ context.Context, repo model.RepoRef, rev string) (string, bool, error) {
	bare := filepath.Join(g.root, repo.Owner, repo.Name+".git")
	if _, err := os.Stat(bare); err != nil {
		return "", false, nil
	}
	if rev == "" {
		rev = "HEAD"
	}
	out, err := gitTry(bare, "rev-parse", "--verify", "--quiet", rev+"^{commit}")
	if err != nil {
		return "", true, nil
	}
	return out, true, nil
}

// gitServer is git-http-backend for every bare repo under root, behind a
// githubsim.GitUpstream that only lets botToken through, behind the real
// gitproxy that scopes each sandbox token to its task's repos.
type gitServer struct {
	root     string
	upstream *httptest.Server
	proxy    *httptest.Server
	tokens   *gitproxy.SandboxTokenStore
}

func startGit(ctx context.Context, tmp string, store *model.Store, repos []string) *gitServer {
	root := filepath.Join(tmp, "git")
	execPath := gitCmd(tmp, "--exec-path")
	backend := &cgi.Handler{
		Path: filepath.Join(execPath, "git-http-backend"),
		Env:  []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"},
	}
	up := httptest.NewServer(&githubsim.GitUpstream{
		Backend: backend,
		Tokens:  map[string]githubsim.GitAccess{"bot-token": {Read: repos, Write: repos}},
	})
	dataDir := filepath.Join(tmp, "data")
	_ = os.MkdirAll(filepath.Join(dataDir, "secrets", "github"), 0o700)
	_ = os.WriteFile(filepath.Join(dataDir, "secrets", "github", "credentials.json"), []byte(`{"*": "bot"}`), 0o600)
	_ = os.WriteFile(filepath.Join(dataDir, "secrets", "github", "bot.token"), []byte("bot-token"), 0o600)
	_ = store.UpdateConfig(ctx, func(cfg *model.Config) error { cfg.GitCredential = "bot"; return nil })
	proxy, err := gitproxy.BuildProxy(gitproxy.BuildConfig{
		DataDir: dataDir, Store: store, ForwardHost: strings.TrimPrefix(up.URL, "http://"),
		ForwardTLS: false, SingleTenant: true,
	})
	if err != nil {
		panic(err)
	}
	return &gitServer{
		root: root, upstream: up, proxy: httptest.NewServer(gitproxy.NewHandler(proxy)),
		tokens: gitproxy.NewSandboxTokenStore(filepath.Join(dataDir, "secrets", "sandbox-tokens.json")),
	}
}

func (g *gitServer) Close() { g.proxy.Close(); g.upstream.Close() }

type fnProvider func(ctx context.Context, rc agent.RunConfig) (*agent.Result, error)

func (p fnProvider) Run(ctx context.Context, rc agent.RunConfig) (*agent.Result, error) {
	return p(ctx, rc)
}

// apiClient is a tiny HTTP client against the real ui.Server.
type apiClient struct {
	base, token string
}

func (a apiClient) do(method, path string, body any) (int, []byte) {
	var rd io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		rd = bytes.NewReader(data)
	}
	req, _ := http.NewRequest(method, a.base+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if a.token != "" {
		req.Header.Set("Authorization", "Bearer "+a.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, []byte(err.Error())
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

// grainCLI runs the freshly built grain binary against a live server.
func grainCLI(server, token string, args ...string) (string, error) {
	bin := os.Getenv("GRAIN_BIN")
	if bin == "" {
		bin = "/home/debian/bin/grain"
	}
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), "GRAIN_SERVER="+server, "GRAIN_TOKEN="+token, "NO_COLOR=1")
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func clip(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 600 {
		s = s[:600] + " [clipped]"
	}
	return s
}

func taskByID(ctx context.Context, store *model.Store, id string) model.Task {
	t, err := store.GetTask(ctx, id)
	if err != nil || t == nil {
		panic(fmt.Sprintf("task %s: %v", id, err))
	}
	return *t
}

func designTargets(t model.Task) []string {
	var out []string
	for _, l := range t.Links {
		if l.Kind == model.LinkDesign {
			out = append(out, l.Target)
		}
	}
	return out
}

func repoStrings(rs []model.RepoRef) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.String())
	}
	return out
}

// ---------------------------------------------------------------------------
// Item 1: committed context end-to-end

func runItem1(ctx context.Context) *checks {
	c := newChecks("item1-committed-context")
	tmp, _ := os.MkdirTemp("", "walk-item1-*")
	defer os.RemoveAll(tmp)
	store, db := openStore(ctx, filepath.Join(tmp, "data"))
	defer db.Close()
	app := model.RepoRef{Owner: "acme", Name: "app"}
	specs := model.RepoRef{Owner: "acme", Name: "specs"}
	gs := startGit(ctx, tmp, store, []string{"acme/app", "acme/specs"})
	defer gs.Close()
	_, _, appSHA := initBare(gs.root, "acme", "app", map[string]string{"README.md": "acme app\n"})
	_, specsSeed, sha1 := initBare(gs.root, "acme", "specs", map[string]string{
		"architecture.md": "# Architecture v1 (approved)\n",
		"plc/042/spec.md": "approved spec v1\n",
		"plc/042/context.yml": "title: PLC-042 billing spec\ndescription: Approved by design review\nfiles:\n" +
			"  - path: spec.md\n    kind: design\n    title: Billing spec\n    source_url: https://planner.example.com/PLC-042\n",
	})
	c.Data["appSeedSHA"] = appSHA
	c.Data["specsSHA1"] = sha1
	for _, r := range []model.RepoRef{app, specs} {
		if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: r}); err != nil {
			panic(err)
		}
	}

	client := ui.NewClient(ui.Config{
		Actor:         model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com"},
		Capabilities:  ui.OfferedCapabilities(),
		RepoRevs:      gitRevResolver{root: gs.root},
		LocalAPIToken: operatorToken,
	}, store)
	srv := httptest.NewServer(ui.NewServerWithClient(client))
	defer srv.Close()
	api := apiClient{base: srv.URL, token: operatorToken}

	// 1a. autoGrantContextReads empty, no -read: refused at filing.
	code, body := api.do("POST", "/api/tasks", map[string]any{
		"title": "Implement architecture", "description": "Follow the spec.", "repo": "acme/app",
		"designs": []string{"acme/specs:architecture.md@main"},
	})
	c.check("1a refused without -read when autoGrantContextReads is empty",
		code == http.StatusBadRequest && strings.Contains(string(body), "grant read access with -read acme/specs"),
		"HTTP %d %s", code, clip(body))
	c.Data["1a_refusal"] = clip(body)

	// 1b. With explicit -read it files and pins main to sha1.
	code, body = api.do("POST", "/api/tasks", map[string]any{
		"title": "Implement architecture (explicit read)", "description": "x", "repo": "acme/app",
		"designs": []string{"acme/specs:architecture.md@main"}, "reads": []string{"acme/specs"},
	})
	var explicit ui.Task
	_ = json.Unmarshal(body, &explicit)
	c.check("1b explicit -read files and pins @main to the SHA",
		code == http.StatusCreated || code == http.StatusOK,
		"HTTP %d id=%s", code, explicit.ID)

	// 1c. Turn on autoGrantContextReads via the settings API.
	code, body = api.do("PUT", "/api/settings", map[string]any{"githubHost": "github.com", "autoGrantContextReads": []string{"acme/specs"}})
	cfg, _ := store.GetConfig(ctx)
	c.check("1c PUT /api/settings stores autoGrantContextReads",
		code < 300 && cfg != nil && slices.Contains(cfg.AutoGrantContextReads, "acme/specs"),
		"HTTP %d stored=%v %s", code, cfg.AutoGrantContextReads, clip(body))

	code, body = api.do("POST", "/api/tasks", map[string]any{
		"title": "Implement architecture", "description": "Follow the pinned spec.", "repo": "acme/app",
		"designs": []string{"acme/specs:architecture.md@main", "acme/specs:plc/042@main"},
	})
	var created ui.Task
	_ = json.Unmarshal(body, &created)
	task := taskByID(ctx, store, created.ID)
	wantDesigns := []string{"acme/specs:architecture.md@" + sha1, "acme/specs:plc/042@" + sha1}
	c.check("1d auto-granted -read acme/specs",
		slices.ContainsFunc(task.Reads, specs.Same), "reads=%v", repoStrings(task.Reads))
	c.check("1e @main pinned to immutable SHA at filing",
		slices.Equal(designTargets(task), wantDesigns), "designs=%v want %v", designTargets(task), wantDesigns)
	c.Data["filedTask"] = map[string]any{"id": task.ID, "designs": designTargets(task), "reads": repoStrings(task.Reads)}
	if task.Approval == nil {
		_ = client.Approve(ctx, task.ID)
	}
	// Only dispatch this one.
	if explicit.ID != "" {
		_ = client.Close(ctx, explicit.ID, ui.CloseOptions{})
	}

	// 1f. Advance acme/specs main before dispatch.
	sha2 := commitAndPush(specsSeed, map[string]string{
		"architecture.md": "# Architecture v2 (UNAPPROVED DRAFT)\n",
		"plc/042/spec.md": "unapproved draft v2\n",
	}, "draft v2")
	c.Data["specsSHA2"] = sha2

	sandboxRoot := filepath.Join(tmp, "sandboxes")
	_ = os.MkdirAll(sandboxRoot, 0o755)
	seen := map[string]any{}
	provider := fnProvider(func(ctx context.Context, rc agent.RunConfig) (*agent.Result, error) {
		clone := filepath.Join(rc.SandboxRoot, "repos", "acme", "specs")
		head, _ := gitTry(clone, "rev-parse", "HEAD")
		arch, _ := os.ReadFile(filepath.Join(clone, "architecture.md"))
		pushURL, _ := gitTry(clone, "remote", "get-url", "--push", "origin")
		seen["sandboxHead"] = head
		seen["architecture"] = string(arch)
		seen["pushURL"] = pushURL
		seen["promptPinned"] = strings.Contains(rc.Prompt, sha1)
		seen["promptManifestTitle"] = strings.Contains(rc.Prompt, "PLC-042 billing spec")
		seen["promptMentionsSHA2"] = strings.Contains(rc.Prompt, sha2)
		// Tamper attempt from inside the sandbox: push to the read-only clone.
		gitCmd(clone, "checkout", "-q", "-b", "tamper")
		_ = os.WriteFile(filepath.Join(clone, "architecture.md"), []byte("tampered\n"), 0o644)
		gitCmd(clone, "commit", "-qam", "tamper")
		out, err := gitTry(clone, "push", "-q", "origin", "HEAD:refs/heads/main")
		seen["tamperPushRefused"] = err != nil
		seen["tamperPushOutput"] = out
		// Also try pushing to the proxy URL directly (bypassing grain-read-only).
		fetchURL, _ := gitTry(clone, "remote", "get-url", "origin")
		cmd := exec.Command("git", "push", "-q", fetchURL, "HEAD:refs/heads/main")
		cmd.Dir = clone
		cmd.Env = append(gitEnv(), "HOME="+rc.SandboxRoot)
		raw, err := cmd.CombinedOutput()
		out = strings.TrimSpace(string(raw))
		seen["tamperProxyPushRefused"] = err != nil
		seen["tamperProxyPushOutput"] = out
		return &agent.Result{
			FinalText: "Implemented against the pinned spec.",
			ToolCalls: []agent.ToolCall{
				{Name: "propose_task", Arguments: map[string]any{
					"title": "Follow-up: add billing tests", "body": "Add tests per the pinned spec.",
				}, Text: "proposed"},
				{Name: "comment_on_issue", Arguments: map[string]any{"comment": "Done against the pinned spec."}, Text: "ok"},
			},
		}, nil
	})
	deps := orchestrator.Deps{
		Store:            store,
		Sandboxes:        orchestrator.NewHostSandboxes(sandboxRoot),
		Framework:        orchestrator.StaticFramework(provider),
		Config:           orchestrator.Config{GitRemoteBase: gs.proxy.URL},
		MintSandboxToken: gs.tokens.EnsureToken,
		MaxWorkers:       1,
	}
	now := time.Now().UTC()
	if err := orchestrator.RunCycle(ctx, deps, now); err != nil {
		c.check("1f RunCycle dispatch", false, "%v", err)
	}
	for i := 0; i < 5 && seen["sandboxHead"] == nil; i++ {
		_ = orchestrator.RunCycle(ctx, deps, now.Add(time.Duration(i+1)*time.Minute))
	}
	runs, _ := store.Runs(ctx, task.ID)
	if len(runs) > 0 {
		c.Data["run"] = map[string]any{"outcome": runs[len(runs)-1].Outcome, "detail": runs[len(runs)-1].Detail}
	}
	c.Data["sandbox"] = seen
	c.check("1f sandbox clone detached at pinned SHA1 though main moved to SHA2",
		seen["sandboxHead"] == sha1, "HEAD=%v sha1=%s sha2=%s", seen["sandboxHead"], sha1, sha2)
	c.check("1g sandbox sees approved v1 content", seen["architecture"] == "# Architecture v1 (approved)\n",
		"architecture.md=%q", seen["architecture"])
	c.check("1h prompt names pinned SHA and manifest title, never SHA2",
		seen["promptPinned"] == true && seen["promptManifestTitle"] == true && seen["promptMentionsSHA2"] == false,
		"pinned=%v manifest=%v sha2=%v", seen["promptPinned"], seen["promptManifestTitle"], seen["promptMentionsSHA2"])
	c.check("1i tamper: push via read-only remote refused", seen["tamperPushRefused"] == true,
		"pushURL=%v out=%v", seen["pushURL"], seen["tamperPushOutput"])
	c.check("1j tamper: push straight to proxy URL refused by scoped proxy", seen["tamperProxyPushRefused"] == true,
		"out=%v", seen["tamperProxyPushOutput"])
	specsHead := gitCmd(filepath.Join(gs.root, "acme", "specs.git"), "rev-parse", "main")
	c.check("1k acme/specs main untouched by sandbox", specsHead == sha2, "main=%s", specsHead)

	after := taskByID(ctx, store, task.ID)
	var manifest *model.ContextManifest
	for _, r := range after.ContextRefs() {
		if r.Path == "plc/042" {
			manifest = r.Manifest
		}
	}
	c.check("1l manifest persisted on task after checkout",
		manifest != nil && manifest.Title == "PLC-042 billing spec" && len(manifest.Files) == 1,
		"manifest=%+v", manifest)

	// Child proposal inherits the pinned refs + reads.
	all, _ := store.ListTasks(ctx)
	var child *model.Task
	for i := range all {
		if all[i].Title == "Follow-up: add billing tests" {
			child = &all[i]
		}
	}
	if child == nil {
		c.check("1m propose_task child filed", false, "no child task found")
	} else {
		c.check("1m propose_task child inherits pinned manifest refs",
			slices.Equal(designTargets(*child), wantDesigns), "child designs=%v", designTargets(*child))
		c.check("1n propose_task child inherits -read acme/specs",
			slices.ContainsFunc(child.Reads, specs.Same), "child reads=%v", repoStrings(child.Reads))
		var childManifest *model.ContextManifest
		for _, r := range child.ContextRefs() {
			if r.Path == "plc/042" {
				childManifest = r.Manifest
			}
		}
		c.check("1o propose_task child carries the manifest", childManifest != nil, "manifest=%+v", childManifest)
		c.Data["child"] = map[string]any{"id": child.ID, "designs": designTargets(*child), "reads": repoStrings(child.Reads)}
	}

	// REST payload the Task detail page renders.
	code, body = api.do("GET", "/api/tasks/"+task.ID, nil)
	var detail map[string]any
	_ = json.Unmarshal(body, &detail)
	c.Data["taskDetailContext"] = detail["context"]
	cj, _ := json.Marshal(detail["context"])
	c.check("1p GET /api/tasks/{id} renders context refs with manifest",
		code == 200 && strings.Contains(string(cj), sha1) && strings.Contains(string(cj), "PLC-042 billing spec"),
		"HTTP %d context=%s", code, clip(cj))

	showOut, showErr := grainCLI(srv.URL, operatorToken, "show", task.ID)
	c.Data["grainShow"] = showOut
	c.check("1p' grain show renders the pinned context refs and manifest",
		showErr == nil && strings.Contains(showOut, "acme/specs:plc/042@"+sha1[:12]) && strings.Contains(showOut, "PLC-042 billing spec") && strings.Contains(showOut, "[design] spec.md -- Billing spec <https://planner.example.com/PLC-042>"),
		"err=%v out=%s", showErr, clip([]byte(showOut)))

	// 1q. A pin whose commit vanished upstream (history rewritten) must refuse.
	vanished := strings.Repeat("ab", 20)
	code, body = api.do("POST", "/api/tasks", map[string]any{
		"title": "Pinned to missing commit", "description": "x", "repo": "acme/app",
		"designs": []string{"acme/specs:architecture.md@" + vanished},
	})
	var bad ui.Task
	_ = json.Unmarshal(body, &bad)
	c.Data["vanishedFiling"] = fmt.Sprintf("HTTP %d id=%s", code, bad.ID)
	if bad.ID != "" {
		if bt := taskByID(ctx, store, bad.ID); bt.Approval == nil {
			_ = client.Approve(ctx, bad.ID)
		}
		_ = client.Close(ctx, task.ID, ui.CloseOptions{})
		if child != nil {
			_ = client.Close(ctx, child.ID, ui.CloseOptions{})
		}
		for i := 0; i < 3; i++ {
			_ = orchestrator.RunCycle(ctx, deps, now.Add(time.Duration(10+i)*time.Minute))
		}
		badRuns, _ := store.Runs(ctx, bad.ID)
		var errText, outcome string
		if len(badRuns) > 0 {
			errText = badRuns[len(badRuns)-1].Detail
			outcome = string(badRuns[len(badRuns)-1].Outcome)
		}
		c.check("1q pin to a commit missing upstream fails setup instead of using another revision",
			len(badRuns) > 0 && errText != "", "runs=%d outcome=%s error=%s", len(badRuns), outcome, errText)
	}
	return c
}

// Items 2-4 live in walkthrough_items.go-style sections below.

func runItem2(ctx context.Context) *checks { return item2(ctx) }
func runItem3(ctx context.Context) *checks { return item3(ctx) }
func runItem4(ctx context.Context) *checks { return item4(ctx) }

// liveServer is a real ui.Server over a fresh SQLite store with the
// operator's access code, for items 2-4.
type liveServer struct {
	store  *model.Store
	db     *sql.DB
	client *ui.Client
	srv    *httptest.Server
	api    apiClient
	tmp    string
}

func newLiveServer(ctx context.Context, name string, mutate func(*ui.Config, *model.Store)) *liveServer {
	tmp, _ := os.MkdirTemp("", "walk-"+name+"-*")
	store, db := openStore(ctx, filepath.Join(tmp, "data"))
	cfg := ui.Config{
		Actor:         model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com"},
		Capabilities:  ui.OfferedCapabilities(),
		LocalAPIToken: operatorToken,
	}
	if mutate != nil {
		mutate(&cfg, store)
	}
	client := ui.NewClient(cfg, store)
	srv := httptest.NewServer(ui.NewServerWithClient(client))
	l := &liveServer{store: store, db: db, client: client, srv: srv, api: apiClient{base: srv.URL, token: operatorToken}, tmp: tmp}
	if code, body := l.api.do("PUT", "/api/settings", map[string]any{"githubHost": "github.com", "externalAgentApi": true}); code != 200 {
		panic(fmt.Sprintf("enabling external agent API: HTTP %d %s", code, body))
	}
	return l
}

func (l *liveServer) Close() { l.srv.Close(); l.db.Close(); os.RemoveAll(l.tmp) }

func (l *liveServer) token(c *checks, name, owner string, scopes, repos []string) apiClient {
	code, body := l.api.do("POST", "/api/external-agents/tokens", map[string]any{
		"name": name, "scopes": scopes, "repos": repos, "owner": owner,
	})
	var issued struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(body, &issued)
	if issued.Token == "" {
		c.check("mint token "+name, false, "HTTP %d %s", code, clip(body))
	}
	return apiClient{base: l.srv.URL, token: issued.Token}
}

func (l *liveServer) create(c *checks, as apiClient, label string, req map[string]any) ui.Task {
	code, body := as.do("POST", "/api/tasks", req)
	var t ui.Task
	_ = json.Unmarshal(body, &t)
	if t.ID == "" {
		c.check("file "+label, false, "HTTP %d %s", code, clip(body))
	}
	return t
}

func listIDs(body []byte) []string {
	var tasks []map[string]any
	if json.Unmarshal(body, &tasks) != nil {
		var wrapped struct {
			Tasks []map[string]any `json:"tasks"`
		}
		_ = json.Unmarshal(body, &wrapped)
		tasks = wrapped.Tasks
	}
	var ids []string
	for _, t := range tasks {
		if id, ok := t["id"].(string); ok {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
}

// ---------------------------------------------------------------------------
// Item 2: externalRef transitive inheritance & cross-repo isolation

func item2(ctx context.Context) *checks {
	c := newChecks("item2-external-ref")
	l := newLiveServer(ctx, "item2", nil)
	defer l.Close()
	core := model.RepoRef{Owner: "acme", Name: "core"}
	billing := model.RepoRef{Owner: "acme", Name: "billing"}
	for _, r := range []model.RepoRef{core, billing} {
		_ = l.store.PutRepoConfig(ctx, model.RepoConfig{Repo: r})
	}
	root := l.create(c, l.api, "root", map[string]any{
		"title": "Root PROJ-42 work", "description": "root", "repo": "acme/core", "externalRef": "PROJ-42", "approved": true,
	})
	// Child proposal in repo B filed from the root (origin task:<root>).
	child := l.create(c, l.api, "child", map[string]any{
		"title": "Billing follow-up", "description": "child", "repo": "acme/billing", "origin": "task:" + root.ID,
	})
	// Superseding task in repo B and a second-hop proposal off it: the
	// shapes orchestrator files (LinkSupersedes / LinkProposedBy).
	now := time.Now().UTC()
	super := model.Task{ID: "900", Intent: model.IntentImplement, Title: "Billing follow-up v2", Target: &billing,
		Links: []model.Link{{Kind: model.LinkSupersedes, Target: child.ID}}, CreatedAt: &now}
	hop := model.Task{ID: "901", Intent: model.IntentImplement, Title: "Billing two-hop proposal", Target: &billing,
		Links: []model.Link{{Kind: model.LinkProposedBy, Target: "900"}}, CreatedAt: &now}
	for _, t := range []model.Task{super, hop} {
		if err := l.store.PutTask(ctx, t); err != nil {
			panic(err)
		}
	}
	got := map[string]string{}
	for _, id := range []string{root.ID, child.ID, "900", "901"} {
		got[id] = taskByID(ctx, l.store, id).ExternalRef
	}
	c.Data["externalRefs"] = got
	c.check("2a child proposal (repo B) inherits PROJ-42 from root (repo A)", got[child.ID] == "PROJ-42", "%v", got)
	c.check("2b superseding task inherits PROJ-42", got["900"] == "PROJ-42", "%v", got)
	c.check("2c two-hop proposal inherits PROJ-42", got["901"] == "PROJ-42", "%v", got)

	// Operator view.
	code, body := l.api.do("GET", "/api/refs/PROJ-42/summary", nil)
	var opSum map[string]any
	_ = json.Unmarshal(body, &opSum)
	c.Data["operatorSummary"] = opSum
	c.check("2d operator summary counts all 4 tasks", code == 200 && fmt.Sprint(opSum["total"]) == "4",
		"HTTP %d %s", code, clip(body))
	code, body = l.api.do("GET", "/api/tasks?externalRef=PROJ-42", nil)
	opIDs := listIDs(body)
	c.check("2e operator grain list -externalRef sees all 4", code == 200 && len(opIDs) == 4, "HTTP %d ids=%v", code, opIDs)

	// Token scoped to repo A only.
	scoped := l.token(c, "core-reader", "", []string{"tasks:read", "tasks:write"}, []string{"acme/core"})
	code, body = scoped.do("GET", "/api/refs/PROJ-42/summary", nil)
	var scSum map[string]any
	_ = json.Unmarshal(body, &scSum)
	c.Data["scopedSummary"] = scSum
	c.check("2f scoped summary counts only repo A's task", code == 200 && fmt.Sprint(scSum["total"]) == "1",
		"HTTP %d %s", code, clip(body))
	c.check("2g scoped summary never names repo B", !strings.Contains(strings.ToLower(string(body)), "billing"),
		"%s", clip(body))
	code, body = scoped.do("GET", "/api/tasks?externalRef=PROJ-42", nil)
	scIDs := listIDs(body)
	c.check("2h scoped list -externalRef sees only the root", code == 200 && slices.Equal(scIDs, []string{root.ID}),
		"HTTP %d ids=%v body=%s", code, scIDs, clip(body))
	code, body = scoped.do("GET", "/api/refs/PROJ-42/summary?repo=acme/billing", nil)
	c.check("2i scoped summary narrowed to repo B is refused or empty",
		code == 403 || (code == 200 && !strings.Contains(string(body), `"total":1`) && strings.Contains(string(body), `"total":0`)),
		"HTTP %d %s", code, clip(body))
	code, body = scoped.do("GET", "/api/tasks/"+child.ID, nil)
	c.check("2j scoped token cannot read repo B's task", code == 403 || code == 404, "HTTP %d %s", code, clip(body))
	code, body = scoped.do("GET", "/api/tasks?externalRef=PROJ-42&repo=acme/billing", nil)
	c.check("2k scoped list narrowed to repo B is refused or empty", code == 403 || len(listIDs(body)) == 0,
		"HTTP %d %s", code, clip(body))

	// Inheritance guard: the scoped token files in repo A with origin=task:<repo B task>.
	probe := l.create(c, scoped, "probe", map[string]any{
		"title": "Probe", "description": "probe", "repo": "acme/core", "origin": "task:900",
	})
	if probe.ID != "" {
		ref := taskByID(ctx, l.store, probe.ID).ExternalRef
		c.check("2l scoped filer cannot inherit externalRef from an unreadable repo B task", ref == "",
			"probe externalRef=%q", ref)
	}
	// A different ref nobody can see: zero, not 404 (no existence oracle).
	code, body = scoped.do("GET", "/api/refs/UNKNOWN-1/summary", nil)
	c.Data["unknownRef"] = clip(body)
	c.check("2m unknown ref answers an empty summary", code == 200 && strings.Contains(string(body), `"total":0`),
		"HTTP %d %s", code, clip(body))
	return c
}

// ---------------------------------------------------------------------------
// Item 3: task owner & proposer

func item3(ctx context.Context) *checks {
	c := newChecks("item3-owner-proposer")
	l := newLiveServer(ctx, "item3", nil)
	defer l.Close()
	core := model.RepoRef{Owner: "acme", Name: "core"}
	_ = l.store.PutRepoConfig(ctx, model.RepoConfig{Repo: core})
	alice := l.token(c, "alice-cli", "alice@example.com", []string{"tasks:read", "tasks:write"}, []string{"acme/core"})
	bob := l.token(c, "bob-cli", "bob@example.com", []string{"tasks:read", "tasks:write"}, []string{"acme/core"})

	// Operator default owner.
	op := l.create(c, l.api, "operator task", map[string]any{"title": "Operator task", "description": "x", "repo": "acme/core"})
	opT := taskByID(ctx, l.store, op.ID)
	c.check("3a operator-filed task owner defaults to the operator", opT.Owner == "operator@example.com", "owner=%q", opT.Owner)

	// Alice files with her token.
	at := l.create(c, alice, "alice task", map[string]any{"title": "Alice task", "description": "x", "repo": "acme/core"})
	aT := taskByID(ctx, l.store, at.ID)
	c.Data["aliceTask"] = map[string]any{"id": aT.ID, "owner": aT.Owner, "creator": aT.Creator}
	c.check("3b token-filed task owner defaults to the token owner (Alice)", aT.Owner == "alice@example.com",
		"owner=%q creator=%q", aT.Owner, aT.Creator)

	// Alice's agent proposes a follow-up: a real dispatch through the scoped
	// proxy whose run calls propose_task.
	gs := startGit(ctx, l.tmp, l.store, []string{"acme/core"})
	defer gs.Close()
	initBare(gs.root, "acme", "core", map[string]string{"README.md": "core\n"})
	_ = l.client.Approve(ctx, aT.ID)
	sandboxRoot := filepath.Join(l.tmp, "sandboxes")
	_ = os.MkdirAll(sandboxRoot, 0o755)
	deps := orchestrator.Deps{
		Store: l.store, Sandboxes: orchestrator.NewHostSandboxes(sandboxRoot),
		Framework: orchestrator.StaticFramework(fnProvider(func(ctx context.Context, rc agent.RunConfig) (*agent.Result, error) {
			return &agent.Result{FinalText: "done", ToolCalls: []agent.ToolCall{
				{Name: "propose_task", Arguments: map[string]any{"title": "Alice agent follow-up", "body": "follow-up"}, Text: "ok"},
				{Name: "comment_on_issue", Arguments: map[string]any{"comment": "Done."}, Text: "ok"},
			}}, nil
		})),
		Config:           orchestrator.Config{GitRemoteBase: gs.proxy.URL},
		MintSandboxToken: gs.tokens.EnsureToken,
		MaxWorkers:       1,
	}
	_ = orchestrator.RunCycle(ctx, deps, time.Now().UTC())
	propID := ""
	all, _ := l.store.ListTasks(ctx)
	for _, t := range all {
		if t.Title == "Alice agent follow-up" {
			propID = t.ID
		}
	}
	if propID == "" {
		c.check("3c dispatch files Alice's agent proposal", false, "no proposal")
		return c
	}
	pT := taskByID(ctx, l.store, propID)
	code, body := alice.do("GET", "/api/tasks/"+propID, nil)
	var propView map[string]any
	_ = json.Unmarshal(body, &propView)
	proposedLabel := ""
	if trs, ok := propView["transitions"].([]any); ok {
		for _, tr := range trs {
			m, _ := tr.(map[string]any)
			if m["state"] == "proposed" {
				proposedLabel = fmt.Sprint(m["actorLabel"])
			}
		}
	}
	c.Data["proposal"] = map[string]any{"id": propID, "owner": pT.Owner, "proposer": pT.Proposer, "timelineProposedBy": proposedLabel}
	c.check("3c agent proposal inherits owner=Alice", pT.Owner == "alice@example.com", "owner=%q", pT.Owner)
	c.check("3c' proposer attribution names the proposing task", pT.Proposer.TaskID == aT.ID && pT.Proposer.RunID != "",
		"proposer=%+v", pT.Proposer)
	c.check("3c'' timeline 'Proposed by' names task #"+aT.ID+" on behalf of Alice",
		code == 200 && strings.Contains(proposedLabel, "task #"+aT.ID) && strings.Contains(proposedLabel, "alice@example.com"),
		"HTTP %d label=%q", code, proposedLabel)
	prop := pT

	// owner:me as Alice and Bob before the transfer.
	_, body = alice.do("GET", "/api/tasks?owner=me", nil)
	aliceMine := listIDs(body)
	_, body = bob.do("GET", "/api/tasks?owner=me", nil)
	bobMine := listIDs(body)
	c.check("3d owner:me as Alice lists Alice's tasks", slices.Contains(aliceMine, aT.ID) && slices.Contains(aliceMine, prop.ID) && !slices.Contains(aliceMine, op.ID),
		"alice=%v", aliceMine)
	c.check("3e owner:me as Bob lists nothing yet", len(bobMine) == 0, "bob=%v", bobMine)

	// Transfer via the UI's PATCH (the Task detail owner editor).
	code, body = l.api.do("PATCH", "/api/tasks/"+aT.ID, map[string]any{"owner": "bob@example.com"})
	c.check("3f UI owner transfer Alice -> Bob", code == 200 && taskByID(ctx, l.store, aT.ID).Owner == "bob@example.com",
		"HTTP %d %s", code, clip(body))
	_, body = alice.do("GET", "/api/tasks?owner=me", nil)
	aliceMine = listIDs(body)
	_, body = bob.do("GET", "/api/tasks?owner=me", nil)
	bobMine = listIDs(body)
	c.check("3g owner:me follows the transfer", slices.Contains(bobMine, aT.ID) && !slices.Contains(aliceMine, aT.ID),
		"alice=%v bob=%v", aliceMine, bobMine)
	_, body = l.api.do("GET", "/api/tasks?owner=bob@example.com", nil)
	c.check("3h owner:<email> filter", slices.Equal(listIDs(body), []string{aT.ID}), "ids=%v", listIDs(body))

	// Audit / timeline entry.
	code, body = l.api.do("GET", "/api/tasks/"+aT.ID+"/timeline", nil)
	if code == 404 {
		code, body = l.api.do("GET", "/api/tasks/"+aT.ID, nil)
	}
	c.Data["timelineSnippet"] = clip(body)
	c.check("3i ownership transfer recorded on the task's history",
		strings.Contains(string(body), "bob@example.com") && strings.Contains(strings.ToLower(string(body)), "owner"),
		"HTTP %d", code)

	// grain update -owner and grain list -owner me through the real CLI.
	out, err := grainCLI(l.srv.URL, operatorToken, "update", aT.ID, "-owner", "carol@example.com")
	c.check("3j grain update -owner transfers Bob -> Carol", err == nil && taskByID(ctx, l.store, aT.ID).Owner == "carol@example.com",
		"err=%v out=%s", err, clip([]byte(out)))
	out, err = grainCLI(l.srv.URL, alice.token, "list", "-owner", "me")
	c.Data["cliAliceOwnerMe"] = out
	c.check("3k grain list -owner me as Alice lists her proposal only",
		err == nil && strings.Contains(out, "Alice agent follow-up") && !strings.Contains(out, "Alice task"),
		"err=%v out=%s", err, clip([]byte(out)))
	out, err = grainCLI(l.srv.URL, bob.token, "list", "-owner", "me")
	c.Data["cliBobOwnerMe"] = out
	c.check("3l grain list -owner me as Bob is empty after the second transfer",
		err == nil && !strings.Contains(out, "Alice task") && !strings.Contains(out, "Alice agent follow-up"),
		"err=%v out=%s", err, clip([]byte(out)))
	out, err = grainCLI(l.srv.URL, operatorToken, "show", aT.ID)
	c.Data["cliShowOwner"] = out
	c.check("3m grain show names the owner", err == nil && strings.Contains(out, "carol@example.com"), "err=%v out=%s", err, clip([]byte(out)))
	var transfers []string
	_, body = l.api.do("GET", "/api/tasks/"+aT.ID, nil)
	var detail map[string]any
	_ = json.Unmarshal(body, &detail)
	if trs, ok := detail["transitions"].([]any); ok {
		for _, tr := range trs {
			m, _ := tr.(map[string]any)
			if strings.Contains(strings.ToLower(fmt.Sprint(m["state"])), "owner") {
				transfers = append(transfers, fmt.Sprintf("%v by %v: %v", m["state"], m["actorLabel"], m["reason"]))
			}
		}
	}
	c.Data["ownerTransitions"] = transfers
	c.check("3n both transfers audited as owner transitions", len(transfers) >= 2, "%v", transfers)

	// Bob can't be set by an agent; Alice (token, not an agent) can hand it back.
	code, body = alice.do("PATCH", "/api/tasks/"+aT.ID, map[string]any{"owner": "alice@example.com"})
	c.Data["aliceTakesBack"] = fmt.Sprintf("HTTP %d %s", code, clip(body))
	return c
}

// ---------------------------------------------------------------------------
// Item 4: PR adoption (review-only vs finish) + request_human_review

func pushBranch(bare, branch string, files map[string]string, msg string) string {
	tmp, _ := os.MkdirTemp("", "walk-push-*")
	defer os.RemoveAll(tmp)
	work := filepath.Join(tmp, "w")
	gitCmd(tmp, "clone", "-q", "-b", "main", bare, work)
	gitCmd(work, "checkout", "-q", "-b", branch)
	writeFiles(work, files)
	gitCmd(work, "add", ".")
	gitCmd(work, "commit", "-q", "-m", msg)
	gitCmd(work, "push", "-q", "-f", "origin", branch)
	return gitCmd(work, "rev-parse", "HEAD")
}

func markMergeable(sim *githubsim.Sim) {
	t := true
	for i := range sim.PullRequests {
		if sim.PullRequests[i].State == "open" {
			sim.PullRequests[i].Mergeable = &t
		}
	}
}

func item4(ctx context.Context) *checks {
	c := newChecks("item4-pr-adoption-human-review")
	gitRoot, _ := os.MkdirTemp("", "walk-item4-git-*")
	defer os.RemoveAll(gitRoot)
	bare, _, mainSHA := initBare(gitRoot, "acme", "widgets", map[string]string{"README.md": "widgets\n"})
	sim := githubsim.New("acme", "widgets", bare, "main")
	ghClient := github.NewClient(sim, nil)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	// External contributor PR #41.
	contribSHA := pushBranch(bare, "contrib/faster-parser", map[string]string{"parser.go": "package widgets\n"}, "faster parser")
	yes := true
	sim.PullRequests = append(sim.PullRequests, githubsim.PullRequest{
		Number: 41, Title: "Faster parser", Body: "Speeds up parsing.", Head: "contrib/faster-parser", Base: "main",
		HTMLURL: "https://github.com/acme/widgets/pull/41", State: "open", Mergeable: &yes, CreatedAt: time.Now().UTC(),
	})
	success := "success"
	sim.CheckRuns[contribSHA] = []github.CheckRun{{ID: 1, Name: "Tests", Status: "completed", Conclusion: &success}}

	l := newLiveServer(ctx, "item4", func(cfg *ui.Config, store *model.Store) {
		cfg.PullRequests = &daemon.PullRequestOpener{Store: store, Client: ghClient}
	})
	defer l.Close()
	_ = l.store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo})
	deps := orchestrator.Deps{Store: l.store, Client: ghClient}
	now := time.Now().UTC()

	// 4a. grain pr review (review-only filing) through the real CLI.
	out, err := grainCLI(l.srv.URL, operatorToken, "pr", "review", "acme/widgets#41")
	c.Data["cliPrReview"] = out
	c.check("4a grain pr review files a review of the external PR", err == nil, "err=%v out=%s", err, clip([]byte(out)))
	out, err = grainCLI(l.srv.URL, operatorToken, "pr", "adopt", "acme/widgets#41")
	c.Data["cliPrAdoptReviewOnly"] = out
	c.check("4b grain pr adopt (default review-only) files a shepherd", err == nil && strings.Contains(strings.ToLower(out), "review-only"),
		"err=%v out=%s", err, clip([]byte(out)))
	for i := 0; i < 3; i++ {
		markMergeable(sim)
		_ = orchestrator.RunCycle(ctx, deps, now.Add(time.Duration(i)*time.Minute))
	}
	headAfter := gitCmd(bare, "rev-parse", "refs/heads/contrib/faster-parser")
	mainAfter := gitCmd(bare, "rev-parse", "refs/heads/main")
	pr := sim.PullRequests[0]
	c.check("4c review-only: no pushes to the PR branch, no merge, main untouched",
		headAfter == contribSHA && mainAfter == mainSHA && !pr.Merged && pr.State == "open",
		"head=%s (was %s) main=%s (was %s) merged=%v state=%s", headAfter[:12], contribSHA[:12], mainAfter[:12], mainSHA[:12], pr.Merged, pr.State)
	out, err = grainCLI(l.srv.URL, operatorToken, "pr", "status", "acme/widgets#41")
	c.Data["cliPrStatus"] = out
	c.check("4d grain pr status reports the shepherd and review-only mode",
		err == nil && strings.Contains(strings.ToLower(out), "review-only"), "err=%v out=%s", err, clip([]byte(out)))

	// 4e. Upgrade gate: a tasks:write token (no tasks:admin) cannot upgrade an approved shepherd.
	writer := l.token(c, "widgets-writer", "dana@example.com", []string{"tasks:read", "tasks:write", "repos:read", "merge:write"}, []string{"acme/widgets"})
	code, body := writer.do("POST", "/api/pull-requests/adopt", map[string]any{"pr": "acme/widgets#41", "mode": "finish"})
	c.Data["writerUpgrade"] = fmt.Sprintf("HTTP %d %s", code, clip(body))
	c.check("4e upgrade to finish refused for a tasks:write token", code == 403, "HTTP %d %s", code, clip(body))
	out, err = grainCLI(l.srv.URL, operatorToken, "pr", "adopt", "-mode", "finish", "acme/widgets#41")
	if err != nil && strings.Contains(out, "flag provided but not defined") {
		out, err = grainCLI(l.srv.URL, operatorToken, "pr", "adopt", "--mode=finish", "acme/widgets#41")
	}
	c.Data["cliPrAdoptFinish"] = out
	c.check("4f operator upgrades review-only -> finish with grain pr adopt", err == nil && strings.Contains(strings.ToLower(out), "finish"),
		"err=%v out=%s", err, clip([]byte(out)))

	// 4g. request_human_review on an auto_merge: true task: a real run
	// pushes through the scoped proxy and grain opens its PR; CI is still
	// running when the review asks for a person, then turns green.
	// Part one is done: close the adoption's tasks so only #500 dispatches.
	if all, err := l.store.ListTasks(ctx); err == nil {
		for _, t := range all {
			_ = l.client.Close(ctx, t.ID, ui.CloseOptions{})
		}
	}
	gs := startGit(ctx, l.tmp, l.store, []string{"acme/widgets"})
	defer gs.Close()
	_ = os.MkdirAll(filepath.Join(gs.root, "acme"), 0o755)
	gitCmd(gitRoot, "clone", "-q", "--bare", bare, filepath.Join(gs.root, "acme", "widgets.git"))
	bare = filepath.Join(gs.root, "acme", "widgets.git")
	gitCmd(bare, "config", "http.receivepack", "true")
	sim.BareRepo = bare
	heldUI := l.create(c, l.api, "held task", map[string]any{
		"title": "feat: add widget cache", "description": "cache", "repo": "acme/widgets", "autoMerge": true, "approved": true,
	})
	held := taskByID(ctx, l.store, heldUI.ID)
	if held.Approval == nil {
		_ = l.client.Approve(ctx, held.ID)
	}
	var sha string
	sandboxRoot := filepath.Join(l.tmp, "sandboxes")
	_ = os.MkdirAll(sandboxRoot, 0o755)
	runDeps := orchestrator.Deps{
		Store: l.store, Client: ghClient, Sandboxes: orchestrator.NewHostSandboxes(sandboxRoot),
		Framework: orchestrator.StaticFramework(fnProvider(func(ctx context.Context, rc agent.RunConfig) (*agent.Result, error) {
			work := filepath.Join(rc.SandboxRoot, "work")
			_ = os.WriteFile(filepath.Join(work, "cache.go"), []byte("package widgets\n"), 0o644)
			gitCmd(work, "add", "cache.go")
			gitCmd(work, "commit", "-q", "-m", "feat: add widget cache\n\nAdds the cache.")
			cmd := exec.Command("git", "push", "-q", "origin", "HEAD:refs/heads/"+rc.Branch)
			cmd.Dir = work
			cmd.Env = append(gitEnv(), "HOME="+rc.SandboxRoot)
			if out, err := cmd.CombinedOutput(); err != nil {
				return nil, fmt.Errorf("push: %v %s", err, out)
			}
			sha = gitCmd(work, "rev-parse", "HEAD")
			sim.CheckRuns[sha] = []github.CheckRun{{ID: 2, Name: "Tests", Status: "in_progress"}}
			return &agent.Result{FinalText: "done", ToolCalls: []agent.ToolCall{
				{Name: "comment_on_issue", Arguments: map[string]any{"comment": "Added the cache."}, Text: "ok"}}}, nil
		})),
		Config:           orchestrator.Config{GitRemoteBase: gs.proxy.URL},
		MintSandboxToken: gs.tokens.EnsureToken,
		MaxWorkers:       1,
	}
	deps = runDeps
	for i := 0; i < 3; i++ {
		markMergeable(sim)
		_ = orchestrator.RunCycle(ctx, deps, now.Add(time.Duration(5+i)*time.Minute))
	}
	pr500 := model.TaskPullRequest(taskByID(ctx, l.store, held.ID))
	if pr500 == nil {
		runs, _ := l.store.Runs(ctx, held.ID)
		c.check("4g run opens #500's PR", false, "no PR; links=%v runs=%+v", taskByID(ctx, l.store, held.ID).Links, runs)
		return c
	}
	st := struct{ PullRequest struct{ Number int } }{}
	st.PullRequest.Number = pr500.Number
	c.Data["heldTaskLinksBeforeHold"] = fmt.Sprint(taskByID(ctx, l.store, held.ID).Links)
	prNum := st.PullRequest.Number
	review := model.Task{ID: "9501", Intent: model.IntentReview, Title: "Security review of #" + held.ID, Target: &repo, CreatedAt: &now,
		Origin: model.Origin{Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}}, Reason: model.ReasonReview},
		Links:  []model.Link{{Kind: model.LinkProposedBy, Target: held.ID}}}
	if err := l.store.PutTask(ctx, review); err != nil {
		panic(err)
	}
	code, body = l.api.do("POST", "/api/tasks/"+review.ID+"/human-review", map[string]any{
		"category": "security", "reason": "Adds an unauthenticated cache endpoint", "details": "Needs a person to confirm the exposure is intended.",
	})
	c.check("4g review run's request_human_review raises a hold on #500", code == 200, "HTTP %d %s", code, clip(body))
	// The review run then finishes, so nothing but the hold stands between #500 and its merge.
	if err := l.client.Close(ctx, review.ID, ui.CloseOptions{}); err != nil {
		c.check("4g' review run finishes", false, "%v", err)
	}
	for i := 0; i < 3; i++ {
		markMergeable(sim)
		_ = orchestrator.RunCycle(ctx, deps, now.Add(time.Duration(10+i)*time.Minute))
	}
	var prState githubsim.PullRequest
	for _, p := range sim.PullRequests {
		if p.Number == prNum {
			prState = p
		}
	}
	sim.CheckRuns[sha] = []github.CheckRun{{ID: 2, Name: "Tests", Status: "completed", Conclusion: &success}}
	for i := 0; i < 3; i++ {
		markMergeable(sim)
		_ = orchestrator.RunCycle(ctx, deps, now.Add(time.Duration(13+i)*time.Minute))
	}
	for _, p := range sim.PullRequests {
		if p.Number == prNum {
			prState = p
		}
	}
	c.check("4h green checks + auto_merge still held for a person", !prState.Merged && prState.State == "open",
		"PR #%d merged=%v state=%s", prNum, prState.Merged, prState.State)
	code, body = l.api.do("GET", "/api/tasks/"+held.ID, nil)
	var detail map[string]any
	_ = json.Unmarshal(body, &detail)
	hj, _ := json.Marshal(detail["humanReviewHold"])
	c.Data["heldTaskState"] = detail["state"]
	c.Data["humanReviewHold"] = detail["humanReviewHold"]
	c.check("4i Task detail payload carries humanReviewHold with the category and reason",
		strings.Contains(string(hj), "security") && strings.Contains(string(hj), "unauthenticated cache"), "state=%v hold=%s", detail["state"], hj)
	_, body = l.api.do("GET", "/api/tasks", nil)
	c.check("4j task list payload carries the hold (humanReview filter / list badge)",
		strings.Contains(string(body), `"humanReviewHold"`), "list mentions humanReviewHold=%v", strings.Contains(string(body), `"humanReviewHold"`))
	out, _ = grainCLI(l.srv.URL, operatorToken, "show", held.ID)
	c.Data["cliShowHeld"] = out
	c.check("4k grain show names the hold", strings.Contains(strings.ToLower(out), "human review") || strings.Contains(strings.ToLower(out), "held"),
		"out=%s", clip([]byte(out)))
	_, body = l.api.do("GET", "/api/inbox", nil)
	inboxHeld := strings.Contains(string(body), `"kind":"merge-held","task":"`+held.ID+`"`)
	c.Data["inbox"] = json.RawMessage(body)
	c.check("4l Inbox lists the held task for a person", inboxHeld, "inbox=%s", clip(body))

	// 4m. An agent token may not release it; a person can.
	code, body = writer.do("POST", "/api/tasks/"+held.ID+"/human-review/release", nil)
	c.check("4m external agent token cannot release the hold", code == 403, "HTTP %d %s", code, clip(body))
	code, body = l.api.do("POST", "/api/tasks/"+held.ID+"/human-review/release", nil)
	c.check("4n operator releases the hold", code == 200, "HTTP %d %s", code, clip(body))
	for i := 0; i < 3; i++ {
		markMergeable(sim)
		_ = orchestrator.RunCycle(ctx, deps, now.Add(time.Duration(20+i)*time.Minute))
	}
	for _, p := range sim.PullRequests {
		if p.Number == prNum {
			prState = p
		}
	}
	c.check("4o released hold lets auto-merge land", prState.Merged, "PR #%d merged=%v state=%s", prNum, prState.Merged, prState.State)
	return c
}
