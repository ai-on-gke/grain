package ui_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/ui"
)

// standing is everything a task's dispatch standing is made of, read off
// the store: what a pause must leave alone and a resume must give back.
type standing struct {
	State         model.State
	Approved      bool
	OrderKey      float64
	DeferredUntil *time.Time
	Links         []model.Link
	Tags          []string
	RunNowAt      *time.Time
	PendingQ      *int64
}

func standingOf(t *testing.T, ctx context.Context, store *model.Store, id string) standing {
	t.Helper()
	task, err := store.GetTask(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.State(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	obs, err := store.GetObservation(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	s := standing{State: state, Approved: task.Approval != nil, OrderKey: task.OrderKey,
		DeferredUntil: task.DeferredUntil, Links: task.Links, Tags: task.Tags}
	if obs != nil {
		s.RunNowAt, s.PendingQ = obs.RunNowAt, obs.PendingQuestionCommentID
	}
	return s
}

// Pausing then resuming a task in each open state that has no live run
// leaves its state and standing exactly as they were -- Run now, a
// deferral's wake time, a pending question and approval included -- and
// while it is paused the task keeps its own state, is never ready, and
// says who paused it.
func TestPauseAndResumeRestoreEachStateExactly(t *testing.T) {
	t.Parallel()
	for name, setup := range map[string]func(t *testing.T, c *ui.Client, store *model.Store, ctx context.Context) string{
		"queued with run now": func(t *testing.T, c *ui.Client, store *model.Store, ctx context.Context) string {
			task := create(t, c, ctx)
			if err := c.RunNow(ctx, task.ID); err != nil {
				t.Fatal(err)
			}
			return task.ID
		},
		"proposed": func(t *testing.T, c *ui.Client, store *model.Store, ctx context.Context) string {
			task := create(t, c, ctx)
			if err := c.WithdrawApproval(ctx, task.ID); err != nil {
				t.Fatal(err)
			}
			return task.ID
		},
		"deferred until a date": func(t *testing.T, c *ui.Client, store *model.Store, ctx context.Context) string {
			task := create(t, c, ctx)
			until := baseTime.Add(48 * time.Hour)
			if err := c.Defer(ctx, task.ID, ui.DeferOptions{Until: &until}); err != nil {
				t.Fatal(err)
			}
			return task.ID
		},
		"awaiting_reply on a question": func(t *testing.T, c *ui.Client, store *model.Store, ctx context.Context) string {
			task := create(t, c, ctx)
			id, err := store.AddComment(ctx, model.Comment{TaskID: task.ID, Body: "which one?", CreatedAt: baseTime,
				Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAgent, ID: "run"}}})
			if err != nil {
				t.Fatal(err)
			}
			if err := store.ObserveField(ctx, task.ID, baseTime, func(o *model.Observation) { o.PendingQuestionCommentID = &id }); err != nil {
				t.Fatal(err)
			}
			return task.ID
		},
		"failed": func(t *testing.T, c *ui.Client, store *model.Store, ctx context.Context) string {
			task := create(t, c, ctx)
			for i := 1; i <= model.MaxConsecutiveFailures; i++ {
				runID := task.ID + "-" + string(rune('0'+i))
				at := baseTime.Add(time.Duration(i) * time.Minute)
				if err := store.StartRun(ctx, model.Run{ID: runID, TaskID: task.ID, Attempt: i, StartedAt: at}, model.Limits{}); err != nil {
					t.Fatal(err)
				}
				if err := store.FinishRun(ctx, runID, at.Add(time.Second), "failed", "broke"); err != nil {
					t.Fatal(err)
				}
			}
			return task.ID
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c, store, ctx := testClient(t)
			id := setup(t, c, store, ctx)
			before := standingOf(t, ctx, store, id)

			if err := c.Pause(ctx, id, ui.PauseOptions{}); err != nil {
				t.Fatalf("pausing a %s task: %v", before.State, err)
			}
			during := standingOf(t, ctx, store, id)
			if !reflect.DeepEqual(during, before) {
				t.Fatalf("pausing changed the task's standing:\n before %+v\n during %+v", before, during)
			}
			view, err := c.Task(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if view.Paused == nil || view.Paused.By != "alice" || view.Paused.FromState != before.State || view.Stall != "" {
				t.Fatalf("the paused task's view = paused %+v, stall %q; want paused by alice from %s, no stall",
					view.Paused, view.Stall, before.State)
			}
			if ready, _ := store.IsReady(ctx, id); ready {
				t.Fatal("a paused task is ready to dispatch")
			}
			if reqs, _ := store.RunNow(ctx); len(reqs) != 0 {
				t.Fatalf("a paused task's run-now request is offered to dispatch: %+v", reqs)
			}
			// Pausing again is a no-op.
			if err := c.Pause(ctx, id, ui.PauseOptions{}); err != nil {
				t.Fatal(err)
			}

			if err := c.Resume(ctx, id); err != nil {
				t.Fatal(err)
			}
			after := standingOf(t, ctx, store, id)
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("resuming did not restore the task's standing:\n before %+v\n after  %+v", before, after)
			}
			if p, _ := store.GetPause(ctx, id); p != nil {
				t.Fatalf("the task is still paused after resume: %+v", p)
			}
			// Resuming a task that is not paused is a no-op.
			if err := c.Resume(ctx, id); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPauseRefusesAClosedTask(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)
	if err := c.Close(ctx, task.ID, ui.CloseOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.Pause(ctx, task.ID, ui.PauseOptions{}); err == nil {
		t.Fatal("pausing a closed task was accepted")
	}
	if p, _ := store.GetPause(ctx, task.ID); p != nil {
		t.Fatalf("a refused pause left a pause behind: %+v", p)
	}
}

// A graceful pause of a running task tells the run, in the operator's own
// voice (which is what pkg/mcp's addenda deliver into a live session), to
// push and hand over within the grace period, and records when that
// period ends. A second pause with Immediate ends the wait.
func TestAGracefulPauseOfARunningTaskMessagesTheRun(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	c.Config.PauseGrace = 90 * time.Second
	task := create(t, c, ctx)
	if err := store.StartRun(ctx, model.Run{ID: task.ID + "-1", TaskID: task.ID, Attempt: 1, StartedAt: baseTime}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := c.Pause(ctx, task.ID, ui.PauseOptions{}); err != nil {
		t.Fatal(err)
	}
	p, err := store.GetPause(ctx, task.ID)
	if err != nil || p == nil {
		t.Fatalf("no pause recorded: %v", err)
	}
	if p.FromState != model.StateRunning || p.RunID != task.ID+"-1" || p.Immediate ||
		p.GraceUntil == nil || !p.GraceUntil.Equal(baseTime.Add(90*time.Second)) {
		t.Fatalf("pause = %+v, want from running, run %s-1, grace until %v", p, task.ID, baseTime.Add(90*time.Second))
	}
	comments, err := store.Comments(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	last := comments[len(comments)-1]
	if last.Author.Actor.Kind != model.PrincipalHuman || !strings.Contains(last.Body, "commit and push") ||
		!strings.Contains(last.Body, "write_handover") {
		t.Fatalf("the run was told %q by %+v, want the operator's pause message", last.Body, last.Author)
	}

	if err := c.Pause(ctx, task.ID, ui.PauseOptions{Immediate: true}); err != nil {
		t.Fatal(err)
	}
	if p, _ := store.GetPause(ctx, task.ID); p == nil || !p.Immediate {
		t.Fatalf("pausing again with Immediate left %+v", p)
	}
}
