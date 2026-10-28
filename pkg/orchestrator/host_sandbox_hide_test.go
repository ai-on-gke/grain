package orchestrator_test

import (
	"context"
	"os/exec"
	"slices"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// The paths a HostSandboxes is built WithHidden reach the Framework as
// RunConfig.SandboxHide, which is what puts them on the agent's own
// mcpserver's argv (agent.SandboxEnvArgs): without that, the agent's
// run_command would run unconfined however the daemon's own did
// (grain/task-4366).
func TestHostSandboxesWithHiddenReachTheFramework(t *testing.T) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		// The dispatch's own checkout runs under it too.
		t.Skipf("bwrap not installed: %v", err)
	}
	t.Parallel()
	store, ctx := openStore(t)
	dispatchTask(t, ctx, store, "t1")
	hide := []string{t.TempDir()}

	var got []string
	fw := agentFunc(func(_ context.Context, cfg agent.RunConfig) (*agent.Result, error) {
		got = cfg.SandboxHide
		return &agent.Result{}, nil
	})
	_, client := newSim(t, "acme", "widgets", "main")
	deps := orchestrator.Deps{
		Store: store, Client: client, Sandboxes: orchestrator.NewHostSandboxes(t.TempDir()).WithHidden(hide),
		Framework: orchestrator.StaticFramework(fw),
		Config: orchestrator.Config{
			AgentFramework: model.AgentFrameworkClaude,
			Now:            func() time.Time { return baseTime },
		},
	}
	_ = orchestrator.RunCycle(ctx, deps, baseTime)
	if !slices.Equal(got, hide) {
		t.Fatalf("RunConfig.SandboxHide = %q, want %q", got, hide)
	}
}
