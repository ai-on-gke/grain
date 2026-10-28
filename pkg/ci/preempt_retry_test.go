// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ci

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// fakePreemptedSandboxes hands out sandboxes whose every step fails the
// way a step does when its pod is taken away mid-exec, where preempted
// says that sandbox was, and passes otherwise.
type fakePreemptedSandboxes struct {
	mu        sync.Mutex
	acquired  int
	preempted map[int]bool
}

func (f *fakePreemptedSandboxes) Acquire(ctx context.Context, req SandboxRequest) (Sandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acquired++
	return &fakePreemptedSandbox{name: fmt.Sprintf("sandbox-%d", f.acquired), preempted: f.preempted[f.acquired]}, nil
}

type fakePreemptedSandbox struct {
	name      string
	preempted bool
}

func (s *fakePreemptedSandbox) Name() string { return s.name }
func (s *fakePreemptedSandbox) Exec(ctx context.Context, cmd Command) (CommandResult, error) {
	if s.preempted {
		return CommandResult{ExitCode: 1, Output: "command terminated with exit code 1"}, nil
	}
	return CommandResult{ExitCode: 0, Output: "ok"}, nil
}
func (s *fakePreemptedSandbox) Release(ctx context.Context) error { return nil }
func (s *fakePreemptedSandbox) SandboxPreempted(ctx context.Context) (string, bool) {
	if !s.preempted {
		return "", false
	}
	return "sandbox pod " + s.name + " was disrupted (PreemptionByScheduler: preempting to accommodate a higher priority pod)", true
}

// A job whose pod the scheduler preempted -- for an interactive session's,
// on a full cluster -- is retried once in a fresh sandbox, though nothing
// it printed reads as transient, and passing there is infrastructure
// rather than a flake.
func TestPreemptedJobIsRetriedOnceAsInfrastructure(t *testing.T) {
	sandboxes := &fakePreemptedSandboxes{preempted: map[int]bool{1: true}}
	engine := &Engine{Sandboxes: sandboxes}
	run := engine.Run(context.Background(), "r1", testPlan(t, oomWorkflow), nil)

	job := jobByID(t, run, "build")
	if job.Conclusion != ConclusionSuccess {
		t.Fatalf("conclusion = %q, want success on the retry", job.Conclusion)
	}
	if sandboxes.acquired != 2 || !job.Retried || job.RetryCount != 1 {
		t.Errorf("acquired %d sandboxes, retried=%v count=%d; want exactly one retry", sandboxes.acquired, job.Retried, job.RetryCount)
	}
	if job.Flake {
		t.Error("a preemption was recorded as a flake")
	}
	if !strings.HasPrefix(job.RetryReason, PreemptedRetryReasonPrefix) || !strings.Contains(job.Warning, "infrastructure") {
		t.Errorf("retry reason %q, warning %q; want both to name the preemption as infrastructure", job.RetryReason, job.Warning)
	}
}

// Preempted again on the retry: errored -- which the CI layer re-runs --
// rather than a red verdict on the tree.
func TestPreemptedJobPreemptedAgainIsErroredNotFailed(t *testing.T) {
	sandboxes := &fakePreemptedSandboxes{preempted: map[int]bool{1: true, 2: true}}
	engine := &Engine{Sandboxes: sandboxes}
	run := engine.Run(context.Background(), "r1", testPlan(t, oomWorkflow), nil)

	job := jobByID(t, run, "build")
	if sandboxes.acquired != 2 {
		t.Errorf("acquired %d sandboxes, want 2 (one retry, no more)", sandboxes.acquired)
	}
	if job.Conclusion != ConclusionErrored || job.Flake {
		t.Errorf("conclusion = %q, flake = %v; want errored and no flake", job.Conclusion, job.Flake)
	}
	if !strings.HasPrefix(job.Error, PreemptedRetryReasonPrefix) || !strings.Contains(job.Error, "PreemptionByScheduler") {
		t.Errorf("error = %q, want it to name the preemption", job.Error)
	}
}
