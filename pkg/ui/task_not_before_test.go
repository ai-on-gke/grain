package ui_test

// grain/task-1405: a task scheduled for later at the moment it is filed
// (CreateTaskRequest.DeferredUntil) -- the live half of propose_task's
// `not_before`, and the same thing for anybody filing over the API.

import (
	"net/http"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/ui"
)

func TestCreateTaskWithAWakeTimeIsFiledDeferredUntilThen(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)

	wake := time.Date(2099, 10, 14, 9, 0, 0, 0, time.UTC)
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "read the first week of the nightly suite", Approved: true, DeferredUntil: &wake,
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	stored, err := store.GetTask(ctx, task.ID)
	if err != nil || stored == nil {
		t.Fatalf("get: (%+v, %v)", stored, err)
	}
	if stored.DeferredAt == nil || stored.DeferredUntil == nil || !stored.DeferredUntil.Equal(wake) {
		t.Fatalf("deferred at %v until %v, want deferred until %v", stored.DeferredAt, stored.DeferredUntil, wake)
	}
	state, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state != model.StateDeferred {
		t.Fatalf("state = %q, want deferred", state)
	}
	// The approval survives the deferral, so the task wakes onto the
	// queue rather than back into proposed.
	if stored.ApprovedAt == nil {
		t.Fatal("approval was dropped by the deferral")
	}
	due, err := store.DueDeferrals(ctx, wake)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0] != task.ID {
		t.Fatalf("due at its wake time = %v, want [%s]", due, task.ID)
	}
}

func TestApprovingAndWithdrawingApprovalOnADeferredTaskPreservesItsWakeTime(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	srv := ui.NewServerWithClient(c)

	wake := time.Date(2099, 10, 14, 9, 0, 0, 0, time.UTC)
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "scheduled release", DeferredUntil: &wake,
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if task.State != model.StateDeferred || task.Approved {
		t.Fatalf("initial task: state = %q, approved = %v, want deferred and unapproved", task.State, task.Approved)
	}
	if task.DeferredUntil == nil || !task.DeferredUntil.Equal(wake) {
		t.Fatalf("initial deferredUntil = %v, want %v", task.DeferredUntil, wake)
	}

	rec := do(t, srv, http.MethodPost, "/api/tasks/"+task.ID+"/approve", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("approve status = %d, want 200: %s", rec.Code, rec.Body)
	}
	approved := decode[ui.Task](t, rec)
	if approved.State != model.StateDeferred || !approved.Approved {
		t.Fatalf("after approve: state = %q, approved = %v, want deferred and approved", approved.State, approved.Approved)
	}
	if approved.DeferredUntil == nil || !approved.DeferredUntil.Equal(wake) {
		t.Fatalf("after approve: deferredUntil = %v, want %v", approved.DeferredUntil, wake)
	}
	stored, err := store.GetTask(ctx, task.ID)
	if err != nil || stored == nil {
		t.Fatalf("get after approve: (%+v, %v)", stored, err)
	}
	if stored.DeferredAt == nil || stored.DeferredUntil == nil || !stored.DeferredUntil.Equal(wake) {
		t.Fatalf("stored after approve: deferred at %v until %v, want deferred until %v", stored.DeferredAt, stored.DeferredUntil, wake)
	}

	rec = do(t, srv, http.MethodPost, "/api/tasks/"+task.ID+"/withdraw-approval", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("withdraw-approval status = %d, want 200: %s", rec.Code, rec.Body)
	}
	withdrawn := decode[ui.Task](t, rec)
	if withdrawn.State != model.StateDeferred || withdrawn.Approved {
		t.Fatalf("after withdraw: state = %q, approved = %v, want deferred and unapproved", withdrawn.State, withdrawn.Approved)
	}
	if withdrawn.DeferredUntil == nil || !withdrawn.DeferredUntil.Equal(wake) {
		t.Fatalf("after withdraw: deferredUntil = %v, want %v", withdrawn.DeferredUntil, wake)
	}
	stored, err = store.GetTask(ctx, task.ID)
	if err != nil || stored == nil {
		t.Fatalf("get after withdraw: (%+v, %v)", stored, err)
	}
	if stored.DeferredAt == nil || stored.DeferredUntil == nil || !stored.DeferredUntil.Equal(wake) {
		t.Fatalf("stored after withdraw: deferred at %v until %v, want deferred until %v", stored.DeferredAt, stored.DeferredUntil, wake)
	}

	// Re-approving while still deferred makes the task wake directly onto
	// the queue when its wake time arrives.
	if err := c.Approve(ctx, task.ID); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	due, err := store.DueDeferrals(ctx, wake)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0] != task.ID {
		t.Fatalf("due at wake time = %v, want [%s]", due, task.ID)
	}
	if err := store.SetDeferred(ctx, task.ID, nil, nil, wake); err != nil {
		t.Fatalf("SetDeferred: %v", err)
	}
	woken, err := c.Task(ctx, task.ID)
	if err != nil {
		t.Fatalf("Task after wake: %v", err)
	}
	if woken.State != model.StateQueued || !woken.Approved {
		t.Fatalf("after wake: state = %q, approved = %v, want queued and approved", woken.State, woken.Approved)
	}
}

func TestCreateTaskWithAWakeTimeAlreadyPastIsNotDeferred(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)

	past := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "t", DeferredUntil: &past})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	state, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state != model.StateProposed {
		t.Fatalf("state = %q, want proposed", state)
	}
}
