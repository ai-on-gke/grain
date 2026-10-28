package codex

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
)

// recordingRunner captures everything Framework.Run hands the subprocess
// and replays a canned stream back, so Run's arg building, its config
// plumbing and its transcript parsing can all be exercised without a
// real codex binary or a live OpenAI credential.
type recordingRunner struct {
	args   []string
	stdin  string
	env    []string
	dir    string
	stdout string
	err    error
	// configAtRun is a copy of the config file found in the CODEX_HOME
	// this run was given, read while the run is notionally in flight --
	// Run deletes that directory as it returns, so a test cannot look
	// afterwards.
	mcpArgs     string
	configAtRun string
	authAtRun   string
	authLink    string
	// authWriteAtRun, when set, simulates Codex rotating its OAuth
	// tokens through CODEX_HOME/auth.json.
	authWriteAtRun string
}

func (r *recordingRunner) Run(_ context.Context, args []string, stdin string, env []string, dir string, tee io.Writer) (string, error) {
	r.args, r.stdin, r.env, r.dir = args, stdin, env, dir
	for _, e := range env {
		if home, ok := strings.CutPrefix(e, "CODEX_HOME="); ok {
			if data, err := os.ReadFile(filepath.Join(home, configFileName)); err == nil {
				r.configAtRun = string(data)
			}
			authPath := filepath.Join(home, authFileName)
			if target, err := os.Readlink(authPath); err == nil {
				r.authLink = target
			}
			if data, err := os.ReadFile(authPath); err == nil {
				r.authAtRun = string(data)
			}
			if r.authWriteAtRun != "" {
				_ = os.WriteFile(authPath, []byte(r.authWriteAtRun), 0o600)
			}
		}
	}
	if tee != nil {
		io.WriteString(tee, r.stdout)
	}
	return r.stdout, r.err
}

// okStream is the shortest complete run: one tool call, one assistant
// message, one completed turn.
func okStream() string {
	return strings.Join([]string{
		`{"type":"thread.started","thread_id":"t1"}`,
		`{"type":"item.started","item":{"id":"i1","item_type":"mcp_tool_call",` +
			`"server":"grain-sandbox","tool":"write_file","arguments":{"file_path":"ok.txt"}}}`,
		`{"type":"item.completed","item":{"id":"i1","item_type":"mcp_tool_call",` +
			`"server":"grain-sandbox","tool":"write_file","status":"completed","result":"wrote ok.txt"}}`,
		`{"type":"item.completed","item":{"id":"i2","item_type":"agent_message","text":"done"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":10}}`,
	}, "\n")
}

func argsHave(args []string, want ...string) bool {
	for i := range args {
		if slices.Equal(args[i:min(i+len(want), len(args))], want) {
			return true
		}
	}
	return false
}

// TestRunSendsThePromptOverStdinAndNeverInArgv is the discipline v1's
// dispatch.py set and both other frameworks kept: untrusted issue
// content must never become a ps-visible argument. codex exec takes a
// prompt as a positional argument, which is exactly why this package
// passes "-" and writes the prompt to stdin instead.
func TestRunSendsThePromptOverStdinAndNeverInArgv(t *testing.T) {
	const secret = "untrusted-issue-body-that-must-not-reach-argv"
	r := &recordingRunner{stdout: okStream()}
	f := newTestFramework(r, "/usr/local/bin/grain")

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: secret, SandboxRoot: t.TempDir()}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, a := range r.args {
		if strings.Contains(a, secret) {
			t.Fatalf("prompt reached argv (%q); it must travel over stdin only", a)
		}
	}
	if r.stdin != secret {
		t.Errorf("stdin = %q, want the prompt verbatim", r.stdin)
	}
	if r.args[len(r.args)-1] != "-" {
		t.Errorf("args = %v, want the trailing \"-\" that makes codex read the prompt from stdin", r.args)
	}
}

// TestRunAsksForTheJSONStreamAndSkipsTheRepoCheck covers the two
// arguments every run needs whatever else it is doing: the parser reads
// codex's event stream, and a granule run leaves codex started in a
// directory that is nobody's repository.
func TestRunAsksForTheJSONStreamAndSkipsTheRepoCheck(t *testing.T) {
	r := &recordingRunner{stdout: okStream()}
	f := newTestFramework(r, "/usr/local/bin/grain")

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if r.args[0] != "exec" {
		t.Errorf("args = %v, want the exec subcommand first", r.args)
	}
	if !slices.Contains(r.args, "--json") {
		t.Errorf("args = %v, want --json", r.args)
	}
	if !slices.Contains(r.args, "--skip-git-repo-check") {
		t.Errorf("args = %v, want --skip-git-repo-check", r.args)
	}
	if slices.Contains(r.args, "--model") {
		t.Errorf("args = %v, want no --model: none was named, so codex runs its own default", r.args)
	}
}

// TestRunWritesThisRunsOwnConfigIntoAPrivateCodexHome is the property
// that stands in for claude's --mcp-config: codex registers MCP servers
// in a config file inside its config directory, so two concurrent runs
// against two sandboxes could otherwise only ever see whichever
// registration was written last.
func TestRunWritesThisRunsOwnConfigIntoAPrivateCodexHome(t *testing.T) {
	root := t.TempDir()
	r := &recordingRunner{stdout: okStream()}
	f := newTestFramework(r, "/usr/local/bin/grain")

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: root}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var home string
	for _, e := range r.env {
		if h, ok := strings.CutPrefix(e, "CODEX_HOME="); ok {
			home = h
		}
	}
	if home == "" {
		t.Fatal("no CODEX_HOME in the subprocess environment; codex would read the controller's own ~/.codex")
	}
	if r.configAtRun == "" {
		t.Fatalf("no %s written into %s", configFileName, home)
	}
	if !strings.Contains(r.configAtRun, "[mcp_servers."+mcpServerName+"]") {
		t.Errorf("config = %q, want this run's own MCP server", r.configAtRun)
	}
	if !strings.Contains(r.mcpArgs, `"-sandbox-root","`+root+`"`) {
		t.Errorf("config = %q, want the mcpserver args naming %q", r.configAtRun, root)
	}
	// The directory is this run's alone and goes when the run does --
	// nothing here may leave a registration behind for the next one.
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Errorf("CODEX_HOME %s still exists after Run; it must be cleaned up", home)
	}
}

// TestRunDeniesCodexsOwnToolsAnythingWorthHaving: codex has no
// --allowedTools to empty its native roster with, so the config it is
// given is what keeps its own shell and patch tools from being a way
// around the sandbox -- and what keeps an unattended run from blocking
// on an approval prompt nobody is there to answer.
func TestRunDeniesCodexsOwnToolsAnythingWorthHaving(t *testing.T) {
	r := &recordingRunner{stdout: okStream()}
	f := newTestFramework(r, "/usr/local/bin/grain")

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(r.configAtRun, `sandbox_mode = "read-only"`) {
		t.Errorf("config = %q, want a read-only sandbox for codex's own tools", r.configAtRun)
	}
	if !strings.Contains(r.configAtRun, `approval_policy = "never"`) {
		t.Errorf("config = %q, want approvals never asked for", r.configAtRun)
	}
	// Code Mode is orchestration, not native host access. The real-CLI
	// probe also verifies nested MCP calls reach Grain's parsed record.
	if !strings.Contains(r.configAtRun, "code_mode_host = true") {
		t.Errorf("config = %q, want codex's Code Mode enabled for MCP orchestration", r.configAtRun)
	}
}

// codex retries a refused API call about thirty times over 25 seconds of
// its own before grain hears about the refusal, and grain deliberately
// leaves that alone: retry.go's header has the whole of the reasoning,
// and the short version is that the internal ladder is short beside
// agent.BaseTransientBackoff and cannot be bounded without this package
// declaring a model provider of its own.
//
// That last part is what this asserts. codex takes request_max_retries
// and stream_max_retries per provider and nowhere else -- there is no
// environment variable an operator could set instead, and codex 0.153
// refuses to override a key on the built-in "openai" provider ("Built-in
// providers cannot be overridden") -- so bounding the retry means
// writing a whole [model_providers.*] stanza here, with a base URL, a
// wire API and an auth key this repository would then own. A change that
// starts writing one has left the decision in retry.go behind and should
// re-read it rather than inherit it.
func TestRunLeavesCodexsOwnAPIRetryAlone(t *testing.T) {
	r := &recordingRunner{stdout: okStream()}
	f := newTestFramework(r, "/usr/local/bin/grain")

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, key := range []string{"request_max_retries", "stream_max_retries", "[model_providers."} {
		if strings.Contains(r.configAtRun, key) {
			t.Errorf("config = %q, want no %s of grain's own: see this test's comment and retry.go's header", r.configAtRun, key)
		}
	}
}

// TestRunRunsInTheSandboxDirectory: a host-rooted run starts codex in
// the working tree it is meant to be working on, which is also the one
// directory its own read-only tools can usefully look at.
func TestRunRunsInTheSandboxDirectory(t *testing.T) {
	root := t.TempDir()
	r := &recordingRunner{stdout: okStream()}
	f := newTestFramework(r, "/usr/local/bin/grain")

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: root}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if r.dir == root || r.dir == "" {
		t.Errorf("dir = %q, want the sandbox root %q", r.dir, root)
	}
}

// TestRunPassesTheAPIKeyThroughTheEnvironmentOnly is the same rule both
// other frameworks keep for their own credential: a key in argv is a key
// in `ps`.
func TestRunPassesTheAPIKeyThroughTheEnvironmentOnly(t *testing.T) {
	const key = "sk-openai-fake"
	r := &recordingRunner{stdout: okStream()}
	f := newTestFramework(r, "/usr/local/bin/grain", WithAPIKey(key))

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !slices.Contains(r.env, "OPENAI_API_KEY="+key) {
		t.Errorf("env = %v, want OPENAI_API_KEY passed through the environment", r.env)
	}
	for _, a := range r.args {
		if strings.Contains(a, key) {
			t.Fatalf("the API key reached argv (%q)", a)
		}
	}
}

func TestRunSharesOnlyTheConfiguredAuthFile(t *testing.T) {
	authFile := filepath.Join(t.TempDir(), authFileName)
	if err := os.WriteFile(authFile, []byte(`{"tokens":{"refresh_token":"old"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &recordingRunner{
		stdout:         okStream(),
		authWriteAtRun: `{"tokens":{"refresh_token":"new"}}`,
	}
	f := newTestFramework(r, "/usr/local/bin/grain", WithAuthFile(authFile))

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if r.authAtRun != `{"tokens":{"refresh_token":"old"}}` {
		t.Errorf("auth visible during run = %q", r.authAtRun)
	}
	wantTarget, _ := filepath.Abs(authFile)
	if r.authLink != wantTarget {
		t.Errorf("auth.json link target = %q, want %q", r.authLink, wantTarget)
	}
	if !strings.Contains(r.configAtRun, `cli_auth_credentials_store = "file"`) {
		t.Errorf("config = %q, want the file credential store forced", r.configAtRun)
	}
	got, err := os.ReadFile(authFile)
	if err != nil {
		t.Fatalf("the shared auth file was removed with the private home: %v", err)
	}
	if string(got) != r.authWriteAtRun {
		t.Errorf("shared auth after the run = %q, want Codex's update %q", got, r.authWriteAtRun)
	}
	if slices.ContainsFunc(r.env, func(v string) bool { return strings.HasPrefix(v, "OPENAI_API_KEY=") }) {
		t.Errorf("env = %v, want no API key for auth.json login", r.env)
	}
}

func TestRunRefusesAnInvalidSharedAuthFileWithoutExposingIt(t *testing.T) {
	authFile := filepath.Join(t.TempDir(), authFileName)
	const contents = "not-json-and-secret"
	if err := os.WriteFile(authFile, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	f := newTestFramework(&recordingRunner{stdout: okStream()}, "/usr/local/bin/grain", WithAuthFile(authFile))

	_, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "not a JSON object") {
		t.Fatalf("Run error = %v, want invalid auth JSON reported", err)
	}
	if strings.Contains(err.Error(), contents) {
		t.Fatalf("Run error exposed auth file contents: %v", err)
	}
}

// A key that cannot be read fails the run rather than silently running
// unauthenticated -- WithAPIKeyFunc's own contract, and the difference
// between "this deployment configured none" and "the store is broken".
func TestRunFailsWhenTheAPIKeyCannotBeRead(t *testing.T) {
	r := &recordingRunner{stdout: okStream()}
	boom := errors.New("secrets database is locked")
	f := newTestFramework(r, "/usr/local/bin/grain", WithAPIKeyFunc(func(context.Context) (string, error) {
		return "", boom
	}))

	_, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the credential error", err)
	}
}

func TestRunReturnsTheFinalAnswerAndItsToolCalls(t *testing.T) {
	r := &recordingRunner{stdout: okStream()}
	f := newTestFramework(r, "/usr/local/bin/grain")

	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.FinalText != "done" {
		t.Errorf("FinalText = %q, want the last assistant message", result.FinalText)
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("ToolCalls = %+v, want one", result.ToolCalls)
	}
	call := result.ToolCalls[0]
	if call.Name != "write_file" || call.Text != "wrote ok.txt" || call.IsError {
		t.Errorf("ToolCalls[0] = %+v", call)
	}
	if call.Arguments["file_path"] != "ok.txt" {
		t.Errorf("ToolCalls[0].Arguments = %+v", call.Arguments)
	}
}

// The granule half of mcpServerArgs: a run with no local directory
// reaches its workspace through the forked mcpserver's SSH transport,
// and a Framework that was never told how to do that must say so rather
// than start a run whose tools reach nothing.
func TestRunRejectsAGranuleVMWithNoSSHConfig(t *testing.T) {
	r := &recordingRunner{stdout: okStream()}
	f := newTestFramework(r, "/usr/local/bin/grain")

	_, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", GranuleVM: "grain-sandbox-1"})
	if err == nil || !strings.Contains(err.Error(), "WithGranuleSSH") {
		t.Fatalf("err = %v, want it to name the missing granule SSH config", err)
	}
}

func TestRunPointsTheMCPServerAtAGranuleVM(t *testing.T) {
	r := &recordingRunner{stdout: okStream()}
	f := newTestFramework(r, "/usr/local/bin/grain", WithGranuleSSH("grain", "", "/home/grain/work"))

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", GranuleVM: "grain-sandbox-1"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, want := range []string{`"-granule-vm","grain-sandbox-1"`, `"-ssh-user","grain"`, `"-workspace","/home/grain/work"`} {
		if !strings.Contains(r.mcpArgs, want) {
			t.Errorf("config = %q, want it to carry %s", r.mcpArgs, want)
		}
	}
	if strings.Contains(r.mcpArgs, "-exec-key") {
		t.Errorf("config = %q, want no -exec-key when none is configured", r.mcpArgs)
	}
}

// pull_request_status and open_pull_request are turned on by what the
// forked mcpserver is passed, and half of either pair is worse than
// none: see pullRequestArgs and grainServerArgs.
func TestRunPassesThePullRequestAndDaemonArgsOnlyWhenComplete(t *testing.T) {
	root := t.TempDir()
	r := &recordingRunner{stdout: okStream()}
	f := newTestFramework(r, "/usr/local/bin/grain",
		WithGitHubAccess("/var/lib/grain/data", "github.com", false),
		WithGrainServer("http://127.0.0.1:8420"))

	// A task with a repo and a branch, and a run with a task id: every
	// argument present.
	if _, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "go", SandboxRoot: root, Repo: "bwsalmon/grain", Branch: "grain/task-1", TaskID: "task-1",
		GitCredential: "grain-team-default",
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, want := range []string{
		`"-data-dir","/var/lib/grain/data"`, `"-pr-repo","bwsalmon/grain"`, `"-pr-branch","grain/task-1"`,
		// The credential the branch is pushed with, by name, so the
		// subprocess reads GitHub as the same account rather than off a
		// ladder that may name nothing (agent.RunConfig.GitCredential).
		`"-pr-credential","grain-team-default"`,
		`"-server","http://127.0.0.1:8420"`, `"-task","task-1"`,
	} {
		if !strings.Contains(r.mcpArgs, want) {
			t.Errorf("config = %q, want it to carry %s", r.mcpArgs, want)
		}
	}

	// An ordinary run carries no -pr-repair: that flag is what tells
	// base_branch_status to send a merge queue repair at breakage it
	// inherited (agent.RunConfig.Repair).
	if strings.Contains(r.mcpArgs, "-pr-repair") {
		t.Errorf("config = %q, want no -pr-repair for an ordinary run", r.mcpArgs)
	}
	rRepair := &recordingRunner{stdout: okStream()}
	fRepair := newTestFramework(rRepair, "/usr/local/bin/grain",
		WithGitHubAccess("/var/lib/grain/data", "github.com", false))
	if _, err := fRepair.Run(context.Background(), agent.RunConfig{
		Prompt: "go", SandboxRoot: root, Repo: "bwsalmon/grain", Branch: "grain/task-1", Repair: true,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(rRepair.mcpArgs, "-pr-repair") {
		t.Errorf("config = %q, want it to carry -pr-repair for a repair run", rRepair.mcpArgs)
	}

	// A task with no repo attached, which is a real case: the CI half
	// drops out whole rather than half-configured.
	r2 := &recordingRunner{stdout: okStream()}
	f2 := newTestFramework(r2, "/usr/local/bin/grain", WithGitHubAccess("/var/lib/grain/data", "github.com", false))
	if _, err := f2.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: root}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, unwanted := range []string{"-data-dir", "-pr-repo", "-server", "-task"} {
		if strings.Contains(r2.mcpArgs, unwanted) {
			t.Errorf("config = %q, want no %s for a task with no repo and no id", r2.mcpArgs, unwanted)
		}
	}
}

// The run's own wall-clock deadline reaches the forked mcpserver too,
// which is what lets its tool results tell the run how long it has left
// (mcp.Registry.AnnounceDeadline). Asserted here as well as in
// agent/claude's own test, since each framework builds these arguments
// separately.
func TestRunPassesTheRunsDeadlineToTheMCPServer(t *testing.T) {
	root := t.TempDir()
	r := &recordingRunner{stdout: okStream()}
	f := newTestFramework(r, "/usr/local/bin/grain")

	ctx, cancel := context.WithDeadline(context.Background(),
		time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC))
	defer cancel()
	if _, err := f.Run(ctx, agent.RunConfig{Prompt: "go", SandboxRoot: root}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(r.mcpArgs, `"-run-deadline","2026-03-04T05:06:07Z"`) {
		t.Errorf("config = %q, want the ctx's own deadline passed on", r.mcpArgs)
	}

	// No deadline on the ctx, no flag: a run bounded some other way is
	// not told a moment nobody set.
	r2 := &recordingRunner{stdout: okStream()}
	f2 := newTestFramework(r2, "/usr/local/bin/grain")
	if _, err := f2.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: root}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(r2.mcpArgs, "-run-deadline") {
		t.Errorf("config = %q, want no -run-deadline for a ctx that has none", r2.mcpArgs)
	}
}

// CanOpenPullRequest is read by orchestrator.BuildPrompt to decide
// whether to tell a run about a tool it may not have.
func TestCanOpenPullRequestFollowsWithGrainServer(t *testing.T) {
	if newTestFramework(&recordingRunner{}, "grain").CanOpenPullRequest() {
		t.Error("CanOpenPullRequest with no daemon URL = true, want false")
	}
	if !newTestFramework(&recordingRunner{}, "grain", WithGrainServer("http://127.0.0.1:8420")).CanOpenPullRequest() {
		t.Error("CanOpenPullRequest with a daemon URL = false, want true")
	}
}

// The live mirror RunConfig.TranscriptPath asks for: readable while the
// run is still going, which is the whole reason it exists.
func TestRunMirrorsTheRawStreamToTheTranscriptPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run-1")
	r := &recordingRunner{stdout: okStream()}
	f := newTestFramework(r, "/usr/local/bin/grain")

	if _, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "go", SandboxRoot: t.TempDir(), TranscriptPath: path,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the mirrored transcript: %v", err)
	}
	if string(data) != okStream() {
		t.Errorf("mirrored transcript = %q, want codex's own stream verbatim", data)
	}
	if got := PartialTranscript(string(data)); !strings.Contains(got, "done") {
		t.Errorf("PartialTranscript = %q, want the narrative so far", got)
	}
}

// The mirror is a run's raw tool output, so nobody but the daemon's own
// user may read it.
func TestRunCreatesTheTranscriptOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run-1")
	f := newTestFramework(&recordingRunner{stdout: okStream()}, "/usr/local/bin/grain")

	if _, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "go", SandboxRoot: t.TempDir(), TranscriptPath: path,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat transcript: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("transcript mode = %o, want 600", got)
	}
}

// A failed subprocess still owes its caller whatever the run managed to
// do -- agent.Framework's own contract, and the reason partialResult
// exists.
func TestRunReturnsWhatARunDidBeforeItFailed(t *testing.T) {
	partial := strings.Join([]string{
		`{"type":"item.completed","item":{"id":"i1","item_type":"mcp_tool_call",` +
			`"server":"grain-sandbox","tool":"run_command","status":"completed","result":"pushed"}}`,
	}, "\n")
	r := &recordingRunner{stdout: partial, err: errors.New("exit status 1")}
	f := newTestFramework(r, "/usr/local/bin/grain")

	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	if err == nil {
		t.Fatal("Run of a failed subprocess returned no error")
	}
	if result == nil {
		t.Fatal("Result = nil for a run that had already called a tool; the work would be stranded")
	}
	if len(result.ToolCalls) != 1 || result.ToolCalls[0].Name != "run_command" {
		t.Errorf("ToolCalls = %+v, want the call the run made before it failed", result.ToolCalls)
	}
}

// ...and a run that never said anything gets a nil Result, which is the
// line agent.Framework draws between "did nothing" and "never started".
func TestRunReturnsNoResultForARunThatNeverSpoke(t *testing.T) {
	r := &recordingRunner{stdout: "", err: errors.New("exec: \"codex\": executable file not found in $PATH")}
	f := newTestFramework(r, "/usr/local/bin/grain")

	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	if err == nil {
		t.Fatal("Run with no binary returned no error")
	}
	if result != nil {
		t.Errorf("Result = %+v for a run that never started, want nil", result)
	}
}

func TestRunRequiresASandbox(t *testing.T) {
	f := newTestFramework(&recordingRunner{stdout: okStream()}, "/usr/local/bin/grain")
	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go"}); err == nil {
		t.Fatal("Run with neither a sandbox root nor a granule VM succeeded")
	}
}

// TestRunReportsCodexsOwnErrorAlongsideItsExitStatus is the same pinning
// agent/antigravity's own test does, for the same failure shape: codex
// reports a turn it could not finish in the stream and then exits
// non-zero, and reporting only the exit status left the daemon's log
// with "running codex: exit status 1" -- no cause named, and no capture
// left to read it out of once the run finished.
func TestRunReportsCodexsOwnErrorAlongsideItsExitStatus(t *testing.T) {
	exitErr := errors.New("exit status 1 (stderr: )")
	r := &recordingRunner{
		stdout: strings.Join([]string{
			`{"type":"item.completed","item":{"id":"i1","item_type":"mcp_tool_call","server":"grain-sandbox",` +
				`"tool":"run_command","status":"completed","result":"pushed"}}`,
			`{"type":"turn.failed","error":{"message":"stream disconnected before completion"}}`,
		}, "\n"),
		err: exitErr,
	}
	f := newTestFramework(r, "/usr/local/bin/grain")

	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	if err == nil {
		t.Fatal("Run err = nil, want the failure")
	}
	if !strings.Contains(err.Error(), "stream disconnected") {
		t.Errorf("Run err = %q, want codex's own account of why the run failed", err)
	}
	if !strings.Contains(err.Error(), "exit status 1") {
		t.Errorf("Run err = %q, want the exit status kept alongside it", err)
	}
	if !errors.Is(err, exitErr) {
		t.Errorf("Run err = %q, want the subprocess's own error still wrapped in it", err)
	}
	if result == nil || len(result.ToolCalls) != 1 {
		t.Fatalf("Result = %+v, want the work the run did before it failed", result)
	}
}

// A codex that died before reporting anything has left nothing but its
// exit status, and that is what Run reports.
func TestRunReportsTheExitStatusAloneWhenCodexSaidNothing(t *testing.T) {
	r := &recordingRunner{err: errors.New("signal: killed")}
	f := newTestFramework(r, "/usr/local/bin/grain")

	_, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	if err == nil {
		t.Fatal("Run err = nil, want the failure")
	}
	if !strings.Contains(err.Error(), "codex: running codex: signal: killed") {
		t.Errorf("Run err = %q, want the subprocess's own failure named", err)
	}
}
