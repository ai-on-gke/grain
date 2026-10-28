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
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// The end-of-run replay honours `supersedes` exactly as the live filing
// path does (ui.Client.CreateTask): the same propose_task call must not
// mean two different things depending on whether the daemon happened to
// be filing it while the run was still going (grain/task-473).
func TestRelayedProposalSupersedesTheTaskItReplaces(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	stale := filedTask(t, ctx, store, "468", repo)

	result := toolResult(agent.ToolCall{Name: "propose_task", Arguments: map[string]any{
		"title": "merge queue design, revision five",
		"body":  "this replaces the fourth attempt",
		// "#"-prefixed, the way agents still write a task reference.
		"supersedes": "#" + stale.ID,
	}})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	proposal := onlyProposal(t, store, task.ID, stale.ID)
	if got := supersedeLink(proposal.Links, model.LinkSupersedes); got != stale.ID {
		t.Errorf("supersedes link on the proposal = %q, want %s", got, stale.ID)
	}
	if strings.Contains(proposal.Body, "still open") {
		t.Errorf("body = %q, want no refusal note on a supersede that happened", proposal.Body)
	}

	state, err := store.State(ctx, stale.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state != model.StateClosed {
		t.Errorf("state of the replaced task = %s, want closed", state)
	}
	replaced, err := store.GetTask(ctx, stale.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := supersedeLink(replaced.Links, model.LinkSupersededBy); got != proposal.ID {
		t.Errorf("superseded-by link = %q, want the replacement (%s)", got, proposal.ID)
	}
	comments := commentBodies(t, ctx, store, stale.ID)
	if len(comments) != 1 || !strings.Contains(comments[0], proposal.ID) {
		t.Errorf("conversation on the closed task = %q, want one comment naming %s",
			comments, proposal.ID)
	}
}

// A supersede grain will not do costs the proposal nothing: the work is
// still filed, the task named is untouched, and the reason is in the
// body the human approving it reads.
func TestRelayedProposalSupersedingADispatchedTaskFilesAnywayWithANote(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	dispatched := filedTask(t, ctx, store, "468", repo)
	if err := store.StartRun(ctx, model.Run{
		ID: "468-1", TaskID: dispatched.ID, Sandbox: "s1", Attempt: 1, StartedAt: baseTime,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, "468-1", baseTime, "succeeded", ""); err != nil {
		t.Fatal(err)
	}

	result := toolResult(agent.ToolCall{Name: "propose_task", Arguments: map[string]any{
		"title": "the same work, restated", "body": "the agent's own words",
		"supersedes": dispatched.ID,
	}})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	proposal := onlyProposal(t, store, task.ID, dispatched.ID)
	if got := supersedeLink(proposal.Links, model.LinkSupersedes); got != "" {
		t.Errorf("supersedes link = %q on a supersede that did not happen", got)
	}
	if !strings.Contains(proposal.Body, "still open") ||
		!strings.Contains(proposal.Body, dispatched.ID) {
		t.Errorf("body = %q, want a note naming the task left open and why", proposal.Body)
	}
	state, err := store.State(ctx, dispatched.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state == model.StateClosed {
		t.Error("a task with a run behind it was closed by a relayed propose_task call")
	}
}

func TestRelayedProposalSupersedesNeverDispatchedDeferredTask(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	stale := filedTask(t, ctx, store, "36", repo)
	wake := baseTime.Add(time.Hour)
	if err := store.SetDeferred(ctx, stale.ID, &baseTime, &wake, baseTime); err != nil {
		t.Fatal(err)
	}
	if st, err := store.State(ctx, stale.ID); err != nil || st != model.StateDeferred {
		t.Fatalf("stale state before relay = (%q, %v), want deferred", st, err)
	}

	result := toolResult(agent.ToolCall{Name: "propose_task", Arguments: map[string]any{
		"title":      "scheduled coordinator, restated",
		"body":       "replaces the deferred task",
		"supersedes": stale.ID,
	}})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	proposal := onlyProposal(t, store, task.ID, stale.ID)
	if got := supersedeLink(proposal.Links, model.LinkSupersedes); got != stale.ID {
		t.Errorf("supersedes link on the proposal = %q, want %s", got, stale.ID)
	}
	if strings.Contains(proposal.Body, "still open") {
		t.Errorf("body = %q, want no refusal note on a supersede that happened", proposal.Body)
	}
	state, err := store.State(ctx, stale.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state != model.StateClosed {
		t.Errorf("state of the replaced deferred task = %s, want closed", state)
	}
	replaced, err := store.GetTask(ctx, stale.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := supersedeLink(replaced.Links, model.LinkSupersededBy); got != proposal.ID {
		t.Errorf("superseded-by link = %q, want %s", got, proposal.ID)
	}
}

func TestRelayedProposalRefusesDeferredTaskWithAttemptOrPR(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	wake := baseTime.Add(time.Hour)

	dispatched := filedTask(t, ctx, store, "37", repo)
	if err := store.SetDeferred(ctx, dispatched.ID, &baseTime, &wake, baseTime); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: "37-1", TaskID: dispatched.ID, Sandbox: "s1", Attempt: 1, StartedAt: baseTime,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, "37-1", baseTime, "failed", "err"); err != nil {
		t.Fatal(err)
	}

	withPR := filedTask(t, ctx, store, "38", repo)
	if err := store.SetDeferred(ctx, withPR.ID, &baseTime, &wake, baseTime); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateTask(ctx, withPR.ID, func(tk *model.Task) error {
		tk.Links = append(tk.Links, model.Link{Kind: model.LinkFixes, Target: "acme/widgets#77"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	result := toolResult(
		agent.ToolCall{Name: "propose_task", Arguments: map[string]any{
			"title": "restate 37", "body": "body 37", "supersedes": dispatched.ID,
		}},
		agent.ToolCall{Name: "propose_task", Arguments: map[string]any{
			"title": "restate 38", "body": "body 38", "supersedes": withPR.ID,
		}},
	)
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	for _, id := range []string{dispatched.ID, withPR.ID} {
		st, err := store.State(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if st != model.StateDeferred {
			t.Errorf("task %s state = %s, want deferred", id, st)
		}
	}
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	proposals := 0
	for _, tk := range tasks {
		if tk.ID == task.ID || tk.ID == dispatched.ID || tk.ID == withPR.ID {
			continue
		}
		proposals++
		if got := supersedeLink(tk.Links, model.LinkSupersedes); got != "" {
			t.Errorf("proposal %s supersedes link = %q, want none", tk.ID, got)
		}
		if !strings.Contains(tk.Body, "still open") {
			t.Errorf("proposal %s body = %q, want refusal note", tk.ID, tk.Body)
		}
	}
	if proposals != 2 {
		t.Fatalf("proposals filed = %d, want 2", proposals)
	}
}

// `supersedes` is one argument in two packages with nothing holding the
// halves together: pkg/mcp declares it in propose_task's schema, and the
// relay above reads that key off the finished call as a string literal
// of its own. A rename on one side is silent on the other -- the run
// makes its call, the tool answers, and the stale task is quietly left
// dispatchable -- which is escape_hatch_arguments_test.go's argument for
// the required arguments, made here for the optional one it does not
// cover.
func TestProposeTaskDeclaresTheSupersedesKeyTheRelayReads(t *testing.T) {
	t.Parallel()
	var tool mcp.Tool
	for _, registered := range mcp.NewMockTools(&mcp.MockSink{}, nil) {
		if registered.Name == "propose_task" {
			tool = registered
		}
	}
	properties, ok := tool.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("propose_task declares no properties: %v", tool.InputSchema)
	}
	if _, ok := properties["supersedes"]; !ok {
		t.Fatalf("propose_task declares no \"supersedes\" argument, and relayProposedTasks "+
			"still reads that key off the call: %v", properties)
	}
}

// onlyProposal is the one task in the store that is neither the task
// that ran nor the one it named in `supersedes`.
func onlyProposal(t *testing.T, store *model.Store, ids ...string) model.Task {
	t.Helper()
	tasks, err := store.ListTasks(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var found []model.Task
	for _, task := range tasks {
		if !contains(ids, task.ID) {
			found = append(found, task)
		}
	}
	if len(found) != 1 {
		t.Fatalf("tasks other than %v = %+v, want exactly the proposal", ids, found)
	}
	return found[0]
}

func contains(ids []string, id string) bool {
	for _, known := range ids {
		if known == id {
			return true
		}
	}
	return false
}

func supersedeLink(links []model.Link, kind model.LinkKind) string {
	for _, l := range links {
		if l.Kind == kind {
			return l.Target
		}
	}
	return ""
}
