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

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// nothingNeededRun files a task with the given NothingNeededAction, starts
// and finishes one run for it the way RunDispatch does (as "succeeded",
// outcomeOf's guess), and returns both so ProcessResult can be handed the
// run's result.
func nothingNeededRun(t *testing.T, ctx context.Context, store *model.Store,
	action model.NothingNeededAction) (model.Task, string) {
	t.Helper()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	task.NothingNeededAction = action
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	runID := "t1-1"
	if err := store.StartRun(ctx, model.Run{
		ID: runID, TaskID: task.ID, Sandbox: "s1", Attempt: 1, StartedAt: baseTime,
	}, model.Limits{}); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if err := store.FinishRun(ctx, runID, baseTime, "succeeded", ""); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	return task, runID
}

func nothingNeededCall(reason string) agent.ToolCall {
	return agent.ToolCall{Name: "nothing_needed", Arguments: map[string]any{"reason": reason}}
}

// runEnding is how the task's one run reads back through model.EndingOf.
func runEnding(t *testing.T, ctx context.Context, store *model.Store, taskID string) model.RunEnding {
	t.Helper()
	runs, err := store.Runs(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %+v, want one", runs)
	}
	return model.EndingOf(runs[0].Outcome, runs[0].Detail)
}

// A task left at its default -- the field empty, as every task filed
// before the setting existed is -- and one that says "close" outright
// both go straight to closed: the reason is the closing comment, grain
// notes why it closed, and the run reads as nothing_needed rather than
// as a success or a no-op.
func TestProcessResultNothingNeededClosesTheTask(t *testing.T) {
	t.Parallel()
	for _, action := range []model.NothingNeededAction{model.NothingNeededClose, ""} {
		t.Run("action="+string(action), func(t *testing.T) {
			t.Parallel()
			store, ctx := openStore(t)
			_, client := newSim(t, "acme", "widgets", "main")
			task, runID := nothingNeededRun(t, ctx, store, action)

			result := toolResult(nothingNeededCall("the flag is already off on main"))
			if err := orchestrator.ProcessResult(ctx, store, client, task, result, runID, baseTime); err != nil {
				t.Fatalf("ProcessResult: %v", err)
			}

			if st := taskState(t, ctx, store, task.ID); st != model.StateClosed {
				t.Fatalf("state = %q, want closed", st)
			}
			got := commentBodies(t, ctx, store, task.ID)
			if len(got) != 2 || got[0] != "the flag is already off on main" || got[1] != model.NothingNeededClosedNote {
				t.Fatalf("conversation = %q, want the reason then the close note", got)
			}
			if e := runEnding(t, ctx, store, task.ID); e != model.EndingNothingNeeded {
				t.Fatalf("ending = %q, want nothing_needed", e)
			}
			// A run that said nothing was needed did its job: it is no
			// failure for the streak to count.
			streak, err := store.FailureStreak(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if streak != nil && streak.Count != 0 {
				t.Fatalf("failure streak = %+v, want none", streak)
			}
		})
	}
}

// "ack" keeps the old behaviour: the task completes into awaiting_ack
// with the reason as its comment, and only the run's ending differs.
func TestProcessResultNothingNeededLeftForAcknowledgement(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	task, runID := nothingNeededRun(t, ctx, store, model.NothingNeededAck)

	result := toolResult(nothingNeededCall("nothing to change"))
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, runID, baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	if st := taskState(t, ctx, store, task.ID); st != model.StateAwaitingReply {
		t.Fatalf("state = %q, want awaiting_reply", st)
	}
	if got := commentBodies(t, ctx, store, task.ID); len(got) != 1 || got[0] != "nothing to change" {
		t.Fatalf("conversation = %q, want the reason alone", got)
	}
	if e := runEnding(t, ctx, store, task.ID); e != model.EndingNothingNeeded {
		t.Fatalf("ending = %q, want nothing_needed", e)
	}
}

// Pushed commits win: nothing_needed never discards work. The pull
// request is opened as for any pushed branch, the reason is posted beside
// it, the task is not closed, and the run keeps RunDispatch's outcome.
func TestProcessResultNothingNeededDoesNotDiscardAPushedBranch(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	task, runID := nothingNeededRun(t, ctx, store, "")
	pushBranch(t, sim.BareRepo, model.BranchName(task.ID))

	result := toolResult(
		agent.ToolCall{Name: "run_command", Text: "pushed"},
		nothingNeededCall("on reflection nothing was needed"),
	)
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, runID, baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	if len(sim.PullRequests) != 1 {
		t.Fatalf("pull requests = %+v, want the pushed branch's", sim.PullRequests)
	}
	if got := commentBodies(t, ctx, store, task.ID); len(got) != 1 || got[0] != "on reflection nothing was needed" {
		t.Fatalf("conversation = %q, want the reason posted beside the pull request", got)
	}
	if st := taskState(t, ctx, store, task.ID); st != model.StateAwaitingSubmit {
		t.Fatalf("state = %q, want awaiting_submit", st)
	}
	if e := runEnding(t, ctx, store, task.ID); e != model.EndingSucceeded {
		t.Fatalf("ending = %q, want succeeded", e)
	}
}

// A question outranks it too: the task parks on the question, unclosed.
func TestProcessResultNothingNeededDoesNotDiscardAQuestion(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	task, runID := nothingNeededRun(t, ctx, store, "")

	result := toolResult(
		nothingNeededCall("probably nothing needed"),
		agent.ToolCall{Name: "ask_question", Arguments: map[string]any{"question": "or did you mean the other flag?"}},
	)
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, runID, baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	if st := taskState(t, ctx, store, task.ID); st != model.StateAwaitingReply {
		t.Fatalf("state = %q, want awaiting_reply", st)
	}
	if e := runEnding(t, ctx, store, task.ID); e == model.EndingNothingNeeded {
		t.Fatalf("ending = %q, want the run's own outcome kept", e)
	}
}

// staleBranchRun files a task whose first attempt pushed its branch --
// and, with openPR, had a pull request opened for it, as a change in
// review has; without, it parked or ran out of turns before one was --
// then starts and finishes a second run the way RunDispatch does,
// returning that run and the branch head it was dispatched with (runOne's
// preRunBranchSHA).
func staleBranchRun(t *testing.T, ctx context.Context, store *model.Store, sim *githubsim.Sim,
	client github.Client, openPR bool) (model.Task, string, string) {
	t.Helper()
	task, firstRun := nothingNeededRun(t, ctx, store, "")
	pushBranch(t, sim.BareRepo, model.BranchName(task.ID))
	if openPR {
		pushed := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
		if err := orchestrator.ProcessResult(ctx, store, client, task, pushed, firstRun, baseTime); err != nil {
			t.Fatalf("ProcessResult for the first attempt: %v", err)
		}
		if len(sim.PullRequests) != 1 {
			t.Fatalf("pull requests = %+v, want the first attempt's", sim.PullRequests)
		}
	}
	head, err := client.GetBranchHead("acme", "widgets", model.BranchName(task.ID))
	if err != nil || head == nil {
		t.Fatalf("GetBranchHead = %v, %v", head, err)
	}
	runID := "t1-2"
	if err := store.StartRun(ctx, model.Run{
		ID: runID, TaskID: task.ID, Sandbox: "s2", Attempt: 2, StartedAt: baseTime,
	}, model.Limits{}); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if err := store.FinishRun(ctx, runID, baseTime, "succeeded", ""); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	got, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	return *got, runID, head.SHA
}

// latestRunEnding is how the task's newest run reads back through
// model.EndingOf.
func latestRunEnding(t *testing.T, ctx context.Context, store *model.Store, taskID, runID string) model.RunEnding {
	t.Helper()
	runs, err := store.Runs(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range runs {
		if r.ID == runID {
			return model.EndingOf(r.Outcome, r.Detail)
		}
	}
	t.Fatalf("runs = %+v, want %s among them", runs, runID)
	return ""
}

// A branch an earlier attempt pushed is not this run's work: a run that
// says nothing_needed and pushes nothing on top of it closes the task,
// rather than having a pull request salvaged off the stale branch and
// the task dispatched again to merge it (grain/task-208's PR #4489). The
// task is told which branch was left behind.
func TestProcessResultNothingNeededWinsOverABranchAnEarlierAttemptPushed(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	task, runID, head := staleBranchRun(t, ctx, store, sim, client, false)

	result := toolResult(nothingNeededCall("already removed on main by 464a4bbcb"))
	if err := orchestrator.ProcessResultFrom(ctx, store, nil, client, task, result, runID, head, baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	if len(sim.PullRequests) != 0 {
		t.Fatalf("pull requests = %+v, want none opened off the stale branch", sim.PullRequests)
	}
	if st := taskState(t, ctx, store, task.ID); st != model.StateClosed {
		t.Fatalf("state = %q, want closed", st)
	}
	if e := latestRunEnding(t, ctx, store, task.ID, runID); e != model.EndingNothingNeeded {
		t.Fatalf("ending = %q, want nothing_needed", e)
	}
	got := commentBodies(t, ctx, store, task.ID)
	branchNoted := false
	for _, body := range got {
		if strings.Contains(body, "`"+task.BranchName()+"`") && strings.Contains(body, "left in place") {
			branchNoted = true
		}
	}
	if !branchNoted {
		t.Fatalf("conversation = %q, want a note naming the branch left behind", got)
	}
}

// A pull request already open off the branch is a change in review, and a
// run sent back to it that finds nothing more to do -- a reviewer's point
// that needs no change, a CI red that was a flake -- does not get to close
// it, or the task, on its say-so: the pull request stays open and the task
// stays waiting on its owner, as it did before nothing_needed could
// outrank a branch.
func TestProcessResultNothingNeededLeavesAPullRequestInReviewOpen(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	task, runID, head := staleBranchRun(t, ctx, store, sim, client, true)

	result := toolResult(nothingNeededCall("the reviewer's point needs no change"))
	if err := orchestrator.ProcessResultFrom(ctx, store, nil, client, task, result, runID, head, baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	if len(sim.PullRequests) != 1 || sim.PullRequests[0].State == "closed" {
		t.Fatalf("pull requests = %+v, want the one in review still open", sim.PullRequests)
	}
	if st := taskState(t, ctx, store, task.ID); st != model.StateAwaitingSubmit {
		t.Fatalf("state = %q, want awaiting_submit", st)
	}
	if e := latestRunEnding(t, ctx, store, task.ID, runID); e == model.EndingNothingNeeded {
		t.Fatalf("ending = %q, want the run's own outcome kept", e)
	}
}

// The same run with a push of its own on top of the earlier attempt's
// branch keeps today's rule: pushed work wins over nothing_needed, and
// goes through the ordinary pull request flow.
func TestProcessResultNothingNeededDoesNotDiscardANewPushOnAnEarlierBranch(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	task, runID, head := staleBranchRun(t, ctx, store, sim, client, true)
	pushBranch(t, sim.BareRepo, task.BranchName())

	result := toolResult(
		agent.ToolCall{Name: "run_command", Text: "pushed"},
		nothingNeededCall("on reflection nothing was needed"),
	)
	if err := orchestrator.ProcessResultFrom(ctx, store, nil, client, task, result, runID, head, baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	if len(sim.PullRequests) != 1 || sim.PullRequests[0].State == "closed" {
		t.Fatalf("pull requests = %+v, want the branch's one still open", sim.PullRequests)
	}
	if st := taskState(t, ctx, store, task.ID); st != model.StateAwaitingSubmit {
		t.Fatalf("state = %q, want awaiting_submit", st)
	}
	if e := latestRunEnding(t, ctx, store, task.ID, runID); e != model.EndingSucceeded {
		t.Fatalf("ending = %q, want succeeded", e)
	}
}
