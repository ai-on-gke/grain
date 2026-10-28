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

package orchestrator

// The cadence half of a scheduled workflow, checked against this
// repository's own `.grain/workflows` rather than against a tree a test
// wrote.
//
// scheduledWorkflowDeadline's doc comment claims its length is "far beyond
// any suite". That is a claim about files in a different package, in a
// different directory, that nothing compiles together -- so it is true
// until somebody raises a `timeout-minutes` and nothing tells them. These
// are the two bounds that make a nightly work, asserted where the constant
// and the workflow files can be read in the same breath.

import (
	"os"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
)

// repoRoot is this package's own directory, two levels down.
const repoRoot = "../.."

// TestThisRepositoryScheduledWorkflowsFitInsideTheirOwnCadence is the check
// a person would have to make by hand, and would not.
//
// A firing of a workflow-review schedule is a run and a report, and it is
// not over until the report is filed: nothing fires again until it is
// (model.Store.workflowFiringInFlight, which is also why the translation
// hard-codes OverlapSkip). Two lengths therefore have to sit inside each
// other, and both are written down in places that do not know about each
// other:
//
//   - Every job in a scheduled workflow has to be able to finish inside
//     scheduledWorkflowDeadline. Past it grain stops waiting and files the
//     review anyway, saying the run never ended -- which for a job that is
//     merely slow is a false report of a hang, made every single night, and
//     one that also abandons the rest of the suite's verdict.
//   - The deadline has to sit inside the cadence's own interval. It is the
//     backstop for a run nothing concluded, and a backstop that outlasts the
//     gap between firings is not one: the next firing comes due while the
//     last is still unreviewed, overlap suppresses it, and the suite stops
//     running with nothing anywhere saying so -- the one failure the review
//     exists to report being the one it cannot.
//
// The job bound is the sum rather than the maximum, on purpose. Jobs get a
// sandbox each and this deployment admits a bounded number of them at once
// (pkg/ci's admission), so a busy night really can serialize a suite, and
// the length grain must not give up inside is the one where every job took
// everything it was allowed.
func TestThisRepositoryScheduledWorkflowsFitInsideTheirOwnCadence(t *testing.T) {
	workflows, err := ci.LoadNativeDir(os.DirFS(repoRoot))
	if err != nil {
		t.Fatalf("this repository's own %s do not load:\n%v", ci.WorkflowDir, err)
	}
	scheduled := 0
	for _, workflow := range workflows {
		if len(workflow.On.Schedule) == 0 {
			continue
		}
		scheduled++

		var total time.Duration
		for _, job := range workflow.Jobs {
			if job.Timeout() > scheduledWorkflowDeadline {
				t.Errorf("%s: job %q may run for %s, and grain gives a whole scheduled run %s "+
					"before it stops waiting and reports a night that never ended -- so this job "+
					"timing out is a hang grain reports every night and never has",
					workflow.Path, job.ID, job.Timeout(), scheduledWorkflowDeadline)
			}
			total += job.Timeout()
		}
		if total > scheduledWorkflowDeadline {
			t.Errorf("%s: its %d jobs may run for %s between them, which is longer than the %s grain "+
				"waits for the run -- a night on which they serialize is abandoned unreviewed",
				workflow.Path, len(workflow.Jobs), total, scheduledWorkflowDeadline)
		}

		for _, cron := range workflow.On.Schedule {
			interval, err := shortestInterval(cron.Recurrence)
			if err != nil {
				t.Errorf("%s: cron %q: %v", workflow.Path, cron.Cron, err)
				continue
			}
			if scheduledWorkflowDeadline >= interval {
				t.Errorf("%s: cron %q fires every %s, and grain waits %s for a firing's run before "+
					"giving up on it -- so a run nothing concludes is still unreviewed when the next "+
					"firing is due, and overlap suppresses that one and every one after it",
					workflow.Path, cron.Cron, interval, scheduledWorkflowDeadline)
			}
		}
	}
	if scheduled == 0 {
		t.Fatalf("no workflow in %s declares `on: schedule`, so this asserted nothing -- either the "+
			"nightly's cron has gone or the wrong tree was read", ci.WorkflowDir)
	}
}
