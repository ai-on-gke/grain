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

// What a sync reports about the settings a repository declared for
// itself (model.RepoSettings.Changes, grain/task-476).
//
// A declaration writes two columns of the repository's config row and
// cannot reach any of the others (grain/task-340), so there is no loss
// here to guard against and these do not test for one. What they test is
// that the two it does write are said out loud when they move: the
// prompt every agent dispatched to this repository is handed, and the
// command every checkout of it runs. Both change by merging a file, and
// both fail as an agent behaving differently somewhere else rather than
// as anything naming the merge, so the report has to name the field the
// way the file names it and say the old value as well as the new one.

import (
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

var declaredRepo = model.RepoRef{Owner: "bwsalmon", Name: "grain"}

func TestASyncReportsTheSettingsFieldsItWrites(t *testing.T) {
	t.Parallel()
	// A row with a settings file already applied to it, and an edit to
	// that file that rewrites one column and blanks the other.
	row := model.RepoConfig{
		Repo: declaredRepo,
		// The operator's own values, which a declaration never touches.
		PromptExtension: "Operator standing instructions.",
		SetupCommand:    "make operator-deps",
		// The declared layer, as the last sync left it.
		DeclaredPromptExtension: "Always run the linter before you push.",
		DeclaredSetupCommand:    "make deps",
	}
	declared := model.RepoSettings{PromptExtension: "Run the linter, and the formatter."}

	changes := declared.Changes(row)
	described := model.DescribeSettingsChanges(changes)
	for _, want := range []string{
		`prompt-extension: "Always run the linter before you push." -> "Run the linter, and the formatter."`,
		`setup-command: "make deps" -> (blank)`,
	} {
		if !strings.Contains(described, want) {
			t.Errorf("the report does not say %q, so nobody reading it knows what changed:\n%s", want, described)
		}
	}
	// And nothing about the operator's own columns, which sit beside the
	// declared ones and are not in a declaration's reach at all.
	if strings.Contains(described, "Operator standing instructions") ||
		strings.Contains(described, "make operator-deps") {
		t.Errorf("the report names a value the declaration cannot write:\n%s", described)
	}

	// Every sync after the file's last edit: the row already says what the
	// file says, so there is nothing to report and nothing is logged.
	applied := row
	declared.ApplyTo(&applied)
	if again := declared.Changes(applied); len(again) > 0 {
		t.Errorf("re-applying the same file reports %d change(s), so every poll would log one:\n%s",
			len(again), model.DescribeSettingsChanges(again))
	}

	// A withdrawn settings file takes the declared layer back to blank,
	// which is reported the same way (Store.SyncRepoDeclarations).
	withdrawn := model.DescribeSettingsChanges(model.RepoSettings{}.Changes(applied))
	if !strings.Contains(withdrawn, `prompt-extension: "Run the linter, and the formatter." -> (blank)`) {
		t.Errorf("withdrawing the file does not report the declared layer going:\n%s", withdrawn)
	}
}

// A prompt extension is prose and runs to pages, so the line that
// reports it being replaced has to stay a line.
func TestALongSettingsValueIsReportedAsALine(t *testing.T) {
	t.Parallel()
	long := "The first line of a standing instruction.\n" + strings.Repeat("and more of it. ", 200)
	changes := model.RepoSettings{}.Changes(model.RepoConfig{Repo: declaredRepo, DeclaredPromptExtension: long})
	described := model.DescribeSettingsChanges(changes)
	if strings.Contains(described, "\n") {
		t.Errorf("the report of a multi-line value is not one line:\n%s", described)
	}
	if !strings.Contains(described, "The first line of a standing instruction.") {
		t.Errorf("the report does not say which text was dropped:\n%s", described)
	}
	if !strings.Contains(described, "characters)") {
		t.Errorf("the report does not say how much text was dropped:\n%s", described)
	}
}
