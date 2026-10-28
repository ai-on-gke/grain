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

package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func scheduledTasksWithTag(t *testing.T, store *model.Store, tag string) []model.Task {
	t.Helper()
	all, err := store.ListTasks(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var out []model.Task
	for _, tk := range all {
		for _, got := range tk.Tags {
			if got == tag {
				out = append(out, tk)
			}
		}
	}
	return out
}

func dispatchTask(t *testing.T, store *model.Store, taskID string, clock time.Time) (*dispatch.Dispatch, time.Time) {
	t.Helper()
	ctx := t.Context()
	for i := 0; i < 10; i++ {
		clock = clock.Add(time.Minute)
		ds, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 10}, clock)
		if err != nil {
			t.Fatalf("dispatch.Cycle: %v", err)
		}
		for _, d := range ds {
			if d.TaskID == taskID {
				dCopy := d
				return &dCopy, clock
			}
		}
	}
	t.Fatalf("task %s was never dispatched", taskID)
	return nil, clock
}

func TestScheduleAutoApproveProposalsWalkthrough(t *testing.T) {
	t.Parallel()
	rig := newProposalRig(t)
	storeDir := t.TempDir()
	clock := baseTime

	withStore(t, storeDir, func(store *model.Store, ctx context.Context) {
		client := ui.NewClient(ui.Config{Actor: ui.DefaultActor("test-operator")}, store)
		deps := orchestrator.Deps{Store: store, Client: rig.client}

		// =============================================================
		// Step 1: Schedule WITH auto_approve_proposals: true (without auto_merge)
		// =============================================================
		schedAuto := model.Schedule{
			ID:                   "sched-auto-approve",
			Title:                "Nightly audit with auto-approve",
			Body:                 "Run security audit and file follow-ups directly",
			Target:               rig.target,
			Recurrence:           model.Recurrence{Kind: model.RecurrenceEveryNHours, EveryNHours: 24},
			Enabled:              true,
			NextRunAt:            clock.Add(-time.Minute),
			AutoApproveProposals: true,
			AutoMerge:            false,
			CreatedAt:            clock,
		}
		if err := store.PutSchedule(ctx, schedAuto); err != nil {
			t.Fatalf("filing auto-approve schedule: %v", err)
		}

		// Fire schedule via orchestrator cycle
		if err := orchestrator.RunCycle(ctx, deps, clock); err != nil {
			t.Fatalf("RunCycle schedule fire: %v", err)
		}

		firedTasks := scheduledTasksWithTag(t, store, "schedule:"+schedAuto.ID)
		if len(firedTasks) != 1 {
			t.Fatalf("expected 1 task fired for sched-auto-approve, got %d", len(firedTasks))
		}
		parentAuto := firedTasks[0]
		if !parentAuto.AutoApproveProposals {
			t.Fatalf("parent task AutoApproveProposals = false, want true")
		}
		if parentAuto.Approval == nil {
			t.Fatalf("parent task Approval = nil, want approved")
		}

		// Dispatch and run parent task
		parentDispatch, nextClock := dispatchTask(t, store, parentAuto.ID, clock)
		clock = nextClock

		parentBranch := model.BranchName(parentAuto.ID)
		w := &world{t: t, store: store, ctx: ctx, roots: map[string]string{parentDispatch.RunID: rig.credentialedRoot()}}
		scriptAuto := []antigravity.Step{
			toolCall("run_command", map[string]any{
				"command": "git clone " + rig.remote + " work && cd work && git checkout -b " + parentBranch + " && echo 'audit' >> NOTES.md && git add NOTES.md && git commit -q -m 'audit' && git push origin " + parentBranch,
			}),
			toolCall("propose_task", map[string]any{"id": "fix-1", "title": "Auto follow-up 1: sanitize inputs", "body": "fix input sanitization"}),
			toolCall("propose_task", map[string]any{"id": "fix-2", "title": "Auto follow-up 2: update tls config", "body": "modernize cipher suites"}),
			finalText("audit completed and proposed 2 follow-ups"),
		}
		resultAuto := w.runDispatch(*parentDispatch, scriptAuto, clock)
		if !pushedOK(resultAuto) {
			t.Fatalf("parent run did not push cleanly: %+v", resultAuto.ToolCalls)
		}

		clock = clock.Add(time.Minute)
		pTask, err := store.GetTask(ctx, parentAuto.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := orchestrator.ProcessResult(ctx, store, rig.client, *pTask, resultAuto, parentDispatch.RunID, clock); err != nil {
			t.Fatalf("ProcessResult: %v", err)
		}

		// Assert proposed tasks are immediately approved and queued in DB and UI
		props := proposedBy(w, parentAuto.ID)
		if len(props) != 2 {
			t.Fatalf("expected 2 proposed tasks, got %d", len(props))
		}
		for title, pt := range props {
			if pt.Approval == nil || pt.Approval.Actor != model.AutoSubmitPrincipal {
				t.Fatalf("task %q approval = %+v, want AutoSubmitPrincipal", title, pt.Approval)
			}
			state, err := store.State(ctx, pt.ID)
			if err != nil {
				t.Fatal(err)
			}
			if state != model.StateQueued {
				t.Fatalf("task %q state = %s, want queued", title, state)
			}

			// UI client verification
			uiTask, err := client.Task(ctx, pt.ID)
			if err != nil {
				t.Fatalf("client.Task(%s): %v", pt.ID, err)
			}
			if uiTask.State != model.StateQueued {
				t.Fatalf("uiTask.State = %s, want queued", uiTask.State)
			}
		}

		// Verify inbox does not have them in proposed state
		allTasks, err := client.ListTasks(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, tk := range allTasks {
			if strings.HasPrefix(tk.Title, "Auto follow-up") {
				if tk.State == model.StateProposed {
					t.Fatalf("task %s is StateProposed, should not be in Inbox proposals", tk.Title)
				}
			}
		}

		// =============================================================
		// Step 2: Safety Bounds - Depth limit (DefaultAutoSubmitDepth = 2)
		// =============================================================
		// To test depth progression across multiple generations of proposals:
		// Chain root (AutoMerge: true) -> Child 1 (AutoMerge: true, depth 1) -> Child 2 (AutoMerge: true, depth 2) -> Child 3 (exceeds depth 2)
		schedDepth := model.Schedule{
			ID:                   "sched-depth-test",
			Title:                "Auto-merge schedule for depth testing",
			Body:                 "Test proposal depth bounds",
			Target:               rig.target,
			Recurrence:           model.Recurrence{Kind: model.RecurrenceEveryNHours, EveryNHours: 24},
			Enabled:              true,
			NextRunAt:            clock.Add(-time.Minute),
			AutoApproveProposals: true,
			AutoMerge:            true,
			CreatedAt:            clock,
		}
		if err := store.PutSchedule(ctx, schedDepth); err != nil {
			t.Fatal(err)
		}
		if err := orchestrator.RunCycle(ctx, deps, clock); err != nil {
			t.Fatal(err)
		}
		parentDepth := scheduledTasksWithTag(t, store, "schedule:"+schedDepth.ID)[0]

		parentDepthDispatch, nextClock := dispatchTask(t, store, parentDepth.ID, clock)
		clock = nextClock

		w.roots[parentDepthDispatch.RunID] = rig.credentialedRoot()
		parentDepthBranch := model.BranchName(parentDepth.ID)
		scriptDepth0 := []antigravity.Step{
			toolCall("run_command", map[string]any{
				"command": "git clone " + rig.remote + " work && cd work && git checkout -b " + parentDepthBranch + " && echo 'depth0' >> NOTES.md && git add NOTES.md && git commit -q -m 'depth0' && git push origin " + parentDepthBranch,
			}),
			toolCall("propose_task", map[string]any{"id": "d1", "title": "Depth child 1", "body": "level 1 work"}),
			finalText("depth 0 finished"),
		}
		resultDepth0 := w.runDispatch(*parentDepthDispatch, scriptDepth0, clock)
		clock = clock.Add(time.Minute)
		pDepth0, _ := store.GetTask(ctx, parentDepth.ID)
		if err := orchestrator.ProcessResult(ctx, store, rig.client, *pDepth0, resultDepth0, parentDepthDispatch.RunID, clock); err != nil {
			t.Fatalf("ProcessResult depth 0: %v", err)
		}

		propsD0 := proposedBy(w, parentDepth.ID)
		d1Task := propsD0["Depth child 1"]
		if d1Task.Approval == nil || d1Task.Approval.Actor != model.AutoSubmitPrincipal {
			t.Fatalf("d1Task should be auto-approved, got %+v", d1Task.Approval)
		}

		// Dispatch Child 1 (Depth 1) and propose Child 2
		d1Dispatch, nextClock := dispatchTask(t, store, d1Task.ID, clock)
		clock = nextClock

		w.roots[d1Dispatch.RunID] = rig.credentialedRoot()
		d1Branch := model.BranchName(d1Task.ID)
		scriptD1 := []antigravity.Step{
			toolCall("run_command", map[string]any{
				"command": "git clone " + rig.remote + " work && cd work && git checkout -b " + d1Branch + " && echo 'd1' >> NOTES.md && git add NOTES.md && git commit -q -m 'd1' && git push origin " + d1Branch,
			}),
			toolCall("propose_task", map[string]any{"id": "d2", "title": "Depth child 2", "body": "level 2 work"}),
			finalText("depth 1 finished"),
		}
		resultD1 := w.runDispatch(*d1Dispatch, scriptD1, clock)
		clock = clock.Add(time.Minute)
		pD1, _ := store.GetTask(ctx, d1Task.ID)
		if err := orchestrator.ProcessResult(ctx, store, rig.client, *pD1, resultD1, d1Dispatch.RunID, clock); err != nil {
			t.Fatalf("ProcessResult D1: %v", err)
		}

		propsD1 := proposedBy(w, d1Task.ID)
		d2Task := propsD1["Depth child 2"]
		// Depth 2 == DefaultAutoSubmitDepth (still within limit)
		if d2Task.Approval == nil || d2Task.Approval.Actor != model.AutoSubmitPrincipal {
			t.Fatalf("d2Task should be auto-approved, got %+v", d2Task.Approval)
		}
		d2State, _ := store.State(ctx, d2Task.ID)
		if d2State != model.StateQueued {
			t.Fatalf("d2Task state = %s, want queued", d2State)
		}

		// Dispatch Child 2 (Depth 2) and propose Child 3
		d2Dispatch, nextClock := dispatchTask(t, store, d2Task.ID, clock)
		clock = nextClock

		w.roots[d2Dispatch.RunID] = rig.credentialedRoot()
		d2Branch := model.BranchName(d2Task.ID)
		scriptD2 := []antigravity.Step{
			toolCall("run_command", map[string]any{
				"command": "git clone " + rig.remote + " work && cd work && git checkout -b " + d2Branch + " && echo 'd2' >> NOTES.md && git add NOTES.md && git commit -q -m 'd2' && git push origin " + d2Branch,
			}),
			toolCall("propose_task", map[string]any{"id": "d3", "title": "Depth child 3", "body": "level 3 work (exceeds depth)"}),
			finalText("depth 2 finished"),
		}
		resultD2 := w.runDispatch(*d2Dispatch, scriptD2, clock)
		clock = clock.Add(time.Minute)
		pD2, _ := store.GetTask(ctx, d2Task.ID)
		if err := orchestrator.ProcessResult(ctx, store, rig.client, *pD2, resultD2, d2Dispatch.RunID, clock); err != nil {
			t.Fatalf("ProcessResult D2: %v", err)
		}

		propsD2 := proposedBy(w, d2Task.ID)
		d3Task := propsD2["Depth child 3"]
		// Depth 3 exceeds DefaultAutoSubmitDepth = 2 -> must NOT be approved
		if d3Task.Approval != nil {
			t.Fatalf("d3Task should have Approval=nil, got %+v", d3Task.Approval)
		}
		d3State, _ := store.State(ctx, d3Task.ID)
		if d3State != model.StateProposed {
			t.Fatalf("d3Task state = %s, want proposed", d3State)
		}
		if !strings.Contains(d3Task.Body, "this would be 3 levels of self-approved work below the last person") {
			t.Fatalf("d3Task body missing depth limit note: %q", d3Task.Body)
		}

		// =============================================================
		// Step 3: Safety Bounds - Fan-out limit (DefaultAutoSubmitFanOut = 5)
		// =============================================================
		schedFanOut := model.Schedule{
			ID:                   "sched-fanout-test",
			Title:                "Schedule for fanout test",
			Body:                 "Test fanout limits",
			Target:               rig.target,
			Recurrence:           model.Recurrence{Kind: model.RecurrenceEveryNHours, EveryNHours: 24},
			Enabled:              true,
			NextRunAt:            clock.Add(-time.Minute),
			AutoApproveProposals: true,
			CreatedAt:            clock,
		}
		if err := store.PutSchedule(ctx, schedFanOut); err != nil {
			t.Fatal(err)
		}
		if err := orchestrator.RunCycle(ctx, deps, clock); err != nil {
			t.Fatal(err)
		}
		parentFanOut := scheduledTasksWithTag(t, store, "schedule:"+schedFanOut.ID)[0]

		fanOutDispatch, nextClock := dispatchTask(t, store, parentFanOut.ID, clock)
		clock = nextClock

		w.roots[fanOutDispatch.RunID] = rig.credentialedRoot()
		fanOutBranch := model.BranchName(parentFanOut.ID)
		scriptFanOut := []antigravity.Step{
			toolCall("run_command", map[string]any{
				"command": "git clone " + rig.remote + " work && cd work && git checkout -b " + fanOutBranch + " && echo 'fanout' >> NOTES.md && git add NOTES.md && git commit -q -m 'fanout' && git push origin " + fanOutBranch,
			}),
			toolCall("propose_task", map[string]any{"id": "fo-1", "title": "Fanout task 1", "body": "task 1"}),
			toolCall("propose_task", map[string]any{"id": "fo-2", "title": "Fanout task 2", "body": "task 2"}),
			toolCall("propose_task", map[string]any{"id": "fo-3", "title": "Fanout task 3", "body": "task 3"}),
			toolCall("propose_task", map[string]any{"id": "fo-4", "title": "Fanout task 4", "body": "task 4"}),
			toolCall("propose_task", map[string]any{"id": "fo-5", "title": "Fanout task 5", "body": "task 5"}),
			toolCall("propose_task", map[string]any{"id": "fo-6", "title": "Fanout task 6", "body": "task 6 (excess)"}),
			finalText("proposed 6 tasks"),
		}
		resultFanOut := w.runDispatch(*fanOutDispatch, scriptFanOut, clock)
		if !pushedOK(resultFanOut) {
			t.Fatalf("run did not push cleanly: %+v", resultFanOut.ToolCalls)
		}

		clock = clock.Add(time.Minute)
		pTaskFanOut, err := store.GetTask(ctx, parentFanOut.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := orchestrator.ProcessResult(ctx, store, rig.client, *pTaskFanOut, resultFanOut, fanOutDispatch.RunID, clock); err != nil {
			t.Fatalf("ProcessResult: %v", err)
		}

		propsFanOut := proposedBy(w, parentFanOut.ID)
		if len(propsFanOut) != 6 {
			t.Fatalf("expected 6 proposals, got %d", len(propsFanOut))
		}

		for i := 1; i <= 5; i++ {
			title := "Fanout task " + string(rune('0'+i))
			pt, ok := propsFanOut[title]
			if !ok {
				t.Fatalf("missing %s", title)
			}
			if pt.Approval == nil || pt.Approval.Actor != model.AutoSubmitPrincipal {
				t.Fatalf("%s should be approved, got %+v", title, pt.Approval)
			}
			state, _ := store.State(ctx, pt.ID)
			if state != model.StateQueued {
				t.Fatalf("%s state = %s, want queued", title, state)
			}
		}

		// 6th proposal must be quarantined as proposed
		excessTask := propsFanOut["Fanout task 6"]
		if excessTask.Approval != nil {
			t.Fatalf("excess task 6 should have Approval=nil, got %+v", excessTask.Approval)
		}
		excessState, _ := store.State(ctx, excessTask.ID)
		if excessState != model.StateProposed {
			t.Fatalf("excess task 6 state = %s, want proposed", excessState)
		}
		if !strings.Contains(excessTask.Body, "already had 5 proposals queued that way, which is the limit") {
			t.Fatalf("excess task 6 body missing explanatory limit note: %q", excessTask.Body)
		}

		// =============================================================
		// Step 4: Schedule WITHOUT auto_approve_proposals (false)
		// =============================================================
		schedNoAuto := model.Schedule{
			ID:                   "sched-no-auto-approve",
			Title:                "Standard scheduled sweep",
			Body:                 "Run sweep without auto-approval",
			Target:               rig.target,
			Recurrence:           model.Recurrence{Kind: model.RecurrenceEveryNHours, EveryNHours: 24},
			Enabled:              true,
			NextRunAt:            clock.Add(-time.Minute),
			AutoApproveProposals: false,
			CreatedAt:            clock,
		}
		if err := store.PutSchedule(ctx, schedNoAuto); err != nil {
			t.Fatalf("filing no-auto-approve schedule: %v", err)
		}

		if err := orchestrator.RunCycle(ctx, deps, clock); err != nil {
			t.Fatalf("RunCycle schedule fire: %v", err)
		}

		firedNoAuto := scheduledTasksWithTag(t, store, "schedule:"+schedNoAuto.ID)
		if len(firedNoAuto) != 1 {
			t.Fatalf("expected 1 task fired for sched-no-auto-approve, got %d", len(firedNoAuto))
		}
		parentNoAuto := firedNoAuto[0]
		if parentNoAuto.AutoApproveProposals {
			t.Fatalf("parent task AutoApproveProposals = true, want false")
		}

		parentNoAutoDispatch, nextClock := dispatchTask(t, store, parentNoAuto.ID, clock)
		clock = nextClock

		w.roots[parentNoAutoDispatch.RunID] = rig.credentialedRoot()
		parentNoAutoBranch := model.BranchName(parentNoAuto.ID)
		scriptNoAuto := []antigravity.Step{
			toolCall("run_command", map[string]any{
				"command": "git clone " + rig.remote + " work && cd work && git checkout -b " + parentNoAutoBranch + " && echo 'sweep' >> NOTES.md && git add NOTES.md && git commit -q -m 'sweep' && git push origin " + parentNoAutoBranch,
			}),
			toolCall("propose_task", map[string]any{"id": "manual-1", "title": "Manual review proposal: refactor auth", "body": "requires review"}),
			finalText("sweep completed and proposed 1 manual follow-up"),
		}
		resultNoAuto := w.runDispatch(*parentNoAutoDispatch, scriptNoAuto, clock)
		if !pushedOK(resultNoAuto) {
			t.Fatalf("run did not push cleanly: %+v", resultNoAuto.ToolCalls)
		}

		clock = clock.Add(time.Minute)
		pTaskNoAuto, err := store.GetTask(ctx, parentNoAuto.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := orchestrator.ProcessResult(ctx, store, rig.client, *pTaskNoAuto, resultNoAuto, parentNoAutoDispatch.RunID, clock); err != nil {
			t.Fatalf("ProcessResult: %v", err)
		}

		propsNoAuto := proposedBy(w, parentNoAuto.ID)
		if len(propsNoAuto) != 1 {
			t.Fatalf("expected 1 proposal for parentNoAuto, got %d", len(propsNoAuto))
		}
		for title, pt := range propsNoAuto {
			if pt.Approval != nil {
				t.Fatalf("task %q approval = %+v, want nil", title, pt.Approval)
			}
			state, err := store.State(ctx, pt.ID)
			if err != nil {
				t.Fatal(err)
			}
			if state != model.StateProposed {
				t.Fatalf("task %q state = %s, want proposed", title, state)
			}

			// UI client verification: matches Inbox proposals match filter (state === "proposed")
			uiTask, err := client.Task(ctx, pt.ID)
			if err != nil {
				t.Fatalf("client.Task(%s): %v", pt.ID, err)
			}
			if uiTask.State != model.StateProposed {
				t.Fatalf("uiTask.State = %s, want proposed", uiTask.State)
			}
		}

		// =============================================================
		// Step 5: Suite WITH auto_approve_proposals: true
		// =============================================================
		tmpl := model.Template{
			ID:    "tmpl-audit",
			Name:  "Audit template",
			Title: "Suite audit step",
			Body:  "Perform suite audit",
		}
		if err := store.PutTemplate(ctx, tmpl); err != nil {
			t.Fatal(err)
		}
		suite := model.Suite{
			ID:                   "suite-auto-audit",
			Name:                 "Automated Security Suite",
			Mode:                 model.SuiteCount,
			Count:                1,
			RequireApproval:      false,
			AutoMerge:            false,
			AutoApproveProposals: true,
			Items:                []model.SuiteItem{{TemplateID: tmpl.ID}},
			CreatedAt:            clock,
		}
		if err := store.PutSuite(ctx, suite); err != nil {
			t.Fatal(err)
		}
		run, err := store.CreateSuiteRun(ctx, suite, rig.target, "", clock)
		if err != nil {
			t.Fatalf("CreateSuiteRun: %v", err)
		}
		if !run.AutoApproveProposals {
			t.Fatalf("suite run AutoApproveProposals = false, want true")
		}

		hydratedRun, err := store.GetSuiteRun(ctx, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(hydratedRun.Tasks) != 1 {
			t.Fatalf("expected 1 suite task, got %d", len(hydratedRun.Tasks))
		}
		suiteTaskID := hydratedRun.Tasks[0].TaskID
		suiteTask, err := store.GetTask(ctx, suiteTaskID)
		if err != nil || suiteTask == nil {
			t.Fatalf("GetTask(%s): %v", suiteTaskID, err)
		}
		if !suiteTask.AutoApproveProposals {
			t.Fatalf("suite task AutoApproveProposals = false, want true")
		}

		suiteDispatch, nextClock := dispatchTask(t, store, suiteTask.ID, clock)
		clock = nextClock

		w.roots[suiteDispatch.RunID] = rig.credentialedRoot()
		suiteBranch := model.BranchName(suiteTask.ID)
		scriptSuite := []antigravity.Step{
			toolCall("run_command", map[string]any{
				"command": "git clone " + rig.remote + " work && cd work && git checkout -b " + suiteBranch + " && echo 'suite' >> NOTES.md && git add NOTES.md && git commit -q -m 'suite' && git push origin " + suiteBranch,
			}),
			toolCall("propose_task", map[string]any{"id": "st-1", "title": "Suite follow-up: rotate keys", "body": "rotate keys"}),
			finalText("suite task finished"),
		}
		resultSuite := w.runDispatch(*suiteDispatch, scriptSuite, clock)
		clock = clock.Add(time.Minute)
		pSuite, _ := store.GetTask(ctx, suiteTask.ID)
		if err := orchestrator.ProcessResult(ctx, store, rig.client, *pSuite, resultSuite, suiteDispatch.RunID, clock); err != nil {
			t.Fatalf("ProcessResult suite: %v", err)
		}

		propsSuite := proposedBy(w, suiteTask.ID)
		suiteFollowUp, ok := propsSuite["Suite follow-up: rotate keys"]
		if !ok {
			t.Fatalf("missing suite follow-up proposal")
		}
		if suiteFollowUp.Approval == nil || suiteFollowUp.Approval.Actor != model.AutoSubmitPrincipal {
			t.Fatalf("suite follow-up should be auto-approved, got %+v", suiteFollowUp.Approval)
		}
		sfState, _ := store.State(ctx, suiteFollowUp.ID)
		if sfState != model.StateQueued {
			t.Fatalf("suite follow-up state = %s, want queued", sfState)
		}
	})
}
