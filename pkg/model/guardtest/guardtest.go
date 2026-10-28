// Package guardtest is the single-tenant startup guard's test
// (model.Store.CheckSingleTenant, docs/single-tenant-reset.md stage 5),
// written once and run against every store driver: pkg/model/sqlite and
// pkg/model/postgres each call Run with their own way of opening a store.
//
// It covers both places the guard stands: Init, which a daemon's start
// runs, and staterepo.CheckMigration, which is `grain state check
// -migrate` and the rehearsal an upgrade runs in the new build before it
// cuts over.
package guardtest

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// Open opens a fresh, initialized store on the driver under test.
type Open func(t *testing.T) (*model.Store, *sql.DB)

// Case is a data dir this build refuses: one only a multi-tenant
// deployment writes, or one stamped from before the single-tenant
// baseline. seed puts the rows there, and table is where one of them
// sits, so the test can say the guard left it alone.
type Case struct {
	Name  string
	Table string
	// Want is a word the refusal has to name, so an operator can tell
	// which rows stopped the start.
	Want string
	Seed func(ctx context.Context, t *testing.T, store *model.Store, db *sql.DB)
}

// Cases are the guard's list, one data dir per item on it. A second team
// is not one: schema 24 dropped the team table, so a data dir at this
// build's schema has nowhere to hold one, and the guard asks a dump
// about it instead (Run's "a dump holding a second team").
var Cases = []Case{
	{"a schema from before the baseline", "grain_schema", "from before the single-tenant baseline", func(ctx context.Context, t *testing.T, _ *model.Store, db *sql.DB) {
		exec(ctx, t, db, "UPDATE `grain_schema` SET `version` = ? WHERE `id` = 1", model.BaselineSchemaVersion-1)
	}},
}

// singleTenant seeds what an ordinary single-tenant data dir holds that
// sits near the guard's list without being on it: a task with read
// targets, write targets and capability grants. (The default team's row,
// and the members builds before 3e-iv-b2c-iii seeded on it, went with
// the team and team_user tables at schemas 24 and 21; staterepo's
// retired.go asks a dump about them.)
func singleTenant(ctx context.Context, t *testing.T, store *model.Store, db *sql.DB) {
	settings(ctx, t, store)
	exec(ctx, t, db, "INSERT INTO `task_read` (`task_id`, `owner`, `name`) VALUES (?, ?, ?)", "1", "acme", "docs")
	exec(ctx, t, db, "INSERT INTO `task_write` (`task_id`, `owner`, `name`, `base`) VALUES (?, ?, ?, ?)", "1", "acme", "site", "main")
	exec(ctx, t, db, "INSERT INTO `task_grant` (`task_id`, `capability`, `via`) VALUES (?, ?, ?)", "1", "self-debug", "label")
}

// Run is the whole test, against stores open returns.
func Run(t *testing.T, open Open) {
	t.Run("a single-tenant data dir starts and migrates", func(t *testing.T) {
		ctx := context.Background()
		store, db := open(t)
		singleTenant(ctx, t, store, db)
		if err := store.Init(ctx); err != nil {
			t.Fatalf("Init on a single-tenant data dir: %v", err)
		}
		if _, err := migrate(ctx, t, open, db); err != nil {
			t.Fatalf("CheckMigration of a single-tenant dump: %v", err)
		}
	})
	t.Run("a dump holding a second team", func(t *testing.T) {
		ctx := context.Background()
		store, db := open(t)
		singleTenant(ctx, t, store, db)
		// The team table as a dump from before schema 24 carries it: the
		// default team's row, which a single-tenant deployment had too,
		// and one besides it.
		teams := `[{"id": "default", "name": "admin"}, {"id": "beta", "name": "beta"}]`
		_, err := migrateWith(ctx, t, open, db, map[string]string{"team": teams})
		refused(t, err, Case{Table: "team", Want: "1 teams besides the default one"}, model.StateRepository)

		// The default team's row alone is a single-tenant deployment's.
		_, err = migrateWith(ctx, t, open, db, map[string]string{"team": `[{"id": "default", "name": "admin"}]`})
		if err != nil {
			t.Fatalf("CheckMigration of a dump holding only the default team: %v", err)
		}
	})
	for _, c := range Cases {
		t.Run(c.Name, func(t *testing.T) {
			t.Run("Init refuses it", func(t *testing.T) {
				ctx := context.Background()
				store, db := open(t)
				c.Seed(ctx, t, store, db)
				before := count(ctx, t, db, c.Table)
				err := store.Init(ctx)
				refused(t, err, c, model.DataDir)
				if after := count(ctx, t, db, c.Table); after != before {
					t.Errorf("%s holds %d rows after the refusal and %d before: the guard must not touch a row", c.Table, after, before)
				}
			})
			t.Run("state check -migrate refuses it", func(t *testing.T) {
				ctx := context.Background()
				store, db := open(t)
				c.Seed(ctx, t, store, db)
				_, err := migrate(ctx, t, open, db)
				refused(t, err, c, model.StateRepository)
			})
		})
	}
}

// migrate exports db the way an upgrade's Prepare does, stamped with the
// schema the store is stamped with, and rehearses importing it into a
// fresh store the way `grain state check -migrate` does.
func migrate(ctx context.Context, t *testing.T, open Open, db *sql.DB) (staterepo.CheckReport, error) {
	t.Helper()
	return migrateWith(ctx, t, open, db, nil)
}

// migrateWith is migrate with extra table files written into the export
// before it is rehearsed, each a table's JSON rows by its name: a table
// this build no longer has, as a dump from an older one carries it.
func migrateWith(ctx context.Context, t *testing.T, open Open, db *sql.DB, extra map[string]string) (staterepo.CheckReport, error) {
	t.Helper()
	dir := t.TempDir()
	if err := staterepo.Export(ctx, db, dir); err != nil {
		t.Fatalf("Export: %v", err)
	}
	for table, rows := range extra {
		path := filepath.Join(dir, filepath.FromSlash(staterepo.DumpPath(table)))
		if err := os.WriteFile(path, []byte(rows), 0o600); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
	}
	var version int
	if err := db.QueryRowContext(ctx, "SELECT `version` FROM `grain_schema` WHERE `id` = 1").Scan(&version); err != nil {
		t.Fatalf("reading the store's schema version: %v", err)
	}
	if err := staterepo.WriteSchemaVersion(dir, version); err != nil {
		t.Fatalf("WriteSchemaVersion: %v", err)
	}
	_, scratch := open(t)
	return staterepo.CheckMigration(ctx, scratch, dir, t.TempDir(), model.SchemaVersion)
}

// refused fails t unless err is the guard's refusal of c, naming in.
// A schema from before the baseline is ErrBeforeBaseline, whose words do
// not say multi-tenant: the rows it seeds are a single-tenant data dir's
// (task 4800).
func refused(t *testing.T, err error, c Case, in model.Holder) {
	t.Helper()
	want := model.ErrMultiTenantData
	if c.Table == "grain_schema" {
		want = model.ErrBeforeBaseline
	}
	if !errors.Is(err, want) {
		t.Fatalf("got %v, want %v", err, want)
	}
	if want == model.ErrBeforeBaseline && strings.Contains(err.Error(), "multi-tenant") {
		t.Errorf("the refusal of a schema from before the baseline says multi-tenant:\n%v", err)
	}
	for _, w := range []string{c.Want, "single-tenant only", "this " + string(in), "fresh data dir",
		"grain connections import", "repositories again", "Nothing in this " + string(in) + " has been changed"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("the refusal does not say %q:\n%v", w, err)
		}
	}
}

// settings stores a deployment's settings row, the way a daemon's first
// start does (daemon.Config.ToModelConfig).
func settings(ctx context.Context, t *testing.T, store *model.Store) {
	t.Helper()
	cfg := model.NewDeploymentConfig()
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}
}

func exec(ctx context.Context, t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(ctx, query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

func count(ctx context.Context, t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM `"+table+"`").Scan(&n); err != nil {
		t.Fatalf("counting %s: %v", table, err)
	}
	return n
}
