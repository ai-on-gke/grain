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

package demo

import (
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// The sub-cases the demo seeds beside its merge-queue states are ones the
// orchestrator classifies, each under the state it is seeded with: a
// reason renamed there and not here would put a raw name on the demo's
// rows that no deployment ever shows.
func TestDemoMergeReasonsAreClassified(t *testing.T) {
	for state, reason := range demoMergeReasons {
		if got := orchestrator.MergeReason(reason); got.Reason != reason {
			t.Errorf("%q is not a merge-queue sub-case the orchestrator classifies", reason)
		}
		if !strings.HasPrefix(reason, state+"/") {
			t.Errorf("%q is seeded with state %q, which it is not a sub-case of", reason, state)
		}
	}
	for _, reason := range []string{"waiting_ci/grain_running", "waiting_ci/github_checks_pending", "merge_failed/given_up"} {
		if got := orchestrator.MergeReason(reason); got.Reason != reason {
			t.Errorf("%q is not a merge-queue sub-case the orchestrator classifies", reason)
		}
	}
}
