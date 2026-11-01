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
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
)

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func TestRunHostUpgradeNilAndFlushResumeLifecycle(t *testing.T) {
	t.Parallel()

	// 1. Nil receiver when neither runhost, hostd, nor CI is enabled.
	if u := newRunHostUpgrade(nil, nil, false); u != nil {
		t.Fatalf("newRunHostUpgrade(nil, nil, false) = %v, want nil", u)
	}
	var nilUpgrade *runHostUpgrade
	nilUpgrade.withStopping(make(chan struct{}))
	if nilUpgrade.flushHook() != nil || nilUpgrade.adoption() != nil || nilUpgrade.hasAdoptedCI() {
		t.Fatal("nil runHostUpgrade returned non-nil hook or report")
	}
	nilUpgrade.setReport(&orchestrator.AdoptionReport{})
	nilUpgrade.setCIAdoption([]orchestrator.AdoptedCIJobNote{{Run: "ci-1"}}, nil)
	nilUpgrade.setReadopt(func() {})

	// 2. Active runHostUpgrade preserves CI adoption across setReport and honours stopping on resume.
	stopping := make(chan struct{})
	hd := &runhost.Hostd{}
	u := newRunHostUpgrade(nil, hd, true).withStopping(stopping)
	u.setCIAdoption(
		[]orchestrator.AdoptedCIJobNote{{Run: "ci-1", Job: "go"}},
		[]orchestrator.AdoptedCIJobNote{{Run: "ci-2", Job: "lint", Reason: "gone"}},
	)
	if !u.hasAdoptedCI() {
		t.Fatal("hasAdoptedCI() = false after setCIAdoption, want true")
	}

	u.setReport(&orchestrator.AdoptionReport{
		At:      time.Now().UTC(),
		Adopted: []orchestrator.AdoptedRunNote{{Run: "r-1", Task: "t-1"}},
		Freed:   2,
	})
	rep := u.adoption()()
	if rep == nil || len(rep.Adopted) != 1 || len(rep.CIAdopted) != 1 || len(rep.CILost) != 1 || rep.Freed != 2 {
		t.Fatalf("merged report = %+v, want Adopted=1 CIAdopted=1 CILost=1 Freed=2", rep)
	}

	var readopted atomic.Int32
	u.setReadopt(func() { readopted.Add(1) })
	resumeFn, err := u.flushHook()(context.Background())
	if err != nil {
		t.Fatalf("flush: %v", err)
	}
	resumeFn()
	if got := readopted.Load(); got != 1 {
		t.Fatalf("readopted count after active resume = %d, want 1", got)
	}

	// Once stopping is closed, a late resume must be a no-op so it never adopts back into a draining daemon.
	close(stopping)
	resumeFn()
	if got := readopted.Load(); got != 1 {
		t.Fatalf("readopted count after stopping closed = %d, want 1 (no-op)", got)
	}
}

func TestAdoptAllWithHostdClaimsLiveSandboxAndMarksMissingSandboxLost(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("hostd refuses to run as root")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no shell: %v", err)
	}
	sh, _ = filepath.EvalSymlinks(sh)

	ctx := context.Background()
	store, db, err := OpenStore("sqlite", "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// Empty adoptAll with no host and no hostd returns immediately.
	if pass, rep, err := adoptAll(ctx, store, nil, nil, nil, nil); err != nil || rep != nil || len(pass.took) != 0 {
		t.Fatalf("adoptAll(nil, nil) = (%+v, %+v, %v), want empty", pass, rep, err)
	}

	// Start a real in-process hostd server with two detached sleep processes:
	// - kept-1: has a matching host sandbox on disk -> adopted
	// - missing-sb-1: has a live process in hostd, but its sandbox directory is gone -> rejected by ReadoptSandbox and marked Lost
	dir, err := os.MkdirTemp("", "hostd-adopt")
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
	srvCtx, cancelSrv := context.WithCancel(ctx)
	srvDone := make(chan error, 1)
	go func() { srvDone <- srv.Serve(srvCtx) }()
	t.Cleanup(func() { cancelSrv(); <-srvDone })

	client := hostd.Client{Socket: srv.Socket, Build: "test"}
	for i := 0; ; i++ {
		if _, err := client.Hello(ctx); err == nil {
			break
		} else if i > 100 {
			t.Fatalf("hostd never answered: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	previous := &runhost.Hostd{
		Client:      client,
		CursorEvery: time.Millisecond,
		OnCursor: func(ctx context.Context, run string, n int64) {
			_ = store.SetHostdCursor(ctx, run, n, time.Now().UTC())
		},
	}

	now := time.Now().UTC()
	var detachedChans []chan error
	for _, id := range []string{"kept", "missing-sb"} {
		if err := store.PutTask(ctx, model.Task{ID: id, Title: id}); err != nil {
			t.Fatal(err)
		}
		runID := id + "-1"
		if err := store.StartRun(ctx, model.Run{ID: runID, TaskID: id, Sandbox: runID, Attempt: 1, StartedAt: now.Add(-time.Minute)}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		var tee lockedBuffer
		runCtx := runhost.WithRun(ctx, runhost.Run{Task: id, Run: runID, Deadline: now.Add(time.Hour)})
		ch := make(chan error, 1)
		detachedChans = append(detachedChans, ch)
		go func() {
			_, err := previous.Host(agent.LocalHost{}, "sh").Run(runCtx, agent.Command{Args: []string{"-c", "echo ready; sleep 30"}}, &tee)
			ch <- err
		}()
		for deadline := time.Now().Add(5 * time.Second); !strings.Contains(tee.String(), "ready\n"); {
			if time.Now().After(deadline) {
				t.Fatalf("%s never printed ready: %q", runID, tee.String())
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	if _, err := previous.Flush(ctx); err != nil {
		t.Fatalf("previous.Flush: %v", err)
	}
	for _, ch := range detachedChans {
		if err := <-ch; !errors.Is(err, agent.ErrDetached) {
			t.Fatalf("detached run err = %v, want ErrDetached", err)
		}
	}

	sandboxDir := t.TempDir()
	sandboxes := orchestrator.NewHostSandboxes(sandboxDir)
	// Create the sandbox directory only for kept-1; leave missing-sb-1 without a sandbox on disk.
	if _, err := sandboxes.Acquire(ctx, "kept-1", orchestrator.Shape{}); err != nil {
		t.Fatal(err)
	}

	next := &runhost.Hostd{Client: client}
	pass, report, err := adoptAll(ctx, store, nil, nil, next, sandboxes)
	if err != nil {
		t.Fatalf("adoptAll: %v", err)
	}
	if len(pass.hostd) != 1 || pass.hostd[0].Adoption.Claim.Run != "kept-1" || !pass.took["kept-1"] {
		t.Fatalf("pass = %+v, want kept-1 adopted", pass)
	}
	if pass.took["missing-sb-1"] {
		t.Fatalf("missing-sb-1 should not be adopted when its sandbox is missing")
	}
	if report == nil || len(report.Adopted) != 1 || len(report.Lost) != 1 {
		t.Fatalf("report = %+v, want 1 Adopted (kept-1) and 1 Lost (missing-sb-1)", report)
	}
}
