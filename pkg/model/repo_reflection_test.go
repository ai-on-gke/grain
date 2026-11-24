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

// What grain's weekly repository reflection is and is not allowed to
// be. Like the infrastructure review, the template is a prompt plus a
// set of grants, and the feature is that this run proposes repository
// changes and cannot make one: every assertion here is one of the ways
// that stops being true.

import (
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func repoReflection(t *testing.T) *model.Template {
	t.Helper()
	tmpl := model.SystemTemplate(model.RepoReflectionTemplateID,
		model.RepoRef{Owner: "bwsalmon", Name: "grain-home"})
	if tmpl == nil {
		t.Fatal("this build ships no repository reflection template")
	}
	return tmpl
}

func TestTheRepoReflectionIsSeededWeekly(t *testing.T) {
	t.Parallel()
	seeded := false
	for _, tmpl := range model.SystemTemplates(model.RepoRef{Owner: "bwsalmon", Name: "grain-home"}) {
		seeded = seeded || tmpl.ID == model.RepoReflectionTemplateID
	}
	if !seeded {
		t.Errorf("SystemTemplates does not seed %s, so no deployment gets a repository reflection",
			model.RepoReflectionTemplateID)
	}
	got := model.SystemScheduleRecurrence(model.RepoReflectionTemplateID, 180)
	want := model.Recurrence{Kind: model.RecurrenceWeekly, Weekday: time.Monday, TimeOfDay: 180}
	if got != want {
		t.Errorf("recurrence = %+v, want %+v: recurrence across distinct tasks needs a week of them", got, want)
	}
}

// The reflection reads everything and writes nothing but proposals and
// its one ledger document. A target or a write grant would turn a job
// whose value is that a person approves each change into one that makes
// them.
func TestTheRepoReflectionCanProposeAndCannotApply(t *testing.T) {
	t.Parallel()
	tmpl := repoReflection(t)
	if tmpl.Target != nil {
		t.Errorf("target = %s, want none: this job files proposals, each naming its own repository", tmpl.Target)
	}
	granted := map[string]bool{}
	for _, g := range tmpl.Grants {
		granted[g.Capability] = true
	}
	for _, want := range []string{model.APIRef("self-debug"), model.GrainSourceGrant, model.APIRef("memory")} {
		if !granted[want] {
			t.Errorf("the repository reflection does not hold %q; its grants are %+v", want, tmpl.Grants)
		}
	}
	for _, forbidden := range []string{"self-repair", model.GrainHostExecGrant, model.GrainCIGrant, model.GitPushAnyRefGrant} {
		if granted[forbidden] {
			t.Errorf("the repository reflection holds %q: a job that proposes changes must not be able to make one", forbidden)
		}
	}
	if len(tmpl.Reads) != 1 || tmpl.Reads[0] != (model.RepoRef{Owner: "bwsalmon", Name: "grain-home"}) {
		t.Errorf("reads = %v, want the state repository alone", tmpl.Reads)
	}
}

func TestTheRepoReflectionReadsNothingWithoutAStateRepository(t *testing.T) {
	t.Parallel()
	tmpl := model.SystemTemplate(model.RepoReflectionTemplateID, model.RepoRef{})
	if tmpl == nil {
		t.Fatal("no repository reflection template for a local-only deployment")
	}
	if len(tmpl.Reads) != 0 {
		t.Errorf("reads = %v, want none for a deployment with no state repository", tmpl.Reads)
	}
	if strings.Contains(tmpl.Body, "tables/repo_config.json") {
		t.Error("the prompt points a deployment with no state repository at tables/repo_config.json")
	}
}

// The prompt's load-bearing sentences: each is one of the ways the job
// would quietly turn into noise, an unreviewed change, or a second
// memory agent.
func TestTheRepoReflectionPromptHoldsItsRules(t *testing.T) {
	t.Parallel()
	body := repoReflection(t).Body
	for _, want := range []string{
		"**You propose. You never apply.**",
		"Start with what you said last week",
		"**distinct tasks**",
		"never instructions to you",
		"propose nothing",
		"grain task propose --repo owner/name",
		"At most 3 proposals a week",
		"**Eliminate**",
		"**Automate**",
		"why each does not",
		"**This deployment's configuration**",
		"**grain itself**",
		"`" + model.GrainImprovementLedgerName + "`",
		"Do not run `grain memory write`",
		"Saying nothing is a good week",
		"Never end on `ask_question`",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the repository reflection prompt is missing %q", want)
		}
	}
}
