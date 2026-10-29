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

package protocol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseRef(t *testing.T) {
	good := map[string]Ref{
		"https://github.com/bwsalmon/grain/capabilities/gcp#gcp-key": {"https://github.com/bwsalmon/grain/capabilities/gcp", "gcp-key"},
		"https://github.com/bwsalmon/grain/capabilities/self-debug":  {"https://github.com/bwsalmon/grain/capabilities/self-debug", ""},
		"https://cap.example.com:8443/x#a_b-1":                       {"https://cap.example.com:8443/x", "a_b-1"},
	}
	for in, want := range good {
		got, err := ParseRef(in)
		if err != nil || got != want {
			t.Errorf("ParseRef(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	for _, in := range []string{
		"http://github.com/x",
		"https://github.com",
		"https://github.com/",
		"https://github.com/x/",
		"https://github.com/x?y=1",
		"https://github.com//x",
		"https://github.com/./x",
		"https://github.com/a/../x",
		"https://user@github.com/x",
		"https://github.com/x#Upper",
		"https://github.com/x#-lead",
		"https://github.com/x#",
	} {
		if _, err := ParseRef(in); err == nil {
			t.Errorf("ParseRef(%q) accepted a reference §2.1 forbids", in)
		}
	}
}

func TestShorthandResolution(t *testing.T) {
	id := "https://example.com/caps/self-debug"
	one := map[string]*MethodInfo{"other": {Name: "other"}}
	two := map[string]*MethodInfo{"self-debug": {}, "b": {}}
	many := map[string]*MethodInfo{"a": {}, "b": {}}

	if r, err := resolveMethod(Ref{CapabilityID: id}, two); err != nil || r.Method != "self-debug" {
		t.Errorf("last-segment rule: %+v, %v", r, err)
	}
	if r, err := resolveMethod(Ref{CapabilityID: id}, one); err != nil || r.Method != "other" {
		t.Errorf("single-method rule: %+v, %v", r, err)
	}
	if _, err := resolveMethod(Ref{CapabilityID: id}, many); err == nil || !strings.Contains(err.Error(), "#a, #b") {
		t.Errorf("ambiguous reference must be rejected listing the methods, got %v", err)
	}
	if _, err := resolveMethod(Ref{CapabilityID: id, Method: "zzz"}, many); err == nil {
		t.Errorf("an unadvertised method must be rejected")
	}
}

func TestValidateArgs(t *testing.T) {
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"repo"},
		"properties": map[string]any{
			"repo":    map[string]any{"type": "string", "pattern": "^[a-z]+/[a-z]+$"},
			"mode":    map[string]any{"type": "string", "enum": []any{"read", "write"}, "default": "read"},
			"root":    map[string]any{"type": "boolean", "default": true},
			"depth":   map[string]any{"type": "integer", "minimum": 1, "maximum": 10},
			"name":    map[string]any{"type": "string", "minLength": 2, "maxLength": 4},
			"tags":    map[string]any{"type": "array", "items": map[string]any{"type": "string", "const": "x"}},
			"account": map[string]any{"$ref": "#/$defs/Account"},
		},
		"$defs": map[string]any{
			"Account": map[string]any{"type": "string", "enum": []any{"staging", "prod"}, "default": "staging"},
		},
	}
	got, err := ValidateArgs(schema, map[string]any{"repo": "bw/grain", "depth": 3})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"repo": "bw/grain", "mode": "read", "root": true, "depth": float64(3), "account": "staging"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("normalized = %v, want %v", got, want)
	}
	bad := []map[string]any{
		{},
		{"repo": "BW/grain"},
		{"repo": "bw/grain", "mode": "admin"},
		{"repo": "bw/grain", "root": "yes"},
		{"repo": "bw/grain", "depth": 1.5},
		{"repo": "bw/grain", "depth": 11},
		{"repo": "bw/grain", "name": "a"},
		{"repo": "bw/grain", "name": "abcde"},
		{"repo": "bw/grain", "tags": []any{"x", "y"}},
		{"repo": "bw/grain", "account": "dev"},
		{"repo": "bw/grain", "extra": 1},
	}
	for _, with := range bad {
		if _, err := ValidateArgs(schema, with); err == nil {
			t.Errorf("ValidateArgs accepted %v", with)
		}
	}
	if _, err := ValidateArgs(nil, map[string]any{"x": 1}); err == nil {
		t.Errorf("an absent args schema must accept no arguments")
	}
	if !ArgsHaveNoRequirement(nil) || ArgsHaveNoRequirement(schema) {
		t.Errorf("ArgsHaveNoRequirement is wrong")
	}
}

// A schema written as Go literals, the way a MethodSpec's Args naturally
// is, must be enforced exactly as its JSON would be.
func TestValidateArgsEnforcesGoLiteralSchemas(t *testing.T) {
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"repo"},
		"properties": map[string]map[string]any{
			"repo": {"type": []string{"string"}},
			"mode": {"enum": []string{"read"}},
			"tags": {"type": "array", "items": map[string]any{"enum": []string{"x"}}},
		},
	}
	for _, with := range []map[string]any{
		{"mode": "read"},
		{"repo": 1},
		{"repo": "a", "mode": "write"},
		{"repo": "a", "tags": []string{"y"}},
	} {
		if _, err := ValidateArgs(schema, with); err == nil {
			t.Errorf("ValidateArgs accepted %v", with)
		}
	}
	if _, err := ValidateArgs(schema, map[string]any{"repo": "a", "mode": "read", "tags": []string{"x"}}); err != nil {
		t.Error(err)
	}
}

func TestValidateArgsComposition(t *testing.T) {
	// What pydantic emits for repo: Optional[Literal-ish str] = None.
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"repo": map[string]any{
				"anyOf":   []any{map[string]any{"type": "string", "pattern": "^bwsalmon/grain$"}, map[string]any{"type": "null"}},
				"default": nil,
				"title":   "Repo",
			},
			"n":    map[string]any{"type": "number", "exclusiveMinimum": 0, "exclusiveMaximum": 10},
			"one":  map[string]any{"oneOf": []any{map[string]any{"type": "integer"}, map[string]any{"type": "number", "minimum": 5}}},
			"both": map[string]any{"allOf": []any{map[string]any{"type": "string"}, map[string]any{"maxLength": 3}}},
			"not":  map[string]any{"type": "string", "not": map[string]any{"const": "root"}},
			"list": map[string]any{"type": "array", "minItems": 1, "maxItems": 2, "uniqueItems": true},
			"ref":  map[string]any{"$ref": "#/$defs/S", "maxLength": 2},
		},
		"$defs": map[string]any{"S": map[string]any{"type": "string"}},
	}
	for _, with := range []map[string]any{
		{"repo": "bwsalmon/grain"},
		{"repo": nil},
		{"n": 9.5},
		{"one": 1},
		{"one": 6.5},
		{"both": "abc"},
		{"not": "user"},
		{"list": []any{1, 2}},
		{"ref": "ab"},
	} {
		if _, err := ValidateArgs(schema, with); err != nil {
			t.Errorf("ValidateArgs refused %v: %v", with, err)
		}
	}
	for _, with := range []map[string]any{
		{"repo": "evil/other"},
		{"n": 0},
		{"n": 10},
		{"one": 6},
		{"one": 1.5},
		{"both": "abcd"},
		{"not": "root"},
		{"list": []any{}},
		{"list": []any{1, 2, 3}},
		{"list": []any{1, 1}},
		{"ref": "abc"},
		{"ref": 1},
	} {
		if _, err := ValidateArgs(schema, with); err == nil {
			t.Errorf("ValidateArgs accepted %v", with)
		}
	}
}

// A keyword the validator does not enforce must refuse the schema, not
// be skipped: skipping it would turn a restrictive schema into an open one.
func TestUnsupportedSchemaKeywordsAreRefused(t *testing.T) {
	for name, prop := range map[string]map[string]any{
		"patternProperties": {"type": "object", "patternProperties": map[string]any{"^x": map[string]any{"type": "string"}}},
		"propertyNames":     {"type": "object", "propertyNames": map[string]any{"pattern": "^x"}},
		"multipleOf":        {"type": "number", "multipleOf": 2},
		"if":                {"if": map[string]any{"type": "string"}},
		"tuple items":       {"type": "array", "items": []any{map[string]any{"type": "string"}}},
		"unknown type":      {"type": "str"},
		"bad pattern":       {"type": "string", "pattern": "("},
		"misspelled":        {"type": "string", "maxlength": 3},
		"nested in anyOf":   {"anyOf": []any{map[string]any{"type": "string", "contains": map[string]any{}}}},
	} {
		schema := map[string]any{"type": "object", "properties": map[string]any{"v": prop}}
		if _, err := ValidateArgs(schema, map[string]any{}); err == nil {
			t.Errorf("%s: ValidateArgs accepted the schema", name)
		}
		if _, err := Discover([]Tool{mintTool("a", schema)}); err == nil {
			t.Errorf("%s: Discover accepted the schema", name)
		}
		if _, err := NewMethod(MethodSpec{Name: "a", Args: schema, Mint: func(context.Context, string, time.Time, map[string]any) (*MintResponse, error) {
			return nil, nil
		}}); err == nil {
			t.Errorf("%s: NewMethod accepted the schema", name)
		}
	}
}

func TestSelfReferentialSchemaIsAnErrorNotACrash(t *testing.T) {
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"v": map[string]any{"$ref": "#/$defs/L"}},
		"$defs":      map[string]any{"L": map[string]any{"allOf": []any{map[string]any{"$ref": "#/$defs/L"}}}},
	}
	if _, err := ValidateArgs(schema, map[string]any{"v": 1}); err == nil {
		t.Error("a $ref loop was accepted")
	}
}

func mintTool(name string, args map[string]any) Tool {
	props := map[string]any{"attempt_id": map[string]any{"type": "string"}}
	if args != nil {
		props["args"] = args
	}
	return Tool{Name: "mint_" + name, InputSchema: map[string]any{
		"type": "object", "required": []any{"attempt_id", "deadline", "args"}, "properties": props,
	}}
}

func releaseTool(name string) Tool {
	return Tool{Name: "release_" + name, InputSchema: map[string]any{"type": "object", "required": []any{"attempt_id"}}}
}

func TestDiscover(t *testing.T) {
	methods, err := Discover([]Tool{mintTool("gcp-key", nil), releaseTool("gcp-key"), mintTool("skill", nil), {Name: "unrelated"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(methods) != 2 || !methods["gcp-key"].Releasable || methods["skill"].Releasable {
		t.Errorf("methods = %+v", methods)
	}
	for name, tools := range map[string][]Tool{
		"bad suffix":       {mintTool("Bad", nil)},
		"duplicate mint":   {mintTool("a", nil), mintTool("a", nil)},
		"orphan release":   {releaseTool("a")},
		"mint schema":      {{Name: "mint_a", InputSchema: map[string]any{"type": "object"}}},
		"release schema":   {mintTool("a", nil), {Name: "release_a"}},
		"args not object":  {mintTool("a", map[string]any{"type": "string"})},
		"duplicate releas": {mintTool("a", nil), releaseTool("a"), releaseTool("a")},
	} {
		if _, err := Discover(tools); err == nil {
			t.Errorf("%s: Discover accepted it", name)
		}
	}
}

func TestDiscoverCarriesRootDefsToArgs(t *testing.T) {
	tool := mintTool("a", map[string]any{"type": "object", "properties": map[string]any{"v": map[string]any{"$ref": "#/$defs/V"}}})
	tool.InputSchema["$defs"] = map[string]any{"V": map[string]any{"type": "string", "enum": []any{"ok"}}}
	methods, err := Discover([]Tool{tool})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateArgs(methods["a"].ArgsSchema, map[string]any{"v": "ok"}); err != nil {
		t.Error(err)
	}
	if _, err := ValidateArgs(methods["a"].ArgsSchema, map[string]any{"v": "no"}); err == nil {
		t.Error("$ref into the root's $defs was not enforced")
	}
}

func TestParseGrant(t *testing.T) {
	g, err := ParseGrant(json.RawMessage(`"https://x.com/a#m"`))
	if err != nil || g.Capability != "https://x.com/a#m" || !g.bare {
		t.Errorf("bare: %+v %v", g, err)
	}
	g, err = ParseGrant(json.RawMessage(`{"capability":"https://x.com/a","with":{"k":"v"}}`))
	if err != nil || g.With["k"] != "v" || g.bare {
		t.Errorf("object: %+v %v", g, err)
	}
	for _, bad := range []string{`{"capability":"https://x.com/a","extra":1}`, `{"with":{}}`, `3`, `{"capability":"https://x.com/a","with":[]}`} {
		if _, err := ParseGrant(json.RawMessage(bad)); err == nil {
			t.Errorf("ParseGrant(%s) accepted it", bad)
		}
	}
	g, err = ParseCLIGrant(`https://x.com/gcp#gcp-key={"account":"infra-staging"}`)
	if err != nil || g.Capability != "https://x.com/gcp#gcp-key" || g.With["account"] != "infra-staging" {
		t.Errorf("cli: %+v %v", g, err)
	}
	if _, err := ParseCLIGrant(`https://x.com/gcp#gcp-key=[1]`); err == nil {
		t.Errorf("cli grant with non-object JSON accepted")
	}
}

func testServer(id string) *Server {
	return &Server{ID: id, Methods: map[string]*MethodInfo{
		"git-repo": {Name: "git-repo", Releasable: true, ArgsSchema: map[string]any{
			"type": "object", "additionalProperties": false, "required": []any{"repo"},
			"properties": map[string]any{
				"repo": map[string]any{"type": "string"},
				"root": map[string]any{"type": "boolean", "default": true},
			},
		}},
		"skill": {Name: "skill"},
	}}
}

func TestNormalizeDedupesAndRefusesUnmapped(t *testing.T) {
	id := "https://example.com/caps/github"
	srv := testServer(id)
	lookup := func(cid string) (*Server, error) {
		if cid != id {
			return nil, fmt.Errorf("capability %s is not configured", cid)
		}
		return srv, nil
	}
	grants, err := Normalize([]GrantSpec{
		{Capability: id + "#git-repo", With: map[string]any{"repo": "a/b"}},
		{Capability: id + "#git-repo", With: map[string]any{"repo": "a/b", "root": true}},
		{Capability: id + "#git-repo", With: map[string]any{"repo": "c/d", "root": false}},
		BareGrant(id + "#skill"),
	}, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != 3 || grants[1].Args["repo"] != "c/d" || grants[2].Ref.Method != "skill" {
		t.Errorf("grants = %+v", grants)
	}
	if !grants[0].Releasable || grants[2].Releasable {
		t.Errorf("Releasable not carried from the method")
	}
	if _, err := Normalize([]GrantSpec{BareGrant(id + "#git-repo")}, lookup); err == nil || !strings.Contains(err.Error(), "{capability, with}") {
		t.Errorf("a bare grant of a method with required args must be refused, got %v", err)
	}
	if _, err := Normalize([]GrantSpec{BareGrant("https://other.example.com/x")}, lookup); err == nil {
		t.Errorf("an unmapped capability_id must be refused")
	}

	child, _ := Normalize([]GrantSpec{{Capability: id + "#git-repo", With: map[string]any{"repo": "a/b"}}, {Capability: id + "#git-repo", With: map[string]any{"repo": "x/y"}}}, lookup)
	unc := Uncovered(grants, child)
	if len(unc) != 1 || unc[0].Args["repo"] != "x/y" {
		t.Errorf("Uncovered = %+v", unc)
	}
}

func TestAttemptID(t *testing.T) {
	if id, err := AttemptID("e1010c47", "175", 2, 0); err != nil || id != "e1010c47-175-2-0" {
		t.Errorf("AttemptID = %q, %v", id, err)
	}
	if _, err := AttemptID("bad/deployment", "1", 1, 0); err == nil {
		t.Errorf("a non-conforming attempt id was formed")
	}
}

func TestParseServers(t *testing.T) {
	servers, err := ParseServers([]byte(`{"mcpServers": {
		"https://github.com/bwsalmon/grain/capabilities/gcp": {"command": "grain", "args": ["capability", "gcp"], "env": {"K": "$secret:gcp-minter-key"}},
		"https://github.com/my-org/capabilities/custom-plugin": {"command": "plugins/custom-plugin/server"},
		"https://github.com/my-org/capabilities/external-cap": {"serverUrl": "http://127.0.0.1:9802/mcp", "headers": {"Authorization": "Bearer $secret:external-cap-token"}, "disabled": true}
	}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 3 || !servers["https://github.com/my-org/capabilities/external-cap"].Disabled {
		t.Errorf("servers = %+v", servers)
	}
	for name, doc := range map[string]string{
		"both":       `{"https://x.com/a": {"command": "a", "serverUrl": "http://h/mcp"}}`,
		"neither":    `{"https://x.com/a": {}}`,
		"mixed":      `{"https://x.com/a": {"serverUrl": "http://h/mcp", "env": {"A": "b"}}}`,
		"headers":    `{"https://x.com/a": {"command": "a", "headers": {"A": "b"}}}`,
		"escape":     `{"https://x.com/a": {"command": "plugins/../../etc/x"}}`,
		"unclean":    `{"https://x.com/a": {"command": "/usr//bin/x"}}`,
		"bad key":    `{"http://x.com/a": {"command": "a"}}`,
		"bad url":    `{"https://x.com/a": {"serverUrl": "ftp://h/mcp"}}`,
		"unknown":    `{"https://x.com/a": {"command": "a", "url": "http://h"}}`,
		"empty ref":  `{"https://x.com/a": {"command": "a", "env": {"A": "$secret:"}}}`,
		"userinfo":   `{"https://x.com/a": {"serverUrl": "http://u:p@h/mcp"}}`,
		"bad envkey": `{"https://x.com/a": {"command": "a", "env": {"A=B": "c"}}}`,
	} {
		if _, err := ParseServers([]byte(doc)); err == nil {
			t.Errorf("%s: ParseServers accepted %s", name, doc)
		}
	}
	bin, err := servers["https://github.com/my-org/capabilities/custom-plugin"].resolveCommand("/srv/grain-config")
	if err != nil || bin != "/srv/grain-config/plugins/custom-plugin/server" {
		t.Errorf("relative command resolved to %q, %v", bin, err)
	}
}

func TestResolveSecrets(t *testing.T) {
	cfg := ServerConfig{ServerURL: "http://h/mcp", Headers: map[string]string{"Authorization": "Bearer $secret:tok", "X": "plain"}}
	store := map[string]string{"tok": "s3cret"}
	lookup := func(n string) (string, bool) { v, ok := store[n]; return v, ok }
	r, err := cfg.Resolve(lookup)
	if err != nil || r.Headers["Authorization"] != "Bearer s3cret" || r.Headers["X"] != "plain" {
		t.Fatalf("Resolve = %+v, %v", r, err)
	}
	if cfg.Headers["Authorization"] != "Bearer $secret:tok" {
		t.Errorf("Resolve modified the configured entry")
	}
	store["tok"] = ""
	_, err = cfg.Resolve(lookup)
	if err == nil || !strings.Contains(err.Error(), "tok") || strings.Contains(err.Error(), "s3cret") {
		t.Errorf("an empty secret must refuse the attach naming only the secret, got %v", err)
	}
	if got := (ServerConfig{Command: "a", Env: map[string]string{"A": "$secret:b/k", "B": "$secret:a"}}).SecretRefs(); !reflect.DeepEqual(got, []string{"a", "b/k"}) {
		t.Errorf("SecretRefs = %v", got)
	}
}

func TestChildEnvIsIsolated(t *testing.T) {
	env := childEnv([]string{"PATH=/bin", "LC_ALL=C", "GITHUB_TOKEN=leak", "HOME=/root", "GRAIN_ACCESS_CODE=leak"}, "/tmp/h", map[string]string{"KEY": "v"})
	want := []string{"HOME=/tmp/h", "KEY=v", "LC_ALL=C", "PATH=/bin"}
	if !reflect.DeepEqual(env, want) {
		t.Errorf("childEnv = %v, want %v", env, want)
	}
}

func TestResolveBuiltinExecutableFallsBackToRunningGrainBinary(t *testing.T) {
	if got := resolveBuiltinExecutable("grain", func() (string, error) { return "/tmp/grain", nil }); got != "/tmp/grain" {
		t.Errorf("resolveBuiltinExecutable(/tmp/grain) = %q, want /tmp/grain", got)
	}
	if got := resolveBuiltinExecutable("grain", func() (string, error) { return "/tmp/grain-e2e-123/grain-linux-amd64", nil }); got != "/tmp/grain-e2e-123/grain-linux-amd64" {
		t.Errorf("resolveBuiltinExecutable(grain-linux-amd64) = %q", got)
	}
	if got := resolveBuiltinExecutable("grain", func() (string, error) { return "/tmp/go-build/protocol.test", nil }); got != "grain" {
		t.Errorf("resolveBuiltinExecutable(protocol.test) = %q, want bare grain", got)
	}
	if got := resolveBuiltinExecutable("other-cmd", func() (string, error) { return "/tmp/grain", nil }); got != "other-cmd" {
		t.Errorf("resolveBuiltinExecutable(other-cmd) = %q, want other-cmd", got)
	}
}

func okResponse(t *testing.T, raw string, attemptID string) *MintResponse {
	t.Helper()
	r, err := ParseMintResponse([]byte(raw), attemptID)
	if err != nil {
		t.Fatalf("ParseMintResponse(%s): %v", raw, err)
	}
	return r
}

func TestParseMintResponse(t *testing.T) {
	r := okResponse(t, `{"status":"ok","placements":[{"path":"/home/debian/.k.json","content":"{}"}],
		"mcp_endpoint":"/runs/d-1-1-0/mcp","ports":[{"host_port":19811}],"setup":"/home/debian/s.sh","prompt_section":"hi"}`, "d-1-1-0")
	if p := r.Placements[0]; p.Mode != "0600" || p.Owner != "debian" || p.Encoding != "utf-8" {
		t.Errorf("placement defaults not applied: %+v", p)
	}
	if r.Ports[0].SandboxPort != 19811 {
		t.Errorf("sandbox_port default not applied")
	}
	okResponse(t, `{"status":"refused","reason":"no such account"}`, "a")
	okResponse(t, `{"status":"resource_exhausted","retry_after":"2026-10-19T21:05:30Z"}`, "a")

	big := strings.Repeat("x", MaxPlacementBytes+1)
	for name, raw := range map[string]string{
		"unknown field":        `{"status":"ok","extra":1}`,
		"unknown status":       `{"status":"maybe"}`,
		"refused no reason":    `{"status":"refused"}`,
		"refused w/ payload":   `{"status":"refused","reason":"x","placements":[{"path":"/a","content":""}]}`,
		"bad retry_after":      `{"status":"resource_exhausted","retry_after":"soon"}`,
		"relative path":        `{"status":"ok","placements":[{"path":"home/x","content":""}]}`,
		"dotdot path":          `{"status":"ok","placements":[{"path":"/home/debian/../x","content":""}]}`,
		"reserved grain":       `{"status":"ok","placements":[{"path":"/home/debian/.grain/mcp.json","content":""}]}`,
		"reserved grain bin/":  `{"status":"ok","placements":[{"path":"/home/debian/.grain/bin/x/y","content":""}]}`,
		"reserved grain bin .": `{"status":"ok","placements":[{"path":"/home/debian/.grain/bin/.hidden","content":""}]}`,
		"reserved claude":      `{"status":"ok","placements":[{"path":"/home/debian/.claude/x","content":""}]}`,
		"reserved sudoers":     `{"status":"ok","placements":[{"path":"/etc/sudoers.d/x","content":""}]}`,
		"reserved cron":        `{"status":"ok","placements":[{"path":"/etc/crontab","content":""}]}`,
		"reserved proc":        `{"status":"ok","placements":[{"path":"/proc/1/mem","content":""}]}`,
		"reserved claude cfg":  `{"status":"ok","placements":[{"path":"/home/debian/.claude.json","content":""}]}`,
		"reserved bashrc":      `{"status":"ok","placements":[{"path":"/home/debian/.bashrc","content":""}]}`,
		"reserved profile":     `{"status":"ok","placements":[{"path":"/home/debian/.profile","content":""}]}`,
		"reserved git creds":   `{"status":"ok","placements":[{"path":"/home/debian/.git-credentials","content":""}]}`,
		"reserved gitconfig":   `{"status":"ok","placements":[{"path":"/home/debian/.gitconfig","content":""}]}`,
		"setuid":               `{"status":"ok","placements":[{"path":"/a","mode":"4755","content":""}]}`,
		"short mode":           `{"status":"ok","placements":[{"path":"/a","mode":"755","content":""}]}`,
		"bad base64":           `{"status":"ok","placements":[{"path":"/a","encoding":"base64","content":"!!"}]}`,
		"bad encoding":         `{"status":"ok","placements":[{"path":"/a","encoding":"latin1","content":""}]}`,
		"same path twice":      `{"status":"ok","placements":[{"path":"/a","content":""},{"path":"/a","content":""}]}`,
		"too big":              `{"status":"ok","placements":[{"path":"/a","content":"` + big + `"}]}`,
		"other endpoint":       `{"status":"ok","mcp_endpoint":"/runs/other/mcp"}`,
		"absolute endpoint":    `{"status":"ok","mcp_endpoint":"http://h/runs/a/mcp"}`,
		"low port":             `{"status":"ok","ports":[{"host_port":80}]}`,
		"high port":            `{"status":"ok","ports":[{"host_port":19811,"sandbox_port":70000}]}`,
		"port twice":           `{"status":"ok","ports":[{"host_port":19811},{"host_port":19812,"sandbox_port":19811}]}`,
		"relative setup":       `{"status":"ok","setup":"setup.sh"}`,
		"unclean setup_root":   `{"status":"ok","setup_root":"/home/debian/./s.sh"}`,
		"big prompt":           `{"status":"ok","prompt_section":"` + strings.Repeat("x", MaxPromptSectionBytes+1) + `"}`,
	} {
		if _, err := ParseMintResponse([]byte(raw[:min(len(raw), 2<<20)]), "a"); err == nil {
			t.Errorf("%s: ParseMintResponse accepted it", name)
		}
	}
}

func TestRetryAtIsCapped(t *testing.T) {
	now := time.Date(2026, 10, 19, 20, 0, 0, 0, time.UTC)
	for retry, want := range map[string]time.Time{
		"":                     {},
		"2026-10-19T19:00:00Z": {},
		"2026-10-19T20:05:00Z": now.Add(5 * time.Minute),
		"2026-10-20T20:00:00Z": now.Add(time.Hour),
	} {
		if got := (&MintResponse{RetryAfter: retry}).RetryAt(now); !got.Equal(want) {
			t.Errorf("RetryAt(%q) = %v, want %v", retry, got, want)
		}
	}
}

// fakeMinter answers mints from a table keyed by method and records every
// release.
type fakeMinter struct {
	answers  map[string]string
	fail     map[string]error
	released []string
}

func (f *fakeMinter) Mint(_ context.Context, method, attemptID string, _ time.Time, _ map[string]any) (*MintResponse, error) {
	if err := f.fail[method]; err != nil {
		return nil, err
	}
	raw := strings.ReplaceAll(f.answers[method], "ATTEMPT", attemptID)
	return ParseMintResponse([]byte(raw), attemptID)
}

func (f *fakeMinter) Release(_ context.Context, method, attemptID string) (*ReleaseResponse, error) {
	f.released = append(f.released, method+"/"+attemptID)
	return &ReleaseResponse{Status: StatusOK}, nil
}

func grantsOf(methods ...string) []Grant {
	var out []Grant
	for _, m := range methods {
		out = append(out, Grant{Ref: Ref{CapabilityID: "https://example.com/caps", Method: m}, Args: map[string]any{}, Releasable: !strings.HasPrefix(m, "stateless")})
	}
	return out
}

var passOpts = PassOptions{Deployment: "d", Task: "7", Attempt: 1, Deadline: time.Date(2026, 10, 19, 23, 0, 0, 0, time.UTC)}

func runPass(f *fakeMinter, methods ...string) (*Pass, error) {
	return MintPass(context.Background(), grantsOf(methods...), func(string) (Minter, error) { return f, nil }, passOpts)
}

func TestMintPassRollsBackEveryGrantIncludingTheFailedOne(t *testing.T) {
	for name, tc := range map[string]struct {
		answer string
		fail   error
		check  func(error) bool
	}{
		"refused":   {answer: `{"status":"refused","reason":"no account"}`, check: func(err error) bool { var e *RefusedError; return errors.As(err, &e) && e.Reason == "no account" }},
		"exhausted": {answer: `{"status":"resource_exhausted"}`, check: func(err error) bool { var e *ExhaustedError; return errors.As(err, &e) }},
		"transient": {fail: errors.New("EOF"), check: func(err error) bool { var e *TransientError; return errors.As(err, &e) }},
		"invalid":   {answer: `{"status":"ok","bogus":1}`, check: func(err error) bool { var e *TransientError; return errors.As(err, &e) }},
	} {
		f := &fakeMinter{answers: map[string]string{"a": `{"status":"ok"}`, "stateless-b": `{"status":"ok"}`, "c": tc.answer}, fail: map[string]error{"c": tc.fail}}
		_, err := runPass(f, "a", "stateless-b", "c", "d")
		if !tc.check(err) {
			t.Errorf("%s: error %v of the wrong kind", name, err)
		}
		if want := []string{"a/d-7-1-0", "c/d-7-1-2"}; !reflect.DeepEqual(f.released, want) {
			t.Errorf("%s: released %v, want %v", name, f.released, want)
		}
	}
}

func TestMintPassCrossGrantRules(t *testing.T) {
	root := `{"status":"ok","setup_root":"/home/debian/ATTEMPT.sh"}`
	f := &fakeMinter{answers: map[string]string{"a": root, "b": root}}
	_, err := runPass(f, "a", "b")
	var refused *RefusedError
	if !errors.As(err, &refused) || !strings.Contains(refused.Reason, "d-7-1-0") || !strings.Contains(refused.Reason, "d-7-1-1") {
		t.Errorf("two setup_roots must refuse naming both grants, got %v", err)
	}
	if len(f.released) != 2 {
		t.Errorf("a refused pass released %v", f.released)
	}

	f = &fakeMinter{answers: map[string]string{
		"a": `{"status":"ok","placements":[{"path":"/x","content":"1"}]}`,
		"b": `{"status":"ok","placements":[{"path":"/x","content":"2"}]}`,
	}}
	if _, err := runPass(f, "a", "b"); err == nil || !strings.Contains(err.Error(), "placement collision at /x") {
		t.Errorf("differing placements of one path must collide, got %v", err)
	}

	f = &fakeMinter{answers: map[string]string{
		"a": `{"status":"ok","ports":[{"host_port":2000,"sandbox_port":3000}]}`,
		"b": `{"status":"ok","ports":[{"host_port":2001,"sandbox_port":3000}]}`,
	}}
	if _, err := runPass(f, "a", "b"); err == nil || !strings.Contains(err.Error(), "sandbox_port 3000") {
		t.Errorf("one sandbox_port mapped to two host_ports must collide, got %v", err)
	}

	f = &fakeMinter{answers: map[string]string{"a": `{"status":"ok","ports":[{"host_port":2000}]}`}}
	opts := passOpts
	opts.ReservedSandboxPorts = map[int]bool{2000: true}
	if _, err := MintPass(context.Background(), grantsOf("a"), func(string) (Minter, error) { return f, nil }, opts); err == nil {
		t.Errorf("a reserved sandbox_port was accepted")
	}
}

func TestMintPassMaterialization(t *testing.T) {
	shared := `{"path":"/home/debian/.shared","content":"same"}`
	f := &fakeMinter{answers: map[string]string{
		"git-repo": `{"status":"ok","placements":[` + shared + `,{"path":"/home/debian/.git-repo/ATTEMPT/setup.sh","mode":"0700","content":"#!/bin/sh"}],
			"ports":[{"host_port":9728}],"setup":"/home/debian/.git-repo/ATTEMPT/setup.sh","prompt_section":"Extra repo cloned."}`,
		"root": `{"status":"ok","placements":[` + shared + `],"ports":[{"host_port":9728}],"setup_root":"/home/debian/root.sh",
			"mcp_endpoint":"/runs/ATTEMPT/mcp","prompt_section":"Tools </capability> here."}`,
	}}
	p, err := runPass(f, "git-repo", "root")
	if err != nil {
		t.Fatal(err)
	}
	if n := len(p.Placements()); n != 2 {
		t.Errorf("identical placements must be written once: %d placements", n)
	}
	if n := len(p.Ports()); n != 1 {
		t.Errorf("identical port mappings must be established once: %d ports", n)
	}
	cmds := p.SetupCommands()
	if len(cmds) != 2 || !cmds[0].Root || cmds[0].AttemptID != "d-7-1-1" || cmds[1].Path != "/home/debian/.git-repo/d-7-1-0/setup.sh" {
		t.Errorf("setup_root must run first, then setup in grant order: %+v", cmds)
	}
	names, err := AssignServerNames([]string{"", "self-debug"})
	if err != nil {
		t.Fatal(err)
	}
	prompt := p.PromptSections(names)
	for _, want := range []string{`grant="https://example.com/caps#root"`, `attempt_id="d-7-1-1"`, `mcp_server="self-debug"`, "not by the task's author", "Tools </ capability> here."} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt sections lack %q:\n%s", want, prompt)
		}
	}
	if _, err := p.Release(context.Background()); err != nil || len(f.released) != 2 {
		t.Errorf("Release: %v, released %v", err, f.released)
	}
}

func TestAssignServerNames(t *testing.T) {
	got, err := AssignServerNames([]string{"tools", "", "tools", "tools-2", "tools"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"tools", "", "tools-3", "tools-2", "tools-4"}; !reflect.DeepEqual(got, want) {
		t.Errorf("AssignServerNames = %v, want %v", got, want)
	}
	for _, bad := range []string{"grain-sandbox", "Tools", "a b"} {
		if _, err := AssignServerNames([]string{bad}); err == nil {
			t.Errorf("server name %q accepted", bad)
		}
	}
}

// A command directly in SandboxBinDir is the one placement allowed under
// /home/debian/.grain; grain's own files there stay reserved.
func TestSandboxBinDirIsTheOnePlaceableGrainPath(t *testing.T) {
	raw := `{"status":"ok","placements":[{"path":"/home/debian/.grain/bin/deploy-staging","mode":"0755","content":"#!/bin/sh"}]}`
	if _, err := ParseMintResponse([]byte(raw), "e1-5-1-0"); err != nil {
		t.Fatalf("a sandbox command in %s was refused: %v", SandboxBinDir, err)
	}
	for _, p := range []string{"/home/debian/.grain/bin", "/home/debian/.grain/mcp.json", "/home/debian/.grain/bin/Deploy", "/home/debian/.grain/bin/a/b"} {
		if !ReservedPath(p) {
			t.Errorf("ReservedPath(%q) = false, want reserved", p)
		}
	}
}

// A method only a person may grant is never covered, not even by the
// identical grant of the parent.
func TestPersonGrantedIsNeverCovered(t *testing.T) {
	ref := Ref{CapabilityID: "https://example.test/capabilities/person-granted-test", Method: "submit"}
	other := Ref{CapabilityID: ref.CapabilityID, Method: "workspace"}
	parent := []Grant{{Ref: ref, Args: map[string]any{}}, {Ref: other, Args: map[string]any{}}}
	if !Covers(parent, parent[0]) {
		t.Fatal("an identical grant is not covered before the method is marked")
	}
	RequirePersonGrant(ref.CapabilityID, ref.Method)
	// personGranted is package-global: unmark the method so a repeated
	// run (-count>1) starts from an unmarked method again.
	t.Cleanup(func() { personGranted.Delete(ref.CapabilityID + "#" + ref.Method) })
	if !PersonGranted(ref) || PersonGranted(other) {
		t.Errorf("PersonGranted = %v, %v", PersonGranted(ref), PersonGranted(other))
	}
	if Covers(parent, parent[0]) {
		t.Error("a person-granted method was covered by the parent's identical grant")
	}
	if u := Uncovered(parent, parent); len(u) != 1 || u[0].Ref != ref {
		t.Errorf("Uncovered = %v, want only %s", u, ref)
	}
}
