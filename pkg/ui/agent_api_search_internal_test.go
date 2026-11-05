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
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestEditDistance(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"", "", 0}, {"log", "log", 0}, {"log", "logs", 1}, {"lits", "list", 1},
		{"coment", "comment", 1}, {"tasks", "task", 1}, {"abc", "xyz", 3}, {"", "abc", 3},
	} {
		if got := editDistance(tc.a, tc.b); got != tc.want {
			t.Errorf("editDistance(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestNearestToolsSuggestsTheLikelyName(t *testing.T) {
	routes := AgentAPIRoutes()
	for _, tc := range []struct {
		query string
		want  string // among the first three
		first string // the very first, when it matters
	}{
		{query: "api_get_logs", want: "api_get_log_lines"},
		{query: "api_lits_tasks", want: "api_list_tasks", first: "api_list_tasks"},
		{query: "api_comment", want: "api_add_comment", first: "api_add_comment"},
		{query: "api_check_git_connection", want: "api_check_deployment_git_connection", first: "api_check_deployment_git_connection"},
	} {
		got := nearestTools(tc.query, routes, 3)
		if !slices.Contains(got, tc.want) {
			t.Errorf("nearestTools(%q) = %v, want %s among them", tc.query, got, tc.want)
		}
		if tc.first != "" && (len(got) == 0 || got[0] != tc.first) {
			t.Errorf("nearestTools(%q) = %v, want %s first", tc.query, got, tc.first)
		}
	}
	if got := nearestTools("xyzzy", routes, 3); len(got) != 0 {
		t.Errorf("nearestTools(xyzzy) = %v, want nothing: a word near nothing gets no guess", got)
	}
	if got := nearestTools("api_", routes, 3); len(got) != 0 {
		t.Errorf("nearestTools(api_) = %v, want nothing: the prefix alone names nothing", got)
	}
}

func TestRouteMatchesToolQueryByWord(t *testing.T) {
	byTool := map[string]APIRoute{}
	for _, rt := range AgentAPIRoutes() {
		byTool[rt.Tool] = rt
	}
	for _, tc := range []struct {
		tool, query string
		want        bool
	}{
		{"api_list_access_principals", "access", true},                  // a path segment
		{"api_add_comment", "comment", true},                            // a word of the tool name
		{"api_check_deployment_git_connection", "git connection", true}, // two words
		{"api_submit", "merge queue", true},                             // words only the summary carries
		{"api_submit", "merge queue xyzzy", false},                      // every word must be found
		{"api_list_tasks", "list_tasks", true},                          // a name without its prefix
		{"api_list_tasks", "api_list_tasks", true},
		{"api_add_comment", "access", false},
	} {
		rt, ok := byTool[tc.tool]
		if !ok {
			t.Fatalf("no route tool %s", tc.tool)
		}
		if got := routeMatchesToolQuery(rt, tc.query); got != tc.want {
			t.Errorf("routeMatchesToolQuery(%s, %q) = %v, want %v (summary %q)", tc.tool, tc.query, got, tc.want, rt.Summary)
		}
	}
}

// The index of every route fits in one explore answer, under the budget
// the tool shrinks its views to; a group's compact listing fits too.
func TestExploreIndexOfEveryRouteFitsTheBudget(t *testing.T) {
	access := mcpCallerAccess{fullAccess: true}
	authorized := access.authorizedRoutes()
	res := renderScopeDetail(access, authorized, exploreQuery{})
	if res.IsError {
		t.Fatalf("explore(all): %s", res.Text)
	}
	if len(res.Text) > maxExploreResultBytes {
		t.Errorf("explore(all) is %d bytes, over the %d byte budget: the index view must list every route in one answer", len(res.Text), maxExploreResultBytes)
	}
	var got struct {
		View       string              `json:"view"`
		RouteCount int                 `json:"routeCount"`
		Index      map[string][]string `json:"index"`
	}
	if err := json.Unmarshal([]byte(res.Text), &got); err != nil {
		t.Fatal(err)
	}
	if got.View != exploreViewIndex {
		t.Errorf("explore(all) view = %q, want %q", got.View, exploreViewIndex)
	}
	want := 0
	for _, rt := range authorized {
		if rt.AliasOf == "" {
			want++
		}
	}
	listed := 0
	for _, names := range got.Index {
		listed += len(names)
	}
	if listed != want || got.RouteCount != want {
		t.Errorf("explore(all) lists %d tools (routeCount %d), want the %d non-alias routes", listed, got.RouteCount, want)
	}
	for _, group := range authorizedGroups(authorized) {
		res := renderScopeDetail(access, authorized, exploreQuery{scope: group, view: exploreViewCompact})
		if res.IsError || len(res.Text) > maxExploreResultBytes {
			t.Errorf("explore(%s, compact) is %d bytes (error %v), want under %d", group, len(res.Text), res.IsError, maxExploreResultBytes)
		}
	}
}

func TestRouteOneLinerIsOneShortSentence(t *testing.T) {
	for _, rt := range AgentAPIRoutes() {
		line := routeOneLiner(rt)
		if line == "" || len(line) > maxExploreSummaryChars || strings.Contains(line, "\n") {
			t.Errorf("%s: one-liner %q is empty, over %d characters or more than one line", rt.Tool, line, maxExploreSummaryChars)
		}
		if strings.Contains(line, "through the same handler") {
			t.Errorf("%s: one-liner %q carries the handler boilerplate", rt.Tool, line)
		}
	}
}

// Every per-field alias names a route that exists and a field that the
// whole-settings routes read and write; and the per-field routes the
// table names all exist, so a renamed route does not leave a stale
// entry silently matching nothing.
func TestRepoSettingsAliasesAreTotal(t *testing.T) {
	routes := AgentAPIRoutes()
	byTool := map[string]APIRoute{}
	for _, rt := range routes {
		byTool[rt.Tool] = rt
	}
	jsonNames := func(t reflect.Type) map[string]bool {
		out := map[string]bool{}
		for i := 0; i < t.NumField(); i++ {
			name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
			if name != "" && name != "-" {
				out[name] = true
			}
		}
		return out
	}
	readable := jsonNames(reflect.TypeOf(RepoDefaults{}))
	writable := jsonNames(reflect.TypeOf(SetRepoSettingsRequest{}))
	seen := map[string]int{}
	for _, rt := range routes {
		if rt.AliasOf == "" {
			continue
		}
		canonical, ok := byTool[rt.AliasOf]
		if !ok {
			t.Errorf("%s is an alias of %s, which is no route", rt.Tool, rt.AliasOf)
			continue
		}
		if canonical.Method != rt.Method {
			t.Errorf("%s (%s) is an alias of %s (%s): the methods differ", rt.Tool, rt.Method, canonical.Tool, canonical.Method)
		}
		if rt.Method == "GET" && !readable[rt.AliasField] {
			t.Errorf("%s reads %s, which GET .../settings (RepoDefaults) does not report", rt.Tool, rt.AliasField)
		}
		if rt.Method == "PUT" && !writable[rt.AliasField] {
			t.Errorf("%s writes %s, which PUT .../settings (SetRepoSettingsRequest) does not take", rt.Tool, rt.AliasField)
		}
		seen[strings.TrimPrefix(rt.Path, repoSettingsPathPrefix)]++
	}
	for suffix := range repoSettingsFieldRoutes {
		if seen[suffix] != 2 {
			t.Errorf("repoSettingsFieldRoutes names %s, of which %d routes exist, want a GET and a PUT", suffix, seen[suffix])
		}
	}
}

// routeFieldDocs describes fields of routes that exist, in bodies that
// have them.
func TestRouteFieldDocsNameRealFields(t *testing.T) {
	for pattern, docs := range routeFieldDocs {
		body, ok := routeBodies[pattern]
		if !ok {
			t.Errorf("routeFieldDocs describes %s, which declares no body", pattern)
			continue
		}
		schema := jsonSchemaOf(reflect.TypeOf(body), 0)
		props, _ := schema["properties"].(map[string]any)
		for field, doc := range docs {
			if _, ok := props[field]; !ok {
				t.Errorf("routeFieldDocs describes %s's %s, which %T has no field for", pattern, field, body)
			}
			if strings.TrimSpace(doc) == "" {
				t.Errorf("routeFieldDocs gives %s's %s no description", pattern, field)
			}
		}
	}
}

// The git connection check says, in its description and its schema, how
// to check an unsaved connection and how a saved one: a caller working
// from the schema alone put "ambient" in credential, which is only ever
// a saved credential's name, and was told that no such credential existed.
func TestGitConnectionCheckSchemaSaysWhatUnsavedMeans(t *testing.T) {
	var route *APIRoute
	for _, rt := range AgentAPIRoutes() {
		if rt.Pattern == "POST /api/deployment/git-connections/check" {
			rt := rt
			route = &rt
		}
	}
	if route == nil {
		t.Fatal("no route for POST /api/deployment/git-connections/check")
	}
	if route.Tool != "api_check_deployment_git_connection" {
		t.Errorf("tool = %s, want api_check_deployment_git_connection", route.Tool)
	}
	for _, want := range []string{"unsaved", "leave credential empty", "ambient", "saved", "never a type"} {
		if !strings.Contains(route.Description, want) {
			t.Errorf("description lacks %q: %s", want, route.Description)
		}
	}
	body := route.inputSchema()["properties"].(map[string]any)["body"].(map[string]any)
	fields := body["properties"].(map[string]any)
	for field, want := range map[string][]string{
		"credential": {"SAVED", "Leave it empty", "never a type"},
		"type":       {"ambient", "leave credential empty", "pat", "app"},
		"value":      {"unsaved", "PAT"},
	} {
		desc, _ := fields[field].(map[string]any)["description"].(string)
		for _, w := range want {
			if !strings.Contains(desc, w) {
				t.Errorf("body.%s description lacks %q: %q", field, w, desc)
			}
		}
	}
}
