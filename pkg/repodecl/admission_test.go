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
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestTemplatesAndSchedulesDeclareAdmission(t *testing.T) {
	declared := load(t, map[string]string{
		".grain/templates/hotfix.yml":  "title: Hotfix\nadmission: bypass\n",
		".grain/templates/steward.yml": "title: Steward\nadmission: durable\n",
		".grain/schedules/hotfix.yml": `
template: hotfix
admission: durable
daily:
  at: "03:00"
`,
	})
	if len(declared.Warnings) != 0 {
		t.Fatalf("Warnings = %v, want none", declared.Warnings)
	}
	byName := map[string]model.Admission{}
	for _, tmpl := range declared.Templates {
		byName[tmpl.Template.Name] = tmpl.Template.Admission
	}
	if byName["hotfix"] != model.AdmissionBypass {
		t.Errorf("hotfix admission = %s, want bypass", byName["hotfix"])
	}
	if byName["steward"] != model.AdmissionDurable {
		t.Errorf("steward admission = %s, want durable", byName["steward"])
	}
	if got := declared.Schedules[0].Schedule.Admission; got != model.AdmissionDurable {
		t.Errorf("schedule admission = %s, want durable", got)
	}
}

func TestDeprecatedAdmissionGrantsFoldOntoAdmissionWithWarning(t *testing.T) {
	declared := load(t, map[string]string{
		".grain/templates/byp.yml":  "title: Byp\ncapabilities:\n  - priority-dispatch\n  - repo-read:other/lib\n",
		".grain/templates/both.yml": "title: Both\ncapabilities:\n  - priority-dispatch\n  - durable\n",
		".grain/schedules/dur.yml": `
template: byp
capabilities:
  - durable
every:
  hours: 2
`,
	})
	byName := map[string]model.Template{}
	for _, tmpl := range declared.Templates {
		byName[tmpl.Template.Name] = tmpl.Template
	}
	if byName["byp"].Admission != model.AdmissionBypass {
		t.Errorf("byp admission = %s, want bypass", byName["byp"].Admission)
	}
	if len(byName["byp"].Grants) != 0 || len(byName["byp"].Reads) != 1 {
		t.Errorf("byp grants = %+v reads = %+v, want [] and [other/lib]", byName["byp"].Grants, byName["byp"].Reads)
	}
	if byName["both"].Admission != model.AdmissionDurable || len(byName["both"].Grants) != 0 {
		t.Errorf("both = admission %s grants %+v, want durable and []", byName["both"].Admission, byName["both"].Grants)
	}
	sched := declared.Schedules[0].Schedule
	if sched.Admission != model.AdmissionDurable || len(sched.Grants) != 0 {
		t.Errorf("schedule = admission %s grants %+v, want durable and []", sched.Admission, sched.Grants)
	}
	if len(declared.Warnings) != 3 {
		t.Fatalf("Warnings = %v, want 3 deprecation warnings", declared.Warnings)
	}
	for _, wantPath := range []string{".grain/templates/byp.yml", ".grain/templates/both.yml", ".grain/schedules/dur.yml"} {
		if !slices.ContainsFunc(declared.Warnings, func(w string) bool { return strings.Contains(w, wantPath) }) {
			t.Errorf("Warnings = %v, missing warning for %s", declared.Warnings, wantPath)
		}
	}
}

func TestInvalidAndSuiteScheduleAdmissionAreRefused(t *testing.T) {
	msg := loadErr(t, map[string]string{
		".grain/templates/bad.yml": "title: Bad\nadmission: urgent\n",
	})
	if !strings.Contains(msg, `admission "urgent"`) {
		t.Fatalf("invalid admission error = %q", msg)
	}
	suiteMsg := loadErr(t, map[string]string{
		".grain/templates/smoke.yml": "title: Smoke\n",
		".grain/suites/smoke.yml":    "templates: [smoke]\n",
		".grain/schedules/smoke.yml": "suite: smoke\nbase: main\nadmission: bypass\nevery:\n  hours: 1\n",
	})
	if !strings.Contains(suiteMsg, "cannot choose admission") {
		t.Fatalf("suite schedule admission error = %q", suiteMsg)
	}
}
