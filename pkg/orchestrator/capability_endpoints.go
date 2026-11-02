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
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
)

// This file is the last two steps of materializing a pass
// (design/spec/capabilities/capabilities.spec.md §5, steps 5 and 6): every grant's
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
	rosters := make([]capabilityMCPTools, len(m.pass.Grants))
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
		tools, err := probeCapabilityMCPTools(hctx, ep.URL, g.MCPDelivery)
		cancel()
		if err != nil {
			return refuse("failed its handshake: " + err.Error())
		}
		if tools.ServerName == "" {
			return refuse("reports no serverInfo.name")
		}
		reported[i], rosters[i], urls[i], handlers[i] = tools.ServerName, tools, ep.URL, ep.Handler()
		proxies[i], labels[i] = proxy, tools.Labels
	}
	names, err := protocol.AssignServerNames(reported)
	if err != nil {
		return &protocol.RefusedError{Reason: err.Error()}
	}
	m.servers, m.capServers = nil, nil
	for i, name := range names {
		if name == "" {
			continue
		}
		// The model-only tools are the agent CLI's, on its own MCP
		// configuration; the sandbox tools are grain cap's, on the
		// sandbox proxy (§5.2.6). A server with none of one kind is
		// simply absent from that side.
		if len(rosters[i].ModelOnlyTools) > 0 {
			m.servers = append(m.servers, agent.CapabilityMCPServer{Name: name, URL: urls[i], Tools: rosters[i].ModelOnlyTools, Handler: handlers[i]})
		}
		if len(rosters[i].SandboxCLITools) > 0 {
			m.capServers = append(m.capServers, gitproxy.CapServer{Name: name, Grant: m.pass.Grants[i].Ref.String(), Tools: rosters[i].Specs, Handler: handlers[i]})
		}
		// Every call the agent makes on the server says so on the task's
		// row, under the name the agent knows it by: these calls never
		// pass through grain's own MCP server, whose registry narrates
		// the rest (mcp.CapabilityNarrator).
		if m.narrator != nil {
			proxies[i].ObserveToolCalls(m.narrator.Observer(name, reported[i], labels[i]))
		}
	}
	m.promptSections = joinPromptSections(m.pass.PromptSections(names), capabilityCLISection(m.capServers))
	return nil
}

// capabilityMCPTools is what probeCapabilityMCPTools learned of one
// grant's endpoint: its handshake, and its tools partitioned by their
// resolved delivery (design/spec/capabilities/capabilities.spec.md §5.2.6).
type capabilityMCPTools struct {
	protocol.EndpointRoster
	// ModelOnlyTools are wired to the agent CLI's own MCP configuration
	// and refused on the sandbox proxy; SandboxCLITools are reached from
	// the sandbox through grain cap and summarized in the prompt.
	ModelOnlyTools  []string
	SandboxCLITools []string
	// Specs are every tool with its delivery resolved, as the sandbox
	// proxy checks and lists them.
	Specs []gitproxy.CapTool
}

// probeCapabilityMCPTools handshakes url, a grant's endpoint proxy, and
// classifies each tool it lists: the tool's own annotations.modelOnly
// (or x-grain-model-only) wins, else methodDelivery, its mint_<method>'s
// mcpDelivery, else model-only -- an external capability server's tools
// stay on the model's channel until their author opts them into the
// sandbox.
func probeCapabilityMCPTools(ctx context.Context, url, methodDelivery string) (capabilityMCPTools, error) {
	roster, err := protocol.Handshake(ctx, url)
	if err != nil {
		return capabilityMCPTools{}, err
	}
	out := capabilityMCPTools{EndpointRoster: roster}
	for _, t := range roster.Specs {
		modelOnly := protocol.ResolveModelOnly(t.ModelOnly, methodDelivery)
		if modelOnly {
			out.ModelOnlyTools = append(out.ModelOnlyTools, t.Name)
		} else {
			out.SandboxCLITools = append(out.SandboxCLITools, t.Name)
		}
		out.Specs = append(out.Specs, gitproxy.CapTool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema, ModelOnly: modelOnly})
	}
	return out, nil
}

// capabilityCLISection tells the agent which grain cap commands its
// grants unlocked. It names servers and tools only -- both checked
// against a strict pattern at the handshake -- and never a tool's own
// description, which is the server's untrusted text and belongs inside
// its attributed prompt_section.
func capabilityCLISection(servers []gitproxy.CapServer) string {
	if len(servers) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<capability-cli>\nThese capability tools are commands in the sandbox, not MCP tools: run `grain cap <server> <tool> --help` for a tool's flags, ")
	b.WriteString("`grain cap <server> <tool> [flags | --json '<args>']` to call it, and `grain cap list` to see them all.\n")
	for _, s := range servers {
		for _, t := range s.Tools {
			if !t.ModelOnly {
				fmt.Fprintf(&b, "- grain cap %s %s\n", s.Name, t.Name)
			}
		}
	}
	b.WriteString("</capability-cli>")
	return b.String()
}

func joinPromptSections(sections ...string) string {
	var parts []string
	for _, s := range sections {
		if s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n\n")
}

// registerSandboxCapabilities makes the pass's sandbox tools reachable
// under /cap/ for key, until Close.
func (m *passMaterialization) registerSandboxCapabilities(registry *gitproxy.CapRegistry, key string) {
	if m.unregisterCap != nil {
		m.unregisterCap()
		m.unregisterCap = nil
	}
	if registry == nil || len(m.capServers) == 0 {
		return
	}
	m.unregisterCap = registry.Register(key, m.capServers)
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
	if m.unregisterCap != nil {
		m.unregisterCap()
		m.unregisterCap = nil
	}
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
