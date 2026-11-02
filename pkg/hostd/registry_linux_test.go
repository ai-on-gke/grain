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
	"fmt"
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
}
