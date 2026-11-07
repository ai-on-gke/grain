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

package server

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/registry/api"
	"github.com/ai-on-gke/grain/pkg/registry/store"
	"github.com/ai-on-gke/grain/pkg/tunnel/mux"
	"github.com/ai-on-gke/grain/pkg/webauth"
)

// attachPipeTunnel registers on rt an ActiveTunnel for entry whose far end is
// a grain answering every request with "<label>:<method>:<body>", over an
// in-memory pipe.
func attachPipeTunnel(t *testing.T, rt *Router, entry *store.EntryRow, epoch int64, label string) *ActiveTunnel {
	t.Helper()
	serverConn, clientConn := net.Pipe()
	tun := NewActiveTunnel(serverConn, entry, epoch, "", api.AuthModeToken, TunnelOptions{})
	clientMux := mux.NewMux(clientConn, mux.Config{IsServer: false, MaxStreams: 256})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_, _ = io.WriteString(w, label+":"+r.Method+":"+string(body))
	})}
	go func() { _ = srv.Serve(api.NewMuxListener(clientMux)) }()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = clientMux.Close()
		_ = tun.CloseWithReason("test cleanup")
	})
	rt.registerActiveTunnel(tun, entry)
	return tun
}

// TestARequestCaughtByADrainFollowsTheTunnel covers the window a rolling
// restart is made of: routing has picked this replica's tunnel for a request,
// and before the request opens its stream the replica drains and lets the
// tunnel go so the grain can redial elsewhere. The request never reached the
// grain, so it must follow the tunnel through reconnect_grace -- to whichever
// replica the grain redials -- instead of being answered 503 by a replica that
// held the tunnel a moment ago.
func TestARequestCaughtByADrainFollowsTheTunnel(t *testing.T) {
	admitter, err := webauth.NewAdmitter(webauth.AdmissionConfig{
		Verifier:       testIdentityVerifier{},
		AllowedDomains: []string{"example.com"},
	})
	if err != nil {
		t.Fatalf("NewAdmitter: %v", err)
	}
	const grace = 5 * time.Second
	newRouter := func(t *testing.T) *Router {
		rt, err := NewRouter(RouterOptions{
			RegistryHost:   "registry.example.com",
			InstanceDomain: "instances.example.com",
			ReplicaID:      "replica-a",
			ReconnectGrace: grace,
			Admitter:       admitter,
			Signer:         newTestAssertionSigner(t),
		})
		if err != nil {
			t.Fatalf("NewRouter: %v", err)
		}
		t.Cleanup(rt.Close)
		return rt
	}
	entry := &store.EntryRow{ID: "entry-1", Name: "alice-tunnel", Kind: api.KindConnected, OwnerEmail: "alice@example.com"}

	// startCaughtRequest picks the local tunnel as routing does, drains it the
	// way BeginDrain does, and only then lets the request reach it.
	startCaughtRequest := func(t *testing.T, rt *Router, method, body string) (*httptest.ResponseRecorder, chan struct{}) {
		t.Helper()
		local := rt.GetLocalTunnel(entry.Name)
		if local == nil {
			t.Fatalf("expected a local tunnel before the drain")
		}
		local.DrainAndClose(0, "graceful shutdown")
		if rt.GetLocalTunnel(entry.Name) != nil {
			t.Fatalf("a drained tunnel must not be routable")
		}
		rec := httptest.NewRecorder()
		done := make(chan struct{})
		go func() {
			defer close(done)
			req := httptest.NewRequest(method, "https://alice-tunnel.instances.example.com/hello", strings.NewReader(body))
			req.Host = "alice-tunnel.instances.example.com"
			rt.serveLocalTunnel(rec, req, entry, false, local, time.Time{})
		}()
		deadline := time.Now().Add(5 * time.Second)
		for rt.TunnelCache().ActiveWaiters(entry.Name) == 0 {
			select {
			case <-done:
				t.Fatalf("request caught by the drain was answered instead of waiting for the redial: %d %s", rec.Code, rec.Body.String())
			case <-time.After(5 * time.Millisecond):
			}
			if time.Now().After(deadline) {
				t.Fatalf("request caught by the drain never entered reconnect_grace")
			}
		}
		return rec, done
	}
	waitDone := func(t *testing.T, done chan struct{}) {
		t.Helper()
		select {
		case <-done:
		case <-time.After(grace):
			t.Fatalf("request still waiting after the grain redialed")
		}
	}

	t.Run("RedialLandsOnAnotherReplica", func(t *testing.T) {
		rt := newRouter(t)
		attachPipeTunnel(t, rt, entry, 1, "old-tunnel")
		newPeer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			_, _ = io.WriteString(w, "replica-b:"+r.Method+":"+string(body))
		}))
		defer newPeer.Close()

		// A POST: its stream never opened, so its body is still unread and it
		// can follow the tunnel too, not only replayable requests.
		rec, done := startCaughtRequest(t, rt, http.MethodPost, "payload")
		rt.TunnelCache().SetActive(TunnelCacheEntry{Name: entry.Name, EntryID: entry.ID, SessionEpoch: 2, ReplicaID: "replica-b", PeerAddr: newPeer.Listener.Addr().String()})
		waitDone(t, done)
		if rec.Code != http.StatusOK || rec.Body.String() != "replica-b:POST:payload" {
			t.Fatalf("expected the caught request to reach the grain on replica-b, got %d (%s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("RedialLandsBackOnThisReplica", func(t *testing.T) {
		rt := newRouter(t)
		attachPipeTunnel(t, rt, entry, 1, "old-tunnel")

		rec, done := startCaughtRequest(t, rt, http.MethodGet, "")
		attachPipeTunnel(t, rt, entry, 2, "new-tunnel")
		waitDone(t, done)
		if rec.Code != http.StatusOK || rec.Body.String() != "new-tunnel:GET:" {
			t.Fatalf("expected the caught request to reach the grain over its new tunnel, got %d (%s)", rec.Code, rec.Body.String())
		}
	})
}

// TestADrainingTunnelRefusesStreamsItWouldAbort checks the other side of the
// drain: once DrainAndClose has begun, OpenStream refuses new streams rather
// than counting one the drain has already decided not to wait for.
func TestADrainingTunnelRefusesStreamsItWouldAbort(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	entry := &store.EntryRow{ID: "entry-1", Name: "alice-tunnel", Kind: api.KindConnected}
	tun := NewActiveTunnel(serverConn, entry, 1, "", api.AuthModeToken, TunnelOptions{})
	defer tun.CloseWithReason("test cleanup")

	tun.superseded.Store(true) // DrainAndClose's first step.
	if _, err := tun.OpenStream(t.Context()); !errors.Is(err, mux.ErrSessionClosed) {
		t.Fatalf("OpenStream on a draining tunnel = %v, want mux.ErrSessionClosed", err)
	}
	if n := tun.ActiveStreams(); n != 0 {
		t.Fatalf("a refused stream must not stay counted: ActiveStreams = %d", n)
	}
	if !tun.Gone() {
		t.Fatalf("a draining tunnel must report Gone")
	}
}

// TestAPeerWaitsOutAHandshakeThatHasAcquiredItsSession covers the other end of
// a redial: the new replica's /connect handshake acquires the session in
// PostgreSQL -- whose NOTIFY tells every peer to forward here -- a moment
// before it registers the tunnel. A forwarded request that lands in between
// must wait for the tunnel, not be told it is gone: the forwarding replica
// would then forget the session it was just told about and, with no further
// NOTIFY coming, hold the request for its whole reconnect_grace and answer 503.
func TestAPeerWaitsOutAHandshakeThatHasAcquiredItsSession(t *testing.T) {
	admitter, err := webauth.NewAdmitter(webauth.AdmissionConfig{
		Verifier:       testIdentityVerifier{},
		AllowedDomains: []string{"example.com"},
	})
	if err != nil {
		t.Fatalf("NewAdmitter: %v", err)
	}
	rt, err := NewRouter(RouterOptions{
		RegistryHost:   "registry.example.com",
		InstanceDomain: "instances.example.com",
		ReplicaID:      "replica-b",
		ReconnectGrace: 5 * time.Second,
		Admitter:       admitter,
		Signer:         newTestAssertionSigner(t),
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	defer rt.Close()
	entry := &store.EntryRow{ID: "entry-1", Name: "alice-tunnel", Kind: api.KindConnected, OwnerEmail: "alice@example.com"}
	peerToken, err := rt.peerTokens.GetToken(t.Context())
	if err != nil {
		t.Fatalf("GetToken: %v", err)
	}
	newPeerRequest := func() *http.Request {
		req := httptest.NewRequest(http.MethodGet, "http://peer.internal/internal/tunnel/alice-tunnel", nil)
		req.Host = "alice-tunnel.instances.example.com"
		req.Header.Set(HeaderPeerAuth, peerToken)
		req.Header.Set(HeaderRegistryHop, "1")
		req.Header.Set(HeaderTargetURI, "/hello")
		return req
	}

	// With no handshake in flight, a replica without the tunnel says so at once.
	rec := httptest.NewRecorder()
	rt.PeerHandler().ServeHTTP(rec, newPeerRequest())
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get(HeaderTunnelGone) != "1" {
		t.Fatalf("expected tunnel_gone with no tunnel and no handshake, got %d (%s)", rec.Code, rec.Body.String())
	}

	finishHandshake := rt.beginHandshake(entry.Name)
	defer finishHandshake()
	rec = httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		rt.PeerHandler().ServeHTTP(rec, newPeerRequest())
	}()
	select {
	case <-done:
		t.Fatalf("forwarded request answered while the handshake was still registering the tunnel: %d (%s)", rec.Code, rec.Body.String())
	case <-time.After(50 * time.Millisecond):
	}
	attachPipeTunnel(t, rt, entry, 2, "new-tunnel")
	finishHandshake()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("forwarded request still waiting after the tunnel registered")
	}
	if rec.Code != http.StatusOK || rec.Body.String() != "new-tunnel:GET:" {
		t.Fatalf("expected the forwarded request to reach the newly registered tunnel, got %d (%s)", rec.Code, rec.Body.String())
	}
}
