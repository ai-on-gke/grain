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
	"sync"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/capability/servers"
	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// hostdCapabilityServers starts run: host capability servers -- host
// scripts among them -- on the grain hostd each task's run is routed to
// (resolveHostdRoute), rather than only on the default hostd.
//
// The deployment's registry (startCapabilityServers) attaches every
// server, its run: host ones through the default hostd, and is what a
// run on the default hostd -- every task with no pin -- mints through. A
// run routed to another hostd mints through that hostd's own registry
// for its run: host servers: one per hostd name, holding only the run:
// host entries, built with the deployment registry's options but that
// hostd's client the first time a run is routed there, and kept in step
// with grain_config.mcp_servers from then on (update). Every other
// server is the deployment registry's, wherever the run went.
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

// update hands every hostd's registry the run: host entries of
// configured, as LiveConfig.Refresh hands the deployment's registry all
// of them.
func (h *hostdCapabilityServers) update(configured map[string]protocol.ServerConfig) {
	if h == nil {
		return
	}
	onHost := hostCapabilityServers(configured)
	h.mu.Lock()
	h.configured = onHost
	regs := make([]*servers.Registry, 0, len(h.regs))
	for _, r := range h.regs {
		regs = append(regs, r.reg)
	}
	h.mu.Unlock()
	for _, reg := range regs {
		reg.Update(onHost)
	}
}

// hostCapabilityServers is configured's run: host entries.
func hostCapabilityServers(configured map[string]protocol.ServerConfig) map[string]protocol.ServerConfig {
	out := map[string]protocol.ServerConfig{}
	for id, cfg := range configured {
		if cfg.OnHost() {
			out[id] = cfg
		}
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
	configured := h.configured
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
// registry for a run on the default hostd, and otherwise one whose run:
// host servers are the routed hostd's.
func (h *hostdCapabilityServers) ForTask(ctx context.Context, taskID string) (orchestrator.CapabilityServers, string, error) {
	if h.live == nil {
		return h.base, "", nil
	}
	cfg := h.live()
	route, err := resolveHostdRoute(ctx, cfg, h.store, taskID)
	if err != nil {
		return nil, "", err
	}
	if !route.Pinned || route.Name == cfg.DefaultHostd() {
		return h.base, capabilityHostdName(cfg), nil
	}
	reg, err := h.registry(ctx, route.Name, route.Exec.hostdClient())
	if err != nil {
		return nil, "", err
	}
	h.mu.Lock()
	onHost := h.configured
	h.mu.Unlock()
	return hostdRoutedServers{base: h.base, host: reg, onHost: onHost}, route.Name, nil
}

// Lookup is the deployment registry's, for whatever is not minting for
// one task.
func (h *hostdCapabilityServers) Lookup(capabilityID string) (*protocol.Server, error) {
	return h.base.Lookup(capabilityID)
}

// hostdRoutedServers is a run's capability servers on a hostd other than
// the default: its run: host servers from that hostd's registry, every
// other from the deployment's.
type hostdRoutedServers struct {
	base, host *servers.Registry
	onHost     map[string]protocol.ServerConfig
}

func (s hostdRoutedServers) Lookup(capabilityID string) (*protocol.Server, error) {
	if isHostCapability(s.onHost, capabilityID) {
		return s.host.Lookup(capabilityID)
	}
	return s.base.Lookup(capabilityID)
}

// isHostCapability reports whether capabilityID names one of onHost's
// entries, by its id or by an alias it adopted.
func isHostCapability(onHost map[string]protocol.ServerConfig, capabilityID string) bool {
	if _, ok := onHost[capabilityID]; ok {
		return true
	}
	for _, cfg := range onHost {
		for _, a := range cfg.Aliases {
			if a == capabilityID {
				return true
			}
		}
	}
	return false
}
