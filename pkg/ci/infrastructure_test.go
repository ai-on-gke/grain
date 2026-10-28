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
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// A cache miss must never be able to fail a test (grain/task-464).
//
// These are the tests of that sentence. What they assert is the whole of
// what `infrastructure: true` on a step buys -- a step that fetches the
// job's environment is off the job's clock, and its failure is
// ConclusionErrored rather than ConclusionFailure -- and the two things it
// deliberately does not buy: it does not make the step optional, and it
// does not launder a failing test that happens to run after one.
//
// The history behind them is in Step.Infrastructure: a branch that
// changed a guest input missed `guest:inputs-<key>`, built the ~1GB guest
// inline, ran past ci.DefaultJobTimeout doing it, and was recorded red.
// Grain then filed repairs against branches that were fine, held every
// merge in the repository behind the same verdicts, and finally deleted
// the job (grain/task-427).

func infraPlan(t *testing.T, workflow string) Plan {
	t.Helper()
	return planFor(t, Request{Repo: "o/r", Ref: "grain/task-1", Commit: "abc", Event: EventPush},
		map[string]string{".grain/workflows/a.yml": workflow})
}

// A step that could not get the job an environment is a verdict about
// this deployment and about nothing else: errored, not failure, so the
// branch is not broken, no repair is filed against it, and -- because
// errored is not cacheable -- the next run samples the commit again
// rather than replaying what a cold cache decided.
func TestAStepThatCannotFetchTheJobsEnvironmentErrorsRatherThanFails(t *testing.T) {
	sandboxes := &fakeSandboxes{script: func(cmd Command) (CommandResult, error) {
		if strings.Contains(cmd.Script, "resolve-guest") {
			return CommandResult{ExitCode: 1, Output: "no such tag: guest:inputs-deadbeef"}, nil
		}
		return CommandResult{}, nil
	}}
	cache := newCannedCache()
	plan := infraPlan(t, "on: push\njobs:\n  kvm:\n    steps:\n"+
		"      - name: Resolve the sandbox guest\n        infrastructure: true\n        run: resolve-guest\n"+
		"      - name: Twenty boots\n        run: go test ./...\n")

	run := (&Engine{Sandboxes: sandboxes, Cache: cache}).Run(context.Background(), "r1", plan, nil)

	job := jobByID(t, run, "kvm")
	if job.Conclusion != ConclusionErrored {
		t.Fatalf("job conclusion = %q, want errored: the cache was cold, which says nothing about the tree", job.Conclusion)
	}
	if run.Conclusion != ConclusionErrored {
		t.Errorf("run conclusion = %q, want errored: nothing in this run judged the commit", run.Conclusion)
	}
	if run.Conclusion == ConclusionFailure {
		t.Error("a run that only failed to get an environment must never be a failure: that is what holds a merge queue and files a repair")
	}
	if !strings.Contains(job.Error, "Resolve the sandbox guest") {
		t.Errorf("job error = %q, want the step that could not get an environment named in it", job.Error)
	}
	if len(job.Steps) != 2 {
		t.Fatalf("steps = %+v, want both recorded", job.Steps)
	}
	if job.Steps[0].Conclusion != ConclusionErrored {
		t.Errorf("the infrastructure step concluded %q, want errored", job.Steps[0].Conclusion)
	}
	if job.Steps[0].Output == "" {
		t.Error("the infrastructure step kept no output, and it is the whole of what a reader has to go on")
	}
	// Not optional: the job stopped, exactly as it stops at any other
	// step that did not succeed. A job whose environment never arrived
	// must not be able to look as though it had tested something.
	if job.Steps[1].Conclusion != ConclusionSkipped {
		t.Errorf("the step after it concluded %q, want skipped: nothing ran", job.Steps[1].Conclusion)
	}
	if len(cache.saved) != 0 {
		t.Errorf("an errored job was written to the cache (%d entries): the next run has to sample the commit again", len(cache.saved))
	}
}

// The other half of the same invariant: the key says what a step is
// about, not whether it counts. A test that fails after the environment
// arrived is a failing test.
func TestAFailingTestAfterAnInfrastructureStepIsStillAFailure(t *testing.T) {
	sandboxes := &fakeSandboxes{script: func(cmd Command) (CommandResult, error) {
		if strings.Contains(cmd.Script, "go test") {
			return CommandResult{ExitCode: 1, Output: "--- FAIL: TestSomething"}, nil
		}
		return CommandResult{}, nil
	}}
	plan := infraPlan(t, "on: push\njobs:\n  kvm:\n    steps:\n"+
		"      - name: Resolve the sandbox guest\n        infrastructure: true\n        run: resolve-guest\n"+
		"      - name: Twenty boots\n        run: go test ./...\n")

	run := (&Engine{Sandboxes: sandboxes}).Run(context.Background(), "r1", plan, nil)

	job := jobByID(t, run, "kvm")
	if job.Conclusion != ConclusionFailure {
		t.Fatalf("job conclusion = %q, want failure: the environment arrived and then the tree came back wrong", job.Conclusion)
	}
	if job.Error != "" {
		t.Errorf("job error = %q, want none: a failing test is reported by the step that failed", job.Error)
	}
}

// `continue-on-error:` still wins, and still means what it says: a warm
// that is worth trying and not worth stopping for.
func TestAnInfrastructureStepMayStillContinueOnError(t *testing.T) {
	sandboxes := &fakeSandboxes{script: func(cmd Command) (CommandResult, error) {
		if strings.Contains(cmd.Script, "prewarm") {
			return CommandResult{ExitCode: 1}, nil
		}
		return CommandResult{}, nil
	}}
	plan := infraPlan(t, "on: push\njobs:\n  kvm:\n    steps:\n"+
		"      - name: Prewarm\n        infrastructure: true\n        continue-on-error: true\n        run: prewarm\n"+
		"      - name: Test\n        run: go test ./...\n")

	run := (&Engine{Sandboxes: sandboxes}).Run(context.Background(), "r1", plan, nil)

	job := jobByID(t, run, "kvm")
	if job.Conclusion != ConclusionSuccess {
		t.Fatalf("job conclusion = %q (%s), want success: the step that failed said its failure does not stop the job",
			job.Conclusion, job.Error)
	}
	if job.Steps[0].Conclusion != ConclusionErrored {
		t.Errorf("the step itself concluded %q, want errored: it still did not get what it went for", job.Steps[0].Conclusion)
	}
}

// A job's `timeout-minutes` bounds the work the job does. Fetching a
// gigabyte out of a registry, or building it because nothing published
// it, is the environment the job waits for -- so it gets a bound of its
// own, and the sandbox is told that bound rather than the minute the job
// had left.
func TestFetchingTheEnvironmentIsNotChargedToTheJobsTimeout(t *testing.T) {
	sandboxes := &fakeSandboxes{}
	plan := infraPlan(t, "on: push\njobs:\n  kvm:\n    timeout-minutes: 1\n    steps:\n"+
		"      - name: Resolve the sandbox guest\n        infrastructure: true\n        timeout-minutes: 120\n        run: resolve-guest\n"+
		"      - name: Twenty boots\n        run: go test ./...\n")

	run := (&Engine{Sandboxes: sandboxes}).Run(context.Background(), "r1", plan, nil)
	if job := jobByID(t, run, "kvm"); job.Conclusion != ConclusionSuccess {
		t.Fatalf("job conclusion = %q (%s), want success", job.Conclusion, job.Error)
	}

	if len(sandboxes.commands) != 2 {
		t.Fatalf("ran %d commands, want 2: %+v", len(sandboxes.commands), sandboxes.commands)
	}
	// The guest resolve is given its own two hours, not what is left of
	// the job's one minute. This is the number that decided the incident:
	// a 50-110 minute inline build inside a job bounded at
	// DefaultJobTimeout was killed and recorded as a red verdict.
	if got := sandboxes.commands[0].Timeout; got < 119*time.Minute {
		t.Errorf("the infrastructure step was given %s, want its own 2h: a job's bound is not what fetching its environment gets", got)
	}
	// And the job's own bound is untouched for the step that does the
	// work: the tests get the minute the job declared, whether the cache
	// was warm or cold.
	if got := sandboxes.commands[1].Timeout; got <= 0 || got > time.Minute {
		t.Errorf("the test step was given %s, want the job's own minute", got)
	}
}

// The clock itself, at the granularity the engine cannot show: an
// infrastructure step spends its own time and gives it back, so the steps
// after it are where they would have been had the cache been warm.
func TestTheJobsClockDoesNotChargeItForFetchingItsEnvironment(t *testing.T) {
	job := Job{TimeoutMinutes: 30}
	clock := &jobClock{deadline: time.Now().Add(30 * time.Minute)}
	before := clock.deadline

	ctx, spent := clock.bound(context.Background(), job, Step{Infrastructure: true, TimeoutMinutes: 120})
	deadline, ok := ctx.Deadline()
	if !ok || deadline.Sub(before) < 80*time.Minute {
		t.Fatalf("an infrastructure step's deadline is %v (job's is %v), want its own 2h", deadline, before)
	}
	time.Sleep(2 * time.Millisecond)
	spent()
	if !clock.deadline.After(before) {
		t.Errorf("the job's deadline is still %v: the time spent fetching its environment was charged to it", clock.deadline)
	}

	// An ordinary step is bounded by the job's own deadline and spends it,
	// which is what the key changes nothing about.
	ordinary := clock.deadline
	ctx, spent = clock.bound(context.Background(), job, Step{})
	deadline, ok = ctx.Deadline()
	if !ok || !deadline.Equal(ordinary) {
		t.Errorf("an ordinary step's deadline is %v, want the job's own %v", deadline, ordinary)
	}
	spent()
	if !clock.deadline.Equal(ordinary) {
		t.Errorf("the job's deadline moved to %v: only an infrastructure step gives time back", clock.deadline)
	}
}

// An infrastructure step that never finishes is errored too, rather than
// cancelled or failed: the bound expiring is still "we did not get an
// environment", and a bound is there so that a hung fetch cannot hold a
// deployment's sandbox forever.
func TestAnInfrastructureStepThatRunsOutOfTimeIsErrored(t *testing.T) {
	sandboxes := &fakeSandboxes{script: func(cmd Command) (CommandResult, error) {
		// What a backend does with a command whose bound expired: the
		// context is gone and there is no exit code to report.
		return CommandResult{ExitCode: -1}, context.DeadlineExceeded
	}}
	plan := infraPlan(t, "on: push\njobs:\n  kvm:\n    steps:\n"+
		"      - name: Resolve the sandbox guest\n        infrastructure: true\n        run: resolve-guest\n")

	run := (&Engine{Sandboxes: sandboxes}).Run(context.Background(), "r1", plan, nil)

	job := jobByID(t, run, "kvm")
	if job.Conclusion != ConclusionErrored {
		t.Fatalf("job conclusion = %q (%s), want errored", job.Conclusion, job.Error)
	}
	if !strings.Contains(job.Error, "not a verdict") {
		t.Errorf("job error = %q, want it to say plainly that this is not a verdict on the commit", job.Error)
	}
}

// The format's own half: the key parses, and -- because a Job is hashed
// through encoding/json into its cache key -- a step that does not
// declare it hashes exactly as it did, so adding the key misses no cache
// entry in any deployment. TestDeclaringGuestInputsMissesNoCacheEntry is
// the same test for the same reason.
func TestDeclaringAStepInfrastructureParsesAndMissesNoCacheEntry(t *testing.T) {
	t.Parallel()
	w := mustParse(t, ".grain/workflows/a.yml", "on: push\njobs:\n  kvm:\n    steps:\n"+
		"      - name: Resolve the sandbox guest\n        infrastructure: true\n        run: resolve-guest\n"+
		"      - name: Test\n        run: go test ./...\n")
	if got := w.Jobs[0].Steps; !got[0].Infrastructure || got[1].Infrastructure {
		t.Fatalf("parsed steps = %+v, want only the first declared infrastructure", got)
	}

	req := Request{Repo: "bwsalmon/grain", Ref: "main", Commit: "aaa", Event: EventPush}
	wf := Workflow{Path: "w.yml", Name: "w"}
	plain := Job{ID: "kvm", Cache: true, Steps: []Step{{Run: "resolve-guest"}}}
	declaring := Job{ID: "kvm", Cache: true, Steps: []Step{{Run: "resolve-guest", Infrastructure: true}}}
	if Fingerprint(req, wf, plain) == Fingerprint(req, wf, declaring) {
		t.Error("declaring a step infrastructure did not change the job's key, and it changes what the job's failure means")
	}
	body, err := json.Marshal(plain)
	if err != nil {
		t.Fatalf("marshalling a job: %v", err)
	}
	if strings.Contains(string(body), "infrastructure") {
		t.Errorf("a job with no infrastructure step hashes as %s, which mentions the new field and so misses "+
			"every cache entry every deployment holds", body)
	}
}
