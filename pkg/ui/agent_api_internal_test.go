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

package ui

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
)

// TestRouteScopesAreTotal is the route table and the scope table held to
// each other: every route Server registers has a scope, every scope
// names a route, and every scope is one of the documented vocabulary
// (docs/agent-api.md). A route added without a scope fails here, which
// is the whole of what makes a new UI action agent-callable with no
// further work -- and the whole of what stops one being reachable by an
// agent under a scope nobody wrote down.
func TestRouteScopesAreTotal(t *testing.T) {
	s := NewServerWithClient(nil)
	routes := map[string]bool{}
	for _, pattern := range s.Routes() {
		if pattern == "/" {
			continue
		}
		routes[pattern] = true
		scope, ok := routeScopes[pattern]
		if !ok {
			t.Errorf("route %q has no scope in route_scopes.go", pattern)
			continue
		}
		if scope == scopeInternal {
			continue
		}
		if _, ok := apiScopeInfo(scope); !ok {
			t.Errorf("route %q has scope %q, which is not in the documented vocabulary (model.APIScopes)", pattern, scope)
		}
		method, _, _ := strings.Cut(pattern, " ")
		if method == http.MethodGet && !strings.HasSuffix(string(scope), ":read") {
			t.Errorf("read route %q has scope %q; reads take a :read scope", pattern, scope)
		}
	}
	for pattern := range routeScopes {
		if !routes[pattern] {
			t.Errorf("route_scopes.go scopes %q, which Server does not register", pattern)
		}
	}
}

// TestAPIScopeVocabularyIsDocumented holds docs/agent-api.md and the
// vocabulary to each other the cheap way: every scope and preset is named
// in the document.
func TestAPIScopeVocabularyIsDocumented(t *testing.T) {
	doc := readRepoFile(t, "docs/agent-api.md")
	for _, s := range model.APIScopes() {
		if !strings.Contains(doc, "`"+string(s.Scope)+"`") {
			t.Errorf("docs/agent-api.md does not name scope %s", s.Scope)
		}
	}
	for _, p := range model.APIPresets() {
		if !strings.Contains(doc, "`"+p.Name+"`") {
			t.Errorf("docs/agent-api.md does not name preset %s", p.Name)
		}
	}
}

func TestGeneratedToolsMatchTheRouteTable(t *testing.T) {
	routes := AgentAPIRoutes()
	want := map[string]bool{}
	for pattern, scope := range routeScopes {
		if scope != scopeInternal {
			want[pattern] = true
		}
	}
	names := map[string]bool{}
	for _, r := range routes {
		if !want[r.Pattern] {
			t.Errorf("tool %s serves %s, which is internal or unscoped", r.Tool, r.Pattern)
		}
		delete(want, r.Pattern)
		if r.Scope != routeScopes[r.Pattern] {
			t.Errorf("tool %s has scope %s, route table says %s", r.Tool, r.Scope, routeScopes[r.Pattern])
		}
		if names[r.Tool] {
			t.Errorf("tool name %s is generated twice", r.Tool)
		}
		names[r.Tool] = true
		if !strings.HasPrefix(r.Tool, APIToolPrefix) || len(mcp.QualifiedToolName(r.Tool)) > 64 {
			t.Errorf("tool name %q: want the %s prefix and a qualified name of at most 64 characters", r.Tool, APIToolPrefix)
		}
		schema := r.inputSchema()
		props := schema["properties"].(map[string]any)
		for _, p := range r.Params {
			if _, ok := props[p]; !ok {
				t.Errorf("tool %s takes no %s argument, which %s needs", r.Tool, p, r.Pattern)
			}
		}
		if r.NeedsReason && !slices.Contains(schema["required"].([]string), "reason") {
			t.Errorf("tool %s is destructive but does not require a reason", r.Tool)
		}
	}
	for pattern := range want {
		t.Errorf("route %s is scoped but has no generated tool", pattern)
	}

	// Every generated name is published to the frameworks' allowed-tool
	// lists, or a run granted the scope would be refused its own tool.
	published := agent.PublishedToolNames()
	for name := range names {
		if !slices.Contains(published, name) {
			t.Errorf("agent.PublishedToolNames does not name generated tool %s", name)
		}
	}

	// The tools a grant gets are exactly the routes of its scopes.
	tools := AgentAPITools(NewHTTPClient("http://127.0.0.1:1"), "7", []string{"api:ci:read"})
	if len(tools) == 0 {
		t.Fatal("api:ci:read was served no tools")
	}
	for _, tool := range tools {
		r := routeByTool(routes, tool.Name)
		if r.Scope != "ci:read" {
			t.Errorf("api:ci:read was served %s (scope %s)", tool.Name, r.Scope)
		}
	}
	if got := AgentAPITools(nil, "7", []string{"grain-log"}); got != nil {
		t.Errorf("a task with no api grant was served %d api tools", len(got))
	}
}

// TestEveryScopedRouteIsDescribed holds routeDocs, routeBodies and
// routeQueries to the handlers they describe, so a UI action is fully
// described to agents as soon as its route is scoped: every route that
// changes something has a sentence; every route whose handler reads a
// body declares the type it reads, the very type the handler's source
// decodes into (routeHandlerInputs); every query parameter a handler
// names is declared, and nothing is declared that no handler reads.
func TestEveryScopedRouteIsDescribed(t *testing.T) {
	inputs := routeHandlerInputs(t)
	scoped := map[string]bool{}
	for _, r := range AgentAPIRoutes() {
		scoped[r.Pattern] = true
		in := inputs[r.Pattern]
		if r.Method != http.MethodGet && routeDocs[r.Pattern] == "" {
			t.Errorf("%s changes something and has no routeDocs sentence: say what it does, for the agents calling %s", r.Pattern, r.Tool)
		}

		var want string
		for _, b := range in.bodies {
			if want != "" && b != want {
				t.Errorf("%s's handler reads its body into both %s and %s; routeBodies can declare one", r.Pattern, want, b)
			}
			want = b
		}
		if r.Method == http.MethodGet || bodyReadForAnotherMethod[r.Pattern] {
			want = ""
		}
		body, declared := routeBodies[r.Pattern]
		switch {
		case want != "" && !declared:
			t.Errorf("%s's handler reads its body into %s, which routeBodies does not declare", r.Pattern, want)
		case want == "" && declared:
			t.Errorf("routeBodies declares %T for %s, whose handler reads no body", body, r.Pattern)
		case declared && sourceTypeName(reflect.TypeOf(body)) != want:
			t.Errorf("routeBodies declares %T for %s, whose handler reads its body into %s", body, r.Pattern, want)
		}

		var declaredQueries []string
		for name := range routeQueries[r.Pattern] {
			declaredQueries = append(declaredQueries, name)
		}
		slices.Sort(declaredQueries)
		if !slices.Equal(declaredQueries, in.queries) {
			t.Errorf("routeQueries declares %v for %s, whose handler reads query parameters %v", declaredQueries, r.Pattern, in.queries)
		}
		for name, doc := range routeQueries[r.Pattern] {
			if strings.TrimSpace(doc) == "" {
				t.Errorf("routeQueries gives %s's %s no description", r.Pattern, name)
			}
		}
	}
	for _, table := range []map[string]bool{keysOf(routeDocs), keysOf(routeBodies), keysOf(routeQueries)} {
		for pattern := range table {
			if !scoped[pattern] {
				t.Errorf("%s is described, but is not a scoped route", pattern)
			}
		}
	}
	for pattern := range bodyReadForAnotherMethod {
		if !scoped[pattern] {
			t.Errorf("bodyReadForAnotherMethod names %s, which is not a scoped route", pattern)
		}
	}
}

// bodyReadForAnotherMethod are the routes whose handler, shared with
// another method's route, reads a body only for that other method.
var bodyReadForAnotherMethod = map[string]bool{
	"DELETE /api/agent-backends/{id}/codex-auth": true, // handleBackendCodexAuth reads one for PUT
}

func keysOf[V any](m map[string]V) map[string]bool {
	out := map[string]bool{}
	for k := range m {
		out[k] = true
	}
	return out
}

// sourceTypeName is t as this package's source spells it: Foo for a
// type of its own, model.Foo for another package's.
func sourceTypeName(t reflect.Type) string {
	return strings.TrimPrefix(t.String(), "ui.")
}

func TestToolSchemasTypeBodiesAndQueries(t *testing.T) {
	routes := AgentAPIRoutes()
	props := func(name string) map[string]any {
		t.Helper()
		r := routeByTool(routes, name)
		if r.Tool == "" {
			t.Fatalf("no tool %s", name)
		}
		return r.inputSchema()["properties"].(map[string]any)
	}
	comment := props("api_add_comment")["body"].(map[string]any)
	if _, ok := comment["properties"].(map[string]any)["body"]; !ok {
		t.Errorf("api_add_comment's body schema does not describe its body field: %v", comment)
	}
	if _, ok := props("api_approve")["body"]; ok {
		t.Error("api_approve offers a body, which its handler never reads")
	}
	query := props("api_list_ci_runs")["query"].(map[string]any)
	if _, ok := query["properties"].(map[string]any)["conclusion"]; !ok {
		t.Errorf("api_list_ci_runs's query does not describe conclusion: %v", query)
	}
	if _, ok := query["additionalProperties"]; !ok {
		t.Error("a typed query no longer carries parameters it does not name")
	}
}

func routeByTool(routes []APIRoute, name string) APIRoute {
	for _, r := range routes {
		if r.Tool == name {
			return r
		}
	}
	return APIRoute{}
}

// TestDoneWhenToolsExist names the tools the director needs for the
// release-candidate cut, so a rename breaks here rather than in a prompt.
func TestDoneWhenToolsExist(t *testing.T) {
	want := map[string]APIScope{
		"api_reorder":           "tasks:write",
		"api_start_ci_run":      "ci:write",
		"api_get_ci_run":        "ci:read",
		"api_run_schedule":      "schedules:write",
		"api_cut_candidate":     "releases:write",
		"api_promote_candidate": "releases:write",
	}
	routes := AgentAPIRoutes()
	for name, scope := range want {
		if r := routeByTool(routes, name); r.Scope != scope {
			t.Errorf("tool %s: scope %q, want %q", name, r.Scope, scope)
		}
	}
}

func TestAPIPresetsExpand(t *testing.T) {
	has := func(name string, scopes ...APIScope) {
		t.Helper()
		set := APIGrantsOf([]string{name})
		for _, s := range scopes {
			if !set.Allows(s, "acme/widgets") {
				t.Errorf("%s does not grant %s", name, s)
			}
		}
	}
	has("api:task-admin", "tasks:write", "merge:write", "ci:write")
	has("api:ci-read", "ci:read")
	has("api:deployment-ops", "deployment:upgrade", "deployment:restart", "deployment:reboot")
	has("api:release-manager", "releases:write", "schedules:write", "ci:write", "tasks:write")
	// The old capabilities are aliases for their presets.
	has("self-debug", "tasks:read", "deployment:read")
	has("api:self-debug", "tasks:read", "deployment:read")
	has("self-upgrade", "deployment:read", "deployment:upgrade")
	has("api:self-upgrade", "deployment:read", "deployment:upgrade")
	has("pipeline-control", "dispatch:write")
	has("memory", "memory:read", "memory:write")
	has("api:memory", "memory:read", "memory:write")
	has("release-notes", "release-notes:read", "release-notes:write")
	has("api:release-notes", "release-notes:read", "release-notes:write")
	has("test-quarantine", "tests:write")
	has("api:test-quarantine", "tests:write")
	for _, p := range model.APIPresets() {
		for _, s := range p.Scopes {
			if _, ok := apiScopeInfo(s); !ok {
				t.Errorf("preset %s names %s, which is not a scope", p.Name, s)
			}
		}
	}
	if APIGrantsOf([]string{"api:task-admin"}).Has("merge:force") {
		t.Error("task-admin grants merge:force")
	}
}

func TestParseAPIGrant(t *testing.T) {
	for _, tc := range []struct {
		name    string
		isAPI   bool
		wantErr bool
		repo    string
	}{
		{"grain-log", false, false, ""},
		{"self-debug", false, false, ""},
		{"api:self-debug", true, false, ""},
		{"api:tasks:write", true, false, ""},
		{"api:tasks:write@acme/widgets", true, false, "acme/widgets"},
		{"api:task-admin@acme/widgets", true, false, "acme/widgets"},
		{"api:tasks:fly", true, true, ""},
		{"api:internal", true, true, ""},
		{"api:tasks:write@nope", true, true, ""},
	} {
		g, ok, err := ParseAPIGrant(tc.name)
		if ok != tc.isAPI || (err != nil) != tc.wantErr || g.Repo != tc.repo {
			t.Errorf("ParseAPIGrant(%q) = %+v, %v, %v", tc.name, g, ok, err)
		}
	}
	if _, ok := AgentAPIDynamicProvider("api:ci:read"); !ok {
		t.Error("api:ci:read has no provider, so a task holding it would not dispatch")
	}
	if _, ok := AgentAPIDynamicProvider("api:nonsense"); ok {
		t.Error("api:nonsense resolved to a provider")
	}
}

func TestDangerousAPIGrantsAreEscalationsEvenWhenHeld(t *testing.T) {
	parent := model.Task{ID: "1", Grants: []model.Grant{
		{Capability: "api:secrets:write"}, {Capability: "api:tasks:write"}}}
	child := model.Task{Grants: []model.Grant{
		{Capability: "api:secrets:write", Via: model.GrantByLabel},
		{Capability: "api:tasks:write", Via: model.GrantByLabel}}}
	got := model.EscalatedCapabilities(child, parent)
	if !slices.Equal(got, []string{"api:secrets:write"}) {
		t.Errorf("EscalatedCapabilities = %v, want only the dangerous api:secrets:write", got)
	}
}

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestAPICapabilitiesCarryPickerFields: the picker's "Grain API" group
// is drawn from these fields, not from parsing the rows' names.
func TestAPICapabilitiesCarryPickerFields(t *testing.T) {
	rows := APICapabilities()
	seenScope := false
	for _, row := range rows {
		if len(row.Scopes) == 0 || row.Summary == "" {
			t.Errorf("%s carries no scopes or summary", row.ID)
		}
		if row.Preset {
			if seenScope {
				t.Errorf("preset %s comes after a scope", row.ID)
			}
		} else {
			seenScope = true
		}
	}
	byID := map[string]Capability{}
	for _, row := range rows {
		byID[row.ID] = row
	}
	if r := byID["api:task-admin"]; !r.Preset || !slices.Equal(r.Scopes,
		[]string{"tasks:read", "tasks:write", "merge:write", "ci:read", "ci:write", "notifications:write"}) {
		t.Errorf("api:task-admin = %+v", r)
	}
	if r := byID["api:merge:force"]; !r.Dangerous || r.Preset {
		t.Errorf("api:merge:force = %+v, want a dangerous scope", r)
	}
	if r := byID["api:tasks:read"]; r.Dangerous {
		t.Error("api:tasks:read is flagged dangerous")
	}
	if r, ok := apiCapability("api:merge:force@acme/widgets"); !ok || !r.Dangerous ||
		!slices.Equal(r.Scopes, []string{"merge:force"}) {
		t.Errorf("repo-limited merge:force = %+v", r)
	}
}

// bodyReaders are the functions a handler reads its JSON body through,
// each with the index of the argument it decodes into.
var bodyReaders = map[string]int{
	"readJSON":           2,
	"readJSONLimit":      2,
	"readJSONRefusing":   2,
	"readOptionalJSON":   2,
	"readAttachmentJSON": 2,
}

// handlerInputs is what a route's handler reads from its request, as
// its source spells it: the type of every value it decodes its body
// into, and every query parameter it names.
type handlerInputs struct {
	bodies  []string
	queries []string
}

func (in *handlerInputs) addQuery(quoted string) {
	if key, err := strconv.Unquote(quoted); err == nil && !slices.Contains(in.queries, key) {
		in.queries = append(in.queries, key)
	}
}

// routeHandlerInputs reads this package's source for what each route's
// handler takes: every s.route call's pattern, mapped to what its
// handler -- or a helper it hands the request or its query to -- passes
// to a bodyReader or a json.Decoder over r.Body, and the parameters it
// reads with Get from r.URL.Query().
func routeHandlerInputs(t *testing.T) map[string]handlerInputs {
	t.Helper()
	fset := token.NewFileSet()
	funcs := map[string][]*ast.FuncDecl{}
	var parsed []*ast.File
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		parsed = append(parsed, f)
		for _, decl := range f.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
				funcs[fn.Name.Name] = append(funcs[fn.Name.Name], fn)
			}
		}
	}

	var inputsOf func(name string, seen map[string]bool, in *handlerInputs)
	inputsOf = func(name string, seen map[string]bool, in *handlerInputs) {
		if seen[name] {
			return
		}
		seen[name] = true
		for _, fn := range funcs[name] {
			queries := queryValuesIn(fn)
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if ix, ok := n.(*ast.IndexExpr); ok { // q["conclusion"], every value of a repeated parameter
					if lit, ok := ix.Index.(*ast.BasicLit); ok && lit.Kind == token.STRING && isQuery(ix.X, queries) {
						in.addQuery(lit.Value)
					}
					return true
				}
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				callee := calleeName(call.Fun)
				if i, ok := bodyReaders[callee]; ok && len(call.Args) > i {
					in.bodies = append(in.bodies, declaredType(fn, call.Args[i]))
					return true
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && len(call.Args) == 1 {
					switch callee {
					case "Decode":
						if dec, ok := sel.X.(*ast.CallExpr); ok && calleeName(dec.Fun) == "NewDecoder" &&
							strings.Contains(types.ExprString(dec), ".Body") {
							in.bodies = append(in.bodies, declaredType(fn, call.Args[0]))
						}
						return true
					case "Get":
						if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING && isQuery(sel.X, queries) {
							in.addQuery(lit.Value)
						}
						return true
					}
				}
				if takesRequest(funcs[callee]) {
					inputsOf(callee, seen, in)
				}
				return true
			})
		}
	}

	out := map[string]handlerInputs{}
	for _, f := range parsed {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || calleeName(call.Fun) != "route" || len(call.Args) != 2 {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok {
				return true
			}
			pattern, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Fatal(err)
			}
			handler := call.Args[1]
			if c, ok := handler.(*ast.CallExpr); ok {
				handler = c.Fun // a handler built by a call, handleCapabilityServerAction("remove")
			}
			var in handlerInputs
			inputsOf(calleeName(handler), map[string]bool{}, &in)
			slices.Sort(in.queries)
			out[pattern] = in
			return true
		})
	}
	if len(out) == 0 {
		t.Fatal("found no s.route call in pkg/ui: the source no longer reads the way routeHandlerInputs expects")
	}
	return out
}

func calleeName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	case *ast.IndexExpr: // an instantiated generic, readAttachmentJSON[T]
		return calleeName(f.X)
	}
	return ""
}

// takesRequest says one of fns has an *http.Request or url.Values
// parameter: a helper a handler can hand its body or query to.
func takesRequest(fns []*ast.FuncDecl) bool {
	for _, fn := range fns {
		for _, p := range fn.Type.Params.List {
			if t := types.ExprString(p.Type); t == "*http.Request" || t == "url.Values" {
				return true
			}
		}
	}
	return false
}

// queryValuesIn is the names fn binds to a request's query: q :=
// r.URL.Query(), or a url.Values parameter.
func queryValuesIn(fn *ast.FuncDecl) map[string]bool {
	names := map[string]bool{}
	for _, p := range fn.Type.Params.List {
		if types.ExprString(p.Type) == "url.Values" {
			for _, n := range p.Names {
				names[n.Name] = true
			}
		}
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if a, ok := n.(*ast.AssignStmt); ok && len(a.Lhs) == len(a.Rhs) {
			for i, rhs := range a.Rhs {
				if id, ok := a.Lhs[i].(*ast.Ident); ok && isQuery(rhs, nil) {
					names[id.Name] = true
				}
			}
		}
		return true
	})
	return names
}

// isQuery says x is r.URL.Query() or one of the names bound to it.
func isQuery(x ast.Expr, names map[string]bool) bool {
	if id, ok := x.(*ast.Ident); ok {
		return names[id.Name]
	}
	call, ok := x.(*ast.CallExpr)
	return ok && types.ExprString(call.Fun) == "r.URL.Query"
}

// declaredType is the type of the variable v (&req, say) as fn declares
// it: by var, by := of a composite literal, or as a parameter.
func declaredType(fn *ast.FuncDecl, v ast.Expr) string {
	if u, ok := v.(*ast.UnaryExpr); ok && u.Op == token.AND {
		v = u.X
	}
	id, ok := v.(*ast.Ident)
	if !ok {
		return "?" + types.ExprString(v)
	}
	found := ""
	ast.Inspect(fn, func(n ast.Node) bool {
		if found != "" {
			return false
		}
		switch d := n.(type) {
		case *ast.ValueSpec:
			for _, name := range d.Names {
				if name.Name == id.Name && d.Type != nil {
					found = types.ExprString(d.Type)
				}
			}
		case *ast.Field:
			for _, name := range d.Names {
				if name.Name == id.Name {
					found = strings.TrimPrefix(types.ExprString(d.Type), "*")
				}
			}
		case *ast.AssignStmt:
			for i, lhs := range d.Lhs {
				if l, ok := lhs.(*ast.Ident); ok && l.Name == id.Name && i < len(d.Rhs) {
					rhs := d.Rhs[i]
					if u, ok := rhs.(*ast.UnaryExpr); ok && u.Op == token.AND {
						rhs = u.X
					}
					if lit, ok := rhs.(*ast.CompositeLit); ok {
						found = types.ExprString(lit.Type)
					}
				}
			}
		}
		return true
	})
	if found == "" {
		return "?" + id.Name
	}
	return found
}

// capabilitiesAdminBodies is every request body a capabilities:admin
// route reads, with each of its top-level keys sorted into those that
// name a grant (which agentGrantEscalation must refuse an agent's
// escalation through) and those that do not. A route that takes a new
// body type, or a body that gains a key, fails
// TestCapabilitiesAdminGrantKeysAreScanned until it is sorted here.
var capabilitiesAdminBodies = map[string]struct {
	typ       reflect.Type
	grantKeys []string
	// otherKeys name no grant a task is given: a capability server's
	// id is a capability_id, which grants nothing until a task holds a
	// reference to it; a policy only admits or denies what a task was
	// already granted.
	otherKeys []string
}{
	"setCapabilityRequest": {reflect.TypeFor[setCapabilityRequest](),
		[]string{"id", "capability", "grant", "replace"}, []string{"attach", "with"}},
	"SetRepoCapabilitiesRequest": {reflect.TypeFor[SetRepoCapabilitiesRequest](),
		[]string{"defaultCapabilities"}, nil},
	"setRepoCICapabilitiesRequest": {reflect.TypeFor[setRepoCICapabilitiesRequest](),
		[]string{"ciCapabilities"}, nil},
	"CapabilityPolicyBody": {reflect.TypeFor[CapabilityPolicyBody](), nil, []string{"policy"}},
	"SetCapabilityServerRequest": {reflect.TypeFor[SetCapabilityServerRequest](),
		nil, []string{"id", "config", "configSecrets", "renameFrom"}},
	"CapabilityServerIDRequest": {reflect.TypeFor[CapabilityServerIDRequest](), nil, []string{"id"}},
	"BindCapabilityServerSecretRequest": {reflect.TypeFor[BindCapabilityServerSecretRequest](),
		nil, []string{"id", "kind", "name", "secret", "prefix", "value"}},
	"TestCapabilityServerRequest": {reflect.TypeFor[TestCapabilityServerRequest](),
		nil, []string{"id", "config", "configSecrets"}},
	"TestHostScriptRequest": {reflect.TypeFor[TestHostScriptRequest](), nil, []string{"id", "script", "argv"}},
	"TestGChatPostRequest":  {reflect.TypeFor[TestGChatPostRequest](), nil, []string{"id", "space"}},
	// A scope set is a definition: declaring one hands it to no task, but
	// editing one already granted widens every grant of it. set is not a
	// grant name, so it is sorted here; grantBodies reads it as the scopes
	// it resolves to, and TestAnAgentCannotWriteAScopeSetPastItsOwnGrants
	// holds that.
	"PutAPIScopeSetRequest":    {reflect.TypeFor[PutAPIScopeSetRequest](), nil, []string{"set"}},
	"DeleteAPIScopeSetRequest": {reflect.TypeFor[DeleteAPIScopeSetRequest](), nil, []string{"name"}},
	"CreateExternalAgentTokenRequest": {reflect.TypeFor[CreateExternalAgentTokenRequest](),
		nil, []string{"name", "scopeSet", "scopes", "repos", "expiresAt", "rateLimit", "maxConcurrent"}},
}

// capabilitiesAdminBodylessRoutes are capabilities:admin routes that act by URL
// path parameter (or on all credentials) without decoding a JSON request body.
var capabilitiesAdminBodylessRoutes = map[string]bool{
	"POST /api/external-agents/tokens/{id}/rotate": true,
	"POST /api/external-agents/tokens/{id}/revoke": true,
	"DELETE /api/external-agents/tokens/{id}":      true,
	"POST /api/external-agents/tokens/revoke-all":  true,
}

// TestCapabilitiesAdminGrantKeysAreScanned holds agentGrantEscalation to
// every key a capabilities:admin route takes a grant by: under each, a
// capability the acting task does not hold, and a dangerous api grant it
// does, are refused, and a grant it holds is not. The key lists are
// checked against the request types the routes' handlers decode
// (routeHandlerInputs) and those types' own fields, so neither a new
// route nor a new field can add an unscanned key silently.
func TestCapabilitiesAdminGrantKeysAreScanned(t *testing.T) {
	inputs := routeHandlerInputs(t)
	held := []string{"api:capabilities:admin", "api:tasks:read", "gcp-key", "self-upgrade"}
	for pattern, scope := range routeScopes {
		if scope != "capabilities:admin" {
			continue
		}
		bodies := inputs[pattern].bodies
		if len(bodies) == 0 {
			if capabilitiesAdminBodylessRoutes[pattern] {
				continue
			}
			t.Errorf("%s reads no body this test can see: sort its keys in capabilitiesAdminBodies", pattern)
		}
		for _, name := range bodies {
			body, ok := capabilitiesAdminBodies[name]
			if !ok {
				t.Errorf("%s reads a %s, which capabilitiesAdminBodies does not sort into grant and other keys", pattern, name)
				continue
			}
			fields := map[string]reflect.Type{}
			for i := 0; i < body.typ.NumField(); i++ {
				f := body.typ.Field(i)
				key, _, _ := strings.Cut(f.Tag.Get("json"), ",")
				if key == "" {
					key = f.Name
				}
				fields[key] = f.Type
			}
			sorted := append(slices.Clone(body.grantKeys), body.otherKeys...)
			for key := range fields {
				if !slices.Contains(sorted, key) {
					t.Errorf("%s has a key %q that capabilitiesAdminBodies does not sort", name, key)
				}
			}
			for _, key := range sorted {
				if _, ok := fields[key]; !ok {
					t.Errorf("capabilitiesAdminBodies sorts %q of %s, which has no such key", key, name)
				}
			}
			for _, key := range body.grantKeys {
				encode := func(grant string) []byte {
					var v any = grant
					if fields[key].Kind() == reflect.Slice {
						v = []string{grant}
					}
					data, _ := json.Marshal(map[string]any{key: v, "attach": true})
					return data
				}
				for _, grant := range []string{"self-debug", "api:deployment:upgrade", "api:capabilities:admin", "self-upgrade",
					"api:tasks:write", "api:secrets:write@acme/widgets"} {
					if agentGrantEscalation(held, pattern, encode(grant), nil) == nil {
						t.Errorf("%s let an agent grant %s under %q", pattern, grant, key)
					}
				}
				for _, grant := range []string{"api:tasks:read", "gcp-key"} {
					if err := agentGrantEscalation(held, pattern, encode(grant), nil); err != nil {
						t.Errorf("%s refused an agent %s under %q, which it holds: %v", pattern, grant, key, err)
					}
				}
			}
		}
	}
}

// A scope set is granted by name and resolved when used, so writing one
// is granting what it resolves to: an agent holding capabilities:admin
// may write a set only of scopes it holds itself, none dangerous -- not
// widen a set it (or anyone) holds into deployment:reboot.
func TestAnAgentCannotWriteAScopeSetPastItsOwnGrants(t *testing.T) {
	t.Cleanup(func() { _ = model.InstallAPIScopeSets(nil) })
	if err := model.InstallAPIScopeSets([]model.APIScopeSet{
		{Name: "ops/read", Scopes: []string{"tasks:read"}},
		{Name: "ops/wide", Includes: []string{"ops/read"}},
	}); err != nil {
		t.Fatal(err)
	}
	held := []string{"api:capabilities:admin", "api:ops/read", "api:ci:read@acme/widgets"}
	const pattern = "PUT /api/api-scope-sets"
	put := func(set string) error {
		return agentGrantEscalation(held, pattern, []byte(`{"set":`+set+`}`), nil)
	}
	for _, set := range []string{
		`{"name":"ops/read","scopes":["tasks:read","deployment:reboot"]}`,
		`{"name":"ops/read","scopes":["tasks:read","tasks:write"]}`,
		`{"name":"ops/read","scopes":["secrets:*"]}`,
		`{"name":"ops/new","includes":["grain/task-admin"]}`,
		`{"name":"ops/new","scopes":["ci:read"]}`,
		`{"name":"ops/new","scopes":["ci:read"],"repos":["acme/other"]}`,
		`{"name":"ops/new","includes":["no/such-set"]}`,
		`{"name":"grain/task-admin","scopes":["tasks:read"]}`,
	} {
		if put(set) == nil {
			t.Errorf("an agent wrote the scope set %s, beyond what it holds", set)
		}
	}
	for _, set := range []string{
		`{"name":"ops/read","scopes":["tasks:read"],"description":"reworded"}`,
		`{"name":"ops/new","scopes":["ci:read"],"repos":["acme/widgets"]}`,
		`{"name":" ops/new ","includes":["ops/read"],"requiresPerson":true}`,
	} {
		if err := put(set); err != nil {
			t.Errorf("an agent was refused the scope set %s, all of which it holds: %v", set, err)
		}
	}
	// While the declared sets are not the ones in effect, a write would
	// install sets the gate did not read, so an agent's is refused.
	if model.InstallAPIScopeSets([]model.APIScopeSet{{Name: "ops/read", Includes: []string{"missing"}}}) == nil {
		t.Fatal("a set including a missing one installed")
	}
	if put(`{"name":"ops/read","scopes":["tasks:read"]}`) == nil {
		t.Error("an agent wrote a scope set while the deployment's declared sets were not in effect")
	}
}

// TestAgentGrantRefusalComparesGrants is agentGrantRefusal reading names
// as the grants they are: a spelling of a held grant passes, and an
// unheld grant cannot pass as a spelling of a held one.
func TestAgentGrantRefusalComparesGrants(t *testing.T) {
	ref := "https://github.com/acme/caps/gcp#key"
	held := []string{"api:tasks:read", "api:ci:read@acme/widgets", ref + `={"project":"p"}`, "gcp-key"}
	for _, c := range []struct {
		name string
		ok   bool
	}{
		{" api:tasks:read ", true},
		{"api:tasks:read@Acme/Widgets", true},
		{"api:ci:read@ACME/widgets", true},
		{"api:ci:read", false},
		{"api:ci:read@acme/gadgets", false},
		{"api:Tasks:read", false},
		{"api:task-admin", false},
		{"api:tasks:admin", false},
		{"self-upgrade", false},
		{ref + `={"project":"p"}`, true},
		{ref + `={"project":"q"}`, false},
		{"https://github.com/acme/caps/gcp#other", false},
		{"gcp-key", true},
		{"GCP-KEY", false},
		{"self-debug", false},
	} {
		err := agentGrantRefusal(held, c.name, nil)
		if (err == nil) != c.ok {
			t.Errorf("agentGrantRefusal(%q) = %v, want allowed %v", c.name, err, c.ok)
		}
	}
	for _, c := range []struct {
		body string
		ok   bool
	}{
		{`{"capability":"` + ref + `","with":{"project":"p"},"attach":true}`, true},
		{`{"capability":"` + ref + `","with":{"project":"q"},"attach":true}`, false},
		{`{"grant":"` + ref + `={\"project\":\"q\"}","attach":true}`, false},
	} {
		err := agentGrantEscalation(held, "POST /api/tasks/{id}/capabilities", []byte(c.body), nil)
		if (err == nil) != c.ok {
			t.Errorf("agentGrantEscalation(%s) = %v, want allowed %v", c.body, err, c.ok)
		}
	}
	// A capabilities list's {capability, with} entries are grants too.
	if agentGrantEscalation(held, "PATCH /api/tasks/{id}", []byte(`{"capabilities":[{"capability":"`+ref+`","with":{"project":"q"}}]}`), nil) == nil {
		t.Error("a {capability, with} entry of a capabilities list escaped the check")
	}
}

// A scope set limited to some repositories grants its scopes on those
// alone, so a task holding it may pass it on, and one holding the same
// scopes only on another repository may not. The 2026-10-28 walkthrough
// (docs/test-runs/2026-10-28-unified-grants-scope-sets.md) found the
// first refused: the check read the grant's own @repo, which a set
// granted by name never has.
func TestAgentMayPassOnARepoLimitedScopeSetItHolds(t *testing.T) {
	if err := model.InstallAPIScopeSets([]model.APIScopeSet{{
		Name: "walk-readers", Scopes: []string{"tasks:read", "ci:read"}, Repos: []string{"acme/widgets"},
	}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = model.InstallAPIScopeSets(nil) })
	for _, c := range []struct {
		held []string
		name string
		ok   bool
	}{
		{[]string{"api:walk-readers"}, "api:walk-readers", true},
		{[]string{"api:walk-readers"}, "api:walk-readers@acme/widgets", true},
		{[]string{"api:tasks:read@acme/widgets", "api:ci:read@acme/widgets"}, "api:walk-readers", true},
		{[]string{"api:tasks:read@acme/gadgets", "api:ci:read@acme/gadgets"}, "api:walk-readers", false},
		{[]string{"api:tasks:read@acme/widgets"}, "api:walk-readers", false},
		{[]string{"api:walk-readers"}, "api:tasks:read", false},
		{[]string{"api:walk-readers"}, "api:grain/ci-read", false},
	} {
		err := agentGrantRefusal(c.held, c.name, nil)
		if (err == nil) != c.ok {
			t.Errorf("holding %v, agentGrantRefusal(%q) = %v, want allowed %v", c.held, c.name, err, c.ok)
		}
	}
}

// priority-dispatch lets a task jump the queue, so an agent cannot grant
// it -- not even one whose own task holds it (the pipeline steward).
func TestAnAgentCannotGrantPriorityDispatchEvenHoldingIt(t *testing.T) {
	held := []string{model.PriorityDispatchCapability, "self-debug"}
	if err := agentGrantRefusal(held, model.PriorityDispatchCapability, nil); err == nil ||
		!strings.Contains(err.Error(), "only a person") {
		t.Fatalf("agentGrantRefusal(priority-dispatch) = %v, want it refused as a person's to grant", err)
	}
	if err := agentGrantRefusal(held, "self-debug", nil); err != nil {
		t.Fatalf("agentGrantRefusal(self-debug), which it holds = %v, want allowed", err)
	}
	if agentGrantEscalation(held, "POST /api/tasks/{id}/capabilities",
		[]byte(`{"capability":"priority-dispatch","attach":true}`), nil) == nil {
		t.Error("an agent attached priority-dispatch through the capability pane's route")
	}
}

// durable implies priority-dispatch and holds a sandbox with no deadline,
// so it is a person's to grant in the same way.
func TestAnAgentCannotGrantDurableEvenHoldingIt(t *testing.T) {
	held := []string{model.DurableCapability, "self-debug"}
	if err := agentGrantRefusal(held, model.DurableCapability, nil); err == nil ||
		!strings.Contains(err.Error(), "only a person") {
		t.Fatalf("agentGrantRefusal(durable) = %v, want it refused as a person's to grant", err)
	}
	if agentGrantEscalation(held, "POST /api/tasks/{id}/capabilities",
		[]byte(`{"capability":"durable","attach":true}`), nil) == nil {
		t.Error("an agent attached durable through the capability pane's route")
	}
}

// TestPatchTouchesTaskGrantsCoversPriorityDispatch pins that every
// spelling of the priority-dispatch toggle on PATCH /api/tasks/{id} is a
// grant edit (capabilities:admin and a reason), while the numeric
// priority stays an ordinary task edit.
func TestPatchTouchesTaskGrantsCoversPriorityDispatch(t *testing.T) {
	for _, body := range []string{
		`{"priority": true}`, `{"priority": false}`,
		`{"priorityDispatch": true}`, `{"priorityDispatch": false}`,
		`{"PriorityDispatch": true}`, `{"priority_dispatch": true}`,
	} {
		if !patchTouchesTaskGrants([]byte(body)) {
			t.Errorf("patchTouchesTaskGrants(%s) = false, want true", body)
		}
	}
	for _, body := range []string{`{"priority": 7}`, `{"priority": null}`, `{"title": "x"}`} {
		if patchTouchesTaskGrants([]byte(body)) {
			t.Errorf("patchTouchesTaskGrants(%s) = true, want false", body)
		}
	}
}
