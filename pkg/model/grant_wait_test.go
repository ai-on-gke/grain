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
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// A task_grant_wait row is derived from a task's grants and target, so
// the write that changes either of those is what invalidates it
// (store.go's invalidateStaleGrantWait). These tests pin both halves of
// that rule: the edit that moves an input drops the fact, and the edit
// that moves nothing it was derived from keeps it -- because the next
// admission probe only runs for a candidate a cycle has capacity for,
// and a deployment with every worker busy would otherwise go on
// displaying a reason that stopped being true the moment an operator
// fixed it.

func waitingTask(t *testing.T, store *model.Store, ctx context.Context, id string) model.Task {
	t.Helper()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := model.Task{
		ID: id, Intent: model.IntentImplement, Title: "fix the thing",
		Filing: model.Filing{
			Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect,
		},
		Approval: &model.Attribution{Actor: human},
		Target:   &repo,
		Binding:  model.BindingDirective,
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("filing %s: %v", id, err)
	}
	if err := store.SetGrantWait(ctx, model.GrantWait{
		TaskID: id, Capability: "grain-ci",
		Resource: "repository CI for " + repo.String(), WaitingAt: now,
	}); err != nil {
		t.Fatalf("recording %s's grant wait: %v", id, err)
	}
	return task
}

func waitOf(t *testing.T, store *model.Store, ctx context.Context, id string) *model.GrantWait {
	t.Helper()
	wait, err := store.GrantWaitOf(ctx, id)
	if err != nil {
		t.Fatalf("reading %s's grant wait: %v", id, err)
	}
	return wait
}

func TestAddingAGrantInvalidatesTheWaitThatAskedForIt(t *testing.T) {
	store, ctx := open(t)
	task := waitingTask(t, store, ctx, "t1")

	if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		t.Grants = append(t.Grants, model.Grant{Capability: "grain-ci", Via: model.GrantByLabel})
		return nil
	}); err != nil {
		t.Fatalf("granting grain-ci: %v", err)
	}

	if wait := waitOf(t, store, ctx, task.ID); wait != nil {
		t.Fatalf("grant wait after the grant was added = %+v, want none: the reason it gave is no longer true", wait)
	}
}

func TestRemovingAGrantInvalidatesTheWaitToo(t *testing.T) {
	store, ctx := open(t)
	task := waitingTask(t, store, ctx, "t1")
	task.Grants = []model.Grant{{Capability: "gemini-key", Via: model.GrantByLabel}}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := store.SetGrantWait(ctx, model.GrantWait{
		TaskID: task.ID, Capability: "gemini-key", Resource: "Gemini API keys", WaitingAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	task.Grants = nil
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	if wait := waitOf(t, store, ctx, task.ID); wait != nil {
		t.Fatalf("grant wait after the grant was removed = %+v, want none: what it was derived from is gone", wait)
	}
}

func TestAnEditThatChangesNoGrantKeepsTheWait(t *testing.T) {
	store, ctx := open(t)
	task := waitingTask(t, store, ctx, "t1")

	if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		t.Title = "fix the thing, properly"
		t.Body = "with more detail"
		return nil
	}); err != nil {
		t.Fatalf("retitling: %v", err)
	}

	wait := waitOf(t, store, ctx, task.ID)
	if wait == nil || wait.Capability != "grain-ci" || !wait.WaitingAt.Equal(now) {
		t.Fatalf("grant wait after an unrelated edit = %+v, want the original: renaming a task frees no resource", wait)
	}
}

// The same rule read from the other end: a scarce pool that was
// exhausted is still exhausted after somebody edits a different task, so
// that task's write must leave this one's wait alone.
func TestAnEditToAnotherTaskKeepsAScarceWait(t *testing.T) {
	store, ctx := open(t)
	waiting := waitingTask(t, store, ctx, "t1")
	if err := store.SetGrantWait(ctx, model.GrantWait{
		TaskID: waiting.ID, Capability: "gemini-key", Resource: "Gemini API keys", WaitingAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	other := waitingTask(t, store, ctx, "t2")

	if err := store.UpdateTask(ctx, other.ID, func(t *model.Task) error {
		t.Grants = append(t.Grants, model.Grant{Capability: "gemini-key", Via: model.GrantByLabel})
		return nil
	}); err != nil {
		t.Fatalf("granting the other task a key: %v", err)
	}

	wait := waitOf(t, store, ctx, waiting.ID)
	if wait == nil || wait.Resource != "Gemini API keys" {
		t.Fatalf("scarce wait after another task was edited = %+v, want it preserved", wait)
	}
	if wait := waitOf(t, store, ctx, other.ID); wait != nil {
		t.Fatalf("edited task's own wait = %+v, want it invalidated", wait)
	}
}

// Retargeting is the other input: whether Grain CI is required of a task
// at all is a property of the repository it targets, so a wait derived
// under one repository says nothing under another.
func TestRetargetingATaskInvalidatesItsWait(t *testing.T) {
	store, ctx := open(t)
	task := waitingTask(t, store, ctx, "t1")

	if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		t.Target = &model.RepoRef{Owner: "acme", Name: "gadgets"}
		return nil
	}); err != nil {
		t.Fatalf("retargeting: %v", err)
	}

	if wait := waitOf(t, store, ctx, task.ID); wait != nil {
		t.Fatalf("grant wait after retargeting = %+v, want none", wait)
	}
}

func TestClearGrantWaitIsNoOpWhenUnchanged(t *testing.T) {
	store, ctx := open(t)
	waitingTask(t, store, ctx, "t1")

	// ClearGrantWaitUnless keeping "grain-ci" must not open a write transaction.
	before := store.CommitCount()
	if err := store.ClearGrantWaitUnless(ctx, "t1", "grain-ci"); err != nil {
		t.Fatal(err)
	}
	if after := store.CommitCount(); after != before {
		t.Fatalf("ClearGrantWaitUnless keeping existing capability bumped commits from %d to %d", before, after)
	}

	// ClearGrantWait on a task with no wait must not open a write transaction.
	if err := store.ClearGrantWait(ctx, "t-none"); err != nil {
		t.Fatal(err)
	}
	if after := store.CommitCount(); after != before {
		t.Fatalf("ClearGrantWait on absent row bumped commits from %d to %d", before, after)
	}
}

func TestSetAndClearQuotaWaitsBatchAndSkipUnchanged(t *testing.T) {
	store, ctx := open(t)
	waitingTask(t, store, ctx, "t-nonquota")
	for _, id := range []string{"t-a", "t-b", "t-c"} {
		tk := model.Task{
			ID: id, Intent: model.IntentImplement, Title: "task " + id,
			Filing:  model.Filing{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
			Binding: model.BindingDirective,
		}
		if err := store.PutTask(ctx, tk); err != nil {
			t.Fatal(err)
		}
	}

	waits := []model.GrantWait{
		{TaskID: "t-nonquota", Capability: model.QuotaWaitCapability, Resource: "full", WaitingAt: now},
		{TaskID: "t-a", Capability: model.QuotaWaitCapability, Resource: "full", WaitingAt: now},
		{TaskID: "t-b", Capability: model.QuotaWaitCapability, Resource: "full", WaitingAt: now},
		{TaskID: "t-c", Capability: model.QuotaWaitCapability, Resource: "full", WaitingAt: now},
	}
	before := store.CommitCount()
	if err := store.SetQuotaWaits(ctx, waits); err != nil {
		t.Fatal(err)
	}
	if commits := store.CommitCount() - before; commits != 1 {
		t.Fatalf("SetQuotaWaits made %d commits, want 1", commits)
	}
	if w := waitOf(t, store, ctx, "t-nonquota"); w == nil || w.Capability != "grain-ci" {
		t.Fatalf("t-nonquota wait = %+v, want grain-ci preserved", w)
	}
	for _, id := range []string{"t-a", "t-b", "t-c"} {
		if w := waitOf(t, store, ctx, id); w == nil || w.Capability != model.QuotaWaitCapability || w.Resource != "full" {
			t.Fatalf("%s wait = %+v, want quota/full", id, w)
		}
	}

	// Unchanged batch does 0 commits.
	before = store.CommitCount()
	if err := store.SetQuotaWaits(ctx, waits); err != nil {
		t.Fatal(err)
	}
	if commits := store.CommitCount() - before; commits != 0 {
		t.Fatalf("unchanged SetQuotaWaits made %d commits, want 0", commits)
	}

	// ClearQuotaWaits leaves non-quota waits alone and batches the delete into 1 commit.
	before = store.CommitCount()
	if err := store.ClearQuotaWaits(ctx, []string{"t-nonquota", "t-a", "t-b", "t-c"}); err != nil {
		t.Fatal(err)
	}
	if commits := store.CommitCount() - before; commits != 1 {
		t.Fatalf("ClearQuotaWaits made %d commits, want 1", commits)
	}
	if w := waitOf(t, store, ctx, "t-nonquota"); w == nil || w.Capability != "grain-ci" {
		t.Fatalf("t-nonquota wait after ClearQuotaWaits = %+v, want grain-ci preserved", w)
	}
	for _, id := range []string{"t-a", "t-b", "t-c"} {
		if w := waitOf(t, store, ctx, id); w != nil {
			t.Fatalf("%s wait after ClearQuotaWaits = %+v, want nil", id, w)
		}
	}

	// Repeat ClearQuotaWaits does 0 commits.
	before = store.CommitCount()
	if err := store.ClearQuotaWaits(ctx, []string{"t-nonquota", "t-a"}); err != nil {
		t.Fatal(err)
	}
	if commits := store.CommitCount() - before; commits != 0 {
		t.Fatalf("repeat ClearQuotaWaits made %d commits, want 0", commits)
	}
}
