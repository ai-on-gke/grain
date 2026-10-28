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
	"log"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

type mcpRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type mcpToolListResult struct {
	Tools []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"tools"`
}

type mcpToolCallResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

func doMCP(t *testing.T, srv *ui.Server, token string, method string, params any) (*httptest.ResponseRecorder, mcpRPCResponse) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		t.Fatalf("marshal MCP request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	var rpc mcpRPCResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &rpc); err != nil {
			t.Fatalf("unmarshal MCP response (%s): %v", rec.Body.String(), err)
		}
	}
	return rec, rpc
}

func callMCPTool(t *testing.T, srv *ui.Server, token, toolName string, args map[string]any) (*httptest.ResponseRecorder, mcpToolCallResult) {
	t.Helper()
	rec, rpc := doMCP(t, srv, token, "tools/call", map[string]any{
		"name":      toolName,
		"arguments": args,
	})
	var res mcpToolCallResult
	if rec.Code == http.StatusOK && rpc.Error == nil && len(rpc.Result) > 0 {
		if err := json.Unmarshal(rpc.Result, &res); err != nil {
			t.Fatalf("unmarshal tool call result (%s): %v", string(rpc.Result), err)
		}
	}
	return rec, res
}

func enableExternalAgentAPI(t *testing.T, store *model.Store) {
	t.Helper()
	ctx := context.Background()
	cfg, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil {
		d := model.DefaultConfig()
		cfg = &d
	}
	cfg.ExternalAgentAPI = true
	if err := store.PutConfig(ctx, *cfg); err != nil {
		t.Fatal(err)
	}
}

func TestExternalAgentMCPDoneWhen(t *testing.T) {
	runner := &fakeCIRunner{run: ci.Run{ID: "ci-run-444", Repo: "bwsalmon/grain", Ref: "grain/task-101"}}
	srv, client, store := releaseServer(t, runner)
	ctx := context.Background()

	cfg := model.DefaultConfig()
	cfg.TargetRepos = []string{"bwsalmon/grain"}
	cfg.ExternalAgentAPI = true
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	repo := model.RepoRef{Owner: "bwsalmon", Name: "grain"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CIMode: model.CIModeGrain}); err != nil {
		t.Fatal(err)
	}

	// Create two queued tasks on bwsalmon/grain so we can move the second to the front
	// and re-run CI on its PR.
	t1, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title:       "First task",
		Description: "Already at front",
		Repo:        "bwsalmon/grain",
		Approved:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t2, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title:       "Second task with PR",
		Description: "Move me to the front and rerun CI",
		Repo:        "bwsalmon/grain",
		Approved:    true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Operator issues credential "ops-laptop" with set grain/task-admin restricted to bwsalmon/grain.
	humanCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com"})
	issued, err := client.CreateExternalAgentToken(humanCtx, ui.CreateExternalAgentTokenRequest{
		Name:     "ops-laptop",
		ScopeSet: "grain/task-admin",
		Repos:    []string{"bwsalmon/grain"},
	})
	if err != nil {
		t.Fatalf("CreateExternalAgentToken: %v", err)
	}
	if !strings.HasPrefix(issued.Token, model.ExternalAgentTokenPrefix) {
		t.Fatalf("issued token = %q, want prefix %q", issued.Token, model.ExternalAgentTokenPrefix)
	}
	if issued.Principal != "external:ops-laptop" {
		t.Fatalf("issued.Principal = %q, want external:ops-laptop", issued.Principal)
	}

	// Capture daemon journal logs to verify attribution.
	var logBuf bytes.Buffer
	origOut := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(origOut)

	// 1. External MCP client connects and initializes.
	rec, initRPC := doMCP(t, srv, issued.Token, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
	})
	if rec.Code != http.StatusOK || initRPC.Error != nil {
		t.Fatalf("initialize failed: status=%d body=%s", rec.Code, rec.Body.String())
	}

	// 2. External MCP client lists tools.
	rec, listRPC := doMCP(t, srv, issued.Token, "tools/list", map[string]any{})
	if rec.Code != http.StatusOK || listRPC.Error != nil {
		t.Fatalf("tools/list failed: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var toolList mcpToolListResult
	if err := json.Unmarshal(listRPC.Result, &toolList); err != nil {
		t.Fatal(err)
	}
	var toolNames []string
	for _, tool := range toolList.Tools {
		toolNames = append(toolNames, tool.Name)
	}
	for _, want := range []string{"api_reorder", "api_start_ci_run", "api_close", "api_list_tasks"} {
		if !slices.Contains(toolNames, want) {
			t.Errorf("tools/list missing expected tool %q; got %v", want, toolNames)
		}
	}
	for _, unwanted := range []string{"api_update_settings", "api_post_task_message", "api_set_task_session_title"} {
		if slices.Contains(toolNames, unwanted) {
			t.Errorf("tools/list unexpectedly included %q", unwanted)
		}
	}

	// 3. External MCP client moves task t2 to the front ahead of t1.
	rec, reorderRes := callMCPTool(t, srv, issued.Token, "api_reorder", map[string]any{
		"reason": "prioritize hotfix PR",
		"body": map[string]any{
			"ids":      []string{t2.ID},
			"beforeId": t1.ID,
		},
	})
	if rec.Code != http.StatusOK || reorderRes.IsError {
		t.Fatalf("api_reorder failed: code=%d isError=%v content=%+v", rec.Code, reorderRes.IsError, reorderRes.Content)
	}
	tasksAfter, err := client.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasksAfter) < 2 || tasksAfter[0].ID != t2.ID {
		t.Fatalf("expected task %s at front of queue, got order %v", t2.ID, []string{tasksAfter[0].ID, tasksAfter[1].ID})
	}

	// 4. External MCP client re-runs CI on t2's PR.
	rec, ciRes := callMCPTool(t, srv, issued.Token, "api_start_ci_run", map[string]any{
		"reason": "re-run CI on hotfix PR",
		"body": map[string]any{
			"taskId": t2.ID,
		},
	})
	if rec.Code != http.StatusOK || ciRes.IsError {
		t.Fatalf("api_start_ci_run failed: code=%d isError=%v content=%+v", rec.Code, ciRes.IsError, ciRes.Content)
	}

	// 5. Verify attribution in the daemon journal and in t2's conversation.
	logs := logBuf.String()
	for _, wantLog := range []string{
		"grain api: {agent, external:ops-laptop} POST /api/tasks/reorder scope=tasks:write repo=\"bwsalmon/grain\" reason=\"prioritize hotfix PR\" -> 200",
		"grain api: {agent, external:ops-laptop} POST /api/ci/runs scope=ci:write repo=\"bwsalmon/grain\" reason=\"re-run CI on hotfix PR\" -> 202",
	} {
		if !strings.Contains(logs, wantLog) {
			t.Errorf("journal missing %q\ngot logs:\n%s", wantLog, logs)
		}
	}

	comments, err := store.Comments(ctx, t2.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantPrincipal := model.Principal{Kind: model.PrincipalAgent, ID: "external:ops-laptop"}
	var sawReorderComment, sawCIComment bool
	for _, c := range comments {
		if c.Author.OnBehalfOf == nil || *c.Author.OnBehalfOf != wantPrincipal {
			continue
		}
		if strings.Contains(c.Body, "POST /api/tasks/reorder") && strings.Contains(c.Body, "prioritize hotfix PR") {
			sawReorderComment = true
		}
		if strings.Contains(c.Body, "POST /api/ci/runs") && strings.Contains(c.Body, "re-run CI on hotfix PR") {
			sawCIComment = true
		}
	}
	if !sawReorderComment {
		t.Errorf("task %s conversation missing external:ops-laptop attribution for POST /api/tasks/reorder; comments=%+v", t2.ID, comments)
	}
	if !sawCIComment {
		t.Errorf("task %s conversation missing external:ops-laptop attribution for POST /api/ci/runs; comments=%+v", t2.ID, comments)
	}
}

func TestExternalAgentAuthAndSettingsGate(t *testing.T) {
	srv, client := testServer(t)
	store := client.Store
	ctx := context.Background()
	humanCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com"})

	issued, err := client.CreateExternalAgentToken(humanCtx, ui.CreateExternalAgentTokenRequest{
		Name:   "reader-bot",
		Scopes: []string{"tasks:read"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// 1. Off by default in Settings: even a valid token is refused with 403.
	rec, _ := doMCP(t, srv, issued.Token, "tools/list", map[string]any{})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST /mcp while disabled in Settings: status=%d, want 403 (%s)", rec.Code, rec.Body.String())
	}

	// Enable in Settings.
	enableExternalAgentAPI(t, store)

	// 2. Unauthenticated call (no token) is refused with 401.
	rec, _ = doMCP(t, srv, "", "tools/list", map[string]any{})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated POST /mcp: status=%d, want 401 (%s)", rec.Code, rec.Body.String())
	}

	// 3. Unknown / wrong token is refused with 401.
	rec, _ = doMCP(t, srv, "gma_00000000000000000000000000000000", "tools/list", map[string]any{})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown token POST /mcp: status=%d, want 401 (%s)", rec.Code, rec.Body.String())
	}

	// 4. Valid token succeeds once enabled.
	rec, rpc := doMCP(t, srv, issued.Token, "tools/list", map[string]any{})
	if rec.Code != http.StatusOK || rpc.Error != nil {
		t.Fatalf("valid token POST /mcp: status=%d body=%s", rec.Code, rec.Body.String())
	}

	// 5. Listing tokens never exposes the raw token secret.
	listed, err := client.ListExternalAgentTokens(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rawJSON, _ := json.Marshal(listed)
	if strings.Contains(string(rawJSON), issued.Token) {
		t.Fatalf("ListExternalAgentTokens leaked raw token in response: %s", string(rawJSON))
	}
}

func TestExternalAgentScopesAndReposEnforced(t *testing.T) {
	srv, client := testServer(t)
	store := client.Store
	ctx := context.Background()
	enableExternalAgentAPI(t, store)
	humanCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com"})

	seedAPITask(t, client, "10", "bwsalmon/grain")
	seedAPITask(t, client, "11", "other/repo")

	issued, err := client.CreateExternalAgentToken(humanCtx, ui.CreateExternalAgentTokenRequest{
		Name:   "scoped-agent",
		Scopes: []string{"tasks:read", "tasks:write"},
		Repos:  []string{"bwsalmon/grain"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Allowed repo and scope: commenting on task 10 succeeds.
	rec, res := callMCPTool(t, srv, issued.Token, "api_add_comment", map[string]any{
		"id":   "10",
		"body": map[string]any{"body": "hello on bwsalmon/grain"},
	})
	if rec.Code != http.StatusOK || res.IsError {
		t.Fatalf("api_add_comment on allowed repo failed: code=%d res=%+v", rec.Code, res)
	}

	// Disallowed repo: commenting on task 11 (other/repo) is refused.
	_, res = callMCPTool(t, srv, issued.Token, "api_add_comment", map[string]any{
		"id":   "11",
		"body": map[string]any{"body": "hello on other/repo"},
	})
	if !res.IsError || !strings.Contains(res.Content[0].Text, "holds tasks:write, but not on other/repo") {
		t.Fatalf("expected repo refusal on other/repo, got %+v", res)
	}

	// Disallowed scope: calling tasks:admin tool via MCP is refused as unknown tool,
	// and calling the HTTP route directly is refused with 403 Forbidden.
	_, approveRPC := doMCP(t, srv, issued.Token, "tools/call", map[string]any{
		"name": "api_approve_task",
		"arguments": map[string]any{
			"id":     "10",
			"reason": "try to approve without tasks:admin",
		},
	})
	if approveRPC.Error == nil || !strings.Contains(strings.ToLower(approveRPC.Error.Message), "unknown tool") {
		t.Fatalf("expected MCP tools/call to reject ungranted tool api_approve_task, got %+v", approveRPC)
	}
	approveReq := httptest.NewRequest(http.MethodPost, "/api/tasks/10/approve", nil)
	approveReq.Header.Set("Authorization", "Bearer "+issued.Token)
	approveReq.Header.Set(ui.ActingReasonHeader, "try to approve without tasks:admin")
	approveRec := httptest.NewRecorder()
	srv.ServeHTTP(approveRec, approveReq)
	if approveRec.Code != http.StatusForbidden || !strings.Contains(approveRec.Body.String(), "holds no api grant for scope tasks:admin") {
		t.Fatalf("expected 403 scope refusal for tasks:admin, got status=%d body=%s", approveRec.Code, approveRec.Body.String())
	}
}

func TestExternalAgentDangerousScopeRequiresPerson(t *testing.T) {
	srv, client := testServer(t)
	store := client.Store
	ctx := context.Background()
	enableExternalAgentAPI(t, store)

	// Seed a declarative scope set (task 442) with requires_person: true.
	t.Cleanup(func() { _ = model.InstallAPIScopeSets(nil) })
	if err := client.PutAPIScopeSet(ctx, model.APIScopeSet{
		Name:           "team-ops",
		Scopes:         []string{"tasks:read"},
		RequiresPerson: true,
	}); err != nil {
		t.Fatal(err)
	}

	// An in-grain task holding capabilities:admin and tasks:read (non-dangerous)
	// cannot issue a credential with a dangerous scope (tasks:admin) or a requires_person set.
	seedAPITask(t, client, "1", "bwsalmon/grain", "api:capabilities:admin", "api:tasks:read")

	// 1. Agent trying to issue dangerous scope tasks:admin -> 403 Forbidden.
	code, body := asAgent(t, srv, "1", http.MethodPost, "/api/external-agents/tokens",
		`{"name":"escalate","scopes":["tasks:admin"]}`, "try to mint admin token")
	if code != http.StatusForbidden {
		t.Fatalf("agent issuing dangerous scope: status=%d, want 403 (%s)", code, body)
	}

	// 2. Agent trying to issue requires_person scope set -> 403 Forbidden.
	code, body = asAgent(t, srv, "1", http.MethodPost, "/api/external-agents/tokens",
		`{"name":"escalate-set","scopeSet":"team-ops"}`, "try to mint requires_person set")
	if code != http.StatusForbidden {
		t.Fatalf("agent issuing requires_person scopeSet: status=%d, want 403 (%s)", code, body)
	}

	// 3. Agent issuing a non-dangerous scope it holds (tasks:read) succeeds.
	code, body = asAgent(t, srv, "1", http.MethodPost, "/api/external-agents/tokens",
		`{"name":"safe-reader","scopes":["tasks:read"]}`, "mint reader token")
	if code != http.StatusCreated {
		t.Fatalf("agent issuing held non-dangerous scope: status=%d, want 201 (%s)", code, body)
	}

	// 4. Human issuing dangerous preset grain/task-admin succeeds.
	humanCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com"})
	adminTok, err := client.CreateExternalAgentToken(humanCtx, ui.CreateExternalAgentTokenRequest{
		Name:     "human-admin",
		ScopeSet: "grain/task-admin",
	})
	if err != nil {
		t.Fatalf("human issuing dangerous scopeSet failed: %v", err)
	}

	// 5. Agent trying to rotate the human-issued dangerous credential -> 403 Forbidden.
	code, body = asAgent(t, srv, "1", http.MethodPost, "/api/external-agents/tokens/"+adminTok.ID+"/rotate", "", "try to rotate admin token")
	if code != http.StatusForbidden {
		t.Fatalf("agent rotating dangerous credential: status=%d, want 403 (%s)", code, body)
	}
}

func TestExternalAgentRevokeRotateExpiryAndKillSwitch(t *testing.T) {
	srv, client := testServer(t)
	store := client.Store
	ctx := context.Background()
	enableExternalAgentAPI(t, store)
	humanCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com"})

	tok1, err := client.CreateExternalAgentToken(humanCtx, ui.CreateExternalAgentTokenRequest{
		Name:   "agent-one",
		Scopes: []string{"tasks:read"},
	})
	if err != nil {
		t.Fatal(err)
	}
	tok2, err := client.CreateExternalAgentToken(humanCtx, ui.CreateExternalAgentTokenRequest{
		Name:   "agent-two",
		Scopes: []string{"tasks:read"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Rotate tok1: old token fails immediately, new token works.
	rotated, err := client.RotateExternalAgentToken(humanCtx, tok1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Token == tok1.Token {
		t.Fatal("expected rotated token to differ from original token")
	}
	if rec, _ := doMCP(t, srv, tok1.Token, "tools/list", map[string]any{}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("old token after rotation: status=%d, want 401", rec.Code)
	}
	if rec, _ := doMCP(t, srv, rotated.Token, "tools/list", map[string]any{}); rec.Code != http.StatusOK {
		t.Fatalf("new token after rotation: status=%d, want 200", rec.Code)
	}

	// Revoke tok1: fails immediately.
	if err := client.RevokeExternalAgentToken(humanCtx, tok1.ID); err != nil {
		t.Fatal(err)
	}
	if rec, _ := doMCP(t, srv, rotated.Token, "tools/list", map[string]any{}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token: status=%d, want 401", rec.Code)
	}

	// Kill switch (RevokeAllExternalAgentTokens) revokes tok2 and any other active tokens immediately.
	n, err := client.RevokeAllExternalAgentTokens(humanCtx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("RevokeAllExternalAgentTokens count = %d, want 1", n)
	}
	if rec, _ := doMCP(t, srv, tok2.Token, "tools/list", map[string]any{}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("token after kill switch: status=%d, want 401", rec.Code)
	}
}

func TestExternalAgentNoTaskContextRails(t *testing.T) {
	srv, client := testServer(t)
	store := client.Store
	ctx := context.Background()
	enableExternalAgentAPI(t, store)
	humanCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com"})

	seedAPITask(t, client, "10", "bwsalmon/grain")

	issued, err := client.CreateExternalAgentToken(humanCtx, ui.CreateExternalAgentTokenRequest{
		Name:   "ops-agent",
		Scopes: []string{"tasks:read", "tasks:write", "sessions:read", "sessions:write"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// 1. Calling own-task-only route POST /api/tasks/{id}/messages directly over HTTP is refused.
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/10/messages", strings.NewReader(`{"body":"pretend to be task 10"}`))
	req.Header.Set("Authorization", "Bearer "+issued.Token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "external agents have no calling task") {
		t.Fatalf("expected no-task-context refusal on POST /api/tasks/{id}/messages, got status=%d body=%s", rec.Code, rec.Body.String())
	}

	// 2. Filing a task via POST /api/tasks without an explicit repo (or noRepo: true) is refused.
	_, res := callMCPTool(t, srv, issued.Token, "api_create_task", map[string]any{
		"body": map[string]any{
			"title":       "Implicit repo task",
			"description": "Should fail without repo",
		},
	})
	if !res.IsError || !strings.Contains(res.Content[0].Text, "POST /api/tasks requires an explicit repo") {
		t.Fatalf("expected explicit-repo refusal on POST /api/tasks, got %+v", res)
	}

	// 3. Filing a task with an explicit repo succeeds.
	_, res = callMCPTool(t, srv, issued.Token, "api_create_task", map[string]any{
		"body": map[string]any{
			"title":       "Explicit repo task",
			"description": "Has explicit repo",
			"repo":        "bwsalmon/grain",
		},
	})
	if res.IsError {
		t.Fatalf("expected POST /api/tasks with explicit repo to succeed, got %+v", res)
	}
}

func TestExternalAgentRateLimitConcurrencyAndSizeLimit(t *testing.T) {
	srv, client := testServer(t)
	store := client.Store
	ctx := context.Background()
	enableExternalAgentAPI(t, store)
	humanCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com"})

	issued, err := client.CreateExternalAgentToken(humanCtx, ui.CreateExternalAgentTokenRequest{
		Name:          "limited-bot",
		Scopes:        []string{"tasks:read"},
		RateLimit:     2,
		MaxConcurrent: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	// 1. Rate limit of 2 req/min: first 2 succeed, 3rd returns 429 Too Many Requests.
	for i := 1; i <= 2; i++ {
		rec, _ := doMCP(t, srv, issued.Token, "tools/list", map[string]any{})
		if rec.Code != http.StatusOK {
			t.Fatalf("call %d status = %d, want 200", i, rec.Code)
		}
	}
	rec, _ := doMCP(t, srv, issued.Token, "tools/list", map[string]any{})
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("call 3 over rate limit: status = %d, want 429 (%s)", rec.Code, rec.Body.String())
	}

	// 2. Oversized request body (> 4 MiB) returns 413 Request Entity Too Large.
	bigBody := bytes.Repeat([]byte("a"), (4<<20)+1024)
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(bigBody))
	req.Header.Set("Authorization", "Bearer "+issued.Token)
	bigRec := httptest.NewRecorder()
	srv.ServeHTTP(bigRec, req)
	if bigRec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized /mcp request: status = %d, want 413", bigRec.Code)
	}

	// 3. Concurrency cap of 1: test via a second token with high rate limit and MaxConcurrent=1.
	concTok, err := client.CreateExternalAgentToken(humanCtx, ui.CreateExternalAgentTokenRequest{
		Name:          "conc-bot",
		Scopes:        []string{"tasks:read"},
		RateLimit:     100,
		MaxConcurrent: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		doMCP(t, srv, concTok.Token, "tools/list", map[string]any{})
	}()
	wg.Wait()
}

func TestExternalAgentExternalConfigRepoGuidance(t *testing.T) {
	c, store, ctx := testClient(t)
	enableExternalAgentAPI(t, store)

	// Point the deployment at an external config repository (task 443).
	fake := &fakeStateRepo{status: ui.StateRepoStatus{
		Remote: "https://github.com/acme/grain-config.git",
		Branch: "main",
	}}
	c.Config.StateRepo = fake
	srv := ui.NewServerWithClient(c)

	humanCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com"})
	issued, err := c.CreateExternalAgentToken(humanCtx, ui.CreateExternalAgentTokenRequest{
		Name:   "settings-editor",
		Scopes: []string{"settings:write"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Calling a configuration-writing tool returns 409 config_managed_externally with a git patch.
	_, res := callMCPTool(t, srv, issued.Token, "api_update_settings", map[string]any{
		"reason": "adjust environment name",
		"body": map[string]any{
			"environmentName": "prod",
		},
	})
	if !res.IsError || !strings.Contains(res.Content[0].Text, "config_managed_externally") || !strings.Contains(res.Content[0].Text, "tables/grain_config.json") {
		t.Fatalf("expected 409 config_managed_externally with patch, got %+v", res)
	}
}
