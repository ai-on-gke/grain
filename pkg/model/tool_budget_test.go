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

import "testing"

func TestUsesToolBudgetIsTheStewardAndDurableTasks(t *testing.T) {
	steward := Task{Tags: []string{ScheduleFiringTag(PipelineStewardTemplateID)}}
	durable := Task{Grants: []Grant{{Capability: DurableCapability}}}
	plain := Task{Tags: []string{"schedule:nightly"}, Grants: []Grant{{Capability: "self-debug"}}}
	if !UsesToolBudget(steward) || !UsesToolBudget(durable) || UsesToolBudget(plain) {
		t.Errorf("UsesToolBudget = steward %v, durable %v, plain %v; want true, true, false",
			UsesToolBudget(steward), UsesToolBudget(durable), UsesToolBudget(plain))
	}
}
