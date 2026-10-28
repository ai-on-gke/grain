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

package staterepo_test

// The one schema change of 2026-09-06 that CREATE TABLE IF NOT EXISTS
// cannot make to a database that already exists: task_custom_field's
// primary key, widened from (task_id, name) to (task_id, name, value)
// when a custom field learned to hold a set (model.CustomField.Multiple).
//
// pkg/model/sqlite's own tests cover the rebuild -- that
// Store.ensureTaskCustomFieldValueKey turns the old table into the new
// one and keeps its rows. This file covers the half that lives here: a
// dump carrying two values for one (task, field), which is a file only a
// build with the widened key can write and only a store with the widened
// key can take. Nothing pinned the order of those two events, and the
// order is the whole of it -- the rebuild runs in Store.Init and the
// import runs after it (cmd/grain's openStore, then Load), and an import
// that reached a store still on the old key would fail on the primary
// key with a deployment's whole restore in the transaction.

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// storeBeforeTheWidening is a database as the build before the widening
// left it: this build's schema with task_custom_field put back the way
// it was, keyed on (task_id, name), holding the one value per field such
// a build could store.
//
// Built by rebuilding the table rather than by checking in an old
// database file, for the reason the rest of this package builds its old
// builds the same way: the shape is what matters, and a fixture database
// would be a second copy of the schema to keep current.
func storeBeforeTheWidening(t *testing.T, ctx context.Context) (*model.Store, *sql.DB) {
	t.Helper()
	store, db := openDB(t)
	for _, stmt := range []string{
		"DROP TABLE `task_custom_field`",
		"CREATE TABLE `task_custom_field` (" +
			"`task_id` TEXT NOT NULL, `name` TEXT NOT NULL, `value` TEXT NOT NULL, " +
			"PRIMARY KEY (`task_id`, `name`))",
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if err := store.PutTask(ctx, task("old-1")); err != nil {
		t.Fatalf("putting a task: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO `task_custom_field` (`task_id`, `name`, `value`) VALUES ('old-1', 'customer', 'acme')",
	); err != nil {
		t.Fatalf("writing the value that build could store: %v", err)
	}
	return store, db
}

// dumpWithTwoValuesForOneField is what a build with the widened key
// exports for a task carrying a multi-valued field: two rows in
// task_custom_field with the same (task_id, name) and different values.
func dumpWithTwoValuesForOneField(t *testing.T, ctx context.Context) string {
	t.Helper()
	store, db := openDB(t)
	if err := store.PutCustomField(ctx, model.CustomField{
		Name: "customer", Label: "Customer", Type: model.CustomFieldEnum,
		Options: []string{"acme", "globex"}, Multiple: true,
	}); err != nil {
		t.Fatalf("declaring the field: %v", err)
	}
	tk := task("old-1")
	tk.CustomFields = map[string][]string{"customer": {"acme", "globex"}}
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatalf("putting a task: %v", err)
	}
	dir := t.TempDir()
	if err := staterepo.Export(ctx, db, dir); err != nil {
		t.Fatalf("exporting: %v", err)
	}
	if err := staterepo.WriteSchemaVersion(dir, model.SchemaVersion); err != nil {
		t.Fatal(err)
	}
	body := readGolden(t, dir+"/"+staterepo.TablesDir+"/task_custom_field.json")
	if !strings.Contains(body, "acme") || !strings.Contains(body, "globex") {
		t.Fatalf("the dump does not carry both values, so there is nothing here to import:\n%s", body)
	}
	return dir
}

// Why that order is not incidental: the same import into a store whose
// rebuild has not run yet fails, and fails on the whole dump.
//
// This is not a bug to fix -- an import is a replacement of everything
// in one transaction, so a row it cannot store has to stop it rather
// than be dropped -- it is the reason Init runs first. A deployment that
// imported before opening its store would come up with no state at all
// rather than with one value missing, so the failure is the loud kind;
// this pins that it is still loud, and that nothing has quietly started
// swallowing the row instead.
func TestADumpWithTwoValuesForOneFieldIsRefusedBeforeTheKeyIsWidened(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, db := storeBeforeTheWidening(t, ctx)
	dir := dumpWithTwoValuesForOneField(t, ctx)

	err := staterepo.Import(ctx, db, dir)
	if err == nil {
		t.Fatal("a store still keyed (task_id, name) took a dump holding two values for one field")
	}
	if !strings.Contains(err.Error(), "task_custom_field") {
		t.Errorf("the failure does not name the table it is about: %v", err)
	}
	// Rolled back whole: the task the dump would have imported is not
	// there, which is what makes this a failed start rather than a
	// half-restored deployment.
	var tasks int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM `task`").Scan(&tasks); err != nil {
		t.Fatalf("counting the tasks: %v", err)
	}
	if tasks != 1 {
		t.Errorf("%d tasks in the store after the refused import, want the 1 that was there before", tasks)
	}
}

// The dump side of the same widening, one step further out: a store that
// has been through the rebuild exports what it holds, and the file it
// writes is one a build without the widened key would refuse -- which is
// what `grain state check` says about a rollback, rather than what a
// deployment finds out at start.
func TestADumpWrittenAfterTheWideningIsRefusedByAStoreBeforeIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := dumpWithTwoValuesForOneField(t, ctx)
	_, db := storeBeforeTheWidening(t, ctx)

	if _, err := staterepo.Check(ctx, db, dir, model.SchemaVersion); err == nil {
		t.Fatal("checking this dump against a store on the old key passed")
	} else if !strings.Contains(err.Error(), "task_custom_field") {
		t.Errorf("the check does not name the table the dump cannot be loaded into: %v", err)
	}
}
