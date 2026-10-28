package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// grain/task-4320: a connection with no saved models advertises what its
// provider reports now, so a model the provider stops listing is one
// nothing dispatches to -- and a task that asked for it waits, saying
// why, whether it went through the rotation or pinned the connection.
func TestNoDispatchToAModelTheProviderNoLongerLists(t *testing.T) {
	t.Parallel()
	discovered := &model.DiscoveredModels{Models: []string{"claude-opus-5-5"}, RefreshedAt: time.Now()}
	pool := NewAgentPool(Config{AgentBackends: []model.AgentBackend{
		{ID: "claude-live", Framework: model.AgentFrameworkClaude, Discovered: discovered},
	}}, nil, nil)
	ctx := context.Background()

	if reason := pool.Availability(ctx, orchestrator.AgentChoice{Model: "claude-opus-5-5"}); reason != "" {
		t.Fatalf("a model the provider lists waits: %q", reason)
	}
	reason := pool.Availability(ctx, orchestrator.AgentChoice{Model: "claude-opus-4"})
	if !strings.Contains(reason, "serves model claude-opus-4") || !strings.Contains(reason, "no longer lists it") {
		t.Errorf("rotation wait reason = %q, want it to say the provider no longer lists the model", reason)
	}
	if _, err := pool.Build(ctx, orchestrator.AgentChoice{Model: "claude-opus-4"}); err == nil {
		t.Error("Build dispatched to a model the provider no longer lists")
	}

	reason = pool.Availability(ctx, orchestrator.AgentChoice{BackendID: "claude-live", Model: "claude-opus-4"})
	if want := `connection "claude-live"'s provider no longer lists model claude-opus-4`; reason != want {
		t.Errorf("pinned wait reason = %q, want %q", reason, want)
	}
}
