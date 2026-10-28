package codex

import (
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/mcp"
)

func TestCapabilityServersAreWrittenIntoConfigTOML(t *testing.T) {
	f := New("/bin/codex", "/bin/grain")
	servers := []agent.CapabilityMCPServer{{Name: "self-debug", URL: "http://127.0.0.1:4000/abc/mcp", Tools: []string{"read_grain_source"}}}
	data, err := f.configTOML(&mcp.ManagedHTTP{URL: "http://127.0.0.1:1/mcp", Token: "t"}, servers...)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{
		"[mcp_servers.\"self-debug\"]\nurl = \"http://127.0.0.1:4000/abc/mcp\"\nrequired = true\n",
		"enabled_tools = [\"read_grain_source\"]\n",
		"[mcp_servers.\"self-debug\".tools.\"read_grain_source\"]\napproval_mode = \"approve\"\n",
		"[mcp_servers." + mcpServerName + "]\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("config.toml lacks %q:\n%s", want, got)
		}
	}
}
