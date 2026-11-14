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
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/mcp"
)

// A run's credential reaches the forked mcpserver in its environment,
// and never on its argv.
func TestTheRunCredentialIsInTheMCPServersEnvNotItsArgs(t *testing.T) {
	cfg := agent.RunConfig{SandboxRoot: t.TempDir(), RunCredential: "grr_test"}
	f := newFramework(nil, "/bin/grain")
	args, err := f.mcpServerArgs(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(args, func(a string) bool { return strings.Contains(a, "grr_test") }) {
		t.Fatalf("the run credential is on the mcpserver's argv: %q", args)
	}
	ctx := mcp.WithServerEnv(context.Background(), agent.RunCredentialEnv(cfg)...)
	data, err := mcpConfigJSONEnv("/bin/grain", args, mcp.ServerEnvMap(ctx))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		MCPServers map[string]struct {
			Env map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if got := doc.MCPServers[mcpServerName].Env[mcp.RunCredentialEnv]; got != "grr_test" {
		t.Fatalf("%s in the mcpserver's env = %q, want grr_test (%s)", mcp.RunCredentialEnv, got, data)
	}
	// No credential, no env.
	if data, _ := mcpConfigJSON("/bin/grain", args); strings.Contains(string(data), `"env"`) {
		t.Fatalf("a run with no credential got an env: %s", data)
	}
}
