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
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/runhost"
	"github.com/ai-on-gke/grain/pkg/whererun"
)

// flakyHostd is hostd ws-a dialing in to cfg's hub through a server the
// test can cut it off at: drop closes its session, and while away is
// set it cannot dial in again.
type flakyHostd struct {
	away    atomic.Bool
	mu      sync.Mutex
	session []net.Conn
}

func (f *flakyHostd) drop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.session {
		_ = c.Close()
	}
	f.session = nil
}

// parkedHostdSetup is a daemon whose default hostd is a local socket and
// whose task "pinned" is pinned to ws-a, a hostd that dials in, with
// runs started detached on it and re-attached within reattach.
func parkedHostdSetup(t *testing.T, reattach time.Duration) (Config, *AgentPool, *flakyHostd) {
	t.Helper()
	ctx := context.Background()
	store, closeStore := openTestStore(t, t.TempDir())
	t.Cleanup(closeStore)
	if err := store.EnsureLocalHostd(ctx); err != nil {
		t.Fatal(err)
	}
	_, tok, err := store.CreateHostd(ctx, model.Hostd{Name: "ws-a"})
	if err != nil {
		t.Fatal(err)
	}
	const offersAgy = "clis:\n  agy:\n    path: %[1]s\n    paths_under: [%[2]s]\n"
	sock := startTestHostd(t, offersAgy)
	cfg := withExecutor(Config{HostdSocket: sock, AgyRun: model.CLIRunHost, HostdReattachTimeout: reattach},
		whererun.Detection{Signal: "/.dockerenv"}, nil)
	cfg.HostdHub = newHostdHub(cfg, store)
	cfg.pinnedHostdRuns = newPinnedHostdRuns(cfg, store)

	f := &flakyHostd{}
	ctl := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.away.Load() {
			http.Error(w, "held off by the test", http.StatusServiceUnavailable)
			return
		}
		cfg.HostdHub.ServeHTTP(w, r)
	}))
	ctl.Config.ConnState = func(conn net.Conn, st http.ConnState) {
		if st == http.StateHijacked {
			f.mu.Lock()
			f.session = append(f.session, conn)
			f.mu.Unlock()
		}
	}
	ctl.Start()
	t.Cleanup(ctl.Close)
	t.Cleanup(f.drop)
	startDialingHostd(t, ctl.URL, tok, offersAgy)
	waitFor(t, func() bool { return cfg.HostdHub.HostdStatus("ws-a").Connected })
	if err := store.SetTaskHostd(ctx, "pinned", "ws-a"); err != nil {
		t.Fatal(err)
	}
	return cfg, NewAgentPool(cfg, store, nil), f
}

// pinnedHost is the agent.Host the pinned task's run starts its CLI on.
func pinnedHost(t *testing.T, cfg Config, pool *AgentPool) agent.Host {
	t.Helper()
	ctx := context.Background()
	route, err := pool.routeHostd(ctx, cfg, "pinned")
	if err != nil {
		t.Fatal(err)
	}
	live := route.apply(cfg)
	r := live.ResolveCLIRun(ctx, model.AgentFrameworkAntigravity)
	if r.Via != ViaHostd || r.Err != "" {
		t.Fatalf("agy resolves to %+v, want through ws-a", r)
	}
	host, err := live.cliHostdHost(r)
	if err != nil {
		t.Fatal(err)
	}
	if host.(agent.HostdHost).Start == nil {
		t.Fatal("a run on ws-a is a plain start, want it started detached so a drop can be re-attached")
	}
	return host
}

type hostRun struct {
	out string
	err error
}

// startPinnedRun runs script on host as run r1 of the pinned task.
func startPinnedRun(host agent.Host, script string, tee *lockedTee) <-chan hostRun {
	done := make(chan hostRun, 1)
	ctx := runhost.WithRun(context.Background(), runhost.Run{Task: "pinned", Run: "r1", Deadline: time.Now().Add(time.Hour)})
	go func() {
		out, err := host.Run(ctx, agent.Command{Args: []string{"-c", script}}, tee)
		done <- hostRun{out, err}
	}()
	return done
}

type lockedTee struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedTee) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedTee) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// A task pinned to a hostd that drops is parked "waiting for hostd
// ws-a" rather than started anywhere else; the CLI its run left running
// there carries on, and when ws-a reconnects within the reattach timeout
// dispatch is woken, the task is admitted again, and the CLI is
// re-attached and finishes with its whole transcript.
func TestARunWaitsForItsDisconnectedHostdAndItsCLIIsReattached(t *testing.T) {
	cfg, pool, ws := parkedHostdSetup(t, time.Minute)
	ctx := context.Background()
	choice := orchestrator.AgentChoice{TaskID: "pinned", Framework: model.AgentFrameworkAntigravity}
	if why := pool.Availability(ctx, choice); why != "" {
		t.Fatalf("ws-a connected: availability %q, want none", why)
	}
	host := pinnedHost(t, cfg, pool)
	var b strings.Builder
	for i := range 30 {
		fmt.Fprintf(&b, "line%d\n", i)
	}
	want := b.String()
	var tee lockedTee
	done := startPinnedRun(host, `i=0; while [ $i -lt 30 ]; do echo line$i; i=$((i+1)); sleep 0.05; done`, &tee)
	waitFor(t, func() bool { return strings.Contains(tee.String(), "line3\n") })

	ws.away.Store(true)
	ws.drop()
	waitFor(t, func() bool { return !cfg.HostdHub.HostdStatus("ws-a").Connected })
	if why := pool.Availability(ctx, choice); why != "waiting for hostd ws-a" {
		t.Errorf("ws-a away: availability %q, want the task parked waiting for hostd ws-a", why)
	}
	select {
	case r := <-done:
		t.Fatalf("the run ended while ws-a was away: %q, %v", r.out, r.err)
	case <-time.After(300 * time.Millisecond):
	}
	// Drain any wake already pending, so the one below is the reconnect's.
	select {
	case <-cycleWakes:
	default:
	}

	ws.away.Store(false)
	waitFor(t, func() bool { return cfg.HostdHub.HostdStatus("ws-a").Connected })
	select {
	case <-cycleWakes:
	case <-time.After(10 * time.Second):
		t.Error("ws-a reconnecting did not wake dispatch for the work parked on it")
	}
	if why := pool.Availability(ctx, choice); why != "" {
		t.Errorf("ws-a back: availability %q, want the task admitted again", why)
	}
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("the run after ws-a came back = %v", r.err)
		}
		if r.out != want || tee.String() != want {
			t.Fatalf("transcript across the drop =\n%q\nteed\n%q\nwant\n%q", r.out, tee.String(), want)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the run's CLI was not re-attached after ws-a came back")
	}
}

// Past -hostd-reattach-timeout the run fails as agent.ErrHostdLost, which
// the orchestrator records without a strike; the task stays parked on
// ws-a meanwhile.
func TestARunFailsAsLostWhenItsHostdIsAwayPastTheReattachTimeout(t *testing.T) {
	cfg, pool, ws := parkedHostdSetup(t, 500*time.Millisecond)
	host := pinnedHost(t, cfg, pool)
	var tee lockedTee
	done := startPinnedRun(host, "echo up; sleep 60", &tee)
	waitFor(t, func() bool { return strings.Contains(tee.String(), "up\n") })

	ws.away.Store(true)
	ws.drop()
	select {
	case r := <-done:
		if !errors.Is(r.err, agent.ErrHostdLost) {
			t.Fatalf("the run with ws-a away past the timeout = %v, want ErrHostdLost", r.err)
		}
		if !strings.Contains(r.err.Error(), "grain hostd ws-a") {
			t.Errorf("error %q does not name ws-a", r.err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the run kept waiting for ws-a past the reattach timeout")
	}
	choice := orchestrator.AgentChoice{TaskID: "pinned", Framework: model.AgentFrameworkAntigravity}
	if why := pool.Availability(context.Background(), choice); why != "waiting for hostd ws-a" {
		t.Errorf("after the lost run: availability %q, want the task parked waiting for hostd ws-a", why)
	}
}

// A CLI a daemon left running detached on ws-a -- it crashed, or the run
// gave up waiting -- is cancelled when ws-a next connects once its run
// has finished; nothing else lists a pinned hostd's processes. One whose
// run is still live is left for its stream to re-attach.
func TestAHostdThatConnectsIsRidOfCLIsForFinishedRuns(t *testing.T) {
	cfg, pool, ws := parkedHostdSetup(t, time.Minute)
	ctx := context.Background()
	store := pool.Store
	client := hostd.Client{Dial: cfg.HostdHub.Dialer("ws-a"), Build: daemonBuild()}
	// The daemon that started them is gone: its streams let go, and
	// nothing re-attaches.
	gone := &runhost.Hostd{Client: client, Detach: func(context.Context) bool { return true }}
	for _, run := range []string{"finished", "live"} {
		if err := store.PutTask(ctx, model.Task{ID: run, Title: run}); err != nil {
			t.Fatal(err)
		}
		if err := store.StartRun(ctx, model.Run{ID: run, TaskID: run, Sandbox: run, Attempt: 1, StartedAt: time.Now().UTC()}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		var tee lockedTee
		runCtx, cancel := context.WithCancel(runhost.WithRun(ctx, runhost.Run{Task: run, Run: run, Deadline: time.Now().Add(time.Hour)}))
		done := make(chan error, 1)
		go func() {
			_, err := gone.Host(agent.LocalHost{}, "agy").Run(runCtx, agent.Command{Args: []string{"-c", "echo up; sleep 60"}}, &tee)
			done <- err
		}()
		waitFor(t, func() bool { return strings.Contains(tee.String(), "up\n") })
		cancel()
		if err := <-done; !errors.Is(err, agent.ErrDetached) {
			t.Fatalf("run %s's CLI let go of = %v, want it left running detached", run, err)
		}
	}
	if err := store.FinishRun(ctx, "finished", time.Now().UTC(), model.InterruptedOutcome, "the daemon stopped"); err != nil {
		t.Fatal(err)
	}
	running := func() map[string]bool {
		procs, err := client.Processes(ctx)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for _, p := range procs {
			if p.Detached && !p.Exited {
				out[p.Run] = true
			}
		}
		return out
	}
	if got := running(); !got["finished"] || !got["live"] {
		t.Fatalf("before ws-a reconnects, running = %v, want both CLIs", got)
	}

	ws.drop()
	waitFor(t, func() bool { return !running()["finished"] })
	if !running()["live"] {
		t.Error("ws-a reconnecting cancelled the CLI of a run that is still live")
	}
}
