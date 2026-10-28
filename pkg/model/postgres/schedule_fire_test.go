package postgres_test

// Firing a schedule on PostgreSQL, each of the three kinds a firing can
// be -- a task, a workflow run and a suite run -- since each writes rows
// the others do not. A workflow schedule's firing wrote a Go bool into
// scheduled_workflow_run.review_not_needed, which PostgreSQL held as
// BIGINT, and every one of them failed on a Postgres deployment for two
// days while the SQLite suite passed (grain/task-4537).

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/postgres/postgrestest"
)

// twoDaysLate is when these schedules are fired: two days of hourly
// occurrences after the one they were due at, the backlog a deployment
// whose firings had been failing has on the cycle they start working.
var twoDaysLate = now.Add(48*time.Hour + 30*time.Minute)

func hourly(id string) model.Schedule {
	sched := schedule(id, now)
	sched.Recurrence = model.Recurrence{Kind: model.RecurrenceEveryNHours, EveryNHours: 1}
	return sched
}

// fireDue fires sched the way the reconciler does -- found by
// DueSchedules, then FireSchedule -- and checks the cadence came out the
// far side of the backlog: one firing, NextRunAt the next occurrence
// after now, and nothing due again at now.
func fireDue(t *testing.T, store *model.Store, ctx context.Context, sched model.Schedule) model.ScheduleFiring {
	t.Helper()
	if err := store.PutSchedule(ctx, sched); err != nil {
		t.Fatalf("put schedule: %v", err)
	}
	due, err := store.DueSchedules(ctx, twoDaysLate)
	if err != nil || len(due) != 1 || due[0].ID != sched.ID {
		t.Fatalf("DueSchedules = %+v, %v; want %s", due, err, sched.ID)
	}
	firing, err := store.FireSchedule(ctx, due[0], twoDaysLate, time.UTC, model.RepoRef{})
	if err != nil {
		t.Fatalf("FireSchedule: %v", err)
	}
	if firing.Skipped {
		t.Fatalf("firing skipped: %+v", firing)
	}
	got, err := store.GetSchedule(ctx, sched.ID)
	if err != nil || got == nil {
		t.Fatalf("GetSchedule: %v, %v", got, err)
	}
	if want := now.Add(49 * time.Hour); !got.NextRunAt.Equal(want) {
		t.Errorf("NextRunAt = %s, want %s: the next occurrence after the firing", got.NextRunAt, want)
	}
	if got.LastRunAt == nil || !got.LastRunAt.Equal(twoDaysLate) {
		t.Errorf("LastRunAt = %v, want %s", got.LastRunAt, twoDaysLate)
	}
	if due, err := store.DueSchedules(ctx, twoDaysLate); err != nil || len(due) != 0 {
		t.Errorf("DueSchedules after the firing = %+v, %v; want none: the backlog fires once", due, err)
	}
	return firing
}

func TestATaskScheduleFiresOnPostgres(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	firing := fireDue(t, store, ctx, hourly("sched-task"))
	if firing.TaskID == "" {
		t.Fatalf("firing = %+v, want a task filed", firing)
	}
	task, err := store.GetTask(ctx, firing.TaskID)
	if err != nil || task == nil {
		t.Fatalf("GetTask(%s): %v, %v", firing.TaskID, task, err)
	}
	if task.Title != "Nightly dependency bump" {
		t.Errorf("filed task title = %q", task.Title)
	}
}

func TestAWorkflowScheduleFiresOnPostgres(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	assertWorkflowScheduleFires(t, store, ctx)
}

func assertWorkflowScheduleFires(t *testing.T, store *model.Store, ctx context.Context) {
	t.Helper()
	sched := hourly("sched-workflow")
	templateID := model.WorkflowReviewTemplateID("nightly")
	sched.TemplateID = &templateID
	firing := fireDue(t, store, ctx, sched)
	if !firing.WorkflowRun {
		t.Fatalf("firing = %+v, want a workflow run started", firing)
	}
	row, err := store.GetScheduledWorkflowRun(ctx, sched.ID)
	if err != nil || row == nil {
		t.Fatalf("GetScheduledWorkflowRun: %v, %v", row, err)
	}
	if row.Stem != "nightly" || !row.FiredAt.Equal(twoDaysLate) || row.ReviewNotNeeded {
		t.Errorf("scheduled workflow run = %+v", row)
	}
	// The flag a firing writes false is written true once a run passes
	// with nothing to review, on the same column.
	row.ReviewNotNeeded, row.Unfinished = true, true
	if err := store.PutScheduledWorkflowRun(ctx, *row); err != nil {
		t.Fatalf("PutScheduledWorkflowRun: %v", err)
	}
	if again, err := store.GetScheduledWorkflowRun(ctx, sched.ID); err != nil || !again.ReviewNotNeeded || !again.Unfinished {
		t.Errorf("after setting the flags: %+v, %v", again, err)
	}
}

func TestASuiteScheduleFiresOnPostgres(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	suite := putSuite(t, ctx, store, twoItemSuite())
	sched := hourly("sched-suite")
	sched.SuiteID = &suite.ID
	firing := fireDue(t, store, ctx, sched)
	if firing.SuiteRunID == 0 {
		t.Fatalf("firing = %+v, want a suite run started", firing)
	}
	run, err := store.GetSuiteRun(ctx, firing.SuiteRunID)
	if err != nil || run == nil {
		t.Fatalf("GetSuiteRun(%d): %v, %v", firing.SuiteRunID, run, err)
	}
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(tasks) != 2 {
		t.Errorf("tasks the firing filed = %d, want one per item (2)", len(tasks))
	}
}

// TestAFailingFiringIsRecordedAndFiresOnceItRecovers is the two days
// above from the schedule's side: every cycle's failure is one held skip
// on the schedule, news only when it is new or changes, and the cycle
// that finally fires it settles that skip as fired late, standing in for
// every occurrence in between -- one firing, not a backlog.
func TestAFailingFiringIsRecordedAndFiresOnceItRecovers(t *testing.T) {
	t.Parallel()
	for name, open := range map[string]func(t *testing.T) *model.Store{
		"sqlite": func(t *testing.T) *model.Store {
			store, _ := openStoreIn(t, t.TempDir())
			return store
		},
		"postgres": func(t *testing.T) *model.Store {
			store, _ := postgrestest.Open(t)
			return store
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := open(t)
			ctx := context.Background()
			sched := hourly("sched-failing")
			if err := store.PutSchedule(ctx, sched); err != nil {
				t.Fatalf("put schedule: %v", err)
			}
			fail := errors.New("failed to encode args[15]: unable to encode false into binary format for int8")
			for i, c := range []struct {
				at   time.Time
				err  error
				news bool
			}{
				{now.Add(10 * time.Second), fail, true},
				{now.Add(20 * time.Second), fail, false},
				{now.Add(24 * time.Hour), fail, false},
				{now.Add(25 * time.Hour), errors.New("connection refused"), true},
				{now.Add(26 * time.Hour), errors.New("connection refused"), false},
			} {
				news, err := store.RecordScheduleFiringFailure(ctx, sched, c.at, c.err)
				if err != nil {
					t.Fatalf("cycle %d: RecordScheduleFiringFailure: %v", i, err)
				}
				if news != c.news {
					t.Errorf("cycle %d (%v): news = %v, want %v", i, c.err, news, c.news)
				}
			}
			skips, err := store.ScheduleSkips(ctx, sched.ID)
			if err != nil || len(skips) != 1 {
				t.Fatalf("skips = %+v, %v; want one for the one occurrence", skips, err)
			}
			k := skips[0]
			if k.Reason != model.SkippedFiringFailed || k.Outcome != model.ScheduleSkipHeld ||
				!k.At.Equal(now.Add(10*time.Second)) || !k.Occurrence.Equal(now) {
				t.Errorf("skip = %+v", k)
			}
			if want := "firing failed: connection refused"; k.Why() != want {
				t.Errorf("Why = %q, want %q", k.Why(), want)
			}

			firing := fireDue(t, store, ctx, sched)
			skips, err = store.ScheduleSkips(ctx, sched.ID)
			if err != nil || len(skips) != 1 {
				t.Fatalf("skips after the firing = %+v, %v", skips, err)
			}
			k = skips[0]
			if k.Outcome != model.ScheduleSkipFired || k.FiredTaskID != firing.TaskID || k.Folded != 48 {
				t.Errorf("skip after the firing = %+v, want fired as %s, folding 48 occurrences", k, firing.TaskID)
			}
			tasks, err := store.ListTasks(ctx)
			if err != nil || len(tasks) != 1 {
				t.Errorf("tasks = %d, %v; want the one firing", len(tasks), err)
			}
			// Settled, the occurrence is not news again.
			if news, err := store.RecordScheduleFiringFailure(ctx, sched, twoDaysLate, fail); err != nil || news {
				t.Errorf("failure on a settled occurrence: news %v, %v", news, err)
			}
		})
	}
}
