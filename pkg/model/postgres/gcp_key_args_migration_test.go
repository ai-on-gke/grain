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

func migrateFrom106(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	rewindSchemaTo(t, ctx, db, 106)
	if err := model.New(db).Init(ctx); err != nil {
		t.Fatalf("migrating a version-106 store: %v", err)
	}
}

func TestMigration107PostgresRewritesAndNotesMissingSets(t *testing.T) {
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

	migrateFrom106(t, ctx, db)

	wantIAP := `https://grain.dev/capabilities/gcp#gcp-key={"permission_set":"iap-test","primary":true}`
	wantDefault := `https://grain.dev/capabilities/gcp#gcp-key={"permission_set":"sandbox-agent","primary":true}`
	if got := grantRows(t, ctx, db, "task_grant", "task_id")["t-ok"]; !slices.Equal(got, []string{wantIAP}) {
		t.Errorf("t-ok grants = %v, want [%s]", got, wantIAP)
	}
	if got := grantRows(t, ctx, db, "task_grant", "task_id")["t-missing"]; !slices.Equal(got, []string{missingGrant}) {
		t.Errorf("t-missing grants = %v, want [%s] kept", got, missingGrant)
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
	if !strings.Contains(noteBody, "ghost-set") {
		t.Errorf("note = %q, want mention of ghost-set", noteBody)
	}
}
