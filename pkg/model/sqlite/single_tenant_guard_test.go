package sqlite_test

import (
	"database/sql"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/guardtest"
	"github.com/ai-on-gke/grain/pkg/model/sqlite/sqlitetest"
)

// The single-tenant startup guard (docs/single-tenant-reset.md, stage 5)
// on SQLite: Init and `grain state check -migrate` both refuse a
// multi-tenant deployment's rows, and both take a single-tenant one.
func TestSingleTenantGuard(t *testing.T) {
	guardtest.Run(t, func(t *testing.T) (*model.Store, *sql.DB) { return sqlitetest.Open(t) })
}
