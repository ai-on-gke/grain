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
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwsalmon/granule/pkg/agent"
	"github.com/bwsalmon/granule/pkg/guestexec"
	"github.com/bwsalmon/granule/pkg/guestforward"
	"github.com/bwsalmon/granule/pkg/sandbox"
)

func TestRunnerReattachesMidStepWithoutGapOrDuplicate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	a, live := newTestAcceptor(t, ctx, nil)
	const name = "grain-granule-reattach-1"
	live.set(name, true)

	creds, err := a.Mint(name, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	dir := writeCreds(t, creds)
	startWrapper(t, ctx, a, name, creds.Token, dir)

	if _, err := a.WaitClient(ctx, name); err != nil {
		t.Fatalf("WaitClient: %v", err)
	}

	runner := &Runner{
		Acceptor:        a,
		Sandbox:         name,
		User:            currentUser(t),
		ReattachTimeout: 5 * time.Second,
	}

	// Trigger a mid-step channel cut as soon as the first lines arrive on the
	// stream writer.
	var cutOnce atomic.Bool
	streamBuf := &notifyingWriter{
		onWrite: func(total int) {
			if total >= 64 && cutOnce.CompareAndSwap(false, true) {
				_ = a.DropActiveSession(name)
			}
		},
	}

	script := "i=0; while [ $i -lt 60 ]; do printf 'line-%03d\\n' $i; i=$((i+1)); sleep 0.01; done"
	stdout, stderr, code, err := runner.Exec(ctx, []string{"sh", "-c", script}, "", streamBuf)
	if err != nil {
		t.Fatalf("Exec error = %v (stderr=%q)", err, stderr)
	}
	if code != 0 {
		t.Fatalf("Exec code = %d, want 0 (stderr=%q)", code, stderr)
	}

	var want strings.Builder
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&want, "line-%03d\n", i)
	}
	if stdout != want.String() {
		t.Fatalf("stdout mismatch after reattach:\ngot (%d bytes):\n%s\nwant (%d bytes):\n%s",
			len(stdout), stdout, want.Len(), want.String())
	}
	if gotStream := streamBuf.String(); gotStream != want.String() {
		t.Fatalf("streamed output mismatch after reattach:\ngot (%d bytes):\n%s\nwant (%d bytes):\n%s",
			len(gotStream), gotStream, want.Len(), want.String())
	}
	if runner.Reattaches() == 0 {
		t.Fatalf("runner.Reattaches() = 0, want >= 1")
	}
	if a.Reconnects() == 0 {
		t.Fatalf("acceptor.Reconnects() = 0, want >= 1")
	}
}

func TestRunnerChannelCutPastTimeoutReturnsErrChannelLost(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	a, live := newTestAcceptor(t, ctx, nil)
	const name = "grain-granule-lost-1"
	live.set(name, true)

	creds, err := a.Mint(name, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	dir := writeCreds(t, creds)

	// Separate dialCtx from agentCtx so cancelling dialCtx drops the dial-out
	// connection and stops the wrapper from reconnecting while the guest
	// command stays running.
	dialCtx, stopDial := context.WithCancel(ctx)
	defer stopDial()

	srv := sandbox.NewServer(sandbox.ServerConfig{
		SandboxID: name,
		Token:     creds.Token,
		Guest:     guestexec.Config{User: currentUser(t)},
		DialGuest: func(_ context.Context) (net.Conn, error) {
			c1, c2 := net.Pipe()
			go func() {
				defer c2.Close()
				_ = agent.Serve(ctx, c2)
			}()
			return c1, nil
		},
	})
	opts := sandbox.DialOutOptions{
		CAFile:     filepath.Join(dir, KeyCA),
		CertFile:   filepath.Join(dir, KeyCert),
		KeyFile:    filepath.Join(dir, KeyKey),
		MaxBackoff: 50 * time.Millisecond,
	}
	go func() { _ = srv.DialOut(dialCtx, a.Addr().String(), opts) }()

	if _, err := a.WaitClient(ctx, name); err != nil {
		t.Fatalf("WaitClient: %v", err)
	}

	var lostCalled atomic.Int32
	var lostErr atomic.Value
	runner := &Runner{
		Acceptor:        a,
		Sandbox:         name,
		User:            currentUser(t),
		ReattachTimeout: 250 * time.Millisecond,
		OnChannelLost: func(err error) {
			lostCalled.Add(1)
			lostErr.Store(err)
		},
	}

	var cutOnce atomic.Bool
	streamBuf := &notifyingWriter{
		onWrite: func(total int) {
			if total > 0 && cutOnce.CompareAndSwap(false, true) {
				stopDial()
				_ = a.DropActiveSession(name)
			}
		},
	}

	started := time.Now()
	_, _, code, err := runner.Exec(ctx, []string{"sh", "-c", "echo started; sleep 10"}, "", streamBuf)
	elapsed := time.Since(started)

	if !errors.Is(err, ErrChannelLost) {
		t.Fatalf("Exec err = %v, want errors.Is(..., ErrChannelLost)", err)
	}
	if code != -1 {
		t.Fatalf("Exec code = %d, want -1", code)
	}
	var chErr *ChannelLostError
	if !errors.As(err, &chErr) {
		t.Fatalf("Exec err %T is not *ChannelLostError", err)
	}
	if chErr.Sandbox != name {
		t.Fatalf("ChannelLostError.Sandbox = %q, want %q", chErr.Sandbox, name)
	}
	if chErr.Down < 200*time.Millisecond {
		t.Fatalf("ChannelLostError.Down = %s, want >= 200ms (elapsed=%s)", chErr.Down, elapsed)
	}
	if lostCalled.Load() != 1 {
		t.Fatalf("OnChannelLost called %d times, want 1", lostCalled.Load())
	}
}

func TestRunnerPutFilesAndForwardOverChannel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	a, live := newTestAcceptor(t, ctx, nil)
	const name = "grain-granule-files-fwd-1"
	live.set(name, true)

	creds, err := a.Mint(name, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	dir := writeCreds(t, creds)
	startWrapper(t, ctx, a, name, creds.Token, dir)

	if _, err := a.WaitClient(ctx, name); err != nil {
		t.Fatalf("WaitClient: %v", err)
	}

	runner := &Runner{
		Acceptor:        a,
		Sandbox:         name,
		User:            currentUser(t),
		ReattachTimeout: 5 * time.Second,
	}

	workDir := t.TempDir()
	fileA := filepath.Join(workDir, "nested", "a.txt")
	fileB := filepath.Join(workDir, "nested", "b.sh")

	if err := runner.PutFiles(ctx, []sandbox.FileUpload{
		{Path: fileA, Content: []byte("alpha\n"), Mode: 0o600},
		{Path: fileB, Content: []byte("#!/bin/sh\necho beta\n"), Mode: 0o755},
	}); err != nil {
		t.Fatalf("PutFiles: %v", err)
	}

	gotA, err := os.ReadFile(fileA)
	if err != nil || string(gotA) != "alpha\n" {
		t.Fatalf("ReadFile(%s) = %q, %v; want %q", fileA, gotA, err, "alpha\n")
	}
	stA, err := os.Stat(fileA)
	if err != nil || stA.Mode().Perm() != 0o600 {
		t.Fatalf("Stat(%s) mode = %v, %v; want 0600", fileA, stA.Mode().Perm(), err)
	}

	stdout, stderr, code := runner.Run(ctx, []string{fileB}, "")
	if code != 0 || strings.TrimSpace(stdout) != "beta" {
		t.Fatalf("Run(%s) = (%q, %q, %d), want (\"beta\", \"\", 0)", fileB, stdout, stderr, code)
	}

	// Test RemoteForward (guest loopback -> host service) over the channel.
	hostLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer hostLn.Close()
	go func() {
		conn, err := hostLn.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.WriteString(conn, "pong-from-host\n")
	}()

	// Pick an unused loopback port for the guest-side remote forward.
	probeLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	guestPort := probeLn.Addr().(*net.TCPAddr).Port
	_ = probeLn.Close()

	fwd, err := runner.StartForward(ctx, []guestforward.RemoteForward{{
		Guest:  fmt.Sprintf("127.0.0.1:%d", guestPort),
		Target: hostLn.Addr().String(),
	}}, nil)
	if err != nil {
		t.Fatalf("StartForward: %v", err)
	}
	defer fwd.Close()

	// Wait briefly for the remote listener to bind, then read from it.
	var gotFwd string
	for attempt := 0; attempt < 20; attempt++ {
		c, dialErr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", guestPort), 200*time.Millisecond)
		if dialErr == nil {
			b, _ := io.ReadAll(c)
			_ = c.Close()
			gotFwd = string(b)
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if gotFwd != "pong-from-host\n" {
		t.Fatalf("forwarded read = %q, want %q", gotFwd, "pong-from-host\n")
	}
}

type notifyingWriter struct {
	buf     bytes.Buffer
	onWrite func(total int)
}

func (w *notifyingWriter) Write(p []byte) (int, error) {
	n, err := w.buf.Write(p)
	if w.onWrite != nil {
		w.onWrite(w.buf.Len())
	}
	return n, err
}

func (w *notifyingWriter) String() string {
	return w.buf.String()
}
