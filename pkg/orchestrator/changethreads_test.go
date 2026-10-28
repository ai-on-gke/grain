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
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/change"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// threadedCLs is a changelist provider whose CLs carry review threads.
// Like the edition's, it leaves out threads the task already records.
type threadedCLs struct {
	pollingCLs
	threads map[string][]change.Thread
	asked   int
}

func (p *threadedCLs) Threads(_ context.Context, task model.Task, target string) ([]change.Thread, error) {
	p.asked++
	recorded := map[string]bool{}
	for _, l := range task.Links {
		if l.Kind == model.LinkCLThread {
			recorded[l.Target] = true
		}
	}
	var out []change.Thread
	for _, th := range p.threads[target] {
		if !recorded[th.ID] {
			out = append(out, th)
		}
	}
	return out, nil
}

func putCLTask(t *testing.T, ctx context.Context, store *model.Store, id string) {
	t.Helper()
	human := model.Principal{Kind: model.PrincipalHuman, ID: "bsalmon"}
	if err := store.PutTask(ctx, model.Task{
		ID: id, Intent: model.IntentImplement, Title: "port the retry budget", Body: "please",
		Origin:   model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
		Approval: &model.Attribution{Actor: human},
		Links:    []model.Link{{Kind: model.LinkChangelist, Target: "cl/" + id}},
	}); err != nil {
		t.Fatal(err)
	}
}

// New threads on a finished CL task send one run per batch: they are
// recorded before the run is asked for, so a second poll sends none.
func TestNewCLThreadsSendOneRunPerBatch(t *testing.T) {
	provider := &threadedCLs{threads: map[string][]change.Thread{
		"cl/41": {{ID: "cl/41:c1", Author: "reviewer", Body: "<external>Rename this.</external>"}},
	}}
	change.Register(provider)
	t.Cleanup(func() { change.Unregister(model.LinkChangelist) })

	store, ctx := openStore(t)
	putCLTask(t, ctx, store, "41")
	done := baseTime
	if err := store.ObserveField(ctx, "41", done, func(o *model.Observation) { o.CompletedAt = &done }); err != nil {
		t.Fatal(err)
	}
	if s := stateOf(t, ctx, store, "41"); s != model.StateAwaitingSubmit {
		t.Fatalf("state = %q, want %q", s, model.StateAwaitingSubmit)
	}

	sync := func(at time.Time) {
		t.Helper()
		if err := orchestrator.SyncChangeThreads(ctx, store, at); err != nil {
			t.Fatal(err)
		}
	}
	sync(baseTime.Add(time.Minute))
	if s := stateOf(t, ctx, store, "41"); s != model.StateQueued {
		t.Fatalf("state = %q, want %q -- a new thread sends a run", s, model.StateQueued)
	}
	bodies := commentBodies(t, ctx, store, "41")
	note := bodies[len(bodies)-1]
	for _, want := range []string{"1 new review thread", "cl/41:c1", "reviewer", "Rename this.", "resolve_review_thread"} {
		if !strings.Contains(note, want) {
			t.Errorf("the run's note lacks %q:\n%s", want, note)
		}
	}
	recorded := 0
	for _, l := range reloaded(t, ctx, store, "41").Links {
		if l.Kind == model.LinkCLThread && l.Target == "cl/41:c1" {
			recorded++
		}
	}
	if recorded != 1 {
		t.Fatalf("thread recorded %d times, want once", recorded)
	}

	// Polled again with the run done: the same thread sends nothing.
	again := baseTime.Add(time.Hour)
	if err := store.ObserveField(ctx, "41", again, func(o *model.Observation) { o.CompletedAt = &again }); err != nil {
		t.Fatal(err)
	}
	sync(baseTime.Add(time.Hour + time.Minute))
	if s := stateOf(t, ctx, store, "41"); s != model.StateAwaitingSubmit {
		t.Fatalf("state = %q, want %q -- a recorded thread sends no second run", s, model.StateAwaitingSubmit)
	}
	if n := len(commentBodies(t, ctx, store, "41")); n != len(bodies) {
		t.Fatalf("%d comments after the second poll, want %d", n, len(bodies))
	}
	if provider.asked != 2 {
		t.Fatalf("provider asked %d times, want 2", provider.asked)
	}
}

// A thread opened while a run is going is that run's: recorded, and told
// to it as an addendum, without queueing another run.
func TestCLThreadsOnARunningTaskAreAnAddendum(t *testing.T) {
	provider := &threadedCLs{threads: map[string][]change.Thread{
		"cl/42": {{ID: "cl/42:c9", Author: "reviewer", Body: "nit"}},
	}}
	change.Register(provider)
	t.Cleanup(func() { change.Unregister(model.LinkChangelist) })

	store, ctx := openStore(t)
	putCLTask(t, ctx, store, "42")
	if err := store.StartRun(ctx, model.Run{ID: "42-1", TaskID: "42", Attempt: 1, StartedAt: baseTime}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := orchestrator.SyncChangeThreads(ctx, store, baseTime.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if s := stateOf(t, ctx, store, "42"); s != model.StateRunning {
		t.Fatalf("state = %q, want %q", s, model.StateRunning)
	}
	comments, err := store.Comments(ctx, "42")
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 || comments[0].Author.Actor != model.CLThreadsPrincipal ||
		!strings.Contains(comments[0].Body, "while this run is going") {
		t.Fatalf("comments = %+v, want one addendum from %v", comments, model.CLThreadsPrincipal)
	}
}

// A CL task held on its own review's questions waits for its owner, as a
// pull request's does: a reviewer's thread neither records nor sends a run.
func TestCLThreadsWaitOnAHeldReviewsQuestions(t *testing.T) {
	provider := &threadedCLs{threads: map[string][]change.Thread{
		"cl/43": {{ID: "cl/43:c2", Author: "reviewer", Body: "rename"}},
	}}
	change.Register(provider)
	t.Cleanup(func() { change.Unregister(model.LinkChangelist) })

	store, ctx := openStore(t)
	putCLTask(t, ctx, store, "43")
	held := model.ReviewQuestions{ReviewTaskID: "50", AfterCommentID: 1}
	if err := store.UpdateTask(ctx, "43", func(t *model.Task) error {
		t.Links = append(t.Links, model.Link{Kind: model.LinkReviewQuestions, Target: held.Target()})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	done := baseTime
	if err := store.ObserveField(ctx, "43", done, func(o *model.Observation) { o.CompletedAt = &done }); err != nil {
		t.Fatal(err)
	}
	before := stateOf(t, ctx, store, "43")
	if err := orchestrator.SyncChangeThreads(ctx, store, baseTime.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if s := stateOf(t, ctx, store, "43"); s != before {
		t.Fatalf("state = %q, want %q -- a held change waits for its owner", s, before)
	}
	if provider.asked != 0 {
		t.Fatalf("provider asked %d times, want 0", provider.asked)
	}
	for _, l := range reloaded(t, ctx, store, "43").Links {
		if l.Kind == model.LinkCLThread {
			t.Fatalf("thread %s recorded on a held change", l.Target)
		}
	}
}
