package sqlite_test

// The per-run tool census, against a real database: what a run recorded
// about its own tool use has to survive the round trip, a re-record has to
// replace rather than double, and a store that predates these tables has
// to gain them rather than fail (model/schema.go's own note on why
// neither needs a migration rung).

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

func TestRunTelemetryRoundTrips(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	seedFinishedRun(t, store, ctx, "r1", "a1b2")

	sizes := model.SizeHistogram{}
	for _, n := range []int64{40, 40, 90000} {
		sizes.Add(n)
	}
	dropped := model.SizeHistogram{}
	dropped.Add(74000)
	err := store.RecordRunTelemetry(ctx, "r1", model.RunTelemetry{
		Tools: []model.RunToolUse{
			{Tool: "run_command", Calls: 3, Errored: 1, TimedOut: 1, Elided: 1,
				ResultBytes: 90080, MaxResultBytes: 90000, Sizes: sizes, Dropped: dropped},
			{Tool: "edit_file", Calls: 2, Errored: 2, ResultBytes: 60,
				MaxResultBytes: 40, Sizes: model.SizeHistogram{6: 2}},
		},
		CheckWaits: []model.RunCheckWait{
			{Seq: 0, Verdict: "failed", Waited: 4 * time.Minute, PushesBefore: 1},
			{Seq: 1, Verdict: "passed", Waited: 9 * time.Minute, PushesBefore: 2},
		},
	})
	if err != nil {
		t.Fatalf("RecordRunTelemetry: %v", err)
	}

	uses, err := store.RunToolUses(ctx)
	if err != nil {
		t.Fatalf("RunToolUses: %v", err)
	}
	if len(uses) != 2 {
		t.Fatalf("read %d census rows, want 2: %+v", len(uses), uses)
	}
	// Ordered by tool within a run, so edit_file comes first.
	if got := uses[0]; got.RunID != "r1" || got.Tool != "edit_file" || got.Calls != 2 || got.Errored != 2 {
		t.Errorf("edit_file row = %+v", got)
	}
	cmd := uses[1]
	if cmd.Tool != "run_command" || cmd.Calls != 3 || cmd.Errored != 1 || cmd.TimedOut != 1 {
		t.Errorf("run_command row = %+v", cmd)
	}
	if cmd.ResultBytes != 90080 || cmd.MaxResultBytes != 90000 {
		t.Errorf("run_command sizes = %d total, %d max", cmd.ResultBytes, cmd.MaxResultBytes)
	}
	if cmd.Sizes.Total() != 3 || cmd.Sizes[model.SizeBucket(90000)] != 1 {
		t.Errorf("run_command histogram = %v, want the three results it recorded", cmd.Sizes)
	}
	if cmd.Elided != 1 || cmd.Dropped.Total() != 1 || cmd.Dropped[model.SizeBucket(74000)] != 1 {
		t.Errorf("run_command cuts = %d elided, dropping %v; want the one cut it recorded",
			cmd.Elided, cmd.Dropped)
	}
	// A tool nothing was cut on reads back as nothing cut, not as a row
	// with an empty histogram standing in for a missing one.
	if edits := uses[0]; edits.Elided != 0 || edits.Dropped.Total() != 0 {
		t.Errorf("edit_file cuts = %d elided, dropping %v; want none", edits.Elided, edits.Dropped)
	}

	waits, err := store.RunCheckWaits(ctx)
	if err != nil {
		t.Fatalf("RunCheckWaits: %v", err)
	}
	if len(waits) != 2 {
		t.Fatalf("read %d CI waits, want 2: %+v", len(waits), waits)
	}
	if got := waits[0]; got.Seq != 0 || got.Verdict != "failed" || got.Waited != 4*time.Minute || got.PushesBefore != 1 {
		t.Errorf("first wait = %+v", got)
	}
	if got := waits[1]; got.Seq != 1 || got.Verdict != "passed" || got.Waited != 9*time.Minute || got.PushesBefore != 2 {
		t.Errorf("second wait = %+v", got)
	}
}

// A second record for the same run replaces the first. Every count in
// these tables is summed by a report, so a retried write that added
// instead would double a run's whole census.
func TestRecordRunTelemetryReplacesWhatWasThereBefore(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	seedFinishedRun(t, store, ctx, "r1", "a1b2")

	first := model.RunTelemetry{
		Tools:      []model.RunToolUse{{Tool: "run_command", Calls: 3}},
		CheckWaits: []model.RunCheckWait{{Seq: 0, Verdict: "passed"}},
	}
	if err := store.RecordRunTelemetry(ctx, "r1", first); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordRunTelemetry(ctx, "r1", first); err != nil {
		t.Fatal(err)
	}
	uses, err := store.RunToolUses(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(uses) != 1 || uses[0].Calls != 3 {
		t.Errorf("after two records, census = %+v, want one row of 3 calls", uses)
	}
	waits, err := store.RunCheckWaits(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(waits) != 1 {
		t.Errorf("after two records, %d CI waits, want 1", len(waits))
	}
}

// RunTimings carries the run's own id and detail, which is what joins a
// census row to the run it belongs to and what tells a run cancelled by
// the wall-clock cap from one whose task a human closed.
func TestRunTimingsCarryTheIDAndDetailAnEndingIsReadFrom(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	seedFinishedRun(t, store, ctx, "r1", "a1b2")
	if err := store.SetRunOutcome(ctx, "r1", "cancelled", model.RuntimeCapDetail(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	runs, err := store.RunTimings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("read %d runs, want 1", len(runs))
	}
	if runs[0].RunID != "r1" {
		t.Errorf("RunID = %q, want r1", runs[0].RunID)
	}
	if got := model.EndingOf(runs[0].Outcome, runs[0].Detail); got != model.EndingRuntimeCap {
		t.Errorf("ending = %q, want %q", got, model.EndingRuntimeCap)
	}
}

// A store created before these two tables existed gains them on Init and
// reads back empty, rather than failing every report with "no such
// table".
func TestInitCreatesTheTelemetryTablesOnAnOlderStore(t *testing.T) {
	t.Parallel()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	// The shape of an older database: the tables simply are not there.
	for _, table := range []string{"task_run_tool", "task_run_check_wait"} {
		if _, err := db.ExecContext(ctx, "DROP TABLE `"+table+"`"); err != nil {
			t.Fatalf("dropping %s: %v", table, err)
		}
	}
	if err := store.Init(ctx); err != nil {
		t.Fatalf("re-applying schema: %v", err)
	}
	if uses, err := store.RunToolUses(ctx); err != nil || len(uses) != 0 {
		t.Errorf("RunToolUses = %v, %v; want an empty table", uses, err)
	}
	if waits, err := store.RunCheckWaits(ctx); err != nil || len(waits) != 0 {
		t.Errorf("RunCheckWaits = %v, %v; want an empty table", waits, err)
	}
}

func seedFinishedRun(t *testing.T, store *model.Store, ctx context.Context, runID, taskID string) {
	t.Helper()
	if err := store.PutTask(ctx, task(taskID, true)); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: runID, TaskID: taskID, Sandbox: "s1", Attempt: 1, StartedAt: now,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, runID, now.Add(time.Hour), "succeeded", "the run made 5 tool call(s)"); err != nil {
		t.Fatal(err)
	}
}

func TestRecordBatchRunTelemetry(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	seedFinishedRun(t, store, ctx, "r1", "a1b2")
	seedFinishedRun(t, store, ctx, "r2", "a1b2")

	batch := map[string]model.RunTelemetry{
		"r1": {
			Tools:      []model.RunToolUse{{Tool: "read_file", Calls: 3, ResultBytes: 1024}},
			CheckWaits: []model.RunCheckWait{{Seq: 1, Verdict: "success", Waited: 5 * time.Second}},
			WaitCredit: &model.RunWaitCredit{Blocked: 5 * time.Second, Credited: 5 * time.Second},
		},
		"r2": {
			Tools: []model.RunToolUse{{Tool: "run_command", Calls: 2, ResultBytes: 2048}},
		},
	}
	if err := store.RecordBatchRunTelemetry(ctx, batch); err != nil {
		t.Fatalf("RecordBatchRunTelemetry: %v", err)
	}

	uses, err := store.RunToolUses(ctx)
	if err != nil {
		t.Fatalf("RunToolUses: %v", err)
	}
	if len(uses) != 2 {
		t.Fatalf("len(uses) = %d, want 2", len(uses))
	}
	waits, err := store.RunCheckWaits(ctx)
	if err != nil {
		t.Fatalf("RunCheckWaits: %v", err)
	}
	if len(waits) != 1 {
		t.Fatalf("len(waits) = %d, want 1", len(waits))
	}
	credits, err := store.RunWaitCredits(ctx)
	if err != nil {
		t.Fatalf("RunWaitCredits: %v", err)
	}
	if len(credits) != 1 {
		t.Fatalf("len(credits) = %d, want 1", len(credits))
	}
}

// How a run worked a red build (model.RunCIHabit) survives the round trip
// as booleans, a re-record replaces it, and a run that did none of it
// gets no row.
func TestRunCIHabitsRoundTrip(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	seedFinishedRun(t, store, ctx, "r1", "a1b2")
	seedFinishedRun(t, store, ctx, "r2", "a1b2")

	habit := model.RunCIHabit{NarrowTestBeforeFixPush: true, PushedAfterRed: true, Selective: true}
	tools := []model.RunToolUse{{Tool: "run_command", Calls: 1}}
	if err := store.RecordRunTelemetry(ctx, "r1", model.RunTelemetry{Tools: tools, CIHabit: &habit}); err != nil {
		t.Fatalf("RecordRunTelemetry: %v", err)
	}
	if err := store.RecordRunTelemetry(ctx, "r2", model.RunTelemetry{Tools: tools, CIHabit: &model.RunCIHabit{}}); err != nil {
		t.Fatalf("RecordRunTelemetry: %v", err)
	}
	habits, err := store.RunCIHabits(ctx)
	if err != nil {
		t.Fatalf("RunCIHabits: %v", err)
	}
	habit.RunID = "r1"
	if len(habits) != 1 || habits[0] != habit {
		t.Fatalf("habits = %+v, want only r1's %+v", habits, habit)
	}

	if err := store.RecordRunTelemetry(ctx, "r1", model.RunTelemetry{Tools: tools}); err != nil {
		t.Fatalf("re-recording: %v", err)
	}
	if habits, err := store.RunCIHabits(ctx); err != nil || len(habits) != 0 {
		t.Fatalf("after re-recording without a habit: %+v, %v; want none", habits, err)
	}
}

// A CI wait the sandbox proxy served (grain ci wait) is in
// task_run_check_wait as soon as it is recorded, and survives the
// end-of-run write that replaces the run's own waits, numbered after
// them.
func TestProxyCheckWaitsMergeWithTheRunsOwn(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	seedFinishedRun(t, store, ctx, "r1", "a1b2")

	if err := store.RecordProxyCheckWait(ctx, "r1", "timed_out", 9*time.Minute); err != nil {
		t.Fatalf("RecordProxyCheckWait: %v", err)
	}
	waits, err := store.RunCheckWaits(ctx)
	if err != nil || len(waits) != 1 || waits[0].Verdict != "timed_out" || waits[0].Waited != 9*time.Minute {
		t.Fatalf("waits before the run's own write = %+v, %v", waits, err)
	}
	if err := store.RecordProxyCheckWait(ctx, "r1", "passed", 2*time.Minute); err != nil {
		t.Fatalf("RecordProxyCheckWait: %v", err)
	}
	err = store.RecordRunTelemetry(ctx, "r1", model.RunTelemetry{
		CheckWaits: []model.RunCheckWait{{Seq: 0, Verdict: "failed", Waited: time.Minute, PushesBefore: 1}},
	})
	if err != nil {
		t.Fatalf("RecordRunTelemetry: %v", err)
	}
	waits, err = store.RunCheckWaits(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, w := range waits {
		got = append(got, fmt.Sprintf("%d:%s", w.Seq, w.Verdict))
	}
	if strings.Join(got, " ") != "0:failed 1:timed_out 2:passed" {
		t.Errorf("waits = %q", got)
	}

	if err := store.RecordProxyToolCallArgs(ctx, "r1", "add_review_comment", map[string]any{"body": "x"}); err != nil {
		t.Fatal(err)
	}
	if calls := store.TakeProxyToolCalls("r1"); len(calls) != 1 || calls[0].Arguments["body"] != "x" {
		t.Errorf("calls = %+v", calls)
	}
	if calls := store.TakeProxyToolCalls("r1"); len(calls) != 0 {
		t.Errorf("calls were not forgotten once taken: %+v", calls)
	}
}
