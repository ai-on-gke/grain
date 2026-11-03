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
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// customConnectionsBefore103 is grain_config.agent_backends as a release
// before 103 stored it: an enabled custom command connection beside a
// claude one.
const customConnectionsBefore103 = `[{"id":"house","name":"House agent","framework":"custom","models":["house-model"],"command":"agents/run.sh"},` +
	`{"id":"claude","framework":"claude"}]`

// storedConnections reads grain_config.agent_backends as it is stored, not
// through DecodeAgentBackends, which disables a custom connection on read
// whatever the stored row says.
func storedConnections(t *testing.T, ctx context.Context, db *sql.DB) []model.AgentBackend {
	t.Helper()
	var raw string
	if err := db.QueryRowContext(ctx, "SELECT `agent_backends` FROM `grain_config` WHERE `id` = 1").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var out []model.AgentBackend
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("stored agent_backends %q: %v", raw, err)
	}
	return out
}

func checkCustomConnectionDisabled(t *testing.T, got []model.AgentBackend) {
	t.Helper()
	if len(got) != 2 {
		t.Fatalf("connections = %+v, want both kept", got)
	}
	house, claude := got[0], got[1]
	if house.ID != "house" || house.Framework != model.RemovedAgentFrameworkCustom || !house.Disabled ||
		house.DisabledReason != model.RemovedCustomConnectionReason || house.Command != "agents/run.sh" || house.Name != "House agent" {
		t.Errorf("custom connection = %+v, want it kept as it was, disabled with %q", house, model.RemovedCustomConnectionReason)
	}
	if claude.ID != "claude" || claude.Disabled || claude.DisabledReason != "" {
		t.Errorf("claude connection = %+v, want it untouched", claude)
	}
}

func TestMigration103DisablesCustomCommandConnections(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)
	if err := store.PutConfig(ctx, testConfig()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE `grain_config` SET `agent_backends` = ? WHERE `id` = 1", customConnectionsBefore103); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE `grain_schema` SET `version` = 102 WHERE `id` = 1"); err != nil {
		t.Fatal(err)
	}
	if err := model.New(db).Init(ctx); err != nil {
		t.Fatalf("migrating a version-102 store with a custom connection: %v", err)
	}
	checkCustomConnectionDisabled(t, storedConnections(t, ctx, db))
	cfg, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if enabled := model.DeploymentAgentBackends(cfg.AgentBackends, false); len(enabled) != 1 || enabled[0].ID != "claude" {
		t.Errorf("enabled connections = %+v, want only claude", enabled)
	}

	// Idempotent: a second run leaves the row as the first wrote it.
	var before, after string
	if err := db.QueryRowContext(ctx, "SELECT `agent_backends` FROM `grain_config` WHERE `id` = 1").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE `grain_schema` SET `version` = 102 WHERE `id` = 1"); err != nil {
		t.Fatal(err)
	}
	if err := model.New(db).Init(ctx); err != nil {
		t.Fatalf("migrating again: %v", err)
	}
	if err := db.QueryRowContext(ctx, "SELECT `agent_backends` FROM `grain_config` WHERE `id` = 1").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("a second run changed agent_backends:\n%s\n->\n%s", before, after)
	}
}

// A grain/config dump written before 103, holding a custom command
// connection, still imports: the connection arrives disabled and kept.
func TestStateRepoImportDisablesCustomCommandConnections(t *testing.T) {
	t.Parallel()
	src, srcDB, ctx := openStore(t)
	if err := src.PutConfig(ctx, testConfig()); err != nil {
		t.Fatal(err)
	}
	if _, err := srcDB.ExecContext(ctx, "UPDATE `grain_config` SET `agent_backends` = ? WHERE `id` = 1", customConnectionsBefore103); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := staterepo.Export(ctx, srcDB, dir); err != nil {
		t.Fatal(err)
	}
	if err := staterepo.WriteSchemaVersion(dir, 102); err != nil {
		t.Fatal(err)
	}

	_, dstDB, _ := openStore(t)
	if err := staterepo.Import(ctx, dstDB, dir); err != nil {
		t.Fatalf("importing a schema-102 export with a custom connection: %v", err)
	}
	checkCustomConnectionDisabled(t, storedConnections(t, ctx, dstDB))
}
