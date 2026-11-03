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

// The 2026-10-30 plan's §7 gaps 3 and 5, which grain demo's agent cannot
// drive -- it never runs, so nothing ends a run through ProcessResult
// there (docs/test-runs/2026-10-31-task-targeting-parking.md). Both go
// through a run's real end, ProcessResult, rather than the helpers under
// it that wait_for_task_test.go and tuning_test.go already cover.

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// A run that parks with resume_later on another task: the reason it gave
// is in the run's entry in the task's conversation (the attempt detail
// DetailOverlay's timeline draws, read here through ui.Client.GetTask as
// GET /api/tasks/{id} serves it) and in the next run's prompt once the
// task it waited on has merged.
func TestResumeLaterReasonIsQuotedInTheConversationAndTheNextPrompt(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	dep := filedTask(t, ctx, store, "dep", repo)
	task := filedTask(t, ctx, store, "parks", repo)
	const reason = "Waiting on #dep's schema migration before touching the reader"

	if err := store.StartRun(ctx, model.Run{ID: "parks-1", TaskID: task.ID, Sandbox: "s1", Attempt: 1, StartedAt: baseTime}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	// What ui.Client.ResumeLater does when the tool is called: the
	// depends-on link the park waits on.
	linked := task
	linked.Links = append(slices.Clone(linked.Links), model.Link{Kind: model.LinkDependsOn, Target: dep.ID})
	if err := store.PutTask(ctx, linked); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, "parks-1", baseTime, "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	when := model.ResumeOn{Tasks: []string{dep.ID}}
	res := toolResult(agent.ToolCall{
		Name:      "resume_later",
		Arguments: map[string]any{"reason": reason, "when": map[string]any{"task": dep.ID}},
		Text:      "Parked. This task is queued again once #" + dep.ID + " has closed.\n\nresume_on: " + when.JSON(),
	})
	if err := orchestrator.ProcessResult(ctx, store, client, linked, res, "parks-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	view, err := ui.NewClient(ui.Config{Actor: ui.DefaultActor("alice")}, store).GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Attempts) != 1 || !strings.Contains(view.Attempts[0].Detail, reason) {
		t.Fatalf("conversation's attempts = %+v, want run 1's entry to quote %q", view.Attempts, reason)
	}

	closedAt := baseTime.Add(time.Minute)
	if err := store.ObserveField(ctx, dep.ID, closedAt, func(o *model.Observation) {
		o.CompletedAt, o.ClosedAt = &closedAt, &closedAt
	}); err != nil {
		t.Fatal(err)
	}
	runs, err := store.Runs(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	prompt := orchestrator.BuildPrompt(linked, "work", "", true, 0,
		orchestrator.History{Attempt: 2, Attempts: runs, Resumed: orchestrator.ResumedFrom(ctx, store, runs)}, nil)
	for _, want := range []string{reason, "resumes from a park", "Task " + dep.ID} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("next run's prompt lacks %q:\n%s", want, prompt)
		}
	}
}

// A system:infra-review run that ends proposing (a) a fix in a source
// repository the deployment works in, (b) a change to a repository it
// does not, and (c) a change to grain/config naming no parameter: only
// (a) is filed, and the run's conversation says why the other two were
// not.
func TestInfraReviewRunFilesOnlyItsSourceRepoProposal(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.SetTargetRepos(ctx, []string{repo.String()}); err != nil {
		t.Fatal(err)
	}
	review := model.Task{
		ID: "infra", Intent: model.IntentImplement, Title: "Nightly infra review",
		TemplateID: model.InfraReviewTemplateID,
		Origin:     model.Origin{Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "infra-review"}}, Reason: model.ReasonDirect},
		Reads:      []model.RepoRef{model.ConfigRepo()},
		Binding:    model.BindingDirective,
	}
	if err := store.PutTask(ctx, review); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{ID: "infra-1", TaskID: review.ID, Sandbox: "s1", Attempt: 1, StartedAt: baseTime}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, "infra-1", baseTime, "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	propose := func(title, target string) agent.ToolCall {
		return agent.ToolCall{Name: "propose_task", Arguments: map[string]any{
			"title": title, "body": "From the infra review.", "repo": target,
		}}
	}
	res := toolResult(
		propose("fix(ci): classify the sandbox forward timeout as infrastructure", repo.String()),
		propose("Retire the old dashboards", "unknown/elsewhere"),
		propose("Update grain/config without naming a parameter", model.ConfigRepo().String()),
		agent.ToolCall{Name: "comment_on_issue", Arguments: map[string]any{"comment": "Reviewed last night's runs."}},
	)
	if err := orchestrator.ProcessResult(ctx, store, client, review, res, "infra-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var filed []string
	for _, tk := range tasks {
		if tk.ID == review.ID {
			continue
		}
		filed = append(filed, tk.Title)
		if tk.Target == nil || !tk.Target.Same(repo) {
			t.Errorf("filed %q against %v, want %s", tk.Title, tk.Target, repo)
		}
	}
	if len(filed) != 1 || !strings.Contains(filed[0], "sandbox forward timeout") {
		t.Fatalf("filed = %q, want only the source-repo fix", filed)
	}
	said := strings.Join(commentBodies(t, ctx, store, review.ID), "\n---\n")
	for _, want := range []string{"Retire the old dashboards", "Update grain/config without naming a parameter", "names no grain_config parameter"} {
		if !strings.Contains(said, want) {
			t.Errorf("infra review's conversation does not say %q:\n%s", want, said)
		}
	}
}
