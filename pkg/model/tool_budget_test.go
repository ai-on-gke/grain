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
