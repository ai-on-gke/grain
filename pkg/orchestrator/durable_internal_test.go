package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/runhost"
)

func durableStdTask(id string) model.Task {
	tk := stdTask(id)
	tk.Grants = []model.Grant{{Capability: model.DurableCapability, Via: model.GrantByLabel}}
	return tk
}

// A durable run has no clock: durableDispatch makes maxRunRuntime 0 and
// the wait-credit cap 0, the run clock built from them never fires, the
// run host is given no deadline, and its leases are minted for the long
// term rather than to a deadline.
func TestADurableRunHasNoDeadlineAndNoWaitCreditCap(t *testing.T) {
	cfg := durableDispatch(durableStdTask("d"), Config{MaxRunRuntime: time.Hour})
	if cfg.maxRunRuntime() != 0 || cfg.runWaitCreditCap() != 0 {
		t.Fatalf("durable: maxRunRuntime = %v, runWaitCreditCap = %v; want 0 and 0", cfg.maxRunRuntime(), cfg.runWaitCreditCap())
	}
	if !runHostDeadline(cfg).IsZero() {
		t.Fatalf("runHostDeadline = %v, want none", runHostDeadline(cfg))
	}
	ordinary := durableDispatch(stdTask("o"), Config{MaxRunRuntime: time.Hour})
	if ordinary.maxRunRuntime() != time.Hour || ordinary.runWaitCreditCap() != RunWaitCreditCap {
		t.Fatalf("ordinary: maxRunRuntime = %v, runWaitCreditCap = %v", ordinary.maxRunRuntime(), ordinary.runWaitCreditCap())
	}
	at := standingNow
	if got := capabilityDeadline(at, 0); !got.Equal(at.Add(durableLeaseTerm)) {
		t.Fatalf("capabilityDeadline(at, 0) = %v, want the long lease %v", got, at.Add(durableLeaseTerm))
	}

	ctx, clock := newRunClock(context.Background(), 0, 0)
	defer clock.stop()
	credit := clock.credit(time.Hour)
	if !credit.Deadline.IsZero() || credit.Credited != 0 || !credit.Live {
		t.Fatalf("crediting a durable clock = %+v, want no deadline and nothing credited", credit)
	}
	select {
	case <-ctx.Done():
		t.Fatalf("a durable run clock fired: %v", context.Cause(ctx))
	case <-time.After(50 * time.Millisecond):
	}
	actx, aclock := newAdoptedRunClock(context.Background(), time.Time{}, 0, time.Minute)
	defer aclock.stop()
	select {
	case <-actx.Done():
		t.Fatalf("an adopted durable run clock fired: %v", context.Cause(actx))
	case <-time.After(20 * time.Millisecond):
	}
}

// The prompt tells a durable run it has no deadline and what the idle
// watchdog does, instead of the budget paragraph.
func TestADurableRunsPromptSaysItHasNoDeadline(t *testing.T) {
	got := runtimeSection(durableStdTask("d"), 0)
	if !strings.Contains(got, "This run has no deadline; if it goes idle for 1h it is suspended and resumed") {
		t.Fatalf("durable runtime section = %q", got)
	}
	if strings.Contains(got, "wall-clock") {
		t.Fatalf("durable runtime section still states a budget: %q", got)
	}
	if got := runtimeSection(stdTask("o"), 0); got != "" {
		t.Fatalf("an ordinary task's zero budget = %q, want nothing said", got)
	}
}

// idleHarness is a durable run's watchdog against a real store, with a
// cancel func that records its cause.
type idleHarness struct {
	mu    sync.Mutex
	cause error
	fired chan struct{}
}

func (h *idleHarness) cancel(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cause == nil {
		h.cause = err
		close(h.fired)
	}
}

func startIdleRun(t *testing.T) (*model.Store, context.Context) {
	store, ctx := openTestStore(t)
	if err := store.PutTask(ctx, durableStdTask("d")); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{ID: "d-1", TaskID: "d", Attempt: 1, StartedAt: time.Now()}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	return store, ctx
}

func TestTheIdleWatchdogSuspendsARunThatDoesNothing(t *testing.T) {
	store, ctx := startIdleRun(t)
	h := &idleHarness{fired: make(chan struct{})}
	stop := watchIdle(ctx, idleWatch{store: store, taskID: "d", runID: "d-1",
		timeout: 100 * time.Millisecond, poll: 10 * time.Millisecond, cancel: h.cancel})
	defer stop()
	select {
	case <-h.fired:
	case <-time.After(5 * time.Second):
		t.Fatal("the watchdog never fired on a run doing nothing")
	}
	if !errors.Is(h.cause, errRunIdle) {
		t.Fatalf("cancelled with %v, want errRunIdle", h.cause)
	}
}

func TestTheIdleWatchdogLeavesARunWritingItsTranscript(t *testing.T) {
	store, ctx := startIdleRun(t)
	transcript := filepath.Join(t.TempDir(), "d-1")
	if err := os.WriteFile(transcript, []byte("start\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := &idleHarness{fired: make(chan struct{})}
	stop := watchIdle(ctx, idleWatch{store: store, taskID: "d", runID: "d-1", transcript: transcript,
		timeout: 150 * time.Millisecond, poll: 10 * time.Millisecond, cancel: h.cancel})
	defer stop()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		f, err := os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.WriteString("output\n")
		f.Close()
		select {
		case <-h.fired:
			t.Fatal("the watchdog fired on a run that was writing its transcript")
		case <-time.After(30 * time.Millisecond):
		}
	}
	select {
	case <-h.fired:
	case <-time.After(5 * time.Second):
		t.Fatal("the watchdog never fired once the transcript stopped")
	}
}

func TestTheIdleWatchdogLeavesARunInsideADeclaredWait(t *testing.T) {
	store, ctx := startIdleRun(t)
	if _, err := store.SetTaskActivity(ctx, "d", "waiting for CI", time.Now()); err != nil {
		t.Fatal(err)
	}
	h := &idleHarness{fired: make(chan struct{})}
	stop := watchIdle(ctx, idleWatch{store: store, taskID: "d", runID: "d-1",
		timeout: 100 * time.Millisecond, poll: 10 * time.Millisecond, cancel: h.cancel})
	defer stop()
	select {
	case <-h.fired:
		t.Fatal("the watchdog fired on a run blocked in wait_for_checks")
	case <-time.After(400 * time.Millisecond):
	}
	// A tool call that is not a wait starts: the run is working again,
	// and idle once it goes quiet.
	if _, err := store.SetTaskActivity(ctx, "d", "reading a file in the sandbox", time.Now()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-h.fired:
	case <-time.After(5 * time.Second):
		t.Fatal("the watchdog never fired once the wait was over and the run went quiet")
	}
}

func TestTheIdleWatchdogRenewsADurableRunsLeases(t *testing.T) {
	store, ctx := startIdleRun(t)
	clock := standingNow
	var mu sync.Mutex
	now := func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	renewed := make(chan time.Time, 1)
	h := &idleHarness{fired: make(chan struct{})}
	stop := watchIdle(ctx, idleWatch{store: store, taskID: "d", runID: "d-1", timeout: 48 * time.Hour,
		poll: 5 * time.Millisecond, cancel: h.cancel, now: now,
		renew: func(_ context.Context, until time.Time) error {
			select {
			case renewed <- until:
			default:
			}
			return nil
		}})
	defer stop()
	mu.Lock()
	clock = clock.Add(durableLeaseTerm / 4)
	mu.Unlock()
	select {
	case until := <-renewed:
		if want := standingNow.Add(durableLeaseTerm/4 + durableLeaseTerm); !until.Equal(want) {
			t.Fatalf("renewed to %v, want %v", until, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the leases were never renewed")
	}
}

// A durable task's setup failure is a setup suspension held for the
// backoff, never a strike, and the fifth in a row raises
// durable.setup-failing:<task>; an ordinary task's is still a counted
// setup-failed.
func TestADurableTasksSetupFailureIsASuspension(t *testing.T) {
	store, ctx := openTestStore(t)
	for _, tk := range []model.Task{durableStdTask("d"), stdTask("o")} {
		if err := store.PutTask(ctx, tk); err != nil {
			t.Fatal(err)
		}
	}
	var logged bytes.Buffer
	log.SetOutput(&logged)
	defer log.SetOutput(os.Stderr)
	for i := 1; i <= model.DurableSetupFailingAfter; i++ {
		at := standingNow.Add(time.Duration(i) * time.Hour)
		id := "d-" + string(rune('0'+i))
		if err := store.StartRun(ctx, model.Run{ID: id, TaskID: "d", Attempt: i, StartedAt: at}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		if err := finishSetupFailed(ctx, store, "d", id, at, "sandbox could not be prepared"); err != nil {
			t.Fatal(err)
		}
		subject := model.DurableSetupFailingSubject("d")
		if raised := strings.Contains(logged.String(), subject); raised != (i == model.DurableSetupFailingAfter) {
			t.Fatalf("after %d setup suspensions, %s raised = %v", i, subject, raised)
		}
		resume, err := store.SuspensionResumeAt(ctx, "d")
		if err != nil || resume == nil || !resume.Equal(at.Add(2*time.Minute)) {
			t.Fatalf("resume at = %v, %v; want %v", resume, err, at.Add(2*time.Minute))
		}
	}
	runs, err := store.Runs(ctx, "d")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range runs {
		if r.Outcome != model.SuspendedOutcome || r.SuspendKind != model.SuspendSetup {
			t.Fatalf("run %s recorded as (%q, %q), want a setup suspension", r.ID, r.Outcome, r.SuspendKind)
		}
	}
	if n := failureCount(t, ctx, store, "d"); n != 0 {
		t.Fatalf("failure streak = %d, want setup suspensions to count for nothing", n)
	}

	if err := store.StartRun(ctx, model.Run{ID: "o-1", TaskID: "o", Attempt: 1, StartedAt: standingNow}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := finishSetupFailed(ctx, store, "o", "o-1", standingNow, "sandbox could not be prepared"); err != nil {
		t.Fatal(err)
	}
	if n := failureCount(t, ctx, store, "o"); n != 1 {
		t.Fatalf("an ordinary task's setup failure counted %d, want 1", n)
	}
}

type blockingAdoptHost struct{}

func (blockingAdoptHost) Attach(ctx context.Context, _ runhost.Info, _ int64, _ io.Writer) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}
func (blockingAdoptHost) Release(context.Context, runhost.Info, string) {}

type resultParserFramework struct{}

func (resultParserFramework) Run(context.Context, agent.RunConfig) (*agent.Result, error) {
	return nil, errors.New("unused")
}
func (resultParserFramework) ParseResult(_ string, err error) (*agent.Result, error) {
	return nil, err
}

func TestResumeAdoptedRunWatchesAnAdoptedDurableRunForIdleness(t *testing.T) {
	store, ctx := startIdleRun(t)
	sb, err := NewHostSandboxes(t.TempDir()).Acquire(ctx, "d-1", Shape{})
	if err != nil {
		t.Fatal(err)
	}
	deps := Deps{
		Store:     store,
		Sandboxes: NewHostSandboxes(t.TempDir()),
		Framework: StaticFramework(resultParserFramework{}),
		Config:    Config{DurableIdleTimeout: 60 * time.Millisecond},
	}
	adopted := AdoptedRun{
		Adoption: runhost.Adoption{Claim: runhost.Claim{Task: "d", Run: "d-1"}},
		Sandbox:  sb,
	}
	err = ResumeAdoptedRun(ctx, deps, blockingAdoptHost{}, adopted)
	if !errors.Is(err, errRunIdle) {
		t.Fatalf("ResumeAdoptedRun err = %v, want errRunIdle", err)
	}
	runs, err := store.Runs(ctx, "d")
	if err != nil || len(runs) != 1 {
		t.Fatalf("Runs = %+v, %v", runs, err)
	}
	if runs[0].Outcome != model.SuspendedOutcome || runs[0].SuspendKind != model.SuspendIdle {
		t.Fatalf("adopted durable run ended as (%q, %q), want (%q, %q)",
			runs[0].Outcome, runs[0].SuspendKind, model.SuspendedOutcome, model.SuspendIdle)
	}
}
