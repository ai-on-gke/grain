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

//go:build linux

package daemon

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// While grain hostd drains for its own upgrade, a task whose CLI runs on
// the host waits in the queue saying so, rather than being dispatched into
// a sandbox only for hostd to refuse it; a task whose CLI runs in the
// container is dispatched as usual.
func TestHostRunsWaitWhileHostdDrains(t *testing.T) {
	socket := startTestHostd(t, offersAgy)
	ctx := context.Background()
	agy := model.AgentBackend{ID: "agy", Framework: model.AgentFrameworkAntigravity,
		AuthMode: model.AntigravityAuthAmbient, Discovered: discovered("gemini-3.1-pro")}
	claude := model.AgentBackend{ID: "claude", Framework: model.AgentFrameworkClaude, Discovered: discovered("claude-z")}
	pool := providerDefaultPool(t, "", agy, claude)
	pool.cfg = withExecutor(Config{HostdSocket: socket, AgyRun: model.CLIRunHost, ClaudeRun: model.CLIRunContainer},
		inContainer, nil)

	onHost := []orchestrator.AgentChoice{{Model: "gemini-3.1-pro"}, {BackendID: "agy"}}
	inContainer := []orchestrator.AgentChoice{{Model: "claude-z"}, {BackendID: "claude"}}
	for _, choice := range append(onHost, inContainer...) {
		if reason := pool.Availability(ctx, choice); reason != "" {
			t.Fatalf("Availability(%+v) before the drain = %q, want it dispatchable", choice, reason)
		}
	}

	drainTestHostd(t, socket)

	for _, choice := range onHost {
		reason := pool.Availability(ctx, choice)
		if !strings.Contains(reason, "agy runs on the host") || !strings.Contains(reason, "draining for its own upgrade") {
			t.Errorf("Availability(%+v) while hostd drains = %q, want it waiting on hostd", choice, reason)
		}
	}
	for _, choice := range inContainer {
		if reason := pool.Availability(ctx, choice); reason != "" {
			t.Errorf("Availability(%+v) while hostd drains = %q, want a container run dispatchable", choice, reason)
		}
	}

	// With no connections, the daemon's own framework is the one asked about.
	if err := pool.Store.UpdateConfig(ctx, func(c *model.Config) error {
		c.AgentBackends = nil
		c.AgentFramework = model.AgentFrameworkAntigravity
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if reason := pool.Availability(ctx, orchestrator.AgentChoice{}); !strings.Contains(reason, "draining") {
		t.Errorf("Availability on the daemon's own agy while hostd drains = %q, want it waiting on hostd", reason)
	}
	if reason := pool.Availability(ctx, orchestrator.AgentChoice{Framework: model.AgentFrameworkClaude}); reason != "" {
		t.Errorf("Availability on claude in the container while hostd drains = %q, want it dispatchable", reason)
	}
}

// drainTestHostd drains the test hostd at socket with a run still going,
// which holds the drain open: with none, hostd would exit at once.
func drainTestHostd(t *testing.T, socket string) {
	t.Helper()
	ctx := context.Background()
	client := hostd.Client{Socket: socket}
	holding := make(chan hostd.Started, 1)
	holder := client
	holder.Detach = func(context.Context) bool { return true }
	holder.OnStarted = func(st hostd.Started) { holding <- st }
	hctx, stopHolder := context.WithCancel(ctx)
	go func() {
		_, _ = holder.Run(hctx, hostd.Request{Kind: hostd.KindCLI, Name: "agy", Args: []string{"-c", "sleep 30"},
			Dir: strings.TrimSuffix(socket, "/hostd.sock"), Detach: true, Run: "holder", Deadline: time.Now().Add(time.Minute)},
			nil, nil, nil)
	}()
	var held hostd.Started
	select {
	case held = <-holding:
	case <-time.After(10 * time.Second):
		t.Fatal("hostd never started the holding run")
	}
	t.Cleanup(func() {
		stopHolder()
		_ = client.Cancel(context.Background(), held.ID)
	})
	if err := client.Drain(ctx); err != nil {
		t.Fatal(err)
	}
}

// drainPool is a pool whose agy connection serves gemini-pro-coordinator
// on the host through the test hostd at socket, and whose claude one
// serves claude-opus-5-5 in the container, with defaultModels as the
// deployment's default model list.
func drainPool(t *testing.T, socket, defaultModels string) *AgentPool {
	t.Helper()
	agy := model.AgentBackend{ID: "agy", Framework: model.AgentFrameworkAntigravity,
		AuthMode: model.AntigravityAuthAmbient, Models: []string{"gemini-pro-coordinator"}}
	cl := model.AgentBackend{ID: "claude", Framework: model.AgentFrameworkClaude,
		AuthMode: model.AntigravityAuthAmbient, CLIPath: "/bin/true", Models: []string{"claude-opus-5-5"}}
	pool := providerDefaultPool(t, defaultModels, agy, cl)
	pool.cfg = withExecutor(Config{HostdSocket: socket, AgyRun: model.CLIRunHost, ClaudeRun: model.CLIRunContainer},
		inContainer, nil)
	return pool
}

// A default model list that puts a host-run model first falls back to
// the container-run one after it while hostd drains, rather than picking
// agy only for hostd to refuse it. A task pinned to the host-run model
// has nothing to fall back to, and waits on hostd.
func TestDefaultModelsFallBackToTheContainerWhileHostdDrains(t *testing.T) {
	socket := startTestHostd(t, offersAgy)
	ctx := context.Background()
	pool := drainPool(t, socket, "gemini-pro-coordinator, claude-opus-5-5")
	pinned := orchestrator.AgentChoice{Model: "gemini-pro-coordinator"}
	for _, choice := range []orchestrator.AgentChoice{{}, pinned} {
		if reason := pool.Availability(ctx, choice); reason != "" {
			t.Fatalf("Availability(%+v) before the drain = %q, want it dispatchable", choice, reason)
		}
	}

	drainTestHostd(t, socket)

	fw := buildChecked(t, pool, orchestrator.AgentChoice{}, "claude-opus-5-5", model.AgentFrameworkClaude)
	if !strings.Contains(fw.Fallback, "gemini-pro-coordinator runs on the host, and grain hostd is updating") {
		t.Errorf("fallback = %q, want it to say hostd is why gemini-pro-coordinator was passed over", fw.Fallback)
	}
	reason := pool.Availability(ctx, pinned)
	if !strings.Contains(reason, "agy runs on the host") || !strings.Contains(reason, "draining for its own upgrade") {
		t.Errorf("Availability pinned to the host-run model while hostd drains = %q, want it waiting on hostd", reason)
	}
	if _, err := pool.Build(ctx, pinned); err == nil || !strings.Contains(err.Error(), "grain hostd updates") {
		t.Errorf("Build pinned to the host-run model while hostd drains = %v, want it refused naming hostd", err)
	}
}

// With no model chosen the rotation spans both connections; while hostd
// drains it never lands on the one that runs agy on the host. With only
// host-run connections there is nothing to rotate onto, and the run waits
// on hostd.
func TestRotationNeverPicksAHostConnectionWhileHostdDrains(t *testing.T) {
	socket := startTestHostd(t, offersAgy)
	ctx := context.Background()
	pool := drainPool(t, socket, "")
	drainTestHostd(t, socket)

	for i := 0; i < 6; i++ {
		buildChecked(t, pool, orchestrator.AgentChoice{}, "claude-opus-5-5", model.AgentFrameworkClaude).Lease.Release()
	}

	if err := pool.Store.UpdateConfig(ctx, func(c *model.Config) error {
		c.AgentBackends = slices.DeleteFunc(c.AgentBackends, func(b model.AgentBackend) bool { return b.ID == "claude" })
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if reason := pool.Availability(ctx, orchestrator.AgentChoice{}); !strings.Contains(reason, "agy runs on the host") {
		t.Errorf("Availability with only host-run connections while hostd drains = %q, want it waiting on hostd", reason)
	}
}
