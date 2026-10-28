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
