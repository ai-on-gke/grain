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
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
)

// The in-process release server and its three methods: the CI
// credentials the retired image-signing, registry-push and
// package-publish providers placed (schema migration 101).
const (
	ReleaseCapabilityServerID = "https://grain.dev/capabilities/release"
	ImageSigningRef           = ReleaseCapabilityServerID + "#image-signing"
	RegistryPushRef           = ReleaseCapabilityServerID + "#registry-push"
	PackagePublishRef         = ReleaseCapabilityServerID + "#package-publish"
)

// CIGrantRule is when a CI job may be handed a capability reference, as
// the in-process server that answers it declares (capability.Method.CI):
// what a retired provider's CapabilitySpec.CIOnly, SupportsCIEvent and
// SupportsTrustedSchedule decided, carried by the reference now that no
// provider stands behind it.
//
// It is grain's rule, not the deployment's. A rule is CI-only unless it
// sets TaskGrantable: no task or process run is ever granted it, whatever
// the capability policy says. That default is fail-closed on purpose --
// before TaskGrantable existed, declaring any rule at all made a method
// CI-only, so a rule written to that contract (in core or a layer built
// on it) stays CI-only rather than silently becoming a task grant. A rule
// that gates on CI Events or TrustedSchedule is CI-only even with
// TaskGrantable set, since a task has no event for the gate to admit.
// When Events or TrustedSchedule is set, it is handed to a CI job only
// on Events (and on the schedule run grain itself fired, when
// TrustedSchedule); a job of any other event is not handed it at all, as
// the provider's gate skipped it. CapabilityPolicy.Permits refuses it beyond the rule before
// any policy rule is read, so a policy can only narrow it further.
//
// RepoGranted is the provider's label rule: the repository's CI
// capabilities list, which an operator edits, is what grants it -- a
// job's own declaration only narrows what that list provides, as it
// narrows every other deployment grant -- rather than the declaration
// granting it, as it does other capability references.
//
// TaskGrantable marks a reference that may also be granted to tasks,
// templates, schedules and console sessions (such as github#sandbox-repo,
// whose retired provider had CIOnly false) -- unless the rule is
// event-gated, which keeps it CI-only (IsCIOnly).
type CIGrantRule struct {
	Events          []string
	TrustedSchedule bool
	RepoGranted     bool
	TaskGrantable   bool
}

// IsEventGated reports whether r restricts which CI trigger events may
// receive the grant: whenever it names Events or TrustedSchedule. A rule
// naming neither admits every event; there is no separate opt-out to
// forget.
func (r CIGrantRule) IsEventGated() bool {
	return len(r.Events) > 0 || r.TrustedSchedule
}

// IsCIOnly reports whether r restricts the grant to CI jobs alone: every
// rule that does not opt in with TaskGrantable, and every event-gated
// rule, because tasks have no CI trigger event.
func (r CIGrantRule) IsCIOnly() bool {
	return !r.TaskGrantable || r.IsEventGated()
}

// Admits reports whether a CI job of event, trustedSchedule being
// whether it is the schedule run grain fired from a workflow's cron
// (ci.Request.TrustedSchedule), is handed the grant.
func (r CIGrantRule) Admits(event string, trustedSchedule bool) bool {
	if !r.IsEventGated() {
		return true
	}
	if slices.Contains(r.Events, event) {
		return true
	}
	return event == "schedule" && trustedSchedule && r.TrustedSchedule
}

// describe is the rule's events in a sentence.
func (r CIGrantRule) describe() string {
	events := slices.Clone(r.Events)
	if r.TrustedSchedule {
		events = append(events, "grain's own scheduled run")
	}
	switch len(events) {
	case 0:
		return "no CI event"
	case 1:
		return events[0]
	}
	return strings.Join(events[:len(events)-1], ", ") + " or " + events[len(events)-1]
}

var (
	ciGrantRulesMu sync.RWMutex
	ciGrantRules   = map[string]CIGrantRule{
		// github#sandbox-repo (formerly github-sandbox) may be granted to
		// tasks and, for CI jobs, is granted by the repository's
		// ci_capabilities list on every event rather than by a job's own
		// declaration alone.
		GitHubSandboxRepoRef: {RepoGranted: true, TaskGrantable: true},
	}
)

// RegisterCIGrantRule declares rule for the capability reference ref
// (<server>#<method>) until the returned func is called. capability.Register
// calls it for a method that declares one; it panics on a reference that
// already has one.
func RegisterCIGrantRule(ref string, rule CIGrantRule) (unregister func()) {
	ref = strings.TrimSpace(ref)
	rule.Events = slices.Clone(rule.Events)
	ciGrantRulesMu.Lock()
	defer ciGrantRulesMu.Unlock()
	if _, ok := ciGrantRules[ref]; ok {
		panic(fmt.Sprintf("model: RegisterCIGrantRule(%q): already declared", ref))
	}
	ciGrantRules[ref] = rule
	return func() {
		ciGrantRulesMu.Lock()
		defer ciGrantRulesMu.Unlock()
		delete(ciGrantRules, ref)
	}
}

// ciGrantRuleLookup is the declared reference and rule grant names, in
// any spelling: recorded (<ref>={...}), bare (<ref>), with the server ID
// in another case, methodless shorthand on a server that declares a rule
// for exactly one method, or a retired name an alias rewrites to one.
func ciGrantRuleLookup(grant string) (string, CIGrantRule, bool) {
	trimmed := strings.TrimSpace(grant)
	c := CanonicalLegacyGrant(trimmed)
	if c == trimmed && !IsCapabilityRef(c) {
		c = CanonicalLegacyGrant(strings.ToLower(trimmed))
	}
	if !IsCapabilityRef(c) {
		return "", CIGrantRule{}, false
	}
	ref := CapabilityGrantRef(c)
	ciGrantRulesMu.RLock()
	defer ciGrantRulesMu.RUnlock()
	if rule, ok := ciGrantRules[ref]; ok {
		return ref, rule, true
	}
	if serverID, method, hasFrag := strings.Cut(ref, "#"); hasFrag {
		// Server IDs are matched without regard to case (as
		// capability policy and catalogue rules match them); method
		// names are not.
		for k, rule := range ciGrantRules {
			kServer, kMethod, ok := strings.Cut(k, "#")
			if ok && strings.EqualFold(kServer, serverID) && kMethod == method {
				return k, rule, true
			}
		}
		return "", CIGrantRule{}, false
	}
	// Methodless shorthand names the server's only method -- binding
	// only when the server declares a rule for exactly one.
	var matchedRef string
	var matched CIGrantRule
	count := 0
	for k, rule := range ciGrantRules {
		kServer, _, ok := strings.Cut(k, "#")
		if ok && strings.EqualFold(kServer, ref) {
			matchedRef, matched = k, rule
			count++
		}
	}
	if count == 1 {
		return matchedRef, matched, true
	}
	return "", CIGrantRule{}, false
}

// CIGrantRuleFor is the rule declared for the reference grant names, in
// any spelling: recorded, bare reference, the server ID in another case,
// methodless shorthand on a server with one rule-bearing method, or a
// retired name an alias rewrites to one (CanonicalLegacyGrant). False
// when none is. Binding every spelling is what keeps a case-varied or
// shorthand grant from slipping past CIGrantRuleRefusal,
// CapabilityPolicy.Permits and IsRepoGrantedCIGrant.
func CIGrantRuleFor(grant string) (CIGrantRule, bool) {
	_, rule, ok := ciGrantRuleLookup(grant)
	return rule, ok
}

// CanonicalCIGrant normalizes a CI capability spelling for matching a
// workflow's declaration or a stored grant against the repository's CI
// capabilities list: retired bare aliases are rewritten by
// CanonicalLegacyGrant, and a bare, case-varied or methodless reference
// to a method with a declared CIGrantRule and no arguments becomes its
// recorded <ref>={}. Any other grant is returned as CanonicalLegacyGrant
// leaves it.
func CanonicalCIGrant(grant string) string {
	c := CanonicalLegacyGrant(strings.TrimSpace(grant))
	ref, _, ok := ciGrantRuleLookup(c)
	if !ok {
		return c
	}
	_, args, hasArgs := strings.Cut(c, "=")
	if !hasArgs || strings.TrimSpace(args) == "{}" {
		return ref + "={}"
	}
	return c
}

// IsRepoGrantedCIGrant reports whether grant is a capability reference a
// repository's CI capabilities list grants (CIGrantRule.RepoGranted)
// rather than a job's declaration.
func IsRepoGrantedCIGrant(grant string) bool {
	rule, ok := CIGrantRuleFor(grant)
	return ok && rule.RepoGranted
}

// CIGrantRuleRefusal is why grain's own rule refuses grant to s, or ""
// when it admits it or declares no rule for it.
func CIGrantRuleRefusal(grant string, s CapabilitySubject) string {
	rule, ok := CIGrantRuleFor(grant)
	if !ok {
		return ""
	}
	if s.Kind != CapabilityRunCI {
		if !rule.IsCIOnly() {
			return ""
		}
		return CIGrantRuleCIOnlyRefusal(grant, rule)
	}
	if !s.AnyEvent && !rule.Admits(s.Event, s.TrustedSchedule) {
		run := "a run with no trigger event"
		if s.Event != "" {
			run = fmt.Sprintf("a %s run", s.Event)
		}
		return fmt.Sprintf("%s is handed only to a CI job of %s, and this is %s: that rule is grain's own, "+
			"declared by the capability's server, and no capability policy rule can widen it",
			CapabilityGrantRef(CanonicalLegacyGrant(grant)), rule.describe(), run)
	}
	return ""
}

// CIGrantRuleCIOnlyRefusal is the refusal of grant, whose rule is rule,
// to a task or process run.
func CIGrantRuleCIOnlyRefusal(grant string, rule CIGrantRule) string {
	return fmt.Sprintf("%s is a CI capability, not a task grant: it is handed only to a CI job of %s, "+
		"and a task run is not one. Take it off this task, and enable it for the repository's CI jobs "+
		"instead -- the repos page, under CI capabilities.", strings.TrimSpace(grant), rule.describe())
}

type ciMintKey struct{}

// CIMint is the CI job a method of an in-process capability server mints
// for: what a retired provider read off its CapabilityContext there.
// mintCIPass hands it in the mint's context (WithCIMint).
type CIMint struct {
	Repo        RepoRef
	RunID       string
	Credentials CredentialResolver
	// Event, TrustedSchedule and WithholdEventGated are the job's run's
	// (ci.Request), so a method can hold its own CIGrantRule rather than
	// trust that grain did.
	Event              string
	TrustedSchedule    bool
	WithholdEventGated bool
}

// WithCIMint is ctx carrying job to a method minting for it.
func WithCIMint(ctx context.Context, job CIMint) context.Context {
	return context.WithValue(ctx, ciMintKey{}, job)
}

// CIMintFrom is the CI job WithCIMint put in ctx, and false outside one.
func CIMintFrom(ctx context.Context) (CIMint, bool) {
	job, ok := ctx.Value(ciMintKey{}).(CIMint)
	return job, ok
}

// registered reports whether a provider in reg answers name.
func registered(reg *CapabilityRegistry, name string) bool {
	_, ok := reg.Lookup(name)
	return ok
}
