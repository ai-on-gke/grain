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

package runhost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
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
)

// serveHostd starts a grain hostd whose allowlist offers sh as a cli.
func serveHostd(t *testing.T) hostd.Client {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("hostd refuses to run as root")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no shell: %v", err)
	}
	sh, _ = filepath.EvalSymlinks(sh)
	dir, err := os.MkdirTemp("", "hostd")
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
		Logf:            t.Logf,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	c := hostd.Client{Socket: srv.Socket, Build: "test"}
	for i := 0; ; i++ {
		if _, err := c.Hello(context.Background()); err == nil {
			return c
		} else if i > 100 {
			t.Fatalf("hostd never answered: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// cursors records what OnCursor was told, per run.
type cursors struct {
	mu   sync.Mutex
	last map[string]int64
}

func (c *cursors) on(_ context.Context, run string, n int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.last == nil {
		c.last = map[string]int64{}
	}
	c.last[run] = n
}

func (c *cursors) get(run string) (int64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n, ok := c.last[run]
	return n, ok
}

// lockedBuf is a bytes.Buffer safe to read while it is written.
type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func steadyLines(n int) (script, want string) {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "line%d\n", i)
	}
	return fmt.Sprintf(`i=0; while [ $i -lt %d ]; do echo line$i; i=$((i+1)); sleep 0.05; done`, n), b.String()
}

// A run's host CLI starts detached with the run's id, task and deadline;
// an upgrade's Flush lets go of it with its cursor persisted; and the
// next daemon's Attach from that cursor finishes the transcript with
// nothing lost and nothing repeated, and returns the whole stdout.
func TestHostdRunIsFlushedAndReattachedFromItsCursor(t *testing.T) {
	client := serveHostd(t)
	var first cursors
	d := &Hostd{Client: client, OnCursor: first.on, CursorEvery: time.Millisecond}
	script, want := steadyLines(40)
	budget := time.Now().Add(time.Hour)
	ctx := WithRun(context.Background(), Run{Task: "42", Run: "run-1", Deadline: budget})
	var tee lockedBuf
	done := make(chan error, 1)
	go func() {
		_, err := d.Host(agent.LocalHost{}, "sh").Run(ctx, agent.Command{Args: []string{"-c", script}}, &tee)
		done <- err
	}()
	for !strings.Contains(tee.String(), "line5\n") {
		time.Sleep(10 * time.Millisecond)
	}
	runs, err := d.Flush(context.Background())
	if err != nil || strings.Join(runs, ",") != "run-1" {
		t.Fatalf("Flush = %v, %v", runs, err)
	}
	if err := <-done; !errors.Is(err, agent.ErrDetached) {
		t.Fatalf("Run after the flush = %v, want ErrDetached", err)
	}
	cursor, ok := first.get("run-1")
	if !ok || cursor != int64(len(tee.String())) {
		t.Fatalf("persisted cursor %d (%v), want what was teed: %d", cursor, ok, len(tee.String()))
	}

	procs, err := client.Processes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	infos := HostdInfos(procs, hostd.ThisHello("test"))
	if len(infos) != 1 || infos[0].Labels.Run != "run-1" || infos[0].Labels.Task != "42" || !infos[0].Running {
		t.Fatalf("listing after the flush = %+v", infos)
	}
	if got, want := procs[0].Deadline, budget.Add(DefaultAllowance); got.Sub(want).Abs() > time.Second {
		t.Fatalf("process deadline %v, want the budget plus the allowance %v", got, want)
	}

	var second cursors
	next := &Hostd{Client: client, OnCursor: second.on}
	var rest bytes.Buffer
	whole, err := next.Attach(context.Background(), infos[0], cursor, &rest)
	if err != nil {
		t.Fatal(err)
	}
	if tee.String()+rest.String() != want {
		t.Fatalf("transcript across the restart =\n%q\nwant\n%q", tee.String()+rest.String(), want)
	}
	if whole != want {
		t.Fatalf("whole stdout = %q", whole)
	}
	if n, _ := second.get("run-1"); n != int64(len(want)) {
		t.Fatalf("final cursor %d, want %d", n, len(want))
	}
}

// A draining hostd's refusal reaches the framework as ErrHostdDraining,
// and a call that is not a run is a plain start.
func TestHostdDrainingRefusalAndPlainStarts(t *testing.T) {
	client := serveHostd(t)
	d := &Hostd{Client: client}
	host := d.Host(agent.LocalHost{}, "sh")
	out, err := host.Run(context.Background(), agent.Command{Args: []string{"-c", "echo probe"}}, nil)
	if err != nil || out != "probe\n" {
		t.Fatalf("plain start = %q, %v", out, err)
	}
	// A process still running holds the drain open: with none, hostd
	// would exit at once and the start below would find no socket.
	holding := make(chan hostd.Started, 1)
	holder := client
	holder.Detach = func(context.Context) bool { return true }
	holder.OnStarted = func(st hostd.Started) { holding <- st }
	hctx, stopHolder := context.WithCancel(context.Background())
	go func() {
		_, _ = holder.Run(hctx, hostd.Request{Kind: hostd.KindCLI, Name: "sh", Args: []string{"-c", "sleep 30"},
			Detach: true, Run: "holder", Deadline: time.Now().Add(time.Minute)}, nil, nil, nil)
	}()
	held := <-holding
	t.Cleanup(func() {
		stopHolder()
		_ = client.Cancel(context.Background(), held.ID)
	})
	if err := client.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx := WithRun(context.Background(), Run{Task: "42", Run: "run-2"})
	_, err = host.Run(ctx, agent.Command{Args: []string{"-c", "echo no"}}, nil)
	if !errors.Is(err, agent.ErrHostdDraining) {
		t.Fatalf("start on a draining hostd = %v, want ErrHostdDraining", err)
	}
}

// A lone --version inside a run is a probe, not the run: it is a plain
// start, never detached under the run's id, so it leaves no process for
// a later daemon to adopt and takes no cursor.
func TestHostdVersionProbeInARunIsAPlainStart(t *testing.T) {
	client := serveHostd(t)
	var seen cursors
	d := &Hostd{Client: client, OnCursor: seen.on}
	host := d.Host(agent.LocalHost{}, "sh")
	ctx := WithRun(context.Background(), Run{Task: "42", Run: "run-3", Deadline: time.Now().Add(time.Minute)})
	// sh's answer to --version varies by shell; only how it was started
	// matters here.
	_, _ = host.Run(ctx, agent.Command{Args: []string{"--version"}}, nil)
	if _, ok := seen.get("run-3"); ok {
		t.Fatal("a --version probe was started detached under the run's id")
	}
	if out, err := host.Run(ctx, agent.Command{Args: []string{"-c", "echo run"}}, nil); err != nil || out != "run\n" {
		t.Fatalf("run start = %q, %v", out, err)
	}
	if _, ok := seen.get("run-3"); !ok {
		t.Fatal("the run itself was not started detached")
	}
}

// flakyLink is a connection to hostd that a test can cut, as a hostd
// that dials in loses its session: every open connection closes at once,
// and while it is down a dial finds no hostd connected.
type flakyLink struct {
	socket string
	down   atomic.Bool
	mu     sync.Mutex
	conns  []net.Conn
}

func (f *flakyLink) dial(ctx context.Context) (net.Conn, error) {
	if f.down.Load() {
		return nil, hostd.ErrRemoteNotConnected
	}
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", f.socket)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.conns = append(f.conns, c)
	f.mu.Unlock()
	return c, nil
}

// cut drops every open connection and leaves the link down.
func (f *flakyLink) cut() {
	f.down.Store(true)
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.conns {
		_ = c.Close()
	}
	f.conns = nil
}

func flaky(t *testing.T, client hostd.Client) (*flakyLink, hostd.Client) {
	t.Helper()
	link := &flakyLink{socket: client.Socket}
	client.Dial = link.dial
	return link, client
}

// A run's CLI on a hostd that drops is re-attached when hostd is back
// within the reattach timeout, from where its stream stood: the run
// finishes with its whole transcript, nothing lost and nothing repeated.
func TestHostdRunIsReattachedWhenHostdReconnectsWithinTheTimeout(t *testing.T) {
	link, client := flaky(t, serveHostd(t))
	var seen cursors
	back := make(chan struct{})
	d := &Hostd{Client: client, OnCursor: seen.on, CursorEvery: time.Millisecond, ReattachTimeout: time.Minute,
		Reconnected: func(ctx context.Context) error {
			select {
			case <-back:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}}
	script, want := steadyLines(30)
	ctx := WithRun(context.Background(), Run{Task: "42", Run: "run-r", Deadline: time.Now().Add(time.Hour)})
	var tee lockedBuf
	type result struct {
		out string
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := d.Host(agent.LocalHost{}, "sh").Run(ctx, agent.Command{Args: []string{"-c", script}}, &tee)
		done <- result{out, err}
	}()
	for !strings.Contains(tee.String(), "line5\n") {
		time.Sleep(10 * time.Millisecond)
	}
	link.cut()
	time.Sleep(300 * time.Millisecond)
	select {
	case r := <-done:
		t.Fatalf("Run returned while hostd was away: %q, %v", r.out, r.err)
	default:
	}
	link.down.Store(false)
	close(back)
	r := <-done
	if r.err != nil {
		t.Fatalf("Run after hostd came back = %v", r.err)
	}
	if tee.String() != want || r.out != want {
		t.Fatalf("transcript across the drop =\n%q\nreturned\n%q\nwant\n%q", tee.String(), r.out, want)
	}
	if n, _ := seen.get("run-r"); n != int64(len(want)) {
		t.Fatalf("final cursor %d, want %d", n, len(want))
	}
}

// A hostd that is not back within the reattach timeout fails the run as
// agent.ErrHostdLost, and the CLI it left running is cancelled once hostd
// answers again.
func TestHostdRunFailsAsLostPastTheReattachTimeout(t *testing.T) {
	plain := serveHostd(t)
	link, client := flaky(t, plain)
	d := &Hostd{Client: client, ReattachTimeout: 300 * time.Millisecond}
	ctx := WithRun(context.Background(), Run{Task: "42", Run: "run-l", Deadline: time.Now().Add(time.Hour)})
	var tee lockedBuf
	done := make(chan error, 1)
	go func() {
		_, err := d.Host(agent.LocalHost{}, "sh").Run(ctx, agent.Command{Args: []string{"-c", "echo up; sleep 60"}}, &tee)
		done <- err
	}()
	for !strings.Contains(tee.String(), "up\n") {
		time.Sleep(10 * time.Millisecond)
	}
	// Before the cut, not after: cut closes the connection before it
	// returns, and the stream can see the drop and start its 300ms clock
	// before this goroutine reads the time again -- which measured a wait
	// a hair under the timeout it was waiting out.
	start := time.Now()
	link.cut()
	err := <-done
	if !errors.Is(err, agent.ErrHostdLost) {
		t.Fatalf("Run with hostd gone past the timeout = %v, want ErrHostdLost", err)
	}
	if waited := time.Since(start); waited < 300*time.Millisecond || waited > 10*time.Second {
		t.Fatalf("gave up after %s, want about the 300ms timeout", waited)
	}
	link.down.Store(false)
	for i := 0; ; i++ {
		procs, err := plain.Processes(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		running := false
		for _, p := range procs {
			running = running || (p.Run == "run-l" && !p.Exited)
		}
		if !running {
			break
		}
		if i > 200 {
			t.Fatalf("the lost run's CLI is still running in hostd: %+v", procs)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Across a hostd handover (StartHandover -> replacement Server on the same
// StateDir and Socket), a live HostdHost.Run stream sees ErrNoExit, re-attaches
// on the replacement hostd from its cursor, and finishes with no lost or
// repeated lines.
func TestHostdRunContinuesAcrossHostdHandoverWithZeroLoss(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("hostd refuses to run as root")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no shell: %v", err)
	}
	sh, _ = filepath.EvalSymlinks(sh)
	dir, err := os.MkdirTemp("", "hostd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	allow := filepath.Join(dir, "hostd.yaml")
	if err := os.WriteFile(allow, []byte(fmt.Sprintf("clis:\n  sh:\n    path: %s\n", sh)), 0o644); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "run", "hostd.sock")
	state := filepath.Join(dir, "state")
	makeSrv := func(build string) *hostd.Server {
		return &hostd.Server{
			Socket:          sock,
			Allowlist:       allow,
			AllowlistOwners: []uint32{uint32(os.Getuid())},
			StateDir:        state,
			DisconnectGrace: 500 * time.Millisecond,
			Env:             []string{"PATH=" + os.Getenv("PATH")},
			Logf:            t.Logf,
			Build:           build,
			AllowHandover:   true,
		}
	}
	srv1 := makeSrv("build-old")
	ctx1, cancel1 := context.WithCancel(context.Background())
	done1 := make(chan error, 1)
	go func() { done1 <- srv1.Serve(ctx1) }()
	t.Cleanup(func() { cancel1(); <-done1 })
	client := hostd.Client{Socket: sock, Build: "test"}
	for i := 0; ; i++ {
		if _, err := client.Hello(context.Background()); err == nil {
			break
		} else if i > 100 {
			t.Fatalf("hostd build-old never answered: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	var seen cursors
	d := &Hostd{Client: client, OnCursor: seen.on, CursorEvery: time.Millisecond, ReattachTimeout: time.Minute}
	script, want := steadyLines(30)
	ctx := WithRun(context.Background(), Run{Task: "42", Run: "run-ho", Deadline: time.Now().Add(time.Hour)})
	var tee lockedBuf
	type result struct {
		out string
		err error
	}
	runDone := make(chan result, 1)
	go func() {
		out, err := d.Host(agent.LocalHost{}, "sh").Run(ctx, agent.Command{Args: []string{"-c", script}}, &tee)
		runDone <- result{out, err}
	}()
	for !strings.Contains(tee.String(), "line5\n") {
		time.Sleep(10 * time.Millisecond)
	}

	if err := client.Handover(context.Background()); err != nil {
		t.Fatalf("Handover: %v", err)
	}
	if err := <-done1; err != nil {
		t.Fatalf("srv1 Serve: %v", err)
	}
	done1 <- nil

	srv2 := makeSrv("build-new")
	ctx2, cancel2 := context.WithCancel(context.Background())
	done2 := make(chan error, 1)
	go func() { done2 <- srv2.Serve(ctx2) }()
	t.Cleanup(func() { cancel2(); <-done2 })

	r := <-runDone
	if r.err != nil {
		t.Fatalf("Run across hostd handover = %v", r.err)
	}
	if tee.String() != want || r.out != want {
		t.Fatalf("transcript across handover =\n%q\nreturned\n%q\nwant\n%q", tee.String(), r.out, want)
	}
}

// When an explicit Drain hits drain_cap and cancels a running process, both
// HostdHost.Run (live run) and Hostd.Attach (adopted run) surface
// agent.ErrHostdDraining so the orchestrator records interrupted rather than failed.
func TestHostdDrainCapKillSurfacesErrHostdDrainingOnRunAndAttach(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("hostd refuses to run as root")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no shell: %v", err)
	}
	sh, _ = filepath.EvalSymlinks(sh)

	startCapped := func(t *testing.T) hostd.Client {
		t.Helper()
		dir, err := os.MkdirTemp("", "hostd")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(dir) })
		allow := filepath.Join(dir, "hostd.yaml")
		if err := os.WriteFile(allow, []byte(fmt.Sprintf("drain_cap: 300ms\nclis:\n  sh:\n    path: %s\n", sh)), 0o644); err != nil {
			t.Fatal(err)
		}
		srv := &hostd.Server{
			Socket:          filepath.Join(dir, "run", "hostd.sock"),
			Allowlist:       allow,
			AllowlistOwners: []uint32{uint32(os.Getuid())},
			StateDir:        filepath.Join(dir, "state"),
			DisconnectGrace: 500 * time.Millisecond,
			Env:             []string{"PATH=" + os.Getenv("PATH")},
			Logf:            t.Logf,
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- srv.Serve(ctx) }()
		t.Cleanup(func() { cancel(); <-done })
		c := hostd.Client{Socket: srv.Socket, Build: "test"}
		for i := 0; ; i++ {
			if _, err := c.Hello(context.Background()); err == nil {
				return c
			} else if i > 100 {
				t.Fatalf("hostd never answered: %v", err)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	t.Run("live Run", func(t *testing.T) {
		client := startCapped(t)
		d := &Hostd{Client: client}
		ctx := WithRun(context.Background(), Run{Task: "42", Run: "run-live", Deadline: time.Now().Add(time.Hour)})
		var tee lockedBuf
		done := make(chan error, 1)
		go func() {
			_, err := d.Host(agent.LocalHost{}, "sh").Run(ctx, agent.Command{Args: []string{"-c", "echo up; sleep 60"}}, &tee)
			done <- err
		}()
		for !strings.Contains(tee.String(), "up\n") {
			time.Sleep(10 * time.Millisecond)
		}
		if err := client.Drain(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := <-done; !errors.Is(err, agent.ErrHostdDraining) {
			t.Fatalf("live Run cancelled at drain_cap = %v, want ErrHostdDraining", err)
		}
	})

	t.Run("adopted Attach", func(t *testing.T) {
		client := startCapped(t)
		first := &Hostd{Client: client}
		ctx := WithRun(context.Background(), Run{Task: "42", Run: "run-adopted", Deadline: time.Now().Add(time.Hour)})
		var tee lockedBuf
		flushed := make(chan error, 1)
		go func() {
			_, err := first.Host(agent.LocalHost{}, "sh").Run(ctx, agent.Command{Args: []string{"-c", "echo up; sleep 60"}}, &tee)
			flushed <- err
		}()
		for !strings.Contains(tee.String(), "up\n") {
			time.Sleep(10 * time.Millisecond)
		}
		if _, err := first.Flush(context.Background()); err != nil {
			t.Fatal(err)
		}
		<-flushed

		procs, err := client.Processes(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		infos := HostdInfos(procs, hostd.ThisHello("test"))
		if len(infos) != 1 {
			t.Fatalf("infos = %+v, want 1", infos)
		}
		next := &Hostd{Client: client}
		attachDone := make(chan error, 1)
		go func() {
			_, err := next.Attach(context.Background(), infos[0], int64(len(tee.String())), nil)
			attachDone <- err
		}()
		time.Sleep(50 * time.Millisecond)
		if err := client.Drain(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := <-attachDone; !errors.Is(err, agent.ErrHostdDraining) {
			t.Fatalf("adopted Attach cancelled at drain_cap = %v, want ErrHostdDraining", err)
		}
	})
}

// When hostd stops or restarts without draining (killStopping or loadSpools
// closing an unfinished spool), both HostdHost.Run and Hostd.Attach surface
// agent.ErrHostdStopped rather than agent.ErrHostdDraining, so the interrupted
// reason says the host agent stopped or restarted rather than drained.
func TestHostdStopWithoutDrainSurfacesErrHostdStoppedOnRunAndAttach(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("hostd refuses to run as root")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no shell: %v", err)
	}
	sh, _ = filepath.EvalSymlinks(sh)

	startRestartable := func(t *testing.T) (dir string, socket string, stopFirst func(), startSecond func()) {
		t.Helper()
		dir, err := os.MkdirTemp("", "hostd-stop")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(dir) })
		allow := filepath.Join(dir, "hostd.yaml")
		if err := os.WriteFile(allow, []byte(fmt.Sprintf("clis:\n  sh:\n    path: %s\n", sh)), 0o644); err != nil {
			t.Fatal(err)
		}
		socket = filepath.Join(dir, "run", "hostd.sock")
		state := filepath.Join(dir, "state")

		launch := func() func() {
			srv := &hostd.Server{
				Socket:          socket,
				Allowlist:       allow,
				AllowlistOwners: []uint32{uint32(os.Getuid())},
				StateDir:        state,
				DisconnectGrace: 500 * time.Millisecond,
				Env:             []string{"PATH=" + os.Getenv("PATH")},
				Logf:            t.Logf,
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- srv.Serve(ctx) }()
			c := hostd.Client{Socket: socket, Build: "test"}
			for i := 0; ; i++ {
				if _, err := c.Hello(context.Background()); err == nil {
					break
				} else if i > 100 {
					cancel()
					<-done
					t.Fatalf("hostd never answered: %v", err)
				}
				time.Sleep(20 * time.Millisecond)
			}
			stopped := false
			stopFn := func() {
				if stopped {
					return
				}
				stopped = true
				cancel()
				<-done
			}
			t.Cleanup(stopFn)
			return stopFn
		}
		stop1 := launch()
		return dir, socket, stop1, func() { _ = launch() }
	}

	t.Run("live Run across host restart", func(t *testing.T) {
		_, socket, stopFirst, startSecond := startRestartable(t)
		client := hostd.Client{Socket: socket, Build: "test"}
		d := &Hostd{Client: client, ReattachTimeout: 10 * time.Second}
		ctx := WithRun(context.Background(), Run{Task: "42", Run: "run-stop-live", Deadline: time.Now().Add(time.Hour)})
		var tee lockedBuf
		done := make(chan error, 1)
		go func() {
			_, err := d.Host(agent.LocalHost{}, "sh").Run(ctx, agent.Command{Args: []string{"-c", "echo up; sleep 60"}}, &tee)
			done <- err
		}()
		for !strings.Contains(tee.String(), "up\n") {
			time.Sleep(10 * time.Millisecond)
		}
		stopFirst()
		startSecond()
		err := <-done
		if !errors.Is(err, agent.ErrHostdStopped) || errors.Is(err, agent.ErrHostdDraining) {
			t.Fatalf("live Run across host restart = %v, want ErrHostdStopped (not ErrHostdDraining)", err)
		}
		if strings.Contains(err.Error(), "draining for its own upgrade") {
			t.Errorf("err = %q, must not say draining for its own upgrade", err.Error())
		}
	})

	t.Run("adopted Attach after host stop", func(t *testing.T) {
		_, socket, stopFirst, startSecond := startRestartable(t)
		client := hostd.Client{Socket: socket, Build: "test"}
		first := &Hostd{Client: client}
		ctx := WithRun(context.Background(), Run{Task: "42", Run: "run-stop-adopted", Deadline: time.Now().Add(time.Hour)})
		var tee lockedBuf
		flushed := make(chan error, 1)
		go func() {
			_, err := first.Host(agent.LocalHost{}, "sh").Run(ctx, agent.Command{Args: []string{"-c", "echo up; sleep 60"}}, &tee)
			flushed <- err
		}()
		for !strings.Contains(tee.String(), "up\n") {
			time.Sleep(10 * time.Millisecond)
		}
		if _, err := first.Flush(context.Background()); err != nil {
			t.Fatal(err)
		}
		<-flushed

		stopFirst()
		startSecond()

		procs, err := client.Processes(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		infos := HostdInfos(procs, hostd.ThisHello("test"))
		if len(infos) != 1 {
			t.Fatalf("infos = %+v, want 1", infos)
		}
		next := &Hostd{Client: client}
		_, err = next.Attach(context.Background(), infos[0], int64(len(tee.String())), nil)
		if !errors.Is(err, agent.ErrHostdStopped) || errors.Is(err, agent.ErrHostdDraining) {
			t.Fatalf("adopted Attach after host stop = %v, want ErrHostdStopped (not ErrHostdDraining)", err)
		}
		if strings.Contains(err.Error(), "draining for its own upgrade") {
			t.Errorf("err = %q, must not say draining for its own upgrade", err.Error())
		}
	})
}

// gatedLink is a hostd that cannot be reached at all until open is
// closed: every dial before then fails, as one does while hostd restarts
// onto a new build -- a drain's exit, systemd's Restart= -- or while a
// hostd that dials in has not reconnected yet.
type gatedLink struct {
	socket string
	open   chan struct{}
	failed chan struct{} // closed at the first dial that failed
	once   sync.Once
}

func (g *gatedLink) dial(ctx context.Context) (net.Conn, error) {
	select {
	case <-g.open:
	default:
		g.once.Do(func() { close(g.failed) })
		return nil, errors.New("gated: the host agent is restarting")
	}
	var d net.Dialer
	return d.DialContext(ctx, "unix", g.socket)
}

// An adopted run whose hostd cannot be reached when the next daemon first
// attaches to it -- hostd is restarting under it, onto the build a drain
// or cutover is installing -- waits for hostd within the reattach timeout,
// as a stream that drops does, rather than failing the run at once: the
// process it adopts is still running there, or its exit is in its spool.
func TestAnAdoptedHostdRunWaitsForAHostdThatIsNotBackYet(t *testing.T) {
	client := serveHostd(t)
	first := &Hostd{Client: client, CursorEvery: time.Millisecond}
	ctx := WithRun(context.Background(), Run{Task: "42", Run: "run-g", Deadline: time.Now().Add(time.Hour)})
	var tee lockedBuf
	done := make(chan error, 1)
	go func() {
		_, err := first.Host(agent.LocalHost{}, "sh").Run(ctx, agent.Command{Args: []string{"-c", "echo up; sleep 0.2; echo down"}}, &tee)
		done <- err
	}()
	for !strings.Contains(tee.String(), "up\n") {
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := first.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, agent.ErrDetached) {
		t.Fatalf("Run after the flush = %v, want ErrDetached", err)
	}
	procs, err := client.Processes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	infos := HostdInfos(procs, hostd.ThisHello("test"))
	if len(infos) != 1 {
		t.Fatalf("listing after the flush = %+v", infos)
	}

	gate := &gatedLink{socket: client.Socket, open: make(chan struct{}), failed: make(chan struct{})}
	gated := client
	gated.Dial = gate.dial
	waiting := make(chan struct{})
	var waitOnce sync.Once
	next := &Hostd{Client: gated, ReattachTimeout: time.Minute,
		Reconnected: func(ctx context.Context) error {
			waitOnce.Do(func() { close(waiting) })
			select {
			case <-gate.open:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}}
	type result struct {
		out string
		err error
	}
	attached := make(chan result, 1)
	go func() {
		out, err := next.Attach(context.Background(), infos[0], int64(len("up\n")), nil)
		attached <- result{out, err}
	}()
	<-gate.failed
	select {
	case r := <-attached:
		t.Fatalf("Attach with hostd not back yet = %q, %v; want it to wait for hostd", r.out, r.err)
	case <-waiting:
	}
	close(gate.open)
	r := <-attached
	if r.err != nil || r.out != "up\ndown\n" {
		t.Fatalf("Attach once hostd was back = %q, %v; want the run's whole stdout", r.out, r.err)
	}
}
