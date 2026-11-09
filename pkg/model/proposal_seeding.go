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
	"strings"
)

// ProposalSeeding is where a proposal's GrantByDefault grants came from
// when it was filed: the default capabilities resolved for its target,
// layer by layer, and the catalogue that judged them. It is the seeding
// source design/spec/director/director-capabilities.spec.md §4.2 measures
// a director proposal's defaults against (ProposalEscalations).
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

// SeededDefault is one grant a director proposal holds that §4.2 does
// not count as an escalation, and where it was seeded from.
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
// seeding as its seeding source, less the defaults
// design/spec/director/director-capabilities.spec.md §4.2 admits, which
// it returns as admitted.
//
// The director is no_repo, so it holds no repository's
// default_capabilities. A proposal it files against a repository whose
// defaults include a capability reference would otherwise be held as a
// "seeded" escalation (SeededEscalations), though a task anybody files
// there gets the same grant: the repository's defaults are a person's
// standing decision about every task on it, and choosing which
// repository to file against does not widen that decision (§4.3).
//
// A grant is admitted only when every one of these holds:
//
//   - parent is the director (IsDirectorTask) and sessionQueues -- the
//     deployment lets it queue its own work (ConsoleSession.QueuesProposals);
//   - it is on child only as GrantByDefault;
//   - it is, unchanged in its with: arguments, one of seeding's
//     DeploymentDefaults, or one of its RepoDefaults where seeding.Target
//     is child's own Target;
//   - it is not person-only (IsPersonOnlyGrant);
//   - seeding's Catalogue neither refuses it nor marks it requires_person
//     for child's repository, template and kind.
//
// Everything else EscalatedCapabilities reports stays in escalated: a
// grant the director typed (GrantByLabel), a default whose arguments
// changed, a default of a repository other than child's target, and a
// person-only or requires_person grant. Reads and writes beyond the
// target are not capabilities at all, and stay held by UngrantedReads
// and UngrantedWrites. A nil seeding admits nothing.
func ProposalEscalations(child, parent Task, sessionQueues bool, seeding *ProposalSeeding) (escalated []string, admitted []SeededDefault) {
	all := EscalatedCapabilities(child, parent)
	if seeding == nil || !sessionQueues || !IsDirectorTask(parent) {
		return all, nil
	}
	for _, c := range all {
		if d, ok := seeding.admits(child, c); ok {
			admitted = append(admitted, d)
			continue
		}
		escalated = append(escalated, c)
	}
	return escalated, admitted
}

// admits is §4.2 for one escalated capability c of child: the seeded
// default it is, when the rule admits it.
func (s *ProposalSeeding) admits(child Task, c string) (SeededDefault, bool) {
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
// reference that parses, and s as written otherwise.
func canonicalGrantString(s string) string {
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

// SeededDefaultsNote is the line appended to a director proposal holding
// defaults ProposalEscalations admitted, in the voice of
// CapabilityEscalationNote: the row says which grants it received that
// its proposer does not hold, where each came from, and why they did not
// keep it from being queued (director-capabilities.spec.md §4.4). "" for
// none.
//
// Here rather than in either package that files a proposal, for the
// reason AutoSubmitLimitNote is: the end-of-run relay and the live call
// must say it the same way.
func SeededDefaultsNote(proposer string, admitted []SeededDefault) string {
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
	return fmt.Sprintf("\n\n---\n\ngrain: this proposal holds %s, which task %s, the director, which "+
		"proposed it, does not hold itself. They were seeded unchanged when it was filed -- every task "+
		"filed there gets them, whoever files it -- and the director did not ask for them, so they are "+
		"not counted as asking for more than it holds (director-capabilities spec §4.2). Remove them "+
		"from the task if it should go ahead without them.", strings.Join(parts, "; and "), proposer)
}
