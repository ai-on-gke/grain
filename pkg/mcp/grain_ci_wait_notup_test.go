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

package mcp

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
)

// unavailable is a 503 as it reaches this package across the wire
// (ui.UnavailableError), without importing pkg/ui.
type unavailable struct{}

func (unavailable) Error() string     { return "grain server: this daemon's CI service is not up yet" }
func (unavailable) Unavailable() bool { return true }

// restartingRunner is a daemon that restarted under a wait: its CI is
// not up for the first startsDown starts and readsDown reads.
type restartingRunner struct {
	fakeGrainCIRunner
	mu         sync.Mutex
	startsDown int
	readsDown  int
	readErr    error
	run        ci.Run
}

func (r *restartingRunner) StartGrainCI(context.Context, []string) (ci.Run, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.starts++
	if r.starts <= r.startsDown {
		return ci.Run{}, ci.ErrNotUp
	}
	return ci.Run{ID: r.run.ID, Commit: r.run.Commit}, nil
}

func (r *restartingRunner) GrainCIRun(context.Context, string) (ci.Run, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reads++
	if r.reads <= r.readsDown {
		return ci.Run{}, r.readErr
	}
	return r.run, nil
}

// grain/task-1863: a wait_for_checks polling across a daemon restart was
// told the run had been forgotten, and one started during it was refused
// outright. Both are the daemon's CI not being up yet, which a wait sits
// out.
func TestWaitForChecksSitsOutCINotBeingUpYet(t *testing.T) {
	old := checkPollInterval
	checkPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { checkPollInterval = old })

	done := ci.Run{ID: "ci-1", Repo: testCIRepo, Ref: "grain/task-1", Commit: "abcdef012345",
		Conclusion: ci.ConclusionSuccess,
		Jobs:       []ci.JobRun{{JobID: "go", Name: "go", Conclusion: ci.ConclusionSuccess}}}
	for _, tc := range []struct {
		name   string
		runner *restartingRunner
	}{
		{"start refused", &restartingRunner{startsDown: 3, run: done}},
		{"reads refused in process", &restartingRunner{readsDown: 3, readErr: ci.ErrNotUp, run: done}},
		{"reads refused as 503", &restartingRunner{readsDown: 3, readErr: unavailable{}, run: done}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := ciTools(tc.runner)[0].Handler(context.Background(), map[string]any{})
			if result.IsError {
				t.Fatalf("wait gave up: %s", result.Text)
			}
			if !strings.Contains(result.Text, "success") {
				t.Fatalf("wait reported %q, want the run's success", result.Text)
			}
		})
	}
}

// What is not CI being down still ends the wait at once.
func TestWaitForChecksStillReportsAnotherReadError(t *testing.T) {
	old := checkPollInterval
	checkPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { checkPollInterval = old })

	runner := &restartingRunner{readsDown: 100, readErr: errors.New("no CI run \"ci-1\""), run: ci.Run{ID: "ci-1"}}
	result := ciTools(runner)[0].Handler(context.Background(), map[string]any{})
	if !result.IsError || !strings.Contains(result.Text, "no CI run") {
		t.Fatalf("wait reported %q, want the read error", result.Text)
	}
	if runner.reads != 1 {
		t.Fatalf("wait read %d times, want it to stop at the first error", runner.reads)
	}
}

// A daemon that never comes up within the wait is reported as that, not
// waited on past the wait's own timeout.
func TestWaitForChecksGivesUpOnCINotUpAtItsTimeout(t *testing.T) {
	old := checkPollInterval
	checkPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { checkPollInterval = old })

	runner := &restartingRunner{startsDown: 1 << 30}
	_, err := waitForGrainCI(context.Background(), runner, nil, 100*time.Millisecond)
	if !ci.IsNotUp(err) {
		t.Fatalf("err = %v, want ci.ErrNotUp", err)
	}
	if runner.starts < 2 {
		t.Fatalf("started %d times, want it to have retried", runner.starts)
	}
}
