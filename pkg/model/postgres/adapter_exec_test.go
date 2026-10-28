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
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"regexp"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// TestAdapterTableMapsNameOnlySchemaTables is the reverse of the two
// coverage tests above: an entry in tablePrimaryKeys or
// autoIncrementTables that names no table in model.Tables is dead weight
// that reads as though the table exists.
func TestAdapterTableMapsNameOnlySchemaTables(t *testing.T) {
	reTable := regexp.MustCompile(`(?is)^\s*CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?["` + "`" + `]?([a-zA-Z0-9_]+)["` + "`" + `]?`)
	tables := map[string]bool{
		// Legacy name Store.Init renames to suite_sequence; a database
		// written before the rename still inserts into it during Init.
		"task_suite_sequence": true,
	}
	for _, ddl := range model.Tables {
		if m := reTable.FindStringSubmatch(strings.TrimSpace(ddl)); len(m) == 2 {
			tables[m[1]] = true
		}
	}
	for table := range tablePrimaryKeys {
		if !tables[table] {
			t.Errorf("tablePrimaryKeys names %q, which is not a table in model.Tables", table)
		}
	}
	for table := range autoIncrementTables {
		if !tables[table] {
			t.Errorf("autoIncrementTables names %q, which is not a table in model.Tables", table)
		}
	}
}

// fakeReturningConn is an inner driver.Conn whose QueryContext answers
// the RETURNING query ExecContext rewrites an auto-increment INSERT into.
type fakeReturningConn struct {
	ids      []int64
	nextErr  error // returned by Next once ids are exhausted, instead of io.EOF
	closeErr error
	query    string
}

func (c *fakeReturningConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unused") }
func (c *fakeReturningConn) Close() error                        { return nil }
func (c *fakeReturningConn) Begin() (driver.Tx, error)           { return nil, errors.New("unused") }

func (c *fakeReturningConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.query = query
	return &fakeRows{ids: c.ids, nextErr: c.nextErr, closeErr: c.closeErr}, nil
}

type fakeRows struct {
	ids      []int64
	nextErr  error
	closeErr error
}

func (r *fakeRows) Columns() []string { return []string{"id"} }
func (r *fakeRows) Close() error      { return r.closeErr }
func (r *fakeRows) Next(dest []driver.Value) error {
	if len(r.ids) == 0 {
		if r.nextErr != nil {
			return r.nextErr
		}
		return io.EOF
	}
	dest[0] = r.ids[0]
	r.ids = r.ids[1:]
	return nil
}

func TestExecAutoIncrementInsertCountsEveryReturnedRow(t *testing.T) {
	inner := &fakeReturningConn{ids: []int64{7, 8, 9}}
	conn := &adaptedConn{inner: inner}
	res, err := conn.ExecContext(context.Background(),
		"INSERT INTO `task_comment` (`task_id`, `body`) SELECT `id`, 'x' FROM `task`", nil)
	if err != nil {
		t.Fatalf("ExecContext: %v", err)
	}
	if !strings.HasSuffix(inner.query, `RETURNING "id"`) {
		t.Fatalf("query was not rewritten with RETURNING: %s", inner.query)
	}
	if n, _ := res.RowsAffected(); n != 3 {
		t.Errorf("RowsAffected = %d, want 3", n)
	}
	if id, _ := res.LastInsertId(); id != 9 {
		t.Errorf("LastInsertId = %d, want 9 (the last row inserted)", id)
	}
}

func TestExecAutoIncrementInsertReportsLateErrors(t *testing.T) {
	for name, inner := range map[string]*fakeReturningConn{
		"next":  {ids: []int64{1}, nextErr: errors.New("duplicate key on row 2")},
		"close": {ids: []int64{1}, closeErr: errors.New("duplicate key on row 2")},
	} {
		t.Run(name, func(t *testing.T) {
			conn := &adaptedConn{inner: inner}
			_, err := conn.ExecContext(context.Background(),
				"INSERT INTO `task_comment` (`task_id`) VALUES (?)", []driver.NamedValue{{Ordinal: 1, Value: "t"}})
			if err == nil || !strings.Contains(err.Error(), "duplicate key") {
				t.Fatalf("ExecContext error = %v, want the error from the rows", err)
			}
		})
	}
}
