package ci

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSandboxes is a whole sandbox backend in forty lines: it records
// every command it was asked to run, answers each one from script, and
// keeps count of how many sandboxes are alive at once so a test can
// check the engine's parallelism rather than infer it from a stopwatch.
type fakeSandboxes struct {
	mu sync.Mutex
	// script decides what a command does, by the script it was handed.
	// nil means every command succeeds silently.
	script func(cmd Command) (CommandResult, error)
	// acquireErr, when set, fails every Acquire.
	acquireErr error
	// block, when set, is closed by the test to let commands finish.
	block chan struct{}
	// acquires and execs, when set, are sent to as each sandbox is
	// acquired and as each command starts. They are how a test waits for
	// the engine to have got somewhere: a signal the engine itself
	// raises, rather than a stopwatch it knows nothing about. Polling
	// these counters behind a one-second deadline instead is what made
	// these tests red on a loaded CI host, where a second of wall clock
	// bounds nothing (grain/task-426). Give them room for every event
	// the test waits on -- a send that would block is dropped rather
	// than stalling the engine.
	acquires chan SandboxRequest
	execs    chan Command

	commands  []Command
	acquired  []SandboxRequest
	released  []string
	live      int
	peakLive  int
	sandboxes int
}

func (f *fakeSandboxes) Acquire(_ context.Context, req SandboxRequest) (Sandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.acquireErr != nil {
		return nil, f.acquireErr
	}
	f.acquired = append(f.acquired, req)
	f.sandboxes++
	f.live++
	if f.live > f.peakLive {
		f.peakLive = f.live
	}
	signal(f.acquires, req)
	return &fakeSandbox{owner: f, name: req.Name}, nil
}

// signal tells a test something happened without ever making the engine
// wait for the test to be listening: an unset or full channel drops it.
func signal[T any](ch chan<- T, v T) {
	if ch == nil {
		return
	}
	select {
	case ch <- v:
	default:
	}
}

// signalWaitLimit is a ceiling, not a budget -- the one this package's
// tests wait under, whether what they are waiting for comes from the
// engine, a gate or the service. Everything they wait for is a signal the
// code under test raises as it goes, so it either arrives as fast as the
// machine can carry it or never arrives at all; the limit exists only so
// a genuinely stuck run fails the test rather than hanging the package
// until go test's own timeout. Nothing here asserts the engine is quick,
// because a CI host running the whole suite at once is not quick and
// never was: a second of wall clock was enough on an idle machine and
// failed on eight branches at once while a dozen sandboxes were each
// building a guest image (grain/task-394, grain/task-426). A generous
// ceiling costs a genuinely hung engine nothing but a slower red, and
// costs a merely busy one nothing at all.
const signalWaitLimit = 30 * time.Second

// awaitSignal blocks for one value from ch, failing the test with what
// it was waiting for if the code under test never gets there.
func awaitSignal[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(signalWaitLimit):
		var zero T
		t.Fatalf("%s (nothing after %s)", what, signalWaitLimit)
		return zero
	}
}

type fakeSandbox struct {
	owner *fakeSandboxes
	name  string
}

type blockingAdmission struct {
	entered chan struct{}
	allow   chan struct{}
	mu      sync.Mutex
	blocked string
}

func (a *blockingAdmission) Acquire(ctx context.Context, req SandboxRequest, wait func(Wait)) (func(), error) {
	a.mu.Lock()
	if a.blocked != "" {
		a.mu.Unlock()
		return func() {}, nil
	}
	a.blocked = req.Name
	a.mu.Unlock()
	if wait != nil {
		wait(Wait{Cause: WaitForGrant, Reason: "waiting for Gemini API keys (gemini-key grant)"})
	}
	close(a.entered)
	select {
	case <-a.allow:
		return func() {}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *fakeSandbox) Name() string { return s.name }

func (s *fakeSandbox) Exec(ctx context.Context, cmd Command) (CommandResult, error) {
	s.owner.mu.Lock()
	s.owner.commands = append(s.owner.commands, cmd)
	block, script := s.owner.block, s.owner.script
	signal(s.owner.execs, cmd)
	s.owner.mu.Unlock()

	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return CommandResult{ExitCode: -1}, ctx.Err()
		}
	}
	if script == nil {
		return CommandResult{}, nil
	}
	return script(cmd)
}

func (s *fakeSandbox) Release(context.Context) error {
	s.owner.mu.Lock()
	defer s.owner.mu.Unlock()
	s.owner.live--
	s.owner.released = append(s.owner.released, s.name)
	return nil
}

func planFor(t *testing.T, req Request, files map[string]string) Plan {
	t.Helper()
	var workflows []Workflow
	for path, body := range files {
		workflows = append(workflows, mustParse(t, path, body))
	}
	plan, err := PlanRun(req, workflows)
	if err != nil {
		t.Fatalf("planning: %v", err)
	}
	return plan
}

func jobByID(t *testing.T, run Run, id string) JobRun {
	t.Helper()
	for _, j := range run.Jobs {
		if j.JobID == id {
			return j
		}
	}
	t.Fatalf("run has no job %q: %+v", id, run.Jobs)
	return JobRun{}
}

func TestEngineRunsAJobsStepsInOrderAndReportsThem(t *testing.T) {
	sandboxes := &fakeSandboxes{script: func(cmd Command) (CommandResult, error) {
		return CommandResult{Output: "ran " + cmd.Script}, nil
	}}
	plan := planFor(t, Request{Repo: "o/r", Ref: "main", Commit: "abc", Event: EventPush}, map[string]string{
		".grain/workflows/a.yml": "on: push\njobs:\n  one:\n    steps:\n      - name: first\n        run: echo one\n      - name: second\n        run: echo two\n",
	})

	run := (&Engine{Sandboxes: sandboxes}).Run(context.Background(), "r1", plan, nil)
	if run.Conclusion != ConclusionSuccess {
		t.Fatalf("conclusion: got %q (%s)", run.Conclusion, run.Error)
	}
	job := jobByID(t, run, "one")
	if len(job.Steps) != 2 || job.Steps[0].Name != "first" || job.Steps[1].Name != "second" {
		t.Fatalf("steps: got %+v", job.Steps)
	}
	if job.Steps[0].Output != "ran echo one" {
		t.Errorf("step output: got %q", job.Steps[0].Output)
	}
	if job.Sandbox == "" || !strings.Contains(job.Sandbox, "r1") {
		t.Errorf("a job records the sandbox it ran in: got %q", job.Sandbox)
	}
	if len(sandboxes.released) != 1 {
		t.Errorf("every sandbox is released: acquired %d, released %v", sandboxes.sandboxes, sandboxes.released)
	}
	if got := sandboxes.acquired[0]; got.Repo != "o/r" || got.Commit != "abc" || got.Ref != "main" {
		t.Errorf("the backend is told what to check out: got %+v", got)
	}
}

// TestGrantWaitDoesNotTakeCISandboxCapacityAndIsObservable is about
// ordering, not speed: a grant-blocked job holds no sandbox while it
// waits, the wait is reported with an empty Sandbox so a reader can see
// it before there is a machine to name, and the run finishes once the
// grant arrives. Every step of it therefore waits on the engine's own
// signals with no wall-clock budget -- see signalWaitLimit.
func TestGrantWaitDoesNotTakeCISandboxCapacityAndIsObservable(t *testing.T) {
	sandboxes := &fakeSandboxes{acquires: make(chan SandboxRequest, 2)}
	admission := &blockingAdmission{entered: make(chan struct{}), allow: make(chan struct{})}
	plan := planFor(t, Request{Repo: "o/r", Ref: "main", Event: EventPush}, map[string]string{
		".grain/workflows/a.yml": "on: push\njobs:\n  held:\n    steps:\n      - run: held\n  free:\n    steps:\n      - run: free\n",
	})
	// The observer keeps only the grant waits, and keeps them without
	// ever blocking the engine: a test that stops draining every
	// observation the engine makes stops the run itself, which is a
	// deadlock dressed up as whatever deadline the test had on it.
	grantWaits := make(chan JobRun, 4)
	done := make(chan Run, 1)
	go func() {
		done <- (&Engine{Sandboxes: sandboxes, Admission: admission, Concurrency: 1}).Run(
			context.Background(), "r1", plan, func(job JobRun) {
				if job.Activity == "waiting for Gemini API keys (gemini-key grant)" {
					signal(grantWaits, job)
				}
			})
	}()

	awaitSignal(t, admission.entered, "held job never reached grant admission")

	// The first sandbox the engine takes, with Concurrency 1 and one job
	// held at admission, has to be the other job's: the wait costs the
	// run no capacity.
	first := awaitSignal(t, sandboxes.acquires, "the unrelated CI job did not use the free sandbox capacity")
	admission.mu.Lock()
	blocked := admission.blocked
	admission.mu.Unlock()
	if first.Name == blocked {
		t.Fatalf("grant-blocked job acquired the first sandbox: %+v", first)
	}

	// The wait is reported before there is any machine to report, so the
	// first report of it names no sandbox.
	wait := awaitSignal(t, grantWaits, "no observable grant wait was reported")
	if wait.Sandbox != "" {
		t.Errorf("the grant wait was reported in sandbox %q, want none: it is waiting for a grant, not running", wait.Sandbox)
	}

	close(admission.allow)
	run := awaitSignal(t, done, "run did not finish after the grant became available")
	if run.Conclusion != ConclusionSuccess {
		t.Fatalf("run conclusion = %q: %s", run.Conclusion, run.Error)
	}
}

// refusingAdmission reports the wait the way the real one does and then
// refuses: the deployment's gcp-key minting is forbidden by an
// organization policy, so no amount of waiting will produce a grant.
type refusingAdmission struct{ err error }

func (a *refusingAdmission) Acquire(_ context.Context, _ SandboxRequest, wait func(Wait)) (func(), error) {
	if wait != nil {
		wait(Wait{Cause: WaitForGrant, Reason: "waiting for GCP service-account keys (gcp-key grant)"})
	}
	return nil, a.err
}

// A job that never got its capability grants has to be recorded as a job
// that did no work, and the record has to be able to say so: it is
// finished and it never started, which is a StartedAt of nil rather
// than a zero time.Time. The zero one went out over the wire whatever
// the field's `omitempty` said, because omitempty does not omit a
// struct -- which is how one of these was drawn on the CI page as
// having taken 17756979h 39m, the span from the year 1 to that
// afternoon.
func TestJobRefusedItsGrantsIsRecordedAsHavingDoneNoWork(t *testing.T) {
	sandboxes := &fakeSandboxes{}
	plan := planFor(t, Request{Repo: "o/r", Ref: "main", Event: EventPush}, map[string]string{
		".grain/workflows/a.yml": "on: push\njobs:\n  held:\n    steps:\n      - run: one\n      - run: two\n",
	})

	run := (&Engine{Sandboxes: sandboxes, Admission: &refusingAdmission{err: errors.New("no keys for you")}}).Run(
		context.Background(), "r1", plan, nil)

	job := jobByID(t, run, "held")
	if job.Conclusion != ConclusionErrored {
		t.Fatalf("conclusion = %q, want errored: the fault is the deployment's, not the commit's", job.Conclusion)
	}
	if !strings.Contains(job.Error, "no keys for you") {
		t.Errorf("error = %q, want GCP's own refusal in it", job.Error)
	}
	if job.StartedAt != nil {
		t.Errorf("startedAt = %v, want none at all: this job never got a machine to start on", *job.StartedAt)
	}
	if job.FinishedAt == nil {
		t.Error("finishedAt is absent, want the instant the grant was refused: it is over, whatever else it is")
	}
	if job.Activity != "" || job.ActivityAt != nil {
		t.Errorf("activity = %q at %v, want it cleared: a finished job is not still waiting for anything", job.Activity, job.ActivityAt)
	}
	// What the wire says is the whole point: an absent field rather than
	// an instant every reader has to disbelieve.
	encoded, err := json.Marshal(job)
	if err != nil {
		t.Fatalf("marshalling the job: %v", err)
	}
	if bytes.Contains(encoded, []byte("startedAt")) {
		t.Errorf("serialized job = %s, want no startedAt in it at all", encoded)
	}
	if len(job.Steps) != 2 {
		t.Fatalf("steps = %+v, want both of them recorded as passed over", job.Steps)
	}
	for _, step := range job.Steps {
		if step.Conclusion != ConclusionSkipped || step.ExitCode != -1 {
			t.Errorf("step %q = %q exit %d, want skipped with no status of its own", step.Name, step.Conclusion, step.ExitCode)
		}
	}
	if len(sandboxes.acquired) != 0 {
		t.Errorf("a job refused its grants took a machine anyway: %+v", sandboxes.acquired)
	}
}

func TestEngineFailsTheJobAtTheFirstFailingStepAndSkipsTheRest(t *testing.T) {
	sandboxes := &fakeSandboxes{script: func(cmd Command) (CommandResult, error) {
		if strings.Contains(cmd.Script, "boom") {
			return CommandResult{Output: "boom", ExitCode: 3}, nil
		}
		return CommandResult{}, nil
	}}
	plan := planFor(t, Request{Repo: "o/r", Ref: "main", Event: EventPush}, map[string]string{
		".grain/workflows/a.yml": "on: push\njobs:\n  one:\n    steps:\n      - run: ok\n      - run: boom\n      - run: never\n",
	})

	run := (&Engine{Sandboxes: sandboxes}).Run(context.Background(), "r1", plan, nil)
	if run.Conclusion != ConclusionFailure {
		t.Fatalf("conclusion: got %q", run.Conclusion)
	}
	steps := jobByID(t, run, "one").Steps
	if steps[0].Conclusion != ConclusionSuccess || steps[1].Conclusion != ConclusionFailure || steps[2].Conclusion != ConclusionSkipped {
		t.Fatalf("step conclusions: got %+v", steps)
	}
	if steps[1].ExitCode != 3 {
		t.Errorf("a step reports the status its script exited with: got %d", steps[1].ExitCode)
	}
	for _, cmd := range sandboxes.commands {
		if cmd.Script == "never" {
			t.Error("a step after a failure must not run")
		}
	}
}

func TestEngineCarriesOnPastAContinueOnErrorStep(t *testing.T) {
	sandboxes := &fakeSandboxes{script: func(cmd Command) (CommandResult, error) {
		if strings.Contains(cmd.Script, "flaky") {
			return CommandResult{ExitCode: 1}, nil
		}
		return CommandResult{}, nil
	}}
	plan := planFor(t, Request{Repo: "o/r", Ref: "main", Event: EventPush}, map[string]string{
		".grain/workflows/a.yml": "on: push\njobs:\n  one:\n    steps:\n      - run: flaky\n        continue-on-error: true\n      - run: after\n",
	})

	run := (&Engine{Sandboxes: sandboxes}).Run(context.Background(), "r1", plan, nil)
	if run.Conclusion != ConclusionSuccess {
		t.Fatalf("a continue-on-error failure does not fail the job: got %q", run.Conclusion)
	}
	steps := jobByID(t, run, "one").Steps
	if steps[0].Conclusion != ConclusionFailure || steps[1].Conclusion != ConclusionSuccess {
		t.Fatalf("step conclusions: got %+v", steps)
	}
}

func TestEngineWaitsForNeedsAndSkipsWhatCannotRun(t *testing.T) {
	sandboxes := &fakeSandboxes{script: func(cmd Command) (CommandResult, error) {
		if strings.Contains(cmd.Script, "fail") {
			return CommandResult{ExitCode: 1}, nil
		}
		return CommandResult{}, nil
	}}
	plan := planFor(t, Request{Repo: "o/r", Ref: "main", Event: EventPush}, map[string]string{
		".grain/workflows/a.yml": "on: push\njobs:\n" +
			"  build:\n    steps:\n      - run: fail\n" +
			"  test:\n    needs: [build]\n    steps:\n      - run: after-build\n" +
			"  deploy:\n    needs: [test]\n    steps:\n      - run: after-test\n" +
			"  lint:\n    steps:\n      - run: independent\n",
	})

	run := (&Engine{Sandboxes: sandboxes}).Run(context.Background(), "r1", plan, nil)
	if run.Conclusion != ConclusionFailure {
		t.Fatalf("conclusion: got %q", run.Conclusion)
	}
	if got := jobByID(t, run, "test").Conclusion; got != ConclusionSkipped {
		t.Errorf("a job whose need failed is skipped: got %q", got)
	}
	if got := jobByID(t, run, "deploy").Conclusion; got != ConclusionSkipped {
		t.Errorf("a job whose need was skipped is skipped too: got %q", got)
	}
	if got := jobByID(t, run, "lint").Conclusion; got != ConclusionSuccess {
		t.Errorf("a job that needs nothing runs anyway: got %q", got)
	}
	for _, cmd := range sandboxes.commands {
		if strings.HasPrefix(cmd.Script, "after-") {
			t.Errorf("nothing downstream of a failure should have run: %q", cmd.Script)
		}
	}
}

func TestEngineDefaultsToNoPerRunConcurrencyLimit(t *testing.T) {
	sandboxes := &fakeSandboxes{block: make(chan struct{}), acquires: make(chan SandboxRequest, 6)}
	plan := planFor(t, Request{Repo: "o/r", Ref: "main", Event: EventPush}, map[string]string{
		".grain/workflows/a.yml": "on: push\njobs:\n" +
			"  a:\n    steps:\n      - run: a\n" +
			"  b:\n    steps:\n      - run: b\n" +
			"  c:\n    steps:\n      - run: c\n" +
			"  d:\n    steps:\n      - run: d\n" +
			"  e:\n    steps:\n      - run: e\n" +
			"  f:\n    steps:\n      - run: f\n",
	})

	// Neither Concurrency nor ConcurrencyLimit is set: all six ready jobs
	// proceed directly to Engine.Jobs without a per-run cap of 4.
	engine := &Engine{
		Sandboxes: sandboxes,
		Jobs:      &SharedGate{Limit: func() int { return 6 }},
	}
	var sawWaitForRun bool
	done := make(chan Run, 1)
	go func() {
		done <- engine.Run(context.Background(), "r1", plan, func(job JobRun) {
			if strings.Contains(job.Activity, "this run may hold at once") {
				sawWaitForRun = true
			}
		})
	}()

	for i := 0; i < 6; i++ {
		awaitSignal(t, sandboxes.acquires, fmt.Sprintf("the engine never started job %d/6 concurrently", i+1))
	}
	close(sandboxes.block)

	run := <-done
	if run.Conclusion != ConclusionSuccess {
		t.Fatalf("conclusion: got %q (%s)", run.Conclusion, run.Error)
	}
	if sandboxes.peakLive != 6 {
		t.Errorf("with no per-run limit, all 6 jobs should run concurrently: peak %d", sandboxes.peakLive)
	}
	if sawWaitForRun {
		t.Error("a run with no per-run limit reported WaitForRun activity")
	}
	for _, job := range run.Jobs {
		if job.QueuedCause == WaitForRun {
			t.Errorf("job %s recorded QueuedCause = %q, want no per-run queueing", job.JobID, job.QueuedCause)
		}
	}
}

func TestEngineRunsIndependentJobsAtOnceUpToItsLimit(t *testing.T) {
	sandboxes := &fakeSandboxes{block: make(chan struct{}), acquires: make(chan SandboxRequest, 4)}
	plan := planFor(t, Request{Repo: "o/r", Ref: "main", Event: EventPush}, map[string]string{
		".grain/workflows/a.yml": "on: push\njobs:\n" +
			"  a:\n    steps:\n      - run: a\n" +
			"  b:\n    steps:\n      - run: b\n" +
			"  c:\n    steps:\n      - run: c\n" +
			"  d:\n    steps:\n      - run: d\n",
	})

	engine := &Engine{Sandboxes: sandboxes, Concurrency: 2}
	waitForRun := make(chan JobRun, 4)
	done := make(chan Run, 1)
	go func() {
		done <- engine.Run(context.Background(), "r1", plan, func(job JobRun) {
			if strings.Contains(job.Activity, "this run may hold at once") {
				signal(waitForRun, job)
			}
		})
	}()

	// Wait for the engine to have as many jobs in flight as it is
	// allowed, then let them all go. Nothing releases a sandbox until
	// block is closed, so two acquisitions are two jobs running at once
	// however long the machine took over them.
	awaitSignal(t, sandboxes.acquires, "the engine never started a first job")
	awaitSignal(t, sandboxes.acquires, "the engine never got two jobs running at once")
	awaitSignal(t, waitForRun, "the per-run gate never reported WaitForRun on the waiting jobs")
	close(sandboxes.block)

	run := <-done
	if run.Conclusion != ConclusionSuccess {
		t.Fatalf("conclusion: got %q", run.Conclusion)
	}
	if sandboxes.peakLive > 2 {
		t.Errorf("Concurrency 2 should never have three sandboxes alive: peak %d", sandboxes.peakLive)
	}
	if sandboxes.sandboxes != 4 {
		t.Errorf("one sandbox per job: got %d", sandboxes.sandboxes)
	}
}

// TestEngineErrsRatherThanFailsAJobWhoseSandboxCannotBeBuilt is the
// distinction ConclusionErrored exists for: a job that never got a
// machine has said nothing about the commit, and reporting it as a
// failure is what put a red verdict on pull requests whose code had
// never been compiled (grain/task-361).
func TestEngineErrsRatherThanFailsAJobWhoseSandboxCannotBeBuilt(t *testing.T) {
	sandboxes := &fakeSandboxes{acquireErr: errors.New("no capacity")}
	plan := planFor(t, Request{Repo: "o/r", Ref: "main", Event: EventPush}, map[string]string{
		".grain/workflows/a.yml": "on: push\njobs:\n  one:\n    steps:\n      - run: true\n",
	})

	run := (&Engine{Sandboxes: sandboxes}).Run(context.Background(), "r1", plan, nil)
	job := jobByID(t, run, "one")
	if job.Conclusion != ConclusionErrored || !strings.Contains(job.Error, "no capacity") {
		t.Fatalf("job: got %q / %q", job.Conclusion, job.Error)
	}
	if job.Steps[0].Conclusion != ConclusionSkipped {
		t.Errorf("a job with no sandbox ran no steps: got %+v", job.Steps)
	}
	if run.Conclusion != ConclusionErrored {
		t.Errorf("a run whose only job never ran is errored, not failed: got %q", run.Conclusion)
	}
	if Cacheable(job) {
		t.Errorf("a job that never got a machine must not be cached: %+v", job)
	}
}

// TestEngineConcludesFailureWhenAnyJobActuallyFailed keeps "errored"
// from swallowing a real failure: one job tested the tree and did not
// pass, so there is something wrong with the commit and the run has to
// say so, whatever happened to the job beside it.
func TestEngineConcludesFailureWhenAnyJobActuallyFailed(t *testing.T) {
	if got := concludeRun([]JobRun{
		{Conclusion: ConclusionErrored},
		{Conclusion: ConclusionFailure},
	}); got != ConclusionFailure {
		t.Errorf("errored beside a failure: got %q, want failure", got)
	}
	if got := concludeRun([]JobRun{
		{Conclusion: ConclusionErrored},
		{Conclusion: ConclusionSuccess},
	}); got != ConclusionErrored {
		t.Errorf("errored beside a success: got %q, want errored", got)
	}
	if got := concludeRun([]JobRun{
		{Conclusion: ConclusionErrored},
		{Conclusion: ConclusionCancelled},
	}); got != ConclusionCancelled {
		t.Errorf("errored beside a cancellation: got %q, want cancelled", got)
	}
}

func TestEngineWithNoBackendSaysSoRatherThanPassing(t *testing.T) {
	plan := planFor(t, Request{Repo: "o/r", Ref: "main", Event: EventPush}, map[string]string{
		".grain/workflows/a.yml": "on: push\njobs:\n  one:\n    steps:\n      - run: true\n",
	})
	run := (&Engine{}).Run(context.Background(), "r1", plan, nil)
	// Errored rather than failure for the reason above: a deployment
	// with nowhere to run a job has not found anything wrong with this
	// commit.
	if run.Conclusion != ConclusionErrored || run.Error == "" {
		t.Fatalf("got %q / %q", run.Conclusion, run.Error)
	}
}

// A run that matched no workflow is not a pass. It ran nothing of this
// tree, so it says errored and carries the plan's own explanation --
// the fix to a green tick that anything reading a conclusion alone, the
// merge queue included, could not tell from a suite that ran
// (grain/task-254).
func TestEngineDoesNotPassARunThatMatchedNoWorkflow(t *testing.T) {
	plan := planFor(t, Request{Repo: "o/r", Ref: "topic", Event: EventPush}, map[string]string{
		".grain/workflows/a.yml": "on:\n  push:\n    branches: [main]\njobs:\n  one:\n    steps:\n      - run: true\n",
	})
	run := (&Engine{Sandboxes: &fakeSandboxes{}}).Run(context.Background(), "r1", plan, nil)
	if run.Conclusion != ConclusionErrored {
		t.Fatalf("conclusion = %q, want errored", run.Conclusion)
	}
	if run.Error != plan.Unmatched || run.Error == "" {
		t.Fatalf("error = %q, want the plan's own reason %q", run.Error, plan.Unmatched)
	}
	if run.FinishedAt == nil {
		t.Error("a run that concluded has a finish time")
	}

	// And the empty plan that is nobody's problem still passes: a
	// repository that declares no workflows at all.
	plan = planFor(t, Request{Repo: "o/r", Ref: "topic", Event: EventPush}, nil)
	run = (&Engine{Sandboxes: &fakeSandboxes{}}).Run(context.Background(), "r2", plan, nil)
	if run.Conclusion != ConclusionSuccess || run.Error != "" {
		t.Fatalf("a repository declaring nothing = %q / %q, want a quiet success", run.Conclusion, run.Error)
	}
}

// A release of a repository with no `release` workflow published
// nothing and broke nothing, so it is skipped rather than errored -- the
// red run every merge used to leave in the CI list -- and still carries
// the explanation NoWorkflowAnswered recognises. A release that asked
// for jobs by name stays errored: it wanted work and got none.
func TestEngineSkipsAReleaseNoWorkflowAnswers(t *testing.T) {
	files := map[string]string{
		".grain/workflows/v3.yml": "on: [push, pull_request, workflow_dispatch]\njobs:\n  one:\n    steps:\n      - run: true\n",
	}
	release := Request{Repo: "o/r", Ref: "main", Commit: "abc", Event: EventRelease, TrustedRelease: true}
	run := (&Engine{Sandboxes: &fakeSandboxes{}}).Run(context.Background(), "r1", planFor(t, release, files), nil)
	if run.Conclusion != ConclusionSkipped {
		t.Fatalf("conclusion = %q, want skipped", run.Conclusion)
	}
	if !NoWorkflowAnswered(run) {
		t.Fatalf("a skipped unanswered release is still recognised: %q", run.Error)
	}

	release.Jobs = []string{"publish"}
	run = (&Engine{Sandboxes: &fakeSandboxes{}}).Run(context.Background(), "r2", planFor(t, release, files), nil)
	if run.Conclusion != ConclusionErrored {
		t.Fatalf("a release that named jobs = %q, want errored", run.Conclusion)
	}

	// A run stored errored before releases were skipped reads the same.
	stored := Run{Conclusion: ConclusionErrored, Error: NoWorkflowAnswersPrefix + "o/r answers release of branch main"}
	if !NoWorkflowAnswered(stored) {
		t.Error("an errored unanswered release read back from a store is still recognised")
	}
	stored.Conclusion = ConclusionFailure
	if NoWorkflowAnswered(stored) {
		t.Error("a failed run is never an unanswered one")
	}
}

func TestEngineCancelsRatherThanFailsWhenTheContextEnds(t *testing.T) {
	sandboxes := &fakeSandboxes{block: make(chan struct{}), execs: make(chan Command, 1)}
	plan := planFor(t, Request{Repo: "o/r", Ref: "main", Event: EventPush}, map[string]string{
		".grain/workflows/a.yml": "on: push\njobs:\n  one:\n    steps:\n      - run: forever\n",
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan Run, 1)
	go func() { done <- (&Engine{Sandboxes: sandboxes}).Run(ctx, "r1", plan, nil) }()
	// Let the step start before pulling the rug.
	awaitSignal(t, sandboxes.execs, "the step never started, so there was nothing to cancel")
	cancel()

	run := awaitSignal(t, done, "the cancelled run never returned")
	if run.Conclusion != ConclusionCancelled {
		t.Fatalf("conclusion: got %q", run.Conclusion)
	}
	if len(sandboxes.released) != 1 {
		t.Errorf("a cancelled job still releases its sandbox: %v", sandboxes.released)
	}
}

func TestEngineLayersEnvironmentTheWayGitHubDoes(t *testing.T) {
	var seen Command
	sandboxes := &fakeSandboxes{script: func(cmd Command) (CommandResult, error) {
		seen = cmd
		return CommandResult{}, nil
	}}
	plan := planFor(t, Request{Repo: "o/r", Ref: "main", Commit: "abc", Event: EventPush}, map[string]string{
		".grain/workflows/a.yml": "on: push\nenv:\n  LAYER: workflow\n  FROM_WORKFLOW: yes\njobs:\n  one:\n    env:\n      LAYER: job\n      FROM_JOB: yes\n    steps:\n      - run: true\n        env:\n          LAYER: step\n",
	})

	if run := (&Engine{Sandboxes: sandboxes}).Run(context.Background(), "r1", plan, nil); run.Conclusion != ConclusionSuccess {
		t.Fatalf("conclusion: got %q", run.Conclusion)
	}
	for key, want := range map[string]string{
		"LAYER":           "step",
		"FROM_WORKFLOW":   "yes",
		"FROM_JOB":        "yes",
		"CI":              "true",
		"GRAIN_CI_REPO":   "o/r",
		"GRAIN_CI_COMMIT": "abc",
		"GRAIN_CI_JOB":    "one",
	} {
		if got := seen.Env[key]; got != want {
			t.Errorf("env %s: got %q, want %q", key, got, want)
		}
	}
}

// cannedCache is a Cache a test drives: what it holds, what it was
// asked for, and what it was told.
type cannedCache struct {
	mu       sync.Mutex
	entries  map[Key]JobRun
	saved    map[Key]JobRun
	lookedUp []Key
	failLook bool
}

func newCannedCache() *cannedCache {
	return &cannedCache{entries: map[Key]JobRun{}, saved: map[Key]JobRun{}}
}

func (c *cannedCache) Lookup(_ context.Context, key Key) (JobRun, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lookedUp = append(c.lookedUp, key)
	if c.failLook {
		return JobRun{}, false, errors.New("cache is broken")
	}
	hit, ok := c.entries[key]
	return hit, ok, nil
}

func (c *cannedCache) Save(_ context.Context, key Key, result JobRun) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.saved[key] = result
	return nil
}

func TestEngineServesAJobFromCacheWithoutASandbox(t *testing.T) {
	sandboxes := &fakeSandboxes{}
	cache := newCannedCache()
	plan := planFor(t, Request{Repo: "o/r", Ref: "main", Commit: "abc", Event: EventPush}, map[string]string{
		".grain/workflows/a.yml": "on: push\njobs:\n  one:\n    steps:\n      - run: true\n",
	})
	cache.entries[plan.Jobs[0].Key] = JobRun{Conclusion: ConclusionSuccess, Steps: []StepRun{{Name: "true", Conclusion: ConclusionSuccess}}}

	run := (&Engine{Sandboxes: sandboxes, Cache: cache}).Run(context.Background(), "r1", plan, nil)
	if run.Conclusion != ConclusionSuccess {
		t.Fatalf("conclusion: got %q", run.Conclusion)
	}
	job := jobByID(t, run, "one")
	if !job.Cached {
		t.Error("a job served from cache says so")
	}
	if sandboxes.sandboxes != 0 {
		t.Errorf("a cache hit builds no sandbox: built %d", sandboxes.sandboxes)
	}
}

func TestEngineAlwaysRunsAJobThatOptsOutOfCache(t *testing.T) {
	sandboxes := &fakeSandboxes{}
	cache := newCannedCache()
	plan := planFor(t, Request{Repo: "o/r", Ref: "main", Commit: "abc", Event: EventPush}, map[string]string{
		".grain/workflows/release.yml": "on: push\njobs:\n  publish:\n    cache: false\n    steps:\n      - run: ./publish\n",
	})
	cache.entries[plan.Jobs[0].Key] = JobRun{Conclusion: ConclusionSuccess}

	run := (&Engine{Sandboxes: sandboxes, Cache: cache}).Run(context.Background(), "r1", plan, nil)
	if run.Conclusion != ConclusionSuccess || sandboxes.sandboxes != 1 {
		t.Fatalf("uncached release job = %q with %d sandboxes, want a successful execution", run.Conclusion, sandboxes.sandboxes)
	}
	if len(cache.saved) != 0 {
		t.Fatalf("uncached release job wrote cache entries: %v", cache.saved)
	}
	if len(cache.lookedUp) != 0 {
		t.Fatalf("uncached release job consulted cache entries: %v", cache.lookedUp)
	}
}

// A recheck is a request for the work rather than for the answer: the
// caller already has an answer and does not trust it (Request.Recheck,
// and orchestrator's confirmation of a red base branch). A cached
// verdict served here would agree with the run being checked every
// single time, which is exactly the confirmation that confirms nothing.
func TestEngineRunsARecheckedJobWithoutReadingTheCache(t *testing.T) {
	sandboxes := &fakeSandboxes{}
	cache := newCannedCache()
	plan := planFor(t, Request{Repo: "o/r", Ref: "main", Commit: "abc", Event: EventPush, Recheck: true},
		map[string]string{
			".grain/workflows/a.yml": "on: push\njobs:\n  one:\n    steps:\n      - run: true\n",
		})
	cache.entries[plan.Jobs[0].Key] = JobRun{Conclusion: ConclusionFailure,
		Steps: []StepRun{{Name: "true", Conclusion: ConclusionFailure, ExitCode: 1}}}

	run := (&Engine{Sandboxes: sandboxes, Cache: cache}).Run(context.Background(), "r1", plan, nil)
	if run.Conclusion != ConclusionSuccess {
		t.Fatalf("a recheck was answered %q, want the verdict of the job it actually ran", run.Conclusion)
	}
	if sandboxes.sandboxes != 1 {
		t.Fatalf("a recheck built %d sandboxes, want one: it is the work that is being asked for",
			sandboxes.sandboxes)
	}
	if len(cache.lookedUp) != 0 {
		t.Fatalf("a recheck consulted the cache: %v", cache.lookedUp)
	}
	// And the finished run still says it was one: Service.Start replaces
	// the record with this run when it concludes, and wait_for_checks'
	// note on a full-suite wait handed a recheck reads it (grain/task-2388).
	if !run.Recheck {
		t.Error("the finished recheck came back with Recheck unset")
	}
}

// What a recheck learns replaces what it re-ran. The case it exists for
// is a cached red that turns out to be a flake: if the passing recheck
// were not written back, every later ordinary run of the same tree --
// wait_for_checks, the merge-queue gate -- would keep replaying the red
// until the entry aged out.
func TestARecheckThatPassesReplacesTheCachedFailure(t *testing.T) {
	cache := newCannedCache()
	files := map[string]string{
		".grain/workflows/a.yml": "on: push\njobs:\n  one:\n    steps:\n      - run: true\n",
	}
	recheck := planFor(t, Request{Repo: "o/r", Ref: "main", Commit: "abc", Event: EventPush, Recheck: true}, files)
	key := recheck.Jobs[0].Key
	cache.entries[key] = JobRun{Conclusion: ConclusionFailure,
		Steps: []StepRun{{Name: "true", Conclusion: ConclusionFailure, ExitCode: 1}}}

	(&Engine{Sandboxes: &fakeSandboxes{}, Cache: cache}).Run(context.Background(), "recheck", recheck, nil)
	if saved, ok := cache.saved[key]; !ok || saved.Conclusion != ConclusionSuccess {
		t.Fatalf("a passing recheck saved %+v (saved=%v), want its success written over the cached failure", saved, ok)
	}

	// And the next ordinary run of the same tree reads the recheck's
	// verdict rather than the failure, without needing a sandbox.
	plan := planFor(t, Request{Repo: "o/r", Ref: "main", Commit: "abc", Event: EventPush}, files)
	if plan.Jobs[0].Key != key {
		t.Fatalf("a recheck planned key %s for the job an ordinary run keys %s; they must be one entry", key, plan.Jobs[0].Key)
	}
	cache.entries[key] = cache.saved[key]
	sandboxes := &fakeSandboxes{}
	run := (&Engine{Sandboxes: sandboxes, Cache: cache}).Run(context.Background(), "after", plan, nil)
	if run.Conclusion != ConclusionSuccess || sandboxes.sandboxes != 0 {
		t.Fatalf("the run after a passing recheck was %q with %d sandboxes, want the recheck's success from the cache",
			run.Conclusion, sandboxes.sandboxes)
	}
}

// A recheck of a named job reruns that job and reuses its dependency's
// cached pass (Request.Recheck, grain/task-2124): the dependency is
// there for the named job to start, not because it is in doubt. A
// dependency cached as anything but a pass runs, since the named job
// would otherwise be skipped behind it and the recheck learn nothing.
func TestARecheckReusesADependencysCachedPassAndRerunsTheNamedJob(t *testing.T) {
	files := map[string]string{
		".grain/workflows/a.yml": "on: push\njobs:\n" +
			"  build:\n    steps:\n      - run: true\n" +
			"  e2e:\n    needs: [build]\n    steps:\n      - run: true\n",
	}
	for _, tc := range []struct {
		name      string
		cached    Conclusion
		sandboxes int
	}{
		{name: "cached pass reused", cached: ConclusionSuccess, sandboxes: 1},
		{name: "cached failure rerun", cached: ConclusionFailure, sandboxes: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache := newCannedCache()
			plan := planFor(t, Request{Repo: "o/r", Ref: "main", Commit: "abc", Event: EventPush,
				Jobs: []string{"e2e"}, Recheck: true}, files)
			for _, pj := range plan.Jobs {
				conclusion := ConclusionFailure // the red the recheck doubts
				if pj.Job.ID == "build" {
					if !pj.DependencyOnly {
						t.Fatal("build is only a dependency of the named job")
					}
					conclusion = tc.cached
				}
				cache.entries[pj.Key] = JobRun{Conclusion: conclusion}
			}
			sandboxes := &fakeSandboxes{}
			run := (&Engine{Sandboxes: sandboxes, Cache: cache}).Run(context.Background(), "r1", plan, nil)
			if run.Conclusion != ConclusionSuccess || sandboxes.sandboxes != tc.sandboxes {
				t.Fatalf("recheck = %q with %d sandboxes, want success with %d", run.Conclusion, sandboxes.sandboxes, tc.sandboxes)
			}
			if e2e := jobByID(t, run, "e2e"); e2e.Cached {
				t.Error("the named job was served from the cache; a recheck reruns it")
			}
			if build := jobByID(t, run, "build"); build.Cached != (tc.cached == ConclusionSuccess) {
				t.Errorf("dependency cached=%v, want %v", build.Cached, tc.cached == ConclusionSuccess)
			}
		})
	}
}

func TestEngineSavesWhatARunLearnedAndNothingElse(t *testing.T) {
	sandboxes := &fakeSandboxes{script: func(cmd Command) (CommandResult, error) {
		if strings.Contains(cmd.Script, "fail") {
			return CommandResult{ExitCode: 1}, nil
		}
		return CommandResult{}, nil
	}}
	cache := newCannedCache()
	plan := planFor(t, Request{Repo: "o/r", Ref: "main", Commit: "abc", Event: EventPush}, map[string]string{
		".grain/workflows/a.yml": "on: push\njobs:\n" +
			"  ok:\n    steps:\n      - run: true\n" +
			"  bad:\n    steps:\n      - run: fail\n" +
			"  after:\n    needs: [bad]\n    steps:\n      - run: true\n",
	})

	(&Engine{Sandboxes: sandboxes, Cache: cache}).Run(context.Background(), "r1", plan, nil)
	byJob := map[string]Conclusion{}
	for _, pj := range plan.Jobs {
		if saved, ok := cache.saved[pj.Key]; ok {
			byJob[pj.Job.ID] = saved.Conclusion
		}
	}
	if byJob["ok"] != ConclusionSuccess {
		t.Errorf("a successful job is cached: got %+v", byJob)
	}
	if byJob["bad"] != ConclusionFailure {
		t.Errorf("a job that ran and failed is cached: got %+v", byJob)
	}
	if _, ok := byJob["after"]; ok {
		t.Error("a skipped job is a fact about the run, not about the job -- it must not be cached")
	}
}

func TestEngineRunsTheJobWhenTheCacheIsBroken(t *testing.T) {
	sandboxes := &fakeSandboxes{}
	cache := newCannedCache()
	cache.failLook = true
	plan := planFor(t, Request{Repo: "o/r", Ref: "main", Commit: "abc", Event: EventPush}, map[string]string{
		".grain/workflows/a.yml": "on: push\njobs:\n  one:\n    steps:\n      - run: true\n",
	})

	run := (&Engine{Sandboxes: sandboxes, Cache: cache}).Run(context.Background(), "r1", plan, nil)
	if run.Conclusion != ConclusionSuccess || sandboxes.sandboxes != 1 {
		t.Fatalf("a broken cache must not fail a build: %q, %d sandboxes", run.Conclusion, sandboxes.sandboxes)
	}
}

func TestEngineReportsEachJobAndStepStartingAndFinishing(t *testing.T) {
	sandboxes := &fakeSandboxes{}
	plan := planFor(t, Request{Repo: "o/r", Ref: "main", Event: EventPush}, map[string]string{
		".grain/workflows/a.yml": "on: push\njobs:\n  a:\n    steps:\n      - run: true\n  b:\n    needs: [a]\n    steps:\n      - run: true\n",
	})

	var observed []string
	var runningStep, finishedStep bool
	run := (&Engine{Sandboxes: sandboxes}).Run(context.Background(), "r1", plan, func(job JobRun) {
		step := "none"
		if len(job.Steps) > 0 {
			step = string(job.Steps[len(job.Steps)-1].Conclusion)
			if step == "" && job.Steps[len(job.Steps)-1].StartedAt != nil {
				runningStep = true
			}
			if step == string(ConclusionSuccess) && job.Steps[len(job.Steps)-1].FinishedAt != nil {
				finishedStep = true
			}
		}
		observed = append(observed, fmt.Sprintf("%s=%s/%s", job.JobID, job.Conclusion, step))
		if !job.Conclusion.Done() {
			// A job in flight is reported so that whatever is watching
			// can say which sandbox is being spent on it -- so a report
			// with no sandbox on it would be no use to anybody.
			if job.Sandbox == "" || job.StartedAt == nil {
				t.Errorf("a job reported in flight carries its sandbox and its start: got %+v", job)
			}
		}
	})
	if run.Conclusion != ConclusionSuccess {
		t.Fatalf("conclusion: got %q", run.Conclusion)
	}
	// Four snapshots per job, in the order the run went: on its machine,
	// step started, step finished, then the job finished. b needs a, so
	// nothing here is interleaved.
	want := "a=/none,a=/,a=/success,a=success/success," +
		"b=/none,b=/,b=/success,b=success/success"
	if strings.Join(observed, ",") != want {
		t.Errorf("observer should see each job and step start and finish: got %v", observed)
	}
	if !runningStep || !finishedStep {
		t.Errorf("observer did not see both live and finished step timestamps: running=%v finished=%v", runningStep, finishedStep)
	}
}

func TestEngineReportsNoSandboxForAJobThatNeverGetsOne(t *testing.T) {
	// The two ways a job is decided without a machine: its needs did not
	// succeed (skipped), and the cache already knew the answer. Neither
	// has a sandbox to spend, so neither is ever reported as being in
	// flight -- a pane listing what CI is running now would otherwise
	// show a row for work nothing is doing.
	cache := newCannedCache()
	sandboxes := &fakeSandboxes{script: func(cmd Command) (CommandResult, error) {
		if strings.Contains(cmd.Script, "false") {
			return CommandResult{ExitCode: 1}, nil
		}
		return CommandResult{}, nil
	}}
	plan := planFor(t, Request{Repo: "o/r", Ref: "main", Commit: "abc", Event: EventPush}, map[string]string{
		".grain/workflows/a.yml": "on: push\njobs:\n" +
			"  cached:\n    steps:\n      - run: true\n" +
			"  bad:\n    steps:\n      - run: false\n" +
			"  after:\n    needs: [bad]\n    steps:\n      - run: true\n",
	})
	for _, pj := range plan.Jobs {
		if pj.Job.ID == "cached" {
			cache.entries[pj.Key] = JobRun{Conclusion: ConclusionSuccess, Sandbox: "c-older"}
		}
	}

	inFlight := map[string]string{}
	(&Engine{Sandboxes: sandboxes, Cache: cache}).Run(context.Background(), "r1", plan, func(job JobRun) {
		if !job.Conclusion.Done() {
			inFlight[job.JobID] = job.Sandbox
		}
	})
	if _, ok := inFlight["cached"]; ok {
		t.Errorf("a cached job gets no machine, so it is never in flight: got %v", inFlight)
	}
	if _, ok := inFlight["after"]; ok {
		t.Errorf("a skipped job gets no machine, so it is never in flight: got %v", inFlight)
	}
	if inFlight["bad"] == "" {
		t.Errorf("a job that ran is reported on its machine first: got %v", inFlight)
	}
}

func TestSandboxNameIsUsableAsAHostname(t *testing.T) {
	plan := Plan{Jobs: []PlannedJob{{ID: "a", WorkflowPath: ".grain/workflows/Tests Suite.yml", Job: Job{ID: "go_test"}}}}
	name := sandboxName("9f2c1a3b8e4d5f60", plan, plan.Jobs[0])
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			t.Fatalf("sandbox name %q holds %q", name, r)
		}
	}
	if strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") || strings.Contains(name, "--") {
		t.Errorf("sandbox name %q is not tidy", name)
	}
	if !strings.HasPrefix(name, "c9f2c1a") {
		t.Errorf("sandbox name %q should say which run it belongs to", name)
	}
}

// The name has nine bytes to live in (MaxSandboxName), and what has to
// survive that squeeze is uniqueness: two jobs of one run must never be
// handed the same machine, however long the run id or the workflow file
// happens to be.
func TestSandboxNamesFitTheBudgetAndStayDistinct(t *testing.T) {
	plan := Plan{}
	for i := 0; i < 40; i++ {
		plan.Jobs = append(plan.Jobs, PlannedJob{
			ID:           fmt.Sprintf(".grain/workflows/a-very-long-workflow-name.yml:job-%d", i),
			WorkflowPath: ".grain/workflows/a-very-long-workflow-name.yml",
			Job:          Job{ID: fmt.Sprintf("job-%d", i)},
		})
	}
	for _, runID := range []string{"9f2c1a3b8e4d5f60", "r1", "", "Run Id/With Junk"} {
		seen := map[string]string{}
		for _, pj := range plan.Jobs {
			name := sandboxName(runID, plan, pj)
			if len(name) > MaxSandboxName {
				t.Errorf("sandboxName(%q, %s) = %q, %d bytes over the %d-byte budget",
					runID, pj.ID, name, len(name)-MaxSandboxName, MaxSandboxName)
			}
			if other, dup := seen[name]; dup {
				t.Errorf("run %q: %s and %s are both called %q", runID, other, pj.ID, name)
			}
			seen[name] = pj.ID
		}
	}
}

// A job the plan does not hold is a caller's bug rather than a shape the
// engine produces, but it must not produce a name that overruns the
// budget on its way to being reported.
func TestSandboxNameOfAJobOutsideThePlan(t *testing.T) {
	name := sandboxName("9f2c1a3b8e4d5f60", Plan{}, PlannedJob{ID: "nowhere"})
	if len(name) > MaxSandboxName {
		t.Errorf("sandboxName() = %q, over the %d-byte budget", name, MaxSandboxName)
	}
}

func TestTruncateOutputKeepsBothEnds(t *testing.T) {
	body := strings.Repeat("a", MaxStepOutput/4) + strings.Repeat("b", MaxStepOutput) + strings.Repeat("c", 100)
	got := TruncateOutput(body)
	if len(got) > MaxStepOutput+512 {
		t.Fatalf("truncated output is still %d bytes", len(got))
	}
	if !strings.HasPrefix(got, "aaaa") || !strings.HasSuffix(got, "cccc") {
		t.Error("both ends of the output should survive")
	}
	if !strings.Contains(got, "elided from the middle by grain") {
		t.Error("truncation should say it happened")
	}
	if short := "small"; TruncateOutput(short) != short {
		t.Error("output under the cap is untouched")
	}
}

// What a step printed is cut twice before anybody reads it: to
// MaxStepOutput as the step ends, and to MaxStoredStepOutput on its way
// into the database. The second cut is the one a reader gets, so the
// number in it has to be about the step rather than about the first cut.
//
// The failure this is for: a guest build printed some 40MB and the run
// stored "192KB of this step's output elided", because 192KB is what the
// second cut took off the first one's 256KB. A reader who believes that
// number thinks they are looking at a log with a page missing, and goes
// hunting in it for a failure that is three orders of magnitude away.
func TestTruncatingTwiceCountsWhatBothCutsDropped(t *testing.T) {
	body := strings.Repeat("a", 16) + strings.Repeat("b", 4<<20) + strings.Repeat("c", 16)
	stored := TruncateOutputTo(TruncateOutput(body), MaxStoredStepOutput)

	if len(stored) > MaxStoredStepOutput+512 {
		t.Errorf("the stored output is %d bytes, over the %d it is cut to", len(stored), MaxStoredStepOutput)
	}
	if !strings.HasPrefix(stored, "aaaa") || !strings.HasSuffix(stored, "cccc") {
		t.Error("both ends of the step's output should still be there after two cuts")
	}
	// One note, not one per cut: each truncation takes the note it found
	// with it, which is what lets the count accumulate in one place.
	if got := strings.Count(stored, "elided from the middle by grain"); got != 1 {
		t.Errorf("the twice-cut output carries %d elision notes, want exactly 1", got)
	}

	kb := elidedKB(t, stored)
	if want := (len(body) - MaxStoredStepOutput) / 1024; kb < want-1 || kb > want+1 {
		t.Errorf("the note says %dKB was elided; the step printed %dKB and %dKB of it is here, so %dKB is gone",
			kb, len(body)/1024, MaxStoredStepOutput/1024, want)
	}
}

// elidedKB reads the number out of grain's own elision note.
func elidedKB(t *testing.T, s string) int {
	t.Helper()
	m := regexp.MustCompile(`\[grain ci] (\d+)KB of this step's output elided`).FindStringSubmatch(s)
	if m == nil {
		t.Fatalf("no elision note in %q", s[:min(len(s), 200)])
	}
	kb, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatal(err)
	}
	return kb
}
