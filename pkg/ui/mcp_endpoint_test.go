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
	"maps"
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

// An external agent token is the credential on POST /api/mcp as it is on
// every other route: X-Grain-Acting-Task on its request names nothing,
// so a token holder cannot read or act with a task's grants by naming
// the task.
func TestControllerMCPExternalTokenIgnoresActingTaskHeader(t *testing.T) {
	t.Parallel()
	srv, client, _ := newSingleTenantControllerMCPServer(t)
	enableExternalAgentAPI(t, client.Store)
	seedAPITask(t, client, "30", "acme/widgets", "api:settings:read", "api:tasks:read")

	humanCtx := ui.WithPrincipal(context.Background(), model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com", Source: model.SourceIAP})
	issued, err := client.CreateExternalAgentToken(humanCtx, ui.CreateExternalAgentTokenRequest{
		Name:     "byo-harness",
		ScopeSet: "grain/ci-read",
	})
	if err != nil {
		t.Fatalf("CreateExternalAgentToken: %v", err)
	}
	spoofed := map[string]string{
		"Authorization":     "Bearer " + issued.Token,
		ui.ActingTaskHeader: "30",
	}

	overview, isErr := callControllerTool(t, srv, spoofed, ui.ToolGrainExploreScope, map[string]any{})
	if isErr {
		t.Fatalf("explore as external token failed: %s", overview)
	}
	if !strings.Contains(overview, `"ci:read"`) || strings.Contains(overview, `"settings:read"`) || strings.Contains(overview, `"tasks:read"`) {
		t.Fatalf("external token naming task 30 saw the task's scopes, not its own: %s", overview)
	}
	if text, isErr := callControllerTool(t, srv, spoofed, ui.ToolGrainFilingContext, map[string]any{}); !isErr || !strings.Contains(text, "403") {
		t.Fatalf("grain_filing_context on a ci-read token naming task 30: isError=%v %s", isErr, text)
	}
	if text, isErr := callControllerTool(t, srv, spoofed, ui.ToolGrainInvoke, map[string]any{"tool": "api_get_settings"}); !isErr || !strings.Contains(text, "403") {
		t.Fatalf("api_get_settings on a ci-read token naming task 30: isError=%v %s", isErr, text)
	}
}

// grain_filing_context serves each part of the context only to a caller
// holding the scope of the route that serves the same data.
func TestControllerMCPFilingContextIsGatedPerSection(t *testing.T) {
	t.Parallel()
	srv, client, code := newSingleTenantControllerMCPServer(t)
	if _, err := client.AddTargetRepo(context.Background(), "acme/widgets"); err != nil {
		t.Fatal(err)
	}
	seedAPITask(t, client, "40", "acme/widgets", "api:tasks:write")

	decode := func(text string) map[string]any {
		var out map[string]any
		if err := json.Unmarshal([]byte(text), &out); err != nil {
			t.Fatalf("decoding filing context: %v (%s)", err, text)
		}
		return out
	}
	human, isErr := callControllerTool(t, srv, map[string]string{ui.AccessCodeHeader: code}, ui.ToolGrainFilingContext, map[string]any{})
	if isErr {
		t.Fatalf("human filing context failed: %s", human)
	}
	for _, key := range []string{"targetRepos", "templates", "boards", "capabilities", "apiPresets", "defaultModel", "models", "defaultNetwork", "defaultCapabilities"} {
		if _, ok := decode(human)[key]; !ok {
			t.Errorf("a person's filing context has no %q: %s", key, human)
		}
	}
	// What a person may read and that has nothing in it is an empty
	// list, never null: null would read as "not yours to read".
	for _, key := range []string{"boards", "customFields", "models", "apiPresets"} {
		if v, ok := decode(human)[key].([]any); !ok || v == nil {
			t.Errorf("a person's filing context %s = %v, want an empty list: %s", key, decode(human)[key], human)
		}
	}

	writer, isErr := callControllerTool(t, srv, map[string]string{ui.AccessCodeHeader: code, ui.ActingTaskHeader: "40"}, ui.ToolGrainFilingContext, map[string]any{})
	if isErr {
		t.Fatalf("tasks:write filing context failed: %s", writer)
	}
	got := decode(writer)
	if repos, _ := got["targetRepos"].([]any); len(repos) != 1 {
		t.Errorf("tasks:write filing context targetRepos = %v, want acme/widgets", got["targetRepos"])
	}
	for _, key := range []string{"capabilities", "apiPresets", "defaultModel", "models", "defaultNetwork", "defaultCapabilities", "boards"} {
		if _, ok := got[key]; ok {
			t.Errorf("a task holding only api:tasks:write was served %q, which capabilities:read / settings:read / boards:read gate: %s", key, writer)
		}
	}
}

// grain_filing_context names the one repository a deployment works in
// even when nothing restricts it to that repository: Config.TargetRepos
// empty means any owner/name is accepted, and before this the tool
// answered targetRepos: null, so a caller had to read api_config's
// defaultTarget to learn the name the deployment files into by default.
func TestControllerMCPFilingContextListsTheDefaultRepo(t *testing.T) {
	t.Parallel()
	srv, client, code := newSingleTenantControllerMCPServer(t)
	if client.Config.DefaultTarget == nil || client.Config.DefaultTarget.String() != "acme/widgets" {
		t.Fatalf("test client default target = %v, want acme/widgets", client.Config.DefaultTarget)
	}
	headers := map[string]string{ui.AccessCodeHeader: code}
	decode := func(text string) map[string]any {
		var out map[string]any
		if err := json.Unmarshal([]byte(text), &out); err != nil {
			t.Fatalf("decoding filing context: %v (%s)", err, text)
		}
		return out
	}

	text, isErr := callControllerTool(t, srv, headers, ui.ToolGrainFilingContext, map[string]any{})
	if isErr {
		t.Fatalf("filing context failed: %s", text)
	}
	got := decode(text)
	if repos, _ := got["targetRepos"].([]any); len(repos) != 1 || repos[0] != "acme/widgets" {
		t.Errorf("targetRepos = %v (%T), want [acme/widgets] from the deployment's default target", got["targetRepos"], got["targetRepos"])
	}
	if got["anyRepoAllowed"] != true {
		t.Errorf("anyRepoAllowed = %v, want true with no target repos configured", got["anyRepoAllowed"])
	}
	if got["defaultRepo"] != "acme/widgets" {
		t.Errorf("defaultRepo = %v, want acme/widgets", got["defaultRepo"])
	}

	// Restricting the deployment to a repository makes the list a bound.
	if _, err := client.AddTargetRepo(context.Background(), "acme/gadgets"); err != nil {
		t.Fatal(err)
	}
	text, isErr = callControllerTool(t, srv, headers, ui.ToolGrainFilingContext, map[string]any{})
	if isErr {
		t.Fatalf("filing context failed: %s", text)
	}
	got = decode(text)
	if repos, _ := got["targetRepos"].([]any); len(repos) != 1 || repos[0] != "acme/gadgets" {
		t.Errorf("targetRepos = %v, want [acme/gadgets] once the deployment targets it", got["targetRepos"])
	}
	if got["anyRepoAllowed"] != false {
		t.Errorf("anyRepoAllowed = %v, want false with a target repo configured", got["anyRepoAllowed"])
	}
	// A filter that matches nothing is an empty list, not null.
	text, isErr = callControllerTool(t, srv, headers, ui.ToolGrainFilingContext, map[string]any{"repo": "other/repo"})
	if isErr {
		t.Fatalf("filing context failed: %s", text)
	}
	if repos, ok := decode(text)["targetRepos"].([]any); !ok || repos == nil || len(repos) != 0 {
		t.Errorf("targetRepos filtered to a repo the deployment lacks = %v, want []", decode(text)["targetRepos"])
	}
}

// grain_invoke answers a tool name that does not exist with the nearest
// of the names the caller may use, so a guess costs one call rather than
// a failure and two explores.
func TestControllerMCPInvokeSuggestsNearestToolNames(t *testing.T) {
	t.Parallel()
	srv, client, code := newSingleTenantControllerMCPServer(t)
	seedAPITask(t, client, "50", "acme/widgets", "api:ci:read")
	headers := map[string]string{ui.AccessCodeHeader: code}

	text, isErr := callControllerTool(t, srv, headers, ui.ToolGrainInvoke, map[string]any{"tool": "api_get_logs"})
	if !isErr || !strings.Contains(text, "did you mean") || !strings.Contains(text, "api_get_log_lines") {
		t.Errorf("api_get_logs: isError=%v %s, want a suggestion of api_get_log_lines", isErr, text)
	}
	text, isErr = callControllerTool(t, srv, headers, ui.ToolGrainInvoke, map[string]any{"tool": "api_lits_tasks"})
	if !isErr || !strings.Contains(text, "did you mean api_list_tasks") {
		t.Errorf("api_lits_tasks: isError=%v %s, want api_list_tasks suggested first", isErr, text)
	}
	// Suggestions come from what the caller can see: a ci:read task is
	// not told about task routes it could not call anyway.
	text, isErr = callControllerTool(t, srv, map[string]string{ui.AccessCodeHeader: code, ui.ActingTaskHeader: "50"}, ui.ToolGrainInvoke, map[string]any{"tool": "api_lits_tasks"})
	if !isErr || strings.Contains(text, "api_list_tasks") || !strings.Contains(text, "unknown route tool") {
		t.Errorf("api_lits_tasks as a ci:read task: isError=%v %s, want no suggestion of a tasks route", isErr, text)
	}
}

// grain_explore_scope's tool filter finds a route by a word of its
// description or path, not only of its tool name, and a word that finds
// nothing is answered with the nearest tool names rather than a dead end.
func TestControllerMCPExploreMatchesWordsAndSuggests(t *testing.T) {
	t.Parallel()
	srv, _, code := newSingleTenantControllerMCPServer(t)
	headers := map[string]string{ui.AccessCodeHeader: code}
	tools := func(args map[string]any) ([]string, string, bool) {
		text, isErr := callControllerTool(t, srv, headers, ui.ToolGrainExploreScope, args)
		var out struct {
			Routes []struct {
				Tool    string `json:"tool"`
				AliasOf string `json:"aliasOf"`
			} `json:"routes"`
		}
		_ = json.Unmarshal([]byte(text), &out)
		var names []string
		for _, rt := range out.Routes {
			names = append(names, rt.Tool)
		}
		return names, text, isErr
	}

	// A path segment.
	if names, text, isErr := tools(map[string]any{"tool": "access"}); isErr || !slices.Contains(names, "api_list_access_principals") {
		t.Errorf("tool=access: isError=%v routes=%v (%s)", isErr, names, text)
	}
	// Two words, found across a tool name and its description.
	if names, text, isErr := tools(map[string]any{"tool": "git connection"}); isErr || !slices.Contains(names, "api_check_deployment_git_connection") {
		t.Errorf("tool='git connection': isError=%v routes=%v (%s)", isErr, names, text)
	}
	// A phrase only the description carries.
	if names, text, isErr := tools(map[string]any{"tool": "merge queue"}); isErr || !slices.Contains(names, "api_submit") {
		t.Errorf("tool='merge queue': isError=%v routes=%v (%s)", isErr, names, text)
	}
	// A misspelling.
	if _, text, isErr := tools(map[string]any{"tool": "coment"}); !isErr || !strings.Contains(text, "Did you mean") || !strings.Contains(text, "api_add_comment") {
		t.Errorf("tool=coment: isError=%v %s, want api_add_comment suggested", isErr, text)
	}
	// A near-miss on a group name.
	if _, text, isErr := tools(map[string]any{"scope": "task"}); !isErr || !strings.Contains(text, "Did you mean tasks") {
		t.Errorf("scope=task: isError=%v %s, want tasks suggested", isErr, text)
	}
}

// grain_explore_scope answers a whole group, and the whole API, in one
// readable page: schemas when they fit, compact lines when they do not,
// and an index of tool names for everything at once. Per-field aliases
// of the repository settings routes are folded out of a listing unless
// asked for, and found by a search regardless.
func TestControllerMCPExploreViewsFitOneAnswer(t *testing.T) {
	t.Parallel()
	srv, _, code := newSingleTenantControllerMCPServer(t)
	headers := map[string]string{ui.AccessCodeHeader: code}
	const cap = 10 << 10
	explore := func(args map[string]any) (map[string]any, string) {
		text, isErr := callControllerTool(t, srv, headers, ui.ToolGrainExploreScope, args)
		if isErr {
			t.Fatalf("explore %v failed: %s", args, text)
		}
		var out map[string]any
		if err := json.Unmarshal([]byte(text), &out); err != nil {
			t.Fatalf("explore %v: %v (%d bytes: %.200s)", args, err, len(text), text)
		}
		return out, text
	}

	// Every route, in one call, under the cap.
	all, text := explore(map[string]any{"scope": "all"})
	if all["view"] != "index" || len(text) > cap {
		t.Errorf("explore(all) = view %v in %d bytes, want the index under %d", all["view"], len(text), cap)
	}
	index, _ := all["index"].(map[string]any)
	listed := 0
	for _, names := range index {
		listed += len(names.([]any))
	}
	authorized := 0
	for _, rt := range ui.AgentAPIRoutes() {
		if rt.Scope != "" && rt.Scope != "internal" && rt.AliasOf == "" {
			authorized++
		}
	}
	if listed != authorized || int(all["routeCount"].(float64)) != authorized {
		t.Errorf("explore(all) indexes %d tools (routeCount %v), want every one of the %d non-alias routes", listed, all["routeCount"], authorized)
	}
	if note, _ := all["aliases"].(string); !strings.Contains(note, "includeAliases") {
		t.Errorf("explore(all) aliases note = %q, want how to list them", note)
	}
	if _, isErr := callControllerTool(t, srv, headers, ui.ToolGrainExploreScope, map[string]any{"view": "index"}); isErr {
		t.Errorf("explore(view=index) with no scope failed")
	}

	// A big group drops to compact lines that fit; a route named in
	// tool still gets its schema.
	for _, group := range []string{"repos", "tasks", "deployment", "capabilities", "secrets"} {
		got, text := explore(map[string]any{"scope": group})
		if len(text) > cap {
			t.Errorf("explore(%s) = %d bytes in view %v, want under %d", group, len(text), got["view"], cap)
		}
		routes, _ := got["routes"].([]any)
		if len(routes) == 0 {
			t.Errorf("explore(%s) lists no routes: %.300s", group, text)
			continue
		}
		first, _ := routes[0].(map[string]any)
		if got["view"] == "compact" {
			if _, has := first["inputSchema"]; has || first["summary"] == "" || first["path"] == "" {
				t.Errorf("explore(%s) compact route = %v, want tool, method, path and summary without inputSchema", group, first)
			}
			if hint, _ := got["hint"].(string); !strings.Contains(hint, "tool=") {
				t.Errorf("explore(%s) compact hint = %q, want how to get one route's schema", group, hint)
			}
		}
	}
	one, _ := explore(map[string]any{"tool": "api_set_repo_settings"})
	if routes, _ := one["routes"].([]any); len(routes) != 1 || routes[0].(map[string]any)["inputSchema"] == nil {
		t.Errorf("explore(tool=api_set_repo_settings) = %v, want its one route with inputSchema", one)
	}
	full, _ := explore(map[string]any{"scope": "repos", "view": "full"})
	if full["view"] != "full" || full["routes"].([]any)[0].(map[string]any)["inputSchema"] == nil {
		t.Errorf("explore(repos, view=full) did not give schemas: %v", full["view"])
	}
	if _, isErr := callControllerTool(t, srv, headers, ui.ToolGrainExploreScope, map[string]any{"scope": "repos", "view": "brief"}); !isErr {
		t.Errorf("explore(view=brief) succeeded, want a refusal naming the views")
	}

	// Aliases: folded out of the repos listing with a note, listed on
	// request and marked, and found by a word that names one.
	names := func(got map[string]any) map[string]string {
		out := map[string]string{}
		routes, _ := got["routes"].([]any)
		for _, r := range routes {
			rt := r.(map[string]any)
			alias, _ := rt["aliasOf"].(string)
			out[rt["tool"].(string)] = alias
		}
		return out
	}
	repos, _ := explore(map[string]any{"scope": "repos"})
	if n := names(repos); n["api_set_repo_settings"] != "" || slices.Contains(slices.Collect(maps.Keys(n)), "api_set_repo_ci_mode") {
		t.Errorf("explore(repos) lists %v, want api_set_repo_settings and no api_set_repo_ci_mode", n)
	} else if _, has := n["api_set_repo_settings"]; !has {
		t.Errorf("explore(repos) lists %v, want api_set_repo_settings", n)
	}
	if note, _ := repos["aliases"].(string); !strings.Contains(note, "ci-mode") || !strings.Contains(note, "api_set_repo_settings") {
		t.Errorf("explore(repos) aliases note = %q, want the fields and the route they alias", note)
	}
	withAliases, _ := explore(map[string]any{"scope": "repos", "includeAliases": true})
	if n := names(withAliases); n["api_set_repo_ci_mode"] != "api_set_repo_settings" || n["api_get_repo_ci_mode"] != "api_get_repo_settings" {
		t.Errorf("explore(repos, includeAliases) lists %v, want api_set_repo_ci_mode marked as an alias of api_set_repo_settings", n)
	}
	searched, _ := explore(map[string]any{"tool": "ci_mode"})
	if n := names(searched); n["api_set_repo_ci_mode"] != "api_set_repo_settings" {
		t.Errorf("explore(tool=ci_mode) lists %v, want the alias it names", n)
	}
}

// grain_invoke refuses an expect it would not check rather than run the
// mutation unguarded.
func TestControllerMCPInvokeRefusesUnknownExpectFields(t *testing.T) {
	t.Parallel()
	srv, client, code := newSingleTenantControllerMCPServer(t)
	seedAPITask(t, client, "50", "acme/widgets", "api:tasks:read")
	headers := map[string]string{ui.AccessCodeHeader: code}

	text, isErr := callControllerTool(t, srv, headers, ui.ToolGrainInvoke, map[string]any{
		"tool":      "api_add_comment",
		"arguments": map[string]any{"id": "50", "body": map[string]any{"body": "guarded"}, "expect": map[string]any{"merge_queue_state": "idle"}},
	})
	if !isErr || !strings.Contains(text, "merge_queue_state") || !strings.Contains(text, "title_prefix") {
		t.Fatalf("unknown expect field: isError=%v %s", isErr, text)
	}
	if text, isErr := callControllerTool(t, srv, headers, ui.ToolGrainInvoke, map[string]any{
		"tool":      "api_add_comment",
		"arguments": map[string]any{"id": "50", "body": map[string]any{"body": "guarded"}, "expect": "queued"},
	}); !isErr || !strings.Contains(text, "object") {
		t.Fatalf("expect that is not an object: isError=%v %s", isErr, text)
	}
	// A numeric id with an expect reads the task it names, not /api/tasks/.
	text, isErr = callControllerTool(t, srv, headers, ui.ToolGrainInvoke, map[string]any{
		"tool":      "api_add_comment",
		"arguments": map[string]any{"id": 50, "body": map[string]any{"body": "guarded"}, "expect": map[string]any{"state": "running"}},
	})
	if !isErr || !strings.Contains(text, "state is") {
		t.Fatalf("expect against a numeric id: isError=%v %s", isErr, text)
	}
	text, isErr = callControllerTool(t, srv, headers, ui.ToolGrainInvoke, map[string]any{
		"tool":      "api_add_comment",
		"arguments": map[string]any{"id": 50, "body": map[string]any{"body": "guarded"}, "expect": map[string]any{"title_prefix": "task 50"}},
	})
	if isErr {
		t.Fatalf("expect that holds: %s", text)
	}
}

// A configuration write made through grain_invoke is recorded in the
// config audit as made by the acting task's agent, as the same write
// over the REST route is -- not as the person whose request carried it,
// and not left off the audit.
func TestControllerMCPInvokeRecordsConfigWritesAsTheActingTask(t *testing.T) {
	t.Parallel()
	srv, client, code := newSingleTenantControllerMCPServer(t)
	if _, err := client.AddTargetRepo(context.Background(), "acme/widgets"); err != nil {
		t.Fatal(err)
	}
	seedAPITask(t, client, "60", "acme/widgets", "api:repos:write")

	text, isErr := callControllerTool(t, srv, map[string]string{ui.AccessCodeHeader: code, ui.ActingTaskHeader: "60"}, ui.ToolGrainInvoke, map[string]any{
		"tool":      "api_set_repo_settings",
		"reason":    "tighten the merge gate",
		"arguments": map[string]any{"owner": "acme", "name": "widgets", "body": map[string]any{"requireBranchUpToDate": true}},
	})
	if isErr {
		t.Fatalf("api_set_repo_settings via grain_invoke: %s", text)
	}
	audit, err := client.Store.ConfigAudit(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(audit) != 1 || audit[0].By.Kind != model.PrincipalAgent || audit[0].By.ID != "60" {
		t.Fatalf("config audit after a grain_invoke settings write = %+v, want one row by {agent, 60}", audit)
	}
	if audit[0].Surface != "repo:acme/widgets" && !strings.Contains(audit[0].Summary, "/api/repos/acme/widgets/settings") {
		t.Fatalf("config audit row = %+v, want it to name acme/widgets' settings", audit[0])
	}
}

func TestControllerMCPGetTaskPreservesOwnerAttributionAndFiledVia(t *testing.T) {
	t.Parallel()
	srv, client, _ := newSingleTenantControllerMCPServer(t)
	enableExternalAgentAPI(t, client.Store)
	if _, err := client.AddTargetRepo(context.Background(), "acme/widgets"); err != nil {
		t.Fatal(err)
	}

	humanCtx := ui.WithPrincipal(context.Background(), model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com", Source: model.SourceIAP})
	issued, err := client.CreateExternalAgentToken(humanCtx, ui.CreateExternalAgentTokenRequest{
		Name:     "alice-mcp",
		ScopeSet: "grain/external-dev",
		Repos:    []string{"acme/widgets"},
		Owner:    "alice@example.com",
	})
	if err != nil {
		t.Fatalf("CreateExternalAgentToken: %v", err)
	}

	extHeaders := map[string]string{
		"Authorization":         "Bearer " + issued.Token,
		model.GrainClientHeader: "codex-cli/0.9.2 session=mcp-99",
	}
	subText, isErr := callControllerTool(t, srv, extHeaders, ui.ToolGrainSubmitTask, map[string]any{
		"repo":  "acme/widgets",
		"title": "Check owner attribution on /api/mcp",
		"body":  "Verify grain_get_task summary and conversation views preserve owner and filedVia.",
	})
	if isErr {
		t.Fatalf("grain_submit_task failed: %s", subText)
	}
	var submitted struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(subText), &submitted); err != nil || submitted.ID == "" {
		t.Fatalf("decoding submitted task: %v (%s)", err, subText)
	}

	if text, isErr := callControllerTool(t, srv, extHeaders, ui.ToolGrainInvoke, map[string]any{
		"tool": "api_add_comment",
		"arguments": map[string]any{
			"id":   submitted.ID,
			"body": map[string]any{"body": "Follow-up from external MCP client."},
		},
	}); isErr {
		t.Fatalf("api_add_comment failed: %s", text)
	}

	summaryView, isErr := callControllerTool(t, srv, extHeaders, ui.ToolGrainGetTask, map[string]any{
		"id": submitted.ID,
	})
	if isErr {
		t.Fatalf("grain_get_task summary failed: %s", summaryView)
	}
	if want := "external agent alice-mcp for alice@example.com (agent)"; !strings.Contains(summaryView, want) {
		t.Errorf("grain_get_task summary missing %q:\n%s", want, summaryView)
	}

	convView, isErr := callControllerTool(t, srv, extHeaders, ui.ToolGrainGetTask, map[string]any{
		"id":   submitted.ID,
		"view": "conversation",
	})
	if isErr {
		t.Fatalf("grain_get_task conversation failed: %s", convView)
	}
	if want := "external agent alice-mcp for alice@example.com (agent) (from codex-cli/0.9.2 session=mcp-99):"; !strings.Contains(convView, want) {
		t.Errorf("grain_get_task conversation missing %q:\n%s", want, convView)
	}
}

// grain_submit_task once dropped grants: it copied the arguments it knew
// into its POST /api/tasks body and grants was not one of them, so a grants
// list was silently left off the task it filed, and a mixed request that
// REST and grain_invoke refuse was filed anyway (2026-11-09 store-upgrades
// walkthrough, item 3). It forwards grants as sent now: the control is
// filed holding its grant, and every refused shape is refused with the
// route's own reason and files nothing.
func TestControllerMCPSubmitTaskForwardsGrants(t *testing.T) {
	t.Parallel()
	srv, client, code := newSingleTenantControllerMCPServer(t)
	if _, err := client.AddTargetRepo(context.Background(), "acme/widgets"); err != nil {
		t.Fatal(err)
	}
	headers := map[string]string{ui.AccessCodeHeader: code}
	ctx := context.Background()

	text, isErr := callControllerTool(t, srv, headers, ui.ToolGrainSubmitTask, map[string]any{
		"title": "grants only", "repo": "acme/widgets", "grants": []any{"api:ci-read"},
	})
	if isErr {
		t.Fatalf("grain_submit_task with grants failed: %s", text)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(text), &created); err != nil {
		t.Fatalf("decoding submit response: %v (%s)", err, text)
	}
	got, err := client.GetTask(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Slice 3 records the canonical reference for the legacy name it was given.
	if !slices.Contains(got.Capabilities, model.APIRef("ci-read")) {
		t.Fatalf("task filed with grants [api:ci-read] holds %v, want %s", got.Capabilities, model.APIRef("ci-read"))
	}

	target := map[string]any{"capability": model.RepoTargetRef, "with": map[string]any{"repo": "acme/widgets", "base": "main"}}
	read := map[string]any{"capability": model.RepoReadRef, "with": map[string]any{"repo": "acme/widgets"}}
	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"capabilities beside grants: []", map[string]any{"repo": "acme/widgets", "capabilities": []any{"api:ci-read"}, "grants": []any{}}, "grants and capabilities both name capability grants"},
		{"reads: [] beside grants", map[string]any{"repo": "acme/widgets", "reads": []any{}, "grants": []any{read}}, "and reads both name read access"},
		{"base beside repo#target", map[string]any{"base": "main", "grants": []any{target}}, "both name the task's repository"},
		{"no_repo beside repo#target", map[string]any{"no_repo": true, "grants": []any{target}}, "both name the task's repository"},
	} {
		before, err := client.ListTasks(ctx)
		if err != nil {
			t.Fatal(err)
		}
		args := map[string]any{"title": tc.name}
		for k, v := range tc.args {
			args[k] = v
		}
		text, isErr := callControllerTool(t, srv, headers, ui.ToolGrainSubmitTask, args)
		if !isErr || !strings.Contains(text, tc.want) {
			t.Errorf("%s: grain_submit_task = isError %v %q, want refused with %q", tc.name, isErr, text, tc.want)
		}
		after, err := client.ListTasks(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(after) != len(before) {
			t.Errorf("%s: a refused request filed a task (%d tasks, then %d)", tc.name, len(before), len(after))
		}
	}
}
