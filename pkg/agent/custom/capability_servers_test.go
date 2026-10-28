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
