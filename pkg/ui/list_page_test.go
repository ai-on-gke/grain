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
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// The collection reads of the agent API take limit, cursor and q, and
// say when a page was cut short (grain/task-57): an MCP tool result
// carries about this many bytes before the client elides it, and a list
// that grows with the deployment -- what a credential reaches, the memory
// index, a repository's branches -- has to fit a page of it in that.
const mcpToolResultCap = 10 * 1024

// reachableReposPage is GET /api/deployment/reachable-repos' answer as
// these tests read it.
type reachableReposPage struct {
	Available  bool                     `json:"available"`
	Repos      []gitproxy.ReachableRepo `json:"repos"`
	Total      int                      `json:"total"`
	Truncated  bool                     `json:"truncated"`
	NextCursor string                   `json:"nextCursor"`
}

// bigReachClient is reachClient with a credential that reaches a few
// hundred repositories across three owners, a fifth of them read-only,
// and one on another git host -- the deployment that reported a 118 KB
// answer with nothing to narrow it by.
func bigReachClient(t *testing.T) (*ui.Server, *ui.Client) {
	t.Helper()
	srv, client, credentials := testServerWithCreds(t)
	ctx := context.Background()
	if err := credentials.SetToken("bouncer-cred", "tok-bouncer"); err != nil {
		t.Fatal(err)
	}
	sim := githubsim.New("acme", "widgets", "", "main")
	var repos []githubsim.ReachableRepo
	for i := range 300 {
		owner := []string{"acme", "gke-labs", "widgets-org"}[i%3]
		repos = append(repos, githubsim.ReachableRepo{
			FullName: fmt.Sprintf("%s/service-%03d-with-a-longish-name", owner, i),
			Push:     i%5 != 0,
		})
	}
	repos = append(repos, githubsim.ReachableRepo{FullName: "depot.code.forge.test/corp/tools", Push: true})
	sim.Reachable = map[string][]githubsim.ReachableRepo{"tok-bouncer": repos}
	client.Config.GitReach = &gitproxy.Reach{Credentials: credentials, API: sim}
	if err := client.Store.UpdateConfig(ctx, func(tm *model.Config) error {
		tm.GitCredential = "bouncer-cred"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return srv, client
}

func reachableRepos(t *testing.T, srv *ui.Server, as string, query url.Values) (reachableReposPage, int) {
	t.Helper()
	path := "/api/deployment/reachable-repos"
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	var code int
	var body string
	if as == "" {
		rec := do(t, srv, http.MethodGet, path, "")
		code, body = rec.Code, rec.Body.String()
	} else {
		code, body = asAgent(t, srv, as, http.MethodGet, path, "", "")
	}
	if code != http.StatusOK {
		return reachableReposPage{}, code
	}
	var got reachableReposPage
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("reachable-repos answered %q: %v", body, err)
	}
	return got, code
}

// A person's unparameterised read -- the New task and Add repo pickers --
// still gets every repository, and now knows how many that is.
func TestReachableReposGivesAPersonTheWholeList(t *testing.T) {
	t.Parallel()
	srv, _ := bigReachClient(t)
	got, _ := reachableRepos(t, srv, "", nil)
	if !got.Available || len(got.Repos) != 301 || got.Total != 301 || got.Truncated || got.NextCursor != "" {
		t.Errorf("a person's read = %d repos, total %d, truncated %v, cursor %q; want all 301, not truncated",
			len(got.Repos), got.Total, got.Truncated, got.NextCursor)
	}
}

// An agent's unparameterised read is one page, under the tool-result cap,
// and says so: truncated, with the total and the cursor that reads on.
func TestReachableReposDefaultPageForAnAgentFitsTheToolResultCap(t *testing.T) {
	t.Parallel()
	srv, client := bigReachClient(t)
	seedAPITask(t, client, "1", "acme/widgets", "api:repos:read")
	code, body := asAgent(t, srv, "1", http.MethodGet, "/api/deployment/reachable-repos", "", "")
	if code != http.StatusOK {
		t.Fatalf("agent read: %d %s", code, body)
	}
	if len(body) > mcpToolResultCap {
		t.Errorf("an agent's default page is %d bytes; want under the %d-byte tool-result cap", len(body), mcpToolResultCap)
	}
	var got reachableReposPage
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Truncated || got.NextCursor == "" || got.Total != 301 || len(got.Repos) >= 301 {
		t.Errorf("agent's page = %d repos, total %d, truncated %v, cursor %q; want a truncated page of 301 with a cursor",
			len(got.Repos), got.Total, got.Truncated, got.NextCursor)
	}
}

// limit cuts the page and cursor reads on from it, until the last page
// carries no cursor; the pages together are the whole list, in order.
func TestReachableReposPagesRoundTrip(t *testing.T) {
	t.Parallel()
	srv, client := bigReachClient(t)
	seedAPITask(t, client, "1", "acme/widgets", "api:repos:read")
	whole, _ := reachableRepos(t, srv, "", nil)

	var paged []gitproxy.ReachableRepo
	cursor := ""
	for pages := 0; ; pages++ {
		q := url.Values{"limit": {"120"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		got, code := reachableRepos(t, srv, "1", q)
		if code != http.StatusOK {
			t.Fatalf("page %d: %d", pages, code)
		}
		if got.Total != 301 {
			t.Errorf("page %d total = %d, want 301 on every page", pages, got.Total)
		}
		paged = append(paged, got.Repos...)
		if got.Truncated != (got.NextCursor != "") {
			t.Errorf("page %d: truncated %v but cursor %q", pages, got.Truncated, got.NextCursor)
		}
		if !got.Truncated {
			if pages != 2 {
				t.Errorf("301 repos at 120 a page took %d pages, want 3", pages+1)
			}
			break
		}
		if len(got.Repos) != 120 {
			t.Errorf("page %d carries %d repos, want the limit 120", pages, len(got.Repos))
		}
		cursor = got.NextCursor
		if pages > 5 {
			t.Fatal("paging never ends")
		}
	}
	if len(paged) != len(whole.Repos) {
		t.Fatalf("paged %d repos, the whole list has %d", len(paged), len(whole.Repos))
	}
	for i := range paged {
		if paged[i] != whole.Repos[i] {
			t.Fatalf("paged[%d] = %+v, whole[%d] = %+v", i, paged[i], i, whole.Repos[i])
		}
	}

	// A cursor this route did not hand out, and a limit that is not a
	// number, are refused rather than taken for the first page.
	for name, q := range map[string]url.Values{
		"cursor": {"cursor": {"page-2"}},
		"limit":  {"limit": {"many"}},
		"push":   {"push": {"maybe"}},
	} {
		if _, code := reachableRepos(t, srv, "1", q); code != http.StatusBadRequest {
			t.Errorf("a bad %s was answered %d, want 400", name, code)
		}
	}
}

// push, owner, host and q answer "which repo can I file against?" in one
// call, each filter honest about its total.
func TestReachableReposFilters(t *testing.T) {
	t.Parallel()
	srv, client := bigReachClient(t)
	seedAPITask(t, client, "1", "acme/widgets", "api:repos:read")

	for _, tc := range []struct {
		name  string
		query url.Values
		total int
		check func(gitproxy.ReachableRepo) bool
	}{
		{"push", url.Values{"push": {"true"}}, 241, func(r gitproxy.ReachableRepo) bool { return r.Push }},
		{"read-only", url.Values{"push": {"false"}}, 60, func(r gitproxy.ReachableRepo) bool { return !r.Push }},
		{"owner", url.Values{"owner": {"GKE-Labs"}}, 100, func(r gitproxy.ReachableRepo) bool {
			return strings.HasPrefix(r.Repo, "gke-labs/")
		}},
		{"q", url.Values{"q": {"service-00"}}, 10, func(r gitproxy.ReachableRepo) bool {
			return strings.Contains(r.Repo, "service-00")
		}},
		{"other host", url.Values{"host": {"depot.code.forge.test"}}, 1, func(r gitproxy.ReachableRepo) bool {
			return r.Repo == "depot.code.forge.test/corp/tools"
		}},
		{"default host", url.Values{"host": {"github.com"}}, 300, func(r gitproxy.ReachableRepo) bool {
			return !strings.HasPrefix(r.Repo, "depot.")
		}},
		{"owner and push", url.Values{"owner": {"acme"}, "push": {"true"}, "q": {"01"}}, 0, nil},
	} {
		got, code := reachableRepos(t, srv, "1", tc.query)
		if code != http.StatusOK {
			t.Fatalf("%s: %d", tc.name, code)
		}
		if tc.total == 0 {
			// acme owns i%3==0; push is i%5!=0; "01" is in 010..019,
			// 100..109 ... count by hand would be brittle, so only check
			// the filters compose.
			for _, r := range got.Repos {
				if !strings.HasPrefix(r.Repo, "acme/") || !r.Push || !strings.Contains(r.Repo, "01") {
					t.Errorf("%s: %+v escapes the filters", tc.name, r)
				}
			}
			if got.Total == 0 || got.Total != len(got.Repos) {
				t.Errorf("%s: total %d, %d repos", tc.name, got.Total, len(got.Repos))
			}
			continue
		}
		if got.Total != tc.total {
			t.Errorf("%s: total = %d, want %d", tc.name, got.Total, tc.total)
		}
		for _, r := range got.Repos {
			if !tc.check(r) {
				t.Errorf("%s: %+v escapes the filter", tc.name, r)
			}
		}
		if got.Truncated != (tc.total > 100) {
			t.Errorf("%s: truncated %v with %d matching and the default page of 100", tc.name, got.Truncated, tc.total)
		}
	}
}

// The tool's description names the filters, so grain_explore_scope
// surfaces them, and its query schema describes each.
func TestReachableReposToolNamesItsFilters(t *testing.T) {
	t.Parallel()
	var tool ui.APIRoute
	for _, r := range ui.AgentAPIRoutes() {
		if r.Pattern == "GET /api/deployment/reachable-repos" {
			tool = r
		}
	}
	if tool.Tool != "api_deployment_reachable_repos" {
		t.Fatalf("tool = %q", tool.Tool)
	}
	for _, want := range []string{"push=true", "owner", "host", "q", "limit", "cursor", "truncated", "nextCursor"} {
		if !strings.Contains(tool.Description, want) {
			t.Errorf("description does not name %s: %s", want, tool.Description)
		}
	}
}

// Every list route that grows with the deployment pages the same way: a
// person's bare read is the list it always was; an agent's is a page,
// {items, total, truncated, nextCursor}; q, limit and cursor narrow
// either.
func TestListRoutesPageForAgents(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	ctx := context.Background()
	// A scope keeps at most two dozen documents, so the index grows with
	// the repositories: a dozen of them, 20 notes each.
	for i := range 240 {
		repo := fmt.Sprintf("acme/widgets-%02d", i/20)
		if _, err := client.PutMemoryDocument(ctx, repo, fmt.Sprintf("note-%03d", i),
			fmt.Sprintf("summary of note %03d", i), "the text", model.Principal{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := client.PutMemoryDocument(ctx, "", "deployment-note", "the deployment's own", "the text", model.Principal{}); err != nil {
		t.Fatal(err)
	}
	seedAPITask(t, client, "1", "acme/widgets-03", "api:memory:read")

	// A person: the bare list, whole.
	rec := do(t, srv, http.MethodGet, "/api/memory/documents", "")
	var bare []ui.MemoryDocument
	if err := json.Unmarshal(rec.Body.Bytes(), &bare); err != nil || len(bare) != 241 {
		t.Fatalf("a person's read = %d documents (%v): %s", len(bare), err, rec.Body.String())
	}

	type page struct {
		Items      []ui.MemoryDocument `json:"items"`
		Total      int                 `json:"total"`
		Truncated  bool                `json:"truncated"`
		NextCursor string              `json:"nextCursor"`
	}
	read := func(query string) (page, string) {
		t.Helper()
		code, body := asAgent(t, srv, "1", http.MethodGet, "/api/memory/documents"+query, "", "")
		if code != http.StatusOK {
			t.Fatalf("GET /api/memory/documents%s: %d %s", query, code, body)
		}
		var got page
		if err := json.Unmarshal([]byte(body), &got); err != nil {
			t.Fatalf("GET /api/memory/documents%s answered %q: %v", query, body, err)
		}
		return got, body
	}

	// An agent: the default page, under the cap, honest about the rest.
	got, body := read("")
	if len(body) > mcpToolResultCap {
		t.Errorf("an agent's default page is %d bytes; want under %d", len(body), mcpToolResultCap)
	}
	if !got.Truncated || got.NextCursor == "" || got.Total != 241 || len(got.Items) != 50 {
		t.Errorf("agent's page = %d items, total %d, truncated %v, cursor %q", len(got.Items), got.Total, got.Truncated, got.NextCursor)
	}

	// q narrows, limit cuts, cursor reads on, and the last page says so.
	got, _ = read("?q=note-1&limit=60")
	if got.Total != 100 || len(got.Items) != 60 || !got.Truncated {
		t.Errorf("q=note-1&limit=60: %d items of %d, truncated %v", len(got.Items), got.Total, got.Truncated)
	}
	got, _ = read("?q=note-1&limit=60&cursor=" + url.QueryEscape(got.NextCursor))
	if got.Total != 100 || len(got.Items) != 40 || got.Truncated || got.NextCursor != "" {
		t.Errorf("second page: %d items of %d, truncated %v, cursor %q", len(got.Items), got.Total, got.Truncated, got.NextCursor)
	}
	if got.Items[0].Name != "note-160" {
		t.Errorf("second page starts at %s, want note-160", got.Items[0].Name)
	}

	// repo keeps one scope's; empty, the deployment's own.
	if got, _ = read("?repo=acme/widgets-03&limit=0"); got.Total != 20 || len(got.Items) != 20 {
		t.Errorf("repo=acme/widgets-03&limit=0: %d of %d", len(got.Items), got.Total)
	}
	if got, _ = read("?repo="); got.Total != 1 || got.Items[0].Name != "deployment-note" {
		t.Errorf("repo= (the deployment's own): %+v", got)
	}

	// A person who names a parameter gets the page shape too.
	rec = do(t, srv, http.MethodGet, "/api/memory/documents?limit=5", "")
	var person page
	if err := json.Unmarshal(rec.Body.Bytes(), &person); err != nil || len(person.Items) != 5 || !person.Truncated || person.Total != 241 {
		t.Errorf("a person's limit=5 = %+v (%v)", person, err)
	}
	if rec := do(t, srv, http.MethodGet, "/api/memory/documents?limit=-1", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("limit=-1 answered %d, want 400", rec.Code)
	}
}

// ui.ListCursorFor is the cursor a caller paging by hand would build, and
// it is what a page hands back.
func TestListCursorForStartsWhereItSays(t *testing.T) {
	t.Parallel()
	srv, client := bigReachClient(t)
	seedAPITask(t, client, "1", "acme/widgets", "api:repos:read")
	first, _ := reachableRepos(t, srv, "1", url.Values{"limit": {"7"}})
	if first.NextCursor != ui.ListCursorFor(7) {
		t.Errorf("after 7 the cursor is %q, ListCursorFor(7) = %q", first.NextCursor, ui.ListCursorFor(7))
	}
	rest, _ := reachableRepos(t, srv, "1", url.Values{"limit": {"7"}, "cursor": {ui.ListCursorFor(294)}})
	if len(rest.Repos) != 7 || rest.Truncated || rest.NextCursor != "" {
		t.Errorf("from 294 at 7 a page: %d repos, truncated %v, cursor %q; want the last 7", len(rest.Repos), rest.Truncated, rest.NextCursor)
	}
	past, _ := reachableRepos(t, srv, "1", url.Values{"cursor": {ui.ListCursorFor(1000)}})
	if len(past.Repos) != 0 || past.Truncated || past.Total != 301 {
		t.Errorf("a cursor past the end: %d repos, truncated %v, total %d; want an empty last page", len(past.Repos), past.Truncated, past.Total)
	}
}

// A BYO harness calling grain_invoke on POST /api/mcp authenticates with an
// access code rather than X-Grain-Acting-Task, and still needs every list
// route paged under the MCP tool-result cap and its filters surfaced by
// grain_explore_scope.
func TestControllerMCPInvokePagesUnderCap(t *testing.T) {
	t.Parallel()
	srv, client := bigReachClient(t)
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
	headers := map[string]string{ui.AccessCodeHeader: code}

	// grain_explore_scope surfaces the filter and pagination docs.
	explored, isErr := callControllerTool(t, srv, headers, "grain_explore_scope", map[string]any{
		"tool": "api_deployment_reachable_repos",
	})
	if isErr {
		t.Fatalf("grain_explore_scope error: %s", explored)
	}
	for _, want := range []string{"push=true", "owner", "host", "limit", "cursor", "truncated", "nextCursor"} {
		if !strings.Contains(explored, want) {
			t.Errorf("grain_explore_scope(api_deployment_reachable_repos) missing %q:\n%s", want, explored)
		}
	}

	// Unfiltered grain_invoke stays under the 10 KB cap and walks all 301 repos via nextCursor.
	seen := map[string]bool{}
	cursor := ""
	pages := 0
	for {
		args := map[string]any{"tool": "api_deployment_reachable_repos"}
		if cursor != "" {
			args["arguments"] = map[string]any{"query": map[string]any{"cursor": cursor}}
		}
		text, isErr := callControllerTool(t, srv, headers, "grain_invoke", args)
		if isErr {
			t.Fatalf("grain_invoke page %d error: %s", pages+1, text)
		}
		if pages == 0 && len(text) > mcpToolResultCap {
			t.Errorf("first grain_invoke page is %d bytes; want <= %d", len(text), mcpToolResultCap)
		}
		var got reachableReposPage
		if err := json.Unmarshal([]byte(text), &got); err != nil {
			t.Fatalf("unmarshalling grain_invoke page %d: %v (%s)", pages+1, err, text)
		}
		if got.Total != 301 {
			t.Fatalf("page %d total = %d, want 301", pages+1, got.Total)
		}
		for _, r := range got.Repos {
			if seen[r.Repo] {
				t.Errorf("duplicate repo across pages: %s", r.Repo)
			}
			seen[r.Repo] = true
		}
		pages++
		if !got.Truncated {
			if got.NextCursor != "" {
				t.Errorf("final page has nextCursor %q", got.NextCursor)
			}
			break
		}
		if got.NextCursor == "" {
			t.Fatal("truncated page missing nextCursor")
		}
		cursor = got.NextCursor
	}
	if len(seen) != 301 || pages != 4 {
		t.Errorf("paged %d repos across %d pages; want 301 across 4 pages", len(seen), pages)
	}

	// Filtered single-call lookup ("which gke-labs repo can I push to?").
	filteredText, isErr := callControllerTool(t, srv, headers, "grain_invoke", map[string]any{
		"tool": "api_deployment_reachable_repos",
		"arguments": map[string]any{
			"query": map[string]any{"push": "true", "owner": "gke-labs", "q": "service-00"},
		},
	})
	if isErr {
		t.Fatalf("filtered grain_invoke error: %s", filteredText)
	}
	var filtered reachableReposPage
	if err := json.Unmarshal([]byte(filteredText), &filtered); err != nil {
		t.Fatalf("unmarshalling filtered grain_invoke: %v", err)
	}
	if filtered.Truncated || filtered.Total == 0 {
		t.Errorf("filtered = total %d, truncated %v; want small non-empty un-truncated match", filtered.Total, filtered.Truncated)
	}
	for _, r := range filtered.Repos {
		if !r.Push || !strings.HasPrefix(r.Repo, "gke-labs/service-00") {
			t.Errorf("repo %+v escaped push+owner+q filter", r)
		}
	}
}

// Newly paged collection routes support q/domain filters, limit, honest
// truncation + cursor round-trip, and preserve human unparameterised shapes.
func TestNewlyPagedListRoutesFilterLimitAndCursorRoundTrip(t *testing.T) {
	t.Parallel()

	t.Run("templates_memory_boards_messages_and_audit", func(t *testing.T) {
		t.Parallel()
		srv, client := testServer(t)
		ctx := context.Background()
		seedAPITask(t, client, "1", "acme/widgets",
			"api:templates:read", "api:memory:read", "api:boards:read",
			"api:tasks:read", "api:settings:read", "api:capabilities:read")

		// Seed 8 templates across two repos.
		for i := range 8 {
			repo := "acme/widgets"
			if i%2 == 1 {
				repo = "acme/other"
			}
			if _, err := client.CreateTemplate(ctx, ui.CreateTemplateRequest{
				Name:        fmt.Sprintf("tmpl-%02d", i),
				Title:       fmt.Sprintf("Template %02d", i),
				Repo:        repo,
				Description: fmt.Sprintf("Prompt instructions for template %02d", i),
			}); err != nil {
				t.Fatal(err)
			}
		}
		// Human read: bare slice including any built-in default templates + our 8.
		rec := do(t, srv, http.MethodGet, "/api/templates", "")
		var humanTmpls []ui.Template
		if err := json.Unmarshal(rec.Body.Bytes(), &humanTmpls); err != nil || len(humanTmpls) < 8 {
			t.Fatalf("human GET /api/templates = %d (%v)", len(humanTmpls), err)
		}
		wantTotalTmpls := len(humanTmpls)
		// Agent default page: listPageTiny = 5, truncated = true.
		type tmplPage struct {
			Items      []ui.Template `json:"items"`
			Total      int           `json:"total"`
			Truncated  bool          `json:"truncated"`
			NextCursor string        `json:"nextCursor"`
		}
		code, body := asAgent(t, srv, "1", http.MethodGet, "/api/templates", "", "")
		if code != http.StatusOK {
			t.Fatalf("agent GET /api/templates: %d %s", code, body)
		}
		var tp1 tmplPage
		if err := json.Unmarshal([]byte(body), &tp1); err != nil {
			t.Fatal(err)
		}
		if len(tp1.Items) != 5 || tp1.Total != wantTotalTmpls || !tp1.Truncated || tp1.NextCursor == "" {
			t.Errorf("templates page 1 = %+v, want total %d", tp1, wantTotalTmpls)
		}
		_, body2 := asAgent(t, srv, "1", http.MethodGet, "/api/templates?cursor="+url.QueryEscape(tp1.NextCursor), "", "")
		var tp2 tmplPage
		if err := json.Unmarshal([]byte(body2), &tp2); err != nil {
			t.Fatal(err)
		}
		if len(tp2.Items) != wantTotalTmpls-5 || tp2.Truncated || tp2.NextCursor != "" {
			t.Errorf("templates page 2 = %+v, want %d items", tp2, wantTotalTmpls-5)
		}
		// fields=summary omits Description and raises default limit to 50; repo= narrows.
		_, summaryBody := asAgent(t, srv, "1", http.MethodGet, "/api/templates?repo=acme/widgets&fields=summary", "", "")
		var tpSummary tmplPage
		if err := json.Unmarshal([]byte(summaryBody), &tpSummary); err != nil {
			t.Fatal(err)
		}
		if tpSummary.Total != 4 || tpSummary.Truncated {
			t.Errorf("templates repo+summary = %+v", tpSummary)
		}
		for _, item := range tpSummary.Items {
			if item.Repo != "acme/widgets" || item.Description != "" {
				t.Errorf("summary template = %+v; want acme/widgets with empty description", item)
			}
		}

		// Standing memory: 6 repo memories + 1 deployment memory.
		if _, err := client.PutMemory(ctx, "", "global deployment note", model.Principal{}); err != nil {
			t.Fatal(err)
		}
		for i := range 6 {
			if _, err := client.PutMemory(ctx, fmt.Sprintf("acme/repo-%02d", i), fmt.Sprintf("remember rule %02d", i), model.Principal{}); err != nil {
				t.Fatal(err)
			}
		}
		type memPage struct {
			Items      []ui.Memory `json:"items"`
			Total      int         `json:"total"`
			Truncated  bool        `json:"truncated"`
			NextCursor string      `json:"nextCursor"`
		}
		_, mBody := asAgent(t, srv, "1", http.MethodGet, "/api/memory", "", "")
		var mp1 memPage
		if err := json.Unmarshal([]byte(mBody), &mp1); err != nil {
			t.Fatal(err)
		}
		if len(mp1.Items) != 5 || mp1.Total != 7 || !mp1.Truncated || mp1.NextCursor == "" {
			t.Errorf("memory page 1 = %+v", mp1)
		}
		_, mDepBody := asAgent(t, srv, "1", http.MethodGet, "/api/memory?repo=", "", "")
		var mpDep memPage
		if err := json.Unmarshal([]byte(mDepBody), &mpDep); err != nil {
			t.Fatal(err)
		}
		if mpDep.Total != 1 || mpDep.Items[0].Repo != "" {
			t.Errorf("deployment memory filter = %+v", mpDep)
		}

		// Boards: q + limit + cursor round-trip.
		for i := range 6 {
			if _, err := client.CreateBoard(ctx, ui.CreateBoardRequest{
				Name: fmt.Sprintf("Ops Board %02d", i),
			}); err != nil {
				t.Fatal(err)
			}
		}
		type boardPage struct {
			Items      []ui.Board `json:"items"`
			Total      int        `json:"total"`
			Truncated  bool       `json:"truncated"`
			NextCursor string     `json:"nextCursor"`
		}
		_, bBody := asAgent(t, srv, "1", http.MethodGet, "/api/boards?q=ops&limit=4", "", "")
		var bp1 boardPage
		if err := json.Unmarshal([]byte(bBody), &bp1); err != nil {
			t.Fatal(err)
		}
		if bp1.Total != 6 || len(bp1.Items) != 4 || !bp1.Truncated {
			t.Errorf("boards page 1 = %+v", bp1)
		}
		_, bBody2 := asAgent(t, srv, "1", http.MethodGet, "/api/boards?q=ops&limit=4&cursor="+url.QueryEscape(bp1.NextCursor), "", "")
		var bp2 boardPage
		if err := json.Unmarshal([]byte(bBody2), &bp2); err != nil {
			t.Fatal(err)
		}
		if len(bp2.Items) != 2 || bp2.Truncated || bp2.NextCursor != "" {
			t.Errorf("boards page 2 = %+v", bp2)
		}

		// Task messages: listPageSmall = 10 + q filter + cursor round-trip.
		for i := range 12 {
			do(t, srv, http.MethodPost, "/api/tasks/1/comments", fmt.Sprintf(`{"body":"comment-%02d needle"}`, i))
		}
		type msgPage struct {
			Items      []map[string]any `json:"items"`
			Total      int              `json:"total"`
			Truncated  bool             `json:"truncated"`
			NextCursor string           `json:"nextCursor"`
		}
		_, msgBody := asAgent(t, srv, "1", http.MethodGet, "/api/tasks/1/messages?q=needle", "", "")
		var msgs1 msgPage
		if err := json.Unmarshal([]byte(msgBody), &msgs1); err != nil {
			t.Fatal(err)
		}
		if msgs1.Total != 12 || len(msgs1.Items) != 10 || !msgs1.Truncated || msgs1.NextCursor == "" {
			t.Errorf("messages page 1 = %+v", msgs1)
		}
		_, msgBody2 := asAgent(t, srv, "1", http.MethodGet, "/api/tasks/1/messages?q=needle&cursor="+url.QueryEscape(msgs1.NextCursor), "", "")
		var msgs2 msgPage
		if err := json.Unmarshal([]byte(msgBody2), &msgs2); err != nil {
			t.Fatal(err)
		}
		if len(msgs2.Items) != 2 || msgs2.Truncated {
			t.Errorf("messages page 2 = %+v", msgs2)
		}

		// Config audit: human ?limit=100 stays a bare JSON array (AuditSection.jsx),
		// while an agent call gets ListPage.
		do(t, srv, http.MethodPut, "/api/settings", `{"maxConcurrent":3}`)
		humanAudit := do(t, srv, http.MethodGet, "/api/deployment/config-audit?limit=100", "")
		if !strings.HasPrefix(strings.TrimSpace(humanAudit.Body.String()), "[") {
			t.Errorf("human GET /api/deployment/config-audit?limit=100 = %s, want bare JSON array", humanAudit.Body.String())
		}
		_, agentAudit := asAgent(t, srv, "1", http.MethodGet, "/api/deployment/config-audit", "", "")
		var auditPage struct {
			Total     int  `json:"total"`
			Truncated bool `json:"truncated"`
		}
		if err := json.Unmarshal([]byte(agentAudit), &auditPage); err != nil {
			t.Fatalf("agent config-audit not a page object: %v (%s)", err, agentAudit)
		}
	})

	t.Run("ci_runs_and_workflows", func(t *testing.T) {
		t.Parallel()
		now := time.Now()
		var runs []ci.Run
		for i := range 15 {
			t0 := now.Add(-time.Duration(i) * time.Minute)
			runs = append(runs, ci.Run{
				ID:         fmt.Sprintf("run-%02d", i),
				Repo:       "acme/widgets",
				Ref:        fmt.Sprintf("feature-%02d", i),
				Commit:     fmt.Sprintf("sha%02d", i),
				StartedAt:  &t0,
				Conclusion: ci.ConclusionSuccess,
				Jobs:       []ci.JobRun{{JobID: "go", Name: "go", Conclusion: ci.ConclusionSuccess}},
			})
		}
		var wfOpts []ci.WorkflowOption
		for i := range 25 {
			wfOpts = append(wfOpts, ci.WorkflowOption{
				Path: fmt.Sprintf(".grain/workflows/wf-%02d.yaml", i),
				Name: fmt.Sprintf("Workflow %02d", i),
			})
		}
		srv, client := testServer(t)
		client.Config.CI = &fakeCIRunner{runs: runs, workflowOptions: wfOpts}
		widgetsRef, err := model.ParseRepo("acme/widgets")
		if err != nil {
			t.Fatal(err)
		}
		enableCI(t, client.Store, widgetsRef)
		seedAPITask(t, client, "1", "acme/widgets", "api:ci:read")

		// Full CI runs default to 10 for agents and page via nextCursor.
		code, body := asAgent(t, srv, "1", http.MethodGet, "/api/ci/runs", "", "")
		if code != http.StatusOK {
			t.Fatalf("GET /api/ci/runs: %d %s", code, body)
		}
		var rp1 ui.CIRunsResponse
		if err := json.Unmarshal([]byte(body), &rp1); err != nil {
			t.Fatal(err)
		}
		if len(rp1.Runs) != 10 || rp1.Matched != 15 || !rp1.Truncated || rp1.NextCursor == "" {
			t.Errorf("ci/runs page 1 = %d runs, matched %d, truncated %v, cursor %q", len(rp1.Runs), rp1.Matched, rp1.Truncated, rp1.NextCursor)
		}
		_, body2 := asAgent(t, srv, "1", http.MethodGet, "/api/ci/runs?cursor="+url.QueryEscape(rp1.NextCursor), "", "")
		var rp2 ui.CIRunsResponse
		if err := json.Unmarshal([]byte(body2), &rp2); err != nil {
			t.Fatal(err)
		}
		if len(rp2.Runs) != 5 || rp2.Truncated || rp2.NextCursor != "" {
			t.Errorf("ci/runs page 2 = %d runs, truncated %v, cursor %q", len(rp2.Runs), rp2.Truncated, rp2.NextCursor)
		}

		// Compact CI runs support q + limit + cursor.
		_, cBody := asAgent(t, srv, "1", http.MethodGet, "/api/ci/runs?view=compact&q=feature-0&limit=4", "", "")
		var cp1 ui.CICompactRunsResponse
		if err := json.Unmarshal([]byte(cBody), &cp1); err != nil {
			t.Fatal(err)
		}
		if cp1.Matched != 10 || len(cp1.Runs) != 4 || !cp1.Truncated || cp1.NextCursor == "" {
			t.Errorf("compact ci/runs = %+v", cp1)
		}

		// CI workflows default to 20 for agents and support q + cursor.
		_, wfBody := asAgent(t, srv, "1", http.MethodGet, "/api/ci/workflows?repo=acme/widgets", "", "")
		var wfPage struct {
			Workflows  []ci.WorkflowOption `json:"workflows"`
			Total      int                 `json:"total"`
			Truncated  bool                `json:"truncated"`
			NextCursor string              `json:"nextCursor"`
		}
		if err := json.Unmarshal([]byte(wfBody), &wfPage); err != nil {
			t.Fatal(err)
		}
		if len(wfPage.Workflows) != 20 || wfPage.Total != 25 || !wfPage.Truncated || wfPage.NextCursor == "" {
			t.Errorf("ci/workflows page 1 = %d of %d, truncated %v", len(wfPage.Workflows), wfPage.Total, wfPage.Truncated)
		}
	})
}

// Heavy collection routes with multi-paragraph prompts, standing memories,
// job trees, or task descriptions stay under the 10 KB MCP tool-result cap
// on an unfiltered agent call.
func TestHeavyListRoutesDefaultAgentPageFitsToolResultCap(t *testing.T) {
	t.Parallel()

	now := time.Now()
	var runs []ci.Run
	for i := range 35 {
		t0 := now.Add(-time.Duration(i) * time.Minute)
		runs = append(runs, ci.Run{
			ID:         fmt.Sprintf("heavy-run-%02d", i),
			Repo:       "acme/widgets",
			Ref:        fmt.Sprintf("grain/branch-%02d-with-descriptive-slug", i),
			Commit:     fmt.Sprintf("0123456789abcdef0123456789abcdef012345%02d", i),
			StartedAt:  &t0,
			Conclusion: ci.ConclusionFailure,
			Jobs: []ci.JobRun{
				{JobID: "lint", Name: "lint", Conclusion: ci.ConclusionSuccess},
				{JobID: "go-unit", Name: "go unit", Conclusion: ci.ConclusionFailure, Error: "step \"go test\" exited 1"},
				{JobID: "ui-e2e", Name: "ui e2e", Conclusion: ci.ConclusionSuccess},
			},
		})
	}
	client, _, ctx := testClient(t)
	client.Config.CI = &fakeCIRunner{runs: runs}
	srv := ui.NewServerWithClient(client)
	seedAPITask(t, client, "1", "acme/widgets",
		"api:templates:read", "api:memory:read", "api:ci:read", "api:tasks:read", "api:access:read")

	for i := range 60 {
		role := "grain/contributor"
		if i%10 == 0 {
			role = "grain/viewer"
		}
		if _, err := client.AllowPrincipal(ctx, ui.AllowPrincipalRequest{
			Entry:  fmt.Sprintf("user:member-%03d@example.com", i),
			Role:   role,
			Reason: "seeded for paging cap check",
		}); err != nil {
			t.Fatal(err)
		}
	}

	prose := strings.Repeat("Detailed operational context and instructions for agents. ", 15) // ~870 B
	for i := range 30 {
		if _, err := client.CreateTemplate(ctx, ui.CreateTemplateRequest{
			Name:        fmt.Sprintf("heavy-tmpl-%02d", i),
			Title:       fmt.Sprintf("Heavy Template %02d", i),
			Repo:        "acme/widgets",
			Description: prose,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := client.PutMemory(ctx, fmt.Sprintf("acme/service-%02d", i), prose, model.Principal{}); err != nil {
			t.Fatal(err)
		}
	}
	targetRef, err := model.ParseRepo("acme/widgets")
	if err != nil {
		t.Fatal(err)
	}
	for i := range 260 {
		id := fmt.Sprintf("t-%03d", i)
		if err := client.Store.PutTask(ctx, model.Task{
			ID:     id,
			Intent: model.IntentImplement,
			Title:  fmt.Sprintf("Task %03d investigating production latency", i),
			Body:   "Short task description for list cap check.",
			Target: &targetRef,
		}); err != nil {
			t.Fatal(err)
		}
	}

	for _, path := range []string{
		"/api/templates",
		"/api/memory",
		"/api/ci/runs",
		"/api/tasks",
		"/api/tasks/states",
		"/api/events?since=0",
		"/api/access/principals",
	} {
		code, body := asAgent(t, srv, "1", http.MethodGet, path, "", "")
		if code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, code, body)
		}
		if len(body) > mcpToolResultCap {
			t.Errorf("agent default page for GET %s is %d bytes; want <= %d", path, len(body), mcpToolResultCap)
		}
		var meta struct {
			Truncated  bool   `json:"truncated"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal([]byte(body), &meta); err != nil {
			t.Fatalf("GET %s invalid JSON: %v", path, err)
		}
		if !meta.Truncated || meta.NextCursor == "" {
			t.Errorf("GET %s: truncated=%v nextCursor=%q; want true and non-empty", path, meta.Truncated, meta.NextCursor)
		}
	}

	// GET /api/access/principals filters by role in q, omits picker roles on
	// paged calls, and preserves roles on unparameterised human calls.
	code, viewerBody := asAgent(t, srv, "1", http.MethodGet, "/api/access/principals?q=grain/viewer", "", "")
	if code != http.StatusOK {
		t.Fatalf("GET /api/access/principals?q=grain/viewer: %d %s", code, viewerBody)
	}
	var viewerResp ui.AccessPrincipalsResponse
	if err := json.Unmarshal([]byte(viewerBody), &viewerResp); err != nil {
		t.Fatal(err)
	}
	if len(viewerResp.Entries) != 6 || viewerResp.Total != 6 || viewerResp.Truncated || len(viewerResp.Roles) != 0 {
		t.Errorf("viewer principals = %d (total %d, truncated %v, roles %d); want 6, total 6, not truncated, 0 roles",
			len(viewerResp.Entries), viewerResp.Total, viewerResp.Truncated, len(viewerResp.Roles))
	}
	humanRec := do(t, srv, http.MethodGet, "/api/access/principals", "")
	var humanResp ui.AccessPrincipalsResponse
	if err := json.Unmarshal(humanRec.Body.Bytes(), &humanResp); err != nil {
		t.Fatal(err)
	}
	if len(humanResp.Entries) != 60 || len(humanResp.Roles) == 0 {
		t.Errorf("human principals = %d entries, %d roles; want all 60 entries and role picker choices",
			len(humanResp.Entries), len(humanResp.Roles))
	}
}
