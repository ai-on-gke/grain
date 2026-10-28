package orchestrator

import (
	"context"
	"errors"
	"sync"
)

// ErrTurnStopped is context.Cause's report when a turn is stopped / interrupted by a user.
var ErrTurnStopped = errors.New("orchestrator: turn stopped by user")

// ErrOperatorPaused is context.Cause's report when a run is ended because
// an operator paused its task (model.TaskPause, reconcilePauses). The run
// is recorded as model.InterruptedOutcome: no strike, conversation kept.
var ErrOperatorPaused = errors.New("orchestrator: task paused by the operator")

type taskCanceller struct {
	cancel context.CancelCauseFunc
}

// TaskCancellers is the set of live runs that can be cancelled / interrupted
// on request (for example, when a user clicks Stop in the UI), keyed by task ID.
//
// Keyed by task rather than by run: one task has at most one live run at a time.
//
// The zero value is not usable; NewTaskCancellers builds one. A nil
// *TaskCancellers is tolerated by all methods.
type TaskCancellers struct {
	mu   sync.Mutex
	live map[string]*taskCanceller
}

// NewTaskCancellers returns an empty registry, ready for runs to register
// themselves in as they are dispatched.
func NewTaskCancellers() *TaskCancellers {
	return &TaskCancellers{live: map[string]*taskCanceller{}}
}

// register makes taskID's live run cancellable and returns the function
// that unregisters it once framework.Run has returned.
func (c *TaskCancellers) register(taskID string, cancel context.CancelCauseFunc) func() {
	if c == nil || cancel == nil {
		return func() {}
	}
	entry := &taskCanceller{cancel: cancel}
	c.mu.Lock()
	c.live[taskID] = entry
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		// Compared, not deleted blindly: a redispatch of the same task
		// registers under the same key, and a slow unregister from the
		// previous run must not drop the new run's entry -- the same
		// care RunWaitCredits and SandboxRecreations take.
		if c.live[taskID] == entry {
			delete(c.live, taskID)
		}
		c.mu.Unlock()
	}
}

// Cancel cancels the live run for taskID with the given cause, returning true
// if a live run was registered and cancelled.
func (c *TaskCancellers) Cancel(taskID string, cause error) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	entry, ok := c.live[taskID]
	c.mu.Unlock()
	if ok && entry != nil && entry.cancel != nil {
		entry.cancel(cause)
		return true
	}
	return false
}

// Live reports whether taskID has a live run registered here.
func (c *TaskCancellers) Live(taskID string) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.live[taskID]
	return ok
}
