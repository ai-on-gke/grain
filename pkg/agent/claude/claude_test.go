package claude

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/mcp"
)

// fakeRunner scripts a single canned stream-json transcript and records
// exactly how it was invoked, so Run's arg-building, mcp-config plumbing,
// and transcript parsing can all be exercised without a real claude binary
// or a live Claude Code credential.
type fakeRunner struct {
	stdout       string
	err          error
	gotArgs      []string
	gotStdin     string
	gotEnv       []string
	gotMCPConfig []byte
	onRun        func(args []string, env []string)
}

func (f *fakeRunner) Run(_ context.Context, args []string, stdin string, env []string, tee io.Writer) (string, error) {
	f.gotArgs = args
	f.gotStdin = stdin
	f.gotEnv = env
	if f.onRun != nil {
		f.onRun(args, env)
	}
	if path := argValue(args, "--mcp-config"); path != "" {
		if data, err := os.ReadFile(path); err == nil {
			f.gotMCPConfig = data
		}
	}
	if tee != nil {
		io.WriteString(tee, f.stdout)
	}
	return f.stdout, f.err
}

func argValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func streamJSONLine(t *testing.T, v map[string]any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestRunReturnsFinalTextWhenNoToolCallsAreMade(t *testing.T) {
	fake := &fakeRunner{stdout: strings.Join([]string{
		streamJSONLine(t, map[string]any{
			"type": "assistant",
			"message": map[string]any{
				"content": []map[string]any{{"type": "text", "text": "thinking"}},
			},
		}),
		streamJSONLine(t, map[string]any{"type": "result", "result": "all done"}),
	}, "\n")}
	f := newFramework(fake, "mcpserver-path")

	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "say hi", SandboxRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalText != "all done" {
		t.Errorf("FinalText = %q", result.FinalText)
	}
	if len(result.ToolCalls) != 0 {
		t.Errorf("ToolCalls = %v, want none", result.ToolCalls)
	}
}

func TestRunPairsToolUseWithItsToolResult(t *testing.T) {
	fake := &fakeRunner{stdout: strings.Join([]string{
		streamJSONLine(t, map[string]any{
			"type": "assistant",
			"message": map[string]any{
				"content": []map[string]any{{
					"type": "tool_use", "id": "call-1", "name": "write_file",
					"input": map[string]any{"file_path": "out.txt", "content": "PONG"},
				}},
			},
		}),
		streamJSONLine(t, map[string]any{
			"type": "user",
			"message": map[string]any{
				"content": []map[string]any{{
					"type": "tool_result", "tool_use_id": "call-1", "content": "Wrote out.txt",
				}},
			},
		}),
		streamJSONLine(t, map[string]any{"type": "result", "result": "wrote it"}),
	}, "\n")}
	f := newFramework(fake, "mcpserver-path")

	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "write PONG to out.txt", SandboxRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalText != "wrote it" {
		t.Errorf("FinalText = %q", result.FinalText)
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("ToolCalls = %+v", result.ToolCalls)
	}
	call := result.ToolCalls[0]
	if call.Name != "write_file" || call.Text != "Wrote out.txt" || call.IsError {
		t.Errorf("ToolCalls[0] = %+v", call)
	}
	if call.Arguments["file_path"] != "out.txt" {
		t.Errorf("ToolCalls[0].Arguments = %+v", call.Arguments)
	}
}

func TestRunReportsToolResultContentGivenAsBlocksNotAPlainString(t *testing.T) {
	fake := &fakeRunner{stdout: strings.Join([]string{
		streamJSONLine(t, map[string]any{
			"type": "assistant",
			"message": map[string]any{
				"content": []map[string]any{{
					"type": "tool_use", "id": "call-1", "name": "run_command",
					"input": map[string]any{"command": "false"},
				}},
			},
		}),
		streamJSONLine(t, map[string]any{
			"type": "user",
			"message": map[string]any{
				"content": []map[string]any{{
					"type": "tool_result", "tool_use_id": "call-1", "is_error": true,
					"content": []map[string]any{{"type": "text", "text": "exit=1"}},
				}},
			},
		}),
		streamJSONLine(t, map[string]any{"type": "result", "result": "done"}),
	}, "\n")}
	f := newFramework(fake, "mcpserver-path")

	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "x", SandboxRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ToolCalls) != 1 || result.ToolCalls[0].Text != "exit=1" || !result.ToolCalls[0].IsError {
		t.Fatalf("ToolCalls = %+v", result.ToolCalls)
	}
}

func TestRunBuildsAHumanReadableTranscript(t *testing.T) {
	fake := &fakeRunner{stdout: strings.Join([]string{
		streamJSONLine(t, map[string]any{
			"type": "assistant",
			"message": map[string]any{
				"content": []map[string]any{{"type": "thinking", "thinking": "let me check the file first"}},
			},
		}),
		streamJSONLine(t, map[string]any{
			"type": "assistant",
			"message": map[string]any{
				"content": []map[string]any{{
					"type": "tool_use", "id": "call-1", "name": "read_file",
					"input": map[string]any{"file_path": "out.txt"},
				}},
			},
		}),
		streamJSONLine(t, map[string]any{
			"type": "user",
			"message": map[string]any{
				"content": []map[string]any{{
					"type": "tool_result", "tool_use_id": "call-1", "content": "PONG",
				}},
			},
		}),
		streamJSONLine(t, map[string]any{
			"type": "assistant",
			"message": map[string]any{
				"content": []map[string]any{{"type": "text", "text": "found it"}},
			},
		}),
		streamJSONLine(t, map[string]any{"type": "result", "result": "done"}),
	}, "\n")}
	f := newFramework(fake, "mcpserver-path")

	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "x", SandboxRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"let me check the file first", "read_file", "out.txt", "PONG", "found it"} {
		if !strings.Contains(result.Transcript, want) {
			t.Errorf("Transcript = %q, want it to contain %q", result.Transcript, want)
		}
	}
	// Chronological: the thinking block precedes the tool call it explains,
	// which precedes the result it read, which precedes the final text
	// that used it.
	thinkAt := strings.Index(result.Transcript, "let me check the file first")
	toolAt := strings.Index(result.Transcript, "read_file")
	resultAt := strings.Index(result.Transcript, "PONG")
	textAt := strings.Index(result.Transcript, "found it")
	if !(thinkAt < toolAt && toolAt < resultAt && resultAt < textAt) {
		t.Errorf("Transcript not in chronological order: %q", result.Transcript)
	}
}

func TestRunReportsAnErrorResultEvent(t *testing.T) {
	fake := &fakeRunner{stdout: streamJSONLine(t, map[string]any{
		"type": "result", "subtype": "error_max_turns", "is_error": true, "result": "",
	})}
	f := newFramework(fake, "mcpserver-path")

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "x", SandboxRoot: t.TempDir()}); err == nil {
		t.Fatal("expected an error for an error_max_turns result event")
	}
}

func TestRunFailsWithoutSandboxRoot(t *testing.T) {
	f := newFramework(&fakeRunner{}, "mcpserver-path")
	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "x"}); err == nil {
		t.Fatal("expected an error for a missing SandboxRoot and GranuleVM")
	}
}

func TestRunFailsWithGranuleVMButNoGranuleSSHConfig(t *testing.T) {
	f := newFramework(&fakeRunner{}, "mcpserver-path")
	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "x", GranuleVM: "g-1-1"}); err == nil {
		t.Fatal("expected an error for a GranuleVM run with no WithGranuleSSH config")
	}
}

func TestRunWritesMCPConfigPointingAtTheGranuleVM(t *testing.T) {
	fake := &fakeRunner{stdout: streamJSONLine(t, map[string]any{"type": "result", "result": "ok"})}
	f := newFramework(fake, "/path/to/grain", WithGranuleSSH("root", "/images/key", "/workspace"))

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "x", GranuleVM: "g-1-1"}); err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(fake.gotMCPConfig, &cfg); err != nil {
		t.Fatalf("mcp-config was not valid JSON: %v (%s)", err, fake.gotMCPConfig)
	}
	server, ok := cfg.MCPServers["grain-sandbox"]
	if !ok {
		t.Fatalf("mcp-config missing grain-sandbox server: %+v", cfg)
	}
	want := []string{"mcpserver", "-granule-vm", "g-1-1", "-ssh-user", "root", "-workspace", "/workspace", "-exec-key", "/images/key"}
	if len(server.Args) != len(want) {
		t.Fatalf("args = %v, want %v", server.Args, want)
	}
	for i, w := range want {
		if server.Args[i] != w {
			t.Errorf("args[%d] = %q, want %q (full args %v)", i, server.Args[i], w, server.Args)
		}
	}
}

// The forked mcpserver has to be told which repo and branch it may read
// CI for, or pull_request_status has nothing to answer with. The repo
// and branch come off RunConfig (orchestrator.RunDispatch sets them from
// the task) and the data directory off the Framework, and both halves
// have to arrive for either to be passed.
func TestRunPassesTheRunsOwnRepoAndBranchToTheMCPServer(t *testing.T) {
	mcpArgs := func(t *testing.T, f *Framework, cfg agent.RunConfig, fake *fakeRunner) []string {
		t.Helper()
		if _, err := f.Run(context.Background(), cfg); err != nil {
			t.Fatal(err)
		}
		var parsed struct {
			MCPServers map[string]struct {
				Args []string `json:"args"`
			} `json:"mcpServers"`
		}
		if err := json.Unmarshal(fake.gotMCPConfig, &parsed); err != nil {
			t.Fatalf("mcp-config was not valid JSON: %v (%s)", err, fake.gotMCPConfig)
		}
		return parsed.MCPServers["grain-sandbox"].Args
	}

	t.Run("configured", func(t *testing.T) {
		fake := &fakeRunner{stdout: streamJSONLine(t, map[string]any{"type": "result", "result": "ok"})}
		f := newFramework(fake, "/path/to/grain", WithGitHubAccess("/data", "github.example", true))
		args := mcpArgs(t, f, agent.RunConfig{
			Prompt: "x", SandboxRoot: t.TempDir(),
			Repo: "acme/widgets", Branch: "grain/task-9",
			GitCredential: "grain-team-acme",
		}, fake)

		for _, want := range [][2]string{
			{"-data-dir", "/data"},
			{"-pr-repo", "acme/widgets"},
			{"-pr-branch", "grain/task-9"},
			{"-github-host", "github.example"},
			// The credential the branch is pushed with, by name, so the
			// subprocess reads GitHub as the same account rather than
			// off a ladder that may name nothing (agent.RunConfig.GitCredential).
			{"-pr-credential", "grain-team-acme"},
		} {
			if !argsHave(args, want[0], want[1]) {
				t.Errorf("args = %v, want %s %s", args, want[0], want[1])
			}
		}
		if !slices.Contains(args, "-github-insecure-http") {
			t.Errorf("args = %v, want -github-insecure-http passed through", args)
		}
	})

	// -pr-repair rides on the same triple, and only for the run it is
	// true of: it is what makes base_branch_status tell a merge queue
	// repair to fix breakage it inherited rather than to leave it alone
	// (mcp.PullRequestScope.Repair).
	t.Run("a repair run", func(t *testing.T) {
		fake := &fakeRunner{stdout: streamJSONLine(t, map[string]any{"type": "result", "result": "ok"})}
		f := newFramework(fake, "/path/to/grain", WithGitHubAccess("/data", "github.com", false))
		args := mcpArgs(t, f, agent.RunConfig{
			Prompt: "x", SandboxRoot: t.TempDir(),
			Repo: "acme/widgets", Branch: "grain/task-9", Repair: true,
		}, fake)
		if !slices.Contains(args, "-pr-repair") {
			t.Errorf("args = %v, want -pr-repair for a repair run", args)
		}

		fake2 := &fakeRunner{stdout: streamJSONLine(t, map[string]any{"type": "result", "result": "ok"})}
		f2 := newFramework(fake2, "/path/to/grain", WithGitHubAccess("/data", "github.com", false))
		ordinary := mcpArgs(t, f2, agent.RunConfig{
			Prompt: "x", SandboxRoot: t.TempDir(),
			Repo: "acme/widgets", Branch: "grain/task-9",
		}, fake2)
		if slices.Contains(ordinary, "-pr-repair") {
			t.Errorf("args = %v, want no -pr-repair for an ordinary run", ordinary)
		}
	})

	// A task with no repo attached is a real case, and half the flags
	// would make mcpserver warn about a misconfiguration that is not one.
	t.Run("no repo on the run", func(t *testing.T) {
		fake := &fakeRunner{stdout: streamJSONLine(t, map[string]any{"type": "result", "result": "ok"})}
		f := newFramework(fake, "/path/to/grain", WithGitHubAccess("/data", "github.com", false))
		args := mcpArgs(t, f, agent.RunConfig{Prompt: "x", SandboxRoot: t.TempDir(), GitCredential: "grain-team-acme"}, fake)
		for _, unwanted := range []string{"-data-dir", "-pr-repo", "-pr-branch", "-pr-credential", "-github-host"} {
			if slices.Contains(args, unwanted) {
				t.Errorf("args = %v, want no %s for a run with no repo", args, unwanted)
			}
		}
	})

	// And a deployment that never called WithGitHubAccess has no
	// credential to read GitHub with, so naming the repo would only
	// produce a warning per run.
	t.Run("no github access on the framework", func(t *testing.T) {
		fake := &fakeRunner{stdout: streamJSONLine(t, map[string]any{"type": "result", "result": "ok"})}
		f := newFramework(fake, "/path/to/grain")
		args := mcpArgs(t, f, agent.RunConfig{
			Prompt: "x", SandboxRoot: t.TempDir(),
			Repo: "acme/widgets", Branch: "grain/task-9",
		}, fake)
		if slices.Contains(args, "-pr-repo") {
			t.Errorf("args = %v, want no -pr-repo without WithGitHubAccess", args)
		}
	})
}

// The run's own wall-clock deadline reaches its tools the same way its
// repo does: through the forked mcpserver's arguments. Without it a
// server has no way to tell the run how long it has left, and the prompt
// -- read once, at turn 1 -- is the only warning it ever gets.
func TestRunPassesTheRunsDeadlineToTheMCPServer(t *testing.T) {
	mcpArgs := func(t *testing.T, ctx context.Context) []string {
		t.Helper()
		fake := &fakeRunner{stdout: streamJSONLine(t, map[string]any{"type": "result", "result": "ok"})}
		f := newFramework(fake, "/path/to/grain")
		if _, err := f.Run(ctx, agent.RunConfig{Prompt: "x", SandboxRoot: t.TempDir()}); err != nil {
			t.Fatal(err)
		}
		var parsed struct {
			MCPServers map[string]struct {
				Args []string `json:"args"`
			} `json:"mcpServers"`
		}
		if err := json.Unmarshal(fake.gotMCPConfig, &parsed); err != nil {
			t.Fatalf("mcp-config was not valid JSON: %v (%s)", err, fake.gotMCPConfig)
		}
		return parsed.MCPServers["grain-sandbox"].Args
	}

	at := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	ctx, cancel := context.WithDeadline(context.Background(), at)
	defer cancel()
	if args := mcpArgs(t, ctx); !argsHave(args, "-run-deadline", "2026-03-04T05:06:07Z") {
		t.Errorf("args = %v, want -run-deadline carrying the ctx's own deadline", args)
	}

	// A caller that bounds the run some other way, or not at all, passes
	// no flag rather than a made-up deadline.
	if args := mcpArgs(t, context.Background()); slices.Contains(args, "-run-deadline") {
		t.Errorf("args = %v, want no -run-deadline for a ctx that has none", args)
	}
}

// The repositories a proposal this run makes may be filed against reach
// its tools the same way, and for a reason of the same kind: the process
// serving propose_task has no store to ask, so a description that is not
// told which repositories those are can only state the rule and leave
// the run to guess (pkg/cli/mcpserver.go's -propose-repo).
func TestRunPassesTheProposableReposToTheMCPServer(t *testing.T) {
	mcpArgs := func(t *testing.T, cfg agent.RunConfig) []string {
		t.Helper()
		fake := &fakeRunner{stdout: streamJSONLine(t, map[string]any{"type": "result", "result": "ok"})}
		f := newFramework(fake, "/path/to/grain")
		cfg.Prompt, cfg.SandboxRoot = "x", t.TempDir()
		if _, err := f.Run(context.Background(), cfg); err != nil {
			t.Fatal(err)
		}
		var parsed struct {
			MCPServers map[string]struct {
				Args []string `json:"args"`
			} `json:"mcpServers"`
		}
		if err := json.Unmarshal(fake.gotMCPConfig, &parsed); err != nil {
			t.Fatalf("mcp-config was not valid JSON: %v (%s)", err, fake.gotMCPConfig)
		}
		return parsed.MCPServers["grain-sandbox"].Args
	}

	args := mcpArgs(t, agent.RunConfig{ProposeRepos: []string{"acme/widgets", "acme/docs"}})
	for _, want := range []string{"acme/widgets", "acme/docs"} {
		if !argsHave(args, "-propose-repo", want) {
			t.Errorf("args = %v, want -propose-repo %s", args, want)
		}
	}

	// A run given none -- a deployment whose store said nothing, or a
	// caller with no store at all -- passes no flag, leaving that
	// description as it reads without this.
	if bare := mcpArgs(t, agent.RunConfig{}); slices.Contains(bare, "-propose-repo") {
		t.Errorf("args = %v, want no -propose-repo for a run given no repos", bare)
	}
}

// argsHave reports whether args carries name followed by value.
func argsHave(args []string, name, value string) bool {
	for i, a := range args {
		if a == name && i+1 < len(args) && args[i+1] == value {
			return true
		}
	}
	return false
}

func TestRunPrefersSandboxRootOverGranuleVMWhenBothAreSet(t *testing.T) {
	fake := &fakeRunner{stdout: streamJSONLine(t, map[string]any{"type": "result", "result": "ok"})}
	root := t.TempDir()
	f := newFramework(fake, "mcpserver-path", WithGranuleSSH("root", "/images/key", "/workspace"))

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "x", SandboxRoot: root, GranuleVM: "g-1-1"}); err != nil {
		t.Fatal(err)
	}
	if v := argValue(fake.gotArgs, "--mcp-config"); v == "" {
		t.Fatal("--mcp-config not passed")
	}
}

func TestRunFailsWithoutMCPServerPath(t *testing.T) {
	f := newFramework(&fakeRunner{}, "")
	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "x", SandboxRoot: t.TempDir()}); err == nil {
		t.Fatal("expected an error for a missing grainBinaryPath")
	}
}

func TestRunPassesThePromptOverStdinNotArgv(t *testing.T) {
	fake := &fakeRunner{stdout: streamJSONLine(t, map[string]any{"type": "result", "result": "ok"})}
	f := newFramework(fake, "mcpserver-path")

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "untrusted issue body", SandboxRoot: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if fake.gotStdin != "untrusted issue body" {
		t.Errorf("stdin = %q, want the prompt", fake.gotStdin)
	}
	for _, a := range fake.gotArgs {
		if strings.Contains(a, "untrusted issue body") {
			t.Errorf("prompt leaked into argv: %v", fake.gotArgs)
		}
	}
}

func TestRunEmptiesTheNativeToolRoster(t *testing.T) {
	fake := &fakeRunner{stdout: streamJSONLine(t, map[string]any{"type": "result", "result": "ok"})}
	f := newFramework(fake, "mcpserver-path")

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "x", SandboxRoot: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if v := argValue(fake.gotArgs, "--tools"); v != "" {
		t.Errorf("--tools = %q, want empty", v)
	}
	found := false
	for _, a := range fake.gotArgs {
		if a == "--strict-mcp-config" {
			found = true
		}
	}
	if !found {
		t.Errorf("--strict-mcp-config missing from args: %v", fake.gotArgs)
	}
}

func TestRunCleansUpTheMCPConfigFile(t *testing.T) {
	fake := &fakeRunner{stdout: streamJSONLine(t, map[string]any{"type": "result", "result": "ok"})}
	f := newFramework(fake, "mcpserver-path")

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "x", SandboxRoot: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	configPath := argValue(fake.gotArgs, "--mcp-config")
	if configPath == "" {
		t.Fatal("--mcp-config not passed")
	}
	if _, err := os.Stat(configPath); err == nil {
		t.Errorf("mcp-config file %s was not cleaned up after Run", configPath)
	}
}

func TestRunWritesMCPConfigPointingAtTheServerBinaryAndSandboxRoot(t *testing.T) {
	fake := &fakeRunner{stdout: streamJSONLine(t, map[string]any{"type": "result", "result": "ok"})}
	root := t.TempDir()
	f := newFramework(fake, "/path/to/grain")

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "x", SandboxRoot: root}); err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(fake.gotMCPConfig, &cfg); err != nil {
		t.Fatalf("mcp-config was not valid JSON: %v (%s)", err, fake.gotMCPConfig)
	}
	server, ok := cfg.MCPServers["grain-sandbox"]
	if !ok {
		t.Fatalf("mcp-config missing grain-sandbox server: %+v", cfg)
	}
	if server.Command != "/path/to/grain" {
		t.Errorf("command = %q", server.Command)
	}
	// "mcpserver" comes first: claude forks this exact command, and the
	// grain binary itself dispatches on that leading argument (main.go)
	// to run as an MCP server rather than the task CLI.
	if len(server.Args) != 3 || server.Args[0] != "mcpserver" || server.Args[1] != "-sandbox-root" || server.Args[2] != root {
		t.Errorf("args = %v", server.Args)
	}
}

// Exactly the published vocabulary, in claude's own spelling: nothing
// dropped on the way through this package and nothing invented here.
//
// Which tools that vocabulary holds is agent.PublishedToolNames' own
// business, and so is why it holds tools this particular run will never
// be offered -- --allowedTools filters what the server advertises rather
// than adding to it. All that is claude's is the spelling, and the two
// halves below are the two ways to get it wrong: a name the list omits
// and a name only this list has.
//
// Derived from that list rather than counted against a literal, which is
// what this used to be (grain/task-398). A count is weaker -- swapping
// one tool for another keeps it -- and it is a number every task that
// adds a tool has to edit, on a line none of their changes are about, so
// two branches adding a tool at the same time conflicted here every
// time. Nothing in this test has to be edited to add a tool now; the
// membership list below is appended to, which no two authors collide on.
func TestAllowedToolsNamesEveryGrainSandboxTool(t *testing.T) {
	names := allowedTools()
	var published []string
	for _, n := range agent.PublishedToolNames() {
		published = append(published, mcp.QualifiedToolName(n))
	}
	for _, n := range published {
		if !slices.Contains(names, n) {
			t.Errorf("allowedTools() is missing %s, which grain mcpserver may advertise", n)
		}
	}
	for _, n := range names {
		if !slices.Contains(published, n) {
			t.Errorf("allowedTools() names %s, which no server advertises", n)
		}
	}
	for _, n := range names {
		if !strings.HasPrefix(n, "mcp__grain-sandbox__") {
			t.Errorf("tool name %q missing mcp__grain-sandbox__ prefix", n)
		}
	}
	// --strict-mcp-config refuses any tool this list omits, so a tool the
	// server may advertise and this list may not name is a run that dies
	// on its first call to it.
	for _, tool := range []string{"pull_request_status", "base_branch_status", "wait_for_checks",
		"list_ci_runs", "get_ci_log",
		"open_pull_request", "recreate_sandbox",
		"update_status", "chat", "update_memory", "list_memory_documents", "read_memory_document",
		"read_cluster_stats", "write_release_notes",
		"read_grain_log",
		"read_deployment_health", "read_deployment_state",
		"list_bootstrap_playbooks", "read_bootstrap_playbook",
		"deployment_upgrade_status", "upgrade_deployment", "cancel_deployment_upgrade"} {
		if !slices.Contains(names, mcp.QualifiedToolName(tool)) {
			t.Errorf("allowedTools() = %v, want %s admitted", names, tool)
		}
	}
}

// The daemon's address and the run's task id are what the forked
// mcpserver needs to offer open_pull_request at all, and they only travel
// as its own arguments.
func TestRunPassesTheGrainServerAndTaskToTheMCPServer(t *testing.T) {
	root := t.TempDir()
	fake := &fakeRunner{stdout: streamJSONLine(t, map[string]any{"type": "result", "result": "ok"})}
	f := newFramework(fake, "/path/to/grain", WithGrainServer("http://127.0.0.1:8420"))

	if _, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "go", SandboxRoot: root, TaskID: "t1",
	}); err != nil {
		t.Fatal(err)
	}
	args := mcpConfigArgs(t, fake.gotMCPConfig)
	if !argsHave(args, "-server", "http://127.0.0.1:8420") || !argsHave(args, "-task", "t1") {
		t.Errorf("mcpserver args = %v, want -server and -task among them", args)
	}
}

// Both or neither: a task id with no daemon to ask names a question with
// nowhere to send it, and mcpserver rejects either half on its own.
func TestRunOmitsTheGrainServerWhenEitherHalfIsMissing(t *testing.T) {
	for _, tt := range []struct {
		name   string
		opts   []Option
		taskID string
	}{
		{"no daemon configured", nil, "t1"},
		{"no task id", []Option{WithGrainServer("http://127.0.0.1:8420")}, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeRunner{stdout: streamJSONLine(t, map[string]any{"type": "result", "result": "ok"})}
			f := newFramework(fake, "/path/to/grain", tt.opts...)

			if _, err := f.Run(context.Background(), agent.RunConfig{
				Prompt: "go", SandboxRoot: t.TempDir(), TaskID: tt.taskID,
			}); err != nil {
				t.Fatal(err)
			}
			args := mcpConfigArgs(t, fake.gotMCPConfig)
			if slices.Contains(args, "-server") || slices.Contains(args, "-task") {
				t.Errorf("mcpserver args = %v, want neither -server nor -task", args)
			}
		})
	}
}

// A task's tool-granting capabilities travel to the forked mcpserver as
// its own arguments, since that process is where those tools are
// actually served from -- a Framework driving a CLI can hand it nothing
// else. Every grant the run holds, not just the first: the configuration
// agent holds both of these at once.
func TestRunPassesTheGrantsToTheMCPServer(t *testing.T) {
	fake := &fakeRunner{stdout: streamJSONLine(t, map[string]any{"type": "result", "result": "ok"})}
	f := newFramework(fake, "/path/to/grain")

	if _, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "go", SandboxRoot: t.TempDir(),
		Grants:         []string{"self-debug", "bootstrap-playbooks"},
		GrainSourceDir: "/usr/local/share/grain/src",
	}); err != nil {
		t.Fatal(err)
	}
	args := mcpConfigArgs(t, fake.gotMCPConfig)
	if !argsHave(args, "-grant", "self-debug") ||
		!argsHave(args, "-grant", "bootstrap-playbooks") ||
		!argsHave(args, "-grain-src-dir", "/usr/local/share/grain/src") {
		t.Errorf("mcpserver args = %v, want both -grant pairs and -grain-src-dir among them", args)
	}
}

// A task without a grant gets a roster with nothing of grain's own
// insides in it, whatever the deployment happens to know about where its
// source lives.
func TestRunOmitsTheGrantsWithoutOne(t *testing.T) {
	fake := &fakeRunner{stdout: streamJSONLine(t, map[string]any{"type": "result", "result": "ok"})}
	f := newFramework(fake, "/path/to/grain")

	if _, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "go", SandboxRoot: t.TempDir(), GrainSourceDir: "/usr/local/share/grain/src",
	}); err != nil {
		t.Fatal(err)
	}
	args := mcpConfigArgs(t, fake.gotMCPConfig)
	if slices.Contains(args, "-grant") || slices.Contains(args, "-grain-src-dir") {
		t.Errorf("mcpserver args = %v, want neither -grant nor -grain-src-dir", args)
	}
}

// Whether somebody is sitting with this task travels the same way, and
// is what turns the chat tool on in that process: a run that can hold a
// conversation inside itself rather than one message per dispatch.
func TestRunPassesInteractiveToTheMCPServer(t *testing.T) {
	fake := &fakeRunner{stdout: streamJSONLine(t, map[string]any{"type": "result", "result": "ok"})}
	f := newFramework(fake, "/path/to/grain")

	if _, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "go", SandboxRoot: t.TempDir(), Interactive: true,
	}); err != nil {
		t.Fatal(err)
	}
	if args := mcpConfigArgs(t, fake.gotMCPConfig); !slices.Contains(args, "-interactive") {
		t.Errorf("mcpserver args = %v, want -interactive among them", args)
	}

	// And an ordinary run is not offered a tool that waits for a person.
	fake = &fakeRunner{stdout: streamJSONLine(t, map[string]any{"type": "result", "result": "ok"})}
	f = newFramework(fake, "/path/to/grain")
	if _, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "go", SandboxRoot: t.TempDir(),
	}); err != nil {
		t.Fatal(err)
	}
	if args := mcpConfigArgs(t, fake.gotMCPConfig); slices.Contains(args, "-interactive") {
		t.Errorf("mcpserver args = %v, want no -interactive", args)
	}
}

// And where this run's prompt stopped quoting the task's conversation,
// which is what lets that process tell the run what a person typed after
// it without reading the prompt's own thread back as news (mcp's
// addenda.go).
func TestRunPassesTheAddendaCursorToTheMCPServer(t *testing.T) {
	fake := &fakeRunner{stdout: streamJSONLine(t, map[string]any{"type": "result", "result": "ok"})}
	f := newFramework(fake, "/path/to/grain")

	if _, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "go", SandboxRoot: t.TempDir(), AddendaAfter: 91,
	}); err != nil {
		t.Fatal(err)
	}
	args := mcpConfigArgs(t, fake.gotMCPConfig)
	at := slices.Index(args, "-addenda-after")
	if at < 0 || at+1 >= len(args) || args[at+1] != "91" {
		t.Errorf("mcpserver args = %v, want -addenda-after 91 among them", args)
	}

	// A first dispatch quotes no conversation and passes no cursor.
	fake = &fakeRunner{stdout: streamJSONLine(t, map[string]any{"type": "result", "result": "ok"})}
	f = newFramework(fake, "/path/to/grain")
	if _, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "go", SandboxRoot: t.TempDir(),
	}); err != nil {
		t.Fatal(err)
	}
	if args := mcpConfigArgs(t, fake.gotMCPConfig); slices.Contains(args, "-addenda-after") {
		t.Errorf("mcpserver args = %v, want no -addenda-after", args)
	}
}

// mcpConfigArgs pulls the grain-sandbox server's own argument list out of
// the --mcp-config file Run wrote.
func mcpConfigArgs(t *testing.T, configJSON []byte) []string {
	t.Helper()
	var cfg struct {
		MCPServers map[string]struct {
			Args []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(configJSON, &cfg); err != nil {
		t.Fatalf("mcp-config was not valid JSON: %v (%s)", err, configJSON)
	}
	server, ok := cfg.MCPServers["grain-sandbox"]
	if !ok {
		t.Fatalf("mcp-config missing grain-sandbox server: %+v", cfg)
	}
	return server.Args
}

func TestRunPassesNoModelWhenNoneIsGiven(t *testing.T) {
	fake := &fakeRunner{stdout: streamJSONLine(t, map[string]any{"type": "result", "result": "ok"})}
	f := newFramework(fake, "mcpserver-path")

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "x", SandboxRoot: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(fake.gotArgs, "--model") {
		t.Errorf("args = %v, want no --model: none was named, so claude runs its own default", fake.gotArgs)
	}
}

func TestWithModelOverridesTheDefault(t *testing.T) {
	fake := &fakeRunner{stdout: streamJSONLine(t, map[string]any{"type": "result", "result": "ok"})}
	f := newFramework(fake, "mcpserver-path", WithModel("claude-opus-5"))

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "x", SandboxRoot: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if got := argValue(fake.gotArgs, "--model"); got != "claude-opus-5" {
		t.Errorf("--model = %q, want %q", got, "claude-opus-5")
	}
}

func TestWithMaxTurnsOverridesTheDefault(t *testing.T) {
	fake := &fakeRunner{stdout: streamJSONLine(t, map[string]any{"type": "result", "result": "ok"})}
	f := newFramework(fake, "mcpserver-path", WithMaxTurns(5))

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "x", SandboxRoot: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if got := argValue(fake.gotArgs, "--max-turns"); got != "5" {
		t.Errorf("--max-turns = %q, want 5", got)
	}
}

func TestRunConfigMaxTurnsOverridesTheFrameworkDefault(t *testing.T) {
	fake := &fakeRunner{stdout: streamJSONLine(t, map[string]any{"type": "result", "result": "ok"})}
	f := newFramework(fake, "mcpserver-path", WithMaxTurns(5))

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "x", SandboxRoot: t.TempDir(), MaxTurns: 2}); err != nil {
		t.Fatal(err)
	}
	if got := argValue(fake.gotArgs, "--max-turns"); got != "2" {
		t.Errorf("--max-turns = %q, want 2 (RunConfig should override WithMaxTurns)", got)
	}
}

func TestWithOAuthTokenSetsTheEnvironmentNotArgv(t *testing.T) {
	fake := &fakeRunner{stdout: streamJSONLine(t, map[string]any{"type": "result", "result": "ok"})}
	f := newFramework(fake, "mcpserver-path", WithOAuthToken("secret-token"))

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "x", SandboxRoot: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range fake.gotEnv {
		if e == "CLAUDE_CODE_OAUTH_TOKEN=secret-token" {
			found = true
		}
	}
	if !found {
		t.Errorf("gotEnv = %v, want CLAUDE_CODE_OAUTH_TOKEN=secret-token", fake.gotEnv)
	}
	for _, a := range fake.gotArgs {
		if strings.Contains(a, "secret-token") {
			t.Errorf("token leaked into argv: %v", fake.gotArgs)
		}
	}
}

func TestWithVertexSetsTheEnvironment(t *testing.T) {
	fake := &fakeRunner{stdout: streamJSONLine(t, map[string]any{"type": "result", "result": "ok"})}
	f := newFramework(fake, "mcpserver-path", WithVertex(true))

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "x", SandboxRoot: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	foundVertex := false
	for _, e := range fake.gotEnv {
		if e == "CLAUDE_CODE_USE_VERTEX=1" {
			foundVertex = true
		}
		if strings.HasPrefix(e, "GOOGLE_APPLICATION_CREDENTIALS=") {
			t.Errorf("ambient vertex mode set %q, want no GOOGLE_APPLICATION_CREDENTIALS", e)
		}
	}
	if !foundVertex {
		t.Errorf("gotEnv = %v, want CLAUDE_CODE_USE_VERTEX=1", fake.gotEnv)
	}
}

func TestWithVertexServiceAccountJSONCredentials(t *testing.T) {
	fake := &fakeRunner{stdout: streamJSONLine(t, map[string]any{"type": "result", "result": "ok"})}
	saJSON := `{"type":"service_account","project_id":"test-proj-123","private_key":"dummy"}`
	f := newFramework(fake, "mcpserver-path", WithVertex(true), WithOAuthToken(saJSON))

	var credPath string
	fake.onRun = func(args []string, env []string) {
		for _, e := range env {
			if strings.HasPrefix(e, "GOOGLE_APPLICATION_CREDENTIALS=") {
				credPath = strings.TrimPrefix(e, "GOOGLE_APPLICATION_CREDENTIALS=")
			}
		}
		if credPath != "" {
			data, err := os.ReadFile(credPath)
			if err != nil {
				t.Errorf("reading temp credentials file: %v", err)
			} else if string(data) != saJSON {
				t.Errorf("temp credentials file = %q, want %q", string(data), saJSON)
			}
		}
	}

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "x", SandboxRoot: t.TempDir()}); err != nil {
		t.Fatal(err)
	}

	foundVertex := false
	foundProject := false
	for _, e := range fake.gotEnv {
		if e == "CLAUDE_CODE_USE_VERTEX=1" {
			foundVertex = true
		}
		if e == "ANTHROPIC_VERTEX_PROJECT_ID=test-proj-123" {
			foundProject = true
		}
	}
	if !foundVertex {
		t.Errorf("gotEnv = %v, want CLAUDE_CODE_USE_VERTEX=1", fake.gotEnv)
	}
	if !foundProject {
		t.Errorf("gotEnv = %v, want ANTHROPIC_VERTEX_PROJECT_ID=test-proj-123", fake.gotEnv)
	}
	if credPath == "" {
		t.Errorf("gotEnv = %v, want GOOGLE_APPLICATION_CREDENTIALS set", fake.gotEnv)
	} else if _, err := os.Stat(credPath); !os.IsNotExist(err) {
		t.Errorf("temp credentials file %s was not cleaned up after Run: err = %v", credPath, err)
	}
}

func TestWithVertexFilePathCredentials(t *testing.T) {
	fake := &fakeRunner{stdout: streamJSONLine(t, map[string]any{"type": "result", "result": "ok"})}
	keyFile := filepath.Join(t.TempDir(), "sa-key.json")
	if err := os.WriteFile(keyFile, []byte(`{"type":"service_account","project_id":"file-proj"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	f := newFramework(fake, "mcpserver-path", WithVertex(true), WithOAuthToken(keyFile))

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "x", SandboxRoot: t.TempDir()}); err != nil {
		t.Fatal(err)
	}

	foundCred := false
	foundProject := false
	for _, e := range fake.gotEnv {
		if e == "GOOGLE_APPLICATION_CREDENTIALS="+keyFile {
			foundCred = true
		}
		if e == "ANTHROPIC_VERTEX_PROJECT_ID=file-proj" {
			foundProject = true
		}
	}
	if !foundCred {
		t.Errorf("gotEnv = %v, want GOOGLE_APPLICATION_CREDENTIALS=%s", fake.gotEnv, keyFile)
	}
	if !foundProject {
		t.Errorf("gotEnv = %v, want ANTHROPIC_VERTEX_PROJECT_ID=file-proj", fake.gotEnv)
	}
	if _, err := os.Stat(keyFile); err != nil {
		t.Errorf("pre-existing key file %s was unexpectedly removed: %v", keyFile, err)
	}
}

// wait_for_checks blocks for as long as CI takes, so claude's own cap on
// a tool call has to be moved out past the longest wait that tool will
// ever do -- otherwise a run that asked to wait out a slow build has the
// call killed under it and is told the tool failed.
// withoutEnv removes name for the life of one test and puts back
// whatever was there.
//
// It exists because two of the tests below assert that grain *sets* a
// variable, and both of them silently invert when the process running
// them already has one: mcpToolTimeout and disableAutoMemory
// deliberately stand aside for an operator's own value, so an ambient
// MCP_TOOL_TIMEOUT turns "grain sets the cap" into "grain sets nothing"
// and the test fails describing the opposite of the bug.
//
// That is not hypothetical. Every agent grain dispatches runs inside a
// harness that sets MCP_TOOL_TIMEOUT for its own MCP servers, so `go
// test ./...` from inside one -- which is how most of this repository's
// changes are now written -- failed here while GitHub's runners, which
// set no such thing, stayed green. A test whose result depends on who
// runs it is worse than one that is simply wrong.
//
// t.Setenv cannot express "unset", so this is the plain pair with a
// Cleanup. Like t.Setenv it must not be used from a parallel test, and
// nothing in this file is one.
func withoutEnv(t *testing.T, name string) {
	t.Helper()
	previous, had := os.LookupEnv(name)
	if err := os.Unsetenv(name); err != nil {
		t.Fatalf("unsetting %s: %v", name, err)
	}
	t.Cleanup(func() {
		if had {
			os.Setenv(name, previous)
			return
		}
		os.Unsetenv(name)
	})
}

func TestRunRaisesTheMCPToolTimeoutPastTheLongestWait(t *testing.T) {
	// The cap grain sets is the one an operator has not already set, so
	// this asks the question with nobody else's answer in the way.
	withoutEnv(t, "MCP_TOOL_TIMEOUT")
	fake := &fakeRunner{stdout: streamJSONLine(t, map[string]any{"type": "result", "result": "ok"})}
	f := newFramework(fake, "mcpserver-path")

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "x", SandboxRoot: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	var got string
	for _, e := range fake.gotEnv {
		if value, ok := strings.CutPrefix(e, "MCP_TOOL_TIMEOUT="); ok {
			got = value
		}
	}
	if got == "" {
		t.Fatalf("gotEnv = %v, want an MCP_TOOL_TIMEOUT in it", fake.gotEnv)
	}
	ms, err := strconv.ParseInt(got, 10, 64)
	if err != nil {
		t.Fatalf("MCP_TOOL_TIMEOUT = %q, want milliseconds: %v", got, err)
	}
	if limit := time.Duration(ms) * time.Millisecond; limit <= mcp.MaxWaitForChecksTimeout {
		t.Errorf("MCP_TOOL_TIMEOUT = %v, want more than wait_for_checks' own %v maximum",
			limit, mcp.MaxWaitForChecksTimeout)
	}
}

// An operator who set the cap themselves outranks that default: env
// entries are appended after os.Environ(), so setting it unconditionally
// would quietly win over theirs.
func TestRunLeavesAnOperatorsOwnMCPToolTimeoutAlone(t *testing.T) {
	t.Setenv("MCP_TOOL_TIMEOUT", "1234")
	fake := &fakeRunner{stdout: streamJSONLine(t, map[string]any{"type": "result", "result": "ok"})}
	f := newFramework(fake, "mcpserver-path")

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "x", SandboxRoot: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if got := envValues(fake.gotEnv, "MCP_TOOL_TIMEOUT"); !slices.Equal(got, []string{"1234"}) {
		t.Errorf("MCP_TOOL_TIMEOUT values = %q, want only the operator's 1234", got)
	}
}

// claude's own persistent memory lives under the CLI's $HOME, which is
// the controller's -- unreachable from the sandbox every file tool a run
// has acts in. Left on it costs each run a preamble about a directory it
// cannot open and an index of files it cannot read, so it is turned off
// before the run starts.
func TestRunDisablesTheAgentsOwnAutoMemory(t *testing.T) {
	// Same hazard as the MCP_TOOL_TIMEOUT test above, and latent rather
	// than seen: nothing in the harnesses this is run from sets this one
	// today, and the test would invert just as quietly if one did.
	withoutEnv(t, "CLAUDE_CODE_DISABLE_AUTO_MEMORY")
	fake := &fakeRunner{stdout: streamJSONLine(t, map[string]any{"type": "result", "result": "ok"})}
	f := newFramework(fake, "mcpserver-path")

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "x", SandboxRoot: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	var got string
	for _, e := range fake.gotEnv {
		if value, ok := strings.CutPrefix(e, "CLAUDE_CODE_DISABLE_AUTO_MEMORY="); ok {
			got = value
		}
	}
	// claude reads this one as a boolean (1/true/yes/on), so the value
	// has to be a truthy spelling and not merely present: an empty or
	// "0" one leaves the memory on.
	if got != "1" {
		t.Errorf("gotEnv = %v, want CLAUDE_CODE_DISABLE_AUTO_MEMORY=1 in it", fake.gotEnv)
	}
}

// And an operator who set it themselves outranks that, the same way one
// who set MCP_TOOL_TIMEOUT does -- including the "=0" of a deployment
// that has given its runs a memory they can really reach.
func TestRunLeavesAnOperatorsOwnAutoMemorySettingAlone(t *testing.T) {
	t.Setenv("CLAUDE_CODE_DISABLE_AUTO_MEMORY", "0")
	fake := &fakeRunner{stdout: streamJSONLine(t, map[string]any{"type": "result", "result": "ok"})}
	f := newFramework(fake, "mcpserver-path")

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "x", SandboxRoot: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if got := envValues(fake.gotEnv, "CLAUDE_CODE_DISABLE_AUTO_MEMORY"); !slices.Equal(got, []string{"0"}) {
		t.Errorf("CLAUDE_CODE_DISABLE_AUTO_MEMORY values = %q, want only the operator's 0", got)
	}
}

// claude retries a refused API call ten times over nearly three minutes
// of its own before grain hears about the refusal at all, and grain
// deliberately leaves that alone: it is the cheaper backoff, and a short
// outage absorbed down there never spends one of the two retries
// agent.RetryPolicy keeps for real ones. retry.go's own header has the
// whole of the reasoning. What this asserts is the half of it an
// operator depends on -- that grain sets no CLAUDE_CODE_MAX_RETRIES, so
// a deployment that wants grain's backoff to be the only policy can set
// it on the controller and have it reach claude untouched.
func TestRunLeavesClaudesOwnAPIRetryAlone(t *testing.T) {
	fake := &fakeRunner{stdout: streamJSONLine(t, map[string]any{"type": "result", "result": "ok"})}
	f := newFramework(fake, "mcpserver-path")

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "x", SandboxRoot: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	for _, e := range fake.gotEnv {
		if strings.HasPrefix(e, "CLAUDE_CODE_MAX_RETRIES=") {
			t.Errorf("gotEnv = %v, want no CLAUDE_CODE_MAX_RETRIES of grain's own over the operator's", fake.gotEnv)
		}
	}
}

// A run host (-run-host-image) starts claude with Command.Env over its
// image's environment and nothing of the daemon's, so an operator's
// setting reaches it only if Run names it. Each variable this package
// promises an operator is forwarded with the operator's own value; one
// it does not promise, a credential included, is not.
func TestRunForwardsAnOperatorsOwnSettings(t *testing.T) {
	want := map[string]string{
		"MCP_TOOL_TIMEOUT":                  "1234",
		"CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT": "5678",
		"CLAUDE_CODE_DISABLE_AUTO_MEMORY":   "0",
		"CLAUDE_CODE_MAX_RETRIES":           "1",
	}
	for name, value := range want {
		t.Setenv(name, value)
	}
	t.Setenv("ANTHROPIC_API_KEY", "daemon-secret")
	t.Setenv("ANTHROPIC_BASE_URL", "http://127.0.0.1:1")
	fake := &fakeRunner{stdout: streamJSONLine(t, map[string]any{"type": "result", "result": "ok"})}
	f := newFramework(fake, "mcpserver-path")

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "x", SandboxRoot: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	for name, value := range want {
		if got := envValues(fake.gotEnv, name); !slices.Equal(got, []string{value}) {
			t.Errorf("%s values = %q, want only the operator's %q", name, got, value)
		}
	}
	for _, name := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL"} {
		if got := envValues(fake.gotEnv, name); len(got) > 0 {
			t.Errorf("%s values = %q, want it left out of a run host", name, got)
		}
	}
}

// envValues is every value env gives name, in order.
func envValues(env []string, name string) []string {
	var values []string
	for _, kv := range env {
		if k, v, _ := strings.Cut(kv, "="); k == name {
			values = append(values, v)
		}
	}
	return values
}

func TestRunMirrorsStdoutToTranscriptPath(t *testing.T) {
	stdout := strings.Join([]string{
		streamJSONLine(t, map[string]any{
			"type": "assistant",
			"message": map[string]any{
				"content": []map[string]any{{"type": "text", "text": "on it"}},
			},
		}),
		streamJSONLine(t, map[string]any{"type": "result", "result": "done"}),
	}, "\n")
	fake := &fakeRunner{stdout: stdout}
	f := newFramework(fake, "mcpserver-path")

	transcriptPath := filepath.Join(t.TempDir(), "r1")
	if _, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "x", SandboxRoot: t.TempDir(), TranscriptPath: transcriptPath,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(transcriptPath)
	if err != nil {
		t.Fatalf("expected TranscriptPath to have been written: %v", err)
	}
	if string(got) != stdout {
		t.Errorf("transcript file = %q, want the raw stdout %q", got, stdout)
	}
	if partial := PartialTranscript(string(got)); !strings.Contains(partial, "on it") {
		t.Errorf("PartialTranscript(transcript file) = %q, want it to contain %q", partial, "on it")
	}
}

// The mirror is a run's raw tool output, so nobody but the daemon's own
// user may read it.
func TestRunCreatesTheTranscriptOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run-1")
	f := newFramework(&fakeRunner{stdout: streamJSONLine(t, map[string]any{"type": "result", "result": "ok"})}, "mcpserver-path")

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

func TestRunWithNoTranscriptPathWritesNoFile(t *testing.T) {
	fake := &fakeRunner{stdout: streamJSONLine(t, map[string]any{"type": "result", "result": "ok"})}
	f := newFramework(fake, "mcpserver-path")

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "x", SandboxRoot: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	// Nothing to assert on directly beyond "this didn't panic or error" --
	// TranscriptPath's own zero value ("") is the only thing distinguishing
	// this from the test above, and Run must treat it as "no caller wants
	// this" rather than trying to open a file at an empty path.
}

// TestMockToolCallsNeverReachAnyNetwork mirrors agent/antigravity's own test of
// the same name: proving this package never acts on an escape-hatch call
// itself -- it talks to no network at all, and only parses the
// confirmation `grain mcpserver` already answered with. What that call
// eventually turns into is orchestrator.ProcessResult's business, off
// the ToolCalls this parser produces.
func TestMockToolCallsNeverReachAnyNetwork(t *testing.T) {
	fake := &fakeRunner{stdout: strings.Join([]string{
		streamJSONLine(t, map[string]any{
			"type": "assistant",
			"message": map[string]any{
				"content": []map[string]any{{
					"type": "tool_use", "id": "call-1", "name": "comment_on_issue",
					"input": map[string]any{"comment": "done"},
				}},
			},
		}),
		streamJSONLine(t, map[string]any{
			"type": "user",
			"message": map[string]any{
				"content": []map[string]any{{
					"type": "tool_result", "tool_use_id": "call-1",
					"content": "Recorded. When this run finishes, grain relays this comment " +
						"into the task's conversation.",
				}},
			},
		}),
		streamJSONLine(t, map[string]any{"type": "result", "result": "ok"}),
	}, "\n")}
	f := newFramework(fake, "mcpserver-path")

	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "leave a comment", SandboxRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ToolCalls) != 1 || result.ToolCalls[0].IsError {
		t.Fatalf("ToolCalls = %+v", result.ToolCalls)
	}
}

// The normal case now: no exec key configured, because `granule run`
// generates one per guest and `granule exec`'s own default path already
// holds it. Passing "-exec-key" with an empty value instead of omitting
// it would have mcpserver hand GRANULE_EXEC_KEY="" to granule exec, which
// reads an empty path as a key it cannot open rather than as "unset".
func TestRunOmitsExecKeyWhenUnset(t *testing.T) {
	fake := &fakeRunner{stdout: streamJSONLine(t, map[string]any{"type": "result", "result": "ok"})}
	f := newFramework(fake, "/path/to/grain", WithGranuleSSH("root", "", "/workspace"))

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "x", GranuleVM: "g-1-1"}); err != nil {
		t.Fatalf("a granule run with no exec key should work, got: %v", err)
	}
	var cfg struct {
		MCPServers map[string]struct {
			Args []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(fake.gotMCPConfig, &cfg); err != nil {
		t.Fatalf("mcp-config was not valid JSON: %v (%s)", err, fake.gotMCPConfig)
	}
	for _, arg := range cfg.MCPServers["grain-sandbox"].Args {
		if arg == "-exec-key" {
			t.Errorf("args = %v, want no -exec-key when none is configured", cfg.MCPServers["grain-sandbox"].Args)
		}
	}
}

// maxTurnsStdout is the shape a real `claude -p --output-format
// stream-json` capture has when its --max-turns budget runs out: a run
// that did real work -- streamed text, called a tool, got a result back
// -- and only then hit the cap. Captured against claude 2.1.258, which
// exits 1 with *nothing on stderr* and reports the failure only here,
// as the terminal result event.
func maxTurnsStdout(t *testing.T) string {
	t.Helper()
	return strings.Join([]string{
		streamJSONLine(t, map[string]any{
			"type": "assistant",
			"message": map[string]any{
				"content": []map[string]any{{"type": "text", "text": "pushing the branch"}},
			},
		}),
		streamJSONLine(t, map[string]any{
			"type": "assistant",
			"message": map[string]any{
				"content": []map[string]any{{
					"type": "tool_use", "id": "call-1", "name": "run_command",
					"input": map[string]any{"command": "git push"},
				}},
			},
		}),
		streamJSONLine(t, map[string]any{
			"type": "user",
			"message": map[string]any{
				"content": []map[string]any{{
					"type": "tool_result", "tool_use_id": "call-1", "content": "branch pushed",
				}},
			},
		}),
		// "result": null, exactly as claude sends it for this subtype --
		// there is no final answer to report, which is the whole point.
		streamJSONLine(t, map[string]any{
			"type": "result", "subtype": "error_max_turns", "is_error": true, "result": nil,
		}),
	}, "\n")
}

// A claude that exits non-zero has still already done whatever it did
// before it failed, and Run owes its caller that record: returning nil
// here is what stranded a pushed branch, and -- because
// orchestrator.RunDispatch only records a transcript for a non-nil
// Result, then removes the live mirror the UI had been rendering -- what
// made a failed run's transcript vanish from the UI the instant it
// failed.
func TestRunReturnsTheWorkDoneBeforeANonZeroExit(t *testing.T) {
	fake := &fakeRunner{stdout: maxTurnsStdout(t), err: errors.New("exit status 1 (stderr: )")}
	f := newFramework(fake, "mcpserver-path")

	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "x", SandboxRoot: t.TempDir()})
	if err == nil {
		t.Fatal("expected an error for a claude that exited non-zero")
	}
	if result == nil {
		t.Fatal("Result = nil; a failed run's completed work must still come back")
	}
	if len(result.ToolCalls) != 1 || result.ToolCalls[0].Name != "run_command" {
		t.Errorf("ToolCalls = %+v, want the run_command the run made before it failed", result.ToolCalls)
	}
	if !strings.Contains(result.Transcript, "pushing the branch") {
		t.Errorf("Transcript = %q, want the text streamed before the failure", result.Transcript)
	}
}

// "exit status 1 (stderr: )" is what claude gives a caller that reads
// only the exit status, and it says nothing at all. The stream's own
// terminal result event knows why, and that is what an operator reading
// the failed run in the UI has to be shown.
func TestRunNamesTheTurnCapRatherThanTheExitStatus(t *testing.T) {
	fake := &fakeRunner{stdout: maxTurnsStdout(t), err: errors.New("exit status 1 (stderr: )")}
	f := newFramework(fake, "mcpserver-path", WithMaxTurns(7))

	_, err := f.Run(context.Background(), agent.RunConfig{Prompt: "x", SandboxRoot: t.TempDir()})
	if err == nil {
		t.Fatal("expected an error")
	}
	if got, want := err.Error(), "claude: exceeded max turns (7) without a final answer"; got != want {
		t.Errorf("err = %q, want %q", got, want)
	}
}

// A subprocess that died before saying anything -- no binary, a signal,
// a cancelled context -- has no result event to explain itself, so the
// exit error is all there is and must not be swallowed.
func TestRunFallsBackToTheExitErrorWhenTheStreamSaysNothing(t *testing.T) {
	fake := &fakeRunner{err: errors.New("exec: \"claude\": executable file not found in $PATH")}
	f := newFramework(fake, "mcpserver-path")

	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "x", SandboxRoot: t.TempDir()})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "executable file not found") {
		t.Errorf("err = %q, want the underlying exec error", err)
	}
	// nil, not an empty Result: agent.Framework's contract is that a nil
	// Result with an error means the run never started, and
	// orchestrator.RunDispatch reads it that way.
	if result != nil {
		t.Errorf("Result = %+v, want nil for a run that never started", result)
	}
}

// No cap by default: v1 passed claude no --max-turns at all, and any
// number this package picks is a guess at how much work a task deserves
// that fails a working run when it guesses low. Config.MaxRunRuntime is
// what actually bounds a runaway run.
func TestRunPassesNoMaxTurnsByDefault(t *testing.T) {
	fake := &fakeRunner{stdout: streamJSONLine(t, map[string]any{"type": "result", "result": "done"})}
	f := newFramework(fake, "mcpserver-path")

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "x", SandboxRoot: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	for _, a := range fake.gotArgs {
		if a == "--max-turns" {
			t.Fatalf("args = %v, want no --max-turns at all when none is configured", fake.gotArgs)
		}
	}
}

// A deployment that does want a ceiling still gets one, passed through
// unchanged -- "unlimited" is the default, not the only option.
func TestRunPassesAnExplicitMaxTurnsThrough(t *testing.T) {
	fake := &fakeRunner{stdout: streamJSONLine(t, map[string]any{"type": "result", "result": "done"})}
	f := newFramework(fake, "mcpserver-path")

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "x", SandboxRoot: t.TempDir(), MaxTurns: 12}); err != nil {
		t.Fatal(err)
	}
	if got := argValue(fake.gotArgs, "--max-turns"); got != "12" {
		t.Errorf("--max-turns = %q, want 12", got)
	}
}

// With no cap configured there is no number to tell an operator to raise,
// so the error must not invent one -- it would send them looking for a
// setting that is already unlimited.
func TestRunNamesNoTurnBudgetWhenNoneWasConfigured(t *testing.T) {
	fake := &fakeRunner{stdout: maxTurnsStdout(t), err: errors.New("exit status 1 (stderr: )")}
	f := newFramework(fake, "mcpserver-path")

	_, err := f.Run(context.Background(), agent.RunConfig{Prompt: "x", SandboxRoot: t.TempDir()})
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "(0)") {
		t.Errorf("err = %q, want no fabricated turn budget in the message", err)
	}
	if !strings.Contains(err.Error(), "turn limit") {
		t.Errorf("err = %q, want it to still say the run was stopped at a turn limit", err)
	}
}

// The prompt's half of open_pull_request: orchestrator.BuildPrompt names
// the tool only for a run that really has it, and this is the answer it
// asks for (agent.PullRequestFramework). It tracks WithGrainServer alone,
// since that is the half of -server/-task a Framework holds -- the other
// half, a task id, comes with every dispatch.
func TestCanOpenPullRequestTracksWithGrainServer(t *testing.T) {
	with := New("claude", "/path/to/grain", WithGrainServer("http://127.0.0.1:8420"))
	if !with.CanOpenPullRequest() {
		t.Error("CanOpenPullRequest() = false for a Framework built WithGrainServer")
	}
	without := New("claude", "/path/to/grain")
	if without.CanOpenPullRequest() {
		t.Error("CanOpenPullRequest() = true for a Framework with no daemon to ask -- " +
			"its runs' mcpserver registers no open_pull_request at all")
	}
}

// TestRunReportsClaudesOwnErrorAlongsideItsExitStatus: the stream's
// account of the failure is what a reader can act on, and it led the
// message already -- but the exit status used to be thrown away with it,
// leaving no way to tell a run claude ended deliberately from one that
// died under it. Both halves now, in that order.
func TestRunReportsClaudesOwnErrorAlongsideItsExitStatus(t *testing.T) {
	exitErr := errors.New("exit status 1 (stderr: )")
	fake := &fakeRunner{
		stdout: streamJSONLine(t, map[string]any{
			"type": "result", "subtype": "error_during_execution", "is_error": true,
			"result": "Connection error: the server dropped the request",
		}),
		err: exitErr,
	}
	f := newFramework(fake, "mcpserver-path")

	_, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	if err == nil {
		t.Fatal("Run err = nil, want the failure")
	}
	if !strings.Contains(err.Error(), "the server dropped the request") {
		t.Errorf("Run err = %q, want claude's own account of why the run failed", err)
	}
	if !strings.Contains(err.Error(), "exit status 1") {
		t.Errorf("Run err = %q, want the exit status kept alongside it", err)
	}
	if !errors.Is(err, exitErr) {
		t.Errorf("Run err = %q, want the subprocess's own error still wrapped in it", err)
	}
}

// A claude that died before saying anything -- no stream at all -- is
// still reported by its exit status alone.
func TestRunReportsTheExitStatusAloneWhenClaudeSaidNothing(t *testing.T) {
	fake := &fakeRunner{err: errors.New("signal: killed")}
	f := newFramework(fake, "mcpserver-path")

	_, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()})
	if err == nil {
		t.Fatal("Run err = nil, want the failure")
	}
	if !strings.Contains(err.Error(), "claude: running claude: signal: killed") {
		t.Errorf("Run err = %q, want the subprocess's own failure named", err)
	}
}

type unconfinedTestHost struct {
	agent.LocalHost
}

func (unconfinedTestHost) Unconfined() bool { return true }

func TestRunUnconfinedPreservesNativeTools(t *testing.T) {
	// 1. Confined
	fakeConfined := &fakeRunner{stdout: streamJSONLine(t, map[string]any{"type": "result", "result": "ok"})}
	fConfined := newFramework(fakeConfined, "/path/to/grain")
	if _, err := fConfined.Run(context.Background(), agent.RunConfig{
		Prompt: "go", SandboxRoot: t.TempDir(), TaskID: "t1",
	}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(fakeConfined.gotArgs, "--tools") || !slices.Contains(fakeConfined.gotArgs, "--strict-mcp-config") {
		t.Errorf("confined args = %v, want --tools '' and --strict-mcp-config", fakeConfined.gotArgs)
	}

	// 2. Unconfined
	fakeUnconfined := &fakeRunner{stdout: streamJSONLine(t, map[string]any{"type": "result", "result": "ok"})}
	fUnconfined := newFramework(fakeUnconfined, "/path/to/grain", WithHost(unconfinedTestHost{}))
	if _, err := fUnconfined.Run(context.Background(), agent.RunConfig{
		Prompt: "go", SandboxRoot: t.TempDir(), TaskID: "t1",
	}); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(fakeUnconfined.gotArgs, "--tools") || slices.Contains(fakeUnconfined.gotArgs, "--strict-mcp-config") {
		t.Errorf("unconfined args = %v, want --tools '' and --strict-mcp-config omitted", fakeUnconfined.gotArgs)
	}
	if !slices.Contains(fakeUnconfined.gotArgs, "--mcp-config") {
		t.Errorf("unconfined args = %v, want --mcp-config included", fakeUnconfined.gotArgs)
	}
}

// claude's other cap -- how long a tool call may go without sending
// anything -- defaults to half an hour, and a chat turn waiting on a
// person sends nothing while it waits. Left alone it abandoned chat calls
// at 1800s, before grain's own wait ended, and the person's reply landed
// where no run was reading (grain/task-1774).
func TestRunRaisesTheMCPToolIdleTimeoutPastTheLongestWait(t *testing.T) {
	withoutEnv(t, "CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT")
	fake := &fakeRunner{stdout: streamJSONLine(t, map[string]any{"type": "result", "result": "ok"})}
	f := newFramework(fake, "mcpserver-path")

	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "x", SandboxRoot: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	var got string
	for _, e := range fake.gotEnv {
		if value, ok := strings.CutPrefix(e, "CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT="); ok {
			got = value
		}
	}
	ms, err := strconv.ParseInt(got, 10, 64)
	if err != nil {
		t.Fatalf("CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT = %q in %v, want milliseconds: %v", got, fake.gotEnv, err)
	}
	if limit := time.Duration(ms) * time.Millisecond; limit <= mcp.ChatWait {
		t.Errorf("CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT = %v, want more than a chat turn's own %v", limit, mcp.ChatWait)
	}
}
