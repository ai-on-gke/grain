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
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// reportOnlyChildren has parent t1's run propose two sub-tasks, the
// second depending on the first (by the first's local id), with extra
// laid over the first one's arguments, and approves both the way a
// person would. It returns the parent, the first child and the second.
func reportOnlyChildren(t *testing.T, ctx context.Context, store *model.Store, client github.Client,
	extra map[string]any) (model.Task, model.Task, model.Task) {
	t.Helper()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	parent := filedTask(t, ctx, store, "t1", repo)
	first := map[string]any{"id": "survey", "title": "survey the tests", "body": "report what is missing"}
	for k, v := range extra {
		first[k] = v
	}
	result := toolResult(
		agent.ToolCall{Name: "propose_task", Arguments: first},
		agent.ToolCall{Name: "propose_task", Arguments: map[string]any{
			"title": "rank the gaps", "body": "read the survey's note and rank it",
			"depends_on": []any{"survey"},
		}},
	)
	if err := orchestrator.ProcessResult(ctx, store, client, parent, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult for the parent: %v", err)
	}
	proposals := proposalsByTitle(t, ctx, store, parent.ID)
	survey, rank := proposals["survey the tests"], proposals["rank the gaps"]
	if survey.ID == "" || rank.ID == "" {
		t.Fatalf("proposals = %v, want both filed", proposals)
	}
	if got := dependsOn(rank); len(got) != 1 || got[0] != survey.ID {
		t.Fatalf("second child depends on %v, want %s", got, survey.ID)
	}
	operator := model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "grace"}}
	for _, id := range []string{survey.ID, rank.ID} {
		if err := store.Approve(ctx, id, operator, baseTime); err != nil {
			t.Fatalf("Approve %s: %v", id, err)
		}
	}
	return parent, reportChild(t, ctx, store, survey.ID), reportChild(t, ctx, store, rank.ID)
}

func reportChild(t *testing.T, ctx context.Context, store *model.Store, id string) model.Task {
	t.Helper()
	got, err := store.GetTask(ctx, id)
	if err != nil || got == nil {
		t.Fatalf("GetTask %s = %v, %v", id, got, err)
	}
	return *got
}

// finishChild starts and finishes one run of task the way RunDispatch
// does, then hands result to ProcessResult.
func finishChild(t *testing.T, ctx context.Context, store *model.Store, client github.Client,
	task model.Task, result *agent.Result) {
	t.Helper()
	runID := task.ID + "-1"
	if err := store.StartRun(ctx, model.Run{
		ID: runID, TaskID: task.ID, Sandbox: "s-" + task.ID, Attempt: 1, StartedAt: baseTime,
	}, model.Limits{}); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if err := store.FinishRun(ctx, runID, baseTime, "succeeded", ""); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, runID, baseTime); err != nil {
		t.Fatalf("ProcessResult for %s: %v", task.ID, err)
	}
}

func readyIDs(t *testing.T, ctx context.Context, store *model.Store) []string {
	t.Helper()
	ready, err := store.Ready(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return ready
}

func closingNoteCall(note string) agent.ToolCall {
	return agent.ToolCall{Name: "comment_on_issue", Arguments: map[string]any{"comment": note}}
}

// grain/task-4384: a parent fans its work out to two report-only
// sub-tasks, the second depending on the first. The first answering with
// a closing note and no pull request closes it, with nobody's hand on
// it, so the second is dispatchable at once; the parent's thread points
// at the note, and a wait on the first reads the note back.
func TestAReportOnlySubTaskClosesWhenItAnswersSoItsDependentDispatches(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	parent, survey, rank := reportOnlyChildren(t, ctx, store, client, nil)

	if ready := readyIDs(t, ctx, store); !slices.Contains(ready, survey.ID) || slices.Contains(ready, rank.ID) {
		t.Fatalf("ready = %v, want %s and not %s, which waits on it", ready, survey.ID, rank.ID)
	}

	finishChild(t, ctx, store, client, survey, toolResult(closingNoteCall("three packages have no tests")))

	if st := taskState(t, ctx, store, survey.ID); st != model.StateClosed {
		t.Fatalf("first child state = %q, want closed", st)
	}
	if got := commentBodies(t, ctx, store, survey.ID); len(got) != 2 ||
		got[0] != "three packages have no tests" || got[1] != model.AnsweredClosedNote {
		t.Fatalf("first child's conversation = %q, want its note then the close note", got)
	}
	if ready := readyIDs(t, ctx, store); !slices.Contains(ready, rank.ID) {
		t.Fatalf("ready = %v, want %s now that %s has answered", ready, rank.ID, survey.ID)
	}
	parentSays := strings.Join(commentBodies(t, ctx, store, parent.ID), "\n")
	if !strings.Contains(parentSays, "(/tasks/"+survey.ID+")") {
		t.Errorf("parent's conversation = %q, want a pointer to %s", parentSays, survey.ID)
	}
	target, err := store.WaitTargetAt(ctx, survey.ID, baseTime)
	if err != nil || target == nil {
		t.Fatalf("WaitTargetOf = %v, %v", target, err)
	}
	if target.ClosingNote != "three packages have no tests" || target.Outcome != model.WaitOutcomeCompleted {
		t.Errorf("wait target = %+v, want the child's own note and a completed outcome", target)
	}

	// The dependent dispatched on that close is told what it was waiting
	// for: the first child's id and its closing note, in its own prompt.
	rank = reportChild(t, ctx, store, rank.ID)
	deps := orchestrator.DependenciesOf(ctx, store, rank, nil)
	prompt := orchestrator.BuildPrompt(rank, "", "", true, 0, orchestrator.History{Dependencies: deps}, nil)
	for _, want := range []string{"Task " + survey.ID + " ", "> three packages have no tests"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("second child's prompt lacks %q:\n%s", want, prompt)
		}
	}

	finishChild(t, ctx, store, client, rank, toolResult(closingNoteCall("rank: a, b, c")))
	if st := taskState(t, ctx, store, rank.ID); st != model.StateClosed {
		t.Fatalf("second child state = %q, want closed", st)
	}
}

// A report-only sub-task that asks a question has not answered: it
// parks on the question, and what depends on it stays blocked.
func TestAReportOnlySubTaskThatAsksAQuestionStillBlocks(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	_, survey, rank := reportOnlyChildren(t, ctx, store, client, nil)

	finishChild(t, ctx, store, client, survey, toolResult(
		closingNoteCall("half done"),
		agent.ToolCall{Name: "ask_question", Arguments: map[string]any{"question": "which packages count?"}},
	))

	if st := taskState(t, ctx, store, survey.ID); st != model.StateAwaitingReply {
		t.Fatalf("first child state = %q, want awaiting_reply on its question", st)
	}
	if ready := readyIDs(t, ctx, store); slices.Contains(ready, rank.ID) {
		t.Fatalf("ready = %v, want %s still blocked on %s's question", ready, rank.ID, survey.ID)
	}
}

// A sub-task that pushed a branch follows the pull request path: the pull
// request is opened, the task is not closed on its note, and its
// dependent waits for the change as before.
func TestASubTaskWithAPullRequestFollowsThePullRequestPath(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	_, survey, rank := reportOnlyChildren(t, ctx, store, client, nil)
	pushBranch(t, sim.BareRepo, model.BranchName(survey.ID))

	finishChild(t, ctx, store, client, survey, toolResult(closingNoteCall("added the missing tests")))

	if len(sim.PullRequests) != 1 {
		t.Fatalf("pull requests = %+v, want the child's", sim.PullRequests)
	}
	if st := taskState(t, ctx, store, survey.ID); st == model.StateClosed {
		t.Fatalf("first child state = %q, want it left for its pull request", st)
	}
	if ready := readyIDs(t, ctx, store); slices.Contains(ready, rank.ID) {
		t.Fatalf("ready = %v, want %s still blocked on %s's pull request", ready, rank.ID, survey.ID)
	}
}

// A parent that wants a person to read and close its child says
// nothing_needed_action "ack": the child completes into awaiting_reply
// as a person's task does, and its dependent keeps waiting for that
// close.
func TestASubTaskFiledForAcknowledgementWaitsForAPerson(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	_, survey, rank := reportOnlyChildren(t, ctx, store, client, map[string]any{"nothing_needed_action": "ack"})

	finishChild(t, ctx, store, client, survey, toolResult(closingNoteCall("three packages have no tests")))

	if st := taskState(t, ctx, store, survey.ID); st != model.StateAwaitingReply {
		t.Fatalf("first child state = %q, want awaiting_reply", st)
	}
	if ready := readyIDs(t, ctx, store); slices.Contains(ready, rank.ID) {
		t.Fatalf("ready = %v, want %s blocked until a person closes %s", ready, rank.ID, survey.ID)
	}
}

// A task a person filed is unchanged: its closing note is for them, and
// it completes into awaiting_reply.
func TestAPersonsTaskStillCompletesIntoAwaitingReply(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	task := filedTask(t, ctx, store, "t1", model.RepoRef{Owner: "acme", Name: "widgets"})

	finishChild(t, ctx, store, client, task, toolResult(closingNoteCall("here is what I found")))

	if st := taskState(t, ctx, store, task.ID); st != model.StateAwaitingReply {
		t.Fatalf("state = %q, want awaiting_reply", st)
	}
}

// A sub-task whose run uploaded a changelist has its change linked to the
// stored task while the run is live, after the snapshot ProcessResult is
// handed was taken. Its summary note does not close it: it is a change
// waiting for review and Submit, and what depends on it keeps waiting.
func TestASubTaskWithAChangelistLinkedMidRunIsNotClosedOnItsNote(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	_, survey, rank := reportOnlyChildren(t, ctx, store, client, nil)

	if err := store.UpdateTask(ctx, survey.ID, func(tk *model.Task) error {
		tk.Links = append(tk.Links, model.Link{Kind: model.LinkChangelist, Target: "cl/4242"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	finishChild(t, ctx, store, client, survey, toolResult(closingNoteCall("uploaded cl/4242")))

	if st := taskState(t, ctx, store, survey.ID); st == model.StateClosed {
		t.Fatalf("first child state = %q, want it left for its changelist", st)
	}
	if ready := readyIDs(t, ctx, store); slices.Contains(ready, rank.ID) {
		t.Fatalf("ready = %v, want %s still blocked on %s's changelist", ready, rank.ID, survey.ID)
	}
}

// A park through wait_for_task holds its task with depends-on links too.
// parkedResumeSection already tells the next attempt about those targets,
// so DependenciesOf leaves them out and lists only the task's other
// dependencies.
func TestDependenciesOfLeavesOutAParksOwnTargets(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	parkedOn := filedTask(t, ctx, store, "b1", repo)
	other := filedTask(t, ctx, store, "c1", repo)
	task := filedTask(t, ctx, store, "a1", repo)
	task.Links = append(task.Links,
		model.Link{Kind: model.LinkDependsOn, Target: parkedOn.ID},
		model.Link{Kind: model.LinkDependsOn, Target: other.ID})
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	task = reportChild(t, ctx, store, task.ID)

	resumed := []model.WaitTarget{{ID: parkedOn.ID}}
	deps := orchestrator.DependenciesOf(ctx, store, task, resumed)
	if len(deps) != 1 || deps[0].ID != other.ID {
		t.Fatalf("dependencies = %+v, want only %s", deps, other.ID)
	}
	if all := orchestrator.DependenciesOf(ctx, store, task, nil); len(all) != 2 {
		t.Fatalf("dependencies with no park = %+v, want both", all)
	}
}
