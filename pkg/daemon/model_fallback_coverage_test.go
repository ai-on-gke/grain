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
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// The fallback models Settings → Models edits are the tail of the
// deployment's default model list. A default model only disabled
// connections advertise is passed over for the next one an enabled
// connection serves, and the run names the model it really ran on.
func TestDefaultModelServedOnlyByADisabledConnectionFallsToTheNext(t *testing.T) {
	t.Parallel()
	off := model.AgentBackend{ID: "agy-off", Framework: model.AgentFrameworkAntigravity,
		AuthMode: model.AntigravityAuthAmbient, CLIPath: "/bin/true", Models: []string{"gemini-x"}, Disabled: true}
	on := model.AgentBackend{ID: "agy-on", Framework: model.AgentFrameworkAntigravity,
		AuthMode: model.AntigravityAuthAmbient, CLIPath: "/bin/true", Models: []string{"gemini-y"}}
	pool := providerDefaultPool(t, "gemini-x, gemini-y", off, on)
	pool.Pause = &orchestrator.Pause{}
	buildChecked(t, pool, orchestrator.AgentChoice{}, "gemini-y", model.AgentFrameworkAntigravity)
}

// A task that pins its model does not fall back when every connection
// serving that model is at its concurrency limit, even with a default
// fallback model free on another connection: it waits, and the wait says
// it is pinned. The same saturation moves an unpinned task down the list.
func TestPinnedModelWaitsAtTheConcurrencyLimitInsteadOfFallingBack(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	one := model.AgentBackend{ID: "agy-1", Framework: model.AgentFrameworkAntigravity,
		AuthMode: model.AntigravityAuthAmbient, CLIPath: "/bin/true", Models: []string{"gemini-x"}, Concurrency: 1}
	two := model.AgentBackend{ID: "agy-2", Framework: model.AgentFrameworkAntigravity,
		AuthMode: model.AntigravityAuthAmbient, CLIPath: "/bin/true", Models: []string{"gemini-y"}, Concurrency: 1}
	pool := providerDefaultPool(t, "gemini-x, gemini-y", one, two)
	pool.Pause = &orchestrator.Pause{}

	held, err := pool.Build(ctx, orchestrator.AgentChoice{Model: "gemini-x"})
	if err != nil {
		t.Fatalf("Build(pinned gemini-x): %v", err)
	}
	defer held.(*BackendFramework).Lease.Release()

	reason := pool.Availability(ctx, orchestrator.AgentChoice{Model: "gemini-x"})
	if reason == "" || !strings.Contains(reason, "pinned to gemini-x; it does not fall back") {
		t.Errorf("Availability(pinned gemini-x, its connection full) = %q, want a pinned wait", reason)
	}
	if _, err := pool.Build(ctx, orchestrator.AgentChoice{Model: "gemini-x"}); err == nil {
		t.Error("Build(pinned gemini-x) with its only connection full succeeded, want it refused rather than run elsewhere")
	}
	// An unpinned task on the default list falls to gemini-y.
	buildChecked(t, pool, orchestrator.AgentChoice{}, "gemini-y", model.AgentFrameworkAntigravity)
}
