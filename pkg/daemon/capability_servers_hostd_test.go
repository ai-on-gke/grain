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

// A pinned task's run: host capability server -- its host scripts --
// is started on the hostd the task is pinned to, and a call through the
// servers its run mints with lands there, while its other servers, and
// an unpinned task's host scripts, stay the deployment's on the default
// hostd. Each says which hostd for the run's audit records.
func TestAPinnedTasksHostScriptsRunOnItsHostd(t *testing.T) {
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
	sock := startTestHostd(t, "clis:\n  only-local:\n    path: %[1]s\n    paths_under: [%[2]s]\n")
	cfg := withExecutor(Config{HostdSocket: sock}, whererun.Detection{Signal: "/.dockerenv"}, nil)
	cfg.HostdHub = newHostdHub(cfg, store)
	ctl := httptest.NewServer(cfg.HostdHub)
	t.Cleanup(ctl.Close)
	startDialingHostd(t, ctl.URL, tokA, "clis:\n  only-a:\n    path: %[1]s\n    paths_under: [%[2]s]\n")
	waitFor(t, func() bool { return cfg.HostdHub.HostdStatus("ws-a").Connected })
	if err := store.SetTaskHostd(ctx, "pinned", "ws-a"); err != nil {
		t.Fatal(err)
	}

	// Every server is attached to a hostScriptCaller saying where it
	// runs: a run: host one on the hostd its options reach, as
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
		hostScripts = "https://example.com/caps/host-scripts"
		inContainer = "https://example.com/caps/in-container"
	)
	configured := map[string]protocol.ServerConfig{
		hostScripts: {Command: "grain-host-scripts", Run: protocol.RunHost},
		inContainer: {Command: "in-container"},
	}
	client := cfg.capabilityHostd()
	opts := servers.Options{Hostd: client, Attach: attach}
	base := servers.New(opts)
	base.Sync(ctx, configured)
	h := newHostdCapabilityServers(ctx, base, opts, store)
	h.update(configured)
	live := NewLiveConfig(store, nil, cfg, nil).WithCapabilityServers(base).withHostdCapabilityServers(h)
	routed, ok := capabilityServerLookup(live).(orchestrator.TaskCapabilityServers)
	if !ok {
		t.Fatal("the daemon's capability servers do not route each task's to its hostd")
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
	for _, tc := range []struct{ task, hostd, scripts string }{
		{"unpinned", model.LocalHostd, "ran on only-local"},
		{"pinned", "ws-a", "ran on only-a"},
	} {
		srvs, hostd, err := routed.ForTask(ctx, tc.task)
		if err != nil {
			t.Fatalf("%s: ForTask = %v", tc.task, err)
		}
		if hostd != tc.hostd {
			t.Errorf("%s: its capability audit would say hostd %q, want %s", tc.task, hostd, tc.hostd)
		}
		if got := call(srvs, hostScripts); got != tc.scripts {
			t.Errorf("%s: its host-script call %s, want %s", tc.task, got, tc.scripts)
		}
		if got := call(srvs, inContainer); got != "ran on the container" {
			t.Errorf("%s: its in-container server %s, want the deployment's", tc.task, got)
		}
	}

	// A server dropped from mcp_servers goes from the hostd's registry too.
	h.update(map[string]protocol.ServerConfig{inContainer: configured[inContainer]})
	srvs, _, err := routed.ForTask(ctx, "pinned")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		_, err := srvs.(hostdRoutedServers).host.Lookup(hostScripts)
		return err != nil
	})
}
