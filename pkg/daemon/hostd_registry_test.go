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
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/whererun"
)

// The co-located hostd and two registry hostds dialing in serve one
// daemon at once. Host work goes to local unless pinned to a hostd by
// name or selected by label; a pinned hostd that is not connected is
// waited for, never swapped for another; a revoked or unknown one is
// refused outright.
func TestLocalAndRemoteHostdsServeTogetherRoutedByPinAndLabel(t *testing.T) {
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
	if _, _, err := store.CreateHostd(ctx, model.Hostd{Name: "ws-gone"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RevokeHostd(ctx, "ws-gone"); err != nil {
		t.Fatal(err)
	}

	sock := startTestHostd(t, "clis:\n  only-local:\n    path: %[1]s\n    paths_under: [%[2]s]\n")
	cfg := Config{HostdSocket: sock}
	if err := CheckHostExecutorFlags(cfg); err != nil {
		t.Fatalf("CheckHostExecutorFlags = %v", err)
	}
	cfg = withExecutor(cfg, whererun.Detection{Signal: "/.dockerenv"}, nil)
	cfg.HostdHub = newHostdHub(cfg, store)
	ctl := httptest.NewServer(cfg.HostdHub)
	t.Cleanup(ctl.Close)

	startDialingHostd(t, ctl.URL, tokA, "clis:\n  only-a:\n    path: %[1]s\n    paths_under: [%[2]s]\n")
	startDialingHostd(t, ctl.URL, tokB, "clis:\n  only-b:\n    path: %[1]s\n    paths_under: [%[2]s]\n")
	waitFor(t, func() bool {
		return cfg.HostdHub.HostdStatus("ws-a").Connected && cfg.HostdHub.HostdStatus("ws-b").Connected
	})
	if st := cfg.HostdHub.HostdStatus("ws-a"); st.Owner != "alice@example.com" || st.Host != "cloudtop" {
		t.Errorf("ws-a's connection = %+v, want its registry owner and its instance", st)
	}

	offers := func(exec HostExecutor) []string {
		t.Helper()
		got, err := exec.hostdClient().Offers(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, o := range got {
			names = append(names, o.Name)
		}
		return names
	}
	for _, tc := range []struct {
		sel        HostdSelector
		name, want string
	}{
		{HostdSelector{}, model.LocalHostd, "only-local"},
		{HostdSelector{Name: model.LocalHostd}, model.LocalHostd, "only-local"},
		{HostdSelector{Name: "ws-a"}, "ws-a", "only-a"},
		{HostdSelector{Name: "ws-b"}, "ws-b", "only-b"},
		// The registry's labels.
		{HostdSelector{Labels: map[string]string{"gpu": "yes"}}, "ws-a", "only-a"},
		{HostdSelector{Labels: map[string]string{"zone": "b"}}, "ws-b", "only-b"},
	} {
		exec, name, err := cfg.SelectHostd(ctx, store, tc.sel)
		if err != nil {
			t.Fatalf("SelectHostd(%s) = %v", tc.sel, err)
		}
		if name != tc.name {
			t.Errorf("SelectHostd(%s) chose %s, want %s", tc.sel, name, tc.name)
		}
		if got := offers(exec); !slices.Contains(got, tc.want) || len(got) != 1 {
			t.Errorf("SelectHostd(%s) reached a hostd offering %v, want only %s", tc.sel, got, tc.want)
		}
	}

	// Registered and not connected: waited for, by name.
	_, _, err = cfg.SelectHostd(ctx, store, HostdSelector{Name: "ws-off"})
	var wait *HostdWaitError
	if !errors.As(err, &wait) || err.Error() != "waiting for host ws-off" {
		t.Errorf("pinned to a disconnected hostd: %v, want waiting for hostd ws-off", err)
	}
	for _, sel := range []HostdSelector{{Name: "ws-gone"}, {Name: "nope"}, {Labels: map[string]string{"gpu": "no"}}} {
		_, _, err := cfg.SelectHostd(ctx, store, sel)
		if err == nil || errors.As(err, &wait) {
			t.Errorf("SelectHostd(%s) = %v, want a refusal that waiting will not fix", sel, err)
		}
	}
}

// A label selector matches only the labels an admin set on a hostd's
// registry row. A hostd whose own allowlist claims gpu=a100 says so in
// its hello, for display, but that draws in no host work selected by
// gpu=a100: that waits for ws-a, the hostd the registry gives the
// label, and goes to it once it connects.
func TestHostdSelfAssertedLabelsDoNotSelectIt(t *testing.T) {
	ctx := context.Background()
	store, closeStore := openTestStore(t, t.TempDir())
	t.Cleanup(closeStore)
	if err := store.EnsureLocalHostd(ctx); err != nil {
		t.Fatal(err)
	}
	_, tokA, err := store.CreateHostd(ctx, model.Hostd{Name: "ws-a", Owner: "alice@example.com", Labels: map[string]string{"gpu": "a100"}})
	if err != nil {
		t.Fatal(err)
	}
	_, tokB, err := store.CreateHostd(ctx, model.Hostd{Name: "ws-b", Owner: "bob@example.com"})
	if err != nil {
		t.Fatal(err)
	}

	sock := startTestHostd(t, "clis:\n  only-local:\n    path: %[1]s\n    paths_under: [%[2]s]\n")
	cfg := withExecutor(Config{HostdSocket: sock}, whererun.Detection{Signal: "/.dockerenv"}, nil)
	cfg.HostdHub = newHostdHub(cfg, store)
	ctl := httptest.NewServer(cfg.HostdHub)
	t.Cleanup(ctl.Close)

	startDialingHostd(t, ctl.URL, tokB, "labels:\n  gpu: a100\nclis:\n  only-b:\n    path: %[1]s\n    paths_under: [%[2]s]\n")
	waitFor(t, func() bool { return cfg.HostdHub.HostdStatus("ws-b").Connected })

	execB, _, err := cfg.SelectHostd(ctx, store, HostdSelector{Name: "ws-b"})
	if err != nil {
		t.Fatal(err)
	}
	hello, err := execB.hostdClient().Hello(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if hello.Labels["gpu"] != "a100" {
		t.Errorf("ws-b's hello labels = %v, want its allowlist's gpu=a100 shown", hello.Labels)
	}

	gpu := HostdSelector{Labels: map[string]string{"gpu": "a100"}}
	_, name, err := cfg.SelectHostd(ctx, store, gpu)
	var wait *HostdWaitError
	if !errors.As(err, &wait) || wait.Name != "ws-a" {
		t.Fatalf("SelectHostd(%s) = %q, %v; want to wait for ws-a, not ws-b's own claim", gpu, name, err)
	}
	if _, name, err := cfg.SelectHostd(ctx, store, HostdSelector{Labels: map[string]string{"gpu": "a100", "zone": "x"}}); err == nil {
		t.Errorf("a selector no registry row matches chose %s", name)
	}

	startDialingHostd(t, ctl.URL, tokA, "clis:\n  only-a:\n    path: %[1]s\n    paths_under: [%[2]s]\n")
	waitFor(t, func() bool { return cfg.HostdHub.HostdStatus("ws-a").Connected })
	if _, name, err := cfg.SelectHostd(ctx, store, gpu); err != nil || name != "ws-a" {
		t.Errorf("SelectHostd(%s) with ws-a connected = %q, %v; want ws-a", gpu, name, err)
	}
}

// A drop reported right after a connect leaves hostd-disconnected:<name>
// raised even when the connect's clear, held back behind asking the hostd
// for its finished runs, would otherwise land after the drop's raise.
func TestAConnectsLateClearDoesNotHideTheDropThatFollowedIt(t *testing.T) {
	ctx := context.Background()
	store, closeStore := openTestStore(t, t.TempDir())
	t.Cleanup(closeStore)
	if _, _, err := store.CreateHostd(ctx, model.Hostd{Name: "ws-a"}); err != nil {
		t.Fatal(err)
	}
	unblock := make(chan struct{})
	released := make(chan struct{})
	var once sync.Once
	notify := hostdConnectionNotifierReleasing(store, func(context.Context, string) {
		<-unblock
		once.Do(func() { close(released) })
	})
	open := func() bool {
		n, err := store.OpenNotification(ctx, hostdDisconnectedSubject("ws-a"))
		return err == nil && n != nil
	}

	notify("ws-a", true)
	notify("ws-a", false)
	waitFor(t, open)
	close(unblock)
	<-released
	for deadline := time.Now().Add(300 * time.Millisecond); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if !open() {
			t.Fatal("the connect's late clear hid the drop that followed it")
		}
	}

	notify("ws-a", true)
	waitFor(t, func() bool { return !open() })
}

// A disconnected hostd's pinned work waits for it, and its drop raises
// hostd-disconnected:<name>; the same hostd reconnecting is reached
// again, under the same name, and clears it.
func TestAPinnedHostdIsWaitedForAcrossADisconnect(t *testing.T) {
	ctx := context.Background()
	store, closeStore := openTestStore(t, t.TempDir())
	t.Cleanup(closeStore)
	_, tok, err := store.CreateHostd(ctx, model.Hostd{Name: "ws-a"})
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	cfg.HostdHub = newHostdHub(cfg, store)
	var mu sync.Mutex
	var changes []string
	notify := cfg.HostdHub.OnChange
	cfg.HostdHub.OnChange = func(name string, connected bool) {
		notify(name, connected)
		mu.Lock()
		defer mu.Unlock()
		changes = append(changes, name+":"+map[bool]string{true: "up", false: "down"}[connected])
	}
	open := func() bool {
		n, err := store.OpenNotification(ctx, "hostd-disconnected:ws-a")
		return err == nil && n != nil
	}
	// The sessions are hijacked connections, which the server's own
	// Close does not cut: keep them to cut by hand.
	var hijacked []net.Conn
	// away holds ws-a off while the test looks at it disconnected: the
	// test hostd redials within milliseconds, faster than the checks
	// below could otherwise see it gone.
	var away atomic.Bool
	ctl := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if away.Load() {
			http.Error(w, "held off by the test", http.StatusServiceUnavailable)
			return
		}
		cfg.HostdHub.ServeHTTP(w, r)
	}))
	ctl.Config.ConnState = func(conn net.Conn, st http.ConnState) {
		if st == http.StateHijacked {
			mu.Lock()
			hijacked = append(hijacked, conn)
			mu.Unlock()
		}
	}
	ctl.Start()
	t.Cleanup(ctl.Close)
	drop := func() {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range hijacked {
			_ = c.Close()
		}
		hijacked = nil
	}
	t.Cleanup(drop)

	startDialingHostd(t, ctl.URL, tok, offersAgy)
	waitFor(t, func() bool { return cfg.HostdHub.HostdStatus("ws-a").Connected })
	if _, _, err := cfg.SelectHostd(ctx, store, HostdSelector{Name: "ws-a"}); err != nil {
		t.Fatal(err)
	}

	away.Store(true)
	drop()
	waitFor(t, func() bool { return !cfg.HostdHub.HostdStatus("ws-a").Connected })
	_, _, err = cfg.SelectHostd(ctx, store, HostdSelector{Name: "ws-a"})
	if err == nil || !strings.Contains(err.Error(), "waiting for host ws-a") {
		t.Errorf("while ws-a is away: %v, want waiting for hostd ws-a", err)
	}
	waitFor(t, open)

	away.Store(false)
	waitFor(t, func() bool { return cfg.HostdHub.HostdStatus("ws-a").Connected })
	if _, _, err := cfg.SelectHostd(ctx, store, HostdSelector{Name: "ws-a"}); err != nil {
		t.Errorf("after ws-a reconnected: %v", err)
	}
	waitFor(t, func() bool { return !open() })
	mu.Lock()
	defer mu.Unlock()
	if len(changes) < 3 || changes[0] != "ws-a:up" || changes[1] != "ws-a:down" || changes[2] != "ws-a:up" {
		t.Errorf("changes = %v, want ws-a up, down, up", changes)
	}
}

// Revoking a connected hostd and disconnecting it raises no
// hostd-disconnected:<name> notification (and clears any open one), and
// SelectHostd by label does not wait for local when the deployment has
// no co-located hostd.
func TestRevokedHostdDisconnectDoesNotNotifyAndUnavailableLocalIsNotWaitedFor(t *testing.T) {
	ctx := context.Background()
	store, closeStore := openTestStore(t, t.TempDir())
	t.Cleanup(closeStore)
	if err := store.EnsureLocalHostd(ctx); err != nil {
		t.Fatal(err)
	}
	localLabels := map[string]string{"gpu": "yes"}
	if _, err := store.UpdateHostd(ctx, model.LocalHostd, nil, &localLabels); err != nil {
		t.Fatal(err)
	}
	_, tok, err := store.CreateHostd(ctx, model.Hostd{Name: "ws-rev"})
	if err != nil {
		t.Fatal(err)
	}

	// Kubernetes-like config with no co-located hostd: local has gpu=yes in
	// the registry, and no remote hostd does, so label selection must refuse
	// rather than return HostdWaitError("waiting for hostd local").
	cfg := withExecutor(Config{}, whererun.Detection{Signal: "/.dockerenv"}, func(string) bool { return false })
	cfg.HostdHub = newHostdHub(cfg, store)
	var waitErr *HostdWaitError
	if _, _, err := cfg.SelectHostd(ctx, store, HostdSelector{Labels: map[string]string{"gpu": "yes"}}); err == nil || errors.As(err, &waitErr) {
		t.Errorf("SelectHostd(gpu=yes) with unavailable local = %v, want immediate non-wait refusal", err)
	}

	done := make(chan struct{}, 1)
	cfg.HostdHub.OnChange = func(name string, connected bool) {
		hostdConnectionNotifier(store, nil)(name, connected)
		if name == "ws-rev" && !connected {
			select {
			case done <- struct{}{}:
			default:
			}
		}
	}
	ctl := httptest.NewServer(cfg.HostdHub)
	t.Cleanup(ctl.Close)
	startDialingHostd(t, ctl.URL, tok, offersAgy)
	waitFor(t, func() bool { return cfg.HostdHub.HostdStatus("ws-rev").Connected })

	// Seed an open notification right before revoking and disconnecting: the
	// drop handler for a revoked hostd clears it rather than raising one.
	if _, err := store.RaiseNotification(ctx, model.Notification{
		Subject:  "hostd-disconnected:ws-rev",
		Severity: model.NotificationWarning,
		Title:    "grain hostd ws-rev disconnected",
		Body:     "stale",
		RaisedBy: "automation:hostd",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RevokeHostd(ctx, "ws-rev"); err != nil {
		t.Fatal(err)
	}
	if !cfg.HostdHub.Disconnect("ws-rev") {
		t.Fatal("Disconnect(ws-rev) = false, want true")
	}
	<-done
	waitFor(t, func() bool {
		n, err := store.OpenNotification(ctx, "hostd-disconnected:ws-rev")
		return err == nil && n == nil
	})
}
