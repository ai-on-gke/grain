package sqlite_test

import (
	"database/sql"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/model/sqlite"
	"github.com/bwsalmon/grain/pkg/model/sqlite/sqlitetest"
	"github.com/bwsalmon/grain/pkg/model/v2baseline"
)

// A store grain 2.0.0 wrote, and a state repository it dumped, open in
// this build on SQLite (pkg/model/v2baseline).
func TestAStoreGrain2_0WroteOpensInThisBuild(t *testing.T) {
	v2baseline.Run(t, v2baseline.Engine{
		Schema: "schema.sqlite.sql",
		Empty: func(t *testing.T) *sql.DB {
			db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			return db
		},
		Fresh: func(t *testing.T) (*model.Store, *sql.DB) { return sqlitetest.Open(t) },
	})
}
