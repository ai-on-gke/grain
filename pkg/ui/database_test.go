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
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/postgres"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/secrets"
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

func TestUpgradeDatabaseRouteWithCustomUpgradeHandler(t *testing.T) {
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

	called := false
	var operator confirmationLog
	secretStore := secrets.New(t.TempDir())
	cfg := ui.Config{
		DBDriver:        "sqlite",
		DataDir:         dataDir,
		Secrets:         secretStore,
		SourceDB:        db,
		Actor:           databaseAdmin,
		DatabaseConfirm: operator.record,
		UpgradePostgres: func(ctx context.Context, dsn string, migrateData bool) (*postgres.UpgradeResult, error) {
			called = true
			return &postgres.UpgradeResult{
				TablesMigrated: 12,
				TableNames:     []string{"tasks", "comments"},
				Driver:         "postgres",
				DSN:            "postgres://user:******@localhost:5432/grain",
			}, nil
		},
	}

	srv := ui.NewServer(cfg, store)

	reqBody := `{"dsn":"postgres://user:secret@localhost:5432/grain","migrateData":true}`
	rec := confirmedUpgrade(t, srv, &operator, "/api/database/upgrade", reqBody)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !called {
		t.Errorf("expected custom UpgradePostgres to be called")
	}

	var resp struct {
		OK             bool     `json:"ok"`
		Message        string   `json:"message"`
		TablesMigrated int      `json:"tablesMigrated"`
		TableNames     []string `json:"tableNames"`
		Driver         string   `json:"driver"`
		DSN            string   `json:"dsn"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshalling: %v", err)
	}

	if !resp.OK {
		t.Errorf("expected ok=true")
	}
	if resp.TablesMigrated != 12 {
		t.Errorf("expected 12 tables migrated, got %d", resp.TablesMigrated)
	}

	// Verify db-config.json was written, without the password, which
	// went to the secret store instead.
	raw, err := os.ReadFile(filepath.Join(dataDir, postgres.DatabaseConfigFile))
	if err != nil {
		t.Fatalf("reading db-config.json: %v", err)
	}
	if strings.Contains(string(raw), ":secret@") {
		t.Errorf("db-config.json holds the password in clear text: %s", raw)
	}
	savedCfg, err := postgres.LoadDatabaseConfig(context.Background(), dataDir, secretStore)
	if err != nil {
		t.Fatalf("LoadDatabaseConfig: %v", err)
	}
	if savedCfg == nil || savedCfg.Driver != "postgres" {
		t.Fatalf("expected saved postgres config, got %+v", savedCfg)
	}
	if savedCfg.DSN != "postgres://user:secret@localhost:5432/grain" {
		t.Errorf("loaded DSN = %q, want the password merged back in", savedCfg.DSN)
	}
}

func TestDatabaseHTTPClient(t *testing.T) {
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

	var operator confirmationLog
	cfg := ui.Config{
		DBDriver:        "sqlite",
		DBDSN:           dataDir,
		DataDir:         dataDir,
		Secrets:         secrets.New(t.TempDir()),
		SourceDB:        db,
		Actor:           databaseAdmin,
		DatabaseConfirm: operator.record,
		UpgradePostgres: func(ctx context.Context, dsn string, migrateData bool) (*postgres.UpgradeResult, error) {
			return &postgres.UpgradeResult{
				TablesMigrated: 5,
				TableNames:     []string{"tasks"},
				Driver:         "postgres",
				DSN:            "postgres://user:******@localhost:5432/grain",
			}, nil
		},
	}

	srv := ui.NewServer(cfg, store)
	httpServer := httptest.NewServer(srv)
	defer httpServer.Close()

	client := serverAccessClient(srv, httpServer.URL)
	status, err := client.DatabaseStatus(context.Background())
	if err != nil {
		t.Fatalf("client.DatabaseStatus: %v", err)
	}
	if status.Driver != "sqlite" || !status.IsSqlite {
		t.Errorf("unexpected status: %+v", status)
	}

	opts := ui.DatabaseUpgradeOptions{
		DSN:         "postgres://user:secret@localhost:5432/grain",
		MigrateData: true,
	}
	res, err := client.UpgradeDatabase(context.Background(), opts)
	if err != nil {
		t.Fatalf("client.UpgradeDatabase: %v", err)
	}
	if !res.ConfirmationRequired || res.OK || res.ExpiresAt.IsZero() {
		t.Fatalf("first UpgradeDatabase should only ask for confirmation, got %+v", res)
	}
	opts.ConfirmationToken = operator.token
	res, err = client.UpgradeDatabase(context.Background(), opts)
	if err != nil {
		t.Fatalf("client.UpgradeDatabase (confirmed): %v", err)
	}
	if !res.OK || res.TablesMigrated != 5 || res.ConfirmationRequired {
		t.Errorf("unexpected upgrade result: %+v", res)
	}

	// Test settings/db/migrate-to-postgres alias endpoint
	reqBody := `{"dsn":"postgres://user:secret@localhost:5432/grain","migrateData":true}`
	rec := confirmedUpgrade(t, srv, &operator, "/api/settings/db/migrate-to-postgres", reqBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on /api/settings/db/migrate-to-postgres, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestUpgradeDatabaseRouteWithEmbeddedPostgres(t *testing.T) {
	if !postgres.IsAvailable() {
		t.Skip("embedded postgres not available")
	}

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

	var operator confirmationLog
	cfg := ui.Config{
		DBDriver:        "sqlite",
		DataDir:         dataDir,
		SourceDB:        db,
		Actor:           databaseAdmin,
		DatabaseConfirm: operator.record,
	}

	srv := ui.NewServer(cfg, store)

	// Test test-connection with empty DSN
	testReq := httptest.NewRequest(http.MethodPost, "/api/database/test", strings.NewReader(`{"dsn":""}`))
	testReq.Header.Set("Content-Type", "application/json")
	testRec := httptest.NewRecorder()
	srv.ServeHTTP(testRec, testReq)
	if testRec.Code != http.StatusOK {
		t.Fatalf("test connection: expected 200, got %d: %s", testRec.Code, testRec.Body.String())
	}

	// Upgrade with empty DSN
	reqBody := `{"dsn":"","migrateData":true}`
	rec := confirmedUpgrade(t, srv, &operator, "/api/database/upgrade", reqBody)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		OK             bool     `json:"ok"`
		Message        string   `json:"message"`
		TablesMigrated int      `json:"tablesMigrated"`
		TableNames     []string `json:"tableNames"`
		Driver         string   `json:"driver"`
		DSN            string   `json:"dsn"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshalling: %v", err)
	}
	if !resp.OK {
		t.Errorf("expected ok=true")
	}
	if resp.Driver != "postgres" {
		t.Errorf("expected driver postgres, got %s", resp.Driver)
	}

	// Status endpoint should now report postgres
	statusReq := httptest.NewRequest(http.MethodGet, "/api/database", nil)
	statusRec := httptest.NewRecorder()
	srv.ServeHTTP(statusRec, statusReq)
	var statusResp struct {
		Driver   string `json:"driver"`
		IsSqlite bool   `json:"isSqlite"`
	}
	if err := json.Unmarshal(statusRec.Body.Bytes(), &statusResp); err != nil {
		t.Fatalf("unmarshalling status: %v", err)
	}
	if statusResp.Driver != "postgres" || statusResp.IsSqlite {
		t.Errorf("expected driver=postgres, isSqlite=false, got %+v", statusResp)
	}
}

var databaseAdmin = model.Principal{ID: "admin@example.com", Kind: model.PrincipalHuman}

func makeDatabaseAdmin(t *testing.T, store *model.Store) {
	t.Helper()
	if err := store.PutConfig(context.Background(), model.Config{PollInterval: model.DefaultPollInterval}); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}
}

// A failed test connection says only that it failed: the driver's error
// tells a refused port from an open one, which would make the route a
// port scanner.
func TestTestDatabaseConnectionHidesDialErrors(t *testing.T) {
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

	srv := ui.NewServer(ui.Config{
		DBDriver: "sqlite",
		DataDir:  dataDir,
		SourceDB: db,
		Actor:    databaseAdmin,
	}, store)

	body := `{"dsn":"postgres://probe:probe@127.0.0.1:1/probedb?sslmode=disable&connect_timeout=2"}`
	req := httptest.NewRequest(http.MethodPost, "/api/database/test", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	got := rec.Body.String()
	for _, leak := range []string{"127.0.0.1", "refused", "dial", "probe"} {
		if strings.Contains(got, leak) {
			t.Errorf("response leaks %q: %s", leak, got)
		}
	}
}

// confirmationLog stands in for the daemon log a database upgrade's
// confirmation token is written to: what an operator on the daemon host
// would read, and an HTTP caller would not.
type confirmationLog struct {
	token   string
	summary string
}

func (l *confirmationLog) record(token, summary string) {
	l.token, l.summary = token, summary
}

func postDatabaseUpgrade(t *testing.T, srv http.Handler, path string, body map[string]any, principal *model.Principal) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshalling body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	if principal != nil {
		req = req.WithContext(ui.WithPrincipal(req.Context(), *principal))
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// confirmedUpgrade sends body to path the way an operator would: once to
// be issued a token, which must change nothing, then again with the
// token read out of operator. It returns the second response.
func confirmedUpgrade(t *testing.T, srv http.Handler, operator *confirmationLog, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var fields map[string]any
	if err := json.Unmarshal([]byte(body), &fields); err != nil {
		t.Fatalf("unmarshalling body: %v", err)
	}
	operator.token = ""
	rec := postDatabaseUpgrade(t, srv, path, fields, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("%s: unconfirmed request: expected 202, got %d: %s", path, rec.Code, rec.Body.String())
	}
	if operator.token == "" {
		t.Fatalf("%s: unconfirmed request issued no confirmation token", path)
	}
	fields["confirmationToken"] = operator.token
	return postDatabaseUpgrade(t, srv, path, fields, nil)
}

// An admin's request alone does not repoint the daemon's database: the
// first request only writes a token to the operator's channel, and the
// work is done only by a request carrying that token, from the same
// principal, for exactly the same DSN and options, once, before it
// expires.
func TestUpgradeDatabaseRequiresOutOfBandConfirmation(t *testing.T) {
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
	otherAdmin := model.Principal{ID: "carol@example.com", Kind: model.PrincipalHuman}
	if err := store.PutConfig(context.Background(), model.Config{PollInterval: model.DefaultPollInterval}); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}

	calls := 0
	restarts := 0
	var operator confirmationLog
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	secretStore := secrets.New(t.TempDir())
	client := ui.NewClient(ui.Config{
		DBDriver:        "sqlite",
		DataDir:         dataDir,
		Secrets:         secretStore,
		SourceDB:        db,
		Actor:           databaseAdmin,
		DatabaseConfirm: operator.record,
		Restart:         func(context.Context) error { restarts++; return nil },
		UpgradePostgres: func(ctx context.Context, dsn string, migrateData bool) (*postgres.UpgradeResult, error) {
			calls++
			return &postgres.UpgradeResult{Driver: "postgres"}, nil
		},
	}, store)
	client.Now = func() time.Time { return now }
	srv := ui.NewServerWithClient(client)

	const path = "/api/database/upgrade"
	dsn := "postgres://evil:hunter2@attacker.example:5432/grain"
	request := func() map[string]any {
		return map[string]any{"dsn": dsn, "migrateData": true, "restart": true}
	}
	unchanged := func(step string) {
		t.Helper()
		if calls != 0 || restarts != 0 {
			t.Fatalf("%s: upgrade ran (calls=%d restarts=%d)", step, calls, restarts)
		}
		if saved, _ := postgres.ReadDatabaseConfig(dataDir); saved != nil {
			t.Fatalf("%s: saved a database config: %+v", step, saved)
		}
	}
	issue := func() string {
		t.Helper()
		operator.token = ""
		rec := postDatabaseUpgrade(t, srv, path, request(), nil)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("unconfirmed request: expected 202, got %d: %s", rec.Code, rec.Body.String())
		}
		if operator.token == "" || strings.Contains(rec.Body.String(), operator.token) {
			t.Fatalf("token must reach the operator and not the caller; token=%q body=%s", operator.token, rec.Body.String())
		}
		return operator.token
	}
	refused := func(step string, body map[string]any, principal *model.Principal) {
		t.Helper()
		rec := postDatabaseUpgrade(t, srv, path, body, principal)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s: expected 403, got %d: %s", step, rec.Code, rec.Body.String())
		}
		unchanged(step)
	}

	token := issue()
	unchanged("unconfirmed request")
	if strings.Contains(operator.summary, "hunter2") || !strings.Contains(operator.summary, "attacker.example") {
		t.Errorf("summary should name the target without its password: %q", operator.summary)
	}

	body := request()
	body["confirmationToken"] = "not-the-token"
	refused("made-up token", body, nil)

	// Every mismatch spends the token, so each case is issued its own.
	body = request()
	body["dsn"] = "postgres://evil:hunter2@elsewhere.example:5432/grain"
	body["confirmationToken"] = token
	refused("token for another DSN", body, nil)

	body = request()
	body["restart"] = false
	body["confirmationToken"] = issue()
	refused("token for other options", body, nil)

	body = request()
	body["confirmationToken"] = issue()
	refused("token issued to another admin", body, &otherAdmin)

	body = request()
	body["confirmationToken"] = issue()
	now = now.Add(11 * time.Minute)
	refused("expired token", body, nil)

	body = request()
	body["confirmationToken"] = strings.ToUpper(issue())
	rec := postDatabaseUpgrade(t, srv, path, body, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("confirmed request: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if calls != 1 || restarts != 1 {
		t.Fatalf("confirmed request: calls=%d restarts=%d, want 1 and 1", calls, restarts)
	}
	if saved, _ := postgres.LoadDatabaseConfig(context.Background(), dataDir, secretStore); saved == nil || saved.DSN != dsn {
		t.Fatalf("confirmed request: saved config %+v", saved)
	}

	rec = postDatabaseUpgrade(t, srv, path, body, nil)
	if rec.Code != http.StatusForbidden || calls != 1 {
		t.Fatalf("replayed token: expected 403 and no second upgrade, got %d (calls=%d): %s", rec.Code, calls, rec.Body.String())
	}
}

// An upgrade whose switch cannot be recorded is not reported as done: the
// daemon would reopen SQLite at its next start, so the route answers 500
// and neither repoints the daemon nor restarts it.
func TestUpgradeDatabaseRouteFailsWhenTheSwitchCannotBeRecorded(t *testing.T) {
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

	restarted := false
	var operator confirmationLog
	cfg := ui.Config{
		DBDriver: "sqlite",
		// A directory that is not there: db-config.json cannot be written.
		DataDir:         filepath.Join(dataDir, "missing"),
		Secrets:         secrets.New(t.TempDir()),
		SourceDB:        db,
		Actor:           databaseAdmin,
		DatabaseConfirm: operator.record,
		UpgradePostgres: func(context.Context, string, bool) (*postgres.UpgradeResult, error) {
			return &postgres.UpgradeResult{TablesMigrated: 1, Driver: "postgres"}, nil
		},
		Restart: func(context.Context) error { restarted = true; return nil },
	}
	srv := ui.NewServer(cfg, store)

	rec := confirmedUpgrade(t, srv, &operator, "/api/database/upgrade",
		`{"dsn":"postgres://user:secret@localhost:5432/grain","restart":true}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", rec.Code, rec.Body)
	}
	if restarted {
		t.Errorf("the daemon was restarted onto a switch it had not recorded")
	}
	status := httptest.NewRecorder()
	srv.ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/api/database", nil))
	if !strings.Contains(status.Body.String(), `"driver":"sqlite"`) {
		t.Errorf("GET /api/database after a failed upgrade = %s, want sqlite still", status.Body)
	}
}
