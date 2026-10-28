CREATE TABLE IF NOT EXISTS `grain_instance` (
  `singleton` INTEGER NOT NULL CHECK (`singleton` = 1),
  `id`        TEXT    NOT NULL,
  PRIMARY KEY (`singleton`),
  UNIQUE (`id`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `task` (
  `id`                    TEXT    NOT NULL,
  `branch`                TEXT    NOT NULL DEFAULT '',
  `intent`                TEXT    NOT NULL,
  `title`                 TEXT    NOT NULL,
  `body`                  TEXT    NOT NULL,

  `origin_actor_kind`     TEXT    NOT NULL,
  `origin_actor_id`       TEXT    NOT NULL,
  `origin_behalf_kind`    TEXT    NULL,
  `origin_behalf_id`      TEXT    NULL,
  `origin_reason`         TEXT    NOT NULL,

  `approval_actor_kind`   TEXT    NULL,
  `approval_actor_id`     TEXT    NULL,
  `approval_behalf_kind`  TEXT    NULL,
  `approval_behalf_id`    TEXT    NULL,
  `approved_at`           DATETIME NULL,
  `deferred_at`           DATETIME NULL,
  `deferred_until`        DATETIME NULL,
  `undeferred_at`         DATETIME NULL,

  `target_owner`          TEXT    NULL,
  `target_name`           TEXT    NULL,
  `binding`               TEXT    NOT NULL,
  `base`                  TEXT    NULL,
  `folder`                TEXT    NULL,

  `auto_merge`            INTEGER  NOT NULL,
  `auto_approve_proposals` INTEGER NOT NULL DEFAULT 0,
  `created_at`            DATETIME NULL,
  `order_key`             REAL     NOT NULL DEFAULT 0,
  `sandbox_cpus`          INTEGER  NOT NULL DEFAULT 0,
  `sandbox_memory_mb`     INTEGER  NOT NULL DEFAULT 0,
  `sandbox_disk_gb`       INTEGER  NOT NULL DEFAULT 0,
  `interactive`           INTEGER  NOT NULL DEFAULT 0,
  `agent_framework`       TEXT     NOT NULL DEFAULT '',
  `agent_backend_id`      TEXT     NOT NULL DEFAULT '',
  `agent_model`           TEXT     NOT NULL DEFAULT '',
  `prompt_extension`      TEXT     NOT NULL DEFAULT '',
  `review_template_id`    TEXT     NOT NULL DEFAULT '',
  `network_access`        TEXT     NOT NULL DEFAULT '',
  `nothing_needed_action` TEXT     NOT NULL DEFAULT '',
  `creator`               TEXT     NOT NULL DEFAULT '',
  `time_zone`             TEXT     NOT NULL DEFAULT '',
  `issue`                 INTEGER  NOT NULL DEFAULT 0,
  `no_port`               INTEGER  NOT NULL DEFAULT 0,
  `agent_model_source`    TEXT     NOT NULL DEFAULT '',
  `origin_ref`            TEXT     NOT NULL DEFAULT '',
  PRIMARY KEY (`id`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `work_origin_comment` (
  `origin`     TEXT     NOT NULL,
  `comment_id` INTEGER  NOT NULL DEFAULT 0,
  `body`       TEXT     NOT NULL DEFAULT '',
  `checked_at` DATETIME NULL,
  PRIMARY KEY (`origin`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `task_read` (
  `task_id` TEXT NOT NULL,
  `owner`   TEXT NOT NULL,
  `name`    TEXT NOT NULL,
  PRIMARY KEY (`task_id`, `owner`, `name`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `task_write` (
  `task_id` TEXT NOT NULL,
  `owner`   TEXT NOT NULL,
  `name`    TEXT NOT NULL,
  `base`    TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (`task_id`, `owner`, `name`, `base`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `task_filed_template` (
  `task_id`     TEXT NOT NULL,
  `template_id` TEXT NOT NULL,
  PRIMARY KEY (`task_id`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `task_grant_wait` (
  `task_id`    TEXT     NOT NULL,
  `capability` TEXT     NOT NULL,
  `resource`   TEXT     NOT NULL,
  `waiting_at` DATETIME NOT NULL,
  PRIMARY KEY (`task_id`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `task_grant` (
  `task_id`    TEXT NOT NULL,
  `capability` TEXT NOT NULL,
  `via`        TEXT NOT NULL,
  `folder`     TEXT NULL,
  PRIMARY KEY (`task_id`, `capability`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `task_link` (
  `task_id` TEXT    NOT NULL,
  `kind`    TEXT    NOT NULL,
  `target`  TEXT    NOT NULL,
  `blocks`  INTEGER NOT NULL,
  PRIMARY KEY (`task_id`, `kind`, `target`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `task_tag` (
  `task_id` TEXT NOT NULL,
  `tag`     TEXT NOT NULL,
  PRIMARY KEY (`task_id`, `tag`)
)
-- grain:end-of-statement
CREATE INDEX IF NOT EXISTS `task_tag_tag` ON `task_tag` (`tag`, `task_id`)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `task_custom_field` (
  `task_id` TEXT NOT NULL,
  `name`    TEXT NOT NULL,
  `value`   TEXT NOT NULL,
  PRIMARY KEY (`task_id`, `name`, `value`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `task_observation` (
  `task_id`                     TEXT     NOT NULL,
  `closed_at`                   DATETIME NULL,
  `completed_at`                DATETIME NULL,
  `pending_question_comment_id` INTEGER  NULL,
  `baseline_comment_id`         INTEGER  NULL,
  `pending_secret`              TEXT     NULL,
  `merge_queue_blocked_at`      DATETIME NULL,
  `merge_queue_refreshed_at`    DATETIME NULL,
  `merge_queue_repair_at`       DATETIME NULL,
  `merge_queue_state`           TEXT     NULL,
  `merge_queue_note`            TEXT     NULL,
  `merge_queue_state_at`        DATETIME NULL,
  `merge_queue_stalled_at`      DATETIME NULL,
  `merge_queue_inherited`       TEXT     NULL,
  `merge_queue_blocked_base`    TEXT     NULL,
  `merge_queue_rearms`          INTEGER  NULL,
  `merge_queue_repairs`         INTEGER  NULL,
  `merge_queue_repair_base`     TEXT     NULL,
  `merge_queue_repair_failures` TEXT     NULL,
  `merge_queue_repair_why`      TEXT     NULL,
  `merge_queue_stall_retry_at`   DATETIME NULL,
  `merge_queue_stall_retry_head` TEXT     NULL,
  `merge_queue_stall_retry_why`  TEXT     NULL,
  `merge_queue_entered_at`      DATETIME NULL,
  `observed_at`                 DATETIME NULL,
  `retry_requested_at`          DATETIME NULL,
  `run_now_at`                  DATETIME NULL,
  `pr_opened_at`                DATETIME NULL,
  `pr_merged_at`                DATETIME NULL,
  `pr_closed_at`                DATETIME NULL,
  `pr_additions`                INTEGER  NULL,
  `pr_deletions`                INTEGER  NULL,
  `pr_changed_files`            INTEGER  NULL,
  `ci_run_id`                   TEXT     NULL,
  `ci_commit`                   TEXT     NULL,
  `ci_started_at`               DATETIME NULL,
  `ci_conclusion`               TEXT     NULL,
  `ci_finished_at`              DATETIME NULL,
  `ci_detail`                   TEXT     NULL,
  `ci_attempts`                 INTEGER  NULL,
  `ci_previous_conclusion`      TEXT     NULL,
  `ci_previous_detail`          TEXT     NULL,
  `ci_cancelled_by`             TEXT     NULL,
  `ci_recheck_of`               TEXT     NULL,
  `ci_recheck_detail`           TEXT     NULL,
  `suspended_session_token`     TEXT     NULL,
  `suspended_session_framework` TEXT     NULL,
  `suspended_session_run_id`    TEXT     NULL,
  `suspended_session_at`        DATETIME NULL,
  `force_merge_at`              DATETIME NULL,
  `force_merge_reason`          TEXT     NULL,
  `force_merge_by`              TEXT     NULL,
  `force_merge_skipped`         TEXT     NULL,
  `force_merge_head_sha`        TEXT     NULL,
  `streak_count`                 INTEGER  NOT NULL DEFAULT 0,
  `last_failed_at`               DATETIME NULL,
  `last_succeeded_at`            DATETIME NULL,
  `delivered_comment_id`         INTEGER  NULL,
  PRIMARY KEY (`task_id`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `task_run` (
  `id`               TEXT     NOT NULL,
  `task_id`          TEXT     NOT NULL,
  `sandbox`          TEXT     NOT NULL,
  `unit`             TEXT     NULL,
  `attempt`          INTEGER  NOT NULL,
  `started_at`       DATETIME NOT NULL,
  `agent_started_at` DATETIME NULL,
  `finished_at`      DATETIME NULL,
  `outcome`          TEXT     NULL,
  `detail`           TEXT     NULL,
  `transcript`       TEXT     NULL,
  `prompt`           TEXT     NULL,
  `activity`         TEXT     NULL,
  `activity_at`      DATETIME NULL,
  `target_owner`     TEXT     NULL,
  `target_name`      TEXT     NULL,
  PRIMARY KEY (`id`)
)
-- grain:end-of-statement
CREATE UNIQUE INDEX IF NOT EXISTS `task_run_open_task` ON `task_run` (`task_id`) WHERE `finished_at` IS NULL
-- grain:end-of-statement
CREATE INDEX IF NOT EXISTS `task_run_task_outcome_started` ON `task_run` (`task_id`, `outcome`, `started_at`)
-- grain:end-of-statement
CREATE INDEX IF NOT EXISTS `task_run_started` ON `task_run` (`started_at`)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `task_run_tool` (
  `run_id`           TEXT     NOT NULL,
  `tool`             TEXT     NOT NULL,
  `calls`            INTEGER  NOT NULL,
  `errored`          INTEGER  NOT NULL,
  `timed_out`        INTEGER  NOT NULL,
  `elided`           INTEGER  NOT NULL DEFAULT 0,
  `result_bytes`     INTEGER  NOT NULL,
  `max_result_bytes` INTEGER  NOT NULL,
  `size_buckets`     TEXT     NULL,
  `dropped_buckets`  TEXT     NULL,
  PRIMARY KEY (`run_id`, `tool`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `task_run_check_wait` (
  `run_id`        TEXT     NOT NULL,
  `seq`           INTEGER  NOT NULL,
  `verdict`       TEXT     NOT NULL,
  `waited_ms`     INTEGER  NOT NULL,
  `pushes_before` INTEGER  NOT NULL,
  PRIMARY KEY (`run_id`, `seq`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `task_run_wait_credit` (
  `run_id`      TEXT     NOT NULL,
  `blocked_ms`  INTEGER  NOT NULL,
  `credited_ms` INTEGER  NOT NULL,
  PRIMARY KEY (`run_id`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `task_run_spool_cursor` (
  `run_id`     TEXT     NOT NULL,
  `offset`     INTEGER  NOT NULL,
  `updated_at` DATETIME NOT NULL,
  PRIMARY KEY (`run_id`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `task_run_unmatched_failure` (
  `run_id`    TEXT     NOT NULL,
  `task_id`   TEXT     NOT NULL,
  `framework` TEXT     NOT NULL,
  `at`        DATETIME NOT NULL,
  `text`      TEXT     NOT NULL,
  `suspect`   INTEGER  NOT NULL,
  PRIMARY KEY (`run_id`)
)
-- grain:end-of-statement
CREATE INDEX IF NOT EXISTS `task_run_unmatched_failure_at` ON `task_run_unmatched_failure` (`at`)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `lease` (
  `run_id`     TEXT     NOT NULL,
  `capability` TEXT     NOT NULL,
  `resource`   TEXT     NOT NULL,
  `minted_by`  TEXT     NOT NULL,
  `issued_at`  DATETIME NOT NULL,
  `expires_at` DATETIME NULL,
  PRIMARY KEY (`run_id`, `capability`, `resource`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `task_comment` (
  `id`                 INTEGER PRIMARY KEY AUTOINCREMENT,
  `task_id`            TEXT    NOT NULL,
  `author_kind`        TEXT    NOT NULL,
  `author_id`          TEXT    NOT NULL,
  `author_behalf_kind` TEXT     NULL,
  `author_behalf_id`   TEXT     NULL,
  `body`               TEXT     NOT NULL,
  `created_at`         DATETIME NOT NULL,
  `handover`           INTEGER  NOT NULL DEFAULT 0
)
-- grain:end-of-statement
CREATE INDEX IF NOT EXISTS `task_comment_task` ON `task_comment` (`task_id`)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `task_attachment` (
  `id`           INTEGER PRIMARY KEY AUTOINCREMENT,
  `task_id`      TEXT     NOT NULL,
  `comment_id`   INTEGER  NULL,
  `filename`     TEXT     NOT NULL,
  `content_type` TEXT     NOT NULL,
  `size`         INTEGER  NOT NULL,
  `content`      BLOB     NOT NULL,
  `created_at`   DATETIME NOT NULL
)
-- grain:end-of-statement
CREATE INDEX IF NOT EXISTS `task_attachment_task` ON `task_attachment` (`task_id`)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `task_sequence` (
  `number`    INTEGER PRIMARY KEY AUTOINCREMENT,
  `issued_at` DATETIME NOT NULL
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `schedule` (
  `id`                    TEXT     NOT NULL,
  `title`                 TEXT     NOT NULL,
  `body`                  TEXT     NOT NULL,
  `target_owner`          TEXT     NOT NULL,
  `target_name`           TEXT     NOT NULL,
  `base`                  TEXT     NULL,
  `auto_merge`            INTEGER  NOT NULL,
  `auto_approve_proposals` INTEGER NOT NULL DEFAULT 0,
  `template_id`           TEXT     NULL,
  `suite_id`              TEXT     NULL,
  `recurrence_kind`       TEXT     NOT NULL,
  `every_n_hours`         INTEGER  NULL,
  `time_of_day_minutes`   INTEGER  NULL,
  `weekday`               INTEGER  NULL,
  `day_of_month`          INTEGER  NULL,
  `overlap`               TEXT     NOT NULL DEFAULT 'skip',
  `network_access`        TEXT     NOT NULL DEFAULT '',
  `nothing_needed_action` TEXT     NOT NULL DEFAULT '',
  `agent_model`           TEXT     NOT NULL DEFAULT '',
  `time_zone`             TEXT     NOT NULL DEFAULT '',
  `workflow_review`       TEXT     NOT NULL DEFAULT '',
  `enabled`               INTEGER  NOT NULL,
  `next_run_at`           DATETIME NOT NULL,
  `last_run_at`           DATETIME NULL,
  `created_at`            DATETIME NOT NULL,
  PRIMARY KEY (`id`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `schedule_sequence` (
  `number`    INTEGER PRIMARY KEY AUTOINCREMENT,
  `issued_at` DATETIME NOT NULL
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `schedule_read` (
  `schedule_id` TEXT NOT NULL,
  `owner`       TEXT NOT NULL,
  `name`        TEXT NOT NULL,
  PRIMARY KEY (`schedule_id`, `owner`, `name`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `schedule_write` (
  `schedule_id` TEXT NOT NULL,
  `owner`       TEXT NOT NULL,
  `name`        TEXT NOT NULL,
  `base`        TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (`schedule_id`, `owner`, `name`, `base`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `schedule_grant` (
  `schedule_id` TEXT NOT NULL,
  `capability`  TEXT NOT NULL,
  `via`         TEXT NOT NULL,
  `folder`      TEXT NULL,
  PRIMARY KEY (`schedule_id`, `capability`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `schedule_custom_field` (
  `schedule_id` TEXT NOT NULL,
  `name`        TEXT NOT NULL,
  `value`       TEXT NOT NULL,
  PRIMARY KEY (`schedule_id`, `name`, `value`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `template` (
  `id`           TEXT     NOT NULL,
  `name`         TEXT     NOT NULL,
  `title`        TEXT     NOT NULL,
  `body`         TEXT     NOT NULL,
  `target_owner` TEXT     NOT NULL,
  `target_name`  TEXT     NOT NULL,
  `base`         TEXT     NULL,
  `auto_merge`   INTEGER  NOT NULL,
  `created_at`   DATETIME NOT NULL,
  `network_access` TEXT   NOT NULL DEFAULT '',
  `nothing_needed_action` TEXT NOT NULL DEFAULT '',
  `agent_model`  TEXT     NOT NULL DEFAULT '',
  PRIMARY KEY (`id`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `template_sequence` (
  `number`    INTEGER PRIMARY KEY AUTOINCREMENT,
  `issued_at` DATETIME NOT NULL
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `template_read` (
  `template_id` TEXT NOT NULL,
  `owner`       TEXT NOT NULL,
  `name`        TEXT NOT NULL,
  PRIMARY KEY (`template_id`, `owner`, `name`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `template_write` (
  `template_id` TEXT NOT NULL,
  `owner`       TEXT NOT NULL,
  `name`        TEXT NOT NULL,
  `base`        TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (`template_id`, `owner`, `name`, `base`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `template_grant` (
  `template_id` TEXT NOT NULL,
  `capability`  TEXT NOT NULL,
  `via`         TEXT NOT NULL,
  `folder`      TEXT NULL,
  PRIMARY KEY (`template_id`, `capability`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `template_field` (
  `template_id`   TEXT    NOT NULL,
  `name`          TEXT    NOT NULL,
  `label`         TEXT    NOT NULL,
  `options`       TEXT    NOT NULL,
  `default_value` TEXT    NOT NULL,
  `required`      INTEGER NOT NULL DEFAULT 0,
  `position`      INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (`template_id`, `name`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `template_custom_field` (
  `template_id` TEXT NOT NULL,
  `name`        TEXT NOT NULL,
  `value`       TEXT NOT NULL,
  PRIMARY KEY (`template_id`, `name`, `value`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `custom_field` (
  `name`          TEXT    NOT NULL,
  `label`         TEXT    NOT NULL,
  `type`          TEXT    NOT NULL,
  `options`       TEXT    NOT NULL,
  `description`   TEXT    NOT NULL,
  `default_value` TEXT    NOT NULL,
  `position`      INTEGER NOT NULL DEFAULT 0,
  `multiple`      INTEGER NOT NULL DEFAULT 0,
  `show_on_row`   INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (`name`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `grain_schema` (
  `id`      INTEGER NOT NULL,
  `version` INTEGER NOT NULL,
  PRIMARY KEY (`id`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `grain_config` (
  `id`                         INTEGER NOT NULL,
  `poll_interval_ms`           INTEGER NOT NULL,
  `max_workers`                 INTEGER NOT NULL DEFAULT 1,
  `max_mergers`                 INTEGER NOT NULL DEFAULT 1,
  `max_merges_per_repo`         INTEGER NOT NULL DEFAULT 1,
  `merge_batch_size`            INTEGER NOT NULL DEFAULT 3,
  `merge_gather_window_ms`      INTEGER NOT NULL DEFAULT 0,
  `max_ci_jobs`                 INTEGER NOT NULL DEFAULT 1,
  `max_agent_turns`             INTEGER NOT NULL,
  `github_host`                 TEXT    NOT NULL,
  `github_insecure_http`        INTEGER NOT NULL,
  `gcp_project`                 TEXT    NOT NULL,
  `gcp_service_account_email`   TEXT    NOT NULL,
  `gcp_service_account_count`   INTEGER NOT NULL DEFAULT 1,
  `newest_first`                INTEGER NOT NULL DEFAULT 0,
  `sandbox_backend`              TEXT    NOT NULL DEFAULT '',
  `sandbox_cpus`                INTEGER NOT NULL DEFAULT 0,
  `sandbox_memory_mb`            INTEGER NOT NULL DEFAULT 0,
  `sandbox_disk_gb`              INTEGER NOT NULL DEFAULT 0,
  `sandbox_cpu_overcommit`       REAL    NOT NULL DEFAULT 2,
  `sandbox_memory_overcommit`    REAL    NOT NULL DEFAULT 1,
  `sandbox_memory_reserve_mb`    INTEGER NOT NULL DEFAULT -1,
  `sandbox_disk_floor_share`     REAL    NOT NULL DEFAULT 0.1,
  `sandbox_disk_floor_min_mb`    INTEGER NOT NULL DEFAULT 5120,
  `show_closed_by_default`       INTEGER NOT NULL DEFAULT 0,
  `default_filters`               TEXT    NOT NULL DEFAULT 'f=state:none:deferred|closed&f=kind:none:interactive',
  `agent_backends`               TEXT    NOT NULL DEFAULT '',
  `default_model`                TEXT    NOT NULL DEFAULT '',
  `agent_backend_id`             TEXT    NOT NULL DEFAULT '',
  `model_settings`               TEXT    NOT NULL DEFAULT '',
  `git_credential`               TEXT    NOT NULL DEFAULT '',
  `git_connections`              TEXT    NOT NULL DEFAULT '',
  `approved_by_default`          INTEGER NOT NULL DEFAULT 1,
  `auto_merge_by_default`        INTEGER NOT NULL DEFAULT 1,
  `task_defaults_on_backfilled`  INTEGER NOT NULL DEFAULT 1,
  `default_capabilities`         TEXT    NOT NULL DEFAULT '',
  `environment_name`             TEXT    NOT NULL DEFAULT '',
  `prompt_extension`             TEXT    NOT NULL DEFAULT '',
  `task_branch_template`          TEXT    NOT NULL DEFAULT '',
  `deployment_id`                TEXT    NOT NULL DEFAULT '',
  `time_zone`                    TEXT    NOT NULL DEFAULT 'America/Los_Angeles',
  `upgrade_check_mode`           TEXT    NOT NULL DEFAULT 'auto',
  `upgrade_check_pauses`         INTEGER NOT NULL DEFAULT 0,
  `network_access`               TEXT    NOT NULL DEFAULT '',
  `console_sessions`             TEXT    NOT NULL DEFAULT '',
  `sandbox_setup_script`         TEXT    NOT NULL DEFAULT '',
  `package_cache_enabled`        INTEGER NOT NULL DEFAULT 1,
  `package_cache_size_gb`        INTEGER NOT NULL DEFAULT 0,
  `actions_cache_enabled`        INTEGER NOT NULL DEFAULT 1,
  `actions_cache_size_gb`        INTEGER NOT NULL DEFAULT 0,
  `ci_run_history`               INTEGER NOT NULL DEFAULT 0,
  `ci_step_output_mb`            INTEGER NOT NULL DEFAULT 0,
  `ci_step_output_days`          INTEGER NOT NULL DEFAULT 0,
  `ci_run_output_kb`             INTEGER NOT NULL DEFAULT 0,
  `ci_cache_mb`                  INTEGER NOT NULL DEFAULT 0,
  `ci_cache_entries`             INTEGER NOT NULL DEFAULT 0,
  `ci_cache_days`                INTEGER NOT NULL DEFAULT 0,
  `ci_artifact_mb`               INTEGER NOT NULL DEFAULT 0,
  `ci_artifact_run_mb`           INTEGER NOT NULL DEFAULT 0,
  `ci_artifact_file_mb`          INTEGER NOT NULL DEFAULT 0,
  `ci_artifact_days`             INTEGER NOT NULL DEFAULT 0,
  `ci_jobs_per_run`              INTEGER NOT NULL DEFAULT 0,
  `ci_age_boost_ms`              INTEGER NOT NULL DEFAULT 0,
  `ci_reserve_after_ms`          INTEGER NOT NULL DEFAULT 0,
  `max_auto_submit_depth`        INTEGER NOT NULL DEFAULT 2,
  `max_auto_submit_fan_out`      INTEGER NOT NULL DEFAULT 5,
  `iap_backend_service`          TEXT    NOT NULL DEFAULT '',
  `iap_resource`                 TEXT    NOT NULL DEFAULT '',
  `repair_template_id`          TEXT    NOT NULL DEFAULT '',
  `agent_framework`              TEXT    NOT NULL DEFAULT '',
  `agent_git_name`               TEXT    NOT NULL DEFAULT '',
  `agent_git_email`              TEXT    NOT NULL DEFAULT '',
  `target_repos`                 TEXT    NOT NULL DEFAULT '',
  `password_auth_enabled`      INTEGER NOT NULL DEFAULT 1,
  `open_registration`            INTEGER NOT NULL DEFAULT 0,
  `google_oauth_enabled`         INTEGER NOT NULL DEFAULT 0,
  `google_oauth_client_id`       TEXT    NOT NULL DEFAULT '',
  `google_oauth_client_secret`   TEXT    NOT NULL DEFAULT '',
  `google_oauth_allowed_domains` TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (`id`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `pipeline_pause` (
  `id`            INTEGER  NOT NULL,
  `paused_at`     DATETIME NOT NULL,
  `reason`        TEXT     NOT NULL,
  `actor_kind`    TEXT     NOT NULL,
  `actor_id`      TEXT     NOT NULL,
  `until_task_id` TEXT     NOT NULL,
  PRIMARY KEY (`id`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `base_branch_ci` (
  `owner`          TEXT     NOT NULL,
  `name`           TEXT     NOT NULL,
  `branch`         TEXT     NOT NULL,
  `run_id`         TEXT     NOT NULL,
  `commit`         TEXT     NOT NULL,
  `started_at`     DATETIME NOT NULL,
  `finished_at`    DATETIME NULL,
  `attempts`       INTEGER  NOT NULL DEFAULT 0,
  `conclusion`     TEXT     NOT NULL DEFAULT '',
  `detail`         TEXT     NOT NULL DEFAULT '',
  `merged_task_id` TEXT     NOT NULL DEFAULT '',
  `pending_task_id` TEXT    NOT NULL DEFAULT '',
  `repair_task_id` TEXT     NOT NULL DEFAULT '',
  `confirmation`   TEXT     NOT NULL DEFAULT '',
  `confirm_run_id` TEXT     NOT NULL DEFAULT '',
  `confirm_jobs`   TEXT     NOT NULL DEFAULT '',
  `confirm_attempts` INTEGER NOT NULL DEFAULT 0,
  `confirm_detail` TEXT     NOT NULL DEFAULT '',
  `covers`         INTEGER  NOT NULL DEFAULT 0,
  `pending_merges` INTEGER  NOT NULL DEFAULT 0,
  `run_requested_at` DATETIME NULL,
  `run_requested_by` TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (`owner`, `name`, `branch`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `base_branch_dispatch` (
  `owner`       TEXT     NOT NULL,
  `name`        TEXT     NOT NULL,
  `branch`      TEXT     NOT NULL,
  `commit`      TEXT     NOT NULL DEFAULT '',
  `jobs`        TEXT     NOT NULL DEFAULT '',
  `run_id`      TEXT     NOT NULL DEFAULT '',
  `started_at`  DATETIME NOT NULL,
  `finished_at` DATETIME NULL,
  `conclusion`  TEXT     NOT NULL DEFAULT '',
  `detail`      TEXT     NOT NULL DEFAULT '',
  PRIMARY KEY (`owner`, `name`, `branch`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `base_branch_release` (
  `owner`         TEXT     NOT NULL,
  `name`          TEXT     NOT NULL,
  `branch`        TEXT     NOT NULL,
  `commit`        TEXT     NOT NULL DEFAULT '',
  `run_id`        TEXT     NOT NULL DEFAULT '',
  `started_at`    DATETIME NOT NULL,
  `finished_at`   DATETIME NULL,
  `conclusion`    TEXT     NOT NULL DEFAULT '',
  `detail`        TEXT     NOT NULL DEFAULT '',
  `failures`      INTEGER  NOT NULL DEFAULT 0,
  `failing_since` DATETIME NULL,
  `attempts`      INTEGER  NOT NULL DEFAULT 0,
  PRIMARY KEY (`owner`, `name`, `branch`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `merge_batch` (
  `owner`       TEXT     NOT NULL,
  `name`        TEXT     NOT NULL,
  `branch`      TEXT     NOT NULL,
  `ref`         TEXT     NOT NULL,
  `base_commit` TEXT     NOT NULL,
  `commit`      TEXT     NOT NULL,
  `members`     TEXT     NOT NULL DEFAULT '',
  `run_id`      TEXT     NOT NULL DEFAULT '',
  `started_at`  DATETIME NOT NULL,
  `finished_at` DATETIME NULL,
  `conclusion`  TEXT     NOT NULL DEFAULT '',
  `detail`      TEXT     NOT NULL DEFAULT '',
  `splits`      INTEGER  NOT NULL DEFAULT 0,
  `queued`      INTEGER  NOT NULL DEFAULT 0,
  `recheck_of`     TEXT     NOT NULL DEFAULT '',
  `recheck_detail` TEXT     NOT NULL DEFAULT '',
  `recheck_at`     DATETIME NULL,
  PRIMARY KEY (`owner`, `name`, `branch`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `scheduled_workflow_run` (
  `schedule_id`    TEXT     NOT NULL,
  `stem`           TEXT     NOT NULL DEFAULT '',
  `owner`          TEXT     NOT NULL DEFAULT '',
  `name`           TEXT     NOT NULL DEFAULT '',
  `branch`         TEXT     NOT NULL DEFAULT '',
  `workflow`       TEXT     NOT NULL DEFAULT '',
  `run_id`         TEXT     NOT NULL DEFAULT '',
  `fired_at`       DATETIME NOT NULL,
  `attempted_at`   DATETIME NULL,
  `finished_at`    DATETIME NULL,
  `attempts`       INTEGER  NOT NULL DEFAULT 0,
  `conclusion`     TEXT     NOT NULL DEFAULT '',
  `detail`         TEXT     NOT NULL DEFAULT '',
  `unfinished`     INTEGER  NOT NULL DEFAULT 0,
  `review_task_id` TEXT     NOT NULL DEFAULT '',
  `review_not_needed` INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (`schedule_id`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `merge_batch_history` (
  `id`          INTEGER PRIMARY KEY AUTOINCREMENT,
  `owner`       TEXT     NOT NULL,
  `name`        TEXT     NOT NULL,
  `branch`      TEXT     NOT NULL,
  `commit`      TEXT     NOT NULL DEFAULT '',
  `run_id`      TEXT     NOT NULL DEFAULT '',
  `size`        INTEGER  NOT NULL DEFAULT 0,
  `queued`      INTEGER  NOT NULL DEFAULT 0,
  `splits`      INTEGER  NOT NULL DEFAULT 0,
  `merged`      INTEGER  NOT NULL DEFAULT 0,
  `outcome`     TEXT     NOT NULL DEFAULT '',
  `detail`      TEXT     NOT NULL DEFAULT '',
  `post_merge`  INTEGER  NOT NULL DEFAULT 0,
  `started_at`  DATETIME NOT NULL,
  `finished_at` DATETIME NOT NULL
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `merge_gather_history` (
  `id`        INTEGER PRIMARY KEY AUTOINCREMENT,
  `owner`     TEXT     NOT NULL,
  `name`      TEXT     NOT NULL,
  `number`    INTEGER  NOT NULL DEFAULT 0,
  `waiting`   TEXT     NOT NULL DEFAULT '',
  `window_ms` INTEGER  NOT NULL DEFAULT 0,
  `outcome`   TEXT     NOT NULL DEFAULT '',
  `armed_at`  DATETIME NOT NULL,
  `ended_at`  DATETIME NOT NULL
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `scheduled_firing_skip` (
  `id`            INTEGER PRIMARY KEY AUTOINCREMENT,
  `schedule_id`   TEXT     NOT NULL,
  `occurrence`    DATETIME NOT NULL,
  `at`            DATETIME NOT NULL,
  `reason`        TEXT     NOT NULL DEFAULT '',
  `task_id`       TEXT     NOT NULL DEFAULT '',
  `task_state`    TEXT     NOT NULL DEFAULT '',
  `detail`        TEXT     NOT NULL DEFAULT '',
  `outcome`       TEXT     NOT NULL DEFAULT '',
  `resolved_at`   DATETIME,
  `fired_task_id` TEXT     NOT NULL DEFAULT '',
  `folded`        INTEGER  NOT NULL DEFAULT 0,
  `noted_task_id` TEXT     NOT NULL DEFAULT ''
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `merge_queue_contention` (
  `id`      INTEGER PRIMARY KEY AUTOINCREMENT,
  `owner`   TEXT     NOT NULL,
  `name`    TEXT     NOT NULL,
  `task_id` TEXT     NOT NULL DEFAULT '',
  `kind`    TEXT     NOT NULL DEFAULT '',
  `reason`  TEXT     NOT NULL DEFAULT '',
  `at`      DATETIME NOT NULL
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `force_merge_history` (
  `id`           INTEGER PRIMARY KEY AUTOINCREMENT,
  `task_id`      TEXT     NOT NULL,
  `owner`        TEXT     NOT NULL,
  `name`         TEXT     NOT NULL,
  `number`       INTEGER  NOT NULL DEFAULT 0,
  `branch`       TEXT     NOT NULL DEFAULT '',
  `commit`       TEXT     NOT NULL DEFAULT '',
  `reason`       TEXT     NOT NULL DEFAULT '',
  `requested_by` TEXT     NOT NULL DEFAULT '',
  `skipped`      TEXT     NOT NULL DEFAULT '',
  `requested_at` DATETIME NOT NULL,
  `merged_at`    DATETIME NOT NULL
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `scheduled_upgrade` (
  `id`           INTEGER  NOT NULL,
  `branch`       TEXT     NOT NULL,
  `requested_at` DATETIME NOT NULL,
  `actor_kind`   TEXT     NOT NULL,
  `actor_id`     TEXT     NOT NULL,
  PRIMARY KEY (`id`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `tuning_recommendation` (
  `id`               INTEGER PRIMARY KEY AUTOINCREMENT,
  `target_parameter` TEXT     NOT NULL,
  `proposed_value`   TEXT     NOT NULL,
  `cooldown_expiry`  DATETIME NOT NULL,
  `adopted`          INTEGER  NOT NULL DEFAULT 0,
  `task_id`          TEXT     NOT NULL DEFAULT '',
  `created_at`       DATETIME NOT NULL
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `release` (
  `id`                INTEGER PRIMARY KEY AUTOINCREMENT,
  `owner`             TEXT     NOT NULL,
  `repo`              TEXT     NOT NULL,
  `name`              TEXT     NOT NULL,
  `status`            TEXT     NOT NULL,
  `created_at`        DATETIME NOT NULL,
  `merged_at`         DATETIME NULL,
  `pull_request_url`  TEXT     NULL,
  `last_error`        TEXT     NULL,
  `merge_note`        TEXT     NULL
)
-- grain:end-of-statement
CREATE INDEX IF NOT EXISTS `release_repo` ON `release` (`owner`, `repo`, `name`, `id`)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `release_candidate` (
  `id`             INTEGER PRIMARY KEY AUTOINCREMENT,
  `release_id`     INTEGER  NOT NULL,
  `owner`          TEXT     NOT NULL,
  `repo`           TEXT     NOT NULL,
  `number`         INTEGER  NOT NULL,
  `branch`         TEXT     NOT NULL,
  `status`         TEXT     NOT NULL,
  `created_at`     DATETIME NOT NULL,
  `promoted_at`    DATETIME NULL,
  `last_error`     TEXT     NULL
)
-- grain:end-of-statement
CREATE INDEX IF NOT EXISTS `release_candidate_release` ON `release_candidate` (`release_id`, `id`)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `branch` (
  `id`           INTEGER PRIMARY KEY AUTOINCREMENT,
  `owner`        TEXT     NOT NULL,
  `repo`         TEXT     NOT NULL,
  `name`         TEXT     NOT NULL,
  `from_branch`  TEXT     NOT NULL DEFAULT '',
  `status`       TEXT     NOT NULL,
  `created_at`   DATETIME NOT NULL,
  `last_error`   TEXT     NULL
)
-- grain:end-of-statement
CREATE INDEX IF NOT EXISTS `branch_repo` ON `branch` (`owner`, `repo`, `id`)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `repo_config` (
  `owner`                TEXT NOT NULL,
  `name`                 TEXT NOT NULL,
  `clone_url`            TEXT NOT NULL DEFAULT '',
  `default_capabilities` TEXT NOT NULL,
  `prompt_extension`     TEXT NOT NULL DEFAULT '',
  `setup_command`        TEXT NOT NULL DEFAULT '',
  `ci_capabilities`      TEXT NOT NULL DEFAULT '',
  `release_workflows_disabled` INTEGER NOT NULL DEFAULT 0,
  `work_ownership`        TEXT NOT NULL DEFAULT '',
  `work_overrides`        TEXT NOT NULL DEFAULT '',
  `release_secrets`       TEXT NOT NULL DEFAULT '',
  `declarations`          INTEGER NOT NULL DEFAULT 0,
  `require_branch_up_to_date` INTEGER NOT NULL DEFAULT 0,
  `ignore_github_ci`      INTEGER NOT NULL DEFAULT 0,
  `watch_default_branch` INTEGER NOT NULL DEFAULT 0,
  `unfixable_checks`      TEXT NOT NULL DEFAULT '',
  `base_branch_ci_jobs`   TEXT NOT NULL DEFAULT '',
  `post_merge_dispatch_jobs` TEXT NOT NULL DEFAULT '',
  `declared_prompt_extension` TEXT NOT NULL DEFAULT '',
  `declared_setup_command`   TEXT NOT NULL DEFAULT '',
  `ci_mode`                  TEXT NOT NULL DEFAULT '',
  `repair_template_id`       TEXT NOT NULL DEFAULT '',
  `port_branches`            TEXT NOT NULL DEFAULT '',
  `declared_port_branches`   TEXT NOT NULL DEFAULT '',
  `port_types`               TEXT NOT NULL DEFAULT '',
  `declared_port_types`      TEXT NOT NULL DEFAULT '',
  `git_credential`           TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (`owner`, `name`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `gcp_key` (
  `name`                      TEXT     NOT NULL,
  `project_id`                TEXT     NOT NULL,
  `service_account_email`     TEXT     NOT NULL,
  `service_account_count`     INTEGER  NOT NULL DEFAULT 1,
  `minter_credential`         TEXT     NOT NULL DEFAULT '',
  `max_key_age_seconds`       INTEGER  NOT NULL DEFAULT 0,
  `description`               TEXT     NOT NULL DEFAULT '',
  `created_at`                DATETIME NOT NULL,
  `updated_at`                DATETIME NOT NULL,
  PRIMARY KEY (`name`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `repo_workflow` (
  `owner` TEXT NOT NULL,
  `name`  TEXT NOT NULL,
  `path`  TEXT NOT NULL,
  `text`  TEXT NOT NULL,
  PRIMARY KEY (`owner`, `name`, `path`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `repo_declaration` (
  `id`        TEXT NOT NULL,
  `kind`      TEXT NOT NULL,
  `owner`     TEXT NOT NULL,
  `name`      TEXT NOT NULL,
  `path`      TEXT NOT NULL,
  `commit`    TEXT NOT NULL DEFAULT '',
  `synced_at` DATETIME NOT NULL,
  PRIMARY KEY (`id`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `repo_declaration_push` (
  `id`      TEXT    NOT NULL,
  `pattern` TEXT    NOT NULL,
  `allow_force`  INTEGER NOT NULL DEFAULT 0,
  `allow_delete` INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (`id`, `pattern`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `repo_declaration_sync` (
  `owner`      TEXT NOT NULL,
  `name`       TEXT NOT NULL,
  `commit`     TEXT NOT NULL DEFAULT '',
  `checked_at` DATETIME NOT NULL,
  `synced_at`  DATETIME NULL,
  `last_error` TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (`owner`, `name`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `repo_path_trigger` (
  `owner`   TEXT NOT NULL,
  `name`    TEXT NOT NULL,
  `trigger` TEXT NOT NULL,
  `commit`  TEXT NOT NULL DEFAULT '',
  `rule`    TEXT NOT NULL,
  PRIMARY KEY (`owner`, `name`, `trigger`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `repo_merge_rules` (
  `owner`  TEXT NOT NULL,
  `name`   TEXT NOT NULL,
  `commit` TEXT NOT NULL DEFAULT '',
  `rules`  TEXT NOT NULL,
  PRIMARY KEY (`owner`, `name`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `path_trigger_cursor` (
  `owner`      TEXT NOT NULL,
  `name`       TEXT NOT NULL,
  `branch`     TEXT NOT NULL,
  `commit`     TEXT NOT NULL DEFAULT '',
  `updated_at` DATETIME NOT NULL,
  PRIMARY KEY (`owner`, `name`, `branch`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `memory` (
  `owner`           TEXT NOT NULL DEFAULT '',
  `name`            TEXT NOT NULL DEFAULT '',
  `text`            TEXT NOT NULL,
  `updated_at`      DATETIME NULL,
  `updated_by_kind` TEXT NOT NULL DEFAULT '',
  `updated_by_id`   TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (`owner`, `name`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `memory_document` (
  `owner`           TEXT NOT NULL DEFAULT '',
  `name`            TEXT NOT NULL DEFAULT '',
  `document`        TEXT NOT NULL,
  `summary`         TEXT NOT NULL,
  `text`            TEXT NOT NULL,
  `updated_at`      DATETIME NULL,
  `updated_by_kind` TEXT NOT NULL DEFAULT '',
  `updated_by_id`   TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (`owner`, `name`, `document`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `release_note` (
  `period`          TEXT NOT NULL,
  `title`           TEXT NOT NULL DEFAULT '',
  `text`            TEXT NOT NULL,
  `created_at`      DATETIME NULL,
  `updated_at`      DATETIME NULL,
  `updated_by_kind` TEXT NOT NULL DEFAULT '',
  `updated_by_id`   TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (`period`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `qualification_config` (
  `owner`            TEXT    NOT NULL,
  `name`             TEXT    NOT NULL,
  `require_approval` INTEGER NOT NULL,
  `auto_promote`     INTEGER NOT NULL,
  PRIMARY KEY (`owner`, `name`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `qualification_item` (
  `owner`        TEXT    NOT NULL,
  `name`         TEXT    NOT NULL,
  `template_id`  TEXT    NOT NULL,
  `repeat_count` INTEGER NOT NULL,
  `order_key`    REAL    NOT NULL,
  PRIMARY KEY (`owner`, `name`, `template_id`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `qualification_item_depends_on` (
  `owner`                  TEXT NOT NULL,
  `name`                   TEXT NOT NULL,
  `template_id`             TEXT NOT NULL,
  `depends_on_template_id` TEXT NOT NULL,
  PRIMARY KEY (`owner`, `name`, `template_id`, `depends_on_template_id`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `qualification_run` (
  `id`           INTEGER PRIMARY KEY AUTOINCREMENT,
  `owner`        TEXT     NOT NULL,
  `name`         TEXT     NOT NULL,
  `candidate_id` INTEGER  NOT NULL,
  `created_at`   DATETIME NOT NULL
)
-- grain:end-of-statement
CREATE UNIQUE INDEX IF NOT EXISTS `qualification_run_candidate` ON `qualification_run` (`candidate_id`)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `qualification_task` (
  `id`             INTEGER PRIMARY KEY AUTOINCREMENT,
  `run_id`        INTEGER NOT NULL,
  `task_id`       TEXT    NOT NULL,
  `template_id`   TEXT    NOT NULL,
  `template_name` TEXT    NOT NULL,
  `instance_index` INTEGER NOT NULL,
  `repeat_count`  INTEGER NOT NULL
)
-- grain:end-of-statement
CREATE INDEX IF NOT EXISTS `qualification_task_run` ON `qualification_task` (`run_id`)
-- grain:end-of-statement
CREATE UNIQUE INDEX IF NOT EXISTS `qualification_task_task` ON `qualification_task` (`task_id`)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `suite` (
  `id`                TEXT     NOT NULL,
  `name`              TEXT     NOT NULL,
  `mode`              TEXT     NOT NULL,
  `count`             INTEGER  NOT NULL,
  `max_passes`        INTEGER  NOT NULL,
  `require_approval`  INTEGER  NOT NULL,
  `auto_merge`        INTEGER  NOT NULL,
  `auto_approve_proposals` INTEGER NOT NULL DEFAULT 0,
  `created_at`        DATETIME NOT NULL,
  PRIMARY KEY (`id`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `suite_sequence` (
  `id`         INTEGER PRIMARY KEY AUTOINCREMENT,
  `issued_at`  DATETIME NOT NULL
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `suite_item` (
  `id`           INTEGER PRIMARY KEY AUTOINCREMENT,
  `suite_id`     TEXT    NOT NULL,
  `template_id`  TEXT    NOT NULL,
  `order_key`    REAL    NOT NULL
)
-- grain:end-of-statement
CREATE INDEX IF NOT EXISTS `suite_item_suite` ON `suite_item` (`suite_id`, `order_key`)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `suite_run` (
  `id`                INTEGER PRIMARY KEY AUTOINCREMENT,
  `suite_id`          TEXT     NOT NULL,
  `suite_name`        TEXT     NOT NULL,
  `schedule_id`       TEXT     NULL,
  `owner`             TEXT     NOT NULL,
  `repo`              TEXT     NOT NULL,
  `base`              TEXT     NOT NULL,
  `mode`              TEXT     NOT NULL,
  `count`             INTEGER  NOT NULL,
  `max_passes`        INTEGER  NOT NULL,
  `require_approval`  INTEGER  NOT NULL,
  `auto_merge`        INTEGER  NOT NULL,
  `auto_approve_proposals` INTEGER NOT NULL DEFAULT 0,
  `status`            TEXT     NOT NULL,
  `last_error`        TEXT     NULL,
  `created_at`        DATETIME NOT NULL,
  `completed_at`      DATETIME NULL
)
-- grain:end-of-statement
CREATE INDEX IF NOT EXISTS `suite_run_status` ON `suite_run` (`status`)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `suite_run_item` (
  `id`           INTEGER PRIMARY KEY AUTOINCREMENT,
  `run_id`       INTEGER NOT NULL,
  `template_id`  TEXT    NOT NULL,
  `order_key`    REAL    NOT NULL
)
-- grain:end-of-statement
CREATE INDEX IF NOT EXISTS `suite_run_item_run` ON `suite_run_item` (`run_id`, `order_key`)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `suite_run_task` (
  `id`             INTEGER PRIMARY KEY AUTOINCREMENT,
  `run_id`        INTEGER NOT NULL,
  `task_id`       TEXT    NOT NULL,
  `template_id`   TEXT    NOT NULL,
  `template_name` TEXT    NOT NULL,
  `pass_number`   INTEGER NOT NULL
)
-- grain:end-of-statement
CREATE INDEX IF NOT EXISTS `suite_run_task_run` ON `suite_run_task` (`run_id`)
-- grain:end-of-statement
CREATE UNIQUE INDEX IF NOT EXISTS `suite_run_task_task` ON `suite_run_task` (`task_id`)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `board` (
  `id`           TEXT    NOT NULL,
  `name`         TEXT    NOT NULL,
  `folder`       TEXT    NOT NULL DEFAULT '',
  `kind`         TEXT    NOT NULL,
  `description`  TEXT    NOT NULL DEFAULT '',
  `query`        TEXT    NOT NULL DEFAULT '',
  `order_key`    REAL    NOT NULL DEFAULT 0,
  PRIMARY KEY (`id`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `board_sequence` (
  `id`         INTEGER PRIMARY KEY AUTOINCREMENT,
  `issued_at`  DATETIME NOT NULL
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `board_column` (
  `id`         INTEGER PRIMARY KEY AUTOINCREMENT,
  `board_id`   TEXT    NOT NULL,
  `name`       TEXT    NOT NULL,
  `states`     TEXT    NOT NULL,
  `order_key`  REAL    NOT NULL
)
-- grain:end-of-statement
CREATE INDEX IF NOT EXISTS `board_column_board` ON `board_column` (`board_id`, `order_key`)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `board_panel` (
  `id`         INTEGER PRIMARY KEY AUTOINCREMENT,
  `board_id`   TEXT    NOT NULL,
  `kind`       TEXT    NOT NULL,
  `title`      TEXT    NOT NULL DEFAULT '',
  `query`      TEXT    NOT NULL DEFAULT '',
  `range`      TEXT    NOT NULL DEFAULT '',
  `width`      TEXT    NOT NULL DEFAULT '',
  `order_key`  REAL    NOT NULL
)
-- grain:end-of-statement
CREATE INDEX IF NOT EXISTS `board_panel_board` ON `board_panel` (`board_id`, `order_key`)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `board_section` (
  `id`         INTEGER PRIMARY KEY AUTOINCREMENT,
  `board_id`   TEXT    NOT NULL,
  `kind`       TEXT    NOT NULL,
  `title`      TEXT    NOT NULL DEFAULT '',
  `order_key`  REAL    NOT NULL
)
-- grain:end-of-statement
CREATE INDEX IF NOT EXISTS `board_section_board` ON `board_section` (`board_id`, `order_key`)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `ci_run` (
  `id`           TEXT     NOT NULL,
  `run_key`      TEXT     NOT NULL,
  `repo`         TEXT     NOT NULL,
  `ref`          TEXT     NOT NULL,
  `commit_sha`   TEXT     NOT NULL DEFAULT '',
  `event`        TEXT     NOT NULL DEFAULT '',
  `requested_by` TEXT     NOT NULL DEFAULT '',
  `started_at`   DATETIME NULL,
  `finished_at`  DATETIME NULL,
  `conclusion`   TEXT     NOT NULL DEFAULT '',
  `interrupted`  INTEGER  NOT NULL DEFAULT 0,
  `error`        TEXT     NOT NULL DEFAULT '',
  `cancel_reason` TEXT    NOT NULL DEFAULT '',
  `cancelled_by` TEXT     NOT NULL DEFAULT '',
  `build_revision` TEXT   NOT NULL DEFAULT '',
  `build_time`   DATETIME NULL,
  `build_modified` INTEGER NOT NULL DEFAULT 0,
  `recheck`        INTEGER  NOT NULL DEFAULT 0,
  PRIMARY KEY (`id`)
)
-- grain:end-of-statement
CREATE INDEX IF NOT EXISTS `ci_run_key` ON `ci_run` (`run_key`, `conclusion`)
-- grain:end-of-statement
CREATE INDEX IF NOT EXISTS `ci_run_started` ON `ci_run` (`started_at`)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `ci_job` (
  `run_id`        TEXT     NOT NULL,
  `job_index`     INTEGER  NOT NULL,
  `workflow`      TEXT     NOT NULL DEFAULT '',
  `workflow_path` TEXT     NOT NULL DEFAULT '',
  `job_id`        TEXT     NOT NULL DEFAULT '',
  `name`          TEXT     NOT NULL DEFAULT '',
  `job_key`       TEXT     NOT NULL DEFAULT '',
  `cached`        INTEGER  NOT NULL DEFAULT 0,
  `sandbox`       TEXT     NOT NULL DEFAULT '',
  `started_at`    DATETIME NULL,
  `finished_at`   DATETIME NULL,
  `conclusion`    TEXT     NOT NULL DEFAULT '',
  `error`         TEXT     NOT NULL DEFAULT '',
  `activity`      TEXT     NOT NULL DEFAULT '',
  `activity_at`   DATETIME NULL,
  `queued_ms`     INTEGER  NOT NULL DEFAULT 0,
  `queued_cause`  TEXT     NOT NULL DEFAULT '',
  `queued_reason` TEXT     NOT NULL DEFAULT '',
  `retry_count`   INTEGER  NOT NULL DEFAULT 0,
  `retry_reason`  TEXT     NOT NULL DEFAULT '',
  `flake`             INTEGER  NOT NULL DEFAULT 0,
  `quarantined`       INTEGER  NOT NULL DEFAULT 0,
  `quarantined_tests` TEXT     NOT NULL DEFAULT '',
  `status`            TEXT     NOT NULL DEFAULT '',
  `warning`           TEXT     NOT NULL DEFAULT '',
  `failure_summary`   TEXT     NOT NULL DEFAULT '',
  `base_commit`       TEXT     NOT NULL DEFAULT '',
  PRIMARY KEY (`run_id`, `job_index`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `ci_step` (
  `run_id`      TEXT     NOT NULL,
  `job_index`   INTEGER  NOT NULL,
  `step_index`  INTEGER  NOT NULL,
  `name`        TEXT     NOT NULL DEFAULT '',
  `conclusion`  TEXT     NOT NULL DEFAULT '',
  `exit_code`   INTEGER  NOT NULL DEFAULT 0,
  `output`      TEXT     NOT NULL DEFAULT '',
  `error`       TEXT     NOT NULL DEFAULT '',
  `started_at`  DATETIME NULL,
  `finished_at` DATETIME NULL,
  PRIMARY KEY (`run_id`, `job_index`, `step_index`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `ci_step_log_chunk` (
  `run_id`      TEXT     NOT NULL,
  `job_index`   INTEGER  NOT NULL,
  `step_index`  INTEGER  NOT NULL,
  `chunk_index` INTEGER  NOT NULL,
  `output`      TEXT     NOT NULL DEFAULT '',
  `created_at`  DATETIME NULL,
  PRIMARY KEY (`run_id`, `job_index`, `step_index`, `chunk_index`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `sandbox_usage` (
  `kind`        TEXT     NOT NULL,
  `attempted`   INTEGER  NOT NULL DEFAULT 0,
  `created`     INTEGER  NOT NULL DEFAULT 0,
  `finished`    INTEGER  NOT NULL DEFAULT 0,
  `duration_ms` BIGINT   NOT NULL DEFAULT 0,
  `since`       DATETIME NULL,
  `updated_at`  DATETIME NULL,
  PRIMARY KEY (`kind`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `standing_sandbox` (
  `name`           TEXT    NOT NULL,
  `image`          TEXT    NOT NULL,
  `command`        TEXT    NOT NULL DEFAULT '',
  `dir`            TEXT    NOT NULL DEFAULT '',
  `cpus`           INTEGER NOT NULL DEFAULT 0,
  `memory_mb`      INTEGER NOT NULL DEFAULT 0,
  `disk_gb`        INTEGER NOT NULL DEFAULT 0,
  `network_access` TEXT    NOT NULL DEFAULT '',
  `enabled`        INTEGER NOT NULL DEFAULT 1,
  PRIMARY KEY (`name`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `standing_sandbox_secret` (
  `sandbox_name` TEXT NOT NULL,
  `secret_name`  TEXT NOT NULL,
  `env_var`      TEXT NOT NULL DEFAULT '',
  `file_path`    TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (`sandbox_name`, `secret_name`, `env_var`, `file_path`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `standing_sandbox_env` (
  `sandbox_name` TEXT NOT NULL,
  `name`         TEXT NOT NULL,
  `value`        TEXT NOT NULL,
  PRIMARY KEY (`sandbox_name`, `name`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `persistent_sandbox` (
  `name`             TEXT    NOT NULL,
  `repo`             TEXT    NOT NULL,
  `ref`              TEXT    NOT NULL DEFAULT '',
  `auto_update`      INTEGER NOT NULL DEFAULT 0,
  `command`          TEXT    NOT NULL DEFAULT '',
  `dir`              TEXT    NOT NULL DEFAULT '',
  `env`              TEXT    NOT NULL DEFAULT '',
  `capabilities`     TEXT    NOT NULL DEFAULT '',
  `cpus`             INTEGER NOT NULL DEFAULT 0,
  `memory_mb`        INTEGER NOT NULL DEFAULT 0,
  `disk_gb`          INTEGER NOT NULL DEFAULT 0,
  `network_access`   TEXT    NOT NULL DEFAULT '',
  `sandbox_port`     INTEGER NOT NULL DEFAULT 0,
  `host_port`        INTEGER NOT NULL DEFAULT 0,
  `ui_subpath`       TEXT    NOT NULL DEFAULT '',
  `strip_prefix`     INTEGER NOT NULL DEFAULT 0,
  `enabled`          INTEGER NOT NULL DEFAULT 1,
  PRIMARY KEY (`name`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `executor_token` (
  `id`                TEXT     NOT NULL,
  `name`              TEXT     NOT NULL,
  `token_hash`        TEXT     NOT NULL,
  `prefix`            TEXT     NOT NULL,
  `created_at`        DATETIME NOT NULL,
  `revoked_at`        DATETIME NULL,
  `last_connected_at` DATETIME NULL,
  PRIMARY KEY (`id`),
  UNIQUE (`token_hash`)
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `executor_token_sequence` (
  `id`        INTEGER PRIMARY KEY AUTOINCREMENT,
  `issued_at` DATETIME NOT NULL
)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `secret_resolution_audit` (
  `id`          INTEGER PRIMARY KEY AUTOINCREMENT,
  `timestamp`   DATETIME NOT NULL,
  `task_id`     TEXT     NOT NULL DEFAULT '',
  `run_id`      TEXT     NOT NULL DEFAULT '',
  `secret_name` TEXT     NOT NULL DEFAULT '',
  `key`         TEXT     NOT NULL DEFAULT '',
  `principal`   TEXT     NOT NULL DEFAULT ''
)
-- grain:end-of-statement
CREATE INDEX IF NOT EXISTS `secret_resolution_audit_time` ON `secret_resolution_audit` (`timestamp` DESC)
-- grain:end-of-statement
CREATE TABLE IF NOT EXISTS `credential_access_audit` (
  `id`         INTEGER PRIMARY KEY AUTOINCREMENT,
  `timestamp`  DATETIME NOT NULL,
  `task_id`    TEXT     NOT NULL DEFAULT '',
  `run_id`     TEXT     NOT NULL DEFAULT '',
  `sandbox`    TEXT     NOT NULL DEFAULT '',
  `kind`       TEXT     NOT NULL DEFAULT '',
  `resource`   TEXT     NOT NULL DEFAULT '',
  `action`     TEXT     NOT NULL DEFAULT '',
  `credential` TEXT     NOT NULL DEFAULT '',
  `outcome`    TEXT     NOT NULL DEFAULT ''
)
-- grain:end-of-statement
CREATE INDEX IF NOT EXISTS `credential_access_audit_time` ON `credential_access_audit` (`timestamp` DESC)
-- grain:end-of-statement
DROP VIEW IF EXISTS `task_streak`
-- grain:end-of-statement
CREATE VIEW `task_streak` AS
SELECT
  `task_id` AS `task_id`,
  `streak_count` AS `streak`
FROM `task_observation`
WHERE `streak_count` > 0
-- grain:end-of-statement
DROP VIEW IF EXISTS `task_state`
-- grain:end-of-statement
CREATE VIEW `task_state` AS
SELECT
  `t`.`id` AS `task_id`,
  CASE
    WHEN `o`.`closed_at`    IS NOT NULL THEN 'closed'
    WHEN `o`.`completed_at` IS NOT NULL AND `t`.`auto_merge` = FALSE AND EXISTS (
           SELECT 1 FROM `task_link` AS `fl`
           WHERE `fl`.`task_id` = `t`.`id` AND `fl`.`kind` = 'fixes'
         ) THEN 'awaiting_submit'
    WHEN `o`.`completed_at` IS NOT NULL
         AND `t`.`origin_reason` != 'review' AND NOT EXISTS (
           SELECT 1 FROM `task_link` AS `al`
           WHERE `al`.`task_id` = `t`.`id` AND `al`.`kind` = 'fixes'
         ) THEN 'awaiting_reply'
    WHEN `o`.`completed_at` IS NOT NULL THEN 'completed'
    WHEN `o`.`pending_question_comment_id` IS NOT NULL THEN 'awaiting_reply'
    WHEN `r`.`id` IS NOT NULL THEN 'running'
    WHEN COALESCE(`o`.`streak_count`, 0) >= 5 THEN 'failed'
    WHEN `t`.`deferred_at` IS NOT NULL THEN 'deferred'
    WHEN `t`.`approval_actor_kind` IS NULL THEN 'proposed'
    ELSE 'queued'
  END AS `state`
FROM `task` AS `t`
LEFT JOIN `task_observation` AS `o` ON `o`.`task_id` = `t`.`id`
LEFT JOIN `task_run` AS `r`
       ON `r`.`task_id` = `t`.`id` AND `r`.`finished_at` IS NULL
-- grain:end-of-statement
DROP VIEW IF EXISTS `task_blocked`
-- grain:end-of-statement
CREATE VIEW `task_blocked` AS
SELECT
  `l`.`task_id` AS `task_id`,
  COUNT(*)      AS `open_blockers`
FROM `task_link` AS `l`
LEFT JOIN `task_observation` AS `bo` ON `bo`.`task_id` = `l`.`target`
WHERE `l`.`blocks` = TRUE AND `bo`.`closed_at` IS NULL
GROUP BY `l`.`task_id`
-- grain:end-of-statement
DROP VIEW IF EXISTS `task_ready`
-- grain:end-of-statement
CREATE VIEW `task_ready` AS
SELECT `s`.`task_id` AS `task_id`
FROM `task_state` AS `s`
LEFT JOIN `task_blocked` AS `b` ON `b`.`task_id` = `s`.`task_id`
WHERE `s`.`state` = 'queued'
  AND COALESCE(`b`.`open_blockers`, 0) = 0
-- grain:end-of-statement
DROP VIEW IF EXISTS `lease_live`
-- grain:end-of-statement
CREATE VIEW `lease_live` AS
SELECT
  `l`.`run_id`     AS `run_id`,
  `r`.`task_id`    AS `task_id`,
  `l`.`capability` AS `capability`,
  `l`.`resource`   AS `resource`,
  `l`.`minted_by`  AS `minted_by`,
  `l`.`issued_at`  AS `issued_at`,
  `l`.`expires_at` AS `expires_at`
FROM `lease` AS `l`
JOIN `task_run` AS `r` ON `r`.`id` = `l`.`run_id`
WHERE `r`.`finished_at` IS NULL
-- grain:end-of-statement
