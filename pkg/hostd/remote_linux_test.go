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
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// controller is a stand-in for a daemon that a hostd dials out to: an
// HTTP server taking RemotePath through a Remote, whose connections a
// test can drop.
type controller struct {
	remote *Remote
	http   *httptest.Server

	mu    sync.Mutex
	conns []net.Conn
}

func startController(t *testing.T, token string) *controller {
	t.Helper()
	c := &controller{remote: &Remote{Token: token, Logf: t.Logf}}
	mux := http.NewServeMux()
	mux.Handle("GET "+RemotePath, c.remote)
	c.http = httptest.NewUnstartedServer(mux)
	c.http.Config.ConnState = func(conn net.Conn, st http.ConnState) {
		if st == http.StateHijacked {
			c.mu.Lock()
			c.conns = append(c.conns, conn)
			c.mu.Unlock()
		}
	}
	c.http.Start()
	t.Cleanup(c.http.Close)
	return c
}

// drop cuts every hostd session the controller holds, as a network
// blip or a controller pod restart would.
func (c *controller) drop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, conn := range c.conns {
		_ = conn.Close()
	}
	c.conns = nil
}

func (c *controller) client() Client { return Client{Dial: c.remote.Dial} }

// startRemoteServer is startServer with the hostd dialing out to ctl as
// well as listening on its socket.
func startRemoteServer(t *testing.T, ctl *controller, token, extra string) *testServer {
	t.Helper()
	ts := startServer(t, extra, func(s *Server) {
		s.Remote = Dialer{Server: ctl.http.URL, Token: token, Host: "workstation", Build: "test-build"}.Dial
		s.RemoteMinBackoff = 20 * time.Millisecond
		s.RemoteMaxBackoff = 200 * time.Millisecond
	})
	waitConnected(t, ctl.remote, 1)
	return ts
}

// waitConnected waits for ctl to have taken its n-th hostd session.
func waitConnected(t *testing.T, r *Remote, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		st := r.Status()
		if st.Connected && st.Sessions >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("hostd never made session %d to the controller: %+v", n, st)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A hostd that dials out serves the controller what it serves on its
// socket -- its offers, a CLI's stdio and exit, a run: host capability
// server's stdio -- in the same frame protocol, over streams of the one
// session.
func TestAHostdDialingOutServesTheControllerOverTheSession(t *testing.T) {
	ctl := startController(t, "s3cret")
	sh, _ := filepath.EvalSymlinks("/bin/sh")
	ts := startRemoteServer(t, ctl, "s3cret",
		fmt.Sprintf("capabilities:\n  cap:\n    command: %s\n    args: [-c, \"read line; echo got:$line\"]\n", sh))
	c := ctl.client()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	st := ctl.remote.Status()
	if st.Host != "workstation" || st.Build != "test-build" || st.Protocol == "" {
		t.Errorf("status = %+v, want the hostd's host, build and protocol", st)
	}
	offers, err := c.Offers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, o := range offers {
		names = append(names, string(o.Kind)+" "+o.Name)
	}
	if got := strings.Join(names, ","); !strings.Contains(got, "cli sh") || !strings.Contains(got, "capability cap") {
		t.Errorf("offers = %s, want cli sh and capability cap", got)
	}
	if h, err := c.Hello(ctx); err != nil || h.Protocol != ProtocolVersion {
		t.Errorf("hello = %+v, %v", h, err)
	}

	var out, errb bytes.Buffer
	exit, err := c.Run(ctx, Request{Kind: KindCLI, Name: "sh", Args: []string{"-c", "cat; echo oops >&2; exit 3"}},
		strings.NewReader("hello\n"), &out, &errb)
	if err != nil {
		t.Fatal(err)
	}
	if exit.Code != 3 || out.String() != "hello\n" || errb.String() != "oops\n" {
		t.Errorf("run = %+v stdout %q stderr %q", exit, out.String(), errb.String())
	}

	out.Reset()
	exit, err = c.Run(ctx, Request{Kind: KindCapability, Name: "cap"}, strings.NewReader("ping\n"), &out, nil)
	if err != nil || exit.Code != 0 || out.String() != "got:ping\n" {
		t.Errorf("capability server = %+v, %v, stdout %q", exit, err, out.String())
	}

	// The socket is still served beside the session.
	if _, o, _, err := ts.sh(ctx, "echo local", ""); err != nil || o != "local\n" {
		t.Errorf("socket run = %q, %v", o, err)
	}
}

// A detached run started over the session outlives the controller that
// started it, and is listed and re-attached by the next one.
func TestADetachedRunOverTheSessionIsReattachedAfterTheControllerGoes(t *testing.T) {
	ctl := startController(t, "s3cret")
	startRemoteServer(t, ctl, "s3cret", "")
	started := make(chan Started, 1)
	c := ctl.client()
	c.Detach = func(context.Context) bool { return true }
	c.OnStarted = func(s Started) { started <- s }
	ctx, cancel := context.WithCancel(context.Background())
	var first bytes.Buffer
	done := make(chan error, 1)
	go func() {
		_, err := c.Run(ctx, Request{Kind: KindCLI, Name: "sh", Args: []string{"-c", steady(30)},
			Detach: true, Run: "run-1", Task: "7"}, nil, &first, nil)
		done <- err
	}()
	st := <-started
	time.Sleep(300 * time.Millisecond)
	cancel()
	if err := <-done; !errors.Is(err, ErrDetached) {
		t.Fatalf("Run = %v, want ErrDetached", err)
	}
	got := first.String()
	ctl.drop()
	waitConnected(t, ctl.remote, 2)

	next := ctl.client()
	procs, err := next.Processes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(procs) != 1 || procs[0].ID != st.ID || procs[0].Run != "run-1" {
		t.Fatalf("processes = %+v, want the detached run", procs)
	}
	var rest bytes.Buffer
	exit, _, err := next.Attach(context.Background(), st.ID, Cursor{Stdout: int64(len(got))}, &rest, nil)
	if err != nil || exit.Code != 0 {
		t.Fatalf("attach = %+v, %v", exit, err)
	}
	if whole := got + rest.String(); whole != wantLines(30) {
		t.Errorf("read %q, want every line once", whole)
	}
}

// A dropped session is made again, and the controller's requests work
// over the new one; one made while the hostd is reconnecting waits for
// it rather than failing.
func TestAHostdReconnectsAfterTheSessionDrops(t *testing.T) {
	ctl := startController(t, "s3cret")
	startRemoteServer(t, ctl, "s3cret", "")
	c := ctl.client()
	for i := 2; i <= 4; i++ {
		ctl.drop()
		// Until the controller sees the old session end, a request may
		// still be put on it and fail with it, as one in flight does.
		for st := ctl.remote.Status(); st.Connected && st.Sessions < i; st = ctl.remote.Status() {
			time.Sleep(time.Millisecond)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		var out bytes.Buffer
		exit, err := c.Run(ctx, Request{Kind: KindCLI, Name: "sh", Args: []string{"-c", "echo again"}}, nil, &out, nil)
		cancel()
		if err != nil || exit.Code != 0 || out.String() != "again\n" {
			t.Fatalf("after drop %d: run = %+v, %v, %q", i-1, exit, err, out.String())
		}
		waitConnected(t, ctl.remote, i)
	}
}

// A hostd that has been retrying through a long controller outage still
// redials well inside the window a restarted controller waits for its
// hello (dialRetry) before giving up on adopting the host runs it left.
// With the backoff capped at 30s, the same as that window, a daemon
// restarted after a ~40s outage met its hostd only 18s after it was
// serving (docs/test-runs/2026-10-29-hostd-dial-out-model-server.md).
func TestAHostdRedialsInsideTheControllersWaitForIt(t *testing.T) {
	if got := 2 * defaultRemoteMaxBackoff; got > dialRetry {
		t.Fatalf("twice the default reconnect backoff (%s) is longer than the controller's wait for a hostd (%s)", got, dialRetry)
	}
	var mu sync.Mutex
	var at []time.Time
	srv := &Server{
		Remote: func(ctx context.Context) (net.Conn, error) {
			mu.Lock()
			at = append(at, time.Now())
			mu.Unlock()
			return nil, errors.New("connection refused")
		},
		RemoteMinBackoff: 3 * time.Second,
		Logf:             func(string, ...any) {},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { srv.serveRemote(ctx); close(done) }()
	// 3s doubles to 6s, past the 5s cap: the third attempt comes 5s
	// after the second.
	deadline := time.Now().Add(15 * time.Second)
	for {
		mu.Lock()
		n := len(at)
		mu.Unlock()
		if n >= 3 || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-done
	mu.Lock()
	defer mu.Unlock()
	if len(at) < 3 {
		t.Fatalf("only %d attempts in 15s", len(at))
	}
	if gap := at[2].Sub(at[1]); gap > defaultRemoteMaxBackoff+500*time.Millisecond {
		t.Errorf("the third attempt came %s after the second, want at most %s", gap, defaultRemoteMaxBackoff)
	}
}

// A controller that does not take the hostd's token refuses it, and
// nothing reaches the hostd; with none connected a request says so.
func TestAControllerRefusesAHostdWithTheWrongToken(t *testing.T) {
	ctl := startController(t, "s3cret")
	_, err := Dialer{Server: ctl.http.URL, Token: "wrong"}.Dial(context.Background())
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("dial with the wrong token = %v, want a 401 refusal", err)
	}
	if ctl.remote.Status().Connected {
		t.Fatal("a refused hostd is listed as connected")
	}
	old := dialRetry
	dialRetry = 200 * time.Millisecond
	defer func() { dialRetry = old }()
	_, err = ctl.client().Offers(context.Background())
	if !errors.Is(err, ErrRemoteNotConnected) || !strings.Contains(err.Error(), "-server") {
		t.Fatalf("offers with no hostd connected = %v", err)
	}
}

// A hostd may serve only its session, with no socket at all.
func TestAHostdServesWithNoSocket(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("hostd refuses to run as root")
	}
	ctl := startController(t, "s3cret")
	allow := filepath.Join(t.TempDir(), "hostd.yaml")
	if err := os.WriteFile(allow, []byte("clis: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := &Server{
		Allowlist:       allow,
		AllowlistOwners: []uint32{uint32(os.Getuid())},
		Remote:          Dialer{Server: ctl.http.URL, Token: "s3cret"}.Dial,
		Logf:            t.Logf,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	waitConnected(t, ctl.remote, 1)
	if _, err := ctl.client().Offers(context.Background()); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return")
	}
	if err := (&Server{Allowlist: allow, AllowlistOwners: []uint32{uint32(os.Getuid())}}).Serve(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "nothing to serve") {
		t.Fatalf("Serve with neither a socket nor a server = %v", err)
	}
}
