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
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// A builtin job (Job.Builtin) reaches Sandboxes.Acquire with its name on
// the request, which is how the daemon knows to answer it itself rather
// than build a machine for it; a job a file declares carries none.
func TestABuiltinJobIsNamedOnItsSandboxRequest(t *testing.T) {
	workflow := mustParse(t, ".grain/workflows/a.yml",
		"on: push\njobs:\n  check:\n    steps:\n      - run: grain state check /state\n  other:\n    steps:\n      - run: true\n")
	for i := range workflow.Jobs {
		if workflow.Jobs[i].ID == "check" {
			workflow.Jobs[i].Builtin = "grain-state-check"
		}
	}
	plan, err := PlanRun(Request{Repo: "grain/config", Ref: "grain/task-1", Commit: "abc", Event: EventPush}, []Workflow{workflow})
	if err != nil {
		t.Fatalf("planning: %v", err)
	}
	sandboxes := &fakeSandboxes{acquires: make(chan SandboxRequest, 2)}
	run := (&Engine{Sandboxes: sandboxes}).Run(context.Background(), "r1", plan, nil)
	if run.Conclusion != ConclusionSuccess {
		t.Fatalf("run = %q, want success", run.Conclusion)
	}
	close(sandboxes.acquires)
	got := map[string]string{}
	for req := range sandboxes.acquires {
		got[req.Name] = req.Builtin
	}
	if len(got) != 2 {
		t.Fatalf("acquired %v, want both jobs", got)
	}
	for name, builtin := range got {
		switch {
		case strings.Contains(name, "check") && builtin != "grain-state-check":
			t.Errorf("%s: Builtin = %q, want the job's own", name, builtin)
		case strings.Contains(name, "other") && builtin != "":
			t.Errorf("%s: Builtin = %q, want none for a job the file declares", name, builtin)
		}
	}
}

// No workflow file can ask for one: the format has no key for it, so a
// branch cannot point one of its own jobs at work the daemon does itself.
func TestAWorkflowFileCannotDeclareABuiltinJob(t *testing.T) {
	_, err := Parse(".grain/workflows/a.yml",
		[]byte("on: push\njobs:\n  check:\n    builtin: grain-state-check\n    steps:\n      - run: true\n"))
	if err == nil {
		t.Fatal("a workflow file declared `builtin:` and was accepted")
	}
}

// A builtin job runs in-process in the daemon with no image and no sandbox, so
// it neither blocks on nor charges Engine.Jobs (SharedGate / model.Ledger) or
// the per-run sandbox concurrency gate when the host's sandbox capacity is full.
func TestABuiltinJobSkipsSandboxCapacityGates(t *testing.T) {
	ledger := &model.Ledger{}
	budget := model.Budget{
		Host:             model.Commitment{CPUs: 4, MemoryMB: 16 * 1024, DiskGB: 18},
		CPUOvercommit:    1,
		MemoryOvercommit: 1,
		DiskOvercommit:   1.5, // 27 GiB sandbox disk budget
	}
	defaultShape := model.Commitment{CPUs: 2, MemoryMB: 8 * 1024, DiskGB: 30}
	// A sole agent sandbox holds 30 GiB against a 27 GiB disk budget (>100%),
	// and the shared gate's count limit (1) is also held.
	if v, _ := ledger.Admit(budget, model.PoolAgent, "run-3-1", model.Commitment{CPUs: 4, MemoryMB: 16 * 1024, DiskGB: 30}); !v.OK || !v.Sole {
		t.Fatalf("admitting sole agent sandbox: OK=%v Sole=%v (%q)", v.OK, v.Sole, v.Reason)
	}
	gate := &SharedGate{
		Limit:   func() int { return 1 },
		Default: func() model.Commitment { return defaultShape },
		Budget:  func() model.Budget { return budget },
		Ledger:  func() *model.Ledger { return ledger },
		Poll:    time.Millisecond,
	}
	// Hold the gate's sole count slot before the agent budget fills it, or
	// verify via Limit: 0 that any Acquire call on Engine.Jobs would block.
	gate.Limit = func() int { return 0 }

	workflow := mustParse(t, ".grain/workflows/a.yml",
		"on: push\njobs:\n  check:\n    steps:\n      - run: grain state check /state\n")
	workflow.Jobs[0].Builtin = "grain-state-check"
	plan, err := PlanRun(Request{Repo: "grain/config", Ref: "grain/task-3", Commit: "abc", Event: EventPush}, []Workflow{workflow})
	if err != nil {
		t.Fatalf("planning: %v", err)
	}

	var liveDuringExec model.Commitment
	sandboxes := &fakeSandboxes{
		script: func(cmd Command) (CommandResult, error) {
			liveDuringExec = ledger.Live()
			return CommandResult{Output: "ok\n"}, nil
		},
	}
	engine := &Engine{
		Sandboxes:   sandboxes,
		Concurrency: 1,
		Jobs:        gate,
		JobQueued: func(jobID, runID string) {
			t.Errorf("builtin job queued on Engine.Jobs (%s in %s)", jobID, runID)
		},
	}

	done := make(chan Run, 1)
	go func() {
		done <- engine.Run(context.Background(), "r1", plan, nil)
	}()
	run := awaitSignal(t, done, "builtin job blocked on a full sandbox gate")
	if run.Conclusion != ConclusionSuccess {
		t.Fatalf("run = %q (%s), want success", run.Conclusion, run.Error)
	}
	if got := run.Jobs[0].QueuedCause; got != "" {
		t.Errorf("QueuedCause = %q (%s), want no queueing", got, run.Jobs[0].QueuedReason)
	}
	if got := gate.inUse(); got != 0 {
		t.Errorf("gate.inUse() = %d, want 0", got)
	}
	if want := (model.Commitment{CPUs: 4, MemoryMB: 16 * 1024, DiskGB: 30}); liveDuringExec != want {
		t.Errorf("ledger.Live() during builtin exec = %s, want only the agent's %s", liveDuringExec, want)
	}

	// Also verify perRun (Concurrency: 1): a sandboxed job holding the run's
	// sole perRun slot in Exec does not block a builtin job in the same run,
	// nor does a builtin job in Exec hold the perRun slot against the
	// sandboxed job. Both must reach Exec concurrently under Concurrency: 1.
	twoJobWorkflow := mustParse(t, ".grain/workflows/a.yml",
		"on: push\njobs:\n  sandboxed:\n    steps:\n      - run: work\n  check:\n    steps:\n      - run: grain state check /state\n")
	for i := range twoJobWorkflow.Jobs {
		if twoJobWorkflow.Jobs[i].ID == "check" {
			twoJobWorkflow.Jobs[i].Builtin = "grain-state-check"
		}
	}
	twoJobPlan, err := PlanRun(Request{Repo: "grain/config", Ref: "grain/task-3", Commit: "def", Event: EventPush}, []Workflow{twoJobWorkflow})
	if err != nil {
		t.Fatalf("planning two-job run: %v", err)
	}
	sandboxedInExec := make(chan struct{})
	checkInExec := make(chan struct{})
	bothRunningSandboxes := &fakeSandboxes{
		script: func(cmd Command) (CommandResult, error) {
			if strings.Contains(cmd.Script, "grain state check") {
				close(checkInExec)
				awaitSignal(t, sandboxedInExec, "sandboxed job never reached Exec while builtin job was running under Concurrency: 1")
			} else {
				close(sandboxedInExec)
				awaitSignal(t, checkInExec, "builtin job never reached Exec while sandboxed job held Concurrency: 1")
			}
			return CommandResult{Output: "ok\n"}, nil
		},
	}
	perRunEngine := &Engine{
		Sandboxes:   bothRunningSandboxes,
		Concurrency: 1,
		Jobs:        &SharedGate{Limit: func() int { return 1 }, Poll: time.Millisecond},
	}
	twoJobDone := make(chan Run, 1)
	go func() {
		twoJobDone <- perRunEngine.Run(context.Background(), "r2", twoJobPlan, nil)
	}()
	twoJobRun := awaitSignal(t, twoJobDone, "two-job run with builtin + sandboxed under Concurrency: 1 and SharedGate Limit: 1 never finished")
	if twoJobRun.Conclusion != ConclusionSuccess {
		t.Fatalf("two-job run = %q (%s), want success", twoJobRun.Conclusion, twoJobRun.Error)
	}
}
