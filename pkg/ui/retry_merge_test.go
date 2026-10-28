package ui_test

// The human's way back onto the merge queue: Client.RetryMerge, which
// the UI's "Retry merge" button and `grain retry-merge` both come
// through. The queue gives up exactly once per pull request and never
// revisits it on its own (orchestrator.blockMergeQueue), so this is the
// whole of the second chance a deployment has.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/ui"
)

// blockedTask is a task the merge queue has driven as far as it drives
// anything and then given up on: a pull request open and submitted, the
// run completed, and all three of the stamps the queue keeps its own
// memory in set (Observation.MergeQueueBlockedAt, MergeQueueRepairAt,
// MergeQueueRefreshedAt).
func blockedTask(t *testing.T, c *ui.Client, store *model.Store, ctx context.Context) (ui.Task, string) {
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
	repairedAt := baseTime.Add(2 * time.Hour)
	refreshedAt := baseTime.Add(3 * time.Hour)
	blockedAt := baseTime.Add(4 * time.Hour)
	if err := store.Observe(ctx, model.Observation{
		TaskID:                task.ID,
		CompletedAt:           &completedAt,
		MergeQueueRepairAt:    &repairedAt,
		MergeQueueRefreshedAt: &refreshedAt,
		MergeQueueBlockedAt:   &blockedAt,
		// Plus the row the queue left behind: the state it died in and,
		// from the round before that, grain's own verdict that the wait
		// had stopped getting anywhere
		// (Observation.MergeQueueStalledAt).
		MergeQueueState:     "merge_failed",
		MergeQueueReason:    "merge_failed/given_up",
		MergeQueueNote:      "the repair ran and did not take",
		MergeQueueStateAt:   &blockedAt,
		MergeQueueStalledAt: &refreshedAt,
	}); err != nil {
		t.Fatal(err)
	}
	return task, ref
}

// TestRetryMergeClearsWhatTheQueueRemembers is the whole point of the
// button: all three stamps go back to nil together. Clearing the block
// alone would hand the task its queue position back with nothing new to
// do in it -- the refresh and the repair are each spent for the life of
// the pull request -- so the next cycle would find it broken and give up
// on the same tick.
func TestRetryMergeClearsWhatTheQueueRemembers(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task, ref := blockedTask(t, c, store, ctx)

	if err := c.RetryMerge(ctx, task.ID); err != nil {
		t.Fatal(err)
	}

	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.MergeQueueBlockedAt != nil || obs.MergeQueueRepairAt != nil || obs.MergeQueueRefreshedAt != nil {
		t.Fatalf("blocked = %v, repair = %v, refreshed = %v, want all cleared by a retry",
			obs.MergeQueueBlockedAt, obs.MergeQueueRepairAt, obs.MergeQueueRefreshedAt)
	}
	// And the row goes with them, the flag included: the next cycle
	// writes what the queue is doing now, and a row that still read
	// "Merge failed -- stalled" would be flagging the round a person has
	// just asked for against the one it replaced.
	if obs.MergeQueueState != "" || obs.MergeQueueReason != "" || obs.MergeQueueStateAt != nil || obs.MergeQueueStalledAt != nil {
		t.Fatalf("state = %q (%q) at %v, stalled = %v, want the row cleared by a retry",
			obs.MergeQueueState, obs.MergeQueueReason, obs.MergeQueueStateAt, obs.MergeQueueStalledAt)
	}
	// The rest of the observation is untouched: a retry is about the
	// queue, not about the run that produced the branch.
	if obs.CompletedAt == nil {
		t.Fatal("CompletedAt cleared by a retry: the task's own run still finished")
	}

	detail, err := c.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.MergeQueueBlockedAt != nil {
		t.Fatalf("MergeQueueBlockedAt = %v on the wire, want nil: the chip goes with the block",
			detail.MergeQueueBlockedAt)
	}
	// The clearing is three fields going back to nil, which nothing in a
	// task's thread would otherwise show; the comment is its whole
	// record, and it names the pull request it is about.
	if len(detail.Comments) != 1 {
		t.Fatalf("comments = %d, want the retry to say so in the task's own thread", len(detail.Comments))
	}
	if !strings.Contains(detail.Comments[0].Body, ref) {
		t.Fatalf("comment = %q, want it to name %s", detail.Comments[0].Body, ref)
	}
	if detail.Comments[0].Author != "alice" {
		t.Fatalf("comment author = %q, want the person who asked for it", detail.Comments[0].Author)
	}
}

// TestRetryMergeOnATaskTheQueueIsStillDrivingIsAllowed covers the arm
// RetryMerge's doc comment describes as "the same offer made a moment
// early": nothing about a task that has not been given up on makes the
// refresh and the repair worth withholding, and refusing here would mean
// a human racing the queue got an error instead of an answer.
func TestRetryMergeOnATaskTheQueueIsStillDrivingIsAllowed(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)
	if err := store.UpdateTask(ctx, task.ID, func(tk *model.Task) error {
		tk.Links = append(tk.Links, model.Link{Kind: model.LinkFixes, Target: "acme/widgets#7"})
		tk.AutoMerge = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	completedAt := baseTime
	if err := store.Observe(ctx, model.Observation{TaskID: task.ID, CompletedAt: &completedAt}); err != nil {
		t.Fatal(err)
	}

	if err := c.RetryMerge(ctx, task.ID); err != nil {
		t.Fatalf("RetryMerge on a task still on the queue: %v", err)
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.MergeQueueBlockedAt != nil {
		t.Fatalf("MergeQueueBlockedAt = %v, want a retry never to block anything", obs.MergeQueueBlockedAt)
	}
}

// TestRetryMergeRefusesATaskWithNoPullRequest: there is no queue entry
// without one, so there is nothing to ask for another attempt at.
func TestRetryMergeRefusesATaskWithNoPullRequest(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	task := create(t, c, ctx)

	err := c.RetryMerge(ctx, task.ID)
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v, want a ValidationError for a task with no pull request", err)
	}
}

// TestRetryMergeRefusesAClosedTask: a closed task is out of
// Store.OpenPullRequestLinks, so no sync cycle would ever read the queue
// stamps this would clear. Saying so is better than clearing them and
// leaving a person waiting for a merge nothing will ever attempt.
func TestRetryMergeRefusesAClosedTask(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task, _ := blockedTask(t, c, store, ctx)
	if err := c.Close(ctx, task.ID, ui.CloseOptions{}); err != nil {
		t.Fatal(err)
	}

	err := c.RetryMerge(ctx, task.ID)
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v, want a ValidationError for a closed task", err)
	}
	if !strings.Contains(err.Error(), "reopen") {
		t.Fatalf("err = %q, want it to say what to do about it", err)
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.MergeQueueBlockedAt == nil {
		t.Fatal("MergeQueueBlockedAt cleared by a refused retry: a refusal writes nothing")
	}
}

// TestRetryMergeOnAnUnknownTaskIsNotFound matches every other
// single-task Client method's answer to an id nothing is behind.
func TestRetryMergeOnAnUnknownTaskIsNotFound(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	err := c.RetryMerge(ctx, "nope")
	var nf *ui.NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("err = %v, want a NotFoundError", err)
	}
}
