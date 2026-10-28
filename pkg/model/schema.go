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

import "strconv"

// The model as DDL, and the derivations as views.
//
// Two things here are not merely a translation of task.go, and both are
// why a schema'd store earns its place:
//
// task_state is a view. TaskState is derived from approval plus grain's
// own observations and never written, so as a view there is no column to
// write: no finish path can write one, no migration can add one by
// accident, and "a task is in exactly one state" stops being a rule each
// code path upholds and becomes a property of the store.
//
// Declaration and observation are separate tables, not columns on one
// row. They answer to different records — a human authors the first,
// grain writes the second.
//
// On naming: every table is backtick-quoted. SQLite accepts backticks as
// a MySQL-compatibility identifier quote, which is what lets this SQL
// read the same regardless of which engine is behind Store -- no
// identifier here needs it against SQLite's own much smaller reserved
// word list, but keeping the quoting uniform means nothing has to
// remember which columns happen to need it.
//
// On types: enums are TEXT rather than a constrained type. Keeping the
// vocabulary in task.go rather than the schema is what lets a new
// LinkKind ship without a migration; the cost is that the database will
// not reject an unknown value, which the Valid methods and their own
// tests cover instead. Timestamps keep DATETIME rather than MySQL's DATETIME(6):
// SQLite has no timestamp storage class of its own, and this is what
// tells modernc.org/sqlite (the driver pkg/model/sqlite opens) to hand a
// column back as a time.Time rather than the TEXT it stores under the
// hood -- store.go's sql.NullTime scanning and time.Time binding are
// otherwise unchanged from what they were against Dolt. Booleans are
// INTEGER (0/1) -- SQLite has no separate boolean storage class either,
// and database/sql converts either direction without help from this
// package (pinned by pkg/model/sqlite's own store tests).

// SchemaVersion is bumped whenever Tables or Views change in a way an
// existing database cannot simply be re-created into. Init records it,
// migrates a store from 2.0.0 or later forward in place, and refuses any
// other stamp, rather than failing later with a confusing missing column.
//
// The policy from grain 2.0.0 on (docs/data-model.md, "Schema changes
// after 2.0"):
//
//   - V2BaselineSchemaVersion is the schema 2.0.0 shipped. Its DDL is
//     frozen in pkg/model/v2baseline, and Tables and Views may differ
//     from it only by what Migrations add.
//   - Every bump past it ships an in-place Migration (migrations.go) from
//     the version before it, which runs on SQLite and on Postgres alike.
//     A store a 2.0 deployment wrote opens in every later build.
//   - Refusing the previous store and carrying its rows across through a
//     state-repository export and import -- how 20 to 27 below were made
//     -- is no longer an acceptable way to bump the schema. The state
//     repository's dump must still import (a bump the importer does not
//     absorb on its own brings a dumpFixups step, pkg/staterepo), but it
//     is a backup and a restore, not the upgrade path.
//   - The startup guard goes on refusing a store or dump from before the
//     single-tenant reset (BaselineSchemaVersion) in its own words.
//
// pkg/model/v2baseline's tests enforce it: the frozen DDL cannot change,
// a bump with no Migration fails, and a fixture store and a state-
// repository dump made at the 2.0 baseline have to open, rows intact, in
// this build, on both engines.
//
// 20 drops the `user` table, which only a multi-tenant build's sign-in
// accounts ever filled (stage 6 of docs/single-tenant-reset.md). A store
// at 19 is refused as old and its rows come across through the state
// repository, whose import passes over a table this build lacks; a dump
// that holds a `user` row is refused in the startup guard's words
// instead (pkg/staterepo/retired.go).
//
// 21 drops `team_user` and `team_sequence`. Nothing has read or written
// a team member since 3e-iv-b2c-iii, nor allocated a team ID since 3e-iv,
// so only the startup guard still read them; its question about another
// team's members is asked of a dump instead (retired.go), as 20's was.
//
// 22 moves the deployment's git connections (git_connections,
// git_credential), pinned connection (agent_backend_id) and per-model
// settings (model_settings) off the default team's row onto grain_config,
// and folds that row's default_model into grain_config.default_model. A
// column moving between tables is not something the importer absorbs,
// so the state repository's dumpFixups carries a dump stamped 21 or
// below across (pkg/staterepo/migrate.go).
//
// 23 folds the default team's per-repository settings (team_repo_config)
// into repo_config, which gains the one column it lacked, git_credential,
// and moves the deployment's GCP keys from team_gcp_key to gcp_key, keyed
// by name alone. Rows moving between tables, so dumpFixups carries a dump
// stamped 22 or below across as well; another team's row in either old
// table refuses the dump (retired.go).
//
// 24 drops `team`. What the default team's row still held that anything
// acted on moves to grain_config: agent_framework, agent_git_name,
// agent_git_email and target_repos as columns of the same names, its
// repair_template_id and merge_batch_size over grain_config's own (the
// team's won wherever it chose one), and its legacy per-framework models
// folded into default_model and model_settings, as dispatch folded them.
// dumpFixups carries a dump stamped 23 or below across; a team besides
// the default one refuses the dump (retired.go), as the guard's count of
// them refused a store before.
//
// 25 drops the team columns: board, ci_run, custom_field, executor_token,
// memory, memory_document, schedule, suite, suite_run, task and
// template's `team`, and the two audit tables' `team_id`, with the
// indexes over them. Every row a store at 19 or later holds was written
// under the default team (or, for the deployment's own memory, none), so
// nothing read them, and memory, memory_document and custom_field are
// keyed without them. A pure drop: the importer passes over a column
// this build lacks, so no dumpFixups step carries a dump across.
//
// 26 drops grain_config's `admin_accounts`. Whoever holds the access
// code is the deployment's owner (docs/access-code.md), so there is no
// list of administrators left to keep. A pure drop, as 25 was: the
// importer passes over the column in an older dump, and `grain state
// check` says it would be ignored.
//
// 27 drops repo_config's `admins`. Every person is the deployment's
// owner and repository settings ask only whether the caller is a person
// (checkRepoPerson), so a per-repository list of administrators granted
// nothing. A pure drop, as 26 was.
//
// 28 adds Kubernetes sandbox configuration columns (`k8s_namespace`,
// `k8s_kubectl`, `k8s_sandbox_image`, `k8s_granule_image`,
// `k8s_workspace`, `k8s_git_proxy_host`, `k8s_sandbox_service`,
// `k8s_service_account`, `k8s_node_selector`, `k8s_privileged`,
// `k8s_network_policy`, `k8s_deployment`, `k8s_container`) to
// `grain_config`.
//
// 29 adds `grain_config`.`mcp_servers`: the capability servers this
// deployment attaches, keyed by capability_id, as a JSON mcpServers
// object (model.Config.MCPServers, docs/capabilities-spec.md §3.1).
//
// 30 adds `grain_config`.`capability_policy`: which repositories, run
// kinds and events may hold which capability reference
// (model.Config.CapabilityPolicy, docs/capabilities-spec.md §4.2.2).
//
// 31 adds `grain_config`.`cli_run`: where each agent CLI runs -- on the
// host, in a container of the release image, or whichever of the two the
// host allows (model.Config.CLIRun, docs/design/cli-run-location.md).
//
// 32 adds `task_observation`.`merge_queue_cancel_handoff_at` and
// `merge_queue_cancel_handoff_run`: the grain CI run a person cancelled
// that the merge queue has handed back to the task's agent, so each
// cancellation is handed back once (Observation.MergeQueueCancelHandoffRun).
//
// 33 adds `schedule`.`every_n_minutes`: the interval of the
// everyNMinutes recurrence (model.RecurrenceEveryNMinutes), NULL on
// every row of any other kind -- which is every row a store at 32 holds.
//
// 34 adds `persistent_sandbox`.`refresh` and `rollback_on_crash`: when a
// persistent sandbox moves itself to a newer commit of its ref, and
// whether an after_green one goes back to the last green commit when the
// new one crash-loops (model.PersistentSandbox.Refresh).
//
// 35 adds `grain_config`.`sandbox_overcommit`: whether the sandbox
// budget's overcommit ratios are fixed or tuned by the auto overcommit
// controller, and that controller's bounds and pressure targets
// (model.Config.SandboxOvercommit, model.OvercommitSettings).
//
// 36 adds `repo_config`.`merge_on_green`: whether the merge queue merges
// a green, clean pull request in that repository without waiting behind
// entries whose CI is still running (model.RepoConfig.MergeOnGreen).
//
// 37 adds `repo_config`.`base_repair_bypasses_human_gates`: whether a
// base-branch repair pull request in that repository may merge without a
// human on green CI while the base branch is red, bypassing `human: required`
// sensitive path triggers and trust-boundary auto-merge withdrawal
// (model.RepoConfig.BaseRepairBypassesHumanGates).
//
// 38 adds `template`.`interactive`: whether every task filed from a
// template is a live chat (model.Template.Interactive).
//
// 39 adds `merge_batch_history`.`reason`: why an abandoned merge batch
// produced no answer (model.MergeBatchRecord.Reason), and the
// `base_hold_history` table, how many changes each red stretch of a base
// branch held in base_broken (model.BaseHoldPass).
//
// 40 adds `task_observation`.`merge_queue_reason`: the sub-case of the
// merge-queue state an entry is held in, so a row can say which of that
// state's ways of waiting it is (Observation.MergeQueueReason,
// orchestrator's livenessreasons.go).
//
// 41 adds `repo_config`.`merge_recheck`: how the merge queue's overlap
// re-check is applied in that repository -- off, observe or enforce
// (model.RepoConfig.MergeRecheck, grain/task-985).
//
// 42 adds `grain_config`.`base_ci_red_streak`, how many consecutive
// judged tips of a base branch failing the same job confirm it red
// (model.Config.BaseCIRedStreak), and the streak itself on
// `base_branch_ci`: `red_streak`, `red_streak_jobs`, `red_streak_since`,
// `red_streak_tip`, `last_green_commit` and `red_streak_woken` (model.BaseBranchCI.RedStreak).
//
// 43 adds `task_run`.`run_now_at`: the Run now request a run spent when
// it started, kept so a run that ends without doing the work can put the
// request back (Store.FinishRun, RunNowSurvives).
//
// 44 replaces `grain_config`.`sandbox_setup_script` with
// `grain_config`.`sandbox_image`: a custom sandbox image is now an OCI
// reference built FROM guest-base or k8s-sandbox-base
// (model.Config.SandboxImage), not a script the daemon runs at dispatch.
//
// 45 adds the suspension record (docs/design/durable-tasks.md#DT-2):
// `task_suspension`, and on `task_run` its `suspend_kind` (backfilled
// for the four outcomes that were suspensions before the record existed)
// and `standing`, the model.DispatchStanding snapshot startRun takes.
//
// 46 adds `grain_config`.`k8s_priority_class_interactive`,
// `k8s_priority_class_agent` and `k8s_priority_class_ci`: the
// PriorityClass a Kubernetes sandbox pod of each kind runs at
// (model.Config.K8sPriorityClassInteractive and its two siblings),
// defaulting to the three deploy/kubernetes/ ships so a deployment that
// upgrades gets them; an empty name is no priority class at all.
//
// 47 adds `task_run`.`model` and `task_run`.`agent_framework`: the model
// and agent framework a run actually ran on, as dispatch resolved them,
// so an attempt can say which of a rotation's models it was given
// (Run.Model, Store.SetRunModel).
//
// 48 adds `grain_config`.`sandbox_disk_overcommit`: how many times over
// the sandbox volume (or the cluster's allocatable ephemeral storage)
// sandbox disks may be promised (model.Config.SandboxDiskOvercommit).
//
// 49 adds `grain_config`.`auto_upgrade_channel` and `auto_upgrade_time`,
// the branch a deployment upgrades itself onto each day and when
// (model.Config.AutoUpgradeChannel), and the `auto_upgrade_run` table,
// the last day that schedule fired (model.AutoUpgradeRun).
//
// 50 adds `task`.`resume_on_deadline` (Task.ResumeOnDeadline), and
// backfills `task_run`.`suspend_kind` = 'requested' for parked runs, which
// are now the agent's own suspension (docs/design/durable-tasks.md#DT-9).
//
// 51 adds `ci_job`.`attempts`: each CI job's per-attempt logs and metadata
// across automatic job retries (model.CIJob.Attempts).
//
// 52 adds the `task_goal` table, each task's daemon-checked goal and the
// progress it has seen (model.TaskGoal, docs/design/durable-tasks.md#DT-4),
// and `template`.`goal`, the goal a template files its tasks with.
//
// 53 adds the `usage_limit_pause` table: the usage-limit pauses in force
// (model.StoredUsageLimitPause), so they survive a daemon restart.
//
// 54 adds `grain_config`.`judgement_model`, the model the symbolic
// `judgement` model resolves to (model.Config.JudgementModel,
// docs/design/durable-tasks.md#DT-11).
//
// 55 adds the `api_scope_set` table: a deployment's own named sets of
// agent API scopes (model.APIScopeSet, docs/agent-api.md).
//
// 56 adds the `github_ci_job` table: GitHub Actions jobs and other check
// runs grain saw conclude on the repositories it works in
// (model.GitHubCIJob, docs/github-ci.md).
//
// 57 adds `task_comment`.`summary`: the one line a question an agent
// asked is shown by in the inbox, the task list and notifications
// (model.Comment.Summary).
//
// 58 adds `change_target_kind` and `change_target_spec` to `task` and
// `template`: a task whose change is not a git branch, such as a changelist in a
// workspace of a registered kind (Task.ChangeTarget).
const SchemaVersion = 58

// gitHubCIJobTable is the `github_ci_job` table, named once for Tables and
// for the migration that adds it.
const gitHubCIJobTable = `CREATE TABLE IF NOT EXISTS ` + "`github_ci_job`" + ` (
  ` + "`repo`" + `          TEXT     NOT NULL,
  ` + "`check_run_id`" + `  INTEGER  NOT NULL,
  ` + "`source`" + `        TEXT     NOT NULL DEFAULT 'github',
  ` + "`app`" + `           TEXT     NOT NULL DEFAULT '',
  ` + "`ref`" + `           TEXT     NOT NULL DEFAULT '',
  ` + "`commit_sha`" + `    TEXT     NOT NULL DEFAULT '',
  ` + "`workflow`" + `      TEXT     NOT NULL DEFAULT '',
  ` + "`run_id`" + `        INTEGER  NOT NULL DEFAULT 0,
  ` + "`run_attempt`" + `   INTEGER  NOT NULL DEFAULT 0,
  ` + "`job_name`" + `      TEXT     NOT NULL DEFAULT '',
  ` + "`conclusion`" + `    TEXT     NOT NULL DEFAULT '',
  ` + "`started_at`" + `    DATETIME NULL,
  ` + "`completed_at`" + `  DATETIME NULL,
  ` + "`failing_step`" + `  TEXT     NOT NULL DEFAULT '',
  ` + "`run_url`" + `       TEXT     NOT NULL DEFAULT '',
  ` + "`job_url`" + `       TEXT     NOT NULL DEFAULT '',
  ` + "`output`" + `        TEXT     NOT NULL DEFAULT '',
  ` + "`failure_tail`" + `  TEXT     NOT NULL DEFAULT '',
  ` + "`tail_source`" + `   TEXT     NOT NULL DEFAULT '',
  ` + "`tail_fetched`" + `  INTEGER  NOT NULL DEFAULT 0,
  ` + "`via`" + `           TEXT     NOT NULL DEFAULT '',
  ` + "`recorded_at`" + `   DATETIME NOT NULL,
  ` + "`updated_at`" + `    DATETIME NOT NULL,
  PRIMARY KEY (` + "`repo`" + `, ` + "`check_run_id`" + `)
)`

// usageLimitPauseTable is the `usage_limit_pause` table, named once for
// Tables and for the migration that adds it.
const usageLimitPauseTable = `CREATE TABLE IF NOT EXISTS ` + "`usage_limit_pause`" + ` (
  ` + "`scope_key`" + `     TEXT     NOT NULL,
  ` + "`connection_id`" + ` TEXT     NOT NULL DEFAULT '',
  ` + "`model`" + `         TEXT     NOT NULL DEFAULT '',
  ` + "`labels`" + `        TEXT     NOT NULL DEFAULT '',
  ` + "`team`" + `          TEXT     NOT NULL DEFAULT '',
  ` + "`framework`" + `     TEXT     NOT NULL DEFAULT '',
  ` + "`until_at`" + `      DATETIME NOT NULL,
  ` + "`reason`" + `        TEXT     NOT NULL DEFAULT '',
  ` + "`escalated`" + `     TEXT     NOT NULL DEFAULT '',
  ` + "`updated_at`" + `    DATETIME NOT NULL,
  PRIMARY KEY (` + "`scope_key`" + `)
)`

// BaselineSchemaVersion is the schema the single-tenant reset started
// over from (docs/data-model.md): Tables and Views as they stand here,
// created whole into an empty store, with none of the migrations that
// carried a store forward from earlier builds. Every version below it
// was written by a build that could serve several tenants, and there is
// no upgrade path from one: the startup guard refuses a store or a
// state-repository dump stamped below it (CheckSchemaBaseline).
//
// It never changes. The in-place migrations later builds carry start at
// V2BaselineSchemaVersion, not here.
const BaselineSchemaVersion = 19

// V2BaselineSchemaVersion is the schema grain 2.0.0 shipped: Tables and
// Views as they stood after the single-tenant reset's last stage, frozen
// in pkg/model/v2baseline. A store stamped with it or anything later is
// migrated forward in place (migrations.go). A store between
// BaselineSchemaVersion and this was written by a pre-release build of
// the reset and is still carried across through the state repository
// (ErrSchemaTooOld's `grain state prepare-upgrade`); nothing below
// BaselineSchemaVersion is carried at all.
//
// It never changes. It is a second constant rather than a new value for
// BaselineSchemaVersion because the two say different things: that one
// is the startup guard's floor, whose refusal says "from before the
// single-tenant reset", which is not true of a store at 20 to 26.
const V2BaselineSchemaVersion = 27

// Tables is the DDL, in dependency order.
var Tables = []string{
	// The durable identity of this grain installation. Task numbers are
	// only unique inside one store, and the state repository and target
	// repositories can both be shared by several installations. This
	// random value gives callers a stable namespace for values that leave
	// the store, without making the operator choose or maintain one.
	//
	// There is exactly one row. The fixed singleton key keeps that
	// invariant structural while leaving the generated value opaque.
	`CREATE TABLE IF NOT EXISTS ` + "`grain_instance`" + ` (
  ` + "`singleton`" + ` INTEGER NOT NULL CHECK (` + "`singleton`" + ` = 1),
  ` + "`id`" + `        TEXT    NOT NULL,
  PRIMARY KEY (` + "`singleton`" + `),
  UNIQUE (` + "`id`" + `)
)`,

	`CREATE TABLE IF NOT EXISTS ` + "`task`" + ` (
  ` + "`id`" + `                    TEXT    NOT NULL,
  ` + "`branch`" + `                TEXT    NOT NULL DEFAULT '',
  ` + "`intent`" + `                TEXT    NOT NULL,
  ` + "`title`" + `                 TEXT    NOT NULL,
  ` + "`body`" + `                  TEXT    NOT NULL,

  ` + "`origin_actor_kind`" + `     TEXT    NOT NULL,
  ` + "`origin_actor_id`" + `       TEXT    NOT NULL,
  ` + "`origin_behalf_kind`" + `    TEXT    NULL,
  ` + "`origin_behalf_id`" + `      TEXT    NULL,
  ` + "`origin_reason`" + `         TEXT    NOT NULL,

  ` + "`approval_actor_kind`" + `   TEXT    NULL,
  ` + "`approval_actor_id`" + `     TEXT    NULL,
  ` + "`approval_behalf_kind`" + `  TEXT    NULL,
  ` + "`approval_behalf_id`" + `    TEXT    NULL,
  ` + "`approved_at`" + `           DATETIME NULL,
  ` + "`deferred_at`" + `           DATETIME NULL,
  ` + "`deferred_until`" + `        DATETIME NULL,
  ` + "`undeferred_at`" + `         DATETIME NULL,

  ` + "`target_owner`" + `          TEXT    NULL,
  ` + "`target_name`" + `           TEXT    NULL,
  ` + "`binding`" + `               TEXT    NOT NULL,
  ` + "`base`" + `                  TEXT    NULL,
  ` + "`folder`" + `                TEXT    NULL,

  ` + "`auto_merge`" + `            INTEGER  NOT NULL,
  ` + "`auto_approve_proposals`" + ` INTEGER NOT NULL DEFAULT 0,
  ` + "`created_at`" + `            DATETIME NULL,
  ` + "`order_key`" + `             REAL     NOT NULL DEFAULT 0,
  ` + "`sandbox_cpus`" + `          INTEGER  NOT NULL DEFAULT 0,
  ` + "`sandbox_memory_mb`" + `     INTEGER  NOT NULL DEFAULT 0,
  ` + "`sandbox_disk_gb`" + `       INTEGER  NOT NULL DEFAULT 0,
  ` + "`interactive`" + `           INTEGER  NOT NULL DEFAULT 0,
  ` + "`agent_framework`" + `       TEXT     NOT NULL DEFAULT '',
  ` + "`agent_backend_id`" + `      TEXT     NOT NULL DEFAULT '',
  ` + "`agent_model`" + `           TEXT     NOT NULL DEFAULT '',
  ` + "`prompt_extension`" + `      TEXT     NOT NULL DEFAULT '',
  ` + "`review_template_id`" + `    TEXT     NOT NULL DEFAULT '',
  ` + "`network_access`" + `        TEXT     NOT NULL DEFAULT '',
  ` + "`nothing_needed_action`" + ` TEXT     NOT NULL DEFAULT '',
  ` + "`creator`" + `               TEXT     NOT NULL DEFAULT '',
  ` + "`time_zone`" + `             TEXT     NOT NULL DEFAULT '',
  ` + "`issue`" + `                 INTEGER  NOT NULL DEFAULT 0,
  ` + "`no_port`" + `               INTEGER  NOT NULL DEFAULT 0,
  ` + "`agent_model_source`" + `    TEXT     NOT NULL DEFAULT '',
  ` + "`origin_ref`" + `            TEXT     NOT NULL DEFAULT '',
  ` + "`resume_on_deadline`" + `    INTEGER  NOT NULL DEFAULT 0,
  ` + "`change_target_kind`" + `    TEXT     NOT NULL DEFAULT '',
  ` + "`change_target_spec`" + `    TEXT     NOT NULL DEFAULT '',
  PRIMARY KEY (` + "`id`" + `)
)`,

	// The one progress comment grain keeps on a GitHub origin -- the pull
	// request or issue a tree of tasks came from (model.WorkOrigin) --
	// and what it last said, so it is edited in place rather than
	// posted again, and only when what it says has changed.
	`CREATE TABLE IF NOT EXISTS ` + "`work_origin_comment`" + ` (
  ` + "`origin`" + `     TEXT     NOT NULL,
  ` + "`comment_id`" + ` INTEGER  NOT NULL DEFAULT 0,
  ` + "`body`" + `       TEXT     NOT NULL DEFAULT '',
  ` + "`checked_at`" + ` DATETIME NULL,
  PRIMARY KEY (` + "`origin`" + `)
)`,

	// Read targets get a table rather than a JSON column: "which tasks
	// read this repo?" is worth being able to ask, and keeping reads
	// structurally distinct from task.target is what stops a read target
	// becoming a capability-laundering channel by accident.
	`CREATE TABLE IF NOT EXISTS ` + "`task_read`" + ` (
  ` + "`task_id`" + ` TEXT NOT NULL,
  ` + "`owner`" + `   TEXT NOT NULL,
  ` + "`name`" + `    TEXT NOT NULL,
  PRIMARY KEY (` + "`task_id`" + `, ` + "`owner`" + `, ` + "`name`" + `)
)`,

	// The extra repositories a task may have work landed in, one row each,
	// with the base branch for that repository (model.WriteTarget). A
	// table for task_read's own reason -- "which tasks may write this
	// repo?" is worth being able to ask -- and a separate table from
	// task_read rather than a flag on it, because the two answer
	// different questions and only one of them decides where a proposal
	// is filed and whether it inherits auto-merge.
	//
	// The write target the task's own runs push to is not here: that is
	// task.target_owner/target_name and task.base, where it has always
	// been, and duplicating it would give the same fact two places to
	// disagree.
	//
	// Needs no SchemaVersion bump and no rung on store.go's migration
	// ladder, for the reason task_run_tool's own comment below gives:
	// Init's CREATE TABLE IF NOT EXISTS creates a missing table on an
	// existing database perfectly well. An older store gains one empty
	// table, which reads back as every task having no extra write
	// targets -- exactly what those tasks were filed as.
	`CREATE TABLE IF NOT EXISTS ` + "`task_write`" + ` (
  ` + "`task_id`" + ` TEXT NOT NULL,
  ` + "`owner`" + `   TEXT NOT NULL,
  ` + "`name`" + `    TEXT NOT NULL,
  ` + "`base`" + `    TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (` + "`task_id`" + `, ` + "`owner`" + `, ` + "`name`" + `, ` + "`base`" + `)
)`,

	// The template a task was filed from (Task.TemplateID, grain/task-2536),
	// for the tasks that were: a schedule's or a suite's firing, or a
	// person instantiating one. The git proxy reads it to find the
	// template's declared push set (Store.GitDeclaredPush), which it then
	// reads from the declaration as the repository's default branch has
	// it now -- never a copy taken at filing time. Written by putTask with
	// the other task_* rows. Needs no SchemaVersion bump, for task_write's
	// own reason: an older store reads back as no task having a template.
	`CREATE TABLE IF NOT EXISTS ` + "`task_filed_template`" + ` (
  ` + "`task_id`" + `     TEXT NOT NULL,
  ` + "`template_id`" + ` TEXT NOT NULL,
  PRIMARY KEY (` + "`task_id`" + `)
)`,

	// A queued task may be ready in every ordinary sense and still be
	// waiting for a finite capability resource. This is deliberately not a
	// task state and not a run: admission writes it before StartRun, and
	// StartRun clears it in the same transaction that records the attempt.
	//
	// It is a derived fact, so it has an owner on the way out as well as
	// on the way in: the write that changes what it was derived from --
	// the task's grants or its target -- deletes it in the same
	// transaction (putTask's invalidateStaleGrantWait), because the only
	// other thing that re-examines it is an admission probe, and a probe
	// only runs for a candidate a cycle has worker capacity for.
	`CREATE TABLE IF NOT EXISTS ` + "`task_grant_wait`" + ` (
  ` + "`task_id`" + `    TEXT     NOT NULL,
  ` + "`capability`" + ` TEXT     NOT NULL,
  ` + "`resource`" + `   TEXT     NOT NULL,
  ` + "`waiting_at`" + ` DATETIME NOT NULL,
  PRIMARY KEY (` + "`task_id`" + `)
)`,

	// An operator's pause (TaskPause): one row while a task is paused,
	// none otherwise. A table of its own rather than columns on task, so
	// that pausing writes nothing a task's standing is made of -- its
	// approval, order_key, deferral, links and run-now request all stay
	// exactly where they were, and resuming is deleting this row. The
	// views below (task_ready) and RunNow read its presence; the columns
	// are what the UI shows and what the orchestrator's pause reconciler
	// (orchestrator.reconcilePauses) needs to end a paused run.
	//
	// Needs no SchemaVersion bump, for task_write's own reason: Init's
	// CREATE TABLE IF NOT EXISTS creates it on an existing database, and
	// an older store gains an empty table -- no task paused, which is
	// exactly what one upgrading into this has.
	`CREATE TABLE IF NOT EXISTS ` + "`task_pause`" + ` (
  ` + "`task_id`" + `           TEXT     NOT NULL,
  ` + "`paused_at`" + `         DATETIME NOT NULL,
  ` + "`paused_by_kind`" + `    TEXT     NOT NULL,
  ` + "`paused_by_id`" + `      TEXT     NOT NULL,
  ` + "`from_state`" + `        TEXT     NOT NULL,
  ` + "`immediate`" + `         INTEGER  NOT NULL DEFAULT 0,
  ` + "`grace_until`" + `       DATETIME NULL,
  ` + "`run_id`" + `            TEXT     NULL,
  PRIMARY KEY (` + "`task_id`" + `)
)`,

	`CREATE TABLE IF NOT EXISTS ` + "`task_grant`" + ` (
  ` + "`task_id`" + `    TEXT NOT NULL,
  ` + "`capability`" + ` TEXT NOT NULL,
  ` + "`via`" + `        TEXT NOT NULL,
  ` + "`folder`" + `     TEXT NULL,
  PRIMARY KEY (` + "`task_id`" + `, ` + "`capability`" + `)
)`,

	// blocks is stored rather than recomputed in SQL so task_blocked can
	// be a plain join instead of hard-coding the kind vocabulary in two
	// places where it could drift.
	`CREATE TABLE IF NOT EXISTS ` + "`task_link`" + ` (
  ` + "`task_id`" + ` TEXT    NOT NULL,
  ` + "`kind`" + `    TEXT    NOT NULL,
  ` + "`target`" + `  TEXT    NOT NULL,
  ` + "`blocks`" + `  INTEGER NOT NULL,
  PRIMARY KEY (` + "`task_id`" + `, ` + "`kind`" + `, ` + "`target`" + `)
)`,

	`CREATE TABLE IF NOT EXISTS ` + "`task_tag`" + ` (
  ` + "`task_id`" + ` TEXT NOT NULL,
  ` + "`tag`" + `     TEXT NOT NULL,
  PRIMARY KEY (` + "`task_id`" + `, ` + "`tag`" + `)
)`,

	`CREATE INDEX IF NOT EXISTS ` + "`task_tag_tag`" + ` ON ` + "`task_tag`" + ` (` + "`tag`" + `, ` + "`task_id`" + `)`,

	// One task's values for the fields this deployment declares
	// (custom_field below, and model.CustomField's own doc comment on
	// the split between the two). A table rather than a JSON column on
	// task, for task_tag's own reason: "which tasks say `customer` is
	// `acme`?" is worth being able to ask in SQL, and a row per value
	// keeps the dump of a task's fields a diff somebody can read.
	//
	// Nothing joins this to custom_field, deliberately. A field deleted
	// from the state repository leaves the values tasks already carry
	// exactly where they are -- a foreign key would instead make
	// deleting a declaration silently rewrite history on every task that
	// ever used it (NormalizeCustomFieldValues has the reasoning).
	// The value is part of the key, which is what lets a field declared
	// `multiple` (model.CustomField.Multiple) carry several: a row per
	// value, the way task_tag holds a task's tags, rather than a list
	// packed into one column that no SQL could ask a question of and no
	// diff could show one line of. It also makes a duplicate value
	// impossible to store rather than merely unlikely -- what such a
	// field holds is a set.
	//
	// Widening it is the one schema change here that CREATE TABLE IF NOT
	// EXISTS cannot make to a database that already exists, so
	// Store.ensureTaskCustomFieldValueKey rebuilds the table for one
	// written before it.
	`CREATE TABLE IF NOT EXISTS ` + "`task_custom_field`" + ` (
  ` + "`task_id`" + ` TEXT NOT NULL,
  ` + "`name`" + `    TEXT NOT NULL,
  ` + "`value`" + `   TEXT NOT NULL,
  PRIMARY KEY (` + "`task_id`" + `, ` + "`name`" + `, ` + "`value`" + `)
)`,

	`CREATE TABLE IF NOT EXISTS ` + "`task_observation`" + ` (
  ` + "`task_id`" + `                     TEXT     NOT NULL,
  ` + "`closed_at`" + `                   DATETIME NULL,
  ` + "`completed_at`" + `                DATETIME NULL,
  ` + "`pending_question_comment_id`" + ` INTEGER  NULL,
  ` + "`baseline_comment_id`" + `         INTEGER  NULL,
  ` + "`pending_secret`" + `              TEXT     NULL,
  ` + "`merge_queue_blocked_at`" + `      DATETIME NULL,
  ` + "`merge_queue_refreshed_at`" + `    DATETIME NULL,
  ` + "`merge_queue_repair_at`" + `       DATETIME NULL,
  ` + "`merge_queue_state`" + `           TEXT     NULL,
  ` + "`merge_queue_note`" + `            TEXT     NULL,
  ` + "`merge_queue_state_at`" + `        DATETIME NULL,
  ` + "`merge_queue_stalled_at`" + `      DATETIME NULL,
  ` + "`merge_queue_inherited`" + `       TEXT     NULL,
  ` + "`merge_queue_blocked_base`" + `    TEXT     NULL,
  ` + "`merge_queue_rearms`" + `          INTEGER  NULL,
  ` + "`merge_queue_repairs`" + `         INTEGER  NULL,
  ` + "`merge_queue_repair_base`" + `     TEXT     NULL,
  ` + "`merge_queue_repair_failures`" + ` TEXT     NULL,
  ` + "`merge_queue_repair_why`" + `      TEXT     NULL,
  ` + "`merge_queue_stall_retry_at`" + `   DATETIME NULL,
  ` + "`merge_queue_stall_retry_head`" + ` TEXT     NULL,
  ` + "`merge_queue_stall_retry_why`" + `  TEXT     NULL,
  ` + "`merge_queue_entered_at`" + `      DATETIME NULL,
  ` + "`observed_at`" + `                 DATETIME NULL,
  ` + "`retry_requested_at`" + `          DATETIME NULL,
  ` + "`run_now_at`" + `                  DATETIME NULL,
  ` + "`pr_opened_at`" + `                DATETIME NULL,
  ` + "`pr_merged_at`" + `                DATETIME NULL,
  ` + "`pr_closed_at`" + `                DATETIME NULL,
  ` + "`pr_additions`" + `                INTEGER  NULL,
  ` + "`pr_deletions`" + `                INTEGER  NULL,
  ` + "`pr_changed_files`" + `            INTEGER  NULL,
  ` + "`ci_run_id`" + `                   TEXT     NULL,
  ` + "`ci_commit`" + `                   TEXT     NULL,
  ` + "`ci_started_at`" + `               DATETIME NULL,
  ` + "`ci_conclusion`" + `               TEXT     NULL,
  ` + "`ci_finished_at`" + `              DATETIME NULL,
  ` + "`ci_detail`" + `                   TEXT     NULL,
  ` + "`ci_attempts`" + `                 INTEGER  NULL,
  ` + "`ci_previous_conclusion`" + `      TEXT     NULL,
  ` + "`ci_previous_detail`" + `          TEXT     NULL,
  ` + "`ci_cancelled_by`" + `             TEXT     NULL,
  ` + "`ci_recheck_of`" + `               TEXT     NULL,
  ` + "`ci_recheck_detail`" + `           TEXT     NULL,
  ` + "`suspended_session_token`" + `     TEXT     NULL,
  ` + "`suspended_session_framework`" + ` TEXT     NULL,
  ` + "`suspended_session_run_id`" + `    TEXT     NULL,
  ` + "`suspended_session_at`" + `        DATETIME NULL,
  ` + "`force_merge_at`" + `              DATETIME NULL,
  ` + "`force_merge_reason`" + `          TEXT     NULL,
  ` + "`force_merge_by`" + `              TEXT     NULL,
  ` + "`force_merge_skipped`" + `         TEXT     NULL,
  ` + "`force_merge_head_sha`" + `        TEXT     NULL,
  ` + "`streak_count`" + `                 INTEGER  NOT NULL DEFAULT 0,
  ` + "`last_failed_at`" + `               DATETIME NULL,
  ` + "`last_succeeded_at`" + `            DATETIME NULL,
  ` + "`delivered_comment_id`" + `         INTEGER  NULL,
  ` + "`merge_queue_cancel_handoff_at`" + `  DATETIME NULL,
  ` + "`merge_queue_cancel_handoff_run`" + ` TEXT     NULL,
  ` + "`merge_queue_reason`" + `          TEXT     NULL,
  PRIMARY KEY (` + "`task_id`" + `)
)`,

	// detail is a short human-readable reason behind outcome -- see
	// model.Run.Detail's own doc comment on why it exists at all.
	//
	// transcript is the full narrative record of what the agent said and
	// did over the run -- model.Run.Transcript's own doc comment explains
	// why it is worth a column of its own rather than folding into detail
	// (bwsalmon/agents#446). It is written once, by SetRunTranscript,
	// after FinishRun has already recorded outcome/detail -- the same
	// after-the-fact shape SetRunOutcome already uses -- and stays NULL
	// for a run still in flight, or one whose framework never populated
	// agent.Result.Transcript at all.
	//
	// agent_started_at is the one moment inside a run that nothing else
	// records: when its agent actually got its first turn. started_at is
	// stamped by dispatch, before any sandbox exists (Store.SetRunSandbox's
	// own doc comment), so finished_at - started_at is setup *and* agent
	// work together -- a VM boot, a checkout and a capability mint on one
	// side, whatever the agent then did on the other. Splitting the two is
	// the whole point: README's own "What this costs" says a VM boot moved
	// onto the critical path of every task and is "worth measuring before
	// reaching for" a golden image or a warm spare, and this column is what
	// makes that a query (pkg/metrics' SetupLatency vs AgentLatency).
	//
	// Written once, by SetRunAgentStarted, immediately before
	// orchestrator.RunDispatch hands the run to agent.Framework.Run -- the
	// same after-the-fact shape SetRunOutcome and SetRunTranscript already
	// use. It stays NULL for a run still in setup, and for one that never
	// reached its agent at all (outcome "setup-failed", a checkout that
	// would not clone), which is exactly the distinction a reader wants:
	// no agent latency to report, because no agent ran.
	//
	// prompt is the whole text the agent was actually handed for this run
	// -- the task's own title and body plus everything grain adds to them
	// (orchestrator.BuildPrompt's branch and repo sentences, the
	// conversation so far, the attachments section, and each capability's
	// own prompt section). Nothing else records it: the prompt is
	// assembled per run, from a task that may since have been edited and
	// a conversation that has since grown, so it cannot be reconstructed
	// after the fact from the task alone -- which is exactly why "what
	// were you actually told?" needs a column rather than a re-derivation.
	// It carries no credential material by construction (every capability
	// PromptSection names its credential and hands over none -- see
	// githubtoken and gcpkey's own tests), which is what makes it safe to
	// show in the UI.
	//
	// Written once, by SetRunPrompt, immediately before
	// orchestrator.RunDispatch hands the run to agent.Framework.Run, and
	// so NULL for a run that never reached its agent at all (a checkout
	// that would not clone, a capability that would not mint) as well as
	// for every run recorded before this column existed.
	// activity and activity_at are the synopsis of what this run is doing
	// right now -- one short phrase ("waiting for CI on the third push",
	// "reading pkg/orchestrator") and the moment it was last written.
	// Every other column here is grain's record of a run; this is mostly
	// the run's own account of itself, written through the update_status
	// tool and derived from nothing.
	//
	// Mostly, because a run cannot narrate the part of its life that
	// happens before it: while its sandbox is being built, its repo
	// cloned and its credentials minted there is no agent yet to call the
	// tool, and on a kontur deployment that stretch is minutes long. So
	// grain stamps its own phrases here over that stretch
	// (orchestrator.setupNotes) through the same Store.SetTaskActivity,
	// and clears the last of them as it hands the run to its agent.
	// agent_started_at is what tells the two apart for a reader: a note
	// standing while that column is still NULL is grain's
	// (model.RunActivity.BySetup).
	//
	// It answers the question a list of live runs could not: a task has
	// read 'running' for forty minutes and the only way to find out what
	// that meant was to open its transcript after the fact. A phrase on
	// the row turns that into a glance (ui.Task.Activity, TaskList.jsx).
	//
	// activity_at is worth its own column rather than being implied by the
	// write, because the phrase alone cannot say whether it is current: a
	// synopsis from ten seconds ago and one from an hour ago read
	// identically and mean opposite things, so a reader is shown how long
	// it has stood.
	//
	// Both stay NULL for a run that never called the tool, which is every
	// run recorded before the column existed and every run driven by a
	// framework with no route back to the daemon that would carry the
	// call -- so a reader must treat "no synopsis" as ordinary rather than
	// as a run in trouble.
	//
	// target_owner and target_name are the write target this run was
	// actually dispatched against -- the repo its sandbox was told to
	// clone and the only one it may push. The task carries a target of
	// its own (task.target_owner/target_name) and that is where this one
	// is copied from, at the moment the sandbox is recorded
	// (Store.SetRunSandbox), because the task's own can be edited while
	// the run is in flight and this one cannot.
	//
	// Which matters for exactly one reader, and it is the one that can
	// throw a run's work away: the git proxy authorizes a push by
	// resolving the sandbox to its live run and comparing the repo asked
	// for against that run's write target (Store.GitScope,
	// gitproxy.ModelAuthorizer.Authorize). Read from the task, a repo
	// change made mid-run took a live run's push rights to the repo it
	// had spent an hour working in -- its final `git push` refused by the
	// proxy -- and handed it rights to one it had never checked out.
	// Read from here, an edit reaches the task's next run and nothing
	// else, which is what the panel that offers the edit has always said
	// it does (ui DetailOverlay's Target).
	//
	// NULL means "not pinned", which a reader must treat as the old
	// behaviour rather than as "no target": every run recorded before
	// these columns existed reads back that way, and GitScope falls back
	// to the task's own target for it. A run dispatched for a task that
	// genuinely had no target is pinned as two empty strings instead --
	// pinned, to nothing -- so that a target added mid-run does not
	// become something the sandbox may suddenly push.
	//
	// run_now_at is the Run now request (task_observation.run_now_at)
	// this run spent when it started, copied here by startRun in the same
	// transaction that clears it there, and NULL for a run nobody asked
	// for by name. It is what lets a run that ended without doing the
	// work -- one that never reached its agent, or one the deployment
	// stopped (RunNowSurvives) -- put the request back with its original
	// timestamp, so it keeps its place among the other requests.
	//
	// model and agent_framework are what this run actually ran on, as
	// dispatch resolved them out of the task's own pins, the deployment's
	// default list and whichever connection was free (Store.SetRunModel).
	// They differ from the task's own model/agent_framework whenever the
	// task leaves the choice to the rotation, and NULL for a run recorded
	// before they were kept or one whose framework named neither.
	`CREATE TABLE IF NOT EXISTS ` + "`task_run`" + ` (
  ` + "`id`" + `               TEXT     NOT NULL,
  ` + "`task_id`" + `          TEXT     NOT NULL,
  ` + "`sandbox`" + `          TEXT     NOT NULL,
  ` + "`unit`" + `             TEXT     NULL,
  ` + "`attempt`" + `          INTEGER  NOT NULL,
  ` + "`started_at`" + `       DATETIME NOT NULL,
  ` + "`agent_started_at`" + ` DATETIME NULL,
  ` + "`finished_at`" + `      DATETIME NULL,
  ` + "`outcome`" + `          TEXT     NULL,
  ` + "`detail`" + `           TEXT     NULL,
  ` + "`transcript`" + `       TEXT     NULL,
  ` + "`prompt`" + `           TEXT     NULL,
  ` + "`activity`" + `         TEXT     NULL,
  ` + "`activity_at`" + `      DATETIME NULL,
  ` + "`target_owner`" + `     TEXT     NULL,
  ` + "`target_name`" + `      TEXT     NULL,
  ` + "`run_now_at`" + `       DATETIME NULL,
  ` + "`suspend_kind`" + `     TEXT     NULL,
  ` + "`standing`" + `         TEXT     NULL,
  ` + "`model`" + `            TEXT     NULL,
  ` + "`agent_framework`" + `  TEXT     NULL,
  PRIMARY KEY (` + "`id`" + `)
)`,

	// One suspension (model.Suspension): a run that stopped with a
	// model.SuspensionKind rather than being judged, written by
	// Store.FinishRun or Store.Suspend in the transaction that finishes
	// the run, and marked resumed by the next run's startRun. Keyed by
	// the run, since a run ends once; standing is the JSON
	// model.DispatchStanding the run started with (task_run.standing).
	`CREATE TABLE IF NOT EXISTS ` + "`task_suspension`" + ` (
  ` + "`run_id`" + `         TEXT     NOT NULL,
  ` + "`task_id`" + `        TEXT     NOT NULL,
  ` + "`kind`" + `           TEXT     NOT NULL,
  ` + "`detail`" + `         TEXT     NOT NULL,
  ` + "`resume_on`" + `      TEXT     NULL,
  ` + "`resume_at`" + `      DATETIME NULL,
  ` + "`standing`" + `       TEXT     NOT NULL,
  ` + "`model_hint`" + `     TEXT     NULL,
  ` + "`created_at`" + `     DATETIME NOT NULL,
  ` + "`resumed_at`" + `     DATETIME NULL,
  ` + "`resumed_run_id`" + ` TEXT     NULL,
  PRIMARY KEY (` + "`run_id`" + `)
)`,
	`CREATE INDEX IF NOT EXISTS ` + "`task_suspension_open`" + ` ON ` + "`task_suspension`" + ` (` + "`task_id`" + `) WHERE ` + "`resumed_at`" + ` IS NULL`,

	// One task's goal (model.TaskGoal, docs/design/durable-tasks.md#DT-4):
	// what grain checks to close or rest the task, and the progress it
	// has seen towards it. spec is the JSON model.GoalSpec; max_stall is
	// in seconds.
	`CREATE TABLE IF NOT EXISTS ` + "`task_goal`" + ` (
  ` + "`task_id`" + `                TEXT     NOT NULL,
  ` + "`kind`" + `                   TEXT     NOT NULL,
  ` + "`spec`" + `                   TEXT     NOT NULL,
  ` + "`on_met`" + `                 TEXT     NOT NULL,
  ` + "`declared_by`" + `            TEXT     NOT NULL,
  ` + "`declared_at`" + `            DATETIME NOT NULL,
  ` + "`met_at`" + `                 DATETIME NULL,
  ` + "`evidence`" + `               TEXT     NOT NULL DEFAULT '',
  ` + "`outcome`" + `                TEXT     NOT NULL DEFAULT '',
  ` + "`measure`" + `                TEXT     NOT NULL DEFAULT '',
  ` + "`progress_at`" + `            DATETIME NOT NULL,
  ` + "`resumes_since_progress`" + ` INTEGER  NOT NULL DEFAULT 0,
  ` + "`max_resumes`" + `            INTEGER  NOT NULL,
  ` + "`max_stall`" + `              INTEGER  NOT NULL,
  ` + "`on_limit`" + `               TEXT     NOT NULL,
  ` + "`limit_notified_at`" + `      DATETIME NULL,
  ` + "`counted_run_id`" + `         TEXT     NOT NULL DEFAULT '',
  PRIMARY KEY (` + "`task_id`" + `)
)`,

	// At most one open (finished_at IS NULL) run per task -- what is left
	// of the DB-level backstop bwsalmon/agents#434 asked for once slots
	// stopped existing. It used to be one open run per *slot*: dispatch.
	// Cycle read OccupiedSlots and Ready outside any single transaction
	// and then issued one StartRun per free slot it found, so two
	// overlapping Cycle calls could both see the same slot as free and
	// both dispatch onto it, and this index was what made the second one
	// fail loudly instead of quietly landing a second live run on a
	// claimed slot.
	//
	// A sandbox per task removes that race at its source rather than
	// catching it here: startRun now counts live runs and enforces the
	// concurrency limit inside its own transaction (its doc comment), so
	// there is no window between deciding a run may start and recording
	// that it has. What is still worth an index is the invariant that
	// outlived slots -- a task has at most one run in flight at a time,
	// which task_state already assumes when it reads a live run as
	// 'running', and which dispatch.Cycle relies on when it takes
	// task_ready's own order at face value. A partial index rather than a
	// plain UNIQUE column, since the same task legitimately appears in
	// many finished rows over a store's lifetime (one per attempt) and
	// only the currently-open one must be unique.
	`CREATE UNIQUE INDEX IF NOT EXISTS ` + "`task_run_open_task`" + ` ON ` + "`task_run`" + ` (` + "`task_id`" + `) WHERE ` + "`finished_at`" + ` IS NULL`,
	`CREATE INDEX IF NOT EXISTS ` + "`task_run_task_outcome_started`" + ` ON ` + "`task_run`" + ` (` + "`task_id`" + `, ` + "`outcome`" + `, ` + "`started_at`" + `)`,
	`CREATE INDEX IF NOT EXISTS ` + "`task_run_started`" + ` ON ` + "`task_run`" + ` (` + "`started_at`" + `)`,

	// task_run_tool is the per-run, per-tool census: how many times a run
	// called each tool, how many of those calls came back as errors, how
	// many the tool's own bound cut off, how many had their answer cut
	// short by grain's result cap, and how big the answers were.
	// telemetry.go's own doc comment argues for storing it at all -- it is
	// the one measurement in this schema that is not derivable from
	// something else, because agent.Result does not survive the run.
	//
	// size_buckets is a base-2 histogram of result sizes
	// (model.SizeHistogram), encoded as "bucket:count" pairs. A column
	// rather than a table because it is only ever read whole, with the row
	// it belongs to, and never joined or queried on. dropped_buckets is
	// the same encoding over a different sample: how many bytes each cut
	// answer lost. The pair is what keeps the result cap measurable once
	// it is in force -- size_buckets alone is censored at the cap, so it
	// cannot distinguish a tool that fits under it from one being cut down
	// to it (RunToolUse's own comment).
	//
	// Neither this table nor task_run_check_wait below needs a
	// SchemaVersion bump: Init's own CREATE TABLE IF NOT EXISTS creates a
	// *missing* table on an existing database perfectly well -- what it
	// cannot do is alter one that is already there, which is what every
	// ensure*Column migration and every bump exists for. An older store
	// simply gains two empty tables and starts filling them from its next
	// run. The two newest columns here, elided and dropped_buckets, do
	// need the ladder for exactly that reason, and get a pair of
	// ensureColumn rungs at the end of it; both are defaulted, so a row
	// written before they existed reads back as a run nobody counted cuts
	// for -- which it is.
	//
	// Written once, by orchestrator.RunDispatch, after FinishRun has
	// recorded the run's own outcome -- the same after-the-fact shape
	// task_run.transcript uses. A run that never reached its agent, or
	// that made no tool calls at all, gets no rows here rather than rows
	// of zeroes: that fact is already in its outcome, and a zero row would
	// only dilute the per-tool rates it was averaged into.
	`CREATE TABLE IF NOT EXISTS ` + "`task_run_tool`" + ` (
  ` + "`run_id`" + `           TEXT     NOT NULL,
  ` + "`tool`" + `             TEXT     NOT NULL,
  ` + "`calls`" + `            INTEGER  NOT NULL,
  ` + "`errored`" + `          INTEGER  NOT NULL,
  ` + "`timed_out`" + `        INTEGER  NOT NULL,
  ` + "`elided`" + `           INTEGER  NOT NULL DEFAULT 0,
  ` + "`result_bytes`" + `     INTEGER  NOT NULL,
  ` + "`max_result_bytes`" + ` INTEGER  NOT NULL,
  ` + "`size_buckets`" + `     TEXT     NULL,
  ` + "`dropped_buckets`" + `  TEXT     NULL,
  PRIMARY KEY (` + "`run_id`" + `, ` + "`tool`" + `)
)`,

	// task_run_check_wait is one row per wait_for_checks call: the CI loop
	// BuildPrompt sends every run around, measured end to end.
	//
	// A row per call rather than a per-run total because the sequence is
	// the question. "How many pushes does a run take before its checks go
	// green?" is read off the first wait that ended
	// mcp.WaitVerdictPassed, and a per-run average could not answer it;
	// nor could it show a deployment where most waits end with the clock
	// running out, which is mcp.DefaultWaitForChecksTimeout set wrong for
	// that CI. A run makes a handful of these calls, not hundreds, so the
	// per-call shape costs a handful of rows.
	`CREATE TABLE IF NOT EXISTS ` + "`task_run_check_wait`" + ` (
  ` + "`run_id`" + `        TEXT     NOT NULL,
  ` + "`seq`" + `           INTEGER  NOT NULL,
  ` + "`verdict`" + `       TEXT     NOT NULL,
  ` + "`waited_ms`" + `     INTEGER  NOT NULL,
  ` + "`pushes_before`" + ` INTEGER  NOT NULL,
  PRIMARY KEY (` + "`run_id`" + `, ` + "`seq`" + `)
)`,

	// task_run_wait_credit is one row per run that ever blocked on CI:
	// how long it spent inside wait_for_checks over the whole run, and
	// how much of that was credited back to its wall-clock deadline
	// (orchestrator.RunWaitCreditCap).
	//
	// Both numbers, not just the credit, because the pair is the
	// question. Blocked past credited is a run that hit the cap, and how
	// often that happens -- and by how much -- is what re-prices the cap
	// against real runs rather than against the argument that first set
	// it. A row per run rather than per wait: the waits themselves are
	// already a row each next door (task_run_check_wait.waited_ms), and
	// the cap is a per-run rule, so this is the level it can be read at.
	//
	// A run that never waited gets no row, the same way a run that called
	// no tools gets none in task_run_tool: zero credit is already implied
	// by the absence, and a row of zeroes would only dilute the averages
	// it was read into. Needs no SchemaVersion bump and no migration
	// rung, for the reason the two tables above give.
	`CREATE TABLE IF NOT EXISTS ` + "`task_run_wait_credit`" + ` (
  ` + "`run_id`" + `      TEXT     NOT NULL,
  ` + "`blocked_ms`" + `  INTEGER  NOT NULL,
  ` + "`credited_ms`" + ` INTEGER  NOT NULL,
  PRIMARY KEY (` + "`run_id`" + `)
)`,

	// task_run_ci_habit is one row per run that worked a red build or
	// asked CI for less than the whole suite (model.RunCIHabit): whether
	// it reproduced the failure with a narrowed local test before pushing,
	// whether it pushed a fix after a red wait, and whether it re-ran CI
	// selectively. Four flags rather than counts because the question is
	// a share of runs ("how many repairs reproduced locally first?"), not
	// a volume. A run that did none of it gets no row, for the reason
	// task_run_wait_credit gives. Needs no SchemaVersion bump and no
	// migration rung, for the reason the tables above give.
	`CREATE TABLE IF NOT EXISTS ` + "`task_run_ci_habit`" + ` (
  ` + "`run_id`" + `                      TEXT     NOT NULL,
  ` + "`narrow_test_before_push`" + `     INTEGER  NOT NULL,
  ` + "`narrow_test_before_fix_push`" + ` INTEGER  NOT NULL,
  ` + "`pushed_after_red`" + `            INTEGER  NOT NULL,
  ` + "`selective`" + `                   INTEGER  NOT NULL,
  PRIMARY KEY (` + "`run_id`" + `)
)`,

	// task_run_api_call is one row per agent-API / grain tool call a run
	// made (docs/design/durable-tasks.md#DT-14): which tool, the target
	// ids it read or touched, how many bytes the answer carried back, and
	// when. High volume, local only (staterepo.LocalTables), and pruned
	// after 30 days (model.RunAPICallRetention).
	`CREATE TABLE IF NOT EXISTS ` + "`task_run_api_call`" + ` (
  ` + "`run_id`" + `       TEXT     NOT NULL,
  ` + "`seq`" + `          INTEGER  NOT NULL,
  ` + "`tool`" + `         TEXT     NOT NULL,
  ` + "`target_ids`" + `   TEXT     NOT NULL DEFAULT '[]',
  ` + "`result_bytes`" + ` INTEGER  NOT NULL DEFAULT 0,
  ` + "`at`" + `           DATETIME NOT NULL,
  PRIMARY KEY (` + "`run_id`" + `, ` + "`seq`" + `)
)`,
	`CREATE INDEX IF NOT EXISTS ` + "`idx_task_run_api_call_at`" + ` ON ` + "`task_run_api_call`" + ` (` + "`at`" + `)`,

	// task_run_report_check is one row per steward wake (or weekly sampled
	// judgement audit) whose closing report was checked against the run's
	// call log and the cited task records (docs/design/durable-tasks.md#DT-14).
	`CREATE TABLE IF NOT EXISTS ` + "`task_run_report_check`" + ` (
  ` + "`run_id`" + `     TEXT     NOT NULL,
  ` + "`claims`" + `     INTEGER  NOT NULL DEFAULT 0,
  ` + "`uncited`" + `    INTEGER  NOT NULL DEFAULT 0,
  ` + "`unread`" + `     INTEGER  NOT NULL DEFAULT 0,
  ` + "`mismatched`" + ` INTEGER  NOT NULL DEFAULT 0,
  PRIMARY KEY (` + "`run_id`" + `)
)`,

	// task_run_spool_cursor is how far the daemon has read a live run's
	// run-host spool (pkg/runhost): one row per run driven in a run host,
	// written as the spool is tailed and deleted when the run finishes.
	// It is what lets the next daemon resume the tail with neither a
	// hole nor a duplicate in the transcript, and updated_at is the start
	// of the gap an adoption credits as wait time
	// (docs/non-disruptive-upgrade.md, "Run hosts never touch the store").
	// The run host itself never writes it, or anything else here. Needs no
	// SchemaVersion bump and no migration rung, for the reason the tables
	// above give.
	`CREATE TABLE IF NOT EXISTS ` + "`task_run_spool_cursor`" + ` (
  ` + "`run_id`" + `     TEXT     NOT NULL,
  ` + "`offset`" + `     INTEGER  NOT NULL,
  ` + "`updated_at`" + ` DATETIME NOT NULL,
  PRIMARY KEY (` + "`run_id`" + `)
)`,

	// task_run_staged_attachment is a file a live run attached by path
	// to comment_on_issue or ask_question (mcp.ResponseAttachments),
	// read out of its sandbox when it made the call -- the call's own
	// arguments, which are all the finish path gets back from the agent's
	// transcript, carry the path and not the bytes, and by the time the
	// run is finished its sandbox may already be gone. One row per path
	// a run named; the finish path reads them back by run and deletes
	// them once the run's words are relayed
	// (orchestrator.afterAgentRun). Needs no SchemaVersion bump and no
	// migration rung, for the reason the tables above give.
	`CREATE TABLE IF NOT EXISTS ` + "`task_run_staged_attachment`" + ` (
  ` + "`run_id`" + `       TEXT     NOT NULL,
  ` + "`path`" + `         TEXT     NOT NULL,
  ` + "`filename`" + `     TEXT     NOT NULL,
  ` + "`content_type`" + ` TEXT     NOT NULL,
  ` + "`content`" + `      BLOB     NOT NULL,
  ` + "`created_at`" + `   DATETIME NOT NULL,
  PRIMARY KEY (` + "`run_id`" + `, ` + "`path`" + `)
)`,

	// task_run_unmatched_failure is one row per failed run whose own
	// account of itself matched none of grain's markers for a provider
	// refusal -- neither the usage-limit ones (agent/usagelimit.go) nor
	// the capacity ones (agent/capacity.go). It holds the tail of what
	// the failure actually said, bounded by
	// agent.MaxUnmatchedFailureText.
	//
	// It is an instrument rather than a measurement, and the one thing
	// it measures is grain's own vocabulary. Those markers are phrases a
	// provider writes and grain does not control, so the day one is
	// reworded, nothing matches, every run reads as an ordinary failure
	// and every test still passes -- while five ordinary failures in a
	// row retire a task that was never wrong. Reading a week of these
	// rows is how that is noticed, which is why the text is kept at all
	// and why `suspect` is stored next to it: the loose read
	// (agent.ResemblesRefusal) of a failure the strict reads missed.
	//
	// The text is a run's own error, not its transcript: at most a
	// kilobyte per failed run, written once and never updated, the same
	// after-the-fact shape task_run.transcript and the census tables
	// have. `at` is stored rather than joined from task_run.finished_at,
	// unlike the census next door: this is read by time window and by
	// nothing else, and a log that has to join to find out when it
	// happened is a log with the wrong primary question.
	//
	// Needs no SchemaVersion bump and no migration rung, for the reason
	// task_run_tool's own comment gives: an older store gains one empty
	// table and starts filling it from its next failed run.
	`CREATE TABLE IF NOT EXISTS ` + "`task_run_unmatched_failure`" + ` (
  ` + "`run_id`" + `    TEXT     NOT NULL,
  ` + "`task_id`" + `   TEXT     NOT NULL,
  ` + "`framework`" + ` TEXT     NOT NULL,
  ` + "`at`" + `        DATETIME NOT NULL,
  ` + "`text`" + `      TEXT     NOT NULL,
  ` + "`suspect`" + `   INTEGER  NOT NULL,
  PRIMARY KEY (` + "`run_id`" + `)
)`,

	// The one question this table is asked -- "what did grain fail to
	// classify over the last week?" -- is a range scan on `at`, and the
	// table grows for as long as the deployment has failures.
	`CREATE INDEX IF NOT EXISTS ` + "`task_run_unmatched_failure_at`" + ` ON ` + "`task_run_unmatched_failure`" + ` (` + "`at`" + `)`,

	// task_run_exec_failure is one row per run that had at least one
	// sandbox tool call fail on the cluster's exec transport after the
	// exec runner's own retries (mcp.ExecTransportFailed): the tunnel to
	// the node's kubelet failing under a run that had already reached its
	// agent. calls is how many of its calls did. Setup failures on the
	// same transport are not here -- they are the run's own outcome and
	// detail on task_run -- and this is their mid-run counterpart, which
	// task_run alone cannot show.
	//
	// `at` is when the run finished, which is when the count is known:
	// agent.Result, the one place the calls are seen, exists only then.
	// Read by time window (orchestrator's exec-tunnel alert, `grain
	// failures`) and joined to task_run by the metrics census.
	//
	// Needs no SchemaVersion bump and no migration rung, for the reason
	// task_run_tool's own comment gives.
	`CREATE TABLE IF NOT EXISTS ` + "`task_run_exec_failure`" + ` (
  ` + "`run_id`" + `  TEXT     NOT NULL,
  ` + "`task_id`" + ` TEXT     NOT NULL,
  ` + "`at`" + `      DATETIME NOT NULL,
  ` + "`calls`" + `   INTEGER  NOT NULL,
  PRIMARY KEY (` + "`run_id`" + `)
)`,
	`CREATE INDEX IF NOT EXISTS ` + "`task_run_exec_failure_at`" + ` ON ` + "`task_run_exec_failure`" + ` (` + "`at`" + `)`,

	// task_run_capability_audit is what a run's capability grants did
	// for it, as their servers reported it when the run released them
	// (docs/capabilities-spec.md §4.4.4, model.CapabilityAuditRecord):
	// host-script's one record per call -- script, argv, exit code,
	// duration -- above all. One row per record, in the order the
	// servers returned them, `seq` counting from 1 within the run.
	//
	// `record` is the server's own JSON object, verbatim; `note` is
	// grain's instead, for the two things grain says about an audit
	// itself (records a server left out, an audit it refused), and the
	// two are never both set. `capability` (the grant's
	// <capability_id>#<method>) and `attempt_id` say which grant of the
	// run a record is about, so that records stay attempt-scoped
	// however many grants a run held.
	//
	// Written after the run has finished -- grants are released when it
	// ends, recreate_sandbox included, never before (§4.4.3) -- the same
	// after-the-fact shape task_run.transcript has. Needs no
	// SchemaVersion bump and no migration rung, for the reason
	// task_run_tool's own comment gives.
	`CREATE TABLE IF NOT EXISTS ` + "`task_run_capability_audit`" + ` (
  ` + "`run_id`" + `     TEXT     NOT NULL,
  ` + "`seq`" + `        INTEGER  NOT NULL,
  ` + "`capability`" + ` TEXT     NOT NULL,
  ` + "`attempt_id`" + ` TEXT     NOT NULL,
  ` + "`at`" + `         DATETIME NOT NULL,
  ` + "`record`" + `     TEXT     NOT NULL DEFAULT '',
  ` + "`note`" + `       TEXT     NOT NULL DEFAULT '',
  PRIMARY KEY (` + "`run_id`" + `, ` + "`seq`" + `)
)`,

	`CREATE TABLE IF NOT EXISTS ` + "`lease`" + ` (
  ` + "`run_id`" + `     TEXT     NOT NULL,
  ` + "`capability`" + ` TEXT     NOT NULL,
  ` + "`resource`" + `   TEXT     NOT NULL,
  ` + "`minted_by`" + `  TEXT     NOT NULL,
  ` + "`issued_at`" + `  DATETIME NOT NULL,
  ` + "`expires_at`" + ` DATETIME NULL,
  PRIMARY KEY (` + "`run_id`" + `, ` + "`capability`" + `, ` + "`resource`" + `)
)`,

	// The conversation, as grain's own rows rather than a GitHub issue's
	// comment thread. INTEGER PRIMARY KEY is SQLite's own rowid alias, the
	// AUTOINCREMENT that role needs: the id is both the ordering and the
	// thing task_observation.pending_question_comment_id names, so a
	// caller that has just written a question needs its id back to record
	// which question is outstanding.
	//
	// handover marks the one kind of comment another part of grain has to
	// find again rather than merely replay in order: a run's standing
	// summary of the older part of the thread, which the prompt builder
	// carries outside the budget it cuts the rest of the conversation to
	// (Comment.Handover, orchestrator.commentThreadSection). It defaults
	// to 0, so every comment written before the column existed reads back
	// as the ordinary message it was.
	//
	// summary is a question's one-line summary (Comment.Summary), '' on
	// every comment that is not a question an agent asked.
	`CREATE TABLE IF NOT EXISTS ` + "`task_comment`" + ` (
  ` + "`id`" + `                 INTEGER PRIMARY KEY AUTOINCREMENT,
  ` + "`task_id`" + `            TEXT    NOT NULL,
  ` + "`author_kind`" + `        TEXT    NOT NULL,
  ` + "`author_id`" + `          TEXT    NOT NULL,
  ` + "`author_behalf_kind`" + ` TEXT     NULL,
  ` + "`author_behalf_id`" + `   TEXT     NULL,
  ` + "`body`" + `               TEXT     NOT NULL,
  ` + "`created_at`" + `         DATETIME NOT NULL,
  ` + "`handover`" + `           INTEGER  NOT NULL DEFAULT 0,
  ` + "`summary`" + `            TEXT     NOT NULL DEFAULT ''
)`,

	`CREATE INDEX IF NOT EXISTS ` + "`task_comment_task`" + ` ON ` + "`task_comment`" + ` (` + "`task_id`" + `)`,

	// One row per file (bwsalmon/agents#522), content included -- the same
	// "the store is durable; nothing needs to stay reachable to read it
	// back" reasoning task_comment's own doc comment gives for the
	// conversation itself, rather than a path on disk or in a bucket that
	// this row would have to keep pointing at correctly forever.
	// comment_id is NULL for a file carried by the task's own body, and a
	// task_comment.id for one carried by a later comment -- both share
	// this table rather than two, since a dispatched run materializing
	// them into a sandbox (orchestrator's AttachmentsDir) treats every
	// attachment exactly alike regardless of which one it came from.
	// content_type and size are carried alongside content rather than
	// derived from it on every read: content_type is what a browser's
	// file picker already told the upload (and is not always reliably
	// re-derivable from bytes alone), and size is what a listing needs
	// without paying to read every attachment's full content back out of
	// the database just to report how big it is.
	`CREATE TABLE IF NOT EXISTS ` + "`task_attachment`" + ` (
  ` + "`id`" + `           INTEGER PRIMARY KEY AUTOINCREMENT,
  ` + "`task_id`" + `      TEXT     NOT NULL,
  ` + "`comment_id`" + `   INTEGER  NULL,
  ` + "`filename`" + `     TEXT     NOT NULL,
  ` + "`content_type`" + ` TEXT     NOT NULL,
  ` + "`size`" + `         INTEGER  NOT NULL,
  ` + "`content`" + `      BLOB     NOT NULL,
  ` + "`created_at`" + `   DATETIME NOT NULL
)`,

	`CREATE INDEX IF NOT EXISTS ` + "`task_attachment_task`" + ` ON ` + "`task_attachment`" + ` (` + "`task_id`" + `)`,

	// Task identity, allocated here rather than borrowed from a GitHub
	// issue number. A task used to be identified by the issue it was filed
	// from ("owner/name/7"), which meant nothing could file a task without
	// first creating an issue -- the coupling that made GitHub the input.
	//
	// INTEGER PRIMARY KEY rather than a counter column somebody reads and
	// writes back: allocation has to stay correct with a daemon, a UI and
	// a CLI all writing at once (README's "Single writer, many openers"),
	// and an INSERT that lets SQLite assign the rowid is atomic where
	// read-modify-write is a race. The row it leaves behind is also a
	// record of when each id was handed out, which a bare counter throws
	// away.
	`CREATE TABLE IF NOT EXISTS ` + "`task_sequence`" + ` (
  ` + "`number`" + `    INTEGER PRIMARY KEY AUTOINCREMENT,
  ` + "`issued_at`" + ` DATETIME NOT NULL
)`,

	// One row per schedule (bwsalmon/agents#376), not one per firing --
	// task_sequence's own reasoning applies again for a firing's
	// identity, but the schedule itself needs exactly one durable row to
	// carry next_run_at/last_run_at and the enabled switch a UI toggles.
	// recurrence_kind/every_n_hours/time_of_day_minutes/weekday/day_of_month
	// are model.Recurrence's fields (bwsalmon/agents#464) -- only the
	// subset matching recurrence_kind is ever non-NULL for a given row
	// (model.Recurrence.Validate's own doc comment has the mapping), the
	// same "column per case, most left NULL" shape task's own
	// approval_actor_kind columns already use for a different sum type.
	// This replaces the original bare interval_ms (every N hours since it
	// last fired, with no wall-clock alignment); a database from before
	// that widening is migrated by ensureScheduleRecurrenceColumns.
	//
	// template_id and suite_id are what a firing actually is: both NULL
	// for a schedule carrying its own inline task content, template_id
	// for one firing a template's content instead, suite_id for one
	// starting a suite run instead of filing a task at all
	// (model.Schedule's own doc comment on why the two are mutually
	// exclusive). A database created before either existed gets the
	// column from
	// ensureScheduleTemplateColumn/ensureScheduleSuiteColumn rather
	// than from this DDL.
	//
	// overlap is model.OverlapPolicy: what this schedule does when it
	// comes due with its own previous firing still unfinished. 'skip'
	// for every row on a database created before it existed
	// (ensureScheduleOverlapColumn), which is the behaviour every
	// schedule had when there was no column to say otherwise.
	//
	// network_access is model.Schedule.NetworkAccess (grain/task-427),
	// template.network_access's own column one table over: how much of
	// the internet the sandbox of every task an inline schedule files
	// may reach. DEFAULT '' both here and in
	// ensureScheduleNetworkAccessColumn for a database created before
	// it, which reads as "no override, take the deployment's own answer"
	// -- exactly what every schedule written before this column did.
	//
	// nothing_needed_action is model.Schedule.NothingNeededAction,
	// task.nothing_needed_action's own column for what a schedule files:
	// DEFAULT '' here and in Store.migrate for a database created before
	// it, which reads as "close" -- what every run ending on
	// nothing_needed did before a schedule could say otherwise.
	//
	// workflow_review is model.Schedule.WorkflowReview, when a schedule
	// that runs a workflow files a review of the run: DEFAULT '' here and
	// in Store.migrate, which reads as "on-failure" (grain/task-3160).
	//
	// every_n_minutes is model.Recurrence.EveryNMinutes, every_n_hours'
	// counterpart for RecurrenceEveryNMinutes: NULL on every other kind,
	// and last in the table because schema version 33 added it with
	// ALTER TABLE, which can only append.
	`CREATE TABLE IF NOT EXISTS ` + "`schedule`" + ` (
  ` + "`id`" + `                    TEXT     NOT NULL,
  ` + "`title`" + `                 TEXT     NOT NULL,
  ` + "`body`" + `                  TEXT     NOT NULL,
  ` + "`target_owner`" + `          TEXT     NOT NULL,
  ` + "`target_name`" + `           TEXT     NOT NULL,
  ` + "`base`" + `                  TEXT     NULL,
  ` + "`auto_merge`" + `            INTEGER  NOT NULL,
  ` + "`auto_approve_proposals`" + ` INTEGER NOT NULL DEFAULT 0,
  ` + "`template_id`" + `           TEXT     NULL,
  ` + "`suite_id`" + `              TEXT     NULL,
  ` + "`recurrence_kind`" + `       TEXT     NOT NULL,
  ` + "`every_n_hours`" + `         INTEGER  NULL,
  ` + "`time_of_day_minutes`" + `   INTEGER  NULL,
  ` + "`weekday`" + `               INTEGER  NULL,
  ` + "`day_of_month`" + `          INTEGER  NULL,
  ` + "`overlap`" + `               TEXT     NOT NULL DEFAULT 'skip',
  ` + "`network_access`" + `        TEXT     NOT NULL DEFAULT '',
  ` + "`nothing_needed_action`" + ` TEXT     NOT NULL DEFAULT '',
  ` + "`agent_model`" + `           TEXT     NOT NULL DEFAULT '',
  ` + "`time_zone`" + `             TEXT     NOT NULL DEFAULT '',
  ` + "`workflow_review`" + `       TEXT     NOT NULL DEFAULT '',
  ` + "`enabled`" + `               INTEGER  NOT NULL,
  ` + "`next_run_at`" + `           DATETIME NOT NULL,
  ` + "`last_run_at`" + `           DATETIME NULL,
  ` + "`created_at`" + `            DATETIME NOT NULL,
  ` + "`every_n_minutes`" + `       INTEGER  NULL,
  PRIMARY KEY (` + "`id`" + `)
)`,

	// task_sequence's own doc comment gives the reasoning for a dedicated
	// allocator rather than a counter column: an INSERT that lets SQLite
	// assign the rowid is atomic where read-modify-write is a race.
	`CREATE TABLE IF NOT EXISTS ` + "`schedule_sequence`" + ` (
  ` + "`number`" + `    INTEGER PRIMARY KEY AUTOINCREMENT,
  ` + "`issued_at`" + ` DATETIME NOT NULL
)`,

	// task_read's own doc comment gives the reasoning for a table rather
	// than a JSON column, applied again here now that a schedule's firing
	// carries read-only repos too (bwsalmon/agents#464).
	`CREATE TABLE IF NOT EXISTS ` + "`schedule_read`" + ` (
  ` + "`schedule_id`" + ` TEXT NOT NULL,
  ` + "`owner`" + `       TEXT NOT NULL,
  ` + "`name`" + `        TEXT NOT NULL,
  PRIMARY KEY (` + "`schedule_id`" + `, ` + "`owner`" + `, ` + "`name`" + `)
)`,

	// task_write's own shape and its own reasoning (grain/task-86),
	// ported onto a schedule: the extra repositories a task this
	// schedule files may have work landed in, with the base branch for
	// each, so a recurring job that spans two repos can be declared once
	// instead of filed by hand every time.
	//
	// Needs no SchemaVersion bump, for task_write's own reason: Init's
	// CREATE TABLE IF NOT EXISTS creates a missing table on an existing
	// database, and an older store gains one empty table, which reads
	// back as every schedule having no extra write targets -- exactly
	// what those schedules were saved as.
	`CREATE TABLE IF NOT EXISTS ` + "`schedule_write`" + ` (
  ` + "`schedule_id`" + ` TEXT NOT NULL,
  ` + "`owner`" + `       TEXT NOT NULL,
  ` + "`name`" + `        TEXT NOT NULL,
  ` + "`base`" + `        TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (` + "`schedule_id`" + `, ` + "`owner`" + `, ` + "`name`" + `, ` + "`base`" + `)
)`,

	// task_grant's own shape, for a schedule's own capabilities
	// (bwsalmon/agents#464) -- via is always 'label' here (a human chose
	// it in the schedule's own form, same as grantsFor does for a task),
	// but the column exists anyway so scanning stays identical to
	// task_grant's.
	`CREATE TABLE IF NOT EXISTS ` + "`schedule_grant`" + ` (
  ` + "`schedule_id`" + ` TEXT NOT NULL,
  ` + "`capability`" + `  TEXT NOT NULL,
  ` + "`via`" + `         TEXT NOT NULL,
  ` + "`folder`" + `      TEXT NULL,
  PRIMARY KEY (` + "`schedule_id`" + `, ` + "`capability`" + `)
)`,

	// task_custom_field's own shape, for the values every task this
	// schedule files carries (Schedule.CustomFields, grain/task-105).
	// Content, so it sits with schedule_read/schedule_write/
	// schedule_grant and is exported and imported with them.
	//
	// Nothing joins it to custom_field either, for task_custom_field's
	// own reason: a declaration deleted from the state repository leaves
	// the schedules that named it alone, and the firing simply stops
	// carrying that value (model.FilterCustomFieldValues).
	//
	// The value is in the key for task_custom_field's own reason: a field
	// declared `multiple` carries a set of them, a row per value, and no
	// rebuild is needed for a database written before this table since
	// there is no such database -- the table arrives keyed this way.
	//
	// Needs no SchemaVersion bump, for schedule_write's own reason: an
	// older store gains one empty table, which reads back as every
	// schedule saying nothing about any field -- exactly what those
	// schedules were saved as.
	`CREATE TABLE IF NOT EXISTS ` + "`schedule_custom_field`" + ` (
  ` + "`schedule_id`" + ` TEXT NOT NULL,
  ` + "`name`" + `        TEXT NOT NULL,
  ` + "`value`" + `       TEXT NOT NULL,
  PRIMARY KEY (` + "`schedule_id`" + `, ` + "`name`" + `, ` + "`value`" + `)
)`,

	// One row per template (bwsalmon/agents#516) -- task_sequence's own
	// "identity allocated here, not borrowed" reasoning applies again:
	// name/title/body/auto_merge are exactly the reusable-content
	// fields a schedule already carried inline, now given a row of
	// their own so more than one schedule (schedule.template_id) can
	// point at the same one instead of repeating it.
	// target_owner/target_name/base are a template's optional binding
	// (model.Template's own doc comment on why it is optional): which
	// repo and branch a firing targets is by default a property of the
	// caller using this template, and these carry a value only for a
	// template deliberately bound to one repo (grain/task-285). Empty
	// target_owner/target_name -- always both, since putTemplate writes
	// the pair from one *RepoRef -- is an unbound template, the ordinary
	// case; empty base alongside a set target is a repo binding that
	// pins no branch. Empty strings rather than NULL so a database from
	// before templates lost their target (when the same three columns
	// were mandatory) upgrades by keeping its rows exactly as they are:
	// what was a mandatory target reads back as a binding, which is what
	// it meant.
	`CREATE TABLE IF NOT EXISTS ` + "`template`" + ` (
  ` + "`id`" + `           TEXT     NOT NULL,
  ` + "`name`" + `         TEXT     NOT NULL,
  ` + "`title`" + `        TEXT     NOT NULL,
  ` + "`body`" + `         TEXT     NOT NULL,
  ` + "`target_owner`" + ` TEXT     NOT NULL,
  ` + "`target_name`" + `  TEXT     NOT NULL,
  ` + "`base`" + `         TEXT     NULL,
  ` + "`auto_merge`" + `   INTEGER  NOT NULL,
  ` + "`created_at`" + `   DATETIME NOT NULL,
  ` + "`network_access`" + ` TEXT   NOT NULL DEFAULT '',
  ` + "`nothing_needed_action`" + ` TEXT NOT NULL DEFAULT '',
  ` + "`agent_model`" + `  TEXT     NOT NULL DEFAULT '',
  ` + "`interactive`" + `  INTEGER  NOT NULL DEFAULT 0,
  ` + "`goal`" + `         TEXT     NOT NULL DEFAULT '',
  ` + "`change_target_kind`" + ` TEXT NOT NULL DEFAULT '',
  ` + "`change_target_spec`" + ` TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (` + "`id`" + `)
)`,

	// schedule_sequence's own doc comment gives the reasoning for a
	// dedicated allocator rather than a counter column.
	`CREATE TABLE IF NOT EXISTS ` + "`template_sequence`" + ` (
  ` + "`number`" + `    INTEGER PRIMARY KEY AUTOINCREMENT,
  ` + "`issued_at`" + ` DATETIME NOT NULL
)`,

	// schedule_read's own doc comment gives the reasoning for a table
	// rather than a JSON column, ported onto a template's own id.
	`CREATE TABLE IF NOT EXISTS ` + "`template_read`" + ` (
  ` + "`template_id`" + ` TEXT NOT NULL,
  ` + "`owner`" + `       TEXT NOT NULL,
  ` + "`name`" + `        TEXT NOT NULL,
  PRIMARY KEY (` + "`template_id`" + `, ` + "`owner`" + `, ` + "`name`" + `)
)`,

	// schedule_write's own shape and reasoning, ported onto a template's
	// own id: content like template_read, carried onto every task filed
	// from this template (fireTaskSchedule, fireSuitePass,
	// CreateQualificationRun, orchestrator's review filing) the same way
	// its reads are.
	`CREATE TABLE IF NOT EXISTS ` + "`template_write`" + ` (
  ` + "`template_id`" + ` TEXT NOT NULL,
  ` + "`owner`" + `       TEXT NOT NULL,
  ` + "`name`" + `        TEXT NOT NULL,
  ` + "`base`" + `        TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (` + "`template_id`" + `, ` + "`owner`" + `, ` + "`name`" + `, ` + "`base`" + `)
)`,

	// schedule_grant's own shape, for a template's own capabilities.
	`CREATE TABLE IF NOT EXISTS ` + "`template_grant`" + ` (
  ` + "`template_id`" + ` TEXT NOT NULL,
  ` + "`capability`" + `  TEXT NOT NULL,
  ` + "`via`" + `         TEXT NOT NULL,
  ` + "`folder`" + `      TEXT NULL,
  PRIMARY KEY (` + "`template_id`" + `, ` + "`capability`" + `)
)`,

	// The inputs a template asks for when somebody instantiates it
	// (model.TemplateField, grain/task-58) -- the named holes its title
	// and body leave as `{{name}}`, each one either a text box or a set
	// of options to pick from.
	//
	// options is a JSON array for custom_field.options' own reason: an
	// option is a human-written label and a separator one may contain is
	// not a separator. default_value rather than `default` for that
	// table's own reason too -- DEFAULT is reserved in every engine this
	// SQL has to read the same in. position keeps the declared order,
	// which is the order a form asks for them in: "environment, then
	// version" is how somebody wrote the form down.
	//
	// Needs no SchemaVersion bump, for template_write's own reason: an
	// older store gains one empty table, which reads back as every
	// template asking for nothing -- exactly what those templates were
	// saved as.
	`CREATE TABLE IF NOT EXISTS ` + "`template_field`" + ` (
  ` + "`template_id`" + `   TEXT    NOT NULL,
  ` + "`name`" + `          TEXT    NOT NULL,
  ` + "`label`" + `         TEXT    NOT NULL,
  ` + "`options`" + `       TEXT    NOT NULL,
  ` + "`default_value`" + ` TEXT    NOT NULL,
  ` + "`required`" + `      INTEGER NOT NULL DEFAULT 0,
  ` + "`position`" + `      INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (` + "`template_id`" + `, ` + "`name`" + `)
)`,

	// schedule_custom_field's own shape and reasoning, ported onto a
	// template's own id: the values every task filed from this template
	// carries (Template.CustomFields, grain/task-105), whichever of the
	// four things that files one does it -- a schedule firing, a suite
	// pass, a qualification run, a review.
	`CREATE TABLE IF NOT EXISTS ` + "`template_custom_field`" + ` (
  ` + "`template_id`" + ` TEXT NOT NULL,
  ` + "`name`" + `        TEXT NOT NULL,
  ` + "`value`" + `       TEXT NOT NULL,
  PRIMARY KEY (` + "`template_id`" + `, ` + "`name`" + `, ` + "`value`" + `)
)`,

	// The fields this deployment adds to its own tasks -- settings, and
	// among the tables staterepo.Apply imports live, so a merged pull
	// request against tables/custom_field.json is a new field on the
	// next tick (model.CustomField's own doc comment).
	//
	// options is a JSON array rather than the comma-separated list
	// target_repos and default_capabilities use: an enum's choices are
	// human-written labels ("blocked, waiting on legal") and a separator
	// a value may legitimately contain is not a separator. type carries
	// the empty-means-text default as it was declared rather than
	// resolved, so a row reads back as it was written.
	//
	// default_value rather than `default`: DEFAULT is a reserved word in
	// every engine this SQL is written to read the same in, and a column
	// whose name only works because it is quoted is one a future join or
	// a hand-typed query gets wrong.
	//
	// multiple says the field holds a set of values rather than one
	// (model.CustomField.Multiple). A column beside the type rather than
	// a type of its own, for the reason that field's doc comment gives,
	// and defaulted to 0 so that every declaration written before it
	// existed reads back as the single-valued field it was -- which is
	// also what lets Store.ensureCustomFieldMultipleColumn add it to an
	// existing database with no version bump.
	//
	// show_on_row says this field's value belongs on the rows and cards
	// that list a task, not only in the task's own pane
	// (model.CustomField.ShowOnRow). Defaulted to 0 and added to an
	// existing database by Store.ensureCustomFieldShowOnRowColumn, for
	// the same reasons multiple above is.
	`CREATE TABLE IF NOT EXISTS ` + "`custom_field`" + ` (
  ` + "`name`" + `          TEXT    NOT NULL,
  ` + "`label`" + `         TEXT    NOT NULL,
  ` + "`type`" + `          TEXT    NOT NULL,
  ` + "`options`" + `       TEXT    NOT NULL,
  ` + "`description`" + `   TEXT    NOT NULL,
  ` + "`default_value`" + ` TEXT    NOT NULL,
  ` + "`position`" + `      INTEGER NOT NULL DEFAULT 0,
  ` + "`multiple`" + `      INTEGER NOT NULL DEFAULT 0,
  ` + "`show_on_row`" + `   INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (` + "`name`" + `)
)`,

	// A deployment's own agent API scope sets (model.APIScopeSet,
	// docs/agent-api.md) -- settings, among the tables staterepo.Apply
	// imports live, written by hand in a pull request against
	// tables/api_scope_set.json the way custom_field is. The lists are
	// JSON arrays for custom_field.options' reason. A row is a
	// definition, never a grant: nothing here is handed to any task.
	apiScopeSetTable,

	// GitHub's own CI on the repositories grain works in: one row per
	// GitHub Actions job or third-party check run grain saw conclude
	// (model.GitHubCIJob). grain's record of somebody else's CI, written
	// from webhooks and from check-run reads the pull request sync already
	// makes; never a verdict grain reuses.
	gitHubCIJobTable,

	`CREATE TABLE IF NOT EXISTS ` + "`grain_schema`" + ` (
  ` + "`id`" + `      INTEGER NOT NULL,
  ` + "`version`" + ` INTEGER NOT NULL,
  PRIMARY KEY (` + "`id`" + `)
)`,

	// A deployment's tunable configuration, non-secret but for
	// google_oauth_client_secret, which nothing writes any more and stage
	// 6 of docs/single-tenant-reset.md drops -- see config.go's own doc comment on
	// Config for what belongs here and what does not.
	// One row, like grain_schema: there is exactly one of these per
	// deployment, not one per something else, so there is nothing for a
	// second row to key against.
	//
	// target_repos was missing here from grain_config's very first
	// version (schema version 6, bwsalmon/agents#320) even though
	// Config.TargetRepos was already meant to be store-backed like every
	// other field: store.go's scanConfig/PutConfig never selected or
	// bound it, so a Settings change widening it was echoed back at
	// once but was never durable, silently reverting to unrestricted on
	// the very next read or restart -- bwsalmon/agents#427's actual root
	// cause, not merely a PAT-scope/credential-ladder mismatch. CREATE
	// TABLE IF NOT EXISTS does not add a column to a table that already
	// exists, so an already-created grain_config gets this one from
	// Store.Init's own migration step (store.go's
	// ensureConfigTargetReposColumn) instead of from this DDL.
	//
	// max_workers and max_mergers are the two halves of this deployment's
	// concurrency (model.Limits, grain/task-63): how many runs of
	// ordinary work may be live, and how much further capacity only the
	// merge queue's own fix tasks may reach.
	//
	// max_workers is the descendant of a slots column (a comma-separated
	// list of operator-chosen concurrency-slot names), which
	// bwsalmon/agents#461 replaced with a plain max_concurrent count, and
	// which grain/task-63 renamed once it stopped being the whole limit.
	// The same CREATE TABLE IF NOT EXISTS limitation applies to both
	// columns, so an already-created grain_config gets max_concurrent
	// added and backfilled from its old slots column by Store.Init's own
	// ensureConfigMaxConcurrentColumn, and then gets max_workers
	// (backfilled from max_concurrent, which is dropped) and max_mergers
	// from ensureConfigWorkerMergerColumns after it. max_mergers DEFAULT
	// 1 is model.DefaultMaxMergers, the same number DefaultConfig carries
	// for every row written from Go rather than defaulted by the engine.
	//
	// Both carry a DEFAULT, unlike the columns beside them that predate
	// the convention, and max_workers' own is what keeps a *downgrade*
	// survivable: a build from before the split writes a settings row
	// naming max_concurrent and not max_workers (its own migration
	// re-adds the column this one dropped), which a NOT NULL column with
	// no default would reject outright. The setting such a write leaves
	// behind is wrong until something sets it again; the alternative is a
	// deployment that cannot save settings at all.
	//
	// approved_by_default and auto_merge_by_default (Config.ApprovedByDefault/
	// AutoMergeByDefault) are DEFAULT 1, not 0: both settings are on for a
	// deployment that has never chosen either way, the same default
	// model.DefaultConfig carries for every row written from Go rather
	// than defaulted by the engine. task_defaults_on_backfilled is not a
	// setting at all but the ledger for that change of default: its
	// *presence* records that Store.ensureConfigTaskDefaultsOn has
	// already run against this database, so the one-time backfill it does
	// can never run twice and undo an operator who has since turned
	// either setting off. Its value is never read by anything -- PutConfig
	// does not name it, and REPLACE re-defaults it on every settings save,
	// so the value carries no information to read.
	//
	// default_capabilities is Config.DefaultCapabilities' own column --
	// the capability ids a new task is filed holding -- stored the same
	// comma-separated way target_repos is (store.go's joinCSV/splitCSV; a
	// capability id can no more contain a comma than an owner/name repo
	// can), DEFAULT '' both here and in
	// Store.ensureConfigDefaultCapabilitiesColumn for an already-created
	// grain_config. Empty, the default, means a new task starts with only
	// the capabilities whoever files it asked for -- exactly what every
	// deployment did before this column existed.
	//
	// environment_name is Config.EnvironmentName's own column -- what
	// this deployment is called on screen -- DEFAULT '' both here and in
	// Store.ensureConfigEnvironmentNameColumn for an already-created
	// grain_config. Empty is an unnamed deployment, which is what every
	// deployment was before this column existed.
	//
	// prompt_extension is Config.PromptExtension's own column -- the
	// standing instructions every dispatch's prompt ends with
	// (prompt_extension.go) -- DEFAULT '' both here and in
	// Store.ensureConfigPromptExtensionColumn for an already-created
	// grain_config. Empty adds nothing to a prompt, which is what every
	// deployment did before this column existed. TEXT with no length of
	// its own: it is prose for an agent, the same as task.body beside it.
	//
	// sandbox_backend is Config.SandboxBackend's own column -- which
	// isolation mechanism the next daemon start builds. DEFAULT '' both here
	// and in Store.Init's migration means an older deployment keeps the
	// backend its startup flags selected until somebody explicitly chooses.
	//
	// sandbox_image is Config.SandboxImage's own column -- the OCI
	// reference of this installation's custom sandbox image, built FROM
	// one of grain's minimal base images (pkg/sandboximage) -- DEFAULT ''
	// both here and in migration 44. Empty means the image this build is
	// stamped for on whichever backend is active. It replaced
	// sandbox_setup_script, the shell script the daemon used to derive a
	// local guest image from at dispatch time.
	//
	// package_cache_enabled is Config.PackageCacheEnabled's own column --
	// whether every sandbox's package managers are pointed at the controller's
	// package cache (pkg/pkgcache). DEFAULT 1 here and in
	// Store.Init's migration, so a deployment upgrading into it starts
	// caching without anybody having to find the switch; the same switch
	// turns it back off.
	//
	// actions_cache_enabled and actions_cache_size_gb are
	// Config.ActionsCacheEnabled and Config.ActionsCacheSizeGB's own
	// columns -- whether this deployment's CI jobs are handed the
	// controller's GitHub Actions cache (pkg/actionscache), and how much
	// disk that cache may fill. DEFAULT 1 and DEFAULT 0 here and in
	// Store.Init's migration: a deployment upgrading into the cache gets
	// it, for the reason package_cache_enabled above defaults on, and
	// gets it at grain's own size rather than at a bound nobody chose.
	//
	// The switch is worth finding before the first job runs on a
	// deployment that would rather not have it: every branch of a
	// repository shares one cache, which is the whole trust boundary and
	// is spelled out in Config.ActionsCacheEnabled and in
	// pkg/actionscache's package doc.
	//
	// time_zone is Config.TimeZone's own column -- the IANA zone this
	// deployment keeps its wall clock in, which is what a daily, weekly
	// or monthly schedule's time of day is read against and what the UI
	// prints every timestamp in. DEFAULT 'America/Los_Angeles'
	// (model.DefaultTimeZone) both here and in
	// Store.ensureConfigTimeZoneColumn for an already-created
	// grain_config: unlike every column above it, the default is not the
	// empty string, because a deployment upgrading across this column was
	// keeping its clock in UTC by accident rather than by choice, and
	// grain/task-368 is the decision that a deployment nobody has told
	// otherwise runs on Pacific time. Empty is still read as that default
	// (model.TimeZoneOrDefault) rather than as a zone of its own, so a
	// row written by some other path stays on a defensible clock.
	//
	// agent_git_name and agent_git_email are Config.AgentGitName/
	// AgentGitEmail's own columns -- the git identity every dispatched
	// run's sandbox commits under -- DEFAULT '' both here and in
	// Store.ensureConfigAgentGitIdentityColumns for an already-created
	// grain_config. Empty is not an unusable identity but "grain's own"
	// (mcp.DefaultGitIdentityName/DefaultGitIdentityEmail), which is
	// exactly what every deployment committed as before these columns
	// existed; the resolution happens where the .gitconfig is written
	// rather than on read here, for the reason Config.AgentGitName's own
	// doc comment gives.
	//
	// github_posting_credential is Config.GitHubPostingCredential's own
	// column -- the named credential grain's REST calls authenticate
	// with, and so the account its pull requests and comments are posted
	// as -- DEFAULT '' both here and in
	// Store.ensureConfigPostingCredentialColumn for an already-created
	// grain_config. Empty is not "no credential" but the credential
	// ladder's own per-repo answer, which is what every deployment
	// posted with before this column existed.
	//
	// upgrade_check_mode and upgrade_check_pauses are the two halves of
	// what happens after an in-place upgrade (Config.UpgradeCheckMode and
	// Config.UpgradeCheckPausesPipeline): whether grain files a task to
	// check the upgrade it just installed and whether such a task lands
	// approved, and whether the queue is held (pipeline_pause, below)
	// until that check finishes. Both are added to an already-created
	// grain_config by Store.ensureConfigUpgradeCheckColumns, which
	// carries the same defaults this DDL does -- 'auto', so a deployment
	// that has never chosen gets the check filed and queued, and 0, so it
	// gets it without the queue being held.
	//
	// network_access is Config.NetworkAccess's own column -- how much of
	// the internet a run's sandbox reaches when its task names nothing
	// (model.NetworkAccess, grain/task-269) -- DEFAULT '' both here and
	// in Store.ensureConfigNetworkAccessColumn for an already-created
	// grain_config. Empty is read as 'open'
	// (model.NormalizeNetworkAccess), which is what every sandbox this
	// deployment built before the column existed had. Unlike most of the
	// empty defaults beside it, that reading means something rather than
	// nothing -- see SettingsTables' own note on what a settings file
	// missing a column costs.
	//
	// console_sessions is Config.ConsoleSessions' own column -- the
	// standing interactive sessions this deployment keeps open, the
	// director among them (model.ConsoleSession, grain/task-415) --
	// stored as JSON rather than split across a table of its own, for
	// the reason model.EncodeConsoleSessions gives. DEFAULT '' both here
	// and in Store.ensureConfigConsoleSessionsColumn, and empty is the
	// third reading with meaning in it: "this deployment has never
	// chosen", which gets the director. A deployment that wants none
	// stores '[]', which is a different value and stays different.
	//
	// sandbox_cpu_overcommit, sandbox_memory_overcommit and
	// sandbox_memory_reserve_mb are the sandbox budget's own three
	// (Config's fields of the same names, model.Budget for what they
	// make together). DEFAULT 2, 1 and -1, which are
	// DefaultCPUOvercommit, DefaultMemoryOvercommit and
	// ReserveMemoryDefault spelled in SQL -- both here and in the
	// Store.ensureConfigColumn calls that add them to a grain_config
	// created before they existed, so a deployment upgrading across them
	// keeps being admitted against grain's own budget instead of the
	// zero ratio that would bound nothing. -1 for the reserve is "an
	// eighth of whatever machine this turns out to be", which is a
	// number no DDL can name.
	//
	// REAL for the two ratios: they are how much more than the host has
	// may be promised, and an operator writing 1.5 there is doing
	// something reasonable that an INTEGER column would silently round.
	//
	// sandbox_disk_floor_share and sandbox_disk_floor_min_mb are the
	// budget's third dimension, and columns since grain/task-372 for the
	// same reason (Config's fields of the same names, model.DiskFloor).
	// DEFAULT 0.1 and 5120, which are DefaultDiskFloorShare and
	// MinDiskFloorMB spelled in SQL -- both here and in the
	// Store.ensureConfigColumn calls that add them to a grain_config
	// created before they existed, so a deployment upgrading across them
	// goes on keeping a tenth of each volume free instead of the zero
	// that would hold nothing back as the disk fills. REAL for the
	// share, for the ratios' reason above: a tenth is not an integer.

	//
	// max_auto_submit_depth and max_auto_submit_fan_out are
	// Config.MaxAutoSubmitDepth/MaxAutoSubmitFanOut's own columns -- how
	// far a tree of proposals nobody approved may grow from the one
	// approval at the root of it (model.AutoSubmitLimits). DEFAULT 2 and
	// 5, which are DefaultAutoSubmitDepth and DefaultAutoSubmitFanOut
	// spelled in SQL, both here and in
	// Store.ensureConfigAutoSubmitColumns for an already-created
	// grain_config -- so a deployment upgrading across these columns
	// gets grain's own bounds rather than the zero that would otherwise
	// read back. Zero is "nobody chose" wherever it does turn up, for
	// the reason those fields' own doc comment gives.
	`CREATE TABLE IF NOT EXISTS ` + "`grain_config`" + ` (
  ` + "`id`" + `                         INTEGER NOT NULL,
  ` + "`poll_interval_ms`" + `           INTEGER NOT NULL,
  ` + "`max_workers`" + `                 INTEGER NOT NULL DEFAULT 1,
  ` + "`max_mergers`" + `                 INTEGER NOT NULL DEFAULT 1,
  ` + "`max_merges_per_repo`" + `         INTEGER NOT NULL DEFAULT 1,
  ` + "`merge_batch_size`" + `            INTEGER NOT NULL DEFAULT 3,
  ` + "`merge_gather_window_ms`" + `      INTEGER NOT NULL DEFAULT 0,
  ` + "`max_ci_jobs`" + `                 INTEGER NOT NULL DEFAULT 1,
  ` + "`max_agent_turns`" + `             INTEGER NOT NULL,
  ` + "`github_host`" + `                 TEXT    NOT NULL,
  ` + "`github_insecure_http`" + `        INTEGER NOT NULL,
  ` + "`gcp_project`" + `                 TEXT    NOT NULL,
  ` + "`gcp_service_account_email`" + `   TEXT    NOT NULL,
  ` + "`gcp_service_account_count`" + `   INTEGER NOT NULL DEFAULT 1,
  ` + "`newest_first`" + `                INTEGER NOT NULL DEFAULT 0,
  ` + "`sandbox_backend`" + `              TEXT    NOT NULL DEFAULT '',
  ` + "`sandbox_cpus`" + `                INTEGER NOT NULL DEFAULT 0,
  ` + "`sandbox_memory_mb`" + `            INTEGER NOT NULL DEFAULT 0,
  ` + "`sandbox_disk_gb`" + `              INTEGER NOT NULL DEFAULT 0,
  ` + "`sandbox_cpu_overcommit`" + `       REAL    NOT NULL DEFAULT 2,
  ` + "`sandbox_memory_overcommit`" + `    REAL    NOT NULL DEFAULT 1,
  ` + "`sandbox_memory_reserve_mb`" + `    INTEGER NOT NULL DEFAULT -1,
  ` + "`sandbox_disk_floor_share`" + `     REAL    NOT NULL DEFAULT 0.1,
  ` + "`sandbox_disk_floor_min_mb`" + `    INTEGER NOT NULL DEFAULT 5120,
  ` + "`show_closed_by_default`" + `       INTEGER NOT NULL DEFAULT 0,
  ` + "`default_filters`" + `               TEXT    NOT NULL DEFAULT 'f=state:none:deferred|closed&f=kind:none:interactive',
  ` + "`agent_backends`" + `               TEXT    NOT NULL DEFAULT '',
  ` + "`default_model`" + `                TEXT    NOT NULL DEFAULT '',
  ` + "`agent_backend_id`" + `             TEXT    NOT NULL DEFAULT '',
  ` + "`model_settings`" + `               TEXT    NOT NULL DEFAULT '',
  ` + "`git_credential`" + `               TEXT    NOT NULL DEFAULT '',
  ` + "`git_connections`" + `              TEXT    NOT NULL DEFAULT '',
  ` + "`approved_by_default`" + `          INTEGER NOT NULL DEFAULT 1,
  ` + "`auto_merge_by_default`" + `        INTEGER NOT NULL DEFAULT 1,
  ` + "`task_defaults_on_backfilled`" + `  INTEGER NOT NULL DEFAULT 1,
  ` + "`default_capabilities`" + `         TEXT    NOT NULL DEFAULT '',
  ` + "`environment_name`" + `             TEXT    NOT NULL DEFAULT '',
  ` + "`prompt_extension`" + `             TEXT    NOT NULL DEFAULT '',
  ` + "`task_branch_template`" + `          TEXT    NOT NULL DEFAULT '',
  ` + "`deployment_id`" + `                TEXT    NOT NULL DEFAULT '',
  ` + "`time_zone`" + `                    TEXT    NOT NULL DEFAULT 'America/Los_Angeles',
  ` + "`upgrade_check_mode`" + `           TEXT    NOT NULL DEFAULT 'auto',
  ` + "`upgrade_check_pauses`" + `         INTEGER NOT NULL DEFAULT 0,
  ` + "`network_access`" + `               TEXT    NOT NULL DEFAULT '',
  ` + "`console_sessions`" + `             TEXT    NOT NULL DEFAULT '',
  ` + "`sandbox_image`" + `                TEXT    NOT NULL DEFAULT '',
  ` + "`package_cache_enabled`" + `        INTEGER NOT NULL DEFAULT 1,
  ` + "`package_cache_size_gb`" + `        INTEGER NOT NULL DEFAULT 0,
  ` + "`actions_cache_enabled`" + `        INTEGER NOT NULL DEFAULT 1,
  ` + "`actions_cache_size_gb`" + `        INTEGER NOT NULL DEFAULT 0,
  ` + "`ci_run_history`" + `               INTEGER NOT NULL DEFAULT 0,
  ` + "`ci_step_output_mb`" + `            INTEGER NOT NULL DEFAULT 0,
  ` + "`ci_step_output_days`" + `          INTEGER NOT NULL DEFAULT 0,
  ` + "`ci_run_output_kb`" + `             INTEGER NOT NULL DEFAULT 0,
  ` + "`ci_cache_mb`" + `                  INTEGER NOT NULL DEFAULT 0,
  ` + "`ci_cache_entries`" + `             INTEGER NOT NULL DEFAULT 0,
  ` + "`ci_cache_days`" + `                INTEGER NOT NULL DEFAULT 0,
  ` + "`ci_artifact_mb`" + `               INTEGER NOT NULL DEFAULT 0,
  ` + "`ci_artifact_run_mb`" + `           INTEGER NOT NULL DEFAULT 0,
  ` + "`ci_artifact_file_mb`" + `          INTEGER NOT NULL DEFAULT 0,
  ` + "`ci_artifact_days`" + `             INTEGER NOT NULL DEFAULT 0,
  ` + "`ci_jobs_per_run`" + `              INTEGER NOT NULL DEFAULT 0,
  ` + "`ci_age_boost_ms`" + `              INTEGER NOT NULL DEFAULT 0,
  ` + "`ci_reserve_after_ms`" + `          INTEGER NOT NULL DEFAULT 0,
  ` + "`max_auto_submit_depth`" + `        INTEGER NOT NULL DEFAULT 2,
  ` + "`max_auto_submit_fan_out`" + `      INTEGER NOT NULL DEFAULT 5,
  ` + "`iap_backend_service`" + `          TEXT    NOT NULL DEFAULT '',
  ` + "`iap_resource`" + `                 TEXT    NOT NULL DEFAULT '',
  ` + "`repair_template_id`" + `          TEXT    NOT NULL DEFAULT '',
  ` + "`agent_framework`" + `              TEXT    NOT NULL DEFAULT '',
  ` + "`agent_git_name`" + `               TEXT    NOT NULL DEFAULT '',
  ` + "`agent_git_email`" + `              TEXT    NOT NULL DEFAULT '',
  ` + "`target_repos`" + `                 TEXT    NOT NULL DEFAULT '',
  ` + "`password_auth_enabled`" + `      INTEGER NOT NULL DEFAULT 1,
  ` + "`open_registration`" + `            INTEGER NOT NULL DEFAULT 0,
  ` + "`google_oauth_enabled`" + `         INTEGER NOT NULL DEFAULT 0,
  ` + "`google_oauth_client_id`" + `       TEXT    NOT NULL DEFAULT '',
  ` + "`google_oauth_client_secret`" + `   TEXT    NOT NULL DEFAULT '',
  ` + "`google_oauth_allowed_domains`" + ` TEXT    NOT NULL DEFAULT '',
  ` + "`k8s_namespace`" + `                TEXT    NOT NULL DEFAULT 'grain',
  ` + "`k8s_kubectl`" + `                  TEXT    NOT NULL DEFAULT '',
  ` + "`k8s_sandbox_image`" + `            TEXT    NOT NULL DEFAULT '',
  ` + "`k8s_granule_image`" + `            TEXT    NOT NULL DEFAULT '',
  ` + "`k8s_workspace`" + `                TEXT    NOT NULL DEFAULT '',
  ` + "`k8s_git_proxy_host`" + `           TEXT    NOT NULL DEFAULT '',
  ` + "`k8s_sandbox_service`" + `          TEXT    NOT NULL DEFAULT '',
  ` + "`k8s_service_account`" + `          TEXT    NOT NULL DEFAULT '',
  ` + "`k8s_node_selector`" + `            TEXT    NOT NULL DEFAULT '',
  ` + "`k8s_privileged`" + `               INTEGER NOT NULL DEFAULT 0,
  ` + "`k8s_network_policy`" + `           INTEGER NOT NULL DEFAULT 0,
  ` + "`k8s_deployment`" + `               TEXT    NOT NULL DEFAULT 'grain',
  ` + "`k8s_container`" + `                TEXT    NOT NULL DEFAULT 'grain',
  ` + "`mcp_servers`" + `                  TEXT    NOT NULL DEFAULT '',
  ` + "`capability_policy`" + `            TEXT    NOT NULL DEFAULT '',
  ` + "`cli_run`" + `                      TEXT    NOT NULL DEFAULT '',
  ` + "`sandbox_overcommit`" + `           TEXT    NOT NULL DEFAULT '',
  ` + "`base_ci_red_streak`" + `           INTEGER NOT NULL DEFAULT 0,
  ` + "`k8s_priority_class_interactive`" + ` TEXT NOT NULL DEFAULT 'grain-interactive',
  ` + "`k8s_priority_class_agent`" + `     TEXT    NOT NULL DEFAULT 'grain-agent',
  ` + "`k8s_priority_class_ci`" + `        TEXT    NOT NULL DEFAULT 'grain-ci',
  ` + "`sandbox_disk_overcommit`" + `      REAL    NOT NULL DEFAULT 2,
  ` + "`auto_upgrade_channel`" + `         TEXT    NOT NULL DEFAULT '',
  ` + "`auto_upgrade_time`" + `            TEXT    NOT NULL DEFAULT '',
  ` + "`judgement_model`" + `              TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (` + "`id`" + `)
)`,

	// The deployment-wide "pick up no new work" gate
	// (model.PipelinePause): at most one row, present exactly while this
	// deployment is paused, so "is it paused?" is the existence of a row
	// rather than a flag somebody has to remember to clear.
	//
	// A table rather than two more grain_config columns, even though the
	// policy that decides when grain pauses itself does live over there
	// (upgrade_check_pauses above). grain_config is a settings table
	// (staterepo.SettingsTables): a merged change to it is imported
	// wholesale into a running daemon, and the rule that keeps that safe
	// is that the daemon does not write settings for itself. This row the
	// daemon does write for itself -- it closes the gate the moment it
	// files an upgrade check, and opens it again when that check finishes
	// -- so it belongs on the other side of that line, where a pull
	// request merged mid-pause cannot resume a deployment nobody meant to
	// resume.
	//
	// until_task_id is the one task the pause does not apply to, empty
	// for a pause that applies to everything. It is a plain column and
	// not a foreign key, for the same reason nothing else here is one:
	// the row is grain's own note to itself, and a task id that no longer
	// resolves is a pause the reconciler lifts rather than a write it
	// refuses.
	`CREATE TABLE IF NOT EXISTS ` + "`pipeline_pause`" + ` (
  ` + "`id`" + `            INTEGER  NOT NULL,
  ` + "`paused_at`" + `     DATETIME NOT NULL,
  ` + "`reason`" + `        TEXT     NOT NULL,
  ` + "`actor_kind`" + `    TEXT     NOT NULL,
  ` + "`actor_id`" + `      TEXT     NOT NULL,
  ` + "`until_task_id`" + ` TEXT     NOT NULL,
  PRIMARY KEY (` + "`id`" + `)
)`,

	// What grain CI last made of a branch that changes merge *into*
	// (model.BaseBranchCI, grain/task-198) -- one row per (repo, branch),
	// replaced each time a merge starts a new run there.
	//
	// It is the compensating control for a merge queue that no longer
	// brings every branch up to date as the base moves: while a base
	// branch's own post-merge run is failing, nothing further merges into
	// it, and the repair that failure earns is named here so it is filed
	// once rather than every cycle.
	//
	// The confirm_* columns are the second opinion that decides whether a
	// failure is a broken branch or a flaky job (grain/task-202); they
	// are added to an older database by Store.Init's own migration, all
	// defaulted, so a row written before they existed reads back as a
	// failure nobody has asked about yet -- which is the state the
	// reconciler starts a confirmation from.
	//
	// Off SettingsTables, beside pipeline_pause and for its reason: the
	// daemon writes this for itself, and a settings import must not be
	// able to replace grain's own note about what is broken right now.
	// commit is a plain column and repair_task_id is not a foreign key,
	// for the reason nothing else here is one: the row is grain's note to
	// itself, and an id that no longer resolves is a block the reconciler
	// re-asks about rather than a write it refuses.
	`CREATE TABLE IF NOT EXISTS ` + "`base_branch_ci`" + ` (
  ` + "`owner`" + `          TEXT     NOT NULL,
  ` + "`name`" + `           TEXT     NOT NULL,
  ` + "`branch`" + `         TEXT     NOT NULL,
  ` + "`run_id`" + `         TEXT     NOT NULL,
  ` + "`commit`" + `         TEXT     NOT NULL,
  ` + "`started_at`" + `     DATETIME NOT NULL,
  ` + "`finished_at`" + `    DATETIME NULL,
  ` + "`attempts`" + `       INTEGER  NOT NULL DEFAULT 0,
  ` + "`conclusion`" + `     TEXT     NOT NULL DEFAULT '',
  ` + "`detail`" + `         TEXT     NOT NULL DEFAULT '',
  ` + "`merged_task_id`" + ` TEXT     NOT NULL DEFAULT '',
  ` + "`pending_task_id`" + ` TEXT    NOT NULL DEFAULT '',
  ` + "`repair_task_id`" + ` TEXT     NOT NULL DEFAULT '',
  ` + "`confirmation`" + `   TEXT     NOT NULL DEFAULT '',
  ` + "`confirm_run_id`" + ` TEXT     NOT NULL DEFAULT '',
  ` + "`confirm_jobs`" + `   TEXT     NOT NULL DEFAULT '',
  ` + "`confirm_attempts`" + ` INTEGER NOT NULL DEFAULT 0,
  ` + "`confirm_detail`" + ` TEXT     NOT NULL DEFAULT '',
  ` + "`covers`" + `         INTEGER  NOT NULL DEFAULT 0,
  ` + "`pending_merges`" + ` INTEGER  NOT NULL DEFAULT 0,
  ` + "`run_requested_at`" + ` DATETIME NULL,
  ` + "`run_requested_by`" + ` TEXT    NOT NULL DEFAULT '',
  ` + "`red_streak`" + `     INTEGER  NOT NULL DEFAULT 0,
  ` + "`red_streak_jobs`" + ` TEXT    NOT NULL DEFAULT '',
  ` + "`red_streak_since`" + ` TEXT   NOT NULL DEFAULT '',
  ` + "`red_streak_tip`" + ` TEXT     NOT NULL DEFAULT '',
  ` + "`last_green_commit`" + ` TEXT  NOT NULL DEFAULT '',
  ` + "`red_streak_woken`" + ` TEXT   NOT NULL DEFAULT '',
  PRIMARY KEY (` + "`owner`" + `, ` + "`name`" + `, ` + "`branch`" + `)
)`,

	// What grain has dispatched on a base branch after a merge, and for
	// which commit (model.BaseBranchDispatch, grain/task-409) -- one row
	// per (repo, branch), replaced each time a fresh dispatch starts.
	//
	// It is deliberately not part of base_branch_ci above, even though
	// the two watch the same branches: that row is the merge queue's
	// gate and this one is work that merely follows the branch, this
	// repository's content-addressed sandbox guest cache above all. A
	// table of its own is what keeps a failed cache warm from having any
	// path at all to the gate.
	//
	// Needs no SchemaVersion bump: a new table is created by Init's own
	// CREATE TABLE IF NOT EXISTS on every database, old and new alike,
	// and nothing that already exists changes shape. Off SettingsTables
	// for base_branch_ci's reason -- the daemon writes it for itself,
	// and an import must not be able to tell grain it has already
	// dispatched work it has not.
	`CREATE TABLE IF NOT EXISTS ` + "`base_branch_dispatch`" + ` (
  ` + "`owner`" + `       TEXT     NOT NULL,
  ` + "`name`" + `        TEXT     NOT NULL,
  ` + "`branch`" + `      TEXT     NOT NULL,
  ` + "`commit`" + `      TEXT     NOT NULL DEFAULT '',
  ` + "`jobs`" + `        TEXT     NOT NULL DEFAULT '',
  ` + "`run_id`" + `      TEXT     NOT NULL DEFAULT '',
  ` + "`started_at`" + `  DATETIME NOT NULL,
  ` + "`finished_at`" + ` DATETIME NULL,
  ` + "`conclusion`" + `  TEXT     NOT NULL DEFAULT '',
  ` + "`detail`" + `      TEXT     NOT NULL DEFAULT '',
  PRIMARY KEY (` + "`owner`" + `, ` + "`name`" + `, ` + "`branch`" + `)
)`,

	// The trusted release run grain last started on a base branch after a
	// merge, and for which commit (model.BaseBranchRelease,
	// grain/task-461) -- one row per (repo, branch), replaced each time a
	// fresh release starts.
	//
	// It is deliberately not part of base_branch_ci above, for the reason
	// base_branch_dispatch is not: that row is the merge queue's gate and
	// this one is the publishing, which is not a statement about the
	// tree. Until grain/task-461 they were the same row and a flaky
	// publisher held every merge in the repository. A table of its own is
	// what keeps a failed release from having any path at all to the
	// gate. It is not base_branch_dispatch either, because that holds one
	// run per branch and the jobs an operator names there are
	// deliberately not deduplicated against the release
	// (RepoConfig.PostMergeDispatchJobs).
	//
	// Needs no SchemaVersion bump: a new table is created by Init's own
	// CREATE TABLE IF NOT EXISTS on every database, old and new alike,
	// and nothing that already exists changes shape. Off SettingsTables
	// for base_branch_ci's reason -- the daemon writes it for itself, and
	// an import must not be able to tell grain it has already published a
	// tree it has not.
	// `failures` and `failing_since` are the unbroken run of releases that
	// published nothing (BaseBranchRelease.Failures, grain/task-511),
	// carried across the replace above because the replace is what
	// destroys the history a reader needs. Added to an older database by
	// Store.Init's own ensureColumn migration, which is why they need no
	// SchemaVersion bump either: a branch upgrading across them reads as
	// not failing, and the first release after the upgrade says whether
	// that is true. `attempts` (BaseBranchRelease.Attempts, grain/task-4432)
	// is added the same way, and reads as zero -- one attempt -- on a row
	// written before it.
	`CREATE TABLE IF NOT EXISTS ` + "`base_branch_release`" + ` (
  ` + "`owner`" + `         TEXT     NOT NULL,
  ` + "`name`" + `          TEXT     NOT NULL,
  ` + "`branch`" + `        TEXT     NOT NULL,
  ` + "`commit`" + `        TEXT     NOT NULL DEFAULT '',
  ` + "`run_id`" + `        TEXT     NOT NULL DEFAULT '',
  ` + "`started_at`" + `    DATETIME NOT NULL,
  ` + "`finished_at`" + `   DATETIME NULL,
  ` + "`conclusion`" + `    TEXT     NOT NULL DEFAULT '',
  ` + "`detail`" + `        TEXT     NOT NULL DEFAULT '',
  ` + "`failures`" + `      INTEGER  NOT NULL DEFAULT 0,
  ` + "`failing_since`" + ` DATETIME NULL,
  ` + "`attempts`" + `      INTEGER  NOT NULL DEFAULT 0,
  PRIMARY KEY (` + "`owner`" + `, ` + "`name`" + `, ` + "`branch`" + `)
)`,

	// The speculative merge the queue is testing for one base branch
	// (model.MergeBatch, grain/task-294): a throwaway ref holding the
	// base with several queued changes merged into it, and grain CI's
	// verdict about that one tree.
	//
	// One row per (repo, branch), because a branch has at most one batch
	// in flight and its members are held out of the ordinary merge path
	// while it runs. `members` is JSON rather than a delimited list
	// because a member is three fields including a commit; the rest is
	// the shape base_branch_ci above has, and it is off SettingsTables
	// for the same reason -- the daemon writes it for itself, and a
	// settings import landing mid-batch would either resurrect a batch
	// whose ref has moved or drop one whose members are being held.
	`CREATE TABLE IF NOT EXISTS ` + "`merge_batch`" + ` (
  ` + "`owner`" + `       TEXT     NOT NULL,
  ` + "`name`" + `        TEXT     NOT NULL,
  ` + "`branch`" + `      TEXT     NOT NULL,
  ` + "`ref`" + `         TEXT     NOT NULL,
  ` + "`base_commit`" + ` TEXT     NOT NULL,
  ` + "`commit`" + `      TEXT     NOT NULL,
  ` + "`members`" + `     TEXT     NOT NULL DEFAULT '',
  ` + "`run_id`" + `      TEXT     NOT NULL DEFAULT '',
  ` + "`started_at`" + `  DATETIME NOT NULL,
  ` + "`finished_at`" + ` DATETIME NULL,
  ` + "`conclusion`" + `  TEXT     NOT NULL DEFAULT '',
  ` + "`detail`" + `      TEXT     NOT NULL DEFAULT '',
  ` + "`splits`" + `      INTEGER  NOT NULL DEFAULT 0,
  ` + "`queued`" + `      INTEGER  NOT NULL DEFAULT 0,
  ` + "`recheck_of`" + `     TEXT     NOT NULL DEFAULT '',
  ` + "`recheck_detail`" + ` TEXT     NOT NULL DEFAULT '',
  ` + "`recheck_at`" + `     DATETIME NULL,
  PRIMARY KEY (` + "`owner`" + `, ` + "`name`" + `, ` + "`branch`" + `)
)`,

	// One firing of a schedule translated out of a workflow's own
	// `on: schedule` cron (model.ScheduledWorkflowRun, grain/task-351):
	// the run grain started for it, and the review that run is owed.
	//
	// One row per schedule, replaced by the next firing, because a
	// firing that has not been reviewed is what stops the next one
	// starting. schedule_id is a plain column rather than a foreign key
	// for base_branch_ci's reason: the row is grain's note to itself,
	// and a schedule whose declaration has gone since it fired still
	// owes a report on the run it started.
	//
	// Off SettingsTables, beside base_branch_ci: the daemon writes it
	// for itself between one cycle and the next, and a settings import
	// landing in the window between a run and its review would either
	// resurrect a firing already reviewed or drop one still going.
	`CREATE TABLE IF NOT EXISTS ` + "`scheduled_workflow_run`" + ` (
  ` + "`schedule_id`" + `    TEXT     NOT NULL,
  ` + "`stem`" + `           TEXT     NOT NULL DEFAULT '',
  ` + "`owner`" + `          TEXT     NOT NULL DEFAULT '',
  ` + "`name`" + `           TEXT     NOT NULL DEFAULT '',
  ` + "`branch`" + `         TEXT     NOT NULL DEFAULT '',
  ` + "`workflow`" + `       TEXT     NOT NULL DEFAULT '',
  ` + "`run_id`" + `         TEXT     NOT NULL DEFAULT '',
  ` + "`fired_at`" + `       DATETIME NOT NULL,
  ` + "`attempted_at`" + `   DATETIME NULL,
  ` + "`finished_at`" + `    DATETIME NULL,
  ` + "`attempts`" + `       INTEGER  NOT NULL DEFAULT 0,
  ` + "`conclusion`" + `     TEXT     NOT NULL DEFAULT '',
  ` + "`detail`" + `         TEXT     NOT NULL DEFAULT '',
  ` + "`unfinished`" + `     INTEGER  NOT NULL DEFAULT 0,
  ` + "`review_task_id`" + ` TEXT     NOT NULL DEFAULT '',
  ` + "`review_not_needed`" + ` INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (` + "`schedule_id`" + `)
)`,

	// What every batch that has already ended cost and saved
	// (model.MergeBatchRecord, grain/task-300). The table above holds
	// only the batch in flight and is deleted the moment it ends, so
	// without this a deployment has no way of answering the question its
	// own batch size is a guess at: are batches passing first time, or
	// are they failing and bisecting?
	//
	// One row per finished batch run, bounded to MergeBatchHistoryKept by
	// the insert itself, and no member list -- who was in a batch is on
	// the members' own tasks. Off SettingsTables for merge_batch's reason:
	// the daemon writes it for itself, and nothing about a batch that has
	// already happened is a thing to propose in a pull request.
	//
	// reason is model.MergeBatchRecord.Reason, why an abandoned batch
	// produced no answer -- last because schema version 39 added it with
	// ALTER TABLE, and '' on every row written before, which counts as
	// model.AbandonUnrecorded.
	`CREATE TABLE IF NOT EXISTS ` + "`merge_batch_history`" + ` (
  ` + "`id`" + `          INTEGER PRIMARY KEY AUTOINCREMENT,
  ` + "`owner`" + `       TEXT     NOT NULL,
  ` + "`name`" + `        TEXT     NOT NULL,
  ` + "`branch`" + `      TEXT     NOT NULL,
  ` + "`commit`" + `      TEXT     NOT NULL DEFAULT '',
  ` + "`run_id`" + `      TEXT     NOT NULL DEFAULT '',
  ` + "`size`" + `        INTEGER  NOT NULL DEFAULT 0,
  ` + "`queued`" + `      INTEGER  NOT NULL DEFAULT 0,
  ` + "`splits`" + `      INTEGER  NOT NULL DEFAULT 0,
  ` + "`merged`" + `      INTEGER  NOT NULL DEFAULT 0,
  ` + "`outcome`" + `     TEXT     NOT NULL DEFAULT '',
  ` + "`detail`" + `      TEXT     NOT NULL DEFAULT '',
  ` + "`post_merge`" + `  INTEGER  NOT NULL DEFAULT 0,
  ` + "`started_at`" + `  DATETIME NOT NULL,
  ` + "`finished_at`" + ` DATETIME NOT NULL,
  ` + "`reason`" + `      TEXT     NOT NULL DEFAULT ''
)`,

	// And how many changes each red stretch of a base branch held in
	// base_broken, and for how long (model.BaseHoldPass, grain/task-986).
	// One row per pass, updated each cycle while it is open (finished_at
	// NULL) and bounded to BaseHoldHistoryKept finished rows when one
	// closes. held_tasks is the distinct task ids held, comma-separated,
	// which is what makes total a count of changes rather than of
	// samples. Off SettingsTables for merge_batch_history's reason.
	`CREATE TABLE IF NOT EXISTS ` + "`base_hold_history`" + ` (
  ` + "`id`" + `             INTEGER PRIMARY KEY AUTOINCREMENT,
  ` + "`owner`" + `          TEXT     NOT NULL,
  ` + "`name`" + `           TEXT     NOT NULL,
  ` + "`branch`" + `         TEXT     NOT NULL,
  ` + "`repair_task_id`" + ` TEXT     NOT NULL DEFAULT '',
  ` + "`started_at`" + `     DATETIME NOT NULL,
  ` + "`sampled_at`" + `     DATETIME NOT NULL,
  ` + "`finished_at`" + `    DATETIME NULL,
  ` + "`held`" + `           INTEGER  NOT NULL DEFAULT 0,
  ` + "`peak`" + `           INTEGER  NOT NULL DEFAULT 0,
  ` + "`total`" + `          INTEGER  NOT NULL DEFAULT 0,
  ` + "`held_tasks`" + `     TEXT     NOT NULL DEFAULT '',
  ` + "`pr_seconds`" + `     INTEGER  NOT NULL DEFAULT 0
)`,

	// And what every gather hold that has already ended did
	// (model.MergeGatherRecord, grain/task-440). The hold itself is
	// process state in the daemon and its only visible trace was the
	// `gathering` row on the held task, overwritten the moment the hold
	// ended -- so the one branch of the window's tuning rule that is
	// about holds rather than about batches ("shorten it if they
	// routinely expire") had nothing to read at all.
	//
	// One row per ended hold, bounded to MergeGatherHistoryKept by the
	// insert itself, and `window_ms` recorded per row because the whole
	// point of reading these is to consider changing that window. Off
	// SettingsTables for merge_batch_history's reason: a wait that has
	// happened is a measurement, not a decision.
	`CREATE TABLE IF NOT EXISTS ` + "`merge_gather_history`" + ` (
  ` + "`id`" + `        INTEGER PRIMARY KEY AUTOINCREMENT,
  ` + "`owner`" + `     TEXT     NOT NULL,
  ` + "`name`" + `      TEXT     NOT NULL,
  ` + "`number`" + `    INTEGER  NOT NULL DEFAULT 0,
  ` + "`waiting`" + `   TEXT     NOT NULL DEFAULT '',
  ` + "`window_ms`" + ` INTEGER  NOT NULL DEFAULT 0,
  ` + "`outcome`" + `   TEXT     NOT NULL DEFAULT '',
  ` + "`armed_at`" + `  DATETIME NOT NULL,
  ` + "`ended_at`" + `  DATETIME NOT NULL
)`,

	// And what the merge queue's overlap rule said about every merge
	// (model.MergeRecheckRecord, grain/task-985): whether the change
	// would be (in enforce mode, was) re-checked on top of its branch,
	// which clause fired, whether a re-check caught it, and what the
	// branch's own CI next made of it. Bounded to MergeRecheckHistoryKept
	// by the insert itself. Off SettingsTables for merge_batch_history's
	// reason: a decision that has been made is a measurement.
	`CREATE TABLE IF NOT EXISTS ` + "`merge_recheck_history`" + ` (
  ` + "`id`" + `           INTEGER PRIMARY KEY AUTOINCREMENT,
  ` + "`owner`" + `        TEXT     NOT NULL,
  ` + "`name`" + `         TEXT     NOT NULL,
  ` + "`branch`" + `       TEXT     NOT NULL DEFAULT '',
  ` + "`number`" + `       INTEGER  NOT NULL DEFAULT 0,
  ` + "`task_id`" + `      TEXT     NOT NULL DEFAULT '',
  ` + "`mode`" + `         TEXT     NOT NULL DEFAULT '',
  ` + "`head`" + `         TEXT     NOT NULL DEFAULT '',
  ` + "`fork`" + `         TEXT     NOT NULL DEFAULT '',
  ` + "`tip`" + `          TEXT     NOT NULL DEFAULT '',
  ` + "`triggered`" + `    INTEGER  NOT NULL DEFAULT 0,
  ` + "`clauses`" + `      TEXT     NOT NULL DEFAULT '',
  ` + "`detail`" + `       TEXT     NOT NULL DEFAULT '',
  ` + "`rechecked`" + `    INTEGER  NOT NULL DEFAULT 0,
  ` + "`caught`" + `       INTEGER  NOT NULL DEFAULT 0,
  ` + "`base_verdict`" + ` TEXT     NOT NULL DEFAULT '',
  ` + "`decided_at`" + `   DATETIME NOT NULL
)`,

	// Every occurrence of a schedule that came due and filed nothing
	// (model.ScheduleSkip, grain/task-3560): one row per occurrence, kept
	// to ScheduleSkipsKept per schedule by the insert itself. Off
	// SettingsTables for merge_batch_history's reason: a firing that was
	// skipped is a record of what happened, not a decision.
	`CREATE TABLE IF NOT EXISTS ` + "`scheduled_firing_skip`" + ` (
  ` + "`id`" + `            INTEGER PRIMARY KEY AUTOINCREMENT,
  ` + "`schedule_id`" + `   TEXT     NOT NULL,
  ` + "`occurrence`" + `    DATETIME NOT NULL,
  ` + "`at`" + `            DATETIME NOT NULL,
  ` + "`reason`" + `        TEXT     NOT NULL DEFAULT '',
  ` + "`task_id`" + `       TEXT     NOT NULL DEFAULT '',
  ` + "`task_state`" + `    TEXT     NOT NULL DEFAULT '',
  ` + "`detail`" + `        TEXT     NOT NULL DEFAULT '',
  ` + "`outcome`" + `       TEXT     NOT NULL DEFAULT '',
  ` + "`resolved_at`" + `   DATETIME,
  ` + "`fired_task_id`" + ` TEXT     NOT NULL DEFAULT '',
  ` + "`folded`" + `        INTEGER  NOT NULL DEFAULT 0,
  ` + "`noted_task_id`" + ` TEXT     NOT NULL DEFAULT ''
)`,

	// What the merge queue had to do over again because something else
	// moved -- a base merged into a stale head, a suite re-run
	// (model.MergeQueueContentionEvent, grain/task-3227). One row per
	// event, bounded to MergeQueueContentionKept by the insert itself.
	// Off SettingsTables for merge_batch_history's reason: an event that
	// has happened is a measurement, not a decision.
	`CREATE TABLE IF NOT EXISTS ` + "`merge_queue_contention`" + ` (
  ` + "`id`" + `      INTEGER PRIMARY KEY AUTOINCREMENT,
  ` + "`owner`" + `   TEXT     NOT NULL,
  ` + "`name`" + `    TEXT     NOT NULL,
  ` + "`task_id`" + ` TEXT     NOT NULL DEFAULT '',
  ` + "`kind`" + `    TEXT     NOT NULL DEFAULT '',
  ` + "`reason`" + `  TEXT     NOT NULL DEFAULT '',
  ` + "`at`" + `      DATETIME NOT NULL
)`,

	// Every merge grain performed on an operator's override rather than
	// on the predicate's say-so (model.ForceMergeRecord, grain/task-479).
	// The override itself is a stamp on the task's own observation, spent
	// the moment the merge happens; this is what is left afterwards, and
	// it is the only thing that makes "forcing should be rare" a claim
	// anybody can check.
	//
	// One row per forced merge that actually happened, bounded to
	// ForceMergeHistoryKept by the insert itself. Off SettingsTables for
	// merge_batch_history's reason and more plainly still: an override
	// somebody already used is a measurement, and nothing about it is a
	// thing a pull request against this repository would propose.
	`CREATE TABLE IF NOT EXISTS ` + "`force_merge_history`" + ` (
  ` + "`id`" + `           INTEGER PRIMARY KEY AUTOINCREMENT,
  ` + "`task_id`" + `      TEXT     NOT NULL,
  ` + "`owner`" + `        TEXT     NOT NULL,
  ` + "`name`" + `         TEXT     NOT NULL,
  ` + "`number`" + `       INTEGER  NOT NULL DEFAULT 0,
  ` + "`branch`" + `       TEXT     NOT NULL DEFAULT '',
  ` + "`commit`" + `       TEXT     NOT NULL DEFAULT '',
  ` + "`reason`" + `       TEXT     NOT NULL DEFAULT '',
  ` + "`requested_by`" + ` TEXT     NOT NULL DEFAULT '',
  ` + "`skipped`" + `      TEXT     NOT NULL DEFAULT '',
  ` + "`requested_at`" + ` DATETIME NOT NULL,
  ` + "`merged_at`" + `    DATETIME NOT NULL
)`,

	// An upgrade asked for but not started yet
	// (model.ScheduledUpgrade): at most one row, present exactly while
	// this deployment is holding the queue and waiting for the work it
	// already has to finish before it cuts over.
	//
	// A table beside pipeline_pause above, for the same reason and with
	// the same singleton shape. The two rows are written together and
	// read together -- the schedule is why the pause exists, and the
	// pause is what makes the schedule ever come true -- but they are
	// separate because either can outlive the other: an operator may
	// pause a deployment that has no upgrade scheduled, and the instant
	// a scheduled upgrade actually starts, its row goes and the pause
	// stays, now held by the upgrade itself.
	//
	// It is off SettingsTables like pipeline_pause: the daemon writes it
	// for itself, and a settings import landing between the schedule
	// being made and the upgrade starting must not reinstate an upgrade
	// that has already happened.
	`CREATE TABLE IF NOT EXISTS ` + "`scheduled_upgrade`" + ` (
  ` + "`id`" + `           INTEGER  NOT NULL,
  ` + "`branch`" + `       TEXT     NOT NULL,
  ` + "`requested_at`" + ` DATETIME NOT NULL,
  ` + "`actor_kind`" + `   TEXT     NOT NULL,
  ` + "`actor_id`" + `     TEXT     NOT NULL,
  PRIMARY KEY (` + "`id`" + `)
)`,

	// The last day the daily automatic upgrade fired
	// (model.AutoUpgradeRun): at most one row, written by the reconciler
	// that schedules it, so that a deployment the upgrade restarted does
	// not schedule the same day's upgrade all over again.
	//
	// A table of its own rather than a grain_config column because it is
	// grain's note to itself: off SettingsTables for scheduled_upgrade's
	// reason, and out of the whole-row write a settings save makes, which
	// would otherwise carry a value read before the firing back over it.
	`CREATE TABLE IF NOT EXISTS ` + "`auto_upgrade_run`" + ` (
  ` + "`id`" + `       INTEGER  NOT NULL,
  ` + "`day`" + `      TEXT     NOT NULL,
  ` + "`branch`" + `   TEXT     NOT NULL,
  ` + "`fired_at`" + ` DATETIME NOT NULL,
  PRIMARY KEY (` + "`id`" + `)
)`,

	// The usage-limit pauses in force (model.StoredUsageLimitPause): one
	// row per window orchestrator.Pause holds, written as it opens and
	// deleted as it expires or is lifted, so a restarted daemon keeps
	// passing over credentials its predecessor found spent. Off
	// SettingsTables: a provider's refusal as this deployment met it is
	// runtime state, not configuration.
	usageLimitPauseTable,

	// Structured tracking for system tuning recommendations (model.TuningRecommendation):
	// records target parameter, proposed value, cooldown expiry, whether the recommendation
	// was adopted, and origin task id. Off SettingsTables: operational tuning state, not a settings table.
	`CREATE TABLE IF NOT EXISTS ` + "`tuning_recommendation`" + ` (
  ` + "`id`" + `               INTEGER PRIMARY KEY AUTOINCREMENT,
  ` + "`target_parameter`" + ` TEXT     NOT NULL,
  ` + "`proposed_value`" + `   TEXT     NOT NULL,
  ` + "`cooldown_expiry`" + `  DATETIME NOT NULL,
  ` + "`adopted`" + `          INTEGER  NOT NULL DEFAULT 0,
  ` + "`task_id`" + `          TEXT     NOT NULL DEFAULT '',
  ` + "`created_at`" + `       DATETIME NOT NULL
)`,

	// A repo's release settings -- bwsalmon/agents#398's prod/rc branch
	// One named release branch set (bwsalmon/agents#571) -- a repo may
	// have any number of these at once, each with its own name and its own
	// independent candidate sequence, unlike bwsalmon/agents#398's own
	// release_config, which this table replaces and which held exactly one
	// singleton row per repo. INTEGER PRIMARY KEY AUTOINCREMENT for the
	// same reason release_candidate's own id is: starting a fresh release
	// under a name that already merged has to stay correct with more than
	// one writer. `repo` is the target repo's own name, kept apart from
	// `name` -- the release's own user-given name -- since a release is no
	// longer 1:1 with a repo the way release_config's row was. status is
	// TEXT rather than a constrained type for the reason schema.go's own
	// doc comment gives for every other enum column here: the vocabulary
	// lives in release.go, not the schema. last_error is the reconciler's
	// own report of why provisioning or a requested merge has not landed
	// yet -- cleared the moment the attempt that follows succeeds.
	// merge_note is the opposite of an error and is why it is not kept in
	// last_error: a release that reached `merged` with no pull request of
	// its own, because its prod branch carried nothing the default branch
	// did not already have, says so here (Release.MergeNote).
	`CREATE TABLE IF NOT EXISTS ` + "`release`" + ` (
  ` + "`id`" + `                INTEGER PRIMARY KEY AUTOINCREMENT,
  ` + "`owner`" + `             TEXT     NOT NULL,
  ` + "`repo`" + `              TEXT     NOT NULL,
  ` + "`name`" + `              TEXT     NOT NULL,
  ` + "`status`" + `            TEXT     NOT NULL,
  ` + "`created_at`" + `        DATETIME NOT NULL,
  ` + "`merged_at`" + `         DATETIME NULL,
  ` + "`pull_request_url`" + `  TEXT     NULL,
  ` + "`last_error`" + `        TEXT     NULL,
  ` + "`merge_note`" + `        TEXT     NULL
)`,

	// What GetRelease and ListReleases both need: every release for one
	// repo, or by one repo and name, newest first.
	`CREATE INDEX IF NOT EXISTS ` + "`release_repo`" + ` ON ` + "`release`" + ` (` + "`owner`" + `, ` + "`repo`" + `, ` + "`name`" + `, ` + "`id`" + `)`,

	// One cut release candidate. INTEGER PRIMARY KEY AUTOINCREMENT for the
	// same reason task_sequence uses it: cutting a new candidate has to
	// stay correct with more than one writer, and letting SQLite assign
	// the rowid is atomic where a read-then-increment column would race.
	//
	// release_id names the release (above) this candidate belongs to;
	// number is scoped to that release alone, starting back at 1 for every
	// fresh release, unlike bwsalmon/agents#398's own candidate number,
	// which never reset for the life of a repo's single release_config.
	// `owner`/`repo` duplicate the release's own target repo, the same
	// "every repo, oldest first" shape PendingCandidates and
	// QualifiableActiveCandidates both need without a join back to
	// `release` for it. There is no release_branch column the way
	// bwsalmon/agents#398's own release_candidate had: promoting moves the
	// release's own prod branch (release.name, unadorned) forward, and
	// that is the only permanent branch a promotion touches now.
	`CREATE TABLE IF NOT EXISTS ` + "`release_candidate`" + ` (
  ` + "`id`" + `             INTEGER PRIMARY KEY AUTOINCREMENT,
  ` + "`release_id`" + `     INTEGER  NOT NULL,
  ` + "`owner`" + `          TEXT     NOT NULL,
  ` + "`repo`" + `           TEXT     NOT NULL,
  ` + "`number`" + `         INTEGER  NOT NULL,
  ` + "`branch`" + `         TEXT     NOT NULL,
  ` + "`status`" + `         TEXT     NOT NULL,
  ` + "`created_at`" + `     DATETIME NOT NULL,
  ` + "`promoted_at`" + `    DATETIME NULL,
  ` + "`last_error`" + `     TEXT     NULL
)`,

	// What CurrentCandidateForRelease and the releases reconciler both
	// need: every candidate for one release, newest first.
	`CREATE INDEX IF NOT EXISTS ` + "`release_candidate_release`" + ` ON ` + "`release_candidate`" + ` (` + "`release_id`" + `, ` + "`id`" + `)`,

	// One requested branch (bwsalmon/agents#638) -- a repo page's own
	// "create a new branch," recorded here as a declared intent the same
	// way `release` and `release_candidate` are: `status` walks pending ->
	// created, TEXT rather than a constrained type for the same reason
	// every other enum column here is (the vocabulary lives in branch.go,
	// not the schema); `last_error` is the branches reconciler's own
	// report of why a create has not landed yet, cleared the moment it
	// succeeds. There is no uniqueness constraint on (`owner`,`repo`,
	// `name`): whether a name collides with something already on GitHub is
	// a fact only GitHub itself holds, so a collision only ever surfaces
	// as `last_error`, not as a rejected insert. `from_branch` is the
	// branch a create cuts from (grain/task-2317), '' for the repo's
	// default branch.
	`CREATE TABLE IF NOT EXISTS ` + "`branch`" + ` (
  ` + "`id`" + `           INTEGER PRIMARY KEY AUTOINCREMENT,
  ` + "`owner`" + `        TEXT     NOT NULL,
  ` + "`repo`" + `         TEXT     NOT NULL,
  ` + "`name`" + `         TEXT     NOT NULL,
  ` + "`from_branch`" + `  TEXT     NOT NULL DEFAULT '',
  ` + "`status`" + `       TEXT     NOT NULL,
  ` + "`created_at`" + `   DATETIME NOT NULL,
  ` + "`last_error`" + `   TEXT     NULL
)`,

	// What the repo page's own branch list needs: every branch ever
	// requested for one repo, newest first.
	`CREATE INDEX IF NOT EXISTS ` + "`branch_repo`" + ` ON ` + "`branch`" + ` (` + "`owner`" + `, ` + "`repo`" + `, ` + "`id`" + `)`,

	// A repo's own configuration -- model.RepoConfig, the per-repo layer
	// of what grain_config already says for the whole deployment
	// (grain/task-24). One row per repo, the same (owner, name) key
	// qualification_config below uses, and a row exists only while the
	// repo has something of its own to say: PutRepoConfig deletes rather
	// than writing a row that says nothing.
	//
	// default_capabilities is stored the comma-separated way
	// grain_config.default_capabilities and target_repos already are
	// (store.go's joinCSV/splitCSV), for the same reason -- a capability
	// id is a bare word with no room for a comma in it. No DEFAULT and no
	// migration: that column is as old as this table, so Init's own
	// CREATE TABLE IF NOT EXISTS creates the pair together on an existing
	// database the same as on a fresh one, and a deployment that upgrades
	// across it starts with no repo saying anything -- exactly what every
	// deployment did before it existed.
	//
	// prompt_extension is RepoConfig.PromptExtension's own column -- this
	// repo's standing instructions, appended to the deployment's for a
	// task that targets it (prompt_extension.go). Unlike
	// default_capabilities it *is* newer than the table, so it carries a
	// DEFAULT '' and Store.ensureRepoConfigPromptExtensionColumn adds it
	// to a repo_config that already exists.
	//
	// setup_command is RepoConfig.SetupCommand's own column -- the shell
	// orchestrator.prepareCheckout runs in the fresh checkout before a
	// run's first turn (grain/task-154). Newer than the table again, so
	// it carries the same DEFAULT '' and the same
	// Store.ensureRepoConfigSetupCommandColumn migration alongside it.
	//
	// work_ownership and work_overrides are RepoConfig.Work's two
	// columns -- whether this deployment runs this repository's standing
	// work, and the per-item exceptions to that (grain/task-486).
	// work_ownership holds '', 'on' or 'off' and DEFAULT '' is "nobody
	// has said", which resolves as "this deployment runs it": that is
	// what every deployment did before the column existed, and an
	// upgrade that changed it would stop every release and every
	// schedule everywhere at once. work_overrides holds `item=on|off`
	// pairs, comma-separated and sorted (model.EncodeWorkOverrides),
	// DEFAULT '' because no repository has an exception until somebody
	// writes one. Both added in place by Store.Init.
	//
	// release_workflows_disabled is what work_ownership replaced: one
	// negative bool, scoped to releases, inverted so its DEFAULT 0
	// preserved the release event repositories received before the
	// switch existed. It is still read, once, by a row written before
	// the two columns above it existed -- a set bool there becomes a
	// `release=off` override -- and still written, as a mirror of what
	// the release item resolves to, so that rolling this deployment back
	// to a binary that reads only this column does not silently start
	// publishing from a repository somebody switched off. Nothing reads
	// it to decide anything; RunsWorkItem does that.
	//
	// release_secrets stores names only; the corresponding values remain in
	// pkg/secrets' encrypted file and never enter this settings table. Both
	// are added in place by Store.Init like the other additive columns.
	//
	// require_branch_up_to_date is RepoConfig.RequireBranchUpToDate's own
	// column -- whether the merge queue insists a branch contain its base
	// before it merges (grain/task-198). DEFAULT 0 is the behaviour every
	// deployment gets without asking, and it is deliberately the *cheap*
	// one: a repository that wants a branch re-tested every time its base
	// moves turns this on, rather than every repository paying for it.
	// Added in place by Store.ensureRepoConfigRequireBranchUpToDateColumn
	// like the columns above it.
	//
	// watch_default_branch is RepoConfig.WatchDefaultBranch's own column
	// -- whether grain keeps a verdict about this repository's default
	// branch current without ever having merged into it
	// (grain/task-331). DEFAULT 0 because the first cycle after it is
	// turned on spends a full suite, and holds the repository's queue if
	// that suite is red: a bill of that shape is an operator's decision
	// per repository rather than something a deployment upgrade hands
	// out. Added in place by
	// Store.ensureRepoConfigWatchDefaultBranchColumn like the columns
	// around it.
	//
	// unfixable_checks is RepoConfig.UnfixableChecks' own column -- the
	// checks a branch here can never turn green, a CLA bot and its kind
	// (grain/task-291). A comma-separated list like the capability
	// columns above, DEFAULT '' because a repository that names none is
	// every repository until somebody says otherwise, and added in place
	// by Store.ensureRepoConfigUnfixableChecksColumn.
	//
	// base_branch_ci_jobs is RepoConfig.BaseBranchCIJobs' own column --
	// the jobs that judge a base branch for a repository whose releases
	// are switched off (grain/task-330), comma-separated like the
	// capability columns and DEFAULT '' because naming none is where
	// every repository starts: the release event judged the branch until
	// somebody turned releases off, and then nothing did. Added in place
	// by Store.ensureRepoConfigBaseBranchCIJobsColumn.
	//
	// post_merge_dispatch_jobs is RepoConfig.PostMergeDispatchJobs' own
	// column -- the jobs grain dispatches on a base branch after a merge
	// whatever that repository's releases are doing (grain/task-409),
	// comma-separated and DEFAULT '' for base_branch_ci_jobs' reason:
	// naming none is where every repository starts, and a repository
	// that names none is one nothing extra is dispatched on. Added in
	// place by Store.ensureRepoConfigPostMergeDispatchJobsColumn.
	//
	// declared_prompt_extension and declared_setup_command are the two
	// settings a *repository* may write for itself, kept in columns of
	// their own so that the declaration sync never overwrites the
	// operator's own two boxes beside them
	// (RepoConfig.DeclaredPromptExtension has the whole argument,
	// grain/task-340). DEFAULT '' because a repository that declares
	// nothing is every repository, and added in place by
	// Store.ensureRepoConfigDeclaredColumns.
	//
	// port_branches and declared_port_branches are
	// RepoConfig.PortBranches and DeclaredPortBranches: the branches whose
	// merges are ported to one another (orchestrator's port.go), the
	// operator's and the repository's, comma-separated. DEFAULT '' is a
	// repository that ports nothing, which is every repository until one
	// says otherwise.
	//
	// port_types and declared_port_types are RepoConfig.PortTypes and
	// DeclaredPortTypes: per direction, the conventional-commit types a
	// merge must carry to be ported that way (EncodePortTypes). DEFAULT ''
	// ports every merge, which is what every direction did before.
	//
	// git_credential is RepoConfig.GitCredential: the credential this
	// repository is cloned and pushed with ahead of the deployment's git
	// connections. It was team_repo_config's until schema 23 folded that
	// table in; DEFAULT '' is a repository the connections answer for.
	//
	// merge_on_green is RepoConfig.MergeOnGreen: whether the merge queue
	// merges a green, clean pull request here without waiting behind
	// entries whose CI is still running (grain/task-545, schema 36).
	// DEFAULT 0 is the strict lane every repository had before.
	//
	// merge_recheck is RepoConfig.MergeRecheck: off, observe or enforce
	// for the merge queue's overlap re-check (grain/task-985, schema 41).
	// DEFAULT '' reads as observe.
	`CREATE TABLE IF NOT EXISTS ` + "`repo_config`" + ` (
  ` + "`owner`" + `                TEXT NOT NULL,
  ` + "`name`" + `                 TEXT NOT NULL,
  ` + "`clone_url`" + `            TEXT NOT NULL DEFAULT '',
  ` + "`default_capabilities`" + ` TEXT NOT NULL,
  ` + "`prompt_extension`" + `     TEXT NOT NULL DEFAULT '',
  ` + "`setup_command`" + `        TEXT NOT NULL DEFAULT '',
  ` + "`ci_capabilities`" + `      TEXT NOT NULL DEFAULT '',
  ` + "`release_workflows_disabled`" + ` INTEGER NOT NULL DEFAULT 0,
  ` + "`work_ownership`" + `        TEXT NOT NULL DEFAULT '',
  ` + "`work_overrides`" + `        TEXT NOT NULL DEFAULT '',
  ` + "`release_secrets`" + `       TEXT NOT NULL DEFAULT '',
  ` + "`declarations`" + `          INTEGER NOT NULL DEFAULT 0,
  ` + "`require_branch_up_to_date`" + ` INTEGER NOT NULL DEFAULT 0,
  ` + "`ignore_github_ci`" + `      INTEGER NOT NULL DEFAULT 0,
  ` + "`watch_default_branch`" + ` INTEGER NOT NULL DEFAULT 0,
  ` + "`unfixable_checks`" + `      TEXT NOT NULL DEFAULT '',
  ` + "`base_branch_ci_jobs`" + `   TEXT NOT NULL DEFAULT '',
  ` + "`post_merge_dispatch_jobs`" + ` TEXT NOT NULL DEFAULT '',
  ` + "`declared_prompt_extension`" + ` TEXT NOT NULL DEFAULT '',
  ` + "`declared_setup_command`" + `   TEXT NOT NULL DEFAULT '',
  ` + "`ci_mode`" + `                  TEXT NOT NULL DEFAULT '',
  ` + "`repair_template_id`" + `       TEXT NOT NULL DEFAULT '',
  ` + "`port_branches`" + `            TEXT NOT NULL DEFAULT '',
  ` + "`declared_port_branches`" + `   TEXT NOT NULL DEFAULT '',
  ` + "`port_types`" + `               TEXT NOT NULL DEFAULT '',
  ` + "`declared_port_types`" + `      TEXT NOT NULL DEFAULT '',
  ` + "`git_credential`" + `           TEXT NOT NULL DEFAULT '',
  ` + "`merge_on_green`" + `           INTEGER NOT NULL DEFAULT 0,
  ` + "`base_repair_bypasses_human_gates`" + ` INTEGER NOT NULL DEFAULT 0,
  ` + "`merge_recheck`" + `            TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (` + "`owner`" + `, ` + "`name`" + `)
)`,

	// The deployment's named GCP keys (model.GCPKey). team_gcp_key's
	// rows under the default team until schema 23.
	`CREATE TABLE IF NOT EXISTS ` + "`gcp_key`" + ` (
  ` + "`name`" + `                      TEXT     NOT NULL,
  ` + "`project_id`" + `                TEXT     NOT NULL,
  ` + "`service_account_email`" + `     TEXT     NOT NULL,
  ` + "`service_account_count`" + `     INTEGER  NOT NULL DEFAULT 1,
  ` + "`minter_credential`" + `         TEXT     NOT NULL DEFAULT '',
  ` + "`max_key_age_seconds`" + `       INTEGER  NOT NULL DEFAULT 0,
  ` + "`description`" + `               TEXT     NOT NULL DEFAULT '',
  ` + "`created_at`" + `                DATETIME NOT NULL,
  ` + "`updated_at`" + `                DATETIME NOT NULL,
  PRIMARY KEY (` + "`name`" + `)
)`,

	// A repo's grain CI workflows, declared on grain's side rather than
	// in the repository (model.RepoWorkflow, grain/task-245) -- which is
	// the only place they can be declared for a repository grain does not
	// control, since a `.grain/workflows` directory there is a pull
	// request somebody else has to accept.
	//
	// A table of its own rather than a column on repo_config, because the
	// format already assumes a repository has several workflows: one file
	// per workflow, so one row per file, keyed by the path the file would
	// have had in the repository. That path is the key ci.Layered
	// composes the two sides by -- a repo object's workflow wins a path
	// the repository also declares -- so it is stored canonically and
	// nothing writes a row without model.ValidateRepoWorkflowPath's
	// blessing.
	//
	// text is the YAML as somebody typed it, not a parsed structure:
	// pkg/ci owns the format, ci.Parse is run on save so a file that does
	// not parse is refused at the form, and storing anything else here
	// would be a second definition of the format to keep in step.
	//
	// Settings (staterepo.SettingsTables), like repo_config: an operator
	// or an agent proposes a change to a workflow in a pull request
	// against the state repository, and grain never writes one for
	// itself. Needs no SchemaVersion bump -- a new table is created by
	// Init's own CREATE TABLE IF NOT EXISTS on a database of any earlier
	// vintage.
	`CREATE TABLE IF NOT EXISTS ` + "`repo_workflow`" + ` (
  ` + "`owner`" + ` TEXT NOT NULL,
  ` + "`name`" + `  TEXT NOT NULL,
  ` + "`path`" + `  TEXT NOT NULL,
  ` + "`text`" + `  TEXT NOT NULL,
  PRIMARY KEY (` + "`owner`" + `, ` + "`name`" + `, ` + "`path`" + `)
)`,

	// Where a declared template, suite or schedule came from
	// (model.RepoDeclaration, grain/task-60): one row per declared id,
	// naming the repository whose `.grain` directory declared it and the
	// file it was read from.
	//
	// The declared rows themselves are in `template`, `suite` and
	// `schedule` with everything else -- this table is the provenance
	// beside them, which is what lets a sync tell which of those rows are
	// its to replace and what lets pkg/ui refuse an edit with the file
	// name in the message.
	//
	// Not settings (staterepo.SettingsTables): every row here is
	// re-derived from the repository each time it moves, so importing a
	// merged copy of it would assert a provenance grain is about to
	// re-read anyway. Needs no SchemaVersion bump, for repo_workflow's
	// own reason.
	`CREATE TABLE IF NOT EXISTS ` + "`repo_declaration`" + ` (
  ` + "`id`" + `        TEXT NOT NULL,
  ` + "`kind`" + `      TEXT NOT NULL,
  ` + "`owner`" + `     TEXT NOT NULL,
  ` + "`name`" + `      TEXT NOT NULL,
  ` + "`path`" + `      TEXT NOT NULL,
  ` + "`commit`" + `    TEXT NOT NULL DEFAULT '',
  ` + "`synced_at`" + ` DATETIME NOT NULL,
  PRIMARY KEY (` + "`id`" + `)
)`,

	// A declared template's `push:` list (model.RefPush, grain/task-2536):
	// one row per ref pattern, keyed by the declaration's id, written by
	// the same sync that writes repo_declaration and deleted with it. It
	// lives here, beside the provenance, rather than on the template row
	// so it is only ever what the repository's default branch says: the
	// settings export does not carry it and no UI edit writes it. Grain's
	// own record, not settings, for repo_declaration's reason. Needs no
	// SchemaVersion bump, for repo_workflow's own reason.
	`CREATE TABLE IF NOT EXISTS ` + "`repo_declaration_push`" + ` (
  ` + "`id`" + `      TEXT    NOT NULL,
  ` + "`pattern`" + ` TEXT    NOT NULL,
  ` + "`allow_force`" + `  INTEGER NOT NULL DEFAULT 0,
  ` + "`allow_delete`" + ` INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (` + "`id`" + `, ` + "`pattern`" + `)
)`,

	// One row per repository grain reads declarations from
	// (model.RepoDeclarationSync): the commit its live declarations came
	// from, when it was last looked at, and the error if the last look
	// could not be used.
	//
	// Separate from repo_declaration above because it exists even when
	// that table has nothing for the repository: "we read bwsalmon/grain
	// at commit abc and it declares nothing" and "we have never managed
	// to read it" are different states, and only a row of this shape can
	// tell them apart. Grain's own record rather than settings, like the
	// table above and for the same reason.
	`CREATE TABLE IF NOT EXISTS ` + "`repo_declaration_sync`" + ` (
  ` + "`owner`" + `      TEXT NOT NULL,
  ` + "`name`" + `       TEXT NOT NULL,
  ` + "`commit`" + `     TEXT NOT NULL DEFAULT '',
  ` + "`checked_at`" + ` DATETIME NOT NULL,
  ` + "`synced_at`" + `  DATETIME NULL,
  ` + "`last_error`" + ` TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (` + "`owner`" + `, ` + "`name`" + `)
)`,

	// A repository's path triggers (model.PathTrigger, grain/task-1712),
	// one row per `.grain/triggers/<name>.yml` as of the commit the last
	// good declaration sync read -- rewritten whole by every sync, and
	// left alone by a failed one, so the last good commit's triggers go
	// on running. The rule is JSON because nothing queries inside it.
	`CREATE TABLE IF NOT EXISTS ` + "`repo_path_trigger`" + ` (
  ` + "`owner`" + `   TEXT NOT NULL,
  ` + "`name`" + `    TEXT NOT NULL,
  ` + "`trigger`" + ` TEXT NOT NULL,
  ` + "`commit`" + `  TEXT NOT NULL DEFAULT '',
  ` + "`rule`" + `    TEXT NOT NULL,
  PRIMARY KEY (` + "`owner`" + `, ` + "`name`" + `, ` + "`trigger`" + `)
)`,

	// A repository's merge rules (model.MergeRules, grain/task-1659) as
	// of the commit the last good declaration sync read: one row, JSON,
	// rewritten whole by every sync and left alone by a failed one.
	`CREATE TABLE IF NOT EXISTS ` + "`repo_merge_rules`" + ` (
  ` + "`owner`" + `  TEXT NOT NULL,
  ` + "`name`" + `   TEXT NOT NULL,
  ` + "`commit`" + ` TEXT NOT NULL DEFAULT '',
  ` + "`rules`" + `  TEXT NOT NULL,
  PRIMARY KEY (` + "`owner`" + `, ` + "`name`" + `)
)`,

	// How far along each base branch grain has diffed for its merge
	// triggers (model.PathTriggerCursor): the head it last processed, so
	// a burst of merges between two cycles is one diff, and a restart
	// neither re-fires nor skips. Grain's own record, like base_branch_ci.
	`CREATE TABLE IF NOT EXISTS ` + "`path_trigger_cursor`" + ` (
  ` + "`owner`" + `      TEXT NOT NULL,
  ` + "`name`" + `       TEXT NOT NULL,
  ` + "`branch`" + `     TEXT NOT NULL,
  ` + "`commit`" + `     TEXT NOT NULL DEFAULT '',
  ` + "`updated_at`" + ` DATETIME NOT NULL,
  PRIMARY KEY (` + "`owner`" + `, ` + "`name`" + `, ` + "`branch`" + `)
)`,

	// What grain has learned about working here (model.Memory,
	// grain/task-237): one row per scope, the deployment-wide memory
	// keyed by two empty strings and a repo's by its own (owner, name).
	//
	// A table of its own rather than one more column on grain_config and
	// repo_config beside prompt_extension, which is the shape it
	// otherwise has. Those are settings tables (staterepo.SettingsTables)
	// -- a merged file replaces their rows live, so a pull request
	// branched before a column existed resets it -- and memory is not
	// settings: it is grain's own accumulating record, written by an
	// agent through the API while runs are in flight, and losing a
	// night's worth of it to somebody's stale settings diff would be a
	// silent, unattributable loss. Off that list it behaves like every
	// other table grain writes for itself: exported to the state
	// repository on every sync so it is readable and restorable, and
	// imported only by a restore.
	//
	// updated_by_kind/updated_by_id are who last wrote it
	// (Memory.UpdatedBy, grain/task-394) -- the (kind, id) pair every
	// other principal in this schema is stored as, from task's own
	// origin_actor_kind/origin_actor_id down. Both default to the empty
	// string, which is the zero Principal and reads as "unknown": every
	// row written before this column existed has one, and unknown is
	// neither "an agent wrote it" nor "a person did". They are added to
	// an already-created memory by Store.ensureMemoryUpdatedByColumns,
	// which carries the same default this DDL does.
	//
	// Needs no SchemaVersion bump: a new table is created by Init's own
	// CREATE TABLE IF NOT EXISTS on a database of any earlier vintage,
	// which is exactly the case that bump exists to exclude, and a
	// column added to an existing one is what that migration is for.
	`CREATE TABLE IF NOT EXISTS ` + "`memory`" + ` (
  ` + "`owner`" + `           TEXT NOT NULL DEFAULT '',
  ` + "`name`" + `            TEXT NOT NULL DEFAULT '',
  ` + "`text`" + `            TEXT NOT NULL,
  ` + "`updated_at`" + `      DATETIME NULL,
  ` + "`updated_by_kind`" + ` TEXT NOT NULL DEFAULT '',
  ` + "`updated_by_id`" + `   TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (` + "`owner`" + `, ` + "`name`" + `)
)`,

	// The long form of the same thing (model.MemoryDocument,
	// grain/task-270): a named note per scope that no prompt carries,
	// read by the run that decides it matters. Keyed by the scope the
	// table above uses -- (owner, name), two empty strings for the
	// deployment -- plus the document's own slug, since a scope has
	// several.
	//
	// `document` rather than a second `name` column: the two-column
	// (owner, name) scope key is the spelling every repo-keyed table here
	// already has, and renaming half of it for this table alone would
	// make the one join anybody ever writes between the two harder to
	// read than the awkward column name is.
	//
	// `summary` is the line the prompt actually pays for (MemoryIndex);
	// `text` is what nothing reads until it is asked for. Both are
	// bounded at the API rather than here, where a CHECK constraint could
	// only fail a write with no sentence attached.
	//
	// Off staterepo.SettingsTables with `memory` and for the identical
	// reason: written by an agent through the daemon while runs are in
	// flight, so a settings import replacing its rows would silently
	// delete what last night learned. Needs no SchemaVersion bump -- a
	// new table is created by Init's own CREATE TABLE IF NOT EXISTS on a
	// database of any earlier vintage.
	//
	// updated_by_kind/updated_by_id are the memory table's own pair,
	// carried here for the same reason and with the same default of
	// unknown (MemoryDocument.UpdatedBy), added to an already-created
	// memory_document by Store.ensureMemoryDocumentUpdatedByColumns.
	`CREATE TABLE IF NOT EXISTS ` + "`memory_document`" + ` (
  ` + "`owner`" + `           TEXT NOT NULL DEFAULT '',
  ` + "`name`" + `            TEXT NOT NULL DEFAULT '',
  ` + "`document`" + `        TEXT NOT NULL,
  ` + "`summary`" + `         TEXT NOT NULL,
  ` + "`text`" + `            TEXT NOT NULL,
  ` + "`updated_at`" + `      DATETIME NULL,
  ` + "`updated_by_kind`" + ` TEXT NOT NULL DEFAULT '',
  ` + "`updated_by_id`" + `   TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (` + "`owner`" + `, ` + "`name`" + `, ` + "`document`" + `)
)`,

	// One week of what this deployment shipped (model.ReleaseNote,
	// release_note.go): the prose the standing weekly job writes, keyed
	// by the ISO week it is about so that a rerun corrects that week
	// rather than filing a second account of it.
	//
	// Off staterepo.SettingsTables with `memory` and `memory_document`,
	// and for the identical reason: it is written by an agent through
	// the daemon while runs are in flight, so a settings import
	// replacing its rows would silently delete a week nobody meant to
	// touch. It is exported all the same -- ExportConfig writes it, and
	// externalTexts puts each row's prose in `release-notes/<period>.md`
	// where a person with a clone can read it as markdown rather than as
	// a JSON string.
	//
	// Needs no SchemaVersion bump: a new table is created by Init's own
	// CREATE TABLE IF NOT EXISTS on a database of any earlier vintage,
	// which is exactly the case that bump exists to exclude.
	`CREATE TABLE IF NOT EXISTS ` + "`release_note`" + ` (
  ` + "`period`" + `          TEXT NOT NULL,
  ` + "`title`" + `           TEXT NOT NULL DEFAULT '',
  ` + "`text`" + `            TEXT NOT NULL,
  ` + "`created_at`" + `      DATETIME NULL,
  ` + "`updated_at`" + `      DATETIME NULL,
  ` + "`updated_by_kind`" + ` TEXT NOT NULL DEFAULT '',
  ` + "`updated_by_id`" + `   TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (` + "`period`" + `)
)`,

	// A repo's qualification setup -- bwsalmon/agents#518's two switches:
	// require_approval gates every task a run instantiates behind a
	// human's own bulk approval (Store.ApproveQualificationRun) rather
	// than landing pre-approved the way a schedule's own firing does, and
	// auto_promote is what lets the qualifications reconciler promote a
	// candidate itself the moment its run succeeds instead of leaving
	// that to a human. One row per repo, the same (owner, name) key every
	// other repo-scoped, one-row-per-repo table here already uses.
	`CREATE TABLE IF NOT EXISTS ` + "`qualification_config`" + ` (
  ` + "`owner`" + `            TEXT    NOT NULL,
  ` + "`name`" + `             TEXT    NOT NULL,
  ` + "`require_approval`" + ` INTEGER NOT NULL,
  ` + "`auto_promote`" + `     INTEGER NOT NULL,
  PRIMARY KEY (` + "`owner`" + `, ` + "`name`" + `)
)`,

	// One entry in a repo's qualification plan -- a template
	// (bwsalmon/agents#516) this plan schedules, referenced by id
	// rather than copied: content lives on the template, and
	// CreateQualificationRun resolves it fresh every time a candidate
	// is qualified, the same "not a stale copy" discipline
	// fireTaskSchedule already holds a schedule's own TemplateID to.
	// repeat_count is model.QualificationItem's own Repeat; order_key
	// is display order only -- unlike task.order_key it decides nothing
	// about dispatch, since an item's actual scheduling order comes
	// from the dependency graph below, not from this column.
	`CREATE TABLE IF NOT EXISTS ` + "`qualification_item`" + ` (
  ` + "`owner`" + `        TEXT    NOT NULL,
  ` + "`name`" + `         TEXT    NOT NULL,
  ` + "`template_id`" + `  TEXT    NOT NULL,
  ` + "`repeat_count`" + ` INTEGER NOT NULL,
  ` + "`order_key`" + `    REAL    NOT NULL,
  PRIMARY KEY (` + "`owner`" + `, ` + "`name`" + `, ` + "`template_id`" + `)
)`,

	// One dependency edge between two items in the same plan --
	// depends_on_template_id names another item's own template_id, never
	// a task: the graph lives entirely among a plan's items, and
	// CreateQualificationRun is what turns an edge here into real
	// depends-on links between the task instances each side produced.
	`CREATE TABLE IF NOT EXISTS ` + "`qualification_item_depends_on`" + ` (
  ` + "`owner`" + `                  TEXT NOT NULL,
  ` + "`name`" + `                   TEXT NOT NULL,
  ` + "`template_id`" + `             TEXT NOT NULL,
  ` + "`depends_on_template_id`" + ` TEXT NOT NULL,
  PRIMARY KEY (` + "`owner`" + `, ` + "`name`" + `, ` + "`template_id`" + `, ` + "`depends_on_template_id`" + `)
)`,

	// One qualification run per candidate -- CreateQualificationRun's own
	// INSERT, guarded by the unique index below so a reconciler that
	// finds the same active candidate ready twice in a race can only ever
	// create one. INTEGER PRIMARY KEY AUTOINCREMENT for the same reason
	// release_candidate uses it: more than one writer has to stay correct.
	`CREATE TABLE IF NOT EXISTS ` + "`qualification_run`" + ` (
  ` + "`id`" + `           INTEGER PRIMARY KEY AUTOINCREMENT,
  ` + "`owner`" + `        TEXT     NOT NULL,
  ` + "`name`" + `         TEXT     NOT NULL,
  ` + "`candidate_id`" + ` INTEGER  NOT NULL,
  ` + "`created_at`" + `   DATETIME NOT NULL
)`,

	`CREATE UNIQUE INDEX IF NOT EXISTS ` + "`qualification_run_candidate`" + ` ON ` + "`qualification_run`" + ` (` + "`candidate_id`" + `)`,

	// One task instance a qualification run instantiated from a
	// template. template_name is a snapshot of the resolved template's
	// own Name at the moment this instance was created --
	// model.QualificationTaskStatus's own doc comment on why -- and
	// instance_index/repeat_count together are what lets a UI show "2 of
	// 3" against an item whose Repeat is greater than one. task_id is
	// unique: a task belongs to at most one run, ever.
	`CREATE TABLE IF NOT EXISTS ` + "`qualification_task`" + ` (
  ` + "`id`" + `             INTEGER PRIMARY KEY AUTOINCREMENT,
  ` + "`run_id`" + `        INTEGER NOT NULL,
  ` + "`task_id`" + `       TEXT    NOT NULL,
  ` + "`template_id`" + `   TEXT    NOT NULL,
  ` + "`template_name`" + ` TEXT    NOT NULL,
  ` + "`instance_index`" + ` INTEGER NOT NULL,
  ` + "`repeat_count`" + `  INTEGER NOT NULL
)`,

	`CREATE INDEX IF NOT EXISTS ` + "`qualification_task_run`" + ` ON ` + "`qualification_task`" + ` (` + "`run_id`" + `)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS ` + "`qualification_task_task`" + ` ON ` + "`qualification_task`" + ` (` + "`task_id`" + `)`,

	// One suite (bwsalmon/agents#642) -- a saved combination of
	// templates plus how to run them. INTEGER-free TEXT id, the same
	// "own sequence, own prefix" shape template and schedule already
	// use (suite_sequence below), since a suite is created directly
	// rather than cut like a release or a candidate.
	// mode/count/max_passes are model.Suite's own
	// SuiteMode/Count/MaxPasses -- which pair is meaningful depends on
	// mode, model.Suite.Validate's own job to check, not the schema's.
	`CREATE TABLE IF NOT EXISTS ` + "`suite`" + ` (
  ` + "`id`" + `                TEXT     NOT NULL,
  ` + "`name`" + `              TEXT     NOT NULL,
  ` + "`mode`" + `              TEXT     NOT NULL,
  ` + "`count`" + `             INTEGER  NOT NULL,
  ` + "`max_passes`" + `        INTEGER  NOT NULL,
  ` + "`require_approval`" + `  INTEGER  NOT NULL,
  ` + "`auto_merge`" + `        INTEGER  NOT NULL,
  ` + "`auto_approve_proposals`" + ` INTEGER NOT NULL DEFAULT 0,
  ` + "`created_at`" + `        DATETIME NOT NULL,
  PRIMARY KEY (` + "`id`" + `)
)`,

	`CREATE TABLE IF NOT EXISTS ` + "`suite_sequence`" + ` (
  ` + "`id`" + `         INTEGER PRIMARY KEY AUTOINCREMENT,
  ` + "`issued_at`" + `  DATETIME NOT NULL
)`,

	// One template a suite runs, in display and firing order (order_key)
	// -- deliberately no uniqueness constraint on (suite_id, template_id)
	// the way qualification_item has on (owner, name, template_id): a
	// suite has no dependency graph to keep acyclic across its items, so
	// nothing here stops the same template from appearing twice in one
	// suite on purpose (e.g. running it once against each of two
	// unrelated concerns a single pass covers).
	`CREATE TABLE IF NOT EXISTS ` + "`suite_item`" + ` (
  ` + "`id`" + `           INTEGER PRIMARY KEY AUTOINCREMENT,
  ` + "`suite_id`" + `     TEXT    NOT NULL,
  ` + "`template_id`" + `  TEXT    NOT NULL,
  ` + "`order_key`" + `    REAL    NOT NULL
)`,

	`CREATE INDEX IF NOT EXISTS ` + "`suite_item_suite`" + ` ON ` + "`suite_item`" + ` (` + "`suite_id`" + `, ` + "`order_key`" + `)`,

	// One run of a suite against a repo and branch. Every field a run's
	// own behaviour depends on is copied from the suite at creation
	// (model.SuiteRun's own doc comment on why), so this table carries
	// its own mode/count/max_passes/require_approval/auto_merge rather
	// than joining back to suite for them -- a suite edited after a run
	// starts must not change how that run already in flight behaves.
	// suite_id and suite_name are kept anyway, for display and for
	// CreateSuiteRun's own idempotency (a suite deleted after a run
	// starts leaves the run's own history intact). INTEGER PRIMARY KEY
	// AUTOINCREMENT for the same reason release/candidate/
	// qualification_run all use it: starting a run has to stay correct
	// with more than one writer.
	//
	// schedule_id is NULL for a run a human started by hand, and names
	// the schedule that fired it otherwise -- Store.
	// HasActiveRunForSchedule reads it back as the "a previous firing
	// that has not finished suppresses the next one" check a schedule
	// firing a suite needs, exactly what task_tag's own firing tag is
	// for a schedule filing a task. A database created before schedules
	// could fire a suite gets this column from
	// ensureSuiteRunScheduleColumn rather than from this DDL.
	`CREATE TABLE IF NOT EXISTS ` + "`suite_run`" + ` (
  ` + "`id`" + `                INTEGER PRIMARY KEY AUTOINCREMENT,
  ` + "`suite_id`" + `          TEXT     NOT NULL,
  ` + "`suite_name`" + `        TEXT     NOT NULL,
  ` + "`schedule_id`" + `       TEXT     NULL,
  ` + "`owner`" + `             TEXT     NOT NULL,
  ` + "`repo`" + `              TEXT     NOT NULL,
  ` + "`base`" + `              TEXT     NOT NULL,
  ` + "`mode`" + `              TEXT     NOT NULL,
  ` + "`count`" + `             INTEGER  NOT NULL,
  ` + "`max_passes`" + `        INTEGER  NOT NULL,
  ` + "`require_approval`" + `  INTEGER  NOT NULL,
  ` + "`auto_merge`" + `        INTEGER  NOT NULL,
  ` + "`auto_approve_proposals`" + ` INTEGER NOT NULL DEFAULT 0,
  ` + "`status`" + `            TEXT     NOT NULL,
  ` + "`last_error`" + `        TEXT     NULL,
  ` + "`created_at`" + `        DATETIME NOT NULL,
  ` + "`completed_at`" + `      DATETIME NULL
)`,

	// What ListSuiteRuns (newest first) and the reconciler's own
	// ActiveSuiteRuns (status only) both need.
	`CREATE INDEX IF NOT EXISTS ` + "`suite_run_status`" + ` ON ` + "`suite_run`" + ` (` + "`status`" + `)`,

	// A run's own snapshot of its suite's items at the moment it was
	// created -- CreateSuiteRun's copy, read back by FireNextPass every
	// time it files another pass, so a suite edited after a run starts
	// cannot change which templates that run's later passes fire
	// (model.SuiteRun.Items's own doc comment).
	`CREATE TABLE IF NOT EXISTS ` + "`suite_run_item`" + ` (
  ` + "`id`" + `           INTEGER PRIMARY KEY AUTOINCREMENT,
  ` + "`run_id`" + `       INTEGER NOT NULL,
  ` + "`template_id`" + `  TEXT    NOT NULL,
  ` + "`order_key`" + `    REAL    NOT NULL
)`,

	`CREATE INDEX IF NOT EXISTS ` + "`suite_run_item_run`" + ` ON ` + "`suite_run_item`" + ` (` + "`run_id`" + `, ` + "`order_key`" + `)`,

	// One task instance a run's pass instantiated. template_name is a
	// snapshot of the resolved template's own Name at the moment this
	// instance was created, qualification_task's own reasoning for the
	// same column. task_id is unique: a task belongs to at most one
	// suite run, ever.
	`CREATE TABLE IF NOT EXISTS ` + "`suite_run_task`" + ` (
  ` + "`id`" + `             INTEGER PRIMARY KEY AUTOINCREMENT,
  ` + "`run_id`" + `        INTEGER NOT NULL,
  ` + "`task_id`" + `       TEXT    NOT NULL,
  ` + "`template_id`" + `   TEXT    NOT NULL,
  ` + "`template_name`" + ` TEXT    NOT NULL,
  ` + "`pass_number`" + `   INTEGER NOT NULL
)`,

	`CREATE INDEX IF NOT EXISTS ` + "`suite_run_task_run`" + ` ON ` + "`suite_run_task`" + ` (` + "`run_id`" + `)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS ` + "`suite_run_task_task`" + ` ON ` + "`suite_run_task`" + ` (` + "`task_id`" + `)`,

	// One saved dashboard (model.Board, grain/task-48) -- a named way of
	// looking at this deployment's tasks, filed in the nav rail under
	// `folder` and ordered within it by `order_key`, task.order_key's own
	// treatment.
	//
	// `query` is the filtering and sorting, stored in the same encoding
	// the UI writes into the address bar ("q=ci&f=state:any:running",
	// ui/src/paths.js). No column of this schema is a structured filter,
	// on purpose: nothing in the daemon asks a board a question, the one
	// reader is the UI, and the UI already has the parser -- see
	// model.Board's own doc comment.
	//
	// `kind` is which sort of dashboard it is ("list", "kanban",
	// "gantt", "panels", "report"), a string rather than a flag because the next type of
	// dashboard is a new value here and nothing else -- which is what
	// "gantt" cost when the timeline became one (model.BoardKinds).
	//
	// "report" is the first value that is not a view of the backlog at
	// all (grain/task-83) and it cost this table nothing either: the
	// query above is read as a scope over the measurement report instead
	// of as a filter over task rows, and the sections it prints are rows
	// of `board_section` below.
	`CREATE TABLE IF NOT EXISTS ` + "`board`" + ` (
  ` + "`id`" + `           TEXT    NOT NULL,
  ` + "`name`" + `         TEXT    NOT NULL,
  ` + "`folder`" + `       TEXT    NOT NULL DEFAULT '',
  ` + "`kind`" + `         TEXT    NOT NULL,
  ` + "`description`" + `  TEXT    NOT NULL DEFAULT '',
  ` + "`query`" + `        TEXT    NOT NULL DEFAULT '',
  ` + "`order_key`" + `    REAL    NOT NULL DEFAULT 0,
  PRIMARY KEY (` + "`id`" + `)
)`,

	`CREATE TABLE IF NOT EXISTS ` + "`board_sequence`" + ` (
  ` + "`id`" + `         INTEGER PRIMARY KEY AUTOINCREMENT,
  ` + "`issued_at`" + `  DATETIME NOT NULL
)`,

	// A Kanban board's own columns, in display order -- a table rather
	// than a JSON column on `board` for the reason task_read is one: the
	// rows are what an agent edits in the state repository's dump, and a
	// column of escaped JSON is not something anybody edits correctly.
	// `states` is the one exception, a comma-separated list of
	// model.State values, because a state is a word from a closed
	// vocabulary and a table of (board, column, state) triples would
	// carry an ordering nothing means.
	//
	// A board with no rows here renders the columns the browser is set to
	// (ui/src/board.js), which is what an agent asked for "a Kanban board
	// of my open work" should produce without inventing a layout.
	`CREATE TABLE IF NOT EXISTS ` + "`board_column`" + ` (
  ` + "`id`" + `         INTEGER PRIMARY KEY AUTOINCREMENT,
  ` + "`board_id`" + `   TEXT    NOT NULL,
  ` + "`name`" + `       TEXT    NOT NULL,
  ` + "`states`" + `     TEXT    NOT NULL,
  ` + "`order_key`" + `  REAL    NOT NULL
)`,

	`CREATE INDEX IF NOT EXISTS ` + "`board_column_board`" + ` ON ` + "`board_column`" + ` (` + "`board_id`" + `, ` + "`order_key`" + `)`,

	// The panels a "panels" board is made of, in display order
	// (model.BoardPanel, grain/task-88) -- a table for board_column's own
	// reason: these rows are what an agent edits in the state
	// repository's dump, and a column of escaped JSON is not something
	// anybody edits correctly.
	//
	// `kind` is which view the panel draws ("list", "gantt", "states",
	// "metrics"), `query` what it is narrowed to in the address bar's own
	// encoding (empty: the board's own query), `range` the span the two
	// kinds that cover time are set to, and `width` how much of the row
	// it takes. None of the three vocabularies is checked by SQLite: the
	// UI normalizes a word it does not know back to the default, so a
	// board written for a newer build draws rather than fails.
	//
	// No SchemaVersion bump goes with it, for task_write's own reason:
	// Init's CREATE TABLE IF NOT EXISTS creates a missing table on an
	// existing database perfectly well, and an older store gains one
	// empty table -- which reads back as no dashboard having any panels,
	// exactly what was true before this table existed.
	`CREATE TABLE IF NOT EXISTS ` + "`board_panel`" + ` (
  ` + "`id`" + `         INTEGER PRIMARY KEY AUTOINCREMENT,
  ` + "`board_id`" + `   TEXT    NOT NULL,
  ` + "`kind`" + `       TEXT    NOT NULL,
  ` + "`title`" + `      TEXT    NOT NULL DEFAULT '',
  ` + "`query`" + `      TEXT    NOT NULL DEFAULT '',
  ` + "`range`" + `      TEXT    NOT NULL DEFAULT '',
  ` + "`width`" + `      TEXT    NOT NULL DEFAULT '',
  ` + "`order_key`" + `  REAL    NOT NULL
)`,

	`CREATE INDEX IF NOT EXISTS ` + "`board_panel_board`" + ` ON ` + "`board_panel`" + ` (` + "`board_id`" + `, ` + "`order_key`" + `)`,

	// The sections a "report" board prints, in order (model.BoardSection,
	// grain/task-83) -- the third child table a board can carry, and the
	// one that settled board_column's argument into a rule: a setting
	// belonging to one kind of board is rows here, never JSON escaped
	// into a column of `board`. The rows are what an agent edits in the
	// state repository's dump, and that is the whole of the reason.
	//
	// `kind` is which part of the measurement report it draws
	// ("throughput", "latency", "runs", "backlog", "tools", "prompts",
	// "checks", "cycles") and `title` an optional heading for it. A board with no
	// rows here prints the whole report, which is what an agent asked for
	// "a report board for acme/widgets" should produce without having to
	// choose a layout -- board_column's own fallback.
	//
	// No SchemaVersion bump, for board_panel's own reason: Init's CREATE
	// TABLE IF NOT EXISTS creates it on an existing database, and an
	// older store gains one empty table, which reads back as no report
	// board naming any sections.
	`CREATE TABLE IF NOT EXISTS ` + "`board_section`" + ` (
  ` + "`id`" + `         INTEGER PRIMARY KEY AUTOINCREMENT,
  ` + "`board_id`" + `   TEXT    NOT NULL,
  ` + "`kind`" + `       TEXT    NOT NULL,
  ` + "`title`" + `      TEXT    NOT NULL DEFAULT '',
  ` + "`order_key`" + `  REAL    NOT NULL
)`,

	`CREATE INDEX IF NOT EXISTS ` + "`board_section_board`" + ` ON ` + "`board_section`" + ` (` + "`board_id`" + `, ` + "`order_key`" + `)`,

	// One run of grain's own CI (pkg/ci), and below it the jobs it
	// planned and the steps each of those ran. Written by
	// ci.NewStoredRunStore, which is ci.RunStore over this schema rather
	// than over a map -- the swap that type's own doc comment always said
	// it was the seam for (grain/task-303).
	//
	// Why these rows exist at all: a run is read back long after it is
	// over. `grain ci show <id>` and the CI page serve a run's jobs,
	// steps and output out of the store, and orchestrator.collectCIRun
	// asks for the run a task is waiting on -- a question the in-memory
	// store answered "never heard of it" for every run that was in flight
	// across a restart, which cost one wasted CI run per restart per open
	// branch.
	//
	// Three tables rather than one row with a JSON blob in it, for
	// task_read's own reason: "which jobs failed on this branch this
	// week?" is a question worth being able to ask, and a blob answers
	// none of it. The children are keyed by their position in the run
	// (job_index, step_index) rather than by an id of their own, because
	// that position *is* their identity -- plan order is what the engine
	// reports, what the API serves and what a reader scans down -- and it
	// is what lets a whole run be rewritten as a delete and an insert
	// (PutCIRun) with nothing to renumber.
	//
	// `run_key` and `job_key` are ci.Key, the run-level and job-level
	// fingerprints; `run_key` is indexed because Service.Start asks
	// "is this exact run already in flight?" on every request.
	//
	// A run with no conclusion is one still going. That is a fact about
	// this process, so a run left unconcluded by a daemon that stopped is
	// not left to look live forever: RecoverInterruptedCIRuns stamps it
	// cancelled and interrupted at startup (its own doc comment).
	//
	// Needs no SchemaVersion bump, for task_write's own reason: Init's
	// CREATE TABLE IF NOT EXISTS creates a missing table on an existing
	// database perfectly well, and an older store gains three empty
	// tables -- which reads back as a deployment that has run no CI yet,
	// exactly what one upgrading into this is.
	`CREATE TABLE IF NOT EXISTS ` + "`ci_run`" + ` (
  ` + "`id`" + `           TEXT     NOT NULL,
  ` + "`run_key`" + `      TEXT     NOT NULL,
  ` + "`repo`" + `         TEXT     NOT NULL,
  ` + "`ref`" + `          TEXT     NOT NULL,
  ` + "`commit_sha`" + `   TEXT     NOT NULL DEFAULT '',
  ` + "`event`" + `        TEXT     NOT NULL DEFAULT '',
  ` + "`requested_by`" + ` TEXT     NOT NULL DEFAULT '',
  ` + "`started_at`" + `   DATETIME NULL,
  ` + "`finished_at`" + `  DATETIME NULL,
  ` + "`conclusion`" + `   TEXT     NOT NULL DEFAULT '',
  ` + "`interrupted`" + `  INTEGER  NOT NULL DEFAULT 0,
  ` + "`error`" + `        TEXT     NOT NULL DEFAULT '',
  ` + "`cancel_reason`" + ` TEXT    NOT NULL DEFAULT '',
  ` + "`cancelled_by`" + ` TEXT     NOT NULL DEFAULT '',
  ` + "`build_revision`" + ` TEXT   NOT NULL DEFAULT '',
  ` + "`build_time`" + `   DATETIME NULL,
  ` + "`build_modified`" + ` INTEGER NOT NULL DEFAULT 0,
  ` + "`recheck`" + `        INTEGER  NOT NULL DEFAULT 0,
  PRIMARY KEY (` + "`id`" + `)
)`,

	// Service.Start's "is this plan already running?" (RunStore.Live), and
	// the listing's own newest-first order.
	`CREATE INDEX IF NOT EXISTS ` + "`ci_run_key`" + ` ON ` + "`ci_run`" + ` (` + "`run_key`" + `, ` + "`conclusion`" + `)`,
	`CREATE INDEX IF NOT EXISTS ` + "`ci_run_started`" + ` ON ` + "`ci_run`" + ` (` + "`started_at`" + `)`,

	`CREATE TABLE IF NOT EXISTS ` + "`ci_job`" + ` (
  ` + "`run_id`" + `        TEXT     NOT NULL,
  ` + "`job_index`" + `     INTEGER  NOT NULL,
  ` + "`workflow`" + `      TEXT     NOT NULL DEFAULT '',
  ` + "`workflow_path`" + ` TEXT     NOT NULL DEFAULT '',
  ` + "`job_id`" + `        TEXT     NOT NULL DEFAULT '',
  ` + "`name`" + `          TEXT     NOT NULL DEFAULT '',
  ` + "`job_key`" + `       TEXT     NOT NULL DEFAULT '',
  ` + "`cached`" + `        INTEGER  NOT NULL DEFAULT 0,
  ` + "`sandbox`" + `       TEXT     NOT NULL DEFAULT '',
  ` + "`started_at`" + `    DATETIME NULL,
  ` + "`finished_at`" + `   DATETIME NULL,
  ` + "`conclusion`" + `    TEXT     NOT NULL DEFAULT '',
  ` + "`error`" + `         TEXT     NOT NULL DEFAULT '',
  ` + "`activity`" + `      TEXT     NOT NULL DEFAULT '',
  ` + "`activity_at`" + `   DATETIME NULL,
  ` + "`queued_ms`" + `     INTEGER  NOT NULL DEFAULT 0,
  ` + "`queued_cause`" + `  TEXT     NOT NULL DEFAULT '',
  ` + "`queued_reason`" + ` TEXT     NOT NULL DEFAULT '',
  ` + "`retry_count`" + `   INTEGER  NOT NULL DEFAULT 0,
  ` + "`retry_reason`" + `  TEXT     NOT NULL DEFAULT '',
  ` + "`flake`" + `             INTEGER  NOT NULL DEFAULT 0,
  ` + "`quarantined`" + `       INTEGER  NOT NULL DEFAULT 0,
  ` + "`quarantined_tests`" + ` TEXT     NOT NULL DEFAULT '',
  ` + "`status`" + `            TEXT     NOT NULL DEFAULT '',
  ` + "`warning`" + `           TEXT     NOT NULL DEFAULT '',
  ` + "`failure_summary`" + `   TEXT     NOT NULL DEFAULT '',
  ` + "`base_commit`" + `       TEXT     NOT NULL DEFAULT '',
  ` + "`attempts`" + `          TEXT     NOT NULL DEFAULT '',
  PRIMARY KEY (` + "`run_id`" + `, ` + "`job_index`" + `)
)`,

	// `output` is what the step printed, bounded before it is written
	// (ci.MaxStoredStepOutput) rather than by anything here. A database
	// column has no limit of its own, and this one used to be exported
	// into the state repository, where every version of it would be kept
	// in git forever -- so the bound is the caller's and is a good deal
	// tighter than the 256KB ci keeps in memory. It is worth keeping now
	// that the repository carries the configuration only
	// (grain/task-395): what it bounds today is the store on disk and
	// every read of a run's results.
	`CREATE TABLE IF NOT EXISTS ` + "`ci_step`" + ` (
  ` + "`run_id`" + `      TEXT     NOT NULL,
  ` + "`job_index`" + `   INTEGER  NOT NULL,
  ` + "`step_index`" + `  INTEGER  NOT NULL,
  ` + "`name`" + `        TEXT     NOT NULL DEFAULT '',
  ` + "`conclusion`" + `  TEXT     NOT NULL DEFAULT '',
  ` + "`exit_code`" + `   INTEGER  NOT NULL DEFAULT 0,
  ` + "`output`" + `      TEXT     NOT NULL DEFAULT '',
  ` + "`error`" + `       TEXT     NOT NULL DEFAULT '',
  ` + "`started_at`" + `  DATETIME NULL,
  ` + "`finished_at`" + ` DATETIME NULL,
  PRIMARY KEY (` + "`run_id`" + `, ` + "`job_index`" + `, ` + "`step_index`" + `)
)`,

	// Live streaming step output chunks (grain/task-406): appended as a step
	// runs (model.Store.PutCIStepOutput) rather than updating ci_step.output
	// on every tick, avoiding write amplification and row lock contention
	// during live streaming. Rewritten whole into ci_step.output upon step
	// conclusion.
	`CREATE TABLE IF NOT EXISTS ` + "`ci_step_log_chunk`" + ` (
  ` + "`run_id`" + `      TEXT     NOT NULL,
  ` + "`job_index`" + `   INTEGER  NOT NULL,
  ` + "`step_index`" + `  INTEGER  NOT NULL,
  ` + "`chunk_index`" + ` INTEGER  NOT NULL,
  ` + "`output`" + `      TEXT     NOT NULL DEFAULT '',
  ` + "`created_at`" + `  DATETIME NULL,
  PRIMARY KEY (` + "`run_id`" + `, ` + "`job_index`" + `, ` + "`step_index`" + `, ` + "`chunk_index`" + `)
)`,

	// How many sandboxes this deployment has built over its whole life,
	// and how long they lived: one row per kind ("agent", "ci"), counters
	// only (grain/task-315, and see sandboxusage.go for what each column
	// counts and what it deliberately does not).
	//
	// An aggregate rather than a row per sandbox. A row each would answer
	// "which job costs most", which is worth having and is the follow-up
	// -- and it would also grow without bound and need a retention rule,
	// at which point the lifetime figure is only as long as the retention
	// (what the CI history's own bounds already do to ci_run: a count over
	// those rows goes *down* when the sweep runs). Four counters per kind
	// answer "what has this cost" for ever, in two rows, and cost one
	// upsert per sandbox.
	//
	// `duration_ms` is milliseconds rather than seconds so that adding a
	// sandbox's life to it neither rounds nor accumulates a bias: rounding
	// each of a few thousand short CI jobs to the second would drift the
	// total by minutes in whichever direction the rounding fell.
	//
	// Needs no SchemaVersion bump, for task_write's own reason: Init's
	// CREATE TABLE IF NOT EXISTS creates it on an existing database, and
	// an older store gains an empty table -- a deployment whose counting
	// starts today, which is exactly what one upgrading into this is.
	`CREATE TABLE IF NOT EXISTS ` + "`sandbox_usage`" + ` (
  ` + "`kind`" + `        TEXT     NOT NULL,
  ` + "`attempted`" + `   INTEGER  NOT NULL DEFAULT 0,
  ` + "`created`" + `     INTEGER  NOT NULL DEFAULT 0,
  ` + "`finished`" + `    INTEGER  NOT NULL DEFAULT 0,
  ` + "`duration_ms`" + ` BIGINT   NOT NULL DEFAULT 0,
  ` + "`since`" + `       DATETIME NULL,
  ` + "`updated_at`" + `  DATETIME NULL,
  PRIMARY KEY (` + "`kind`" + `)
)`,

	// Long-standing sandboxes (model.StandingSandbox, grain/task-126):
	// sandboxes defined in grain/config, backed by a Granule OCI image,
	// configured with secrets resolved from Grain's encrypted secret store,
	// and supervised so they are restarted on failure and on system restart.
	`CREATE TABLE IF NOT EXISTS ` + "`standing_sandbox`" + ` (
  ` + "`name`" + `           TEXT    NOT NULL,
  ` + "`image`" + `          TEXT    NOT NULL,
  ` + "`command`" + `        TEXT    NOT NULL DEFAULT '',
  ` + "`dir`" + `            TEXT    NOT NULL DEFAULT '',
  ` + "`cpus`" + `           INTEGER NOT NULL DEFAULT 0,
  ` + "`memory_mb`" + `      INTEGER NOT NULL DEFAULT 0,
  ` + "`disk_gb`" + `        INTEGER NOT NULL DEFAULT 0,
  ` + "`network_access`" + ` TEXT    NOT NULL DEFAULT '',
  ` + "`enabled`" + `        INTEGER NOT NULL DEFAULT 1,
  PRIMARY KEY (` + "`name`" + `)
)`,

	`CREATE TABLE IF NOT EXISTS ` + "`standing_sandbox_secret`" + ` (
  ` + "`sandbox_name`" + ` TEXT NOT NULL,
  ` + "`secret_name`" + `  TEXT NOT NULL,
  ` + "`env_var`" + `      TEXT NOT NULL DEFAULT '',
  ` + "`file_path`" + `    TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (` + "`sandbox_name`" + `, ` + "`secret_name`" + `, ` + "`env_var`" + `, ` + "`file_path`" + `)
)`,

	`CREATE TABLE IF NOT EXISTS ` + "`standing_sandbox_env`" + ` (
  ` + "`sandbox_name`" + ` TEXT NOT NULL,
  ` + "`name`" + `         TEXT NOT NULL,
  ` + "`value`" + `        TEXT NOT NULL,
  PRIMARY KEY (` + "`sandbox_name`" + `, ` + "`name`" + `)
)`,

	// Persistent sandboxes (model.PersistentSandbox): staging environments
	// and long-lived services checked out from a repository, kept running
	// by the orchestrator's PersistentSandboxSupervisor and reachable on a
	// host port or under the grain UI's /staging/<name>/.
	`CREATE TABLE IF NOT EXISTS ` + "`persistent_sandbox`" + ` (
  ` + "`name`" + `             TEXT    NOT NULL,
  ` + "`repo`" + `             TEXT    NOT NULL,
  ` + "`ref`" + `              TEXT    NOT NULL DEFAULT '',
  ` + "`auto_update`" + `      INTEGER NOT NULL DEFAULT 0,
  ` + "`command`" + `          TEXT    NOT NULL DEFAULT '',
  ` + "`dir`" + `              TEXT    NOT NULL DEFAULT '',
  ` + "`env`" + `              TEXT    NOT NULL DEFAULT '',
  ` + "`capabilities`" + `     TEXT    NOT NULL DEFAULT '',
  ` + "`cpus`" + `             INTEGER NOT NULL DEFAULT 0,
  ` + "`memory_mb`" + `        INTEGER NOT NULL DEFAULT 0,
  ` + "`disk_gb`" + `          INTEGER NOT NULL DEFAULT 0,
  ` + "`network_access`" + `   TEXT    NOT NULL DEFAULT '',
  ` + "`sandbox_port`" + `     INTEGER NOT NULL DEFAULT 0,
  ` + "`host_port`" + `        INTEGER NOT NULL DEFAULT 0,
  ` + "`ui_subpath`" + `       TEXT    NOT NULL DEFAULT '',
  ` + "`strip_prefix`" + `     INTEGER NOT NULL DEFAULT 0,
  ` + "`enabled`" + `          INTEGER NOT NULL DEFAULT 1,
  ` + "`refresh`" + `          TEXT    NOT NULL DEFAULT '',
  ` + "`rollback_on_crash`" + ` INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (` + "`name`" + `)
)`,

	`CREATE TABLE IF NOT EXISTS ` + "`executor_token`" + ` (
  ` + "`id`" + `                TEXT     NOT NULL,
  ` + "`name`" + `              TEXT     NOT NULL,
  ` + "`token_hash`" + `        TEXT     NOT NULL,
  ` + "`prefix`" + `            TEXT     NOT NULL,
  ` + "`created_at`" + `        DATETIME NOT NULL,
  ` + "`revoked_at`" + `        DATETIME NULL,
  ` + "`last_connected_at`" + ` DATETIME NULL,
  PRIMARY KEY (` + "`id`" + `),
  UNIQUE (` + "`token_hash`" + `)
)`,

	`CREATE TABLE IF NOT EXISTS ` + "`executor_token_sequence`" + ` (
  ` + "`id`" + `        INTEGER PRIMARY KEY AUTOINCREMENT,
  ` + "`issued_at`" + ` DATETIME NOT NULL
)`,

	`CREATE TABLE IF NOT EXISTS ` + "`secret_resolution_audit`" + ` (
  ` + "`id`" + `          INTEGER PRIMARY KEY AUTOINCREMENT,
  ` + "`timestamp`" + `   DATETIME NOT NULL,
  ` + "`task_id`" + `     TEXT     NOT NULL DEFAULT '',
  ` + "`run_id`" + `      TEXT     NOT NULL DEFAULT '',
  ` + "`secret_name`" + ` TEXT     NOT NULL DEFAULT '',
  ` + "`key`" + `         TEXT     NOT NULL DEFAULT '',
  ` + "`principal`" + `   TEXT     NOT NULL DEFAULT ''
)`,

	`CREATE INDEX IF NOT EXISTS ` + "`secret_resolution_audit_time`" + ` ON ` + "`secret_resolution_audit`" + ` (` + "`timestamp`" + ` DESC)`,

	`CREATE TABLE IF NOT EXISTS ` + "`credential_access_audit`" + ` (
  ` + "`id`" + `         INTEGER PRIMARY KEY AUTOINCREMENT,
  ` + "`timestamp`" + `  DATETIME NOT NULL,
  ` + "`task_id`" + `    TEXT     NOT NULL DEFAULT '',
  ` + "`run_id`" + `     TEXT     NOT NULL DEFAULT '',
  ` + "`sandbox`" + `    TEXT     NOT NULL DEFAULT '',
  ` + "`kind`" + `       TEXT     NOT NULL DEFAULT '',
  ` + "`resource`" + `   TEXT     NOT NULL DEFAULT '',
  ` + "`action`" + `     TEXT     NOT NULL DEFAULT '',
  ` + "`credential`" + ` TEXT     NOT NULL DEFAULT '',
  ` + "`outcome`" + `    TEXT     NOT NULL DEFAULT ''
)`,

	`CREATE INDEX IF NOT EXISTS ` + "`credential_access_audit_time`" + ` ON ` + "`credential_access_audit`" + ` (` + "`timestamp`" + ` DESC)`,

	// Operator notifications (model.Notification, docs/design/
	// durable-tasks.md DT-5): one open row per subject, which the partial
	// unique index holds, and every cleared instance kept as history. A
	// new table, so an older store gains an empty one with no
	// SchemaVersion bump, for task_write's reason.
	`CREATE TABLE IF NOT EXISTS ` + "`notification`" + ` (
  ` + "`id`" + `                    INTEGER PRIMARY KEY AUTOINCREMENT,
  ` + "`subject`" + `               TEXT     NOT NULL,
  ` + "`family`" + `                TEXT     NOT NULL,
  ` + "`severity`" + `              TEXT     NOT NULL,
  ` + "`title`" + `                 TEXT     NOT NULL,
  ` + "`body`" + `                  TEXT     NOT NULL,
  ` + "`task_id`" + `               TEXT     NULL,
  ` + "`repo`" + `                  TEXT     NULL,
  ` + "`raised_by`" + `             TEXT     NOT NULL,
  ` + "`raised_at`" + `             DATETIME NOT NULL,
  ` + "`last_raised_at`" + `        DATETIME NOT NULL,
  ` + "`raise_count`" + `           INTEGER  NOT NULL DEFAULT 1,
  ` + "`clears_when`" + `           TEXT     NULL,
  ` + "`cleared_at`" + `            DATETIME NULL,
  ` + "`cleared_by`" + `            TEXT     NULL,
  ` + "`acked_at`" + `              DATETIME NULL,
  ` + "`acked_by`" + `              TEXT     NULL,
  ` + "`actions`" + `               TEXT     NULL,
  ` + "`investigation_task_id`" + ` TEXT     NULL,
  ` + "`delivered`" + `             TEXT     NULL
)`,

	`CREATE UNIQUE INDEX IF NOT EXISTS ` + "`notification_open_subject`" + ` ON ` + "`notification`" + ` (` + "`subject`" + `) WHERE ` + "`cleared_at`" + ` IS NULL`,

	// Which notification families wake a durable task (DT-5
	// "Subscriptions"): a family, or a "prefix.*" pattern.
	`CREATE TABLE IF NOT EXISTS ` + "`task_notification_subscription`" + ` (
  ` + "`task_id`" + ` TEXT NOT NULL,
  ` + "`family`" + `  TEXT NOT NULL,
  PRIMARY KEY (` + "`task_id`" + `, ` + "`family`" + `)
)`,
}

// Views is the derivations, each a (name, DDL) pair so Init can drop and
// recreate one by name -- SQLite has no CREATE OR REPLACE VIEW, unlike
// the MySQL dialect Dolt spoke. Order matters: each view here may only
// name a table or an earlier view in this same slice, since Init applies
// them in order and a CREATE VIEW referencing something not yet created
// fails outright. task_ready reads task_state and task_blocked, and both
// appear before it.
var Views = []View{
	// How many of a task's most recent runs, in a row, ended without
	// succeeding -- zero the moment any of them succeeds, or once a human
	// asks for a retry (task_observation.retry_requested_at), whichever
	// is later -- for every task whose streak is not zero.
	//
	// The count itself is not derived here. It is
	// task_observation.streak_count, which computeStreakForTask (store.go)
	// recomputes in the same transaction as every write that can move it
	// (FinishRun, SetRunOutcome, PutObservation), skipping PausedOutcome,
	// CapacityOutcome, InterruptedOutcome, EnvironmentOutcome and OOMOutcome runs as evidence about the
	// deployment rather than the task -- see those constants' own doc
	// comments. task_state reads that column directly; this view is the
	// same number by name, and Store.FailureStreak skips the same four
	// words so that state_test.go can hold the two to one answer
	// (sqlite's TestTheDeploymentsOwnEndingsAreLeftOutOfTheStreak holds
	// all four).
	{"task_streak", `
SELECT
  ` + "`task_id`" + ` AS ` + "`task_id`" + `,
  ` + "`streak_count`" + ` AS ` + "`streak`" + `
FROM ` + "`task_observation`" + `
WHERE ` + "`streak_count`" + ` > 0`},

	// The invariant, as a view. Precedence top to bottom, matching
	// StateOf exactly — see state_test.go on what holds them together.
	// 'failed' sits between 'running' and 'proposed': a task with a live
	// run is running whatever its streak says (task_ready cannot have
	// dispatched a capped task in the first place, but a run started
	// before this cutoff shipped, or before a human lowered
	// MaxConsecutiveFailures, should still be let to finish), and a
	// proposed task can carry no streak at all -- dispatch never ran an
	// unapproved task -- so the two branches never compete in practice.
	//
	// 'deferred' sits between 'failed' and 'proposed' for the reason
	// StateOf gives at the same branch: it is a declaration about what
	// should happen next, so it outranks the two states that are also
	// read off the declaration and never overrides grain's own account of
	// what already happened.
	//
	// 'awaiting_submit' sits immediately above 'completed' and tests the
	// same completed_at: the two are one condition split by whether
	// anything is going to land the pull request without a human
	// (model.AwaitsSubmit, which this EXISTS clause and the auto_merge
	// test spell out in SQL). EXISTS rather than a join to task_link,
	// because a task carrying two fixes-links would otherwise appear
	// twice in a view whose whole point is one row per task.
	//
	// 'awaiting_reply' also splits the same completed_at a third way, and it is
	// model.AwaitsAck in SQL: a finished task with no pull request at all
	// has nothing on any queue, and what it produced is words somebody
	// has to read or reply to. The origin_reason test is that function's own
	// exclusion of a review grain filed itself, whose findings are
	// delivered on the change under review rather than here -- the origin
	// and not the intent, because such a review declares IntentImplement
	// and is a review only by its provenance (orchestrator.SyncReviews),
	// and because a task that merely declares IntentReview has nowhere
	// else its findings could have gone. The three branches are mutually exclusive
	// -- one needs a fixes-link, one needs none -- so their order between
	// themselves decides nothing; they are written in the order StateOf
	// tests them so the two derivations read alike.
	{"task_state", `
SELECT
  ` + "`t`.`id`" + ` AS ` + "`task_id`" + `,
  CASE
    WHEN ` + "`o`.`closed_at`" + `    IS NOT NULL THEN 'closed'
    WHEN ` + "`o`.`completed_at`" + ` IS NOT NULL AND ` + "`t`.`auto_merge`" + ` = FALSE AND EXISTS (
           SELECT 1 FROM ` + "`task_link`" + ` AS ` + "`fl`" + `
           WHERE ` + "`fl`.`task_id`" + ` = ` + "`t`.`id`" + ` AND ` + "`fl`.`kind`" + ` = '` + string(LinkFixes) + `'
         ) THEN 'awaiting_submit'
    WHEN ` + "`o`.`completed_at`" + ` IS NOT NULL
         AND ` + "`t`.`origin_reason`" + ` != '` + string(ReasonReview) + `' AND NOT EXISTS (
           SELECT 1 FROM ` + "`task_link`" + ` AS ` + "`al`" + `
           WHERE ` + "`al`.`task_id`" + ` = ` + "`t`.`id`" + ` AND ` + "`al`.`kind`" + ` = '` + string(LinkFixes) + `'
         ) THEN 'awaiting_reply'
    WHEN ` + "`o`.`completed_at`" + ` IS NOT NULL THEN 'completed'
    WHEN ` + "`o`.`pending_question_comment_id`" + ` IS NOT NULL THEN 'awaiting_reply'
    WHEN ` + "`r`.`id`" + ` IS NOT NULL THEN 'running'
    WHEN COALESCE(` + "`o`.`streak_count`" + `, 0) >= ` + strconv.Itoa(MaxConsecutiveFailures) + ` THEN 'failed'
    WHEN ` + "`t`.`deferred_at`" + ` IS NOT NULL THEN 'deferred'
    WHEN ` + "`t`.`approval_actor_kind`" + ` IS NULL THEN 'proposed'
    ELSE 'queued'
  END AS ` + "`state`" + `
FROM ` + "`task`" + ` AS ` + "`t`" + `
LEFT JOIN ` + "`task_observation`" + ` AS ` + "`o`" + ` ON ` + "`o`.`task_id`" + ` = ` + "`t`.`id`" + `
LEFT JOIN ` + "`task_run`" + ` AS ` + "`r`" + `
       ON ` + "`r`.`task_id`" + ` = ` + "`t`.`id`" + ` AND ` + "`r`.`finished_at`" + ` IS NULL`},

	// Blocked is an annotation on queued, never a state of its own, so it
	// gets its own view rather than another branch of the CASE above.
	// Only a link marked blocks (LinkKind.Blocks: depends-on, child-of)
	// counts, and it counts until its target's observation says closed --
	// including a target that names no task at all, which therefore
	// blocks for ever. That is deliberate (it fails closed, the same as
	// IsBlocked), and it is why the filing paths normalise a dependency's
	// id (NormalizeTaskID) and orchestrator.relayProposedTasks keeps a
	// depends_on entry naming no task in the proposal's body rather than
	// filing it as a link.
	{"task_blocked", `
SELECT
  ` + "`l`.`task_id`" + ` AS ` + "`task_id`" + `,
  COUNT(*)      AS ` + "`open_blockers`" + `
FROM ` + "`task_link`" + ` AS ` + "`l`" + `
LEFT JOIN ` + "`task_observation`" + ` AS ` + "`bo`" + ` ON ` + "`bo`.`task_id`" + ` = ` + "`l`.`target`" + `
WHERE ` + "`l`.`blocks`" + ` = TRUE AND ` + "`bo`.`closed_at`" + ` IS NULL
GROUP BY ` + "`l`.`task_id`" + ``},

	// What is dispatchable right now: approved, not running, nothing open
	// in front of it. The whole dispatch query, as one view.
	{"task_ready", `
SELECT ` + "`s`.`task_id`" + ` AS ` + "`task_id`" + `
FROM ` + "`task_state`" + ` AS ` + "`s`" + `
LEFT JOIN ` + "`task_blocked`" + ` AS ` + "`b`" + ` ON ` + "`b`.`task_id`" + ` = ` + "`s`.`task_id`" + `
WHERE ` + "`s`.`state`" + ` = 'queued'
  AND COALESCE(` + "`b`.`open_blockers`" + `, 0) = 0
  AND NOT EXISTS (SELECT 1 FROM ` + "`task_pause`" + ` AS ` + "`p`" + ` WHERE ` + "`p`.`task_id`" + ` = ` + "`s`.`task_id`" + `)`},

	// Every lease still outstanding, with the credential that minted it —
	// which makes "what would I break by rotating this?" a query.
	{"lease_live", `
SELECT
  ` + "`l`.`run_id`" + `     AS ` + "`run_id`" + `,
  ` + "`r`.`task_id`" + `    AS ` + "`task_id`" + `,
  ` + "`l`.`capability`" + ` AS ` + "`capability`" + `,
  ` + "`l`.`resource`" + `   AS ` + "`resource`" + `,
  ` + "`l`.`minted_by`" + `  AS ` + "`minted_by`" + `,
  ` + "`l`.`issued_at`" + `  AS ` + "`issued_at`" + `,
  ` + "`l`.`expires_at`" + ` AS ` + "`expires_at`" + `
FROM ` + "`lease`" + ` AS ` + "`l`" + `
JOIN ` + "`task_run`" + ` AS ` + "`r`" + ` ON ` + "`r`.`id`" + ` = ` + "`l`.`run_id`" + `
WHERE ` + "`r`.`finished_at`" + ` IS NULL`},
}

// View is one derivation: a name, and the SELECT that defines it.
type View struct {
	Name string
	DDL  string // a SELECT statement, with no CREATE VIEW/AS around it
}

// Statements is the order Open applies things in: tables, then views
// (each dropped first, so a changed definition always wins over
// whatever an older build left behind -- a view has no data of its own
// to lose), a view referencing a missing table being an error.
func Statements() []string {
	out := make([]string, 0, len(Tables)+len(Views)*2)
	out = append(out, Tables...)
	for _, v := range Views {
		out = append(out,
			"DROP VIEW IF EXISTS `"+v.Name+"`",
			"CREATE VIEW `"+v.Name+"` AS"+v.DDL)
	}
	return out
}

// apiScopeSetTable is api_scope_set's DDL, named so the migration that
// adds it (47) runs the same statement Tables does.
const apiScopeSetTable = `CREATE TABLE IF NOT EXISTS ` + "`api_scope_set`" + ` (
  ` + "`name`" + `            TEXT    NOT NULL,
  ` + "`description`" + `     TEXT    NOT NULL,
  ` + "`scopes`" + `          TEXT    NOT NULL,
  ` + "`includes`" + `        TEXT    NOT NULL,
  ` + "`repos`" + `           TEXT    NOT NULL,
  ` + "`deny`" + `            TEXT    NOT NULL,
  ` + "`requires_person`" + ` INTEGER NOT NULL DEFAULT 0,
  ` + "`version`" + `         INTEGER NOT NULL DEFAULT 0,
  ` + "`deprecated`" + `      INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (` + "`name`" + `)
)`
