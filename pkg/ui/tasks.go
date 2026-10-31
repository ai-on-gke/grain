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

package ui

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/apierror"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/installtrack"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/version"
)

// SupersedeKeptEntry is one model.KeptDependent on the wire.
type SupersedeKeptEntry struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// Task is a task's JSON shape -- everything the frontend needs to list
// and render one.
//
// ID is a string, not a number. It used to be the GitHub issue number a
// task was named after; it is now whatever Store.NewTaskID allocated,
// which happens to be decimal today and is opaque to everything here.
//
// Three fields the issue-backed shape carried are gone: htmlUrl and
// githubState, because there is no issue to link to or mirror the open/
// closed flag of, and labels, because state and capabilities are no
// longer derived from any. PullRequest replaces htmlUrl as the one thing
// worth linking to on GitHub, read off the task's own LinkFixes link.
// GeneratedFrom is the same read off LinkProposedBy: the ID of the task
// whose propose_task call filed this one, provenance only, empty for a
// task nobody proposed.
type Task struct {
	// Goal is the task's goal and grain's progress towards it
	// (model.TaskGoal, docs/design/durable-tasks.md#DT-4); filled on a
	// single task's read, nil in a list and for a task with none.
	Goal        *model.TaskGoal `json:"goal,omitempty"`
	ID          string          `json:"id"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	Author      string          `json:"author"`
	// AuthorKind is that author's model.PrincipalKind -- "human",
	// "agent" or "automation", Comment.AuthorKind's own vocabulary for
	// the same question about a comment. Author alone cannot answer it:
	// an ID is a GitHub login for a person, a run ID for an agent and a
	// deployment name for automation, and nothing in the string says
	// which. The frontend needs the distinction to keep a task nobody
	// filed by hand from steering a human's own defaults -- see
	// state.js's lastBaseForRepo.
	AuthorKind string      `json:"authorKind,omitempty"`
	State      model.State `json:"state"`
	Repo       string      `json:"repo,omitempty"`
	Branch     string      `json:"branch,omitempty"`
	// Reads is every repo this task's run may read but never push to --
	// model.Task.Reads, rendered as owner/name strings the same way Repo
	// renders its single Target.
	Reads []string `json:"reads,omitempty"`
	// Writes is every *other* repo this task's work may land in, with
	// the base branch for each -- model.Task.Writes. Repo and Base above
	// are still the one repo and branch this task's own runs push to;
	// these are the ones its runs reach by proposing a task
	// (model.WriteTarget), and each carries its own branch because that
	// is half of what somebody chose when they added it.
	//
	// Objects rather than owner/name strings like Reads, for that
	// reason: a repo here without its branch is a declaration with a
	// piece missing.
	Writes               []WriteTarget `json:"writes,omitempty"`
	Base                 string        `json:"base,omitempty"`
	AutoMerge            bool          `json:"autoMerge"`
	AutoApproveProposals bool          `json:"autoApproveProposals"`
	// Interactive mirrors model.Task's own field of the same name
	// (bwsalmon/agents#539) -- true for a task filed for a live chat
	// rather than handed off to run unattended, which is what tells the
	// frontend to open its chat view straight after creating it and to
	// label its Timeline as a conversation rather than a task history.
	Interactive bool `json:"interactive"`
	// ConsoleSession is the standing session this task holds the
	// conversation for, or "" for the ordinary task that is not one
	// (model.ConsoleSessionIDOf, grain/task-415).
	//
	// It is on every task rather than only reachable through
	// GET /api/console-sessions because the frontend needs it about
	// tasks it already has: a session is never finished, so it is parked
	// on its own conversation between exchanges, and a permanently
	// parked task would otherwise sit in the inbox forever asking to be
	// replied to (state.js's waitsOnUser). The chat in the rail is where
	// a session is answered, and this is what says which task that is.
	ConsoleSession string `json:"consoleSession,omitempty"`
	// SandboxCPUs, SandboxMemoryMB and SandboxDiskGB
	// (bwsalmon/agents#534, grain/task-41) override the
	// deployment's default sandbox shape for this task's own dispatch
	// alone -- model.Task's own fields of the same name. Zero (the
	// default for all three) means "use the deployment default", omitted
	// from the JSON response the same way Base's own empty string is,
	// since "no override" is the common case and worth not cluttering
	// every task response with.
	SandboxCPUs     int `json:"sandboxCpus,omitempty"`
	SandboxMemoryMB int `json:"sandboxMemoryMb,omitempty"`
	SandboxDiskGB   int `json:"sandboxDiskGb,omitempty"`
	// AgentFramework is this task's own override of the deployment's
	// agent framework -- model.Task's own field of the same name.
	// Omitted, like the three above, for the common "no override" case, so
	// a frontend reading it back gets the same empty-means-default it
	// sent.
	AgentFramework string `json:"agentFramework,omitempty"`
	// AgentBackendID is the one model this task was pinned to --
	// model.Task's own field of the same name (grain/task-110) -- or
	// absent for a task dispatched through the rotation, which is most of
	// them. Omitted for that case like the overrides above, so a frontend
	// reading it back gets the same empty-means-the-rotation it sent.
	AgentBackendID string `json:"agentBackendId,omitempty"`
	// Model is the model this task chose -- model.Task's own field of the
	// same name -- or absent for one running on its team's default.
	Model string `json:"model,omitempty"`
	// ModelSource is which declaration chose Model -- "template" or
	// "schedule", model.Task.ModelSource -- or absent when the task chose
	// it itself, or chose none and runs on its team's default.
	ModelSource string `json:"modelSource,omitempty"`
	// NetworkAccess is this task's own override of how much of the
	// internet its sandbox may reach -- model.Task's own field of the
	// same name (grain/task-269). Omitted, like the overrides above, for
	// the common "no override" case, so a frontend reading it back gets
	// the same empty-means-default it sent.
	NetworkAccess string `json:"networkAccess,omitempty"`
	// NothingNeededAction is model.Task's own field of the same name:
	// what a run ending on nothing_needed does to this task, "close" or
	// "ack", with "" meaning close.
	NothingNeededAction string `json:"nothingNeededAction,omitempty"`
	// NoPort is model.Task.NoPort: this task's change is not ported to
	// its repository's other paired branches.
	NoPort bool `json:"noPort,omitempty"`
	// FixesBaseBranch is model.FixesBaseBranch: this task is a repair of
	// its base branch -- grain's own, or one a person marked -- and is
	// never made to wait on that branch's repair. GrainBaseRepair says
	// which of the two (model.GrainBaseRepair): grain's own cannot be
	// unmarked.
	FixesBaseBranch  bool   `json:"fixesBaseBranch,omitempty"`
	GrainBaseRepair  bool   `json:"grainBaseRepair,omitempty"`
	RedBaseBranch    string `json:"redBaseBranch,omitempty"`
	BaseRepairBypass string `json:"baseRepairBypass,omitempty"`
	// PromptExtension is this task's own override of the standing
	// instructions its deployment and its repo would otherwise give its
	// run -- model.Task's own field of the same name (grain/task-114).
	// Omitted, like the fields above, for the common "no override" case,
	// so a frontend reading it back gets the same empty-means-default it
	// sent.
	PromptExtension string `json:"promptExtension,omitempty"`
	// ReviewTemplateID is the template a review of this task's own work
	// is filed from once it is done -- model.Task.ReviewTemplateID.
	// Omitted, like the overrides above, for the common case of a task
	// with no review attached.
	//
	// ReviewTemplateName is that template's own Name, resolved by the
	// caller (Client.ListTasks reads every template once for a whole
	// list; Client.Task reads the one), so a task pane can say which
	// review is attached without the frontend having to hold the whole
	// template list to look an id up in. Empty when the id names a
	// template that is no longer there, which is the one thing worth
	// telling a reader apart from "no review".
	ReviewTemplateID   string `json:"reviewTemplateId,omitempty"`
	ReviewTemplateName string `json:"reviewTemplateName,omitempty"`
	Creator            string `json:"creator,omitempty"`
	// ReviewTask is the ID of the task actually filed to review this one
	// (model.LinkReviewTask), empty until orchestrator.SyncReviews has
	// filed it. Paired with ReviewTemplateID it is what tells a reader
	// whether the review is still owed or already in flight -- and it is
	// what a task pane links to.
	ReviewTask string `json:"reviewTask,omitempty"`
	// SecurityHold is the trust-boundary list version this task's pull
	// request was put on a security hold under (model.LinkSecurityHold),
	// empty for every task not on one -- including a change touching a
	// path only a human may release, which never carries the link and
	// simply waits for Submit. SecurityReviewTask is the ID of the
	// security review grain filed for the change
	// (model.LinkSecurityReviewTask), which outlives the hold: it is still
	// the review a person reads before pressing Submit once the hold has
	// ended on findings, or never existed because a human must decide.
	// Together they are what lets a task pane say why a change that
	// looks like any other awaiting-submit task is not merging itself,
	// and link to the review that decides it.
	SecurityHold       string `json:"securityHold,omitempty"`
	SecurityReviewTask string `json:"securityReviewTask,omitempty"`
	// Approvals is each rule this task's change waits on a named
	// approver's Submit for -- a sensitive trigger with `human:
	// required`, or grain's own guard over the repository's triggers --
	// who may give it, and who did (approvals.go). RepoRules is the
	// identity of the repository's rules a security hold was decided
	// under (model.LinkRepoRules), beside SecurityHold's list version.
	Approvals []ApprovalView `json:"approvals,omitempty"`
	RepoRules string         `json:"repoRules,omitempty"`
	// PathTriggerHold is the strictest `human` setting of the gating
	// triggers holding this change (model.LinkPathTriggerHold), and
	// PathTriggerTasks the tasks they filed, as "<trigger>=<task>".
	PathTriggerHold  string   `json:"pathTriggerHold,omitempty"`
	PathTriggerTasks []string `json:"pathTriggerTasks,omitempty"`
	// ReviewQuestions is the review task whose open questions this change
	// waits on its owner's decision about (model.LinkReviewQuestions):
	// a reply sends another run to carry the decision out, and Submit
	// ships it as is.
	ReviewQuestions string `json:"reviewQuestions,omitempty"`
	// MergePolicy is the effective policy the repository's merge rules
	// gave this change's head (model.LinkMergePolicy, grain/task-1659),
	// with the rule each requirement came from.
	MergePolicy  *model.EffectiveMergePolicy `json:"mergePolicy,omitempty"`
	Capabilities []string                    `json:"capabilities"`
	// CustomFields is this task's values for the fields this deployment
	// declares (model.CustomField), keyed by field name -- omitted for
	// the ordinary task that says nothing about any of them.
	//
	// The values only; what the fields *are* (their labels, types and
	// options) rides on GET /api/config once for the whole page rather
	// than on every task in a list of two hundred. The frontend joins
	// the two by name, which is also what lets it render a value whose
	// field has since been deleted from the state repository as the bare
	// name-and-value it now is, rather than dropping it silently.
	//
	// One field's value is a string or an array of strings, since a
	// declaration may say the field holds a set (custom_fields.go's own
	// doc comment on which, and why the one-value case is still a bare
	// string).
	CustomFields  map[string]CustomFieldValue `json:"customFields,omitempty"`
	Issue         int                         `json:"issue,omitempty"`
	PullRequest   string                      `json:"pullRequest,omitempty"`
	GeneratedFrom string                      `json:"generatedFrom,omitempty"`
	// Origin is the pull request, issue or grain task this task's tree of
	// work came from (model.WorkOrigin): "owner/repo#12" or "task:1766",
	// empty for a task that has none.
	Origin string `json:"origin,omitempty"`
	// Continues is the ID of the task whose conversation this one
	// carries on, read off model.LinkContinues -- a standing console
	// session's closed chat, and its replacement. Provenance only, like
	// GeneratedFrom beside it: it blocks nothing, and it is empty for
	// every task that continues nothing.
	Continues string `json:"continues,omitempty"`
	// Supersedes is the ID of the task this one was filed to replace,
	// and SupersededBy the ID of the task that replaced this one --
	// model.LinkSupersedes and model.LinkSupersededBy, the two halves of
	// one relation written on both tasks so either can be read without a
	// scan (grain/task-473).
	//
	// SupersededBy is the one that earns its place on the wire: a closed
	// task whose row says nothing is a task somebody has to reconstruct
	// the reason for, and "superseded by 471" is that reason in three
	// words. Supersedes is its other half, so the newest task in a chain
	// of revisions says what it grew out of.
	//
	// Provenance only, like Continues and GeneratedFrom beside them:
	// neither blocks anything, and both are empty for every task that
	// replaced nothing and was replaced by nothing.
	Supersedes   string `json:"supersedes,omitempty"`
	SupersededBy string `json:"supersededBy,omitempty"`
	// SupersedeMoved is the open dependents of the task this one
	// superseded that now wait on this one instead
	// (model.SupersedeResult.Moved), and SupersedeKept the ones left
	// waiting on the closed task, each with why. Filled in only on the
	// answer to the CreateTask call that did the supersede -- they are
	// what that call did rather than anything about the task -- so a
	// propose_task call can tell its run which tasks moved
	// (mcp.FiledProposal).
	SupersedeMoved []string             `json:"supersedeMoved,omitempty"`
	SupersedeKept  []SupersedeKeptEntry `json:"supersedeKept,omitempty"`
	// Reverts is the ID of the task this one was filed to revert, and
	// RevertedBy the ID of the task that reverted this one --
	// model.LinkReverts and model.LinkRevertedBy.
	Reverts    string `json:"reverts,omitempty"`
	RevertedBy string `json:"revertedBy,omitempty"`
	// PortOf is the ID of the task whose merged change this one ports
	// onto a paired branch, and PortedBy the IDs of the ports filed for
	// this one -- model.LinkPortOf and model.LinkPortedBy.
	PortOf   string   `json:"portOf,omitempty"`
	PortedBy []string `json:"portedBy,omitempty"`
	// GoogleChatSpace is the Google Chat space linked to this task
	// (LinkGoogleChat), or "" when unlinked.
	GoogleChatSpace string `json:"googleChatSpace,omitempty"`
	// Changelists are the changelists (CLs) that are this task's work, as
	// cl/<number> (model.LinkChangelist).
	Changelists []string `json:"changelists,omitempty"`
	// Presubmit is what grain last observed of the primary CL's checks
	// (change.LastObserved): the presubmit badge beside its cl/N chip.
	// Nil until the CL has been polled or its checks read.
	Presubmit *ChangePresubmit `json:"presubmit,omitempty"`
	// Review is the primary CL's review beside its chip, with its Mail
	// and Publish replies buttons (change_review.go). Nil until observed.
	CLReview *ChangeReview `json:"clReview,omitempty"`
	// CLSubmit is the first live CL's Submit and Sync buttons, with why
	// either is disabled (change_submit.go). Nil for a task with no live
	// CL.
	CLSubmit *ChangeSubmit `json:"clSubmit,omitempty"`
	// ChangeTargetKind is the kind of non-git change this task's work is
	// ("changelist"), or "" for a git task (model.ChangeTarget).
	ChangeTargetKind string `json:"changeTargetKind,omitempty"`
	// Reviewers is the person-set list of reviewer usernames for this task
	// (model.Task.Reviewers, docs/design/changelists.md §5).
	Reviewers []string `json:"reviewers,omitempty"`
	// Stacked is true for a task filed automatically onto another task's
	// own branch and merged straight back into it once green -- a repair
	// of a broken pull request (model.ReasonFix) or a review of one
	// (model.ReasonReview). Paired with GeneratedFrom, it is what tells
	// the frontend
	// to nest a task under the one that generated it (bwsalmon/agents#378)
	// rather than list it as a separate task: unlike an ordinary
	// propose_task child, a stacked task is not new work, just a
	// continuation of the same change.
	Stacked bool `json:"stacked,omitempty"`
	// Review is true for the half of Stacked that is a review of another
	// task's work rather than a repair of its pull request
	// (model.ReasonReview, grain/task-284). Both nest under the task
	// they came from, which is what Stacked says; only this says which
	// of the two a nested task is, and a reader wants to know -- "a
	// second agent read this change" and "the merge queue patched a red
	// build" are not the same event.
	Review bool `json:"review,omitempty"`
	// Scheduled is true for a task a schedule filed automatically
	// (model.ReasonSchedule) -- a UI badge, the same treatment Stacked
	// already gives model.ReasonFix, so a task that appeared with nobody
	// having filed it by hand reads as expected rather than as a mystery.
	Scheduled bool `json:"scheduled,omitempty"`
	// SuiteRun is true for a task a suite run filed automatically
	// (model.ReasonSuite, bwsalmon/agents#642) -- Scheduled's own badge
	// treatment, so a pass of tasks that appeared with nobody filing
	// them by hand reads as expected rather than as a mystery.
	SuiteRun bool `json:"suiteRun,omitempty"`
	// Imported is true for a task the GitHub issue importer filed
	// (model.ReasonImported): the row's "imported" chip, kept after the
	// task is approved so an imported issue stays identifiable. Origin
	// names the issue it came from.
	Imported bool `json:"imported,omitempty"`
	// AwaitingImportApproval is Imported while nobody has approved the
	// task -- its explicit "imported" status, standing in for
	// "proposed": the same Approve action takes it to the queue, and
	// until then nothing dispatches it.
	AwaitingImportApproval bool       `json:"awaitingImportApproval,omitempty"`
	CreatedAt              *time.Time `json:"createdAt,omitempty"`
	// StartedAt and EndedAt are model.TaskSpan on the wire: when this
	// task's first attempt began, and when it stopped being work in
	// progress (that type's own doc comment says which ending counts and
	// why). Both are absent for a task that has not got there yet -- a
	// proposal has neither, a running task has only the first -- which is
	// an answer rather than a gap.
	//
	// They ride on the list shape, alongside CreatedAt, for Activity's own
	// reason: the question they answer is asked of every task at once.
	// "When did this run, and how long was it waiting first?" is a
	// timeline over the whole backlog (ui/src/components/GanttChart.jsx),
	// and drawing it from TaskDetail would be one request per bar.
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	EndedAt    *time.Time `json:"endedAt,omitempty"`
	ClosedAt   *time.Time `json:"closedAt,omitempty"`
	PrMergedAt *time.Time `json:"prMergedAt,omitempty"`
	PrClosedAt *time.Time `json:"prClosedAt,omitempty"`
	// DeferredUntil is when a deferred task picks itself back up
	// (model.Task.DeferredUntil), absent for the deferral nobody put a
	// date on and for every task that is not deferred at all.
	//
	// It rides here rather than in TaskDetail because a list is where
	// "deferred until when?" is asked: the state alone says a task is
	// aside, and the date is what tells a reader whether it is coming
	// back on its own. DeferredAt has no counterpart here for the
	// opposite reason -- "put aside three days ago" is on the timeline
	// already (model.Transitions), and no list draws it.
	DeferredUntil *time.Time `json:"deferredUntil,omitempty"`
	// Approved is whether the task carries an approval (model.Task.
	// ApprovedAt), which its state alone does not say for a deferred
	// task: an approved one wakes onto the queue and an unapproved one
	// wakes back into proposed, and both read "deferred" until then.
	Approved bool `json:"approved,omitempty"`
	// HeldBy is the task whose propose_task call filed this one for a
	// person to approve when its own permission would have queued it
	// (model.HeldForApprovalTag), and "" for every other task. The row
	// says so while the task still waits, so the operator can tell a
	// proposal held on purpose from one that simply was not covered.
	HeldBy string `json:"heldBy,omitempty"`
	// ApprovalNote is, on the response to a proposal just filed, why it
	// is waiting for a person rather than queued -- propose_task relays it
	// to the run that filed it. Set by CreateTask only; empty on every
	// other read, like SupersedeMoved.
	ApprovalNote string `json:"approvalNote,omitempty"`
	// DependsOn is every task this one has declared a depends-on link to,
	// resolved or not -- the definition. Blocked and BlockedBy are the
	// signal: whether any of it (or a child-of link) is still open right
	// now, re-derived on every read rather than pinned at creation, per
	// docs/data-model.md's "blocked is not a state, it is derived from
	// links" -- see model.IsBlocked.
	DependsOn []string `json:"dependsOn,omitempty"`
	Blocked   bool     `json:"blocked"`
	// Parked is a blocked task whose last run parked it on those blockers
	// through wait_for_task (model.ParkedOutcome), which the page words as
	// "waiting on" rather than "blocked on". Set on a task's own detail.
	Parked    bool     `json:"parked,omitempty"`
	BlockedBy []string `json:"blockedBy,omitempty"`
	// RunNowAt is an outstanding "Run now" request
	// (model.Observation.RunNowAt, Client.RunNow): somebody has asked for
	// a run of this task and no cycle has started one yet. nil for every
	// other task, and nil again the moment the run it asked for starts,
	// since starting it is what spends the request.
	//
	// The pane reads it to say the click took, over the seconds before a
	// dispatch cycle picks the task up. The list reads it too: under a
	// pause (manual mode), or with no capacity free, a request can wait
	// many cycles, and a row that still said "waits for Run now" after
	// Run now was pressed on it would be telling the operator to press
	// it again (alerts.js's heldForRunNow).
	RunNowAt *time.Time `json:"runNowAt,omitempty"`
	// TurnStopped is true for an awaiting_reply task parked on the
	// comment a person posts by stopping its turn (Client.Stop) rather
	// than on a question its agent asked -- the "Stopped by you" case of
	// the UI's "Asked" state.
	TurnStopped bool `json:"turnStopped,omitempty"`
	// QuestionSummary is the one line the question an awaiting_reply task
	// is parked on is shown by -- the inbox row, the task list's status
	// line, a notification's title -- with the question itself behind an
	// expander (model.Comment.Summary). Derived from the question's first
	// sentence for one asked without a summary; "" when the task is not
	// parked on a question, or is parked on a stopped turn.
	QuestionSummary string `json:"questionSummary,omitempty"`
	// MergeQueueBlockedAt mirrors model.Observation's own field: non-nil
	// once the merge queue has stopped driving this task's pull request
	// -- an automatic fix that did not take, or checks that never
	// finished -- so it needs a human rather than another automatic
	// attempt. It is what lets the frontend put a "Merge blocked" chip
	// beside the state badge of a task whose pull request is on the queue
	// in name only (ui/src/state.js's completionPhase).
	//
	// The other half of the distinction bwsalmon/agents#494 asked for is
	// no longer a chip: a task waiting on a human's Submit click has its
	// own State now (model.StateAwaitingSubmit), so the badge says it
	// rather than contradicting it. The same went for a run that
	// answered in a comment and opened nothing (model.StateAwaitingReply).
	MergeQueueBlockedAt *time.Time `json:"mergeQueueBlockedAt,omitempty"`
	// Repairing is true for a task the merge queue has sent back to an
	// agent to repair its own pull request branch, and whose repair has
	// not finished (model.Observation.MergeQueueRepairAt,
	// orchestrator.repairInFlight). Such a task reads 'running' or
	// 'queued' like any other attempt, and from the state alone a person
	// watching cannot tell "still being written" from "written, merged
	// nowhere, and now being unstuck" -- so the frontend colours the
	// running mark differently for it (ui/src/components/StateDot.jsx).
	Repairing bool `json:"repairing,omitempty"`
	// MergeQueueState is grain's own live landing activity after the run,
	// or the reason there is none: model.Observation.MergeQueueState, the
	// vocabulary orchestrator's queuestatus.go defines. MergeQueueNote is
	// that state in a person's words -- which check, which task ahead of
	// this one, what GitHub said when it refused the merge -- and
	// MergeQueueStateAt is when it started, since "Merging" ten seconds
	// old and "Merging" three hours old are the difference between a
	// queue working and a queue stuck.
	// MergeQueueStalledAt is set once that state has stood for longer
	// than whatever drives it should take
	// (model.Observation.MergeQueueStalledAt, orchestrator's liveness.go):
	// the wait is one in name only, and the row says so rather than
	// wearing the live mark that means grain is getting somewhere
	// (ui/src/state.js's mergeQueueStalled). Absent for the ordinary
	// wait, which is nearly every task the queue is driving.
	MergeQueueState     string     `json:"mergeQueueState,omitempty"`
	MergeQueueNote      string     `json:"mergeQueueNote,omitempty"`
	MergeQueueStateAt   *time.Time `json:"mergeQueueStateAt,omitempty"`
	MergeQueueStalledAt *time.Time `json:"mergeQueueStalledAt,omitempty"`
	// MergeQueueReason is MergeQueueState's sub-case, classified
	// (Config.Reasons): which of that state's ways of waiting this entry
	// is in, and what is meant to end it. nil with no merge-queue state,
	// and on a row written before the cycle recorded one; a reason this
	// build cannot classify comes back with Reason alone.
	MergeQueueReason *Reason `json:"mergeQueueReason,omitempty"`
	// Stall is what this task's own state has to say for itself once it
	// has stood for longer than that state should take: which component
	// ought to have ended the wait, and what a person should conclude
	// and go and look at (orchestrator's taskLane, reached through
	// Config.Stall).
	//
	// It is MergeQueueNote-and-MergeQueueStalledAt's counterpart for the
	// lane the badge itself is about -- the merge lane carries the same
	// finding in two fields, the words and the moment the patience was
	// crossed, and this carries it in one string because a task's own
	// state has nothing storing that moment (model.StateTimes). Empty
	// for the overwhelming majority of tasks: a
	// state that is inside its patience, one that is waiting on a person
	// by design and so carries no patience at all, and one whose record
	// cannot say when the state began (model.StateTimes.Since). Empty is
	// therefore the ordinary answer and never a sign that nothing was
	// checked.
	//
	// A constant sentence rather than a running age, deliberately: it
	// says the patience that was crossed, so the row changes once when
	// it is crossed and then stands still (orchestrator's noteWithStall
	// has the whole argument, and MergeQueueNote is the same shape one
	// lane over). The age itself is already on the row -- CreatedAt,
	// StartedAt -- for a reader who wants it.
	Stall string `json:"stall,omitempty"`
	// Paused is an operator's pause on this task (model.TaskPause): who,
	// when, and from which state. Nil for a task nobody has paused. A
	// paused task keeps its own State -- pausing changes nothing a state
	// is derived from -- and is never reported stalled: Stall is empty
	// while it is paused, since a person stopped it on purpose.
	Paused *TaskPaused `json:"paused,omitempty"`
	// Reason is the sub-case of State this task is held in, classified
	// (Config.Reasons, orchestrator's taskReasonOf): which of the three
	// conditions it is in, what it waits on, and the control that ends
	// it -- the button the inbox's stalled group offers beside Stall's
	// sentence. nil where no classifier is wired.
	Reason *Reason `json:"reason,omitempty"`
	// Activity is what this task's live run is doing right now -- one
	// short phrase (model.Run.Activity), with ActivityAt the moment it
	// was written.
	//
	// Both are empty for every task that is not running, and for a running
	// task whose agent has not said anything yet: a run is under no
	// obligation to narrate itself, so their absence is the ordinary case
	// and never a sign of trouble. TaskList.jsx shows the phrase on the
	// row and leans on ActivityAt to say how long it has stood, since
	// "waiting for CI" ten seconds old and the same words an hour old mean
	// opposite things.
	//
	// It rides on the list shape rather than only on TaskDetail --
	// unlike Comments or Attempts -- because a list is exactly where it
	// earns its place: the question it answers is "what are my running
	// tasks doing?", asked of all of them at once.
	Activity   string     `json:"activity,omitempty"`
	ActivityAt *time.Time `json:"activityAt,omitempty"`
	// ActivityBySetup says the phrase is grain's own rather than the
	// run's: what the dispatch path is doing to get this run started --
	// building its sandbox, cloning its repo, minting its credentials --
	// over the stretch before its agent's first turn, which is the one
	// part of a run nothing could narrate before (orchestrator.setupNotes,
	// model.RunActivity.BySetup).
	//
	// It is carried rather than blurred because everything else that has
	// ever appeared in this field is something an agent wrote, and a
	// reader who has learnt to read the phrase as the agent's voice should
	// not have to guess which of the two is speaking. The frontend marks
	// such a phrase as grain's (TaskList.jsx, DetailOverlay.jsx); it is
	// never true for a phrase written through update_status, because
	// grain's own are cleared the moment the agent starts.
	ActivityBySetup bool `json:"activityBySetup,omitempty"`
}

// oomRetryActivity is a queued task's row line after its last run's
// sandbox was OOM-killed (model.OOMOutcome): grain's own words, as a
// grant wait's are, standing until the next run starts.
func oomRetryActivity(at time.Time) *model.RunActivity {
	return &model.RunActivity{Note: model.OOMRetryActivity, At: &at, BySetup: true}
}

func grantWaitActivity(wait model.GrantWait) *model.RunActivity {
	if wait.Capability == model.ModelWaitCapability || wait.Capability == model.TeamSetupWaitCapability ||
		wait.Capability == model.BranchWaitCapability || wait.Capability == model.PolicyWaitCapability ||
		wait.Capability == model.QuotaWaitCapability || wait.Capability == model.ChangeTargetWaitCapability {
		// The resource is already the whole sentence: "no enabled
		// connection serves model X", "all connections serving X are busy",
		// 'team "x" has no GitHub credential for o/r -- ...'.
		return &model.RunActivity{Note: "waiting: " + wait.Resource, At: &wait.WaitingAt, BySetup: true}
	}
	return &model.RunActivity{
		Note:    fmt.Sprintf("waiting for %s (%s grant)", wait.Resource, wait.Capability),
		At:      &wait.WaitingAt,
		BySetup: true,
	}
}

// Comment is one entry in a task's conversation.
//
// AuthorAssociation is gone with the issue thread that had one. What
// replaces it says more: OnBehalfOf is set when grain relayed somebody
// else's words, so a question from a dispatched run renders as grain
// speaking for an agent rather than as grain's own.
type Comment struct {
	ID         int64     `json:"id"`
	Author     string    `json:"author"`
	AuthorKind string    `json:"authorKind"`
	OnBehalfOf string    `json:"onBehalfOf,omitempty"`
	Body       string    `json:"body"`
	CreatedAt  time.Time `json:"createdAt"`
	// Handover marks this comment as the task's handover: a run's standing
	// summary of the older part of the conversation, written for the next
	// run on the task rather than for a person (model.Comment.Handover).
	//
	// Surfaced because a human reading the thread would otherwise see an
	// agent summarising a conversation they can already read, with nothing
	// saying why -- and because which comment grain is carrying into later
	// prompts is a fact about the task worth being able to see.
	Handover bool `json:"handover,omitempty"`
	// Summary is a question's one-line summary, as its asker gave it
	// (model.Comment.Summary); "" on every other comment, and on a question
	// stored before summaries existed.
	Summary string `json:"summary,omitempty"`
	// Attachments is every file posted alongside this comment
	// (bwsalmon/agents#522) -- metadata only, GET
	// /api/tasks/{id}/attachments/{attachmentId} is what serves one's
	// actual content.
	Attachments []Attachment `json:"attachments,omitempty"`
}

// Attachment is one file's metadata on the wire -- never its content,
// which GET /api/tasks/{id}/attachments/{attachmentId} serves separately
// so listing a task or its conversation never pays to carry every byte of
// every file attached to it (bwsalmon/agents#522).
type Attachment struct {
	ID          int64  `json:"id"`
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	Size        int64  `json:"size"`
}

func attachmentFrom(a model.AttachmentMeta) Attachment {
	return Attachment{ID: a.ID, Filename: a.Filename, ContentType: a.ContentType, Size: a.Size}
}

// TaskDetail is one task plus its conversation -- what GET
// /api/tasks/{id} returns, wider than the list shape since a list of many
// tasks has no reason to pay for every conversation.
//
// FailedAttempts, LastFailureAt and LastFailureReason are model.Store.
// FailureStreak's own fields, surfaced here rather than on Task: a list
// of many tasks already shows enough to notice a 'failed' one (its own
// state badge), and the reason it got there is a one-task question, the
// same reasoning that keeps Comments off the list shape (bwsalmon/
// agents#403 -- "a way for grain get/the UI to show N consecutive failed
// attempts, last failure: reason").
type TaskDetail struct {
	Task
	Comments []Comment `json:"comments"`
	// LocalScriptApprovals is the local-script calls this task's run is
	// waiting on a person for, and the ones answered recently
	// (LocalScriptApprovals).
	LocalScriptApprovals []LocalScriptApproval `json:"localScriptApprovals,omitempty"`
	// DeclaredPush is the refs beyond its own branch this task's runs may
	// push, by the `push:` of the repository-declared template it was
	// filed from (grain/task-2536) -- read from that declaration as the
	// repository's default branch has it now, which is what the git proxy
	// reads too. Nil for a task filed from no such template, or from one
	// declaring no push set.
	DeclaredPush *DeclaredPush `json:"declaredPush,omitempty"`
	// FailedAttempts is 0 once the task's most recent run succeeded, or
	// none has finished yet -- otherwise how many of its most recent runs,
	// in a row, ended without succeeding.
	FailedAttempts int        `json:"failedAttempts,omitempty"`
	LastFailureAt  *time.Time `json:"lastFailureAt,omitempty"`
	// LastFailureReason is that run's own task_run.detail -- a tool
	// error, the agent framework's own error text, or ProcessResult's own
	// "finished without pushing, asking, or commenting".
	LastFailureReason string `json:"lastFailureReason,omitempty"`
	// Attempts is every run this task has had, oldest first -- the full
	// history FailedAttempts only counts and summarises (bwsalmon/
	// agents#445: "show attempts and their status, in order, in the task
	// view").
	Attempts []Attempt `json:"attempts,omitempty"`
	// Transitions is model.Transitions' own projection: every point the
	// record lets this task's state be pinned to a time, oldest first
	// (bwsalmon/agents#452 -- "show the state transitions for the task").
	Transitions []Transition `json:"transitions,omitempty"`
	// PullRequestEvents is this task's own tracked pull request's history
	// -- opened, then merged or closed without merging -- off
	// model.Observation's own PrOpenedAt/PrMergedAt/PrClosedAt, oldest
	// first (bwsalmon/agents#493 -- "show PR events in the task
	// timeline"). Empty for a task with no pull request yet, or with one
	// orchestrator.SyncPullRequests has not synced even once.
	PullRequestEvents []PullRequestEvent `json:"pullRequestEvents,omitempty"`
	// Attachments is every file carried by the task's own body (never one
	// posted alongside a comment -- see Comment.Attachments for those),
	// bwsalmon/agents#522.
	Attachments []Attachment `json:"attachments,omitempty"`
	// PendingSecret is the credential a parked run asked a human to set
	// (mcp's request_secret, model.Observation.PendingSecret), resolved
	// into the secret and key a write addresses -- what tells the task
	// pane to offer a write-only box for it beside the reply box, and
	// nil for the overwhelming majority of tasks, which are parked on
	// nothing of the kind.
	//
	// A name and a location, never a value: nothing in this package can
	// read a stored secret back, and PUT /api/tasks/{id}/secret is the
	// one direction this travels (see task_secret.go).
	PendingSecret *CapabilitySecret `json:"pendingSecret,omitempty"`
	// CI is what grain's own CI last said about the branch this task
	// pushed (model.Observation.CI), nil for the overwhelming majority of
	// tasks -- one that does not hold the grain-ci capability, or whose
	// branch has not been through CI yet.
	//
	// The verdict itself, not a pointer at one: RunID is here so a client
	// with the run still in reach can fetch its jobs from GET
	// /api/ci/runs/{id}, but Conclusion and Detail stand on their own
	// once it has been evicted, which is the whole reason
	// model.CIStatus stores them.
	CI *TaskCIStatus `json:"ci,omitempty"`
	// CIFaults are the shared CI faults (grain or GitHub) affecting this
	// task's branch or run right now, even when the task has no grain CI run
	// of its own (e.g. a GitHub-CI failure shared across branches).
	CIFaults []CIFault `json:"ciFaults,omitempty"`
	// SuspendedSession is the agent conversation this task's last run
	// left parked, which its next run will be resumed into
	// (model.SuspendedSession), and nil for a task that has none --
	// every task before its first run, and every task whose framework
	// keeps no session that outlives one.
	//
	// It is on the task pane because it answers a question a person
	// watching a long task actually asks: is the next attempt going to
	// start from nothing, or carry on? And because the token is
	// something they can use -- `claude --resume <token>` on the
	// controller opens the very conversation grain will.
	SuspendedSession *TaskSuspendedSession `json:"suspendedSession,omitempty"`
	// DeploymentWide is every team-scoped capability this task holds in
	// its deployment-wide form (admincaps.TeamScoped, grain/task-2555):
	// self-debug reading every team's tasks and metrics. A capability held but not named here
	// reaches only the task's own team's work -- which is what `grain
	// mcpserver` reads it as, so an older daemon that never sends it
	// leaves a run team-scoped rather than wider than it should be.
	DeploymentWide []string `json:"deploymentWide,omitempty"`
	// FiledBySuiteRun is the suite run that filed this task
	// (model.Store.SuiteRunOfTask) -- what lets the task's page say
	// "pass 2 of walk4-suite run 1" and link to /suites/runs/<id>, the
	// other direction from the run's pane, which links to each of its
	// tasks. Nil for a task no suite run filed, and for one whose run is
	// no longer in the store, which keeps only Task.SuiteRun's plain
	// chip. On the detail rather than the list because only the task's
	// own page draws it.
	FiledBySuiteRun *TaskSuiteRunOrigin `json:"filedBySuiteRun,omitempty"`
}

// TaskSuiteRunOrigin is model.SuiteRunOrigin on the wire.
type TaskSuiteRunOrigin struct {
	RunID        int64  `json:"runId"`
	SuiteName    string `json:"suiteName"`
	Pass         int    `json:"pass"`
	TemplateName string `json:"templateName,omitempty"`
}

// TaskSuspendedSession is model.SuspendedSession on the wire: the token
// that reopens the conversation, whose CLI holds it, and which run left
// it there.
//
// The token is an id in a session store on the controller's disk, not a
// credential and not any of the conversation's content, which is what
// makes it something a task pane may show -- see model.SuspendedSession
// on why it is a pointer into somebody else's store rather than a value
// of grain's.
type TaskSuspendedSession struct {
	Token     string    `json:"token"`
	Framework string    `json:"framework,omitempty"`
	RunID     string    `json:"runId,omitempty"`
	At        time.Time `json:"at"`
}

// suspendedSessionFrom projects an observation's suspended session onto
// the wire, nil for an observation with none and for a task nothing has
// ever observed -- the same shape ciStatusFrom above has, for the same
// reason.
func suspendedSessionFrom(obs *model.Observation) *TaskSuspendedSession {
	if obs == nil || obs.SuspendedSession == nil || obs.SuspendedSession.Token == "" {
		return nil
	}
	s := obs.SuspendedSession
	return &TaskSuspendedSession{Token: s.Token, Framework: s.Framework, RunID: s.RunID, At: s.At}
}

// TaskCIStatus is model.CIStatus on the wire.
//
// Conclusion carries pkg/ci's own vocabulary -- "success", "failure",
// "skipped", "cancelled" -- and is empty for a run still going, the same
// way Attempt.Outcome is empty for a run still in flight.
type TaskCIStatus struct {
	RunID      string     `json:"runId"`
	Commit     string     `json:"commit,omitempty"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	Conclusion string     `json:"conclusion,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	Detail     string     `json:"detail,omitempty"`
	// Faults are the failures this run shares with other branches
	// (cifault.go), empty for the ordinary red build that is this
	// branch's own. It is here because this pane is where a red verdict
	// is read and acted on: the afternoon this was written for spent
	// itself repairing four pull requests one at a time against a cause
	// none of them owned, and every one of those decisions was taken in
	// front of this alert.
	Faults []CIFault `json:"faults,omitempty"`
}

// ciStatusFrom projects an observation's CI status onto the wire, nil for
// an observation that has none and for a task nothing has ever observed.
func ciStatusFrom(obs *model.Observation) *TaskCIStatus {
	if obs == nil || obs.CI == nil {
		return nil
	}
	return &TaskCIStatus{
		RunID:      obs.CI.RunID,
		Commit:     obs.CI.Commit,
		StartedAt:  obs.CI.StartedAt,
		Conclusion: obs.CI.Conclusion,
		FinishedAt: obs.CI.FinishedAt,
		Detail:     obs.CI.Detail,
	}
}

// PullRequestEvent is one moment in a task's own tracked pull request's
// life. Kind is "opened", "merged" or "closed" ("closed" meaning closed
// *without* merging -- a merged PR is always "merged", never "closed",
// the same distinction model.Observation.PrMergedAt/PrClosedAt draw).
type PullRequestEvent struct {
	Kind string    `json:"kind"`
	At   time.Time `json:"at"`
}

// Transition is one entry in a task's timeline, projected from
// model.Transition. Inferred is model.Transition.Inferred: At is the
// earliest the state can have begun, not a recorded moment, and the
// timeline says so rather than showing it as precise.
type Transition struct {
	State    model.State `json:"state"`
	At       time.Time   `json:"at"`
	Inferred bool        `json:"inferred,omitempty"`
}

func transitionFrom(t model.Transition) Transition {
	return Transition{State: t.State, At: t.At, Inferred: t.Inferred}
}

// Attempt is one of a task's runs, projected from model.Run.
//
// Number is model.Run.Attempt, 1-based and dense per task (dispatch.
// Cycle's own doc comment on how it assigns one). Outcome is empty for a
// run still in flight (no FinishedAt yet); otherwise it is model.Run's
// own outcome vocabulary -- "succeeded", "failed", "cancelled" -- the
// same strings orchestrator.outcomeOf and orchestrator.run already
// record, passed through rather than re-encoded so the UI's label for
// one matches `grain get`'s.
//
// SetupNote is the last phrase grain stamped on this run while it was
// still getting it started -- "building a sandbox", "cloning
// acme/widgets", "minting the task's credentials" (orchestrator.
// setupNotes) -- and SetupNoteAt when it said it. It is set only for a
// run that never reached its agent (model.Run.AgentStartedAt is nil), and
// so is empty for the overwhelming majority of attempts: grain clears its
// last phrase in the same breath as handing the run over, and everything
// written after that is the agent's own.
//
// That narrowing is the point, not a limitation. A run that ends
// "setup-failed" gets a detail saying only that its sandbox could not be
// prepared, and the phrase it broke on is the one record of how far setup
// actually got -- which nothing showed a reader, because Store.
// TaskActivity reads live runs only, by design. What the *agent* last
// said through update_status is a different thing entirely, and is
// deliberately not carried here: "waiting for CI on the second push"
// beside a failed attempt reads as that attempt's diagnosis and is not
// one, and an attempt whose agent ran has its whole transcript one click
// away (AttemptTranscript). So only grain's own half travels, and the
// frontend can render it in grain's voice without having to ask whose
// sentence it is (DetailOverlay.jsx's attempt timeline).
// PromptBytes and PromptTokens are how big the prompt this attempt was
// handed came out (model.PromptSize): the number that says whether a
// long-running task's prompt is growing, attempt by attempt, and the
// only way to see that it is before it starts costing the run. Both are
// 0 for an attempt that never reached its agent and for every attempt
// recorded before the prompt was stored at all, which read the same way
// here on purpose -- neither has a prompt to measure. PromptTokens is an
// estimate and the frontend says so.
type Attempt struct {
	Number       int        `json:"number"`
	StartedAt    time.Time  `json:"startedAt"`
	FinishedAt   *time.Time `json:"finishedAt,omitempty"`
	Outcome      string     `json:"outcome,omitempty"`
	Detail       string     `json:"detail,omitempty"`
	SetupNote    string     `json:"setupNote,omitempty"`
	SetupNoteAt  *time.Time `json:"setupNoteAt,omitempty"`
	PromptBytes  int        `json:"promptBytes,omitempty"`
	PromptTokens int        `json:"promptTokens,omitempty"`
	// Model and AgentFramework are what this attempt actually ran on
	// (model.Run.Model) -- which, for a task left to the deployment's
	// default list or the rotation, can differ from one attempt to the
	// next. Empty for an attempt recorded before grain kept them.
	Model          string `json:"model,omitempty"`
	AgentFramework string `json:"agentFramework,omitempty"`
	// CapabilityAuditCount is how many records this attempt's capability
	// grants' servers returned about what the grants did -- host-script's
	// one per call above all -- when the attempt released them
	// (model.CapabilityAuditRecord). The records are their own fetch
	// (AttemptCapabilityAudit), for the transcript's reason.
	CapabilityAuditCount int `json:"capabilityAuditCount,omitempty"`
}

// taskFrom projects a model.Task to its JSON shape. closed reports, for
// every task ID a blocking link might target, whether that task reads
// closed -- the caller resolves it (Client.ListTasks over the whole
// store in one query, Client.Task over just this task's own targets) so
// this function stays free of the store itself. activity is the same
// arrangement for the task's live run's own synopsis, resolved by the
// caller for the same reason and nil for a task with no live run or one
// whose run has said nothing. reviewTemplateName is the same again for
// the review attached to the task (Client.ListTasks reads every template
// once for a whole list; Client.Task reads the one it needs), empty for
// a task with no review and for one whose template has since gone. span
// is that arrangement once more, for the task's own stretch of the
// calendar (Store.TaskSpans over a whole list, Store.TaskSpanOf for one),
// and a zero TaskSpan for a task that has neither started nor ended.
// stall is that arrangement one last time, for whether this task's state
// has outlived what it should take (Task.Stall): the caller resolves it
// because answering needs both the deployment's own deadlines, which
// live in the orchestrator and arrive through Config.Stall, and a clock,
// neither of which belongs in a projection.
func taskFrom(t model.Task, state model.State, closed map[string]bool, mergeQueueBlockedAt *time.Time,
	repairing bool, mergeQueue model.MergeQueueActivity, activity *model.RunActivity,
	reviewTemplateName string, span model.TaskSpan, stall string) Task {
	out := Task{
		ID:                   t.ID,
		Title:                t.Title,
		Description:          t.Body,
		Author:               t.Origin.Attribution.Actor.ID,
		AuthorKind:           string(t.Origin.Attribution.Actor.Kind),
		State:                state,
		Base:                 t.Base,
		Issue:                t.Issue,
		AutoMerge:            t.AutoMerge,
		AutoApproveProposals: t.AutoApproveProposals,
		Interactive:          t.Interactive,
		ConsoleSession:       consoleSessionOf(t),
		SandboxCPUs:          t.SandboxCPUs,
		SandboxMemoryMB:      t.SandboxMemoryMB,
		SandboxDiskGB:        t.SandboxDiskGB,
		AgentFramework:       t.AgentFramework,
		AgentBackendID:       t.AgentBackendID,
		Model:                t.Model,
		ModelSource:          string(t.ModelSource),
		NetworkAccess:        string(t.NetworkAccess),
		NothingNeededAction:  string(t.NothingNeededAction),
		NoPort:               t.NoPort,
		FixesBaseBranch:      model.FixesBaseBranch(t),
		GrainBaseRepair:      model.GrainBaseRepair(t),
		PromptExtension:      t.PromptExtension,
		ReviewTemplateID:     t.ReviewTemplateID,
		ReviewTemplateName:   reviewTemplateName,
		Creator:              t.Creator,
		Capabilities:         []string{},
		CustomFields:         customFieldValues(t.CustomFields),
		Stacked:              t.Origin.Reason == model.ReasonFix || t.Origin.Reason == model.ReasonReview,
		Review:               t.Origin.Reason == model.ReasonReview,
		Scheduled:            t.Origin.Reason == model.ReasonSchedule,
		SuiteRun:             t.Origin.Reason == model.ReasonSuite,
		Imported:             t.Origin.Reason == model.ReasonImported,
		AwaitingImportApproval: t.Origin.Reason == model.ReasonImported && t.ApprovedAt == nil &&
			state == model.StateProposed,
		CreatedAt:           t.CreatedAt,
		StartedAt:           span.StartedAt,
		EndedAt:             span.EndedAt,
		ClosedAt:            span.ClosedAt,
		PrMergedAt:          span.PrMergedAt,
		PrClosedAt:          span.PrClosedAt,
		DeferredUntil:       t.DeferredUntil,
		Approved:            t.ApprovedAt != nil,
		MergeQueueBlockedAt: mergeQueueBlockedAt,
		Repairing:           repairing,
		MergeQueueState:     mergeQueue.State,
		MergeQueueNote:      mergeQueue.Note,
		MergeQueueStateAt:   mergeQueue.At,
		MergeQueueStalledAt: mergeQueue.StalledAt,
		Stall:               stall,
	}
	if activity != nil {
		out.Activity, out.ActivityAt = activity.Note, activity.At
		out.ActivityBySetup = activity.BySetup
	}
	if t.Target != nil {
		out.Repo = t.Target.String()
		out.Branch = t.BranchName()
	}
	for _, r := range t.Reads {
		out.Reads = append(out.Reads, r.String())
	}
	for _, w := range t.Writes {
		out.Writes = append(out.Writes, WriteTarget{Repo: w.Repo.String(), Base: w.Base})
	}
	for _, g := range t.Grants {
		out.Capabilities = append(out.Capabilities, g.Capability)
	}
	out.Approvals = approvalViews(t)
	if model.HeldForApproval(t) && t.Origin.Attribution.OnBehalfOf != nil {
		out.HeldBy = t.Origin.Attribution.OnBehalfOf.ID
	}
	out.Origin = string(t.WorkOrigin)
	if t.ChangeTarget != nil {
		out.ChangeTargetKind = t.ChangeTarget.Kind
	}
	out.Reviewers = slices.Clone(t.Reviewers)
	for _, l := range t.Links {
		if l.Kind == model.LinkFixes && !model.LinkOffTarget(t, l) {
			out.PullRequest = l.Target
		}
		if l.Kind == model.LinkProposedBy {
			out.GeneratedFrom = l.Target
		}
		if l.Kind == model.LinkReviewTask {
			out.ReviewTask = l.Target
		}
		if l.Kind == model.LinkSecurityHold {
			out.SecurityHold = l.Target
		}
		if l.Kind == model.LinkSecurityReviewTask {
			out.SecurityReviewTask = l.Target
		}
		if l.Kind == model.LinkRepoRules {
			out.RepoRules = l.Target
		}
		if l.Kind == model.LinkPathTriggerHold {
			out.PathTriggerHold = l.Target
		}
		if l.Kind == model.LinkBaseRepairBypass {
			out.BaseRepairBypass = l.Target
		}
		if l.Kind == model.LinkReviewQuestions {
			if q, ok := model.ReviewQuestionsOf(model.Task{Links: []model.Link{l}}); ok {
				out.ReviewQuestions = q.ReviewTaskID
			}
		}
		if l.Kind == model.LinkMergePolicy {
			if p, ok := model.MergePolicyOf(model.Task{Links: []model.Link{l}}); ok {
				out.MergePolicy = &p
			}
		}
		if l.Kind == model.LinkPathTriggerTask {
			out.PathTriggerTasks = append(out.PathTriggerTasks, l.Target)
		}
		if l.Kind == model.LinkContinues {
			out.Continues = l.Target
		}
		if l.Kind == model.LinkSupersedes {
			out.Supersedes = l.Target
		}
		if l.Kind == model.LinkSupersededBy {
			out.SupersededBy = l.Target
		}
		if l.Kind == model.LinkReverts {
			out.Reverts = l.Target
		}
		if l.Kind == model.LinkRevertedBy {
			out.RevertedBy = l.Target
		}
		if l.Kind == model.LinkPortOf {
			out.PortOf = l.Target
		}
		if l.Kind == model.LinkPortedBy {
			out.PortedBy = append(out.PortedBy, l.Target)
		}
		if l.Kind == model.LinkGoogleChat {
			out.GoogleChatSpace = l.Target
		}
		if l.Kind == model.LinkChangelist {
			if len(out.Changelists) == 0 {
				out.Presubmit = changePresubmit(l.Target)
				out.CLReview = changeReview(t.ID, l.Target)
				out.CLSubmit = changeSubmit(t, state)
			}
			out.Changelists = append(out.Changelists, l.Target)
		}
		if l.Kind == model.LinkDependsOn {
			out.DependsOn = append(out.DependsOn, l.Target)
		}
		// A follow-up filed with its parent in depends_on carries both a
		// child-of and a depends-on link to the same task; it is waiting
		// on that task once, and says so once.
		if l.Kind.Blocks() && !closed[l.Target] && !slices.Contains(out.BlockedBy, l.Target) {
			out.BlockedBy = append(out.BlockedBy, l.Target)
		}
	}
	out.Blocked = len(out.BlockedBy) > 0
	return out
}

// commentFrom projects a model.Comment to its JSON shape. attachments is
// this one comment's own files, already narrowed by the caller (GetTask
// groups a task's AttachmentMetas by CommentID before calling this) --
// commentFrom stays free of the store the same way taskFrom does.
func commentFrom(c model.Comment, attachments []Attachment) Comment {
	out := Comment{
		ID:          c.ID,
		Author:      c.Author.Actor.ID,
		AuthorKind:  string(c.Author.Actor.Kind),
		Body:        c.Body,
		CreatedAt:   c.CreatedAt,
		Handover:    c.Handover,
		Summary:     c.Summary,
		Attachments: attachments,
	}
	if b := c.Author.OnBehalfOf; b != nil {
		out.OnBehalfOf = b.ID
	}
	return out
}

// promptBytes is how big this attempt's own prompt was, from the sizes
// the caller read for the whole task in one query
// (model.Store.RunPromptSizes) -- absent for an attempt that recorded
// none, which attemptFrom passes through as the zero it is rather than
// inventing a measurement.
func attemptFrom(r model.Run, promptBytes, auditCounts map[int]int) Attempt {
	out := Attempt{
		Number:               r.Attempt,
		StartedAt:            r.StartedAt,
		FinishedAt:           r.FinishedAt,
		Outcome:              r.Outcome,
		Detail:               r.Detail,
		Model:                r.Model,
		AgentFramework:       r.AgentFramework,
		CapabilityAuditCount: auditCounts[r.Attempt],
	}
	if size := model.MeasurePromptBytes(promptBytes[r.Attempt]); !size.Zero() {
		out.PromptBytes, out.PromptTokens = size.Bytes, size.EstimatedTokens
	}
	// Grain's own phrase travels; the agent's does not (Attempt's own doc
	// comment). AgentStartedAt is the same test model.RunActivity.BySetup
	// makes for a live run, asked of a finished one.
	if r.AgentStartedAt == nil {
		out.SetupNote, out.SetupNoteAt = r.Activity, r.ActivityAt
	}
	return out
}

// --- handlers ------------------------------------------------------------
//
// Every handler below is a thin HTTP shim over a Client method: decode
// the request, call it, translate the result (or error) into a status
// code and a JSON body. The logic that actually reads or writes the store
// lives in client.go, once, so a non-HTTP caller (cmd/grain's CLI) gets
// identical behaviour by calling the same Client directly.

// configResponse is GET /api/config's JSON shape -- deliberately its own
// type rather than json.Marshal'ing model.Principal/model.RepoRef
// directly, since neither carries wire tags (they are not otherwise a
// wire format) and would round-trip as {"Kind":...,"ID":...} instead of
// the plain strings HTTPClient and the frontend both expect.
type configResponse struct {
	Actor         string `json:"actor"`
	ActorKind     string `json:"actorKind"`
	Authenticated bool   `json:"authenticated"`
	// AuthMethod is AuthUserResponse's, so the frontend knows without a
	// second request how the caller is signed in.
	AuthMethod AuthMethod `json:"authMethod,omitempty"`
	// Allowed is always true: whoever holds the access code is the
	// deployment's owner (docs/access-code.md), and nobody else gets
	// past the gate to be refused. The frontend still reads it.
	Allowed bool `json:"allowed"`
	// IdentityNotAccepted, for a caller nobody has authenticated, says
	// which identity its request presented that did not count and why
	// (unverifiedIdentity): an assertion that did not verify, or a
	// plaintext header this deployment does not believe. The sign-in
	// dialog shows it for as long as it is open, so whoever runs the
	// proxy in front can see what to configure (grain/task-2018).
	IdentityNotAccepted string `json:"identityNotAccepted,omitempty"`
	// SingleTenantRefusal, for a caller a single-tenant daemon does not
	// trust as itself, is why it gets nothing here: there is no account
	// to sign in to, so the frontend shows this in place of a sign-in
	// form that could never work (task 4359).
	SingleTenantRefusal string       `json:"singleTenantRefusal,omitempty"`
	DefaultTarget       string       `json:"defaultTarget,omitempty"`
	Capabilities        []Capability `json:"capabilities"`
	CICapabilities      []Capability `json:"ciCapabilities,omitempty"`
	// CapabilityMethods is how many methods the attached capability
	// servers offer, a count and nothing more: the pickers fetch the
	// methods and their argument schemas (GET /api/capability-methods,
	// trusted) only where there are some.
	CapabilityMethods int `json:"capabilityMethods,omitempty"`
	// RebootEnabled mirrors secretsResponse's own Enabled: whether
	// Config.Reboot is set, so the frontend can hide the reboot button
	// entirely rather than show one that can only ever 404.
	RebootEnabled bool `json:"rebootEnabled"`
	// ShutdownEnabled and RestartEnabled are the same flag for the two
	// buttons beside it (grain/task-40): whether Config.Shutdown and
	// Config.Restart are set, so the System overlay offers the actions
	// this deployment can actually take and not the others. Three flags
	// rather than one because they are three independent host-side
	// commands -- see Config.Shutdown.
	ShutdownEnabled bool `json:"shutdownEnabled"`
	RestartEnabled  bool `json:"restartEnabled"`
	// ModelsConfigured and GitCredentialsConfigured are the two pieces of
	// setup without which a fresh deployment cannot do useful work. They
	// ride on the config response because the shell already polls it on
	// every page, and uses them for standing setup notices until each one
	// has actually been supplied.
	//
	// A backend is a configured model even when its Model is empty: each
	// agent CLI has its own default model. The legacy model fields still
	// count as well, so an upgraded deployment using the pre-backend
	// configuration does not acquire a spurious onboarding notice.
	ModelsConfigured         bool `json:"modelsConfigured"`
	GitCredentialsConfigured bool `json:"gitCredentialsConfigured"`
	// TargetRepos mirrors Client.Config.TargetRepos -- the same list
	// CreateTask enforces a task's repo against -- so the frontend can
	// offer a dropdown of known repos on the task and schedule forms
	// instead of a bare text field (bwsalmon/agents#447). Empty means
	// unrestricted, same as everywhere else this field appears.
	TargetRepos []string `json:"targetRepos,omitempty"`
	// ConfigRepo is the repository this deployment keeps its own
	// configuration in. It is a repo like the target repos for navigation
	// and per-repo settings, but is intrinsic to the deployment and cannot
	// be removed from it.
	ConfigRepo string `json:"configRepo,omitempty"`
	// AutoMergeDegraded mirrors Config.AutoMergeDegraded's own doc
	// comment: true means this deployment's GitHub credential cannot
	// read check runs, so a submitted task's AutoMerge is set but will
	// never actually merge. The frontend uses this to warn on Submit
	// instead of a task looking stuck for no visible reason
	// (bwsalmon/agents#483).
	AutoMergeDegraded bool `json:"autoMergeDegraded,omitempty"`
	// ShowClosedByDefault mirrors model.Config.ShowClosedByDefault
	// (bwsalmon/agents#537), read straight from the store the same way
	// Settings itself does rather than cached on Client.Config -- unlike
	// TargetRepos, nothing else in this process needs it kept in sync
	// mid-run, so there is no setTargetRepos-style write-through to keep
	// current. App.jsx works out the condition every task view opens
	// narrowed by from this (taskFilters.js's defaultFilters) the moment
	// it arrives, before Settings has ever been opened this session.
	ShowClosedByDefault bool `json:"showClosedByDefault"`
	// DefaultFilters is the config repository's query-encoded starting
	// conditions for every task list.
	DefaultFilters string `json:"defaultFilters"`
	// EnvironmentName mirrors model.Config.EnvironmentName -- what this
	// deployment is called on screen -- read from the store the same way
	// ShowClosedByDefault above is. It rides on this response rather than
	// on GET /api/settings alone because the frontend shows it in the
	// sidebar and the tab title from the first paint, on every view, and
	// this is the one call App.jsx makes before rendering anything.
	//
	// omitempty: absent and empty both mean an unnamed deployment, and
	// unlike ui.Settings there is nothing here to merge a cleared value
	// over -- App.jsx replaces this response wholesale on every poll.
	EnvironmentName string `json:"environmentName,omitempty"`
	// TimeZone mirrors model.Config.TimeZone -- the IANA zone this
	// deployment keeps its wall clock in -- read from the store the same
	// way EnvironmentName above is. Empty when the deployment never set
	// one (no row yet, or a row written before the column existed): that
	// is not an instruction to show UTC but the absence of one, and the
	// frontend formats in the browser's own zone (or the one picked in
	// Settings → Preferences) rather than being forced into UTC.
	//
	// It rides on this response rather than on GET /api/settings alone
	// for a stronger reason than the name above it: every timestamp the
	// frontend prints is formatted in this zone (ui/src/time.js), on
	// every view, from the first paint. Reading it from Settings would
	// mean every screen showed the browser's own zone until somebody
	// opened Settings -- which is the bug this setting exists to fix,
	// only intermittently.
	TimeZone string `json:"timeZone"`
	// Version is which build of grain is answering -- the commit this
	// binary was stamped with and when it was made (version.go, over
	// pkg/version). It rides here for the same reason EnvironmentName
	// above does: the sidebar shows it in every view, and this is the
	// call the frontend already makes before its first paint.
	//
	// Unlike every other field on this response it comes from the
	// process rather than from the store or from Config, and nil -- a
	// binary built with no VCS stamp at all -- means the sidebar simply
	// has nothing to print.
	Version *versionResponse `json:"version,omitempty"`
	// ListenPort is the port this request reached the daemon on, and
	// Proxied whether it came through a reverse proxy (relayedByProxy).
	// The rail's staging entries (ui/src/staging.js) read the two to
	// tell whether a persistent sandbox's host port is reachable from the
	// browser at all: behind UberProxy or IAP, which front grain's own
	// port and nothing else, it usually is not.
	ListenPort int  `json:"listenPort,omitempty"`
	Proxied    bool `json:"proxied,omitempty"`
	// InstallTrack classifies whether this deployment is a Google internal
	// install ("google") or an open-source install ("oss"), and
	// InstallTrackSignals lists the matched host, systemd unit, and store
	// signals when InstallTrack is "google".
	InstallTrack        string   `json:"installTrack"`
	InstallTrackSignals []string `json:"installTrackSignals,omitempty"`
	// ApprovedByDefault and AutoMergeByDefault mirror model.Config's own
	// fields of the same name (bwsalmon/agents#612), the same
	// read-straight-from-the-store-not-cached way ShowClosedByDefault
	// does. NewTaskOverlay.jsx seeds its "Queue immediately" and
	// "Auto-merge once checks pass" checkboxes from these the moment it
	// first renders, before Settings has ever been opened this session.
	//
	// A deployment with no stored config yet reports model.DefaultConfig's
	// own values -- both on -- rather than the zero value beside them, the
	// same reading of "no row yet" AgentFramework below takes.
	ApprovedByDefault  bool `json:"approvedByDefault"`
	AutoMergeByDefault bool `json:"autoMergeByDefault"`
	// NewestFirst mirrors model.Config's own field of the same name --
	// which end of the backlog a new task joins -- read from the store
	// the same way the two above are. NewTaskOverlay.jsx seeds its
	// "Add to backlog" picker from it, so the form opens on the end the
	// last task added chose (ui.CreateTaskRequest.AtFront writes it back
	// on every filing that states one, grain/task-202) rather than on a
	// guess.
	//
	// Not omitempty, unlike the optional strings below: false is the
	// meaningful answer "the end of the backlog", not the absence of one,
	// and a picker that has to render one of two positions cannot tell an
	// absent field from a chosen false.
	NewestFirst bool `json:"newestFirst"`
	// ReconcilerDown mirrors Config.ReconcilerDown's own doc comment:
	// true means this deployment's reconcile loop has died and nothing is
	// dispatching or reconciling tasks, even though this same API is
	// still answering. The frontend uses this to show a standing banner
	// rather than an operator having to notice tasks have stopped moving
	// and go searching logs to learn why (bwsalmon/agents#576).
	ReconcilerDown bool `json:"reconcilerDown,omitempty"`
	// InstallNotice is Config.InstallNotice: how this deployment is
	// installed is something its operator has to fix. The frontend shows
	// it as a standing banner and in System → Health. Absent when there
	// is nothing to say.
	InstallNotice *InstallNotice `json:"installNotice,omitempty"`
	// Restarting is Config.Restarting: this daemon is on its way down
	// for a restart, or an upgrade that ends in one is running. The
	// frontend remembers it, so that when the next poll cannot reach
	// the API it says "grain is restarting" rather than that the
	// deployment cannot be reached (docs/non-disruptive-upgrade.md,
	// Stage 1).
	Restarting bool `json:"restarting,omitempty"`
	// SandboxCount is the number of live agent and CI sandboxes the
	// deployment is holding right now. It rides on this response because
	// the sidebar shows it in every view and already polls config there;
	// Config.Sandboxes counts its live set without the per-guest probes
	// GET /api/sandboxes performs for the health pane.
	SandboxCount int `json:"sandboxCount"`
	// ExecutorAddr is where this deployment's remote executor listener
	// accepts workers (Config.ExecutorAddr), with a wildcard host left
	// off (":8422") since only the browser knows which name reaches this
	// machine. Absent when remote execution is off, and from anybody who
	// has not signed in. ExecutorTLS says the listener wants --tls.
	ExecutorAddr string `json:"executorAddr,omitempty"`
	ExecutorTLS  bool   `json:"executorTLS,omitempty"`
	// PackageCacheEntries is how many packages this deployment's proxy
	// cache is holding, across every ecosystem. It rides here for the
	// reason SandboxCount above does: the sidebar shows it beside its
	// Proxy cache entry in every view, and this is the call every view
	// already polls. It is the cache's running count rather than the
	// walk GET /api/package-cache performs -- see
	// PackageCache.PackageCacheEntryCount, which says why this one must
	// stay cheap and why being a few commits behind costs nothing.
	//
	// A pointer, and the one nullable count on this response, because
	// "this deployment runs no proxy cache" and "it runs one and it is
	// empty" are different answers and the rail draws them differently:
	// nil leaves the entry with no number at all, the way it read before
	// it had one, where 0 would claim an empty cache. Every other count
	// here describes something this deployment always has.
	PackageCacheEntries *int `json:"packageCacheEntries,omitempty"`
	// ActionsCacheEntries is how many entries this deployment's GitHub
	// Actions cache is holding, across every repository -- the same
	// figure beside the rail's Actions cache entry that
	// PackageCacheEntries is beside its Proxy cache one, riding here for
	// the same reason and nullable for the same reason: nil is "this
	// deployment keeps no Actions cache", which is not the empty cache a
	// 0 would claim.
	ActionsCacheEntries *int `json:"actionsCacheEntries,omitempty"`
	// StateRepoResetNotice mirrors Config.StateRepoResetNotice's own doc
	// comment: a merged settings change replaced a value on this
	// deployment and grain had nowhere to say so but its journal. It
	// rides here because it drives a marker in the application chrome --
	// the State pane behind it
	// carries the values, the reason, and the button that dismisses them.
	// Unlike that one it goes away when somebody has read it: it says
	// something happened rather than that something is so.
	StateRepoResetNotice bool `json:"stateRepoResetNotice,omitempty"`
	// StateRepoSyncFailure mirrors Config.StateRepoSyncFailure: the
	// settings have not been exported since Since. A standing banner, not
	// a marker -- it is a condition, and goes away when a sync succeeds.
	StateRepoSyncFailure *StateRepoSyncFailure `json:"stateRepoSyncFailure,omitempty"`
	// PendingLocalScripts is how many local-script calls are waiting on
	// a person (GET /api/local-script-approvals, which only the Inbox
	// polls). It rides here so the sidebar's Inbox entry can count them
	// the way the Inbox heading does without a poll of its own on every
	// page: a run blocked on somebody is the Inbox's business wherever
	// that somebody is looking (2026-10-29 intake walkthrough, O1).
	PendingLocalScripts int `json:"pendingLocalScripts,omitempty"`
	// HostSandboxes is true when the backend this daemon is running is
	// -host-sandboxes (model.SandboxBackendHost, read off RunningConfig):
	// every dispatched run happens in a plain directory on the daemon's
	// own machine, as its user, with no isolation from it. It rides on
	// this response for the reason ReconcilerDown above does -- it drives
	// a standing alert on every page (ui/src/alerts.js), and this is the
	// one call every view already polls (grain/task-15). Everyone is told,
	// not only admins: it is a fact about where the work anybody files
	// here will run. false wherever there is no running config to read
	// (`grain demo`).
	HostSandboxes bool `json:"hostSandboxes,omitempty"`
	// SandboxShapeIgnored mirrors Config.SandboxShapeIgnored's own doc
	// comment: true means this deployment's sandbox backend has no shape
	// to apply. Settings reports the same flag so its Sandbox tab can say
	// the three deployment-wide numbers size nothing (grain/task-9), and
	// it rides here as well because the *per-task* half of that setting
	// lives on the new-task form, which reads this response and never
	// GET /api/settings.
	//
	// Per task the consequence is worse than a number that shapes
	// nothing: orchestrator.HostSandboxes.Acquire refuses a non-zero
	// shape rather than ignoring it, so a task filed with an override on
	// such a deployment is dispatched and then dies at sandbox setup. The
	// form disables the override fields and says so, rather than letting
	// somebody fill in the one combination that guarantees the run fails
	// (grain/task-26).
	//
	// Distinct from HostSandboxes above, even though today's two backends
	// make the two agree: that one is what this daemon was *asked* for
	// (-host-sandboxes, cfg.granuleSandboxes), and this one is asked of
	// the backend actually built, which is the thing that would refuse
	// the shape. false where there is no backend to speak for, the same
	// silence Config.SandboxShapeIgnored documents.
	SandboxShapeIgnored bool `json:"sandboxShapeIgnored,omitempty"`
	// ChangeTargets is every non-git change target kind this binary has
	// registered (model.ChangeTargetKinds), as an edition linked in
	// registers them -- so the new-task form offers a non-git choice only
	// where filing one can work.
	ChangeTargets []string `json:"changeTargets,omitempty"`
	// AgentPause mirrors Config.AgentPause's own doc comment: set when a
	// model connection of a team this caller can see has said its
	// credential has no budget left in the current window, so nothing is
	// being dispatched onto it until that window resets (its own Pauses
	// names which connection of which team). It rides on this
	// response, rather than only on
	// GET /api/pause beside it, for the reason ReconcilerDown above does
	// -- it drives a standing banner on every page, and this is the one
	// call the frontend already polls from every view (grain/task-132).
	//
	// nil for a deployment that is not paused as well as for one with no
	// gate wired: a banner has nothing to draw either way, and the
	// difference between them is what GET /api/pause exists to state.
	AgentPause *AgentPauseStatus `json:"agentPause,omitempty"`
	// PipelinePause is the deliberate halt beside that involuntary one
	// (model.PipelinePause): somebody -- an operator, or grain itself
	// around a check of an upgrade it has just installed -- has stopped
	// this deployment picking new work up. It rides here for exactly the
	// reason AgentPause above does, and drives the same banner, since
	// "nothing is being dispatched" is one question however it is
	// answered.
	//
	// nil when the deployment is running. A pause that names a task
	// (UntilTask) is one the banner can say will lift itself; one that
	// names none waits for a person.
	PipelinePause *PipelinePauseStatus `json:"pipelinePause,omitempty"`
	// AgentFramework mirrors model.Config.AgentFramework -- this
	// deployment's default -- read from the store the same way
	// ShowClosedByDefault above is. NewTaskOverlay.jsx names it in the
	// "deployment default" option of its own per-task framework picker,
	// so whoever files a task can see which framework leaving that alone
	// actually means. Never empty: an unset stored value reads back as
	// model.AgentFrameworkAntigravity, the same defaulting ui.Settings
	// does.
	AgentFramework string   `json:"agentFramework"`
	DefaultModel   string   `json:"defaultModel,omitempty"`
	Models         []string `json:"models,omitempty"`
	// AgentBackends is the deployment's configured models, in the order
	// the rotation reaches them (model.Config.AgentBackends), for the
	// per-task model picker NewTaskOverlay.jsx and the task pane both
	// offer (grain/task-110). Disabled ones are included and say so:
	// a model out of the rotation is exactly the kind somebody pins a
	// task to by hand, so leaving it out of the picker would take away
	// the only way it ever runs.
	//
	// Deliberately not the stored AgentBackend itself. This response is
	// read by anyone who can see the board, while Settings is not, so it
	// carries what a picker needs to label a choice -- id, name,
	// framework, model, effort -- and nothing about how a backend
	// authenticates (its auth mode, or which other backend's credential
	// it borrows).
	AgentBackends []AgentBackendOption `json:"agentBackends,omitempty"`
	// NetworkAccess mirrors model.Config.NetworkAccess -- how much of
	// the internet a run's sandbox reaches here unless its task says
	// otherwise -- read from the store the same way AgentFramework above
	// is, and reported resolved for the same reason ui.Settings reports
	// it resolved: NewTaskOverlay.jsx names it in the "deployment
	// default" option of its own picker, and a blank there would tell
	// whoever is filing the task nothing. Never empty: an unset stored
	// value reads back as model.NetworkOpen.
	NetworkAccess string `json:"networkAccess"`
	// DefaultCapabilities mirrors model.Config's own field of the same
	// name, read from the store the same way ShowClosedByDefault above
	// is: the capability ids a task filed here starts out holding.
	// NewTaskOverlay.jsx seeds its capability picker from this, so the
	// boxes are already ticked when the form opens and whoever files the
	// task can untick one they do not want -- the form is where that
	// choice belongs, since the request it sends is the last word on
	// which capabilities the task gets (CreateTaskRequest.Capabilities).
	//
	// Only ever the form's starting state, like ApprovedByDefault and
	// AutoMergeByDefault above. What is filed is what the request names;
	// this is only what it names when nobody has said otherwise.
	DefaultCapabilities []string `json:"defaultCapabilities"`
	// RepoDefaultCapabilities is the per-repo layer of the same thing
	// (model.RepoConfig.DefaultCapabilities, grain/task-24): what each
	// repo adds to DefaultCapabilities above, keyed by "owner/name". Only
	// repos that add something appear -- Store.ListRepoConfigs keeps no
	// row for a repo that says nothing -- so an absent key and an empty
	// list mean the same thing here.
	//
	// Sent with the config rather than fetched per repo because the
	// new-task form needs it the moment the repo picker changes, on a
	// form that has not been submitted yet: this is one small map for a
	// deployment's whole repo list, and re-seeding the capability picker
	// from a round trip per keystroke would be a request for every
	// character typed into a repo field. The repos pane, which edits one
	// repo at a time and has somewhere to show a save failing, reads and
	// writes GET/PUT /api/repos/{owner}/{name}/capabilities instead.
	//
	// Filtered to what this build offers, the same as DefaultCapabilities
	// above and for the same reason.
	RepoDefaultCapabilities map[string][]string `json:"repoDefaultCapabilities,omitempty"`
	// PromptExtension mirrors model.Config.PromptExtension -- this
	// deployment's own standing instructions for every run -- read from
	// the store the same way AgentFramework above is.
	// NewTaskOverlay.jsx shows it beside its own per-task override box,
	// since that box *replaces* this text rather than adding to it
	// (model.Task.PromptExtension) and nobody can make that choice
	// sensibly without seeing what they would be replacing.
	//
	// Deployment-wide only, unlike RepoDefaultCapabilities above: the
	// per-repo layer is a paragraph of prose per repo rather than a short
	// list of ids, and this response is fetched on every poll by every
	// open tab. The repos pane reads a repo's own through GET
	// /api/repos/{owner}/{name}/prompt-extension, one repo at a time,
	// which is where the editing of it happens anyway --
	// ReposWithPromptExtension below is all this response says about
	// them.
	//
	// omitempty: absent and empty both mean a deployment that adds
	// nothing, and App.jsx replaces this response wholesale on every
	// poll, so there is nothing here for a cleared value to have to
	// overwrite.
	PromptExtension string `json:"promptExtension,omitempty"`
	// ReposWithPromptExtension names every repo that has standing
	// instructions of its own -- the names alone, not the text, for the
	// reason PromptExtension above gives.
	//
	// Names are enough for what the frontend needs it for, which is the
	// same job RepoDefaultCapabilities does for a repo whose only
	// presence here is its stored defaults: making the repo appear on the
	// repos page at all (state.js's repoRows). A repo can carry
	// configuration without being allow-listed and without any task
	// targeting it (SetRepoPromptExtension's own doc comment), and this
	// page is the only place that configuration can be read or edited --
	// so a repo missing from every other source has to come from
	// somewhere, or its text goes on reaching every run against it with
	// nowhere to see it.
	ReposWithPromptExtension []string `json:"reposWithPromptExtension,omitempty"`
	// ReposWithSetupCommand names every repo that has a setup command of
	// its own (model.RepoConfig.SetupCommand) -- names alone, for the
	// same two reasons as the field above: a shell command per repo is
	// not something every open tab needs on every poll, and what the
	// frontend needs the list for is making the repo appear on the repos
	// page at all (state.js's repoRows). A repo whose only presence here
	// is the command grain runs in its checkouts would otherwise be
	// reachable from nowhere.
	ReposWithCICapabilities  []string `json:"reposWithCICapabilities,omitempty"`
	ReposWithReleaseSettings []string `json:"reposWithReleaseSettings,omitempty"`
	ReposWithSetupCommand    []string `json:"reposWithSetupCommand,omitempty"`
	// ReposWithWorkflows names every repo this deployment declares grain
	// CI workflows for (model.RepoWorkflow) -- names alone, for the same
	// two reasons the two fields above are names alone, and with the
	// same job to do: a repository grain declares workflows for is very
	// often one grain does not control, so it may well have no tasks, no
	// allowlist entry and no other configuration, and the repos page is
	// where the workflows are edited. Without this it would be a
	// repository whose CI grain runs and whose CI nobody can find.
	ReposWithWorkflows []string `json:"reposWithWorkflows,omitempty"`
	// CustomFields is what this deployment's own task fields are
	// (model.CustomField) -- name, label, type, options and all -- in
	// the order every listing of them should read.
	//
	// It rides on this response for the reason RepoDefaultCapabilities
	// above does: the new-task form, a task's detail pane and the link
	// between the two all need the declarations the moment they render,
	// and this is the one call the frontend already makes before its
	// first paint. It is a handful of short rows for a whole
	// deployment, which is why sending all of it beats a route per
	// reader.
	//
	// The values a task carries are on the task (Task.CustomFields);
	// this is only what those values mean.
	CustomFields []CustomFieldDeclaration `json:"customFields,omitempty"`
	// Deployment is the deployment's own settings, as GET
	// /api/deployment answers them (GetTeam): its config repository,
	// default model, git connections and repos, which the frontend
	// needs before its first paint. Left out for a caller nobody has
	// signed in or trusted, who is told nothing about the deployment.
	Deployment           *Team  `json:"deployment,omitempty"`
	ExternalConfigRepo   string `json:"externalConfigRepo,omitempty"`
	ExternalConfigBranch string `json:"externalConfigBranch,omitempty"`
	// ConfigRepoOnly is whether a non-bootstrap settings write from the
	// API is refused with a config pull request (configRepoFor): an
	// external config repository, or the internal grain/config without
	// the directConfigWrites opt-out. The UI draws the config-repo-only
	// settings read-only exactly then (docs/design/config-repo-settings.md
	// §2.1); a UI with no config repository (`grain demo`) still edits
	// them, since there is nowhere else to.
	ConfigRepoOnly bool `json:"configRepoOnly,omitempty"`
}

// CustomFieldDeclaration is one declared field on the wire -- the JSON
// shape of model.CustomField, which carries no tags of its own (it is
// not otherwise a wire format, the same reason configResponse spells
// out model.Principal by hand).
//
// It is read-only everywhere it appears, and there is deliberately no
// endpoint that writes one: the fields a deployment has are decided in
// its state repository, in a pull request an agent opens and a human
// merges (model.CustomField's own doc comment). Settings shows them and
// says where to change them.
type CustomFieldDeclaration struct {
	Name        string   `json:"name"`
	Label       string   `json:"label,omitempty"`
	Type        string   `json:"type"`
	Options     []string `json:"options,omitempty"`
	Description string   `json:"description,omitempty"`
	Default     string   `json:"default,omitempty"`
	Position    int      `json:"position,omitempty"`
	// Multiple says this field holds a set of values rather than one
	// (model.CustomField.Multiple). It is what tells the frontend to
	// offer an input that holds several and comparisons over a set
	// ("is any of", "has all of"), and the only place that can be read
	// from: a task's own values say a string or an array according to
	// how many there are, not according to what the field allows.
	Multiple bool `json:"multiple,omitempty"`
	// ShowOnRow says this field's value belongs on every row and card
	// that lists a task (model.CustomField.ShowOnRow), rather than only
	// in the task's own pane. The other half of what a row shows -- the
	// field a list is currently sorted by -- the frontend works out for
	// itself from the sort it is applying, so this is the deployment's
	// standing answer and not the whole of it.
	ShowOnRow bool `json:"showOnRow,omitempty"`
}

// AgentBackendOption is one configured model as a task-filing choice --
// enough to label it in a picker and to name it in a request, and
// nothing about its credential (configResponse.AgentBackends has why).
type AgentBackendOption struct {
	// ID is what a task carries when it pins this model
	// (CreateTaskRequest.AgentBackendID).
	ID string `json:"id"`
	// Name is the operator's own name for this account or installation,
	// empty for one nobody named -- in which case a picker falls back to
	// Model and Framework below, exactly as Settings' own list does.
	Name string `json:"name,omitempty"`
	// Framework is which agent CLI runs this model, one of
	// model.AgentFrameworks(): what pinning it also decides about the
	// task (model.Task.AgentBackendID).
	Framework string   `json:"framework"`
	Models    []string `json:"models,omitempty"`
	// Disabled is a model the rotation skips (model.AgentBackend.
	// Disabled). Still offered, and worth saying out loud next to the
	// name: pinning is the only way work reaches one of these.
	Disabled bool `json:"disabled,omitempty"`
	// Team is the team this backend/connection belongs to.
	Team string `json:"team,omitempty"`
	// CredentialBackendID is the connection whose key this one borrows
	// (model.AgentBackend.CredentialBackendID), empty for one with a key
	// of its own: a usage-limit pause on that key covers this connection
	// too, which is how the panes tell a borrowing connection is paused.
	CredentialBackendID string `json:"credentialBackendId,omitempty"`
}

// agentBackendOptions projects the configured pool onto the wire for the
// pickers that file tasks against it, keeping the stored order -- which
// is the order the rotation itself reaches them in.
func agentBackendOptions(backends []model.AgentBackend) []AgentBackendOption {
	if len(backends) == 0 {
		return nil
	}
	out := make([]AgentBackendOption, 0, len(backends))
	for _, backend := range backends {
		out = append(out, AgentBackendOption{
			ID:                  backend.ID,
			Name:                backend.Name,
			Framework:           model.NormalizeAgentFrameworkName(backend.Framework),
			Models:              backend.SupportedModels(),
			Disabled:            backend.Disabled,
			Team:                backend.Team,
			CredentialBackendID: backend.CredentialBackendID,
		})
	}
	return out
}

func allDistinctModels(cfg model.Config) []string {
	var out []string
	add := func(m string) {
		m = strings.TrimSpace(m)
		if m != "" && !slices.Contains(out, m) {
			out = append(out, m)
		}
	}
	for _, b := range cfg.AgentBackends {
		for _, m := range b.SupportedModels() {
			add(m)
		}
	}
	return out
}

// customFieldDeclarations projects the store's own declarations onto the
// wire, in the order SortCustomFields already put them in.
//
// Type is resolved rather than passed through (model.CustomField.
// FieldType), so a frontend switching on it never has to know that an
// empty type means text; nothing else here is interpreted, because
// nothing else has a default to apply.
func customFieldDeclarations(fields []model.CustomField) []CustomFieldDeclaration {
	if len(fields) == 0 {
		return nil
	}
	out := make([]CustomFieldDeclaration, 0, len(fields))
	for _, f := range fields {
		out = append(out, CustomFieldDeclaration{
			Name:        f.Name,
			Label:       f.Label,
			Type:        string(f.FieldType()),
			Options:     f.Options,
			Description: f.Description,
			Default:     f.Default,
			Position:    f.Position,
			Multiple:    f.Multiple,
			ShowOnRow:   f.ShowOnRow,
		})
	}
	return out
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.tasks.Store.GetConfig(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	// Read before the capability listing below, which needs both to say
	// whether a row a human can tick would actually work on this
	// deployment -- the same two inputs the Settings pane's own
	// Capabilities tab reads.
	repoConfigs, err := s.tasks.Store.ListRepoConfigs(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	// The repos this deployment declares CI workflows for, read the same
	// way and for the same reason: they live in a table of their own, so
	// a repo can have one without having a repo_config row at all.
	repoWorkflows, err := s.tasks.Store.ListRepoWorkflows(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	// Read here rather than in the cfg != nil branch below, for the
	// reason the repo configs above are: a deployment that has never
	// saved a settings row can perfectly well have custom fields, since
	// they arrive from the state repository rather than from any pane.
	customFields, err := s.tasks.Store.ListCustomFields(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	deployment, err := s.tasks.GetTeam(r.Context(), model.DefaultTeamID)
	if err != nil {
		writeClientError(w, err)
		return
	}
	actor := s.tasks.actor(r.Context())
	_, authenticated := PrincipalFromContext(r.Context())
	// /api/config is served to callers nobody has vouched for, since it
	// is what the sign-in page reads. Only a trusted source runs as the
	// deployment's own actor (serveHTTP); anybody else who has not
	// signed in is nobody, and must not be told they are the deployment's
	// actor (grain/task-1616).
	if !authenticated && !singleTenantTrusted(r) {
		actor = model.Principal{}
	}
	allTargetRepos := make([]string, 0, len(deployment.TargetRepos))
	for _, r := range deployment.TargetRepos {
		if !slices.Contains(allTargetRepos, r) {
			allTargetRepos = append(allTargetRepos, r)
		}
	}
	authMethod, _ := s.authMethod(r)
	resp := configResponse{
		AuthMethod:          authMethod,
		Actor:               actor.ID,
		ActorKind:           string(actor.Kind),
		Authenticated:       authenticated,
		Allowed:             true,
		IdentityNotAccepted: identityNotAccepted(r, authenticated),
		ListenPort:          requestLocalPort(r),
		Proxied:             relayedByProxy(r),
		Capabilities:        s.tasks.capabilitiesWithReadiness(cfg, repoConfigs),
		CapabilityMethods:   len(s.tasks.ListCapabilityMethods(r.Context())),
		CICapabilities:      s.tasks.ciCapabilitiesWithReadiness(cfg, repoConfigs),
		RebootEnabled:       s.tasks.Config.Reboot != nil,
		ShutdownEnabled:     s.tasks.Config.Shutdown != nil,
		RestartEnabled:      s.tasks.Config.Restart != nil,
		GitCredentialsConfigured: s.tasks.Config.Credentials != nil &&
			len(s.tasks.Config.Credentials.Names()) > 0,
		TargetRepos:  allTargetRepos,
		CustomFields: customFieldDeclarations(customFields),
		Deployment:   &deployment,
		Version:      versionResponseFrom(version.Get()),
	}
	if s.tasks.Config.Sandboxes != nil {
		resp.SandboxCount = s.tasks.Config.Sandboxes.SandboxCount()
	}
	if actor.ID != "" {
		resp.ExecutorAddr = executorDialAddr(s.tasks.Config.ExecutorAddr)
		resp.ExecutorTLS = resp.ExecutorAddr != "" && s.tasks.Config.ExecutorTLS
	}
	if s.tasks.Config.PackageCache != nil {
		entries := s.tasks.Config.PackageCache.PackageCacheEntryCount()
		resp.PackageCacheEntries = &entries
	}
	if s.tasks.Config.ActionsCache != nil {
		entries := s.tasks.Config.ActionsCache.ActionsCacheEntryCount()
		resp.ActionsCacheEntries = &entries
	}
	if s.tasks.Config.StateRepoRef != nil {
		resp.ConfigRepo = s.tasks.Config.StateRepoRef().String()
	}
	resp.ExternalConfigRepo, resp.ExternalConfigBranch = s.tasks.externalConfigRepo(r.Context())
	resp.ConfigRepoOnly = s.tasks.configRepoOnly(r.Context())
	resp.AgentFramework = model.AgentFrameworkAntigravity
	resp.NetworkAccess = string(model.NetworkOpen)
	// Same reading of "no row yet" the AgentFramework line above takes:
	// what a form should start as on a deployment that has never stored a
	// config is the built-in default, not the zero value beside it.
	def := model.DefaultConfig()
	resp.ApprovedByDefault, resp.AutoMergeByDefault = def.ApprovedByDefault, def.AutoMergeByDefault
	resp.TimeZone = def.TimeZone
	resp.DefaultFilters = def.DefaultFilters
	if cfg != nil {
		resp.ModelsConfigured = len(cfg.AgentBackends) > 0
		resp.ShowClosedByDefault = cfg.ShowClosedByDefault
		resp.DefaultFilters = cfg.DefaultFilters
		resp.EnvironmentName = cfg.EnvironmentName
		// The stored zone, which before this never reached the frontend:
		// the line above the cfg block assigned a literal "UTC" and
		// nothing here overrode it, so every timestamp was formatted in
		// UTC whatever the column said, and nothing could be done about
		// it because UpdateSettingsRequest had no field to set it with.
		// Not TimeZoneOrDefault: an unset column reads back "", which the
		// frontend takes as "the browser's own zone", where "UTC" would
		// override every browser with a choice nobody made.
		resp.TimeZone = cfg.TimeZone
		resp.ApprovedByDefault = cfg.ApprovedByDefault
		resp.AutoMergeByDefault = cfg.AutoMergeByDefault
		resp.NewestFirst = cfg.NewestFirst
		resp.AgentBackends = agentBackendOptions(cfg.AgentBackends)
		resp.DefaultModel = cfg.EffectiveDefaultModel()
		resp.Models = allDistinctModels(*cfg)
		resp.NetworkAccess = string(model.NormalizeNetworkAccess(cfg.NetworkAccess))
		resp.PromptExtension = cfg.PromptExtension
		// Filtered to what this build actually offers, the same way
		// (*Client).defaultCapabilities filters before granting -- the
		// form should tick what a task would really be filed with, and
		// a stored id no row answers to is neither.
		for _, id := range cfg.DefaultCapabilities {
			if _, ok := s.tasks.capabilityByID(id); ok {
				resp.DefaultCapabilities = append(resp.DefaultCapabilities, id)
			}
		}
	}
	if actor.ID == "" {
		resp.SingleTenantRefusal = singleTenantRefusal
	}
	// Nor is nobody told who has access. The sign-in page needs none of
	// it, and a deployment on 0.0.0.0:4242 served the admin accounts and
	// every team's members to anyone who could reach the port (the 10-15
	// install walkthrough).
	if !authenticated && !singleTenantTrusted(r) {
		resp.Deployment = nil
		resp.TargetRepos = []string{}
	}
	// Outside the cfg != nil branch above: a repo can carry defaults of
	// its own on a deployment that has never saved a settings row, the
	// same case GetSettings reads this in both of its branches for.
	for _, rc := range repoConfigs {
		if len(rc.CICapabilities) > 0 {
			resp.ReposWithCICapabilities = append(resp.ReposWithCICapabilities, rc.Repo.String())
		}
		if rc.PromptExtension != "" {
			resp.ReposWithPromptExtension = append(resp.ReposWithPromptExtension, rc.Repo.String())
		}
		if rc.SetupCommand != "" {
			resp.ReposWithSetupCommand = append(resp.ReposWithSetupCommand, rc.Repo.String())
		}
		// BaseBranchCIJobs counts as a release setting because it is set
		// on that pane and is the other half of the switch there
		// (grain/task-330): a repository carrying one and nothing else --
		// written straight into the settings repository, say -- is one
		// whose pane there is something to see on.
		// PostMergeDispatchJobs counts for the same reason and is set on
		// the same pane (grain/task-409), even though what it names runs
		// whether or not releases are on.
		if !rc.Work.Empty() || len(rc.ReleaseSecrets) > 0 ||
			len(rc.BaseBranchCIJobs) > 0 || len(rc.PostMergeDispatchJobs) > 0 {
			resp.ReposWithReleaseSettings = append(resp.ReposWithReleaseSettings, rc.Repo.String())
		}
		var ids []string
		for _, id := range rc.DefaultCapabilities {
			if _, ok := s.tasks.capabilityByID(id); ok {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			continue
		}
		if resp.RepoDefaultCapabilities == nil {
			resp.RepoDefaultCapabilities = make(map[string][]string, len(repoConfigs))
		}
		resp.RepoDefaultCapabilities[rc.Repo.String()] = ids
	}
	// One name per repo, however many workflows it declares: the rows are
	// ordered by repo, so a repo's own run of them is contiguous.
	for _, w := range repoWorkflows {
		repo := w.Repo.String()
		if n := len(resp.ReposWithWorkflows); n > 0 && resp.ReposWithWorkflows[n-1] == repo {
			continue
		}
		resp.ReposWithWorkflows = append(resp.ReposWithWorkflows, repo)
	}
	if s.tasks.Config.AutoMergeDegraded != nil {
		resp.AutoMergeDegraded = s.tasks.Config.AutoMergeDegraded()
	}
	if s.tasks.Config.ReconcilerDown != nil {
		resp.ReconcilerDown = s.tasks.Config.ReconcilerDown()
	}
	if s.tasks.Config.InstallNotice != nil {
		resp.InstallNotice = s.tasks.Config.InstallNotice()
	}
	if s.tasks.Config.Restarting != nil {
		resp.Restarting = s.tasks.Config.Restarting()
	}
	if s.tasks.Config.StateRepoSyncFailure != nil {
		resp.StateRepoSyncFailure = s.tasks.Config.StateRepoSyncFailure()
	}
	resp.PendingLocalScripts = len(s.tasks.localScriptApprovals().Pending(s.tasks.now()))
	if s.tasks.Config.StateRepoResetNotice != nil {
		resp.StateRepoResetNotice = s.tasks.Config.StateRepoResetNotice()
	}
	resp.HostSandboxes = s.tasks.runningSandboxBackend() == string(model.SandboxBackendHost)
	resp.SandboxShapeIgnored = s.tasks.Config.SandboxShapeIgnored
	resp.ChangeTargets = model.ChangeTargetKinds()
	if status, ok, err := s.tasks.AgentPause(r.Context()); err != nil {
		log.Printf("ui: reading the usage-limit pauses for /api/config: %v", err)
	} else if ok && status.Paused {
		resp.AgentPause = &status
	}
	// A store error here is not worth failing the whole response over --
	// this route is what every open tab polls, and the rest of it is
	// still true -- but it must not read as "not paused" either, so it is
	// logged and the field left nil, the same shape the reads above take.
	if status, err := s.tasks.PipelinePause(r.Context()); err != nil {
		log.Printf("ui: reading the pipeline pause: %v", err)
	} else if status.Paused {
		resp.PipelinePause = &status
	}
	if s.tasks.Config.DefaultTarget != nil {
		resp.DefaultTarget = s.tasks.Config.DefaultTarget.String()
	}
	var trackProbe installtrack.Probe
	if s.tasks.Config.InstallTrackProbe != nil {
		trackProbe = *s.tasks.Config.InstallTrackProbe
	}
	trackProbe.Config = cfg
	trackProbe.RepoConfigs = repoConfigs
	if trackProbe.DataDir == "" {
		trackProbe.DataDir = s.tasks.Config.DataDir
	}
	det := trackProbe.Detect()
	resp.InstallTrack = string(det.Track)
	if authenticated || singleTenantTrusted(r) {
		resp.InstallTrackSignals = det.Signals
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleListTasks answers the whole task list -- and answers a poller
// that already has it with a 304 and no body at all. taskListCache is
// where the reasoning for both lives, including why it cannot serve a
// stale list. A caller who is not a system admin is answered only the
// tasks of the teams they belong to (bwsalmon/grain#2415). ?repo=owner/name
// answers only that repository's tasks.
func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	if q, ok := parseTaskListQuery(r.URL.Query()); ok {
		s.handleFilteredTaskList(w, r, q)
		return
	}
	body, etag, err := s.taskList.response(r.Context(), s.tasks, strings.TrimSpace(r.URL.Query().Get("repo")))
	if err != nil {
		writeClientError(w, err)
		return
	}
	// no-cache rather than no-store: revalidate every time, which is what
	// the ETag is for, rather than forbidding anything in between from
	// holding a copy to revalidate.
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", etag)
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// TaskStatesResponse is the paginated response for GET /api/tasks/states.
type TaskStatesResponse struct {
	States  map[string]model.State `json:"states"`
	Cursor  string                 `json:"cursor,omitempty"`
	HasMore bool                   `json:"hasMore,omitempty"`
}

func (s *Server) handleTaskStates(w http.ResponseWriter, r *http.Request) {
	limit := 1000
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	cursor := r.URL.Query().Get("cursor")
	if cursor == "" {
		cursor = r.URL.Query().Get("since")
	}

	states, nextCursor, err := s.tasks.StatesPage(r.Context(), limit, cursor)
	if err != nil {
		writeClientError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, TaskStatesResponse{
		States:  states,
		Cursor:  nextCursor,
		HasMore: nextCursor != "",
	})
}

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	body, etag, err := s.taskDetails.response(r.Context(), s.tasks, id)
	if err != nil {
		writeClientError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", etag)
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// attemptTranscriptResponse is GET
// /api/tasks/{id}/attempts/{number}/transcript's whole body -- its own
// shape, not folded into Attempt, so a caller listing many attempts (the
// task detail fetch) never pays to carry every one's full transcript,
// the same reasoning logLinesResponse's own {lines} shape follows for
// GET /api/logs/{source} (bwsalmon/agents#446).
type attemptTranscriptResponse struct {
	Transcript string `json:"transcript"`
}

func (s *Server) handleGetAttemptTranscript(w http.ResponseWriter, r *http.Request) {
	number, err := strconv.Atoi(r.PathValue("number"))
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("attempt number must be an integer"))
		return
	}
	transcript, err := s.tasks.AttemptTranscript(r.Context(), r.PathValue("id"), number)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, attemptTranscriptResponse{Transcript: transcript})
}

// CapabilityAuditEntry is one record of what an attempt's capability
// grant did (model.CapabilityAuditRecord). Record is the capability
// server's own JSON object, untrusted, and Note grain's own words about
// the grant's audit; an entry carries one or the other.
type CapabilityAuditEntry struct {
	Grant     string          `json:"grant"`
	AttemptID string          `json:"attemptId"`
	At        time.Time       `json:"at"`
	Record    json.RawMessage `json:"record,omitempty"`
	Note      string          `json:"note,omitempty"`
}

// attemptCapabilityAuditResponse is GET
// /api/tasks/{id}/attempts/{number}/capability-audit's whole body.
type attemptCapabilityAuditResponse struct {
	Records []CapabilityAuditEntry `json:"records"`
}

func (s *Server) handleGetAttemptCapabilityAudit(w http.ResponseWriter, r *http.Request) {
	number, err := strconv.Atoi(r.PathValue("number"))
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("attempt number must be an integer"))
		return
	}
	records, err := s.tasks.AttemptCapabilityAudit(r.Context(), r.PathValue("id"), number)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, attemptCapabilityAuditResponse{Records: records})
}

// handleGetTaskPrompt serves the prompt a task's own agent was handed --
// its own route rather than a field on TaskDetail, the same reasoning
// attemptTranscriptResponse gives for the transcript: a prompt runs to
// thousands of words, and the list every task detail fetch already pays
// for should not carry one per task for the sake of a pane most readers
// never open.
//
// A task that has no recorded prompt is a 200 with an empty one, not a
// 404: the task exists and the honest answer is "nothing has been
// dispatched for it yet" (Client.TaskPrompt), which the frontend renders
// as such. Only an unknown task id 404s.
func (s *Server) handleGetTaskPrompt(w http.ResponseWriter, r *http.Request) {
	prompt, err := s.tasks.TaskPrompt(r.Context(), r.PathValue("id"))
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, prompt)
}

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	user := requestUser(r)
	if user == "" && s.tasks.Config.Actor.Kind == model.PrincipalHuman {
		user = strings.TrimSpace(s.tasks.Config.Actor.ID)
	}
	if user == "" {
		writeError(w, http.StatusUnauthorized, errors.New("authentication required"))
		return
	}
	var req CreateTaskRequest
	if !readAttachmentJSON(w, r, &req, func(req *CreateTaskRequest) []AttachmentUpload { return req.Attachments }) {
		return
	}
	// A signed-in person is the creator whatever the body says: the
	// creator is what the task's detail pane names, and a member could
	// otherwise record a task as filed by a system admin
	// (grain/task-1573). A caller with no principal -- the CLI, a
	// loopback browser -- files as the deployment's own actor, never as
	// whoever it names: CreateTask checks the creator's team role, so a
	// named creator would be a borrowed permission (grain/task-2078).
	req.Creator = user
	// A requester the body names (CreateTaskRequest.Author) is the
	// operator's word about who asked, and only the operator's: a
	// signed-in person may name nobody but themselves.
	if author := strings.TrimSpace(req.Author); author != "" && requestUser(r) != "" && !strings.EqualFold(author, user) {
		writeError(w, http.StatusForbidden, fmt.Errorf("a signed-in user files tasks as themselves, not as %q", author))
		return
	}
	// The browser's zone when the body names none (TimeZoneHeader).
	if strings.TrimSpace(req.TimeZone) == "" {
		req.TimeZone = requestTimeZone(r)
	}
	task, err := s.tasks.CreateTask(r.Context(), req)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, task)
}

// handleUpdateTask is the CLI's `grain update` (cmdUpdate) reached over
// HTTP now that the CLI is a Client (this package's own HTTPClient) over
// REST rather than a direct model.Store caller -- the one Client method
// with no route before this, since the frontend has never needed a
// general field editor (it drives capabilities, dependencies, approval
// and comments through their own narrower endpoints instead).
func (s *Server) handleUpdateTask(w http.ResponseWriter, r *http.Request) {
	var req UpdateTaskRequest
	if !readJSON(w, r, &req) {
		return
	}
	task, err := s.tasks.UpdateTask(r.Context(), r.PathValue("id"), req)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, task)
}

// handleReorder is TaskList.jsx's drag-and-drop drop handler
// (bwsalmon/agents#476): move every task named in the request to sit
// between whatever neighbours it names, in one call so a multi-select
// drag lands as a single atomic move rather than one race-prone request
// per task.
func (s *Server) handleReorder(w http.ResponseWriter, r *http.Request) {
	var req ReorderRequest
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.tasks.Reorder(r.Context(), req); err != nil {
		writeClientError(w, err)
		return
	}
	tasks, err := s.tasks.ListTasks(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tasks)
}

// setCapabilityRequest is {id, attach} for a grant without arguments, or
// {capability, with, attach} for a capability server method's grant with
// its with: arguments (GrantRequest); grant names one in recorded
// <ref>=<json> form, as a task's capabilities list it, which is how a
// chip detaches the exact grant it shows. replace, on an attach, names a
// grant the new one takes the place of.
type setCapabilityRequest struct {
	ID string `json:"id"`
	// Attach is a pointer so the grant form can insist on it: a body
	// naming a capability but spelling attach wrong ({"capability":
	// ..., "enabled": true}) is refused, not read as a detach.
	Attach     *bool          `json:"attach"`
	Capability string         `json:"capability,omitempty"`
	With       map[string]any `json:"with,omitempty"`
	Grant      string         `json:"grant,omitempty"`
	Replace    string         `json:"replace,omitempty"`
}

func (s *Server) handleSetCapability(w http.ResponseWriter, r *http.Request) {
	var req setCapabilityRequest
	if !readJSON(w, r, &req) {
		return
	}
	id := r.PathValue("id")
	var err error
	if req.Capability != "" || req.Grant != "" || req.With != nil || req.Replace != "" {
		if req.ID != "" {
			writeError(w, http.StatusBadRequest, errors.New("name either id, or capability and with, not both"))
			return
		}
		if req.Attach == nil {
			writeError(w, http.StatusBadRequest, errors.New(`attach is required: true to grant, false to remove`))
			return
		}
		err = s.tasks.SetCapabilityGrant(r.Context(), id, GrantRequest{Capability: req.Capability, With: req.With, Grant: req.Grant}, *req.Attach, req.Replace)
	} else {
		err = s.tasks.SetCapability(r.Context(), id, req.ID, req.Attach != nil && *req.Attach)
	}
	if err != nil {
		writeClientError(w, err)
		return
	}
	s.respondWithTask(w, r, id)
}

type setDependencyRequest struct {
	ID     string `json:"id"`
	Attach bool   `json:"attach"`
}

// handleSetDependency attaches or detaches one depends-on link, the same
// attach/detach shape handleSetCapability already gives a checkbox --
// this is the picker's "add"/"remove" button, not a general link editor.
func (s *Server) handleSetDependency(w http.ResponseWriter, r *http.Request) {
	var req setDependencyRequest
	if !readJSON(w, r, &req) {
		return
	}
	id := r.PathValue("id")
	if err := s.tasks.SetDependency(r.Context(), id, req.ID, req.Attach); err != nil {
		writeClientError(w, err)
		return
	}
	s.respondWithTask(w, r, id)
}

// handleApprove is the UI's own "approve button" docs/data-model.md's UI
// direction asks for.
func (s *Server) handleApprove(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.tasks.Approve(r.Context(), id); err != nil {
		writeClientError(w, err)
		return
	}
	s.respondWithTask(w, r, id)
}

// handleWithdrawApproval is handleApprove's own undo: the button that
// takes a queued task back out of the queue and leaves it a proposal
// again, without closing it.
func (s *Server) handleWithdrawApproval(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.tasks.WithdrawApproval(r.Context(), id); err != nil {
		writeClientError(w, err)
		return
	}
	s.respondWithTask(w, r, id)
}

// deferRequest is the body of POST /api/tasks/{id}/defer: the wake time,
// and nothing else. See DeferOptions, whose one field this is.
type deferRequest struct {
	// DeferredUntil is an RFC 3339 instant, or "" for the deferral with
	// no date on it. A string rather than a time.Time so that a
	// malformed one is this handler's own 400 naming the field, rather
	// than the decoder's "cannot unmarshal" about a request body.
	DeferredUntil string `json:"deferredUntil"`
}

// handleDefer and handleUndefer are the pair of buttons that put a task
// aside and pick it back up again (Client.Defer, which says which states
// it applies to and why the rest are refused).
//
// The wake time is read out of the request body, and a body that does
// not mention it -- an empty one included -- is the indefinite deferral
// every caller that predates it makes (`grain defer` without its flag,
// `curl -X POST`, the batch Defer button), for the reason handleClose
// reads its own flag that way.
func (s *Server) handleDefer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req deferRequest
	if !readOptionalJSON(w, r, &req) {
		return
	}
	var opts DeferOptions
	if req.DeferredUntil != "" {
		until, err := time.Parse(time.RFC3339, req.DeferredUntil)
		if err != nil {
			writeClientError(w, validationErrorf("deferredUntil %q is not an RFC 3339 time", req.DeferredUntil))
			return
		}
		opts.Until = &until
	}
	if err := s.tasks.Defer(r.Context(), id, opts); err != nil {
		writeClientError(w, err)
		return
	}
	s.respondWithTask(w, r, id)
}

func (s *Server) handleUndefer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.tasks.Undefer(r.Context(), id); err != nil {
		writeClientError(w, err)
		return
	}
	s.respondWithTask(w, r, id)
}

// handleSubmit is the UI's own "submit" button docs/data-model.md's UI
// direction asks for, alongside the approve button handleApprove already
// is: once a task's run has produced a pull request, this is what queues
// it for the merge queue's automatic conflict resolution and merging.
func (s *Server) handleSubmit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.tasks.Submit(r.Context(), id); err != nil {
		writeClientError(w, err)
		return
	}
	s.respondWithTask(w, r, id)
}

type addCommentRequest struct {
	Body        string             `json:"body"`
	Attachments []AttachmentUpload `json:"attachments"`
	Author      string             `json:"author,omitempty"`
	Reviewers   *[]string          `json:"reviewers,omitempty"`
}

func (s *Server) handleAddComment(w http.ResponseWriter, r *http.Request) {
	var req addCommentRequest
	if !readAttachmentJSON(w, r, &req, func(req *addCommentRequest) []AttachmentUpload { return req.Attachments }) {
		return
	}
	req.Author = actingUser(r, req.Author)
	id := r.PathValue("id")
	if req.Reviewers != nil {
		if _, err := s.tasks.UpdateTask(r.Context(), id, UpdateTaskRequest{Reviewers: req.Reviewers}); err != nil {
			writeClientError(w, err)
			return
		}
	}
	if err := s.tasks.AddCommentWithAuthor(r.Context(), id, req.Body, req.Attachments, req.Author); err != nil {
		writeClientError(w, err)
		return
	}
	// Best effort: the comment is written, and a zone that failed to
	// stick costs the next run only the time of day it is told.
	_ = s.tasks.noteRequesterTimeZone(r.Context(), id, requestTimeZone(r))
	s.respondWithTask(w, r, id)
}

// handleGetAttachment serves one attachment's raw content -- the only
// place an attachment's bytes leave the store, since every other
// attachment-carrying response (TaskDetail, its comments) is metadata
// only (Attachment's own doc comment).
//
// The stored content type is whatever the uploader said -- an HTTP caller
// or an agent's tool call, neither of them trusted -- so it is never
// echoed back verbatim: serving "text/html" or "image/svg+xml" inline
// from the daemon's own origin is stored XSS against everyone who opens
// the link (#2374). Only the raster images and plain text in
// inlineAttachmentTypes are served inline, under their own type, so a
// screenshot still previews; everything else goes out as an
// application/octet-stream download. Every response also carries nosniff
// and a sandboxing CSP, so even an inline one cannot be reinterpreted as
// a document that runs script.
func (s *Server) handleGetAttachment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	attachmentID, err := strconv.ParseInt(r.PathValue("attachmentId"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("attachment id must be an integer"))
		return
	}
	a, err := s.tasks.Attachment(r.Context(), id, attachmentID)
	if err != nil {
		writeClientError(w, err)
		return
	}
	contentType, disposition := attachmentServingHeaders(a.ContentType)
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": a.Filename}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	w.Header().Set("Content-Length", strconv.FormatInt(a.Size, 10))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(a.Content)
}

// inlineAttachmentTypes are the media types an attachment may be served
// inline as: images a browser draws without running anything, and plain
// text. SVG is deliberately absent -- it is a document that can carry
// script, not a picture.
var inlineAttachmentTypes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
	"image/webp": true,
	"image/bmp":  true,
	"text/plain": true,
}

// attachmentServingHeaders turns an attachment's stored, uploader-chosen
// content type into the Content-Type and Content-Disposition type it is
// actually served with (handleGetAttachment): the normalized type and
// "inline" for one in inlineAttachmentTypes, application/octet-stream and
// "attachment" for anything else, including a type that does not parse.
func attachmentServingHeaders(stored string) (contentType, disposition string) {
	mediaType, _, err := mime.ParseMediaType(stored)
	if err != nil || !inlineAttachmentTypes[mediaType] {
		return "application/octet-stream", "attachment"
	}
	if mediaType == "text/plain" {
		return "text/plain; charset=utf-8", "inline"
	}
	return mediaType, "inline"
}

// closeRequest is the body of POST /api/tasks/{id}/close: the choice
// made at the moment of closing, and nothing else. See CloseOptions,
// whose one field this is.
type closeRequest struct {
	ClosePullRequest bool `json:"close_pull_request"`
}

// handleClose closes a task, and closes its pull request too if -- and
// only if -- the request said to.
//
// The flag is read out of the request body rather than a query
// parameter, and defaults to false on a body that does not mention it,
// which is what keeps every caller that predates it (the CLI without its
// flag, `curl -X POST`, the batch Close button) closing tasks and
// nothing else. An empty body is one of those: this is a POST that has
// never needed one.
func (s *Server) handleClose(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req closeRequest
	if !readOptionalJSON(w, r, &req) {
		return
	}
	if err := s.tasks.Close(r.Context(), id, CloseOptions{ClosePullRequest: req.ClosePullRequest}); err != nil {
		writeClientError(w, err)
		return
	}
	s.respondWithTask(w, r, id)
}

// handleStop interrupts an in-flight turn on a task, posting a turn stopped comment
// and putting the task in awaiting_reply state.
func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.tasks.Stop(r.Context(), id); err != nil {
		writeClientError(w, err)
		return
	}
	s.respondWithTask(w, r, id)
}

func (s *Server) handleReopen(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.tasks.Reopen(r.Context(), id); err != nil {
		writeClientError(w, err)
		return
	}
	s.respondWithTask(w, r, id)
}

// handleRetry is the UI's own "Retry" button (bwsalmon/agents#403): the
// one way a human clears a task's own failure streak once it has reached
// model.MaxConsecutiveFailures and task_state has stopped calling it
// 'queued' at all, since nothing else ever resets that count.
func (s *Server) handleRetry(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.tasks.Retry(r.Context(), id); err != nil {
		writeClientError(w, err)
		return
	}
	s.respondWithTask(w, r, id)
}

// handleRetryMerge is the UI's own "Retry merge" button: the one way a
// human asks the merge queue for another go at landing a pull request it
// has given up on, since nothing in orchestrator.SyncPullRequests ever
// revisits that decision by itself. See Client.RetryMerge.
func (s *Server) handleRetryMerge(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.tasks.RetryMerge(r.Context(), id); err != nil {
		writeClientError(w, err)
		return
	}
	s.respondWithTask(w, r, id)
}

// forceMergeRequest is the body of POST /api/tasks/{id}/force-merge: the
// reason, which is the whole of what an operator says when they override
// the merge queue, and which Client.ForceMerge refuses to do without.
type forceMergeRequest struct {
	Reason string `json:"reason,omitempty"`
	// Head is the commit the operator reviewed -- the head the page was
	// showing them. Optional: without it the override is for the pull
	// request's head as it is when the request arrives. Either way it is
	// for that one commit (bwsalmon/grain#2399).
	Head string `json:"head,omitempty"`
}

// handleForceMerge is the UI's own "Force merge" button, and `grain
// force-merge`: an operator telling grain to land a change the merge
// queue is not going to land on its own, with a reason recorded against
// the task and the pull request. See Client.ForceMerge.
//
// It records the request and returns; the merge is grain's next
// merge-queue cycle's to perform, which is what keeps grain's own state
// consistent with what happened on GitHub. Nothing here talks to GitHub.
func (s *Server) handleForceMerge(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req forceMergeRequest
	if !readOptionalJSON(w, r, &req) {
		return
	}
	if err := s.tasks.ForceMerge(r.Context(), id, req.Reason, req.Head); err != nil {
		writeClientError(w, err)
		return
	}
	s.respondWithTask(w, r, id)
}

// handleRunNow is the UI's own "Run now" button, and `grain run-now`:
// put an agent on this task on the next cycle, ahead of the queue and
// regardless of what the state machine makes of it. See Client.RunNow.
//
// Its body is optional, and names the agent asking for a run holding
// dispatch-control (runNowRequest): the approval and the note this
// leaves on the task are then that agent's, not the operator's.
func (s *Server) handleRunNow(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req runNowRequest
	if !readOptionalJSON(w, r, &req) {
		return
	}
	ctx, err := withAgentRequester(r.Context(), "a run-now's requester", req.RequestedBy, req.RequestedByKind)
	if err != nil {
		writeClientError(w, err)
		return
	}
	if err := s.tasks.RunNow(ctx, id); err != nil {
		writeClientError(w, err)
		return
	}
	s.respondWithTask(w, r, id)
}

// runNowRequest is POST /api/tasks/{id}/run-now's optional body -- see
// pausePipelineRequest's pair of the same name.
type runNowRequest struct {
	RequestedBy     string `json:"requestedBy,omitempty"`
	RequestedByKind string `json:"requestedByKind,omitempty"`
}

func (s *Server) respondWithTask(w http.ResponseWriter, r *http.Request, id string) {
	task, err := s.tasks.Task(r.Context(), id)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, task)
}

// --- request/response plumbing -----------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes err as the response. A 5xx error's own text is for
// the daemon's log, not the caller: see apierror.Body.
func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, apierror.Body(status, err))
}

// writeClientError maps a Client error to a status: 400 for a
// ValidationError (a mistake caught before the store was touched), 404
// for a task id with nothing behind it, and 500 otherwise.
//
// The fallback is 500 rather than the 502 this used to return. 502 was
// right when every failure came from an upstream GitHub call; the store
// is not upstream of this process in the same sense, and reporting a
// database error as "the gateway had trouble" would point an operator at
// the wrong thing.
func writeClientError(w http.ResponseWriter, err error) {
	if errors.Is(err, ci.ErrNotUp) {
		writeCINotUp(w)
		return
	}
	var fe *ForbiddenError
	if errors.As(err, &fe) {
		writeError(w, http.StatusForbidden, err)
		return
	}
	var ecre *externalConfigRepoError
	if errors.As(err, &ecre) {
		writeJSON(w, http.StatusConflict, ecre.body())
		return
	}
	var ve *ValidationError
	if errors.As(err, &ve) {
		if len(ve.fields) > 0 {
			// A capability grant's refusal, keyed by the argument it is
			// about as well, so a grant form can mark the field.
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "fields": ve.fields})
			return
		}
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var nf *NotFoundError
	if errors.As(err, &nf) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	var snf *scheduleNotFoundError
	if errors.As(err, &snf) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	// Not a fault of the request or of the deployment: the schedule is
	// simply still busy with its own previous firing, and it is set to
	// skip a run that would overlap one (Client.RunScheduleNow).
	var sb *scheduleBusyError
	if errors.As(err, &sb) {
		writeError(w, http.StatusConflict, err)
		return
	}
	// A save from a copy somebody else has since saved over
	// (revision.go): coded, so an editor can offer to reload.
	var se *staleEditError
	if errors.As(err, &se) {
		writeJSON(w, http.StatusConflict, apierror.Response{Error: err.Error(), Code: staleEditCode})
		return
	}
	var tnf *templateNotFoundError
	if errors.As(err, &tnf) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	var qnf *qualificationRunNotFoundError
	if errors.As(err, &qnf) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	var rnf *releaseNotFoundError
	if errors.As(err, &rnf) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	var snf2 *suiteNotFoundError
	if errors.As(err, &snf2) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	var srnf *suiteRunNotFoundError
	if errors.As(err, &srnf) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	var tmnf *teamNotFoundError
	if errors.As(err, &tmnf) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	var bnf *boardNotFoundError
	if errors.As(err, &bnf) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if errors.Is(err, errGitHubTokensUnavailable) || errors.Is(err, errSecretsUnavailable) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	// A conflict that survived the store's own retries: the change did not
	// land, and saying so plainly beats retrying forever or calling it a
	// server fault. It should be vanishingly rare -- it needs another
	// writer to win several times in a row.
	if errors.Is(err, model.ErrConflict) {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeError(w, http.StatusInternalServerError, err)
}

const (
	// maxJSONBody bounds the request body readJSON will read for an
	// ordinary JSON API route. Every such body is a handful of fields --
	// a title, a prompt, a settings object -- so this is far above
	// anything a real caller sends, and exists so a client cannot make
	// the server buffer an unbounded body into memory (#2423).
	maxJSONBody = 8 << 20
	// maxAttachmentJSONBody bounds the routes whose body can carry
	// AttachmentUploads: as many files as MaxAttachmentsPerRequest allows,
	// each at MaxAttachmentSize once base64-encoded, plus maxJSONBody for
	// everything else in the request. A body over it could never pass
	// decodeAttachments anyway.
	maxAttachmentJSONBody = MaxAttachmentsPerRequest*((MaxAttachmentSize+2)/3*4) + maxJSONBody
)

// readJSON decodes r's body into v, reading at most maxJSONBody bytes of
// it. A malformed body is a 400 and an oversized one a 413; either way
// the error has been written and readJSON reports false.
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	return readJSONLimit(w, r, v, maxJSONBody)
}

// readJSONLimit is readJSON with the body bound set by the caller, for
// the few routes whose bodies are legitimately larger than maxJSONBody.
func readJSONLimit(w http.ResponseWriter, r *http.Request, v any, limit int64) bool {
	defer r.Body.Close()
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit)).Decode(v); err != nil {
		writeBodyError(w, err)
		return false
	}
	return true
}

// readAttachmentJSON is readJSONLimit for the routes whose body can carry
// AttachmentUploads (task create, comments, agent messages). The body as
// a whole may run to maxAttachmentJSONBody, but only the uploads' own
// base64 content earns that allowance: what is left once uploads(v)'s
// Content is subtracted -- a title, a description, a comment body -- is
// held to maxJSONBody like any other route's, and answered 413 past it.
// Without this a 140 MiB description posted with no attachment at all
// was accepted and then served in every task listing.
func readAttachmentJSON[T any](w http.ResponseWriter, r *http.Request, v *T, uploads func(*T) []AttachmentUpload) bool {
	defer r.Body.Close()
	body := &countingReader{r: http.MaxBytesReader(w, r.Body, maxAttachmentJSONBody)}
	if err := json.NewDecoder(body).Decode(v); err != nil {
		writeBodyError(w, err)
		return false
	}
	rest := body.n
	for _, a := range uploads(v) {
		rest -= int64(len(a.Content))
	}
	if rest > maxJSONBody {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Errorf("request body exceeds %d bytes besides its attachments", maxJSONBody))
		return false
	}
	return true
}

// countingReader counts the bytes read through it.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// writeBodyError answers a request whose body failed to decode: 413 when
// it ran past its http.MaxBytesReader bound, 400 for anything else.
func writeBodyError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Errorf("request body exceeds %d bytes", tooLarge.Limit))
		return
	}
	writeError(w, http.StatusBadRequest, err)
}

// readOptionalJSON is readJSON for a route whose body is optional: no
// body at all leaves v at its zero value and succeeds, while a body that
// is there and malformed is still the caller's mistake and still a 400.
//
// It exists for POSTs that acquired a field after callers already
// existed without one -- see handleClose. io.EOF is precisely "there was
// nothing to decode", and is the only error swallowed here.
func readOptionalJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	defer r.Body.Close()
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBody)).Decode(v)
	if errors.Is(err, io.EOF) {
		return true
	}
	if err != nil {
		writeBodyError(w, err)
		return false
	}
	return true
}
