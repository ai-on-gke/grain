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
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Store is the model's read and write surface over any database/sql
// database. It imports no driver: opening an embedded SQLite database
// lives in the sqlite subpackage, so this file — and everything above
// it — stays free of that dependency and testable against anything that
// speaks SQL.
//
// Every statement here is parameterised. That is the whole of what the
// language change bought at this layer: a Python controller cannot embed
// SQLite the way Go can, so an earlier version of this store shelled out
// to a CLI with no bind parameters — which meant hand-rendering every
// untrusted issue title and comment body into a statement and getting
// escaping rules right with no server to check against. That module does
// not exist here. Writes are also real transactions rather than a
// best-effort batch, so a task and its child rows land together or not
// at all.
type Store struct {
	db *sql.DB
	// id is a monotonically increasing process-local identifier assigned in
	// New, so callers keying process-memory state by a Store (such as
	// orchestrator's pathTriggerPolled) never collide when the garbage
	// collector reuses a closed Store's heap address.
	id uint64
	// The connection ChangeToken reads its "has anything been committed?"
	// counter from, and the generation number that says which connection
	// that was. Both belong to change_token.go, which is the only thing
	// that touches them and which documents why the connection has to be
	// this store's own and has to never write.
	changeMu   sync.Mutex
	changeConn *sql.Conn
	changeGen  uint64

	// commits counts successful write transactions made through this
	// Store. It is process-local instrumentation: RunCycle samples it to
	// say how many commits landed during a working tick without adding the
	// measurement's own database write.
	commits atomic.Uint64

	// CI step log chunk write batching buffer and coordinator
	stepOutputMu      sync.Mutex
	stepOutputQueue   []*stepOutputWriteReq
	stepOutputFlusher bool

	// Per-run proxy tool call census accumulated during live sandbox requests
	// so end-of-run RecordRunTelemetry merges proxy calls with MCP sidecar calls.
	proxyToolsMu sync.Mutex
	proxyTools   map[string]map[string]RunToolUse
	// proxyCheckWaits and proxyCalls are the sandbox CLI's CI waits and
	// argument-carrying calls per run (proxy_cli.go), under proxyToolsMu.
	proxyCheckWaits map[string][]RunCheckWait
	proxyCalls      map[string][]ProxyToolCall

	// SettingsRepo is the deployment's own state repository, whose merged
	// changes the daemon applies to itself on its next tick
	// (staterepo.Apply) -- which is why the security hold judges a pull
	// request against it by trustboundary.ClassifyState as well
	// (orchestrator's stateRepoGuard). Set once at start, as
	// GitDefaultHost is, to the state manager's own answer, which follows
	// the UI repointing it. Nil, or an answer of the zero RepoRef, leaves
	// ConfigRepo as the only one, and ConfigRepo is judged so whatever
	// this says.
	SettingsRepo func() RepoRef

	// GitDefaultHost is the git host a repository or team connection
	// written without one means: the git proxy's ForwardHost when the
	// deployment sets one, and DefaultGitHost (github.com) when it is
	// empty (docs/git-hosts.md).
	GitDefaultHost string
	// SingleTenant is the single-tenant engine, which the daemon always
	// sets (false is left only for the team tests until team RBAC goes,
	// docs/single-tenant-reset.md 3e-iii-d): its
	// one tenant is the machine's owner, so a task's git credential is
	// resolved as the default team's -- the connection grain init points
	// at the daemon's own credential (TaskGitCredentialOn). Set once at start, as
	// GitDefaultHost is; false is the multi-tenant deployment.
	SingleTenant bool
}

var nextStoreID atomic.Uint64

// New wraps an already-open database. It runs nothing against it: Init
// is what creates and migrates the schema.
func New(db *sql.DB) *Store { return &Store{db: db, id: nextStoreID.Add(1)} }

// ID returns this Store's process-local identifier, unique across every
// Store opened with New in this process even if a closed Store's heap
// address is later reused by the garbage collector.
func (s *Store) ID() uint64 {
	if s == nil {
		return 0
	}
	return s.id
}

// CommitCount returns the successful write transactions this Store has
// committed since it was opened. It is intended for differences between
// two nearby readings; the absolute value is deliberately not durable.
func (s *Store) CommitCount() uint64 {
	if s == nil {
		return 0
	}
	return s.commits.Load()
}

// querier is the subset of *sql.DB that *sql.Tx also provides.
//
// It exists so a read and the write that depends on it happen inside one
// transaction. That is not a nicety: Store.write retries an operation
// from the top when it loses a race, and a retry is only correct if the
// re-read sees the winner's state.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// ErrSchemaTooNew is returned when the database was written by a build
// that knows a later schema. Refusing up front beats failing later with a
// confusing missing column.
var ErrSchemaTooNew = errors.New("database schema is newer than this build")

// ErrSchemaTooOld is returned when the database was written by a build
// that knows an earlier schema, and so carries columns and indexes this
// build cannot reconcile it into.
//
// It is the same argument ErrSchemaTooNew makes, in the direction that
// used to be assumed harmless. Init's own CREATE TABLE IF NOT EXISTS
// never alters a table that already exists, so a store at an older
// version keeps whatever that version had -- a column since dropped,
// still declared NOT NULL with no default, fails every write that no
// longer supplies it. A store from before the single-tenant baseline
// never gets this far: the startup guard refuses it first
// (CheckSingleTenant).
//
// Left unchecked, that surfaces as a dispatch failure on every tick,
// forever, from a daemon that started up perfectly happily -- so the
// operator sees a deployment that runs and never runs anything, rather
// than one that says why. ../scripts/setup.sh carries such a store
// across at deploy (pkg/cli/schemaversion.go's own doc comment on how it
// knows to), and this is what a build started any other way does instead.
//
// The message names `grain state prepare-upgrade` (pkg/cli/stateprepare.go)
// as the way out, because it is the one that keeps the rows: it exports
// the store for this build's next start to rebuild from, on SQLite and
// Postgres alike, and that start brings the state repository's dump --
// the same rows at the same old schema -- across with it. Moving the
// store aside with `grain state reformat` is named too, last and for
// what it is: a fresh, empty deployment (grain/task-4851, where an
// operator following the older wording of this message lost every row).
var ErrSchemaTooOld = errors.New("database schema is older than this build")

// SchemaVersion reads the version stamped in this database, without
// creating, altering or stamping anything -- the one question worth
// asking of a store Init has just refused.
//
// A database with no stamp in it reports 0 with no error: that is a
// database Init has never run against (a fresh file, or one whose
// grain_schema table is not there yet), and "there is nothing here"
// is an answer rather than a failure. Every other error is the
// database being unreadable, which is a different thing again.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var version int
	err := s.db.QueryRowContext(ctx,
		"SELECT `version` FROM `grain_schema` WHERE `id` = 1").Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		// No such table: the same "nothing here" as no row, and worth
		// treating as one rather than making every caller match on a
		// driver's message.
		errLower := strings.ToLower(err.Error())
		if strings.Contains(errLower, "no such table") || strings.Contains(errLower, "does not exist") {
			return 0, nil
		}
		return 0, err
	}
	return version, nil
}

// Init creates the schema if absent and stamps the version.
//
// The schema in schema.go is created whole into an empty store: CREATE
// TABLE IF NOT EXISTS does nothing to a store already at this build's
// version. A store stamped at or past the 2.0 baseline
// (V2BaselineSchemaVersion) and below this build's version is migrated
// in place (migrations.go). Any other is refused before anything in it is
// touched -- one from before the reset by the startup guard
// (CheckSingleTenant), one between the reset and 2.0.0, or newer than
// this build, by checkSchemaVersion.
func (s *Store) Init(ctx context.Context) error {
	// The guard first: a store from before the reset is refused in its
	// words, which say what to do instead, rather than as merely old.
	if err := s.CheckSingleTenant(ctx); err != nil {
		return err
	}
	version, err := s.SchemaVersion(ctx)
	if err != nil {
		return fmt.Errorf("reading schema version: %w", err)
	}
	if version >= V2BaselineSchemaVersion && version < SchemaVersion {
		if err := s.migrateInPlace(ctx, version); err != nil {
			return err
		}
		version = SchemaVersion
	}
	if version != 0 {
		if err := checkSchemaVersion(version); err != nil {
			return err
		}
	}
	if err := s.applySchema(ctx); err != nil {
		return err
	}
	if err := s.ensureInstanceID(ctx); err != nil {
		return fmt.Errorf("initializing grain instance id: %w", err)
	}
	if version != 0 {
		return nil
	}
	// INSERT OR IGNORE for ensureInstanceID's reason: two processes
	// initializing the same empty store converge on one stamp.
	if _, err := s.db.ExecContext(ctx,
		"INSERT OR IGNORE INTO `grain_schema` (`id`, `version`) VALUES (1, ?)", SchemaVersion); err != nil {
		return fmt.Errorf("stamping schema version: %w", err)
	}
	return nil
}

// checkSchemaVersion refuses a stamp other than this build's own. A zero
// is a store Init has never stamped, which is not a refusal. A store at
// or past the 2.0 baseline never reaches the "too old" branch: Init has
// migrated it in place first. The branch is for one written between the
// single-tenant reset (BaselineSchemaVersion) and 2.0.0, by a build no
// release shipped, whose rows still come across through the state
// repository.
func checkSchemaVersion(version int) error {
	if version > SchemaVersion {
		return fmt.Errorf("%w: found %d, this build knows %d",
			ErrSchemaTooNew, version, SchemaVersion)
	}
	if version < SchemaVersion {
		return fmt.Errorf("%w: found %d, this build knows %d -- nothing has been changed. To carry this "+
			"deployment's rows across, stop grain and run `grain state -data-dir DIR prepare-upgrade` with this "+
			"build (scripts/setup.sh does this itself on a deploy), then start it again: the next start keeps the "+
			"old store under a timestamped name -- a directory beside it for SQLite, a schema beside it for "+
			"Postgres -- and rebuilds this one from its rows, state repository included. Starting empty instead "+
			"loses every task, connection and memory document: only if that is what you want, move the store "+
			"aside yourself and run `grain state reformat`",
			ErrSchemaTooOld, version, SchemaVersion)
	}
	return nil
}

// InstanceID returns this installation's durable random identity. It is
// generated by Init and exported with the rest of the database, so the
// same installation keeps its identity across restarts and state-repository
// restores while independently initialized installations do not collide.
func (s *Store) InstanceID(ctx context.Context) (string, error) {
	var id string
	if err := s.db.QueryRowContext(ctx,
		"SELECT `id` FROM `grain_instance` WHERE `singleton` = 1").Scan(&id); err != nil {
		return "", fmt.Errorf("reading grain instance id: %w", err)
	}
	return id, nil
}

// DeploymentID returns the id this deployment puts in its branch names
// through the template's {deployment} placeholder: the one a system
// admin set in Settings, or, until one has, the default derived from
// InstanceID (see EffectiveDeploymentID).
func (s *Store) DeploymentID(ctx context.Context) (string, error) {
	instanceID, err := s.InstanceID(ctx)
	if err != nil {
		return "", err
	}
	var configured string
	err = s.db.QueryRowContext(ctx,
		"SELECT `deployment_id` FROM `grain_config` WHERE `id` = 1").Scan(&configured)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("reading deployment id: %w", err)
	}
	return EffectiveDeploymentID(configured, instanceID), nil
}

// ensureInstanceID creates the identity once. INSERT OR IGNORE makes two
// processes racing to initialize the same store converge on the winner.
func (s *Store) ensureInstanceID(ctx context.Context) error {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Errorf("generating random id: %w", err)
	}
	_, err := s.db.ExecContext(ctx,
		"INSERT OR IGNORE INTO `grain_instance` (`singleton`, `id`) VALUES (1, ?)",
		hex.EncodeToString(b))
	return err
}

// applySchema runs every statement Statements() produces, in one
// transaction.
//
// One transaction rather than eighty-odd auto-committed statements
// because each auto-commit is its own durable commit, and a durable
// commit is an fsync: on a fresh database that was ~82 fsyncs, and on a
// database that already has the schema it was still one per view (the
// DROP VIEW/CREATE VIEW pair Statements() emits writes every time,
// unlike CREATE TABLE IF NOT EXISTS, which does nothing and so commits
// nothing). fsync is what dominated Init, not the SQL: measured at
// ~30-45ms each on a container filesystem, which is where Init's ~3s
// went. Batched into one transaction the same DDL takes ~120ms, and
// every test that opens a store pays that instead -- Init runs once per
// store, and stores are opened per test all over this repo.
//
// SQLite's DDL is transactional, so this is also the honest shape for
// what Init means: the schema arrives whole or not at all, rather than
// leaving a database half-created behind a returned error.
func (s *Store) applySchema(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("applying schema: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, stmt := range Statements() {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("applying schema: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("applying schema: %w", err)
	}
	return nil
}

// ErrConflict reports that an operation could not get a write in even
// after retrying -- the store stayed busy with some other writer for
// longer than it was willing to wait. A caller seeing it should tell
// whoever asked that their change did not land.
var ErrConflict = errors.New("conflict: the store kept changing under this operation")

// maxWriteAttempts bounds write's retry loop. A handful is plenty: the
// sqlite package's own busy_timeout already gives a blocked writer
// several seconds to get in on its own; a caller still losing this many
// attempts after that is contending with something that is not going to
// stop.
const maxWriteAttempts = 5

// write runs a mutation in one transaction, retrying it if the attempt
// could not get the store's write lock in time.
//
// Locking, not merging, is the whole mechanism now. sqlite.Open's
// _txlock=immediate takes SQLite's write lock at BEGIN rather than at
// the transaction's first write statement, so two overlapping mutations
// are serialised at the same point every time -- one proceeds, and the
// other either waits out busy_timeout or fails outright, in both cases
// before either has touched a row. That is a stronger guarantee than the
// store had against Dolt, which merged concurrent writers cell by cell
// and only reported a conflict when two of them touched the same cell
// with different values (dolt/store_test.go's now-deleted
// TestACounterStampWouldNotConflict pinned exactly that hazard); SQLite
// admits only one writer at a time, full stop, so there is nothing left
// for an artificial per-write marker to catch that the lock itself does
// not already catch.
//
// fn may run more than once, on a fresh transaction each time. It must
// therefore read what it needs through the querier it is handed rather
// than relying on anything read before write was called -- that is what
// makes a retry see the previous attempt's own result rather than
// rewriting over it.
func (s *Store) write(ctx context.Context, what string, fn func(*sql.Tx) error) error {
	var lastErr error
	for attempt := 0; attempt < maxWriteAttempts; attempt++ {
		err := s.writeOnce(ctx, fn)
		if err == nil {
			return nil
		}
		if !isSerializationFailure(err) {
			return err
		}
		lastErr = err
	}
	return fmt.Errorf("%w after %d attempts (%s): %v", ErrConflict, maxWriteAttempts, what, lastErr)
}

func (s *Store) writeOnce(ctx context.Context, fn func(*sql.Tx) error) error {
	if r, ok := WriteRehearsalFrom(ctx); ok && r.Rehearsing() {
		return s.rehearseOnce(ctx, r, fn)
	}
	// BeginTx is where a lost race shows up: sqlite.Open's DSN puts every
	// transaction in immediate mode, so this is where SQLite's write lock
	// is actually acquired (or waited for, or refused) rather than at the
	// first write statement inside fn.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.commits.Add(1)
	return nil
}

// isSerializationFailure reports whether the database is telling us this
// attempt could not get the write lock and should be tried again.
//
// modernc.org/sqlite reports SQLite's own SQLITE_BUSY as "database is
// locked (5) (SQLITE_BUSY)" -- measured directly, in sqlite/store_test.go's
// TestSQLiteReportsABusyDatabase, which is what this matches against
// rather than a hard-coded error code from the driver's own package (this
// package imports no driver, on purpose -- pkg/model/sqlite's own doc
// comment).
//
// A miss here costs a pointless surfaced error rather than a wrong
// answer, and the pinning test is there to catch a reword.
func isSerializationFailure(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, marker := range []string{
		"database is locked",
		"SQLITE_BUSY",
		"40001",
		"40P01",
		"55P03",
		"could not serialize access",
		"deadlock detected",
		"lock timeout",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// PutTask inserts or replaces a task and everything hanging off it, in
// one transaction.
//
// Child rows are deleted and re-inserted rather than diffed: the sets are
// tiny, and "the row set equals the object" is a property worth having
// outright rather than maintaining.
func (s *Store) PutTask(ctx context.Context, t Task) error {
	if t.Branch == "" {
		branch, err := s.defaultBranchForTask(ctx, t.ID, t.Title)
		if err != nil {
			return err
		}
		t.Branch = branch
	}
	return s.write(ctx, "put task "+t.ID, func(tx *sql.Tx) error { return putTask(ctx, tx, t) })
}

func (s *Store) defaultBranchForTask(ctx context.Context, taskID, title string) (string, error) {
	return branchForTask(ctx, s.db, taskID, title)
}

// BranchForNewTask is the branch PutTask would give a task with this id
// and title and no branch of its own: for a caller that has to push to
// that branch before the task is stored (orchestrator's mechanical port).
func (s *Store) BranchForNewTask(ctx context.Context, taskID, title string) (string, error) {
	return s.defaultBranchForTask(ctx, taskID, title)
}

// branchForTask expands the deployment's current branch template for a
// new task with this id and title (a {slug} in the template is the
// title's, FormatTaskBranchName), reading the template, the
// installation id and the deployment id through q -- the store itself,
// or the transaction a write is already in.
func branchForTask(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, taskID, title string) (string, error) {
	var template, deploymentID, instanceID string
	err := q.QueryRowContext(ctx,
		"SELECT `task_branch_template`, `deployment_id` FROM `grain_config` WHERE `id` = 1").Scan(&template, &deploymentID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("reading task branch template: %w", err)
	}
	if err := q.QueryRowContext(ctx, "SELECT `id` FROM `grain_instance` WHERE `singleton` = 1").Scan(&instanceID); err != nil {
		return "", fmt.Errorf("reading grain instance id for task branch: %w", err)
	}
	return FormatTaskBranchName(template, instanceID, EffectiveDeploymentID(deploymentID, instanceID), taskID, title)
}

// putTask is PutTask's body, against whatever transaction it is running
// in -- so UpdateTask can read and write in the same one.
func putTask(ctx context.Context, tx *sql.Tx, t Task) error {
	var existing int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM `task` WHERE `id` = ?", t.ID).Scan(&existing); err != nil {
		return err
	}
	if t.Branch == "" {
		branch, err := branchForTask(ctx, tx, t.ID, t.Title)
		if err != nil {
			return err
		}
		t.Branch = branch
	}
	// Admission facts are derived from the row this write is about to
	// replace, so they are invalidated here, in the same transaction, by
	// whoever changes what they were derived from. See
	// invalidateStaleGrantWait: the alternative -- leaving it to the next
	// admission probe -- is only reached when a worker slot is free, and a
	// task whose operator has just added the grant it was missing would go
	// on displaying the wait for as long as the deployment stayed busy.
	if err := invalidateStaleGrantWait(ctx, tx, t); err != nil {
		return err
	}

	if t.WorkOrigin == "" {
		origin, err := originForNewTask(ctx, tx, t)
		if err != nil {
			return err
		}
		t.WorkOrigin = origin
	} else {
		origin, err := rootOfNamedOrigin(ctx, tx, t)
		if err != nil {
			return err
		}
		t.WorkOrigin = origin
	}

	oActor, oBehalf := t.Origin.Attribution.Actor, t.Origin.Attribution.OnBehalfOf
	var aActorKind, aActorID, aBehalfKind, aBehalfID any
	if t.Approval != nil {
		aActorKind, aActorID = string(t.Approval.Actor.Kind), t.Approval.Actor.ID
		if b := t.Approval.OnBehalfOf; b != nil {
			aBehalfKind, aBehalfID = string(b.Kind), b.ID
		}
	}
	var targetOwner, targetName any
	if t.Target != nil {
		targetOwner, targetName = t.Target.StoredOwner(), t.Target.Name
	}
	if _, err := tx.ExecContext(ctx, `REPLACE INTO `+"`task`"+` (
  `+"`id`, `branch`, `intent`, `title`, `body`"+`,
  `+"`origin_actor_kind`, `origin_actor_id`, `origin_behalf_kind`, `origin_behalf_id`, `origin_reason`"+`,
  `+"`approval_actor_kind`, `approval_actor_id`, `approval_behalf_kind`, `approval_behalf_id`, `approved_at`, `deferred_at`, `deferred_until`, `undeferred_at`"+`,
  `+"`target_owner`, `target_name`, `binding`, `base`, `folder`"+`,
  `+"`auto_merge`, `auto_approve_proposals`, `created_at`, `order_key`, `sandbox_cpus`, `sandbox_memory_mb`, `sandbox_disk_gb`, `interactive`, `agent_framework`, `agent_backend_id`, `agent_model`, `prompt_extension`, `review_template_id`, `network_access`, `nothing_needed_action`, `creator`, `time_zone`, `issue`, `no_port`, `agent_model_source`, `origin_ref`, `resume_on_deadline`, `change_target_kind`, `change_target_spec`"+`
) VALUES (?,?,?,?,?, ?,?,?,?,?, ?,?,?,?,?,?,?,?, ?,?,?,?,?, ?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.Branch, string(t.Intent), t.Title, t.Body,
		string(oActor.Kind), oActor.ID, kindOf(oBehalf), idOf(oBehalf), string(t.Origin.Reason),
		aActorKind, aActorID, aBehalfKind, aBehalfID, timeOf(t.ApprovedAt), timeOf(t.DeferredAt), timeOf(t.DeferredUntil),
		timeOf(t.UndeferredAt),
		targetOwner, targetName, string(t.Binding), nullable(t.Base), folderOf(t.Folder),
		t.AutoMerge, t.AutoApproveProposals, timeOf(t.CreatedAt), t.OrderKey, t.SandboxCPUs, t.SandboxMemoryMB, t.SandboxDiskGB, t.Interactive,
		t.AgentFramework, t.AgentBackendID, t.Model, t.PromptExtension, t.ReviewTemplateID, string(t.NetworkAccess), string(t.NothingNeededAction), t.Creator, t.TimeZone, t.Issue, t.NoPort, string(t.ModelSource), string(t.WorkOrigin), t.ResumeOnDeadline,
		changeTargetKind(t.ChangeTarget), t.ChangeTarget.SpecString(),
	); err != nil {
		return fmt.Errorf("writing task %s: %w", t.ID, err)
	}

	for _, table := range []string{"task_read", "task_write", "task_filed_template", "task_grant", "task_link", "task_tag", "task_custom_field"} {
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM `"+table+"` WHERE `task_id` = ?", t.ID); err != nil {
			return err
		}
	}
	for _, r := range t.Reads {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO `task_read` (`task_id`, `owner`, `name`) VALUES (?,?,?)",
			t.ID, r.StoredOwner(), r.Name); err != nil {
			return err
		}
	}
	for _, w := range t.Writes {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO `task_write` (`task_id`, `owner`, `name`, `base`) VALUES (?,?,?,?)",
			t.ID, w.Repo.StoredOwner(), w.Repo.Name, w.Base); err != nil {
			return err
		}
	}
	if t.TemplateID != "" {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO `task_filed_template` (`task_id`, `template_id`) VALUES (?,?)",
			t.ID, t.TemplateID); err != nil {
			return err
		}
		// A task filed from a template with a goal is declared with it
		// when it is first written; a later rewrite of the task leaves
		// whatever goal it has now -- a person may have changed it.
		if existing == 0 {
			if err := goalFromTemplateTx(ctx, tx, t); err != nil {
				return err
			}
		}
	}
	for _, g := range t.Grants {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO `task_grant` (`task_id`, `capability`, `via`, `folder`) VALUES (?,?,?,?)",
			t.ID, g.Capability, string(g.Via), folderOf(g.Folder)); err != nil {
			return err
		}
	}
	for _, l := range t.Links {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO `task_link` (`task_id`, `kind`, `target`, `blocks`) VALUES (?,?,?,?)",
			t.ID, string(l.Kind), l.Target, l.Kind.Blocks()); err != nil {
			return err
		}
	}
	for _, tag := range t.Tags {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO `task_tag` (`task_id`, `tag`) VALUES (?,?)",
			t.ID, tag); err != nil {
			return err
		}
	}
	// Written in name order rather than in map order: a map's iteration
	// order is deliberately random, and rows written in a different
	// order every time would give the state repository a diff on every
	// export of a task nobody touched (staterepo.Export's own byte
	// stability). An empty value is dropped rather than stored, the same
	// rule NormalizeCustomFieldValues applies on the way in, so a
	// cleared field leaves no row behind.
	//
	// One row per value: a field this deployment declared `multiple`
	// carries a set of them (CustomField.Multiple), and the primary key
	// has the value in it so that they fit. A duplicate is dropped here
	// rather than left to fail against that key -- what a field holds is
	// a set, so a caller that said "acme" twice said it once.
	for _, name := range sortedKeys(t.CustomFields) {
		written := map[string]bool{}
		for _, value := range t.CustomFields[name] {
			if value == "" || written[value] {
				continue
			}
			written[value] = true
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO `task_custom_field` (`task_id`, `name`, `value`) VALUES (?,?,?)",
				t.ID, name, value); err != nil {
				return err
			}
		}
	}
	return nil
}

// invalidateStaleGrantWait drops t's admission wait when this write
// changes what such a wait is derived from.
//
// A task_grant_wait row is a derived fact, not a state: it says why the
// admitter passed a ready task over last time it looked at it, and its
// inputs are the task's own grants and the repository it targets (which
// is what decides whether Grain CI is required of it at all) together
// with whatever the capability pools held at that instant. Only the
// first two live in this table, and only they can change without the
// orchestrator noticing -- an operator adding the `grain-ci` grant the
// admitter complained about edits the task, and nothing else.
//
// So the rule is: the mutation that changes an admission fact's inputs
// invalidates that fact, and the next admission probe derives it again.
// That keeps the wait honest without teaching this package what any
// capability means, and without depending on a worker slot coming free
// -- a probe only runs for a candidate a cycle still has capacity for,
// which is precisely the case the stale row survived before.
//
// A write that leaves the grants and the target alone -- a retitle, a
// comment count, a reorder, a state-repository import of an unchanged
// task -- keeps the row, because nothing it was derived from moved. That
// is what stops an unrelated edit from claiming an exhausted pool has a
// slot free: an exhausted scarce resource is still exhausted after
// somebody renames a different task, and the wait is deleted only where
// it can no longer be trusted, never where it merely might be stale.
func invalidateStaleGrantWait(ctx context.Context, tx *sql.Tx, t Task) error {
	changed, err := admissionInputsChanged(ctx, tx, t)
	if err != nil || !changed {
		return err
	}
	_, err = tx.ExecContext(ctx, "DELETE FROM `task_grant_wait` WHERE `task_id` = ?", t.ID)
	return err
}

// admissionInputsChanged reports whether t's grants or target differ from
// what is stored for it. A task with no row yet has nothing derived from
// it, so it never counts as changed.
func admissionInputsChanged(ctx context.Context, tx *sql.Tx, t Task) (bool, error) {
	var owner, name sql.NullString
	err := tx.QueryRowContext(ctx,
		"SELECT `target_owner`, `target_name` FROM `task` WHERE `id` = ?", t.ID).Scan(&owner, &name)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading task %s's stored target: %w", t.ID, err)
	}
	var target RepoRef
	if owner.Valid && name.Valid {
		target = RepoFromStored(owner.String, name.String)
	}
	var want RepoRef
	if t.Target != nil {
		want = *t.Target
	}
	if target != want {
		return true, nil
	}
	stored := map[string]bool{}
	if err := each(ctx, tx,
		"SELECT `capability`, `via`, `folder` FROM `task_grant` WHERE `task_id` = ?", t.ID,
		func(rows *sql.Rows) error {
			var capability, via string
			var folder sql.NullString
			if err := rows.Scan(&capability, &via, &folder); err != nil {
				return err
			}
			stored[grantKey(capability, via, folder.String)] = true
			return nil
		}); err != nil {
		return false, fmt.Errorf("reading task %s's stored grants: %w", t.ID, err)
	}
	wanted := map[string]bool{}
	for _, g := range t.Grants {
		var folder string
		if g.Folder != nil {
			folder = g.Folder.String()
		}
		wanted[grantKey(g.Capability, string(g.Via), folder)] = true
	}
	if len(stored) != len(wanted) {
		return true, nil
	}
	for key := range wanted {
		if !stored[key] {
			return true, nil
		}
	}
	return false, nil
}

// grantKey identifies one stored grant. Via and Folder are part of it
// because they decide where a capability resolves -- two deployments'
// worth of the same capability name in different folders are different
// resources, and a wait derived from one says nothing about the other.
func grantKey(capability, via, folder string) string {
	return capability + "\x00" + via + "\x00" + folder
}

// NewTaskID allocates a task identity from the store.
//
// A task used to be named after the GitHub issue it was filed from, so
// nothing could file one without creating an issue first. This is that
// coupling's replacement: ids come from task_sequence, and a caller with
// a store and nothing else can create a task.
//
// The result is the decimal sequence number ("42"), which is what makes
// `grain get 42` and the branch `grain/task-42` read the way they do --
// where a GitHub-derived id put a repo path inside the branch name.
// Task.ID stays an opaque string, so nothing but this function is
// entitled to assume the shape.
func (s *Store) NewTaskID(ctx context.Context) (id string, err error) {
	err = s.write(ctx, "allocate a task id", func(tx *sql.Tx) error {
		id, err = newTaskID(ctx, tx)
		return err
	})
	return id, err
}

func newTaskID(ctx context.Context, tx *sql.Tx) (string, error) {
	res, err := tx.ExecContext(ctx,
		"INSERT INTO `task_sequence` (`issued_at`) VALUES (?)", time.Now().UTC())
	if err != nil {
		return "", fmt.Errorf("allocating a task id: %w", err)
	}
	n, err := res.LastInsertId()
	if err != nil {
		return "", fmt.Errorf("reading the allocated task id: %w", err)
	}
	return strconv.FormatInt(n, 10), nil
}

// AddComment appends one entry to a task's conversation and returns the
// id the store assigned it -- which a caller recording an outstanding
// question needs, since Observation.PendingQuestionCommentID names one of
// these.
func (s *Store) AddComment(ctx context.Context, c Comment) (id int64, err error) {
	err = s.write(ctx, "comment on task "+c.TaskID, func(tx *sql.Tx) error {
		id, err = addComment(ctx, tx, c)
		return err
	})
	return id, err
}

func addComment(ctx context.Context, tx *sql.Tx, c Comment) (int64, error) {
	// Every comment carries the moment it was written: a caller that left
	// CreatedAt unset would otherwise store year one, which the timeline
	// can only place before everything else on the task.
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now()
	}
	behalf := c.Author.OnBehalfOf
	res, err := tx.ExecContext(ctx, "INSERT INTO `task_comment` ("+
		"`task_id`, `author_kind`, `author_id`, `author_behalf_kind`, `author_behalf_id`, "+
		"`body`, `created_at`, `handover`, `summary`) VALUES (?,?,?,?,?,?,?,?,?)",
		c.TaskID, string(c.Author.Actor.Kind), c.Author.Actor.ID,
		kindOf(behalf), idOf(behalf), c.Body, c.CreatedAt.UTC(), c.Handover, c.Summary)
	if err != nil {
		return 0, fmt.Errorf("commenting on task %s: %w", c.TaskID, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("reading the new comment's id on task %s: %w", c.TaskID, err)
	}
	return id, nil
}

// Comments returns a task's conversation, oldest first. Ordering is by
// the assigned id rather than created_at: two comments written in the
// same instant still have an order, and it is the one they were written
// in.
func (s *Store) Comments(ctx context.Context, taskID string) ([]Comment, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+
		"`id`, `author_kind`, `author_id`, `author_behalf_kind`, `author_behalf_id`, "+
		"`body`, `created_at`, `handover`, `summary` FROM `task_comment` WHERE `task_id` = ? ORDER BY `id`", taskID)
	if err != nil {
		return nil, fmt.Errorf("reading comments on task %s: %w", taskID, err)
	}
	defer rows.Close()

	var out []Comment
	for rows.Next() {
		c := Comment{TaskID: taskID}
		var actorKind, actorID string
		var behalfKind, behalfID sql.NullString
		if err := rows.Scan(&c.ID, &actorKind, &actorID, &behalfKind, &behalfID,
			&c.Body, &c.CreatedAt, &c.Handover, &c.Summary); err != nil {
			return nil, err
		}
		c.Author.Actor = Principal{Kind: PrincipalKind(actorKind), ID: actorID}
		if behalfKind.Valid {
			c.Author.OnBehalfOf = &Principal{
				Kind: PrincipalKind(behalfKind.String), ID: behalfID.String,
			}
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AttachmentMeta is one attachment's wire-worthy metadata, without its
// content -- what a task or a comment listing needs (bwsalmon/agents#522),
// kept separate from Attachment so reading every attachment on a busy
// task never pulls every one's full bytes along with it just to report a
// filename and a size.
type AttachmentMeta struct {
	ID          int64
	TaskID      string
	CommentID   *int64
	Filename    string
	ContentType string
	Size        int64
	CreatedAt   time.Time
}

// AddAttachment stores one file against a.TaskID -- a.CommentID nil for a
// file carried by the task's own body, or naming the Comment.ID it was
// posted alongside otherwise -- and returns the id the store assigned it.
func (s *Store) AddAttachment(ctx context.Context, a Attachment) (id int64, err error) {
	err = s.write(ctx, "add attachment to task "+a.TaskID, func(tx *sql.Tx) error {
		id, err = addAttachment(ctx, tx, a)
		return err
	})
	return id, err
}

func addAttachment(ctx context.Context, tx *sql.Tx, a Attachment) (int64, error) {
	res, err := tx.ExecContext(ctx, "INSERT INTO `task_attachment` ("+
		"`task_id`, `comment_id`, `filename`, `content_type`, `size`, `content`, `created_at`"+
		") VALUES (?,?,?,?,?,?,?)",
		a.TaskID, int64Of(a.CommentID), a.Filename, a.ContentType, a.Size, a.Content, a.CreatedAt.UTC())
	if err != nil {
		return 0, fmt.Errorf("adding attachment %q to task %s: %w", a.Filename, a.TaskID, err)
	}
	return res.LastInsertId()
}

// AttachmentMetas returns every attachment on taskID, oldest first, with
// no content -- what GetTask's own projection needs to list a task's own
// and every comment's attachments. Attachments and GetAttachment are the
// two calls that read content back.
func (s *Store) AttachmentMetas(ctx context.Context, taskID string) ([]AttachmentMeta, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+
		"`id`, `comment_id`, `filename`, `content_type`, `size`, `created_at` "+
		"FROM `task_attachment` WHERE `task_id` = ? ORDER BY `id`", taskID)
	if err != nil {
		return nil, fmt.Errorf("reading attachments on task %s: %w", taskID, err)
	}
	defer rows.Close()

	var out []AttachmentMeta
	for rows.Next() {
		m := AttachmentMeta{TaskID: taskID}
		var commentID sql.NullInt64
		if err := rows.Scan(&m.ID, &commentID, &m.Filename, &m.ContentType, &m.Size, &m.CreatedAt); err != nil {
			return nil, err
		}
		m.CommentID = int64Ptr(commentID)
		out = append(out, m)
	}
	return out, rows.Err()
}

// Attachments returns every attachment on taskID, oldest first, content
// included -- what a dispatched run needs to materialize them into its
// sandbox (orchestrator.placeAttachments).
func (s *Store) Attachments(ctx context.Context, taskID string) ([]Attachment, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+
		"`id`, `comment_id`, `filename`, `content_type`, `size`, `content`, `created_at` "+
		"FROM `task_attachment` WHERE `task_id` = ? ORDER BY `id`", taskID)
	if err != nil {
		return nil, fmt.Errorf("reading attachments on task %s: %w", taskID, err)
	}
	defer rows.Close()

	var out []Attachment
	for rows.Next() {
		a := Attachment{TaskID: taskID}
		var commentID sql.NullInt64
		if err := rows.Scan(&a.ID, &commentID, &a.Filename, &a.ContentType, &a.Size, &a.Content, &a.CreatedAt); err != nil {
			return nil, err
		}
		a.CommentID = int64Ptr(commentID)
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetAttachment returns one attachment on taskID, content included, or
// nil if there is none with that id (including one that exists but
// belongs to a different task) -- the read behind a download endpoint,
// scoped to taskID the same way Comments already scopes by task so one
// task's attachment id can never be used to read another's file.
func (s *Store) GetAttachment(ctx context.Context, taskID string, id int64) (*Attachment, error) {
	a := Attachment{TaskID: taskID}
	var commentID sql.NullInt64
	err := s.db.QueryRowContext(ctx, "SELECT "+
		"`id`, `comment_id`, `filename`, `content_type`, `size`, `content`, `created_at` "+
		"FROM `task_attachment` WHERE `task_id` = ? AND `id` = ?", taskID, id).
		Scan(&a.ID, &commentID, &a.Filename, &a.ContentType, &a.Size, &a.Content, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading attachment %d on task %s: %w", id, taskID, err)
	}
	a.CommentID = int64Ptr(commentID)
	return &a, nil
}

// GetTask returns a task, or nil if there is none with that ID.
func (s *Store) GetTask(ctx context.Context, id string) (*Task, error) {
	return getTask(ctx, s.db, id)
}

func getTask(ctx context.Context, q querier, id string) (*Task, error) {
	t, err := scanTask(q.QueryRowContext(ctx,
		"SELECT "+taskColumns+" FROM `task` WHERE `id` = ?", id).Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if err := hydrate(ctx, q, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// taskColumns is the task row, in the order scanTask reads it. Shared so
// GetTask and ListTasks cannot drift into scanning different column
// orders -- the kind of mismatch that fails as a type error at a
// distance, if it fails at all.
const taskColumns = "`id`,`branch`,`intent`,`title`,`body`," +
	"`origin_actor_kind`,`origin_actor_id`,`origin_behalf_kind`,`origin_behalf_id`,`origin_reason`," +
	"`approval_actor_kind`,`approval_actor_id`,`approval_behalf_kind`,`approval_behalf_id`,`approved_at`,`deferred_at`,`deferred_until`,`undeferred_at`," +
	"`target_owner`,`target_name`,`binding`,`base`,`folder`," +
	"`auto_merge`,`auto_approve_proposals`,`created_at`,`order_key`,`sandbox_cpus`,`sandbox_memory_mb`,`sandbox_disk_gb`,`interactive`," +
	"`agent_framework`,`agent_backend_id`,`agent_model`,`prompt_extension`,`review_template_id`,`network_access`,`nothing_needed_action`,`creator`,`time_zone`,`issue`,`no_port`,`agent_model_source`,`origin_ref`,`resume_on_deadline`,`change_target_kind`,`change_target_spec`"

// scanTask reads one task row. It takes the Scan method rather than a
// *sql.Row or *sql.Rows so one function serves both the single-row and
// the many-row query.
func scanTask(scan func(...any) error) (Task, error) {
	var t Task
	var intent, binding, networkAccess, nothingNeeded, modelSource, workOrigin string
	var ctKind, ctSpec string
	var oaKind, oaID, oReason string
	var obKind, obID, aaKind, aaID, abKind, abID sql.NullString
	var tOwner, tName, base, folder sql.NullString
	var createdAt, approvedAt, deferredAt, deferredUntil, undeferredAt sql.NullTime
	if err := scan(&t.ID, &t.Branch, &intent, &t.Title, &t.Body,
		&oaKind, &oaID, &obKind, &obID, &oReason,
		&aaKind, &aaID, &abKind, &abID, &approvedAt, &deferredAt, &deferredUntil, &undeferredAt,
		&tOwner, &tName, &binding, &base, &folder,
		&t.AutoMerge, &t.AutoApproveProposals, &createdAt, &t.OrderKey, &t.SandboxCPUs, &t.SandboxMemoryMB, &t.SandboxDiskGB, &t.Interactive,
		&t.AgentFramework, &t.AgentBackendID, &t.Model, &t.PromptExtension, &t.ReviewTemplateID, &networkAccess, &nothingNeeded, &t.Creator, &t.TimeZone, &t.Issue, &t.NoPort, &modelSource, &workOrigin, &t.ResumeOnDeadline, &ctKind, &ctSpec); err != nil {
		return Task{}, err
	}
	t.ChangeTarget = ChangeTargetFromStored(ctKind, ctSpec)

	if t.Branch == "" {
		// A state-repository dump from before configurable branches has no
		// branch column. Normalize it on read so the next unrelated update
		// freezes the historical name instead of applying today's template.
		t.Branch = BranchName(t.ID)
	}
	t.Intent, t.Binding = Intent(intent), RepoBinding(binding)
	// Read as it was written, "" included: empty is this task's own "no
	// override" rather than a network to be resolved here, which is the
	// deployment default's job at dispatch (NormalizeNetworkAccess).
	t.NetworkAccess = NetworkAccess(networkAccess)
	t.NothingNeededAction = NothingNeededAction(nothingNeeded)
	t.ModelSource = CompositionLayer(modelSource)
	t.WorkOrigin = WorkOrigin(workOrigin)
	t.Origin = Origin{
		Attribution: Attribution{
			Actor:      Principal{Kind: PrincipalKind(oaKind), ID: oaID},
			OnBehalfOf: principalFrom(obKind, obID),
		},
		Reason: OriginReason(oReason),
	}
	if aaKind.Valid {
		t.Approval = &Attribution{
			Actor:      Principal{Kind: PrincipalKind(aaKind.String), ID: aaID.String},
			OnBehalfOf: principalFrom(abKind, abID),
		}
	}
	t.ApprovedAt = timePtr(approvedAt)
	t.DeferredAt = timePtr(deferredAt)
	t.DeferredUntil = timePtr(deferredUntil)
	t.UndeferredAt = timePtr(undeferredAt)
	if tOwner.Valid {
		target := RepoFromStored(tOwner.String, tName.String)
		t.Target = &target
	}
	t.Base = base.String
	t.Folder = ParseFolder(folder.String)
	if createdAt.Valid {
		t.CreatedAt = &createdAt.Time
	}
	return t, nil
}

// ListTasks returns every task in backlog order -- ascending OrderKey,
// the same order Store.Ready dispatches in -- fully hydrated.
//
// This is what a UI or a CLI lists, and it is deliberately the whole
// table: grain's task count is bounded by what a small team files by
// hand, and a store that answers "everything" in one call costs less
// complexity than a pagination scheme nothing has yet asked for. When
// that stops being true, this is where a filter goes.
//
// Hydration is a handful of queries per task rather than one join over
// all of them. The join would return a task's row once per read target
// per grant per link and need de-duplicating in Go, which is more code
// to get wrong than the extra round trips are worth at this size.
//
// This is the only order there is: ui.Client.ListTasks hands it to a UI
// or a CLI untouched, so a list is read top-to-bottom in the order the
// tasks will run. It used to reverse this slice into a newest-first
// display unless model.Config.NewestFirst said otherwise
// (bwsalmon/agents#476), which left the list pointing one way and the
// dispatcher the other; that flip is gone (grain/task-201) and
// NewestFirst is now only about which end OrderKeyForNewTask files new
// work at. Ties break by id, ascending: OrderKey is unique in
// practice (Store.OrderKeyForNewTask and Store.Reorder both space new
// values away from their neighbours) but nothing enforces it, so a tie
// still needs a stable, deterministic break.
func (s *Store) ListTasks(ctx context.Context) ([]Task, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT "+taskColumns+" FROM `task` ORDER BY `order_key`, `id`")
	if err != nil {
		return nil, fmt.Errorf("listing tasks: %w", err)
	}
	var out []Task
	for rows.Next() {
		t, err := scanTask(rows.Scan)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	for i := range out {
		if err := hydrate(ctx, s.db, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// TaskRepo is one task's target repository, as TaskRepos lists them.
type TaskRepo struct {
	ID     string
	Target *RepoRef
}

// TaskRepos lists every task's target in one query, for a caller that
// needs only those -- the deployment's list of repos, which GET
// /api/config draws -- and not ListTasks's whole row and
// the per-task reads hydrating it, which cost two orders of magnitude
// more on a store of a few thousand tasks (grain/task-3903).
func (s *Store) TaskRepos(ctx context.Context) ([]TaskRepo, error) {
	var out []TaskRepo
	err := each(ctx, s.db, "SELECT `id`,`target_owner`,`target_name` FROM `task` ORDER BY `order_key`, `id`", nil,
		func(rows *sql.Rows) error {
			var t TaskRepo
			var owner, name sql.NullString
			if err := rows.Scan(&t.ID, &owner, &name); err != nil {
				return err
			}
			if owner.Valid {
				target := RepoFromStored(owner.String, name.String)
				t.Target = &target
			}
			out = append(out, t)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("listing task repos: %w", err)
	}
	return out, nil
}

// States reads every task's state from the view in one query, for a
// caller rendering a list -- State per task would be one round trip each,
// and the view already computes the whole column.
func (s *Store) States(ctx context.Context) (map[string]State, error) {
	out := map[string]State{}
	err := each(ctx, s.db, "SELECT `task_id`,`state` FROM `task_state` ORDER BY `task_id`", nil,
		func(rows *sql.Rows) error {
			var id, state string
			if err := rows.Scan(&id, &state); err != nil {
				return err
			}
			out[id] = State(state)
			return nil
		})
	return out, err
}

// StatesPage reads a page of task states from the task_state view, ordered by task_id ascending.
// If limit <= 0, a default limit of 1000 is used.
// If cursor is non-empty, only tasks with task_id > cursor are returned.
// It returns a map of task_id to State, the next cursor (empty string if no more pages), and any error.
func (s *Store) StatesPage(ctx context.Context, limit int, cursor string) (map[string]State, string, error) {
	if limit <= 0 {
		limit = 1000
	}
	query := "SELECT s.`task_id`,s.`state` FROM `task_state` s "
	var args []any
	if cursor != "" {
		query += "WHERE s.`task_id` > ? "
		args = append(args, cursor)
	}
	query += "ORDER BY s.`task_id` LIMIT ?"
	args = append(args, limit+1)

	out := make(map[string]State, limit)
	var lastID string
	var count int
	var hasMore bool

	err := each(ctx, s.db, query, args, func(rows *sql.Rows) error {
		var id, state string
		if err := rows.Scan(&id, &state); err != nil {
			return err
		}
		count++
		if count > limit {
			hasMore = true
			return nil
		}
		out[id] = State(state)
		lastID = id
		return nil
	})
	if err != nil {
		return nil, "", err
	}

	nextCursor := ""
	if hasMore {
		nextCursor = lastID
	}
	return out, nextCursor, nil
}

// EachState streams task states page by page to fn with bounded memory allocation.
func (s *Store) EachState(ctx context.Context, pageSize int, fn func(id string, state State) error) error {
	if pageSize <= 0 {
		pageSize = 1000
	}
	var cursor string
	for {
		page, nextCursor, err := s.StatesPage(ctx, pageSize, cursor)
		if err != nil {
			return err
		}
		for id, st := range page {
			if err := fn(id, st); err != nil {
				return err
			}
		}
		if nextCursor == "" || len(page) == 0 {
			break
		}
		cursor = nextCursor
	}
	return nil
}

// MergeQueueBlocked reads every task_observation row whose
// merge_queue_blocked_at is set, in one query, for a caller rendering a
// list -- the same "one round trip for everyone" trade States already
// makes, applied to the one Observation field a task list needs
// (bwsalmon/agents#494's "make post complete states clear": a human
// needs to see, without opening each task, which ones the merge queue
// has given up fixing on its own).
func (s *Store) MergeQueueBlocked(ctx context.Context) (map[string]time.Time, error) {
	out := map[string]time.Time{}
	err := each(ctx, s.db,
		"SELECT `task_id`,`merge_queue_blocked_at` FROM `task_observation` WHERE `merge_queue_blocked_at` IS NOT NULL", nil,
		func(rows *sql.Rows) error {
			var id string
			var at time.Time
			if err := rows.Scan(&id, &at); err != nil {
				return err
			}
			out[id] = at
			return nil
		})
	return out, err
}

// TurnsStopped is every open task parked on the comment a person posts
// when they stop its turn (TurnStoppedComment, ui.Client.Stop) -- one
// query for a whole list, as MergeQueueBlocked is, so a row can say
// "Stopped by you" rather than that an agent asked something.
func (s *Store) TurnsStopped(ctx context.Context) (map[string]bool, error) {
	out := map[string]bool{}
	err := each(ctx, s.db,
		"SELECT `o`.`task_id` FROM `task_observation` `o` JOIN `task_comment` `c` "+
			"ON `c`.`id` = `o`.`pending_question_comment_id` "+
			"WHERE `o`.`closed_at` IS NULL AND `c`.`body` = ?", TurnStoppedComment,
		func(rows *sql.Rows) error {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			out[id] = true
			return nil
		})
	return out, err
}

// PendingQuestionSummaries is the one-line summary of the question every
// open task is parked on (Comment.Summary), keyed by task -- one query for
// a whole list, as TurnsStopped is, so a row can say what it was asked
// without the conversation. A question stored without a summary gets one
// derived from its body; a stopped turn has none.
func (s *Store) PendingQuestionSummaries(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	err := each(ctx, s.db,
		"SELECT `o`.`task_id`, `c`.`summary`, `c`.`body` FROM `task_observation` `o` JOIN `task_comment` `c` "+
			"ON `c`.`id` = `o`.`pending_question_comment_id` "+
			"WHERE `o`.`closed_at` IS NULL AND `c`.`body` <> ?", TurnStoppedComment,
		func(rows *sql.Rows) error {
			var id, summary, body string
			if err := rows.Scan(&id, &summary, &body); err != nil {
				return err
			}
			if summary == "" {
				summary = DeriveQuestionSummary(body)
			}
			if summary != "" {
				out[id] = summary
			}
			return nil
		})
	return out, err
}

// RunNowRequested is every open task with an outstanding Run now request
// (Observation.RunNowAt), with when it was asked for -- one query for a
// whole list, as MergeQueueBlocked is, so a row can tell a task somebody
// has already pressed Run now on from one still waiting for it.
func (s *Store) RunNowRequested(ctx context.Context) (map[string]time.Time, error) {
	out := map[string]time.Time{}
	err := each(ctx, s.db,
		"SELECT `task_id`,`run_now_at` FROM `task_observation` WHERE `run_now_at` IS NOT NULL AND `closed_at` IS NULL", nil,
		func(rows *sql.Rows) error {
			var id string
			var at time.Time
			if err := rows.Scan(&id, &at); err != nil {
				return err
			}
			out[id] = at
			return nil
		})
	return out, err
}

// MergeQueueStates reads the merge queue's live per-task activity for
// list views -- the state, what it is in words, since when, and whether
// that wait has outlived what drives it, which is the whole of what a row
// has to say about a task grain rather than an agent is now driving
// (MergeQueueActivity).
func (s *Store) MergeQueueStates(ctx context.Context) (map[string]MergeQueueActivity, error) {
	out := map[string]MergeQueueActivity{}
	err := each(ctx, s.db,
		"SELECT `task_id`,`merge_queue_state`,`merge_queue_note`,`merge_queue_state_at`,"+
			"`merge_queue_stalled_at`,`merge_queue_reason` FROM `task_observation` "+
			"WHERE `merge_queue_state` IS NOT NULL AND `merge_queue_state` != ''", nil,
		func(rows *sql.Rows) error {
			var id, state string
			var note, reason sql.NullString
			var at, stalled sql.NullTime
			if err := rows.Scan(&id, &state, &note, &at, &stalled, &reason); err != nil {
				return err
			}
			out[id] = MergeQueueActivity{
				State: state, Note: note.String, At: timePtr(at), StalledAt: timePtr(stalled),
				Reason: reason.String,
			}
			return nil
		})
	return out, err
}

// MergeQueueRepairing reads every task whose automatic repair is in flight.
func (s *Store) MergeQueueRepairing(ctx context.Context) (map[string]bool, error) {
	out := map[string]bool{}
	err := each(ctx, s.db,
		"SELECT `task_id` FROM `task_observation` "+
			"WHERE `merge_queue_repair_at` IS NOT NULL "+
			"AND `completed_at` IS NULL AND `closed_at` IS NULL", nil,
		func(rows *sql.Rows) error {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			out[id] = true
			return nil
		})
	return out, err
}

func hydrate(ctx context.Context, q querier, t *Task) error {
	if err := each(ctx, q,
		"SELECT `owner`,`name` FROM `task_read` WHERE `task_id` = ? ORDER BY `owner`,`name`",
		t.ID, func(rows *sql.Rows) error {
			var owner, name string
			if err := rows.Scan(&owner, &name); err != nil {
				return err
			}
			r := RepoFromStored(owner, name)
			t.Reads = append(t.Reads, r)
			return nil
		}); err != nil {
		return err
	}
	if err := each(ctx, q,
		"SELECT `owner`,`name`,`base` FROM `task_write` WHERE `task_id` = ? ORDER BY `owner`,`name`,`base`",
		t.ID, func(rows *sql.Rows) error {
			var w WriteTarget
			var owner, name string
			if err := rows.Scan(&owner, &name, &w.Base); err != nil {
				return err
			}
			w.Repo = RepoFromStored(owner, name)
			t.Writes = append(t.Writes, w)
			return nil
		}); err != nil {
		return err
	}
	if err := each(ctx, q,
		"SELECT `template_id` FROM `task_filed_template` WHERE `task_id` = ?",
		t.ID, func(rows *sql.Rows) error {
			return rows.Scan(&t.TemplateID)
		}); err != nil {
		return err
	}
	grants, err := grantsOf(ctx, q, t.ID)
	if err != nil {
		return err
	}
	t.Grants = grants
	if err := each(ctx, q,
		"SELECT `kind`,`target` FROM `task_link` WHERE `task_id` = ? ORDER BY `kind`,`target`",
		t.ID, func(rows *sql.Rows) error {
			var l Link
			var kind string
			if err := rows.Scan(&kind, &l.Target); err != nil {
				return err
			}
			l.Kind = LinkKind(kind)
			t.Links = append(t.Links, l)
			return nil
		}); err != nil {
		return err
	}
	if err := each(ctx, q,
		"SELECT `tag` FROM `task_tag` WHERE `task_id` = ? ORDER BY `tag`",
		t.ID, func(rows *sql.Rows) error {
			var tag string
			if err := rows.Scan(&tag); err != nil {
				return err
			}
			t.Tags = append(t.Tags, tag)
			return nil
		}); err != nil {
		return err
	}
	// By value as well as by name, so a multi-valued field reads back in
	// one order rather than in whichever the query planner felt like:
	// what it holds is a set (CustomField.Multiple), and a set that came
	// back shuffled would give the state repository a diff on every
	// export of a task nobody touched.
	return each(ctx, q,
		"SELECT `name`,`value` FROM `task_custom_field` WHERE `task_id` = ? ORDER BY `name`,`value`",
		t.ID, func(rows *sql.Rows) error {
			var name, value string
			if err := rows.Scan(&name, &value); err != nil {
				return err
			}
			// Left nil for a task with no values at all, rather than an
			// empty map: "says nothing about any field" is the ordinary
			// case, and every reader already treats a missing key that
			// way.
			if t.CustomFields == nil {
				t.CustomFields = map[string][]string{}
			}
			t.CustomFields[name] = append(t.CustomFields[name], value)
			return nil
		})
}

// UpdateTask reads a task, applies mutate, and writes it back -- all in
// one stamped transaction, retried from the top if another writer wins.
//
// This is the way to change a task. mutate may run more than once, on a
// task freshly read inside each attempt, so it must be a function of the
// task it is handed rather than of anything captured earlier -- that is
// what makes the retry build on the winner's state rather than rewrite
// over it.
func (s *Store) UpdateTask(ctx context.Context, id string, mutate func(*Task) error) error {
	var missing bool
	err := s.write(ctx, "update task "+id, func(tx *sql.Tx) error {
		missing = false
		task, err := getTask(ctx, tx, id)
		if err != nil {
			return err
		}
		if task == nil {
			missing = true
			return nil
		}
		if err := mutate(task); err != nil {
			return err
		}
		return putTask(ctx, tx, *task)
	})
	if err != nil {
		return err
	}
	if missing {
		return fmt.Errorf("updating task %s: no such task", id)
	}
	return nil
}

// ObserveField reads a task's observation (or starts a fresh one),
// applies set, and writes it back with ObservedAt stamped.
//
// Observe REPLACEs the whole row rather than patching one column, so a
// caller changing one field has to read the row first or erase the
// others. That read-modify-write lives here rather than in every caller,
// inside the same transaction as the write, for the reason UpdateTask
// gives.
func (s *Store) ObserveField(ctx context.Context, taskID string, now time.Time,
	set func(*Observation)) error {

	return s.write(ctx, "observe task "+taskID, func(tx *sql.Tx) error {
		obs, err := getObservation(ctx, tx, taskID)
		if err != nil {
			return fmt.Errorf("reading observation for %s: %w", taskID, err)
		}
		if obs == nil {
			obs = &Observation{TaskID: taskID}
		}
		set(obs)
		obs.ObservedAt = &now
		return observe(ctx, tx, *obs)
	})
}

// AdvanceDelivered moves taskID's Observation.DeliveredCommentID forward
// to through, and never back: a report that arrives late, or twice, or
// from a run that was seeded with an older cursor than the one already
// recorded, leaves the later mark where it is.
func (s *Store) AdvanceDelivered(ctx context.Context, taskID string, through int64, now time.Time) error {
	if through <= 0 {
		return nil
	}
	return s.ObserveField(ctx, taskID, now, func(o *Observation) {
		if o.DeliveredCommentID == nil || *o.DeliveredCommentID < through {
			o.DeliveredCommentID = &through
		}
	})
}

// SetSuspendedSession records the agent conversation this task's last
// run left parked, or clears it -- the whole of the state
// SuspendedSession describes, in one call, because it is one bit with
// two sides and a caller should not have to reach for a different method
// depending on which way it went.
//
// A nil session, and one with no token, both clear it: "there is nothing
// to resume" is the absence of a session rather than a session with an
// empty name, and a framework that could not name one hands back exactly
// that empty string (agent.Result.ResumableSession).
//
// Through ObserveField, so it is a read-modify-write inside one
// transaction like every other single-field observation: Observe
// REPLACEs the whole row, and a write of this field that did not read
// the row first would erase the pull request clocks, the merge queue's
// stamps and the CI verdict beside it.
func (s *Store) SetSuspendedSession(ctx context.Context, taskID string, session *SuspendedSession, now time.Time) error {
	return s.ObserveField(ctx, taskID, now, func(o *Observation) {
		if session == nil || session.Token == "" {
			o.SuspendedSession = nil
			return
		}
		copied := *session
		o.SuspendedSession = &copied
	})
}

// Approve records who approved a task — the whole difference between
// proposed and queued, and what withdrawing would cancel.
//
// It takes approvedAt from the caller rather than reading the clock
// itself, the same discipline ObserveField and FinishRun already hold to
// -- ui.Client.now() is the one clock this store's writes ever run
// against, so a test gets a deterministic timestamp and a real deployment
// gets one instant shared with everything else that call recorded.
func (s *Store) Approve(ctx context.Context, taskID string, a Attribution, approvedAt time.Time) error {
	return s.write(ctx, "approve task "+taskID, func(tx *sql.Tx) error { return approve(ctx, tx, taskID, a, approvedAt) })
}

func approve(ctx context.Context, tx *sql.Tx, taskID string, a Attribution, approvedAt time.Time) error {
	_, err := tx.ExecContext(ctx,
		"UPDATE `task` SET `approval_actor_kind` = ?, `approval_actor_id` = ?, "+
			"`approval_behalf_kind` = ?, `approval_behalf_id` = ?, `approved_at` = ? WHERE `id` = ?",
		string(a.Actor.Kind), a.Actor.ID, kindOf(a.OnBehalfOf), idOf(a.OnBehalfOf), approvedAt.UTC(), taskID)
	return err
}

// WithdrawApproval is approve's exact inverse, down to the columns it
// writes: it clears a task's approval, and a task with no approval is a
// proposal again -- task_state reads it as 'proposed' and task_ready
// never selects one, so nothing dispatches it until somebody approves it
// a second time. Withdrawing from a task that carries no approval writes
// the NULLs that are already there.
//
// approved_at goes with it rather than being kept as history, because
// keeping it would make the row ambiguous: Task.ApprovedAt's own doc
// comment reads a nil approval alongside a set approved_at as "approved
// before this column existed", and Transitions would go on reporting the
// task as having queued at an instant its approval no longer claims.
func (s *Store) WithdrawApproval(ctx context.Context, taskID string) error {
	return s.write(ctx, "withdraw approval from task "+taskID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"UPDATE `task` SET `approval_actor_kind` = NULL, `approval_actor_id` = NULL, "+
				"`approval_behalf_kind` = NULL, `approval_behalf_id` = NULL, `approved_at` = NULL "+
				"WHERE `id` = ?", taskID)
		return err
	})
}

// SetDeferred puts a task aside, or picks it back up: it writes
// task.deferred_at, which is the whole of StateDeferred (Task.DeferredAt
// has the reasoning). deferredAt is the caller's own clock for Approve's
// reason above, and nil is undeferring -- one method rather than two,
// because the pair is one column with two values and nothing else
// distinguishes them.
//
// deferredUntil is the optional wake time that rides along with it
// (Task.DeferredUntil), written in the same statement because the two
// columns are one decision: a deferral either names a date to come back
// on or it does not, and undeferring clears both. Passing a wake time
// with a nil deferredAt would be a task that is not deferred and wakes
// anyway, so callers do not -- ui.Client.Defer is the one that sets it
// and it always sets both.
//
// Approval is left exactly as it was, in both directions. That is the
// point of a column of its own: a task deferred while queued comes back
// queued, and one deferred while proposed comes back proposed, without
// anybody re-deciding a question they already answered. Deferring a task
// that is already deferred rewrites the timestamp, and undeferring one
// that is not writes the NULL that is already there -- neither is worth a
// read to avoid.
//
// An undeferral also stamps task.undeferred_at (Task.UndeferredAt), which
// is why `now` is a parameter rather than a clock read here: the caller's
// own instant, the discipline Approve and ObserveField already hold to.
// It is written in this same statement, on purpose -- every way a task
// comes back goes through this one method, so a stamp here is one nobody
// can forget, and it cannot land without the clearing it records.
//
// The stamp is made only when the task really was deferred, which is what
// the CASE says: undeferring a task that is not deferred picks nothing
// back up, and stamping it anyway would restart the queue clock of a task
// nobody had put aside. SQLite evaluates every SET expression against the
// row as it was before the update, so the deferred_at this reads is the
// old one and not the NULL being written beside it. Deferring leaves the
// column alone rather than clearing it: the return a task made before
// this deferral is a moment that really happened, and Transitions has a
// place on the timeline for it.
func (s *Store) SetDeferred(ctx context.Context, taskID string, deferredAt, deferredUntil *time.Time, now time.Time) error {
	return s.write(ctx, "defer task "+taskID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"UPDATE `task` SET "+
				"`undeferred_at` = CASE WHEN ? IS NULL AND `deferred_at` IS NOT NULL THEN ? "+
				"ELSE `undeferred_at` END, "+
				"`deferred_at` = ?, `deferred_until` = ? WHERE `id` = ?",
			timeOf(deferredAt), now.UTC(), timeOf(deferredAt), timeOf(deferredUntil), taskID)
		return err
	})
}

// DueDeferrals lists the IDs of the tasks whose wake time has arrived --
// deferred, with a Task.DeferredUntil at or before now -- in ID order.
//
// The orchestrator's reconcileWake is the only caller: this is the read
// half of "let a task say when it may start", and it is deliberately as
// narrow as DueSchedules is, IDs rather than whole tasks, because the
// only thing done with one is undeferring it.
//
// Both columns are required to be set, so a wake time left behind on a
// task somebody has already picked up by hand is not a task this brings
// back. That pairing cannot arise through SetDeferred, which writes the
// two together; it can arise through a hand-edited state repository, and
// "not deferred" is the honest reading of such a row.
func (s *Store) DueDeferrals(ctx context.Context, now time.Time) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT `id` FROM `task` WHERE `deferred_at` IS NOT NULL "+
			"AND `deferred_until` IS NOT NULL AND `deferred_until` <= ? "+
			"AND NOT EXISTS (SELECT 1 FROM `task_pause` AS `p` WHERE `p`.`task_id` = `task`.`id`) "+
			"ORDER BY `id`", now.UTC())
	if err != nil {
		return nil, fmt.Errorf("listing due deferrals: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// Observe records what grain has seen about a task.
func (s *Store) Observe(ctx context.Context, o Observation) error {
	return s.write(ctx, "observe task "+o.TaskID, func(tx *sql.Tx) error { return observe(ctx, tx, o) })
}

// observe is Observe's body, against whatever transaction it is running
// in -- so ObserveField can read and write in the same one.
func observe(ctx context.Context, tx *sql.Tx, o Observation) error {
	// The six CI columns are read off a pointer that is nil on almost
	// every row, so they are unpacked into locals first rather than
	// dereferenced six times in the argument list.
	ci := o.CI
	if ci == nil {
		ci = &CIStatus{}
	}
	// The suspended session's four columns, unpacked for the same reason
	// the CI ones are: the pointer is nil on nearly every row, and a nil
	// one writes four empties -- which is how the state "this task has
	// no suspended session" is stored (getObservation reads an empty
	// token back as no session at all).
	session := o.SuspendedSession
	if session == nil {
		session = &SuspendedSession{}
	}
	sessionAt := any(nil)
	if !session.At.IsZero() {
		sessionAt = session.At.UTC()
	}
	streakCount, lastFailed, lastSucceeded, err := computeStreakForTask(ctx, tx, o.TaskID, o.RetryRequestedAt)
	if err == nil {
		o.StreakCount = streakCount
		o.LastFailedAt = lastFailed
		o.LastSucceededAt = lastSucceeded
	}
	_, err = tx.ExecContext(ctx,
		"REPLACE INTO `task_observation` (`task_id`,`closed_at`,`completed_at`,"+
			"`pending_question_comment_id`,`baseline_comment_id`,`pending_secret`,"+
			"`merge_queue_blocked_at`,"+
			"`merge_queue_refreshed_at`,`merge_queue_repair_at`,`merge_queue_state`,"+
			"`merge_queue_note`,`merge_queue_state_at`,`merge_queue_stalled_at`,"+
			"`merge_queue_inherited`,`merge_queue_blocked_base`,`merge_queue_rearms`,"+
			"`merge_queue_repairs`,`merge_queue_repair_base`,`merge_queue_repair_failures`,`merge_queue_repair_why`,"+
			"`merge_queue_stall_retry_at`,`merge_queue_stall_retry_head`,`merge_queue_stall_retry_why`,`merge_queue_entered_at`,`observed_at`,"+
			"`retry_requested_at`,`run_now_at`,`pr_opened_at`,`pr_merged_at`,`pr_closed_at`,"+
			"`pr_additions`,`pr_deletions`,`pr_changed_files`,"+
			"`ci_run_id`,`ci_commit`,`ci_started_at`,`ci_conclusion`,`ci_finished_at`,`ci_detail`,"+
			"`ci_attempts`,`ci_previous_conclusion`,`ci_previous_detail`,`ci_cancelled_by`,`ci_recheck_of`,`ci_recheck_detail`,"+
			"`suspended_session_token`,`suspended_session_framework`,`suspended_session_run_id`,"+
			"`suspended_session_at`,"+
			"`force_merge_at`,`force_merge_reason`,`force_merge_by`,`force_merge_skipped`,`force_merge_head_sha`,"+
			"`streak_count`,`last_failed_at`,`last_succeeded_at`,`delivered_comment_id`,"+
			"`merge_queue_cancel_handoff_at`,`merge_queue_cancel_handoff_run`,`merge_queue_reason`"+
			") VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
		o.TaskID, timeOf(o.ClosedAt), timeOf(o.CompletedAt),
		int64Of(o.PendingQuestionCommentID), int64Of(o.BaselineCommentID), o.PendingSecret,
		timeOf(o.MergeQueueBlockedAt), timeOf(o.MergeQueueRefreshedAt),
		timeOf(o.MergeQueueRepairAt), o.MergeQueueState,
		o.MergeQueueNote, timeOf(o.MergeQueueStateAt), timeOf(o.MergeQueueStalledAt),
		o.MergeQueueInherited, o.MergeQueueBlockedBase, o.MergeQueueRearms,
		o.MergeQueueRepairs, o.MergeQueueRepairBase, o.MergeQueueRepairFailures, o.MergeQueueRepairWhy,
		timeOf(o.MergeQueueStallRetryAt), o.MergeQueueStallRetryHead, o.MergeQueueStallRetryWhy,
		timeOf(o.MergeQueueEnteredAt),
		timeOf(o.ObservedAt), timeOf(o.RetryRequestedAt), timeOf(o.RunNowAt),
		timeOf(o.PrOpenedAt), timeOf(o.PrMergedAt), timeOf(o.PrClosedAt),
		intOf(o.PrAdditions), intOf(o.PrDeletions), intOf(o.PrChangedFiles),
		ci.RunID, ci.Commit, timeOf(ci.StartedAt), ci.Conclusion, timeOf(ci.FinishedAt), ci.Detail,
		ci.Attempts, ci.PreviousConclusion, ci.PreviousDetail, ci.CancelledBy, ci.RecheckOf, ci.RecheckDetail,
		session.Token, session.Framework, session.RunID, sessionAt,
		timeOf(o.ForceMergeAt), o.ForceMergeReason, o.ForceMergeBy, o.ForceMergeSkipped,
		o.ForceMergeHeadSHA,
		o.StreakCount, timeOf(o.LastFailedAt), timeOf(o.LastSucceededAt),
		int64Of(o.DeliveredCommentID),
		timeOf(o.MergeQueueCancelHandoffAt), o.MergeQueueCancelHandoffRun, o.MergeQueueReason)
	return err
}

// GetObservation reads grain's observation row for taskID, nil with no
// error for a task grain has not observed anything about yet.
func (s *Store) GetObservation(ctx context.Context, taskID string) (*Observation, error) {
	return getObservation(ctx, s.db, taskID)
}

func getObservation(ctx context.Context, q querier, taskID string) (*Observation, error) {
	row := q.QueryRowContext(ctx,
		"SELECT `closed_at`,`completed_at`,`pending_question_comment_id`,"+
			"`baseline_comment_id`,`pending_secret`,`merge_queue_blocked_at`,`merge_queue_refreshed_at`,"+
			"`merge_queue_repair_at`,`merge_queue_state`,`merge_queue_note`,`merge_queue_state_at`,"+
			"`merge_queue_stalled_at`,"+
			"`merge_queue_inherited`,`merge_queue_blocked_base`,`merge_queue_rearms`,"+
			"`merge_queue_repairs`,`merge_queue_repair_base`,`merge_queue_repair_failures`,`merge_queue_repair_why`,"+
			"`merge_queue_stall_retry_at`,`merge_queue_stall_retry_head`,`merge_queue_stall_retry_why`,"+
			"`merge_queue_entered_at`,"+
			"`observed_at`,`retry_requested_at`,`run_now_at`,"+
			"`pr_opened_at`,`pr_merged_at`,`pr_closed_at`,"+
			"`pr_additions`,`pr_deletions`,`pr_changed_files`,"+
			"`ci_run_id`,`ci_commit`,`ci_started_at`,`ci_conclusion`,`ci_finished_at`,`ci_detail`,"+
			"`ci_attempts`,`ci_previous_conclusion`,`ci_previous_detail`,`ci_cancelled_by`,`ci_recheck_of`,`ci_recheck_detail`,"+
			"`suspended_session_token`,`suspended_session_framework`,`suspended_session_run_id`,"+
			"`suspended_session_at`,"+
			"`force_merge_at`,`force_merge_reason`,`force_merge_by`,`force_merge_skipped`,`force_merge_head_sha`,"+
			"`streak_count`,`last_failed_at`,`last_succeeded_at`,`delivered_comment_id`,"+
			"`merge_queue_cancel_handoff_at`,`merge_queue_cancel_handoff_run`,`merge_queue_reason` "+
			"FROM `task_observation` WHERE `task_id` = ?", taskID)
	o := Observation{TaskID: taskID}
	var closed, completed, blocked, refreshed, repaired, mergeQueueStateAt sql.NullTime
	// mergeQueueStalledAt is NULL on every row written before
	// ensureColumn(task_observation, merge_queue_stalled_at) ran, which
	// reads back as the "nothing has flagged this wait" a row written
	// since carries.
	var mergeQueueStalledAt sql.NullTime
	// The three inherited-failure columns are NULL on every row written
	// before they were added, which reads as nothing inherited and no
	// re-arm made.
	var mergeQueueInherited, mergeQueueBlockedBase sql.NullString
	var mergeQueueRearms sql.NullInt64
	var mergeQueueRepairs sql.NullInt64
	var mergeQueueRepairBase, mergeQueueRepairFailures, mergeQueueRepairWhy sql.NullString
	var mergeQueueStallRetryAt sql.NullTime
	var mergeQueueStallRetryHead, mergeQueueStallRetryWhy sql.NullString
	var cancelHandoffAt sql.NullTime
	var cancelHandoffRun sql.NullString
	// mergeQueueReason is NULL on every row written before schema 40,
	// which reads as a state whose sub-case nothing recorded.
	var mergeQueueReason sql.NullString
	var mergeQueueEnteredAt sql.NullTime
	var ciRecheckOf, ciRecheckDetail sql.NullString
	var observed, retried, prOpened, prMerged, prClosed sql.NullTime
	// runNow is NULL on every row written before
	// ensureColumn(task_observation, run_now_at) ran, which is the same
	// "nobody has asked for one" a row written since carries.
	var runNow sql.NullTime
	var pending, baseline, delivered sql.NullInt64
	// The diff's three sizes, NULL on every row written before
	// ensureColumn(task_observation, pr_additions) ran and on every task
	// whose pull request has not merged -- which is why they are read as
	// NullInt64 and handed back as pointers rather than as zeroes
	// (Observation.PrAdditions on why the nil matters).
	var prAdditions, prDeletions, prChangedFiles sql.NullInt64
	// pendingSecret is scanned as a NullString rather than into the
	// string field directly: a row written before the column existed
	// carries NULL there, which is the same "nothing pending" an empty
	// string means (ensureTaskObservationPendingSecretColumn).
	var pendingSecret sql.NullString
	// The CI columns are NullString/NullTime for the same reason, and
	// with more of it: every row written before
	// ensureTaskObservationCIColumns ran carries NULL in all of them.
	var mergeQueueState, mergeQueueNote sql.NullString
	var ciRunID, ciCommit, ciConclusion, ciDetail sql.NullString
	var ciPreviousConclusion, ciPreviousDetail, ciCancelledBy sql.NullString
	var ciAttempts sql.NullInt64
	var ciStarted, ciFinished sql.NullTime
	// And the suspended session's four, for the same reason again: a row
	// written before ensureTaskObservationSuspendedSessionColumns ran
	// carries NULL in all of them.
	var sessionToken, sessionFramework, sessionRunID sql.NullString
	var sessionAt sql.NullTime
	// And the override's four, for the same reason once more: every row
	// written before ensureColumn(task_observation, force_merge_at) ran
	// carries NULL in all of them, which is the "nobody has asked for one"
	// a row written since carries (Observation.ForceMergeAt).
	var forceMergeAt sql.NullTime
	var forceMergeReason, forceMergeBy, forceMergeSkipped, forceMergeHeadSHA sql.NullString
	var streakCount sql.NullInt64
	var lastFailedAt, lastSucceededAt sql.NullTime
	if err := row.Scan(&closed, &completed, &pending, &baseline, &pendingSecret, &blocked, &refreshed, &repaired, &mergeQueueState,
		&mergeQueueNote, &mergeQueueStateAt, &mergeQueueStalledAt,
		&mergeQueueInherited, &mergeQueueBlockedBase, &mergeQueueRearms,
		&mergeQueueRepairs, &mergeQueueRepairBase, &mergeQueueRepairFailures, &mergeQueueRepairWhy,
		&mergeQueueStallRetryAt, &mergeQueueStallRetryHead, &mergeQueueStallRetryWhy,
		&mergeQueueEnteredAt,
		&observed, &retried, &runNow, &prOpened, &prMerged, &prClosed,
		&prAdditions, &prDeletions, &prChangedFiles,
		&ciRunID, &ciCommit, &ciStarted, &ciConclusion, &ciFinished, &ciDetail,
		&ciAttempts, &ciPreviousConclusion, &ciPreviousDetail, &ciCancelledBy, &ciRecheckOf, &ciRecheckDetail,
		&sessionToken, &sessionFramework, &sessionRunID, &sessionAt,
		&forceMergeAt, &forceMergeReason, &forceMergeBy, &forceMergeSkipped, &forceMergeHeadSHA,
		&streakCount, &lastFailedAt, &lastSucceededAt, &delivered,
		&cancelHandoffAt, &cancelHandoffRun, &mergeQueueReason); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	// A run id is what makes the other five worth reading: grain records
	// the status when it starts a run, so a row with no run id has never
	// had CI run against it and gets nil rather than an empty CIStatus a
	// caller would have to tell apart from one.
	if ciRunID.String != "" {
		o.CI = &CIStatus{
			RunID:              ciRunID.String,
			Commit:             ciCommit.String,
			StartedAt:          timePtr(ciStarted),
			Conclusion:         ciConclusion.String,
			FinishedAt:         timePtr(ciFinished),
			Detail:             ciDetail.String,
			Attempts:           int(ciAttempts.Int64),
			PreviousConclusion: ciPreviousConclusion.String,
			PreviousDetail:     ciPreviousDetail.String,
			CancelledBy:        ciCancelledBy.String,
			RecheckOf:          ciRecheckOf.String,
			RecheckDetail:      ciRecheckDetail.String,
		}
	}
	// The token is what makes the other three worth reading, exactly as
	// the run id is for CI above: a session with no token is no session,
	// which is how "this task has nothing suspended" is stored. A caller
	// gets nil rather than an empty record it would have to tell apart
	// from one.
	if sessionToken.String != "" {
		o.SuspendedSession = &SuspendedSession{
			Token:     sessionToken.String,
			Framework: sessionFramework.String,
			RunID:     sessionRunID.String,
		}
		if sessionAt.Valid {
			o.SuspendedSession.At = sessionAt.Time
		}
	}
	o.ClosedAt, o.CompletedAt, o.ObservedAt = timePtr(closed), timePtr(completed), timePtr(observed)
	o.PendingQuestionCommentID, o.BaselineCommentID = int64Ptr(pending), int64Ptr(baseline)
	o.DeliveredCommentID = int64Ptr(delivered)
	o.PendingSecret = pendingSecret.String
	o.MergeQueueBlockedAt, o.MergeQueueRefreshedAt = timePtr(blocked), timePtr(refreshed)
	o.MergeQueueRepairAt = timePtr(repaired)
	o.MergeQueueState = mergeQueueState.String
	o.MergeQueueNote, o.MergeQueueStateAt = mergeQueueNote.String, timePtr(mergeQueueStateAt)
	o.MergeQueueStalledAt = timePtr(mergeQueueStalledAt)
	o.MergeQueueReason = mergeQueueReason.String
	o.MergeQueueInherited, o.MergeQueueBlockedBase = mergeQueueInherited.String, mergeQueueBlockedBase.String
	o.MergeQueueRearms = int(mergeQueueRearms.Int64)
	o.MergeQueueRepairs = int(mergeQueueRepairs.Int64)
	o.MergeQueueRepairBase, o.MergeQueueRepairFailures = mergeQueueRepairBase.String, mergeQueueRepairFailures.String
	o.MergeQueueRepairWhy = mergeQueueRepairWhy.String
	o.MergeQueueStallRetryAt = timePtr(mergeQueueStallRetryAt)
	o.MergeQueueStallRetryHead, o.MergeQueueStallRetryWhy = mergeQueueStallRetryHead.String, mergeQueueStallRetryWhy.String
	o.MergeQueueEnteredAt = timePtr(mergeQueueEnteredAt)
	o.MergeQueueCancelHandoffAt, o.MergeQueueCancelHandoffRun = timePtr(cancelHandoffAt), cancelHandoffRun.String
	o.RetryRequestedAt = timePtr(retried)
	o.RunNowAt = timePtr(runNow)
	o.PrOpenedAt, o.PrMergedAt, o.PrClosedAt = timePtr(prOpened), timePtr(prMerged), timePtr(prClosed)
	o.PrAdditions, o.PrDeletions = intPtr(prAdditions), intPtr(prDeletions)
	o.PrChangedFiles = intPtr(prChangedFiles)
	o.ForceMergeAt = timePtr(forceMergeAt)
	o.ForceMergeReason, o.ForceMergeBy = forceMergeReason.String, forceMergeBy.String
	o.ForceMergeSkipped, o.ForceMergeHeadSHA = forceMergeSkipped.String, forceMergeHeadSHA.String
	if streakCount.Valid {
		o.StreakCount = int(streakCount.Int64)
	}
	o.LastFailedAt = timePtr(lastFailedAt)
	o.LastSucceededAt = timePtr(lastSucceededAt)
	return &o, nil
}

// ErrAtCapacity is what StartRun returns instead of recording a run when
// the limits it was given already admit no run of this one's kind. It is
// an ordinary outcome, not a fault: dispatch.Cycle counts live runs
// before it decides what to start, so seeing this means another caller
// started one in between -- the race this exists to lose safely. A caller
// treats it as "no free capacity this tick" and tries again on the next
// one.
var ErrAtCapacity = errors.New("model: the concurrency limit is already reached")

// ErrDurableCeiling is StartRun's refusal of a durable task's run when
// Limits.Durable runs of durable tasks are already live. It is
// ErrAtCapacity (errors.Is), so every caller that treats a full count as
// "not this tick" treats this one the same way.
var ErrDurableCeiling = fmt.Errorf("%w: the durable-run ceiling is full", ErrAtCapacity)

// StartRun records a run and its leases together, so long as the counts
// still admit one more run of this one's kind -- ordinary worker or
// merger. A run of a task with priority dispatch (priorityDispatchTaskSQL:
// a console session -- the director or any other configured session -- or
// a task holding PriorityDispatchCapability or DurableCapability) is held
// to no count but Limits.Durable, and that only for a durable task,
// deployment or team: Limits' own doc comment is the concurrency rule, and
// a task's one live run is the only limit such a task has. The merger and
// priority identities are both read from the task rows: the automation
// that files a session writes the one, and a person's grant the other. Limits.Admits implements the ordinary rule, shared with the
// caller's look-before-you-leap check so the two cannot drift.
//
// Counts are all that is checked here, and the team's are the ones that
// matter: the deployment-wide resource budget is charged in-process,
// under one lock, by whoever admits the sandbox (dispatch.startTask and
// pkg/ci's gate), because a budget every CI job in this process also
// spends cannot be tested from inside one SQL transaction. The limits
// this is handed carry that budget only so a caller has one value to pass
// around; what is enforced below is the counts.
//
// The capacity check happens here, inside the same transaction as the
// insert, rather than in the caller that decided to start this run --
// which is the whole reason it takes limits at all. dispatch.Cycle reads
// the live-run counts and task_ready outside any single transaction and
// then issues a StartRun per unit of headroom it found, so nothing in Go
// stops two overlapping Cycle calls from both seeing the same headroom
// and both spending it. Under slots, a unique index on the slot each run
// claimed caught that after the fact (bwsalmon/agents#434); with nothing
// left to claim, the counts and the insert simply happen together
// instead, which rules the race out rather than detecting it. Limits that
// bound nothing (Limits.Unlimited, which the zero value is) disable the
// check -- for a caller with no limit of its own to enforce, such as a
// test starting a run directly.
func (s *Store) StartRun(ctx context.Context, r Run, limits Limits) error {
	return s.write(ctx, "start run "+r.ID+" for task "+r.TaskID, func(tx *sql.Tx) error {
		// A paused task is never dispatched (TaskPause), whichever path
		// asked: the ready list, a run-now request, a schedule's wake or a
		// console session. Checked here, in the transaction that records
		// the run, so a pause racing a dispatch cannot lose.
		if p, err := getPause(ctx, tx, r.TaskID); err != nil {
			return err
		} else if p != nil {
			return ErrTaskPaused
		}
		priority, err := taskHasPriorityDispatch(ctx, tx, r.TaskID)
		if err != nil {
			return err
		}
		if priority && limits.Durable > 0 {
			durable, err := taskIsDurable(ctx, tx, r.TaskID)
			if err != nil {
				return err
			}
			if durable {
				live, err := liveDurableRuns(ctx, tx)
				if err != nil {
					return err
				}
				if live >= limits.Durable {
					return ErrDurableCeiling
				}
			}
		}
		if !priority && !limits.Unlimited() {
			live, err := liveRunCounts(ctx, tx)
			if err != nil {
				return err
			}
			merger, err := taskIsMerger(ctx, tx, r.TaskID)
			if err != nil {
				return err
			}
			if !limits.Admits(live, merger) {
				return ErrAtCapacity
			}
		}

		return startRun(ctx, tx, r)
	})
}

// taskIsMerger answers mergerTaskSQL for one task id, from the columns
// rather than from a whole Task: this runs inside StartRun's own
// transaction, where the only thing worth reading is what the capacity
// check turns on.
//
// A task id with no row is not a merger. StartRun has no business being
// called for one, and a foreign key on task_run.task_id is what actually
// rejects it a statement later -- guessing "merger" for a task nobody can
// see would be the one reading that spends the reserved capacity.
func taskIsMerger(ctx context.Context, tx *sql.Tx, taskID string) (bool, error) {
	var merger bool
	err := tx.QueryRowContext(ctx,
		"SELECT "+mergerTaskSQL+" FROM `task` AS `t` "+
			"LEFT JOIN `task_observation` AS `o` ON `o`.`task_id` = `t`.`id` "+
			"WHERE `t`.`id` = ?", string(ReasonFix), taskID).Scan(&merger)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading whether task %s's run is a merger: %w", taskID, err)
	}
	return merger, nil
}

// startRun uses INSERT rather than REPLACE, unlike most writes in this
// file, precisely so it does not behave like them here: REPLACE INTO
// resolves a conflict -- on task_run's own id, or on task_run_open_task's
// one-live-run-per-task index (schema.go's own doc comment on it) -- by
// silently deleting the conflicting row and inserting this one, which is
// exactly the silent-overwrite failure mode that index exists to rule
// out. A caller never legitimately starts the same run id twice (RunID's
// own doc comment: an id already names its task and attempt), so INSERT's
// ordinary conflict error is both correct and, for the task index
// specifically, the loud failure a second live run on one task should
// produce instead of one run's row quietly clobbering the other's.
func startRun(ctx context.Context, tx *sql.Tx, r Run) error {
	// Admission has finished: the run row and the disappearance of its
	// queued wait marker are one durable transition.
	if _, err := tx.ExecContext(ctx, "DELETE FROM `task_grant_wait` WHERE `task_id` = ?", r.TaskID); err != nil {
		return err
	}
	// And a standing "run this one now" request is spent by the run it
	// asked for, in that same transaction and for the same reason
	// (Observation.RunNowAt): a request that outlived the run it started
	// would start another the moment that one finished, every cycle,
	// for as long as nobody noticed.
	//
	// What it spent goes onto the run row, though, so a run that ends
	// without doing the work can give it back (settleRunNow): the request
	// outlives such a run, and only such a run, which is RunNowSurvives'
	// list rather than every ending.
	var runNow sql.NullTime
	if err := tx.QueryRowContext(ctx,
		"SELECT `run_now_at` FROM `task_observation` WHERE `task_id` = ?", r.TaskID).Scan(&runNow); err != nil &&
		!errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		"UPDATE `task_observation` SET `run_now_at` = NULL WHERE `task_id` = ?", r.TaskID); err != nil {
		return err
	}
	var spent any
	if runNow.Valid {
		spent = runNow.Time.UTC()
	}
	// The standing the run starts with (model.DispatchStanding, DT-3),
	// which a suspension of it records and is judged against.
	standing, err := dispatchStandingOf(ctx, tx, r.TaskID, timePtr(runNow))
	if err != nil {
		return err
	}
	standingText, err := standingJSON(standing)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO `task_run` (`id`,`task_id`,`sandbox`,`unit`,`attempt`,"+
			"`started_at`,`finished_at`,`outcome`,`run_now_at`,`standing`) VALUES (?,?,?,?,?,?,?,?,?,?)",
		r.ID, r.TaskID, r.Sandbox, nullable(r.Unit), r.Attempt,
		r.StartedAt.UTC(), timeOf(r.FinishedAt), nullable(r.Outcome), spent, standingText); err != nil {
		return err
	}
	if err := resumeSuspensionsTx(ctx, tx, r.TaskID, r.ID, r.StartedAt); err != nil {
		return err
	}
	for _, l := range r.Leases {
		if _, err := tx.ExecContext(ctx,
			"REPLACE INTO `lease` (`run_id`,`capability`,`resource`,`minted_by`,"+
				"`issued_at`,`expires_at`) VALUES (?,?,?,?,?,?)",
			r.ID, l.Capability, l.Resource, l.MintedBy.Name,
			l.IssuedAt.UTC(), timeOf(l.ExpiresAt)); err != nil {
			return err
		}
	}
	return nil
}

func computeStreakForTask(ctx context.Context, q querier, taskID string, retryRequestedAt *time.Time) (int, *time.Time, *time.Time, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT `outcome`, `started_at`, `finished_at`, `suspend_kind` FROM `task_run` "+
			"WHERE `task_id` = ? AND `finished_at` IS NOT NULL ORDER BY `started_at` DESC", taskID)
	if err != nil {
		return 0, nil, nil, err
	}
	defer rows.Close()

	var streakCount int
	var lastFailedAt, lastSucceededAt *time.Time
	var countingStreak = true

	for rows.Next() {
		var outcome string
		var startedAt, finishedAt time.Time
		var suspended sql.NullString
		if err := rows.Scan(&outcome, &startedAt, &finishedAt, &suspended); err != nil {
			return 0, nil, nil, err
		}
		if suspended.Valid || outcome == EnvironmentOutcome {
			// Neither counts towards the streak nor breaks it: a
			// suspension (model.SuspensionKind) is not a judgement of the
			// task, and nor is a host held for a person (OutcomeHeld).
			continue
		}
		if SucceededOutcome(outcome) {
			if lastSucceededAt == nil {
				t := startedAt
				lastSucceededAt = &t
			}
			countingStreak = false
		} else {
			if lastFailedAt == nil {
				t := finishedAt
				lastFailedAt = &t
			}
			if countingStreak {
				if retryRequestedAt != nil && !startedAt.After(*retryRequestedAt) {
					countingStreak = false
				} else {
					streakCount++
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return 0, nil, nil, err
	}
	return streakCount, lastFailedAt, lastSucceededAt, nil
}

func updateTaskStreakTx(ctx context.Context, tx *sql.Tx, taskID string) error {
	obs, err := getObservation(ctx, tx, taskID)
	if err != nil {
		return err
	}
	var retryRequestedAt *time.Time
	if obs != nil {
		retryRequestedAt = obs.RetryRequestedAt
	}
	streakCount, lastFailedAt, lastSucceededAt, err := computeStreakForTask(ctx, tx, taskID, retryRequestedAt)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT OR IGNORE INTO `task_observation` (`task_id`, `streak_count`) VALUES (?, 0)", taskID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		"UPDATE `task_observation` SET `streak_count` = ?, `last_failed_at` = ?, `last_succeeded_at` = ? WHERE `task_id` = ?",
		streakCount, timeOf(lastFailedAt), timeOf(lastSucceededAt), taskID)
	return err
}

// FinishRun records that runID ended at at with outcome and detail, and
// recomputes its task's failure streak in the same transaction so
// task_state's 'failed' cutoff never reads a stale count. An unknown
// runID is not an error: there is nothing to finish.
//
// The same transaction settles the Run now request the run spent, if it
// spent one: a run that ended without doing the work (RunNowSurvives)
// puts it back, so the task is started ahead of the queue again
// (settleRunNow).
//
// An outcome that is a suspension (SuspensionKindOfOutcome) is recorded
// as one in the same transaction, as Store.Suspend records it.
func (s *Store) FinishRun(ctx context.Context, runID string, at time.Time, outcome, detail string) error {
	return s.write(ctx, "finish run "+runID, func(tx *sql.Tx) error {
		return finishRunTx(ctx, tx, runID, at, outcome, detail, nil)
	})
}

// finishRunTx is FinishRun's transaction, and Store.Suspend's with susp
// naming the suspension.
func finishRunTx(ctx context.Context, tx *sql.Tx, runID string, at time.Time, outcome, detail string, susp *Suspension) error {
	var taskID string
	err := tx.QueryRowContext(ctx, "SELECT `task_id` FROM `task_run` WHERE `id` = ?", runID).Scan(&taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		"UPDATE `task_run` SET `finished_at` = ?, `outcome` = ?, `detail` = ? WHERE `id` = ?",
		at.UTC(), outcome, nullable(detail), runID); err != nil {
		return err
	}
	// A finished run has no spool left to tail, in a run host or
	// through grain hostd (HostdCursorKey).
	if _, err := tx.ExecContext(ctx, "DELETE FROM `task_run_spool_cursor` WHERE `run_id` IN (?, ?)",
		runID, HostdCursorKey(runID)); err != nil {
		return err
	}
	if err := recordSuspensionTx(ctx, tx, runID, taskID, outcome, detail, at, susp); err != nil {
		return err
	}
	if err := updateTaskStreakTx(ctx, tx, taskID); err != nil {
		return err
	}
	_, err = settleRunNow(ctx, tx, runID, taskID)
	return err
}

// SetRunOutcome overrides a run's outcome and detail after FinishRun has
// already recorded one -- the one case FinishRun's own caller cannot yet
// know: RunDispatch judges outcome purely from whether the agent made a
// tool call at all (outcomeOf), before ProcessResult has checked
// whether that tool call amounted to anything -- a push, a question, a
// closing comment. A run that made calls but produced none
// of those would otherwise read "succeeded" forever, which both
// misreports what happened and would let it dodge FailureStreak's own
// cap indefinitely (bwsalmon/agents#403).
func (s *Store) SetRunOutcome(ctx context.Context, runID, outcome, detail string) error {
	return s.write(ctx, "set run "+runID+" outcome", func(tx *sql.Tx) error {
		var taskID string
		err := tx.QueryRowContext(ctx, "SELECT `task_id` FROM `task_run` WHERE `id` = ?", runID).Scan(&taskID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			"UPDATE `task_run` SET `outcome` = ?, `detail` = ? WHERE `id` = ?",
			outcome, nullable(detail), runID); err != nil {
			return err
		}
		var finished sql.NullTime
		if err := tx.QueryRowContext(ctx, "SELECT `finished_at` FROM `task_run` WHERE `id` = ?", runID).
			Scan(&finished); err != nil {
			return err
		}
		if err := recordSuspensionTx(ctx, tx, runID, taskID, outcome, detail, finished.Time, nil); err != nil {
			return err
		}
		if err := updateTaskStreakTx(ctx, tx, taskID); err != nil {
			return err
		}
		_, err = settleRunNow(ctx, tx, runID, taskID)
		return err
	})
}

// SetRunSandbox records which sandbox a run was actually given, once one
// has been acquired for it. It is a write of its own, after StartRun
// rather than part of it, because the two happen at genuinely different
// moments: dispatch decides a run may start and records that decision
// durably (dispatch.Cycle's own doc comment) before any sandbox exists,
// and the orchestrator then builds one -- a kontur VM that has to boot,
// or a directory that has to be made -- and names it here.
//
// The gap between the two is deliberately visible rather than papered
// over. A live run whose sandbox is still "" is one whose sandbox is
// still being built, and every reader that resolves a sandbox to its task
// (Store.GitScope, Store.GitCredentialOverride) answers "no live run" for
// the empty name, which is the fail-closed default gitproxy already
// applies to a sandbox it does not recognise: a run that has not got a
// sandbox yet cannot have anything calling the proxy on its behalf.
//
// target is the write target this run was dispatched against -- the
// caller's own snapshot of task.Target, the one it is about to clone
// into that sandbox (orchestrator.runOne reads the task once and works
// from that copy for the rest of the run) -- and it is pinned onto the
// row here, in the same write, deliberately. Naming the sandbox is the
// moment the proxy starts resolving requests through this run
// (GitScope), so the target it resolves them against must already be
// recorded by then; a second write afterwards would leave a window in
// which the run's push rights still followed an edit made to its task.
// nil is a run dispatched for a task that had no target, pinned as
// such -- see the DDL comment in schema.go on why that is not the same
// as leaving the columns NULL.
func (s *Store) SetRunSandbox(ctx context.Context, runID, sandbox string, target *RepoRef) error {
	owner, name := "", ""
	if target != nil {
		owner, name = target.StoredOwner(), target.Name
	}
	return s.write(ctx, "set run "+runID+" sandbox", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"UPDATE `task_run` SET `sandbox` = ?, `target_owner` = ?, `target_name` = ? WHERE `id` = ?",
			sandbox, owner, name, runID)
		return err
	})
}

// SetRunAgentStarted records the moment a run's agent actually got its
// first turn -- everything before it (a sandbox built, a repo cloned, a
// capability minted) is this run's setup, and everything after it is the
// agent's own work. It is its own write, after StartRun, for the same
// reason SetRunSandbox is: the two moments are genuinely different, and
// how far apart they are is the measurement (schema.go's own DDL comment
// on agent_started_at, and pkg/metrics).
//
// Recording it must never cost a run: the caller (orchestrator.
// RunDispatch) logs a failure here and dispatches anyway, since a
// measurement that cannot be taken is not a reason to refuse the work
// being measured.
func (s *Store) SetRunAgentStarted(ctx context.Context, runID string, at time.Time) error {
	return s.write(ctx, "set run "+runID+" agent start", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"UPDATE `task_run` SET `agent_started_at` = ? WHERE `id` = ?", at.UTC(), runID)
		return err
	})
}

// SetRunModel records the model and agent framework a run is running on,
// as dispatch resolved them for it (Run.Model). Its own write, after
// StartRun, because which connection -- and so which model of a default
// list -- a run is given is only settled once its framework is built.
//
// Like SetRunAgentStarted, recording it must never cost a run: the caller
// logs a failure and carries on.
func (s *Store) SetRunModel(ctx context.Context, runID, model, framework string) error {
	return s.write(ctx, "set run "+runID+" model", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"UPDATE `task_run` SET `model` = ?, `agent_framework` = ? WHERE `id` = ?",
			nullable(model), nullable(framework), runID)
		return err
	})
}

// SetRunTranscript records a run's own agent transcript -- the full
// narrative record (agent.Result.Transcript) that FinishRun's own
// outcome/detail only summarise. It is its own write, called after
// FinishRun rather than folded into it, for the same reason SetRunOutcome
// is: RunDispatch only has a transcript to record once framework.Run has
// already returned, by which point FinishRun's own outcome/detail have
// already been decided from it (bwsalmon/agents#446 -- "Show attempt
// agent logs").
func (s *Store) SetRunTranscript(ctx context.Context, runID, transcript string) error {
	return s.write(ctx, "set run "+runID+" transcript", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"UPDATE `task_run` SET `transcript` = ? WHERE `id` = ?",
			nullable(transcript), runID)
		return err
	})
}

// RunTranscript returns the transcript recorded for taskID's numbered
// attempt, and whether such an attempt exists at all -- taskID+attempt
// rather than a bare run ID, since that pair (model.Run.Attempt) is the
// only handle on a run the wire's own Attempt shape ever gives a caller
// (ui.Attempt carries no run ID). The transcript itself may be "" either
// because the attempt has not finished yet or because its framework
// never populated one; both look the same here, and ui.Client tells them
// apart against the attempt's own FinishedAt.
func (s *Store) RunTranscript(ctx context.Context, taskID string, attempt int) (transcript string, found bool, err error) {
	var t sql.NullString
	err = s.db.QueryRowContext(ctx,
		"SELECT `transcript` FROM `task_run` WHERE `task_id` = ? AND `attempt` = ?",
		taskID, attempt).Scan(&t)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return t.String, true, nil
}

// SetRunPrompt records the whole prompt a run's agent was handed --
// everything orchestrator.RunDispatch assembled for it, not just the
// task's own title and body (schema.go's own DDL comment on the column).
// It is its own write, after StartRun, for the same reason
// SetRunAgentStarted is: the prompt only exists once the run's setup is
// over, and it is written immediately before the agent gets its first
// turn so that a run still in flight can already be asked what it was
// told.
//
// Recording it must never cost a run, the same as SetRunAgentStarted:
// its caller logs a failure here and dispatches anyway.
func (s *Store) SetRunPrompt(ctx context.Context, runID, prompt string) error {
	return s.write(ctx, "set run "+runID+" prompt", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"UPDATE `task_run` SET `prompt` = ? WHERE `id` = ?", nullable(prompt), runID)
		return err
	})
}

// RunPrompt returns the prompt recorded for taskID's numbered attempt,
// and whether such an attempt exists at all -- keyed by taskID+attempt
// rather than a bare run ID for the same reason RunTranscript is: that
// pair is the only handle on a run the wire's own Attempt shape ever
// gives a caller. The prompt itself may be "" because the attempt never
// reached its agent (a checkout or a capability that failed first), or
// because it ran before this column existed; both look the same here,
// and ui.Client.TaskPrompt says as much rather than guessing between
// them.
func (s *Store) RunPrompt(ctx context.Context, taskID string, attempt int) (prompt string, found bool, err error) {
	var p sql.NullString
	err = s.db.QueryRowContext(ctx,
		"SELECT `prompt` FROM `task_run` WHERE `task_id` = ? AND `attempt` = ?",
		taskID, attempt).Scan(&p)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return p.String, true, nil
}

// RunPromptSizes is how big each of taskID's attempts' prompts was, by
// attempt number -- the measurement behind "is this task's prompt
// growing?", which is a question nobody could ask of a deployment until
// it had a number per attempt to ask it with (model.PromptSize,
// grain/task-268).
//
// Measured in the database rather than by reading every attempt's prompt
// back into Go: a task with ten attempts carries ten prompts of up to a
// few hundred kilobytes each, and a caller that wants the sizes -- a
// task record, a report over a window -- wants none of that text. CAST
// to BLOB because SQLite's length() counts characters on TEXT, and what
// a context window is spent in is bytes.
//
// An attempt that never reached its agent recorded no prompt and is
// absent from the map rather than present as a nought: "this run was
// never told anything" and "this run was told nothing" are different
// facts, and a zero here would average into every report as the second.
func (s *Store) RunPromptSizes(ctx context.Context, taskID string) (map[int]int, error) {
	out := map[int]int{}
	err := each(ctx, s.db,
		"SELECT `attempt`,length(CAST(`prompt` AS BLOB)) FROM `task_run` "+
			"WHERE `task_id` = ? AND `prompt` IS NOT NULL ORDER BY `attempt`",
		[]any{taskID},
		func(rows *sql.Rows) error {
			var attempt, size int
			if err := rows.Scan(&attempt, &size); err != nil {
				return err
			}
			out[attempt] = size
			return nil
		})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// LastPrompt is what the most recent dispatch of taskID cost: how big
// the prompt handed to it was, and whether grain had to cut the task's
// conversation down to fit it (PromptThreadCutNotice).
//
// The twin of RunPromptSizes for a reader that wants one number rather
// than the series -- "is this conversation costing its runs *now*",
// which is the only question a control offering a fresh thread may turn
// on (ui.Client.ListConsoleSessions). The series is what says whether a
// prompt is growing; the last row is what says whether it has arrived.
//
// A task no run has ever been dispatched for, or whose attempts all
// failed before reaching their agent, has no measurement at all and gets
// a zero PromptSize rather than a prompt of nought bytes -- PromptSize.
// Zero is how a caller tells the two apart, and Large is false for it
// either way. Measured in the database for RunPromptSizes' own reason,
// with the same instr() test RunPrompts uses so that a prompt merely
// quoting a cut notice is not counted as one.
func (s *Store) LastPrompt(ctx context.Context, taskID string) (size PromptSize, threadCut bool, err error) {
	var bytes int
	err = s.db.QueryRowContext(ctx,
		"SELECT length(CAST(`prompt` AS BLOB)),"+
			"(instr(`prompt`, ?) > 0 OR instr(`prompt`, ?) > 0) "+
			"FROM `task_run` WHERE `task_id` = ? AND `prompt` IS NOT NULL AND `prompt` <> '' "+
			"ORDER BY `attempt` DESC LIMIT 1",
		"\n"+PromptThreadCutNotice, "\n"+PromptMessageCutNotice, taskID).Scan(&bytes, &threadCut)
	if errors.Is(err, sql.ErrNoRows) {
		return PromptSize{}, false, nil
	}
	if err != nil {
		return PromptSize{}, false, err
	}
	return MeasurePromptBytes(bytes), threadCut, nil
}

// SetTaskActivity records what taskID's live run says it is doing right
// now -- one short phrase the run wrote for itself, and the moment it
// wrote it (Run.Activity). It reports whether there was a live run to
// record it against at all: false, with no error, means the task has
// nothing in flight, which is not a failure of the write but the answer
// to a call that arrived after its run was over.
//
// Keyed by task rather than by run because the caller is the task's own
// run reaching back through the daemon (ui.Client.SetTaskActivity, over
// the update_status tool), and a run identifies itself by the task it was
// dispatched for -- the same handle every other write an agent can reach
// takes. The WHERE clause is what makes that safe: at most one run per
// task is ever open (schema.go's task_run_open_task index), so "the live
// run of this task" names exactly one row, and a call that arrives late
// cannot overwrite the synopsis a finished run ended on.
//
// The agent is not the only caller. grain narrates the run's own setup
// through this same write, before there is an agent to call anything
// (orchestrator.setupNotes), which is why nothing here is agent-specific
// and why "" is a meaningful argument: it stores NULL, clearing whatever
// stood, and is how the dispatch path hands a clean row to the agent it
// is about to start. ui.Client.SetTaskActivity refuses "" on the way in
// from the tool, so an agent cannot blank its own row by accident.
func (s *Store) SetTaskActivity(ctx context.Context, taskID, activity string, at time.Time) (live bool, err error) {
	// Both columns go, not just the note: activity_at is the note's own
	// timestamp and nothing else reads it, so a cleared row that kept one
	// would only offer a later reader a time with nothing it belongs to.
	var stamp any = at.UTC()
	if activity == "" {
		stamp = nil
	}
	err = s.write(ctx, "set task "+taskID+" activity", func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			"UPDATE `task_run` SET `activity` = ?, `activity_at` = ? "+
				"WHERE `task_id` = ? AND `finished_at` IS NULL",
			nullable(activity), stamp, taskID)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		live = n > 0
		return nil
	})
	return live, err
}

// TaskActivity is what every task with a live run currently says it is
// doing, keyed by task ID -- one query for a whole list, the same trade
// MergeQueueBlocked above makes for the merge queue's own signal rather
// than a read per task.
//
// Only live runs are here, and deliberately: a synopsis is a fact about
// what is happening now, and a finished run's last words describe a
// moment that is over and would read on a task list as work still in
// progress. The rows keep it (nothing clears the columns at FinishRun),
// so a caller that wants the last thing a finished run said can still
// read it off the run itself.
//
// A live run that has never called the tool is absent rather than present
// and empty: "said nothing" and "is not running" are different, but
// neither gives a caller anything to show.
//
// agent_started_at comes along because it is what says whose phrase this
// is: unset means the run has not been handed to its agent yet, so the
// note is one grain stamped during setup (RunActivity.BySetup).
func (s *Store) TaskActivity(ctx context.Context) (map[string]RunActivity, error) {
	out := map[string]RunActivity{}
	err := each(ctx, s.db,
		"SELECT `task_id`,`activity`,`activity_at`,`agent_started_at` FROM `task_run` "+
			"WHERE `finished_at` IS NULL AND `activity` IS NOT NULL", nil,
		func(rows *sql.Rows) error {
			var taskID string
			var note sql.NullString
			var at, agentStarted sql.NullTime
			if err := rows.Scan(&taskID, &note, &at, &agentStarted); err != nil {
				return err
			}
			out[taskID] = RunActivity{Note: note.String, At: timePtr(at), BySetup: !agentStarted.Valid}
			return nil
		})
	return out, err
}

// TaskActivityOf is TaskActivity for one task -- what its live run says
// it is doing, or nil for a task with nothing in flight or a run that has
// said nothing yet. A caller rendering one task uses this rather than
// reading a map of the whole store, the same split Client.Task and
// Client.ListTasks already make over MergeQueueBlocked.
func (s *Store) TaskActivityOf(ctx context.Context, taskID string) (*RunActivity, error) {
	var note sql.NullString
	var at, agentStarted sql.NullTime
	err := s.db.QueryRowContext(ctx,
		"SELECT `activity`,`activity_at`,`agent_started_at` FROM `task_run` "+
			"WHERE `task_id` = ? AND `finished_at` IS NULL", taskID).Scan(&note, &at, &agentStarted)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !note.Valid) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &RunActivity{Note: note.String, At: timePtr(at), BySetup: !agentStarted.Valid}, nil
}

// TaskSpans is when work began and ended on every task that has begun or
// ended at all, keyed by task ID -- TaskActivity's own "one query for a
// whole list" trade, for the two timestamps a timeline over many tasks
// needs (TaskSpan, ui/src/components/GanttChart.jsx).
//
// One query rather than three, because a span is one fact about a task
// assembled from two tables: the first attempt's start comes from
// task_run, the ending from task_observation, and a per-task read of
// either would put a query per row behind a screen that draws hundreds.
//
// There is no GROUP BY: the join hands back one row per attempt (and one
// for a task with none), and the earliest start is kept here rather than
// by MIN(). An aggregate would need either SQLite's bare-column rule --
// a plain column beside MIN() takes the minimum's row -- which PostgreSQL
// refuses outright ("must appear in the GROUP BY clause", SQLSTATE 42803,
// a 500 on every GET /api/tasks), or scanning MIN() itself, whose result
// carries no declared type for the SQLite driver to convert to a time.
// The observation columns are the same on every one of a task's rows, so
// whichever row is read last cannot change them.
//
// A task with neither -- proposed, queued, never dispatched and never
// closed -- is absent rather than present and empty, the same way
// TaskActivity leaves out a task that is not running: there is nothing to
// draw, and the caller already knows the task exists.
func (s *Store) TaskSpans(ctx context.Context) (map[string]TaskSpan, error) {
	out := map[string]TaskSpan{}
	err := each(ctx, s.db,
		"SELECT `t`.`id`,`r`.`started_at`,`o`.`completed_at`,`o`.`closed_at`,`o`.`pr_merged_at`,`o`.`pr_closed_at` "+
			"FROM `task` AS `t` "+
			"LEFT JOIN `task_run` AS `r` ON `r`.`task_id` = `t`.`id` "+
			"LEFT JOIN `task_observation` AS `o` ON `o`.`task_id` = `t`.`id`", nil,
		func(rows *sql.Rows) error {
			var id string
			var started, completed, closed, prMerged, prClosed sql.NullTime
			if err := rows.Scan(&id, &started, &completed, &closed, &prMerged, &prClosed); err != nil {
				return err
			}
			startedAt := timePtr(started)
			if prev, ok := out[id]; ok && prev.StartedAt != nil && (startedAt == nil || prev.StartedAt.Before(*startedAt)) {
				startedAt = prev.StartedAt
			}
			span := TaskSpan{
				StartedAt:  startedAt,
				EndedAt:    earlier(timePtr(completed), timePtr(closed)),
				ClosedAt:   timePtr(closed),
				PrMergedAt: timePtr(prMerged),
				PrClosedAt: timePtr(prClosed),
			}
			if span.StartedAt == nil && span.EndedAt == nil && span.ClosedAt == nil && span.PrMergedAt == nil && span.PrClosedAt == nil {
				return nil
			}
			out[id] = span
			return nil
		})
	return out, err
}

// TaskSpanOf is TaskSpans for one task, for a caller rendering a single
// one -- the same split TaskActivityOf makes of TaskActivity. A task that
// has neither started nor ended reads back as a zero TaskSpan rather than
// as an error: "no span yet" is the ordinary answer for everything still
// waiting its turn.
func (s *Store) TaskSpanOf(ctx context.Context, taskID string) (TaskSpan, error) {
	// Two reads rather than the join above, because for one task neither
	// half needs one: the first attempt is an ORDER BY ... LIMIT 1 -- a
	// plain column, for the reason TaskSpans' own comment gives -- and the
	// ending sits in the single observation row this store already knows
	// how to read.
	var started sql.NullTime
	err := s.db.QueryRowContext(ctx,
		"SELECT `started_at` FROM `task_run` WHERE `task_id` = ? "+
			"ORDER BY `started_at` LIMIT 1", taskID).Scan(&started)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return TaskSpan{}, err
	}
	obs, err := s.GetObservation(ctx, taskID)
	if err != nil {
		return TaskSpan{}, err
	}
	span := TaskSpan{StartedAt: timePtr(started)}
	if obs != nil {
		span.EndedAt = earlier(obs.CompletedAt, obs.ClosedAt)
		span.ClosedAt = obs.ClosedAt
		span.PrMergedAt = obs.PrMergedAt
		span.PrClosedAt = obs.PrClosedAt
	}
	return span, nil
}

// earlier is the rule both readers above share for an ending: the sooner
// of a completion and a closure, and whichever one there is when there is
// only one (TaskSpan's own doc comment on why the sooner).
func earlier(a, b *time.Time) *time.Time {
	if a == nil {
		return b
	}
	if b == nil || a.Before(*b) {
		return a
	}
	return b
}

// StateTimes is when every task's current state could have begun,
// keyed by task ID -- StateTimesFrom's own gathering in SQL, for a
// caller rendering a whole list rather than one task (TaskSpans' trade,
// for the same reason: the alternative is a read of every task's runs
// and observation behind a screen that draws hundreds).
//
// Two queries rather than one because two aggregates over task_run
// cannot share a row. The first picks each task's last *finished*
// attempt with TaskSpans' own bare-column rule -- one MAX() in the
// select list, and the plain column beside it takes its value from the
// row that produced it, which is what lets it be scanned as the DATETIME
// it is declared as rather than as the driver's string rendering of an
// expression. The second reads the live runs, of which a task has at
// most one, as a plain column needing no aggregate at all.
//
// Every task in the store is present, including the ones with nothing to
// say: unlike TaskSpans, whose absent rows mean "nothing to draw", a
// caller here is asking a question about a state the task is in right
// now, and a zero StateTimes answers it (Since returns nil) without the
// caller having to tell a missing key from an empty value.
func (s *Store) StateTimes(ctx context.Context) (map[string]StateTimes, error) {
	out := map[string]StateTimes{}
	err := each(ctx, s.db,
		"SELECT `t`.`id`,`t`.`approved_at`,`t`.`undeferred_at`,"+
			"`o`.`completed_at`,`o`.`retry_requested_at`,`o`.`run_now_at`,"+
			"MAX(`r`.`finished_at`),`r`.`finished_at` "+
			"FROM `task` AS `t` "+
			"LEFT JOIN `task_run` AS `r` ON `r`.`task_id` = `t`.`id` "+
			"LEFT JOIN `task_observation` AS `o` ON `o`.`task_id` = `t`.`id` "+
			"GROUP BY `t`.`id`", nil,
		func(rows *sql.Rows) error {
			var id string
			var max sql.NullString
			var approved, undeferred, completed, retried, runNow, finished sql.NullTime
			if err := rows.Scan(&id, &approved, &undeferred, &completed, &retried, &runNow, &max, &finished); err != nil {
				return err
			}
			out[id] = StateTimes{
				ApprovedAt:        timePtr(approved),
				RequeuedAt:        latest(timePtr(retried), timePtr(runNow), timePtr(undeferred)),
				LastRunFinishedAt: timePtr(finished),
				CompletedAt:       timePtr(completed),
			}
			return nil
		})
	if err != nil {
		return nil, err
	}
	err = each(ctx, s.db,
		"SELECT `task_id`,`started_at` FROM `task_run` WHERE `finished_at` IS NULL", nil,
		func(rows *sql.Rows) error {
			var id string
			var started time.Time
			if err := rows.Scan(&id, &started); err != nil {
				return err
			}
			// A live run for a task the first query did not see cannot
			// happen -- task_run.task_id references task(id) -- but the
			// map read is what keeps the rest of the row if it ever does.
			times := out[id]
			times.LiveRunStartedAt = &started
			out[id] = times
			return nil
		})
	return out, err
}

// DropLease forgets a lease once its resource is actually revoked.
// Idempotent by construction — a DELETE matching nothing is not an error,
// which is what lets release and the expiry reaper both reach the same
// lease without coordinating.
func (s *Store) DropLease(ctx context.Context, runID, capability, resource string) error {
	return s.write(ctx, "drop lease "+capability+" on "+resource, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"DELETE FROM `lease` WHERE `run_id` = ? AND `capability` = ? AND `resource` = ?",
			runID, capability, resource)
		return err
	})
}

// RecordLeases records leases against runID, beside the ones admission
// reserved for it (StartRun): what a run minted while it set up, which
// otherwise lived only on the stack of the daemon that started it. A
// daemon adopting the run reads them back (RunLeases) to revoke them at
// its end, as RunDispatch would have (orchestrator.ResumeAdoptedRun).
// Recording a lease already recorded replaces it, so a reservation that
// materialization folded into its own answer is written once.
func (s *Store) RecordLeases(ctx context.Context, runID string, leases []Lease) error {
	if len(leases) == 0 {
		return nil
	}
	return s.write(ctx, "record leases of run "+runID, func(tx *sql.Tx) error {
		for _, l := range leases {
			if _, err := tx.ExecContext(ctx,
				"REPLACE INTO `lease` (`run_id`,`capability`,`resource`,`minted_by`,"+
					"`issued_at`,`expires_at`) VALUES (?,?,?,?,?,?)",
				runID, l.Capability, l.Resource, l.MintedBy.Name,
				l.IssuedAt.UTC(), timeOf(l.ExpiresAt)); err != nil {
				return err
			}
		}
		return nil
	})
}

// RunLeases is every lease still recorded against runID, oldest first --
// what is left to revoke when the run ends.
func (s *Store) RunLeases(ctx context.Context, runID string) ([]Lease, error) {
	var out []Lease
	err := each(ctx, s.db,
		"SELECT `capability`,`resource`,`minted_by`,`issued_at`,`expires_at` "+
			"FROM `lease` WHERE `run_id` = ? ORDER BY `issued_at`,`capability`,`resource`", []any{runID},
		func(rows *sql.Rows) error {
			var l Lease
			var expires sql.NullTime
			if err := rows.Scan(&l.Capability, &l.Resource, &l.MintedBy.Name, &l.IssuedAt, &expires); err != nil {
				return err
			}
			l.ExpiresAt = timePtr(expires)
			out = append(out, l)
			return nil
		})
	return out, err
}

// Attempts is how many times a task has been run — answerable because
// runs are rows, where the records previously existed as files nothing
// aggregated.
func (s *Store) Attempts(ctx context.Context, taskID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM `task_run` WHERE `task_id` = ?", taskID).Scan(&n)
	return n, err
}

// Runs is every attempt at taskID, oldest first -- the full history
// Attempts only counts and FailureStreak only summarises, for a caller
// (Client.GetTask) that wants to show each one: when it ran, whether it
// finished, and how (bwsalmon/agents#445).
func (s *Store) Runs(ctx context.Context, taskID string) ([]Run, error) {
	var out []Run
	err := each(ctx, s.db,
		"SELECT `id`,`sandbox`,`unit`,`attempt`,`started_at`,`finished_at`,`outcome`,`detail`,"+
			"`activity`,`activity_at`,`agent_started_at`,`suspend_kind`,`model`,`agent_framework` "+
			"FROM `task_run` WHERE `task_id` = ? ORDER BY `attempt` ASC", taskID,
		func(rows *sql.Rows) error {
			r := Run{TaskID: taskID}
			var unit, outcome, detail, activity, suspended, runModel, runFramework sql.NullString
			var finishedAt, activityAt, agentStartedAt sql.NullTime
			if err := rows.Scan(&r.ID, &r.Sandbox, &unit, &r.Attempt,
				&r.StartedAt, &finishedAt, &outcome, &detail, &activity, &activityAt,
				&agentStartedAt, &suspended, &runModel, &runFramework); err != nil {
				return err
			}
			r.Unit, r.Outcome, r.Detail = unit.String, outcome.String, detail.String
			r.SuspendKind = SuspensionKind(suspended.String)
			r.Model, r.AgentFramework = runModel.String, runFramework.String
			// agent_started_at comes along for the same reason
			// TaskActivity selects it: it is what says whether the
			// activity beside it is grain's own account of this run's
			// setup or the run's own (Run.AgentStartedAt).
			r.Activity, r.ActivityAt = activity.String, timePtr(activityAt)
			r.AgentStartedAt = timePtr(agentStartedAt)
			r.FinishedAt = timePtr(finishedAt)
			out = append(out, r)
			return nil
		})
	return out, err
}

// FailureStreak is taskID's own failure streak (Count), plus the most
// recent finished run's own outcome/detail and BackoffCount -- what
// task_observation.streak_count (computeStreakForTask, which task_state
// and the task_streak view read) does not carry, walked again here over
// the same runs so a caller gets all of it from one read.
//
// nil, with no error, means taskID has never finished a run at all --
// dispatch.Cycle's own retry backoff and Client.GetTask's own display
// both treat that the same as "not currently failing".
//
// A PausedOutcome run is passed over rather than counted, exactly as
// computeStreakForTask passes over it: it is neither a failure
// of this task nor a success that clears the failures before it. A
// CapacityOutcome one is passed over the same way, and for the same
// reason, with one difference: it still counts toward BackoffCount,
// because nothing pauses dispatch for it (that constant's own doc
// comment). Their LastFinishedAt/LastOutcome still describe them where
// they are the most recent run, since those two fields answer "what
// happened last", not "how badly is this task going".
func (s *Store) FailureStreak(ctx context.Context, taskID string) (*FailureStreak, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT `outcome`,`started_at`,`finished_at`,`detail`,`suspend_kind` FROM `task_run` "+
			"WHERE `task_id` = ? AND `finished_at` IS NOT NULL ORDER BY `started_at` DESC", taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var since time.Time
	if obs, err := getObservation(ctx, s.db, taskID); err != nil {
		return nil, err
	} else if obs != nil && obs.RetryRequestedAt != nil {
		since = *obs.RetryRequestedAt
	}

	var streak *FailureStreak
	for rows.Next() {
		var outcome string
		var startedAt, finishedAt time.Time
		var detail, suspended sql.NullString
		if err := rows.Scan(&outcome, &startedAt, &finishedAt, &detail, &suspended); err != nil {
			return nil, err
		}
		if streak == nil {
			streak = &FailureStreak{LastStartedAt: startedAt, LastFinishedAt: finishedAt,
				LastOutcome: outcome, LastDetail: detail.String}
		}
		if !since.IsZero() && !startedAt.After(since) {
			break
		}
		// Read before success: a park is a success word and a suspension
		// (SuspendRequested), and computeStreakForTask skips it.
		if !suspended.Valid && SucceededOutcome(outcome) {
			break
		}
		if suspended.Valid {
			// Skipped, not counted and not a boundary: a suspended run
			// (task_run.suspend_kind, model.SuspensionKind) was stopped
			// for a reason that is evidence about the deployment and none
			// at all about this task, so it neither adds to the streak
			// behind it nor clears it. computeStreakForTask skips the same
			// rows the same way.
			//
			// BackoffCount is incremented for a capacity suspension alone.
			// Nothing stops dispatch while a provider is overloaded, so
			// without it the task would be sent back at the same refusal
			// on every poll for as long as the outage lasted, rebuilding a
			// sandbox each time. Every other kind either stops dispatch
			// for as long as it lasts (a usage limit pauses the
			// deployment) or should be resumed without waiting.
			if SuspensionKind(suspended.String) == SuspendCapacity {
				streak.BackoffCount++
			}
			continue
		}
		if outcome == EnvironmentOutcome {
			// Skipped from Count and from BackoffCount: a host whose
			// credentials expired says nothing about this task
			// (EnvironmentOutcome's doc comment), and the task is parked
			// until a human has fixed the host and replied, so there is
			// no retry here to space out.
			continue
		}
		streak.Count++
		streak.BackoffCount++
	}
	return streak, rows.Err()
}

// ConsecutiveOOMRuns is how many of taskID's most recent finished runs,
// in a row, ended as OOMOutcome. Runs the deployment ended for reasons of
// its own (paused, capacity, interrupted, environment) neither count nor
// break the run of them; anything else, a success included, ends it. A
// retry request (task_observation.retry_requested_at) ends it too, as it
// ends the failure streak.
func (s *Store) ConsecutiveOOMRuns(ctx context.Context, taskID string) (int, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT `outcome`,`started_at` FROM `task_run` "+
			"WHERE `task_id` = ? AND `finished_at` IS NOT NULL ORDER BY `started_at` DESC", taskID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var since time.Time
	if obs, err := getObservation(ctx, s.db, taskID); err != nil {
		return 0, err
	} else if obs != nil && obs.RetryRequestedAt != nil {
		since = *obs.RetryRequestedAt
	}
	n := 0
	for rows.Next() {
		var outcome string
		var startedAt time.Time
		if err := rows.Scan(&outcome, &startedAt); err != nil {
			return 0, err
		}
		if !since.IsZero() && !startedAt.After(since) {
			break
		}
		switch outcome {
		case OOMOutcome:
			n++
			continue
		case PausedOutcome, CapacityOutcome, InterruptedOutcome, EnvironmentOutcome:
			continue
		}
		break
	}
	return n, rows.Err()
}

// OOMRetryActivity is the task row's line for a queued task whose last
// run was OOM-killed (Store.OOMRetrying).
const OOMRetryActivity = "retrying after OOM kill"

// OOMRetrying is every task whose most recent finished run ended as
// OOMOutcome, with when it ended -- what a task row reads to say
// "retrying after OOM kill" while the task waits for its next run. One
// query for a whole list, as TaskActivity is.
func (s *Store) OOMRetrying(ctx context.Context) (map[string]time.Time, error) {
	return s.oomRetrying(ctx, "")
}

// OOMRetryingOf is OOMRetrying for one task: when its last run was
// OOM-killed, nil when its last run ended any other way.
func (s *Store) OOMRetryingOf(ctx context.Context, taskID string) (*time.Time, error) {
	m, err := s.oomRetrying(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if at, ok := m[taskID]; ok {
		return &at, nil
	}
	return nil, nil
}

func (s *Store) oomRetrying(ctx context.Context, taskID string) (map[string]time.Time, error) {
	q := "SELECT `r`.`task_id`, `r`.`finished_at` FROM `task_run` AS `r` " +
		"WHERE `r`.`outcome` = ? AND `r`.`finished_at` IS NOT NULL AND NOT EXISTS (" +
		"SELECT 1 FROM `task_run` AS `n` WHERE `n`.`task_id` = `r`.`task_id` AND `n`.`started_at` > `r`.`started_at`)"
	args := []any{OOMOutcome}
	if taskID != "" {
		q += " AND `r`.`task_id` = ?"
		args = append(args, taskID)
	}
	out := map[string]time.Time{}
	err := each(ctx, s.db, q, args, func(rows *sql.Rows) error {
		var id string
		var at time.Time
		if err := rows.Scan(&id, &at); err != nil {
			return err
		}
		out[id] = at
		return nil
	})
	return out, err
}

// FailureStreak is one task's own retry history -- see Store.FailureStreak.
type FailureStreak struct {
	// Count is how many of the task's most recent runs, in a row, ended
	// without succeeding -- 0 once the most recent run itself succeeded,
	// or was requested more recently than the task's last retry request.
	Count int
	// BackoffCount is the same streak counted for a different question:
	// not "how badly is this task going" but "how soon should it be
	// tried again". It is Count plus the runs a provider's own capacity
	// ended (CapacityOutcome), which are not the task's fault -- so they
	// stay out of Count and out of MaxConsecutiveFailures -- but are
	// still runs that produced nothing, and firing the next attempt at
	// the same overloaded provider on the next poll helps neither the
	// task nor the provider. dispatch.retryEligible spaces retries by
	// this one; StateOf caps tasks by Count.
	BackoffCount int
	// LastStartedAt is when the most recent finished run began, which
	// dispatch reads to tell a Run now request that run put back
	// (made before it started) from one made since.
	LastStartedAt  time.Time
	LastFinishedAt time.Time
	LastOutcome    string
	LastDetail     string
}

// LiveLease is one outstanding lease, joined to the run holding it.
type LiveLease struct {
	RunID      string
	TaskID     string
	Capability string
	Resource   string
	MintedBy   string
	IssuedAt   time.Time
	ExpiresAt  *time.Time
}

// LiveLeases returns outstanding leases, optionally only those minted by
// one credential — which is what makes "what would rotating this break?"
// a query rather than an unanswerable question.
func (s *Store) LiveLeases(ctx context.Context, mintedBy string) ([]LiveLease, error) {
	q := "SELECT `run_id`,`task_id`,`capability`,`resource`,`minted_by`,`issued_at`,`expires_at` " +
		"FROM `lease_live`"
	args := []any{}
	if mintedBy != "" {
		q += " WHERE `minted_by` = ?"
		args = append(args, mintedBy)
	}
	q += " ORDER BY `issued_at`"
	var out []LiveLease
	err := each(ctx, s.db, q, args, func(rows *sql.Rows) error {
		var l LiveLease
		var expires sql.NullTime
		if err := rows.Scan(&l.RunID, &l.TaskID, &l.Capability, &l.Resource,
			&l.MintedBy, &l.IssuedAt, &expires); err != nil {
			return err
		}
		l.ExpiresAt = timePtr(expires)
		out = append(out, l)
		return nil
	})
	return out, err
}

// State reads a task's state from the view. There is no column to read it
// from, which is the point.
func (s *Store) State(ctx context.Context, taskID string) (State, error) {
	var st string
	err := s.db.QueryRowContext(ctx,
		"SELECT `state` FROM `task_state` WHERE `task_id` = ?", taskID).Scan(&st)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return State(st), err
}

// DependencyNeverSatisfiable reports whether a LinkDependsOn naming this
// task could ever come unstuck: true for a task that by design never
// completes (NeverCompletes) and has not been closed either.
//
// Closed is the whole of what satisfies a blocking link -- the
// task_blocked view counts a target whose closed_at is null, and IsBlocked
// reads the same fact in Go -- which is why the state matters and no
// other state does. A never-ending task somebody has closed is not this:
// it is a dependency already met. Only the live one is a link with
// nothing on the other end of it, and that is the whole of grain/task-487:
// eight tasks filed by the director naming the director's own session
// task sat queued and undispatchable for five hours, because nothing was
// ever going to finish that session.
//
// False for an id that names no task at all. That is a different
// failure, and each filing path already answers it its own way --
// ui.Client.dependsOnLinks refuses it outright, and
// orchestrator.proposedDependencies drops it with a note.
func (s *Store) DependencyNeverSatisfiable(ctx context.Context, id string) (bool, error) {
	t, err := s.GetTask(ctx, id)
	if err != nil || t == nil {
		return false, err
	}
	if !NeverCompletes(*t) {
		return false, nil
	}
	state, err := s.State(ctx, id)
	if err != nil {
		return false, err
	}
	return state != StateClosed, nil
}

// Ready is every task dispatchable right now: approved, not running, with
// no open blocker -- in dispatch order, which is the backlog's own order
// (bwsalmon/agents#476): ascending OrderKey, the same order ListTasks
// hands a UI or CLI and the same order that list is drawn in, ties broken
// on task ID.
//
// That is the whole rule, with no carve-out for any kind of task. A fix
// task the merge queue filed (Origin.Reason == ReasonFix) used to sort
// ahead of everything else here regardless of where the backlog put it --
// bwsalmon/agents#389's "a queue head's repair must not wait behind
// unrelated new work", since the longer it waits the more likely
// something else lands on the branch it targets and the fix has to be
// refiled rather than simply merged. That priority is a position now
// rather than a sort: the task being repaired is its repo's queue head,
// which showQueueAtFrontOfBacklog has already moved to the front of the
// backlog (and a fix task, back when the queue filed those, was filed at
// the very head with OrderKeyForNewTask's atFront), where a human can
// see it sitting first, see why the queue behind it is waiting, and drag
// it elsewhere if they disagree. A rule that lived only in this ORDER BY
// could do none of that -- the list said one thing and the dispatcher did
// another -- and two orderings that have to agree but are computed in
// different places are the kind that quietly stop agreeing.
func (s *Store) Ready(ctx context.Context) ([]string, error) {
	var out []string
	err := each(ctx, s.db,
		"SELECT `r`.`task_id` FROM `task_ready` AS `r` "+
			"JOIN `task` AS `t` ON `t`.`id` = `r`.`task_id` "+
			"ORDER BY `t`.`order_key`, `r`.`task_id`",
		nil,
		func(rows *sql.Rows) error {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			out = append(out, id)
			return nil
		})
	return out, err
}

// RunNow is every task somebody has asked to be started now
// (Observation.RunNowAt, ui.Client.RunNow), oldest request first -- the
// list dispatch.Cycle works through before it looks at Ready at all.
//
// It deliberately shares almost nothing with Ready. Ready is the queue's
// own answer to "what may start", and a run-now request is exactly the
// operator saying they do not want that answer: the task need not be
// approved, need not be queued, may have completed with a pull request
// already open, and may be sitting behind a blocker. None of that is
// consulted here.
//
// Two things still are, because neither is the state machine's opinion.
// A closed task is left out: its run would be cancelled on the spot
// (orchestrator.runOne re-reads the closure) and nothing downstream
// would keep its work, so reopening it first is the only order that
// does anything. And a task with a live run is left out because one
// live run per task is a store invariant (schema.go's
// task_run_open_task index), not a policy -- the request stays
// outstanding and the next cycle after that run ends honours it.
// It carries each task's kind with it, unlike Ready -- which leaves that
// to ReadyMergers and ReadyPriorityDispatch, two more queries over the same
// view. Those narrow task_ready, and a run-now task is precisely one
// task_ready may say nothing about, so the two predicates are read here
// as columns instead. The SQL is the same constant either way, so what
// this says about a task and what StartRun's own in-transaction check
// says about it cannot drift.
func (s *Store) RunNow(ctx context.Context) ([]RunNowRequest, error) {
	var out []RunNowRequest
	err := each(ctx, s.db,
		"SELECT `o`.`task_id`, `o`.`run_now_at`, "+mergerTaskSQL+", "+priorityDispatchTaskSQL+" "+
			"FROM `task_observation` AS `o` "+
			"JOIN `task` AS `t` ON `t`.`id` = `o`.`task_id` "+
			"LEFT JOIN `task_run` AS `r` "+
			"ON `r`.`task_id` = `o`.`task_id` AND `r`.`finished_at` IS NULL "+
			"WHERE `o`.`run_now_at` IS NOT NULL AND `o`.`closed_at` IS NULL "+
			"AND `r`.`id` IS NULL "+
			"AND NOT EXISTS (SELECT 1 FROM `task_pause` AS `p` WHERE `p`.`task_id` = `o`.`task_id`) "+
			"ORDER BY `o`.`run_now_at`, `o`.`task_id`",
		append([]any{string(ReasonFix)}, priorityDispatchArgs()...),
		func(rows *sql.Rows) error {
			var req RunNowRequest
			if err := rows.Scan(&req.TaskID, &req.At, &req.Merger, &req.Priority); err != nil {
				return err
			}
			out = append(out, req)
			return nil
		})
	return out, err
}

// RunNowRequest is one outstanding run-now request: the task to start,
// and which half of Limits a run of it spends -- the same two identities
// ReadyMergers and ReadyPriorityDispatch answer for a ready task.
type RunNowRequest struct {
	TaskID string
	// At is when the request was made -- for a request a run gave back
	// (settleRunNow), when it was first made, before that run.
	At       time.Time
	Merger   bool
	Priority bool
}

// ReadyTarget is one dispatchable task and the repo a run of it would be
// cloned from and pushed to -- ReadyTargets' row.
type ReadyTarget struct {
	TaskID string
	// Target is nil for a task that names no repo at all (Task.Target's
	// own nil -- a NoRepo task), which is a run with nothing to clone
	// rather than a run with an unknown repo to clone.
	Target *RepoRef
}

// ReadyTargets is Ready with each task's target beside it, in the same
// order -- what a caller that has to judge the whole ready list against
// something repo-shaped reads instead of the list plus a GetTask per
// entry.
//
// Its caller is orchestrator.reconcileTargetRepos, which asks
// Config.TargetRepos about every dispatchable task once per cycle, on
// every cycle, for as long as a deployment restricts its repos at all.
// A row per ready task is what that costs; a GetTask per ready task
// would have made an ordinary tick's cost the size of the queue, on a
// question two columns answer.
func (s *Store) ReadyTargets(ctx context.Context) ([]ReadyTarget, error) {
	var out []ReadyTarget
	err := each(ctx, s.db,
		"SELECT `r`.`task_id`, `t`.`target_owner`, `t`.`target_name` FROM `task_ready` AS `r` "+
			"JOIN `task` AS `t` ON `t`.`id` = `r`.`task_id` "+
			"ORDER BY `t`.`order_key`, `r`.`task_id`",
		nil,
		func(rows *sql.Rows) error {
			var row ReadyTarget
			var owner, name sql.NullString
			if err := rows.Scan(&row.TaskID, &owner, &name); err != nil {
				return err
			}
			// An empty owner is the same nothing a NULL one is: that is
			// how a task with no repo is stored, and how GitScope already
			// reads one back.
			if owner.Valid && owner.String != "" {
				target := RepoFromStored(owner.String, name.String)
				row.Target = &target
			}
			out = append(out, row)
			return nil
		})
	return out, err
}

// IsReady is Ready asked about one task: whether taskID is dispatchable
// at the moment of the call, by exactly the rule the list itself applies
// (the task_ready view).
//
// Ready answers "what could start", which is a list, and a caller
// walking that list spends real time on each entry -- reading a streak,
// starting a run. IsReady is for the look taken at the end of that walk,
// immediately before a run is recorded: a list read a moment ago is a
// snapshot, and the one thing most likely to have invalidated it is a
// run of that very task ending in the meantime. dispatch.Cycle's own doc
// comment has the window; this is the cheapest honest way to close it,
// one row rather than the whole view re-read per dispatch.
func (s *Store) IsReady(ctx context.Context, taskID string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx,
		"SELECT 1 FROM `task_ready` WHERE `task_id` = ?", taskID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// mergerTaskSQL is the one definition of "a run of this task spends the
// merge queue's reserved capacity", written once and used by all three
// places that have to decide it: ReadyMergers, liveRunCounts and
// taskIsMerger. It expects `t` bound to the task row and `o` to its
// observation (LEFT JOIN -- a task with no observation yet is not being
// repaired), and takes one parameter, ReasonFix.
//
// Two arms because a repair has two shapes now. A task the merge queue
// filed to repair somebody else's branch is a merger by its reason, which
// is what every fix task was before this; and a task the queue has sent
// back to repair its own branch is a merger while that repair is in
// flight (Observation.MergeQueueRepairAt with no completed_at), which is
// what a repair is now. Both are the same claim on capacity kept back so
// that a repair does not have to wait out whatever else is running.
const mergerTaskSQL = "(`t`.`origin_reason` = ? OR (" +
	"`o`.`merge_queue_repair_at` IS NOT NULL AND `o`.`completed_at` IS NULL))"

// directorTaskSQL is the durable identity of console-session tasks --
// every configured session, not only the director. It is half of
// priorityDispatchTaskSQL, which is what decides admission; nothing asks
// it on its own. All three predicates are written by
// orchestrator.fileConsoleSession; requiring them together means neither
// a title nor an editable Interactive bit or tag can turn ordinary work
// into deployment control-plane capacity. It expects `t` bound to task
// and takes ReasonConsoleSession followed by the console-session tag pattern.
const directorTaskSQL = "(`t`.`origin_reason` = ? AND `t`.`interactive` = 1 AND EXISTS (" +
	"SELECT 1 FROM `task_tag` AS `dt` WHERE `dt`.`task_id` = `t`.`id` AND `dt`.`tag` LIKE ?))"

// ReadyMergers is Ready narrowed to the tasks whose runs are mergers
// (mergerTaskSQL, OriginReason.Merger) -- and so bounded by
// Limits.Mergers on top of Limits.Workers rather than by the worker
// ceiling alone.
//
// It is a second, narrower query rather than a kind returned alongside
// each entry of Ready because that is all its caller needs: dispatch.
// Cycle walks Ready in Ready's order and asks of each candidate only
// "does this one spend merger rather than worker capacity", which
// membership of this set answers. ReadyPriorityDispatch holds the
// separate priority identity. Ready keeps its own shape, and the order the two agree on
// stays the one order task_ready and the backlog already define -- there
// is no second ordering here to disagree with it.
func (s *Store) ReadyMergers(ctx context.Context) ([]string, error) {
	var out []string
	err := each(ctx, s.db,
		"SELECT `r`.`task_id` FROM `task_ready` AS `r` "+
			"JOIN `task` AS `t` ON `t`.`id` = `r`.`task_id` "+
			"LEFT JOIN `task_observation` AS `o` ON `o`.`task_id` = `r`.`task_id` "+
			"WHERE "+mergerTaskSQL+" "+
			"ORDER BY `t`.`order_key`, `r`.`task_id`",
		[]any{string(ReasonFix)},
		func(rows *sql.Rows) error {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			out = append(out, id)
			return nil
		})
	return out, err
}

// ReadyPriorityDispatch is Ready narrowed to the tasks with priority
// dispatch (priorityDispatchTaskSQL, Task.HasPriorityDispatch): every
// console session -- the director's and every other configured
// session's, on any team, since directorTaskSQL matches the
// console-session tag prefix rather than one session id -- and every
// task holding PriorityDispatchCapability. A run of any of them is
// admitted outside every count (Limits), so none of them queues behind
// the ordinary backlog.
func (s *Store) ReadyPriorityDispatch(ctx context.Context) ([]string, error) {
	var out []string
	err := each(ctx, s.db,
		"SELECT `r`.`task_id` FROM `task_ready` AS `r` "+
			"JOIN `task` AS `t` ON `t`.`id` = `r`.`task_id` "+
			"WHERE "+priorityDispatchTaskSQL+" "+
			"ORDER BY `t`.`order_key`, `r`.`task_id`",
		priorityDispatchArgs(),
		func(rows *sql.Rows) error {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			out = append(out, id)
			return nil
		})
	return out, err
}

// orderKeySpacing is the gap Store.OrderKeyForNewTask and a rebalance
// (rebalanceOrderKeys) leave between adjacent tasks' OrderKey. It only
// has to be wide enough that ordinary use doesn't need a rebalance often
// -- Store.Reorder's own splitOrderKeys still narrows the gap between two
// neighbours every time something is dropped between them, and
// rebalanceOrderKeys is what restores it once minOrderKeyGap says that
// gap is getting too fine to split again.
const orderKeySpacing = 1 << 20

// minOrderKeyGap is the smallest gap Store.Reorder will still split
// rather than rebalance first. float64 has roughly 15-17 significant
// decimal digits; orderKeySpacing's 2^20 leaves this many orders of
// magnitude of headroom below it before two distinct float64 values
// would stop being representably distinct, which is the actual failure
// this bounds against -- not a UX judgement about how fine a manual
// reorder is allowed to get.
const minOrderKeyGap = 1e-6

// OrderKeyForNewTask returns the OrderKey a newly filed task should take:
// one orderKeySpacing step past whichever extreme of the backlog
// model.Config.NewestFirst currently asks new work to join. atFront asks
// for the low end -- Ready dispatches ascending OrderKey, so a task
// placed there runs before everything already queued, which is what
// NewestFirst true means. atFront false (NewestFirst's own default) is
// the opposite end: last in line, behind everything already queued, the
// FIFO backlog grain has always defaulted to. An empty task table (no
// extreme to step past) returns 0, same as OrderKey's own zero value.
func (s *Store) OrderKeyForNewTask(ctx context.Context, atFront bool) (float64, error) {
	q := "SELECT MAX(`order_key`) FROM `task`"
	if atFront {
		q = "SELECT MIN(`order_key`) FROM `task`"
	}
	var extreme sql.NullFloat64
	if err := s.db.QueryRowContext(ctx, q).Scan(&extreme); err != nil {
		return 0, fmt.Errorf("computing a new task's order key: %w", err)
	}
	if !extreme.Valid {
		return 0, nil
	}
	if atFront {
		return extreme.Float64 - orderKeySpacing, nil
	}
	return extreme.Float64 + orderKeySpacing, nil
}

// Reorder moves ids to sit between whatever afterID and beforeID
// currently name -- a drag-and-drop move (bwsalmon/agents#476), against
// the same OrderKey column ListTasks and Ready both already read, so a
// move here is immediately visible to both. Either bound may be nil: no
// afterID means ids become the new minimum -- dropped at the head of a
// list, "just before the following job" -- and no beforeID means they
// become the new maximum. Both nil is only reachable by dropping into an
// empty list, which cannot happen through the UI (there would be nothing
// to drop onto) but is handled the same as any other unbounded case
// rather than rejected. ids keep their relative order among themselves,
// so dragging a multi-selection reorders it as a block rather than by id.
//
// A neighbour named by afterID or beforeID that does not exist is an
// error: both are read fresh from the store inside this call's own
// transaction, so this can only mean the caller's own view was already
// stale (the task was closed or reordered out from under it) between
// when it computed the request and when this ran. So is an id among ids
// that does not exist, for the same reason.
//
// ids is re-sorted by each task's own current OrderKey before anything is
// written, rather than trusted to already be in that order -- a
// multi-select drag's relative order is a property of the backlog these
// ids already had, not an incidental fact about Set iteration order or
// click order a caller would otherwise have to get right itself.
func (s *Store) Reorder(ctx context.Context, ids []string, afterID, beforeID *string) error {
	if len(ids) == 0 {
		return nil
	}
	return s.write(ctx, "reorder tasks", func(tx *sql.Tx) error {
		ordered, _, err := sortByOrderKey(ctx, tx, ids)
		if err != nil {
			return err
		}
		lower, upper, err := orderKeyBounds(ctx, tx, afterID, beforeID)
		if err != nil {
			return err
		}
		if !orderKeysFitBetween(lower, upper, len(ordered)) {
			if err := rebalanceOrderKeys(ctx, tx); err != nil {
				return err
			}
			if lower, upper, err = orderKeyBounds(ctx, tx, afterID, beforeID); err != nil {
				return err
			}
		}
		for i, key := range splitOrderKeys(lower, upper, len(ordered)) {
			if _, err := tx.ExecContext(ctx,
				"UPDATE `task` SET `order_key` = ? WHERE `id` = ?", key, ordered[i]); err != nil {
				return err
			}
		}
		return nil
	})
}

// MoveToFrontOfBacklog places ids at the front of the backlog: ahead of
// every other task, and behind nothing but the merge tasks
// (Origin.Reason == ReasonFix) already scheduled at the very head of it.
// It places them in the order in which they appear in ids, so this is a move
// a human could have made by hand rather than a second ordering rule layered
// on top of theirs.
//
// It is the merge queue making its own order visible
// (orchestrator.SyncPullRequests): a pull request waiting to land is the
// work closest to done, and which of them lands next used to be a
// comparison the queue made inside one cycle and never wrote down.
// Putting them at the front, in the order they will land, lets a list
// answer "what is grain about to finish" without anyone opening a task --
// and, because the queue reads its own head back off this same order
// (orchestrator.queueOrder), dragging one of those tasks above another
// really does change which merges first. An order a human can see and an
// order a human can set are the same fact here, deliberately.
//
// Nothing is written when ids already sit there in that order, which is
// what lets a reconciler call this every cycle. A task dragged out of the
// block altogether does come back to it next cycle -- while it is in the
// queue its position belongs to the queue -- but it comes back where the
// drag left it relative to the others, so "merge this one last" still
// lands.
//
// An id naming no task is an error, the same as Reorder's own and for the
// same reason: every key here is read inside this call's transaction, so
// missing one means the caller's view was already stale.
func (s *Store) MoveToFrontOfBacklog(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	// Read-only pre-check: test whether the tasks are already in front of the backlog
	// without taking an exclusive write lock. Since the reconciler calls this on every cycle
	// and the order already holds on almost every cycle, this avoids serializing reconciler ticks.
	// Asked of ids in the order given, as the write below is: sorting them
	// by their current keys first would find any order "already" held.
	_, keys, err := sortByOrderKey(ctx, s.db, ids)
	if err != nil {
		return err
	}
	lower, upper, err := frontOfBacklogBounds(ctx, s.db, ids)
	if err != nil {
		return err
	}
	if alreadyAtFrontOfBacklog(ids, keys, lower, upper) {
		return nil
	}

	return s.write(ctx, "move tasks to the front of the backlog", func(tx *sql.Tx) error {
		keys := make(map[string]float64, len(ids))
		for _, id := range ids {
			k, err := orderKeyOf(ctx, tx, id)
			if err != nil {
				return err
			}
			keys[id] = k
		}
		lower, upper, err := frontOfBacklogBounds(ctx, tx, ids)
		if err != nil {
			return err
		}
		if alreadyAtFrontOfBacklog(ids, keys, lower, upper) {
			return nil
		}
		if !orderKeysFitBetween(lower, upper, len(ids)) {
			if err := rebalanceOrderKeys(ctx, tx); err != nil {
				return err
			}
			if lower, upper, err = frontOfBacklogBounds(ctx, tx, ids); err != nil {
				return err
			}
		}
		for i, key := range splitOrderKeys(lower, upper, len(ids)) {
			if _, err := tx.ExecContext(ctx,
				"UPDATE `task` SET `order_key` = ? WHERE `id` = ?", key, ids[i]); err != nil {
				return err
			}
		}
		return nil
	})
}

// frontOfBacklogBounds is the interval MoveToFrontOfBacklog moves ids
// into: below the first task of the ordinary backlog, above whatever
// merge tasks are already scheduled at the very head of it. Either side
// is nil when there is nothing on it -- a deployment whose whole backlog
// is the merge queue, or one with no fix task filed -- which
// splitOrderKeys reads as "step away from the other side" the same way
// Reorder's own unbounded drops do.
//
// The head is decided by position, not by state: only a fix task that
// actually sits ahead of the whole ordinary backlog bounds the block from
// below. One dragged down into the middle of the backlog since, or filed
// before fix tasks had a place of their own and left at OrderKey's zero
// value, is just another task there -- pinning the merge queue behind it
// would put the block somewhere nobody is looking, which is the opposite
// of what moving it to the front is for.
func frontOfBacklogBounds(ctx context.Context, tx querier, ids []string) (lower, upper *float64, err error) {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids)+2)
	args = append(args, string(ReasonFix))
	for _, id := range ids {
		args = append(args, id)
	}

	var ordinary sql.NullFloat64
	if err := tx.QueryRowContext(ctx,
		"SELECT MIN(`order_key`) FROM `task` "+
			"WHERE `origin_reason` <> ? AND `id` NOT IN ("+placeholders+")",
		args...).Scan(&ordinary); err != nil {
		return nil, nil, fmt.Errorf("reading the first task of the backlog: %w", err)
	}

	q := "SELECT MAX(`order_key`) FROM `task` " +
		"WHERE `origin_reason` = ? AND `id` NOT IN (" + placeholders + ")"
	if ordinary.Valid {
		q += " AND `order_key` < ?"
		args = append(args, ordinary.Float64)
	}
	var head sql.NullFloat64
	if err := tx.QueryRowContext(ctx, q, args...).Scan(&head); err != nil {
		return nil, nil, fmt.Errorf("reading the merge tasks at the head of the backlog: %w", err)
	}

	if head.Valid {
		lower = &head.Float64
	}
	if ordinary.Valid {
		upper = &ordinary.Float64
	}
	return lower, upper, nil
}

// alreadyAtFrontOfBacklog reports whether ordered already holds distinct,
// ascending OrderKey values strictly inside (lower, upper) -- exactly what
// MoveToFrontOfBacklog would otherwise write, so writing it would move
// nothing. It is asked before every one of those writes because the
// reconciler calling it has nothing to do on almost every cycle, and a
// write that changes no order still narrows the gaps the next one has to
// split.
func alreadyAtFrontOfBacklog(ordered []string, keys map[string]float64, lower, upper *float64) bool {
	first, last := keys[ordered[0]], keys[ordered[len(ordered)-1]]
	if lower != nil && first <= *lower {
		return false
	}
	if upper != nil && last >= *upper {
		return false
	}
	for i := 1; i < len(ordered); i++ {
		if keys[ordered[i]] <= keys[ordered[i-1]] {
			return false
		}
	}
	return true
}

// sortByOrderKey returns ids sorted ascending by each task's current
// OrderKey, alongside the keys it read to do it -- Reorder's own "the
// block keeps its existing relative order" guarantee, and the same order
// MoveToFrontOfBacklog carries to the front of the backlog.
func sortByOrderKey(ctx context.Context, tx querier, ids []string) ([]string, map[string]float64, error) {
	keys := make(map[string]float64, len(ids))
	for _, id := range ids {
		k, err := orderKeyOf(ctx, tx, id)
		if err != nil {
			return nil, nil, err
		}
		keys[id] = k
	}
	ordered := append([]string(nil), ids...)
	sort.SliceStable(ordered, func(i, j int) bool { return keys[ordered[i]] < keys[ordered[j]] })
	return ordered, keys, nil
}

// orderKeyBounds resolves Reorder's afterID/beforeID to the OrderKey
// values already in the store -- read inside Reorder's own transaction,
// never trusted from an earlier read, the same "re-read, never pin"
// discipline IsBlocked's own doc comment argues for.
func orderKeyBounds(ctx context.Context, tx querier, afterID, beforeID *string) (lower, upper *float64, err error) {
	if afterID != nil {
		k, err := orderKeyOf(ctx, tx, *afterID)
		if err != nil {
			return nil, nil, err
		}
		lower = &k
	}
	if beforeID != nil {
		k, err := orderKeyOf(ctx, tx, *beforeID)
		if err != nil {
			return nil, nil, err
		}
		upper = &k
	}
	return lower, upper, nil
}

func orderKeyOf(ctx context.Context, tx querier, taskID string) (float64, error) {
	var k float64
	err := tx.QueryRowContext(ctx, "SELECT `order_key` FROM `task` WHERE `id` = ?", taskID).Scan(&k)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("reordering: no such task %s", taskID)
	}
	return k, err
}

// orderKeysFitBetween reports whether n new, distinct OrderKey values can
// still be split out of (lower, upper) without any pair closer than
// minOrderKeyGap -- Reorder's own signal that a rebalance has to run
// before it can compute keys, not after. An unbounded side (either nil)
// always fits: Reorder only ever narrows a gap by splitting between two
// existing neighbours, so the only way to run out of room is repeated
// splits of the same bounded interval.
func orderKeysFitBetween(lower, upper *float64, n int) bool {
	if lower == nil || upper == nil {
		return true
	}
	return (*upper-*lower)/float64(n+1) >= minOrderKeyGap
}

// splitOrderKeys computes n OrderKey values strictly between lower and
// upper (whichever are non-nil), evenly spaced and in ascending order --
// Reorder's own ids keep this order, which is what keeps a multi-select
// drag's relative order intact.
func splitOrderKeys(lower, upper *float64, n int) []float64 {
	keys := make([]float64, n)
	switch {
	case lower != nil && upper != nil:
		step := (*upper - *lower) / float64(n+1)
		for i := range keys {
			keys[i] = *lower + step*float64(i+1)
		}
	case lower != nil:
		for i := range keys {
			keys[i] = *lower + orderKeySpacing*float64(i+1)
		}
	case upper != nil:
		for i := range keys {
			keys[i] = *upper - orderKeySpacing*float64(n-i)
		}
	default:
		for i := range keys {
			keys[i] = orderKeySpacing * float64(i+1)
		}
	}
	return keys
}

// rebalanceOrderKeys renumbers every task's OrderKey, ascending and
// spaced by orderKeySpacing, without changing their relative order --
// Reorder's own backstop against minOrderKeyGap, restoring room to split
// between two neighbours that repeated drops have crowded together. It
// runs inside Reorder's own transaction, so the renumbering and the move
// that triggered it land together or not at all.
func rebalanceOrderKeys(ctx context.Context, tx *sql.Tx) error {
	var ids []string
	if err := each(ctx, tx, "SELECT `id` FROM `task` ORDER BY `order_key`, `id`", nil,
		func(rows *sql.Rows) error {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
			return nil
		}); err != nil {
		return err
	}
	for i, id := range ids {
		if _, err := tx.ExecContext(ctx,
			"UPDATE `task` SET `order_key` = ? WHERE `id` = ?",
			orderKeySpacing*float64(i+1), id); err != nil {
			return err
		}
	}
	return nil
}

// LiveRunCount is how many runs are currently in flight -- rows with no
// `finished_at`. A dispatch loop reads this, rather than remembering what
// it handed out last cycle, for the same reason IsBlocked re-reads closed
// dependencies: a run can finish between cycles with nothing about the
// loop's own state changing, and the headroom that frees must show up the
// moment it is asked about.
//
// This is the count on its own because a count is all a caller deciding
// what to dispatch needs now. It replaced OccupiedSlots, which returned
// the identifier of every slot holding a live run, back when what a cycle
// needed was the difference between a fixed pool and the part of it in
// use. There is no pool to difference against any more: a sandbox is
// created for a run and destroyed with it, so the only question left is
// how many are in flight against model.Limits.
//
// It is deliberately not what StartRun enforces the limit with -- see
// that method's own doc comment on why the check has to happen inside the
// insert's transaction rather than against a count read beforehand.
func (s *Store) LiveRunCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM `task_run` WHERE `finished_at` IS NULL").Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("counting live runs: %w", err)
	}
	return n, nil
}

// LiveRunCounts is LiveRunCount split the way Limits is: the same live
// rows, counted as mergers (a merge-queue repair), Directors (the
// canonical console session), and workers (everything else). Each is
// bounded differently, so a caller deciding what to dispatch has to know
// which capacity is occupied.
//
// Everything LiveRunCount's own doc comment says about re-reading rather
// than remembering applies here unchanged; this is that count with the
// one distinction the limits draw, not a second notion of what is live.
func (s *Store) LiveRunCounts(ctx context.Context) (RunCounts, error) {
	return liveRunCounts(ctx, s.db)
}

// liveRunCounts is LiveRunCounts against either the store's own handle or
// one transaction -- StartRun needs exactly this count inside the same
// transaction as its insert, which is the only way the limit is enforced
// at all rather than merely checked.
//
// LEFT JOIN, not JOIN: a live run whose task row has somehow gone still
// occupies a sandbox and still has to be counted against the total. It
// counts as a worker, which is the same "don't spend the merge queue's
// reserved capacity on a task nobody can see" reading taskIsMerger takes.
func liveRunCounts(ctx context.Context, q querier) (RunCounts, error) {
	var c RunCounts
	var mergers, directors, total int
	// A merger with priority dispatch is counted once, as priority:
	// StartRun admitted it outside the counts, so it holds none of the
	// merge queue's capacity either.
	args := append([]any{string(ReasonFix)}, priorityDispatchArgs()...)
	args = append(args, priorityDispatchArgs()...)
	if err := q.QueryRowContext(ctx,
		"SELECT COALESCE(SUM(CASE WHEN "+mergerTaskSQL+" AND NOT "+priorityDispatchTaskSQL+" THEN 1 ELSE 0 END), 0), "+
			"COALESCE(SUM(CASE WHEN "+priorityDispatchTaskSQL+" THEN 1 ELSE 0 END), 0), COUNT(*) "+
			"FROM `task_run` AS `r` LEFT JOIN `task` AS `t` ON `t`.`id` = `r`.`task_id` "+
			"LEFT JOIN `task_observation` AS `o` ON `o`.`task_id` = `r`.`task_id` "+
			"WHERE `r`.`finished_at` IS NULL",
		args...).Scan(&mergers, &directors, &total); err != nil {
		return RunCounts{}, fmt.Errorf("counting live runs: %w", err)
	}
	c.Mergers, c.Directors, c.Workers = mergers, directors, total-mergers-directors
	return c, nil
}

// LiveRuns is every task_run row with no `finished_at` -- the same rows
// LiveRunCount counts, in full, for a caller (orchestrator.
// RecoverOrphanedRuns) that needs to know which task and run each one
// belongs to rather than just how many there are. A daemon calls
// this exactly once, at startup, before it has driven any run of its
// own: at that point every row here can only be left over from a
// process that is no longer around to finish it -- see that func's own
// doc comment for why a run still legitimately in flight is never
// mistaken for one of these.
//
// AgentStartedAt is read as well, because it is where a run's own clock
// started (orchestrator.RunDispatch): a daemon adopting the run carries
// its deadline from there rather than from StartedAt, which would charge
// the run its setup a second time (orchestrator.ResumeAdoptedRun).
func (s *Store) LiveRuns(ctx context.Context) ([]Run, error) {
	var out []Run
	err := each(ctx, s.db,
		"SELECT `id`,`task_id`,`sandbox`,`attempt`,`started_at`,`agent_started_at` "+
			"FROM `task_run` WHERE `finished_at` IS NULL ORDER BY `id`", nil,
		func(rows *sql.Rows) error {
			var r Run
			var agentStarted sql.NullTime
			if err := rows.Scan(&r.ID, &r.TaskID, &r.Sandbox, &r.Attempt, &r.StartedAt, &agentStarted); err != nil {
				return err
			}
			r.AgentStartedAt = timePtr(agentStarted)
			out = append(out, r)
			return nil
		})
	return out, err
}

// RunFinished reports whether runID names a task_run row that has
// finished, and whether it names one at all.
func (s *Store) RunFinished(ctx context.Context, runID string) (finished, found bool, err error) {
	var at sql.NullTime
	err = s.db.QueryRowContext(ctx,
		"SELECT `finished_at` FROM `task_run` WHERE `id` = ?", runID).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	return at.Valid, true, nil
}

// AgentRunsAwaitingCIRun returns the task_run ids of every live agent run
// whose own task is waiting on ciRunID -- the CI run has not finished,
// and this task's own CIStatus still names it (task_observation.ci_run_id).
//
// This is the one query ci.SharedGate's Stuck hook needs and cannot do
// without: a gate charges an agent run's sandbox against model.Ledger
// under (PoolAgent, this run's own id, LiveRuns' own r.ID), and the
// question Stuck is asking -- "is this agent sandbox the reason a task
// is holding a CI job back, and is that task waiting for this very job
// to answer it" -- is exactly this join. A task's own CIStatus is
// cleared the instant its run concludes (CIStatus.FinishedAt is set),
// so a run this reads back for is still genuinely unanswered, not one
// whose verdict has arrived and not yet reached the task row.
func (s *Store) AgentRunsAwaitingCIRun(ctx context.Context, ciRunID string) ([]string, error) {
	var out []string
	err := each(ctx, s.db,
		"SELECT `task_run`.`id` FROM `task_run` "+
			"JOIN `task_observation` ON `task_observation`.`task_id` = `task_run`.`task_id` "+
			"WHERE `task_run`.`finished_at` IS NULL "+
			"AND `task_observation`.`ci_run_id` = ? AND `task_observation`.`ci_finished_at` IS NULL",
		ciRunID,
		func(rows *sql.Rows) error {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			out = append(out, id)
			return nil
		})
	return out, err
}

// OldestLiveWorkStartedAt returns the earliest start among every live agent
// run and every unconcluded CI run. Both kinds of work can hold external
// capability resources, so a reaper boundary derived from only one can delete
// resources still owned by the other.
//
// false means no work is live. The caller, not the store, decides the grace
// period and what an idle deployment's boundary should be.
func (s *Store) OldestLiveWorkStartedAt(ctx context.Context) (time.Time, bool, error) {
	queries := []struct {
		kind  string
		query string
	}{
		{"agent run", "SELECT `started_at` FROM `task_run` WHERE `finished_at` IS NULL ORDER BY `started_at` ASC LIMIT 1"},
		{"CI run", "SELECT `started_at` FROM `ci_run` WHERE `conclusion` = '' ORDER BY `started_at` ASC LIMIT 1"},
	}
	var oldest time.Time
	var found bool
	for _, q := range queries {
		var started time.Time
		err := s.db.QueryRowContext(ctx, q.query).Scan(&started)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return time.Time{}, false, fmt.Errorf("finding oldest live %s: %w", q.kind, err)
		}
		if !found || started.Before(oldest) {
			oldest, found = started, true
		}
	}
	return oldest.UTC(), found, nil
}

// GitScope is what a sandbox's currently live run may touch through the
// git proxy: the write target of the task it is running, and every repo
// it may fetch but not push. Nil target and an empty slice mean the
// sandbox has no live run right now, which the proxy must treat as
// "touch nothing" -- the same fail-closed default a static allowlist
// gave it, except this one can never drift from what a task actually
// declares, because it isn't a second copy of that declaration.
//
// The task's extra write targets (Task.Writes) come back in that second
// list, alongside its Reads, and that is not a mistake: a run pushes to
// Target and nowhere else, however many repositories its task may
// eventually have work landed in (WriteTarget's own doc comment).
// Work in another one of them happens in the run dispatched for the task
// this one proposes, which has that repository as its *own* Target and
// so gets a push through this same rule. Being able to clone it
// meanwhile is what makes such a proposal worth anything: an agent that
// cannot read the other repository can only guess at what the work there
// would be.
//
// The write target comes off the *run* rather than off the task
// (task_run.target_owner/target_name, pinned by SetRunSandbox), and the
// reads come off the task as they stand this instant. That split is the
// answer to two different questions rather than an inconsistency. A read
// added to a task mid-run is a repo somebody has decided this run may
// look at, and letting it fetch one costs it nothing it was already
// doing. Its target is the repo it cloned an hour ago and has been
// committing to since, so following an edit there does not redirect the
// run -- it strands it, refusing the push it was dispatched to make
// while granting a push to a repo nothing in the sandbox has ever seen.
// A run therefore finishes against the repo it was given, and a repo
// edit reaches the task's next run, which is what the panel offering
// that edit says it does (ui DetailOverlay's Target).
//
// A run with no pinned target at all -- every run recorded before those
// columns existed -- is answered out of its task, as this did for all of
// them before.
func (s *Store) GitScope(ctx context.Context, sandbox string) (target *RepoRef, reads []RepoRef, writes []RepoRef, err error) {
	taskID, pinned, live, err := s.liveRunTarget(ctx, sandbox)
	if err != nil || !live {
		return nil, nil, nil, err
	}

	if pinned.Valid {
		// Pinned, and pinned to nothing where the run was dispatched for
		// a task with no target: an empty owner leaves target nil, which
		// the proxy reads as a sandbox that may push nowhere.
		if pinned.Ref.Owner != "" {
			ref := pinned.Ref
			target = &ref
		}
	} else {
		row := s.db.QueryRowContext(ctx,
			"SELECT `target_owner`, `target_name` FROM `task` WHERE `id` = ?", taskID)
		var tOwner, tName sql.NullString
		if err = row.Scan(&tOwner, &tName); err != nil {
			return nil, nil, nil, fmt.Errorf("reading target of task %s: %w", taskID, err)
		}
		if tOwner.Valid {
			ref := RepoFromStored(tOwner.String, tName.String)
			target = &ref
		}
	}

	err = each(ctx, s.db,
		"SELECT `owner`,`name` FROM `task_read` WHERE `task_id` = ? ORDER BY `owner`,`name`",
		taskID, func(rows *sql.Rows) error {
			var owner, name string
			if err := rows.Scan(&owner, &name); err != nil {
				return err
			}
			r := RepoFromStored(owner, name)
			reads = append(reads, r)
			return nil
		})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("reading reads of task %s: %w", taskID, err)
	}

	err = each(ctx, s.db,
		"SELECT `owner`,`name` FROM `task_write` WHERE `task_id` = ? ORDER BY `owner`,`name`",
		taskID, func(rows *sql.Rows) error {
			var owner, name string
			if err := rows.Scan(&owner, &name); err != nil {
				return err
			}
			r := RepoFromStored(owner, name)
			writes = append(writes, r)
			return nil
		})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("reading write targets of task %s: %w", taskID, err)
	}

	return target, reads, writes, nil
}

// GitPushBranch is the one branch a sandbox's currently live run may
// push: its task's own (Task.BranchName), the branch the dispatch prompt
// told it to push. The git proxy refuses a receive-pack that updates any
// other ref, in any repository the run may push to (gitproxy's
// ModelAuthorizer.AuthorizeRefUpdates). live is false, with a nil error,
// for a sandbox with nothing running on it right now, which the proxy
// reads as a sandbox that may push no branch at all.
func (s *Store) GitPushBranch(ctx context.Context, sandbox string) (branch string, live bool, err error) {
	taskID, live, err := s.liveTaskID(ctx, sandbox)
	if err != nil || !live {
		return "", false, err
	}
	var stored sql.NullString
	err = s.db.QueryRowContext(ctx, "SELECT `branch` FROM `task` WHERE `id` = ?", taskID).Scan(&stored)
	if err != nil {
		return "", false, fmt.Errorf("reading branch of task %s: %w", taskID, err)
	}
	// A review working on the reviewed change's own branch
	// (LinkPushesReviewedBranch) may push it only while it is open: one
	// closed -- superseded by a review of a newer head, or closed with the
	// change -- has no verdict left to give, and its run, still live until
	// the close reaches it, must not move a branch somebody else's pull
	// request is open from.
	var closedInPlace int
	err = s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM `task_link` AS `l` JOIN `task_observation` AS `o` ON `o`.`task_id` = `l`.`task_id` "+
			"WHERE `l`.`task_id` = ? AND `l`.`kind` = ? AND `o`.`closed_at` IS NOT NULL",
		taskID, string(LinkPushesReviewedBranch)).Scan(&closedInPlace)
	if err != nil {
		return "", false, fmt.Errorf("reading whether task %s is a closed review: %w", taskID, err)
	}
	if closedInPlace > 0 {
		return "", false, nil
	}
	return Task{ID: taskID, Branch: stored.String}.BranchName(), true, nil
}

// GitPushFastForwardOnly reports whether a sandbox's currently live run
// may only fast-forward the branch GitPushBranch names, never create,
// delete or force-move it: true for a review working on the reviewed
// change's own branch (LinkPushesReviewedBranch), whose branch is that
// change's and whose pull request is that change's too. false, with a nil
// error, for a sandbox with nothing live.
func (s *Store) GitPushFastForwardOnly(ctx context.Context, sandbox string) (bool, error) {
	taskID, live, err := s.liveTaskID(ctx, sandbox)
	if err != nil || !live {
		return false, err
	}
	var n int
	err = s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM `task_link` WHERE `task_id` = ? AND `kind` = ?",
		taskID, string(LinkPushesReviewedBranch)).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("reading whether task %s works on a reviewed branch: %w", taskID, err)
	}
	return n > 0, nil
}

// GitPushAnyRef reports whether a sandbox's currently live run holds
// GitPushAnyRefCapability, which lifts the one-branch rule GitPushBranch
// feeds: the git proxy then lets it update any ref of a repository it may
// push. false, with a nil error, for a sandbox with nothing live.
func (s *Store) GitPushAnyRef(ctx context.Context, sandbox string) (bool, error) {
	taskID, live, err := s.liveTaskID(ctx, sandbox)
	if err != nil || !live {
		return false, err
	}
	grants, err := grantsOf(ctx, s.db, taskID)
	if err != nil {
		return false, fmt.Errorf("reading grants of task %s: %w", taskID, err)
	}
	for _, g := range grants {
		if g.Capability == GitPushAnyRefCapability {
			return true, nil
		}
	}
	return false, nil
}

// NamespaceScope describes the namespace roots and repositories a sandbox's
// currently live run may read over /fs/<scheme>/<path> and /cli/ on the sandbox proxy.
type NamespaceScope struct {
	TaskID            string
	RunID             string
	TaskRepo          string
	TaskBranch        string
	TaskBase          string
	ConfigRepo        string
	Grants            []string
	MayReadCI         bool
	MayReadAllCI      bool
	AllowedRepos      map[string]bool
	MayReadSrc        bool
	MayReadAllTasks   bool
	MayReadFullConfig bool
	MayReadAllMemory  bool
	MayReadMetrics    bool
	MayReadPipeline   bool
	MayReadHealth     bool
	MayReadGrainLog   bool
	MayReadPlaybooks  bool
	MayReadIssues     bool
	MayReadDB         bool
}

// SandboxNamespaceScope returns the namespace access scope of the currently
// live run on sandbox. live is false, with a nil error, when no run is live on
// sandbox right now.
func (s *Store) SandboxNamespaceScope(ctx context.Context, sandbox string) (scope NamespaceScope, live bool, err error) {
	runID, taskID, _, live, err := s.liveRunIDAndTarget(ctx, sandbox)
	if err != nil || !live {
		return NamespaceScope{}, false, err
	}
	target, reads, writes, err := s.GitScope(ctx, sandbox)
	if err != nil {
		return NamespaceScope{}, false, err
	}
	grants, err := grantsOf(ctx, s.db, taskID)
	if err != nil {
		return NamespaceScope{}, false, fmt.Errorf("reading grants of task %s: %w", taskID, err)
	}

	var storedBranch, storedBase sql.NullString
	_ = s.db.QueryRowContext(ctx, "SELECT `branch`, `base` FROM `task` WHERE `id` = ?", taskID).Scan(&storedBranch, &storedBase)
	taskBranch := Task{ID: taskID, Branch: storedBranch.String}.BranchName()
	taskBase := strings.TrimSpace(storedBase.String)

	allowed := make(map[string]bool)
	addRepo := func(r string) {
		r = strings.TrimSpace(r)
		if r == "" {
			return
		}
		allowed[r] = true
		allowed[strings.ToLower(r)] = true
	}
	var taskRepo string
	if target != nil {
		taskRepo = target.String()
		addRepo(taskRepo)
	}
	for _, r := range reads {
		addRepo(r.String())
	}
	for _, w := range writes {
		addRepo(w.String())
	}

	cfgRepo := ConfigRepo()
	if s.SettingsRepo != nil {
		if sr := s.SettingsRepo(); sr != (RepoRef{}) {
			cfgRepo = sr
		}
	}
	mayReadFullConfig := allowed[strings.ToLower(cfgRepo.String())] || allowed[strings.ToLower(ConfigRepo().String())]

	var grantNames []string
	var (
		mayReadCI        bool
		mayReadAllCI     bool
		mayReadSrc       bool
		mayReadAllTasks  bool
		mayReadAllMemory bool
		mayReadMetrics   bool
		mayReadPipeline  bool
		mayReadHealth    bool
		mayReadGrainLog  bool
		mayReadPlaybooks bool
		mayReadIssues    bool
		mayReadDB        bool
	)
	for _, g := range grants {
		name := strings.TrimSpace(g.Capability)
		grantNames = append(grantNames, name)
		switch name {
		case "grain-ci":
			mayReadCI = true
		case "self-debug":
			mayReadCI = true
			mayReadAllCI = true
			mayReadSrc = true
			// What self-debug opens and no more
			// (docs/design/sandbox-first-agent-surface.md, and the
			// tools pkg/mcp/toolnames.go gates on it): memory,
			// issues and SQL each keep a grant of their own, as their
			// MCP tools do. Health comes with its api:self-debug
			// preset's deployment:read, below.
			mayReadAllTasks = true
			mayReadFullConfig = true
			mayReadMetrics = true
			mayReadPipeline = true
		case "grain-log":
			mayReadGrainLog = true
		case "deployment-health":
			mayReadHealth = true
		case "bootstrap-playbooks":
			mayReadPlaybooks = true
		case "memory":
			mayReadAllMemory = true
		case "read-issues":
			mayReadIssues = true
		case "db-read", "db-write":
			mayReadDB = true
		}
		// An api grant limited to one repository is spelt
		// api:<scope-or-preset>@owner/repo (ParseAPIGrant); a name that
		// does not parse as one grants nothing here, as it grants no
		// route on the agent API either.
		// A scope set may limit ci:read to some repositories of its own
		// (APIScopeSet.Repos) under a grant with no @repo, so where it
		// reaches is the set's to say (reposOf), never the grant's alone.
		if parsed, isAPI, perr := ParseAPIGrant(name); isAPI && perr == nil && slices.Contains(parsed.Scopes, "ci:read") {
			for _, repo := range parsed.reposOf("ci:read") {
				mayReadCI = true
				if repo == "" {
					mayReadAllCI = true
				} else {
					addRepo(repo)
				}
			}
		}
	}
	apiSet := APIGrantsOf(grantNames)
	if apiSet.Has("ci:read") {
		mayReadCI = true
		if apiSet.Allows("ci:read", "") {
			mayReadAllCI = true
		}
	}
	if apiSet.Allows("tasks:read", "") {
		mayReadAllTasks = true
	}
	if apiSet.Allows("settings:read", "") || apiSet.Allows("repos:read", "") {
		mayReadFullConfig = true
	}
	if apiSet.Allows("memory:read", "") {
		mayReadAllMemory = true
	}
	if apiSet.Allows("deployment:read", "") {
		mayReadMetrics = true
		mayReadPipeline = true
		mayReadHealth = true
	}
	if apiSet.Allows("dispatch:read", "") {
		mayReadPipeline = true
	}

	return NamespaceScope{
		TaskID:            taskID,
		RunID:             runID,
		TaskRepo:          taskRepo,
		TaskBranch:        taskBranch,
		TaskBase:          taskBase,
		ConfigRepo:        cfgRepo.String(),
		Grants:            grantNames,
		MayReadCI:         mayReadCI,
		MayReadAllCI:      mayReadAllCI,
		AllowedRepos:      allowed,
		MayReadSrc:        mayReadSrc,
		MayReadAllTasks:   mayReadAllTasks,
		MayReadFullConfig: mayReadFullConfig,
		MayReadAllMemory:  mayReadAllMemory,
		MayReadMetrics:    mayReadMetrics,
		MayReadPipeline:   mayReadPipeline,
		MayReadHealth:     mayReadHealth,
		MayReadGrainLog:   mayReadGrainLog,
		MayReadPlaybooks:  mayReadPlaybooks,
		MayReadIssues:     mayReadIssues,
		MayReadDB:         mayReadDB,
	}, true, nil
}

// GitDeclaredPush is what a sandbox's currently live run may push beyond
// its own branch by declaration (grain/task-2536): the push set of the
// repository-declared template its task was filed from, read from that
// declaration as the declaring repository's default branch has it now.
//
// nil, with a nil error, for a sandbox with nothing live, for a task
// filed from no template, and for one filed from a template that is not
// a repository's declaration -- a settings-owned template carries no
// push set, and neither does a template whose declaration has since been
// withdrawn. The run's own branch cannot change any of it: the push set
// is never read from the run's tree, only from what the declaration
// sync read off the default branch (repo_declaration_push).
func (s *Store) GitDeclaredPush(ctx context.Context, sandbox string) (*DeclaredPush, error) {
	taskID, live, err := s.liveTaskID(ctx, sandbox)
	if err != nil || !live {
		return nil, err
	}
	var templateID string
	err = s.db.QueryRowContext(ctx,
		"SELECT `template_id` FROM `task_filed_template` WHERE `task_id` = ?", taskID).Scan(&templateID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && templateID == "") {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the template of task %s: %w", taskID, err)
	}
	decl, err := s.GetRepoDeclaration(ctx, templateID)
	if err != nil {
		return nil, err
	}
	if decl == nil || decl.Kind != DeclaredTemplate {
		return nil, nil
	}
	return &DeclaredPush{TaskID: taskID, TemplateID: templateID, Declaration: *decl, Push: decl.Push}, nil
}

// ErrNoTeamGitCredential is what GitCredentialOverride and
// TaskGitCredential answer for a task whose resolution ladder ends
// without a credential: no github-credential grant on the task, no
// repository override on its team, and no credential on the team itself.
// There is no deployment-wide fallback past the team (grain/task-1592):
// a team's sandboxes clone and push with the team's own credential or
// not at all, so the git proxy refuses the request with this error's
// message and the dispatcher leaves the task queued (NoTeamGitCredentialError).
var ErrNoTeamGitCredential = errors.New("team has no GitHub credential")

// NoTeamGitCredentialError is ErrNoTeamGitCredential naming the team, so
// whoever reads the refusal knows whose Team settings to open.
type NoTeamGitCredentialError struct {
	Team string
	// Host is the git host the team has no connection for, when the
	// request named one other than the deployment's default -- there is
	// never a fallback to another host's credential (docs/git-hosts.md).
	Host string
}

func (e *NoTeamGitCredentialError) Error() string {
	if e.Host != "" {
		return fmt.Sprintf("the deployment has no GitHub credential for host %s -- add a git connection "+
			"for that host in Settings -> Git (/settings/git)", e.Host)
	}
	return "the deployment has no GitHub credential -- set one in Settings -> Git (/settings/git)"
}

func (e *NoTeamGitCredentialError) Is(target error) bool { return target == ErrNoTeamGitCredential }

// GitCredentialOverride is the named credential a sandbox's currently
// live task clones and pushes owner/repo with, resolving hierarchically:
// 1. Task explicit grant (model.GitCredentialGrant / github-credential:<name>)
// 2. The repository's own credential (RepoConfig.GitCredential)
// 3. The deployment's git connections (Config.GitConnections)
// and stopping there. false, with a nil error, means the sandbox has no
// live run at all -- grain's own use of the proxy rather than a team's
// work -- and the proxy falls back to the deployment's own per-repo
// ladder for that alone. A live run whose ladder ends empty is a
// *NoTeamGitCredentialError: there is no deployment-wide credential a
// team's work falls back to.
func (s *Store) GitCredentialOverride(ctx context.Context, sandbox, owner, repo string) (name string, ok bool, err error) {
	return s.GitCredentialOverrideOn(ctx, sandbox, "", owner, repo)
}

// GitCredentialOverrideOn is GitCredentialOverride for a repository on
// host, empty meaning the deployment's default host: the task's grant,
// then its team's credential for that host (TeamGitCredentialOn).
func (s *Store) GitCredentialOverrideOn(ctx context.Context, sandbox, host, owner, repo string) (name string, ok bool, err error) {
	taskID, live, err := s.liveTaskID(ctx, sandbox)
	if err != nil || !live {
		return "", false, err
	}
	name, err = s.TaskGitCredentialOn(ctx, taskID, host, owner, repo)
	if err != nil {
		return "", false, err
	}
	return name, true, nil
}

// TaskGitCredential is GitCredentialOverride's ladder for one task and
// repository, without a sandbox: the credential the task's runs would
// clone owner/repo with, or a *NoTeamGitCredentialError when the ladder
// ends without one. owner and repo may be empty, for a task with no
// target, in which case the team-repo rung is skipped.
func (s *Store) TaskGitCredential(ctx context.Context, taskID, owner, repo string) (string, error) {
	return s.TaskGitCredentialOn(ctx, taskID, "", owner, repo)
}

// TaskGitCredentialOn is TaskGitCredential for a repository on host,
// empty meaning the deployment's default host. The task's own
// github-credential grant names one credential for the whole task, so it
// answers on every host; the proxy refuses it on any host but its own
// (docs/git-hosts.md). On a single-tenant store (Store.SingleTenant) the
// team rung is always the default team's.
func (s *Store) TaskGitCredentialOn(ctx context.Context, taskID, host, owner, repo string) (string, error) {
	grants, err := grantsOf(ctx, s.db, taskID)
	if err != nil {
		return "", fmt.Errorf("reading grants of task %s: %w", taskID, err)
	}
	if name, ok := gitCredentialOverride(grants); ok {
		return name, nil
	}
	// The task's team chooses nothing: every task resolves through the
	// deployment's git connections (docs/single-tenant-reset.md, stage 1).
	return s.GitConnectionOn(ctx, host, owner, repo)
}

// TeamGitCredential is the team half of TaskGitCredential's ladder for a
// repository on the deployment's default git host: TeamGitCredentialOn
// with no host. It is what a grain CI job holding a repository access it
// declared (repo-read:, repo-write:) goes through the proxy with, since
// such a job has no task to carry a grant of its own (grain/task-1654).
// An empty team is the default team.
func (s *Store) TeamGitCredential(ctx context.Context, team, owner, repo string) (string, error) {
	return s.TeamGitCredentialOn(ctx, team, "", owner, repo)
}

// TeamGitCredentialOn is the credential team clones and pushes owner/repo
// on host with: the team's override for that repository, then its most
// specific git connection on host (MatchTeamGitConnection: repository,
// then owner, then the host's default, with the legacy Team.GitCredential
// as the default host's default), or a *NoTeamGitCredentialError naming
// the host when nothing on that host matches. An empty host is the
// deployment's default (Store.GitDefaultHost). A credential held for any
// other host is never the answer (docs/git-hosts.md).
//
// The per-repository override (RepoConfig.GitCredential) is keyed by
// the repository's host as well as its owner/name (RepoRef.StoredOwner),
// so an override applies on its own host only.
//
// team is no longer read: the answer is the deployment's
// (GitConnectionOn), whichever team asks. The parameter stays until the
// team plumbing that passes it is removed (docs/single-tenant-reset.md).
func (s *Store) TeamGitCredentialOn(ctx context.Context, team, host, owner, repo string) (string, error) {
	return s.GitConnectionOn(ctx, host, owner, repo)
}

// GitConnectionOn is the credential this deployment clones, pushes and
// calls the forge's API for owner/repo on host with: the repository's own
// credential (RepoConfig.GitCredential), then the most specific of the
// deployment's git connections on host (MatchTeamGitConnection:
// repository, then owner, then the host's default), or a
// *NoTeamGitCredentialError naming the host when nothing on that host
// matches. An empty host is the deployment's default (GitDefaultHost). A
// credential held for any other host is never the answer
// (docs/git-hosts.md), and nothing falls back to the
// secrets/github/credentials.json ladder.
//
// The deployment's connections are grain_config's (Config.GitConnections)
// since schema 22, and the per-repository credential repo_config's since
// schema 23 (docs/single-tenant-reset.md, 6-ii-b2 and b3).
func (s *Store) GitConnectionOn(ctx context.Context, host, owner, repo string) (string, error) {
	onDefaultHost := SameGitHost(host, "", s.GitDefaultHost)
	if owner != "" && repo != "" {
		ref := RepoRef{Owner: owner, Name: repo}
		if !onDefaultHost {
			ref.Host, _ = NormalizeGitHost(host)
		}
		cfg, err := s.GetRepoConfig(ctx, ref)
		if err != nil {
			return "", fmt.Errorf("reading repo config for %s/%s: %w", owner, repo, err)
		}
		if cfg != nil && cfg.GitCredential != "" {
			return cfg.GitCredential, nil
		}
	}
	settings, err := s.GetConfig(ctx)
	if err != nil {
		return "", fmt.Errorf("reading the deployment's git connections: %w", err)
	}
	if settings != nil {
		if conn, ok := MatchTeamGitConnection(settings.EffectiveGitConnections(), host, s.GitDefaultHost, owner, repo); ok {
			return conn.Credential, nil
		}
	}
	missing := &NoTeamGitCredentialError{Team: DefaultTeamID}
	if !onDefaultHost {
		missing.Host, _ = NormalizeGitHost(host)
	}
	return "", missing
}

// pinnedTarget is what a run's own target columns say
// (task_run.target_owner/target_name): Valid false for a run with
// nothing pinned onto it, which is every run recorded before those
// columns existed and which a reader answers out of the task instead;
// Valid true with an empty Ref.Owner for a run dispatched for a task
// that had no target, which is pinned to nothing rather than unpinned.
type pinnedTarget struct {
	Valid bool
	Ref   RepoRef
}

// liveTaskID is the task ID of the sandbox's currently live run, if any --
// what GitCredentialOverride needs, which is the half of liveRunTarget's
// answer that has nothing to do with repos.
func (s *Store) liveTaskID(ctx context.Context, sandbox string) (taskID string, live bool, err error) {
	taskID, _, live, err = s.liveRunTarget(ctx, sandbox)
	return taskID, live, err
}

// liveRunTarget is the sandbox's currently live run, as the two things a
// caller resolving one asks about it: whose task it is, and the write
// target it was dispatched against. live is false, with a nil error, for
// a sandbox with nothing running on it right now.
func (s *Store) liveRunTarget(ctx context.Context, sandbox string) (taskID string, target pinnedTarget, live bool, err error) {
	_, taskID, target, live, err = s.liveRunIDAndTarget(ctx, sandbox)
	return taskID, target, live, err
}

func (s *Store) liveRunIDAndTarget(ctx context.Context, sandbox string) (runID, taskID string, target pinnedTarget, live bool, err error) {
	// A run records its sandbox only once one has been acquired for it
	// (SetRunSandbox), so "" is not a name -- it is the absence of one,
	// and matching it against the rows that have not been filled in yet
	// would hand a caller some arbitrary still-provisioning run's task.
	// Refusing it here keeps that out of every reader below at once.
	if sandbox == "" {
		return "", "", pinnedTarget{}, false, nil
	}
	var owner, name sql.NullString
	err = s.db.QueryRowContext(ctx,
		"SELECT `id`, `task_id`, `target_owner`, `target_name` FROM `task_run` "+
			"WHERE `sandbox` = ? AND `finished_at` IS NULL "+
			"ORDER BY `started_at` DESC LIMIT 1", sandbox).Scan(&runID, &taskID, &owner, &name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", pinnedTarget{}, false, nil
	}
	if err != nil {
		return "", "", pinnedTarget{}, false, fmt.Errorf("finding the live run on %s: %w", sandbox, err)
	}
	if owner.Valid {
		target = pinnedTarget{Valid: true, Ref: RepoFromStored(owner.String, name.String)}
	}
	return runID, taskID, target, true, nil
}

// grantsOf is a task's Grants, read straight off task_grant -- shared by
// hydrate (a full Task) and GitCredentialOverride (which needs only
// these, and without ever paying for the rest of the task).
func grantsOf(ctx context.Context, q querier, taskID string) ([]Grant, error) {
	var grants []Grant
	err := each(ctx, q,
		"SELECT `capability`,`via`,`folder` FROM `task_grant` WHERE `task_id` = ? ORDER BY `capability`",
		taskID, func(rows *sql.Rows) error {
			var g Grant
			var via string
			var folder sql.NullString
			if err := rows.Scan(&g.Capability, &via, &folder); err != nil {
				return err
			}
			g.Via, g.Folder = GrantSource(via), ParseFolder(folder.String)
			grants = append(grants, g)
			return nil
		})
	return grants, err
}

// TaskPullRequestLink is one task_link row of kind LinkFixes, belonging to
// a task whose run is over and whose pull request has not finished — a run
// pushed and a PR was opened or found for it, and grain has not yet
// observed that PR merge or close.
type TaskPullRequestLink struct {
	TaskID string
	// PullRequest is the link's own target, a model.PullRequestRef's
	// String() — parse it back with model.ParsePullRequestRef.
	PullRequest string
}

// OpenPullRequestLinks returns every fixes-link on a task whose run has
// finished and which has not closed — what a GitHub-sync component polls
// each cycle to find a PR whose health it should refresh, without needing
// a table of its own: task_link and task_state already carry everything
// this needs, and task_state stops returning either post-run state the
// moment task_observation's closed_at is set, so a closed-out task drops
// out of this list with no extra bookkeeping.
//
// Both post-run states, not only 'completed'. A task nobody has submitted
// reads 'awaiting_submit' (StateAwaitingSubmit) and its pull request is
// every bit as real: somebody may merge or close it on GitHub by hand,
// and that is precisely the observation SyncPullRequests exists to make.
// Narrowing this to 'completed' would leave such a task showing a pull
// request grain had quietly stopped watching, open forever after it had
// already merged.
//
// The third arm is the one task here whose run is not over: one the merge
// queue has sent back to an agent to repair its own branch
// (Observation.MergeQueueRepairAt with no completed_at yet, and no
// closed_at -- orchestrator.requeueForRepair). Such a task reads 'queued'
// or 'running' rather than either post-run state, and dropping it would
// hand its queue position to whatever is behind it and leave the repair
// running with nothing watching the deadline it is running against. It
// keeps its head position instead, exactly as it did when the repair was
// a separate fix task stacked on its branch.
func (s *Store) OpenPullRequestLinks(ctx context.Context) ([]TaskPullRequestLink, error) {
	var out []TaskPullRequestLink
	err := each(ctx, s.db,
		"SELECT `l`.`task_id`, `l`.`target` FROM `task_link` AS `l` "+
			"JOIN `task_state` AS `st` ON `st`.`task_id` = `l`.`task_id` "+
			"LEFT JOIN `task_observation` AS `o` ON `o`.`task_id` = `l`.`task_id` "+
			"WHERE `l`.`kind` = ? AND (`st`.`state` IN (?, ?) OR ("+
			"`o`.`merge_queue_repair_at` IS NOT NULL AND `o`.`completed_at` IS NULL "+
			"AND `o`.`closed_at` IS NULL)) ORDER BY `l`.`task_id`",
		[]any{string(LinkFixes), string(StateCompleted), string(StateAwaitingSubmit)},
		func(rows *sql.Rows) error {
			var l TaskPullRequestLink
			if err := rows.Scan(&l.TaskID, &l.PullRequest); err != nil {
				return err
			}
			out = append(out, l)
			return nil
		})
	return out, err
}

// OpenBlockers is how many unclosed tasks stand in front of this one.
func (s *Store) OpenBlockers(ctx context.Context, taskID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		"SELECT `open_blockers` FROM `task_blocked` WHERE `task_id` = ?", taskID).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return n, err
}

// GetConfig reads the deployment's stored configuration, or nil (with no
// error) if nothing has written one yet -- a fresh database, or one
// whose daemon has never started. See Config's own doc comment on why
// nil, not a zero-value Config, marks that case.
func (s *Store) GetConfig(ctx context.Context) (*Config, error) {
	row := s.db.QueryRowContext(ctx, "SELECT "+configColumns+" FROM `grain_config` WHERE `id` = 1")
	c, err := scanConfig(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &c, nil
}

const configColumns = "`poll_interval_ms`,`max_workers`,`max_mergers`,`max_merges_per_repo`,`merge_batch_size`,`merge_gather_window_ms`,`max_ci_jobs`,`max_agent_turns`," +
	"`github_host`,`github_insecure_http`,`gcp_project`,`gcp_service_account_email`," +
	"`newest_first`,`sandbox_backend`,`sandbox_cpus`,`sandbox_memory_mb`,`sandbox_disk_gb`,`show_closed_by_default`,`default_filters`,`agent_backends`,`default_model`," +
	"`agent_backend_id`,`model_settings`,`git_credential`,`git_connections`," +
	"`approved_by_default`,`auto_merge_by_default`,`default_capabilities`," +
	"`environment_name`,`prompt_extension`,`task_branch_template`,`deployment_id`,`time_zone`," +
	"`upgrade_check_mode`,`upgrade_check_pauses`,`network_access`,`console_sessions`,`sandbox_image`," +
	"`package_cache_enabled`,`package_cache_size_gb`," +
	"`actions_cache_enabled`,`actions_cache_size_gb`," +
	"`ci_run_history`,`ci_step_output_mb`,`ci_step_output_days`,`ci_run_output_kb`," +
	"`ci_cache_mb`,`ci_cache_entries`,`ci_cache_days`," +
	"`ci_artifact_mb`,`ci_artifact_run_mb`,`ci_artifact_file_mb`,`ci_artifact_days`," +
	"`ci_jobs_per_run`,`ci_age_boost_ms`,`ci_reserve_after_ms`," +
	"`max_auto_submit_depth`,`max_auto_submit_fan_out`," +
	"`sandbox_cpu_overcommit`,`sandbox_memory_overcommit`,`sandbox_memory_reserve_mb`," +
	"`sandbox_disk_floor_share`,`sandbox_disk_floor_min_mb`," +
	"`gcp_service_account_count`,`iap_backend_service`,`repair_template_id`," +
	"`password_auth_enabled`,`open_registration`," +
	"`google_oauth_enabled`,`google_oauth_client_id`,`google_oauth_client_secret`,`google_oauth_allowed_domains`,`iap_resource`," +
	"`agent_framework`,`agent_git_name`,`agent_git_email`,`target_repos`," +
	"`k8s_namespace`,`k8s_kubectl`,`k8s_sandbox_image`,`k8s_granule_image`,`k8s_workspace`," +
	"`k8s_git_proxy_host`,`k8s_sandbox_service`,`k8s_service_account`,`k8s_node_selector`," +
	"`k8s_privileged`,`k8s_network_policy`,`k8s_deployment`,`k8s_container`," +
	"`mcp_servers`,`capability_policy`,`cli_run`,`sandbox_overcommit`,`base_ci_red_streak`," +
	"`k8s_priority_class_interactive`,`k8s_priority_class_agent`,`k8s_priority_class_ci`,`sandbox_disk_overcommit`," +
	"`auto_upgrade_channel`,`auto_upgrade_time`,`judgement_model`,`external_agent_api`"

func scanConfig(scan func(...any) error) (Config, error) {
	var c Config
	var pollMS int64
	// The CI gate's two windows, stored in milliseconds for the reason
	// the poll interval above is: a column an operator reads in a diff of
	// grain_config.json should hold a number rather than a string this
	// package has to parse before it can be trusted.
	var ageBoostMS, reserveAfterMS int64
	// The merge queue's gather window, in milliseconds for the same
	// reason (grain/task-360).
	var gatherWindowMS int64
	var defaultCapabilities string
	var networkAccess string
	var consoleSessions string
	var agentBackends string
	var modelSettings, gitConnections string
	var sandboxBackend string
	var targetRepos string
	var k8sNodeSelector string
	var mcpServers string
	var capabilityPolicy string
	var cliRun string
	var sandboxOvercommit string
	// The sign-in settings (password_auth_enabled, open_registration,
	// google_oauth_*) are still in the table until the second part of the
	// schema reset's stage 6 drops them (docs/single-tenant-reset.md),
	// and nothing reads them.
	var legacy string
	var legacyFlag bool
	if err := scan(&pollMS, &c.MaxWorkers, &c.MaxMergers, &c.MaxMergesPerRepo, &c.MergeBatchSize, &gatherWindowMS, &c.MaxCIJobs, &c.MaxAgentTurns,
		&c.GitHubHost, &c.GitHubInsecureHTTP, &c.GCPProject, &c.GCPServiceAccountEmail,
		&c.NewestFirst, &sandboxBackend, &c.SandboxCPUs, &c.SandboxMemoryMB, &c.SandboxDiskGB, &c.ShowClosedByDefault, &c.DefaultFilters,
		&agentBackends, &c.DefaultModel,
		&c.AgentBackendID, &modelSettings, &c.GitCredential, &gitConnections,
		&c.ApprovedByDefault, &c.AutoMergeByDefault,
		&defaultCapabilities, &c.EnvironmentName, &c.PromptExtension, &c.TaskBranchTemplate, &c.DeploymentID,
		&c.TimeZone,
		&c.UpgradeCheckMode, &c.UpgradeCheckPausesPipeline, &networkAccess, &consoleSessions,
		&c.SandboxImage, &c.PackageCacheEnabled, &c.PackageCacheSizeGB,
		&c.ActionsCacheEnabled, &c.ActionsCacheSizeGB,
		&c.CIRunHistory, &c.CIStepOutputMB, &c.CIStepOutputDays, &c.CIRunOutputKB,
		&c.CICacheMB, &c.CICacheEntries, &c.CICacheDays,
		&c.CIArtifactMB, &c.CIArtifactRunMB, &c.CIArtifactFileMB, &c.CIArtifactDays,
		&c.CIJobsPerRun, &ageBoostMS, &reserveAfterMS,
		&c.MaxAutoSubmitDepth, &c.MaxAutoSubmitFanOut,
		&c.SandboxCPUOvercommit, &c.SandboxMemoryOvercommit, &c.SandboxMemoryReserveMB,
		&c.SandboxDiskFloorShare, &c.SandboxDiskFloorMinMB,
		&c.GCPServiceAccountCount, &c.IAPBackendService, &c.RepairTemplateID,
		&legacyFlag, &legacyFlag,
		&legacyFlag, &legacy, &legacy, &legacy, &c.IAPResource,
		&c.AgentFramework, &c.AgentGitName, &c.AgentGitEmail, &targetRepos,
		&c.K8sNamespace, &c.K8sKubectl, &c.K8sSandboxImage, &c.K8sGranuleImage, &c.K8sWorkspace,
		&c.K8sGitProxyHost, &c.K8sSandboxService, &c.K8sServiceAccount, &k8sNodeSelector,
		&c.K8sPrivileged, &c.K8sNetworkPolicy, &c.K8sDeployment, &c.K8sContainer,
		&mcpServers, &capabilityPolicy, &cliRun, &sandboxOvercommit, &c.BaseCIRedStreak,
		&c.K8sPriorityClassInteractive, &c.K8sPriorityClassAgent, &c.K8sPriorityClassCI, &c.SandboxDiskOvercommit,
		&c.AutoUpgradeChannel, &c.AutoUpgradeTime, &c.JudgementModel, &c.ExternalAgentAPI); err != nil {
		return Config{}, err
	}
	backends, err := DecodeAgentBackends(agentBackends)
	if err != nil {
		return Config{}, err
	}
	c.AgentBackends = backends
	c.ModelSettings = DecodeModelSettings(modelSettings)
	c.GitConnections = DecodeTeamGitConnections(gitConnections)
	c.SandboxBackend = SandboxBackend(sandboxBackend)
	c.NetworkAccess = NetworkAccess(networkAccess)
	// nil for an unset column, which ConsoleSessionsOr reads as "this
	// deployment has never chosen" -- see Config.ConsoleSessions.
	sessions, err := DecodeConsoleSessions(consoleSessions)
	if err != nil {
		return Config{}, err
	}
	c.ConsoleSessions = sessions
	servers, err := DecodeMCPServers(mcpServers)
	if err != nil {
		return Config{}, err
	}
	c.MCPServers = servers
	if c.CapabilityPolicy, err = DecodeCapabilityPolicy(capabilityPolicy); err != nil {
		return Config{}, err
	}
	if c.CLIRun, err = DecodeCLIRun(cliRun); err != nil {
		return Config{}, err
	}
	if c.SandboxOvercommit, err = DecodeOvercommitSettings(sandboxOvercommit); err != nil {
		return Config{}, err
	}
	c.PollInterval = time.Duration(pollMS) * time.Millisecond
	c.MergeGatherWindow = time.Duration(gatherWindowMS) * time.Millisecond
	c.CIAgeBoost = time.Duration(ageBoostMS) * time.Millisecond
	c.CIReserveAfter = time.Duration(reserveAfterMS) * time.Millisecond
	c.DefaultCapabilities = splitCapabilities(defaultCapabilities)
	c.TargetRepos = splitCSV(targetRepos)
	c.K8sNodeSelector = splitCSV(k8sNodeSelector)
	// Same folding, for the same reason, one setting over: a row written
	// before this column existed carries the empty string, which means
	// "nobody has chosen" and reads as the default rather than as "off"
	// (Config.UpgradeCheckMode's own doc comment).
	c.UpgradeCheckMode = NormalizeUpgradeCheckMode(c.UpgradeCheckMode)
	return c, nil
}

// PutConfig replaces the deployment's stored configuration wholesale --
// there is one row, so there is nothing to merge a partial update
// against; a caller changing one field reads Config first the same way
// UpdateTask's mutate does for a task.
func (s *Store) PutConfig(ctx context.Context, c Config) error {
	return s.write(ctx, "update config", func(tx *sql.Tx) error {
		return putConfigTx(ctx, tx, c)
	})
}

// UpdateConfig atomically mutates the deployment's stored configuration:
// it reads the row, hands it to mutate and writes back what mutate left,
// in one transaction, so two writers changing different settings cannot
// lose each other's change the way two read-then-PutConfig callers can.
// A store with no row yet starts mutate from a zero Config, so what it
// writes holds only what mutate set: a row the daemon's first start and
// the first Settings save seed as they would a missing one, keeping its
// connections (Config.ConnectionsOnly).
func (s *Store) UpdateConfig(ctx context.Context, mutate func(*Config) error) error {
	return s.write(ctx, "update config", func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, "SELECT "+configColumns+" FROM `grain_config` WHERE `id` = 1")
		c, err := scanConfig(row.Scan)
		if errors.Is(err, sql.ErrNoRows) {
			c, err = Config{}, nil
		}
		if err != nil {
			return err
		}
		if err := mutate(&c); err != nil {
			return err
		}
		return putConfigTx(ctx, tx, c)
	})
}

func putConfigTx(ctx context.Context, tx *sql.Tx, c Config) error {
	agentBackends, err := EncodeAgentBackends(c.AgentBackends)
	if err != nil {
		return err
	}
	consoleSessions, err := EncodeConsoleSessions(c.ConsoleSessions)
	if err != nil {
		return err
	}
	mcpServers, err := EncodeMCPServers(c.MCPServers)
	if err != nil {
		return err
	}
	capabilityPolicy, err := EncodeCapabilityPolicy(c.CapabilityPolicy)
	if err != nil {
		return err
	}
	cliRun, err := EncodeCLIRun(c.CLIRun)
	if err != nil {
		return err
	}
	sandboxOvercommit, err := EncodeOvercommitSettings(c.SandboxOvercommit)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		"REPLACE INTO `grain_config` (`id`, "+configColumns+") VALUES (1,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
		c.PollInterval.Milliseconds(), c.MaxWorkers, c.MaxMergers, c.MaxMergesPerRepo, c.MergeBatchSize,
		c.MergeGatherWindow.Milliseconds(), c.MaxCIJobs, c.MaxAgentTurns,
		c.GitHubHost, c.GitHubInsecureHTTP, c.GCPProject, c.GCPServiceAccountEmail,
		c.NewestFirst, string(c.SandboxBackend), c.SandboxCPUs, c.SandboxMemoryMB, c.SandboxDiskGB, c.ShowClosedByDefault, c.DefaultFilters,
		agentBackends, c.DefaultModel,
		strings.TrimSpace(c.AgentBackendID), EncodeModelSettings(c.ModelSettings), strings.TrimSpace(c.GitCredential), EncodeTeamGitConnections(c.GitConnections),
		c.ApprovedByDefault, c.AutoMergeByDefault,
		joinCapabilities(c.DefaultCapabilities), c.EnvironmentName, c.PromptExtension, c.TaskBranchTemplate, c.DeploymentID,
		c.TimeZone,
		NormalizeUpgradeCheckMode(c.UpgradeCheckMode), c.UpgradeCheckPausesPipeline,
		string(c.NetworkAccess), consoleSessions, strings.TrimSpace(c.SandboxImage), c.PackageCacheEnabled, c.PackageCacheSizeGB,
		c.ActionsCacheEnabled, c.ActionsCacheSizeGB,
		c.CIRunHistory, c.CIStepOutputMB, c.CIStepOutputDays, c.CIRunOutputKB,
		c.CICacheMB, c.CICacheEntries, c.CICacheDays,
		c.CIArtifactMB, c.CIArtifactRunMB, c.CIArtifactFileMB, c.CIArtifactDays,
		c.CIJobsPerRun, c.CIAgeBoost.Milliseconds(), c.CIReserveAfter.Milliseconds(),
		c.MaxAutoSubmitDepth, c.MaxAutoSubmitFanOut,
		c.SandboxCPUOvercommit, c.SandboxMemoryOvercommit, c.SandboxMemoryReserveMB,
		c.SandboxDiskFloorShare, c.SandboxDiskFloorMinMB,
		c.GCPServiceAccountCount, c.IAPBackendService, c.RepairTemplateID,
		false, false,
		false, "", "", "", c.IAPResource,
		strings.TrimSpace(c.AgentFramework), strings.TrimSpace(c.AgentGitName), strings.TrimSpace(c.AgentGitEmail),
		joinCSV(c.TargetRepos),
		strings.TrimSpace(c.K8sNamespace), strings.TrimSpace(c.K8sKubectl),
		strings.TrimSpace(c.K8sSandboxImage), strings.TrimSpace(c.K8sGranuleImage),
		strings.TrimSpace(c.K8sWorkspace), strings.TrimSpace(c.K8sGitProxyHost),
		strings.TrimSpace(c.K8sSandboxService), strings.TrimSpace(c.K8sServiceAccount),
		joinCSV(c.K8sNodeSelector), c.K8sPrivileged, c.K8sNetworkPolicy,
		strings.TrimSpace(c.K8sDeployment), strings.TrimSpace(c.K8sContainer),
		mcpServers, capabilityPolicy, cliRun, sandboxOvercommit, c.BaseCIRedStreak,
		strings.TrimSpace(c.K8sPriorityClassInteractive), strings.TrimSpace(c.K8sPriorityClassAgent),
		strings.TrimSpace(c.K8sPriorityClassCI), c.SandboxDiskOvercommit,
		strings.TrimSpace(c.AutoUpgradeChannel), strings.TrimSpace(c.AutoUpgradeTime),
		strings.TrimSpace(c.JudgementModel), c.ExternalAgentAPI)
	return err
}

// SetNewestFirst writes grain_config.newest_first on its own, leaving
// every other column of the single config row exactly as it was --
// unlike PutConfig, which replaces the whole row.
//
// It is what ui.Client.CreateTask remembers a filing choice with: a task
// filed at the front or the end of the backlog says which end new work
// should join by default from now on, so the next new-task form opens on
// the choice the last one made (ui.CreateTaskRequest.AtFront). Filing a
// task is not a settings save, which is why it does not go through the
// read-modify-write PutConfig would need: a whole-row write from a task
// creation would carry a Config read moments earlier back over anything
// the Settings pane had written in between, and newest_first is the only
// column a filing has any business touching.
//
// A deployment with no grain_config row yet has nowhere to remember it,
// and this writes nothing rather than creating one: the row's existence
// is what "this deployment has been configured" means
// (ui.Settings.Configured), and filing a task must not answer that
// question on an operator's behalf. The daemon seeds the row the first
// time it starts (cmd/grain's loadConfig), so that case is a store no
// daemon has ever run against.
func (s *Store) SetNewestFirst(ctx context.Context, newestFirst bool) error {
	return s.write(ctx, "remember which end of the backlog new tasks join", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"UPDATE `grain_config` SET `newest_first` = ? WHERE `id` = 1", newestFirst)
		return err
	})
}

// joinCSV/splitCSV round-trip Config.TargetRepos (an owner/name repo can
// never contain a comma) through the same comma-separated shape the
// daemon's own -target-repos flag already parses, so a value written by
// one reads back identically through the other.
//
// Config.DefaultCapabilities is stored the same way, for the same reason:
// a capability id is a bare word (ui.OfferedCapabilities' own rows), with
// no more room for a comma in it than a repo name has.
func joinCSV(items []string) string { return strings.Join(items, ",") }

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

// --- schedules -------------------------------------------------------

// NewScheduleID allocates a schedule identity from its own sequence,
// distinct from task_sequence -- so a schedule's id (e.g. "sched-3") is
// never mistaken for one of the tasks it files, the same "not the GitHub
// issue it was filed from" reasoning NewTaskID's own doc comment gives.
func (s *Store) NewScheduleID(ctx context.Context) (id string, err error) {
	err = s.write(ctx, "allocate a schedule id", func(tx *sql.Tx) error {
		id, err = newScheduleID(ctx, tx)
		return err
	})
	return id, err
}

func newScheduleID(ctx context.Context, tx *sql.Tx) (string, error) {
	res, err := tx.ExecContext(ctx,
		"INSERT INTO `schedule_sequence` (`issued_at`) VALUES (?)", time.Now().UTC())
	if err != nil {
		return "", fmt.Errorf("allocating a schedule id: %w", err)
	}
	n, err := res.LastInsertId()
	if err != nil {
		return "", fmt.Errorf("reading the allocated schedule id: %w", err)
	}
	return "sched-" + strconv.FormatInt(n, 10), nil
}

const scheduleColumns = "`id`,`title`,`body`,`target_owner`,`target_name`,`base`," +
	"`auto_merge`,`auto_approve_proposals`,`template_id`,`suite_id`,`recurrence_kind`,`every_n_hours`,`time_of_day_minutes`,`weekday`,`day_of_month`," +
	"`overlap`,`network_access`,`nothing_needed_action`,`agent_model`,`enabled`,`next_run_at`,`last_run_at`,`created_at`,`time_zone`,`workflow_review`,`every_n_minutes`"

func scanSchedule(scan func(...any) error) (Schedule, error) {
	var t Schedule
	var base, templateID, suiteID sql.NullString
	var kind, overlap, networkAccess, nothingNeeded, timeZone, workflowReview string
	var everyNHours, everyNMinutes, timeOfDay, weekday, dayOfMonth sql.NullInt64
	var lastRun sql.NullTime
	if err := scan(&t.ID, &t.Title, &t.Body, &t.Target.Owner, &t.Target.Name, &base,
		&t.AutoMerge, &t.AutoApproveProposals, &templateID, &suiteID, &kind, &everyNHours, &timeOfDay, &weekday, &dayOfMonth,
		&overlap, &networkAccess, &nothingNeeded, &t.Model, &t.Enabled, &t.NextRunAt, &lastRun, &t.CreatedAt, &timeZone,
		&workflowReview, &everyNMinutes); err != nil {
		return Schedule{}, err
	}
	t.Target = RepoFromStored(t.Target.Owner, t.Target.Name)
	t.Overlap = OverlapPolicy(overlap)
	t.NetworkAccess = NetworkAccess(networkAccess)
	t.NothingNeededAction = NothingNeededAction(nothingNeeded)
	t.WorkflowReview = WorkflowReviewPolicy(workflowReview)
	t.Base = base.String
	if templateID.Valid {
		t.TemplateID = &templateID.String
	}
	if suiteID.Valid {
		t.SuiteID = &suiteID.String
	}
	t.TimeZone = timeZone
	t.Recurrence = Recurrence{
		Kind:          RecurrenceKind(kind),
		EveryNHours:   int(everyNHours.Int64),
		EveryNMinutes: int(everyNMinutes.Int64),
		TimeOfDay:     int(timeOfDay.Int64),
		Weekday:       time.Weekday(weekday.Int64),
		DayOfMonth:    int(dayOfMonth.Int64),
		TimeZone:      timeZone,
	}
	t.LastRunAt = timePtr(lastRun)
	return t, nil
}

// PutSchedule inserts or replaces a schedule wholesale -- putTask's own
// multi-table dance, now that Reads and Grants give a schedule child rows
// of its own (bwsalmon/agents#464).
func (s *Store) PutSchedule(ctx context.Context, t Schedule) error {
	return s.write(ctx, "put schedule "+t.ID,
		func(tx *sql.Tx) error { return putSchedule(ctx, tx, t) })
}

func putSchedule(ctx context.Context, tx *sql.Tx, t Schedule) error {
	r := t.Recurrence
	var everyNHours, everyNMinutes, timeOfDay, weekday, dayOfMonth any
	switch r.Kind {
	case RecurrenceEveryNHours:
		everyNHours = r.EveryNHours
	case RecurrenceEveryNMinutes:
		everyNMinutes = r.EveryNMinutes
	case RecurrenceDaily:
		timeOfDay = r.TimeOfDay
	case RecurrenceWeekly:
		timeOfDay, weekday = r.TimeOfDay, int(r.Weekday)
	case RecurrenceMonthly:
		timeOfDay, dayOfMonth = r.TimeOfDay, r.DayOfMonth
	}
	// The zero value is OverlapSkip (OverlapPolicy's own doc comment), so
	// it is written down as such rather than as an empty string: the
	// column then says the same thing whether the row was written by a
	// caller that set the field or by one that never heard of it.
	overlap := t.Overlap
	if overlap == "" {
		overlap = OverlapSkip
	}
	timeZone := t.TimeZone
	if timeZone == "" {
		timeZone = r.TimeZone
	}
	_, err := tx.ExecContext(ctx, `REPLACE INTO `+"`schedule`"+` (
  `+"`id`,`title`,`body`,`target_owner`,`target_name`,`base`,"+
		"`auto_merge`,`auto_approve_proposals`,`template_id`,`suite_id`,`recurrence_kind`,`every_n_hours`,`time_of_day_minutes`,`weekday`,`day_of_month`,"+
		"`overlap`,`network_access`,`nothing_needed_action`,`agent_model`,`enabled`,`next_run_at`,`last_run_at`,`created_at`,`time_zone`,`workflow_review`,`every_n_minutes`"+`
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.Title, t.Body, t.Target.StoredOwner(), t.Target.Name, nullable(t.Base),
		t.AutoMerge, t.AutoApproveProposals, stringOf(t.TemplateID), stringOf(t.SuiteID), string(r.Kind), everyNHours, timeOfDay, weekday, dayOfMonth,
		string(overlap), string(t.NetworkAccess), string(t.NothingNeededAction), t.Model, t.Enabled, t.NextRunAt.UTC(), timeOf(t.LastRunAt), t.CreatedAt.UTC(), timeZone,
		string(t.WorkflowReview), everyNMinutes)
	if err != nil {
		return fmt.Errorf("writing schedule %s: %w", t.ID, err)
	}

	if err := deleteCompanionRows(ctx, tx, scheduleCompanions, t.ID); err != nil {
		return err
	}
	for _, r := range t.Reads {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO `schedule_read` (`schedule_id`, `owner`, `name`) VALUES (?,?,?)",
			t.ID, r.StoredOwner(), r.Name); err != nil {
			return err
		}
	}
	for _, w := range t.Writes {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO `schedule_write` (`schedule_id`, `owner`, `name`, `base`) VALUES (?,?,?,?)",
			t.ID, w.Repo.StoredOwner(), w.Repo.Name, w.Base); err != nil {
			return err
		}
	}
	for _, g := range t.Grants {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO `schedule_grant` (`schedule_id`, `capability`, `via`, `folder`) VALUES (?,?,?,?)",
			t.ID, g.Capability, string(g.Via), folderOf(g.Folder)); err != nil {
			return err
		}
	}
	return putCustomFieldValues(ctx, tx, "schedule_custom_field", "schedule_id", t.ID, t.CustomFields)
}

// putCustomFieldValues writes one owner's custom field values into its
// own child table -- putTask's own loop over task_custom_field, shared
// by the two other things that carry values (schedule_custom_field,
// template_custom_field) rather than written out a third and fourth
// time.
//
// Name order, the dropped empty value, the row per value and the
// collapsed duplicate are putTask's rules and matter for its reasons: a
// map iterates in a random order and rows written in a different order
// every time would give the state repository a diff on every export of
// a schedule nobody touched, and what a field holds is a set, so an
// owner that said "acme" twice said it once.
//
// The caller is expected to have deleted the owner's existing rows in
// the same transaction, exactly as putTask does.
func putCustomFieldValues(ctx context.Context, tx *sql.Tx,
	table, idColumn, id string, values map[string][]string) error {

	for _, name := range sortedKeys(values) {
		written := map[string]bool{}
		for _, value := range values[name] {
			if value == "" || written[value] {
				continue
			}
			written[value] = true
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO `"+table+"` (`"+idColumn+"`, `name`, `value`) VALUES (?,?,?)",
				id, name, value); err != nil {
				return err
			}
		}
	}
	return nil
}

// customFieldValuesOf reads them back -- nil for an owner carrying none,
// the same "says nothing about any field" a task's own hydration returns
// rather than an empty map.
// Ordered by value as well as by name, for the reason a task's own
// hydration is: what a multi-valued field holds is a set, and one that
// came back shuffled would give the state repository a diff on every
// export of a schedule nobody touched.
func customFieldValuesOf(ctx context.Context, q querier, table, idColumn, id string) (map[string][]string, error) {
	var out map[string][]string
	err := each(ctx, q,
		"SELECT `name`,`value` FROM `"+table+"` WHERE `"+idColumn+"` = ? ORDER BY `name`,`value`",
		id, func(rows *sql.Rows) error {
			var name, value string
			if err := rows.Scan(&name, &value); err != nil {
				return err
			}
			if out == nil {
				out = map[string][]string{}
			}
			out[name] = append(out[name], value)
			return nil
		})
	return out, err
}

// scheduleReadsOf and scheduleGrantsOf are a schedule's Reads and Grants,
// read straight off their own tables -- task.go's grantsOf and GetTask's
// own reads query, ported onto schedule_id.
func scheduleReadsOf(ctx context.Context, q querier, id string) ([]RepoRef, error) {
	var reads []RepoRef
	err := each(ctx, q,
		"SELECT `owner`,`name` FROM `schedule_read` WHERE `schedule_id` = ? ORDER BY `owner`,`name`",
		id, func(rows *sql.Rows) error {
			var owner, name string
			if err := rows.Scan(&owner, &name); err != nil {
				return err
			}
			r := RepoFromStored(owner, name)
			reads = append(reads, r)
			return nil
		})
	return reads, err
}

// scheduleWritesOf is a schedule's Writes, read off schedule_write --
// scheduleReadsOf with the base branch each row carries alongside the
// repo (WriteTarget).
func scheduleWritesOf(ctx context.Context, q querier, id string) ([]WriteTarget, error) {
	var writes []WriteTarget
	err := each(ctx, q,
		"SELECT `owner`,`name`,`base` FROM `schedule_write` WHERE `schedule_id` = ? ORDER BY `owner`,`name`,`base`",
		id, func(rows *sql.Rows) error {
			var w WriteTarget
			var owner, name string
			if err := rows.Scan(&owner, &name, &w.Base); err != nil {
				return err
			}
			w.Repo = RepoFromStored(owner, name)
			writes = append(writes, w)
			return nil
		})
	return writes, err
}

func scheduleGrantsOf(ctx context.Context, q querier, id string) ([]Grant, error) {
	var grants []Grant
	err := each(ctx, q,
		"SELECT `capability`,`via`,`folder` FROM `schedule_grant` WHERE `schedule_id` = ? ORDER BY `capability`",
		id, func(rows *sql.Rows) error {
			var g Grant
			var via string
			var folder sql.NullString
			if err := rows.Scan(&g.Capability, &via, &folder); err != nil {
				return err
			}
			g.Via, g.Folder = GrantSource(via), ParseFolder(folder.String)
			grants = append(grants, g)
			return nil
		})
	return grants, err
}

// hydrateSchedule fills in t's Reads and Grants, read off their own
// tables -- scanSchedule itself only ever reads schedule's own columns,
// the same split scanning a Task's own row has from grantsOf/its reads
// query.
func hydrateSchedule(ctx context.Context, q querier, t *Schedule) error {
	reads, err := scheduleReadsOf(ctx, q, t.ID)
	if err != nil {
		return fmt.Errorf("reading reads of schedule %s: %w", t.ID, err)
	}
	writes, err := scheduleWritesOf(ctx, q, t.ID)
	if err != nil {
		return fmt.Errorf("reading write targets of schedule %s: %w", t.ID, err)
	}
	grants, err := scheduleGrantsOf(ctx, q, t.ID)
	if err != nil {
		return fmt.Errorf("reading grants of schedule %s: %w", t.ID, err)
	}
	values, err := customFieldValuesOf(ctx, q, "schedule_custom_field", "schedule_id", t.ID)
	if err != nil {
		return fmt.Errorf("reading custom field values of schedule %s: %w", t.ID, err)
	}
	t.Reads, t.Writes, t.Grants, t.CustomFields = reads, writes, grants, values
	return nil
}

func getSchedule(ctx context.Context, q querier, id string) (*Schedule, error) {
	t, err := scanSchedule(q.QueryRowContext(ctx,
		"SELECT "+scheduleColumns+" FROM `schedule` WHERE `id` = ?", id).Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if err := hydrateSchedule(ctx, q, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// GetSchedule returns a schedule, or nil if there is none with that ID.
func (s *Store) GetSchedule(ctx context.Context, id string) (*Schedule, error) {
	return getSchedule(ctx, s.db, id)
}

// ListSchedules returns every schedule, newest first -- ListTasks' own
// "the whole table" reasoning applies again at this size.
func (s *Store) ListSchedules(ctx context.Context) ([]Schedule, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT "+scheduleColumns+" FROM `schedule` ORDER BY `created_at` DESC, `id` DESC")
	if err != nil {
		return nil, fmt.Errorf("listing schedules: %w", err)
	}
	var out []Schedule
	for rows.Next() {
		t, err := scanSchedule(rows.Scan)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	for i := range out {
		if err := hydrateSchedule(ctx, s.db, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// DueSchedules is every enabled schedule whose next run has come -- what
// the orchestrator's schedule reconciler fires each cycle. Ordered by id
// for a deterministic firing order, the same reasoning v1's own
// ScheduledJobsConfig.load gives for sorting by name.
func (s *Store) DueSchedules(ctx context.Context, now time.Time) ([]Schedule, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT "+scheduleColumns+" FROM `schedule` "+
			"WHERE `enabled` = 1 AND `next_run_at` <= ? ORDER BY `id`", now.UTC())
	if err != nil {
		return nil, fmt.Errorf("listing due schedules: %w", err)
	}
	var out []Schedule
	for rows.Next() {
		t, err := scanSchedule(rows.Scan)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	for i := range out {
		if err := hydrateSchedule(ctx, s.db, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// UpdateSchedule reads a schedule, applies mutate, and writes it back --
// UpdateTask's own read-modify-write-and-retry shape, for the same
// reason: mutate may run more than once, on a schedule freshly read
// inside each attempt.
func (s *Store) UpdateSchedule(ctx context.Context, id string, mutate func(*Schedule) error) error {
	var missing bool
	err := s.write(ctx, "update schedule "+id, func(tx *sql.Tx) error {
		missing = false
		t, err := getSchedule(ctx, tx, id)
		if err != nil {
			return err
		}
		if t == nil {
			missing = true
			return nil
		}
		if err := mutate(t); err != nil {
			return err
		}
		return putSchedule(ctx, tx, *t)
	})
	if err != nil {
		return err
	}
	if missing {
		return fmt.Errorf("updating schedule %s: no such schedule", id)
	}
	return nil
}

// DeleteSchedule removes a schedule -- unlike a task (Close's own doc
// comment: "a task that ran is a record of a dispatch that happened"), a
// schedule is only ever a standing declaration with no history of its own
// worth keeping once a human no longer wants it, so deleting it outright
// (rather than adding a closed-like flag) loses nothing: every task it
// already filed remains exactly where it always was, untouched by this.
//
// Its companion rows go with it, in the same transaction, exactly as
// DeleteSuite drops a suite's items: nothing in this schema carries a
// foreign key, so a reads/writes/grants/custom-field row left behind
// here is one no schedule claims and nothing ever deletes (grain/task-380).
func (s *Store) DeleteSchedule(ctx context.Context, id string) error {
	return s.write(ctx, "delete schedule "+id, func(tx *sql.Tx) error {
		if err := deleteCompanionRows(ctx, tx, scheduleCompanions, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM `schedule` WHERE `id` = ?", id)
		return err
	})
}

// TaskWithTag returns the task carrying an idempotency tag, if any.
func (s *Store) TaskWithTag(ctx context.Context, tag string) (*Task, error) {
	var id string
	err := s.db.QueryRowContext(ctx, "SELECT `task_id` FROM `task_tag` WHERE `tag` = ? LIMIT 1", tag).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("finding task tagged %s: %w", tag, err)
	}
	return s.GetTask(ctx, id)
}

// HasOpenTaskWithTag reports whether any task carrying tag has not yet
// reached model.StateClosed -- the idempotency check a schedule's own
// firing needs before filing another one. v1's scheduled_jobs.py gave
// every job a marker_label and had _scheduled_jobs list issues by it to
// find a previous firing that has not finished; docs/data-model.md kept
// that idea as a plain tag rather than a capability or a state of its
// own ("neither a state nor a capability: it is an idempotency tag"),
// and this is the query that reads it back.
func (s *Store) HasOpenTaskWithTag(ctx context.Context, tag string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM `task_tag` AS `tg` "+
			"JOIN `task_state` AS `st` ON `st`.`task_id` = `tg`.`task_id` "+
			"WHERE `tg`.`tag` = ? AND `st`.`state` != ?", tag, string(StateClosed)).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("checking for an open task tagged %s: %w", tag, err)
	}
	return n > 0, nil
}

// OpenTaskWithTag is the newest task carrying tag that has not reached
// StateClosed, or nil if none has -- HasOpenTaskWithTag's question asked
// for the task rather than for a yes/no.
//
// A caller wants the task itself when "a previous firing is still open"
// is not the end of the answer: workflowFiringInFlight needs to know
// *which* task is holding a schedule's cadence and since when, because a
// review nobody ever closes must not hold it for ever
// (workflowReviewHold), and because a firing that goes ahead over an
// abandoned review has to name the review it went ahead over.
//
// The newest open one is the right one to answer with for that question:
// if the most recently filed open task is old enough to have been
// abandoned, every older one carrying the same tag is older still.
func (s *Store) OpenTaskWithTag(ctx context.Context, tag string) (*Task, error) {
	var id string
	err := s.db.QueryRowContext(ctx,
		"SELECT `tg`.`task_id` FROM `task_tag` AS `tg` "+
			"JOIN `task_state` AS `st` ON `st`.`task_id` = `tg`.`task_id` "+
			"JOIN `task` AS `t` ON `t`.`id` = `tg`.`task_id` "+
			"WHERE `tg`.`tag` = ? AND `st`.`state` != ? "+
			"ORDER BY `t`.`created_at` DESC, `tg`.`task_id` DESC LIMIT 1",
		tag, string(StateClosed)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("looking up the open task tagged %s: %w", tag, err)
	}
	return s.GetTask(ctx, id)
}

// OpenTasksWithTag is every task carrying tag that has not reached
// StateClosed, oldest first -- OpenTaskWithTag's question asked for all of
// them, for a caller closing a set out rather than reading the newest.
func (s *Store) OpenTasksWithTag(ctx context.Context, tag string) ([]string, error) {
	var out []string
	err := each(ctx, s.db,
		"SELECT `tg`.`task_id` FROM `task_tag` AS `tg` "+
			"JOIN `task_state` AS `st` ON `st`.`task_id` = `tg`.`task_id` "+
			"JOIN `task` AS `t` ON `t`.`id` = `tg`.`task_id` "+
			"WHERE `tg`.`tag` = ? AND `st`.`state` != ? "+
			"ORDER BY `t`.`created_at`, `tg`.`task_id`",
		[]any{tag, string(StateClosed)},
		func(rows *sql.Rows) error {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			out = append(out, id)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("listing the open tasks tagged %s: %w", tag, err)
	}
	return out, nil
}

// OpenTasksWithLink is every open task carrying a link of kind (a port's
// LinkPortOf, say), oldest first.
func (s *Store) OpenTasksWithLink(ctx context.Context, kind LinkKind) ([]string, error) {
	var out []string
	err := each(ctx, s.db,
		"SELECT DISTINCT `l`.`task_id`, `t`.`created_at` FROM `task_link` AS `l` "+
			"JOIN `task_state` AS `st` ON `st`.`task_id` = `l`.`task_id` "+
			"JOIN `task` AS `t` ON `t`.`id` = `l`.`task_id` "+
			"WHERE `l`.`kind` = ? AND `st`.`state` != ? "+
			"ORDER BY `t`.`created_at`, `l`.`task_id`",
		[]any{string(kind), string(StateClosed)},
		func(rows *sql.Rows) error {
			var id string
			var created any
			if err := rows.Scan(&id, &created); err != nil {
				return err
			}
			out = append(out, id)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("listing the open tasks linked %s: %w", kind, err)
	}
	return out, nil
}

// OpenTasksWithLinkTarget is every open task carrying a link of kind to
// target (the open tasks whose own pull request is "owner/name#7", say),
// oldest first.
func (s *Store) OpenTasksWithLinkTarget(ctx context.Context, kind LinkKind, target string) ([]string, error) {
	var out []string
	err := each(ctx, s.db,
		"SELECT DISTINCT `l`.`task_id`, `t`.`created_at` FROM `task_link` AS `l` "+
			"JOIN `task_state` AS `st` ON `st`.`task_id` = `l`.`task_id` "+
			"JOIN `task` AS `t` ON `t`.`id` = `l`.`task_id` "+
			"WHERE `l`.`kind` = ? AND `l`.`target` = ? AND `st`.`state` != ? "+
			"ORDER BY `t`.`created_at`, `l`.`task_id`",
		[]any{string(kind), target, string(StateClosed)},
		func(rows *sql.Rows) error {
			var id string
			var created any
			if err := rows.Scan(&id, &created); err != nil {
				return err
			}
			out = append(out, id)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("listing the open tasks linked %s to %s: %w", kind, target, err)
	}
	return out, nil
}

// RunningTaskWithTag returns an open task tagged with tag that is currently
// in StateRunning, or nil if no such task is running.
func (s *Store) RunningTaskWithTag(ctx context.Context, tag string) (*Task, error) {
	var id string
	err := s.db.QueryRowContext(ctx,
		"SELECT `tg`.`task_id` FROM `task_tag` AS `tg` "+
			"JOIN `task_state` AS `st` ON `st`.`task_id` = `tg`.`task_id` "+
			"JOIN `task` AS `t` ON `t`.`id` = `tg`.`task_id` "+
			"WHERE `tg`.`tag` = ? AND `st`.`state` = ? "+
			"ORDER BY `t`.`created_at` DESC, `tg`.`task_id` DESC LIMIT 1",
		tag, string(StateRunning)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("looking up running task tagged %s: %w", tag, err)
	}
	return s.GetTask(ctx, id)
}

// HasTaskWithTag reports whether any task carries tag at all, closed
// ones included -- HasOpenTaskWithTag's question asked over the whole
// record rather than over what is still in flight.
//
// The two are different idempotency guarantees, and the caller picks.
// "Not again while the last one is still going" is a schedule's, and is
// what HasOpenTaskWithTag answers: the next firing is meant to happen,
// eventually. "Not ever again" is the upgrade check's: an upgrade is
// checked once, and a check that ran and closed last week is the reason
// not to file another one for the same upgrade now
// (orchestrator's upgradeCheckTag).
func (s *Store) HasTaskWithTag(ctx context.Context, tag string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM `task_tag` WHERE `tag` = ?", tag).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("checking for a task tagged %s: %w", tag, err)
	}
	return n > 0, nil
}

// TaskIDWithTag is the task carrying tag, or "" if no task does --
// HasTaskWithTag's question asked for the answer rather than for a
// yes/no.
//
// It is what a standing console session finds its own task by
// (model.ConsoleSessionTag, orchestrator.reconcileConsoleSessions): the
// tag is the session's identity, and the task behind it is looked up
// rather than remembered anywhere, so the association survives a store
// restored from a state repository dump with no extra bookkeeping.
//
// An open task wins over a closed one. That is the important case for a
// standing session: closing its conversation leaves the record tagged
// and its reconciler files a replacement, which is now the task the
// session must attach to. Among tasks with the same closure status, the
// newest wins. A hand-edited store can carry times that are not monotonic,
// so chronology alone must not strand the session on its closed record.
func (s *Store) TaskIDWithTag(ctx context.Context, tag string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx,
		"SELECT `tg`.`task_id` FROM `task_tag` AS `tg` "+
			"JOIN `task` AS `t` ON `t`.`id` = `tg`.`task_id` "+
			"LEFT JOIN `task_observation` AS `o` ON `o`.`task_id` = `tg`.`task_id` "+
			"WHERE `tg`.`tag` = ? "+
			"ORDER BY (`o`.`closed_at` IS NOT NULL), `t`.`created_at` DESC, `tg`.`task_id` DESC LIMIT 1",
		tag).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("looking up the task tagged %s: %w", tag, err)
	}
	return id, nil
}

// OpenConsoleSessionTaskIDs returns a map of session ID -> open task ID for
// every unclosed task tagged with ConsoleSessionTagPrefix.
func (s *Store) OpenConsoleSessionTaskIDs(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	err := each(ctx, s.db,
		"SELECT `tg`.`tag`, `tg`.`task_id` FROM `task_tag` AS `tg` "+
			"LEFT JOIN `task_observation` AS `o` ON `o`.`task_id` = `tg`.`task_id` "+
			"WHERE `tg`.`tag` >= ? AND `tg`.`tag` < ? AND `o`.`closed_at` IS NULL",
		[]any{ConsoleSessionTagPrefix, ConsoleSessionTagPrefix + "\xff"},
		func(rows *sql.Rows) error {
			var tag, taskID string
			if err := rows.Scan(&tag, &taskID); err != nil {
				return err
			}
			if id, ok := ConsoleSessionIDFromTag(tag); ok {
				out[id] = taskID
			}
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("listing open console session tasks: %w", err)
	}
	return out, nil
}

// --- templates ----------------------------------------------------

// NewTemplateID allocates a template identity from its own sequence,
// distinct from task_sequence and schedule_sequence -- the same "not
// mistaken for one of the things that use it" reasoning NewScheduleID's
// own doc comment gives.
func (s *Store) NewTemplateID(ctx context.Context) (id string, err error) {
	err = s.write(ctx, "allocate a template id", func(tx *sql.Tx) error {
		id, err = newTemplateID(ctx, tx)
		return err
	})
	return id, err
}

func newTemplateID(ctx context.Context, tx *sql.Tx) (string, error) {
	res, err := tx.ExecContext(ctx,
		"INSERT INTO `template_sequence` (`issued_at`) VALUES (?)", time.Now().UTC())
	if err != nil {
		return "", fmt.Errorf("allocating a template id: %w", err)
	}
	n, err := res.LastInsertId()
	if err != nil {
		return "", fmt.Errorf("reading the allocated template id: %w", err)
	}
	return "template-" + strconv.FormatInt(n, 10), nil
}

const templateColumns = "`id`,`name`,`title`,`body`," +
	"`target_owner`,`target_name`,`base`,`auto_merge`,`created_at`,`network_access`,`nothing_needed_action`,`agent_model`,`interactive`,`goal`,`change_target_kind`,`change_target_spec`"

func scanTemplate(scan func(...any) error) (Template, error) {
	var t Template
	var target RepoRef
	var base sql.NullString
	var networkAccess, nothingNeeded, goal, ctKind, ctSpec string
	if err := scan(&t.ID, &t.Name, &t.Title, &t.Body,
		&target.Owner, &target.Name, &base, &t.AutoMerge, &t.CreatedAt, &networkAccess, &nothingNeeded, &t.Model, &t.Interactive,
		&goal, &ctKind, &ctSpec); err != nil {
		return Template{}, err
	}
	t.ChangeTarget = ChangeTargetFromStored(ctKind, ctSpec)
	// A goal this build cannot read is dropped rather than failing every
	// read of the template table.
	t.Goal, _ = ParseGoalDecl(goal)
	t.NetworkAccess = NetworkAccess(networkAccess)
	t.NothingNeededAction = NothingNeededAction(nothingNeeded)
	// An empty owner and name is an unbound template, not a binding to a
	// repo with no name (schema.go's own DDL comment on why the pair is
	// stored empty rather than NULL).
	if target.Owner != "" || target.Name != "" {
		target = RepoFromStored(target.Owner, target.Name)
		t.Target = &target
	}
	t.Base = base.String
	return t, nil
}

// PutTemplate inserts or replaces a template wholesale -- putSchedule's
// own multi-table dance, ported onto template's own child tables.
func (s *Store) PutTemplate(ctx context.Context, t Template) error {
	return s.write(ctx, "put template "+t.ID,
		func(tx *sql.Tx) error { return putTemplate(ctx, tx, t) })
}

func putTemplate(ctx context.Context, tx *sql.Tx, t Template) error {
	var targetOwner, targetName string
	if t.Target != nil {
		targetOwner, targetName = t.Target.StoredOwner(), t.Target.Name
	}
	_, err := tx.ExecContext(ctx, `REPLACE INTO `+"`template`"+` (
  `+templateColumns+`
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.Name, t.Title, t.Body,
		targetOwner, targetName, t.Base, t.AutoMerge, t.CreatedAt.UTC(), string(t.NetworkAccess), string(t.NothingNeededAction), t.Model, t.Interactive,
		t.Goal.JSON(), changeTargetKind(t.ChangeTarget), t.ChangeTarget.SpecString())
	if err != nil {
		return fmt.Errorf("writing template %s: %w", t.ID, err)
	}

	if err := deleteCompanionRows(ctx, tx, templateCompanions, t.ID); err != nil {
		return err
	}
	for _, r := range t.Reads {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO `template_read` (`template_id`, `owner`, `name`) VALUES (?,?,?)",
			t.ID, r.StoredOwner(), r.Name); err != nil {
			return err
		}
	}
	for _, w := range t.Writes {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO `template_write` (`template_id`, `owner`, `name`, `base`) VALUES (?,?,?,?)",
			t.ID, w.Repo.StoredOwner(), w.Repo.Name, w.Base); err != nil {
			return err
		}
	}
	for _, g := range t.Grants {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO `template_grant` (`template_id`, `capability`, `via`, `folder`) VALUES (?,?,?,?)",
			t.ID, g.Capability, string(g.Via), folderOf(g.Folder)); err != nil {
			return err
		}
	}
	// position, rather than the order rows happen to come back in: the
	// order a template's inputs are declared in is the order a form asks
	// for them (Template.Fields' own doc comment), and it is this column
	// that survives a dump and a reimport of it.
	for i, f := range t.Fields {
		options, err := json.Marshal(f.Options)
		if err != nil {
			return fmt.Errorf("writing field %s of template %s: %w", f.Name, t.ID, err)
		}
		if f.Options == nil {
			// "[]" rather than "null", for PutCustomField's own reason:
			// a dump of a table nobody has changed should be
			// byte-identical however the slice was built.
			options = []byte("[]")
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO `template_field` (`template_id`, `name`, `label`, `options`, `default_value`, `required`, `position`)"+
				" VALUES (?,?,?,?,?,?,?)",
			t.ID, f.Name, f.Label, string(options), f.Default, f.Required, i); err != nil {
			return err
		}
	}
	return putCustomFieldValues(ctx, tx, "template_custom_field", "template_id", t.ID, t.CustomFields)
}

// hydrateTemplate fills in t's Reads and Grants, read off their own
// tables -- hydrateSchedule's own split-scan reasoning applies again
// here.
func hydrateTemplate(ctx context.Context, q querier, t *Template) error {
	var reads []RepoRef
	if err := each(ctx, q,
		"SELECT `owner`,`name` FROM `template_read` WHERE `template_id` = ? ORDER BY `owner`,`name`",
		t.ID, func(rows *sql.Rows) error {
			var owner, name string
			if err := rows.Scan(&owner, &name); err != nil {
				return err
			}
			r := RepoFromStored(owner, name)
			reads = append(reads, r)
			return nil
		}); err != nil {
		return fmt.Errorf("reading reads of template %s: %w", t.ID, err)
	}
	var writes []WriteTarget
	if err := each(ctx, q,
		"SELECT `owner`,`name`,`base` FROM `template_write` WHERE `template_id` = ? ORDER BY `owner`,`name`,`base`",
		t.ID, func(rows *sql.Rows) error {
			var w WriteTarget
			var owner, name string
			if err := rows.Scan(&owner, &name, &w.Base); err != nil {
				return err
			}
			w.Repo = RepoFromStored(owner, name)
			writes = append(writes, w)
			return nil
		}); err != nil {
		return fmt.Errorf("reading write targets of template %s: %w", t.ID, err)
	}
	var grants []Grant
	if err := each(ctx, q,
		"SELECT `capability`,`via`,`folder` FROM `template_grant` WHERE `template_id` = ? ORDER BY `capability`",
		t.ID, func(rows *sql.Rows) error {
			var g Grant
			var via string
			var folder sql.NullString
			if err := rows.Scan(&g.Capability, &via, &folder); err != nil {
				return err
			}
			g.Via, g.Folder = GrantSource(via), ParseFolder(folder.String)
			grants = append(grants, g)
			return nil
		}); err != nil {
		return fmt.Errorf("reading grants of template %s: %w", t.ID, err)
	}
	var fields []TemplateField
	if err := each(ctx, q,
		"SELECT `name`,`label`,`options`,`default_value`,`required` FROM `template_field`"+
			" WHERE `template_id` = ? ORDER BY `position`,`name`",
		t.ID, func(rows *sql.Rows) error {
			var f TemplateField
			var options string
			if err := rows.Scan(&f.Name, &f.Label, &options, &f.Default, &f.Required); err != nil {
				return err
			}
			// An unreadable options column reads as a text field rather
			// than as an error -- scanCustomField's own reasoning: the
			// column is JSON grain wrote itself, and a template whose
			// one selector cannot be read is still worth listing and
			// still worth filing from.
			if options != "" {
				var parsed []string
				if err := json.Unmarshal([]byte(options), &parsed); err == nil {
					f.Options = parsed
				}
			}
			fields = append(fields, f)
			return nil
		}); err != nil {
		return fmt.Errorf("reading input fields of template %s: %w", t.ID, err)
	}
	values, err := customFieldValuesOf(ctx, q, "template_custom_field", "template_id", t.ID)
	if err != nil {
		return fmt.Errorf("reading custom field values of template %s: %w", t.ID, err)
	}
	t.Reads, t.Writes, t.Grants, t.Fields, t.CustomFields = reads, writes, grants, fields, values
	return nil
}

func getTemplate(ctx context.Context, q querier, id string) (*Template, error) {
	t, err := scanTemplate(q.QueryRowContext(ctx,
		"SELECT "+templateColumns+" FROM `template` WHERE `id` = ?", id).Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if err := hydrateTemplate(ctx, q, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// GetTemplate returns a template, or nil if there is none with that ID.
func (s *Store) GetTemplate(ctx context.Context, id string) (*Template, error) {
	return getTemplate(ctx, s.db, id)
}

// ListTemplates returns every template, newest first -- ListSchedules'
// own "the whole table" reasoning applies again at this size.
func (s *Store) ListTemplates(ctx context.Context) ([]Template, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT "+templateColumns+" FROM `template` ORDER BY `created_at` DESC, `id` DESC")
	if err != nil {
		return nil, fmt.Errorf("listing templates: %w", err)
	}
	var out []Template
	for rows.Next() {
		t, err := scanTemplate(rows.Scan)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	for i := range out {
		if err := hydrateTemplate(ctx, s.db, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// UpdateTemplate reads a template, applies mutate, and writes it back
// -- UpdateSchedule's own read-modify-write-and-retry shape, for the
// same reason: mutate may run more than once, on a template freshly
// read inside each attempt.
func (s *Store) UpdateTemplate(ctx context.Context, id string, mutate func(*Template) error) error {
	var missing bool
	err := s.write(ctx, "update template "+id, func(tx *sql.Tx) error {
		missing = false
		t, err := getTemplate(ctx, tx, id)
		if err != nil {
			return err
		}
		if t == nil {
			missing = true
			return nil
		}
		if err := mutate(t); err != nil {
			return err
		}
		return putTemplate(ctx, tx, *t)
	})
	if err != nil {
		return err
	}
	if missing {
		return fmt.Errorf("updating template %s: no such template", id)
	}
	return nil
}

// DeleteTemplate removes a template outright -- DeleteSchedule's own
// doc comment gives the reasoning: a template is only ever a standing
// declaration, so there is no history on the row itself worth keeping
// once nobody wants it. Callers that must not orphan a schedule still
// pointing at this template (ui.Client.DeleteTemplate) check
// SchedulesUsingTemplate first; the store itself enforces nothing here,
// the same way it enforces nothing about a task naming a repo nobody
// configured.
//
// Its companion rows go with it, in the same transaction, for
// DeleteSchedule's own reason.
func (s *Store) DeleteTemplate(ctx context.Context, id string) error {
	return s.write(ctx, "delete template "+id, func(tx *sql.Tx) error {
		if err := deleteCompanionRows(ctx, tx, templateCompanions, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM `template` WHERE `id` = ?", id)
		return err
	})
}

// SchedulesUsingTemplate returns every schedule whose TemplateID is id --
// what ui.Client.DeleteTemplate checks before deleting one out from under
// a schedule that still fires from it, and what a template's own "used by
// N schedules" display reads.
func (s *Store) SchedulesUsingTemplate(ctx context.Context, id string) ([]Schedule, error) {
	return s.schedulesUsing(ctx, "`template_id`", "template", id)
}

// UnsettledTasksInRepo returns the ids of repo's tasks that are still in
// play -- anything whose task_state is not one of SettledStates.
//
// It answers one question, for StrandedByTargetRepos (repo_scope.go): is
// taking this repo off the allowlist going to strand work? It is,
// exactly here -- each of these tasks is parked on its next dispatch
// rather than run (orchestrator.reconcileTargetRepos), and putting the
// repo back plus a reply on each of them is what lifts that. Which is
// why the count is worth showing to whoever is narrowing the list while
// they still have the choice.
//
// Matched with lower() on both sides rather than exactly, for
// RepoRef.Same's reason: two spellings of one repository are one
// repository, so a task filed against `Bwsalmon/grain-home` is work in
// the repo an operator is removing when they remove
// `bwsalmon/grain-home`. Matching exactly here would let precisely the
// tasks this is meant to protect slip through, since a spelling
// mismatch is how they came to be filed that way in the first place.
func (s *Store) UnsettledTasksInRepo(ctx context.Context, repo RepoRef) ([]string, error) {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(SettledStates)), ",")
	args := []any{strings.ToLower(repo.Owner), strings.ToLower(repo.Name)}
	for _, st := range SettledStates {
		args = append(args, string(st))
	}
	var out []string
	err := each(ctx, s.db,
		"SELECT `t`.`id` FROM `task` AS `t` "+
			"JOIN `task_state` AS `st` ON `st`.`task_id` = `t`.`id` "+
			"WHERE lower(`t`.`target_owner`) = ? AND lower(`t`.`target_name`) = ? "+
			"AND `st`.`state` NOT IN ("+placeholders+") ORDER BY `t`.`id`",
		args,
		func(rows *sql.Rows) error {
			var taskID string
			if err := rows.Scan(&taskID); err != nil {
				return err
			}
			out = append(out, taskID)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("looking for open tasks in %s: %w", repo, err)
	}
	return out, nil
}

// MergedUpstreamTaskBase checks whether base names the branch of a task in repo
// whose pull request has merged (Observation.PrMergedAt != nil). If so, it
// follows any chain of merged upstream tasks to return the live targetBase the
// upstream work landed on (for example "candidate", "main", or "" for the
// repository default branch), the immediate upstream task's ID, and merged=true.
func (s *Store) MergedUpstreamTaskBase(ctx context.Context, repo RepoRef, base string) (targetBase, upstreamID string, merged bool, err error) {
	if s == nil || base == "" {
		return "", "", false, nil
	}
	owner := strings.ToLower(repo.StoredOwner())
	name := strings.ToLower(repo.Name)
	curr := base
	seen := map[string]bool{curr: true}
	for hop := 0; hop < 8; hop++ {
		legacyID := ""
		if strings.HasPrefix(curr, "grain/task-") {
			legacyID = strings.TrimPrefix(curr, "grain/task-")
		}
		var id string
		var nextBase sql.NullString
		qErr := s.db.QueryRowContext(ctx,
			"SELECT `t`.`id`, `t`.`base` "+
				"FROM `task` AS `t` "+
				"JOIN `task_observation` AS `o` ON `o`.`task_id` = `t`.`id` "+
				"WHERE lower(`t`.`target_owner`) = ? AND lower(`t`.`target_name`) = ? "+
				"AND (`t`.`branch` = ? OR (`t`.`branch` = '' AND ? != '' AND `t`.`id` = ?)) "+
				"AND `o`.`pr_merged_at` IS NOT NULL "+
				"ORDER BY `o`.`pr_merged_at` DESC, `t`.`id` DESC "+
				"LIMIT 1",
			owner, name, curr, legacyID, legacyID).Scan(&id, &nextBase)
		if errors.Is(qErr, sql.ErrNoRows) {
			break
		}
		if qErr != nil {
			return "", "", false, fmt.Errorf("resolving merged upstream base %q in %s: %w", base, repo, qErr)
		}
		if !merged {
			upstreamID = id
			merged = true
		}
		curr = nextBase.String
		if curr == "" || seen[curr] {
			break
		}
		seen[curr] = true
	}
	if !merged {
		return "", "", false, nil
	}
	return curr, upstreamID, true, nil
}

// TasksReviewedByTemplate returns the ID of every still-open task whose
// ReviewTemplateID is id -- what ui.Client.DeleteTemplate checks before
// deleting a template out from under a task that is still going to be
// reviewed from it, SchedulesUsingTemplate's own reasoning applied to
// the third thing that can point at a template.
//
// Closed tasks are deliberately excluded, unlike the schedule, plan and
// suite queries beside this one: those name standing declarations that
// go on firing, where a task is a single piece of work, and one that has
// closed will never be reviewed again however long its
// review_template_id keeps naming a row. Counting them would make a
// template used once, months ago, undeletable forever.
func (s *Store) TasksReviewedByTemplate(ctx context.Context, id string) ([]string, error) {
	var out []string
	err := each(ctx, s.db,
		"SELECT `t`.`id` FROM `task` AS `t` "+
			"JOIN `task_state` AS `st` ON `st`.`task_id` = `t`.`id` "+
			"WHERE `t`.`review_template_id` = ? AND `st`.`state` != ? ORDER BY `t`.`id`",
		[]any{id, string(StateClosed)},
		func(rows *sql.Rows) error {
			var taskID string
			if err := rows.Scan(&taskID); err != nil {
				return err
			}
			out = append(out, taskID)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("listing tasks reviewed by template %s: %w", id, err)
	}
	return out, nil
}

// SchedulesUsingSuite returns every schedule whose SuiteID is id -- what
// ui.Client.DeleteSuite checks before deleting a suite out from under a
// schedule that still runs it, SchedulesUsingTemplate's own reasoning
// applied to the other thing a schedule can point at.
func (s *Store) SchedulesUsingSuite(ctx context.Context, id string) ([]Schedule, error) {
	return s.schedulesUsing(ctx, "`suite_id`", "suite", id)
}

func (s *Store) schedulesUsing(ctx context.Context, column, what, id string) ([]Schedule, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT "+scheduleColumns+" FROM `schedule` WHERE "+column+" = ? ORDER BY `id`", id)
	if err != nil {
		return nil, fmt.Errorf("listing schedules using %s %s: %w", what, id, err)
	}
	var out []Schedule
	for rows.Next() {
		t, err := scanSchedule(rows.Scan)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	for i := range out {
		if err := hydrateSchedule(ctx, s.db, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// --- helpers ---------------------------------------------------------

func each(ctx context.Context, q querier, query string, args any,
	scan func(*sql.Rows) error) error {
	var list []any
	switch a := args.(type) {
	case nil:
	case []any:
		list = a
	default:
		list = []any{a}
	}
	rows, err := q.QueryContext(ctx, query, list...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func principalFrom(kind, id sql.NullString) *Principal {
	if !kind.Valid || kind.String == "" {
		return nil
	}
	return &Principal{Kind: PrincipalKind(kind.String), ID: id.String}
}

func kindOf(p *Principal) any {
	if p == nil {
		return nil
	}
	return string(p.Kind)
}

func idOf(p *Principal) any {
	if p == nil {
		return nil
	}
	return p.ID
}

func folderOf(f *FolderRef) any {
	if f == nil {
		return nil
	}
	return f.String()
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func stringOf(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func timeOf(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC()
}

func int64Of(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

func timePtr(n sql.NullTime) *time.Time {
	if !n.Valid {
		return nil
	}
	t := n.Time
	return &t
}

func int64Ptr(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	v := n.Int64
	return &v
}

// intOf and intPtr are int64Of/int64Ptr for the *int fields -- the diff
// sizes an observation carries (Observation.PrAdditions), which are
// counts small enough to be an int everywhere else they are read and
// would only be an int64 here to save these six lines.
func intOf(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}

func intPtr(n sql.NullInt64) *int {
	if !n.Valid {
		return nil
	}
	v := int(n.Int64)
	return &v
}

// DB returns the underlying database handle.
func (s *Store) DB() *sql.DB {
	return s.db
}
