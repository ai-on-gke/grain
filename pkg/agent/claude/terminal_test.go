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

package claude

import (
	"context"
	"os"
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
	f := New("/usr/local/bin/claude", "/usr/local/bin/grain", opts...)
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

func mcpConfigPath(t *testing.T, cmd *agent.TerminalCommand) string {
	t.Helper()
	for i, arg := range cmd.Args {
		if arg == "--mcp-config" && i+1 < len(cmd.Args) {
			return cmd.Args[i+1]
		}
	}
	t.Fatalf("args = %v, want an --mcp-config", cmd.Args)
	return ""
}

// The whole difference between a session and a headless run of the same
// task: no print mode, and none of the flags that exist to make print
// mode parseable. What is left is claude's own terminal interface.
func TestTerminalCommandDropsPrintMode(t *testing.T) {
	cmd := terminalCommand(t, agent.RunConfig{SandboxRoot: t.TempDir()})

	for _, unwanted := range []string{"-p", "--output-format", "--verbose"} {
		if slices.Contains(cmd.Args, unwanted) {
			t.Errorf("args = %v, want no %s", cmd.Args, unwanted)
		}
	}
}

// The property the whole design rests on. The CLI runs on the
// *controller*, so without this roster a person typing into a session
// would be editing the daemon's own filesystem rather than the sandbox's
// -- which makes these four arguments the difference between a session
// and a remote shell on the wrong machine.
func TestTerminalCommandKeepsTheToolRoster(t *testing.T) {
	root := t.TempDir()
	cmd := terminalCommand(t, agent.RunConfig{SandboxRoot: root})

	if !argsHave(cmd.Args, "--tools", "") {
		t.Errorf(`args = %v, want --tools "" to empty claude's native roster`, cmd.Args)
	}
	if !slices.Contains(cmd.Args, "--strict-mcp-config") {
		t.Errorf("args = %v, want --strict-mcp-config", cmd.Args)
	}
	if !argsHave(cmd.Args, "--allowedTools", strings.Join(allowedTools(), ",")) {
		t.Errorf("args = %v, want the same allowlist a run of this task gets", cmd.Args)
	}
	config, err := os.ReadFile(mcpConfigPath(t, cmd))
	if err != nil {
		t.Fatalf("reading the session's mcp config: %v", err)
	}
	if !strings.Contains(string(config), root) {
		t.Errorf("mcp config = %q, want it to name this run's sandbox %q", config, root)
	}
}

// An interactive session's first message is typed by whoever opened it,
// so a task's own brief never reaches argv here (it does not reach it
// for a run either -- Run puts it on stdin, which a keyboard has no room
// for).
func TestTerminalCommandCarriesNoPrompt(t *testing.T) {
	secret := "the-task-brief-should-not-be-here"
	cmd := terminalCommand(t, agent.RunConfig{SandboxRoot: t.TempDir(), Prompt: secret})

	for _, arg := range cmd.Args {
		if strings.Contains(arg, secret) {
			t.Errorf("args = %v, want no prompt", cmd.Args)
		}
	}
}

// The config file has to outlive this call -- the process that reads it
// has not been started yet -- so a session's cleanup travels with the
// command instead of being a defer the way Run's is.
func TestTerminalCommandCleanupRemovesTheConfig(t *testing.T) {
	f := New("/usr/local/bin/claude", "/usr/local/bin/grain")
	cmd, err := f.TerminalCommand(context.Background(), agent.RunConfig{SandboxRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("TerminalCommand: %v", err)
	}
	path := mcpConfigPath(t, cmd)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the session's mcp config should exist before it runs: %v", err)
	}

	cmd.Cleanup()

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("stat after cleanup = %v, want the config to be gone", err)
	}
}

// A Framework built round a test's own runner has no binary to start,
// and says so rather than handing pkg/console an empty path to exec.
func TestTerminalCommandRefusesWithoutABinary(t *testing.T) {
	f := newFramework(&fakeRunner{}, "/usr/local/bin/grain")

	if _, err := f.TerminalCommand(context.Background(), agent.RunConfig{}); err == nil {
		t.Fatal("TerminalCommand: want an error for a Framework with no claude binary")
	}
}

func TestTerminalCommandSetsVertexEnvironment(t *testing.T) {
	cmd := terminalCommand(t, agent.RunConfig{SandboxRoot: t.TempDir()}, WithVertex(true))
	if !slices.Contains(cmd.Env, "CLAUDE_CODE_USE_VERTEX=1") {
		t.Errorf("cmd.Env = %v, want CLAUDE_CODE_USE_VERTEX=1", cmd.Env)
	}
	for _, e := range cmd.Env {
		if strings.HasPrefix(e, "GOOGLE_APPLICATION_CREDENTIALS=") {
			t.Errorf("ambient vertex mode set %q, want no GOOGLE_APPLICATION_CREDENTIALS", e)
		}
	}
}

func TestTerminalCommandSetsVertexServiceAccountCredentials(t *testing.T) {
	saJSON := `{"type":"service_account","project_id":"terminal-proj-456"}`
	cmd := terminalCommand(t, agent.RunConfig{SandboxRoot: t.TempDir()}, WithVertex(true), WithOAuthToken(saJSON))
	if !slices.Contains(cmd.Env, "CLAUDE_CODE_USE_VERTEX=1") {
		t.Errorf("cmd.Env = %v, want CLAUDE_CODE_USE_VERTEX=1", cmd.Env)
	}
	if !slices.Contains(cmd.Env, "ANTHROPIC_VERTEX_PROJECT_ID=terminal-proj-456") {
		t.Errorf("cmd.Env = %v, want ANTHROPIC_VERTEX_PROJECT_ID=terminal-proj-456", cmd.Env)
	}

	var credPath string
	for _, e := range cmd.Env {
		if strings.HasPrefix(e, "GOOGLE_APPLICATION_CREDENTIALS=") {
			credPath = strings.TrimPrefix(e, "GOOGLE_APPLICATION_CREDENTIALS=")
		}
	}
	if credPath == "" {
		t.Fatalf("cmd.Env = %v, want GOOGLE_APPLICATION_CREDENTIALS set", cmd.Env)
	}
	data, err := os.ReadFile(credPath)
	if err != nil {
		t.Fatalf("reading credPath: %v", err)
	}
	if string(data) != saJSON {
		t.Errorf("credPath content = %q, want %q", string(data), saJSON)
	}

	cmd.Cleanup()
	if _, err := os.Stat(credPath); !os.IsNotExist(err) {
		t.Errorf("credPath %s was not cleaned up after Cleanup(): err = %v", credPath, err)
	}
}
