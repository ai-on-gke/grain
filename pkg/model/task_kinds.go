package model

// The five kind tags a task can carry on the board and in agent-API task
// headers (pkg/ui/task_kinds.go).
const (
	TaskKindPort     = "port"
	TaskKindRepair   = "repair"
	TaskKindSession  = "session"
	TaskKindProposal = "proposal"
	TaskKindDurable  = "durable"
)

// TaskKinds is the fixed vocabulary of task kind tags, in the order they are
// shown on a task row.
var TaskKinds = []string{
	TaskKindPort,
	TaskKindRepair,
	TaskKindSession,
	TaskKindProposal,
	TaskKindDurable,
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
	return kinds
}
