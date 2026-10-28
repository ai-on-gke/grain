package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
)

// This file is the last two steps of materializing a pass
// (docs/capabilities-spec.md §5, steps 5 and 6): every grant's
// mcp_endpoint proxied, handshaken and named (§5.2), and every grant's
// prompt_section rendered as an attributed block (§5.4). Each proxy is a
// loopback listener on grain's own host, where the agent CLI runs, aimed
// at the capability server rather than at the sandbox -- a CLI on a
// remote executor is given one on the executor instead, whose requests
// its stream carries back to the same proxy (agent.CarryCapabilityServers,
// modelexec's capability routes) -- so a
// recreate_sandbox leaves it pointing where it did: rebuilding the VM
// only checks every endpoint still answers (§5.2.5).

// capabilityHandshakeTimeout bounds one endpoint's initialize and
// tools/list.
const capabilityHandshakeTimeout = 30 * time.Second

// capabilityEndpoint is one grant's proxied mcp_endpoint.
type capabilityEndpoint struct {
	grant     string
	attemptID string
	endpoint  *protocol.Endpoint
}

// wireEndpoints proxies every grant's mcp_endpoint on dataPlane(the
// grant's capability_id), handshakes each through its proxy, names the
// servers (protocol.AssignServerNames) and renders the pass's prompt
// sections under those names. A failure is a validation failure that
// refuses the run (§5); whatever it opened is closed by Close.
func (m *passMaterialization) wireEndpoints(ctx context.Context, dataPlane func(capabilityID string) (protocol.DataPlane, error)) error {
	reported := make([]string, len(m.pass.Grants))
	rosters := make([][]string, len(m.pass.Grants))
	urls := make([]string, len(m.pass.Grants))
	handlers := make([]http.Handler, len(m.pass.Grants))
	proxies := make([]*protocol.Proxy, len(m.pass.Grants))
	labels := make([]map[string]string, len(m.pass.Grants))
	for i, g := range m.pass.Grants {
		if g.Response.MCPEndpoint == "" {
			continue
		}
		refuse := func(reason string) error {
			return &protocol.RefusedError{Grant: g.Ref.String(), Reason: "its mcp_endpoint " + reason}
		}
		if dataPlane == nil {
			return refuse("cannot be reached: this deployment attaches no capability servers")
		}
		dp, err := dataPlane(g.Ref.CapabilityID)
		if err != nil {
			return refuse("cannot be reached: " + err.Error())
		}
		proxy, err := protocol.NewProxy(dp, g.AttemptID, nil)
		if err != nil {
			return refuse(err.Error())
		}
		ep, err := protocol.ServeEndpoint(proxy)
		if err != nil {
			return refuse("could not be proxied: " + err.Error())
		}
		m.endpoints = append(m.endpoints, capabilityEndpoint{grant: g.Ref.String(), attemptID: g.AttemptID, endpoint: ep})
		hctx, cancel := context.WithTimeout(ctx, capabilityHandshakeTimeout)
		roster, err := protocol.Handshake(hctx, ep.URL)
		cancel()
		if err != nil {
			return refuse("failed its handshake: " + err.Error())
		}
		if roster.ServerName == "" {
			return refuse("reports no serverInfo.name")
		}
		reported[i], rosters[i], urls[i], handlers[i] = roster.ServerName, roster.Tools, ep.URL, ep.Handler()
		proxies[i], labels[i] = proxy, roster.Labels
	}
	names, err := protocol.AssignServerNames(reported)
	if err != nil {
		return &protocol.RefusedError{Reason: err.Error()}
	}
	m.servers = nil
	for i, name := range names {
		if name == "" {
			continue
		}
		m.servers = append(m.servers, agent.CapabilityMCPServer{Name: name, URL: urls[i], Tools: rosters[i], Handler: handlers[i]})
		// Every call the agent makes on the server says so on the task's
		// row, under the name the agent knows it by: these calls never
		// pass through grain's own MCP server, whose registry narrates
		// the rest (mcp.CapabilityNarrator).
		if m.narrator != nil {
			proxies[i].ObserveToolCalls(m.narrator.Observer(name, reported[i], labels[i]))
		}
	}
	m.promptSections = m.pass.PromptSections(names)
	return nil
}

// checkEndpoints handshakes every proxied endpoint again, for a
// recreate_sandbox (§5.2.5): the proxies still point at the same
// still-active /runs/<attempt_id>/mcp, and an endpoint that no longer
// answers is reported rather than ending the attempt.
func (m *passMaterialization) checkEndpoints(ctx context.Context) error {
	var failed []string
	for _, e := range m.endpoints {
		hctx, cancel := context.WithTimeout(ctx, capabilityHandshakeTimeout)
		_, err := protocol.Handshake(hctx, e.endpoint.URL)
		cancel()
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s (%s): %v", e.grant, e.attemptID, err))
		}
	}
	if len(failed) > 0 {
		return errors.New("these capability MCP servers no longer answer: " + strings.Join(failed, "; "))
	}
	return nil
}

// closeEndpoints stops every proxy of the attempt.
func (m *passMaterialization) closeEndpoints() {
	for _, e := range m.endpoints {
		e.endpoint.Close()
	}
	m.endpoints = nil
}

// taskActivityReporter is mcp.StatusReporter straight onto a task's live
// run in the store: how the daemon, which serves a run's capability
// proxies, narrates the calls made through them, where the run's own MCP
// server goes through the daemon's HTTP API (daemon.daemonStatus).
type taskActivityReporter struct {
	store  *model.Store
	taskID string
	now    func() time.Time
}

func (r taskActivityReporter) ReportStatus(ctx context.Context, note string) (mcp.StatusReport, error) {
	live, err := r.store.SetTaskActivity(ctx, r.taskID, note, r.now())
	return mcp.StatusReport{Live: live}, err
}

// capabilityDataPlane is where the server admission minted through serves
// its attempt toolsets.
func (d Deps) capabilityDataPlane(capabilityID string) (protocol.DataPlane, error) {
	if d.CapabilityServers == nil {
		return protocol.DataPlane{}, errors.New("this deployment attaches no capability servers")
	}
	srv, err := d.CapabilityServers.Lookup(capabilityID)
	if err != nil {
		return protocol.DataPlane{}, err
	}
	return srv.DataPlane()
}
