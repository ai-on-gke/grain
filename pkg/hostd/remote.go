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
var ErrRemoteNotConnected = errors.New("no grain hostd is connected to this daemon")

// DefaultSharedName is the hostd the shared token (Remote.Token) names
// when Remote.SharedName does not say.
const DefaultSharedName = "remote"

// Remote is the controller's end of the hostds that dial out to it (hostd
// -server): the daemon serves RemotePath, hands each upgraded connection
// to Serve, and gives Client.Dial a Dialer's Dial, which opens a stream
// over that hostd's session in place of a connection to the socket.
//
// Any number of hostds are connected at once, each under the name its
// token belongs to -- never the name it gives itself, which only has to
// agree (NameHeader). A new connection replaces the one before it only
// for the same name, which is what a reconnect after a drop looks like
// from here; no hostd can displace another's session.
type Remote struct {
	// Token is the shared token of the deprecated -hostd-token-file: it
	// names the one hostd SharedName. Kept for one release beside the
	// registry's per-hostd tokens (Resolve).
	Token string
	// SharedName is the hostd Token names; DefaultSharedName when "".
	SharedName string
	// Resolve is the hostd registry: the name and owner of the hostd a
	// token belongs to, ok false for a token that names none (never
	// minted, rotated away, or its hostd revoked).
	Resolve func(ctx context.Context, token string) (name, owner string, ok bool, err error)
	// Default is the hostd Dial reaches; SharedName's when "" and Token is
	// set.
	Default string
	// Logf logs connects, drops and refused tokens; nothing when nil.
	Logf func(format string, args ...any)
	// OnChange, when set, is told each time a hostd connects or drops.
	OnChange func(name string, connected bool)
	// now is time.Now, or a test's clock for refusal's rate limit.
	now func() time.Time

	mu       sync.Mutex
	cur      map[string]*remoteSession
	arrived  chan struct{}
	last     map[string]RemoteStatus
	sessions int

	// refusedAt is when a refused hostd was last logged, and refused how
	// many have been refused since without a line of their own.
	refusedAt time.Time
	refused   int
}

// refusalLogEvery is how often at most a refused hostd is logged: a
// hostd with the wrong token retries every few seconds, and one line a
// minute says so without filling the daemon's log.
const refusalLogEvery = time.Minute

// RemotePeer is what a hostd said about itself when it connected, and
// who its token says it is.
type RemotePeer struct {
	// Name is the registry hostd the connection's token belongs to.
	Name string `json:"name,omitempty"`
	// Owner is that hostd's registry owner, "" for none.
	Owner string `json:"owner,omitempty"`
	// Host is the machine the hostd runs on (HostHeader): its instance.
	Host     string `json:"host,omitempty"`
	Build    string `json:"build,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	Addr     string `json:"addr,omitempty"`
}

// RemoteStatus is Settings' view of a hostd that dials in.
type RemoteStatus struct {
	Connected bool `json:"connected"`
	RemotePeer
	// Since is when the connected hostd connected, or when the last one
	// dropped.
	Since time.Time `json:"since,omitzero"`
	// Sessions counts the connections taken since the daemon started,
	// from every hostd.
	Sessions int `json:"sessions"`
}

type remoteSession struct {
	m    *mux.Mux
	peer RemotePeer
}

func (r *Remote) sharedName() string {
	if r.SharedName != "" {
		return r.SharedName
	}
	return DefaultSharedName
}

// DefaultName is the hostd Dial reaches.
func (r *Remote) DefaultName() string {
	if r == nil {
		return ""
	}
	if r.Default != "" {
		return r.Default
	}
	if r.Token != "" {
		return r.sharedName()
	}
	return ""
}

// Authorize reports whether token is the shared hostd token. With no
// Token configured nothing is let in by it.
func (r *Remote) Authorize(token string) bool {
	if r == nil || r.Token == "" || token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(r.Token)) == 1
}

// Identify is the hostd token belongs to: the shared token's, or the
// registry's. ok is false for a token that names none.
func (r *Remote) Identify(ctx context.Context, token string) (name, owner string, ok bool, err error) {
	if r == nil || token == "" {
		return "", "", false, nil
	}
	if r.Authorize(token) {
		return r.sharedName(), "", true, nil
	}
	if r.Resolve == nil {
		return "", "", false, nil
	}
	return r.Resolve(ctx, token)
}

func (r *Remote) logf(format string, args ...any) {
	if r.Logf != nil {
		r.Logf(format, args...)
	}
}

// Serve runs conn as the session to the hostd peer.Name until it drops
// or ctx is done, replacing any session that same hostd had before it.
func (r *Remote) Serve(ctx context.Context, conn net.Conn, peer RemotePeer) {
	if peer.Name == "" {
		peer.Name = r.sharedName()
	}
	m := mux.NewMux(conn, mux.Config{
		IsServer:     true,
		PingInterval: 15 * time.Second,
		PingTimeout:  15 * time.Second,
	})
	defer m.Close()
	sess := &remoteSession{m: m, peer: peer}
	r.mu.Lock()
	if r.cur == nil {
		r.cur = map[string]*remoteSession{}
	}
	if r.last == nil {
		r.last = map[string]RemoteStatus{}
	}
	prev := r.cur[peer.Name]
	r.cur[peer.Name] = sess
	r.sessions++
	r.last[peer.Name] = RemoteStatus{Connected: true, RemotePeer: peer, Since: time.Now(), Sessions: r.sessions}
	if r.arrived != nil {
		close(r.arrived)
		r.arrived = nil
	}
	r.mu.Unlock()
	if prev != nil {
		_ = prev.m.Close()
	}
	r.logf("grain daemon: grain hostd %s on %s (build %s) connected from %s", peer.Name, peer.Host, peer.Build, peer.Addr)
	if r.OnChange != nil {
		r.OnChange(peer.Name, true)
	}
	select {
	case <-ctx.Done():
	case <-m.Done():
	}
	r.mu.Lock()
	dropped := r.cur[peer.Name] == sess
	if dropped {
		delete(r.cur, peer.Name)
		st := r.last[peer.Name]
		st.Connected = false
		st.Since = time.Now()
		r.last[peer.Name] = st
	}
	r.mu.Unlock()
	r.logf("grain daemon: grain hostd %s on %s disconnected", peer.Name, peer.Host)
	if dropped && r.OnChange != nil {
		r.OnChange(peer.Name, false)
	}
}

// Dial opens a stream to the default hostd (DefaultName), which serves it
// as one socket connection. With it not connected it is
// ErrRemoteNotConnected.
func (r *Remote) Dial(ctx context.Context) (net.Conn, error) {
	return r.DialHostd(ctx, r.DefaultName())
}

// DialHostd is Dial to the hostd named name. Another hostd being
// connected is no answer: there is no fallback from one to another.
func (r *Remote) DialHostd(ctx context.Context, name string) (net.Conn, error) {
	r.mu.Lock()
	sess := r.cur[name]
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

// Dialer is DialHostd for name, in Client.Dial's shape.
func (r *Remote) Dialer(name string) func(context.Context) (net.Conn, error) {
	return func(ctx context.Context) (net.Conn, error) { return r.DialHostd(ctx, name) }
}

// WaitConnected blocks until the default hostd is connected or ctx is
// done.
func (r *Remote) WaitConnected(ctx context.Context) error {
	return r.WaitHostd(ctx, r.DefaultName())
}

// WaitHostd blocks until the hostd named name is connected or ctx is
// done.
func (r *Remote) WaitHostd(ctx context.Context, name string) error {
	for {
		r.mu.Lock()
		if r.cur[name] != nil {
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

// Status is whether the default hostd is connected, and which.
func (r *Remote) Status() RemoteStatus {
	return r.HostdStatus(r.DefaultName())
}

// HostdStatus is whether the hostd named name is connected.
func (r *Remote) HostdStatus(name string) RemoteStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.last[name]
	out.Sessions = r.sessions
	return out
}

// Statuses is every hostd that has connected since the daemon started,
// by name, whether or not it is connected now.
func (r *Remote) Statuses() map[string]RemoteStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]RemoteStatus, len(r.last))
	for name, st := range r.last {
		st.Sessions = r.sessions
		out[name] = st
	}
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

// logRefused logs a hostd refused for its token, at most once every
// refusalLogEvery, so an operator watching the daemon can tell a hostd
// with the wrong token from no hostd at all -- otherwise only the hostd's
// own log says so.
func (r *Remote) logRefused(req *http.Request, why string) {
	now := time.Now
	if r.now != nil {
		now = r.now
	}
	t := now()
	r.mu.Lock()
	if !r.refusedAt.IsZero() && t.Sub(r.refusedAt) < refusalLogEvery {
		r.refused++
		r.mu.Unlock()
		return
	}
	more := r.refused
	r.refusedAt, r.refused = t, 0
	r.mu.Unlock()
	host := req.Header.Get(HostHeader)
	if host == "" {
		host = "an unnamed host"
	}
	since := ""
	if more > 0 {
		since = fmt.Sprintf(" (%d more refused since the last line like this)", more)
	}
	r.logf("grain daemon: refused a grain hostd on %s connecting from %s: %s%s", host, req.RemoteAddr, why, since)
}

// ServeHTTP takes a hostd's upgrade of RemotePath (Dialer.Dial): it is
// refused unless its token names a hostd (Identify), and refused again if
// it calls itself any other (NameHeader); it is otherwise answered 101
// and served as the session to that hostd until it drops.
func (r *Remote) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	tok := RequestToken(req)
	name, owner, ok, err := r.Identify(req.Context(), tok)
	if err != nil {
		r.logf("grain daemon: looking up a grain hostd's token: %v", err)
		http.Error(w, "grain hostd: this controller could not check that hostd token", http.StatusServiceUnavailable)
		return
	}
	if !ok {
		why := "it presented a token that is not a hostd token here (Settings -> Hostds, or -hostd-token-file)"
		if tok == "" {
			why = "it presented no hostd token"
		}
		r.logRefused(req, why)
		http.Error(w, "grain hostd: this controller does not take that hostd token", http.StatusUnauthorized)
		return
	}
	if claimed := strings.TrimSpace(req.Header.Get(NameHeader)); claimed != "" && claimed != name {
		r.logRefused(req, fmt.Sprintf("it calls itself hostd %q, but its token is hostd %q's", claimed, name))
		http.Error(w, fmt.Sprintf("grain hostd: this token belongs to hostd %q, not %q", name, claimed), http.StatusForbidden)
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
		Name:     name,
		Owner:    owner,
		Host:     req.Header.Get(HostHeader),
		Build:    req.Header.Get(BuildHeader),
		Protocol: req.Header.Get(ProtocolHeader),
		Addr:     req.RemoteAddr,
	})
}
