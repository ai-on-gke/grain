// Package guestforward forwards TCP (and unix-socket) connections into
// and out of a granule VM's guest over the same channel "granule exec"
// uses -- `docker exec` or `kubectl exec` into the VM's container, the
// shim, vsock, granule-agent -- rather than over the guest's network.
//
// It is out-of-band tunnelling and nothing else. The guest's real
// networking -- its NIC, routes, DNS, egress, NetworkPolicy, Services --
// stays whatever Docker or Kubernetes made it; nothing here touches an
// interface or a route. A forwarded port inside the guest is a loopback
// listener the agent holds open, backed by vsock, so it works the same
// for a guest with no network at all.
//
// Two directions, as with ssh -L and -R:
//
//   - Local (caller → guest): the caller binds a port; each connection
//     to it opens a stream to the agent, which dials a target inside the
//     guest and splices.
//   - Remote (guest → caller): the agent binds a loopback port inside the
//     guest; each connection to it opens a stream back to the caller,
//     which dials a target on the caller's side and splices.
//
// One transport carries every mapping of a Forwarder at once, multiplexed
// (see mux.go): one stream per listener the agent holds, and one per
// forwarded connection. Mappings live exactly as long as the transport:
// when it goes, the agent closes every listener and connection it opened
// for it, and a Forwarder with Reconnect set opens a new transport and
// asks again.
//
// Trust is exactly that of "granule exec": anyone who can exec into the
// VM's container can already run any command in the guest as root, and
// can therefore forward. There is no authentication of its own here.
//
// The guest end lives in granule-agent (pkg/agent hands it a connection
// whose execwire.Request asked for Forward); the container end is the
// shim's "forward-stdio" mode, a plain pipe between its stdio and that
// connection; the caller end is Forwarder, driven by the `granule
// forward` CLI or embedded directly by a program that would rather not
// supervise a subprocess.
package guestforward

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// ShimMode is the argument the VM container's granule binary takes to
// relay a forwarding session on its stdio: a transport runs
// `granule forward-stdio` in the container.
const ShimMode = "forward-stdio"

// Operations a stream's opening metadata names.
const (
	opListen   = "listen"   // caller → agent: bind Addr in the guest
	opConnect  = "connect"  // caller → agent: dial Addr in the guest
	opAccepted = "accepted" // agent → caller: a connection to listener Listener
)

// openMsg is the metadata a stream is opened with.
type openMsg struct {
	Op   string `json:"op"`
	Addr string `json:"addr,omitempty"`

	// AnyAddr lets a listen bind an address that is not loopback.
	AnyAddr bool `json:"any_addr,omitempty"`

	// Listener names, on an accepted stream, the listen stream whose
	// listener took the connection.
	Listener uint32 `json:"listener,omitempty"`
}

// listenReply is what the agent confirms a listen with.
type listenReply struct {
	// Addr is the address actually bound, which differs from the one
	// asked for when that one's port was 0.
	Addr string `json:"addr"`
}

func encode(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// ParseAddr resolves an address as the forward flags spell it into a
// network and address for net.Dial or net.Listen:
//
//	9728             127.0.0.1:9728
//	host:9728        host:9728 (and [::1]:9728)
//	unix:/run/x.sock a unix socket at /run/x.sock
//
// A bare port means loopback rather than every interface, which is the
// safe reading on both ends.
func ParseAddr(s string) (network, address string, err error) {
	if s == "" {
		return "", "", fmt.Errorf("empty address")
	}
	if path, ok := strings.CutPrefix(s, "unix:"); ok {
		if path == "" {
			return "", "", fmt.Errorf("address %q: unix: needs a socket path", s)
		}
		return "unix", path, nil
	}
	if p, err := strconv.Atoi(s); err == nil {
		if p < 0 || p > 65535 {
			return "", "", fmt.Errorf("address %q: port out of range", s)
		}
		return "tcp", net.JoinHostPort("127.0.0.1", s), nil
	}
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return "", "", fmt.Errorf("address %q: want host:port, a bare port, or unix:/path: %w", s, err)
	}
	if p, err := strconv.Atoi(port); err != nil || p < 0 || p > 65535 {
		return "", "", fmt.Errorf("address %q: bad port %q", s, port)
	}
	return "tcp", net.JoinHostPort(host, port), nil
}

// isLoopback reports whether a listen address only accepts connections
// from the machine it is on. Unix sockets count: they are not reachable
// over a network at all.
func isLoopback(network, address string) bool {
	if network == "unix" {
		return true
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// formatAddr renders a bound address the way ParseAddr reads it back.
func formatAddr(a net.Addr) string {
	if a.Network() == "unix" {
		return "unix:" + a.String()
	}
	return a.String()
}
