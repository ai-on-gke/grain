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
	"errors"
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

// A run's agent CLI goes to the hostd its model connection is placed on
// by name or selects by label -- its framework is built on that hostd's
// client -- a connection with no placement's to the default, and a
// placed hostd that is not connected, or is at its allowlist's
// capacity, holds a run on that connection rather than starting it
// anywhere else, unless the connection lists a fallback. A usage limit
// on an ambient credential is that hostd's own.
func TestRunsRouteToTheirConnectionsHostdWithCapacityAndPerHostdPauses(t *testing.T) {
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

	conn := func(id, hostd string, fallback ...string) model.AgentBackend {
		return model.AgentBackend{ID: id, Framework: model.AgentFrameworkAntigravity, Models: []string{"m"},
			AuthMode: model.AntigravityAuthAmbient, Hostd: hostd, HostdFallback: fallback}
	}
	byID := map[string]model.AgentBackend{}
	for _, b := range []model.AgentBackend{
		conn("unplaced", ""), conn("by-name", "ws-a"), conn("by-label", "zone=b"),
		conn("waits", "ws-off"), conn("falls-back", "ws-off", "ws-b"),
	} {
		cfg.AgentBackends = append(cfg.AgentBackends, b)
		byID[b.ID] = b
	}
	pool := NewAgentPool(cfg, store, nil)

	for _, tc := range []struct{ conn, hostd, only string }{
		{"unplaced", model.LocalHostd, "only-local"},
		{"by-name", "ws-a", "only-a"},
		{"by-label", "ws-b", "only-b"},
		{"falls-back", "ws-b", "only-b"},
	} {
		route, err := connectionHostdRoute(ctx, cfg, store, byID[tc.conn])
		if err != nil {
			t.Fatalf("%s: connectionHostdRoute = %v", tc.conn, err)
		}
		if route.Name != tc.hostd {
			t.Errorf("%s: routed to %q, want %s", tc.conn, route.Name, tc.hostd)
		}
		live := route.apply(cfg, model.AgentFrameworkAntigravity)
		r := live.ResolveCLIRun(ctx, model.AgentFrameworkAntigravity)
		if r.Via != ViaHostd || r.Err != "" {
			t.Fatalf("%s: agy resolves to %+v, want through its hostd", tc.conn, r)
		}
		// The host the run's CLI is started on is the routed hostd's.
		host, err := live.cliHostdHost(r)
		if err != nil {
			t.Fatalf("%s: cliHostdHost = %v", tc.conn, err)
		}
		// A remote hostd's endpoints are carried to its own machine, not
		// the default hostd's.
		if tc.hostd != model.LocalHostd {
			if c, ok := host.(agent.HostdHost).Remote.(*hostd.HostdCarrier); !ok || c.Name != tc.hostd {
				t.Errorf("%s: the run's CLI host carries its endpoints over %#v, want %s's", tc.conn, host.(agent.HostdHost).Remote, tc.hostd)
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
			t.Errorf("%s: the run's CLI host reaches a hostd offering %v, want %s's", tc.conn, names, tc.hostd)
		}
	}

	pinned := func(id string) orchestrator.AgentChoice {
		return orchestrator.AgentChoice{BackendID: id, Model: "m", Framework: model.AgentFrameworkAntigravity}
	}
	if why := pool.Availability(ctx, pinned("waits")); !strings.Contains(why, "waiting for hostd ws-off") {
		t.Errorf("a run on a connection placed on a disconnected hostd: availability %q, want it to wait for ws-off", why)
	}
	if _, err := pool.Build(ctx, pinned("waits")); !errors.Is(err, agent.ErrHostdWaiting) {
		t.Errorf("building a run on a connection whose hostd is away = %v, want a hostd wait", err)
	}

	// Capacity: ws-a takes one run at once.
	routeA, err := connectionHostdRoute(ctx, cfg, store, byID["by-name"])
	if err != nil {
		t.Fatal(err)
	}
	if got := pool.hostdCapacity(ctx, routeA); got != 1 {
		t.Fatalf("ws-a's capacity = %d, want its allowlist's 1", got)
	}
	if why := pool.Availability(ctx, pinned("by-name")); why != "" {
		t.Fatalf("ws-a with a free slot: availability %q, want none", why)
	}
	pool.mu.Lock()
	lease := &connectionLease{pool: pool, id: hostdLeaseKey("ws-a"), taken: time.Now()}
	pool.inflight[lease.id] = append(pool.inflight[lease.id], lease)
	pool.mu.Unlock()
	lease.start()
	if why := pool.Availability(ctx, pinned("by-name")); !strings.Contains(why, "hostd ws-a is at its capacity (1 of 1 runs)") {
		t.Errorf("ws-a full: availability %q, want it to wait for ws-a's capacity", why)
	}
	if why := pool.Availability(ctx, pinned("by-label")); why != "" {
		t.Errorf("ws-b while ws-a is full: availability %q, want none", why)
	}
	// The rotation passes over the connections whose hostd cannot take
	// a run, and only those.
	_, blocked := pool.connectionRoutes(ctx, cfg, cfg.AgentBackends, false)
	if blocked["by-name"] == nil || blocked["waits"] == nil || blocked["by-label"] != nil || blocked["unplaced"] != nil || blocked["falls-back"] != nil {
		t.Errorf("blocked connections = %v, want by-name (full) and waits (away) alone", blocked)
	}
	lease.Release()
	if why := pool.Availability(ctx, pinned("by-name")); why != "" {
		t.Errorf("ws-a after its run ended: availability %q, want none", why)
	}

	// An ambient credential's usage limit is keyed to the hostd its
	// connection runs on; a key grain holds, and the default hostd, keep
	// the bare one.
	routes, _ := pool.connectionRoutes(ctx, cfg, cfg.AgentBackends, true)
	req := poolRequest{routes: routes}
	ambient := byID["by-name"]
	keyed := ambient
	keyed.AuthMode = ""
	if got := req.pauseOwner(cfg, ambient); got != "by-name@hostd/ws-a" {
		t.Errorf("ambient on ws-a pauses %q, want by-name@hostd/ws-a", got)
	}
	if got := req.pauseOwner(cfg, keyed); got != "by-name" {
		t.Errorf("an API key on ws-a pauses %q, want by-name", got)
	}
	if got := req.pauseOwner(cfg, byID["unplaced"]); got != "unplaced" {
		t.Errorf("the default hostd's connection pauses %q, want its bare key", got)
	}
	// And a task waiting on such a pause says whose it is.
	if got := req.pausedConnection(cfg, ambient); got != `connection "by-name" on hostd ws-a` {
		t.Errorf("ambient on ws-a is named %q, want the hostd named beside the connection", got)
	}
	if got := req.pausedConnection(cfg, keyed); got != `connection "by-name"` {
		t.Errorf("an API key on ws-a is named %q, want the connection alone", got)
	}
	pool.Pause = &orchestrator.Pause{}
	pool.Pause.Begin(time.Now(), orchestrator.PauseScope{Connection: "by-name@hostd/ws-a", Model: "m", Team: model.DefaultTeamID},
		&agent.UsageLimitError{Message: "out of quota"})
	moved := poolRequest{routes: map[string]hostdRoute{"by-name": routes["by-label"]}}
	pool.mu.Lock()
	pausedOnA := pool.pausedLocked(cfg, req, ambient, "m", time.Now())
	pausedElsewhere := pool.pausedLocked(cfg, moved, ambient, "m", time.Now())
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
