package intree

import (
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
)

// listen binds a data-plane listener on a loopback address and serves h
// on it until the server closes. Data-plane ports are reached only through
// Grain's forwarders (§5.3.3), so anything but loopback is refused.
func (s *Server) listen(addr string, h http.Handler) (*net.TCPAddr, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("listen address %q: %v", addr, err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("listen address %q is not a loopback IP address", addr)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 30 * time.Second}
	go srv.Serve(ln)
	s.onClose(func() error { return srv.Close() })
	if s.addr == "" {
		s.addr = ln.Addr().String()
	}
	return ln.Addr().(*net.TCPAddr), nil
}

// listenAddr is where a data-plane listener given addr binds: addr
// itself, or an ephemeral port on its host when Grain started this
// process only to probe it (protocol.ProbeEnv). A probe never mints, and
// the entry's attached instance may already hold addr's port.
func listenAddr(env Env, addr string) string {
	if env == nil || env(protocol.ProbeEnv) != "1" {
		return addr
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		// listen refuses it with the reason.
		return addr
	}
	return net.JoinHostPort(host, "0")
}
