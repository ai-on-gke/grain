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

package postgres_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/postgres"
	"github.com/ai-on-gke/grain/pkg/model/postgres/postgrestest"
)

// ArchiveStore sets a whole store aside -- tables, their rows, the views
// over them -- and leaves the current schema empty for Init to build a
// fresh store in, which is what a schema bump does to a Postgres store
// instead of renaming a SQLite directory (grain/task-4851).
func TestArchiveStoreKeepsTheOldRowsAndLeavesRoomForAFreshStore(t *testing.T) {
	store, db := postgrestest.Open(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx,
		"INSERT INTO `memory_document` (`name`, `document`, `summary`, `text`) VALUES (?, ?, ?, ?)",
		"kept", "doc", "", "the old store's row"); err != nil {
		t.Fatalf("seeding a row: %v", err)
	}
	var current string
	if err := db.QueryRowContext(ctx, "SELECT current_schema()").Scan(&current); err != nil {
		t.Fatal(err)
	}

	archive, err := postgres.ArchiveStore(ctx, db, "_schema19_20261020_010203")
	if err != nil {
		t.Fatalf("ArchiveStore: %v", err)
	}
	t.Cleanup(func() { _, _ = db.Exec(fmt.Sprintf(`DROP SCHEMA IF EXISTS "%s" CASCADE`, archive)) })
	if !strings.HasSuffix(archive, "_schema19_20261020_010203") || len(archive) > 63 {
		t.Errorf("archived into %q", archive)
	}

	var left int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = current_schema() AND c.relkind IN ('r', 'v', 'S')`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Errorf("%d relations were left in %s", left, current)
	}
	var content string
	if err := db.QueryRowContext(ctx, fmt.Sprintf(`SELECT "text" FROM "%s"."memory_document" WHERE "name" = 'kept'`,
		archive)).Scan(&content); err != nil || content != "the old store's row" {
		t.Errorf("the row is not in %s: %q, %v", archive, content, err)
	}

	if err := store.Init(ctx); err != nil {
		t.Fatalf("Init on the emptied schema: %v", err)
	}
	var fresh int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM `memory_document`").Scan(&fresh); err != nil {
		t.Fatal(err)
	}
	if fresh != 0 {
		t.Errorf("the fresh store holds %d memory documents", fresh)
	}
	if version, err := store.SchemaVersion(ctx); err != nil || version != model.SchemaVersion {
		t.Errorf("the fresh store is stamped %d (%v)", version, err)
	}
}

func TestArchiveSchemaNameFitsPostgresIdentifiers(t *testing.T) {
	suffix := "_schema19_20261020_010203"
	if got := postgres.ArchiveSchemaName("public", suffix); got != "public"+suffix {
		t.Errorf("got %q", got)
	}
	long := strings.Repeat("s", 60)
	got := postgres.ArchiveSchemaName(long, suffix)
	if len(got) != 63 || !strings.HasSuffix(got, suffix) {
		t.Errorf("got %q (%d)", got, len(got))
	}
}
