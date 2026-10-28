package sandbox

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bwsalmon/granule/pkg/guestforward"
)

type sandboxSlot struct {
	sandboxID string
	token     string
	expiresAt time.Time
	client    *Client
}

// Acceptor is the controller-side listener for dial-out mode. It mints
// per-sandbox tokens bound to a sandbox ID and expiration time, verifies
// incoming TLS/mTLS + token handshakes from pod-side wrappers, and hands back
// a multiplexed *Client per sandbox.
type Acceptor struct {
	ln     net.Listener
	muxCfg guestforward.Config

	mu       sync.Mutex
	cond     *sync.Cond
	slots    map[string]*sandboxSlot
	closed   bool
	refusals atomic.Uint64
}

// NewAcceptor wraps an existing listener (typically a *tls.Listener) as a
// dial-out controller Acceptor.
func NewAcceptor(ln net.Listener, muxCfg guestforward.Config) *Acceptor {
	a := &Acceptor{
		ln:     ln,
		muxCfg: muxCfg,
		slots:  make(map[string]*sandboxSlot),
	}
	a.cond = sync.NewCond(&a.mu)
	return a
}

// ListenTLS starts a TLS 1.3 listener on addr and returns a ready *Acceptor.
func ListenTLS(addr string, tlsCfg *tls.Config) (*Acceptor, error) {
	if tlsCfg == nil {
		return nil, errors.New("sandbox: ListenTLS requires a non-nil *tls.Config")
	}
	cfg := tlsCfg.Clone()
	cfg.MinVersion = tls.VersionTLS13
	ln, err := tls.Listen("tcp", addr, cfg)
	if err != nil {
		return nil, err
	}
	return NewAcceptor(ln, guestforward.Config{}), nil
}

// Addr returns the listener's network address.
func (a *Acceptor) Addr() net.Addr {
	return a.ln.Addr()
}

// Refusals returns the total number of handshakes rejected due to
// authentication or protocol failures.
func (a *Acceptor) Refusals() uint64 {
	return a.refusals.Load()
}

// Issue mints a new cryptographically random token bound exclusively to
// sandboxID that expires after ttl.
func (a *Acceptor) Issue(sandboxID string, ttl time.Duration) (string, error) {
	sandboxID = strings.TrimSpace(sandboxID)
	if sandboxID == "" {
		return "", errors.New("sandbox: sandboxID must not be empty")
	}
	if ttl <= 0 {
		return "", errors.New("sandbox: token TTL must be positive")
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generating sandbox token: %w", err)
	}
	token := hex.EncodeToString(raw[:])
	if err := a.RegisterToken(sandboxID, token, time.Now().Add(ttl)); err != nil {
		return "", err
	}
	return token, nil
}

// RegisterToken registers a pre-generated token for sandboxID valid until
// expiresAt.
func (a *Acceptor) RegisterToken(sandboxID, token string, expiresAt time.Time) error {
	sandboxID = strings.TrimSpace(sandboxID)
	if sandboxID == "" {
		return errors.New("sandbox: sandboxID must not be empty")
	}
	if token == "" {
		return errors.New("sandbox: token must not be empty")
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	slot, ok := a.slots[sandboxID]
	if !ok {
		slot = &sandboxSlot{sandboxID: sandboxID}
		a.slots[sandboxID] = slot
	}
	slot.token = token
	slot.expiresAt = expiresAt
	a.cond.Broadcast()
	return nil
}

// Revoke invalidates the token for sandboxID and closes its active client
// session, if any.
func (a *Acceptor) Revoke(sandboxID string) {
	a.mu.Lock()
	slot := a.slots[sandboxID]
	delete(a.slots, sandboxID)
	a.cond.Broadcast()
	a.mu.Unlock()
	if slot != nil && slot.client != nil {
		_ = slot.client.Close()
	}
}

// DropActiveSession closes the current underlying transport session for
// sandboxID without revoking its token or closing its *Client, forcing the
// wrapper to reconnect.
func (a *Acceptor) DropActiveSession(sandboxID string) error {
	a.mu.Lock()
	slot := a.slots[sandboxID]
	var sess *guestforward.Session
	if slot != nil && slot.client != nil {
		slot.client.mu.Lock()
		sess = slot.client.sess
		slot.client.mu.Unlock()
	}
	a.mu.Unlock()
	if sess == nil {
		return fmt.Errorf("sandbox %q has no active session", sandboxID)
	}
	return sess.Close()
}

// Close stops accepting new connections and closes all active sandbox
// clients.
func (a *Acceptor) Close() error {
	a.mu.Lock()
	a.closed = true
	slots := make([]*sandboxSlot, 0, len(a.slots))
	for _, s := range a.slots {
		slots = append(slots, s)
	}
	a.cond.Broadcast()
	a.mu.Unlock()

	err := a.ln.Close()
	for _, s := range slots {
		if s.client != nil {
			_ = s.client.Close()
		}
	}
	return err
}

// Serve accepts incoming dial-out connections from wrappers until ctx is
// cancelled or Close is called.
func (a *Acceptor) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		_ = a.Close()
	}()
	for {
		conn, err := a.ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go a.handleConn(ctx, conn)
	}
}

func (a *Acceptor) reject(conn net.Conn, reason string) {
	a.refusals.Add(1)
	_ = writeJSONLine(conn, HandshakeResponse{
		OK:    false,
		Error: reason,
	})
	_ = conn.Close()
}

func (a *Acceptor) handleConn(ctx context.Context, conn net.Conn) {
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	var peerCerts []*x509.Certificate
	if tlsConn, ok := conn.(*tls.Conn); ok {
		hctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := tlsConn.HandshakeContext(hctx)
		cancel()
		if err != nil {
			a.refusals.Add(1)
			_ = conn.Close()
			return
		}
		state := tlsConn.ConnectionState()
		peerCerts = state.PeerCertificates
	}

	br := bufio.NewReader(conn)
	var hs HandshakeRequest
	if err := readJSONLine(br, &hs); err != nil {
		a.refusals.Add(1)
		_ = conn.Close()
		return
	}

	if hs.Version != ProtocolVersion {
		a.reject(conn, fmt.Sprintf("unsupported protocol version %q, want %q", hs.Version, ProtocolVersion))
		return
	}
	if hs.SandboxID == "" {
		a.reject(conn, "missing sandbox_id")
		return
	}

	now := time.Now()
	a.mu.Lock()
	slot := a.slots[hs.SandboxID]
	if slot == nil {
		a.mu.Unlock()
		a.reject(conn, fmt.Sprintf("unknown sandbox %q", hs.SandboxID))
		return
	}
	token := slot.token
	expiresAt := slot.expiresAt
	a.mu.Unlock()

	if now.After(expiresAt) {
		a.reject(conn, fmt.Sprintf("token for sandbox %q has expired", hs.SandboxID))
		return
	}
	if subtle.ConstantTimeCompare([]byte(hs.Token), []byte(token)) != 1 {
		a.reject(conn, "invalid token for sandbox")
		return
	}
	if len(peerCerts) > 0 && !certMatchesSandbox(peerCerts[0], hs.SandboxID) {
		a.reject(conn, fmt.Sprintf("client certificate identity does not match sandbox %q", hs.SandboxID))
		return
	}

	if err := writeJSONLine(conn, HandshakeResponse{
		OK:        true,
		Version:   ProtocolVersion,
		SandboxID: hs.SandboxID,
	}); err != nil {
		_ = conn.Close()
		return
	}
	_ = conn.SetDeadline(time.Time{})

	// Controller acts as the mux client (client=true, odd stream IDs); wrapper
	// acts as client=false (even stream IDs).
	sess := guestforward.NewSession(&bufferedConn{Reader: br, Conn: conn}, true, a.muxCfg)

	a.mu.Lock()
	slot = a.slots[hs.SandboxID]
	if slot == nil || now.After(slot.expiresAt) {
		a.mu.Unlock()
		_ = sess.Close()
		return
	}
	if slot.client == nil {
		slot.client = newClient(hs.SandboxID, sess, true)
	} else {
		slot.client.setSession(sess)
	}
	a.cond.Broadcast()
	a.mu.Unlock()
}

func certMatchesSandbox(cert *x509.Certificate, sandboxID string) bool {
	if cert.Subject.CommonName == sandboxID {
		return true
	}
	for _, dns := range cert.DNSNames {
		if dns == sandboxID {
			return true
		}
	}
	for _, uri := range cert.URIs {
		if uri.String() == sandboxID || strings.HasSuffix(uri.Path, "/"+sandboxID) {
			return true
		}
	}
	return false
}

// Client returns the *Client for sandboxID if that sandbox is currently
// connected.
func (a *Acceptor) Client(sandboxID string) (*Client, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	slot := a.slots[sandboxID]
	if slot == nil || slot.client == nil {
		return nil, false
	}
	slot.client.mu.Lock()
	alive := !slot.client.closed && slot.client.sess != nil && slot.client.sess.Err() == nil
	slot.client.mu.Unlock()
	if !alive {
		return nil, false
	}
	return slot.client, true
}

// WaitClient waits until sandboxID has connected (or reconnected) to this
// Acceptor and returns its *Client.
func (a *Acceptor) WaitClient(ctx context.Context, sandboxID string) (*Client, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	stop := context.AfterFunc(ctx, func() {
		a.mu.Lock()
		a.cond.Broadcast()
		a.mu.Unlock()
	})
	defer stop()

	for {
		if a.closed {
			return nil, errors.New("sandbox: acceptor closed")
		}
		slot := a.slots[sandboxID]
		if slot == nil {
			return nil, fmt.Errorf("sandbox: unknown sandbox %q", sandboxID)
		}
		if time.Now().After(slot.expiresAt) {
			return nil, fmt.Errorf("%w: token for sandbox %q expired", ErrAuthFailed, sandboxID)
		}
		if slot.client != nil {
			slot.client.mu.Lock()
			alive := !slot.client.closed && slot.client.sess != nil && slot.client.sess.Err() == nil
			slot.client.mu.Unlock()
			if alive {
				return slot.client, nil
			}
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		a.cond.Wait()
	}
}
