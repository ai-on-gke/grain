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

package hostd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// registry is a stand-in for the daemon's hostd registry: token to name
// and owner, with revocation.
type registry struct {
	mu     sync.Mutex
	tokens map[string][2]string
}

func (r *registry) resolve(_ context.Context, token string) (string, string, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	h, ok := r.tokens[token]
	return h[0], h[1], ok, nil
}

func (r *registry) revoke(token string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tokens, token)
}

func startRegistryController(t *testing.T) (*controller, *registry) {
	t.Helper()
	ctl := startController(t, "")
	reg := &registry{tokens: map[string][2]string{
		"tok-a": {"ws-a", "alice@example.com"},
		"tok-b": {"ws-b", ""},
	}}
	ctl.remote.Resolve = reg.resolve
	return ctl, reg
}

// startNamedRemoteServer is startRemoteServer for a hostd dialing in with
// a registry token, saying it is name ("" to say nothing).
func startNamedRemoteServer(t *testing.T, ctl *controller, token, name, extra string) *testServer {
	t.Helper()
	return startServer(t, extra, func(s *Server) {
		s.Remote = Dialer{Server: ctl.http.URL, Token: token, Name: name, Host: "machine-" + token, Build: "test-build"}.Dial
		s.RemoteMinBackoff = 20 * time.Millisecond
		s.RemoteMaxBackoff = 200 * time.Millisecond
	})
}

func waitHostd(t *testing.T, r *Remote, name string, connected bool) RemoteStatus {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		st := r.HostdStatus(name)
		if st.Connected == connected {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("hostd %s connected = %v, want %v: %+v", name, st.Connected, connected, st)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Two hostds dial in at once, each named by its own token, and each name
// reaches that hostd and no other.
func TestManyHostdsAreConnectedAtOnceNamedByTheirTokens(t *testing.T) {
	ctl, _ := startRegistryController(t)
	sh, _ := filepath.EvalSymlinks("/bin/sh")
	startNamedRemoteServer(t, ctl, "tok-a", "", fmt.Sprintf("capabilities:\n  only-on-a:\n    command: %s\n", sh))
	startNamedRemoteServer(t, ctl, "tok-b", "ws-b", fmt.Sprintf("capabilities:\n  only-on-b:\n    command: %s\n", sh))
	a := waitHostd(t, ctl.remote, "ws-a", true)
	b := waitHostd(t, ctl.remote, "ws-b", true)
	if a.Name != "ws-a" || a.Owner != "alice@example.com" || a.Host != "machine-tok-a" {
		t.Errorf("ws-a status = %+v, want its registry name, owner and instance", a)
	}
	if b.Name != "ws-b" || b.Host != "machine-tok-b" {
		t.Errorf("ws-b status = %+v", b)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for name, want := range map[string]string{"ws-a": "only-on-a", "ws-b": "only-on-b"} {
		offers, err := Client{Dial: ctl.remote.Dialer(name)}.Offers(ctx)
		if err != nil {
			t.Fatalf("offers from %s: %v", name, err)
		}
		var got []string
		for _, o := range offers {
			got = append(got, o.Name)
		}
		joined := strings.Join(got, ",")
		if !strings.Contains(joined, want) || strings.Contains(joined, map[string]string{"ws-a": "only-on-b", "ws-b": "only-on-a"}[name]) {
			t.Errorf("%s offers %s, want its own %s and not the other's", name, joined, want)
		}
	}
	if len(ctl.remote.Statuses()) != 2 {
		t.Errorf("statuses = %+v, want both hostds", ctl.remote.Statuses())
	}
	// There is no default with no shared token: nothing falls back to
	// whichever hostd happens to be connected.
	if _, err := ctl.remote.Dial(ctx); err == nil {
		t.Error("Dial with no default hostd reached one anyway")
	}
}

// A hostd that calls itself by a name its token is not is refused, and a
// token that names no hostd -- or one revoked -- is refused too.
func TestAHostdWhoseNameDisagreesWithItsTokenIsRefused(t *testing.T) {
	ctl, reg := startRegistryController(t)
	_, err := Dialer{Server: ctl.http.URL, Token: "tok-a", Name: "ws-b"}.Dial(context.Background())
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("dial as ws-b with ws-a's token = %v, want a 403 refusal", err)
	}
	if st := ctl.remote.HostdStatus("ws-b"); st.Connected {
		t.Fatal("a hostd refused for its name is listed as ws-b")
	}
	if st := ctl.remote.HostdStatus("ws-a"); st.Connected {
		t.Fatal("a hostd refused for its name is listed as ws-a")
	}
	reg.revoke("tok-a")
	if _, err := (Dialer{Server: ctl.http.URL, Token: "tok-a"}).Dial(context.Background()); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("dial with a revoked token = %v, want a 401 refusal", err)
	}
}

// A hostd reconnecting replaces only its own session; another hostd's
// stays as it was.
func TestOnlyTheSameHostdReplacesItsSession(t *testing.T) {
	ctl, _ := startRegistryController(t)
	startNamedRemoteServer(t, ctl, "tok-a", "", "")
	startNamedRemoteServer(t, ctl, "tok-b", "", "")
	waitHostd(t, ctl.remote, "ws-a", true)
	before := waitHostd(t, ctl.remote, "ws-b", true)

	// A second ws-a: it takes ws-a's place and leaves ws-b alone.
	startNamedRemoteServer(t, ctl, "tok-a", "ws-a", "")
	deadline := time.Now().Add(10 * time.Second)
	for ctl.remote.HostdStatus("ws-a").Sessions < 3 {
		if time.Now().After(deadline) {
			t.Fatal("the second ws-a never connected")
		}
		time.Sleep(10 * time.Millisecond)
	}
	after := ctl.remote.HostdStatus("ws-b")
	if !after.Connected || !after.Since.Equal(before.Since) {
		t.Errorf("ws-b after ws-a reconnected = %+v, want its session untouched (%+v)", after, before)
	}
}

// A hostd's hello carries its allowlist's capacity and labels, which is
// how the daemon learns them.
func TestHelloCarriesTheAllowlistsCapacityAndLabels(t *testing.T) {
	ts := startServer(t, "capacity: 2\nlabels:\n  os: linux\n", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	h, err := ts.client.Hello(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if h.Capacity != 2 || h.Labels["os"] != "linux" {
		t.Errorf("hello = %+v, want capacity 2 and os=linux", h)
	}
	if h.AgentsOnly {
		t.Errorf("hello = %+v, says agents-only for a hostd not started so", h)
	}
}

// A host agent started as agents-only (grain-agentd) says so in its
// hello, for the placement check refusing a run: host server there.
func TestHelloSaysAnAgentsOnlyHostAgentIsOne(t *testing.T) {
	ts := startServer(t, "", func(s *Server) { s.AgentsOnly = true })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	h, err := ts.client.Hello(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !h.AgentsOnly {
		t.Errorf("hello = %+v, want agents_only", h)
	}
}

// Disconnecting a revoked hostd closes its live session at once while
// leaving other hostds connected, and its retired token cannot reconnect.
func TestDisconnectDropsLiveSessionOfRevokedHostdOnly(t *testing.T) {
	ctl, reg := startRegistryController(t)
	startNamedRemoteServer(t, ctl, "tok-a", "", "")
	startNamedRemoteServer(t, ctl, "tok-b", "ws-b", "")
	waitHostd(t, ctl.remote, "ws-a", true)
	beforeB := waitHostd(t, ctl.remote, "ws-b", true)

	reg.revoke("tok-a")
	if !ctl.remote.Disconnect("ws-a") {
		t.Fatal("Disconnect(ws-a) = false, want true for a live session")
	}
	waitHostd(t, ctl.remote, "ws-a", false)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := ctl.remote.DialHostd(ctx, "ws-a"); err == nil {
		t.Fatal("DialHostd(ws-a) succeeded after revoke and Disconnect")
	}
	afterB := ctl.remote.HostdStatus("ws-b")
	if !afterB.Connected || !afterB.Since.Equal(beforeB.Since) {
		t.Errorf("ws-b after disconnecting ws-a = %+v, want untouched (%+v)", afterB, beforeB)
	}
	if ctl.remote.Disconnect("ws-a") {
		t.Error("second Disconnect(ws-a) = true, want false")
	}
}

// A run routed to one hostd of several carries its endpoints to that
// hostd's machine (Remote.Carrier), not the default hostd's -- with none
// configured, as on a registry-only daemon, the default carry fails --
// and a carried listener serves a client that keeps its connection
// alive across requests, as every CLI's HTTP client does: the second
// request on one connection used to be cancelled, a 502 behind the
// run's MCP reverse proxy (docs/test-runs/2026-11-03-hostd-routing.md).
func TestACarryGoesToTheNamedHostdAndServesKeepAliveRequests(t *testing.T) {
	ctl, _ := startRegistryController(t)
	startNamedRemoteServer(t, ctl, "tok-a", "ws-a", "")
	startNamedRemoteServer(t, ctl, "tok-b", "ws-b", "")
	waitHostd(t, ctl.remote, "ws-a", true)
	waitHostd(t, ctl.remote, "ws-b", true)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if _, _, err := ctl.remote.CarryHTTP(ctx, http.NotFoundHandler()); !errors.Is(err, ErrRemoteNotConnected) {
		t.Fatalf("CarryHTTP to the default hostd with none configured = %v, want ErrRemoteNotConnected", err)
	}
	// As the run's MCP reverse proxy does, a request whose context is
	// already cancelled -- its connection read as gone -- is a 502.
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if err := r.Context().Err(); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		_, _ = io.WriteString(w, "ok")
	})
	base, release, err := ctl.remote.Carrier("ws-a").CarryHTTP(ctx, ok)
	if err != nil {
		t.Fatalf("carrying to ws-a: %v", err)
	}
	defer release()

	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 1}}
	defer client.CloseIdleConnections()
	for i := range 3 {
		reused := false
		trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused }}
		req, _ := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), http.MethodPost, base+"/mcp", strings.NewReader("{}"))
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || string(body) != "ok" {
			t.Fatalf("request %d (connection reused: %v) = %d %q, want 200 ok", i, reused, resp.StatusCode, body)
		}
		if i > 0 && !reused {
			t.Errorf("request %d opened a new connection; want the kept-alive one", i)
		}
	}

	// The listener is ws-a's: ws-b going away leaves it, ws-a's does not.
	ctl.remote.Disconnect("ws-b")
	client.CloseIdleConnections()
	if resp, err := client.Post(base+"/mcp", "application/json", strings.NewReader("{}")); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("after ws-b disconnected: %v %v, want ws-a's listener still serving", resp, err)
	} else {
		resp.Body.Close()
	}
	ctl.remote.Disconnect("ws-a")
	client.CloseIdleConnections()
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := client.Post(base+"/mcp", "application/json", strings.NewReader("{}"))
		if err != nil {
			break
		}
		resp.Body.Close()
		if time.Now().After(deadline) {
			t.Fatal("ws-a's carried listener still serves after ws-a disconnected")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
