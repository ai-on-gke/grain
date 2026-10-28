package model

import (
	"fmt"
	"github.com/ai-on-gke/grain/pkg/workflows"
	"time"
)

// OverlapPolicy is what a schedule does when it comes due while its own
// previous firing has not finished yet -- the rule docs/schedules.md
// records as v1's ("a previous, unfinished firing suppresses the next
// one"), now a per-schedule choice rather than the only behaviour there
// is.
//
// Skipping is the default, and the zero value says so: a chore that runs
// long must not pile a second copy of itself on top of the first, which
// is what an unattended queue does with no gate at all. A schedule whose
// firings genuinely do not collide -- one whose task waits on a human's
// reply for days, say, while the cadence is what actually matters -- can
// say so with OverlapAllow instead.
//
// What counts as "unfinished" is whatever that kind of schedule's firing
// *is*: an open task for one that files a task, an unfinished run for one
// that runs a suite, and -- for one translated out of a workflow's cron --
// a CI run that has not ended or a review of it nobody has read yet
// (Store.workflowFiringInFlight). Both halves, deliberately: skipping
// while the previous firing is unfinished is the setting that stops a
// nightly stacking on a morning that is still being read.
type OverlapPolicy string

const (
	// OverlapSkip does not fire while a previous firing of the same
	// schedule is still unfinished: the firing is dropped, NextRunAt is
	// left exactly where it was, and the next cycle asks the same
	// question again -- so it is delayed rather than skipped outright,
	// and picks up as soon as the previous one closes.
	OverlapSkip OverlapPolicy = "skip"
	// OverlapAllow fires regardless, so a schedule can have several
	// unfinished firings at once.
	OverlapAllow OverlapPolicy = "allow"
)

// Allows reports whether p lets a firing go ahead while the previous one
// is still unfinished. Everything that is not OverlapAllow skips,
// including the zero value: a schedule written before this field existed
// (or by a caller that never sets it) keeps the behaviour it always had.
func (p OverlapPolicy) Allows() bool { return p == OverlapAllow }

// Validate rejects a policy this package does not know -- checked at
// creation/update (ui.parseOverlap) the same way Recurrence.Validate is,
// so nothing further in has to handle an unrecognized value. The zero
// value is accepted and means OverlapSkip.
func (p OverlapPolicy) Validate() error {
	switch p {
	case "", OverlapSkip, OverlapAllow:
		return nil
	default:
		return fmt.Errorf("unknown overlap policy %q", string(p))
	}
}

// Schedule is what a human sets up once and grain refiles as a real
// Task on a fixed cadence -- bwsalmon/agents#376's v2 equivalent of
// v1's scheduled_jobs.py, with one deliberate difference: there is no
// template directory here, because the whole point of this package's
// own shape (README, "Input is a model update, not a GitHub issue") is
// that a schedule is a store row a UI can create, edit and delete, not
// a file an operator drops on a server's disk. ("Template" here used to
// describe this type itself, before Template (bwsalmon/agents#516,
// template.go) existed to name the reusable half of a schedule's own
// content -- TemplateID below is what points at one.)
//
// docs/data-model.md's "the SCHEDULED special case dissolves" is why
// firing carries no approval flag of its own: creating a schedule is
// itself the standing human approval, so every task it later files lands
// already approved (orchestrator.fireTaskSchedule sets Approval
// unconditionally) -- the actor rule (LandsQueued) still holds
// unqualified, because the automation that actually creates the firing is
// never what decided to trust it.
//
// Reads and Grants mirror Task's own fields (bwsalmon/agents#464): a
// firing carries whatever read-only repos and capabilities the schedule
// itself was given, the same as a task filed by hand. DependsOn and
// Approved have no equivalent here, and deliberately so -- a one-shot
// dependency link makes no sense against a task this schedule refiles
// indefinitely, and CreateSchedule's own doc comment already explains why
// approval is not a per-firing choice.
//
// SuiteID is the one field that changes *what* a firing is: set, this
// schedule starts a SuiteRun instead of filing a single Task. Every
// other field here means the same thing either way -- the cadence, the
// enabled switch, the target repo and base branch, and Overlap's "does a
// previous firing that has not finished suppress the next one" rule are
// the schedule's, not the task's.
type Schedule struct {
	ID    string
	Title string
	Body  string

	Target               RepoRef
	Base                 string
	AutoMerge            bool
	AutoApproveProposals bool
	Reads                []RepoRef
	// Writes is the extra write targets every task this schedule files
	// carries (Task.Writes) -- repositories that task may have work
	// landed in besides Target, each with its own base branch. Content
	// alongside Reads and Grants, and taken from the template instead
	// while TemplateID is set, exactly as they are.
	Writes []WriteTarget
	Grants []Grant

	// NetworkAccess is how much network every task this schedule files
	// gets (Task.NetworkAccess, grain/task-269) -- content alongside
	// Reads, Writes and Grants, and taken from the template instead
	// while TemplateID is set, exactly as they are.
	//
	// It is here (grain/task-427) because an inline schedule is a task
	// on a cadence, and it already carries the whole of what a task may
	// touch -- the repos it reads, the repos it may have work landed in,
	// the capabilities it holds. The network was the one member of that
	// list a schedule could not say anything about, so the only way to
	// keep a recurring job off the internet was to move its content into
	// a Template (Template.NetworkAccess) and point this schedule at it.
	// That put the cost on the restrictive answer and left the permissive
	// one free, which is the wrong way round for an isolation setting.
	//
	// Empty, the default, means the same as it does on a task and on a
	// template: no override, take the deployment's own answer
	// (Config.NetworkAccess).
	NetworkAccess NetworkAccess

	// NothingNeededAction is what a run ending on nothing_needed does to
	// every task this schedule files (Task.NothingNeededAction) --
	// content alongside NetworkAccess, and taken from the template
	// instead while TemplateID is set, exactly as it is. A recurring
	// sweep is the filer most likely to find nothing to do, so this is
	// where "ack" (or an explicit "close") is most often wanted. Empty,
	// the default, means close.
	NothingNeededAction NothingNeededAction

	// Model is the model every task this schedule files asks for
	// (Task.Model, docs/connections-and-models.md). Empty means this
	// schedule chose none: the template's model when it fires one
	// (Template.Model), and otherwise the team's default.
	//
	// Not content in the sense NetworkAccess is: a schedule firing a
	// template keeps its own Model rather than taking the template's, and
	// a model it sets beats the template's (ComposeScheduleTask). Which
	// model a recurring job runs on is a choice about that cadence, and a
	// declared schedule (.grain/schedules), which carries no content of
	// its own, has no other place to make it.
	Model string

	// WorkflowReview is when a schedule that runs a workflow files a
	// review of the run (WorkflowReviewPolicy): empty or "on-failure" for
	// only a run that did not pass, "always" for every one. Meaningless
	// on any other schedule, and set only by a declaration -- a
	// workflow's own `on: schedule` entry (`review:`) or a
	// `.grain/schedules` file with `workflow:` -- since those are the
	// only schedules that run a workflow.
	WorkflowReview WorkflowReviewPolicy

	// CustomFields is the values every task this schedule files carries
	// for the fields this deployment declares (CustomField), keyed by
	// field name -- content alongside Reads, Writes and Grants, and
	// taken from the template instead while TemplateID is set, exactly
	// as they are.
	//
	// It is why grain/task-105 exists: a deployment whose tasks are
	// sorted and filtered by a declared field (grain/task-64) had a
	// column that only a human at a form could ever fill in, so the
	// recurring work grain files for itself was blank in it. A schedule
	// that files "weekly dependency bump" can now say `team: infra` on
	// every firing.
	//
	// Applied at firing time rather than resolved once
	// (orchestrator.fireTaskSchedule), the same as every other content
	// field here: editing the schedule changes what it files next, and
	// nothing has to walk the tasks it has already filed. A value whose
	// field has since been deleted from the state repository is dropped
	// from that firing rather than failing it (FilterCustomFieldValues)
	// -- the alternative is a chore that silently stops running because
	// of a declaration somebody retired.
	CustomFields map[string][]string

	// TemplateID, if set, names the Template (template.go) this
	// schedule fires from instead of its own
	// Title/Body/AutoMerge/Reads/Grants/NetworkAccess/NothingNeededAction/CustomFields above
	// (bwsalmon/agents#516). Target and Base are this schedule's own,
	// unless that template is bound to a repo of its own, in which case
	// the binding decides both (Template.Target, docs/composition.md's
	// own table of which layer decides what). TemplateID is resolved
	// fresh at firing time (orchestrator.fireTaskSchedule), not copied
	// in once, so editing the template changes what every schedule
	// pointing at it files next -- the same way editing this schedule's
	// own fields already changes what it itself files next. The fields
	// above still hold a value even when this is set: fireTaskSchedule
	// keeps them in sync as a display cache each time it fires, so
	// ui.scheduleFrom can render a schedule's effective title and the
	// rest with no extra lookup, but while TemplateID is set they are
	// not what decides what gets filed.
	TemplateID *string

	// SuiteID, if set, names the Suite (suite.go) this schedule runs on
	// its cadence instead of filing a single task: every firing is one
	// CreateScheduledSuiteRun against this schedule's own Target and
	// Base, the same run a human starts by hand from the suites page,
	// started by the clock instead. Mutually exclusive with TemplateID
	// -- a suite already resolves its own items' templates, so there is
	// no second template for a firing to also carry -- and, like it,
	// resolved fresh at firing time (orchestrator.fireSuiteSchedule),
	// never copied in once, so editing the suite changes what every
	// schedule pointing at it runs next. Title is kept in sync with the
	// suite's own Name as a display cache while this is set, exactly as
	// the content fields above are for TemplateID;
	// Body/AutoMerge/Reads/Grants/CustomFields are unused, since a suite run takes
	// all of those from the suite and its templates.
	//
	// What a schedule fires -- a task or a suite -- is fixed when it is
	// created: ui.UpdateSchedule will repoint a suite-backed schedule at
	// a different suite, but never turn one kind into the other, since
	// there is nothing sensible to carry across (a suite has no title or
	// body of its own to become a task's, and a task's content has
	// nowhere to go in a suite).
	SuiteID *string

	// Recurrence is how often this schedule fires.
	Recurrence Recurrence
	// TimeZone is the optional IANA time zone this schedule's recurrence is
	// evaluated against (e.g. "America/New_York"). When empty, UTC is used.
	TimeZone string
	// Overlap is what this schedule does when it comes due while its own
	// previous firing is still unfinished -- OverlapPolicy's own doc
	// comment has the two answers and why skipping is the default one.
	// It reads the same question either way a schedule can fire it: an
	// unclosed task carrying the schedule's firing tag for one that
	// files a task, an unfinished run for one that runs a suite.
	Overlap OverlapPolicy
	// Enabled is the pause/resume switch a UI toggles -- a disabled
	// schedule stays around (with its own history in NextRunAt/LastRunAt)
	// rather than being deleted, the same as closing a task keeps its row
	// rather than erasing it.
	Enabled bool
	// NextRunAt is when this schedule next comes due. Set to the first
	// occurrence of Recurrence after the moment it is created
	// (ui.CreateSchedule), so a fresh schedule waits for its own cadence
	// rather than firing once on the very next cycle: a schedule is a
	// human saying when something should run, and creating one is not
	// itself a request to run it now. Retimed the same way when the
	// cadence itself is edited (ui.UpdateSchedule), and otherwise moved
	// only by orchestrator.advanceSchedule as the schedule fires.
	NextRunAt time.Time
	// LastRunAt is when this schedule last actually filed a task, nil if
	// it never has.
	LastRunAt *time.Time

	CreatedAt time.Time
}

// Location resolves the schedule's explicit time zone, falling back to its
// recurrence's time zone, or time.UTC.
func (s Schedule) Location() *time.Location {
	if s.TimeZone != "" {
		return LoadLocation(s.TimeZone)
	}
	if s.Recurrence.TimeZone != "" {
		return LoadLocation(s.Recurrence.TimeZone)
	}
	return time.UTC
}

// Recurrence and WorkflowReviewPolicy moved to pkg/workflows with the
// workflow parser that produces them from a cron line, so that it does
// not import this package (docs/design/grit-split.md, GS-2).
type (
	RecurrenceKind       = workflows.RecurrenceKind
	Recurrence           = workflows.Recurrence
	WorkflowReviewPolicy = workflows.ReviewPolicy
)

const (
	RecurrenceEveryNHours   = workflows.RecurrenceEveryNHours
	RecurrenceEveryNMinutes = workflows.RecurrenceEveryNMinutes
	RecurrenceDaily         = workflows.RecurrenceDaily
	RecurrenceWeekly        = workflows.RecurrenceWeekly
	RecurrenceMonthly       = workflows.RecurrenceMonthly
	MaxEveryNHours          = workflows.MaxEveryNHours
	MinEveryNMinutes        = workflows.MinEveryNMinutes
	MaxEveryNMinutes        = workflows.MaxEveryNMinutes

	WorkflowReviewOnFailure = workflows.ReviewOnFailure
	WorkflowReviewAlways    = workflows.ReviewAlways
)
