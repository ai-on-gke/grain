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
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/tunnel/mux"
	"github.com/ai-on-gke/grain/pkg/tunnel/wsupgrade"
)

// ErrRemoteNotConnected is Remote.Dial's error while no hostd has dialed
// in: Client.dial waits it out for a moment, as it does a restarting
// hostd's missing socket.
var ErrRemoteNotConnected = errors.New("no grain hostd is connected to this controller")

// Remote is the controller's end of a hostd that dials out to it (hostd
// -server): the daemon serves RemotePath, hands each upgraded connection
// to Serve, and gives Client.Dial its Dial, which opens a stream over
// the session in place of a connection to the socket. One hostd is
// connected at a time; a new one replaces the one before it, which is
// what a reconnect after a drop looks like from here.
type Remote struct {
	// Token is the shared token a hostd must present (Authorize).
	Token string
	// Logf logs connects and drops; nothing when nil.
	Logf func(format string, args ...any)

	mu       sync.Mutex
	cur      *remoteSession
	arrived  chan struct{}
	last     RemoteStatus
	sessions int
}

// RemotePeer is what a hostd said about itself when it connected.
type RemotePeer struct {
	Host     string `json:"host,omitempty"`
	Build    string `json:"build,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	Addr     string `json:"addr,omitempty"`
}

// RemoteStatus is Settings' view of the hostd that dials in.
type RemoteStatus struct {
	Connected bool `json:"connected"`
	RemotePeer
	// Since is when the connected hostd connected, or when the last one
	// dropped.
	Since time.Time `json:"since,omitzero"`
	// Sessions counts the connections taken since the daemon started.
	Sessions int `json:"sessions"`
}

type remoteSession struct {
	m    *mux.Mux
	peer RemotePeer
}

// Authorize reports whether token is the shared hostd token. With no
// Token configured nothing is let in.
func (r *Remote) Authorize(token string) bool {
	if r == nil || r.Token == "" || token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(r.Token)) == 1
}

func (r *Remote) logf(format string, args ...any) {
	if r.Logf != nil {
		r.Logf(format, args...)
	}
}

// Serve runs conn as the session to the connected hostd until it drops
// or ctx is done, replacing any hostd connected before it.
func (r *Remote) Serve(ctx context.Context, conn net.Conn, peer RemotePeer) {
	m := mux.NewMux(conn, mux.Config{
		IsServer:     true,
		PingInterval: 15 * time.Second,
		PingTimeout:  15 * time.Second,
	})
	defer m.Close()
	sess := &remoteSession{m: m, peer: peer}
	r.mu.Lock()
	prev := r.cur
	r.cur = sess
	r.sessions++
	r.last = RemoteStatus{Connected: true, RemotePeer: peer, Since: time.Now(), Sessions: r.sessions}
	if r.arrived != nil {
		close(r.arrived)
		r.arrived = nil
	}
	r.mu.Unlock()
	if prev != nil {
		_ = prev.m.Close()
	}
	r.logf("grain daemon: grain hostd on %s (build %s) connected from %s", peer.Host, peer.Build, peer.Addr)
	select {
	case <-ctx.Done():
	case <-m.Done():
	}
	r.mu.Lock()
	if r.cur == sess {
		r.cur = nil
		r.last.Connected = false
		r.last.Since = time.Now()
	}
	r.mu.Unlock()
	r.logf("grain daemon: grain hostd on %s disconnected", peer.Host)
}

// Dial opens a stream to the connected hostd, which serves it as one
// socket connection. With none connected it is ErrRemoteNotConnected.
func (r *Remote) Dial(ctx context.Context) (net.Conn, error) {
	r.mu.Lock()
	sess := r.cur
	r.mu.Unlock()
	if sess == nil {
		return nil, ErrRemoteNotConnected
	}
	st, err := sess.m.OpenStream(ctx)
	if err != nil {
		return nil, ErrRemoteNotConnected
	}
	return st, nil
}

// WaitConnected blocks until a hostd is connected or ctx is done.
func (r *Remote) WaitConnected(ctx context.Context) error {
	for {
		r.mu.Lock()
		if r.cur != nil {
			r.mu.Unlock()
			return nil
		}
		if r.arrived == nil {
			r.arrived = make(chan struct{})
		}
		ch := r.arrived
		r.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ch:
		}
	}
}

// Status is whether a hostd is connected, and which.
func (r *Remote) Status() RemoteStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.last
	out.Sessions = r.sessions
	return out
}

// RequestToken is the hostd token r presents: TokenHeader, or else
// Authorization's bearer.
func RequestToken(r *http.Request) string {
	if tok := strings.TrimSpace(r.Header.Get(TokenHeader)); tok != "" {
		return tok
	}
	if tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return strings.TrimSpace(tok)
	}
	return ""
}

// ServeHTTP takes a hostd's upgrade of RemotePath (Dialer.Dial): it is
// refused unless it presents Token, and is otherwise answered 101 and
// served as the session to that hostd until it drops.
func (r *Remote) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if !r.Authorize(RequestToken(req)) {
		http.Error(w, "grain hostd: this controller does not take that hostd token", http.StatusUnauthorized)
		return
	}
	if !strings.EqualFold(req.Header.Get("Upgrade"), wsupgrade.Protocol) {
		http.Error(w, "grain hostd: expected a WebSocket upgrade", http.StatusBadRequest)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "grain hostd: this server cannot take over the connection", http.StatusInternalServerError)
		return
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	if _, err := fmt.Fprintf(brw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: %s\r\nConnection: Upgrade\r\n%s\r\n",
		wsupgrade.Protocol, wsupgrade.AcceptHeader(req)); err != nil {
		return
	}
	if err := brw.Flush(); err != nil {
		return
	}
	var c net.Conn = conn
	if brw.Reader.Buffered() > 0 {
		c = &bufferedConn{Conn: conn, r: io.MultiReader(brw.Reader, conn)}
	}
	r.Serve(req.Context(), c, RemotePeer{
		Host:     req.Header.Get(HostHeader),
		Build:    req.Header.Get(BuildHeader),
		Protocol: req.Header.Get(ProtocolHeader),
		Addr:     req.RemoteAddr,
	})
}
