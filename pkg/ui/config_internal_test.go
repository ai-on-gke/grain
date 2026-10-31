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

	"github.com/ai-on-gke/grain/pkg/staterepo"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// In internal mode the deployment's own grain/config is the only write
// path for non-bootstrap configuration from the API, while Models and
// Sandboxes settings are still written directly.
func TestInternalModeWritesBootstrapDirectlyAndRefusesTheRest(t *testing.T) {
	t.Parallel()
	fake := &fakeStateRepo{status: ui.StateRepoStatus{Branch: "main"}}
	srv := stateRepoServer(t, fake)

	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPut, "/api/deployment/agent-backends/c", `{"framework":"claude","models":["claude-opus-5-5"]}`},
		{http.MethodPut, "/api/settings", `{"maxWorkers":4}`},
		{http.MethodPut, "/api/deployment", `{"defaultModel":"claude-opus-5-5"}`},
		{http.MethodDelete, "/api/deployment/agent-backends/c", ``},
	} {
		if rec := do(t, srv, tc.method, tc.path, tc.body); rec.Code >= 300 {
			t.Fatalf("bootstrap %s %s: status %d: %s", tc.method, tc.path, rec.Code, rec.Body)
		}
	}

	for _, tc := range []struct{ method, path, body, file string }{
		{http.MethodPost, "/api/templates", `{"name":"T","title":"Fresh title"}`, "settings/templates.yml"},
		{http.MethodPost, "/api/boards", `{"name":"Board"}`, "tables/board.json"},
		{http.MethodPut, "/api/settings", `{"promptExtension":"be nice"}`, "tables/grain_config.json"},
		{http.MethodPut, "/api/settings", `{"maxWorkers":5,"environmentName":"prod"}`, "tables/grain_config.json"},
		{http.MethodPut, "/api/deployment", `{"targetRepos":["acme/widgets"]}`, "tables/grain_config.json"},
	} {
		rec := do(t, srv, tc.method, tc.path, tc.body)
		var got configRefusalBody
		if rec.Code != http.StatusConflict || json.Unmarshal(rec.Body.Bytes(), &got) != nil {
			t.Fatalf("%s %s %s: status %d: %s", tc.method, tc.path, tc.body, rec.Code, rec.Body)
		}
		if got.Code != ui.ConfigManagedExternallyCode || got.Repo != "grain/config" || len(got.Change) == 0 {
			t.Fatalf("%s %s: refusal %+v", tc.method, tc.path, got)
		}
		found := false
		for _, p := range got.Path {
			found = found || p == tc.file
		}
		if !found {
			t.Fatalf("%s %s: paths %v, want %s", tc.method, tc.path, got.Path, tc.file)
		}
	}

	// The refused mixed write wrote nothing, its bootstrap half included.
	rec := do(t, srv, http.MethodGet, "/api/settings", "")
	if !strings.Contains(rec.Body.String(), `"maxWorkers":4`) {
		t.Fatalf("settings after the refused write: %s", rec.Body)
	}
}

// grain's own code -- a context no API request carried -- still writes
// directly: only an API caller is sent to the config repository.
func TestInternalModeLetsGrainsOwnWritesThrough(t *testing.T) {
	t.Parallel()
	_, client := testServer(t)
	storeSettingsRow(t, client)
	client.Config.StateRepo = &fakeStateRepo{status: ui.StateRepoStatus{Branch: "main"}}
	if _, err := client.CreateBoard(context.Background(), ui.CreateBoardRequest{Name: "Internal"}); err != nil {
		t.Fatalf("a write from grain's own code was refused: %v", err)
	}
}

// openerStateRepo is a config repository that opens pull requests.
type openerStateRepo struct {
	*fakeStateRepo
	got []ui.ConfigPullRequestRequest
}

func (o *openerStateRepo) OpenConfigPullRequest(_ context.Context, req ui.ConfigPullRequestRequest) (ui.ConfigPullRequest, error) {
	o.got = append(o.got, req)
	return ui.ConfigPullRequest{TaskID: "7", Number: 7, Branch: "grain/task-7", Base: "main"}, nil
}

// The refusal's own change, sent back as it is, opens the pull request;
// anything that is not a refusal's change is refused before it reaches
// the repository.
func TestOpenConfigPullRequestTakesTheRefusalsChange(t *testing.T) {
	t.Parallel()
	opener := &openerStateRepo{fakeStateRepo: &fakeStateRepo{status: ui.StateRepoStatus{Branch: "main"}}}
	_, client := testServer(t)
	storeSettingsRow(t, client)
	client.Config.StateRepo = opener
	srv := ui.NewServerWithClient(client)

	rec := do(t, srv, http.MethodPost, "/api/templates", `{"name":"T","title":"Fresh title"}`)
	var refusal configRefusalBody
	if rec.Code != http.StatusConflict || json.Unmarshal(rec.Body.Bytes(), &refusal) != nil {
		t.Fatalf("refusal: %d %s", rec.Code, rec.Body)
	}
	body, _ := json.Marshal(map[string]any{"title": "Add template T", "changes": refusal.Change})
	rec = do(t, srv, http.MethodPost, "/api/state-repo/pull-requests", string(body))
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"branch":"grain/task-7"`) {
		t.Fatalf("open: %d %s", rec.Code, rec.Body)
	}
	if len(opener.got) != 1 || opener.got[0].Title != "Add template T" || len(opener.got[0].Changes) != len(refusal.Change) {
		t.Fatalf("the repository was asked %+v", opener.got)
	}

	for _, bad := range []string{
		`{"changes":[]}`,
		`{"changes":[{"path":"../etc/passwd","op":"modify","patch":"x"}]}`,
		`{"changes":[{"path":".git/config","op":"modify","patch":"x"}]}`,
		`{"changes":[{"path":"settings/templates.yml","op":"rename","patch":"x"}]}`,
	} {
		if rec := do(t, srv, http.MethodPost, "/api/state-repo/pull-requests", bad); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", bad, rec.Code, rec.Body)
		}
	}
	if len(opener.got) != 1 {
		t.Fatalf("a refused change reached the repository: %+v", opener.got)
	}

	client.Config.StateRepo = nil
	if rec := do(t, srv, http.MethodPost, "/api/state-repo/pull-requests", string(body)); rec.Code != http.StatusNotImplemented {
		t.Fatalf("no config repository: %d %s", rec.Code, rec.Body)
	}
}

// An operator can opt this host out of the config repository write path
// (state-repo.json's directConfigWrites), and the API writes directly
// again.
func TestDirectConfigWritesLetsTheAPIWriteInInternalMode(t *testing.T) {
	t.Parallel()
	_, client := testServer(t)
	storeSettingsRow(t, client)
	client.Config.StateRepo = &fakeStateRepo{status: ui.StateRepoStatus{Branch: "main"}}
	client.Config.DataDir = t.TempDir()
	srv := ui.NewServerWithClient(client)
	if rec := do(t, srv, http.MethodPost, "/api/boards", `{"name":"Refused"}`); rec.Code != http.StatusConflict {
		t.Fatalf("before opting out: %d %s", rec.Code, rec.Body)
	}
	if err := staterepo.SaveSettings(client.Config.DataDir, staterepo.Settings{DirectConfigWrites: true}); err != nil {
		t.Fatal(err)
	}
	if rec := do(t, srv, http.MethodPost, "/api/boards", `{"name":"Direct"}`); rec.Code >= 300 {
		t.Fatalf("after opting out: %d %s", rec.Code, rec.Body)
	}
}

// GET /api/config says when the API refuses config-repo-only settings,
// so the UI draws them read-only exactly then: in internal mode, not
// after the directConfigWrites opt-out, and never without a config
// repository at all (`grain demo`).
func TestConfigSaysWhenSettingsAreConfigRepoOnly(t *testing.T) {
	t.Parallel()
	configRepoOnly := func(srv *ui.Server) bool {
		t.Helper()
		rec := do(t, srv, http.MethodGet, "/api/config", "")
		var got struct {
			ConfigRepoOnly bool `json:"configRepoOnly"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil {
			t.Fatalf("GET /api/config: %d %s", rec.Code, rec.Body)
		}
		return got.ConfigRepoOnly
	}

	_, bare := testServer(t)
	if configRepoOnly(ui.NewServerWithClient(bare)) {
		t.Fatal("configRepoOnly with no config repository")
	}

	_, client := testServer(t)
	client.Config.StateRepo = &fakeStateRepo{status: ui.StateRepoStatus{Branch: "main"}}
	client.Config.DataDir = t.TempDir()
	srv := ui.NewServerWithClient(client)
	if !configRepoOnly(srv) {
		t.Fatal("configRepoOnly false in internal mode")
	}
	if err := staterepo.SaveSettings(client.Config.DataDir, staterepo.Settings{DirectConfigWrites: true}); err != nil {
		t.Fatal(err)
	}
	if configRepoOnly(srv) {
		t.Fatal("configRepoOnly after the directConfigWrites opt-out")
	}
}
