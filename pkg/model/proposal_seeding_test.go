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

package model_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/model"
)

// Fixtures for design/spec/director/director-capabilities.spec.md §4.2
// (G9): a director, a repository with a reference among its
// default_capabilities, and the seeding source a proposal filed against
// it was seeded from.
const (
	seededRepoDefault   = `https://caps.example/host-script#host-script={"script":"autoscaler"}`
	seededDeployDefault = `https://caps.example/keys#key={"set":"builder"}`
	seededOtherDefault  = `https://caps.example/host-script#host-script={"script":"elsewhere"}`
	seededPersonDefault = `https://caps.example/seeded-person-only#approve={}`
)

func init() {
	// A method only a person may grant (protocol.RequirePersonGrant):
	// unique to these tests, so registering it once touches no other.
	protocol.RequirePersonGrant("https://caps.example/seeded-person-only", "approve")
}

var (
	seededTarget = model.RepoRef{Owner: "acme", Name: "widgets"}
	seededOther  = model.RepoRef{Owner: "acme", Name: "gadgets"}
)

func seedingDirector() model.Task {
	session := model.ConsoleSession{ID: model.DirectorSessionID, Capabilities: []string{"self-debug"}}
	return model.Task{
		ID:          "9",
		Interactive: true,
		Tags:        []string{model.ConsoleSessionTag(model.DirectorSessionID)},
		Origin:      model.Origin{Reason: model.ReasonConsoleSession},
		Grants:      model.ConsoleSessionGrants(session),
	}
}

func seedingSource() *model.ProposalSeeding {
	target := seededTarget
	return &model.ProposalSeeding{
		Target:             &target,
		RepoDefaults:       []string{seededRepoDefault, seededPersonDefault},
		DeploymentDefaults: []string{"corp-research", seededDeployDefault},
		GrantContext:       model.GrantContext{Kind: model.CatalogueKindTask},
	}
}

func seededChild(grants ...model.Grant) model.Task {
	target := seededTarget
	return model.Task{Target: &target, Grants: grants}
}

func byDefault(c string) model.Grant { return model.Grant{Capability: c, Via: model.GrantByDefault} }
func byLabel(c string) model.Grant   { return model.Grant{Capability: c, Via: model.GrantByLabel} }

func admittedCaps(admitted []model.SeededDefault) []string {
	var out []string
	for _, d := range admitted {
		out = append(out, d.Capability)
	}
	return out
}

func hasPrefixIn(list []string, prefix string) bool {
	return slices.ContainsFunc(list, func(s string) bool { return strings.HasPrefix(s, prefix) })
}

// §4.2's admitted case: a director allowed to queue its own work files
// against a repository whose default_capabilities include a reference,
// and the proposal carries it -- and the deployment's -- unchanged. A
// task anybody files there gets the same grants, so neither is an
// escalation, the proposal is not refused, and each is named with its
// source.
func TestDirectorProposalAdmitsUnchangedSeededDefaults(t *testing.T) {
	t.Parallel()
	director := seedingDirector()
	child := seededChild(byDefault("corp-research"), byDefault(seededRepoDefault), byDefault(seededDeployDefault))
	seeding := seedingSource()

	if got := model.EscalatedCapabilities(child, director); len(got) != 2 {
		t.Fatalf("EscalatedCapabilities = %v, want both default references (the rule's input)", got)
	}
	escalated, admitted := model.ProposalEscalations(child, director, true, seeding)
	if len(escalated) != 0 {
		t.Errorf("escalated = %v, want none: every reference is an unchanged seeded default", escalated)
	}
	if len(admitted) != 2 {
		t.Fatalf("admitted = %+v, want the repository's default and the deployment's", admitted)
	}
	for _, d := range admitted {
		switch {
		case strings.HasPrefix(d.Capability, "https://caps.example/host-script#host-script"):
			if d.Repo == nil || !d.Repo.Same(seededTarget) {
				t.Errorf("repository default %q recorded source %v, want %s", d.Capability, d.Repo, seededTarget)
			}
		case strings.HasPrefix(d.Capability, "https://caps.example/keys#key"):
			if d.Repo != nil {
				t.Errorf("deployment default %q recorded repository %v, want none", d.Capability, d.Repo)
			}
		default:
			t.Errorf("admitted %q, which is no seeded default", d.Capability)
		}
	}
	if refusal := model.AutoApprovalRefusal(child, director, true, seeding); refusal != "" {
		t.Errorf("AutoApprovalRefusal = %q, want none", refusal)
	}

	// §4.4: the note names each default and where it came from.
	note := model.SeededDefaultsNote(director.ID, admitted)
	for _, want := range []string{
		"task 9", "the director",
		"`https://caps.example/host-script#host-script",
		"from the default capabilities of `acme/widgets`",
		"`https://caps.example/keys#key",
		"from this deployment's default capabilities",
		"§4.2",
	} {
		if !strings.Contains(note, want) {
			t.Errorf("note = %q, want it to mention %q", note, want)
		}
	}
	if note := model.SeededDefaultsNote(director.ID, nil); note != "" {
		t.Errorf("note for nothing admitted = %q, want empty", note)
	}
}

// The rule is the director's, and only while it may queue its own work.
func TestSeededDefaultsAdmittedOnlyForAQueueingDirector(t *testing.T) {
	t.Parallel()
	child := seededChild(byDefault(seededRepoDefault))
	seeding := seedingSource()
	director := seedingDirector()

	cases := map[string]struct {
		parent        model.Task
		sessionQueues bool
		seeding       *model.ProposalSeeding
	}{
		"director that may not queue": {director, false, seeding},
		"no seeding source":           {director, true, nil},
		"another console session": {func() model.Task {
			s := director
			s.Tags = []string{model.ConsoleSessionTag("ops")}
			return s
		}(), true, seeding},
		"task merely tagged as the director": {func() model.Task {
			s := director
			s.Interactive = false
			return s
		}(), true, seeding},
		"ordinary task": {model.Task{ID: "3"}, true, seeding},
	}
	for name, tc := range cases {
		escalated, admitted := model.ProposalEscalations(child, tc.parent, tc.sessionQueues, tc.seeding)
		if len(admitted) != 0 || len(escalated) != 1 {
			t.Errorf("%s: escalated = %v, admitted = %+v; want the default held as an escalation", name, escalated, admitted)
		}
		if refusal := model.AutoApprovalRefusal(child, tc.parent, tc.sessionQueues, tc.seeding); !strings.Contains(refusal, "seeded with this deployment's default capability") {
			t.Errorf("%s: refusal = %q, want the seeded default named", name, refusal)
		}
	}
}

// Every case §4.2 keeps as an escalation.
func TestDirectorProposalStillHoldsEveryExcludedCase(t *testing.T) {
	t.Parallel()
	director := seedingDirector()

	cases := map[string]struct {
		child   model.Task
		seeding func(*model.ProposalSeeding)
		held    string // prefix of the escalation that must stay
		refusal string // what the refusal must say
	}{
		// A grant the director typed into its own call.
		"typed by the director": {
			child:   seededChild(byLabel(seededRepoDefault)),
			held:    "https://caps.example/host-script#host-script",
			refusal: "it asks for `https://caps.example/host-script",
		},
		// The proposal carries the default ref with arguments other than
		// the repository's: not the grant the repository's owner decided.
		"default with changed arguments": {
			child:   seededChild(byDefault(seededOtherDefault)),
			held:    "https://caps.example/host-script#host-script",
			refusal: "seeded with this deployment's default capability",
		},
		// The default of a repository other than the one the proposal
		// targets: the seeding source read gadgets', not widgets'.
		"default of another repository": {
			child: seededChild(byDefault(seededRepoDefault)),
			seeding: func(s *model.ProposalSeeding) {
				other := seededOther
				s.Target = &other
			},
			held:    "https://caps.example/host-script#host-script",
			refusal: "seeded with this deployment's default capability",
		},
		// A repository default carried by a proposal with no repository:
		// nothing seeded it from child's own target.
		"repository default on a proposal with no target": {
			child:   model.Task{Grants: []model.Grant{byDefault(seededRepoDefault)}},
			held:    "https://caps.example/host-script#host-script",
			refusal: "seeded with this deployment's default capability",
		},
		// A default that is not in the seeding source at all.
		"default the source never listed": {
			child: seededChild(byDefault(seededRepoDefault)),
			seeding: func(s *model.ProposalSeeding) {
				s.RepoDefaults = nil
			},
			held:    "https://caps.example/host-script#host-script",
			refusal: "seeded with this deployment's default capability",
		},
		// A method only a person may grant (IsPersonOnlyGrant), even
		// seeded unchanged from the target's defaults.
		"person-only default": {
			child:   seededChild(byDefault(seededPersonDefault)),
			held:    "https://caps.example/seeded-person-only#approve",
			refusal: "seeded with this deployment's default capability",
		},
		// An unnormalized reference with no #method cannot have its
		// method-level person-only rules or normalized args checked, even
		// when the seeding source spells it identically.
		"unnormalized reference without a method": {
			child: seededChild(byDefault("https://caps.example/seeded-person-only")),
			seeding: func(s *model.ProposalSeeding) {
				s.RepoDefaults = []string{"https://caps.example/seeded-person-only"}
			},
			held:    "https://caps.example/seeded-person-only",
			refusal: "seeded with this deployment's default capability",
		},
		// The catalogue marks it a person's decision for this task.
		"requires_person in the catalogue": {
			child: seededChild(byDefault(seededRepoDefault)),
			seeding: func(s *model.ProposalSeeding) {
				s.Catalogue = &model.CapabilityCatalogue{Servers: []model.CatalogueServer{{
					ID:           "https://caps.example/host-script",
					Restrictions: &model.CapabilityRestrictions{RequiresPerson: true},
				}}}
			},
			held:    "https://caps.example/host-script#host-script",
			refusal: "seeded with this deployment's default capability",
		},
		// requires_person for these arguments only.
		"requires_person_when in the catalogue": {
			child: seededChild(byDefault(seededRepoDefault)),
			seeding: func(s *model.ProposalSeeding) {
				s.Catalogue = &model.CapabilityCatalogue{Servers: []model.CatalogueServer{{
					ID: "https://caps.example/host-script",
					Restrictions: &model.CapabilityRestrictions{
						RequiresPersonWhen: map[string][]string{"script": {"autoscaler"}},
					},
				}}}
			},
			held:    "https://caps.example/host-script#host-script",
			refusal: "seeded with this deployment's default capability",
		},
		// The catalogue refuses it for this repository.
		"refused by the catalogue for the repository": {
			child: seededChild(byDefault(seededRepoDefault)),
			seeding: func(s *model.ProposalSeeding) {
				s.Catalogue = &model.CapabilityCatalogue{Servers: []model.CatalogueServer{{
					ID:           "https://caps.example/host-script",
					Restrictions: &model.CapabilityRestrictions{Repos: model.Patterns{"acme/gadgets"}},
				}}}
			},
			held:    "https://caps.example/host-script#host-script",
			refusal: "seeded with this deployment's default capability",
		},
		// ... for its template.
		"refused by the catalogue for the template": {
			child: seededChild(byDefault(seededRepoDefault)),
			seeding: func(s *model.ProposalSeeding) {
				s.GrantContext.TemplateID = "tmpl-ad-hoc-only"
				s.Catalogue = &model.CapabilityCatalogue{Servers: []model.CatalogueServer{{
					ID:           "https://caps.example/host-script",
					Restrictions: &model.CapabilityRestrictions{Templates: model.Patterns{"tmpl-release"}},
				}}}
			},
			held:    "https://caps.example/host-script#host-script",
			refusal: "seeded with this deployment's default capability",
		},
		// ... and for its kind.
		"refused by the catalogue for the kind": {
			child: seededChild(byDefault(seededRepoDefault)),
			seeding: func(s *model.ProposalSeeding) {
				s.Catalogue = &model.CapabilityCatalogue{Servers: []model.CatalogueServer{{
					ID:           "https://caps.example/host-script",
					Restrictions: &model.CapabilityRestrictions{Kinds: model.Patterns{model.CatalogueKindCI}},
				}}}
			},
			held:    "https://caps.example/host-script#host-script",
			refusal: "seeded with this deployment's default capability",
		},
	}
	for name, tc := range cases {
		seeding := seedingSource()
		if tc.seeding != nil {
			tc.seeding(seeding)
		}
		escalated, admitted := model.ProposalEscalations(tc.child, director, true, seeding)
		if !hasPrefixIn(escalated, tc.held) {
			t.Errorf("%s: escalated = %v, want %s held", name, escalated, tc.held)
		}
		if hasPrefixIn(admittedCaps(admitted), tc.held) {
			t.Errorf("%s: admitted = %+v, want %s not admitted", name, admitted, tc.held)
		}
		if refusal := model.AutoApprovalRefusal(tc.child, director, true, seeding); !strings.Contains(refusal, tc.refusal) {
			t.Errorf("%s: refusal = %q, want it to say %q", name, refusal, tc.refusal)
		}
	}
}

// A seeded default admitted beside one the director typed: only the
// typed one is held, and only it is named as asked for.
func TestAdmittedDefaultDoesNotHideATypedEscalation(t *testing.T) {
	t.Parallel()
	director := seedingDirector()
	const typed = `https://caps.example/other#m={"x":1}`
	child := seededChild(byDefault(seededRepoDefault), byLabel(typed))
	seeding := seedingSource()

	escalated, admitted := model.ProposalEscalations(child, director, true, seeding)
	if len(escalated) != 1 || !strings.HasPrefix(escalated[0], "https://caps.example/other#m") {
		t.Errorf("escalated = %v, want only the typed reference", escalated)
	}
	if len(admitted) != 1 || !strings.HasPrefix(admitted[0].Capability, "https://caps.example/host-script#host-script") {
		t.Errorf("admitted = %+v, want only the repository default", admitted)
	}
	refusal := model.AutoApprovalRefusal(child, director, true, seeding)
	if !strings.Contains(refusal, "it asks for `https://caps.example/other#m") ||
		strings.Contains(refusal, "host-script") {
		t.Errorf("refusal = %q, want only the typed reference, and the admitted default left out", refusal)
	}
	note := model.ProposalEscalationNote(director.ID, child, escalated)
	if strings.Contains(note, "host-script") {
		t.Errorf("escalation note = %q, want the admitted default left out", note)
	}
}

// Reads and writes beyond the target are not capabilities: an admitted
// default does not let a proposal past UngrantedWrites or UngrantedReads.
func TestAdmittedDefaultsLeaveUngrantedReadsAndWritesHeld(t *testing.T) {
	t.Parallel()
	director := seedingDirector()
	seeding := seedingSource()

	writes := seededChild(byDefault(seededRepoDefault))
	writes.Writes = []model.WriteTarget{{Repo: seededOther}}
	if escalated, _ := model.ProposalEscalations(writes, director, true, seeding); len(escalated) != 0 {
		t.Fatalf("escalated = %v, want the default admitted", escalated)
	}
	if refusal := model.AutoApprovalRefusal(writes, director, true, seeding); !strings.Contains(refusal, "may land work in acme/gadgets") {
		t.Errorf("refusal with an extra write = %q, want the write named", refusal)
	}

	reads := seededChild(byDefault(seededRepoDefault))
	reads.Reads = []model.RepoRef{seededOther}
	if refusal := model.AutoApprovalRefusal(reads, director, true, seeding); !strings.Contains(refusal, "it reads acme/gadgets") {
		t.Errorf("refusal with an extra read = %q, want the read named", refusal)
	}
}

// A default spelled differently in the seeding source (its arguments in
// another order, or written before normalization) is still the same
// grant once both are in recorded form.
func TestSeededDefaultMatchesItsSourceInRecordedForm(t *testing.T) {
	t.Parallel()
	director := seedingDirector()
	const recorded = `https://caps.example/multi#m={"a":"1","b":"2"}`
	const asWritten = `https://caps.example/multi#m={"b":"2","a":"1"}`
	seeding := seedingSource()
	seeding.RepoDefaults = []string{" " + asWritten + " "}
	child := seededChild(byDefault(recorded))
	escalated, admitted := model.ProposalEscalations(child, director, true, seeding)
	if len(escalated) != 0 || len(admitted) != 1 {
		t.Errorf("escalated = %v, admitted = %+v; want the default matched to its source", escalated, admitted)
	}
}
