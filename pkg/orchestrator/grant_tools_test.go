// Whether runOne actually adds a granted capability's own extra MCP
// tools (Config.GrantTools, bwsalmon/agents#540) to a run, and only for
// an Interactive task -- selfdebug_test.go/selfrepair_test.go already
// prove SourceTools/HostCommandTools work in isolation; this proves
// RunCycle wires them in (or doesn't) the way that field's own doc
// comment promises, the same "prove the wiring, not the backend again"
// split lifecycle_test.go already draws for Acquire and Release.
package orchestrator_test

import (
	"context"
	"sync"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// toolCapturingFramework records the names of every tool RunConfig.Tools
// carried on the one Run call it saw, then completes the same way
// completesWithAComment's own stubFramework does -- a comment, not a
// push, so ProcessResult has something to observe without a branch
// needing to exist.
type toolCapturingFramework struct {
	mu    sync.Mutex
	names []string
}

func (f *toolCapturingFramework) Run(_ context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	f.mu.Lock()
	for _, tool := range cfg.Tools {
		f.names = append(f.names, tool.Name)
	}
	f.mu.Unlock()
	return toolResult(agent.ToolCall{Name: "comment_on_issue", Arguments: map[string]any{"comment": "done"}}), nil
}

func (f *toolCapturingFramework) toolNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.names...)
}

func hasToolNamed(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

func grantToolsConfig() orchestrator.Config {
	return orchestrator.Config{
		GrantTools: map[string]func(*model.Store, string) []mcp.Tool{
			"configured-capability": func(*model.Store, string) []mcp.Tool {
				return []mcp.Tool{{
					Name:    "configured_tool",
					Handler: func(context.Context, map[string]any) mcp.Result { return mcp.Result{} },
				}}
			},
		},
	}
}

func TestRunCycleAddsGrantToolsForAnInteractiveTasksGrant(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	task.Interactive = true
	task.Grants = []model.Grant{{Capability: "configured-capability"}}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	framework := &toolCapturingFramework{}
	deps := orchestrator.Deps{
		Store: store, Client: client, Sandboxes: orchestrator.NewHostSandboxes(t.TempDir()),
		Framework:  orchestrator.StaticFramework(framework),
		Config:     grantToolsConfig(),
		MaxWorkers: 1,
	}

	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}

	if names := framework.toolNames(); !hasToolNamed(names, "configured_tool") {
		t.Fatalf("tool names = %v, want configured_tool among them", names)
	}
}

func TestRunCycleDoesNotAddGrantToolsForANonInteractiveTask(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	task.Grants = []model.Grant{{Capability: "configured-capability"}}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	framework := &toolCapturingFramework{}
	deps := orchestrator.Deps{
		Store: store, Client: client, Sandboxes: orchestrator.NewHostSandboxes(t.TempDir()),
		Framework:  orchestrator.StaticFramework(framework),
		Config:     grantToolsConfig(),
		MaxWorkers: 1,
	}

	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}

	if names := framework.toolNames(); hasToolNamed(names, "configured_tool") {
		t.Fatalf("tool names = %v, want configured_tool absent for a non-interactive task", names)
	}
}

func TestRunCycleAddsNoGrantToolsForAGrantWithNoEntry(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	task.Interactive = true
	task.Grants = []model.Grant{{Capability: "some-other-capability"}}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	framework := &toolCapturingFramework{}
	deps := orchestrator.Deps{
		Store: store, Client: client, Sandboxes: orchestrator.NewHostSandboxes(t.TempDir()),
		Framework:  orchestrator.StaticFramework(framework),
		Config:     grantToolsConfig(),
		MaxWorkers: 1,
	}

	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}

	if names := framework.toolNames(); hasToolNamed(names, "configured_tool") {
		t.Fatalf("tool names = %v, want configured_tool absent for an unrelated grant", names)
	}
}
