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

package sandboxaccess

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// restartHarness is one sandbox's wrapper dialling a grain whose acceptor
// can be stopped and started again at the same address, with the same CA
// -- a grain restart, as the wrapper sees one.
type restartHarness struct {
	t       *testing.T
	ctx     context.Context
	ca      *CA
	addr    string
	live    *liveSet
	name    string
	creds   Credentials
	credDir string
	cur     *Acceptor
	stop    context.CancelFunc
}

func newRestartHarness(t *testing.T, ctx context.Context, name string) *restartHarness {
	t.Helper()
	ca, err := NewCA(testDeployment, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	h := &restartHarness{t: t, ctx: ctx, ca: ca, live: &liveSet{live: map[string]bool{}}, name: name}
	h.live.set(name, true)
	h.start("127.0.0.1:0")
	h.addr = h.cur.Addr().String()
	h.creds, err = h.cur.Mint(name, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	h.credDir = writeCreds(t, h.creds)
	return h
}

func (h *restartHarness) start(addr string) {
	h.t.Helper()
	var ln net.Listener
	var err error
	// The old listener's port is free again the moment it closes, but
	// give the kernel a moment rather than flake on a busy machine.
	for i := 0; i < 50; i++ {
		if ln, err = net.Listen("tcp", addr); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		h.t.Fatal(err)
	}
	a, err := New(Config{
		Deployment:  testDeployment,
		CA:          h.ca,
		ServerNames: []string{"127.0.0.1", "grain-access.grain.svc"},
		Live:        h.live.has,
	}, ln)
	if err != nil {
		h.t.Fatal(err)
	}
	serveCtx, stop := context.WithCancel(h.ctx)
	go func() { _ = a.Serve(serveCtx) }()
	h.cur, h.stop = a, stop
	h.t.Cleanup(func() { stop(); _ = a.Close() })
}

// kill stops the acceptor the way a grain process dying does: the
// listener and every channel close at once.
func (h *restartHarness) kill() {
	h.stop()
	_ = h.cur.Close()
}

// restart brings up a new acceptor at the same address with the sandbox's
// stored token hash registered, as a restarted grain does
// (K8sGranuleSandboxes.RegisterStoredAccess).
func (h *restartHarness) restart() *Acceptor {
	h.t.Helper()
	h.start(h.addr)
	if err := h.cur.Register(h.name, TokenHash(h.creds.Token), time.Now().Add(time.Hour)); err != nil {
		h.t.Fatal(err)
	}
	return h.cur
}

func (h *restartHarness) runner(a *Acceptor) *Runner {
	return &Runner{Acceptor: a, Sandbox: h.name, User: currentUser(h.t), ReattachTimeout: 5 * time.Second}
}

// recordingAttachment is what the CI engine persists, kept in memory.
type recordingAttachment struct {
	mu     sync.Mutex
	execID string
	cursor int64
}

func (r *recordingAttachment) attachment() *Attachment {
	return &Attachment{
		Started:  func(id string) { r.mu.Lock(); r.execID = id; r.mu.Unlock() },
		Progress: func(c int64) { r.mu.Lock(); r.cursor = c; r.mu.Unlock() },
	}
}

func (r *recordingAttachment) get() (string, int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.execID, r.cursor
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// startStep starts a step on the first grain that prints a line, waits for
// flag, prints another and exits 3, and returns once the first line is in
// and grain has been killed under it.
func startStep(t *testing.T, ctx context.Context, h *restartHarness, flag string) (*recordingAttachment, chan error) {
	t.Helper()
	startWrapper(t, ctx, h.cur, h.name, h.creds.Token, h.credDir)
	if _, err := h.cur.WaitClient(ctx, h.name); err != nil {
		t.Fatalf("WaitClient: %v", err)
	}
	rec := &recordingAttachment{}
	first := h.runner(h.cur)
	// A grain that dies does not wait for its channel; this one only
	// has to give up before the test restarts it.
	first.ReattachTimeout = 50 * time.Millisecond
	script := "echo before-restart; while [ ! -e " + flag + " ]; do sleep 0.02; done; echo after-restart; exit 3"
	done := make(chan error, 1)
	go func() {
		_, _, _, err := first.ExecAttached(ctx, []string{"sh", "-c", script}, "", nil, rec.attachment())
		done <- err
	}()
	waitFor(t, "the first line of the step", func() bool { _, c := rec.get(); return c > 0 })
	h.kill()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the killed grain's Exec never returned")
	}
	return rec, done
}

func TestRunnerReattachesByExecIDAcrossAGrainRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newRestartHarness(t, ctx, "grain-granule-restart-1")
	flag := filepath.Join(t.TempDir(), "go")
	rec, _ := startStep(t, ctx, h, flag)

	execID, cursor := rec.get()
	if execID == "" || cursor != int64(len("before-restart\n")) {
		t.Fatalf("persisted attachment = (%q, %d), want an exec id and cursor %d", execID, cursor, len("before-restart\n"))
	}

	second := h.runner(h.restart())
	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = os.WriteFile(flag, nil, 0o644)
	}()
	again := &recordingAttachment{}
	att := again.attachment()
	att.ExecID, att.Cursor = execID, cursor
	stdout, stderr, code, err := second.ExecAttached(ctx, nil, "", nil, att)
	if err != nil {
		t.Fatalf("reattach: %v (stderr %q)", err, stderr)
	}
	if code != 3 {
		t.Fatalf("exit code = %d, want the step's own 3", code)
	}
	if stdout != "after-restart\n" {
		t.Fatalf("stdout after reattach = %q, want only what followed the cursor", stdout)
	}
	if !strings.Contains(stderr, "reattached to this step after a restart") {
		t.Fatalf("stderr = %q, want a note saying output before the cursor is not repeated", stderr)
	}
	if id, c := again.get(); id != execID || c != cursor+int64(len("after-restart\n")) {
		t.Fatalf("attachment after reattach = (%q, %d), want (%q, %d)", id, c, execID, cursor+int64(len("after-restart\n")))
	}
}

func TestRunnerReattachToAStepThatFinishedWhileGrainWasDownYieldsItsExitCode(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newRestartHarness(t, ctx, "grain-granule-restart-2")
	flag := filepath.Join(t.TempDir(), "go")
	rec, _ := startStep(t, ctx, h, flag)

	// The step ends with no grain to tell.
	if err := os.WriteFile(flag, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)

	execID, cursor := rec.get()
	second := h.runner(h.restart())
	stdout, _, code, err := second.ExecAttached(ctx, nil, "", nil, &Attachment{ExecID: execID, Cursor: cursor})
	if err != nil {
		t.Fatalf("reattach: %v", err)
	}
	if code != 3 || stdout != "after-restart\n" {
		t.Fatalf("reattach = (%q, %d), want (%q, 3)", stdout, code, "after-restart\n")
	}
}

func TestRunnerReattachToAnExecTheWrapperNoLongerKnowsIsChannelLost(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newRestartHarness(t, ctx, "grain-granule-restart-3")
	a := h.cur
	startWrapper(t, ctx, a, h.name, h.creds.Token, h.credDir)
	if _, err := a.WaitClient(ctx, h.name); err != nil {
		t.Fatal(err)
	}
	_, _, code, err := h.runner(a).ExecAttached(ctx, nil, "", nil, &Attachment{ExecID: "no-such-exec", Cursor: 10})
	if !errors.Is(err, ErrChannelLost) || code != -1 {
		t.Fatalf("reattach to an unknown exec = (%d, %v), want (-1, ErrChannelLost)", code, err)
	}
}

func TestRunnerReattachToASandboxThatNeverDialsInIsChannelLost(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newRestartHarness(t, ctx, "grain-granule-restart-4")
	r := h.runner(h.cur)
	r.ReattachTimeout = 200 * time.Millisecond
	_, _, _, err := r.ExecAttached(ctx, nil, "", nil, &Attachment{ExecID: "e1", Cursor: 0})
	var lost *ChannelLostError
	if !errors.As(err, &lost) || lost.Sandbox != h.name {
		t.Fatalf("reattach to a sandbox that never dials in = %v, want a ChannelLostError for %s", err, h.name)
	}
}
