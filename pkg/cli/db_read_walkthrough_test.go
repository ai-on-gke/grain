package cli

import (
	"context"
	"database/sql"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/capability/dbaccess"
	"github.com/bwsalmon/grain/pkg/daemon"
	"github.com/bwsalmon/grain/pkg/mcp"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/model/sqlite"
	"github.com/bwsalmon/grain/pkg/ui"
)

// docs/test-plans/2026-10-11.md §14 step 2, through the path a run
// actually takes (docs/test-runs/2026-10-12-connection-pauses-db-read.md):
// a task holding db-read gets query_grain_db from GrantedDaemonTools,
// whose daemonDB hop is a real ui.HTTPClient against a real ui.Server on
// a real SQLite store with SourceDB wired the way pkg/daemon/ui_server.go
// wires it. The audit's bypasses (task 2801) are sent both as the agent
// sends them, through the tool, and straight at POST
// /api/tasks/{id}/db/query, which is what an agent that skipped the
// tool's own validator would reach. Each is refused and no row changes.
func TestWalkthroughDBReadBypassesAreRefusedThroughTheToolAndHTTP(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dataDir := t.TempDir()
	db, err := sqlite.Open(sqlite.DefaultConfig(dataDir))
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		t.Fatalf("init store: %v", err)
	}

	reader := model.Task{
		ID: "dbr", Title: "reads the database",
		Grants: []model.Grant{{Capability: dbaccess.CapabilityDBRead, Via: model.GrantByLabel}},
	}
	for _, task := range []model.Task{reader, {ID: "bystander", Title: "untouched"}} {
		if err := store.PutTask(ctx, task); err != nil {
			t.Fatalf("filing %s: %v", task.ID, err)
		}
	}
	if err := store.StartRun(ctx, model.Run{
		ID: "run-dbr", TaskID: reader.ID, Attempt: 1, StartedAt: time.Now().UTC(),
	}, model.Limits{}); err != nil {
		t.Fatalf("starting dbr's run: %v", err)
	}

	srv := httptest.NewServer(ui.NewServer(withAccessCode(ui.Config{
		Actor:    ui.DefaultActor("operator"),
		DBDriver: "sqlite",
		DBDSN:    dataDir,
		DataDir:  dataDir,
		SourceDB: db,
	}), store))
	t.Cleanup(srv.Close)
	client := accessCodeClient(srv.URL)

	var query mcp.Tool
	for _, tool := range daemon.GrantedDaemonTools(daemon.GrantNames{dbaccess.CapabilityDBRead}, "", "", "", reader.ID, client) {
		if tool.Name == "execute_grain_db" {
			t.Fatal("a db-read grant was given execute_grain_db")
		}
		if tool.Name == "query_grain_db" {
			query = tool
		}
	}
	if query.Handler == nil {
		t.Fatal("a db-read grant was not given query_grain_db")
	}

	// What the bypasses are after: grain's task rows and the grants
	// on them. The audit wrote "tasks"; grain's table is "task", so the
	// audit's literal queries are sent and then the same ones aimed at
	// the tables that exist -- a refusal of a query on a missing table
	// proves nothing about the database holding a db-read run to reading.
	snapshot := func() string {
		t.Helper()
		var tasks, grants int
		var titles sql.NullString
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*), group_concat(title, '|') FROM (SELECT title FROM task ORDER BY id)").Scan(&tasks, &titles); err != nil {
			t.Fatalf("reading task: %v", err)
		}
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM task_grant").Scan(&grants); err != nil {
			t.Fatalf("reading task_grant: %v", err)
		}
		return fmt.Sprintf("%d task(s) %q, %d grant(s)", tasks, titles.String, grants)
	}
	before := snapshot()

	// The tool still answers a plain read, so a refusal below is the
	// query's and not a broken path.
	if res := query.Handler(ctx, map[string]any{"query": "SELECT COUNT(*) AS n FROM task"}); res.IsError || !strings.Contains(res.Text, `"n": 2`) {
		t.Fatalf("SELECT through query_grain_db = %+v, want two tasks counted", res)
	}

	for _, q := range []string{
		"WITH x AS (SELECT 1) DELETE\tFROM tasks",
		"SELECT 1; DELETE\nFROM tasks",
		"PRAGMA writable_schema=ON",
		"WITH x AS (SELECT 1) DELETE\tFROM task",
		"SELECT 1; DELETE\nFROM task",
		"WITH x AS (SELECT 1) UPDATE\ntask SET title='changed'",
		"SELECT 1; INSERT INTO task_grant (task_id, capability, via) VALUES ('dbr', 'db-write', 'label')",
		"SELECT 1; PRAGMA query_only = OFF; COMMIT; DELETE FROM task",
	} {
		if res := query.Handler(ctx, map[string]any{"query": q}); !res.IsError {
			t.Errorf("query_grain_db accepted %q: %s", q, res.Text)
		} else {
			t.Logf("query_grain_db %q: %s", q, res.Text)
		}
		if after := snapshot(); after != before {
			t.Fatalf("after %q through the tool: tasks %q, want %q", q, after, before)
		}
		if res, err := client.TaskDBQuery(ctx, reader.ID, q, nil); err == nil {
			t.Errorf("POST /api/tasks/%s/db/query accepted %q: %+v", reader.ID, q, res)
		} else {
			t.Logf("POST /api/tasks/%s/db/query %q: %v", reader.ID, q, err)
		}
		if after := snapshot(); after != before {
			t.Fatalf("after %q over HTTP: tasks %q, want %q", q, after, before)
		}
	}

	// The read-only connection is not handed back to the pool: grain's
	// own writes still work after it.
	if err := store.PutTask(ctx, model.Task{ID: "later", Title: "written after"}); err != nil {
		t.Fatalf("a write after the db-read queries failed: %v", err)
	}
}
