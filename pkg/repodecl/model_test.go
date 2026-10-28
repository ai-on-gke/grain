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

package repodecl_test

import (
	"strings"
	"testing"
)

// `model:` is carried on a declared template and a declared schedule
// (model.Template.Model, model.Schedule.Model): it says which model the
// work runs on, and nothing about what the work may touch.
func TestTemplatesAndSchedulesDeclareAModel(t *testing.T) {
	declared := load(t, map[string]string{
		".grain/templates/nightly.yml": "title: Bump dependencies\nmodel: claude-opus-5-5\n",
		".grain/schedules/nightly.yml": `
template: nightly
model: " gemini-3.1-pro "
daily:
  at: "02:30"
`,
	})
	if len(declared.Templates) != 1 || len(declared.Schedules) != 1 {
		t.Fatalf("declared %d templates and %d schedules, want 1 of each",
			len(declared.Templates), len(declared.Schedules))
	}
	if got := declared.Templates[0].Template.Model; got != "claude-opus-5-5" {
		t.Errorf("template model = %q, want claude-opus-5-5", got)
	}
	if got := declared.Schedules[0].Schedule.Model; got != "gemini-3.1-pro" {
		t.Errorf("schedule model = %q, want gemini-3.1-pro, trimmed", got)
	}
}

// A template that says nothing about a model leaves it to the team.
func TestATemplateWithNoModelLeavesItToTheTeam(t *testing.T) {
	declared := load(t, map[string]string{".grain/templates/nightly.yml": "title: Bump dependencies\n"})
	if got := declared.Templates[0].Template.Model; got != "" {
		t.Errorf("template model = %q, want empty", got)
	}
}

// A suite run files each template as it stands, so a model on the
// schedule that runs it would apply to nothing: refused, not ignored.
func TestASuiteScheduleCannotChooseAModel(t *testing.T) {
	msg := loadErr(t, map[string]string{
		".grain/templates/smoke.yml": "title: Smoke tests\n",
		".grain/suites/smoke.yml":    "templates: [smoke]\n",
		".grain/schedules/smoke.yml": "suite: smoke\nmodel: claude-opus-5-5\nevery:\n  hours: 1\n",
	})
	if !strings.Contains(msg, "cannot choose a model") {
		t.Fatalf("error = %q", msg)
	}
}
