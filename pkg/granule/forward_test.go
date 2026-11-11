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

package granule

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/gke-labs/granule/pkg/guestforward"
)

func TestForwardArgsAndKubectlForwardArgs(t *testing.T) {
	t.Parallel()

	cfg := KubectlForwardConfig{
		Kubectl:     "/usr/local/bin/kubectl",
		Kubeconfig:  "/var/lib/grain/kubeconfig",
		Namespace:   "grain",
		Pod:         "grain-granule-7-1",
		Container:   "sandbox",
		RemotePorts: []int{9730, 9728, 9728},
		Locals: []ForwardSpec{
			{BindHost: "127.0.0.1", BindPort: 18080, TargetHost: "127.0.0.1", TargetPort: 8080},
		},
	}

	gotCLI := Local.ForwardArgs(cfg)
	wantCLI := []string{
		"forward", "pod/grain-granule-7-1",
		"-kubeconfig", "/var/lib/grain/kubeconfig",
		"-n", "grain",
		"-c", "sandbox",
		"-L", "127.0.0.1:18080=127.0.0.1:8080",
		"-R", "127.0.0.1:9728=127.0.0.1:9728",
		"-R", "127.0.0.1:9730=127.0.0.1:9730",
	}
	if !slices.Equal(gotCLI, wantCLI) {
		t.Errorf("ForwardArgs() = %v, want %v", gotCLI, wantCLI)
	}

	gotKubectl := KubectlForwardArgs(cfg)
	wantKubectl := []string{
		"--kubeconfig", "/var/lib/grain/kubeconfig",
		"-n", "grain",
		"exec", "-i",
		"-c", "sandbox",
		"grain-granule-7-1",
		"--",
		"granule", "forward-stdio",
	}
	if !slices.Equal(gotKubectl, wantKubectl) {
		t.Errorf("KubectlForwardArgs() = %v, want %v", gotKubectl, wantKubectl)
	}
}

func TestServeForwardRoundTrip(t *testing.T) {
	t.Parallel()

	controllerLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer controllerLn.Close()
	controllerPort := controllerLn.Addr().(*net.TCPAddr).Port

	go func() {
		conn, err := controllerLn.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn)
	}()

	// The guest port is probed free and closed before the guest binds it,
	// so another listener can take it in between: that is retried on a
	// fresh port, and any other failure is reported rather than read as a
	// forward that never became ready.
	var (
		guestPort int
		f         *forwardPair
	)
	for attempt := 0; ; attempt++ {
		probe, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		guestPort = probe.Addr().(*net.TCPAddr).Port
		_ = probe.Close()
		f = startForwardPair([]ForwardSpec{{
			BindHost:   "127.0.0.1",
			BindPort:   guestPort,
			TargetHost: "127.0.0.1",
			TargetPort: controllerPort,
		}})
		err = f.awaitReady(30 * time.Second)
		if err == nil {
			break
		}
		f.stop()
		if !strings.Contains(err.Error(), syscall.EADDRINUSE.Error()) || attempt == 4 {
			t.Fatalf("waiting for forward ready: %v", err)
		}
	}
	defer f.stop()

	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", guestPort), 5*time.Second)
	if err != nil {
		t.Fatalf("dialing guest forwarded port: %v", err)
	}
	defer conn.Close()

	const payload = "ping-controller-service"
	if _, err := conn.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != payload {
		t.Fatalf("got %q, want %q", buf, payload)
	}

}

// TestForwardKubectlRefusesOpenOutsideRemotePorts stands a fake kubectl in
// for a hostile guest agent: it confirms the one listener the controller
// asks for, then hands the controller forwarded connections that name a
// listener it never asked for, that ask it to dial the metadata server,
// and that name the real listener while also naming a forbidden address.
// Only the last may be dialed, and only to the RemotePorts target.
func TestForwardKubectlRefusesOpenOutsideRemotePorts(t *testing.T) {
	t.Parallel()

	listen := func() (net.Listener, int, *atomic.Int32) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ln.Close() })
		var hits atomic.Int32
		go func() {
			for {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				hits.Add(1)
				_ = conn.Close()
			}
		}()
		return ln, ln.Addr().(*net.TCPAddr).Port, &hits
	}
	_, allowedPort, allowedHits := listen()
	_, forbiddenPort, forbiddenHits := listen()

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	result := filepath.Join(dir, "result")
	forbidden := fmt.Sprintf("127.0.0.1:%d", forbiddenPort)
	script := fmt.Sprintf("#!/bin/sh\n%s=%q %s=%q exec %q -test.run='^TestFakeKubectlForwardGuest$'\n",
		fakeKubectlTargetsEnv, forbidden, fakeKubectlResultEnv, result, exe)
	kubectl := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(kubectl, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err = ForwardKubectl(ctx, KubectlForwardConfig{
		Kubectl:     kubectl,
		Pod:         "grain-granule-1-1",
		RemotePorts: []int{allowedPort},
	})
	if ctx.Err() != nil {
		t.Fatalf("ForwardKubectl did not finish: %v", err)
	}

	got, rerr := os.ReadFile(result)
	if rerr != nil {
		t.Fatalf("fake kubectl left no result (ForwardKubectl: %v): %v", err, rerr)
	}
	if want := "rejected,rejected,rejected,confirmed"; string(got) != want {
		t.Errorf("replies to the fake guest's opens = %q, want %q", got, want)
	}
	if n := forbiddenHits.Load(); n != 0 {
		t.Errorf("the listener outside RemotePorts was connected to %d times", n)
	}
	if n := allowedHits.Load(); n != 1 {
		t.Errorf("the RemotePorts listener was connected to %d times, want 1", n)
	}
}

const (
	fakeKubectlTargetsEnv = "GRAIN_TEST_FAKE_KUBECTL_TARGETS"
	fakeKubectlResultEnv  = "GRAIN_TEST_FAKE_KUBECTL_RESULT"
)

// TestFakeKubectlForwardGuest is not a test: it is the guest end of
// TestForwardKubectlRefusesOpenOutsideRemotePorts, run as a child process by
// the fake kubectl that test writes. It serves the agent side of a
// guestforward session on its stdio, confirms the controller's listen,
// opens the hostile streams in turn, and writes whether the controller
// confirmed or rejected each to the result file.
func TestFakeKubectlForwardGuest(t *testing.T) {
	forbidden := os.Getenv(fakeKubectlTargetsEnv)
	if forbidden == "" {
		t.Skip("only run as the fake kubectl's guest")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sess := guestforward.NewSession(pipeConn{os.Stdin, os.Stdout}, false, guestforward.Config{})
	listenSt, err := sess.Accept(ctx)
	if err != nil {
		os.Exit(1)
	}
	var asked struct{ Addr string }
	_ = json.Unmarshal(listenSt.Meta(), &asked)
	reply, _ := json.Marshal(map[string]string{"addr": asked.Addr})
	if err := listenSt.Confirm(reply); err != nil {
		os.Exit(1)
	}
	open := func(v map[string]any) string {
		meta, _ := json.Marshal(v)
		st, err := sess.Open(ctx, meta)
		var rejected *guestforward.RejectedError
		switch {
		case errors.As(err, &rejected):
			return "rejected"
		case err != nil:
			os.Exit(1)
		}
		_ = st.Close()
		return "confirmed"
	}
	replies := []string{
		open(map[string]any{"op": "accepted", "listener": listenSt.ID() + 100, "addr": forbidden}),
		open(map[string]any{"op": "connect", "addr": "169.254.169.254:80"}),
		open(map[string]any{"op": "connect", "addr": forbidden}),
		open(map[string]any{"op": "accepted", "listener": listenSt.ID(), "addr": forbidden}),
	}
	if err := os.WriteFile(os.Getenv(fakeKubectlResultEnv), []byte(strings.Join(replies, ",")), 0o644); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

// forwardPair is a ServeForwardGuest and ServeForwardClient joined by
// pipes, as a sandbox and its controller are.
type forwardPair struct {
	cancel                context.CancelFunc
	c2gWriter, g2cWriter  *io.PipeWriter
	c2gReader, g2cReader  *io.PipeReader
	ready                 chan struct{}
	clientDone, guestDone chan error
	guestExited, stopped  bool
	clientExited          bool
}

func startForwardPair(remotes []ForwardSpec) *forwardPair {
	f := &forwardPair{ready: make(chan struct{}), clientDone: make(chan error, 1), guestDone: make(chan error, 1)}
	f.c2gReader, f.c2gWriter = io.Pipe()
	f.g2cReader, f.g2cWriter = io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	f.cancel = cancel
	go func() { f.guestDone <- ServeForwardGuest(ctx, f.c2gReader, f.g2cWriter, remotes) }()
	go func() {
		f.clientDone <- ServeForwardClient(ctx, f.g2cReader, f.c2gWriter, remotes, nil, func() { close(f.ready) })
	}()
	return f
}

// awaitReady waits for the client to report the forward ready, or for
// the guest to give up first (a port it could not bind), or for timeout.
func (f *forwardPair) awaitReady(timeout time.Duration) error {
	select {
	case <-f.ready:
		return nil
	case err := <-f.guestDone:
		f.guestExited = true
		if err == nil {
			err = errors.New("guest exited before the forward was ready")
		}
		return err
	case err := <-f.clientDone:
		f.clientExited = true
		if err == nil {
			err = errors.New("client exited before the forward was ready")
		}
		return err
	case <-time.After(timeout):
		return fmt.Errorf("no ready after %v", timeout)
	}
}

func (f *forwardPair) stop() {
	if f.stopped {
		return
	}
	f.stopped = true
	f.cancel()
	_ = f.c2gWriter.Close()
	_ = f.g2cWriter.Close()
	_ = f.c2gReader.Close()
	_ = f.g2cReader.Close()
	if !f.clientExited {
		<-f.clientDone
	}
	if !f.guestExited {
		<-f.guestDone
	}
}
