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

// Package sqlitetest opens disposable, schema-initialized SQLite databases for tests.
package sqlitetest

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

type templateFile struct {
	name string
	mode os.FileMode
	data []byte
}

var (
	templateOnce  sync.Once
	templateFiles []templateFile
	templateErr   error
)

// Open returns an empty store with the current schema. The schema is applied
// once per test binary and the resulting database directory is copied for
// each caller; tests of Init and migrations should use sqlite.Open directly.
func Open(t testing.TB) (*model.Store, *sql.DB) {
	t.Helper()
	templateOnce.Do(makeTemplate)
	if templateErr != nil {
		t.Fatalf("preparing sqlite schema template: %v", templateErr)
	}

	dir := t.TempDir()
	for _, file := range templateFiles {
		if err := os.WriteFile(filepath.Join(dir, file.name), file.data, file.mode); err != nil {
			t.Fatalf("copying sqlite schema template: %v", err)
		}
	}
	db, err := sqlite.Open(sqlite.DefaultConfig(dir))
	if err != nil {
		t.Fatalf("opening copied sqlite database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return model.New(db), db
}

func makeTemplate() {
	dir, err := os.MkdirTemp("", "grain-sqlite-test-template-")
	if err != nil {
		templateErr = err
		return
	}
	defer os.RemoveAll(dir)

	db, err := sqlite.Open(sqlite.DefaultConfig(dir))
	if err != nil {
		templateErr = err
		return
	}
	if err := model.New(db).Init(context.Background()); err != nil {
		_ = db.Close()
		templateErr = fmt.Errorf("applying schema: %w", err)
		return
	}
	// Init writes in WAL mode. Put every committed frame in grain.db before
	// taking the snapshot, then close the last connection so every file in
	// the database directory is stable.
	if _, err := db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		_ = db.Close()
		templateErr = fmt.Errorf("checkpointing schema: %w", err)
		return
	}
	if err := db.Close(); err != nil {
		templateErr = fmt.Errorf("closing schema template: %w", err)
		return
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		templateErr = err
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			templateErr = fmt.Errorf("unexpected directory %s", entry.Name())
			return
		}
		info, err := entry.Info()
		if err != nil {
			templateErr = err
			return
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			templateErr = err
			return
		}
		templateFiles = append(templateFiles, templateFile{
			name: entry.Name(),
			mode: info.Mode().Perm(),
			data: data,
		})
	}
}
