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

package deploy

import (
	"os"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/repodecl"
	"github.com/ai-on-gke/grain/pkg/trustboundary"
)

// promoteReleasePath is the person-only last step of a trunk release
// (design/design/release-from-trunk.design.md, §4).
const promoteReleasePath = ".grain/workflows/promote-release.yml"

func promoteReleaseWorkflow(t *testing.T) ci.Workflow {
	t.Helper()
	workflows, _, err := ci.LoadDir(os.DirFS(repoRoot(t)))
	if err != nil {
		t.Fatalf("this repository's own %s does not parse: %v", ci.WorkflowDir, err)
	}
	for _, w := range workflows {
		if w.Path == promoteReleasePath {
			return w
		}
	}
	t.Fatalf("%s does not exist", promoteReleasePath)
	return ci.Workflow{}
}

// It answers a dispatch and nothing else: no merge, no push, no pull
// request and no cron can make a release official.
func TestPromoteReleaseIsDispatchOnly(t *testing.T) {
	w := promoteReleaseWorkflow(t)
	if !w.On.Matches(ci.EventManual, "main") {
		t.Errorf("%s does not answer workflow_dispatch on main (%s)", promoteReleasePath, w.On.Describe())
	}
	for _, event := range []ci.Event{ci.EventPush, ci.EventPullRequest, ci.EventRelease, ci.EventSchedule} {
		for _, branch := range []string{"main", "candidate"} {
			if w.On.Matches(event, branch) {
				t.Errorf("%s answers %s on %s; only a person's dispatch may run it", promoteReleasePath, event, branch)
			}
		}
	}
}

// Every job refuses to run unless grain says a person asked: the train
// script's approve and reject modes check GRAIN_CI_REQUESTED_BY_KIND
// themselves, and any other job checks it in its own first step. A dispatch
// by any route but the person-only one therefore pushes nothing.
func TestEveryPromoteReleaseJobRefusesAnybodyButAPerson(t *testing.T) {
	w := promoteReleaseWorkflow(t)
	jobs := map[string]bool{}
	for _, job := range w.Jobs {
		jobs[job.ID] = true
		if len(job.Steps) == 0 {
			t.Errorf("%s job %s has no steps", promoteReleasePath, job.ID)
			continue
		}
		first := job.Steps[0].Run
		guarded := strings.Contains(first, `GRAIN_CI_REQUESTED_BY_KIND:-}" != human`) ||
			strings.Contains(first, "scripts/release-from-trunk.sh approve") ||
			strings.Contains(first, "scripts/release-from-trunk.sh reject")
		if !guarded {
			t.Errorf("%s job %s's first step does not refuse a caller that is not a person:\n%s",
				promoteReleasePath, job.ID, first)
		}
		if job.Cache {
			t.Errorf("%s job %s may be replayed from the cache; every run is a decision", promoteReleasePath, job.ID)
		}
	}
	for _, want := range []string{"approve", "promote", "reject"} {
		if !jobs[want] {
			t.Errorf("%s declares no %s job (the approval route starts promote and reject)", promoteReleasePath, want)
		}
	}
	script := read(t, "scripts", "release-from-trunk.sh")
	if !strings.Contains(script, `if [ "${GRAIN_CI_REQUESTED_BY_KIND:-}" != human ]; then`) {
		t.Error("scripts/release-from-trunk.sh's approve and reject modes no longer refuse a caller that is not a person")
	}
}

// human: required, in both of the trust-boundary declarations: the
// repository's own path trigger holds a change to the workflow and the
// scripts it runs for a person's Submit, and grain's built-in list puts
// them in a category no clean automated review releases.
func TestPromoteReleaseIsDeclaredHumanRequired(t *testing.T) {
	root := repoRoot(t)
	decl, err := repodecl.Load(os.DirFS(root), model.GrainSourceRepo())
	if err != nil {
		t.Fatalf("this repository's declarations do not load: %v", err)
	}
	guarded := []string{promoteReleasePath, ".grain/workflows/promote.yml", "scripts/release-from-trunk.sh", "scripts/release-images.sh"}
	var trigger *model.PathTrigger
	for i, tr := range decl.Triggers {
		if tr.Name == "release-promotion" {
			trigger = &decl.Triggers[i]
		}
	}
	if trigger == nil {
		t.Fatalf(".grain/triggers/release-promotion.yml is not among the declared triggers: %+v", decl.Triggers)
	}
	if !trigger.Gate || trigger.Human != model.TriggerHumanRequired || !trigger.Sensitive {
		t.Errorf("release-promotion trigger = gate %v, human %q, sensitive %v; want gate: true, human: required, sensitive: true",
			trigger.Gate, trigger.Human, trigger.Sensitive)
	}
	for _, p := range guarded {
		if !containsString(trigger.Paths, p) {
			t.Errorf("the release-promotion trigger does not hold %s (paths %v)", p, trigger.Paths)
		}
		matches := trustboundary.Classify([]string{p})
		if !trustboundary.RequiresHuman(matches) {
			t.Errorf("%s is not on a human-required trust-boundary category: %+v", p, matches)
		}
	}
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
