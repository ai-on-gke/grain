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

package demo

import (
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// An owner's reply to the seeded review questions sends the held change
// back to the queue, as a daemon's cycle would. Before -review-questions
// ran the reconciler, the reply was written and the change stayed on its
// hold, awaiting a decision it already had, for as long as the demo ran.
func TestDemoReviewQuestionsRequeuesOnTheOwnersReply(t *testing.T) {
	t.Parallel()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	cfg := ui.Config{Actor: ui.DefaultActor("demo-operator"), DefaultTarget: &repo}
	if err := seedReviewQuestionsDemo(ctx, store, cfg, repo, ""); err != nil {
		t.Fatalf("seedReviewQuestionsDemo: %v", err)
	}
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatalf("listing tasks: %v", err)
	}
	var held *model.Task
	for i := range tasks {
		if tasks[i].Title == "Publish both daemon images" {
			held = &tasks[i]
		}
	}
	if held == nil {
		t.Fatalf("seeded tasks = %v, want the held change", tasks)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		runDemoReviewQuestions(ctx, store, 10*time.Millisecond)
	}()

	// Unanswered, the hold stays where it is.
	time.Sleep(100 * time.Millisecond)
	if state, err := store.State(ctx, held.ID); err != nil || state != model.StateAwaitingSubmit {
		t.Fatalf("unanswered held change state = %q, %v; want %q", state, err, model.StateAwaitingSubmit)
	}

	if _, err := store.AddComment(ctx, model.Comment{
		TaskID:    held.ID,
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "demo-operator"}},
		Body:      "Keep the core image core-only.",
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("replying: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		state, err := store.State(ctx, held.ID)
		if err != nil {
			t.Fatalf("State: %v", err)
		}
		if state == model.StateQueued {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("10s after the owner's reply the held change is %q, want %q", state, model.StateQueued)
		}
		time.Sleep(10 * time.Millisecond)
	}
	got, err := store.GetTask(ctx, held.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if _, ok := model.ReviewQuestionsOf(*got); ok {
		t.Error("requeued change still carries its review-questions hold")
	}
	if !got.AutoMerge {
		t.Error("requeued change's auto-merge was not restored")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("runDemoReviewQuestions did not return once its context was done")
	}
}
