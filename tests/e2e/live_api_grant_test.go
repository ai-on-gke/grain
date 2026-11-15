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

package e2e

// The live cases in this file run a task that holds an api:* grant on
// each provider in the deployment's default_model chain -- Gemini through
// agy, and Claude -- so the grain-controller MCP server is attached the
// way orchestrator.wireControllerMCP attaches it, and the provider is
// handed the controller's whole tools list.
//
// That is the surface that hid #4225: grain_submit_task's grants array
// had no "items", Gemini refused the whole tools list, and none of the
// other live cases held an api:* grant, so none was ever handed it. A
// provider that refuses one declaration fails the run before the model
// says anything, so a run that completes is the tools list accepted; the
// assertion beyond that is one controller tool call that succeeded.
//
// TestMCPToolSurfacesConformToModelFunctionDeclarationRules (pkg/ui)
// checks the same declarations against the providers' documented rules on
// every pull request; these check them against the providers themselves,
// nightly, from .github/workflows/live-agent.yml and live-frameworks.yml:
//
//	GRAIN_LIVE_AGENT_TEST=1 GEMINI_API_KEY=... \
//	  go test ./tests/e2e/ -run TestLiveAgyAcceptsTheControllerToolsOfAnAPIGrant -v
//	GRAIN_LIVE_AGENT_TEST=1 CLAUDE_CODE_OAUTH_TOKEN=... \
//	  go test ./tests/e2e/ -run TestLiveClaudeAcceptsTheControllerToolsOfAnAPIGrant -v
//
// GRAIN_LIVE_AGY_MODEL and GRAIN_LIVE_CLAUDE_MODEL pick the model (the
// workflows set the cheapest); unset is each framework's default.

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/agent/claude"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// apiGrantTaskID is the task the controller acts for; it holds
// apiGrantScope and nothing else, and its title is what the model is
// asked to read back.
const (
	apiGrantTaskID    = "iss-api-grant"
	apiGrantScope     = "api:tasks:read"
	apiGrantTaskTitle = "the controller tools were accepted"
)

// attachControllerMCP files a task holding an api:* grant into w's store,
// serves the controller API over it, and returns the grain-controller MCP
// server a run of that task is given -- built as the daemon builds it
// (pkg/daemon's controllerMCPProxy and orchestrator.wireControllerMCP):
// an endpoint proxy onto POST /api/mcp that acts for the task.
func attachControllerMCP(t *testing.T, w *world) agent.CapabilityMCPServer {
	t.Helper()
	target := model.RepoRef{Owner: "acme", Name: "live"}
	tk := model.Task{
		ID: apiGrantTaskID, Intent: model.IntentImplement, Title: apiGrantTaskTitle,
		Origin:  model.Origin{Attribution: model.Attribution{Actor: human("tester")}, Reason: model.ReasonDirect},
		Binding: model.BindingDirective,
		Target:  &target,
		Grants:  []model.Grant{{Capability: apiGrantScope, Via: model.GrantByLabel}},
	}
	if err := w.store.PutTask(w.ctx, tk); err != nil {
		t.Fatalf("filing %s: %v", tk.ID, err)
	}
	daemonURL := newTestServer(t, ui.NewServer(withAccessCode(ui.Config{
		Actor: ui.DefaultActor("operator"),
	}), w.store))
	proxy, err := protocol.NewEndpointProxy(daemonURL+"/api/mcp", map[string]string{
		ui.AccessCodeHeader: testAccessCode,
		ui.ActingTaskHeader: tk.ID,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ep, err := protocol.ServeEndpoint(proxy)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ep.Close() })
	ctx, cancel := context.WithTimeout(w.ctx, 30*time.Second)
	defer cancel()
	roster, err := protocol.Handshake(ctx, ep.URL)
	if err != nil {
		t.Fatalf("grain-controller handshake: %v", err)
	}
	name := roster.ServerName
	if name == "" {
		name = mcp.ControllerToolNamespace
	}
	return agent.CapabilityMCPServer{Name: name, URL: ep.URL, Tools: roster.Tools, Handler: ep.Handler()}
}

// The harness itself, without a model: the controller this file attaches
// lists grain_get_task and answers it for the acting task. It runs on
// every pull request, so a live failure is the provider's and not this
// wiring's.
func TestControllerMCPAttachedForAnAPIGrantAnswersGetTask(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.newRepo("acme", "live")
	srv := attachControllerMCP(t, w)
	if !strings.Contains(strings.Join(srv.Tools, ","), ui.ToolGrainGetTask) {
		t.Fatalf("grain-controller roster = %v, want %s among it", srv.Tools, ui.ToolGrainGetTask)
	}
	ctx, cancel := context.WithTimeout(w.ctx, 30*time.Second)
	defer cancel()
	c, err := protocol.Dial(ctx, protocol.ServerConfig{ServerURL: srv.URL}, protocol.DialOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	raw, err := c.CallTool(ctx, ui.ToolGrainGetTask, map[string]any{"id": apiGrantTaskID})
	if err != nil {
		t.Fatalf("%s: %v", ui.ToolGrainGetTask, err)
	}
	var res struct {
		IsError bool `json:"isError"`
	}
	_ = json.Unmarshal(raw, &res)
	if res.IsError || !strings.Contains(string(raw), apiGrantTaskTitle) {
		t.Fatalf("%s(%s) = %s, want the task's title and no error", ui.ToolGrainGetTask, apiGrantTaskID, raw)
	}
}

func TestLiveAgyAcceptsTheControllerToolsOfAnAPIGrant(t *testing.T) {
	t.Parallel()
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		skipUnlessLiveRequired(t, missingLiveSecret("GEMINI_API_KEY"))
	}
	agyPath, err := exec.LookPath("agy")
	if err != nil {
		skipUnlessLiveRequired(t, "no agy binary on $PATH")
	}
	opts := []antigravity.Option{antigravity.WithAPIKey(apiKey)}
	if m := os.Getenv("GRAIN_LIVE_AGY_MODEL"); m != "" {
		opts = append(opts, antigravity.WithModel(m))
	}
	runControllerToolCall(t, antigravity.New(agyPath, buildGrainBinary(t), opts...))
}

func TestLiveClaudeAcceptsTheControllerToolsOfAnAPIGrant(t *testing.T) {
	t.Parallel()
	token := os.Getenv("CLAUDE_CODE_OAUTH_TOKEN")
	if token == "" {
		skipUnlessLiveRequired(t, missingLiveSecret("CLAUDE_CODE_OAUTH_TOKEN"))
	}
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		skipUnlessLiveRequired(t, "no claude binary on $PATH")
	}
	opts := []claude.Option{claude.WithOAuthToken(token)}
	if m := os.Getenv("GRAIN_LIVE_CLAUDE_MODEL"); m != "" {
		opts = append(opts, claude.WithModel(m))
	}
	runControllerToolCall(t, claude.New(claudePath, buildGrainBinary(t), opts...))
}

// runControllerToolCall runs fw on a task holding an api:* grant, with
// the grain-controller server attached, and fails unless the run
// completed and its grain_get_task call succeeded.
func runControllerToolCall(t *testing.T, fw agent.Framework) {
	t.Helper()
	w := newWorld(t)
	w.newRepo("acme", "live")
	srv := attachControllerMCP(t, w)

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	transcriptPath := filepath.Join(t.TempDir(), "stream.jsonl")
	result, runErr := fw.Run(ctx, agent.RunConfig{
		Prompt: "Call the " + ui.ToolGrainGetTask + " tool of the " + srv.Name + " MCP server with id \"" +
			apiGrantTaskID + "\", then reply with the task's title, exactly as the tool returned it.",
		SandboxRoot:          t.TempDir(),
		TaskID:               apiGrantTaskID,
		Grants:               []string{apiGrantScope},
		CapabilityMCPServers: []agent.CapabilityMCPServer{srv},
		MaxTurns:             8,
		TranscriptPath:       transcriptPath,
	})
	if runErr != nil {
		if raw, err := os.ReadFile(transcriptPath); err == nil {
			t.Logf("event stream:\n%s", raw)
		}
		t.Fatalf("a run handed the grain-controller tools list failed (a provider refusing a tool declaration fails here, before any call): %v", runErr)
	}
	for _, c := range result.ToolCalls {
		t.Logf("tool call: %s(%v) -> error=%v text=%q", c.Name, c.Arguments, c.IsError, c.Text)
		if strings.HasSuffix(c.Name, ui.ToolGrainGetTask) && !c.IsError && strings.Contains(c.Text, apiGrantTaskTitle) {
			return
		}
	}
	t.Fatalf("no successful %s call among the run's %d tool call(s); final answer: %q",
		ui.ToolGrainGetTask, len(result.ToolCalls), result.FinalText)
}
