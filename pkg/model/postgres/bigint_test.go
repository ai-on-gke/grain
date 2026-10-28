package postgres_test

// The schema's INTEGER is 64-bit on both backends. SQLite's always is;
// PostgreSQL's is int4, so adaptDDL spells it BIGINT and Init widens
// any int4 column an older build left behind.

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/postgres/postgrestest"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

// past2to31 is the first value int4 cannot hold.
const past2to31 = int64(1) << 31

func TestIntegerColumnsHoldValuesPast2to31(t *testing.T) {
	t.Parallel()
	backends := map[string]func(t *testing.T) *model.Store{
		"sqlite": func(t *testing.T) *model.Store {
			db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
			if err != nil {
				t.Fatalf("opening embedded sqlite: %v", err)
			}
			t.Cleanup(func() { db.Close() })
			store := model.New(db)
			if err := store.Init(context.Background()); err != nil {
				t.Fatalf("applying schema: %v", err)
			}
			return store
		},
		"postgres": func(t *testing.T) *model.Store {
			store, _ := postgrestest.Open(t)
			return store
		},
	}
	for name, open := range backends {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := open(t)
			assertHoldsPast2to31(t, store)
		})
	}
}

func assertHoldsPast2to31(t *testing.T, store *model.Store) {
	t.Helper()
	ctx := context.Background()

	// sandbox_usage.duration_ms: a cumulative millisecond counter, past
	// 2^31 after about 24.8 days of sandbox-time.
	lived := time.Duration(past2to31+1) * time.Millisecond
	if err := store.RecordSandboxFinish(ctx, "agent", lived, now); err != nil {
		t.Fatalf("RecordSandboxFinish(%v): %v", lived, err)
	}
	usages, err := store.SandboxUsages(ctx)
	if err != nil {
		t.Fatalf("SandboxUsages: %v", err)
	}
	if len(usages) != 1 || usages[0].Duration != lived {
		t.Errorf("sandbox usage = %+v, want one row with Duration %v", usages, lived)
	}

	// task_attachment.size: a byte count, past 2^31 at 2 GiB.
	if err := store.PutTask(ctx, task("big-attachment", true)); err != nil {
		t.Fatalf("PutTask: %v", err)
	}
	size := past2to31 + 7
	if _, err := store.AddAttachment(ctx, model.Attachment{
		TaskID: "big-attachment", Filename: "big.bin",
		ContentType: "application/octet-stream", Size: size, CreatedAt: now,
		Content: []byte("x"),
	}); err != nil {
		t.Fatalf("AddAttachment(size %d): %v", size, err)
	}
	metas, err := store.AttachmentMetas(ctx, "big-attachment")
	if err != nil {
		t.Fatalf("AttachmentMetas: %v", err)
	}
	if len(metas) != 1 || metas[0].Size != size {
		t.Errorf("attachments = %+v, want one of size %d", metas, size)
	}
}

// TestTheBaselineIsCreatedWithTheDialectsOwnTypes creates the baseline
// schema from scratch and checks it is already in the types the store
// binds: no int4 column, which overflows at 2^31, and the 0/1 flags as
// BOOLEAN, which pgx needs to bind a Go bool. There is no migration to
// put either right afterwards, so adaptDDL has to get them right the
// first time.
func TestTheBaselineIsCreatedWithTheDialectsOwnTypes(t *testing.T) {
	t.Parallel()
	_, db := postgrestest.Open(t)
	if got := intColumns(t, db, "integer"); len(got) != 0 {
		t.Errorf("int4 columns in a fresh baseline = %v, want none", got)
	}
	if got := intColumns(t, db, "bigint"); len(got) == 0 {
		t.Errorf("no bigint columns in a fresh baseline")
	}
	booleans := map[string]bool{}
	for _, c := range intColumns(t, db, "boolean") {
		booleans[c] = true
	}
	for _, c := range []string{"task.auto_merge", "ci_run.recheck", "ci_job.flake",
		"scheduled_workflow_run.review_not_needed", "scheduled_workflow_run.unfinished"} {
		if !booleans[c] {
			t.Errorf("%s is not BOOLEAN in a fresh baseline", c)
		}
	}
}

func intColumns(t *testing.T, db *sql.DB, dataType string) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `SELECT c.table_name || '.' || c.column_name
FROM information_schema.columns c
JOIN information_schema.tables t
  ON t.table_schema = c.table_schema AND t.table_name = c.table_name
WHERE c.table_schema = current_schema() AND t.table_type = 'BASE TABLE' AND c.data_type = ?
ORDER BY 1`, dataType)
	if err != nil {
		t.Fatalf("listing %s columns: %v", dataType, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("listing %s columns: %v", dataType, err)
		}
		out = append(out, name)
	}
	return out
}
