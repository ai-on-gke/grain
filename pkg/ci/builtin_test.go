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
