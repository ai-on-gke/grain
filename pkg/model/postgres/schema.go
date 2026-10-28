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

package postgres

import (
	"github.com/ai-on-gke/grain/pkg/model"
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
