package orchestrator

import (
	"context"
	"errors"
	"testing"
)

func TestTaskCancellers(t *testing.T) {
	c := NewTaskCancellers()

	// Cancel on unregistered task returns false
	if c.Cancel("task-1", ErrTurnStopped) {
		t.Error("Cancel on unregistered task returned true, want false")
	}

	// Register a task
	ctx, cancel := context.WithCancelCause(context.Background())
	unregister := c.register("task-1", cancel)

	// Cancel registered task
	if !c.Cancel("task-1", ErrTurnStopped) {
		t.Error("Cancel on registered task returned false, want true")
	}
	if !errors.Is(context.Cause(ctx), ErrTurnStopped) {
		t.Errorf("context cause = %v, want ErrTurnStopped", context.Cause(ctx))
	}

	// Unregister task
	unregister()
	if c.Cancel("task-1", ErrTurnStopped) {
		t.Error("Cancel after unregister returned true, want false")
	}
}

func TestTaskCancellersRedispatchRace(t *testing.T) {
	c := NewTaskCancellers()

	// Run 1 registers
	ctx1, cancel1 := context.WithCancelCause(context.Background())
	unregister1 := c.register("task-1", cancel1)

	// Run 2 registers for same task before run 1 unregisters
	ctx2, cancel2 := context.WithCancelCause(context.Background())
	unregister2 := c.register("task-1", cancel2)

	// Run 1 unregisters (slow cleanup from previous run)
	unregister1()

	// Run 2 should still be cancellable
	if !c.Cancel("task-1", ErrTurnStopped) {
		t.Fatal("Cancel on task-1 returned false after previous run unregistered, want true")
	}
	if !errors.Is(context.Cause(ctx2), ErrTurnStopped) {
		t.Errorf("ctx2 cause = %v, want ErrTurnStopped", context.Cause(ctx2))
	}
	if context.Cause(ctx1) != nil {
		t.Errorf("ctx1 was cancelled unexpectedly: %v", context.Cause(ctx1))
	}

	// Now run 2 unregisters
	unregister2()
	if c.Cancel("task-1", ErrTurnStopped) {
		t.Error("Cancel after run 2 unregister returned true, want false")
	}
}

func TestNilTaskCancellers(t *testing.T) {
	var c *TaskCancellers
	unregister := c.register("task-1", func(cause error) {})
	unregister()
	if c.Cancel("task-1", ErrTurnStopped) {
		t.Error("Cancel on nil TaskCancellers returned true, want false")
	}
}
