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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// Regression for 2026-11-07 walk O1 (2026-11-04 F2 class): a schema-108 store
// whose capability catalogue disables in_process.read-issues while read-issues
// still sits in deployment/repository defaults, template_grant and
// schedule_grant drops the disabled grants on upgrade (noting affected
// templates and schedules) while keeping task_grant, console_sessions and deny
// rules intact, and its exported state repository passes staterepo.Check.
func TestMigration121And128DropCatalogueDisabledGrantsFrom108Store(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	off := false
	on := true

	cfg := testConfig()
	cfg.DefaultCapabilities = []string{"read-issues", "release-gate-read"}
	cfg.ConsoleSessions = []model.ConsoleSession{{
		ID:           "retired-session",
		Title:        "Retired Session",
		Capabilities: []string{"read-issues", "release-gate-read"},
	}}
	cfg.CapabilityPolicy = model.CapabilityPolicy{
		Rules: []model.CapabilityPolicyRule{
			{Capability: "read-issues", Repos: []string{"acme/denied"}, Deny: true},
		},
		Catalogue: &model.CapabilityCatalogue{
			Defaults: model.CatalogueDefaults{
				Task: []string{"read-issues", "release-gate-read"},
				CI:   []string{"read-issues"},
				Repos: map[string]model.CatalogueRepoDefaults{
					"acme/widgets": {
						Task: []string{"release-gate-read", "read-issues"},
						CI:   []string{"read-issues"},
					},
				},
			},
			InProcess: map[string]model.CatalogueInProcess{
				"read-issues": {Enabled: &off},
				"self-debug":  {Enabled: &on},
			},
		},
	}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:                widgets,
		DefaultCapabilities: []string{"release-gate-read", "read-issues"},
		CICapabilities:      []string{"read-issues", "release-gate-read"},
	}); err != nil {
		t.Fatal(err)
	}

	if err := store.PutTask(ctx, model.Task{
		ID: "task-2", Title: "Seeded task", Target: &widgets,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO `task_grant` (`task_id`, `capability`, `via`) VALUES ('task-2', 'read-issues', 'label'), ('task-2', 'release-gate-read', 'label')"); err != nil {
		t.Fatal(err)
	}

	if err := store.PutTemplate(ctx, model.Template{
		ID: "template-1", Name: "walk-tmpl", Title: "Walk template", Body: "seed", Target: &widgets, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO `template_grant` (`template_id`, `capability`, `via`) VALUES ('template-1', 'read-issues', 'label'), ('template-1', 'release-gate-read', 'label')"); err != nil {
		t.Fatal(err)
	}

	if err := store.PutSchedule(ctx, model.Schedule{
		ID: "sched-1", Title: "Walk schedule", Body: "seed", Target: widgets,
		Recurrence: model.Recurrence{Kind: model.RecurrenceDaily},
		Overlap:    model.OverlapSkip, Enabled: true, NextRunAt: now, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO `schedule_grant` (`schedule_id`, `capability`, `via`) VALUES ('sched-1', 'read-issues', 'label'), ('sched-1', 'release-gate-read', 'label')"); err != nil {
		t.Fatal(err)
	}

	migrateFrom108(t, ctx, db)

	gotCfg, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(gotCfg.DefaultCapabilities, []string{"release-gate-read"}) {
		t.Errorf("grain_config.default_capabilities = %v, want [release-gate-read]", gotCfg.DefaultCapabilities)
	}
	cat := gotCfg.CapabilityPolicy.Catalogue
	if cat == nil {
		t.Fatal("catalogue nil after migration")
	}
	if !slices.Equal(cat.Defaults.Task, []string{"release-gate-read"}) {
		t.Errorf("catalogue.defaults.task = %v, want [release-gate-read]", cat.Defaults.Task)
	}
	if len(cat.Defaults.CI) != 0 {
		t.Errorf("catalogue.defaults.ci = %v, want empty", cat.Defaults.CI)
	}
	if got := cat.Defaults.Repos["acme/widgets"]; !slices.Equal(got.Task, []string{"release-gate-read"}) || len(got.CI) != 0 {
		t.Errorf("catalogue.defaults.repos[acme/widgets] = %+v, want task=[release-gate-read] ci=[]", got)
	}
	if len(gotCfg.CapabilityPolicy.Rules) != 1 || gotCfg.CapabilityPolicy.Rules[0].Capability != model.GitHubReadIssuesRef || !gotCfg.CapabilityPolicy.Rules[0].Deny {
		t.Errorf("capability_policy.rules = %+v, want deny on %s", gotCfg.CapabilityPolicy.Rules, model.GitHubReadIssuesRef)
	}
	if len(gotCfg.ConsoleSessions) != 1 || !slices.Equal(gotCfg.ConsoleSessions[0].Capabilities, []string{model.GitHubReadIssuesGrant, "release-gate-read"}) {
		t.Errorf("console_sessions = %+v, want migrated grant preserved", gotCfg.ConsoleSessions)
	}

	gotRepo, err := store.GetRepoConfig(ctx, widgets)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(gotRepo.DefaultCapabilities, []string{"release-gate-read"}) || !slices.Equal(gotRepo.CICapabilities, []string{"release-gate-read"}) {
		t.Errorf("repo_config = default:%v ci:%v, want [release-gate-read] on both", gotRepo.DefaultCapabilities, gotRepo.CICapabilities)
	}

	// Task grant is rewritten and kept (dispatch holds disabled task grants).
	if got := grantRows(t, ctx, db, "task_grant", "task_id")["task-2"]; !slices.Equal(got, []string{model.GitHubReadIssuesGrant, "release-gate-read"}) {
		t.Errorf("task-2 grants = %v, want [%s release-gate-read]", got, model.GitHubReadIssuesGrant)
	}

	// Template and schedule grants drop the disabled grant and record an idempotent note.
	if got := grantRows(t, ctx, db, "template_grant", "template_id")["template-1"]; !slices.Equal(got, []string{"release-gate-read"}) {
		t.Errorf("template-1 grants = %v, want [release-gate-read]", got)
	}
	if got := grantRows(t, ctx, db, "schedule_grant", "schedule_id")["sched-1"]; !slices.Equal(got, []string{"release-gate-read"}) {
		t.Errorf("sched-1 grants = %v, want [release-gate-read]", got)
	}

	wantReason := "capability " + model.GitHubReadIssuesGrant + " is disabled in settings/capabilities.yml (in_process.read-issues, the capability it replaced)"
	wantNote := model.CatalogueDisabledGrantMigrationNote(model.GitHubReadIssuesGrant, wantReason)

	tmpl, err := store.GetTemplate(ctx, "template-1")
	if err != nil || tmpl == nil {
		t.Fatalf("GetTemplate: %v", err)
	}
	if !strings.Contains(tmpl.Body, wantNote) {
		t.Errorf("template-1 body missing migration note:\ngot:  %q\nwant: %q", tmpl.Body, wantNote)
	}
	sched, err := store.GetSchedule(ctx, "sched-1")
	if err != nil || sched == nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if !strings.Contains(sched.Body, wantNote) {
		t.Errorf("sched-1 body missing migration note:\ngot:  %q\nwant: %q", sched.Body, wantNote)
	}

	// Re-inserting the disabled grant and re-running migration 128 must not duplicate the note.
	if _, err := db.ExecContext(ctx,
		"INSERT INTO `template_grant` (`template_id`, `capability`, `via`) VALUES ('template-1', ?, 'label')",
		model.GitHubReadIssuesGrant); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE `grain_schema` SET `version` = 127 WHERE `id` = 1"); err != nil {
		t.Fatal(err)
	}
	if err := model.New(db).Init(ctx); err != nil {
		t.Fatalf("re-running migration 128: %v", err)
	}
	tmplAgain, err := store.GetTemplate(ctx, "template-1")
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(tmplAgain.Body, wantNote); count != 1 {
		t.Errorf("template-1 body note count = %d, want 1:\n%s", count, tmplAgain.Body)
	}

	// Exported state repository passes staterepo.Check cleanly.
	exportDir := t.TempDir()
	if err := staterepo.Export(ctx, db, exportDir); err != nil {
		t.Fatalf("staterepo.Export: %v", err)
	}
	if err := staterepo.WriteSchemaVersion(exportDir, model.SchemaVersion); err != nil {
		t.Fatal(err)
	}
	_, checkDB, _ := openStore(t)
	if _, err := staterepo.Check(ctx, checkDB, exportDir, model.SchemaVersion); err != nil {
		t.Fatalf("staterepo.Check after upgrade failed: %v", err)
	}
}

func TestMigration128CleansAlreadyMigrated127StoreAcrossCatalogueSurfaces(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)
	now := time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)
	off := false

	extServer := "https://capabilities.example/ext"
	extGrant := extServer + "#run={}"
	signGrant := "https://grain.dev/capabilities/release#image-signing={}"

	cfg := testConfig()
	cfg.MCPServers = map[string]protocol.ServerConfig{
		extServer: {
			Command:  "grain",
			Args:     []string{"capability", "config-map"},
			Disabled: true,
		},
	}
	cfg.DefaultCapabilities = []string{model.GitHubReadIssuesGrant, extGrant, "api:ops/triage", "release-gate-read"}
	cfg.CapabilityPolicy = model.CapabilityPolicy{
		Catalogue: &model.CapabilityCatalogue{
			Defaults: model.CatalogueDefaults{
				Task: []string{model.GitHubReadIssuesGrant, "release-gate-read"},
				CI:   []string{signGrant},
			},
			InProcess: map[string]model.CatalogueInProcess{
				"read-issues":   {Enabled: &off},
				"image-signing": {Enabled: &off},
			},
			APIScopes: model.CatalogueAPIScopes{
				Presets: map[string]model.CataloguePreset{
					"ops/triage": {Enabled: &off},
				},
			},
		},
	}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := store.PutAPIScopeSet(ctx, model.APIScopeSet{
		Name: "ops/triage", Description: "Triage", Scopes: []string{"tasks:read"}, Version: 1,
	}); err != nil {
		t.Fatal(err)
	}
	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:                widgets,
		DefaultCapabilities: []string{"api:ops/triage", "release-gate-read"},
		CICapabilities:      []string{signGrant, model.GitHubReadIssuesGrant},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTemplate(ctx, model.Template{
		ID: "tpl-127", Name: "tpl-127", Title: "T", Body: "body", Target: &widgets, CreatedAt: now,
		Grants: []model.Grant{
			{Capability: extGrant, Via: "label"},
			{Capability: "api:ops/triage", Via: "label"},
			{Capability: "release-gate-read", Via: "label"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := db.ExecContext(ctx, "UPDATE `grain_schema` SET `version` = 127 WHERE `id` = 1"); err != nil {
		t.Fatal(err)
	}
	if err := model.New(db).Init(ctx); err != nil {
		t.Fatalf("migrating 127 -> %d: %v", model.SchemaVersion, err)
	}

	exportDir := t.TempDir()
	if err := staterepo.Export(ctx, db, exportDir); err != nil {
		t.Fatalf("staterepo.Export: %v", err)
	}
	if err := staterepo.WriteSchemaVersion(exportDir, model.SchemaVersion); err != nil {
		t.Fatal(err)
	}
	_, checkDB, _ := openStore(t)
	if _, err := staterepo.Check(ctx, checkDB, exportDir, model.SchemaVersion); err != nil {
		t.Fatalf("staterepo.Check after migration 128 failed: %v", err)
	}
}
