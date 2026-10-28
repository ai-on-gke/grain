package orchestrator_test

// The instrument over grain's own refusal markers, from the end that
// writes it.
//
// pkg/agent tells a provider refusal from an ordinary failure by
// matching phrases the provider writes, so the day one of those phrases
// is reworded, the refusal arrives as an ordinary failure and nothing
// says so: the outcome is right for what grain could see, every test
// still passes, and five in a row retire a task that was never wrong.
// These tests pin the one thing that would make that visible -- that a
// failure grain could not classify leaves the provider's own words
// behind, and that a failure it *could* classify leaves none, so the log
// stays a list of what grain does not understand rather than a second
// copy of what it does.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/agent"
	"github.com/bwsalmon/grain/pkg/dispatch"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
)

// runFailing dispatches one run of a fresh task that fails with err, and
// hands back whatever unmatched failures the store then holds.
func runFailing(t *testing.T, taskID string, err error) ([]model.UnmatchedFailure, []model.Run) {
	t.Helper()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, taskID)
	d := dispatch.Dispatch{TaskID: taskID, RunID: taskID + "-1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)
	task, getErr := store.GetTask(ctx, taskID)
	if getErr != nil || task == nil {
		t.Fatalf("reading task: %v", getErr)
	}

	fw := agentFunc(func(context.Context, agent.RunConfig) (*agent.Result, error) {
		return pushed(), err
	})
	cfg := orchestrator.Config{Pause: &orchestrator.Pause{}, Now: func() time.Time { return baseTime }}
	if _, runErr := orchestrator.RunDispatch(ctx, store, fw, cfg, *task, d, nil, t.TempDir(), "", nil, baseTime); runErr == nil {
		t.Fatal("RunDispatch err = nil, want the failure reported")
	}

	failures, ferr := store.UnmatchedFailures(ctx, baseTime.Add(-time.Hour), 0)
	if ferr != nil {
		t.Fatalf("UnmatchedFailures: %v", ferr)
	}
	runs, rerr := store.Runs(ctx, taskID)
	if rerr != nil {
		t.Fatalf("reading runs: %v", rerr)
	}
	return failures, runs
}

// The whole instrument in one test: a run fails saying something grain
// has no marker for, and what it said is kept -- with the run, the task
// and the framework beside it, because "which provider started wording
// things differently?" is the question a week of these gets asked.
func TestAFailureGrainCannotClassifyKeepsWhatTheProviderSaid(t *testing.T) {
	t.Parallel()
	failures, runs := runFailing(t, "t1", errors.New(
		"claude: the model is currently experiencing unusually heavy demand; please retry shortly"))

	if len(failures) != 1 {
		t.Fatalf("recorded %d unmatched failures, want 1: %+v", len(failures), failures)
	}
	f := failures[0]
	if !strings.Contains(f.Text, "unusually heavy demand") {
		t.Errorf("text = %q, want the provider's own words", f.Text)
	}
	if f.RunID != "t1-1" || f.TaskID != "t1" {
		t.Errorf("recorded run %q of task %q, want t1-1 of t1", f.RunID, f.TaskID)
	}
	if f.Framework == "" {
		t.Error("no framework recorded; a deployment running two needs to know which one reworded")
	}
	// The loose read, which is the column that makes a week of these
	// scannable: this one reads like a provider refusing a run.
	if !f.Suspect {
		t.Errorf("suspect = false for %q, want a refusal-shaped failure flagged", f.Text)
	}
	// And the person reading the one failed task, rather than the log,
	// is told the same thing.
	if len(runs) != 1 {
		t.Fatalf("runs = %+v, want one", runs)
	}
	if !strings.Contains(runs[0].Detail, "reads like the provider refusing") {
		t.Errorf("run detail = %q, want it to say the failure may be the provider's", runs[0].Detail)
	}
}

// The ordinary failure -- an agent that ran out of turns -- is recorded
// too, because "grain could not classify it" is the only test that can
// be applied without already knowing the answer. It simply is not
// flagged, and nothing is added to the task's own detail: a hedge on
// every failed run would be noise on all of them and a signal on none.
func TestAnOrdinaryFailureIsKeptButNotFlagged(t *testing.T) {
	t.Parallel()
	failures, runs := runFailing(t, "t2", agent.MaxTurnsExceeded("claude", 100))

	if len(failures) != 1 {
		t.Fatalf("recorded %d unmatched failures, want 1: %+v", len(failures), failures)
	}
	if failures[0].Suspect {
		t.Errorf("suspect = true for %q, want an ordinary failure left unflagged", failures[0].Text)
	}
	if strings.Contains(runs[0].Detail, "reads like the provider refusing") {
		t.Errorf("run detail = %q, want no hedge on an ordinary failure", runs[0].Detail)
	}
}

// A refusal grain *did* recognise leaves nothing here. The log is a list
// of what grain could not name, and a capacity refusal that appeared in
// it would dilute exactly the signal it exists to carry -- the run is
// already recorded as the provider's doing, on the attempt itself.
func TestARecognisedRefusalIsNotRecordedAsUnmatched(t *testing.T) {
	t.Parallel()
	failures, runs := runFailing(t, "t3", &agent.CapacityError{
		Framework: "claude", Message: "api error: 529 overloaded_error",
	})
	if len(failures) != 0 {
		t.Errorf("recorded %+v, want nothing: grain named this refusal already", failures)
	}
	if runs[0].Outcome != model.CapacityOutcome {
		t.Errorf("run outcome = %q, want %q", runs[0].Outcome, model.CapacityOutcome)
	}
}

// Nor does a usage limit, for the same reason: the deployment paused for
// it and said so.
func TestAUsageLimitIsNotRecordedAsUnmatched(t *testing.T) {
	t.Parallel()
	failures, runs := runFailing(t, "t4", &agent.UsageLimitError{
		Framework: "claude", Message: "Claude AI usage limit reached",
	})
	if len(failures) != 0 {
		t.Errorf("recorded %+v, want nothing: grain named this refusal already", failures)
	}
	if runs[0].Outcome != model.PausedOutcome {
		t.Errorf("run outcome = %q, want %q", runs[0].Outcome, model.PausedOutcome)
	}
}

// What a provider says has no length, and this row is read a week at a
// time, so the text is bounded when it is written rather than when it is
// rendered.
func TestWhatIsKeptIsBounded(t *testing.T) {
	t.Parallel()
	failures, _ := runFailing(t, "t5", errors.New(strings.Repeat("x", 50000)+" 503 from the edge"))
	if len(failures) != 1 {
		t.Fatalf("recorded %d unmatched failures, want 1", len(failures))
	}
	if len(failures[0].Text) > agent.MaxUnmatchedFailureText+len("...") {
		t.Errorf("kept %d bytes of a 50KB failure, want at most %d",
			len(failures[0].Text), agent.MaxUnmatchedFailureText+len("..."))
	}
	// The tail, so the thing the provider said last survived the cut.
	if !strings.HasSuffix(failures[0].Text, "503 from the edge") {
		t.Errorf("text = %q, want it to end with what the run said last", failures[0].Text)
	}
}
