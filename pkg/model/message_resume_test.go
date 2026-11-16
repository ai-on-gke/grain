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
	"slices"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// stoppedOnMessage files a task whose run called resume_later(when:
// {message: true}) at now+1m -- what ui.Client.ResumeLater writes while
// the run is live -- and returns the time it did.
func stoppedOnMessage(t *testing.T, store *model.Store, id string) time.Time {
	t.Helper()
	ctx := t.Context()
	task := model.Task{
		ID: id, Intent: model.IntentImplement, Title: "t",
		Origin:   model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
		Approval: &model.Attribution{Actor: human}, Binding: model.BindingDirective,
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{ID: id + "-1", TaskID: id, Attempt: 1, StartedAt: now},
		model.Limits{}); err != nil {
		t.Fatal(err)
	}
	deferredAt := now.Add(time.Minute)
	if err := store.SetDeferred(ctx, id, &deferredAt, nil, deferredAt); err != nil {
		t.Fatal(err)
	}
	if err := store.HoldResumeOn(ctx, id, model.ResumeOn{Message: true}, deferredAt); err != nil {
		t.Fatal(err)
	}
	return deferredAt
}

func post(t *testing.T, store *model.Store, id string, by model.Attribution, at time.Time) int64 {
	t.Helper()
	cid, err := store.AddComment(t.Context(), model.Comment{TaskID: id, Author: by, Body: "hello", CreatedAt: at})
	if err != nil {
		t.Fatal(err)
	}
	return cid
}

// A person who posts while the run that called resume_later(when:
// {message: true}) is still ending finds no finished suspension for
// WakeOnMessage to release. That post is what the task waits for all the
// same, and DueMessageResumes is what keeps it from being lost: before
// it, the task stayed deferred with no wake time until somebody posted
// a second time.
func TestAPostWhileTheRunEndsStillWakesAMessageHold(t *testing.T) {
	store, ctx := open(t)
	stoppedOnMessage(t, store, "late")

	person := model.Attribution{Actor: human}
	post(t, store, "late", person, now.Add(90*time.Second))
	if woke, err := store.WakeOnMessage(ctx, "late", now.Add(90*time.Second)); err != nil || woke {
		t.Fatalf("WakeOnMessage while the run is live = %v, %v; this test pins the window it cannot see", woke, err)
	}
	if err := store.FinishRun(ctx, "late-1", now.Add(2*time.Minute), "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	when := model.ResumeOn{Message: true}
	if err := store.Suspend(ctx, model.Suspension{RunID: "late-1", Kind: model.SuspendRequested,
		Detail: model.RequestedDetail("r", when), ResumeOn: when.JSON(), CreatedAt: now.Add(2 * time.Minute)}); err != nil {
		t.Fatal(err)
	}

	due, err := store.DueMessageResumes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(due, []string{"late"}) {
		t.Fatalf("DueMessageResumes = %v, want [late]: the person's post was lost", due)
	}
}

// Only a post the task has not already been given counts, and only one
// from somebody other than grain and the task's own agent. Grain relaying
// another agent's words (a team broadcast) is that agent's post.
func TestDueMessageResumesSkipsOwnAndDeliveredPosts(t *testing.T) {
	store, ctx := open(t)
	finish := func(id string) {
		t.Helper()
		if err := store.FinishRun(ctx, id+"-1", now.Add(2*time.Minute), "succeeded", ""); err != nil {
			t.Fatal(err)
		}
	}
	own := model.Principal{Kind: model.PrincipalAgent, ID: "quiet"}
	grain := model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}

	stoppedOnMessage(t, store, "quiet")
	finish("quiet")
	post(t, store, "quiet", model.Attribution{Actor: grain}, now.Add(3*time.Minute))
	post(t, store, "quiet", model.Attribution{Actor: grain, OnBehalfOf: &own}, now.Add(3*time.Minute))
	post(t, store, "quiet", model.Attribution{Actor: own}, now.Add(3*time.Minute))

	deferredAt := stoppedOnMessage(t, store, "before")
	post(t, store, "before", model.Attribution{Actor: human}, deferredAt.Add(-30*time.Second))
	finish("before")

	stoppedOnMessage(t, store, "delivered")
	cid := post(t, store, "delivered", model.Attribution{Actor: human}, now.Add(90*time.Second))
	if err := store.ObserveField(ctx, "delivered", now.Add(90*time.Second), func(o *model.Observation) {
		o.DeliveredCommentID = &cid
	}); err != nil {
		t.Fatal(err)
	}
	finish("delivered")

	other := model.Principal{Kind: model.PrincipalAgent, ID: "elsewhere"}
	stoppedOnMessage(t, store, "messaged")
	finish("messaged")
	post(t, store, "messaged", model.Attribution{Actor: other}, now.Add(3*time.Minute))

	stoppedOnMessage(t, store, "broadcast")
	finish("broadcast")
	post(t, store, "broadcast", model.Attribution{Actor: grain, OnBehalfOf: &other}, now.Add(3*time.Minute))

	due, err := store.DueMessageResumes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(due, []string{"broadcast", "messaged"}) {
		t.Fatalf("DueMessageResumes = %v, want only [broadcast messaged]", due)
	}
}

// Closing a task that called resume_later stops HeldCIResumes,
// DueMessageResumes and WakeOnMessage from reporting or waking it, while
// preserving deferred_at so reopening restores the hold.
func TestClosedTasksDoNotStayInHeldResumes(t *testing.T) {
	store, ctx := open(t)
	target := model.RepoRef{Owner: "acme", Name: "widgets"}

	// 1. Suspension-backed CI + message hold.
	t1 := model.Task{
		ID: "ci-susp", Intent: model.IntentImplement, Title: "ci wait",
		Target:   &target,
		Origin:   model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
		Approval: &model.Attribution{Actor: human}, Binding: model.BindingDirective,
	}
	if err := store.PutTask(ctx, t1); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{ID: "ci-susp-1", TaskID: "ci-susp", Attempt: 1, StartedAt: now}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	defAt := now.Add(time.Minute)
	when := model.ResumeOn{CI: "gha:acme:widgets:5001", Message: true}
	if err := store.SetDeferred(ctx, "ci-susp", &defAt, nil, defAt); err != nil {
		t.Fatal(err)
	}
	if err := store.HoldResumeOn(ctx, "ci-susp", when, defAt); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, "ci-susp-1", now.Add(2*time.Minute), "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.Suspend(ctx, model.Suspension{
		RunID: "ci-susp-1", Kind: model.SuspendRequested,
		Detail: model.RequestedDetail("wait", when), ResumeOn: when.JSON(), CreatedAt: now.Add(2 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	// 2. Comment-recovered CI hold (no suspension row).
	t2 := model.Task{
		ID: "ci-rec", Intent: model.IntentImplement, Title: "recovered ci wait",
		Target:   &target,
		Origin:   model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
		Approval: &model.Attribution{Actor: human}, Binding: model.BindingDirective,
	}
	if err := store.PutTask(ctx, t2); err != nil {
		t.Fatal(err)
	}
	if err := store.SetDeferred(ctx, "ci-rec", &defAt, nil, defAt); err != nil {
		t.Fatal(err)
	}
	grainActor := model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}}
	if _, err := store.AddComment(ctx, model.Comment{
		TaskID: "ci-rec", Author: grainActor,
		Body:      "grain: this task's run stopped with resume_later. It goes back in the queue once CI (gha:acme:widgets:5002) has a verdict.",
		CreatedAt: defAt,
	}); err != nil {
		t.Fatal(err)
	}

	held, err := store.HeldCIResumes(ctx)
	if err != nil || len(held) != 2 {
		t.Fatalf("HeldCIResumes while open = %+v, %v; want both tasks", held, err)
	}

	closedAt := now.Add(3 * time.Minute)
	for _, id := range []string{"ci-susp", "ci-rec"} {
		if err := store.ObserveField(ctx, id, closedAt, func(o *model.Observation) {
			o.ClosedAt = &closedAt
		}); err != nil {
			t.Fatal(err)
		}
	}

	if held, err = store.HeldCIResumes(ctx); err != nil || len(held) != 0 {
		t.Fatalf("HeldCIResumes after close = %+v, %v; want empty", held, err)
	}
	post(t, store, "ci-susp", model.Attribution{Actor: human}, now.Add(4*time.Minute))
	if woke, err := store.WakeOnMessage(ctx, "ci-susp", now.Add(4*time.Minute)); err != nil || woke {
		t.Fatalf("WakeOnMessage on closed task = %v, %v; want false", woke, err)
	}
	if due, err := store.DueMessageResumes(ctx); err != nil || len(due) != 0 {
		t.Fatalf("DueMessageResumes on closed task = %v, %v; want empty", due, err)
	}

	// Reopening restores the hold.
	if err := store.ObserveField(ctx, "ci-susp", now.Add(5*time.Minute), func(o *model.Observation) {
		o.ClosedAt = nil
	}); err != nil {
		t.Fatal(err)
	}
	if held, err = store.HeldCIResumes(ctx); err != nil || len(held) != 1 || held[0].TaskID != "ci-susp" {
		t.Fatalf("HeldCIResumes after reopen = %+v, %v; want [ci-susp]", held, err)
	}
}
