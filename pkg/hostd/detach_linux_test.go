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

package hostd

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// steady prints line0..line<n-1>, one every 50 ms.
func steady(n int) string {
	return fmt.Sprintf(`i=0; while [ $i -lt %d ]; do echo line$i; i=$((i+1)); sleep 0.05; done`, n)
}

func wantLines(n int) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "line%d\n", i)
	}
	return b.String()
}

// detachedClient is ts's client with Detach always true: a cancelled ctx
// is the daemon stopping.
func detachedClient(ts *testServer, started chan<- Started) Client {
	c := ts.client
	c.Detach = func(context.Context) bool { return true }
	c.OnStarted = func(s Started) { started <- s }
	return c
}

func listed(t *testing.T, ts *testServer, id string) Process {
	t.Helper()
	ps, err := ts.client.Processes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("process %s is not listed: %+v", id, ps)
	return Process{}
}

func TestADetachedProcessOutlivesItsDaemonAndIsReattachedWithoutLossOrDuplication(t *testing.T) {
	ts := startServer(t, "", nil)
	started := make(chan Started, 1)
	c := detachedClient(ts, started)
	ctx, cancel := context.WithCancel(context.Background())
	var first bytes.Buffer
	deadline := time.Now().Add(time.Minute)
	done := make(chan error, 1)
	go func() {
		_, err := c.Run(ctx, Request{Kind: KindCLI, Name: "sh", Args: []string{"-c", steady(40)},
			Detach: true, Run: "run-1", Task: "42", Deadline: deadline}, nil, &first, nil)
		done <- err
	}()
	st := <-started
	if st.ID == "" || st.PID == 0 {
		t.Fatalf("Started = %+v, want an id and a pid", st)
	}
	time.Sleep(300 * time.Millisecond)
	cancel() // the daemon stops
	if err := <-done; !errors.Is(err, ErrDetached) {
		t.Fatalf("Run after the daemon stopped = %v, want ErrDetached", err)
	}
	got := first.String()
	// Longer than DisconnectGrace (500 ms here): a detached process is
	// held to its deadline, not the grace.
	time.Sleep(time.Second)
	if !pidAlive(st.PID) {
		t.Fatalf("pid %d died after its daemon went away", st.PID)
	}
	p := listed(t, ts, st.ID)
	if p.Run != "run-1" || p.Task != "42" || p.PID != st.PID || !p.Detached || p.Spool == "" ||
		p.Deadline.Unix() != deadline.Unix() || p.Started.IsZero() || p.Exited {
		t.Errorf("listing = %+v", p)
	}
	if p.Cursor.Stdout <= int64(len(got)) {
		t.Errorf("spool cursor %d did not move past what the first daemon read (%d bytes)", p.Cursor.Stdout, len(got))
	}
	// The next daemon re-attaches from the cursor the first one reached.
	var rest bytes.Buffer
	exit, cur, err := ts.client.Attach(context.Background(), st.ID, Cursor{Stdout: int64(len(got))}, &rest, nil)
	if err != nil {
		t.Fatal(err)
	}
	if exit.Code != 0 {
		t.Errorf("exit = %+v", exit)
	}
	if whole := got + rest.String(); whole != wantLines(40) {
		t.Errorf("the two daemons read %q, want every line once", whole)
	}
	if cur.Stdout != int64(len(wantLines(40))) {
		t.Errorf("cursor = %+v", cur)
	}
	// Its exit was read: the spool goes. hostd releases it only once the
	// exit frame has been written, so a failed write leaves it for the
	// next daemon -- which means the client can read the exit a moment
	// before the spool is gone. Wait for it rather than racing it.
	gone := time.Now().Add(5 * time.Second)
	for {
		_, err := os.Stat(p.Spool)
		ps, _ := ts.client.Processes(context.Background())
		if os.IsNotExist(err) && len(ps) == 0 {
			break
		}
		if time.Now().After(gone) {
			t.Fatalf("5s after its exit was read: spool %s stat = %v, still listed: %+v", p.Spool, err, ps)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestADetachedProcessIsKilledAtItsOwnDeadlineWhileNoDaemonIsAttached(t *testing.T) {
	ts := startServer(t, "", nil)
	started := make(chan Started, 1)
	c := detachedClient(ts, started)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.Run(ctx, Request{Kind: KindCLI, Name: "sh", Args: []string{"-c", "echo up; sleep 30"},
			Detach: true, Run: "run-2", Deadline: time.Now().Add(1500 * time.Millisecond)}, nil, nil, nil)
		done <- err
	}()
	st := <-started
	cancel()
	<-done
	waitGone(t, st.PID, 10*time.Second)
	// waitGone sees the zombie in /proc before hostd's cmd.Wait goroutine
	// writes exit.json and records Exited; poll listed as the restart test
	// below does.
	var p Process
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		p = listed(t, ts, st.ID)
		if p.Exited {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("listing 5s after pid %d exited = %+v, want Exited", st.PID, p)
		}
	}
	if p.Exit == nil || !p.Exit.Deadline {
		t.Fatalf("listing after the deadline = %+v, want exited at its deadline", p)
	}
	// exit.json is a run host's: code, error, finished_at.
	b, err := os.ReadFile(filepath.Join(p.Spool, spoolExit))
	if err != nil {
		t.Fatal(err)
	}
	var e struct {
		Code       *int      `json:"code"`
		FinishedAt time.Time `json:"finished_at"`
	}
	if err := json.Unmarshal(b, &e); err != nil || e.Code == nil || e.FinishedAt.IsZero() {
		t.Errorf("exit.json = %s (%v), want a run host's code and finished_at", b, err)
	}
	var out bytes.Buffer
	exit, _, err := ts.client.Attach(context.Background(), st.ID, Cursor{}, &out, nil)
	if err != nil || !exit.Deadline || out.String() != "up\n" {
		t.Errorf("Attach = %+v %q %v", exit, out.String(), err)
	}
}

func TestCancelReleasesADetachedProcess(t *testing.T) {
	ts := startServer(t, "", nil)
	started := make(chan Started, 1)
	c := detachedClient(ts, started)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_, _ = c.Run(ctx, Request{Kind: KindCLI, Name: "sh", Args: []string{"-c", "sleep 30"}, Detach: true, Run: "orphan"}, nil, nil, nil)
	}()
	st := <-started
	cancel()
	if err := ts.client.Cancel(context.Background(), st.ID); err != nil {
		t.Fatal(err)
	}
	waitGone(t, st.PID, 10*time.Second)
	if ps, _ := ts.client.Processes(context.Background()); len(ps) != 0 {
		t.Errorf("still listed after Cancel: %+v", ps)
	}
	if err := ts.client.Cancel(context.Background(), st.ID); err != nil {
		t.Errorf("a second Cancel = %v, want nil", err)
	}
}

func TestCancellingTheRunsContextStillKillsADetachedProcess(t *testing.T) {
	ts := startServer(t, "", nil)
	started := make(chan Started, 1)
	c := ts.client
	c.Detach = func(context.Context) bool { return false } // a task cancel, not the daemon stopping
	c.OnStarted = func(s Started) { started <- s }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		exit, err := c.Run(ctx, Request{Kind: KindCLI, Name: "sh", Args: []string{"-c", "sleep 30"}, Detach: true}, nil, nil, nil)
		if err == nil && exit.Signal != "KILL" {
			err = fmt.Errorf("exit = %+v, want KILL", exit)
		}
		done <- err
	}()
	st := <-started
	cancel()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Error(err)
	}
	waitGone(t, st.PID, 10*time.Second)
}

func TestAttachSignalsReachTheProcessGroup(t *testing.T) {
	ts := startServer(t, "", nil)
	started := make(chan Started, 1)
	c := detachedClient(ts, started)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_, _ = c.Run(ctx, Request{Kind: KindCLI, Name: "sh", Args: []string{"-c", "echo hi; sleep 30"}, Detach: true}, nil, nil, nil)
	}()
	st := <-started
	cancel()
	// A re-attached daemon cancelling its run kills it the normal way.
	actx, acancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(300 * time.Millisecond)
		acancel()
	}()
	_, _, err := ts.client.Attach(actx, st.ID, Cursor{}, nil, nil)
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	waitGone(t, st.PID, 10*time.Second)
}

func TestAttachRefusesAnUnknownProcess(t *testing.T) {
	ts := startServer(t, "", nil)
	_, _, err := ts.client.Attach(context.Background(), "p-nope", Cursor{}, nil, nil)
	var r *RefusedError
	if !errors.As(err, &r) {
		t.Fatalf("err = %v, want a refusal", err)
	}
}

func TestARestartedHostdKeepsAFinishedSpoolForTheDaemon(t *testing.T) {
	state, err := os.MkdirTemp("", "hostd-state")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(state) })
	ts := startServer(t, "", func(s *Server) { s.StateDir = state })
	started := make(chan Started, 1)
	c := detachedClient(ts, started)
	ctx, cancel := context.WithCancel(context.Background())
	// The process finishes only once the daemon has detached from it. One
	// that exits at once can race the cancel below, deliver its exit to the
	// still-attached daemon, and have its spool released as read -- the
	// opposite of what this test is about.
	goAhead := filepath.Join(ts.dir, "go-ahead")
	script := "while [ ! -e go-ahead ]; do sleep 0.01; done; echo done; exit 7"
	ran := make(chan struct{})
	go func() {
		defer close(ran)
		_, _ = c.Run(ctx, Request{Kind: KindCLI, Name: "sh", Args: []string{"-c", script}, Dir: ts.dir, Detach: true, Run: "r"}, nil, nil, nil)
	}()
	st := <-started
	cancel()
	<-ran
	if err := os.WriteFile(goAhead, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitGone(t, st.PID, 10*time.Second)
	for !listed(t, ts, st.ID).Exited {
		time.Sleep(20 * time.Millisecond)
	}
	ts.cancel()
	<-ts.done
	ts.done <- nil // for the first server's cleanup

	ts2 := startServer(t, "", func(s *Server) { s.StateDir = state })
	p := listed(t, ts2, st.ID)
	if !p.Exited || p.Exit == nil || p.Exit.Code != 7 || p.Run != "r" {
		t.Fatalf("after a restart: %+v", p)
	}
	var out bytes.Buffer
	exit, _, err := ts2.client.Attach(context.Background(), st.ID, Cursor{}, &out, nil)
	if err != nil || exit.Code != 7 || out.String() != "done\n" {
		t.Errorf("Attach = %+v %q %v", exit, out.String(), err)
	}
}

// hostd stopping -- systemd's stop or restart, SIGTERM -- kills a run's
// detached CLI for the host's reasons, not the run's: its exit says
// Draining, as one a crash left without an exit does (loadSpools), so the
// daemon re-queues the run as interrupted rather than failing it with a
// SIGKILL that nothing about the task earned.
func TestAStoppingHostdEndsADetachedRunAsDraining(t *testing.T) {
	state, err := os.MkdirTemp("", "hostd-state")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(state) })
	ts := startServer(t, "", func(s *Server) { s.StateDir = state })
	started := make(chan Started, 1)
	c := detachedClient(ts, started)
	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan struct{})
	go func() {
		defer close(ran)
		_, _ = c.Run(ctx, Request{Kind: KindCLI, Name: "sh", Args: []string{"-c", "sleep 60"}, Detach: true, Run: "r"}, nil, nil, nil)
	}()
	st := <-started
	cancel()
	<-ran
	ts.cancel()
	if err := <-ts.done; err != nil {
		t.Fatal(err)
	}
	ts.done <- nil // for the first server's cleanup
	waitGone(t, st.PID, 10*time.Second)

	ts2 := startServer(t, "", func(s *Server) { s.StateDir = state })
	p := listed(t, ts2, st.ID)
	if !p.Exited || p.Exit == nil || !p.Exit.Draining {
		t.Fatalf("a run hostd's own stop killed, as the next hostd lists it = %+v (exit %+v), want it ended Draining", p, p.Exit)
	}
}

// The same for a protocol-1 start, whose exit goes down the connection
// that started it.
func TestAStoppingHostdEndsAnAttachedStartAsDraining(t *testing.T) {
	ts := startServer(t, "", nil)
	started := make(chan Started, 1)
	c := ts.client
	c.OnStarted = func(s Started) { started <- s }
	type result struct {
		exit Exit
		err  error
	}
	done := make(chan result, 1)
	go func() {
		exit, err := c.Run(context.Background(), Request{Kind: KindCLI, Name: "sh", Args: []string{"-c", "sleep 60"}}, nil, nil, nil)
		done <- result{exit, err}
	}()
	<-started
	ts.cancel()
	r := <-done
	if r.err != nil || !r.exit.Draining {
		t.Fatalf("a start hostd's own stop killed = %+v, %v; want it ended Draining", r.exit, r.err)
	}
}

func TestHelloAndCompatibility(t *testing.T) {
	ts := startServer(t, "", func(s *Server) { s.Build = "v9.9.9" })
	h, err := ts.client.Hello(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if h.Protocol != ProtocolVersion || h.MinCompatible != MinCompatible || h.Build != "v9.9.9" || h.Draining {
		t.Errorf("Hello = %+v", h)
	}
	if !h.SupportsAttach() {
		t.Error("this build's hostd does not say it supports attach")
	}
	if !h.SupportsSocketOwner() {
		t.Error("this build's hostd does not say it supports socket_owner")
	}
	if legacyHello.SupportsAttach() || legacyHello.SupportsSocketOwner() {
		t.Error("a protocol-1 hostd is said to support attach or socket_owner")
	}
	if (Hello{Protocol: 2, MinCompatible: 1}).SupportsSocketOwner() {
		t.Error("a protocol-2 hostd is said to support socket_owner")
	}
}

func TestCompatible(t *testing.T) {
	this := ThisHello("")
	for _, c := range []struct {
		name string
		peer Hello
		ok   bool
	}{
		{"same", this, true},
		{"one older (protocol 1, which predates hello)", legacyHello, true},
		{"one newer, still serving this one", Hello{Protocol: ProtocolVersion + 1, MinCompatible: ProtocolVersion}, true},
		{"newer, no longer serving this one", Hello{Protocol: ProtocolVersion + 2, MinCompatible: ProtocolVersion + 1}, false},
		{"older than this build's minimum", Hello{Protocol: MinCompatible - 1, MinCompatible: MinCompatible - 1}, false},
	} {
		err := Compatible(this, c.peer)
		if (err == nil) != c.ok {
			t.Errorf("%s: Compatible = %v, want ok=%v", c.name, err, c.ok)
		}
		if err != nil && !strings.Contains(err.Error(), "hostd is updating or mismatched") {
			t.Errorf("%s: %v does not say hostd is updating", c.name, err)
		}
	}
}

func TestDrainRefusesNewStartsKeepsChildrenAndExitsWhenTheyEnd(t *testing.T) {
	ts := startServer(t, "", nil)
	started := make(chan Started, 1)
	c := detachedClient(ts, started)
	ctx, cancel := context.WithCancel(context.Background())
	// Long enough to outlast the checks below on a slow -race runner: at
	// 1.5s the child could end first, hostd would already have drained and
	// exited, and the start while draining was not refused but answered.
	go func() {
		_, _ = c.Run(ctx, Request{Kind: KindCLI, Name: "sh", Args: []string{"-c", "sleep 5"}, Detach: true}, nil, nil, nil)
	}()
	st := <-started
	cancel()
	if err := ts.client.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := ts.sh(context.Background(), "echo no", "")
	if !IsDraining(err) {
		t.Errorf("a start while draining = %v, want a draining refusal", err)
	}
	if h, _ := ts.client.Hello(context.Background()); !h.Draining {
		t.Errorf("Hello while draining = %+v", h)
	}
	if !pidAlive(st.PID) {
		t.Error("draining killed a running child")
	}
	if listed(t, ts, st.ID).Exited {
		t.Error("child listed as exited while it runs")
	}
	select {
	case <-ts.done:
		t.Fatal("hostd exited while a child still ran")
	case <-time.After(500 * time.Millisecond):
	}
	select {
	case err := <-ts.done:
		ts.done <- err
	case <-time.After(15 * time.Second):
		t.Fatal("hostd did not exit once its child ended")
	}
	if !strings.Contains(ts.logs.String(), "drained") {
		t.Errorf("no drained line:\n%s", ts.logs.String())
	}
}

func TestDrainCancelsAStuckChildAtTheCap(t *testing.T) {
	ts := startServer(t, "drain_cap: 500ms\n", nil)
	started := make(chan Started, 1)
	c := detachedClient(ts, started)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_, _ = c.Run(ctx, Request{Kind: KindCLI, Name: "sh", Args: []string{"-c", "sleep 60"}, Detach: true, Run: "stuck"}, nil, nil, nil)
	}()
	st := <-started
	cancel()
	ts.srv.Drain()
	select {
	case err := <-ts.done:
		ts.done <- err
	case <-time.After(15 * time.Second):
		t.Fatal("hostd did not exit at its drain cap")
	}
	waitGone(t, st.PID, 10*time.Second)
	if !strings.Contains(ts.logs.String(), "drain cap 500ms reached: cancelling cli \"sh\"") {
		t.Errorf("the cap's cancel was not logged:\n%s", ts.logs.String())
	}
}

func TestDrainDoesNotWaitForACapabilityServerButEndsIt(t *testing.T) {
	sh, _ := filepath.EvalSymlinks("/bin/sh")
	ts := startServer(t, fmt.Sprintf("capabilities:\n  cap:\n    command: %s\n    args: [-c, \"sleep 60\"]\n", sh), nil)
	started := make(chan Started, 1)
	c := ts.client
	c.OnStarted = func(s Started) { started <- s }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_, _ = c.Run(ctx, Request{Kind: KindCapability, Name: "cap"}, nil, nil, nil)
	}()
	st := <-started
	ts.srv.Drain()
	select {
	case err := <-ts.done:
		ts.done <- err
	case <-time.After(15 * time.Second):
		t.Fatal("hostd's drain waited on a capability server")
	}
	waitGone(t, st.PID, 10*time.Second)
	if !strings.Contains(ts.logs.String(), `ending capability server "cap"`) {
		t.Errorf("ending the capability server was not logged:\n%s", ts.logs.String())
	}
}

// untilExists is sh's arguments for a run that lasts until path exists.
func untilExists(path string) []string {
	// hostd refuses a path that does not exist yet, so the directory and
	// the bare name go apart.
	return []string{"-c", `cd "$1" && while [ ! -e "$2" ]; do sleep 0.1; done`, "sh", filepath.Dir(path), filepath.Base(path)}
}

// While hostd drains for the runs it is still carrying, a capability
// server still starts -- gitproxy mints those runs' credentials through
// one -- and is ended with the rest once the runs are done.
func TestACapabilityServerStartsWhileDrainingAndEndsWithTheDrain(t *testing.T) {
	sh, _ := filepath.EvalSymlinks("/bin/sh")
	ts := startServer(t, fmt.Sprintf("capabilities:\n  cap:\n    command: %s\n    args: [-c, \"sleep 60\"]\n", sh), nil)
	started := make(chan Started, 1)
	c := detachedClient(ts, started)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release := filepath.Join(ts.dir, "release")
	go func() {
		_, _ = c.Run(ctx, Request{Kind: KindCLI, Name: "sh", Args: untilExists(release),
			Detach: true, Run: "holding"}, nil, nil, nil)
	}()
	run := <-started
	if err := ts.client.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := ts.sh(context.Background(), "echo no", ""); !IsDraining(err) {
		t.Fatalf("a CLI start while draining = %v, want a draining refusal", err)
	}

	capStarted := make(chan Started, 1)
	capClient := ts.client
	capClient.OnStarted = func(s Started) { capStarted <- s }
	capDone := make(chan error, 1)
	go func() {
		_, err := capClient.Run(ctx, Request{Kind: KindCapability, Name: "cap"}, nil, nil, nil)
		capDone <- err
	}()
	var server Started
	select {
	case server = <-capStarted:
	case err := <-capDone:
		t.Fatalf("a capability server start while draining = %v, want it started", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the capability server never started")
	}
	select {
	case <-ts.done:
		t.Fatal("hostd exited while a run still ran")
	case <-time.After(300 * time.Millisecond):
	}

	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-ts.done:
		ts.done <- err
	case <-time.After(15 * time.Second):
		t.Fatal("hostd did not exit once its run ended")
	}
	waitGone(t, server.PID, 10*time.Second)
	waitGone(t, run.PID, 10*time.Second)
	if !strings.Contains(ts.logs.String(), `ending capability server "cap"`) {
		t.Errorf("ending the capability server was not logged:\n%s", ts.logs.String())
	}
}

// A cutover keeps taking starts while runs go on -- a daemon notices
// nothing -- and hostd exits at the first moment none is running.
func TestCutoverKeepsTakingStartsAndExitsWhenNothingRuns(t *testing.T) {
	ts := startServer(t, "", nil)
	started := make(chan Started, 2)
	c := detachedClient(ts, started)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := filepath.Join(ts.dir, "first")
	go func() {
		_, _ = c.Run(ctx, Request{Kind: KindCLI, Name: "sh", Args: untilExists(first),
			Detach: true, Run: "first"}, nil, nil, nil)
	}()
	<-started
	if err := ts.client.Cutover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h, err := ts.client.Hello(context.Background()); err != nil || h.Draining || !h.Cutover {
		t.Errorf("Hello during a cutover = %+v, %v; want it cutting over and taking starts\nhostd's log:\n%s",
			h, err, ts.logs.String())
	}
	// A start during the cutover runs, and holds it open as the first
	// run ends.
	second := filepath.Join(ts.dir, "second")
	secondDone := make(chan error, 1)
	go func() {
		_, err := c.Run(ctx, Request{Kind: KindCLI, Name: "sh", Args: untilExists(second),
			Detach: true, Run: "second"}, nil, nil, nil)
		secondDone <- err
	}()
	select {
	case <-started:
	case err := <-secondDone:
		t.Fatalf("a start during a cutover = %v, want it started", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the start during the cutover never started")
	}
	if err := os.WriteFile(first, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ts.done:
		t.Fatal("hostd exited during a cutover while a run still ran")
	case <-time.After(500 * time.Millisecond):
	}
	if err := os.WriteFile(second, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-ts.done:
		ts.done <- err
	case <-time.After(15 * time.Second):
		t.Fatal("hostd did not exit once nothing ran")
	}
	if strings.Contains(ts.logs.String(), "refused") || strings.Contains(ts.logs.String(), "drain cap") {
		t.Errorf("a cutover refused or cancelled something:\n%s", ts.logs.String())
	}
}

// A cutover that never finds a moment with nothing running turns into a
// drain at drain_cap: new starts are refused so the upgrade can go ahead.
func TestCutoverDrainsWhenNothingStopsWithinTheCap(t *testing.T) {
	ts := startServer(t, "drain_cap: 300ms\n", nil)
	started := make(chan Started, 1)
	c := detachedClient(ts, started)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_, _ = c.Run(ctx, Request{Kind: KindCLI, Name: "sh", Args: []string{"-c", "sleep 60"}, Detach: true, Run: "long"}, nil, nil, nil)
	}()
	st := <-started
	ts.srv.Cutover()
	if ts.srv.Draining() {
		t.Fatal("a cutover refuses starts from the outset")
	}
	for deadline := time.Now().Add(10 * time.Second); !ts.srv.Draining(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the cutover never turned into a drain")
		}
	}
	if _, _, _, err := ts.sh(context.Background(), "echo no", ""); !IsDraining(err) {
		t.Errorf("a start once the cutover drains = %v, want a draining refusal", err)
	}
	select {
	case err := <-ts.done:
		ts.done <- err
	case <-time.After(15 * time.Second):
		t.Fatal("hostd did not exit at the drain's own cap")
	}
	waitGone(t, st.PID, 10*time.Second)
}

// drain_cap is read on every pass of a drain, not once as it starts: a
// drain that began on an allowlist it could not read, or one that set no
// cap, takes the cap the file holds as soon as it holds one, rather than
// waiting DefaultDrainCap on a run that never ends.
func TestCutoverTakesADrainCapTheAllowlistGainsAfterItStarted(t *testing.T) {
	ts := startServer(t, "", nil)
	started := make(chan Started, 1)
	c := detachedClient(ts, started)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_, _ = c.Run(ctx, Request{Kind: KindCLI, Name: "sh", Args: []string{"-c", "sleep 60"}, Detach: true, Run: "stuck"}, nil, nil, nil)
	}()
	st := <-started
	good, err := os.ReadFile(ts.srv.Allowlist)
	if err != nil {
		t.Fatal(err)
	}
	// The file as an in-place edit leaves it for a moment: cut short.
	if err := os.WriteFile(ts.srv.Allowlist, []byte("clis:\n  sh:\n    path: [\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ts.srv.Cutover()
	for deadline := time.Now().Add(10 * time.Second); !strings.Contains(ts.logs.String(), "using "+DefaultDrainCap.String()); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the drain did not start on the unreadable allowlist:\n%s", ts.logs.String())
		}
	}
	if err := os.WriteFile(ts.srv.Allowlist, append(good, "drain_cap: 300ms\n"...), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-ts.done:
		ts.done <- err
	case <-time.After(15 * time.Second):
		t.Fatalf("hostd did not exit at the drain_cap the allowlist gained:\n%s", ts.logs.String())
	}
	waitGone(t, st.PID, 10*time.Second)
	for _, want := range []string{"drain_cap is now 300ms", "drain cap 300ms reached: cancelling cli \"sh\""} {
		if !strings.Contains(ts.logs.String(), want) {
			t.Errorf("no %q line:\n%s", want, ts.logs.String())
		}
	}
}

func TestAProtocol1StartIsListedButNotAttachable(t *testing.T) {
	ts := startServer(t, "", nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _, _ = ts.sh(context.Background(), "sleep 30", "")
	}()
	var p Process
	for deadline := time.Now().Add(10 * time.Second); ; {
		ps, err := ts.client.Processes(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(ps) == 1 {
			p = ps[0]
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("listing = %+v, want the one process", ps)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if p.Detached || p.Spool != "" || p.PID == 0 {
		t.Errorf("protocol-1 listing = %+v", p)
	}
	var r *RefusedError
	if _, _, err := ts.client.Attach(context.Background(), p.ID, Cursor{}, nil, nil); !errors.As(err, &r) {
		t.Errorf("Attach to a protocol-1 start = %v, want a refusal", err)
	}
	start := time.Now()
	if err := ts.client.Cancel(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("Cancel took %s", time.Since(start))
	}
	waitGone(t, p.PID, 10*time.Second)
	<-done
}

// A handover whose exec onto the new build fails ends the runs it had
// let go of, as hostd stopping does: each is reaped with its exit
// recorded as Draining, in this hostd and at once, so that Serve returns
// for systemd to restart it without waiting out each process in turn.
// Before, the watchers the handover released were never started again:
// nothing reaped the killed runs or wrote their exits, and Serve waited
// WaitDelay and a second for every one of them.
func TestAFailedHandoverReexecEndsItsRunsAsDraining(t *testing.T) {
	ts := startServer(t, "", func(s *Server) {
		s.Reexec = func() error { return errors.New("the new build is not there") }
	})
	var sts []Started
	for _, run := range []string{"a", "b"} {
		started := make(chan Started, 1)
		c := detachedClient(ts, started)
		ctx, cancel := context.WithCancel(context.Background())
		ran := make(chan struct{})
		go func() {
			defer close(ran)
			_, _ = c.Run(ctx, Request{Kind: KindCLI, Name: "sh", Args: []string{"-c", "sleep 60"}, Detach: true, Run: run}, nil, nil, nil)
		}()
		sts = append(sts, <-started)
		cancel()
		<-ran
	}
	spools := map[string]string{}
	for _, st := range sts {
		spools[st.ID] = listed(t, ts, st.ID).Spool
	}
	if err := ts.client.Handover(context.Background()); err != nil {
		t.Fatalf("Handover: %v", err)
	}
	err := <-ts.done
	ts.done <- err
	if err == nil {
		t.Fatal("Serve after a failed exec = nil, want its error")
	}
	for _, st := range sts {
		b, rerr := os.ReadFile(filepath.Join(spools[st.ID], spoolExit))
		var exit Exit
		if rerr != nil || json.Unmarshal(b, &exit) != nil || !exit.Draining {
			t.Errorf("run %s's exit once Serve returned = %s (%v), want it recorded as Draining", st.ID, b, rerr)
		}
		waitGone(t, st.PID, 10*time.Second)
	}
}

func TestHandoverAdoptsLiveDetachedRunsAcrossServerReplacement(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	var sock string
	var allow string
	var dir string
	ts1 := startServer(t, "", func(s *Server) {
		s.StateDir = state
		s.AllowHandover = true
	})
	sock = ts1.srv.Socket
	allow = ts1.srv.Allowlist
	dir = ts1.dir

	started1 := make(chan Started, 1)
	c1 := ts1.client
	c1.OnStarted = func(s Started) { started1 <- s }
	var first lockedTestBuf
	streamDone := make(chan error, 1)
	go func() {
		_, err := c1.Run(context.Background(), Request{
			Kind: KindCLI, Name: "sh", Args: []string{"-c", steady(30) + "; exit 5"},
			Detach: true, Run: "run-handover", Task: "100", Deadline: time.Now().Add(time.Minute),
		}, strings.NewReader("initial-prompt\n"), &first, nil)
		streamDone <- err
	}()
	st1 := <-started1
	for !strings.Contains(first.String(), "line4\n") {
		time.Sleep(10 * time.Millisecond)
	}

	// Second detached process with a background grandchild and a short deadline,
	// to verify the replacement server enforces the deadline and kills the group.
	bgPIDFile := filepath.Join(dir, "bg.pid")
	started2 := make(chan Started, 1)
	c2 := detachedClient(ts1, started2)
	ctx2, cancel2 := context.WithCancel(context.Background())
	go func() {
		_, _ = c2.Run(ctx2, Request{
			Kind: KindCLI, Name: "sh",
			Args:     []string{"-c", `cd "$1" || exit; sleep 60 & echo $! > "$2"; wait`, "sh", dir, filepath.Base(bgPIDFile)},
			Detach:   true,
			Run:      "run-deadline",
			Deadline: time.Now().Add(1500 * time.Millisecond),
		}, nil, nil, nil)
	}()
	st2 := <-started2
	bgPID := readPID(t, bgPIDFile)
	cancel2()

	handoverStart := time.Now()
	if err := ts1.client.Handover(context.Background()); err != nil {
		t.Fatalf("Handover: %v", err)
	}
	select {
	case err := <-ts1.done:
		ts1.done <- err
		if err != nil {
			t.Fatalf("ts1 Serve after Handover: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ts1 did not return promptly on Handover")
	}
	if took := time.Since(handoverStart); took > 3*time.Second {
		t.Errorf("Handover waited %s, want immediate handover", took)
	}
	if err := <-streamDone; !errors.Is(err, ErrNoExit) {
		t.Fatalf("attached stream across Handover = %v, want ErrNoExit so daemon re-attaches", err)
	}
	if !pidAlive(st1.PID) || !pidAlive(st2.PID) {
		t.Fatalf("detached children died on Handover: pid1=%d alive=%v, pid2=%d alive=%v",
			st1.PID, pidAlive(st1.PID), st2.PID, pidAlive(st2.PID))
	}

	// Start replacement server on the same StateDir and Socket.
	logs2 := &logBuf{}
	srv2 := &Server{
		Socket:          sock,
		Allowlist:       allow,
		AllowlistOwners: []uint32{uint32(os.Getuid())},
		StateDir:        state,
		DisconnectGrace: 500 * time.Millisecond,
		Env:             []string{"PATH=" + os.Getenv("PATH")},
		Logf:            logs2.logf,
		Build:           "build-new",
	}
	ctxSrv2, cancelSrv2 := context.WithCancel(context.Background())
	done2 := make(chan error, 1)
	go func() { done2 <- srv2.Serve(ctxSrv2) }()
	t.Cleanup(func() { cancelSrv2(); <-done2 })

	client2 := Client{Socket: sock}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if h, err := client2.Hello(context.Background()); err == nil && h.Build == "build-new" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("replacement server never answered")
		}
	}

	gotBefore := first.String()
	var rest bytes.Buffer
	exit1, _, err := client2.Attach(context.Background(), st1.ID, Cursor{Stdout: int64(len(gotBefore))}, &rest, nil)
	if err != nil {
		t.Fatalf("Attach on replacement server: %v", err)
	}
	if exit1.Code != 5 {
		t.Errorf("adopted process exit = %+v, want code 5", exit1)
	}
	if whole := gotBefore + rest.String(); whole != wantLines(30) {
		t.Errorf("stream across handover =\n%q\nwant\n%q", whole, wantLines(30))
	}

	// Replacement server enforces st2's deadline and kills its background grandchild.
	waitGone(t, st2.PID, 10*time.Second)
	waitGone(t, bgPID, 10*time.Second)
	exit2, _, err := client2.Attach(context.Background(), st2.ID, Cursor{}, nil, nil)
	if err != nil || !exit2.Deadline {
		t.Errorf("adopted deadline process Attach = %+v, %v; want Deadline=true", exit2, err)
	}
}

type lockedTestBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedTestBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedTestBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func TestHelperHostdReexec(t *testing.T) {
	sock := os.Getenv("GRAIN_TEST_HOSTD_SOCKET")
	if sock == "" {
		return
	}
	allow := os.Getenv("GRAIN_TEST_HOSTD_ALLOWLIST")
	state := os.Getenv("GRAIN_TEST_HOSTD_STATE")
	build := os.Getenv("GRAIN_TEST_HOSTD_BUILD")
	exe, err := os.Executable()
	if err != nil {
		os.Exit(2)
	}
	srv := &Server{
		Socket:          sock,
		Allowlist:       allow,
		AllowlistOwners: []uint32{uint32(os.Getuid())},
		StateDir:        state,
		DisconnectGrace: 500 * time.Millisecond,
		Env:             []string{"PATH=" + os.Getenv("PATH")},
		Build:           build,
		Reexec: func() error {
			_ = os.Setenv("GRAIN_TEST_HOSTD_BUILD", build+"-next")
			return syscall.Exec(exe, os.Args, os.Environ())
		},
	}
	if err := srv.Serve(context.Background()); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestHandoverReexecInPlacePreservesPIDAndAdoptsLiveDetachedRuns(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("hostd refuses to run as root")
	}
	sh, err := filepath.EvalSymlinks("/bin/sh")
	if err != nil {
		t.Skipf("no /bin/sh: %v", err)
	}
	dir, err := os.MkdirTemp("", "hostd-reexec")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	allow := filepath.Join(dir, "hostd.yaml")
	body := fmt.Sprintf("clis:\n  sh:\n    path: %s\n    paths_under: [%[2]s]\n    cwd_under: [%[2]s]\n", sh, dir)
	if err := os.WriteFile(allow, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "run", "hostd.sock")
	state := filepath.Join(dir, "state")

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestHelperHostdReexec$")
	cmd.Env = append(os.Environ(),
		"GRAIN_TEST_HOSTD_SOCKET="+sock,
		"GRAIN_TEST_HOSTD_ALLOWLIST="+allow,
		"GRAIN_TEST_HOSTD_STATE="+state,
		"GRAIN_TEST_HOSTD_BUILD=v1",
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	hostdPID := cmd.Process.Pid
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	client := Client{Socket: sock}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if h, err := client.Hello(context.Background()); err == nil && h.Build == "v1" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("helper hostd v1 never answered")
		}
	}

	release := filepath.Join(dir, "release")
	started := make(chan Started, 1)
	c := client
	c.Detach = func(context.Context) bool { return true }
	c.OnStarted = func(s Started) { started <- s }
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_, _ = c.Run(ctx, Request{
			Kind: KindCLI, Name: "sh",
			Args:     []string{"-c", `cd "$1" && while [ ! -e "$2" ]; do sleep 0.05; done; echo reexec-ok; exit 9`, "sh", dir, filepath.Base(release)},
			Detach:   true,
			Run:      "run-across-exec",
			Deadline: time.Now().Add(time.Minute),
		}, nil, nil, nil)
	}()
	st := <-started
	cancel()

	if err := client.Handover(context.Background()); err != nil {
		t.Fatalf("Handover: %v", err)
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if h, err := client.Hello(context.Background()); err == nil && h.Build == "v1-next" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("helper hostd never re-execed onto v1-next")
		}
	}
	if !pidAlive(hostdPID) {
		t.Fatalf("hostd pid %d died instead of re-execing in place", hostdPID)
	}
	if !pidAlive(st.PID) {
		t.Fatalf("detached child pid %d died across in-place re-exec", st.PID)
	}

	// New detached runs start immediately on v1-next without waiting.
	var quickOut bytes.Buffer
	qExit, err := client.Run(context.Background(), Request{
		Kind: KindCLI, Name: "sh", Args: []string{"-c", "echo during-ok"}, Detach: true, Run: "run-new",
	}, nil, &quickOut, nil)
	if err != nil || qExit.Code != 0 || quickOut.String() != "during-ok\n" {
		t.Fatalf("new detached start after re-exec = %+v, %q, %v", qExit, quickOut.String(), err)
	}

	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	exit, _, err := client.Attach(context.Background(), st.ID, Cursor{}, &out, nil)
	if err != nil || exit.Code != 9 || out.String() != "reexec-ok\n" {
		t.Fatalf("Attach after in-place re-exec = %+v, %q, %v", exit, out.String(), err)
	}
}

func TestDrainCapMarksCancelledProcessesAsDraining(t *testing.T) {
	ts := startServer(t, "drain_cap: 300ms\n", nil)
	started := make(chan Started, 1)
	c := ts.client
	c.OnStarted = func(s Started) { started <- s }
	type res struct {
		exit Exit
		err  error
	}
	ch := make(chan res, 1)
	go func() {
		exit, err := c.Run(context.Background(), Request{
			Kind: KindCLI, Name: "sh", Args: []string{"-c", "sleep 60"}, Detach: true, Run: "capped",
		}, nil, nil, nil)
		ch <- res{exit, err}
	}()
	st := <-started
	ts.srv.Drain()
	r := <-ch
	if r.err != nil {
		t.Fatalf("Run across drain_cap = %v, want Exit frame", r.err)
	}
	if !r.exit.Draining || !strings.Contains(r.exit.String(), "drained for an upgrade") {
		t.Errorf("Exit at drain_cap = %+v (%q), want Draining=true", r.exit, r.exit.String())
	}
	waitGone(t, st.PID, 10*time.Second)
	select {
	case err := <-ts.done:
		ts.done <- err
	case <-time.After(10 * time.Second):
		t.Fatal("hostd did not exit after drain_cap")
	}

	// A spool left without exit.json when hostd stopped without handover is
	// also closed with Draining=true by the next hostd's loadSpools.
	state := filepath.Join(t.TempDir(), "state")
	spoolDir := filepath.Join(state, "spool", "p-dead")
	if err := os.MkdirAll(spoolDir, 0o700); err != nil {
		t.Fatal(err)
	}
	infoBytes, _ := json.Marshal(Process{ID: "p-dead", Kind: KindCLI, Name: "sh", PID: 99999999, Detached: true, Run: "lost"})
	if err := os.WriteFile(filepath.Join(spoolDir, spoolProcess), infoBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	ts2 := startServer(t, "", func(s *Server) { s.StateDir = state })
	p := listed(t, ts2, "p-dead")
	if !p.Exited || p.Exit == nil || !p.Exit.Draining {
		t.Fatalf("unclean-stop spool listing = %+v, want Exited with Draining=true", p)
	}
}

// A previous hostd's detached run that survived it without becoming this
// hostd's child (so loadSpools cannot adopt it) has its process group
// killed by ReapRecorded, not left running unsupervised while its spool
// says it ended.
func TestAnUnadoptableSurvivingDetachedRunIsReaped(t *testing.T) {
	// setsid makes sleep its own group leader; sh exits at once, so sleep
	// is reparented away from this process.
	out, err := exec.Command("sh", "-c", "setsid sleep 60 >/dev/null 2>&1 & echo $!").Output()
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
	var start string
	for i := 0; ; i++ {
		ppid, s, err := procStatFields(pid)
		if err != nil {
			t.Fatal(err)
		}
		if ppid != os.Getpid() && syscall.Kill(-pid, 0) == nil {
			start = s
			break
		}
		if i > 100 {
			t.Fatalf("pid %d is still this process's child, or not yet its own group leader", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}

	state := filepath.Join(t.TempDir(), "state")
	spoolDir := filepath.Join(state, "spool", "p-orphan")
	if err := os.MkdirAll(spoolDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, strconv.Itoa(pid)), []byte(start), 0o600); err != nil {
		t.Fatal(err)
	}
	infoBytes, _ := json.Marshal(Process{ID: "p-orphan", Kind: KindCLI, Name: "sh", PID: pid, Detached: true, Run: "orphaned"})
	if err := os.WriteFile(filepath.Join(spoolDir, spoolProcess), infoBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	ts := startServer(t, "", func(s *Server) { s.StateDir = state })
	p := listed(t, ts, "p-orphan")
	if !p.Exited || p.Exit == nil || !p.Exit.Draining {
		t.Fatalf("orphaned spool listing = %+v, want Exited with Draining=true", p)
	}
	waitGone(t, pid, 10*time.Second)
}

// A daemon that has been written a process's Exit but does not hang up
// within DrainExitGrace is not known to have read it: hostd keeps the
// process and its spool, so the next attach still gets the Exit.
func TestAnExitNobodyHungUpOnKeepsItsSpoolForTheNextAttach(t *testing.T) {
	ts := startServer(t, "", func(s *Server) {
		s.Poll = 20 * time.Millisecond
		s.drainExitGrace = 200 * time.Millisecond
	})
	release := filepath.Join(ts.dir, "release")
	started := make(chan Started, 1)
	c := detachedClient(ts, started)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.Run(ctx, Request{
			Kind: KindCLI, Name: "sh",
			Args:     []string{"-c", `cd "$1" && while [ ! -e "$2" ]; do sleep 0.02; done; echo kept; exit 5`, "sh", ts.dir, filepath.Base(release)},
			Detach:   true,
			Run:      "run-1",
			Task:     "42",
			Deadline: time.Now().Add(time.Minute),
		}, nil, nil, nil)
		done <- err
	}()
	st := <-started
	cancel()
	if err := <-done; !errors.Is(err, ErrDetached) {
		t.Fatalf("Run after the daemon stopped = %v, want ErrDetached", err)
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// A daemon reads the Exit and then holds the connection open.
	conn, err := net.Dial("unix", ts.srv.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := writeJSON(conn, frameRequest, Request{Op: OpAttach, ID: st.ID}); err != nil {
		t.Fatal(err)
	}
	for {
		typ, _, err := readFrame(conn)
		if err != nil {
			t.Fatalf("reading the attach before its Exit: %v", err)
		}
		if typ == frameExit {
			break
		}
	}
	// attach logs before its defer clears Attached, so wait for both.
	var p Process
	deadline := time.Now().Add(10 * time.Second)
	for {
		p = listed(t, ts, st.ID)
		if strings.Contains(ts.logs.String(), "did not hang up") && p.Exited && !p.Attached {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after the unacknowledged Exit: listing %+v, logs:\n%s", p, ts.logs.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(p.Spool); err != nil {
		t.Fatalf("after the unacknowledged Exit: spool stat %v; want it kept", err)
	}
	var out bytes.Buffer
	exit, _, err := ts.client.Attach(context.Background(), st.ID, Cursor{}, &out, nil)
	if err != nil || exit.Code != 5 || out.String() != "kept\n" {
		t.Fatalf("the next attach = %+v, %v, stdout %q; want the kept Exit (code 5) and output", exit, err, out.String())
	}
}

// A connection that breaks mid-frame (io.ErrUnexpectedEOF) during Exit grace
// is an aborted client, not a clean hang-up, so hostd keeps the spool.
func TestATruncatedFrameDuringExitGraceKeepsTheSpool(t *testing.T) {
	ts := startServer(t, "", func(s *Server) {
		s.Poll = 20 * time.Millisecond
		s.drainExitGrace = 2 * time.Second
	})
	conn, err := net.Dial("unix", ts.srv.Socket)
	if err != nil {
		t.Fatal(err)
	}
	req := Request{
		Op: OpStart, Kind: KindCLI, Name: "sh",
		Args:   []string{"-c", "echo trunc-ok; exit 8"},
		Detach: true, Run: "run-trunc", Task: "43",
	}
	if err := writeJSON(conn, frameRequest, req); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	typ, payload, err := readFrame(conn)
	if err != nil || typ != frameStarted {
		conn.Close()
		t.Fatalf("first frame = %q, %v; want frameStarted", typ, err)
	}
	var st Started
	if err := json.Unmarshal(payload, &st); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	for {
		typ, _, err := readFrame(conn)
		if err != nil {
			conn.Close()
			t.Fatalf("reading before Exit: %v", err)
		}
		if typ == frameExit {
			break
		}
	}
	// Write a partial 2-byte frame header and close so control reads
	// io.ErrUnexpectedEOF while attach is waiting in DrainExitGrace.
	_, _ = conn.Write([]byte{frameSignal, 0})
	conn.Close()

	deadline := time.Now().Add(5 * time.Second)
	for listed(t, ts, st.ID).Attached {
		if time.Now().After(deadline) {
			t.Fatal("attach never let go after truncated frame")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var out bytes.Buffer
	exit, _, err := ts.client.Attach(context.Background(), st.ID, Cursor{}, &out, nil)
	if err != nil || exit.Code != 8 || out.String() != "trunc-ok\n" {
		t.Fatalf("Attach after truncated frame = %+v, %v, stdout %q; want kept Exit (code 8) and output", exit, err, out.String())
	}
}

type holdExitUnixConn struct {
	net.Conn
	onExit    func()
	closed    chan struct{}
	closeOnce sync.Once
	hdr       [5]byte
	hdrN      int
	rem       uint32
}

func (c *holdExitUnixConn) Close() error {
	defer c.closeOnce.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

func (c *holdExitUnixConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	for i := 0; i < n; {
		if c.rem > 0 {
			take := min(uint32(n-i), c.rem)
			c.rem -= take
			i += int(take)
			continue
		}
		c.hdr[c.hdrN] = b[i]
		c.hdrN++
		i++
		if c.hdrN == 5 {
			typ := c.hdr[0]
			c.rem = binary.BigEndian.Uint32(c.hdr[1:5])
			c.hdrN = 0
			if typ == frameExit && c.onExit != nil {
				fn := c.onExit
				c.onExit = nil
				fn()
				<-c.closed
				return 0, net.ErrClosed
			}
		}
	}
	return n, err
}

// A daemon detaching (Client.Detach) over a local Unix domain socket while
// hostd's frameExit is already in flight must not send a clean EOF (which
// AF_UNIX Close delivers even when unread frames sit in the receive buffer),
// so hostd keeps the spool for the replacement daemon.
func TestADetachRacingExitOverUnixSocketKeepsTheSpool(t *testing.T) {
	ts := startServer(t, "", func(s *Server) {
		s.Poll = 20 * time.Millisecond
		s.drainExitGrace = 2 * time.Second
	})
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	started := make(chan Started, 1)
	c := Client{
		Dial: func(ctx context.Context) (net.Conn, error) {
			raw, err := (&net.Dialer{}).DialContext(ctx, "unix", ts.srv.Socket)
			if err != nil {
				return nil, err
			}
			return &holdExitUnixConn{Conn: raw, onExit: cancelRun, closed: make(chan struct{})}, nil
		},
		Detach:    func(context.Context) bool { return true },
		OnStarted: func(s Started) { started <- s },
	}
	var first bytes.Buffer
	_, err := c.Run(runCtx, Request{
		Kind: KindCLI, Name: "sh",
		Args:   []string{"-c", "echo unix-race-ok; exit 9"},
		Detach: true, Run: "run-unix-race", Task: "44",
	}, nil, &first, nil)
	if !errors.Is(err, ErrDetached) {
		t.Fatalf("Run detaching in flight with Exit over unix socket = %v, want ErrDetached", err)
	}
	st := <-started
	deadline := time.Now().Add(5 * time.Second)
	for listed(t, ts, st.ID).Attached {
		if time.Now().After(deadline) {
			t.Fatal("attach never let go after unix socket abort")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var rest bytes.Buffer
	exit, _, err := ts.client.Attach(context.Background(), st.ID, Cursor{Stdout: int64(first.Len())}, &rest, nil)
	if err != nil || exit.Code != 9 || first.String()+rest.String() != "unix-race-ok\n" {
		t.Fatalf("Attach after unix detach racing Exit = %+v, %v, stdout %q; want Exit code 9 and full output",
			exit, err, first.String()+rest.String())
	}
}

// endlessStdin is a stdin that never ends, counting what has been read of it.
type endlessStdin struct{ n atomic.Int64 }

func (r *endlessStdin) Read(p []byte) (int, error) {
	r.n.Add(int64(len(p)))
	return len(p), nil
}

// Detaching while a stdin frame is blocked on a hostd that has stopped
// reading must still end the stream: abort may not wait on the send lock
// that the blocked write holds.
func TestDetachingWhileStdinIsBlockedOnHostdEndsTheStream(t *testing.T) {
	dir, err := os.MkdirTemp("", "hostd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "hostd.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	// A hostd that starts the process and then reads nothing more.
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		t.Cleanup(func() { conn.Close() })
		if _, _, err := readFrame(conn); err != nil {
			return
		}
		_ = writeJSON(conn, frameStarted, Started{PID: 1, ID: "blocked"})
	}()
	stdin := &endlessStdin{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan Started, 1)
	c := Client{
		Socket:    sock,
		Detach:    func(context.Context) bool { return true },
		OnStarted: func(s Started) { started <- s },
	}
	done := make(chan error, 1)
	go func() {
		_, err := c.Run(ctx, Request{Kind: KindCLI, Name: "sh", Detach: true}, stdin, nil, nil)
		done <- err
	}()
	<-started
	// Wait for stdin to stop being read: its send is blocked on the socket.
	deadline := time.Now().Add(10 * time.Second)
	for last := int64(-1); ; {
		n := stdin.n.Load()
		if n > 0 && n == last {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stdin never blocked (read %d bytes)", n)
		}
		last = n
		time.Sleep(100 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, ErrDetached) {
			t.Fatalf("Run after detaching = %v, want ErrDetached", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after detaching while a stdin frame was blocked")
	}
}
