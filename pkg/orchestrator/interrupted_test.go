package orchestrator_test

// A run the daemon is stopped in the middle of -- an upgrade, a restart --
// is interrupted, not failed: no strike against its task, the task back in
// the queue with a note saying why, and the conversation the run was in
// still there for the next dispatch to resume. These pin both routes to
// that ending: RunDispatch's own, when the daemon's context is cancelled
// under a live run, and RecoverOrphanedRuns', when the process was gone
// before its run could finish at all.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// The session goes on record the moment the framework names it, while
// the run is still going, and survives the run being cancelled by the
// daemon stopping -- even though the framework comes back with no Result
// at all, which is exactly the ending that used to leave nothing behind.
func TestADaemonStoppedMidRunKeepsItsConversationAndTakesNoStrike(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")
	d := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	daemonCtx, stopDaemon := context.WithCancel(ctx)
	defer stopDaemon()
	before := orchestrator.InterruptedRuns()
	fw := agentFunc(func(runCtx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		if cfg.OnResumableSession == nil {
			t.Fatal("RunConfig.OnResumableSession is nil: nothing can record the session before the run returns")
		}
		cfg.OnResumableSession("sess-early")
		// Recorded already, mid-run: a daemon killed outright at this
		// point, with no drain at all, still leaves it behind.
		if obs := observationOf(t, ctx, store, "t1"); obs == nil || obs.SuspendedSession == nil ||
			obs.SuspendedSession.Token != "sess-early" {
			t.Errorf("mid-run the task's suspended session = %+v, want sess-early", obs)
		}
		stopDaemon()
		<-runCtx.Done()
		return nil, runCtx.Err()
	})
	if _, err := orchestrator.RunDispatch(daemonCtx, store, fw, claudeConfig(),
		*task, d, nil, t.TempDir(), "", nil, baseTime); err == nil {
		t.Fatal("RunDispatch err = nil, want the interruption")
	}

	runs, err := store.Runs(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Outcome != model.InterruptedOutcome {
		t.Fatalf("runs = %+v, want the one run finished as %s", runs, model.InterruptedOutcome)
	}
	if got := model.EndingOf(runs[0].Outcome, runs[0].Detail); got != model.EndingInterrupted {
		t.Errorf("EndingOf = %q, want %q", got, model.EndingInterrupted)
	}
	obs := observationOf(t, ctx, store, "t1")
	if obs == nil || obs.SuspendedSession == nil || obs.SuspendedSession.Token != "sess-early" {
		t.Fatalf("after the interruption the task's suspended session = %+v, want sess-early", obs)
	}
	if obs.SuspendedSession.RunID != "r1" || obs.SuspendedSession.Framework != model.AgentFrameworkClaude {
		t.Errorf("suspended session = %+v, want run r1 on claude", obs.SuspendedSession)
	}
	assertInterruptedTaskRequeued(t, ctx, store, "t1", "r1")
	if got := orchestrator.InterruptedRuns(); got <= before {
		t.Errorf("InterruptedRuns = %d, want more than %d: the shutdown's journal line counts these", got, before)
	}
}

// What the framework says at the end still has the last word over what
// it named early, as it always did: here it moved to another session
// after the early report, and the task is left with the later one.
func TestTheRunsOwnAnswerReplacesTheSessionRecordedEarly(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")
	d := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	var report func(string)
	fw := agentFunc(func(_ context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		report = cfg.OnResumableSession
		cfg.OnResumableSession("sess-early")
		return &agent.Result{FinalText: "done", ToolCalls: []agent.ToolCall{{Name: "run_command"}},
			ResumableSession: "sess-final"}, nil
	})
	if _, err := orchestrator.RunDispatch(ctx, store, fw, claudeConfig(),
		*task, d, nil, t.TempDir(), "", nil, baseTime); err != nil {
		t.Fatalf("RunDispatch: %v", err)
	}
	// A report straggling in after the run has had its say changes
	// nothing.
	report("sess-straggler")
	obs := observationOf(t, ctx, store, "t1")
	if obs == nil || obs.SuspendedSession == nil || obs.SuspendedSession.Token != "sess-final" {
		t.Fatalf("suspended session = %+v, want sess-final", obs)
	}
}

// Closing the task under a run clears even a session recorded early:
// there is no next run to resume it into.
func TestClosingTheTaskClearsASessionRecordedEarly(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")
	d := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	fw := agentFunc(func(runCtx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		cfg.OnResumableSession("sess-early")
		closeTask(t, ctx, store, "t1", baseTime)
		<-runCtx.Done()
		return nil, runCtx.Err()
	})
	cfg := claudeConfig()
	cfg.CancelPollInterval = 10 * time.Millisecond
	if _, err := orchestrator.RunDispatch(ctx, store, fw, cfg,
		*task, d, nil, t.TempDir(), "", nil, baseTime); err == nil {
		t.Fatal("RunDispatch err = nil, want the cancellation")
	}
	if obs := observationOf(t, ctx, store, "t1"); obs != nil && obs.SuspendedSession != nil {
		t.Errorf("the closed task still carries %+v", obs.SuspendedSession)
	}
}

// RecoverOrphanedRuns' half: a run the previous process never finished is
// interrupted as well, takes no strike however many times it happens, and
// keeps the session it recorded early.
func TestRecoverOrphanedRunsInterruptsWithoutAStrikeAndKeepsTheSession(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	task := filedTask(t, ctx, store, "t1", model.RepoRef{Owner: "acme", Name: "widgets"})
	if err := store.SetSuspendedSession(ctx, task.ID, &model.SuspendedSession{
		Framework: model.AgentFrameworkClaude, Token: "sess-early", RunID: "t1-1", At: baseTime,
	}, baseTime); err != nil {
		t.Fatal(err)
	}

	// More restarts in a row than MaxConsecutiveFailures would allow
	// failures: none of them may retire the task.
	at := baseTime
	for i := 1; i <= model.MaxConsecutiveFailures+1; i++ {
		d := dispatch.Dispatch{TaskID: task.ID, RunID: fmt.Sprintf("t1-%d", i), Attempt: i}
		startRun(t, ctx, store, d, at)
		at = at.Add(time.Hour)
		if err := orchestrator.RecoverOrphanedRuns(ctx, store, client, at); err != nil {
			t.Fatalf("RecoverOrphanedRuns: %v", err)
		}
	}

	runs, err := store.Runs(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range runs {
		if r.Outcome != model.InterruptedOutcome {
			t.Errorf("run %s outcome = %q, want %q", r.ID, r.Outcome, model.InterruptedOutcome)
		}
	}
	assertInterruptedTaskRequeued(t, ctx, store, task.ID, "t1-1")
	obs := observationOf(t, ctx, store, task.ID)
	if obs == nil || obs.SuspendedSession == nil || obs.SuspendedSession.Token != "sess-early" {
		t.Errorf("after recovery the task's suspended session = %+v, want sess-early left alone", obs)
	}
}

// RunCycle's afterAgentRun tail must not attempt salvagePushedBranch when a
// run stops on errDaemonStopped, even if the agent already pushed commits to
// the task's remote branch and the framework returns a partial Result. With
// the daemon's ctx already cancelled, salvagePushedBranch fails in taskClosed
// and noteFinishFailure would overwrite "interrupted" with "finish-failed"
// and increment the task's failure streak (or, if ctx were still live,
// prematurely complete a task whose run was interrupted mid-turn).
func TestRunCycleStoppedMidRunWithAPushedBranchDoesNotSalvageOrOverwriteInterruptedOutcome(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	pushBranch(t, sim.BareRepo, model.BranchName(task.ID))

	daemonCtx, stopDaemon := context.WithCancel(ctx)
	defer stopDaemon()
	fw := agentFunc(func(runCtx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		if cfg.OnResumableSession != nil {
			cfg.OnResumableSession("sess-early")
		}
		stopDaemon()
		<-runCtx.Done()
		return &agent.Result{
			ToolCalls: []agent.ToolCall{{Name: "run_command", Text: "git push"}},
		}, runCtx.Err()
	})
	deps := orchestrator.Deps{
		Store:      store,
		Client:     client,
		Sandboxes:  orchestrator.NewHostSandboxes(t.TempDir()),
		Framework:  orchestrator.StaticFramework(fw),
		MaxWorkers: 1,
	}
	if err := orchestrator.RunCycle(daemonCtx, deps, baseTime); err == nil {
		t.Fatal("RunCycle err = nil, want the daemon shutdown error")
	}

	runs, err := store.Runs(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Outcome != model.InterruptedOutcome {
		t.Fatalf("runs = %+v, want the one run kept as %q (not overwritten with finish-failed)", runs, model.InterruptedOutcome)
	}
	if got := model.EndingOf(runs[0].Outcome, runs[0].Detail); got != model.EndingInterrupted {
		t.Errorf("EndingOf = %q, want %q", got, model.EndingInterrupted)
	}
	if len(sim.PullRequests) != 0 {
		t.Errorf("pull requests = %+v, want none opened for an interrupted run", sim.PullRequests)
	}
	assertInterruptedTaskRequeued(t, ctx, store, task.ID, runs[0].ID)
}

// assertInterruptedTaskRequeued is the shared half of both routes: the
// task is queued, carries no streak, and says in its own conversation
// that runID was interrupted by the daemon stopping.
func assertInterruptedTaskRequeued(t *testing.T, ctx context.Context, store *model.Store, taskID, runID string) {
	t.Helper()
	if st, err := store.State(ctx, taskID); err != nil || st != model.StateQueued {
		t.Errorf("state = %q (%v), want queued", st, err)
	}
	if streak, err := store.FailureStreak(ctx, taskID); err != nil {
		t.Fatal(err)
	} else if streak != nil && (streak.Count != 0 || streak.BackoffCount != 0) {
		t.Errorf("FailureStreak = %+v, want no strike and no backoff", streak)
	}
	comments, err := store.Comments(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range comments {
		if strings.Contains(c.Body, "Run "+runID+" was interrupted") &&
			strings.Contains(c.Body, "upgrade or a restart") {
			return
		}
	}
	t.Errorf("no note on %s says run %s was interrupted by an upgrade or restart; comments = %+v", taskID, runID, comments)
}

// stoppingSandboxes stops the daemon while the run's sandbox is still
// being built, and fails the way a backend does when its acquire's ctx
// is cancelled under it: the run is in setup, with no CLI started.
type stoppingSandboxes struct{ stop func() }

func (s stoppingSandboxes) Acquire(ctx context.Context, name string, shape orchestrator.Shape) (orchestrator.Sandbox, error) {
	s.stop()
	<-ctx.Done()
	return nil, ctx.Err()
}

// A run the daemon is stopped under while its sandbox is still building
// is handed back at shutdown: finished interrupted (not setup-failed),
// no strike, the task queued again at once with the Run now request it
// spent put back with its original timestamp (task 837's rule), so it
// keeps its place at the head of the queue.
func TestADaemonStoppedMidSetupHandsTheRunBackWithItsRunNow(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	requested := baseTime.Add(-time.Minute)
	requestRunNow(t, store, task.ID, requested)

	daemonCtx, stopDaemon := context.WithCancel(ctx)
	defer stopDaemon()
	stopped := false
	deps := orchestrator.Deps{
		Store:  store,
		Client: client,
		Sandboxes: stoppingSandboxes{stop: func() {
			stopped = true
			stopDaemon()
		}},
		Framework: orchestrator.StaticFramework(agentFunc(func(context.Context, agent.RunConfig) (*agent.Result, error) {
			t.Error("the agent ran, but the daemon was stopped before its sandbox was built")
			return nil, nil
		})),
		MaxWorkers: 1,
	}
	before := orchestrator.InterruptedRuns()
	err := orchestrator.RunCycle(daemonCtx, deps, baseTime)
	if !stopped {
		t.Fatalf("RunCycle = %v without building a sandbox: the run never reached setup", err)
	}
	if err == nil {
		t.Fatal("RunCycle err = nil, want the daemon shutdown error")
	}

	runs, err := store.Runs(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].FinishedAt == nil || runs[0].Outcome != model.InterruptedOutcome {
		t.Fatalf("runs = %+v, want the one run finished as %s, not %s", runs, model.InterruptedOutcome,
			model.SetupFailedOutcome)
	}
	assertInterruptedTaskRequeued(t, ctx, store, task.ID, runs[0].ID)
	if got := runNowOf(t, store, task.ID); got == nil || !got.Equal(requested) {
		t.Errorf("Run now = %v, want it put back as %v", got, requested)
	}
	reqs, err := store.RunNow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 1 || reqs[0].TaskID != task.ID {
		t.Errorf("Run now requests = %+v, want %s's, ready to dispatch again at once", reqs, task.ID)
	}
	if got := orchestrator.InterruptedRuns(); got <= before {
		t.Errorf("InterruptedRuns = %d, want more than %d: the shutdown's journal line counts this run", got, before)
	}
}

// RunDispatch's half of the same: a daemon stopped before the run's
// checkout and prompt were ready finishes it interrupted too, rather than
// as a setup failure or a failed run.
func TestRunDispatchStoppedBeforeItsAgentStartedIsInterrupted(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")
	d := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store, d, baseTime)
	task, err := store.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}
	daemonCtx, stopDaemon := context.WithCancel(ctx)
	stopDaemon()
	fw := agentFunc(func(context.Context, agent.RunConfig) (*agent.Result, error) {
		t.Error("the agent ran under a stopped daemon")
		return nil, nil
	})
	if _, err := orchestrator.RunDispatch(daemonCtx, store, fw, claudeConfig(),
		*task, d, nil, t.TempDir(), "", nil, baseTime); err == nil || !orchestrator.StoppedByDaemon(errCtx(err)) {
		t.Fatalf("RunDispatch err = %v, want the daemon's stop", err)
	}
	runs, err := store.Runs(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Outcome != model.InterruptedOutcome || runs[0].Detail != orchestrator.InterruptedInSetupDetail {
		t.Fatalf("runs = %+v, want r1 finished as %s in setup", runs, model.InterruptedOutcome)
	}
	assertInterruptedTaskRequeued(t, ctx, store, "t1", "r1")
}

// errCtx is a context cancelled with err as its cause, so StoppedByDaemon
// can say whether err is the daemon's stop.
func errCtx(err error) context.Context {
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(err)
	return ctx
}
