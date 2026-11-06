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

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// An awaiting_reply task's API reason says which ask it is parked on,
// the same case the frontend's askedCase draws its badge from: a run that
// answered in a comment is awaiting_reply/answered, not
// awaiting_reply/question, and the list, the one-task read and the
// detail read all agree. The demo seeds both, which is what ui/e2e's
// reason-chip walkthrough reads.
func TestSeedDemoAwaitingReplyReasonsNameTheAsk(t *testing.T) {
	t.Parallel()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	cfg := ui.Config{
		Actor:         ui.DefaultActor("demo-operator"),
		DefaultTarget: &repo,
		Capabilities:  ui.OfferedCapabilities(),
		Reasons:       daemon.ReasonClassifier{},
	}
	if err := seedDemo(ctx, store, cfg); err != nil {
		t.Fatalf("seedDemo: %v", err)
	}
	client := ui.NewClient(cfg, store)
	tasks, err := client.ListTasks(ctx)
	if err != nil {
		t.Fatalf("listing tasks: %v", err)
	}
	seen := map[string]bool{}
	for _, task := range tasks {
		if task.State != model.StateAwaitingReply || task.Paused != nil {
			continue
		}
		// askedCase's order (ui/src/state.js).
		want := "awaiting_reply/answered"
		switch {
		case task.TurnStopped:
			want = "awaiting_reply/stopped"
		case task.AwaitingAccess:
			want = "awaiting_reply/access_request"
		case task.Interactive || task.ConsoleSession != "":
			want = "awaiting_reply/your_message"
		case task.QuestionSummary != "":
			want = "awaiting_reply/question"
		}
		if task.Reason == nil || task.Reason.Reason != want {
			t.Errorf("task %s (%q) lists reason %+v, want %s", task.ID, task.Title, task.Reason, want)
			continue
		}
		wantControl := "Reply"
		if want == "awaiting_reply/access_request" {
			wantControl = "Grant"
		}
		if task.Reason.Control != wantControl || task.Reason.Class != "awaiting a person" {
			t.Errorf("task %s reason %+v, want awaiting a person with %q", task.ID, task.Reason, wantControl)
		}
		seen[want] = true
		one, err := client.Task(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		detail, err := client.GetTask(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		for name, got := range map[string]*ui.Reason{"Task": one.Reason, "GetTask": detail.Reason} {
			if got == nil || got.Reason != want {
				t.Errorf("task %s: %s reason %+v, want the list's %s", task.ID, name, got, want)
			}
		}
	}
	for _, want := range []string{"awaiting_reply/answered", "awaiting_reply/question"} {
		if !seen[want] {
			t.Errorf("the demo seeds no task whose reason is %s (seen %v)", want, seen)
		}
	}
}
