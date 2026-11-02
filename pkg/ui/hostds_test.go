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
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// The hostd registry's routes: a created hostd's token is shown once and
// names it, a rotation retires the old token, revoking retires them all,
// and the co-located hostd can be neither given a token nor revoked.
func TestHostdRegistryRoutes(t *testing.T) {
	srv, client := testServer(t)
	ctx := context.Background()
	if err := client.Store.EnsureLocalHostd(ctx); err != nil {
		t.Fatal(err)
	}

	rec := do(t, srv, http.MethodPost, "/api/hostds", `{"name":"ws-1","owner":"Alice@Example.com","labels":{"os":"linux"}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/hostds = %d %s", rec.Code, rec.Body.String())
	}
	var issued ui.IssuedHostdToken
	if err := json.Unmarshal(rec.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(issued.Token, model.HostdTokenPrefix) || issued.Hostd.Owner != "alice@example.com" {
		t.Fatalf("issued = %+v", issued)
	}
	if h, _ := client.Store.HostdForToken(ctx, issued.Token); h == nil || h.Name != "ws-1" {
		t.Fatalf("the issued token names %+v, want ws-1", h)
	}

	for _, bad := range []string{`{"name":"Bad Name"}`, `{"name":"local"}`, `{"name":"ws-1"}`, `{"name":"ws-2","owner":"nobody"}`} {
		if rec := do(t, srv, http.MethodPost, "/api/hostds", bad); rec.Code != http.StatusBadRequest {
			t.Errorf("POST /api/hostds %s = %d, want 400", bad, rec.Code)
		}
	}

	rec = do(t, srv, http.MethodGet, "/api/hostds", "")
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), issued.Token) {
		t.Fatalf("GET /api/hostds = %d %s, want the list without the token", rec.Code, rec.Body.String())
	}
	var listed []ui.HostdView
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[0].Name != model.LocalHostd || listed[1].Name != "ws-1" || listed[1].TokenPrefix == "" {
		t.Errorf("listed = %+v, want local and ws-1 with its token's prefix", listed)
	}

	rec = do(t, srv, http.MethodPatch, "/api/hostds/ws-1", `{"owner":"bob@example.com","labels":{"gpu":"yes"}}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "bob@example.com") || !strings.Contains(rec.Body.String(), `"gpu":"yes"`) {
		t.Errorf("PATCH /api/hostds/ws-1 = %d %s", rec.Code, rec.Body.String())
	}

	rec = do(t, srv, http.MethodPost, "/api/hostds/ws-1/rotate", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("rotate = %d %s", rec.Code, rec.Body.String())
	}
	var rotated ui.IssuedHostdToken
	if err := json.Unmarshal(rec.Body.Bytes(), &rotated); err != nil {
		t.Fatal(err)
	}
	if h, _ := client.Store.HostdForToken(ctx, issued.Token); h != nil {
		t.Error("the token before the rotation still names a hostd")
	}
	if h, _ := client.Store.HostdForToken(ctx, rotated.Token); h == nil || h.Name != "ws-1" {
		t.Errorf("the rotated token names %+v, want ws-1", h)
	}

	if rec := do(t, srv, http.MethodPost, "/api/hostds/ws-1/revoke", ""); rec.Code != http.StatusOK {
		t.Fatalf("revoke = %d %s", rec.Code, rec.Body.String())
	}
	if h, _ := client.Store.HostdForToken(ctx, rotated.Token); h != nil {
		t.Error("a revoked hostd's token still names it")
	}
	if rec := do(t, srv, http.MethodPost, "/api/hostds/ws-1/rotate", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("rotating a revoked hostd = %d, want 400", rec.Code)
	}
	for _, path := range []string{"/api/hostds/local/revoke", "/api/hostds/local/rotate"} {
		if rec := do(t, srv, http.MethodPost, path, ""); rec.Code != http.StatusBadRequest {
			t.Errorf("POST %s = %d, want 400", path, rec.Code)
		}
	}
	if rec := do(t, srv, http.MethodPost, "/api/hostds/nope/revoke", ""); rec.Code != http.StatusNotFound {
		t.Errorf("revoking an unknown hostd = %d, want 404", rec.Code)
	}
}

type fakeHostdHub struct {
	statuses     map[string]hostd.RemoteStatus
	disconnected []string
}

func (f *fakeHostdHub) ServeHTTP(http.ResponseWriter, *http.Request) {}

func (f *fakeHostdHub) Statuses() map[string]hostd.RemoteStatus { return f.statuses }

func (f *fakeHostdHub) Disconnect(name string) bool {
	f.disconnected = append(f.disconnected, name)
	if st, ok := f.statuses[name]; ok {
		st.Connected = false
		f.statuses[name] = st
		return true
	}
	return false
}

// Revoking a hostd immediately drops its live session on HostdRemote and
// clears any open hostd-disconnected:<name> notification.
func TestRevokeHostdDisconnectsSessionAndClearsNotification(t *testing.T) {
	srv, client := testServer(t)
	ctx := context.Background()
	hub := &fakeHostdHub{statuses: map[string]hostd.RemoteStatus{
		"ws-1": {Connected: true, RemotePeer: hostd.RemotePeer{Name: "ws-1", Host: "box-1"}},
	}}
	client.Config.HostdRemote = hub
	if _, _, err := client.Store.CreateHostd(ctx, model.Hostd{Name: "ws-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Store.RaiseNotification(ctx, model.Notification{
		Subject:  "hostd-disconnected:ws-1",
		Severity: model.NotificationWarning,
		Title:    "grain hostd ws-1 disconnected",
		Body:     "waiting",
		RaisedBy: "automation:hostd",
		RaisedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	if rec := do(t, srv, http.MethodPost, "/api/hostds/ws-1/revoke", ""); rec.Code != http.StatusOK {
		t.Fatalf("revoke = %d %s", rec.Code, rec.Body.String())
	}
	if len(hub.disconnected) != 1 || hub.disconnected[0] != "ws-1" {
		t.Errorf("disconnected = %v, want [ws-1]", hub.disconnected)
	}
	if n, err := client.Store.OpenNotification(ctx, "hostd-disconnected:ws-1"); err != nil || n != nil {
		t.Errorf("open hostd-disconnected:ws-1 after revoke = %+v (err %v), want cleared", n, err)
	}
}

// Repository-scoped grants (on an acting task or an external agent token)
// cannot reach deployment-wide routes such as /api/hostds by smuggling an
// ignored "repo" or "taskId" in the body or "?repo=" in the query, nor
// read another repository's metrics or task list behind a repeated or
// comma-separated ?repo= value.
func TestRepoScopedAgentCannotReachHostdRegistryOrMultiRepoQueries(t *testing.T) {
	srv, client := testServer(t)
	ctx := context.Background()
	if err := client.Store.EnsureLocalHostd(ctx); err != nil {
		t.Fatal(err)
	}
	seedAPITask(t, client, "10", "acme/widgets",
		"api:capabilities:admin@acme/widgets",
		"api:settings:read@acme/widgets",
		"api:deployment:read@acme/widgets",
		"api:tasks:read@acme/widgets",
	)
	seedAPITask(t, client, "11", "acme/widgets",
		"api:capabilities:admin",
		"api:settings:read",
	)

	for _, tc := range []struct {
		method, path, body string
	}{
		{http.MethodPost, "/api/hostds", `{"name":"rogue-1","repo":"acme/widgets"}`},
		{http.MethodPost, "/api/hostds", `{"name":"rogue-2","taskId":"10"}`},
		{http.MethodGet, "/api/hostds?repo=acme/widgets", ""},
		{http.MethodGet, "/api/metrics?repo=acme/widgets&repo=acme/secret", ""},
		{http.MethodGet, "/api/metrics?repo=acme/widgets,acme/secret", ""},
		{http.MethodGet, "/api/tasks?repo=&repo=acme/widgets", ""},
	} {
		if code, resp := asAgent(t, srv, "10", tc.method, tc.path, tc.body, "testing"); code != http.StatusForbidden {
			t.Errorf("repo-scoped task 10 %s %s (%s) = %d %s, want 403", tc.method, tc.path, tc.body, code, resp)
		}
	}

	// Single-repo queries on routes whose handlers actually filter by ?repo= still work.
	for _, path := range []string{"/api/metrics?repo=acme/widgets", "/api/tasks?repo=acme/widgets"} {
		if code, resp := asAgent(t, srv, "10", http.MethodGet, path, "", ""); code != http.StatusOK {
			t.Errorf("repo-scoped task 10 GET %s = %d %s, want 200", path, code, resp)
		}
	}

	// Unscoped deployment-wide grants still reach /api/hostds.
	if code, resp := asAgent(t, srv, "11", http.MethodPost, "/api/hostds", `{"name":"legit-1"}`, "register hostd"); code != http.StatusCreated {
		t.Fatalf("unscoped task 11 POST /api/hostds = %d %s, want 201", code, resp)
	}
	if code, resp := asAgent(t, srv, "11", http.MethodGet, "/api/hostds", "", ""); code != http.StatusOK {
		t.Fatalf("unscoped task 11 GET /api/hostds = %d %s, want 200", code, resp)
	}

	// Same boundary for a repo-restricted external agent credential over /mcp.
	enableExternalAgentAPI(t, client.Store)
	humanCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com"})
	issued, err := client.CreateExternalAgentToken(humanCtx, ui.CreateExternalAgentTokenRequest{
		Name:   "ext-widgets",
		Scopes: []string{"capabilities:admin", "settings:read", "deployment:read", "tasks:read"},
		Repos:  []string{"acme/widgets"},
	})
	if err != nil {
		t.Fatalf("CreateExternalAgentToken: %v", err)
	}
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"api_create_hostd", map[string]any{"reason": "testing", "body": map[string]any{"name": "rogue-ext-1", "repo": "acme/widgets"}}},
		{"api_create_hostd", map[string]any{"reason": "testing", "body": map[string]any{"name": "rogue-ext-2", "taskId": "10"}}},
		{"api_list_hostds", map[string]any{"query": map[string]any{"repo": "acme/widgets"}}},
		{"api_get_metrics", map[string]any{"query": map[string]any{"repo": "acme/widgets,acme/secret"}}},
	} {
		mcpRec, res := callMCPTool(t, srv, issued.Token, tc.tool, tc.args)
		if mcpRec.Code != http.StatusOK || !res.IsError {
			t.Errorf("repo-scoped external token %s (%v) = status %d isError=%v content=%v, want tool error",
				tc.tool, tc.args, mcpRec.Code, res.IsError, res.Content)
		}
	}
	mcpRec, res := callMCPTool(t, srv, issued.Token, "api_get_metrics", map[string]any{"query": map[string]any{"repo": "acme/widgets"}})
	if mcpRec.Code != http.StatusOK || res.IsError {
		t.Errorf("repo-scoped external token api_get_metrics(acme/widgets) = status %d isError=%v content=%v, want success",
			mcpRec.Code, res.IsError, res.Content)
	}
}
