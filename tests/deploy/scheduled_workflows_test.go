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
// (pkg/repodecl's workflows.go), and .grain/workflows/nightly.yml is what
// fires the long suite on this deployment.
func TestTheNightlysVerdictIsBackedByAGrainWorkflowThatIsActuallyScheduled(t *testing.T) {
	workflows, err := ci.LoadNativeDir(os.DirFS(repoRoot(t)))
	if err != nil {
		t.Fatalf("this repository's own %s does not parse: %v", ci.WorkflowDir, err)
	}
	for _, w := range workflows {
		if w.Path != ".grain/workflows/nightly.yml" {
			continue
		}
		if len(w.On.Schedule) == 0 {
			t.Fatalf("%s declares no `on: schedule`, so nothing fires the long suite on this deployment", w.Path)
		}
		return
	}
	t.Fatal("no .grain/workflows/nightly.yml: nothing runs the long suite on this deployment")
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
