package codex

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/agent"
)

// terminalCommand builds a Framework the way New does -- with a real
// binary path, since that is what TerminalCommand needs and what a test
// runner does not supply -- and asks it for one session's argv.
func terminalCommand(t *testing.T, cfg agent.RunConfig, opts ...Option) *agent.TerminalCommand {
	t.Helper()
	f := newTerminalTestFramework("/usr/local/bin/codex", "/usr/local/bin/grain", opts...)
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

func codexHome(t *testing.T, cmd *agent.TerminalCommand) string {
	t.Helper()
	for _, e := range cmd.Env {
		if home, ok := strings.CutPrefix(e, "CODEX_HOME="); ok {
			return home
		}
	}
	t.Fatalf("env = %v, want a CODEX_HOME", cmd.Env)
	return ""
}

// The whole difference between a session and a headless run of the same
// task: no `exec`, and none of the flags that exist to make its output
// parseable. What is left is codex's own terminal interface.
func TestTerminalCommandDropsTheHeadlessSubcommand(t *testing.T) {
	cmd := terminalCommand(t, agent.RunConfig{SandboxRoot: t.TempDir()})

	if slices.Contains(cmd.Args, "exec") {
		t.Errorf("args = %v, want no exec subcommand", cmd.Args)
	}
	if slices.Contains(cmd.Args, "--json") {
		t.Errorf("args = %v, want no --json: nothing is parsing this", cmd.Args)
	}
	if slices.Contains(cmd.Args, "-") {
		t.Errorf("args = %v, want no prompt argument: an interactive session's first message is typed", cmd.Args)
	}
}

// And the flag that does *not* come across from codexArgs, which is the
// one this whole argument list was got wrong about once:
// --skip-git-repo-check belongs to `codex exec`, and codex answers
// "error: unexpected argument" to it at the top level -- a session dead
// on the terminal of whoever opened it. See TerminalCommand's comment for
// what happens instead (the interactive half asks, and there is somebody
// there to answer), and surface_capture_test.go for the binary's own
// answer, which is where this stopped being a reading of a help page.
func TestTerminalCommandDropsTheExecOnlyRepoCheck(t *testing.T) {
	cmd := terminalCommand(t, agent.RunConfig{SandboxRoot: t.TempDir()})

	if slices.Contains(cmd.Args, "--skip-git-repo-check") {
		t.Errorf("args = %v, want no --skip-git-repo-check: it is a flag of `codex exec`, and codex refuses the whole command line over it", cmd.Args)
	}
}

// The property the whole design rests on: an interactive session reaches
// the same sandbox through the same MCP server an unattended run of the
// same task would, because it is given the same private CODEX_HOME --
// including the two policies that keep codex's native tools from being a
// way around it.
func TestTerminalCommandGetsThisRunsOwnConfig(t *testing.T) {
	root := t.TempDir()
	cmd := terminalCommand(t, agent.RunConfig{SandboxRoot: root})

	config, err := os.ReadFile(filepath.Join(codexHome(t, cmd), configFileName))
	if err != nil {
		t.Fatalf("reading the session's config: %v", err)
	}
	got := string(config)
	for _, want := range []string{
		`url = "http://127.0.0.1:12345/mcp"`,
		`code_mode_host = true`,
		`approval_policy = "never"`,
		`sandbox_mode = "read-only"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("config = %q, want it to contain %q", got, want)
		}
	}
}

// The CODEX_HOME has to outlive this call -- the process that reads it
// has not been started yet -- so a session's cleanup travels with the
// command instead of being a defer the way Run's is.
func TestTerminalCommandCleanupRemovesTheConfigHome(t *testing.T) {
	f := newTerminalTestFramework("/usr/local/bin/codex", "/usr/local/bin/grain")
	cmd, err := f.TerminalCommand(context.Background(), agent.RunConfig{SandboxRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("TerminalCommand: %v", err)
	}
	home := codexHome(t, cmd)
	if _, err := os.Stat(home); err != nil {
		t.Fatalf("the session's CODEX_HOME should exist before it runs: %v", err)
	}

	cmd.Cleanup()

	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Errorf("stat after cleanup = %v, want the home to be gone", err)
	}
}

// A session starts where a run of the same task starts, because both
// CLIs decide something by where they were started.
func TestTerminalCommandStartsWhereTheRunWould(t *testing.T) {
	root := t.TempDir()
	if dir := terminalCommand(t, agent.RunConfig{SandboxRoot: root}).Dir; dir == root || dir == "" {
		t.Errorf("Dir = %q, want the sandbox root %q", dir, root)
	}
	// A granule run has no local directory at all, and leaves this empty
	// exactly as Run does.
	granule := terminalCommand(t, agent.RunConfig{GranuleVM: "vm-1"},
		WithGranuleSSH("grain", "", "/home/grain/work"))
	if dir := granule.Dir; dir != codexHome(t, granule) {
		t.Errorf("Dir = %q, want empty for a granule run", dir)
	}
}

func TestTerminalCommandCarriesTheModelAndTheKey(t *testing.T) {
	cmd := terminalCommand(t, agent.RunConfig{SandboxRoot: t.TempDir()},
		WithModel("gpt-5-codex"),
		WithAPIKey("sk-test"),
	)

	if !argsHave(cmd.Args, "--model", "gpt-5-codex") {
		t.Errorf("args = %v, want --model gpt-5-codex", cmd.Args)
	}
	if !slices.Contains(cmd.Env, "OPENAI_API_KEY=sk-test") {
		t.Errorf("env = %v, want the API key", cmd.Env)
	}
}

func TestTerminalCommandSharesTheAuthFile(t *testing.T) {
	authFile := filepath.Join(t.TempDir(), authFileName)
	if err := os.WriteFile(authFile, []byte(`{"tokens":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := terminalCommand(t, agent.RunConfig{SandboxRoot: t.TempDir()}, WithAuthFile(authFile))
	home := codexHome(t, cmd)

	target, err := os.Readlink(filepath.Join(home, authFileName))
	if err != nil {
		t.Fatalf("auth.json is not a link: %v", err)
	}
	want, _ := filepath.Abs(authFile)
	if target != want {
		t.Errorf("auth.json link target = %q, want %q", target, want)
	}
	cmd.Cleanup()
	if _, err := os.Stat(authFile); err != nil {
		t.Fatalf("session cleanup removed the shared auth file: %v", err)
	}
}

// A Framework built round a test's own runner has no binary to start,
// and says so rather than handing pkg/console an empty path to exec.
func TestTerminalCommandRefusesWithoutABinary(t *testing.T) {
	f := newTestFramework(&recordingRunner{}, "/usr/local/bin/grain")

	if _, err := f.TerminalCommand(context.Background(), agent.RunConfig{}); err == nil {
		t.Fatal("TerminalCommand: want an error for a Framework with no codex binary")
	}
}
