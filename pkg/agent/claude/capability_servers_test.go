package claude

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/bwsalmon/grain/pkg/agent"
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
