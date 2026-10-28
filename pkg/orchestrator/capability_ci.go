package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"strconv"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/portforward"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
)

// This file carries a CI job's pass (minted by pkg/daemon's grant
// admission) into the job's sandbox, in the order docs/capabilities-spec.md
// §5 sets: the same ports, placements and setup an agent run's pass gets
// (capability_materialize.go), then every mcp_endpoint proxied, handshaken
// and named as for an agent (capability_endpoints.go). A job's steps run
// inside the sandbox rather than on grain's host, though, so each proxy is
// also forwarded into the sandbox over the capability port relay -- which
// is on the sandbox fabric, so it is reachable under network_access: none
// -- and listed in /home/debian/.grain/mcp.json (§5.2.4 item 4).

// CIMCPConfigPath is where a CI job finds its capability MCP servers.
const CIMCPConfigPath = "/home/debian/.grain/mcp.json"

// ciEndpointPortBase is the first guest loopback port a job's proxied
// endpoints are forwarded to; each further endpoint takes the next port
// no grant of the pass maps. A variable only so a test running the guest
// forwarder on its own host can move it off a port that host uses.
var ciEndpointPortBase = 38017

// CIPassOptions are what a CI job's pass is materialized with.
type CIPassOptions struct {
	// Ports and PortsURL are the daemon's capability port relay and where
	// a sandbox reaches it (Config.CapabilityPorts, CapabilityPortsURL).
	Ports    *portforward.Relay
	PortsURL string
	// SetupTimeout bounds each setup command; zero is
	// DefaultCapabilitySetupTimeout.
	SetupTimeout time.Duration
	// Servers are the capability servers the pass was minted through,
	// whose services and data planes its grants are carried from.
	Servers CapabilityServers
	// Now is the clock; nil is the wall clock.
	Now func() time.Time
}

// CIPassMaterialization is one CI job's pass as it has been carried into
// its sandbox: the relay forwards and endpoint proxies it holds open on
// grain's host, which outlive the sandbox, so the job's retry in a fresh
// sandbox is pointed at the same ones (Apply). Like the pass it holds
// leased secrets and is never logged or stored.
type CIPassMaterialization struct {
	m *passMaterialization
	// exposed are the relay forwards to each endpoint proxy, by the
	// guest port it is listed under, and config the mcp.json naming them.
	exposed []openForward
	config  string
}

// MaterializeCIPass materializes pass into sandbox, a CI job's, before
// any of the job's steps runs. On error, whatever it opened has already
// been closed.
func MaterializeCIPass(ctx context.Context, opts CIPassOptions, sandbox Sandbox, pass *protocol.Pass, deadline time.Time) (*CIPassMaterialization, error) {
	cfg := Config{CapabilityPorts: opts.Ports, CapabilityPortsURL: opts.PortsURL, CapabilitySetupTimeout: opts.SetupTimeout, Now: opts.Now}
	deps := Deps{CapabilityServers: opts.Servers}
	m, err := materializePass(ctx, cfg, sandbox, pass, deadline, deps.capabilityService)
	if err != nil {
		return nil, err
	}
	c := &CIPassMaterialization{m: m}
	if err := m.wireEndpoints(ctx, deps.capabilityDataPlane); err != nil {
		c.Close()
		return nil, err
	}
	if err := c.exposeEndpoints(ctx, cfg); err != nil {
		c.Close()
		return nil, err
	}
	if err := c.applyEndpoints(ctx, sandbox); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// exposeEndpoints opens a relay forward to each endpoint proxy and writes
// the mcp.json listing it at the guest port it will be forwarded to.
func (c *CIPassMaterialization) exposeEndpoints(ctx context.Context, cfg Config) error {
	if len(c.m.servers) == 0 {
		return nil
	}
	if cfg.CapabilityPorts == nil || cfg.CapabilityPortsURL == "" {
		return &protocol.RefusedError{Reason: "this deployment serves no capability port relay, so a CI job's sandbox cannot reach its capability MCP servers"}
	}
	if c.m.relayHost == "" {
		host, port, err := portforward.RelayAddress(cfg.CapabilityPortsURL)
		if err != nil {
			return err
		}
		c.m.relayHost, c.m.relayPort = host, port
	}
	taken := map[int]bool{}
	for _, p := range c.m.pass.Ports() {
		taken[p.SandboxPort] = true
	}
	next := ciEndpointPortBase
	type entry struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	}
	servers := map[string]entry{}
	for _, s := range c.m.servers {
		u, err := url.Parse(s.URL)
		if err != nil {
			return fmt.Errorf("the proxy of capability MCP server %s: %w", s.Name, err)
		}
		hostPort, err := strconv.Atoi(u.Port())
		if err != nil {
			return fmt.Errorf("the proxy of capability MCP server %s names no port", s.Name)
		}
		f, err := cfg.CapabilityPorts.OpenOwn(ctx, hostPort)
		if err != nil {
			return fmt.Errorf("forwarding capability MCP server %s into the sandbox: %w", s.Name, err)
		}
		for taken[next] {
			next++
		}
		taken[next] = true
		c.exposed = append(c.exposed, openForward{sandboxPort: next, forward: f})
		servers[s.Name] = entry{Type: "http", URL: "http://127.0.0.1:" + strconv.Itoa(next) + u.Path}
	}
	data, err := json.MarshalIndent(map[string]any{"mcpServers": servers}, "", "  ")
	if err != nil {
		return err
	}
	c.config = string(data) + "\n"
	return nil
}

// applyEndpoints starts the guest end of every endpoint's forward in
// sandbox and writes its mcp.json there.
func (c *CIPassMaterialization) applyEndpoints(ctx context.Context, sandbox Sandbox) error {
	if len(c.exposed) == 0 {
		return nil
	}
	commander, ok := sandboxRunsCommands(sandbox)
	if !ok {
		return fmt.Errorf("this sandbox backend (%T) cannot run commands in its sandbox, so it cannot reach capability MCP servers", sandbox)
	}
	if err := c.m.startForwarders(ctx, sandbox, commander, c.exposed); err != nil {
		return err
	}
	if err := placeInSandbox(ctx, sandbox, commander, protocol.Placement{Path: CIMCPConfigPath, Mode: "0600", Content: c.config}); err != nil {
		return fmt.Errorf("writing %s: %w", CIMCPConfigPath, err)
	}
	return nil
}

// Apply carries the materialization into sandbox, a fresh one built for
// the same job -- its automatic retry -- without minting: the same
// forwards and tokens, placements written and setup run again, and every
// endpoint proxy, still aimed at the same still-active
// /runs/<attempt_id>/mcp, forwarded into it under the same mcp.json
// (§4.4.3, §5.2.5, §5.5.5). An endpoint that no longer answers is logged
// rather than failing the job: its own steps will say what they could not
// reach.
func (c *CIPassMaterialization) Apply(ctx context.Context, sandbox Sandbox) error {
	if err := c.m.apply(ctx, sandbox); err != nil {
		return err
	}
	if err := c.applyEndpoints(ctx, sandbox); err != nil {
		return err
	}
	if err := c.m.checkEndpoints(ctx); err != nil {
		log.Printf("orchestrator: CI sandbox %s: %v", sandbox.Name(), err)
	}
	return nil
}

// MCPConfig is the mcp.json written into the job's sandbox, "" when the
// pass has no mcp_endpoint.
func (c *CIPassMaterialization) MCPConfig() string {
	if c == nil {
		return ""
	}
	return c.config
}

// Close closes every forward and proxy of the job (§5.3.4). The pass
// itself is released by whoever holds it.
func (c *CIPassMaterialization) Close() {
	if c == nil {
		return
	}
	for _, f := range c.exposed {
		f.forward.Close()
	}
	c.exposed = nil
	c.m.Close()
}
