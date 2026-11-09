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
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
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
	humanCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com", Source: model.SourceIAP})
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
		"grain api: {agent, external:ops-laptop on behalf of operator@example.com} POST /api/tasks/reorder scope=tasks:write repo=\"bwsalmon/grain\" reason=\"prioritize hotfix PR\" -> 200",
		"grain api: {agent, external:ops-laptop on behalf of operator@example.com} POST /api/ci/runs scope=ci:write repo=\"bwsalmon/grain\" reason=\"re-run CI on hotfix PR\" -> 202",
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
	humanCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com", Source: model.SourceIAP})

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
	humanCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com", Source: model.SourceIAP})

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
	humanCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com", Source: model.SourceIAP})
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
	humanCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com", Source: model.SourceIAP})

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
	humanCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com", Source: model.SourceIAP})

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
	humanCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com", Source: model.SourceIAP})

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

	humanCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com", Source: model.SourceIAP})
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

// TestExternalAgentTemplateWithinCredential: a template an external agent
// names -- the one a task is filed from (templateId) or the review filed
// approved and auto-merge once it is done (reviewTemplateId) -- may carry
// no grant and no repository beyond the credential's own, exactly as an
// in-grain agent is held to its task's (TestAgentAPIReviewTemplateWithinGrants).
func TestExternalAgentTemplateWithinCredential(t *testing.T) {
	srv, client := testServer(t)
	ctx := context.Background()
	enableExternalAgentAPI(t, client.Store)
	humanCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com", Source: model.SourceIAP})
	seedAPITask(t, client, "102", "acme/widgets")
	ref, _ := model.ParseRepo("acme/widgets")
	gadgets, _ := model.ParseRepo("acme/gadgets")
	for _, tmpl := range []model.Template{
		{ID: "tpl-upgrade", Name: "upgrade", Title: "upgrade", Grants: []model.Grant{{Capability: "api:deployment:upgrade", Via: model.GrantByLabel}}},
		{ID: "tpl-writes", Name: "writes", Title: "writes", Writes: []model.WriteTarget{{Repo: gadgets}}},
		{ID: "tpl-reads", Name: "reads", Title: "reads", Reads: []model.RepoRef{gadgets}},
		{ID: "tpl-other", Name: "other", Title: "other", Grants: []model.Grant{{Capability: "github-app", Via: model.GrantByLabel}}},
		{ID: "tpl-plain", Name: "plain", Title: "plain"},
		{ID: model.DefaultReviewTemplateID(ref), Name: "default review", Title: "review",
			Grants: []model.Grant{{Capability: "api:deployment:upgrade", Via: model.GrantByLabel}}},
	} {
		if err := client.Store.PutTemplate(ctx, tmpl); err != nil {
			t.Fatal(err)
		}
	}
	issued, err := client.CreateExternalAgentToken(humanCtx, ui.CreateExternalAgentTokenRequest{
		Name:   "template-agent",
		Scopes: []string{"tasks:read", "tasks:write"},
		Repos:  []string{"acme/widgets"},
	})
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body string) (int, string) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+issued.Token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(ui.ActingReasonHeader, "testing")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	for _, id := range []string{"tpl-upgrade", "tpl-writes", "tpl-reads", "tpl-other"} {
		for _, c := range []struct{ method, path, body string }{
			{http.MethodPost, "/api/tasks", `{"title":"x","repo":"acme/widgets","reviewTemplateId":"` + id + `"}`},
			{http.MethodPatch, "/api/tasks/102", `{"reviewTemplateId":"` + id + `"}`},
			{http.MethodPost, "/api/tasks", `{"title":"x","repo":"acme/widgets","templateId":"` + id + `"}`},
		} {
			if code, resp := call(c.method, c.path, c.body); code != http.StatusForbidden {
				t.Errorf("%s %s with %s answered an external agent %d: %s", c.method, c.path, c.body, code, resp)
			}
		}
	}
	for _, id := range []string{"tpl-plain", model.DefaultReviewTemplateID(ref)} {
		if code, resp := call(http.MethodPost, "/api/tasks", `{"title":"x","repo":"acme/widgets","reviewTemplateId":"`+id+`"}`); code >= 300 {
			t.Errorf("reviewTemplateId %s was refused: %d %s", id, code, resp)
		}
	}
}

// TestExternalAgentRepoScopedGrantsHoldFiledWork checks that a credential
// with no repos list whose scopes are each held on one repository
// (tasks:write@owner/name) cannot file work that pushes to or reads another
// repository: what the filed work may reach is what the credential's scope
// reaches, not "every repository" for want of a repos list.
func TestExternalAgentRepoScopedGrantsHoldFiledWork(t *testing.T) {
	srv, client := testServer(t)
	ctx := context.Background()
	enableExternalAgentAPI(t, client.Store)
	humanCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com", Source: model.SourceIAP})
	gadgets, _ := model.ParseRepo("acme/gadgets")
	if err := client.Store.PutTemplate(ctx, model.Template{ID: "tpl-writes", Name: "writes", Title: "writes",
		Writes: []model.WriteTarget{{Repo: gadgets}}}); err != nil {
		t.Fatal(err)
	}
	issued, err := client.CreateExternalAgentToken(humanCtx, ui.CreateExternalAgentTokenRequest{
		Name:   "scoped-agent",
		Scopes: []string{"tasks:read@acme/widgets", "tasks:write@acme/widgets"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(issued.Repos) != 0 {
		t.Fatalf("credential repos = %v, want none (the restriction is on its scopes)", issued.Repos)
	}
	call := func(body string) (int, string) {
		req := httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+issued.Token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	for _, body := range []string{
		`{"title":"x","repo":"acme/widgets","writes":[{"repo":"acme/gadgets"}]}`,
		`{"title":"x","repo":"acme/widgets","reads":["acme/gadgets"]}`,
		`{"title":"x","repo":"acme/widgets","templateId":"tpl-writes"}`,
		`{"title":"x","repo":"acme/gadgets"}`,
	} {
		if code, resp := call(body); code != http.StatusForbidden {
			t.Errorf("filing %s answered %d, want 403: %s", body, code, resp)
		}
	}
	if code, resp := call(`{"title":"x","repo":"acme/widgets"}`); code >= 300 {
		t.Errorf("filing work on the credential's own repository was refused: %d %s", code, resp)
	}
}

// TestExternalAgentCredentialNamesAreUnique checks that a second credential
// cannot take a name already in use: the name is the agent's identity.
func TestExternalAgentCredentialNamesAreUnique(t *testing.T) {
	_, client := testServer(t)
	humanCtx := ui.WithPrincipal(context.Background(), model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com"})
	req := ui.CreateExternalAgentTokenRequest{Name: "ops-laptop", Scopes: []string{"tasks:read"}}
	if _, err := client.CreateExternalAgentToken(humanCtx, req); err != nil {
		t.Fatal(err)
	}
	req.Name = "OPS-laptop"
	if _, err := client.CreateExternalAgentToken(humanCtx, req); err == nil {
		t.Fatal("a second credential named ops-laptop was issued")
	}
}

// TestExternalAgentSingleRepoCredentialListsOnlyItsRepo pins the
// walkthrough finding of docs/test-runs/2026-10-29-external-agent-mcp.md:
// the gate admits a credential restricted to one repository to
// GET /api/tasks by defaulting ?repo= to that repository, so the list it is
// answered must hold no task of any other -- GET /api/tasks used to ignore
// ?repo= and answer every task in the deployment.
func TestExternalAgentSingleRepoCredentialListsOnlyItsRepo(t *testing.T) {
	srv, client := testServer(t)
	ctx := context.Background()
	enableExternalAgentAPI(t, client.Store)
	humanCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com", Source: model.SourceIAP})

	seedAPITask(t, client, "10", "bwsalmon/grain")
	seedAPITask(t, client, "11", "other/repo")

	issued, err := client.CreateExternalAgentToken(humanCtx, ui.CreateExternalAgentTokenRequest{
		Name:   "one-repo",
		Scopes: []string{"tasks:read"},
		Repos:  []string{"bwsalmon/grain"},
	})
	if err != nil {
		t.Fatal(err)
	}
	listed := func(path string) []string {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+issued.Token)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: status %d (%s)", path, rec.Code, rec.Body.String())
		}
		var page struct {
			Tasks []ui.Task `json:"tasks"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, task := range page.Tasks {
			ids = append(ids, task.ID)
		}
		return ids
	}
	for _, path := range []string{"/api/tasks", "/api/tasks?repo=bwsalmon/grain", "/api/tasks?repo=BWSalmon/Grain"} {
		if got := listed(path); !slices.Equal(got, []string{"10"}) {
			t.Errorf("GET %s as a bwsalmon/grain credential listed %v, want only [10]", path, got)
		}
	}

	_, res := callMCPTool(t, srv, issued.Token, "api_list_tasks", map[string]any{})
	if res.IsError || len(res.Content) == 0 {
		t.Fatalf("api_list_tasks: %+v", res)
	}
	if strings.Contains(res.Content[0].Text, "other/repo") || !strings.Contains(res.Content[0].Text, "bwsalmon/grain") {
		t.Errorf("api_list_tasks as a bwsalmon/grain credential answered %s", res.Content[0].Text)
	}
}

// TestExternalAgentConcurrencyCapRefusesASecondRequest holds one request
// open -- authenticated, its body not yet sent -- and checks that a
// credential with maxConcurrent 1 is refused a second until it finishes.
func TestExternalAgentConcurrencyCapRefusesASecondRequest(t *testing.T) {
	srv, client := testServer(t)
	ctx := context.Background()
	enableExternalAgentAPI(t, client.Store)
	humanCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com", Source: model.SourceIAP})
	issued, err := client.CreateExternalAgentToken(humanCtx, ui.CreateExternalAgentTokenRequest{
		Name:          "one-at-a-time",
		Scopes:        []string{"tasks:read"},
		RateLimit:     1000,
		MaxConcurrent: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	pr, pw := io.Pipe()
	held := httptest.NewRequest(http.MethodPost, "/mcp", pr)
	held.Header.Set("Authorization", "Bearer "+issued.Token)
	heldRec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer pr.Close()
		srv.ServeHTTP(heldRec, held)
	}()

	// Writing to the unbuffered pipe blocks until handleMCP calls
	// io.ReadAll(r.Body), which is after serveHTTP's
	// authenticateExternalAgent has acquired the concurrency slot. Keeping
	// pw open holds the request in io.ReadAll with the slot taken.
	if _, err := pw.Write([]byte(" ")); err != nil {
		t.Fatalf("held request exited before reading its body (%d: %s): %v", heldRec.Code, heldRec.Body.String(), err)
	}

	refused, _ := doMCP(t, srv, issued.Token, "ping", map[string]any{})
	if refused.Code != http.StatusTooManyRequests {
		t.Fatalf("second request while slot held: status %d (%s), want 429", refused.Code, refused.Body.String())
	}
	if !strings.Contains(refused.Body.String(), "exceeded max concurrent requests (1)") {
		t.Errorf("refusal = %s, want the concurrency cap named", refused.Body.String())
	}

	_, _ = pw.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	_ = pw.Close()
	<-done
	if heldRec.Code != http.StatusOK {
		t.Fatalf("held request: status %d (%s)", heldRec.Code, heldRec.Body.String())
	}
	if rec, _ := doMCP(t, srv, issued.Token, "ping", map[string]any{}); rec.Code != http.StatusOK {
		t.Errorf("after the held request finished: status %d, want 200", rec.Code)
	}
}

// TestExternalAgentRateLimitRetryAfterCoversTheRefill: a caller told to
// retry after the Retry-After it was given must not be refused again
// because the bucket has not refilled yet -- at 2 requests a minute that is
// 30 seconds, not 1.
func TestExternalAgentRateLimitRetryAfterCoversTheRefill(t *testing.T) {
	srv, client := testServer(t)
	ctx := context.Background()
	enableExternalAgentAPI(t, client.Store)
	humanCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com", Source: model.SourceIAP})
	issued, err := client.CreateExternalAgentToken(humanCtx, ui.CreateExternalAgentTokenRequest{
		Name:      "slow-bot",
		Scopes:    []string{"tasks:read"},
		RateLimit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	var rec *httptest.ResponseRecorder
	for range 3 {
		rec, _ = doMCP(t, srv, issued.Token, "ping", map[string]any{})
	}
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("third request at 2/min: status %d, want 429", rec.Code)
	}
	retry, err := strconv.Atoi(rec.Header().Get("Retry-After"))
	if err != nil || retry < 25 || retry > 30 {
		t.Errorf("Retry-After = %q, want about 30 seconds at 2 requests/minute", rec.Header().Get("Retry-After"))
	}
}

// TestExternalAgentCredentialNotFoundNamesTheCredential: a 404 from the
// credential routes says which kind of thing was missing.
func TestExternalAgentCredentialNotFoundNamesTheCredential(t *testing.T) {
	srv, _ := testServer(t)
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/api/external-agents/tokens/999/rotate"},
		{http.MethodPost, "/api/external-agents/tokens/999/revoke"},
		{http.MethodDelete, "/api/external-agents/tokens/999"},
	} {
		rec := do(t, srv, c.method, c.path, "")
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "no external agent credential 999") {
			t.Errorf("%s %s: %d %s, want 404 naming the credential", c.method, c.path, rec.Code, rec.Body.String())
		}
	}
}

// TestExternalAgentViewKeepsPerScopeRepos checks that a scope granted on
// one repository (tasks:write@acme/widgets) is listed with that
// repository in resolvedScopes -- on create, rotate and list -- rather
// than as a bare scope that reads as a grant on every repository.
func TestExternalAgentViewKeepsPerScopeRepos(t *testing.T) {
	_, client := testServer(t)
	ctx := context.Background()
	enableExternalAgentAPI(t, client.Store)
	humanCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com", Source: model.SourceIAP})

	issued, err := client.CreateExternalAgentToken(humanCtx, ui.CreateExternalAgentTokenRequest{
		Name:   "widgets-writer",
		Scopes: []string{"tasks:write@acme/widgets", "ci:read"},
	})
	if err != nil {
		t.Fatal(err)
	}
	check := func(where string, got []string) {
		t.Helper()
		if !slices.Contains(got, "tasks:write@acme/widgets") || slices.Contains(got, "tasks:write") {
			t.Errorf("%s: resolvedScopes %v, want tasks:write@acme/widgets and no bare tasks:write", where, got)
		}
		if !slices.Contains(got, "ci:read") {
			t.Errorf("%s: resolvedScopes %v, want the unlimited ci:read listed bare", where, got)
		}
	}
	check("create", issued.ResolvedScopes)

	rotated, err := client.RotateExternalAgentToken(humanCtx, issued.ID)
	if err != nil {
		t.Fatal(err)
	}
	check("rotate", rotated.ResolvedScopes)

	views, err := client.ListExternalAgentTokens(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 {
		t.Fatalf("listed %d credentials, want 1", len(views))
	}
	check("list", views[0].ResolvedScopes)
}

// fileAsExternal posts body to path as the external agent holding token,
// with headers, and returns the response.
func fileAsExternal(t *testing.T, srv *ui.Server, token, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// filedTask is the id and state of the task a POST /api/tasks answered.
func filedTask(t *testing.T, rec *httptest.ResponseRecorder) ui.Task {
	t.Helper()
	if rec.Code != http.StatusCreated {
		t.Fatalf("filing answered %d, want 201: %s", rec.Code, rec.Body)
	}
	var task ui.Task
	if err := json.Unmarshal(rec.Body.Bytes(), &task); err != nil {
		t.Fatalf("decoding the filed task: %v (%s)", err, rec.Body)
	}
	return task
}

// #XC-14: a credential belongs to the person who issued it, and what it
// does is recorded as external:<name> on behalf of that person -- in the
// journal, in the audit note on the task it touched, and as the requester
// of the work it files -- however the request itself names somebody else.
func TestExternalAgentTokenOwnerAttributionOnJournalAuditAndTaskAuthor(t *testing.T) {
	srv, client := testServer(t)
	ctx := context.Background()
	enableExternalAgentAPI(t, client.Store)
	jane := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "jane@example.com", Source: model.SourceIAP})

	issued, err := client.CreateExternalAgentToken(jane, ui.CreateExternalAgentTokenRequest{
		Name: "jane-laptop", ScopeSet: "grain/external-dev",
	})
	if err != nil {
		t.Fatal(err)
	}
	if issued.Owner != "jane@example.com" {
		t.Fatalf("owner = %q, want the issuing person jane@example.com", issued.Owner)
	}
	listed, err := client.ListExternalAgentTokens(ctx)
	if err != nil || len(listed) != 1 || listed[0].Owner != "jane@example.com" {
		t.Fatalf("listed credentials = %+v, %v; want one owned by jane@example.com", listed, err)
	}
	// An operator may issue one for somebody else; a person minting their
	// own may not, and neither may an agent.
	if other, err := client.CreateExternalAgentToken(jane, ui.CreateExternalAgentTokenRequest{
		Name: "for-bob", ScopeSet: "grain/external-read", Owner: "bob@example.com",
	}); err != nil || other.Owner != "bob@example.com" {
		t.Fatalf("issuing for bob = %+v, %v; want owner bob@example.com", other, err)
	}
	if _, err := client.CreateExternalAgentToken(jane, ui.CreateExternalAgentTokenRequest{
		Name: "self", ScopeSet: "grain/external-read", Owner: "bob@example.com", SelfService: true,
	}); err == nil {
		t.Fatal("a self-service credential was minted on behalf of somebody else")
	}
	agentCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalAgent, ID: "42", Source: model.SourceSandbox})
	if _, err := client.CreateExternalAgentToken(agentCtx, ui.CreateExternalAgentTokenRequest{
		Name: "laundered", ScopeSet: "grain/external-read", Owner: "jane@example.com",
	}); err == nil {
		t.Fatal("an agent minted a credential on behalf of a person")
	}

	var logBuf bytes.Buffer
	origOut := log.Writer()
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(origOut) })

	// The body may not name an author, and X-Forwarded-User is not
	// believed from an external agent.
	if rec := fileAsExternal(t, srv, issued.Token, "/api/tasks",
		`{"title":"x","repo":"acme/widgets","author":"mallory@example.com"}`, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("filing with an author answered %d, want 403: %s", rec.Code, rec.Body)
	}
	task := filedTask(t, fileAsExternal(t, srv, issued.Token, "/api/tasks",
		`{"title":"from the laptop","repo":"acme/widgets"}`,
		map[string]string{"X-Forwarded-User": "mallory@example.com"}))
	stored, err := client.Store.GetTask(ctx, task.ID)
	if err != nil || stored == nil {
		t.Fatalf("reading task %s: %v", task.ID, err)
	}
	if stored.Creator != "jane@example.com" {
		t.Errorf("task creator = %q, want the owner jane@example.com", stored.Creator)
	}
	if a := stored.Origin.Attribution; a.Actor.ID != "external:jane-laptop" || a.OnBehalfOf == nil ||
		a.OnBehalfOf.Kind != model.PrincipalHuman || a.OnBehalfOf.ID != "jane@example.com" {
		t.Errorf("task origin = %+v (on behalf of %+v), want external:jane-laptop on behalf of jane@example.com", a, a.OnBehalfOf)
	}
	if stored.Approval != nil {
		t.Errorf("an unapproved filing landed approved by %+v", stored.Approval)
	}

	// A write to the task leaves an audit note naming the owner, and the
	// journal line says the same -- including on refused calls and with
	// client=<filed_via> when X-Grain-Client is present.
	if rec := fileAsExternal(t, srv, issued.Token, "/api/tasks/"+task.ID+"/comments", `{"body":"hello"}`,
		map[string]string{"X-Grain-Client": "codex-cli/0.9.2 session=s9"}); rec.Code >= 300 {
		t.Fatalf("commenting answered %d: %s", rec.Code, rec.Body)
	}
	comments, err := client.Store.Comments(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	var audit, said bool
	for _, c := range comments {
		if strings.Contains(c.Body, "External agent `jane-laptop`") && strings.Contains(c.Body, "on behalf of `jane@example.com`") {
			audit = true
		}
		if c.Body == "hello" {
			said = c.Author.Actor.ID == "external:jane-laptop" && c.Author.OnBehalfOf != nil && c.Author.OnBehalfOf.ID == "jane@example.com"
		}
	}
	if !audit {
		t.Errorf("no audit note on behalf of jane@example.com among %+v", comments)
	}
	if !said {
		t.Errorf("the agent's comment is not attributed on behalf of jane@example.com: %+v", comments)
	}
	journal := logBuf.String()
	for _, want := range []string{
		"refused {agent, external:jane-laptop on behalf of jane@example.com} POST /api/tasks",
		"{agent, external:jane-laptop on behalf of jane@example.com} POST /api/tasks",
		`client="codex-cli/0.9.2 session=s9"`,
	} {
		if !strings.Contains(journal, want) {
			t.Errorf("journal missing %q:\n%s", want, journal)
		}
	}
}

// #XC-15: X-Grain-Client is stored, sanitized, as the filed_via of the
// task and comments an agent posts, and shown as provenance -- but it is
// a label: it reaches no repository, approval or scope the credential
// does not already have.
func TestXGrainClientHeaderStoredAsFiledViaWithoutGrantingAuthority(t *testing.T) {
	srv, client := testServer(t)
	ctx := context.Background()
	enableExternalAgentAPI(t, client.Store)
	jane := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "jane@example.com", Source: model.SourceIAP})
	issued, err := client.CreateExternalAgentToken(jane, ui.CreateExternalAgentTokenRequest{
		Name: "jetski", Scopes: []string{"tasks:read@acme/widgets", "tasks:write@acme/widgets"},
	})
	if err != nil {
		t.Fatal(err)
	}
	label := map[string]string{"X-Grain-Client": "jetski/1.4\x01 session=abc123"}

	task := filedTask(t, fileAsExternal(t, srv, issued.Token, "/api/tasks", `{"title":"x","repo":"acme/widgets"}`, label))
	if task.FiledVia != "jetski/1.4 session=abc123" {
		t.Errorf("filedVia = %q, want the sanitized label", task.FiledVia)
	}
	if task.Provenance != "filed from jetski/1.4 session=abc123 by jane@example.com" {
		t.Errorf("provenance = %q", task.Provenance)
	}
	if task.State != model.StateProposed {
		t.Errorf("state = %s, want proposed", task.State)
	}
	if rec := fileAsExternal(t, srv, issued.Token, "/api/tasks/"+task.ID+"/comments", `{"body":"progress"}`, label); rec.Code >= 300 {
		t.Fatalf("commenting answered %d: %s", rec.Code, rec.Body)
	}
	detail, err := client.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, c := range detail.Comments {
		if c.Body == "progress" {
			found = c.FiledVia == "jetski/1.4 session=abc123" &&
				c.Provenance == "filed from jetski/1.4 session=abc123 by jane@example.com"
		}
	}
	if !found {
		t.Errorf("the comment does not carry its filed_via: %+v", detail.Comments)
	}

	// Whatever the label claims, the credential's own limits hold.
	for _, claim := range []string{"grain-operator", "owner-trust", "tasks:admin session=root"} {
		h := map[string]string{"X-Grain-Client": claim}
		if rec := fileAsExternal(t, srv, issued.Token, "/api/tasks", `{"title":"x","repo":"acme/gadgets"}`, h); rec.Code != http.StatusForbidden {
			t.Errorf("label %q reached acme/gadgets: %d %s", claim, rec.Code, rec.Body)
		}
		if rec := fileAsExternal(t, srv, issued.Token, "/api/tasks", `{"title":"x","repo":"acme/widgets","approved":true}`, h); rec.Code != http.StatusForbidden {
			t.Errorf("label %q approved its own filing: %d %s", claim, rec.Code, rec.Body)
		}
		if got := filedTask(t, fileAsExternal(t, srv, issued.Token, "/api/tasks", `{"title":"x","repo":"acme/widgets"}`, h)); got.State != model.StateProposed {
			t.Errorf("label %q filed work %s, want proposed", claim, got.State)
		}
	}
	// Nor does the body set it.
	if got := filedTask(t, fileAsExternal(t, srv, issued.Token, "/api/tasks", `{"title":"x","repo":"acme/widgets","filedVia":"forged"}`, nil)); got.FiledVia != "" {
		t.Errorf("a body's filedVia was stored: %q", got.FiledVia)
	}
}

// #XC-17: the deployment's owner-trust policy queues an external agent's
// unapproved filing only when the credential's owner is trusted on the
// filing's repository (or on "*"); every other filing stays proposed.
func TestOwnerTrustProposalPolicyAutoApprovesMatchingOwnerAndRepoOnly(t *testing.T) {
	srv, client := testServer(t)
	ctx := context.Background()
	enableExternalAgentAPI(t, client.Store)
	operator := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "ops@example.com", Source: model.SourceIAP})
	issue := func(name, owner string) string {
		t.Helper()
		issued, err := client.CreateExternalAgentToken(operator, ui.CreateExternalAgentTokenRequest{
			Name: name, ScopeSet: "grain/external-dev", Owner: owner,
		})
		if err != nil {
			t.Fatal(err)
		}
		return issued.Token
	}
	janeTok, bobTok := issue("jane-laptop", "jane@example.com"), issue("bob-laptop", "bob@example.com")

	// grain/external-dev cannot write the policy itself.
	req := httptest.NewRequest(http.MethodPut, "/api/settings",
		strings.NewReader(`{"ownerTrust":[{"owner":"jane@example.com","repo":"*"}]}`))
	req.Header.Set("Authorization", "Bearer "+janeTok)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("an external-dev credential wrote settings: %d %s", rec.Code, rec.Body)
	}

	rules := []model.OwnerTrustRule{{Owner: "jane@example.com", Repo: "acme/widgets"}}
	settings, err := client.UpdateSettings(operator, ui.UpdateSettingsRequest{OwnerTrust: &rules})
	if err != nil {
		t.Fatal(err)
	}
	if len(settings.OwnerTrust) != 1 || settings.OwnerTrust[0] != rules[0] {
		t.Fatalf("settings ownerTrust = %+v, want %+v", settings.OwnerTrust, rules)
	}

	file := func(token, repo string, headers map[string]string) *model.Task {
		t.Helper()
		got := filedTask(t, fileAsExternal(t, srv, token, "/api/tasks", `{"title":"x","repo":"`+repo+`"}`, headers))
		stored, err := client.Store.GetTask(ctx, got.ID)
		if err != nil || stored == nil {
			t.Fatalf("reading task %s: %v", got.ID, err)
		}
		return stored
	}
	trusted := file(janeTok, "acme/widgets", nil)
	if a := trusted.Approval; a == nil || a.Actor != model.OwnerTrustPrincipal || a.OnBehalfOf == nil || a.OnBehalfOf.ID != "jane@example.com" {
		t.Fatalf("jane's filing on acme/widgets approval = %+v, want owner-trust on behalf of jane@example.com", a)
	}
	if detail, err := client.GetTask(ctx, trusted.ID); err != nil || detail.State != model.StateQueued {
		t.Errorf("jane's filing on acme/widgets is %s (%v), want queued", detail.State, err)
	}
	for _, c := range []struct {
		name, token, repo string
		headers           map[string]string
	}{
		{"owner on another repository", janeTok, "acme/gadgets", nil},
		{"another owner on the trusted repository", bobTok, "acme/widgets", nil},
		{"another owner whose label claims the trusted one", bobTok, "acme/widgets",
			map[string]string{"X-Grain-Client": "jane@example.com", "X-Forwarded-User": "jane@example.com"}},
	} {
		if got := file(c.token, c.repo, c.headers); got.Approval != nil {
			t.Errorf("%s: filing approved by %+v, want proposed", c.name, got.Approval)
		}
	}

	// (owner, "*") trusts that owner everywhere.
	rules = []model.OwnerTrustRule{{Owner: "bob@example.com", Repo: model.OwnerTrustAnyRepo}}
	if _, err := client.UpdateSettings(operator, ui.UpdateSettingsRequest{OwnerTrust: &rules}); err != nil {
		t.Fatal(err)
	}
	if got := file(bobTok, "acme/gadgets", nil); got.Approval == nil || got.Approval.Actor != model.OwnerTrustPrincipal {
		t.Errorf("bob's filing under (bob, *) approval = %+v, want owner-trust", got.Approval)
	}
	if got := file(janeTok, "acme/widgets", nil); got.Approval != nil {
		t.Errorf("jane's filing after her rule was removed approved by %+v, want proposed", got.Approval)
	}
	// A rule with no repository is refused.
	bad := []model.OwnerTrustRule{{Owner: "bob@example.com"}}
	if _, err := client.UpdateSettings(operator, ui.UpdateSettingsRequest{OwnerTrust: &bad}); err == nil {
		t.Error("a rule naming no repository was accepted")
	}
	// Control characters in owner are refused on both settings and token issuance.
	badCtrl := []model.OwnerTrustRule{{Owner: "bob\x1b[31m@example.com", Repo: "acme/widgets"}}
	if _, err := client.UpdateSettings(operator, ui.UpdateSettingsRequest{OwnerTrust: &badCtrl}); err == nil {
		t.Error("an ownerTrust rule with control characters in owner was accepted")
	}
	if _, err := client.CreateExternalAgentToken(operator, ui.CreateExternalAgentTokenRequest{
		Name: "ctrl-owner", ScopeSet: "grain/external-dev", Owner: "jane\x00@example.com",
	}); err == nil {
		t.Error("CreateExternalAgentToken accepted control characters in owner")
	}
}

func TestOwnerTrustChecksReadsWritesHoldsAndPatchEdits(t *testing.T) {
	srv, client := testServer(t)
	ctx := context.Background()
	enableExternalAgentAPI(t, client.Store)
	operator := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "ops@example.com", Source: model.SourceIAP})
	rules := []model.OwnerTrustRule{{Owner: "jane@example.com", Repo: "acme/widgets"}}
	if _, err := client.UpdateSettings(operator, ui.UpdateSettingsRequest{OwnerTrust: &rules}); err != nil {
		t.Fatal(err)
	}
	issue := func(name, owner string) string {
		t.Helper()
		issued, err := client.CreateExternalAgentToken(operator, ui.CreateExternalAgentTokenRequest{
			Name: name, ScopeSet: "grain/external-dev", Owner: owner,
		})
		if err != nil {
			t.Fatal(err)
		}
		return issued.Token
	}
	janeTok := issue("jane-dev", "jane@example.com")
	bobTok := issue("bob-dev", "bob@example.com")

	// Cross-repo writes, cross-repo reads, and holdForApproval must not auto-approve
	// under an ownerTrust rule that covers only acme/widgets.
	for _, tc := range []struct {
		name string
		body string
	}{
		{"untrusted extra write repo", `{"title":"x","repo":"acme/widgets","writes":[{"repo":"acme/gadgets"}]}`},
		{"untrusted extra read repo", `{"title":"x","repo":"acme/widgets","reads":["acme/gadgets"]}`},
		{"explicit holdForApproval", `{"title":"x","repo":"acme/widgets","holdForApproval":true}`},
	} {
		created := filedTask(t, fileAsExternal(t, srv, janeTok, "/api/tasks", tc.body, nil))
		stored, err := client.Store.GetTask(ctx, created.ID)
		if err != nil || stored == nil {
			t.Fatalf("%s: reading task %s: %v", tc.name, created.ID, err)
		}
		if stored.Approval != nil {
			t.Errorf("%s: approval = %+v, want nil (proposed)", tc.name, stored.Approval)
		}
	}

	doPatch := func(token, id, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPatch, "/api/tasks/"+id, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}

	// Editing title on Jane's own owner-trust task within acme/widgets stays queued.
	approvedTask := filedTask(t, fileAsExternal(t, srv, janeTok, "/api/tasks", `{"title":"orig","repo":"acme/widgets"}`, nil))
	if rec := doPatch(janeTok, approvedTask.ID, `{"title":"updated within widgets"}`); rec.Code != http.StatusOK {
		t.Fatalf("jane editing own owner-trust task within trusted repo: %d %s", rec.Code, rec.Body)
	}
	if stored, _ := client.Store.GetTask(ctx, approvedTask.ID); stored == nil || stored.Approval == nil || stored.Approval.Actor != model.OwnerTrustPrincipal {
		t.Fatalf("expected owner-trust approval kept on in-scope title edit, got %+v", stored)
	}

	// Another owner's external-dev credential editing Jane's owner-trust task
	// withdraws Jane's owner-trust approval so Bob's edits wait for a person.
	if rec := doPatch(bobTok, approvedTask.ID, `{"description":"edited by bob"}`); rec.Code != http.StatusOK {
		t.Fatalf("bob editing task: status %d (%s), want 200", rec.Code, rec.Body)
	}
	if stored, _ := client.Store.GetTask(ctx, approvedTask.ID); stored == nil || stored.Approval != nil {
		t.Errorf("expected owner-trust approval withdrawn after untrusted owner's edit, got %+v", stored)
	}

	// Moving repo, adding writes, or adding reads outside Jane's ownerTrust via PATCH
	// withdraws the owner-trust approval so the task reverts to proposed.
	for _, patch := range []string{
		`{"repo":"acme/gadgets"}`,
		`{"writes":[{"repo":"acme/gadgets"}]}`,
		`{"reads":["acme/gadgets"]}`,
	} {
		tk := filedTask(t, fileAsExternal(t, srv, janeTok, "/api/tasks", `{"title":"move-me","repo":"acme/widgets"}`, nil))
		if rec := doPatch(janeTok, tk.ID, patch); rec.Code != http.StatusOK {
			t.Fatalf("PATCH %s on %s: %d %s", patch, tk.ID, rec.Code, rec.Body)
		}
		stored, err := client.Store.GetTask(ctx, tk.ID)
		if err != nil || stored == nil {
			t.Fatalf("GetTask(%s): %v", tk.ID, err)
		}
		if stored.Approval != nil {
			t.Errorf("after PATCH %s, approval = %+v, want withdrawn (proposed)", patch, stored.Approval)
		}
	}
}

func TestExternalAgentEscalationRailsAndTokenRotationOwnerGuard(t *testing.T) {
	srv, client := testServer(t)
	ctx := context.Background()
	enableExternalAgentAPI(t, client.Store)
	operator := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "ops@example.com", Source: model.SourceIAP})
	netNone := string(model.NetworkNone)
	if _, err := client.UpdateSettings(operator, ui.UpdateSettingsRequest{NetworkAccess: &netNone}); err != nil {
		t.Fatal(err)
	}

	widgets, _ := model.ParseRepo("acme/widgets")
	highPrio := 8
	for _, tmpl := range []model.Template{
		{ID: "tpl-targetless", Name: "targetless", Title: "no target"},
		{ID: "tpl-widgets", Name: "widgets", Title: "on widgets", Target: &widgets},
		{ID: "tpl-net-open", Name: "net-open", Title: "open net", Target: &widgets, NetworkAccess: model.NetworkOpen},
		{ID: "tpl-durable", Name: "durable", Title: "durable", Target: &widgets, KeepRunning: true},
		{ID: "tpl-prio", Name: "prio", Title: "high prio", Target: &widgets, Priority: &highPrio},
		{ID: "tpl-interactive", Name: "interactive", Title: "interactive", Target: &widgets, Interactive: true},
	} {
		if err := client.Store.PutTemplate(ctx, tmpl); err != nil {
			t.Fatal(err)
		}
	}

	janeIssued, err := client.CreateExternalAgentToken(operator, ui.CreateExternalAgentTokenRequest{
		Name: "jane-ext", ScopeSet: "grain/external-dev", Owner: "jane@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	seedAPITask(t, client, "200", "acme/widgets")

	call := func(token, method, path, body string) (int, string) {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(ui.ActingReasonHeader, "testing")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}

	// #XC-17 & parity rails: priority > 5, keepRunning, creator, interactive,
	// fixesBaseBranch, targetless template without repo, and escalating
	// templates are refused with 403.
	for _, tc := range []struct {
		method, path, body string
	}{
		{http.MethodPost, "/api/tasks", `{"title":"x","repo":"acme/widgets","priority":6}`},
		{http.MethodPatch, "/api/tasks/200", `{"priority":6}`},
		{http.MethodPost, "/api/tasks", `{"title":"x","repo":"acme/widgets","keepRunning":true}`},
		{http.MethodPost, "/api/tasks", `{"title":"x","repo":"acme/widgets","creator":"ops@example.com"}`},
		{http.MethodPost, "/api/tasks", `{"title":"x","repo":"acme/widgets","interactive":true}`},
		{http.MethodPost, "/api/tasks", `{"title":"x","repo":"acme/widgets","fixesBaseBranch":true}`},
		{http.MethodPatch, "/api/tasks/200", `{"fixesBaseBranch":true}`},
		{http.MethodPost, "/api/tasks", `{"title":"x","templateId":"tpl-targetless"}`},
		{http.MethodPost, "/api/tasks", `{"title":"x","templateId":"tpl-net-open"}`},
		{http.MethodPost, "/api/tasks", `{"title":"x","templateId":"tpl-durable"}`},
		{http.MethodPost, "/api/tasks", `{"title":"x","templateId":"tpl-prio"}`},
		{http.MethodPost, "/api/tasks", `{"title":"x","templateId":"tpl-interactive"}`},
	} {
		if code, resp := call(janeIssued.Token, tc.method, tc.path, tc.body); code != http.StatusForbidden {
			t.Errorf("%s %s %s: status %d (%s), want 403", tc.method, tc.path, tc.body, code, resp)
		}
	}

	// A task has no hostd at all (docs/host-placement.md): naming one is
	// refused for everybody, an external agent included, never routed.
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/tasks", `{"title":"x","repo":"acme/widgets","hostd":"workstation-1"}`},
		{http.MethodPut, "/api/tasks/200/hostd", `{"hostd":"workstation-1"}`},
	} {
		code, resp := call(janeIssued.Token, tc.method, tc.path, tc.body)
		if code != http.StatusBadRequest && code != http.StatusForbidden {
			t.Errorf("%s %s %s: status %d (%s), want it refused", tc.method, tc.path, tc.body, code, resp)
		}
		if code == http.StatusBadRequest && !strings.Contains(resp, "not the task") {
			t.Errorf("%s %s: refused with %q, want it to say hostd is set on the connection or server", tc.method, tc.path, resp)
		}
	}

	// Default/lower priority (<= 5) and a clean template naming its own target repo succeed.
	if code, resp := call(janeIssued.Token, http.MethodPost, "/api/tasks", `{"title":"ok","repo":"acme/widgets","priority":3}`); code != http.StatusCreated {
		t.Errorf("POST priority=3 failed: %d %s", code, resp)
	}
	if code, resp := call(janeIssued.Token, http.MethodPost, "/api/tasks", `{"title":"from-tmpl","templateId":"tpl-widgets"}`); code != http.StatusCreated {
		t.Errorf("POST templateId=tpl-widgets failed: %d %s", code, resp)
	}

	// Token rotation owner guard: neither an in-sandbox agent nor another owner's
	// agent holding capabilities:admin + external-dev may rotate Jane's credential
	// to obtain a live bearer token acting as jane@example.com, nor mint selfService.
	seedAPITask(t, client, "201", "acme/widgets", "api:capabilities:admin", "api:grain/external-dev")
	if code, resp := asAgent(t, srv, "201", http.MethodPost, "/api/external-agents/tokens/"+janeIssued.ID+"/rotate", `{}`, "rotating"); code != http.StatusForbidden {
		t.Errorf("in-sandbox agent rotated Jane's owned token: %d %s, want 403", code, resp)
	}
	if code, resp := asAgent(t, srv, "201", http.MethodPost, "/api/external-agents/tokens",
		`{"name":"self-by-agent","scopeSet":"grain/external-dev","selfService":true}`, "minting"); code != http.StatusForbidden {
		t.Errorf("in-sandbox agent minted selfService credential: %d %s, want 403", code, resp)
	}
	bobAdmin, err := client.CreateExternalAgentToken(operator, ui.CreateExternalAgentTokenRequest{
		Name:   "bob-cap-admin",
		Scopes: []string{"capabilities:admin", "tasks:read", "tasks:write", "ci:read", "ci:write", "merge:write", "repos:read", "templates:read", "deployment:read"},
		Owner:  "bob@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if code, resp := call(bobAdmin.Token, http.MethodPost, "/api/external-agents/tokens/"+janeIssued.ID+"/rotate", `{}`); code != http.StatusForbidden {
		t.Errorf("bob's agent rotated Jane's token: %d %s, want 403", code, resp)
	}
}
