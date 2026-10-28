// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/model"
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
