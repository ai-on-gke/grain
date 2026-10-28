package ci

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// fakeCountingSandboxes tracks sandbox acquisitions and provides custom script execution behavior.
type fakeCountingSandboxes struct {
	mu           sync.Mutex
	acquireCount int
	released     []string
	scriptFn     func(sandboxIndex int, cmd Command) (CommandResult, error)
}

func (f *fakeCountingSandboxes) Acquire(ctx context.Context, req SandboxRequest) (Sandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acquireCount++
	idx := f.acquireCount
	name := fmt.Sprintf("sandbox-%d", idx)
	return &fakeTrackedSandbox{
		name:   name,
		parent: f,
		exec: func(cmd Command) (CommandResult, error) {
			if f.scriptFn != nil {
				return f.scriptFn(idx, cmd)
			}
			return CommandResult{ExitCode: 0}, nil
		},
	}, nil
}

type fakeTrackedSandbox struct {
	name   string
	parent *fakeCountingSandboxes
	exec   func(Command) (CommandResult, error)
}

func (s *fakeTrackedSandbox) Name() string { return s.name }
func (s *fakeTrackedSandbox) Exec(ctx context.Context, cmd Command) (CommandResult, error) {
	return s.exec(cmd)
}
func (s *fakeTrackedSandbox) Release(ctx context.Context) error {
	s.parent.mu.Lock()
	defer s.parent.mu.Unlock()
	s.parent.released = append(s.parent.released, s.name)
	return nil
}

func testPlan(t *testing.T, workflow string) Plan {
	t.Helper()
	return planFor(t, Request{Repo: "o/r", Ref: "grain/task-655", Commit: "c1", Event: EventPush},
		map[string]string{".grain/workflows/ci.yml": workflow})
}

// 1. Automatic retry on transient socket timeout passes and marks job as passed (retry) with flake warning.
func TestAutomaticRetryOnTransientSocketTimeoutPasses(t *testing.T) {
	sandboxes := &fakeCountingSandboxes{
		scriptFn: func(sandboxIndex int, cmd Command) (CommandResult, error) {
			if sandboxIndex == 1 {
				return CommandResult{ExitCode: 1, Output: "fatal: socket timeout while connecting to runner"}, nil
			}
			return CommandResult{ExitCode: 0, Output: "PASS: all tests passed"}, nil
		},
	}

	plan := testPlan(t, "on: push\njobs:\n  e2e:\n    steps:\n      - name: Run e2e\n        run: ./run-e2e.sh\n")
	engine := &Engine{Sandboxes: sandboxes}
	run := engine.Run(context.Background(), "r1", plan, nil)

	job := jobByID(t, run, "e2e")
	if job.Conclusion != ConclusionSuccess {
		t.Fatalf("job conclusion = %q, want %q", job.Conclusion, ConclusionSuccess)
	}
	if !job.Retried {
		t.Errorf("job.Retried = false, want true")
	}
	if job.RetryCount != 1 {
		t.Errorf("job.RetryCount = %d, want 1", job.RetryCount)
	}
	if !job.Flake {
		t.Errorf("job.Flake = false, want true")
	}
	if job.Status != "passed (retry)" {
		t.Errorf("job.Status = %q, want 'passed (retry)'", job.Status)
	}
	if !strings.Contains(job.Warning, "flaky") || !strings.Contains(job.Warning, "socket timeout") {
		t.Errorf("job.Warning = %q, want flake note with initial error", job.Warning)
	}
	if sandboxes.acquireCount != 2 {
		t.Errorf("sandboxes acquired = %d, want 2 (initial + retry)", sandboxes.acquireCount)
	}
	if len(sandboxes.released) != 2 {
		t.Errorf("sandboxes released = %d, want 2", len(sandboxes.released))
	}
}

// 2. Automatic retry on runner host resource contention passes.
func TestAutomaticRetryOnRunnerHostResourceContentionPasses(t *testing.T) {
	sandboxes := &fakeCountingSandboxes{
		scriptFn: func(sandboxIndex int, cmd Command) (CommandResult, error) {
			if sandboxIndex == 1 {
				return CommandResult{ExitCode: 1, Output: "error: runner host resource contention: temporary failure in name resolution"}, nil
			}
			return CommandResult{ExitCode: 0, Output: "ok"}, nil
		},
	}

	plan := testPlan(t, "on: push\njobs:\n  build:\n    steps:\n      - run: make build\n")
	engine := &Engine{Sandboxes: sandboxes}
	run := engine.Run(context.Background(), "r1", plan, nil)

	job := jobByID(t, run, "build")
	if job.Conclusion != ConclusionSuccess {
		t.Fatalf("job conclusion = %q, want success", job.Conclusion)
	}
	if !job.Retried || job.RetryCount != 1 || !job.Flake {
		t.Errorf("retry metadata: retried=%v, count=%d, flake=%v", job.Retried, job.RetryCount, job.Flake)
	}
	if job.Status != "passed (retry)" {
		t.Errorf("job status = %q, want 'passed (retry)'", job.Status)
	}
	if sandboxes.acquireCount != 2 {
		t.Errorf("sandboxes acquired = %d, want 2", sandboxes.acquireCount)
	}
}

// 3. Automatic retry on infrastructure step failure passes.
func TestAutomaticRetryOnInfrastructureStepFailurePasses(t *testing.T) {
	sandboxes := &fakeCountingSandboxes{
		scriptFn: func(sandboxIndex int, cmd Command) (CommandResult, error) {
			if sandboxIndex == 1 && strings.Contains(cmd.Script, "setup-env") {
				return CommandResult{ExitCode: 1, Output: "error downloading base image"}, nil
			}
			return CommandResult{ExitCode: 0, Output: "success"}, nil
		},
	}

	plan := testPlan(t, "on: push\njobs:\n  real-vm:\n    steps:\n"+
		"      - name: Setup\n        infrastructure: true\n        run: setup-env\n"+
		"      - name: Test\n        run: ./test.sh\n")
	engine := &Engine{Sandboxes: sandboxes}
	run := engine.Run(context.Background(), "r1", plan, nil)

	job := jobByID(t, run, "real-vm")
	if job.Conclusion != ConclusionSuccess {
		t.Fatalf("job conclusion = %q, want %q", job.Conclusion, ConclusionSuccess)
	}
	if !job.Retried || job.RetryCount != 1 || !job.Flake {
		t.Errorf("retry metadata: retried=%v, count=%d, flake=%v", job.Retried, job.RetryCount, job.Flake)
	}
	if sandboxes.acquireCount != 2 {
		t.Errorf("sandboxes acquired = %d, want 2", sandboxes.acquireCount)
	}
}

// 4. Retry failure with same error records definitive failure.
func TestRetryFailsWithSameErrorRecordsDefinitiveFailure(t *testing.T) {
	sandboxes := &fakeCountingSandboxes{
		scriptFn: func(sandboxIndex int, cmd Command) (CommandResult, error) {
			return CommandResult{ExitCode: 1, Output: "connection reset by peer"}, nil
		},
	}

	plan := testPlan(t, "on: push\njobs:\n  net:\n    steps:\n      - run: fetch-deps\n")
	engine := &Engine{Sandboxes: sandboxes}
	run := engine.Run(context.Background(), "r1", plan, nil)

	job := jobByID(t, run, "net")
	if job.Conclusion != ConclusionFailure {
		t.Fatalf("job conclusion = %q, want failure", job.Conclusion)
	}
	if !job.Retried || job.RetryCount != 1 {
		t.Errorf("job retried=%v, count=%d, want retried=true, count=1", job.Retried, job.RetryCount)
	}
	if job.Flake {
		t.Errorf("job.Flake = true, want false on persistent failure")
	}
	if sandboxes.acquireCount != 2 {
		t.Errorf("sandboxes acquired = %d, want 2", sandboxes.acquireCount)
	}
}

// 5. Retries are strictly bounded to 1 attempt per job.
func TestRetriesBoundedStrictlyToOneAttempt(t *testing.T) {
	sandboxes := &fakeCountingSandboxes{
		scriptFn: func(sandboxIndex int, cmd Command) (CommandResult, error) {
			return CommandResult{ExitCode: 1, Output: "socket timeout"}, nil
		},
	}

	plan := testPlan(t, "on: push\njobs:\n  tight:\n    steps:\n      - run: fail-always\n")
	engine := &Engine{Sandboxes: sandboxes}
	run := engine.Run(context.Background(), "r1", plan, nil)

	job := jobByID(t, run, "tight")
	if job.Conclusion != ConclusionFailure {
		t.Fatalf("job conclusion = %q, want failure", job.Conclusion)
	}
	if sandboxes.acquireCount > 2 {
		t.Fatalf("sandboxes acquired = %d, must be bounded strictly to 2 (1 attempt + 1 retry)", sandboxes.acquireCount)
	}
}

// 6. Designated retry policy on job retries non-infrastructure test failure.
func TestDesignatedRetryPolicyOnJob(t *testing.T) {
	sandboxes := &fakeCountingSandboxes{
		scriptFn: func(sandboxIndex int, cmd Command) (CommandResult, error) {
			if sandboxIndex == 1 {
				return CommandResult{ExitCode: 1, Output: "assertion failed: expected 5 got 4"}, nil
			}
			return CommandResult{ExitCode: 0, Output: "assertion passed"}, nil
		},
	}

	plan := testPlan(t, "on: push\njobs:\n  flaky-test:\n    retry: true\n    steps:\n      - run: go test ./...\n")
	engine := &Engine{Sandboxes: sandboxes}
	run := engine.Run(context.Background(), "r1", plan, nil)

	job := jobByID(t, run, "flaky-test")
	if job.Conclusion != ConclusionSuccess {
		t.Fatalf("job conclusion = %q, want success", job.Conclusion)
	}
	if !job.Retried || job.RetryCount != 1 || !job.Flake {
		t.Errorf("job retried=%v, count=%d, flake=%v", job.Retried, job.RetryCount, job.Flake)
	}
	if job.Status != "passed (retry)" {
		t.Errorf("job.Status = %q, want 'passed (retry)'", job.Status)
	}
	if sandboxes.acquireCount != 2 {
		t.Errorf("sandboxes acquired = %d, want 2", sandboxes.acquireCount)
	}
}

// 7. Deterministic failure without retry policy does NOT retry.
func TestDeterministicFailureWithoutRetryPolicyDoesNotRetry(t *testing.T) {
	sandboxes := &fakeCountingSandboxes{
		scriptFn: func(sandboxIndex int, cmd Command) (CommandResult, error) {
			return CommandResult{ExitCode: 1, Output: "syntax error on line 42"}, nil
		},
	}

	plan := testPlan(t, "on: push\njobs:\n  compile:\n    steps:\n      - run: make\n")
	engine := &Engine{Sandboxes: sandboxes}
	run := engine.Run(context.Background(), "r1", plan, nil)

	job := jobByID(t, run, "compile")
	if job.Conclusion != ConclusionFailure {
		t.Fatalf("job conclusion = %q, want failure", job.Conclusion)
	}
	if job.Retried || job.RetryCount != 0 {
		t.Errorf("deterministic failure without retry policy was retried: retried=%v, count=%d", job.Retried, job.RetryCount)
	}
	if sandboxes.acquireCount != 1 {
		t.Errorf("sandboxes acquired = %d, want 1 (no retry)", sandboxes.acquireCount)
	}
}

// 8. Cancelled context does NOT retry.
func TestCancelledContextDoesNotRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	sandboxes := &fakeCountingSandboxes{
		scriptFn: func(sandboxIndex int, cmd Command) (CommandResult, error) {
			cancel() // Cancel context during first attempt
			return CommandResult{ExitCode: 1, Output: "socket timeout"}, nil
		},
	}

	plan := testPlan(t, "on: push\njobs:\n  cancelling:\n    steps:\n      - run: long-run\n")
	engine := &Engine{Sandboxes: sandboxes}
	run := engine.Run(ctx, "r1", plan, nil)

	job := jobByID(t, run, "cancelling")
	if job.Conclusion != ConclusionCancelled {
		t.Fatalf("job conclusion = %q, want cancelled", job.Conclusion)
	}
	if job.Retried {
		t.Errorf("job.Retried = true on cancelled context, want false")
	}
	if sandboxes.acquireCount != 1 {
		t.Errorf("sandboxes acquired = %d, want 1", sandboxes.acquireCount)
	}
}

// A job being retried reads as running, not as the failure it is retrying
// from: the snapshot sent as the retry starts carries no conclusion, no
// finish time and the retry itself (grain/task-1345).
func TestAutomaticRetrySnapshotReadsAsRunning(t *testing.T) {
	sandboxes := &fakeCountingSandboxes{
		scriptFn: func(sandboxIndex int, cmd Command) (CommandResult, error) {
			if sandboxIndex == 1 {
				return CommandResult{ExitCode: 1, Output: "fatal: socket timeout while connecting to runner"}, nil
			}
			return CommandResult{ExitCode: 0, Output: "ok"}, nil
		},
	}

	plan := testPlan(t, "on: push\njobs:\n  go:\n    steps:\n      - run: go test ./...\n")
	engine := &Engine{Sandboxes: sandboxes}
	var mu sync.Mutex
	var snapshots []JobRun
	run := engine.Run(context.Background(), "r1", plan, func(job JobRun) {
		mu.Lock()
		defer mu.Unlock()
		snapshots = append(snapshots, job)
	})

	var retryStart *JobRun
	for i := range snapshots {
		if snapshots[i].Sandbox == "sandbox-2" {
			retryStart = &snapshots[i]
			break
		}
	}
	if retryStart == nil {
		t.Fatalf("no progress snapshot from the retry sandbox among %d", len(snapshots))
	}
	if retryStart.Conclusion != "" {
		t.Errorf("retry-start conclusion = %q, want none (still running)", retryStart.Conclusion)
	}
	if retryStart.FinishedAt != nil {
		t.Errorf("retry-start FinishedAt = %v, want nil", retryStart.FinishedAt)
	}
	if retryStart.Error != "" {
		t.Errorf("retry-start Error = %q, want empty", retryStart.Error)
	}
	if !retryStart.Retried || retryStart.RetryCount != 1 {
		t.Errorf("retry-start retried=%v count=%d, want true/1", retryStart.Retried, retryStart.RetryCount)
	}
	if !strings.Contains(retryStart.RetryReason, "socket timeout") {
		t.Errorf("retry-start RetryReason = %q, want the first attempt's failure", retryStart.RetryReason)
	}
	if len(retryStart.Steps) != 0 {
		t.Errorf("retry-start steps = %d, want the first attempt's cleared", len(retryStart.Steps))
	}

	job := jobByID(t, run, "go")
	if job.Conclusion != ConclusionSuccess || !job.Flake || job.FinishedAt == nil {
		t.Errorf("final job: conclusion=%q flake=%v finished=%v", job.Conclusion, job.Flake, job.FinishedAt)
	}
	if !strings.Contains(job.RetryReason, "socket timeout") {
		t.Errorf("final RetryReason = %q", job.RetryReason)
	}
}
