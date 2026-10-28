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
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// The tests above set NothingNeededAction on a task directly. These
// follow the setting the way the 2026-10-04 §9 walkthrough does (grain/
// task-1835): picked on a template, carried to a task by a schedule
// firing over it or by a suite composing it, and read by the run that
// ends on nothing_needed. The UI half -- the picker, Run now, the filed
// task's own field -- is walked in ui/e2e/ui-followups-walkthrough.spec.js;
// `grain demo` has no orchestrator to end a run, so the ending is here.

// endWithNothingNeeded runs the filed task once and ends that run on
// nothing_needed, returning the state ProcessResult leaves it in.
func endWithNothingNeeded(t *testing.T, ctx context.Context, store *model.Store, task model.Task) model.State {
	t.Helper()
	_, client := newSim(t, "acme", "widgets", "main")
	runID := task.ID + "-1"
	if err := store.StartRun(ctx, model.Run{
		ID: runID, TaskID: task.ID, Sandbox: "s1", Attempt: 1, StartedAt: baseTime,
	}, model.Limits{}); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if err := store.FinishRun(ctx, runID, baseTime, "succeeded", ""); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	result := toolResult(nothingNeededCall("already done on main"))
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, runID, baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	return taskState(t, ctx, store, task.ID)
}

var nothingNeededEndings = map[model.NothingNeededAction]model.State{
	model.NothingNeededAck:   model.StateAwaitingReply,
	model.NothingNeededClose: model.StateClosed,
}

func TestATemplatesNothingNeededActionDecidesTheEndingOfTheTaskItsScheduleFiles(t *testing.T) {
	t.Parallel()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	for action, want := range nothingNeededEndings {
		t.Run(string(action), func(t *testing.T) {
			t.Parallel()
			store, ctx := openStore(t)
			templateID := "template-" + string(action)
			if err := store.PutTemplate(ctx, model.Template{
				ID: templateID, Name: "sweep-" + string(action), Title: "Weekly sweep",
				CreatedAt: baseTime, NothingNeededAction: action,
			}); err != nil {
				t.Fatalf("PutTemplate: %v", err)
			}
			sched := model.Schedule{
				ID: "sched-" + string(action), Title: "Weekly sweep", Target: repo,
				TemplateID: &templateID,
				Recurrence: model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 9 * 60},
				Enabled:    true, NextRunAt: baseTime, CreatedAt: baseTime.Add(-24 * time.Hour),
			}
			if err := store.PutSchedule(ctx, sched); err != nil {
				t.Fatalf("PutSchedule: %v", err)
			}
			firing, err := store.FireSchedule(ctx, sched, baseTime, time.UTC, model.RepoRef{})
			if err != nil || firing.TaskID == "" {
				t.Fatalf("FireSchedule: (%+v, %v), want a filed task", firing, err)
			}
			task, err := store.GetTask(ctx, firing.TaskID)
			if err != nil || task == nil {
				t.Fatalf("GetTask(%s): (%+v, %v)", firing.TaskID, task, err)
			}
			if task.NothingNeededAction != action {
				t.Fatalf("filed task's NothingNeededAction = %q, want the template's %q", task.NothingNeededAction, action)
			}
			if st := endWithNothingNeeded(t, ctx, store, *task); st != want {
				t.Fatalf("after nothing_needed the task is %q, want %q", st, want)
			}
		})
	}
}

func TestATemplatesNothingNeededActionDecidesTheEndingOfTheTaskItsSuiteFiles(t *testing.T) {
	t.Parallel()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	for action, want := range nothingNeededEndings {
		t.Run(string(action), func(t *testing.T) {
			t.Parallel()
			store, ctx := openStore(t)
			tmpl := model.Template{ID: "template-1", Title: "Sweep", NothingNeededAction: action}
			comp := model.ComposeSuiteTask(tmpl, repo, "main", false, false, false)
			task := comp.Task("suite-task-"+string(action), model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}, "", baseTime)
			if err := store.PutTask(ctx, task); err != nil {
				t.Fatalf("PutTask: %v", err)
			}
			if st := endWithNothingNeeded(t, ctx, store, task); st != want {
				t.Fatalf("after nothing_needed the suite's task is %q, want %q", st, want)
			}
		})
	}
}
