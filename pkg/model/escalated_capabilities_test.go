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

	"github.com/ai-on-gke/grain/pkg/model"
)

// grain#2409: only what a proposal asked for by name, and its parent does
// not hold, is an escalation -- a deployment default is attached to every
// task filed there whoever files it.
func TestEscalatedCapabilitiesIsWhatTheProposalNamedBeyondItsParent(t *testing.T) {
	t.Parallel()
	parent := model.Task{Grants: []model.Grant{{Capability: "self-debug", Via: model.GrantByLabel}}}
	child := model.Task{Grants: []model.Grant{
		{Capability: "gcp-key", Via: model.GrantByDefault},
		{Capability: "self-debug", Via: model.GrantByLabel},
		{Capability: "self-repair", Via: model.GrantByLabel},
		{Capability: "self-repair", Via: model.GrantByLabel},
		{Capability: "self-upgrade", Via: model.GrantByLabel},
	}}
	got := model.EscalatedCapabilities(child, parent)
	if want := []string{"self-repair", "self-upgrade"}; !slices.Equal(got, want) {
		t.Errorf("EscalatedCapabilities = %v, want %v", got, want)
	}
	if got := model.EscalatedCapabilities(model.Task{Grants: parent.Grants}, parent); len(got) != 0 {
		t.Errorf("EscalatedCapabilities of a child holding its parent's grants = %v, want none", got)
	}
}

func TestCapabilityEscalationNoteNamesEachCapability(t *testing.T) {
	t.Parallel()
	if note := model.CapabilityEscalationNote("7", nil); note != "" {
		t.Errorf("note for nothing escalated = %q, want empty", note)
	}
	note := model.CapabilityEscalationNote("7", []string{"self-repair", "gcp-key"})
	for _, want := range []string{"`self-repair`", "`gcp-key`", "task 7"} {
		if !strings.Contains(note, want) {
			t.Errorf("note = %q, want it to mention %s", note, want)
		}
	}
}

// grain/task-1983: a capability reference seeded on a proposal from this
// deployment's defaults is still an escalation when its proposer does not
// hold it, but it is told apart from one the proposal asked for -- and a
// standing session filed holding the deployment's defaults
// (ConsoleSessionGrants) holds it, so it is no escalation at all.
func TestSeededDefaultReferenceIsReportedAsADefault(t *testing.T) {
	t.Parallel()
	const ref = `https://caps.example/host-script#host-script={"script":"autoscaler"}`
	const asked = `https://caps.example/other#m={"x":1}`
	child := model.Task{Grants: []model.Grant{
		{Capability: "corp-research", Via: model.GrantByDefault},
		{Capability: ref, Via: model.GrantByDefault},
		{Capability: asked, Via: model.GrantByLabel},
	}}
	director := model.ConsoleSession{ID: model.DirectorSessionID, Capabilities: []string{"self-debug"}}

	bare := model.Task{ID: "9", Grants: model.ConsoleSessionGrants(director)}
	escalated := model.EscalatedCapabilities(child, bare)
	if len(escalated) != 2 {
		t.Fatalf("EscalatedCapabilities = %v, want both references", escalated)
	}
	gotAsked, gotSeeded := model.SeededEscalations(child, escalated)
	if len(gotAsked) != 1 || !strings.HasPrefix(gotAsked[0], "https://caps.example/other#m") {
		t.Errorf("asked = %v, want only the reference the proposal named", gotAsked)
	}
	if len(gotSeeded) != 1 || !strings.HasPrefix(gotSeeded[0], "https://caps.example/host-script#host-script") {
		t.Errorf("seeded = %v, want only the default", gotSeeded)
	}
	note := model.ProposalEscalationNote("9", child, escalated)
	if !strings.Contains(note, "this proposal asked for `https://caps.example/other") ||
		!strings.Contains(note, "default capability") ||
		strings.Contains(note, "asked for `https://caps.example/host-script") {
		t.Errorf("note = %q, want the asked reference and the seeded default each explained as what it is", note)
	}
	refusal := model.AutoApprovalRefusal(child, bare, true)
	if !strings.Contains(refusal, "default capability `https://caps.example/host-script") ||
		strings.Contains(refusal, "asks for `https://caps.example/host-script") {
		t.Errorf("refusal = %q, want the seeded reference named as a default", refusal)
	}

	seeded := model.Task{ID: "9", Grants: model.ConsoleSessionGrants(director, "corp-research", ref, "self-debug", "")}
	if got := model.EscalatedCapabilities(model.Task{Grants: child.Grants[:2]}, seeded); len(got) != 0 {
		t.Errorf("EscalatedCapabilities against a session holding the defaults = %v, want none", got)
	}
	if refusal := model.AutoApprovalRefusal(model.Task{Grants: child.Grants[:2]}, seeded, true); refusal != "" {
		t.Errorf("AutoApprovalRefusal against a session holding the defaults = %q, want none", refusal)
	}
	var vias []model.GrantSource
	for _, g := range seeded.Grants {
		vias = append(vias, g.Via)
	}
	want := []model.GrantSource{model.GrantByGrain, model.GrantByGrain, model.GrantByDefault, model.GrantByDefault}
	if !slices.Equal(vias, want) {
		t.Errorf("session grants = %+v, want durable and self-debug by grain, then each default once", seeded.Grants)
	}
}
