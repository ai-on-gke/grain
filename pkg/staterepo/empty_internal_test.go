package staterepo

// "Is there a database here at all", from both ends: the rows and the
// files.
//
// The pair is load-bearing and is a pair of judgements rather than a
// fact, which is why it is pinned here. Both discount the schema stamp,
// because model.Store.Init writes it into a database the moment it
// creates one and the export writes it out again -- so a database that
// counted it would never read as empty and the guard would never fire,
// and a dump that counted it would read as holding a deployment on a
// brand new install and refuse the first export every deployment makes.
// Neither failure says anything when it happens.

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
)

// aRow writes one row into a database, so that "empty" and "not empty"
// can be told apart without this file knowing anything about a schema
// pkg/model owns.
func aRow(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	if err := model.New(db).PutTemplate(ctx, model.Template{
		ID: "tpl-1", Name: "tpl-1", Title: "Run the nightly sweep", Body: "as configured",
		CreatedAt: time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("writing a row: %v", err)
	}
}

// A database that has just had the schema applied holds nothing. This is
// the one an operator's lost store is replaced by, and if a later Init
// seeds a row of its own -- a default config row, a bootstrap template
// -- then it stops reading as empty, Load stops restoring, and the
// export that empties the repository comes back. Then this fails, and
// the name of whatever was seeded goes on schemaStampTables.
func TestAFreshDatabaseHoldsNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openSchema(t)
	empty, err := databaseIsEmpty(ctx, db)
	if err != nil {
		t.Fatalf("asking whether a fresh database is empty: %v", err)
	}
	if !empty {
		t.Fatal("a database with nothing but the schema in it does not read as empty, " +
			"so a host whose store was lost will export it over its own repository")
	}
	// And one row anywhere is enough to make it a database this host is
	// responsible for, whichever table it is in.
	aRow(t, ctx, db)
	if empty, err := databaseIsEmpty(ctx, db); err != nil || empty {
		t.Fatalf("a database with a template in it reads as empty: %v %v", empty, err)
	}
}

// The files, and the same discounting: a dump of a database with nothing
// in it holds no rows, so a fresh install is not refused its first
// export. A dump of a deployment names the table that proves it.
func TestADumpOfAnEmptyDatabaseHoldsNoRows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openSchema(t)
	dir := t.TempDir()
	if err := Export(ctx, db, dir); err != nil {
		t.Fatalf("exporting an empty database: %v", err)
	}
	if table, has := dumpHasRows(dir); has {
		t.Fatalf("the dump of an empty database reads as holding rows, in %s", table)
	}
	aRow(t, ctx, db)
	if err := Export(ctx, db, dir); err != nil {
		t.Fatalf("exporting: %v", err)
	}
	table, has := dumpHasRows(dir)
	if !has {
		t.Fatal("a dump holding a row reads as holding no rows")
	}
	if table == "" {
		t.Error("the dump was found to hold rows and named no table to say where")
	}
	// A directory that is not a dump at all holds nothing to lose, so
	// nothing is refused over it.
	if _, has := dumpHasRows(filepath.Join(dir, "nothing-here")); has {
		t.Error("a directory with no dump in it reads as holding rows")
	}
	// A file caught half-written errs the other way: what it starts with
	// is a row, and the export this is asked before is the one that would
	// delete the deployment.
	torn := t.TempDir()
	if err := os.MkdirAll(filepath.Join(torn, TablesDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(torn, TablesDir, "task.json"), []byte(`[{"id":`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, has := dumpHasRows(torn); !has {
		t.Error("a half-written dump reads as holding no rows, so an empty database is exported over it")
	}
}

// Every name on schemaStampTables has to be a table this build has, for
// the reason every other list of table names in this package is checked
// against the schema (tables_internal_test.go): a rename in pkg/model
// leaves it naming nothing, and what follows is silence -- the stamp row
// starts counting as a deployment's own, and no database ever reads as
// empty again.
func TestSchemaStampTablesAllExist(t *testing.T) {
	t.Parallel()
	have := schemaTableSet(t, openSchema(t))
	for name := range schemaStampTables {
		if !have[name] {
			t.Errorf("schemaStampTables names %q, which this build's schema does not have", name)
		}
	}
}
