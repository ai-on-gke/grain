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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// agentStateRepoServer is stateRepoServer, keeping the client so the test
// can seed acting tasks and credentials.
func agentStateRepoServer(t *testing.T, fake ui.StateRepoManager) (*ui.Server, *ui.Client) {
	t.Helper()
	_, client := testServer(t)
	storeSettingsRow(t, client)
	credentials, err := gitproxy.LoadCredentialSet(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	client.Config.Credentials = credentials
	client.Config.StateRepo = fake
	return ui.NewServerWithClient(client), client
}

// TestAgentAPIStateRepoRemoteNeedsCapabilitiesAdmin: POST
// /api/state-repo/remote repoints the config repository, turns
// require_verified off and accepts held commits. A person needs an
// unlimited role for it (task 3965), so an agent's api:settings:write is
// not enough on its own: it needs api:capabilities:admin as well (task
// 4007), and the manager never hears from a call that lacks it.
func TestAgentAPIStateRepoRemoteNeedsCapabilitiesAdmin(t *testing.T) {
	fake := &fakeStateRepo{status: ui.StateRepoStatus{Branch: "main"}}
	srv, client := agentStateRepoServer(t, fake)
	seedAPITask(t, client, "1", "acme/widgets", "api:settings:write")
	seedAPITask(t, client, "2", "acme/widgets", "api:settings:write", "api:capabilities:admin@acme/widgets")
	seedAPITask(t, client, "3", "acme/widgets", "api:settings:write", "api:capabilities:admin")
	seedAPITask(t, client, "4", "acme/widgets", "api:capabilities:admin")

	for _, body := range []string{
		`{"remote":"https://github.com/attacker/grain-config","branch":"main"}`,
		`{"remote":"https://github.com/acme/grain-config","branch":"main","requireVerified":false}`,
		`{"remote":"https://github.com/acme/grain-config","branch":"main","acceptCommit":"0123456789abcdef0123456789abcdef01234567"}`,
		`{"none":true}`,
	} {
		code, resp := asAgent(t, srv, "1", http.MethodPost, "/api/state-repo/remote", body, "repoint config")
		if code != http.StatusForbidden || !strings.Contains(resp, "capabilities:admin") {
			t.Errorf("api:settings:write alone reached POST /api/state-repo/remote with %s: %d %s", body, code, resp)
		}
		// A repository-limited capabilities:admin is not the deployment's.
		code, resp = asAgent(t, srv, "2", http.MethodPost, "/api/state-repo/remote", body, "repoint config")
		if code != http.StatusForbidden || !strings.Contains(resp, "capabilities:admin") {
			t.Errorf("capabilities:admin@acme/widgets reached POST /api/state-repo/remote with %s: %d %s", body, code, resp)
		}
		// capabilities:admin does not stand in for the route's own scope.
		code, resp = asAgent(t, srv, "4", http.MethodPost, "/api/state-repo/remote", body, "repoint config")
		if code != http.StatusForbidden || !strings.Contains(resp, "settings:write") {
			t.Errorf("capabilities:admin without settings:write reached POST /api/state-repo/remote with %s: %d %s", body, code, resp)
		}
	}
	if len(fake.remotes) != 0 {
		t.Fatalf("a refused agent call reached the manager: %+v", fake.remotes)
	}

	// Both scopes, deployment-wide, still need a reason: settings:write is
	// a dangerous scope.
	if code, resp := asAgent(t, srv, "3", http.MethodPost, "/api/state-repo/remote", `{"none":true}`, ""); code != http.StatusForbidden ||
		!strings.Contains(resp, "reason") {
		t.Errorf("a call with no reason was not refused for it: %d %s", code, resp)
	}
	if code, resp := asAgent(t, srv, "3", http.MethodPost, "/api/state-repo/remote", `{"none":true}`, "back to the internal repository"); code != http.StatusOK {
		t.Fatalf("settings:write with capabilities:admin was refused: %d %s", code, resp)
	}
	if len(fake.remotes) != 1 || fake.remotes[0].Remote != "" {
		t.Fatalf("the manager was asked for %+v", fake.remotes)
	}
}

// TestExternalAgentStateRepoRemoteNeedsCapabilitiesAdmin holds an external
// agent credential to the same rule as an in-grain task.
func TestExternalAgentStateRepoRemoteNeedsCapabilitiesAdmin(t *testing.T) {
	fake := &fakeStateRepo{status: ui.StateRepoStatus{Branch: "main"}}
	srv, client := agentStateRepoServer(t, fake)
	ctx := context.Background()
	enableExternalAgentAPI(t, client.Store)
	humanCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com", Source: model.SourceIAP})

	call := func(scopes ...string) *httptest.ResponseRecorder {
		t.Helper()
		issued, err := client.CreateExternalAgentToken(humanCtx, ui.CreateExternalAgentTokenRequest{
			Name:   "state-repo-" + strings.ReplaceAll(strings.Join(scopes, "-"), ":", "-"),
			Scopes: scopes,
		})
		if err != nil {
			t.Fatal(err)
		}
		return doWithHeaders(t, srv, http.MethodPost, "/api/state-repo/remote",
			`{"remote":"https://github.com/attacker/grain-config","branch":"main"}`,
			map[string]string{
				"Authorization":       "Bearer " + issued.Token,
				"Content-Type":        "application/json",
				ui.ActingReasonHeader: "repoint config",
			})
	}

	if rec := call("settings:write"); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "capabilities:admin") {
		t.Errorf("an external settings:write credential reached POST /api/state-repo/remote: %d %s", rec.Code, rec.Body)
	}
	if len(fake.remotes) != 0 {
		t.Fatalf("a refused external call reached the manager: %+v", fake.remotes)
	}
	if rec := call("settings:write", "capabilities:admin"); rec.Code == http.StatusForbidden && strings.Contains(rec.Body.String(), "capabilities:admin") {
		t.Errorf("settings:write with capabilities:admin was refused for capabilities:admin: %d %s", rec.Code, rec.Body)
	}
}
