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

package sqlitetest_test

import (
	"context"
	"database/sql"
	"reflect"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/model/sqlite/sqlitetest"
)

type schemaObject struct {
	Type  string
	Name  string
	Table string
	SQL   string
}

func TestOpenMatchesAStoreInitializedFromScratch(t *testing.T) {
	store, copiedDB := sqlitetest.Open(t)
	copied := schema(t, copiedDB)

	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("initializing the copied template: %v", err)
	}
	if after := schema(t, copiedDB); !reflect.DeepEqual(after, copied) {
		t.Fatal("Init changed the schema copied from the template")
	}

	freshDB, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening fresh sqlite database: %v", err)
	}
	t.Cleanup(func() { _ = freshDB.Close() })
	if err := model.New(freshDB).Init(context.Background()); err != nil {
		t.Fatalf("initializing fresh sqlite database: %v", err)
	}
	if fresh := schema(t, freshDB); !reflect.DeepEqual(copied, fresh) {
		t.Fatalf("template schema differs from fresh Init:\ntemplate: %#v\nfresh: %#v", copied, fresh)
	}
}

func schema(t *testing.T, db *sql.DB) []schemaObject {
	t.Helper()
	rows, err := db.Query(`
		SELECT type, name, tbl_name, coalesce(sql, '')
		FROM sqlite_schema
		WHERE name NOT LIKE 'sqlite_%'
		ORDER BY type, name`)
	if err != nil {
		t.Fatalf("reading schema: %v", err)
	}
	defer rows.Close()
	var objects []schemaObject
	for rows.Next() {
		var object schemaObject
		if err := rows.Scan(&object.Type, &object.Name, &object.Table, &object.SQL); err != nil {
			t.Fatalf("scanning schema: %v", err)
		}
		objects = append(objects, object)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading schema rows: %v", err)
	}
	return objects
}
