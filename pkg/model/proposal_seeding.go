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
	"fmt"
	"slices"
	"strings"
)

// ProposalSeeding is where a proposal's GrantByDefault grants came from
// when it was filed: the default capabilities resolved for its target,
// layer by layer, and the catalogue that judged them. It is the seeding
// source a proposal's defaults are measured against
// (ProposalEscalations; design/spec/director/director-capabilities.spec.md
// §4.2 for the director, docs/capabilities.md for every proposer).
//
// Both filing paths build it from the same reads they seed the proposal
// from -- ui.Client.CreateTask for a live propose_task call and
// orchestrator.relayProposedTasks for one replayed at the end of a run --
// so a default is admitted only as it was written down at filing time.
type ProposalSeeding struct {
	// Target is the repository whose default_capabilities RepoDefaults
	// was read from: the proposal's own Target when it was filed. A
	// proposal whose Target is not this repository admits none of
	// RepoDefaults.
	Target *RepoRef
	// RepoDefaults is Target's default_capabilities as resolved at
	// filing time, in the form the filing path records grants in.
	RepoDefaults []string
	// DeploymentDefaults is Config.DefaultCapabilities as resolved at
	// filing time, in the same form.
	DeploymentDefaults []string
	// Catalogue is the deployment's capability catalogue
	// (settings/capabilities.yml); nil admits everything, as
	// CapabilityCatalogue.ValidateGrants does.
	Catalogue *CapabilityCatalogue
	// GrantContext is the context the proposal is judged in by
	// Catalogue: its repository, template and kind. Grants is ignored,
	// and Repo is always the proposal's own Target.
	GrantContext GrantContext
}

// SeededDefault is one grant a proposal holds that ProposalEscalations
// does not count as an escalation, and where it was seeded from.
type SeededDefault struct {
	// Capability is the grant as EscalatedCapabilities names it.
	Capability string
	// Repo is the repository whose default_capabilities seeded it; nil
	// for the deployment's own Config.DefaultCapabilities.
	Repo *RepoRef
}

// IsDirectorTask reports whether t is the director's own standing
// session task -- one grain filed as the director, not one merely
// carrying its tag (CatalogueKindOf).
func IsDirectorTask(t Task) bool {
	return CatalogueKindOf(t) == CatalogueKindDirector
}

// ProposalEscalations is EscalatedCapabilities for a proposal filed with
// seeding as its seeding source, less the defaults every task filed
// there gets anyway, which it returns as admitted.
//
// A proposer holds whatever defaults its own target repository and
// template gave it, which need not be the proposal's: a task filed from a
// template with its own grant list holds none of the deployment's
// default_capabilities, and the director is no_repo and holds no
// repository's. A proposal filed against a repository whose defaults --
// or the deployment's -- include a capability reference would otherwise
// be held as a "seeded" escalation (SeededEscalations), though a task a
// person files there gets the same grant without a prompt. The defaults
// are a person's standing decision about every task filed there, and
// filing one more task does not widen that decision
// (director-capabilities.spec.md §4.3): such a grant gives the proposal
// nothing a person-filed task would not have.
//
// A grant is admitted only when every one of these holds:
//
//   - it is on child only as GrantByDefault;
//   - it is, unchanged in its with: arguments, one of seeding's
//     DeploymentDefaults, or one of its RepoDefaults where seeding.Target
//     is child's own Target -- the defaults as they stand when the
//     proposal is filed, so one a person has since changed is not;
//   - it is not person-only (IsPersonOnlyGrant);
//   - seeding's Catalogue neither refuses it nor marks it requires_person
//     for child's repository, template and kind.
//
// Everything else EscalatedCapabilities reports stays in escalated: a
// grant the proposer or the proposal named (GrantByLabel), a default
// whose arguments differ from the current default, a default of a
// repository other than child's target, and a person-only or
// requires_person grant. Reads and writes beyond the target are not
// capabilities at all, and stay held by UngrantedReads and
// UngrantedWrites. A nil seeding admits nothing.
//
// A default a person removed from the proposer is not admitted where the
// model records the removal: a standing console session other than the
// director (CatalogueKindSession) is created holding the deployment's
// defaults (ConsoleSession.Capabilities), so one of them it does not
// hold is one an operator took off it, and a proposal of the session's
// carrying it stays held. An ordinary task records no removal --
// detaching a grant deletes it, as a template's own grant list replaces
// the defaults -- so a default detached from one cannot be told apart
// from one it never had, and is admitted: the proposal is a new task
// filed where every task gets that default.
//
// Admitted grants stay on the proposal; WithoutSeededDefaults is the
// proposal the permission checks (AutoSubmits) measure instead.
func ProposalEscalations(child, parent Task, seeding *ProposalSeeding) (escalated []string, admitted []SeededDefault) {
	all := EscalatedCapabilities(child, parent)
	if seeding == nil {
		return all, nil
	}
	for _, c := range all {
		if d, ok := seeding.admits(child, parent, c); ok {
			admitted = append(admitted, d)
			continue
		}
		escalated = append(escalated, c)
	}
	return escalated, admitted
}

// WithoutSeededDefaults is child less the GrantByDefault grants admitted
// names (ProposalEscalations): the proposal as the permission checks that
// compare its grants with its proposer's (AutoSubmits, ScratchFixture,
// the proposal's own auto-merge) are to see it, since a default every
// task filed there gets is not one the proposer passes on. The proposal
// itself is filed holding them.
func WithoutSeededDefaults(child Task, admitted []SeededDefault) Task {
	if len(admitted) == 0 {
		return child
	}
	child.Grants = slices.DeleteFunc(slices.Clone(child.Grants), func(g Grant) bool {
		if g.Via != GrantByDefault {
			return false
		}
		return slices.ContainsFunc(admitted, func(d SeededDefault) bool { return grantIsCapability(g, d.Capability) })
	})
	return child
}

// admits is ProposalEscalations' rule for one escalated capability c of
// child, proposed by parent: the seeded default it is, when the rule
// admits it.
func (s *ProposalSeeding) admits(child, parent Task, c string) (SeededDefault, bool) {
	// A capability reference that was never normalized into recorded form
	// (no #method) cannot have its method-level person-only rules
	// (protocol.RequirePersonGrant, SetGrantPolicy) or normalized with:
	// arguments checked, so it stays uncovered (§2.3.4).
	if IsCapabilityRef(c) {
		if _, ok := recordedCapabilityGrant(c); !ok {
			return SeededDefault{}, false
		}
	}
	if !isSeededGrant(child.Grants, c) || IsPersonOnlyGrant(c) {
		return SeededDefault{}, false
	}
	var d SeededDefault
	switch {
	case seededFrom(s.DeploymentDefaults, c):
		// A standing session was seeded with the deployment's defaults,
		// so lacking this one it had it removed (ProposalEscalations).
		if CatalogueKindOf(parent) == CatalogueKindSession {
			return SeededDefault{}, false
		}
	case s.Target != nil && child.Target != nil && s.Target.Same(*child.Target) && seededFrom(s.RepoDefaults, c):
		repo := *child.Target
		d.Repo = &repo
	default:
		return SeededDefault{}, false
	}
	d.Capability = c
	gc := s.GrantContext
	gc.Repo = child.Target
	gc.Grants = []string{childGrantString(child.Grants, c)}
	verdict, err := s.Catalogue.ValidateGrants(gc)
	if err != nil || len(verdict.RequiresPerson) > 0 {
		return SeededDefault{}, false
	}
	return d, true
}

// seededFrom reports whether c -- as EscalatedCapabilities names it, which
// for a reference is its recorded form -- is one of defaults exactly,
// with: arguments and all once both are in recorded form.
func seededFrom(defaults []string, c string) bool {
	if strings.HasPrefix(c, APIGrantPrefix) || IsAPIGrantRef(c) {
		want := CanonicalGrantKey(c)
		for _, d := range defaults {
			if CanonicalGrantKey(strings.TrimSpace(d)) == want {
				return true
			}
		}
		return false
	}
	if IsCapabilityRef(c) {
		want, ok := recordedCapabilityGrant(c)
		if !ok {
			return false
		}
		wantRecorded := RecordedCapabilityGrant(want)
		for _, d := range defaults {
			if got, ok := recordedCapabilityGrant(strings.TrimSpace(d)); ok && RecordedCapabilityGrant(got) == wantRecorded {
				return true
			}
		}
		return false
	}
	for _, d := range defaults {
		if strings.TrimSpace(d) == c {
			return true
		}
	}
	return false
}

// canonicalGrantString is s in recorded form when it is a capability
// reference that parses (or CanonicalGrantKey for an api grant), and s as
// written otherwise.
func canonicalGrantString(s string) string {
	if strings.HasPrefix(s, APIGrantPrefix) || IsAPIGrantRef(s) {
		return CanonicalGrantKey(s)
	}
	if g, ok := recordedCapabilityGrant(s); ok {
		return RecordedCapabilityGrant(g)
	}
	return s
}

// childGrantString is the grant on grants that c names, as the task
// records it -- what the catalogue judges -- or c itself.
func childGrantString(grants []Grant, c string) string {
	want := canonicalGrantString(c)
	for _, g := range grants {
		if g.Capability == c || canonicalGrantString(g.Capability) == want {
			return g.Capability
		}
	}
	return c
}

// SeededDefaultsNote is the line appended to a proposal holding defaults
// ProposalEscalations admitted, in the voice of CapabilityEscalationNote:
// the row says which grants it received that its proposer does not hold,
// where each came from, and why they did not keep it from being queued
// (director-capabilities.spec.md §4.4 for the director). "" for none.
//
// Here rather than in either package that files a proposal, for the
// reason AutoSubmitLimitNote is: the end-of-run relay and the live call
// must say it the same way.
func SeededDefaultsNote(proposer Task, admitted []SeededDefault) string {
	if len(admitted) == 0 {
		return ""
	}
	var deployment []string
	var repoOrder []string
	byRepo := map[string][]string{}
	for _, d := range admitted {
		if d.Repo == nil {
			deployment = append(deployment, d.Capability)
			continue
		}
		name := d.Repo.String()
		if _, ok := byRepo[name]; !ok {
			repoOrder = append(repoOrder, name)
		}
		byRepo[name] = append(byRepo[name], d.Capability)
	}
	var parts []string
	for _, name := range repoOrder {
		parts = append(parts, fmt.Sprintf("%s from the default capabilities of `%s`",
			"`"+strings.Join(byRepo[name], "`, `")+"`", name))
	}
	if len(deployment) > 0 {
		parts = append(parts, fmt.Sprintf("%s from this deployment's default capabilities",
			"`"+strings.Join(deployment, "`, `")+"`"))
	}
	if IsDirectorTask(proposer) {
		return fmt.Sprintf("\n\n---\n\ngrain: this proposal holds %s, which task %s, the director, which "+
			"proposed it, does not hold itself. They were seeded unchanged when it was filed -- every task "+
			"filed there gets them, whoever files it -- and the director did not ask for them, so they are "+
			"not counted as asking for more than it holds (director-capabilities spec §4.2). Remove them "+
			"from the task if it should go ahead without them.", strings.Join(parts, "; and "), proposer.ID)
	}
	return fmt.Sprintf("\n\n---\n\ngrain: this proposal holds %s, which task %s, which proposed it, does "+
		"not hold itself. They were seeded unchanged when it was filed -- every task filed there gets them, "+
		"whoever files it -- and task %s did not ask for them, so they are not counted as asking for more "+
		"than it holds. Remove them from the task if it should go ahead without them.",
		strings.Join(parts, "; and "), proposer.ID, proposer.ID)
}
