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
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/runhost"
)

// serveTestHostd starts an in-process grain hostd whose allowlist offers
// sh, and returns a client that has had its hello answered.
func serveTestHostd(t *testing.T) hostd.Client {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("hostd refuses to run as root")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no shell: %v", err)
	}
	sh, _ = filepath.EvalSymlinks(sh)
	dir, err := os.MkdirTemp("", "hostd-runs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	allow := filepath.Join(dir, "hostd.yaml")
	if err := os.WriteFile(allow, []byte(fmt.Sprintf("clis:\n  sh:\n    path: %s\n", sh)), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := &hostd.Server{
		Socket:          filepath.Join(dir, "run", "hostd.sock"),
		Allowlist:       allow,
		AllowlistOwners: []uint32{uint32(os.Getuid())},
		StateDir:        filepath.Join(dir, "state"),
		DisconnectGrace: 500 * time.Millisecond,
		Env:             []string{"PATH=" + os.Getenv("PATH")},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	client := hostd.Client{Socket: srv.Socket, Build: "test"}
	for i := 0; ; i++ {
		if _, err := client.Hello(ctx); err == nil {
			return client
		} else if i > 100 {
			t.Fatalf("hostd never answered: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A run routed to a hostd other than the default starts detached through
// that hostd's own runhost.Hostd, and the next daemon's adoption pass
// re-adopts it from the hostd it recorded (task_run_hostd) -- not from
// the default hostd, whose pass would otherwise have found its process
// gone and finished it -- while a run with no recorded hostd is still
// adopted from the default.
func TestRunOnANonDefaultHostdIsReadoptedFromThatHostd(t *testing.T) {
	t.Parallel()
	defaultClient := serveTestHostd(t)
	pinnedClient := serveTestHostd(t)

	ctx := context.Background()
	store, db, err := OpenStore("sqlite", "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// Each daemon's set: the default hostd at its socket, and box-b
	// reached by name, as a dialed-in hostd is.
	newSet := func() *hostdRunSet {
		set := &hostdRunSet{
			defaultName: model.LocalHostd,
			byName:      map[string]*runhost.Hostd{},
			newHostd: func(exec HostExecutor) *runhost.Hostd {
				return &runhost.Hostd{Client: exec.hostdClient(), CursorEvery: time.Millisecond,
					OnCursor: func(ctx context.Context, run string, n int64) {
						_ = store.SetHostdCursor(ctx, run, n, time.Now().UTC())
					}}
			},
			executor: func(name string) (HostExecutor, bool) {
				return HostExecutor{Kind: HostExecutorHostd, Socket: pinnedClient.Socket}, name == "box-b"
			},
		}
		set.byName[model.LocalHostd] = set.newHostd(HostExecutor{Kind: HostExecutorHostd, Socket: defaultClient.Socket})
		return set
	}

	// The previous daemon: plain-1 on the default hostd, pinned-1 routed
	// to box-b (hostdRoute.apply) and recorded there.
	previous := newSet()
	route := hostdRoute{Name: "box-b", Pinned: true,
		Exec: HostExecutor{Kind: HostExecutorHostd, Socket: pinnedClient.Socket}}
	live := route.apply(Config{hostdRunSet: previous, hostdRuns: previous.defaultRuns()}, "")
	if live.hostdRuns == nil || live.hostdRuns == previous.defaultRuns() || live.hostdRuns.Client.Socket != pinnedClient.Socket {
		t.Fatalf("a run routed to box-b drives its CLI through %+v, want box-b's own runhost.Hostd", live.hostdRuns)
	}
	now := time.Now().UTC()
	runs := map[string]*runhost.Hostd{"plain": previous.defaultRuns(), "pinned": live.hostdRuns}
	var detached []chan error
	for id, hd := range runs {
		if err := store.PutTask(ctx, model.Task{ID: id, Title: id}); err != nil {
			t.Fatal(err)
		}
		runID := id + "-1"
		if err := store.StartRun(ctx, model.Run{ID: runID, TaskID: id, Sandbox: runID, Attempt: 1, StartedAt: now.Add(-time.Minute)}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		if id == "pinned" {
			if err := store.RecordTaskRunHostd(ctx, model.TaskRunHostd{RunID: runID, TaskID: id, Hostd: "box-b"}); err != nil {
				t.Fatal(err)
			}
		}
		var tee lockedBuffer
		runCtx := runhost.WithRun(ctx, runhost.Run{Task: id, Run: runID, Deadline: now.Add(time.Hour)})
		ch := make(chan error, 1)
		detached = append(detached, ch)
		script := "echo ready; sleep 3; echo done-" + id
		go func(hd *runhost.Hostd) {
			_, err := hd.Host(agent.LocalHost{}, "sh").Run(runCtx, agent.Command{Args: []string{"-c", script}}, &tee)
			ch <- err
		}(hd)
		for deadline := time.Now().Add(5 * time.Second); !strings.Contains(tee.String(), "ready\n"); {
			if time.Now().After(deadline) {
				t.Fatalf("%s never printed ready: %q", runID, tee.String())
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	// An upgrade flushes every hostd's runs, not only the default's.
	flushed, err := previous.Flush(ctx)
	if err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if strings.Join(flushed, ",") != "pinned-1,plain-1" {
		t.Fatalf("Flush let go of %v, want both hostds' runs", flushed)
	}
	for _, ch := range detached {
		if err := <-ch; !errors.Is(err, agent.ErrDetached) {
			t.Fatalf("detached run err = %v, want ErrDetached", err)
		}
	}

	sandboxes := orchestrator.NewHostSandboxes(t.TempDir())
	for _, sb := range []string{"plain-1", "pinned-1"} {
		if _, err := sandboxes.Acquire(ctx, sb, orchestrator.Shape{}); err != nil {
			t.Fatal(err)
		}
	}

	// The next daemon has met box-b only by the name pinned-1 recorded.
	next := newSet()
	pass, report, err := adoptAll(ctx, store, nil, nil, next, sandboxes)
	if err != nil {
		t.Fatalf("adoptAll: %v", err)
	}
	if report == nil || len(report.Adopted) != 2 || len(report.Lost) != 0 {
		t.Fatalf("report = %+v, want both runs adopted and none lost", report)
	}
	byHostd := map[string]hostdAdopted{}
	for _, h := range pass.hostd {
		byHostd[h.name] = h
	}
	for name, want := range map[string]struct {
		run    string
		socket string
	}{model.LocalHostd: {"plain-1", defaultClient.Socket}, "box-b": {"pinned-1", pinnedClient.Socket}} {
		h, ok := byHostd[name]
		if !ok || len(h.runs) != 1 || h.runs[0].Adoption.Claim.Run != want.run {
			t.Fatalf("hostd %s adopted %+v, want %s", name, h.runs, want.run)
		}
		if h.host.Client.Socket != want.socket {
			t.Fatalf("hostd %s's runs resume through the hostd at %s, want %s", name, h.host.Client.Socket, want.socket)
		}
	}

	// Re-attached through box-b's runhost.Hostd, pinned-1 runs to its end
	// on box-b.
	a := byHostd["box-b"].runs[0]
	attachCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := byHostd["box-b"].host.Attach(attachCtx, a.Adoption.Host, a.Cursor, &lockedBuffer{})
	if err != nil || !strings.Contains(out, "done-pinned") {
		t.Fatalf("re-attaching pinned-1 on box-b = (%q, %v), want it run to its end there", out, err)
	}
}

// A hostd that is away for the adoption pass leaves its runs to
// RecoverOrphanedRunsExcept, and the pass names them (adoptionPass.away);
// cancelAbandoned, started once those runs are finished, cancels their
// processes once the hostd answers -- even one back before the recovery.
func TestAbandonedHostdRunsAreCancelledOnceRecoveredAndHostdIsBack(t *testing.T) {
	t.Parallel()
	client := serveTestHostd(t)

	ctx := context.Background()
	store, db, err := OpenStore("sqlite", "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	previous := &runhost.Hostd{Client: client, CursorEvery: time.Millisecond,
		OnCursor: func(ctx context.Context, run string, n int64) {
			_ = store.SetHostdCursor(ctx, run, n, time.Now().UTC())
		}}
	now := time.Now().UTC()
	if err := store.PutTask(ctx, model.Task{ID: "away", Title: "away"}); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{ID: "away-1", TaskID: "away", Sandbox: "away-1", Attempt: 1, StartedAt: now.Add(-time.Minute)}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	var tee lockedBuffer
	runCtx := runhost.WithRun(ctx, runhost.Run{Task: "away", Run: "away-1", Deadline: now.Add(time.Hour)})
	detached := make(chan error, 1)
	go func() {
		_, err := previous.Host(agent.LocalHost{}, "sh").Run(runCtx, agent.Command{Args: []string{"-c", "echo ready; sleep 30"}}, &tee)
		detached <- err
	}()
	for deadline := time.Now().Add(5 * time.Second); !strings.Contains(tee.String(), "ready\n"); {
		if time.Now().After(deadline) {
			t.Fatalf("away-1 never printed ready: %q", tee.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := previous.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if err := <-detached; !errors.Is(err, agent.ErrDetached) {
		t.Fatalf("detached run err = %v, want ErrDetached", err)
	}

	// The next daemon reaches the hostd through a socket path that is not
	// there yet: the hostd is mid-restart.
	link := filepath.Join(t.TempDir(), "hostd.sock")
	away := hostd.Client{Socket: link, Build: "test"}
	next := testHostdRunSet(model.LocalHostd, map[string]*runhost.Hostd{model.LocalHostd: {Client: away}})
	// Bounded well under hostd.Client's own dial retry, so the test does
	// not wait that out.
	actx, cancelAdopt := context.WithTimeout(ctx, time.Second)
	pass, _, err := adoptAll(actx, store, nil, nil, next, orchestrator.NewHostSandboxes(t.TempDir()))
	cancelAdopt()
	var awayErr *orchestrator.HostdAwayError
	if !errors.As(err, &awayErr) {
		t.Fatalf("adoptAll err = %v, want a HostdAwayError", err)
	}
	if len(pass.took) != 0 || len(pass.away) != 1 || strings.Join(pass.away[0].abandoned, ",") != "away-1" {
		t.Fatalf("pass = %+v, want away-1 left, and named, for the hostd that did not answer", pass)
	}
	// Back before its runs are recovered: had anything started cancelling
	// already, it would find away-1 still live and spare it for good.
	if err := os.Symlink(client.Socket, link); err != nil {
		t.Fatal(err)
	}
	if err := orchestrator.RecoverOrphanedRunsExcept(ctx, store, nil, time.Now().UTC(), pass.took); err != nil {
		t.Fatal(err)
	}
	cctx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	pass.cancelAbandoned(cctx, store)
	for deadline := time.Now().Add(10 * time.Second); ; {
		procs, err := client.Processes(ctx)
		if err != nil {
			t.Fatal(err)
		}
		left := false
		for _, p := range procs {
			left = left || p.Run == "away-1"
		}
		if !left {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("away-1's process is still in hostd: %+v", procs)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
