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

package ui_test

// The operator's override of the merge queue: Client.ForceMerge, which
// the UI's "Force merge" button and `grain force-merge` both come
// through, and which is the one sanctioned exception to the merge
// predicate (orchestrator's mergepredicate.go, grain/task-479).
//
// What is being tested here is a door and its lock. The door: a request
// with a reason is recorded for the next cycle to act on, with what the
// queue was saying at the time captured beside it. The lock: no reason,
// no override -- and no asking for one on a task nothing could merge.
// The merge itself is the orchestrator's, and is tested there.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// stuckHead is the commit grain CI judged on stuckTask's pull request,
// which is what the task page shows an operator as its head.
const stuckHead = "c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f6"

// stuckTask is the task this control exists for: a pull request open,
// auto-merge on, the run finished, and the merge queue holding it back
// because grain CI failed on the head -- which is exactly what the row
// and the CI record say.
func stuckTask(t *testing.T, c *ui.Client, store *model.Store, ctx context.Context) (ui.Task, string) {
	t.Helper()
	task := create(t, c, ctx)
	ref := "acme/widgets#42"
	if err := store.UpdateTask(ctx, task.ID, func(tk *model.Task) error {
		tk.Links = append(tk.Links, model.Link{Kind: model.LinkFixes, Target: ref})
		tk.AutoMerge = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	completedAt := baseTime.Add(time.Hour)
	if err := store.Observe(ctx, model.Observation{
		TaskID:            task.ID,
		CompletedAt:       &completedAt,
		MergeQueueState:   "repair_needed",
		MergeQueueNote:    "`go` is failing on this branch's own head",
		MergeQueueStateAt: &completedAt,
		CI: &model.CIStatus{
			RunID: "run-7", Commit: stuckHead, Conclusion: "failure",
			Detail: "go failed", FinishedAt: &completedAt,
		},
	}); err != nil {
		t.Fatal(err)
	}
	return task, ref
}

// TestForceMergeRecordsTheOverrideForTheNextCycle is the door: what the
// button writes is a request, not a merge, and the request carries
// everything the record of it will need.
func TestForceMergeRecordsTheOverrideForTheNextCycle(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task, ref := stuckTask(t, c, store, ctx)

	if err := c.ForceMerge(ctx, task.ID, "  the release is broken and this is the fix  ", ""); err != nil {
		t.Fatal(err)
	}

	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.ForceMergeAt == nil {
		t.Fatal("no override recorded: nothing would ever merge this")
	}
	if obs.ForceMergeReason != "the release is broken and this is the fix" {
		t.Fatalf("reason = %q, want the operator's own words, trimmed", obs.ForceMergeReason)
	}
	if obs.ForceMergeBy != "alice" {
		t.Fatalf("by = %q, want whoever pressed it", obs.ForceMergeBy)
	}
	// Which commit it is for: with nothing named and no GitHub to ask,
	// the one the page was showing -- grain CI's own.
	if obs.ForceMergeHeadSHA != stuckHead {
		t.Fatalf("head = %q, want the commit the operator was looking at (%s)",
			obs.ForceMergeHeadSHA, stuckHead)
	}
	// What it skipped, captured now rather than at the merge: by then the
	// row says "merging" and the reason it was not merging is gone.
	if !strings.Contains(obs.ForceMergeSkipped, "`go` is failing") {
		t.Fatalf("skipped = %q, want the queue's own account of why it was not merging",
			obs.ForceMergeSkipped)
	}
	if !strings.Contains(obs.ForceMergeSkipped, "failure") {
		t.Fatalf("skipped = %q, want the conclusion the check reached", obs.ForceMergeSkipped)
	}
	// Nothing about the change's own state is touched: the override
	// authorises a merge, it does not make anything green.
	if obs.CI.Green() {
		t.Fatal("the CI verdict was changed by an override, which is not what one is")
	}
	if obs.CompletedAt == nil {
		t.Fatal("CompletedAt cleared by an override: the task's own run still finished")
	}

	// And it says so where a person -- and any agent later handed this
	// thread -- will read it, naming the pull request it is about.
	detail, err := c.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Comments) != 1 {
		t.Fatalf("comments = %d, want the override said in the task's own thread", len(detail.Comments))
	}
	body := detail.Comments[0].Body
	if !strings.Contains(body, ref) || !strings.Contains(body, "the release is broken") {
		t.Fatalf("comment = %q, want it to name %s and give the reason", body, ref)
	}
	if detail.Comments[0].Author != "alice" {
		t.Fatalf("comment author = %q, want the person who asked for it", detail.Comments[0].Author)
	}
}

// TestForceMergeRefusesWithoutAReason is the lock, and the one refusal
// that is not about the task at all: an override nobody wrote a reason
// for is indistinguishable afterwards from a queue that merged something
// it should not have.
func TestForceMergeRefusesWithoutAReason(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task, _ := stuckTask(t, c, store, ctx)

	for _, reason := range []string{"", "   ", "\n"} {
		err := c.ForceMerge(ctx, task.ID, reason, "")
		var ve *ui.ValidationError
		if !errors.As(err, &ve) {
			t.Fatalf("ForceMerge(%q) = %v, want a ValidationError", reason, err)
		}
		if !strings.Contains(err.Error(), "reason") {
			t.Fatalf("err = %q, want it to say what is missing", err)
		}
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.ForceMergeAt != nil {
		t.Fatal("a refused request still recorded an override: a refusal writes nothing")
	}
	detail, err := c.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Comments) != 0 {
		t.Fatalf("comments = %d, want a refusal to leave nothing behind", len(detail.Comments))
	}
}

// TestForceMergeRefusesATaskWithNoPullRequest: there is nothing to
// merge, so there is nothing to override.
func TestForceMergeRefusesATaskWithNoPullRequest(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	task := create(t, c, ctx)

	err := c.ForceMerge(ctx, task.ID, "because I said so", "")
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v, want a ValidationError for a task with no pull request", err)
	}
}

// TestForceMergeRefusesAClosedTask: a closed task is out of
// Store.OpenPullRequestLinks, so no cycle would ever read the stamp this
// writes -- the same refusal RetryMerge makes, for the same reason.
func TestForceMergeRefusesAClosedTask(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task, _ := stuckTask(t, c, store, ctx)
	if err := c.Close(ctx, task.ID, ui.CloseOptions{}); err != nil {
		t.Fatal(err)
	}

	err := c.ForceMerge(ctx, task.ID, "the release is broken and this is the fix", "")
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v, want a ValidationError for a closed task", err)
	}
	if !strings.Contains(err.Error(), "reopen") {
		t.Fatalf("err = %q, want it to say what to do about it", err)
	}
}

// inOpenPullRequestLinks reports whether the merge queue's next cycle
// would visit id at all (Store.OpenPullRequestLinks).
func inOpenPullRequestLinks(t *testing.T, store *model.Store, ctx context.Context, id string) bool {
	t.Helper()
	links, err := store.OpenPullRequestLinks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range links {
		if l.TaskID == id {
			return true
		}
	}
	return false
}

// hungOnCI is stuckTask as it looks while its run sits in
// wait_for_checks: the run still live and grain CI still going.
func hungOnCI(t *testing.T, c *ui.Client, store *model.Store, ctx context.Context) (ui.Task, string) {
	t.Helper()
	task, ref := stuckTask(t, c, store, ctx)
	startRunOn(t, store, ctx, task.ID)
	if err := store.ObserveField(ctx, task.ID, baseTime, func(o *model.Observation) {
		o.MergeQueueState, o.MergeQueueNote = "", ""
		o.CI = &model.CIStatus{RunID: "run-8", Commit: stuckHead}
	}); err != nil {
		t.Fatal(err)
	}
	return task, ref
}

// TestForceMergeStopsARunHungOnCI is the case the button is most often
// wanted for: an agent blocked in wait_for_checks on CI that will not
// finish. Forcing the merge ends the run, cancels the CI, and finishes
// the task so the next cycle picks it up -- pinned to the commit the
// operator saw.
func TestForceMergeStopsARunHungOnCI(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	runner := &fakeCIRunner{}
	c.Config.CI = runner
	task, _ := hungOnCI(t, c, store, ctx)
	if inOpenPullRequestLinks(t, store, ctx, task.ID) {
		t.Fatal("a running task is already on the merge queue's walk; the test proves nothing")
	}

	if err := c.ForceMerge(ctx, task.ID, "CI is hung and this is the fix", ""); err != nil {
		t.Fatal(err)
	}

	state, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state != model.StateCompleted {
		t.Fatalf("state = %s, want completed", state)
	}
	runs, err := store.Runs(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range runs {
		if r.FinishedAt == nil {
			t.Fatalf("run %s still open: forcing the merge did not stop it", r.ID)
		}
	}
	if !inOpenPullRequestLinks(t, store, ctx, task.ID) {
		t.Fatal("the task is not on the merge queue's walk, so nothing would ever merge it")
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.ForceMergeAt == nil || obs.ForceMergeHeadSHA != stuckHead {
		t.Fatalf("override = %v @ %q, want it pinned to %s", obs.ForceMergeAt, obs.ForceMergeHeadSHA, stuckHead)
	}
	if len(runner.cancelled) != 1 || runner.cancelled[0] != "run-8" || runner.sources[0] != ci.CancelBySystem {
		t.Fatalf("cancelled = %v by %v, want the hung CI run-8 cancelled by grain", runner.cancelled, runner.sources)
	}
}

// linkingPullRequests opens a pull request the way
// orchestrator.OpenPullRequestForTask does as far as the store sees it:
// by linking it to the task.
type linkingPullRequests struct {
	store *model.Store
	ref   string
	asked []string
}

func (f *linkingPullRequests) OpenForTask(ctx context.Context, taskID string) (ui.PullRequestStatus, error) {
	f.asked = append(f.asked, taskID)
	return ui.PullRequestStatus{}, f.store.UpdateTask(ctx, taskID, func(tk *model.Task) error {
		tk.Links = append(tk.Links, model.Link{Kind: model.LinkFixes, Target: f.ref})
		return nil
	})
}

// TestForceMergeOpensThePullRequestARunNeverAskedFor: the run pushed its
// branch and went straight to wait_for_checks, so there is no pull
// request yet. Forcing the merge opens it rather than refusing.
func TestForceMergeOpensThePullRequestARunNeverAskedFor(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task, ref := hungOnCI(t, c, store, ctx)
	if err := store.UpdateTask(ctx, task.ID, func(tk *model.Task) error {
		tk.Links = nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	opener := &linkingPullRequests{store: store, ref: ref}
	c.Config.PullRequests = opener

	if err := c.ForceMerge(ctx, task.ID, "CI is hung and this is the fix", ""); err != nil {
		t.Fatal(err)
	}
	if len(opener.asked) != 1 || opener.asked[0] != task.ID {
		t.Fatalf("opened for %v, want the task's own pull request opened once", opener.asked)
	}
	if !inOpenPullRequestLinks(t, store, ctx, task.ID) {
		t.Fatal("the task is not on the merge queue's walk after its pull request was opened")
	}
	detail, err := c.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(detail.Comments); n == 0 || !strings.Contains(detail.Comments[n-1].Body, ref) {
		t.Fatalf("comments = %+v, want the override to name %s", detail.Comments, ref)
	}
}

// TestForceMergeRefusesARunWithNothingToMerge: a run with no pull
// request and no way to open one has nothing to force, and is left
// running rather than stopped for nothing.
func TestForceMergeRefusesARunWithNothingToMerge(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task, _ := hungOnCI(t, c, store, ctx)
	if err := store.UpdateTask(ctx, task.ID, func(tk *model.Task) error {
		tk.Links = nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	err := c.ForceMerge(ctx, task.ID, "CI is hung and this is the fix", "")
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v, want a ValidationError", err)
	}
	state, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state != model.StateRunning {
		t.Fatalf("state = %s, want a refused request to leave the run alone", state)
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.ForceMergeAt != nil {
		t.Fatal("a refused request still recorded an override")
	}
}

// TestForceMergeRefusesARunWhoseBranchMovedOnFromTheReviewedHead is the
// pin to the reviewed head (bwsalmon/grain#2399) where it matters most:
// the run is live and has pushed past the commit the operator saw. The
// request is refused at the door, and refused before anything is done
// to the run -- it is not stopped, its CI is not cancelled, nothing is
// stamped and the task is not finished.
func TestForceMergeRefusesARunWhoseBranchMovedOnFromTheReviewedHead(t *testing.T) {
	t.Parallel()
	const pushedSince = "0123456789abcdef0123456789abcdef01234567"
	c, store, ctx := testClient(t)
	runner := &fakeCIRunner{}
	c.Config.CI = runner
	c.Config.GitHubPullRequests = prHead(pushedSince)
	task, _ := hungOnCI(t, c, store, ctx)

	err := c.ForceMerge(ctx, task.ID, "CI is hung and this is the fix", stuckHead)
	var ve *ui.ValidationError
	if !errors.As(err, &ve) || !strings.Contains(err.Error(), "moved") {
		t.Fatalf("err = %v, want a ValidationError saying the branch moved", err)
	}
	state, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state != model.StateRunning {
		t.Fatalf("state = %s, want a refused request to leave the run alone", state)
	}
	runs, err := store.Runs(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range runs {
		if r.FinishedAt != nil {
			t.Fatalf("run %s finished by a refused request", r.ID)
		}
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.ForceMergeAt != nil || obs.ForceMergeHeadSHA != "" || obs.CompletedAt != nil {
		t.Fatalf("observation = %+v, want a refused request to stamp nothing", obs)
	}
	if len(runner.cancelled) != 0 {
		t.Fatalf("cancelled = %v, want a refused request to leave CI alone", runner.cancelled)
	}
}

// TestForceMergeAfterStop: an operator who pressed Stop first left the
// task in awaiting_reply, which the merge queue never visits. Forcing
// the merge from there finishes it so the queue does.
func TestForceMergeAfterStop(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task, _ := hungOnCI(t, c, store, ctx)
	if err := c.Stop(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	state, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state != model.StateAwaitingReply {
		t.Fatalf("state after Stop = %s, want awaiting_reply", state)
	}

	if err := c.ForceMerge(ctx, task.ID, "CI is hung and this is the fix", ""); err != nil {
		t.Fatal(err)
	}
	if state, err = store.State(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	if state != model.StateCompleted {
		t.Fatalf("state = %s, want completed", state)
	}
	if !inOpenPullRequestLinks(t, store, ctx, task.ID) {
		t.Fatal("the stopped task is not on the merge queue's walk, so nothing would ever merge it")
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.PendingQuestionCommentID != nil {
		t.Fatal("the Stop's pending question is still set")
	}
	if obs.ForceMergeHeadSHA != stuckHead {
		t.Fatalf("head = %q, want %s", obs.ForceMergeHeadSHA, stuckHead)
	}
}

// TestForceMergeOnAnUnknownTaskIsNotFound matches every other
// single-task Client method's answer to an id nothing is behind.
func TestForceMergeOnAnUnknownTaskIsNotFound(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	err := c.ForceMerge(ctx, "nope", "because I said so", "")
	var nf *ui.NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("err = %v, want a NotFoundError", err)
	}
}

// prHead is a GitHub that answers one head for every pull request.
type prHead string

func (h prHead) GetPullRequest(owner, repo string, number int) (github.PullRequestDetail, error) {
	return github.PullRequestDetail{Number: number, HeadSHA: string(h)}, nil
}

// TestForceMergeIsForTheHeadTheOperatorReviewed is bwsalmon/grain#2399
// at the door: the override names one commit, an abbreviation of it is
// widened to the whole sha the merge queue compares against, and a head
// GitHub says the branch has already left is refused while the operator
// is still at the button -- not stamped for the queue to merge whatever
// is there instead.
func TestForceMergeIsForTheHeadTheOperatorReviewed(t *testing.T) {
	t.Parallel()
	const live = "0123456789abcdef0123456789abcdef01234567"

	t.Run("an abbreviation of the live head is stamped whole", func(t *testing.T) {
		t.Parallel()
		c, store, ctx := testClient(t)
		c.Config.GitHubPullRequests = prHead(live)
		task, _ := stuckTask(t, c, store, ctx)
		if err := c.ForceMerge(ctx, task.ID, "the release is broken", live[:12]); err != nil {
			t.Fatal(err)
		}
		obs, err := store.GetObservation(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if obs.ForceMergeHeadSHA != live {
			t.Fatalf("head = %q, want the full sha %s", obs.ForceMergeHeadSHA, live)
		}
	})

	t.Run("a head pasted in upper case is the same commit", func(t *testing.T) {
		t.Parallel()
		c, store, ctx := testClient(t)
		c.Config.GitHubPullRequests = prHead(live)
		task, _ := stuckTask(t, c, store, ctx)
		if err := c.ForceMerge(ctx, task.ID, "the release is broken", strings.ToUpper(live[:12])); err != nil {
			t.Fatal(err)
		}
		obs, err := store.GetObservation(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if obs.ForceMergeHeadSHA != live {
			t.Fatalf("head = %q, want the full sha %s", obs.ForceMergeHeadSHA, live)
		}
	})

	t.Run("a full head in upper case is not refused as abbreviated", func(t *testing.T) {
		t.Parallel()
		c, store, ctx := testClient(t)
		task, _ := stuckTask(t, c, store, ctx)
		if err := c.ForceMerge(ctx, task.ID, "the release is broken", strings.ToUpper(live)); err != nil {
			t.Fatalf("err = %v, want a full sha accepted whatever its case", err)
		}
		obs, err := store.GetObservation(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if obs.ForceMergeHeadSHA != live {
			t.Fatalf("head = %q, want git's lower-case spelling %s", obs.ForceMergeHeadSHA, live)
		}
	})

	t.Run("no head named takes GitHub's over grain CI's", func(t *testing.T) {
		t.Parallel()
		c, store, ctx := testClient(t)
		c.Config.GitHubPullRequests = prHead(live)
		task, _ := stuckTask(t, c, store, ctx)
		if err := c.ForceMerge(ctx, task.ID, "the release is broken", ""); err != nil {
			t.Fatal(err)
		}
		obs, err := store.GetObservation(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if obs.ForceMergeHeadSHA != live {
			t.Fatalf("head = %q, want the pull request's head now (%s)", obs.ForceMergeHeadSHA, live)
		}
	})

	t.Run("a head the branch has left is refused", func(t *testing.T) {
		t.Parallel()
		c, store, ctx := testClient(t)
		c.Config.GitHubPullRequests = prHead(live)
		task, _ := stuckTask(t, c, store, ctx)
		err := c.ForceMerge(ctx, task.ID, "the release is broken", stuckHead)
		var ve *ui.ValidationError
		if !errors.As(err, &ve) {
			t.Fatalf("err = %v, want a ValidationError for a head the branch has moved off", err)
		}
		if !strings.Contains(err.Error(), "moved") || !strings.Contains(err.Error(), live[:12]) {
			t.Fatalf("err = %q, want it to say the branch moved and where to", err)
		}
		obs, err := store.GetObservation(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if obs.ForceMergeAt != nil || obs.ForceMergeHeadSHA != "" {
			t.Fatalf("observation = %+v, want a refused request to stamp nothing", obs)
		}
	})

	t.Run("nothing says which commit it would be", func(t *testing.T) {
		t.Parallel()
		c, store, ctx := testClient(t)
		task, _ := stuckTask(t, c, store, ctx)
		if err := store.ObserveField(ctx, task.ID, baseTime, func(o *model.Observation) {
			o.CI = nil
		}); err != nil {
			t.Fatal(err)
		}
		err := c.ForceMerge(ctx, task.ID, "the release is broken", "")
		var ve *ui.ValidationError
		if !errors.As(err, &ve) {
			t.Fatalf("err = %v, want a ValidationError when no head is known", err)
		}
	})
}

// startRunOn puts the task into StateRunning the way a dispatch does,
// since nothing in this package hands one out.
func startRunOn(t *testing.T, store *model.Store, ctx context.Context, id string) {
	t.Helper()
	if err := store.ObserveField(ctx, id, baseTime, func(o *model.Observation) {
		o.CompletedAt = nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: "r1", TaskID: id, Sandbox: "s1", Attempt: 1, StartedAt: baseTime.Add(2 * time.Hour),
	}, model.Limits{}); err != nil {
		t.Fatalf("starting a run: %v", err)
	}
}
