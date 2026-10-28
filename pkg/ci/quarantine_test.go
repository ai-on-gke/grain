package ci

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestQuarantineParsingAndMatching(t *testing.T) {
	yamlContent := `
tests:
  - test_name: TestFlakyReconcile
    reason: Timing flake under CPU contention
    task_id: task-100
    follow_up_task_id: task-101
    ttl: 30d
  - test_name: "pkg/orchestrator_test.TestSub*"
    reason: Subtest suite flake
  - test_name: TestExpired
    reason: Old flake
    expires_at: 2020-01-01T00:00:00Z
`
	cfg, err := LoadQuarantine([]byte(yamlContent))
	if err != nil {
		t.Fatalf("LoadQuarantine failed: %v", err)
	}

	now := time.Now()

	// Exact match
	entry := cfg.FindMatch("TestFlakyReconcile", now)
	if entry == nil {
		t.Fatal("expected match for TestFlakyReconcile, got nil")
	}
	if entry.Reason != "Timing flake under CPU contention" {
		t.Errorf("unexpected reason: %s", entry.Reason)
	}

	// Subtest match
	entry = cfg.FindMatch("TestFlakyReconcile/ParallelRun_1", now)
	if entry == nil {
		t.Fatal("expected match for TestFlakyReconcile/ParallelRun_1, got nil")
	}

	// Package qualified match
	entry = cfg.FindMatch("pkg/engine.TestFlakyReconcile", now)
	if entry == nil {
		t.Fatal("expected match for pkg/engine.TestFlakyReconcile, got nil")
	}

	// Wildcard match
	entry = cfg.FindMatch("pkg/orchestrator_test.TestSubA", now)
	if entry == nil {
		t.Fatal("expected match for pkg/orchestrator_test.TestSubA, got nil")
	}

	// Expired rule should not match
	entry = cfg.FindMatch("TestExpired", now)
	if entry != nil {
		t.Errorf("expected expired test TestExpired not to match, got %v", entry)
	}

	// Non-matching test
	entry = cfg.FindMatch("TestCleanSuccess", now)
	if entry != nil {
		t.Errorf("expected no match for TestCleanSuccess, got %v", entry)
	}
}

func TestQuarantineEvaluation(t *testing.T) {
	yamlContent := `
tests:
  - test_name: TestFlakyA
    reason: Flake A
  - test_name: TestFlakyB
    reason: Flake B
`
	cfg, err := LoadQuarantine([]byte(yamlContent))
	if err != nil {
		t.Fatalf("LoadQuarantine failed: %v", err)
	}

	now := time.Now()

	// Scenario 1: All failures are quarantined
	summary1 := &FailureSummary{
		Tests: []TestFailure{
			{Name: "TestFlakyA", Message: "expected 1, got 2"},
			{Name: "TestFlakyB", Message: "timeout waiting for chan"},
		},
	}
	allQ, qNames, unQ := EvaluateQuarantine(summary1, cfg, now)
	if !allQ {
		t.Errorf("expected allQuarantined = true, got false")
	}
	if len(qNames) != 2 || len(unQ) != 0 {
		t.Errorf("expected 2 quarantined names and 0 unquarantined, got qNames=%v, unQ=%v", qNames, unQ)
	}
	if !summary1.Tests[0].Quarantined || !summary1.Tests[1].Quarantined {
		t.Errorf("expected summary tests to be marked Quarantined=true")
	}

	// Scenario 2: Mixed failure (one quarantined, one unquarantined)
	summary2 := &FailureSummary{
		Tests: []TestFailure{
			{Name: "TestFlakyA", Message: "expected 1, got 2"},
			{Name: "TestRealFailure", Message: "nil pointer dereference"},
		},
	}
	allQ, qNames, unQ = EvaluateQuarantine(summary2, cfg, now)
	if allQ {
		t.Errorf("expected allQuarantined = false for mixed failures, got true")
	}
	if len(qNames) != 1 || len(unQ) != 1 {
		t.Errorf("expected 1 quarantined and 1 unquarantined, got qNames=%v, unQ=%v", qNames, unQ)
	}

	// Scenario 3: Test failure with compile/diagnostic error
	summary3 := &FailureSummary{
		Tests: []TestFailure{
			{Name: "TestFlakyA", Message: "failed"},
		},
		Diagnostics: []Diagnostic{
			{File: "main.go", Line: 10, Message: "syntax error"},
		},
	}
	allQ, _, _ = EvaluateQuarantine(summary3, cfg, now)
	if allQ {
		t.Errorf("expected allQuarantined = false when compiler diagnostics exist, got true")
	}
}

func TestEngineQuarantineNonBlockingExecution(t *testing.T) {
	t.Parallel()

	quarantineYAML := `
tests:
  - test_name: TestFlaky
    reason: Flaky under contention
`

	f := &fakeSandboxes{
		script: func(cmd Command) (CommandResult, error) {
			if strings.Contains(cmd.Script, "quarantine") {
				return CommandResult{Output: quarantineYAML, ExitCode: 0}, nil
			}
			if strings.Contains(cmd.Script, "go test") {
				output := "=== RUN   TestFlaky\n--- FAIL: TestFlaky (0.01s)\n    reconcile_test.go:42: timeout waiting for sync\nFAIL\nexit status 1\n"
				return CommandResult{Output: output, ExitCode: 1}, nil
			}
			return CommandResult{ExitCode: 0}, nil
		},
	}

	eng := &Engine{Sandboxes: f}
	plan := Plan{
		Request: Request{Repo: "bwsalmon/grain", Ref: "main", Event: EventPush},
		Jobs: []PlannedJob{
			{
				ID: "ci.yml:test",
				Job: Job{
					Name: "test",
					Steps: []Step{
						{Run: "go test ./..."},
					},
				},
			},
		},
	}

	run := eng.Run(context.Background(), "run-1", plan, nil)

	if run.Conclusion != ConclusionSuccess {
		t.Fatalf("run.Conclusion = %v, want %v", run.Conclusion, ConclusionSuccess)
	}
	if !run.Quarantined {
		t.Fatalf("run.Quarantined = false, want true")
	}
	if len(run.Jobs) != 1 {
		t.Fatalf("len(run.Jobs) = %d, want 1", len(run.Jobs))
	}
	job := run.Jobs[0]
	if job.Conclusion != ConclusionSuccess {
		t.Errorf("job.Conclusion = %v, want %v", job.Conclusion, ConclusionSuccess)
	}
	if !job.Quarantined {
		t.Errorf("job.Quarantined = false, want true")
	}
	if job.Status != "passed (quarantined)" {
		t.Errorf("job.Status = %q, want \"passed (quarantined)\"", job.Status)
	}
	if !strings.Contains(job.Warning, "TestFlaky") {
		t.Errorf("job.Warning = %q, want it to mention TestFlaky", job.Warning)
	}
}

// Quarantine excuses a step that ran and failed, and nothing else. An
// infrastructure step that errored never finished judging the tree, so
// quarantined test names in what it printed must not turn the job green.
func TestEngineQuarantineDoesNotExcuseErroredStep(t *testing.T) {
	t.Parallel()

	f := &fakeSandboxes{
		script: func(cmd Command) (CommandResult, error) {
			if strings.Contains(cmd.Script, "quarantine") {
				return CommandResult{Output: "tests:\n  - test_name: TestFlaky\n", ExitCode: 0}, nil
			}
			if strings.Contains(cmd.Script, "go test") {
				return CommandResult{Output: "--- FAIL: TestFlaky (0.01s)\nFAIL\n", ExitCode: 1}, nil
			}
			return CommandResult{ExitCode: 0}, nil
		},
	}
	eng := &Engine{Sandboxes: f}
	plan := Plan{
		Request: Request{Repo: "bwsalmon/grain", Ref: "main", Event: EventPush},
		Jobs: []PlannedJob{{
			ID:  "ci.yml:test",
			Job: Job{Name: "test", Steps: []Step{{Run: "go test ./...", Infrastructure: true}}},
		}},
	}

	run := eng.Run(context.Background(), "run-1", plan, nil)
	if run.Conclusion != ConclusionErrored {
		t.Fatalf("run.Conclusion = %v, want %v", run.Conclusion, ConclusionErrored)
	}
	if run.Quarantined || run.Jobs[0].Quarantined {
		t.Errorf("an errored step was quarantined: %+v", run.Jobs[0])
	}
}

// A step stopped by its own `timeout-minutes` printed a quarantined
// failure and then hung: the sandbox kills it at its bound and it comes
// back as a plain non-zero exit. It never finished, so quarantine must not
// turn it green -- while the same step failing well inside its bound still
// is excused. (docs/test-runs/2026-10-02-ci-cache-glob-wait-credit.md, F1.)
func TestEngineQuarantineDoesNotExcuseAStepPastItsOwnTimeout(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		took  time.Duration
		green bool
	}{
		{name: "killed at its bound", took: 2 * time.Minute, green: false},
		{name: "failed inside its bound", took: 10 * time.Second, green: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var mu sync.Mutex
			now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
			var timeouts []time.Duration
			f := &fakeSandboxes{
				script: func(cmd Command) (CommandResult, error) {
					if strings.Contains(cmd.Script, "quarantine") {
						return CommandResult{Output: "tests:\n  - test_name: TestFlaky\n", ExitCode: 0}, nil
					}
					if strings.Contains(cmd.Script, "go test") {
						mu.Lock()
						timeouts = append(timeouts, cmd.Timeout)
						now = now.Add(tc.took)
						mu.Unlock()
						return CommandResult{Output: "--- FAIL: TestFlaky (0.01s)\n", ExitCode: -1}, nil
					}
					return CommandResult{ExitCode: 0}, nil
				},
			}
			eng := &Engine{Sandboxes: f, Now: func() time.Time {
				mu.Lock()
				defer mu.Unlock()
				return now
			}}
			plan := Plan{
				Request: Request{Repo: "bwsalmon/grain", Ref: "main", Event: EventPush},
				Jobs: []PlannedJob{{
					ID:  "ci.yml:test",
					Job: Job{Name: "test", Steps: []Step{{Run: "go test ./...", TimeoutMinutes: 1}}},
				}},
			}

			run := eng.Run(context.Background(), "run-1", plan, nil)
			if len(timeouts) == 0 {
				t.Fatal("the step never ran")
			}
			for _, got := range timeouts {
				if got != time.Minute {
					t.Fatalf("the step was run with timeouts %v, want its own 1m bound", timeouts)
				}
			}
			job := run.Jobs[0]
			if tc.green {
				if run.Conclusion != ConclusionSuccess || !job.Quarantined {
					t.Fatalf("run %v, quarantined %v: a failure inside the bound should still be excused", run.Conclusion, job.Quarantined)
				}
				return
			}
			if run.Conclusion != ConclusionFailure {
				t.Fatalf("run.Conclusion = %v, want %v", run.Conclusion, ConclusionFailure)
			}
			if run.Quarantined || job.Quarantined {
				t.Errorf("a step past its own timeout was quarantined: %+v", job)
			}
			if got := job.Steps[0].Error; !strings.Contains(got, "1m0s bound") {
				t.Errorf("step error = %q, want it to name the step's own bound", got)
			}
		})
	}
}

// The quarantine file is only read from the paths trustboundary guards: a
// root-level quarantine.yml is one a branch could add without review.
func TestEngineQuarantineIgnoresUnguardedPaths(t *testing.T) {
	t.Parallel()

	var scripts []string
	f := &fakeSandboxes{
		script: func(cmd Command) (CommandResult, error) {
			if strings.Contains(cmd.Script, "quarantine") {
				scripts = append(scripts, cmd.Script)
			}
			return CommandResult{Output: "--- FAIL: TestFlaky (0.01s)\nFAIL\n", ExitCode: 1}, nil
		},
	}
	eng := &Engine{Sandboxes: f}
	plan := Plan{
		Request: Request{Repo: "bwsalmon/grain", Ref: "main", Event: EventPush},
		Jobs:    []PlannedJob{{ID: "ci.yml:test", Job: Job{Name: "test", Steps: []Step{{Run: "go test ./..."}}}}},
	}
	eng.Run(context.Background(), "run-1", plan, nil)
	if len(scripts) == 0 {
		t.Fatal("the quarantine file was never read")
	}
	for _, script := range scripts {
		if strings.Contains(strings.ReplaceAll(script, ".grain/quarantine.y", ""), "quarantine.y") {
			t.Errorf("quarantine read outside .grain/: %q", script)
		}
	}
}

// test_name is never a regular expression: a repository's own quarantine
// file must not be able to suppress every test with one ".*" entry.
func TestQuarantineMatchesIgnoresRegex(t *testing.T) {
	names := []string{
		"TestFlakyReconcile",
		"pkg/orchestrator_test.TestCompletelyUnrelatedThing",
		"TestSomethingCriticalThatMustNeverBeSuppressed",
	}
	for _, pattern := range []string{".*", ".+", "Test.*", "TestFlaky.econcile"} {
		entry := &QuarantineEntry{TestName: pattern}
		for _, name := range names {
			if entry.Matches(name) {
				t.Errorf("QuarantineEntry{TestName: %q}.Matches(%q) = true, want false", pattern, name)
			}
		}
	}
	if !(&QuarantineEntry{TestName: "TestFlaky*"}).Matches("TestFlakyReconcile") {
		t.Error("glob patterns should still match")
	}
}

func TestEvaluateQuarantineWithRunGoTestsMultiLineAndPanicOutput(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	cfg := &QuarantineConfig{
		Tests: []QuarantineEntry{
			{TestName: "TestFlakyMultiLog"},
			{TestName: "TestFlakyPanic"},
		},
	}

	raw := `[go test] --- FAIL: TestFlakyMultiLog (0.02s) [pkg/foo]
[go test] --- FAIL: TestFlakyPanic (0.01s) [pkg/foo]
panic: flaky boom [recovered, repanicked] [pkg/foo]
FAIL	github.com/ai-on-gke/grain/pkg/foo	0.030s

=== failing tests ===
--- FAIL: TestFlakyMultiLog (0.02s) [pkg/foo]
    flaky_test.go:12: first log line
    flaky_test.go:18: second error line
--- FAIL: TestFlakyPanic (0.01s) [pkg/foo]
panic: flaky boom [recovered, repanicked]

goroutine 9 [running]:
github.com/ai-on-gke/grain/pkg/foo.TestFlakyPanic(0xc0000c8380?)
	/home/debian/work/pkg/foo/flaky_test.go:30 +0x2c

=== panics ===
panic: flaky boom [recovered, repanicked] [pkg/foo]

goroutine 9 [running]:
github.com/ai-on-gke/grain/pkg/foo.TestFlakyPanic(0xc0000c8380?)
	/home/debian/work/pkg/foo/flaky_test.go:30 +0x2c

=== last 200 lines of the raw run ===
=== RUN   TestPassingWithLog
    passing_test.go:44: setup finished cleanly
--- PASS: TestPassingWithLog (0.00s)
=== RUN   TestFlakyMultiLog
    flaky_test.go:12: first log line
    flaky_test.go:18: second error line
--- FAIL: TestFlakyMultiLog (0.02s)
FAIL	github.com/ai-on-gke/grain/pkg/foo	0.030s`

	summary := ExtractFailureSummary(raw)
	if summary == nil {
		t.Fatal("expected non-nil FailureSummary")
	}
	allQ, qNames, unQ := EvaluateQuarantine(summary, cfg, now)
	if !allQ {
		t.Fatalf("expected allQuarantined = true for run-go-tests.sh output, got false (diagnostics=%+v, rawErrors=%+v, unQ=%+v)",
			summary.Diagnostics, summary.RawErrors, unQ)
	}
	if len(qNames) != 2 {
		t.Errorf("expected 2 deduplicated quarantined test names, got %v", qNames)
	}
}

// A job that passed because its failing tests are quarantined keeps them
// in its FailureSummary, marked quarantined, so test history still sees
// them fail once the step output is gone (grain/task-1572).
func TestEngineQuarantinedPassKeepsItsFailureSummary(t *testing.T) {
	t.Parallel()

	f := &fakeSandboxes{
		script: func(cmd Command) (CommandResult, error) {
			if strings.Contains(cmd.Script, "quarantine") {
				return CommandResult{Output: "tests:\n  - test_name: TestFlaky\n", ExitCode: 0}, nil
			}
			if strings.Contains(cmd.Script, "go test") {
				return CommandResult{Output: "--- FAIL: TestFlaky (0.01s)\n    flaky_test.go:9: timed out\n--- PASS: TestSteady (0.00s)\nFAIL\n", ExitCode: 1}, nil
			}
			return CommandResult{ExitCode: 0}, nil
		},
	}
	eng := &Engine{Sandboxes: f}
	plan := Plan{
		Request: Request{Repo: "bwsalmon/grain", Ref: "main", Event: EventPush},
		Jobs:    []PlannedJob{{ID: "ci.yml:test", Job: Job{Name: "test", Steps: []Step{{Run: "go test ./..."}}}}},
	}

	run := eng.Run(context.Background(), "run-1", plan, nil)
	job := run.Jobs[0]
	if job.Conclusion != ConclusionSuccess || !job.Quarantined {
		t.Fatalf("job = %+v, want a quarantined pass", job)
	}
	fs := job.FailureSummary
	if fs == nil || len(fs.Tests) != 1 || fs.Tests[0].Name != "TestFlaky" || !fs.Tests[0].Quarantined {
		t.Fatalf("job.FailureSummary = %+v, want TestFlaky marked quarantined", fs)
	}

	// With the step output dropped, as a listed run has it.
	job.Steps = nil
	run.Jobs[0] = job
	report := QueryTestHistory([]Run{run}, "TestFlaky")
	if report.Quarantined != 1 || report.Failed != 0 || report.Passed != 0 {
		t.Errorf("history of TestFlaky = %+v, want one quarantined failure", report)
	}
	if !strings.Contains(report.Render(), "Quarantined: 1") {
		t.Errorf("rendered history does not say it was quarantined:\n%s", report.Render())
	}
}

// A quarantined failure in an early step does not stand in for the real
// failure of a later one: the failure that decided the job leads its
// summary, and the quarantined test is kept after it.
func TestEngineRealFailureLeadsAQuarantinedOne(t *testing.T) {
	t.Parallel()

	f := &fakeSandboxes{
		script: func(cmd Command) (CommandResult, error) {
			switch {
			case strings.Contains(cmd.Script, "quarantine"):
				return CommandResult{Output: "tests:\n  - test_name: TestFlaky\n", ExitCode: 0}, nil
			case strings.Contains(cmd.Script, "go test ./a"):
				return CommandResult{Output: "--- FAIL: TestFlaky (0.01s)\nFAIL\n", ExitCode: 1}, nil
			case strings.Contains(cmd.Script, "go test ./b"):
				return CommandResult{Output: "--- FAIL: TestBroken (0.01s)\nFAIL\n", ExitCode: 1}, nil
			}
			return CommandResult{ExitCode: 0}, nil
		},
	}
	eng := &Engine{Sandboxes: f}
	plan := Plan{
		Request: Request{Repo: "bwsalmon/grain", Ref: "main", Event: EventPush},
		Jobs: []PlannedJob{{ID: "ci.yml:test", Job: Job{Name: "test", Steps: []Step{
			{Run: "go test ./a"}, {Run: "go test ./b"},
		}}}},
	}

	run := eng.Run(context.Background(), "run-1", plan, nil)
	job := run.Jobs[0]
	if job.Conclusion != ConclusionFailure {
		t.Fatalf("job.Conclusion = %v, want failure", job.Conclusion)
	}
	fs := job.FailureSummary
	if fs == nil || len(fs.Tests) != 2 {
		t.Fatalf("job.FailureSummary = %+v, want both tests", fs)
	}
	if fs.Tests[0].Name != "TestBroken" || fs.Tests[0].Quarantined {
		t.Errorf("first test = %+v, want TestBroken, not quarantined", fs.Tests[0])
	}
	if fs.Tests[1].Name != "TestFlaky" || !fs.Tests[1].Quarantined {
		t.Errorf("second test = %+v, want TestFlaky, quarantined", fs.Tests[1])
	}
	if !strings.Contains(fs.Render(), "TestFlaky [quarantined]") {
		t.Errorf("summary does not mark the quarantined test:\n%s", fs.Render())
	}
}
