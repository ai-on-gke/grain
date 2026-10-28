package daemon

import (
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// ReasonClassifier is ui.Config.Reasons: the orchestrator's sub-cases of
// each state (its livenessreasons.go), in pkg/ui's types. It reads
// nothing but what it is handed, so it is wired wherever a UI is,
// `grain demo`'s included.
type ReasonClassifier struct{}

// TaskReason is orchestrator.TaskReason.
func (ReasonClassifier) TaskReason(t model.Task, state model.State, obs *model.Observation,
	openDeps []string, wait *model.GrantWait) ui.Reason {
	return uiReason(orchestrator.TaskReason(t, state, obs, openDeps, wait))
}

// MergeReason is orchestrator.MergeReason.
func (ReasonClassifier) MergeReason(reason string) ui.Reason {
	return uiReason(orchestrator.MergeReason(reason))
}

func uiReason(r orchestrator.ReasonLiveness) ui.Reason {
	return ui.Reason{Reason: r.Reason, Class: r.Class, Resource: r.Resource, Driver: r.Driver, Control: r.Control}
}
