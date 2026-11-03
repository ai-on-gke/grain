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

package daemon

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/capability/servers"
	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// hostdCapabilityServers starts run: hostd capability servers -- host
// scripts among them -- on the grain hostd each one's own entry names
// (protocol.ServerConfig.Hostd), rather than only on the default hostd.
// Which hostd a server runs on is the server's placement: it is the same
// for every run that holds a grant of it, whatever hostd that run's
// agent CLI went to (connectionHostdRoute).
//
// The deployment's registry (startCapabilityServers) attaches every
// server but the placed ones (deploymentCapabilityServers), its run:
// hostd ones through the default hostd. A placed server is attached in
// the registry of the hostd its placement resolves to: one per hostd
// name, holding the placed entries that may resolve there (its name's,
// and every label selector's), built with the deployment registry's
// options but that hostd's client the first time a placement resolves
// there, and kept in step with grain_config.mcp_servers from then on
// (update). While a placed server's hostd is not connected, a grant of
// it cannot be minted: the run waits, and says needs-hostd:<selector>.
type hostdCapabilityServers struct {
	ctx  context.Context
	base *servers.Registry
	opts servers.Options
	// live is the configuration a run is routed by
	// (LiveConfig.withHostdCapabilityServers).
	live  func() Config
	store *model.Store

	mu sync.Mutex
	// configured is the run: host entries last handed over.
	configured map[string]protocol.ServerConfig
	regs       map[string]*hostdRegistry
}

// hostdRegistry is one hostd's registry; ready is closed once its first
// Sync has attached what it could.
type hostdRegistry struct {
	reg   *servers.Registry
	ready chan struct{}
}

func newHostdCapabilityServers(ctx context.Context, base *servers.Registry, opts servers.Options, store *model.Store) *hostdCapabilityServers {
	return &hostdCapabilityServers{ctx: ctx, base: base, opts: opts, store: store, regs: map[string]*hostdRegistry{}}
}

// update hands every hostd's registry the placed run: hostd entries of
// configured that may resolve to it, as LiveConfig.Refresh hands the
// deployment's registry the rest (deploymentCapabilityServers).
func (h *hostdCapabilityServers) update(configured map[string]protocol.ServerConfig) {
	if h == nil {
		return
	}
	placed := placedCapabilityServers(configured)
	h.mu.Lock()
	h.configured = placed
	regs := make(map[string]*servers.Registry, len(h.regs))
	for name, r := range h.regs {
		regs[name] = r.reg
	}
	h.mu.Unlock()
	for name, reg := range regs {
		reg.Update(placedOn(placed, name))
	}
}

// placedCapabilityServers is configured's run: hostd entries that name a
// hostd of their own.
func placedCapabilityServers(configured map[string]protocol.ServerConfig) map[string]protocol.ServerConfig {
	out := map[string]protocol.ServerConfig{}
	for id, cfg := range configured {
		if cfg.OnHost() && cfg.Hostd != "" {
			out[id] = cfg
		}
	}
	return out
}

// placedOn is the placed entries hostd name's registry holds: those
// naming it, and those selecting by labels, which may resolve to it.
func placedOn(placed map[string]protocol.ServerConfig, name string) map[string]protocol.ServerConfig {
	out := map[string]protocol.ServerConfig{}
	for id, cfg := range placed {
		if sel := string(cfg.Hostd); sel == name || strings.Contains(sel, "=") {
			out[id] = cfg
		}
	}
	return out
}

// deploymentCapabilityServers is configured without its placed entries:
// what the deployment's registry attaches.
func deploymentCapabilityServers(configured map[string]protocol.ServerConfig) map[string]protocol.ServerConfig {
	out := make(map[string]protocol.ServerConfig, len(configured))
	for id, cfg := range configured {
		if cfg.OnHost() && cfg.Hostd != "" {
			continue
		}
		out[id] = cfg
	}
	return out
}

// registry is the hostd name's registry, reaching it through client:
// built, and its run: host servers attached, the first time a run is
// routed there.
func (h *hostdCapabilityServers) registry(ctx context.Context, name string, client hostd.Client) (*servers.Registry, error) {
	h.mu.Lock()
	r := h.regs[name]
	creating := r == nil
	if creating {
		opts := h.opts
		opts.Hostd = &client
		logf := opts.Logf
		if logf != nil {
			opts.Logf = func(format string, args ...any) {
				logf("on grain hostd "+name+": "+format, args...)
			}
		}
		r = &hostdRegistry{reg: servers.New(opts), ready: make(chan struct{})}
		h.regs[name] = r
	}
	configured := placedOn(h.configured, name)
	h.mu.Unlock()
	if creating {
		// Attached now rather than on the registry's first tick, so the
		// run that is waiting to mint finds them.
		r.reg.Sync(h.ctx, configured)
		r.reg.Update(configured)
		go r.reg.Run(h.ctx, 0)
		close(r.ready)
	}
	select {
	case <-r.ready:
		return r.reg, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// ForTask is orchestrator.TaskCapabilityServers: the deployment's
// registry when no server is placed on a hostd of its own, and otherwise
// one that reaches each placed server on its hostd. taskID does not
// route anything: a task has no hostd.
func (h *hostdCapabilityServers) ForTask(ctx context.Context, taskID string) (orchestrator.CapabilityServers, string, error) {
	if h.live == nil {
		return h.base, "", nil
	}
	cfg := h.live()
	h.mu.Lock()
	placed := h.configured
	h.mu.Unlock()
	if len(placed) == 0 {
		return h.base, capabilityHostdName(cfg), nil
	}
	return &hostdRoutedServers{h: h, ctx: ctx, cfg: cfg, placed: placed, hostdOf: map[string]string{}}, capabilityHostdName(cfg), nil
}

// Lookup is the deployment registry's, for whatever is not minting for
// one task.
func (h *hostdCapabilityServers) Lookup(capabilityID string) (*protocol.Server, error) {
	return h.base.Lookup(capabilityID)
}

// hostdRoutedServers is one run's capability servers when some are
// placed on hostds of their own: each placed server from the registry of
// the hostd its placement resolves to now, every other from the
// deployment's. hostdOf records which hostd each placed one was reached
// on, for its audit records (HostdOf).
type hostdRoutedServers struct {
	h      *hostdCapabilityServers
	ctx    context.Context
	cfg    Config
	placed map[string]protocol.ServerConfig

	mu      sync.Mutex
	hostdOf map[string]string
}

func (s *hostdRoutedServers) Lookup(capabilityID string) (*protocol.Server, error) {
	entry, ok := placedEntry(s.placed, capabilityID)
	if !ok {
		return s.h.base.Lookup(capabilityID)
	}
	route, err := resolveHostdSelector(s.ctx, s.cfg, s.h.store, string(entry.Hostd))
	if err != nil {
		return nil, fmt.Errorf("needs-hostd:%s: capability %s runs on hostd %s: %w", entry.Hostd, capabilityID, entry.Hostd, err)
	}
	reg, err := s.h.registry(s.ctx, route.Name, route.Exec.hostdClient())
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.hostdOf[capabilityID] = route.Name
	s.mu.Unlock()
	return reg.Lookup(capabilityID)
}

// HostdOf is orchestrator.CapabilityHostdOf: the hostd capabilityID was
// reached on for this run, and whether it was a placed one.
func (s *hostdRoutedServers) HostdOf(capabilityID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	name, ok := s.hostdOf[capabilityID]
	return name, ok
}

// placedEntry is the entry of placed capabilityID names, by its id or by
// an alias it adopted.
func placedEntry(placed map[string]protocol.ServerConfig, capabilityID string) (protocol.ServerConfig, bool) {
	if cfg, ok := placed[capabilityID]; ok {
		return cfg, true
	}
	for _, cfg := range placed {
		for _, a := range cfg.Aliases {
			if a == capabilityID {
				return cfg, true
			}
		}
	}
	return protocol.ServerConfig{}, false
}

// NeedsHostd is the state Settings shows a placed server in while the
// hostd it is placed on cannot be reached: "needs-hostd:<selector>".
func NeedsHostd(selector string) servers.State {
	return servers.State("needs-hostd:" + selector)
}

// placedStatusTimeout bounds resolving one placed server's hostd for
// Settings.
const placedStatusTimeout = 2 * time.Second

// statuses is a Settings row for each placed server: its own hostd
// registry's, once it has been attached there; pending until then; and
// NeedsHostd while its hostd is not connected.
func (h *hostdCapabilityServers) statuses() []servers.Status {
	if h == nil || h.live == nil {
		return nil
	}
	h.mu.Lock()
	placed := h.configured
	h.mu.Unlock()
	if len(placed) == 0 {
		return nil
	}
	cfg := h.live()
	var out []servers.Status
	for id, entry := range placed {
		sel := string(entry.Hostd)
		row := servers.Status{ID: id, State: servers.Pending, Transport: "stdio", Run: protocol.RunHost, Builtin: entry.Builtin, Aliases: entry.Aliases}
		ctx, cancel := context.WithTimeout(h.ctx, placedStatusTimeout)
		route, err := resolveHostdSelector(ctx, cfg, h.store, sel)
		cancel()
		if err != nil {
			row.State, row.Error = NeedsHostd(sel), err.Error()
			out = append(out, row)
			continue
		}
		h.mu.Lock()
		r := h.regs[route.Name]
		h.mu.Unlock()
		if r != nil {
			for _, st := range r.reg.Statuses() {
				if st.ID == id {
					row = st
				}
			}
		}
		out = append(out, row)
	}
	return out
}

// placedStatusRegistry is the deployment's registry as Settings reads
// it, with a row for each server placed on a hostd of its own, which the
// deployment's registry does not attach.
type placedStatusRegistry struct {
	*servers.Registry
	placed *hostdCapabilityServers
}

func (r placedStatusRegistry) Statuses() []servers.Status {
	return append(r.Registry.Statuses(), r.placed.statuses()...)
}
