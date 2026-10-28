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

// Package hostport binds ports on the grain host and forwards every
// connection made to one through a dial function -- how a persistent
// sandbox's host_port reaches the port its service listens on inside the
// sandbox (orchestrator.PersistentSandboxSupervisor).
//
// The dial function is asked per connection rather than once, so a
// listener outlives the sandbox behind it: a connection made while the
// sandbox is restarting is refused, and the next one made after it is up
// reaches the new one, with nothing to rebind.
package hostport

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"
)

// DialFunc opens one connection to whatever a port forwards to.
type DialFunc func(ctx context.Context) (net.Conn, error)

// dialTimeout bounds one dial into a sandbox.
const dialTimeout = 30 * time.Second

// Forwarder holds the listeners. The zero value is not usable; New.
type Forwarder struct {
	// Host is the address listeners bind on; "" is every interface.
	Host string

	mu        sync.Mutex
	listeners map[int]*listener
}

// New builds a Forwarder binding on host.
func New(host string) *Forwarder {
	return &Forwarder{Host: host, listeners: make(map[int]*listener)}
}

type listener struct {
	ln   net.Listener
	mu   sync.Mutex
	dial DialFunc
}

// Sync makes the bound set exactly ports: it closes listeners on ports no
// longer wanted, opens ones on new ports, and points kept ones at their
// (possibly new) dial function. It returns, by port, why a port could not
// be bound.
func (f *Forwarder) Sync(ports map[int]func(ctx context.Context) (net.Conn, error)) map[int]error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for port, l := range f.listeners {
		if _, ok := ports[port]; !ok {
			_ = l.ln.Close()
			delete(f.listeners, port)
		}
	}
	errs := make(map[int]error)
	for port, dial := range ports {
		if l := f.listeners[port]; l != nil {
			l.mu.Lock()
			l.dial = dial
			l.mu.Unlock()
			continue
		}
		ln, err := net.Listen("tcp", net.JoinHostPort(f.Host, fmt.Sprint(port)))
		if err != nil {
			errs[port] = fmt.Errorf("binding host port %d: %w", port, err)
			continue
		}
		l := &listener{ln: ln, dial: dial}
		f.listeners[port] = l
		go l.serve()
	}
	return errs
}

// Addr is the address port's listener is bound to, for a test that asked
// for port 0's neighbour; nil when it is not bound.
func (f *Forwarder) Addr(port int) net.Addr {
	f.mu.Lock()
	defer f.mu.Unlock()
	if l := f.listeners[port]; l != nil {
		return l.ln.Addr()
	}
	return nil
}

// Close closes every listener.
func (f *Forwarder) Close() {
	f.Sync(nil)
}

func (l *listener) serve() {
	for {
		conn, err := l.ln.Accept()
		if err != nil {
			return
		}
		l.mu.Lock()
		dial := l.dial
		l.mu.Unlock()
		go forward(conn, dial)
	}
}

// forward splices one accepted connection to a freshly dialled one, and
// closes both once either side is done.
func forward(conn net.Conn, dial DialFunc) {
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	upstream, err := dial(ctx)
	cancel()
	if err != nil {
		log.Printf("hostport: forwarding %s: %v", conn.LocalAddr(), err)
		return
	}
	defer upstream.Close()
	Splice(conn, upstream)
}

// Splice copies a to b and b to a until either direction ends, half-
// closing where the connection allows it so a request-then-EOF protocol
// still gets its answer.
func Splice(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	cp := func(dst, src net.Conn) {
		defer wg.Done()
		_, _ = io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			_ = dst.Close()
		}
	}
	go cp(a, b)
	go cp(b, a)
	wg.Wait()
}
