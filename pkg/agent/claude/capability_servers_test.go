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
	"encoding/json"
	"slices"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
)

func TestCapabilityServersAreWiredBesideGrainSandbox(t *testing.T) {
	servers := []agent.CapabilityMCPServer{{Name: "self-debug", URL: "http://127.0.0.1:4000/abc/mcp", Tools: []string{"read_grain_source", "list_grain_source"}}}
	data, err := mcpConfigJSON("/bin/grain", []string{"mcpserver"}, servers...)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.MCPServers[mcpServerName]["command"] != "/bin/grain" {
		t.Errorf("grain-sandbox entry = %v", doc.MCPServers[mcpServerName])
	}
	if got := doc.MCPServers["self-debug"]; got["type"] != "http" || got["url"] != "http://127.0.0.1:4000/abc/mcp" {
		t.Errorf("self-debug entry = %v", got)
	}
	allowed := allowedTools(servers...)
	for _, want := range []string{"mcp__self-debug__read_grain_source", "mcp__self-debug__list_grain_source"} {
		if !slices.Contains(allowed, want) {
			t.Errorf("--allowedTools does not admit %s", want)
		}
	}
	if len(allowed) != len(allowedTools())+2 {
		t.Errorf("--allowedTools admits %d names, want grain's own plus 2", len(allowed))
	}
}
