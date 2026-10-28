package postgres_test

import (
	"database/sql"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/postgres/postgrestest"
	"github.com/ai-on-gke/grain/pkg/model/v2baseline"
)

// A store grain 2.0.0 wrote, and a state repository it dumped, open in
// this build on Postgres (pkg/model/v2baseline).
func TestAStoreGrain2_0WroteOpensInThisBuild(t *testing.T) {
	v2baseline.Run(t, v2baseline.Engine{
		Schema: "schema.postgres.sql",
		Empty:  func(t *testing.T) *sql.DB { return postgrestest.OpenEmpty(t) },
		Fresh:  func(t *testing.T) (*model.Store, *sql.DB) { return postgrestest.Open(t) },
	})
}
