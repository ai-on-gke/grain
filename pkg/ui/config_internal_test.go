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
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// In internal mode, settings are writable by default (settings_locked == false).
// Once settings_locked is enabled, the deployment's own grain/config is the only
// write path for non-bootstrap configuration from the API, while Models and
// Sandboxes settings are still written directly and a human operator can unlock
// settings again from the UI/API.
func TestInternalModeWritesBootstrapDirectlyAndRefusesTheRest(t *testing.T) {
	t.Parallel()
	fake := &fakeStateRepo{status: ui.StateRepoStatus{Branch: "main"}}
	srv := stateRepoServer(t, fake)

	// By default (settings_locked == false), non-bootstrap writes succeed directly.
	if rec := do(t, srv, http.MethodPut, "/api/settings", `{"environmentName":"staging"}`); rec.Code >= 300 {
		t.Fatalf("unlocked PUT /api/settings: status %d: %s", rec.Code, rec.Body)
	}

	// Lock settings via the API.
	if rec := do(t, srv, http.MethodPut, "/api/settings", `{"settingsLocked":true}`); rec.Code >= 300 {
		t.Fatalf("locking settings: status %d: %s", rec.Code, rec.Body)
	}

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

	// A human operator can unlock settings again via PUT /api/settings (or PUT /api/config).
	if rec := do(t, srv, http.MethodPut, "/api/config", `{"settings_locked":false}`); rec.Code >= 300 {
		t.Fatalf("unlocking settings via PUT /api/config: status %d: %s", rec.Code, rec.Body)
	}
	if rec := do(t, srv, http.MethodPost, "/api/boards", `{"name":"Board"}`); rec.Code >= 300 {
		t.Fatalf("POST /api/boards after unlocking: status %d: %s", rec.Code, rec.Body)
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
	// err, when set, is what opening answers.
	err error
}

func (o *openerStateRepo) OpenConfigPullRequest(_ context.Context, req ui.ConfigPullRequestRequest) (ui.ConfigPullRequest, error) {
	o.got = append(o.got, req)
	if o.err != nil {
		return ui.ConfigPullRequest{}, o.err
	}
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
	if rec := do(t, srv, http.MethodPut, "/api/settings", `{"settingsLocked":true}`); rec.Code >= 300 {
		t.Fatalf("locking settings: %d %s", rec.Code, rec.Body)
	}

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

// A change the pre-review suite refuses answers 422 with its report, and
// one that no longer applies to the base says so in its code, so the UI
// can tell the person to make the change again rather than to fix it.
func TestOpenConfigPullRequestAnswersAnInvalidChangeWithItsReport(t *testing.T) {
	t.Parallel()
	change := `{"changes":[{"path":"tables/grain_config.json","op":"modify","patch":"x"}]}`
	for _, tc := range []struct {
		name     string
		invalid  *ui.ConfigPullRequestInvalid
		wantCode string
	}{
		{"liveness", &ui.ConfigPullRequestInvalid{Err: staterepo.ErrDispatchLocked, Validation: staterepo.CandidateReport{Liveness: []string{"max_parallel is 0, which lets no run start"}}}, ""},
		{"stale", &ui.ConfigPullRequestInvalid{Err: errors.New("the change does not apply to main"), Stale: true}, ui.ConfigChangeStale},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, client := testServer(t)
			storeSettingsRow(t, client)
			client.Config.StateRepo = &openerStateRepo{fakeStateRepo: &fakeStateRepo{status: ui.StateRepoStatus{Branch: "main"}}, err: tc.invalid}
			rec := do(t, ui.NewServerWithClient(client), http.MethodPost, "/api/state-repo/pull-requests", change)
			var got struct {
				Error      string                    `json:"error"`
				Code       string                    `json:"code"`
				Validation staterepo.CandidateReport `json:"validation"`
			}
			if rec.Code != http.StatusUnprocessableEntity || json.Unmarshal(rec.Body.Bytes(), &got) != nil {
				t.Fatalf("open: %d %s", rec.Code, rec.Body)
			}
			if got.Code != tc.wantCode || got.Error == "" || !slices.Equal(got.Validation.Liveness, tc.invalid.Validation.Liveness) {
				t.Fatalf("422 body = %+v, want code %q and the report", got, tc.wantCode)
			}
		})
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
	if rec := do(t, srv, http.MethodPut, "/api/settings", `{"settingsLocked":true}`); rec.Code >= 300 {
		t.Fatalf("locking settings: %d %s", rec.Code, rec.Body)
	}
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

// GET /api/settings, and the answer to a write of the lock, carry the
// same configRepoOnly as GET /api/config for the row they answer with,
// which the Settings page draws read-only on (settings-mutability D-10):
// true once settings_locked is on, false again under directConfigWrites.
func TestSettingsSayWhenTheyAreConfigRepoOnly(t *testing.T) {
	t.Parallel()
	_, client := testServer(t)
	storeSettingsRow(t, client)
	client.Config.StateRepo = &fakeStateRepo{status: ui.StateRepoStatus{Branch: "main"}}
	client.Config.DataDir = t.TempDir()
	srv := ui.NewServerWithClient(client)
	configRepoOnly := func(rec *httptest.ResponseRecorder) bool {
		t.Helper()
		var got struct {
			ConfigRepoOnly *bool `json:"configRepoOnly"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.ConfigRepoOnly == nil {
			t.Fatalf("settings: %d %s", rec.Code, rec.Body)
		}
		return *got.ConfigRepoOnly
	}
	if configRepoOnly(do(t, srv, http.MethodGet, "/api/settings", "")) {
		t.Fatal("configRepoOnly before settings_locked")
	}
	if !configRepoOnly(do(t, srv, http.MethodPut, "/api/settings", `{"settingsLocked":true}`)) {
		t.Fatal("the answer to locking says configRepoOnly false")
	}
	if !configRepoOnly(do(t, srv, http.MethodGet, "/api/settings", "")) {
		t.Fatal("configRepoOnly false once settings_locked is on")
	}
	if err := staterepo.SaveSettings(client.Config.DataDir, staterepo.Settings{DirectConfigWrites: true}); err != nil {
		t.Fatal(err)
	}
	if configRepoOnly(do(t, srv, http.MethodGet, "/api/settings", "")) {
		t.Fatal("configRepoOnly under directConfigWrites")
	}
}

// GET /api/config says when the API refuses config-repo-only settings,
// so the UI draws them read-only exactly then: when settings_locked is
// enabled in internal mode (not by default), not after the
// directConfigWrites opt-out, and never without a config repository at
// all (`grain demo`).
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
	storeSettingsRow(t, client)
	client.Config.StateRepo = &fakeStateRepo{status: ui.StateRepoStatus{Branch: "main"}}
	client.Config.DataDir = t.TempDir()
	srv := ui.NewServerWithClient(client)
	if configRepoOnly(srv) {
		t.Fatal("configRepoOnly true by default in internal mode when settings_locked is false")
	}
	if rec := do(t, srv, http.MethodPut, "/api/settings", `{"settingsLocked":true}`); rec.Code >= 300 {
		t.Fatalf("locking settings: %d %s", rec.Code, rec.Body)
	}
	if !configRepoOnly(srv) {
		t.Fatal("configRepoOnly false in internal mode when settings_locked is true")
	}
	if err := staterepo.SaveSettings(client.Config.DataDir, staterepo.Settings{DirectConfigWrites: true}); err != nil {
		t.Fatal(err)
	}
	if configRepoOnly(srv) {
		t.Fatal("configRepoOnly after the directConfigWrites opt-out")
	}
	// GET /api/settings says so too: the Settings page reads its lock
	// from there (settings-mutability.spec.md D-10).
	rec := do(t, srv, http.MethodGet, "/api/settings", "")
	var settings struct {
		SettingsLocked bool `json:"settingsLocked"`
		ConfigRepoOnly bool `json:"configRepoOnly"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &settings) != nil {
		t.Fatalf("GET /api/settings: %d %s", rec.Code, rec.Body)
	}
	if !settings.SettingsLocked || settings.ConfigRepoOnly {
		t.Fatalf("GET /api/settings after the opt-out: %+v, want settingsLocked but not configRepoOnly", settings)
	}
}

// On a deployment with no state repository (`grain demo`, or a standalone UI
// with Config.StateRepo == nil), settings_locked has no config repository to
// send writes to (settings-mutability.spec.md D-12): configRepoOnly stays
// false on GET/PUT /api/settings and GET /api/config, and non-bootstrap writes
// continue to succeed directly.
func TestSettingsLockedWithoutAStateRepositoryLeavesSettingsWritable(t *testing.T) {
	t.Parallel()
	_, client := testServer(t)
	storeSettingsRow(t, client)
	srv := ui.NewServerWithClient(client)

	rec := do(t, srv, http.MethodPut, "/api/settings", `{"settingsLocked":true}`)
	var putSettings struct {
		SettingsLocked bool `json:"settingsLocked"`
		ConfigRepoOnly bool `json:"configRepoOnly"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &putSettings) != nil {
		t.Fatalf("PUT /api/settings settingsLocked=true: %d %s", rec.Code, rec.Body)
	}
	if !putSettings.SettingsLocked || putSettings.ConfigRepoOnly {
		t.Fatalf("PUT /api/settings response = %+v, want settingsLocked=true configRepoOnly=false", putSettings)
	}

	rec = do(t, srv, http.MethodGet, "/api/config", "")
	var gotCfg struct {
		ConfigRepoOnly bool `json:"configRepoOnly"`
		SettingsLocked bool `json:"settingsLocked"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &gotCfg) != nil {
		t.Fatalf("GET /api/config: %d %s", rec.Code, rec.Body)
	}
	if gotCfg.ConfigRepoOnly || !gotCfg.SettingsLocked {
		t.Fatalf("GET /api/config = %+v, want configRepoOnly=false settingsLocked=true", gotCfg)
	}

	rec = do(t, srv, http.MethodPut, "/api/settings", `{"pollInterval":"8s"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"pollInterval":"8s"`) {
		t.Fatalf("non-bootstrap PUT /api/settings without StateRepo: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, srv, http.MethodPost, "/api/templates", `{"name":"T","title":"Standalone"}`); rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/templates without StateRepo: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, srv, http.MethodPut, "/api/repos/acme/widgets/settings", `{"promptExtension":"local"}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT /api/repos/acme/widgets/settings without StateRepo: %d %s", rec.Code, rec.Body)
	}

	rec = do(t, srv, http.MethodGet, "/api/settings", "")
	var getSettings struct {
		SettingsLocked bool   `json:"settingsLocked"`
		ConfigRepoOnly bool   `json:"configRepoOnly"`
		PollInterval   string `json:"pollInterval"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &getSettings) != nil {
		t.Fatalf("GET /api/settings: %d %s", rec.Code, rec.Body)
	}
	if !getSettings.SettingsLocked || getSettings.ConfigRepoOnly || getSettings.PollInterval != "8s" {
		t.Fatalf("GET /api/settings = %+v, want settingsLocked=true configRepoOnly=false pollInterval=8s", getSettings)
	}
}

type realSyncStateRepo struct {
	*fakeStateRepo
	repo   *staterepo.Repo
	client *ui.Client
	syncs  int
}

func (r *realSyncStateRepo) SyncAfterWrite(ctx context.Context) error {
	r.syncs++
	if _, err := staterepo.Apply(ctx, r.repo, r.client.Store.DB(), model.SchemaVersion); err != nil {
		return err
	}
	_, err := staterepo.Sync(ctx, r.repo, r.client.Store.DB(), model.SchemaVersion)
	return err
}

// With settings_repo configured and settings_locked: false (the default),
// editing default_capabilities, console_sessions, repo_config, schedules, and
// templates via the API succeeds, exports immediately to settings_repo, and
// persists across subsequent staterepo reconcile cycles.
func TestInternalModeEditsSyncToStateRepoAndPersistAcrossReconcileCycles(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, client := testServer(t)
	storeSettingsRow(t, client)
	stateDir := filepath.Join(t.TempDir(), "state-repo")
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: stateDir})
	if err != nil {
		t.Fatalf("staterepo.Open: %v", err)
	}
	if err := staterepo.Load(ctx, repo, client.Store.DB(), model.SchemaVersion); err != nil {
		t.Fatalf("staterepo.Load: %v", err)
	}
	syncer := &realSyncStateRepo{
		fakeStateRepo: &fakeStateRepo{status: ui.StateRepoStatus{Branch: "main"}},
		repo:          repo,
		client:        client,
	}
	client.Config.StateRepo = syncer
	srv := ui.NewServerWithClient(client)

	// 1. Edit default_capabilities via PUT /api/config and PUT /api/settings.
	capID := ui.OfferedCapabilities()[0].ID
	rec := do(t, srv, http.MethodPut, "/api/config", `{"default_capabilities":["`+capID+`"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /api/config default_capabilities: %d %s", rec.Code, rec.Body)
	}
	// 2. Edit console_sessions via POST /api/console-sessions.
	rec = do(t, srv, http.MethodPost, "/api/console-sessions", `{"name":"ops-director","title":"Ops Director"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/console-sessions: %d %s", rec.Code, rec.Body)
	}
	// 3. Edit repo_config via PUT /api/repos/acme/widgets/settings.
	rec = do(t, srv, http.MethodPut, "/api/repos/acme/widgets/settings", `{"promptExtension":"Always run go test","mergeOnGreen":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /api/repos/acme/widgets/settings: %d %s", rec.Code, rec.Body)
	}
	// 4. Edit schedules via POST /api/schedules.
	rec = do(t, srv, http.MethodPost, "/api/schedules", `{"title":"Hourly check","repo":"acme/widgets","recurrence":{"kind":"everyNHours","everyNHours":1}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/schedules: %d %s", rec.Code, rec.Body)
	}
	// 5. Edit templates via POST /api/templates.
	rec = do(t, srv, http.MethodPost, "/api/templates", `{"name":"triage","title":"Triage incoming issue"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/templates: %d %s", rec.Code, rec.Body)
	}

	if syncer.syncs < 5 {
		t.Fatalf("expected SyncAfterWrite on each settings mutation, got %d syncs", syncer.syncs)
	}

	// Verify the state repository files contain the exported changes before any reconcile tick.
	for _, wantFile := range []struct {
		path   string
		needle string
	}{
		{"tables/grain_config.json", capID},
		{"tables/grain_config.json", "Ops Director"},
		{"tables/repo_config.json", "Always run go test"},
		{"settings/schedules.yml", "Hourly check"},
		{"settings/templates.yml", "Triage incoming issue"},
	} {
		raw, err := os.ReadFile(filepath.Join(stateDir, filepath.FromSlash(wantFile.path)))
		if err != nil {
			t.Fatalf("reading exported %s: %v", wantFile.path, err)
		}
		if !strings.Contains(string(raw), wantFile.needle) {
			t.Fatalf("exported %s missing %q:\n%s", wantFile.path, wantFile.needle, raw)
		}
	}

	// Run multiple staterepo reconcile cycles (Apply + Sync) and verify UI edits are NOT overwritten.
	for cycle := range 2 {
		if _, err := staterepo.Apply(ctx, repo, client.Store.DB(), model.SchemaVersion); err != nil {
			t.Fatalf("cycle %d staterepo.Apply: %v", cycle, err)
		}
		if _, err := staterepo.Sync(ctx, repo, client.Store.DB(), model.SchemaVersion); err != nil {
			t.Fatalf("cycle %d staterepo.Sync: %v", cycle, err)
		}
	}

	cfg, err := client.Store.GetConfig(ctx)
	if err != nil || cfg == nil {
		t.Fatalf("GetConfig: %v (%v)", cfg, err)
	}
	if !slices.Contains(cfg.DefaultCapabilities, capID) {
		t.Fatalf("default_capabilities = %v after reconcile, want %q", cfg.DefaultCapabilities, capID)
	}
	if !slices.ContainsFunc(cfg.ConsoleSessions, func(s model.ConsoleSession) bool { return s.Title == "Ops Director" }) {
		t.Fatalf("console_sessions = %+v after reconcile, want Ops Director", cfg.ConsoleSessions)
	}
	rc, err := client.Store.GetRepoConfig(ctx, model.RepoRef{Owner: "acme", Name: "widgets"})
	if err != nil || rc == nil || rc.PromptExtension != "Always run go test" || !rc.MergeOnGreen {
		t.Fatalf("repo_config = %+v (err %v) after reconcile, want PromptExtension and MergeOnGreen", rc, err)
	}
	schedules, err := client.Store.ListSchedules(ctx)
	if err != nil || !slices.ContainsFunc(schedules, func(s model.Schedule) bool { return s.Title == "Hourly check" }) {
		t.Fatalf("schedules = %+v (err %v) after reconcile, want Hourly check", schedules, err)
	}
	templates, err := client.Store.ListTemplates(ctx)
	if err != nil || !slices.ContainsFunc(templates, func(tp model.Template) bool { return tp.Title == "Triage incoming issue" }) {
		t.Fatalf("templates = %+v (err %v) after reconcile, want Triage incoming issue", templates, err)
	}

	// Verify an agent cannot unlock settingsLocked once locked, while a human operator can.
	if rec := do(t, srv, http.MethodPut, "/api/config", `{"settings_locked":true}`); rec.Code != http.StatusOK {
		t.Fatalf("locking via PUT /api/config: %d %s", rec.Code, rec.Body)
	}
	agentReq := httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(`{"settings_locked":false}`))
	agentReq.Header.Set("Content-Type", "application/json")
	agentReq = agentReq.WithContext(ui.WithPrincipal(agentReq.Context(), model.Principal{Kind: model.PrincipalAgent, ID: "task-99"}))
	agentRec := httptest.NewRecorder()
	srv.ServeHTTP(agentRec, agentReq)
	if agentRec.Code != http.StatusConflict {
		t.Fatalf("expected agent to be refused (409) when unlocking settingsLocked, got %d: %s", agentRec.Code, agentRec.Body)
	}
	if rec := do(t, srv, http.MethodPut, "/api/config", `{"settings_locked":false}`); rec.Code != http.StatusOK {
		t.Fatalf("human unlocking via PUT /api/config: %d %s", rec.Code, rec.Body)
	}
}
