package orchestrator_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/agent"
	"github.com/bwsalmon/grain/pkg/authfix"
	"github.com/bwsalmon/grain/pkg/authfix/authfixtest"
	"github.com/bwsalmon/grain/pkg/dispatch"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
)

// A host that refuses to exec the agent CLI because the account's
// credentials have lapsed (the made-up authfixtest host). Every run failed in seconds as
//
//	antigravity: running agy: fork/exec /opt/tools/cli: host credential lapsed (stderr: )
//
// and counted against its task. What it is instead: the registered fix's
// sentence, recorded as model.EnvironmentOutcome (no strike against the
// task), with the task parked on a comment saying what to run -- and the
// framework not paused as a refusal burst, however many runs end this way.
func TestRunDispatchParksAnExpiredHostCredentialWithoutAStrike(t *testing.T) {
	t.Parallel()
	authfixtest.Register(t)
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	const path = "/opt/tools/cli"
	// What agent.LocalHost.Run hands a framework when exec is refused,
	// and what the framework then returns (agent.RunFailure).
	fw := agentFunc(func(context.Context, agent.RunConfig) (*agent.Result, error) {
		execErr := &fs.PathError{Op: "fork/exec", Path: path, Err: authfixtest.ErrLapsed}
		runErr := fmt.Errorf("%w (stderr: )", authfix.Wrap(execErr, "", "agy"))
		return nil, agent.RunFailure("antigravity", "agy", nil, runErr)
	})
	pause := &orchestrator.Pause{}
	cfg := orchestrator.Config{Pause: pause, Now: func() time.Time { return baseTime }}

	// More runs than a refusal burst needs, all fast: none of them may
	// read as the provider refusing.
	for i := 1; i <= 4; i++ {
		d := dispatch.Dispatch{TaskID: "t1", RunID: fmt.Sprintf("r%d", i), Attempt: i}
		startRun(t, ctx, store, d, baseTime)
		_, runErr := orchestrator.RunDispatch(ctx, store, fw, cfg, *task, d, nil, t.TempDir(), "", nil, baseTime)
		if runErr == nil {
			t.Fatal("RunDispatch err = nil, want the failure reported")
		}
		if !errors.Is(runErr, authfixtest.ErrLapsed) {
			t.Errorf("RunDispatch err = %v, want it to still carry the refusal", runErr)
		}
	}

	runs, err := store.Runs(ctx, "t1")
	if err != nil || len(runs) != 4 {
		t.Fatalf("runs = %+v (%v), want four", runs, err)
	}
	for _, r := range runs {
		if r.Outcome != model.EnvironmentOutcome {
			t.Errorf("run %s outcome = %q, want %q", r.ID, r.Outcome, model.EnvironmentOutcome)
		}
		if !strings.HasPrefix(r.Detail, authfixtest.Summary+": "+authfixtest.Action) || !strings.Contains(r.Detail, path) {
			t.Errorf("run %s detail = %q, want the fix's sentence naming %s", r.ID, r.Detail, path)
		}
		if got := model.EndingOf(r.Outcome, r.Detail); got != model.EndingEnvironment {
			t.Errorf("EndingOf = %q, want %q", got, model.EndingEnvironment)
		}
	}

	streak, err := store.FailureStreak(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if streak == nil || streak.Count != 0 || streak.BackoffCount != 0 {
		t.Errorf("FailureStreak = %+v, want no strike and no backoff", streak)
	}
	if pause.Blocked(baseTime).Any() {
		t.Error("dispatch was paused: expired host credentials are not a provider refusal")
	}

	obs, err := store.GetObservation(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || obs.PendingQuestionCommentID == nil {
		t.Fatalf("observation = %+v, want the task parked on a comment", obs)
	}
	comments, err := store.Comments(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	var parkedOn *model.Comment
	for i := range comments {
		if comments[i].ID == *obs.PendingQuestionCommentID {
			parkedOn = &comments[i]
		}
	}
	if parkedOn == nil || !strings.Contains(parkedOn.Body, authfixtest.Action) {
		t.Errorf("parked on %+v, want a comment saying what to run", parkedOn)
	}
	if state, err := store.State(ctx, "t1"); err != nil || state != model.StateAwaitingReply {
		t.Errorf("state = %q (%v), want %q", state, err, model.StateAwaitingReply)
	}
}

// An ordinary exec failure is still an ordinary failure: the fix's
// sentence is for what a classifier recognises alone.
func TestRunDispatchLeavesOtherExecFailuresAsFailures(t *testing.T) {
	t.Parallel()
	authfixtest.Register(t)
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t2")
	task, err := store.GetTask(ctx, "t2")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}
	fw := agentFunc(func(context.Context, agent.RunConfig) (*agent.Result, error) {
		execErr := &fs.PathError{Op: "fork/exec", Path: "/usr/bin/agy", Err: fs.ErrNotExist}
		return nil, agent.RunFailure("antigravity", "agy", nil, authfix.Wrap(execErr, "", "agy"))
	})
	cfg := orchestrator.Config{Now: func() time.Time { return baseTime }}
	d := dispatch.Dispatch{TaskID: "t2", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)
	if _, runErr := orchestrator.RunDispatch(ctx, store, fw, cfg, *task, d, nil, t.TempDir(), "", nil, baseTime); runErr == nil {
		t.Fatal("RunDispatch err = nil, want the failure reported")
	}
	runs, err := store.Runs(ctx, "t2")
	if err != nil || len(runs) != 1 || runs[0].Outcome != "failed" {
		t.Fatalf("runs = %+v (%v), want one failed run", runs, err)
	}
	if streak, err := store.FailureStreak(ctx, "t2"); err != nil || streak == nil || streak.Count != 1 {
		t.Errorf("FailureStreak = %+v (%v), want one strike", streak, err)
	}
}
