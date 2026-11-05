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
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/agent/antigravity/hook"
	"github.com/ai-on-gke/grain/pkg/mcp"
)

// TestHookToolListsMatchTheRoster holds the leaf hook package's own copy
// of grain's tool roster to the one pkg/mcp builds. The hook keeps a copy
// so that grain-agentd, which answers it on a machine that is not the
// daemon's, need not link pkg/mcp; a tool the copy lacks would be denied
// to every agy run that hook answers for.
func TestHookToolListsMatchTheRoster(t *testing.T) {
	if want := agent.PublishedToolNames(); !slices.Equal(hook.PublishedTools, want) {
		t.Errorf("hook.PublishedTools has drifted from agent.PublishedToolNames; set it to:\n%s", goStrings(want))
	}
	var sandbox []string
	for _, tool := range mcp.NewSandboxTools("") {
		sandbox = append(sandbox, tool.Name)
	}
	if !slices.Equal(hook.SandboxTools, sandbox) {
		t.Errorf("hook.SandboxTools has drifted from mcp.NewSandboxTools; set it to:\n%s", goStrings(sandbox))
	}
	// The namespace: a published tool, spelled as pkg/mcp spells it for
	// agy, is one the hook leaves alone.
	payload := fmt.Sprintf(`{"toolCall":{"name":%q}}`, mcp.AgyQualifiedToolName("run_command"))
	if got := hook.Decision([]byte(payload)); len(got) != 0 {
		t.Errorf("the hook answered %s for %s; the namespace it spells grain's tools in has drifted from mcp.ToolNamespace", got, mcp.AgyQualifiedToolName("run_command"))
	}
	if HookSubcommand != hook.Subcommand || HookName != hook.Name {
		t.Errorf("antigravity's hook names are not the hook package's")
	}
}

func goStrings(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = fmt.Sprintf("%q", n)
	}
	return "[]string{" + strings.Join(quoted, ", ") + "}"
}
