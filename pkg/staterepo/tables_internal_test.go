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

// Every table this package names as a string, checked against the tables
// the schema actually creates.
//
// The lists here -- SettingsTables, churnTables, the names the README
// spells out for whoever opens the repository -- are []string and
// map[string]bool, so the compiler has nothing to say about them, and
// what goes wrong when one drifts is silence rather than an error.
// bwsalmon/grain#672 is the worked example: a branch renamed
// task_template to template while another added SettingsTables naming
// the old one, they merged cleanly, and every template and suite dropped
// out of what Apply imports -- Import skips a table this build does not
// have on purpose, because that is what makes a dump from a newer build
// importable at all. A rename is a normal thing to do to this schema and
// two branches merging is a normal way to do it, so the check belongs
// here rather than in a reviewer's head.

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

// openSchema is a freshly Init-ed database: the schema this build
// creates, which is the only authority on what a table name means.
func openSchema(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := model.New(db).Init(context.Background()); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	return db
}

// schemaTableSet is what tableNames lists -- the same query Export and
// the whole-database Import walk -- as a set.
func schemaTableSet(t *testing.T, db *sql.DB) map[string]bool {
	t.Helper()
	names, err := tableNames(context.Background(), db)
	if err != nil {
		t.Fatalf("listing tables: %v", err)
	}
	set := map[string]bool{}
	for _, n := range names {
		set[n] = true
	}
	if len(set) == 0 {
		t.Fatal("the schema has no tables, so nothing below is checking anything")
	}
	return set
}

// Every name on SettingsTables has to be a table this build actually
// has, or Apply silently imports nothing from a file it was the whole
// point of that list to import. A rename in pkg/model is exactly how
// that goes wrong, and nothing else notices: Import skips a table it
// does not have on purpose, so the daemon comes up, applies nothing,
// and says nothing.
func TestSettingsTablesAllExist(t *testing.T) {
	t.Parallel()
	have := schemaTableSet(t, openSchema(t))
	for _, name := range SettingsTables {
		if !have[name] {
			t.Errorf("SettingsTables names %q, which this build's schema does not have", name)
		}
	}
}

// notSettings is the other half of the same decision: every table the
// schema has that SettingsTables deliberately leaves out.
//
// It is written out rather than derived so that a table added to the
// schema fails this file until somebody says which half it is in. The
// default that costs nothing to get wrong is "not settings" -- a table
// left off SettingsTables is still exported, still imported at start,
// and merely not applied live -- and the default that loses data is the
// other one, so the decision is worth making on purpose once rather than
// inheriting from whichever list a new table happens to fall into.
//
// The reasons come from SettingsTables' own doc comment: a table belongs
// here if it is grain's record of what it did (the database is
// authoritative and a live import would delete rows written since the
// dump), or if nobody proposes a change to it in a pull request.
var notSettings = []string{
	// grain's own record of what it did. Replacing any of these
	// underneath a running reconcile loop deletes the rows that loop is
	// holding ids for.
	"task",
	"task_read",
	"task_write",
	"task_filed_template",
	"task_grant",
	"task_link",
	"task_tag",
	// Which comment grain keeps on a work tree's GitHub origin, and what it
	// last said (model.WorkOriginComment): runtime bookkeeping grain writes
	// itself, never a setting.
	"work_origin_comment",
	// One task's own custom field values, written by putTask in the same
	// transaction as the task row -- task_tag's sibling, and off this
	// list for the same reason. The declarations they are values for
	// (custom_field) are settings and are on SettingsTables.
	"task_custom_field",
	"task_grant_wait",
	// An operator's pause (model.TaskPause): a person's decision about
	// one task's work right now, carried like the task's own rows.
	"task_pause",
	// A task's goal and grain's count of progress towards it
	// (model.TaskGoal): declared with the task and rewritten by
	// reconcileTaskGoals every cycle.
	"task_goal",
	"task_observation",
	"task_run",
	"task_run_tool",
	"task_run_check_wait",
	"task_run_wait_credit",
	"task_run_ci_habit",
	"task_run_api_call",
	"task_run_report_check",
	// How far the daemon has read a run host's spool
	// (model.SpoolCursor): runtime bookkeeping the daemon rewrites every
	// second while a run lives.
	"task_run_spool_cursor",
	// A file a live run attached by path, held from the tool call to
	// the run's finish (model.StagedAttachment): runtime bookkeeping the
	// finish path deletes once it has relayed it.
	"task_run_staged_attachment",
	// What a run said when it failed for a reason grain could not name
	// (model.UnmatchedFailure). grain's own record of what happened to
	// it, written after the run finished, and nothing a pull request
	// would ever propose a change to: it is a quote of somebody else's
	// error message.
	"task_run_unmatched_failure",
	// How many of a run's sandbox tool calls the cluster's exec tunnel
	// failed (model.ExecTransportFailure): grain's record of what
	// happened to the run, written after it finished.
	"task_run_exec_failure",
	// What a run's capability grants did for it, as their servers
	// reported it at release (model.CapabilityAuditRecord): grain's
	// record of what happened, written after the run finished.
	"task_run_capability_audit",
	// A run's suspension (model.Suspension): grain's record of how a run
	// stopped, written in the transaction that finished it.
	"task_suspension",
	"task_sequence",
	"lease",
	// One run of grain's own CI, its jobs and its steps
	// (model.CIRun, grain/task-303). grain's record of what it did if
	// anything is: written by the daemon as the run happens, and read
	// back by whoever asks what happened on a branch. Replacing these
	// underneath a running daemon would delete the run it is at that
	// moment writing job results into.
	"ci_run",
	"ci_job",
	"ci_step",
	"ci_step_log_chunk",
	// GitHub's CI on the same repositories (model.GitHubCIJob): grain's
	// record of what somebody else's CI reported, written as it reports.
	"github_ci_job",
	// How many sandboxes this deployment has built and how long they
	// lived (model.SandboxUsage, grain/task-315). A meter, so about as
	// far from a setting as a table gets: nobody proposes a change to a
	// count of what already happened, and importing one live would set
	// the counters back to whatever the dump was taken at -- losing every
	// sandbox counted since, which is precisely the data this table
	// exists to keep across a restart.
	"sandbox_usage",
	"branch",
	// Written by people, and still not applied live: a comment or an
	// attachment is not something a pull request against the state
	// repository proposes, and clearing the tables to reinstate them
	// would race the daemon writing new ones.
	"task_comment",
	"task_attachment",
	// Runs, as against the settings they were run from. A qualification
	// plan and a suite are settings (they are on SettingsTables); a run
	// of one is grain's record of having run it.
	"qualification_run",
	"qualification_task",
	"suite_run",
	"suite_run_item",
	"suite_run_task",
	"release",
	"release_candidate",
	// Whether this deployment is picking new work up
	// (model.PipelinePause). It is a decision somebody made -- an
	// operator taking the deployment out of service, or grain holding
	// the queue while it checks an upgrade it just installed -- and the
	// daemon writes it for itself, which is the property that keeps a
	// table off SettingsTables: a merge landing mid-pause would
	// otherwise resume a deployment nobody meant to resume, or reinstate
	// one somebody had just let go. Nothing proposes a pause in a pull
	// request either; there are three routes and a CLI flag for that.
	"pipeline_pause",
	// What grain CI last made of a branch things merge into
	// (model.BaseBranchCI, grain/task-198). Off SettingsTables for
	// pipeline_pause's own reason: the daemon writes it for itself every
	// time a merge lands, and a settings import landing in the window
	// where a base branch is red would reopen a merge queue grain had
	// just closed -- or close one it had just reopened. Nobody proposes
	// one in a pull request either; it is a verdict, not a decision.
	"base_branch_ci",
	// What grain last dispatched on a base branch after a merge, and for
	// which commit (model.BaseBranchDispatch, grain/task-409). Off
	// SettingsTables for base_branch_ci's reason and a sharper one: the
	// row is what stops grain dispatching the same expensive job every
	// cycle, so an import landing mid-dispatch would either re-ask for a
	// ~25-minute build already in flight or claim one had been asked for
	// that never was. The *setting* it acts on is a repo_config column
	// and is settings; this is grain's note of having acted on it.
	"base_branch_dispatch",
	// The trusted release run grain last started on a base branch, and
	// for which commit (model.BaseBranchRelease, grain/task-461). Off
	// SettingsTables for base_branch_dispatch's reason: the row is what
	// stops grain publishing the same tree every cycle, so an import
	// landing mid-release would either re-ask for a publish already in
	// flight or claim one had been asked for that never was. The
	// *setting* it acts on -- whether this repository releases at all --
	// is a repo_config column and is settings; this is grain's note of
	// having acted on it.
	"base_branch_release",
	// One firing of a schedule a workflow's `on: schedule` cron declares,
	// between the CI run it started and the review of that run
	// (model.ScheduledWorkflowRun, grain/task-351). Off SettingsTables
	// for base_branch_ci's reason: the daemon writes it for itself
	// between one cycle and the next, and an import landing in the
	// window between the run and its review would either resurrect a
	// night already reviewed or lose one still running -- and losing one
	// is a nightly nobody ever reads, which is the whole thing this row
	// exists to prevent. Nobody proposes a firing in a pull request
	// either; the cron in the workflow file is what a change goes to.
	"scheduled_workflow_run",
	// The speculative merge the queue is testing right now
	// (model.MergeBatch, grain/task-294). Off SettingsTables for
	// base_branch_ci's reason and more sharply: its members are held out
	// of the ordinary merge path while it runs, so an import could hold
	// changes for a batch that had already finished, or drop a batch
	// whose ref had already been built and paid for.
	"merge_batch",
	// And what every batch that has already ended cost
	// (model.MergeBatchRecord, grain/task-300). Off SettingsTables for
	// merge_batch's reason, and for a plainer one: a batch that has
	// happened is a measurement, not a decision, and nothing about it is
	// a thing a pull request against this repository would propose.
	"merge_batch_history",
	// And what every gather hold that has already ended did
	// (model.MergeGatherRecord, grain/task-440) -- the wait a lone ready
	// change takes at the front of the queue, counted. Off
	// SettingsTables for merge_batch_history's reason: a wait that has
	// already been taken is a measurement of the window, not the window.
	"merge_gather_history",
	// And what the overlap re-check said about every merge
	// (model.MergeRecheckRecord, grain/task-985). Off SettingsTables for
	// merge_batch_history's reason: a decision made is a measurement.
	"merge_recheck_history",
	// And what each red stretch of a base branch held (model.BaseHoldPass,
	// grain/task-986). Off SettingsTables for merge_batch_history's reason.
	"base_hold_history",
	// And what the queue had to do over again (model.MergeQueueContentionEvent,
	// grain/task-3227). Off SettingsTables for merge_batch_history's reason.
	"merge_queue_contention",
	// Every skipped firing of a schedule (model.ScheduleSkip,
	// grain/task-3560): a record of what the cadence did, not a decision,
	// for merge_batch_history's reason.
	"scheduled_firing_skip",
	// And every merge an operator forced with a reason
	// (model.ForceMergeRecord, grain/task-479) -- the one sanctioned
	// exception to the merge predicate, counted so that "it should be
	// rare" can be checked. Off SettingsTables for merge_batch_history's
	// reason: an override somebody has already used is a measurement,
	// and importing one would be asserting a merge that did not happen.
	"force_merge_history",
	// Tuning recommendations and tracking (model.TuningRecommendation).
	// Operational tuning state rather than a settings table.
	"tuning_recommendation",
	// The upgrade that gate is sometimes held for
	// (model.ScheduledUpgrade, grain/task-156). Off SettingsTables for
	// pipeline_pause's own reason and one more: the daemon clears this
	// row the instant it starts the upgrade, and a settings import
	// landing in that window would put an upgrade that has already begun
	// back on the books, to be applied a second time by the deployment
	// the restart brings up.
	"scheduled_upgrade",
	// The last day the daily automatic upgrade fired
	// (model.AutoUpgradeRun). Off SettingsTables for scheduled_upgrade's
	// reason: importing an older one would have a deployment schedule a
	// day's upgrade it has already applied.
	"auto_upgrade_run",
	// The usage-limit pauses in force (model.StoredUsageLimitPause,
	// grain/task-1259): runtime state, a provider's refusal of one
	// credential as this deployment met it. Importing one would stop a
	// deployment dispatching onto a key that never refused it, and
	// exporting them would churn the state repository on every limit.
	"usage_limit_pause",
	// Memory is written by agents while runs are live, so it remains
	// outside SettingsTables even though its Markdown representation is
	// imported separately when a repository change arrives.
	"memory",
	"memory_document",
	// And this deployment's weekly account of itself
	// (model.ReleaseNote), which is written by the same kind of run, at
	// the same kind of moment, and is off SettingsTables for the same
	// reason: a branch cut before Monday's job ran would otherwise
	// delete the week it published.
	"release_note",
	// The installation's identity is generated once, not an operator
	// setting. It comes back on a startup restore, but applying a proposed
	// edit live would let a state-repository branch rename the running
	// installation out from under values already uniquified with it.
	"grain_instance",
	// Where each declared template, suite and schedule came from, and
	// grain's record of polling the repositories that declare them
	// (model.RepoDeclaration, grain/task-60). Both are re-derived from
	// the repository itself every time it moves, so neither is anything
	// a pull request against this repository would propose a change to
	// -- the file in the repository is where that change goes -- and
	// importing a merged copy live would assert a provenance grain is
	// about to re-read anyway.
	//
	// The declared rows themselves are a different matter and are not
	// here: they live in `template`, `suite` and `schedule`, which are
	// on SettingsTables, so a merged settings file carries them like any
	// other row. That is harmless in the direction that matters -- the
	// next poll makes them what the repository says again -- and it is
	// what keeps a restore from losing a deployment's whole schedule
	// list until each repository has been polled once.
	"repo_declaration",
	"repo_declaration_sync",
	// A declared template's `push:` list (grain/task-2536), the same kind
	// of row again: re-read from the repository's default branch by every
	// sync, and never a setting anybody proposes.
	"repo_declaration_push",
	// A repository's path triggers (grain/task-1712) are the same kind
	// of row as repo_declaration -- re-read from the repository by the
	// next poll -- and the cursor beside them is grain's note of how far
	// along a branch it has fired them, base_branch_release's reason.
	"repo_path_trigger",
	"path_trigger_cursor",
	// And its merge rules (grain/task-1659), the same kind of row again.
	"repo_merge_rules",
	// The schema's own bookkeeping. Nothing proposes a change to the
	// version a database is at; the stamp in the repository
	// (SchemaVersionFile) is what a dump says about that, and importing
	// this table live would tell a running database it is at a different
	// schema than it is.
	"grain_schema",
	// Dynamic executor authentication tokens and their sequence allocator.
	// Generated and revoked dynamically through the UI/API, not via state
	// repo declarative settings.
	"executor_token",
	"executor_token_sequence",
	"external_agent_token",
	"external_agent_token_sequence",
	// Security audit log. Dynamic runtime records, not state repo
	// declarative settings.
	"secret_resolution_audit",
	"credential_access_audit",
	// Operator notifications (model.Notification) and the subscriptions
	// that wake durable tasks on them: grain's own running record of
	// what it raised and who has seen it, rewritten every cycle, and
	// nothing a pull request against the state repository proposes.
	"notification",
	"task_notification_subscription",
	// Each live k8s-granule sandbox's reverse-channel token hash and expiry
	// (model.SandboxAccessCredential): ephemeral runtime state written when
	// a sandbox is minted and deleted when it is released or reaped.
	"sandbox_access",
}

// The converse of TestSettingsTablesAllExist, and the one that catches a
// table added to the schema rather than a table renamed out from under a
// list: every table has to be on exactly one of the two lists. A new
// settings-shaped table that nobody put on SettingsTables is a settings
// change an operator merges and grain never applies -- the #672 failure
// again, arrived at by addition instead of by rename.
func TestEveryTableIsEitherSettingsOrDeliberatelyNot(t *testing.T) {
	t.Parallel()
	have := schemaTableSet(t, openSchema(t))
	classified := map[string]bool{}
	for _, name := range SettingsTables {
		classified[name] = true
	}
	for _, name := range notSettings {
		if classified[name] {
			t.Errorf("%q is on both SettingsTables and notSettings", name)
		}
		classified[name] = true
		if !have[name] {
			t.Errorf("notSettings names %q, which this build's schema does not have: "+
				"drop it, or fix the name it was renamed to", name)
		}
	}
	var missing []string
	for name := range have {
		if !classified[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("the schema has tables neither list mentions: %s\n"+
			"Decide for each: settings a merged pull request should apply to a running "+
			"daemon go on staterepo.SettingsTables (bind.go), and everything else on "+
			"notSettings in this file.", strings.Join(missing, ", "))
	}
}

// The whole-database path -- Export, and the Import a fresh clone does
// at startup -- takes its tables from the database rather than from a
// list here, and that is the property worth pinning: it is why nothing
// above has to enumerate the non-settings tables for those two to be
// complete. A dump with a table missing from it restores a database with
// that table empty, and nothing would say so.
//
// Every table has a file; not every table has a file of its own. The
// three settings kinds and their companions are carried by one nested
// document each (decl.go), so what is checked for those is that the
// document exists and that DumpPath agrees about which file a table's
// rows are in -- which is the same claim, since every reader in this
// package finds a table's rows through DumpPath.
func TestExportWritesAFileForEveryTableTheSchemaHas(t *testing.T) {
	t.Parallel()
	db := openSchema(t)
	have := schemaTableSet(t, db)
	dir := t.TempDir()
	if err := Export(context.Background(), db, dir); err != nil {
		t.Fatalf("exporting: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, TablesDir))
	if err != nil {
		t.Fatalf("reading the dump: %v", err)
	}
	written := map[string]bool{}
	for _, e := range entries {
		written[strings.TrimSuffix(e.Name(), ".json")] = true
	}
	nested := declTables()
	for name := range have {
		if nested[name] {
			if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(DumpPath(name)))); err != nil {
				t.Errorf("the export wrote no %s for %q, so a clone of the repository restores it empty",
					DumpPath(name), name)
			}
			if written[name] {
				t.Errorf("%q is carried by %s and was written to %s/ as well; two files for one table is "+
					"two answers to what the table holds", name, DumpPath(name), TablesDir)
			}
			continue
		}
		if LocalTables[name] {
			if written[name] {
				t.Errorf("%q is on LocalTables and was exported anyway", name)
			}
			continue
		}
		if !written[name] {
			t.Errorf("the export wrote no file for %q, so a clone of the repository restores it empty", name)
		}
	}
}

// Every table a settings document carries is a settings table, and every
// table it does not is not carried by one.
//
// The first half is what makes the documents safe to write: they are
// exported whole on the state tier and imported whole by Apply, so a
// table in one that Apply did not import would be a file a merge could
// change and nothing would read. The second is the pair of it -- a
// settings table that quietly stopped being in any document would go
// back to tables/ with no file written for it at all.
func TestEveryTableInASettingsDocumentIsASettingsTable(t *testing.T) {
	t.Parallel()
	settings := map[string]bool{}
	for _, name := range SettingsTables {
		settings[name] = true
	}
	for name := range declTables() {
		if !settings[name] {
			t.Errorf("%q is carried by %s and is not on SettingsTables, so a merged change to it "+
				"would never be applied", name, DumpPath(name))
		}
	}
}

// The README is the dump's own documentation, and it names tables in
// prose -- which is a list of table names like any other, read by
// whoever opens the repository and by the agents dispatched to work in
// it. A stale name here sends somebody to edit a file nothing reads
// (#672 left `task_template` in it for exactly as long as it left it in
// SettingsTables).
func TestTheReadmeNamesTablesTheSchemaHas(t *testing.T) {
	t.Parallel()
	have := schemaTableSet(t, openSchema(t))
	// Backticked words that are not tables and are not meant to be. Kept
	// short on purpose: a word in backticks in this README is a table
	// name unless it is one of these.
	notATable := map[string]bool{
		"tables":        true, // TablesDir, the directory the files are in
		"settings":      true, // SettingsDir, where the nested documents are
		"id":            true, // a column of the settings documents, not a table
		"qualification": true, // the family, naming its tables collectively
	}
	// Rendered for a named deployment, which is the longer of the two
	// READMEs: the naming line is prose about the dump like any other and
	// names grain_config in it (bind.go's deploymentParagraph).
	for _, quoted := range regexp.MustCompile("`([^`]+)`").FindAllStringSubmatch(readme("staging"), -1) {
		// tables/task_run.json and task_run both mean the table; a path
		// with a placeholder in it, a filename that is not a table's and
		// anything with a space in it mean something else and are skipped
		// by the shape test below.
		name := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSuffix(quoted[1], "/"), TablesDir+"/"), ".json")
		if !regexp.MustCompile(`^[a-z][a-z0-9_]*$`).MatchString(name) || notATable[name] {
			continue
		}
		if !have[name] {
			t.Errorf("the README names %q as a table and this build's schema has no such table: "+
				"rename it there too, or add it to notATable in this test if it is not a table name",
				name)
		}
	}
}
