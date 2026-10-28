// Package portforward carries a capability grant's ports
// (docs/capabilities-spec.md §5.3) into a sandbox: TCP connections to
// 127.0.0.1:<sandbox_port> in the guest arrive at 127.0.0.1:<host_port> on
// this host, whatever the sandbox backend.
//
// Two halves. The Relay is an http.Handler the daemon serves on its
// sandbox fabric, beside the git proxy -- so it is reachable from every
// sandbox that can reach grain's own services, network_access: none
// included, and nothing else needs opening for it. Each forward is
// registered under a random token; the guest's forwarder (GuestForwarder,
// a small script grain places and starts in the sandbox) listens on the
// sandbox port and, per connection, upgrades a request to the relay
// carrying that token into a raw byte stream. The token is what keeps a
// mapping private to the sandbox it was materialized for (§5.3.4).
//
// Before a byte is bridged the relay dials host_port itself and checks,
// on that established connection, that the process owning its far end
// belongs to the capability server's own service (§5.3.2, Service); a
// connection that fails is closed, so a capability server cannot bridge a
// sandbox to grain's own ports or to an unrelated local service.
package portforward

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// UpgradeProtocol is the Upgrade token a guest forwarder asks for.
const UpgradeProtocol = "grain-capability-port"

// Path is where the relay accepts forwards.
const Path = "/forward"

// Relay is the host half of every capability port forward of this
// process. The zero value is not usable; NewRelay builds one.
type Relay struct {
	// Dial connects to a host_port; tests substitute their own.
	Dial func(ctx context.Context, port int) (net.Conn, error)
	// Owner reports the process at the far end of a dialled connection.
	Owner func(net.Conn) (int, error)
	// InodeOwner, when set, is asked who holds a socket inode open when
	// this process cannot find it in /proc/<pid>/fd itself -- a run: host
	// capability server started by grain hostd under another uid.
	InodeOwner func(ctx context.Context, inode uint64) (int, error)

	mu       sync.Mutex
	forwards map[string]*Forward
}

// NewRelay returns a relay with no forwards.
func NewRelay() *Relay {
	return &Relay{
		Dial: func(ctx context.Context, port int) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		},
		Owner:    ConnOwner,
		forwards: map[string]*Forward{},
	}
}

// Forward is one registered mapping: the host_port it reaches and the
// service whose listener it must find there.
type Forward struct {
	relay    *Relay
	token    string
	hostPort int
	service  Service

	mu     sync.Mutex
	closed bool
	conns  map[net.Conn]struct{}
}

// Open registers a forward to hostPort, after checking that a listener
// belonging to service is there now: a grant whose host_port has no
// qualifying listener at setup is refused (§5.3.2).
func (r *Relay) Open(ctx context.Context, hostPort int, service Service) (*Forward, error) {
	conn, err := r.verifiedDial(ctx, hostPort, service)
	if err != nil {
		return nil, err
	}
	conn.Close()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	f := &Forward{relay: r, token: hex.EncodeToString(raw), hostPort: hostPort, service: service, conns: map[net.Conn]struct{}{}}
	r.mu.Lock()
	r.forwards[f.token] = f
	r.mu.Unlock()
	return f, nil
}

// OpenOwn registers a forward to hostPort, a loopback listener of this
// process's own -- a capability grant's data-plane proxy (protocol.Endpoint)
// carried into a sandbox that cannot reach grain's loopback, a CI job's
// (docs/capabilities-spec.md §5.2.2) -- checked the way Open checks a
// capability server's, against this process instead.
func (r *Relay) OpenOwn(ctx context.Context, hostPort int) (*Forward, error) {
	return r.Open(ctx, hostPort, Service{PID: os.Getpid()})
}

// Token is the secret a guest forwarder presents for this forward.
func (f *Forward) Token() string { return f.token }

// HostPort is the port this forward reaches.
func (f *Forward) HostPort() int { return f.hostPort }

// Close unregisters the forward and closes every connection it is
// carrying (§5.3.4). Safe to call more than once.
func (f *Forward) Close() {
	f.relay.mu.Lock()
	delete(f.relay.forwards, f.token)
	f.relay.mu.Unlock()
	f.mu.Lock()
	f.closed = true
	conns := f.conns
	f.conns = map[net.Conn]struct{}{}
	f.mu.Unlock()
	for c := range conns {
		c.Close()
	}
}

func (f *Forward) track(c net.Conn) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return false
	}
	f.conns[c] = struct{}{}
	return true
}

func (f *Forward) untrack(c net.Conn) {
	f.mu.Lock()
	delete(f.conns, c)
	f.mu.Unlock()
}

// verifiedDial connects to hostPort and checks the established
// connection's far end belongs to service, closing it if not.
func (r *Relay) verifiedDial(ctx context.Context, hostPort int, service Service) (net.Conn, error) {
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := r.Dial(dctx, hostPort)
	if err != nil {
		return nil, fmt.Errorf("nothing is listening on host_port %d: %w", hostPort, err)
	}
	pid, err := r.owner(dctx, conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("the listener on host_port %d could not be attributed to a process: %w", hostPort, err)
	}
	if !service.Contains(pid) {
		conn.Close()
		return nil, fmt.Errorf("the listener on host_port %d belongs to process %d, which is not part of the capability server's own service", hostPort, pid)
	}
	return conn, nil
}

func (r *Relay) owner(ctx context.Context, conn net.Conn) (int, error) {
	if r.InodeOwner == nil {
		return r.Owner(conn)
	}
	inode, err := connInode(conn)
	if err != nil {
		return 0, err
	}
	if pid, err := inodeOwner(inode); err == nil {
		return pid, nil
	}
	return r.InodeOwner(ctx, inode)
}

func (r *Relay) lookup(token string) *Forward {
	r.mu.Lock()
	defer r.mu.Unlock()
	for t, f := range r.forwards {
		if subtle.ConstantTimeCompare([]byte(t), []byte(token)) == 1 {
			return f
		}
	}
	return nil
}

// ServeHTTP accepts one forwarded connection from a guest forwarder.
func (r *Relay) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.URL.Path != Path || !strings.EqualFold(req.Header.Get("Upgrade"), UpgradeProtocol) {
		http.NotFound(w, req)
		return
	}
	token, ok := strings.CutPrefix(req.Header.Get("Authorization"), "Bearer ")
	f := r.lookup(strings.TrimSpace(token))
	if !ok || f == nil {
		http.Error(w, "unknown forward", http.StatusForbidden)
		return
	}
	upstream, err := r.verifiedDial(req.Context(), f.hostPort, f.service)
	if err != nil {
		log.Printf("capability ports: refusing a connection to host_port %d: %v", f.hostPort, err)
		http.Error(w, "host_port unavailable", http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		upstream.Close()
		http.Error(w, "connection cannot be upgraded", http.StatusInternalServerError)
		return
	}
	guest, buf, err := hj.Hijack()
	if err != nil {
		upstream.Close()
		return
	}
	// The server's header deadlines must not cut a long-lived stream.
	guest.SetDeadline(time.Time{})
	if !f.track(guest) || !f.track(upstream) {
		guest.Close()
		upstream.Close()
		return
	}
	defer f.untrack(guest)
	defer f.untrack(upstream)
	if _, err := io.WriteString(guest, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: "+UpgradeProtocol+"\r\nConnection: Upgrade\r\n\r\n"); err != nil {
		guest.Close()
		upstream.Close()
		return
	}
	bridge(guest, buf.Reader, upstream)
}

// bridge copies both ways until both directions end; from is what the
// guest had already sent past the request.
func bridge(guest net.Conn, from io.Reader, upstream net.Conn) {
	done := make(chan struct{})
	go func() {
		io.Copy(upstream, from)
		closeWrite(upstream)
		close(done)
	}()
	io.Copy(guest, upstream)
	closeWrite(guest)
	<-done
	guest.Close()
	upstream.Close()
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		cw.CloseWrite()
		return
	}
	c.Close()
}
