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
	"errors"
	"fmt"
	"sort"
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
		reg.Update(placedOn(placed, name, h.hostdLabels(h.ctx, name)))
	}
	if len(placed) > 0 && h.live != nil {
		go h.attachConnected()
	}
}

// attachConnected builds the registry of every connected hostd a placed
// server resolves to now, so placed servers attach and advertise their
// methods and config schemas in Settings before any task grants them.
func (h *hostdCapabilityServers) attachConnected() {
	if h == nil || h.live == nil {
		return
	}
	h.mu.Lock()
	placed := h.configured
	h.mu.Unlock()
	if len(placed) == 0 {
		return
	}
	cfg := h.live()
	seen := map[string]bool{}
	for _, entry := range placed {
		sel := string(entry.Hostd)
		if seen[sel] {
			continue
		}
		seen[sel] = true
		ctx, cancel := context.WithTimeout(h.ctx, placedStatusTimeout)
		route, err := resolveHostdSelector(ctx, cfg, h.store, sel)
		cancel()
		if err != nil {
			continue
		}
		h.mu.Lock()
		exists := h.regs[route.Name] != nil
		h.mu.Unlock()
		if !exists {
			_, _ = h.registry(h.ctx, route.Name, route.Exec.hostdClient())
		}
	}
}

// hostdLabels is the registry labels of hostd name, nil when it has none
// or is not in the registry: what a label-selected placement is matched
// against, as SelectHostd matches it.
func (h *hostdCapabilityServers) hostdLabels(ctx context.Context, name string) map[string]string {
	if h.store == nil {
		return nil
	}
	row, err := h.store.GetHostd(ctx, name)
	if err != nil || row == nil || row.Revoked() {
		return nil
	}
	return row.Labels
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
// naming it, and those selecting by labels that labels, its registry
// labels, carry -- the ones SelectHostd may resolve to it. A server
// selecting labels the hostd does not carry is never started there.
func placedOn(placed map[string]protocol.ServerConfig, name string, labels map[string]string) map[string]protocol.ServerConfig {
	out := map[string]protocol.ServerConfig{}
	for id, cfg := range placed {
		selName, selLabels, err := model.ParseHostdSelector(string(cfg.Hostd))
		if err != nil {
			continue
		}
		if (selName != "" && selName == name) || (selName == "" && len(selLabels) > 0 && labelsMatch(labels, selLabels)) {
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
				logf("on host "+name+": "+format, args...)
			}
		}
		if h.store != nil {
			raiseDrop, clearDrop := capabilityServerAlertsOnHostd(h.ctx, h.store, name)
			opts.OnDrop = func(st servers.Status) {
				raiseDrop(st)
				reason := st.Reason
				if reason == "" {
					reason = st.Error
				}
				orchestrator.NoteCapabilityServerDroppedOnHostd(h.ctx, h.store, st.ID, name, string(st.State), reason, st.ConfigRefused)
			}
			opts.OnAttach = func(id string) {
				clearDrop(id)
				go orchestrator.RetryCapabilityReleases(h.ctx, h.store)
			}
			opts.OnClear = clearDrop
		} else {
			opts.OnDrop, opts.OnAttach, opts.OnClear = nil, nil, nil
		}
		r = &hostdRegistry{reg: servers.New(opts), ready: make(chan struct{})}
		h.regs[name] = r
	}
	placed := h.configured
	h.mu.Unlock()
	if creating {
		// Attached off the caller's ctx so a short status timeout cannot
		// strand the initial Sync while still letting ctx bound the wait.
		go func() {
			configured := placedOn(placed, name, h.hostdLabels(h.ctx, name))
			r.reg.Sync(h.ctx, configured)
			r.reg.Update(configured)
			go r.reg.Run(h.ctx, 0)
			close(r.ready)
		}()
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

// Lookup reaches capabilityID on the deployment's registry, or on the
// hostd its entry places it on when it is a placed run: hostd server
// (for CI passes and control-plane callers that are not a task run).
func (h *hostdCapabilityServers) Lookup(capabilityID string) (*protocol.Server, error) {
	if h == nil || h.live == nil {
		return h.base.Lookup(capabilityID)
	}
	h.mu.Lock()
	placed := h.configured
	h.mu.Unlock()
	_, entry, ok := placedEntry(placed, capabilityID)
	if !ok {
		return h.base.Lookup(capabilityID)
	}
	route, err := resolveHostdSelector(h.ctx, h.live(), h.store, string(entry.Hostd))
	if err != nil {
		return nil, fmt.Errorf("needs-hostd:%s: capability %s runs on host %s: %w", entry.Hostd, capabilityID, entry.Hostd, err)
	}
	reg, err := h.registry(h.ctx, route.Name, route.Exec.hostdClient())
	if err != nil {
		return nil, err
	}
	return reg.Lookup(capabilityID)
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
	canonicalID, entry, ok := placedEntry(s.placed, capabilityID)
	if !ok {
		return s.h.base.Lookup(capabilityID)
	}
	s.mu.Lock()
	chosen, pinned := s.hostdOf[canonicalID]
	s.mu.Unlock()
	lookupCtx := s.ctx
	if lookupCtx == nil || lookupCtx.Err() != nil {
		lookupCtx = s.h.ctx
	}
	if pinned {
		s.h.mu.Lock()
		r := s.h.regs[chosen]
		s.h.mu.Unlock()
		if r != nil {
			select {
			case <-r.ready:
				return r.reg.Lookup(capabilityID)
			case <-lookupCtx.Done():
				return nil, lookupCtx.Err()
			}
		}
	}
	route, err := resolveHostdSelector(lookupCtx, s.cfg, s.h.store, string(entry.Hostd))
	if err != nil {
		return nil, fmt.Errorf("needs-hostd:%s: capability %s runs on host %s: %w", entry.Hostd, capabilityID, entry.Hostd, err)
	}
	reg, err := s.h.registry(lookupCtx, route.Name, route.Exec.hostdClient())
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.hostdOf[canonicalID] = route.Name
	s.hostdOf[capabilityID] = route.Name
	for _, a := range entry.Aliases {
		s.hostdOf[a] = route.Name
	}
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
func placedEntry(placed map[string]protocol.ServerConfig, capabilityID string) (string, protocol.ServerConfig, bool) {
	if cfg, ok := placed[capabilityID]; ok {
		return capabilityID, cfg, true
	}
	for id, cfg := range placed {
		for _, a := range cfg.Aliases {
			if a == capabilityID {
				return id, cfg, true
			}
		}
	}
	return "", protocol.ServerConfig{}, false
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
		if err != nil {
			cancel()
			row.State, row.Error = NeedsHostd(sel), err.Error()
			out = append(out, row)
			continue
		}
		reg, regErr := h.registry(ctx, route.Name, route.Exec.hostdClient())
		cancel()
		if regErr == nil && reg != nil {
			for _, st := range reg.Statuses() {
				if st.ID == id {
					row = st
				}
			}
		}
		out = append(out, row)
	}
	return out
}

func offeredCapabilityNames(offers []hostd.Offer) []string {
	var out []string
	for _, o := range offers {
		if o.Kind == hostd.KindCapability {
			out = append(out, o.Name)
		}
	}
	sort.Strings(out)
	return out
}

func (h *hostdCapabilityServers) defaultHostCapabilityEntries(ctx context.Context) ([]string, error) {
	if h != nil && h.live != nil {
		exec := h.live().hostExecutor()
		if exec.Kind == HostExecutorHostd {
			offers, err := queryHostdOffers(hostd.ForDisplay(ctx), exec, hostdOffersTimeout)
			if err != nil {
				return nil, fmt.Errorf("asking the host agent what it offers: %w", err)
			}
			return offeredCapabilityNames(offers), nil
		}
		if h.opts.Hostd == nil {
			return nil, servers.ErrNoHostd
		}
	}
	if h == nil || h.base == nil {
		return nil, servers.ErrNoHostd
	}
	octx, cancel := context.WithTimeout(hostd.ForDisplay(ctx), hostdOffersTimeout)
	defer cancel()
	return h.base.HostCapabilityEntries(octx)
}

func (h *hostdCapabilityServers) probe(ctx context.Context, id string, cfg protocol.ServerConfig, extra protocol.SecretLookup) servers.ProbeResult {
	if h == nil || h.live == nil {
		return servers.ProbeResult{ID: id, Methods: []servers.MethodDetail{}, Error: servers.ErrNoHostd.Error()}
	}
	rctx, cancel := context.WithTimeout(hostd.ForDisplay(ctx), hostdOffersTimeout)
	route, err := resolveHostdSelector(rctx, h.live(), h.store, string(cfg.Hostd))
	cancel()
	if err != nil {
		return servers.ProbeResult{ID: id, Methods: []servers.MethodDetail{}, Error: err.Error()}
	}
	client := route.Exec.hostdClient()
	opts := h.opts
	opts.Hostd = &client
	opts.OnDrop, opts.OnAttach, opts.OnClear = nil, nil, nil
	return servers.New(opts).Probe(ctx, id, cfg, extra)
}

func (h *hostdCapabilityServers) hostCapabilityEntries(ctx context.Context, selector string) ([]string, bool, error) {
	if h == nil || h.live == nil {
		return nil, false, servers.ErrNoHostd
	}
	octx, cancel := context.WithTimeout(hostd.ForDisplay(ctx), hostdOffersTimeout)
	defer cancel()
	route, err := resolveHostdSelector(octx, h.live(), h.store, selector)
	if err != nil {
		var w *HostdWaitError
		if errors.As(err, &w) {
			return nil, false, err
		}
		return nil, true, err
	}
	offers, err := queryHostdOffers(octx, route.Exec, hostdOffersTimeout)
	if err != nil {
		return nil, true, fmt.Errorf("asking the host agent what it offers: %w", err)
	}
	return offeredCapabilityNames(offers), true, nil
}

// hostAgentsOnly reports whether the connected host selector names says,
// in its hello, that it runs agent CLIs only (grain-agentd). A host that
// cannot be reached, or one too old to say, is not.
func (h *hostdCapabilityServers) hostAgentsOnly(ctx context.Context, selector string) bool {
	if h == nil || h.live == nil {
		return false
	}
	octx, cancel := context.WithTimeout(hostd.ForDisplay(ctx), hostdOffersTimeout)
	defer cancel()
	route, err := resolveHostdSelector(octx, h.live(), h.store, selector)
	if err != nil {
		return false
	}
	hello, err := route.Exec.hostdClient().Hello(octx)
	return err == nil && hello.AgentsOnly
}

func (h *hostdCapabilityServers) rotateManaged(capabilityID string) ([]string, error) {
	if h == nil || h.live == nil {
		return h.base.RotateManaged(capabilityID)
	}
	h.mu.Lock()
	placed := h.configured
	h.mu.Unlock()
	canonicalID, entry, ok := placedEntry(placed, capabilityID)
	if !ok {
		return h.base.RotateManaged(capabilityID)
	}
	ctx, cancel := context.WithTimeout(h.ctx, placedStatusTimeout)
	defer cancel()
	route, err := resolveHostdSelector(ctx, h.live(), h.store, string(entry.Hostd))
	if err != nil {
		return nil, fmt.Errorf("needs-hostd:%s: capability %s runs on host %s: %w", entry.Hostd, capabilityID, entry.Hostd, err)
	}
	reg, err := h.registry(ctx, route.Name, route.Exec.hostdClient())
	if err != nil {
		return nil, err
	}
	return reg.RotateManaged(canonicalID)
}

// placedStatusRegistry is the deployment's registry as Settings and
// control-plane callers read it, routing placed run: hostd servers --
// including Probe and CallTool, so credentials never go to the default
// hostd -- to the hostd each entry names.
type placedStatusRegistry struct {
	*servers.Registry
	placed *hostdCapabilityServers
}

func (r placedStatusRegistry) Statuses() []servers.Status {
	return append(r.Registry.Statuses(), r.placed.statuses()...)
}

func (r placedStatusRegistry) Lookup(capabilityID string) (*protocol.Server, error) {
	if r.placed != nil {
		return r.placed.Lookup(capabilityID)
	}
	return r.Registry.Lookup(capabilityID)
}

func (r placedStatusRegistry) CallTool(ctx context.Context, capabilityID, tool string, arguments map[string]any, timeout time.Duration) ([]byte, error) {
	srv, err := r.Lookup(capabilityID)
	if err != nil {
		return nil, err
	}
	return srv.CallTool(ctx, tool, arguments, timeout)
}

func (r placedStatusRegistry) Probe(ctx context.Context, id string, cfg protocol.ServerConfig, extra protocol.SecretLookup) servers.ProbeResult {
	if cfg.OnHost() && cfg.Hostd != "" {
		return r.placed.probe(ctx, id, cfg, extra)
	}
	return r.Registry.Probe(ctx, id, cfg, extra)
}

func (r placedStatusRegistry) HostCapabilityEntries(ctx context.Context) ([]string, error) {
	if r.placed != nil {
		return r.placed.defaultHostCapabilityEntries(ctx)
	}
	octx, cancel := context.WithTimeout(hostd.ForDisplay(ctx), hostdOffersTimeout)
	defer cancel()
	return r.Registry.HostCapabilityEntries(octx)
}

func (r placedStatusRegistry) HostCapabilityEntriesFor(ctx context.Context, selector string) ([]string, bool, error) {
	return r.placed.hostCapabilityEntries(ctx, selector)
}

func (r placedStatusRegistry) HostAgentsOnly(ctx context.Context, selector string) bool {
	return r.placed.hostAgentsOnly(ctx, selector)
}

func (r placedStatusRegistry) RotateManaged(capabilityID string) ([]string, error) {
	if r.placed != nil {
		return r.placed.rotateManaged(capabilityID)
	}
	return r.Registry.RotateManaged(capabilityID)
}
