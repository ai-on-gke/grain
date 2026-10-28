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
	"fmt"
	"time"
)

// SuitePrincipal is the automation actor every task a suite run files
// is attributed to -- its own actor, distinct from SchedulePrincipal
// (schedule_fire.go) and QualificationPrincipal (qualification.go), so a
// task's own conversation and origin make plain which mechanism filed
// it.
var SuitePrincipal = Principal{Kind: PrincipalAutomation, ID: "suite"}

// SuiteItem is one Template (bwsalmon/agents#516) a suite runs,
// referenced by id rather than copied -- content lives on the template,
// and a run resolves it fresh every time a pass fires, the same "not a
// stale copy" discipline fireTaskSchedule and CreateQualificationRun
// already hold TemplateID to.
type SuiteItem struct {
	TemplateID string
}

// SuiteMode decides when a run's passes stop.
type SuiteMode string

const (
	// SuiteCount runs the suite's items exactly Count times, whatever
	// each pass produces.
	SuiteCount SuiteMode = "count"
	// SuiteUntilClean re-runs the suite's items pass after pass until
	// one pass opens no pull request and proposes no follow-up task --
	// bwsalmon/agents#642's own "run the tasks until they generate no
	// issues or repo changes" -- or until MaxPasses passes have run
	// without ever reaching one, whichever comes first.
	SuiteUntilClean SuiteMode = "until_clean"
)

func (m SuiteMode) Valid() bool {
	switch m {
	case SuiteCount, SuiteUntilClean:
		return true
	}
	return false
}

// Suite is a saved combination of templates plus how to run them
// against a repo and branch (bwsalmon/agents#642) -- a template a human
// builds once, then runs any number of times, the same
// declared/instantiated split every other "run this against a repo"
// mechanism here already has (Template -> Schedule/QualificationRun,
// here Suite -> SuiteRun).
//
// RequireApproval is QualificationPlan's own switch
// (bwsalmon/agents#518), read the same way here: it leaves every pass's
// tasks unapproved for a human to approve through the ordinary
// task-approval flow -- there is no suite-specific approval endpoint,
// since a suite's tasks are just tasks. AutoMerge has no equivalent on a
// plan, and is this suite's own policy for every task of every pass it
// runs: stamped onto each of them so its pull request lands the moment
// it reads clean, with no separate review of the change itself, and
// beating whatever each item's template says about auto-merging on its
// own (docs/composition.md: the batch decides the disposition).
// The issue's own "by default they should auto queue and auto merge" is
// RequireApproval false and AutoMerge true, CreateSuite's own default.
type Suite struct {
	ID                   string
	Name                 string
	Items                []SuiteItem
	Mode                 SuiteMode
	Count                int // SuiteCount only; 1..MaxSuitePasses
	MaxPasses            int // SuiteUntilClean only; 1..MaxSuitePasses
	RequireApproval      bool
	AutoMerge            bool
	AutoApproveProposals bool
	CreatedAt            time.Time
}

// MaxSuitePasses is the most passes a suite may ask for, as either its
// Count or its MaxPasses. Each pass files one task per template, so
// without a cap a single "Add suite" could commit the deployment to
// thousands of sequential tasks (task 3213's walkthrough saved a count
// of 1000). SuiteOverlay.jsx mirrors it as the number boxes' max.
const MaxSuitePasses = 100

// Validate reports whether a suite's own fields make sense to run --
// checked once here, the same as QualificationPlan.Validate, rather
// than left for CreateSuiteRun to discover mid-way through filing a
// pass. It does not check that any item's TemplateID names a template
// that still exists -- that is a store lookup, left to
// ui.Client.CreateSuite/UpdateSuite and to CreateSuiteRun, which each
// resolve every item fresh at the moment they need its content.
func (s Suite) Validate() error {
	if len(s.Items) == 0 {
		return fmt.Errorf("a suite needs at least one template")
	}
	for _, it := range s.Items {
		if it.TemplateID == "" {
			return fmt.Errorf("a suite item needs a template")
		}
	}
	switch s.Mode {
	case SuiteCount:
		if s.Count < 1 {
			return fmt.Errorf("count must be at least 1")
		}
		if s.Count > MaxSuitePasses {
			return fmt.Errorf("count must be at most %d", MaxSuitePasses)
		}
	case SuiteUntilClean:
		if s.MaxPasses < 1 {
			return fmt.Errorf("max passes must be at least 1")
		}
		if s.MaxPasses > MaxSuitePasses {
			return fmt.Errorf("max passes must be at most %d", MaxSuitePasses)
		}
	default:
		return fmt.Errorf("unknown suite mode %q", s.Mode)
	}
	return nil
}

// SuiteRunStatus is a run's own progress -- unlike
// QualificationRunStatus, not purely derived from its tasks on every
// read, since deciding whether to stop or fire another pass is a
// judgement SyncSuites itself makes once (was the last pass clean? has
// Count or MaxPasses been reached?) and records here.
type SuiteRunStatus string

const (
	// SuiteRunActive means a pass is still in flight, or the next one
	// is waiting on this cycle's reconciler to fire it.
	SuiteRunActive SuiteRunStatus = "active"
	// SuiteRunSucceeded means the run stopped on its own terms:
	// SuiteCount finished every pass, or SuiteUntilClean reached a pass
	// that opened no pull request and proposed no follow-up task.
	SuiteRunSucceeded SuiteRunStatus = "succeeded"
	// SuiteRunFailed means a pass's task failed or closed without
	// completing, or (SuiteUntilClean only) MaxPasses ran out with no
	// pass ever reading clean.
	SuiteRunFailed SuiteRunStatus = "failed"
)

// SuiteTaskStatus is one task instance a run's pass instantiated, with
// enough of its current progress to tell whether that pass has finished
// and, once it has, whether it was clean -- QualificationTaskStatus's
// own shape, with PassNumber in place of InstanceIndex/Repeat since a
// suite run repeats whole passes rather than individual items.
type SuiteTaskStatus struct {
	TaskID       string
	TemplateID   string
	TemplateName string
	PassNumber   int
	Approved     bool
	State        State
	// OpenedPullRequest is this task's own LinkFixes -- true once its run
	// pushed a branch and grain opened (or found) a pull request for it.
	OpenedPullRequest bool
	// Proposed is true if this task's run proposed at least one
	// follow-up task (a LinkProposedBy pointing back at it) -- the
	// other half of what a SuiteUntilClean pass's own "clean" reading
	// is built from, alongside OpenedPullRequest.
	Proposed bool
}

// SuiteRun is one run of a suite against a repo and branch --
// bwsalmon/agents#642's own "run the template against a repo and
// branch." Every field a run's own behaviour depends on (Items, Mode,
// Count, MaxPasses, RequireApproval, AutoMerge) is snapshotted from the
// suite at creation, not read live from it, so editing a suite never
// changes how a run already in flight behaves.
type SuiteRun struct {
	ID        int64
	SuiteID   string
	SuiteName string // snapshot, QualificationTaskStatus.TemplateName's own reasoning
	// ScheduleID is the Schedule (schedule.go) whose firing started this
	// run, or empty for a run a human started by hand. It is both how a
	// run says where it came from and, through
	// Store.HasActiveRunForSchedule, the idempotency check that keeps a
	// schedule from starting a second run on top of one still in flight
	// -- the firing tag every task-filing schedule already carries, in
	// the form a run can wear.
	ScheduleID string
	Target     RepoRef
	// Base is the branch every task this run files targets, and
	// (through AutoMerge) the branch every one of them lands back on --
	// bwsalmon/agents#642's own "tasks created from the suite should
	// stack against the source branch": every task's pull request bases
	// off Base, and once it merges the next task to run (the next item
	// in this pass, or the first item of the next pass) sees that
	// change simply by cloning Base's own current tip -- the same
	// stacking trick the merge queue's own fix tasks used to play with
	// one task's own branch as Base, applied here with a fixed Base every
	// task in the run shares instead.
	Base                 string
	Items                []SuiteItem
	Mode                 SuiteMode
	Count                int
	MaxPasses            int
	RequireApproval      bool
	AutoMerge            bool
	AutoApproveProposals bool
	Status               SuiteRunStatus
	LastError            string
	CreatedAt            time.Time
	CompletedAt          *time.Time
	// Tasks is every pass's own instances, oldest first -- PassNumber
	// groups them, the same way QualificationRun.Tasks groups by
	// InstanceIndex.
	Tasks []SuiteTaskStatus
}

// CurrentPass returns run's own highest PassNumber, or 0 if it has no
// tasks yet -- unreachable once created, since CreateSuiteRun always
// files a first pass, but a safe zero value all the same.
func (r SuiteRun) CurrentPass() int {
	max := 0
	for _, t := range r.Tasks {
		if t.PassNumber > max {
			max = t.PassNumber
		}
	}
	return max
}

// PassTasks returns the tasks belonging to pass n only.
func (r SuiteRun) PassTasks(n int) []SuiteTaskStatus {
	var out []SuiteTaskStatus
	for _, t := range r.Tasks {
		if t.PassNumber == n {
			out = append(out, t)
		}
	}
	return out
}

// PassOutcome is what SyncSuites reads off a pass's own tasks to decide
// what to do next.
type PassOutcome int

const (
	// PassPending means at least one task in the pass has not yet reached
	// a terminal state -- includes a task RequireApproval left
	// unapproved, since an unapproved task never leaves StateProposed on
	// its own.
	PassPending PassOutcome = iota
	// PassFailed means every task reached a terminal state, and at least
	// one of them failed or closed without completing.
	PassFailed
	// PassClean means every task completed, and none opened a pull
	// request or proposed a follow-up task.
	PassClean
	// PassChanged means every task completed, but at least one opened a
	// pull request or proposed a follow-up task.
	PassChanged
)

// OutcomeOfPass reduces one pass's own task instances to what
// SyncSuites needs to decide whether to stop or fire another pass. A
// failure found anywhere outranks everything else, the same precedence
// QualificationStatus gives anyFailed over allCompleted -- a straggler
// elsewhere does not hide a failure already known, and nothing here
// waits for every instance to settle before reporting one.
func OutcomeOfPass(tasks []SuiteTaskStatus) PassOutcome {
	changed, anyFailed, anyPending := false, false, false
	for _, t := range tasks {
		switch t.State {
		// StateAwaitingSubmit and StateAwaitingReply count as completed here
		// for the reason QualificationStatus gives: a pass measures
		// whether its tasks ran and what they produced, and a pull request
		// nobody has submitted yet is a pull request all the same --
		// OpenedPullRequest below is exactly the case that makes a task
		// read that way. A task that answered in a comment is the other
		// half of that: it ran, it produced no pull request, and PassClean
		// below is the outcome that says so.
		case StateCompleted, StateAwaitingSubmit, StateAwaitingReply:
			if t.OpenedPullRequest || t.Proposed {
				changed = true
			}
		case StateFailed, StateClosed:
			anyFailed = true
		default:
			anyPending = true
		}
	}
	switch {
	case anyFailed:
		// Ahead of anyPending deliberately, and the reason this loop
		// records a straggler rather than returning on the first one it
		// sees: a pass fires every item at once, so a failure in one
		// item and a second item still running is the ordinary shape of
		// a pass going wrong. Reporting PassPending there would leave
		// the run active -- firing further passes, in SuiteCount mode
		// -- on a pass already known to have failed.
		return PassFailed
	case anyPending:
		return PassPending
	case changed:
		return PassChanged
	}
	return PassClean
}
