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

import (
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// A gate override is for one commit. Once the branch has moved on (a new
// head pushed, the outstanding force-merge stamp withdrawn by the merge
// queue), the earlier override must neither show its gates as
// "overridden" nor let a later override at the new head skip a review gate
// it does not name (docs/test-runs/2026-11-11-merge-gates.md, finding F1).
func TestAGateOverrideForAnEarlierHeadSkipsNothingAtTheNewHead(t *testing.T) {
	t.Parallel()
	const headA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const headB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	c, store, ctx := testClient(t)
	c.Config.GitHubPullRequests = prHead(headA)
	task := create(t, c, ctx)
	if err := store.UpdateTask(ctx, task.ID, func(tk *model.Task) error {
		tk.AutoMerge = true
		tk.Links = append(tk.Links,
			model.Link{Kind: model.LinkFixes, Target: "acme/widgets#77"},
			model.Link{Kind: model.LinkSecurityHold, Target: "trust-boundary/v5"},
			model.Link{Kind: model.LinkSecurityReviewTask, Target: "901"},
		)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	done := time.Date(2026, 11, 11, 1, 0, 0, 0, time.UTC)
	if err := store.Observe(ctx, model.Observation{
		TaskID: task.ID, CompletedAt: &done,
		CI: &model.CIStatus{RunID: "run-1", Commit: headA, Conclusion: "success", FinishedAt: &done},
	}); err != nil {
		t.Fatal(err)
	}

	if err := c.ForceMergeWithGates(ctx, task.ID, "rollback at A", headA, []string{"security-review"}); err != nil {
		t.Fatalf("override at head A: %v", err)
	}

	// The branch moves on: a new head is pushed and the merge queue
	// withdraws the stamp for A (orchestrator.dropMovedForcedMerge).
	c.Config.GitHubPullRequests = prHead(headB)
	if err := store.ObserveField(ctx, task.ID, done.Add(time.Minute), func(o *model.Observation) {
		o.ForceMergeAt = nil
		o.ForceMergeReason, o.ForceMergeBy, o.ForceMergeSkipped, o.ForceMergeHeadSHA = "", "", "", ""
		o.CI = &model.CIStatus{RunID: "run-2", Commit: headB, Conclusion: "success", FinishedAt: &done}
	}); err != nil {
		t.Fatal(err)
	}

	// Before the merge queue's next cycle has withdrawn the override, an
	// override at B that names no review gate must already be refused: the
	// security review has not passed on B and nobody overrode it there.
	err := c.ForceMergeWithGates(ctx, task.ID, "just CI please", headB, nil)
	if err == nil || !strings.Contains(err.Error(), "security-review") {
		t.Fatalf("override at head B naming no review gate = %v, want it refused for the unsatisfied security-review gate", err)
	}

	// Once the queue has withdrawn it (as orchestrator.dropMovedForcedMerge
	// does), the task pane no longer shows the gate as overridden, while the
	// record of who asked and why stays.
	if err := store.UpdateTask(ctx, task.ID, func(tk *model.Task) error {
		model.WithdrawGateOverride(tk, done.Add(time.Minute))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	detail, err := c.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range detail.MergeGates {
		if g.ID == model.GateSecurityReview && g.State == model.GateStateOverridden {
			t.Fatalf("security-review gate at head B = %s (%s): an override given for head A still shows as standing", g.State, g.Detail)
		}
	}
	got, err := store.GetTask(ctx, task.ID)
	if err != nil || got == nil {
		t.Fatal(err)
	}
	if all := model.GateOverrides(*got); len(all) != 1 || all[0].WithdrawnAt == nil || all[0].Reason != "rollback at A" {
		t.Fatalf("override records = %+v, want head A's kept as a withdrawn audit record", all)
	}

	// And an override at B naming the gate is accepted and stands.
	if err := c.ForceMergeWithGates(ctx, task.ID, "rollback at B", headB, []string{"security-review"}); err != nil {
		t.Fatalf("override at head B: %v", err)
	}
	got, _ = store.GetTask(ctx, task.ID)
	if ov, ok := model.LatestGateOverride(*got); !ok || ov.HeadSHA != headB {
		t.Fatalf("latest override = %+v (%v), want head B's", ov, ok)
	}
}

// A row in a list (the Inbox, the task list) shows the same CI gate as the
// task page: grain CI's finished verdict, not "waiting for checks"
// (docs/test-runs/2026-11-11-merge-gates.md, finding F3).
func TestAListRowsCIGateMatchesTheTaskPage(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)
	if err := store.UpdateTask(ctx, task.ID, func(tk *model.Task) error {
		tk.AutoMerge = true
		tk.Links = append(tk.Links, model.Link{Kind: model.LinkFixes, Target: "acme/widgets#5"},
			model.Link{Kind: model.LinkSecurityHold, Target: "trust-boundary/v5"},
			model.Link{Kind: model.LinkSecurityReviewTask, Target: "77"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	done := time.Date(2026, 11, 11, 1, 0, 0, 0, time.UTC)
	for conclusion, want := range map[string]model.MergeGateState{
		"success": model.GateStatePassed,
		"failure": model.GateStateFailing,
	} {
		if err := store.Observe(ctx, model.Observation{TaskID: task.ID, CompletedAt: &done,
			CI: &model.CIStatus{RunID: "r-" + conclusion, Commit: "0123456789abcdef0123456789abcdef01234567",
				Conclusion: conclusion, FinishedAt: &done}}); err != nil {
			t.Fatal(err)
		}
		detail, err := c.GetTask(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		list, err := c.ListTasks(ctx)
		if err != nil {
			t.Fatal(err)
		}
		ciGate := func(gates []model.MergeGate) model.MergeGateState {
			for _, g := range gates {
				if g.ID == model.GateCI {
					return g.State
				}
			}
			return ""
		}
		if got := ciGate(detail.MergeGates); got != want {
			t.Fatalf("%s: task page CI gate = %q, want %q", conclusion, got, want)
		}
		found := false
		for _, row := range list {
			if row.ID != task.ID {
				continue
			}
			found = true
			if got := ciGate(row.MergeGates); got != want {
				t.Fatalf("%s: list row CI gate = %q, want %q like the task page", conclusion, got, want)
			}
		}
		if !found {
			t.Fatalf("task %s not listed", task.ID)
		}
	}
}

// c.Task (single-task summary reads), c.GetTask (the task page) and
// c.ListTasks (Inbox and list rows) resolve linked review task states the same
// way, and name every passed review task when the code-review gate stands for
// more than one.
func TestSingleTaskViewReviewGatesMatchTaskPageAndListRows(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)
	r1 := create(t, c, ctx)
	r2 := create(t, c, ctx)
	if err := store.UpdateTask(ctx, task.ID, func(tk *model.Task) error {
		tk.Links = append(tk.Links,
			model.Link{Kind: model.LinkFixes, Target: "acme/widgets#88"},
			model.Link{Kind: model.LinkPathTriggerHold, Target: string(model.TriggerHumanRequired)},
			model.Link{Kind: model.LinkPathTriggerTask, Target: "alpha=" + r1.ID},
			model.Link{Kind: model.LinkPathTriggerTask, Target: "beta=" + r2.ID},
		)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	done := time.Date(2026, 11, 11, 2, 0, 0, 0, time.UTC)
	if err := store.Observe(ctx, model.Observation{TaskID: r1.ID, ClosedAt: &done}); err != nil {
		t.Fatal(err)
	}

	reviewGate := func(gates []model.MergeGate) model.MergeGate {
		for _, g := range gates {
			if g.ID == model.GateReview {
				return g
			}
		}
		return model.MergeGate{}
	}
	assertAllViews := func(wantState model.MergeGateState, wantDetail string) {
		t.Helper()
		single, err := c.Task(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		detail, err := c.GetTask(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		list, err := c.ListTasks(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var listed model.MergeGate
		for _, row := range list {
			if row.ID == task.ID {
				listed = reviewGate(row.MergeGates)
			}
		}
		for label, g := range map[string]model.MergeGate{
			"Task":      reviewGate(single.MergeGates),
			"GetTask":   reviewGate(detail.MergeGates),
			"ListTasks": listed,
		} {
			if g.State != wantState || g.Detail != wantDetail {
				t.Fatalf("%s review gate = %q (%q), want %q (%q)", label, g.State, g.Detail, wantState, wantDetail)
			}
		}
	}

	assertAllViews(model.GateStateRunning, "running (task #"+r2.ID+")")

	if err := store.Observe(ctx, model.Observation{TaskID: r2.ID, ClosedAt: &done}); err != nil {
		t.Fatal(err)
	}
	assertAllViews(model.GateStatePassed, "passed (tasks #"+r1.ID+", #"+r2.ID+")")
}
