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
		"api:capabilities:read@acme/widgets",
		"api:deployment:read@acme/widgets",
		"api:tasks:read@acme/widgets",
	)
	seedAPITask(t, client, "11", "acme/widgets",
		"api:capabilities:admin",
		"api:capabilities:read",
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
		Scopes: []string{"capabilities:admin", "capabilities:read", "settings:read", "deployment:read", "tasks:read"},
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

// The deprecated shared token's hostd holds its -hostd-token-name, so a
// registry hostd cannot be created under it.
func TestCreateHostdRefusesTheSharedTokensName(t *testing.T) {
	srv, client := testServer(t)
	client.Config.HostdRemote = &hostd.Remote{Token: "shared", SharedName: "edge"}
	if rec := do(t, srv, http.MethodPost, "/api/hostds", `{"name":"edge"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("POST /api/hostds edge = %d %s, want 400", rec.Code, rec.Body.String())
	}
	if rec := do(t, srv, http.MethodPost, "/api/hostds", `{"name":"ws-1"}`); rec.Code != http.StatusCreated {
		t.Errorf("POST /api/hostds ws-1 = %d %s, want 201", rec.Code, rec.Body.String())
	}
}

// Changing a connected hostd's owner drops a session admitted as anyone
// else, so its redial is judged against the new owner; a session admitted
// by its token alone, or as the new owner, stays (walkthrough
// 2026-11-03-hostd-registry, F3).
func TestChangingAHostdsOwnerDropsASessionConnectedAsTheOldOne(t *testing.T) {
	srv, client := testServer(t)
	ctx := context.Background()
	hub := &fakeHostdHub{statuses: map[string]hostd.RemoteStatus{
		"ws-iap":  {Connected: true, RemotePeer: hostd.RemotePeer{Name: "ws-iap", Principal: "ops@example.com"}},
		"ws-same": {Connected: true, RemotePeer: hostd.RemotePeer{Name: "ws-same", Principal: "ops@example.com"}},
		"ws-tok":  {Connected: true, RemotePeer: hostd.RemotePeer{Name: "ws-tok"}},
	}}
	client.Config.HostdRemote = hub
	for _, name := range []string{"ws-iap", "ws-same", "ws-tok"} {
		if _, _, err := client.Store.CreateHostd(ctx, model.Hostd{Name: name, Owner: "ops@example.com"}); err != nil {
			t.Fatal(err)
		}
	}
	for path, body := range map[string]string{
		"/api/hostds/ws-iap":  `{"owner":"owner-b@example.com"}`,
		"/api/hostds/ws-same": `{"owner":"OPS@example.com","labels":{"gpu":"yes"}}`,
		"/api/hostds/ws-tok":  `{"owner":"owner-b@example.com"}`,
	} {
		if rec := do(t, srv, http.MethodPatch, path, body); rec.Code != http.StatusOK {
			t.Fatalf("PATCH %s = %d %s", path, rec.Code, rec.Body.String())
		}
	}
	if len(hub.disconnected) != 1 || hub.disconnected[0] != "ws-iap" {
		t.Errorf("disconnected = %v, want only [ws-iap]", hub.disconnected)
	}
	hub.disconnected = nil
	if rec := do(t, srv, http.MethodPatch, "/api/hostds/ws-same", `{"labels":{"gpu":"no"}}`); rec.Code != http.StatusOK {
		t.Fatalf("PATCH labels = %d %s", rec.Code, rec.Body.String())
	}
	if len(hub.disconnected) != 0 {
		t.Errorf("a labels-only edit disconnected %v", hub.disconnected)
	}
}

// The deprecated shared token's hostd is listed beside the registry's,
// marked shared, with its -hostd-token-owner and its connection
// (walkthrough 2026-11-03-hostd-registry, F4).
func TestListHostdsShowsTheSharedTokensHostd(t *testing.T) {
	srv, client := testServer(t)
	ctx := context.Background()
	if err := client.Store.EnsureLocalHostd(ctx); err != nil {
		t.Fatal(err)
	}
	client.Config.HostdRemote = &hostd.Remote{Token: "s3cret-shared-token", SharedName: "edge", SharedOwner: "Shared@Example.com"}
	rec := do(t, srv, http.MethodGet, "/api/hostds", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/hostds = %d %s", rec.Code, rec.Body.String())
	}
	var got []ui.HostdView
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	var edge *ui.HostdView
	for i := range got {
		if got[i].Name == "edge" {
			edge = &got[i]
		}
	}
	if edge == nil || !edge.Shared || edge.Owner != "shared@example.com" {
		t.Fatalf("GET /api/hostds = %+v, want a shared edge row owned by shared@example.com", got)
	}
	if strings.Contains(rec.Body.String(), "s3cret-shared-token") {
		t.Errorf("the listing carries the shared token: %s", rec.Body.String())
	}
	client.Config.HostdRemote = &hostd.Remote{}
	rec = do(t, srv, http.MethodGet, "/api/hostds", "")
	if strings.Contains(rec.Body.String(), `"shared":true`) {
		t.Errorf("with no shared token, GET /api/hostds = %s, want no shared row", rec.Body.String())
	}
}

// GET /api/tasks/{id}/attempts/{number}/capability-audit carries each
// record's hostd name when its capability server ran on a grain hostd,
// so the UI and API audit match task_run_capability_audit.hostd.
func TestAttemptCapabilityAuditIncludesHostd(t *testing.T) {
	srv, client := testServer(t)
	ctx := context.Background()
	task, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "host-script on h2", NoRepo: true, Approved: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := client.Store.StartRun(ctx, model.Run{
		ID: task.ID + "-1", TaskID: task.ID, Attempt: 1, StartedAt: now,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := client.Store.AppendRunCapabilityAudit(ctx, task.ID+"-1", []model.CapabilityAuditRecord{
		{Grant: "https://grain.dev/capabilities/host-script#host-script", AttemptID: "grain-" + task.ID + "-1-0", At: now, Hostd: "h2", Record: `{"script":"whoami","exit_code":0}`},
		{Grant: "https://grain.dev/capabilities/config-map#config-map", AttemptID: "grain-" + task.ID + "-1-1", At: now, Record: `{"map":"env"}`},
	}); err != nil {
		t.Fatal(err)
	}
	rec := do(t, srv, http.MethodGet, "/api/tasks/"+task.ID+"/attempts/1/capability-audit", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET capability-audit = %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Records []ui.CapabilityAuditEntry `json:"records"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Records) != 2 || got.Records[0].Hostd != "h2" || got.Records[1].Hostd != "" {
		t.Fatalf("capability-audit records = %+v, want [hostd=h2, hostd='']", got.Records)
	}
}

func TestHostCLISnippetGatesEnvOpenOnFeature(t *testing.T) {
	modern := hostd.ThisHello("v3.5.0")
	for _, fw := range []string{"claude", "codex"} {
		got := ui.HostCLISnippet(fw, modern)
		if !strings.Contains(got, `env: ["*"]`+"\n") || !strings.Contains(got, "paths_under: [~/.grain/hostd-stage]\n") {
			t.Errorf("HostCLISnippet(%q, modern) =\n%s", fw, got)
		}
	}
	if got := ui.HostCLISnippet("antigravity", modern); !strings.Contains(got, `env: [HOME, "*"]`+"\n") {
		t.Errorf("HostCLISnippet(antigravity, modern) =\n%s", got)
	}

	older := hostd.Hello{Protocol: 4, Features: []hostd.Feature{hostd.FeatureStart, hostd.FeatureStage, hostd.FeatureFeatures}}
	for _, fw := range []string{"claude", "codex", "antigravity"} {
		got := ui.HostCLISnippet(fw, older)
		if strings.Contains(got, `"*"`) || strings.Contains(got, "~/") {
			t.Errorf("HostCLISnippet(%q, older) wrote env_open syntax for a host without env_open:\n%s", fw, got)
		}
	}
}
