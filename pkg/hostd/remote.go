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
var ErrRemoteNotConnected = errors.New("no host agent is connected to this daemon")

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
	// SharedOwner is the owner of the hostd Token names (-hostd-token-owner),
	// "" for none: what a remote connection with Token must be verified as.
	SharedOwner string
	// Resolve is the hostd registry: the name and owner of the hostd a
	// token belongs to, ok false for a token that names none (never
	// minted, rotated away, or its hostd revoked).
	Resolve func(ctx context.Context, token string) (name, owner string, ok bool, err error)
	// ResolveCredential, when set, extends Resolve with the host's bound
	// Ed25519 public key ("" for legacy per-host tokens) so ServeHTTP can
	// enforce proof-of-possession on every connection.
	ResolveCredential func(ctx context.Context, token string) (ResolvedCredential, bool, error)
	// Renew, when set, silently extends a connected host's credential for
	// another 30-day window over OpRenew.
	Renew func(ctx context.Context, name, currentToken string) (newToken string, expiresAt time.Time, err error)
	// OnCheckin, when set, records check-in metadata when a host connects.
	OnCheckin func(ctx context.Context, peer RemotePeer)
	// Default is the hostd Dial reaches; SharedName's when "" and Token is
	// set.
	Default string
	// Logf logs connects, drops and refused tokens; nothing when nil.
	Logf func(format string, args ...any)
	// OnChange, when set, is told each time a hostd connects or drops.
	OnChange func(name string, connected bool)
	// now is time.Now, or a test's clock for refusal's rate limit.
	now func() time.Time

	replay NonceReplayCache

	mu       sync.Mutex
	cur      map[string]*remoteSession
	arrived  chan struct{}
	last     map[string]RemoteStatus
	sessions int

	// refusals is when each kind of refused hostd (refusalKey) was last
	// logged, and how many of that kind have been refused since without a
	// line of their own.
	refusals map[string]*refusalLog
}

type refusalLog struct {
	at   time.Time
	more int
}

// maxRefusalKeys bounds Remote.refusals: past it, any new kind of refusal
// shares one line a minute with every other kind past it.
const maxRefusalKeys = 64

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
	// Principal is the verified identity the connection came in as
	// (ConnectIdentity), "" for a local hostd named by its token alone.
	Principal string `json:"principal,omitempty"`
	// Host is the machine the hostd runs on (HostHeader): its instance.
	Host     string `json:"host,omitempty"`
	Build    string `json:"build,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	Addr     string `json:"addr,omitempty"`
	// Logins are the agent CLI logins on the hostd's machine, from the
	// hello the daemon asks it for once it has connected (Hello.Logins,
	// learnLogins). LoginsKnown says that hello was answered, so an
	// empty list reads as "none" rather than "not asked yet" or "a hostd
	// too old to say".
	Logins      []string `json:"logins,omitempty"`
	LoginsKnown bool     `json:"loginsKnown,omitempty"`
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

// ResolvedCredential describes the registry host a credential belongs to.
type ResolvedCredential struct {
	Name      string
	Owner     string
	PublicKey string
	ExpiresAt time.Time
}

type remoteSession struct {
	r         *Remote
	m         *mux.Mux
	peer      RemotePeer
	publicKey string
	carries   carryRegistry
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
	cred, ok, err := r.IdentifyCredential(ctx, token)
	if !ok || err != nil {
		return "", "", false, err
	}
	return cred.Name, cred.Owner, true, nil
}

// IdentifyCredential resolves token to its host name, owner, and optional
// bound Ed25519 public key.
func (r *Remote) IdentifyCredential(ctx context.Context, token string) (ResolvedCredential, bool, error) {
	if r == nil || token == "" {
		return ResolvedCredential{}, false, nil
	}
	if r.Authorize(token) {
		return ResolvedCredential{
			Name:  r.sharedName(),
			Owner: strings.ToLower(strings.TrimSpace(r.SharedOwner)),
		}, true, nil
	}
	var cred ResolvedCredential
	var ok bool
	var err error
	switch {
	case r.ResolveCredential != nil:
		cred, ok, err = r.ResolveCredential(ctx, token)
	case r.Resolve != nil:
		cred.Name, cred.Owner, ok, err = r.Resolve(ctx, token)
	default:
		return ResolvedCredential{}, false, nil
	}
	if ok && r.Token != "" && cred.Name == r.sharedName() {
		// The shared token's hostd holds this name: a registry hostd by
		// it would take over that hostd's session, and work sent to it
		// would not be checked against the registry's revocation.
		return ResolvedCredential{}, false, fmt.Errorf("registry host %q has the name -hostd-token-name gives the shared token's host; rename one of them", cred.Name)
	}
	return cred, ok, err
}

// SharedTokenName is the hostd the deprecated shared token names, "" when
// no shared token is configured: a name the registry may not also use.
func (r *Remote) SharedTokenName() string {
	if r == nil || r.Token == "" {
		return ""
	}
	return r.sharedName()
}

// SharedTokenOwner is the owner of the hostd the deprecated shared token
// names (-hostd-token-owner), "" for none or no shared token.
func (r *Remote) SharedTokenOwner() string {
	if r == nil || r.Token == "" {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(r.SharedOwner))
}

func (r *Remote) logf(format string, args ...any) {
	if r.Logf != nil {
		r.Logf(format, args...)
	}
}

// Serve runs conn as the session to the hostd peer.Name until it drops
// or ctx is done, replacing any session that same hostd had before it.
func (r *Remote) Serve(ctx context.Context, conn net.Conn, peer RemotePeer) {
	r.serveWithKey(ctx, conn, peer, "")
}

func (r *Remote) serveWithKey(ctx context.Context, conn net.Conn, peer RemotePeer, publicKey string) {
	if peer.Name == "" {
		peer.Name = r.sharedName()
	}
	m := mux.NewMux(conn, mux.Config{
		IsServer:     true,
		PingInterval: 15 * time.Second,
		PingTimeout:  15 * time.Second,
	})
	defer m.Close()
	sess := &remoteSession{r: r, m: m, peer: peer, publicKey: publicKey}
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
	go sess.acceptCarries()
	go r.learnLogins(ctx, sess)
	if r.OnCheckin != nil {
		r.OnCheckin(ctx, peer)
	}
	if r.OnChange != nil {
		r.OnChange(peer.Name, true)
	}
	select {
	case <-ctx.Done():
	case <-m.Done():
	}
	r.mu.Lock()
	name := sess.peer.Name
	dropped := r.cur[name] == sess
	if dropped {
		delete(r.cur, name)
		st := r.last[name]
		st.Connected = false
		st.Since = time.Now()
		r.last[name] = st
	}
	r.mu.Unlock()
	r.logf("grain daemon: grain hostd %s on %s disconnected", name, sess.peer.Host)
	if dropped && r.OnChange != nil {
		r.OnChange(name, false)
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
	_, st, err := r.dialSession(ctx, name)
	return st, err
}

// dialSession opens a stream to the hostd named name, with the session
// it went over.
func (r *Remote) dialSession(ctx context.Context, name string) (*remoteSession, net.Conn, error) {
	r.mu.Lock()
	sess := r.cur[name]
	r.mu.Unlock()
	if sess == nil {
		return nil, nil, ErrRemoteNotConnected
	}
	st, err := sess.m.OpenStream(ctx)
	if err != nil {
		return nil, nil, ErrRemoteNotConnected
	}
	return sess, st, nil
}

// Dialer is DialHostd for name, in Client.Dial's shape.
func (r *Remote) Dialer(name string) func(context.Context) (net.Conn, error) {
	return func(ctx context.Context) (net.Conn, error) { return r.DialHostd(ctx, name) }
}

// IsConnected reports whether name currently holds an active mux session.
func (r *Remote) IsConnected(name string) bool {
	if r == nil || name == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cur[name] != nil
}

// RenameSession moves any live or cached status entry from oldName to newName.
func (r *Remote) RenameSession(oldName, newName string) {
	if r == nil || oldName == "" || newName == "" || oldName == newName {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if sess, ok := r.cur[oldName]; ok {
		delete(r.cur, oldName)
		sess.peer.Name = newName
		r.cur[newName] = sess
	}
	if st, ok := r.last[oldName]; ok {
		delete(r.last, oldName)
		st.Name = newName
		r.last[newName] = st
	}
}

// ReplayCache exposes Remote's nonce replay cache for HTTP endpoints such as
// POST /api/hostd/renew.
func (r *Remote) ReplayCache() *NonceReplayCache {
	if r == nil {
		return nil
	}
	return &r.replay
}

// Disconnect closes the live session of the hostd named name, if one is
// connected now, and reports whether there was one. Serve records the
// drop as usual; Identify refuses any reconnect once its token is
// revoked in the registry.
func (r *Remote) Disconnect(name string) bool {
	if r == nil || name == "" {
		return false
	}
	r.mu.Lock()
	sess := r.cur[name]
	r.mu.Unlock()
	if sess == nil {
		return false
	}
	_ = sess.m.Close()
	return true
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
// refusalLogEvery for each kind of refusal (key), so an operator watching
// the daemon can tell a hostd with the wrong token from no hostd at all --
// otherwise only the hostd's own log says so. Kinds are limited apart so
// that a hostd retrying a stale token every few seconds does not hide a
// token presented under another hostd's name.
func (r *Remote) logRefused(req *http.Request, key, why string) {
	now := time.Now
	if r.now != nil {
		now = r.now
	}
	t := now()
	r.mu.Lock()
	if r.refusals == nil {
		r.refusals = map[string]*refusalLog{}
	}
	if _, ok := r.refusals[key]; !ok && len(r.refusals) >= maxRefusalKeys {
		key = "overflow"
	}
	last := r.refusals[key]
	if last != nil && t.Sub(last.at) < refusalLogEvery {
		last.more++
		r.mu.Unlock()
		return
	}
	more := 0
	if last != nil {
		more = last.more
	}
	r.refusals[key] = &refusalLog{at: t}
	r.mu.Unlock()
	host := hostOf(req)
	since := ""
	if more > 0 {
		since = fmt.Sprintf(" (%d more refused since the last line like this)", more)
	}
	r.logf("grain daemon: refused a grain hostd on %s connecting from %s: %s%s", host, req.RemoteAddr, why, since)
}

// hostOf is the machine a connecting hostd says it runs on, for a log line.
func hostOf(req *http.Request) string {
	if host := req.Header.Get(HostHeader); host != "" {
		return host
	}
	return "an unnamed host"
}

// ServeHTTP takes a hostd's upgrade of RemotePath (Dialer.Dial): it is
// refused unless its token names a hostd (Identify), refused again if it
// calls itself any other (NameHeader), and, for a remote connection
// (ConnectIdentity), refused unless its verified identity is that hostd's
// owner; it is otherwise answered 101 and served as the session to that
// hostd until it drops.
func (r *Remote) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	tok := RequestToken(req)
	cred, ok, err := r.IdentifyCredential(req.Context(), tok)
	name, owner := cred.Name, cred.Owner
	if err != nil {
		r.logf("grain daemon: looking up a grain hostd's token: %v", err)
		http.Error(w, "host agent: this controller could not check that host token", http.StatusServiceUnavailable)
		return
	}
	if !ok {
		why := "it presented a token that is not a host token here (/api/hostds, or -hostd-token-file)"
		if tok == "" {
			why = "it presented no host token"
		}
		r.logRefused(req, "token", why)
		http.Error(w, "host agent: this controller does not take that host token", http.StatusUnauthorized)
		return
	}
	if strings.TrimSpace(cred.PublicKey) != "" {
		now := time.Now
		if r.now != nil {
			now = r.now
		}
		if err := VerifyConnectHeaders(req, tok, cred.PublicKey, now(), &r.replay); err != nil {
			r.logRefused(req, "key\x00"+name, fmt.Sprintf("key-bound host %q failed proof-of-possession: %v", name, err))
			http.Error(w, "host agent: "+err.Error(), http.StatusUnauthorized)
			return
		}
	}
	if claimed := strings.TrimSpace(req.Header.Get(NameHeader)); claimed != "" && claimed != name {
		r.logRefused(req, "name\x00"+name+"\x00"+claimed, fmt.Sprintf("it calls itself host %q, but its token is host %q's", claimed, name))
		http.Error(w, fmt.Sprintf("host agent: this token belongs to host %q, not %q", name, claimed), http.StatusForbidden)
		return
	}
	id := ConnectIdentityFrom(req.Context())
	if why := ownerRefusal(id, name, owner, r.Authorize(tok)); why != "" {
		r.logf("grain daemon: refused a grain hostd on %s connecting from %s: %s", hostOf(req), req.RemoteAddr, why)
		code := http.StatusForbidden
		if id.Principal == "" {
			code = http.StatusUnauthorized
		}
		http.Error(w, "host agent: "+why, code)
		return
	}
	if !strings.EqualFold(req.Header.Get("Upgrade"), wsupgrade.Protocol) {
		http.Error(w, "host agent: expected a WebSocket upgrade", http.StatusBadRequest)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "host agent: this server cannot take over the connection", http.StatusInternalServerError)
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
	r.serveWithKey(req.Context(), c, RemotePeer{
		Name:      name,
		Owner:     owner,
		Principal: id.Principal,
		Host:      req.Header.Get(HostHeader),
		Build:     req.Header.Get(BuildHeader),
		Protocol:  req.Header.Get(ProtocolHeader),
		Addr:      req.RemoteAddr,
	}, cred.PublicKey)
}

// learnLoginsTimeout bounds the hello learnLogins asks.
const learnLoginsTimeout = 10 * time.Second

// learnLogins asks the hostd that just connected over sess for its hello
// and keeps the agent CLI logins it reports (Hello.Logins) in its
// status, for Settings' host pickers. Has(FeatureLogins) distinguishes
// an authoritative empty list ("no logins") from an older host that does
// not report them ("not known").
func (r *Remote) learnLogins(ctx context.Context, sess *remoteSession) {
	ctx, cancel := context.WithTimeout(ctx, learnLoginsTimeout)
	defer cancel()
	c := Client{Dial: func(ctx context.Context) (net.Conn, error) { return sess.m.OpenStream(ctx) }}
	h, err := c.Hello(ForDisplay(ctx))
	if err != nil || (!h.Has(FeatureLogins) && len(h.Logins) == 0) {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// Under the lock: a rename (RenameSession) moves the session to its
	// new name, and a newer session from the same hostd replaces it.
	name := sess.peer.Name
	if r.cur[name] != sess {
		return
	}
	st := r.last[name]
	st.Logins, st.LoginsKnown = append([]string(nil), h.Logins...), true
	r.last[name] = st
}
