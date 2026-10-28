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

package orchestrator

// What a run is told about the fields this deployment records on its own
// tasks (model.CustomField, customFieldsSection).
//
// Internal because the section is a function of the task alone, and the
// exported way to it is building a whole prompt around a dispatch --
// which is a great deal of machinery for one paragraph of text.

import (
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestCustomFieldsSection(t *testing.T) {
	t.Parallel()
	if got := customFieldsSection(model.Task{ID: "1"}); got != "" {
		t.Errorf("a task carrying no fields got a section: %q", got)
	}

	got := customFieldsSection(model.Task{
		ID: "1",
		CustomFields: map[string][]string{
			"customer": {"acme"},
			// A field declared multiple carries a set, written as the one
			// line a person would write (model.CustomField.Multiple).
			"components": {"prompt", "store"},
		},
	})
	if !strings.Contains(got, "\n- customer: acme") {
		t.Errorf("section = %q, want the single value on its own line", got)
	}
	if !strings.Contains(got, "- components: prompt, store") {
		t.Errorf("section = %q, want the set on one line", got)
	}
	// Sorted by name, so two runs of one task are handed the same text.
	if strings.Index(got, "components") > strings.Index(got, "customer") {
		t.Errorf("section = %q, want the fields in name order", got)
	}
	// And the names are said to be reusable on the work this run
	// proposes (grain/task-105), including the one thing the list above
	// does not itself show: a set is a list on the way back out, not the
	// comma-joined line it is read as.
	if !strings.Contains(got, "custom_fields") || !strings.Contains(got, "list of strings") {
		t.Errorf("section = %q, want it saying what propose_task can carry forward", got)
	}
}

// The section is a function of the task and nothing else -- it never
// reads the declarations -- so a value whose field has since been
// withdrawn from the state repository still reaches the run that is given
// that task. That is the same answer the store gives (the value stays on
// the task) and the task pane gives (it shows it as an undeclared
// leftover), rather than a third one; pkg/staterepo's own
// custom_field_withdrawn_test.go has the migration it falls out of.
func TestCustomFieldsSectionCarriesAValueNothingDeclaresAnyMore(t *testing.T) {
	t.Parallel()
	got := customFieldsSection(model.Task{
		ID:           "1",
		CustomFields: map[string][]string{"priority": {"high"}},
	})
	if !strings.Contains(got, "- priority: high") {
		t.Errorf("section = %q, want the value the task carries, declared or not", got)
	}
}
