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
	"encoding/base64"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRetriedJobKeepsAttempt1LogsAndPerTestOutputAcrossStores(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	attempt1GotestLog := strings.Join([]string{
		"=== PACKAGE github.com/ai-on-gke/grain/pkg/ci",
		"=== RUN   TestPassOnAttempt1",
		"--- PASS: TestPassOnAttempt1 (0.01s)",
		"=== RUN   TestFlakyOnAttempt1",
		"    flaky_test.go:42: expected 200, got 500 on first attempt",
		"--- FAIL: TestFlakyOnAttempt1 (0.12s)",
		"FAIL\tgithub.com/ai-on-gke/grain/pkg/ci\t0.130s",
	}, "\n") + "\n"

	attempt2GotestLog := strings.Join([]string{
		"=== PACKAGE github.com/ai-on-gke/grain/pkg/ci",
		"=== RUN   TestPassOnAttempt1",
		"--- PASS: TestPassOnAttempt1 (0.01s)",
		"=== RUN   TestFlakyOnAttempt1",
		"    flaky_test.go:42: retry succeeded cleanly",
		"--- PASS: TestFlakyOnAttempt1 (0.02s)",
		"ok  \tgithub.com/ai-on-gke/grain/pkg/ci\t0.030s",
	}, "\n") + "\n"

	wrapGoTestLog := func(raw string) string {
		return goTestLogStatusMarker + "ok\n" +
			goTestLogBeginMarker + "\n" +
			base64.StdEncoding.EncodeToString([]byte(raw)) + "\n" +
			goTestLogEndMarker + "\n"
	}

	var mu sync.Mutex
	stepRuns := 0
	gotestReads := 0
	sandboxes := &fakeSandboxes{
		script: func(cmd Command) (CommandResult, error) {
			mu.Lock()
			defer mu.Unlock()
			if strings.Contains(cmd.Script, goTestLogStatusMarker) {
				gotestReads++
				if gotestReads == 1 {
					return CommandResult{ExitCode: 0, Output: wrapGoTestLog(attempt1GotestLog)}, nil
				}
				return CommandResult{ExitCode: 0, Output: wrapGoTestLog(attempt2GotestLog)}, nil
			}
			stepRuns++
			if stepRuns == 1 {
				return CommandResult{
					ExitCode: 1,
					Output:   "=== per-package results ===\n  FAIL  github.com/ai-on-gke/grain/pkg/ci  (0.1s)\n=== failing tests ===\n  --- FAIL: github.com/ai-on-gke/grain/pkg/ci TestFlakyOnAttempt1\n      flaky_test.go:42: expected 200, got 500 on first attempt\n",
				}, nil
			}
			return CommandResult{
				ExitCode: 0,
				Output:   "=== per-package results ===\n  ok    github.com/ai-on-gke/grain/pkg/ci  (0.0s)\n",
			}, nil
		},
	}

	plan := planFor(t, Request{Repo: "bwsalmon/grain", Ref: "grain/task-846", Commit: "abc1234", Event: EventPush}, map[string]string{
		".grain/workflows/ci.yml": "on: push\njobs:\n  go:\n    steps:\n      - name: Test\n        run: ./scripts/run-go-tests.sh ./...\n",
	})

	result := (&Engine{Sandboxes: sandboxes, Concurrency: 1}).Run(ctx, "run-retried-846", plan, nil)
	if len(result.Jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(result.Jobs))
	}
	job := result.Jobs[0]
	if !job.Retried || job.RetryCount != 1 {
		t.Fatalf("expected job to be retried once, got Retried=%v RetryCount=%d", job.Retried, job.RetryCount)
	}
	if len(job.EffectiveAttempts()) != 2 {
		t.Fatalf("expected 2 attempts on retried job, got %d", len(job.EffectiveAttempts()))
	}

	verifyJobAttempts := func(label string, j JobRun) {
		t.Helper()
		att1, ok := j.GetAttempt(1)
		if !ok {
			t.Fatalf("%s: GetAttempt(1) not found", label)
		}
		if att1.Conclusion != ConclusionFailure {
			t.Errorf("%s: attempt 1 conclusion = %q, want %q", label, att1.Conclusion, ConclusionFailure)
		}
		if len(att1.Steps) != 1 || !strings.Contains(att1.Steps[0].Output, "expected 200, got 500 on first attempt") {
			t.Errorf("%s: attempt 1 step output = %+v, want attempt 1 failure text", label, att1.Steps)
		}
		if !strings.Contains(att1.GoTestLog, "expected 200, got 500 on first attempt") {
			t.Errorf("%s: attempt 1 GoTestLog = %q, want raw go test log", label, att1.GoTestLog)
		}
		testLog1, ok := LookupAttemptGoTest(att1, "pkg/ci", "TestFlakyOnAttempt1")
		if !ok || !strings.Contains(testLog1, "expected 200, got 500 on first attempt") {
			t.Errorf("%s: attempt 1 per-test output for pkg/ci/TestFlakyOnAttempt1 = %q (%v)", label, testLog1, ok)
		}

		att2, ok := j.GetAttempt(2)
		if !ok {
			t.Fatalf("%s: GetAttempt(2) not found", label)
		}
		if att2.Conclusion != ConclusionSuccess {
			t.Errorf("%s: attempt 2 conclusion = %q, want %q", label, att2.Conclusion, ConclusionSuccess)
		}
		if len(att2.Steps) != 1 || !strings.Contains(att2.Steps[0].Output, "ok    github.com/ai-on-gke/grain/pkg/ci") {
			t.Errorf("%s: attempt 2 step output = %+v", label, att2.Steps)
		}
		testLog2, ok := LookupAttemptGoTest(att2, "pkg/ci", "TestFlakyOnAttempt1")
		if !ok || !strings.Contains(testLog2, "retry succeeded cleanly") {
			t.Errorf("%s: attempt 2 per-test output for pkg/ci/TestFlakyOnAttempt1 = %q (%v)", label, testLog2, ok)
		}

		latest, ok := j.LatestAttempt()
		if !ok || latest.Attempt != 2 {
			t.Errorf("%s: LatestAttempt() = %+v (%v), want attempt 2", label, latest, ok)
		}
		failing, ok := j.FailingAttempt()
		if !ok || failing.Attempt != 1 {
			t.Errorf("%s: FailingAttempt() = %+v (%v), want attempt 1", label, failing, ok)
		}
	}

	verifyJobAttempts("engine", job)

	now := time.Now().UTC().Truncate(time.Millisecond)
	run := Run{
		ID:         "run-retried-846",
		Key:        "key-retried-846",
		Repo:       "bwsalmon/grain",
		Ref:        "grain/task-846",
		Commit:     "abc1234",
		Event:      EventPush,
		StartedAt:  &now,
		FinishedAt: &now,
		Conclusion: ConclusionSuccess,
		Jobs:       []JobRun{job},
	}

	// Verify MemoryRunStore preserves both attempts.
	memStore := NewRunStore(10)
	memStore.Put(run)
	memGot, ok := memStore.Get(run.ID)
	if !ok || len(memGot.Jobs) != 1 {
		t.Fatalf("MemoryRunStore.Get failed: %+v (%v)", memGot, ok)
	}
	verifyJobAttempts("MemoryRunStore", memGot.Jobs[0])

	// Verify StoredRunStore preserves both attempts across restarts.
	dir := t.TempDir()
	dbStore := storedTestStore(t, dir, StoredRunStoreOptions{})
	dbStore.Put(run)

	reopened := storedTestStore(t, dir, StoredRunStoreOptions{})
	dbGot, ok := reopened.Get(run.ID)
	if !ok || len(dbGot.Jobs) != 1 {
		t.Fatalf("StoredRunStore.Get failed: %+v (%v)", dbGot, ok)
	}
	verifyJobAttempts("StoredRunStore", dbGot.Jobs[0])
}
