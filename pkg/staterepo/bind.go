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

package staterepo

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ErrSchemaTooNew is returned when the repository was written by a build
// that knows a later schema than this one. It is pkg/model's own
// ErrSchemaTooNew argument, one layer out: importing rows shaped for a
// schema this build does not have produces a database that is wrong
// rather than one that refuses to start.
var ErrSchemaTooNew = errors.New("state repository was written by a newer build of grain")

// ErrSchemaTooOld is returned when the repository holds a dump from an
// earlier schema. Nothing here migrates one: a state repository is a
// dump, not a database, and the supported move is to wipe it and let a
// fresh Seed write today's schema (README.md's own note on the state
// repository being destructive across schema changes).
var ErrSchemaTooOld = errors.New("state repository was written by an older build of grain")

// ErrNotApplied marks a pull that arrived and could not be made live:
// the working tree holds commits the database has not taken up. It is
// returned wrapped around whatever the actual failure was, so a caller
// can tell that case apart from a fetch that never got off the ground --
// which matters, because exporting the database over a working tree in
// that state commits a revert of somebody's merged pull request and
// pushes it. The daemon's own sync cycle stops there rather than doing
// that (pkg/cli/statemanager.go).
var ErrNotApplied = errors.New("state repository holds changes grain could not apply")

// ErrNoLocalCopy is the one unreachable remote a Load will not carry on
// from: a fetch that failed against a working tree with no commits in it
// at all.
//
// The difference is what grain would be running on. A tree with commits
// in it was cloned or fetched at some point, so the dump under it is
// this repository's own content and the database beside it was loaded
// from that -- the remote being unreachable now costs nothing but the
// news, and the next tick asks again. A tree with no commits has never
// held a byte of this repository, and nothing on this host can say
// whether the remote holds a whole deployment's state or nothing at all.
// Carrying on there means Seed: an export committed as a root commit
// sharing no history with the remote's branch, which no push can ever
// fast-forward and every later pull reports as divergence. Better to
// stop and say why, and let the start after this one find the remote
// back.
//
// Deliberately not wrapped around ErrUnreachable, even though that is
// what caused it, so that errors.Is(err, ErrUnreachable) means exactly
// one thing at a call site deciding whether to start: loaded, but out of
// touch with the remote.
var ErrNoLocalCopy = errors.New("the state repository's remote could not be reached and this host " +
	"has no copy of it to fall back on")

// ErrDatabaseEmpty is the export this package will not do: writing a
// database with nothing in it over a repository that holds a whole
// deployment.
//
// Nothing further up stops that on its own. The remote is not ahead --
// this host really is the one that wrote the commit the dump sits on --
// so RemoteAhead has no objection, the commit is a fast-forward, and the
// push lands. The off-host copy of the deployment is then deleted by the
// deployment, in one commit, by the very loop that exists to keep it.
//
// The way in is a store that was lost while its working tree survived:
// the file deleted, a volume restored from a backup that did not include
// it, sqlite.Open handed a path that has never held one. A *start* in
// that state now puts itself right rather than reaching this at all --
// Load imports the whole repository into a database that has nothing to
// lose by it, which is the restore the repository exists for. This is
// what is left over: a database that went away underneath a daemon
// already running, and a `grain state sync` run against one. There the
// answer is to export nothing, say so where an operator looks (the UI's
// State pane reports it, cmd/grain's stateManager), and leave the
// restore to the restart that can do it safely -- an import that
// replaces every row is not something to do underneath a reconcile loop
// holding the ids it would delete.
var ErrDatabaseEmpty = errors.New("grain's database holds nothing and its state repository holds a " +
	"deployment: refusing to export an empty database over it -- restart grain to restore from the " +
	"repository, or point it at the store it was running on")

// SettingsTables names the tables Apply imports into a running daemon.
//
// The list is the whole of the "which rows may change underneath a live
// deployment" decision, so it is here, once, rather than implied by a
// filter somewhere. Two properties put a table on it. It has to be
// something a human or an agent proposes a change to -- a template, a
// suite, a schedule, a repo's configuration, the deployment's own
// config row -- because a table nothing proposes a change to has
// nothing to apply. And it has to be something the daemon does not
// write for itself while it runs, because Import is a replacement:
// clearing and refilling a table the reconcile loop is inserting into
// would delete rows written since the dump was made.
//
// That second property is what keeps task, task_run, lease, the metrics
// and the release and qualification *runs* off the list. Those are
// grain's own record of what it did, the database is authoritative for
// them, and they stay with the startup import (Load), which happens
// before anything is live. A merged change to one of them is therefore
// not applied and is written back out by the next export -- see the
// README's own note on the direction of travel.
//
// One consequence of "Import is a replacement" is worth saying out loud,
// because it costs something and the cost is accepted rather than
// overlooked: the replacement is whole rows, so a column a file does not
// mention lands on its DDL default, not on what the database was
// holding. A settings file that is missing a column is not a malformed
// one -- it is what every dump written before that column existed looks
// like, since a column added with a default does not move
// model.SchemaVersion -- so this is reachable by an ordinary merge: a
// pull request against grain_config.json branched before the upgrade and
// merged after it, or a settings file edited by hand that simply does
// not mention the new column. The column reverts on the next tick, and
// the export after that writes the revert back out.
//
// How much that costs is exactly what the column's default means. A
// default of the empty string is the usual case, and reverting one
// changes nothing anybody would notice, because empty is what every
// consumer already reads as "nobody has chosen": gemini_effort,
// agent_git_name, agent_git_email and github_posting_credential, all added on
// 2026-09-05, are that. grain_config.time_zone, added the same day, is
// not -- its default is a zone (model.DefaultTimeZone), so a revert
// moves every timestamp in the UI and the firing time of every daily,
// weekly and monthly schedule. Adding a settings column whose default
// means something is therefore adding a value an old-shaped file can
// change without saying so, and that is the thing to weigh when adding
// one.
//
// grain_config.network_access and template.network_access
// (grain/task-269), and schedule.network_access (grain/task-427), are
// the borderline case: all three default to the empty
// string, which is the usual "nobody has chosen" -- but what nobody
// having chosen *means* here is model.NetworkOpen, so a settings file
// merged from before the column silently gives the internet back to a
// deployment, or to a standing job, that was set to run without it. It
// is a revert of a security setting rather than of a preference, which
// is worth knowing when reading one of these warnings. It is pinned by
// TestApplyingASettingsFileWithoutAColumnResetsItToItsDefault, beside
// the tests for what the same missing columns do on the way in.
//
// template.nothing_needed_action and schedule.nothing_needed_action
// (grain/task-1599) are the ordinary shape: the empty default reads as
// model.NothingNeededClose, which is what every task filed before either
// column could say otherwise did. A settings file merged from before
// them turns a standing job that had chosen "ack" back to closing its
// nothing_needed endings unseen -- a run's report that lands closed
// rather than in the inbox, still on the task for anybody who looks,
// and no capability or isolation given back.
//
// template.agent_model and schedule.agent_model (grain/task-1608) are
// the ordinary case, not that one. Empty is "nobody chose a model", and
// the team's default model answers it at dispatch
// (docs/connections-and-models.md), so a settings file merged from
// before the columns puts a template's or a schedule's work back on its
// team's default model. That is a preference reverted, not a reach:
// a model is served only by connections the team already has, so the
// revert grants nothing. TestASettingsFileWithoutModelsWarnsTheyGoBack
// ToTheTeamDefault pins the warning and the revert.
//
// template.interactive is the ordinary case too. 0 is what every
// template meant before the column existed, so a settings file merged
// from before it turns a template's tasks back into unattended work:
// they run without the chat tool rather than talking with somebody, and
// nothing is granted.
//
// grain_config.console_sessions (grain/task-415) is the same borderline
// shape one setting over: its empty default means "this deployment has
// never chosen", which is read as the shipped director
// (model.ConsoleSessionsOr), so a settings file merged from before the
// column gives a deployment that had deliberately removed its standing
// sessions a director back on the next cycle. The revert is visible --
// there is an entry in the rail again, and a task filed under it -- and
// it takes away no capability, since a session's grants are in the same
// reverted value.
//
// Since the loss is accepted rather than fixed, the pull request that
// would cause it is told about it: `grain state check` warns for a
// column missing from a file of one of these tables, and for those alone
// (check.go's settingsResets). It can say which default the column would
// land on and not which value that would be instead of -- the check runs
// against a throwaway database, not the deployment's -- which is why it
// is a warning for a reviewer to weigh rather than a failure.
//
// grain_config.max_auto_submit_depth and max_auto_submit_fan_out
// (grain/task-120) default to 2 and 5, which means something --
// model.AutoSubmitLimits, the bound on how far a tree of proposals
// nobody approved may grow -- so a settings file merged from before
// those columns takes a deployment that had raised either back to
// grain's own numbers. It is the one revert on this list worth having
// anyway: the direction is toward the tighter bound, and what a reverted
// bound costs is a click on a proposal that would otherwise have been
// queued, where every other revert here costs the opposite. The zero
// those columns never land on is the reason -- a default of 0 would read
// as "nobody chose" and resolve back to 2 and 5 anyway, one layer
// further along, so the DDL says the same thing the Go does.
//
// grain_config.sandbox_cpu_overcommit, sandbox_memory_overcommit and
// sandbox_memory_reserve_mb (grain/task-197) are the same shape one more
// time, and on the same list for the same reason: they default to 2, 1
// and -1, which are grain's own budget rather than a zero, so a settings
// file merged from before those columns takes a deployment that had
// widened its budget back to what grain promises. The direction is
// toward the tighter bound, like the two above -- what a reverted budget
// costs is a CI job waiting for memory it would otherwise have been
// given -- and the zero the DDL never lands on is the reason: a ratio of
// 0 means "bound this dimension by the run counts alone", which is the
// unbounded reading and the one revert here that would be worth failing
// over.
//
// grain_config.ci_run_history, ci_step_output_mb, ci_step_output_days
// and ci_run_output_kb (grain/task-269) are the same shape again, with
// the zero doing the opposite work: 0 there means "grain's own default"
// rather than a bound of nothing, so a settings file merged from before
// those columns takes a deployment that had raised how long it keeps its
// CI history back to what grain ships. What the revert costs is a log
// somebody wanted to read next week, never a verdict -- the run records
// and every job's conclusion are bounded by the same reverted number and
// are the half that was already generous -- which is why this one is a
// warning to weigh like the rest rather than a failure.
//
// grain_config.ci_cache_mb, ci_cache_entries, ci_cache_days,
// ci_artifact_mb, ci_artifact_run_mb, ci_artifact_file_mb and
// ci_artifact_days (grain/task-317) are that group's neighbours and read
// the same way: 0 means "grain's own default", so a settings file merged
// from before those columns takes a deployment that had widened what its
// CI keeps on disk back to what grain ships. What the revert costs is a
// cache entry that has to be produced again and an artifact somebody
// meant to download, never a verdict -- and the direction, as for the
// budget's three above, is toward the tighter bound on a disk this
// deployment dispatches out of. A warning to weigh, like the rest.
//
// grain_config.ci_jobs_per_run, ci_age_boost_ms and ci_reserve_after_ms
// (grain/task-325) are that same zero once more: 0 in the ordering
// windows means "grain's own window" and 0 in ci_jobs_per_run means no
// per-run cap, so a settings file merged from before them costs a
// deployment that had tuned its queue nothing except the tuning -- jobs
// wait in a different order for a tick, and none of them is refused or
// lost.
//
// grain_config.base_ci_red_streak (grain/task-1034) is that zero again:
// 0 means grain's own streak of two (model.Config.BaseCIRedStreakOr), so
// a settings file merged from before it takes a deployment that had
// lengthened the streak back to two consecutive red tips. What that
// costs is a red confirmed, and the pipeline steward woken, one tip
// sooner than the deployment asked -- never a red missed.
//
// grain_config.actions_cache_enabled (grain/task-357) is on this list
// with grain_config.network_access, and for its reason rather than for
// the retention columns' reason: it defaults to 1, and what it turns
// back on is not a preference but a sharing decision -- every branch of
// a repository reads what every other branch's CI wrote
// (model.Config.ActionsCacheEnabled). A settings file merged from
// before the column therefore gives that back to a deployment that had
// deliberately switched it off, silently, on the next tick. Its
// neighbour actions_cache_size_gb is the ordinary shape instead: 0
// there means "grain's own default", so reverting it moves a disk bound
// and costs a cold build at worst.
//
// The half the check cannot reach is said here instead, by the one
// process that knows both sides. Applying reads the running values of
// the columns the arriving file leaves out, and writes a journal line
// naming each one it actually replaced, what it held and what replaced
// it (resets.go). Only the ones that changed: a column already sitting
// on its default loses nothing, and the check has to warn about it blind
// precisely because from a throwaway database that case and this one
// look the same.
//
// The alternative -- having ImportTables leave a column the file does
// not mention alone -- was considered and not taken. It would make the
// same file mean two different things depending on which path read it:
// Load's restore replaces whole rows (importInto is the one body), so a
// pre-upgrade dump restored onto a new host would land the DDL defaults
// while a merge of that same dump kept whatever the database held. And
// it would make a settings file a patch rather than the settings, which
// is not what this repository says it is -- "the merged files replace
// what is in its database, so a deleted row is a deleted row" (README).
// Changing that is a decision about what a merged deletion means, not a
// tightening of this one.
var SettingsTables = []string{
	// The deployment's own configuration row, prompt extension included.
	"grain_config",
	// Per-repo configuration: default capabilities, prompt extension,
	// setup command.
	"repo_config",
	// The grain CI workflows this deployment declares for a repo it does
	// not control (model.RepoWorkflow). Settings in the same sense
	// repo_config is -- operator config, written through grain's own
	// settings surface and never by the daemon -- and on this list for
	// the same reason: a workflow somebody got merged should be what the
	// next run of that repo's CI uses, not what it uses after a restart.
	"repo_workflow",
	// A repo's qualification plan -- the config and its items, not the
	// runs the reconciler creates from them.
	"qualification_config",
	"qualification_item",
	"qualification_item_depends_on",
	// The custom fields this deployment adds to its own tasks
	// (model.CustomField). Settings by construction: the declarations
	// are proposed in a pull request against this repository and
	// nowhere else, and grain never writes one for itself, so a merged
	// change is live on the next tick rather than on the next restart.
	// The *values* tasks carry for them are task data and stay off this
	// list with task and task_tag -- the values a schedule or a template
	// puts on what it files are settings like the rest of that
	// declaration, and are on it (schedule_custom_field,
	// template_custom_field).
	"custom_field",
	// The deployment's own agent API scope sets (model.APIScopeSet,
	// docs/agent-api.md): written by hand, by a person or an agent, in a
	// pull request against tables/api_scope_set.json, validated by the
	// state check (statecheck.APIScopeSets) and live on the next tick --
	// installed whole, or not at all, by model.InstallAPIScopeSets.
	"api_scope_set",
	// Templates, with the repos they read, the extra repos their
	// firings may have work landed in, the capabilities they grant, the
	// inputs they ask for when somebody instantiates them
	// (model.TemplateField) and the values their firings carry for this
	// deployment's own declared fields (model.Template.CustomFields).
	"template",
	"template_read",
	"template_write",
	"template_grant",
	"template_field",
	"template_custom_field",
	"template_sequence",
	// Suites and the templates they run, but not suite_run and its
	// own tables: a suite is settings, a run of one is not.
	"suite",
	"suite_item",
	"suite_sequence",
	// Boards: the saved dashboards the nav rail offers (model.Board),
	// the columns a Kanban one carries, the panels a "panels" one is
	// assembled from (model.BoardPanel) and the sections a "report" one
	// prints (model.BoardSection). Settings in the fullest
	// sense of this list -- a board exists in the store rather than in a
	// browser precisely so that making one is a pull request against
	// this table, which an agent can open and this list is what makes
	// live without a restart.
	"board",
	"board_column",
	"board_panel",
	"board_section",
	"board_sequence",
	// Schedules, likewise with their reads, grants and the values every
	// firing carries (model.Schedule.CustomFields). next_run_at and
	// last_run_at live on this row and are written by the firing loop,
	// which is the one place a settings table is also grain's own record
	// -- a schedule imported here can therefore lose a firing time to
	// whatever the dump had. That is the same answer a restart gives
	// today, and it costs at most one early or late firing; the
	// alternative is not being able to change a schedule at all without
	// one.
	"schedule",
	"schedule_read",
	"schedule_write",
	"schedule_grant",
	"schedule_custom_field",
	"schedule_sequence",
	// Long-standing sandboxes (model.StandingSandbox, grain/task-126):
	// sandboxes defined in grain/config, backed by Granule OCI images,
	// and configured with secrets resolved from Grain's secret store.
	"standing_sandbox",
	"standing_sandbox_secret",
	"standing_sandbox_env",
	// Persistent sandboxes (model.PersistentSandbox): staging
	// environments run from a repository checkout, exposed on a host port
	// or under the UI's /staging/<name>/.
	"persistent_sandbox",
	// The deployment's GCP keys (model.GCPKey); team_gcp_key's until
	// schema 23.
	"gcp_key",
}

var memoryTables = []string{"memory", "memory_document"}

// Apply pulls, and makes what arrived live in a database that is already
// running: a settings change an agent got merged takes effect on the
// next tick rather than on the next restart (bwsalmon/grain#184).
//
// Only SettingsTables are imported, and that restriction is the whole
// design. Load's whole-database Import is exactly what a merged deletion
// needs and exactly what cannot run here: it clears task and task_run
// along with everything else, underneath a reconcile loop holding the
// ids it would delete. Within the settings tables the replacement is the
// same one, so a merge that deleted a template deletes it here too.
//
// The commit is recorded as loaded even though only part of it was
// imported. That is deliberate: the alternative is a restart re-importing
// the whole of a commit whose settings are already live, which would
// throw away every task filed since it arrived. What the database holds
// for its own tables wins, and the next export writes it back out.
//
// What it imports is decided by the same marker Load reads -- the commit
// this host last loaded or wrote -- rather than by whether this
// particular Pull brought something down. The two differ exactly when it
// matters: an import that failed on one tick (the database was busy, a
// merged row would not insert) has left the working tree at a commit
// nothing has taken up, and the next tick's Pull, having nothing further
// to fetch, would report no news and let the export write over it.
// Against the marker, that tick tries the import again.
//
// What the import took away is said on its way past, in both of the ways
// it can take something. A merged file that does not mention a column
// resets it, and applySettings (resets.go) reads those values while they
// still exist and names the ones that changed -- in this host's journal,
// and on the pull request the merge came in on where the deployment can
// reach it (Config.AnnounceResets). A merged file that does mention a
// column replaces whatever this database has done to it since the last
// export, which is how a host that was ahead of its own dump gets quietly
// put back; every row that differed is named in the journal too
// (replaced.go).
//
// And one thing the import takes that it does not keep: an edit to the
// content of one of grain's own schedules, which is a cache of a template
// that lives in code and is written back over on the next reconcile cycle
// (orchestrator.reconcileSystemSchedules). That change lands here and is
// undone a tick later, so it is said on the same two surfaces as a reset
// -- with the sentence a reader of it actually needs, which is that the
// change belongs in a pull request against grain itself
// (systemschedules.go, Config.AnnounceSystemScheduleEdits).
//
// Reports whether anything was imported. Nothing to apply is
// (false, nil), which is the ordinary case on almost every tick.
func Apply(ctx context.Context, r *Repo, db *sql.DB, version int) (bool, error) {
	if _, err := r.Pull(ctx); err != nil {
		return false, err
	}
	head, err := r.Head(ctx)
	if err != nil {
		return false, fmt.Errorf("%w: %w", ErrNotApplied, err)
	}
	marker, err := r.loadedHead(ctx)
	if err != nil {
		return false, fmt.Errorf("%w: %w", ErrNotApplied, err)
	}
	// head == marker is the ordinary tick: the repository is exactly
	// where this host left it. An empty head is a repository with no
	// commits yet, and an empty marker is a working tree Load has not
	// decided about -- neither is Apply's to import, and Load is where
	// both are answered.
	if head == "" || marker == "" || marker == head {
		return false, nil
	}
	// A repository with no dump in it has nothing to say about the
	// database -- an initial commit holding only a README, say. Left
	// unrecorded on purpose: the next Sync exports over it and records
	// the commit it makes, which is the same seeding path a fresh
	// repository already takes.
	if !HasDump(r.Dir()) {
		return false, nil
	}
	found, err := ReadSchemaVersion(r.Dir())
	if err != nil {
		return false, fmt.Errorf("%w: %w", ErrNotApplied, err)
	}
	switch {
	case found > version:
		return false, fmt.Errorf("%w: %w: repository is at schema %d, this build knows %d",
			ErrNotApplied, ErrSchemaTooNew, found, version)
	case found < version && found > 0:
		return false, fmt.Errorf("%w: %w: repository is at schema %d, this build knows %d",
			ErrNotApplied, ErrSchemaTooOld, found, version)
	case found == 0:
		if _, err := os.Stat(filepath.Join(r.Dir(), TablesDir)); err == nil {
			return false, fmt.Errorf("%w: %w: repository is at schema %d, this build knows %d",
				ErrNotApplied, ErrSchemaTooOld, found, version)
		}
	}
	// A database with nothing in it is not one to make a settings change
	// live in, and refusing here is what keeps the export below honest
	// rather than a nicety. The settings tables are a small part of the
	// dump; importing them into an empty database would leave a database
	// that holds templates and no tasks -- populated enough that sync's
	// own refusal (ErrDatabaseEmpty) no longer applies, and the very next
	// export would commit an empty task.json over a repository that holds
	// them. Reported as ErrNotApplied so cmd/grain's cycle stops before
	// that export, exactly as it does for a dump it could not read.
	//
	// The whole repository is what such a database wants, and a start is
	// where it gets it (Load). Not here: Apply runs underneath a live
	// reconcile loop, and replacing every row is not something to do with
	// runs in flight holding the ids.
	empty, err := databaseIsEmpty(ctx, db)
	if err != nil {
		return false, fmt.Errorf("%w: %w", ErrNotApplied, err)
	}
	if empty {
		if table, has := dumpHasRows(r.Dir()); has {
			return false, fmt.Errorf("%w: %w (%s holds rows this database does not)",
				ErrNotApplied, ErrDatabaseEmpty, DumpPath(table))
		}
	}
	if err := applySettings(ctx, r, db, head, marker); err != nil {
		return false, fmt.Errorf("%w: %w", ErrNotApplied, err)
	}
	if err := r.setLoadedHead(ctx, head); err != nil {
		return true, err
	}
	return true, nil
}

// Load makes the repository and the database agree, in whichever
// direction has something to say.
//
// Which direction that is, is the whole of this function, and getting it
// wrong loses data. The repository is the source of truth for changes
// made *to the repository* -- a merged pull request against a template,
// a commit someone pushed while grain was down -- and importing one is
// how those take effect. But the database is the source of truth for
// everything grain itself did, and it is routinely ahead: the export
// runs on a timer, so every write since the last tick exists only in the
// database. A process that imported unconditionally at start would
// therefore undo whatever the previous one did in its last few seconds,
// which is exactly what a container stopped shortly after a task was
// filed looks like.
//
// So the question is not "does the repository hold a dump" but "has the
// repository moved since this host last agreed with it". The commit grain
// last loaded or wrote is recorded beside the repository (loadedHead),
// inside the git directory so it is never committed and never travels to
// the remote:
//
//   - no dump at all: seed the repository from the database. A brand new
//     install, or a running grain pointed at a fresh empty repository.
//   - no marker: this working tree was cloned onto a host that has never
//     loaded it. Import all of what it holds -- which is what makes
//     "clone the repository onto a new machine" a restore of this
//     deployment's configuration. Not of what it did: the repository
//     carries no operational table (ExportConfig), so a clone brings
//     back the settings a deployment was running on and nothing else.
//   - HEAD is not the commit we recorded: something arrived. Import the
//     settings out of it, exactly as Apply does on a tick, and leave the
//     database's own record of what grain did alone.
//   - otherwise: the repository is exactly where we left it, so the
//     database is authoritative and nothing is imported. The next sync
//     exports whatever has happened since.
//
// There is a second question underneath all of that, and the marker
// cannot answer it: whether there is a database here at all. The marker
// is a fact about the repository and this host's agreement with it, and
// it lives in the git directory -- so a store that went away takes none
// of it with it. Deleted, or on a volume restored from a backup that did
// not include it, or simply opened at a path that has never held one: the
// working tree is untouched, HEAD is exactly the commit this host last
// wrote, and the answer above is "nothing to import" onto an empty
// database. The sync after that would export it and push a commit
// deleting the deployment from its own off-host copy, and no check
// further out would object, because the remote is not ahead -- this host
// wrote the commit under it.
//
// So a database with nothing in it takes the whole repository, marker or
// no marker. It is the same case the missing marker already is, arrived
// at from the other side: there is no database ahead of the dump to
// protect, so importing all of it can cost nothing and gains back
// everything the dump holds -- the restore this repository exists to
// make possible, done by the deployment rather than by an operator who
// has to know to move the working tree aside first. "Nothing in it"
// means exactly that: no row in any table, bar the schema stamp every
// database has from the moment it is created (databaseIsEmpty). A
// database with a single task in it is a database this host is
// responsible for, and it takes the ordinary path.
//
// A failure to reach the remote does not stop any of that. Everything
// above is decided against the working tree, and a tree this host has
// already loaded is still a tree this host can load again, so a fetch
// that failed leaves the load going ahead on what is on disk and comes
// back as an error marked ErrUnreachable -- loaded, but out of touch,
// which a daemon reports and goes on running. The two exceptions are
// ErrNoLocalCopy, where there is no tree to fall back on, and every
// failure that is about the repository's contents rather than about
// reaching it: a dump at a schema this build cannot read, rows that
// would not import, a history that has diverged. Those come back
// unmarked and are a deployment that must stop -- see cmd/grain's own
// run() for the other half of that decision.
func Load(ctx context.Context, r *Repo, db *sql.DB, version int) error {
	// unreachable, once set, is what this returns at the end of an
	// otherwise successful load: the caller is owed the fact that the
	// repository it is running on is however stale the last fetch left
	// it, even though nothing here failed to do its job.
	var unreachable error
	if _, err := r.Pull(ctx); err != nil {
		if !errors.Is(err, ErrUnreachable) {
			return err
		}
		if empty, _ := r.isEmpty(ctx); empty {
			return fmt.Errorf("%w: %v", ErrNoLocalCopy, err)
		}
		unreachable = err
	}
	if !HasDump(r.Dir()) {
		if err := Seed(ctx, r, db, version); err != nil {
			// A seed ends in a push, and a push cannot land while the
			// remote is out of reach. The commit it made is on disk either
			// way and the sync loop puts it out with the first push that
			// works, so this stays the unreachable remote it started as
			// rather than becoming a failure to seed.
			if unreachable == nil {
				return err
			}
			return fmt.Errorf("%w (seeding it locally got as far as: %v)", unreachable, err)
		}
		return unreachable
	}
	// Checked before anything else, and whether or not this ends up
	// importing: a dump at a schema this build does not know is a
	// deployment that must stop and say so, not one that quietly exports
	// today's schema over yesterday's rows -- or, in the other direction,
	// over a newer build's.
	//
	// Unconditional, including on the one host where the answer is to
	// write over the dump anyway: a schema change that has already moved
	// this deployment's store aside. Nothing visible from here tells that
	// host apart from one restoring a repository an older build wrote, so
	// it is asked rather than guessed at, and Reformat (reformat.go) is
	// where the caller that was told the answer acts on it.
	found, err := ReadSchemaVersion(r.Dir())
	if err != nil {
		return err
	}
	switch {
	case found > version:
		return fmt.Errorf("%w: repository is at schema %d, this build knows %d", ErrSchemaTooNew, found, version)
	case found < version:
		return fmt.Errorf("%w: repository is at schema %d, this build knows %d", ErrSchemaTooOld, found, version)
	}
	head, err := r.Head(ctx)
	if err != nil {
		return err
	}
	marker, err := r.loadedHead(ctx)
	if err != nil {
		return err
	}
	// Whether there is a database here to protect, asked before the
	// marker is allowed to answer anything: a host can agree with the
	// repository exactly and have nothing to run on, because the marker
	// survives the store it was written beside (this function's own doc
	// comment, and databaseIsEmpty).
	empty, err := databaseIsEmpty(ctx, db)
	if err != nil {
		return err
	}
	// The ordinary restart: the repository is exactly where this host left
	// it, and this host has a database of its own to run on. Nothing is
	// imported, and the next sync exports whatever has happened since.
	if marker != "" && marker == head && !empty {
		return unreachable
	}
	// Which tables the import replaces depends on which of three cases
	// this is. The marker tells the first two apart; the third is the one
	// it cannot see.
	//
	// No marker at all is a working tree this host has never loaded -- a
	// clone onto a new machine, the restore case -- and there the
	// repository is the only copy of anything it holds, so all of it is
	// imported. What that restores is this deployment's configuration and
	// no more, because that is all the repository carries
	// (ExportConfig): a clone brings back the settings a deployment was
	// running on, and says nothing about what it did. Nothing below
	// applies to this case -- there is no database ahead of the dump to
	// protect.
	//
	// A database with nothing in it is that same case with the marker
	// still in place: a store that was lost while its working tree
	// survived. It takes the whole repository too, and for the identical
	// reason -- an empty database is not ahead of anything -- which is why
	// it is decided here rather than left to the marker, whose answer
	// ("this repository has not moved, import nothing") would come up on
	// an empty database and then export it back over the dump.
	//
	// A marker that disagrees with HEAD is a repository that moved under a
	// host that already has a database -- a merged pull request, or a
	// working tree RecoverDiverged reset onto the remote's branch -- and
	// there only the settings are taken, exactly as Apply takes them on a
	// tick.
	//
	// This used to replace the whole state tier, and that lost data every
	// time. The dump is whatever the last export wrote, so the database is
	// ahead of it on the state tier by up to an export interval as a
	// matter of design; replacing that tier deletes every task, comment,
	// attachment, grant, branch and release written since. The window is
	// half a minute in the ordinary case and however long the pushes had
	// been failing in the recovered one, and an operator lands in it by
	// habit rather than by accident: merge a settings pull request,
	// restart grain to make it take effect, lose whatever was filed in the
	// half minute before the stop.
	//
	// Nothing worth having is given up by narrowing it. A merged edit to
	// task or task_run no longer takes effect at a start -- but it never
	// took effect reliably, because the tick that pulled it down (Apply)
	// imported the settings out of it and recorded the commit as loaded,
	// so whether an edit to grain's own record survived came down to
	// whether the process happened to restart inside the same half minute.
	// A deterministic no-op the README can state is a better answer than a
	// coin toss that eats rows, and it is the answer the rest of this
	// package already gives: the database is authoritative for what grain
	// itself did, the repository is authoritative for settings, and the
	// next export writes the database's version back out over a merged
	// change to anything else. The import a restart does is now the import
	// a tick does.
	if marker == "" || empty {
		log.Printf("staterepo: loading %s as the deployment's state repository; importing its settings", r.Dir())
		if err := ImportTables(ctx, db, r.Dir(), append(append([]string{}, SettingsTables...), memoryTables...)); err != nil {
			return err
		}
	} else {
		if err := applySettings(ctx, r, db, head, marker); err != nil {
			return err
		}
	}
	if err := r.setLoadedHead(ctx, head); err != nil {
		return err
	}
	return unreachable
}

// LoadExternal pulls the external config repository and imports its settings
// and memory tables into db without ever seeding, exporting, committing, or
// pushing to origin.
func LoadExternal(ctx context.Context, r *Repo, db *sql.DB, version int) error {
	var unreachable error
	if _, err := r.PullExternal(ctx); err != nil {
		if !errors.Is(err, ErrUnreachable) {
			return err
		}
		if empty, _ := r.isEmpty(ctx); empty {
			return fmt.Errorf("%w: %v", ErrNoLocalCopy, err)
		}
		unreachable = err
	}
	if !HasDump(r.Dir()) {
		return unreachable
	}
	if found, err := ReadSchemaVersion(r.Dir()); err == nil {
		switch {
		case found > version:
			return fmt.Errorf("%w: repository is at schema %d, this build knows %d", ErrSchemaTooNew, found, version)
		case found < version:
			return fmt.Errorf("%w: repository is at schema %d, this build knows %d", ErrSchemaTooOld, found, version)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	head, err := r.Head(ctx)
	if err != nil {
		return err
	}
	if head == "" {
		return unreachable
	}
	log.Printf("staterepo: loading external config repository %s (%s); importing its settings", r.Remote(), r.Branch())
	if err := ImportTables(ctx, db, r.Dir(), append(append([]string{}, SettingsTables...), memoryTables...)); err != nil {
		return err
	}
	if err := r.setLoadedHead(ctx, head); err != nil {
		return err
	}
	return unreachable
}

// SyncExternal pulls from the external config repository and imports any new
// commits (or forces a re-import when force is true) into db without ever
// exporting, committing, or pushing to origin.
func SyncExternal(ctx context.Context, r *Repo, db *sql.DB, force bool) (bool, error) {
	pulled, err := r.PullExternal(ctx)
	if err != nil {
		return false, err
	}
	head, err := r.Head(ctx)
	if err != nil || head == "" {
		return false, err
	}
	if !HasDump(r.Dir()) {
		return false, nil
	}
	marker, err := r.loadedHead(ctx)
	if err != nil {
		return false, err
	}
	if !force && !pulled && marker == head {
		return false, nil
	}
	if err := ImportTables(ctx, db, r.Dir(), append(append([]string{}, SettingsTables...), memoryTables...)); err != nil {
		return false, fmt.Errorf("%w: %v", ErrNotApplied, err)
	}
	if err := r.setLoadedHead(ctx, head); err != nil {
		return true, err
	}
	return true, nil
}

// Seed writes the database out as the repository's first content: the
// dump, the schema stamp, the README that tells whoever opens the
// repository next what they are looking at, and the CI step that checks
// what they propose to change in it.
func Seed(ctx context.Context, r *Repo, db *sql.DB, version int) error {
	if err := writeReadme(r.Dir(), deploymentName(ctx, db)); err != nil {
		return err
	}
	if err := ExportConfig(ctx, db, r.Dir()); err != nil {
		return err
	}
	if err := WriteSchemaVersion(r.Dir(), version); err != nil {
		return err
	}
	if _, err := r.Commit(ctx, "Initialise grain's state repository\n\n"+
		"grain exports its database here as JSON, one file per table, so its\n"+
		"settings can be read and changed through pull requests like anything\n"+
		"else. See README.md."); err != nil {
		return err
	}
	if err := r.recordLoadedHead(ctx); err != nil {
		return err
	}
	// The build that wrote the repository's first content is the build
	// the next sync compares itself against, so a deployment that seeds
	// and is then upgraded says so on its first export afterwards -- and,
	// as importantly, one that seeds and is not upgraded says nothing.
	if err := r.recordExportedBuild(ctx); err != nil {
		return err
	}
	if err := r.Push(ctx); err != nil {
		return err
	}
	// After the seed's own commit has reached the remote, and as a commit
	// of its own: a remote that will not take a file under
	// .github/workflows (installWorkflow says why that happens) must not
	// be able to take the dump down with it, and the undo that handles
	// that case needs a commit to come back to.
	_, err := r.installWorkflow(ctx)
	return err
}

// ErrRemoteAhead is returned by Sync and SyncAll when the remote holds
// commits this deployment has not taken up -- a merged pull request against grain's
// own settings, which is the mechanism this whole package exists to
// allow.
//
// Apply, above, is what handles that in the ordinary case: a tick pulls
// the merge down and makes its settings live. This is the tick where
// that did not happen -- a history that will not fast-forward, or a
// merge that landed between the pull and the push -- and it is a refusal
// to export rather than a failure to push, which is the whole point:
// exporting would commit this deployment's dump on top of the merge, and
// no amount of retrying afterwards could get that commit onto a remote
// that has moved past it. The database is still the live state and grain
// goes on running against it; what is waiting is loaded at the next
// start, whole, because that import replaces every row and is not
// something to do underneath runs holding ids (Load, above).
var ErrRemoteAhead = errors.New("the state repository has changes this deployment has not loaded; " +
	"restart grain to apply them")

// Sync writes the database out and commits and pushes anything that
// changed, reporting whether there was anything to commit.
//
// What goes out is the editable configuration and nothing else
// (ExportConfig), so a call with no settings change behind it writes the
// same bytes as the last one and commits nothing. That is what keeps
// this repository growing with the data rather than with the clock: a
// busy deployment that nobody has reconfigured commits nothing at all,
// however many tasks it files and runs it records, because none of that
// is in here.
//
// The commit message is deliberately plain and identical every time.
// What changed is in the diff, which is the whole reason the dump is
// text; a message that tried to summarise it would be a second, worse
// answer that could disagree with the first.
//
// With one exception, which is the one thing a reader cannot get from
// the diff: the first export written by a build of grain that is not the
// build that wrote the last one. That commit names both builds and the
// schema they agree on, because it is the only commit here whose diff
// can be bigger than the activity behind it -- a new build writes the
// dump the way its own code does. See exportCommitMessage.
//
// A push that failed is not the last word on it either: every call
// pushes whatever this host holds that the remote has not got, whether
// or not it had anything of its own to commit, so an outage costs the
// remote the delay and nothing else. The reported bool stays what it
// says -- a call that only carried an earlier commit out to the remote
// committed nothing and reports false -- and a push that goes on failing
// comes back as an error every time rather than once.
func Sync(ctx context.Context, r *Repo, db *sql.DB, version int) (bool, error) {
	return sync(ctx, r, db, version, "")
}

// SyncAll is what a human means by `grain state sync` or the pane's own
// Sync button, and what a clean shutdown owes the repository on its way
// out.
//
// It is Sync exactly, and the name is kept because "sync everything" is
// what those two callers are asking for and what they go on getting. It
// used to differ: an ordinary tick left grain's own churn -- runs,
// leases, observations -- for an hourly clock, and this wrote that out
// whether or not it was due, so that a shutdown left nothing unwritten.
// Since grain/task-395 none of it is written here at all, so there is
// nothing for an explicit sync to force and no clock for it to overrule.
func SyncAll(ctx context.Context, r *Repo, db *sql.DB, version int) (bool, error) {
	return sync(ctx, r, db, version, "")
}

// SyncNoted is SyncAll for the one caller whose export is not the
// deployment going about its business: a command that has just rewritten
// the database wholesale with the daemon stopped, and knows something
// about the commit that the diff cannot say.
//
// note is a whole commit message -- a subject line, a blank line, a
// paragraph -- and it replaces the plain subject an export otherwise
// carries. Everything else about the cycle is SyncAll's, the "Written by
// grain" line and the upgrade wording included (exportCommitMessage).
//
// The case it exists for is `grain repo-case -apply`, whose export is the
// other commit in this repository whose diff can be far bigger than the
// activity behind it: every row keyed on a repository, rewritten at once,
// because an operator reconciled a spelling rather than because the
// deployment did anything. A reviewer reading that diff a month later has
// no way to tell it from a deployment that went mad, and the subject line
// is the cheapest place to tell them.
func SyncNoted(ctx context.Context, r *Repo, db *sql.DB, version int, note string) (bool, error) {
	return sync(ctx, r, db, version, note)
}

func sync(ctx context.Context, r *Repo, db *sql.DB, version int, note string) (bool, error) {
	// One ls-remote, before anything is written, answering both of this
	// cycle's questions about the remote (remoteState, staterepo.go): is
	// it ahead of us, which decides whether we may export at all, and are
	// we ahead of it, which decides whether we push at the end.
	//
	// Asked before anything is written so that a merged change is never
	// committed over -- see ErrRemoteAhead. An unreachable remote answers
	// "nothing is known" and the push below reports the network in its
	// own words. It guards both entry points: asking for a sync
	// explicitly (SyncAll) is no more a reason to commit over a merge
	// than the timer's own tick is.
	remote := r.remoteState(ctx)
	if remote.ahead {
		return false, ErrRemoteAhead
	}
	// The other export this must not do, and the one no check on the
	// remote can catch: a database with nothing in it, written over a
	// repository that holds a deployment. The remote is not ahead in that
	// case -- this host wrote the commit under the dump -- so the commit
	// fast-forwards and the push lands, and the off-host copy is gone. See
	// ErrDatabaseEmpty for how a host gets into that state and why the
	// answer here is to stop rather than to restore: the restore is a
	// start's to do (Load), because it replaces every row.
	//
	// Both halves of the question are needed and the cheap half comes
	// first. An empty database is not on its own a reason to refuse: a
	// deployment nobody has configured yet, seeded into a repository of
	// its own, is empty at both ends and has to go on exporting. What is
	// refused is the pair -- nothing here, something there.
	empty, err := databaseIsEmpty(ctx, db)
	if err != nil {
		return false, err
	}
	if empty {
		if table, has := dumpHasRows(r.Dir()); has {
			// DumpPath rather than tables/<table>.json: the first file
			// dumpHasRows finds rows in is very often a settings table,
			// and those live in a nested document now. A refusal that
			// named tables/template.json would send an operator looking
			// for a file the export has not written since grain/task-393.
			return false, fmt.Errorf("%w (%s holds rows this database does not)",
				ErrDatabaseEmpty, DumpPath(table))
		}
	}
	// The CI step, before anything is exported and as a commit and a push
	// of its own (installWorkflow, format.go). Every sync, not only at
	// Seed, for the reason the README below is rewritten every sync: a
	// repository an operator adopted, or one a merge dropped the file out
	// of, has to end up with it anyway. Unlike the README it is written
	// only when it is missing, because it is a file an operator may edit
	// and grain must not fight them for.
	installed, err := r.installWorkflow(ctx)
	if err != nil {
		return installed, err
	}
	// And, once an hour and only where something can answer, whether that
	// step is a step that runs at all (workflowhealth.go). Installing the
	// file is as far as the paragraph above can see: a check GitHub
	// concludes before a job of it starts is committed, green-looking in
	// git, and failing every pull request against this deployment's own
	// settings on every tree. Best effort and no error of its own -- the
	// export below is the thing this tick is for.
	r.observeWorkflowHealth(ctx)
	// And, where an operator has asked for it, the check itself: grain
	// importing each open pull request's tree on this host and posting
	// the answer as a check run (postcheck.go). At most one import a
	// tick, so what this costs the export below is bounded, and every
	// failure of it leaves a check that never finishes rather than a pull
	// request nobody checked -- the gate this closes fails closed, which
	// is the whole of the design.
	r.postChecks(ctx)
	// Rewritten on every sync, not only at Seed: a repository an operator
	// adopted, or one whose README a merge dropped, still has to explain
	// itself to whoever opens it next, and this is the cheapest place to
	// make that true rather than a thing that is true only if the
	// repository was created here.
	//
	// Read from the database on every sync for the same reason it is
	// written on every sync: a deployment that has just been named, or
	// renamed, says so in the README on the next tick rather than at the
	// next restart. The name only ever changes through a settings change,
	// which is a commit of its own anyway, so this costs no churn.
	if err := writeReadme(r.Dir(), deploymentName(ctx, db)); err != nil {
		return installed, err
	}
	now := r.now()
	if err := ExportConfig(ctx, db, r.Dir()); err != nil {
		return installed, err
	}
	if err := WriteSchemaVersion(r.Dir(), version); err != nil {
		return installed, err
	}
	// Which build last exported here, read before this cycle's commit
	// overwrites the answer. It is what turns "the dump changed" into
	// "the dump changed because this deployment was upgraded" for the one
	// commit where those are different facts (exportCommitMessage).
	previousBuild := r.exportedBuild(ctx)
	// installed rides along in every answer from here on: a sync that
	// committed the CI step and exported nothing has still changed the
	// repository, and a caller that logs "nothing to do" over a commit it
	// just pushed would be describing the wrong tick.
	changed, err := r.Commit(ctx, exportCommitMessage(previousBuild, r.cfg.Build, version, now, note))
	if err != nil {
		return installed, err
	}
	if changed {
		// Recorded before the push, not after: the commit exists either
		// way, and a push that fails (an expired credential, an
		// unreachable remote) must not leave the next start believing the
		// repository has moved on without it and importing its own stale
		// dump back over a database that is ahead.
		if err := r.recordLoadedHead(ctx); err != nil {
			return true, err
		}
	}
	// After the marker that matters, because this one only decides how a
	// future commit is worded and the loaded-head marker decides whether
	// a future start imports its own dump back over a live database.
	if err := r.noteExportedBuild(ctx, previousBuild, changed); err != nil {
		return installed || changed, err
	}
	// Pushed when this host holds a commit the remote has not got, which
	// is a different question from whether this cycle made one -- and the
	// difference is a commit that never leaves the host.
	//
	// A push that failed used to be retried only by the next cycle that
	// had something of its own to commit. On a busy deployment that is
	// the next one and nobody notices. On an idle one there is no such
	// cycle at all: nothing changes a setting, so every export from here
	// on is byte-identical, Commit reports nothing to do, and the commit sits
	// here until the deployment next does something -- which may be
	// hours, and is exactly the window in which a merged pull request
	// turns it into the divergence diverge.go exists to clear. The pane
	// went quiet over it too, since a cycle that pushed nothing reported
	// nothing.
	//
	// Asking the remote instead retries on the very next tick, and goes
	// on reporting the failure for as long as it lasts. It costs nothing
	// extra to ask: the answer is the one already fetched at the top of
	// this function.
	if !changed && !remote.behind {
		return installed, nil
	}
	if err := r.Push(ctx); err != nil {
		return installed || changed, err
	}
	r.maintain(ctx)
	return installed || changed, nil
}

// exportCommitMessage is what an export commit says: the same plain
// message every time, and the one that says an upgrade happened.
//
// previous is the build recorded as having last exported into this
// working tree and current is Config.Build, so the upgrade wording needs
// both to be known and to differ. Anything else -- a host that has never
// exported here, a caller that does not know its own build, the ordinary
// tick -- is the plain message, because every one of those would
// otherwise announce an upgrade that did not happen, and a message that
// cries upgrade on every commit tells a reader nothing on the commit
// where it is true.
//
// What the upgrade wording is for is the question a reviewer of this
// repository cannot answer out of the diff. Every other commit here is
// the deployment doing something -- a task filed, a setting changed --
// and its diff is that. This one is the first dump a new build wrote,
// and a new build writes the dump the way its own code does: a column
// that has appeared on a table, a value spelled another way, a table
// exported that was not before. Those arrive for every row at once, in a
// commit that otherwise looks like a deployment that did an enormous
// amount in thirty seconds. Naming both builds is what lets a reviewer
// go and read the difference between them.
//
// version -- the schema this build knows -- is stated because it is the
// other version a reader wants and because the answer is always "the
// same one": grain refuses to load a dump stamped with a schema it does
// not know, so a build whose schema differed could not have got as far
// as exporting here at all. A schema change takes the other path
// instead: the working tree is moved aside to be seeded afresh, or --
// where a remote holds the old dump and hands it straight back --
// reformatted over in a commit of its own that says so (reformat.go,
// scripts/setup.sh, and Load's refusal above). So a schema change is
// never what this commit is.
//
// note is the one thing a caller may say for itself, and only SyncNoted
// passes one: a subject and a paragraph of its own, for an export that is
// not the deployment going about its business. It replaces the subject
// and keeps everything else, upgrade wording included -- a build that
// upgraded and then had its first export made by such a caller has still
// upgraded, and noteExportedBuild retires that fact on this commit
// whether or not it is worded here.
func exportCommitMessage(previous, current string, version int, now time.Time, note string) string {
	written := "Written by grain from its own database at " + now.UTC().Format(time.RFC3339) + "."
	message := "Update grain state"
	if note != "" {
		message = note
	}
	message += "\n\n" + written
	if previous == "" || current == "" || previous == current {
		return message
	}
	if note != "" {
		message += "\n\nIt is also the first export by an upgraded grain, which is worth\nsaying for the reason below."
	} else {
		message = "Update grain state, on the first export by an upgraded grain\n\n" + written
	}
	return message + "\n\n" +
		"This deployment has been upgraded since it last wrote here:\n\n" +
		"    from " + previous + "\n" +
		"    to   " + current + "\n\n" +
		"and this is the first export the new build has made. It is worth\n" +
		"saying because it is the one commit in this repository whose diff can\n" +
		"be larger than the half minute of activity behind it: a build writes\n" +
		"the dump the way its own code does, so a column that has appeared on\n" +
		"a table, or a value spelled another way, arrives here for every row at\n" +
		"once rather than because the deployment did anything.\n\n" +
		"The schema is not what moved. Both builds write schema " + strconv.Itoa(version) + ": grain\n" +
		"refuses to load a dump stamped with a schema it does not know, so a\n" +
		"build that wrote another one could not have exported over this dump\n" +
		"at all -- a schema change seeds or reformats this repository in a\n" +
		"commit that says so instead (see README.md). Every commit after this\n" +
		"one says \"Update grain state\" again."
}

// noteExportedBuild records this build as the one that last exported
// here -- with the one delay that makes the message above worth having.
//
// An upgrade that has not been said yet is not recorded. A sync that
// found nothing to commit made no commit to say it in, and recording the
// new build there would retire the fact silently: the deployment would
// have been upgraded, and the first commit that did carry the new
// build's dump would say nothing about it. Left where it is, the next
// commit -- whenever the deployment next does something -- carries the
// wording, which is exactly the commit a reviewer would want it on.
//
// Everything else records immediately, committed or not. A host with no
// marker at all is establishing a baseline rather than reporting a move
// (a fresh seed, a clone, the first run of a build that knows its own
// name), and there is nothing to say about it.
func (r *Repo) noteExportedBuild(ctx context.Context, previous string, committed bool) error {
	if r.cfg.Build == "" || r.cfg.Build == previous {
		return nil
	}
	if previous != "" && !committed {
		return nil
	}
	return r.recordExportedBuild(ctx)
}

// deploymentName is what this deployment calls itself, for the README:
// grain_config.environment_name, read out of the very database the dump
// is written from.
//
// Every way of not having an answer is "" -- the unnamed deployment,
// whose README is the one grain has always written. A build whose schema
// predates the column, a deployment with no config row because nobody
// has opened Settings yet, a NULL left by a hand-edited dump, a read
// that lost to something else holding the database: none of those is a
// reason to fail an export over a line of documentation, and each of
// them means the same thing to a reader anyway.
//
// Queried rather than taken through model.Store because this package
// deliberately knows the database as tables and columns rather than as
// grain's types (dump.go) -- and because Seed and sync are handed the
// *sql.DB and nothing else.
func deploymentName(ctx context.Context, db *sql.DB) string {
	var name string
	if err := db.QueryRowContext(ctx,
		"SELECT `environment_name` FROM `grain_config` WHERE `id` = 1").Scan(&name); err != nil {
		return ""
	}
	return name
}

// deploymentNameFromDump is deploymentName for the caller that has no
// database to ask: `grain state format` runs in a clone (format.go),
// where the config row is a file like every other table.
//
// Without this, formatting a clone of a named deployment's repository
// would write the unnamed README over the named one -- a diff for a
// human to commit, and grain putting it straight back on its next sync
// -- and Format's own promise that a directory already formatted comes
// out byte-identical would stop being true there. A directory with no
// dump in it (the bootstrap case: format an empty repository, then point
// a deployment at it) has nothing to name, and the seed that follows
// writes the name in.
func deploymentNameFromDump(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, TablesDir, "grain_config.json"))
	if err != nil {
		return ""
	}
	var rows []struct {
		EnvironmentName string `json:"environment_name"`
	}
	if err := json.Unmarshal(data, &rows); err != nil || len(rows) == 0 {
		return ""
	}
	return rows[0].EnvironmentName
}

func writeReadme(dir, deployment string) error {
	return writeFileIfChanged(filepath.Join(dir, ReadmeFile), []byte(readme(deployment)))
}

// readme is the README's text for a deployment called deployment, which
// is model.Config.EnvironmentName -- free text an operator sets, and
// what the pane already labels itself with.
//
// "" is the unnamed deployment, and renders the README grain has always
// written, byte for byte. That matters more here than the naming line
// does: this file is rewritten on every sync, so a rendering that moved
// so much as a space for a deployment with nothing to say would be a
// commit and a push on the next tick of every installation that upgrades
// past this build.
func readme(deployment string) string {
	return readmeIntro + deploymentParagraph(deployment) + readmeBody
}

// deploymentParagraph names the deployment whose database this is, or
// says nothing at all.
//
// It is what nothing else in the tree says. The dump is the same files
// with the same tables in the same shape whoever wrote it, so a clone,
// or a checkout handed to an agent that never saw the deployment it came
// from, cannot tell one installation's state from another's -- which is
// the reading a human is most likely to get wrong on a fleet of them,
// and the one an agent has no way of checking. A run dispatched by grain
// itself is told separately (orchestrator's settingsRepoSection); this
// is for every other way somebody ends up looking at these files.
//
// The name is quoted rather than pasted in, and both bounds below are
// about what can reach this function. environment_name is validated on
// the way through the UI (ui.UpdateSettings: trimmed, 32 runes, no line
// breaks or tabs), but grain_config is a settings table, so a merged
// pull request against this very repository can put anything at all in
// that column and grain will write it back out here on the next sync.
// strconv.Quote is what keeps a newline or a control character in that
// value from turning one line of this file into several, and the
// truncation is what keeps a pasted paragraph from taking the README
// over. Neither is markdown-aware and neither needs to be: what is left
// is a quoted string on a line of its own, where the worst a stray
// backtick or asterisk can do is italicise some prose.
func deploymentParagraph(deployment string) string {
	name := strings.TrimSpace(deployment)
	if name == "" {
		return ""
	}
	if rs := []rune(name); len(rs) > maxDeploymentNameRunes {
		name = string(rs[:maxDeploymentNameRunes])
	}
	return `
This is the deployment called ` + strconv.Quote(name) + `.
` + "`grain_config.environment_name`" + ` is where that name comes from -- the
same name the deployment's own pane shows. Nothing else in this tree
says whose rows these are: every deployment exports the same files,
holding the same tables in the same shape, so a clone of this repository
-- or a checkout of it handed to somebody who did not clone it -- is
otherwise indistinguishable from any other deployment's.
`
}

// maxDeploymentNameRunes bounds what deploymentParagraph will print,
// deliberately the same bound ui.maxEnvironmentNameLen applies on the
// way in rather than a second opinion about how long a name may be: a
// value longer than this did not come through the UI, and a README is
// no better a place for a paragraph than the sidebar badge is.
const maxDeploymentNameRunes = 32

const readmeIntro = `# grain state

This repository is grain's editable configuration and memory, written out as text.
`

const readmeBody = `
grain runs against an embedded SQLite database, and exports its editable
configuration and memory here after each change.

Templates, suites and schedules -- the three things people actually edit
-- are written as nested documents in ` + "`" + SettingsDir + `/` + "`" + `, in the same format a
repository writes its own under ` + "`" + `.grain` + "`" + `: one entry per object, hyphenated
keys, a cadence as ` + "`" + `daily: {at: "09:00"}` + "`" + ` and a template's reads, grants and
inputs nested inside it rather than in tables of their own. Everything
else is a database dump: ` + "`" + TablesDir + `/<table>.json` + "`" + `, one file per table,
rows sorted by primary key and columns in the table's declared order so
that an unchanged database always produces byte-identical files.

Editable configuration and memory are written within seconds of changing.
grain's own running record of what it did -- ` + "`" + TablesDir + `/task_run.json` + "`" + `, ` + "`" + `task_observation.json` + "`" + `
and ` + "`" + `lease.json` + "`" + ` -- is not exported: those rows change on nearly every reconcile cycle,
nobody sends a pull request against them, and committing them would
rewrite the largest files here forever. Tasks, attempts and CI runs live
only in SQLite on the grain server until a separate backup mechanism is
added.

## What is here

- ` + "`" + SettingsDir + `/` + "`" + ` -- templates, suites and schedules, nested, one
  document per kind. ` + "`" + `id` + "`" + ` is grain's to allocate and is what history and
  the other settings refer an object by, so changing one is a delete and
  a create rather than a rename; everything else is yours.
- ` + "`" + TablesDir + `/` + "`" + ` -- the rest of the settings (repo configuration,
  prompt extensions, custom fields and boards), one file per table.
  Nothing else is here: grain's own record of what it did is not
  exported. Script bodies
  are references to editable files under scripts/ and workflows/; memory
  bodies are Markdown files under memory/.
- ` + "`" + ReleaseNotesDir + `/` + "`" + ` -- one Markdown file per week, ` + "`" + `<year>-W<week>.md` + "`" + `,
  of what this deployment shipped: the notes grain's own weekly job
  writes, exported from ` + "`" + TablesDir + `/release_note.json` + "`" + ` the way memory
  bodies are. Written by grain, like everything else here, so a change
  to one is overwritten rather than kept.
- ` + "`" + SchemaVersionFile + "`" + ` -- the schema the dump was written by. A grain
  that knows a different one refuses to import rather than guessing.

Which tables are settings, and which are grain's own record of what it
did, is the distinction to hold on to before changing anything.
` + "`" + `template` + "`" + `, ` + "`" + `suite` + "`" + ` (with ` + "`" + `suite_item` + "`" + `), ` + "`" + `board` + "`" + ` (with
` + "`" + `board_column` + "`" + `, ` + "`" + `board_panel` + "`" + ` and ` + "`" + `board_section` + "`" + `), ` + "`" + `repo_config` + "`" + `,
` + "`" + `schedule` + "`" + `, ` + "`" + `custom_field` + "`" + ` and ` + "`" + `grain_config` + "`" + `, plus the
` + "`" + `_read` + "`" + `/` + "`" + `_grant` + "`" + `/` + "`" + `_sequence` + "`" + `
tables belonging to them, are settings. ` + "`" + `task` + "`" + `, ` + "`" + `task_run` + "`" + `,
` + "`" + `task_comment` + "`" + `, ` + "`" + `task_observation` + "`" + `, ` + "`" + `lease` + "`" + `, ` + "`" + `branch` + "`" + `, ` + "`" + `release` + "`" + ` and
their like are observations: grain writes them, and a change to one is
either overwritten by the next export or kept as a record of something
that never happened.

One row is both. A ` + "`" + `schedule` + "`" + ` whose id starts ` + "`" + `system:` + "`" + ` is one of grain's
own standing jobs, seeded rather than written by anybody here: how often
it runs, what it does about an overlapping firing and whether it is
enabled at all are this deployment's, and they are settings like any
other. ` + "`" + `schedule.title` + "`" + ` and ` + "`" + `schedule.body` + "`" + ` are not. They are a copy of a
prompt that ships in grain's own code and is re-read every time it fires, so
editing them here changes what this file says and nothing about what
runs -- and grain writes its own text back over them on the next tick,
which is also how they catch up after an upgrade changes the prompt. A
change to one of those jobs is a pull request against grain itself.

## Changing something

Open a pull request against this repository the way you would against
any other. On merge, grain pulls -- within half a minute, without a
restart -- and the merged files replace what is in its database, so a
deleted row is a deleted row.

Whole rows, not the lines you touched. A column a settings row does not
mention is not left as it was; it lands on its default, which for
` + "`" + `grain_config.time_zone` + "`" + ` is a zone rather than nothing. A branch taken
before grain gained a column and merged after it is exactly that, so
` + "`" + `grain state check` + "`" + ` warns for a settings row missing a column and says
which value would land -- add the column back with the value the
deployment is running on, if that is not the one you want. A few settings
columns have no default to land on at all, and for those the check fails
rather than warns: nothing can import a row that leaves one out, so the
file has to name them.

That check runs before the merge and against a throwaway database, so it
can name the default a column would land on and not the value it would
land on instead of. grain says the other half afterwards, once the merge
reaches it: when a merge resets a settings value the deployment was
actually running on, it says which file, which row, which column, what it
held and what replaced it -- in its own journal, and, where it has a
GitHub credential that may comment there, back on the pull request the
merge came in on, since by then that value exists nowhere else. A column
whose value was already the default is passed over -- nothing was lost --
so a merge nothing is said about is a merge that cost nothing.

That applies to the settings tables, and to them only: ` + "`" + `grain_config` + "`" + `,
` + "`" + `repo_config` + "`" + `, ` + "`" + `custom_field` + "`" + `, the ` + "`" + `template` + "`" + `, ` + "`" + `suite` + "`" + `,
` + "`" + `board` + "`" + `,
` + "`" + `schedule` + "`" + ` and
` + "`" + `qualification` + "`" + ` tables. The rest -- tasks, runs, metrics: grain's own
record of what it did -- is never replaced from here, at a restart no
more than on a tick. The database is the live copy of those rows and is
always ahead of these files by however long it is since the last export,
so importing them would delete whatever grain had written since. Editing
one of them here does nothing; the next export writes the database's
version back over your change.

The exception is a restore, and it is the only one: a start with nothing
to lose brings back every table this repository holds -- a clone of it
onto a host that has never loaded it, or a start on a host whose
database has gone away and left this directory behind, which is the same
situation from the other side. What comes back is the configuration,
which is all that is here: a restored deployment is configured the way
this one is and has no record of anything this one did. grain will not
do the reverse: an export from a database with nothing in it, over a
dump that holds rows, is refused and reported instead of committed.

That check already runs on your pull request. ` + "`" + WorkflowFile + "`" + `
runs ` + "`" + `grain state check` + "`" + `, which loads this whole directory into a
throwaway database and reports what breaks, so a red tick means grain
would not have loaded your change -- and a malformed file or a row
missing a required column is caught there rather than when the daemon
next starts, which is the worst place to find out. ` + "`" + `grain state check .` + "`" + `
is the same thing in a terminal.

grain writes that workflow itself, whenever it is not here, and never
rewrites one that is: edit it -- pin the image, change the runner, add a
step -- and what you wrote is what stays. If it is missing and does not
come back, the deployment's credential is not allowed to push files
under ` + "`" + `.github/workflows` + "`" + ` (its journal says so, and
` + "`" + `grain state ci .` + "`" + ` in a clone writes the file for a human to commit),
or the operator has turned it off with ` + "`" + `"noWorkflow": true` + "`" + `.

Do not hand-edit while grain is running unless you mean it: grain is the
only writer, exports on a timer, and a local edit it did not make is
overwritten by the next export.

## Secrets are not here

grain's secrets are encrypted, and they are not in this repository. They
sit beside the private key on the machine grain runs on, under
` + "`" + `<data-dir>/secrets` + "`" + `, which is the directory an operator backs up -- the
key was never here either, so a copy of this repository never could
decrypt anything.

They used to be here, as ` + "`" + SecretsFile + "`" + `. This repository is somewhere
agents are dispatched to work now, and everything a sandbox can clone is
everything a sandbox can read; ciphertext an agent can carry off is
still ciphertext an agent can carry off. If that file appears anywhere
in this repository's history, grain refuses to let any sandbox reach it
at all -- see the "State repository" section of grain's README.
`

// EnsureIgnored writes a .gitignore that keeps the things which are not
// state out of the repository, for the case where the working tree
// shares a directory with anything else. Small, and worth having
// written down rather than assumed: a stray editor swap file committed
// into the state repository would be pushed to the remote.
//
// A file that is already there is added to rather than replaced. It used
// to be left exactly as it was, which was fine while this list never
// changed; it does now (secrets.enc joined it), and a repository created
// by an earlier build would otherwise never learn the new line. Whatever
// an operator added themselves stays.
func EnsureIgnored(dir string) error {
	const body = "# Written by grain.\n" +
		"*.swp\n" +
		".DS_Store\n" +
		// The private key never belongs here, and naming it makes an
		// operator who drops one in this directory by mistake fail to
		// commit it rather than push it to a remote.
		"*.key\n" +
		"secrets.key\n" +
		// Nor does the encrypted file any more: this repository is
		// somewhere agents are dispatched to work now, and everything a
		// sandbox can clone is everything a sandbox can read. The
		// ciphertext lives beside the key under <data-dir>/secrets --
		// see SecretsFile -- and this line is what keeps a copy left
		// behind by an older build, or by a hand that put one here, from
		// being committed back.
		SecretsFile + "\n"
	path := filepath.Join(dir, IgnoreFile)
	existing, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return writeFileIfChanged(path, []byte(body))
	}
	if err != nil {
		return fmt.Errorf("staterepo: reading %s: %w", path, err)
	}
	have := map[string]bool{}
	for _, line := range strings.Split(string(existing), "\n") {
		have[strings.TrimSpace(line)] = true
	}
	var missing []string
	for _, line := range strings.Split(strings.TrimSuffix(body, "\n"), "\n") {
		if !have[line] {
			missing = append(missing, line)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	updated := string(existing)
	if !strings.HasSuffix(updated, "\n") {
		updated += "\n"
	}
	return writeFileIfChanged(path, []byte(updated+strings.Join(missing, "\n")+"\n"))
}
