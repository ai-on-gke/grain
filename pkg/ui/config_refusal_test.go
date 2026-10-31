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
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
	"github.com/ai-on-gke/grain/pkg/ui"
)

type fileChange struct {
	Path     string `json:"path"`
	Op       string `json:"op"`
	Patch    string `json:"patch"`
	Fragment string `json:"fragment"`
}

type configRefusalBody struct {
	Error  string       `json:"error"`
	Code   string       `json:"code"`
	Repo   string       `json:"repo"`
	Branch string       `json:"branch"`
	Path   []string     `json:"path"`
	Change []fileChange `json:"change"`
	Docs   string       `json:"docs"`
	URL    string       `json:"url"`
}

func seedID(t *testing.T, rec interface{ Result() *http.Response }, body string) string {
	t.Helper()
	var got struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil || got.ID == "" {
		t.Fatalf("seeding: no id in %s (%v)", body, err)
	}
	return got.ID
}

// In external mode every configuration write is refused with the file
// that holds the setting and, where the write could be rehearsed, the
// patch that makes it -- carrying the value the request asked for.
func TestExternalModeRefusesEachConfigWriteWithTheEditToCommit(t *testing.T) {
	t.Parallel()
	fake := &fakeStateRepo{status: ui.StateRepoStatus{Branch: "main"}}
	// Seeded with no config repository wired at all: an internal one
	// refuses these writes from the API too.
	_, client := testServer(t)
	storeSettingsRow(t, client)
	srv := ui.NewServerWithClient(client)

	create := func(path, body string) string {
		t.Helper()
		rec := do(t, srv, http.MethodPost, path, body)
		if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
			t.Fatalf("seeding %s: status %d: %s", path, rec.Code, rec.Body)
		}
		return seedID(t, rec, rec.Body.String())
	}
	tmpl := create("/api/templates", `{"name":"Seed template","title":"Seed title"}`)
	sched := create("/api/schedules", `{"title":"nightly","repo":"acme/widgets","recurrence":{"kind":"everyNHours","everyNHours":24}}`)
	member := create("/api/templates", `{"name":"Suite member","title":"Member title"}`)
	suite := create("/api/suites", `{"name":"seed suite","mode":"count","count":1,"templateIds":["`+member+`"]}`)
	board := create("/api/boards", `{"name":"Seed Board"}`)
	if rec := do(t, srv, http.MethodPut, "/api/deployment/agent-backends/c", `{"framework":"claude"}`); rec.Code != http.StatusOK {
		t.Fatalf("seeding a connection: %d %s", rec.Code, rec.Body)
	}

	const remote = "https://github.com/acme/grain-config.git"
	fake.status = ui.StateRepoStatus{Remote: remote, Branch: "trunk"}
	client.Config.StateRepo = fake

	for _, tc := range []struct {
		method, path, body string
		wantPath           string
		// wantPatch is a string the patch must carry: what the request
		// asked for, or what it removes.
		wantPatch string
	}{
		{http.MethodPatch, "/api/schedules/" + sched, `{"title":"weekly please"}`, "settings/schedules.yml", "+    title: weekly please"},
		{http.MethodDelete, "/api/schedules/" + sched, ``, "settings/schedules.yml", "-    title: nightly"},
		{http.MethodPost, "/api/schedules", `{"title":"second","repo":"acme/widgets","recurrence":{"kind":"everyNHours","everyNHours":12}}`, "settings/schedules.yml", "+    title: second"},
		{http.MethodPost, "/api/templates", `{"name":"Fresh","title":"Fresh title"}`, "settings/templates.yml", "Fresh title"},
		{http.MethodPatch, "/api/templates/" + tmpl, `{"title":"Renamed title"}`, "settings/templates.yml", "+    title: Renamed title"},
		{http.MethodDelete, "/api/templates/" + tmpl, ``, "settings/templates.yml", "-    title: Seed title"},
		{http.MethodPatch, "/api/suites/" + suite, `{"name":"renamed suite"}`, "settings/suites.yml", "renamed suite"},
		{http.MethodDelete, "/api/suites/" + suite, ``, "settings/suites.yml", "seed suite"},
		{http.MethodPost, "/api/boards", `{"name":"Another Board"}`, "tables/board.json", "Another Board"},
		{http.MethodPatch, "/api/boards/" + board, `{"name":"Renamed Board"}`, "tables/board.json", "Renamed Board"},
		{http.MethodDelete, "/api/boards/" + board, ``, "tables/board.json", "Seed Board"},
		{http.MethodPost, "/api/settings", `{"environmentName":"prod"}`, "tables/grain_config.json", "prod"},
		{http.MethodPut, "/api/deployment/agent-backends/c2", `{"framework":"codex"}`, "tables/grain_config.json", "codex"},
		{http.MethodDelete, "/api/deployment/agent-backends/c", ``, "tables/grain_config.json", "claude"},
		{http.MethodPut, "/api/deployment/repos/acme/widgets/config", `{"promptExtension":"mind the gap"}`, "tables/repo_config.json", "mind the gap"},
		// Not rehearsed: the route sets a secret before its write, so it
		// is refused at the guard with the file and no patch.
		{http.MethodPost, "/api/gcp-keys", `{"name":"prod","projectId":"p1","serviceAccountEmail":"sa@p1.iam.gserviceaccount.com"}`, "tables/gcp_key.json", ""},
		{http.MethodPost, "/api/deployment/git-connections", `{"host":"github.com","type":"pat","token":"ghp_test"}`, "tables/grain_config.json", ""},
	} {
		rec := do(t, srv, tc.method, tc.path, tc.body)
		if rec.Code != http.StatusConflict {
			t.Errorf("%s %s: status %d (want 409): %s", tc.method, tc.path, rec.Code, rec.Body)
			continue
		}
		var got configRefusalBody
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("%s %s: %v", tc.method, tc.path, err)
		}
		if got.Code != ui.ConfigManagedExternallyCode || got.Repo != "acme/grain-config" || got.Branch != "trunk" ||
			got.Docs == "" || got.URL != "https://github.com/acme/grain-config" {
			t.Errorf("%s %s: refusal = %+v", tc.method, tc.path, got)
		}
		if !slices.Contains(got.Path, tc.wantPath) {
			t.Errorf("%s %s: path = %v, want [%s]", tc.method, tc.path, got.Path, tc.wantPath)
		}
		if tc.wantPatch == "" {
			if len(got.Change) != 0 {
				t.Errorf("%s %s: an unrehearsed route carried a change: %+v", tc.method, tc.path, got.Change)
			}
			if !strings.Contains(got.Error, tc.wantPath) {
				t.Errorf("%s %s: the instruction does not name %s: %s", tc.method, tc.path, tc.wantPath, got.Error)
			}
			continue
		}
		i := slices.IndexFunc(got.Change, func(c fileChange) bool { return c.Path == tc.wantPath })
		if i < 0 || !strings.Contains(got.Change[i].Patch, tc.wantPatch) {
			t.Errorf("%s %s: change = %+v, want a patch of %s carrying %q", tc.method, tc.path, got.Change, tc.wantPath, tc.wantPatch)
			continue
		}
		// What an agent reads off the tool result: the repository, the
		// file and the edit, in one instruction.
		for _, want := range []string{"acme/grain-config@trunk", "Commit this change to ", tc.wantPath, got.Change[i].Patch, "next sync"} {
			if !strings.Contains(got.Error, want) {
				t.Errorf("%s %s: the instruction lacks %q:\n%s", tc.method, tc.path, want, got.Error)
			}
		}
	}

	// Nothing was written: the rehearsals were rolled back.
	fake.status = ui.StateRepoStatus{Branch: "main"}
	rec := do(t, srv, http.MethodGet, "/api/schedules", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"nightly"`) {
		t.Fatalf("a refused write changed the schedule: %d %s", rec.Code, rec.Body)
	}
	rec = do(t, srv, http.MethodGet, "/api/templates", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Seed title") {
		t.Fatalf("a refused write changed the template: %d %s", rec.Code, rec.Body)
	}

	// In internal mode with settingsLocked enabled, the same write is
	// refused too, with the edit to make in grain/config.
	fake.status = ui.StateRepoStatus{Branch: "main"}
	if rec := do(t, srv, http.MethodPut, "/api/settings", `{"settingsLocked":true}`); rec.Code >= 300 {
		t.Fatalf("locking settings: status %d: %s", rec.Code, rec.Body)
	}
	rec = do(t, srv, http.MethodPatch, "/api/schedules/"+sched, `{"title":"weekly please"}`)
	var internal configRefusalBody
	if rec.Code != http.StatusConflict || json.Unmarshal(rec.Body.Bytes(), &internal) != nil ||
		internal.Repo != "grain/config" || len(internal.Change) == 0 || !strings.Contains(internal.Change[0].Patch, "+    title: weekly please") {
		t.Fatalf("internal mode (locked): status %d: %s", rec.Code, rec.Body)
	}
	// When unlocked (default), or with no config repository at all, it simply happens.
	if rec := do(t, srv, http.MethodPut, "/api/settings", `{"settingsLocked":false}`); rec.Code >= 300 {
		t.Fatalf("unlocking settings: status %d: %s", rec.Code, rec.Body)
	}
	rec = do(t, srv, http.MethodPatch, "/api/schedules/"+sched, `{"title":"weekly please"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "weekly please") {
		t.Fatalf("internal mode (unlocked): status %d: %s", rec.Code, rec.Body)
	}
}

// A request that would have failed anyway -- an unknown id, a body that
// fails validation -- is still refused as a configuration write: external
// mode answers every one of them 409, with the file that holds the
// setting and no patch, since there is no write to rehearse.
func TestExternalModeRefusesARequestThatWouldFailAnyway(t *testing.T) {
	t.Parallel()
	fake := &fakeStateRepo{status: ui.StateRepoStatus{Remote: "https://github.com/acme/grain-config.git"}}
	srv := stateRepoServer(t, fake)
	for _, tc := range []struct{ method, path, body, file string }{
		{http.MethodPatch, "/api/schedules/no-such", `{"title":"x"}`, "schedule"},
		{http.MethodPost, "/api/schedules", `{}`, "schedule"},
	} {
		rec := do(t, srv, tc.method, tc.path, tc.body)
		if rec.Code != http.StatusConflict {
			t.Fatalf("%s %s: status %d: %s", tc.method, tc.path, rec.Code, rec.Body)
		}
		var got configRefusalBody
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Code != ui.ConfigManagedExternallyCode || len(got.Change) != 0 || !slices.ContainsFunc(got.Path, func(p string) bool { return strings.Contains(p, tc.file) }) {
			t.Fatalf("%s %s: refusal %+v", tc.method, tc.path, got)
		}
	}
}

// The edit a refusal hands back is the write: applied to the exported
// configuration, it is the export the write itself would have left --
// which is what the sync then imports.
func TestTheRefusalsEditRoundTripsToTheWritesOwnResult(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fake := &fakeStateRepo{status: ui.StateRepoStatus{Branch: "main"}}
	_, client := testServer(t)
	storeSettingsRow(t, client)
	srv := ui.NewServerWithClient(client)

	rec := do(t, srv, http.MethodPost, "/api/schedules", `{"title":"nightly","repo":"acme/widgets","recurrence":{"kind":"everyNHours","everyNHours":24}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("seeding: %d %s", rec.Code, rec.Body)
	}
	sched := seedID(t, rec, rec.Body.String())
	client.Config.StateRepo = fake

	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPatch, "/api/schedules/" + sched, `{"title":"weekly","enabled":false}`},
		{http.MethodPost, "/api/boards", `{"name":"Round trip board"}`},
		{http.MethodPost, "/api/settings", `{"environmentName":"staging"}`},
	} {
		exported := t.TempDir()
		if err := staterepo.ExportConfig(ctx, client.Store.DB(), exported); err != nil {
			t.Fatal(err)
		}

		fake.status = ui.StateRepoStatus{Remote: "https://github.com/acme/grain-config.git", Branch: "main"}
		client.Config.StateRepo = fake
		rec := do(t, srv, tc.method, tc.path, tc.body)
		fake.status = ui.StateRepoStatus{Branch: "main"}
		client.Config.StateRepo = nil
		if rec.Code != http.StatusConflict {
			t.Fatalf("%s %s: status %d: %s", tc.method, tc.path, rec.Code, rec.Body)
		}
		var got struct {
			Change []staterepo.FileChange `json:"change"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got.Change) == 0 {
			t.Fatalf("%s %s: no change in %s (%v)", tc.method, tc.path, rec.Body, err)
		}
		if err := staterepo.ApplyChanges(exported, got.Change); err != nil {
			t.Fatalf("%s %s: %v", tc.method, tc.path, err)
		}

		if rec := do(t, srv, tc.method, tc.path, tc.body); rec.Code >= 300 {
			t.Fatalf("%s %s in internal mode: %d %s", tc.method, tc.path, rec.Code, rec.Body)
		}
		written := t.TempDir()
		if err := staterepo.ExportConfig(ctx, client.Store.DB(), written); err != nil {
			t.Fatal(err)
		}
		diff, err := staterepo.DiffTrees(exported, written)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range diff {
			if onlyIssuedAtDiffers(d.Patch) {
				// When an id was issued is when the write ran, and the
				// rehearsal ran before the write did.
				continue
			}
			t.Errorf("%s %s: the applied edit and the write differ in %s:\n%s", tc.method, tc.path, d.Path, d.Patch)
		}
	}
}

func onlyIssuedAtDiffers(patch string) bool {
	for _, line := range strings.Split(patch, "\n") {
		if (strings.HasPrefix(line, "-") || strings.HasPrefix(line, "+")) &&
			!strings.HasPrefix(line, "---") && !strings.HasPrefix(line, "+++") &&
			!strings.Contains(line, `"issued_at"`) {
			return false
		}
	}
	return true
}

// An agent is told whether it can open the pull request itself: its own
// task's writes: grant on the config repository says it can, and the
// lack of one is named as what it would need.
func TestTheRefusalSaysWhetherTheAgentCanOpenThePullRequest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, client := testServer(t)
	client.Config.StateRepo = &fakeStateRepo{status: ui.StateRepoStatus{Remote: "git@github.com:acme/grain-config.git", Branch: "main"}}
	granted := model.Task{ID: "task-granted", Title: "granted",
		Writes: []model.WriteTarget{{Repo: model.RepoRef{Owner: "acme", Name: "grain-config"}, Base: "main"}}}
	plain := model.Task{ID: "task-plain", Title: "plain"}
	for _, task := range []model.Task{granted, plain} {
		if err := client.Store.PutTask(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	as := func(id string) context.Context {
		return ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalAgent, ID: id})
	}

	err := ui.RefuseConfigWriteForTest(client, as(granted.ID), "schedule")
	if err == nil || !strings.Contains(err.Error(), "This task can write to acme/grain-config") ||
		!strings.Contains(err.Error(), "settings/schedules.yml") {
		t.Fatalf("granted agent: %v", err)
	}
	err = ui.RefuseConfigWriteForTest(client, as(plain.ID), "schedule")
	if err == nil || !strings.Contains(err.Error(), "it would need a writes: grant on acme/grain-config (base main)") {
		t.Fatalf("ungranted agent: %v", err)
	}
	// A person is told what to change and nothing about grants.
	err = ui.RefuseConfigWriteForTest(client, ctx, "schedule")
	if err == nil || strings.Contains(err.Error(), "This task") {
		t.Fatalf("person: %v", err)
	}

	// And in internal mode the guard lets the write through.
	client.Config.StateRepo = &fakeStateRepo{status: ui.StateRepoStatus{Branch: "main"}}
	if err := ui.RefuseConfigWriteForTest(client, as(plain.ID), "schedule"); err != nil {
		t.Fatalf("internal mode refused: %v", err)
	}
}

// A capability server saved with a secret in external mode is refused
// before the secret is set: the rehearsal stops a route at its first
// store write, and this route sets its secrets before that, so it is
// refused at the guard and the secrets store is left untouched.
func TestExternalModeRefusesACapabilityServerSecretBeforeStoringIt(t *testing.T) {
	ts, _, client, store := configTestServer(t, &fakeProber{})
	client.Config.StateRepo = &fakeStateRepo{status: ui.StateRepoStatus{Remote: "https://github.com/acme/grain-config.git", Branch: "main"}}
	const secretValue = "tok-must-not-be-stored"
	body := map[string]any{
		"id":            configCap,
		"config":        map[string]any{"command": "grain", "args": []string{"capability", "cloud"}, "config": map[string]any{"project": "p1"}},
		"configSecrets": map[string]string{"token": secretValue},
	}
	var out configRefusalBody
	if code := postJSON(t, ts, "PUT", "/api/capability-servers", body, &out); code != http.StatusConflict || out.Code != ui.ConfigManagedExternallyCode {
		t.Fatalf("status %d %+v, want the 409 refusal", code, out)
	}
	names, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 0 {
		t.Errorf("secrets stored despite the refusal: %v", names)
	}
}
