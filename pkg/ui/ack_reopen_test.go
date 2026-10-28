package ui_test

// A person's comment on a task waiting to be acknowledged -- one whose run
// ended on words and no pull request (model.StateAwaitingAck) -- is taken
// as "continue", the way a reply to a question is: the task goes back to
// the queue and the next dispatch cycle starts a run of it. Nothing else
// that is said on a task does that, and no other finished state moves.

import (
	"context"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/dispatch"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/ui"
)

// completeForAck finishes task the way a run that answered in a comment
// does, and checks it reads awaiting_ack (or want, for a task the caller
// has given a pull request).
func completeForAck(t *testing.T, store *model.Store, ctx context.Context, id string, want model.State) {
	t.Helper()
	runID := dispatch.RunID(id, 1)
	if err := store.StartRun(ctx, model.Run{ID: runID, TaskID: id, Attempt: 1, StartedAt: baseTime}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	done := baseTime.Add(time.Minute)
	if err := store.FinishRun(ctx, runID, done, "success", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.ObserveField(ctx, id, done, func(o *model.Observation) {
		o.CompletedAt = &done
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.AdvanceDelivered(ctx, id, 0, done); err != nil {
		t.Fatal(err)
	}
	if state, err := store.State(ctx, id); err != nil || state != want {
		t.Fatalf("state = %q, err = %v; want %q before the comment", state, err, want)
	}
}

func dispatchedIDs(t *testing.T, store *model.Store, ctx context.Context) []string {
	t.Helper()
	ds, err := dispatch.Cycle(ctx, store, model.Limits{}, baseTime.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(ds))
	for _, d := range ds {
		ids = append(ids, d.TaskID)
	}
	return ids
}

func TestAHumanCommentReopensAnAwaitingAckTask(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	woken := 0
	c.Config.Wake = func() { woken++ }
	task := create(t, c, ctx)
	completeForAck(t, store, ctx, task.ID, model.StateAwaitingReply)

	if err := c.AddComment(ctx, task.ID, "thanks -- please also do X", nil); err != nil {
		t.Fatal(err)
	}

	if state, err := store.State(ctx, task.ID); err != nil || state != model.StateQueued {
		t.Fatalf("state = %q, err = %v; want queued again", state, err)
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.CompletedAt != nil || obs.RetryRequestedAt == nil {
		t.Fatalf("CompletedAt = %v, RetryRequestedAt = %v; want the completion cleared and a retry asked for",
			obs.CompletedAt, obs.RetryRequestedAt)
	}
	if obs.RunNowAt != nil {
		t.Fatalf("RunNowAt = %v, want none: a comment takes its ordinary place in the backlog", obs.RunNowAt)
	}
	if woken != 1 {
		t.Fatalf("Wake called %d times, want once", woken)
	}
	comments, err := store.Comments(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 {
		t.Fatalf("comments = %+v, want only the person's own", comments)
	}
	if obs.DeliveredCommentID != nil && *obs.DeliveredCommentID >= comments[0].ID {
		t.Fatalf("DeliveredCommentID = %d, want the new comment %d still undelivered",
			*obs.DeliveredCommentID, comments[0].ID)
	}
	if ids := dispatchedIDs(t, store, ctx); len(ids) != 1 || ids[0] != task.ID {
		t.Fatalf("dispatched %v, want a run of %s", ids, task.ID)
	}
}

// What a run says on a task, and what grain says on its behalf, never
// sends it anywhere -- nor does a person's broadcast to the whole team.
func TestOnlyAPersonsOwnCommentReopensAnAwaitingAckTask(t *testing.T) {
	t.Parallel()
	for name, say := range map[string]func(c *ui.Client, ctx context.Context, id string) error{
		"agent": func(c *ui.Client, ctx context.Context, id string) error {
			return c.AddComment(ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalAgent, ID: "run-1"}), id, "done", nil)
		},
		"automation": func(c *ui.Client, ctx context.Context, id string) error {
			return c.AddComment(ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}), id, "note", nil)
		},
		"broadcast": func(c *ui.Client, ctx context.Context, id string) error {
			_, err := c.BroadcastTeamMessage(ctx, ui.BroadcastTeamRequest{Message: "freeze", Author: "alice"})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c, store, ctx := testClient(t)
			task := create(t, c, ctx)
			completeForAck(t, store, ctx, task.ID, model.StateAwaitingReply)
			if err := say(c, ctx, task.ID); err != nil {
				t.Fatal(err)
			}
			if state, err := store.State(ctx, task.ID); err != nil || state != model.StateAwaitingReply {
				t.Fatalf("state = %q, err = %v; want awaiting_reply still", state, err)
			}
			if ids := dispatchedIDs(t, store, ctx); len(ids) != 0 {
				t.Fatalf("dispatched %v, want nothing", ids)
			}
		})
	}
}

// A closed task stays closed, and one with a pull request -- waiting on
// Submit or on the merge queue -- keeps to its own flow.
func TestACommentLeavesClosedAndPullRequestTasksWhereTheyAre(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		autoMerge bool
		closed    bool
		want      model.State
	}{
		"closed":          {closed: true, want: model.StateClosed},
		"awaiting submit": {want: model.StateAwaitingSubmit},
		"merge queue":     {autoMerge: true, want: model.StateCompleted},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c, store, ctx := testClient(t)
			task := create(t, c, ctx)
			if !tc.closed {
				if err := store.UpdateTask(ctx, task.ID, func(tk *model.Task) error {
					tk.Links = append(tk.Links, model.Link{Kind: model.LinkFixes, Target: "acme/widgets#42"})
					tk.AutoMerge = tc.autoMerge
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				completeForAck(t, store, ctx, task.ID, tc.want)
			} else {
				completeForAck(t, store, ctx, task.ID, model.StateAwaitingReply)
				closed := baseTime.Add(2 * time.Minute)
				if err := store.ObserveField(ctx, task.ID, closed, func(o *model.Observation) {
					o.ClosedAt = &closed
				}); err != nil {
					t.Fatal(err)
				}
			}
			if err := c.AddComment(ctx, task.ID, "one more thing", nil); err != nil {
				t.Fatal(err)
			}
			if state, err := store.State(ctx, task.ID); err != nil || state != tc.want {
				t.Fatalf("state = %q, err = %v; want %q still", state, err, tc.want)
			}
			if ids := dispatchedIDs(t, store, ctx); len(ids) != 0 {
				t.Fatalf("dispatched %v, want nothing", ids)
			}
		})
	}
}

// A task parked on a question is un-parked by a reply exactly as before,
// and is not given a retry stamp it never needed.
func TestAReplyStillUnparksAnAwaitingReplyTask(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)
	q, err := store.AddComment(ctx, model.Comment{
		TaskID:    task.ID,
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalAgent, ID: "run-1"}},
		Body:      "which one?",
		CreatedAt: baseTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ObserveField(ctx, task.ID, baseTime, func(o *model.Observation) {
		o.PendingQuestionCommentID = &q
	}); err != nil {
		t.Fatal(err)
	}
	if state, err := store.State(ctx, task.ID); err != nil || state != model.StateAwaitingReply {
		t.Fatalf("state = %q, err = %v; want awaiting_reply before the reply", state, err)
	}
	if err := c.AddComment(ctx, task.ID, "the first", nil); err != nil {
		t.Fatal(err)
	}
	if state, err := store.State(ctx, task.ID); err != nil || state != model.StateQueued {
		t.Fatalf("state = %q, err = %v; want queued", state, err)
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.PendingQuestionCommentID != nil || obs.CompletedAt != nil || obs.RetryRequestedAt == nil || obs.RunNowAt != nil {
		t.Fatalf("PendingQuestionCommentID = %v, CompletedAt = %v, RetryRequestedAt = %v, RunNowAt = %v; want question/completion cleared, retry stamped, and RunNowAt unset",
			obs.PendingQuestionCommentID, obs.CompletedAt, obs.RetryRequestedAt, obs.RunNowAt)
	}
}
