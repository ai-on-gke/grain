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
