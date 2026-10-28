package demo

import (
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/orchestrator"
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
	for _, reason := range []string{"batching/batch_run", "waiting_ci/github_checks_pending", "merge_failed/given_up"} {
		if got := orchestrator.MergeReason(reason); got.Reason != reason {
			t.Errorf("%q is not a merge-queue sub-case the orchestrator classifies", reason)
		}
	}
}
