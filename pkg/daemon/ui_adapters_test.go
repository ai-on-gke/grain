package daemon_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// fixedHealthSandboxes is an orchestrator.Sandboxes whose Health is
// whatever the test says, so the adapter's own copying is what is tested.
type fixedHealthSandboxes struct {
	orchestrator.Sandboxes
	health []orchestrator.SandboxHealth
}

func (f fixedHealthSandboxes) Health(context.Context) []orchestrator.SandboxHealth {
	return f.health
}

// The start time a backend records is what the System pane names the
// longest-running CI sandbox by, so it has to reach /api/sandboxes; a
// backend that does not know it gives a row with no startedAt at all,
// rather than one claiming the sandbox started in year 1.
func TestSandboxHealthAdapterCarriesStartedAt(t *testing.T) {
	started := time.Date(2026, 10, 26, 8, 0, 0, 0, time.UTC)
	got := daemon.SandboxHealthAdapter{Inner: fixedHealthSandboxes{health: []orchestrator.SandboxHealth{
		{Sandbox: "c1-0", Kind: "ci", Backend: "host", Ready: true, StartedAt: started},
		{Sandbox: "t1-1", Kind: "agent", Backend: "host", Ready: true},
	}}}.Health(context.Background())
	if len(got) != 2 {
		t.Fatalf("Health = %+v, want 2 rows", got)
	}
	if got[0].StartedAt == nil || !got[0].StartedAt.Equal(started) {
		t.Errorf("c1-0 StartedAt = %v, want %v", got[0].StartedAt, started)
	}
	if got[1].StartedAt != nil {
		t.Errorf("t1-1 StartedAt = %v, want nil for a backend that gave none", got[1].StartedAt)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"startedAt":"2026-10-26T08:00:00Z"`) {
		t.Errorf("JSON %s lacks c1-0's startedAt", raw)
	}
	if strings.Count(string(raw), `"startedAt"`) != 1 {
		t.Errorf("JSON %s carries startedAt on a row whose backend gave none", raw)
	}
}
