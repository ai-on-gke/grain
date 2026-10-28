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
