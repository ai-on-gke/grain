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

package postgres

import (
	"context"
	"database/sql/driver"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"
)

// Primary keys for model tables in Grain.
var tablePrimaryKeys = map[string][]string{
	"grain_schema":                   {"id"},
	"grain_instance":                 {"singleton"},
	"task":                           {"id"},
	"task_sequence":                  {"number"},
	"task_run":                       {"id"},
	"task_run_tool":                  {"run_id", "tool"},
	"task_run_check_wait":            {"run_id", "seq"},
	"task_run_wait_credit":           {"run_id"},
	"task_run_ci_habit":              {"run_id"},
	"task_run_api_call":              {"run_id", "seq"},
	"task_run_report_check":          {"run_id"},
	"task_run_spool_cursor":          {"run_id"},
	"task_run_staged_attachment":     {"run_id", "path"},
	"task_run_unmatched_failure":     {"run_id"},
	"task_run_exec_failure":          {"run_id"},
	"task_run_capability_audit":      {"run_id", "seq"},
	"task_suspension":                {"run_id"},
	"task_goal":                      {"task_id"},
	"task_link":                      {"task_id", "kind", "target"},
	"task_observation":               {"task_id"},
	"task_tag":                       {"task_id", "tag"},
	"work_origin_comment":            {"origin"},
	"task_comment":                   {"id"},
	"task_event":                     {"id"},
	"task_attachment":                {"id"},
	"task_custom_field":              {"task_id", "name", "value"},
	"task_read":                      {"task_id", "owner", "name"},
	"task_write":                     {"task_id", "owner", "name", "base"},
	"task_filed_template":            {"task_id"},
	"task_grant":                     {"task_id", "capability"},
	"task_grant_wait":                {"task_id"},
	"task_pause":                     {"task_id"},
	"custom_field":                   {"name"},
	"api_scope_set":                  {"name"},
	"lease":                          {"run_id", "capability", "resource"},
	"template":                       {"id"},
	"template_sequence":              {"number"},
	"template_read":                  {"template_id", "owner", "name"},
	"template_write":                 {"template_id", "owner", "name", "base"},
	"template_grant":                 {"template_id", "capability"},
	"template_field":                 {"template_id", "name"},
	"template_custom_field":          {"template_id", "name", "value"},
	"schedule":                       {"id"},
	"schedule_sequence":              {"number"},
	"schedule_read":                  {"schedule_id", "owner", "name"},
	"schedule_write":                 {"schedule_id", "owner", "name", "base"},
	"schedule_grant":                 {"schedule_id", "capability"},
	"schedule_custom_field":          {"schedule_id", "name", "value"},
	"pipeline_pause":                 {"id"},
	"grain_config":                   {"id"},
	"base_branch_ci":                 {"owner", "name", "branch"},
	"base_branch_dispatch":           {"owner", "name", "branch"},
	"base_branch_release":            {"owner", "name", "branch"},
	"merge_batch":                    {"owner", "name", "branch"},
	"scheduled_workflow_run":         {"schedule_id"},
	"merge_batch_history":            {"id"},
	"merge_gather_history":           {"id"},
	"merge_recheck_history":          {"id"},
	"base_hold_history":              {"id"},
	"merge_queue_contention":         {"id"},
	"scheduled_firing_skip":          {"id"},
	"force_merge_history":            {"id"},
	"scheduled_upgrade":              {"id"},
	"auto_upgrade_run":               {"id"},
	"usage_limit_pause":              {"scope_key"},
	"tuning_recommendation":          {"id"},
	"release":                        {"id"},
	"release_candidate":              {"id"},
	"branch":                         {"id"},
	"repo_config":                    {"owner", "name"},
	"repo_workflow":                  {"owner", "name", "path"},
	"repo_declaration":               {"id"},
	"repo_declaration_push":          {"id", "pattern"},
	"repo_declaration_sync":          {"owner", "name"},
	"repo_path_trigger":              {"owner", "name", "trigger"},
	"repo_merge_rules":               {"owner", "name"},
	"path_trigger_cursor":            {"owner", "name", "branch"},
	"memory":                         {"owner", "name"},
	"memory_document":                {"owner", "name", "document"},
	"release_note":                   {"period"},
	"qualification_config":           {"owner", "name"},
	"qualification_item":             {"owner", "name", "template_id"},
	"qualification_item_depends_on":  {"owner", "name", "template_id", "depends_on_template_id"},
	"qualification_run":              {"id"},
	"qualification_task":             {"id"},
	"suite":                          {"id"},
	"suite_sequence":                 {"id"},
	"suite_item":                     {"id"},
	"suite_run":                      {"id"},
	"suite_run_item":                 {"id"},
	"suite_run_task":                 {"id"},
	"board":                          {"id"},
	"board_sequence":                 {"id"},
	"board_column":                   {"id"},
	"board_panel":                    {"id"},
	"board_section":                  {"id"},
	"ci_run":                         {"id"},
	"github_ci_job":                  {"repo", "check_run_id"},
	"ci_job":                         {"run_id", "job_index"},
	"ci_step":                        {"run_id", "job_index", "step_index"},
	"ci_step_log_chunk":              {"run_id", "job_index", "step_index", "chunk_index"},
	"sandbox_usage":                  {"kind"},
	"standing_sandbox":               {"name"},
	"standing_sandbox_secret":        {"sandbox_name", "secret_name", "env_var", "file_path"},
	"standing_sandbox_env":           {"sandbox_name", "name"},
	"persistent_sandbox":             {"name"},
	"executor_token":                 {"id"},
	"executor_token_sequence":        {"id"},
	"external_agent_token":           {"id"},
	"external_agent_token_sequence":  {"id"},
	"gcp_key":                        {"name"},
	"secret_resolution_audit":        {"id"},
	"credential_access_audit":        {"id"},
	"notification":                   {"id"},
	"task_notification_subscription": {"task_id", "family"},
	"sandbox_access":                 {"sandbox"},
	"ci_step_attachment":             {"run_id", "job_id", "step_id"},
}

// Auto-increment primary key column for tables that use sequences or SERIAL.
var autoIncrementTables = map[string]string{
	"task_sequence":                 "number",
	"schedule_sequence":             "number",
	"template_sequence":             "number",
	"suite_sequence":                "id",
	"task_suite_sequence":           "id",
	"board_sequence":                "id",
	"executor_token_sequence":       "id",
	"external_agent_token_sequence": "id",
	"task_comment":                  "id",
	"task_event":                    "id",
	"task_attachment":               "id",
	"branch":                        "id",
	"release":                       "id",
	"release_candidate":             "id",
	"qualification_run":             "id",
	"qualification_task":            "id",
	"suite_item":                    "id",
	"suite_run":                     "id",
	"suite_run_item":                "id",
	"suite_run_task":                "id",
	"board_column":                  "id",
	"board_panel":                   "id",
	"board_section":                 "id",
	"merge_batch_history":           "id",
	"merge_gather_history":          "id",
	"merge_recheck_history":         "id",
	"base_hold_history":             "id",
	"merge_queue_contention":        "id",
	"scheduled_firing_skip":         "id",
	"force_merge_history":           "id",
	"tuning_recommendation":         "id",
	"secret_resolution_audit":       "id",
	"credential_access_audit":       "id",
	"notification":                  "id",
}

type adaptedConnector struct {
	inner driver.Connector
}

func newAdaptedConnector(inner driver.Connector) driver.Connector {
	return &adaptedConnector{inner: inner}
}

func (c *adaptedConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &adaptedConn{inner: conn}, nil
}

func (c *adaptedConnector) Driver() driver.Driver {
	return &adaptedDriver{inner: c.inner.Driver()}
}

type adaptedDriver struct {
	inner driver.Driver
}

func (d *adaptedDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return &adaptedConn{inner: conn}, nil
}

type adaptedConn struct {
	inner driver.Conn
}

func (c *adaptedConn) Prepare(query string) (driver.Stmt, error) {
	adapted := adaptQuery(query)
	stmt, err := c.inner.Prepare(adapted)
	if err != nil {
		return nil, err
	}
	return &adaptedStmt{inner: stmt}, nil
}

func (c *adaptedConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	adapted := adaptQuery(query)
	if pc, ok := c.inner.(driver.ConnPrepareContext); ok {
		stmt, err := pc.PrepareContext(ctx, adapted)
		if err != nil {
			return nil, err
		}
		return &adaptedStmt{inner: stmt}, nil
	}
	return c.Prepare(query)
}

func (c *adaptedConn) Close() error {
	return c.inner.Close()
}

func (c *adaptedConn) Begin() (driver.Tx, error) {
	return c.inner.Begin()
}

func (c *adaptedConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if btx, ok := c.inner.(driver.ConnBeginTx); ok {
		return btx.BeginTx(ctx, opts)
	}
	return c.inner.Begin()
}

func (c *adaptedConn) Ping(ctx context.Context) error {
	if pinger, ok := c.inner.(driver.Pinger); ok {
		return pinger.Ping(ctx)
	}
	return nil
}

func (c *adaptedConn) ResetSession(ctx context.Context) error {
	if resetter, ok := c.inner.(driver.SessionResetter); ok {
		return resetter.ResetSession(ctx)
	}
	return nil
}

func (c *adaptedConn) IsValid() bool {
	if val, ok := c.inner.(driver.Validator); ok {
		return val.IsValid()
	}
	return true
}

func adaptValue(v any) any {
	if s, ok := v.(string); ok {
		if strings.ContainsRune(s, '\x00') {
			s = strings.ReplaceAll(s, "\x00", "")
		}
		if !utf8.ValidString(s) {
			s = strings.ToValidUTF8(s, "\uFFFD")
		}
		return s
	}
	return v
}

func adaptDriverValues(args []driver.Value) []driver.Value {
	if len(args) == 0 {
		return args
	}
	out := make([]driver.Value, len(args))
	for i, arg := range args {
		out[i] = adaptValue(arg)
	}
	return out
}

func adaptArgs(args []driver.NamedValue) []driver.NamedValue {
	if len(args) == 0 {
		return args
	}
	out := make([]driver.NamedValue, len(args))
	for i, arg := range args {
		out[i] = arg
		out[i].Value = adaptValue(arg.Value)
	}
	return out
}

func (c *adaptedConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	adapted := adaptQuery(query)
	args = adaptArgs(args)

	// Check if this is an INSERT statement on an auto-increment table without RETURNING
	if _, col, ok := isAutoIncrementInsert(adapted); ok {
		returningQuery := adapted + ` RETURNING "` + col + `"`
		if qc, ok := c.inner.(driver.QueryerContext); ok {
			rows, err := qc.QueryContext(ctx, returningQuery, args)
			if err != nil {
				return nil, err
			}
			// Read every returned row: an INSERT ... SELECT can insert
			// several, and an error from a later row (a constraint
			// violation, say) surfaces from Next or Close rather than from
			// QueryContext. LastInsertId is the last row's id, as SQLite
			// reports it.
			var res adaptedResult
			dest := make([]driver.Value, 1)
			for {
				err := rows.Next(dest)
				if err == io.EOF {
					break
				}
				if err != nil {
					_ = rows.Close()
					return nil, err
				}
				res.lastInsertID = toInt64(dest[0])
				res.rowsAffected++
			}
			if err := rows.Close(); err != nil {
				return nil, err
			}
			return res, nil
		}
	}

	if ec, ok := c.inner.(driver.ExecerContext); ok {
		res, err := ec.ExecContext(ctx, adapted, args)
		if err != nil {
			return nil, err
		}
		return adaptedResultFrom(res), nil
	}

	// Fallback to Prepare & Exec
	stmt, err := c.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()
	return stmt.(driver.StmtExecContext).ExecContext(ctx, args)
}

func (c *adaptedConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	adapted := adaptQuery(query)
	args = adaptArgs(args)
	if qc, ok := c.inner.(driver.QueryerContext); ok {
		return qc.QueryContext(ctx, adapted, args)
	}

	stmt, err := c.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()
	return stmt.(driver.StmtQueryContext).QueryContext(ctx, args)
}

type adaptedStmt struct {
	inner driver.Stmt
}

func (s *adaptedStmt) Close() error {
	return s.inner.Close()
}

func (s *adaptedStmt) NumInput() int {
	return s.inner.NumInput()
}

func (s *adaptedStmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.inner.Exec(adaptDriverValues(args))
}

func (s *adaptedStmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.inner.Query(adaptDriverValues(args))
}

func (s *adaptedStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	args = adaptArgs(args)
	if sec, ok := s.inner.(driver.StmtExecContext); ok {
		res, err := sec.ExecContext(ctx, args)
		if err != nil {
			return nil, err
		}
		return adaptedResultFrom(res), nil
	}
	dargs := make([]driver.Value, len(args))
	for i, nv := range args {
		dargs[i] = nv.Value
	}
	return s.Exec(dargs)
}

func (s *adaptedStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	args = adaptArgs(args)
	if sqc, ok := s.inner.(driver.StmtQueryContext); ok {
		return sqc.QueryContext(ctx, args)
	}
	dargs := make([]driver.Value, len(args))
	for i, nv := range args {
		dargs[i] = nv.Value
	}
	return s.Query(dargs)
}

type adaptedResult struct {
	lastInsertID int64
	rowsAffected int64
}

func (r adaptedResult) LastInsertId() (int64, error) {
	return r.lastInsertID, nil
}

func (r adaptedResult) RowsAffected() (int64, error) {
	return r.rowsAffected, nil
}

func adaptedResultFrom(r driver.Result) driver.Result {
	if r == nil {
		return adaptedResult{}
	}
	ra, _ := r.RowsAffected()
	li, _ := r.LastInsertId()
	return adaptedResult{lastInsertID: li, rowsAffected: ra}
}

func toInt64(val any) int64 {
	switch v := val.(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case int32:
		return int64(v)
	case uint64:
		return int64(v)
	case uint32:
		return int64(v)
	case uint:
		return int64(v)
	case []byte:
		var n int64
		_, _ = fmt.Sscanf(string(v), "%d", &n)
		return n
	case string:
		var n int64
		_, _ = fmt.Sscanf(v, "%d", &n)
		return n
	default:
		return 0
	}
}

var pragmaTableInfoRe = regexp.MustCompile(`(?i)^\s*PRAGMA\s+table_info\s*\(\s*['"` + "`" + `"]?([a-zA-Z0-9_]+)['"` + "`" + `"]?\s*\)\s*;?\s*$`)

// adaptedQueries memoises adaptQuery by its input. Every rewrite below is
// a pure function of the query text, and the store's queries are constant
// strings (or a handful of shapes of one), so without this the same few
// hundred queries ran every regexp in this file -- four per boolean column
// in adaptBoolExpressions alone -- on every call. adaptedQueryCount bounds
// it: a query built with literals in it would otherwise grow the cache for
// as long as the process lives, so past maxAdaptedQueries new queries are
// still adapted, just no longer remembered.
var (
	adaptedQueries    sync.Map // string -> string
	adaptedQueryCount atomic.Int64
)

const maxAdaptedQueries = 4096

// adaptQuery transforms SQLite SQL into PostgreSQL-compatible SQL,
// remembering the answer for next time (adaptedQueries).
func adaptQuery(query string) string {
	if adapted, ok := adaptedQueries.Load(query); ok {
		return adapted.(string)
	}
	adapted := translateQuery(query)
	if adaptedQueryCount.Load() < maxAdaptedQueries {
		if _, loaded := adaptedQueries.LoadOrStore(query, adapted); !loaded {
			adaptedQueryCount.Add(1)
		}
	}
	return adapted
}

// translateQuery is adaptQuery without the memo: every rewrite, every time.
func translateQuery(query string) string {
	if !utf8.ValidString(query) {
		query = strings.ToValidUTF8(query, "\uFFFD")
	}
	trimmed := strings.TrimSpace(query)

	// Check PRAGMA table_info(...)
	if m := pragmaTableInfoRe.FindStringSubmatch(trimmed); len(m) > 1 {
		tableName := m[1]
		return fmt.Sprintf(`SELECT 
    c.ordinal_position AS cid,
    c.column_name AS name,
    c.data_type AS type,
    CASE WHEN c.is_nullable = 'NO' THEN 1 ELSE 0 END AS notnull,
    c.column_default AS dflt_value,
    CASE WHEN kcu.column_name IS NOT NULL THEN 1 ELSE 0 END AS pk
FROM information_schema.columns c
LEFT JOIN (
    SELECT kcu.table_schema, kcu.table_name, kcu.column_name
    FROM information_schema.table_constraints tc
    JOIN information_schema.key_column_usage kcu
      ON tc.constraint_name = kcu.constraint_name
     AND tc.table_schema = kcu.table_schema
    WHERE tc.constraint_type = 'PRIMARY KEY'
) kcu ON c.table_schema = kcu.table_schema 
     AND c.table_name = kcu.table_name 
     AND c.column_name = kcu.column_name
WHERE c.table_name = '%s'
  AND c.table_schema = CURRENT_SCHEMA()
ORDER BY c.ordinal_position`, tableName)
	}

	// Check other PRAGMAs (e.g. data_version, journal_mode, foreign_keys, synchronous)
	if strings.HasPrefix(strings.ToUpper(trimmed), "PRAGMA ") {
		upper := strings.ToUpper(trimmed)
		if strings.Contains(upper, "DATA_VERSION") {
			return "SELECT (pg_stat_get_db_xact_commit(d.oid))::bigint AS data_version FROM pg_database d WHERE datname = current_database()"
		}
		// Harmless no-op query for other PRAGMAs
		return "SELECT 1"
	}

	// Translate quotes (backticks -> double quotes) and placeholders (? -> $1, $2, ...)
	q := translateQuotesAndPlaceholders(query)

	// Adapt functions and SQLite expressions
	q = adaptSqliteFunctions(q)

	// Adapt DDL syntax and types for PostgreSQL
	q = adaptDDL(q)

	// Adapt subquery alias and LIMIT expressions for PostgreSQL
	q = adaptSubqueriesAndLimits(q)

	// Translate REPLACE INTO -> INSERT ... ON CONFLICT (...) DO UPDATE
	if isReplaceInto(q) {
		q = adaptReplaceInto(q)
	}

	// Translate INSERT OR IGNORE INTO -> INSERT ... ON CONFLICT DO NOTHING
	if isInsertOrIgnore(q) {
		q = adaptInsertOrIgnore(q)
	}

	// Disambiguate sandbox_usage conflict columns
	q = adaptSandboxUsage(q)

	// Adapt boolean column integer comparisons (e.g. "col" = 1 -> "col" = TRUE)
	q = adaptBoolExpressions(q)

	// Adapt boolean literals in INSERT INTO ... VALUES (...)
	q = adaptInsertBoolLiterals(q)

	return q
}

var (
	reAutoincrement = regexp.MustCompile(`(?i)\bINTEGER\s+PRIMARY\s+KEY\s+AUTOINCREMENT\b`)
	reDatetime      = regexp.MustCompile(`(?i)\bDATETIME\b`)
	reBlob          = regexp.MustCompile(`(?i)\bBLOB\b`)
	reReal          = regexp.MustCompile(`(?i)\bREAL\b`)
	reInteger       = regexp.MustCompile(`(?i)\bINTEGER\b`)
	reDropView      = regexp.MustCompile(`(?i)\bDROP\s+VIEW\s+IF\s+EXISTS\s+([^\s;]+)(?:\s+CASCADE)?`)
	reInstr         = regexp.MustCompile(`(?i)\binstr\s*\(\s*([^,]+)\s*,\s*([^)]+)\s*\)`)
	reGlobParam     = regexp.MustCompile(`(?i)\bGLOB\s+\$(\d+)`)
	reGlob          = regexp.MustCompile(`(?i)\bGLOB\b`)
	reBlobLength    = regexp.MustCompile(`(?i)\blength\s*\(\s*CAST\s*\(\s*([^()]+?)\s+AS\s+BLOB\s*\)\s*\)`)
	reBlobCast      = regexp.MustCompile(`(?i)\bAS\s+BLOB\b`)
	reBlobType      = regexp.MustCompile(`(?i)::BLOB\b`)
	rePragmaFrom    = regexp.MustCompile(`(?i)\bFROM\s+pragma_table_info\s*\(\s*([^)]+?)\s*\)`)
	reParamIsNull   = regexp.MustCompile(`(?i)\$(\d+)\s+IS\s+(NOT\s+)?NULL\b`)

	// boolColumns are the schema's INTEGER 0/1 flag columns, by name:
	// adaptDDL makes each one BOOLEAN, and every other INTEGER is BIGINT,
	// which pgx will not fill from a Go bool. A flag column missing here
	// fails every write of it on PostgreSQL alone
	// (TestEveryBoolBindNamesABooleanColumn), and one added here after a
	// database was created is converted by convertBooleanColumns.
	boolColumns = []string{
		"auto_merge",
		"tail_fetched",
		"requires_person",
		"deprecated",
		"blocks",
		"auto_approve_proposals",
		"interactive",
		"immediate",
		"no_port",
		"resume_on_deadline",
		"require_approval",
		"auto_promote",
		"enabled",
		"auto_update",
		"strip_prefix",
		"rollback_on_crash",
		"unfinished",
		"interrupted",
		"build_modified",
		"recheck",
		"cached",
		"flake",
		"quarantined",
		"approved_by_default",
		"auto_merge_by_default",
		"require_branch_up_to_date",
		"watch_default_branch",
		"merge_on_green",
		"base_repair_bypasses_human_gates",
		"issue_import_enabled",
		"issue_import_approved",
		"triggered",
		"rechecked",
		"caught",
		"declarations",
		"ignore_github_ci",
		"post_merge",
		"handover",
		"release_workflows_disabled",
		"required",
		"multiple",
		"show_on_row",
		"github_insecure_http",
		"newest_first",
		"show_closed_by_default",
		"task_defaults_on_backfilled",
		"upgrade_check_pauses",
		"package_cache_enabled",
		"actions_cache_enabled",
		"password_auth_enabled",
		"open_registration",
		"google_oauth_enabled",
		"suspect",
		"review_not_needed",
		"adopted",
		"k8s_privileged",
		"k8s_network_policy",
		"external_agent_api",
		// task_run_ci_habit (model.RunCIHabit).
		"narrow_test_before_push",
		"narrow_test_before_fix_push",
		"pushed_after_red",
		"selective",
	}
)

type boolExprReplacer struct {
	re1   *regexp.Regexp
	re0   *regexp.Regexp
	reNe1 *regexp.Regexp
	reNe0 *regexp.Regexp
}

type boolDDLReplacer struct {
	reDef1  *regexp.Regexp
	reDef0  *regexp.Regexp
	reDefNN *regexp.Regexp
	reDef   *regexp.Regexp
}

var (
	boolExprReplacers []boolExprReplacer
	boolDDLReplacers  []boolDDLReplacer
	boolColumnsSet    = make(map[string]bool)
	insertValuesRe    = regexp.MustCompile(`(?is)^\s*INSERT\s+INTO\s+([^\s(]+)\s*\(([^)]+)\)\s*VALUES\s*\((.*)\)(.*)$`)
)

func init() {
	for _, col := range boolColumns {
		boolColumnsSet[col] = true
		boolExprReplacers = append(boolExprReplacers, boolExprReplacer{
			re1:   regexp.MustCompile(`(?i)(["` + "`" + `]?` + col + `["` + "`" + `]?\s*=\s*)1\b`),
			re0:   regexp.MustCompile(`(?i)(["` + "`" + `]?` + col + `["` + "`" + `]?\s*=\s*)0\b`),
			reNe1: regexp.MustCompile(`(?i)(["` + "`" + `]?` + col + `["` + "`" + `]?\s*!=\s*)1\b`),
			reNe0: regexp.MustCompile(`(?i)(["` + "`" + `]?` + col + `["` + "`" + `]?\s*!=\s*)0\b`),
		})
		boolDDLReplacers = append(boolDDLReplacers, boolDDLReplacer{
			reDef1:  regexp.MustCompile(`(?i)(["` + "`" + `]` + col + `["` + "`" + `]\s+)INTEGER(\s+NOT\s+NULL\s+DEFAULT\s+)1`),
			reDef0:  regexp.MustCompile(`(?i)(["` + "`" + `]` + col + `["` + "`" + `]\s+)INTEGER(\s+NOT\s+NULL\s+DEFAULT\s+)0`),
			reDefNN: regexp.MustCompile(`(?i)(["` + "`" + `]` + col + `["` + "`" + `]\s+)INTEGER(\s+NOT\s+NULL)`),
			reDef:   regexp.MustCompile(`(?i)(["` + "`" + `]` + col + `["` + "`" + `]\s+)INTEGER`),
		})
	}
}

func adaptSqliteFunctions(query string) string {
	query = reInstr.ReplaceAllString(query, "strpos($1, $2)")
	query = reGlobParam.ReplaceAllString(query, "LIKE REPLACE(REPLACE($$$1, '_', '\\_'), '*', '%')")
	query = reGlob.ReplaceAllString(query, "LIKE")
	// length(CAST(x AS BLOB)) is how the store measures a TEXT column in
	// bytes, SQLite's length() counting characters on TEXT. Postgres reads
	// a text->bytea cast as a bytea *literal*, so a lone backslash or a
	// "\x" without hex digits in the text fails the whole query (22P02);
	// octet_length is the same byte count without parsing anything.
	query = reBlobLength.ReplaceAllString(query, "octet_length($1)")
	query = reBlobCast.ReplaceAllString(query, "AS BYTEA")
	query = reBlobType.ReplaceAllString(query, "::BYTEA")
	query = rePragmaFrom.ReplaceAllString(query, "FROM (SELECT column_name AS name, data_type AS type FROM information_schema.columns WHERE table_schema = CURRENT_SCHEMA() AND table_name = $1 ORDER BY ordinal_position) AS _pragma_table_info")
	query = reParamIsNull.ReplaceAllString(query, "$$$1::text IS ${2}NULL")
	return query
}

func adaptSandboxUsage(query string) string {
	if strings.Contains(query, `"sandbox_usage"`) && strings.Contains(query, "ON CONFLICT") {
		query = strings.ReplaceAll(query, `"attempted" = "attempted" +`, `"attempted" = "sandbox_usage"."attempted" +`)
		query = strings.ReplaceAll(query, `"created" = "created" +`, `"created" = "sandbox_usage"."created" +`)
		query = strings.ReplaceAll(query, `"finished" = "finished" +`, `"finished" = "sandbox_usage"."finished" +`)
		query = strings.ReplaceAll(query, `"duration_ms" = "duration_ms" +`, `"duration_ms" = "sandbox_usage"."duration_ms" +`)
	}
	return query
}

func adaptBoolExpressions(query string) string {
	for _, r := range boolExprReplacers {
		query = r.re1.ReplaceAllString(query, "${1}TRUE")
		query = r.re0.ReplaceAllString(query, "${1}FALSE")
		query = r.reNe1.ReplaceAllString(query, "${1}TRUE")
		query = r.reNe0.ReplaceAllString(query, "${1}FALSE")
	}
	return query
}

func splitSqlValues(s string) []string {
	var vals []string
	var cur strings.Builder
	inQuote := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\'' {
			if inQuote && i+1 < len(s) && s[i+1] == '\'' {
				cur.WriteByte(c)
				cur.WriteByte(c)
				i++
				continue
			}
			inQuote = !inQuote
			cur.WriteByte(c)
			continue
		}
		if c == ',' && !inQuote {
			vals = append(vals, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteByte(c)
	}
	if cur.Len() > 0 {
		vals = append(vals, cur.String())
	}
	return vals
}

func adaptInsertBoolLiterals(query string) string {
	m := insertValuesRe.FindStringSubmatch(query)
	if len(m) < 5 {
		return query
	}
	table := m[1]
	colsStr := m[2]
	valsStr := m[3]
	tail := m[4]

	cols := splitIdentifiers(colsStr)
	vals := splitSqlValues(valsStr)

	if len(cols) != len(vals) {
		return query
	}

	modified := false
	for i, col := range cols {
		if boolColumnsSet[cleanIdentifier(col)] {
			v := strings.TrimSpace(vals[i])
			if v == "0" {
				vals[i] = strings.Replace(vals[i], "0", "FALSE", 1)
				modified = true
			} else if v == "1" {
				vals[i] = strings.Replace(vals[i], "1", "TRUE", 1)
				modified = true
			}
		}
	}

	if !modified {
		return query
	}

	return fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)%s", table, colsStr, strings.Join(vals, ","), tail)
}

var (
	reSubqueryGroup = regexp.MustCompile(`(?i)\)\s+GROUP\s+BY\s+`)
	reLimitParam    = regexp.MustCompile(`(?i)\bLIMIT\s+\$(\d+)`)
	reOrderByDesc   = regexp.MustCompile(`(?i)\bORDER\s+BY\s+"recent"\s+DESC\b`)
)

func adaptSubqueriesAndLimits(query string) string {
	if strings.Contains(query, "FROM (") && strings.Contains(query, ") GROUP BY") {
		query = reSubqueryGroup.ReplaceAllString(query, ") AS _subq GROUP BY ")
		query = strings.ReplaceAll(query, `GROUP BY lower("owner"), lower("name")`, `GROUP BY "owner", "name"`)
	}
	if strings.Contains(query, `GROUP BY "t"."id"`) && strings.Contains(query, `"o"."completed_at"`) {
		query = strings.ReplaceAll(query, `MAX("r"."finished_at"), "r"."finished_at"`, `MAX("r"."finished_at"), MAX("r"."finished_at")`)
		query = strings.ReplaceAll(query, `MAX("r"."finished_at"),"r"."finished_at"`, `MAX("r"."finished_at"), MAX("r"."finished_at")`)
		query = strings.ReplaceAll(query, `GROUP BY "t"."id"`, `GROUP BY "t"."id", "o"."completed_at", "o"."retry_requested_at", "o"."run_now_at"`)
	}
	if strings.Contains(query, `GROUP BY "t"."id"`) && strings.Contains(query, `"o"."closed_at"`) {
		query = strings.ReplaceAll(query, `MIN("r"."started_at"), "r"."started_at"`, `MIN("r"."started_at"), MIN("r"."started_at")`)
		query = strings.ReplaceAll(query, `MIN("r"."started_at"),"r"."started_at"`, `MIN("r"."started_at"), MIN("r"."started_at")`)
		query = strings.ReplaceAll(query, `GROUP BY "t"."id"`, `GROUP BY "t"."id", "o"."completed_at", "o"."closed_at"`)
	}
	query = reOrderByDesc.ReplaceAllString(query, `ORDER BY "recent" DESC NULLS LAST`)
	query = reLimitParam.ReplaceAllString(query, `LIMIT (CASE WHEN $$$1 < 0 THEN NULL ELSE $$$1 END)`)
	return query
}

func adaptDDL(query string) string {
	upper := strings.ToUpper(query)
	if strings.Contains(upper, "CREATE TABLE") ||
		strings.Contains(upper, "ALTER TABLE") ||
		strings.Contains(upper, "CREATE VIEW") ||
		strings.Contains(upper, "DROP VIEW") ||
		strings.Contains(upper, "CREATE INDEX") ||
		strings.Contains(upper, "CREATE UNIQUE INDEX") {

		query = reAutoincrement.ReplaceAllString(query, "BIGSERIAL PRIMARY KEY")
		query = reDatetime.ReplaceAllString(query, "TIMESTAMPTZ")
		query = reBlob.ReplaceAllString(query, "BYTEA")
		query = reReal.ReplaceAllString(query, "DOUBLE PRECISION")
		query = reDropView.ReplaceAllString(query, "DROP VIEW IF EXISTS $1 CASCADE")

		for _, r := range boolDDLReplacers {
			query = r.reDef1.ReplaceAllString(query, "${1}BOOLEAN${2}TRUE")
			query = r.reDef0.ReplaceAllString(query, "${1}BOOLEAN${2}FALSE")
			query = r.reDefNN.ReplaceAllString(query, "${1}BOOLEAN${2}")
			query = r.reDef.ReplaceAllString(query, "${1}BOOLEAN")
		}
		// SQLite's INTEGER is always 64-bit; PostgreSQL's is int4, which
		// overflows at 2^31 on cumulative milliseconds and byte counts
		// that SQLite stores without complaint. After the boolean
		// replacers, so that the 0/1 flag columns still become BOOLEAN.
		query = reInteger.ReplaceAllString(query, "BIGINT")
	}

	if strings.Contains(query, "'0001-01-01 00:00:00')") {
		query = strings.ReplaceAll(query, "'0001-01-01 00:00:00')", "'0001-01-01 00:00:00'::timestamptz)")
	}

	return query
}

func translateQuotesAndPlaceholders(query string) string {
	var buf strings.Builder
	buf.Grow(len(query) + 16)

	paramIdx := 1
	inSingleQuote := false
	inDoubleQuote := false
	inBacktick := false
	inLineComment := false
	inBlockComment := false

	runes := []rune(query)
	n := len(runes)

	for i := 0; i < n; i++ {
		r := runes[i]

		// Handle comments
		if inLineComment {
			if r == '\n' {
				inLineComment = false
			}
			buf.WriteRune(r)
			continue
		}
		if inBlockComment {
			if r == '*' && i+1 < n && runes[i+1] == '/' {
				buf.WriteString("*/")
				i++
				inBlockComment = false
				continue
			}
			buf.WriteRune(r)
			continue
		}

		if !inSingleQuote && !inDoubleQuote && !inBacktick {
			if r == '-' && i+1 < n && runes[i+1] == '-' {
				inLineComment = true
				buf.WriteString("--")
				i++
				continue
			}
			if r == '/' && i+1 < n && runes[i+1] == '*' {
				inBlockComment = true
				buf.WriteString("/*")
				i++
				continue
			}
		}

		// Handle string literals
		if r == '\'' && !inDoubleQuote && !inBacktick {
			buf.WriteRune('\'')
			if inSingleQuote {
				// Check for escaped quote ''
				if i+1 < n && runes[i+1] == '\'' {
					buf.WriteRune('\'')
					i++
					continue
				}
				inSingleQuote = false
			} else {
				inSingleQuote = true
			}
			continue
		}

		if inSingleQuote {
			buf.WriteRune(r)
			continue
		}

		// Handle double quotes
		if r == '"' && !inBacktick {
			buf.WriteRune('"')
			inDoubleQuote = !inDoubleQuote
			continue
		}
		if inDoubleQuote {
			buf.WriteRune(r)
			continue
		}

		// Handle backticks (turn into double quotes for identifiers)
		if r == '`' {
			buf.WriteRune('"')
			inBacktick = !inBacktick
			continue
		}
		if inBacktick {
			buf.WriteRune(r)
			continue
		}

		// Handle parameter placeholders
		if r == '?' {
			buf.WriteString(fmt.Sprintf("$%d", paramIdx))
			paramIdx++
			continue
		}

		buf.WriteRune(r)
	}

	return buf.String()
}

func isReplaceInto(query string) bool {
	upper := strings.TrimSpace(strings.ToUpper(query))
	return strings.HasPrefix(upper, "REPLACE INTO ") || strings.HasPrefix(upper, "REPLACE\nINTO ")
}

func isInsertOrIgnore(query string) bool {
	upper := strings.TrimSpace(strings.ToUpper(query))
	return strings.HasPrefix(upper, "INSERT OR IGNORE INTO ") || strings.HasPrefix(upper, "INSERT OR IGNORE\nINTO ")
}

var replaceRe = regexp.MustCompile(`(?is)^\s*REPLACE\s+INTO\s+([^\s(]+)\s*\((.*?)\)\s*VALUES\s*\((.*?)\)(.*)$`)

func adaptReplaceInto(query string) string {
	m := replaceRe.FindStringSubmatch(query)
	if len(m) < 4 {
		return query
	}

	rawTable := strings.TrimSpace(m[1])
	rawCols := strings.TrimSpace(m[2])
	rawVals := strings.TrimSpace(m[3])
	tail := m[4]

	cleanTable := cleanIdentifier(rawTable)
	cols := splitIdentifiers(rawCols)

	pkCols, ok := tablePrimaryKeys[cleanTable]
	if !ok {
		// Default to "id" if table not found
		pkCols = []string{"id"}
	}

	// Find non-PK columns for UPDATE
	var updateParts []string
	for _, col := range cols {
		cleanCol := cleanIdentifier(col)
		if !contains(pkCols, cleanCol) {
			updateParts = append(updateParts, fmt.Sprintf(`"%s" = EXCLUDED."%s"`, cleanCol, cleanCol))
		}
	}

	var pkClause strings.Builder
	for i, pk := range pkCols {
		if i > 0 {
			pkClause.WriteString(", ")
		}
		pkClause.WriteString(fmt.Sprintf(`"%s"`, pk))
	}

	if len(updateParts) == 0 {
		return fmt.Sprintf(`INSERT INTO %s (%s) VALUES (%s) ON CONFLICT (%s) DO NOTHING%s`,
			rawTable, rawCols, rawVals, pkClause.String(), tail)
	}

	return fmt.Sprintf(`INSERT INTO %s (%s) VALUES (%s) ON CONFLICT (%s) DO UPDATE SET %s%s`,
		rawTable, rawCols, rawVals, pkClause.String(), strings.Join(updateParts, ", "), tail)
}

var insertOrIgnoreRe = regexp.MustCompile(`(?is)^\s*INSERT\s+OR\s+IGNORE\s+INTO\s+(.*)$`)

func adaptInsertOrIgnore(query string) string {
	m := insertOrIgnoreRe.FindStringSubmatch(query)
	if len(m) < 2 {
		return query
	}
	rest := strings.TrimSpace(m[1])
	return fmt.Sprintf("INSERT INTO %s ON CONFLICT DO NOTHING", rest)
}

func cleanIdentifier(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, `"`+"`")
	return strings.ToLower(s)
}

func splitIdentifiers(s string) []string {
	parts := strings.Split(s, ",")
	var res []string
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			res = append(res, trimmed)
		}
	}
	return res
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

var insertTableRe = regexp.MustCompile(`(?is)^\s*INSERT\s+INTO\s+([^\s(]+)`)

func isAutoIncrementInsert(query string) (string, string, bool) {
	upper := strings.ToUpper(query)
	if strings.Contains(upper, "RETURNING") {
		return "", "", false
	}
	m := insertTableRe.FindStringSubmatch(query)
	if len(m) < 2 {
		return "", "", false
	}
	table := cleanIdentifier(m[1])
	col, ok := autoIncrementTables[table]
	if !ok {
		return "", "", false
	}
	return table, col, true
}
