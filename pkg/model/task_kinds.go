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

// The six kind tags a task can carry on the board and in agent-API task
// headers (pkg/ui/task_kinds.go).
const (
	TaskKindPort     = "port"
	TaskKindRepair   = "repair"
	TaskKindSession  = "session"
	TaskKindProposal = "proposal"
	TaskKindDurable  = "durable"
	// TaskKindImported is a task the GitHub issue importer filed
	// (ReasonImported). It stays on the task after it is approved and
	// dispatched, so an imported issue is identifiable whatever state it
	// has reached.
	TaskKindImported = "imported"
)

// TaskKinds is the fixed vocabulary of task kind tags, in the order they are
// shown on a task row.
var TaskKinds = []string{
	TaskKindPort,
	TaskKindRepair,
	TaskKindSession,
	TaskKindProposal,
	TaskKindDurable,
	TaskKindImported,
}

// TaskKindTags returns the kind tags t carries at state, in TaskKinds order.
func TaskKindTags(t Task, state State) []string {
	var kinds []string
	for _, l := range t.Links {
		if l.Kind == LinkPortOf && l.Target != "" {
			kinds = append(kinds, TaskKindPort)
			break
		}
	}
	if FixesBaseBranch(t) || GrainBaseRepair(t) {
		kinds = append(kinds, TaskKindRepair)
	}
	if t.Interactive || t.IsConsoleSession() {
		kinds = append(kinds, TaskKindSession)
	}
	if state == StateProposed {
		kinds = append(kinds, TaskKindProposal)
	}
	if t.IsDurable() {
		kinds = append(kinds, TaskKindDurable)
	}
	if t.Origin.Reason == ReasonImported {
		kinds = append(kinds, TaskKindImported)
	}
	return kinds
}
