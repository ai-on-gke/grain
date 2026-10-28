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
