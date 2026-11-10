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
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/capability/servers"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

type stubCapServerStatuses struct {
	statuses []servers.Status
}

func (s stubCapServerStatuses) Statuses() []servers.Status { return s.statuses }

func TestTasksListPagingSummaryFullValidationAndSearch(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	ctx := context.Background()
	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	seedAPITask(t, client, "caller", "acme/widgets", "api:tasks:read")

	now := time.Now().UTC()
	longBody := strings.Repeat("Detailed investigation notes and logs. ", 30)
	for i := range 60 {
		at := now.Add(time.Duration(i) * time.Second)
		id := fmt.Sprintf("%d", 100+i)
		if err := client.Store.PutTask(ctx, model.Task{
			ID:          id,
			Intent:      model.IntentImplement,
			Title:       fmt.Sprintf("Backlog item %02d", i),
			Body:        fmt.Sprintf("Unique body %02d %s", i, longBody),
			Target:      &widgets,
			Branch:      fmt.Sprintf("grain/feat-branch-%02d", i),
			ExternalRef: fmt.Sprintf("PROJ-%02d", i),
			CreatedAt:   &at,
			Grants: []model.Grant{
				{Capability: "api:tasks:read"},
				{Capability: "api:ci:read"},
				{Capability: "api:repos:read"},
			},
		}); err != nil {
			t.Fatal(err)
		}
	}

	// 1. Default agent call (fields unset): 10 rows, under 10 KB, truncated with nextCursor.
	code, body := asAgent(t, srv, "caller", http.MethodGet, "/api/tasks", "", "")
	if code != http.StatusOK {
		t.Fatalf("GET /api/tasks: %d %s", code, body)
	}
	if len(body) > mcpToolResultCap {
		t.Errorf("GET /api/tasks default page = %d bytes, want <= %d", len(body), mcpToolResultCap)
	}
	var defPage struct {
		Tasks      []ui.Task `json:"tasks"`
		Total      int       `json:"total"`
		Truncated  bool      `json:"truncated"`
		NextCursor string    `json:"nextCursor"`
	}
	if err := json.Unmarshal([]byte(body), &defPage); err != nil {
		t.Fatal(err)
	}
	if len(defPage.Tasks) != 10 || defPage.Total != 61 || !defPage.Truncated || defPage.NextCursor == "" {
		t.Errorf("GET /api/tasks page 1 = %d items, total %d, truncated %v, cursor %q",
			len(defPage.Tasks), defPage.Total, defPage.Truncated, defPage.NextCursor)
	}

	// 2. fields=summary for an agent: paged 50 rows, compact summary objects (no descriptions), under 10 KB.
	code, sumBody := asAgent(t, srv, "caller", http.MethodGet, "/api/tasks?fields=summary", "", "")
	if code != http.StatusOK {
		t.Fatalf("GET /api/tasks?fields=summary: %d %s", code, sumBody)
	}
	if len(sumBody) > mcpToolResultCap {
		t.Errorf("GET /api/tasks?fields=summary = %d bytes, want <= %d", len(sumBody), mcpToolResultCap)
	}
	if strings.Contains(sumBody, "Unique body") {
		t.Errorf("fields=summary leaked task description")
	}
	var sumPage1, sumPage2 struct {
		Tasks      []map[string]any `json:"tasks"`
		Total      int              `json:"total"`
		Truncated  bool             `json:"truncated"`
		NextCursor string           `json:"nextCursor"`
	}
	if err := json.Unmarshal([]byte(sumBody), &sumPage1); err != nil {
		t.Fatalf("fields=summary was not a paged object: %v (%s)", err, sumBody)
	}
	if len(sumPage1.Tasks) != 50 || sumPage1.Total != 61 || !sumPage1.Truncated || sumPage1.NextCursor == "" {
		t.Errorf("fields=summary page 1 = %d of %d, truncated %v, cursor %q",
			len(sumPage1.Tasks), sumPage1.Total, sumPage1.Truncated, sumPage1.NextCursor)
	}
	_, sumBody2 := asAgent(t, srv, "caller", http.MethodGet, "/api/tasks?fields=summary&cursor="+url.QueryEscape(sumPage1.NextCursor), "", "")
	if err := json.Unmarshal([]byte(sumBody2), &sumPage2); err != nil {
		t.Fatal(err)
	}
	if len(sumPage2.Tasks) != 11 || sumPage2.Truncated || sumPage2.NextCursor != "" {
		t.Errorf("fields=summary page 2 = %d, truncated %v, cursor %q", len(sumPage2.Tasks), sumPage2.Truncated, sumPage2.NextCursor)
	}

	// 3. fields=full for an agent: paged 5 rows with descriptions preserved, under 10 KB.
	code, fullBody := asAgent(t, srv, "caller", http.MethodGet, "/api/tasks?fields=full", "", "")
	if code != http.StatusOK {
		t.Fatalf("GET /api/tasks?fields=full: %d %s", code, fullBody)
	}
	if len(fullBody) > mcpToolResultCap {
		t.Errorf("GET /api/tasks?fields=full = %d bytes, want <= %d", len(fullBody), mcpToolResultCap)
	}
	var fullPage struct {
		Tasks      []ui.Task `json:"tasks"`
		Total      int       `json:"total"`
		Truncated  bool      `json:"truncated"`
		NextCursor string    `json:"nextCursor"`
	}
	if err := json.Unmarshal([]byte(fullBody), &fullPage); err != nil {
		t.Fatalf("fields=full was not a paged object: %v", err)
	}
	if len(fullPage.Tasks) != 5 || !fullPage.Truncated || fullPage.Tasks[0].Description == "" {
		t.Errorf("fields=full page 1 = %d items, truncated %v, first desc len %d",
			len(fullPage.Tasks), fullPage.Truncated, len(fullPage.Tasks[0].Description))
	}

	// 4. q matches branch, externalRef, and case-insensitive id.
	for _, needle := range []string{"FEAT-BRANCH-07", "proj-07", "#CALLER"} {
		c, b := asAgent(t, srv, "caller", http.MethodGet, "/api/tasks?q="+url.QueryEscape(needle), "", "")
		if c != http.StatusOK {
			t.Fatalf("GET /api/tasks?q=%s: %d %s", needle, c, b)
		}
		var qp struct {
			Total int `json:"total"`
		}
		if err := json.Unmarshal([]byte(b), &qp); err != nil || qp.Total != 1 {
			t.Errorf("GET /api/tasks?q=%s matched %d tasks, want 1 (%s)", needle, qp.Total, b)
		}
	}

	// 5. Invalid limit and forged/other-route cursors return 400 on /api/tasks and /api/tasks/states.
	for _, path := range []string{
		"/api/tasks?limit=-1",
		"/api/tasks?limit=abc",
		"/api/tasks?cursor=forged",
		"/api/tasks?cursor=" + ui.ListCursorFor(20),
		"/api/tasks?fields=summary&cursor=forged",
		"/api/tasks/states?limit=-1",
		"/api/tasks/states?limit=abc",
		"/api/tasks/states?cursor=forged",
		"/api/tasks/states?cursor=" + ui.ListCursorFor(20),
		"/api/states?cursor=forged",
	} {
		c, b := asAgent(t, srv, "caller", http.MethodGet, path, "", "")
		if c != http.StatusBadRequest {
			t.Errorf("GET %s = %d (%s), want 400", path, c, b)
		}
	}
}

func TestAllHeavyPagedRoutesStayUnderToolResultCap(t *testing.T) {
	t.Parallel()
	srv, client, creds := testServerWithCreds(t)
	ctx := context.Background()
	humanCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "owner@example.com"})

	// Seed GitHub credentials + 120 connections on "bot".
	if err := creds.SetToken("bot", "tok-bot"); err != nil {
		t.Fatal(err)
	}
	var gitConns []model.TeamGitConnection
	for i := range 60 {
		if err := creds.SetToken(fmt.Sprintf("gh-cred-%03d", i), "tok"); err != nil {
			t.Fatal(err)
		}
		gitConns = append(gitConns, model.TeamGitConnection{
			Credential: "bot",
			Scope:      fmt.Sprintf("acme/service-%03d-component", i),
		})
	}

	// Seed capability servers (with URLs and heavy method schemas) + console sessions.
	capServers := map[string]protocol.ServerConfig{}
	var statuses []servers.Status
	enumVals := make([]string, 60)
	for i := range 60 {
		enumVals[i] = fmt.Sprintf("gh-cred-%03d-long-identifier", i)
	}
	bigSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"credential": map[string]any{"type": "string", "enum": enumVals},
		},
	}
	for i := range 15 {
		id := fmt.Sprintf("https://mcp-%02d.example.com/cap", i)
		capServers[id] = protocol.ServerConfig{ServerURL: fmt.Sprintf("https://mcp-%02d.example.com/rpc", i)}
		statuses = append(statuses, servers.Status{
			ID:           id,
			State:        servers.Attached,
			Transport:    "http",
			Methods:      []string{"query", "mutate"},
			ConfigSchema: bigSchema,
			MethodDetails: []servers.MethodDetail{
				{Name: "query", Description: "Query backend", Args: bigSchema},
				{Name: "mutate", Description: "Mutate backend", Args: bigSchema},
			},
		})
	}
	client.Config.CapabilityServers = stubCapServerStatuses{statuses: statuses}

	sessions := model.DefaultConsoleSessions()
	for i := range 8 {
		sessions = append(sessions, model.ConsoleSession{
			ID:           fmt.Sprintf("standing-%02d", i),
			Title:        fmt.Sprintf("Standing Triage %02d", i),
			Capabilities: []string{"api:tasks:read"},
		})
	}
	if err := client.Store.UpdateConfig(ctx, func(cfg *model.Config) error {
		cfg.GitCredential = "bot"
		cfg.GitConnections = gitConns
		cfg.MCPServers = capServers
		cfg.ConsoleSessions = sessions
		cfg.ExternalAgentAPI = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Seed 80 open tasks holding capability server & scope set grants (testing usage endpoints).
	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	for i := range 80 {
		if err := client.Store.PutTask(ctx, model.Task{
			ID:     fmt.Sprintf("%d", 500+i),
			Intent: model.IntentImplement,
			Title:  fmt.Sprintf("Holder task %03d", i),
			Target: &widgets,
			Grants: []model.Grant{
				{Capability: "https://mcp-00.example.com/cap#query"},
				{Capability: "api:grain/ci-read"},
			},
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Seed inline schedules with 4 KB prompt descriptions.
	bigPrompt := strings.Repeat("Check every subsystem invariant carefully before filing follow-up work. ", 55)
	for i := range 15 {
		if err := client.Store.PutSchedule(ctx, model.Schedule{
			ID:         fmt.Sprintf("sched-%02d", i),
			Title:      fmt.Sprintf("Recurring Audit %02d", i),
			Body:       bigPrompt,
			Target:     widgets,
			Enabled:    false,
			Recurrence: model.Recurrence{Kind: model.RecurrenceEveryNHours, EveryNHours: 24},
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Seed 60 external agent tokens, GCP keys, templates, release notes, and memory documents.
	for i := range 60 {
		if _, err := client.CreateExternalAgentToken(humanCtx, ui.CreateExternalAgentTokenRequest{
			Name:     fmt.Sprintf("ext-tok-%02d", i),
			ScopeSet: "grain/external-dev",
			Repos:    []string{"acme/widgets", "acme/alpha", "acme/beta"},
		}); err != nil {
			t.Fatal(err)
		}
		if err := client.Store.PutGCPKey(ctx, model.GCPKey{
			Name:                fmt.Sprintf("gcp-sa-key-%02d", i),
			ProjectID:           fmt.Sprintf("cloud-project-prod-%02d", i),
			ServiceAccountEmail: fmt.Sprintf("worker-%02d@cloud-project-prod-%02d.iam.gserviceaccount.com", i, i),
			MinterCredential:    "corp-minter",
			Description:         fmt.Sprintf("Production deployment key %02d", i),
			CreatedAt:           time.Now().UTC(),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := client.CreateTemplate(humanCtx, ui.CreateTemplateRequest{
			Name:        fmt.Sprintf("tmpl-%02d", i),
			Title:       fmt.Sprintf("Automated Regression Sweep Template %02d", i),
			Repo:        "acme/widgets",
			Description: bigPrompt,
		}); err != nil {
			t.Fatal(err)
		}
		year := 2024 + (i / 50)
		week := (i % 50) + 1
		if _, err := client.PutReleaseNote(ctx, fmt.Sprintf("%04d-W%02d", year, week),
			fmt.Sprintf("Weekly Release Summary %02d for Platform Reliability", i), bigPrompt,
			model.Principal{Kind: model.PrincipalHuman, ID: "release-lead@example.com"}); err != nil {
			t.Fatal(err)
		}
		if _, err := client.PutMemoryDocument(ctx, fmt.Sprintf("acme/repo-%02d", i/10), fmt.Sprintf("doc-%02d", i),
			fmt.Sprintf("Summary of architecture and operational runbook notes for component %02d", i), "body",
			model.Principal{Kind: model.PrincipalHuman, ID: "owner@example.com"}); err != nil {
			t.Fatal(err)
		}
	}

	// Seed 60 failing CI runs for view=compact + RequestedBy q test.
	now := time.Now().UTC()
	var runs []ci.Run
	for i := range 60 {
		st := now.Add(-time.Duration(i) * time.Minute)
		runs = append(runs, ci.Run{
			ID:          fmt.Sprintf("ci-run-%03d", i),
			Repo:        "acme/widgets",
			Ref:         fmt.Sprintf("grain/pr-branch-%03d", i),
			Commit:      fmt.Sprintf("%040x", i+1),
			RequestedBy: fmt.Sprintf("%d", 500+i),
			StartedAt:   &st,
			Conclusion:  ci.ConclusionFailure,
			Jobs: []ci.JobRun{
				{JobID: "unit", Name: "unit", Conclusion: ci.ConclusionFailure, Error: "step unit exited 1"},
			},
		})
	}
	client.Config.CI = &fakeCIRunner{runs: runs}
	enableCI(t, client.Store, widgets)

	// Seed 15 access history entries for ?limit=5 human check.
	for i := range 15 {
		if _, err := client.AllowPrincipal(humanCtx, ui.AllowPrincipalRequest{
			Entry:  fmt.Sprintf("user:audit-%02d@example.com", i),
			Role:   "grain/viewer",
			Reason: "audit history check",
		}); err != nil {
			t.Fatal(err)
		}
	}

	seedAPITask(t, client, "agent-all", "acme/widgets",
		"api:capabilities:read", "api:schedules:read", "api:sessions:read",
		"api:access:read", "api:secrets:read", "api:templates:read",
		"api:release-notes:read", "api:memory:read", "api:ci:read")

	for _, path := range []string{
		"/api/capability-servers",
		"/api/capability-servers/usage",
		"/api/api-scope-sets/usage",
		"/api/schedules",
		"/api/console-sessions",
		"/api/ci/runs?view=compact",
		"/api/external-agents/tokens",
		"/api/gcp-keys",
		"/api/github-tokens",
		"/api/templates?fields=summary",
		"/api/release-notes",
		"/api/memory/documents",
	} {
		code, body := asAgent(t, srv, "agent-all", http.MethodGet, path, "", "")
		if code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, code, body)
		}
		if len(body) > mcpToolResultCap {
			t.Errorf("agent default page for GET %s is %d bytes, want <= %d: %.900s", path, len(body), mcpToolResultCap, body)
		}
	}

	// capability-servers q matches ServerURL case-insensitively.
	code, capBody := asAgent(t, srv, "agent-all", http.MethodGet, "/api/capability-servers?q=MCP-07.EXAMPLE.COM", "", "")
	if code != http.StatusOK || !strings.Contains(capBody, "https://mcp-07.example.com/cap") {
		t.Errorf("capability-servers URL q failed: %d %s", code, capBody)
	}

	// ci/runs q matches RequestedBy (task id).
	code, ciBody := asAgent(t, srv, "agent-all", http.MethodGet, "/api/ci/runs?view=compact&q=542", "", "")
	if code != http.StatusOK || !strings.Contains(ciBody, `"requestedBy":"542"`) {
		t.Errorf("ci/runs task id q failed: %d %s", code, ciBody)
	}

	// schedules?fields=full preserves prompt description when explicitly requested.
	code, schedFull := asAgent(t, srv, "agent-all", http.MethodGet, "/api/schedules?limit=1&fields=full", "", "")
	if code != http.StatusOK || !strings.Contains(schedFull, "Check every subsystem invariant") {
		t.Errorf("schedules?fields=full omitted description: %d %s", code, schedFull)
	}

	// Human GET /api/access/history?limit=5 honours limit=5 and stays a bare JSON array.
	histRec := do(t, srv, http.MethodGet, "/api/access/history?limit=5", "")
	if histRec.Code != http.StatusOK {
		t.Fatalf("GET /api/access/history?limit=5: %d", histRec.Code)
	}
	var histRows []ui.ConfigAuditView
	if err := json.Unmarshal(histRec.Body.Bytes(), &histRows); err != nil || len(histRows) != 5 {
		t.Errorf("human GET /api/access/history?limit=5 = %d rows (%v), want 5", len(histRows), err)
	}
}

func TestRepoLimitedCredentialsNeverLeakReachableReposOrTasksCounts(t *testing.T) {
	t.Parallel()
	srv, client := bigReachClient(t)
	ctx := context.Background()
	humanCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "owner@example.com"})
	if err := client.Store.UpdateConfig(ctx, func(cfg *model.Config) error {
		cfg.ExternalAgentAPI = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	widgets := model.RepoRef{Owner: "acme", Name: "service-000-with-a-longish-name"}
	other := model.RepoRef{Owner: "gke-labs", Name: "service-001-with-a-longish-name"}
	for i := range 12 {
		target := widgets
		title := fmt.Sprintf("Widgets task %02d", i)
		if i >= 7 {
			target = other
			title = fmt.Sprintf("Other secret task %02d", i)
		}
		if err := client.Store.PutTask(ctx, model.Task{
			ID:     fmt.Sprintf("%d", 200+i),
			Intent: model.IntentImplement,
			Title:  title,
			Body:   "Body for " + title,
			Target: &target,
		}); err != nil {
			t.Fatal(err)
		}
	}

	allowedRepo := widgets.String()
	seedAPITask(t, client, "scoped-task", allowedRepo,
		"api:repos:read@"+allowedRepo, "api:tasks:read@"+allowedRepo)

	tokRepos, err := client.CreateExternalAgentToken(humanCtx, ui.CreateExternalAgentTokenRequest{
		Name:     "by-repos",
		ScopeSet: "grain/external-dev",
		Repos:    []string{allowedRepo},
	})
	if err != nil {
		t.Fatal(err)
	}
	tokQualifier, err := client.CreateExternalAgentToken(humanCtx, ui.CreateExternalAgentTokenRequest{
		Name:   "by-qualifier",
		Scopes: []string{"repos:read@" + allowedRepo, "tasks:read@" + allowedRepo},
	})
	if err != nil {
		t.Fatal(err)
	}

	callWith := func(hdrs map[string]string, path string) (int, string) {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		for k, v := range hdrs {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}

	callers := []struct {
		name string
		hdrs map[string]string
	}{
		{"task-token", map[string]string{ui.ActingTaskHeader: "scoped-task"}},
		{"ext-repos", map[string]string{"Authorization": "Bearer " + tokRepos.Token}},
		{"ext-qualifier", map[string]string{"Authorization": "Bearer " + tokQualifier.Token}},
	}

	for _, caller := range callers {
		t.Run(caller.name, func(t *testing.T) {
			// 1. reachable-repos returns only allowedRepo, Total=1, and no connection counts.
			code, body := callWith(caller.hdrs, "/api/deployment/reachable-repos")
			if code != http.StatusOK {
				t.Fatalf("reachable-repos: %d %s", code, body)
			}
			var rr ui.ReachableReposResult
			if err := json.Unmarshal([]byte(body), &rr); err != nil {
				t.Fatal(err)
			}
			if rr.Total != 1 || len(rr.Repos) != 1 || rr.Repos[0].Repo != allowedRepo || len(rr.Connections) != 0 {
				t.Errorf("reachable-repos = %+v; want Total=1 with only %s and empty Connections", rr, allowedRepo)
			}
			if strings.Contains(body, "gke-labs") || strings.Contains(body, "301") {
				t.Errorf("reachable-repos leaked other repo or count: %s", body)
			}

			// 2. /api/tasks, fields=summary, and fields=full never see gke-labs or its counts.
			// Seven seeded tasks plus scoped-task itself, which is filed on
			// allowedRepo too and so is in every caller's view.
			wantTotal := 8
			for _, path := range []string{"/api/tasks?limit=3", "/api/tasks?fields=summary&limit=3", "/api/tasks?fields=full&limit=3"} {
				cur := ""
				seen := 0
				for pages := 0; ; pages++ {
					if pages > 20 {
						t.Fatalf("walking %s never reached the last page (cursor %q)", path, cur)
					}
					u := path
					if cur != "" {
						u += "&cursor=" + url.QueryEscape(cur)
					}
					c, b := callWith(caller.hdrs, u)
					if c != http.StatusOK {
						t.Fatalf("GET %s: %d %s", u, c, b)
					}
					if strings.Contains(b, "Other secret task") || strings.Contains(b, "gke-labs") {
						t.Fatalf("GET %s leaked other repo: %s", u, b)
					}
					var page struct {
						Tasks      []json.RawMessage `json:"tasks"`
						Total      int               `json:"total"`
						Truncated  bool              `json:"truncated"`
						NextCursor string            `json:"nextCursor"`
					}
					if err := json.Unmarshal([]byte(b), &page); err != nil {
						t.Fatalf("GET %s invalid page JSON: %v (%s)", u, err, b)
					}
					if page.Total != wantTotal {
						t.Errorf("GET %s total = %d, want %d", u, page.Total, wantTotal)
					}
					seen += len(page.Tasks)
					if !page.Truncated {
						break
					}
					if page.NextCursor == "" || page.NextCursor == cur {
						t.Fatalf("GET %s: truncated page did not advance the cursor (%q)", u, page.NextCursor)
					}
					cur = page.NextCursor
				}
				if seen != wantTotal {
					t.Errorf("walked %s: saw %d tasks, want %d", path, seen, wantTotal)
				}
			}
		})
	}

	// Verify HTTPClient.ListTasks with an external repo-scoped token walks pages cleanly.
	ts := httptest.NewServer(srv)
	defer ts.Close()
	cli := ui.NewHTTPClient(ts.URL)
	cli.Token = tokQualifier.Token
	listed, err := cli.ListTasks(ctx)
	if err != nil {
		t.Fatalf("HTTPClient.ListTasks with external token: %v", err)
	}
	if len(listed) != 8 {
		t.Errorf("HTTPClient.ListTasks returned %d tasks, want 8", len(listed))
	}
	for _, tk := range listed {
		if tk.Repo != allowedRepo || (tk.Description == "" && tk.ID != "scoped-task") {
			t.Errorf("unexpected task from HTTPClient.ListTasks: repo=%q desc=%q", tk.Repo, tk.Description)
		}
	}
}

// docs/test-runs/2026-11-10-agent-api-paging.md: against a seeded daemon an
// agent's default page of GET /api/deployment/git-connections was 28 KB (each
// row's status repeated GitHub's ~450-byte known_hosts) and GET
// /api/capability-servers was 12 KB (the api capability lists one method per
// scope set). Both must fit the tool-result cap while a person's unpaged
// read keeps every field.
func TestGitConnectionsAndCapabilityServersAgentPagesFitToolResultCap(t *testing.T) {
	t.Parallel()
	srv, client, creds := testServerWithCreds(t)
	ctx := context.Background()
	if err := creds.SetToken("bot", "tok-bot"); err != nil {
		t.Fatal(err)
	}
	var gitConns []model.TeamGitConnection
	for i := range 80 {
		gitConns = append(gitConns, model.TeamGitConnection{
			Credential: "bot",
			Scope:      fmt.Sprintf("acme/service-%03d-component", i),
		})
	}
	methods := make([]string, 300)
	for i := range methods {
		methods[i] = fmt.Sprintf("ops/scope-set-%03d", i)
	}
	id := "https://mcp-big.example.com/cap"
	client.Config.CapabilityServers = stubCapServerStatuses{statuses: []servers.Status{{
		ID: id, State: servers.Attached, Transport: "http", Methods: methods,
	}}}
	if err := client.Store.UpdateConfig(ctx, func(cfg *model.Config) error {
		cfg.GitCredential = "bot"
		cfg.GitConnections = gitConns
		cfg.MCPServers = map[string]protocol.ServerConfig{id: {ServerURL: "https://mcp-big.example.com/rpc"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	seedAPITask(t, client, "agent-conns", "acme/widgets", "api:connections:read", "api:capabilities:read", "api:deployment:read")

	code, body := asAgent(t, srv, "agent-conns", http.MethodGet, "/api/deployment/git-connections", "", "")
	if code != http.StatusOK {
		t.Fatalf("agent GET git-connections: %d %s", code, body)
	}
	if len(body) > mcpToolResultCap {
		t.Errorf("agent default git-connections page is %d bytes, want <= %d: %.600s", len(body), mcpToolResultCap, body)
	}
	if strings.Contains(body, "ssh-ed25519") {
		t.Errorf("agent git-connections page repeats known_hosts on every row")
	}
	human := do(t, srv, http.MethodGet, "/api/deployment/git-connections", "")
	if human.Code != http.StatusOK || !strings.Contains(human.Body.String(), "ssh-ed25519") {
		t.Errorf("person's unpaged git-connections lost defaultKnownHosts: %d", human.Code)
	}

	code, body = asAgent(t, srv, "agent-conns", http.MethodGet, "/api/capability-servers?q=mcp-big", "", "")
	if code != http.StatusOK {
		t.Fatalf("agent GET capability-servers: %d %s", code, body)
	}
	if len(body) > mcpToolResultCap {
		t.Errorf("agent capability-servers page is %d bytes, want <= %d", len(body), mcpToolResultCap)
	}
	var page struct {
		Items []ui.CapabilityServer `json:"items"`
	}
	if err := json.Unmarshal([]byte(body), &page); err != nil || len(page.Items) != 1 {
		t.Fatalf("capability-servers page = %v %s", err, body)
	}
	row := page.Items[0]
	if row.Status == nil || len(row.Status.Methods) != 40 || row.MethodCount != 300 || !row.MethodsTruncated {
		t.Errorf("paged row methods = %d (count %d, truncated %v), want 40 of 300 truncated", len(row.Status.Methods), row.MethodCount, row.MethodsTruncated)
	}
	human = do(t, srv, http.MethodGet, "/api/capability-servers", "")
	var all []ui.CapabilityServer
	if err := json.Unmarshal(human.Body.Bytes(), &all); err != nil {
		t.Fatalf("human capability-servers: %v", err)
	}
	for _, s := range all {
		if s.ID == id && (s.Status == nil || len(s.Status.Methods) != 300 || s.MethodsTruncated) {
			t.Errorf("person's unpaged capability-servers row lost methods")
		}
	}
}

// docs/test-runs/2026-11-10-agent-api-paging.md: a task with no filing or
// closing time sorts at time.Time{}, whose UnixNano is outside int64's
// range, so its nextCursor read back as 1754 and the next page began at
// the same task again: an agent (or HTTPClient.ListTasks) walking the list
// never reached its end.
func TestTaskListCursorAdvancesPastTasksWithNoTime(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	ctx := context.Background()
	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	seedAPITask(t, client, "walker", "acme/widgets", "api:tasks:read")
	for i := range 7 {
		if err := client.Store.PutTask(ctx, model.Task{
			ID: fmt.Sprintf("%d", 300+i), Intent: model.IntentImplement,
			Title: fmt.Sprintf("Timeless task %02d", i), Target: &widgets,
		}); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	cur := ""
	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatalf("walk never ended; cursor %q", cur)
		}
		path := "/api/tasks?limit=2"
		if cur != "" {
			path += "&cursor=" + url.QueryEscape(cur)
		}
		code, body := asAgent(t, srv, "walker", http.MethodGet, path, "", "")
		if code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, code, body)
		}
		var page struct {
			Tasks []struct {
				ID string `json:"id"`
			} `json:"tasks"`
			Truncated  bool   `json:"truncated"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal([]byte(body), &page); err != nil {
			t.Fatal(err)
		}
		for _, tk := range page.Tasks {
			if seen[tk.ID] {
				t.Fatalf("task %s on two pages (cursor %q)", tk.ID, cur)
			}
			seen[tk.ID] = true
		}
		if !page.Truncated {
			break
		}
		cur = page.NextCursor
	}
	if len(seen) != 8 {
		t.Errorf("walked %d tasks, want 8", len(seen))
	}
}

// docs/test-runs/2026-11-10-agent-api-paging.md: a repo-limited agent paging
// GET /api/events hung for good (and spun a CPU) on the page after its last
// readable event when a full scan batch (100) of other repositories' events
// followed it: the scan re-read the same unreadable batch forever.
func TestEventsPageAfterLastReadableEventReturnsForRepoLimitedAgent(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	ctx := context.Background()
	seedAPITask(t, client, "events-walker", "acme/widgets", "api:tasks:read@acme/widgets")
	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	other := model.RepoRef{Owner: "acme", Name: "other"}
	now := time.Now().UTC()
	put := func(id string, target model.RepoRef) {
		if err := client.Store.PutTask(ctx, model.Task{
			ID: id, Intent: model.IntentImplement, Title: "Event source " + id,
			Target: &target, CreatedAt: &now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 3 {
		put(fmt.Sprintf("%d", 400+i), widgets)
	}
	for i := range 150 {
		put(fmt.Sprintf("%d", 500+i), other)
	}

	cur := "0"
	walked := 0
	for pages := 0; pages < 50; pages++ {
		path := "/api/events?limit=1&since=" + url.QueryEscape(cur)
		type result struct {
			code int
			body string
		}
		done := make(chan result, 1)
		go func() {
			code, body := asAgent(t, srv, "events-walker", http.MethodGet, path, "", "")
			done <- result{code, body}
		}()
		var res result
		select {
		case res = <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("GET %s did not return after %d events", path, walked)
		}
		if res.code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, res.code, res.body)
		}
		var page struct {
			Events []struct {
				Repo string `json:"repo"`
			} `json:"events"`
			Truncated  bool   `json:"truncated"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal([]byte(res.body), &page); err != nil {
			t.Fatal(err)
		}
		for _, ev := range page.Events {
			if ev.Repo != "" && ev.Repo != "acme/widgets" {
				t.Fatalf("repo-limited agent saw an event for %s", ev.Repo)
			}
		}
		walked += len(page.Events)
		if !page.Truncated || page.NextCursor == "" {
			if walked == 0 {
				t.Fatal("walked no events")
			}
			return
		}
		cur = page.NextCursor
	}
	t.Fatalf("events walk did not end in 50 pages")
}

// docs/test-runs/2026-11-10-agent-api-paging.md: the director reads every
// target repository, so on a deployment of 260 repositories its list row
// alone was 7.8 KB and an agent's default page of GET /api/tasks 14 KB. A
// paged list row caps reads; the task itself still names them all.
func TestTaskListRowCapsReadsForAgents(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	ctx := context.Background()
	seedAPITask(t, client, "reader", "acme/widgets", "api:tasks:read")
	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	var reads []model.RepoRef
	for i := range 30 {
		reads = append(reads, model.RepoRef{Owner: "acme", Name: fmt.Sprintf("watched-repo-%03d-with-a-long-name", i)})
	}
	now := time.Now().UTC()
	if err := client.Store.PutTask(ctx, model.Task{
		ID: "777", Intent: model.IntentImplement, Title: "Director-like reader",
		Target: &widgets, Reads: reads, CreatedAt: &now,
	}); err != nil {
		t.Fatal(err)
	}
	code, body := asAgent(t, srv, "reader", http.MethodGet, "/api/tasks?q=director-like", "", "")
	if code != http.StatusOK {
		t.Fatalf("GET /api/tasks: %d %s", code, body)
	}
	var page struct {
		Tasks []ui.Task `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(body), &page); err != nil || len(page.Tasks) != 1 {
		t.Fatalf("page = %v %s", err, body)
	}
	row := page.Tasks[0]
	if len(row.Reads) != 10 || row.ReadCount != 30 || !row.ReadsTruncated {
		t.Errorf("list row reads = %d (count %d, truncated %v), want 10 of 30 truncated", len(row.Reads), row.ReadCount, row.ReadsTruncated)
	}
	code, body = asAgent(t, srv, "reader", http.MethodGet, "/api/tasks?q=director-like&fields=full", "", "")
	if code != http.StatusOK || strings.Count(body, "watched-repo-") != 30 || strings.Contains(body, "readsTruncated") {
		t.Errorf("fields=full row should carry every read: %d", code)
	}
}

// docs/test-runs/2026-11-10-agent-api-paging.md, F1: one base-branch run
// over a few hundred declared workflows carried every job on its row, so
// an agent's default GET /api/ci/runs page was 220-240 KB, and its
// compact row's queued-job lines alone were 9 KB. A paged row carries
// the first few jobs, the failed ones first, and says how many there are.
func TestCIRunAgentPagesCapJobsOnWideRuns(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	seedAPITask(t, client, "ci-reader", "acme/widgets", "api:ci:read")
	now := time.Now().UTC()
	wide := ci.Run{ID: "wide-run", Repo: "acme/widgets", Ref: "trunk", Commit: fmt.Sprintf("%040x", 7), StartedAt: &now}
	// A run with nothing to run lists every declared workflow in its error.
	var declared strings.Builder
	declared.WriteString("ci: no workflow of acme/widgets answers release of branch trunk. The workflows this commit declares:\n")
	for i := range 400 {
		fmt.Fprintf(&declared, "  .grain/workflows/declared-%03d.yml: push on any branch\n", i)
	}
	nothing := now.Add(-30 * time.Second)
	idle := ci.Run{ID: "idle-run", Repo: "acme/widgets", Ref: "trunk", Commit: fmt.Sprintf("%040x", 8), StartedAt: &nothing,
		Conclusion: ci.ConclusionErrored, Error: declared.String()}
	for i := range 400 {
		job := ci.JobRun{
			Workflow:     fmt.Sprintf("Declared Workflow %03d", i),
			WorkflowPath: fmt.Sprintf(".grain/workflows/declared-%03d.yml", i),
			JobID:        "check",
			Name:         fmt.Sprintf("check-%03d", i),
			Key:          ci.Key(fmt.Sprintf("job:%064x", i)),
		}
		if i == 390 {
			job.Conclusion, job.Error = ci.ConclusionFailure, "step check exited 1"
		}
		wide.Jobs = append(wide.Jobs, job)
	}
	runs := []ci.Run{wide, idle}
	for i := range 25 {
		st := now.Add(-time.Duration(i+1) * time.Minute)
		runs = append(runs, ci.Run{
			ID: fmt.Sprintf("small-run-%03d", i), Repo: "acme/widgets", Ref: fmt.Sprintf("grain/pr-branch-%03d", i),
			Commit: fmt.Sprintf("%040x", i+100), StartedAt: &st, Conclusion: ci.ConclusionSuccess,
			Jobs: []ci.JobRun{{JobID: "unit", Name: "unit", Conclusion: ci.ConclusionSuccess}},
		})
	}
	client.Config.CI = &fakeCIRunner{runs: runs}
	enableCI(t, client.Store, widgets)

	code, body := asAgent(t, srv, "ci-reader", http.MethodGet, "/api/ci/runs", "", "")
	if code != http.StatusOK {
		t.Fatalf("agent GET /api/ci/runs: %d %s", code, body)
	}
	if len(body) > mcpToolResultCap {
		t.Errorf("agent default ci/runs page is %d bytes, want <= %d", len(body), mcpToolResultCap)
	}
	var full struct {
		Runs []ci.Run `json:"runs"`
	}
	if err := json.Unmarshal([]byte(body), &full); err != nil || len(full.Runs) == 0 || full.Runs[0].ID != "wide-run" {
		t.Fatalf("ci/runs page = %v %.300s", err, body)
	}
	row := full.Runs[0]
	if len(row.Jobs) != 5 || row.JobCount != 400 || !row.JobsTruncated {
		t.Errorf("paged wide row jobs = %d (count %d, truncated %v), want 5 of 400 truncated", len(row.Jobs), row.JobCount, row.JobsTruncated)
	}
	if len(row.Jobs) == 0 || row.Jobs[0].Name != "check-390" {
		t.Errorf("paged wide row should keep the failed job first, got %+v", row.Jobs)
	}
	if got := full.Runs[1]; got.ID != "idle-run" || !got.ErrorTruncated || len(got.Error) > 600 || !strings.HasPrefix(got.Error, "ci: no workflow") {
		t.Errorf("paged idle row error = %d bytes (truncated %v), want its first 500", len(got.Error), got.ErrorTruncated)
	}
	if small := full.Runs[2]; small.JobsTruncated || small.JobCount != 0 || len(small.Jobs) != 1 {
		t.Errorf("a one-job row should be left whole: %+v", small)
	}

	code, body = asAgent(t, srv, "ci-reader", http.MethodGet, "/api/ci/runs?view=compact", "", "")
	if code != http.StatusOK {
		t.Fatalf("agent GET /api/ci/runs?view=compact: %d %s", code, body)
	}
	if len(body) > mcpToolResultCap {
		t.Errorf("agent default compact ci/runs page is %d bytes, want <= %d", len(body), mcpToolResultCap)
	}
	var compact ui.CICompactRunsResponse
	if err := json.Unmarshal([]byte(body), &compact); err != nil || len(compact.Runs) == 0 {
		t.Fatalf("compact page = %v %.300s", err, body)
	}
	crow := compact.Runs[0]
	if len(crow.Jobs) != 10 || crow.JobCount != 400 || !crow.JobsTruncated || crow.Jobs[0].Name != "check-390" {
		t.Errorf("paged compact wide row = %d lines (count %d, truncated %v, first %+v), want 10 of 400 with the failure first",
			len(crow.Jobs), crow.JobCount, crow.JobsTruncated, crow.Jobs)
	}

	code, body = asAgent(t, srv, "ci-reader", http.MethodGet, "/api/ci/runs?limit=1&fields=full", "", "")
	if code != http.StatusOK {
		t.Fatalf("agent GET /api/ci/runs?limit=1&fields=full: %d %s", code, body)
	}
	var whole struct {
		Runs []ci.Run `json:"runs"`
	}
	if err := json.Unmarshal([]byte(body), &whole); err != nil || len(whole.Runs) != 1 || len(whole.Runs[0].Jobs) != 400 || whole.Runs[0].JobsTruncated {
		t.Errorf("agent fields=full wide row = %v (%d rows), want 1 row with all 400 jobs", err, len(whole.Runs))
	}

	// CIPage.jsx polls GET /api/ci/runs?limit=50: a person's ?limit= must
	// keep every job, full error and standalone GitHub Actions shared faults
	// (such as when grain CI has no runs matching them).
	faultAt := baseTime.Add(-10 * time.Minute)
	for b := range 2 {
		if err := client.Store.PutGitHubCIJob(context.Background(), model.GitHubCIJob{
			Repo: "acme/widgets", CheckRunID: int64(910000 + b), Source: model.GitHubCISource,
			App: "github-actions", Ref: fmt.Sprintf("grain/standalone-fault-%d", b),
			Commit: fmt.Sprintf("%040x", 0x9100+b), Workflow: "Tests", RunID: int64(920000 + b),
			RunAttempt: 1, JobName: "ui e2e", Conclusion: "failure",
			StartedAt: &faultAt, CompletedAt: &faultAt, FailingStep: "playwright",
			FailureTail: "--- FAIL: TestSharedBrowserFault\n    ui/e2e/fault.spec.js:12: boom\n",
			TailSource:  "log", RecordedAt: faultAt, UpdatedAt: faultAt,
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range []string{"", "?limit=50"} {
		human := do(t, srv, http.MethodGet, "/api/ci/runs"+query, "")
		var all struct {
			Runs   []ci.Run     `json:"runs"`
			Faults []ui.CIFault `json:"faults"`
		}
		if err := json.Unmarshal(human.Body.Bytes(), &all); err != nil || len(all.Runs) == 0 {
			t.Fatalf("human ci/runs%s = %v", query, err)
		}
		if got := all.Runs[0]; len(got.Jobs) != 400 || got.JobsTruncated {
			t.Errorf("person's ci/runs%s wide row = %d jobs (truncated %v), want all 400", query, len(got.Jobs), got.JobsTruncated)
		}
		if got := all.Runs[1]; got.Error != declared.String() || got.ErrorTruncated {
			t.Errorf("person's ci/runs%s idle row lost its error (%d bytes)", query, len(got.Error))
		}
		if len(all.Faults) != 1 || all.Faults[0].Trimmed {
			t.Errorf("person's ci/runs%s faults = %+v, want standalone GitHub CI fault whole", query, all.Faults)
		}
	}
	// And when grain CI is wired with zero runs (as demo -observability
	// starts), CIPage's ?limit=50 poll still receives the standalone fault.
	client.Config.CI = &fakeCIRunner{}
	emptyHuman := do(t, srv, http.MethodGet, "/api/ci/runs?limit=50", "")
	var emptyRuns struct {
		Faults []ui.CIFault `json:"faults"`
	}
	if err := json.Unmarshal(emptyHuman.Body.Bytes(), &emptyRuns); err != nil || len(emptyRuns.Faults) != 1 || emptyRuns.Faults[0].Trimmed {
		t.Errorf("person's ci/runs?limit=50 with no grain CI runs = %+v (%v), want standalone fault", emptyRuns.Faults, err)
	}

	// Security regression: a repo-scoped agent (api:ci:read@acme/widgets)
	// must never see another repository's shared faults via fields=full,
	// same-ref+commit collisions, or when grain CI is disabled.
	seedAPITask(t, client, "scoped-ci-reader", "acme/widgets", "api:ci:read@acme/widgets")
	for b := range 2 {
		ref := fmt.Sprintf("feature/other-%d", b)
		commit := fmt.Sprintf("%040x", 0x9900+b)
		if b == 0 {
			// Collide with wide-run's ref+commit on another repository.
			ref, commit = wide.Ref, wide.Commit
		}
		if err := client.Store.PutGitHubCIJob(context.Background(), model.GitHubCIJob{
			Repo: "acme/other", CheckRunID: int64(930000 + b), Source: model.GitHubCISource,
			App: "github-actions", Ref: ref, Commit: commit,
			Workflow: "Tests", RunID: int64(940000 + b), RunAttempt: 1,
			JobName: "CI / secret", Conclusion: "failure",
			StartedAt: &faultAt, CompletedAt: &faultAt, FailingStep: "go test",
			FailureTail: "--- FAIL: TestOtherSecretFault\n    pkg/secret/key.go:9: nope\n",
			TailSource:  "log", RecordedAt: faultAt, UpdatedAt: faultAt,
		}); err != nil {
			t.Fatal(err)
		}
	}
	client.Config.CI = &fakeCIRunner{runs: runs}
	for _, query := range []string{"", "?fields=full"} {
		code, body = asAgent(t, srv, "scoped-ci-reader", http.MethodGet, "/api/ci/runs"+query, "", "")
		var scoped ui.CIRunsResponse
		if err := json.Unmarshal([]byte(body), &scoped); code != http.StatusOK || err != nil {
			t.Fatalf("scoped agent GET /api/ci/runs%s: %d %v %s", query, code, err, body)
		}
		for _, f := range scoped.Faults {
			if f.Repo != "acme/widgets" {
				t.Errorf("scoped agent GET /api/ci/runs%s leaked fault for %s: %+v", query, f.Repo, f)
			}
		}
	}
	client.Config.CI = nil
	for _, query := range []string{"", "?fields=full"} {
		code, body = asAgent(t, srv, "scoped-ci-reader", http.MethodGet, "/api/ci/runs"+query, "", "")
		var disabled ui.CIRunsResponse
		if err := json.Unmarshal([]byte(body), &disabled); code != http.StatusOK || err != nil {
			t.Fatalf("scoped agent GET /api/ci/runs%s (CI off): %d %v %s", query, code, err, body)
		}
		if len(disabled.Faults) != 1 || disabled.Faults[0].Repo != "acme/widgets" {
			t.Errorf("scoped agent GET /api/ci/runs%s (CI off) faults = %+v, want only acme/widgets", query, disabled.Faults)
		}
	}
}

// docs/test-runs/2026-11-10-agent-api-paging.md, F2, F3 and F6: a
// default agent page of GET /api/ci/faults was 11 KB (each row naming
// every run and branch), of GET /api/base-branch-gates 15.5 KB (each
// row's ~650 B detail), and of GET /api/suite-runs just over 10 KB.
func TestFaultGateAndSuiteRunAgentPagesFitToolResultCap(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	ctx := context.Background()
	seedAPITask(t, client, "derived-reader", "acme/widgets", "api:ci:read", "api:suites:read")

	// 30 shared GitHub Actions faults, each failing on 8 branches and
	// naming 8 files, inside the fault window of the test client's clock.
	at := baseTime.Add(-time.Hour)
	for i := range 30 {
		var tail strings.Builder
		fmt.Fprintf(&tail, "--- FAIL: TestShared_%03d (0.01s)\n", i)
		for f := range 8 {
			fmt.Fprintf(&tail, "    pkg/gh/shared_%03d_%d_test.go:17: want ok\n", i, f)
		}
		tail.WriteString("FAIL\tgithub.com/acme/widgets/pkg/gh\n")
		for b := range 8 {
			jobAt := at.Add(time.Duration(i*8+b) * time.Second)
			if err := client.Store.PutGitHubCIJob(ctx, model.GitHubCIJob{
				Repo: "acme/widgets", CheckRunID: int64(700000 + i*8 + b), Source: model.GitHubCISource,
				App: "github-actions", Ref: fmt.Sprintf("grain/gh-%d-%03d", b, i),
				Commit: fmt.Sprintf("%040x", 0x7000+i*8+b), Workflow: "Tests", RunID: int64(800000 + i*8 + b),
				RunAttempt: 1, JobName: fmt.Sprintf("gh-shared-%03d", i), Conclusion: "failure",
				StartedAt: &jobAt, CompletedAt: &jobAt, FailingStep: "go test", FailureTail: tail.String(),
				TailSource: "log", RecordedAt: jobAt, UpdatedAt: jobAt,
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	// 30 watched repositories on GitHub CI: the "not enabled" gate.
	for i := range 30 {
		if err := client.Store.PutRepoConfig(ctx, model.RepoConfig{
			Repo:   model.RepoRef{Owner: "acme", Name: fmt.Sprintf("watched-repo-%03d", i)},
			CIMode: model.CIModeGitHub, WatchDefaultBranch: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// 30 suite runs, each of a suite of 6 templates.
	var items []model.SuiteItem
	for i := range 6 {
		id := fmt.Sprintf("tmpl-%02d", i)
		if err := client.Store.PutTemplate(ctx, model.Template{
			ID: id, Name: fmt.Sprintf("template-slug-%02d", i), Title: fmt.Sprintf("Template Title %02d", i), Body: "Check it.",
		}); err != nil {
			t.Fatal(err)
		}
		items = append(items, model.SuiteItem{TemplateID: id})
	}
	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	for i := range 30 {
		suite := model.Suite{ID: fmt.Sprintf("suite-%03d", i), Name: fmt.Sprintf("Qualification Suite %03d", i),
			Items: items, Mode: model.SuiteCount, Count: 1}
		if err := client.Store.PutSuite(ctx, suite); err != nil {
			t.Fatal(err)
		}
		if _, err := client.Store.CreateSuiteRun(ctx, suite, widgets, "trunk", at.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}

	for _, path := range []string{"/api/ci/faults", "/api/base-branch-gates", "/api/suite-runs"} {
		code, body := asAgent(t, srv, "derived-reader", http.MethodGet, path, "", "")
		if code != http.StatusOK {
			t.Fatalf("agent GET %s: %d %s", path, code, body)
		}
		if len(body) > mcpToolResultCap {
			t.Errorf("agent default page of GET %s is %d bytes, want <= %d: %.600s", path, len(body), mcpToolResultCap, body)
		}
	}

	code, body := asAgent(t, srv, "derived-reader", http.MethodGet, "/api/ci/faults", "", "")
	var faults ui.CIFaultsResponse
	if err := json.Unmarshal([]byte(body), &faults); code != http.StatusOK || err != nil || len(faults.Faults) != 10 || faults.Total != 30 {
		t.Fatalf("faults page = %d %v (%d rows of %d), want 10 of 30", code, err, len(faults.Faults), faults.Total)
	}
	if f := faults.Faults[0]; len(f.Runs) != 3 || f.RunCount != 8 || len(f.Refs) != 5 || f.RefCount != 8 ||
		len(f.Paths) != 5 || f.PathCount != 8 || !f.Trimmed {
		t.Errorf("paged fault = %d runs (of %d), %d refs (of %d), %d paths (of %d), trimmed %v; want 3/8, 5/8, 5/8",
			len(f.Runs), f.RunCount, len(f.Refs), f.RefCount, len(f.Paths), f.PathCount, f.Trimmed)
	}
	code, body = asAgent(t, srv, "derived-reader", http.MethodGet, "/api/ci/faults?limit=1&fields=full", "", "")
	faults = ui.CIFaultsResponse{}
	if err := json.Unmarshal([]byte(body), &faults); code != http.StatusOK || err != nil || len(faults.Faults) != 1 ||
		len(faults.Faults[0].Runs) != 8 || faults.Faults[0].Trimmed {
		t.Errorf("fields=full fault should name every run: %d %v %.300s", code, err, body)
	}

	code, body = asAgent(t, srv, "derived-reader", http.MethodGet, "/api/base-branch-gates", "", "")
	var gates ui.BaseBranchGates
	if err := json.Unmarshal([]byte(body), &gates); code != http.StatusOK || err != nil || len(gates.Gates) != 20 {
		t.Fatalf("gates page = %d %v (%d rows)", code, err, len(gates.Gates))
	}
	if gates.Gates[0].Detail != "" {
		t.Errorf("paged gate row carries detail %q", gates.Gates[0].Detail)
	}
	code, body = asAgent(t, srv, "derived-reader", http.MethodGet, "/api/base-branch-gates?limit=1&fields=full", "", "")
	gates = ui.BaseBranchGates{}
	if err := json.Unmarshal([]byte(body), &gates); code != http.StatusOK || err != nil || len(gates.Gates) != 1 || gates.Gates[0].Detail == "" {
		t.Errorf("fields=full gate should carry detail: %d %v %.300s", code, err, body)
	}
	human := do(t, srv, http.MethodGet, "/api/base-branch-gates", "")
	var all ui.BaseBranchGates
	if err := json.Unmarshal(human.Body.Bytes(), &all); err != nil || len(all.Gates) != 30 || all.Gates[0].Detail == "" {
		t.Errorf("person's unpaged gates lost detail: %v (%d rows)", err, len(all.Gates))
	}

	code, body = asAgent(t, srv, "derived-reader", http.MethodGet, "/api/suite-runs", "", "")
	var runs struct {
		Items []ui.SuiteRun `json:"items"`
		Total int           `json:"total"`
	}
	if err := json.Unmarshal([]byte(body), &runs); code != http.StatusOK || err != nil || len(runs.Items) != 10 || runs.Total != 30 {
		t.Fatalf("suite-runs page = %d %v (%d rows of %d), want 10 of 30", code, err, len(runs.Items), runs.Total)
	}
	if r := runs.Items[0]; len(r.Tasks) != 3 || r.TaskCount != 6 || !r.TasksTruncated {
		t.Errorf("paged suite run = %d tasks (count %d, truncated %v), want 3 of 6", len(r.Tasks), r.TaskCount, r.TasksTruncated)
	}
	got, err := client.GetSuiteRun(ctx, runs.Items[0].ID)
	if err != nil || len(got.Tasks) != 6 {
		t.Errorf("the suite run itself should keep all 6 tasks: %v (%d)", err, len(got.Tasks))
	}
}

// docs/test-runs/2026-11-10-agent-api-paging.md, F4 and F5: the built-in
// gchat entry's config listed 260 spaces (19.7 KB), so an agent's default
// GET /api/capability-servers page was 24 KB, and gchat#post's
// description and args enumerate the same spaces, so its page of GET
// /api/capability-methods was 30 KB.
func TestCapabilityServerConfigAndMethodRowsAreCappedForAgents(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	ctx := context.Background()
	id := "https://chat-rooms.example.com/cap"
	var spaces []any
	var names []any
	var desc strings.Builder
	desc.WriteString("Post a message to one of the configured spaces. The spaces are:")
	for i := range 260 {
		name := fmt.Sprintf("room-%03d", i)
		spaces = append(spaces, map[string]any{"name": name, "transport": "webhook", "allowed": map[string]any{"per_hour": 30}})
		names = append(names, name)
		fmt.Fprintf(&desc, " %s (webhook, 30 posts an hour, for the team channel number %03d);", name, i)
	}
	args := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"space": map[string]any{"type": "string", "enum": names},
		},
	}
	client.Config.CapabilityServers = stubCapServerStatuses{statuses: []servers.Status{{
		ID: id, State: servers.Attached, Transport: "http", Methods: []string{"post"},
		MethodDetails: []servers.MethodDetail{{Name: "post", Description: desc.String(), Args: args}},
	}}}
	if err := client.Store.UpdateConfig(ctx, func(cfg *model.Config) error {
		cfg.MCPServers = map[string]protocol.ServerConfig{id: {
			ServerURL: "https://chat-rooms.example.com/rpc",
			Config:    map[string]any{"grain_url": "https://grain.example.com", "spaces": spaces},
		}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	seedAPITask(t, client, "cap-reader", "acme/widgets", "api:capabilities:read")

	code, body := asAgent(t, srv, "cap-reader", http.MethodGet, "/api/capability-servers", "", "")
	if code != http.StatusOK {
		t.Fatalf("agent GET capability-servers: %d %s", code, body)
	}
	if len(body) > mcpToolResultCap {
		t.Errorf("agent default capability-servers page is %d bytes, want <= %d", len(body), mcpToolResultCap)
	}
	code, body = asAgent(t, srv, "cap-reader", http.MethodGet, "/api/capability-servers?q=chat-rooms", "", "")
	if code != http.StatusOK {
		t.Fatalf("agent GET capability-servers?q=chat-rooms: %d %s", code, body)
	}
	var page struct {
		Items []ui.CapabilityServer `json:"items"`
	}
	if err := json.Unmarshal([]byte(body), &page); err != nil {
		t.Fatal(err)
	}
	var row *ui.CapabilityServer
	for i := range page.Items {
		if page.Items[i].ID == id {
			row = &page.Items[i]
		}
	}
	if row == nil {
		t.Fatalf("capability-servers page has no %s: %.400s", id, body)
	}
	if got, _ := row.Config.Config["spaces"].([]any); len(got) != 10 || !row.ConfigTruncated || row.ConfigItemCounts["spaces"] != 260 {
		t.Errorf("paged config = %d spaces (truncated %v, counts %v), want 10 of 260", len(got), row.ConfigTruncated, row.ConfigItemCounts)
	}
	if row.Config.Config["grain_url"] != "https://grain.example.com" {
		t.Errorf("paged config lost its scalar fields: %v", row.Config.Config)
	}
	cfg, err := client.Store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := cfg.MCPServers[id].Config["spaces"].([]any); len(got) != 260 {
		t.Errorf("paging cut the stored config: %d spaces", len(got))
	}
	code, body = asAgent(t, srv, "cap-reader", http.MethodGet, "/api/capability-servers?q=chat-rooms&fields=full", "", "")
	page.Items = nil
	if err := json.Unmarshal([]byte(body), &page); code != http.StatusOK || err != nil || len(page.Items) != 1 {
		t.Fatalf("fields=full capability-servers = %d %v %.300s", code, err, body)
	}
	if got, _ := page.Items[0].Config.Config["spaces"].([]any); len(got) != 260 || page.Items[0].ConfigTruncated {
		t.Errorf("fields=full capability server should carry every space: %d (truncated %v)", len(got), page.Items[0].ConfigTruncated)
	}

	code, body = asAgent(t, srv, "cap-reader", http.MethodGet, "/api/capability-methods?q=chat-rooms", "", "")
	if code != http.StatusOK {
		t.Fatalf("agent GET capability-methods: %d %s", code, body)
	}
	if len(body) > mcpToolResultCap {
		t.Errorf("agent capability-methods page is %d bytes, want <= %d", len(body), mcpToolResultCap)
	}
	var methods struct {
		Items []ui.CapabilityMethod `json:"items"`
	}
	if err := json.Unmarshal([]byte(body), &methods); err != nil || len(methods.Items) != 1 {
		t.Fatalf("capability-methods page = %v %.400s", err, body)
	}
	m := methods.Items[0]
	if !m.DescriptionTruncated || len(m.Description) > 600 || !strings.HasPrefix(m.Description, "Post a message") {
		t.Errorf("paged method description = %d bytes (truncated %v)", len(m.Description), m.DescriptionTruncated)
	}
	if !m.ArgsTruncated || m.ArgItemCounts["properties.space.enum"] != 260 {
		t.Errorf("paged method args truncated %v counts %v, want properties.space.enum of 260", m.ArgsTruncated, m.ArgItemCounts)
	}
	code, body = asAgent(t, srv, "cap-reader", http.MethodGet, "/api/capability-methods?q=chat-rooms&fields=full", "", "")
	if code != http.StatusOK || strings.Contains(body, "argsTruncated") || strings.Count(body, "room-259") != 2 {
		t.Errorf("fields=full method should carry its whole description and schema: %d", code)
	}
}

// docs/test-runs/2026-11-10-agent-api-paging.md, F7: GET
// /api/access/history read the newest maxConfigAudit config changes of
// every surface and only then kept the access ones, so once that many
// capability-server edits were newer than the last access change it
// answered nothing -- to people and agents alike.
func TestAccessHistorySurvivesNewerChangesToOtherSurfaces(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	ctx := context.Background()
	humanCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "owner@example.com"})
	for i := range 7 {
		if _, err := client.AllowPrincipal(humanCtx, ui.AllowPrincipalRequest{
			Entry: fmt.Sprintf("user:audit-%02d@example.com", i), Role: "grain/viewer", Reason: "access history check",
		}); err != nil {
			t.Fatal(err)
		}
	}
	later := time.Now().Add(time.Minute)
	for i := range 600 {
		if _, err := client.Store.RecordConfigAudit(ctx, model.ConfigAuditEntry{
			At:      later.Add(time.Duration(i) * time.Millisecond),
			By:      model.Principal{Kind: model.PrincipalHuman, ID: "owner@example.com"},
			Surface: "capability-servers", Summary: fmt.Sprintf("PUT https://tools.example.com/capabilities/cap-%03d", i),
		}); err != nil {
			t.Fatal(err)
		}
	}
	seedAPITask(t, client, "access-reader", "acme/widgets", "api:access:read")

	rec := do(t, srv, http.MethodGet, "/api/access/history", "")
	var rows []ui.ConfigAuditView
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); rec.Code != http.StatusOK || err != nil || len(rows) != 7 {
		t.Errorf("person's GET /api/access/history = %d rows (%d, %v), want all 7 access changes", len(rows), rec.Code, err)
	}
	rec = do(t, srv, http.MethodGet, "/api/access/history?limit=5", "")
	rows = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil || len(rows) != 5 {
		t.Errorf("person's GET /api/access/history?limit=5 = %d rows (%v), want 5", len(rows), err)
	}
	for _, row := range rows {
		if row.Surface != "access" {
			t.Errorf("access history carries a %s change", row.Surface)
		}
	}
	code, body := asAgent(t, srv, "access-reader", http.MethodGet, "/api/access/history", "", "")
	var page struct {
		Items []ui.ConfigAuditView `json:"items"`
		Total int                  `json:"total"`
	}
	if err := json.Unmarshal([]byte(body), &page); code != http.StatusOK || err != nil || len(page.Items) != 7 || page.Total != 7 {
		t.Errorf("agent GET /api/access/history = %d rows of %d (%d, %v), want 7", len(page.Items), page.Total, code, err)
	}
}
