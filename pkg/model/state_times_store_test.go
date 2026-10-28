package model_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
)

// TestStateTimesInSQLAgreeWithStateTimesInGo is the StateOf/task_state
// bargain one field-gathering down: a whole list is answered in SQL
// (Store.StateTimes, for a screen that draws hundreds of rows) and one
// task in Go (model.StateTimesFrom, off what a task pane has already
// read), and a reader of a row and a reader of the pane it opens must
// not disagree about when the wait on it began.
func TestStateTimesInSQLAgreeWithStateTimesInGo(t *testing.T) {
	store, ctx := open(t)
	at := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)

	// One task of each shape the gathering has an answer for: never run,
	// run and queued again, running now, completed, put back in the queue
	// by hand after its runs had stopped, and put aside and picked back
	// up again.
	mk := func(id string, autoMerge bool) model.Task {
		tk := model.Task{
			ID: id, Intent: model.IntentImplement, Title: "t",
			Origin:    model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
			Binding:   model.BindingDirective,
			Approval:  &model.Attribution{Actor: human},
			AutoMerge: autoMerge,
		}
		if autoMerge {
			tk.Links = []model.Link{{Kind: model.LinkFixes, Target: "acme/widgets#1"}}
		}
		return tk
	}
	ids := []string{"never", "again", "running", "done", "retried", "undeferred"}
	for _, id := range ids {
		if err := store.PutTask(ctx, mk(id, id == "done")); err != nil {
			t.Fatalf("filing %s: %v", id, err)
		}
		if err := store.Approve(ctx, id, model.Attribution{Actor: human}, at); err != nil {
			t.Fatalf("approving %s: %v", id, err)
		}
	}
	run := func(id string, n int, started time.Time, finished *time.Time) {
		t.Helper()
		r := model.Run{ID: id + "-" + string(rune('0'+n)), TaskID: id, Sandbox: "s", Attempt: n, StartedAt: started}
		if err := store.StartRun(ctx, r, model.Limits{}); err != nil {
			t.Fatalf("starting %s's run %d: %v", id, n, err)
		}
		if finished != nil {
			if err := store.FinishRun(ctx, r.ID, *finished, "succeeded", ""); err != nil {
				t.Fatalf("finishing %s's run %d: %v", id, n, err)
			}
		}
	}
	end1, end2 := at.Add(2*time.Hour), at.Add(4*time.Hour)
	run("again", 1, at.Add(time.Hour), &end1)
	run("again", 2, at.Add(3*time.Hour), &end2)
	run("retried", 1, at.Add(time.Hour), &end1)
	run("done", 1, at.Add(time.Hour), &end1)
	run("running", 1, at.Add(5*time.Hour), nil)
	if err := store.ObserveField(ctx, "done", at, func(o *model.Observation) {
		completed := at.Add(2 * time.Hour)
		o.CompletedAt = &completed
	}); err != nil {
		t.Fatalf("recording the completion: %v", err)
	}
	if err := store.ObserveField(ctx, "retried", at, func(o *model.Observation) {
		retried := at.Add(6 * time.Hour)
		o.RetryRequestedAt = &retried
	}); err != nil {
		t.Fatalf("recording the retry: %v", err)
	}
	// Put aside and picked back up, which is the third way a task
	// re-enters the queue and the one that used to leave no trace at all
	// (model.Task.UndeferredAt).
	deferredAt := at.Add(time.Hour)
	pickedUp := at.Add(7 * time.Hour)
	if err := store.SetDeferred(ctx, "undeferred", &deferredAt, nil, deferredAt); err != nil {
		t.Fatalf("deferring: %v", err)
	}
	if err := store.SetDeferred(ctx, "undeferred", nil, nil, pickedUp); err != nil {
		t.Fatalf("picking it back up: %v", err)
	}

	inSQL, err := store.StateTimes(ctx)
	if err != nil {
		t.Fatalf("StateTimes: %v", err)
	}
	// Every task is present, including the one with nothing to say: a
	// caller asking about the state a task is in right now should not
	// have to tell a missing key from an empty value.
	if len(inSQL) != len(ids) {
		t.Errorf("StateTimes covered %d tasks, want all %d", len(inSQL), len(ids))
	}
	for _, id := range ids {
		tk, err := store.GetTask(ctx, id)
		if err != nil || tk == nil {
			t.Fatalf("reading %s: %v", id, err)
		}
		obs, err := store.GetObservation(ctx, id)
		if err != nil {
			t.Fatalf("reading %s's observation: %v", id, err)
		}
		runs, err := store.Runs(ctx, id)
		if err != nil {
			t.Fatalf("reading %s's runs: %v", id, err)
		}
		inGo := model.StateTimesFrom(*tk, obs, runs)
		if !reflect.DeepEqual(inSQL[id], inGo) {
			t.Errorf("%s: StateTimes = %+v, StateTimesFrom = %+v", id, inSQL[id], inGo)
		}
		// And the pair actually answers the question they are gathered
		// for, rather than agreeing on nothing.
		state, err := store.State(ctx, id)
		if err != nil {
			t.Fatalf("reading %s's state: %v", id, err)
		}
		if id != "never" && inGo.Since(state) == nil {
			t.Errorf("%s is %s and has no since", id, state)
		}
		// And for the task that came back, both readers measure the wait
		// from the return rather than from the approval it was deferred
		// with -- agreeing on the approval would be the failure this
		// column was added to stop, agreed on twice.
		if id == "undeferred" {
			if since := inGo.Since(state); since == nil || !since.Equal(pickedUp) {
				t.Errorf("the undeferred task is queued since %v, want the undeferral %v", since, pickedUp)
			}
		}
	}
}
