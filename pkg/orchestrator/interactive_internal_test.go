package orchestrator

import (
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
)

// The ordinary task is untouched: an unattended run's budget is a budget
// for working, and nothing here spends it waiting on a person.
func TestInteractiveDispatchLeavesAnOrdinaryTaskAlone(t *testing.T) {
	t.Parallel()
	cfg := interactiveDispatch(model.Task{ID: "task-1"}, Config{MaxRunRuntime: 90 * time.Minute})
	if cfg.MaxRunRuntime != 90*time.Minute {
		t.Errorf("MaxRunRuntime = %v, want the deployment's own", cfg.MaxRunRuntime)
	}
}

// A task somebody is sitting with gets the longer clock a conversation
// needs -- one turn of chat can stand still for the best part of an hour,
// which is most of an ordinary run's whole budget.
func TestInteractiveDispatchGivesASessionItsOwnClock(t *testing.T) {
	t.Parallel()
	cfg := interactiveDispatch(model.Task{ID: "task-1", Interactive: true}, Config{})
	if cfg.maxRunRuntime() != DefaultMaxInteractiveRuntime {
		t.Errorf("maxRunRuntime() = %v, want %v", cfg.maxRunRuntime(), DefaultMaxInteractiveRuntime)
	}

	// A deployment that set its own number is honoured.
	cfg = interactiveDispatch(model.Task{ID: "task-1", Interactive: true},
		Config{MaxInteractiveRuntime: 3 * time.Hour})
	if cfg.maxRunRuntime() != 3*time.Hour {
		t.Errorf("maxRunRuntime() = %v, want the deployment's own 3h", cfg.maxRunRuntime())
	}
}

// Only ever upwards, so a console session -- which is also interactive,
// and lives on a day -- is not cut down to a chat session's eight hours
// by being handed through here after consoleDispatch.
func TestInteractiveDispatchNeverShortensARun(t *testing.T) {
	t.Parallel()
	cfg := interactiveDispatch(model.Task{ID: "task-1", Interactive: true},
		Config{MaxRunRuntime: DefaultMaxConsoleRuntime})
	if cfg.MaxRunRuntime != DefaultMaxConsoleRuntime {
		t.Errorf("MaxRunRuntime = %v, want the longer clock it already had", cfg.MaxRunRuntime)
	}
}
