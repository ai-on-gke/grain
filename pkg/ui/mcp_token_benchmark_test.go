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

// What the controller MCP endpoint (POST /api/mcp) costs a run in context,
// measured against the surface it replaced: one generated api_* tool per
// route (ui.AgentAPITools), every schema of which sat in the model's
// context on every turn whether or not the run ever called it.
//
// Both surfaces are still in this package, so both are measured against
// the same seeded store, the same grants and the same questions, in one
// process: the legacy roster is built exactly as grain-sandbox used to
// build it, and the new one is read off /api/mcp as a harness would read
// it. Nothing here is a tokenizer. Sizes are bytes on the wire, turned
// into tokens by the same rule of thumb the rest of grain measures
// prompts with (model.PromptBytesPerToken), so the numbers are estimates
// and the ratios between them are what to read.
//
// Two numbers per surface and profile:
//
//   - schema: the bytes of tools/list (plus initialize's instructions for
//     the new surface, which carries a workflow guide the old one had no
//     need of). This is paid on every turn of a run, because a harness
//     sends the whole tool list with every model call.
//   - a replayed trajectory: the tool results of a short, realistic piece
//     of work asked of each surface in its own vocabulary -- including the
//     grain_explore_scope discovery call the new surface needs before it
//     can grain_invoke a route, so the comparison charges the new surface
//     for the indirection it introduced.
//
// From those, two totals a model bill is made of:
//
//   - context at the end of the trajectory: schema + every result, which
//     is what a provider with prompt caching charges roughly once.
//   - uncached input over the trajectory: the sum over turns of (schema +
//     every result so far), which is what a provider with no caching, or
//     a harness that defeats it, charges.
//
// TestControllerMCPTokenComparison prints the table and holds the claims
// the endpoint was built on (a fixed-size tool list; a cheaper task read);
// BenchmarkControllerMCPTokens reports the same numbers through
// testing.B so they can be tracked with benchstat.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// tokenProfile is one kind of caller, by the grants it holds, and the
// trajectory it replays.
type tokenProfile struct {
	name string
	// taskID is the acting task seeded with grants; "" is a human / BYO
	// harness caller with no acting task, who sees every non-internal
	// scope on the new surface.
	taskID string
	grants []string
	// trajectory is which replayed piece of work this profile does.
	trajectory string
}

// tokenCall is one tool call of a replayed trajectory and the size of
// what it answered.
type tokenCall struct {
	name  string
	bytes int
}

// tokenSurface is one surface measured for one profile.
type tokenSurface struct {
	name        string
	schemaBytes int
	tools       int
	// results is each tool call's result, in the order made.
	results []tokenCall
}

func (s tokenSurface) resultBytes() int {
	total := 0
	for _, r := range s.results {
		total += r.bytes
	}
	return total
}

// contextAtEnd is schema plus every result: the context a run holds once
// the trajectory is done.
func (s tokenSurface) contextAtEnd() int { return s.schemaBytes + s.resultBytes() }

// uncachedInput is what a provider with no prompt caching bills for the
// trajectory: on each turn the model reads the schema and every result
// so far. The turn that reads the last result is the turn that answers,
// so it is counted too.
func (s tokenSurface) uncachedInput() int {
	total, sofar := 0, 0
	for _, r := range s.results {
		sofar += r.bytes
		total += s.schemaBytes + sofar
	}
	if len(s.results) == 0 {
		return s.schemaBytes
	}
	return total
}

func estTokens(bytes int) int { return bytes / model.PromptBytesPerToken }

// everyGrantableScope is api:<scope> for every scope of the vocabulary:
// what a Director or a fully trusted BYO session reaches.
func everyGrantableScope() []string {
	var grants []string
	for _, s := range model.APIScopes() {
		grants = append(grants, "api:"+string(s.Scope))
	}
	return grants
}

func tokenProfiles() []tokenProfile {
	return []tokenProfile{
		{name: "ci-reader", taskID: "901", grants: []string{"api:ci:read"}, trajectory: "ci-triage"},
		{name: "steward", taskID: "902", grants: []string{"api:tasks:read", "api:tasks:write", "api:ci:read", "api:dispatch:read"}, trajectory: "task-triage"},
		{name: "director", taskID: "903", grants: everyGrantableScope(), trajectory: "task-triage"},
	}
}

// seedTokenFixture fills the store with the weight a real deployment's
// tasks carry -- a long description and a dozen comments each -- so that
// the difference between a full record and a summary is measurable, and
// seeds one acting task per profile holding its grants.
func seedTokenFixture(tb testing.TB, client *ui.Client) []string {
	tb.Helper()
	ctx := context.Background()
	if _, err := client.AddTargetRepo(ctx, "acme/widgets"); err != nil {
		tb.Fatal(err)
	}
	ref, err := model.ParseRepo("acme/widgets")
	if err != nil {
		tb.Fatal(err)
	}
	var heavy []string
	for i := range 12 {
		id := fmt.Sprintf("8%02d", i)
		task := model.Task{
			ID:     id,
			Intent: model.IntentImplement,
			Title:  fmt.Sprintf("Port the merge-queue fix %s to the release branch", id),
			Body:   strings.Repeat("What this task is for, at length, with the paths it touches and the checks it must keep green. ", 40),
			Target: &ref,
		}
		if err := client.Store.PutTask(ctx, task); err != nil {
			tb.Fatal(err)
		}
		for c := range 12 {
			body := strings.Repeat(fmt.Sprintf("comment %d on %s says a good deal about the run. ", c, id), 12)
			if err := client.AddComment(ctx, id, body, nil); err != nil {
				tb.Fatal(err)
			}
		}
		heavy = append(heavy, id)
	}
	for _, p := range tokenProfiles() {
		seedAPITaskTB(tb, client, p.taskID, "acme/widgets", p.grants...)
	}
	return heavy
}

// seedAPITaskTB is seedAPITask for a testing.TB, so benchmarks can use it.
func seedAPITaskTB(tb testing.TB, client *ui.Client, id, repo string, grants ...string) {
	tb.Helper()
	ref, err := model.ParseRepo(repo)
	if err != nil {
		tb.Fatal(err)
	}
	task := model.Task{ID: id, Intent: model.IntentImplement, Title: "task " + id, Target: &ref}
	for _, g := range grants {
		task.Grants = append(task.Grants, model.Grant{Capability: g, Via: model.GrantByLabel})
	}
	if err := client.Store.PutTask(context.Background(), task); err != nil {
		tb.Fatal(err)
	}
}

// tokenFixture is the server both surfaces are measured against.
type tokenFixture struct {
	srv    *ui.Server
	client *ui.Client
	code   string
	daemon *httptest.Server
	heavy  []string
}

func newTokenFixture(tb testing.TB) *tokenFixture {
	tb.Helper()
	srv, client, code := newSingleTenantControllerMCPServerTB(tb)
	daemon := httptest.NewServer(srv)
	tb.Cleanup(daemon.Close)
	return &tokenFixture{srv: srv, client: client, code: code, daemon: daemon, heavy: seedTokenFixture(tb, client)}
}

// newSingleTenantControllerMCPServerTB is newSingleTenantControllerMCPServer
// for a testing.TB: a test gets exactly that; a benchmark builds the same
// store and server, claiming the package log as testClient does so its
// lines are not printed between the benchmark's own.
func newSingleTenantControllerMCPServerTB(tb testing.TB) (*ui.Server, *ui.Client, string) {
	tb.Helper()
	if t, ok := tb.(*testing.T); ok {
		return newSingleTenantControllerMCPServer(t)
	}
	ui.LogToBenchmark(tb.(*testing.B))
	db, err := sqlite.Open(sqlite.DefaultConfig(tb.TempDir()))
	if err != nil {
		tb.Fatalf("opening embedded sqlite: %v", err)
	}
	tb.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		tb.Fatalf("applying schema: %v", err)
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	client := ui.NewClient(ui.Config{
		Actor:         ui.DefaultActor("alice"),
		DefaultTarget: &repo,
		Capabilities:  ui.OfferedCapabilities(),
	}, store)
	client.Now = func() time.Time { return baseTime }
	tokenPath := filepath.Join(tb.TempDir(), "local-api-token")
	code, err := ui.EnsureLocalAPIToken(tokenPath)
	if err != nil {
		tb.Fatal(err)
	}
	if err := os.Chmod(tokenPath, 0o600); err != nil {
		tb.Fatal(err)
	}
	client.Config.LocalAPITokenFile = tokenPath
	client.Config.Actor = model.Principal{Kind: model.PrincipalHuman, ID: "owner@example.com"}
	cfg, err := store.GetConfig(ctx)
	if err != nil {
		tb.Fatal(err)
	}
	if cfg == nil {
		cfg = &model.Config{}
	}
	if cfg.PollInterval == 0 {
		cfg.PollInterval = model.DefaultPollInterval
	}
	if err := store.PutConfig(ctx, *cfg); err != nil {
		tb.Fatal(err)
	}
	return ui.NewServerWithClient(client), client, code
}

// legacyRoster is the roster grain-sandbox used to serve a run holding
// grants: one generated tool per reachable route, calling the daemon over
// HTTP as the acting task.
func (f *tokenFixture) legacyRoster(taskID string, grants []string) (*mcp.Registry, map[string]mcp.Tool) {
	hc := ui.NewHTTPClient(f.daemon.URL)
	hc.Token = f.code
	tools := ui.AgentAPITools(hc, taskID, grants)
	reg := mcp.NewRegistry()
	reg.Register(tools...)
	byPattern := map[string]mcp.Tool{}
	for _, tool := range tools {
		for _, r := range ui.AgentAPIRoutes() {
			if r.Tool == tool.Name {
				byPattern[r.Pattern] = tool
			}
		}
	}
	return reg, byPattern
}

const (
	rpcInitialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`
	rpcToolsList  = `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
)

// legacySchemaBytes is what a harness attached to the legacy roster held
// in context: tools/list, plus initialize (whose instructions are empty).
func legacySchemaBytes(reg *mcp.Registry) (int, int) {
	ctx := context.Background()
	list := reg.Handle(ctx, []byte(rpcToolsList))
	var resp struct {
		Result struct {
			Tools []json.RawMessage `json:"tools"`
		} `json:"result"`
	}
	_ = json.Unmarshal(list, &resp)
	return len(list) + len(reg.Handle(ctx, []byte(rpcInitialize))), len(resp.Result.Tools)
}

// controllerSchemaBytes is the same for /api/mcp, read as a harness reads
// it: initialize (with its instructions) and tools/list.
func (f *tokenFixture) controllerSchemaBytes(tb testing.TB, headers map[string]string) (int, int) {
	tb.Helper()
	init := f.post(tb, headers, rpcInitialize)
	list := f.post(tb, headers, rpcToolsList)
	var resp struct {
		Result struct {
			Tools []json.RawMessage `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(list, &resp); err != nil {
		tb.Fatal(err)
	}
	return len(init) + len(list), len(resp.Result.Tools)
}

func (f *tokenFixture) post(tb testing.TB, headers map[string]string, body string) []byte {
	tb.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/mcp", strings.NewReader(body))
	req.RemoteAddr = "203.0.113.42:51234"
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		tb.Fatalf("POST /api/mcp = %d: %s", rec.Code, rec.Body.String())
	}
	return rec.Body.Bytes()
}

// controllerCall makes one tools/call on /api/mcp and returns the text the
// model would read.
func (f *tokenFixture) controllerCall(tb testing.TB, headers map[string]string, tool string, args map[string]any) string {
	tb.Helper()
	payload, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args},
	})
	body := f.post(tb, headers, string(payload))
	var resp struct {
		Error  *struct{ Message string } `json:"error"`
		Result struct {
			Content []struct{ Text string } `json:"content"`
			IsError bool                    `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		tb.Fatal(err)
	}
	if resp.Error != nil {
		tb.Fatalf("%s: protocol error %s", tool, resp.Error.Message)
	}
	text := ""
	if len(resp.Result.Content) > 0 {
		text = resp.Result.Content[0].Text
	}
	if resp.Result.IsError {
		tb.Fatalf("%s(%v) failed: %s", tool, args, text)
	}
	return text
}

// legacyCall makes one call through a generated tool's handler, exactly
// as the mcpserver's tools/call did.
func legacyCall(tb testing.TB, tool mcp.Tool, args map[string]any) string {
	tb.Helper()
	if tool.Handler == nil {
		tb.Fatalf("the legacy roster has no tool for this call")
	}
	res := tool.Handler(context.Background(), args)
	if res.IsError {
		tb.Fatalf("%s(%v) failed: %s", tool.Name, args, res.Text)
	}
	return res.Text
}

// measure replays p's trajectory on both surfaces and returns them, legacy
// first.
func (f *tokenFixture) measure(tb testing.TB, p tokenProfile) (legacy, controller tokenSurface) {
	tb.Helper()
	legacy.name, controller.name = "legacy api_* roster", "controller /api/mcp"

	reg, byPattern := f.legacyRoster(p.taskID, p.grants)
	legacy.schemaBytes, legacy.tools = legacySchemaBytes(reg)

	headers := map[string]string{ui.AccessCodeHeader: f.code}
	if p.taskID != "" {
		headers[ui.ActingTaskHeader] = p.taskID
	}
	controller.schemaBytes, controller.tools = f.controllerSchemaBytes(tb, headers)

	read := f.heavy[:3]
	// The note goes on a task the trajectory never reads, so that a
	// benchmark replaying it does not read its own earlier notes back.
	noted := f.heavy[4]
	comment := map[string]any{"body": "Looked at this: the port is blocked on the base branch's red CI, not on the task."}
	viaLegacy := func(pattern string, args map[string]any) {
		tool := byPattern[pattern]
		legacy.results = append(legacy.results, tokenCall{tool.Name, len(legacyCall(tb, tool, args))})
	}
	viaController := func(tool string, args map[string]any) {
		label := tool
		if tool == ui.ToolGrainInvoke {
			label += "(" + args["tool"].(string) + ")"
		} else if scope, ok := args["scope"].(string); ok {
			label += "(" + scope
			if narrow, ok := args["tool"].(string); ok {
				label += ", tool=" + narrow
			}
			label += ")"
		}
		controller.results = append(controller.results, tokenCall{label, len(f.controllerCall(tb, headers, tool, args))})
	}
	switch p.trajectory {
	case "ci-triage":
		// What is red? One list, compact.
		ciQuery := map[string]any{"query": map[string]any{"view": "compact", "limit": 10}}
		viaLegacy("GET /api/ci/runs", ciQuery)
		// The new surface has to find the route before it can call it,
		// asking for the one it wants as the workflow guide says to.
		viaController(ui.ToolGrainExploreScope, map[string]any{"scope": "ci", "tool": "runs"})
		viaController(ui.ToolGrainInvoke, map[string]any{"tool": byPattern["GET /api/ci/runs"].Name, "arguments": ciQuery})
	case "task-triage":
		// What moved? List the queue, read three tasks, leave a note on one.
		viaLegacy("GET /api/tasks", map[string]any{"query": map[string]any{"fields": "summary", "limit": 20}})
		for _, id := range read {
			viaLegacy("GET /api/tasks/{id}", map[string]any{"id": id})
		}
		viaLegacy("POST /api/tasks/{id}/comments", map[string]any{"id": noted, "body": comment})

		viaController(ui.ToolGrainListTasks, map[string]any{"limit": 20})
		for _, id := range read {
			viaController(ui.ToolGrainGetTask, map[string]any{"id": id})
		}
		viaController(ui.ToolGrainExploreScope, map[string]any{"scope": "tasks", "tool": "comment"})
		viaController(ui.ToolGrainInvoke, map[string]any{
			"tool": byPattern["POST /api/tasks/{id}/comments"].Name, "arguments": map[string]any{"id": noted, "body": comment}})
	default:
		tb.Fatalf("unknown trajectory %q", p.trajectory)
	}
	return legacy, controller
}

// wholeGroupBytes is what grain_explore_scope answers for a whole group
// with no tool named: the discovery an agent that ignores the guide pays,
// reported beside the trajectory for reference rather than charged to it.
func (f *tokenFixture) wholeGroupBytes(tb testing.TB, p tokenProfile, group string) int {
	tb.Helper()
	headers := map[string]string{ui.AccessCodeHeader: f.code}
	if p.taskID != "" {
		headers[ui.ActingTaskHeader] = p.taskID
	}
	return len(f.controllerCall(tb, headers, ui.ToolGrainExploreScope, map[string]any{"scope": group}))
}

func pct(after, before int) string {
	if before == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%+.0f%%", (float64(after)-float64(before))/float64(before)*100)
}

// TestControllerMCPTokenComparison prints the before/after table and holds
// the claims the endpoint was built on: the tool list is the same six
// tools whatever the grants, an order of magnitude under the roster a
// many-scoped caller used to carry; and a task read through
// grain_get_task is cheaper than the full record api_get_task returned --
// by enough that a short triage pays for the discovery call it now needs
// and still comes out ahead, cached or not.
func TestControllerMCPTokenComparison(t *testing.T) {
	t.Parallel()
	f := newTokenFixture(t)

	var table, detail strings.Builder
	fmt.Fprintf(&table, "\n%-10s %-22s %6s %10s %6s %12s %14s %14s\n",
		"profile", "surface", "tools", "schema~tok", "calls", "results~tok", "ctx-at-end~tok", "uncached~tok")
	schemaBytes := map[string]int{}
	for _, p := range tokenProfiles() {
		legacy, controller := f.measure(t, p)
		schemaBytes[p.name] = controller.schemaBytes
		for _, s := range []tokenSurface{legacy, controller} {
			fmt.Fprintf(&table, "%-10s %-22s %6d %10d %6d %12d %14d %14d\n",
				p.name, s.name, s.tools, estTokens(s.schemaBytes), len(s.results),
				estTokens(s.resultBytes()), estTokens(s.contextAtEnd()), estTokens(s.uncachedInput()))
			fmt.Fprintf(&detail, "%s / %s:\n", p.name, s.name)
			for _, c := range s.results {
				fmt.Fprintf(&detail, "  %-44s %6d bytes ~%5d tok\n", c.name, c.bytes, estTokens(c.bytes))
			}
		}
		group := map[string]string{"ci-triage": "ci", "task-triage": "tasks"}[p.trajectory]
		whole := f.wholeGroupBytes(t, p, group)
		fmt.Fprintf(&detail, "  (for reference: grain_explore_scope(%s) whole group  %6d bytes ~%5d tok)\n", group, whole, estTokens(whole))
		fmt.Fprintf(&table, "%-10s %-22s %6s %10s %6s %12s %14s %14s\n", "", "  change",
			pct(controller.tools, legacy.tools), pct(controller.schemaBytes, legacy.schemaBytes),
			pct(len(controller.results), len(legacy.results)), pct(controller.resultBytes(), legacy.resultBytes()),
			pct(controller.contextAtEnd(), legacy.contextAtEnd()), pct(controller.uncachedInput(), legacy.uncachedInput()))

		// The tool list is six tools whatever the grants.
		if controller.tools != len(ui.ControllerMCPToolNames()) {
			t.Errorf("%s: /api/mcp lists %d tools, want %d", p.name, controller.tools, len(ui.ControllerMCPToolNames()))
		}
		// A triage that reads tasks ends with less in context on the new
		// surface, discovery call included, and is billed less for the
		// whole of it with no caching at all.
		if p.trajectory == "task-triage" {
			if controller.contextAtEnd() >= legacy.contextAtEnd() {
				t.Errorf("%s: triage ends with %d bytes in context on /api/mcp, %d on the legacy roster",
					p.name, controller.contextAtEnd(), legacy.contextAtEnd())
			}
			if controller.uncachedInput() >= legacy.uncachedInput() {
				t.Errorf("%s: triage reads %d uncached bytes on /api/mcp, %d on the legacy roster",
					p.name, controller.uncachedInput(), legacy.uncachedInput())
			}
		}
		// And for the caller the endpoint was built for -- one holding
		// many scopes -- the schema alone is cut by an order of magnitude.
		if p.name == "director" && controller.schemaBytes*10 > legacy.schemaBytes {
			t.Errorf("director: /api/mcp schema is %d bytes against legacy %d, want at most a tenth",
				controller.schemaBytes, legacy.schemaBytes)
		}
	}
	// The schema is the same bytes for every caller: it is what makes the
	// new surface's cost a constant rather than a function of the grants.
	// (It is not smaller than every legacy roster: a task holding one
	// narrow scope had a roster of a handful of routes, about the size of
	// the fixed six plus the guide, and pays a discovery call on top. The
	// break-even is a roster of about ten routes; see the ci-reader row.)
	for name, n := range schemaBytes {
		if n != schemaBytes["director"] {
			t.Errorf("%s: /api/mcp schema is %d bytes, director's %d: the fixed roster is not fixed", name, n, schemaBytes["director"])
		}
	}
	t.Log(table.String())
	t.Log("\n" + detail.String())
}

// BenchmarkControllerMCPTokens reports the same measurement per profile
// and surface as benchmark metrics (schema-tok, results-tok, ctx-end-tok,
// uncached-tok), so `go test -bench ControllerMCPTokens -run ^$ ./pkg/ui`
// gives numbers benchstat can diff across commits.
func BenchmarkControllerMCPTokens(b *testing.B) {
	f := newTokenFixture(b)
	for _, p := range tokenProfiles() {
		for _, surface := range []string{"legacy", "controller"} {
			b.Run(p.name+"/"+surface, func(b *testing.B) {
				var s tokenSurface
				for i := 0; i < b.N; i++ {
					legacy, controller := f.measure(b, p)
					if surface == "legacy" {
						s = legacy
					} else {
						s = controller
					}
				}
				b.ReportMetric(float64(s.tools), "tools")
				b.ReportMetric(float64(estTokens(s.schemaBytes)), "schema-tok")
				b.ReportMetric(float64(estTokens(s.resultBytes())), "results-tok")
				b.ReportMetric(float64(estTokens(s.contextAtEnd())), "ctx-end-tok")
				b.ReportMetric(float64(estTokens(s.uncachedInput())), "uncached-tok")
			})
		}
	}
}
