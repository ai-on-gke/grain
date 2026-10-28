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

// What a settings sync says about itself (grain/task-476).
//
// A `.grain/settings.yml` writes two columns of the repository's config
// row and cannot reach the rest of it (grain/task-340), so nothing here
// is a loss. It is still a change nobody is told about at the time: the
// prompt is what every agent dispatched to this repository is handed and
// the setup command is what every checkout of it runs, so a merge that
// edits the file shows up later as an agent behaving differently, with
// nothing pointing back at the commit. The line is what makes it
// answerable.
//
// These are serial for the reason checkout_internal_test.go's pair are:
// the journal collects every test holding the log at once, so a test
// asserting on what was *not* said has to hold it alone.

import (
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestASettingsSyncLogsEveryFieldItWrote(t *testing.T) {
	journal := logToTest(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	row := model.RepoConfig{
		Repo:                    repo,
		DeclaredPromptExtension: "Mind the generated client.",
		DeclaredSetupCommand:    "make deps",
	}
	declared := model.RepoSettings{PromptExtension: "Mind the generated client, and the fixtures."}

	logSettingsChanges(repo, "0123456789abcdef", ".grain/settings.yml", declared.Changes(row))

	said := journal.String()
	for _, want := range []string{
		"acme/widgets",
		"0123456789ab",
		".grain/settings.yml",
		`prompt-extension: "Mind the generated client." -> "Mind the generated client, and the fixtures."`,
		`setup-command: "make deps" -> (blank)`,
	} {
		if !strings.Contains(said, want) {
			t.Errorf("the log does not mention %q, so the change is still silent:\n%s", want, said)
		}
	}
	if lines := strings.Count(strings.TrimSpace(said), "\n"); lines != 0 {
		t.Errorf("one sync logged %d lines; it should grep as one:\n%s", lines+1, said)
	}
}

func TestASyncThatChangesNoSettingsLogsNothing(t *testing.T) {
	journal := logToTest(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	logSettingsChanges(repo, "0123456789abcdef", ".grain/settings.yml", nil)
	if said := journal.String(); said != "" {
		t.Errorf("a sync that changed nothing logged a line, which every poll would repeat:\n%s", said)
	}
}

// The cadence half, for the repositories no test of ours can read.
//
// TestThisRepositoryScheduledWorkflowsFitInsideTheirOwnCadence has this
// bound over our own `.grain/workflows`. These two are about somebody
// else's: their crons are translated at sync time, and the failure is
// silent and permanent, so the sync saying so is the only warning there
// is.

// declaredWorkflowReview is a declared schedule of the kind
// repodecl.workflowSchedule produces -- the template id is what marks it
// as one, and what the check keys off.
func declaredWorkflowReview(path, stem string, r model.Recurrence) model.DeclaredScheduleFile {
	id := model.WorkflowReviewTemplateID(stem)
	return model.DeclaredScheduleFile{
		Path: path,
		Schedule: model.Schedule{
			TemplateID: &id,
			Recurrence: r,
			Overlap:    model.OverlapSkip,
			Enabled:    true,
		},
	}
}

func TestASyncWarnsAboutAScheduledWorkflowFiringInsideItsOwnDeadline(t *testing.T) {
	journal := logToTest(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	logScheduledWorkflowCadence(repo, "0123456789abcdef", []model.DeclaredScheduleFile{
		declaredWorkflowReview(".grain/workflows/nightly.yml", "nightly",
			model.Recurrence{Kind: model.RecurrenceEveryNHours, EveryNHours: 6}),
	})

	said := journal.String()
	for _, want := range []string{
		"acme/widgets",
		"0123456789ab",
		".grain/workflows/nightly.yml",
		"6h0m0s",
		"12h0m0s",
	} {
		if !strings.Contains(said, want) {
			t.Errorf("the warning does not mention %q, so the cadence that will stop this suite "+
				"is still unsaid:\n%s", want, said)
		}
	}
}

func TestASyncSaysNothingAboutACadenceWithRoomForItsOwnDeadline(t *testing.T) {
	journal := logToTest(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	logScheduledWorkflowCadence(repo, "0123456789abcdef", []model.DeclaredScheduleFile{
		// The ordinary nightly, which is nearly every one of these, and
		// an ordinary task schedule beside it: a warning on either would
		// be repeated for every repository this deployment reads.
		declaredWorkflowReview(".grain/workflows/nightly.yml", "nightly",
			model.Recurrence{Kind: model.RecurrenceDaily}),
		{Path: ".grain/schedules/chores.yml", Schedule: model.Schedule{
			Recurrence: model.Recurrence{Kind: model.RecurrenceEveryNHours, EveryNHours: 1},
		}},
	})

	if said := journal.String(); said != "" {
		t.Errorf("a cadence with room for the deadline warned anyway:\n%s", said)
	}
}

// A `port-types` entry the sync accepts but that cannot take effect is
// said so in the daemon log, beside the line reporting it as written
// (docs/test-runs/2026-10-11-port-types-filter.md, F2 and F4).
func TestASettingsSyncWarnsAboutPortTypesWithNoEffect(t *testing.T) {
	store, ctx := recreateStore(t)
	journal := logToTest(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, PortBranches: []string{"main", "candidate"},
		DeclaredPortTypes: map[string][]string{"main->candidate": {"fix"}, "main->candidtae": {"fix"}},
		// The operator's own entries are the operator's to read on the
		// repository's page; the sync of the repository's file is not
		// where they are reported.
		PortTypes: map[string][]string{"mian->candidate": {"fix"}}}); err != nil {
		t.Fatal(err)
	}

	logPortTypesWarnings(ctx, store, repo, "0123456789abcdef")

	said := journal.String()
	for _, want := range []string{
		"acme/widgets at 0123456789ab: warning: port-types in .grain/settings.yml: main->candidate is ignored",
		"main->candidtae names candidtae, which is not a paired branch",
	} {
		if !strings.Contains(said, want) {
			t.Errorf("the log does not mention %q:\n%s", want, said)
		}
	}
	if strings.Contains(said, "mian") {
		t.Errorf("the repository's sync reported the operator's entry:\n%s", said)
	}
}
