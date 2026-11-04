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
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// Migration 108 drops the task, template and repository hostd selectors
// -- task_hostd, template.hostd and repo_config.default_hostd, values
// and all -- from a store at schema 107, and keeps task_run_hostd, the
// record of where each run went, untouched.
func TestMigration108DropsTaskHostdSelectorsAndKeepsRunHistory(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)
	seedFinishedRun(t, store, ctx, "r1", "a1b2")
	at := now.Add(time.Minute)
	if err := store.RecordTaskRunHostd(ctx, model.TaskRunHostd{
		RunID: "r1", TaskID: "a1b2", Hostd: "laptop", Instance: "laptop.example.com", Owner: "alice@example.com", At: at,
	}); err != nil {
		t.Fatal(err)
	}

	for _, stmt := range []string{
		"CREATE TABLE `task_hostd` (`task_id` TEXT NOT NULL, `selector` TEXT NOT NULL DEFAULT '', " +
			"`updated_at` DATETIME NOT NULL, PRIMARY KEY (`task_id`))",
		"ALTER TABLE `template` ADD COLUMN `hostd` TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE `repo_config` ADD COLUMN `default_hostd` TEXT NOT NULL DEFAULT ''",
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("recreating schema 107's hostd selectors: %v", err)
		}
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO `task_hostd` (`task_id`, `selector`, `updated_at`) VALUES (?, ?, ?)",
		"a1b2", "gpu=a100", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE `grain_schema` SET `version` = 107 WHERE `id` = 1"); err != nil {
		t.Fatal(err)
	}
	if err := model.New(db).Init(ctx); err != nil {
		t.Fatalf("migrating a version-107 store: %v", err)
	}

	var n int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM `task_hostd`").Scan(&n); err == nil {
		t.Errorf("task_hostd survived migration 108 (%d rows)", n)
	}
	for table, column := range map[string]string{"template": "hostd", "repo_config": "default_hostd"} {
		if _, err := db.ExecContext(ctx, "SELECT `"+column+"` FROM `"+table+"`"); err == nil {
			t.Errorf("%s.%s survived migration 108", table, column)
		}
	}
	got, err := store.RunHostd(ctx, "r1")
	if err != nil || got == nil || got.Hostd != "laptop" || got.Instance != "laptop.example.com" || got.Owner != "alice@example.com" {
		t.Errorf("run r1's hostd after migration 108 = %+v, %v; want it kept", got, err)
	}
	var version int
	if err := db.QueryRowContext(ctx, "SELECT `version` FROM `grain_schema` WHERE `id` = 1").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != model.SchemaVersion {
		t.Errorf("schema version = %d after migrating, want %d", version, model.SchemaVersion)
	}
}
