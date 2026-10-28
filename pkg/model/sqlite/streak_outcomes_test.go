package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
)

// TestTheDeploymentsOwnEndingsAreLeftOutOfTheStreak is the rule the
// task_streak view (schema.go) and Store.FailureStreak share, for each of
// the outcomes it names: a run ended by the deployment -- a usage limit,
// a provider with no capacity, a daemon stopped under it -- says nothing
// about its task. Each row runs the same history: two failures, then a
// wall of the outcome under test, then the reading. Excluded means the
// wall neither adds to the two failures before it nor clears them.
// "failed" is the control row, so the table cannot pass by reading zero
// for everything.
func TestTheDeploymentsOwnEndingsAreLeftOutOfTheStreak(t *testing.T) {
	t.Parallel()
	const failures = 2
	wall := model.MaxConsecutiveFailures + 1
	for _, tc := range []struct {
		outcome    string
		wantStreak int
		wantState  model.State
	}{
		{model.PausedOutcome, failures, model.StateQueued},
		{model.CapacityOutcome, failures, model.StateQueued},
		{model.InterruptedOutcome, failures, model.StateQueued},
		{model.EnvironmentOutcome, failures, model.StateQueued},
		{model.OOMOutcome, failures, model.StateQueued},
		{"failed", failures + wall, model.StateFailed},
	} {
		t.Run(tc.outcome, func(t *testing.T) {
			t.Parallel()
			store, db, ctx := openStore(t)
			if err := store.PutTask(ctx, task("a1b2", true)); err != nil {
				t.Fatal(err)
			}
			n := 0
			finish := func(outcome string) {
				t.Helper()
				n++
				id := fmt.Sprintf("r%d", n)
				at := now.Add(time.Duration(n) * time.Hour)
				if err := store.StartRun(ctx, model.Run{
					ID: id, TaskID: "a1b2", Sandbox: "s1", Attempt: n, StartedAt: at,
				}, model.Limits{}); err != nil {
					t.Fatal(err)
				}
				if err := store.FinishRun(ctx, id, at.Add(time.Minute), outcome, "detail"); err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < failures; i++ {
				finish("failed")
			}
			for i := 0; i < wall; i++ {
				finish(tc.outcome)
			}

			if got := taskStreakView(t, ctx, db, "a1b2"); got != tc.wantStreak {
				t.Errorf("task_streak after %d failures and %d %q runs = %d, want %d",
					failures, wall, tc.outcome, got, tc.wantStreak)
			}
			streak, err := store.FailureStreak(ctx, "a1b2")
			if err != nil {
				t.Fatal(err)
			}
			if streak == nil || streak.Count != tc.wantStreak {
				t.Errorf("FailureStreak = %+v, want Count %d", streak, tc.wantStreak)
			}
			if state, err := store.State(ctx, "a1b2"); err != nil {
				t.Fatal(err)
			} else if state != tc.wantState {
				t.Errorf("state = %q, want %q", state, tc.wantState)
			}
		})
	}
}

// taskStreakView reads the task_streak view itself, the name the view's
// own comment says state_test.go holds to Store.FailureStreak's answer.
// A task with no row there has no streak.
func taskStreakView(t *testing.T, ctx context.Context, db *sql.DB, taskID string) int {
	t.Helper()
	var streak int
	err := db.QueryRowContext(ctx, "SELECT `streak` FROM `task_streak` WHERE `task_id` = ?", taskID).Scan(&streak)
	if errors.Is(err, sql.ErrNoRows) {
		return 0
	}
	if err != nil {
		t.Fatalf("reading task_streak: %v", err)
	}
	return streak
}

// ConsecutiveOOMRuns counts the OOM kills a task's most recent runs ended
// in, passing over the deployment's other endings and stopping at
// anything else.
func TestConsecutiveOOMRuns(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutTask(ctx, task("a1b2", true)); err != nil {
		t.Fatal(err)
	}
	n := 0
	finish := func(outcome string) {
		t.Helper()
		n++
		id := fmt.Sprintf("r%d", n)
		at := now.Add(time.Duration(n) * time.Hour)
		if err := store.StartRun(ctx, model.Run{ID: id, TaskID: "a1b2", Sandbox: "s1", Attempt: n, StartedAt: at}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		if err := store.FinishRun(ctx, id, at.Add(time.Minute), outcome, "detail"); err != nil {
			t.Fatal(err)
		}
	}
	count := func() int {
		t.Helper()
		got, err := store.ConsecutiveOOMRuns(ctx, "a1b2")
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := count(); got != 0 {
		t.Fatalf("no runs: ConsecutiveOOMRuns = %d, want 0", got)
	}
	finish(model.OOMOutcome)
	finish("failed")
	finish(model.OOMOutcome)
	finish(model.InterruptedOutcome)
	finish(model.OOMOutcome)
	if got := count(); got != 2 {
		t.Errorf("oom, failed, oom, interrupted, oom: ConsecutiveOOMRuns = %d, want 2", got)
	}
	finish("succeeded")
	if got := count(); got != 0 {
		t.Errorf("after a success: ConsecutiveOOMRuns = %d, want 0", got)
	}
}
