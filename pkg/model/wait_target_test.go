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

package model_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestSettledUntilMergedWaitsThroughAClosureWithItsPullRequestOpen(t *testing.T) {
	for name, tc := range map[string]struct {
		target            model.WaitTarget
		settled, met      bool
		closedSettled     bool
		closedSettledMet  bool
		describeSubstring string
	}{
		"open": {target: model.WaitTarget{State: model.StateQueued},
			describeSubstring: "queued"},
		"merged": {target: model.WaitTarget{Closed: true, Merged: true},
			settled: true, met: true, closedSettled: true, closedSettledMet: true,
			describeSubstring: "its change merged"},
		"closed, nothing to merge": {target: model.WaitTarget{Closed: true},
			settled: true, closedSettled: true, closedSettledMet: true,
			describeSubstring: "nothing of it merged"},
		"closed, its pull request still open": {target: model.WaitTarget{Closed: true, PullRequestOpen: true},
			closedSettled: true, closedSettledMet: true,
			describeSubstring: "its pull request is still open"},
	} {
		t.Run(name, func(t *testing.T) {
			if settled, met := tc.target.Settled(true); settled != tc.settled || met != tc.met {
				t.Errorf("until merged: settled=%v met=%v, want %v %v", settled, met, tc.settled, tc.met)
			}
			if settled, met := tc.target.Settled(false); settled != tc.closedSettled || met != tc.closedSettledMet {
				t.Errorf("until closed: settled=%v met=%v, want %v %v", settled, met, tc.closedSettled, tc.closedSettledMet)
			}
			if d := tc.target.Describe(); !strings.Contains(d, tc.describeSubstring) {
				t.Errorf("Describe() = %q, want it to say %q", d, tc.describeSubstring)
			}
		})
	}
}

// waitTargetTask files an approved task in acme/widgets with links.
func waitTargetTask(t *testing.T, store *model.Store, ctx context.Context, id string, links ...model.Link) model.Task {
	t.Helper()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := model.Task{
		ID: id, Intent: model.IntentImplement, Title: "task " + id,
		Origin:   model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
		Approval: &model.Attribution{Actor: human},
		Target:   &repo,
		Binding:  model.BindingDirective,
		Links:    links,
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("filing %s: %v", id, err)
	}
	return task
}

func closeAt(t *testing.T, store *model.Store, ctx context.Context, id string, at time.Time) {
	t.Helper()
	if err := store.ObserveField(ctx, id, at, func(o *model.Observation) { o.ClosedAt = &at }); err != nil {
		t.Fatalf("closing %s: %v", id, err)
	}
}

func TestWaitTargetAtReadsAClosedTasksUnsettledPullRequestAsOpenInsideTheWindow(t *testing.T) {
	store, ctx := open(t)
	waitTargetTask(t, store, ctx, "pr", model.Link{Kind: model.LinkFixes, Target: "acme/widgets#7"})
	waitTargetTask(t, store, ctx, "nopr")
	closeAt(t, store, ctx, "pr", now)
	closeAt(t, store, ctx, "nopr", now)

	read := func(id string, at time.Time) model.WaitTarget {
		t.Helper()
		target, err := store.WaitTargetAt(ctx, id, at)
		if err != nil || target == nil {
			t.Fatalf("WaitTargetAt(%s): %v, %v", id, target, err)
		}
		return *target
	}
	if got := read("pr", now.Add(time.Hour)); !got.PullRequestOpen || got.Outcome != model.WaitOutcomeCancelled {
		t.Fatalf("closed with its pull request open: %+v, want PullRequestOpen", got)
	}
	if got := read("nopr", now.Add(time.Hour)); got.PullRequestOpen {
		t.Fatalf("a task with no pull request read as one still open: %+v", got)
	}
	if got := read("pr", now.Add(model.ClosedPullRequestWindow+time.Minute)); got.PullRequestOpen {
		t.Fatalf("a pull request grain no longer reads still held a wait: %+v", got)
	}

	merged := now.Add(2 * time.Hour)
	if err := store.ObserveField(ctx, "pr", merged, func(o *model.Observation) { o.PrMergedAt = &merged }); err != nil {
		t.Fatal(err)
	}
	if got := read("pr", merged); got.PullRequestOpen || !got.Merged {
		t.Fatalf("after the hand merge: %+v, want merged and no longer open", got)
	}
}

func TestUntilMergedParkWaitsOnATargetClosedWithItsPullRequestOpen(t *testing.T) {
	store, ctx := open(t)
	waitTargetTask(t, store, ctx, "target", model.Link{Kind: model.LinkFixes, Target: "acme/widgets#7"})
	waiter := waitTargetTask(t, store, ctx, "waiter", model.Link{Kind: model.LinkDependsOn, Target: "target"})
	if err := store.StartRun(ctx, model.Run{ID: "waiter-1", TaskID: waiter.ID, Sandbox: "s1", Attempt: 1, StartedAt: now}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	park := func(untilMerged bool) {
		t.Helper()
		if err := store.Suspend(ctx, model.Suspension{RunID: "waiter-1", TaskID: waiter.ID, Kind: model.SuspendRequested,
			Detail:    model.ParkedDetail([]string{"target"}),
			ResumeOn:  model.ResumeOn{Tasks: []string{"target"}, UntilMerged: untilMerged}.JSON(),
			CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	waitsOn := func(at time.Time) string {
		t.Helper()
		id, err := store.UntilMergedParkWaitsOn(ctx, waiter.ID, at)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}

	park(true)
	if got := waitsOn(now); got != "" {
		t.Fatalf("an open target is task_ready's to hold, not this: waits on %q", got)
	}
	closeAt(t, store, ctx, "target", now.Add(time.Minute))
	if got := waitsOn(now.Add(2 * time.Minute)); got != "target" {
		t.Fatalf("closed with its pull request open: waits on %q, want target", got)
	}
	if got := waitsOn(now.Add(model.ClosedPullRequestWindow + time.Hour)); got != "" {
		t.Fatalf("past the window grain reads it in: waits on %q, want none", got)
	}

	park(false)
	if got := waitsOn(now.Add(2 * time.Minute)); got != "" {
		t.Fatalf("a park until closed waits on %q past the close", got)
	}

	park(true)
	unlinked := waiter
	unlinked.Links = nil
	if err := store.PutTask(ctx, unlinked); err != nil {
		t.Fatal(err)
	}
	if got := waitsOn(now.Add(2 * time.Minute)); got != "" {
		t.Fatalf("with the dependency removed: waits on %q, want none", got)
	}
	if err := store.PutTask(ctx, waiter); err != nil {
		t.Fatal(err)
	}

	closedPR := now.Add(time.Hour)
	if err := store.ObserveField(ctx, "target", closedPR, func(o *model.Observation) { o.PrClosedAt = &closedPR }); err != nil {
		t.Fatal(err)
	}
	if got := waitsOn(closedPR); got != "" {
		t.Fatalf("after its pull request closed unmerged: waits on %q, want none", got)
	}
}

