package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/agent/antigravity"
	"github.com/bwsalmon/grain/pkg/model"
)

// The connection a run is dispatched onto decides its model-turn limits:
// buildOn carries model.AgentBackend's ModelTurnTimeout and
// ModelRetryCutoff, and the name its timeout messages use, into the
// antigravity Framework it builds.
func TestBuildOnCarriesTheConnectionsTurnLimits(t *testing.T) {
	for name, tc := range map[string]struct {
		backend                 model.AgentBackend
		wantTimeout, wantCutoff time.Duration
		wantConnection          string
	}{
		"overridden": {
			backend: model.AgentBackend{ID: "corp-agy", Name: "Corp agy", Framework: model.AgentFrameworkAntigravity,
				AuthMode: model.AntigravityAuthAmbient, ModelTurnTimeout: "40m", ModelRetryCutoff: "2m"},
			wantTimeout: 40 * time.Minute, wantCutoff: 2 * time.Minute, wantConnection: "Corp agy",
		},
		"defaults": {
			backend: model.AgentBackend{ID: "plain-agy", Framework: model.AgentFrameworkAntigravity,
				AuthMode: model.AntigravityAuthAmbient},
			wantTimeout: antigravity.DefaultModelTurnTimeout, wantCutoff: antigravity.DefaultModelRetryCutoff, wantConnection: "plain-agy",
		},
	} {
		t.Run(name, func(t *testing.T) {
			built, err := (&AgentPool{}).buildOn(context.Background(), Config{AgyPath: "/bin/true"}, "", &tc.backend, "", nil, nil)
			if err != nil {
				t.Fatalf("buildOn: %v", err)
			}
			f, ok := built.(*antigravity.Framework)
			if !ok {
				t.Fatalf("buildOn built %T, want an antigravity framework", built)
			}
			timeout, cutoff, connection := f.TurnLimits()
			if timeout != tc.wantTimeout || cutoff != tc.wantCutoff || connection != tc.wantConnection {
				t.Errorf("TurnLimits = %s, %s, %q; want %s, %s, %q",
					timeout, cutoff, connection, tc.wantTimeout, tc.wantCutoff, tc.wantConnection)
			}
		})
	}
}
