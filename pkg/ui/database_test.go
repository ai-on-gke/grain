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

package ui_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/postgres"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestGetDatabaseStatusRoute(t *testing.T) {
	dataDir := t.TempDir()
	db, err := sqlite.Open(sqlite.DefaultConfig(dataDir))
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	defer db.Close()

	store := model.New(db)
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("init store: %v", err)
	}
	makeDatabaseAdmin(t, store)

	cfg := ui.Config{
		DBDriver: "sqlite",
		DBDSN:    dataDir,
		DataDir:  dataDir,
		SourceDB: db,
		Actor:    databaseAdmin,
	}

	srv := ui.NewServer(cfg, store)

	req := httptest.NewRequest(http.MethodGet, "/api/database", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Driver            string `json:"driver"`
		DSN               string `json:"dsn"`
		IsSqlite          bool   `json:"isSqlite"`
		PostgresAvailable bool   `json:"postgresAvailable"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshalling response: %v", err)
	}

	if resp.Driver != "sqlite" {
		t.Errorf("expected driver sqlite, got %s", resp.Driver)
	}
	if !resp.IsSqlite {
		t.Errorf("expected isSqlite true, got false")
	}
}

// No route copies, migrates or switches a running daemon's store: a
// store moves only offline (docs/database.md), because a copy taken under
// a live daemon loses every write it makes afterwards. Each of the routes
// that used to is gone, for a system administrator too.
func TestNoRouteMigratesTheStoreOfARunningDaemon(t *testing.T) {
	dataDir := t.TempDir()
	db, err := sqlite.Open(sqlite.DefaultConfig(dataDir))
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	defer db.Close()
	store := model.New(db)
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("init store: %v", err)
	}
	makeDatabaseAdmin(t, store)
	srv := ui.NewServer(ui.Config{DBDriver: "sqlite", DBDSN: dataDir, DataDir: dataDir, SourceDB: db, Actor: databaseAdmin}, store)

	for _, path := range []string{
		"/api/database/upgrade",
		"/api/database/test",
		"/api/upgrade/postgres",
		"/api/settings/db/migrate-to-postgres",
	} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"dsn":"postgres://grain@127.0.0.1:1/grain"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s = %d, want it gone (404 or 405): %s", path, rec.Code, rec.Body.String())
		}
	}
	if _, err := os.Stat(filepath.Join(dataDir, postgres.DatabaseConfigFile)); !os.IsNotExist(err) {
		t.Errorf("%s was written: %v", postgres.DatabaseConfigFile, err)
	}
}

func makeDatabaseAdmin(t *testing.T, store *model.Store) {
	t.Helper()
	if err := store.PutConfig(context.Background(), model.Config{PollInterval: model.DefaultPollInterval}); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}
}

var databaseAdmin = model.Principal{ID: "admin@example.com", Kind: model.PrincipalHuman}
