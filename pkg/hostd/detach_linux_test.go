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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	p := listed(t, ts, st.ID)
	if !p.Exited || p.Exit == nil || !p.Exit.Deadline {
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
	go func() {
		_, _ = c.Run(ctx, Request{Kind: KindCLI, Name: "sh", Args: []string{"-c", "sleep 1.5"}, Detach: true}, nil, nil, nil)
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
	if h, _ := ts.client.Hello(context.Background()); h.Draining || !h.Cutover {
		t.Errorf("Hello during a cutover = %+v, want it cutting over and taking starts", h)
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
