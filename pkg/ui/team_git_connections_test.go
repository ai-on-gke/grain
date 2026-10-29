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
	"os"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// TestTeamGitConnectionsKeepHasGitCredential is grain/task-3079: adding a
// team's second git connection moves its legacy GitCredential into
// GitConnections and blanks it, and the UI then said the team had "no
// GitHub credential". hasGitCredential is what it reads now, and it stays
// true for as long as a connection on the default host is left.
func TestTeamGitConnectionsKeepHasGitCredential(t *testing.T) {
	client, store, _ := testClient(t)
	credentials, err := gitproxy.LoadCredentialSet(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	client.Config.Credentials = credentials
	srv := ui.NewServerWithClient(client)
	ctx := context.Background()

	if err := store.PutConfig(ctx, model.Config{PollInterval: model.DefaultPollInterval}); err != nil {
		t.Fatal(err)
	}
	admitUser(t, store, "alice")
	const legacy = "grain-team-ZGVmYXVsdA"
	if err := credentials.SetPAT(legacy, "ghp_legacy"); err != nil {
		t.Fatalf("SetPAT: %v", err)
	}
	if err := store.UpdateConfig(ctx, func(c *model.Config) error { c.GitCredential = legacy; return nil }); err != nil {
		t.Fatalf("PutTeam: %v", err)
	}
	alice := map[string]string{"Content-Type": "application/json", "X-Grain-User": "alice"}

	decode := func(body []byte) ui.Team {
		t.Helper()
		var team ui.Team
		if err := json.Unmarshal(body, &team); err != nil {
			t.Fatalf("Unmarshal team: %v", err)
		}
		return team
	}
	get := func() ui.Team {
		t.Helper()
		rec := doWithHeaders(t, srv, http.MethodGet, "/api/deployment", "", alice)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET team: %d %s", rec.Code, rec.Body)
		}
		return decode(rec.Body.Bytes())
	}
	if !get().HasGitCredential {
		t.Fatal("a team with a legacy credential reads hasGitCredential=false")
	}

	rec := doWithHeaders(t, srv, http.MethodPost, "/api/deployment/git-connections",
		`{"host": "github.com", "kind": "github", "scope": "acme", "type": "pat", "value": "ghp_acme"}`, alice)
	if rec.Code != http.StatusOK {
		t.Fatalf("AddTeamGitConnection: %d %s", rec.Code, rec.Body)
	}
	added := decode(rec.Body.Bytes())
	if added.GitCredential != "" || len(added.GitConnections) != 2 {
		t.Fatalf("want the legacy credential migrated into 2 connections, got gitCredential=%q connections=%+v", added.GitCredential, added.GitConnections)
	}
	if !added.HasGitCredential || !get().HasGitCredential {
		t.Fatal("adding a second connection left the team reading hasGitCredential=false")
	}
	scoped := added.GitConnections[1].Credential

	// Removing the migrated default leaves the owner-scoped one on the
	// same host: still covered.
	rec = doWithHeaders(t, srv, http.MethodDelete, "/api/deployment/git-connections/"+legacy, "", alice)
	if rec.Code != http.StatusOK {
		t.Fatalf("DeleteTeamGitConnection: %d %s", rec.Code, rec.Body)
	}
	if !decode(rec.Body.Bytes()).HasGitCredential || !get().HasGitCredential {
		t.Fatal("removing one of two default-host connections left hasGitCredential=false")
	}

	rec = doWithHeaders(t, srv, http.MethodDelete, "/api/deployment/git-connections/"+scoped, "", alice)
	if rec.Code != http.StatusOK {
		t.Fatalf("DeleteTeamGitConnection: %d %s", rec.Code, rec.Body)
	}
	if decode(rec.Body.Bytes()).HasGitCredential || get().HasGitCredential {
		t.Fatal("a team with no connections left reads hasGitCredential=true")
	}

	// A connection on another host only does not cover the default one.
	rec = doWithHeaders(t, srv, http.MethodPost, "/api/deployment/git-connections",
		`{"host": "ghe.example.com", "kind": "github", "type": "pat", "value": "ghp_ghe"}`, alice)
	if rec.Code != http.StatusOK {
		t.Fatalf("AddTeamGitConnection: %d %s", rec.Code, rec.Body)
	}
	if decode(rec.Body.Bytes()).HasGitCredential || get().HasGitCredential {
		t.Fatal("a team whose only connection is on ghe.example.com reads hasGitCredential=true for the default host")
	}
}

// TestAddTeamGitConnectionRefusesNoCredential is the 500 the 10-11 §2
// walkthrough (docs/test-runs/2026-10-11-team-git-credential.md) hit:
// adding a connection of type "none", or naming the "anonymous" selector,
// reached the credential set's reserved-name refusal and came back as an
// internal error. It is the client's mistake, a 400, and leaves the team
// with no connection -- so it does not read hasGitCredential=true either.
func TestAddTeamGitConnectionRefusesNoCredential(t *testing.T) {
	client, store, _ := testClient(t)
	credentials, err := gitproxy.LoadCredentialSet(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	client.Config.Credentials = credentials
	srv := ui.NewServerWithClient(client)
	ctx := context.Background()

	if err := store.PutConfig(ctx, model.Config{PollInterval: model.DefaultPollInterval}); err != nil {
		t.Fatal(err)
	}
	admitUser(t, store, "alice")
	alice := map[string]string{"Content-Type": "application/json", "X-Grain-User": "alice"}
	root := map[string]string{"Content-Type": "application/json", "X-Grain-User": "root"}

	for _, tc := range []struct {
		name    string
		body    string
		headers map[string]string
	}{
		{"type none", `{"host": "github.com", "type": "none"}`, alice},
		{"type NONE", `{"type": " NONE "}`, alice},
		{"anonymous selector", `{"host": "github.com", "credential": "anonymous"}`, root},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := doWithHeaders(t, srv, http.MethodPost, "/api/deployment/git-connections", tc.body, tc.headers)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("POST git-connections %s: got %d %s, want 400", tc.body, rec.Code, rec.Body)
			}
			rec = doWithHeaders(t, srv, http.MethodGet, "/api/deployment", "", alice)
			var team ui.Team
			if err := json.Unmarshal(rec.Body.Bytes(), &team); err != nil {
				t.Fatalf("Unmarshal team: %v", err)
			}
			if len(team.GitConnections) != 0 || team.HasGitCredential {
				t.Fatalf("a refused connection left the team with connections=%+v hasGitCredential=%v", team.GitConnections, team.HasGitCredential)
			}
		})
	}
}

func TestAddTeamGitConnectionRefusesDuplicateHostAndScope(t *testing.T) {
	client, store, _ := testClient(t)
	credDir := t.TempDir()
	credentials, err := gitproxy.LoadCredentialSet(credDir)
	if err != nil {
		t.Fatal(err)
	}
	client.Config.Credentials = credentials
	srv := ui.NewServerWithClient(client)
	ctx := context.Background()

	if err := store.PutConfig(ctx, model.Config{PollInterval: model.DefaultPollInterval}); err != nil {
		t.Fatal(err)
	}
	admitUser(t, store, "alice")
	alice := map[string]string{"Content-Type": "application/json", "X-Grain-User": "alice"}

	credFileNames := func(t *testing.T) []string {
		t.Helper()
		entries, err := os.ReadDir(credDir)
		if err != nil {
			t.Fatalf("ReadDir(%s): %v", credDir, err)
		}
		var names []string
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "grain-git-") {
				names = append(names, entry.Name())
			}
		}
		return names
	}

	// 1. Add the first host-wide connection on git.example.test.
	rec := doWithHeaders(t, srv, http.MethodPost, "/api/deployment/git-connections",
		`{"host": "git.example.test", "kind": "git", "type": "pat", "value": "tok-first"}`, alice)
	if rec.Code != http.StatusOK {
		t.Fatalf("first POST git-connections: got %d %s, want 200", rec.Code, rec.Body)
	}
	var team ui.Team
	if err := json.Unmarshal(rec.Body.Bytes(), &team); err != nil {
		t.Fatalf("Unmarshal team: %v", err)
	}
	if len(team.GitConnections) != 1 {
		t.Fatalf("GitConnections len = %d, want 1", len(team.GitConnections))
	}
	firstCred := team.GitConnections[0].Credential
	filesAfterFirst := credFileNames(t)
	if len(filesAfterFirst) == 0 {
		t.Fatalf("expected credential files in %s after first add", credDir)
	}

	// 2. Posting the same host (including non-normalized https:// URL) and empty scope is refused with 400
	// and leaves no orphaned credential file behind.
	for _, dupBody := range []string{
		`{"host": "git.example.test", "kind": "git", "type": "pat", "value": "tok-second"}`,
		`{"host": "https://GIT.EXAMPLE.TEST/", "scope": " / ", "kind": "git", "type": "pat", "value": "tok-third"}`,
	} {
		rec = doWithHeaders(t, srv, http.MethodPost, "/api/deployment/git-connections", dupBody, alice)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("duplicate POST git-connections (%s): got %d %s, want 400", dupBody, rec.Code, rec.Body)
		}
		var errBody struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &errBody); err != nil {
			t.Fatalf("Unmarshal error response: %v (%s)", err, rec.Body)
		}
		if !strings.Contains(errBody.Error, firstCred) || !strings.Contains(errBody.Error, "remove it or edit its scope first") {
			t.Errorf("duplicate error = %q, want mention of %q and instructions to remove or edit scope", errBody.Error, firstCred)
		}
		if got := credFileNames(t); strings.Join(got, ",") != strings.Join(filesAfterFirst, ",") {
			t.Fatalf("credential files after refused duplicate = %v, want %v (no orphaned credential file)", got, filesAfterFirst)
		}
	}

	// 3. Scoped connections on the same host with different owners/repos stay allowed.
	rec = doWithHeaders(t, srv, http.MethodPost, "/api/deployment/git-connections",
		`{"host": "git.example.test", "scope": "acme", "kind": "git", "type": "pat", "value": "tok-acme"}`, alice)
	if rec.Code != http.StatusOK {
		t.Fatalf("scoped owner POST git-connections: got %d %s, want 200", rec.Code, rec.Body)
	}
	rec = doWithHeaders(t, srv, http.MethodPost, "/api/deployment/git-connections",
		`{"host": "git.example.test", "scope": "acme/widgets", "kind": "git", "type": "pat", "value": "tok-widgets"}`, alice)
	if rec.Code != http.StatusOK {
		t.Fatalf("scoped repo POST git-connections: got %d %s, want 200", rec.Code, rec.Body)
	}
	filesAfterScoped := credFileNames(t)

	// 4. Duplicate scoped connection (case-insensitive scope normalization) is also refused with 400 and no orphan.
	rec = doWithHeaders(t, srv, http.MethodPost, "/api/deployment/git-connections",
		`{"host": "git.example.test", "scope": "ACME/Widgets", "kind": "git", "type": "pat", "value": "tok-dup-scoped"}`, alice)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("duplicate scoped POST git-connections: got %d %s, want 400", rec.Code, rec.Body)
	}
	if got := credFileNames(t); strings.Join(got, ",") != strings.Join(filesAfterScoped, ",") {
		t.Fatalf("credential files after refused scoped duplicate = %v, want %v", got, filesAfterScoped)
	}
}

func TestTeamGitConnectionsWithoutCredentialDirectory(t *testing.T) {
	client, store, _ := testClient(t)
	// Leave client.Config.Credentials nil (as in `grain demo -unconfigured`).
	srv := ui.NewServerWithClient(client)
	ctx := context.Background()

	if err := store.PutConfig(ctx, model.Config{PollInterval: model.DefaultPollInterval}); err != nil {
		t.Fatal(err)
	}
	admitUser(t, store, "alice")
	alice := map[string]string{"Content-Type": "application/json", "X-Grain-User": "alice"}

	rec := doWithHeaders(t, srv, http.MethodDelete, "/api/deployment/git-connections/grain-git-c-1", "", alice)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("DELETE git-connections without credential dir: got %d %s, want 404", rec.Code, rec.Body)
	}
	var errBody struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &errBody); err != nil {
		t.Fatalf("Unmarshal error response: %v (%s)", err, rec.Body)
	}
	wantMsg := "this UI has no local GitHub credential directory"
	if !strings.Contains(errBody.Error, wantMsg) {
		t.Errorf("DELETE error = %q, want substring %q", errBody.Error, wantMsg)
	}
}
