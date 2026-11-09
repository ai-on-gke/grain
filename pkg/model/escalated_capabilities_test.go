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

// grain/task-3411: a person-granted set (a gcp permission set over an
// existing service account, x-grain-person-granted in its server's
// schema) is held for a person even when the proposer holds the very same
// grant -- and the note says so, rather than that the proposer does not
// hold it. Walked live in docs/test-runs/2026-11-08-gcp-token-mode-containers.md,
// where the old note told the person deciding that task 9 "does not hold
// itself" the set it was running with.
func TestPersonGrantedEscalationIsNotSaidToBeUnheld(t *testing.T) {
	const id = "https://caps.example/walk-gcp"
	protocol.NotePersonGrantedArgs(id, map[string]*protocol.MethodInfo{"gcp-key": {ArgsSchema: map[string]any{
		"properties": map[string]any{"permission_set": map[string]any{
			"enum": []any{"sandbox-agent", "org-deploy"}, "default": "sandbox-agent",
			protocol.PersonGrantedArgKey: []any{"org-deploy"},
		}},
	}}})
	t.Cleanup(func() { protocol.NotePersonGrantedArgs(id, map[string]*protocol.MethodInfo{"gcp-key": {}}) })
	held := id + `#gcp-key={"permission_set":"org-deploy","primary":true}`
	covered := id + `#gcp-key={"permission_set":"sandbox-agent","primary":true}`
	parent := model.Task{ID: "9", Grants: []model.Grant{
		{Capability: held, Via: model.GrantByLabel},
		{Capability: covered, Via: model.GrantByLabel},
	}}
	child := model.Task{Grants: []model.Grant{
		{Capability: held, Via: model.GrantByLabel},
		{Capability: covered, Via: model.GrantByLabel},
		{Capability: "self-debug", Via: model.GrantByLabel},
	}}
	escalated := model.EscalatedCapabilities(child, parent)
	if !slices.Contains(escalated, held) || slices.Contains(escalated, covered) {
		t.Fatalf("EscalatedCapabilities = %v, want the person-granted set (held by its parent) and not the covered one", escalated)
	}
	if !model.IsPersonOnlyGrant(held) || model.IsPersonOnlyGrant(covered) {
		t.Errorf("IsPersonOnlyGrant: want %s and not %s", held, covered)
	}
	note := model.ProposalEscalationNote("9", child, escalated)
	personal := "this proposal asked for `" + held + "`, which only a person may grant"
	if !strings.Contains(note, personal) || !strings.Contains(note, "whether or not task 9, which proposed it, holds it") {
		t.Errorf("note = %q, want the held person-granted set explained as a person's to grant", note)
	}
	if strings.Contains(note, held+"`, which task 9, which proposed it, does not hold itself") ||
		strings.Contains(note, "`self-debug`, `"+held) {
		t.Errorf("note = %q, says task 9 does not hold a set it holds", note)
	}
	if !strings.Contains(note, "this proposal asked for `self-debug`, which task 9, which proposed it, does not hold itself") {
		t.Errorf("note = %q, want self-debug still named as one task 9 does not hold", note)
	}
	refusal := model.AutoApprovalRefusal(child, parent, false, nil)
	if !strings.Contains(refusal, "it asks for `"+held+"`, which only a person may grant, whether or not your task holds it") ||
		!strings.Contains(refusal, "it asks for `self-debug`, which your task does not hold") {
		t.Errorf("refusal = %q, want the person-granted set and self-debug each explained as what it is", refusal)
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
	refusal := model.AutoApprovalRefusal(child, bare, true, nil)
	if !strings.Contains(refusal, "default capability `https://caps.example/host-script") ||
		strings.Contains(refusal, "asks for `https://caps.example/host-script") {
		t.Errorf("refusal = %q, want the seeded reference named as a default", refusal)
	}

	seededSession := model.ConsoleSession{
		ID:           model.DirectorSessionID,
		Capabilities: []string{"self-debug", "corp-research", ref, "self-debug", ""},
	}
	seeded := model.Task{ID: "9", Admission: model.AdmissionDurable, Grants: model.ConsoleSessionGrants(seededSession)}
	if got := model.EscalatedCapabilities(model.Task{Grants: child.Grants[:2]}, seeded); len(got) != 0 {
		t.Errorf("EscalatedCapabilities against a session holding the defaults = %v, want none", got)
	}
	if refusal := model.AutoApprovalRefusal(model.Task{Grants: child.Grants[:2]}, seeded, true, nil); refusal != "" {
		t.Errorf("AutoApprovalRefusal against a session holding the defaults = %q, want none", refusal)
	}
	var vias []model.GrantSource
	for _, g := range seeded.Grants {
		vias = append(vias, g.Via)
	}
	want := []model.GrantSource{model.GrantByGrain, model.GrantByGrain, model.GrantByGrain, model.GrantByDefault}
	if !slices.Equal(vias, want) {
		t.Errorf("session grants = %+v, want each configured capability once by grain, then the session defaults", seeded.Grants)
	}
}
