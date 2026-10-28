package postgres

import (
	"github.com/bwsalmon/grain/pkg/model"
)

// Statements returns the PostgreSQL DDL statements required to initialize
// all tables, indexes, and views matching pkg/model/schema.go.
func Statements() []string {
	var stmts []string
	for _, stmt := range model.Statements() {
		stmts = append(stmts, adaptQuery(stmt))
	}
	// Add sqlite_master view for compatibility with test queries
	stmts = append(stmts,
		`DROP VIEW IF EXISTS "sqlite_master" CASCADE`,
		`CREATE OR REPLACE VIEW "sqlite_master" AS
SELECT
  CASE WHEN table_type = 'VIEW' THEN 'view' ELSE 'table' END::text AS "type",
  table_name::text AS "name",
  table_name::text AS "tbl_name",
  0::int AS "rootpage",
  ''::text AS "sql"
FROM information_schema.tables
WHERE table_schema = current_schema()
UNION ALL
SELECT
  'index'::text AS "type",
  indexname::text AS "name",
  tablename::text AS "tbl_name",
  0::int AS "rootpage",
  ''::text AS "sql"
FROM pg_indexes
WHERE schemaname = current_schema()`,
	)
	return stmts
}
