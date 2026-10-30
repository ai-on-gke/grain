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
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/capability/servers"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

const grantTestCap = "https://capabilities.example.com/cloud"

// grantServers is an attached capability server as both the UI's status
// reader and admission's Lookup see it, over a fixed tools/list.
type grantServers struct{ server *protocol.Server }

func (g grantServers) Statuses() []servers.Status {
	var details []servers.MethodDetail
	for _, name := range []string{"key", "viewer"} {
		m := g.server.Methods[name]
		details = append(details, servers.MethodDetail{Name: name, Description: m.Description, Args: m.ArgsSchema, Releasable: m.Releasable})
	}
	return []servers.Status{{ID: grantTestCap, State: servers.Attached, Methods: []string{"key", "viewer"}, MethodDetails: details}}
}

func (g grantServers) Lookup(id string) (*protocol.Server, error) {
	if id != grantTestCap {
		return nil, fmt.Errorf("capability %s is not configured in mcpServers", id)
	}
	return g.server, nil
}

type grantToolsCaller struct{ tools []protocol.Tool }

func (c grantToolsCaller) ListTools(context.Context) ([]protocol.Tool, error) { return c.tools, nil }
func (c grantToolsCaller) CallTool(context.Context, string, map[string]any) ([]byte, error) {
	return nil, fmt.Errorf("not minted in this test")
}
func (c grantToolsCaller) Close() error { return nil }

func mintTool(name string, args map[string]any) protocol.Tool {
	props := map[string]any{"attempt_id": map[string]any{"type": "string"}, "deadline": map[string]any{"type": "string"}}
	if args != nil {
		props["args"] = args
	}
	return protocol.Tool{Name: "mint_" + name, Description: "Mint " + name, InputSchema: map[string]any{
		"type": "object", "required": []any{"attempt_id", "deadline", "args"}, "properties": props,
	}}
}

func grantTestServer(t *testing.T) (*httptest.Server, *ui.Client) {
	t.Helper()
	srv, client := testServer(t)
	server, err := protocol.NewServer(context.Background(), grantTestCap, grantToolsCaller{tools: []protocol.Tool{
		mintTool("key", map[string]any{
			"type": "object", "additionalProperties": false, "required": []any{"account"},
			"properties": map[string]any{
				"account": map[string]any{"enum": []any{"staging", "prod"}},
				"project": map[string]any{"type": "string", "pattern": "^[a-z-]+$"},
				"scopes":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "uniqueItems": true, "default": []any{"all"}},
			},
		}),
		{Name: "release_key", InputSchema: map[string]any{"type": "object", "required": []any{"attempt_id"}}},
		mintTool("viewer", nil),
	}})
	if err != nil {
		t.Fatal(err)
	}
	client.Config.CapabilityServers = grantServers{server: server}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	ui.SetAccessCodeForTest(srv, testAccessCode)
	return ts, client
}

// postJSON sends body and decodes the answer into out, returning the
// status code.
func postJSON(t *testing.T, ts *httptest.Server, method, path string, body any, out any) int {
	t.Helper()
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, ts.URL+path, bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer "+testAccessCode)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if out != nil {
		if err := json.NewDecoder(res.Body).Decode(out); err != nil {
			t.Fatalf("%s %s: decoding: %v", method, path, err)
		}
	}
	return res.StatusCode
}

func refGrants(caps []string) []string {
	var out []string
	for _, c := range caps {
		if strings.HasPrefix(c, "https://") {
			out = append(out, c)
		}
	}
	return out
}

func TestCapabilityMethodsListArgumentSchemas(t *testing.T) {
	ts, client := grantTestServer(t)
	var methods []ui.CapabilityMethod
	if code := postJSON(t, ts, "GET", "/api/capability-methods", nil, &methods); code != 200 {
		t.Fatalf("status %d", code)
	}
	if len(methods) != 2 {
		t.Fatalf("methods = %+v", methods)
	}
	key := methods[0]
	if key.Ref != grantTestCap+"#key" || !key.RequiresArgs || !key.Releasable || key.Args == nil || key.Policy != nil {
		t.Errorf("key = %+v", key)
	}
	if props, _ := key.Args["properties"].(map[string]any); props["account"] == nil {
		t.Errorf("key's schema lost its properties: %v", key.Args)
	}
	if v := methods[1]; v.RequiresArgs || v.Releasable || v.Args != nil || v.Policy != nil {
		t.Errorf("viewer = %+v", v)
	}

	if err := client.SetCapabilityPolicy(context.Background(), model.CapabilityPolicy{
		Rules: []model.CapabilityPolicyRule{
			{Capability: grantTestCap + "#key", RunKinds: []string{model.CapabilityRunTask}, Deny: true},
			{Capability: grantTestCap + "#viewer", RunKinds: []string{model.CapabilityRunTask}, Repos: []string{"acme/*"}, Deny: true},
		},
	}); err != nil {
		t.Fatal(err)
	}
	methods = nil
	if code := postJSON(t, ts, "GET", "/api/capability-methods", nil, &methods); code != 200 {
		t.Fatalf("status %d", code)
	}
	if got := methods[0].Policy[model.CapabilityRunTask]; got != model.CapabilityPolicyDenied {
		t.Errorf("key policy = %v, want task denied", methods[0].Policy)
	}
	if got := methods[1].Policy[model.CapabilityRunTask]; got != model.CapabilityPolicyMayBeRefused {
		t.Errorf("viewer policy = %v, want task may_be_refused", methods[1].Policy)
	}
}

func TestSetCapabilityGrantWithArguments(t *testing.T) {
	ts, client := grantTestServer(t)
	ctx := context.Background()
	task, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "grant with args", Capabilities: &[]string{}})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/tasks/" + task.ID + "/capabilities"
	attach := func(with map[string]any) (int, map[string]any) {
		var out map[string]any
		code := postJSON(t, ts, "POST", path, map[string]any{"capability": grantTestCap + "#key", "with": with, "attach": true}, &out)
		return code, out
	}

	// A refusal names the argument it is about.
	code, out := attach(map[string]any{"account": "qa"})
	if code != 400 {
		t.Fatalf("an out-of-enum account was accepted: %d %v", code, out)
	}
	if fields, _ := out["fields"].(map[string]any); fields["account"] == nil {
		t.Errorf("fields = %v, want one for account", out["fields"])
	}
	code, out = attach(map[string]any{"account": "staging", "project": "Not Valid"})
	if fields, _ := out["fields"].(map[string]any); code != 400 || fields["project"] == nil {
		t.Errorf("a pattern refusal: %d %v", code, out)
	}
	if code, out = attach(map[string]any{"account": "staging", "secret": "x"}); code != 400 {
		t.Errorf("an argument the schema does not accept was kept: %d %v", code, out)
	}

	// Two grants of one method with different arguments are two grants;
	// an equal one, even written differently, is not a third.
	if code, out = attach(map[string]any{"account": "staging"}); code != 200 {
		t.Fatalf("attach staging: %d %v", code, out)
	}
	if code, out = attach(map[string]any{"account": "prod", "project": "web"}); code != 200 {
		t.Fatalf("attach prod: %d %v", code, out)
	}
	if code, out = attach(map[string]any{"account": "staging", "scopes": []any{"all"}}); code != 200 {
		t.Fatalf("attach staging again: %d %v", code, out)
	}
	detail, err := client.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	staging := grantTestCap + `#key={"account":"staging","scopes":["all"]}`
	prod := grantTestCap + `#key={"account":"prod","project":"web","scopes":["all"]}`
	got := refGrants(detail.Capabilities)
	if len(got) != 2 || !contains(got, staging) || !contains(got, prod) {
		t.Fatalf("grants = %q, want staging and prod, normalized, once each", got)
	}

	// A chip edited in place replaces its grant.
	edited := map[string]any{"capability": grantTestCap + "#key", "with": map[string]any{"account": "prod", "project": "api"}, "attach": true, "replace": prod}
	if code := postJSON(t, ts, "POST", path, edited, &out); code != 200 {
		t.Fatalf("replace: %d %v", code, out)
	}
	detail, _ = client.GetTask(ctx, task.ID)
	got = refGrants(detail.Capabilities)
	if len(got) != 2 || contains(got, prod) || !contains(got, grantTestCap+`#key={"account":"prod","project":"api","scopes":["all"]}`) {
		t.Fatalf("after replace: %q", got)
	}

	// Detaching names the recorded grant, and only that one goes.
	if code := postJSON(t, ts, "POST", path, map[string]any{"grant": staging, "attach": false}, &out); code != 200 {
		t.Fatalf("detach: %d %v", code, out)
	}
	detail, _ = client.GetTask(ctx, task.ID)
	if got = refGrants(detail.Capabilities); len(got) != 1 || contains(got, staging) {
		t.Fatalf("after detach: %q", got)
	}

	// {id, attach} still works for an argument-less grant.
	if code := postJSON(t, ts, "POST", path, map[string]any{"id": grantTestCap + "#viewer", "attach": true}, &out); code != 200 {
		t.Fatalf("bare attach: %d %v", code, out)
	}
	// A method that requires arguments cannot be granted bare.
	if code := postJSON(t, ts, "POST", path, map[string]any{"id": grantTestCap + "#key", "attach": true}, &out); code != 400 {
		t.Errorf("key was granted with no arguments: %d %v", code, out)
	}
	// A grant without attach is refused, not read as a detach.
	if code := postJSON(t, ts, "POST", path, map[string]any{"capability": grantTestCap + "#viewer", "enabled": true}, &out); code != 400 {
		t.Errorf("a grant naming no attach was accepted: %d", code)
	}
	// Both id and capability is a malformed request.
	if code := postJSON(t, ts, "POST", path, map[string]any{"id": "gcp-key", "capability": grantTestCap + "#viewer", "attach": true}, &out); code != 400 {
		t.Errorf("id and capability together were accepted: %d", code)
	}
}

func TestDetachGrantNoServerOffersAnyMore(t *testing.T) {
	ts, client := grantTestServer(t)
	ctx := context.Background()
	gone := "https://capabilities.example.com/retired#old={\"x\":1}"
	task, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "stale grant", Capabilities: &[]string{}})
	if err != nil {
		t.Fatal(err)
	}
	// Written as it would have been while that server was attached.
	if err := client.Store.UpdateTask(ctx, task.ID, func(tk *model.Task) error {
		tk.Grants = append(tk.Grants, model.Grant{Capability: gone, Via: model.GrantByLabel})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if code := postJSON(t, ts, "POST", "/api/tasks/"+task.ID+"/capabilities", map[string]any{"grant": gone, "attach": false}, &out); code != 200 {
		t.Fatalf("detach of a grant nothing offers: %d %v", code, out)
	}
	detail, _ := client.GetTask(ctx, task.ID)
	if contains(detail.Capabilities, gone) {
		t.Errorf("the grant is still held: %q", detail.Capabilities)
	}
}

func TestCheckCapabilityGrants(t *testing.T) {
	ts, _ := grantTestServer(t)
	var out []ui.GrantCheck
	code := postJSON(t, ts, "POST", "/api/capability-grants/check", map[string]any{"grants": []any{
		map[string]any{"capability": grantTestCap + "#key", "with": map[string]any{"account": "prod"}},
		map[string]any{"grant": grantTestCap + `#key={"account":"prod"}`},
		map[string]any{"grant": grantTestCap + `#key={"account":"dev","scopes":["all"]}`},
		map[string]any{"grant": "https://capabilities.example.com/retired#old"},
		map[string]any{"grant": "gcp-key"},
	}}, &out)
	if code != 200 || len(out) != 5 {
		t.Fatalf("%d %+v", code, out)
	}
	if out[0].Recorded != grantTestCap+`#key={"account":"prod","scopes":["all"]}` || out[0].Error != "" || out[0].Changed {
		t.Errorf("a fresh grant: %+v", out[0])
	}
	if !out[1].Changed || out[1].Error != "" {
		t.Errorf("a recorded grant missing a default since added is not flagged changed: %+v", out[1])
	}
	if out[2].Error == "" || out[2].Fields["account"] == "" {
		t.Errorf("a recorded grant that no longer validates: %+v", out[2])
	}
	if !out[3].Unchecked {
		t.Errorf("a grant of an unattached server: %+v", out[3])
	}
	if out[4].Recorded != "gcp-key" || out[4].Error != "" {
		t.Errorf("an in-process capability: %+v", out[4])
	}
}

func TestCreateTaskGrantRefusalNamesItsField(t *testing.T) {
	ts, _ := grantTestServer(t)
	var out map[string]any
	code := postJSON(t, ts, "POST", "/api/tasks", map[string]any{
		"title":        "with a bad grant",
		"capabilities": []string{grantTestCap + `#key={"account":"qa"}`},
	}, &out)
	if fields, _ := out["fields"].(map[string]any); code != 400 || fields["account"] == nil {
		t.Errorf("%d %v", code, out)
	}
}

// setDenyViewerFor stores a capability policy denying grantTestCap's
// viewer to agent tasks on acme/widgets.
func setDenyViewerFor(t *testing.T, client *ui.Client) {
	t.Helper()
	if err := client.SetCapabilityPolicy(context.Background(), model.CapabilityPolicy{Rules: []model.CapabilityPolicyRule{
		{Capability: grantTestCap + "#viewer", Repos: []string{"acme/widgets"}, RunKinds: []string{model.CapabilityRunTask}, Deny: true},
	}}); err != nil {
		t.Fatal(err)
	}
}

// A grant the capability policy denies the task's repo and run kind is
// refused when the task is filed, naming the rule, rather than accepted
// and parked at admission; another repository is not affected.
func TestCreateTaskRefusesAGrantThePolicyDenies(t *testing.T) {
	ts, client := grantTestServer(t)
	setDenyViewerFor(t, client)
	var out map[string]any
	code := postJSON(t, ts, "POST", "/api/tasks", map[string]any{
		"title":        "denied grant",
		"repo":         "acme/widgets",
		"capabilities": []string{grantTestCap + "#viewer"},
	}, &out)
	msg, _ := out["error"].(string)
	if code != 400 || !strings.Contains(msg, "denies "+grantTestCap+"#viewer") || !strings.Contains(msg, "rule 1: deny "+grantTestCap+"#viewer") {
		t.Fatalf("filing a denied grant: %d %v, want 400 naming the rule", code, out)
	}
	if code := postJSON(t, ts, "POST", "/api/tasks", map[string]any{
		"title":        "elsewhere",
		"repo":         "acme/gadgets",
		"capabilities": []string{grantTestCap + "#viewer"},
	}, &out); code != 201 {
		t.Fatalf("filing on a repo the rule does not cover: %d %v", code, out)
	}
}

// Attaching a denied grant to a task already filed is refused the same
// way.
func TestSetCapabilityGrantRefusesAGrantThePolicyDenies(t *testing.T) {
	ts, client := grantTestServer(t)
	repo := "acme/widgets"
	task, err := client.CreateTask(context.Background(), ui.CreateTaskRequest{Title: "t", Repo: repo, Capabilities: &[]string{}})
	if err != nil {
		t.Fatal(err)
	}
	setDenyViewerFor(t, client)
	var out map[string]any
	code := postJSON(t, ts, "POST", "/api/tasks/"+task.ID+"/capabilities", map[string]any{"capability": grantTestCap + "#viewer", "attach": true}, &out)
	if msg, _ := out["error"].(string); code != 400 || !strings.Contains(msg, "capability policy denies") {
		t.Fatalf("attaching a denied grant: %d %v", code, out)
	}
}

// The method listing asked for one run says which methods the policy
// denies it, and why; one that names no run says nothing about policy.
func TestCapabilityMethodsMarkWhatThePolicyDenies(t *testing.T) {
	ts, client := grantTestServer(t)
	setDenyViewerFor(t, client)
	refusals := func(path string) map[string]string {
		t.Helper()
		var methods []ui.CapabilityMethod
		if code := postJSON(t, ts, "GET", path, nil, &methods); code != 200 {
			t.Fatalf("GET %s: %d", path, code)
		}
		out := map[string]string{}
		for _, m := range methods {
			out[m.Method] = m.PolicyRefusal
		}
		return out
	}
	got := refusals("/api/capability-methods?repo=acme/widgets&kind=task")
	if !strings.Contains(got["viewer"], "rule 1: deny") || got["key"] != "" {
		t.Errorf("acme/widgets task: %q", got)
	}
	for _, path := range []string{
		"/api/capability-methods",
		"/api/capability-methods?repo=acme/gadgets&kind=task",
		"/api/capability-methods?repo=acme/widgets&kind=ci",
	} {
		if got := refusals(path); got["viewer"] != "" || got["key"] != "" {
			t.Errorf("%s: %q, want nothing refused", path, got)
		}
	}
	if code := postJSON(t, ts, "GET", "/api/capability-methods?kind=job", nil, nil); code != 400 {
		t.Errorf("an unknown kind: %d, want 400", code)
	}
}
