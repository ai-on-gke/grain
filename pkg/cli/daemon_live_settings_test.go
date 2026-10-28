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

package cli

// Settings without a restart: liveConfig re-reads grain_config once per
// reconcile tick and hands each change to whatever it configures, so a
// value changed in the UI (or by `grain settings`) changes what this
// process is doing rather than what its next process would do. These
// tests cover the three changes liveConfig applies itself -- the poll
// interval, the capability registry, and a sandbox backend's default VM
// shape -- the two settings it deliberately refuses to apply, and
// dispatchConfig's own per-dispatch half of the same promise.

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// startupConfig is what a daemon has after loadConfig: flags parsed,
// grain_config seeded from them.
func startupConfig() daemon.Config {
	return daemon.Config{
		PollInterval: 30 * time.Second, MaxWorkers: 1,
		AgentFramework: model.AgentFrameworkAntigravity,
		GeminiModel:    "gemini-2.5-pro", ClaudeModel: "claude-sonnet-5",
		GithubHost: "github.com",
	}
}

// putConfig writes mc as the grain_config row an operator's own save
// through the Settings pane would have left behind.
func putConfig(t *testing.T, store *model.Store, mc model.Config) {
	t.Helper()
	if err := store.PutConfig(context.Background(), mc); err != nil {
		t.Fatalf("storing configuration: %v", err)
	}
}

func TestLiveConfigAppliesAChangedPollInterval(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := testStore(t)
	cfg := startupConfig()
	putConfig(t, store, cfg.ToModelConfig())
	live := daemon.NewLiveConfig(store, nil, cfg, nil)
	deps := orchestrator.Deps{}

	// Nothing changed yet: the loop is told not to disturb its ticker.
	if live.Refresh(ctx, &deps) {
		t.Fatal("refresh reported the poll interval changed when nothing had")
	}

	changed := cfg.ToModelConfig()
	changed.PollInterval = 5 * time.Second
	putConfig(t, store, changed)

	if !live.Refresh(ctx, &deps) {
		t.Fatal("refresh did not report the changed poll interval, so the ticker would keep the old one")
	}
	if got := live.Current().PollInterval; got != 5*time.Second {
		t.Fatalf("poll interval in effect = %s, want 5s", got)
	}
}

// The other half of that promise: what liveConfig reports as in effect
// has to be what this *process* is running, not a copy of the row. A
// restart-only setting changed in the store is exactly the case where
// the two differ, and reporting the stored value would make the Settings
// pane say a change had been applied when nothing had applied it.
func TestLiveConfigNeverAdoptsARestartOnlySetting(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := testStore(t)
	cfg := startupConfig()
	putConfig(t, store, cfg.ToModelConfig())
	live := daemon.NewLiveConfig(store, nil, cfg, nil)
	deps := orchestrator.Deps{}

	changed := cfg.ToModelConfig()
	changed.GitHubHost = "github.internal"
	changed.GitHubInsecureHTTP = true
	putConfig(t, store, changed)
	live.Refresh(ctx, &deps)

	now := live.Current()
	if now.GithubHost != "github.com" || now.GithubInsecureHTTP {
		t.Fatalf("github host in effect = %q/%t, want the startup value github.com/false", now.GithubHost, now.GithubInsecureHTTP)
	}
	if got := live.ModelConfig().GitHubHost; got != "github.com" {
		t.Fatalf("modelConfig().GitHubHost = %q, want what the process is running with", got)
	}
}

// A GCP project set in Settings enables the gcp-key/gemini-key
// capabilities, which is only true if the registry a cycle resolves
// grants against is rebuilt -- otherwise a task granted one is refused
// by model.ResolveGrants until someone restarts the daemon.
func TestLiveConfigRebuildsCapabilityProvidersWhenGCPChanges(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := testStore(t)
	cfg := startupConfig()
	putConfig(t, store, cfg.ToModelConfig())
	live := daemon.NewLiveConfig(store, nil, cfg, nil)
	deps := orchestrator.Deps{Config: orchestrator.Config{
		Capabilities: model.NewCapabilityRegistry(daemon.CapabilityProviders(cfg, nil, daemon.NewModelProxies(), nil, nil)...),
	}}

	if _, ok := deps.Config.Capabilities.Lookup("gcp-key"); ok {
		t.Fatal("gcp-key is registered with no GCP project configured")
	}

	changed := cfg.ToModelConfig()
	changed.GCPProject = "acme-proj"
	changed.GCPServiceAccountEmail = "agent@acme-proj.iam.gserviceaccount.com"
	putConfig(t, store, changed)
	live.Refresh(ctx, &deps)

	if _, ok := deps.Config.Capabilities.Lookup("gcp-key"); !ok {
		t.Fatal("gcp-key is still unregistered after a GCP project was configured -- the registry was not rebuilt")
	}
}

// shapedSandboxes is a sandbox backend that records the default shape it
// is told to adopt -- orchestrator.GranuleSandboxes' own SetDefaultShape,
// which is what actually carries this on a real deployment, minus the
// VMs.
type shapedSandboxes struct {
	orchestrator.Sandboxes
	shape orchestrator.Shape
	set   int
}

func (s *shapedSandboxes) SetDefaultShape(shape orchestrator.Shape) {
	s.shape = shape
	s.set++
}

func TestLiveConfigPushesAChangedSandboxShapeToTheBackend(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := testStore(t)
	cfg := startupConfig()
	putConfig(t, store, cfg.ToModelConfig())
	sandboxes := &shapedSandboxes{}
	live := daemon.NewLiveConfig(store, sandboxes, cfg, nil)
	deps := orchestrator.Deps{}

	live.Refresh(ctx, &deps)
	if sandboxes.set != 0 {
		t.Fatalf("the backend was reshaped %d time(s) with nothing changed", sandboxes.set)
	}

	changed := cfg.ToModelConfig()
	changed.SandboxCPUs = 4
	changed.SandboxMemoryMB = 8192
	changed.SandboxDiskGB = 40
	putConfig(t, store, changed)
	live.Refresh(ctx, &deps)

	if want := (orchestrator.Shape{CPUs: 4, MemoryMB: 8192, DiskGB: 40}); sandboxes.shape != want {
		t.Fatalf("default shape = %+v, want %+v", sandboxes.shape, want)
	}

	// And a change to the disk alone reaches the backend too: the three
	// dimensions are compared independently (liveConfig's own reshape
	// check), not as one all-or-nothing pair of CPU and memory.
	changed.SandboxDiskGB = 80
	putConfig(t, store, changed)
	live.Refresh(ctx, &deps)

	if want := (orchestrator.Shape{CPUs: 4, MemoryMB: 8192, DiskGB: 80}); sandboxes.shape != want {
		t.Fatalf("default shape after a disk-only change = %+v, want %+v", sandboxes.shape, want)
	}
}

// A backend with no shape to set -- orchestrator.HostSandboxes, a local
// directory -- is not an error, just a pair of settings with nothing to
// apply to. Nothing here may panic on one.
func TestLiveConfigToleratesABackendWithNoShape(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := testStore(t)
	cfg := startupConfig()
	putConfig(t, store, cfg.ToModelConfig())
	live := daemon.NewLiveConfig(store, orchestrator.NewHostSandboxes(t.TempDir()), cfg, nil)
	deps := orchestrator.Deps{}

	changed := cfg.ToModelConfig()
	changed.SandboxCPUs = 4
	putConfig(t, store, changed)
	live.Refresh(ctx, &deps)

	if got := live.Current().SandboxCPUs; got != 4 {
		t.Fatalf("sandboxCPUs in effect = %d, want the stored 4 even with nothing to apply it to", got)
	}
}

// The sandbox budget is three settings now rather than three flags
// (grain/task-197), and the whole point of moving them is that a
// deployment being throttled can be widened while it runs: the gate and
// the dispatch ask config.budget() per job, and config.budget() is built
// from fields withModelConfig refreshes each tick. A budget that only
// changed at the next restart would be a flag with extra steps.
func TestLiveConfigAppliesAChangedSandboxBudget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := testStore(t)
	cfg := startupConfig()
	cfg.HostCPUs, cfg.HostMemoryMB = 8, 32*1024
	cfg.CpuOvercommit, cfg.MemoryOvercommit = model.DefaultCPUOvercommit, model.DefaultMemoryOvercommit
	cfg.MemoryReserveMB = model.ReserveMemoryDefault
	putConfig(t, store, cfg.ToModelConfig())
	live := daemon.NewLiveConfig(store, nil, cfg, nil)
	deps := orchestrator.Deps{}

	changed := cfg.ToModelConfig()
	changed.SandboxCPUOvercommit = 4
	changed.SandboxMemoryOvercommit = 2
	changed.SandboxMemoryReserveMB = 4096
	putConfig(t, store, changed)
	live.Refresh(ctx, &deps)

	budget := live.Current().Budget()
	if budget.ReserveMemoryMB != 4096 {
		t.Errorf("reserve in effect = %d MiB, want the stored 4096", budget.ReserveMemoryMB)
	}
	if cpus, ok := budget.CPUBudget(); !ok || cpus != 24 {
		t.Errorf("cpu budget = %v (bounded %t), want 24 vCPU: 8 host vCPU less 2 reserved at the stored 4x", cpus, ok)
	}
	if memMB, ok := budget.MemoryBudgetMB(); !ok || memMB != 2*(32*1024-4096) {
		t.Errorf("memory budget = %d MiB (bounded %t), want %d: 32 GiB less the stored 4 GiB reserve, doubled",
			memMB, ok, 2*(32*1024-4096))
	}
}

// The other half of the same promise: a row whose reserve is the whole
// machine leaves a budget of nothing, which admits one sandbox at a time
// and queues everything else behind it. ui.UpdateSettings refuses to
// store one, so such a row arrived by some path with no validation in
// front of it -- and this daemon is the only reader that knows how big
// the host is.
func TestStoredSandboxReserveLargerThanTheHostFallsBackToGrainsOwn(t *testing.T) {
	t.Parallel()
	cfg := startupConfig()
	cfg.HostCPUs, cfg.HostMemoryMB = 8, 32*1024
	cfg.CpuOvercommit, cfg.MemoryOvercommit = model.DefaultCPUOvercommit, model.DefaultMemoryOvercommit
	cfg.MemoryReserveMB = model.ReserveMemoryDefault

	stored := cfg.ToModelConfig()
	stored.SandboxMemoryReserveMB = 64 * 1024
	stored.SandboxCPUOvercommit = -2

	runnable, notes := cfg.RunnableStoredConfig(stored)

	if runnable.SandboxMemoryReserveMB != model.ReserveMemoryDefault {
		t.Errorf("reserve = %d, want grain's own (%d)", runnable.SandboxMemoryReserveMB, model.ReserveMemoryDefault)
	}
	if runnable.SandboxCPUOvercommit != model.DefaultCPUOvercommit {
		t.Errorf("cpu overcommit = %v, want the daemon's own %v rather than a negative ratio",
			runnable.SandboxCPUOvercommit, model.DefaultCPUOvercommit)
	}
	// One note per substitution, whatever else this configuration has to
	// say: a value silently not in effect is the failure these notes
	// exist for.
	for _, want := range []string{"sandbox cpu overcommit", "sandbox memory reserve"} {
		if !slices.ContainsFunc(notes, func(note string) bool { return strings.Contains(note, want) }) {
			t.Errorf("notes = %q, want one naming %q", notes, want)
		}
	}
}

// A row that has no value for a field -- written by an older build, or
// by a migration that added the column -- must not be adopted as one: ""
// is not a model any framework can be asked for, and a zero duration is
// not an interval a ticker can be built from.
func TestLiveConfigKeepsStartupValuesForUnsetStoredFields(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := testStore(t)
	cfg := startupConfig()
	putConfig(t, store, model.Config{})
	live := daemon.NewLiveConfig(store, nil, cfg, nil)
	deps := orchestrator.Deps{}

	live.Refresh(ctx, &deps)

	now := live.Current()
	if now.PollInterval != 30*time.Second {
		t.Fatalf("poll interval = %s, want the startup 30s", now.PollInterval)
	}
	if now.GeminiModel != "gemini-2.5-pro" || now.ClaudeModel != "claude-sonnet-5" {
		t.Fatalf("models = %q/%q, want the startup ones", now.GeminiModel, now.ClaudeModel)
	}
	if now.AgentFramework != model.AgentFrameworkAntigravity {
		t.Fatalf("agent framework = %q, want the startup one", now.AgentFramework)
	}
}
