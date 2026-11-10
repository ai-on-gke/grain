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
