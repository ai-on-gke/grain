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
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/packagepublish"
	"github.com/ai-on-gke/grain/pkg/ci"
)

// Grain reads a cron under .grain/workflows as a task schedule of its own
// (pkg/repodecl's workflows.go). With ai-on-gke/grain in ci_mode: "github",
// only the live-agent workflows (live-agent.yml, live-frameworks.yml,
// live-secrets-repo.yml) carry `on: schedule` in .grain/workflows; all
// non-agent workflows in .grain/workflows are manual workflow_dispatch only,
// while the ported nightly, post-reset-e2e, gcp-smoke, and release-train
// schedules run in .github/workflows/.
func TestTheNightlysVerdictIsBackedByAGrainWorkflowThatIsActuallyScheduled(t *testing.T) {
	workflows, err := ci.LoadNativeDir(os.DirFS(repoRoot(t)))
	if err != nil {
		t.Fatalf("this repository's own %s does not parse: %v", ci.WorkflowDir, err)
	}
	liveScheduled := map[string]bool{
		".grain/workflows/live-agent.yml":        false,
		".grain/workflows/live-frameworks.yml":   false,
		".grain/workflows/live-secrets-repo.yml": false,
	}
	for _, w := range workflows {
		if _, isLive := liveScheduled[w.Path]; isLive {
			if len(w.On.Schedule) == 0 {
				t.Errorf("%s declares no `on: schedule`, so the live-agent suite will not fire on this deployment", w.Path)
			}
			liveScheduled[w.Path] = true
			continue
		}
		if len(w.On.Schedule) > 0 {
			t.Errorf("non-agent workflow %s still declares `on: schedule` (%v); non-agent schedules belong in .github/workflows/", w.Path, w.On.Schedule)
		}
		for _, event := range []ci.Event{ci.EventPush, ci.EventPullRequest, ci.EventRelease, ci.EventSchedule} {
			for _, branch := range []string{"main", "candidate", "grain/task-1"} {
				if w.On.Matches(event, branch) {
					t.Errorf("non-agent workflow %s still triggers automatically on %s (%s); expected workflow_dispatch only", w.Path, event, branch)
				}
			}
		}
		if !w.On.Matches(ci.EventManual, "main") {
			t.Errorf("non-agent workflow %s does not answer workflow_dispatch on main", w.Path)
		}
	}
	for path, seen := range liveScheduled {
		if !seen {
			t.Errorf("missing expected live-agent workflow %s", path)
		}
	}

	ghNightly := upTo(t, stripComments(read(t, ".github", "workflows", "nightly.yml")), "jobs:")
	contains(t, ghNightly, "schedule:")
	contains(t, ghNightly, `cron: "11 2 * * *"`)
}

// The publishing credential is refused to a scheduled run a caller asked
// for, because `schedule` is accepted by ci.Request.Validate from any
// caller. Only the cron grain itself fires (ci.Request.TrustedSchedule)
// reaches it, through model.TrustedScheduleCapability.
func TestThePublishingCredentialStillRefusesAScheduledRun(t *testing.T) {
	p := &packagepublish.Provider{}
	if p.SupportsCIEvent(string(ci.EventSchedule)) {
		t.Errorf("package-publish now accepts %s runs, so a scheduled grain workflow could hold the credential "+
			"that deletes package versions: revisit whether `schedule` is a trusted event at all "+
			"(ci.Request.Validate accepts it from any caller)",
			ci.EventSchedule)
	}
	for _, event := range []ci.Event{ci.EventRelease, ci.EventManual} {
		if !p.SupportsCIEvent(string(event)) {
			t.Errorf("package-publish no longer accepts %s runs, which is where the release publishes from", event)
		}
	}
}
