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
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// ReasonClassifier is ui.Config.Reasons: the orchestrator's sub-cases of
// each state (its livenessreasons.go), in pkg/ui's types. It reads
// nothing but what it is handed, so it is wired wherever a UI is,
// `grain demo`'s included.
type ReasonClassifier struct {
	// PolicyFile is the daemon's -policy file (Config.PolicyFile), which
	// a task local policy holds is told to have edited; "" for none.
	PolicyFile string
	// DispatchBlocked is the condition holding this deployment's
	// dispatch, in the daemon's words (dispatchcondition.Set.Why), or
	// "" while it dispatches; nil is a deployment with nothing to ask
	// -- `grain demo`'s UI, a test -- which never reports one.
	DispatchBlocked func() string
}

// TaskReason is orchestrator.TaskReason.
func (c ReasonClassifier) TaskReason(t model.Task, state model.State, obs *model.Observation,
	openDeps []string, holds []model.BlockerHold, wait *model.GrantWait, asked model.ReplyFacts) ui.Reason {
	dispatchBlocked := ""
	if c.DispatchBlocked != nil && state == model.StateQueued {
		dispatchBlocked = c.DispatchBlocked()
	}
	return uiReason(orchestrator.TaskReason(t, state, obs, openDeps, holds, wait, asked, c.PolicyFile, dispatchBlocked))
}

// MergeReason is orchestrator.MergeReason.
func (ReasonClassifier) MergeReason(reason string) ui.Reason {
	return uiReason(orchestrator.MergeReason(reason))
}

func uiReason(r orchestrator.ReasonLiveness) ui.Reason {
	return ui.Reason{Reason: r.Reason, Class: r.Class, Resource: r.Resource, Driver: r.Driver, Control: r.Control}
}
