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

package custom

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCapabilityServersReachTheMappedConfigs(t *testing.T) {
	req := Request{Prompt: "p", MCPServer: Command{Command: "/opt/grain", Args: []string{"mcpserver"}},
		MCPServers: []HTTPServer{{Name: "self-debug", Type: "http", URL: "http://127.0.0.1:4000/abc/mcp", Tools: []string{"t"}}}}

	m := &Mapping{Binary: "/bin/x", MCPConfig: MappingMCPConfig{Format: "claude-json", Arg: []string{"--mcp-config", "{file}"}}, Permissions: MappingPermissions{None: &[]string{}}}
	run, err := m.plan("/d", req)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(run.files[mappingJSONConfig], &doc); err != nil {
		t.Fatal(err)
	}
	if doc.MCPServers["self-debug"]["url"] != "http://127.0.0.1:4000/abc/mcp" || doc.MCPServers["grain-sandbox"] == nil {
		t.Errorf("claude-json = %s", run.files[mappingJSONConfig])
	}

	m.MCPConfig.Format = "codex-toml"
	run, err = m.plan("/d", req)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(run.files[mappingTOMLConfig]); !strings.Contains(got, "[mcp_servers.\"self-debug\"]\nurl = \"http://127.0.0.1:4000/abc/mcp\"\n") {
		t.Errorf("codex-toml = %s", got)
	}

	data, _ := json.Marshal(req)
	if !strings.Contains(string(data), `"mcp_servers":[{"name":"self-debug","type":"http","url":"http://127.0.0.1:4000/abc/mcp","tools":["t"]}]`) {
		t.Errorf("launch request = %s", data)
	}
}
