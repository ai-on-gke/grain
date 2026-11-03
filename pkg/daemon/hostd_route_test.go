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
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/whererun"
)

// A run's agent CLI goes to the hostd its task is pinned to by name or
// selects by label -- its framework is built on that hostd's client --
// an unpinned task's to the default, and a pinned hostd that is not
// connected, or is at its allowlist's capacity, holds the task rather
// than starting it anywhere else. A usage limit on an ambient credential
// is that hostd's own.
func TestRunsRouteToTheirTasksHostdWithCapacityAndPerHostdPauses(t *testing.T) {
	ctx := context.Background()
	store, closeStore := openTestStore(t, t.TempDir())
	t.Cleanup(closeStore)
	if err := store.EnsureLocalHostd(ctx); err != nil {
		t.Fatal(err)
	}
	_, tokA, err := store.CreateHostd(ctx, model.Hostd{Name: "ws-a", Owner: "alice@example.com", Labels: map[string]string{"gpu": "yes"}})
	if err != nil {
		t.Fatal(err)
	}
	_, tokB, err := store.CreateHostd(ctx, model.Hostd{Name: "ws-b", Labels: map[string]string{"zone": "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateHostd(ctx, model.Hostd{Name: "ws-off"}); err != nil {
		t.Fatal(err)
	}

	sock := startTestHostd(t, "clis:\n  agy:\n    path: %[1]s\n    paths_under: [%[2]s]\n  only-local:\n    path: %[1]s\n    paths_under: [%[2]s]\n")
	cfg := withExecutor(Config{HostdSocket: sock, AgyRun: model.CLIRunHost}, whererun.Detection{Signal: "/.dockerenv"}, nil)
	cfg.HostdHub = newHostdHub(cfg, store)
	ctl := httptest.NewServer(cfg.HostdHub)
	t.Cleanup(ctl.Close)
	startDialingHostd(t, ctl.URL, tokA, "capacity: 1\nclis:\n  agy:\n    path: %[1]s\n    paths_under: [%[2]s]\n  only-a:\n    path: %[1]s\n    paths_under: [%[2]s]\n")
	startDialingHostd(t, ctl.URL, tokB, "clis:\n  agy:\n    path: %[1]s\n    paths_under: [%[2]s]\n  only-b:\n    path: %[1]s\n    paths_under: [%[2]s]\n")
	waitFor(t, func() bool {
		return cfg.HostdHub.HostdStatus("ws-a").Connected && cfg.HostdHub.HostdStatus("ws-b").Connected
	})

	for id, selector := range map[string]string{"by-name": "ws-a", "by-label": "zone=b", "waits": "ws-off"} {
		if err := store.SetTaskHostd(ctx, id, selector); err != nil {
			t.Fatal(err)
		}
	}
	pool := NewAgentPool(cfg, store, nil)

	for _, tc := range []struct{ task, hostd, only string }{
		{"unpinned", model.LocalHostd, "only-local"},
		{"by-name", "ws-a", "only-a"},
		{"by-label", "ws-b", "only-b"},
	} {
		route, err := pool.routeHostd(ctx, cfg, tc.task)
		if err != nil {
			t.Fatalf("%s: routeHostd = %v", tc.task, err)
		}
		if route.Name != tc.hostd {
			t.Errorf("%s: routed to %q, want %s", tc.task, route.Name, tc.hostd)
		}
		live := route.apply(cfg)
		r := live.ResolveCLIRun(ctx, model.AgentFrameworkAntigravity)
		if r.Via != ViaHostd || r.Err != "" {
			t.Fatalf("%s: agy resolves to %+v, want through its hostd", tc.task, r)
		}
		// The host the run's CLI is started on is the routed hostd's.
		host, err := live.cliHostdHost(r)
		if err != nil {
			t.Fatalf("%s: cliHostdHost = %v", tc.task, err)
		}
		// A remote hostd's endpoints are carried to its own machine, not
		// the default hostd's.
		if tc.hostd != model.LocalHostd {
			if c, ok := host.(agent.HostdHost).Remote.(*hostd.HostdCarrier); !ok || c.Name != tc.hostd {
				t.Errorf("%s: the run's CLI host carries its endpoints over %#v, want %s's", tc.task, host.(agent.HostdHost).Remote, tc.hostd)
			}
		}
		offers, err := host.(agent.HostdHost).Client.Offers(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, o := range offers {
			names = append(names, o.Name)
		}
		if !slices.Contains(names, tc.only) {
			t.Errorf("%s: the run's CLI host reaches a hostd offering %v, want %s's", tc.task, names, tc.hostd)
		}
	}

	if why := pool.Availability(ctx, orchestrator.AgentChoice{TaskID: "waits"}); why != "waiting for hostd ws-off" {
		t.Errorf("a task pinned to a disconnected hostd: availability %q, want it to wait for ws-off", why)
	}

	// Capacity: ws-a takes one run at once.
	routeA, err := pool.routeHostd(ctx, cfg, "by-name")
	if err != nil {
		t.Fatal(err)
	}
	if got := pool.hostdCapacity(ctx, routeA); got != 1 {
		t.Fatalf("ws-a's capacity = %d, want its allowlist's 1", got)
	}
	choice := orchestrator.AgentChoice{TaskID: "by-name", Framework: model.AgentFrameworkAntigravity}
	if why := pool.Availability(ctx, choice); why != "" {
		t.Fatalf("ws-a with a free slot: availability %q, want none", why)
	}
	pool.mu.Lock()
	lease := &connectionLease{pool: pool, id: hostdLeaseKey("ws-a"), taken: time.Now()}
	pool.inflight[lease.id] = append(pool.inflight[lease.id], lease)
	pool.mu.Unlock()
	lease.start()
	if why := pool.Availability(ctx, choice); !strings.Contains(why, "hostd ws-a is at its capacity (1 of 1 runs)") {
		t.Errorf("ws-a full: availability %q, want it to wait for ws-a's capacity", why)
	}
	if why := pool.Availability(ctx, orchestrator.AgentChoice{TaskID: "by-label", Framework: model.AgentFrameworkAntigravity}); why != "" {
		t.Errorf("ws-b while ws-a is full: availability %q, want none", why)
	}
	lease.Release()
	if why := pool.Availability(ctx, choice); why != "" {
		t.Errorf("ws-a after its run ended: availability %q, want none", why)
	}

	// An ambient credential's usage limit is keyed to the hostd it was
	// met on; a key grain holds, and the default hostd, keep the bare one.
	ambient := model.AgentBackend{ID: "c1", AuthMode: model.AntigravityAuthAmbient}
	keyed := model.AgentBackend{ID: "c2"}
	reqA := poolRequest{}
	if routeA.scopesPauses(cfg) {
		reqA.pauseHostd = routeA.Name
	}
	if got := reqA.pauseOwner(cfg, ambient); got != "c1@hostd/ws-a" {
		t.Errorf("ambient on ws-a pauses %q, want c1@hostd/ws-a", got)
	}
	if got := reqA.pauseOwner(cfg, keyed); got != "c2" {
		t.Errorf("an API key on ws-a pauses %q, want c2", got)
	}
	routeLocal, _ := pool.routeHostd(ctx, cfg, "unpinned")
	if routeLocal.scopesPauses(cfg) {
		t.Error("the default hostd scopes its pauses; want the connection's bare key")
	}
	pool.Pause = &orchestrator.Pause{}
	pool.Pause.Begin(time.Now(), orchestrator.PauseScope{Connection: "c1@hostd/ws-a", Model: "m", Team: model.DefaultTeamID},
		&agent.UsageLimitError{Message: "out of quota"})
	pool.mu.Lock()
	pausedOnA := pool.pausedLocked(cfg, reqA, ambient, "m", time.Now())
	pausedElsewhere := pool.pausedLocked(cfg, poolRequest{pauseHostd: "ws-b"}, ambient, "m", time.Now())
	pool.mu.Unlock()
	if !pausedOnA || pausedElsewhere {
		t.Errorf("paused on ws-a = %v, on ws-b = %v; want only ws-a paused", pausedOnA, pausedElsewhere)
	}

	// The audit: the hostd, its instance and its owner.
	instance, owner := pool.hostdAudit(ctx, cfg, routeA)
	if instance != "cloudtop" || owner != "alice@example.com" {
		t.Errorf("ws-a's audit = %q, %q; want its instance and registry owner", instance, owner)
	}
}
