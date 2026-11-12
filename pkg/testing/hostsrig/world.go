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

// Package hostsrig exercises multi-host git lifecycle scenarios -- repository
// registration and settings, cross-host task filing, dispatch, clone and push
// through the git proxy, proxy-side credential isolation, state-repository
// export/import, lapsed host logins, and mid-task connection removal --
// against either gitproxytest's in-process command-backed forge fake or a
// caller-supplied command-backed ambient source and scratch repository.
package hostsrig

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

var baseTime = time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

// probeSandbox is the read-only sandbox identity used by lsRemoteTip to read
// branch tips through the proxy when a host has no local bare repository.
const probeSandbox = "hostsrig-probe"

// auditRecorder wraps a gitproxy.AuditLog so assertions can inspect the exact
// host and credential the proxy chose for every request since the last drain.
type auditRecorder struct {
	next    gitproxy.AuditLog
	mu      sync.Mutex
	entries []gitproxy.AuditEntry
}

func (a *auditRecorder) Record(entry gitproxy.AuditEntry) {
	if a.next != nil {
		a.next.Record(entry)
	}
	a.mu.Lock()
	a.entries = append(a.entries, entry)
	a.mu.Unlock()
}

func (a *auditRecorder) drain() []gitproxy.AuditEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := a.entries
	a.entries = nil
	return out
}

// world is the subset of tests/e2e's harness that the multi-host lifecycle
// scenarios need: an embedded SQLite store, a real gitproxy in front of local
// and remote upstreams, per-sandbox token minting, and scripted antigravity
// runs.
type world struct {
	t           testing.TB
	store       *model.Store
	db          *sql.DB
	ctx         context.Context
	upstreamDir string
	proxyURL    string
	tokens      *gitproxy.SandboxTokenStore
	credentials *gitproxy.CredentialSet
	readScopes  *gitproxy.ReadScopes
	audit       *auditRecorder

	mu    sync.Mutex
	roots map[string]string
}

func newWorld(t testing.TB, namedSecrets map[string]string, wrapUpstream func(http.Handler) http.Handler) *world {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}

	upstreamDir := t.TempDir()
	dataDir := t.TempDir()

	var upstream http.Handler = GitHTTPBackend(upstreamDir)
	for name, token := range namedSecrets {
		mustWriteFile(t, filepath.Join(dataDir, "secrets", "github", name+".token"), token)
	}
	if wrapUpstream != nil {
		upstream = wrapUpstream(upstream)
	}
	backendHost := MustHost(t, NewTestServer(t, upstream))

	mustWriteFile(t, filepath.Join(dataDir, "secrets", "github", "credentials.json"), `{"*": "anonymous"}`)
	if err := store.UpdateConfig(ctx, func(tm *model.Config) error {
		tm.GitCredential = "anonymous"
		return nil
	}); err != nil {
		t.Fatalf("giving the default team its credential: %v", err)
	}

	readScopes := gitproxy.NewReadScopes()
	proxy, err := gitproxy.BuildProxy(gitproxy.BuildConfig{
		DataDir:     dataDir,
		Store:       store,
		ForwardHost: backendHost,
		ForwardTLS:  false,
		ReadScopes:  readScopes,
	})
	if err != nil {
		t.Fatal(err)
	}
	audit := &auditRecorder{next: proxy.Audit}
	proxy.Audit = audit
	proxyURL := NewTestServer(t, gitproxy.NewHandler(proxy))

	return &world{
		t:           t,
		store:       store,
		db:          db,
		ctx:         ctx,
		upstreamDir: upstreamDir,
		proxyURL:    proxyURL,
		tokens:      gitproxy.NewSandboxTokenStore(filepath.Join(dataDir, "secrets", "sandbox-tokens.json")),
		credentials: proxy.Credentials,
		readScopes:  readScopes,
		audit:       audit,
		roots:       map[string]string{},
	}
}

func (w *world) sandboxRoot(sandbox string) string {
	w.t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	if root, ok := w.roots[sandbox]; ok {
		return root
	}
	token, err := w.tokens.EnsureToken(sandbox)
	if err != nil {
		w.t.Fatalf("minting a token for %s: %v", sandbox, err)
	}
	root := w.t.TempDir()
	if err := mcp.ConfigureGitCredentials(root, w.proxyURL+"/placeholder/placeholder.git", token, mcp.GitIdentity{}); err != nil {
		w.t.Fatalf("configuring git credentials for %s: %v", sandbox, err)
	}
	w.roots[sandbox] = root
	return root
}

func (w *world) prepareSandbox(d dispatch.Dispatch) string {
	w.t.Helper()
	root := w.sandboxRoot(d.RunID)
	task, err := w.store.GetTask(w.ctx, d.TaskID)
	if err != nil {
		w.t.Fatalf("reading task %s to record its run's target: %v", d.TaskID, err)
	}
	if task == nil {
		w.t.Fatalf("dispatch %s names task %s, which is not in the store", d.RunID, d.TaskID)
	}
	if err := w.store.SetRunSandbox(w.ctx, d.RunID, d.RunID, task.Target); err != nil {
		w.t.Fatalf("recording run %s's sandbox: %v", d.RunID, err)
	}
	return root
}

func (w *world) remote(owner, name string) string {
	return w.proxyURL + "/" + owner + "/" + name + ".git"
}

func (w *world) runDispatch(d dispatch.Dispatch, script []antigravity.Step, at time.Time) *agent.Result {
	w.t.Helper()
	root := w.prepareSandbox(d)
	fw := antigravity.NewForTest(antigravity.Steps(script...))
	result, err := fw.Run(w.ctx, agent.RunConfig{Prompt: "work the task", SandboxRoot: root})
	if err != nil {
		w.t.Fatalf("agent run for %s failed outright: %v", d.RunID, err)
	}
	outcome := "succeeded"
	for _, c := range result.ToolCalls {
		if c.IsError {
			outcome = "failed"
		}
	}
	if err := w.store.FinishRun(w.ctx, d.RunID, at, outcome, ""); err != nil {
		w.t.Fatalf("FinishRun(%s): %v", d.RunID, err)
	}
	return result
}

// workHeadSHA returns HEAD's commit SHA inside runID's cloned work directory.
func (w *world) workHeadSHA(runID string) string {
	w.t.Helper()
	root := w.sandboxRoot(runID)
	return strings.TrimSpace(runOutput(w.t, filepath.Join(root, "work"), "git", "rev-parse", "HEAD"))
}

// lsRemoteSHA reads refs/heads/branch on repo through the git proxy via `git
// ls-remote`, returning "" when the branch does not exist on the remote.
func (w *world) lsRemoteSHA(remote string, repo model.RepoRef, branch string) string {
	w.t.Helper()
	probeRoot := w.sandboxRoot(probeSandbox)
	w.readScopes.Grant(probeSandbox, repo)
	defer w.readScopes.Drop(probeSandbox)

	ref := "refs/heads/" + branch
	cmd := exec.Command("git", "ls-remote", remote, ref)
	cmd.Dir = probeRoot
	env := make([]string, 0, len(os.Environ())+2)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "HOME=") && !strings.HasPrefix(kv, "GIT_TERMINAL_PROMPT=") {
			env = append(env, kv)
		}
	}
	cmd.Env = append(env, "HOME="+probeRoot, "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		w.t.Fatalf("git ls-remote %s %s: %v\n%s", remote, ref, err, out)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == ref {
			return fields[0]
		}
	}
	return ""
}

func pushedOK(result *agent.Result) bool {
	sawRunCommand := false
	for _, c := range result.ToolCalls {
		if c.IsError {
			return false
		}
		if c.Name == "run_command" {
			sawRunCommand = true
		}
	}
	return sawRunCommand
}

func pushScript(remote, branch, taskID string) []antigravity.Step {
	cmd := "git clone " + remote + " work && cd work && " +
		"git checkout -b " + branch + " && " +
		"echo 'change for " + taskID + "' >> NOTES.md && " +
		"git add NOTES.md && git commit -q -m 'agent commit for " + taskID + "' && " +
		"git push origin " + branch
	return []antigravity.Step{
		antigravity.ToolStep("run_command", map[string]any{"command": cmd}),
		antigravity.TextStep("pushed " + branch),
	}
}

func commandOutput(result *agent.Result) string {
	var b strings.Builder
	for _, c := range result.ToolCalls {
		b.WriteString(c.Text)
		b.WriteString("\n")
	}
	return b.String()
}

// GitHTTPBackend serves projectRoot over git's smart-HTTP CGI contract by
// shelling out to `git http-backend` per request.
func GitHTTPBackend(projectRoot string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		cmd := exec.Command("git", "http-backend")
		cmd.Env = append(os.Environ(),
			"GIT_PROJECT_ROOT="+projectRoot,
			"GIT_HTTP_EXPORT_ALL=1",
			"PATH_INFO="+r.URL.Path,
			"QUERY_STRING="+r.URL.RawQuery,
			"REQUEST_METHOD="+r.Method,
			"CONTENT_TYPE="+r.Header.Get("Content-Type"),
			"HTTP_CONTENT_ENCODING="+r.Header.Get("Content-Encoding"),
			"GATEWAY_INTERFACE=CGI/1.1",
			"SERVER_PROTOCOL=HTTP/1.1",
		)
		cmd.Stdin = bytes.NewReader(body)
		out, err := cmd.Output()
		if err != nil {
			http.Error(w, fmt.Sprintf("git http-backend: %v", err), http.StatusInternalServerError)
			return
		}

		sep := []byte("\r\n\r\n")
		headEnd := bytes.Index(out, sep)
		if headEnd == -1 {
			sep = []byte("\n\n")
			headEnd = bytes.Index(out, sep)
		}
		headerBlock := string(out[:headEnd])
		respBody := out[headEnd+len(sep):]

		status := http.StatusOK
		for _, line := range strings.Split(headerBlock, "\n") {
			line = strings.TrimRight(line, "\r")
			if line == "" {
				continue
			}
			key, value, _ := strings.Cut(line, ":")
			value = strings.TrimSpace(value)
			if strings.EqualFold(key, "Status") {
				if code, err := strconv.Atoi(strings.Fields(value)[0]); err == nil {
					status = code
				}
				continue
			}
			w.Header().Add(key, value)
		}
		w.WriteHeader(status)
		_, _ = w.Write(respBody)
	}
}

// NewTestServer starts h on an httptest.Server, registers t.Cleanup(s.Close),
// and returns its URL.
func NewTestServer(t testing.TB, h http.Handler) string {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s.URL
}

// MustHost strips the "http://" prefix from an httptest.Server URL.
func MustHost(t testing.TB, rawURL string) string {
	t.Helper()
	host, ok := strings.CutPrefix(rawURL, "http://")
	if !ok {
		t.Fatalf("expected an http:// URL from httptest.NewServer, got %q", rawURL)
	}
	return host
}

func initBareRepo(t testing.TB, path, defaultBranch string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	runCmd(t, filepath.Dir(path), "git", "init", "--bare", "--initial-branch="+defaultBranch, path)
	return path
}

func runCmd(t testing.TB, dir, name string, args ...string) {
	t.Helper()
	_ = runOutput(t, dir, name, args...)
}

func runOutput(t testing.TB, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}

func mustWriteFile(t testing.TB, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
