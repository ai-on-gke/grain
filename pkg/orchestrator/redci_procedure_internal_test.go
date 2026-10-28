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

import (
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/model"
)

// The steps a run is told to take on a red build, each pinned by the
// words that carry it: the narrow local reproduction, the
// infrastructure classification, and the selective re-run before the
// full suite are the three a run left to itself skips.
var redCIProcedureSteps = []string{
	model.RedCIProcedure,
	"exact failing test names",
	"`query_test_history`",
	"exit 124",
	"exit 137",
	"retrigger at most once",
	"go test -race -run '<TestName>' ./<pkg>",
	"before changing anything",
	"`wait_for_checks` with `jobs` set to the jobs that failed",
	"`wait_for_checks` without `jobs` for the merge gate",
}

func TestEveryRepoTaskIsGivenTheRedCIProcedure(t *testing.T) {
	t.Parallel()
	task := model.Task{
		ID: "t1", Title: "Do the thing", Body: "details",
		Target: &model.RepoRef{Owner: "acme", Name: "widgets"},
		Grants: []model.Grant{{Capability: grainci.CapabilityName}},
	}
	for _, history := range []History{{}, {Repair: true}} {
		prompt := BuildPrompt(task, "", "", false, DefaultMaxRunRuntime, history, nil)
		for _, want := range redCIProcedureSteps {
			if !strings.Contains(prompt, want) {
				t.Errorf("repair=%v: prompt is missing %q of the red-CI procedure:\n%s",
					history.Repair, want, prompt)
			}
		}
		if n := strings.Count(prompt, "1. Get the failing job"); n != 1 {
			t.Errorf("repair=%v: the procedure appears %d times, want once", history.Repair, n)
		}
	}
}

func TestATaskWithNoRepoIsNotGivenTheRedCIProcedure(t *testing.T) {
	t.Parallel()
	task := model.Task{
		ID: "t1", Title: "Do the thing", Body: "details",
		Grants: []model.Grant{{Capability: grainci.CapabilityName}},
	}
	prompt := BuildPrompt(task, "", "", false, DefaultMaxRunRuntime, History{}, nil)
	if strings.Contains(prompt, model.RedCIProcedure) || strings.Contains(prompt, "1. Get the failing job") {
		t.Errorf("a task with no repo has no CI to fix, but was given the procedure:\n%s", prompt)
	}
}

// grain's own repair tasks point at the procedure rather than repeating
// it: the body says to use it, and the steps themselves are the prompt's.
func TestGrainFiledRepairBodiesReferToTheRedCIProcedure(t *testing.T) {
	t.Parallel()
	row := model.BaseBranchCI{
		Repo:   model.RepoRef{Owner: "acme", Name: "widgets"},
		Branch: "main",
		Commit: "0123456789abcdef0123456789abcdef01234567",
		Detail: "the go job failed",
	}
	template := model.SystemTemplate(model.BaseRepairTemplateID, model.RepoRef{})
	bodies := map[string]string{
		"built-in base repair": baseBranchRepairBody(row, nil),
		"base repair template": template.Body,
		"release triage": baseBranchReleaseTriageBody(model.BaseBranchRelease{
			Repo: row.Repo, Branch: row.Branch, Commit: row.Commit, RunID: "r1", Conclusion: "failure",
		}),
	}
	for name, body := range bodies {
		if !strings.Contains(body, model.RedCIProcedureReminder) {
			t.Errorf("%s body does not refer to the %s:\n%s", name, model.RedCIProcedure, body)
		}
		if strings.Contains(body, "1. Get the failing job") {
			t.Errorf("%s body repeats the procedure instead of referring to it:\n%s", name, body)
		}
	}
}

// A task repairing the base branch -- whether grain filed it
// (BaseCIRepairBranchTag) or an operator/agent marked FixesBaseBranch -- must
// never be told that a failure already red on the base branch is "not yours
// to fix -- leave it alone", whether or not grain-ci is granted.
func TestFixesBaseBranchRepairPromptsDoNotSkipInheritedBaseFailures(t *testing.T) {
	t.Parallel()
	target := &model.RepoRef{Owner: "acme", Name: "widgets"}
	marked := model.Task{
		ID: "t-flag", Title: "Unbreak main", Body: "details",
		Target: target,
	}
	model.SetFixesBaseBranch(&marked, true)

	for _, tc := range []struct {
		name string
		task model.Task
	}{
		{
			name: "grain-filed base repair",
			task: model.Task{
				ID: "t-origin", Title: "Fix main CI", Body: "details",
				Target: target,
				Tags:   []string{BaseCIRepairBranchTag(*target, "main")},
			},
		},
		{
			name: "marked fixes_base_branch",
			task: marked,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !mergeRepairRun(tc.task, nil) {
				t.Errorf("mergeRepairRun(%s, nil) = false, want true so RunConfig.Repair and History.Repair are set", tc.name)
			}

			// Without grain-ci granted (GitHub check runs path), both
			// BuildPrompt and BuildSuspendedPrompt must include repairCISection
			// and never the "not yours to fix -- leave it alone" paragraph,
			// even if History.Repair was left false by a caller of BuildPrompt.
			for _, h := range []History{{}, {Repair: true}} {
				prompt := BuildPrompt(tc.task, "", "", false, DefaultMaxRunRuntime, h, nil)
				if strings.Contains(prompt, "not yours to fix") || strings.Contains(prompt, "leave it alone") {
					t.Errorf("without grain-ci (history.Repair=%v): repair task was told to skip inherited base failures:\n%s", h.Repair, prompt)
				}
				if !strings.Contains(prompt, "a failure that is red there too is yours to fix here all the same") {
					t.Errorf("without grain-ci (history.Repair=%v): repair task is missing repairCISection:\n%s", h.Repair, prompt)
				}
				suspended := BuildSuspendedPrompt(tc.task, "", "", h, nil, nil)
				if !strings.Contains(suspended, "a failure that is red there too is yours to fix here all the same") {
					t.Errorf("without grain-ci (history.Repair=%v): suspended prompt is missing repairCISection:\n%s", h.Repair, suspended)
				}
			}

			// With grain-ci granted, the prompt includes the red-CI procedure
			// with its "unless repairing that branch is this run's own work"
			// carve-out and never includes the GitHub-checks "not yours to fix"
			// paragraph.
			withGrainCI := tc.task
			withGrainCI.Grants = []model.Grant{{Capability: grainci.CapabilityName}}
			prompt := BuildPrompt(withGrainCI, "", "", false, DefaultMaxRunRuntime, History{}, nil)
			if strings.Contains(prompt, "not yours to fix") || strings.Contains(prompt, "leave it alone") {
				t.Errorf("with grain-ci: repair task was told to skip inherited base failures:\n%s", prompt)
			}
			if !strings.Contains(prompt, "unless repairing that branch is this run's own work") {
				t.Errorf("with grain-ci: repair task is missing the base-repair carve-out in redCIProcedureSection:\n%s", prompt)
			}
		})
	}
}
