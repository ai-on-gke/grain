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

package daemon

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite/sqlitetest"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

type controllerMCPTestFramework struct {
	run func(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error)
}

func (f controllerMCPTestFramework) Name() string { return "claude" }

func (f controllerMCPTestFramework) Run(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	return f.run(ctx, cfg)
}

// TestSandboxMCPExposesNoAPIToolsAndControllerMCPServesAPIGrants verifies Step 4:
// grain-sandbox (GrantedDaemonTools) no longer exposes api_* tools, while
// built-in runs/sessions holding api:* grants get "grain-controller" attached
// via CapabilityMCPServers -> POST /api/mcp with daemon auth and
// X-Grain-Acting-Task: <taskID>, and grain_invoke unwraps into run telemetry.
func TestSandboxMCPExposesNoAPIToolsAndControllerMCPServesAPIGrants(t *testing.T) {
	store, db := sqlitetest.Open(t)
	ctx := t.Context()
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

	dataDir := t.TempDir()
	tokenPath := ui.LocalAPITokenPath(dataDir)
	tok, err := ui.EnsureLocalAPIToken(tokenPath)
	if err != nil {
		t.Fatalf("EnsureLocalAPIToken: %v", err)
	}

	uiCfg := ui.Config{
		Actor:             ui.DefaultActor("operator"),
		SourceDB:          db,
		DBDriver:          "sqlite",
		DataDir:           dataDir,
		LocalAPIToken:     tok,
		LocalAPITokenFile: tokenPath,
		Capabilities:      append(ui.OfferedCapabilities(), ui.APICapabilities()...),
	}
	uiSrv := ui.NewServer(uiCfg, store)
	srv := httptest.NewServer(uiSrv)
	defer srv.Close()
	// As startUIServer does: grain-controller reaches /api/mcp through
	// LiveAPI, in process, not over the listener.
	LiveAPI.Set(uiSrv)
	t.Cleanup(func() { LiveAPI.handler.Store(nil) })

	// 1. grain-sandbox must not include any api_* tools even when api:* grants are passed.
	sandboxTools := GrantedDaemonTools(
		GrantNames{"api:task-admin", "api:ci:read"},
		"", "", "", "42",
		&ui.HTTPClient{BaseURL: srv.URL},
	)
	for _, tool := range sandboxTools {
		if strings.HasPrefix(tool.Name, "api_") {
			t.Fatalf("GrantedDaemonTools still advertised %q on grain-sandbox", tool.Name)
		}
	}

	// 2. Seed two tasks: one with api:tasks:read + api:ci:read, and one without api:* grants.
	apiTask := model.Task{
		ID:          "42",
		Title:       "Director Session",
		Body:        "Inspect tasks and CI.",
		Interactive: true,
		CreatedAt:   &now,
		Grants: []model.Grant{
			{Capability: "api:tasks:read", Via: model.GrantByLabel},
			{Capability: "api:ci:read", Via: model.GrantByLabel},
		},
	}
	plainTask := model.Task{
		ID:          "43",
		Title:       "Plain Coding Task",
		Body:        "Fix typo.",
		Interactive: true,
		CreatedAt:   &now,
	}
	for _, tk := range []model.Task{apiTask, plainTask} {
		if err := store.PutTask(ctx, tk); err != nil {
			t.Fatalf("PutTask(%s): %v", tk.ID, err)
		}
		if err := store.Approve(ctx, tk.ID, model.Attribution{Actor: ui.DefaultActor("operator")}, now); err != nil {
			t.Fatalf("Approve(%s): %v", tk.ID, err)
		}
	}

	daemonCfg := Config{DataDir: dataDir}
	orchCfg := orchestrator.Config{
		ControllerMCP: controllerMCPProxy(daemonCfg),
		Now:           func() time.Time { return now },
	}

	// 3. Dispatch plainTask: must NOT attach grain-controller.
	plainD := dispatch.Dispatch{TaskID: plainTask.ID, RunID: "run-plain-1", Attempt: 1}
	if err := store.StartRun(ctx, model.Run{ID: plainD.RunID, TaskID: plainTask.ID, Attempt: 1, StartedAt: now}, model.Limits{}); err != nil {
		t.Fatalf("StartRun(plain): %v", err)
	}
	plainFW := controllerMCPTestFramework{
		run: func(_ context.Context, runCfg agent.RunConfig) (*agent.Result, error) {
			if len(runCfg.CapabilityMCPServers) != 0 {
				t.Errorf("plain task received CapabilityMCPServers = %+v, want empty", runCfg.CapabilityMCPServers)
			}
			return &agent.Result{FinalText: "done"}, nil
		},
	}
	if _, err := orchestrator.RunDispatch(ctx, store, plainFW, orchCfg, plainTask, plainD, mcp.NewSandboxTools(t.TempDir()), t.TempDir(), "", nil, now); err != nil {
		t.Fatalf("RunDispatch(plain): %v", err)
	}

	// 4. Dispatch apiTask: must attach grain-controller with the 6 fixed tools,
	// enforce task grants via X-Grain-Acting-Task, and unwrap grain_invoke in telemetry.
	apiD := dispatch.Dispatch{TaskID: apiTask.ID, RunID: "run-api-1", Attempt: 1}
	if err := store.StartRun(ctx, model.Run{ID: apiD.RunID, TaskID: apiTask.ID, Attempt: 1, StartedAt: now}, model.Limits{}); err != nil {
		t.Fatalf("StartRun(api): %v", err)
	}
	apiFW := controllerMCPTestFramework{
		run: func(rctx context.Context, runCfg agent.RunConfig) (*agent.Result, error) {
			if len(runCfg.CapabilityMCPServers) != 1 {
				t.Fatalf("CapabilityMCPServers = %+v, want 1 server (grain-controller)", runCfg.CapabilityMCPServers)
			}
			server := runCfg.CapabilityMCPServers[0]
			if server.Name != ui.ControllerMCPServerName {
				t.Fatalf("server.Name = %q, want %q", server.Name, ui.ControllerMCPServerName)
			}
			wantTools := ui.ControllerMCPToolNames()
			if !slices.Equal(server.Tools, wantTools) {
				t.Fatalf("server.Tools = %v, want %v", server.Tools, wantTools)
			}

			client, err := protocol.Dial(rctx, protocol.ServerConfig{ServerURL: server.URL}, protocol.DialOptions{})
			if err != nil {
				t.Fatalf("protocol.Dial: %v", err)
			}
			defer client.Close()

			// Explore scopes: only tasks and ci should be visible for task 42's grants.
			exploreRaw, err := client.CallTool(rctx, ui.ToolGrainExploreScope, map[string]any{})
			if err != nil {
				t.Fatalf("grain_explore_scope: %v", err)
			}
			var groups struct {
				Groups []struct {
					Group string `json:"group"`
				} `json:"groups"`
			}
			if err := json.Unmarshal(exploreRaw, &groups); err != nil {
				t.Fatalf("unmarshal explore scopes: %v", err)
			}
			var domains []string
			for _, g := range groups.Groups {
				domains = append(domains, g.Group)
			}
			if !slices.Contains(domains, "tasks") || !slices.Contains(domains, "ci") || slices.Contains(domains, "settings") {
				t.Fatalf("authorized domains for task 42 = %v, want tasks & ci without settings", domains)
			}

			// Authorized invoke (api_get_task) succeeds; unauthorized invoke (api_get_config) fails.
			invokeOK, err := client.CallTool(rctx, ui.ToolGrainInvoke, map[string]any{
				"tool":      "api_get_task",
				"arguments": map[string]any{"id": "43"},
			})
			if err != nil {
				t.Fatalf("authorized grain_invoke(api_get_task) failed: %v", err)
			}
			if _, err := client.CallTool(rctx, ui.ToolGrainInvoke, map[string]any{
				"tool": "api_get_config",
			}); err == nil {
				t.Fatal("unauthorized grain_invoke(api_get_config) succeeded, want error")
			}

			return &agent.Result{
				FinalText: "checked task 43",
				ToolCalls: []agent.ToolCall{
					{
						Name: "mcp__grain-controller__grain_invoke",
						Arguments: map[string]any{
							"tool":      "api_get_task",
							"arguments": map[string]any{"id": "43"},
						},
						Text: string(invokeOK),
					},
				},
			}, nil
		},
	}
	if _, err := orchestrator.RunDispatch(ctx, store, apiFW, orchCfg, apiTask, apiD, mcp.NewSandboxTools(t.TempDir()), t.TempDir(), "", nil, now); err != nil {
		t.Fatalf("RunDispatch(api): %v", err)
	}

	uses, err := store.RunToolUses(ctx)
	if err != nil {
		t.Fatalf("RunToolUses: %v", err)
	}
	var sawUnwrapped bool
	for _, u := range uses {
		if u.RunID == apiD.RunID && u.Tool == "api_get_task" && u.Calls == 1 {
			sawUnwrapped = true
		}
	}
	if !sawUnwrapped {
		t.Fatalf("RunToolUses = %+v, want unwrapped api_get_task recorded for %s", uses, apiD.RunID)
	}
}
