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

// chat_tool.go is a conversation held inside one run, rather than one run
// per thing said.
//
// A task already carries a conversation, and a person can already type
// into it while a run is in flight (orchestrator's addendaPoller,
// ui.Client.AddComment). What they could not do is get an answer back
// without the run ending first: everything a run says is recovered from
// agent.Result once the process exits (orchestrator.ProcessResult
// relaying ask_question and comment_on_issue), so a two-line exchange
// cost two dispatches, two sandboxes and a cold prompt each way -- and a
// message that landed after a run finished waited for the next dispatch
// to be read at all.
//
// So this tool blocks. It posts what the run has to say, parks the task
// on it so the task list reads awaiting_reply the instant the run pauses,
// and waits for a person to answer -- inside a single tool call, with the
// agent's own context still standing behind it. The shape is
// pkg/capability/selfrepair's Confirm, which has described exactly this
// since before anything could call it; what makes it callable now is that
// the waiting happens over the daemon's REST API (Chatter, implemented by
// pkg/cli/mcpserver.go's daemonChat) rather than against a
// *model.Store the forked process does not have.
//
// Three consequences worth stating rather than discovering:
//
//   - It works for every framework. A blocking tool call needs nothing of
//     the framework but that it lets the call block, which claude, codex
//     and antigravity all do -- where agent.RunConfig.Addenda, the seam
//     meant for this, is polled by none of them and never has been.
//     addenda.go reaches a live run the other way round, through a tool
//     result rather than a tool call, and the two are complementary:
//     this is the run asking a person something and waiting, and that is
//     a person interrupting a run that asked nothing.
//   - A wait is not work. One turn of chat can stand still for the best
//     part of an hour, which is most of an ordinary run's whole budget --
//     so an Interactive dispatch is given a clock of its own to spend it
//     against (orchestrator.DefaultMaxInteractiveRuntime), rather than
//     the two hours a task nobody is watching gets.
//   - A wait that ends without a reply leaves the task parked, which is
//     the honest state: the message is in the conversation, the task
//     reads awaiting_reply, and the person's next message un-parks it
//     (ui.Client.AddComment) and dispatches a fresh run with the whole
//     thread in front of it. Nothing is lost by a session ending; the
//     conversation simply goes back to costing a run per turn.

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ChatWait is how long one chat turn blocks on a person before it gives
// up and lets the task park on the message instead.
//
// Long, because the failure it trades against is a session that ends
// while somebody is still typing, and the cost of waiting is a sandbox
// standing idle -- an agent blocked in a tool call asks its provider for
// nothing. It is exported because a client with a tool-call timeout of
// its own has to be told a number larger than this one or it kills the
// call before the wait can end cleanly (MaxBlockingToolWait, and
// agent/claude's mcpToolTimeout).
const ChatWait = 60 * time.Minute

// chatWaitSlack is how much of the run's remaining time a chat turn
// refuses to spend, so that a reply arriving at the last moment still has
// a turn to be acted on rather than landing in a run being cancelled.
const chatWaitSlack = 2 * time.Minute

// minChatWait is the shortest wait worth blocking for. Below it the tool
// posts the message and returns at once: a run with ninety seconds left
// cannot hold a conversation, and the useful half of the call -- the
// message being said and the task being parked on it -- does not need the
// wait at all.
const minChatWait = 1 * time.Minute

// MaxBlockingToolWait is the longest a single tool call served by this
// package can block: the larger of one chat turn and one wait_for_checks.
//
// It exists for the agent CLIs, each of which caps a single MCP tool call
// and would otherwise kill a wait grain deliberately started
// (agent/claude's MCP_TOOL_TIMEOUT, agent/antigravity's per-server
// timeoutSeconds). Naming the maximum here rather than in each of them is
// what keeps a new blocking tool from silently outliving the cap that was
// sized for the old one.
var MaxBlockingToolWait = max(ChatWait, MaxWaitForChecksTimeout)

// ChatTurn is what one chat call came back with: what the person said, or
// nothing at all if the wait ran out first.
//
// An empty Reply is not an error. Nobody being at the keyboard is an
// ordinary way for a chat turn to end, and what it means for the run --
// the task is parked, their next message starts a fresh dispatch -- is
// something to be told, not something to fail over.
type ChatTurn struct {
	Reply string
}

// Chatter says something in the task's own conversation, on behalf of the
// run this server serves, and waits up to wait for a person to answer.
//
// An interface, and one that names no task, for the reason StatusReporter
// names none: which conversation a message lands in is fixed at process
// start (`grain mcpserver -task`), never by anything in a tool call, so a
// run can only ever talk about itself. The real implementation
// (pkg/cli/mcpserver.go's daemonChat) posts over the daemon's REST API
// and polls it for the reply, since the conversation lives there.
//
// wait of zero or less means "post it and come back" -- what a run too
// close to its own deadline to hold a conversation still wants, since the
// message and the park are worth having on their own.
type Chatter interface {
	Chat(ctx context.Context, message, summary string, attachments []ResponseAttachment, wait time.Duration) (ChatTurn, error)
}

// NewChatTools returns the one tool an interactive run gets: chat.
//
// Registered only for a task filed as Interactive (model.Task.Interactive,
// carried to the forked server by agent.RunConfig.Interactive and
// `mcpserver -interactive`), because the tool's whole promise is that
// somebody is on the other end right now. On a background run it would
// promise a human who is not there, and every call would cost the run an
// hour of standing still before finding out.
//
// chatter nil returns the tool anyway, refusing every call -- what lets
// each agent framework's allowedTools enumerate the names this package
// registers without holding a live Chatter, exactly as NewStatusTools and
// NewRecreateSandboxTools do.
//
// read is how an attachment given by path is read out of the run's
// sandbox (SandboxFileReaderOf); nil refuses one. Nothing is staged:
// chat posts its attachments as it is called.
func NewChatTools(chatter Chatter, read SandboxFileReader) []Tool {
	return []Tool{chatTool(chatter, read)}
}

func chatTool(chatter Chatter, read SandboxFileReader) Tool {
	return Tool{
		Name: "chat",
		ElisionAdvice: "they are still there -- ask them, in another chat call, for the part " +
			"of it you need rather than for the whole of it again",
		Description: "Say something to the human sitting with this task, in the task's own " +
			"chat, and wait for their reply -- a real back-and-forth inside this run, not " +
			"a message relayed after it ends. Use it whenever you want their answer before " +
			"going on: what they meant, which of two ways to do it, whether what you just " +
			"did is what they wanted, or simply to tell them what you found. The call " +
			"blocks until they reply (up to " + ChatWait.String() + "), so their " +
			"answer comes back as this call's own result and you keep everything you " +
			"already have in front of you -- your sandbox, your reading of the code, the " +
			"conversation so far. While it waits, this task reads 'Asked' in " +
			"grain's UI, so they can see you are waiting on them. " +
			"If nobody answers before the wait runs out, your message stays in the " +
			"conversation and the task stays parked on it: finish or stop rather than " +
			"calling this again to keep waiting, because their next message starts a " +
			"fresh run with this whole thread in front of it. " +
			"This is the tool for a conversation; ask_question is what a background run " +
			"uses to stop and be answered later, and comment_on_issue is for a closing " +
			"note nobody has to answer. Give each message a one-line summary too: while " +
			"you wait, the inbox and the task list show only that line.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"message": map[string]any{
					"type": "string",
					"description": "What to say to them, in your own words. It is posted as a " +
						"message from you in this task's chat, so write it as one side of a " +
						"conversation rather than as a report: say what you need from them, or " +
						"what you want them to know, and stop there.",
				},
				"summary":     questionSummarySchema("the message"),
				"attachments": responseAttachmentsSchema(),
			},
			"required": []string{"message", "summary"},
		},
		Handler: func(ctx context.Context, args map[string]any) Result {
			if chatter == nil {
				return Result{
					Text: "This run cannot chat (no route back to the grain daemon that " +
						"dispatched it). Say what you have to say with comment_on_issue, or " +
						"ask_question if you need an answer before anything else can happen.",
					IsError: true,
				}
			}
			message, _ := argString(args, "message")
			message = strings.TrimSpace(message)
			if message == "" {
				return Result{Text: "message is required: say something to the human watching this task",
					IsError: true}
			}
			summary, err := questionSummaryArg(args)
			if err != nil {
				return Result{Text: err.Error(), IsError: true}
			}
			attachments, err := ResponseAttachments(ctx, args, AttachmentFiles{Read: read})
			if err != nil {
				return Result{Text: err.Error(), IsError: true}
			}
			wait, tooLate := chatWaitBudget(ctx)
			turn, err := chatter.Chat(ctx, message, summary, attachments, wait)
			if err != nil {
				return Result{Text: fmt.Sprintf("could not reach this task's chat: %v", err), IsError: true}
			}
			switch {
			case turn.Reply != "":
				return Result{Text: fmt.Sprintf(
					"They replied:\n\n%s\n\n[grain] Carry on from there. Another chat call "+
						"reaches them again, in this same run.", turn.Reply), HumanTurn: true}
			case tooLate:
				return Result{Text: "Posted, but this run is too close to its deadline to wait " +
					"for an answer. The task is parked on what you said, so it is not lost: " +
					"push what works and stop, and their reply will start a fresh run."}
			default:
				return Result{Text: fmt.Sprintf(
					"Nobody replied within %s. What you said is in the task's conversation and "+
						"the task is parked on it, so their answer is not lost -- it will start "+
						"a fresh run with this whole thread in front of it. Finish what you can "+
						"and stop rather than calling chat again to keep waiting.",
					roundDuration(wait))}
			}
		},
	}
}

// chatWaitBudget is how long this call may actually block: one chat turn,
// or what is left of the run less a turn to act on the answer, whichever
// is shorter.
//
// tooLate is a run with no room to wait at all. It is not a refusal --
// the call still posts the message and parks the task, which is the half
// worth having when there is no time for the other half -- it only says
// which of the two answers the report has to give.
//
// Unlike wait_for_checks this counts no wait credit into the room it has:
// a chat wait earns none. An interactive dispatch is given a longer clock
// up front instead (orchestrator.DefaultMaxInteractiveRuntime), because a
// session is mostly waiting by nature and crediting it back one turn at a
// time would be a budget for working stretched to cover something that is
// not working.
func chatWaitBudget(ctx context.Context) (wait time.Duration, tooLate bool) {
	remaining, ok := runDeadlineRemaining(ctx)
	if !ok {
		// Nobody told this server about a deadline -- a bare `grain
		// mcpserver`, a test -- which is not "no time left". The same
		// rule runDeadlineRemaining's own doc comment states.
		return ChatWait, false
	}
	room := remaining - chatWaitSlack
	switch {
	case room < minChatWait:
		return 0, true
	case room < ChatWait:
		return room, false
	}
	return ChatWait, false
}
