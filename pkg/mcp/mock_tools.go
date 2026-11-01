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

package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/ai-on-gke/grain/pkg/model"
)

// ProposalFiler persists an interactive proposal while its run is still live.
type ProposalFiler interface {
	FileProposal(context.Context, ProposedTask, string) (FiledProposal, error)
}

// FiledProposal is the task a ProposalFiler just filed, as far as the
// run that proposed it needs to know.
//
// Queued is whether it is waiting on a human at all: a proposal is
// normally filed unapproved, but two permissions reach the queue without
// a click -- a task that already holds a human's permission to land
// unreviewed work, proposing inside that permission (model.AutoSubmits),
// and a standing console session whose deployment has said it may queue
// its own work (model.ConsoleSessionQueuesProposals). Which of the two
// happened is worth telling the agent, since it is the difference
// between work that starts by itself and work that starts whenever
// somebody next looks at the queue.
//
// Reported rather than assumed, in both directions. This is the only
// immediate feedback a run gets about what it just filed, and a run that
// is told the wrong answer acts on it: the director, believing its
// proposals dispatched, declined to file work it thought would spend a
// sandbox at once, and then told an operator four tasks were "set going"
// while all four sat proposed (grain/task-311).
type FiledProposal struct {
	ID     string
	URL    string
	Queued bool
	// Superseded is the task this proposal closed as replaced by it, or
	// "" if it closed none -- either because the call asked for none or
	// because the one it named could not be superseded
	// (model.SupersedeRefusal). Reported for the same reason Queued is:
	// the run is about to act on what it believes just happened, and a
	// run that thinks a stale task is closed stops telling anybody it
	// isn't.
	Superseded string
	// SupersedeMoved is the open tasks that were waiting on Superseded
	// and now wait on this proposal instead (model.SupersedeResult.Moved),
	// and SupersedeKept the ones left waiting on the closed task -- and so
	// released by its close -- each with why. Reported because a run
	// superseding a task with dependents has just changed what they wait
	// on, and is the one that knows whether that was meant.
	SupersedeMoved []string
	SupersedeKept  []SupersedeKept
	// Held is whether the proposal waits for a person only because the
	// call asked it to (ProposedTask.HoldForApproval): its proposer's
	// permission would otherwise have queued it (model.HeldForApprovalTag).
	Held bool
	// ApprovalNote is the filer's reason an unqueued proposal is waiting
	// for a person -- held on request, over the bound on self-approved
	// work, or what the proposer's permission does not cover -- and ""
	// for a queued one or a filer that does not say.
	ApprovalNote string
	// Parked is why filing parked the proposal awaiting a reply -- its
	// repo is off the deployment's target repo list, say -- and "" for one
	// filing did not park. A parked proposal is not dispatched whatever
	// Queued says: Queued is its approval, and the park is a second thing
	// standing between it and a run that only an operator lifts.
	Parked string
}

// SupersedeKept is one dependent a supersede left where it was, and why
// (model.KeptDependent).
type SupersedeKept struct {
	ID     string
	Reason string
}

// ProposalKeyArgs computes the key from the tool arguments exactly as they
// appear in an agent result, so replay and live filing agree.
func ProposalKeyArgs(parent string, args map[string]any) string {
	identity, _ := args["id"].(string)
	if identity == "" {
		encoded, _ := json.Marshal(args)
		identity = string(encoded)
	}
	sum := sha256.Sum256([]byte(parent + "\x00" + identity))
	return "proposal:" + parent + ":" + hex.EncodeToString(sum[:])
}

// WriteTarget is one extra write target declared on a proposed task:
// the repository (owner/name) and base branch.
type WriteTarget struct {
	Repo string
	Base string
}

// ProposedTask is one propose_task call as this process recorded it.
// Filing it as a real task happens elsewhere and later
// (orchestrator.relayProposedTasks, off agent.Result.ToolCalls); this is
// the shape a caller holding a MockSink sees.
//
// AutoMerge is a pointer because "the agent said nothing" and "the agent
// said false" are different answers: an unset auto_merge inherits the
// proposing task's own setting (orchestrator.relayProposedTasks says why),
// and an explicit false opts a proposal out of an inheritance it would
// otherwise get.
type ProposedTask struct {
	ID    string
	Title string
	Body  string
	// Repo is the repository the call asked its task to be filed
	// against, as the agent wrote it, and empty for the ordinary call
	// that named none. Where the proposal actually lands is decided
	// later and elsewhere, against the proposing task's own repositories
	// and this deployment's (orchestrator.proposedTarget) -- the same
	// division CustomFields below is recorded under.
	Repo string

	// Branch is the base branch this proposal's work should target.
	// Empty means the target repository's default branch.
	Branch string

	// Priority is the numeric task priority (default 5; higher numbers mean higher priority).
	// 0 means unset (defaults to model.DefaultTaskPriority, or template
	// priority); the tool refuses an explicit priority below 1.
	Priority int

	// NoRepo files this task with no repository at all -- a standalone
	// task with no repository target,
	// task that will not clone or push a branch.
	NoRepo bool
	// ChangeTarget is propose_task's registered change-target argument: a task
	// whose change is in a non-git workspace rather than a git branch (model.ChangeTarget).
	ChangeTarget *model.ChangeTarget
	// AtFront is whether this task joins the front of the backlog ahead
	// of already queued work.
	AtFront *bool
	// TemplateID files this task from a template, with its input fields
	// filled in from TemplateFields.
	TemplateID string
	// TemplateFields is what the template's own input fields are filled in with,
	// keyed by field name.
	TemplateFields map[string]string
	DependsOn      []string
	// Capabilities is the capability ids requested for this proposed task.
	Capabilities []string
	AutoMerge    *bool
	// Supersedes is the id of an existing task this proposal replaces, as
	// the agent wrote it, and empty for the ordinary proposal that
	// replaces nothing. Filing it closes that task as superseded and
	// links the two (model.Store.Supersede) -- but only within the bounds
	// model.SupersedeRefusal states, and whether those hold is decided
	// later and elsewhere, against the store, exactly as Repo above is.
	Supersedes string
	// CustomFields is what the call asked the proposal to say about this
	// deployment's own declared fields (model.CustomField), keyed by
	// field name. Recorded here as the agent wrote it: what is actually
	// stored is decided later and elsewhere, against the declarations
	// (orchestrator.relayProposedTasks, model.FilterCustomFieldValues),
	// which is also the only place that can know what this deployment
	// declares at all.
	//
	// A slice per name because a field may be declared to hold a set
	// (model.CustomField.Multiple); a single-valued field is a slice of
	// one, exactly as it is on the task API.
	CustomFields map[string][]string

	// Reads is the list of repositories this proposal is allowed to read.
	Reads []string

	// Writes is the list of extra repositories and branches this proposal is allowed to write to.
	Writes []WriteTarget

	// ReviewTemplateID is the ID of a pre-merge code review template to attach.
	ReviewTemplateID string

	// NotBefore is the earliest the proposed task may start, and nil for
	// the ordinary proposal that may start whenever it is dispatched. It
	// is filed deferred with this as its wake time (model.Task.
	// DeferredUntil), which is the one place "not before this date" holds
	// anything: a date in the body is a note to a human, and has been
	// dispatched straight past (docs/test-runs/
	// 2026-09-08-nightly-week-one-still-early.md).
	NotBefore *time.Time

	// SandboxCPUs, SandboxMemoryMB, and SandboxDiskGB override the deployment's
	// default sandbox shape for the proposed task. 0 means use the deployment default.
	SandboxCPUs     int
	SandboxMemoryMB int
	SandboxDiskGB   int

	// NetworkAccess overrides how much of the internet the proposed task's sandbox
	// may reach: one of model.NetworkAccesses() ("open", "none"). Empty means
	// use the deployment default.
	NetworkAccess string

	// NothingNeededAction is what the proposed task does if its run ends on
	// nothing_needed: "close" or "ack", empty for the default (close).
	NothingNeededAction string
	// Goal is the goal the proposed task is declared with
	// (model.TaskGoal): grain closes it when the goal is met rather
	// than when its agent stops. Nil for none.
	Goal *model.GoalDecl

	// NoPort files the proposal as one whose change is not ported to its
	// repository's other paired branches (model.Task.NoPort).
	NoPort bool
	// KeepRunning files the proposal so that its task immediately restarts
	// when its run exits rather than completing or parking (model.Task.KeepRunning).
	KeepRunning bool
	// FixesBaseBranch files the proposal marked as fixing its base branch
	// while that branch is red (model.FixesBaseBranch): it is not made to
	// wait on the branch's repair, because it is one.
	FixesBaseBranch bool
	// Origin is the pull request or issue the call named as where this
	// work comes from (model.WorkOrigin), for a chat whose work answers
	// one; empty means the proposal inherits its parent's.
	Origin string

	// Model is the model or agent backend the call asked this proposal to run
	// on, as the agent wrote it, and empty for the ordinary proposal that
	// left it to the rotation.
	Model string
	// AgentFramework is the agent framework the call asked this proposal to run
	// with, as the agent wrote it, and empty for this deployment's default or
	// the framework the pinned model runs on.
	AgentFramework string

	// HoldForApproval files the proposal for a person to approve even
	// when the proposing task's own permission would have queued it
	// (model.HeldForApprovalTag). It only ever lowers what the call may
	// do: a proposal the permission does not cover waits either way.
	// Per call, and not inherited by what the held task files in turn.
	HoldForApproval bool
}

// TaskProposal aliases ProposedTask for callers referring to a proposed task record.
type TaskProposal = ProposedTask

// ReviewComment is one add_review_comment call as this process recorded
// it. Turning it into a real draft review on the pull request under
// review happens elsewhere and later, off agent.Result.ToolCalls
// (orchestrator.relayReviewFeedback), the same way a proposed task is
// filed; this is the shape a caller holding a MockSink sees.
type ReviewComment struct {
	Body string
	Path string
	Line int
	// HasLocation distinguishes "no path/line given" from "path/line
	// given as the zero value", since 0 is a real line number.
	HasLocation bool
}

// SecretRequest is one request_secret call: the name of a credential a
// run needs and cannot have, and what the run says it is for.
//
// No value is anywhere in this shape, and none ever passes through this
// package. The whole point of the tool is that the human types the value
// into grain's own UI and it goes straight into the secret store -- see
// requestSecretTool.
type SecretRequest struct {
	Secret string
	Reason string
}

// MockSink accumulates every escape-hatch tool call made during one run,
// so a caller (a test, or an in-process client) can inspect what an agent
// asked for. It is the whole of what *this process* does with such a
// call: nothing is created from here, and no agent ever has a live
// GitHub credential in reach.
//
// It is a test seam, not the production path. What a question, a closing
// comment or a proposal actually turns into happens after the run, from
// agent.Result.ToolCalls, in orchestrator.ProcessResult -- which is why
// the tools below describe that and never mention this sink. See
// NewMockTools.
type MockSink struct {
	mu             sync.Mutex
	question       string
	comment        string
	nothingNeeded  string
	secretRequest  *SecretRequest
	proposedTasks  []ProposedTask
	reviewComments []ReviewComment
	// files is how ask_question and comment_on_issue read and hold an
	// attachment given by path (SetAttachmentFiles); the zero value
	// refuses one.
	files AttachmentFiles
}

// SetAttachmentFiles gives ask_question and comment_on_issue a way to
// read an attachment given by path out of the run's sandbox, and to hold
// what they read until the run's finish relays it. Set after the tools
// are registered, since what it stages through -- the daemon -- is
// reached later than the roster is built.
func (s *MockSink) SetAttachmentFiles(files AttachmentFiles) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files = files
}

func (s *MockSink) attachmentFiles() AttachmentFiles {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.files
}

func (s *MockSink) Question() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.question
}

func (s *MockSink) Comment() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.comment
}

// NothingNeeded is the reason a nothing_needed call gave, or "" if the run
// made none.
func (s *MockSink) NothingNeeded() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nothingNeeded
}

// SecretRequest is the credential this run asked a human to set, or nil
// if it asked for none.
func (s *MockSink) SecretRequest() *SecretRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.secretRequest == nil {
		return nil
	}
	req := *s.secretRequest
	return &req
}

func (s *MockSink) ProposedTasks() []ProposedTask {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ProposedTask, len(s.proposedTasks))
	copy(out, s.proposedTasks)
	return out
}

func (s *MockSink) RecordProposedTask(task ProposedTask) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.proposedTasks = append(s.proposedTasks, task)
}

func (s *MockSink) ReviewComments() []ReviewComment {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ReviewComment, len(s.reviewComments))
	copy(out, s.reviewComments)
	return out
}

// NewMockTools returns the seven escape-hatch tools -- ask_question,
// request_secret, comment_on_issue, nothing_needed, write_handover, propose_task,
// add_review_comment -- four of them under the names v1 gave them, which
// is also the vocabulary orchestrator.ProcessResult matches against (see
// toolnames.go).
//
// "Mock" describes this process, not the effect. Nothing outside sink is
// touched from here, but every one of them really does reach the human:
// ProcessResult recovers each call from agent.Result.ToolCalls once the
// run finishes and relays it for real -- a question becomes a comment
// the task is parked on until somebody replies, a closing comment
// becomes a comment on the task, a proposal becomes an unapproved task
// with real depends_on edges, a review comment becomes a draft review on
// the pull request under review. So the descriptions and the confirmations
// below say *that*, in v2's own vocabulary (task, conversation, task
// queue), and say nothing about the sink.
//
// They used to say the opposite -- "mocked -- no GitHub comment was
// posted" -- on every production run, and describe v1's GitHub issue and
// trigger label besides. Neither has been true since tasks became rows,
// and the cost fell on the run: an agent told its question went nowhere
// asks it again, works around the block it was supposed to park on, or
// downgrades it into its final text, which nothing relays at all
// (docs/agent-ergonomics.md, finding 1).
//
// add_review_comment used to be the one exception -- nothing relayed it
// anywhere, for want of a pull request to attach a draft review to. A
// review dispatch has one, so all five are relayed now: see
// addReviewCommentTool for what a call becomes on a review and on
// everything else.
//
// proposeRepos is the one thing here that differs from one dispatch to
// the next: the repositories this deployment works in, which a
// propose_task call may name in its `repo` argument
// (model.Store.WorkRepos, carried in by pkg/cli/mcpserver.go's
// -propose-repo). It is folded into that argument's own description, so
// a run reads which repositories are on offer at the moment it is
// deciding what to write there -- see proposeTaskTool. Nil, which is
// every caller that has no store to ask, leaves the description exactly
// as it read before, naming the rule without the list.
func NewMockTools(sink *MockSink, proposeRepos []string) []Tool {
	return []Tool{
		askQuestionTool(sink),
		requestSecretTool(sink),
		commentOnIssueTool(sink),
		nothingNeededTool(sink),
		writeHandoverTool(),
		proposeTaskTool(sink, proposeRepos, "", nil),
		addReviewCommentTool(sink),
	}
}

func askQuestionTool(sink *MockSink) Tool {
	return Tool{
		Name: "ask_question",
		Description: "Ask the human a clarifying question when you cannot safely or " +
			"correctly proceed without their input. This ends your turn: " +
			"when this run finishes, grain relays the question into this " +
			"task's own conversation, in grain's UI, and parks the task on " +
			"it -- the task is not attempted again until a human replies " +
			"there, and their reply puts it back in the queue. The next " +
			"attempt is given that whole conversation, so the answer reaches " +
			"the run that carries on this work. Only the first ask_question " +
			"call in a run is relayed. Do not call this for routine progress " +
			"updates or when you can reasonably proceed on your own " +
			"judgment -- only when you are genuinely blocked. Give a one-line " +
			"summary as well as the question: the inbox shows only that line, " +
			"with the question behind an expander, so make it the decision you " +
			"need rather than \"I have a question\". After calling " +
			"this, do not take any further actions.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"question": map[string]any{
					"type":        "string",
					"description": "The question to ask the human. Include all necessary details and context so they can answer.",
				},
				"summary":     questionSummarySchema("the question"),
				"attachments": responseAttachmentsSchema(),
			},
			"required": []string{"question", "summary"},
		},
		Handler: func(ctx context.Context, args map[string]any) Result {
			if _, err := questionSummaryArg(args); err != nil {
				return Result{Text: err.Error(), IsError: true}
			}
			if _, err := ResponseAttachments(ctx, args, sink.attachmentFiles()); err != nil {
				return Result{Text: err.Error(), IsError: true}
			}
			question, ok := argString(args, "question")
			if !ok || question == "" {
				return Result{Text: "question is required", IsError: true}
			}
			sink.mu.Lock()
			sink.question = question
			sink.mu.Unlock()
			return Result{Text: "Recorded. When this run finishes, grain relays this question into the " +
				"task's conversation and parks the task there until a human replies. This ends " +
				"your turn: stop here and take no further actions."}
		},
	}
}

// requestSecretTool is ask_question for the one answer a question must
// never be used to get: a credential.
//
// It parks the task the same way, through the same
// Observation.PendingQuestionCommentID, and for the same reason -- the
// run cannot go on until a human acts. What differs is what the human is
// offered. A parked question offers a reply box, and a reply is a
// comment: stored in the task's conversation in plain text, shown to
// everyone who opens the task, and fed back into the next run's own
// prompt (orchestrator's commentThreadSection). A credential pasted
// there is a credential handed to an agent and written into grain's
// state repository as prose. So this call makes the UI offer a
// write-only box instead, whose value goes straight into the secret
// store (ui.Client.SetPendingSecret) and into no comment, no prompt and
// no tool result.
//
// The agent therefore never learns the value it asked for -- deliberately,
// and the description says so plainly rather than leaving a run to
// discover it by asking for the secret back. What a later run gets is
// not the material but the effect: whatever resolves that name -- a
// capability's own credential (model.CredentialRef), grain's own
// github/agent keys -- resolves where it previously did not.
func requestSecretTool(sink *MockSink) Tool {
	return Tool{
		Name: "request_secret",
		Description: "Ask the human to set a credential this deployment does not " +
			"have and this work needs -- an API key, a token, a password. " +
			"This ends your turn the way ask_question does: when this run " +
			"finishes, grain relays the request into this task's own " +
			"conversation, parks the task on it, and offers a box in the " +
			"task pane for the value. Only the first request_secret call " +
			"in a run is relayed. What the human types goes straight into " +
			"grain's encrypted secret store and is never shown to you: not " +
			"in this run, not in a later one, and there is nothing here " +
			"that reads a stored value back. A later run gets the use of " +
			"it, not the material -- whatever resolves that name resolves " +
			"where it did not before. For the same reason, never ask for a " +
			"credential through ask_question or comment_on_issue: a reply " +
			"there is a plain-text comment on the task, and it is fed back " +
			"to the agent. After calling this, do not take any further " +
			"actions.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"secret": map[string]any{
					"type": "string",
					"description": "The name to store it under, as grain resolves " +
						"one: \"stripe-api-key\" for a secret holding a single " +
						"value, or \"<secret>/<key>\" (\"github-app/app-id\") to " +
						"name one key of a secret that holds several. Use the " +
						"name whatever will consume it already looks up, if " +
						"something does.",
				},
				"reason": map[string]any{
					"type": "string",
					"description": "What the credential is, where the human gets " +
						"one, and what it will be used for -- this is all they " +
						"have to go on when deciding what to paste, and it is " +
						"relayed to them verbatim.",
				},
				"summary": questionSummarySchema("what you need set and why, such as " +
					"\"Set stripe-api-key so the deploy script can authenticate\""),
			},
			"required": []string{"secret", "reason", "summary"},
		},
		Handler: func(_ context.Context, args map[string]any) Result {
			if _, err := questionSummaryArg(args); err != nil {
				return Result{Text: err.Error(), IsError: true}
			}
			secret, ok := argString(args, "secret")
			if !ok || secret == "" {
				return Result{Text: "secret is required", IsError: true}
			}
			if !validSecretName(secret) {
				return Result{
					Text: fmt.Sprintf("%q is not a name grain can store a secret under: "+
						"give \"<secret>\" or \"<secret>/<key>\", each part non-empty and "+
						"free of spaces, backslashes and path components like \".\".", secret),
					IsError: true,
				}
			}
			reason, ok := argString(args, "reason")
			if !ok || reason == "" {
				return Result{
					Text: "reason is required: the human deciding what to paste is " +
						"told nothing else about it",
					IsError: true,
				}
			}
			sink.mu.Lock()
			// First call wins, matching what is actually relayed:
			// orchestrator.ProcessResult reads the first non-error
			// request_secret call off agent.Result.ToolCalls.
			if sink.secretRequest == nil {
				sink.secretRequest = &SecretRequest{Secret: secret, Reason: reason}
			}
			sink.mu.Unlock()
			return Result{Text: fmt.Sprintf("Recorded. When this run finishes, grain asks for %s in the "+
				"task's conversation and parks the task there until somebody sets it. The value "+
				"goes into grain's secret store and never comes back to you. This ends your turn: "+
				"stop here and take no further actions.", secret)}
		},
	}
}

// validSecretName reports whether name is one secrets.Store.Resolve
// could take: "<secret>" or "<secret>/<key>", each part non-empty, not
// "." or "..", and carrying no whitespace or backslash. It is the same
// rule secrets.validComponent applies at the point of writing, checked
// here so a run learns it named something unstorable in the turn it made
// the call rather than by being parked on a request no box can be built
// for.
func validSecretName(name string) bool {
	secret, key, explicit := strings.Cut(name, "/")
	if !validSecretComponent(secret) {
		return false
	}
	return !explicit || validSecretComponent(key)
}

func validSecretComponent(s string) bool {
	if s == "" || s == "." || s == ".." || strings.ContainsAny(s, "/\\") {
		return false
	}
	return !strings.ContainsFunc(s, unicode.IsSpace)
}

func commentOnIssueTool(sink *MockSink) Tool {
	return Tool{
		Name: "comment_on_issue",
		Description: "Leave a comment on this task. Despite the name there is no " +
			"GitHub issue behind it: when this run finishes, grain relays " +
			"the comment into the task's own conversation, in grain's UI, " +
			"where a human reads it and where the next attempt at this task " +
			"reads it back. Use this when the task only asked for an " +
			"answer, an investigation, or a recommendation -- not a code " +
			"change: if you never push a branch and never ask a question, " +
			"this comment becomes the task's closing note and completes " +
			"it. If you do push commits, a pull request is opened for them " +
			"regardless of whether you also call this -- calling it does " +
			"not by itself prevent a pull request from opening. Only the " +
			"first comment_on_issue call in a run is relayed, so say " +
			"everything you have to say in one call. After calling this, do " +
			"not take any further actions unless you still intend to push " +
			"commits or to end your turn with ask_question -- a comment and " +
			"a question are both relayed, in that order.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"comment": map[string]any{
					"type":        "string",
					"description": "The full text of the comment to post to this task's conversation. If this is a closing note or report, include the complete, detailed findings or report here -- never truncate it, summarize it with an ellipsis (…), or save the rest for a conversational response.",
				},
				"attachments": responseAttachmentsSchema(),
			},
			"required": []string{"comment"},
		},
		Handler: func(ctx context.Context, args map[string]any) Result {
			if _, err := ResponseAttachments(ctx, args, sink.attachmentFiles()); err != nil {
				return Result{Text: err.Error(), IsError: true}
			}
			comment, ok := argString(args, "comment")
			if !ok || comment == "" {
				return Result{Text: "comment is required", IsError: true}
			}
			sink.mu.Lock()
			sink.comment = comment
			sink.mu.Unlock()
			return Result{Text: "Recorded. When this run finishes, grain relays this comment into the " +
				"task's conversation; with no branch pushed and no question asked, it is the " +
				"task's closing note."}
		},
	}
}

// NothingNeededDescription is the sentence the nothing_needed tool and
// the agent prompt (orchestrator.BuildPrompt) both use to say when to
// reach for it, kept in one place so the two cannot drift apart.
const NothingNeededDescription = "use this when, after looking, the task needs no " +
	"change and no report anyone has to act on"

// nothingNeededTool is comment_on_issue for the run that has nothing to
// report: it ends the run the same way, its reason relayed as the task's
// closing comment, but orchestrator.ProcessResult records the run as
// model.EndingNothingNeeded and -- unless the task says
// model.NothingNeededAck -- closes the task rather than leaving it in
// awaiting_ack for somebody to read. Pushed commits and a question still
// win over it, as they do over a closing comment: the work is not
// discarded, and the reason is posted beside it.
func nothingNeededTool(sink *MockSink) Tool {
	return Tool{
		Name: "nothing_needed",
		Description: "End this task as needing no work: " + NothingNeededDescription + ". " +
			"Calling it ends your run the way comment_on_issue does: when this run " +
			"finishes, grain posts your reason as the task's closing comment and, unless " +
			"the task asks to be left for acknowledgement, closes the task -- it does not " +
			"wait in anybody's inbox. If you pushed commits or asked a question in this " +
			"run, that work still wins: a pull request is opened, or the task is parked on " +
			"your question, and the reason is posted beside it. Do not use this for a " +
			"finding, an answer or a recommendation somebody should read -- that is " +
			"comment_on_issue. After calling this, do not take any further actions.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"reason": map[string]any{
					"type":        "string",
					"description": "Why nothing is needed -- what you looked at and what you found. Posted as the task's closing comment.",
				},
			},
			"required": []string{"reason"},
		},
		Handler: func(_ context.Context, args map[string]any) Result {
			reason, ok := argString(args, "reason")
			if !ok || strings.TrimSpace(reason) == "" {
				return Result{Text: "reason is required", IsError: true}
			}
			sink.mu.Lock()
			sink.nothingNeeded = reason
			sink.mu.Unlock()
			return Result{Text: "Recorded. When this run finishes, grain posts this reason as the task's " +
				"closing comment and ends the task as needing no work. Take no further actions."}
		},
	}
}

// writeHandoverTool is comment_on_issue for the one reader a closing note
// is not written for: the next run on this task.
//
// A task's conversation is carried into every dispatch's prompt and is
// bounded there -- both ends kept, the middle cut, the gap marked
// (orchestrator.commentThreadSection). The bound is what keeps a
// week-old chat from crowding out the instructions it is wrapped around,
// and what it cannot do is choose *by meaning*: it keeps the oldest and
// newest messages because that is where a thread's load-bearing material
// usually is, not because it read them. The editorial judgement belongs
// where the context actually is, which is inside the run.
//
// So a run whose prompt says the thread was cut is asked, in that same
// prompt, to write down what the older part settled -- and this is the
// tool it writes it with. What it produces is an ordinary comment in the
// task's conversation, marked as a handover (model.Comment.Handover), and
// the prompt builder carries the most recent one outside the head/tail
// budget it cuts everything else to. That is the whole of what the mark
// buys: the one message written to survive the cut is not one the cut can
// drop.
//
// Deliberately not comment_on_issue with an argument, for two reasons.
// Only the first comment_on_issue call in a run is relayed, so a run that
// owes a closing note and a handover would have to choose between them.
// And a relayed comment_on_issue completes the task
// (orchestrator.ProcessResult) -- which is exactly wrong for a note whose
// entire purpose is that the task stays open and is dispatched again.
//
// Unlike every other escape hatch here, the *last* call wins rather than
// the first: a handover is a document a run revises as it learns what the
// thread was actually about, and discarding the revision in favour of the
// first draft would be the one failure this tool exists to prevent, one
// turn earlier. orchestrator.relayHandover reads it the same way round.
func writeHandoverTool() Tool {
	return Tool{
		Name: "write_handover",
		Description: "Write this task's handover: a standing summary of what the older " +
			"part of this task's conversation settled, for the next run on this task " +
			"rather than for a person. Use it when grain has told you in your prompt " +
			"that this task's conversation is longer than one prompt can carry and has " +
			"been cut -- what you write here is carried into every later prompt on this " +
			"task outside the part that gets cut, so it is the one thing that survives " +
			"the thread growing. Write down the decisions, the invariants, the names and " +
			"the things already ruled out; say what was settled rather than what was " +
			"discussed, and do not repeat messages the prompt still quotes in full. A few " +
			"hundred words. This is not a closing note and it does not complete the task " +
			"or stand in for comment_on_issue, ask_question or a pull request -- it is a " +
			"note to whoever picks this task up next, which may be you. Calling it again " +
			"replaces what you wrote: the last call in a run is the one relayed.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"handover": map[string]any{
					"type": "string",
					"description": "The summary itself, in your own words and in full -- it is " +
						"read by an agent with none of the conversation you are summarizing " +
						"in front of it, so state facts and decisions plainly rather than " +
						"referring back to \"the discussion above\".",
				},
			},
			"required": []string{"handover"},
		},
		Handler: func(_ context.Context, args map[string]any) Result {
			handover, ok := argString(args, "handover")
			if !ok || strings.TrimSpace(handover) == "" {
				return Result{Text: "handover is required: say what the older part of this " +
					"task's conversation settled", IsError: true}
			}
			return Result{Text: "Recorded. When this run finishes, grain posts this in the task's " +
				"conversation marked as its handover, and carries it into every later prompt " +
				"on this task outside the part of the thread that gets cut. It is not a " +
				"closing note and does not complete the task, so carry on with the work."}
		},
	}
}

// proposalRepoSentence is what the `repo` argument says about the
// repositories on offer, for a dispatch that was told which those are
// (NewMockTools' proposeRepos, pkg/cli/mcpserver.go's -propose-repo)
// -- and "" for one that was not, which leaves the argument's
// description as it has always read.
//
// The list is the answer to the one question the rest of that
// description cannot answer: it says a repository this deployment works
// in may be named, and a run with no way to find out which those are has
// to already know the owner/name from its task body or guess. Guessing
// is the failure this argument exists to prevent, and it is silent --
// the proposal is filed against the run's own repository with a note,
// and nobody finds out until a human retargets it by hand (grain/task-144).
//
// It does not claim to be the whole list, and must not: the caller caps
// it, so what a run is holding is the repositories there was room to
// name. The sentence beside it already covers what happens to any other
// repository -- filed against yours, with a note -- so a run reading
// both learns the two things it needs: naming one of these costs
// nothing, and naming something else might cost a correction.
func proposalRepoSentence(repos []string) string {
	var named []string
	for _, repo := range repos {
		if repo = strings.TrimSpace(repo); repo != "" {
			named = append(named, repo)
		}
	}
	if len(named) == 0 {
		return ""
	}
	return " Repositories this deployment works in, most recently worked in first: " +
		strings.Join(named, ", ") + ". Naming one of those files the proposal there."
}

// NewLiveProposalTools returns propose_task with immediate durable filing.
// Register it after NewMockTools to replace only that tool in the registry.
func NewLiveProposalTools(sink *MockSink, proposeRepos []string, parent string, filer ProposalFiler) []Tool {
	return []Tool{proposeTaskTool(sink, proposeRepos, parent, filer)}
}

func proposeTaskTool(sink *MockSink, proposeRepos []string, parent string, filer ProposalFiler) Tool {
	filing := "Each proposal is filed as a real task once this run finishes"
	if filer != nil {
		filing = "Each proposal is filed immediately as a real task and the result returns its task number and UI link"
	}
	return Tool{
		Name: "propose_task",
		Description: "Propose a follow-up or out-of-scope task for the queue. " + filing +
			", linked to this task: queued when your task's permission covers it, or held " +
			"unapproved for human review (pass hold_for_approval: true to hold regardless; " +
			"keep_running or a priority above your task's always holds). " +
			"Set depends_on for tasks it must wait on, repo if the work belongs in another " +
			"repository, or supersedes to replace an undispatched task. Run 'grain help propose' " +
			"for full reference.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": withProposeChangeTargetProperties(map[string]any{
				"id":     map[string]any{"type": "string"},
				"title":  map[string]any{"type": "string"},
				"body":   map[string]any{"type": "string"},
				"branch": map[string]any{"type": "string"},
				"repo": map[string]any{
					"type":        "string",
					"description": "owner/name of target repository (defaults to yours)." + proposalRepoSentence(proposeRepos),
				},
				"custom_fields": map[string]any{
					"type": "object",
					"additionalProperties": map[string]any{
						"anyOf": []any{
							map[string]any{"type": "string"},
							map[string]any{
								"type":  "array",
								"items": map[string]any{"type": "string"},
							},
						},
					},
				},
				"depends_on": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "string"},
				},
				"supersedes": map[string]any{
					"type": "string",
					"description": "ID of a task in the same repository that has never been dispatched " +
						"to replace; a dispatched task is left open.",
				},
				"auto_merge": map[string]any{"type": "boolean"},
				"reads": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "string"},
				},
				"writes": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type":                 "object",
						"additionalProperties": false,
						"properties": map[string]any{
							"repo": map[string]any{"type": "string"},
							"base": map[string]any{"type": "string"},
						},
						"required": []string{"repo"},
					},
				},
				"review_template_id": map[string]any{"type": "string"},
				"sandbox_cpus":       map[string]any{"type": "integer"},
				"sandbox_memory_mb":  map[string]any{"type": "integer"},
				"sandbox_disk_gb":    map[string]any{"type": "integer"},
				"network_access": map[string]any{
					"type": "string",
					"enum": networkAccessEnum(),
				},
				"goal": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"kind":     map[string]any{"type": "string", "enum": []string{"base-green", "pr-merged", "tasks-closed", "person-closes", "pipeline-healthy", "cl-submitted"}},
						"spec":     map[string]any{"type": "object"},
						"on_met":   map[string]any{"type": "string", "enum": []string{"close", "rest"}},
						"on_limit": map[string]any{"type": "string", "enum": []string{"hold", "notify"}},
					},
					"required": []string{"kind"},
				},
				"nothing_needed_action": map[string]any{
					"type": "string",
					"enum": []any{"close", "ack"},
				},
				"keep_running":      map[string]any{"type": "boolean"},
				"no_port":           map[string]any{"type": "boolean"},
				"fixes_base_branch": map[string]any{"type": "boolean"},
				"origin":            map[string]any{"type": "string"},
				"no_repo":           map[string]any{"type": "boolean"},
				"at_front":          map[string]any{"type": "boolean"},
				"priority": map[string]any{
					"type":        "integer",
					"description": "1 or more (default 5; higher runs first). Not inherited from your task",
				},
				"template_id": map[string]any{"type": "string"},
				"template_fields": map[string]any{
					"type": "object",
					"additionalProperties": map[string]any{
						"type": "string",
					},
				},
				"capabilities": map[string]any{
					"type": "array",
					"items": map[string]any{"anyOf": []any{
						map[string]any{"type": "string"},
						map[string]any{
							"type": "object",
							"properties": map[string]any{
								"capability": map[string]any{"type": "string"},
								"with":       map[string]any{"type": "object"},
							},
							"required":             []string{"capability"},
							"additionalProperties": false,
						},
					}},
				},
				"model":             map[string]any{"type": "string"},
				"agent_framework":   map[string]any{"type": "string"},
				"not_before":        map[string]any{"type": "string"},
				"hold_for_approval": map[string]any{"type": "boolean"},
			}),
			"required": []string{"title", "body"},
		},
		Handler: func(ctx context.Context, args map[string]any) Result {
			title, hasTitle := argString(args, "title")
			body, hasBody := argString(args, "body")
			templateID, _ := argString(args, "template_id")
			templateID = strings.TrimSpace(templateID)
			if templateID == "" && (!hasTitle || !hasBody || strings.TrimSpace(title) == "" || strings.TrimSpace(body) == "") {
				return Result{Text: "title and body are required", IsError: true}
			}
			id, _ := argString(args, "id")
			repo, _ := argString(args, "repo")
			branch, _ := argString(args, "branch")
			noRepo, _ := argBool(args, "no_repo")
			changeTarget, err := ProposedChangeTarget(args)
			if err != nil {
				return Result{Text: err.Error(), IsError: true}
			}
			if changeTarget != nil {
				if refusal := changeTargetRefusal(changeTarget.Kind, args); refusal != "" {
					return Result{Text: refusal, IsError: true}
				}
			}
			var atFront *bool
			if front, ok := argBool(args, "at_front"); ok {
				atFront = &front
			} else if pos, ok := argString(args, "position"); ok {
				if pos == "front" {
					front := true
					atFront = &front
				} else if pos == "end" {
					front := false
					atFront = &front
				}
			}
			supersedes, _ := argString(args, "supersedes")
			reviewTemplateID, _ := argString(args, "review_template_id")
			priority, hasPriority := argInt(args, "priority")
			if hasPriority && priority < 1 {
				return Result{Text: "priority must be a positive integer (1 or more; leave it out for the default, 5)", IsError: true}
			}
			cpus, _ := argInt(args, "sandbox_cpus")
			memoryMB, _ := argInt(args, "sandbox_memory_mb")
			diskGB, _ := argInt(args, "sandbox_disk_gb")
			networkAccess, _ := argString(args, "network_access")
			// Refused rather than passed on: every sandbox backend
			// withholds the network only for model.NetworkNone exactly,
			// so a word grain cannot build would otherwise be filed and
			// dispatched with an open network.
			networkAccess = strings.TrimSpace(networkAccess)
			if networkAccess != "" && !model.ValidNetworkAccess(model.NetworkAccess(networkAccess)) {
				return Result{Text: fmt.Sprintf("network_access %q is not one grain can build a sandbox for: "+
					"use %s, or leave it out for the deployment default.",
					networkAccess, model.NetworkAccessNames()), IsError: true}
			}
			nothingNeeded, _ := argString(args, "nothing_needed_action")
			goal, err := model.ParseGoalArg(args["goal"])
			if err != nil {
				return Result{Text: fmt.Sprintf("goal: %v", err), IsError: true}
			}
			model, _ := argString(args, "model")
			agentFramework, _ := argString(args, "agent_framework")
			var notBefore *time.Time
			if raw, _ := argString(args, "not_before"); strings.TrimSpace(raw) != "" {
				t, err := ParseNotBefore(raw)
				if err != nil {
					return Result{Text: err.Error(), IsError: true}
				}
				// Past already: nothing to wait for, and saying the
				// task is deferred would be untrue (ui.CreateTaskRequest.
				// DeferredUntil files it undeferred).
				if t.After(time.Now()) {
					notBefore = &t
				}
			}
			capabilities, err := argCapabilities(args, "capabilities")
			if err != nil {
				return Result{Text: err.Error(), IsError: true}
			}
			if err := refuseReviewOfGrants(capabilities); err != nil {
				return Result{Text: err.Error(), IsError: true}
			}
			task := ProposedTask{
				ID:                  id,
				Title:               title,
				Body:                body,
				Repo:                repo,
				Branch:              strings.TrimSpace(branch),
				Priority:            priority,
				NoRepo:              noRepo,
				ChangeTarget:        changeTarget,
				AtFront:             atFront,
				TemplateID:          templateID,
				TemplateFields:      argStringMap(args, "template_fields"),
				Supersedes:          strings.TrimSpace(supersedes),
				DependsOn:           argStringSlice(args, "depends_on"),
				Capabilities:        capabilities,
				CustomFields:        argCustomFields(args, "custom_fields"),
				Reads:               argStringSlice(args, "reads"),
				Writes:              argWrites(args, "writes"),
				ReviewTemplateID:    strings.TrimSpace(reviewTemplateID),
				SandboxCPUs:         cpus,
				SandboxMemoryMB:     memoryMB,
				SandboxDiskGB:       diskGB,
				NetworkAccess:       networkAccess,
				NothingNeededAction: strings.TrimSpace(nothingNeeded),
				Goal:                goal,
				Model:               strings.TrimSpace(model),
				AgentFramework:      strings.TrimSpace(agentFramework),
				NotBefore:           notBefore,
			}
			if autoMerge, ok := argBool(args, "auto_merge"); ok {
				task.AutoMerge = &autoMerge
			}
			if noPort, ok := argBool(args, "no_port"); ok {
				task.NoPort = noPort
			}
			if keepRunning, ok := argBool(args, "keep_running"); ok {
				task.KeepRunning = keepRunning
			}
			if fixes, ok := argBool(args, "fixes_base_branch"); ok {
				task.FixesBaseBranch = fixes
			}
			if origin, ok := argString(args, "origin"); ok {
				task.Origin = strings.TrimSpace(origin)
			}
			if hold, ok := argBool(args, "hold_for_approval"); ok {
				task.HoldForApproval = hold
			}

			if filer != nil {
				key := ProposalKeyArgs(parent, args)
				filed, err := filer.FileProposal(ctx, task, key)
				if err != nil {
					return Result{Text: "Could not file proposal: " + err.Error(), IsError: true}
				}
				standing := proposalStanding(task, filed)
				return Result{Text: fmt.Sprintf("Proposed task #%s is visible now: %s. %s%s%s",
					filed.ID, relativeProposalURL(filed), standing, notBeforeOutcome(task.NotBefore),
					supersedeOutcome(task.Supersedes, filed))}
			}

			sink.mu.Lock()
			sink.proposedTasks = append(sink.proposedTasks, task)
			n := len(sink.proposedTasks)
			sink.mu.Unlock()

			recorded := fmt.Sprintf("Recorded (%d proposed task(s) so far this run). Each is "+
				"filed as a real task in grain's queue when this run finishes -- unapproved until "+
				"a human approves it there, unless your own task's permission already covers it.", n)
			if task.HoldForApproval {
				recorded += " This one you asked to hold, so it is filed unapproved and waits for " +
					"a human whatever your permission covers."
			}
			recorded += notBeforeOutcome(task.NotBefore)
			if task.Supersedes != "" {
				recorded += fmt.Sprintf(" Task %s is closed as superseded by it at the same "+
					"moment, if it is still a task that may be -- one already dispatched, or in "+
					"another repository, is left open with a note on this proposal saying so. "+
					"Open tasks waiting on task %s are moved to wait on this one instead, each "+
					"with a comment saying so.",
					task.Supersedes, task.Supersedes)
			}
			return Result{Text: recorded}
		},
	}
}

// proposalStanding is what a live propose_task call is told about whether
// the task it just filed waits for a person, and why: queued on the
// caller's own permission, held because the call asked, or filed
// unapproved for a reason the filer gives (FiledProposal.ApprovalNote).
//
// The why is said, not only the what, because hold_for_approval makes the
// answer partly the run's own choice: a run that asked to hold a
// proposal and is told only "unapproved" cannot tell whether its request
// was the reason or its permission was, and so cannot say which in chat.
//
// A proposal filing parked (FiledProposal.Parked) is said to be parked
// as well, and never "queued": an approved one parked on a repo off the
// target repo list is not dispatched in its turn, and a run told it was
// would pass that on as fact -- or, holding one, conclude its hold had
// not worked.
func proposalStanding(task ProposedTask, filed FiledProposal) string {
	if filed.Queued {
		if filed.Parked != "" {
			return "It is filed approved -- your own task's permission covers it -- but it is " +
				"not queued: filing parked it awaiting a reply, and it is not dispatched until an " +
				"operator lifts that. " + parkedReason(filed.Parked)
		}
		return "It is filed approved -- your own task's permission covers it -- so it is " +
			"queued already and dispatched in its turn without anybody approving it."
	}
	var standing string
	switch {
	case filed.ApprovalNote != "":
		standing = "It is filed unapproved because " + filed.ApprovalNote +
			". It waits until a human approves it."
	case task.HoldForApproval:
		standing = "It is filed unapproved because you asked to hold it (hold_for_approval). " +
			"It waits until a human approves it."
	default:
		standing = "It is filed unapproved: your permission does not cover it. It waits until a human approves it."
	}
	if filed.Parked != "" {
		standing += " It is also parked awaiting a reply, and a reply alone does not approve it. " +
			parkedReason(filed.Parked)
	}
	return standing
}

// parkedReason quotes the comment filing parked a proposal on, which
// says what lifts the park in grain's own words (model.
// OffAllowlistParkComment).
func parkedReason(note string) string {
	return "Parked because: " + strings.TrimSpace(note)
}

// ParseNotBefore reads propose_task's `not_before`: a bare date, taken
// as the start of that day in UTC, or an RFC 3339 time. A bare date is
// accepted because it is what somebody scheduling work for later usually
// has in mind, the same reason `grain defer -until` takes one. The
// end-of-run relay (orchestrator.relayProposedTasks) parses the same
// argument with this, so the two filing paths cannot disagree about
// what a value means.
func ParseNotBefore(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if t, err := time.Parse("2006-01-02", raw); err == nil {
		return t.UTC(), nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("not_before %q: want a date (2026-10-14) or an RFC 3339 time (2026-10-14T09:00:00Z)", raw)
	}
	return t.UTC(), nil
}

// notBeforeOutcome is what a propose_task call is told about the wake
// time it asked for, empty for one that asked for none.
func notBeforeOutcome(notBefore *time.Time) string {
	if notBefore == nil {
		return ""
	}
	return fmt.Sprintf(" It is filed deferred and cannot start before %s, when it picks itself back up.",
		notBefore.Format(time.RFC3339))
}

func relativeProposalURL(filed FiledProposal) string {
	raw := strings.TrimSpace(filed.URL)
	if raw == "" {
		return "/tasks/" + filed.ID
	}
	if u, err := url.Parse(raw); err == nil && u.Scheme != "" && u.Host != "" {
		host := u.Hostname()
		if host == "127.0.0.1" || strings.EqualFold(host, "localhost") || host == "::1" {
			if u.Path != "" {
				return u.RequestURI()
			}
			return "/tasks/" + filed.ID
		}
	}
	return raw
}

// supersedeOutcome is what a live propose_task call is told about the
// task it asked to supersede: that it is closed, or that it is not and
// where the reason is.
//
// Said plainly rather than left for the run to infer, for the reason
// FiledProposal.Queued is reported: the whole point of the argument is
// that a stale task stops being dispatchable, and a run that believes a
// close happened when it did not will neither say so in its own closing
// note nor ask anybody to do it by hand.
func supersedeOutcome(asked string, filed FiledProposal) string {
	switch {
	case asked == "":
		return ""
	case filed.Superseded != "":
		out := fmt.Sprintf(" Task %s is closed as superseded by it.", filed.Superseded)
		if len(filed.SupersedeMoved) > 0 {
			out += fmt.Sprintf(" Task(s) %s were waiting on task %s and now wait on #%s instead.",
				hashList(filed.SupersedeMoved), filed.Superseded, filed.ID)
		}
		for _, k := range filed.SupersedeKept {
			out += fmt.Sprintf(" Task #%s was waiting on task %s and was not moved: %s.",
				k.ID, filed.Superseded, k.Reason)
		}
		return out
	}
	return fmt.Sprintf(" Task %s is still open: grain would not close it -- a task that has "+
		"already been dispatched, has a pull request, lives in another repository or does not "+
		"exist is left for a human. The note on the new task says which.", asked)
}

// hashList is ids written "#1, #2".
func hashList(ids []string) string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = "#" + id
	}
	return strings.Join(out, ", ")
}

// addReviewCommentTool used to be the one escape hatch with nothing
// downstream of it: its calls were recovered from agent.Result.ToolCalls
// like the others' and then relayed nowhere, because attaching a draft
// review needs a pull request in hand and no dispatch had one.
//
// A review dispatch does (orchestrator.SyncReviews), so
// orchestrator.relayReviewFeedback now posts these as a real draft review
// on the pull request under review, repeats them on that change's own
// task, and takes it off automatic merge until a human has read them. A
// run with no pull request behind it has them relayed into its own task's
// conversation rather than dropped.
//
// The description says both halves rather than only the first, because
// which one a run gets is not something the run can see from here: this
// process serves every dispatch alike, and whether the task it belongs to
// is a review is a fact about the store, two packages away. Saying "the
// pull request under review, or this task's conversation" is true on
// every run, where a description tailored to one of them would be a
// promise broken on the other. Narrowing it properly -- registering the
// review wording only for a dispatch that is actually a review, behind a
// `grain mcpserver` flag the way -pr-repo/-pr-branch scope the CI tools
// (docs/agent-ergonomics.md, finding 2) -- is worth doing now that
// something could set that flag, and is not this change.
func addReviewCommentTool(sink *MockSink) Tool {
	return Tool{
		Name: "add_review_comment",
		Description: "Record one piece of code-review feedback. Give both `path` " +
			"and `line` to attach the comment to a specific line of a " +
			"specific file, as shown in the diff against the base branch; " +
			"omit both for a general remark that isn't tied to one line. " +
			"Call this once per point of feedback. When this run finishes, " +
			"grain relays every call: if this task is reviewing another " +
			"task's change (its own description says which pull request), " +
			"they become a draft review on that pull request, on the lines " +
			"they name, and are repeated on the reviewed task's own " +
			"conversation as open questions -- and that change stops merging " +
			"until its owner decides: their reply sends another run to carry " +
			"the decision out. If this run is not reviewing a pull request, " +
			"they are relayed into this task's conversation instead. Use " +
			"this only for what you cannot settle without the owner -- a " +
			"design decision, a trade-off, a behaviour change the brief does " +
			"not settle -- written as a question with the answer you " +
			"recommend. Fix everything else yourself (bugs, missing tests, " +
			"docs that no longer match) and push it; never leave a finding " +
			"here that you could have fixed.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"body": map[string]any{"type": "string"},
				"path": map[string]any{
					"type": "string",
					"description": "File path, relative to the repo root, the comment " +
						"applies to. Omit for a general remark.",
				},
				"line": map[string]any{
					"type": "integer",
					"description": "Line number in the new version of the file (as " +
						"shown in the diff) the comment applies to. " +
						"Required if path is given.",
				},
			},
			"required": []string{"body"},
		},
		Handler: func(_ context.Context, args map[string]any) Result {
			body, ok := argString(args, "body")
			if !ok || strings.TrimSpace(body) == "" {
				return Result{Text: "body is required", IsError: true}
			}
			path, hasPath := argString(args, "path")
			path = strings.TrimSpace(path)
			if path == "" {
				hasPath = false
			}
			lineF, hasLine := argFloat(args, "line")
			if hasLine && int(lineF) <= 0 {
				hasLine = false
			}
			if hasPath != hasLine {
				return Result{
					Text: "Give both path and line to attach this to a specific " +
						"line, or neither for a general remark -- not just one.",
					IsError: true,
				}
			}

			comment := ReviewComment{Body: body, HasLocation: hasPath}
			if hasPath {
				comment.Path = path
				comment.Line = int(lineF)
			}

			sink.mu.Lock()
			sink.reviewComments = append(sink.reviewComments, comment)
			n := len(sink.reviewComments)
			sink.mu.Unlock()

			return Result{Text: fmt.Sprintf("Recorded (%d review comment(s) so far this run). When this "+
				"run finishes, grain posts these as a draft review on the pull request under "+
				"review and repeats them on that task's conversation, which also stops it "+
				"merging automatically until a human has read them -- or, if this run is not "+
				"reviewing a pull request, relays them into this task's own conversation.", n)}
		},
	}
}

// networkAccessEnum is propose_task's network_access enum, built from
// model.NetworkAccesses so the schema can never offer an access grain
// has no sandbox for.
func networkAccessEnum() []any {
	names := model.NetworkAccesses()
	out := make([]any, len(names))
	for i, n := range names {
		out[i] = string(n)
	}
	return out
}
