package gitproxy_test

// The whole pipeline this package exists to prove, end to end: a real bare
// git repo, served over real smart-HTTP by `git http-backend` (standing in
// for GitHub -- there is no live GitHub credential in this environment,
// matching v1's own live-test rig in tests/test_vm_integration.py), behind
// a real GitProxy whose Authorizer reads a real embedded SQLite-backed
// model.Store, reached through a real HTTP server, from a real `git`
// process running with credentials NewSandboxTools' local stand-in
// (mcp) configured via ConfigureGitCredentials, driven by a scripted
// (not live-CLI) antigravity.Framework.Run.
//
// What each layer contributes to the proof:
//   - model:    a task's Target and Reads are the only source of truth
//     for what the sandbox may touch -- no allowlist file exists anywhere
//     in this test.
//   - gitproxy: authenticates the sandbox token, asks the model, forwards
//     through to the local git http-backend with the right credential.
//   - mcp/antigravity: an agent turn calling run_command with an ordinary
//     `git clone`/`git push` succeeds without ever seeing the proxy
//     token, because ConfigureGitCredentials already wrote it where
//     git's own credential.helper picks it up.

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

// gitHTTPBackend serves projectRoot over the smart-HTTP CGI contract git
// itself defines, by shelling out to `git http-backend` per request --
// the same technique tests/test_vm_integration.py's _GitBackendHandler
// uses to stand in for GitHub without a live credential.
func gitHTTPBackend(projectRoot string) http.HandlerFunc {
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
		w.Write(respBody)
	}
}

// initBareRepo creates the bare repository at path -- the upstream every
// test here pushes to -- on main, with its parent directories, and
// returns the path.
//
// The two config settings are what keep a push from outliving the test
// that made it: git runs auto-maintenance on the receiving side of a
// push, and since v2.47 it detaches, so the repacking goes on in the
// background while t.TempDir deletes the tree underneath it, and the
// deletion fails on files that land after RemoveAll has walked past them
// ("TempDir RemoveAll cleanup: unlinkat ...: directory not empty").
// Saying autoDetach=false does not turn the maintenance off, it runs it
// in the foreground, so the push that triggered it is what waits for it.
// pkg/cli/bare_remote_test.go's initBareRepo has the whole
// explanation, including why both keys are set and why nothing on a
// runner with git 2.39 can see any of this; this is that helper
// restated, because this codebase duplicates a small test helper across
// packages rather than sharing one (tests/e2e/harness_test.go's own
// comment on why).
func initBareRepo(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("creating the bare repository's parent directory: %v", err)
	}
	run(t, filepath.Dir(path), "git", "init", "--bare", "-b", "main", path)
	run(t, path, "git", "config", "maintenance.autoDetach", "false")
	run(t, path, "git", "config", "gc.autoDetach", "false")
	return path
}

// newBareRepoWithOneCommit creates a bare repo at dir/owner/name.git
// seeded with one commit on its default branch -- nested under owner/
// because GitProxy always forwards /owner/repo.git/..., matching where a
// real request for owner/repo lands on github.com; this stand-in needs
// the same shape on disk for git http-backend to resolve it.
func newBareRepoWithOneCommit(t *testing.T, dir, owner, name string) string {
	t.Helper()
	bare := initBareRepo(t, filepath.Join(dir, owner, name+".git"))
	// GIT_HTTP_EXPORT_ALL only covers the read side; git http-backend
	// denies git-receive-pack (push) by default even with it set --
	// found live building v1's own equivalent rig.
	run(t, dir, "git", "-C", bare, "config", "http.receivepack", "true")

	seed := filepath.Join(dir, name+"-seed")
	run(t, dir, "git", "clone", bare, seed)
	run(t, seed, "git", "config", "user.email", "seed@example.com")
	run(t, seed, "git", "config", "user.name", "seed")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, seed, "git", "add", "README.md")
	run(t, seed, "git", "commit", "-m", "initial commit")
	run(t, seed, "git", "push", "origin", "main")
	return bare
}

func run(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
}

func openStore(t *testing.T) (*model.Store, context.Context) {
	t.Helper()
	// The live tests' own claim on the log -- see log_test.go. First,
	// because the release it registers has to run after the cleanup
	// below and anything the caller registers after us.
	gitproxy.LogToTest(t)
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
	return store, ctx
}

// giveTeamCredential gives team the GitHub credential name: a team's work
// clones and pushes with its own credential and never the deployment's
// ladder (grain/task-1592), so a live test standing in for a team's
// sandbox names the credential the way a team admin would.
func giveTeamCredential(t *testing.T, store *model.Store, ctx context.Context, team, name string) {
	t.Helper()
	if err := store.UpdateConfig(ctx, func(tm *model.Config) error {
		tm.GitCredential = name
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestLiveCloneAndPushThroughTheWholeStack proves a sandbox scoped by the
// model to owner/widgets can clone and push it through the proxy, end to
// end, with no allowlist file and no real GitHub involved.
func TestLiveCloneAndPushThroughTheWholeStack(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	// --- the stand-in "GitHub": a bare repo served by git http-backend
	upstreamDir := t.TempDir()
	newBareRepoWithOneCommit(t, upstreamDir, "owner", "widgets")
	backend := httptest.NewServer(gitHTTPBackend(upstreamDir))
	defer backend.Close()
	backendHost := mustHost(t, backend.URL)

	// --- the model: a task targeting owner/widgets, dispatched to
	// sandbox-0 -- this, and nothing else, is what makes the clone/push
	// below legal.
	store, ctx := openStore(t)
	task := model.Task{
		ID: "t1", Intent: model.IntentImplement, Title: "test task",
		Origin: model.Origin{
			Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "tester"}},
			Reason:      model.ReasonDirect,
		},
		Target:  &model.RepoRef{Owner: "owner", Name: "widgets"},
		Binding: model.BindingDirective,
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: "t1-1", TaskID: "t1", Sandbox: "sandbox-0",
		Attempt: 1, StartedAt: time.Now(),
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}

	// --- the proxy: credentials.json needs no real GitHub PAT since
	// git http-backend enforces nothing -- "anonymous" plays the bot
	// credential's role here.
	dataDir := t.TempDir()
	mustWriteFile(t, filepath.Join(dataDir, "secrets", "github", "credentials.json"), `{"*": "anonymous"}`)
	giveTeamCredential(t, store, ctx, model.DefaultTeamID, "anonymous")
	mustWriteFile(t, filepath.Join(dataDir, "secrets", "sandbox-tokens.json"),
		`{"sandbox-0": "live-test-sandbox-token"}`)

	proxy, err := gitproxy.BuildProxy(gitproxy.BuildConfig{
		DataDir: dataDir, Store: store,
		ForwardHost: backendHost, ForwardTLS: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	proxyServer := httptest.NewServer(gitproxy.NewHandler(proxy))
	defer proxyServer.Close()

	// --- the sandbox stand-in: NewSandboxTools' root, with git
	// credentials configured exactly as a real dispatch would.
	root := t.TempDir()
	remote := proxyServer.URL + "/owner/widgets.git"
	if err := mcp.ConfigureGitCredentials(root, remote, "live-test-sandbox-token", mcp.GitIdentity{}); err != nil {
		t.Fatal(err)
	}

	// --- gemini, scripted rather than live: one turn that clones,
	// commits, and pushes, proving an agent's ordinary tool calls reach
	// the proxy without ever seeing its token.
	script := []antigravity.Step{
		antigravity.ToolStep("run_command", map[string]any{
			"command": fmt.Sprintf(
				"git clone %s work && cd work && "+
					"echo 'agent was here' >> README.md && "+
					"git add README.md && git commit -m 'agent commit' -q && "+
					"git push origin HEAD:grain/task-t1",
				remote),
		}),
		// The same commit at the default branch: refused by the proxy,
		// since a sandbox may push only its own task's branch.
		antigravity.ToolStep("run_command", map[string]any{
			"command": "cd work && git push origin HEAD:main",
		}),
		antigravity.TextStep("done"),
	}
	framework := antigravity.NewForTest(antigravity.Steps(script...))

	result, err := framework.Run(ctx, agent.RunConfig{Prompt: "clone, edit, push", SandboxRoot: root})
	if err != nil {
		t.Fatalf("agent run failed: %v", err)
	}
	if len(result.ToolCalls) != 2 || result.ToolCalls[0].IsError {
		t.Fatalf("run_command calls: %+v", result.ToolCalls)
	}
	// Refused the way receive-pack refuses an update, so git prints the
	// proxy's reason rather than a bare "HTTP 403".
	if refused := result.ToolCalls[1]; !refused.IsError || !strings.Contains(refused.Text, "[remote rejected] HEAD -> main") ||
		!strings.Contains(refused.Text, "git push origin grain/task-t1") {
		t.Errorf("pushing the default branch: %+v, want it rejected with the proxy's reason", refused)
	}

	// --- verify against the upstream bare repo directly: the push
	// actually landed, through the proxy, authorized by the model alone,
	// and only on the task's own branch.
	gitDir := filepath.Join(upstreamDir, "owner", "widgets.git")
	out := runOutput(t, upstreamDir, "git", "--git-dir", gitDir, "log", "-1", "--format=%s", "grain/task-t1")
	if strings.TrimSpace(out) != "agent commit" {
		t.Errorf("upstream grain/task-t1 tip = %q, want the agent's commit", out)
	}
	out = runOutput(t, upstreamDir, "git", "--git-dir", gitDir, "log", "-1", "--format=%s", "main")
	if strings.TrimSpace(out) == "agent commit" {
		t.Error("upstream main has the agent's commit: the proxy let a sandbox push the default branch")
	}
}

// TestLivePushToARepoOutsideTheTasksScopeIsDenied proves the flip side:
// the same sandbox, same proxy, but a repo the task never named is
// refused before the local git http-backend ever sees a request for it.
func TestLivePushToARepoOutsideTheTasksScopeIsDenied(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	upstreamDir := t.TempDir()
	newBareRepoWithOneCommit(t, upstreamDir, "owner", "other-repo")
	backend := httptest.NewServer(gitHTTPBackend(upstreamDir))
	defer backend.Close()
	backendHost := mustHost(t, backend.URL)

	store, ctx := openStore(t)
	task := model.Task{
		ID: "t1", Intent: model.IntentImplement, Title: "test task",
		Origin: model.Origin{
			Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "tester"}},
			Reason:      model.ReasonDirect,
		},
		Target:  &model.RepoRef{Owner: "owner", Name: "widgets"}, // NOT other-repo
		Binding: model.BindingDirective,
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: "t1-1", TaskID: "t1", Sandbox: "sandbox-0",
		Attempt: 1, StartedAt: time.Now(),
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}

	dataDir := t.TempDir()
	mustWriteFile(t, filepath.Join(dataDir, "secrets", "github", "credentials.json"), `{"*": "anonymous"}`)
	giveTeamCredential(t, store, ctx, model.DefaultTeamID, "anonymous")
	mustWriteFile(t, filepath.Join(dataDir, "secrets", "sandbox-tokens.json"),
		`{"sandbox-0": "live-test-sandbox-token"}`)
	proxy, err := gitproxy.BuildProxy(gitproxy.BuildConfig{
		DataDir: dataDir, Store: store, ForwardHost: backendHost, ForwardTLS: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	proxyServer := httptest.NewServer(gitproxy.NewHandler(proxy))
	defer proxyServer.Close()

	dir := t.TempDir()
	cmd := exec.Command("git", "-c", fmt.Sprintf("http.extraHeader=Authorization: Basic %s", basicAuth("live-test-sandbox-token")),
		"clone", proxyServer.URL+"/owner/other-repo.git", filepath.Join(dir, "clone"))
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected the clone to fail for an out-of-scope repo, output:\n%s", out)
	}
}

func basicAuth(token string) string {
	return base64.StdEncoding.EncodeToString([]byte("x:" + token))
}

// mustHost strips httptest.NewServer's fixed "http://" scheme, since
// gitproxy.BuildConfig.ForwardHost wants a bare host:port -- RealForwarder
// (forward.go) prepends the scheme itself from ForwardTLS.
func mustHost(t *testing.T, rawURL string) string {
	t.Helper()
	host, ok := strings.CutPrefix(rawURL, "http://")
	if !ok {
		t.Fatalf("expected an http:// URL from httptest.NewServer, got %q", rawURL)
	}
	return host
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runOutput(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}
