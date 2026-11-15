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

// The pinned wait above is what a queued task's structured reason says
// too, not "a worker slot" with Run now: dispatch records the pool's
// sentence as the task's model wait (orchestrator's grantAdmitter), and
// TaskReason names the model and its connections' limit off it, in the
// words the task's activity line already uses (2026-11-14 §4, item 3).
func TestPinnedModelWaitIsTheQueuedTasksReason(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := model.AgentBackend{ID: "conn-b", Framework: model.AgentFrameworkAntigravity,
		AuthMode: model.AntigravityAuthAmbient, CLIPath: "/bin/true", Models: []string{"walk-b"}, Concurrency: 1}
	c := model.AgentBackend{ID: "conn-c", Framework: model.AgentFrameworkAntigravity,
		AuthMode: model.AntigravityAuthAmbient, CLIPath: "/bin/true", Models: []string{"walk-c"}, Concurrency: 1}
	pool := providerDefaultPool(t, "walk-b, walk-c", b, c)
	pool.Pause = &orchestrator.Pause{}

	held, err := pool.Build(ctx, orchestrator.AgentChoice{Model: "walk-b"})
	if err != nil {
		t.Fatalf("Build(pinned walk-b): %v", err)
	}
	defer held.(*BackendFramework).Lease.Release()

	why := pool.Availability(ctx, orchestrator.AgentChoice{Model: "walk-b"})
	if why == "" {
		t.Fatal("Availability(pinned walk-b, its connection full) = \"\", want a wait")
	}
	wait := &model.GrantWait{TaskID: "2", Capability: model.ModelWaitCapability, Resource: why}
	got := orchestrator.TaskReason(model.Task{ID: "2", Model: "walk-b"}, model.StateQueued, nil, nil, nil, wait,
		model.ReplyFacts{}, "", "")
	if got.Reason != "queued/waiting_for_model" {
		t.Errorf("TaskReason(queued, pinned walk-b at its limit).Reason = %q, want queued/waiting_for_model", got.Reason)
	}
	for _, want := range []string{"all connections serving walk-b are busy", "1 connection at its concurrency limit (1 of 1 runs)",
		"pinned to walk-b; it does not fall back"} {
		if !strings.Contains(got.Resource, want) {
			t.Errorf("Resource = %q, want it to say %q", got.Resource, want)
		}
	}
	if strings.Contains(got.Resource, "max_workers") || got.Control == "Run now" {
		t.Errorf("TaskReason = %+v, blames the worker pool or offers Run now, which cannot free walk-b's connection", got)
	}
}
