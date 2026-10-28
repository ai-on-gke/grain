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

package codex

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/mcp"
)

// adoptedStream is a codex run's whole `codex exec --json` stream as
// grain hostd spools it: the "type" spelling of a real capture
// (TestParseTranscriptReadsTheItemKindUnderEitherFieldName), a thread,
// two tool calls and a completed turn.
func adoptedStream(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "exec-json-adopted.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// An adopting daemon reads a codex run it never watched from the stream
// hostd spooled (agent.ResultParser), and comes to what Run would have:
// the same final text, the same tool calls, and the thread a later
// dispatch may resume. Without this an adopted codex run cannot be read,
// and orchestrator.ResumeAdoptedRun finishes it as interrupted.
func TestParseResultReadsARecordedStreamAsRunWould(t *testing.T) {
	stream := adoptedStream(t)
	f := newTestFramework(&attemptRunner{replies: []attemptReply{{stdout: stream}}}, "/usr/local/bin/grain", withoutRetries())
	ran, runErr := f.Run(context.Background(), agent.RunConfig{Prompt: "do the thing", SandboxRoot: t.TempDir()})
	parsed, err := f.ParseResult(stream, nil)
	if err != nil || runErr != nil {
		t.Fatalf("ParseResult err = %v, Run err = %v; want both clean", err, runErr)
	}
	if parsed.FinalText != "pushed the branch" || parsed.FinalText != ran.FinalText {
		t.Errorf("FinalText = %q, Run's = %q; want the stream's last message from both", parsed.FinalText, ran.FinalText)
	}
	if len(parsed.ToolCalls) != 2 || parsed.ToolCalls[1].Name != "comment_on_issue" || len(ran.ToolCalls) != len(parsed.ToolCalls) {
		t.Errorf("ToolCalls = %+v, Run's = %+v; want both calls under their bare names", parsed.ToolCalls, ran.ToolCalls)
	}
	if parsed.ResumableSession != "01a066db-4912-79b0-8254-ff2d2927a870" {
		t.Errorf("ResumableSession = %q, want the thread the stream opened", parsed.ResumableSession)
	}
	if parsed.Transcript != ran.Transcript {
		t.Errorf("transcript differs from Run's:\nParseResult: %q\nRun: %q", parsed.Transcript, ran.Transcript)
	}

	// A run hostd reports killed comes back as a failure, with whatever it
	// did before it was stopped.
	cut := strings.Join(strings.SplitAfter(stream, "\n")[:5], "")
	partial, err := f.ParseResult(cut, errors.New("signal: killed"))
	if err == nil || partial == nil || len(partial.ToolCalls) != 1 {
		t.Fatalf("ParseResult(killed) = %+v, %v; want the partial run beside an error", partial, err)
	}
}

// A run adopted after it had already retried is read whole, and leaves
// resumable the last thread any attempt named.
func TestParseAttemptsReadsTheWholeCodexRun(t *testing.T) {
	f := newTestFramework(&attemptRunner{}, "/usr/local/bin/grain", withoutBackoff())
	earlier := attemptStream(agentMessage("first try"), overloadedTurn)
	parsed, err := f.ParseAttempts(earlier, `{"type":"turn.started"}`+"\n"+agentMessage("second try")+"\n"+`{"type":"turn.completed"}`+"\n", nil)
	if err != nil || parsed == nil {
		t.Fatalf("ParseAttempts = %+v, %v; want the run's result", parsed, err)
	}
	if !strings.Contains(parsed.Transcript, "first try") || !strings.Contains(parsed.Transcript, "second try") {
		t.Errorf("transcript = %q, want both attempts", parsed.Transcript)
	}
	if parsed.ResumableSession != "t1" {
		t.Errorf("ResumableSession = %q, want the earlier attempt's thread", parsed.ResumableSession)
	}
}

// A codex run the daemon detached from as it stopped is still running in
// grain hostd: the exec is neither retried -- not even as a refused
// resume of a carried thread, which an empty stream would otherwise read
// as -- nor parsed, Run hands back agent.ErrDetached for the orchestrator
// to leave the run live, and the CODEX_HOME codex is running against,
// config and login link included, is left where it is.
func TestADetachedCodexRunIsLeftAlone(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: "", err: fmt.Errorf("hostd: %w", agent.ErrDetached)},
	}}
	f := newTestFramework(r, "/usr/local/bin/grain", withoutBackoff())
	result, err := f.Run(context.Background(), agent.RunConfig{Prompt: "do the thing", SandboxRoot: t.TempDir(),
		ResumeSession: "thread-carried"})
	if !errors.Is(err, agent.ErrDetached) || result != nil {
		t.Fatalf("Run = %+v, %v; want nil and agent.ErrDetached", result, err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("codex was exec'd %d time(s), want the one the daemon detached from", len(r.calls))
	}
	home := r.calls[0].dir
	t.Cleanup(func() { os.RemoveAll(home) })
	if _, err := os.Stat(filepath.Join(home, configFileName)); err != nil {
		t.Errorf("the detached run's %s is gone (%v): codex is still reading it in grain hostd", configFileName, err)
	}

	// The one that is not detached cleans up as before.
	r = &attemptRunner{replies: []attemptReply{{stdout: attemptStream(agentMessage("done"), `{"type":"turn.completed"}`)}}}
	f = newTestFramework(r, "/usr/local/bin/grain", withoutBackoff())
	if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "do the thing", SandboxRoot: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(r.calls[0].dir); !os.IsNotExist(err) {
		t.Errorf("a finished run's CODEX_HOME is still there (%v)", err)
	}
}

func TestDetachedMCPTestChild(t *testing.T) {
	if os.Getenv("GRAIN_CODEX_DETACHED_MCP_CHILD") != "1" {
		return
	}
	registry := mcp.NewRegistry()
	registry.Register(mcp.Tool{
		Name:        "write_file",
		InputSchema: map[string]any{"type": "object"},
		Handler: func(_ context.Context, args map[string]any) mcp.Result {
			return mcp.Result{Text: fmt.Sprintf("wrote %v", args["file_path"])}
		},
	})
	_ = mcp.Serve(context.Background(), registry, bufio.NewReader(os.Stdin), bufio.NewWriter(os.Stdout))
	os.Exit(0)
}

// WithDetachedMCP writes mcpserver.json instead of starting an HTTP MCP
// adapter inside the daemon, and RunDetached (`grain codex-detached`) starts
// mcp.StartCommandHTTP in the detached wrapper, writes the loopback URL,
// per-run bearer token, and snapshotted tools into $CODEX_HOME/config.toml,
// and serves tool calls for the duration of the codex invocation.
func TestDetachedMCPWrapperServesToolsAndWritesConfig(t *testing.T) {
	dir := t.TempDir()
	fakeGrain := filepath.Join(dir, "grain")
	grainScript := fmt.Sprintf("#!/bin/sh\nexport GRAIN_CODEX_DETACHED_MCP_CHILD=1\nexec %q -test.run=^TestDetachedMCPTestChild$\n", os.Args[0])
	if err := os.WriteFile(fakeGrain, []byte(grainScript), 0o755); err != nil {
		t.Fatal(err)
	}

	f := newFramework(nil, fakeGrain, WithDetachedMCP(), WithEffort("high"))
	f.startMCP = func(context.Context, string, []string) (*mcp.ManagedHTTP, error) {
		t.Fatal("daemon started MCP adapter despite WithDetachedMCP")
		return nil, nil
	}

	home, _, cleanup, err := f.writeConfig(context.Background(), []string{"mcpserver", "-sandbox-root", dir}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	initialCfg, err := os.ReadFile(filepath.Join(home, configFileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(initialCfg), "[mcp_servers."+mcpServerName+"]") {
		t.Fatalf("initial config.toml already has [%s]:\n%s", mcpServerName, initialCfg)
	}

	outFile := filepath.Join(dir, "mcp-response.json")
	configSnapshot := filepath.Join(dir, "config-at-run.toml")
	fakeCodex := filepath.Join(dir, "codex.bin")
	codexScript := fmt.Sprintf(`#!/bin/sh
set -eu
if [ "${1:-}" = "--version" ]; then
  echo "codex-cli %s"
  exit 0
fi
cp "$CODEX_HOME/config.toml" %q
url=$(sed -n 's/^url = "\(.*\)"/\1/p' "$CODEX_HOME/config.toml")
auth=$(sed -n 's/^http_headers = { Authorization = "\(.*\)" }/\1/p' "$CODEX_HOME/config.toml")
curl -fsS -X POST "$url" \
  -H "Authorization: $auth" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"write_file","arguments":{"file_path":"detached.txt"}}}' \
  > %q
`, ReviewedCLIVersion, configSnapshot, outFile)
	if err := os.WriteFile(fakeCodex, []byte(codexScript), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CODEX_HOME", home)
	// A --version probe must not start the MCP adapter or rewrite config.toml.
	if code, err := RunDetached(context.Background(), []string{fakeCodex, "--version"}); err != nil || code != 0 {
		t.Fatalf("RunDetached(--version) = %d, %v", code, err)
	}
	afterProbeCfg, err := os.ReadFile(filepath.Join(home, configFileName))
	if err != nil {
		t.Fatal(err)
	}
	if string(afterProbeCfg) != string(initialCfg) {
		t.Fatalf("--version probe rewrote config.toml:\n%s", afterProbeCfg)
	}

	// An exec invocation starts the managed HTTP MCP server and writes its
	// URL, token, and snapshotted tools into config.toml before running codex.
	if code, err := RunDetached(context.Background(), []string{fakeCodex, "exec", "--json", "-"}); err != nil || code != 0 {
		t.Fatalf("RunDetached(exec) = %d, %v", code, err)
	}
	runCfgBytes, err := os.ReadFile(configSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	runCfg := string(runCfgBytes)
	for _, want := range []string{
		`approval_policy = "never"`,
		`[mcp_servers.grain-sandbox]`,
		`url = "http://127.0.0.1:`,
		`http_headers = { Authorization = "Bearer `,
		`enabled_tools = ["write_file"]`,
		`[mcp_servers.grain-sandbox.tools."write_file"]`,
		`approval_mode = "approve"`,
	} {
		if !strings.Contains(runCfg, want) {
			t.Errorf("config.toml during detached run missing %q:\n%s", want, runCfg)
		}
	}
	respBytes, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(respBytes), "wrote detached.txt") {
		t.Errorf("MCP response = %s, want 'wrote detached.txt'", respBytes)
	}
}
