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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ai-on-gke/grain/pkg/model"
)

// The layout of a state repository, as constants rather than as strings
// spelled out at each use: an agent reading these files, a human
// reviewing a pull request against them and this package all have to
// agree on the paths, and there is one place here to change them.
const (
	// TablesDir holds one file per table, named for the table.
	TablesDir = "tables"
	// SchemaVersionFile records the model.SchemaVersion the dump was
	// written by, so a build that would misread it can say so instead of
	// importing rows into a schema they do not fit.
	SchemaVersionFile = "schema-version"
	// SecretsFile is the encrypted secrets blob (pkg/secrets). It used to
	// live in this repository -- one thing to clone, one thing to back up
	// -- and no longer does: the state repository is now somewhere agents
	// are dispatched to work, and a repository a sandbox can clone is a
	// repository a sandbox can read every byte of, ciphertext included.
	// It sits beside the private key under <data-dir>/secrets instead
	// (cmd/grain's secretsConfig), which is the one directory an operator
	// already had to back up, since the key was never in here either.
	//
	// The name stays here because this package is still what knows about
	// it: EnsureIgnored keeps a stray copy from being committed, and
	// HasSecrets asks whether a repository is one an earlier build wrote
	// it into -- which is a question about history, and history does not
	// forget.
	SecretsFile = "secrets.enc"
	// ReadmeFile explains the repository to whoever opens it next.
	ReadmeFile = "README.md"
	// IgnoreFile keeps what is not state out of the repository
	// (EnsureIgnored).
	IgnoreFile = ".gitignore"
	// WorkflowFile is the CI step that validates a proposed change before
	// it is merged: `grain state check`, run against the pull request
	// rather than against the deployment that would otherwise be the
	// first thing to find out (format.go). A slash-separated path, since
	// it is a path in a git repository as well as one on disk; callers
	// writing it go through filepath.FromSlash.
	WorkflowFile = ".github/workflows/grain-state-check.yml"
	ScriptsDir   = "scripts"
	WorkflowsDir = "workflows"
	MemoryDir    = "memory"
	// ReleaseNotesDir holds one markdown file per week of what this
	// deployment shipped (model.ReleaseNote), named for the ISO week it
	// is about: `release-notes/2026-W38.md`. The well-known place the
	// weekly job's prose ends up, for whoever has a clone of this
	// repository and no browser pointed at the deployment.
	ReleaseNotesDir = "release-notes"
)

type externalText struct {
	column string
	path   func(map[string]any) (string, error)
}

var externalTexts = map[string]externalText{
	"repo_config": {"setup_command", func(r map[string]any) (string, error) {
		return safeComponentsPath("scripts/repos", r, []string{"owner", "name"}, "setup.sh")
	}},
	"repo_workflow": {"text", func(r map[string]any) (string, error) {
		base, err := safeComponentsPath("workflows", r, []string{"owner", "name"}, "")
		if err != nil {
			return "", err
		}
		path := filepath.Clean(filepath.FromSlash(textValue(r["path"])))
		if path == "." || filepath.IsAbs(path) || path == ".." || strings.HasPrefix(path, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("staterepo: workflow path is not safe")
		}
		return filepath.ToSlash(filepath.Join(base, path)), nil
	}},
	"memory": {"text", func(r map[string]any) (string, error) {
		team := textValue(r["team"])
		owner := textValue(r["owner"])
		name := textValue(r["name"])
		switch {
		case team == "" && owner == "" && name == "":
			return "memory/deployment.md", nil
		case team != "" && owner == "" && name == "":
			return safeComponentsPath("memory/teams", r, []string{"team"}, "team.md")
		case team != "" && owner != "" && name != "":
			return safeComponentsPath("memory/teams", r, []string{"team", "owner"}, name+".md")
		default:
			// legacy or deployment-scoped repo row
			return safeComponentsPath("memory/repos", r, []string{"owner"}, name+".md")
		}
	}},
	"release_note": {"text", func(r map[string]any) (string, error) {
		return safeComponentsPath(ReleaseNotesDir, r, nil, textValue(r["period"])+".md")
	}},
	"memory_document": {"text", func(r map[string]any) (string, error) {
		team := textValue(r["team"])
		owner := textValue(r["owner"])
		name := textValue(r["name"])
		doc := textValue(r["document"])
		switch {
		case team == "" && owner == "" && name == "":
			return safeComponentsPath("memory/documents/deployment", r, nil, doc+".md")
		case team != "" && owner == "" && name == "":
			return safeComponentsPath("memory/documents/teams", r, []string{"team"}, doc+".md")
		case team != "" && owner != "" && name != "":
			return safeComponentsPath("memory/documents/teams", r, []string{"team", "owner", "name"}, doc+".md")
		default:
			// legacy or deployment-scoped repo doc
			return safeComponentsPath("memory/documents/repos", r, []string{"owner", "name"}, doc+".md")
		}
	}},
}

func textValue(v any) string { s, _ := v.(string); return s }

func safeComponentsPath(prefix string, row map[string]any, keys []string, leaf string) (string, error) {
	out := []string{prefix}
	for _, key := range keys {
		value := textValue(row[key])
		if value == "" {
			return "", fmt.Errorf("staterepo: %s is not a safe path component", key)
		}
		for _, seg := range strings.Split(value, "/") {
			if seg == "" || seg == "." || seg == ".." || filepath.Base(seg) != seg || strings.Contains(seg, "\\") {
				return "", fmt.Errorf("staterepo: %s is not a safe path component", key)
			}
			out = append(out, seg)
		}
	}
	if leaf != "" {
		if leaf == "." || leaf == ".." || filepath.Base(leaf) != leaf || strings.Contains(leaf, "\\") {
			return "", fmt.Errorf("staterepo: file name is not a safe path component")
		}
		out = append(out, leaf)
	}
	return filepath.ToSlash(filepath.Join(out...)), nil
}

// Export writes every table in db to dir as JSON, one file per table.
//
// Rows are sorted by primary key and columns are emitted in the table's
// own declared order, both so that exporting an unchanged database twice
// produces byte-identical files. That is not tidiness: it is what lets a
// caller tell "this deployment changed" from "this dump was written
// again".
//
// This is not what the state repository holds -- ExportConfig below is,
// and has been since grain/task-395 -- and the difference is the whole
// of why both exist. This one is the deployment entire, and the caller
// it exists for is the schema-upgrade handover (cmd/grain's
// writeSchemaUpgrade): a build about to be replaced writes every row it
// has into the data directory, and the build that comes up imports them
// into a fresh database at the new schema. That dump never leaves the
// host and is never committed anywhere. Anything writing into a state
// repository's working tree wants ExportConfig, including the paths that
// replace the whole of it (reformat.go).
func Export(ctx context.Context, db *sql.DB, dir string) error {
	// Every table is read inside one transaction, so the dump is a
	// snapshot of one state of the database rather than a series of
	// unrelated reads taken as the daemon wrote underneath them.
	//
	// It is not tidiness, and it is not theoretical. A table is read per
	// statement and the tables are read in name order, so an export that
	// took no snapshot read `task` before `task_run`: a task filed and
	// dispatched in the milliseconds between the two produced a dump whose
	// task_run.json named a task task.json did not have. Measured rather
	// than reasoned about -- with a writer filing a task and starting a run
	// every millisecond, 18 exports in 20 came out inconsistent -- and what
	// it costs is the claim a restore rests on: a dump that lands rows
	// referring to rows that are not there is not one. It also reaches CI,
	// where `grain state check` imports the dump under foreign keys the
	// schema is free to grow.
	//
	// ReadOnly, and that word does the work: pkg/model/sqlite opens the
	// database with _txlock=immediate, so an ordinary BeginTx would take
	// SQLite's write lock for the whole export and stall every writer
	// behind a dump of every transcript grain has ever stored. The driver
	// begins a read-only transaction deferred instead, which in WAL mode
	// is a read snapshot that blocks nobody.
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("staterepo: opening a read transaction: %w", err)
	}
	// Rolled back rather than committed: nothing here writes, and a
	// rollback cannot fail in a way that would turn a good dump into an
	// error.
	defer tx.Rollback()
	return exportAll(ctx, tx, dir)
}

// LocalTables is the tables that stay in the local database and are never
// exported to a dump (docs/design/durable-tasks.md#DT-14): high-volume
// per-call logs pruned locally after their retention window.
var LocalTables = map[string]bool{
	"task_run_api_call": true,
}

// exportAll is Export's body, against whatever is already reading: the
// snapshot transaction above at every real call site.
func exportAll(ctx context.Context, db querier, dir string) error {
	tables, err := tableNames(ctx, db)
	if err != nil {
		return err
	}
	tablesDir := filepath.Join(dir, TablesDir)
	if err := os.MkdirAll(tablesDir, 0o700); err != nil {
		return fmt.Errorf("staterepo: preparing %s: %w", tablesDir, err)
	}
	// The three settings kinds go out as nested documents in the format
	// people write them in (decl.go) rather than as thirteen files of
	// rows -- here as well as in ExportConfig, so that one reader
	// (DumpPath) finds a table's rows wherever the dump came from.
	nested := declTables()
	if err := exportDeclarations(ctx, db, dir); err != nil {
		return err
	}
	written := map[string]bool{}
	for _, t := range tables {
		if nested[t] || LocalTables[t] {
			continue
		}
		data, err := exportTable(ctx, db, dir, t, false)
		if err != nil {
			return err
		}
		name := t + ".json"
		written[name] = true
		if err := writeFileIfChanged(filepath.Join(tablesDir, name), data); err != nil {
			return err
		}
	}
	// A table this build no longer has must not leave its old rows behind
	// to be imported by the next one.
	entries, err := os.ReadDir(tablesDir)
	if err != nil {
		return fmt.Errorf("staterepo: reading %s: %w", tablesDir, err)
	}
	for _, e := range entries {
		if e.IsDir() || written[e.Name()] || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if err := os.Remove(filepath.Join(tablesDir, e.Name())); err != nil {
			return fmt.Errorf("staterepo: removing %s: %w", e.Name(), err)
		}
	}
	return nil
}

// ExportConfig writes only the editable configuration tables and removes
// every operational table file left by an older build.
func ExportConfig(ctx context.Context, db *sql.DB, dir string) error {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("staterepo: opening a read transaction: %w", err)
	}
	defer tx.Rollback()
	return exportConfig(ctx, tx, dir)
}

// exportConfig is ExportConfig reading through q: its own read
// transaction, or the write transaction a rehearsal (Rehearse) is
// inside, which is what lets a refused write be described in exactly the
// files this function would have written after it.
func exportConfig(ctx context.Context, tx querier, dir string) error {
	tablesDir := filepath.Join(dir, TablesDir)
	if err := os.MkdirAll(tablesDir, 0o700); err != nil {
		return fmt.Errorf("staterepo: preparing %s: %w", tablesDir, err)
	}
	for _, root := range []string{ScriptsDir, WorkflowsDir, MemoryDir, ReleaseNotesDir} {
		if err := os.RemoveAll(filepath.Join(dir, root)); err != nil {
			return fmt.Errorf("staterepo: clearing %s: %w", root, err)
		}
	}
	if err := exportDeclarations(ctx, tx, dir); err != nil {
		return err
	}
	nested := declTables()
	tables := append(append([]string{}, SettingsTables...), "memory", "memory_document", "release_note")
	written := make(map[string]bool, len(tables))
	for _, table := range tables {
		if nested[table] {
			continue
		}
		data, err := exportTable(ctx, tx, dir, table, true)
		if err != nil {
			return err
		}
		name := table + ".json"
		written[name] = true
		if err := writeFileIfChanged(filepath.Join(tablesDir, name), data); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(tablesDir)
	if err != nil {
		return fmt.Errorf("staterepo: reading %s: %w", tablesDir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() || written[entry.Name()] || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if err := os.Remove(filepath.Join(tablesDir, entry.Name())); err != nil {
			return fmt.Errorf("staterepo: removing %s: %w", entry.Name(), err)
		}
	}
	return nil
}

// writeFileIfChanged leaves a file whose bytes already match alone, so
// its mtime does not move. git would not see a difference either way;
// what this buys is that a directory full of files nothing rewrites is
// cheap to watch and obvious to reason about.
//
// A symlink at path is replaced by a regular file rather than written
// through: the checkout holds whatever was committed to it, and writing
// through a committed link would write wherever that link points.
func writeFileIfChanged(path string, data []byte) error {
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("staterepo: replacing symlink %s: %w", path, err)
		}
	} else if old, err := os.ReadFile(path); err == nil && string(old) == string(data) {
		return nil
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("staterepo: writing %s: %w", path, err)
	}
	return nil
}

// writeRepoFileIfChanged is writeFileIfChanged for a path below dir,
// creating the directories it needs. It goes through an os.Root so that
// a directory symlink committed on the way to rel cannot carry the write
// out of dir.
func writeRepoFileIfChanged(dir, rel string, data []byte) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("staterepo: opening %s: %w", dir, err)
	}
	defer root.Close()
	if parent := filepath.Dir(rel); parent != "." {
		if err := root.MkdirAll(parent, 0o700); err != nil {
			return fmt.Errorf("staterepo: preparing %s: %w", filepath.Join(dir, parent), err)
		}
	}
	if info, err := root.Lstat(rel); err == nil && info.Mode()&os.ModeSymlink != 0 {
		if err := root.Remove(rel); err != nil {
			return fmt.Errorf("staterepo: replacing symlink %s: %w", filepath.Join(dir, rel), err)
		}
	} else if old, err := root.ReadFile(rel); err == nil && string(old) == string(data) {
		return nil
	}
	if err := root.WriteFile(rel, data, 0o600); err != nil {
		return fmt.Errorf("staterepo: writing %s: %w", filepath.Join(dir, rel), err)
	}
	return nil
}

// exportTable renders one table's rows. withhold leaves out the columns
// withheldColumns names, which is what every export into a state
// repository wants (ExportConfig) and the host-local handover (Export)
// does not.
func exportTable(ctx context.Context, db querier, dir, table string, withhold bool) ([]byte, error) {
	cols, err := columns(ctx, db, table)
	if err != nil {
		return nil, err
	}
	if withhold {
		cols = withoutWithheld(table, cols)
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("staterepo: table %q has no columns", table)
	}
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = c.name
	}
	query := "SELECT " + quoteList(names) + " FROM " + quote(table) + " ORDER BY " + quoteList(orderBy(cols))
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("staterepo: reading %s: %w", table, err)
	}
	defer rows.Close()

	var out []json.RawMessage
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("staterepo: reading %s: %w", table, err)
		}
		rowValues := make(map[string]any, len(names))
		for i, name := range names {
			rowValues[name] = vals[i]
		}
		if ext, ok := externalTexts[table]; ok {
			path, err := ext.path(rowValues)
			if err != nil {
				return nil, err
			}
			if err := writeRepoFileIfChanged(dir, filepath.FromSlash(path), []byte(textValue(rowValues[ext.column]))); err != nil {
				return nil, err
			}
			rowValues[ext.column] = map[string]string{"file": path}
			for i, name := range names {
				vals[i] = rowValues[name]
			}
		}
		row, err := encodeRow(names, vals)
		if err != nil {
			return nil, fmt.Errorf("staterepo: encoding a row of %s: %w", table, err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("staterepo: reading %s: %w", table, err)
	}
	if out == nil {
		out = []json.RawMessage{}
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// encodeRow renders one row as a JSON object with its columns in the
// table's own order. encoding/json sorts a map's keys alphabetically,
// which would scatter related columns across the file and make a review
// of a settings change harder to read than it needs to be.
func encodeRow(names []string, vals []any) (json.RawMessage, error) {
	var b strings.Builder
	b.WriteByte('{')
	for i, name := range names {
		if i > 0 {
			b.WriteByte(',')
		}
		key, err := json.Marshal(name)
		if err != nil {
			return nil, err
		}
		b.Write(key)
		b.WriteByte(':')
		val, err := json.Marshal(encodeValue(vals[i]))
		if err != nil {
			return nil, err
		}
		b.Write(val)
	}
	b.WriteByte('}')
	return json.RawMessage(b.String()), nil
}

// encodeValue maps what the SQLite driver hands back onto something JSON
// can carry and Import can turn back into the same value.
//
// Timestamps are RFC3339 in UTC: the driver returns a DATETIME column as
// a time.Time (pkg/model/schema.go's own note on why the columns are
// declared that way), and a fixed textual form is what makes a dump
// comparable across hosts in different zones. Bytes that are not valid
// text are base64 in a one-key object, which is the one shape that
// cannot collide with a string a column legitimately holds.
//
// A string is one of those bytes-that-are-not-valid-text cases more often
// than it looks. SQLite does not police the encoding of what goes into a
// TEXT column, and grain stores things there that came off somebody
// else's stdout -- a run's transcript above all -- so a byte sequence
// that is not valid UTF-8 does reach this function. encoding/json cannot
// carry one: it replaces every invalid byte with U+FFFD and reports no
// error, so the dump would quietly hold different bytes than the database
// and a restore from it would hand them back corrupted. Base64 is the
// same answer for the same reason it is the answer for a BLOB, and
// decodeValue puts it back in the column's own storage class.
func encodeValue(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case time.Time:
		return t.UTC().Format(time.RFC3339Nano)
	case []byte:
		return base64Value(t)
	case string:
		if !utf8.ValidString(t) {
			return base64Value([]byte(t))
		}
		return t
	default:
		return v
	}
}

func base64Value(b []byte) map[string]string {
	return map[string]string{"base64": base64.StdEncoding.EncodeToString(b)}
}

// Import replaces every row in db with what dir holds.
//
// Destructive, and named that way in the doc rather than softened: this
// is the restore -- a clone of the repository onto a host that has never
// loaded it becoming that deployment's database -- and it is the only
// direction in which the repository is allowed to say what grain itself
// did. A merged change is not imported this way and never reaches
// anything but the settings tables (ImportTables, below): the dump there
// is always a little behind the database, so replacing a table from it
// deletes rows rather than bringing any back. A caller that cannot
// afford to lose what is in the database should export first.
//
// Everything happens in one transaction with foreign keys deferred, so
// the dump's own table order does not matter and a dump that is
// internally inconsistent (a task_link naming a task that is not in the
// file) is rejected whole at COMMIT rather than applied halfway.
func Import(ctx context.Context, db *sql.DB, dir string) error {
	tables, err := tableNames(ctx, db)
	if err != nil {
		return err
	}
	return importInto(ctx, db, dir, tables, false, nil)
}

// ImportTables is Import over a named subset: the rows of those tables
// are replaced by what dir holds and every other table is left exactly
// as it is.
//
// This is what makes a merged change applicable to a daemon that is
// already running (Apply, in bind.go). A whole-database Import cannot
// be: it clears task and task_run too, underneath a reconcile loop
// holding the very ids it is deleting. A subset that names only tables
// the daemon does not write for itself can be, and is replacement
// within that subset for exactly the reason Import is replacement
// overall -- a merge that deleted a template has to delete it here.
//
// A name no table in db has is skipped rather than refused, the same
// way importTable treats a file the dump does not have: the set of
// settings tables is a constant in this package, and a build whose
// schema does not have one of them yet must not fail to import the
// rest.
//
// Unlike Import, it never writes a column withheldColumns names: what
// the database held there before is what it holds afterwards, whatever
// the file says (withheld.go).
func ImportTables(ctx context.Context, db *sql.DB, dir string, tables []string) error {
	present, err := tableNames(ctx, db)
	if err != nil {
		return err
	}
	has := map[string]bool{}
	for _, t := range present {
		has[t] = true
	}
	var wanted []string
	for _, t := range tables {
		if has[t] {
			wanted = append(wanted, t)
		}
	}
	if len(wanted) == 0 {
		return nil
	}
	return importInto(ctx, db, dir, wanted, true, nil)
}

// importInto is Import's and ImportTables' shared body: clear the named
// tables and refill them from the dump, all in one transaction. withhold
// keeps the withheld columns' values the database already held rather
// than taking them from the dump (ImportTables). A non-nil plan puts back
// the rows a three-way merge keeps at the database's values (merge.go),
// in the same transaction, so no reader ever sees the rewound rows.
func importInto(ctx context.Context, db *sql.DB, dir string, tables []string, withhold bool, plan *mergePlan) error {
	// Before anything is cleared: a dump from before the single-tenant
	// baseline is refused in the startup guard's words, whichever path
	// it arrives by (model.CheckSchemaBaseline).
	found, err := ReadSchemaVersion(dir)
	if err != nil {
		return err
	}
	if err := model.CheckSchemaBaseline(found, model.StateRepository); err != nil {
		return err
	}
	// And one holding rows in a table only a multi-tenant deployment
	// filled, which this build has dropped (retired.go).
	if err := checkRetiredMultiTenantRows(dir); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "PRAGMA defer_foreign_keys = ON"); err != nil {
		return fmt.Errorf("staterepo: deferring foreign keys: %w", err)
	}
	var held []*heldValues
	if withhold {
		for _, t := range tables {
			h, err := readHeld(ctx, tx, t)
			if err != nil {
				return err
			}
			if h != nil {
				held = append(held, h)
			}
		}
	}
	for _, t := range tables {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+quote(t)); err != nil {
			return fmt.Errorf("staterepo: clearing %s: %w", t, err)
		}
	}
	for _, t := range tables {
		if err := importTable(ctx, tx, dir, t, withhold, found); err != nil {
			return err
		}
	}
	for _, h := range held {
		if err := h.restore(ctx, tx); err != nil {
			return err
		}
	}
	if err := plan.restore(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func importTable(ctx context.Context, tx *sql.Tx, dir, table string, withhold bool, found int) error {
	// Whichever shape the repository writes this table in: a nested
	// settings document for the three kinds and their companions
	// (decl.go), the table's own JSON file for everything else -- and
	// for the three kinds too, in a repository written before the
	// documents existed.
	rows, present, err := dumpRowsOf(dir, table)
	if err != nil {
		return err
	}
	if !present {
		// A table the dump has no file for imports as empty. That is what a
		// repository written before this build gained the table looks like,
		// and an empty table is the right answer for it.
		return nil
	}
	path := filepath.FromSlash(DumpPath(table))
	cols, err := columns(ctx, tx, table)
	if err != nil {
		return err
	}
	byName := map[string]column{}
	for _, c := range cols {
		byName[c.name] = c
	}

	for i, row := range rows {
		if table == "grain_config" && found > 0 && found < 74 {
			if tpl, _ := row["task_branch_template"].(string); tpl == model.UnsluggedTaskBranchTemplate {
				row["task_branch_template"] = model.DefaultTaskBranchTemplate
			}
		}
		if ext, ok := externalTexts[table]; ok {
			if ref, isRef := row[ext.column].(map[string]any); isRef {
				rel, hasFile := ref["file"].(string)
				expected, pathErr := ext.path(row)
				if pathErr != nil {
					return fmt.Errorf("staterepo: %s row %d: %w", path, i, pathErr)
				}
				if !hasFile || len(ref) != 1 || filepath.ToSlash(filepath.Clean(filepath.FromSlash(rel))) != expected {
					return fmt.Errorf("staterepo: %s row %d: %s must refer to %s", path, i, ext.column, expected)
				}
			}
		}
		var names []string
		for name := range row {
			if _, ok := byName[name]; !ok {
				// A column this build does not have is skipped rather than
				// refused: that is a dump from a build with one more column
				// than this one, and the rest of the row is still good.
				continue
			}
			if withhold && isWithheld(table, name) {
				// Never set from a state repository (withheld.go);
				// importInto puts back what the database held.
				continue
			}
			names = append(names, name)
		}
		sort.Strings(names)
		if len(names) == 0 {
			return fmt.Errorf("staterepo: %s row %d names no column this build has", path, i)
		}
		args := make([]any, len(names))
		for j, name := range names {
			value, err := resolveFileValue(dir, row[name])
			if err != nil {
				return fmt.Errorf("staterepo: %s row %d, column %s: %w", path, i, name, err)
			}
			v, err := decodeValue(value, byName[name])
			if err != nil {
				return fmt.Errorf("staterepo: %s row %d, column %s: %w", path, i, name, err)
			}
			args[j] = v
		}
		stmt := "INSERT INTO " + quote(table) + " (" + quoteList(names) + ") VALUES (" +
			strings.TrimSuffix(strings.Repeat("?,", len(names)), ",") + ")"
		if _, err := tx.ExecContext(ctx, stmt, args...); err != nil {
			return fmt.Errorf("staterepo: inserting %s row %d: %w", table, i, err)
		}
	}
	return nil
}

// resolveFileValue is value with a {"file": path} reference replaced by
// the contents of that file under dir; any other value comes back as it is.
func resolveFileValue(dir string, value any) (any, error) {
	ref, ok := value.(map[string]any)
	if !ok {
		return value, nil
	}
	rel, fileRef := ref["file"].(string)
	if !fileRef || len(ref) != 1 {
		return value, nil
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("file path escapes the repository")
	}
	// The check above only covers the path as written. The checkout is
	// whatever was committed to it, so a symlink -- the file itself or a
	// directory on the way to it -- could still lead out of it: read
	// through an os.Root, which refuses any path that resolves outside
	// dir, and refuse a symlink outright, since export never writes one.
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", rel, err)
	}
	defer root.Close()
	info, err := root.Lstat(clean)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", rel, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("reading %s: not a regular file", rel)
	}
	body, err := root.ReadFile(clean)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", rel, err)
	}
	return string(body), nil
}

// decodeValue is encodeValue's inverse, told by the column's declared
// type which of the ambiguous cases a JSON string is.
func decodeValue(v any, col column) (any, error) {
	switch t := v.(type) {
	case nil:
		return nil, nil
	case bool:
		return t, nil
	case json.Number:
		if isBoolColumn(col.declType) {
			if i, err := t.Int64(); err == nil {
				return i != 0, nil
			}
		}
		if i, err := t.Int64(); err == nil {
			return i, nil
		}
		return t.Float64()
	case string:
		if isBoolColumn(col.declType) {
			return t == "1" || strings.EqualFold(t, "true"), nil
		}
		if isTimeColumn(col.declType) {
			ts, err := time.Parse(time.RFC3339Nano, t)
			if err != nil {
				return nil, fmt.Errorf("%q is not an RFC3339 timestamp: %w", t, err)
			}
			return ts, nil
		}
		return t, nil
	case map[string]any:
		// Exactly one key, which is what this has always said it wanted
		// and did not check. The one-key object is the shape base64Value
		// writes and the one shape that cannot collide with a string a
		// column legitimately holds (encodeValue), so anything beside the
		// key is a hand edit -- and one that was silently thrown away
		// here rather than reported, on the one path where a value is
		// carried as something other than itself. Refused instead, which
		// is where `grain state check` says so on the pull request.
		enc, ok := t["base64"].(string)
		if !ok || len(t) != 1 {
			return nil, fmt.Errorf("an object value must hold exactly one \"base64\" string")
		}
		raw, err := base64.StdEncoding.DecodeString(enc)
		if err != nil {
			return nil, err
		}
		// Which storage class those bytes go back in is the column's to
		// say, and SQLite's own affinity rule is what says it: bytes bound
		// as a []byte are stored as a BLOB whatever the column is declared
		// as, so a TEXT column whose value was base64 only because it was
		// not valid UTF-8 (encodeValue, above) would come back a blob and
		// the round trip would have changed its type. Bound as a string it
		// comes back exactly as it went in.
		if hasTextAffinity(col.declType) {
			return string(raw), nil
		}
		return raw, nil
	default:
		return nil, fmt.Errorf("cannot store a %T", v)
	}
}

func isBoolColumn(declType string) bool {
	t := strings.ToUpper(declType)
	return strings.Contains(t, "BOOL")
}

func isTimeColumn(declType string) bool {
	t := strings.ToUpper(declType)
	return strings.Contains(t, "DATE") || strings.Contains(t, "TIME")
}

// hasTextAffinity is SQLite's own rule for it (datatype3.html, rule 2):
// a declared type containing CHAR, CLOB or TEXT takes TEXT affinity.
// Spelled out here rather than approximated with `== "TEXT"`, so a
// VARCHAR a later schema declares is not quietly treated as a blob.
func hasTextAffinity(declType string) bool {
	t := strings.ToUpper(declType)
	return strings.Contains(t, "CHAR") || strings.Contains(t, "CLOB") || strings.Contains(t, "TEXT")
}

// column is one column of a table as SQLite itself describes it.
type column struct {
	name     string
	declType string
	pk       int // 1-based position in the primary key, 0 if not part of one
	// dflt is the DDL default, as the literal the schema spells it with
	// ('America/Los_Angeles', '', 0), and invalid for a column that
	// declares none -- which is a default of NULL. Nothing in the dump
	// itself needs it: a row carries every column, so the default is
	// never reached on the way in. The check does (check.go's
	// settingsResets), because a settings file that does not mention a
	// column is a row that lands on exactly this value, and the value is
	// the whole of what makes that worth warning about.
	dflt sql.NullString
	// notNull is the NOT NULL constraint, and it is here for the one
	// question dflt cannot answer on its own: a column that is NOT NULL
	// and declares no default has no value to land on, so a row that
	// leaves it out is not reset to NULL but refused outright. The check
	// has to tell those two apart before it promises a default the schema
	// will not give (check.go's settingsResets).
	notNull bool
}

// querier is whatever can run a read: a *sql.DB, or the *sql.Tx an
// import is already inside. The distinction matters to more than tidiness
// -- a caller that has pinned its pool to one connection (Check) would
// deadlock the moment a statement inside the transaction reached for a
// second one.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func columns(ctx context.Context, db querier, table string) ([]column, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+quote(table)+")")
	if err != nil {
		return nil, fmt.Errorf("staterepo: describing %s: %w", table, err)
	}
	defer rows.Close()
	var out []column
	for rows.Next() {
		var (
			cid        int
			name       string
			declType   sql.NullString
			notNull    int
			defaultVal sql.NullString
			pk         int
		)
		if err := rows.Scan(&cid, &name, &declType, &notNull, &defaultVal, &pk); err != nil {
			return nil, fmt.Errorf("staterepo: describing %s: %w", table, err)
		}
		out = append(out, column{
			name:     name,
			declType: declType.String,
			pk:       pk,
			dflt:     defaultVal,
			notNull:  notNull != 0,
		})
	}
	return out, rows.Err()
}

// orderBy names the columns a table's rows are sorted by: its primary
// key if it has one, and otherwise every column, which is arbitrary but
// stable -- the only property the ordering has to have.
func orderBy(cols []column) []string {
	var pk []column
	for _, c := range cols {
		if c.pk > 0 {
			pk = append(pk, c)
		}
	}
	if len(pk) == 0 {
		names := make([]string, len(cols))
		for i, c := range cols {
			names[i] = c.name
		}
		return names
	}
	sort.Slice(pk, func(i, j int) bool { return pk[i].pk < pk[j].pk })
	names := make([]string, len(pk))
	for i, c := range pk {
		names[i] = c.name
	}
	return names
}

// tableNames lists the real tables in the database, sorted. Views are
// excluded by sqlite_master's own type column: pkg/model derives
// task_state as a view precisely so that nothing writes it, and a dump
// that carried one would invite exactly that.
func tableNames(ctx context.Context, db querier) ([]string, error) {
	rows, err := db.QueryContext(ctx,
		"SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("staterepo: listing tables: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// schemaStampTables names the tables a database has rows in merely by
// existing, and therefore the tables that say nothing about whether
// there is a deployment here.
//
// There are two: model.Store.Init writes the schema it just applied into
// grain_schema and generates the installation identity in grain_instance,
// so a store file that was created a millisecond ago already has rows and
// "no rows anywhere" is never true of a database grain has opened. The
// dump has the same rows for the same reason, so both halves of the question
// below discount the same tables -- see databaseIsEmpty and dumpHasRows,
// which are a matched pair and would answer differently about a fresh
// install if either forgot.
//
// Pinned by a test rather than trusted, because a later Init that seeded
// another bookkeeping row would make every database look populated and
// quietly turn both of those into functions that always answer the same
// thing (empty_internal_test.go).
var schemaStampTables = map[string]bool{"grain_schema": true, "grain_instance": true}

// databaseIsEmpty reports whether db holds nothing a deployment put
// there: no row, in any table, other than the schema stamp above.
//
// It is the question the loaded-head marker cannot answer. That marker
// lives inside the git directory and says whether the *repository* has
// moved under this host; a store file that was deleted, restored from a
// backup that did not include it, or opened at a path that has never
// held one takes none of it with it, so a host can agree with the
// repository exactly and still have nothing to run on. What follows from
// the answer is bind.go's -- Load restores, Apply and sync refuse.
//
// One row is enough to stop, and no row is read: LIMIT 1 with nothing
// scanned out of it, table by table until something has one. On the
// ordinary tick, where this runs before every export, the answer comes
// out of the first table that holds anything.
func databaseIsEmpty(ctx context.Context, db querier) (bool, error) {
	tables, err := tableNames(ctx, db)
	if err != nil {
		return false, err
	}
	for _, table := range tables {
		if schemaStampTables[table] {
			continue
		}
		rows, err := db.QueryContext(ctx, "SELECT 1 FROM "+quote(table)+" LIMIT 1")
		if err != nil {
			return false, fmt.Errorf("staterepo: looking for rows in %s: %w", table, err)
		}
		present := rows.Next()
		err = rows.Err()
		rows.Close()
		if err != nil {
			return false, fmt.Errorf("staterepo: looking for rows in %s: %w", table, err)
		}
		if present {
			return false, nil
		}
	}
	return true, nil
}

// dumpHasRows is databaseIsEmpty's other half, asked of the files: does
// the dump in dir hold a row of anything, and which table was the first
// one found to have one. The name is for the message an operator reads,
// which is worth more with a table in it than without.
//
// Asked only once the database has already answered that it holds
// nothing, which is why it may read files at all: the two together are
// "an export from here would delete the deployment", and the pair of
// them is a good deal cheaper than the export it declines to do.
//
// Streamed to the first element rather than parsed: task_run.json is
// tens of megabytes on a deployment that has been running a while, and
// nothing here needs more than its first token. A file caught
// half-written therefore counts as holding rows, since what it starts
// with is a row, and that is the direction to err in: this guard's
// failure is a deployment deleted from its own repository, and refusing
// an export costs a delay. Nothing is validated beyond that -- the
// reader with an opinion about malformed JSON is Import -- so anything
// that opens with a bracket and has something after it counts, and only
// a file that will not open, or whose first token will not parse at
// all, is read as holding nothing.
func dumpHasRows(dir string) (string, bool) {
	// The settings documents first: they are the three kinds and their
	// companions, they are small, and on a deployment that has anything
	// at all one of them holds a template.
	for _, k := range declKinds() {
		rows, present, err := dumpRowsOf(dir, k.schema.Table)
		if err == nil && present && len(rows) > 0 {
			return k.schema.Table, true
		}
	}
	entries, err := os.ReadDir(filepath.Join(dir, TablesDir))
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		table := strings.TrimSuffix(e.Name(), ".json")
		if schemaStampTables[table] {
			continue
		}
		if fileHoldsARow(filepath.Join(dir, TablesDir, e.Name())) {
			return table, true
		}
	}
	return "", false
}

// fileHoldsARow reports whether path is a JSON array with anything in
// it, reading as little of it as that takes.
func fileHoldsARow(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	// The opening bracket, then whether anything follows it.
	if _, err := dec.Token(); err != nil {
		return false
	}
	return dec.More()
}

func quote(ident string) string {
	return "`" + strings.ReplaceAll(ident, "`", "``") + "`"
}

func quoteList(idents []string) string {
	out := make([]string, len(idents))
	for i, id := range idents {
		out[i] = quote(id)
	}
	return strings.Join(out, ", ")
}

// WriteSchemaVersion stamps the dump with the schema it was written by.
func WriteSchemaVersion(dir string, version int) error {
	return writeFileIfChanged(filepath.Join(dir, SchemaVersionFile),
		[]byte(strconv.Itoa(version)+"\n"))
}

// ReadSchemaVersion reads the stamp. A repository with no stamp at all
// reports 0 and no error: that is either a repository grain has not
// written yet or one an operator created by hand, and both are things
// the caller decides about rather than failures to read a file.
func ReadSchemaVersion(dir string) (int, error) {
	data, err := os.ReadFile(filepath.Join(dir, SchemaVersionFile))
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("staterepo: reading %s: %w", SchemaVersionFile, err)
	}
	v, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("staterepo: %s does not hold a version number: %w", SchemaVersionFile, err)
	}
	return v, nil
}

// HasDump reports whether dir holds an exported database at all, which
// is how a caller tells "a repository grain has already written" from
// "an empty repository an operator just created".
func HasDump(dir string) bool {
	entries, err := os.ReadDir(filepath.Join(dir, TablesDir))
	if err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
				return true
			}
		}
	}
	// A repository can hold the settings documents and nothing else --
	// that is what `grain state export --config-only` writes -- so they
	// count as a dump too.
	for _, k := range declKinds() {
		if _, err := os.Stat(filepath.Join(dir, SettingsDir, k.file)); err == nil {
			return true
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "repos.yaml")); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(dir, "repos.yml")); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(dir, SettingsDir, "repos.yaml")); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(dir, SettingsDir, "repos.yml")); err == nil {
		return true
	}
	reposEntries, err := os.ReadDir(filepath.Join(dir, "repos"))
	if err == nil && len(reposEntries) > 0 {
		return true
	}
	return false
}
