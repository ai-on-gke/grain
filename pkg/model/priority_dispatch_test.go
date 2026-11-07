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
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// One predicate: a console session (admission: durable) and a task with
// admission: bypass or durable both have priority dispatch, and nothing else
// does -- not an interactive task, not a title, and not a bare session tag
// without the admission setting.
func TestHasPriorityDispatchIsSessionsAndHoldersOfTheGrant(t *testing.T) {
	t.Parallel()
	session := model.Task{
		Origin:      model.Origin{Reason: model.ReasonConsoleSession},
		Interactive: true,
		Tags:        []string{model.ConsoleSessionTag(model.DirectorSessionID)},
		Grants:      model.ConsoleSessionGrants(model.ConsoleSession{ID: model.DirectorSessionID}),
		Admission:   model.AdmissionDurable,
	}
	bareSession := model.Task{
		Origin:      model.Origin{Reason: model.ReasonConsoleSession},
		Interactive: true,
		Tags:        []string{model.ConsoleSessionTag(model.DirectorSessionID)},
	}
	holder := model.Task{Admission: model.AdmissionBypass}
	for name, c := range map[string]struct {
		task model.Task
		want bool
	}{
		"session":          {session, true},
		"bare session":     {bareSession, false},
		"holder":           {holder, true},
		"ordinary":         {model.Task{}, false},
		"standard":         {model.Task{Admission: model.AdmissionStandard}, false},
		"interactive":      {model.Task{Interactive: true, Title: "Director"}, false},
		"other capability": {model.Task{Grants: []model.Grant{{Capability: "self-debug"}}}, false},
	} {
		if got := c.task.HasPriorityDispatch(); got != c.want {
			t.Errorf("%s: HasPriorityDispatch = %v, want %v", name, got, c.want)
		}
	}
}

// Only a person may raise admission above standard: a proposal asking for
// bypass or durable waits for a person even when its proposer holds it, and a
// template carrying bypass is refused to an agent that holds it too.
func TestPriorityDispatchIsAPersonsToGrant(t *testing.T) {
	t.Parallel()
	if !model.IsPersonOnlyGrant(model.PriorityDispatchCapability) {
		t.Fatal("priority-dispatch input alias is not a person-only grant")
	}
	if model.IsPersonOnlyGrant("self-debug") {
		t.Fatal("self-debug reads as a person-only grant")
	}
	parent := model.Task{
		ID:        "steward",
		Admission: model.AdmissionBypass,
		Grants:    []model.Grant{{Capability: "self-debug", Via: model.GrantByGrain}},
	}
	child := model.Task{
		Admission: model.AdmissionBypass,
		Grants:    []model.Grant{{Capability: "self-debug", Via: model.GrantByLabel}},
	}
	if why := model.PersonOnlyProposalSettings(child, parent); why == "" {
		t.Error("PersonOnlyProposalSettings(bypass) = empty, want person-only hold")
	}
	legacyChild := model.Task{Grants: []model.Grant{
		{Capability: model.PriorityDispatchCapability, Via: model.GrantByLabel},
		{Capability: "self-debug", Via: model.GrantByLabel}}}
	if got := model.EscalatedCapabilities(legacyChild, parent); !slices.Equal(got, []string{model.PriorityDispatchCapability}) {
		t.Errorf("EscalatedCapabilities = %v, want priority-dispatch alone", got)
	}
	tmpl := model.Template{ID: "jumper", Admission: model.AdmissionBypass}
	if err := model.TemplateWithinTask(parent, tmpl); err == nil {
		t.Error("an agent holding bypass admission may file from a template setting bypass")
	}
}
