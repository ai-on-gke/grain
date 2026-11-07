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

package sqlite_test

import (
	"bytes"
	"context"
	"database/sql"
	"log"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/model"
)

func gcpServerWithSets(names ...string) map[string]protocol.ServerConfig {
	var sets []any
	for _, n := range names {
		sets = append(sets, map[string]any{
			"name":    n,
			"project": "acme-ai",
			"roles":   []any{"roles/viewer"},
		})
	}
	return map[string]protocol.ServerConfig{
		model.GCPCapabilityServerID: {
			Command: "grain",
			Args:    []string{"capability", "gcp"},
			Builtin: "gcp",
			Config: map[string]any{
				"admin_credential_source": "ambient",
				"permission_sets":         sets,
			},
		},
	}
}

func migrateFrom108(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(ctx, "UPDATE `grain_schema` SET `version` = 108 WHERE `id` = 1"); err != nil {
		t.Fatal(err)
	}
	if err := model.New(db).Init(ctx); err != nil {
		t.Fatalf("migrating a version-108 store: %v", err)
	}
}

func taskCommentBodies(t *testing.T, ctx context.Context, db *sql.DB, taskID string) []string {
	t.Helper()
	rows, err := db.QueryContext(ctx, "SELECT `author_id`, `body` FROM `task_comment` WHERE `task_id` = ? ORDER BY `id`", taskID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var author, body string
		if err := rows.Scan(&author, &body); err != nil {
			t.Fatal(err)
		}
		out = append(out, author+": "+body)
	}
	return out
}

func TestMigration109RewritesGCPKeyAccountAndActivateEverywhere(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)
	now := time.Date(2026, 11, 4, 3, 0, 0, 0, time.UTC)

	cfg := testConfig()
	cfg.MCPServers = gcpServerWithSets("sandbox-agent", "iap-test", "ci-runner")
	cfg.DefaultCapabilities = []string{
		`https://grain.dev/capabilities/gcp#gcp-key={"account":"default","activate":true}`,
		"console",
	}
	cfg.ConsoleSessions = []model.ConsoleSession{{
		ID:    "ops",
		Title: "Ops",
		Capabilities: []string{
			`https://grain.dev/capabilities/gcp#gcp-key={"account":"iap-test","permission_set":"sandbox-agent","primary":false,"activate":false}`,
		},
	}}
	cfg.CapabilityPolicy = model.CapabilityPolicy{
		Catalogue: &model.CapabilityCatalogue{
			Defaults: model.CatalogueDefaults{
				Task: []string{`https://grain.dev/capabilities/gcp#gcp-key={"account":"iap-test","activate":true}`},
				CI:   []string{`https://grain.dev/capabilities/gcp#gcp-key={"activate":true}`},
				Repos: map[string]model.CatalogueRepoDefaults{
					"acme/widgets": {
						Task: []string{`https://grain.dev/capabilities/gcp#gcp-key={"account":"default"}`},
						CI:   []string{`https://grain.dev/capabilities/gcp#gcp-key={"account":"ci-runner","activate":true}`},
					},
				},
			},
			Servers: []model.CatalogueServer{{
				ID: model.GCPCapabilityServerID,
				Methods: []model.CatalogueMethod{{
					Name: "gcp-key",
					Args: map[string]model.CatalogueArg{
						"account":  {Allowed: []string{"default", "iap-test"}},
						"activate": {Allowed: []string{"true"}},
					},
					Restrictions: &model.CapabilityRestrictions{
						RequiresPersonWhen: map[string][]string{
							"account":  {"iap-test"},
							"activate": {"true"},
						},
					},
				}},
			}},
		},
	}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:                widgets,
		DefaultCapabilities: []string{`https://grain.dev/capabilities/gcp#gcp-key={"account":"iap-test"}`},
		CICapabilities:      []string{"gcp-key", "package-publish", `https://grain.dev/capabilities/gcp#gcp-key={"account":"default","activate":true}`},
	}); err != nil {
		t.Fatal(err)
	}

	if err := store.PutTask(ctx, model.Task{
		ID:    "t1",
		Title: "Deploy test",
	}); err != nil {
		t.Fatal(err)
	}
	// Two old-form grants that collapse to the same canonical grant are deduplicated.
	for _, g := range []string{
		`https://grain.dev/capabilities/gcp#gcp-key={"account":"iap-test","activate":true}`,
		`https://grain.dev/capabilities/gcp#gcp-key={"account":"iap-test","permission_set":"sandbox-agent","primary":true}`,
		`https://grain.dev/capabilities/gcp#gcp-key={"account":"default","primary":false}`,
	} {
		if _, err := db.ExecContext(ctx, "INSERT INTO `task_grant` (`task_id`, `capability`, `via`) VALUES ('t1', ?, 'label')", g); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO `task_grant_wait` (`task_id`, `capability`, `resource`, `waiting_at`) VALUES ('t1', ?, 'pool', ?)",
		`https://grain.dev/capabilities/gcp#gcp-key={"account":"ci-runner","activate":true}`, now); err != nil {
		t.Fatal(err)
	}

	if err := store.PutTemplate(ctx, model.Template{
		ID:    "tpl1",
		Name:  "Deploy template",
		Title: "Deploy",
		Body:  "Run deploy.",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO `template_grant` (`template_id`, `capability`, `via`) VALUES ('tpl1', ?, 'label')",
		`https://grain.dev/capabilities/gcp#gcp-key={"account":"default","activate":true}`); err != nil {
		t.Fatal(err)
	}

	if err := store.PutSchedule(ctx, model.Schedule{
		ID:     "sched1",
		Target: widgets,
		Title:  "Nightly",
		Body:   "Check infra.",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO `schedule_grant` (`schedule_id`, `capability`, `via`) VALUES ('sched1', ?, 'label')",
		`https://grain.dev/capabilities/gcp#gcp-key={"account":"iap-test","activate":false}`); err != nil {
		t.Fatal(err)
	}

	if err := store.PutPersistentSandbox(ctx, model.PersistentSandbox{
		Name:         "devbox",
		Repo:         "acme/widgets",
		Command:      "sleep 3600",
		Capabilities: []string{`https://grain.dev/capabilities/gcp#gcp-key={"account":"ci-runner","activate":true}`},
	}); err != nil {
		t.Fatal(err)
	}

	migrateFrom108(t, ctx, db)

	wantSandboxDefault := `https://grain.dev/capabilities/gcp#gcp-key={"permission_set":"sandbox-agent","primary":true}`
	wantSandboxSecondary := `https://grain.dev/capabilities/gcp#gcp-key={"permission_set":"sandbox-agent","primary":false}`
	wantIAP := `https://grain.dev/capabilities/gcp#gcp-key={"permission_set":"iap-test","primary":true}`
	wantIAPSecondary := `https://grain.dev/capabilities/gcp#gcp-key={"permission_set":"iap-test","primary":false}`
	wantCI := `https://grain.dev/capabilities/gcp#gcp-key={"permission_set":"ci-runner","primary":true}`
	wantEmpty := `https://grain.dev/capabilities/gcp#gcp-key={}`

	if got, want := grantRows(t, ctx, db, "task_grant", "task_id")["t1"], []string{wantIAP, wantSandboxSecondary}; !slices.Equal(got, want) {
		t.Errorf("task_grant = %v, want %v", got, want)
	}
	if got, want := grantRows(t, ctx, db, "task_grant_wait", "task_id")["t1"], []string{wantCI}; !slices.Equal(got, want) {
		t.Errorf("task_grant_wait = %v, want %v", got, want)
	}
	if got, want := grantRows(t, ctx, db, "template_grant", "template_id")["tpl1"], []string{wantSandboxDefault}; !slices.Equal(got, want) {
		t.Errorf("template_grant = %v, want %v", got, want)
	}
	if got, want := grantRows(t, ctx, db, "schedule_grant", "schedule_id")["sched1"], []string{wantIAP}; !slices.Equal(got, want) {
		t.Errorf("schedule_grant = %v, want %v", got, want)
	}
	if comments := taskCommentBodies(t, ctx, db, "t1"); len(comments) != 0 {
		t.Errorf("resolved sets must not leave migration notes on task, got %v", comments)
	}

	gotCfg, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{wantSandboxDefault, "console"}; !slices.Equal(gotCfg.DefaultCapabilities, want) {
		t.Errorf("config default_capabilities = %v, want %v", gotCfg.DefaultCapabilities, want)
	}
	if got := gotCfg.ConsoleSessions[0].Capabilities; !slices.Equal(got, []string{wantIAPSecondary}) {
		t.Errorf("console session capabilities = %v, want [%s]", got, wantIAPSecondary)
	}
	cat := gotCfg.CapabilityPolicy.Catalogue
	if !slices.Equal(cat.Defaults.Task, []string{wantIAP}) {
		t.Errorf("catalogue task defaults = %v, want [%s]", cat.Defaults.Task, wantIAP)
	}
	if !slices.Equal(cat.Defaults.CI, []string{wantEmpty}) {
		t.Errorf("catalogue ci defaults = %v, want [%s]", cat.Defaults.CI, wantEmpty)
	}
	if rd := cat.Defaults.Repos["acme/widgets"]; !slices.Equal(rd.Task, []string{wantSandboxDefault}) || !slices.Equal(rd.CI, []string{wantCI}) {
		t.Errorf("catalogue repo defaults = %+v", rd)
	}
	m := cat.Servers[0].Methods[0]
	if _, has := m.Args["account"]; has {
		t.Errorf("catalogue method args still has account: %+v", m.Args)
	}
	if _, has := m.Args["activate"]; has {
		t.Errorf("catalogue method args still has activate: %+v", m.Args)
	}
	if got, want := m.Args["permission_set"].Allowed, []string{"sandbox-agent", "iap-test"}; !slices.Equal(got, want) {
		t.Errorf("catalogue permission_set.allowed = %v, want %v", got, want)
	}
	if got, want := m.Restrictions.RequiresPersonWhen["permission_set"], []string{"iap-test"}; !slices.Equal(got, want) {
		t.Errorf("catalogue requires_person_when = %+v", m.Restrictions.RequiresPersonWhen)
	}
	if _, has := m.Restrictions.RequiresPersonWhen["activate"]; has {
		t.Errorf("catalogue requires_person_when still has activate: %+v", m.Restrictions.RequiresPersonWhen)
	}

	gotRepo, err := store.GetRepoConfig(ctx, widgets)
	if err != nil || gotRepo == nil {
		t.Fatalf("GetRepoConfig: %v, %+v", err, gotRepo)
	}
	if !slices.Equal(gotRepo.DefaultCapabilities, []string{wantIAP}) {
		t.Errorf("repo default_capabilities = %v, want [%s]", gotRepo.DefaultCapabilities, wantIAP)
	}
	if want := []string{"gcp-key", "package-publish", wantSandboxDefault}; !slices.Equal(gotRepo.CICapabilities, want) {
		t.Errorf("repo ci_capabilities = %v, want %v", gotRepo.CICapabilities, want)
	}

	gotSB, err := store.GetPersistentSandbox(ctx, "devbox")
	if err != nil || gotSB == nil {
		t.Fatalf("GetPersistentSandbox: %v, %+v", err, gotSB)
	}
	if !slices.Equal(gotSB.Capabilities, []string{wantCI}) {
		t.Errorf("persistent_sandbox capabilities = %v, want [%s]", gotSB.Capabilities, wantCI)
	}
}

func TestMigration109KeepsAndNotesMissingPermissionSets(t *testing.T) {
	// Not parallel: captures package-global log output.
	store, db, ctx := openStore(t)

	cfg := testConfig()
	cfg.MCPServers = gcpServerWithSets("sandbox-agent")
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutTask(ctx, model.Task{
		ID: "t-missing", Title: "Missing set task",
	}); err != nil {
		t.Fatal(err)
	}
	missingGrant := `https://grain.dev/capabilities/gcp#gcp-key={"account":"ghost-set","activate":true}`
	if _, err := db.ExecContext(ctx, "INSERT INTO `task_grant` (`task_id`, `capability`, `via`) VALUES ('t-missing', ?, 'label')", missingGrant); err != nil {
		t.Fatal(err)
	}

	if err := store.PutTemplate(ctx, model.Template{
		ID: "tpl-missing", Name: "Missing tpl", Title: "Missing", Body: "Template body.",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO `template_grant` (`template_id`, `capability`, `via`) VALUES ('tpl-missing', ?, 'label')", missingGrant); err != nil {
		t.Fatal(err)
	}

	if err := store.PutSchedule(ctx, model.Schedule{
		ID: "sched-missing", Target: widgets, Title: "Missing sched", Body: "Schedule body.",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO `schedule_grant` (`schedule_id`, `capability`, `via`) VALUES ('sched-missing', ?, 'label')", missingGrant); err != nil {
		t.Fatal(err)
	}

	var logBuf bytes.Buffer
	origOut := log.Writer()
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(origOut) })

	migrateFrom108(t, ctx, db)

	// Missing permission set keeps the grant intact everywhere.
	if got := grantRows(t, ctx, db, "task_grant", "task_id")["t-missing"]; !slices.Equal(got, []string{missingGrant}) {
		t.Errorf("task_grant = %v, want [%s] kept", got, missingGrant)
	}
	if got := grantRows(t, ctx, db, "template_grant", "template_id")["tpl-missing"]; !slices.Equal(got, []string{missingGrant}) {
		t.Errorf("template_grant = %v, want [%s] kept", got, missingGrant)
	}
	if got := grantRows(t, ctx, db, "schedule_grant", "schedule_id")["sched-missing"]; !slices.Equal(got, []string{missingGrant}) {
		t.Errorf("schedule_grant = %v, want [%s] kept", got, missingGrant)
	}

	// Notes are left on the task, template, schedule, and migration log.
	comments := taskCommentBodies(t, ctx, db, "t-missing")
	if len(comments) != 1 || !strings.Contains(comments[0], "ghost-set") || !strings.Contains(comments[0], "permission_set") {
		t.Errorf("task comments = %v, want 1 migration note naming ghost-set and permission_set", comments)
	}
	tpl, err := store.GetTemplate(ctx, "tpl-missing")
	if err != nil || tpl == nil || !strings.Contains(tpl.Body, "ghost-set") {
		t.Errorf("template body = %+v (err=%v), want migration note naming ghost-set", tpl, err)
	}
	sched, err := store.GetSchedule(ctx, "sched-missing")
	if err != nil || sched == nil || !strings.Contains(sched.Body, "ghost-set") {
		t.Errorf("schedule body = %+v (err=%v), want migration note naming ghost-set", sched, err)
	}
	logged := logBuf.String()
	for _, wantSub := range []string{"task t-missing", "template tpl-missing", "schedule sched-missing", "ghost-set"} {
		if !strings.Contains(logged, wantSub) {
			t.Errorf("migration log %q missing %q", logged, wantSub)
		}
	}

	// Re-running migration 109 is idempotent: notes are not duplicated.
	tplBodyBefore := tpl.Body
	schedBodyBefore := sched.Body
	migrateFrom108(t, ctx, db)
	if got := taskCommentBodies(t, ctx, db, "t-missing"); len(got) != 1 {
		t.Errorf("second migration duplicated task comments: %v", got)
	}
	tplAfter, _ := store.GetTemplate(ctx, "tpl-missing")
	if tplAfter.Body != tplBodyBefore {
		t.Errorf("second migration duplicated template note: %q", tplAfter.Body)
	}
	schedAfter, _ := store.GetSchedule(ctx, "sched-missing")
	if schedAfter.Body != schedBodyBefore {
		t.Errorf("second migration duplicated schedule note: %q", schedAfter.Body)
	}
}
