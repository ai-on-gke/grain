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

// Token accounting: what each run spent at its model provider, and what
// grain has learned about each connection's budget (grain/task-3267).
//
// Before this, grain learned about quota only when a provider refused a
// run (agent.UsageLimitError, a usage_limit_pause). It knew nothing about
// how many tokens runs used, so an operator had to guess max_workers and
// the default model order. Three tables back the accounting:
//
//   - task_run_usage: one row per finished run that reported usage, keyed
//     by run and carrying the connection, model, framework, host and task
//     it ran on. Pruned after UsageSettings.RetentionDays.
//   - usage_capacity: one row per (connection, model), what the window
//     that ended in the latest usage-limit pause had consumed when it
//     ended -- grain's estimate of that window's capacity where the
//     operator configured none.
//   - usage_setting: one row, the operator's budgets and the concurrency
//     mode (UsageSettings), as JSON.
//
// The first two are runtime state, off SettingsTables like
// usage_limit_pause. usage_setting is the operator's own choice and is a
// settings table (staterepo.SettingsTables): it is in the state
// repository as tables/usage_setting.json, so a deployment rebuilt from
// that repository keeps its budgets and mode.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// taskRunUsageTable, usageCapacityTable and usageSettingTable are named
// once for Tables and for migration 130, which adds them.
const taskRunUsageTable = `CREATE TABLE IF NOT EXISTS ` + "`task_run_usage`" + ` (
  ` + "`run_id`" + `             TEXT     NOT NULL,
  ` + "`task_id`" + `            TEXT     NOT NULL DEFAULT '',
  ` + "`connection_id`" + `      TEXT     NOT NULL DEFAULT '',
  ` + "`team`" + `               TEXT     NOT NULL DEFAULT '',
  ` + "`model`" + `              TEXT     NOT NULL DEFAULT '',
  ` + "`framework`" + `          TEXT     NOT NULL DEFAULT '',
  ` + "`hostd`" + `              TEXT     NOT NULL DEFAULT '',
  ` + "`input_tokens`" + `       INTEGER  NOT NULL DEFAULT 0,
  ` + "`output_tokens`" + `      INTEGER  NOT NULL DEFAULT 0,
  ` + "`cache_read_tokens`" + `  INTEGER  NOT NULL DEFAULT 0,
  ` + "`cache_write_tokens`" + ` INTEGER  NOT NULL DEFAULT 0,
  ` + "`cost_micro_usd`" + `     INTEGER  NOT NULL DEFAULT 0,
  ` + "`turns`" + `              INTEGER  NOT NULL DEFAULT 0,
  ` + "`duration_ms`" + `        INTEGER  NOT NULL DEFAULT 0,
  ` + "`started_at`" + `         DATETIME NOT NULL,
  ` + "`finished_at`" + `        DATETIME NOT NULL,
  PRIMARY KEY (` + "`run_id`" + `)
)`

const taskRunUsageFinishedIndex = `CREATE INDEX IF NOT EXISTS ` + "`task_run_usage_finished`" + ` ON ` + "`task_run_usage`" + ` (` + "`finished_at`" + `)`

const taskRunUsageTaskIndex = `CREATE INDEX IF NOT EXISTS ` + "`task_run_usage_task`" + ` ON ` + "`task_run_usage`" + ` (` + "`task_id`" + `)`

const usageCapacityTable = `CREATE TABLE IF NOT EXISTS ` + "`usage_capacity`" + ` (
  ` + "`connection_id`" + ` TEXT     NOT NULL,
  ` + "`model`" + `         TEXT     NOT NULL,
  ` + "`tokens`" + `        INTEGER  NOT NULL DEFAULT 0,
  ` + "`window_ms`" + `     INTEGER  NOT NULL DEFAULT 0,
  ` + "`observed_at`" + `   DATETIME NOT NULL,
  ` + "`reset_at`" + `      DATETIME NULL,
  PRIMARY KEY (` + "`connection_id`" + `, ` + "`model`" + `)
)`

const usageSettingTable = `CREATE TABLE IF NOT EXISTS ` + "`usage_setting`" + ` (
  ` + "`id`" + `         INTEGER  NOT NULL,
  ` + "`value`" + `      TEXT     NOT NULL DEFAULT '',
  ` + "`updated_at`" + ` DATETIME NOT NULL,
  PRIMARY KEY (` + "`id`" + `)
)`

// RunUsage is one task_run_usage row: what one run spent, and where.
type RunUsage struct {
	RunID string `json:"runId"`
	// TaskID is the task the run belonged to.
	TaskID string `json:"taskId,omitempty"`
	// Connection is the connection that owns the credential the run
	// spent (AgentBackend.CredentialOwnerID) -- the same key a
	// usage-limit pause is filed under -- and Team that connection's.
	Connection string `json:"connection,omitempty"`
	Team       string `json:"team,omitempty"`
	// Model is the model the run passed its CLI, lower-cased as a pause
	// names it; "" when grain could not name one.
	Model     string `json:"model,omitempty"`
	Framework string `json:"framework,omitempty"`
	// Hostd is the host whose CLI and credential ran the run, "" for the
	// daemon's own.
	Hostd            string    `json:"host,omitempty"`
	InputTokens      int64     `json:"inputTokens"`
	OutputTokens     int64     `json:"outputTokens"`
	CacheReadTokens  int64     `json:"cacheReadTokens"`
	CacheWriteTokens int64     `json:"cacheWriteTokens"`
	CostUSD          float64   `json:"costUsd,omitempty"`
	Turns            int       `json:"turns,omitempty"`
	StartedAt        time.Time `json:"startedAt"`
	FinishedAt       time.Time `json:"finishedAt"`
}

// Billable is the figure budgets count in: input, output and cache
// writes (agent.TokenUsage.Billable).
func (u RunUsage) Billable() int64 { return u.InputTokens + u.OutputTokens + u.CacheWriteTokens }

// Duration is how long the run took.
func (u RunUsage) Duration() time.Duration {
	if u.FinishedAt.Before(u.StartedAt) {
		return 0
	}
	return u.FinishedAt.Sub(u.StartedAt)
}

// RecordRunUsage records what u's run spent, replacing any earlier row
// for the same run (an adopted run is finished once, but a retry of the
// write must not count it twice). The host is read from task_run_hostd
// when u names none.
func (s *Store) RecordRunUsage(ctx context.Context, u RunUsage) error {
	if strings.TrimSpace(u.RunID) == "" {
		return errors.New("recording run usage: no run id")
	}
	return s.write(ctx, "record run "+u.RunID+"'s token usage", func(tx *sql.Tx) error {
		hostd := u.Hostd
		if hostd == "" {
			var h sql.NullString
			err := tx.QueryRowContext(ctx, "SELECT `hostd` FROM `task_run_hostd` WHERE `run_id` = ?", u.RunID).Scan(&h)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			hostd = h.String
		}
		_, err := tx.ExecContext(ctx,
			"REPLACE INTO `task_run_usage` (`run_id`,`task_id`,`connection_id`,`team`,`model`,`framework`,`hostd`,"+
				"`input_tokens`,`output_tokens`,`cache_read_tokens`,`cache_write_tokens`,`cost_micro_usd`,`turns`,`duration_ms`,"+
				"`started_at`,`finished_at`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
			u.RunID, u.TaskID, u.Connection, u.Team, strings.ToLower(strings.TrimSpace(u.Model)), u.Framework, hostd,
			u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens, int64(u.CostUSD*1e6+0.5), u.Turns,
			u.Duration().Milliseconds(), u.StartedAt.UTC(), u.FinishedAt.UTC())
		return err
	})
}

const runUsageColumns = "`run_id`,`task_id`,`connection_id`,`team`,`model`,`framework`,`hostd`," +
	"`input_tokens`,`output_tokens`,`cache_read_tokens`,`cache_write_tokens`,`cost_micro_usd`,`turns`,`started_at`,`finished_at`"

func scanRunUsages(rows *sql.Rows) ([]RunUsage, error) {
	defer rows.Close()
	var out []RunUsage
	for rows.Next() {
		var u RunUsage
		var micros int64
		if err := rows.Scan(&u.RunID, &u.TaskID, &u.Connection, &u.Team, &u.Model, &u.Framework, &u.Hostd,
			&u.InputTokens, &u.OutputTokens, &u.CacheReadTokens, &u.CacheWriteTokens, &micros, &u.Turns,
			&u.StartedAt, &u.FinishedAt); err != nil {
			return nil, fmt.Errorf("reading run usage: %w", err)
		}
		u.CostUSD = float64(micros) / 1e6
		u.StartedAt, u.FinishedAt = u.StartedAt.UTC(), u.FinishedAt.UTC()
		out = append(out, u)
	}
	return out, rows.Err()
}

// RunUsagesSince is every recorded run that finished at or after since,
// oldest first.
func (s *Store) RunUsagesSince(ctx context.Context, since time.Time) ([]RunUsage, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+runUsageColumns+" FROM `task_run_usage` WHERE `finished_at` >= ? ORDER BY `finished_at`, `run_id`", since.UTC())
	if err != nil {
		return nil, fmt.Errorf("listing run usage: %w", err)
	}
	return scanRunUsages(rows)
}

// TaskRunUsages is every recorded run of taskID, oldest first: what the
// task page totals and the run timeline shows per run.
func (s *Store) TaskRunUsages(ctx context.Context, taskID string) ([]RunUsage, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+runUsageColumns+" FROM `task_run_usage` WHERE `task_id` = ? ORDER BY `finished_at`, `run_id`", taskID)
	if err != nil {
		return nil, fmt.Errorf("listing task %s's run usage: %w", taskID, err)
	}
	return scanRunUsages(rows)
}

// TaskRunUsagesInRange is TaskRunUsages bounded to attempts [minAttempt,
// maxAttempt] when maxAttempt > 0.
func (s *Store) TaskRunUsagesInRange(ctx context.Context, taskID string, minAttempt, maxAttempt int) ([]RunUsage, error) {
	if maxAttempt <= 0 {
		return s.TaskRunUsages(ctx, taskID)
	}
	rows, err := s.db.QueryContext(ctx,
		"SELECT `u`.`run_id`,`u`.`task_id`,`u`.`connection_id`,`u`.`team`,`u`.`model`,`u`.`framework`,`u`.`hostd`,"+
			"`u`.`input_tokens`,`u`.`output_tokens`,`u`.`cache_read_tokens`,`u`.`cache_write_tokens`,`u`.`cost_micro_usd`,`u`.`turns`,`u`.`started_at`,`u`.`finished_at` "+
			"FROM `task_run_usage` AS `u` JOIN `task_run` AS `r` ON `r`.`id` = `u`.`run_id` "+
			"WHERE `u`.`task_id` = ? AND `r`.`attempt` >= ? AND `r`.`attempt` <= ? ORDER BY `u`.`finished_at`, `u`.`run_id`",
		taskID, minAttempt, maxAttempt)
	if err != nil {
		return nil, fmt.Errorf("listing task %s's run usage: %w", taskID, err)
	}
	return scanRunUsages(rows)
}

// TaskUsageSummary is the aggregate token usage recorded across all runs of one task.
type TaskUsageSummary struct {
	Runs             int
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	CostUSD          float64
	Billable         int64
}

// SummarizeTaskUsage sums taskID's recorded run usage in SQL without loading
// every run's row when only a page of runs is displayed. Returns nil when no
// run of taskID has recorded usage.
func (s *Store) SummarizeTaskUsage(ctx context.Context, taskID string) (*TaskUsageSummary, error) {
	var sum TaskUsageSummary
	var costMicros int64
	err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*), COALESCE(SUM(`input_tokens`), 0), COALESCE(SUM(`output_tokens`), 0), "+
			"COALESCE(SUM(`cache_read_tokens`), 0), COALESCE(SUM(`cache_write_tokens`), 0), COALESCE(SUM(`cost_micro_usd`), 0) "+
			"FROM `task_run_usage` WHERE `task_id` = ?", taskID).Scan(
		&sum.Runs, &sum.InputTokens, &sum.OutputTokens, &sum.CacheReadTokens, &sum.CacheWriteTokens, &costMicros)
	if err != nil {
		return nil, fmt.Errorf("summarising task %s's run usage: %w", taskID, err)
	}
	if sum.Runs == 0 {
		return nil, nil
	}
	sum.CostUSD = float64(costMicros) / 1e6
	sum.Billable = sum.InputTokens + sum.OutputTokens + sum.CacheWriteTokens
	return &sum, nil
}

// PruneRunUsage deletes the rows of runs that finished before before,
// reporting how many went.
func (s *Store) PruneRunUsage(ctx context.Context, before time.Time) (int64, error) {
	var n int64
	err := s.write(ctx, "prune run token usage", func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "DELETE FROM `task_run_usage` WHERE `finished_at` < ?", before.UTC())
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	})
	return n, err
}

// UsageCapacity is one usage_capacity row: what a (connection, model)
// window had consumed when the provider refused it -- grain's learned
// estimate of that window's capacity.
type UsageCapacity struct {
	Connection string `json:"connection"`
	// Model is "" for a pause on the whole connection.
	Model      string        `json:"model,omitempty"`
	Tokens     int64         `json:"tokens"`
	Window     time.Duration `json:"-"`
	ObservedAt time.Time     `json:"observedAt"`
	// ResetAt is when the provider said the window would reset (the
	// pause's own until).
	ResetAt time.Time `json:"resetAt,omitempty"`
}

// RecordUsageCapacity records c, replacing the previous observation for
// the same connection and model: the latest limit hit is the best
// estimate of a window's size.
func (s *Store) RecordUsageCapacity(ctx context.Context, c UsageCapacity) error {
	var reset any
	if !c.ResetAt.IsZero() {
		reset = c.ResetAt.UTC()
	}
	return s.write(ctx, "record the usage capacity of "+c.Connection+"/"+c.Model, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"REPLACE INTO `usage_capacity` (`connection_id`,`model`,`tokens`,`window_ms`,`observed_at`,`reset_at`) VALUES (?,?,?,?,?,?)",
			c.Connection, strings.ToLower(strings.TrimSpace(c.Model)), c.Tokens, c.Window.Milliseconds(), c.ObservedAt.UTC(), reset)
		return err
	})
}

// ListUsageCapacities is every learned capacity, by connection and model.
func (s *Store) ListUsageCapacities(ctx context.Context) ([]UsageCapacity, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT `connection_id`,`model`,`tokens`,`window_ms`,`observed_at`,`reset_at` FROM `usage_capacity` ORDER BY `connection_id`, `model`")
	if err != nil {
		return nil, fmt.Errorf("listing usage capacities: %w", err)
	}
	defer rows.Close()
	var out []UsageCapacity
	for rows.Next() {
		var c UsageCapacity
		var windowMS int64
		var reset sql.NullTime
		if err := rows.Scan(&c.Connection, &c.Model, &c.Tokens, &windowMS, &c.ObservedAt, &reset); err != nil {
			return nil, fmt.Errorf("reading a usage capacity: %w", err)
		}
		c.Window = time.Duration(windowMS) * time.Millisecond
		c.ObservedAt = c.ObservedAt.UTC()
		if reset.Valid {
			c.ResetAt = reset.Time.UTC()
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Concurrency modes (UsageSettings.Mode).
const (
	// UsageModeAdvisory computes the sustainable concurrency and shows
	// it, and changes nothing about dispatch. The default.
	UsageModeAdvisory = "advisory"
	// UsageModeAuto also lowers the effective worker cap to the
	// sustainable estimate, within [MinWorkers, max_workers].
	UsageModeAuto = "auto"
	// UsageModeOff computes no estimate at all.
	UsageModeOff = "off"
)

// DefaultUsageRetentionDays is how long task_run_usage rows are kept when
// the operator has not said.
const DefaultUsageRetentionDays = 30

// TokenBudget is a budget an operator knows a connection has: Tokens
// billable tokens (agent.TokenUsage.Billable) per Window, for one model
// on the connection or, with Model empty, for all of them together.
type TokenBudget struct {
	Connection string `json:"connection"`
	Model      string `json:"model,omitempty"`
	Tokens     int64  `json:"tokens"`
	// Window is a Go duration ("5h", "168h").
	Window string `json:"window"`
}

// WindowDuration is Window parsed, 0 when it does not parse.
func (b TokenBudget) WindowDuration() time.Duration {
	d, err := time.ParseDuration(strings.TrimSpace(b.Window))
	if err != nil || d <= 0 {
		return 0
	}
	return d
}

// UsageSettings is the operator's usage configuration: the usage_setting
// row.
type UsageSettings struct {
	// Mode is UsageModeAdvisory (the default, "" reads as it),
	// UsageModeAuto or UsageModeOff.
	Mode string `json:"mode,omitempty"`
	// MinWorkers is the floor the automatic cap never goes below; 0
	// reads as 1.
	MinWorkers int `json:"minWorkers,omitempty"`
	// RetentionDays is how long per-run usage is kept; 0 reads as
	// DefaultUsageRetentionDays.
	RetentionDays int `json:"retentionDays,omitempty"`
	// Budgets are the limits the operator knows of.
	Budgets []TokenBudget `json:"budgets,omitempty"`
}

// EffectiveMode is Mode with "" read as UsageModeAdvisory.
func (u UsageSettings) EffectiveMode() string {
	switch strings.ToLower(strings.TrimSpace(u.Mode)) {
	case UsageModeAuto:
		return UsageModeAuto
	case UsageModeOff:
		return UsageModeOff
	}
	return UsageModeAdvisory
}

// Retention is RetentionDays as a duration, defaulted.
func (u UsageSettings) Retention() time.Duration {
	days := u.RetentionDays
	if days <= 0 {
		days = DefaultUsageRetentionDays
	}
	return time.Duration(days) * 24 * time.Hour
}

// Validate refuses settings that cannot mean anything.
func (u UsageSettings) Validate() error {
	switch strings.ToLower(strings.TrimSpace(u.Mode)) {
	case "", UsageModeAdvisory, UsageModeAuto, UsageModeOff:
	default:
		return fmt.Errorf("usage mode %q: want advisory, auto or off", u.Mode)
	}
	if u.MinWorkers < 0 {
		return fmt.Errorf("minWorkers %d: must not be negative", u.MinWorkers)
	}
	if u.RetentionDays < 0 {
		return fmt.Errorf("retentionDays %d: must not be negative", u.RetentionDays)
	}
	for i, b := range u.Budgets {
		if strings.TrimSpace(b.Connection) == "" {
			return fmt.Errorf("budget %d: no connection", i+1)
		}
		if b.Tokens <= 0 {
			return fmt.Errorf("budget %d (%s): tokens must be positive", i+1, b.Connection)
		}
		if b.WindowDuration() == 0 {
			return fmt.Errorf("budget %d (%s): window %q is not a positive duration such as 5h", i+1, b.Connection, b.Window)
		}
	}
	return nil
}

// UsageSettings reads the usage_setting row; a store that has none, or
// one that does not parse, reads as the zero settings (advisory, no
// budgets).
func (s *Store) UsageSettings(ctx context.Context) (UsageSettings, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, "SELECT `value` FROM `usage_setting` WHERE `id` = 1").Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return UsageSettings{}, nil
	}
	if err != nil {
		return UsageSettings{}, fmt.Errorf("reading usage settings: %w", err)
	}
	var out UsageSettings
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			return UsageSettings{}, nil
		}
	}
	return out, nil
}

// PutUsageSettings replaces the usage settings after validating them.
func (s *Store) PutUsageSettings(ctx context.Context, u UsageSettings, at time.Time) error {
	if err := u.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(u)
	if err != nil {
		return err
	}
	return s.write(ctx, "set usage settings", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "REPLACE INTO `usage_setting` (`id`,`value`,`updated_at`) VALUES (1,?,?)", string(raw), at.UTC())
		return err
	})
}
