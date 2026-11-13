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
	"regexp"
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
		Description: "rewrite stored self-upgrade grants to api:self-upgrade (the in-process capability is retired)",
		Up:          rewriteLegacyGrants(map[string]string{"self-upgrade": "api:self-upgrade"}),
	},
	{
		Version:     76,
		Description: "rewrite stored test-quarantine grants to api:test-quarantine (the in-process capability is retired)",
		Up:          rewriteLegacyGrants(map[string]string{"test-quarantine": "api:test-quarantine"}),
	},
	{
		Version:     77,
		Description: "add the New Task form's configuration to grain_config",
		Statements: []string{
			"ALTER TABLE `grain_config` ADD COLUMN `new_task_form` TEXT NOT NULL DEFAULT ''",
		},
	},
	{
		Version:     78,
		Description: "upgrade the pre-slug default task_branch_template (grain/{deployment}/task-{id}) to grain/{deployment}/task-{id}-{slug}",
		Statements: []string{
			"UPDATE `grain_config` SET `task_branch_template` = '" + DefaultTaskBranchTemplate + "' WHERE `task_branch_template` = '" + UnsluggedTaskBranchTemplate + "'",
		},
	},
	{
		Version:     79,
		Description: "add pending_config_change to task_observation for propose_config_change",
		Statements: []string{
			"ALTER TABLE `task_observation` ADD COLUMN `pending_config_change` TEXT NULL",
		},
	},
	{
		Version:     80,
		Description: "rewrite stored grain-log grants to https://grain.dev/capabilities/grain#journal={} (the in-process capability is retired)",
		Up:          rewriteLegacyGrants(map[string]string{"grain-log": GrainJournalGrant}),
	},
	{
		Version:     81,
		Description: "rewrite stored deployment-health grants to api:deployment:read and https://grain.dev/capabilities/grain#health={} (the in-process capability is retired)",
		Up:          rewriteLegacyGrantsMulti(map[string][]string{"deployment-health": {"api:deployment:read", GrainHealthGrant}}),
	},
	{
		Version:     82,
		Description: "rewrite stored db-read and db-write grants to https://grain.dev/capabilities/grain#sql (the in-process capabilities are retired)",
		Up: rewriteLegacyGrants(map[string]string{
			"db-read":  GrainSQLReadGrant,
			"db-write": GrainSQLWriteGrant,
		}),
	},
	{
		Version:     83,
		Description: "rewrite retired capability grants in repo_config.ci_capabilities",
		Up: func(ctx context.Context, tx *sql.Tx) error {
			return rewriteCapabilityListColumnMulti(ctx, tx, "repo_config", []string{"owner", "name"}, "ci_capabilities", map[string][]string{
				"team-broadcast":    {"api:team-broadcast"},
				"dispatch-control":  {"api:dispatch-control"},
				"pipeline-control":  {"api:pipeline-control"},
				"memory":            {"api:memory"},
				"release-notes":     {"api:release-notes"},
				"self-upgrade":      {"api:self-upgrade"},
				"test-quarantine":   {"api:test-quarantine"},
				"grain-log":         {GrainJournalGrant},
				"deployment-health": {"api:deployment:read", GrainHealthGrant},
				"db-read":           {GrainSQLReadGrant},
				"db-write":          {GrainSQLWriteGrant},
			})
		},
	},
	{
		Version:     84,
		Description: "rewrite stored self-repair grants, repo_config.ci_capabilities included, to https://grain.dev/capabilities/grain#host-exec={} (the in-process capability is retired)",
		Up: func(ctx context.Context, tx *sql.Tx) error {
			renames := map[string]string{"self-repair": GrainHostExecGrant}
			if err := rewriteLegacyGrants(renames)(ctx, tx); err != nil {
				return err
			}
			return rewriteCapabilityListColumnMulti(ctx, tx, "repo_config", []string{"owner", "name"}, "ci_capabilities",
				map[string][]string{"self-repair": {GrainHostExecGrant}})
		},
	},
	{
		Version:     85,
		Description: "rewrite stored git-push-any-ref grants to https://grain.dev/capabilities/repo#write (the in-process capability is retired)",
		Up: func(ctx context.Context, tx *sql.Tx) error {
			renames := map[string][]string{GitPushAnyRefCapability: {GitPushAnyRefGrant}}
			if err := rewriteLegacyGrantsMulti(renames)(ctx, tx); err != nil {
				return err
			}
			// Unlike 67-82 (which 83 caught up on), this one rewrites
			// repo_config.ci_capabilities at once.
			return rewriteCapabilityListColumnMulti(ctx, tx, "repo_config", []string{"owner", "name"}, "ci_capabilities", renames)
		},
	},
	{
		Version:     86,
		Description: "rewrite stored corp-research grants, repo_config.ci_capabilities included, to https://grain.dev/capabilities/corp#research={} (a wrapper distribution's in-process capability is retired)",
		Up: func(ctx context.Context, tx *sql.Tx) error {
			renames := map[string]string{"corp-research": CorpResearchGrant}
			if err := rewriteLegacyGrants(renames)(ctx, tx); err != nil {
				return err
			}
			return rewriteCapabilityListColumnMulti(ctx, tx, "repo_config", []string{"owner", "name"}, "ci_capabilities",
				map[string][]string{"corp-research": {CorpResearchGrant}})
		},
	},
	{
		Version:     87,
		Description: "drop executor_token and executor_token_sequence (remote executors are retired)",
		Statements: []string{
			"DROP TABLE IF EXISTS `executor_token`",
			"DROP TABLE IF EXISTS `executor_token_sequence`",
		},
	},
	{
		Version:     88,
		Description: "rewrite stored repo-read:<repo> and repo-write:<repo> grants, repo_config.ci_capabilities included, to https://grain.dev/capabilities/repo#read / #write {repo} (the in-process providers are retired)",
		Up: func(ctx context.Context, tx *sql.Tx) error {
			renames, err := legacyRepoAccessRenames(ctx, tx)
			if err != nil || len(renames) == 0 {
				return err
			}
			if err := rewriteLegacyGrantsMulti(renames)(ctx, tx); err != nil {
				return err
			}
			return rewriteCapabilityListColumnMulti(ctx, tx, "repo_config", []string{"owner", "name"}, "ci_capabilities", renames)
		},
	},
	{
		Version:     89,
		Description: "rebuild merge_batch keyed by task as well as branch: one overlap re-check per change",
		// Neither engine changes a primary key in place. The rows are
		// copied out to a table with no constraints at all (CREATE TABLE
		// ... AS, so that no constraint name is left behind to collide
		// with the new table's on Postgres), and back into merge_batch
		// created afresh from the statement a new store uses.
		Statements: []string{
			"CREATE TABLE `merge_batch_v88` AS SELECT * FROM `merge_batch`",
			"DROP TABLE `merge_batch`",
			mergeBatchTable,
			"INSERT INTO `merge_batch` (`owner`,`name`,`branch`,`task_id`,`ref`,`base_commit`,`commit`," +
				"`members`,`run_id`,`started_at`,`finished_at`,`conclusion`,`detail`,`splits`,`queued`," +
				"`recheck_of`,`recheck_detail`,`recheck_at`) " +
				"SELECT `owner`,`name`,`branch`,'',`ref`,`base_commit`,`commit`," +
				"`members`,`run_id`,`started_at`,`finished_at`,`conclusion`,`detail`,`splits`,`queued`," +
				"`recheck_of`,`recheck_detail`,`recheck_at` FROM `merge_batch_v88`",
			"DROP TABLE `merge_batch_v88`",
		},
	},
	{
		Version:     90,
		Description: "fold retired in-process catalogue entries into api_scopes.presets in grain_config.capability_policy",
		Up: foldRetiredCatalogueInProcess(
			map[string]string{
				"team-broadcast":   "team-broadcast",
				"dispatch-control": "dispatch-control",
				"pipeline-control": "pipeline-control",
				"memory":           "memory",
				"release-notes":    "release-notes",
				"self-upgrade":     "self-upgrade",
				"test-quarantine":  "test-quarantine",
			},
			map[string][]string{
				"team-broadcast":        {"api:team-broadcast"},
				"dispatch-control":      {"api:dispatch-control"},
				"pipeline-control":      {"api:pipeline-control"},
				"memory":                {"api:memory"},
				"release-notes":         {"api:release-notes"},
				"self-upgrade":          {"api:self-upgrade"},
				"test-quarantine":       {"api:test-quarantine"},
				"grain-log":             {GrainJournalGrant},
				"deployment-health":     {"api:deployment:read", GrainHealthGrant},
				"db-read":               {GrainSQLReadGrant},
				"db-write":              {GrainSQLWriteGrant},
				"self-repair":           {GrainHostExecGrant},
				GitPushAnyRefCapability: {GitPushAnyRefGrant},
				"corp-research":         {CorpResearchGrant},
			},
		),
	},
	{
		Version:     91,
		Description: "add a *_source column beside every principal's *_kind/*_id pair, task_event's on-behalf-of, and config_audit (operator-attribution spec §1.3, §7.1)",
		Statements: append([]string{
			// task_pause and task_event postdate the 2.0 baseline and
			// Init's CREATE TABLE IF NOT EXISTS made them in place, so a
			// store this old may not have them yet: create them in the
			// shape schema 90 knew first, so that the ALTERs below meet
			// the same table whichever store they run on.
			schema90TaskPauseTable,
			schema90TaskEventTable,
		}, principalSourceColumns()...),
	},
	{
		Version:     92,
		Description: "add the hostd registry (hostd, hostd_token), with the co-located hostd as local",
		Statements: []string{
			schema92HostdTable,
			schema92HostdTokenTable,
		},
		Up: ensureLocalHostd,
	},
	{
		Version:     93,
		Description: "route host work per run: task_hostd, task_run_hostd, and the hostd of each task_run_capability_audit record",
		Statements: []string{
			taskHostdTable,
			taskRunHostdTable,
			// task_run_capability_audit postdates the 2.0 baseline and
			// Init made it in place, so a store this old may not have it
			// yet: create it in schema 92's shape first.
			schema92CapabilityAuditTable,
			"ALTER TABLE `task_run_capability_audit` ADD COLUMN `hostd` TEXT NOT NULL DEFAULT ''",
		},
	},
	{
		Version:     94,
		Description: "add the hostd selector a template's tasks start pinned to",
		Statements: []string{
			"ALTER TABLE `template` ADD COLUMN `hostd` TEXT NOT NULL DEFAULT ''",
		},
	},
	{
		Version:     95,
		Description: "raise grain_config.max_merges_per_repo's column default from 1 to 5 (stored values are kept)",
		Up:          raiseMaxMergesPerRepoDefault,
	},
	{
		Version:     96,
		Description: "rewrite stored grain-ci grants, repo_config.ci_capabilities included, to https://grain.dev/capabilities/grain#ci={} (the in-process capability is retired)",
		Up: func(ctx context.Context, tx *sql.Tx) error {
			renames := map[string]string{GrainCICapability: GrainCIGrant}
			if err := rewriteLegacyGrants(renames)(ctx, tx); err != nil {
				return err
			}
			return rewriteCapabilityListColumnMulti(ctx, tx, "repo_config", []string{"owner", "name"}, "ci_capabilities",
				map[string][]string{GrainCICapability: {GrainCIGrant}})
		},
	},
	{
		Version:     97,
		Description: "seed gcp#gcp-key permission sets from grain_config pool and gcp_key rows, and rewrite stored gcp-key and gcp-key:<name> grants (in-process gcp-key is retired)",
		Up:          migrateGCPKeyToCapabilityServer,
	},
	{
		Version:     98,
		Description: "add allowed_principal and allowed_principal_migration for verified-identity sign-in access",
		Statements: []string{
			allowedPrincipalTable,
			allowedPrincipalMigrationTable,
		},
	},
	{
		// schema99AutoUpgradeReleaseTable creates auto_upgrade_release at
		// its schema-99 shape so migration 139's ALTER TABLE runs cleanly
		// from any baseline predating 99.
		Version:     99,
		Description: "add grain_config.auto_upgrade_on, which turns on upgrading when a release is published",
		Statements: []string{
			"ALTER TABLE `grain_config` ADD COLUMN `auto_upgrade_on` TEXT NOT NULL DEFAULT ''",
			schema99AutoUpgradeReleaseTable,
		},
	},
	{
		Version:     100,
		Description: "rewrite stored claude-proxy, openai-proxy and gemini-proxy grants, repo_config.ci_capabilities and the capability policy included, to https://grain.dev/capabilities/model-proxy#model-proxy={\"vendor\":\"<vendor>\"} (the in-process providers are retired)",
		Up:          migrateModelProxyGrants,
	},
	{
		Version:     101,
		Description: "rewrite stored image-signing, registry-push and package-publish grants, repo_config.ci_capabilities and the capability policy included, to https://grain.dev/capabilities/release#<method>={} (the in-process providers are retired)",
		Up:          migrateReleaseGrants,
	},
	{
		Version:     102,
		Description: "add the hostd selector a repository's tasks start pinned to",
		Statements: []string{
			"ALTER TABLE `repo_config` ADD COLUMN `default_hostd` TEXT NOT NULL DEFAULT ''",
		},
	},
	{
		Version:     103,
		Description: "seed gcp#gemini-key on the gcp server where grain_config.gcp_project is set, minting with the gcp-key-minter secret, and rewrite stored gemini-key grants, repo_config.ci_capabilities and the capability policy included, to https://grain.dev/capabilities/gcp#gemini-key={} (the in-process gemini-key is retired)",
		Up:          migrateGeminiKeyToCapabilityServer,
	},
	{
		Version:     104,
		Description: "disable (and keep) every stored custom command connection: the connection type is removed",
		Up:          disableCustomConnections,
	},
	{
		Version:     105,
		Description: "add pending_access_request and access_decisions to task_observation for request_access",
		Statements: []string{
			"ALTER TABLE `task_observation` ADD COLUMN `pending_access_request` TEXT NULL",
			"ALTER TABLE `task_observation` ADD COLUMN `access_decisions` TEXT NULL",
		},
	},
	{
		Version:     106,
		Description: "add repo_config.upstream_repo for fork-to-upstream pull request workflows",
		Statements: []string{
			"ALTER TABLE `repo_config` ADD COLUMN `upstream_repo` TEXT NOT NULL DEFAULT ''",
		},
	},
	{
		Version:     107,
		Description: "add external_agent_token.owner, task.filed_via, task_comment.filed_via and grain_config.owner_trust (external agent owner attribution and owner-trust approval)",
		Statements: []string{
			"ALTER TABLE `external_agent_token` ADD COLUMN `owner` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `task` ADD COLUMN `filed_via` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `task_comment` ADD COLUMN `filed_via` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `grain_config` ADD COLUMN `owner_trust` TEXT NOT NULL DEFAULT ''",
		},
	},
	{
		Version:     108,
		Description: "add allowed_principal.reason, why an allowlist entry was added (required for serviceAccount: entries)",
		Statements: []string{
			"ALTER TABLE `allowed_principal` ADD COLUMN `reason` TEXT NOT NULL DEFAULT ''",
		},
	},
	{
		Version:     109,
		Description: "rewrite stored gcp#gcp-key grants from deprecated account and activate arguments to permission_set",
		Up:          MigrateGCPKeyGrantArgs,
	},
	{
		Version:     110,
		Description: "add allowed_principal.role, the scope set an allowlist entry's sessions are held to (existing entries become grain/operator)",
		Statements: []string{
			"ALTER TABLE `allowed_principal` ADD COLUMN `role` TEXT NOT NULL DEFAULT 'grain/operator'",
		},
	},
	{
		Version: 111,
		Description: "drop the task, template and repository hostd selectors: a model connection or capability server " +
			"chooses its hostd; task_run_hostd keeps where each run ran",
		Statements: []string{
			"DROP TABLE IF EXISTS `task_hostd`",
			"ALTER TABLE `template` DROP COLUMN `hostd`",
			"ALTER TABLE `repo_config` DROP COLUMN `default_hostd`",
		},
	},
	{
		Version:     112,
		Description: "add grain_config.auto_grant_context_reads (operator-configured repositories auto-granted -read for committed context)",
		Statements: []string{
			"ALTER TABLE `grain_config` ADD COLUMN `auto_grant_context_reads` TEXT NOT NULL DEFAULT ''",
		},
	},
	{
		Version:     113,
		Description: "add task.owner and task.proposer_*, backfill existing tasks, and enforce non-empty task owner",
		Statements: []string{
			"ALTER TABLE `task` ADD COLUMN `owner` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `task` ADD COLUMN `proposer_actor_kind` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `task` ADD COLUMN `proposer_actor_id` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `task` ADD COLUMN `proposer_actor_source` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `task` ADD COLUMN `proposer_behalf_kind` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `task` ADD COLUMN `proposer_behalf_id` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `task` ADD COLUMN `proposer_behalf_source` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `task` ADD COLUMN `proposer_task_id` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `task` ADD COLUMN `proposer_run_id` TEXT NOT NULL DEFAULT ''",
		},
		Up: MigrateTaskOwnerAndProposer,
	},
	{
		Version:     114,
		Description: "add task.external_ref and indexes for external-reference filtering and per-reference rollups",
		Statements: []string{
			"ALTER TABLE `task` ADD COLUMN `external_ref` TEXT NOT NULL DEFAULT ''",
			"CREATE INDEX IF NOT EXISTS `task_external_ref` ON `task` (`external_ref`, `id`)",
			"CREATE INDEX IF NOT EXISTS `github_ci_job_repo_ref` ON `github_ci_job` (`repo`, `ref`)",
			"CREATE INDEX IF NOT EXISTS `ci_run_repo_ref` ON `ci_run` (`repo`, `ref`, `started_at`)",
		},
	},
	{
		Version: 115,
		Description: "rewrite stored self-debug grants, repo_config.ci_capabilities and the capability policy's catalogue " +
			"included, to api:self-debug and https://grain.dev/capabilities/grain#source={} (the in-process capability is retired)",
		Up: migrateSelfDebugGrants,
	},
	{
		Version:     116,
		Description: "add grain_config.registry for connected-entry self-registration and heartbeat settings (design/design/hub.design.md)",
		Statements: []string{
			"ALTER TABLE `grain_config` ADD COLUMN `registry` TEXT NOT NULL DEFAULT ''",
		},
	},
	{
		Version:     117,
		Description: "add host enrollment codes, pending join requests, key-bound host metadata and 30-day credential expiry",
		Statements: []string{
			"ALTER TABLE `hostd` ADD COLUMN `public_key` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `hostd` ADD COLUMN `key_fingerprint` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `hostd` ADD COLUMN `enrollment_code` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `hostd` ADD COLUMN `capacity` INTEGER NOT NULL DEFAULT 0",
			"ALTER TABLE `hostd` ADD COLUMN `hostname` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `hostd` ADD COLUMN `os` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `hostd` ADD COLUMN `arch` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `hostd` ADD COLUMN `clis` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `hostd` ADD COLUMN `version` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `hostd` ADD COLUMN `local_user` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `hostd` ADD COLUMN `last_seen_at` DATETIME NULL",
			"ALTER TABLE `hostd_token` ADD COLUMN `expires_at` DATETIME NULL",
			hostEnrollmentCodeTable,
			hostJoinRequestTable,
		},
	},
	{
		Version:     118,
		Description: "rewrite stored bootstrap-playbooks grants, repo_config.ci_capabilities and the capability policy included, to https://grain.dev/capabilities/config-map#config-map={\"config_maps\":[\"bootstrap\"]} (the in-process bootstrap-playbooks is retired)",
		Up:          migrateBootstrapPlaybooksGrants,
	},
	{
		Version:     119,
		Description: "add allowed_principal.label and allowed_principal.last_used_at for delegate: entries (design/design/authentication.design.md §6)",
		Statements: []string{
			"ALTER TABLE `allowed_principal` ADD COLUMN `label` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `allowed_principal` ADD COLUMN `last_used_at` DATETIME NULL",
		},
	},
	{
		Version:     120,
		Description: "rewrite stored github-credential:<name> grants, repo_config.ci_capabilities and the capability policy included, to https://grain.dev/capabilities/repo#credential={\"credential\":\"<name>\"} (the in-process github-credential:<name> providers are retired)",
		Up:          migrateGitHubCredentialGrants,
	},
	{
		Version:     121,
		Description: "rewrite stored read-issues grants, repo_config.ci_capabilities and capability_policy included, to https://grain.dev/capabilities/github#read-issues={} (the in-process capability is retired)",
		Up:          migrateReadIssuesGrants,
	},
	{
		Version:     122,
		Description: "replace priority-dispatch and durable grants with the admission setting on tasks, templates, schedules and defaults",
		Statements: []string{
			"ALTER TABLE `task` ADD COLUMN `admission` TEXT NOT NULL DEFAULT 'standard'",
			"ALTER TABLE `template` ADD COLUMN `admission` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `schedule` ADD COLUMN `admission` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `grain_config` ADD COLUMN `default_admission` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `repo_config` ADD COLUMN `default_admission` TEXT NOT NULL DEFAULT ''",
		},
		Up: func(ctx context.Context, tx *sql.Tx) error {
			return MigrateAdmissionFromGrants(ctx, tx, false)
		},
	},
	{
		Version:     123,
		Description: "replace console grants with the hosting setting on tasks, templates, schedules, console sessions and defaults",
		Statements: []string{
			"ALTER TABLE `task` ADD COLUMN `hosting` TEXT NOT NULL DEFAULT 'unattended'",
			"ALTER TABLE `template` ADD COLUMN `hosting` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `schedule` ADD COLUMN `hosting` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `grain_config` ADD COLUMN `default_hosting` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `repo_config` ADD COLUMN `default_hosting` TEXT NOT NULL DEFAULT ''",
		},
		Up: func(ctx context.Context, tx *sql.Tx) error {
			return MigrateHostingFromGrants(ctx, tx, false)
		},
	},
	{
		Version:     124,
		Description: "rewrite stored github-sandbox grants, repo_config.ci_capabilities and capability_policy included, to https://grain.dev/capabilities/github#sandbox-repo={} (the in-process capability is retired)",
		Up:          migrateGitHubSandboxGrants,
	},
	{
		Version:     125,
		Description: "rewrite grain-ci in the capability policy's rules and catalogue defaults to https://grain.dev/capabilities/grain#ci={} (migration 96 rewrote every other stored grain-ci grant)",
		Up:          migrateGrainCIPolicy,
	},
	{
		Version:     126,
		Description: "add external_agent_token.owner_source, how the issuer who set a credential's owner was established (an owner named under the access code is not a verified on-behalf-of)",
		Statements: []string{
			"ALTER TABLE `external_agent_token` ADD COLUMN `owner_source` TEXT NOT NULL DEFAULT ''",
		},
	},
	{
		Version: 127,
		Description: "rewrite the gcp#gcp-key grants migration 109 kept as written (an \"account\" naming an unconfigured permission set) " +
			"to permission_set, so the state repository a store exports loads again",
		Up: MigrateGCPKeyGrantArgs,
	},
	{
		Version: 128,
		Description: "drop default, template and schedule grants that the deployment's capability catalogue disables, " +
			"noting affected templates and schedules so the state repository a store exports loads again",
		Up: MigrateCatalogueDisabledGrants,
	},
	{
		Version: 129,
		Description: "rewrite retired capability names in task_grant_wait (such as github-sandbox) " +
			"that earlier rewriteLegacyGrantsMulti migrations left under their retired names",
		Up: migrateRetiredGrantWaits,
	},
	{
		Version:     130,
		Description: "add task_run_usage, usage_capacity and usage_setting for per-run token accounting and connection budgets (tokenusage.go)",
		Statements: []string{
			taskRunUsageTable,
			taskRunUsageFinishedIndex,
			taskRunUsageTaskIndex,
			usageCapacityTable,
			usageSettingTable,
		},
	},
	{
		Version:     131,
		Description: "add schedule.grain_ci, a schedule whose workflow firing runs on grain CI even on a ci_mode: github repository",
		Statements: []string{
			"ALTER TABLE `schedule` ADD COLUMN `grain_ci` INTEGER NOT NULL DEFAULT 0",
		},
	},
	{
		Version: 132,
		Description: "rewrite every stored grant still spelled api:<what>[@repo] (or a non-canonical api reference), repo-read:<repo> or repo-write:<repo> " +
			"to https://grain.dev/capabilities/api#<what>[{repo}] and repo#read / #write {repo} across the grant tables, task_grant_wait, " +
			"default and CI capability lists and console sessions (unified-grants slice 4)",
		Up: migrateCanonicalGrants,
	},
	{
		Version:     133,
		Description: "add repository skill discovery configuration columns (skills on grain_config, repo_config, task and template, and declared_skills on repo_config)",
		Statements: []string{
			"ALTER TABLE `grain_config` ADD COLUMN `skills` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `repo_config` ADD COLUMN `skills` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `repo_config` ADD COLUMN `declared_skills` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `task` ADD COLUMN `skills` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `template` ADD COLUMN `skills` TEXT NOT NULL DEFAULT ''",
		},
	},
	{
		Version: 134,
		Description: "add schedule.overlap_group and scheduled_firing_skip.held_by_schedule_id, " +
			"an overlap group shared by one repository's schedules (grain/task-3895)",
		Statements: []string{
			"ALTER TABLE `schedule` ADD COLUMN `overlap_group` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `scheduled_firing_skip` ADD COLUMN `held_by_schedule_id` TEXT NOT NULL DEFAULT ''",
		},
	},
	{
		Version:     135,
		Description: "add grain_config.ci_api_repos, the repositories whose trusted CI jobs may hold api grants (grain/task-4040)",
		Statements: []string{
			"ALTER TABLE `grain_config` ADD COLUMN `ci_api_repos` TEXT NOT NULL DEFAULT ''",
		},
	},
	{
		Version: 136,
		Description: "add task_context and task_brief_edit, a durable task's compaction cursor, handover version " +
			"and current brief (grain/task-4107)",
		Statements: []string{
			taskContextTable,
			taskBriefEditTable,
			taskBriefEditTaskIndex,
		},
	},
	{
		Version:     137,
		Description: "add grain_config.hub_environment_name, the environment name a Grain Hub last supplied as this deployment's default (grain/task-4200)",
		Statements: []string{
			"ALTER TABLE `grain_config` ADD COLUMN `hub_environment_name` TEXT NOT NULL DEFAULT ''",
		},
	},
	{
		Version:     138,
		Description: "add grain_config.sandbox_shape_limits, the deployment and per-role ceilings on a task's sandbox shape (grain/task-4018)",
		Statements: []string{
			"ALTER TABLE `grain_config` ADD COLUMN `sandbox_shape_limits` TEXT NOT NULL DEFAULT ''",
		},
	},
	{
		Version: 139,
		Description: "add grain_config.auto_upgrade_min_interval_hours and auto_upgrade_release.succeeded_at, " +
			"the minimum interval between release-triggered upgrades (grain/task-4230)",
		Statements: []string{
			"ALTER TABLE `grain_config` ADD COLUMN `auto_upgrade_min_interval_hours` INTEGER NOT NULL DEFAULT 6",
			"ALTER TABLE `auto_upgrade_release` ADD COLUMN `succeeded_at` DATETIME NULL",
		},
	},
	{
		Version: 140,
		Description: "add integration_event's notification and host-script approval columns, for GET /api/events's " +
			"notification.* and local_script.* events (grain/task-4284)",
		// integration_event came in with no rung of its own, so a store
		// from before it has none to alter: create it first (as it now
		// stands), and every ADD COLUMN after passes over a column it
		// already has.
		Statements: []string{
			integrationEventTable,
			"ALTER TABLE `integration_event` ADD COLUMN `notification_id` INTEGER NOT NULL DEFAULT 0",
			"ALTER TABLE `integration_event` ADD COLUMN `subject` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `integration_event` ADD COLUMN `severity` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `integration_event` ADD COLUMN `summary` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `integration_event` ADD COLUMN `approval_id` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `integration_event` ADD COLUMN `script` TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE `integration_event` ADD COLUMN `decision` TEXT NOT NULL DEFAULT ''",
		},
	},
	{
		Version:     141,
		Description: "add schedule.skip_when, a condition such as held base-branch publishing that holds a schedule's firings (grain/task-4393)",
		Statements: []string{
			"ALTER TABLE `schedule` ADD COLUMN `skip_when` TEXT NOT NULL DEFAULT ''",
		},
	},
	{
		Version: 142,
		Description: "host-qualify the owner column of qualification_config, qualification_item, " +
			"qualification_item_depends_on and scheduled_workflow_run (StoredOwner) for rows a schedule " +
			"or a target repo places off the default host (grain/task-4335)",
		Up: migrateStoredOwners,
	},
	{
		Version:     143,
		Description: "add template.sandbox, the sandbox shape every task filed from a template asks for (grain/task-4591)",
		Statements: []string{
			"ALTER TABLE `template` ADD COLUMN `sandbox` TEXT NOT NULL DEFAULT ''",
		},
	},
	{
		Version: 144,
		Description: "take the copies of a template's admission, hosting and grants off the schedules that fire it, " +
			"leaving what each schedule adds to its template (grain/task-4607)",
		Up: migrateScheduleOwnSettings,
	},
	{
		Version: 145,
		Description: "host-qualify the owner column of release, release_candidate and qualification_run " +
			"(StoredOwner) for repositories a target repo places off the default host (grain/task-4489)",
		Up: migrateReleaseOwners,
	},
	{
		Version:     146,
		Description: "add grain_config.qa, the deployment-wide quality control engine and PR rollout configuration",
		Statements: []string{
			"ALTER TABLE `grain_config` ADD COLUMN `qa` TEXT NOT NULL DEFAULT ''",
		},
	},
}

// schema99AutoUpgradeReleaseTable is auto_upgrade_release as schema 99
// created it, frozen so migration 99 followed by migration 139's ALTER TABLE
// runs cleanly from any earlier baseline.
const schema99AutoUpgradeReleaseTable = `CREATE TABLE IF NOT EXISTS ` + "`auto_upgrade_release`" + ` (
  ` + "`id`" + `           INTEGER  NOT NULL,
  ` + "`newest`" + `       TEXT     NOT NULL DEFAULT '',
  ` + "`seen_at`" + `      DATETIME NOT NULL,
  ` + "`release_tag`" + `  TEXT     NOT NULL DEFAULT '',
  ` + "`target`" + `       TEXT     NOT NULL DEFAULT '',
  ` + "`scheduled_at`" + ` DATETIME NULL,
  ` + "`outcome`" + `      TEXT     NOT NULL DEFAULT '',
  PRIMARY KEY (` + "`id`" + `)
)`

// schema92HostdTable and schema92HostdTokenTable are hostd and hostd_token as
// schema 92 created them, frozen so migration 92 followed by migration 117's
// ALTER TABLE statements runs cleanly from any earlier baseline.
const schema92HostdTable = `CREATE TABLE IF NOT EXISTS ` + "`hostd`" + ` (
  ` + "`name`" + `       TEXT     NOT NULL,
  ` + "`owner`" + `      TEXT     NOT NULL DEFAULT '',
  ` + "`labels`" + `     TEXT     NOT NULL DEFAULT '',
  ` + "`created_by`" + ` TEXT     NOT NULL DEFAULT '',
  ` + "`created_at`" + ` DATETIME NOT NULL,
  ` + "`revoked_at`" + ` DATETIME NULL,
  PRIMARY KEY (` + "`name`" + `)
)`

const schema92HostdTokenTable = `CREATE TABLE IF NOT EXISTS ` + "`hostd_token`" + ` (
  ` + "`token_hash`" + ` TEXT     NOT NULL,
  ` + "`hostd`" + `      TEXT     NOT NULL,
  ` + "`prefix`" + `     TEXT     NOT NULL DEFAULT '',
  ` + "`created_at`" + ` DATETIME NOT NULL,
  ` + "`revoked_at`" + ` DATETIME NULL,
  PRIMARY KEY (` + "`token_hash`" + `)
)`

// maxMergesPerRepoDefault1 is max_merges_per_repo's column definition as
// schemas before 95 wrote it, with the default captured for replacing.
var maxMergesPerRepoDefault1 = regexp.MustCompile("(`max_merges_per_repo`\\s+INTEGER\\s+NOT\\s+NULL\\s+DEFAULT\\s+)1\\b")

// grainConfigTableName is the table name at the head of grain_config's
// stored CREATE statement, however it was quoted.
var grainConfigTableName = regexp.MustCompile("^(\\s*CREATE\\s+TABLE\\s+(?:IF\\s+NOT\\s+EXISTS\\s+)?)[`\"]?grain_config[`\"]?")

// raiseMaxMergesPerRepoDefault is migration 95. Only the column default
// changes, never a row: a deployment whose row holds 1 keeps 1, because the
// row is written whole on every save and so cannot say whether that 1 was
// chosen.
//
// Postgres changes a default in place. SQLite cannot, so there the table is
// rebuilt from its own stored CREATE statement with only the default
// edited -- the columns, their order and every row as they were. The two
// are told apart by that statement: sqlite_master is a compatibility view
// on Postgres whose sql column is always empty.
func raiseMaxMergesPerRepoDefault(ctx context.Context, tx *sql.Tx) error {
	var ddl string
	if err := tx.QueryRowContext(ctx,
		"SELECT `sql` FROM `sqlite_master` WHERE `type` = 'table' AND `name` = 'grain_config'").Scan(&ddl); err != nil {
		return fmt.Errorf("reading grain_config's definition: %w", err)
	}
	if ddl == "" {
		_, err := tx.ExecContext(ctx, "ALTER TABLE `grain_config` ALTER COLUMN `max_merges_per_repo` SET DEFAULT 5")
		return err
	}
	raised := maxMergesPerRepoDefault1.ReplaceAllString(ddl, "${1}5")
	if raised == ddl {
		return nil
	}
	if !grainConfigTableName.MatchString(raised) {
		return fmt.Errorf("grain_config's stored definition does not start as a CREATE TABLE: %.60q", ddl)
	}
	raised = grainConfigTableName.ReplaceAllString(raised, "${1}`grain_config_95`")
	for _, stmt := range []string{
		"DROP TABLE IF EXISTS `grain_config_95`",
		raised,
		"INSERT INTO `grain_config_95` SELECT * FROM `grain_config`",
		"DROP TABLE `grain_config`",
		"ALTER TABLE `grain_config_95` RENAME TO `grain_config`",
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

// principalSourceColumns are migration 91's ALTERs: `<p>_source` beside
// every stored principal, `”` for every row already there.
func principalSourceColumns() []string {
	cols := []struct{ table, column string }{
		{"task", "origin_actor_source"},
		{"task", "origin_behalf_source"},
		{"task", "approval_actor_source"},
		{"task", "approval_behalf_source"},
		{"task_comment", "author_source"},
		{"task_comment", "author_behalf_source"},
		{"task_pause", "paused_by_source"},
		{"pipeline_pause", "actor_source"},
		{"scheduled_upgrade", "actor_source"},
		{"memory", "updated_by_source"},
		{"memory_document", "updated_by_source"},
		{"release_note", "updated_by_source"},
		{"task_event", "actor_source"},
		{"task_event", "behalf_kind"},
		{"task_event", "behalf_id"},
		{"task_event", "behalf_source"},
	}
	out := make([]string, 0, len(cols))
	for _, c := range cols {
		out = append(out, "ALTER TABLE `"+c.table+"` ADD COLUMN `"+c.column+"` TEXT NOT NULL DEFAULT ''")
	}
	return out
}

// schema92CapabilityAuditTable is task_run_capability_audit as schema
// 92 created it, frozen for migration 93.
const schema92CapabilityAuditTable = `CREATE TABLE IF NOT EXISTS ` + "`task_run_capability_audit`" + ` (
  ` + "`run_id`" + `     TEXT     NOT NULL,
  ` + "`seq`" + `        INTEGER  NOT NULL,
  ` + "`capability`" + ` TEXT     NOT NULL,
  ` + "`attempt_id`" + ` TEXT     NOT NULL,
  ` + "`at`" + `         DATETIME NOT NULL,
  ` + "`record`" + `     TEXT     NOT NULL DEFAULT '',
  ` + "`note`" + `       TEXT     NOT NULL DEFAULT '',
  PRIMARY KEY (` + "`run_id`" + `, ` + "`seq`" + `)
)`

// schema90TaskPauseTable and schema90TaskEventTable are task_pause and
// task_event as schema 90 created them, frozen for migration 91.
const schema90TaskPauseTable = `CREATE TABLE IF NOT EXISTS ` + "`task_pause`" + ` (
  ` + "`task_id`" + `           TEXT     NOT NULL,
  ` + "`paused_at`" + `         DATETIME NOT NULL,
  ` + "`paused_by_kind`" + `    TEXT     NOT NULL,
  ` + "`paused_by_id`" + `      TEXT     NOT NULL,
  ` + "`from_state`" + `        TEXT     NOT NULL,
  ` + "`immediate`" + `         INTEGER  NOT NULL DEFAULT 0,
  ` + "`grace_until`" + `       DATETIME NULL,
  ` + "`run_id`" + `            TEXT     NULL,
  PRIMARY KEY (` + "`task_id`" + `)
)`

const schema90TaskEventTable = `CREATE TABLE IF NOT EXISTS ` + "`task_event`" + ` (
  ` + "`id`" + `         INTEGER PRIMARY KEY AUTOINCREMENT,
  ` + "`task_id`" + `    TEXT     NOT NULL,
  ` + "`kind`" + `       TEXT     NOT NULL,
  ` + "`at`" + `         DATETIME NOT NULL,
  ` + "`actor_kind`" + ` TEXT     NOT NULL DEFAULT '',
  ` + "`actor_id`" + `   TEXT     NOT NULL DEFAULT '',
  ` + "`reason`" + `     TEXT     NOT NULL DEFAULT '',
  ` + "`detail`" + `     TEXT     NOT NULL DEFAULT ''
)`

// CorpResearchGrant is what migration 86 rewrote stored corp-research grants
// to: the method a wrapper distribution's build registers (capability.Register) in place
// of its in-process corp-research provider, and makes corp-research an alias
// of. Core serves no such method; it names the grant only because the
// migration list is core's, and a store migrated by a build without the
// method holds a grant nothing there resolves, as the bare corp-research it
// replaced resolved to no provider there either.
const CorpResearchGrant = "https://grain.dev/capabilities/corp#research={}"

// ApplyDataMigrations runs the row rewrites (Up) of every migration past
// from, inside tx, on tables already at SchemaVersion's shape. It is for
// rows that arrive by another way than the store they were written in --
// a state repository dump stamped from, imported into a store at this
// build's schema -- which an in-place migration never sees: without it a
// dump from before the capability retirements would keep the retired
// names in grain_config's and repo_config's capability lists and in
// the standing console sessions. Every Up is idempotent, so a row that is
// already canonical is left as it is.
func ApplyDataMigrations(ctx context.Context, tx *sql.Tx, from int) error {
	for v := from + 1; v <= SchemaVersion; v++ {
		m, ok := migrationFor(v)
		if !ok || m.Up == nil {
			continue
		}
		if err := m.Up(ctx, tx); err != nil {
			return fmt.Errorf("applying schema %d's rewrite to rows from schema %d: %w", v, from, err)
		}
	}
	return nil
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
			if err := execMigrationStatement(ctx, tx, stmt); err != nil {
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

// execMigrationStatement runs one of a migration's Statements, passing over
// an ADD COLUMN whose column the store already has, and a DROP COLUMN
// whose column it does not. That statement runs
// under a savepoint: Postgres aborts the whole transaction on any failed
// statement (SQLSTATE 25P02 on everything after it), so without rolling
// back to the savepoint the error it passes over would fail the next
// statement instead, under that statement's name.
func execMigrationStatement(ctx context.Context, tx *sql.Tx, stmt string) error {
	upper := strings.ToUpper(stmt)
	adding, dropping := strings.Contains(upper, " ADD COLUMN "), strings.Contains(upper, " DROP COLUMN ")
	if !adding && !dropping {
		_, err := tx.ExecContext(ctx, stmt)
		return err
	}
	if _, err := tx.ExecContext(ctx, "SAVEPOINT grain_add_column"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, stmt); err != nil {
		msg := strings.ToLower(err.Error())
		// The same for a DROP COLUMN whose column the store never had: a
		// store built at a schema before the column was added.
		passOver := adding && (strings.Contains(msg, "duplicate column name:") || strings.Contains(msg, "already exists"))
		passOver = passOver || dropping && (strings.Contains(msg, "no such column") || strings.Contains(msg, "does not exist"))
		if !passOver {
			return err
		}
		_, err := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT grain_add_column")
		return err
	}
	_, err := tx.ExecContext(ctx, "RELEASE SAVEPOINT grain_add_column")
	return err
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
