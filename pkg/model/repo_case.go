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

// repo_case.go is the backfill grain/task-32 deliberately left undone.
//
// That change made ui.(*Client).parseRepo spell every repo name
// *entering* the store the way this deployment already spells it -- the
// target repo allowlist first, then the default target -- and touched
// nothing already written down. So a task filed before it, a repo_config
// row an operator wrote under another capitalisation, a qualification
// plan, a release, a requested branch: each stays keyed under whatever
// case it was typed in, still reads as a repo of its own on the repos
// page, and still misses the per-repo defaults keyed under the
// deployment's spelling. This is the one-shot reconciliation that fixes
// those rows, and `grain repo-case` (../../pkg/cli/repocase.go) is the
// only thing that calls it.
//
// Three decisions are worth naming, because each is a judgement rather
// than a mechanism:
//
//   - Nothing here runs by itself. Store.Init does not call it, and the
//     daemon does not either. This rewrites primary keys in an
//     operator's own database, on evidence (the allowlist) that an
//     operator edits from a browser -- a deployment that has just had a
//     repo added to its allowlist in a new capitalisation would
//     otherwise silently rewrite history on its next restart. Reporting
//     is the default and applying is a flag, so somebody reads what
//     would change before anything does.
//
//   - Applying is all or nothing. Every table is reconciled inside one
//     transaction, and any row this cannot merge mechanically (below)
//     abandons the whole of it: an operator resolves the disagreement by
//     hand and runs it again, rather than being left with a database
//     half in one spelling and half in the other.
//
//   - Merging is conservative, and refuses rather than guesses. Where
//     both spellings hold a row for one key, a set-valued column is
//     unioned (repo_config.default_capabilities, which is exactly how
//     that field already composes with the deployment-wide default: a
//     repo adds and never subtracts), a column one side leaves empty
//     takes the other side's value, and two different non-empty values
//     for one column are a conflict this reports and does not resolve.
//     Concatenating two prompt extensions, or OR-ing two qualification
//     switches together, would each be grain inventing configuration
//     nobody wrote.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ErrRepoCaseConflict is returned by ApplyRepoCase when at least one row
// could not be merged mechanically. Nothing was written: the plan
// returned alongside it holds every conflict, and every change that
// would have been made once they are resolved.
var ErrRepoCaseConflict = errors.New("repo case: rows in two spellings could not be merged")

// errRepoCaseRollback abandons the reconciliation transaction on
// purpose. Reporting is the default mode, and a report that wrote
// anything would not be one -- so the report path does the whole of the
// work (which is what makes its counts real) and then throws it away.
var errRepoCaseRollback = errors.New("repo case: rolling back")

// RepoCaseChange is one table's share of the rewriting, for one repo in
// one divergent spelling.
type RepoCaseChange struct {
	Table string
	// From is the spelling stored today; To is the deployment's own.
	From RepoRef
	To   RepoRef
	// Rewritten is rows whose repo columns move to To's spelling with
	// nothing else about them changing.
	Rewritten int
	// Merged is rows the canonical spelling already had a row for, folded
	// into it and dropped -- the same task reading both spellings of one
	// repo, or two repo_config rows for what is one repo.
	Merged int
}

func (c RepoCaseChange) String() string {
	s := fmt.Sprintf("%s: %s -> %s, %d row(s) rewritten", c.Table, c.From, c.To, c.Rewritten)
	if c.Merged > 0 {
		s += fmt.Sprintf(", %d merged into a row already there", c.Merged)
	}
	return s
}

// RepoCaseConflict is one row this cannot decide for itself: both
// spellings say something, and they disagree.
type RepoCaseConflict struct {
	Table string
	From  RepoRef
	To    RepoRef
	// Row names which row, for a table holding more than one per repo --
	// a qualification item's template id, a release's name. Empty for a
	// table with exactly one row per repo.
	Row string
	// Detail is what disagrees, in a sentence an operator can act on.
	Detail string
}

func (c RepoCaseConflict) String() string {
	where := c.Table
	if c.Row != "" {
		where += " (" + c.Row + ")"
	}
	return fmt.Sprintf("%s: %s vs %s: %s", where, c.From, c.To, c.Detail)
}

// RepoCasePlan is what a reconciliation would do, or has just done.
type RepoCasePlan struct {
	Changes   []RepoCaseChange
	Conflicts []RepoCaseConflict
}

// Empty reports whether this deployment has nothing stored in a
// divergent case at all -- the answer every deployment hopes for, and
// the one that makes running this a no-op rather than a migration.
func (p RepoCasePlan) Empty() bool { return len(p.Changes) == 0 && len(p.Conflicts) == 0 }

// Rows is how many stored rows the plan touches, across every table.
func (p RepoCasePlan) Rows() int {
	n := 0
	for _, c := range p.Changes {
		n += c.Rewritten + c.Merged
	}
	return n
}

// PlanRepoCase reports what reconciling this store against canonical
// would change, and writes nothing.
//
// canonical is the deployment's own spellings, in the order
// ui.(*Client).canonicalRepo consults them: the target repo allowlist
// first, then the default target. A repo neither names has no canonical
// spelling to move to and is left exactly as stored -- the same answer
// parseRepo gives a repo typed into a deployment that has never named
// it.
func (s *Store) PlanRepoCase(ctx context.Context, canonical []RepoRef) (RepoCasePlan, error) {
	return s.reconcileRepoCase(ctx, canonical, false)
}

// ApplyRepoCase performs the rewriting PlanRepoCase describes, in one
// transaction.
//
// It returns ErrRepoCaseConflict, having written nothing at all, when
// any row could not be merged mechanically: the returned plan says which
// rows, and the whole of it is worth reading again after they are
// resolved by hand.
func (s *Store) ApplyRepoCase(ctx context.Context, canonical []RepoRef) (RepoCasePlan, error) {
	return s.reconcileRepoCase(ctx, canonical, true)
}

func (s *Store) reconcileRepoCase(ctx context.Context, canonical []RepoRef, apply bool) (RepoCasePlan, error) {
	var plan RepoCasePlan
	err := s.write(ctx, "reconcile repo case", func(tx *sql.Tx) error {
		// A retried attempt starts from an empty plan: write retries the
		// whole function when it loses the write lock, and a plan
		// accumulated across two attempts would double-count.
		plan = RepoCasePlan{}
		if err := reconcileRepoCase(ctx, tx, canonical, &plan); err != nil {
			return err
		}
		if !apply || len(plan.Conflicts) > 0 {
			return errRepoCaseRollback
		}
		return nil
	})
	if err != nil && !errors.Is(err, errRepoCaseRollback) {
		return RepoCasePlan{}, err
	}
	if apply && len(plan.Conflicts) > 0 {
		return plan, ErrRepoCaseConflict
	}
	return plan, nil
}

func reconcileRepoCase(ctx context.Context, tx *sql.Tx, canonical []RepoRef, plan *RepoCasePlan) error {
	for _, t := range repoCaseTables {
		stored, err := t.spellings(ctx, tx)
		if err != nil {
			return err
		}
		for _, from := range stored {
			to, ok := canonicalSpelling(canonical, from)
			if !ok {
				continue
			}
			if err := t.reconcile(ctx, tx, from, to, plan); err != nil {
				return err
			}
		}
	}
	return nil
}

// canonicalSpelling is ui.(*Client).canonicalRepo's rule, applied to a
// repo already stored: the first canonical entry naming this repository,
// when it spells it differently. An entry that spells it identically is
// no change at all and reports false, which is the case every row in a
// healthy deployment is in.
func canonicalSpelling(canonical []RepoRef, stored RepoRef) (RepoRef, bool) {
	for _, c := range canonical {
		if !c.Same(stored) {
			continue
		}
		if c.Owner == stored.Owner && c.Name == stored.Name {
			return RepoRef{}, false
		}
		return c, true
	}
	return RepoRef{}, false
}

// repoCaseTable describes one table's repo key well enough to rewrite
// it. Every repo-keyed table in schema.go is here; a new one is a new
// entry rather than new code, which is the point of the shape.
type repoCaseTable struct {
	name string
	// owner and repo name the two columns holding the repository. They
	// differ table to table -- (target_owner, target_name) on a task,
	// (owner, repo) on a release, (owner, name) on everything keyed the
	// way repo_config is -- which is exactly why this is data.
	owner string
	repo  string
	// keyed is whether owner/repo are part of this table's primary key,
	// so that rewriting a row can collide with one the canonical
	// spelling already holds. False for a table with an id of its own,
	// where the repo columns are ordinary columns and two rows for one
	// repo are already normal.
	keyed bool
	// rest is the remainder of the primary key when keyed: what decides
	// whether a divergent row and a canonical one are the same row.
	rest []string
	// values is the columns merge is handed and may rewrite. Empty means
	// the key is the whole of what the row says, so two rows with the
	// same key are the same fact twice and the divergent one is simply
	// dropped.
	values []string
	// merge folds a divergent row into the canonical row it collides
	// with, returning the canonical row's new values -- or a conflict
	// sentence, which abandons the entire reconciliation. Required when
	// values is non-empty.
	merge func(canonical, divergent []string) ([]string, string)
	// identity names columns that are not part of any primary key, but
	// that grain reads as though (repo, identity) were unique. Rewriting
	// the repo of a row whose identity the canonical spelling already
	// uses would create the ambiguity that read cannot see past, so it is
	// a conflict rather than a merge.
	identity []string
}

// repoCaseTables is every table in schema.go holding a repository, in
// the order a report reads best: the task tree, the authored content
// that files tasks, the release machinery, and the per-repo
// configuration this whole exercise is about.
var repoCaseTables = []repoCaseTable{
	{name: "task", owner: "target_owner", repo: "target_name"},
	// A task reading one repo under both spellings read it twice; after
	// the rewrite it reads it once, which is what the primary key here
	// already meant.
	{name: "task_read", owner: "owner", repo: "name", keyed: true, rest: []string{"task_id"}},
	{name: "task_write", owner: "owner", repo: "name", keyed: true, rest: []string{"task_id", "base"}},
	// The write target a run was dispatched against, pinned at dispatch
	// from the task's own so that a mid-run edit cannot move it
	// (schema.go's task_run). Rewriting the spelling is not such an edit:
	// it names the same repository, the one reader that could refuse a
	// push over it compares case-insensitively already
	// (gitproxy.canonicalMatches), and this runs against a stopped daemon
	// with no live run to disturb. What it buys is a run history that
	// spells the repo the way the task it belongs to now does.
	{name: "task_run", owner: "target_owner", repo: "target_name"},
	{name: "schedule", owner: "target_owner", repo: "target_name"},
	{name: "schedule_read", owner: "owner", repo: "name", keyed: true, rest: []string{"schedule_id"}},
	{name: "schedule_write", owner: "owner", repo: "name", keyed: true, rest: []string{"schedule_id", "base"}},
	{name: "template", owner: "target_owner", repo: "target_name"},
	{name: "template_read", owner: "owner", repo: "name", keyed: true, rest: []string{"template_id"}},
	{name: "template_write", owner: "owner", repo: "name", keyed: true, rest: []string{"template_id", "base"}},
	// GetRelease reads a release by (repo, name) and expects one, so two
	// releases of the same name under two spellings cannot be rewritten
	// into one repo without deciding which of them is the release --
	// which is not a decision available from here.
	{name: "release", owner: "owner", repo: "repo", identity: []string{"name"}},
	// A candidate's owner/repo duplicate its release's own, so they move
	// with it; the candidate's identity is its release_id, which no
	// rewriting here touches.
	{name: "release_candidate", owner: "owner", repo: "repo"},
	// No identity check: schema.go's own note on `branch` says two rows
	// may already name one branch, since whether the name collides is a
	// fact only GitHub holds.
	{name: "branch", owner: "owner", repo: "repo"},
	{name: "suite_run", owner: "owner", repo: "repo"},
	{name: "qualification_run", owner: "owner", repo: "name"},
	{
		name: "repo_config", owner: "owner", repo: "name", keyed: true,
		// Every column of the row, and TestRepoCaseCoversEveryRepoConfigColumn
		// is what keeps it that way: a column missing from this list is
		// one whose value is silently dropped when two spellings of a
		// repository are folded together, which is how clone_url,
		// unfixable_checks and base_branch_ci_jobs came to be missing
		// from it before grain/task-340 noticed.
		values: []string{"default_capabilities", "prompt_extension", "setup_command", "ci_capabilities", "release_workflows_disabled", "release_secrets", "declarations", "require_branch_up_to_date", "ignore_github_ci", "watch_default_branch", "clone_url", "unfixable_checks", "base_branch_ci_jobs", "declared_prompt_extension", "declared_setup_command", "post_merge_dispatch_jobs", "work_ownership", "work_overrides", "ci_mode", "repair_template_id", "port_branches", "declared_port_branches", "port_types", "declared_port_types", "git_credential", "merge_on_green", "base_repair_bypasses_human_gates", "merge_recheck"},
		merge:  mergeRepoConfigRow,
	},
	// What grain CI last made of a branch things merge into
	// (model.BaseBranchCI). Keyed by the branch as well as the
	// repository, and folded by keeping the canonical row for
	// mergeDeclarationSyncRow's reason: every column is grain's own note
	// about a run it started, and the next merge into that branch
	// overwrites all of it. The one thing worth not losing is a block --
	// so a divergent row that is broken while the canonical one is not
	// wins, since dropping it would open a gate nobody meant to open.
	{
		name: "base_branch_ci", owner: "owner", repo: "name", keyed: true,
		rest: []string{"branch"},
		values: []string{"run_id", "commit", "started_at", "finished_at", "conclusion", "detail",
			"merged_task_id", "repair_task_id"},
		merge: mergeBaseBranchCIRow,
	},
	// What grain last dispatched on a base branch after a merge
	// (model.BaseBranchDispatch, grain/task-409). Keyed by the branch as
	// well as the repository, like base_branch_ci above, and folded by
	// keeping the canonical row (mergeDeclarationSyncRow): every column
	// is grain's own note about a dispatch it started, nothing here
	// holds anything -- a failure is a cold cache rather than a gate --
	// and the worst a dropped side costs is one dispatch re-asked on the
	// next cycle, which is the cheap direction to be wrong in.
	{
		name: "base_branch_dispatch", owner: "owner", repo: "name", keyed: true,
		rest:   []string{"branch"},
		values: []string{"commit", "jobs", "run_id", "started_at", "finished_at", "conclusion", "detail"},
		merge:  mergeDeclarationSyncRow,
	},
	// The release grain last started on a base branch after a merge
	// (model.BaseBranchRelease, grain/task-461). Keyed and folded exactly
	// as base_branch_dispatch above, and for its reason: every column is
	// grain's own note about a run it started, nothing here holds
	// anything -- a failed release is images that did not ship rather
	// than a gate -- and the worst a dropped side costs is one release
	// re-asked on the next cycle.
	{
		name: "base_branch_release", owner: "owner", repo: "name", keyed: true,
		rest:   []string{"branch"},
		values: []string{"commit", "run_id", "started_at", "finished_at", "conclusion", "detail"},
		merge:  mergeDeclarationSyncRow,
	},
	// The speculative merge in flight for a base branch
	// (model.MergeBatch, grain/task-294). Keyed by the branch as well as
	// the repository, like base_branch_ci above, and folded by keeping
	// the canonical row: a batch is transient -- it ends the moment its
	// members merge or fall out -- and the ref it names has already been
	// force-reset under whichever side loses, so the losing row is about
	// a tree that is no longer there. The queue forms another within a
	// cycle.
	{
		name: "merge_batch", owner: "owner", repo: "name", keyed: true,
		rest: []string{"branch"},
		values: []string{"ref", "base_commit", "commit", "members", "run_id", "started_at",
			"finished_at", "conclusion", "detail", "splits"},
		merge: mergeDeclarationSyncRow,
	},
	// One firing of a schedule a workflow's own cron declares
	// (model.ScheduledWorkflowRun, grain/task-351). Not keyed by the
	// repository, for repo_declaration's reason below: the schedule id is
	// the primary key and already carries the repository lowercased
	// (model.RepoDeclaredID), so a rewrite here moves the repo columns
	// onto the canonical spelling and cannot collide two firings into
	// one.
	{name: "scheduled_workflow_run", owner: "owner", repo: "name"},
	// What every batch that has already ended cost (model.MergeBatchRecord,
	// grain/task-300). Rewritten unconditionally, with no key to collide
	// on and nothing to fold: each row is its own autoincrement id, and
	// two rows under two spellings are two batches that both happened.
	// Counting them under one repository is the whole point -- a history
	// split across two spellings is a rate computed over half the
	// evidence.
	{name: "merge_batch_history", owner: "owner", repo: "name"},
	// And what every gather hold that has already ended did
	// (model.MergeGatherRecord, grain/task-440). Rewritten
	// unconditionally for merge_batch_history's reason, and for the same
	// end: a rate computed over holds split across two spellings of one
	// repository is a rate computed over half the evidence.
	{name: "merge_gather_history", owner: "owner", repo: "name"},
	// And what the overlap re-check said about every merge
	// (model.MergeRecheckRecord, grain/task-985), for the same reason:
	// a trigger rate split across two spellings of one repository is a
	// rate computed over half the evidence.
	{name: "merge_recheck_history", owner: "owner", repo: "name"},
	// And what each red stretch of a base branch held (model.BaseHoldPass,
	// grain/task-986), for the same reason.
	{name: "base_hold_history", owner: "owner", repo: "name"},
	// And what the queue had to do over again (model.MergeQueueContentionEvent,
	// grain/task-3227), for the same reason: a rate split across two
	// spellings of one repository is a rate computed over half the evidence.
	{name: "merge_queue_contention", owner: "owner", repo: "name"},
	// And every merge an operator forced with a reason
	// (model.ForceMergeRecord, grain/task-479). Rewritten unconditionally
	// for merge_batch_history's reason, and for the same end: "forcing
	// should be rare" is a rate, and a history split across two spellings
	// of one repository is a rate computed over half the evidence.
	{name: "force_merge_history", owner: "owner", repo: "name"},
	// Where a declared template, suite or schedule came from
	// (model.RepoDeclaration). Not keyed by the repository -- the id is
	// the primary key, and it carries the repository already, lowercased
	// (model.RepoDeclaredID), so a rewrite here moves the provenance
	// columns and cannot collide two rows into one. What it buys is a
	// provenance that spells the repository the way the repo_config row
	// switching it on now does.
	{name: "repo_declaration", owner: "owner", repo: "name"},
	// Grain's record of polling one repository for those declarations.
	// Both sides of a fold are re-derived by the next poll, which is why
	// the merge simply keeps the canonical row's (mergeDeclarationSyncRow).
	{
		name: "repo_declaration_sync", owner: "owner", repo: "name", keyed: true,
		values: []string{"commit", "checked_at", "synced_at", "last_error"},
		merge:  mergeDeclarationSyncRow,
	},
	// A repository's path triggers and how far along each branch they
	// have been diffed (grain/task-1712). Both are re-derived -- the
	// triggers by the next declaration sync, the cursor by the next merge
	// -- so a fold keeps the canonical row, as for the sync row above.
	{
		name: "repo_path_trigger", owner: "owner", repo: "name", keyed: true,
		rest:   []string{"trigger"},
		values: []string{"commit", "rule"},
		merge:  mergeDeclarationSyncRow,
	},
	{
		name: "repo_merge_rules", owner: "owner", repo: "name", keyed: true,
		values: []string{"commit", "rules"},
		merge:  mergeDeclarationSyncRow,
	},
	{
		name: "path_trigger_cursor", owner: "owner", repo: "name", keyed: true,
		rest:   []string{"branch"},
		values: []string{"commit", "updated_at"},
		merge:  mergeDeclarationSyncRow,
	},
	{
		name: "repo_workflow", owner: "owner", repo: "name", keyed: true,
		rest:   []string{"path"},
		values: []string{"text"},
		merge:  mergeRepoWorkflowRow,
	},
	{
		name: "qualification_config", owner: "owner", repo: "name", keyed: true,
		values: []string{"require_approval", "auto_promote"},
		merge:  mergeQualificationConfigRow,
	},
	{
		name: "qualification_item", owner: "owner", repo: "name", keyed: true,
		rest:   []string{"template_id"},
		values: []string{"repeat_count", "order_key"},
		merge:  mergeQualificationItemRow,
	},
	{
		name: "qualification_item_depends_on", owner: "owner", repo: "name", keyed: true,
		rest: []string{"template_id", "depends_on_template_id"},
	},
	// A repo's memory and its documents (grain/task-270). Keyed by the
	// scope, two empty strings for the deployment-wide one -- which names
	// no repository, matches no canonical spelling, and is therefore
	// never touched by any of this.
	{
		name: "memory", owner: "owner", repo: "name", keyed: true,
		values: []string{"text"},
		merge:  mergeMemoryRow,
	},
	{
		name: "memory_document", owner: "owner", repo: "name", keyed: true,
		rest:   []string{"document"},
		values: []string{"summary", "text"},
		merge:  mergeMemoryDocumentRow,
	},
}

// RepoCaseTable is one table the reconciliation visits, and the two
// columns it holds a repository in.
type RepoCaseTable struct {
	Name  string
	Owner string
	Repo  string
}

// RepoCaseTables is every table repoCaseTables names, in its order.
//
// It exists for one caller: the test that holds the list to its own
// claim -- "every repo-keyed table in schema.go is here" -- by reading
// the schema and failing for a repo-keyed table the list omits
// (pkg/model/sqlite's TestRepoCaseNamesEveryRepoKeyedTable). That check
// needs a real database to read pragma_table_info out of, which lives a
// package away, and an omission is otherwise invisible: the
// reconciliation leaves such a table's rows in the old spelling, says
// nothing about them, and then reports the store clean.
func RepoCaseTables() []RepoCaseTable {
	out := make([]RepoCaseTable, len(repoCaseTables))
	for i, t := range repoCaseTables {
		out[i] = RepoCaseTable{Name: t.name, Owner: t.owner, Repo: t.repo}
	}
	return out
}

// spellings is every distinct repository this table holds, in a stable
// order. NULLs are skipped rather than compared: a task with no target
// at all has no repo to reconcile.
func (t repoCaseTable) spellings(ctx context.Context, tx *sql.Tx) ([]RepoRef, error) {
	var out []RepoRef
	err := each(ctx, tx,
		"SELECT DISTINCT "+quoted(t.owner)+","+quoted(t.repo)+" FROM "+quoted(t.name)+
			" WHERE "+quoted(t.owner)+" IS NOT NULL AND "+quoted(t.repo)+" IS NOT NULL"+
			" ORDER BY "+quoted(t.owner)+","+quoted(t.repo),
		nil,
		func(rows *sql.Rows) error {
			var r RepoRef
			if err := rows.Scan(&r.Owner, &r.Name); err != nil {
				return err
			}
			out = append(out, r)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("reading repos from %s: %w", t.name, err)
	}
	return out, nil
}

// reconcile rewrites this table's rows for one repo from one spelling to
// another, recording what it did (or what it refused to do) on plan.
//
// It writes as it goes, whether or not the caller means to keep the
// result: reconcileRepoCase's own transaction is what decides that, and
// doing the work either way is what lets a report count merges exactly
// rather than estimate them.
func (t repoCaseTable) reconcile(ctx context.Context, tx *sql.Tx, from, to RepoRef, plan *RepoCasePlan) error {
	change := RepoCaseChange{Table: t.name, From: from, To: to}
	before := len(plan.Conflicts)

	var merged []repoCaseRow
	if t.keyed {
		divergent, err := t.rows(ctx, tx, from)
		if err != nil {
			return err
		}
		canonical, err := t.rows(ctx, tx, to)
		if err != nil {
			return err
		}
		for _, row := range divergent {
			existing, ok := findRow(canonical, row.key)
			if !ok {
				continue
			}
			values, conflict := t.mergeRow(existing.values, row.values)
			if conflict != "" {
				plan.Conflicts = append(plan.Conflicts, RepoCaseConflict{
					Table: t.name, From: from, To: to,
					Row: strings.Join(row.key, " "), Detail: conflict,
				})
				continue
			}
			merged = append(merged, repoCaseRow{key: row.key, values: values})
		}
	}
	if err := t.checkIdentities(ctx, tx, from, to, plan); err != nil {
		return err
	}
	if len(plan.Conflicts) > before {
		// Nothing to write for a repo whose rows are not all decidable:
		// the transaction is going to be abandoned whole, and attempting
		// the rewrite anyway would only trade the report an operator can
		// act on for a primary key violation they cannot.
		return nil
	}

	for _, row := range merged {
		if err := t.foldRow(ctx, tx, from, to, row); err != nil {
			return err
		}
		change.Merged++
	}
	res, err := tx.ExecContext(ctx,
		"UPDATE "+quoted(t.name)+" SET "+quoted(t.owner)+" = ?,"+quoted(t.repo)+" = ?"+
			" WHERE "+quoted(t.owner)+" = ? AND "+quoted(t.repo)+" = ?",
		to.Owner, to.Name, from.Owner, from.Name)
	if err != nil {
		return fmt.Errorf("rewriting %s rows for %s: %w", t.name, from, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	change.Rewritten = int(n)
	if change.Rewritten > 0 || change.Merged > 0 {
		plan.Changes = append(plan.Changes, change)
	}
	return nil
}

// repoCaseRow is one row of a keyed table: the part of its primary key
// that is not the repo, and the columns a merge may rewrite.
type repoCaseRow struct {
	key    []string
	values []string
}

func findRow(rows []repoCaseRow, key []string) (repoCaseRow, bool) {
	for _, r := range rows {
		if slices.Equal(r.key, key) {
			return r, true
		}
	}
	return repoCaseRow{}, false
}

func (t repoCaseTable) rows(ctx context.Context, tx *sql.Tx, repo RepoRef) ([]repoCaseRow, error) {
	cols := append(append([]string{}, t.rest...), t.values...)
	var out []repoCaseRow
	query := "SELECT " + quotedList(cols) + " FROM " + quoted(t.name) +
		" WHERE " + quoted(t.owner) + " = ? AND " + quoted(t.repo) + " = ?"
	if len(t.rest) > 0 {
		query += " ORDER BY " + quotedList(t.rest)
	}
	err := each(ctx, tx, query, []any{repo.Owner, repo.Name}, func(rows *sql.Rows) error {
		// Every column is scanned as text, whatever its declared type:
		// the merges below compare values and pick between them, and a
		// value written back through a bind parameter takes the column's
		// own affinity on the way in, exactly as the code that wrote it
		// first did.
		cells := make([]string, len(cols))
		into := make([]any, len(cols))
		for i := range cells {
			into[i] = &cells[i]
		}
		if err := rows.Scan(into...); err != nil {
			return err
		}
		out = append(out, repoCaseRow{key: cells[:len(t.rest)], values: cells[len(t.rest):]})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading %s rows for %s: %w", t.name, repo, err)
	}
	return out, nil
}

// mergeRow is the table's own merge, or the answer for a table that has
// no values to merge: two rows with the same key say the same thing
// twice, and keeping the canonical one loses nothing.
func (t repoCaseTable) mergeRow(canonical, divergent []string) ([]string, string) {
	if len(t.values) == 0 {
		return nil, ""
	}
	return t.merge(canonical, divergent)
}

// foldRow writes a merge: the canonical row takes the merged values, and
// the divergent row goes, so the blanket rewrite that follows has no
// primary key left to collide with.
func (t repoCaseTable) foldRow(ctx context.Context, tx *sql.Tx, from, to RepoRef, row repoCaseRow) error {
	if len(row.values) > 0 {
		args := make([]any, 0, len(row.values)+2+len(row.key))
		sets := make([]string, len(t.values))
		for i, col := range t.values {
			sets[i] = quoted(col) + " = ?"
			args = append(args, row.values[i])
		}
		args = append(args, to.Owner, to.Name)
		args = append(args, keyArgs(row.key)...)
		if _, err := tx.ExecContext(ctx,
			"UPDATE "+quoted(t.name)+" SET "+strings.Join(sets, ",")+
				" WHERE "+quoted(t.owner)+" = ? AND "+quoted(t.repo)+" = ?"+t.keyClause(),
			args...); err != nil {
			return fmt.Errorf("merging %s rows for %s: %w", t.name, to, err)
		}
	}
	args := append([]any{from.Owner, from.Name}, keyArgs(row.key)...)
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM "+quoted(t.name)+
			" WHERE "+quoted(t.owner)+" = ? AND "+quoted(t.repo)+" = ?"+t.keyClause(),
		args...); err != nil {
		return fmt.Errorf("dropping the merged %s row for %s: %w", t.name, from, err)
	}
	return nil
}

func (t repoCaseTable) keyClause() string {
	var b strings.Builder
	for _, col := range t.rest {
		b.WriteString(" AND " + quoted(col) + " = ?")
	}
	return b.String()
}

func keyArgs(key []string) []any {
	args := make([]any, len(key))
	for i, k := range key {
		args[i] = k
	}
	return args
}

// checkIdentities reports the rows whose repo cannot be rewritten
// because the canonical spelling already holds a row grain would then
// read as the same thing -- two releases of one name for one repo. There
// is no merge to offer: they are two independent records, and which one
// survives is the operator's call.
func (t repoCaseTable) checkIdentities(ctx context.Context, tx *sql.Tx, from, to RepoRef, plan *RepoCasePlan) error {
	if len(t.identity) == 0 {
		return nil
	}
	cols := quotedList(t.identity)
	var clashes []string
	err := each(ctx, tx,
		"SELECT DISTINCT "+cols+" FROM "+quoted(t.name)+
			" WHERE "+quoted(t.owner)+" = ? AND "+quoted(t.repo)+" = ? AND ("+cols+") IN"+
			" (SELECT "+cols+" FROM "+quoted(t.name)+
			" WHERE "+quoted(t.owner)+" = ? AND "+quoted(t.repo)+" = ?)"+
			" ORDER BY "+cols,
		[]any{from.Owner, from.Name, to.Owner, to.Name},
		func(rows *sql.Rows) error {
			cells := make([]string, len(t.identity))
			into := make([]any, len(t.identity))
			for i := range cells {
				into[i] = &cells[i]
			}
			if err := rows.Scan(into...); err != nil {
				return err
			}
			clashes = append(clashes, strings.Join(cells, " "))
			return nil
		})
	if err != nil {
		return fmt.Errorf("checking %s identities for %s: %w", t.name, from, err)
	}
	for _, c := range clashes {
		plan.Conflicts = append(plan.Conflicts, RepoCaseConflict{
			Table: t.name, From: from, To: to, Row: c,
			Detail: "both spellings hold a row of this name, and they are two separate records: " +
				"delete or rename one of them and run this again",
		})
	}
	return nil
}

// mergeRepoConfigRow folds two repo_config rows for one repo into one.
//
// default_capabilities is unioned, canonical entries first: that field
// composes with Config.DefaultCapabilities by union already
// (repo_config.go's own doc comment -- "a repo adds to what the
// deployment defaults and can never subtract from it"), both sides were
// written by somebody configuring what they believed was this repo, and
// a union is the only merge that loses neither of them. It does mean
// tasks filed afterwards are seeded with the union, which is a widening
// -- of a default that whoever files a task can still untick.
//
// The two text fields are not unioned. A repo has one set of standing
// instructions and one setup command, both prose that means something
// whole; concatenating two of them, in an order decided by which
// capitalisation an operator happened to type, would produce a third
// thing neither of them wrote. So an empty side takes the other's text,
// and two different texts are a conflict to be resolved by hand.
func mergeRepoConfigRow(canonical, divergent []string) ([]string, string) {
	out := make([]string, len(canonical))
	out[0] = joinCSV(unionCSV(canonical[0], divergent[0]))
	// CI grants are authority, so conflicting sets require an explicit choice.
	if !slices.Equal(slices.Sorted(slices.Values(splitCSV(canonical[3]))), slices.Sorted(slices.Values(splitCSV(divergent[3])))) {
		return nil, "both spellings set different ci_capabilities"
	}
	out[3] = canonical[3]
	// Release enablement and the names of credentials exposed to release jobs
	// are authority too. Two spellings that disagree need an operator's choice;
	// silently unioning them could expose a secret or restart publishing.
	for _, i := range []int{4, 5} {
		if canonical[i] != divergent[i] {
			return nil, "both spellings set different " + []string{"release_workflows_disabled", "release_secrets"}[i-4]
		}
		out[i] = canonical[i]
	}
	for i, field := range []string{"prompt_extension", "setup_command"} {
		text, ok := pickText(canonical[i+1], divergent[i+1])
		if !ok {
			return nil, "both spellings set " + field + ", to different text"
		}
		out[i+1] = text
	}
	// Whether grain reads this repository's own `.grain` declarations
	// (RepoConfig.Declarations). Authority, like the three above: it is
	// the switch that lets a repository's own files file tasks here, and
	// two spellings that disagree about it are two different answers to
	// a question somebody decided on purpose. Unioning them would turn
	// "off" into "on" by merging, which is the one direction that must
	// never happen by accident.
	if canonical[6] != divergent[6] {
		return nil, "both spellings set different declarations"
	}
	out[6] = canonical[6]
	// Whether the merge queue requires a branch to contain its base
	// before it merges (RepoConfig.RequireBranchUpToDate). A decision
	// somebody made on purpose, like the switch above it, and one whose
	// two directions cost opposite things -- so two spellings that
	// disagree are a question for a person rather than one to answer by
	// picking a side.
	if canonical[7] != divergent[7] {
		return nil, "both spellings set different require_branch_up_to_date"
	}
	out[7] = canonical[7]
	if canonical[8] != divergent[8] {
		return nil, "both spellings set different ignore_github_ci"
	}
	out[8] = canonical[8]
	// Whether grain watches this repository's default branch without
	// having merged into it (RepoConfig.WatchDefaultBranch). Authority
	// again, and in the expensive direction: unioning "off" into "on"
	// would start a full suite on a repository nobody had ticked under
	// the spelling they were looking at.
	if canonical[9] != divergent[9] {
		return nil, "both spellings set different watch_default_branch"
	}
	out[9] = canonical[9]
	// clone_url decides which tree grain clones for every task in this
	// repository, and unfixable_checks and base_branch_ci_jobs decide
	// what judges it -- authority and verdicts, the two things
	// pkg/declschema refuses a repository outright, and so not things to
	// resolve by picking a side. Each takes the other's value where one
	// side is silent and is a conflict where both spellings speak.
	for i, field := range []string{"clone_url", "unfixable_checks", "base_branch_ci_jobs"} {
		text, ok := pickText(canonical[i+10], divergent[i+10])
		if !ok {
			return nil, "both spellings set different " + field
		}
		out[i+10] = text
	}
	// The two a repository declared for itself (grain/task-340). Neither
	// is a decision anybody typed under either spelling: both are
	// re-derived from `.grain/settings.yml` on the next poll of the
	// canonical repository, so an empty side yields and a disagreement
	// is a stale copy rather than a question for a person.
	for i := range []string{"declared_prompt_extension", "declared_setup_command"} {
		if canonical[i+13] != "" {
			out[i+13] = canonical[i+13]
			continue
		}
		out[i+13] = divergent[i+13]
	}
	// post_merge_dispatch_jobs names jobs grain runs on a base branch
	// with that dispatch's publishing material
	// (RepoConfig.PostMergeDispatchJobs), so it is in the authority
	// clause above rather than the re-derived one below it: an empty
	// side yields, and two spellings that name different jobs are a
	// question for whoever decided what gets published.
	text, ok := pickText(canonical[15], divergent[15])
	if !ok {
		return nil, "both spellings set different post_merge_dispatch_jobs"
	}
	out[15] = text
	// Whether this deployment runs this repository's standing work, and
	// the per-item exceptions to it (RepoConfig.Work). Two spellings
	// that disagree are two deployments' worth of decision about who is
	// cutting this repository's releases, which is the one question a
	// fold must not answer by picking a side: getting it wrong in one
	// direction publishes from a repository somebody stopped, and in the
	// other stops one nobody did. An empty side yields; a disagreement
	// is a person's.
	//
	// The legacy release_workflows_disabled column at index 4 is folded
	// the same way and for the same reason it is still written at all:
	// a divergent row may predate the two columns here, and dropping its
	// side would lose the only record it has.
	for i, field := range []string{"work_ownership", "work_overrides"} {
		merged, ok := pickText(canonical[i+16], divergent[i+16])
		if !ok {
			return nil, "both spellings set different " + field
		}
		out[i+16] = merged
	}
	for i, field := range []string{"ci_mode", "repair_template_id"} {
		merged, ok := pickText(canonical[i+18], divergent[i+18])
		if !ok {
			return nil, "both spellings set different " + field
		}
		out[i+18] = merged
	}
	// port_branches is an operator's word on which branches file ports
	// of one another, so two spellings that disagree are a question for
	// them; declared_port_branches is re-derived from the canonical
	// repository's own `.grain/settings.yml` the way the two declared
	// columns above are.
	merged, ok := pickText(canonical[20], divergent[20])
	if !ok {
		return nil, "both spellings set different port_branches"
	}
	out[20] = merged
	out[21] = canonical[21]
	if out[21] == "" {
		out[21] = divergent[21]
	}
	// port_types and declared_port_types follow the two port_branches
	// columns, for the same reasons.
	merged, ok = pickText(canonical[22], divergent[22])
	if !ok {
		return nil, "both spellings set different port_types"
	}
	out[22] = merged
	out[23] = canonical[23]
	if out[23] == "" {
		out[23] = divergent[23]
	}
	// The credential the repository is cloned and pushed with
	// (RepoConfig.GitCredential): authority, so two different names are
	// an operator's choice, and an empty side takes the other's.
	cred, ok := pickText(canonical[24], divergent[24])
	if !ok {
		return nil, "both spellings set different git_credential"
	}
	out[24] = cred
	// Whether the merge queue may merge a green entry ahead of pending
	// ones (RepoConfig.MergeOnGreen): a decision about merge order
	// somebody made on purpose, so two spellings that disagree are a
	// question for a person.
	if canonical[25] != divergent[25] {
		return nil, "both spellings set different merge_on_green"
	}
	out[25] = canonical[25]
	if canonical[26] != divergent[26] {
		return nil, "both spellings set different base_repair_bypasses_human_gates"
	}
	out[26] = canonical[26]
	if canonical[27] != divergent[27] {
		return nil, "both spellings set different merge_recheck"
	}
	out[27] = canonical[27]
	return out, ""
}

// mergeBaseBranchCIRow folds two base_branch_ci rows -- one branch of
// one repository, recorded under two spellings of that repository --
// into one.
//
// It keeps whichever side is holding the merge queue, and the canonical
// row when neither is. Every column here is grain's own record of a run
// it started, re-derived by the next merge into that branch, so there is
// nothing to lose by dropping a side -- except the one thing that is not
// a record but a decision: a failure is what stops changes stacking on a
// broken base, and a fold that dropped it would open that gate silently.
func mergeBaseBranchCIRow(canonical, divergent []string) ([]string, string) {
	// conclusion is values[4].
	if canonical[4] != BaseBranchCIFailure && divergent[4] == BaseBranchCIFailure {
		return divergent, ""
	}
	return canonical, ""
}

// mergeDeclarationSyncRow folds two repo_declaration_sync rows -- one
// repository polled under two spellings -- into one.
//
// It keeps the canonical row's and drops the other, which is the one
// case here where a conflict is not worth a human's attention: every
// column is grain's own record of a poll, and the next poll overwrites
// all of it from the repository regardless of which side won. Keeping
// the canonical side merely means the deployment does not re-clone
// immediately.
func mergeDeclarationSyncRow(canonical, _ []string) ([]string, string) {
	return canonical, ""
}

// mergeRepoWorkflowRow folds two repo_workflow rows -- the same
// workflow path, declared under two spellings of one repository -- into
// one.
//
// A workflow file is prose that means something whole, exactly like the
// two text fields above, so the same rule applies: an empty side takes
// the other's text, and two different workflow files under one path are
// a conflict for a human to resolve rather than something to
// concatenate. Concatenating two YAML documents would not even parse,
// which makes this the one case where guessing is visibly worse than
// refusing.
func mergeRepoWorkflowRow(canonical, divergent []string) ([]string, string) {
	text, ok := pickText(canonical[0], divergent[0])
	if !ok {
		return nil, "both spellings declare this workflow, with different text"
	}
	return []string{text}, ""
}

// mergeMemoryRow folds two memories for one repository into one.
//
// pickText's rule, and deliberately not "the newer updated_at wins". A
// memory is prose an agent wrote for the next run to read, and the two
// rows here are two accumulations of it: the older is not a stale copy
// of the newer, it is whatever was learned under the other spelling, and
// nothing in either text says which facts survive into the other.
// Dropping one because its timestamp is older would delete the only copy
// of everything it alone remembered, silently, in the one command an
// operator runs precisely to be told what is about to change -- while
// refusing costs them a read of two texts in the memory panel and a
// delete or a paste, with both texts still in front of them.
//
// It is also the rule every other prose column here already follows
// (mergeRepoConfigRow's two text fields, mergeRepoWorkflowRow), and this
// file's own third decision: merging is conservative and refuses rather
// than guesses.
//
// updated_at is left out of the merge rather than consulted: the merged
// row is the canonical row, which keeps its own timestamp, and the
// column is nullable, which the text-scanning this machinery does to
// compare values cannot round-trip. updated_by_kind/updated_by_id go
// with it, for the first of those reasons: who wrote a memory
// (model.Memory.UpdatedBy) is a fact about the row that was kept, and
// this merge only ever keeps a divergent spelling's *text* when the two
// texts already agree.
func mergeMemoryRow(canonical, divergent []string) ([]string, string) {
	text, ok := pickText(canonical[0], divergent[0])
	if !ok {
		return nil, "both spellings hold a memory, and the two texts differ: " +
			"read both and delete or fold one into the other, then run this again"
	}
	return []string{text}, ""
}

// mergeMemoryDocumentRow folds two memory documents of one name, filed
// under two spellings of one repository, into one -- mergeMemoryRow's
// rule applied to the long form, where a document is a summary and a
// text and either of them differing is two documents rather than one.
func mergeMemoryDocumentRow(canonical, divergent []string) ([]string, string) {
	out := make([]string, len(canonical))
	for i, field := range []string{"summary", "text"} {
		text, ok := pickText(canonical[i], divergent[i])
		if !ok {
			return nil, "both spellings hold a document of this name, with different " + field + ": " +
				"read both and delete or fold one into the other, then run this again"
		}
		out[i] = text
	}
	return out, ""
}

// mergeQualificationConfigRow folds two qualification_config rows into
// one.
//
// PutQualificationPlan writes this row whenever anybody touches a repo's
// plan, both switches included, so the ordinary case is a row that says
// nothing: both flags off, which is exactly what no row at all says
// (GetQualificationPlan's own answer for a repo with none). Such a row
// yields to the other one, whichever side it is on.
//
// Two rows that both say something and disagree are a conflict rather
// than an OR. These are gates: require_approval decides whether a run's
// tasks wait for a human, auto_promote whether a candidate promotes
// without one. Turning either on because the other spelling had it on
// would be grain deciding a release policy nobody set for this repo.
func mergeQualificationConfigRow(canonical, divergent []string) ([]string, string) {
	if allZero(divergent) {
		return canonical, ""
	}
	if allZero(canonical) {
		return divergent, ""
	}
	for i, field := range []string{"require_approval", "auto_promote"} {
		if canonical[i] != divergent[i] {
			return nil, fmt.Sprintf("both spellings configure %s, differently (%s and %s)",
				field, boolText(canonical[i]), boolText(divergent[i]))
		}
	}
	return canonical, ""
}

// mergeQualificationItemRow folds one repo's qualification plan into the
// other's, an item at a time -- an item being one template this repo
// qualifies a candidate with.
//
// Only a template both spellings schedule reaches here, and then only
// repeat_count is a real disagreement: how many times a template runs is
// a choice somebody made. order_key is display order within the plan
// (schema.go: "it decides nothing about dispatch"), so the canonical
// row's own position wins rather than being worth refusing over.
func mergeQualificationItemRow(canonical, divergent []string) ([]string, string) {
	if canonical[0] != divergent[0] {
		return nil, fmt.Sprintf("both spellings schedule this template, %s time(s) and %s",
			canonical[0], divergent[0])
	}
	return canonical, ""
}

// unionCSV is the canonical list, then whatever the divergent list adds
// to it -- order preserved on both sides, so a merged row reads as the
// canonical one with additions rather than as a resorted stranger.
func unionCSV(canonical, divergent string) []string {
	out := splitCSV(canonical)
	for _, item := range splitCSV(divergent) {
		if !slices.Contains(out, item) {
			out = append(out, item)
		}
	}
	return out
}

// pickText merges two values of a column only one of them may set:
// equal, or one of them empty, is a merge; anything else is not.
func pickText(canonical, divergent string) (string, bool) {
	switch {
	case canonical == divergent, divergent == "":
		return canonical, true
	case canonical == "":
		return divergent, true
	}
	return "", false
}

func allZero(values []string) bool {
	for _, v := range values {
		if !isZero(v) {
			return false
		}
	}
	return true
}

// isZero reads a column holding a stored bool. The driver hands these
// back as SQLite integers, but a database written through some other
// driver may hold the text, so both readings are spelled out rather than
// assumed.
func isZero(v string) bool {
	switch strings.ToLower(v) {
	case "", "0", "false":
		return true
	}
	return false
}

func boolText(v string) string {
	if isZero(v) {
		return "off"
	}
	return "on"
}

// quoted is schema.go's own identifier quoting, applied to a name from
// repoCaseTables above and never to anything a caller supplies.
func quoted(name string) string { return "`" + name + "`" }

func quotedList(names []string) string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = quoted(n)
	}
	return strings.Join(out, ",")
}
