package orchestrator

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	consolecap "github.com/ai-on-gke/grain/pkg/capability/console"
	"github.com/ai-on-gke/grain/pkg/console"
	"github.com/ai-on-gke/grain/pkg/model"
)

// headlessFramework is an agent.Framework and nothing more -- the shape
// of a CLI that cannot be driven by hand.
type headlessFramework struct{}

func (headlessFramework) Run(context.Context, agent.RunConfig) (*agent.Result, error) {
	return &agent.Result{}, nil
}

// terminalFramework is one that can.
type terminalFramework struct{ headlessFramework }

func (terminalFramework) TerminalCommand(context.Context, agent.RunConfig) (*agent.TerminalCommand, error) {
	return &agent.TerminalCommand{Path: "cat"}, nil
}

func consoleTask(grants ...model.Grant) model.Task {
	return model.Task{ID: "task-1", Title: "pair on the flake", Grants: grants}
}

// The ordinary task: nothing about its dispatch changes, and no session
// manager is consulted.
func TestConsoleDispatchLeavesAnOrdinaryTaskAlone(t *testing.T) {
	t.Parallel()
	framework := terminalFramework{}
	got, cfg, err := consoleDispatch(framework, consoleTask(), "run-1", Config{})
	if err != nil {
		t.Fatalf("consoleDispatch: %v", err)
	}
	if _, wrapped := got.(*console.Framework); wrapped {
		t.Fatal("a task without the console grant was dispatched as a console run")
	}
	if cfg.MaxRunRuntime != 0 {
		t.Fatalf("MaxRunRuntime = %v, want the deployment's own", cfg.MaxRunRuntime)
	}
}

// A task holding the grant is driven as a session, and gets the longer
// clock a session lives on rather than an unattended run's.
func TestConsoleDispatchWrapsAGrantedTask(t *testing.T) {
	t.Parallel()
	cfg := Config{Consoles: console.NewManager()}
	got, cfg, err := consoleDispatch(terminalFramework{},
		consoleTask(model.Grant{Capability: consolecap.CapabilityName}), "run-1", cfg)
	if err != nil {
		t.Fatalf("consoleDispatch: %v", err)
	}
	if _, wrapped := got.(*console.Framework); !wrapped {
		t.Fatalf("framework = %T, want it wrapped as a console run", got)
	}
	if cfg.MaxRunRuntime != DefaultMaxConsoleRuntime {
		t.Fatalf("MaxRunRuntime = %v, want a console session's own %v", cfg.MaxRunRuntime, DefaultMaxConsoleRuntime)
	}
}

// A deployment can be told how long a forgotten session may last, and is
// not given an unattended run's two hours by accident.
func TestConsoleDispatchHonoursTheConfiguredConsoleRuntime(t *testing.T) {
	t.Parallel()
	cfg := Config{Consoles: console.NewManager(), MaxConsoleRuntime: 3 * time.Hour, MaxRunRuntime: time.Hour}
	_, cfg, err := consoleDispatch(terminalFramework{},
		consoleTask(model.Grant{Capability: consolecap.CapabilityName}), "run-1", cfg)
	if err != nil {
		t.Fatalf("consoleDispatch: %v", err)
	}
	if cfg.MaxRunRuntime != 3*time.Hour {
		t.Fatalf("MaxRunRuntime = %v, want the configured 3h", cfg.MaxRunRuntime)
	}
}

// Neither failure falls back to running the task unattended: an agent
// working alone through a task somebody meant to sit with is not a
// smaller version of what was asked for.
func TestConsoleDispatchRefusesWhatItCannotServe(t *testing.T) {
	t.Parallel()
	granted := consoleTask(model.Grant{Capability: consolecap.CapabilityName})

	if _, _, err := consoleDispatch(terminalFramework{}, granted, "run-1", Config{}); err == nil {
		t.Fatal("a deployment with no session manager dispatched a console task anyway")
	} else if !strings.Contains(err.Error(), "does not serve") {
		t.Fatalf("error = %v, want it to say this deployment serves no consoles", err)
	}

	cfg := Config{Consoles: console.NewManager(), AgentFramework: "codex"}
	if _, _, err := consoleDispatch(headlessFramework{}, granted, "run-1", cfg); err == nil {
		t.Fatal("a framework that cannot be driven by hand dispatched a console task anyway")
	} else if !strings.Contains(err.Error(), "codex") {
		t.Fatalf("error = %v, want it to name the framework that cannot serve a console", err)
	}
}
