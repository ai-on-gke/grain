package demo

import (
	"context"
	"fmt"
	"time"

	"github.com/bwsalmon/grain/pkg/agent"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/modelexec"
	"github.com/bwsalmon/grain/pkg/orchestrator"
	"github.com/bwsalmon/grain/pkg/ui"
)

// The pieces of `grain demo` that stand in for something outside grain:
// an agent provider's usage limit, the far end of a capability's
// credential, remote executors dialled in over the network. Each is
// wired into the real ui.Config field a daemon wires, so everything from
// the handler to the pane is the real thing, and ui/e2e asserts on the
// server's own answers instead of fulfilling grain's endpoints from
// fixtures (docs/test-runs/2026-10-02-e2e-own-api-stubs.md).

// unconfigureDemo turns the demo into a fresh install: no agent model
// configured and no git credential set, which is what raises the two
// setup banners. With no credential set, MigrateTeamGitCredentials has
// nothing to copy, so the demo's teams have no GitHub credential either.
// It runs after the stored config is written, so it undoes the models
// whichever scenario stored them.
func unconfigureDemo(ctx context.Context, store *model.Store, cfg ui.Config) (ui.Config, error) {
	cfg.Credentials = nil
	stored, err := store.GetConfig(ctx)
	if err != nil {
		return cfg, err
	}
	if stored == nil {
		return cfg, nil
	}
	stored.AgentBackends = nil
	stored.DefaultModel = ""
	return cfg, store.PutConfig(ctx, *stored)
}

// demoAgentPause is a real orchestrator.Pause, begun the way a run that
// met a provider's usage limit begins one -- on the demo's own Gemini
// connection, which belongs to the default team -- so the agent-pause
// banner and GET /api/pause read a paused connection. Lifting it from
// the UI really lifts it. The window is the longest the orchestrator
// allows from one refusal, so a demo left open for an afternoon is still
// paused.
func demoAgentPause(now time.Time) *orchestrator.Pause {
	pause := &orchestrator.Pause{}
	pause.Begin(now, orchestrator.PauseScope{
		Connection: demoPausedConnection,
		Team:       model.DefaultTeamID,
		Framework:  model.AgentFrameworkAntigravity,
	}, &agent.UsageLimitError{
		Framework: model.AgentFrameworkAntigravity,
		Message:   "Gemini API quota exhausted for project grain-sandbox-dev (demo)",
		ResetAt:   now.Add(6 * time.Hour),
	})
	return pause
}

// demoPausedConnection is the seeded connection demoAgentPause pauses.
const demoPausedConnection = "gemini-pro"

// demoCapabilityChecks makes Settings offer "Test credential" for every
// capability that has a live check, and answers each press with a
// refusal: there is no credential behind a demo and nothing to call. A
// check's answer comes from the service that issued the credential, so a
// test that wants a particular verdict stubs that answer in the browser.
type demoCapabilityChecks struct{}

func (demoCapabilityChecks) CheckCapability(_ context.Context, id string) (ui.CapabilityCheckResult, error) {
	return ui.CapabilityCheckResult{}, fmt.Errorf("grain demo holds no %s credential to test: nothing here is real", id)
}

// demoExecutors is a fixed snapshot of two remote executors, one on each
// of two teams, in place of modelexec.Server's registry of real
// connections. The connection times are fixed at startup, so the uptime
// the Models & connections tab shows keeps counting.
type demoExecutors struct {
	started time.Time
}

func (d demoExecutors) Connected() []modelexec.Status {
	return []modelexec.Status{
		{
			ID:   "exec-infra-01",
			Host: "runner-infra-worker-01",
			Addr: "10.240.1.15:41234",
			Binaries: map[string]string{
				"agy":    "/usr/local/bin/agy",
				"claude": "/usr/local/bin/claude",
			},
			ServerURL:   "https://grain.corp.internal",
			Since:       d.started.Add(-2 * time.Hour),
			Running:     2,
			Concurrency: 4,
		},
		{
			ID:   "exec-frontend-01",
			Host: "runner-frontend-worker-01",
			Addr: "10.240.2.20:41234",
			Binaries: map[string]string{
				"agy":   "/usr/local/bin/agy",
				"codex": "/usr/local/bin/codex",
			},
			ServerURL:   "https://grain.corp.internal",
			Since:       d.started.Add(-time.Hour),
			Running:     0,
			Concurrency: 2,
		},
	}
}
