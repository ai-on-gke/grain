package ui_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/dbaccess"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func setupTestDBServer(t *testing.T) (*ui.Server, *model.Store, context.Context) {
	t.Helper()
	dataDir := t.TempDir()
	db, err := sqlite.Open(sqlite.DefaultConfig(dataDir))
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("init store: %v", err)
	}

	cfg := ui.Config{
		DBDriver: "sqlite",
		DBDSN:    dataDir,
		DataDir:  dataDir,
		SourceDB: db,
	}

	srv := ui.NewServer(cfg, store)
	return srv, store, ctx
}

func TestTaskDBQueryWithReadGrant(t *testing.T) {
	t.Parallel()
	srv, store, ctx := setupTestDBServer(t)

	task := model.Task{
		ID:    "t-read",
		Title: "Read DB Task",
		Grants: []model.Grant{
			{Capability: dbaccess.CapabilityDBRead, Via: model.GrantByLabel},
		},
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("put task: %v", err)
	}
	running(t, store, ctx, task.ID)

	body := `{"query": "SELECT id FROM grain_schema LIMIT 1"}`
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/t-read/db/query", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp ui.TaskDBQueryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Count < 1 {
		t.Fatalf("expected at least 1 row from grain_schema, got %d", resp.Count)
	}
}

func TestTaskDBQueryRejectsMutatingQueryWithReadGrant(t *testing.T) {
	t.Parallel()
	srv, store, ctx := setupTestDBServer(t)

	task := model.Task{
		ID:    "t-read-only",
		Title: "Read Only Task",
		Grants: []model.Grant{
			{Capability: dbaccess.CapabilityDBRead, Via: model.GrantByLabel},
		},
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("put task: %v", err)
	}
	running(t, store, ctx, task.ID)

	body := `{"query": "DELETE FROM grain_schema WHERE id = 1"}`
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/t-read-only/db/query", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for mutating query with read grant, got %d: %s", rec.Code, rec.Body.String())
	}
}

// The bypasses the MCP security audit (task 2801) found: a db-read
// grant must neither have them accepted nor see them change a row. The
// audit wrote them against "tasks", which grain's schema does not have
// (its table is "task"), so they are aimed at the real table here: a
// write refused only because its table is missing proves nothing about
// the row this test reads back.
func TestTaskDBQueryReadGrantCannotWrite(t *testing.T) {
	t.Parallel()
	srv, store, ctx := setupTestDBServer(t)

	task := model.Task{
		ID:    "t-read-bypass",
		Title: "original",
		Grants: []model.Grant{
			{Capability: dbaccess.CapabilityDBRead, Via: model.GrantByLabel},
		},
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("put task: %v", err)
	}
	running(t, store, ctx, task.ID)

	for _, q := range []string{
		"WITH x AS (SELECT 1) DELETE\tFROM task",
		"WITH x AS (SELECT 1) UPDATE\ntask SET title='changed'",
		"SELECT 1; DELETE\nFROM task",
		"SELECT 1; PRAGMA query_only = OFF; COMMIT; UPDATE task SET title='changed'",
		"PRAGMA writable_schema=ON",
		"PRAGMA journal_mode=OFF",
	} {
		body, _ := json.Marshal(ui.TaskDBQueryRequest{Query: q})
		req := httptest.NewRequest(http.MethodPost, "/api/tasks/t-read-bypass/db/query", strings.NewReader(string(body)))
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code == http.StatusOK {
			t.Errorf("%q with a db-read grant: got 200, want a refusal: %s", q, rec.Body.String())
		}
		got, err := store.GetTask(ctx, task.ID)
		if err != nil || got == nil {
			t.Fatalf("after %q: task %s is gone (%v)", q, task.ID, err)
		}
		if got.Title != "original" {
			t.Fatalf("after %q: title is %q, want original", q, got.Title)
		}
	}
}

func TestTaskDBQueryForbiddenWithoutGrant(t *testing.T) {
	t.Parallel()
	srv, store, ctx := setupTestDBServer(t)

	task := model.Task{
		ID:     "t-no-grant",
		Title:  "No Grant Task",
		Grants: []model.Grant{},
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("put task: %v", err)
	}
	running(t, store, ctx, task.ID)

	body := `{"query": "SELECT 1"}`
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/t-no-grant/db/query", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestTaskDBExecWithWriteGrant(t *testing.T) {
	t.Parallel()
	srv, store, ctx := setupTestDBServer(t)

	task := model.Task{
		ID:    "t-write",
		Title: "Write DB Task",
		Grants: []model.Grant{
			{Capability: dbaccess.CapabilityDBWrite, Via: model.GrantByLabel},
		},
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("put task: %v", err)
	}
	running(t, store, ctx, task.ID)

	body := `{"statement": "CREATE TABLE test_sample (id TEXT PRIMARY KEY, value TEXT)"}`
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/t-write/db/execute", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	insertBody := `{"statement": "INSERT INTO test_sample (id, value) VALUES ('1', 'val1')"}`
	req2 := httptest.NewRequest(http.MethodPost, "/api/tasks/t-write/db/execute", strings.NewReader(insertBody))
	rec2 := httptest.NewRecorder()
	srv.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200 on insert, got %d: %s", rec2.Code, rec2.Body.String())
	}

	var resp ui.TaskDBExecResponse
	if err := json.Unmarshal(rec2.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.RowsAffected != 1 {
		t.Fatalf("expected 1 row affected, got %d", resp.RowsAffected)
	}
}

func TestTaskDBExecForbiddenWithReadOnlyGrant(t *testing.T) {
	t.Parallel()
	srv, store, ctx := setupTestDBServer(t)

	task := model.Task{
		ID:    "t-read-only-exec",
		Title: "Read Only Exec Task",
		Grants: []model.Grant{
			{Capability: dbaccess.CapabilityDBRead, Via: model.GrantByLabel},
		},
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("put task: %v", err)
	}
	running(t, store, ctx, task.ID)

	body := `{"statement": "CREATE TABLE hacked (id INT)"}`
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/t-read-only-exec/db/execute", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for exec with read-only grant, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHTTPClientTaskDBRoundTrip(t *testing.T) {
	t.Parallel()
	srv, store, ctx := setupTestDBServer(t)

	task := model.Task{
		ID:    "t-http",
		Title: "HTTP Roundtrip Task",
		Grants: []model.Grant{
			{Capability: dbaccess.CapabilityDBWrite, Via: model.GrantByLabel},
		},
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("put task: %v", err)
	}
	running(t, store, ctx, task.ID)

	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	client := serverAccessClient(srv, ts.URL)

	// Test Exec: CREATE & INSERT
	_, err := client.TaskDBExec(ctx, "t-http", "CREATE TABLE roundtrip_tbl (name TEXT)", nil)
	if err != nil {
		t.Fatalf("TaskDBExec CREATE: %v", err)
	}
	execRes, err := client.TaskDBExec(ctx, "t-http", "INSERT INTO roundtrip_tbl (name) VALUES (?)", []any{"roundtrip-value"})
	if err != nil {
		t.Fatalf("TaskDBExec INSERT: %v", err)
	}
	if execRes.RowsAffected != 1 {
		t.Fatalf("expected 1 row affected, got %d", execRes.RowsAffected)
	}

	// Test Query
	queryRes, err := client.TaskDBQuery(ctx, "t-http", "SELECT name FROM roundtrip_tbl WHERE name = ?", []any{"roundtrip-value"})
	if err != nil {
		t.Fatalf("TaskDBQuery: %v", err)
	}
	if queryRes.Count != 1 || queryRes.Rows[0]["name"] != "roundtrip-value" {
		t.Fatalf("unexpected query result: %+v", queryRes)
	}
}

// putDBTask stores a task holding grant, on team, with a live run unless
// live is false.
func putDBTask(t *testing.T, store *model.Store, ctx context.Context, id, team, grant string, live bool) {
	t.Helper()
	if err := store.PutTask(ctx, model.Task{
		ID: id, Title: id,
		Grants: []model.Grant{{Capability: grant, Via: model.GrantByLabel}},
	}); err != nil {
		t.Fatalf("put task: %v", err)
	}
	if live {
		running(t, store, ctx, id)
	}
}

func postDB(t *testing.T, srv *ui.Server, user, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if user != "" {
		req.Header.Set("X-Grain-User", user)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// A grant stays on the row after the run is over; it must stop working
// then, and it must not work before the run starts either.
func TestTaskDBRefusesTaskWithoutLiveRun(t *testing.T) {
	t.Parallel()
	srv, store, ctx := setupTestDBServer(t)
	putDBTask(t, store, ctx, "t-idle", "", dbaccess.CapabilityDBWrite, false)
	putDBTask(t, store, ctx, "t-closed", "", dbaccess.CapabilityDBWrite, true)
	if rec := postDB(t, srv, "", "/api/tasks/t-closed/db/query", `{"query": "SELECT 1"}`); rec.Code != http.StatusOK {
		t.Fatalf("query on running task: %d %s", rec.Code, rec.Body)
	}
	if rec := postDB(t, srv, "", "/api/tasks/t-closed/close", `{}`); rec.Code >= 300 {
		t.Fatalf("closing task: %d %s", rec.Code, rec.Body)
	}

	for _, id := range []string{"t-idle", "t-closed"} {
		if rec := postDB(t, srv, "", "/api/tasks/"+id+"/db/query", `{"query": "SELECT 1"}`); rec.Code != http.StatusForbidden {
			t.Errorf("query on %s: %d, want 403: %s", id, rec.Code, rec.Body)
		}
		if rec := postDB(t, srv, "", "/api/tasks/"+id+"/db/execute", `{"statement": "CREATE TABLE x (id INT)"}`); rec.Code != http.StatusForbidden {
			t.Errorf("execute on %s: %d, want 403: %s", id, rec.Code, rec.Body)
		}
	}
}

// A query over a large table comes back cut at MaxTaskDBQueryRows and
// says so, rather than as one unbounded body.
func TestTaskDBQueryTruncatesLargeResults(t *testing.T) {
	t.Parallel()
	srv, store, ctx := setupTestDBServer(t)
	putDBTask(t, store, ctx, "t-big", "", dbaccess.CapabilityDBRead, true)

	query := func(limit int) ui.TaskDBQueryResponse {
		t.Helper()
		body := fmt.Sprintf(`{"query": "WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < %d) SELECT i FROM n"}`, limit)
		rec := postDB(t, srv, "", "/api/tasks/t-big/db/query", body)
		if rec.Code != http.StatusOK {
			t.Fatalf("query: %d %s", rec.Code, rec.Body)
		}
		var resp ui.TaskDBQueryResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return resp
	}
	if got := query(ui.MaxTaskDBQueryRows + 500); got.Count != ui.MaxTaskDBQueryRows || len(got.Rows) != ui.MaxTaskDBQueryRows || !got.Truncated {
		t.Errorf("large query: count %d, rows %d, truncated %v; want %d rows, truncated", got.Count, len(got.Rows), got.Truncated, ui.MaxTaskDBQueryRows)
	}
	if got := query(ui.MaxTaskDBQueryRows); got.Count != ui.MaxTaskDBQueryRows || got.Truncated {
		t.Errorf("exactly-at-cap query: count %d, truncated %v; want %d, not truncated", got.Count, got.Truncated, ui.MaxTaskDBQueryRows)
	}
}
