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

package sqlite_test

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// keptOnRepoRemoval is every repo-keyed table Store.ForgetRepo leaves
// alone, and why. A table is on this list or on
// model.ForgottenRepoTables, never neither: a new repo-keyed table makes
// TestForgetRepoClearsEveryRepoKeyedTableItShould fail until somebody
// decides whether removing a repository should take its rows.
var keptOnRepoRemoval = map[string]string{
	// The work filed against the repository. Removing a repository parks
	// its tasks on their next dispatch (orchestrator.reconcileTargetRepos);
	// it does not delete them, their runs or their grants.
	"task":       "tasks keep their target and their page",
	"task_read":  "a task's own grant",
	"task_write": "a task's own grant",
	"task_run":   "a task's runs",
	// Operator-made standing work. Its own pane edits or deletes it, and a
	// firing against a repository off the list is filed parked rather than
	// dispatched. Declared rows of these tables are deleted by ForgetRepo
	// through deleteDeclared, which this test seeds none of.
	"schedule":       "operator-made schedule; parks when it fires off-list",
	"schedule_read":  "a schedule's own grant",
	"schedule_write": "a schedule's own grant",
	"template":       "operator-made template",
	"template_read":  "a template's own grant",
	"template_write": "a template's own grant",
	// History: what already happened in the repository.
	"release":                "release history",
	"release_candidate":      "release history",
	"release_approval":       "release history",
	"branch":                 "a task's branch record",
	"suite_run":              "suite history",
	"qualification_run":      "qualification history",
	"merge_batch":            "a task's in-flight merge; the task outlives the repository",
	"merge_batch_history":    "merge history",
	"merge_gather_history":   "merge history",
	"merge_recheck_history":  "merge history",
	"base_hold_history":      "merge history",
	"merge_queue_contention": "merge history",
	"force_merge_history":    "merge history",
	"declaration_acceptance": "the record of who accepted which declaration",
	"repo_declaration":       "deleted through deleteDeclared, keyed by declared id",
	"repo_declaration_sync":  "deleted by ForgetRepo outright, after deleteDeclared",
	"repo_path_trigger":      "deleted by ForgetRepo through forgetPathTriggers",
	"repo_merge_rules":       "deleted by ForgetRepo through forgetPathTriggers",
	"path_trigger_cursor":    "deleted by ForgetRepo through forgetPathTriggers",
	"memory":                 "what grain learned about the repository survives a re-add",
	"memory_document":        "what grain learned about the repository survives a re-add",
}

// forgottenElsewhere are the keptOnRepoRemoval entries ForgetRepo does in
// fact clear, through a helper rather than forgottenRepoTables.
var forgottenElsewhere = []string{
	"repo_declaration_sync", "repo_path_trigger", "repo_merge_rules", "path_trigger_cursor",
}

// Every repo-keyed table is seeded with one row for the repository being
// removed, one for a neighbour, and one for the same owner/name on
// another host, and ForgetRepo must clear exactly the tables it claims
// to and touch nothing of the others' (grain/task-4288). This is the "no
// rows dangle" check for removal, driven by the schema rather than by a
// hand-kept list of tables. The other host's repository is then removed
// in turn, which only works for tables whose rows carry the host
// (StoredOwner, grain/task-4335).
func TestForgetRepoClearsEveryRepoKeyedTableItShould(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)
	forgotten := model.ForgottenRepoTables()
	for _, table := range model.RepoCaseTables() {
		_, kept := keptOnRepoRemoval[table.Name]
		if kept == slices.Contains(forgotten, table.Name) {
			t.Errorf("`%s` is repo-keyed and is on %s: decide whether removing a repository "+
				"forgets its rows (model.forgottenRepoTables) or keeps them (keptOnRepoRemoval, with why)",
				table.Name, map[bool]string{true: "both lists", false: "neither list"}[kept])
		}
	}
	if t.Failed() {
		return
	}

	gone := model.RepoRef{Owner: "acme", Name: "widgets"}
	neighbour := model.RepoRef{Owner: "acme", Name: "gadgets"}
	offHost := model.RepoRef{Host: "ghe.example.com", Owner: "acme", Name: "widgets"}
	// repo_declaration's rows name the template, suite or schedule they
	// declared, which ForgetRepo deletes through them; a placeholder names
	// nothing it could. TestForgetRepoRemovesAllRepoRecords covers it.
	tables := slices.DeleteFunc(model.RepoCaseTables(), func(t model.RepoCaseTable) bool {
		return t.Name == "repo_declaration"
	})
	for i, table := range tables {
		seedRepoRow(t, ctx, db, table, gone, i*3)
		seedRepoRow(t, ctx, db, table, neighbour, i*3+1)
		seedRepoRow(t, ctx, db, table, offHost, i*3+2)
	}
	isForgotten := func(table string) bool {
		return slices.Contains(forgotten, table) || slices.Contains(forgottenElsewhere, table)
	}
	check := func(table model.RepoCaseTable, removed, untouched []model.RepoRef) {
		t.Helper()
		for _, repo := range removed {
			wantGone := isForgotten(table.Name)
			if n := repoRows(t, ctx, db, table, repo); wantGone && n != 0 {
				t.Errorf("`%s` still holds %d row(s) for the removed %s", table.Name, n, repo)
			} else if !wantGone && n != 1 {
				t.Errorf("`%s` holds %d row(s) for the removed %s, want its one row kept (%s)",
					table.Name, n, repo, keptOnRepoRemoval[table.Name])
			}
		}
		for _, repo := range untouched {
			if n := repoRows(t, ctx, db, table, repo); n != 1 {
				t.Errorf("removing %v left `%s` with %d row(s) for %s, want 1", removed, table.Name, n, repo)
			}
		}
	}

	if err := store.ForgetRepo(ctx, gone); err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		check(table, []model.RepoRef{gone}, []model.RepoRef{neighbour, offHost})
	}
	if t.Failed() {
		return
	}
	if err := store.ForgetRepo(ctx, offHost); err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		check(table, []model.RepoRef{gone, offHost}, []model.RepoRef{neighbour})
	}
}

// seedRepoRow inserts one row for repo, filling every other NOT NULL
// column without a default from its declared type. seq makes primary
// keys distinct across the rows one test writes.
func seedRepoRow(t *testing.T, ctx context.Context, db *sql.DB, table model.RepoCaseTable, repo model.RepoRef, seq int) {
	t.Helper()
	rows, err := db.QueryContext(ctx,
		"SELECT `name`,`type`,`notnull`,`dflt_value`,`pk` FROM pragma_table_info(?)", table.Name)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var cols []string
	var args []any
	for rows.Next() {
		var name, typ string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&name, &typ, &notNull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		switch {
		case name == table.Owner:
			args = append(args, repo.StoredOwner())
		case name == table.Repo:
			args = append(args, repo.Name)
		case notNull == 0 && pk == 0:
			continue
		case dflt.Valid && pk == 0 && !strings.Contains(strings.ToUpper(typ), "TEXT"):
			// Triggers refuse some empty TEXT defaults (a task's owner),
			// so only non-text defaults are left to the schema.
			continue
		default:
			args = append(args, seedValue(strings.ToUpper(typ), seq))
		}
		cols = append(cols, "`"+name+"`")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	q := fmt.Sprintf("INSERT INTO `%s` (%s) VALUES (%s)", table.Name,
		strings.Join(cols, ","), strings.TrimSuffix(strings.Repeat("?,", len(cols)), ","))
	// The rows are placeholders, not real tasks or schedules: the
	// schema's CHECKs and foreign keys are about what a real one says,
	// and this test is only about which repository it is in.
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, pragma := range []string{"PRAGMA ignore_check_constraints = ON", "PRAGMA foreign_keys = OFF"} {
		if _, err := conn.ExecContext(ctx, pragma); err != nil {
			t.Fatal(err)
		}
	}
	defer conn.ExecContext(ctx, "PRAGMA ignore_check_constraints = OFF")
	defer conn.ExecContext(ctx, "PRAGMA foreign_keys = ON")
	if _, err := conn.ExecContext(ctx, q, args...); err != nil {
		t.Fatalf("seeding `%s` for %s: %v", table.Name, repo, err)
	}
}

func seedValue(typ string, seq int) any {
	switch {
	case strings.Contains(typ, "INT"), strings.Contains(typ, "BOOL"):
		return 1000 + seq
	case strings.Contains(typ, "REAL"), strings.Contains(typ, "FLOAT"), strings.Contains(typ, "DOUBLE"):
		return float64(seq)
	case strings.Contains(typ, "DATE"), strings.Contains(typ, "TIME"):
		return "2026-11-11 00:00:00"
	case strings.Contains(typ, "BLOB"):
		return []byte{byte(seq)}
	default:
		return fmt.Sprintf("seed-%d", seq)
	}
}

func repoRows(t *testing.T, ctx context.Context, db *sql.DB, table model.RepoCaseTable, repo model.RepoRef) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(ctx,
		fmt.Sprintf("SELECT COUNT(*) FROM `%s` WHERE `%s` = ? AND `%s` = ?", table.Name, table.Owner, table.Repo),
		repo.StoredOwner(), repo.Name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
