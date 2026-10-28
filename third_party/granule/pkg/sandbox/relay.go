package sandbox

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/bwsalmon/granule/pkg/guestforward"
)

// RelayProtocolVersion identifies the handshake between RelayClient and
// granule relay (pkg/relay).
const RelayProtocolVersion = "granule-relay/1"

// RelayHandshakeRequest is the opening JSON line a controller sends to
// granule relay over TLS, before the multiplexed session starts.
type RelayHandshakeRequest struct {
	Version string `json:"version"`
	// Token is the controller bearer token. It may be empty when the relay
	// authenticates the controller by its mTLS client certificate alone.
	Token string `json:"token,omitempty"`
}

// RelayHandshakeResponse is the relay's reply to RelayHandshakeRequest.
type RelayHandshakeResponse struct {
	OK      bool   `json:"ok"`
	Version string `json:"version,omitempty"`
	Error   string `json:"error,omitempty"`
}

// RelayStreamRequest is the metadata of every stream a controller opens on
// its relay session: the sandbox to reach. The relay resolves Target to a
// pod IP and splices the stream to that pod's access port; it never reads
// what flows over the stream afterwards.
type RelayStreamRequest struct {
	Target string `json:"target"`
}

// RelayCredentials configures DialRelay.
type RelayCredentials struct {
	// Token is the controller bearer token the relay was started with.
	Token string

	// TLSConfig verifies the relay's certificate (RootCAs, ServerName)
	// and, for mTLS, carries the controller's client certificate. TLS 1.3
	// is enforced.
	TLSConfig *tls.Config

	// MinBackoff and MaxBackoff bound the delay between reconnects after
	// the relay session drops. Defaults to 50ms and 5s.
	MinBackoff time.Duration
	MaxBackoff time.Duration

	// MuxConfig tunes the controller-to-relay guestforward.Session.
	MuxConfig guestforward.Config
}

// RelayClient holds one long-lived multiplexed session from an off-cluster
// controller to granule relay, reconnecting with backoff whenever it drops,
// and hands out per-sandbox Clients that reach a named sandbox through it.
type RelayClient struct {
	addr   string
	creds  RelayCredentials
	tlsCfg *tls.Config
	host   string

	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	cond    *sync.Cond
	sess    *guestforward.Session
	fatal   error
	closed  bool
	dropped chan struct{}
}

// DialRelay connects to granule relay at relayAddr ("host:port",
// "tls://host:port" or "https://host:port") and authenticates as a
// controller. The first connection is made before DialRelay returns, so a
// bad credential is reported here (wrapping ErrAuthFailed); after that the
// session is re-established in the background whenever it drops, until
// Close. A credential the relay refuses on a reconnect stops reconnecting
// and fails every later operation with that error.
func DialRelay(ctx context.Context, relayAddr string, creds RelayCredentials) (*RelayClient, error) {
	addr := strings.TrimSpace(relayAddr)
	if !strings.Contains(addr, "://") {
		addr = "tls://" + addr
	}
	tlsCfg, hostPort, err := buildDialOutTLS(addr, DialOutOptions{TLSConfig: creds.TLSConfig})
	if err != nil {
		return nil, err
	}
	if creds.MinBackoff <= 0 {
		creds.MinBackoff = 50 * time.Millisecond
	}
	if creds.MaxBackoff <= 0 {
		creds.MaxBackoff = 5 * time.Second
	}
	r := &RelayClient{
		addr:   hostPort,
		creds:  creds,
		tlsCfg: tlsCfg,
	}
	r.cond = sync.NewCond(&r.mu)
	sess, err := r.connect(ctx)
	if err != nil {
		return nil, err
	}
	r.ctx, r.cancel = context.WithCancel(context.Background())
	r.sess = sess
	go r.maintain(sess)
	return r, nil
}

func (r *RelayClient) connect(ctx context.Context) (*guestforward.Session, error) {
	d := &tls.Dialer{NetDialer: &net.Dialer{Timeout: 10 * time.Second}, Config: r.tlsCfg}
	conn, err := d.DialContext(ctx, "tcp", r.addr)
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if err := writeJSONLine(conn, RelayHandshakeRequest{Version: RelayProtocolVersion, Token: r.creds.Token}); err != nil {
		conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	var resp RelayHandshakeResponse
	if err := readJSONLine(br, &resp); err != nil {
		conn.Close()
		// A relay that refuses the client certificate fails the TLS
		// handshake, which surfaces on this first read.
		var alert tls.AlertError
		if errors.As(err, &alert) {
			return nil, fmt.Errorf("%w: %v", ErrAuthFailed, err)
		}
		return nil, err
	}
	if !resp.OK {
		conn.Close()
		return nil, fmt.Errorf("%w: %s", ErrAuthFailed, resp.Error)
	}
	_ = conn.SetDeadline(time.Time{})
	return guestforward.NewSession(&bufferedConn{Reader: br, Conn: conn}, true, r.creds.MuxConfig), nil
}

// maintain waits for sess to end and replaces it, with backoff, until the
// client is closed or the relay refuses its credentials.
func (r *RelayClient) maintain(sess *guestforward.Session) {
	backoff := r.creds.MinBackoff
	for {
		select {
		case <-sess.Done():
		case <-r.ctx.Done():
			return
		}
		connectedAt := time.Now()
		for {
			if r.ctx.Err() != nil {
				return
			}
			next, err := r.connect(r.ctx)
			if err == nil {
				r.mu.Lock()
				if r.closed {
					r.mu.Unlock()
					_ = next.Close()
					return
				}
				r.sess = next
				r.cond.Broadcast()
				r.mu.Unlock()
				sess = next
				if time.Since(connectedAt) > 2*r.creds.MaxBackoff {
					backoff = r.creds.MinBackoff
				}
				break
			}
			if errors.Is(err, ErrAuthFailed) {
				r.mu.Lock()
				r.fatal = err
				r.cond.Broadcast()
				r.mu.Unlock()
				return
			}
			select {
			case <-r.ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff *= 2
			if backoff > r.creds.MaxBackoff {
				backoff = r.creds.MaxBackoff
			}
		}
	}
}

// session returns the live relay session, waiting for a reconnect if the
// current one has dropped.
func (r *RelayClient) session(ctx context.Context) (*guestforward.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	stop := context.AfterFunc(ctx, func() {
		r.mu.Lock()
		r.cond.Broadcast()
		r.mu.Unlock()
	})
	defer stop()
	for {
		if r.closed {
			return nil, guestforward.ErrSessionClosed
		}
		if r.fatal != nil {
			return nil, r.fatal
		}
		if r.sess != nil && r.sess.Err() == nil {
			return r.sess, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		r.cond.Wait()
	}
}

// OpenTarget opens a raw stream through the relay to the access port of
// the sandbox named target, returning it as a net.Conn. Nothing has been
// said on it yet: the caller runs TLS and the sandbox handshake over it
// end to end, so the relay never sees the per-sandbox token. Most callers
// want Client instead.
func (r *RelayClient) OpenTarget(ctx context.Context, target string) (net.Conn, error) {
	meta, err := json.Marshal(RelayStreamRequest{Target: target})
	if err != nil {
		return nil, err
	}
	for attempt := 0; ; attempt++ {
		sess, err := r.session(ctx)
		if err != nil {
			return nil, err
		}
		st, err := sess.Open(ctx, meta)
		if err != nil {
			var rej *guestforward.RejectedError
			if errors.As(err, &rej) {
				return nil, fmt.Errorf("%w %q: %s", ErrTargetRefused, target, rej.Reason)
			}
			if sess.Err() != nil && ctx.Err() == nil && attempt < 3 {
				continue
			}
			return nil, err
		}
		return newStreamConn(st, target), nil
	}
}

// Client returns a Client for the sandbox named target, reached through the
// relay. opts carries the per-sandbox Token and the TLSConfig that verifies
// the sandbox's own certificate (its ServerName defaults to target). The
// Client redials through the relay -- waiting for the relay session to come
// back if it has dropped -- whenever its own session ends, so an exec cut
// by a dropped relay session can be resumed with Reattach.
func (r *RelayClient) Client(ctx context.Context, target string, opts DialOptions) (*Client, error) {
	var base *tls.Config
	if opts.TLSConfig != nil {
		base = opts.TLSConfig.Clone()
	} else {
		base = &tls.Config{}
	}
	base.MinVersion = tls.VersionTLS13
	if base.ServerName == "" {
		base.ServerName = target
	}
	return newRedialClient(ctx, func(ctx context.Context) (*guestforward.Session, string, error) {
		raw, err := r.OpenTarget(ctx, target)
		if err != nil {
			return nil, "", err
		}
		return clientHandshake(ctx, tls.Client(raw, base), opts)
	})
}

// Close ends the relay session and stops reconnecting. Clients obtained
// from it fail their next operation.
func (r *RelayClient) Close() error {
	r.mu.Lock()
	r.closed = true
	sess := r.sess
	r.cond.Broadcast()
	r.mu.Unlock()
	r.cancel()
	if sess != nil {
		return sess.Close()
	}
	return nil
}

// Connector hands out a Client for a named sandbox. Both transports a
// controller can use implement it -- *RelayClient for an off-cluster
// controller, DirectConnector for one inside the cluster -- so grain and
// grit can be written against one interface.
type Connector interface {
	Connect(ctx context.Context, target string) (*Client, error)
}

// DirectConnector reaches sandboxes by dialing their TCP listeners
// directly, for a controller running inside the cluster.
type DirectConnector struct {
	// Resolve maps a sandbox name to the address Dial takes, such as
	// "tls://10.4.2.17:7443".
	Resolve func(ctx context.Context, target string) (string, error)

	// Options returns the per-sandbox DialOptions (token, TLS) for target.
	Options func(target string) DialOptions
}

// Connect implements Connector.
func (d DirectConnector) Connect(ctx context.Context, target string) (*Client, error) {
	addr, err := d.Resolve(ctx, target)
	if err != nil {
		return nil, err
	}
	var opts DialOptions
	if d.Options != nil {
		opts = d.Options(target)
	}
	return Dial(ctx, addr, opts)
}

// RelayConnector adapts a RelayClient to Connector.
type RelayConnector struct {
	Relay *RelayClient

	// Options returns the per-sandbox DialOptions (token, TLS) for target.
	Options func(target string) DialOptions
}

// Connect implements Connector.
func (rc RelayConnector) Connect(ctx context.Context, target string) (*Client, error) {
	var opts DialOptions
	if rc.Options != nil {
		opts = rc.Options(target)
	}
	return rc.Relay.Client(ctx, target, opts)
}

// streamConn presents a relay stream as a net.Conn so crypto/tls can run
// over it. A deadline that passes resets the stream; that is enough for the
// handshake deadlines it is used with.
type streamConn struct {
	st     *guestforward.Stream
	target string

	mu    sync.Mutex
	timer *time.Timer
}

func newStreamConn(st *guestforward.Stream, target string) *streamConn {
	return &streamConn{st: st, target: target}
}

func (c *streamConn) Read(p []byte) (int, error)  { return c.st.Read(p) }
func (c *streamConn) Write(p []byte) (int, error) { return c.st.Write(p) }
func (c *streamConn) CloseWrite() error           { return c.st.CloseWrite() }

func (c *streamConn) Close() error {
	c.mu.Lock()
	if c.timer != nil {
		c.timer.Stop()
	}
	c.mu.Unlock()
	return c.st.Close()
}

func (c *streamConn) LocalAddr() net.Addr  { return relayAddr("controller") }
func (c *streamConn) RemoteAddr() net.Addr { return relayAddr(c.target) }

func (c *streamConn) SetDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	if !t.IsZero() {
		c.timer = time.AfterFunc(time.Until(t), func() { c.st.Reset("deadline exceeded") })
	}
	return nil
}

func (c *streamConn) SetReadDeadline(t time.Time) error  { return c.SetDeadline(t) }
func (c *streamConn) SetWriteDeadline(t time.Time) error { return c.SetDeadline(t) }

type relayAddr string

func (a relayAddr) Network() string { return "granule-relay" }
func (a relayAddr) String() string  { return string(a) }
