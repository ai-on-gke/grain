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
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// A usage limit pauses one model on a connection: the pool keeps
// dispatching the connection's other models, a task on the paused model
// waits saying so, and the run it builds names the model it is on.
func TestPoolPassesOverOnlyThePausedModel(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	agy := model.AgentBackend{ID: "agy", Framework: model.AgentFrameworkAntigravity,
		AuthMode: model.AntigravityAuthAmbient, Discovered: discovered("gemini-3.8-flash", "gemini-3.1-pro")}
	pool := providerDefaultPool(t, "", agy)
	pool.Pause = &orchestrator.Pause{}
	now := time.Now()

	pool.Pause.Begin(now, orchestrator.PauseScope{Connection: "agy", Model: "gemini-3.1-pro", Team: model.DefaultTeamID},
		&agent.UsageLimitError{ResetAt: now.Add(time.Hour)})

	if reason := pool.Availability(ctx, orchestrator.AgentChoice{Model: "gemini-3.8-flash"}); reason != "" {
		t.Errorf("Availability(flash) = %q, want the connection's other model dispatchable", reason)
	}
	reason := pool.Availability(ctx, orchestrator.AgentChoice{Model: "gemini-3.1-pro"})
	if !strings.Contains(reason, "model gemini-3.1-pro is paused after a usage limit") {
		t.Errorf("Availability(pro) = %q, want it waiting on the paused model", reason)
	}
	// A pin on the connection waits on its model's pause just the same.
	reason = pool.Availability(ctx, orchestrator.AgentChoice{BackendID: "agy", Model: "gemini-3.1-pro"})
	if !strings.Contains(reason, "model gemini-3.1-pro on connection \"agy\" is paused") {
		t.Errorf("Availability(pinned pro) = %q, want it waiting on the paused model", reason)
	}
	if reason := pool.Availability(ctx, orchestrator.AgentChoice{BackendID: "agy", Model: "gemini-3.8-flash"}); reason != "" {
		t.Errorf("Availability(pinned flash) = %q, want it dispatchable", reason)
	}

	built, err := pool.Build(ctx, orchestrator.AgentChoice{Model: "gemini-3.8-flash"})
	if err != nil {
		t.Fatalf("Build(flash): %v", err)
	}
	fw := built.(*BackendFramework)
	defer fw.Lease.Release()
	if got := fw.CredentialModel(); got != "gemini-3.8-flash" {
		t.Errorf("CredentialModel = %q, want the model the run passes the CLI", got)
	}
	// A run that chose no model runs the connection's own -- agy's
	// "auto" -- and is paused under that label.
	auto, err := pool.Build(ctx, orchestrator.AgentChoice{})
	if err != nil {
		t.Fatalf("Build(no model): %v", err)
	}
	defer auto.(*BackendFramework).Lease.Release()
	if got := auto.(*BackendFramework).CredentialModel(); got != model.ProviderModelAuto {
		t.Errorf("CredentialModel with no model chosen = %q, want %q", got, model.ProviderModelAuto)
	}

	// The whole connection paused holds every model.
	pool.Pause.Begin(now, orchestrator.PauseScope{Connection: "agy", Team: model.DefaultTeamID},
		&agent.UsageLimitError{ResetAt: now.Add(time.Hour)})
	if reason := pool.Availability(ctx, orchestrator.AgentChoice{Model: "gemini-3.8-flash"}); !strings.Contains(reason, "cooling off after a usage limit") {
		t.Errorf("Availability(flash) under a connection-wide pause = %q, want it waiting", reason)
	}
}

func TestMultipleDefaultModelsTriesFirstThenFallsDownTheList(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	agy := model.AgentBackend{
		ID: "agy", Framework: model.AgentFrameworkAntigravity,
		AuthMode:   model.AntigravityAuthAmbient,
		Discovered: discovered("gemini-pro-coordinator", "gemini-3.1-pro", "gemini-3.8-flash"),
	}
	pool := providerDefaultPool(t, "gemini-pro-coordinator, gemini-3.1-pro, gemini-3.8-flash", agy)
	pool.Pause = &orchestrator.Pause{}
	now := time.Now()

	// 1. First model is available -> uses gemini-pro-coordinator.
	if reason := pool.Availability(ctx, orchestrator.AgentChoice{}); reason != "" {
		t.Fatalf("Availability with first model free = %q, want empty", reason)
	}
	built1, err := pool.Build(ctx, orchestrator.AgentChoice{})
	if err != nil {
		t.Fatalf("Build 1: %v", err)
	}
	fw1 := built1.(*BackendFramework)
	if got := fw1.CredentialModel(); got != "gemini-pro-coordinator" {
		t.Fatalf("Build 1 model = %q, want gemini-pro-coordinator", got)
	}
	fw1.Lease.Release()

	// 2. Pause the first model -> falls back to second model (gemini-3.1-pro).
	pool.Pause.Begin(now, orchestrator.PauseScope{Connection: "agy", Model: "gemini-pro-coordinator", Team: model.DefaultTeamID},
		&agent.UsageLimitError{ResetAt: now.Add(time.Hour)})
	if reason := pool.Availability(ctx, orchestrator.AgentChoice{}); reason != "" {
		t.Fatalf("Availability with first model paused = %q, want empty (fallback to second)", reason)
	}
	built2, err := pool.Build(ctx, orchestrator.AgentChoice{})
	if err != nil {
		t.Fatalf("Build 2: %v", err)
	}
	fw2 := built2.(*BackendFramework)
	if got := fw2.CredentialModel(); got != "gemini-3.1-pro" {
		t.Fatalf("Build 2 model = %q, want gemini-3.1-pro", got)
	}
	fw2.Lease.Release()

	// 3. With three connections serving the three default models, test
	// stepping down through busy and paused models and returning to the first.
	agy1 := model.AgentBackend{
		ID: "agy-1", Framework: model.AgentFrameworkAntigravity,
		AuthMode: model.AntigravityAuthAmbient, Models: []string{"gemini-pro-coordinator"}, Concurrency: 1,
	}
	agy2 := model.AgentBackend{
		ID: "agy-2", Framework: model.AgentFrameworkAntigravity,
		AuthMode: model.AntigravityAuthAmbient, Models: []string{"gemini-3.1-pro"}, Concurrency: 1,
	}
	agy3 := model.AgentBackend{
		ID: "agy-3", Framework: model.AgentFrameworkAntigravity,
		AuthMode: model.AntigravityAuthAmbient, Models: []string{"gemini-3.8-flash"}, Concurrency: 1,
	}
	pool3 := providerDefaultPool(t, "gemini-pro-coordinator, gemini-3.1-pro, gemini-3.8-flash", agy1, agy2, agy3)
	pool3.Pause = &orchestrator.Pause{}

	// Take the only concurrency slot on agy-1 (serving model 1).
	held1, err := pool3.Build(ctx, orchestrator.AgentChoice{})
	if err != nil {
		t.Fatalf("pool3 Build 1: %v", err)
	}
	if got := held1.(*BackendFramework).CredentialModel(); got != "gemini-pro-coordinator" {
		t.Fatalf("pool3 Build 1 model = %q, want gemini-pro-coordinator", got)
	}

	// Pause model 2 on agy-2 -> next dispatch should fall down to model 3 on agy-3.
	pool3.Pause.Begin(now, orchestrator.PauseScope{Connection: "agy-2", Model: "gemini-3.1-pro", Team: model.DefaultTeamID},
		&agent.UsageLimitError{ResetAt: now.Add(time.Hour)})
	if reason := pool3.Availability(ctx, orchestrator.AgentChoice{}); reason != "" {
		t.Fatalf("pool3 Availability with model 1 busy and model 2 paused = %q, want empty (model 3 free)", reason)
	}
	held3, err := pool3.Build(ctx, orchestrator.AgentChoice{})
	if err != nil {
		t.Fatalf("pool3 Build 2: %v", err)
	}
	if got := held3.(*BackendFramework).CredentialModel(); got != "gemini-3.8-flash" {
		t.Fatalf("pool3 Build 2 model = %q, want gemini-3.8-flash", got)
	}

	// With model 1 busy, model 2 paused, and model 3 now busy too, Availability waits.
	reason := pool3.Availability(ctx, orchestrator.AgentChoice{})
	if reason == "" {
		t.Fatal("pool3 Availability with all 3 models unavailable = empty, want wait reason")
	}

	// Once model 1's slot is released, the next dispatch tries model 1 first again.
	held1.(*BackendFramework).Lease.Release()
	if reason := pool3.Availability(ctx, orchestrator.AgentChoice{}); reason != "" {
		t.Fatalf("pool3 Availability after releasing model 1 slot = %q, want empty", reason)
	}
	again1, err := pool3.Build(ctx, orchestrator.AgentChoice{})
	if err != nil {
		t.Fatalf("pool3 Build after release: %v", err)
	}
	defer again1.(*BackendFramework).Lease.Release()
	defer held3.(*BackendFramework).Lease.Release()
	if got := again1.(*BackendFramework).CredentialModel(); got != "gemini-pro-coordinator" {
		t.Fatalf("pool3 Build after release model = %q, want gemini-pro-coordinator", got)
	}
}

func TestPoolEnforcesDefaultConnectionConcurrencyOfFour(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	agy := model.AgentBackend{
		ID:        "agy-default-concurrency",
		Framework: model.AgentFrameworkAntigravity,
		AuthMode:  model.AntigravityAuthAmbient,
		Models:    []string{"gemini-3.1-pro"},
		// Concurrency left unset (0): defaults to model.DefaultAgentBackendConcurrency (4).
	}
	pool := providerDefaultPool(t, "gemini-3.1-pro", agy)

	var fws []*BackendFramework
	for i := 0; i < model.DefaultAgentBackendConcurrency; i++ {
		if reason := pool.Availability(ctx, orchestrator.AgentChoice{}); reason != "" {
			t.Fatalf("slot %d: Availability = %q, want empty", i+1, reason)
		}
		built, err := pool.Build(ctx, orchestrator.AgentChoice{})
		if err != nil {
			t.Fatalf("slot %d: Build: %v", i+1, err)
		}
		fws = append(fws, built.(*BackendFramework))
	}
	defer func() {
		for _, fw := range fws {
			fw.Lease.Release()
		}
	}()

	reason := pool.Availability(ctx, orchestrator.AgentChoice{})
	if !strings.Contains(reason, "at its concurrency limit (4 of 4 runs)") {
		t.Errorf("Availability at 4/4 = %q, want concurrency limit (4 of 4 runs)", reason)
	}
	if _, err := pool.Build(ctx, orchestrator.AgentChoice{}); err == nil || !strings.Contains(err.Error(), "at its concurrency limit (4 of 4 runs)") {
		t.Errorf("5th Build = %v, want concurrency limit error", err)
	}

	// Releasing one slot makes the connection available again.
	fws[0].Lease.Release()
	if reason := pool.Availability(ctx, orchestrator.AgentChoice{}); reason != "" {
		t.Errorf("Availability after releasing 1 slot = %q, want empty", reason)
	}
}

func TestLiveConfigRefreshUpdatesDepsMaxWorkersAndLimits(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db, err := OpenStore("sqlite", "", t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer db.Close()

	initial := Config{
		PollInterval: 10 * time.Second,
		MaxWorkers:   model.DefaultMaxWorkers,
		MaxMergers:   model.DefaultMaxMergers,
		GithubHost:   "github.com",
	}
	if err := store.PutConfig(ctx, initial.ToModelConfig()); err != nil {
		t.Fatalf("PutConfig initial: %v", err)
	}
	live := NewLiveConfig(store, nil, initial, nil)
	deps := orchestrator.Deps{
		MaxWorkers: initial.MaxWorkers,
		MaxMergers: initial.MaxMergers,
	}

	if got := live.limits(); got.Workers != 10 || got.Mergers != 1 {
		t.Fatalf("initial limits = %+v, want Workers=10 Mergers=1", got)
	}

	updated := initial.ToModelConfig()
	updated.MaxWorkers = 6
	updated.MaxMergers = 2
	if err := store.PutConfig(ctx, updated); err != nil {
		t.Fatalf("PutConfig updated: %v", err)
	}
	live.Refresh(ctx, &deps)
	if deps.MaxWorkers != 6 || deps.MaxMergers != 2 {
		t.Errorf("deps MaxWorkers/MaxMergers after Refresh = %d/%d, want 6/2", deps.MaxWorkers, deps.MaxMergers)
	}
	if got := live.limits(); got.Workers != 6 || got.Mergers != 2 {
		t.Errorf("live.limits() after Refresh = %+v, want Workers=6 Mergers=2", got)
	}
}

// A model paused for the reset agy named stays out of the default-model
// rotation for that whole window: every dispatch in it lands on the next
// model in the list, and the first model comes back only once the window
// is over -- not after the 15-minute default that used to send runs back
// at the same exhausted quota.
func TestRotationStaysOffAModelForTheResetAgyNamed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	agy := model.AgentBackend{
		ID: "agy", Framework: model.AgentFrameworkAntigravity,
		AuthMode:   model.AntigravityAuthAmbient,
		Discovered: discovered("gemini-pro-coordinator", "gemini-3.1-pro"),
	}
	pool := providerDefaultPool(t, "gemini-pro-coordinator, gemini-3.1-pro", agy)
	pool.Pause = &orchestrator.Pause{}
	// Opened in the past so the window's two ends can be probed against
	// the pool's own wall clock.
	window := 2*time.Hour + 28*time.Minute + 27*time.Second
	opened := time.Now().Add(-30 * time.Minute)
	pool.Pause.BeginWindow(opened, orchestrator.PauseScope{Connection: "agy", Model: "gemini-pro-coordinator", Team: model.DefaultTeamID},
		&agent.UsageLimitError{Framework: "antigravity", RetryAfter: window})

	built, err := pool.Build(ctx, orchestrator.AgentChoice{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	fw := built.(*BackendFramework)
	if got := fw.CredentialModel(); got != "gemini-3.1-pro" {
		t.Errorf("Build 30m into a %s pause = %q, want the next model in the rotation", window, got)
	}
	fw.Lease.Release()

	// The same pause opened a whole window ago has lifted, and the
	// rotation is back on its first model.
	fresh := providerDefaultPool(t, "gemini-pro-coordinator, gemini-3.1-pro", agy)
	fresh.Pause = &orchestrator.Pause{}
	fresh.Pause.BeginWindow(time.Now().Add(-window-time.Minute), orchestrator.PauseScope{Connection: "agy", Model: "gemini-pro-coordinator", Team: model.DefaultTeamID},
		&agent.UsageLimitError{Framework: "antigravity", RetryAfter: window})
	again, err := fresh.Build(ctx, orchestrator.AgentChoice{})
	if err != nil {
		t.Fatalf("Build after the window: %v", err)
	}
	defer again.(*BackendFramework).Lease.Release()
	if got := again.(*BackendFramework).CredentialModel(); got != "gemini-pro-coordinator" {
		t.Errorf("Build after the window = %q, want gemini-pro-coordinator again", got)
	}
}
