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

// What a hostd on another machine needs beyond starting processes: a
// run's files staged on its own disk (OpStage), and grain's own
// endpoints -- the run's MCP server, grain's capability proxies --
// reachable from its own loopback (OpListen, OpCarry).
//
// Both go over the session hostd dialed out on (remote.go), and only
// over it: a hostd on the daemon's own machine shares the daemon's
// filesystem and loopback, and its socket serves neither op.
//
// Everything carried is something the daemon chose to listen for. A
// carried connection arrives at the daemon as a stream hostd opened,
// naming the ID of a listener the daemon asked for (Remote.Carry); a
// stream naming any other ID is closed unread. What the connection is
// served with is the daemon's handler, chosen when the daemon asked, so
// nothing the far end sends can pick what it reaches or how.

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Stage operations (StageOp.Op).
const (
	StageMkdirTemp  = "mkdir_temp"
	StageMkdirAll   = "mkdir_all"
	StageWriteFile  = "write_file"
	StageCreateTemp = "create_temp"
	StageSymlink    = "symlink"
	StageReadTail   = "read_tail"
	StageRemoveAll  = "remove_all"
	// StagePrune removes Path's subdirectories last modified more than
	// MaxAge ago, then all but the Limit most recent.
	StagePrune = "prune"
)

// MaxStageData bounds what one OpStage carries either way -- a file
// written, a tail read -- so its JSON stays inside one frame. A run's
// staged files are configuration of a few kilobytes.
const MaxStageData = 512 << 10

// StageOp is one OpStage operation, agent.Host's filesystem methods as
// they cross the session. Paths are hostd's own; a leading "~/" is
// hostd's home.
type StageOp struct {
	Op      string `json:"op"`
	Path    string `json:"path,omitempty"`
	Dir     string `json:"dir,omitempty"`
	Pattern string `json:"pattern,omitempty"`
	// Target is a Symlink's target, which is outside StageRoot: hostd
	// takes only a resolved regular file named in its allowlist's logins:
	// map (Hello.LoginTargets; or the built-in table when absent), linked
	// into a home under StageRoot.
	Target string        `json:"target,omitempty"`
	Data   []byte        `json:"data,omitempty"`
	Perm   uint32        `json:"perm,omitempty"`
	Limit  int64         `json:"limit,omitempty"`
	MaxAge time.Duration `json:"max_age,omitempty"`
}

// StageReply is OpStage's answer.
type StageReply struct {
	Path      string `json:"path,omitempty"`
	Data      []byte `json:"data,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	Error     string `json:"error,omitempty"`
	// NotExist marks Error as the path not being there, so the daemon
	// reads it as fs.ErrNotExist.
	NotExist bool `json:"not_exist,omitempty"`
}

// Listening is OpListen's answer: the loopback address on hostd's
// machine its listener took.
type Listening struct {
	Addr string `json:"addr"`
}

// stageError is a StageReply's Error as the daemon reads it.
type stageError struct {
	msg      string
	notExist bool
}

func (e *stageError) Error() string { return "grain hostd: " + e.msg }

func (e *stageError) Is(target error) bool { return e.notExist && target == fs.ErrNotExist }

// Stage runs op on hostd's machine (OpStage).
func (c Client) Stage(ctx context.Context, op StageOp) (StageReply, error) {
	if len(op.Data) > MaxStageData {
		return StageReply{}, fmt.Errorf("grain hostd: staging %s: %d bytes is more than one stage operation carries (%d)", op.Path, len(op.Data), MaxStageData)
	}
	var out StageReply
	if err := c.oneShot(ctx, Request{Op: OpStage, Stage: &op}, frameStaged, &out); err != nil {
		return out, err
	}
	if out.Error != "" {
		return out, &stageError{msg: out.Error, notExist: out.NotExist}
	}
	return out, nil
}

// ErrCarryUnsupported is Carry's error for a hostd that does not serve
// OpListen: one older than protocol 4.
var ErrCarryUnsupported = errors.New("grain hostd cannot carry grain's endpoints to its machine: it is older than hostd protocol 4; update it")

// carryRegistry is the daemon's half of OpCarry: the listeners it has
// asked a session for, by ID.
type carryRegistry struct {
	mu      sync.Mutex
	handles map[string]func(net.Conn)
}

func (c *carryRegistry) add(id string, handle func(net.Conn)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.handles == nil {
		c.handles = map[string]func(net.Conn){}
	}
	c.handles[id] = handle
}

func (c *carryRegistry) remove(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.handles, id)
}

func (c *carryRegistry) lookup(id string) func(net.Conn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.handles[id]
}

// carryHeaderTimeout bounds how long a stream hostd opened may take to
// say which listener it is for.
const carryHeaderTimeout = 10 * time.Second

// acceptCarries serves the streams hostd opens over s.m until it closes:
// each one a connection made to a listener the daemon asked this session
// for.
func (s *remoteSession) acceptCarries() {
	for {
		st, err := s.m.AcceptStream()
		if err != nil {
			return
		}
		go s.serveCarry(st)
	}
}

func (s *remoteSession) serveCarry(st net.Conn) {
	_ = st.SetReadDeadline(time.Now().Add(carryHeaderTimeout))
	typ, payload, err := readFrame(st)
	if err != nil || typ != frameRequest {
		st.Close()
		return
	}
	var req Request
	if json.Unmarshal(payload, &req) != nil {
		st.Close()
		return
	}
	if req.Op == OpRenew {
		defer st.Close()
		_ = st.SetDeadline(time.Now().Add(carryHeaderTimeout))
		s.serveRenew(st, req)
		return
	}
	if req.Op != OpCarry || req.ID == "" {
		st.Close()
		return
	}
	handle := s.carries.lookup(req.ID)
	if handle == nil {
		// Not a listener this daemon asked this session for, or one it
		// has released.
		st.Close()
		return
	}
	_ = st.SetReadDeadline(time.Time{})
	handle(st)
}

func (s *remoteSession) serveRenew(st net.Conn, req Request) {
	if s.r == nil || s.r.Renew == nil {
		_ = writeJSON(st, frameRefused, Refusal{Reason: "credential renewal is not configured on this controller"})
		return
	}
	if req.Renew == nil {
		_ = writeJSON(st, frameRefused, Refusal{Reason: "missing renew proof"})
		return
	}
	now := time.Now
	if s.r.now != nil {
		now = s.r.now
	}
	s.r.mu.Lock()
	hostName := s.peer.Name
	s.r.mu.Unlock()
	// Only a key-bound session renews, with its current credential and a
	// fresh proof from its bound key: a legacy per-host token has no key
	// behind it, and renewing one would only make it expire.
	if strings.TrimSpace(s.publicKey) == "" {
		_ = writeJSON(st, frameRefused, Refusal{Reason: "only a key-bound host renews its credential"})
		return
	}
	if strings.TrimSpace(req.Renew.CurrentToken) == "" {
		_ = writeJSON(st, frameRefused, Refusal{Reason: "missing current credential in renew proof"})
		return
	}
	if err := VerifyRenewProof(hostName, req.Renew.CurrentToken, s.publicKey, *req.Renew, now(), &s.r.replay); err != nil {
		_ = writeJSON(st, frameRefused, Refusal{Reason: err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), carryHeaderTimeout)
	defer cancel()
	tok, exp, err := s.r.Renew(ctx, hostName, req.Renew.CurrentToken)
	if err != nil {
		_ = writeJSON(st, frameRefused, Refusal{Reason: err.Error()})
		return
	}
	_ = writeJSON(st, frameRenewed, RenewReply{Token: tok, ExpiresAt: exp})
}

// Carry asks the connected hostd to listen on its own loopback, and
// serves every connection made there with handle, on this side of the
// session; handle owns the connection it is given. addr is the
// listener's address on hostd's machine. release stops it: the listener
// is closed and nothing more is handed to handle.
//
// The listener belongs to this session: a hostd that reconnects has
// lost it, and a run that needs it again asks again.
func (r *Remote) Carry(ctx context.Context, handle func(net.Conn)) (addr string, release func(), err error) {
	return r.CarryHostd(ctx, r.DefaultName(), handle)
}

// CarryHostd is Carry on the hostd named name: the listener is on that
// hostd's machine, the one a run routed there starts its CLI on. As with
// DialHostd, another hostd being connected is no answer.
func (r *Remote) CarryHostd(ctx context.Context, name string, handle func(net.Conn)) (addr string, release func(), err error) {
	id, err := randomID()
	if err != nil {
		return "", nil, err
	}
	sess, conn, err := r.dialSession(ctx, name)
	if err != nil {
		return "", nil, err
	}
	sess.carries.add(id, handle)
	fail := func(err error) (string, func(), error) {
		sess.carries.remove(id)
		conn.Close()
		return "", nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	if err := writeJSON(conn, frameRequest, Request{Op: OpListen, ID: id}); err != nil {
		return fail(err)
	}
	typ, payload, err := readFrame(conn)
	if err != nil {
		return fail(err)
	}
	switch typ {
	case frameListening:
	case frameRefused:
		err := refusal(payload)
		var refused *RefusedError
		if errors.As(err, &refused) && strings.HasPrefix(refused.Reason, "unknown op") {
			err = ErrCarryUnsupported
		}
		return fail(err)
	default:
		return fail(fmt.Errorf("hostd: unexpected frame %q", typ))
	}
	var l Listening
	if err := json.Unmarshal(payload, &l); err != nil || l.Addr == "" {
		return fail(fmt.Errorf("hostd: malformed listening answer: %v", err))
	}
	_ = conn.SetDeadline(time.Time{})
	var once sync.Once
	return l.Addr, func() {
		once.Do(func() {
			sess.carries.remove(id)
			conn.Close()
		})
	}, nil
}

// CarryHTTP carries h to hostd's machine: url is the base a client
// there reaches it at, http://<loopback>/<secret>, and every request
// under it is served by h with the secret taken off the front of its
// path. The secret keeps the listener to the CLI it was written for: it
// is in that CLI's staged configuration and nowhere else on the
// machine, and a request without it is answered 404 without reaching h.
// A request with an Origin header or a Host other than the loopback
// listener's own is answered 403, as mcp.StartCommandHTTP does on the
// daemon's loopback, so a browser on the workstation cannot reach h
// through cross-origin requests or DNS rebinding.
func (r *Remote) CarryHTTP(ctx context.Context, h http.Handler) (url string, release func(), err error) {
	return r.CarryHTTPHostd(ctx, r.DefaultName(), h)
}

// CarryHTTPHostd is CarryHTTP to the machine of the hostd named name.
func (r *Remote) CarryHTTPHostd(ctx context.Context, name string, h http.Handler) (url string, release func(), err error) {
	secret, err := randomID()
	if err != nil {
		return "", nil, err
	}
	l := newConnListener()
	addr, stop, err := r.CarryHostd(ctx, name, l.push)
	if err != nil {
		_ = l.Close()
		return "", nil, err
	}
	srv := &http.Server{Handler: carriedHTTP(addr, secret, h), ReadHeaderTimeout: time.Minute}
	go func() { _ = srv.Serve(l) }()
	return "http://" + addr + "/" + secret, func() {
		stop()
		_ = srv.Close()
	}, nil
}

// carriedHTTP serves h only the requests addressed to host with no
// Origin header and starting with the whole path segment secret, with
// that segment taken off.
func carriedHTTP(host, secret string, h http.Handler) http.Handler {
	rawPrefix := "/" + secret
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Host != host || req.Header.Get("Origin") != "" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		trimmed, ok := strings.CutPrefix(req.URL.Path, "/")
		if !ok {
			http.NotFound(w, req)
			return
		}
		seg, tail, hasSlash := strings.Cut(trimmed, "/")
		if len(seg) != len(secret) || subtle.ConstantTimeCompare([]byte(seg), []byte(secret)) != 1 {
			http.NotFound(w, req)
			return
		}
		rest := "/"
		if hasSlash {
			rest = "/" + tail
		}
		r2 := req.Clone(req.Context())
		r2.URL.Path = rest
		if r2.URL.RawPath != "" {
			if rawRest, ok := strings.CutPrefix(r2.URL.RawPath, rawPrefix); ok && (rawRest == "" || strings.HasPrefix(rawRest, "/")) {
				if rawRest == "" {
					rawRest = "/"
				}
				r2.URL.RawPath = rawRest
			} else {
				r2.URL.RawPath = ""
			}
		}
		r2.RequestURI = ""
		h.ServeHTTP(w, r2)
	})
}

func randomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// connListener is a net.Listener fed by push, so an http.Server serves
// connections that arrive as streams.
type connListener struct {
	ch     chan net.Conn
	done   chan struct{}
	closed sync.Once
}

func newConnListener() *connListener {
	return &connListener{ch: make(chan net.Conn), done: make(chan struct{})}
}

func (l *connListener) push(c net.Conn) {
	select {
	case l.ch <- c:
	case <-l.done:
		c.Close()
	}
}

func (l *connListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *connListener) Close() error {
	l.closed.Do(func() { close(l.done) })
	return nil
}

func (l *connListener) Addr() net.Addr { return carryAddr{} }

type carryAddr struct{}

func (carryAddr) Network() string { return "hostd-carry" }
func (carryAddr) String() string  { return "hostd-carry" }

// pipe copies a and b into each other until either side is done, then
// closes both.
func pipe(a, b io.ReadWriteCloser) {
	var once sync.Once
	closeBoth := func() { once.Do(func() { a.Close(); b.Close() }) }
	go func() { _, _ = io.Copy(a, b); closeBoth() }()
	_, _ = io.Copy(b, a)
	closeBoth()
}

// Carrier is r's CarryHTTP bound to the hostd named name ("" for the
// default), for a run whose CLI was routed there: its endpoints have to
// be carried to the machine its CLI runs on, not the default hostd's.
func (r *Remote) Carrier(name string) *HostdCarrier {
	return &HostdCarrier{Remote: r, Name: name}
}

// HostdCarrier is Remote.Carrier.
type HostdCarrier struct {
	Remote *Remote
	Name   string
}

// CarryHTTP is Remote.CarryHTTPHostd to c's hostd.
func (c *HostdCarrier) CarryHTTP(ctx context.Context, h http.Handler) (string, func(), error) {
	name := c.Name
	if name == "" {
		name = c.Remote.DefaultName()
	}
	return c.Remote.CarryHTTPHostd(ctx, name, h)
}
