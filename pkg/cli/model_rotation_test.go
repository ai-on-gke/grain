package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/secrets"
)

// rotationPool is a pool over connections, each with a credential of its
// own, driven by the Claude stub so building one runs nothing.
func rotationPool(t *testing.T, backends []model.AgentBackend) *daemon.AgentPool {
	t.Helper()
	ctx := context.Background()
	store := testStore(t)
	secretStore := testSecrets(t)
	for _, b := range backends {
		if err := secretStore.Set(secrets.AgentBackendCredentialSecret(b.ID),
			secrets.AgentCredentialKey, []byte("sk-ant-oat01-"+b.ID)); err != nil {
			t.Fatalf("saving %s credential: %v", b.ID, err)
		}
	}
	if err := store.PutConfig(ctx, model.Config{AgentBackends: backends}); err != nil {
		t.Fatalf("saving connections: %v", err)
	}
	return daemon.NewAgentPool(daemon.Config{ClaudePath: modelDispatchClaudeStub(t)}, store, secretStore)
}

func builtOn(t *testing.T, p *daemon.AgentPool, choice orchestrator.AgentChoice) *daemon.BackendFramework {
	t.Helper()
	f, err := p.Build(context.Background(), choice)
	if err != nil {
		t.Fatalf("building %+v: %v", choice, err)
	}
	bf, ok := f.(*daemon.BackendFramework)
	if !ok {
		t.Fatalf("building %+v gave %T, not a pooled connection", choice, f)
	}
	return bf
}

// A run asking for a model goes round the deployment's enabled
// connections that advertise that model, evenly, and never onto one that
// is disabled or does not serve it. Which team's editor saved a
// connection chooses nothing: the connections are the deployment's
// (docs/single-tenant-reset.md, stage 1), so one saved under another
// team takes its turn like the rest.
func TestRotationUsesOnlyEnabledConnectionsServingTheModel(t *testing.T) {
	p := rotationPool(t, []model.AgentBackend{
		{ID: "a", Framework: model.AgentFrameworkClaude, Models: []string{"claude-x", "claude-y"}},
		{ID: "off", Framework: model.AgentFrameworkClaude, Models: []string{"claude-x"}, Disabled: true},
		{ID: "other-model", Framework: model.AgentFrameworkClaude, Models: []string{"claude-y"}},
		{ID: "b", Framework: model.AgentFrameworkClaude, Models: []string{"claude-x"}},
		{ID: "saved-elsewhere", Framework: model.AgentFrameworkClaude, Models: []string{"claude-x"}, Team: "elsewhere"},
	})
	counts := map[string]int{}
	for i := 0; i < 6; i++ {
		bf := builtOn(t, p, orchestrator.AgentChoice{Model: "claude-x"})
		counts[bf.Id]++
		bf.Lease.Release()
	}
	if counts["a"] != 2 || counts["b"] != 2 || counts["saved-elsewhere"] != 2 || len(counts) != 3 {
		t.Fatalf("six runs of claude-x landed %v, want two each on a, b and saved-elsewhere only", counts)
	}
	if got := p.Availability(context.Background(), orchestrator.AgentChoice{Model: "claude-z"}); !strings.Contains(got, "no enabled connection") || !strings.Contains(got, "claude-z") {
		t.Fatalf("availability for an unserved model = %q, want it to say nothing serves claude-z", got)
	}
}

// A connection carries at most Concurrency runs at once. With every
// connection serving a model full, the task waits and says so; a run
// finishing frees its slot.
func TestRotationHonoursConnectionConcurrency(t *testing.T) {
	ctx := context.Background()
	p := rotationPool(t, []model.AgentBackend{
		{ID: "a", Framework: model.AgentFrameworkClaude, Models: []string{"claude-x"}, Concurrency: 1},
		{ID: "b", Framework: model.AgentFrameworkClaude, Models: []string{"claude-x"}, Concurrency: 2},
	})
	choice := orchestrator.AgentChoice{Model: "claude-x"}
	var held []*daemon.BackendFramework
	for i := 0; i < 3; i++ {
		if got := p.Availability(ctx, choice); got != "" {
			t.Fatalf("run %d: availability = %q with a slot free", i+1, got)
		}
		held = append(held, builtOn(t, p, choice))
	}
	counts := map[string]int{}
	for _, bf := range held {
		counts[bf.Id]++
	}
	if counts["a"] != 1 || counts["b"] != 2 {
		t.Fatalf("three runs landed %v, want one on a and two on b", counts)
	}
	got := p.Availability(ctx, choice)
	if !strings.Contains(got, "all connections serving claude-x are busy") {
		t.Fatalf("availability with every slot taken = %q, want it to say they are busy", got)
	}
	// The count of full connections must not read as their limit: two
	// connections are full, carrying three runs of the three they allow.
	if !strings.Contains(got, "2 connections at their concurrency limits (3 of 3 runs)") {
		t.Fatalf("availability with every slot taken = %q, want it to name the limit that is full", got)
	}
	if _, err := p.Build(ctx, choice); err == nil {
		t.Fatal("building a fourth run with every slot taken succeeded")
	}
	held[0].Lease.Release()
	if got := p.Availability(ctx, choice); got != "" {
		t.Fatalf("availability after a run finished = %q, want a free slot", got)
	}
	if bf := builtOn(t, p, choice); bf.Id != held[0].Id {
		t.Fatalf("the freed slot went to %q, want %q", bf.Id, held[0].Id)
	}
}

// A pin names one connection: it is used even when disabled or full, it
// does not move the rotation, and it must advertise the model asked for.
func TestPinnedConnectionBypassesTheRotation(t *testing.T) {
	ctx := context.Background()
	p := rotationPool(t, []model.AgentBackend{
		{ID: "a", Framework: model.AgentFrameworkClaude, Models: []string{"claude-x"}},
		{ID: "b", Framework: model.AgentFrameworkClaude, Models: []string{"claude-x"}},
		{ID: "kept", Framework: model.AgentFrameworkClaude, Models: []string{"claude-x", "claude-rare"}, Disabled: true, Concurrency: 1},
	})
	if bf := builtOn(t, p, orchestrator.AgentChoice{Model: "claude-x"}); bf.Id != "a" {
		t.Fatalf("first rotation run went to %q, want a", bf.Id)
	}
	for i := 0; i < 2; i++ {
		pin := orchestrator.AgentChoice{BackendID: "kept", Model: "claude-rare"}
		if got := p.Availability(ctx, pin); got != "" {
			t.Fatalf("availability of a pin = %q, want a pin never to wait", got)
		}
		if bf := builtOn(t, p, pin); bf.Id != "kept" {
			t.Fatalf("pinned run went to %q, want the disabled connection it named", bf.Id)
		}
	}
	if bf := builtOn(t, p, orchestrator.AgentChoice{Model: "claude-x"}); bf.Id != "b" {
		t.Fatalf("rotation after pins went to %q, want b: a pin must not advance it", bf.Id)
	}
	if _, err := p.Build(ctx, orchestrator.AgentChoice{BackendID: "a", Model: "claude-rare"}); err == nil ||
		!strings.Contains(err.Error(), "does not advertise") {
		t.Fatalf("pinning a connection to a model it does not serve = %v, want a refusal", err)
	}
}

// A task that chose no model runs on its team's default model.
func TestRotationRunsTheTeamDefaultModel(t *testing.T) {
	ctx := context.Background()
	p := rotationPool(t, []model.AgentBackend{
		{ID: "x-only", Framework: model.AgentFrameworkClaude, Models: []string{"claude-x"}},
		{ID: "y-only", Framework: model.AgentFrameworkClaude, Models: []string{"claude-y"}},
	})
	if err := p.Store.UpdateConfig(ctx, func(c *model.Config) error { c.DefaultModel = "claude-y"; return nil }); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		bf := builtOn(t, p, orchestrator.AgentChoice{})
		if bf.Id != "y-only" {
			t.Fatalf("run %d of the team default went to %q, want y-only", i+1, bf.Id)
		}
		bf.Lease.Release()
	}
}

func TestRotationTriesMultipleDefaultModelsInOrder(t *testing.T) {
	ctx := context.Background()
	p := rotationPool(t, []model.AgentBackend{
		{ID: "first", Framework: model.AgentFrameworkClaude, Models: []string{"claude-x"}, Concurrency: 1},
		{ID: "second", Framework: model.AgentFrameworkClaude, Models: []string{"claude-y"}, Concurrency: 1},
		{ID: "third", Framework: model.AgentFrameworkClaude, Models: []string{"claude-z"}, Concurrency: 1},
	})
	if err := p.Store.UpdateConfig(ctx, func(c *model.Config) error {
		c.DefaultModel = "claude-x, claude-y, claude-z"
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	bf1 := builtOn(t, p, orchestrator.AgentChoice{})
	if bf1.Id != "first" || bf1.Model != "claude-x" {
		t.Fatalf("first run went to %s (%s), want first (claude-x)", bf1.Id, bf1.Model)
	}
	bf2 := builtOn(t, p, orchestrator.AgentChoice{})
	if bf2.Id != "second" || bf2.Model != "claude-y" {
		t.Fatalf("second run while first is busy went to %s (%s), want second (claude-y)", bf2.Id, bf2.Model)
	}
	bf3 := builtOn(t, p, orchestrator.AgentChoice{})
	if bf3.Id != "third" || bf3.Model != "claude-z" {
		t.Fatalf("third run while first and second are busy went to %s (%s), want third (claude-z)", bf3.Id, bf3.Model)
	}
	if got := p.Availability(ctx, orchestrator.AgentChoice{}); !strings.Contains(got, "all connections serving claude-x, claude-y, claude-z are busy") {
		t.Fatalf("availability with all three busy = %q, want all connections serving claude-x, claude-y, claude-z are busy", got)
	}
	bf1.Lease.Release()
	bf4 := builtOn(t, p, orchestrator.AgentChoice{})
	if bf4.Id != "first" || bf4.Model != "claude-x" {
		t.Fatalf("run after releasing first went to %s (%s), want first (claude-x)", bf4.Id, bf4.Model)
	}
	bf2.Lease.Release()
	bf3.Lease.Release()
	bf4.Lease.Release()
}

// A task with no model of its own, in a team with no default model,
// rotates over every enabled connection of the team. While they are all
// full it waits saying so, not that the team has no connection
// (docs/test-runs/2026-10-04-connections-and-models.md, F2).
func TestRotationWithNoModelSaysTheTeamsConnectionsAreBusy(t *testing.T) {
	ctx := context.Background()
	p := rotationPool(t, []model.AgentBackend{
		{ID: "a", Framework: model.AgentFrameworkClaude, Models: []string{"claude-x"}, Concurrency: 1},
		{ID: "off", Framework: model.AgentFrameworkClaude, Models: []string{"claude-x"}, Disabled: true},
	})
	choice := orchestrator.AgentChoice{}
	bf := builtOn(t, p, choice)
	got := p.Availability(ctx, choice)
	if want := `all connections are busy (1 connection at its concurrency limit (1 of 1 runs))`; got != want {
		t.Fatalf("availability with the team's only enabled connection full = %q, want %q", got, want)
	}
	bf.Lease.Release()
	if got := p.Availability(ctx, choice); got != "" {
		t.Fatalf("availability after the run finished = %q, want a free slot", got)
	}
}

// A model that names a connection's id, rather than a model it serves,
// is that connection's pin, whichever team's editor saved it: the
// connections are the deployment's (docs/single-tenant-reset.md, stage
// 1). A model nothing serves still waits saying so rather than failing
// setup (docs/test-runs/2026-10-04-connections-and-models.md, F5).
func TestAModelNamingAConnectionIsItsPin(t *testing.T) {
	p := rotationPool(t, []model.AgentBackend{
		{ID: "mine", Framework: model.AgentFrameworkClaude, Models: []string{"claude-x"}},
		{ID: "saved-elsewhere", Framework: model.AgentFrameworkClaude, Models: []string{"claude-x"}, Team: "elsewhere"},
	})
	ctx := context.Background()
	for _, id := range []string{"mine", "saved-elsewhere"} {
		if got := p.Availability(ctx, orchestrator.AgentChoice{Model: id}); got != "" {
			t.Fatalf("availability for a model naming connection %s = %q, want a pin never to wait", id, got)
		}
		if bf := builtOn(t, p, orchestrator.AgentChoice{Model: id}); bf.Id != id {
			t.Fatalf("a model naming connection %s ran on %q, want it pinned there", id, bf.Id)
		}
	}
	got := p.Availability(ctx, orchestrator.AgentChoice{Model: "nobody"})
	if !strings.Contains(got, "no enabled connection") || !strings.Contains(got, "nobody") {
		t.Fatalf("availability for a model nothing serves = %q, want it to say nothing serves nobody", got)
	}
}
