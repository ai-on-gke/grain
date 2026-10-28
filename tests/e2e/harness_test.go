// Package e2e ties every layer v2 owns today into one pipeline: a task
// filed the way a human would, dispatch.Cycle deciding when it runs, an agent
// (agent/antigravity) actually driving it, and gitproxy actually authorizing
// and forwarding the git push that results -- against a real embedded
// SQLite store and a real (local, git http-backend) stand-in for GitHub.
// That is the same discipline gitproxy/live_test.go already holds to one
// layer down; what this package adds is dispatch.Cycle's own dispatch
// decision, more than one task/run, and the state transitions that
// follow a run (completed, awaiting_reply, closed), not just the push
// itself.
//
// README.md's "What this does not have yet" section is why this
// package stops where it does: no host adapter means no real sandbox VM
// (NewSandboxTools' root stands in, as it does everywhere else in v2 —
// see world.sandboxRoot), and this harness builds no github.Client at all, so
// "the PR opened" and "the issue closed" are simulated with the same
// store.Observe calls model/simulate_test.go's GitHub-sync helpers
// already use, rather than a real API response. A merge is simulated the
// same way: the test itself merges the pushed branch with a plain `git
// merge`, standing in for GitHub's own merge button. pkg/orchestrator/
// live_test.go is the same two scenarios this file proves by hand, driven
// instead through a real github.Client against githubsim -- a real
// CreatePullRequest, and a real close-out once githubsim reports the PR
// merged, rather than either being simulated here.
package e2e

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"maps"
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

	"github.com/bwsalmon/grain/pkg/agent"
	"github.com/bwsalmon/grain/pkg/agent/antigravity"
	"github.com/bwsalmon/grain/pkg/dispatch"
	"github.com/bwsalmon/grain/pkg/github/githubsim"
	"github.com/bwsalmon/grain/pkg/gitproxy"
	"github.com/bwsalmon/grain/pkg/mcp"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/model/sqlite"
	"github.com/bwsalmon/grain/pkg/orchestrator"
)

var baseTime = time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

// TestMain turns off the check-registration window for this whole suite
// -- the wait that keeps an empty check list from reading as clean until
// CI has had time to register one, see
// orchestrator.SetCheckRegistrationWindow.
//
// Every repo in this package is a bare repo in a temp directory with no
// CI anywhere near it, and these tests drive real cycles against a real
// wall clock rather than a seeded one. Leaving the window on would make
// each auto-merge here sit out two real minutes waiting for a check run
// that does not exist. The window's own behaviour is covered where the
// clock can be handed in, in pkg/orchestrator.
//
// It also hands every client here the test access code
// (useTestAccessCode), as the grain CLI on a daemon's own host has it.
func TestMain(m *testing.M) {
	restore := orchestrator.SetCheckRegistrationWindow(0)
	removeAccessCode, err := useTestAccessCode()
	if err != nil {
		fmt.Fprintln(os.Stderr, "writing the test access code:", err)
		os.Exit(1)
	}
	code := m.Run()
	removeAccessCode()
	restore()
	// The one grain binary the CLI tests share (cli_test.go's
	// buildGrainCLI) outlives every test that used it, so this is the
	// only scope that can remove it.
	if grainCLIDir != "" {
		_ = os.RemoveAll(grainCLIDir)
	}
	os.Exit(code)
}

func human(id string) model.Principal { return model.Principal{Kind: model.PrincipalHuman, ID: id} }

// world is everything one test needs standing in for the outside world: a
// real store, a real "upstream" (the GitHub stand-in, one directory of
// bare repos served over smart HTTP), and a real gitproxy in front of it,
// authorized off the store the same way the production proxy would be.
type world struct {
	t     *testing.T
	store *model.Store
	// db is the same database store is built on, kept because
	// pkg/staterepo works in rows rather than in model types: an export,
	// an import and a merge applied to a running deployment all take the
	// *sql.DB (custom_field_loop_test.go is the caller).
	db          *sql.DB
	ctx         context.Context
	upstreamDir string
	proxyURL    string
	// auth is the stand-in GitHub's own auth layer, nil unless the world
	// was built withGitHubAuth -- what a test changes when it grants the
	// deployment's credential something it was missing.
	auth *gitHubAuth

	tokens      *gitproxy.SandboxTokenStore
	credentials *gitproxy.CredentialSet
	mu          sync.Mutex
	roots       map[string]string // sandbox name -> its sandbox-stand-in directory
	// prNumber hands out the pull request numbers the GitHub-sync
	// stand-in links onto tasks (e2e_test.go's observeCompletion), so two
	// completions in one world never claim to be the same pull request.
	// Under mu with roots because a world is shared by the runs in
	// flight in it.
	prNumber int
	// teamCredential is the credential the harness gives the default
	// team (the ladder's "*" entry), for a test that creates teams of
	// its own and has them clone: a team has no deployment credential
	// to fall back to (grain/task-1592).
	teamCredential string
}

// worldOption changes how newWorld builds the world around the store --
// today, only what the stand-in GitHub does with the credential the proxy
// forwards under.
type worldOption func(*worldConfig)

type worldConfig struct {
	// gitHubTokens, when set, is what GitHub knows about each token,
	// keyed the way githubsim.GitUpstream keys them. Nil leaves the git
	// http-backend serving directly, which enforces nothing -- the world
	// every test that is not about credentials wants.
	gitHubTokens map[string]githubsim.GitAccess
	// namedSecrets, when set, writes extra named tokens into secrets/github/
	// so that per-team or per-grant credentials can be resolved by the proxy.
	namedSecrets map[string]string
	// forwardHost, when set, overrides the default forward host passed to BuildProxy.
	forwardHost string
}

// e2eBotToken is the deployment's own GitHub credential in a world built
// with withGitHubAuth: what credentials.json's "bot" entry resolves to,
// and what GitUpstream sees arrive. It is nothing like the per-run proxy
// token a sandbox holds, which is the distinction a refusal on this
// credential is about.
const e2eBotToken = "e2e-bot-token"

// withGitHubAuth puts githubsim.GitUpstream -- github.com's own git
// auth layer, whose answers were measured against the real thing -- in
// front of the git http-backend, and points the credential ladder at a
// "bot" token rather than at anonymous access.
//
// It is what lets a test arrange a run that fails on *the deployment's*
// credential rather than on the task's scope: a bot token GitHub will not
// let write, or has never heard of, refuses a push that the model
// authorized and the proxy forwarded. Without it every repo here is
// served by a backend that enforces nothing, so the only reachable
// failure is the proxy's own refusal.
func withGitHubAuth(tokens map[string]githubsim.GitAccess) worldOption {
	return func(c *worldConfig) { c.gitHubTokens = tokens }
}

// gitHubAuth is githubsim.GitUpstream with a token map a test may change
// between runs. GitUpstream is a plain value read as it serves, and
// "grant the credential the write it lacked, then dispatch again" is a
// change made while an httptest.Server is already serving it -- so the
// map is replaced whole, under a lock, rather than written into.
type gitHubAuth struct {
	mu      sync.Mutex
	tokens  map[string]githubsim.GitAccess
	backend http.Handler
}

func (a *gitHubAuth) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	upstream := &githubsim.GitUpstream{Tokens: a.tokens, Backend: a.backend}
	a.mu.Unlock()
	upstream.ServeHTTP(w, r)
}

// grantWrite is an operator giving a credential the push permission it
// did not have -- a fine-grained token's Contents: write ticked on, an
// App installation's access widened -- with nothing else about the
// deployment touched.
func (a *gitHubAuth) grantWrite(token, repo string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	granted := maps.Clone(a.tokens)
	access := granted[token]
	access.Write = append(append([]string(nil), access.Write...), repo)
	granted[token] = access
	a.tokens = granted
}

// newWorld builds one world with no sandboxes in it. A sandbox is built
// when a run needs one (sandboxRoot), minting that run's own proxy token
// as it goes -- which is what a real deployment does now that a sandbox
// exists for exactly one run.
//
// It used to take a fixed slot pool and credential every slot up front,
// because that is what a long-lived sandbox's git config genuinely was:
// provisioning-time setup, done once per slot rather than once per task.
// Nothing is set up ahead of time here any more, and the proxy is started
// against an empty token file -- it re-reads on an unknown token
// (gitproxy.SandboxTokens), which is what lets a run mint one while the
// proxy is already serving.
func newWorld(t *testing.T, opts ...worldOption) *world {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	var cfg worldConfig
	for _, opt := range opts {
		opt(&cfg)
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

	var upstream http.Handler = gitHTTPBackend(upstreamDir)
	credentials := `{"*": "anonymous"}`
	var auth *gitHubAuth
	if cfg.gitHubTokens != nil {
		auth = &gitHubAuth{tokens: cfg.gitHubTokens, backend: upstream}
		upstream = auth
		credentials = `{"*": "bot"}`
		mustWriteFile(t, filepath.Join(dataDir, "secrets", "github", "bot.token"), e2eBotToken)
	}
	for name, token := range cfg.namedSecrets {
		mustWriteFile(t, filepath.Join(dataDir, "secrets", "github", name+".token"), token)
	}
	backendHost := mustHost(t, newTestServer(t, upstream))
	if cfg.forwardHost != "" {
		backendHost = cfg.forwardHost
	}

	mustWriteFile(t, filepath.Join(dataDir, "secrets", "github", "credentials.json"), credentials)
	// A team's work clones and pushes with the team's own credential and
	// never the deployment's ladder (grain/task-1592), so the default
	// team these tests file their work on is given the ladder's "*"
	// credential -- what the upgrade's migration
	// (ui.Client.MigrateTeamGitCredentials) gives a team that relied on it.
	defaultCredential := "anonymous"
	if cfg.gitHubTokens != nil {
		defaultCredential = "bot"
	}
	if err := store.UpdateConfig(ctx, func(tm *model.Config) error {
		tm.GitCredential = defaultCredential
		return nil
	}); err != nil {
		t.Fatalf("giving the default team its credential: %v", err)
	}

	proxy, err := gitproxy.BuildProxy(gitproxy.BuildConfig{
		DataDir: dataDir, Store: store, ForwardHost: backendHost, ForwardTLS: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	proxyURL := newTestServer(t, gitproxy.NewHandler(proxy))

	return &world{
		t: t, store: store, db: db, ctx: ctx, upstreamDir: upstreamDir,
		proxyURL: proxyURL, auth: auth, teamCredential: defaultCredential,
		tokens:      gitproxy.NewSandboxTokenStore(filepath.Join(dataDir, "secrets", "sandbox-tokens.json")),
		credentials: proxy.Credentials,
		roots:       map[string]string{},
	}
}

// sandboxRoot is the directory standing in for sandbox's own filesystem,
// built and credentialed on first use -- this harness's equivalent of
// orchestrator.HostSandboxes.Acquire plus the token minting and
// ConfigureGitCredentials call runOne makes around it.
// workTasks is every task in the store that somebody actually filed --
// the standing console sessions left out (model.ConsoleSessionIDOf,
// grain/task-415).
//
// A cycle files the director for itself on a deployment that has never
// configured otherwise, and these tests run real cycles, so a bare
// ListTasks count no longer answers the question they are asking. What
// they are asking is "did this cost a task nobody asked for" -- a fix
// task, a repair -- and a conversation grain keeps open is not one.
func (w *world) workTasks() []model.Task {
	w.t.Helper()
	all, err := w.store.ListTasks(w.ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	var out []model.Task
	for _, task := range all {
		if _, ok := model.ConsoleSessionIDOf(task); ok {
			continue
		}
		out = append(out, task)
	}
	return out
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
	// The path in this dummy remote is never used -- only its scheme and
	// host are, to build the credential-store line -- so any repo name
	// stands in here even though this run may touch several different
	// repos through the same proxy.
	if err := mcp.ConfigureGitCredentials(root, w.proxyURL+"/placeholder/placeholder.git", token, mcp.GitIdentity{}); err != nil {
		w.t.Fatalf("configuring git credentials for %s: %v", sandbox, err)
	}
	w.roots[sandbox] = root
	return root
}

// grantGitHubWrite gives the deployment's credential write access to
// owner/name at the stand-in GitHub, for a test that wants the same task
// dispatched again against a credential that can finally do the job.
func (w *world) grantGitHubWrite(token, repo string) {
	w.t.Helper()
	if w.auth == nil {
		w.t.Fatal("this world has no GitHub auth layer -- build it with withGitHubAuth")
	}
	w.auth.grantWrite(token, repo)
}

// sandboxCredentials is what sandbox's ~/.git-credentials holds now. It
// is not the same file across a run: git's own credential.helper = store
// rewrites it, and erases it outright when it is told 401 for the
// credential it used -- which is why a test that cares whether a sandbox
// still has its proxy token has to read the file back rather than
// remember what was written into it.
func (w *world) sandboxCredentials(sandbox string) string {
	w.t.Helper()
	w.mu.Lock()
	root, ok := w.roots[sandbox]
	w.mu.Unlock()
	if !ok {
		w.t.Fatalf("no sandbox directory for %s", sandbox)
	}
	content, err := os.ReadFile(filepath.Join(root, ".git-credentials"))
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		w.t.Fatal(err)
	}
	return string(content)
}

// prepareSandbox builds d's own sandbox directory and records its name on
// the run, which is what orchestrator.runOne does once it has acquired
// one. The record is load-bearing, not bookkeeping: the git proxy
// resolves the token a sandbox authenticates with back to the task whose
// repos it may touch by looking up the live run on that sandbox name
// (Store.GitScope), so without it every clone through the proxy comes
// back 403 "not in scope for this sandbox".
func (w *world) prepareSandbox(d dispatch.Dispatch) string {
	w.t.Helper()
	root := w.sandboxRoot(d.RunID)
	// Pinned with the task's target, the way runOne pins the one it read
	// before cloning it: the proxy authorizes this run's push against
	// what is recorded here rather than against the task as it stands at
	// the moment of the push (Store.GitScope), so a run left unpinned
	// would be one nothing may push.
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

// newRepo creates a bare repo at upstream/owner/name.git seeded with one
// commit on main -- this world's stand-in for a real GitHub repo the
// controller already knows about.
func (w *world) newRepo(owner, name string) {
	w.t.Helper()
	bare := initBareRepo(w.t, filepath.Join(w.upstreamDir, owner, name+".git"), "main")
	// GIT_HTTP_EXPORT_ALL only covers the read side; git http-backend
	// denies git-receive-pack (push) by default even with it set --
	// matching gitproxy/live_test.go's own note on the same rig.
	run(w.t, w.upstreamDir, "git", "-C", bare, "config", "http.receivepack", "true")

	seedParent := w.t.TempDir()
	run(w.t, seedParent, "git", "clone", bare, "seed")
	seed := filepath.Join(seedParent, "seed")
	run(w.t, seed, "git", "config", "user.email", "seed@example.com")
	run(w.t, seed, "git", "config", "user.name", "seed")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("hello\n"), 0o644); err != nil {
		w.t.Fatal(err)
	}
	run(w.t, seed, "git", "add", "README.md")
	run(w.t, seed, "git", "commit", "-q", "-m", "initial commit")
	run(w.t, seed, "git", "push", "origin", "main")
}

// remote is the proxy URL a sandbox's agent would clone/push against for
// owner/name -- what a real dispatch would write into its prompt or
// discover by running `git remote -v` after a real host adapter clones
// the repo for it.
func (w *world) remote(owner, name string) string {
	return w.proxyURL + "/" + owner + "/" + name + ".git"
}

// runDispatch drives one dispatch.Cycle Dispatch to completion in its own
// run's sandbox-stand-in directory, through a scripted (not live) gemini
// agent, and calls FinishRun once the agent's turn ends. It does not
// touch task_observation -- that is the GitHub-sync stand-in's job,
// applied by the caller from the returned result, the same separation
// model/simulate_test.go's components hold to.
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

// askedQuestion returns the question argument of the first ask_question
// call in result, if any -- the harness's only way to see what the
// mocked escape-hatch tool recorded, since antigravity.Framework.Run's own
// MockSink is internal and discarded when Run returns (mcp/mock_tools.go);
// result.ToolCalls is the seam v2 leaves for exactly this.
func askedQuestion(result *agent.Result) (string, bool) {
	for _, c := range result.ToolCalls {
		if c.Name == "ask_question" {
			if q, ok := c.Arguments["question"].(string); ok {
				return q, true
			}
			return "", true
		}
	}
	return "", false
}

// pushedOK reports whether result's tool calls look like a clean
// clone/commit/push -- at least one non-error run_command call and no
// error at all.
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

// mergeBranchIntoDefault simulates GitHub's own merge button: a plain
// git merge of branch into defaultBranch, performed directly against the
// bare upstream repo -- nothing in v2 owns this action (README.md,
// "What this does not have yet: TrackedPullRequest ... anything that
// reads or writes GitHub's REST API"), so a test standing in for GitHub
// is the only way to exercise the git side of "the PR merged."
func (w *world) mergeBranchIntoDefault(owner, name, branch, defaultBranch string) {
	w.t.Helper()
	bare := filepath.Join(w.upstreamDir, owner, name+".git")
	dir := w.t.TempDir()
	run(w.t, dir, "git", "clone", bare, "merge")
	wd := filepath.Join(dir, "merge")
	run(w.t, wd, "git", "config", "user.email", "github@example.com")
	run(w.t, wd, "git", "config", "user.name", "github (simulated merge)")
	run(w.t, wd, "git", "fetch", "origin", branch)
	run(w.t, wd, "git", "checkout", defaultBranch)
	run(w.t, wd, "git", "merge", "--no-ff", "origin/"+branch, "-m", "Merge "+branch)
	run(w.t, wd, "git", "push", "origin", defaultBranch)
}

// branchExists reports whether ref exists in owner/name's bare repo.
func (w *world) branchExists(owner, name, ref string) bool {
	bare := filepath.Join(w.upstreamDir, owner, name+".git")
	cmd := exec.Command("git", "--git-dir", bare, "rev-parse", "--verify", ref)
	return cmd.Run() == nil
}

// branchContains reports whether ref's history already includes
// ancestor -- how a check that "the merge really landed" tells that
// apart from "a same-named ref exists but was never actually merged."
func (w *world) branchContains(owner, name, ref, ancestor string) bool {
	bare := filepath.Join(w.upstreamDir, owner, name+".git")
	cmd := exec.Command("git", "--git-dir", bare, "merge-base", "--is-ancestor", ancestor, ref)
	return cmd.Run() == nil
}

// log1 is `git log -1 --format=format` against owner/name's bare repo at
// ref, trimmed -- how every assertion below reads back what actually
// landed in the upstream stand-in, independent of anything the model or
// proxy believes happened.
func (w *world) log1(owner, name, ref, format string) string {
	w.t.Helper()
	bare := filepath.Join(w.upstreamDir, owner, name+".git")
	return strings.TrimSpace(runOutput(w.t, w.upstreamDir, "git", "--git-dir", bare, "log", "-1", "--format="+format, ref))
}

// --- small scripting helpers for the gemini agent, mirroring
// gitproxy/live_test.go's own (package-private, deliberately
// duplicated -- see that file's comment on why) ---------------------

func finalText(text string) antigravity.Step { return antigravity.TextStep(text) }

func toolCall(name string, args map[string]any) antigravity.Step {
	return antigravity.ToolStep(name, args)
}

// pushScript is the scripted turn a "succeeding" agent run takes: clone
// the task's target repo, make a small change identifying itself by
// taskID, and push it to a fresh branch -- never main, matching
// model.BranchName's own contract that a task's work lands on its own
// branch.
func pushScript(remote, branch, taskID string) []antigravity.Step {
	cmd := "git clone " + remote + " work && cd work && " +
		"git checkout -b " + branch + " && " +
		"echo 'change for " + taskID + "' >> NOTES.md && " +
		"git add NOTES.md && git commit -q -m 'agent commit for " + taskID + "' && " +
		"git push origin " + branch
	return []antigravity.Step{
		toolCall("run_command", map[string]any{"command": cmd}),
		finalText("pushed " + branch),
	}
}

// pushScriptOwnFile is pushScript with one difference: the agent writes a
// file named for its own task rather than appending to a shared NOTES.md.
//
// That matters wherever several independent tasks' branches are all
// merged into one default branch, because appending to the same file from
// two branches conflicts on merge -- which says nothing about the task
// model and everything about the scripted content. It surfaced when
// sandboxes became per-run: before that, a second dispatch inherited the
// previous run's directory, `git clone work` failed against the one
// already sitting there, and the run failed before it could ever push.
// The happy path a simulation is meant to exercise was being skipped, so
// the conflict never arose. (A merge conflict on purpose is
// mergequeue_conflict_test.go's own subject.)
func pushScriptOwnFile(remote, branch, taskID string) []antigravity.Step {
	cmd := "git clone " + remote + " work && cd work && " +
		"git checkout -b " + branch + " && " +
		"echo 'change for " + taskID + "' > NOTES-" + taskID + ".md && " +
		"git add NOTES-" + taskID + ".md && git commit -q -m 'agent commit for " + taskID + "' && " +
		"git push origin " + branch
	return []antigravity.Step{
		toolCall("run_command", map[string]any{"command": cmd}),
		finalText("pushed " + branch),
	}
}

// failScript is a run that never touches git at all, so a retry after it
// never risks colliding with a branch a real push would have made.
func failScript(reason string) []antigravity.Step {
	return []antigravity.Step{
		toolCall("run_command", map[string]any{"command": "echo '" + reason + "' >&2; exit 1"}),
		finalText("could not complete the task: " + reason),
	}
}

// askScript has the agent call the ask_question escape hatch instead of
// touching git -- mcp/mock_tools.go's own docstring: "this ends your
// turn ... do not take any further actions," which this script honors by
// following it with nothing but a short close-out turn, matching how a
// human reading the tool's own contract would expect it to behave.
func askScript(question string) []antigravity.Step {
	return []antigravity.Step{
		toolCall("ask_question", map[string]any{"question": question}),
		finalText("waiting on a reply"),
	}
}

// --- generic process/HTTP plumbing, duplicated per-package the same
// way gitproxy/live_test.go duplicates it rather than sharing a testutil
// package nothing else in v2 has yet ---------------------------------

func run(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
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

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// newTestServer starts h on an httptest server, registers its own
// cleanup, and returns its URL -- a one-line version of the
// httptest.NewServer/defer Close() pair every caller here would
// otherwise repeat.
func newTestServer(t *testing.T, h http.Handler) string {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s.URL
}

// mustHost strips httptest.NewServer's fixed "http://" scheme, since
// gitproxy.BuildConfig.ForwardHost wants a bare host:port -- RealForwarder
// (gitproxy/forward.go) prepends the scheme itself from ForwardTLS.
func mustHost(t *testing.T, rawURL string) string {
	t.Helper()
	host, ok := strings.CutPrefix(rawURL, "http://")
	if !ok {
		t.Fatalf("expected an http:// URL from httptest.NewServer, got %q", rawURL)
	}
	return host
}

// gitHTTPBackend serves projectRoot over the smart-HTTP CGI contract git
// itself defines, by shelling out to `git http-backend` per request --
// the same technique gitproxy/live_test.go's gitHTTPBackend (and, before
// it, v1's tests/test_vm_integration.py) uses to stand in for GitHub
// without a live credential. Duplicated here rather than exported from
// gitproxy_test, which is an internal test package another package
// cannot import.
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
			// git gzips a request body over 1KB -- a clone or fetch of a
			// repository with a few dozen refs -- and http-backend only
			// inflates it when told, as pkg/gitproxy/local.go tells it.
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

// credentialedSandboxes wraps a Sandboxes backend so every sandbox a
// dispatch builds gets a git identity pointed at remote, at the moment a
// real deployment gives it one -- as part of preparing the run, not
// before the cycle. Without it `git commit` fails outright in a fresh
// sandbox (mcp.ConfigureGitCredentials' own doc comment).
//
// These tests used to do it by hand, once per slot, up front: a slot's
// directory outlived every run dispatched into it, so provisioning-time
// setup was exactly what it was. A sandbox does not exist until its
// dispatch builds it now, so this is the only place left to do it.
//
// It also keeps each sandbox's directory alive past Release, and records
// where it was, so a test can read back what a run actually left on disk
// (rootOf). A real Release deletes the directory -- that is the whole
// point of it, and orchestrator's own lifecycle_test.go is where it is
// asserted -- but a test asserting on a placement the run wrote has to be
// able to look after the cycle has finished. The directories are under
// t.TempDir() either way, so nothing outlives the test.
type credentialedSandboxes struct {
	inner  orchestrator.Sandboxes
	remote string
	t      *testing.T

	mu    sync.Mutex
	roots map[string]string
}

func (s *credentialedSandboxes) Acquire(ctx context.Context, name string, shape orchestrator.Shape) (orchestrator.Sandbox, error) {
	sb, err := s.inner.Acquire(ctx, name, shape)
	if err != nil {
		return nil, err
	}
	// Returned, never t.Fatalf'd: reconcileDispatch calls Acquire on its
	// own goroutine, one per dispatch, and t.Fatalf outside the test
	// goroutine calls runtime.Goexit on the wrong one -- the test does not
	// stop where it says it did, and the failure surfaces somewhere less
	// useful. runOne already turns this error into a failed dispatch.
	if err := sb.ConfigureGitCredentials(ctx, s.remote, "unused", mcp.GitIdentity{}); err != nil {
		return nil, fmt.Errorf("configuring git credentials on %s: %w", name, err)
	}
	rooted, ok := sb.(interface{ Root() (string, error) })
	if !ok {
		// Only HostSandboxes is wired up here, and a sandbox with no local
		// directory would silently give runOne an empty sandboxRoot to
		// place capabilities into -- see keptSandbox.
		return nil, fmt.Errorf("e2e: sandbox %s has no local directory; this harness only wraps HostSandboxes", name)
	}
	root, err := rooted.Root()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.roots[name] = root
	s.mu.Unlock()
	return keptSandbox{Sandbox: sb, root: root}, nil
}

// rootOf is the directory the named sandbox was given, for a test reading
// back what its run wrote.
func (s *credentialedSandboxes) rootOf(name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	root, ok := s.roots[name]
	if !ok {
		s.t.Fatalf("no sandbox named %q was ever acquired (acquired: %v)", name, s.roots)
	}
	return root
}

// keptSandbox is a Sandbox whose Release does nothing -- see
// credentialedSandboxes' own doc comment on why these tests keep the
// directory around.
//
// Root is forwarded explicitly rather than left to the embedded Sandbox:
// rootedSandbox is an optional interface runOne type-asserts for, and an
// embedded interface value does not carry methods outside its own method
// set, so wrapping a host sandbox without this makes a task with
// capabilities fail with "no local directory to place them in".
type keptSandbox struct {
	orchestrator.Sandbox
	root string
}

// Root is unconditional, which is only safe because Acquire above refuses
// a sandbox that has no local directory of its own. rootedSandbox is an
// optional interface runOne type-asserts for, so a wrapper that always
// implements it turns "this backend has no directory to place
// capabilities in" from a refused dispatch into a placement written to
// "".
func (s keptSandbox) Root() (string, error) { return s.root, nil }

func (keptSandbox) Release(ctx context.Context) error { return nil }

// credentialed is credentialedSandboxes over a fresh HostSandboxes -- the
// two lines almost every dispatch-driving test here needs.
func credentialed(t *testing.T, remote string) *credentialedSandboxes {
	return &credentialedSandboxes{
		inner: orchestrator.NewHostSandboxes(t.TempDir()), remote: remote, t: t,
		roots: map[string]string{},
	}
}
