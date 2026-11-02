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
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// Migration 84 drops executor_token and executor_token_sequence, rows
// and all, from a store at schema 83 that still has them.
func TestMigration84DropsExecutorTokens(t *testing.T) {
	t.Parallel()
	_, db, ctx := openStore(t)

	for _, stmt := range []string{
		"CREATE TABLE `executor_token` (`id` TEXT NOT NULL, `name` TEXT NOT NULL, `token_hash` TEXT NOT NULL, " +
			"`prefix` TEXT NOT NULL, `created_at` DATETIME NOT NULL, `revoked_at` DATETIME NULL, " +
			"`last_connected_at` DATETIME NULL, PRIMARY KEY (`id`), UNIQUE (`token_hash`))",
		"CREATE TABLE `executor_token_sequence` (`id` INTEGER PRIMARY KEY AUTOINCREMENT, `issued_at` DATETIME NOT NULL)",
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("recreating a schema-83 table: %v", err)
		}
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO `executor_token` (`id`, `name`, `token_hash`, `prefix`, `created_at`) VALUES (?, ?, ?, ?, ?)",
		"exec-tok-1", "workstation", "hash", "gxt_abcd", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO `executor_token_sequence` (`issued_at`) VALUES (?)", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	rewindSchemaTo(t, ctx, db, 83)
	if err := model.New(db).Init(ctx); err != nil {
		t.Fatalf("migrating a version-83 store: %v", err)
	}

	for _, table := range []string{"executor_token", "executor_token_sequence"} {
		var n int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM `"+table+"`").Scan(&n); err == nil {
			t.Errorf("%s survived migration 84 (%d rows)", table, n)
		}
	}
	var version int
	if err := db.QueryRowContext(ctx, "SELECT `version` FROM `grain_schema` WHERE `id` = 1").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != model.SchemaVersion {
		t.Errorf("schema version = %d after migrating, want %d", version, model.SchemaVersion)
	}
}
