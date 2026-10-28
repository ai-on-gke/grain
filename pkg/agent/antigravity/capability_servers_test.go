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

package antigravity

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
)

func TestCapabilityServersAreConfiguredAllowedAndAdmitted(t *testing.T) {
	servers := []agent.CapabilityMCPServer{{Name: "self-debug", URL: "http://127.0.0.1:4000/abc/mcp", Tools: []string{"read_grain_source"}}}
	cfg := agent.RunConfig{TaskID: "1", CapabilityMCPServers: servers}
	files, err := agyHomeFilesWith("/bin/grain", []string{"mcpserver"}, "", false, servers, hookArgs(cfg, "/h")...)
	if err != nil {
		t.Fatal(err)
	}

	var mcpDoc struct {
		MCPServers map[string]struct {
			ServerURL      string                     `json:"serverUrl"`
			TimeoutSeconds int                        `json:"timeoutSeconds"`
			Tools          map[string]map[string]bool `json:"tools"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(files[mcpConfigRelPath], &mcpDoc); err != nil {
		t.Fatal(err)
	}
	entry := mcpDoc.MCPServers["self-debug"]
	if entry.ServerURL != "http://127.0.0.1:4000/abc/mcp" || entry.TimeoutSeconds <= 0 || !entry.Tools["read_grain_source"]["eager"] {
		t.Errorf("self-debug entry = %+v", entry)
	}
	if _, ok := mcpDoc.MCPServers[mcpServerName]; !ok {
		t.Error("grain-sandbox is gone from mcp_config.json")
	}

	var settings struct {
		Permissions struct {
			Allow []string `json:"allow"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(files[cliSettingsRelPath], &settings); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(settings.Permissions.Allow, "mcp(self-debug/read_grain_source)") {
		t.Errorf("permissions.allow = %v", settings.Permissions.Allow)
	}

	opts, ignored := ParseHookArgs(hookArgs(cfg, "/h"))
	if len(ignored) > 0 {
		t.Fatalf("hook args not understood: %v", ignored)
	}
	call := func(name string) []byte {
		payload, _ := json.Marshal(map[string]any{"toolCall": map[string]any{"name": name}})
		return HookDecisionFor(opts, payload)
	}
	if got := call("mcp_self-debug_read_grain_source"); len(got) != 0 {
		t.Errorf("the granted capability tool was not admitted: %s", got)
	}
	if got := call("mcp_self-debug_write_file"); len(got) == 0 {
		t.Error("a tool the capability server never reported was admitted")
	}
	if got := HookDecision([]byte(`{"toolCall":{"name":"mcp_self-debug_read_grain_source"}}`)); len(got) == 0 {
		t.Error("a run holding no capability grant admitted a capability tool")
	}
}
