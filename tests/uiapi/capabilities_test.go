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

package uiapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/demo/demotest"
)

// ported from ui/e2e/local-scripts-approval-walkthrough.spec.js:176.
func TestClassic_LocalScriptsApproval_ATaskWithoutTheCapabilityCannotAskForALocalScriptApproval(t *testing.T) {
	t.Parallel()
	srv := demotest.Start(t, demotest.Options{})
	id := srv.Do(t, http.MethodPost, "/api/tasks", map[string]any{
		"title":        "No local scripts here",
		"description":  "Uses the ops local scripts.",
		"capabilities": []string{},
	}).WantOK(t).ID(t)
	res := srv.Do(t, http.MethodPost, "/api/tasks/"+id+"/local-script-approvals", map[string]any{
		"id": "1", "tool": "local_deploy", "script": "deploy", "args": []string{},
	}).WantStatus(t, http.StatusBadRequest)
	if !strings.Contains(res.Error(), "holds no host-script grant") {
		t.Errorf("refusal = %q, want it to say the task holds no host-script grant", res.Error())
	}
}

// capabilityMethod is one entry of GET /api/capability-methods?fields=full.
type capabilityMethod struct {
	Ref  string          `json:"ref"`
	Args json.RawMessage `json:"args"`
}

func listCapabilityMethods(t *testing.T, srv *demotest.Server) []capabilityMethod {
	t.Helper()
	var out []capabilityMethod
	cursor := ""
	for range 100 {
		q := url.Values{"limit": {"50"}, "fields": {"full"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var page struct {
			Items      []capabilityMethod `json:"items"`
			NextCursor string             `json:"nextCursor"`
		}
		srv.Do(t, http.MethodGet, "/api/capability-methods?"+q.Encode(), nil).WantOK(t).JSON(t, &page)
		out = append(out, page.Items...)
		if page.NextCursor == "" {
			return out
		}
		cursor = page.NextCursor
	}
	t.Fatal("GET /api/capability-methods never ran out of pages")
	return nil
}

// personGrantedCell is one argument set only a person may grant: an
// argument whose schema reserves the value (x-grain-person-granted).
type personGrantedCell struct {
	ref     string
	variant string
	args    map[string]any
}

// personGrantedCells is grantMatrix.js's argVariants filtered by
// isPersonGrantedArg, reduced to what that filter keeps: for every
// property that reserves values, one argument set per reserved value.
func personGrantedCells(t *testing.T, methods []capabilityMethod) []personGrantedCell {
	t.Helper()
	var cells []personGrantedCell
	for _, m := range methods {
		if len(m.Args) == 0 || string(m.Args) == "null" {
			continue
		}
		var root map[string]any
		if err := json.Unmarshal(m.Args, &root); err != nil {
			t.Fatalf("%s's args schema: %v", m.Ref, err)
		}
		props, _ := deref(root, root)["properties"].(map[string]any)
		for name, sub := range props {
			d := deref(root, sub)
			reserved, _ := d["x-grain-person-granted"].([]any)
			for _, value := range reserved {
				cells = append(cells, personGrantedCell{
					ref:     m.Ref,
					variant: fmt.Sprintf("%s=%v", name, value),
					args:    map[string]any{name: value},
				})
			}
		}
	}
	return cells
}

// deref resolves a local $ref ("#/$defs/x") against root.
func deref(root map[string]any, s any) map[string]any {
	m, _ := s.(map[string]any)
	for depth := 0; m != nil && depth < 8; depth++ {
		ref, ok := m["$ref"].(string)
		if !ok {
			return m
		}
		var at any = root
		for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
			next, _ := at.(map[string]any)
			at = next[part]
		}
		m, _ = at.(map[string]any)
	}
	return m
}

func grantString(t *testing.T, ref string, args map[string]any) string {
	t.Helper()
	if args == nil {
		return ref
	}
	enc, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return ref + "=" + string(enc)
}

func checkGrant(t *testing.T, srv *demotest.Server, ref string, args map[string]any) (recorded, refusal string) {
	t.Helper()
	grant := map[string]any{"capability": ref}
	if args != nil {
		grant["with"] = args
	}
	var out []struct {
		Recorded string `json:"recorded"`
		Error    string `json:"error"`
	}
	srv.Do(t, http.MethodPost, "/api/capability-grants/check", map[string]any{"grants": []any{grant}}).WantOK(t).JSON(t, &out)
	if len(out) != 1 {
		t.Fatalf("capability-grants/check answered %d results for one grant", len(out))
	}
	return out[0].Recorded, out[0].Error
}

func heldOf(t *testing.T, srv *demotest.Server, id, ref string) []string {
	t.Helper()
	var task struct {
		Capabilities []string `json:"capabilities"`
	}
	srv.Do(t, http.MethodGet, "/api/tasks/"+id, nil).WantOK(t).JSON(t, &task)
	var held []string
	for _, c := range task.Capabilities {
		if c == ref || strings.HasPrefix(c, ref+"=") {
			held = append(held, c)
		}
	}
	return held
}

// ported from ui/e2e/grant-matrix-api.spec.js:243.
func TestClassic_GrantMatrixAPI_PersonOnlyArgumentsAreRefusedWhenAnAgentTokenAttemptsThem(t *testing.T) {
	t.Parallel()
	srv := demotest.Start(t, demotest.Options{})
	const gcpKey = "https://grain.dev/capabilities/gcp#gcp-key"
	cells := personGrantedCells(t, listCapabilityMethods(t, srv))
	found := false
	for _, c := range cells {
		if c.ref == gcpKey && c.args["permission_set"] == "org-deploy" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected gcp#gcp-key {permission_set: org-deploy} among the x-grain-person-granted cells, got %+v", cells)
	}

	srv.Do(t, http.MethodPut, "/api/settings", map[string]any{"externalAgentApi": true}).WantOK(t)
	var issued struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	srv.Do(t, http.MethodPost, "/api/external-agents/tokens", map[string]any{
		"name":   fmt.Sprintf("matrix-agent-%d", time.Now().UnixNano()),
		"scopes": []string{"tasks:write", "tasks:admin", "capabilities:admin"},
	}).WantOK(t).JSON(t, &issued)
	external := func(method, path string, body any) *demotest.Response {
		return srv.Do(t, method, path, body,
			demotest.Header{Name: "Authorization", Value: "Bearer " + issued.Token},
			demotest.Header{Name: "X-Grain-Acting-Reason", Value: "grant matrix person-only check"})
	}

	var personGrants []string
	for _, c := range cells {
		recorded, refusal := checkGrant(t, srv, c.ref, c.args)
		if refusal != "" {
			t.Fatalf("%s [%s]: a person's check refused it: %s", c.ref, c.variant, refusal)
		}
		personGrants = append(personGrants, recorded)
	}
	const allowedGCP = gcpKey + `={"permission_set":"sandbox-agent"}`

	// A person files an acting task that already holds every person-only
	// grant plus capabilities:admin and tasks:write, and a target task to
	// attach grants to. Holding the grant itself must not let an agent
	// pass a person-only argument on.
	acting := srv.Do(t, http.MethodPost, "/api/tasks", map[string]any{
		"title":  fmt.Sprintf("E2E matrix acting agent %d", time.Now().UnixNano()),
		"noRepo": true,
		"capabilities": append([]string{
			"api:tasks:write", "api:tasks:admin", "api:capabilities:admin", allowedGCP,
		}, personGrants...),
	}).WantOK(t).ID(t)
	target := srv.Do(t, http.MethodPost, "/api/tasks", map[string]any{
		"title":        fmt.Sprintf("E2E matrix target %d", time.Now().UnixNano()),
		"noRepo":       true,
		"capabilities": []string{},
	}).WantOK(t).ID(t)
	actingHeaders := []demotest.Header{
		{Name: "X-Grain-Acting-Task", Value: acting},
		{Name: "X-Grain-Acting-Reason", Value: "grant matrix person-only check"},
	}
	asActing := func(method, path string, body any) *demotest.Response {
		return srv.Do(t, method, path, body, actingHeaders...)
	}
	attachBody := func(ref string, args map[string]any) map[string]any {
		b := map[string]any{"capability": ref, "attach": true}
		if args != nil {
			b["with"] = args
		}
		return b
	}

	// Positive control: the acting task can attach the variant it holds
	// that is not person-granted.
	asActing(http.MethodPost, "/api/tasks/"+target+"/capabilities",
		attachBody(gcpKey, map[string]any{"permission_set": "sandbox-agent"})).WantOK(t)
	srv.Do(t, http.MethodPatch, "/api/tasks/"+target, map[string]any{"capabilities": []string{}}).WantOK(t)

	onlyAPerson := regexp.MustCompile(`(?i)only a person may grant`)
	for _, c := range cells {
		spec := grantString(t, c.ref, c.args)
		for _, call := range []struct {
			label string
			do    func(method, path string, body any) *demotest.Response
		}{{"acting", asActing}, {"external", external}} {
			for _, attempt := range []struct {
				route, method, path string
				body                any
			}{
				{"attach", http.MethodPost, "/api/tasks/" + target + "/capabilities", attachBody(c.ref, c.args)},
				{"patch", http.MethodPatch, "/api/tasks/" + target, map[string]any{"capabilities": []string{spec}}},
				{"create", http.MethodPost, "/api/tasks", map[string]any{
					"title": "Refused " + call.label + " " + c.variant, "noRepo": true, "capabilities": []string{spec},
				}},
			} {
				res := call.do(attempt.method, attempt.path, attempt.body)
				if res.Status != http.StatusForbidden || !onlyAPerson.Match(res.Body) {
					t.Errorf("%s [%s] %s %s = %d %s, want 403 saying only a person may grant it",
						c.ref, c.variant, call.label, attempt.route, res.Status, res.Body)
				}
			}
		}

		mcp := external(http.MethodPost, "/mcp", map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{
				"name": "api_create_task",
				"arguments": map[string]any{"body": map[string]any{
					"title": "Refused mcp " + c.variant, "body": "x", "noRepo": true, "capabilities": []string{spec},
				}},
			},
		})
		if mcp.OK() {
			var rpc struct {
				Error  json.RawMessage `json:"error"`
				Result struct {
					IsError bool `json:"isError"`
				} `json:"result"`
			}
			mcp.JSON(t, &rpc)
			if len(rpc.Error) == 0 && !rpc.Result.IsError {
				t.Errorf("%s [%s] mcp allowed: %s", c.ref, c.variant, mcp.Body)
			}
		}
		if !onlyAPerson.Match(mcp.Body) {
			t.Errorf("%s [%s] mcp: %s, want it to say only a person may grant it", c.ref, c.variant, mcp.Body)
		}

		if held := heldOf(t, srv, target, c.ref); len(held) != 0 {
			t.Errorf("%s [%s]: target holds %v after every refusal", c.ref, c.variant, held)
		}
	}
}
