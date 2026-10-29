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

package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
)

// This file is how a capability reference (docs/capabilities-spec.md
// §2.1-§2.3) is carried by the grant lists grain already has: a
// Grant.Capability, a Config.DefaultCapabilities entry, a declaration's
// capabilities: list, propose_task's capabilities argument. Each holds a
// string, and a reference is written into it exactly as the grain CLI's
// -capability flag takes one (§2.2.3): the reference on its own, or the
// reference, "=", and its with: arguments as a JSON object
// (protocol.ParseCLIGrant reads either back).
//
// Once a grant has been normalized against its server's schema -- at
// creation, and again at admission -- it is recorded in canonical form
// (RecordedCapabilityGrant): the resolved <capability_id>#<method>, "=",
// and the normalized args, defaults applied, keys sorted. Two recorded
// grants are then equal (§2.3.2), and one covers the other (§2.3.4),
// exactly when their strings are equal, so every comparison the
// in-process grant names were already compared by keeps working for
// references too.
//
// Everything that is not a reference is one of the in-process capability
// names (pkg/capability), which keep working alongside references until
// the in-tree capabilities have moved over to servers.

// IsCapabilityRef reports whether a grant string is a capability
// reference rather than an in-process capability name. Names never
// contain "://", so the scheme alone tells them apart.
func IsCapabilityRef(s string) bool {
	return strings.HasPrefix(strings.TrimSpace(s), "https://")
}

// CapabilityGrantSpec reads a grant string as a reference and its with:
// arguments (§2.2.3's form).
func CapabilityGrantSpec(s string) (protocol.GrantSpec, error) {
	return protocol.ParseCLIGrant(strings.TrimSpace(s))
}

// EncodeCapabilityGrant writes spec as a grant string: the reference
// alone when it has no arguments, otherwise reference=<JSON>.
func EncodeCapabilityGrant(spec protocol.GrantSpec) (string, error) {
	if spec.With == nil {
		return spec.Capability, nil
	}
	data, err := json.Marshal(spec.With)
	if err != nil {
		return "", fmt.Errorf("capability %s: encoding with: %w", spec.Capability, err)
	}
	return spec.Capability + "=" + string(data), nil
}

// RecordedCapabilityGrant is a normalized grant's canonical string: its
// resolved reference and normalized args (§2.3.1). encoding/json sorts
// map keys, so equal grants always record equal strings.
func RecordedCapabilityGrant(g protocol.Grant) string {
	args := g.Args
	if args == nil {
		args = map[string]any{}
	}
	data, err := json.Marshal(args)
	if err != nil {
		// Normalized args came out of JSON and validated against a
		// schema; they always encode.
		panic(fmt.Sprintf("model: encoding normalized args of %s: %v", g.Ref, err))
	}
	return g.Ref.String() + "=" + string(data)
}

// CapabilityGrantRef is the reference part of a grant string, without
// its arguments: what policy matches against.
func CapabilityGrantRef(s string) string {
	ref, _, _ := strings.Cut(strings.TrimSpace(s), "=")
	return ref
}

// CheckCapabilityGrantSyntax checks what can be checked of a reference
// grant without its server: that it parses and that the reference
// conforms to §2.1's grammar. Normalize against the server's schema is
// the rest of the check, wherever a server can be reached.
func CheckCapabilityGrantSyntax(s string) error {
	spec, err := CapabilityGrantSpec(s)
	if err != nil {
		return err
	}
	_, err = protocol.ParseRef(spec.Capability)
	return err
}

// NormalizeCapabilityNames validates every reference among names against
// its server (protocol.Normalize, §4.2.1) and returns names with each
// reference replaced by its recorded form, duplicates dropped, first
// occurrence first (§2.3.2). In-process names pass through untouched.
// With lookup nil only the syntax is checked and references are kept as
// written: the caller has no servers to ask.
func NormalizeCapabilityNames(names []string, lookup protocol.Lookup) ([]string, error) {
	out := make([]string, 0, len(names))
	seen := map[string]bool{}
	for _, name := range names {
		if IsCapabilityRef(name) {
			recorded, err := normalizeCapabilityRef(name, lookup)
			if err != nil {
				return nil, err
			}
			name = recorded
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out, nil
}

func normalizeCapabilityRef(s string, lookup protocol.Lookup) (string, error) {
	if lookup == nil {
		if err := CheckCapabilityGrantSyntax(s); err != nil {
			return "", err
		}
		return strings.TrimSpace(s), nil
	}
	spec, err := CapabilityGrantSpec(s)
	if err != nil {
		return "", err
	}
	grants, err := protocol.Normalize([]protocol.GrantSpec{spec}, lookup)
	if err != nil {
		return "", err
	}
	return RecordedCapabilityGrant(grants[0]), nil
}

// NormalizeCapabilityGrants is NormalizeCapabilityNames for a task's
// Grants, keeping each one's Via and Folder.
func NormalizeCapabilityGrants(grants []Grant, lookup protocol.Lookup) ([]Grant, error) {
	out := make([]Grant, 0, len(grants))
	seen := map[string]bool{}
	for _, g := range grants {
		if IsCapabilityRef(g.Capability) {
			recorded, err := normalizeCapabilityRef(g.Capability, lookup)
			if err != nil {
				return nil, err
			}
			g.Capability = recorded
		}
		if seen[g.Capability] {
			continue
		}
		seen[g.Capability] = true
		out = append(out, g)
	}
	return out, nil
}

// CapabilityGrants is the protocol grants among a task's Grants,
// resolved and validated against their servers and deduplicated in
// declaration order (protocol.Normalize): the list a pass is minted from,
// whose indexes are its attempt_ids' (§2.3.3).
func CapabilityGrants(grants []Grant, lookup protocol.Lookup) ([]protocol.Grant, error) {
	var specs []protocol.GrantSpec
	for _, g := range grants {
		if !IsCapabilityRef(g.Capability) {
			continue
		}
		spec, err := CapabilityGrantSpec(g.Capability)
		if err != nil {
			return nil, err
		}
		specs = append(specs, spec)
	}
	if len(specs) == 0 {
		return nil, nil
	}
	return protocol.Normalize(specs, lookup)
}

// HasCapabilityRef reports whether any of grants is a reference.
func HasCapabilityRef(grants []Grant) bool {
	for _, g := range grants {
		if IsCapabilityRef(g.Capability) {
			return true
		}
	}
	return false
}

// CapabilityList is a capabilities: list as a declaration or a request
// writes it (§2.2.1): each entry an in-process capability name, a bare
// reference, or a {capability, with} object. It decodes to grant strings
// (EncodeCapabilityGrant), so it is a []string to everything that reads
// it.
type CapabilityList []string

// UnmarshalYAML accepts strings and {capability, with} objects.
func (l *CapabilityList) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.SequenceNode {
		return fmt.Errorf("line %d: capabilities must be a list", node.Line)
	}
	out := make(CapabilityList, 0, len(node.Content))
	for _, item := range node.Content {
		var v any
		if err := item.Decode(&v); err != nil {
			return err
		}
		raw, err := json.Marshal(yamlToJSON(v))
		if err != nil {
			return fmt.Errorf("line %d: capability: %w", item.Line, err)
		}
		s, err := capabilityEntries(raw)
		if err != nil {
			return fmt.Errorf("line %d: %w", item.Line, err)
		}
		out = append(out, s...)
	}
	*l = out
	return nil
}

// UnmarshalJSON accepts strings and {capability, with} objects.
func (l *CapabilityList) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		*l = nil
		return nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(data, &items); err != nil {
		return fmt.Errorf("capabilities must be a list")
	}
	out := make(CapabilityList, 0, len(items))
	for _, raw := range items {
		s, err := capabilityEntries(raw)
		if err != nil {
			return err
		}
		out = append(out, s...)
	}
	*l = out
	return nil
}

// CapabilityEntryNames reads one capabilities: entry in its JSON form -- a
// name, a bare reference, or a {capability, with} object -- as the grant
// strings it stands for: one, except for {capability: api, with: ...},
// which is one api grant per set or scope it names (APIWithGrantNames).
func CapabilityEntryNames(raw json.RawMessage) ([]string, error) { return capabilityEntries(raw) }

func capabilityEntries(raw json.RawMessage) ([]string, error) {
	spec, err := protocol.ParseGrant(raw)
	if err == nil && !spec.IsBare() && strings.TrimSpace(spec.Capability) == APIWithCapability {
		return APIWithGrantNames(spec.With)
	}
	s, err := capabilityEntry(raw)
	if err != nil {
		return nil, err
	}
	return []string{s}, nil
}

func capabilityEntry(raw json.RawMessage) (string, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s), nil
	}
	spec, err := protocol.ParseGrant(raw)
	if err != nil {
		return "", err
	}
	if !IsCapabilityRef(spec.Capability) {
		return "", fmt.Errorf("capability grant %q: only a capability reference (https://...) takes with: arguments", spec.Capability)
	}
	return EncodeCapabilityGrant(spec)
}

// yamlToJSON turns what yaml.v3 decodes into any -- whose maps it may key
// by any type -- into values encoding/json can write.
func yamlToJSON(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = yamlToJSON(val)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[fmt.Sprint(k)] = yamlToJSON(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = yamlToJSON(val)
		}
		return out
	default:
		return v
	}
}

// joinCapabilities and splitCapabilities are a stored capability list's
// text. A list of names is comma-separated, as it always was; a list
// holding an entry with a comma in it -- a reference whose JSON args do
// -- is stored as a JSON array instead, which no list of names ever
// starts with.
func joinCapabilities(items []string) string {
	for _, item := range items {
		if strings.Contains(item, ",") {
			data, _ := json.Marshal(items)
			return string(data)
		}
	}
	return joinCSV(items)
}

func splitCapabilities(s string) []string {
	if strings.HasPrefix(s, "[") {
		var out []string
		if err := json.Unmarshal([]byte(s), &out); err == nil {
			return out
		}
	}
	return splitCSV(s)
}

// CapabilityPolicy is the deployment's admission policy for capability
// references (docs/capabilities-spec.md §4.2.2): which repositories, run
// kinds and trigger events may hold which <capability_id>#<method>. It is
// grain's own configuration (Config.CapabilityPolicy), never anything a
// capability server advertises.
//
// A reference no rule names may be held by any trusted run. One that
// some rule names may be held only by a run one of those rules admits.
// A Deny rule refuses the runs it covers whatever any other rule admits,
// and does not on its own restrict anything it does not cover.
// A run whose trigger carries untrusted input -- a pull_request from a
// fork or from an author without write access -- holds no reference at
// all unless a rule naming it sets AllowUntrusted.
type CapabilityPolicy struct {
	Rules []CapabilityPolicyRule `json:"rules,omitempty"`
	// Catalogue is settings/capabilities.yml's restrictions
	// (capability_catalogue.go), nil on a deployment whose grain/config
	// has no such file.
	Catalogue *CapabilityCatalogue `json:"catalogue,omitempty"`
}

// CapabilityPolicyRule admits the references Capability matches.
type CapabilityPolicyRule struct {
	// Capability is a <capability_id>#<method>, or a path.Match pattern
	// of one ("https://github.com/acme/caps/*").
	Capability string `json:"capability"`
	// Repos are owner/name patterns the run's repository must match;
	// empty admits any repository.
	Repos []string `json:"repos,omitempty"`
	// RunKinds are CapabilityRunTask, CapabilityRunProcess and
	// CapabilityRunCI; empty admits every kind.
	RunKinds []string `json:"runKinds,omitempty"`
	// Events are the trigger events (push, pull_request, schedule, ...)
	// a CI run must have been started by; empty admits any event.
	Events []string `json:"events,omitempty"`
	// AllowUntrusted opts the capability in for runs whose trigger
	// carries untrusted input, for the Events listed.
	AllowUntrusted bool `json:"allowUntrusted,omitempty"`
	// Deny refuses the reference to every run the rule's Repos,
	// RunKinds and Events cover -- trusted or not -- ahead of any rule
	// that would admit it. AllowUntrusted means nothing on a deny rule.
	Deny bool `json:"deny,omitempty"`
}

// The run kinds a CapabilityPolicyRule names.
const (
	CapabilityRunTask    = "task"
	CapabilityRunProcess = "process"
	CapabilityRunCI      = "ci"
)

// CapabilityPolicyVerdict is what a CapabilityPolicy says about a
// capability reference when only the run kind is known (for a grant
// picker before a run starts).
type CapabilityPolicyVerdict string

const (
	// CapabilityPolicyAllowed means every trusted run of the kind is
	// admitted by the policy.
	CapabilityPolicyAllowed CapabilityPolicyVerdict = ""
	// CapabilityPolicyDenied means the policy refuses the reference to
	// every run of the kind, whatever its repository or trigger event.
	CapabilityPolicyDenied CapabilityPolicyVerdict = "denied"
	// CapabilityPolicyMayBeRefused means the policy admits some runs of
	// the kind and refuses others, depending on the repository or
	// trigger event.
	CapabilityPolicyMayBeRefused CapabilityPolicyVerdict = "may_be_refused"
)

// CapabilitySubject is the run a grant is admitted for.
type CapabilitySubject struct {
	Repo *RepoRef
	Kind string
	// Event is the CI trigger event; "" for an agent task.
	Event string
	// Untrusted is a trigger carrying untrusted input (§4.2.2).
	Untrusted bool
}

// Validate reports a rule that can never match as written.
func (p CapabilityPolicy) Validate() error {
	for i, r := range p.Rules {
		if !IsCapabilityRef(r.Capability) {
			return fmt.Errorf("capability policy rule %d: capability %q is not a capability reference", i, r.Capability)
		}
		if _, err := path.Match(r.Capability, ""); err != nil {
			return fmt.Errorf("capability policy rule %d: %w", i, err)
		}
		for _, k := range r.RunKinds {
			switch k {
			case CapabilityRunTask, CapabilityRunProcess, CapabilityRunCI:
			default:
				return fmt.Errorf("capability policy rule %d: run kind %q is not task, process or ci", i, k)
			}
		}
		for _, repo := range r.Repos {
			if _, err := path.Match(strings.ToLower(repo), ""); err != nil {
				return fmt.Errorf("capability policy rule %d: repo %q: %w", i, repo, err)
			}
		}
	}
	return nil
}

// Permits reports why the grant string g may not be held by s, or nil
// when it may. In-process names are not this policy's to judge. When
// servers is given, a rule naming an entry's canonical capability_id or
// any of its aliases (ServerConfig.Aliases) applies to grants naming
// either, so adopting a hand-added entry under its canonical id keeps
// every allow and deny rule that named the old id in effect.
func (p CapabilityPolicy) Permits(g string, s CapabilitySubject, servers ...map[string]protocol.ServerConfig) error {
	if !IsCapabilityRef(g) {
		return nil
	}
	ref := CapabilityGrantRef(g)
	refs := equivalentCapabilityRefs(ref, servers)
	for i, r := range p.Rules {
		if !r.Deny || !r.namesAny(refs) {
			continue
		}
		if r.covers(s) {
			return fmt.Errorf("the capability policy denies %s to this %s (rule %d: deny %s)", ref, s.describe(), i+1, r.Capability)
		}
	}
	named := false
	for _, r := range p.Rules {
		if r.Deny || !r.namesAny(refs) {
			continue
		}
		named = true
		if r.admits(s) {
			return nil
		}
	}
	switch {
	case s.Untrusted:
		return fmt.Errorf("capability %s is not granted to runs triggered by untrusted input (a pull request from a fork or from an author without write access); a capability policy rule must opt it in with allowUntrusted", ref)
	case named:
		return fmt.Errorf("the capability policy does not admit %s for this %s", ref, s.describe())
	}
	return nil
}

// VerdictForKind reports what p does to a grant of ref for a trusted run
// of kind when the repository and trigger event are not yet known:
// CapabilityPolicyDenied when every run of that kind is refused,
// CapabilityPolicyMayBeRefused when a repository- or event-dependent rule
// may refuse it, or CapabilityPolicyAllowed when every trusted run of
// that kind is admitted.
func (p CapabilityPolicy) VerdictForKind(ref, kind string, servers ...map[string]protocol.ServerConfig) CapabilityPolicyVerdict {
	if !IsCapabilityRef(ref) {
		return CapabilityPolicyAllowed
	}
	ref = CapabilityGrantRef(ref)
	refs := equivalentCapabilityRefs(ref, servers)
	allOfKind := CapabilitySubject{Kind: kind}
	anyDeny := false
	for _, r := range p.Rules {
		if !r.Deny || !r.namesAny(refs) {
			continue
		}
		if r.covers(allOfKind) {
			return CapabilityPolicyDenied
		}
		if r.coversAnyOfKind(kind) {
			anyDeny = true
		}
	}
	named := false
	admitsAll := false
	admitsSome := false
	for _, r := range p.Rules {
		if r.Deny || !r.namesAny(refs) {
			continue
		}
		named = true
		if r.covers(allOfKind) {
			admitsAll = true
		}
		if r.coversAnyOfKind(kind) {
			admitsSome = true
		}
	}
	if named {
		if !admitsSome {
			return CapabilityPolicyDenied
		}
		if !admitsAll {
			return CapabilityPolicyMayBeRefused
		}
	}
	if anyDeny {
		return CapabilityPolicyMayBeRefused
	}
	return CapabilityPolicyAllowed
}

// Verdicts reports VerdictForKind across CapabilityRunTask,
// CapabilityRunProcess and CapabilityRunCI, omitting kinds the policy
// admits unconditionally. Returns nil when every kind is admitted.
func (p CapabilityPolicy) Verdicts(ref string, servers ...map[string]protocol.ServerConfig) map[string]CapabilityPolicyVerdict {
	var out map[string]CapabilityPolicyVerdict
	for _, kind := range []string{CapabilityRunTask, CapabilityRunProcess, CapabilityRunCI} {
		if v := p.VerdictForKind(ref, kind, servers...); v != CapabilityPolicyAllowed {
			if out == nil {
				out = make(map[string]CapabilityPolicyVerdict)
			}
			out[kind] = v
		}
	}
	return out
}

func (r CapabilityPolicyRule) coversAnyOfKind(kind string) bool {
	if len(r.RunKinds) > 0 && !containsString(r.RunKinds, kind) {
		return false
	}
	if kind != CapabilityRunCI && len(r.Events) > 0 && !containsString(r.Events, "") {
		return false
	}
	return true
}

func (r CapabilityPolicyRule) names(ref string) bool {
	ok, _ := path.Match(r.Capability, ref)
	return ok || r.Capability == ref
}

func (r CapabilityPolicyRule) namesAny(refs []string) bool {
	for _, ref := range refs {
		if r.names(ref) {
			return true
		}
	}
	return false
}

func equivalentCapabilityRefs(ref string, serverMaps []map[string]protocol.ServerConfig) []string {
	refs := []string{ref}
	id, method, hasMethod := strings.Cut(ref, "#")
	suffix := ""
	if hasMethod {
		suffix = "#" + method
	}
	add := func(otherID string) {
		cand := otherID + suffix
		for _, existing := range refs {
			if existing == cand {
				return
			}
		}
		refs = append(refs, cand)
	}
	for _, servers := range serverMaps {
		for entryID, cfg := range servers {
			if entryID == id {
				for _, a := range cfg.Aliases {
					if _, taken := servers[a]; !taken {
						add(a)
					}
				}
			}
			if _, taken := servers[id]; !taken {
				for _, a := range cfg.Aliases {
					if a == id {
						add(entryID)
						for _, other := range cfg.Aliases {
							if _, otherTaken := servers[other]; !otherTaken {
								add(other)
							}
						}
					}
				}
			}
		}
	}
	return refs
}

func (r CapabilityPolicyRule) admits(s CapabilitySubject) bool {
	if s.Untrusted && !r.AllowUntrusted {
		return false
	}
	return r.covers(s)
}

// covers reports whether s is one of the runs r's Repos, RunKinds and
// Events describe, whatever its trust.
func (r CapabilityPolicyRule) covers(s CapabilitySubject) bool {
	if len(r.RunKinds) > 0 && !containsString(r.RunKinds, s.Kind) {
		return false
	}
	if len(r.Events) > 0 && !containsString(r.Events, s.Event) {
		return false
	}
	if len(r.Repos) > 0 {
		if s.Repo == nil {
			return false
		}
		name := strings.ToLower(s.Repo.Owner + "/" + s.Repo.Name)
		matched := false
		for _, pattern := range r.Repos {
			if ok, _ := path.Match(strings.ToLower(pattern), name); ok {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func (s CapabilitySubject) describe() string {
	out := s.Kind
	if out == "" {
		out = "run"
	}
	if s.Repo != nil {
		out += " on " + s.Repo.Owner + "/" + s.Repo.Name
	}
	if s.Event != "" {
		out += " (" + s.Event + ")"
	}
	return out
}

func containsString(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

// EncodeCapabilityPolicy and DecodeCapabilityPolicy are
// grain_config.capability_policy's text: "" for no rules, otherwise JSON.
func EncodeCapabilityPolicy(p CapabilityPolicy) (string, error) {
	if len(p.Rules) == 0 && p.Catalogue == nil {
		return "", nil
	}
	data, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("encoding capability_policy: %w", err)
	}
	return string(data), nil
}

func DecodeCapabilityPolicy(text string) (CapabilityPolicy, error) {
	var p CapabilityPolicy
	if strings.TrimSpace(text) == "" {
		return p, nil
	}
	if err := json.Unmarshal([]byte(text), &p); err != nil {
		return p, fmt.Errorf("decoding capability_policy: %w", err)
	}
	return p, nil
}

// recordedCapabilityGrant reads a recorded reference grant back as the
// protocol grant it records: false for one that is not in recorded form
// (no #method, so never normalized).
func recordedCapabilityGrant(s string) (protocol.Grant, bool) {
	spec, err := CapabilityGrantSpec(s)
	if err != nil {
		return protocol.Grant{}, false
	}
	ref, err := protocol.ParseRef(spec.Capability)
	if err != nil || ref.Method == "" {
		return protocol.Grant{}, false
	}
	args := spec.With
	if args == nil {
		args = map[string]any{}
	}
	return protocol.Grant{Ref: ref, Args: args}, true
}

// UncoveredCapabilityRefs is the reference grants of child that no
// reference grant of parent covers (protocol.Uncovered, §2.3.4): the
// parent's as recorded at its admission, the child's as normalized when
// it was filed. A child reference that was never normalized cannot be
// shown covered, so it is uncovered.
func UncoveredCapabilityRefs(child, parent []Grant) []string {
	var held []protocol.Grant
	for _, g := range parent {
		if pg, ok := recordedCapabilityGrant(g.Capability); ok {
			held = append(held, pg)
		}
	}
	var asked []protocol.Grant
	var out []string
	for _, g := range child {
		if !IsCapabilityRef(g.Capability) {
			continue
		}
		cg, ok := recordedCapabilityGrant(g.Capability)
		if !ok {
			out = append(out, g.Capability)
			continue
		}
		asked = append(asked, cg)
	}
	for _, u := range protocol.Uncovered(held, asked) {
		out = append(out, RecordedCapabilityGrant(u))
	}
	return out
}

// CapabilityEntries reads a capabilities: list as a tool call's JSON
// arguments decode it -- strings and {capability, with} objects -- as
// grant strings (CapabilityEntryNames).
func CapabilityEntries(v any) ([]string, error) {
	switch t := v.(type) {
	case nil:
		return nil, nil
	case []string:
		return t, nil
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			raw, err := json.Marshal(item)
			if err != nil {
				return nil, fmt.Errorf("capabilities: %w", err)
			}
			s, err := capabilityEntries(raw)
			if err != nil {
				return nil, fmt.Errorf("capabilities: %w", err)
			}
			out = append(out, s...)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("capabilities must be a list")
	}
}

// APIWithCapability is the capability name that takes the agent API's
// grants as arguments: {capability: api, with: {set: ops/triage}}.
const APIWithCapability = "api"

// APIWithGrantNames is the api grant names a {capability: api, with: ...}
// entry stands for. with carries set (a scope set's name, or a list of
// them), scopes (scopes of the vocabulary, or <area>:*) and optionally
// repo, which limits every one of them to that repository. Each name is
// one a person could have typed (api:ops/triage@owner/name), so
// admission, policy and the proposal gate see nothing new.
func APIWithGrantNames(with map[string]any) ([]string, error) {
	var names []string
	var repo string
	strs := func(key string, v any) ([]string, error) {
		switch t := v.(type) {
		case string:
			return []string{t}, nil
		case []any:
			out := make([]string, 0, len(t))
			for _, item := range t {
				s, ok := item.(string)
				if !ok {
					return nil, fmt.Errorf("api capability: with.%s must be a string or a list of strings", key)
				}
				out = append(out, s)
			}
			return out, nil
		}
		return nil, fmt.Errorf("api capability: with.%s must be a string or a list of strings", key)
	}
	if v, ok := with["repo"]; ok {
		s, isString := v.(string)
		if !isString {
			return nil, fmt.Errorf("api capability: with.repo must be one owner/name")
		}
		ref, err := ParseRepo(strings.TrimSpace(s))
		if err != nil {
			return nil, fmt.Errorf("api capability: with.repo: %v", err)
		}
		repo = "@" + ref.String()
	}
	for key, v := range with {
		switch key {
		case "repo":
		case "set", "sets":
			sets, err := strs(key, v)
			if err != nil {
				return nil, err
			}
			for _, set := range sets {
				set = strings.TrimSpace(set)
				if _, ok := APIScopeSetNamed(set); !ok {
					if _, isPreset := APIPresetNamed(set); !isPreset {
						return nil, fmt.Errorf("api capability: %q is no scope set (docs/agent-api.md)", set)
					}
				}
				names = append(names, APIGrantPrefix+set+repo)
			}
		case "scopes", "scope":
			patterns, err := strs(key, v)
			if err != nil {
				return nil, err
			}
			for _, p := range patterns {
				scopes, err := ExpandAPIScopePattern(p)
				if err != nil {
					return nil, fmt.Errorf("api capability: %w", err)
				}
				for _, sc := range scopes {
					names = append(names, APIGrantPrefix+string(sc)+repo)
				}
			}
		default:
			return nil, fmt.Errorf("api capability: unknown with.%s (set, scopes and repo are what it takes)", key)
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("api capability: with names no set and no scope")
	}
	sort.Strings(names)
	return slices.Compact(names), nil
}
