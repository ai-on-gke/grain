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

package security

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite/sqlitetest"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// waitScopeReport is the closing note of the private repository's task:
// the text no run outside that repository may be handed.
const waitScopeReport = "SECURITY REVIEW: the token check in private/vault is bypassable"

// TestWaitTargetsKeepToTheWaitersRepositories holds every way a run is
// told about another task -- wait_for_task (ui.Client.WaitOn, which the
// MCP tool reaches with the daemon's own credential, past the agent API
// gate), a park's resume (orchestrator.ResumedFrom) and a dependent's
// prompt (orchestrator.DependenciesOf, quoting each depends_on target's
// closing note since grain/task-4384) -- to the waiting task's own
// repository and its reads and writes. A run may still wait on a task in
// any repository, but one it cannot read comes back with its title and
// closing note withheld (grain/task-4771).
func TestWaitTargetsKeepToTheWaitersRepositories(t *testing.T) {
	store, _ := sqlitetest.Open(t)
	ctx := context.Background()
	now := time.Now().UTC()
	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	vault := model.RepoRef{Owner: "private", Name: "vault"}
	origin := model.Origin{
		Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "operator"}},
		Reason:      model.ReasonDirect,
	}
	put := func(task model.Task) model.Task {
		t.Helper()
		task.Intent, task.Origin, task.CreatedAt = model.IntentImplement, origin, &now
		if err := store.PutTask(ctx, task); err != nil {
			t.Fatal(err)
		}
		return task
	}
	closeWithNote := func(id string) {
		t.Helper()
		agent := model.Principal{Kind: model.PrincipalAgent, ID: id}
		if _, err := store.AddComment(ctx, model.Comment{TaskID: id, Body: waitScopeReport, CreatedAt: now,
			Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}, OnBehalfOf: &agent}}); err != nil {
			t.Fatal(err)
		}
		if err := store.ObserveField(ctx, id, now, func(o *model.Observation) {
			o.CompletedAt, o.ClosedAt = &now, &now
		}); err != nil {
			t.Fatal(err)
		}
	}

	// The private task, and one with no repository that read the private
	// one: its note is just as much about private/vault.
	review := put(model.Task{ID: "801", Title: "Review private/vault's token check", Target: &vault})
	closeWithNote(review.ID)
	research := put(model.Task{ID: "802", Title: "Research private/vault", Reads: []model.RepoRef{vault}})
	closeWithNote(research.ID)

	outsider := put(model.Task{ID: "811", Title: "widgets work", Target: &widgets,
		Links: []model.Link{{Kind: model.LinkDependsOn, Target: review.ID}, {Kind: model.LinkDependsOn, Target: research.ID}}})
	reader := put(model.Task{ID: "812", Title: "widgets work reading vault", Target: &widgets,
		Reads: []model.RepoRef{vault}, Links: []model.Link{{Kind: model.LinkDependsOn, Target: review.ID}}})
	writer := put(model.Task{ID: "813", Title: "widgets work writing vault", Target: &widgets,
		Writes: []model.WriteTarget{{Repo: vault}}})
	insider := put(model.Task{ID: "814", Title: "vault work", Target: &vault})

	// A task on a non-git workspace has no repository at all, but its note
	// is about that workspace's code: only a task on the same kind of
	// workspace may read it.
	workspace := &model.ChangeTarget{Kind: "changelist"}
	cl := put(model.Task{ID: "803", Title: "Review the workspace's token check", ChangeTarget: workspace})
	closeWithNote(cl.ID)
	clWorker := put(model.Task{ID: "815", Title: "workspace work", ChangeTarget: workspace})
	noRepo := put(model.Task{ID: "816", Title: "repository-less work"})

	client := ui.NewClient(ui.Config{Actor: ui.DefaultActor("operator")}, store)

	withheld := func(how string, targets []model.WaitTarget) {
		t.Helper()
		if len(targets) == 0 {
			t.Fatalf("%s: no targets at all, want them listed with their notes withheld", how)
		}
		for _, target := range targets {
			if !target.Withheld || target.ClosingNote != "" || target.Title != "" {
				t.Errorf("%s: target %s = %+v, want its title and closing note withheld", how, target.ID, target)
			}
			if !target.Closed || target.Outcome != model.WaitOutcomeCompleted {
				t.Errorf("%s: target %s = %+v, want how it ended still told", how, target.ID, target)
			}
			if d := target.Describe(); strings.Contains(d, waitScopeReport) || strings.Contains(d, "token check") {
				t.Errorf("%s: target %s describes as %q, leaking its record", how, target.ID, d)
			}
		}
	}
	shown := func(how string, targets []model.WaitTarget) {
		t.Helper()
		if len(targets) == 0 {
			t.Fatalf("%s: no targets", how)
		}
		for _, target := range targets {
			if target.Withheld || target.ClosingNote != waitScopeReport {
				t.Errorf("%s: target %s = %+v, want its closing note", how, target.ID, target)
			}
		}
	}

	// wait_for_task, looking and parking alike.
	for _, park := range []bool{false, true} {
		got, err := client.WaitOn(ctx, outsider.ID, []string{review.ID, research.ID}, park)
		if err != nil {
			t.Fatalf("WaitOn (park %v): %v", park, err)
		}
		withheld("wait_for_task from acme/widgets", got)
	}
	comments, err := store.Comments(ctx, outsider.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range comments {
		if strings.Contains(c.Body, review.Title) || strings.Contains(c.Body, research.Title) {
			t.Errorf("the park comment on %s names a withheld target's title: %q", outsider.ID, c.Body)
		}
	}
	for _, in := range []model.Task{reader, writer, insider} {
		got, err := client.WaitOn(ctx, in.ID, []string{review.ID}, false)
		if err != nil {
			t.Fatalf("WaitOn from %s: %v", in.ID, err)
		}
		shown("wait_for_task from "+in.Title, got)
	}
	for _, out := range []model.Task{outsider, noRepo} {
		got, err := client.WaitOn(ctx, out.ID, []string{cl.ID}, false)
		if err != nil {
			t.Fatalf("WaitOn %s from %s: %v", cl.ID, out.ID, err)
		}
		withheld("wait_for_task on a workspace task from "+out.Title, got)
	}
	got, err := client.WaitOn(ctx, clWorker.ID, []string{cl.ID}, false)
	if err != nil {
		t.Fatalf("WaitOn %s from %s: %v", cl.ID, clWorker.ID, err)
	}
	shown("wait_for_task on a workspace task from "+clWorker.Title, got)

	// A dependent's prompt.
	waiter, err := store.GetTask(ctx, outsider.ID)
	if err != nil {
		t.Fatal(err)
	}
	withheld("DependenciesOf from acme/widgets", orchestrator.DependenciesOf(ctx, store, *waiter, nil))
	prompt := orchestrator.BuildPrompt(*waiter, "work", "", true, 0,
		orchestrator.History{Attempt: 1, Dependencies: orchestrator.DependenciesOf(ctx, store, *waiter, nil)}, nil)
	if strings.Contains(prompt, waitScopeReport) {
		t.Errorf("the prompt of a task in acme/widgets quotes private/vault's closing note:\n%s", prompt)
	}
	shown("DependenciesOf from a task reading private/vault", orchestrator.DependenciesOf(ctx, store, reader, nil))

	// A park's resume.
	if err := store.StartRun(ctx, model.Run{ID: outsider.ID + "-1", TaskID: outsider.ID, Sandbox: "s", Attempt: 1,
		StartedAt: now}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, outsider.ID+"-1", now, model.ParkedOutcome,
		model.ParkedDetail([]string{review.ID, research.ID})); err != nil {
		t.Fatal(err)
	}
	runs, err := store.Runs(ctx, outsider.ID)
	if err != nil {
		t.Fatal(err)
	}
	withheld("ResumedFrom from acme/widgets", orchestrator.ResumedFrom(ctx, store, *waiter, runs))
}
