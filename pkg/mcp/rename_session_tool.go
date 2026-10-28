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
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/ai-on-gke/grain/pkg/model"
)

// MaxSessionTitleLength is the longest title rename_session will send, in
// runes -- model.ConsoleSessionTitleMaxLength, the bound the daemon holds
// a title to on the write (ui.Client.RenameConsoleSessionOfTask), stated
// here so a title that is too long is refused where the agent can still
// shorten it in the same turn rather than after a round trip.
//
// It is a display constraint, exactly as MaxStatusLength is: a session's
// name sits in the rail, on a task row and in a tab title, and one that
// runs past any of them is cut off there rather than read.
const MaxSessionTitleLength = model.ConsoleSessionTitleMaxLength

// SessionRenameReport is what one rename_session call did: the title as
// the daemon stored it, which is what the rail will show once the
// reconciler has carried it onto the session's task.
type SessionRenameReport struct {
	Title string
}

// SessionRenamer writes a new title onto the standing session the run
// this server serves is the agent of.
//
// An interface, and one that names no session, for the reason
// StatusReporter names no task: which session a title lands on is fixed
// at process start (`grain mcpserver -task`, a task that is a console
// session), never by anything in a tool call, so an agent can only ever
// rename its own chat. The real implementation (pkg/daemon/mcpserver.go's
// daemonSessionRenamer) asks the daemon over its REST API, since the
// settings row being written lives there.
type SessionRenamer interface {
	RenameSession(ctx context.Context, title string) (SessionRenameReport, error)
}

// NewRenameSessionTools returns the one tool a run gets when its task is
// a standing chat session (model.Task.IsConsoleSession): rename_session.
//
// It exists because a session started from a chat is named by a
// heuristic on its opening line (model.ConsoleSessionTitleFromMessage:
// the first sentence, cut to eight words), which is the best name
// anything can give a chat before the conversation has a shape. Once it
// has one, the thing that knows the shape is the agent on the other end,
// and this tool is how it says so: "hi, can you look at why the nightly
// is red" becomes "Nightly CI failure" in the rail and the sessions list
// by the end of the first exchange. It names a session once: the daemon
// refuses it once an agent or a person has named the session
// (model.ConsoleSession.TitleSource), and the description says so, so a
// later run whose turn is on another topic does not retitle the chat.
//
// The write goes through the same path PUT /api/console-sessions/{id}
// uses when a person edits the title (ui.Client.UpdateConsoleSession), so
// the reconciler carries it onto the task's title on its next cycle
// exactly as it does for a title typed into the settings pane. Nothing
// else about the session changes: the tool takes a title and nothing
// else, and the daemon refuses it for any task that is not a session.
//
// renamer nil returns the tool anyway, refusing every call -- what lets
// each agent framework's allowedTools enumerate the names this package
// registers without holding a live renamer, exactly as NewStatusTools
// does.
func NewRenameSessionTools(renamer SessionRenamer) []Tool {
	return []Tool{renameSessionTool(renamer)}
}

func renameSessionTool(renamer SessionRenamer) Tool {
	return Tool{
		Name: "rename_session",
		Description: "Name this chat session, once, from what the conversation turns out " +
			"to be about. A session started from a chat is auto-titled by a heuristic on " +
			"its first line, which is the best name anything can give it before the " +
			"conversation has a shape; you know the shape, so replace it with a short " +
			"overview of the subject, the way a chat app names a conversation -- " +
			"\"Nightly CI failure\", \"Granting api:ci-read to the release session\" -- " +
			"rather than the opening words. This is a one-time naming of an auto-titled " +
			"session: call it at most once, early in the first exchange. Once the session " +
			"has been named -- by you, an earlier run of it, or a person -- the name is " +
			"final and this tool refuses; do not retry it when the subject drifts. The " +
			"name shows in the rail, in the sessions list and on this task's row. It " +
			"changes nothing else about the session: not its id, its grants, or the " +
			"conversation. Keep it under " + fmt.Sprint(MaxSessionTitleLength) +
			" characters and on one line.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"title": map[string]any{
					"type": "string",
					"description": "The session's new title, at most " +
						fmt.Sprint(MaxSessionTitleLength) + " characters: a short " +
						"overview of what the conversation is about, not a sentence " +
						"from it.",
				},
			},
			"required": []string{"title"},
		},
		Handler: func(ctx context.Context, args map[string]any) Result {
			if renamer == nil {
				return Result{
					Text: "This run cannot rename its session (no route back to the grain " +
						"daemon that dispatched it, or this task is not a standing chat " +
						"session). Carry on with your work: nothing about the task " +
						"depends on it.",
					IsError: true,
				}
			}
			title, _ := argString(args, "title")
			title = strings.Join(strings.Fields(title), " ")
			switch {
			case title == "":
				return Result{Text: "title is required: a short overview of what this conversation is about",
					IsError: true}
			case utf8.RuneCountInString(title) > MaxSessionTitleLength:
				return Result{
					Text: fmt.Sprintf("That title is %d characters; keep it under %d so it fits in "+
						"the rail. Name the subject, not the request.",
						utf8.RuneCountInString(title), MaxSessionTitleLength),
					IsError: true,
				}
			}
			report, err := renamer.RenameSession(ctx, title)
			if err != nil {
				return Result{Text: err.Error(), IsError: true}
			}
			if report.Title == "" {
				report.Title = title
			}
			return Result{Text: fmt.Sprintf("This session is now titled %q. It shows in the rail and the "+
				"sessions list at once, and on this task's row on the next cycle. That name is "+
				"final: do not call this again.", report.Title)}
		},
	}
}
