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
	"strings"
	"testing"
)

// TestScheduleBusyErrorNamesBothUIsOverlapControl pins the Run now refusal
// on a skip-overlap schedule to the control each UI really shows. It named
// only the classic page's "If the last run has not finished" select, which
// /next/ Processes (the new UI) does not have: there the setting is the
// Overlap policy's "Allow overlap" chip
// (docs/test-runs/2026-11-20-recurring-composed-ui.md, bug 1).
func TestScheduleBusyErrorNamesBothUIsOverlapControl(t *testing.T) {
	msg := (&scheduleBusyError{ID: "sched-1"}).Error()
	for _, want := range []string{"has not finished", `"Allow overlap"`, `"If the last run has not finished"`} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal = %q, want it to contain %s", msg, want)
		}
	}
	wf := (&scheduleBusyError{ID: "sched-2", Workflow: true}).Error()
	if strings.Contains(wf, "Allow overlap") || strings.Contains(wf, "If the last run") {
		t.Errorf("workflow refusal = %q offers an overlap setting a workflow schedule cannot have", wf)
	}
}
