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

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite/sqlitetest"
)

// messageHeldTask is a task whose run called resume_later(when:
// {message: true}) and has finished: deferred with no wake time, held by
// the suspension that call wrote.
func messageHeldTask(t *testing.T, store *model.Store, id string, at time.Time) {
	t.Helper()
	ctx := t.Context()
	person := model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "owner"}}
	if err := store.PutTask(ctx, model.Task{
		ID: id, Intent: model.IntentImplement, Title: "t",
		Origin:   model.Origin{Attribution: person, Reason: model.ReasonDirect},
		Approval: &person, Binding: model.BindingDirective,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{ID: id + "-1", TaskID: id, Attempt: 1, StartedAt: at},
		model.Limits{}); err != nil {
		t.Fatal(err)
	}
	stopped := at.Add(time.Minute)
	if err := store.SetDeferred(ctx, id, &stopped, nil, stopped); err != nil {
		t.Fatal(err)
	}
	if err := store.HoldResumeOn(ctx, id, model.ResumeOn{Message: true}, stopped); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, id+"-1", at.Add(2*time.Minute), "succeeded", ""); err != nil {
		t.Fatal(err)
	}
}

func deferredNow(t *testing.T, store *model.Store, id string) bool {
	t.Helper()
	task, err := store.GetTask(t.Context(), id)
	if err != nil || task == nil {
		t.Fatalf("reading %s: %v", id, err)
	}
	return task.DeferredAt != nil
}

// A person's comment on a grain-owned pull request is relayed into the
// task's conversation. When that task stopped on resume_later(when:
// {message: true}) it reads deferred, not awaiting_reply, and the relay
// used to leave it there: the post it waited for had arrived and nothing
// released it.
func TestARelayedPullRequestCommentWakesAMessageHold(t *testing.T) {
	store, _ := sqlitetest.Open(t)
	ctx := t.Context()
	at := time.Date(2026, 11, 12, 6, 0, 0, 0, time.UTC)
	messageHeldTask(t, store, "held", at)

	state, err := store.State(ctx, "held")
	if err != nil || state != model.StateDeferred {
		t.Fatalf("state = %q (%v), want deferred", state, err)
	}
	if err := deliverReplyAndWakeTask(ctx, store, model.Task{ID: "held"}, state, false,
		"owner", "Comment on acme/widgets#7:\n\n@grain go ahead", "issue_comment:1", true,
		at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if deferredNow(t, store, "held") {
		t.Fatal("a person's relayed pull request comment left the task deferred on resume_later(message)")
	}
}

// A post that reached the store without WakeOnMessage seeing it -- one
// written while the run was still ending, or by any path that does not
// call it -- is picked up by the wake reconciler on its next pass.
func TestReconcileWakeReleasesAMessageHoldAlreadyPostedTo(t *testing.T) {
	store, _ := sqlitetest.Open(t)
	ctx := t.Context()
	at := time.Date(2026, 11, 12, 6, 0, 0, 0, time.UTC)
	messageHeldTask(t, store, "posted", at)
	messageHeldTask(t, store, "quiet", at)
	if _, err := store.AddComment(ctx, model.Comment{TaskID: "posted",
		Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "owner"}},
		Body:   "carry on", CreatedAt: at.Add(90 * time.Second)}); err != nil {
		t.Fatal(err)
	}

	if err := reconcileWake(ctx, Deps{Store: store}, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if deferredNow(t, store, "posted") {
		t.Fatal("reconcileWake left a task deferred on a message that had already been posted")
	}
	if !deferredNow(t, store, "quiet") {
		t.Fatal("reconcileWake woke a message hold nobody had posted to")
	}
}
