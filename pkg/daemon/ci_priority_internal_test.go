package daemon

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/ci"
)

// A trunk release gate's own floor (ci.Request.Priority, RFT-2) survives
// this deployment's ranking of the run: a run on a branch that heads no
// merge queue and has no repair open is still ranked PriorityReleaseGate,
// which is what lets its jobs into the repair reserve. A request that asks
// for nothing is ranked exactly as it was.
func TestTheJobGateRankKeepsARequestsReleaseGateFloor(t *testing.T) {
	t.Parallel()
	rank := CiMergeQueuePriority(nil)
	for _, tc := range []struct {
		name string
		req  ci.Request
		want ci.Priority
	}{
		{"release gate", ci.Request{Repo: "o/r", Ref: "refs/heads/main", Priority: ci.PriorityReleaseGate}, ci.PriorityReleaseGate},
		{"ordinary", ci.Request{Repo: "o/r", Ref: "refs/heads/feature"}, ci.PriorityNormal},
		{"no ref", ci.Request{Repo: "o/r", Priority: ci.PriorityReleaseGate}, ci.PriorityReleaseGate},
	} {
		if got := rank(tc.req); got != tc.want {
			t.Errorf("%s: rank = %d, want %d", tc.name, got, tc.want)
		}
	}
	if !rank(ci.Request{Priority: ci.PriorityReleaseGate}).MaySpendReserve() {
		t.Error("a release gate's rank may not spend the repair reserve")
	}
}
