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

package model

import (
	"context"
	"database/sql"
	"fmt"
)

// taskOpenTable is a trigger-maintained local index of every task that has not
// closed (rows in `task` with no `task_observation` row carrying a non-NULL
// `closed_at`).
//
// Why a separate index table rather than a partial index on `task` or
// `task_observation`: `closed_at` lives on `task_observation` while a newly
// filed task has no `task_observation` row at all until grain observes it.
// Neither table alone can carry a partial index over open tasks without either
// duplicating state onto `task` (and into every state-repository dump) or
// writing synthetic empty observations. Keeping the open-task set in a local
// trigger-maintained index table lets `task_ready` (and open-task scans like
// `OpenPullRequestLinks` and `ListOpenTasks`) scale with the open queue rather
// than with every closed task in history, on both SQLite and PostgreSQL, while
// staying out of `staterepo` exports (staterepo.LocalTables).
const taskOpenTable = `CREATE TABLE IF NOT EXISTS ` + "`task_open`" + ` (
  ` + "`task_id`" + ` TEXT NOT NULL,
  PRIMARY KEY (` + "`task_id`" + `)
)`

// taskLinkTaskBlocksIndex lets correlated blocker checks in `task_ready` and
// per-task `task_blocked` lookups probe only a task's blocking links without
// touching the `task_link` table or scanning links of unrelated tasks.
const taskLinkTaskBlocksIndex = "CREATE INDEX IF NOT EXISTS `task_link_task_blocks` ON `task_link` (`task_id`, `blocks`, `target`)"

// taskPRFeedbackPendingIndex narrows PR-feedback readiness checks to the few
// tasks that actually have undelivered feedback receipts.
const taskPRFeedbackPendingIndex = "CREATE INDEX IF NOT EXISTS `task_pr_feedback_pending` ON `task_pr_feedback` (`task_id`) WHERE `delivered` = 0"

const pgTaskOpenOnTaskFunc = `CREATE OR REPLACE FUNCTION "grain_task_open_on_task"() RETURNS TRIGGER
LANGUAGE plpgsql AS '
BEGIN
  IF TG_OP = ''DELETE'' THEN
    EXECUTE format(''DELETE FROM %I."task_open" WHERE "task_id" = $1'', TG_TABLE_SCHEMA) USING OLD."id";
    RETURN OLD;
  END IF;
  IF TG_OP = ''UPDATE'' THEN
    IF OLD."id" IS NOT DISTINCT FROM NEW."id" THEN
      RETURN NEW;
    END IF;
    EXECUTE format(''DELETE FROM %I."task_open" WHERE "task_id" = $1'', TG_TABLE_SCHEMA) USING OLD."id";
  END IF;
  EXECUTE format(
    ''INSERT INTO %I."task_open" ("task_id") SELECT $1 WHERE NOT EXISTS (SELECT 1 FROM %I."task_observation" WHERE "task_id" = $1 AND "closed_at" IS NOT NULL) ON CONFLICT DO NOTHING'',
    TG_TABLE_SCHEMA, TG_TABLE_SCHEMA
  ) USING NEW."id";
  RETURN NEW;
END;
'`

const pgTaskOpenOnObservationFunc = `CREATE OR REPLACE FUNCTION "grain_task_open_on_observation"() RETURNS TRIGGER
LANGUAGE plpgsql AS '
BEGIN
  IF TG_OP = ''DELETE'' THEN
    IF OLD."closed_at" IS NOT NULL THEN
      EXECUTE format(
        ''INSERT INTO %I."task_open" ("task_id") SELECT $1 WHERE EXISTS (SELECT 1 FROM %I."task" WHERE "id" = $1) ON CONFLICT DO NOTHING'',
        TG_TABLE_SCHEMA, TG_TABLE_SCHEMA
      ) USING OLD."task_id";
    END IF;
    RETURN OLD;
  END IF;
  IF TG_OP = ''UPDATE'' AND OLD."task_id" IS DISTINCT FROM NEW."task_id" THEN
    IF OLD."closed_at" IS NOT NULL THEN
      EXECUTE format(
        ''INSERT INTO %I."task_open" ("task_id") SELECT $1 WHERE EXISTS (SELECT 1 FROM %I."task" WHERE "id" = $1) ON CONFLICT DO NOTHING'',
        TG_TABLE_SCHEMA, TG_TABLE_SCHEMA
      ) USING OLD."task_id";
    END IF;
  END IF;
  IF NEW."closed_at" IS NOT NULL THEN
    EXECUTE format(''DELETE FROM %I."task_open" WHERE "task_id" = $1'', TG_TABLE_SCHEMA) USING NEW."task_id";
  ELSIF TG_OP = ''UPDATE'' AND OLD."closed_at" IS NOT NULL THEN
    EXECUTE format(
      ''INSERT INTO %I."task_open" ("task_id") SELECT $1 WHERE EXISTS (SELECT 1 FROM %I."task" WHERE "id" = $1) ON CONFLICT DO NOTHING'',
      TG_TABLE_SCHEMA, TG_TABLE_SCHEMA
    ) USING NEW."task_id";
  END IF;
  RETURN NEW;
END;
'`

// installTaskOpenIndex installs the triggers that keep `task_open` synchronized
// with `task` and `task_observation`, then reconciles `task_open` from the
// current rows. It is idempotent and runs both during fresh Init, migration
// 154, and ApplyDataMigrations for older state-repository dumps.
func installTaskOpenIndex(ctx context.Context, tx *sql.Tx) error {
	var ddl string
	if err := tx.QueryRowContext(ctx,
		"SELECT `sql` FROM `sqlite_master` WHERE `type` = 'table' AND `name` = 'task'").Scan(&ddl); err != nil {
		return fmt.Errorf("reading task table definition: %w", err)
	}
	if ddl == "" {
		// PostgreSQL: sqlite_master is a compatibility view whose sql column is
		// always empty. Trigger functions qualify table names with
		// TG_TABLE_SCHEMA so they remain safe under empty search_path (such as
		// pg_restore) and isolated test schemas.
		for _, stmt := range []string{
			pgTaskOpenOnTaskFunc,
			pgTaskOpenOnObservationFunc,
			`DROP TRIGGER IF EXISTS "task_open_after_task_change" ON "task"`,
			`CREATE TRIGGER "task_open_after_task_change" AFTER INSERT OR DELETE ON "task" FOR EACH ROW EXECUTE FUNCTION "grain_task_open_on_task"()`,
			`DROP TRIGGER IF EXISTS "task_open_after_task_id_update" ON "task"`,
			`CREATE TRIGGER "task_open_after_task_id_update" AFTER UPDATE OF "id" ON "task" FOR EACH ROW WHEN (OLD."id" IS DISTINCT FROM NEW."id") EXECUTE FUNCTION "grain_task_open_on_task"()`,
			`DROP TRIGGER IF EXISTS "task_open_after_obs_insert" ON "task_observation"`,
			`CREATE TRIGGER "task_open_after_obs_insert" AFTER INSERT ON "task_observation" FOR EACH ROW WHEN (NEW."closed_at" IS NOT NULL) EXECUTE FUNCTION "grain_task_open_on_observation"()`,
			`DROP TRIGGER IF EXISTS "task_open_after_obs_update" ON "task_observation"`,
			`CREATE TRIGGER "task_open_after_obs_update" AFTER UPDATE OF "task_id", "closed_at" ON "task_observation" FOR EACH ROW WHEN (OLD."task_id" IS DISTINCT FROM NEW."task_id" OR (OLD."closed_at" IS NULL) IS DISTINCT FROM (NEW."closed_at" IS NULL)) EXECUTE FUNCTION "grain_task_open_on_observation"()`,
			`DROP TRIGGER IF EXISTS "task_open_after_obs_delete" ON "task_observation"`,
			`CREATE TRIGGER "task_open_after_obs_delete" AFTER DELETE ON "task_observation" FOR EACH ROW WHEN (OLD."closed_at" IS NOT NULL) EXECUTE FUNCTION "grain_task_open_on_observation"()`,
		} {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("installing postgres task_open triggers: %w", err)
			}
		}
	} else {
		// SQLite: REPLACE INTO does not fire DELETE triggers when
		// recursive_triggers is off (SQLite's default), so AFTER INSERT on
		// task_observation handles both closing (NEW.closed_at IS NOT NULL)
		// and reopening via REPLACE (NEW.closed_at IS NULL).
		for _, stmt := range []string{
			"CREATE TRIGGER IF NOT EXISTS `task_open_after_task_insert` AFTER INSERT ON `task` FOR EACH ROW " +
				"BEGIN " +
				"INSERT OR IGNORE INTO `task_open` (`task_id`) " +
				"SELECT NEW.`id` WHERE NOT EXISTS (SELECT 1 FROM `task_observation` WHERE `task_id` = NEW.`id` AND `closed_at` IS NOT NULL); " +
				"END",
			"CREATE TRIGGER IF NOT EXISTS `task_open_after_task_delete` AFTER DELETE ON `task` FOR EACH ROW " +
				"BEGIN " +
				"DELETE FROM `task_open` WHERE `task_id` = OLD.`id`; " +
				"END",
			"CREATE TRIGGER IF NOT EXISTS `task_open_after_task_update` AFTER UPDATE OF `id` ON `task` FOR EACH ROW WHEN OLD.`id` <> NEW.`id` " +
				"BEGIN " +
				"DELETE FROM `task_open` WHERE `task_id` = OLD.`id`; " +
				"INSERT OR IGNORE INTO `task_open` (`task_id`) " +
				"SELECT NEW.`id` WHERE NOT EXISTS (SELECT 1 FROM `task_observation` WHERE `task_id` = NEW.`id` AND `closed_at` IS NOT NULL); " +
				"END",
			"CREATE TRIGGER IF NOT EXISTS `task_open_after_obs_insert_closed` AFTER INSERT ON `task_observation` FOR EACH ROW WHEN NEW.`closed_at` IS NOT NULL " +
				"BEGIN " +
				"DELETE FROM `task_open` WHERE `task_id` = NEW.`task_id`; " +
				"END",
			"CREATE TRIGGER IF NOT EXISTS `task_open_after_obs_insert_open` AFTER INSERT ON `task_observation` FOR EACH ROW WHEN NEW.`closed_at` IS NULL " +
				"BEGIN " +
				"INSERT OR IGNORE INTO `task_open` (`task_id`) " +
				"SELECT NEW.`task_id` WHERE EXISTS (SELECT 1 FROM `task` WHERE `id` = NEW.`task_id`); " +
				"END",
			"CREATE TRIGGER IF NOT EXISTS `task_open_after_obs_update` AFTER UPDATE OF `task_id`, `closed_at` ON `task_observation` FOR EACH ROW " +
				"WHEN OLD.`task_id` <> NEW.`task_id` OR (OLD.`closed_at` IS NULL) <> (NEW.`closed_at` IS NULL) " +
				"BEGIN " +
				"INSERT OR IGNORE INTO `task_open` (`task_id`) " +
				"SELECT OLD.`task_id` WHERE OLD.`task_id` <> NEW.`task_id` AND OLD.`closed_at` IS NOT NULL AND EXISTS (SELECT 1 FROM `task` WHERE `id` = OLD.`task_id`); " +
				"DELETE FROM `task_open` WHERE `task_id` = NEW.`task_id` AND NEW.`closed_at` IS NOT NULL; " +
				"INSERT OR IGNORE INTO `task_open` (`task_id`) " +
				"SELECT NEW.`task_id` WHERE NEW.`closed_at` IS NULL AND EXISTS (SELECT 1 FROM `task` WHERE `id` = NEW.`task_id`); " +
				"END",
			"CREATE TRIGGER IF NOT EXISTS `task_open_after_obs_delete` AFTER DELETE ON `task_observation` FOR EACH ROW WHEN OLD.`closed_at` IS NOT NULL " +
				"BEGIN " +
				"INSERT OR IGNORE INTO `task_open` (`task_id`) " +
				"SELECT OLD.`task_id` WHERE EXISTS (SELECT 1 FROM `task` WHERE `id` = OLD.`task_id`); " +
				"END",
		} {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("installing sqlite task_open triggers: %w", err)
			}
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM `task_open`"); err != nil {
		return fmt.Errorf("clearing task_open for backfill: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO `task_open` (`task_id`) "+
			"SELECT `t`.`id` FROM `task` AS `t` "+
			"LEFT JOIN `task_observation` AS `o` ON `o`.`task_id` = `t`.`id` "+
			"WHERE `o`.`closed_at` IS NULL"); err != nil {
		return fmt.Errorf("backfilling task_open: %w", err)
	}
	return nil
}
