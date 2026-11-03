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

package daemon

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/ci"
)

// The configuration repository's built-in check answers every event
// grain asks of it: a push of main or a task branch, and the merge
// queue's overlap re-check, which is a pull_request run of the queue
// branch. A request it does not answer plans nothing and errors.
func TestConfigRepoCIPlansTheStateCheckForPushAndPullRequest(t *testing.T) {
	t.Parallel()
	workflows, _, err := ConfigRepoCISource{}.Workflows(t.Context(), "grain/config", "main")
	if err != nil {
		t.Fatalf("reading the built-in config workflow: %v", err)
	}
	for _, tc := range []struct {
		event ci.Event
		ref   string
	}{
		{ci.EventPush, "main"},
		{ci.EventPush, "grain/task-7"},
		{ci.EventPullRequest, "grain-merge-queue/abc123/main"},
		{ci.EventPullRequest, "grain/task-7"},
	} {
		plan, err := ci.PlanRun(ci.Request{Repo: "grain/config", Ref: tc.ref, Commit: "abc", Event: tc.event}, workflows)
		if err != nil {
			t.Fatalf("%s of %s: planning: %v", tc.event, tc.ref, err)
		}
		if plan.Unmatched != "" || len(plan.Jobs) != 1 {
			t.Fatalf("%s of %s: plan = %d job(s), unmatched %q; want the state check", tc.event, tc.ref, len(plan.Jobs), plan.Unmatched)
		}
		job := plan.Jobs[0]
		if job.Job.ID != "check" || job.Job.Builtin != StateCheckBuiltin {
			t.Errorf("%s of %s: planned %s (builtin %q), want check (%s)", tc.event, tc.ref, job.ID, job.Job.Builtin, StateCheckBuiltin)
		}
	}
}
