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

package ui_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func callControllerMCP(t *testing.T, srv *ui.Server, headers map[string]string, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/mcp", strings.NewReader(body))
	req.RemoteAddr = "203.0.113.42:51234"
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func callControllerTool(t *testing.T, srv *ui.Server, headers map[string]string, tool string, args map[string]any) (string, bool) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      tool,
			"arguments": args,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := callControllerMCP(t, srv, headers, string(payload))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/mcp status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding JSON-RPC response: %v (%s)", err, rec.Body.String())
	}
	if resp.Error != nil {
		t.Fatalf("unexpected JSON-RPC protocol error: %+v", resp.Error)
	}
	if len(resp.Result.Content) == 0 {
		return "", resp.Result.IsError
	}
	return resp.Result.Content[0].Text, resp.Result.IsError
}

func newSingleTenantControllerMCPServer(t *testing.T) (*ui.Server, *ui.Client, string) {
	t.Helper()
	client, _, _ := testClient(t)
	tokenPath := filepath.Join(t.TempDir(), "local-api-token")
	code, err := ui.EnsureLocalAPIToken(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(tokenPath, 0o600); err != nil {
		t.Fatal(err)
	}
	client.Config.LocalAPITokenFile = tokenPath
	client.Config.Actor = model.Principal{Kind: model.PrincipalHuman, ID: "owner@example.com"}
	storeSettingsRow(t, client)
	return ui.NewServerWithClient(client), client, code
}

func TestControllerMCPAuthInitializeToolsAndNotifications(t *testing.T) {
	t.Parallel()
	srv, _, code := newSingleTenantControllerMCPServer(t)

	initReq := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`

	// Unauthenticated caller is refused 401.
	if rec := callControllerMCP(t, srv, nil, initReq); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401: %s", rec.Code, rec.Body.String())
	}

	// X-Grain-Access-Code fallback succeeds even when Authorization carries an upstream proxy JWT.
	authHeaders := map[string]string{
		"Authorization":     "Bearer eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.upstream.sig",
		ui.AccessCodeHeader: code,
	}
	rec := callControllerMCP(t, srv, authHeaders, initReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("initialize with X-Grain-Access-Code status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var initResp struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
			Instructions    string `json:"instructions"`
			ServerInfo      struct {
				Name string `json:"name"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &initResp); err != nil {
		t.Fatal(err)
	}
	if initResp.Result.ProtocolVersion != "2025-03-26" {
		t.Errorf("protocolVersion = %q, want 2025-03-26", initResp.Result.ProtocolVersion)
	}
	if initResp.Result.ServerInfo.Name != ui.ControllerMCPServerName {
		t.Errorf("serverInfo.name = %q, want %q", initResp.Result.ServerInfo.Name, ui.ControllerMCPServerName)
	}
	if !strings.Contains(initResp.Result.Instructions, "grain_explore_scope") ||
		!strings.Contains(initResp.Result.Instructions, "grain_invoke") {
		t.Errorf("instructions missing workflow guide: %q", initResp.Result.Instructions)
	}

	// Notification returns 202 Accepted with empty body.
	notifRec := callControllerMCP(t, srv, authHeaders, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if notifRec.Code != http.StatusAccepted || notifRec.Body.Len() != 0 {
		t.Fatalf("notification = %d %q, want 202 empty", notifRec.Code, notifRec.Body.String())
	}

	// tools/list returns the 6 fixed tools with _meta.antigravity/alwaysLoad = true.
	listRec := callControllerMCP(t, srv, authHeaders, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if listRec.Code != http.StatusOK {
		t.Fatalf("tools/list = %d: %s", listRec.Code, listRec.Body.String())
	}
	var listResp struct {
		Result struct {
			Tools []struct {
				Name string         `json:"name"`
				Meta map[string]any `json:"_meta"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listResp); err != nil {
		t.Fatal(err)
	}
	var gotNames []string
	for _, tool := range listResp.Result.Tools {
		gotNames = append(gotNames, tool.Name)
		if alwaysLoad, _ := tool.Meta["antigravity/alwaysLoad"].(bool); !alwaysLoad {
			t.Errorf("tool %s missing _meta.antigravity/alwaysLoad=true: %+v", tool.Name, tool.Meta)
		}
	}
	if !slices.Equal(gotNames, ui.ControllerMCPToolNames()) {
		t.Fatalf("tools/list = %v, want %v", gotNames, ui.ControllerMCPToolNames())
	}
}

func TestControllerMCPExploreAndInvokeScopeFilteringAndInnerAuthPreservation(t *testing.T) {
	t.Parallel()
	srv, client, code := newSingleTenantControllerMCPServer(t)
	seedAPITask(t, client, "10", "acme/widgets", "api:ci:read")
	seedAPITask(t, client, "11", "acme/widgets", "api:tasks:read")

	humanHeaders := map[string]string{ui.AccessCodeHeader: code}
	ciAgentHeaders := map[string]string{
		ui.AccessCodeHeader: code,
		ui.ActingTaskHeader: "10",
	}

	// Human/BYO caller sees multiple non-internal scope groups, never "internal".
	humanOverview, isErr := callControllerTool(t, srv, humanHeaders, ui.ToolGrainExploreScope, map[string]any{})
	if isErr {
		t.Fatalf("human explore overview failed: %s", humanOverview)
	}
	if strings.Contains(humanOverview, `"internal"`) || !strings.Contains(humanOverview, `"tasks"`) || !strings.Contains(humanOverview, `"settings"`) {
		t.Fatalf("unexpected human scope overview: %s", humanOverview)
	}

	// Acting task 10 (holding only api:ci:read) sees ONLY the ci group.
	agentOverview, isErr := callControllerTool(t, srv, ciAgentHeaders, ui.ToolGrainExploreScope, map[string]any{})
	if isErr {
		t.Fatalf("agent explore overview failed: %s", agentOverview)
	}
	if !strings.Contains(agentOverview, `"ci:read"`) || strings.Contains(agentOverview, `"settings:write"`) || strings.Contains(agentOverview, `"tasks:write"`) {
		t.Fatalf("acting task saw unauthorized scopes in overview: %s", agentOverview)
	}

	// Exploring an unauthorized scope returns an explicit grant error.
	deniedDetail, isErr := callControllerTool(t, srv, ciAgentHeaders, ui.ToolGrainExploreScope, map[string]any{"scope": "settings"})
	if !isErr || !strings.Contains(deniedDetail, "not authorized") {
		t.Fatalf("expected unauthorized error exploring settings, got isError=%v: %s", isErr, deniedDetail)
	}

	// Exploring authorized scope "ci" returns schemas for ci:read tools.
	ciDetail, isErr := callControllerTool(t, srv, ciAgentHeaders, ui.ToolGrainExploreScope, map[string]any{"scope": "ci"})
	if isErr || !strings.Contains(ciDetail, "api_list_ci_runs") || !strings.Contains(ciDetail, `"inputSchema"`) {
		t.Fatalf("expected ci schemas in explore detail, got isError=%v: %s", isErr, ciDetail)
	}

	// Invoking unauthorized route is refused with 403.
	invDenied, isErr := callControllerTool(t, srv, ciAgentHeaders, ui.ToolGrainInvoke, map[string]any{
		"tool": "api_get_settings",
	})
	if !isErr || !strings.Contains(invDenied, "403") {
		t.Fatalf("expected 403 invoking api_get_settings as ci:read task, got isError=%v: %s", isErr, invDenied)
	}

	// Invoking authorized route succeeds, and inner dispatch preserves X-Grain-Acting-Task
	// (so attempting to comment as task 11 with only tasks:read is refused by agentAPIGate).
	readHeaders := map[string]string{
		ui.AccessCodeHeader: code,
		ui.ActingTaskHeader: "11",
	}
	listViaInvoke, isErr := callControllerTool(t, srv, readHeaders, ui.ToolGrainInvoke, map[string]any{
		"tool":      "api_list_tasks",
		"arguments": map[string]any{"query": map[string]any{"fields": "summary", "limit": 10}},
	})
	if isErr || !strings.Contains(listViaInvoke, `"task 10"`) {
		t.Fatalf("expected api_list_tasks via grain_invoke to succeed, got isError=%v: %s", isErr, listViaInvoke)
	}

	commentDenied, isErr := callControllerTool(t, srv, readHeaders, ui.ToolGrainInvoke, map[string]any{
		"tool":      "api_add_comment",
		"arguments": map[string]any{"id": "10", "body": map[string]any{"body": "sneaky"}},
	})
	if !isErr || !strings.Contains(commentDenied, "403") {
		t.Fatalf("expected tasks:write refusal on api_add_comment, got isError=%v: %s", isErr, commentDenied)
	}
}

func TestControllerMCPTaskWorkflowTools(t *testing.T) {
	t.Parallel()
	srv, client, code := newSingleTenantControllerMCPServer(t)
	if _, err := client.AddTargetRepo(context.Background(), "acme/widgets"); err != nil {
		t.Fatal(err)
	}
	humanHeaders := map[string]string{ui.AccessCodeHeader: code}

	// 1. grain_filing_context returns targetRepos and submitDefaults.approve=false.
	filingCtx, isErr := callControllerTool(t, srv, humanHeaders, ui.ToolGrainFilingContext, map[string]any{})
	if isErr || !strings.Contains(filingCtx, "acme/widgets") {
		t.Fatalf("grain_filing_context failed (isError=%v): %s", isErr, filingCtx)
	}

	// 2. grain_submit_task defaults approve to false -> lands in "proposed" state.
	longDesc := strings.Repeat("Detailed implementation notes for acme/widgets.\n", 80) // > 3 KB raw description
	submittedText, isErr := callControllerTool(t, srv, humanHeaders, ui.ToolGrainSubmitTask, map[string]any{
		"title":       "Implement controller MCP",
		"description": longDesc,
		"repo":        "acme/widgets",
	})
	if isErr {
		t.Fatalf("grain_submit_task failed: %s", submittedText)
	}
	var submitted struct {
		ID       string      `json:"id"`
		State    model.State `json:"state"`
		Approved bool        `json:"approved"`
	}
	if err := json.Unmarshal([]byte(submittedText), &submitted); err != nil {
		t.Fatalf("decoding submit response: %v (%s)", err, submittedText)
	}
	if submitted.State != model.StateProposed || submitted.Approved {
		t.Fatalf("grain_submit_task without approve landed state=%q approved=%v, want proposed/false", submitted.State, submitted.Approved)
	}

	// 3. grain_submit_task with approve=true queues immediately for human caller,
	//    but is refused for an acting task holding only api:tasks:write.
	queuedText, isErr := callControllerTool(t, srv, humanHeaders, ui.ToolGrainSubmitTask, map[string]any{
		"title":       "Approved task",
		"description": "Ready to run",
		"repo":        "acme/widgets",
		"approve":     true,
	})
	if isErr || !strings.Contains(queuedText, `"queued"`) {
		t.Fatalf("grain_submit_task with approve=true failed (isError=%v): %s", isErr, queuedText)
	}

	seedAPITask(t, client, "20", "acme/widgets", "api:tasks:write")
	agentWriteHeaders := map[string]string{
		ui.AccessCodeHeader: code,
		ui.ActingTaskHeader: "20",
	}
	agentApproveText, isErr := callControllerTool(t, srv, agentWriteHeaders, ui.ToolGrainSubmitTask, map[string]any{
		"title":       "Agent trying self-approve",
		"description": "Should be refused",
		"repo":        "acme/widgets",
		"approve":     true,
	})
	if !isErr || !strings.Contains(agentApproveText, "tasks:admin") {
		t.Fatalf("expected tasks:admin refusal when tasks:write agent sets approve=true, got isError=%v: %s", isErr, agentApproveText)
	}

	// 4. grain_list_tasks returns compact summary list without dumping the 3 KB description.
	listedText, isErr := callControllerTool(t, srv, humanHeaders, ui.ToolGrainListTasks, map[string]any{
		"state": "proposed",
		"q":     "controller MCP",
	})
	if isErr || !strings.Contains(listedText, submitted.ID) || strings.Contains(listedText, "Detailed implementation notes") {
		t.Fatalf("unexpected grain_list_tasks output (isError=%v): %s", isErr, listedText)
	}

	// 5. grain_get_task defaults to compact ~1 KB summary view, and supports conversation + full.
	if err := client.AddComment(context.Background(), submitted.ID, "Please check edge cases.", nil); err != nil {
		t.Fatal(err)
	}
	summaryView, isErr := callControllerTool(t, srv, humanHeaders, ui.ToolGrainGetTask, map[string]any{
		"id": submitted.ID,
	})
	if isErr {
		t.Fatalf("grain_get_task summary failed: %s", summaryView)
	}
	if len(bytes.TrimSpace([]byte(summaryView))) > 1500 {
		t.Fatalf("grain_get_task default summary view too large (%d bytes, want ~1 KB): %s", len(summaryView), summaryView)
	}
	if !strings.Contains(summaryView, "Implement controller MCP") || !strings.Contains(summaryView, "State: proposed") {
		t.Fatalf("unexpected summary view: %s", summaryView)
	}

	convView, isErr := callControllerTool(t, srv, humanHeaders, ui.ToolGrainGetTask, map[string]any{
		"id":   submitted.ID,
		"view": "conversation",
	})
	if isErr || !strings.Contains(convView, "Please check edge cases.") {
		t.Fatalf("unexpected conversation view (isError=%v): %s", isErr, convView)
	}

	fullView, isErr := callControllerTool(t, srv, humanHeaders, ui.ToolGrainGetTask, map[string]any{
		"id":   submitted.ID,
		"view": "full",
	})
	if isErr || !strings.Contains(fullView, "Detailed implementation notes") {
		t.Fatalf("unexpected full view (isError=%v): %d bytes", isErr, len(fullView))
	}
}
