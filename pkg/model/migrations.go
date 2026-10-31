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
	"strings"
)

// In-place migrations: how a store written by grain 2.0.0 or later is
// carried to every later schema without leaving the database it is in.
//
// V2BaselineSchemaVersion is the schema grain 2.0.0 shipped, frozen in
// pkg/model/v2baseline. Every SchemaVersion past it brings one Migration
// here, keyed by the version it produces, written in the same SQLite
// dialect as Tables -- the Postgres store runs it through the same
// adapter (pkg/model/postgres) every other statement goes through, so one
// migration serves both engines. Init runs the ones a store is missing,
// in order, in one transaction with the schema itself, and restamps the
// store; a failure leaves the store exactly as it was.
//
// pkg/model/v2baseline's tests are what hold this to the policy in
// schema.go's SchemaVersion comment: a bump with no Migration fails
// TestEveryVersionPastTheV2BaselineHasAMigration, and a Migration that
// does not bring the 2.0 schema to what Init creates fresh fails the
// fixture store tests on SQLite and on Postgres.

// A Migration carries a store from Version-1 to Version in place.
type Migration struct {
	// Version is the SchemaVersion this migration produces.
	Version int
	// Description says what changed, for the log line Init writes.
	Description string
	// Statements run in order, after the store's views are dropped and
	// before Statements() re-creates them and any new table. SQLite
	// dialect, as Tables is.
	Statements []string
	// Up runs after Statements, for a change SQL alone cannot say (rows
	// that have to be rewritten in Go). Optional.
	Up func(ctx context.Context, tx *sql.Tx) error
}

// Migrations are the in-place migrations past V2BaselineSchemaVersion, in
// version order.
var Migrations = []Migration{
	{
		Version:     28,
		Description: "add Kubernetes sandbox settings to grain_config",
		Statements: []string{
			"ALTER TABLE `grain_config` ADD COLUMN `k8s_namespace` TEXT NOT NULL DEFAULT 'grain'",
			"ALTER TABLE `grain_config` ADD COLUMN `k8s_kubectl` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `grain_config` ADD COLUMN `k8s_sandbox_image` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `grain_config` ADD COLUMN `k8s_granule_image` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `grain_config` ADD COLUMN `k8s_workspace` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `grain_config` ADD COLUMN `k8s_git_proxy_host` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `grain_config` ADD COLUMN `k8s_sandbox_service` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `grain_config` ADD COLUMN `k8s_service_account` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `grain_config` ADD COLUMN `k8s_node_selector` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `grain_config` ADD COLUMN `k8s_privileged` INTEGER NOT NULL DEFAULT 0",
			"ALTER TABLE `grain_config` ADD COLUMN `k8s_network_policy` INTEGER NOT NULL DEFAULT 0",
			"ALTER TABLE `grain_config` ADD COLUMN `k8s_deployment` TEXT NOT NULL DEFAULT 'grain'",
			"ALTER TABLE `grain_config` ADD COLUMN `k8s_container` TEXT NOT NULL DEFAULT 'grain'",
		},
	},
	{
		Version:     29,
		Description: "add capability servers (mcpServers) to grain_config",
		Statements: []string{
			"ALTER TABLE `grain_config` ADD COLUMN `mcp_servers` TEXT NOT NULL DEFAULT ''",
		},
	},
	{
		Version:     30,
		Description: "add the capability admission policy to grain_config",
		Statements: []string{
			"ALTER TABLE `grain_config` ADD COLUMN `capability_policy` TEXT NOT NULL DEFAULT ''",
		},
	},
	{
		Version:     31,
		Description: "add each agent CLI's run location to grain_config",
		Statements: []string{
			"ALTER TABLE `grain_config` ADD COLUMN `cli_run` TEXT NOT NULL DEFAULT ''",
		},
	},
	{
		Version:     32,
		Description: "record which person-cancelled grain CI run the merge queue handed back to its agent",
		Statements: []string{
			"ALTER TABLE `task_observation` ADD COLUMN `merge_queue_cancel_handoff_at` DATETIME NULL",
			"ALTER TABLE `task_observation` ADD COLUMN `merge_queue_cancel_handoff_run` TEXT NULL",
		},
	},
	{
		Version:     33,
		Description: "add the everyNMinutes recurrence's interval to schedule",
		Statements: []string{
			"ALTER TABLE `schedule` ADD COLUMN `every_n_minutes` INTEGER NULL",
		},
	},
	{
		Version:     34,
		Description: "add each persistent sandbox's refresh policy",
		Statements: []string{
			"ALTER TABLE `persistent_sandbox` ADD COLUMN `refresh` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `persistent_sandbox` ADD COLUMN `rollback_on_crash` INTEGER NOT NULL DEFAULT 0",
		},
	},
	{
		Version:     35,
		Description: "add the sandbox overcommit mode (fixed or auto) and its bounds to grain_config",
		Statements: []string{
			"ALTER TABLE `grain_config` ADD COLUMN `sandbox_overcommit` TEXT NOT NULL DEFAULT ''",
		},
	},
	{
		Version:     36,
		Description: "add each repository's merge-on-green switch to repo_config",
		Statements: []string{
			"ALTER TABLE `repo_config` ADD COLUMN `merge_on_green` INTEGER NOT NULL DEFAULT 0",
		},
	},
	{
		Version:     37,
		Description: "add each repository's base_repair_bypasses_human_gates switch to repo_config",
		Statements: []string{
			"ALTER TABLE `repo_config` ADD COLUMN `base_repair_bypasses_human_gates` INTEGER NOT NULL DEFAULT 0",
		},
	},
	{
		Version:     38,
		Description: "add each template's interactive switch",
		Statements: []string{
			"ALTER TABLE `template` ADD COLUMN `interactive` INTEGER NOT NULL DEFAULT 0",
		},
	},
	{
		// base_hold_history is new, so Statements() creates it.
		Version:     39,
		Description: "record why each merge batch was abandoned, and what each base-branch red held",
		Statements: []string{
			"ALTER TABLE `merge_batch_history` ADD COLUMN `reason` TEXT NOT NULL DEFAULT ''",
		},
	},
	{
		Version:     40,
		Description: "record the sub-case of each merge-queue state on task_observation",
		Statements: []string{
			"ALTER TABLE `task_observation` ADD COLUMN `merge_queue_reason` TEXT NULL",
		},
	},
	{
		Version:     41,
		Description: "add each repository's merge re-check mode to repo_config",
		Statements: []string{
			"ALTER TABLE `repo_config` ADD COLUMN `merge_recheck` TEXT NOT NULL DEFAULT ''",
		},
	},
	{
		Version:     42,
		Description: "confirm a base branch red on a streak of consecutive red tips",
		Statements: []string{
			"ALTER TABLE `grain_config` ADD COLUMN `base_ci_red_streak` INTEGER NOT NULL DEFAULT 0",
			"ALTER TABLE `base_branch_ci` ADD COLUMN `red_streak` INTEGER NOT NULL DEFAULT 0",
			"ALTER TABLE `base_branch_ci` ADD COLUMN `red_streak_jobs` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `base_branch_ci` ADD COLUMN `red_streak_since` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `base_branch_ci` ADD COLUMN `red_streak_tip` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `base_branch_ci` ADD COLUMN `last_green_commit` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `base_branch_ci` ADD COLUMN `red_streak_woken` TEXT NOT NULL DEFAULT ''",
		},
	},
	{
		Version:     43,
		Description: "record on each run the Run now request it spent",
		Statements: []string{
			"ALTER TABLE `task_run` ADD COLUMN `run_now_at` DATETIME NULL",
		},
	},
	{
		// The script is dropped rather than carried: it was shell to run
		// inside a booted guest, and there is no OCI reference it could
		// become. A deployment that had one keeps the stock image until
		// somebody builds the same packages into a custom image FROM
		// guest-base or k8s-sandbox-base (docs/sandbox-image-contract.md).
		Version:     44,
		Description: "replace grain_config's sandbox setup script with a custom sandbox OCI image reference",
		Statements: []string{
			"ALTER TABLE `grain_config` ADD COLUMN `sandbox_image` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `grain_config` DROP COLUMN `sandbox_setup_script`",
		},
	},
	{
		Version:     45,
		Description: "record suspensions and the dispatch standing a run started with",
		Statements: []string{
			"ALTER TABLE `task_run` ADD COLUMN `suspend_kind` TEXT NULL",
			"ALTER TABLE `task_run` ADD COLUMN `standing` TEXT NULL",
			"UPDATE `task_run` SET `suspend_kind` = CASE `outcome` " +
				"WHEN 'paused' THEN 'usage-limit' WHEN 'capacity' THEN 'capacity' " +
				"WHEN 'interrupted' THEN 'restart' WHEN 'oom' THEN 'oom' END " +
				"WHERE `outcome` IN ('paused','capacity','interrupted','oom')",
			"CREATE TABLE IF NOT EXISTS `task_suspension` (" +
				"`run_id` TEXT NOT NULL, `task_id` TEXT NOT NULL, `kind` TEXT NOT NULL, " +
				"`detail` TEXT NOT NULL, `resume_on` TEXT NULL, `resume_at` DATETIME NULL, " +
				"`standing` TEXT NOT NULL, `model_hint` TEXT NULL, `created_at` DATETIME NOT NULL, " +
				"`resumed_at` DATETIME NULL, `resumed_run_id` TEXT NULL, PRIMARY KEY (`run_id`))",
			"CREATE INDEX IF NOT EXISTS `task_suspension_open` ON `task_suspension` (`task_id`) WHERE `resumed_at` IS NULL",
		},
	},
	{
		Version:     46,
		Description: "add the PriorityClass each kind of Kubernetes sandbox pod runs at to grain_config",
		Statements: []string{
			"ALTER TABLE `grain_config` ADD COLUMN `k8s_priority_class_interactive` TEXT NOT NULL DEFAULT 'grain-interactive'",
			"ALTER TABLE `grain_config` ADD COLUMN `k8s_priority_class_agent` TEXT NOT NULL DEFAULT 'grain-agent'",
			"ALTER TABLE `grain_config` ADD COLUMN `k8s_priority_class_ci` TEXT NOT NULL DEFAULT 'grain-ci'",
		},
	},
	{
		Version:     47,
		Description: "record on each run the model and agent framework it ran on",
		Statements: []string{
			"ALTER TABLE `task_run` ADD COLUMN `model` TEXT NULL",
			"ALTER TABLE `task_run` ADD COLUMN `agent_framework` TEXT NULL",
		},
	},
	{
		Version:     48,
		Description: "add the sandbox disk overcommit ratio to grain_config",
		Statements: []string{
			"ALTER TABLE `grain_config` ADD COLUMN `sandbox_disk_overcommit` REAL NOT NULL DEFAULT 2",
		},
	},
	{
		// The table itself is created by Statements, which Migrate
		// applies after every rung; only the columns need saying here.
		Version:     49,
		Description: "add the daily automatic upgrade's channel and time of day to grain_config",
		Statements: []string{
			"ALTER TABLE `grain_config` ADD COLUMN `auto_upgrade_channel` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `grain_config` ADD COLUMN `auto_upgrade_time` TEXT NOT NULL DEFAULT ''",
		},
	},
	{
		Version:     50,
		Description: "add each task's resume_on_deadline flag and record parks as requested suspensions",
		Statements: []string{
			"ALTER TABLE `task` ADD COLUMN `resume_on_deadline` INTEGER NOT NULL DEFAULT 0",
			"UPDATE `task_run` SET `suspend_kind` = 'requested' WHERE `outcome` = 'parked'",
		},
	},
	{
		Version:     51,
		Description: "add each CI job's per-attempt logs and metadata to ci_job",
		Statements: []string{
			"ALTER TABLE `ci_job` ADD COLUMN `attempts` TEXT NOT NULL DEFAULT ''",
		},
	},
	{
		// task_goal itself is created by Statements.
		Version:     52,
		Description: "add task goals and the goal a template files its tasks with",
		Statements: []string{
			"ALTER TABLE `template` ADD COLUMN `goal` TEXT NOT NULL DEFAULT ''",
		},
	},
	{
		Version:     53,
		Description: "record usage-limit pauses so they survive a daemon restart",
		Statements:  []string{usageLimitPauseTable},
	},
	{
		Version:     54,
		Description: "add the model the symbolic judgement model resolves to to grain_config",
		Statements: []string{
			"ALTER TABLE `grain_config` ADD COLUMN `judgement_model` TEXT NOT NULL DEFAULT ''",
		},
	},
	{
		Version:     55,
		Description: "declare the agent API's scope sets as settings",
		Statements:  []string{apiScopeSetTable},
	},
	{
		Version:     56,
		Description: "record GitHub Actions jobs and check runs alongside grain CI",
		Statements:  []string{gitHubCIJobTable},
	},
	{
		Version:     57,
		Description: "add a question's one-line summary to task_comment",
		Statements: []string{
			"ALTER TABLE `task_comment` ADD COLUMN `summary` TEXT NOT NULL DEFAULT ''",
		},
	},
	{
		Version:     58,
		Description: "add each task's non-git change target (a registered changelist workspace) to task and template",
		Statements: []string{
			"ALTER TABLE `task` ADD COLUMN `change_target_kind` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `task` ADD COLUMN `change_target_spec` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `template` ADD COLUMN `change_target_kind` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `template` ADD COLUMN `change_target_spec` TEXT NOT NULL DEFAULT ''",
		},
	},
	{
		Version:     59,
		Description: "add the external agent API setting and per-agent scoped credentials",
		Statements: []string{
			"ALTER TABLE `grain_config` ADD COLUMN `external_agent_api` INTEGER NOT NULL DEFAULT 0",
			externalAgentTokenTable,
			externalAgentTokenSequenceTable,
		},
	},
	{
		Version:     60,
		Description: "label each recorded CI wait with the CI that answered it (grain, github, tap)",
		Statements: []string{
			"ALTER TABLE `task_run_check_wait` ADD COLUMN `provider` TEXT NOT NULL DEFAULT ''",
		},
	},
	{
		Version:     61,
		Description: "add person-set reviewers to task and template",
		Statements: []string{
			"ALTER TABLE `task` ADD COLUMN `reviewers` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `template` ADD COLUMN `reviewers` TEXT NOT NULL DEFAULT ''",
		},
	},
	{
		Version:     62,
		Description: "persist sandbox-access token hashes and expiry times across restarts",
		Statements:  []string{sandboxAccessTable},
	},
	{
		Version:     63,
		Description: "add sandbox_image_mirror to grain_config for mirroring stamped sandbox images",
		Statements: []string{
			"ALTER TABLE `grain_config` ADD COLUMN `sandbox_image_mirror` TEXT NOT NULL DEFAULT ''",
		},
	},
	{
		Version:     64,
		Description: "persist running CI steps' exec ids and cursors so a restart reattaches them",
		Statements:  []string{ciStepAttachmentTable},
	},
	{
		Version:     65,
		Description: "add each repository's GitHub issue importer settings to repo_config",
		Statements: []string{
			"ALTER TABLE `repo_config` ADD COLUMN `issue_import_enabled` INTEGER NOT NULL DEFAULT 0",
			"ALTER TABLE `repo_config` ADD COLUMN `issue_import_repo` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `repo_config` ADD COLUMN `issue_import_label` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `repo_config` ADD COLUMN `issue_import_approved` INTEGER NOT NULL DEFAULT 0",
			"ALTER TABLE `repo_config` ADD COLUMN `issue_import_interval_seconds` INTEGER NOT NULL DEFAULT 0",
		},
	},
	{
		Version:     66,
		Description: "add session_sandbox_idle_ttl_ms to grain_config for keeping session sandboxes warm between turns",
		Statements: []string{
			"ALTER TABLE `grain_config` ADD COLUMN `session_sandbox_idle_ttl_ms` INTEGER NOT NULL DEFAULT 0",
		},
	},
	{
		Version:     67,
		Description: "rewrite stored team-broadcast grants to api:team-broadcast (the in-process capability is retired)",
		Up:          rewriteLegacyGrants(map[string]string{"team-broadcast": "api:team-broadcast"}),
	},
	{
		Version:     68,
		Description: "rewrite stored dispatch-control grants to api:dispatch-control (the in-process capability is retired)",
		Up:          rewriteLegacyGrants(map[string]string{"dispatch-control": "api:dispatch-control"}),
	},
	{
		Version:     69,
		Description: "rewrite stored pipeline-control grants to api:pipeline-control (the in-process capability is retired)",
		Up:          rewriteLegacyGrants(map[string]string{"pipeline-control": "api:pipeline-control"}),
	},
	{
		Version:     70,
		Description: "add settings_locked to grain_config for opt-in UI/API settings lock",
		Statements: []string{
			"ALTER TABLE `grain_config` ADD COLUMN `settings_locked` INTEGER NOT NULL DEFAULT 0",
		},
	},
	{
		Version:     71,
		Description: "rewrite stored memory grants to api:memory (the in-process capability is retired)",
		Up:          rewriteLegacyGrants(map[string]string{"memory": "api:memory"}),
	},
	{
		Version:     72,
		Description: "rewrite stored release-notes grants to api:release-notes (the in-process capability is retired)",
		Up:          rewriteLegacyGrants(map[string]string{"release-notes": "api:release-notes"}),
	},
	{
		Version:     73,
		Description: "add numeric priority to task (default 5) and template",
		Statements: []string{
			"ALTER TABLE `task` ADD COLUMN `priority` INTEGER NOT NULL DEFAULT 5",
			"ALTER TABLE `template` ADD COLUMN `priority` INTEGER NOT NULL DEFAULT 0",
		},
	},
	{
		Version:     74,
		Description: "add keep_running flag to task, template and schedule",
		Statements: []string{
			"ALTER TABLE `task` ADD COLUMN `keep_running` INTEGER NOT NULL DEFAULT 0",
			"ALTER TABLE `template` ADD COLUMN `keep_running` INTEGER NOT NULL DEFAULT 0",
			"ALTER TABLE `schedule` ADD COLUMN `keep_running` INTEGER NOT NULL DEFAULT 0",
		},
	},
	{
		Version:     75,
		Description: "add the New Task form's configuration to grain_config",
		Statements: []string{
			"ALTER TABLE `grain_config` ADD COLUMN `new_task_form` TEXT NOT NULL DEFAULT ''",
		},
	},
}

// migrationFor returns the migration that produces version, or false.
func migrationFor(version int) (Migration, bool) {
	for _, m := range Migrations {
		if m.Version == version {
			return m, true
		}
	}
	return Migration{}, false
}

// MissingMigrations names each version in (V2BaselineSchemaVersion,
// SchemaVersion] that has no Migration -- a schema bump shipped without
// the in-place migration the policy requires. Empty when the policy
// holds.
func MissingMigrations() []int {
	var missing []int
	for v := V2BaselineSchemaVersion + 1; v <= SchemaVersion; v++ {
		if _, ok := migrationFor(v); !ok {
			missing = append(missing, v)
		}
	}
	return missing
}

// migrateInPlace carries a store stamped `from` (at or past the 2.0
// baseline, below SchemaVersion) to SchemaVersion: it drops the store's
// views, runs each missing Migration, re-applies the schema and restamps
// the store, all in one transaction.
func (s *Store) migrateInPlace(ctx context.Context, from int) error {
	if missing := MissingMigrations(); len(missing) > 0 {
		return fmt.Errorf("%w: found %d, this build knows %d and ships no in-place migration to %v",
			ErrSchemaTooOld, from, SchemaVersion, missing)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("migrating schema %d to %d: %w", from, SchemaVersion, err)
	}
	defer func() { _ = tx.Rollback() }()
	// Read again under the transaction: a second process starting on the
	// same store may have migrated it while this one waited for the lock.
	var stamped int
	if err := tx.QueryRowContext(ctx,
		"SELECT `version` FROM `grain_schema` WHERE `id` = 1").Scan(&stamped); err != nil {
		return fmt.Errorf("migrating schema %d to %d: reading the stamp: %w", from, SchemaVersion, err)
	}
	if stamped != from {
		return nil
	}
	// Views first: SQLite refuses to drop or rename a column a view
	// still reads, and the store's views are re-created whole below.
	views, err := storeViews(ctx, tx)
	if err != nil {
		return fmt.Errorf("migrating schema %d to %d: listing views: %w", from, SchemaVersion, err)
	}
	for _, v := range views {
		if _, err := tx.ExecContext(ctx, "DROP VIEW IF EXISTS `"+v+"`"); err != nil {
			return fmt.Errorf("migrating schema %d to %d: dropping view %s: %w", from, SchemaVersion, v, err)
		}
	}
	for v := from + 1; v <= SchemaVersion; v++ {
		m, _ := migrationFor(v)
		for _, stmt := range m.Statements {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				if strings.Contains(strings.ToUpper(stmt), " ADD COLUMN ") &&
					(strings.Contains(strings.ToLower(err.Error()), "duplicate column name:") ||
						strings.Contains(strings.ToLower(err.Error()), "already exists")) {
					continue
				}
				return fmt.Errorf("migrating schema to %d (%s): %w", v, m.Description, err)
			}
		}
		if m.Up != nil {
			if err := m.Up(ctx, tx); err != nil {
				return fmt.Errorf("migrating schema to %d (%s): %w", v, m.Description, err)
			}
		}
	}
	for _, stmt := range Statements() {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrating schema %d to %d: applying schema: %w", from, SchemaVersion, err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		"UPDATE `grain_schema` SET `version` = ? WHERE `id` = 1", SchemaVersion); err != nil {
		return fmt.Errorf("migrating schema %d to %d: restamping: %w", from, SchemaVersion, err)
	}
	return tx.Commit()
}

// storeViews names the views the store holds, whichever build made
// them. sqlite_master is itself a view on Postgres (pkg/model/postgres's
// compatibility view), and not one of the store's.
func storeViews(ctx context.Context, tx *sql.Tx) ([]string, error) {
	rows, err := tx.QueryContext(ctx, "SELECT `name` FROM `sqlite_master` WHERE `type` = 'view'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var views []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		if strings.EqualFold(name, "sqlite_master") {
			continue
		}
		views = append(views, name)
	}
	return views, rows.Err()
}
