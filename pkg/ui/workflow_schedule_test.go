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

package ui_test

// A schedule translated out of a workflow's `on: schedule` cron is a
// declared row like any other, so the surface an operator reaches it
// through refuses to edit it and names the file to change instead
// (grain/task-344, Client.refuseDeclaredEdit). The point of the test is
// that the file it names is the *workflow* -- which is where the cadence
// actually is, and is not a path anybody would guess from the row.

import (
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

const workflowFile = ".grain/workflows/nightly.yml"

func declaredWorkflowSchedule(t *testing.T, store *model.Store) model.Schedule {
	t.Helper()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	templateID := model.WorkflowReviewTemplateID("nightly")
	sched := model.Schedule{
		ID:         model.RepoDeclaredID(model.DeclaredSchedule, repo, "workflow.nightly"),
		Title:      "Review last night's \"nightly\" workflow run",
		Target:     repo,
		TemplateID: &templateID,
		Recurrence: model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 2*60 + 11},
		Enabled:    true,
	}
	if _, err := store.SyncRepoDeclarations(t.Context(), repo, "abc123",
		model.RepoDeclarations{Schedules: []model.DeclaredScheduleFile{{
			Path: workflowFile, Schedule: sched,
		}}}, baseTime, time.UTC); err != nil {
		t.Fatalf("declaring the schedule: %v", err)
	}
	return sched
}

func TestAWorkflowCronScheduleIsNotEditableAndNamesTheWorkflowFile(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	sched := declaredWorkflowSchedule(t, store)

	title := "Something else"
	_, err := c.UpdateSchedule(ctx, sched.ID, ui.UpdateScheduleRequest{Title: &title})
	if err == nil {
		t.Fatal("edited a schedule a workflow file declares")
	}
	for _, want := range []string{sched.ID, "acme/widgets", workflowFile, "not this deployment's to edit"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not say %q: %v", want, err)
		}
	}

	if err := c.DeleteSchedule(ctx, sched.ID); err == nil {
		t.Fatal("deleted a schedule a workflow file declares")
	} else if !strings.Contains(err.Error(), workflowFile) {
		t.Errorf("delete refusal does not name the workflow file: %v", err)
	}
}

// And the row itself says where it came from, so a UI can show it as the
// repository's rather than as this deployment's.
func TestAWorkflowCronScheduleIsShownAsDeclared(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	sched := declaredWorkflowSchedule(t, store)

	listed, err := c.ListSchedules(ctx)
	if err != nil {
		t.Fatalf("listing schedules: %v", err)
	}
	for _, got := range listed {
		if got.ID != sched.ID {
			continue
		}
		if got.DeclaredIn == nil {
			t.Fatalf("schedule %s is not shown as declared", got.ID)
		}
		if got.DeclaredIn.Path != workflowFile || got.DeclaredIn.Repo != "acme/widgets" {
			t.Fatalf("declaredIn = %+v, want the workflow file", got.DeclaredIn)
		}
		return
	}
	t.Fatalf("schedule %s is not in the list at all", sched.ID)
}

// "Run now" on one of these fires the same two acts the cadence does
// (grain/task-351): it records a firing for grain to start a CI run for,
// and files nothing yet -- the task that reviews the run is filed when
// that run has a verdict to review. So there is no task id to hand back,
// and the caller is told what did happen rather than left to read "no
// task" as "nothing".
func TestRunningAWorkflowScheduleNowRecordsARunRatherThanFilingATask(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	sched := declaredWorkflowSchedule(t, store)

	run, err := c.RunScheduleNow(ctx, sched.ID)
	if err != nil {
		t.Fatalf("running now: %v", err)
	}
	if !run.WorkflowRun {
		t.Error("workflowRun = false, want the caller told its firing is a CI run")
	}
	if run.TaskID != "" || run.SuiteRunID != 0 {
		t.Errorf("run = %+v, want neither a task nor a suite run", run)
	}
	row, err := store.GetScheduledWorkflowRun(ctx, sched.ID)
	if err != nil || row == nil {
		t.Fatalf("GetScheduledWorkflowRun = %v, %v, want the firing recorded", row, err)
	}
	if row.Stem != "nightly" || row.Repo.Name != "widgets" || row.Settled() {
		t.Fatalf("row = %+v, want an unreviewed firing of the nightly workflow", row)
	}

	// And the firing it has in hand is what stops a second one: the
	// button is no way round the overlap rule.
	if _, err := c.RunScheduleNow(ctx, sched.ID); err == nil {
		t.Fatal("fired a second time over an unfinished firing")
	} else if !strings.Contains(err.Error(), "has not finished") {
		t.Errorf("refusal = %v, want it to say the previous firing is unfinished", err)
	} else if strings.Contains(err.Error(), "If the last run has not finished") {
		// A workflow schedule cannot be set to overlap (repodecl refuses
		// `overlap: allow` on one), so pointing at that setting sends the
		// caller to a way out that does not exist
		// (docs/test-runs/2026-10-14-workflow-schedules-candidate.md, F2).
		t.Errorf("refusal = %v, which offers an overlap setting a workflow schedule cannot have", err)
	}
}

// A green night files no task (grain/task-3160), so the schedule is where
// it shows: the list carries the last firing's run, how it ended, and
// that no review was needed -- and says what the schedule's review policy
// is.
func TestAWorkflowScheduleShowsItsLastFiringsRun(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	sched := declaredWorkflowSchedule(t, store)
	finished := baseTime.Add(time.Hour)
	if err := store.PutScheduledWorkflowRun(ctx, model.ScheduledWorkflowRun{
		ScheduleID: sched.ID, Stem: "nightly", Repo: sched.Target, Branch: "candidate",
		Workflow: workflowFile, RunID: "run-7", FiredAt: baseTime, FinishedAt: &finished,
		Conclusion: "success", ReviewNotNeeded: true,
	}); err != nil {
		t.Fatalf("recording the firing: %v", err)
	}

	listed, err := c.ListSchedules(ctx)
	if err != nil {
		t.Fatalf("listing schedules: %v", err)
	}
	for _, got := range listed {
		if got.ID != sched.ID {
			continue
		}
		last := got.LastWorkflowRun
		if last == nil || last.RunID != "run-7" || last.Branch != "candidate" ||
			last.Conclusion != "success" || !last.ReviewNotNeeded || last.ReviewTaskID != "" {
			t.Fatalf("lastWorkflowRun = %+v, want the green run with no review", last)
		}
		if got.WorkflowReview != string(model.WorkflowReviewOnFailure) {
			t.Errorf("workflowReview = %q, want the default on-failure", got.WorkflowReview)
		}
		return
	}
	t.Fatalf("schedule %s is not in the list at all", sched.ID)
}
