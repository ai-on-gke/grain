package postgres_test

import (
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
)

// TaskSpans is what GET /api/tasks draws every row's timeline from, so a
// query PostgreSQL refuses is a task list that answers 500 on every
// deployment running on it. It once leaned on SQLite's bare-column rule
// (a plain column beside MIN() takes the minimum's row), which PostgreSQL
// rejects with "must appear in the GROUP BY clause" (SQLSTATE 42803).
// Two attempts, so the earlier one has to win, and an observation, so the
// joined columns come back too.
func TestTaskSpansOnPostgres(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutTask(ctx, task("a1b2", true)); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTask(ctx, task("c3d4", true)); err != nil {
		t.Fatal(err)
	}
	first := now.Add(time.Hour)
	second := now.Add(3 * time.Hour)
	for _, r := range []model.Run{
		{ID: "run-2", TaskID: "a1b2", Sandbox: "sandbox-2", Attempt: 2, StartedAt: second},
		{ID: "run-1", TaskID: "a1b2", Sandbox: "sandbox-1", Attempt: 1, StartedAt: first},
	} {
		if err := store.StartRun(ctx, r, model.Limits{}); err != nil {
			t.Fatalf("starting %s: %v", r.ID, err)
		}
		if err := store.FinishRun(ctx, r.ID, r.StartedAt.Add(time.Minute), "failed", ""); err != nil {
			t.Fatalf("finishing %s: %v", r.ID, err)
		}
	}
	closed := now.Add(5 * time.Hour)
	merged := now.Add(4 * time.Hour)
	if err := store.Observe(ctx, model.Observation{TaskID: "a1b2", ClosedAt: &closed, PrMergedAt: &merged}); err != nil {
		t.Fatal(err)
	}

	spans, err := store.TaskSpans(ctx)
	if err != nil {
		t.Fatalf("TaskSpans: %v", err)
	}
	if _, ok := spans["c3d4"]; ok {
		t.Errorf("spans = %v, want no entry for a task that has neither started nor ended", spans)
	}
	span, ok := spans["a1b2"]
	if !ok {
		t.Fatalf("spans = %v, want an entry for a1b2", spans)
	}
	if span.StartedAt == nil || !span.StartedAt.Equal(first) {
		t.Errorf("StartedAt = %v, want the first attempt %v", span.StartedAt, first)
	}
	if span.ClosedAt == nil || !span.ClosedAt.Equal(closed) {
		t.Errorf("ClosedAt = %v, want %v", span.ClosedAt, closed)
	}
	if span.PrMergedAt == nil || !span.PrMergedAt.Equal(merged) {
		t.Errorf("PrMergedAt = %v, want %v", span.PrMergedAt, merged)
	}
}
