package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/agent/claude"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// crossFrameworkPool is a deployment on agy whose default model list is
// [gemini-x, claude-y], with one agy connection serving gemini-x and one
// claude connection serving claude-y.
func crossFrameworkPool(t *testing.T) *AgentPool {
	t.Helper()
	agy := model.AgentBackend{ID: "agy", Framework: model.AgentFrameworkAntigravity,
		AuthMode: model.AntigravityAuthAmbient, CLIPath: "/bin/true", Models: []string{"gemini-x"}}
	cl := model.AgentBackend{ID: "claude", Framework: model.AgentFrameworkClaude,
		AuthMode: model.AntigravityAuthAmbient, CLIPath: "/bin/true", Models: []string{"claude-y"}}
	pool := providerDefaultPool(t, "gemini-x, claude-y", agy, cl)
	if err := pool.Store.UpdateConfig(context.Background(), func(c *model.Config) error {
		c.AgentFramework = model.AgentFrameworkAntigravity
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	pool.Pause = &orchestrator.Pause{}
	return pool
}

func pauseGemini(pool *AgentPool, now time.Time) {
	pool.Pause.Begin(now, orchestrator.PauseScope{Connection: "agy", Model: "gemini-x", Team: model.DefaultTeamID},
		&agent.UsageLimitError{ResetAt: now.Add(time.Hour)})
}

// buildChecked builds choice and checks that Availability admitted it
// and that the run is wantModel on wantFramework -- the claude CLI for
// claude, agy's for antigravity -- so the two never disagree.
func buildChecked(t *testing.T, pool *AgentPool, choice orchestrator.AgentChoice, wantModel, wantFramework string) *BackendFramework {
	t.Helper()
	ctx := context.Background()
	if reason := pool.Availability(ctx, choice); reason != "" {
		t.Fatalf("Availability = %q, want it dispatchable", reason)
	}
	built, err := pool.Build(ctx, choice)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	fw := built.(*BackendFramework)
	t.Cleanup(fw.Lease.Release)
	if got := fw.CredentialModel(); got != wantModel {
		t.Errorf("model = %q, want %q", got, wantModel)
	}
	if got := fw.CredentialFramework(); got != wantFramework {
		t.Errorf("framework = %q, want %q", got, wantFramework)
	}
	switch wantFramework {
	case model.AgentFrameworkClaude:
		if _, ok := fw.Framework.(*claude.Framework); !ok {
			t.Errorf("built %T, want the claude CLI", fw.Framework)
		}
	case model.AgentFrameworkAntigravity:
		if _, ok := fw.Framework.(*antigravity.Framework); !ok {
			t.Errorf("built %T, want the agy CLI", fw.Framework)
		}
	}
	return fw
}

func TestDefaultModelFallbackCrossesFrameworks(t *testing.T) {
	t.Parallel()
	pool := crossFrameworkPool(t)
	now := time.Now()

	// Nothing paused: the first model, on the deployment's framework.
	if fw := buildChecked(t, pool, orchestrator.AgentChoice{}, "gemini-x", model.AgentFrameworkAntigravity); fw.FallbackReason() != "" {
		t.Errorf("FallbackReason on the first model = %q, want none", fw.FallbackReason())
	}

	// Every Gemini connection paused: over to claude-y, on the claude CLI.
	pauseGemini(pool, now)
	fw := buildChecked(t, pool, orchestrator.AgentChoice{}, "claude-y", model.AgentFrameworkClaude)
	if got := fw.FallbackReason(); !strings.Contains(got, "gemini-x is paused until ") {
		t.Errorf("FallbackReason = %q, want it naming the paused gemini-x", got)
	}
	// A task with priority dispatch (a session) falls over the same way.
	buildChecked(t, pool, orchestrator.AgentChoice{Priority: true}, "claude-y", model.AgentFrameworkClaude)

	// Lifted: back to the first model in list order.
	pool.Pause.Lift()
	buildChecked(t, pool, orchestrator.AgentChoice{}, "gemini-x", model.AgentFrameworkAntigravity)
}

// A task that named its framework is restricted to it, and a task that
// chose its model runs on that model: neither falls back.
func TestExplicitChoicesDoNotFallBackAcrossFrameworks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := crossFrameworkPool(t)
	pauseGemini(pool, time.Now())

	choice := orchestrator.AgentChoice{Framework: model.AgentFrameworkAntigravity}
	if reason := pool.Availability(ctx, choice); !strings.Contains(reason, "gemini-x is paused") {
		t.Errorf("Availability(framework agy) = %q, want it waiting on the paused gemini-x", reason)
	}
	if _, err := pool.Build(ctx, choice); err == nil {
		t.Error("Build(framework agy) succeeded, want it refused rather than run on claude")
	}

	pinned := orchestrator.AgentChoice{Model: "gemini-x"}
	reason := pool.Availability(ctx, pinned)
	if !strings.Contains(reason, "gemini-x is paused") || !strings.Contains(reason, "pinned to gemini-x; it does not fall back") {
		t.Errorf("Availability(model gemini-x) = %q, want it waiting, pinned", reason)
	}
	onConnection := orchestrator.AgentChoice{BackendID: "agy", Model: "gemini-x"}
	if reason := pool.Availability(ctx, onConnection); !strings.Contains(reason, `pinned to gemini-x on connection "agy"; it does not fall back`) {
		t.Errorf("Availability(connection agy) = %q, want it waiting, pinned", reason)
	}
}
