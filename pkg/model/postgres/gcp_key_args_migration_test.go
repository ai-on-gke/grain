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

package postgres_test

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"testing"

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
	rewindSchemaTo(t, ctx, db, 108)
	if err := model.New(db).Init(ctx); err != nil {
		t.Fatalf("migrating a version-108 store: %v", err)
	}
}

func TestMigration109PostgresRewritesAndNotesMissingSets(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)

	cfg := model.DefaultConfig()
	cfg.MCPServers = gcpServerWithSets("sandbox-agent", "iap-test")
	cfg.DefaultCapabilities = []string{
		`https://grain.dev/capabilities/gcp#gcp-key={"account":"default","activate":true}`,
	}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"t-ok", "t-missing"} {
		if err := store.PutTask(ctx, model.Task{
			ID: id, Title: id,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO `task_grant` (`task_id`, `capability`, `via`) VALUES ('t-ok', ?, 'label')",
		`https://grain.dev/capabilities/gcp#gcp-key={"account":"iap-test","activate":true}`); err != nil {
		t.Fatal(err)
	}
	missingGrant := `https://grain.dev/capabilities/gcp#gcp-key={"account":"ghost-set"}`
	if _, err := db.ExecContext(ctx, "INSERT INTO `task_grant` (`task_id`, `capability`, `via`) VALUES ('t-missing', ?, 'label')", missingGrant); err != nil {
		t.Fatal(err)
	}

	migrateFrom108(t, ctx, db)

	wantIAP := `https://grain.dev/capabilities/gcp#gcp-key={"permission_set":"iap-test","primary":true}`
	wantDefault := `https://grain.dev/capabilities/gcp#gcp-key={"permission_set":"sandbox-agent","primary":true}`
	if got := grantRows(t, ctx, db, "task_grant", "task_id")["t-ok"]; !slices.Equal(got, []string{wantIAP}) {
		t.Errorf("t-ok grants = %v, want [%s]", got, wantIAP)
	}
	wantGhost := `https://grain.dev/capabilities/gcp#gcp-key={"permission_set":"ghost-set","primary":true}`
	if got := grantRows(t, ctx, db, "task_grant", "task_id")["t-missing"]; !slices.Equal(got, []string{wantGhost}) {
		t.Errorf("t-missing grants = %v, want [%s]", got, wantGhost)
	}
	gotCfg, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(gotCfg.DefaultCapabilities, []string{wantDefault}) {
		t.Errorf("default_capabilities = %v, want [%s]", gotCfg.DefaultCapabilities, wantDefault)
	}

	var noteBody string
	if err := db.QueryRowContext(ctx, "SELECT `body` FROM `task_comment` WHERE `task_id` = 't-missing'").Scan(&noteBody); err != nil {
		t.Fatalf("expected migration note on t-missing: %v", err)
	}
	if want := model.GCPKeyMissingSetMigrationNote(missingGrant, wantGhost, "ghost-set"); noteBody != want {
		t.Errorf("note = %q, want %q", noteBody, want)
	}
}

// Migration 127 rewrites, on PostgreSQL too, the grants migration 109 kept
// as written because their "account" named an unconfigured permission set
// (docs/test-runs/2026-11-07-capabilities-fencing-admission.md, F1).
func TestMigration127PostgresRewritesGCPKeyGrantsMigration109Kept(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)

	kept := `https://grain.dev/capabilities/gcp#gcp-key={"account":"ghost-set","activate":false}`
	want := `https://grain.dev/capabilities/gcp#gcp-key={"permission_set":"ghost-set","primary":true}`

	cfg := model.DefaultConfig()
	cfg.MCPServers = gcpServerWithSets("sandbox-agent")
	cfg.DefaultCapabilities = []string{kept}
	cfg.CapabilityPolicy = model.CapabilityPolicy{
		Catalogue: &model.CapabilityCatalogue{
			Defaults: model.CatalogueDefaults{
				Task: []string{kept},
				Repos: map[string]model.CatalogueRepoDefaults{
					"acme/widgets": {CI: []string{kept}},
				},
			},
		},
	}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: widgets, CICapabilities: []string{kept}}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTask(ctx, model.Task{ID: "t-kept", Title: "Kept"}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTemplate(ctx, model.Template{ID: "tpl-kept", Name: "Kept tpl", Title: "Kept", Body: "Template body."}); err != nil {
		t.Fatal(err)
	}
	for _, ins := range []struct{ table, col, id string }{
		{"task_grant", "task_id", "t-kept"},
		{"template_grant", "template_id", "tpl-kept"},
	} {
		if _, err := db.ExecContext(ctx, "INSERT INTO `"+ins.table+"` (`"+ins.col+"`, `capability`, `via`) VALUES (?, ?, 'label')", ins.id, kept); err != nil {
			t.Fatal(err)
		}
	}

	rewindSchemaTo(t, ctx, db, 126)
	if err := model.New(db).Init(ctx); err != nil {
		t.Fatalf("migrating a version-126 store: %v", err)
	}

	if got := grantRows(t, ctx, db, "task_grant", "task_id")["t-kept"]; !slices.Equal(got, []string{want}) {
		t.Errorf("task_grant = %v, want [%s]", got, want)
	}
	if got := grantRows(t, ctx, db, "template_grant", "template_id")["tpl-kept"]; !slices.Equal(got, []string{want}) {
		t.Errorf("template_grant = %v, want [%s]", got, want)
	}
	gotCfg, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(gotCfg.DefaultCapabilities, []string{want}) {
		t.Errorf("default_capabilities = %v, want [%s]", gotCfg.DefaultCapabilities, want)
	}
	if cat := gotCfg.CapabilityPolicy.Catalogue; cat == nil ||
		!slices.Equal(cat.Defaults.Task, []string{want}) ||
		!slices.Equal(cat.Defaults.Repos["acme/widgets"].CI, []string{want}) {
		t.Errorf("catalogue defaults = %+v, want task and acme/widgets CI [%s]", cat, want)
	}
	gotRepo, err := store.GetRepoConfig(ctx, widgets)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(gotRepo.CICapabilities, []string{want}) {
		t.Errorf("repo ci_capabilities = %v, want [%s]", gotRepo.CICapabilities, want)
	}
	wantNote := model.GCPKeyMissingSetMigrationNote(kept, want, "ghost-set")
	var noteBody string
	if err := db.QueryRowContext(ctx, "SELECT `body` FROM `task_comment` WHERE `task_id` = 't-kept'").Scan(&noteBody); err != nil || noteBody != wantNote {
		t.Errorf("task note = %q (err=%v), want %q", noteBody, err, wantNote)
	}
	if tpl, err := store.GetTemplate(ctx, "tpl-kept"); err != nil || tpl == nil || !strings.HasSuffix(tpl.Body, wantNote) {
		t.Errorf("template body = %+v (err=%v), want migration note %q", tpl, err, wantNote)
	}
}
