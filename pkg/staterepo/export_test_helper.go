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
)

// TableResetsWithColumnsForTest exposes tableResets with explicitly specified columns for tests.
func TableResetsWithColumnsForTest(ctx context.Context, db *sql.DB, table string, cols []ColumnForTest, rows []map[string]any) ([]Reset, error) {
	var c []column
	for _, col := range cols {
		c = append(c, column{
			name:     col.Name,
			declType: col.DeclType,
			notNull:  col.NotNull,
			dflt:     sql.NullString{String: col.DefaultVal, Valid: col.DefaultVal != ""},
			pk:       col.PK,
		})
	}
	return tableResets(ctx, db, table, c, rows)
}

// ColumnForTest describes a column for TableResetsWithColumnsForTest.
type ColumnForTest struct {
	Name       string
	DeclType   string
	NotNull    bool
	DefaultVal string
	PK         int
}
