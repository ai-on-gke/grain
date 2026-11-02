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

package engine

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gritsteps "github.com/ai-on-gke/grain/pkg/grit/steps"
	"github.com/ai-on-gke/grain/pkg/sandboxaccess"
	"github.com/ai-on-gke/grain/pkg/workflows"
)

// A step whose sandbox channel stays down past the reattach timeout ends
// the job errored with an "infra: sandbox channel lost" fault -- not a
// failure charged to the tree -- and the steps after it do not run
// (design/design/grain-on-gke.design.md#GKE-3).
func TestAStepThatLosesItsSandboxChannelConcludesTheJobErroredWithAnInfraFault(t *testing.T) {
	var laterRan atomic.Bool
	sb := &fakeSandboxes{
		acquireFn: func(ctx context.Context, req gritsteps.SandboxRequest) (gritsteps.Sandbox, error) {
			return &fakeSandbox{
				name: "sb-1",
				execFn: func(ctx context.Context, cmd gritsteps.Command) (gritsteps.CommandResult, error) {
					if cmd.Script == "later" {
						laterRan.Store(true)
						return gritsteps.CommandResult{}, nil
					}
					return gritsteps.CommandResult{ExitCode: -1, Output: "half of the output\n"},
						&sandboxaccess.ChannelLostError{Sandbox: "sb-1", Down: 2 * time.Minute}
				},
			}, nil
		},
	}
	eng := &Engine{Sandboxes: sb, ConcurrencyLimit: func() int { return 1 }}
	plan := workflows.Plan{
		Key:     "plan-channel-lost",
		Request: workflows.Request{Repo: "ai-on-gke/grain", Ref: "main", Commit: "abc123", Event: workflows.EventPush},
		Jobs: []workflows.PlannedJob{{
			ID: "test", Workflow: "ci", WorkflowPath: ".grain/workflows/ci.yml", Key: "key-test",
			Job: workflows.Job{ID: "test", Name: "test", Steps: []workflows.Step{
				{Name: "test", Run: "go test ./..."},
				{Name: "later", Run: "later"},
			}},
		}},
	}

	run := eng.Run(context.Background(), "run-1", plan, nil)
	if run.Conclusion != workflows.ConclusionErrored {
		t.Fatalf("run conclusion = %q, want errored: nothing judged the tree", run.Conclusion)
	}
	if len(run.Jobs) != 1 {
		t.Fatalf("got %d jobs, want 1", len(run.Jobs))
	}
	job := run.Jobs[0]
	if job.Conclusion != workflows.ConclusionErrored || !strings.HasPrefix(job.Error, ChannelLostFaultPrefix) {
		t.Fatalf("job = (Conclusion=%q, Error=%q), want errored with %q", job.Conclusion, job.Error, ChannelLostFaultPrefix)
	}
	if laterRan.Load() {
		t.Fatal("a step after the lost channel ran")
	}
}
