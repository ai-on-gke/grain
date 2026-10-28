package daemon

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/runhost"
)

// A durable task is placed on a framework this daemon can adopt across
// its own restart (docs/design/durable-tasks.md#DT-1): on a deployment
// whose default models are [gemini-x, claude-y] and whose claude runs in
// a run host, a durable run goes to claude-y first, an ordinary one keeps
// the list's order, and a pinned model or connection is left alone.
func TestDurableRunsPreferAnAdoptableFramework(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := crossFrameworkPool(t)
	live := DispatchConfig(ctx, pool.Store, pool.cfg)
	live.RunHost = &runhost.Host{}

	if got := live.adoptableFrameworks(ctx); !got[model.AgentFrameworkClaude] || got[model.AgentFrameworkAntigravity] {
		t.Fatalf("adoptableFrameworks = %v, want claude alone (its run host)", got)
	}

	pick := func(choice orchestrator.AgentChoice) (string, []string) {
		t.Helper()
		req, err := pool.resolve(ctx, live, choice)
		if err != nil {
			t.Fatalf("resolve(%+v): %v", choice, err)
		}
		pool.mu.Lock()
		defer pool.mu.Unlock()
		picked, candidates, _ := pool.selectCandidateLocked(live, req, time.Now())
		var ids []string
		for _, c := range candidates {
			ids = append(ids, c.ID)
		}
		return picked, ids
	}

	if m, ids := pick(orchestrator.AgentChoice{}); m != "gemini-x" || !slices.Equal(ids, []string{"agy"}) {
		t.Errorf("ordinary run = %s on %v, want gemini-x on agy (the list's order)", m, ids)
	}
	if m, ids := pick(orchestrator.AgentChoice{Durable: true, Priority: true}); m != "claude-y" || !slices.Equal(ids, []string{"claude"}) {
		t.Errorf("durable run = %s on %v, want claude-y on claude (adoptable under the run host)", m, ids)
	}
	// Weaker than a pin, and weaker than failover (AvoidModels).
	if m, ids := pick(orchestrator.AgentChoice{Durable: true, Model: "gemini-x"}); m != "gemini-x" || !slices.Equal(ids, []string{"agy"}) {
		t.Errorf("durable run pinned to gemini-x = %s on %v, want gemini-x on agy", m, ids)
	}
	if m, ids := pick(orchestrator.AgentChoice{Durable: true, AvoidModels: []string{"claude-y"}}); m != "gemini-x" || !slices.Equal(ids, []string{"agy"}) {
		t.Errorf("durable run avoiding claude-y = %s on %v, want gemini-x on agy", m, ids)
	}
	// Still falls back to what cannot be adopted rather than waiting.
	now := time.Now()
	pool.Pause.Begin(now, orchestrator.PauseScope{Connection: "claude", Model: "claude-y", Team: model.DefaultTeamID},
		&agent.UsageLimitError{ResetAt: now.Add(time.Hour)})
	if m, ids := pick(orchestrator.AgentChoice{Durable: true}); m != "gemini-x" || !slices.Equal(ids, []string{"agy"}) {
		t.Errorf("durable run with claude-y paused = %s on %v, want gemini-x on agy (a restart suspension, not a wait)", m, ids)
	}

	// With nothing adoptable the preference is no preference at all.
	live.RunHost = nil
	pool.Pause.Lift()
	if m, _ := pick(orchestrator.AgentChoice{Durable: true}); m != "gemini-x" {
		t.Errorf("durable run with nothing adoptable = %s, want gemini-x (the list's order)", m)
	}
}

// The rotation with no default model: a durable run's candidates are
// narrowed to the adoptable connections when there are any.
func TestDurableRunsNarrowTheRotationToAdoptableConnections(t *testing.T) {
	t.Parallel()
	req := poolRequest{}
	both := []model.AgentBackend{
		{ID: "agy", Framework: model.AgentFrameworkAntigravity},
		{ID: "claude", Framework: model.AgentFrameworkClaude},
	}
	if got := req.adoptableOnly(both); len(got) != 2 {
		t.Errorf("ordinary request kept %v, want both", got)
	}
	req.preferAdoptable(map[string]bool{model.AgentFrameworkClaude: true})
	if got := req.adoptableOnly(both); len(got) != 1 || got[0].ID != "claude" {
		t.Errorf("durable request kept %v, want claude alone", got)
	}
	if got := req.adoptableOnly(both[:1]); len(got) != 1 || got[0].ID != "agy" {
		t.Errorf("durable request with no adoptable candidate kept %v, want agy rather than nothing", got)
	}
}
