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
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
)

// liveAgentTestEnv is the opt-in that makes this file's gates loud, and
// it is deliberately the same name tests/e2e and agent/codex read: one
// variable a maintainer has to remember across every live test in this
// repository, and the one .github/workflows/live-frameworks.yml sets.
//
// It exists because a skip and a pass are the same `ok` in go test's
// summary line -- so a nightly whose credential had quietly gone missing
// would report success for a run that exercised no claude at all, which
// is the same "nobody noticed nothing ran" gap the nightly itself exists
// to close.
const liveAgentTestEnv = "GRAIN_LIVE_AGENT_TEST"

func skipUnlessLiveRequired(t *testing.T, reason string) {
	t.Helper()
	if os.Getenv(liveAgentTestEnv) != "" {
		t.Fatalf("%s is set, but %s: this run proved nothing about a live claude", liveAgentTestEnv, reason)
	}
	t.Skipf("%s; skipping the live claude test (set %s=1 to make this a failure)", reason, liveAgentTestEnv)
}

// TestLiveRunEndToEnd exercises the real claude binary and a real grain
// binary's "mcpserver" subcommand, gating on a credential and skipping
// otherwise -- so the pull-request suite (where CLAUDE_CODE_OAUTH_TOKEN
// is unset, and no claude binary is installed) stays green, while
// .github/workflows/live-frameworks.yml runs it nightly against a
// credential of its own. By hand, wherever both are available:
//
//	GRAIN_LIVE_AGENT_TEST=1 CLAUDE_CODE_OAUTH_TOKEN=... \
//	  go test ./pkg/agent/claude/ -run TestLiveRunEndToEnd -v
func TestLiveRunEndToEnd(t *testing.T) {
	token := os.Getenv("CLAUDE_CODE_OAUTH_TOKEN")
	if token == "" {
		skipUnlessLiveRequired(t, "CLAUDE_CODE_OAUTH_TOKEN is not set")
	}
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		skipUnlessLiveRequired(t, "no claude binary on $PATH")
	}

	grainBinaryPath := buildGrainBinary(t)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	f := New(claudePath, grainBinaryPath, WithOAuthToken(token))

	root := t.TempDir()
	// The raw stream-json is kept and logged whichever way this goes:
	// when a live run fails, what claude actually streamed is the only
	// account of why, and "ok.txt is missing" says nothing about whether
	// the model declined, the tools were absent, or the credential was
	// refused.
	transcriptPath := filepath.Join(t.TempDir(), "claude.jsonl")
	result, err := f.Run(ctx, agent.RunConfig{
		Prompt: "Use your write_file tool to create a file named ok.txt in your sandbox " +
			"workspace containing exactly the text PONG (no extra whitespace or " +
			"punctuation). Then use read_file to confirm it, and reply with a short " +
			"confirmation once you have done both.",
		SandboxRoot:    root,
		TranscriptPath: transcriptPath,
	})
	if raw, readErr := os.ReadFile(transcriptPath); readErr == nil {
		t.Logf("claude event stream:\n%s", raw)
	}
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(root, "ok.txt"))
	if err != nil {
		t.Fatalf("expected ok.txt to have been written inside the sandbox root: %v", err)
	}
	if strings.TrimSpace(string(data)) != "PONG" {
		t.Errorf("ok.txt content = %q, want PONG", string(data))
	}

	var sawWrite bool
	for _, call := range result.ToolCalls {
		if call.Name == "write_file" {
			sawWrite = true
		}
		t.Logf("tool call: %s(%v) -> error=%v text=%q", call.Name, call.Arguments, call.IsError, call.Text)
	}
	if !sawWrite {
		t.Errorf("expected the model to have called write_file at least once; got calls: %+v", result.ToolCalls)
	}
	if result.FinalText == "" {
		t.Errorf("expected a non-empty final answer")
	}
}

// buildGrainBinary compiles cmd/grain into t.TempDir() so the live
// test above can point a real claude --mcp-config at its "mcpserver"
// subcommand, the same binary a real deployment would build once and
// reuse across every dispatch (as the daemon, the UI, or the CLI too).
func buildGrainBinary(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "grain")
	cmd := exec.Command("go", "build", "-o", out, "github.com/ai-on-gke/grain/cmd/grain")
	cmd.Dir = repoRoot(t)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building cmd/grain: %v\n%s", err, output)
	}
	return out
}

// repoRoot walks up from the current package directory to the module
// root -- so `go build`'s package path resolves the same way regardless of
// which directory `go test` happened to be invoked from.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find go.mod above the current test directory")
		}
		dir = parent
	}
}

// TestLiveMaxTurnsExitIsReportedNotSwallowed pins the real CLI behaviour
// this package's error handling is built around, which no fake runner can
// establish: claude reports a run that ran out of turns as a terminal
// stream-json result event on *stdout* and then exits 1 with *nothing on
// stderr*. A caller reading only the exit status therefore gets the
// famously useless "exit status 1 (stderr: )" and, if it also discards
// stdout, loses the entire transcript of a run that may well have edited,
// committed and pushed before the cap stopped it.
//
// Gated the same way TestLiveRunEndToEnd is, and unlike it needs no grain
// binary -- it drives hostRunner directly, since what is under test is
// the subprocess contract rather than the MCP plumbing:
//
//	GRAIN_LIVE_AGENT_TEST=1 CLAUDE_CODE_OAUTH_TOKEN=... \
//	  go test ./pkg/agent/claude/ -run TestLiveMaxTurns -v
func TestLiveMaxTurnsExitIsReportedNotSwallowed(t *testing.T) {
	token := os.Getenv("CLAUDE_CODE_OAUTH_TOKEN")
	if token == "" {
		skipUnlessLiveRequired(t, "CLAUDE_CODE_OAUTH_TOKEN is not set")
	}
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		skipUnlessLiveRequired(t, "no claude binary on $PATH")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// A prompt that cannot be finished inside one turn, against claude's
	// own native Bash rather than grain's MCP tools: the point is to make
	// the CLI exhaust --max-turns, not to exercise a sandbox.
	stdout, runErr := hostRunner{host: agent.LocalHost{}, claudePath: claudePath}.Run(ctx, []string{
		"-p", "--output-format", "stream-json", "--verbose",
		"--max-turns", "1", "--allowedTools", "Bash",
	}, "Run 'ls' with bash, then run 'pwd' with bash, then run 'date' with bash, then report all three.",
		[]string{"CLAUDE_CODE_OAUTH_TOKEN=" + token}, nil)
	if runErr == nil {
		t.Skip("claude finished inside its turn budget this time; nothing to assert")
	}
	if stdout == "" {
		t.Fatal("stdout was discarded on a non-zero exit; the run's own account of itself is the only thing that explains it")
	}
	if got, want := runFailure(parseEvents(stdout), 1, runErr).Error(),
		"claude: exceeded max turns (1) without a final answer"; got != want {
		t.Errorf("runFailure = %q, want %q (raw exit error was %q)", got, want, runErr)
	}
	parsed, _ := parseTranscript(stdout)
	partial := partialResult(parsed, stdout)
	if partial == nil || len(partial.ToolCalls) == 0 || partial.Transcript == "" {
		t.Fatalf("partialResult = %+v, want the work the run completed before the cap stopped it", partial)
	}
}
