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

// Package policy is grain's local Policy Enforcement Point: the check a
// workstation's own grain makes of every task before it is given a
// sandbox, against a small policy its owner writes down
// (grain_policy.yml, named by the daemon's -policy flag).
//
// A task carries who asked for it (Task.Creator), who approved it
// (Task.Approval) and what it may use (Task.Grants, Task.NetworkAccess).
// This package only asks whether those claims fit what this machine's
// owner is willing to run: one of the repositories they attached, an
// approval when the request came from somebody else, and nothing their
// policy forbids.
//
// Check is stateless and fails closed. A task it refuses is held before
// any sandbox is built or any code is cloned; a policy file that cannot
// be read or parsed stops the daemon from starting rather than leaving
// it running with no policy at all.
package policy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ai-on-gke/grain/pkg/model"
)

// Policy is one workstation's local policy, as grain_policy.yml writes
// it down. The zero Policy allows no repository at all.
type Policy struct {
	// Repos are the repositories tasks on this machine may target, write
	// or read: owner/name (or host/owner/name), compared without regard
	// to case, where either segment may be a path.Match pattern
	// ("acme/*"). Empty refuses every task that names a repository.
	Repos []string `yaml:"repos"`

	// LocalRequesters are the requesters (Task.Creator) this machine
	// treats as its owner: their tasks need no approval beyond the one
	// that queued them. A task with no requester at all is grain's own
	// (a schedule, a suite, the local CLI) and is local too -- but only
	// one derived from no other task (model.Task.DerivedFrom): a
	// proposal, port, review or repair is judged as its root requester's
	// (Requester), never as grain's own for naming nobody.
	LocalRequesters []string `yaml:"local_requesters"`

	// AllowUnapprovedExternal turns the approval check off: by default a
	// task requested by anybody not in LocalRequesters must carry an
	// approval by a human other than its requester.
	AllowUnapprovedExternal bool `yaml:"allow_unapproved_external"`

	// AllowExternalOnHostSandboxes lets a task that is not this
	// machine's own (IsOwn) run on a daemon whose sandboxes are
	// plain directories on this host (-host-sandboxes). By default such a
	// task is refused there (CheckUnisolated): every one of those runs
	// executes as the owner, on the owner's machine, with the owner's
	// files, keys and logins within reach, so a request from anybody
	// else would be a request to act as the owner.
	AllowExternalOnHostSandboxes bool `yaml:"allow_external_on_host_sandboxes"`

	// Approvers, when set, are the only people whose approval of an
	// external request this machine accepts.
	Approvers []string `yaml:"approvers"`

	// Capabilities limits what a task may be granted (Task.Grants).
	Capabilities CapabilityRules `yaml:"capabilities"`

	// Network lists the network accesses (open, none) a task's sandbox
	// may have. Empty allows either.
	Network []model.NetworkAccess `yaml:"network"`
}

// CapabilityRules is Policy.Capabilities. Each entry is a capability
// pattern, compared without regard to case (capabilityMatch): a bare
// family name ("gcp-key") matches the family and every parameterised
// grant of it ("gcp-key:team-a"), "family:*" matches every parameterised
// grant of the family, and otherwise the family and the parameter are
// each path.Match patterns ("repo-write:acme/*"). A capability reference
// without "=" ("https://...#method") is a path.Match pattern over the
// method regardless of arguments; one with inline JSON arguments
// ("https://...#method={...}") matches only that exact grant.
type CapabilityRules struct {
	// Allow, when set, is every capability a task may be granted; a
	// grant of anything else refuses the task.
	Allow []string `yaml:"allow"`
	// Deny refuses a task granted any of these, whatever Allow says.
	Deny []string `yaml:"deny"`
}

// Load reads and validates the policy file at path. Unknown keys are an
// error, so a misspelt restriction is never silently dropped.
func Load(file string) (*Policy, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("policy: reading %s: %w", file, err)
	}
	p, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("policy: %s: %w", file, err)
	}
	return p, nil
}

// Parse reads and validates a policy from its YAML form.
func Parse(data []byte) (*Policy, error) {
	var p Policy
	// An empty file is the zero Policy (io.EOF), not an error.
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&p); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parsing: %w", err)
	}
	// A second document would be silently ignored, and with it whatever
	// restrictions it names, so a file holding more than one is refused.
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("parsing: a policy file holds one YAML document, and this one holds more")
	}
	if err := p.validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

func (p *Policy) validate() error {
	for _, r := range p.Repos {
		if _, _, err := splitRepoPattern(r); err != nil {
			return err
		}
	}
	for _, n := range p.Network {
		if !model.ValidNetworkAccess(n) {
			return fmt.Errorf("network: %q is not one of %s", n, model.NetworkAccessNames())
		}
	}
	for _, rule := range []struct {
		key  string
		list []string
	}{{"capabilities.allow", p.Capabilities.Allow}, {"capabilities.deny", p.Capabilities.Deny}} {
		for _, c := range rule.list {
			if err := validCapabilityPattern(c); err != nil {
				return fmt.Errorf("%s: %q %w", rule.key, c, err)
			}
		}
	}
	return nil
}

// lowerCapabilityRefPrefix lowercases the reference half of an https://
// capability grant (before any inline =<json> arguments) so capability
// references participate in pkg/policy's case-insensitive matching while
// keeping JSON key/value case intact.
func lowerCapabilityRefPrefix(s string) string {
	trimmed := strings.TrimSpace(s)
	if len(trimmed) < len("https://") || !strings.EqualFold(trimmed[:len("https://")], "https://") {
		return trimmed
	}
	ref, with, hasWith := strings.Cut(trimmed, "=")
	ref = strings.ToLower(ref)
	if !hasWith {
		return ref
	}
	return ref + "=" + with
}

// validCapabilityPattern reports why pattern cannot be a capabilities
// entry, or nil when it can.
func validCapabilityPattern(pattern string) error {
	norm := lowerCapabilityRefPrefix(pattern)
	if model.IsCapabilityRef(norm) {
		if strings.Contains(norm, "=") {
			if err := model.CheckCapabilityGrantArgsOffline(norm); err != nil {
				return fmt.Errorf("is not a valid capability grant: %w", err)
			}
			return nil
		}
		if _, err := path.Match(norm, ""); err != nil {
			return fmt.Errorf("is not a valid pattern: %w", err)
		}
		return nil
	}
	family, param, hasParam := strings.Cut(strings.TrimSpace(pattern), ":")
	if family == "" || (hasParam && param == "") {
		return errors.New("is not a capability or family:parameter")
	}
	if _, err := path.Match(family, ""); err != nil {
		return fmt.Errorf("is not a valid pattern: %w", err)
	}
	if _, err := path.Match(param, ""); err != nil {
		return fmt.Errorf("is not a valid pattern: %w", err)
	}
	return nil
}

// sameCapabilityRefGrant reports whether two capability references carry
// the same method (without regard to case) and identical JSON arguments
// (treating a bare reference and an empty {} object as the same).
func sameCapabilityRefGrant(a, b string) bool {
	aSpec, err := model.CapabilityGrantSpec(lowerCapabilityRefPrefix(a))
	if err != nil {
		return false
	}
	bSpec, err := model.CapabilityGrantSpec(lowerCapabilityRefPrefix(b))
	if err != nil {
		return false
	}
	if !strings.EqualFold(aSpec.Capability, bSpec.Capability) {
		return false
	}
	aWith := aSpec.With
	if aWith == nil {
		aWith = map[string]any{}
	}
	bWith := bSpec.With
	if bWith == nil {
		bWith = map[string]any{}
	}
	aJSON, err := json.Marshal(aWith)
	if err != nil {
		return false
	}
	bJSON, err := json.Marshal(bWith)
	if err != nil {
		return false
	}
	return bytes.Equal(aJSON, bJSON)
}

// capabilityMatch reports whether the capabilities entry pattern matches
// the granted capability, as CapabilityRules describes. A parameter can
// itself hold a slash (repo-write:owner/name), which path.Match's * does
// not cross, so a parameter pattern of * alone matches any parameter.
func capabilityMatch(pattern, capability string) bool {
	rawPattern := lowerCapabilityRefPrefix(pattern)
	rawCapability := lowerCapabilityRefPrefix(capability)
	// A capability reference pattern carrying inline =<json> arguments
	// matches only that exact grant (preserving JSON key/value case),
	// rather than every argument to the method.
	if model.IsCapabilityRef(rawPattern) && strings.Contains(rawPattern, "=") {
		return model.IsCapabilityRef(rawCapability) && sameCapabilityRefGrant(rawPattern, rawCapability)
	}
	pattern = strings.ToLower(rawPattern)
	capability = strings.ToLower(rawCapability)
	// A capability reference without "=" (https://...#method) matches the
	// method whole, whatever its arguments: its ":" is the scheme's, not a
	// family's parameter.
	if model.IsCapabilityRef(pattern) || model.IsCapabilityRef(capability) {
		ref := model.CapabilityGrantRef(capability)
		ok, err := path.Match(pattern, ref)
		return pattern == ref || (err == nil && ok)
	}
	pFamily, pParam, pHasParam := strings.Cut(pattern, ":")
	family, param, hasParam := strings.Cut(capability, ":")
	if ok, err := path.Match(pFamily, family); err != nil || !ok {
		return false
	}
	switch {
	case !pHasParam:
		return true
	case !hasParam:
		return false
	case pParam == "*":
		return true
	}
	ok, err := path.Match(pParam, param)
	return err == nil && ok
}

// matchesCapability reports whether any of patterns matches capability.
// Repository access and agent API grants are matched in both their
// spellings (model.RepoAccessSpellings, model.APIGrantSpellings): a
// policy file on this machine is not rewritten when store rows or writers
// move to canonical https://grain.dev/capabilities/{repo,api}#...
// references, nor are unmigrated rows or .grain/ declarations when a
// policy names the canonical reference.
func matchesCapability(patterns []string, capability string) bool {
	for _, spelling := range grantSpellings(capability) {
		for _, pattern := range patterns {
			if capabilityMatch(pattern, spelling) {
				return true
			}
		}
	}
	return false
}

func grantSpellings(capability string) []string {
	out := model.RepoAccessSpellings(capability)
	for _, s := range model.APIGrantSpellings(capability) {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// splitRepoPattern splits a repos entry into its host ("" for the
// default host) and its lower-cased owner/name pattern.
func splitRepoPattern(entry string) (host, ownerName string, err error) {
	entry = strings.TrimSpace(entry)
	parts := strings.Split(entry, "/")
	switch len(parts) {
	case 2:
	case 3:
		h, err := model.NormalizeGitHost(parts[0])
		if err != nil {
			return "", "", fmt.Errorf("repos: %q: %w", entry, err)
		}
		host = model.RepoRef{Host: h}.OffDefaultHost()
		parts = parts[1:]
	default:
		return "", "", fmt.Errorf("repos: %q is not owner/name or host/owner/name", entry)
	}
	if parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("repos: %q is not owner/name or host/owner/name", entry)
	}
	ownerName = strings.ToLower(parts[0] + "/" + parts[1])
	if _, err := path.Match(ownerName, ""); err != nil {
		return "", "", fmt.Errorf("repos: %q is not a valid pattern: %w", entry, err)
	}
	return host, ownerName, nil
}

// AllowsRepo reports whether repo is one of p.Repos.
func (p *Policy) AllowsRepo(repo model.RepoRef) bool {
	want := strings.ToLower(repo.Owner + "/" + repo.Name)
	for _, entry := range p.Repos {
		host, pattern, err := splitRepoPattern(entry)
		if err != nil || host != repo.OffDefaultHost() {
			continue
		}
		if ok, _ := path.Match(pattern, want); ok {
			return true
		}
	}
	return false
}

// Check is the whole of the enforcement: nil when task may run on this
// machine, and otherwise an error naming the first claim the policy
// refuses. defaultNetwork is the deployment's own network access, which
// a task asking for none of its own gets.
//
// It judges task's requester from task alone (RequesterOf); a caller that
// can resolve the requester at the root of a derived task's chain passes
// it to CheckAs instead.
func (p *Policy) Check(task model.Task, defaultNetwork model.NetworkAccess) error {
	return p.CheckAs(task, RequesterOf(task), defaultNetwork)
}

// CheckAs is Check judging task as requested by requester: its root
// requester, as the caller resolved it.
func (p *Policy) CheckAs(task model.Task, requester Requester, defaultNetwork model.NetworkAccess) error {
	if p == nil {
		return nil
	}
	if err := p.checkRepos(task); err != nil {
		return err
	}
	if err := p.checkApproval(task, requester); err != nil {
		return err
	}
	if err := p.checkCapabilities(task); err != nil {
		return err
	}
	return p.checkNetwork(task, defaultNetwork)
}

// CheckClaims is Check less the approval: nil when task's repositories,
// capabilities and network fit the policy. A task it refuses can never
// run here, whoever approves it.
func (p *Policy) CheckClaims(task model.Task, defaultNetwork model.NetworkAccess) error {
	if p == nil {
		return nil
	}
	if err := p.checkRepos(task); err != nil {
		return err
	}
	if err := p.checkCapabilities(task); err != nil {
		return err
	}
	return p.checkNetwork(task, defaultNetwork)
}

func (p *Policy) checkRepos(task model.Task) error {
	var repos []model.RepoRef
	if task.Target != nil && task.Target.Owner != "" {
		repos = append(repos, *task.Target)
	}
	for _, w := range task.Writes {
		repos = append(repos, w.Repo)
	}
	repos = append(repos, task.Reads...)
	// A grant can reach a repository the task never names: repo-read:
	// and repo-write: grants. Those are repositories this machine would
	// run against too, so they answer to repos alike. (grain#source,
	// once self-debug, reaches none: its grain-src/ is the deployment's
	// baked-in source.)
	for _, g := range task.Grants {
		repos = append(repos, model.CapabilityRepos(g.Capability)...)
	}
	for _, r := range repos {
		if !p.AllowsRepo(r) {
			return fmt.Errorf("local policy does not allow repository %s on this machine", r)
		}
	}
	return nil
}

// Requester is who local policy judges a task as asked for by: the
// requester at the root of the chain of tasks it was derived from
// (model.Task.DerivedFrom) -- a proposal, port, review or repair is its
// root requester's work, whatever filed it.
type Requester struct {
	// Name is the root requester: the first Creator met walking from the
	// task up through the tasks it was derived from. "" is grain's own
	// (a schedule, a suite, the local CLI), and only ever after a walk
	// that reached a task derived from nothing.
	Name string
	// Unresolved is set when the chain names a task that cannot be read,
	// so the root requester is not known: such a task is judged as an
	// external request, never as grain's own.
	Unresolved bool
}

// RequesterOf is task's Requester read from task alone, for a caller
// with no store to walk its chain with. A derived task naming no Creator
// of its own is Unresolved: its root is not known from here, and an
// empty requester is grain's own only on a task derived from nothing.
func RequesterOf(task model.Task) Requester {
	r := Requester{Name: strings.TrimSpace(task.Creator)}
	if r.Name == "" && task.DerivedFrom() != "" {
		r.Unresolved = true
	}
	return r
}

// IsLocal reports whether r is this machine's own: grain itself (a root
// requester of "", resolved) or one of LocalRequesters. A requester
// whose chain cannot be resolved is never local.
func (p *Policy) IsLocal(r Requester) bool {
	if r.Unresolved {
		return false
	}
	name := strings.TrimSpace(r.Name)
	return name == "" || containsFold(p.LocalRequesters, name)
}

// IsLocalRequester reports whether requester is one of LocalRequesters.
// An empty requester is not: whether a task naming none is grain's own
// depends on what it was derived from (IsLocal).
func (p *Policy) IsLocalRequester(requester string) bool {
	requester = strings.TrimSpace(requester)
	return requester != "" && containsFold(p.LocalRequesters, requester)
}

// IsOwn reports whether r is this machine's own, for the host-sandbox
// rule (CheckUnisolated): IsLocal, or with a nil Policy -- a daemon with
// no -policy, which has no other notion of whom it belongs to -- every
// requester but one whose chain cannot be resolved.
func (p *Policy) IsOwn(r Requester) bool {
	if p == nil {
		return !r.Unresolved
	}
	return p.IsLocal(r)
}

// CheckUnisolated is the rule for a daemon whose sandboxes isolate
// nothing from this host (-host-sandboxes): nil when a task may run
// there. local is whether the task's root requester is this machine's
// own (IsOwn). Anything else is refused unless
// AllowExternalOnHostSandboxes says otherwise -- including on a daemon
// with no -policy at all, so a nil Policy refuses too.
func (p *Policy) CheckUnisolated(local bool) error {
	if local || (p != nil && p.AllowExternalOnHostSandboxes) {
		return nil
	}
	return errors.New("this machine runs tasks in unisolated host sandboxes (-host-sandboxes), as its owner, " +
		"and this task is not the owner's own (it was requested by somebody outside " +
		"local_requesters); local policy runs only the owner's tasks there unless allow_external_on_host_sandboxes is set")
}

func (p *Policy) checkApproval(task model.Task, requester Requester) error {
	// Work derived from an external request is judged the same way as its
	// root requester's: it needs an approval of its own, since whatever
	// approved its source did not approve it.
	if p.IsLocal(requester) {
		return nil
	}
	who := strings.TrimSpace(requester.Name)
	if who == "" {
		who = strings.TrimSpace(task.Creator)
	}
	if who == "" {
		who = "an unknown requester"
	}
	if derived := task.DerivedFrom(); derived != "" {
		who += " (through task " + derived + ")"
	}
	if p.AllowUnapprovedExternal {
		return nil
	}
	a := task.Approval
	if a == nil || a.Actor.Kind != model.PrincipalHuman || strings.TrimSpace(a.Actor.ID) == "" {
		return fmt.Errorf("task requested by %s carries no human approval, which local policy requires of an external request", who)
	}
	approver := strings.TrimSpace(a.Actor.ID)
	for _, own := range []string{task.Creator, requester.Name} {
		if own = strings.TrimSpace(own); own != "" && strings.EqualFold(approver, own) {
			return fmt.Errorf("task requested by %s is approved only by its own requester, which local policy does not accept", who)
		}
	}
	if len(p.Approvers) > 0 && !containsFold(p.Approvers, approver) {
		return fmt.Errorf("task requested by %s is approved by %s, who is not one of this machine's approvers", who, approver)
	}
	return nil
}

// retiredGrants is what a retired capability name became
// (model.CanonicalLegacyGrants), compared without regard to case like
// every other pattern, and nil for any name that is not retired.
func retiredGrants(name string) []string {
	name = strings.ToLower(strings.TrimSpace(name))
	tos := model.CanonicalLegacyGrants(name)
	if len(tos) == 1 && strings.TrimSpace(tos[0]) == name {
		return nil
	}
	out := make([]string, len(tos))
	for i, to := range tos {
		out[i] = strings.TrimSpace(to)
	}
	return out
}

// retiredAs is the single grant a 1-to-1 retired capability name became,
// compared without regard to case, and false for any other name.
func retiredAs(name string) (string, bool) {
	if tos := retiredGrants(name); len(tos) == 1 {
		return tos[0], true
	}
	return "", false
}

// retiredAsSeveral is the grants a retired bare name that became more
// than one became (model.CanonicalLegacyGrants: self-debug,
// deployment-health), compared without regard to case, and nil for any
// other name.
func retiredAsSeveral(name string) []string {
	if tos := retiredGrants(name); len(tos) > 1 {
		return tos
	}
	return nil
}

// capabilityRefAliasVariants returns capability along with its built-in
// config-map/skill server-ID alias counterpart when applicable.
func capabilityRefAliasVariants(capability string) []string {
	c := lowerCapabilityRefPrefix(capability)
	if !model.IsCapabilityRef(c) {
		for _, s := range model.APIGrantSpellings(capability) {
			if model.IsCapabilityRef(s) {
				return []string{capability, lowerCapabilityRefPrefix(s)}
			}
		}
		return []string{capability}
	}
	ref, with, hasWith := strings.Cut(c, "=")
	id, method, hasHash := strings.Cut(ref, "#")
	suffix := ""
	if hasHash {
		suffix = "#" + method
	}
	args := ""
	if hasWith {
		args = "=" + with
	}
	switch id {
	case model.SkillCapabilityServerID:
		return []string{c, model.ConfigMapCapabilityServerID + suffix + args}
	case model.ConfigMapCapabilityServerID:
		return []string{c, model.SkillCapabilityServerID + suffix + args}
	default:
		return []string{c}
	}
}

// deniesCapabilityRefVariant checks whether pattern denies capabilityRef
// directly or via an unnormalized methodless shorthand (https://... without
// #<method>, where the server resolves the omitted method).
func deniesCapabilityRefVariant(pattern, capabilityRef string) bool {
	if capabilityMatch(pattern, capabilityRef) {
		return true
	}
	c := lowerCapabilityRefPrefix(capabilityRef)
	p := lowerCapabilityRefPrefix(pattern)
	if !model.IsCapabilityRef(c) || !model.IsCapabilityRef(p) {
		return false
	}
	cRef, cWith, cHasWith := strings.Cut(c, "=")
	if strings.Contains(cRef, "#") {
		return false
	}
	pRef := model.CapabilityGrantRef(p)
	pID, pMethod, pHasHash := strings.Cut(pRef, "#")
	if !pHasHash || pMethod == "" {
		return false
	}
	if !strings.Contains(p, "=") {
		ok, err := path.Match(pID, cRef)
		return pID == cRef || (err == nil && ok)
	}
	if strings.ContainsAny(pMethod, "*?[") {
		return false
	}
	synth := cRef + "#" + pMethod
	if cHasWith {
		synth += "=" + cWith
	}
	return capabilityMatch(p, synth)
}

// deniesCapability is matchesCapability for Deny, which also reads a
// retired bare name, on either side, as the grant it became: a policy
// file on this machine is not rewritten by the schema migration that
// rewrote the task's grants (git-push-any-ref became repo#write), and a
// deny it names must not stop applying when that happens. The retired
// name denies the whole method it became, whatever the arguments.
func deniesCapability(patterns []string, capability string) bool {
	capability = lowerCapabilityRefPrefix(capability)
	if matchesCapability(patterns, capability) {
		return true
	}
	// A retired name that became several grants (self-debug became
	// api#self-debug and grain#source) is each of them, so a deny of any
	// one, in its canonical spelling, refuses it.
	if tos := retiredAsSeveral(capability); len(tos) > 0 &&
		slices.ContainsFunc(tos, func(to string) bool { return deniesCapability(patterns, to) }) {
		return true
	}
	if set, ok := model.ParseGCPKeyServerGrant(capability); ok {
		legacy := "gcp-key:" + set
		if matchesCapability(patterns, legacy) {
			return true
		}
		if set == model.GCPKeyDefaultPermissionSet && matchesCapability(patterns, "gcp-key") {
			return true
		}
	}
	if (model.IsBootstrapPlaybooksGrant(capability) || model.BootstrapSkillSetUnnamed(capability)) &&
		(matchesCapability(patterns, "bootstrap-playbooks") || matchesCapability(patterns, model.BootstrapPlaybooksGrant)) {
		return true
	}
	if cred, ok := model.ParseRepoCredentialGrant(capability); ok {
		if matchesCapability(patterns, model.GitCredentialCapabilityPrefix+cred) {
			return true
		}
	}
	unnamedGCPKey := model.GCPKeySetUnnamed(capability)
	unnamedRepoCred := repoCredentialUnnamed(capability)
	if to, ok := retiredAs(capability); ok {
		capability = to
	}
	variants := capabilityRefAliasVariants(capability)
	for _, pattern := range patterns {
		trimmedPattern := strings.ToLower(strings.TrimSpace(pattern))
		if strings.HasPrefix(trimmedPattern, "gcp-key:") {
			// The server, not the policy, picks the set of a grant that
			// names none, so any gcp-key:<set> deny may be the one it picks.
			if unnamedGCPKey {
				return true
			}
			if to, ok := retiredAs(trimmedPattern); ok && strings.EqualFold(to, strings.TrimSpace(capability)) {
				return true
			}
			continue
		}
		if strings.HasPrefix(trimmedPattern, model.GitCredentialCapabilityPrefix) {
			if unnamedRepoCred {
				return true
			}
			if to, ok := retiredAs(trimmedPattern); ok && strings.EqualFold(to, strings.TrimSpace(capability)) {
				return true
			}
			continue
		}
		if to, ok := retiredAs(pattern); ok && model.IsCapabilityRef(to) {
			pattern = model.CapabilityGrantRef(to)
		}
		for _, v := range variants {
			if deniesCapabilityRefVariant(pattern, v) {
				return true
			}
		}
	}
	// A glob that matched a retired name (deny: [git-*]) matched it
	// before it was rewritten, so it still denies what it became.
	for _, old := range model.RetiredGrantNames() {
		if !matchesCapability(patterns, old) {
			continue
		}
		for _, to := range model.CanonicalLegacyGrants(old) {
			for _, v := range variants {
				if becameCapability(to, v) {
					return true
				}
				// Methodless shorthand on the server to became may resolve to to's method.
				if model.IsCapabilityRef(to) && model.IsCapabilityRef(v) && !strings.Contains(model.CapabilityGrantRef(v), "#") {
					toID, _, _ := strings.Cut(model.CapabilityGrantRef(to), "#")
					if strings.EqualFold(toID, model.CapabilityGrantRef(v)) {
						return true
					}
				}
			}
		}
	}
	return false
}

// becameCapability reports whether capability is to, the grant a retired
// name became: the same method for a capability reference, whatever its
// arguments, and the same grant otherwise.
func becameCapability(to, capability string) bool {
	if model.IsCapabilityRef(to) {
		return model.IsCapabilityRef(capability) &&
			strings.EqualFold(model.CapabilityGrantRef(to), model.CapabilityGrantRef(strings.TrimSpace(capability)))
	}
	return strings.EqualFold(strings.TrimSpace(to), strings.TrimSpace(capability))
}

// repoCredentialUnnamed reports whether capability is a repo#credential or
// methodless repo server reference that does not name a valid credential
// argument yet.
func repoCredentialUnnamed(capability string) bool {
	s := strings.TrimSpace(capability)
	if !model.IsCapabilityRef(s) {
		return false
	}
	if _, ok := model.ParseRepoCredentialGrant(s); ok {
		return false
	}
	ref := strings.ToLower(model.CapabilityGrantRef(s))
	return ref == strings.ToLower(model.RepoCredentialRef) || ref == strings.ToLower(model.RepoCapabilityServerID)
}

// allowsCapability is matchesCapability for Allow, which also admits
// exactly the grant (or each of the grants) a retired bare name in it
// became -- arguments and all, so an allow written for git-push-any-ref admits its repo#write
// grant and no other repo#write.
func allowsCapability(patterns []string, capability string) bool {
	if matchesCapability(patterns, capability) {
		return true
	}
	want := lowerCapabilityRefPrefix(capability)
	if set, ok := model.ParseGCPKeyServerGrant(want); ok {
		if set == model.GCPKeyDefaultPermissionSet {
			if matchesCapability(patterns, "gcp-key") {
				return true
			}
			if !model.GCPKeySetUnnamed(want) && matchesCapability(patterns, "gcp-key:"+set) {
				return true
			}
		} else if matchesCapability(patterns, "gcp-key:"+set) {
			return true
		}
	}
	if cred, ok := model.ParseRepoCredentialGrant(capability); ok {
		if matchesCapability(patterns, model.GitCredentialCapabilityPrefix+cred) {
			return true
		}
	}
	if to, ok := retiredAs(want); ok {
		want = to
		if matchesCapability(patterns, want) {
			return true
		}
	}
	// A retired name that became several grants is admitted when each of
	// them is, so a policy naming them canonically admits a task still
	// granted the retired name, exactly as one naming the retired name
	// does.
	if tos := retiredAsSeveral(want); len(tos) > 0 &&
		!slices.ContainsFunc(tos, func(to string) bool { return !allowsCapability(patterns, to) }) {
		return true
	}
	for _, pattern := range patterns {
		// A retired name in allow (1-to-1 such as memory -> api#memory, or
		// 1-to-many such as self-debug -> api#self-debug + grain#source)
		// admits each grant it became across both .../api# and legacy api:
		// spellings, with or without empty {}, while keeping JSON argument
		// value case exact.
		for _, to := range retiredGrants(pattern) {
			if sameAllowedGrant(to, want) {
				return true
			}
		}
	}
	return false
}

func sameAllowedGrant(to, capability string) bool {
	for _, s := range grantSpellings(capability) {
		w := lowerCapabilityRefPrefix(s)
		if model.IsCapabilityRef(to) {
			if model.IsCapabilityRef(w) && sameCapabilityRefGrant(to, w) {
				return true
			}
			continue
		}
		if strings.EqualFold(to, w) {
			return true
		}
	}
	return false
}

func (p *Policy) checkCapabilities(task model.Task) error {
	for _, g := range task.Grants {
		norm := lowerCapabilityRefPrefix(g.Capability)
		if model.IsCapabilityRef(norm) {
			if err := model.CheckCapabilityGrantArgsOffline(norm); err != nil {
				return err
			}
		}
		if deniesCapability(p.Capabilities.Deny, g.Capability) {
			return fmt.Errorf("local policy forbids capability %q", g.Capability)
		}
		if len(p.Capabilities.Allow) > 0 && !allowsCapability(p.Capabilities.Allow, g.Capability) {
			return fmt.Errorf("local policy does not allow capability %q", g.Capability)
		}
	}
	return nil
}

func (p *Policy) checkNetwork(task model.Task, defaultNetwork model.NetworkAccess) error {
	if len(p.Network) == 0 {
		return nil
	}
	access := task.NetworkAccess
	if access == "" {
		access = model.NormalizeNetworkAccess(defaultNetwork)
	}
	for _, n := range p.Network {
		if n == access {
			return nil
		}
	}
	return fmt.Errorf("local policy does not allow network access %q", access)
}

func containsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(strings.TrimSpace(v), s) {
			return true
		}
	}
	return false
}
