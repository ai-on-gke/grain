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

package model

import (
	"strings"
	"testing"
)

// clusterSays is a ClusterFit with a fixed answer.
type clusterSays struct {
	ok     bool
	reason string
	asked  int
}

func (c *clusterSays) FitsNow(Commitment) (bool, string) {
	c.asked++
	return c.ok, c.reason
}

func TestBudgetHoldsWorkTheClusterHasNoRoomFor(t *testing.T) {
	cluster := &clusterSays{reason: "no node in the cluster has room"}
	b := NewBudget(Commitment{CPUs: 287, MemoryMB: 1 << 20}, 0, 1.5, 1.5)
	b.ClusterFit = cluster
	want := Commitment{CPUs: 8, MemoryMB: 16384}

	// The budget's arithmetic has plenty of room; the cluster does not.
	for name, verdict := range map[string]BudgetVerdict{
		"Admits":          b.Admits(Commitment{CPUs: 100}, want),
		"AdmitsRepair":    b.AdmitsRepair(Commitment{CPUs: 100}, want),
		"AdmitsException": b.AdmitsException(Commitment{CPUs: 100}, want),
	} {
		if verdict.OK || !strings.Contains(verdict.Reason, "cluster: no node") {
			t.Errorf("%s = %+v, want a refusal in the cluster's words", name, verdict)
		}
	}

	cluster.ok = true
	if v := b.Admits(Commitment{CPUs: 100}, want); !v.OK {
		t.Errorf("Admits with room on the cluster = %+v, want OK", v)
	}
}

func TestClusterFitNeverAdmitsWhatTheBudgetRefuses(t *testing.T) {
	cluster := &clusterSays{ok: true}
	b := NewBudget(Commitment{CPUs: 10, MemoryMB: 1 << 20}, 0, 1, 1)
	b.ClusterFit = cluster
	if v := b.Admits(Commitment{CPUs: 8}, Commitment{CPUs: 4}); v.OK {
		t.Errorf("Admits = %+v, want the budget's own refusal to stand", v)
	}
	// A zero shape charges nothing and asks the cluster nothing.
	asked := cluster.asked
	if v := b.Admits(Commitment{}, Commitment{}); !v.OK || cluster.asked != asked {
		t.Errorf("zero shape: %+v (asked %d more times), want OK without asking", v, cluster.asked-asked)
	}
}
