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

package orchestrator_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// A nightly on trunk is a release gate (docs/design/release-from-trunk.md,
// RFT-2): its run asks for PriorityReleaseGate, and a night that ends
// without a verdict about the tree is re-run before anybody reviews it.

// spendInfraRetries drives a release-gate firing whose run keeps ending
// without a verdict (end is how each one ends) through every automatic
// re-run it gets, checking that each re-run is uncached, ranked as a
// release gate and counted on its requester, and that nothing is
// reviewed while they last. It returns the time of the last cycle and
// the id of the last run, which end has already been applied to; the
// next cycle is the one that files the review.
func spendInfraRetries(t *testing.T, ctx context.Context, deps orchestrator.Deps, runner *fakeCI,
	at time.Time, end func(id string)) (time.Time, string) {

	t.Helper()
	id := "run1"
	for n := 1; n <= orchestrator.MaxInfraRetries; n++ {
		end(id)
		at = at.Add(time.Hour)
		if err := orchestrator.RunCycle(ctx, deps, at); err != nil {
			t.Fatalf("RunCycle (re-run %d): %v", n, err)
		}
		req := runner.lastRequest(t)
		if !req.Recheck || req.Priority != ci.PriorityReleaseGate ||
			!strings.HasSuffix(req.RequestedBy, fmt.Sprintf("#infra-retry-%d", n)) {
			t.Fatalf("re-run %d asked for %+v, want an uncached release-gate re-run counted on its requester", n, req)
		}
		if filed := scheduledTasksTagged(t, deps.Store, "schedule:"+workflowScheduleID()); len(filed) != 0 {
			t.Fatalf("re-run %d: %d review(s) filed while the night was still being re-run", n, len(filed))
		}
		id = fmt.Sprintf("run%d", n+1)
	}
	end(id)
	return at, id
}

func TestANightlyOnTrunkAsksForReleaseGatePriority(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	workflowSchedule(t, store, baseTime.Add(-time.Minute))
	runner := newFakeCI()
	if err := orchestrator.RunCycle(ctx, workflowDeps(store, runner), baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if got := runner.lastRequest(t).Priority; got != ci.PriorityReleaseGate {
		t.Fatalf("the nightly on main asked for priority %d, want PriorityReleaseGate (%d)", got, ci.PriorityReleaseGate)
	}
}

// The re-run is the jobs that did not pass and only them, on the commit
// the night was judging, after the retry delay and not before -- and one
// that passes settles the night green with no review at all.
func TestACancelledNightlyIsReRunSelectivelyAndAGreenReRunNeedsNoReview(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sched := workflowSchedule(t, store, baseTime.Add(-time.Minute))
	runner := newFakeCI()
	deps := workflowDeps(store, runner)
	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	finished := baseTime.Add(30 * time.Minute)
	runner.mu.Lock()
	run := runner.runs["run1"]
	run.Commit = "abc123"
	run.Conclusion = ci.ConclusionCancelled
	run.FinishedAt = &finished
	run.Jobs = []ci.JobRun{
		{WorkflowPath: ci.WorkflowDir + "/nightly.yml", JobID: "go-long", Conclusion: ci.ConclusionSuccess},
		{WorkflowPath: ci.WorkflowDir + "/nightly.yml", JobID: "loadtest", Conclusion: ci.ConclusionCancelled},
	}
	runner.runs["run1"] = run
	runner.mu.Unlock()

	// Within the delay: nothing is started and nothing is reviewed.
	if err := orchestrator.RunCycle(ctx, deps, finished.Add(time.Minute)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if n := runner.started(); n != 1 {
		t.Fatalf("%d runs started inside the retry delay, want only the night's own", n)
	}
	if filed := scheduledTasksTagged(t, store, "schedule:"+sched.ID); len(filed) != 0 {
		t.Fatalf("%d review(s) filed for a night that is still to be re-run", len(filed))
	}

	if err := orchestrator.RunCycle(ctx, deps, finished.Add(10*time.Minute)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	req := runner.lastRequest(t)
	if want := []string{ci.WorkflowDir + "/nightly.yml:loadtest"}; !slices.Equal(req.Jobs, want) ||
		!req.Recheck || req.Commit != "abc123" || req.Workflow != ci.WorkflowDir+"/nightly.yml" {
		t.Fatalf("the re-run asked for %+v, want jobs %v uncached at abc123 in the nightly workflow", req, want)
	}
	row, err := store.GetScheduledWorkflowRun(ctx, sched.ID)
	if err != nil || row == nil || row.RunID != "run2" || row.Conclusion != "" {
		t.Fatalf("row = %+v, %v, want it following the re-run with no conclusion yet", row, err)
	}

	runner.finish("run2", ci.ConclusionSuccess, ci.JobRun{JobID: "loadtest", Conclusion: ci.ConclusionSuccess})
	if err := orchestrator.RunCycle(ctx, deps, finished.Add(time.Hour)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if filed := scheduledTasksTagged(t, store, "schedule:"+sched.ID); len(filed) != 0 {
		t.Fatalf("%d review(s) filed for a night whose re-run passed, want none", len(filed))
	}
	if row, err = store.GetScheduledWorkflowRun(ctx, sched.ID); err != nil || row == nil || !row.ReviewNotNeeded {
		t.Fatalf("row = %+v, %v, want the night settled green", row, err)
	}
}

// A test failure is the tree's: it is reviewed at once, with no re-run.
func TestAFailingNightlyIsReviewedWithoutAReRun(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sched := workflowSchedule(t, store, baseTime.Add(-time.Minute))
	runner := newFakeCI()
	deps := workflowDeps(store, runner)
	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	runner.finish("run1", ci.ConclusionFailure, ci.JobRun{JobID: "go-long", Conclusion: ci.ConclusionFailure})
	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(time.Hour)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if n := runner.started(); n != 1 {
		t.Fatalf("%d runs started for a night that failed a test, want no re-run", n)
	}
	if filed := scheduledTasksTagged(t, store, "schedule:"+sched.ID); len(filed) != 1 {
		t.Fatalf("filed %d reviews, want 1", len(filed))
	}
}

// Off trunk a nightly is not a release gate: ordinary priority, and a
// cancelled night is reviewed at once as it always was.
func TestANightlyOffTrunkIsNeitherPrioritisedNorReRun(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sched := workflowSchedule(t, store, baseTime.Add(-time.Minute))
	sched.Base = "candidate"
	if err := store.PutSchedule(ctx, sched); err != nil {
		t.Fatal(err)
	}
	runner := newFakeCI()
	deps := workflowDeps(store, runner)
	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	req := runner.lastRequest(t)
	if req.Ref != "candidate" || req.Priority != ci.PriorityNormal {
		t.Fatalf("asked for %+v, want candidate at ordinary priority", req)
	}
	runner.finish("run1", ci.ConclusionCancelled, ci.JobRun{JobID: "go-long", Conclusion: ci.ConclusionCancelled})
	if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(time.Hour)); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if n := runner.started(); n != 1 {
		t.Fatalf("%d runs started, want no re-run off trunk", n)
	}
	if filed := scheduledTasksTagged(t, store, "schedule:"+sched.ID); len(filed) != 1 {
		t.Fatalf("filed %d reviews, want 1", len(filed))
	}
}
