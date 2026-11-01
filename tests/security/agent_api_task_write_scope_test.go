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

package security

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite/sqlitetest"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func newTaskScopeSecurityRig(t *testing.T) (*ui.Server, *ui.Client, *model.Store) {
	t.Helper()
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)
	store, _ := sqlitetest.Open(t)
	ctx := context.Background()

	cfg := model.DefaultConfig()
	cfg.TargetRepos = []string{"acme/widgets", "other/gadgets"}
	cfg.ExternalAgentAPI = true
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	client := ui.NewClient(ui.Config{
		Actor: ui.DefaultActor("operator"),
	}, store)
	srv := ui.NewServerWithClient(client)
	return srv, client, store
}

func issueTestExternalToken(t *testing.T, client *ui.Client, name string, scopes, repos []string) string {
	t.Helper()
	issued, err := client.CreateExternalAgentToken(context.Background(), ui.CreateExternalAgentTokenRequest{
		Name:   name,
		Scopes: scopes,
		Repos:  repos,
	})
	if err != nil {
		t.Fatalf("CreateExternalAgentToken(%s): %v", name, err)
	}
	return issued.Token
}

func doBearerJSON(t *testing.T, handler http.Handler, token, method, path, body string, extraHeaders map[string]string) (int, string) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func toolNameForPattern(t *testing.T, pattern string) string {
	t.Helper()
	for _, r := range ui.AgentAPIRoutes() {
		if r.Pattern == pattern {
			return r.Tool
		}
	}
	t.Fatalf("no generated tool for route %s", pattern)
	return ""
}

func callExternalMCPTool(t *testing.T, srv *ui.Server, token, toolName string, args map[string]any) (isError bool, text string) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      toolName,
			"arguments": args,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /mcp status = %d: %s", rec.Code, rec.Body.String())
	}
	var rpc struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rpc); err != nil {
		t.Fatalf("unmarshal MCP response (%s): %v", rec.Body.String(), err)
	}
	if rpc.Error != nil {
		return true, rpc.Error.Message
	}
	var sb strings.Builder
	for _, c := range rpc.Result.Content {
		sb.WriteString(c.Text)
	}
	return rpc.Result.IsError, sb.String()
}

func assertNoSecretMarkers(t *testing.T, where, body string, markers ...string) {
	t.Helper()
	for _, m := range markers {
		if strings.Contains(body, m) {
			t.Errorf("%s leaked marker %q in response: %s", where, m, body)
		}
	}
}

func assertJSONKeysAtMost(t *testing.T, where, body string, allowed ...string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("%s: unmarshal JSON (%s): %v", where, body, err)
	}
	allowSet := map[string]bool{}
	for _, k := range allowed {
		allowSet[k] = true
	}
	for k := range m {
		if !allowSet[k] {
			t.Errorf("%s returned unexpected field %q (allowed %v) in body: %s", where, k, allowed, body)
		}
	}
	return m
}

// TestExternalAgentTaskWriteDoesNotReturnTaskRecord verifies that an external
// gma_ credential holding tasks:write (or tasks:admin) without tasks:read
// cannot read existing task prompts, titles, comments, template bodies, or
// sibling queue entries through mutating task endpoints or POST /mcp.
func TestExternalAgentTaskWriteDoesNotReturnTaskRecord(t *testing.T) {
	srv, client, store := newTaskScopeSecurityRig(t)
	ctx := context.Background()

	const (
		titleMarker    = "SECRET-TITLE-MARKER-991"
		promptMarker   = "SECRET-PROMPT-MARKER-991"
		extMarker      = "SECRET-PROMPT-EXT-991"
		commentMarker  = "SECRET-COMMENT-MARKER-991"
		otherMarker    = "SECRET-OTHER-REPO-MARKER-992"
		templateMarker = "SECRET-TEMPLATE-BODY-993"
	)
	allMarkers := []string{titleMarker, promptMarker, extMarker, commentMarker, otherMarker, templateMarker}

	victim, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title:           titleMarker,
		Description:     promptMarker,
		PromptExtension: extMarker,
		Repo:            "acme/widgets",
		Approved:        true,
	})
	if err != nil {
		t.Fatalf("CreateTask(victim): %v", err)
	}
	if err := client.AddComment(ctx, victim.ID, commentMarker, nil); err != nil {
		t.Fatalf("AddComment(victim): %v", err)
	}

	helperTask, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title:       "helper task",
		Description: "helper body",
		Repo:        "acme/widgets",
		Approved:    false,
	})
	if err != nil {
		t.Fatalf("CreateTask(helper): %v", err)
	}

	otherTask, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title:       otherMarker,
		Description: otherMarker + "-desc",
		Repo:        "other/gadgets",
		Approved:    true,
	})
	if err != nil {
		t.Fatalf("CreateTask(other): %v", err)
	}

	acmeRef := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutTemplate(ctx, model.Template{
		ID:     "sec-tmpl",
		Name:   "Security Template",
		Title:  "From template",
		Body:   templateMarker,
		Target: &acmeRef,
	}); err != nil {
		t.Fatalf("PutTemplate: %v", err)
	}

	writeOnlyTok := issueTestExternalToken(t, client, "write-only", []string{"tasks:write"}, []string{"acme/widgets"})

	// 1. Direct GET is refused with 403.
	if code, body := doBearerJSON(t, srv, writeOnlyTok, http.MethodGet, "/api/tasks/"+victim.ID, "", nil); code != http.StatusForbidden {
		t.Fatalf("GET /api/tasks/%s status = %d, want 403: %s", victim.ID, code, body)
	}

	// 2. Empty and no-op PATCH bodies are refused with 400 and never return the task.
	for _, nop := range []string{"", "{}", `{"description":null}`, `{"unrecognizedField":1}`} {
		code, body := doBearerJSON(t, srv, writeOnlyTok, http.MethodPatch, "/api/tasks/"+victim.ID, nop, nil)
		if code != http.StatusBadRequest {
			t.Errorf("PATCH /api/tasks/%s with %q: status = %d, want 400 (body: %s)", victim.ID, nop, code, body)
		}
		assertNoSecretMarkers(t, "PATCH no-op "+nop, body, allMarkers...)
	}
	patchTool := toolNameForPattern(t, "PATCH /api/tasks/{id}")
	commentTool := toolNameForPattern(t, "POST /api/tasks/{id}/comments")
	if isErr, text := callExternalMCPTool(t, srv, writeOnlyTok, patchTool, map[string]any{"id": victim.ID}); !isErr {
		t.Errorf("MCP %s with no edits succeeded: %s", patchTool, text)
	} else {
		assertNoSecretMarkers(t, "MCP empty PATCH", text, allMarkers...)
	}

	// 3. Valid PATCH returns only {id, state, <editedField>} over both HTTP and MCP.
	code, body := doBearerJSON(t, srv, writeOnlyTok, http.MethodPatch, "/api/tasks/"+victim.ID, `{"base":"release-1.0"}`, nil)
	if code != http.StatusOK {
		t.Fatalf("PATCH base status = %d: %s", code, body)
	}
	assertNoSecretMarkers(t, "PATCH base", body, allMarkers...)
	gotPatch := assertJSONKeysAtMost(t, "PATCH base", body, "id", "state", "base")
	if gotPatch["id"] != victim.ID || gotPatch["base"] != "release-1.0" {
		t.Errorf("PATCH base response = %v", gotPatch)
	}

	if isErr, text := callExternalMCPTool(t, srv, writeOnlyTok, patchTool, map[string]any{
		"id":   victim.ID,
		"base": "release-2.0",
	}); isErr {
		t.Fatalf("MCP PATCH base failed: %s", text)
	} else {
		assertNoSecretMarkers(t, "MCP PATCH base", text, allMarkers...)
		assertJSONKeysAtMost(t, "MCP PATCH base", text, "id", "state", "base")
	}

	// 4. POST /comments and single-task lifecycle mutations return only {id, state}.
	mutations := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"comment", http.MethodPost, "/api/tasks/" + victim.ID + "/comments", `{"body":"hello"}`},
		{"defer", http.MethodPost, "/api/tasks/" + victim.ID + "/defer", `{}`},
		{"undefer", http.MethodPost, "/api/tasks/" + victim.ID + "/undefer", `{}`},
		{"pause", http.MethodPost, "/api/tasks/" + victim.ID + "/pause", `{"reason":"checking"}`},
		{"resume", http.MethodPost, "/api/tasks/" + victim.ID + "/resume", `{}`},
		{"put-goal", http.MethodPut, "/api/tasks/" + victim.ID + "/goal", `{"kind":"person-closes"}`},
		{"delete-goal", http.MethodDelete, "/api/tasks/" + victim.ID + "/goal", ``},
		{"depends-on", http.MethodPost, "/api/tasks/" + victim.ID + "/depends-on", fmt.Sprintf(`{"id":%q,"present":true}`, helperTask.ID)},
		{"withdraw-approval", http.MethodPost, "/api/tasks/" + victim.ID + "/withdraw-approval", `{}`},
		{"close", http.MethodPost, "/api/tasks/" + victim.ID + "/close", `{}`},
		{"reopen", http.MethodPost, "/api/tasks/" + victim.ID + "/reopen", `{}`},
	}
	for _, m := range mutations {
		headers := map[string]string{"X-Grain-Acting-Reason": "security test"}
		c, b := doBearerJSON(t, srv, writeOnlyTok, m.method, m.path, m.body, headers)
		if c != http.StatusOK {
			t.Fatalf("%s (%s %s) status = %d: %s", m.name, m.method, m.path, c, b)
		}
		assertNoSecretMarkers(t, m.name, b, allMarkers...)
		got := assertJSONKeysAtMost(t, m.name, b, "id", "state")
		if got["id"] != victim.ID {
			t.Errorf("%s id = %v, want %s", m.name, got["id"], victim.ID)
		}
	}

	if isErr, text := callExternalMCPTool(t, srv, writeOnlyTok, commentTool, map[string]any{
		"id":   victim.ID,
		"body": map[string]any{"body": "mcp note"},
	}); isErr {
		t.Fatalf("MCP comment failed: %s", text)
	} else {
		assertNoSecretMarkers(t, "MCP comment", text, allMarkers...)
		assertJSONKeysAtMost(t, "MCP comment", text, "id", "state")
	}

	// 5. Creating a task from a template without tasks:read returns {id, state} and hides the template prompt.
	code, body = doBearerJSON(t, srv, writeOnlyTok, http.MethodPost, "/api/tasks",
		`{"repo":"acme/widgets","templateId":"sec-tmpl","title":"filed"}`, nil)
	if code != http.StatusCreated {
		t.Fatalf("POST /api/tasks status = %d: %s", code, body)
	}
	assertNoSecretMarkers(t, "POST /api/tasks with template", body, allMarkers...)
	assertJSONKeysAtMost(t, "POST /api/tasks with template", body, "id", "state")

	// 6. POST /api/tasks/{id}/wait-on strips target title and closing note without tasks:read.
	code, body = doBearerJSON(t, srv, writeOnlyTok, http.MethodPost, "/api/tasks/"+helperTask.ID+"/wait-on",
		fmt.Sprintf(`{"ids":[%q]}`, victim.ID), nil)
	if code != http.StatusOK {
		t.Fatalf("POST wait-on status = %d: %s", code, body)
	}
	assertNoSecretMarkers(t, "POST wait-on", body, allMarkers...)

	// 7. POST /api/tasks/reorder does not disclose ungranted tasks or task records without tasks:read.
	globalWriteTok := issueTestExternalToken(t, client, "write-all", []string{"tasks:write"}, nil)
	code, body = doBearerJSON(t, srv, globalWriteTok, http.MethodPost, "/api/tasks/reorder",
		fmt.Sprintf(`{"ids":[%q,%q]}`, victim.ID, helperTask.ID), nil)
	if code != http.StatusOK {
		t.Fatalf("POST reorder status = %d: %s", code, body)
	}
	assertNoSecretMarkers(t, "POST reorder (no tasks:read)", body, allMarkers...)
	var reordered []map[string]any
	if err := json.Unmarshal([]byte(body), &reordered); err != nil {
		t.Fatalf("unmarshal reorder: %v", err)
	}
	if len(reordered) != 2 {
		t.Fatalf("reorder returned %d items, want 2 requested stubs: %s", len(reordered), body)
	}
	for _, item := range reordered {
		if len(item) != 2 || item["id"] == nil || item["state"] == nil {
			t.Errorf("reorder stub = %v, want only {id, state}", item)
		}
	}

	// Cross-repo move from unreadable other/gadgets to readable acme/widgets still redacts.
	mixedTok := issueTestExternalToken(t, client, "mixed-rw", []string{"tasks:write", "tasks:read@acme/widgets"}, nil)
	code, body = doBearerJSON(t, srv, mixedTok, http.MethodPatch, "/api/tasks/"+otherTask.ID,
		`{"repo":"acme/widgets"}`, nil)
	if code != http.StatusOK {
		t.Fatalf("cross-repo PATCH status = %d: %s", code, body)
	}
	assertNoSecretMarkers(t, "cross-repo move PATCH", body, allMarkers...)
	assertJSONKeysAtMost(t, "cross-repo move PATCH", body, "id", "state", "repo")

	// 8. tasks:admin-only credential: {} is refused with 403, numeric priority PATCH and approve redact.
	adminOnlyTok := issueTestExternalToken(t, client, "admin-only", []string{"tasks:admin"}, []string{"acme/widgets"})
	if code, body := doBearerJSON(t, srv, adminOnlyTok, http.MethodPatch, "/api/tasks/"+victim.ID, `{}`, nil); code != http.StatusForbidden {
		t.Errorf("tasks:admin empty {} PATCH status = %d, want 403: %s", code, body)
	}
	if code, body := doBearerJSON(t, srv, adminOnlyTok, http.MethodPatch, "/api/tasks/"+victim.ID, `{"priority":null}`, nil); code != http.StatusForbidden {
		t.Errorf("tasks:admin null priority PATCH status = %d, want 403: %s", code, body)
	}
	code, body = doBearerJSON(t, srv, adminOnlyTok, http.MethodPatch, "/api/tasks/"+victim.ID, `{"priority":8}`, nil)
	if code != http.StatusOK {
		t.Fatalf("tasks:admin priority PATCH status = %d: %s", code, body)
	}
	assertNoSecretMarkers(t, "tasks:admin priority PATCH", body, allMarkers...)
	gotPrio := assertJSONKeysAtMost(t, "tasks:admin priority PATCH", body, "id", "state", "priority")
	if gotPrio["priority"] != float64(8) {
		t.Errorf("tasks:admin priority = %v, want 8", gotPrio["priority"])
	}

	code, body = doBearerJSON(t, srv, adminOnlyTok, http.MethodPost, "/api/tasks/"+helperTask.ID+"/approve", `{}`,
		map[string]string{"X-Grain-Acting-Reason": "triaged"})
	if code != http.StatusOK {
		t.Fatalf("tasks:admin approve status = %d: %s", code, body)
	}
	assertJSONKeysAtMost(t, "tasks:admin approve", body, "id", "state")

	// 9. Credential holding BOTH tasks:read and tasks:write still receives full Task / TaskDetail records,
	// while empty PATCH remains 400.
	readWriteTok := issueTestExternalToken(t, client, "read-write", []string{"tasks:read", "tasks:write"}, []string{"acme/widgets"})
	if code, _ := doBearerJSON(t, srv, readWriteTok, http.MethodPatch, "/api/tasks/"+victim.ID, `{}`, nil); code != http.StatusBadRequest {
		t.Errorf("tasks:read+write empty PATCH status = %d, want 400", code)
	}
	code, body = doBearerJSON(t, srv, readWriteTok, http.MethodPatch, "/api/tasks/"+victim.ID, `{"base":"main"}`, nil)
	if code != http.StatusOK {
		t.Fatalf("tasks:read+write PATCH status = %d: %s", code, body)
	}
	if !strings.Contains(body, promptMarker) || !strings.Contains(body, titleMarker) {
		t.Errorf("tasks:read+write PATCH omitted task record: %s", body)
	}
	code, body = doBearerJSON(t, srv, readWriteTok, http.MethodPost, "/api/tasks/"+victim.ID+"/comments", `{"body":"rw comment"}`, nil)
	if code != http.StatusOK {
		t.Fatalf("tasks:read+write comment status = %d: %s", code, body)
	}
	if !strings.Contains(body, promptMarker) || !strings.Contains(body, titleMarker) || !strings.Contains(body, extMarker) {
		t.Errorf("tasks:read+write comment omitted TaskDetail record: %s", body)
	}
}

// TestSandboxChannelTaskWriteDoesNotReturnTaskRecord verifies the same
// tasks:write vs tasks:read boundary on the sandbox proxy channel
// (GitProxy.ServeAPIHTTP -> X-Grain-Channel: sandbox-cli).
func TestSandboxChannelTaskWriteDoesNotReturnTaskRecord(t *testing.T) {
	srv, client, store := newTaskScopeSecurityRig(t)
	ctx := context.Background()

	const (
		siblingTitle   = "SANDBOX-SIBLING-TITLE-771"
		siblingPrompt  = "SANDBOX-SIBLING-PROMPT-771"
		siblingComment = "SANDBOX-SIBLING-COMMENT-771"
		otherPrompt    = "SANDBOX-OTHER-PROMPT-772"
	)
	markers := []string{siblingTitle, siblingPrompt, siblingComment, otherPrompt}

	sibling, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title:       siblingTitle,
		Description: siblingPrompt,
		Repo:        "acme/widgets",
		Approved:    true,
	})
	if err != nil {
		t.Fatalf("CreateTask(sibling): %v", err)
	}
	if err := client.AddComment(ctx, sibling.ID, siblingComment, nil); err != nil {
		t.Fatalf("AddComment(sibling): %v", err)
	}

	if _, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title:       otherPrompt,
		Description: otherPrompt,
		Repo:        "other/gadgets",
		Approved:    true,
	}); err != nil {
		t.Fatalf("CreateTask(other): %v", err)
	}

	acme := model.RepoRef{Owner: "acme", Name: "widgets"}
	putSandboxRun := func(taskID, sandbox string, caps ...string) {
		grants := make([]model.Grant, 0, len(caps))
		for _, c := range caps {
			grants = append(grants, model.Grant{Capability: c, Via: model.GrantByLabel})
		}
		if err := store.PutTask(ctx, model.Task{
			ID:     taskID,
			Intent: model.IntentImplement,
			Title:  "runner " + taskID,
			Origin: model.Origin{
				Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "operator"}},
				Reason:      model.ReasonDirect,
			},
			Target:  &acme,
			Grants:  grants,
			Binding: model.BindingDirective,
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.StartRun(ctx, model.Run{
			ID:        taskID + "-run-1",
			TaskID:    taskID,
			Sandbox:   sandbox,
			Attempt:   1,
			StartedAt: time.Now(),
		}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
	}

	putSandboxRun("writer-task", "sandbox-writer", "api:tasks:write")
	putSandboxRun("rw-task", "sandbox-rw", "api:tasks:read@acme/widgets", "api:tasks:write@acme/widgets")

	upstream := &gitUpstream{}
	backend := httptest.NewServer(upstream)
	t.Cleanup(backend.Close)
	backendURL, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}

	dataDir := t.TempDir()
	writeSecret(t, filepath.Join(dataDir, "secrets", "github", "credentials.json"), `{"*": "bot"}`)
	writeSecret(t, filepath.Join(dataDir, "secrets", "github", "bot.token"), "bot-token")
	writeSecret(t, filepath.Join(dataDir, "secrets", "sandbox-tokens.json"),
		`{"sandbox-writer": "tok-writer", "sandbox-rw": "tok-rw"}`)

	proxy, err := gitproxy.BuildProxy(gitproxy.BuildConfig{
		DataDir:     dataDir,
		Store:       store,
		ForwardHost: backendURL.Host,
		ForwardTLS:  false,
		ReadScopes:  gitproxy.NewReadScopes(),
		WriteScopes: gitproxy.NewWriteScopes(),
	})
	if err != nil {
		t.Fatal(err)
	}
	proxy.API = srv
	proxySrv := httptest.NewServer(gitproxy.NewHandler(proxy))
	t.Cleanup(proxySrv.Close)

	doSandboxAPI := func(sandbox, token, method, path, body string) (int, string) {
		t.Helper()
		var rdr io.Reader
		if body != "" {
			rdr = strings.NewReader(body)
		}
		req, err := http.NewRequest(method, proxySrv.URL+path, rdr)
		if err != nil {
			t.Fatal(err)
		}
		req.SetBasicAuth(sandbox, token)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := proxySrv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}

	// GET is refused with 403 over the sandbox channel.
	if code, body := doSandboxAPI("sandbox-writer", "tok-writer", http.MethodGet, "/api/tasks/"+sibling.ID, ""); code != http.StatusForbidden {
		t.Fatalf("sandbox GET /api/tasks/%s status = %d, want 403: %s", sibling.ID, code, body)
	}

	// Empty {} PATCH is refused with 400 and leaks nothing.
	if code, body := doSandboxAPI("sandbox-writer", "tok-writer", http.MethodPatch, "/api/tasks/"+sibling.ID, "{}"); code != http.StatusBadRequest {
		t.Fatalf("sandbox empty PATCH status = %d, want 400: %s", code, body)
	} else {
		assertNoSecretMarkers(t, "sandbox empty PATCH", body, markers...)
	}

	// Valid PATCH over sandbox channel returns only {id, state, base}.
	code, body := doSandboxAPI("sandbox-writer", "tok-writer", http.MethodPatch, "/api/tasks/"+sibling.ID, `{"base":"sandbox-branch"}`)
	if code != http.StatusOK {
		t.Fatalf("sandbox PATCH status = %d: %s", code, body)
	}
	assertNoSecretMarkers(t, "sandbox PATCH", body, markers...)
	assertJSONKeysAtMost(t, "sandbox PATCH", body, "id", "state", "base")

	// Comments, defer/undefer, reorder, and wait-on over the sandbox channel leak no task records.
	for _, tc := range []struct {
		name string
		path string
		body string
		keys []string
	}{
		{"comment", "/api/tasks/" + sibling.ID + "/comments", `{"body":"from sandbox"}`, []string{"id", "state"}},
		{"defer", "/api/tasks/" + sibling.ID + "/defer", `{}`, []string{"id", "state"}},
		{"undefer", "/api/tasks/" + sibling.ID + "/undefer", `{}`, []string{"id", "state"}},
		{"wait-on", "/api/tasks/rw-task/wait-on", fmt.Sprintf(`{"ids":[%q]}`, sibling.ID), []string{"targets"}},
	} {
		c, b := doSandboxAPI("sandbox-writer", "tok-writer", http.MethodPost, tc.path, tc.body)
		if c != http.StatusOK {
			t.Fatalf("sandbox %s status = %d: %s", tc.name, c, b)
		}
		assertNoSecretMarkers(t, "sandbox "+tc.name, b, markers...)
		assertJSONKeysAtMost(t, "sandbox "+tc.name, b, tc.keys...)
	}

	code, body = doSandboxAPI("sandbox-writer", "tok-writer", http.MethodPost, "/api/tasks/reorder", fmt.Sprintf(`{"ids":[%q]}`, sibling.ID))
	if code != http.StatusOK {
		t.Fatalf("sandbox reorder status = %d: %s", code, body)
	}
	assertNoSecretMarkers(t, "sandbox reorder", body, markers...)

	// A sandbox holding both api:tasks:read@acme/widgets and api:tasks:write@acme/widgets still gets the task record.
	code, body = doSandboxAPI("sandbox-rw", "tok-rw", http.MethodPatch, "/api/tasks/"+sibling.ID, `{"base":"main"}`)
	if code != http.StatusOK {
		t.Fatalf("sandbox-rw PATCH status = %d: %s", code, body)
	}
	if !strings.Contains(body, siblingPrompt) || !strings.Contains(body, siblingTitle) {
		t.Errorf("sandbox-rw with tasks:read did not receive full task record: %s", body)
	}
}
