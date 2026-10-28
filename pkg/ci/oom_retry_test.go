package ci

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeOOMSandboxes hands out sandboxes whose every step fails with a
// SIGKILL's exit code where oom says that sandbox is OOM-killed, and
// passes otherwise.
type fakeOOMSandboxes struct {
	mu       sync.Mutex
	acquired int
	oom      map[int]bool
}

func (f *fakeOOMSandboxes) Acquire(ctx context.Context, req SandboxRequest) (Sandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acquired++
	return &fakeOOMSandbox{name: fmt.Sprintf("sandbox-%d", f.acquired), killed: f.oom[f.acquired]}, nil
}

type fakeOOMSandbox struct {
	name   string
	killed bool
}

func (s *fakeOOMSandbox) Name() string { return s.name }
func (s *fakeOOMSandbox) Exec(ctx context.Context, cmd Command) (CommandResult, error) {
	if s.killed {
		return CommandResult{ExitCode: 137, Output: "Killed"}, nil
	}
	return CommandResult{ExitCode: 0, Output: "ok"}, nil
}
func (s *fakeOOMSandbox) Release(ctx context.Context) error { return nil }
func (s *fakeOOMSandbox) SandboxOOM(ctx context.Context) (string, bool) {
	if !s.killed {
		return "", false
	}
	return "the sandbox container was OOM-killed", true
}

const oomWorkflow = "on: push\njobs:\n  build:\n    steps:\n      - run: make\n"

// A job whose sandbox was OOM-killed is retried exactly once in a fresh
// sandbox -- though nothing it printed reads as transient -- and passing
// there is recorded as infrastructure, not as a flake. NoteOOM hears of
// the kill.
func TestOOMKilledJobIsRetriedOnceAsInfrastructure(t *testing.T) {
	sandboxes := &fakeOOMSandboxes{oom: map[int]bool{1: true}}
	var noted int
	engine := &Engine{Sandboxes: sandboxes, NoteOOM: func(time.Time) { noted++ }}
	run := engine.Run(context.Background(), "r1", testPlan(t, oomWorkflow), nil)

	job := jobByID(t, run, "build")
	if job.Conclusion != ConclusionSuccess {
		t.Fatalf("conclusion = %q, want success on the retry", job.Conclusion)
	}
	if sandboxes.acquired != 2 || !job.Retried || job.RetryCount != 1 {
		t.Errorf("acquired %d sandboxes, retried=%v count=%d; want exactly one retry", sandboxes.acquired, job.Retried, job.RetryCount)
	}
	if job.Flake {
		t.Error("an OOM kill was recorded as a flake")
	}
	if !strings.HasPrefix(job.RetryReason, OOMRetryReasonPrefix) || !strings.Contains(job.Warning, "infrastructure") {
		t.Errorf("retry reason %q, warning %q; want both to name the OOM kill as infrastructure", job.RetryReason, job.Warning)
	}
	if noted != 1 {
		t.Errorf("NoteOOM called %d times, want 1", noted)
	}
}

// Killed again on the retry: no third attempt, and the job is errored --
// the machine's answer -- rather than a failure of the tree.
func TestOOMKilledJobIsNotRetriedTwice(t *testing.T) {
	sandboxes := &fakeOOMSandboxes{oom: map[int]bool{1: true, 2: true}}
	var noted int
	engine := &Engine{Sandboxes: sandboxes, NoteOOM: func(time.Time) { noted++ }}
	run := engine.Run(context.Background(), "r1", testPlan(t, oomWorkflow), nil)

	job := jobByID(t, run, "build")
	if sandboxes.acquired != 2 {
		t.Errorf("acquired %d sandboxes, want 2 (one retry, no more)", sandboxes.acquired)
	}
	if job.Conclusion != ConclusionErrored || job.Flake {
		t.Errorf("conclusion = %q, flake = %v; want errored and no flake", job.Conclusion, job.Flake)
	}
	if !strings.Contains(job.Error, "OOM-killed") {
		t.Errorf("error = %q, want it to name the OOM kill", job.Error)
	}
	if noted != 2 {
		t.Errorf("NoteOOM called %d times, want once per kill (2)", noted)
	}
}

// A sandbox that was not OOM-killed fails a deterministic step exactly as
// before: no retry.
func TestFailedJobWithoutOOMIsNotRetried(t *testing.T) {
	sandboxes := &fakeCountingSandboxes{scriptFn: func(int, Command) (CommandResult, error) {
		return CommandResult{ExitCode: 137, Output: "Killed"}, nil
	}}
	engine := &Engine{Sandboxes: sandboxes, NoteOOM: func(time.Time) { t.Error("NoteOOM called without an OOM kill") }}
	run := engine.Run(context.Background(), "r1", testPlan(t, oomWorkflow), nil)
	if job := jobByID(t, run, "build"); job.Retried || sandboxes.acquireCount != 1 {
		t.Errorf("retried=%v, acquired %d; want no retry", job.Retried, sandboxes.acquireCount)
	}
}
