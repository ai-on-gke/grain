package antigravity

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
)

// terminalCommand builds a Framework the way New does -- with a real
// binary path, since that is what TerminalCommand needs and what a test
// runner does not supply -- and asks it for one session's argv.
func terminalCommand(t *testing.T, cfg agent.RunConfig, opts ...Option) *agent.TerminalCommand {
	t.Helper()
	f := New("/usr/local/bin/agy", "/usr/local/bin/grain", opts...)
	cmd, err := f.TerminalCommand(context.Background(), cfg)
	if err != nil {
		t.Fatalf("TerminalCommand: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Cleanup != nil {
			cmd.Cleanup()
		}
	})
	return cmd
}

func agyHome(t *testing.T, cmd *agent.TerminalCommand) string {
	t.Helper()
	for _, e := range cmd.Env {
		if home, ok := strings.CutPrefix(e, "HOME="); ok {
			return home
		}
	}
	t.Fatalf("env = %v, want a HOME", cmd.Env)
	return ""
}

// Print mode and everything that shapes it: an interactive session is
// agy's own terminal interface, and nothing is parsing what it draws.
func TestTerminalCommandDropsPrintMode(t *testing.T) {
	cmd := terminalCommand(t, agent.RunConfig{SandboxRoot: t.TempDir()})

	for _, unwanted := range []string{
		"--print", "-p",
		"--input-format", "--output-format",
		// A session is bounded by orchestrator's own console runtime,
		// not by print mode's five-minute default.
		"--print-timeout",
	} {
		if slices.Contains(cmd.Args, unwanted) {
			t.Errorf("args = %v, want no %s", cmd.Args, unwanted)
		}
	}
}

// The one removal that is a judgement rather than a mechanical one: the
// slash commands are half of what somebody sits down at this CLI for,
// and /exit is one of the three documented ways a session ends at all.
func TestTerminalCommandLeavesTheSlashCommandsOn(t *testing.T) {
	cmd := terminalCommand(t, agent.RunConfig{SandboxRoot: t.TempDir()})

	if slices.Contains(cmd.Args, "--disable-slash-commands") {
		t.Errorf("args = %v, want the slash commands left on for a session", cmd.Args)
	}
}

// Everything that decides what the agent is and what it may touch stays
// exactly as Run passes it.
func TestTerminalCommandKeepsWhatDecidesWhatTheAgentMayDo(t *testing.T) {
	root := t.TempDir()
	cmd := terminalCommand(t, agent.RunConfig{SandboxRoot: root},
		WithModel("gemini-3-pro"), WithEffort("high"))

	if !slices.Contains(cmd.Args, "--dangerously-skip-permissions") {
		t.Errorf("args = %v, want --dangerously-skip-permissions", cmd.Args)
	}
	if !argsHave(cmd.Args, "--add-dir", root) {
		t.Errorf("args = %v, want --add-dir %s", cmd.Args, root)
	}
	if !argsHave(cmd.Args, "--model", "gemini-3-pro") {
		t.Errorf("args = %v, want --model gemini-3-pro", cmd.Args)
	}
	if !argsHave(cmd.Args, "--effort", "high") {
		t.Errorf("args = %v, want --effort high", cmd.Args)
	}
}

// An interactive caller may choose to let agy's permission engine prompt for
// decisions instead of bypassing it, just as an unattended caller may.
func TestTerminalCommandCanUseAmbientPermissions(t *testing.T) {
	cmd := terminalCommand(t, agent.RunConfig{SandboxRoot: t.TempDir()}, WithAmbientPermissions())

	if slices.Contains(cmd.Args, "--dangerously-skip-permissions") {
		t.Errorf("args = %v, want agy's ambient permission mode", cmd.Args)
	}
}

// The property the whole design rests on, and the one that is not on the
// command line at all: a session gets the same private HOME an
// unattended run of the same task gets, so it reaches the same sandbox
// through the same MCP server, under the same denials of agy's own
// filesystem tools.
func TestTerminalCommandGetsThisRunsOwnHome(t *testing.T) {
	root := t.TempDir()
	cmd := terminalCommand(t, agent.RunConfig{SandboxRoot: root})
	home := agyHome(t, cmd)

	mcp, err := os.ReadFile(filepath.Join(home, mcpConfigRelPath))
	if err != nil {
		t.Fatalf("reading the session's mcp config: %v", err)
	}
	if !strings.Contains(string(mcp), root) {
		t.Errorf("mcp config = %q, want it to name this run's sandbox %q", mcp, root)
	}
	settings, err := os.ReadFile(filepath.Join(home, cliSettingsRelPath))
	if err != nil {
		t.Fatalf("reading the session's settings: %v", err)
	}
	// The detailed roster is permissionRules' own test. This verifies that
	// both sides of AGY 1.2's action(target) policy reach terminal sessions.
	if !strings.Contains(string(settings), "mcp(grain-sandbox/run_command)") {
		t.Errorf("settings = %q, want grain MCP grants in action(target) form", settings)
	}
	if !strings.Contains(string(settings), "command(*)") {
		t.Errorf("settings = %q, want native command denials in action(target) form", settings)
	}
}

// A session holds the same capability grants a run of its task would,
// so its HOME names their MCP servers, allows their tools and admits
// them in the hook, as a run's does (§5.2.4).
func TestTerminalCommandGetsTheCapabilityServers(t *testing.T) {
	servers := []agent.CapabilityMCPServer{{Name: "self-debug", URL: "http://127.0.0.1:4000/abc/mcp", Tools: []string{"read_grain_source"}}}
	cmd := terminalCommand(t, agent.RunConfig{SandboxRoot: t.TempDir(), CapabilityMCPServers: servers})
	home := agyHome(t, cmd)

	for rel, want := range map[string]string{
		mcpConfigRelPath:   "http://127.0.0.1:4000/abc/mcp",
		cliSettingsRelPath: "mcp(self-debug/read_grain_source)",
		hooksConfigRelPath: "self-debug/read_grain_source",
	} {
		got, err := os.ReadFile(filepath.Join(home, rel))
		if err != nil {
			t.Fatalf("reading the session's %s: %v", rel, err)
		}
		if !strings.Contains(string(got), want) {
			t.Errorf("%s = %q, want it to name %q", rel, got, want)
		}
	}
}

// The HOME has to outlive this call -- the process that reads it has not
// been started yet -- so a session's cleanup travels with the command
// instead of being a defer the way Run's is.
func TestTerminalCommandCleanupRemovesTheHome(t *testing.T) {
	f := New("/usr/local/bin/agy", "/usr/local/bin/grain")
	cmd, err := f.TerminalCommand(context.Background(), agent.RunConfig{SandboxRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("TerminalCommand: %v", err)
	}
	home := agyHome(t, cmd)
	if _, err := os.Stat(home); err != nil {
		t.Fatalf("the session's HOME should exist before it runs: %v", err)
	}

	cmd.Cleanup()

	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Errorf("stat after cleanup = %v, want the home to be gone", err)
	}
}

// A session starts where a run of the same task starts: the sandbox root
// for a host-rooted run, and the workspace inside the private HOME for a
// granule one, which has no local directory of its own.
func TestTerminalCommandStartsWhereTheRunWould(t *testing.T) {
	root := t.TempDir()
	if dir := terminalCommand(t, agent.RunConfig{SandboxRoot: root}).Dir; dir != root {
		t.Errorf("Dir = %q, want the sandbox root %q", dir, root)
	}
	granule := terminalCommand(t, agent.RunConfig{GranuleVM: "vm-1"},
		WithGranuleSSH("agent", "/keys/exec", "/workspace"))
	if granule.Dir != agyWorkspaceDir(agyHome(t, granule)) {
		t.Errorf("Dir = %q, want the workspace inside the session's own HOME", granule.Dir)
	}
}

// WithAPIKey's own reason, applied to a session: the credential travels
// in the environment rather than on a command line every `ps` on the
// controller can read -- and GOOGLE_API_KEY is cleared beside it, so an
// unrelated key exported on the controller cannot quietly become what
// this session bills against.
func TestTerminalCommandPassesTheKeyByEnvironment(t *testing.T) {
	cmd := terminalCommand(t, agent.RunConfig{SandboxRoot: t.TempDir()},
		WithAPIKey("AIza-not-a-real-key"))

	if !slices.Contains(cmd.Env, "GEMINI_API_KEY=AIza-not-a-real-key") {
		t.Errorf("env = %v, want the key in GEMINI_API_KEY", cmd.Env)
	}
	if !slices.Contains(cmd.Env, "GOOGLE_API_KEY=") {
		t.Errorf("env = %v, want GOOGLE_API_KEY cleared beside it", cmd.Env)
	}
	for _, arg := range cmd.Args {
		if strings.Contains(arg, "AIza-not-a-real-key") {
			t.Errorf("args = %v, want no credential on the command line", cmd.Args)
		}
	}
}

// A Framework built round a test's own runner has no binary to start,
// and says so rather than handing pkg/console an empty path to exec.
func TestTerminalCommandRefusesWithoutABinary(t *testing.T) {
	f := newFramework(&recordingRunner{}, "/usr/local/bin/grain")

	if _, err := f.TerminalCommand(context.Background(), agent.RunConfig{}); err == nil {
		t.Fatal("TerminalCommand: want an error for a Framework with no agy binary")
	}
}
