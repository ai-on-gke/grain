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

package model

import "testing"

func TestNoteAsksAPerson(t *testing.T) {
	t.Parallel()
	for _, note := range []string{
		// #4256's own wording.
		"I didn't change any code. Only someone with admin on ai-on-gke/grain can set GRAIN_MERGE_LOGINS.",
		"Please set the Actions variable GRAIN_MERGE_LOGINS.",
		"Only a repo admin can change this.",
		"You need to grant the app write access before this can publish.",
		"An admin must rotate the key.",
		"The operator has to decide which branch to keep.",
		"This requires manual intervention on the registry.",
		"Publishing needs admin access to the organization.",
		"Grant grain write access to acme/lib and re-run.",
		"please re-run the workflow once the secret is set",
	} {
		if _, ok := NoteAsksAPerson(note); !ok {
			t.Errorf("NoteAsksAPerson(%q) = false, want true", note)
		}
	}
	for _, note := range []string{
		"## Infra review\n\nEverything is healthy.",
		"Fixed the flaky test and pushed; CI is green.",
		"I set the variable in the test fixture and ran the suite.",
		"The admin page renders correctly at both widths.",
		"Nothing needed: the flag is already off on main.",
		"Qualification-Verdict: PASS",
	} {
		if phrase, ok := NoteAsksAPerson(note); ok {
			t.Errorf("NoteAsksAPerson(%q) = true (%q), want false", note, phrase)
		}
	}
}
