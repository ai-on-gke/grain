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

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/mcp"
)

// The live tests in this package stand a real daemon up, and a real
// daemon hands every agent run two commands built out of os.Executable():
// the MCP server that is the run's only route to its sandbox, and the
// PreToolUse hook that withholds agy's own file and command tools. Under
// `go test` that path is this test binary, so what those runs actually
// fork is whatever a test binary does with an argument named "mcpserver"
// -- which, before TestMain answered them, was to ignore it and run this
// entire suite.
//
// Neither half of that says so out loud. A run whose MCP server never
// speaks the protocol comes up with no grain tools and reads as a model
// that would not use them; a hook that runs the suite instead of
// answering spends ~30s and then fails the tool call with agy's own
// "JSON hook ... failed" rather than grain's denial. Both were seen, at
// once, in TestRunLiveWithGranuleAndRESTAPIOpensAPullRequest against a
// live agy -- three symptoms, one cause, and a live credential needed to
// see any of them.
//
// This covers the seam itself, with no credential and no agent: fork this
// binary exactly as a dispatched run does and check that grain answers.
func TestForkedMCPServerAnswersAsGrain(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), daemonWaitLimit)
	defer cancel()

	cmd := exec.CommandContext(ctx, testBinary(t), "mcpserver", "-sandbox-root", t.TempDir())
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("forking this binary as an MCP server: %v", err)
	}
	defer func() {
		stdin.Close()
		_ = cmd.Wait()
	}()

	client := mcp.NewClient(stdin, stdout, stdin)
	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("tools/list against the forked binary: %v (stderr: %s)", err, stderr.String())
	}
	var names []string
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	for _, want := range []string{"run_command", "read_file", "write_file", "edit_file"} {
		if !slices.Contains(names, want) {
			t.Errorf("the forked MCP server advertised %v, want %s among them", names, want)
		}
	}

	// One call, so this says the tools work rather than only that they
	// are listed: the sandbox tools are the whole reason a run forks
	// this at all.
	res, err := client.CallTool(ctx, "run_command", map[string]any{"command": "echo hello"})
	if err != nil {
		t.Fatalf("tools/call run_command: %v (stderr: %s)", err, stderr.String())
	}
	if res.IsError || !strings.Contains(res.Text(), "hello") {
		t.Errorf("run_command answered %q (isError=%v), want the echoed output", res.Text(), res.IsError)
	}
}

// The hook's other end. agyhook_test.go covers what agyToolHook writes
// when called in-process; this covers the fork, which is the part a live
// run depends on and the part that was broken.
func TestForkedToolHookAnswersAsGrain(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), daemonWaitLimit)
	defer cancel()

	cmd := exec.CommandContext(ctx, testBinary(t), antigravity.HookSubcommand)
	cmd.Stdin = strings.NewReader(`{"toolCall":{"name":"run_command"}}`)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("forking this binary as agy's PreToolUse hook: %v (stderr: %s)", err, stderr.String())
	}
	var decision struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &decision); err != nil {
		t.Fatalf("the forked hook wrote %q, which is not the JSON agy reads back: %v", stdout.String(), err)
	}
	if decision.Decision != "deny" {
		t.Errorf("the forked hook answered %q for agy's own run_command, want a deny", stdout.String())
	}
	if !strings.Contains(decision.Reason, mcp.AgyQualifiedToolName("run_command")) {
		t.Errorf("the denial's reason was %q, want it to name the sandbox tool to use instead", decision.Reason)
	}
}

// testBinary is the path a dispatched run would fork: os.Executable(),
// which is what buildAntigravityFramework (daemon.go) reads and hands
// agy, and which under `go test` is this test binary.
func testBinary(t *testing.T) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("resolving this test binary's own path: %v", err)
	}
	return self
}
