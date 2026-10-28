package postgres_test

// The scheduled workflow run store, against a real PostgreSQL database.
// Every workflow schedule (a workflow's own `on: schedule` cron, or a
// `.grain/schedules` file with `workflow:`) reads and writes this row on
// each firing, so a column the adapter does not translate stops every one
// of them firing at once (grain/task-4580).

import (
	"context"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
)

func TestScheduledWorkflowRunRoundTrips(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	assertScheduledWorkflowRunsRoundTrip(t, store, ctx)
}

func assertScheduledWorkflowRunsRoundTrip(t *testing.T, store *model.Store, ctx context.Context) {
	t.Helper()
	finished := now.Add(time.Hour)
	for _, want := range []model.ScheduledWorkflowRun{
		{
			ScheduleID: "repo:schedule:owner:repo:workflow.nightly",
			Stem:       "nightly",
			Repo:       model.RepoRef{Owner: "owner", Name: "repo"},
			FiredAt:    now,
		},
		{
			ScheduleID:      "repo:schedule:owner:repo:nightly-candidate",
			Stem:            "nightly",
			Repo:            model.RepoRef{Owner: "owner", Name: "repo"},
			Branch:          "candidate",
			Workflow:        ".grain/workflows/nightly.yml",
			RunID:           "run1",
			FiredAt:         now,
			AttemptedAt:     &now,
			FinishedAt:      &finished,
			Attempts:        1,
			Conclusion:      "success",
			Unfinished:      true,
			ReviewNotNeeded: true,
		},
	} {
		if err := store.PutScheduledWorkflowRun(ctx, want); err != nil {
			t.Fatalf("put %s: %v", want.ScheduleID, err)
		}
		got, err := store.GetScheduledWorkflowRun(ctx, want.ScheduleID)
		if err != nil || got == nil {
			t.Fatalf("get %s: %v (nil=%v)", want.ScheduleID, err, got == nil)
		}
		if got.ReviewNotNeeded != want.ReviewNotNeeded || got.Unfinished != want.Unfinished {
			t.Errorf("%s read back review_not_needed=%v unfinished=%v, want %v %v", want.ScheduleID,
				got.ReviewNotNeeded, got.Unfinished, want.ReviewNotNeeded, want.Unfinished)
		}
	}
	rows, err := store.ListScheduledWorkflowRuns(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("listed %d rows, want 2", len(rows))
	}
}
