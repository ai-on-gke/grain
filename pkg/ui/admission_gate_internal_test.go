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

package ui

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// The admission task setting replaced the priority-dispatch and durable
// grants (design/design/unified-grants.design.md §4.1), and with them the
// gate that kept an agent from handing either one out. These pin that the
// agent API gate still refuses an agent raising admission above standard
// through a request body -- on a task, a template, a schedule, or as a
// repository or deployment default -- under every spelling encoding/json
// would bind to the field, even when the agent's own task is durable, and
// that PATCH /api/tasks/{id} treats any admission edit as a grant edit.
func TestAnAgentCannotRaiseAdmissionThroughTheAPI(t *testing.T) {
	held := []string{model.DurableCapability, model.PriorityDispatchCapability, "self-debug"}
	refused := []struct{ pattern, body string }{
		{"PATCH /api/tasks/{id}", `{"admission":"bypass"}`},
		{"PATCH /api/tasks/{id}", `{"admission":"durable"}`},
		// Lowering is a grant edit on a task too, as priorityDispatch:false was.
		{"PATCH /api/tasks/{id}", `{"admission":"standard"}`},
		{"PATCH /api/tasks/{id}", `{"Admission":"Durable"}`},
		{"PATCH /api/tasks/{id}", `{"ADMISSION":" bypass "}`},
		{"PATCH /api/tasks/{id}", "{\"admi\u017f\u017fion\":\"durable\"}"},
		{"POST /api/tasks", `{"title":"t","admission":"bypass"}`},
		{"POST /api/tasks", `{"title":"t","admission":"durable"}`},
		{"POST /api/templates", `{"name":"n","admission":"durable"}`},
		{"PATCH /api/templates/{id}", `{"admission":"bypass"}`},
		{"POST /api/schedules", `{"title":"t","admission":"durable"}`},
		{"PATCH /api/schedules/{id}", `{"admission":"bypass"}`},
		{"PUT /api/repos/{owner}/{name}/settings", `{"defaultAdmission":"bypass"}`},
		{"PUT /api/settings", `{"defaultAdmission":"durable"}`},
		{"PUT /api/settings", `{"default_admission":"bypass"}`},
		// The retired grant names, still read for one release, fold onto
		// admission and are refused the same way.
		{"POST /api/tasks", `{"title":"t","capabilities":["priority-dispatch"]}`},
		{"PUT /api/settings", `{"defaultCapabilities":["durable"]}`},
	}
	for _, c := range refused {
		if err := agentGrantEscalation(held, c.pattern, []byte(c.body), nil); err == nil {
			t.Errorf("agentGrantEscalation(%s, %s) = nil, want it refused as a person's to give", c.pattern, c.body)
		}
	}
	allowed := []struct{ pattern, body string }{
		{"POST /api/tasks", `{"title":"t","admission":"standard"}`},
		{"POST /api/templates", `{"name":"n","admission":""}`},
		{"PUT /api/repos/{owner}/{name}/settings", `{"defaultAdmission":"standard"}`},
		{"PATCH /api/tasks/{id}", `{"title":"x"}`},
	}
	for _, c := range allowed {
		if err := agentGrantEscalation(held, c.pattern, []byte(c.body), nil); err != nil {
			t.Errorf("agentGrantEscalation(%s, %s) = %v, want standard admission let through", c.pattern, c.body, err)
		}
	}
	for _, body := range []string{`{"admission":"standard"}`, `{"admission":"durable"}`, `{"Admission":"bypass"}`} {
		if !patchTouchesTaskGrants([]byte(body)) {
			t.Errorf("patchTouchesTaskGrants(%s) = false, want an admission edit to be a grant edit", body)
		}
	}
}

// Only a person may give a task admission above standard, so a proposal
// asking for it waits for one whatever its proposer holds -- including a
// durable proposer -- and a template carrying it cannot be filed from by
// an agent.
func TestAdmissionAboveStandardIsAPersonsToGive(t *testing.T) {
	proposer := model.Task{ID: "parent", Admission: model.AdmissionDurable}
	for _, a := range []model.Admission{model.AdmissionBypass, model.AdmissionDurable} {
		if why := model.PersonOnlyProposalSettings(model.Task{Admission: a}, proposer); why == "" {
			t.Errorf("PersonOnlyProposalSettings(admission %s) = \"\", want it held for a person", a)
		}
		if err := model.TemplateWithinTask(proposer, model.Template{ID: "tmpl", Admission: a}); err == nil {
			t.Errorf("TemplateWithinTask(template with admission %s) = nil, want it refused to an agent", a)
		}
	}
	if why := model.PersonOnlyProposalSettings(model.Task{Admission: model.AdmissionStandard}, proposer); why != "" {
		t.Errorf("PersonOnlyProposalSettings(admission standard) = %q, want no hold", why)
	}
}
