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

// Package sandboxaccess is grain's end of granule's dial-out sandbox
// access channel (docs/design/grain-on-gke.md, "Sandbox access"): the
// mTLS acceptor sandbox wrappers dial, the per-deployment CA, the
// per-sandbox client certificate and token it mints, the admission
// checks, and the table of which sandboxes are connected.
//
// It is neutral in the grit-split sense: it imports granule and the
// standard library only, never grain's core, so a later grit can reuse
// it (pkg/grit/independence_test.go).
//
// The wire protocol, the multiplexed session and the client a caller
// drives a sandbox with are granule's (third_party/granule/pkg/sandbox).
// What this package adds in front of granule's Acceptor is grain's own
// admission: granule checks a token it was given in plain text, and
// grain keeps only the token's hash, wants the client certificate to
// chain to its own CA and name the very sandbox it presents a token for,
// and refuses any sandbox that is no longer live in its own view. A
// connection that passes all of that is handed to granule's Acceptor
// with its token registered, so a wrapper reconnecting to a restarted
// grain is re-admitted from the stored hash alone.
package sandboxaccess

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bwsalmon/granule/pkg/guestforward"
	"github.com/bwsalmon/granule/pkg/sandbox"
)

// DefaultPort is the acceptor's port: its own, on its own Service
// (grain-access), serving nothing else.
const DefaultPort = 7443

// handshakeTimeout bounds the TLS handshake plus the opening JSON line.
const handshakeTimeout = 10 * time.Second

// maxHandshakeLine bounds the opening JSON line a peer may send before
// it is admitted.
const maxHandshakeLine = 4 << 10

// Config is what an Acceptor needs.
type Config struct {
	// Deployment is the deployment id every client certificate's URI SAN
	// names (SandboxURI).
	Deployment string
	// CA is the deployment CA.
	CA *CA
	// ServerNames are the DNS names and IPs the acceptor's serving
	// certificate is minted for -- whatever wrappers dial, e.g.
	// grain-access.grain.svc.
	ServerNames []string
	// Live reports whether sandbox is live in grain's own view. A
	// sandbox that is not is refused even with valid credentials. Nil
	// treats every sandbox with registered credentials as live.
	Live func(sandbox string) bool
	// Mux tunes granule's multiplexed session (heartbeat and so on);
	// zero is granule's defaults.
	Mux guestforward.Config
	// Now is the clock (time.Now when nil).
	Now func() time.Time
}

type registration struct {
	hash     [sha256.Size]byte
	notAfter time.Time
}

// Acceptor admits sandbox wrappers' dial-out connections and holds the
// table of connected sandboxes.
type Acceptor struct {
	cfg    Config
	tlsCfg *tls.Config
	inner  *sandbox.Acceptor
	ln     *admitListener

	mu    sync.Mutex
	creds map[string]registration
}

// New builds an Acceptor serving on ln, a plain TCP listener: TLS is
// this package's, so that admission can see the client certificate.
// Call Serve to start accepting.
func New(cfg Config, ln net.Listener) (*Acceptor, error) {
	if cfg.CA == nil {
		return nil, errors.New("sandboxaccess: Config.CA is required")
	}
	if cfg.Deployment == "" {
		return nil, errors.New("sandboxaccess: Config.Deployment is required")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	serverCert, err := cfg.CA.ServerCertificate(cfg.ServerNames, cfg.Now())
	if err != nil {
		return nil, err
	}
	a := &Acceptor{cfg: cfg, creds: map[string]registration{}}
	a.tlsCfg = &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{serverCert},
		// No application byte before a certificate that chains to the
		// deployment CA: a guest that reached the port without the key
		// gets nothing past the handshake.
		ClientAuth: tls.RequireAndVerifyClientCert,
		ClientCAs:  cfg.CA.Pool(),
		Time:       cfg.Now,
	}
	a.ln = newAdmitListener(ln, a)
	a.inner = sandbox.NewAcceptor(a.ln, cfg.Mux)
	return a, nil
}

// Listen is New over a fresh TCP listener on addr (":7443").
func Listen(cfg Config, addr string) (*Acceptor, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("sandboxaccess: listening on %s: %w", addr, err)
	}
	a, err := New(cfg, ln)
	if err != nil {
		_ = ln.Close()
		return nil, err
	}
	return a, nil
}

// Addr is the listener's address.
func (a *Acceptor) Addr() net.Addr { return a.ln.Addr() }

// CACertPEM is the CA certificate wrappers verify the acceptor with.
func (a *Acceptor) CACertPEM() []byte { return a.cfg.CA.CertPEM() }

// Serve accepts until ctx ends or Close is called.
func (a *Acceptor) Serve(ctx context.Context) error {
	go a.ln.run(ctx)
	return a.inner.Serve(ctx)
}

// Close stops accepting and closes every sandbox's channel.
func (a *Acceptor) Close() error { return a.inner.Close() }

// Refusals counts the connections refused, here or by granule.
func (a *Acceptor) Refusals() uint64 { return a.ln.refusals() + a.inner.Refusals() }

// Mint mints sandbox's credentials, valid for ttl (capped at
// MaxCredentialLife), and registers the token's hash. Minting again for
// the same sandbox replaces its earlier credentials.
func (a *Acceptor) Mint(sandboxName string, ttl time.Duration) (Credentials, error) {
	if err := validSandbox(sandboxName); err != nil {
		return Credentials{}, err
	}
	if ttl <= 0 || ttl > MaxCredentialLife {
		ttl = MaxCredentialLife
	}
	now := a.cfg.Now()
	notAfter := now.Add(ttl)
	token, err := newToken()
	if err != nil {
		return Credentials{}, err
	}
	certPEM, keyPEM, err := a.cfg.CA.clientCertificate(a.cfg.Deployment, sandboxName, now.Add(-time.Minute), notAfter)
	if err != nil {
		return Credentials{}, err
	}
	a.mu.Lock()
	a.creds[sandboxName] = registration{hash: sha256.Sum256([]byte(token)), notAfter: notAfter}
	a.mu.Unlock()
	return Credentials{
		Sandbox:  sandboxName,
		Token:    token,
		CertPEM:  certPEM,
		KeyPEM:   keyPEM,
		CAPEM:    a.cfg.CA.CertPEM(),
		NotAfter: notAfter,
	}, nil
}

// Register restores a sandbox's token hash (TokenHash, hex) minted
// earlier -- by this process or a previous one -- so its wrapper is
// admitted when it reconnects.
func (a *Acceptor) Register(sandboxName, tokenHash string, notAfter time.Time) error {
	if err := validSandbox(sandboxName); err != nil {
		return err
	}
	raw, err := hex.DecodeString(tokenHash)
	if err != nil || len(raw) != sha256.Size {
		return fmt.Errorf("sandboxaccess: token hash for %q is not a hex SHA-256", sandboxName)
	}
	var reg registration
	copy(reg.hash[:], raw)
	reg.notAfter = notAfter
	a.mu.Lock()
	a.creds[sandboxName] = reg
	a.mu.Unlock()
	return nil
}

// Revoke forgets sandbox's credentials and closes its channel: what
// reaping a sandbox does, so a reaped sandbox's credential is dead the
// moment it is.
func (a *Acceptor) Revoke(sandboxName string) {
	a.mu.Lock()
	delete(a.creds, sandboxName)
	a.mu.Unlock()
	a.inner.Revoke(sandboxName)
}

// Client is sandbox's live channel, if it is connected.
func (a *Acceptor) Client(sandboxName string) (*sandbox.Client, bool) {
	return a.inner.Client(sandboxName)
}

// waitPoll is how often WaitClient looks for a sandbox that has
// credentials but has not connected yet: granule's own WaitClient knows
// a sandbox only once it has connected at least once.
const waitPoll = 25 * time.Millisecond

// WaitClient waits for sandbox's wrapper to connect (or reconnect). It
// fails at once for a sandbox with no credentials, and as soon as the
// sandbox's credentials are revoked.
func (a *Acceptor) WaitClient(ctx context.Context, sandboxName string) (*sandbox.Client, error) {
	t := time.NewTicker(waitPoll)
	defer t.Stop()
	for {
		a.mu.Lock()
		_, ok := a.creds[sandboxName]
		a.mu.Unlock()
		if !ok {
			return nil, fmt.Errorf("sandboxaccess: sandbox %q has no credentials", sandboxName)
		}
		if c, ok := a.inner.Client(sandboxName); ok {
			return c, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-t.C:
		}
	}
}

// Connected lists the sandboxes with a live channel, sorted.
func (a *Acceptor) Connected() []string {
	a.mu.Lock()
	names := make([]string, 0, len(a.creds))
	for name := range a.creds {
		names = append(names, name)
	}
	a.mu.Unlock()
	var out []string
	for _, name := range names {
		if _, ok := a.inner.Client(name); ok {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func validSandbox(name string) error {
	if name == "" || strings.TrimSpace(name) != name || strings.ContainsAny(name, "/\n") {
		return fmt.Errorf("sandboxaccess: invalid sandbox name %q", name)
	}
	return nil
}

// admit is grain's admission check, run on a connection whose TLS
// handshake (and so the certificate chain, including its expiry) has
// already been verified. It returns the instant the admission lapses.
func (a *Acceptor) admit(leaf *x509.Certificate, hs sandbox.HandshakeRequest) (time.Time, error) {
	if hs.Version != sandbox.ProtocolVersion {
		return time.Time{}, fmt.Errorf("unsupported protocol version %q", hs.Version)
	}
	// The certificate must be this sandbox's, by both its CN and its
	// URI SAN: one sandbox's certificate cannot open another's channel.
	want := SandboxURI(a.cfg.Deployment, hs.SandboxID).String()
	if leaf.Subject.CommonName != hs.SandboxID || len(leaf.URIs) != 1 || leaf.URIs[0].String() != want {
		return time.Time{}, fmt.Errorf("client certificate is not sandbox %q's", hs.SandboxID)
	}
	a.mu.Lock()
	reg, ok := a.creds[hs.SandboxID]
	a.mu.Unlock()
	if !ok {
		return time.Time{}, fmt.Errorf("sandbox %q has no credentials", hs.SandboxID)
	}
	sum := sha256.Sum256([]byte(hs.Token))
	if subtle.ConstantTimeCompare(sum[:], reg.hash[:]) != 1 {
		return time.Time{}, fmt.Errorf("invalid token for sandbox %q", hs.SandboxID)
	}
	now := a.cfg.Now()
	if !now.Before(reg.notAfter) {
		return time.Time{}, fmt.Errorf("credentials for sandbox %q have expired", hs.SandboxID)
	}
	if a.cfg.Live != nil && !a.cfg.Live(hs.SandboxID) {
		return time.Time{}, fmt.Errorf("sandbox %q is not live", hs.SandboxID)
	}
	notAfter := reg.notAfter
	if leaf.NotAfter.Before(notAfter) {
		notAfter = leaf.NotAfter
	}
	return notAfter, nil
}

// admitListener is the net.Listener granule's Acceptor serves on. It
// runs the TLS handshake and grain's admission itself, concurrently per
// connection, and hands granule only the connections it admitted, with
// their opening line replayed and their token registered.
type admitListener struct {
	raw net.Listener
	a   *Acceptor

	conns   chan net.Conn
	done    chan struct{}
	once    sync.Once
	mu      sync.Mutex
	refused uint64
}

func newAdmitListener(raw net.Listener, a *Acceptor) *admitListener {
	return &admitListener{raw: raw, a: a, conns: make(chan net.Conn), done: make(chan struct{})}
}

func (l *admitListener) run(ctx context.Context) {
	for {
		conn, err := l.raw.Accept()
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			_ = l.Close()
			return
		}
		go l.handle(ctx, conn)
	}
}

func (l *admitListener) refusals() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.refused
}

func (l *admitListener) refuse(conn net.Conn, reason string, reply bool) {
	l.mu.Lock()
	l.refused++
	l.mu.Unlock()
	slog.Info("sandboxaccess: refused connection", "remote", conn.RemoteAddr().String(), "reason", reason)
	if reply {
		b, _ := json.Marshal(sandbox.HandshakeResponse{OK: false, Error: reason})
		_, _ = conn.Write(append(b, '\n'))
	}
	_ = conn.Close()
}

func (l *admitListener) handle(ctx context.Context, raw net.Conn) {
	conn := tls.Server(raw, l.a.tlsCfg)
	_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))
	hctx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	err := conn.HandshakeContext(hctx)
	cancel()
	if err != nil {
		l.refuse(raw, "tls handshake: "+err.Error(), false)
		return
	}
	peers := conn.ConnectionState().PeerCertificates
	if len(peers) == 0 {
		l.refuse(conn, "no client certificate", false)
		return
	}
	br := bufio.NewReaderSize(io.LimitReader(conn, maxHandshakeLine), maxHandshakeLine)
	line, err := br.ReadBytes('\n')
	if err != nil {
		l.refuse(conn, "reading handshake: "+err.Error(), false)
		return
	}
	var hs sandbox.HandshakeRequest
	if err := json.Unmarshal(line, &hs); err != nil {
		l.refuse(conn, "malformed handshake", true)
		return
	}
	notAfter, err := l.a.admit(peers[0], hs)
	if err != nil {
		l.refuse(conn, err.Error(), true)
		return
	}
	// granule's Acceptor checks the token again, against the plain text
	// it is given here; the wrapper has just shown it, so registering it
	// now holds nothing grain did not already have in memory.
	if err := l.a.inner.RegisterToken(hs.SandboxID, hs.Token, notAfter); err != nil {
		l.refuse(conn, err.Error(), true)
		return
	}
	_ = conn.SetDeadline(time.Time{})
	// Replay the line, and anything read past it (the wrapper waits for
	// the reply first, so normally nothing), ahead of the stream.
	rest, _ := br.Peek(br.Buffered())
	replay := append(line, rest...)
	wrapped := &replayConn{Conn: conn, r: io.MultiReader(bytes.NewReader(replay), conn)}
	select {
	case l.conns <- wrapped:
	case <-l.done:
		_ = conn.Close()
	}
}

func (l *admitListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *admitListener) Close() error {
	var err error
	l.once.Do(func() {
		close(l.done)
		err = l.raw.Close()
	})
	return err
}

func (l *admitListener) Addr() net.Addr { return l.raw.Addr() }

type replayConn struct {
	net.Conn
	r io.Reader
}

func (c *replayConn) Read(p []byte) (int, error) { return c.r.Read(p) }
