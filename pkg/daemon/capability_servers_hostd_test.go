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
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/capability/servers"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/whererun"
)

// hostScriptCaller is a capability server whose one control tool,
// run_script, answers with the hostd it was started through: the
// allowlist entry only that hostd offers.
type hostScriptCaller struct{ hostd string }

func (c hostScriptCaller) ListTools(context.Context) ([]protocol.Tool, error) {
	return []protocol.Tool{{Name: "run_script", InputSchema: map[string]any{"type": "object"}}}, nil
}

func (c hostScriptCaller) CallTool(context.Context, string, map[string]any) ([]byte, error) {
	return []byte("ran on " + c.hostd), nil
}

func (c hostScriptCaller) Close() error { return nil }

// A run: hostd capability server placed on a hostd of its own -- its
// host scripts -- is started on that hostd, whichever task's run holds a
// grant of it, while an unplaced run: host server stays on the default
// hostd and an in-container one the deployment's. Each audit record says
// which hostd its own server ran on, and a server whose hostd is not
// connected cannot be minted, saying needs-hostd.
func TestAPlacedCapabilityServerRunsOnItsOwnHostd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	store, closeStore := openTestStore(t, t.TempDir())
	t.Cleanup(closeStore)
	if err := store.EnsureLocalHostd(ctx); err != nil {
		t.Fatal(err)
	}
	_, tokA, err := store.CreateHostd(ctx, model.Hostd{Name: "ws-a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateHostd(ctx, model.Hostd{Name: "ws-off"}); err != nil {
		t.Fatal(err)
	}
	sock := startTestHostd(t, "clis:\n  only-local:\n    path: %[1]s\n    paths_under: [%[2]s]\n")
	cfg := withExecutor(Config{HostdSocket: sock}, whererun.Detection{Signal: "/.dockerenv"}, nil)
	cfg.HostdHub = newHostdHub(cfg, store)
	ctl := httptest.NewServer(cfg.HostdHub)
	t.Cleanup(ctl.Close)
	startDialingHostd(t, ctl.URL, tokA, "clis:\n  only-a:\n    path: %[1]s\n    paths_under: [%[2]s]\n")
	waitFor(t, func() bool { return cfg.HostdHub.HostdStatus("ws-a").Connected })

	// Every server is attached to a hostScriptCaller saying where it
	// runs: a run: hostd one on the hostd its options reach, as
	// protocol.Attach starts it.
	attach := func(ctx context.Context, id string, cfg protocol.ServerConfig, _ protocol.SecretLookup, opts protocol.DialOptions) (*protocol.Server, error) {
		where := "the container"
		if cfg.OnHost() {
			offers, err := opts.Hostd.Offers(ctx)
			if err != nil {
				return nil, err
			}
			for _, o := range offers {
				if strings.HasPrefix(o.Name, "only-") {
					where = o.Name
				}
			}
		}
		return protocol.NewServer(ctx, id, hostScriptCaller{hostd: where})
	}
	const (
		hostScripts  = "https://example.com/caps/host-scripts"
		hostScriptsA = "https://example.com/caps/host-scripts-ws-a"
		offScripts   = "https://example.com/caps/host-scripts-ws-off"
		inContainer  = "https://example.com/caps/in-container"
	)
	configured := map[string]protocol.ServerConfig{
		hostScripts:  {Command: "grain-host-scripts", Run: protocol.RunHost},
		hostScriptsA: {Command: "grain-host-scripts", Run: protocol.RunHostd, Hostd: "ws-a"},
		offScripts:   {Command: "grain-host-scripts", Run: protocol.RunHostd, Hostd: "ws-off"},
		inContainer:  {Command: "in-container"},
	}
	deployment := deploymentCapabilityServers(configured)
	if _, ok := deployment[hostScriptsA]; ok {
		t.Fatal("the deployment's registry attaches a server placed on ws-a")
	}
	client := cfg.capabilityHostd()
	opts := servers.Options{Hostd: client, Attach: attach}
	base := servers.New(opts)
	base.Sync(ctx, deployment)
	h := newHostdCapabilityServers(ctx, base, opts, store)
	h.update(configured)
	live := NewLiveConfig(store, nil, cfg, nil).WithCapabilityServers(base).withHostdCapabilityServers(h)
	routed, ok := capabilityServerLookup(live).(orchestrator.TaskCapabilityServers)
	if !ok {
		t.Fatal("the daemon's capability servers do not route placed servers to their hostd")
	}

	call := func(srvs orchestrator.CapabilityServers, id string) string {
		t.Helper()
		srv, err := srvs.Lookup(id)
		if err != nil {
			t.Fatalf("Lookup(%s) = %v", id, err)
		}
		out, err := srv.CallTool(ctx, "run_script", map[string]any{}, 5*time.Second)
		if err != nil {
			t.Fatalf("calling %s: %v", id, err)
		}
		return string(out)
	}
	// Any task: the placement is the server's, not the task's.
	srvs, hostd, err := routed.ForTask(ctx, "any-task")
	if err != nil {
		t.Fatalf("ForTask = %v", err)
	}
	if hostd != model.LocalHostd {
		t.Errorf("the run's default capability hostd is %q, want %s", hostd, model.LocalHostd)
	}
	if got := call(srvs, hostScripts); got != "ran on only-local" {
		t.Errorf("the unplaced host-script server %s, want the default hostd", got)
	}
	if got := call(srvs, hostScriptsA); got != "ran on only-a" {
		t.Errorf("the server placed on ws-a %s, want ws-a", got)
	}
	if got := call(srvs, inContainer); got != "ran on the container" {
		t.Errorf("the in-container server %s, want the deployment's", got)
	}
	of, ok := srvs.(orchestrator.CapabilityHostdOf)
	if !ok {
		t.Fatal("a run with placed servers cannot say which hostd each ran on")
	}
	if name, ok := of.HostdOf(hostScriptsA); !ok || name != "ws-a" {
		t.Errorf("HostdOf(placed on ws-a) = %q, %v; want ws-a", name, ok)
	}
	if _, ok := of.HostdOf(hostScripts); ok {
		t.Error("HostdOf names a hostd for an unplaced server; want the run's default")
	}
	if _, err := srvs.Lookup(offScripts); err == nil || !strings.Contains(err.Error(), "needs-hostd:ws-off") {
		t.Errorf("a server on a disconnected hostd: Lookup = %v, want needs-hostd:ws-off", err)
	}

	// Settings lists the placed servers beside the deployment's: the one
	// on ws-a as its own registry has it, the one on ws-off as waiting.
	rows := map[string]servers.Status{}
	for _, st := range capabilityServerStatuses(live).Statuses() {
		rows[st.ID] = st
	}
	if got := rows[offScripts].State; got != NeedsHostd("ws-off") {
		t.Errorf("the server on ws-off reads %q in Settings, want %q", got, NeedsHostd("ws-off"))
	}
	if got := rows[hostScriptsA].State; got != servers.Attached {
		t.Errorf("the server on ws-a reads %q in Settings, want attached", got)
	}
	if _, ok := rows[inContainer]; !ok {
		t.Error("Settings lost the deployment's own servers")
	}

	// A server dropped from mcp_servers goes from the hostd's registry too.
	h.update(map[string]protocol.ServerConfig{inContainer: configured[inContainer]})
	h.mu.Lock()
	regA := h.regs["ws-a"].reg
	h.mu.Unlock()
	waitFor(t, func() bool {
		_, err := regA.Lookup(hostScriptsA)
		return err != nil
	})
}
