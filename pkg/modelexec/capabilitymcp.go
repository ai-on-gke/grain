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

package modelexec

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"

	"github.com/ai-on-gke/grain/pkg/agent"
)

// A capability grant's mcp_endpoint is proxied by grain on a loopback
// listener of its own (protocol.ServeEndpoint), which a CLI on a remote
// executor cannot reach. So the executor hosts a loopback listener of its
// own (Daemon.runOnce), announces it in its hello, and carries every
// connection to it back to grain as a StreamTypeCapabilityMCP stream;
// grain serves HTTP on that stream with this connection's capability
// routes (design/spec/capabilities/capabilities.spec.md §5.2.4, last paragraph).
//
// Each route is a random path naming one run's server, registered by
// RemoteHost.CarryCapabilityServers for as long as that run holds it, and
// is served by the server's own protocol.Proxy, here on the controller:
// that is where the path becomes /runs/<attempt_id>/mcp, where only POST,
// GET and DELETE pass, where the client's Authorization and hop-by-hop
// headers are dropped and where the capability server's own headers are
// injected (§5.2.2). The executor only ever moves bytes, so nothing it
// does to a request reaches further than those rules, and it never sees
// the server's credentials.
//
// The routes live on the connection rather than in a table keyed by the
// executor's instance id, for mcpGrants' reason: a run's CLI only ever
// talks to grain over the connection it was dispatched on, and the id is
// the executor's own word.

// capabilityRoutes is the capability MCP servers carried for runs on one
// executor connection, by the path the executor's listener serves each at.
type capabilityRoutes struct {
	mu     sync.Mutex
	routes map[string]http.Handler
}

func (c *capabilityRoutes) add(path string, h http.Handler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.routes == nil {
		c.routes = make(map[string]http.Handler)
	}
	c.routes[path] = h
}

func (c *capabilityRoutes) remove(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.routes, path)
}

// ServeHTTP serves a request carried from the executor with the route its
// path names exactly. Anything else -- a released run's path, a guess --
// is not found.
func (c *capabilityRoutes) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	h := c.routes[r.URL.Path]
	c.mu.Unlock()
	if h == nil {
		http.NotFound(w, r)
		return
	}
	h.ServeHTTP(w, r)
}

// capabilityRoutePath is a fresh route: a secret path, as the loopback
// listener grain serves the same proxy on has (protocol.ServeEndpoint),
// since loopback on the executor is no trust boundary either.
func capabilityRoutePath() (string, error) {
	secret := make([]byte, 16)
	if _, err := rand.Read(secret); err != nil {
		return "", err
	}
	return "/" + hex.EncodeToString(secret) + "/mcp", nil
}

// loopbackHTTPOrigin reports whether raw, the listener an executor
// announced, is a plain http origin on a loopback address: the only kind
// of address the executor is meant to have announced, and the only kind a
// CLI is handed.
func loopbackHTTPOrigin(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	ip := net.ParseIP(u.Hostname())
	return ip != nil && ip.IsLoopback() && u.Port() != ""
}

// CarryCapabilityServers implements agent.CapabilityServerCarrier: each
// server is given a route on this connection and the URL of that route on
// the executor's own listener, until release.
func (h *RemoteHost) CarryCapabilityServers(servers []agent.CapabilityMCPServer) ([]agent.CapabilityMCPServer, func(), error) {
	origin := h.conn.capabilityMCPURL
	if origin == "" {
		return nil, nil, fmt.Errorf("this run's capability grants expose MCP servers, and its agent CLI runs on executor %s, whose grain predates carrying them; upgrade grain there", h.conn.name)
	}
	if !loopbackHTTPOrigin(origin) {
		return nil, nil, fmt.Errorf("executor %s announced %q for capability MCP servers, which is not a loopback http address", h.conn.name, origin)
	}
	var paths []string
	release := func() {
		for _, p := range paths {
			h.conn.capabilityRoutes.remove(p)
		}
	}
	carried := make([]agent.CapabilityMCPServer, 0, len(servers))
	for _, s := range servers {
		if s.Handler == nil {
			release()
			return nil, nil, fmt.Errorf("capability MCP server %s has no proxy grain could carry to executor %s", s.Name, h.conn.name)
		}
		path, err := capabilityRoutePath()
		if err != nil {
			release()
			return nil, nil, err
		}
		h.conn.capabilityRoutes.add(path, s.Handler)
		paths = append(paths, path)
		s.URL = origin + path
		carried = append(carried, s)
	}
	return carried, release, nil
}
