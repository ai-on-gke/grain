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

// Package servers keeps the daemon's attached capability servers: the
// mcpServers entries of grain_config (model.Config.MCPServers,
// docs/capabilities-spec.md §3.1), each attached with protocol.Attach and
// held under its capability_id for as long as its configuration says it
// should be.
//
// A Registry is driven by Sync, which makes the attached set match a
// configuration: an entry that is new, changed or due a retry is
// attached; one that was removed, disabled or changed is detached; and
// every attached server has its tools/list re-issued on a configuration
// reload and at least every RefreshInterval (§4.0.4). Run is the loop
// that calls it, woken by Update whenever the daemon re-reads its
// settings.
//
// Lookup is the only way a caller gets at an attached server, and it
// refuses a capability_id that is not configured, is disabled or did not
// attach (§3.2.1), saying which. Nothing here ever contacts the
// capability_id URL itself: a server is reached only through the command
// or serverUrl its entry names.
//
// What is reported -- Statuses, and the errors Lookup returns -- names a
// server by its capability_id and a missing secret by its name, never a
// secret's value: an attach error is scrubbed of every value its entry's
// secrets resolved to before it is kept (§3.1.2).
package servers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/intree"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/hostd"
)

// Defaults for Options.
const (
	// DefaultRefreshInterval is how often an attached server's
	// tools/list is re-issued when nothing else asks for it: the
	// "at least hourly" of §4.0.4.
	DefaultRefreshInterval = time.Hour
	// DefaultRetryInterval is how long a server that failed to attach
	// waits before it is tried again -- long enough not to hammer a
	// server that is down, short enough that a secret added in Settings
	// is picked up without anybody editing the entry.
	DefaultRetryInterval = time.Minute
)

// State is where one configured server stands.
type State string

const (
	// Attached: connected, with its methods discovered.
	Attached State = "attached"
	// Disabled: configured with disabled: true, so not attached and not
	// admitted (§3.1.1).
	Disabled State = "disabled"
	// Failed: the last attach or refresh failed; Status.Error says why.
	Failed State = "failed"
	// Pending: configured, not yet tried.
	Pending State = "pending"
	// NeedsConfig: a built-in (ServerConfig.Builtin) that cannot serve
	// until an operator configures it -- gcp with no permission sets,
	// host-script with no scripts, or any built-in whose required
	// configuration is missing. It is not an error: Status.Reason says
	// what to configure, and it offers no methods meanwhile.
	NeedsConfig State = "needs-config"
)

// Status is one configured server as Settings and the CLI show it.
type Status struct {
	ID        string `json:"id"`
	State     State  `json:"state"`
	Transport string `json:"transport"`
	// Run is "host" for a stdio server started through grain hostd.
	Run string `json:"run,omitempty"`
	// Builtin is the in-tree server a built-in entry is
	// (ServerConfig.Builtin); empty for an operator's own entry.
	Builtin string `json:"builtin,omitempty"`
	// Aliases are the other capability_ids that resolve to this entry
	// (ServerConfig.Aliases).
	Aliases []string `json:"aliases,omitempty"`
	// Reason is what a built-in in NeedsConfig is waiting to be
	// configured with.
	Reason string `json:"reason,omitempty"`
	// Methods are the capability methods the server's tools/list
	// advertised, sorted; empty unless it is attached.
	Methods []string `json:"methods,omitempty"`
	// MethodDetails are those same methods with what tools/list said of
	// each: its description, its with: argument schema
	// (inputSchema.properties.args, §4.1.3) and whether it is stateful
	// (advertises release_<method>). The UI renders a grant's argument
	// form from these and Settings lists them.
	MethodDetails []MethodDetail `json:"methodDetails,omitempty"`
	// ConfigSchema is the server's configure schema (§4.5.1), the form
	// Settings edits its config with. It is kept for a server refused for
	// its configuration too, so the form that fixes it can be drawn.
	ConfigSchema map[string]any `json:"configSchema,omitempty"`
	// Tools are the control-plane tools of its own the server advertised
	// (protocol.Server.ControlTools) -- host-script's test_script, which
	// Settings' per-script Test button calls; empty unless it is attached.
	Tools []string `json:"tools,omitempty"`
	// Error is why the server is not attached (or why its last refresh
	// failed), naming missing secrets by name and never a value.
	Error       string     `json:"error,omitempty"`
	AttachedAt  *time.Time `json:"attachedAt,omitempty"`
	RefreshedAt *time.Time `json:"refreshedAt,omitempty"`
}

// MethodDetail is one advertised method as Status reports it.
type MethodDetail struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Args is the method's args schema as discovery normalized it; nil
	// for a method that takes no arguments.
	Args map[string]any `json:"args,omitempty"`
	// Releasable is release_<method> being advertised: the method is
	// stateful and its leases are released at teardown.
	Releasable bool `json:"releasable"`
	// Delivery is how the tools its grants' endpoints serve are
	// delivered unless a tool says otherwise (docs/capabilities-spec.md
	// §5.2.6): "sandbox", through grain cap, or "model_only", the
	// default, on the agent's own MCP channel alone.
	Delivery string `json:"delivery"`
	// DeliveryDeclared says the method declared Delivery itself
	// (annotations.mcpDelivery) rather than falling to the default.
	DeliveryDeclared bool `json:"deliveryDeclared"`
}

// AttachFunc is protocol.Attach's signature; tests substitute their own.
type AttachFunc func(ctx context.Context, id string, cfg protocol.ServerConfig, secrets protocol.SecretLookup, opts protocol.DialOptions) (*protocol.Server, error)

// Options configure a Registry.
type Options struct {
	// Secrets resolves $secret:<name> references from the encrypted
	// secrets store. Nil resolves nothing, so an entry referencing a
	// secret is refused.
	Secrets protocol.SecretLookup
	// ConfigRoot is the checked-out grain/config a relative stdio
	// command is resolved under (protocol.DialOptions.ConfigRoot).
	ConfigRoot string
	// Stderr, when set, gives each stdio server somewhere for its
	// diagnostics; nil discards them.
	Stderr func(id string) io.Writer
	// RefreshInterval and RetryInterval default to DefaultRefreshInterval
	// and DefaultRetryInterval.
	RefreshInterval time.Duration
	RetryInterval   time.Duration
	// Hostd starts run: host servers through grain hostd
	// (protocol.DialOptions.Hostd). Nil -- a daemon on the host itself --
	// refuses them.
	Hostd *hostd.Client
	// Identity is this deployment's identity as each server's
	// configure is sent it (protocol.Identity, §4.5.7), read again on
	// every Sync: a server attached under an identity that has since
	// changed -- a deployment id renamed in Settings -- is attached
	// again. Nil sends none.
	Identity func() protocol.Identity
	// Managed keeps the values grain generates for the configure fields
	// it manages (protocol.WithManagedSecrets) -- a data-plane token
	// nobody has to make up. Nil generates none.
	Managed protocol.ManagedSecrets
	// Attach defaults to protocol.Attach.
	Attach AttachFunc
	// Logf records attaches, detaches and failures; nil is silent.
	Logf func(format string, args ...any)
	// Now defaults to time.Now.
	Now func() time.Time
	// ProbeWarnings, when set, adds warnings to what Probe found for an
	// entry -- the gcp server's sets overlapping the in-process gcp-key
	// provider's accounts (intree.GCPOverlapWarnings).
	ProbeWarnings func(id string, cfg protocol.ServerConfig) []string
}

type entry struct {
	cfg      protocol.ServerConfig
	identity protocol.Identity // what the entry was attached under
	server   *protocol.Server
	methods  []string // server.Methods' names, copied under mu
	details  []MethodDetail
	schema   map[string]any // the configure schema, when there is one
	tools    []string       // server.ControlTools, copied under mu
	state    State
	err      string
	tried    time.Time
	// hostRetry marks a run: host built-in that is waiting to be
	// configured only because it never started -- hostd down, or its
	// allowlist without the entry -- rather than because it said so: it
	// is retried every RetryInterval, so the entry grain-hostd-allowlist
	// adds, or a hostd back from a restart, attaches it with no daemon
	// restart.
	hostRetry bool
	attached  time.Time
	refreshed time.Time
}

// Registry holds the attached servers, keyed by capability_id.
type Registry struct {
	opts Options

	syncMu sync.Mutex // one Sync at a time

	mu      sync.RWMutex
	entries map[string]*entry
	applied map[string]protocol.ServerConfig // the configuration last synced to
	aliases map[string]string                // alias capability_id -> entry id

	wake    chan struct{}
	pending map[string]protocol.ServerConfig
	updated bool
}

// New returns an empty registry.
func New(opts Options) *Registry {
	if opts.RefreshInterval <= 0 {
		opts.RefreshInterval = DefaultRefreshInterval
	}
	if opts.RetryInterval <= 0 {
		opts.RetryInterval = DefaultRetryInterval
	}
	if opts.Attach == nil {
		opts.Attach = protocol.Attach
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	if opts.Identity == nil {
		opts.Identity = func() protocol.Identity { return protocol.Identity{} }
	}
	return &Registry{opts: opts, entries: map[string]*entry{}, wake: make(chan struct{}, 1)}
}

// Lookup returns the attached server for capabilityID, refusing one that
// mcpServers does not map, that is disabled, or that is not attached
// (§3.2.1).
func (r *Registry) Lookup(capabilityID string) (*protocol.Server, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[capabilityID]
	if !ok {
		// A grant naming the id a built-in adopted (ServerConfig.Aliases).
		if target, aliased := r.aliases[capabilityID]; aliased {
			e, ok = r.entries[target]
			capabilityID = target
		}
	}
	if !ok {
		return nil, fmt.Errorf("capability %s is not configured in mcpServers", capabilityID)
	}
	switch e.state {
	case Attached:
		if e.server.Ended() {
			return nil, fmt.Errorf("capability server %s has ended; it is being started again", capabilityID)
		}
		return e.server, nil
	case Disabled:
		return nil, fmt.Errorf("capability server %s is disabled", capabilityID)
	case Failed:
		return nil, fmt.Errorf("capability server %s is not attached: %s", capabilityID, e.err)
	case NeedsConfig:
		return nil, fmt.Errorf("capability server %s needs configuring in Settings → Capability servers: %s", capabilityID, e.err)
	default:
		return nil, fmt.Errorf("capability server %s is not attached yet", capabilityID)
	}
}

// RotateManaged replaces the value of every configure field grain
// manages for capabilityID (protocol.ManagedSecretFields) that its
// config does not override, then attaches the server again so it and
// grain's proxy both move to the new value. It names the fields it
// rotated; a server with none to rotate is refused.
func (r *Registry) RotateManaged(capabilityID string) ([]string, error) {
	if r.opts.Managed == nil {
		return nil, errors.New("this grain keeps no managed secrets")
	}
	r.mu.RLock()
	e, ok := r.entries[capabilityID]
	var schema, config map[string]any
	if ok {
		schema, config = e.schema, e.cfg.Config
		if schema == nil && e.cfg.Builtin != "" {
			schema = intree.BuiltinConfigSchema(e.cfg.Builtin)
		}
	}
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("capability %s is not configured in mcpServers", capabilityID)
	}
	var rotated []string
	for _, f := range protocol.ManagedSecretFields(schema) {
		if protocol.ManagedOverridden(capabilityID, f, config) {
			continue
		}
		if err := r.opts.Managed.Rotate(protocol.ManagedSecretName(capabilityID, f), protocol.GenerateManagedValue); err != nil {
			return rotated, fmt.Errorf("rotating %s: %v", f, err)
		}
		rotated = append(rotated, f)
	}
	if len(rotated) == 0 {
		return nil, fmt.Errorf("capability server %s has no grain-managed secret to rotate", capabilityID)
	}
	r.syncMu.Lock()
	defer r.syncMu.Unlock()
	var closing *protocol.Server
	r.set(capabilityID, func(e *entry) {
		closing, e.server, e.state = e.server, nil, Pending
	})
	if closing != nil {
		_ = closing.Close()
	}
	r.opts.Logf("capability server %s: rotated %s, attaching it again", capabilityID, strings.Join(rotated, ", "))
	select {
	case r.wake <- struct{}{}:
	default:
	}
	return rotated, nil
}

// CallTool calls one of capabilityID's control-plane tools
// (protocol.Server.ControlTools) on its attached server.
func (r *Registry) CallTool(ctx context.Context, capabilityID, tool string, arguments map[string]any, timeout time.Duration) ([]byte, error) {
	server, err := r.Lookup(capabilityID)
	if err != nil {
		return nil, err
	}
	return server.CallTool(ctx, tool, arguments, timeout)
}

// Statuses reports every configured server, sorted by capability_id.
func (r *Registry) Statuses() []Status {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Status, 0, len(r.entries))
	for id, e := range r.entries {
		st := e.status(id)
		if r.updated {
			if cfg, ok := r.pending[id]; ok && !reflect.DeepEqual(cfg, e.cfg) && !cfg.Disabled {
				st.State = Pending
			}
		}
		out = append(out, st)
	}
	// An entry Update has handed over that no Sync has picked up yet is
	// pending too, not missing: it is reported until the next tick says
	// how attaching it went.
	if r.updated {
		for id, cfg := range r.pending {
			if _, ok := r.entries[id]; ok {
				continue
			}
			st := (&entry{cfg: cfg, state: Pending}).status(id)
			if cfg.Disabled {
				st.State = Disabled
			}
			out = append(out, st)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (e *entry) status(id string) Status {
	s := Status{ID: id, State: e.state, Error: e.err, Transport: "http", Builtin: e.cfg.Builtin}
	s.Aliases = append([]string(nil), e.cfg.Aliases...)
	if e.state == NeedsConfig && !e.hostRetry {
		s.Error, s.Reason = "", e.err
	}
	if e.cfg.Stdio() {
		s.Transport = "stdio"
	}
	if e.cfg.OnHost() {
		s.Run = protocol.RunHost
	}
	if e.state == Attached {
		s.Methods = append([]string(nil), e.methods...)
		s.MethodDetails = append([]MethodDetail(nil), e.details...)
		s.Tools = append([]string(nil), e.tools...)
	}
	if e.state == Attached || e.state == Failed || e.state == NeedsConfig {
		s.ConfigSchema = protocol.OperatorSchema(e.schema)
		// A built-in that has never started -- a run: host one whose hostd
		// entry is missing, say -- has told nobody its schema; its own
		// static one lets Configure be done first.
		if s.ConfigSchema == nil && e.cfg.Builtin != "" {
			s.ConfigSchema = intree.BuiltinConfigSchema(e.cfg.Builtin)
		}
	}
	if !e.attached.IsZero() && e.state == Attached {
		t := e.attached
		s.AttachedAt = &t
	}
	if !e.refreshed.IsZero() && e.state == Attached {
		t := e.refreshed
		s.RefreshedAt = &t
	}
	return s
}

// Sync makes the registry match servers. When the configuration differs
// from the one last synced to -- a configuration reload -- every
// attached server that is kept is refreshed; otherwise only those whose
// last refresh is older than RefreshInterval are, and only failed
// entries whose retry is due are attached again.
func (r *Registry) Sync(ctx context.Context, servers map[string]protocol.ServerConfig) {
	r.syncMu.Lock()
	defer r.syncMu.Unlock()

	r.mu.RLock()
	reload := !reflect.DeepEqual(normalize(servers), normalize(r.applied))
	r.mu.RUnlock()
	now := r.opts.Now()
	identity := r.opts.Identity()

	// Detach what went away or changed, so nothing keeps serving under
	// a configuration that no longer says it should.
	r.mu.Lock()
	var closing []*protocol.Server
	for id, e := range r.entries {
		cfg, ok := servers[id]
		if ok && reflect.DeepEqual(cfg, e.cfg) && e.identity == identity {
			continue
		}
		if e.server != nil {
			closing = append(closing, e.server)
		}
		delete(r.entries, id)
		if !ok {
			r.opts.Logf("capability server %s: removed from mcpServers, detached", id)
		} else if reflect.DeepEqual(cfg, e.cfg) {
			r.opts.Logf("capability server %s: this deployment's identity changed, attaching it again", id)
		}
	}
	for id, cfg := range servers {
		if _, ok := r.entries[id]; !ok {
			r.entries[id] = &entry{cfg: cfg, identity: identity, state: Pending}
		}
	}
	r.applied = copyServers(servers)
	r.aliases = map[string]string{}
	for id, cfg := range servers {
		for _, a := range cfg.Aliases {
			if _, taken := servers[a]; !taken {
				r.aliases[a] = id
			}
		}
	}
	const legacySkillID = "https://grain.dev/capabilities/skill"
	const configMapID = "https://grain.dev/capabilities/config-map"
	if _, taken := servers[legacySkillID]; !taken {
		if _, hasCM := servers[configMapID]; hasCM {
			if _, already := r.aliases[legacySkillID]; !already {
				r.aliases[legacySkillID] = configMapID
			}
		}
	}
	ids := make([]string, 0, len(r.entries))
	for id := range r.entries {
		ids = append(ids, id)
	}
	r.mu.Unlock()
	for _, s := range closing {
		_ = s.Close()
	}
	sort.Strings(ids)

	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		r.mu.RLock()
		e := r.entries[id]
		cfg, state, server, tried, refreshed, hostRetry := e.cfg, e.state, e.server, e.tried, e.refreshed, e.hostRetry
		r.mu.RUnlock()
		switch {
		case cfg.Disabled:
			if state != Disabled {
				r.set(id, func(e *entry) { e.state, e.err = Disabled, "" })
			}
		case state == Attached && server.Ended():
			// Its process is gone -- a run: host server that hostd's
			// drain ended, say -- and every call over it would fail
			// until the next refresh, an hour off. Start it again now.
			r.opts.Logf("capability server %s: its process ended, attaching it again", id)
			_ = server.Close()
			r.attach(ctx, id, cfg)
		case state == Attached:
			if reload || now.Sub(refreshed) >= r.opts.RefreshInterval {
				r.refresh(ctx, id, server)
			}
		case state == NeedsConfig && len(cfg.SecretRefs()) == 0 && !hostRetry:
			// Waiting for the operator to edit Config or Args in Settings
			// (which changes cfg and resets the entry to Pending above).
		case state == Pending || reload || now.Sub(tried) >= r.opts.RetryInterval:
			r.attach(ctx, id, cfg)
		}
	}
}

// attach connects one entry, keeping the failure -- scrubbed of every
// secret value the attempt resolved -- when it does not.
func (r *Registry) attach(ctx context.Context, id string, cfg protocol.ServerConfig) {
	var resolved []string
	lookup := func(name string) (string, bool) {
		if r.opts.Secrets == nil {
			return "", false
		}
		v, ok := r.opts.Secrets(name)
		if ok && v != "" {
			resolved = append(resolved, v)
		}
		return v, ok
	}
	r.mu.RLock()
	identity := protocol.Identity{}
	if e := r.entries[id]; e != nil {
		identity = e.identity
	}
	r.mu.RUnlock()
	opts := protocol.DialOptions{ConfigRoot: r.opts.ConfigRoot, Hostd: r.opts.Hostd, Identity: identity, Managed: r.opts.Managed}
	if r.opts.Stderr != nil {
		opts.Stderr = r.opts.Stderr(id)
	}
	server, err := r.opts.Attach(ctx, id, cfg, lookup, opts)
	now := r.opts.Now()
	if err != nil {
		msg := Scrub(err.Error(), resolved)
		var schema map[string]any
		var ce *protocol.ConfigureError
		isConfigure := errors.As(err, &ce)
		if isConfigure {
			schema = ce.Schema
		}
		state := Failed
		if cfg.Builtin != "" && (isConfigure || len(cfg.Config) == 0) {
			// A built-in refused before anybody configured it, or for its
			// configuration, is waiting to be configured, not broken.
			state = NeedsConfig
		}
		var prevState State
		var prevErr string
		r.set(id, func(e *entry) {
			prevState, prevErr = e.state, e.err
			e.state, e.err, e.tried, e.schema = state, msg, now, schema
			e.hostRetry = state == NeedsConfig && !isConfigure && cfg.OnHost()
		})
		if state == NeedsConfig {
			if prevState != NeedsConfig || prevErr != msg {
				r.opts.Logf("capability server %s: not configured: %s", id, msg)
			}
		} else {
			r.opts.Logf("capability server %s: attach refused: %s", id, msg)
		}
		return
	}
	methods, details := methodNames(server), methodDetails(server)
	if cfg.Builtin != "" && len(methods) == 0 {
		schema := server.ConfigSchema
		_ = server.Close()
		reason := "it offers no methods until it is configured: fill in its Server settings (Edit server settings, under Settings → Capabilities)"
		var prevState State
		r.set(id, func(e *entry) {
			prevState = e.state
			e.state, e.err, e.tried, e.schema, e.hostRetry = NeedsConfig, reason, now, schema, false
		})
		if prevState != NeedsConfig {
			r.opts.Logf("capability server %s: not configured", id)
		}
		return
	}
	tools := append([]string(nil), server.ControlTools...)
	kept := r.set(id, func(e *entry) {
		e.state, e.err, e.tried, e.server, e.methods, e.details = Attached, "", now, server, methods, details
		e.schema, e.tools = server.ConfigSchema, tools
		e.attached, e.refreshed = now, now
	})
	if !kept {
		// The entry went away while this attach was in flight.
		_ = server.Close()
		return
	}
	r.opts.Logf("capability server %s: attached, %d method(s)", id, len(methods))
}

// refresh re-issues tools/list. A server that no longer answers is
// detached and marked failed, so the next retry attaches it afresh --
// a stdio server that exited is respawned that way.
func (r *Registry) refresh(ctx context.Context, id string, server *protocol.Server) {
	err := server.Refresh(ctx)
	now := r.opts.Now()
	if err == nil {
		methods, details := methodNames(server), methodDetails(server)
		schema, tools := server.ConfigSchema, append([]string(nil), server.ControlTools...)
		r.set(id, func(e *entry) {
			e.refreshed, e.methods, e.details, e.schema, e.tools = now, methods, details, schema, tools
		})
		return
	}
	msg := err.Error()
	r.opts.Logf("capability server %s: refresh failed, detaching: %s", id, msg)
	r.set(id, func(e *entry) { e.state, e.err, e.tried, e.server = Failed, msg, now, nil })
	_ = server.Close()
}

// set applies f to id's entry under the lock, reporting false when the
// entry is gone.
func (r *Registry) set(id string, f func(*entry)) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[id]
	if !ok {
		return false
	}
	f(e)
	return true
}

// Update hands Run the configuration the daemon has just read, without
// waiting for it to be applied. Only the latest one is kept.
func (r *Registry) Update(servers map[string]protocol.ServerConfig) {
	r.mu.Lock()
	r.pending, r.updated = copyServers(servers), true
	r.mu.Unlock()
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Run syncs to each configuration Update hands it, and on its own every
// tick for the refreshes and retries that are due, until ctx is done;
// then it detaches every server. tick defaults to RetryInterval.
func (r *Registry) Run(ctx context.Context, tick time.Duration) {
	if tick <= 0 {
		tick = r.opts.RetryInterval
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	defer r.Close()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.wake:
		case <-t.C:
		}
		r.mu.RLock()
		servers, ok := r.pending, r.updated
		if !ok {
			servers = r.applied
		}
		r.mu.RUnlock()
		if !ok {
			// Nothing has said what is configured yet.
			continue
		}
		r.Sync(ctx, servers)
	}
}

// Close detaches every server.
func (r *Registry) Close() {
	r.syncMu.Lock()
	defer r.syncMu.Unlock()
	r.mu.Lock()
	entries := r.entries
	r.entries, r.applied = map[string]*entry{}, nil
	r.mu.Unlock()
	for _, e := range entries {
		if e.server != nil {
			_ = e.server.Close()
		}
	}
}

// Scrub replaces every occurrence of each value in an error message, so
// a server that echoes a credential it was handed back into an error
// cannot get it shown in Settings or written to the journal.
func Scrub(msg string, values []string) string {
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	for _, v := range values {
		if v != "" {
			msg = strings.ReplaceAll(msg, v, "[redacted]")
		}
	}
	return msg
}

// methodNames lists server's methods. Called only from Sync, the one
// goroutine that refreshes it.
func methodNames(server *protocol.Server) []string {
	out := make([]string, 0, len(server.Methods))
	for m := range server.Methods {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// methodDetails is methodNames with each method's description, args
// schema and statefulness, in the same order. The schemas are discovery's
// own normalized copies, which nothing mutates after a refresh.
func methodDetails(server *protocol.Server) []MethodDetail {
	out := make([]MethodDetail, 0, len(server.Methods))
	for _, name := range methodNames(server) {
		m := server.Methods[name]
		out = append(out, MethodDetail{Name: name, Description: m.Description, Args: m.ArgsSchema, Releasable: m.Releasable,
			Delivery: protocol.DeliveryMode(protocol.ResolveModelOnly(nil, m.MCPDelivery)), DeliveryDeclared: m.MCPDelivery != ""})
	}
	return out
}

func copyServers(in map[string]protocol.ServerConfig) map[string]protocol.ServerConfig {
	if in == nil {
		return nil
	}
	out := make(map[string]protocol.ServerConfig, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func normalize(in map[string]protocol.ServerConfig) map[string]protocol.ServerConfig {
	if len(in) == 0 {
		return nil
	}
	return in
}

// ProbeResult is what one Probe found: the server's methods and config
// schema as discovery read them, and the error that refused it, if one
// did -- scrubbed like any attach error.
type ProbeResult struct {
	ID           string         `json:"id"`
	OK           bool           `json:"ok"`
	Methods      []MethodDetail `json:"methods"`
	ConfigSchema map[string]any `json:"configSchema,omitempty"`
	Error        string         `json:"error,omitempty"`
	// Warnings are things Test found that do not stop the server
	// attaching (Options.ProbeWarnings).
	Warnings []string `json:"warnings,omitempty"`
}

// Probe attaches cfg in a session of its own -- initialize, tools/list
// and, for a server advertising configure, configure with cfg's config --
// reports what it found, and closes it again: Settings' Test button, run
// on an entry before or after it is saved. It never touches the attached
// set. extra resolves secrets ahead of the store, for a credential typed
// into the form and not saved yet.
func (r *Registry) Probe(ctx context.Context, id string, cfg protocol.ServerConfig, extra protocol.SecretLookup) ProbeResult {
	var resolved []string
	lookup := func(name string) (string, bool) {
		var v string
		ok := false
		if extra != nil {
			v, ok = extra(name)
		}
		if !ok && r.opts.Secrets != nil {
			v, ok = r.opts.Secrets(name)
		}
		if ok && v != "" {
			resolved = append(resolved, v)
		}
		return v, ok
	}
	// The probe's stderr is kept, so a server that exits before it
	// answers says why rather than just "closed its output".
	stderr := &tailBuffer{max: probeStderrMax}
	opts := protocol.DialOptions{ConfigRoot: r.opts.ConfigRoot, Hostd: r.opts.Hostd, Identity: r.opts.Identity(), Stderr: stderr, Managed: r.opts.Managed}
	if r.opts.Stderr != nil {
		if w := r.opts.Stderr(id); w != nil {
			opts.Stderr = io.MultiWriter(stderr, w)
		}
	}
	cfg.Disabled = false
	if cfg.Stdio() {
		env := make(map[string]string, len(cfg.Env)+1)
		for k, v := range cfg.Env {
			env[k] = v
		}
		env[protocol.ProbeEnv] = "1"
		cfg.Env = env
	}
	server, err := r.opts.Attach(ctx, id, cfg, lookup, opts)
	res := ProbeResult{ID: id, Methods: []MethodDetail{}}
	if r.opts.ProbeWarnings != nil {
		res.Warnings = r.opts.ProbeWarnings(id, cfg)
	}
	if err != nil {
		msg := err.Error()
		if out := strings.TrimSpace(stderr.String()); out != "" {
			msg += "; its stderr: " + out
		}
		res.Error = Scrub(msg, resolved)
		var ce *protocol.ConfigureError
		if errors.As(err, &ce) {
			res.ConfigSchema = protocol.OperatorSchema(ce.Schema)
		}
		return res
	}
	defer server.Close()
	res.OK = true
	res.Methods = methodDetails(server)
	res.ConfigSchema = protocol.OperatorSchema(server.ConfigSchema)
	return res
}

// ErrNoHostd is HostCapabilityEntries on a daemon with no grain hostd to
// start a run: host server through: one on the host itself, or on
// Kubernetes, where there is no host to run it on.
var ErrNoHostd = errors.New("this daemon has no grain hostd beside it, so no capability server can run on the host: " +
	"run: host needs a daemon in the release container with grain hostd installed (on Kubernetes there is no host to run on)")

// HostCapabilityEntries lists, sorted, the capability entries grain
// hostd's allowlist offers: the commands a run: host server may name.
func (r *Registry) HostCapabilityEntries(ctx context.Context) ([]string, error) {
	if r.opts.Hostd == nil {
		return nil, ErrNoHostd
	}
	offers, err := r.opts.Hostd.Offers(ctx)
	if err != nil {
		return nil, fmt.Errorf("asking grain hostd what it offers: %w", err)
	}
	var out []string
	for _, o := range offers {
		if o.Kind == hostd.KindCapability {
			out = append(out, o.Name)
		}
	}
	sort.Strings(out)
	return out, nil
}

// probeStderrMax bounds how much of a probed server's stderr a failed
// probe reports: the end of it, where the reason it exited is.
const probeStderrMax = 2 << 10

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	max int
	mu  sync.Mutex
	b   []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if over := len(t.b) - t.max; over > 0 {
		t.b = append(t.b[:0], t.b[over:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.b)
}
