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
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/agent/antigravity/hook"
	"github.com/ai-on-gke/grain/pkg/agent/hookpolicy"
	"github.com/ai-on-gke/grain/pkg/mcp"
)

// A host advertising FeatureHookPolicy gets hook-policy.json staged in the
// run's scratch directory and -policy in hooks.json, so an agentd binary that
// never registered the daemon's HostToolGrant still admits and records it;
// an older host without FeatureHookPolicy gets neither the file nor -policy,
// keeping today's -grant argv.
func TestHookPolicyStagedOnlyForHostsWithFeatureHookPolicy(t *testing.T) {
	ctx := context.Background()
	const editionGrant = "https://grain.example/capabilities/edition#lookup={}"
	unregister := RegisterHostToolGrant(HostToolGrant{
		Capability: editionGrant,
		Tools:      []string{"edition_lookup"},
	})
	registered := true
	t.Cleanup(func() {
		if registered {
			unregister()
		}
	})

	cfg := agent.RunConfig{
		TaskID: "4212",
		RunID:  "run-7",
		Grants: []string{editionGrant},
		CapabilityMCPServers: []agent.CapabilityMCPServer{
			{Name: "acme", URL: "http://127.0.0.1:9999/mcp", Tools: []string{"ping"}},
		},
	}

	// 1. Host with FeatureHookPolicy: stages hook-policy.json and passes -policy.
	capableHost := agent.HostdHost{HookPolicy: true}
	home, _, cleanup, err := writeAgyHomeFor(ctx, capableHost, nil, "", cfg.TaskID, "/opt/grain-agentd", "http://127.0.0.1:8080/mcp", nil, "gemini", cfg)
	if err != nil {
		t.Fatalf("writeAgyHomeFor(capable): %v", err)
	}
	defer cleanup()

	policyPath := filepath.Join(home, hookPolicyRelPath)
	loaded, err := hookpolicy.Load(policyPath)
	if err != nil || loaded == nil || loaded.Tool == nil {
		t.Fatalf("staged policy at %s: %+v, %v", policyPath, loaded, err)
	}
	hooksJSON, err := os.ReadFile(filepath.Join(home, hooksConfigRelPath))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(hooksJSON), "'-policy' '"+policyPath+"'") {
		t.Errorf("capable hooks.json = %s, want -policy %s", hooksJSON, policyPath)
	}
	if strings.Contains(string(hooksJSON), "'-grant'") {
		t.Errorf("capable hooks.json = %s, should carry grants in policy file rather than -grant argv", hooksJSON)
	}

	// Unregister the grant to simulate a grain-agentd binary that never linked it:
	// evaluating the staged policy file still admits and records edition_lookup.
	unregister()
	registered = false

	opts := HookOptions{PolicyPath: policyPath}
	if got := hookVerdict(t, HookDecisionFor(opts, hookPayload(t, "edition_lookup", map[string]any{"query": "staged"}))); got != "no opinion" {
		t.Errorf("edition_lookup via staged policy = %q, want no opinion", got)
	}
	if got := hookVerdict(t, HookDecisionFor(opts, hookPayload(t, mcp.AgyQualifiedToolName("resume_later"), nil))); got != "no opinion" {
		t.Errorf("resume_later via staged policy = %q, want no opinion", got)
	}
	if got := hookVerdict(t, HookDecisionFor(opts, hookPayload(t, "mcp_acme_ping", nil))); got != "no opinion" {
		t.Errorf("capability server tool via staged policy = %q, want no opinion", got)
	}
	if got := hookVerdict(t, HookDecisionFor(opts, hookPayload(t, "run_command", nil))); got != "deny" {
		t.Errorf("native run_command via staged policy = %q, want deny", got)
	}
	calls := loadHostToolCalls(filepath.Join(runScratchDir(home), hook.HostToolCallsFilename))
	if len(calls) != 1 || calls[0].Tool != "edition_lookup" || calls[0].Task != "4212" || calls[0].Run != "run-7" {
		t.Errorf("recorded host-tool calls = %+v", calls)
	}

	// Re-register for the legacy-host half of the test.
	t.Cleanup(RegisterHostToolGrant(HostToolGrant{
		Capability: editionGrant,
		Tools:      []string{"edition_lookup"},
	}))

	// 2. Older host without FeatureHookPolicy: no hook-policy.json staged, no -policy in hooks.json.
	olderHost := agent.HostdHost{HookPolicy: false}
	oldHome, _, oldCleanup, err := writeAgyHomeFor(ctx, olderHost, nil, "", cfg.TaskID, "/opt/old-agentd", "http://127.0.0.1:8080/mcp", nil, "gemini", cfg)
	if err != nil {
		t.Fatalf("writeAgyHomeFor(older): %v", err)
	}
	defer oldCleanup()

	if _, err := os.Stat(filepath.Join(oldHome, hookPolicyRelPath)); !os.IsNotExist(err) {
		t.Errorf("older host got a staged hook-policy.json (stat err = %v)", err)
	}
	oldHooksJSON, err := os.ReadFile(filepath.Join(oldHome, hooksConfigRelPath))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(oldHooksJSON), "'-policy'") {
		t.Errorf("older host hooks.json = %s, must not pass -policy", oldHooksJSON)
	}
	var doc map[string]struct {
		PreToolUse []struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"PreToolUse"`
	}
	if err := json.Unmarshal(oldHooksJSON, &doc); err != nil {
		t.Fatal(err)
	}
	cmd := doc[HookName].PreToolUse[0].Hooks[0].Command
	if !strings.Contains(cmd, "'-grant' '"+editionGrant+"'") {
		t.Errorf("older host command = %s, want legacy -grant", cmd)
	}
}
