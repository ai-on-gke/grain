package model

import (
	"fmt"
	"sort"
	"time"
)

// QualificationPrincipal is the automation actor every qualification task
// is attributed to -- its own actor, distinct from SchedulePrincipal (schedule_fire.go)
// and grain's other automation principals, so a task's own conversation
// and origin make plain which mechanism filed it.
var QualificationPrincipal = Principal{Kind: PrincipalAutomation, ID: "qualification"}

// QualificationItem is one entry in a repo's qualification plan: a
// Template (bwsalmon/agents#516) this plan schedules, how many times it
// fires, and which other items in the same plan must finish first.
// TemplateID is what DependsOn names -- there is no separate key, since
// a template may appear at most once per plan
// (QualificationPlan.Validate rejects a duplicate), so its own id
// already identifies this item uniquely within the plan.
//
// There is no per-item content here -- no Title, nothing a Template
// itself already carries -- because CreateQualificationRun resolves
// each item's template fresh from the store at the moment a candidate
// is qualified, the same "not a stale copy" discipline fireTaskSchedule
// already holds TemplateID to. Base is never among that content either
// way (Template's own doc comment on why): CreateQualificationRun
// always targets the candidate's own branch.
type QualificationItem struct {
	TemplateID string
	// Repeat is how many instances of this item's template one run
	// fires -- the issue's own "specify that a template is to be
	// executed multiple times." Always >= 1.
	Repeat int
	// DependsOn is every other item's TemplateID in the same plan that
	// must have all of its own instances complete before any instance of
	// this one is dispatched -- see CreateQualificationRun for how a
	// dependency and a repeat on either side combine.
	DependsOn []string
}

// QualificationPlan is a repo's whole qualification setup: every item a
// fresh, active candidate schedules, and the two switches
// bwsalmon/agents#518 asks for -- requiring a human's approval before any
// of a run's tasks may dispatch, and promoting a candidate automatically
// once its run succeeds. Configured is false, with Items empty, when
// nothing has ever been saved for a repo.
type QualificationPlan struct {
	Repo            RepoRef
	Configured      bool
	RequireApproval bool
	AutoPromote     bool
	Items           []QualificationItem
}

// Validate reports whether a plan's items form a valid dependency graph:
// every TemplateID non-empty and unique within the plan, every DependsOn
// naming another item's TemplateID (never itself), Repeat positive, and
// no cycle -- caught here, once, against the whole plan, rather than left
// for CreateQualificationRun to discover as tasks that can never
// dispatch. It does not check that TemplateID actually names a template
// that still exists -- that is a store lookup, which a pure function over
// the plan's own fields cannot make, so ui.Client.PutQualificationPlan
// checks it against the store before this is ever called.
func (p QualificationPlan) Validate() error {
	ids := make(map[string]bool, len(p.Items))
	for _, it := range p.Items {
		if it.TemplateID == "" {
			return fmt.Errorf("qualification item needs a template")
		}
		if ids[it.TemplateID] {
			return fmt.Errorf("template %s is used more than once in this plan", it.TemplateID)
		}
		ids[it.TemplateID] = true
		if it.Repeat < 1 {
			return fmt.Errorf("qualification item %s: repeat must be at least 1", it.TemplateID)
		}
	}
	for _, it := range p.Items {
		for _, dep := range it.DependsOn {
			if dep == it.TemplateID {
				return fmt.Errorf("qualification item %s cannot depend on itself", it.TemplateID)
			}
			if !ids[dep] {
				return fmt.Errorf("qualification item %s depends on a template not in this plan: %s", it.TemplateID, dep)
			}
		}
	}
	_, err := qualificationTopoOrder(p.Items)
	return err
}

// qualificationTopoOrder returns items in an order where every item
// appears after everything it names in DependsOn, or an error if the
// dependency graph has a cycle -- Kahn's algorithm, run once by Validate
// (to reject a cycle before a plan is ever saved) and again by
// CreateQualificationRun (to know, while allocating task ids, that every
// dependency's own instances already exist by the time they are needed).
//
// Ties break by TemplateID, so the order -- and so the order tasks are
// created in, and their OrderKey spacing -- is deterministic rather than
// a property of map iteration.
func qualificationTopoOrder(items []QualificationItem) ([]QualificationItem, error) {
	byID := make(map[string]QualificationItem, len(items))
	indegree := make(map[string]int, len(items))
	dependents := make(map[string][]string, len(items))
	for _, it := range items {
		byID[it.TemplateID] = it
		indegree[it.TemplateID] = 0
	}
	for _, it := range items {
		for _, dep := range it.DependsOn {
			indegree[it.TemplateID]++
			dependents[dep] = append(dependents[dep], it.TemplateID)
		}
	}

	var ready []string
	for _, it := range items {
		if indegree[it.TemplateID] == 0 {
			ready = append(ready, it.TemplateID)
		}
	}
	sort.Strings(ready)

	order := make([]QualificationItem, 0, len(items))
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		order = append(order, byID[id])

		next := append([]string(nil), dependents[id]...)
		sort.Strings(next)
		for _, dep := range next {
			indegree[dep]--
			if indegree[dep] == 0 {
				ready = append(ready, dep)
			}
		}
		sort.Strings(ready)
	}
	if len(order) != len(items) {
		return nil, fmt.Errorf("qualification plan has a dependency cycle")
	}
	return order, nil
}

// QualificationTaskStatus is one task instance a qualification run
// instantiated, with enough of its current progress to compute the run's
// own aggregate status (QualificationStatus) and to render a summary --
// TemplateName is a snapshot of the resolved template's own Name at the
// moment this instance was created, so a run's own history reads the
// same after a human later edits or deletes the template that produced
// it.
type QualificationTaskStatus struct {
	TaskID       string
	TemplateID   string
	TemplateName string
	// InstanceIndex is 1-based; Repeat > 1 numbers instances 1..Repeat so
	// a UI can show "2 of 3" against an item that fires more than once.
	InstanceIndex int
	Repeat        int
	Approved      bool
	State         State
}

// QualificationRun is one candidate's own qualification: the tasks
// CreateQualificationRun instantiated from its repo's plan the moment it
// first went active. Status is never stored -- QualificationStatus
// derives it from Tasks, the same discipline task_state derives a task's
// own state with, so a run's status can never drift out of sync with
// what its tasks actually did.
type QualificationRun struct {
	ID          int64
	CandidateID int64
	Repo        RepoRef
	CreatedAt   time.Time
	Tasks       []QualificationTaskStatus
}

// QualificationRunStatus is a run's aggregate progress.
type QualificationRunStatus string

const (
	// QualificationPendingApproval means at least one instance is still
	// unapproved -- only reachable when the plan's RequireApproval asked
	// for it, since CreateQualificationRun otherwise lands every instance
	// already approved.
	QualificationPendingApproval QualificationRunStatus = "pending_approval"
	// QualificationRunning means every instance is approved and none has
	// failed yet, but not every one has completed either.
	QualificationRunning QualificationRunStatus = "running"
	// QualificationSucceeded means every instance completed.
	QualificationSucceeded QualificationRunStatus = "succeeded"
	// QualificationFailed means at least one instance failed, or was
	// closed without ever completing -- reported the moment it happens
	// rather than waiting for every other instance to settle, which is
	// the issue's own "show...any failures up front."
	QualificationFailed QualificationRunStatus = "failed"
)

// QualificationStatus reduces a run's task instances to the run's own
// aggregate status. PendingApproval outranks everything else (nothing
// has been let to run yet), and Failed outranks Running (a straggler
// elsewhere does not hide a failure already known).
func QualificationStatus(tasks []QualificationTaskStatus) QualificationRunStatus {
	if len(tasks) == 0 {
		return QualificationRunning
	}
	anyUnapproved, anyFailed, allCompleted := false, false, true
	for _, t := range tasks {
		if !t.Approved {
			anyUnapproved = true
		}
		switch t.State {
		// StateAwaitingSubmit and StateAwaitingReply alongside
		// StateCompleted: all three mean the run is over and produced what
		// it was going to produce, and what separates them is only what is
		// left for a human to do about it -- merge the pull request
		// themselves, read an answer that needed no pull request, or
		// nothing at all. A qualification run asks whether the candidate's
		// tasks worked, not whether somebody has since clicked Submit or
		// read the answer.
		case StateCompleted, StateAwaitingSubmit, StateAwaitingReply:
		case StateFailed, StateClosed:
			// StateClosed here means closed without ever completing --
			// StateCompleted is set independently the moment a task does,
			// and task_state's own precedence means a task can still read
			// 'closed' after completing, but a qualification task closed
			// out from under a run is being told to stop counting on it,
			// which reads as a failure either way.
			anyFailed = true
			allCompleted = false
		default:
			allCompleted = false
		}
	}
	switch {
	case anyUnapproved:
		return QualificationPendingApproval
	case anyFailed:
		return QualificationFailed
	case allCompleted:
		return QualificationSucceeded
	default:
		return QualificationRunning
	}
}
