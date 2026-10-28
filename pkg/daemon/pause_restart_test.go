package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/agent"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
)

// The deployment of grain/task-1259: default_model is
// "gemini-x, claude-y", three Antigravity connections each on a key of
// its own serve gemini-x, and one Claude connection serves claude-y.
func restartRotationPool(t *testing.T) *AgentPool {
	t.Helper()
	var backends []model.AgentBackend
	for _, id := range []string{"agy-1", "agy-2", "agy-3"} {
		backends = append(backends, model.AgentBackend{ID: id, Framework: model.AgentFrameworkAntigravity,
			AuthMode: model.AntigravityAuthAmbient, CLIPath: "/bin/true", Models: []string{"gemini-x"}})
	}
	backends = append(backends, model.AgentBackend{ID: "claude", Framework: model.AgentFrameworkClaude,
		AuthMode: model.AntigravityAuthAmbient, CLIPath: "/bin/true", Models: []string{"claude-y"}})
	pool := providerDefaultPool(t, "gemini-x, claude-y", backends...)
	if err := pool.Store.UpdateConfig(context.Background(), func(c *model.Config) error {
		c.AgentFramework = model.AgentFrameworkAntigravity
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return pool
}

// restorePause is a daemon starting on pool's store: a fresh gate,
// restored before the pool is asked anything (runDaemon's order).
func restorePause(t *testing.T, pool *AgentPool, now time.Time) {
	t.Helper()
	pool.Pause = &orchestrator.Pause{}
	if _, err := pool.Pause.Restore(context.Background(), pool.Store, now); err != nil {
		t.Fatalf("Restore: %v", err)
	}
}

// Every connection serving the rotation's first model has met its limit
// and the daemon restarts inside the window: the restarted pool still
// passes over them, and the rotation moves the task to the next model
// rather than dispatching onto a spent key or waiting. Once the window
// resets, the first model is back.
func TestPausedConnectionsStaySkippedAcrossARestart(t *testing.T) {
	t.Parallel()
	pool := restartRotationPool(t)
	now := time.Now()
	reset := now.Add(13 * time.Hour)
	restorePause(t, pool, now)
	for _, id := range []string{"agy-1", "agy-2", "agy-3"} {
		pool.Pause.Begin(now, orchestrator.PauseScope{Connection: id, Model: "gemini-x", Team: model.DefaultTeamID},
			&agent.UsageLimitError{ResetAt: reset})
	}

	// The restart.
	restorePause(t, pool, now)
	for _, id := range []string{"agy-1", "agy-2", "agy-3"} {
		if !pool.Pause.ModelPaused(id, "gemini-x", model.DefaultTeamID, time.Now()) {
			t.Fatalf("connection %s is not paused after the restart", id)
		}
	}
	fw := buildChecked(t, pool, orchestrator.AgentChoice{}, "claude-y", model.AgentFrameworkClaude)
	if got := fw.FallbackReason(); !strings.Contains(got, "gemini-x is paused until ") {
		t.Errorf("FallbackReason = %q, want it naming the paused gemini-x", got)
	}

	// A daemon starting after the reset restores nothing, and the first
	// model dispatches again.
	restorePause(t, pool, reset.Add(time.Minute))
	if got := pool.Pause.Pauses(reset.Add(time.Minute)); len(got) != 0 {
		t.Fatalf("pauses restored after their reset: %+v", got)
	}
	rows, err := pool.Store.ListUsageLimitPauses(context.Background())
	if err != nil || len(rows) != 0 {
		t.Fatalf("stored pauses after the reset = %+v, %v; want none", rows, err)
	}
}

// Some of the first model's connections paused, one not: the rotation
// stays on the first model, on the connection still serving it.
func TestRotationStaysOnTheFirstModelWhileAConnectionServesIt(t *testing.T) {
	t.Parallel()
	pool := restartRotationPool(t)
	now := time.Now()
	restorePause(t, pool, now)
	for _, id := range []string{"agy-1", "agy-2"} {
		pool.Pause.Begin(now, orchestrator.PauseScope{Connection: id, Model: "gemini-x", Team: model.DefaultTeamID},
			&agent.UsageLimitError{ResetAt: now.Add(time.Hour)})
	}
	restorePause(t, pool, now)
	fw := buildChecked(t, pool, orchestrator.AgentChoice{}, "gemini-x", model.AgentFrameworkAntigravity)
	if fw.Id != "agy-3" {
		t.Errorf("built on %q, want agy-3, the one gemini-x connection not paused", fw.Id)
	}
}
