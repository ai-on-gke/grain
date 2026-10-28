package model

import "time"

// Template is a reusable declaration of a task's content -- the same
// "what to do" fields Task and Schedule already carry, saved once under
// a name so more than one place can point at it instead of repeating
// the same title, body, and grants by hand (bwsalmon/agents#516). A
// schedule is the first caller (Schedule.TemplateID); nothing about
// this type itself knows what a schedule is, the same way Task itself
// knows nothing about Schedule -- more callers are expected to arrive
// later without this type changing shape for them.
//
// Target and Base are optional (grain/task-285): left unset, which repo
// and branch a firing targets is a property of the use -- a schedule's
// own standing repo, or the repo and branch a suite run or qualification
// candidate is running against -- and every caller supplies its own,
// exactly as it did while a template could carry none at all. Set, they
// bind this template to one repo (and, if Base is set too, one branch):
// content that only makes sense against that repo travels with it, and
// no caller has to name the repo a second time or can name a different
// one by mistake.
//
// Unlike Task, there is no Intent and no Origin here: those describe one
// filing, not a reusable declaration meant to be filed more than once by
// more than one caller, each of which decides its own origin and intent
// (fireTaskSchedule, for a schedule, always files IntentImplement
// attributed to SchedulePrincipal -- a template does not override
// either).
type Template struct {
	ID string
	// Name is how a human picks this template out of a list -- shown in
	// the template picker itself, never copied onto a filed Task the way
	// Title is.
	Name string

	Title string
	Body  string

	// Fields is the inputs this template asks for when somebody
	// instantiates it (TemplateField, grain/task-58) -- the named holes
	// its Title and Body leave as `{{name}}`, each one either a text box
	// or a set of options to pick from. Empty, which most templates are,
	// means the content is the content: nothing to fill in, and
	// `{{anything}}` in the prose is prose.
	//
	// Declared in the order a form should ask for them, which is the
	// order they are stored and read back in -- "environment, then
	// version" is how somebody wrote the form down and alphabetical
	// order is not an improvement on it.
	Fields []TemplateField

	// Target, when non-nil, is the repo every task filed from this
	// template targets, whoever fires it and whatever repo that caller
	// otherwise works against -- a schedule's Target, a suite run's
	// Target. nil means unbound: the caller's own target decides, which
	// is how every template behaved before binding existed and still the
	// default for a template created without one.
	//
	// A qualification plan is the one caller that does not let a binding
	// move a firing: a qualification run exists to test one candidate's
	// own branch, so its tasks always target the candidate's repo and
	// branch, and a bound template is instead only allowed into a plan
	// for the repo it is bound to (CreateQualificationRun,
	// ui.Client.PutQualificationPlan).
	Target *RepoRef
	// ChangeTarget, when non-nil, binds every task filed from this template
	// to a non-git change target (such as a registered changelist workspace), with
	// Target == nil and Base == "".
	ChangeTarget *ChangeTarget
	// Base is the branch a bound template's firings target. Only
	// meaningful alongside Target -- an unbound template's firings take
	// the caller's own base -- and optional even then: empty means the
	// target repo's own default branch, the same as an empty Base
	// anywhere else (Task.Base). So a template can bind a repo without
	// pinning a branch, which is what most bound templates want.
	Base string

	// CustomFields is the values every task filed from this template
	// carries for the fields this deployment declares (CustomField),
	// keyed by field name -- content like Reads and Grants, copied onto
	// a firing exactly as they are, so a template for "the weekly
	// dependency bump" can say `team: infra` once rather than leaving
	// every task it files blank in a column the deployment sorts by
	// (grain/task-105).
	//
	// Checked strictly when the template is saved (ui.CreateTemplate,
	// ui.UpdateTemplate, through NormalizeCustomFieldValues) so a value
	// that could never be set is refused to the human writing it, and
	// again, leniently, at each firing (FilterCustomFieldValues): a
	// field deleted from the state repository since must not stop this
	// template from filing anything, so its value is simply dropped from
	// that firing.
	CustomFields map[string][]string

	// NetworkAccess is how much network every task filed from this
	// template gets (Task.NetworkAccess, grain/task-269) -- content like
	// Grants, copied onto a firing exactly as it is. It is here because
	// "this job has no business reaching the internet" is a property of
	// the work rather than of one filing of it: a template says it once
	// and every task it ever files carries it, which is what stops a
	// standing job's isolation depending on who fired it.
	//
	// Empty, the default, means the same as it does on a task: no
	// override, take the deployment's own answer (Config.NetworkAccess).
	NetworkAccess NetworkAccess

	// NothingNeededAction is what a run ending on nothing_needed does to
	// every task filed from this template (Task.NothingNeededAction) --
	// content like NetworkAccess, copied onto a firing exactly as it is,
	// so a standing sweep that usually finds nothing to do can say "ack"
	// once rather than at each filing. Empty, the default, means close.
	NothingNeededAction NothingNeededAction
	// Goal is the goal every task filed from this template is declared
	// with (TaskGoal, docs/design/durable-tasks.md#DT-4); nil for none.
	Goal *GoalDecl

	// Model is the model every task filed from this template asks for
	// (Task.Model, docs/connections-and-models.md) -- content like
	// NetworkAccess, copied onto a firing exactly as it is. Empty, the
	// default, means nobody chose one: the task runs on its team's
	// default model.
	Model string

	Reads []RepoRef
	// Writes is the extra write targets every task filed from this
	// template carries (Task.Writes, WriteTarget's own doc comment):
	// repositories that task's run may have work landed in besides the
	// one it is filed against, each with the base branch for it. Content
	// like Reads and Grants, and copied onto a firing exactly as they
	// are -- which is the whole point of it being here, since a
	// recurring job spanning two repos would otherwise have to be filed
	// by hand every time.
	//
	// Unrelated to Target above: Target is the one repo a firing is
	// filed *against* when this template is bound, and an entry here is
	// a repo whose change that firing proposes a further task for. A
	// firing whose target ends up being one of these drops the redundant
	// entry (WritesBesides) rather than declaring the same repo twice.
	Writes []WriteTarget
	Grants []Grant

	AutoMerge bool

	// Interactive files every task from this template as a live chat
	// (Task.Interactive): its agent gets the chat tool and talks with
	// whoever is watching the task while it works, rather than running
	// unattended. Content like AutoMerge, copied onto a firing exactly as
	// it is -- including a schedule's, whose task parks on its first chat
	// until somebody replies, the same as any other interactive task.
	Interactive bool

	CreatedAt time.Time
}

// FiringTarget is the repo and branch a task filed from this template
// targets, given the target and base the caller would otherwise have
// used: the binding when there is one, the caller's own when there is
// not. Every caller that turns a template into a Task goes through here
// (orchestrator.fireTaskSchedule, fireSuitePass, ui's own preview of
// what a template-backed schedule will fire) so the rule is written once
// rather than re-derived, subtly differently, at each of them.
//
// A bound template's own empty Base is passed straight through rather
// than falling back to the caller's: a caller's base names a branch of
// the repo *it* was going to target, which a binding has just replaced,
// so carrying it over would be a branch name from one repo applied to
// another. Empty means the target repo's default branch, which is
// exactly what a binding with no branch asks for.
func (t Template) FiringTarget(target RepoRef, base string) (RepoRef, string) {
	if t.Target == nil {
		return target, base
	}
	return *t.Target, t.Base
}
