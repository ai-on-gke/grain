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

// Command testagent is the deterministic agent of tests/installer's
// container lifecycle test. It stands in for the Antigravity CLI (agy):
// an antigravity connection whose CLI path is this binary runs it. The
// prompt arrives as one `--input-format stream-json` user event on stdin,
// it makes its tool calls through the grain-sandbox MCP server grain
// wrote into $HOME's mcp_config.json, and it answers in agy's
// stream-json (init, step_update, result).
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/mcp"
)

func main() {
	for _, arg := range os.Args[1:] {
		switch arg {
		case "--version":
			fmt.Println("testagent 0.0.0 (tests/installer)")
			return
		case "models":
			// Model discovery: the connection pins its model.
			return
		}
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		fail(fmt.Errorf("reading the prompt: %w", err))
	}
	var user struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(line), &user); err != nil {
		fail(fmt.Errorf("decoding the prompt event: %w", err))
	}
	fields := parseFields(user.Message.Content)
	for _, key := range []string{"branch", "repo", "host_marker", "signal", "gate"} {
		if fields[key] == "" {
			fail(fmt.Errorf("prompt has no %s field", key))
		}
	}
	emit(map[string]any{"event": "init", "conversation_id": conversation,
		"init": map[string]any{"tools": []string{}}})

	command, args := mcpServer(os.Getenv("HOME"))
	if command == "" {
		fail(fmt.Errorf("no %s server in %s", mcp.ToolNamespace, filepath.Join(os.Getenv("HOME"), ".gemini", "config", "mcp_config.json")))
	}
	server := exec.Command(command, args...)
	server.Env = os.Environ()
	stdin, err := server.StdinPipe()
	if err != nil {
		fail(err)
	}
	stdout, err := server.StdoutPipe()
	if err != nil {
		fail(err)
	}
	server.Stderr = os.Stderr
	if err := server.Start(); err != nil {
		fail(err)
	}
	client := mcp.NewClient(stdin, stdout, stdin)
	defer func() {
		_ = client.Close()
		_ = server.Wait()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	step := 0
	call := func(name string, arguments map[string]any) string {
		emit(map[string]any{"event": "step_update", "step_update": map[string]any{
			"conversation_id": conversation, "step_index": step, "state": "ACTIVE", "step_type": "tool",
			"tool_name": name, "tool_info": map[string]any{"name": name, "parameters": arguments}}})
		result, err := client.CallTool(ctx, name, arguments)
		if err != nil {
			fail(fmt.Errorf("%s: %w", name, err))
		}
		state := "DONE"
		if result.IsError {
			state = "ERROR"
		}
		emit(map[string]any{"event": "step_update", "step_update": map[string]any{
			"conversation_id": conversation, "step_index": step, "state": state, "step_type": "tool",
			"tool_name": name, "tool_info": map[string]any{"name": name, "parameters": arguments, "output": result.Text()}}})
		step++
		if result.IsError {
			fail(fmt.Errorf("%s: %s", name, result.Text()))
		}
		return result.Text()
	}

	// The clone is already here: grain checks the task's repo out into
	// "work" and puts it on the task branch before the agent's first turn
	// (pkg/orchestrator's prepareCheckout and CheckoutDir). This agent
	// used to clone one of its own from $HOME/.git-credentials, which now
	// fails with "destination path 'work' already exists" -- so what it
	// does instead is read what it was handed, which is also the thing
	// worth asserting about a dispatched run.
	//
	// The host marker is checked in the same command: the file exists on
	// the machine running the suite and must not be visible from inside
	// the per-run container.
	checkout := "set -eu; test ! -e " + shellQuote(fields["host_marker"]) +
		"; cd work; test \"$(git rev-parse --abbrev-ref HEAD)\" = " + shellQuote(fields["branch"]) +
		"; git remote get-url origin | grep -qF " + shellQuote(fields["repo"]) +
		"; hostname > .sandbox-hostname"
	call("run_command", map[string]any{
		"command": checkout, "description": "reading the checkout grain prepared inside the sandbox",
	})
	call("write_file", map[string]any{
		"file_path": "work/MCP_PROBE.md",
		"content":   "written through the run's MCP filesystem tool\n",
	})

	if err := os.WriteFile(fields["signal"], []byte("ready\n"), 0o600); err != nil {
		fail(fmt.Errorf("write signal: %w", err))
	}
	for {
		if _, err := os.Stat(fields["gate"]); err == nil {
			break
		} else if !os.IsNotExist(err) {
			fail(fmt.Errorf("read gate: %w", err))
		}
		select {
		case <-ctx.Done():
			fail(fmt.Errorf("waiting for test gate: %w", ctx.Err()))
		case <-time.After(100 * time.Millisecond):
		}
	}

	finish := "set -eu; cd work; git add .sandbox-hostname MCP_PROBE.md; " +
		"git commit -m " + shellQuote("Exercise the local Docker sandbox lifecycle") +
		"; git push origin " + shellQuote(fields["branch"])
	call("run_command", map[string]any{
		"command": finish, "description": "committing and pushing from inside the sandbox",
	})

	const response = "checked out, edited through MCP, committed, and pushed inside the container"
	emit(map[string]any{"event": "step_update", "step_update": map[string]any{
		"conversation_id": conversation, "step_index": step, "state": "DONE", "step_type": "agent_response",
		"text_delta": response}})
	emit(map[string]any{"event": "result", "result": map[string]any{
		"conversation_id": conversation, "status": "SUCCESS", "response": response, "num_turns": 1}})
}

const conversation = "installer-e2e-conversation"

// mcpServer is the grain-sandbox server from the mcp_config.json grain
// wrote into home, the file real agy reads: the command it forks.
func mcpServer(home string) (string, []string) {
	b, err := os.ReadFile(filepath.Join(home, ".gemini", "config", "mcp_config.json"))
	if err != nil {
		return "", nil
	}
	var cfg struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if json.Unmarshal(b, &cfg) != nil {
		return "", nil
	}
	s := cfg.MCPServers[mcp.ToolNamespace]
	return s.Command, s.Args
}

func emit(event map[string]any) {
	b, _ := json.Marshal(event)
	fmt.Println(string(b))
}

// parseFields reads this agent's instructions out of the prompt: the
// space-separated key=value pairs the suite writes into the task's
// description.
//
// First occurrence wins, which is not a detail. The description is not
// the only place it appears in a built prompt: the suite has to file the
// task before it can name the branch grain will give it, so it sets the
// description with an edit -- and an edited task gets a comment quoting
// the new description back ("the description changed to: ...."), which
// the prompt carries too. That copy ends in a full stop, so a parse that
// let the last occurrence win read the last field on the line as a path
// with a "." on the end, and this agent waited out its run on a gate file
// that was never going to exist.
func parseFields(prompt string) map[string]string {
	out := map[string]string{}
	for _, field := range strings.Fields(prompt) {
		if key, value, ok := strings.Cut(field, "="); ok {
			if _, seen := out[key]; !seen {
				out[key] = value
			}
		}
	}
	return out
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func fail(err error) {
	emit(map[string]any{"event": "result", "result": map[string]any{
		"conversation_id": conversation, "status": "FAILURE", "error": err.Error()}})
	os.Exit(1)
}
