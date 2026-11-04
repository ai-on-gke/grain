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

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// fakeAgyTool is one tool call a fake agy reports in its stream-json, as
// agy reports a call it made through grain's MCP server.
type fakeAgyTool struct {
	Name   string
	Args   map[string]any
	Output string
}

// writeFakeAgy writes an executable at path that stands in for the agy
// CLI, for a test that runs a real daemon with an antigravity connection
// whose CLI path (model.AgentBackend.CLIPath) is this file. These tests
// used to run a custom command connection, which grain no longer has.
//
// It answers `--version` and `agy models` (model discovery) with
// nothing, and a run by reading its prompt, running body -- a shell
// fragment, started in the run's sandbox root, which is agy's working
// directory on a host sandbox -- and then printing agy's stream-json: an
// init event, a step for each of tools, and a successful result whose
// response is response. Whatever body prints has to go to stderr or a
// file, since stdout is the stream grain parses.
func writeFakeAgy(t *testing.T, path, body, response string, tools ...fakeAgyTool) {
	t.Helper()
	const conversation = "fake-agy-conversation"
	var events []string
	add := func(event map[string]any) {
		b, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, string(b))
	}
	add(map[string]any{"event": "init", "conversation_id": conversation,
		"init": map[string]any{"tools": []string{}, "permission_mode": "always-proceed"}})
	step := 0
	for _, tool := range tools {
		name := "mcp_grain-sandbox_" + tool.Name
		add(map[string]any{"event": "step_update", "step_update": map[string]any{"conversation_id": conversation,
			"step_index": step, "step_type": "tool", "state": "ACTIVE", "tool_name": name,
			"tool_info": map[string]any{"name": name, "parameters": tool.Args}}})
		add(map[string]any{"event": "step_update", "step_update": map[string]any{"conversation_id": conversation,
			"step_index": step, "step_type": "tool", "state": "DONE", "tool_name": name,
			"tool_info": map[string]any{"name": name, "output": tool.Output}}})
		step++
	}
	add(map[string]any{"event": "step_update", "step_update": map[string]any{"conversation_id": conversation,
		"step_index": step, "step_type": "agent_response", "state": "ACTIVE", "text_delta": response}})
	add(map[string]any{"event": "step_update", "step_update": map[string]any{"conversation_id": conversation,
		"step_index": step, "step_type": "agent_response", "state": "DONE"}})
	add(map[string]any{"event": "result", "result": map[string]any{"conversation_id": conversation,
		"status": "SUCCESS", "response": response, "num_turns": 1}})

	var script strings.Builder
	script.WriteString("#!/bin/sh\n")
	script.WriteString("for arg in \"$@\"; do\n  case \"$arg\" in --version) echo 'fake agy (tests/e2e)'; exit 0;; models) exit 0;; esac\ndone\n")
	// The prompt is one stream-json user event on stdin.
	script.WriteString("IFS= read -r _ || true\n")
	script.WriteString(body)
	script.WriteString("\n")
	for _, e := range events {
		fmt.Fprintf(&script, "printf '%%s\\n' %s\n", shellQuote(e))
	}
	if err := os.WriteFile(path, []byte(script.String()), 0o755); err != nil {
		t.Fatal(err)
	}
}

// fakeAgyConnection is the antigravity connection a test with a fake agy
// at cliPath runs its tasks on.
func fakeAgyConnection(cliPath string) model.AgentBackend {
	return model.AgentBackend{
		ID: "fake", Name: "fake agent", Framework: model.AgentFrameworkAntigravity,
		AuthMode: model.AntigravityAuthAmbient, CLIPath: cliPath,
		// Pinned, so nothing waits on asking the fake which models it
		// serves.
		Models: []string{"fake-model"},
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
