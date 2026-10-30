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

package ui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/intree"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/capability/servers"
	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/model"
)

// The deployment's capability servers: grain_config.mcp_servers, keyed by
// capability_id (model.Config.MCPServers, docs/capabilities-spec.md
// §3.1). Settings and `grain capability-servers` add, remove, enable and
// disable them here and bind their env and headers entries to secrets in
// the encrypted secrets store; the daemon attaches what is saved on its
// next tick (pkg/capability/servers). A capability_id is a URL, slashes
// and all, so every route names it in its body rather than its path.
//
// What these routes return is the stored configuration -- $secret:<name>
// references, never what one resolves to -- and each server's attach
// status, whose errors name a missing secret by name only.

// CapabilityServerStatuses is what the daemon's registry reports
// (servers.Registry); nil where nothing attaches servers.
type CapabilityServerStatuses interface {
	Statuses() []servers.Status
}

// CapabilityServer is one configured server as Settings shows it.
type CapabilityServer struct {
	ID     string                `json:"id"`
	Config protocol.ServerConfig `json:"config"`
	// Secrets are the secret names the entry references, each with
	// whether the secrets store holds it.
	Secrets []CapabilityServerSecret `json:"secrets"`
	// ConfiguredSecretKeys lists the entry names present inside a JSON-map
	// secret field such as config-map's secret_values, without exposing
	// their values.
	ConfiguredSecretKeys []string `json:"configuredSecretKeys,omitempty"`
	// Status is the daemon's attach status; nil when this process does
	// not attach servers or has not yet seen this entry.
	Status *servers.Status `json:"status,omitempty"`
	// Managed are the configure fields grain generates and keeps itself
	// (protocol.ManagedSecretFields): Settings shows each as "managed by
	// grain", with its override under Advanced and a Rotate action.
	Managed []CapabilityServerManagedField `json:"managed,omitempty"`
	// Builtin is the in-tree server a built-in entry is
	// (intree.EnsureBuiltins): one grain registers itself, which can be
	// configured and disabled but not renamed or removed. Nil for an
	// operator's own entry.
	Builtin *intree.Builtin `json:"builtin,omitempty"`
}

// CapabilityServerManagedField is one configure field grain manages for
// an entry: the secret it keeps the value under, whether one is stored
// yet, and whether the entry's config overrides it with a value of the
// operator's own, which then wins.
type CapabilityServerManagedField struct {
	Field      string `json:"field"`
	Secret     string `json:"secret"`
	Present    bool   `json:"present"`
	Overridden bool   `json:"overridden"`
}

// CapabilityServerSecret is one $secret:<name> an entry references.
type CapabilityServerSecret struct {
	Name    string `json:"name"`
	Present bool   `json:"present"`
}

// SetCapabilityServerRequest adds or replaces one entry: an edit in
// place is the same request with the entry's new fields.
type SetCapabilityServerRequest struct {
	ID     string                `json:"id"`
	Config protocol.ServerConfig `json:"config"`
	// ConfigSecrets are values typed into the config form's secret
	// fields (writeOnly or format: password, §4.5.2), keyed by field.
	// Each is written to the secrets store -- under the secret the field
	// already references, or capability-server-<hash>/<field> -- and the
	// field is saved as the $secret:<name> reference; the value itself is
	// never stored in grain_config, exported or returned.
	ConfigSecrets map[string]string `json:"configSecrets,omitempty"`
	// RenameFrom is the capability_id this entry was saved under until
	// now: the entry is moved to ID in the same write, its config
	// checked against the schema the old entry's server reported.
	// Whatever grants the old id is not rewritten (Settings warns with
	// GET /api/capability-servers/usage first).
	RenameFrom string `json:"renameFrom,omitempty"`
}

// CapabilityServerProber is the part of the daemon's registry Settings'
// Test button and run-location check use (servers.Registry); a process
// that does not attach servers has none.
type CapabilityServerProber interface {
	Probe(ctx context.Context, id string, cfg protocol.ServerConfig, extra protocol.SecretLookup) servers.ProbeResult
	HostCapabilityEntries(ctx context.Context) ([]string, error)
}

// CapabilityServerToolCaller is the part of the daemon's registry
// Settings' per-script Test button uses (servers.Registry.CallTool).
type CapabilityServerToolCaller interface {
	CallTool(ctx context.Context, capabilityID, tool string, arguments map[string]any, timeout time.Duration) ([]byte, error)
}

// TestHostScriptRequest is POST /api/capability-servers/test-script: one
// script of an attached host-script server, by name, run once with Argv.
type TestHostScriptRequest struct {
	ID     string   `json:"id"`
	Script string   `json:"script"`
	Argv   []string `json:"argv"`
}

// TestGChatPostRequest is POST /api/capability-servers/gchat-test: one
// configured space of an attached gchat server, to post a test message
// to.
type TestGChatPostRequest struct {
	ID    string `json:"id"`
	Space string `json:"space"`
}

// TestCapabilityServerRequest is POST /api/capability-servers/test: the
// entry to try, which need not be saved yet. A nil Config tries the
// stored entry for ID.
type TestCapabilityServerRequest struct {
	ID            string                 `json:"id"`
	Config        *protocol.ServerConfig `json:"config,omitempty"`
	ConfigSecrets map[string]string      `json:"configSecrets,omitempty"`
}

// CapabilityServerHostEntries is GET /api/capability-servers/host-entries:
// whether a stdio server can be set to run: host here, and which hostd
// allowlist entries it may name.
type CapabilityServerHostEntries struct {
	Available bool     `json:"available"`
	Reason    string   `json:"reason,omitempty"`
	Entries   []string `json:"entries"`
	// Missing are grain's own entries (hostd.EnsureBuiltins' list for
	// this build) the allowlist lacks: grain adds them itself at hostd's
	// next start, and each says how to add it now.
	Missing []HostdMissingEntry `json:"missing,omitempty"`
}

// HostdMissingEntry is one of grain's own allowlist entries hostd lacks.
type HostdMissingEntry struct {
	Name string `json:"name"`
	// Entry is its text, to paste under capabilities:.
	Entry string `json:"entry"`
	// Command adds it now, on the host.
	Command string `json:"command"`
}

// MissingHostdBuiltins are this build's own entries that entries -- what
// hostd's allowlist offers -- lacks.
func MissingHostdBuiltins(entries []string) []HostdMissingEntry {
	var out []HostdMissingEntry
	for _, b := range intree.HostdBuiltins() {
		if !slices.Contains(entries, b.Name) {
			out = append(out, HostdMissingEntry{Name: b.Name, Entry: b.YAML(hostd.DefaultGrainCommand, "  ", "  "), Command: hostd.EnsureCommand})
		}
	}
	return out
}

// CapabilityServerIDRequest names one entry, for remove, enable and
// disable.
type CapabilityServerIDRequest struct {
	ID string `json:"id"`
}

// BindCapabilityServerSecretRequest points one env or headers entry at a
// secret: the entry's value becomes Prefix + "$secret:" + Secret. When
// Value is set it is written to the secrets store under Secret first
// (as <secret>/<key>, or <secret>/value for a bare name), so one call
// both stores a credential and binds it.
type BindCapabilityServerSecretRequest struct {
	ID string `json:"id"`
	// Kind is "env" or "header".
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Secret string `json:"secret"`
	// Prefix is literal text before the reference, as in
	// "Bearer $secret:token".
	Prefix string `json:"prefix,omitempty"`
	Value  string `json:"value,omitempty"`
}

// ListCapabilityServers returns every configured server, sorted by
// capability_id.
func (c *Client) ListCapabilityServers(ctx context.Context) ([]CapabilityServer, error) {
	cfg, err := c.Store.GetConfig(ctx)
	if err != nil {
		return nil, err
	}
	var configured map[string]protocol.ServerConfig
	if cfg != nil {
		configured = cfg.MCPServers
	}
	// The built-ins are listed whether or not the daemon has written
	// them back yet -- it cannot, where grain/config is external.
	configured, _ = intree.EnsureBuiltins(configured)
	statuses := map[string]servers.Status{}
	if c.Config.CapabilityServers != nil {
		for _, s := range c.Config.CapabilityServers.Statuses() {
			statuses[s.ID] = s
		}
	}
	present := c.secretNames()
	out := make([]CapabilityServer, 0, len(configured))
	for id, sc := range configured {
		row := CapabilityServer{ID: id, Config: sc, Secrets: []CapabilityServerSecret{}}
		for _, name := range sc.SecretRefs() {
			row.Secrets = append(row.Secrets, CapabilityServerSecret{Name: name, Present: present(name)})
		}
		row.ConfiguredSecretKeys = c.configuredSecretKeys(ctx, sc)
		if s, ok := statuses[id]; ok {
			if s.ConfigSchema == nil {
				s.ConfigSchema = intree.BuiltinConfigSchema(sc.Builtin)
			}
			row.Status = &s
		} else if schema := intree.BuiltinConfigSchema(sc.Builtin); schema != nil {
			state := servers.NeedsConfig
			if len(sc.Config) > 0 {
				state = servers.Pending
			}
			row.Status = &servers.Status{
				ID:           id,
				Builtin:      sc.Builtin,
				State:        state,
				ConfigSchema: schema,
			}
		}
		if row.Status != nil {
			for _, f := range protocol.ManagedSecretFields(row.Status.ConfigSchema) {
				name := protocol.ManagedSecretName(id, f)
				row.Managed = append(row.Managed, CapabilityServerManagedField{
					Field: f, Secret: name, Present: present(name),
					Overridden: protocol.ManagedOverridden(id, f, sc.Config),
				})
			}
		}
		if b, ok := intree.BuiltinNamed(sc.Builtin); ok {
			row.Builtin = &b
		}
		out = append(out, row)
	}
	// Built-ins first, then the operator's own, each by capability_id.
	sort.Slice(out, func(i, j int) bool {
		if bi, bj := out[i].Builtin != nil, out[j].Builtin != nil; bi != bj {
			return bi
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// secretNames reports which secret names the store can resolve: a
// <secret>/<key> whose key exists, or a bare <secret> holding exactly
// one key (secrets.Store.Resolve's own rule).
func (c *Client) secretNames() func(string) bool {
	keys := map[string][]string{}
	if c.Config.Secrets != nil {
		if list, err := c.Config.Secrets.List(); err == nil {
			for _, s := range list {
				keys[s.Name] = s.Keys
			}
		}
	}
	return func(name string) bool {
		secret, key, explicit := strings.Cut(name, "/")
		if !explicit {
			return len(keys[secret]) == 1
		}
		for _, k := range keys[secret] {
			if k == key {
				return true
			}
		}
		return false
	}
}

// updateCapabilityServers applies mutate to the stored mcpServers and
// validates the result as protocol.ParseServers would read it back.
func (c *Client) updateCapabilityServers(ctx context.Context, mutate func(map[string]protocol.ServerConfig) error) error {
	if err := c.checkSettingsWritable(ctx, "grain_config"); err != nil {
		return err
	}
	return c.Store.UpdateConfig(ctx, func(cfg *model.Config) error {
		next, _ := intree.EnsureBuiltins(cfg.MCPServers)
		if err := mutate(next); err != nil {
			return err
		}
		data, err := json.Marshal(next)
		if err != nil {
			return err
		}
		if _, err := protocol.ParseServers(data); err != nil {
			return validationErrorf("%s", err)
		}
		cfg.MCPServers = next
		return nil
	})
}

// SetCapabilityServer adds or replaces one entry.
func (c *Client) SetCapabilityServer(ctx context.Context, req SetCapabilityServerRequest) error {
	id := strings.TrimSpace(req.ID)
	if id == "" {
		return validationErrorf("a capability_id is required")
	}
	cfg := req.Config
	if err := c.checkRunLocation(ctx, cfg); err != nil {
		return err
	}
	from := strings.TrimSpace(req.RenameFrom)
	if from == id {
		from = ""
	}
	schemaID := id
	if from != "" {
		schemaID = from
	}
	schema := c.capabilityConfigSchema(schemaID)
	if schema == nil && (len(req.ConfigSecrets) > 0 || !c.storedConfigEqual(ctx, schemaID, cfg.Config)) {
		return validationErrorf("capability server %s has not told grain its config schema yet: test it first, so its configuration can be checked before it is saved", id)
	}
	// Each secret value typed in becomes a reference first, so the
	// config is validated -- and refused -- before anything is written.
	writes := map[string]string{}
	if len(req.ConfigSecrets) > 0 {
		secretField := map[string]bool{}
		for _, f := range protocol.SecretFields(schema) {
			secretField[f] = true
		}
		next := make(map[string]any, len(cfg.Config)+len(req.ConfigSecrets))
		for k, v := range cfg.Config {
			next[k] = v
		}
		for field, value := range req.ConfigSecrets {
			if !secretField[field] {
				return &ValidationError{err: fmt.Errorf("config.%s: is not a secret field of the server's config schema", field), fields: map[string]string{field: "not a secret field"}}
			}
			if value == "" {
				continue
			}
			name := configSecretName(id, field)
			if ref, ok := next[field].(string); ok && strings.HasPrefix(ref, "$secret:") {
				name = strings.TrimPrefix(ref, "$secret:")
			}
			if secretMapField(id, cfg, field) {
				value = c.mergeSecretMapValue(ctx, name, value, next["secrets"])
			}
			writes[name] = value
			next[field] = protocol.SecretRef(name)
		}
		cfg.Config = next
	}
	// A config-map save with no value typed still drops the stored values
	// of secret map keys it removed.
	if ref, ok := cfg.Config["secret_values"].(string); ok && strings.HasPrefix(ref, "$secret:") && secretMapField(id, cfg, "secret_values") {
		name := strings.TrimPrefix(ref, "$secret:")
		if _, typed := writes[name]; !typed {
			if pruned, changed := c.pruneSecretMapValue(ctx, name, cfg.Config["secrets"]); changed {
				writes[name] = pruned
			}
		}
	}
	// An entry saved without any config -- a transport edit, or a
	// server still configured by env -- is left to the attach to judge,
	// whose status says what is missing.
	if schema != nil && len(cfg.Config) > 0 {
		if err := protocol.CheckStoredConfig(schema, cfg.Config); err != nil {
			return configValidationError(err)
		}
	}
	// A built-in's own rules beyond its schema are checked here too, so a
	// save its configure would refuse never reaches the store and takes
	// the server out of use.
	if len(cfg.Config) > 0 {
		if err := intree.CheckBuiltinConfig(cfg.Builtin, cfg.Config); err != nil {
			return validationErrorf("%s", err)
		}
	}
	if len(writes) > 0 {
		// Unrehearsed: the secrets below are set before the store write
		// a rehearsal would stop at, and would outlive its refusal.
		if err := c.checkSettingsWritableUnrehearsed(ctx, "grain_config"); err != nil {
			return err
		}
		if c.Config.Secrets == nil {
			return errSecretsUnavailable
		}
		for name, value := range writes {
			secret, key, explicit := strings.Cut(name, "/")
			if !explicit {
				key = "value"
			}
			if err := c.Config.Secrets.Set(secret, key, []byte(value)); err != nil {
				return validationErrorf("%s", err)
			}
		}
	}
	return c.updateCapabilityServers(ctx, func(m map[string]protocol.ServerConfig) error {
		if from != "" {
			old, ok := m[from]
			if !ok {
				return validationErrorf("no capability server %q is configured", from)
			}
			if old.Builtin != "" {
				return validationErrorf("capability server %q is grain's built-in %s server: its capability_id cannot be changed", from, old.Builtin)
			}
			if _, ok := m[id]; ok {
				return validationErrorf("capability server %q is already configured", id)
			}
			delete(m, from)
		}
		// What makes an entry a built-in is grain's to say, not the
		// form's: an edit keeps it, and cannot make an entry one.
		cfg.Builtin, cfg.Aliases, cfg.Notice = "", nil, ""
		if stored, ok := m[id]; ok && stored.Builtin != "" {
			cfg.Builtin, cfg.Aliases, cfg.Notice = stored.Builtin, stored.Aliases, stored.Notice
		}
		m[id] = cfg
		return nil
	})
}

// configSecretName is the secret a config field's value is stored under
// when the field references none yet: one secret per server, keyed by
// field, named for the capability_id without carrying it (a URL is not a
// secret name).
func configSecretName(id, field string) string {
	sum := sha256.Sum256([]byte(id))
	return "capability-server-" + hex.EncodeToString(sum[:4]) + "/" + field
}

// configuredSecretKeys inspects a JSON-map secret field (such as
// config-map's secret_values) and returns the sorted keys -- for
// config-map, "<map>/<key>" -- that have non-empty values stored, without
// returning the values themselves.
func (c *Client) configuredSecretKeys(ctx context.Context, sc protocol.ServerConfig) []string {
	if c.Config.Secrets == nil || len(sc.Config) == 0 {
		return nil
	}
	ref, ok := sc.Config["secret_values"].(string)
	if !ok || !strings.HasPrefix(ref, "$secret:") {
		return nil
	}
	raw, err := c.Config.Secrets.Resolve(ctx, strings.TrimPrefix(ref, "$secret:"))
	if err != nil || strings.TrimSpace(raw) == "" {
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil
	}
	var keys []string
	for k, v := range m {
		if v != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// secretMapField reports whether field of the entry id is a JSON map of
// per-entry secrets that a save merges into what is stored rather than
// replaces: config-map's secret_values, and no other server's field, since
// any other secret whose value happens to be a JSON object (a service
// account key, say) must be stored exactly as typed.
func secretMapField(id string, cfg protocol.ServerConfig, field string) bool {
	return field == "secret_values" && (cfg.Builtin == "config-map" || id == intree.CanonicalID("config-map"))
}

// secretMapKeys is every "<map>/<key>" the config-map secrets list
// declares: the only secret_values keys worth keeping.
func secretMapKeys(secretsList any) map[string]bool {
	keep := map[string]bool{}
	list, _ := secretsList.([]any)
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		keys, _ := m["keys"].(map[string]any)
		for k := range keys {
			keep[intree.SecretValueKey(strings.TrimSpace(name), k)] = true
		}
	}
	return keep
}

// mergeSecretMapValue merges an incoming JSON-map secret value (such as
// secret_values, keyed "<map>/<key>") over any already-stored JSON map at
// existingRef so that setting or rotating one key does not erase the
// others whose value boxes were left blank, and drops the value of every
// key the secrets list no longer declares.
func (c *Client) mergeSecretMapValue(ctx context.Context, existingRef, incoming string, secretsList any) string {
	if c.Config.Secrets == nil {
		return incoming
	}
	var inMap map[string]string
	if err := json.Unmarshal([]byte(incoming), &inMap); err != nil {
		return incoming
	}
	merged := map[string]string{}
	if existing, err := c.Config.Secrets.Resolve(ctx, existingRef); err == nil && strings.TrimSpace(existing) != "" {
		var oldMap map[string]string
		if err := json.Unmarshal([]byte(existing), &oldMap); err == nil {
			for k, v := range oldMap {
				if v != "" {
					merged[k] = v
				}
			}
		}
	}
	for k, v := range inMap {
		if v != "" {
			merged[k] = v
		}
	}
	keep := secretMapKeys(secretsList)
	for k := range merged {
		if !keep[k] {
			delete(merged, k)
		}
	}
	b, err := json.Marshal(merged)
	if err != nil {
		return incoming
	}
	return string(b)
}

// pruneSecretMapValue is the stored JSON-map secret at ref without the
// keys secretsList no longer declares, and whether that removed any.
func (c *Client) pruneSecretMapValue(ctx context.Context, ref string, secretsList any) (string, bool) {
	if c.Config.Secrets == nil {
		return "", false
	}
	existing, err := c.Config.Secrets.Resolve(ctx, ref)
	if err != nil || strings.TrimSpace(existing) == "" {
		return "", false
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(existing), &m); err != nil {
		return "", false
	}
	keep := secretMapKeys(secretsList)
	changed := false
	for k := range m {
		if !keep[k] {
			delete(m, k)
			changed = true
		}
	}
	if !changed {
		return "", false
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// configErrorPath finds the config field a validator refusal is about.
var configErrorPath = regexp.MustCompile(`(?:^|[\s:(])config((?:\.[A-Za-z0-9_$\-]+|\[\d+\])*): (.*)$`)

// configValidationError is a config refusal keyed by its field, so the
// config form can show it inline.
func configValidationError(err error) error {
	ve := &ValidationError{err: err}
	if m := configErrorPath.FindStringSubmatch(err.Error()); m != nil {
		ve.fields = map[string]string{strings.TrimPrefix(m[1], "."): m[2]}
	}
	return ve
}

// capabilityConfigSchema is the configure schema the registry last
// discovered for id, nil when it knows of none.
func (c *Client) capabilityConfigSchema(id string) map[string]any {
	if c.Config.CapabilityServers != nil {
		for _, st := range c.Config.CapabilityServers.Statuses() {
			if st.ID == id && st.ConfigSchema != nil {
				return st.ConfigSchema
			}
		}
	}
	if name := strings.TrimPrefix(id, intree.CanonicalIDPrefix); name != id {
		if schema := intree.BuiltinConfigSchema(name); schema != nil {
			return schema
		}
	}
	return nil
}

// storedConfigEqual reports whether config is what id already has: an
// edit of an entry's transport that leaves its config alone needs no
// schema to check it against.
func (c *Client) storedConfigEqual(ctx context.Context, id string, config map[string]any) bool {
	if len(config) == 0 {
		return true
	}
	stored, err := c.Store.GetConfig(ctx)
	if err != nil || stored == nil {
		return false
	}
	configured, _ := intree.EnsureBuiltins(stored.MCPServers)
	a, _ := json.Marshal(configured[id].Config)
	b, _ := json.Marshal(config)
	return string(a) == string(b)
}

func (c *Client) prober() CapabilityServerProber {
	p, _ := c.Config.CapabilityServers.(CapabilityServerProber)
	return p
}

// checkRunLocation refuses run: host unless grain hostd is beside this
// daemon and its allowlist has the capability entry the command names
// (#5031: on Kubernetes, or a daemon on the host itself, there is none).
func (c *Client) checkRunLocation(ctx context.Context, cfg protocol.ServerConfig) error {
	if !cfg.OnHost() {
		return nil
	}
	hosts := c.HostCapabilityEntries(ctx)
	if !hosts.Available {
		return validationErrorf("run: host is not available here: %s", hosts.Reason)
	}
	for _, e := range hosts.Entries {
		if e == cfg.Command {
			return nil
		}
	}
	// grain's own entry is grain's to add (hostd.EnsureBuiltins, at
	// hostd's next start): its built-in may be configured meanwhile, and
	// attaches once the entry is there (the registry retries it).
	if _, ok := hostd.Builtin(cfg.Command); ok && intree.InTreeName(cfg) != "" {
		return nil
	}
	offered := "no capabilities entries"
	if len(hosts.Entries) > 0 {
		offered = strings.Join(hosts.Entries, ", ")
	}
	return validationErrorf("run: host: grain hostd's allowlist has no capabilities entry %q (it offers %s); %s",
		cfg.Command, offered, hostd.MissingEntryHelp(cfg.Command))
}

// HostCapabilityEntries says whether run: host is possible here and what
// it may name.
func (c *Client) HostCapabilityEntries(ctx context.Context) CapabilityServerHostEntries {
	out := CapabilityServerHostEntries{Entries: []string{}}
	p := c.prober()
	if p == nil {
		out.Reason = "this process does not attach capability servers"
		return out
	}
	entries, err := p.HostCapabilityEntries(ctx)
	if err != nil {
		out.Reason = err.Error()
		return out
	}
	out.Available, out.Entries = true, append(out.Entries, entries...)
	out.Missing = MissingHostdBuiltins(out.Entries)
	if len(entries) == 0 {
		out.Reason = "grain hostd's allowlist has no capabilities entries"
	}
	return out
}

// TestCapabilityServer runs initialize, tools/list and configure against
// an entry now, in a session of its own, and reports what came back.
// Secret values typed into the form are tried without being saved.
func (c *Client) TestCapabilityServer(ctx context.Context, req TestCapabilityServerRequest) (servers.ProbeResult, error) {
	id := strings.TrimSpace(req.ID)
	if err := protocol.ValidateCapabilityID(id); err != nil {
		return servers.ProbeResult{}, validationErrorf("%s", err)
	}
	p := c.prober()
	if p == nil {
		return servers.ProbeResult{}, validationErrorf("this process does not attach capability servers, so it cannot test one")
	}
	var cfg protocol.ServerConfig
	if req.Config != nil {
		cfg = *req.Config
	} else {
		stored, err := c.Store.GetConfig(ctx)
		if err != nil {
			return servers.ProbeResult{}, err
		}
		var configured map[string]protocol.ServerConfig
		if stored != nil {
			configured = stored.MCPServers
		}
		configured, _ = intree.EnsureBuiltins(configured)
		var ok bool
		cfg, ok = configured[id]
		if !ok {
			return servers.ProbeResult{}, validationErrorf("no capability server %q is configured", id)
		}
	}
	if err := cfg.Validate(id); err != nil {
		return servers.ProbeResult{}, validationErrorf("%s", err)
	}
	if err := c.checkRunLocation(ctx, cfg); err != nil {
		return servers.ProbeResult{}, err
	}
	// Typed-in secrets are offered under names of their own, which only
	// this probe resolves.
	extra := map[string]string{}
	if len(req.ConfigSecrets) > 0 {
		next := make(map[string]any, len(cfg.Config)+len(req.ConfigSecrets))
		for k, v := range cfg.Config {
			next[k] = v
		}
		for field, value := range req.ConfigSecrets {
			if value == "" {
				continue
			}
			existingRef := configSecretName(id, field)
			if ref, ok := next[field].(string); ok && strings.HasPrefix(ref, "$secret:") {
				existingRef = strings.TrimPrefix(ref, "$secret:")
			}
			name := "grain-capability-test/" + field
			if secretMapField(id, cfg, field) {
				value = c.mergeSecretMapValue(ctx, existingRef, value, next["secrets"])
			}
			extra[name] = value
			next[field] = protocol.SecretRef(name)
		}
		cfg.Config = next
	}
	return p.Probe(ctx, id, cfg, func(name string) (string, bool) {
		v, ok := extra[name]
		return v, ok
	}), nil
}

// TestHostScript runs one script of the attached host-script server id
// once, with argv exactly, through the server's test_script tool: the
// script it has configured now -- the saved configuration, not what a
// form holds unsaved -- in a throwaway grant the server mints and
// releases around the call, recorded in its audit log with caller
// settings-test.
func (c *Client) TestHostScript(ctx context.Context, req TestHostScriptRequest) (intree.HostScriptTestResult, error) {
	var out intree.HostScriptTestResult
	id := strings.TrimSpace(req.ID)
	if err := protocol.ValidateCapabilityID(id); err != nil {
		return out, validationErrorf("%s", err)
	}
	if strings.TrimSpace(req.Script) == "" {
		return out, validationErrorf("name the script to test")
	}
	caller, err := c.toolCaller(id, "test a script")
	if err != nil {
		return out, err
	}
	argv := make([]any, len(req.Argv))
	for i, a := range req.Argv {
		argv[i] = a
	}
	raw, err := caller.CallTool(ctx, id, intree.HostScriptTestTool, map[string]any{"script": req.Script, "argv": argv}, intree.HostScriptTestTimeout+30*time.Second)
	if err != nil {
		return out, validationErrorf("%s", err)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("capability server %s: test_script answered %q: %w", id, raw, err)
	}
	return out, nil
}

// toolCaller is the registry's CallTool, for a Settings button that
// calls one of an attached server's control-plane tools; what says why
// the button cannot work in a process that attaches none.
func (c *Client) toolCaller(id, what string) (CapabilityServerToolCaller, error) {
	if err := protocol.ValidateCapabilityID(id); err != nil {
		return nil, validationErrorf("%s", err)
	}
	caller, _ := c.Config.CapabilityServers.(CapabilityServerToolCaller)
	if caller == nil {
		return nil, validationErrorf("this process does not attach capability servers, so it cannot %s", what)
	}
	return caller, nil
}

// gchatCaller is toolCaller for an id whose entry runs grain's gchat
// server -- the built-in, or an operator's own `grain capability gchat`
// instance -- and only that: the gchat routes call a tool by a name
// another server may advertise too ("usage"), and GET gchat-usage needs
// only capabilities:read, so it must not reach any other attached server.
func (c *Client) gchatCaller(ctx context.Context, id, what string) (CapabilityServerToolCaller, error) {
	caller, err := c.toolCaller(id, what)
	if err != nil {
		return nil, err
	}
	cfg, err := c.Store.GetConfig(ctx)
	if err != nil {
		return nil, err
	}
	var configured map[string]protocol.ServerConfig
	if cfg != nil {
		configured = cfg.MCPServers
	}
	configured, _ = intree.EnsureBuiltins(configured)
	if entry, ok := configured[id]; !ok || (entry.Builtin != intree.GChatName && intree.InTreeName(entry) != intree.GChatName) {
		return nil, validationErrorf("capability server %q does not run grain's gchat server", id)
	}
	return caller, nil
}

// TestGChatPost posts one clearly marked test message to a configured
// space of the attached gchat server id, through its test_post tool: the
// space as the server has it configured now (the saved configuration),
// outside any grant, recorded with caller settings-test and counted in
// the space's usage. A post Chat refused comes back as the result's
// Error, not as an error here.
func (c *Client) TestGChatPost(ctx context.Context, req TestGChatPostRequest) (intree.GChatTestResult, error) {
	var out intree.GChatTestResult
	id := strings.TrimSpace(req.ID)
	if strings.TrimSpace(req.Space) == "" {
		return out, validationErrorf("name the space to post a test message to")
	}
	caller, err := c.gchatCaller(ctx, id, "post a test message")
	if err != nil {
		return out, err
	}
	raw, err := caller.CallTool(ctx, id, intree.GChatTestTool, map[string]any{"space": req.Space}, gchatToolTimeout)
	if err != nil {
		return out, validationErrorf("%s", err)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("capability server %s: %s answered %q: %w", id, intree.GChatTestTool, raw, err)
	}
	return out, nil
}

// gchatToolTimeout bounds a gchat control-plane call: one post, and the
// margin the registry's round trip needs.
const gchatToolTimeout = time.Minute

// GChatUsage is each configured space's usage on the attached gchat
// server id, through its usage tool: posted, refused and failed since the
// server started, and in the last hour against the space's per-hour cap.
func (c *Client) GChatUsage(ctx context.Context, id string) ([]intree.GChatUsage, error) {
	id = strings.TrimSpace(id)
	caller, err := c.gchatCaller(ctx, id, "read a gchat server's usage")
	if err != nil {
		return nil, err
	}
	raw, err := caller.CallTool(ctx, id, intree.GChatUsageTool, map[string]any{}, gchatToolTimeout)
	if err != nil {
		return nil, validationErrorf("%s", err)
	}
	out := []intree.GChatUsage{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("capability server %s: %s answered %q: %w", id, intree.GChatUsageTool, raw, err)
	}
	return out, nil
}

// RemoveCapabilityServer deletes one entry.
func (c *Client) RemoveCapabilityServer(ctx context.Context, id string) error {
	return c.updateCapabilityServers(ctx, func(m map[string]protocol.ServerConfig) error {
		sc, ok := m[id]
		if !ok {
			return validationErrorf("no capability server %q is configured", id)
		}
		if sc.Builtin != "" {
			return validationErrorf("capability server %q is grain's built-in %s server and cannot be removed: disable it instead (grain capability-servers disable %s)", id, sc.Builtin, id)
		}
		delete(m, id)
		return nil
	})
}

// DismissCapabilityServerNotice clears one entry's one-time notice
// (protocol.ServerConfig.Notice).
func (c *Client) DismissCapabilityServerNotice(ctx context.Context, id string) error {
	return c.updateCapabilityServers(ctx, func(m map[string]protocol.ServerConfig) error {
		sc, ok := m[id]
		if !ok {
			return validationErrorf("no capability server %q is configured", id)
		}
		sc.Notice = ""
		m[id] = sc
		return nil
	})
}

// CapabilityServerRotator is the part of the daemon's registry the
// Rotate action uses (servers.Registry.RotateManaged).
type CapabilityServerRotator interface {
	RotateManaged(capabilityID string) ([]string, error)
}

// RotateCapabilityServerManaged replaces every grain-managed secret of
// one entry its config does not override -- its data-plane token -- and
// attaches the server again under the new value. grain_config is not
// touched: it never held the value, only (for an override) a reference.
func (c *Client) RotateCapabilityServerManaged(ctx context.Context, id string) error {
	if c.Config.Secrets == nil {
		return errSecretsUnavailable
	}
	rot, ok := c.Config.CapabilityServers.(CapabilityServerRotator)
	if !ok {
		return validationErrorf("this grain process does not attach capability servers, so it has nothing to rotate")
	}
	if _, err := rot.RotateManaged(strings.TrimSpace(id)); err != nil {
		return validationErrorf("%s", err)
	}
	return nil
}

// SetCapabilityServerDisabled enables or disables one entry.
func (c *Client) SetCapabilityServerDisabled(ctx context.Context, id string, disabled bool) error {
	return c.updateCapabilityServers(ctx, func(m map[string]protocol.ServerConfig) error {
		sc, ok := m[id]
		if !ok {
			return validationErrorf("no capability server %q is configured", id)
		}
		sc.Disabled = disabled
		m[id] = sc
		return nil
	})
}

// BindCapabilityServerSecret binds one env or headers entry to a secret,
// storing req.Value under it first when one is given.
func (c *Client) BindCapabilityServerSecret(ctx context.Context, req BindCapabilityServerSecretRequest) error {
	name := strings.TrimSpace(req.Name)
	ref := strings.TrimSpace(req.Secret)
	if name == "" {
		return validationErrorf("an env or header name is required")
	}
	if ref == "" || strings.ContainsAny(ref, " \t\r\n") {
		return validationErrorf("a secret name without whitespace is required")
	}
	if req.Kind != "env" && req.Kind != "header" {
		return validationErrorf(`kind must be "env" or "header", not %q`, req.Kind)
	}
	if err := c.checkBootstrapWritableUnrehearsed(ctx, "grain_config"); err != nil {
		return err
	}
	if req.Value != "" {
		if c.Config.Secrets == nil {
			return errSecretsUnavailable
		}
		secret, key, explicit := strings.Cut(ref, "/")
		if !explicit {
			key = "value"
		}
		if err := c.Config.Secrets.Set(secret, key, []byte(req.Value)); err != nil {
			return validationErrorf("%s", err)
		}
	}
	value := req.Prefix + "$secret:" + ref
	return c.updateCapabilityServers(ctx, func(m map[string]protocol.ServerConfig) error {
		sc, ok := m[req.ID]
		if !ok {
			return validationErrorf("no capability server %q is configured", req.ID)
		}
		if req.Kind == "env" {
			sc.Env = withEntry(sc.Env, name, value)
		} else {
			sc.Headers = withEntry(sc.Headers, name, value)
		}
		m[req.ID] = sc
		return nil
	})
}

func withEntry(in map[string]string, k, v string) map[string]string {
	out := make(map[string]string, len(in)+1)
	for key, val := range in {
		out[key] = val
	}
	out[k] = v
	return out
}

func (s *Server) respondWithCapabilityServers(w http.ResponseWriter, r *http.Request) {
	list, err := s.tasks.ListCapabilityServers(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleListCapabilityServers(w http.ResponseWriter, r *http.Request) {
	s.respondWithCapabilityServers(w, r)
}

func (s *Server) handleSetCapabilityServer(w http.ResponseWriter, r *http.Request) {
	var req SetCapabilityServerRequest
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.tasks.SetCapabilityServer(r.Context(), req); err != nil {
		writeClientError(w, err)
		return
	}
	s.wakeReconciler()
	s.respondWithCapabilityServers(w, r)
}

func (s *Server) handleCapabilityServerAction(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req CapabilityServerIDRequest
		if !readJSON(w, r, &req) {
			return
		}
		var err error
		switch action {
		case "remove":
			err = s.tasks.RemoveCapabilityServer(r.Context(), req.ID)
		case "enable":
			err = s.tasks.SetCapabilityServerDisabled(r.Context(), req.ID, false)
		case "disable":
			err = s.tasks.SetCapabilityServerDisabled(r.Context(), req.ID, true)
		case "dismiss-notice":
			err = s.tasks.DismissCapabilityServerNotice(r.Context(), req.ID)
		case "rotate-managed":
			err = s.tasks.RotateCapabilityServerManaged(r.Context(), req.ID)
		default:
			err = fmt.Errorf("unknown capability server action %q", action)
		}
		if err != nil {
			writeClientError(w, err)
			return
		}
		s.wakeReconciler()
		s.respondWithCapabilityServers(w, r)
	}
}

func (s *Server) handleBindCapabilityServerSecret(w http.ResponseWriter, r *http.Request) {
	var req BindCapabilityServerSecretRequest
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.tasks.BindCapabilityServerSecret(r.Context(), req); err != nil {
		if errors.Is(err, errSecretsUnavailable) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeClientError(w, err)
		return
	}
	s.wakeReconciler()
	s.respondWithCapabilityServers(w, r)
}

func (s *Server) handleTestCapabilityServer(w http.ResponseWriter, r *http.Request) {
	var req TestCapabilityServerRequest
	if !readJSON(w, r, &req) {
		return
	}
	res, err := s.tasks.TestCapabilityServer(r.Context(), req)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleTestHostScript(w http.ResponseWriter, r *http.Request) {
	var req TestHostScriptRequest
	if !readJSON(w, r, &req) {
		return
	}
	res, err := s.tasks.TestHostScript(r.Context(), req)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleTestGChatPost(w http.ResponseWriter, r *http.Request) {
	var req TestGChatPostRequest
	if !readJSON(w, r, &req) {
		return
	}
	res, err := s.tasks.TestGChatPost(r.Context(), req)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleGChatUsage(w http.ResponseWriter, r *http.Request) {
	res, err := s.tasks.GChatUsage(r.Context(), r.URL.Query().Get("id"))
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleCapabilityServerHostEntries(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.tasks.HostCapabilityEntries(r.Context()))
}

// wakeReconciler asks the reconcile loop for a tick now, so a saved
// server is attached in seconds rather than at the next poll.
func (s *Server) wakeReconciler() {
	if s.tasks.Config.Wake != nil {
		s.tasks.Config.Wake()
	}
}

// ListCapabilityServers is GET /api/capability-servers.
func (c *HTTPClient) ListCapabilityServers(ctx context.Context) ([]CapabilityServer, error) {
	var out []CapabilityServer
	err := c.do(ctx, http.MethodGet, "/api/capability-servers", nil, &out)
	return out, err
}

// SetCapabilityServer is PUT /api/capability-servers.
func (c *HTTPClient) SetCapabilityServer(ctx context.Context, req SetCapabilityServerRequest) ([]CapabilityServer, error) {
	var out []CapabilityServer
	err := c.do(ctx, http.MethodPut, "/api/capability-servers", req, &out)
	return out, err
}

// CapabilityServerAction is POST /api/capability-servers/{remove,enable,disable}.
func (c *HTTPClient) CapabilityServerAction(ctx context.Context, action, id string) ([]CapabilityServer, error) {
	var out []CapabilityServer
	err := c.do(ctx, http.MethodPost, "/api/capability-servers/"+url.PathEscape(action), CapabilityServerIDRequest{ID: id}, &out)
	return out, err
}

// BindCapabilityServerSecret is POST /api/capability-servers/bind-secret.
func (c *HTTPClient) BindCapabilityServerSecret(ctx context.Context, req BindCapabilityServerSecretRequest) ([]CapabilityServer, error) {
	var out []CapabilityServer
	err := c.do(ctx, http.MethodPost, "/api/capability-servers/bind-secret", req, &out)
	return out, err
}

// TestCapabilityServer is POST /api/capability-servers/test.
func (c *HTTPClient) TestCapabilityServer(ctx context.Context, req TestCapabilityServerRequest) (servers.ProbeResult, error) {
	var out servers.ProbeResult
	err := c.do(ctx, http.MethodPost, "/api/capability-servers/test", req, &out)
	return out, err
}

// TestHostScript is POST /api/capability-servers/test-script.
func (c *HTTPClient) TestHostScript(ctx context.Context, req TestHostScriptRequest) (intree.HostScriptTestResult, error) {
	var out intree.HostScriptTestResult
	err := c.do(ctx, http.MethodPost, "/api/capability-servers/test-script", req, &out)
	return out, err
}

// TestGChatPost is POST /api/capability-servers/gchat-test.
func (c *HTTPClient) TestGChatPost(ctx context.Context, req TestGChatPostRequest) (intree.GChatTestResult, error) {
	var out intree.GChatTestResult
	err := c.do(ctx, http.MethodPost, "/api/capability-servers/gchat-test", req, &out)
	return out, err
}

// GChatUsage is GET /api/capability-servers/gchat-usage.
func (c *HTTPClient) GChatUsage(ctx context.Context, id string) ([]intree.GChatUsage, error) {
	var out []intree.GChatUsage
	err := c.do(ctx, http.MethodGet, "/api/capability-servers/gchat-usage?id="+url.QueryEscape(id), nil, &out)
	return out, err
}

// CapabilityServerHostEntries is GET /api/capability-servers/host-entries.
func (c *HTTPClient) CapabilityServerHostEntries(ctx context.Context) (CapabilityServerHostEntries, error) {
	var out CapabilityServerHostEntries
	err := c.do(ctx, http.MethodGet, "/api/capability-servers/host-entries", nil, &out)
	return out, err
}
